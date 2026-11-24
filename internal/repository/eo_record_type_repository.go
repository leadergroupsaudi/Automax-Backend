package repository

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

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
	// Create writes the type AND spec §7.11's three per-type permission
	// codes, granted to models.EOTypePermissionRoleCodes, in one
	// transaction. See the implementation for why that is not the caller's
	// job.
	Create(ctx context.Context, t *models.EORecordType) error
	Update(ctx context.Context, t *models.EORecordType) error
	// Delete soft-deletes a record type and, in the SAME transaction, the
	// per-type permissions it owns and the role grants naming them. Its field
	// definitions are left alone: a mistaken delete stays recoverable, which
	// is the same reason ValidateTypeEdit never removes a field's stored data.
	Delete(ctx context.Context, id uuid.UUID) error

	GetFieldDef(ctx context.Context, id uuid.UUID) (*models.EOFieldDef, error)
	CreateFieldDef(ctx context.Context, f *models.EOFieldDef) error
	UpdateFieldDef(ctx context.Context, f *models.EOFieldDef) error
	DeleteFieldDef(ctx context.Context, id uuid.UUID) error

	// Transaction runs fn against a repository bound to ONE transaction, so
	// that a write and the audit row that explains it either both happen or
	// neither does. It is how spec §7.14's CONFIRMED destructive edit is
	// applied: the row naming the type, the field and the operator's stated
	// backfill rule is the only durable statement that the guard rail was
	// overridden, and an entry written through a second handle commits for an
	// edit that rolled back or is lost for one that did not (Plan 3, Task C1).
	//
	// Only the designer's own writes are available inside it. Anything that
	// needs another repository's handle does not belong in this transaction.
	Transaction(ctx context.Context, fn func(EORecordTypeRepository) error) error
	// WriteActionLog writes one audit row on THIS repository's handle — the
	// transaction's, inside Transaction. It exists because
	// repository.ActionLogRepository writes on the root handle and so cannot
	// join a caller's transaction; the row it writes is exactly the shape
	// services.ActionLogService writes, so it appears in the existing
	// action-log screen like any other.
	WriteActionLog(ctx context.Context, entry *models.ActionLog) error

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

// ErrEOTypePermissionCodesExist marks a Create refused because spec §7.11's
// per-type codes for that key are already in the database — which means the
// key belonged to a record type that has since been retired (the unique index
// on eo_record_types.key is partial on deleted_at IS NULL, so a key is
// re-usable; a permission code is not scoped that way).
//
// IT IS A REFUSAL AND NOT A SKIP because of who holds those codes. The seed
// grants them to admin and executive_office, but an administrator may have
// granted eo:eo_objection:view to a Legal Review role so counsel could read
// Objections. Retire the Objection type, re-use the key for Supplier
// Objections a year later, and silently re-using the codes would hand Legal
// Review — which nobody re-authorised — every Supplier Objection AND the
// retained Ministerial ones, under a permission whose description still names
// the old type. Refusing makes that an operator's deliberate act.
//
// WHAT HAPPENS TO A RETIRED TYPE'S CODES AND RECORDS is deliberately NOT
// settled here: this repository neither deletes nor renames them, and the
// register still matches a soft-deleted type's key, so a retired type's
// retained records stay readable by whoever held its codes. Whether that is
// right is a records-management question for the client, recorded as an open
// product question in the task report.
//
// THREE OTHER STATES REACH THIS REFUSAL, and the message an operator reads
// must be true of all four. Besides a retired type: a type made abstract and
// then concrete again meets ITS OWN codes (permissions record no owning type,
// so they cannot be told apart); a key renamed away leaves its codes behind,
// so renaming back is refused; and a DELETED permission still occupies its
// code, because permissions.code is uniquely indexed with no deleted_at
// predicate. That last one also means the obvious remedy does not work —
// DELETE /api/v1/admin/permissions/:id is a soft delete and frees nothing.
// The only remedies are a different key or direct database access, and
// i18n key eo_record_type_key_has_permissions says exactly that.
var ErrEOTypePermissionCodesExist = errors.New("per-type permission codes already exist for this record type key")

// ErrEOTypePermissionRoleMissing marks a write refused because a role in
// models.EOTypePermissionRoleCodes is not in the database. It is a sentinel so
// the designer can tell an operator WHICH role to restore instead of showing
// them a bare 500 and a dead screen.
var ErrEOTypePermissionRoleMissing = errors.New("a role required to hold the per-type permission codes is missing")

