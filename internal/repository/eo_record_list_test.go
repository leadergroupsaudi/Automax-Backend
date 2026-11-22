package repository

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// eoListRec creates one record in the given type with a values bag.
func eoListRec(t *testing.T, repo EORecordRepository, typeID uuid.UUID, ref string, mutate func(*models.EORecord)) *models.EORecord {
	t.Helper()
	rec := eoNewTestRecord(typeID, ref)
	rec.Values = datatypes.JSON([]byte(`{}`))
	if mutate != nil {
		mutate(rec)
	}
	if err := repo.Create(context.Background(), rec); err != nil {
		t.Fatalf("create %s: %v", ref, err)
	}
	return rec
}

func eoRefs(recs []models.EORecord) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.Reference
	}
	return out
}

// eoAdmin is the filter base for tests that are not about visibility.
func eoAdmin() models.EORecordFilter { return models.EORecordFilter{IsAdmin: true} }

func TestListFiltersByJSONBFieldValue(t *testing.T) {
	db := eoRecordDB(t)
	repo := NewEORecordRepository(db)
	typeID := uuid.New()
	eoListRec(t, repo, typeID, "R-1", func(r *models.EORecord) { r.Values = datatypes.JSON(`{"topic":"housing","n":1}`) })
	eoListRec(t, repo, typeID, "R-2", func(r *models.EORecord) { r.Values = datatypes.JSON(`{"topic":"water","n":2}`) })
	eoListRec(t, repo, typeID, "R-3", func(r *models.EORecord) { r.Values = datatypes.JSON(`{"topic":"housing","n":2}`) })

	f := eoAdmin()
	f.Values = map[string]any{"topic": "housing"}
	got, total, err := repo.List(context.Background(), &f)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if want := []string{"R-3", "R-1"}; !reflect.DeepEqual(eoRefs(got), want) || total != 2 {
		t.Errorf("topic=housing: refs %v total %d, want %v total 2", eoRefs(got), total, want)
	}

	f = eoAdmin()
	f.Values = map[string]any{"topic": "housing", "n": 2}
	got, _, err = repo.List(context.Background(), &f)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if want := []string{"R-3"}; !reflect.DeepEqual(eoRefs(got), want) {
		t.Errorf("topic=housing,n=2: refs %v, want %v (keys must AND)", eoRefs(got), want)
	}

	// The same predicate List builds must be servable by the GIN index. A
	// small table scans sequentially whatever indexes exist, so seqscan is
	// switched off for the transaction: that proves the index CAN serve it.
	// Plain index scans are off too, and the table is padded with rows that
	// do not match and analyzed: otherwise the planner may take the soft-delete
	// btree (deleted_at IS NULL matches every row) and apply the jsonb
	// predicate as a filter, which says nothing about the GIN index.
	filler := make([]models.EORecord, 2000)
	for i := range filler {
		filler[i] = *eoNewTestRecord(typeID, "F-"+uuid.NewString())
		filler[i].Values = datatypes.JSON(`{"topic":"other"}`)
	}
	if err := db.CreateInBatches(&filler, 500).Error; err != nil {
		t.Fatalf("pad table: %v", err)
	}
	if err := db.Exec(`ANALYZE eo_records`).Error; err != nil {
		t.Fatalf("analyze: %v", err)
	}
	f = eoAdmin()
	f.Values = map[string]any{"topic": "housing"}
	q, err := applyEORecordFilters(db.Session(&gorm.Session{DryRun: true}).Model(&models.EORecord{}), &f)
	if err != nil {
		t.Fatalf("build filters: %v", err)
	}
	stmt := q.Find(&[]models.EORecord{}).Statement
	var plan []string
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`SET LOCAL enable_seqscan = off`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`SET LOCAL enable_indexscan = off`).Error; err != nil {
			return err
		}
		// Not tx.Raw: GORM reads the "@" of the @> operator as a named
		// parameter and drops the bind variables.
		rows, err := tx.Statement.ConnPool.QueryContext(context.Background(), "EXPLAIN "+stmt.SQL.String(), stmt.Vars...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			plan = append(plan, line)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	// The partition's index inherits its name from the parent's GIN index on "values".
	if text := strings.Join(plan, "\n"); !strings.Contains(text, "Bitmap Index Scan on eo_records_default_values_idx") {
		t.Errorf("plan does not use the values GIN index:\n%s", text)
	}
}

