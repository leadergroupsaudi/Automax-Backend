package repository

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// These cover the metamodel writes the designer API needs. They are DB-backed
// because the whole point of each one is the statement it issues: a soft
// delete that List then skips, a Save that writes every column, a read that
// answers (nil, nil) for a key nobody configured.

func TestEORecordTypeWritesRoundTrip(t *testing.T) {
	db := eoRecordDB(t)
	repo := NewEORecordTypeRepository(db)
	ctx := context.Background()

	t.Run("create then read back by id, key and list", func(t *testing.T) {
		rt := &models.EORecordType{
			ID:               uuid.New(),
			Key:              "eo_directive",
			Name:             "Directive",
			ReferencePrefix:  "EO-DIR",
			ReferenceMode:    models.EOReferenceModeOwn,
			ReferencePattern: "EO-DIR-YYYY-NNN",
			IsActive:         true,
		}
		if err := repo.Create(ctx, rt); err != nil {
			t.Fatalf("Create: %v", err)
		}
		byID, err := repo.GetByID(ctx, rt.ID)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if byID == nil || byID.Key != "eo_directive" {
			t.Errorf("GetByID = %+v, want the created type", byID)
		}
		byKey, err := repo.GetByKey(ctx, "eo_directive")
		if err != nil {
			t.Fatalf("GetByKey: %v", err)
		}
		if byKey == nil || byKey.ID != rt.ID {
			t.Errorf("GetByKey = %+v, want the created type", byKey)
		}
		all, err := repo.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(all) != 1 {
			t.Errorf("List returned %d types, want 1", len(all))
		}
	})

	t.Run("update writes every column, not a subset", func(t *testing.T) {
		stored, err := repo.GetByKey(ctx, "eo_directive")
		if err != nil {
			t.Fatalf("GetByKey: %v", err)
		}
		if stored == nil {
			t.Fatal("GetByKey returned nothing")
		}
		stored.Name = "Directive (revised)"
		stored.IsActive = false
		if err := repo.Update(ctx, stored); err != nil {
			t.Fatalf("Update: %v", err)
		}
		again, err := repo.GetByID(ctx, stored.ID)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if again.Name != "Directive (revised)" {
			t.Errorf("Name = %q, want %q", again.Name, "Directive (revised)")
		}
		// A bool going true -> false is the case a field map silently drops.
		if again.IsActive {
			t.Errorf("IsActive = true, want false")
		}
	})

	t.Run("delete is a soft delete that List no longer returns", func(t *testing.T) {
		stored, err := repo.GetByKey(ctx, "eo_directive")
		if err != nil {
			t.Fatalf("GetByKey: %v", err)
		}
		if err := repo.Delete(ctx, stored.ID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		gone, err := repo.GetByID(ctx, stored.ID)
		if err != nil {
			t.Fatalf("GetByID: %v", err)
		}
		if gone != nil {
			t.Errorf("GetByID after delete = %+v, want nil", gone)
		}
		all, err := repo.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(all) != 0 {
			t.Errorf("List returned %d types after delete, want 0", len(all))
		}
	})
}

func TestEOFieldDefWritesRoundTrip(t *testing.T) {
	db := eoRecordDB(t)
	repo := NewEORecordTypeRepository(db)
	ctx := context.Background()

	rt := &models.EORecordType{ID: uuid.New(), Key: "eo_task", Name: "Task", IsActive: true}
	if err := repo.Create(ctx, rt); err != nil {
		t.Fatalf("Create type: %v", err)
	}

	def := &models.EOFieldDef{
		ID:           uuid.New(),
		RecordTypeID: rt.ID,
		Key:          "committee_chair",
		Label:        "Committee chair",
		Kind:         models.EOKindUser,
		Provenance:   models.EOProvenanceUser,
		RequiredMode: models.EORequiredNever,
	}
	if err := repo.CreateFieldDef(ctx, def); err != nil {
		t.Fatalf("CreateFieldDef: %v", err)
	}

	t.Run("read back by id and by owning type", func(t *testing.T) {
		got, err := repo.GetFieldDef(ctx, def.ID)
		if err != nil {
			t.Fatalf("GetFieldDef: %v", err)
		}
		if got == nil || got.Key != "committee_chair" {
			t.Errorf("GetFieldDef = %+v, want the created def", got)
		}
		defs, err := repo.FieldDefs(ctx, []uuid.UUID{rt.ID})
		if err != nil {
			t.Fatalf("FieldDefs: %v", err)
		}
		if len(defs) != 1 {
			t.Errorf("FieldDefs returned %d defs, want 1", len(defs))
		}
	})

	t.Run("update", func(t *testing.T) {
		def.Label = "Chair"
		def.RequiredMode = models.EORequiredAlways
		if err := repo.UpdateFieldDef(ctx, def); err != nil {
			t.Fatalf("UpdateFieldDef: %v", err)
		}
		got, err := repo.GetFieldDef(ctx, def.ID)
		if err != nil {
			t.Fatalf("GetFieldDef: %v", err)
		}
		if got.Label != "Chair" || got.RequiredMode != models.EORequiredAlways {
			t.Errorf("def = {%q, %q}, want {%q, %q}", got.Label, got.RequiredMode, "Chair", models.EORequiredAlways)
		}
	})

	t.Run("delete", func(t *testing.T) {
		if err := repo.DeleteFieldDef(ctx, def.ID); err != nil {
			t.Fatalf("DeleteFieldDef: %v", err)
		}
		got, err := repo.GetFieldDef(ctx, def.ID)
		if err != nil {
			t.Fatalf("GetFieldDef: %v", err)
		}
		if got != nil {
			t.Errorf("GetFieldDef after delete = %+v, want nil", got)
		}
		defs, err := repo.FieldDefs(ctx, []uuid.UUID{rt.ID})
		if err != nil {
			t.Fatalf("FieldDefs: %v", err)
		}
		if len(defs) != 0 {
			t.Errorf("FieldDefs returned %d defs after delete, want 0", len(defs))
		}
	})
}

func TestEOLinkTypeWritesRoundTrip(t *testing.T) {
	db := eoRecordDB(t)
	repo := NewEORecordTypeRepository(db)
	ctx := context.Background()

	lt := &models.EOLinkType{
		ID:                       uuid.New(),
		Key:                      "parent_record",
		Label:                    "Parent record",
		Cardinality:              models.EOCardinalityOne,
		DeriveDeadlineFromParent: true,
		DeadlineOffsetDays:       3,
	}
	if err := repo.CreateLinkType(ctx, lt); err != nil {
		t.Fatalf("CreateLinkType: %v", err)
	}

	t.Run("read back by key, by id and in the list", func(t *testing.T) {
		byKey, err := repo.GetLinkTypeByKey(ctx, "parent_record")
		if err != nil {
			t.Fatalf("GetLinkTypeByKey: %v", err)
		}
		if byKey == nil || byKey.DeadlineOffsetDays != 3 {
			t.Errorf("GetLinkTypeByKey = %+v, want the created link type", byKey)
		}
		byID, err := repo.GetLinkTypeByID(ctx, lt.ID)
		if err != nil {
			t.Fatalf("GetLinkTypeByID: %v", err)
		}
		if byID == nil || byID.Key != "parent_record" {
			t.Errorf("GetLinkTypeByID = %+v, want the created link type", byID)
		}
		all, err := repo.ListLinkTypes(ctx)
		if err != nil {
			t.Fatalf("ListLinkTypes: %v", err)
		}
		if len(all) != 1 {
			t.Errorf("ListLinkTypes returned %d, want 1", len(all))
		}
	})

	t.Run("an unconfigured key is an answer, not an error", func(t *testing.T) {
		got, err := repo.GetLinkTypeByKey(ctx, "nobody_configured_this")
		if err != nil {
			t.Fatalf("GetLinkTypeByKey: %v", err)
		}
		if got != nil {
			t.Errorf("GetLinkTypeByKey = %+v, want nil", got)
		}
	})

	t.Run("update and delete", func(t *testing.T) {
		lt.Cardinality = models.EOCardinalityMany
		lt.DeriveDeadlineFromParent = false
		if err := repo.UpdateLinkType(ctx, lt); err != nil {
			t.Fatalf("UpdateLinkType: %v", err)
		}
		got, err := repo.GetLinkTypeByID(ctx, lt.ID)
		if err != nil {
			t.Fatalf("GetLinkTypeByID: %v", err)
		}
		if got.Cardinality != models.EOCardinalityMany || got.DeriveDeadlineFromParent {
			t.Errorf("link type = {%q, %v}, want {%q, false}", got.Cardinality, got.DeriveDeadlineFromParent, models.EOCardinalityMany)
		}
		if err := repo.DeleteLinkType(ctx, lt.ID); err != nil {
			t.Fatalf("DeleteLinkType: %v", err)
		}
		gone, err := repo.GetLinkTypeByID(ctx, lt.ID)
		if err != nil {
			t.Fatalf("GetLinkTypeByID: %v", err)
		}
		if gone != nil {
			t.Errorf("GetLinkTypeByID after delete = %+v, want nil", gone)
		}
	})
}

// TestEORecordListFilterByRecordID covers the filter field a single-record read
// uses to ask the §7.11 visibility rule about ONE record, instead of
// re-implementing that rule at the HTTP boundary.
func TestEORecordListFilterByRecordID(t *testing.T) {
	db := eoRecordDB(t)
	repo := NewEORecordRepository(db)
	// Catalogued and granted below: §7.11's per-type gate is its own clause,
	// and an ungranted caller would see nothing here whatever RecordID said.
	typeID := eoCatalogueType(t, db, "eo_directive")
	held := []string{models.EOTypePermissionCode("eo_directive", models.EOTypePermissionView)}
	wanted := eoListRec(t, repo, typeID, "EO-DIR-2026-001", nil)
	eoListRec(t, repo, typeID, "EO-DIR-2026-002", nil)

	t.Run("narrows to that record", func(t *testing.T) {
		f := eoAdmin()
		f.RecordID = &wanted.ID
		got, total, err := repo.List(context.Background(), &f)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if total != 1 {
			t.Errorf("total = %d, want 1", total)
		}
		if refs := eoRefs(got); len(refs) != 1 || refs[0] != "EO-DIR-2026-001" {
			t.Errorf("refs = %v, want [EO-DIR-2026-001]", refs)
		}
	})

	t.Run("an invisible record is filtered out, not returned", func(t *testing.T) {
		body := uuid.New()
		restricted := eoListRec(t, repo, typeID, "EO-DIR-2026-003", func(r *models.EORecord) {
			r.Values = []byte(`{"visible_to":"` + body.String() + `"}`)
		})
		// A caller who belongs to no body and did not create it.
		f := models.EORecordFilter{RecordID: &restricted.ID, UserID: uuid.New(), PermissionCodes: held}
		got, total, err := repo.List(context.Background(), &f)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if total != 0 || len(got) != 0 {
			t.Errorf("total = %d, rows = %v, want the restricted record hidden", total, eoRefs(got))
		}

		// The same record, asked for by a caller inside that body.
		f.VisibleToDepartmentIDs = []uuid.UUID{body}
		got, total, err = repo.List(context.Background(), &f)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if total != 1 || len(got) != 1 {
			t.Errorf("total = %d, rows = %v, want the record visible to its own body", total, eoRefs(got))
		}
	})

	t.Run("nil means the whole register", func(t *testing.T) {
		f := eoAdmin()
		_, total, err := repo.List(context.Background(), &f)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if total < 2 {
			t.Errorf("total = %d, want every record when RecordID is nil", total)
		}
	})
}

// TestEORecordListFilterByParentID is the register's answer to "the children
// of this record". The children endpoint serves this query rather than
// EORecordRepository.Children, because Children takes no actor and applies no
// visibility — so a caller who could see a parent would see every record
// beneath it, restricted ones included.
func TestEORecordListFilterByParentID(t *testing.T) {
	db := eoRecordDB(t)
	repo := NewEORecordRepository(db)
	ctx := context.Background()
	typeID := eoCatalogueType(t, db, "eo_directive")
	held := []string{models.EOTypePermissionCode("eo_directive", models.EOTypePermissionView)}
	body := uuid.New()

	parent := eoListRec(t, repo, typeID, "EO-DIR-2026-100", nil)
	eoListRec(t, repo, typeID, "EO-DIR-2026-100-A", func(r *models.EORecord) {
		r.ParentRecordID = &parent.ID
	})
	eoListRec(t, repo, typeID, "EO-DIR-2026-100-B", func(r *models.EORecord) {
		r.ParentRecordID = &parent.ID
		r.Values = []byte(`{"visible_to":"` + body.String() + `"}`)
	})
	// A child of someone else, to prove the parent filter bites.
	eoListRec(t, repo, typeID, "EO-DIR-2026-200-A", nil)

	parentID := parent.ID.String()

	t.Run("a caller outside the restricted body sees one child", func(t *testing.T) {
		f := models.EORecordFilter{ParentID: &parentID, UserID: uuid.New(), PermissionCodes: held, SortBy: "reference", SortDir: "asc"}
		got, total, err := repo.List(ctx, &f)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if total != 1 {
			t.Errorf("total = %d, want 1", total)
		}
		if refs := eoRefs(got); len(refs) != 1 || refs[0] != "EO-DIR-2026-100-A" {
			t.Errorf("refs = %v, want [EO-DIR-2026-100-A]", refs)
		}
	})

	t.Run("a caller inside the body sees both", func(t *testing.T) {
		f := models.EORecordFilter{
			ParentID:               &parentID,
			UserID:                 uuid.New(),
			PermissionCodes:        held,
			VisibleToDepartmentIDs: []uuid.UUID{body},
			SortBy:                 "reference",
			SortDir:                "asc",
		}
		got, total, err := repo.List(ctx, &f)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if total != 2 {
			t.Errorf("total = %d, want 2", total)
		}
		if refs := eoRefs(got); len(refs) != 2 {
			t.Errorf("refs = %v, want both children", refs)
		}
	})

	t.Run("the unscoped repository method returns the restricted child too", func(t *testing.T) {
		// This is the behaviour the handler must NOT expose: Children has no
		// actor and no visibility clause, by design, because the services
		// that call it run server-side.
		kids, err := repo.Children(ctx, parent.ID, nil)
		if err != nil {
			t.Fatalf("Children: %v", err)
		}
		if len(kids) != 2 {
			t.Errorf("Children returned %d, want 2 (it applies no visibility)", len(kids))
		}
	})
}

// ── §7.11: a type created through the designer is usable immediately ────────

// eoRoleUser is the user a role's holder is, loaded the way the auth
// middleware loads one: the role WITH its permissions.
func eoRoleUser(t *testing.T, db *gorm.DB, roleCode string) *models.User {
	t.Helper()
	var role models.Role
	if err := db.Preload("Permissions").Where("code = ?", roleCode).First(&role).Error; err != nil {
		t.Fatalf("read role %q with its permissions: %v", roleCode, err)
	}
	return &models.User{ID: uuid.New(), Roles: []models.Role{role}}
}

// TestEORecordTypeCreateGrantsItsPerTypePermissions is the fix for the
// Critical the A2 review found: with the per-type gate enforcing, a record
// type created through the designer had NO permission row, so every
// non-super-admin — admin and executive_office included — was refused create
// and shown an empty register for it until the next server restart.
//
// Spec §7.13 says a new process is a configuration change rather than a
// release. This asserts the whole chain the designer depends on: the codes
// exist, they are granted, and a holder of the seeded role can immediately see
// a record of the new type through the register's §7.11 clause.
func TestEORecordTypeCreateGrantsItsPerTypePermissions(t *testing.T) {
	db := eoRecordDB(t)
	types := NewEORecordTypeRepository(db)
	records := NewEORecordRepository(db)
	ctx := context.Background()

	created := &models.EORecordType{ID: uuid.New(), Key: "eo_board_resolution", Name: "Board Resolution", IsActive: true}
	if err := types.Create(ctx, created); err != nil {
		t.Fatalf("create the record type: %v", err)
	}

	// 1. the three codes exist, spelled the way the gate and the filter read
	//    them.
	for _, action := range []string{models.EOTypePermissionView, models.EOTypePermissionCreate, models.EOTypePermissionUpdate} {
		code := models.EOTypePermissionCode(created.Key, action)
		var n int64
		if err := db.Model(&models.Permission{}).Where("code = ?", code).Count(&n).Error; err != nil {
			t.Fatalf("count %q: %v", code, err)
		}
		if n != 1 {
			t.Errorf("%d rows for %q, want 1: the designer wrote a type nobody holds a code for", n, code)
		}
	}

	// 2. both seeded roles hold all three — the designer may not grant a
	//    different population than the seed.
	for _, roleCode := range models.EOTypePermissionRoleCodes {
		held := map[string]bool{}
		for _, code := range eoRoleUser(t, db, roleCode).Roles[0].Permissions {
			held[code.Code] = true
		}
		for _, action := range []string{models.EOTypePermissionView, models.EOTypePermissionCreate, models.EOTypePermissionUpdate} {
			if code := models.EOTypePermissionCode(created.Key, action); !held[code] {
				t.Errorf("role %q does not hold %q", roleCode, code)
			}
		}
	}

	// 3. the register shows a record of the new type to a holder of the
	//    seeded role, with no restart. This is the assertion that fails if
	//    anything above is cosmetic.
	rec := eoListRec(t, records, created.ID, "EO-BR-2026-001", nil)
	user := eoRoleUser(t, db, "executive_office")
	if !user.HasPermission(models.EOTypePermissionCode(created.Key, models.EOTypePermissionView)) {
		t.Fatal("the role does not hold the view code, so the register below proves nothing")
	}
	f := models.EORecordFilter{UserID: user.ID, PermissionCodes: user.PermissionCodes()}
	got, total, err := records.List(ctx, &f)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if refs := eoRefs(got); !reflect.DeepEqual(refs, []string{rec.Reference}) || total != 1 {
		t.Errorf("an executive_office user sees %v total %d, want [%s]: the designer-created type is invisible", refs, total, rec.Reference)
	}
}

// TestEORecordTypeCreateWritesNoCodesForAnAbstractType matches the seed, which
// writes codes for CONCRETE types only. An abstract type holds no records of
// its own, so a code naming it would grant access to an empty set and widen
// silently the moment a child type was added.
func TestEORecordTypeCreateWritesNoCodesForAnAbstractType(t *testing.T) {
	db := eoRecordDB(t)
	types := NewEORecordTypeRepository(db)

	abstract := &models.EORecordType{ID: uuid.New(), Key: "eo_abstract_base", Name: "Base", IsAbstract: true, IsActive: true}
	if err := types.Create(context.Background(), abstract); err != nil {
		t.Fatalf("create the abstract type: %v", err)
	}
	var n int64
	if err := db.Model(&models.Permission{}).Where("code LIKE ?", "eo:eo\\_abstract\\_base:%").Count(&n).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("%d per-type codes for an abstract type, want 0", n)
	}
}

