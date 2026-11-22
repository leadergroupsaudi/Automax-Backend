package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/automax/backend/internal/models"
)

// EOTimerClaimLease is how long a claim holds. A timer claimed longer ago than
// this is treated as abandoned — its worker crashed, or its handler failed —
// and ClaimDue hands it out again. Delivery is therefore at-least-once: a
// handler must tolerate being run twice for the same timer.
const EOTimerClaimLease = 10 * time.Minute

// EOTimerMaxAttempts bounds delivery. Each claim counts as an attempt; a timer
// that has been claimed this many times without firing is marked failed instead
// of being retried again.
const EOTimerMaxAttempts = 5

// ErrEOTimerNotClaimed is returned by MarkFired when the timer is not in the
// claimed state: it was cancelled while its handler ran, or it does not exist.
var ErrEOTimerNotClaimed = errors.New("eo timer is not claimed")

// EOTimerRepository stores durable, record-scoped timers (spec §7.10).
type EOTimerRepository interface {
	// Schedule inserts a pending timer. FireAt, RecordID, RecordTypeID and
	// CapabilityKey are required.
	Schedule(ctx context.Context, t *models.EOTimer) error
	// ClaimDue atomically claims up to limit timers whose fire_at is at or
	// before now, earliest first, and returns them. A timer is returned to
	// exactly one concurrent caller: rows another claimer holds are skipped,
	// not waited for. Timers whose claim is older than EOTimerClaimLease are
	// claimable again.
	ClaimDue(ctx context.Context, now time.Time, limit int) ([]models.EOTimer, error)
	// MarkFired, RecordFailure and Release are the writes of the claim's
	// owner, so all three match on the claimed_at ClaimDue returned: a worker
	// whose lease expired and whose timer another worker re-claimed (a handler
	// that outlived its budget) writes nothing, and gets ErrEOTimerNotClaimed
	// (Release: nil), instead of marking fired, failing or releasing a run it
	// no longer owns.
	//
	// MarkFired records that a claimed timer's handler succeeded.
	MarkFired(ctx context.Context, id uuid.UUID, claimedAt time.Time) error
	// RecordFailure notes that a claimed timer's handler failed. It stores the
	// message and, once the timer has used EOTimerMaxAttempts attempts, marks
	// it failed (terminal); otherwise it stays claimed and is re-delivered
	// after the lease. It returns whether the timer is now failed.
	RecordFailure(ctx context.Context, id uuid.UUID, claimedAt time.Time, msg string) (failed bool, err error)
	// Release hands a claimed-but-unprocessed timer back (pending, the attempt
	// uncounted), but only while the caller still holds the claim: claimedAt
	// is the claimed_at that ClaimDue returned. If the lease expired and
	// another worker re-claimed the timer, its claimed_at differs and Release
	// does nothing, so it can never hand a live claim to a third worker. A worker that is shutting down uses it for the timers it
	// claimed and never ran, so an orderly stop cannot walk a healthy timer
	// towards failed.
	Release(ctx context.Context, id uuid.UUID, claimedAt time.Time) error
	// CancelForRecord cancels every timer of the record that has not fired.
	CancelForRecord(ctx context.Context, recordID uuid.UUID) error
	// WithTx returns a repository whose every call runs on tx. Schedule and
	// CancelForRecord belong in the same transaction as the record change that
	// motivates them: on the pooled handle a Schedule commits on its own
	// connection and survives a rolled-back transition. Inside a transaction,
	// use only the bound handle (a pooled call takes a second connection).
	WithTx(tx *gorm.DB) EOTimerRepository
}

type eoTimerRepository struct {
	db *gorm.DB
}

func NewEOTimerRepository(db *gorm.DB) EOTimerRepository {
	return &eoTimerRepository{db: db}
}

func (r *eoTimerRepository) WithTx(tx *gorm.DB) EOTimerRepository {
	return &eoTimerRepository{db: tx}
}

func (r *eoTimerRepository) Release(ctx context.Context, id uuid.UUID, claimedAt time.Time) error {
	if err := r.db.WithContext(ctx).Exec(
		`UPDATE eo_timers SET status = 'pending', claimed_at = NULL, attempts = GREATEST(attempts - 1, 0)
		 WHERE id = ? AND status = 'claimed' AND claimed_at = ?`, id, claimedAt).Error; err != nil {
		return fmt.Errorf("release timer: %w", err)
	}
	return nil
}

