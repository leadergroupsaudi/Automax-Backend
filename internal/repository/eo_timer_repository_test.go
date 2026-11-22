package repository

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/automax/backend/internal/database/migrations"
	"github.com/automax/backend/internal/models"
)

// eoTimerTestDB is the sequence test's pooled handle (a real multi-connection
// pool on the test schema) with the eo_timers table migrated. The one-connection
// eoTestDB would serialise the claimers and make SKIP LOCKED untestable.
func eoTimerTestDB(t *testing.T) *gorm.DB { return eoSequenceTestDB(t) }

func eoNewTimer(fireAt time.Time) *models.EOTimer {
	return &models.EOTimer{
		RecordID:      uuid.New(),
		RecordTypeID:  uuid.New(),
		FireAt:        fireAt,
		CapabilityKey: "reminder",
	}
}

func eoTimerStatus(t *testing.T, db *gorm.DB, id uuid.UUID) string {
	t.Helper()
	var s string
	if err := db.Raw(`SELECT status FROM eo_timers WHERE id = ?`, id).Scan(&s).Error; err != nil {
		t.Fatalf("read status: %v", err)
	}
	return s
}

func TestClaimDueIsExactlyOnceAcrossConcurrentClaimers(t *testing.T) {
	db := eoTimerTestDB(t)
	repo := NewEOTimerRepository(db)
	ctx := context.Background()
	now := time.Now()

	const timers, claimers, batch = 20, 4, 3
	want := map[uuid.UUID]bool{}
	for i := 0; i < timers; i++ {
		tm := eoNewTimer(now.Add(-time.Duration(i+1) * time.Minute))
		if err := repo.Schedule(ctx, tm); err != nil {
			t.Fatalf("schedule: %v", err)
		}
		want[tm.ID] = true
	}

	// Prove the claimers genuinely overlap: count statements in flight on the
	// pooled handle, and hold each one briefly before it reaches the server so
	// the window is wide. eoTestDB's single connection would pin this at 1.
	var inflight, maxInflight int32
	cb := db.Callback().Row()
	if err := cb.Before("gorm:row").Register("eo_timer_test:enter", func(tx *gorm.DB) {
		n := atomic.AddInt32(&inflight, 1)
		for {
			m := atomic.LoadInt32(&maxInflight)
			if n <= m || atomic.CompareAndSwapInt32(&maxInflight, m, n) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
	}); err != nil {
		t.Fatalf("register enter: %v", err)
	}
	if err := cb.After("gorm:row").Register("eo_timer_test:leave", func(tx *gorm.DB) {
		atomic.AddInt32(&inflight, -1)
	}); err != nil {
		t.Fatalf("register leave: %v", err)
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	claimed := make([][]uuid.UUID, claimers)
	errs := make([]error, claimers)
	for g := 0; g < claimers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			<-start
			for {
				got, err := repo.ClaimDue(ctx, now, batch)
				if err != nil {
					errs[g] = err
					return
				}
				if len(got) == 0 {
					return
				}
				for _, tm := range got {
					claimed[g] = append(claimed[g], tm.ID)
				}
			}
		}(g)
	}
	close(start)
	wg.Wait()

	if m := atomic.LoadInt32(&maxInflight); m < 2 {
		t.Fatalf("claimers never overlapped (max in-flight %d): this test proves nothing", m)
	}
	seen := map[uuid.UUID]int{}
	for g := 0; g < claimers; g++ {
		if errs[g] != nil {
			t.Fatalf("claimer %d: %v", g, errs[g])
		}
		for _, id := range claimed[g] {
			seen[id]++
		}
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("timer %s claimed %d times", id, n)
		}
		if !want[id] {
			t.Errorf("timer %s claimed but never scheduled", id)
		}
	}
	for id := range want {
		if seen[id] == 0 {
			t.Errorf("timer %s was never claimed", id)
		}
	}
	t.Logf("max statements in flight: %d", atomic.LoadInt32(&maxInflight))
}

