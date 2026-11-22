package services

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
)

const (
	// eoTimerDefaultInterval is used when the configured interval is zero or
	// negative. Unlike the five-minute polls (spec §7.10) it is short, because
	// a timer is only as punctual as the poll that finds it.
	eoTimerDefaultInterval = 30 * time.Second
	// eoTimerBatchSize is how many timers one ClaimDue call takes. RunOnce keeps
	// claiming until a batch comes back short.
	eoTimerBatchSize = 50
)

// ErrEOTimerRecordGone is what a handler returns when the timer's record no
// longer exists (soft-deleted or never committed). The monitor cancels the
// record's remaining timers instead of leaving them to be retried forever.
var ErrEOTimerRecordGone = errors.New("eo timer: record no longer exists")

// EOTimerHandler fires one claimed timer.
//
// Delivery is at-least-once: a timer whose handler fails, or whose worker dies,
// is claimed again once its lease expires (repository.EOTimerClaimLease), so a
// handler must be idempotent.
//
// The context carries NO actor (WithEOActor): a worker has no HTTP user, and a
// capability hook that needs one — the approval capability errors without it —
// must not be reached from a timer. A handler that acts on a record must do so
// as an explicit system actor of its own choosing; the monitor does not invent
// one, because attributing a system action to a real user would falsify the
// audit trail.
type EOTimerHandler func(ctx context.Context, timer models.EOTimer) error

// EOTimerMonitor fires due durable timers. It is replica-safe: ClaimDue hands
// each timer to exactly one worker, so any number of API replicas may run one.
type EOTimerMonitor interface {
	Start(ctx context.Context)
	Stop()
	RunOnce(ctx context.Context) error
}

type eoTimerMonitor struct {
	repo     repository.EOTimerRepository
	handler  EOTimerHandler
	interval time.Duration
	now      func() time.Time

	mu      sync.Mutex
	running bool
	gen     int
	cancel  context.CancelFunc
}

// NewEOTimerMonitor builds the worker. A zero or negative interval falls back
// to eoTimerDefaultInterval rather than panicking in time.NewTicker.
func NewEOTimerMonitor(repo repository.EOTimerRepository, handler EOTimerHandler, interval time.Duration) EOTimerMonitor {
	if interval <= 0 {
		interval = eoTimerDefaultInterval
	}
	return &eoTimerMonitor{repo: repo, handler: handler, interval: interval, now: time.Now}
}

// Start runs once immediately and then every interval, until Stop or ctx is
// cancelled. Calling it while already running does nothing.
func (m *eoTimerMonitor) Start(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	m.running, m.cancel = true, cancel
	m.gen++
	gen := m.gen
	log.Printf("[EOTimerMonitor] started: interval=%v", m.interval)

	go func() {
		defer func() {
			cancel()
			// If ctx was cancelled from outside, Stop was never called: mark
			// the monitor stopped so it can be started again.
			m.mu.Lock()
			if m.gen == gen {
				m.running = false
			}
			m.mu.Unlock()
		}()
		if err := m.RunOnce(runCtx); err != nil {
			log.Printf("[EOTimerMonitor] initial run failed: %v", err)
		}
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if err := m.RunOnce(runCtx); err != nil {
					log.Printf("[EOTimerMonitor] run failed: %v", err)
				}
			case <-runCtx.Done():
				log.Println("[EOTimerMonitor] stopped")
				return
			}
		}
	}()
}

// Stop ends the loop. It is safe before Start and safe to call repeatedly. It
// does not wait for a run in progress; that run's context is cancelled, and any
// timer it had claimed but not finished is re-claimed after its lease.
func (m *eoTimerMonitor) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.running {
		return
	}
	m.running = false
	m.cancel()
}

// RunOnce claims every due timer and fires each. One timer failing does not
// stop the others; the failures come back joined.
func (m *eoTimerMonitor) RunOnce(ctx context.Context) error {
	if m.handler == nil {
		return errors.New("eo timer monitor has no handler")
	}
	var errs []error
	for {
		if err := ctx.Err(); err != nil {
			return errors.Join(append(errs, err)...)
		}
		timers, err := m.repo.ClaimDue(ctx, m.now(), eoTimerBatchSize)
		if err != nil {
			return errors.Join(append(errs, err)...)
		}
		for _, tm := range timers {
			if err := m.fire(ctx, tm); err != nil {
				errs = append(errs, err)
			}
		}
		if len(timers) < eoTimerBatchSize {
			return errors.Join(errs...)
		}
	}
}

func (m *eoTimerMonitor) fire(ctx context.Context, tm models.EOTimer) error {
	err := m.handler(ctx, tm)
	switch {
	case err == nil:
		if err := m.repo.MarkFired(ctx, tm.ID); err != nil {
			// Cancelled while the handler ran: nothing to record.
			if errors.Is(err, repository.ErrEOTimerNotClaimed) {
				return nil
			}
			return fmt.Errorf("timer %s fired but could not be marked: %w", tm.ID, err)
		}
		return nil
	case errors.Is(err, ErrEOTimerRecordGone):
		if cerr := m.repo.CancelForRecord(ctx, tm.RecordID); cerr != nil {
			return fmt.Errorf("timer %s: record gone, cancel failed: %w", tm.ID, cerr)
		}
		return nil
	default:
		// Left claimed: re-delivered after the lease expires.
		return fmt.Errorf("timer %s (%s) failed: %w", tm.ID, tm.CapabilityKey, err)
	}
}
