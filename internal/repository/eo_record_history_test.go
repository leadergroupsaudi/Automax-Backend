package repository

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// eoHistoryEntry writes one audit row at an EXACT timestamp. The timestamp is
// supplied rather than defaulted because the failure this file exists to catch
// only appears when rows SHARE one.
func eoHistoryEntry(t *testing.T, repo EORecordRepository, rec *models.EORecord, at time.Time, comment string) *models.EORecordHistory {
	t.Helper()
	entry := &models.EORecordHistory{
		ID:           uuid.New(),
		RecordID:     rec.ID,
		RecordTypeID: rec.RecordTypeID,
		Comment:      comment,
		OldValues:    datatypes.JSON([]byte(`{}`)),
		NewValues:    datatypes.JSON([]byte(`{}`)),
		CreatedAt:    at,
	}
	if err := repo.CreateHistory(context.Background(), entry); err != nil {
		t.Fatalf("create history %q: %v", comment, err)
	}
	return entry
}

func eoHistoryComments(rows []models.EORecordHistoryEntry) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Comment
	}
	return out
}

// TestEORecordHistoryIsNewestFirstWithAStableTiebreak.
//
// eo_record_history rows are written on create (two or three in one
// transaction) and on every transition, so SEVERAL ROWS SHARE A TIMESTAMP.
// created_at DESC alone is therefore not a total order: Postgres may return
// tied rows in any order, and it need not return the same order twice. Under
// LIMIT/OFFSET paging that is not a cosmetic wobble — a row can appear on two
// pages and another on none, so a client paging the trail silently loses
// entries from an audit log.
//
// The tiebreak is the id, ascending, the same shape eoOrderClause already uses
// for the register.
func TestEORecordHistoryIsNewestFirstWithAStableTiebreak(t *testing.T) {
	db := eoRecordDB(t)
	repo := NewEORecordRepository(db)
	ctx := context.Background()
	typeID := uuid.New()
	rec := eoListRec(t, repo, typeID, "H-1", nil)

	// Three distinct instants; the middle one carries FOUR rows, as a create
	// followed by a transition in the same transaction does.
	newest := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	tied := newest.Add(-time.Hour)
	oldest := newest.Add(-2 * time.Hour)

	eoHistoryEntry(t, repo, rec, newest, "newest")
	tiedRows := []*models.EORecordHistory{
		eoHistoryEntry(t, repo, rec, tied, "tied-a"),
		eoHistoryEntry(t, repo, rec, tied, "tied-b"),
		eoHistoryEntry(t, repo, rec, tied, "tied-c"),
		eoHistoryEntry(t, repo, rec, tied, "tied-d"),
	}
	eoHistoryEntry(t, repo, rec, oldest, "oldest")

	// What the tiebreak must produce for the four tied rows: id ascending.
	wantTied := make([]string, 0, len(tiedRows))
	for _, r := range tiedRows {
		wantTied = append(wantTied, r.Comment)
	}
	byID := map[string]uuid.UUID{}
	for _, r := range tiedRows {
		byID[r.Comment] = r.ID
	}
	for i := 0; i < len(wantTied); i++ {
		for j := i + 1; j < len(wantTied); j++ {
			if byID[wantTied[j]].String() < byID[wantTied[i]].String() {
				wantTied[i], wantTied[j] = wantTied[j], wantTied[i]
			}
		}
	}
	want := append([]string{"newest"}, wantTied...)
	want = append(want, "oldest")

	t.Run("one page is newest first, ties broken by id", func(t *testing.T) {
		rows, total, err := repo.History(ctx, rec.ID, rec.RecordTypeID, 1, 100)
		if err != nil {
			t.Fatalf("history: %v", err)
		}
		if total != 6 {
			t.Errorf("total = %d, want 6", total)
		}
		got := eoHistoryComments(rows)
		if len(got) != len(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("order = %v, want %v (newest first, id ascending within a tie)", got, want)
			}
		}
	})

	t.Run("paging over tied rows drops and duplicates nothing", func(t *testing.T) {
		// Three pages of two, straight across the four-row tie. Run twice:
		// an unstable sort need not be wrong on the first attempt.
		for attempt := 0; attempt < 2; attempt++ {
			var paged []string
			for page := 1; page <= 3; page++ {
				rows, total, err := repo.History(ctx, rec.ID, rec.RecordTypeID, page, 2)
				if err != nil {
					t.Fatalf("history page %d: %v", page, err)
				}
				if total != 6 {
					t.Errorf("page %d total = %d, want 6", page, total)
				}
				if len(rows) != 2 {
					t.Fatalf("page %d has %d rows, want 2", page, len(rows))
				}
				paged = append(paged, eoHistoryComments(rows)...)
			}
			for i := range want {
				if paged[i] != want[i] {
					t.Fatalf("attempt %d: paged %v, want %v — paging an unstable order drops and duplicates audit rows",
						attempt, paged, want)
				}
			}
		}
	})

	t.Run("a page past the end is empty, and the total still counts them all", func(t *testing.T) {
		rows, total, err := repo.History(ctx, rec.ID, rec.RecordTypeID, 9, 2)
		if err != nil {
			t.Fatalf("history: %v", err)
		}
		if len(rows) != 0 {
			t.Errorf("rows = %v, want none past the end", eoHistoryComments(rows))
		}
		if total != 6 {
			t.Errorf("total = %d, want 6", total)
		}
	})
}

