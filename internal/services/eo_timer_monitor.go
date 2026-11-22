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
	// eoTimerHandlerTimeout bounds one handler call, and eoTimerBatchBudget
	// bounds the whole claimed batch, to just inside the claim lease. Handlers
	// run one after another, so a per-call limit alone would let timer n of 50
	// start long after the lease expired and another replica re-claimed it.
	// Once the budget is spent the unrun tail is released, not run.
	eoTimerHandlerTimeout = repository.EOTimerClaimLease - time.Minute
	eoTimerBatchBudget    = repository.EOTimerClaimLease - time.Minute
	// eoTimerBookkeepingTimeout bounds the writes that record an outcome. They
	// run on a context detached from the run's, so a shutdown cannot lose the
	// record of a timer that already fired.
	eoTimerBookkeepingTimeout = 5 * time.Second
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
	timeout  time.Duration
	budget   time.Duration

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
	return &eoTimerMonitor{repo: repo, handler: handler, interval: interval, now: time.Now, timeout: eoTimerHandlerTimeout, budget: eoTimerBatchBudget}
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
		batchCtx, cancelBatch := context.WithTimeout(ctx, m.budget)
		budgetSpent := false
		for i, tm := range timers {
			if ctx.Err() != nil {
				// Shutting down: the rest were claimed (an attempt counted)
				// but never run, so give them back instead of failing them.
				cancelBatch()
				m.release(timers[i:])
				return errors.Join(append(errs, ctx.Err())...)
			}
			if batchCtx.Err() != nil {
				// The lease is nearly up: another replica may legitimately
				// re-claim the rest. Hand them back (only while the claim is
				// still ours, see Release) and pick them up on the next pass.
				m.release(timers[i:])
				budgetSpent = true
				break
			}
			if err := m.fire(ctx, batchCtx, tm); err != nil {
				errs = append(errs, err)
			}
		}
		cancelBatch()
		if !budgetSpent && len(timers) < eoTimerBatchSize {
			return errors.Join(errs...)
		}
	}
}

// release returns unprocessed timers on a fresh, bounded context, since the
// run's own is already cancelled.
func (m *eoTimerMonitor) release(timers []models.EOTimer) {
	rctx, cancel := m.bookkeepingCtx(context.Background())
	defer cancel()
	for _, tm := range timers {
		if tm.ClaimedAt == nil {
			continue
		}
		if err := m.repo.Release(rctx, tm.ID, *tm.ClaimedAt); err != nil {
			log.Printf("[EOTimerMonitor] release timer %s: %v", tm.ID, err)
		}
	}
}

// eoClaimedAt is the claim's identity. A timer without one (never the case for
// a ClaimDue result) matches no row, so its writes are refused, not misapplied.
func eoClaimedAt(tm models.EOTimer) time.Time {
	if tm.ClaimedAt == nil {
		return time.Time{}
	}
	return *tm.ClaimedAt
}

// bookkeepingCtx detaches from parent's cancellation but keeps a deadline.
func (m *eoTimerMonitor) bookkeepingCtx(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), eoTimerBookkeepingTimeout)
}

// fire runs one timer. The handler gets batchCtx (so it cannot outlive the
// batch budget) under its own per-call timeout; every outcome write uses a
// detached context, so a shutdown that arrives as the handler returns still
// records the result.
func (m *eoTimerMonitor) fire(ctx, batchCtx context.Context, tm models.EOTimer) error {
	hctx, cancelHandler := context.WithTimeout(batchCtx, m.timeout)
	defer cancelHandler()
	err := m.handler(hctx, tm)
	bctx, cancel := m.bookkeepingCtx(ctx)
	defer cancel()
	switch {
	case err == nil:
		if err := m.repo.MarkFired(bctx, tm.ID, eoClaimedAt(tm)); err != nil {
			// Cancelled while the handler ran: nothing to record.
			if errors.Is(err, repository.ErrEOTimerNotClaimed) {
				return nil
			}
			return fmt.Errorf("timer %s fired but could not be marked: %w", tm.ID, err)
		}
		return nil
	case errors.Is(err, ErrEOTimerRecordGone):
		if cerr := m.repo.CancelForRecord(bctx, tm.RecordID); cerr != nil {
			return fmt.Errorf("timer %s: record gone, cancel failed: %w", tm.ID, cerr)
		}
		return nil
	case ctx.Err() != nil:
		// Interrupted by shutdown, not a genuine failure of the timer.
		m.release([]models.EOTimer{tm})
		return fmt.Errorf("timer %s interrupted: %w", tm.ID, err)
	default:
		// Stays claimed and is re-delivered after the lease, until it has used
		// its attempts, when the repository marks it failed.
		failed, rerr := m.repo.RecordFailure(bctx, tm.ID, eoClaimedAt(tm), err.Error())
		if rerr != nil && !errors.Is(rerr, repository.ErrEOTimerNotClaimed) {
			err = errors.Join(err, rerr)
		}
		if failed {
			log.Printf("[EOTimerMonitor] timer %s (%s) gave up after %d attempts: %v", tm.ID, tm.CapabilityKey, tm.Attempts, err)
		}
		return fmt.Errorf("timer %s (%s) failed: %w", tm.ID, tm.CapabilityKey, err)
	}
}
