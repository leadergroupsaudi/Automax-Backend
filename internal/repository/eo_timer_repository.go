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
	// MarkFired records that a claimed timer's handler succeeded.
	MarkFired(ctx context.Context, id uuid.UUID) error
	// CancelForRecord cancels every timer of the record that has not fired.
	CancelForRecord(ctx context.Context, recordID uuid.UUID) error
}

type eoTimerRepository struct {
	db *gorm.DB
}

func NewEOTimerRepository(db *gorm.DB) EOTimerRepository {
	return &eoTimerRepository{db: db}
}

func (r *eoTimerRepository) Schedule(ctx context.Context, t *models.EOTimer) error {
	if t.FireAt.IsZero() || t.RecordID == uuid.Nil || t.RecordTypeID == uuid.Nil || t.CapabilityKey == "" {
		return errors.New("schedule timer: fire_at, record_id, record_type_id and capability_key are required")
	}
	t.Status = models.EOTimerPending
	t.ClaimedAt = nil
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
// in the same statement, so there is no window between "chosen" and "claimed"
// for a second worker to slip through.
//
// The CTE is MATERIALIZED on purpose. Written as `WHERE id IN (SELECT ... LIMIT
// n FOR UPDATE SKIP LOCKED)` the planner may run the subquery as a nested loop,
// re-evaluating the LIMIT per outer row and claiming more than n timers (the
// limit test caught exactly this). Materialising evaluates it once.
const eoClaimDueSQL = `
WITH due AS MATERIALIZED (
	SELECT id FROM eo_timers
	WHERE fire_at <= @now
	  AND (status = 'pending' OR (status = 'claimed' AND claimed_at <= @stale))
	ORDER BY fire_at, id
	LIMIT @limit
	FOR UPDATE SKIP LOCKED
)
UPDATE eo_timers t SET status = 'claimed', claimed_at = @now
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
		"stale": now.Add(-EOTimerClaimLease),
		"limit": limit,
	}).Scan(&out).Error
	if err != nil {
		return nil, fmt.Errorf("claim due timers: %w", err)
	}
	return out, nil
}

func (r *eoTimerRepository) MarkFired(ctx context.Context, id uuid.UUID) error {
	res := r.db.WithContext(ctx).Exec(
		`UPDATE eo_timers SET status = 'fired' WHERE id = ? AND status = 'claimed'`, id)
	if res.Error != nil {
		return fmt.Errorf("mark timer fired: %w", res.Error)
	}
	if res.RowsAffected == 0 {
		return ErrEOTimerNotClaimed
	}
	return nil
}

func (r *eoTimerRepository) CancelForRecord(ctx context.Context, recordID uuid.UUID) error {
	if err := r.db.WithContext(ctx).Exec(
		`UPDATE eo_timers SET status = 'cancelled' WHERE record_id = ? AND status IN ('pending', 'claimed')`,
		recordID).Error; err != nil {
		return fmt.Errorf("cancel timers for record: %w", err)
	}
	return nil
}