// TestEORecordHistoryIsScopedToOneRecord: the trail is per record, and the
// table is keyed by (record_id, record_type_id).
//
// eo_records' primary key is COMPOSITE because Postgres requires the partition
// key in every unique constraint, so the SAME id may exist in two partitions —
// which is exactly what this test creates. A history query on record_id alone
// would pass against ordinary fixtures and hand one register's audit trail to
// the other's reader here.
func TestEORecordHistoryIsScopedToOneRecord(t *testing.T) {
	db := eoRecordDB(t)
	repo := NewEORecordRepository(db)
	ctx := context.Background()
	typeA, typeB := uuid.New(), uuid.New()
	sharedID := uuid.New()

	mine := eoListRec(t, repo, typeA, "H-MINE", func(r *models.EORecord) { r.ID = sharedID })
	theirs := eoListRec(t, repo, typeB, "H-THEIRS", func(r *models.EORecord) { r.ID = sharedID })
	if mine.ID != theirs.ID {
		t.Fatalf("the fixture did not produce two records sharing an id: %s vs %s", mine.ID, theirs.ID)
	}

	at := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	eoHistoryEntry(t, repo, mine, at, "mine-1")
	eoHistoryEntry(t, repo, mine, at.Add(-time.Minute), "mine-2")
	eoHistoryEntry(t, repo, theirs, at, "theirs-1")

	rows, total, err := repo.History(ctx, mine.ID, mine.RecordTypeID, 1, 100)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if total != 2 {
		t.Errorf("total = %d, want 2 — the other register's trail was counted", total)
	}
	for _, r := range rows {
		if r.RecordTypeID != mine.RecordTypeID {
			t.Errorf("row %q belongs to type %s, want %s", r.Comment, r.RecordTypeID, mine.RecordTypeID)
		}
	}
	if got := eoHistoryComments(rows); len(got) != 2 {
		t.Errorf("rows = %v, want the two of this record only", got)
	}

	// A record with no trail is an empty answer, not an error.
	empty := eoListRec(t, repo, typeA, "H-EMPTY", nil)
	rows, total, err = repo.History(ctx, empty.ID, empty.RecordTypeID, 1, 100)
	if err != nil {
		t.Fatalf("history of a record with none: %v", err)
	}
	if len(rows) != 0 || total != 0 {
		t.Errorf("rows = %v total = %d, want none and 0", eoHistoryComments(rows), total)
	}
}