// Create writes the record type and, in the SAME TRANSACTION, spec §7.11's
// three per-type permission codes, granted to the roles the seed grants them
// to.
//
// WHY THE PERMISSIONS ARE WRITTEN HERE. Until Plan 3's A2 fix round nothing
// but the boot seed ever wrote a per-type permission row, and that was
// harmless only while the codes were inert: the module-level eo:view and
// eo:create covered every type. With the per-type gate enforcing, a type an
// administrator creates through the designer has no code for anyone to hold —
// so every non-super-admin, INCLUDING admin and executive_office, is refused
// create and shown an empty register for it, with nothing to explain why,
// until the next server restart runs the seed. Spec §7.13's headline is that
// "a new process is a configuration change rather than a release"; a
// configuration change that needs a restart is a release.
//
// It is the REPOSITORY's job rather than the handler's for the reason
// applyEORecordVisibility gives about the register: a second write path that
// forgot this would reintroduce the invisible type in a place no test of the
// designer route would look. The rows come from models.EOTypePermissionsFor,
// the constructor the seed uses, so a runtime-created type carries exactly
// what a seeded type carries.
//
// AN ABSTRACT TYPE GETS NO CODES, matching the seed, which writes them for
// concrete types only: an abstract type holds no records of its own, so a
// code naming it would grant access to an empty set and silently widen the
// moment a child type was added. Update writes them if that type is later
// made concrete — ValidateTypeEdit allows exactly that edit, and a Critical
// fixed on one path only is not fixed.
//
// A MISSING ROLE FAILS THE WHOLE CREATE. The seed aborts for the same reason
// — a code nobody holds is a type nobody can use — and in a transaction that
// means no type row either. The alternative is the invisible type this
// function exists to prevent.
func (r *eoRecordTypeRepository) Create(ctx context.Context, t *models.EORecordType) error {
	if t == nil {
		return fmt.Errorf("create eo record type: nil record type")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if !t.IsAbstract {
			existing, err := eoExistingTypePermissionCodes(tx, *t)
			if err != nil {
				return err
			}
			if len(existing) > 0 {
				return fmt.Errorf("create eo record type %q: %w: %s", t.Key, ErrEOTypePermissionCodesExist, strings.Join(existing, ", "))
			}
		}
		if err := tx.Create(t).Error; err != nil {
			return fmt.Errorf("create eo record type %q: %w", t.Key, err)
		}
		if t.IsAbstract {
			return nil
		}
		return eoWriteTypePermissions(tx, *t, true)
	})
}

// eoLogTypeWithNoLiveCodes writes one line when a concrete type ends an edit
// with no LIVE per-type code, which makes it invisible to everybody but a
// super admin.
//
// It is reachable only by an administrator deleting the codes, and the
// designer does not resurrect those — the same rule the seed applies to a
// revoked grant. But nothing else would say so: the type is there, the
// register is empty, and the reason is three deleted rows in another table.
// One line in the log is the difference between a five-minute answer and an
// afternoon.
func eoLogTypeWithNoLiveCodes(tx *gorm.DB, t models.EORecordType) {
	codes := make([]string, 0, len(models.EOTypePermissionActions))
	for _, a := range models.EOTypePermissionActions {
		codes = append(codes, models.EOTypePermissionCode(t.Key, a.Action))
	}
	var live int64
	if err := tx.Model(&models.Permission{}).Where("code IN ?", codes).Count(&live).Error; err != nil {
		log.Printf("[EO] could not check the per-type permission codes of %q: %v", t.Key, err)
		return
	}
	if live == 0 {
		log.Printf("[EO] record type %q has NO live per-type permission code (%s): only a super admin can see its records. The codes exist but are deleted, and a deleted permission is not resurrected.",
			t.Key, strings.Join(codes, ", "))
	}
}

// eoExistingTypePermissionCodes returns which of the type's three codes are
// already in the database, in the order models.EOTypePermissionsFor writes
// them.
//
// IT IS SCOPED TO LIVE ROWS. permissions.code and permissions.name are
// uniquely indexed WHERE deleted_at IS NULL (migrations.
// MigratePermissionsPartialUniqueIndexes), so a soft-deleted row occupies
// nothing and a retired type's key is reusable; Delete soft-deletes the
// type's own codes for exactly that reason. A LIVE row is still a refusal:
// it is someone's grant, and adopting it is the privilege carryover this
// check exists to stop.
func eoExistingTypePermissionCodes(tx *gorm.DB, t models.EORecordType) ([]string, error) {
	want := models.EOTypePermissionsFor(t)
	codes := make([]string, 0, len(want))
	for _, p := range want {
		codes = append(codes, p.Code)
	}
	var found []models.Permission
	if err := tx.Where("code IN ?", codes).Find(&found).Error; err != nil {
		return nil, fmt.Errorf("check the per-type permission codes of %q: %w", t.Key, err)
	}
	present := map[string]bool{}
	for _, p := range found {
		present[p.Code] = true
	}
	out := make([]string, 0, len(codes))
	for _, code := range codes {
		if present[code] {
			out = append(out, code)
		}
	}
	return out, nil
}