// TestEORecordTypeCreateRollsBackWhenARoleIsMissing: the grant and the type
// are one transaction. A deployment missing a seeded role is a
// misconfiguration, and a loud failure is better than the type this whole fix
// exists to prevent — one that exists and that nobody but a super admin can
// see.
func TestEORecordTypeCreateRollsBackWhenARoleIsMissing(t *testing.T) {
	db := eoRecordDB(t)
	if err := db.Where("code = ?", "executive_office").Delete(&models.Role{}).Error; err != nil {
		t.Fatalf("remove the role: %v", err)
	}
	types := NewEORecordTypeRepository(db)

	err := types.Create(context.Background(), &models.EORecordType{ID: uuid.New(), Key: "eo_board_resolution", Name: "Board Resolution", IsActive: true})
	if err == nil {
		t.Fatal("Create succeeded with a seeded role missing, so the type's codes were granted to nobody")
	}
	var n int64
	if err := db.Model(&models.EORecordType{}).Where("key = ?", "eo_board_resolution").Count(&n).Error; err != nil {
		t.Fatalf("count types: %v", err)
	}
	if n != 0 {
		t.Errorf("%d type rows after a failed grant, want 0: the transaction did not roll back", n)
	}
	if err := db.Model(&models.Permission{}).Where("code LIKE ?", "eo:eo\\_board\\_resolution:%").Count(&n).Error; err != nil {
		t.Fatalf("count permissions: %v", err)
	}
	if n != 0 {
		t.Errorf("%d permission rows after a failed grant, want 0", n)
	}
}

