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
	// ChildTypes returns the types that name parentID as their
	// parent_type_id — DIRECT children only. It is how the designer asks "is
	// anything inheriting from this type"; no other read answers that, because
	// Ancestors walks upward.
	ChildTypes(ctx context.Context, parentID uuid.UUID) ([]models.EORecordType, error)
	// DescendantIDs is rootID plus every type beneath it, transitively.
	//
	// It exists because §7.14's guard rails are conditional on a type's
	// RECORDS, and an ABSTRACT type never holds any of its own: the records an
	// edit to it would damage belong to its descendants. Asking only about the
	// type's own records leaves every base and intermediate type permanently
	// unguarded. A cycle, or a subtree deeper than eoMaxTypeDepth, is an
	// error — the same bound Ancestors applies walking the other way.
	DescendantIDs(ctx context.Context, rootID uuid.UUID) ([]uuid.UUID, error)
	// FieldDefs returns the field definitions declared by the named types,
	// each type's own only: inheritance is resolved in the service layer at
	// read time and nothing is copied (spec §7.2).
	FieldDefs(ctx context.Context, recordTypeIDs []uuid.UUID) ([]models.EOFieldDef, error)
	GetLinkTypeByKey(ctx context.Context, key string) (*models.EOLinkType, error)

	// ── the designer's writes ────────────────────────────────────────────
	//
	// The metamodel is configuration, not a register: these are plain soft
	// -deleting CRUD. The GUARD RAILS of spec §7.14 (what may be changed once
	// records exist) are models.ValidateTypeEdit's, applied by the caller
	// BEFORE it writes — the repository does not re-check them, exactly as
	// the record repository does not re-run field validation.

	// List returns every configured record type, ordered by key.
	List(ctx context.Context) ([]models.EORecordType, error)
	Create(ctx context.Context, t *models.EORecordType) error
	Update(ctx context.Context, t *models.EORecordType) error
	// Delete soft-deletes a record type. Its field definitions are left
	// alone: a mistaken delete stays recoverable, which is the same reason
	// ValidateTypeEdit never removes a field's stored data.
	Delete(ctx context.Context, id uuid.UUID) error

	GetFieldDef(ctx context.Context, id uuid.UUID) (*models.EOFieldDef, error)
	CreateFieldDef(ctx context.Context, f *models.EOFieldDef) error
	UpdateFieldDef(ctx context.Context, f *models.EOFieldDef) error
	DeleteFieldDef(ctx context.Context, id uuid.UUID) error

	ListLinkTypes(ctx context.Context) ([]models.EOLinkType, error)
	GetLinkTypeByID(ctx context.Context, id uuid.UUID) (*models.EOLinkType, error)
	CreateLinkType(ctx context.Context, lt *models.EOLinkType) error
	UpdateLinkType(ctx context.Context, lt *models.EOLinkType) error
	DeleteLinkType(ctx context.Context, id uuid.UUID) error
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

func (r *eoRecordTypeRepository) ChildTypes(ctx context.Context, parentID uuid.UUID) ([]models.EORecordType, error) {
	var out []models.EORecordType
	if err := r.db.WithContext(ctx).
		Model(&models.EORecordType{}).
		Where("eo_record_types.parent_type_id = ?", parentID).
		Order("eo_record_types.key ASC").
		Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list child record types of %s: %w", parentID, err)
	}
	return out, nil
}