func (r *eoTimerRepository) Schedule(ctx context.Context, t *models.EOTimer) error {
	if t.FireAt.IsZero() || t.RecordID == uuid.Nil || t.RecordTypeID == uuid.Nil || t.CapabilityKey == "" {
		return errors.New("schedule timer: fire_at, record_id, record_type_id and capability_key are required")
	}
	t.Status = models.EOTimerPending
	t.ClaimedAt = nil
	t.Attempts = 0
	t.LastError = ""
	if len(t.Payload) == 0 {
		t.Payload = []byte(`{}`)
	}
	if err := r.db.WithContext(ctx).Create(t).Error; err != nil {
		return fmt.Errorf("schedule timer: %w", err)
	}
	return nil
}

// eoClaimDueSQL claims in one statement: the CTE picks and row-locks the
// candidates with FOR UPDATE SKIP LOCKED, and the UPDATE flips them to claimed
// (counting the attempt) in the same statement, so there is no window between
// "chosen" and "claimed" for a second worker to slip through.
//
// The CTE is MATERIALIZED on purpose. Written as `WHERE id IN (SELECT ... LIMIT
// n FOR UPDATE SKIP LOCKED)` the planner may run the subquery as a nested loop,
// re-evaluating the LIMIT per outer row and claiming more than n timers (the
// limit test caught exactly this). Materialising evaluates it once.
//
// The lease runs on the DATABASE clock (now(), interval), never the caller's:
// with several replicas, one whose clock runs fast would otherwise compute a
// cut-off that expires another replica's fresh claim and run the timer twice.
// Only fire_at is compared with the caller's now, as the interface requires.
//
// Abandoned claims (older than the lease) are only reclaimed while attempts <
// max. The `dead` CTE gives up on the rest, which covers a timer that kills its
// worker before RecordFailure can run.
const eoClaimDueSQL = `
WITH dead AS (
	UPDATE eo_timers SET status = 'failed',
		last_error = CASE WHEN last_error = '' THEN 'abandoned: worker did not finish' ELSE last_error END
	WHERE status = 'claimed' AND claimed_at <= now() - make_interval(secs => @lease) AND attempts >= @max
),
due AS MATERIALIZED (
	SELECT id FROM eo_timers
	WHERE fire_at <= @now
	  AND (status = 'pending' OR (status = 'claimed' AND claimed_at <= now() - make_interval(secs => @lease) AND attempts < @max))
	ORDER BY fire_at, id
	LIMIT @limit
	FOR UPDATE SKIP LOCKED
)
UPDATE eo_timers t SET status = 'claimed', claimed_at = now(), attempts = t.attempts + 1
FROM due
WHERE t.id = due.id
RETURNING t.*`

func (r *eoTimerRepository) ClaimDue(ctx context.Context, now time.Time, limit int) ([]models.EOTimer, error) {
	if limit <= 0 {
		return nil, nil
	}
	var out []models.EOTimer
	err := r.db.WithContext(ctx).Raw(eoClaimDueSQL, map[string]any{
		"now":   now,
		"lease": EOTimerClaimLease.Seconds(),
		"limit": limit,
		"max":   EOTimerMaxAttempts,
	}).Scan(&out).Error
	if err != nil {
		return nil, fmt.Errorf("claim due timers: %w", err)
	}
	return out, nil
}

func (r *eoTimerRepository) MarkFired(ctx context.Context, id uuid.UUID, claimedAt time.Time) error {
	res := r.db.WithContext(ctx).Exec(
		`UPDATE eo_timers SET status = 'fired' WHERE id = ? AND status = 'claimed' AND claimed_at = ?`, id, claimedAt)
	if res.Error != nil {
		return fmt.Errorf("mark timer fired: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrEOTimerNotClaimed
	}
	return nil
}

func (r *eoTimerRepository) RecordFailure(ctx context.Context, id uuid.UUID, claimedAt time.Time, msg string) (bool, error) {
	var status string
	res := r.db.WithContext(ctx).Raw(
		`UPDATE eo_timers SET last_error = ?,
			status = CASE WHEN attempts >= ? THEN 'failed' ELSE status END
		 WHERE id = ? AND status = 'claimed' AND claimed_at = ?
		 RETURNING status`, msg, EOTimerMaxAttempts, id, claimedAt).Scan(&status)
	if res.Error != nil {
		return false, fmt.Errorf("record timer failure: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return false, ErrEOTimerNotClaimed
	}
	return status == models.EOTimerFailed, nil
}

// CancelForRecord is deliberately NOT keyed on a claim, unlike the three writes
// above: it cancels by record, for a caller that is deleting the record and so
// is not competing for any one timer. It must win over whoever holds a claim.
func (r *eoTimerRepository) CancelForRecord(ctx context.Context, recordID uuid.UUID) error {
	if err := r.db.WithContext(ctx).Exec(
		`UPDATE eo_timers SET status = 'cancelled' WHERE record_id = ? AND status IN ('pending', 'claimed')`,
		recordID).Error; err != nil {
		return fmt.Errorf("cancel timers for record: %w", err)
	}
	return nil
}