// TestEORecordTypeUpdateGrantsCodesWhenATypeBecomesConcrete closes the second
// half of the Critical. Create writes no codes for an abstract type, by
// design — and models.ValidateTypeEdit explicitly ALLOWS abstract → concrete.
// So a type created abstract and then made concrete used to end up holding
// records with no per-type code in existence: invisible to admin and
// executive_office alike until the next boot. A Critical fixed on one path is
// not fixed.
func TestEORecordTypeUpdateGrantsCodesWhenATypeBecomesConcrete(t *testing.T) {
	db := eoRecordDB(t)
	types := NewEORecordTypeRepository(db)
	records := NewEORecordRepository(db)
	ctx := context.Background()

	ty := &models.EORecordType{ID: uuid.New(), Key: "eo_board_resolution", Name: "Board Resolution", IsAbstract: true, IsActive: true}
	if err := types.Create(ctx, ty); err != nil {
		t.Fatalf("create the abstract type: %v", err)
	}
	if n := eoCountTypeCodes(t, db, ty.Key); n != 0 {
		t.Fatalf("%d codes for a type created abstract, want 0: the premise of this test is gone", n)
	}

	ty.IsAbstract = false
	if err := types.Update(ctx, ty); err != nil {
		t.Fatalf("make it concrete: %v", err)
	}
	if n := eoCountTypeCodes(t, db, ty.Key); n != 3 {
		t.Errorf("GAP: %d per-type codes after abstract->concrete, want 3", n)
	}

	rec := eoListRec(t, records, ty.ID, "EO-BR-2026-001", nil)
	user := eoRoleUser(t, db, "executive_office")
	f := models.EORecordFilter{UserID: user.ID, PermissionCodes: user.PermissionCodes()}
	got, total, err := records.List(ctx, &f)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if refs := eoRefs(got); !reflect.DeepEqual(refs, []string{rec.Reference}) || total != 1 {
		t.Errorf("an executive_office user sees %v total %d, want [%s]", refs, total, rec.Reference)
	}
}

