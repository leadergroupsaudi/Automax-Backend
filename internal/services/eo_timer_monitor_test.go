package services

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
)

// eoFakeTimerRepo hands out its queued timers once each and records what the
// monitor did with them. Claim semantics are the repository's job and are
// tested against Postgres; this only drives the monitor.
type eoFakeTimerRepo struct {
	mu         sync.Mutex
	queue      []models.EOTimer
	fired      []uuid.UUID
	cancelled  []uuid.UUID
	claims     int
	claimErr   error
	fireErr    error
	batches    []int
	failures   map[uuid.UUID]string
	failedNow  bool
	released   []uuid.UUID
	releasedAt []time.Time
	markedAt   map[uuid.UUID]time.Time
}

func (f *eoFakeTimerRepo) Schedule(context.Context, *models.EOTimer) error { return nil }

func (f *eoFakeTimerRepo) ClaimDue(_ context.Context, _ time.Time, limit int) ([]models.EOTimer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claims++
	f.batches = append(f.batches, limit)
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	n := limit
	if n > len(f.queue) {
		n = len(f.queue)
	}
	out := append([]models.EOTimer(nil), f.queue[:n]...)
	f.queue = f.queue[n:]
	now := time.Now()
	for i := range out {
		out[i].ClaimedAt = &now
	}
	return out, nil
}

func (f *eoFakeTimerRepo) MarkFired(ctx context.Context, id uuid.UUID, claimedAt time.Time) error {
	if err := ctx.Err(); err != nil { // like a real database
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fireErr != nil {
		return f.fireErr
	}
	f.fired = append(f.fired, id)
	if f.markedAt == nil {
		f.markedAt = map[uuid.UUID]time.Time{}
	}
	f.markedAt[id] = claimedAt
	return nil
}

func (f *eoFakeTimerRepo) RecordFailure(ctx context.Context, id uuid.UUID, claimedAt time.Time, msg string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failures == nil {
		f.failures = map[uuid.UUID]string{}
	}
	f.failures[id] = msg
	if f.markedAt == nil {
		f.markedAt = map[uuid.UUID]time.Time{}
	}
	f.markedAt[id] = claimedAt
	return f.failedNow, nil
}

func (f *eoFakeTimerRepo) Release(ctx context.Context, id uuid.UUID, claimedAt time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, id)
	f.releasedAt = append(f.releasedAt, claimedAt)
	return nil
}

func (f *eoFakeTimerRepo) WithTx(*gorm.DB) repository.EOTimerRepository { return f }

func (f *eoFakeTimerRepo) CancelForRecord(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, id)
	return nil
}

func (f *eoFakeTimerRepo) CancelForRecordByCapability(context.Context, uuid.UUID, string) error {
	return nil
}

func (f *eoFakeTimerRepo) claimCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.claims
}

func eoTimers(n int) []models.EOTimer {
	out := make([]models.EOTimer, n)
	for i := range out {
		out[i] = models.EOTimer{ID: uuid.New(), RecordID: uuid.New(), CapabilityKey: "reminder"}
	}
	return out
}

func eoNoopHandler(context.Context, models.EOTimer) error { return nil }

func TestTimerMonitorGuardsAgainstZeroInterval(t *testing.T) {
	for _, interval := range []time.Duration{0, -time.Second} {
		repo := &eoFakeTimerRepo{}
		m := NewEOTimerMonitor(repo, eoNoopHandler, interval)
		m.Start(context.Background()) // time.NewTicker(0) would panic in the goroutine
		deadline := time.Now().Add(2 * time.Second)
		for repo.claimCount() == 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		if repo.claimCount() == 0 {
			t.Errorf("interval %v: monitor never ran", interval)
		}
		m.Stop()
	}
}

func TestTimerMonitorStopIsIdempotent(t *testing.T) {
	m := NewEOTimerMonitor(&eoFakeTimerRepo{}, eoNoopHandler, time.Hour)
	m.Stop() // before Start
	m.Start(context.Background())
	m.Stop()
	m.Stop() // second Stop must not close a closed channel
}

