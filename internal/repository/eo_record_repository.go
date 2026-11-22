package repository

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// eoReturningStamps reads the DB-stamped created_at and updated_at back into
// the inserted record, so every EO timestamp comes from the one server clock.
var eoReturningStamps = clause.Returning{Columns: []clause.Column{{Name: "created_at"}, {Name: "updated_at"}}}

// EORecordRepository is the only place SQL touches eo_records (spec §7.7): the
// engine, capabilities, designer and seeds address records through it, so a
// later deployment can sub-partition or promote columns without touching them.
//
// eo_records has the composite primary key (id, record_type_id) — Postgres
// requires the partition key in every unique constraint — so every lookup
// takes the record type beside the id, no GORM association is declared on
// models.EORecord, and every query spells its Where clause out: First(&rec,
// id), Preload and the association machinery assume a single-column key.
//
// A getter returns (nil, nil) when nothing matches. Absence is an ordinary
// answer here (a parent that was deleted, a reference nobody issued); callers
// decide whether it is an error, and none of them has to know gorm's sentinel.
type EORecordRepository interface {
	Create(ctx context.Context, rec *models.EORecord) error
	GetByID(ctx context.Context, id, recordTypeID uuid.UUID) (*models.EORecord, error)
	// GetAnyByID finds a record by id alone, across every partition. It
	// exists for the parent walk: parent_record_id carries no record type, so
	// the type cannot be supplied. Use GetByID wherever the type is known.
	GetAnyByID(ctx context.Context, id uuid.UUID) (*models.EORecord, error)
	GetByReference(ctx context.Context, reference string) (*models.EORecord, error)
	Update(ctx context.Context, rec *models.EORecord) error
	// UpdateValues merges keys into the values jsonb; keys it does not
	// mention keep their stored value.
	UpdateValues(ctx context.Context, id, recordTypeID uuid.UUID, values map[string]any) error
	// Children returns the DIRECT children of parentID, optionally of one
	// record type. Direct, not descendant: services.BlockingChildren
	// identifies a child by membership in the slice it is given and ignores
	// links whose endpoints are both inside it, so a transitive set would
	// silently stop blocking.
	Children(ctx context.Context, parentID uuid.UUID, recordTypeID *uuid.UUID) ([]models.EORecord, error)
	CreateLink(ctx context.Context, link *models.EORecordLink) error
	LinksFrom(ctx context.Context, recordID uuid.UUID) ([]models.EORecordLink, error)
	CreateHistory(ctx context.Context, entry *models.EORecordHistory) error
	// EnteredStateIDs is every state the record has ever entered, from
	// eo_record_history. It is not the current state: is_locked_after asks
	// whether a record has ever been in a state, and EO workflows have return
	// and exception paths, so the two answers differ.
	EnteredStateIDs(ctx context.Context, recordID, recordTypeID uuid.UUID) (map[uuid.UUID]bool, error)
	// Transaction runs fn against a repository bound to one database
	// transaction, committing when fn returns nil and rolling back otherwise.
	// A record, its parent link and its first history entry are one write.
	Transaction(ctx context.Context, fn func(EORecordRepository) error) error
}

type eoRecordRepository struct {
	db *gorm.DB
}

func NewEORecordRepository(db *gorm.DB) EORecordRepository {
	return &eoRecordRepository{db: db}
}

// Create inserts the record. created_at and updated_at are left to the column
// defaults so they come from the database clock rather than the application
// host's; RETURNING hands the stored values back into rec.
func (r *eoRecordRepository) Create(ctx context.Context, rec *models.EORecord) error {
	if err := r.db.WithContext(ctx).
		Omit("CreatedAt", "UpdatedAt").
		Clauses(eoReturningStamps).
		Create(rec).Error; err != nil {
		return fmt.Errorf("create eo record %q: %w", rec.Reference, err)
	}
	return nil
}

func (r *eoRecordRepository) GetByID(ctx context.Context, id, recordTypeID uuid.UUID) (*models.EORecord, error) {
	return eoFirstRecord(r.db.WithContext(ctx).
		Where("eo_records.id = ? AND eo_records.record_type_id = ?", id, recordTypeID))
}

func (r *eoRecordRepository) GetAnyByID(ctx context.Context, id uuid.UUID) (*models.EORecord, error) {
	return eoFirstRecord(r.db.WithContext(ctx).Where("eo_records.id = ?", id))
}

func (r *eoRecordRepository) GetByReference(ctx context.Context, reference string) (*models.EORecord, error) {
	return eoFirstRecord(r.db.WithContext(ctx).Where("eo_records.reference = ?", reference))
}

