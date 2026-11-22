package repository

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// eoRecordDB migrates the record storage (raw SQL, partitioned) and creates the
// metamodel tables (AutoMigrate's job in production) in one throwaway schema.
func eoRecordDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := eoMigratedDB(t)
	migrationDB := db.Session(&gorm.Session{})
	migrationDB.Config.DisableForeignKeyConstraintWhenMigrating = true
	if err := migrationDB.Migrator().CreateTable(&models.EORecordType{}, &models.EOFieldDef{}, &models.EOLinkType{}); err != nil {
		t.Fatalf("create metamodel tables: %v", err)
	}
	return db
}

func eoValuesMap(t *testing.T, raw datatypes.JSON) map[string]any {
	t.Helper()
	out := map[string]any{}
	if len(raw) == 0 {
		return out
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode values %s: %v", raw, err)
	}
	return out
}

func TestEORecordRoundTripsJSONBValues(t *testing.T) {
	db := eoRecordDB(t)
	repo := NewEORecordRepository(db)
	ctx := context.Background()

	typeID := uuid.New()
	rec := eoNewTestRecord(typeID, "EO-DIR-2026-100")
	rec.Values = datatypes.JSON([]byte(`{"text":"حالة","number":3.5,"flag":true,"list":[1,2,3],"object":{"a":"b"},"empty":null}`))
	// A clock the database must ignore: if created_at were written from Go
	// this value would survive the insert.
	rec.CreatedAt = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	rec.UpdatedAt = rec.CreatedAt

	if err := repo.Create(ctx, rec); err != nil {
		t.Fatalf("create: %v", err)
	}
	if rec.ID == uuid.Nil {
		t.Fatalf("Create did not assign an id")
	}

	got, err := repo.GetByID(ctx, rec.ID, typeID)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if got == nil {
		t.Fatalf("get by id returned no record")
	}
	want := eoValuesMap(t, rec.Values)
	if have := eoValuesMap(t, got.Values); !reflect.DeepEqual(have, want) {
		t.Errorf("values round trip = %#v, want %#v", have, want)
	}
	if got.Reference != rec.Reference || got.SubjectAr != rec.SubjectAr {
		t.Errorf("read back reference %q subject_ar %q, want %q and %q", got.Reference, got.SubjectAr, rec.Reference, rec.SubjectAr)
	}

	// created_at comes from the one server clock, not from the application
	// host's, and the inserted struct is updated with the stored value.
	var dbNow time.Time
	if err := db.Raw(`SELECT NOW()`).Scan(&dbNow).Error; err != nil {
		t.Fatalf("read db clock: %v", err)
	}
	for _, c := range []struct {
		name string
		got  time.Time
	}{{"returned struct", rec.CreatedAt}, {"stored row", got.CreatedAt}} {
		if d := dbNow.Sub(c.got); d < -time.Minute || d > time.Minute {
			t.Errorf("%s created_at = %s, want within a minute of the database clock %s", c.name, c.got, dbNow)
		}
	}
}

func TestEORecordUpdateValuesMergesRatherThanReplaces(t *testing.T) {
	db := eoRecordDB(t)
	repo := NewEORecordRepository(db)
	ctx := context.Background()

	typeID := uuid.New()
	rec := eoNewTestRecord(typeID, "EO-DIR-2026-110")
	rec.Values = datatypes.JSON([]byte(`{"kept":"original","changed":"before","nested":{"a":1}}`))
	if err := repo.Create(ctx, rec); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := repo.UpdateValues(ctx, rec.ID, typeID, map[string]any{
		"changed": "after",
		"added":   7.0,
	}); err != nil {
		t.Fatalf("update values: %v", err)
	}

	got, err := repo.GetByID(ctx, rec.ID, typeID)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	want := map[string]any{
		"kept":    "original",
		"changed": "after",
		"nested":  map[string]any{"a": 1.0},
		"added":   7.0,
	}
	if have := eoValuesMap(t, got.Values); !reflect.DeepEqual(have, want) {
		t.Errorf("values after partial update = %#v, want %#v", have, want)
	}
}

