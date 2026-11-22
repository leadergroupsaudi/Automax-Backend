package repository

import (
	"context"
	"fmt"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// eoMaxTypeDepth bounds the record-type ancestor walk. Real hierarchies are a
// few levels deep; the bound is a backstop that turns a corrupted chain into
// an error instead of a loop inside a request handler. It matches the bound
// services.ResolveEffectiveFields applies to the chain it is handed.
const eoMaxTypeDepth = 32

// EORecordTypeRepository reads the metamodel: record types, their field
// definitions and the link types. Like the record repository it returns
// (nil, nil) for a type nobody has configured.
type EORecordTypeRepository interface {
	GetByID(ctx context.Context, id uuid.UUID) (*models.EORecordType, error)
	GetByKey(ctx context.Context, key string) (*models.EORecordType, error)
	// Ancestors returns t's ancestors nearest-first (parent, grandparent, …),
	// not including t, which is the shape services.ResolveEffectiveFields
	// takes. A cycle or a chain deeper than eoMaxTypeDepth is an error.
	Ancestors(ctx context.Context, t *models.EORecordType) ([]models.EORecordType, error)
	// FieldDefs returns the field definitions declared by the named types,
	// each type's own only: inheritance is resolved in the service layer at
	// read time and nothing is copied (spec §7.2).
	FieldDefs(ctx context.Context, recordTypeIDs []uuid.UUID) ([]models.EOFieldDef, error)
	GetLinkTypeByKey(ctx context.Context, key string) (*models.EOLinkType, error)
}

type eoRecordTypeRepository struct {
	db *gorm.DB
}

func NewEORecordTypeRepository(db *gorm.DB) EORecordTypeRepository {
	return &eoRecordTypeRepository{db: db}
}

func (r *eoRecordTypeRepository) GetByID(ctx context.Context, id uuid.UUID) (*models.EORecordType, error) {
	return eoFirstType(r.db.WithContext(ctx).Where("eo_record_types.id = ?", id))
}

func (r *eoRecordTypeRepository) GetByKey(ctx context.Context, key string) (*models.EORecordType, error) {
	return eoFirstType(r.db.WithContext(ctx).Where("eo_record_types.key = ?", key))
}

// eoFirstType reads at most one record type, with Find for the same reason
// eoFirstRecord uses it: an unconfigured key is an answer, not an error.
func eoFirstType(q *gorm.DB) (*models.EORecordType, error) {
	var out []models.EORecordType
	if err := q.Limit(1).Find(&out).Error; err != nil {
		return nil, fmt.Errorf("read eo record type: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return &out[0], nil
}

func (r *eoRecordTypeRepository) Ancestors(ctx context.Context, t *models.EORecordType) ([]models.EORecordType, error) {
	if t == nil {
		return nil, fmt.Errorf("eo record type ancestors: nil record type")
	}
	var out []models.EORecordType
	visited := map[uuid.UUID]bool{t.ID: true}
	for cur := t; cur.ParentTypeID != nil; {
		pid := *cur.ParentTypeID
		if visited[pid] {
			return nil, fmt.Errorf("eo record type ancestors: inheritance cycle at record type %s", pid)
		}
		if len(out) >= eoMaxTypeDepth {
			return nil, fmt.Errorf("eo record type ancestors: inheritance deeper than %d levels", eoMaxTypeDepth)
		}
		parent, err := r.GetByID(ctx, pid)
		if err != nil {
			return nil, err
		}
		if parent == nil {
			return nil, fmt.Errorf("eo record type ancestors: parent %s of record type %s not found", pid, cur.ID)
		}
		visited[pid] = true
		out = append(out, *parent)
		cur = parent
	}
	return out, nil
}

func (r *eoRecordTypeRepository) FieldDefs(ctx context.Context, recordTypeIDs []uuid.UUID) ([]models.EOFieldDef, error) {
	if len(recordTypeIDs) == 0 {
		return nil, nil
	}
	var out []models.EOFieldDef
	if err := r.db.WithContext(ctx).
		Model(&models.EOFieldDef{}).
		Where("eo_field_defs.record_type_id IN ?", recordTypeIDs).
		Order("eo_field_defs.sort_order ASC, eo_field_defs.key ASC").
		Find(&out).Error; err != nil {
		return nil, fmt.Errorf("read eo field defs: %w", err)
	}
	return out, nil
}

func (r *eoRecordTypeRepository) GetLinkTypeByKey(ctx context.Context, key string) (*models.EOLinkType, error) {
	var out []models.EOLinkType
	if err := r.db.WithContext(ctx).
		Where("eo_link_types.key = ?", key).
		Limit(1).Find(&out).Error; err != nil {
		return nil, fmt.Errorf("read eo link type %q: %w", key, err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return &out[0], nil
}