// A timer another worker holds a row lock on is skipped, not waited for. A
// claimer that merely queued behind the lock would still hand out each timer
// once, so the concurrent test above cannot tell SKIP LOCKED from FOR UPDATE:
// this one can, because the blocked variant cannot return before the deadline.
func TestClaimDueSkipsRowsLockedByAnotherTransaction(t *testing.T) {
	db := eoTimerTestDB(t)
	repo := NewEOTimerRepository(db)
	ctx := context.Background()
	now := time.Now()

	locked := eoNewTimer(now.Add(-2 * time.Minute))
	free := eoNewTimer(now.Add(-time.Minute))
	for _, tm := range []*models.EOTimer{locked, free} {
		if err := repo.Schedule(ctx, tm); err != nil {
			t.Fatalf("schedule: %v", err)
		}
	}

	// A second pooled connection holds the lock, so the claim below has its own.
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatalf("begin: %v", tx.Error)
	}
	defer tx.Rollback()
	if err := tx.Exec(`SELECT id FROM eo_timers WHERE id = ? FOR UPDATE`, locked.ID).Error; err != nil {
		t.Fatalf("lock: %v", err)
	}

	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	got, err := repo.ClaimDue(cctx, now, 10)
	if err != nil {
		t.Fatalf("ClaimDue blocked or failed beside a locked row: %v", err)
	}
	if len(got) != 1 || got[0].ID != free.ID {
		t.Fatalf("got %d timers, want only the unlocked one", len(got))
	}
	if s := eoTimerStatus(t, db, locked.ID); s != models.EOTimerPending {
		t.Errorf("locked timer status = %q, want pending", s)
	}
}

func TestClaimDueIgnoresFutureTimers(t *testing.T) {
	db := eoTimerTestDB(t)
	repo := NewEOTimerRepository(db)
	ctx := context.Background()
	now := time.Now()

	future := eoNewTimer(now.Add(time.Hour))
	due := eoNewTimer(now.Add(-time.Second))
	for _, tm := range []*models.EOTimer{future, due} {
		if err := repo.Schedule(ctx, tm); err != nil {
			t.Fatalf("schedule: %v", err)
		}
	}
	got, err := repo.ClaimDue(ctx, now, 10)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	if len(got) != 1 || got[0].ID != due.ID {
		t.Fatalf("got %d timers, want only the due one", len(got))
	}
	if got[0].Status != models.EOTimerClaimed || got[0].ClaimedAt == nil {
		t.Errorf("returned timer = status %q claimed_at %v, want claimed with a claim time", got[0].Status, got[0].ClaimedAt)
	}
	if s := eoTimerStatus(t, db, future.ID); s != models.EOTimerPending {
		t.Errorf("future timer status = %q, want pending", s)
	}
}

func TestClaimDueHonoursLimitAndOrder(t *testing.T) {
	db := eoTimerTestDB(t)
	repo := NewEOTimerRepository(db)
	ctx := context.Background()
	now := time.Now()

	var ids []uuid.UUID
	for _, ago := range []time.Duration{3, 1, 2} { // scheduled out of order
		tm := eoNewTimer(now.Add(-ago * time.Minute))
		if err := repo.Schedule(ctx, tm); err != nil {
			t.Fatalf("schedule: %v", err)
		}
		ids = append(ids, tm.ID)
	}
	got, err := repo.ClaimDue(ctx, now, 2)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	if len(got) != 2 || got[0].ID != ids[0] || got[1].ID != ids[2] {
		t.Fatalf("want the two earliest, earliest first; got %d timers", len(got))
	}
	if n, _ := repo.ClaimDue(ctx, now, 0); len(n) != 0 {
		t.Errorf("limit 0 claimed %d timers", len(n))
	}
}

func TestClaimDueReclaimsOnlyExpiredClaims(t *testing.T) {
	db := eoTimerTestDB(t)
	repo := NewEOTimerRepository(db)
	ctx := context.Background()
	now := time.Now()

	fresh := eoNewTimer(now.Add(-time.Hour))
	stale := eoNewTimer(now.Add(-time.Hour))
	for _, tm := range []*models.EOTimer{fresh, stale} {
		if err := repo.Schedule(ctx, tm); err != nil {
			t.Fatalf("schedule: %v", err)
		}
	}
	if err := db.Exec(`UPDATE eo_timers SET status='claimed', claimed_at=? WHERE id=?`,
		now.Add(-time.Minute), fresh.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`UPDATE eo_timers SET status='claimed', claimed_at=? WHERE id=?`,
		now.Add(-EOTimerClaimLease-time.Minute), stale.ID).Error; err != nil {
		t.Fatal(err)
	}
	got, err := repo.ClaimDue(ctx, now, 10)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	if len(got) != 1 || got[0].ID != stale.ID {
		t.Fatalf("got %d timers, want only the abandoned claim", len(got))
	}
}