// TestEORecordTypeUpdateDoesNotReinstateARevokedGrant: Update runs on every
// edit of every concrete type, so it must not re-apply a grant an
// administrator deliberately revoked. It writes only codes that do not exist,
// and grants only what it wrote.
func TestEORecordTypeUpdateDoesNotReinstateARevokedGrant(t *testing.T) {
	db := eoRecordDB(t)
	types := NewEORecordTypeRepository(db)
	ctx := context.Background()

	ty := &models.EORecordType{ID: uuid.New(), Key: "eo_board_resolution", Name: "Board Resolution", IsActive: true}
	if err := types.Create(ctx, ty); err != nil {
		t.Fatalf("create: %v", err)
	}
	var role models.Role
	if err := db.Where("code = ?", "executive_office").First(&role).Error; err != nil {
		t.Fatalf("read the role: %v", err)
	}
	var perms []models.Permission
	if err := db.Where("code LIKE ?", "eo:eo\\_board\\_resolution:%").Find(&perms).Error; err != nil {
		t.Fatalf("read the codes: %v", err)
	}
	if err := db.Model(&role).Association("Permissions").Delete(perms); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	ty.Name = "Board Resolution (revised)"
	if err := types.Update(ctx, ty); err != nil {
		t.Fatalf("update: %v", err)
	}

	user := eoRoleUser(t, db, "executive_office")
	if code := models.EOTypePermissionCode(ty.Key, models.EOTypePermissionView); user.HasPermission(code) {
		t.Errorf("Update reinstated %q, which an administrator had revoked", code)
	}
}