// TestEORecordHistoryClampsPagingLikeTheRegister: the repository is the second
// clamp, as it is for List. The handler clamps first, but a caller inside the
// process (a report, a later export) reaches this directly, and a zero limit
// that got through would be an unbounded read of an audit table.
func TestEORecordHistoryClampsPagingLikeTheRegister(t *testing.T) {
	db := eoRecordDB(t)
	repo := NewEORecordRepository(db)
	ctx := context.Background()
	rec := eoListRec(t, repo, uuid.New(), "H-CLAMP", nil)

	at := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	for i := 0; i < eoDefaultLimit+5; i++ {
		eoHistoryEntry(t, repo, rec, at.Add(-time.Duration(i)*time.Minute), "e")
	}

	for _, tc := range []struct {
		name        string
		page, limit int
		wantRows    int
	}{
		{"limit 0 falls back to the default", 1, 0, eoDefaultLimit},
		{"a negative limit falls back to the default", 1, -1, eoDefaultLimit},
		{"a limit above the maximum falls back to the default", 1, eoMaxLimit + 1, eoDefaultLimit},
		{"page 0 is the first page", 0, 5, 5},
		{"a negative page is the first page", -3, 5, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, total, err := repo.History(ctx, rec.ID, rec.RecordTypeID, tc.page, tc.limit)
			if err != nil {
				t.Fatalf("history: %v", err)
			}
			if total != int64(eoDefaultLimit+5) {
				t.Errorf("total = %d, want %d", total, eoDefaultLimit+5)
			}
			if len(rows) != tc.wantRows {
				t.Errorf("rows = %d, want %d", len(rows), tc.wantRows)
			}
		})
	}

	// A clamped page must be the FIRST page, not an offset computed from the
	// unclamped number — page 0 with OFFSET (0-1)*limit is a negative offset,
	// which Postgres rejects outright.
	first, _, err := repo.History(ctx, rec.ID, rec.RecordTypeID, 1, 5)
	if err != nil {
		t.Fatalf("history page 1: %v", err)
	}
	zeroth, _, err := repo.History(ctx, rec.ID, rec.RecordTypeID, 0, 5)
	if err != nil {
		t.Fatalf("history page 0: %v", err)
	}
	for i := range first {
		if first[i].ID != zeroth[i].ID {
			t.Fatalf("page 0 row %d = %s, want page 1's %s", i, zeroth[i].ID, first[i].ID)
		}
	}
}

// ── the display names the trail is read with ────────────────────────────────

// eoHistoryDB is eoRecordDB plus the two tables the history read RESOLVES
// NAMES FROM. They are created here and not in eoRecordDB because only this
// read needs them, and a test schema should say what the code under it
// touches.
//
// users needs the user_call_status enum first: models.User declares a column
// of that type, and CreateTable fails without it (the same two lines
// internal/database and internal/services already carry).
func eoHistoryDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := eoRecordDB(t)
	if err := db.Exec(`CREATE TYPE user_call_status AS ENUM ('offline', 'online', 'busy', 'in_call', 'available')`).Error; err != nil {
		t.Fatalf("create user_call_status enum: %v", err)
	}
	m := db.Session(&gorm.Session{})
	m.Config.DisableForeignKeyConstraintWhenMigrating = true
	if err := m.Migrator().CreateTable(&models.WorkflowState{}, &models.User{}); err != nil {
		t.Fatalf("create the name tables: %v", err)
	}
	return db
}

func eoHistoryUser(t *testing.T, db *gorm.DB, first, middle, last, username string) *models.User {
	t.Helper()
	u := &models.User{
		ID: uuid.New(), FirstName: first, MiddleName: middle, LastName: last,
		Username: username, Email: username + "@eo.gov", Password: "x",
	}
	if err := db.Create(u).Error; err != nil {
		t.Fatalf("create user %q: %v", username, err)
	}
	return u
}

func eoHistoryState(t *testing.T, db *gorm.DB, name, nameAr string) *models.WorkflowState {
	t.Helper()
	s := &models.WorkflowState{ID: uuid.New(), WorkflowID: uuid.New(), Name: name, NameAr: nameAr, Code: name}
	if err := db.Create(s).Error; err != nil {
		t.Fatalf("create state %q: %v", name, err)
	}
	return s
}

