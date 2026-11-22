package services

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
)

// eoFakeTimerRepo hands out its queued timers once each and records what the
// monitor did with them. Claim semantics are the repository's job and are
// tested against Postgres; this only drives the monitor.
type eoFakeTimerRepo struct {
	mu        sync.Mutex
	queue     []models.EOTimer
	fired     []uuid.UUID
	cancelled []uuid.UUID
	claims    int
	claimErr  error
	fireErr   error
	batches   []int
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
	out := f.queue[:n]
	f.queue = f.queue[n:]
	return out, nil
}

func (f *eoFakeTimerRepo) MarkFired(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fireErr != nil {
		return f.fireErr
	}
	f.fired = append(f.fired, id)
	return nil
}

func (f *eoFakeTimerRepo) CancelForRecord(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancelled = append(f.cancelled, id)
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