// TestEORecordTypeCreateRefusesAKeyWhosePermissionsExist is the SECURITY
// finding. eo_record_types.key is unique only among LIVE rows, so a key is
// re-usable after a type is retired — but its permission codes are not scoped
// that way and are still held by whoever was granted them. Re-using the key
// silently would hand the new type to a role nobody re-authorised, under a
// permission whose description names the retired type.
//
// The refusal is the whole fix: an operator must decide, rather than the
// designer deciding for them.
func TestEORecordTypeCreateRefusesAKeyWhosePermissionsExist(t *testing.T) {
	db := eoRecordDB(t)
	types := NewEORecordTypeRepository(db)
	ctx := context.Background()

	// A DELETED type no longer leaves live codes (Delete releases them), so
	// the state that still reaches this refusal is codes left live under an
	// unused key: stranded, LIVE, and possibly granted to a custom role.
	first := &models.EORecordType{ID: uuid.New(), Key: "eo_objection", Name: "Objection", IsActive: true}
	if err := types.Create(ctx, first); err != nil {
		t.Fatalf("create the first type: %v", err)
	}
	// Straight SQL, not Update: the designer's rename now carries the codes
	// along, so only an out-of-band change leaves them live under a key no
	// type uses.
	if err := db.Model(&models.EORecordType{}).Where("id = ?", first.ID).Update("key", "eo_objection_old").Error; err != nil {
		t.Fatalf("move the key out of band: %v", err)
	}

	reused := &models.EORecordType{ID: uuid.New(), Key: "eo_objection", Name: "Supplier Objection", IsActive: true}
	err := types.Create(ctx, reused)
	if !errors.Is(err, ErrEOTypePermissionCodesExist) {
		t.Fatalf("Create = %v, want ErrEOTypePermissionCodesExist: the new type would inherit the stranded codes' grants", err)
	}
	if !strings.Contains(err.Error(), models.EOTypePermissionCode("eo_objection", models.EOTypePermissionView)) {
		t.Errorf("the refusal does not name the codes in the way: %v", err)
	}
	var live int64
	if err := db.Model(&models.EORecordType{}).Where("key = ?", "eo_objection").Count(&live).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if live != 0 {
		t.Errorf("%d live types after a refused create, want 0: the transaction did not roll back", live)
	}
}