func TestMarkFiredAndCancelForRecord(t *testing.T) {
	db := eoTimerTestDB(t)
	repo := NewEOTimerRepository(db)
	ctx := context.Background()
	now := time.Now()

	a := eoNewTimer(now.Add(-time.Minute))
	b := eoNewTimer(now.Add(time.Hour))
	b.RecordID = a.RecordID // same record
	other := eoNewTimer(now.Add(time.Hour))
	fired := eoNewTimer(now.Add(-time.Minute))
	fired.RecordID = a.RecordID
	for _, tm := range []*models.EOTimer{a, b, other, fired} {
		if err := repo.Schedule(ctx, tm); err != nil {
			t.Fatalf("schedule: %v", err)
		}
	}

	if err := repo.MarkFired(ctx, a.ID); !errors.Is(err, ErrEOTimerNotClaimed) {
		t.Fatalf("MarkFired on a pending timer = %v, want ErrEOTimerNotClaimed", err)
	}
	if _, err := repo.ClaimDue(ctx, now, 10); err != nil { // claims a and fired
		t.Fatal(err)
	}
	if err := repo.MarkFired(ctx, fired.ID); err != nil {
		t.Fatalf("MarkFired: %v", err)
	}
	if s := eoTimerStatus(t, db, fired.ID); s != models.EOTimerFired {
		t.Errorf("status = %q, want fired", s)
	}

	if err := repo.CancelForRecord(ctx, a.RecordID); err != nil {
		t.Fatalf("CancelForRecord: %v", err)
	}
	for id, want := range map[uuid.UUID]string{
		a.ID:     models.EOTimerCancelled, // claimed, in flight
		b.ID:     models.EOTimerCancelled, // pending
		fired.ID: models.EOTimerFired,     // history is kept
		other.ID: models.EOTimerPending,   // another record
	} {
		if s := eoTimerStatus(t, db, id); s != want {
			t.Errorf("timer %s status = %q, want %q", id, s, want)
		}
	}
	// A timer cancelled while its handler ran cannot then be marked fired.
	if err := repo.MarkFired(ctx, a.ID); !errors.Is(err, ErrEOTimerNotClaimed) {
		t.Errorf("MarkFired on a cancelled timer = %v, want ErrEOTimerNotClaimed", err)
	}
}

func TestScheduleValidatesAndDefaults(t *testing.T) {
	db := eoTimerTestDB(t)
	repo := NewEOTimerRepository(db)
	ctx := context.Background()

	bad := eoNewTimer(time.Time{})
	if err := repo.Schedule(ctx, bad); err == nil {
		t.Error("zero fire_at accepted")
	}
	noKey := eoNewTimer(time.Now())
	noKey.CapabilityKey = ""
	if err := repo.Schedule(ctx, noKey); err == nil {
		t.Error("empty capability_key accepted")
	}

	tm := eoNewTimer(time.Now())
	tm.Status = models.EOTimerFired // a caller cannot schedule a pre-fired timer
	if err := repo.Schedule(ctx, tm); err != nil {
		t.Fatalf("schedule: %v", err)
	}
	if s := eoTimerStatus(t, db, tm.ID); s != models.EOTimerPending {
		t.Errorf("status = %q, want pending", s)
	}
	var payload string
	db.Raw(`SELECT payload::text FROM eo_timers WHERE id = ?`, tm.ID).Scan(&payload)
	if payload != "{}" {
		t.Errorf("payload = %q, want {}", payload)
	}
}