// DescendantIDs walks the tree downward, one level per query. It is a
// breadth-first walk rather than a recursive CTE because the designer's trees
// are a handful of levels deep and the query stays an ordinary indexed read on
// parent_type_id, which the model already declares an index for.
func (r *eoRecordTypeRepository) DescendantIDs(ctx context.Context, rootID uuid.UUID) ([]uuid.UUID, error) {
	out := []uuid.UUID{rootID}
	seen := map[uuid.UUID]bool{rootID: true}
	frontier := []uuid.UUID{rootID}
	for depth := 0; len(frontier) > 0; depth++ {
		if depth >= eoMaxTypeDepth {
			return nil, fmt.Errorf("eo record type descendants: inheritance deeper than %d levels below record type %s", eoMaxTypeDepth, rootID)
		}
		var next []uuid.UUID
		for _, id := range frontier {
			children, err := r.ChildTypes(ctx, id)
			if err != nil {
				return nil, err
			}
			for _, c := range children {
				if seen[c.ID] {
					// A type that is its own ancestor would otherwise loop
					// here forever; Ancestors refuses the same shape.
					return nil, fmt.Errorf("eo record type descendants: inheritance cycle at record type %s", c.ID)
				}
				seen[c.ID] = true
				out = append(out, c.ID)
				next = append(next, c.ID)
			}
		}
		frontier = next
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

// ── metamodel writes ────────────────────────────────────────────────────────

func (r *eoRecordTypeRepository) List(ctx context.Context) ([]models.EORecordType, error) {
	var out []models.EORecordType
	if err := r.db.WithContext(ctx).
		Model(&models.EORecordType{}).
		Order("eo_record_types.key ASC").
		Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list eo record types: %w", err)
	}
	return out, nil
}

func (r *eoRecordTypeRepository) Create(ctx context.Context, t *models.EORecordType) error {
	if t == nil {
		return fmt.Errorf("create eo record type: nil record type")
	}
	if err := r.db.WithContext(ctx).Create(t).Error; err != nil {
		return fmt.Errorf("create eo record type %q: %w", t.Key, err)
	}
	return nil
}

// Update writes every column with Save rather than a field map: the caller has
// already built the full after-state and had models.ValidateTypeEdit pass on
// it, so a partial write would store something nothing validated.
func (r *eoRecordTypeRepository) Update(ctx context.Context, t *models.EORecordType) error {
	if t == nil {
		return fmt.Errorf("update eo record type: nil record type")
	}
	if err := r.db.WithContext(ctx).Save(t).Error; err != nil {
		return fmt.Errorf("update eo record type %q: %w", t.Key, err)
	}
	return nil
}

func (r *eoRecordTypeRepository) Delete(ctx context.Context, id uuid.UUID) error {
	if err := r.db.WithContext(ctx).
		Where("eo_record_types.id = ?", id).
		Delete(&models.EORecordType{}).Error; err != nil {
		return fmt.Errorf("delete eo record type %s: %w", id, err)
	}
	return nil
}

func (r *eoRecordTypeRepository) GetFieldDef(ctx context.Context, id uuid.UUID) (*models.EOFieldDef, error) {
	var out []models.EOFieldDef
	if err := r.db.WithContext(ctx).
		Where("eo_field_defs.id = ?", id).
		Limit(1).Find(&out).Error; err != nil {
		return nil, fmt.Errorf("read eo field def %s: %w", id, err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return &out[0], nil
}

func (r *eoRecordTypeRepository) CreateFieldDef(ctx context.Context, f *models.EOFieldDef) error {
	if f == nil {
		return fmt.Errorf("create eo field def: nil field def")
	}
	if err := r.db.WithContext(ctx).Create(f).Error; err != nil {
		return fmt.Errorf("create eo field def %q: %w", f.Key, err)
	}
	return nil
}

func (r *eoRecordTypeRepository) UpdateFieldDef(ctx context.Context, f *models.EOFieldDef) error {
	if f == nil {
		return fmt.Errorf("update eo field def: nil field def")
	}
	if err := r.db.WithContext(ctx).Save(f).Error; err != nil {
		return fmt.Errorf("update eo field def %q: %w", f.Key, err)
	}
	return nil
}

func (r *eoRecordTypeRepository) DeleteFieldDef(ctx context.Context, id uuid.UUID) error {
	if err := r.db.WithContext(ctx).
		Where("eo_field_defs.id = ?", id).
		Delete(&models.EOFieldDef{}).Error; err != nil {
		return fmt.Errorf("delete eo field def %s: %w", id, err)
	}
	return nil
}

func (r *eoRecordTypeRepository) ListLinkTypes(ctx context.Context) ([]models.EOLinkType, error) {
	var out []models.EOLinkType
	if err := r.db.WithContext(ctx).
		Model(&models.EOLinkType{}).
		Order("eo_link_types.key ASC").
		Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list eo link types: %w", err)
	}
	return out, nil
}

func (r *eoRecordTypeRepository) GetLinkTypeByID(ctx context.Context, id uuid.UUID) (*models.EOLinkType, error) {
	var out []models.EOLinkType
	if err := r.db.WithContext(ctx).
		Where("eo_link_types.id = ?", id).
		Limit(1).Find(&out).Error; err != nil {
		return nil, fmt.Errorf("read eo link type %s: %w", id, err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return &out[0], nil
}

func (r *eoRecordTypeRepository) CreateLinkType(ctx context.Context, lt *models.EOLinkType) error {
	if lt == nil {
		return fmt.Errorf("create eo link type: nil link type")
	}
	if err := r.db.WithContext(ctx).Create(lt).Error; err != nil {
		return fmt.Errorf("create eo link type %q: %w", lt.Key, err)
	}
	return nil
}

func (r *eoRecordTypeRepository) UpdateLinkType(ctx context.Context, lt *models.EOLinkType) error {
	if lt == nil {
		return fmt.Errorf("update eo link type: nil link type")
	}
	if err := r.db.WithContext(ctx).Save(lt).Error; err != nil {
		return fmt.Errorf("update eo link type %q: %w", lt.Key, err)
	}
	return nil
}

func (r *eoRecordTypeRepository) DeleteLinkType(ctx context.Context, id uuid.UUID) error {
	if err := r.db.WithContext(ctx).
		Where("eo_link_types.id = ?", id).
		Delete(&models.EOLinkType{}).Error; err != nil {
		return fmt.Errorf("delete eo link type %s: %w", id, err)
	}
	return nil
}