// eoCountTypeCodes counts the per-type permission rows for one type key.
func eoCountTypeCodes(t *testing.T, db *gorm.DB, key string) int64 {
	t.Helper()
	var n int64
	if err := db.Model(&models.Permission{}).Where("code IN ?", []string{
		models.EOTypePermissionCode(key, models.EOTypePermissionView),
		models.EOTypePermissionCode(key, models.EOTypePermissionCreate),
		models.EOTypePermissionCode(key, models.EOTypePermissionUpdate),
	}).Count(&n).Error; err != nil {
		t.Fatalf("count the codes of %q: %v", key, err)
	}
	return n
}

// TestEORecordTypeUpdateRefusesAdoptingAnotherTypesCodes is Create's refusal
// read through PUT. models.ValidateTypeEdit returns nil for EVERY edit when
// the type holds no records (models/eo_record_type.go:352), so both of these
// are reachable from the designer — and Create's refusal is worth nothing if
// PUT can walk around it.
func TestEORecordTypeUpdateRefusesAdoptingAnotherTypesCodes(t *testing.T) {
	// retire sets up a key whose per-type codes are LIVE with no live type
	// using the key. Deleting a type now releases its codes, so the remaining
	// way to strand them is an out-of-band key change.
	retire := func(t *testing.T, db *gorm.DB, types EORecordTypeRepository, key string) {
		t.Helper()
		old := &models.EORecordType{ID: uuid.New(), Key: key, Name: key, IsActive: true}
		if err := types.Create(context.Background(), old); err != nil {
			t.Fatalf("create %q: %v", key, err)
		}
		if err := db.Model(&models.EORecordType{}).Where("id = ?", old.ID).Update("key", key+"_moved").Error; err != nil {
			t.Fatalf("move %q away out of band: %v", key, err)
		}
		if n := eoCountTypeCodes(t, db, key); n != 3 {
			t.Fatalf("%d codes survive the rename of %q, want 3: the premise is gone", n, key)
		}
	}

	t.Run("renaming onto a retired type's key", func(t *testing.T) {
		db := eoRecordDB(t)
		types := NewEORecordTypeRepository(db)
		ctx := context.Background()
		retire(t, db, types, "eo_objection")

		mine := &models.EORecordType{ID: uuid.New(), Key: "eo_supplier_note", Name: "Supplier Note", IsActive: true}
		if err := types.Create(ctx, mine); err != nil {
			t.Fatalf("create my type: %v", err)
		}
		renamed := *mine
		renamed.Key = "eo_objection"
		err := types.Update(ctx, &renamed)
		if !errors.Is(err, ErrEOTypePermissionCodesExist) {
			t.Fatalf("Update = %v, want ErrEOTypePermissionCodesExist: the rename would inherit the retired type's grants", err)
		}
		if !strings.Contains(err.Error(), models.EOTypePermissionCode("eo_objection", models.EOTypePermissionView)) {
			t.Errorf("the refusal does not name the codes in the way: %v", err)
		}
		// The check runs BEFORE the save, so no renamed row is committed.
		var stored models.EORecordType
		if err := db.First(&stored, "id = ?", mine.ID).Error; err != nil {
			t.Fatalf("read it back: %v", err)
		}
		if stored.Key != "eo_supplier_note" {
			t.Errorf("key = %q after a refused update, want %q: the rename committed", stored.Key, "eo_supplier_note")
		}
	})

	t.Run("abstract to concrete onto an occupied key", func(t *testing.T) {
		db := eoRecordDB(t)
		types := NewEORecordTypeRepository(db)
		ctx := context.Background()
		retire(t, db, types, "eo_objection")

		// An abstract type may be created on that key: Create writes no codes
		// for an abstract type, so its guard does not fire.
		shell := &models.EORecordType{ID: uuid.New(), Key: "eo_objection", Name: "Supplier Objection", IsAbstract: true, IsActive: true}
		if err := types.Create(ctx, shell); err != nil {
			t.Fatalf("create the abstract shell: %v", err)
		}
		concrete := *shell
		concrete.IsAbstract = false
		err := types.Update(ctx, &concrete)
		if !errors.Is(err, ErrEOTypePermissionCodesExist) {
			t.Fatalf("Update = %v, want ErrEOTypePermissionCodesExist: the transition would inherit the retired type's grants", err)
		}
		var stored models.EORecordType
		if err := db.First(&stored, "id = ?", shell.ID).Error; err != nil {
			t.Fatalf("read it back: %v", err)
		}
		if !stored.IsAbstract {
			t.Error("the type is concrete after a refused update: the save ran before the check")
		}
	})

	t.Run("an ordinary edit still converges the codes", func(t *testing.T) {
		db := eoRecordDB(t)
		types := NewEORecordTypeRepository(db)
		ctx := context.Background()

		// A concrete type whose codes were somehow never written — the state
		// NEW-2's fix converges. The guard must not turn this into a refusal.
		ty := &models.EORecordType{ID: uuid.New(), Key: "eo_board_resolution", Name: "Board Resolution", IsActive: true}
		if err := types.Create(ctx, ty); err != nil {
			t.Fatalf("create: %v", err)
		}
		// HARD delete: permissions.code is uniquely indexed with no deleted_at
		// predicate, so a soft-deleted row still occupies the code. This
		// models a type whose codes were never written, which is the state
		// NEW-2's fix converges.
		if err := db.Unscoped().Where("code LIKE ?", "eo:eo\\_board\\_resolution:%").Delete(&models.Permission{}).Error; err != nil {
			t.Fatalf("remove the codes: %v", err)
		}
		if n := eoCountTypeCodes(t, db, ty.Key); n != 0 {
			t.Fatalf("%d codes left, want 0", n)
		}

		ty.Name = "Board Resolution (revised)"
		if err := types.Update(ctx, ty); err != nil {
			t.Fatalf("an ordinary edit was refused: %v", err)
		}
		if n := eoCountTypeCodes(t, db, ty.Key); n != 3 {
			t.Errorf("%d codes after an ordinary edit, want 3: the guard swallowed the convergence", n)
		}
	})
}