func TestTimerMonitorStopsOnContextCancelAndCanRestart(t *testing.T) {
	repo := &eoFakeTimerRepo{}
	m := NewEOTimerMonitor(repo, eoNoopHandler, 5*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	m.Start(ctx)
	m.Start(ctx) // second Start must not launch a second loop
	cancel()
	m.Stop()
	m.Start(context.Background()) // Stop then Start works
	m.Stop()
}

func TestTimerMonitorRunOnce(t *testing.T) {
	boom := errors.New("boom")
	cases := []struct {
		name          string
		handler       func(bad uuid.UUID) EOTimerHandler
		wantFired     int
		wantCancelled int
		wantErr       bool
	}{
		{"success marks fired", func(uuid.UUID) EOTimerHandler { return eoNoopHandler }, 3, 0, false},
		{"a failing handler is not marked fired, others still run", func(bad uuid.UUID) EOTimerHandler {
			return func(_ context.Context, tm models.EOTimer) error {
				if tm.ID == bad {
					return boom
				}
				return nil
			}
		}, 2, 0, true},
		{"a vanished record cancels its timers instead of retrying", func(bad uuid.UUID) EOTimerHandler {
			return func(_ context.Context, tm models.EOTimer) error {
				if tm.ID == bad {
					return ErrEOTimerRecordGone
				}
				return nil
			}
		}, 2, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &eoFakeTimerRepo{queue: eoTimers(3)}
			bad := repo.queue[1].ID
			m := NewEOTimerMonitor(repo, tc.handler(bad), time.Hour)
			err := m.RunOnce(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("RunOnce err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr && !errors.Is(err, boom) {
				t.Errorf("err %v does not wrap the handler error", err)
			}
			if len(repo.fired) != tc.wantFired || len(repo.cancelled) != tc.wantCancelled {
				t.Errorf("fired %d cancelled %d, want %d / %d", len(repo.fired), len(repo.cancelled), tc.wantFired, tc.wantCancelled)
			}
			if _, recorded := repo.failures[bad]; recorded != (tc.wantErr) {
				t.Errorf("failure recorded = %v, want %v", recorded, tc.wantErr)
			}
			if tc.wantErr && repo.failures[bad] != "boom" {
				t.Errorf("recorded message %q, want the handler error", repo.failures[bad])
			}
			for _, id := range repo.fired {
				if id == bad && tc.wantFired < 3 {
					t.Error("the failed timer was marked fired")
				}
			}
		})
	}
}

func TestTimerMonitorRunOnceDrainsABacklog(t *testing.T) {
	repo := &eoFakeTimerRepo{queue: eoTimers(eoTimerBatchSize*2 + 5)}
	m := NewEOTimerMonitor(repo, eoNoopHandler, time.Hour)
	if err := m.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(repo.fired) != eoTimerBatchSize*2+5 {
		t.Errorf("fired %d, want the whole backlog", len(repo.fired))
	}
}

func TestTimerMonitorRunOnceReportsClaimErrorAndNoHandler(t *testing.T) {
	repo := &eoFakeTimerRepo{claimErr: errors.New("db down")}
	if err := NewEOTimerMonitor(repo, eoNoopHandler, time.Hour).RunOnce(context.Background()); err == nil {
		t.Error("claim error swallowed")
	}
	if err := NewEOTimerMonitor(&eoFakeTimerRepo{}, nil, time.Hour).RunOnce(context.Background()); err == nil {
		t.Error("nil handler accepted")
	}
}

// a compile-time check that the fake satisfies the real interface.
var _ repository.EOTimerRepository = (*eoFakeTimerRepo)(nil)

func TestTimerMonitorMarkFiredErrorBranches(t *testing.T) {
	cases := []struct {
		name    string
		fireErr error
		wantErr bool
	}{
		{"cancelled while running is benign", repository.ErrEOTimerNotClaimed, false},
		{"any other mark error is reported, not swallowed", errors.New("db down"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &eoFakeTimerRepo{queue: eoTimers(1), fireErr: tc.fireErr}
			err := NewEOTimerMonitor(repo, eoNoopHandler, time.Hour).RunOnce(context.Background())
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if len(repo.failures) != 0 {
				t.Error("a handler that succeeded was recorded as failed")
			}
		})
	}
}

func TestTimerMonitorReportsAGiveUp(t *testing.T) {
	repo := &eoFakeTimerRepo{queue: eoTimers(1), failedNow: true}
	h := func(context.Context, models.EOTimer) error { return errors.New("always") }
	err := NewEOTimerMonitor(repo, h, time.Hour).RunOnce(context.Background())
	if err == nil || len(repo.failures) != 1 {
		t.Fatalf("err = %v, failures = %d", err, len(repo.failures))
	}
}

func TestTimerMonitorBoundsHandlerRuntime(t *testing.T) {
	repo := &eoFakeTimerRepo{queue: eoTimers(1)}
	m := NewEOTimerMonitor(repo, func(ctx context.Context, _ models.EOTimer) error {
		<-ctx.Done() // a hung handler
		return ctx.Err()
	}, time.Hour).(*eoTimerMonitor)
	m.timeout = 20 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- m.RunOnce(context.Background()) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want deadline exceeded", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a hung handler was never cut off")
	}
	if eoTimerHandlerTimeout >= repository.EOTimerClaimLease {
		t.Errorf("handler timeout %v must be inside the lease %v", eoTimerHandlerTimeout, repository.EOTimerClaimLease)
	}
}