// eoWriteTypePermissions creates the codes of a concrete type that are not
// there yet and grants THOSE to models.EOTypePermissionRoleCodes.
//
// IT GRANTS ONLY WHAT IT CREATED. Re-applying the grant for a code that
// already existed would reinstate a grant an administrator deliberately
// revoked — the rule the seed states for the same reason — and this runs on
// every Update of a concrete type, not once at boot.
//
// A missing role fails, wrapping ErrEOTypePermissionRoleMissing: a code
// nobody holds is a type nobody can use.
//
// ITS EXISTENCE CHECK IS UNSCOPED, for the reason eoExistingTypePermissionCodes
// gives and for one more: a permission an administrator DELETED must not be
// resurrected by the next edit of the type. That is the same rule the seed
// states about a revoked grant — a deliberate administrative act is not drift
// to be corrected — and the alternative here is worse than inaction, because
// the insert would fail on the unique index and take the whole edit with it.
//
// fresh says the caller has just established that no LIVE row holds these
// codes (Create, and Update when the type adopts a key), so a soft-deleted row
// of the same code is a RETIRED type's and is not in the way: the partial
// unique indexes allow the new row beside it. When fresh is false (an ordinary
// edit) the check stays unscoped, because a deleted code there is an
// administrator's act and must not be resurrected.
func eoWriteTypePermissions(tx *gorm.DB, t models.EORecordType, fresh bool) error {
	want := models.EOTypePermissionsFor(t)
	created := make([]models.Permission, 0, len(want))
	for i := range want {
		perm := want[i]
		var existing int64
		check := tx.Unscoped()
		if fresh {
			check = tx
		}
		if err := check.Model(&models.Permission{}).Where("code = ?", perm.Code).Count(&existing).Error; err != nil {
			return fmt.Errorf("check permission %q: %w", perm.Code, err)
		}
		if existing > 0 {
			continue
		}
		if err := tx.Create(&perm).Error; err != nil {
			return fmt.Errorf("create permission %q: %w", perm.Code, err)
		}
		created = append(created, perm)
	}
	eoLogTypeWithNoLiveCodes(tx, t)
	if len(created) == 0 {
		return nil
	}
	for _, roleCode := range models.EOTypePermissionRoleCodes {
		var role models.Role
		if err := tx.Where("code = ?", roleCode).First(&role).Error; err != nil {
			return fmt.Errorf("%w: %q is required to hold the per-type permission codes of %q: %v", ErrEOTypePermissionRoleMissing, roleCode, t.Key, err)
		}
		if err := tx.Model(&role).Association("Permissions").Append(created); err != nil {
			return fmt.Errorf("grant the per-type permissions of %q to role %q: %w", t.Key, roleCode, err)
		}
	}
	return nil
}

// Update writes every column with Save rather than a field map: the caller has
// already built the full after-state and had models.ValidateTypeEdit pass on
// it, so a partial write would store something nothing validated.
// It also writes any of spec §7.11's three codes that the type does not have,
// and refuses the two edits that would make it ADOPT codes written for
// something else.
//
// THE EDIT THAT NEEDS THE WRITE is abstract → concrete, which
// models.ValidateTypeEdit explicitly allows: a type created abstract has no
// codes by design, and the moment it becomes concrete it can hold records that
// nobody but a super admin could see. A Critical fixed on the create path
// alone is not fixed.
//
// THE EDITS THAT NEED THE REFUSAL are the same two, read the other way round.
// ValidateTypeEdit returns nil for EVERY edit when the type holds no records
// (models/eo_record_type.go:352), so a record-less type can be renamed onto a
// retired type's key, and an abstract type can be made concrete on a key whose
// codes survive an earlier incarnation. Either way eoWriteTypePermissions
// would find the codes present, skip them, and hand the type to every role the
// retired type's codes were granted to — the privilege carryover Create
// refuses. Create's refusal would be worth nothing if PUT could walk around
// it, so the same check, the same sentinel and the same wording are applied
// here.
//
// IT RUNS BEFORE THE SAVE, as Create's does, so a refused update leaves no
// renamed row behind.
//
// AN ORDINARY EDIT IS UNTOUCHED. The guard asks only about a key change and
// about abstract → concrete; anything else converges the codes as before. And
// nothing is granted when all three codes already exist, so an ordinary edit
// touches no permission row and cannot reinstate a grant an administrator
// revoked.
func (r *eoRecordTypeRepository) Update(ctx context.Context, t *models.EORecordType) error {
	if t == nil {
		return fmt.Errorf("update eo record type: nil record type")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before models.EORecordType
		if err := tx.Where("id = ?", t.ID).First(&before).Error; err != nil {
			return fmt.Errorf("read eo record type %s before update: %w", t.ID, err)
		}
		adopting := before.Key != t.Key || (before.IsAbstract && !t.IsAbstract)
		if adopting && !t.IsAbstract {
			existing, err := eoExistingTypePermissionCodes(tx, *t)
			if err != nil {
				return err
			}
			if len(existing) > 0 {
				return fmt.Errorf("update eo record type %q: %w: %s", t.Key, ErrEOTypePermissionCodesExist, strings.Join(existing, ", "))
			}
		}
		if err := tx.Save(t).Error; err != nil {
			return fmt.Errorf("update eo record type %q: %w", t.Key, err)
		}
		if t.IsAbstract {
			return nil
		}
		return eoWriteTypePermissions(tx, *t, adopting)
	})
}