// TestEORecordHistoryResolvesActorAndStateNames.
//
// The consumer cannot resolve these ids itself — no route turns a user id into
// a name — so a trail served as ids alone reads "user 3f2a… moved 9b1c… →
// 7d4e…" and fails the officer the record detail page is for. The names are
// added BESIDE the ids, never in place of them.
func TestEORecordHistoryResolvesActorAndStateNames(t *testing.T) {
	db := eoHistoryDB(t)
	repo := NewEORecordRepository(db)
	ctx := context.Background()
	rec := eoListRec(t, repo, uuid.New(), "H-NAMES", nil)

	actor := eoHistoryUser(t, db, "Layla", "A.", "Mansour", "lmansour")
	from := eoHistoryState(t, db, "Under Review", "قيد المراجعة")
	to := eoHistoryState(t, db, "Approved", "معتمد")

	at := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	entry := eoHistoryEntry(t, repo, rec, at, "moved")
	if err := db.Model(&models.EORecordHistory{}).Where("id = ?", entry.ID).
		Updates(map[string]any{"performed_by_id": actor.ID, "from_state_id": from.ID, "to_state_id": to.ID}).Error; err != nil {
		t.Fatalf("attach ids: %v", err)
	}

	rows, _, err := repo.History(ctx, rec.ID, rec.RecordTypeID, 1, 100)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	got := rows[0]

	if got.PerformedByName != "Layla A. Mansour" {
		t.Errorf("performed_by_name = %q, want %q", got.PerformedByName, "Layla A. Mansour")
	}
	// BILINGUAL, the convention WorkflowState and EORecordType already use.
	if got.FromStateName != "Under Review" || got.FromStateNameAr != "قيد المراجعة" {
		t.Errorf("from state = %q / %q, want %q / %q", got.FromStateName, got.FromStateNameAr, "Under Review", "قيد المراجعة")
	}
	if got.ToStateName != "Approved" || got.ToStateNameAr != "معتمد" {
		t.Errorf("to state = %q / %q, want %q / %q", got.ToStateName, got.ToStateNameAr, "Approved", "معتمد")
	}
	// THE IDS SURVIVE: a consumer may key on them, and losing one from an
	// audit payload would be a lossy change.
	if got.PerformedByID == nil || *got.PerformedByID != actor.ID {
		t.Errorf("performed_by_id = %v, want %s", got.PerformedByID, actor.ID)
	}
	if got.FromStateID == nil || *got.FromStateID != from.ID {
		t.Errorf("from_state_id = %v, want %s", got.FromStateID, from.ID)
	}
	if got.ToStateID == nil || *got.ToStateID != to.ID {
		t.Errorf("to_state_id = %v, want %s", got.ToStateID, to.ID)
	}

	// A user with no name parts falls back to the username — models.PersonDisplayName's
	// rule, the one the rest of the codebase renders people with.
	nameless := eoHistoryUser(t, db, "", "", "", "kiosk")
	second := eoHistoryEntry(t, repo, rec, at.Add(-time.Minute), "kiosk entry")
	if err := db.Model(&models.EORecordHistory{}).Where("id = ?", second.ID).
		Update("performed_by_id", nameless.ID).Error; err != nil {
		t.Fatalf("attach nameless actor: %v", err)
	}
	rows, _, err = repo.History(ctx, rec.ID, rec.RecordTypeID, 1, 100)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if rows[1].PerformedByName != "kiosk" {
		t.Errorf("performed_by_name = %q, want the username %q", rows[1].PerformedByName, "kiosk")
	}

	// AN ARCHIVED ACTOR STILL HAS A NAME. An audit trail records who did
	// something, and that fact does not expire when the account is
	// soft-deleted — which is why the read names the TABLE rather than the
	// model, where GORM would add deleted_at IS NULL.
	if err := db.Delete(&models.User{}, "id = ?", actor.ID).Error; err != nil {
		t.Fatalf("soft-delete actor: %v", err)
	}
	if err := db.Delete(&models.WorkflowState{}, "id = ?", from.ID).Error; err != nil {
		t.Fatalf("soft-delete state: %v", err)
	}
	rows, _, err = repo.History(ctx, rec.ID, rec.RecordTypeID, 1, 100)
	if err != nil {
		t.Fatalf("history after soft delete: %v", err)
	}
	if rows[0].PerformedByName != "Layla A. Mansour" {
		t.Errorf("performed_by_name = %q after the user was archived, want it unchanged: the trail records who acted", rows[0].PerformedByName)
	}
	if rows[0].FromStateName != "Under Review" {
		t.Errorf("from_state_name = %q after the state was retired, want it unchanged", rows[0].FromStateName)
	}
}