func TestEORecordUpdateWritesClearedColumns(t *testing.T) {
	db := eoRecordDB(t)
	repo := NewEORecordRepository(db)
	ctx := context.Background()

	typeID := uuid.New()
	due := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)
	assignee := uuid.New()
	rec := eoNewTestRecord(typeID, "EO-DIR-2026-120")
	rec.DueDate = &due
	rec.AssignedUserID = &assignee
	rec.Priority = "high"
	rec.SubjectEn = "subject"
	if err := repo.Create(ctx, rec); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Clearing a field is an ordinary edit: a struct-based GORM update would
	// skip every zero value and silently keep the old ones.
	rec.DueDate = nil
	rec.AssignedUserID = nil
	rec.Priority = ""
	rec.SubjectEn = ""
	rec.Values = datatypes.JSON([]byte(`{"only":"this"}`))
	if err := repo.Update(ctx, rec); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := repo.GetByID(ctx, rec.ID, typeID)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if got.DueDate != nil {
		t.Errorf("due_date = %v, want nil", got.DueDate)
	}
	if got.AssignedUserID != nil {
		t.Errorf("assigned_user_id = %v, want nil", got.AssignedUserID)
	}
	if got.Priority != "" {
		t.Errorf("priority = %q, want empty", got.Priority)
	}
	if got.SubjectEn != "" {
		t.Errorf("subject_en = %q, want empty", got.SubjectEn)
	}
	if have, want := eoValuesMap(t, got.Values), map[string]any{"only": "this"}; !reflect.DeepEqual(have, want) {
		t.Errorf("values = %#v, want %#v", have, want)
	}
	if !got.UpdatedAt.After(got.CreatedAt) {
		t.Errorf("updated_at = %s, want it moved past created_at %s", got.UpdatedAt, got.CreatedAt)
	}
}

func TestEORecordGetsNeedBothKeyColumns(t *testing.T) {
	db := eoRecordDB(t)
	repo := NewEORecordRepository(db)
	ctx := context.Background()

	typeID := uuid.New()
	rec := eoNewTestRecord(typeID, "EO-DIR-2026-130")
	if err := repo.Create(ctx, rec); err != nil {
		t.Fatalf("create: %v", err)
	}

	t.Run("wrong record type is not found", func(t *testing.T) {
		got, err := repo.GetByID(ctx, rec.ID, uuid.New())
		if err != nil {
			t.Fatalf("get by id: %v", err)
		}
		if got != nil {
			t.Errorf("got %q under a record type it does not belong to, want no record", got.Reference)
		}
	})
	t.Run("absent id is not an error", func(t *testing.T) {
		got, err := repo.GetByID(ctx, uuid.New(), typeID)
		if err != nil {
			t.Fatalf("get by id: %v", err)
		}
		if got != nil {
			t.Errorf("got %q for an id that was never created", got.Reference)
		}
	})
	t.Run("by id alone across partitions", func(t *testing.T) {
		got, err := repo.GetAnyByID(ctx, rec.ID)
		if err != nil {
			t.Fatalf("get any by id: %v", err)
		}
		if got == nil || got.RecordTypeID != typeID {
			t.Fatalf("get any by id = %+v, want the record with its own type", got)
		}
	})
	t.Run("by reference", func(t *testing.T) {
		got, err := repo.GetByReference(ctx, rec.Reference)
		if err != nil {
			t.Fatalf("get by reference: %v", err)
		}
		if got == nil || got.ID != rec.ID {
			t.Fatalf("get by reference = %+v, want record %s", got, rec.ID)
		}
		missing, err := repo.GetByReference(ctx, "EO-NOPE-2026-001")
		if err != nil {
			t.Fatalf("get by unknown reference: %v", err)
		}
		if missing != nil {
			t.Errorf("got %+v for an unissued reference, want no record", missing)
		}
	})
}