func TestTimerMonitorShutdownMidBatchReleasesTheRest(t *testing.T) {
	repo := &eoFakeTimerRepo{queue: eoTimers(5)}
	ctx, cancel := context.WithCancel(context.Background())
	ran := 0
	h := func(context.Context, models.EOTimer) error {
		ran++
		cancel() // Stop arrives during the first handler
		return nil
	}
	err := NewEOTimerMonitor(repo, h, time.Hour).RunOnce(ctx)
	if err == nil {
		t.Error("expected the cancellation to be reported")
	}
	if ran != 1 {
		t.Errorf("handler ran %d times after cancellation, want 1", ran)
	}
	if len(repo.released) != 4 {
		t.Errorf("released %d, want the 4 unprocessed timers handed back", len(repo.released))
	}
	if len(repo.failures) != 0 {
		t.Error("a shutdown recorded failures")
	}
}

// A handler that succeeds just as Stop arrives must still be recorded as fired:
// MarkFired on the cancelled run context would fail against a real database
// (the fake now honours the context) and leave the attempt counted.
func TestTimerMonitorRecordsASuccessThatRacesShutdown(t *testing.T) {
	repo := &eoFakeTimerRepo{queue: eoTimers(1)}
	ctx, cancel := context.WithCancel(context.Background())
	h := func(context.Context, models.EOTimer) error {
		cancel()
		return nil
	}
	err := NewEOTimerMonitor(repo, h, time.Hour).RunOnce(ctx)
	if err != nil {
		t.Errorf("err = %v", err)
	}
	if len(repo.fired) != 1 {
		t.Fatalf("fired %d, want the finished timer recorded despite the cancelled context", len(repo.fired))
	}
}

// Handlers run one after another, so the lease must be enforced across the
// batch: once the budget is spent the unrun tail is released, never run.
func TestTimerMonitorBatchBudgetStopsTheTail(t *testing.T) {
	repo := &eoFakeTimerRepo{queue: eoTimers(3)}
	ran := 0
	m := NewEOTimerMonitor(repo, func(ctx context.Context, _ models.EOTimer) error {
		ran++
		<-ctx.Done()
		return ctx.Err()
	}, time.Hour).(*eoTimerMonitor)
	m.budget = 20 * time.Millisecond
	_ = m.RunOnce(context.Background())
	if ran != 1 {
		t.Errorf("handlers run = %d, want 1: the tail ran past the budget", ran)
	}
	if len(repo.released) != 2 {
		t.Errorf("released %d, want the 2 unrun timers", len(repo.released))
	}
}

