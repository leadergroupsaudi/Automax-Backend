package repository

import (
	"context"
	"testing"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
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