func TestEORecordChildrenAreDirectOnly(t *testing.T) {
	db := eoRecordDB(t)
	repo := NewEORecordRepository(db)
	ctx := context.Background()

	taskType, referralType := uuid.New(), uuid.New()
	parent := eoNewTestRecord(uuid.New(), "EO-DIR-2026-140")
	if err := repo.Create(ctx, parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	child := eoNewTestRecord(taskType, "EO-DIR-2026-140-A1")
	child.ParentRecordID = &parent.ID
	referral := eoNewTestRecord(referralType, "EO-DIR-2026-140-R1")
	referral.ParentRecordID = &parent.ID
	for _, r := range []*models.EORecord{child, referral} {
		if err := repo.Create(ctx, r); err != nil {
			t.Fatalf("create child %s: %v", r.Reference, err)
		}
	}
	// A grandchild is a descendant, not a child: blocking rules identify a
	// child by membership in this slice, so a transitive set would silently
	// stop blocking.
	grandchild := eoNewTestRecord(taskType, "EO-DIR-2026-140-A1-T1")
	grandchild.ParentRecordID = &child.ID
	if err := repo.Create(ctx, grandchild); err != nil {
		t.Fatalf("create grandchild: %v", err)
	}
	deleted := eoNewTestRecord(taskType, "EO-DIR-2026-140-A2")
	deleted.ParentRecordID = &parent.ID
	if err := repo.Create(ctx, deleted); err != nil {
		t.Fatalf("create soft-deleted child: %v", err)
	}
	if err := db.Where("id = ? AND record_type_id = ?", deleted.ID, deleted.RecordTypeID).
		Delete(&models.EORecord{}).Error; err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	t.Run("every type", func(t *testing.T) {
		got, err := repo.Children(ctx, parent.ID, nil)
		if err != nil {
			t.Fatalf("children: %v", err)
		}
		if refs := eoReferences(got); !reflect.DeepEqual(refs, []string{child.Reference, referral.Reference}) {
			t.Errorf("children = %v, want the two live direct children", refs)
		}
	})
	t.Run("one type", func(t *testing.T) {
		got, err := repo.Children(ctx, parent.ID, &taskType)
		if err != nil {
			t.Fatalf("children: %v", err)
		}
		if refs := eoReferences(got); !reflect.DeepEqual(refs, []string{child.Reference}) {
			t.Errorf("children of one type = %v, want only %q", refs, child.Reference)
		}
	})
}

func eoReferences(recs []models.EORecord) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.Reference)
	}
	return out
}

func TestEORecordLinksFromReturnsOutgoingLinks(t *testing.T) {
	db := eoRecordDB(t)
	repo := NewEORecordRepository(db)
	ctx := context.Background()

	a := eoNewTestRecord(uuid.New(), "EO-DIR-2026-150")
	b := eoNewTestRecord(uuid.New(), "EO-TSK-2026-150")
	c := eoNewTestRecord(uuid.New(), "EO-TSK-2026-151")
	for _, r := range []*models.EORecord{a, b, c} {
		if err := repo.Create(ctx, r); err != nil {
			t.Fatalf("create %s: %v", r.Reference, err)
		}
	}
	outgoing := &models.EORecordLink{
		FromRecordID: a.ID, FromRecordTypeID: a.RecordTypeID,
		ToRecordID: b.ID, ToRecordTypeID: b.RecordTypeID,
		LinkTypeKey: "spawned_task",
	}
	incoming := &models.EORecordLink{
		FromRecordID: c.ID, FromRecordTypeID: c.RecordTypeID,
		ToRecordID: a.ID, ToRecordTypeID: a.RecordTypeID,
		LinkTypeKey: "parent_record",
	}
	for _, l := range []*models.EORecordLink{outgoing, incoming} {
		if err := repo.CreateLink(ctx, l); err != nil {
			t.Fatalf("create link %s: %v", l.LinkTypeKey, err)
		}
	}

	got, err := repo.LinksFrom(ctx, a.ID)
	if err != nil {
		t.Fatalf("links from: %v", err)
	}
	if len(got) != 1 || got[0].ID != outgoing.ID {
		t.Fatalf("links from a = %+v, want only the outgoing spawned_task link", got)
	}
	if got[0].ToRecordTypeID != b.RecordTypeID {
		t.Errorf("to_record_type_id = %s, want %s: the resolver reads the type from the link row", got[0].ToRecordTypeID, b.RecordTypeID)
	}
}