// eo_timers is registered with assertColumns: a live table that has drifted
// from the declared shape must fail the migration, not pass silently.
func TestMigrateEOPlatformDetectsEOTimersDrift(t *testing.T) {
	db := eoTestDB(t)
	if err := migrations.MigrateEOPlatform(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Exec(`ALTER TABLE eo_timers DROP COLUMN claimed_at`).Error; err != nil {
		t.Fatal(err)
	}
	err := migrations.MigrateEOPlatform(db)
	if err == nil || !strings.Contains(err.Error(), "claimed_at") {
		t.Fatalf("drifted eo_timers not detected, err = %v", err)
	}
}

// eoTimerRetry plays the monitor against the real repository: claim, then fail
// or fire. Each round advances the clock past the lease so the timer is due
// for re-delivery.
func eoTimerRound(t *testing.T, repo EOTimerRepository, clock *time.Time) []models.EOTimer {
	t.Helper()
	*clock = clock.Add(EOTimerClaimLease + time.Minute)
	got, err := repo.ClaimDue(context.Background(), *clock, 10)
	if err != nil {
		t.Fatalf("ClaimDue: %v", err)
	}
	return got
}

func TestTimerGivesUpAfterMaxAttempts(t *testing.T) {
	db := eoTimerTestDB(t)
	repo := NewEOTimerRepository(db)
	ctx := context.Background()
	clock := time.Now()
	tm := eoNewTimer(clock.Add(-time.Hour))
	if err := repo.Schedule(ctx, tm); err != nil {
		t.Fatal(err)
	}

	for attempt := 1; attempt <= EOTimerMaxAttempts; attempt++ {
		got := eoTimerRound(t, repo, &clock)
		if len(got) != 1 || got[0].Attempts != attempt {
			t.Fatalf("attempt %d: got %d timers (attempts %v)", attempt, len(got), got)
		}
		failed, err := repo.RecordFailure(ctx, tm.ID, "handler exploded")
		if err != nil {
			t.Fatalf("RecordFailure: %v", err)
		}
		if want := attempt == EOTimerMaxAttempts; failed != want {
			t.Fatalf("attempt %d: failed = %v, want %v", attempt, failed, want)
		}
	}
	if s := eoTimerStatus(t, db, tm.ID); s != models.EOTimerFailed {
		t.Fatalf("status = %q, want failed", s)
	}
	var msg string
	db.Raw(`SELECT last_error FROM eo_timers WHERE id = ?`, tm.ID).Scan(&msg)
	if msg != "handler exploded" {
		t.Errorf("last_error = %q", msg)
	}
	if got := eoTimerRound(t, repo, &clock); len(got) != 0 {
		t.Errorf("a failed timer was claimed again")
	}
}

func TestTimerSucceedingOnALaterAttemptIsNotFailed(t *testing.T) {
	db := eoTimerTestDB(t)
	repo := NewEOTimerRepository(db)
	ctx := context.Background()
	clock := time.Now()
	tm := eoNewTimer(clock.Add(-time.Hour))
	if err := repo.Schedule(ctx, tm); err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt < EOTimerMaxAttempts; attempt++ {
		if got := eoTimerRound(t, repo, &clock); len(got) != 1 {
			t.Fatalf("attempt %d: not redelivered", attempt)
		}
		if failed, err := repo.RecordFailure(ctx, tm.ID, "transient"); err != nil || failed {
			t.Fatalf("attempt %d: failed=%v err=%v", attempt, failed, err)
		}
	}
	if got := eoTimerRound(t, repo, &clock); len(got) != 1 {
		t.Fatal("last attempt not redelivered")
	}
	if err := repo.MarkFired(ctx, tm.ID); err != nil {
		t.Fatalf("MarkFired: %v", err)
	}
	if s := eoTimerStatus(t, db, tm.ID); s != models.EOTimerFired {
		t.Errorf("status = %q, want fired", s)
	}
}

// A timer that kills its worker never reaches RecordFailure; ClaimDue must give
// up on it rather than leave it claimed forever.
func TestClaimDueFailsTimersThatExhaustedAttemptsWithoutReporting(t *testing.T) {
	db := eoTimerTestDB(t)
	repo := NewEOTimerRepository(db)
	ctx := context.Background()
	clock := time.Now()
	tm := eoNewTimer(clock.Add(-time.Hour))
	if err := repo.Schedule(ctx, tm); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < EOTimerMaxAttempts; i++ {
		if got := eoTimerRound(t, repo, &clock); len(got) != 1 { // claimed, worker "crashes"
			t.Fatalf("round %d: got %d", i, len(got))
		}
	}
	if got := eoTimerRound(t, repo, &clock); len(got) != 0 {
		t.Fatal("exhausted timer reclaimed")
	}
	if s := eoTimerStatus(t, db, tm.ID); s != models.EOTimerFailed {
		t.Errorf("status = %q, want failed", s)
	}
}