// TestEORecordHistoryServesRowsWhoseNamesCannotBeResolved — the two edge cases
// that must not blank a row or fail the request.
//
// A null performed_by_id is a SYSTEM action (a timer, an escalation sweep) and
// there is no person to name. A null from_state_id is a CREATE row, which came
// from no state. A performed_by_id naming a purged user resolves to nothing.
//
// All three answer with an EMPTY NAME and an otherwise complete row. The name
// is empty rather than a placeholder sentence because an English literal baked
// into the payload would have no Arabic counterpart in a bilingual register —
// the client renders its own wording, and `is_system_action` and the surviving
// ids tell it which case it is looking at.
func TestEORecordHistoryServesRowsWhoseNamesCannotBeResolved(t *testing.T) {
	db := eoHistoryDB(t)
	repo := NewEORecordRepository(db)
	ctx := context.Background()
	rec := eoListRec(t, repo, uuid.New(), "H-UNRESOLVED", nil)
	to := eoHistoryState(t, db, "Registered", "مسجل")
	purged := uuid.New() // a user id that names nobody

	at := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	created := eoHistoryEntry(t, repo, rec, at, "created")
	if err := db.Model(&models.EORecordHistory{}).Where("id = ?", created.ID).
		Updates(map[string]any{"to_state_id": to.ID, "performed_by_id": purged}).Error; err != nil {
		t.Fatalf("attach ids: %v", err)
	}
	system := eoHistoryEntry(t, repo, rec, at.Add(-time.Minute), "escalated")
	if err := db.Model(&models.EORecordHistory{}).Where("id = ?", system.ID).
		Updates(map[string]any{"is_system_action": true, "to_state_id": to.ID}).Error; err != nil {
		t.Fatalf("mark system action: %v", err)
	}

	rows, total, err := repo.History(ctx, rec.ID, rec.RecordTypeID, 1, 100)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("total = %d, rows = %d, want 2 and 2 — an unresolvable name must not drop a row", total, len(rows))
	}

	t.Run("a create row has no from-state and is served whole", func(t *testing.T) {
		got := rows[0]
		if got.FromStateID != nil {
			t.Fatalf("fixture is wrong: from_state_id = %v, want nil", got.FromStateID)
		}
		if got.FromStateName != "" || got.FromStateNameAr != "" {
			t.Errorf("from state = %q / %q, want empty", got.FromStateName, got.FromStateNameAr)
		}
		// The rest of the row is intact — this is the "not blanked" half.
		if got.Comment != "created" || got.ToStateName != "Registered" || got.ToStateNameAr != "مسجل" {
			t.Errorf("row = %+v, want its comment and to-state intact", got)
		}
		if got.ID == uuid.Nil || got.CreatedAt.IsZero() {
			t.Errorf("row lost its id or timestamp: %+v", got)
		}
	})

	t.Run("an actor who no longer exists leaves a name empty, not a row missing", func(t *testing.T) {
		got := rows[0]
		if got.PerformedByName != "" {
			t.Errorf("performed_by_name = %q, want empty for a purged user", got.PerformedByName)
		}
		// The id is still there, so a client can say so rather than guess.
		if got.PerformedByID == nil || *got.PerformedByID != purged {
			t.Errorf("performed_by_id = %v, want %s kept", got.PerformedByID, purged)
		}
	})

	t.Run("a system action names nobody and says so", func(t *testing.T) {
		got := rows[1]
		if got.PerformedByID != nil {
			t.Fatalf("fixture is wrong: performed_by_id = %v, want nil", got.PerformedByID)
		}
		if got.PerformedByName != "" {
			t.Errorf("performed_by_name = %q, want empty: no person acted", got.PerformedByName)
		}
		if !got.IsSystemAction {
			t.Error("is_system_action = false: it is what tells the client this was not a purged user")
		}
		if got.ToStateName != "Registered" {
			t.Errorf("to_state_name = %q, want Registered: a system row still resolves its states", got.ToStateName)
		}
	})
}

// eoQueryCounter records every SQL statement GORM runs on the session it is
// installed on. It is a logger rather than a callback because Count, Find and
// Scan do not all pass through the same callback processor, and a counter that
// missed one would under-report exactly the query an N+1 adds.
type eoQueryCounter struct {
	mu   sync.Mutex
	sqls []string
}

func (c *eoQueryCounter) LogMode(logger.LogLevel) logger.Interface      { return c }
func (c *eoQueryCounter) Info(context.Context, string, ...interface{})  {}
func (c *eoQueryCounter) Warn(context.Context, string, ...interface{})  {}
func (c *eoQueryCounter) Error(context.Context, string, ...interface{}) {}
func (c *eoQueryCounter) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sqls = append(c.sqls, sql)
}
func (c *eoQueryCounter) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sqls = nil
}
func (c *eoQueryCounter) statements() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.sqls...)
}