func TestEORecordEnteredStateIDsComeFromHistory(t *testing.T) {
	db := eoRecordDB(t)
	repo := NewEORecordRepository(db)
	ctx := context.Background()

	draft, review, current := uuid.New(), uuid.New(), uuid.New()
	rec := eoNewTestRecord(uuid.New(), "EO-DIR-2026-160")
	// The current state is a different question from "ever entered": EO
	// workflows have return and exception paths, so a record can sit in a
	// state its history does not yet record as entered.
	rec.StateID = &current
	if err := repo.Create(ctx, rec); err != nil {
		t.Fatalf("create: %v", err)
	}
	entries := []*models.EORecordHistory{
		{RecordID: rec.ID, RecordTypeID: rec.RecordTypeID, ToStateID: &draft},
		{RecordID: rec.ID, RecordTypeID: rec.RecordTypeID, FromStateID: &draft, ToStateID: &review},
		{RecordID: rec.ID, RecordTypeID: rec.RecordTypeID, FromStateID: &review, ToStateID: &draft},
		{RecordID: rec.ID, RecordTypeID: rec.RecordTypeID, Comment: "a field edit, no transition"},
	}
	for i, e := range entries {
		if err := repo.CreateHistory(ctx, e); err != nil {
			t.Fatalf("create history %d: %v", i, err)
		}
	}
	// Another record's history must not leak in.
	other := eoNewTestRecord(rec.RecordTypeID, "EO-DIR-2026-161")
	if err := repo.Create(ctx, other); err != nil {
		t.Fatalf("create other: %v", err)
	}
	stranger := uuid.New()
	if err := repo.CreateHistory(ctx, &models.EORecordHistory{
		RecordID: other.ID, RecordTypeID: other.RecordTypeID, ToStateID: &stranger,
	}); err != nil {
		t.Fatalf("create other history: %v", err)
	}

	got, err := repo.EnteredStateIDs(ctx, rec.ID, rec.RecordTypeID)
	if err != nil {
		t.Fatalf("entered state ids: %v", err)
	}
	want := map[uuid.UUID]bool{draft: true, review: true}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("entered states = %v, want %v (draft once, review once, no current state, no stranger)", got, want)
	}
}

