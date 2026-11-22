package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/automax/backend/internal/models"
)

// EOEscalationEventRepository stores EO's breach log (spec §7.8), a table of
// its own: the incident product's breach log is not touched.
type EOEscalationEventRepository interface {
	// Record inserts the event unless one already exists for the same record,
	// state, policy, step and breached_at, and reports whether it inserted.
	// It is what makes a timer's at-least-once delivery record one breach.
	Record(ctx context.Context, ev *models.EOEscalationEvent) (created bool, err error)
	// ListForRecord returns the record's events, earliest breach first.
	ListForRecord(ctx context.Context, recordID uuid.UUID) ([]models.EOEscalationEvent, error)
}

type eoEscalationEventRepository struct {
	db *gorm.DB
}

func NewEOEscalationEventRepository(db *gorm.DB) EOEscalationEventRepository {
	return &eoEscalationEventRepository{db: db}
}

func (r *eoEscalationEventRepository) Record(ctx context.Context, ev *models.EOEscalationEvent) (bool, error) {
	if ev.RecordID == uuid.Nil || ev.RecordTypeID == uuid.Nil || ev.StateID == uuid.Nil || ev.PolicyID == uuid.Nil || ev.BreachedAt.IsZero() {
		return false, fmt.Errorf("record escalation event: record_id, record_type_id, state_id, policy_id and breached_at are required")
	}
	res := r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(ev)
	if res.Error != nil {
		return false, fmt.Errorf("record escalation event: %w", res.Error)
	}
	return res.RowsAffected == 1, nil
}

func (r *eoEscalationEventRepository) ListForRecord(ctx context.Context, recordID uuid.UUID) ([]models.EOEscalationEvent, error) {
	var out []models.EOEscalationEvent
	if err := r.db.WithContext(ctx).Where("record_id = ?", recordID).
		Order("breached_at, step_index").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list escalation events: %w", err)
	}
	return out, nil
}