// eoFirstRecord reads at most one record. It uses Find rather than First
// because "no such record" is an ordinary answer here: First would return
// gorm.ErrRecordNotFound and log it as an error on every miss.
func eoFirstRecord(q *gorm.DB) (*models.EORecord, error) {
	var out []models.EORecord
	if err := q.Limit(1).Find(&out).Error; err != nil {
		return nil, fmt.Errorf("read eo record: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return &out[0], nil
}

// eoUpdatableColumns are the columns Update writes. They are listed instead of
// handing GORM the struct because a struct update skips every zero value, so
// clearing a due date or blanking a priority would silently keep the old one.
// id, record_type_id, created_by_id and created_at are immutable; updated_at
// is set from the database clock.
func eoUpdatableColumns(rec *models.EORecord) map[string]any {
	return map[string]any{
		"reference":        rec.Reference,
		"subject_ar":       rec.SubjectAr,
		"subject_en":       rec.SubjectEn,
		"parent_record_id": rec.ParentRecordID,
		"state_id":         rec.StateID,
		"priority":         rec.Priority,
		"sensitivity":      rec.Sensitivity,
		"source":           rec.Source,
		"owning_body_id":   rec.OwningBodyID,
		"assigned_user_id": rec.AssignedUserID,
		"due_date":         rec.DueDate,
		"closed_at":        rec.ClosedAt,
		"change_reason":    rec.ChangeReason,
		"delay_reason":     rec.DelayReason,
		"values":           rec.Values,
		"updated_at":       gorm.Expr("NOW()"),
	}
}

func (r *eoRecordRepository) Update(ctx context.Context, rec *models.EORecord) error {
	res := r.db.WithContext(ctx).
		Model(&models.EORecord{}).
		Where("eo_records.id = ? AND eo_records.record_type_id = ?", rec.ID, rec.RecordTypeID).
		Updates(eoUpdatableColumns(rec))
	if res.Error != nil {
		return fmt.Errorf("update eo record %s: %w", rec.ID, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("update eo record %s: no live record of type %s", rec.ID, rec.RecordTypeID)
	}
	return nil
}

// UpdateValues merges values into the jsonb bag with the || operator, so keys
// it does not mention survive. "values" is a Postgres reserved word: GORM
// quotes it when it builds a statement from the column tag, but never inside a
// raw fragment, so it is quoted by hand here.
func (r *eoRecordRepository) UpdateValues(ctx context.Context, id, recordTypeID uuid.UUID, values map[string]any) error {
	patch, err := json.Marshal(values)
	if err != nil {
		return fmt.Errorf("update eo record %s values: encode: %w", id, err)
	}
	res := r.db.WithContext(ctx).Exec(
		`UPDATE eo_records
		 SET "values" = eo_records."values" || ?::jsonb, updated_at = NOW()
		 WHERE eo_records.id = ? AND eo_records.record_type_id = ? AND eo_records.deleted_at IS NULL`,
		string(patch), id, recordTypeID)
	if res.Error != nil {
		return fmt.Errorf("update eo record %s values: %w", id, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("update eo record %s values: no live record of type %s", id, recordTypeID)
	}
	return nil
}

func (r *eoRecordRepository) Children(ctx context.Context, parentID uuid.UUID, recordTypeID *uuid.UUID) ([]models.EORecord, error) {
	q := r.db.WithContext(ctx).
		Model(&models.EORecord{}).
		Where("eo_records.parent_record_id = ?", parentID)
	if recordTypeID != nil {
		q = q.Where("eo_records.record_type_id = ?", *recordTypeID)
	}
	var out []models.EORecord
	if err := q.Order("eo_records.created_at ASC, eo_records.id ASC").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list children of eo record %s: %w", parentID, err)
	}
	return out, nil
}

func (r *eoRecordRepository) CreateLink(ctx context.Context, link *models.EORecordLink) error {
	if err := r.db.WithContext(ctx).Create(link).Error; err != nil {
		return fmt.Errorf("create eo record link %q: %w", link.LinkTypeKey, err)
	}
	return nil
}

func (r *eoRecordRepository) LinksFrom(ctx context.Context, recordID uuid.UUID) ([]models.EORecordLink, error) {
	var out []models.EORecordLink
	if err := r.db.WithContext(ctx).
		Model(&models.EORecordLink{}).
		Where("eo_record_links.from_record_id = ?", recordID).
		Order("eo_record_links.created_at ASC, eo_record_links.id ASC").
		Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list links from eo record %s: %w", recordID, err)
	}
	return out, nil
}

func (r *eoRecordRepository) CreateHistory(ctx context.Context, entry *models.EORecordHistory) error {
	if err := r.db.WithContext(ctx).Create(entry).Error; err != nil {
		return fmt.Errorf("create eo record history for %s: %w", entry.RecordID, err)
	}
	return nil
}

func (r *eoRecordRepository) EnteredStateIDs(ctx context.Context, recordID, recordTypeID uuid.UUID) (map[uuid.UUID]bool, error) {
	var ids []uuid.UUID
	if err := r.db.WithContext(ctx).
		Model(&models.EORecordHistory{}).
		Where("eo_record_history.record_id = ? AND eo_record_history.record_type_id = ? AND eo_record_history.to_state_id IS NOT NULL",
			recordID, recordTypeID).
		Distinct().
		Pluck("eo_record_history.to_state_id", &ids).Error; err != nil {
		return nil, fmt.Errorf("read entered states of eo record %s: %w", recordID, err)
	}
	out := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

func (r *eoRecordRepository) Transaction(ctx context.Context, fn func(EORecordRepository) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(&eoRecordRepository{db: tx})
	})
}