func TestEORecordTransactionRollsBackEveryWrite(t *testing.T) {
	db := eoRecordDB(t)
	repo := NewEORecordRepository(db)
	ctx := context.Background()

	typeID := uuid.New()
	rec := eoNewTestRecord(typeID, "EO-DIR-2026-170")
	boom := errors.New("link rules failed after the record was written")

	err := repo.Transaction(ctx, func(tx EORecordRepository, _ EORecordTypeRepository) error {
		if err := tx.Create(ctx, rec); err != nil {
			return err
		}
		if err := tx.CreateHistory(ctx, &models.EORecordHistory{
			RecordID: rec.ID, RecordTypeID: typeID, Comment: "created",
		}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("transaction error = %v, want %v", err, boom)
	}

	got, err := repo.GetByID(ctx, rec.ID, typeID)
	if err != nil {
		t.Fatalf("get by id: %v", err)
	}
	if got != nil {
		t.Errorf("record %s survived a rolled back transaction", got.Reference)
	}
	var history int64
	if err := db.Model(&models.EORecordHistory{}).Where("record_id = ?", rec.ID).Count(&history).Error; err != nil {
		t.Fatalf("count history: %v", err)
	}
	if history != 0 {
		t.Errorf("history rows after rollback = %d, want 0", history)
	}

	// And a transaction that returns nil commits everything.
	committed := eoNewTestRecord(typeID, "EO-DIR-2026-171")
	if err := repo.Transaction(ctx, func(tx EORecordRepository, _ EORecordTypeRepository) error {
		return tx.Create(ctx, committed)
	}); err != nil {
		t.Fatalf("committing transaction: %v", err)
	}
	if got, err := repo.GetByID(ctx, committed.ID, typeID); err != nil || got == nil {
		t.Errorf("committed record not found: %+v, %v", got, err)
	}
}

func TestEORecordTypeRepositoryResolvesTheTypeGraph(t *testing.T) {
	db := eoRecordDB(t)
	repo := NewEORecordTypeRepository(db)
	ctx := context.Background()

	base := &models.EORecordType{Key: "eo_base", Name: "Base", IsAbstract: true, ReferencePrefix: "EO"}
	if err := db.Create(base).Error; err != nil {
		t.Fatalf("create base type: %v", err)
	}
	mid := &models.EORecordType{Key: "eo_mid", Name: "Mid", ParentTypeID: &base.ID, ReferencePrefix: "EO-MID"}
	if err := db.Create(mid).Error; err != nil {
		t.Fatalf("create mid type: %v", err)
	}
	leaf := &models.EORecordType{Key: "eo_leaf", Name: "Leaf", ParentTypeID: &mid.ID, ReferencePrefix: "EO-LEAF"}
	if err := db.Create(leaf).Error; err != nil {
		t.Fatalf("create leaf type: %v", err)
	}
	unrelated := &models.EORecordType{Key: "eo_other", Name: "Other", ReferencePrefix: "EO-OTH"}
	if err := db.Create(unrelated).Error; err != nil {
		t.Fatalf("create unrelated type: %v", err)
	}
	defs := []*models.EOFieldDef{
		{RecordTypeID: base.ID, Key: "subject_ar", Label: "Subject", Kind: models.EOKindText},
		{RecordTypeID: leaf.ID, Key: "weeks", Label: "Weeks", Kind: models.EOKindNumber},
		{RecordTypeID: unrelated.ID, Key: "noise", Label: "Noise", Kind: models.EOKindText},
	}
	for _, d := range defs {
		if err := db.Create(d).Error; err != nil {
			t.Fatalf("create field def %s: %v", d.Key, err)
		}
	}

	t.Run("by id and key", func(t *testing.T) {
		byID, err := repo.GetByID(ctx, leaf.ID)
		if err != nil || byID == nil || byID.Key != "eo_leaf" {
			t.Fatalf("get by id = %+v, %v", byID, err)
		}
		byKey, err := repo.GetByKey(ctx, "eo_mid")
		if err != nil || byKey == nil || byKey.ID != mid.ID {
			t.Fatalf("get by key = %+v, %v", byKey, err)
		}
		missing, err := repo.GetByKey(ctx, "eo_nothing")
		if err != nil {
			t.Fatalf("get by unknown key: %v", err)
		}
		if missing != nil {
			t.Errorf("get by unknown key = %+v, want no type", missing)
		}
	})

	t.Run("ancestors nearest first", func(t *testing.T) {
		got, err := repo.Ancestors(ctx, leaf)
		if err != nil {
			t.Fatalf("ancestors: %v", err)
		}
		var keys []string
		for _, a := range got {
			keys = append(keys, a.Key)
		}
		if !reflect.DeepEqual(keys, []string{"eo_mid", "eo_base"}) {
			t.Errorf("ancestors = %v, want [eo_mid eo_base]", keys)
		}
		root, err := repo.Ancestors(ctx, base)
		if err != nil {
			t.Fatalf("ancestors of a root type: %v", err)
		}
		if len(root) != 0 {
			t.Errorf("ancestors of a root type = %v, want none", root)
		}
	})

	t.Run("field defs of the named types only", func(t *testing.T) {
		got, err := repo.FieldDefs(ctx, []uuid.UUID{leaf.ID, base.ID})
		if err != nil {
			t.Fatalf("field defs: %v", err)
		}
		keys := map[string]bool{}
		for _, d := range got {
			keys[d.Key] = true
		}
		if !keys["subject_ar"] || !keys["weeks"] {
			t.Errorf("field defs = %v, want the base's and the leaf's", keys)
		}
		if keys["noise"] {
			t.Errorf("field defs include an unrelated type's field")
		}
		if none, err := repo.FieldDefs(ctx, nil); err != nil || len(none) != 0 {
			t.Errorf("field defs of no types = %v, %v, want none and no error", none, err)
		}
	})

	t.Run("link type by key", func(t *testing.T) {
		lt := &models.EOLinkType{Key: "covers_item", Label: "Covers", CascadeSensitivity: true}
		if err := db.Create(lt).Error; err != nil {
			t.Fatalf("create link type: %v", err)
		}
		got, err := repo.GetLinkTypeByKey(ctx, "covers_item")
		if err != nil || got == nil {
			t.Fatalf("get link type = %+v, %v", got, err)
		}
		if !got.CascadeSensitivity {
			t.Errorf("cascade_sensitivity = false, want it read back from the row")
		}
		missing, err := repo.GetLinkTypeByKey(ctx, "no_such_link")
		if err != nil {
			t.Fatalf("get unknown link type: %v", err)
		}
		if missing != nil {
			t.Errorf("get unknown link type = %+v, want no link type", missing)
		}
	})
}

func TestEORecordTypeAncestorsRejectACycle(t *testing.T) {
	db := eoRecordDB(t)
	repo := NewEORecordTypeRepository(db)
	ctx := context.Background()

	a := &models.EORecordType{Key: "eo_cycle_a", Name: "A", ReferencePrefix: "EO-CA"}
	if err := db.Create(a).Error; err != nil {
		t.Fatalf("create a: %v", err)
	}
	b := &models.EORecordType{Key: "eo_cycle_b", Name: "B", ParentTypeID: &a.ID, ReferencePrefix: "EO-CB"}
	if err := db.Create(b).Error; err != nil {
		t.Fatalf("create b: %v", err)
	}
	if err := db.Model(&models.EORecordType{}).Where("id = ?", a.ID).Update("parent_type_id", b.ID).Error; err != nil {
		t.Fatalf("close the cycle: %v", err)
	}
	a.ParentTypeID = &b.ID

	if _, err := repo.Ancestors(ctx, a); err == nil {
		t.Errorf("ancestors of a cyclic type returned no error, want one rather than a loop")
	}
}