func (r *eoRecordTypeRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var ty models.EORecordType
		found := tx.Where("eo_record_types.id = ?", id).Limit(1).Find(&ty)
		if found.Error != nil {
			return fmt.Errorf("read eo record type %s before delete: %w", id, found.Error)
		}
		if err := tx.Where("eo_record_types.id = ?", id).Delete(&models.EORecordType{}).Error; err != nil {
			return fmt.Errorf("delete eo record type %s: %w", id, err)
		}
		if found.RowsAffected == 0 {
			return nil
		}
		return eoReleaseTypePermissions(tx, ty)
	})
}

// eoReleaseTypePermissions soft-deletes the three per-type permissions a type
// owns and removes the role grants that point at them, so retiring a type
// leaves no live permission naming it.
//
// WHY: a type's creation writes 3 permissions and 6 grants, and a delete that
// removed only the type left them behind. Across the previous plan that was 30
// orphan grants; in production an Executive Office that reorganises its
// register types would accumulate live permissions, granted to real roles,
// naming types that no longer exist.
//
// THE SCOPE IS EXACT CODES, NEVER A PATTERN. permissions is product-wide
// (users.create, tickets.view, ...). The rows touched are those whose code is
// one of models.EOTypePermissionCode(key, action) for this type's key — the
// same constructor the gate, the filter and the writer use — compared with
// IN, so no LIKE wildcard (an underscore in the key is one) can reach a
// neighbour's rows. Soft-deleted rows of those codes are included so their
// grants go too; rows already deleted are not stamped again.
//
// IT RUNS IN THE DELETE'S TRANSACTION: a failure here rolls the type delete
// back rather than leaving a type gone and its grants live.
func eoReleaseTypePermissions(tx *gorm.DB, t models.EORecordType) error {
	codes := make([]string, 0, len(models.EOTypePermissionActions))
	for _, a := range models.EOTypePermissionActions {
		codes = append(codes, models.EOTypePermissionCode(t.Key, a.Action))
	}
	var ids []uuid.UUID
	if err := tx.Unscoped().Model(&models.Permission{}).Where("code IN ?", codes).Pluck("id", &ids).Error; err != nil {
		return fmt.Errorf("find the per-type permissions of %q: %w", t.Key, err)
	}
	if len(ids) == 0 {
		return nil
	}
	if err := tx.Exec("DELETE FROM role_permissions WHERE permission_id IN ?", ids).Error; err != nil {
		return fmt.Errorf("remove the role grants of the per-type permissions of %q: %w", t.Key, err)
	}
	if err := tx.Where("id IN ?", ids).Delete(&models.Permission{}).Error; err != nil {
		return fmt.Errorf("delete the per-type permissions of %q: %w", t.Key, err)
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

func (r *eoRecordTypeRepository) Transaction(ctx context.Context, fn func(EORecordTypeRepository) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(&eoRecordTypeRepository{db: tx})
	})
}

// WriteActionLog writes the entry on this repository's handle.
//
// Omit("User"): models.ActionLog carries a *User association that is nil here,
// and gorm must not try to upsert the caller's user row as a side effect of
// auditing. The same Omit is on eo_signoff_handler.go's audit write, for the
// same reason.
func (r *eoRecordTypeRepository) WriteActionLog(ctx context.Context, entry *models.ActionLog) error {
	if entry == nil {
		return fmt.Errorf("write eo action log: nil entry")
	}
	if err := r.db.WithContext(ctx).Omit("User").Create(entry).Error; err != nil {
		return fmt.Errorf("record the confirmed destructive type edit in the action log: %w", err)
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