// TestEORecordHistoryResolvesNamesInAFixedNumberOfQueries.
//
// Resolving a name per row would be an N+1 on a route whose page may hold
// eoMaxLimit rows — the risk the decoration introduces, so it is the thing
// that has to be measured rather than asserted. The count must not grow with
// the number of rows, nor with the number of DISTINCT actors: the count, the
// page, one batch read of the page's actors and one of its states, and nothing
// else.
func TestEORecordHistoryResolvesNamesInAFixedNumberOfQueries(t *testing.T) {
	db := eoHistoryDB(t)
	ctx := context.Background()
	writer := NewEORecordRepository(db)
	rec := eoListRec(t, writer, uuid.New(), "H-NPLUS1", nil)

	states := []*models.WorkflowState{
		eoHistoryState(t, db, "Registered", "مسجل"),
		eoHistoryState(t, db, "Under Review", "قيد المراجعة"),
		eoHistoryState(t, db, "Approved", "معتمد"),
	}

	at := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	// Every row gets its OWN actor, so a per-row resolver cannot be rescued by
	// a cache keyed on the actor.
	written := 0
	write := func(n int) {
		for k := 0; k < n; k++ {
			i := written
			written++
			u := eoHistoryUser(t, db, "Officer", "", strconv.Itoa(i), "officer"+strconv.Itoa(i))
			e := eoHistoryEntry(t, writer, rec, at.Add(-time.Duration(len(states)*i)*time.Minute), "moved")
			if err := db.Model(&models.EORecordHistory{}).Where("id = ?", e.ID).
				Updates(map[string]any{
					"performed_by_id": u.ID,
					"from_state_id":   states[i%len(states)].ID,
					"to_state_id":     states[(i+1)%len(states)].ID,
				}).Error; err != nil {
				t.Fatalf("attach ids: %v", err)
			}
		}
	}

	counter := &eoQueryCounter{}
	repo := NewEORecordRepository(db.Session(&gorm.Session{Logger: counter}))

	measure := func(wantRows int) []string {
		t.Helper()
		counter.reset()
		rows, total, err := repo.History(ctx, rec.ID, rec.RecordTypeID, 1, eoMaxLimit)
		if err != nil {
			t.Fatalf("history: %v", err)
		}
		if len(rows) != wantRows || total != int64(wantRows) {
			t.Fatalf("rows = %d total = %d, want %d", len(rows), total, wantRows)
		}
		for i, row := range rows {
			if row.PerformedByName == "" || row.ToStateName == "" {
				t.Fatalf("row %d resolved nothing (%+v): the measurement below would be meaningless", i, row)
			}
		}
		return counter.statements()
	}

	write(2)
	small := measure(2)
	write(18)
	large := measure(20)

	if len(small) != len(large) {
		t.Fatalf("2 rows took %d queries, 20 rows took %d — the name resolution is an N+1; the 20-row run issued:\n%s",
			len(small), len(large), eoFirstStatements(large, 6))
	}
	// The count, the page, the actors, the states.
	if want := 4; len(large) != want {
		t.Errorf("a page of history took %d queries, want %d (count, page, actors, states):\n%s",
			len(large), want, eoFirstStatements(large, 6))
	}

	// The batch reads go over the page's DISTINCT ids: twenty rows move
	// between three states, so the state lookup must name three, not forty.
	// Without that, the query COUNT stays at four while the IN list grows
	// with the page — an N+1 by another name.
	var stateQuery string
	for _, sql := range large {
		if strings.Contains(sql, "workflow_states") {
			stateQuery = sql
		}
	}
	if stateQuery == "" {
		t.Fatalf("no state lookup was issued: %s", eoFirstStatements(large, 6))
	}
	for _, st := range states {
		if n := strings.Count(stateQuery, st.ID.String()); n != 1 {
			t.Errorf("state %s appears %d times in the lookup, want 1 — the ids are not de-duplicated: %s",
				st.Name, n, eoTruncate(stateQuery, 160))
		}
	}
}

// eoTruncate shortens a statement for a failure message: an un-deduplicated
// IN list runs to thousands of characters and the point is its shape.
func eoTruncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("… (%d chars)", len(s))
}

// eoFirstStatements renders at most n statements, one per line. A failing
// N+1 run issues dozens of near-identical queries and printing them all
// buries the number that matters.
func eoFirstStatements(sqls []string, n int) string {
	if len(sqls) > n {
		sqls = append(append([]string(nil), sqls[:n]...), fmt.Sprintf("… and %d more", len(sqls)-n))
	}
	return strings.Join(sqls, "\n")
}