// TestEORecordTypeUpdateDoesNotResurrectADeletedPermission: deleting a
// permission is an administrative act, like revoking a grant, and the next
// edit of the type must not undo it. It must also not CRASH on it:
// permissions.code is uniquely indexed with no deleted_at predicate, so a
// scoped existence check would report the code absent and the insert would
// then violate the index, turning an ordinary edit into a 500.
func TestEORecordTypeUpdateDoesNotResurrectADeletedPermission(t *testing.T) {
	db := eoRecordDB(t)
	types := NewEORecordTypeRepository(db)
	ctx := context.Background()

	ty := &models.EORecordType{ID: uuid.New(), Key: "eo_board_resolution", Name: "Board Resolution", IsActive: true}
	if err := types.Create(ctx, ty); err != nil {
		t.Fatalf("create: %v", err)
	}
	// A soft delete, which is what DELETE /admin/permissions/:id performs.
	if err := db.Where("code LIKE ?", "eo:eo\\_board\\_resolution:%").Delete(&models.Permission{}).Error; err != nil {
		t.Fatalf("delete the codes: %v", err)
	}

	ty.Name = "Board Resolution (revised)"
	if err := types.Update(ctx, ty); err != nil {
		t.Fatalf("an ordinary edit failed over deleted permissions: %v", err)
	}
	if n := eoCountTypeCodes(t, db, ty.Key); n != 0 {
		t.Errorf("%d live codes after an edit, want 0: the designer resurrected what an administrator deleted", n)
	}
}

