package repository

import (
	"context"
	"testing"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// eoLockSeed inserts one record and returns it.
func eoLockSeed(t *testing.T, repo EORecordRepository) *models.EORecord {
	t.Helper()
	rec := &models.EORecord{
		ID: uuid.New(), RecordTypeID: uuid.New(), Reference: "LOCK-1",
		SubjectAr: "قفل", CreatedByID: uuid.New(),
	}
	if err := repo.Create(context.Background(), rec); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return rec
}

// TestEOLockForUpdateTakesARowLock proves the two halves of the statement
// actually reach Postgres: FOR UPDATE (a RowShareLock on the partition, where
// a plain read would take only an AccessShareLock) and SET LOCAL lock_timeout
// (scoped to the transaction, so it is gone afterwards). Neither is visible
// from the Go side, and an unexercised lock is not a lock.
func TestEOLockForUpdateTakesARowLock(t *testing.T) {
	db := eoRecordDB(t)
	repo := NewEORecordRepository(db)
	rec := eoLockSeed(t, repo)
	ctx := context.Background()

	// The test pool is pinned to ONE connection, so every statement here runs
	// on the transaction's own handle: a db.Raw beside an open transaction
	// would wait for a second connection that never comes.
	err := db.Transaction(func(tx *gorm.DB) error {
		r := &eoRecordRepository{db: tx}
		locked, err := r.LockForUpdate(ctx, rec.ID, rec.RecordTypeID)
		if err != nil {
			return err
		}
		if locked == nil || locked.Reference != "LOCK-1" {
			t.Fatalf("locked = %v, want the seeded record", locked)
		}

		var timeout string
		if err := tx.Raw("SHOW lock_timeout").Scan(&timeout).Error; err != nil {
			return err
		}
		if timeout != "10s" {
			t.Errorf("lock_timeout inside the transaction = %q, want 10s", timeout)
		}

		var modes []string
		if err := tx.Raw(`
			SELECT l.mode
			FROM pg_locks l
			JOIN pg_class c ON c.oid = l.relation
			WHERE l.pid = pg_backend_pid() AND c.relname LIKE 'eo_records%'`).Scan(&modes).Error; err != nil {
			return err
		}
		found := false
		for _, m := range modes {
			if m == "RowShareLock" {
				found = true
			}
		}
		if !found {
			t.Errorf("locks held on eo_records = %v, want a RowShareLock — the FOR UPDATE clause did not reach SQL", modes)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("transaction: %v", err)
	}

	// SET LOCAL is scoped to the transaction, so a contended row cannot leave
	// a 10s timeout behind on a pooled connection.
	var after string
	if err := db.Raw("SHOW lock_timeout").Scan(&after).Error; err != nil {
		t.Fatalf("read lock_timeout: %v", err)
	}
	if after == "10s" {
		t.Errorf("lock_timeout after the transaction = %q, want the session default back", after)
	}
}

// A missing record, or one of another type, is (nil, nil) — absence is an
// ordinary answer here, exactly as it is for every other getter.
func TestEOLockForUpdateMissingRecord(t *testing.T) {
	db := eoRecordDB(t)
	repo := NewEORecordRepository(db)
	rec := eoLockSeed(t, repo)
	ctx := context.Background()

	cases := []struct {
		name       string
		id, typeID uuid.UUID
	}{
		{"no such record", uuid.New(), rec.RecordTypeID},
		{"wrong record type", rec.ID, uuid.New()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := repo.Transaction(ctx, func(r EORecordRepository, _ EORecordTypeRepository, _ EOTimerRepository) error {
				got, err := r.LockForUpdate(ctx, tc.id, tc.typeID)
				if err != nil {
					return err
				}
				if got != nil {
					t.Errorf("got %v, want nil", got)
				}
				return nil
			})
			if err != nil && err != gorm.ErrRecordNotFound {
				t.Fatalf("transaction: %v", err)
			}
		})
	}
}