func TestListFiltersByRecordTypeAndState(t *testing.T) {
	db := eoRecordDB(t)
	repo := NewEORecordRepository(db)
	typeA, typeB := uuid.New(), uuid.New()
	stateX, stateY := uuid.New(), uuid.New()
	eoListRec(t, repo, typeA, "A-X", func(r *models.EORecord) { r.StateID = &stateX })
	eoListRec(t, repo, typeA, "A-Y", func(r *models.EORecord) { r.StateID = &stateY })
	eoListRec(t, repo, typeB, "B-X", func(r *models.EORecord) { r.StateID = &stateX })

	list := func(mutate func(*models.EORecordFilter)) []string {
		f := eoAdmin()
		mutate(&f)
		got, _, err := repo.List(context.Background(), &f)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		refs := eoRefs(got)
		// order-insensitive comparison
		m := map[string]bool{}
		for _, r := range refs {
			m[r] = true
		}
		out := []string{}
		for _, r := range []string{"A-X", "A-Y", "B-X"} {
			if m[r] {
				out = append(out, r)
			}
		}
		return out
	}
	cases := []struct {
		name   string
		mutate func(*models.EORecordFilter)
		want   []string
	}{
		{"type A", func(f *models.EORecordFilter) { f.RecordTypeID = []string{typeA.String()} }, []string{"A-X", "A-Y"}},
		{"state X", func(f *models.EORecordFilter) { f.StateID = []string{stateX.String()} }, []string{"A-X", "B-X"}},
		{"type A and state X", func(f *models.EORecordFilter) {
			f.RecordTypeID = []string{typeA.String()}
			f.StateID = []string{stateX.String()}
		}, []string{"A-X"}},
		{"types A and B", func(f *models.EORecordFilter) { f.RecordTypeID = []string{typeA.String(), typeB.String()} }, []string{"A-X", "A-Y", "B-X"}},
		{"no filter", func(f *models.EORecordFilter) {}, []string{"A-X", "A-Y", "B-X"}},
	}
	for _, c := range cases {
		if got := list(c.mutate); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestListSearchAndPeriod(t *testing.T) {
	db := eoRecordDB(t)
	repo := NewEORecordRepository(db)
	typeID := uuid.New()
	due1 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	due2 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	eoListRec(t, repo, typeID, "S-1", func(r *models.EORecord) { r.SubjectEn = "Road works"; r.DueDate = &due1 })
	eoListRec(t, repo, typeID, "S-2", func(r *models.EORecord) { r.SubjectEn = "100% budget"; r.DueDate = &due2 })

	f := eoAdmin()
	f.Search = "road"
	got, _, err := repo.List(context.Background(), &f)
	if err != nil || !reflect.DeepEqual(eoRefs(got), []string{"S-1"}) {
		t.Errorf("search road: %v err %v", eoRefs(got), err)
	}
	f = eoAdmin()
	f.Search = "100%" // a wildcard in the term matches literally
	got, _, _ = repo.List(context.Background(), &f)
	if !reflect.DeepEqual(eoRefs(got), []string{"S-2"}) {
		t.Errorf("search 100%%: %v", eoRefs(got))
	}
	f = eoAdmin()
	f.Search = "%"
	if got, _, _ = repo.List(context.Background(), &f); len(got) != 1 {
		t.Errorf("search %%: got %v, want only the record containing a literal %%", eoRefs(got))
	}
	from := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	f = eoAdmin()
	f.DueFrom = &from
	got, _, _ = repo.List(context.Background(), &f)
	if !reflect.DeepEqual(eoRefs(got), []string{"S-2"}) {
		t.Errorf("due_from: %v", eoRefs(got))
	}
}

func TestListPaginatesWithoutMutatingTheFilter(t *testing.T) {
	db := eoRecordDB(t)
	repo := NewEORecordRepository(db)
	typeID := uuid.New()
	for _, ref := range []string{"P-1", "P-2", "P-3", "P-4", "P-5"} {
		eoListRec(t, repo, typeID, ref, nil)
	}

	f := eoAdmin() // Page and Limit zero: defaults apply
	got, total, err := repo.List(context.Background(), &f)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 5 || total != 5 {
		t.Errorf("defaults: %d rows total %d, want 5 and 5", len(got), total)
	}
	if f.Page != 0 || f.Limit != 0 {
		t.Errorf("List wrote defaults back into the filter: page %d limit %d", f.Page, f.Limit)
	}

	f = eoAdmin()
	f.Page, f.Limit, f.SortBy, f.SortDir = 2, 2, "reference", "asc"
	before := f
	got, total, err = repo.List(context.Background(), &f)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if want := []string{"P-3", "P-4"}; !reflect.DeepEqual(eoRefs(got), want) || total != 5 {
		t.Errorf("page 2 of 2: %v total %d, want %v total 5", eoRefs(got), total, want)
	}
	if !reflect.DeepEqual(f, before) {
		t.Errorf("List mutated the filter: %+v, was %+v", f, before)
	}

	// Reusing the filter for page 3 gives the last row, not a stale page.
	f.Page = 3
	got, _, _ = repo.List(context.Background(), &f)
	if want := []string{"P-5"}; !reflect.DeepEqual(eoRefs(got), want) {
		t.Errorf("page 3: %v, want %v", eoRefs(got), want)
	}

	// An out-of-range limit falls back to the default instead of fetching everything.
	f = eoAdmin()
	f.Limit = 1000
	got, _, _ = repo.List(context.Background(), &f)
	if len(got) != 5 || f.Limit != 1000 {
		t.Errorf("limit 1000: %d rows, filter limit %d", len(got), f.Limit)
	}
}

func TestListSortByIsAllowlisted(t *testing.T) {
	db := eoRecordDB(t)
	repo := NewEORecordRepository(db)
	typeID := uuid.New()
	eoListRec(t, repo, typeID, "O-2", nil)
	eoListRec(t, repo, typeID, "O-1", nil)

	for _, bad := range []string{
		"values", "id; DROP TABLE eo_records", "reference DESC; --",
		"(SELECT 1)", "subject_ar", "created_at, reference", "REFERENCE",
	} {
		f := eoAdmin()
		f.SortBy = bad
		if _, _, err := repo.List(context.Background(), &f); err == nil {
			t.Errorf("sort_by %q was accepted, want an error", bad)
		}
	}
	f := eoAdmin()
	f.SortDir = "asc; DROP TABLE eo_records"
	if _, _, err := repo.List(context.Background(), &f); err == nil {
		t.Errorf("sort_dir injection was accepted")
	}
	if !db.Migrator().HasTable("eo_records") {
		t.Fatalf("eo_records is gone")
	}

	for _, c := range []struct{ by, dir, first string }{
		{"reference", "asc", "O-1"}, {"reference", "desc", "O-2"},
		{"reference", "", "O-2"}, {"created_at", "asc", "O-2"}, {"", "", "O-1"},
		{"updated_at", "desc", "O-1"},
	} {
		f := eoAdmin()
		f.SortBy, f.SortDir = c.by, c.dir
		got, _, err := repo.List(context.Background(), &f)
		if err != nil || len(got) != 2 || got[0].Reference != c.first {
			t.Errorf("sort %q %q: %v err %v, want first %s", c.by, c.dir, eoRefs(got), err, c.first)
		}
	}
}

func TestListAppliesRowLevelVisibility(t *testing.T) {
	db := eoRecordDB(t)
	repo := NewEORecordRepository(db)
	typeID := uuid.New()
	bodyA, bodyB := uuid.New(), uuid.New()
	// "Confidential" must not matter: there is no clearance policy (§7.11).
	eoListRec(t, repo, typeID, "OPEN", func(r *models.EORecord) { r.Sensitivity = "Highly confidential" })
	eoListRec(t, repo, typeID, "FOR-A", func(r *models.EORecord) {
		r.Values = datatypes.JSON(`{"visible_to":"` + bodyA.String() + `"}`)
	})
	eoListRec(t, repo, typeID, "FOR-B", func(r *models.EORecord) {
		r.Values = datatypes.JSON(`{"visible_to":"` + bodyB.String() + `"}`)
	})

	as := func(admin bool, bodies ...uuid.UUID) ([]string, int64) {
		f := models.EORecordFilter{IsAdmin: admin, VisibleToDepartmentIDs: bodies, SortBy: "reference", SortDir: "asc"}
		got, total, err := repo.List(context.Background(), &f)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		return eoRefs(got), total
	}
	check := func(name string, got []string, total int64, want ...string) {
		t.Helper()
		if !reflect.DeepEqual(got, want) || total != int64(len(want)) {
			t.Errorf("%s: %v total %d, want %v", name, got, total, want)
		}
	}
	g, n := as(false, bodyA)
	check("body A", g, n, "FOR-A", "OPEN")
	g, n = as(false, bodyB)
	check("body B", g, n, "FOR-B", "OPEN")
	g, n = as(false, bodyA, bodyB)
	check("both bodies", g, n, "FOR-A", "FOR-B", "OPEN")
	g, n = as(false)
	check("no bodies", g, n, "OPEN")
	g, n = as(false, uuid.New())
	check("unrelated body", g, n, "OPEN")
	g, n = as(true)
	check("admin", g, n, "FOR-A", "FOR-B", "OPEN")

	// A filter cannot be used to reach a restricted row.
	f := models.EORecordFilter{VisibleToDepartmentIDs: []uuid.UUID{bodyA}, Values: map[string]any{"visible_to": bodyB.String()}}
	if got, total, _ := repo.List(context.Background(), &f); len(got) != 0 || total != 0 {
		t.Errorf("values filter on visible_to leaked %v", eoRefs(got))
	}
}

func TestListCreatorAlwaysSeesTheirRestrictedRecord(t *testing.T) {
	db := eoRecordDB(t)
	repo := NewEORecordRepository(db)
	typeID := uuid.New()
	referred, office, stranger := uuid.New(), uuid.New(), uuid.New()
	// The Office issues a referral restricted to another body.
	eoListRec(t, repo, typeID, "REFERRAL", func(r *models.EORecord) {
		r.CreatedByID = office
		r.Values = datatypes.JSON(`{"visible_to":"` + referred.String() + `"}`)
	})
	eoListRec(t, repo, typeID, "OPEN", nil)

	list := func(f models.EORecordFilter) []string {
		f.SortBy, f.SortDir = "reference", "asc"
		got, total, err := repo.List(context.Background(), &f)
		if err != nil || total != int64(len(got)) {
			t.Fatalf("list: total %d len %d err %v", total, len(got), err)
		}
		return eoRefs(got)
	}
	// Creator, in no referred body: sees it.
	if got := list(models.EORecordFilter{UserID: office, VisibleToDepartmentIDs: []uuid.UUID{uuid.New()}}); !reflect.DeepEqual(got, []string{"OPEN", "REFERRAL"}) {
		t.Errorf("creator: %v, want OPEN and REFERRAL", got)
	}
	// Unrelated user, in neither set: does not.
	if got := list(models.EORecordFilter{UserID: stranger}); !reflect.DeepEqual(got, []string{"OPEN"}) {
		t.Errorf("stranger: %v, want OPEN only", got)
	}
	// An unset UserID is not a creator of anything.
	if got := list(models.EORecordFilter{}); !reflect.DeepEqual(got, []string{"OPEN"}) {
		t.Errorf("zero filter: %v, want OPEN only", got)
	}
	// The referred body still sees it.
	if got := list(models.EORecordFilter{UserID: stranger, VisibleToDepartmentIDs: []uuid.UUID{referred}}); !reflect.DeepEqual(got, []string{"OPEN", "REFERRAL"}) {
		t.Errorf("referred body: %v", got)
	}
}
