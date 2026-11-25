package repository

import (
	"context"
	"testing"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/datatypes"
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

func eoHistoryComments(rows []models.EORecordHistory) []string {
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