// TestEORecordTypeRenameCarriesTheCodesAndGrants: renaming eo_a -> eo_b moves
// the three codes IN PLACE (same permission ids), so every grant, including a
// custom role's, follows the type. The old key's codes no longer exist, so
// the old key is free again, renaming back works, and a rename-then-delete
// leaves nothing behind.
func TestEORecordTypeRenameCarriesTheCodesAndGrants(t *testing.T) {
	db := eoRecordDB(t)
	types := NewEORecordTypeRepository(db)
	ctx := context.Background()

	ty := &models.EORecordType{ID: uuid.New(), Key: "eo_a", Name: "A", IsActive: true}
	if err := types.Create(ctx, ty); err != nil {
		t.Fatalf("create: %v", err)
	}

	// A custom role an administrator granted the type to by hand.
	counsel := models.Role{ID: uuid.New(), Name: "Legal Review", Code: "legal_review", IsActive: true}
	if err := db.Create(&counsel).Error; err != nil {
		t.Fatalf("create the custom role: %v", err)
	}
	oldView := models.EOTypePermissionCode("eo_a", models.EOTypePermissionView)
	var viewPerm models.Permission
	if err := db.Where("code = ?", oldView).First(&viewPerm).Error; err != nil {
		t.Fatalf("read %q: %v", oldView, err)
	}
	if err := db.Model(&counsel).Association("Permissions").Append([]models.Permission{viewPerm}); err != nil {
		t.Fatalf("grant it: %v", err)
	}
	grantsBefore := eoCount(t, db, `SELECT COUNT(*) FROM role_permissions`)

	renamed := *ty
	renamed.Key = "eo_b"
	if err := types.Update(ctx, &renamed); err != nil {
		t.Fatalf("rename: %v", err)
	}

	newView := models.EOTypePermissionCode("eo_b", models.EOTypePermissionView)
	if n := eoCountTypeCodes(t, db, "eo_b"); n != 3 {
		t.Errorf("%d codes for the new key, want 3", n)
	}
	if n := eoCountTypeCodes(t, db, "eo_a"); n != 0 {
		t.Errorf("%d codes left under the old key, want 0", n)
	}
	var moved models.Permission
	if err := db.Where("code = ?", newView).First(&moved).Error; err != nil {
		t.Fatalf("read %q: %v", newView, err)
	}
	if moved.ID != viewPerm.ID {
		t.Error("the view permission was re-created, not renamed: its grants would not follow")
	}
	if moved.Name != "EO View eo_b" || moved.Action != "eo_b:view" {
		t.Errorf("name/action not rewritten: %q / %q", moved.Name, moved.Action)
	}
	if got := eoCount(t, db, `SELECT COUNT(*) FROM role_permissions`); got != grantsBefore {
		t.Errorf("grants %d -> %d across a rename, want unchanged", grantsBefore, got)
	}
	if !eoRoleUser(t, db, "legal_review").HasPermission(newView) {
		t.Error("the custom role lost the type on rename")
	}
	if !eoRoleUser(t, db, "executive_office").HasPermission(newView) {
		t.Error("executive_office lost the type on rename")
	}

	// renaming back is allowed: nothing is stranded under eo_a.
	back := renamed
	back.Key = "eo_a"
	if err := types.Update(ctx, &back); err != nil {
		t.Fatalf("renaming back: %v", err)
	}
	if n := eoCountTypeCodes(t, db, "eo_a"); n != 3 {
		t.Errorf("%d codes after renaming back, want 3", n)
	}
}

// TestEORecordType_RenamedThenDeletedKeyIsReusable: the case the first B3
// round missed. Rename, delete, then create under the ORIGINAL key.
func TestEORecordType_RenamedThenDeletedKeyIsReusable(t *testing.T) {
	db := eoRecordDB(t)
	types := NewEORecordTypeRepository(db)
	ctx := context.Background()

	ty := &models.EORecordType{ID: uuid.New(), Key: "eo_task_note", Name: "Task Note", IsActive: true}
	if err := types.Create(ctx, ty); err != nil {
		t.Fatal(err)
	}
	renamed := *ty
	renamed.Key = "eo_note_moved"
	if err := types.Update(ctx, &renamed); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err := types.Delete(ctx, ty.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	for _, key := range []string{"eo_task_note", "eo_note_moved"} {
		if n := eoCountTypeCodes(t, db, key); n != 0 {
			t.Errorf("%d live codes under %q after rename+delete, want 0", n, key)
		}
		if n := eoTypeGrants(t, db, key); n != 0 {
			t.Errorf("%d grants under %q after rename+delete, want 0", n, key)
		}
	}
	again := &models.EORecordType{ID: uuid.New(), Key: "eo_task_note", Name: "Task Note 2", IsActive: true}
	if err := types.Create(ctx, again); err != nil {
		t.Fatalf("re-create the original key: %v", err)
	}
	if n := eoCountTypeCodes(t, db, "eo_task_note"); n != 3 {
		t.Errorf("%d live codes after re-create, want 3", n)
	}
}