func TestTimerMonitorReleasesWithTheClaimItWasIssued(t *testing.T) {
	repo := &eoFakeTimerRepo{queue: eoTimers(2)}
	ctx, cancel := context.WithCancel(context.Background())
	h := func(context.Context, models.EOTimer) error { cancel(); return nil }
	_ = NewEOTimerMonitor(repo, h, time.Hour).RunOnce(ctx)
	if len(repo.released) != 1 || len(repo.releasedAt) != 1 || repo.releasedAt[0].IsZero() {
		t.Fatalf("released %v at %v: Release must carry the claimed_at the claim returned", repo.released, repo.releasedAt)
	}
}

// Outcome writes must carry the claimed_at the claim returned, or the
// repository's ownership predicate has nothing to compare.
func TestTimerMonitorOutcomeWritesCarryTheClaim(t *testing.T) {
	for _, fail := range []bool{false, true} {
		repo := &eoFakeTimerRepo{queue: eoTimers(1)}
		h := eoNoopHandler
		if fail {
			h = func(context.Context, models.EOTimer) error { return errors.New("x") }
		}
		_ = NewEOTimerMonitor(repo, h, time.Hour).RunOnce(context.Background())
		var got time.Time
		for _, v := range repo.markedAt {
			got = v
		}
		if got.IsZero() {
			t.Errorf("fail=%v: the outcome write carried no claimed_at", fail)
		}
	}
}

// ── the router ──────────────────────────────────────────────────────────────

func TestEoTimerRouterRoutesByCapabilityKey(t *testing.T) {
	cases := []struct {
		name         string
		key          string
		wantA, wantB int
	}{
		{"key a reaches handler a only", "cap.a", 1, 0},
		{"key b reaches handler b only", "cap.b", 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var a, b int
			router := NewEOTimerRouter(map[string]EOTimerHandler{
				"cap.a": func(context.Context, models.EOTimer) error { a++; return nil },
				"cap.b": func(context.Context, models.EOTimer) error { b++; return nil },
			})
			if err := router(context.Background(), models.EOTimer{CapabilityKey: tc.key}); err != nil {
				t.Fatalf("router: %v", err)
			}
			if a != tc.wantA || b != tc.wantB {
				t.Errorf("calls a=%d b=%d, want a=%d b=%d", a, b, tc.wantA, tc.wantB)
			}
		})
	}
}

func TestEoTimerRouterUnknownKeyIsAnError(t *testing.T) {
	called := false
	router := NewEOTimerRouter(map[string]EOTimerHandler{
		"cap.a": func(context.Context, models.EOTimer) error { called = true; return nil },
	})
	err := router(context.Background(), models.EOTimer{CapabilityKey: "cap.unclaimed"})
	if err == nil {
		t.Fatal("an unclaimed key returned nil: the monitor would mark the timer fired and it would vanish")
	}
	if !strings.Contains(err.Error(), "cap.unclaimed") {
		t.Errorf("error %q does not name the key", err)
	}
	if called {
		t.Error("a handler ran for a key it does not own")
	}
}

func TestEoTimerRouterPropagatesSentinel(t *testing.T) {
	router := NewEOTimerRouter(map[string]EOTimerHandler{
		"cap.a": func(context.Context, models.EOTimer) error {
			return fmt.Errorf("load record: %w", ErrEOTimerRecordGone)
		},
	})
	err := router(context.Background(), models.EOTimer{CapabilityKey: "cap.a"})
	if !errors.Is(err, ErrEOTimerRecordGone) {
		t.Errorf("err = %v, want it to wrap ErrEOTimerRecordGone", err)
	}
}

func TestEoTimerRouterCopiesTheMap(t *testing.T) {
	m := map[string]EOTimerHandler{"cap.a": eoNoopHandler}
	router := NewEOTimerRouter(m)
	delete(m, "cap.a")
	if err := router(context.Background(), models.EOTimer{CapabilityKey: "cap.a"}); err != nil {
		t.Errorf("mutating the caller's map changed routing: %v", err)
	}
}
