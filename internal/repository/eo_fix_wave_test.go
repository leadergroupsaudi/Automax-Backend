package repository

import (
	"context"
	"strings"
	"testing"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TestEORecordLinksRejectADuplicatePair covers the uniqueness of
// (from_record_id, to_record_id, link_type_key).
//
// services.ValidateLink's duplicate check reads the existing links and then
// inserts, outside any transaction, so two concurrent POST /:id/links both
// read "not linked yet" and both write. The guarantee has to be the
// database's. Remove the uq_eo_record_links_pair_type index from
// migrations.MigrateEOPlatform and the second Create below succeeds.
func TestEORecordLinksRejectADuplicatePair(t *testing.T) {
	db := eoMigratedDB(t)

	from := eoNewTestRecord(uuid.New(), "EO-DIR-2026-110")
	to := eoNewTestRecord(uuid.New(), "EO-TSK-2026-110")
	other := eoNewTestRecord(uuid.New(), "EO-TSK-2026-111")
	for _, rec := range []*models.EORecord{from, to, other} {
		if err := db.Create(rec).Error; err != nil {
			t.Fatalf("create record %s: %v", rec.Reference, err)
		}
	}

	link := func(f, to *models.EORecord, key string) *models.EORecordLink {
		return &models.EORecordLink{
			FromRecordID:     f.ID,
			FromRecordTypeID: f.RecordTypeID,
			ToRecordID:       to.ID,
			ToRecordTypeID:   to.RecordTypeID,
			LinkTypeKey:      key,
		}
	}

	if err := db.Create(link(from, to, "spawned_task")).Error; err != nil {
		t.Fatalf("create the first link: %v", err)
	}
	if err := eoQuiet(db).Create(link(from, to, "spawned_task")).Error; err == nil {
		t.Error("a second link of the same type between the same pair was accepted, want a unique violation")
	}

	// The constraint is on the TRIPLE, not on the pair: a different link type
	// between the same two records, and the same link type to a different
	// record, are both legitimate.
	if err := db.Create(link(from, to, "related_to")).Error; err != nil {
		t.Errorf("a different link type between the same pair was rejected: %v", err)
	}
	if err := db.Create(link(from, other, "spawned_task")).Error; err != nil {
		t.Errorf("the same link type to a different record was rejected: %v", err)
	}
	// It is DIRECTED: the reverse link is a different relationship.
	if err := db.Create(link(to, from, "spawned_task")).Error; err != nil {
		t.Errorf("the reverse link was rejected: %v", err)
	}
}

// TestEORecordTypeDescendantIDsWalksTheSubtree covers the repository half of
// the §7.14 guard-rail fix: an abstract ancestor's records are its
// descendants' records, so the designer needs the subtree.
func TestEORecordTypeDescendantIDsWalksTheSubtree(t *testing.T) {
	db := eoRecordDB(t)
	repo := NewEORecordTypeRepository(db)
	ctx := context.Background()

	add := func(key string, parent *uuid.UUID) *models.EORecordType {
		t.Helper()
		rt := &models.EORecordType{ID: uuid.New(), Key: key, Name: key, ParentTypeID: parent}
		if err := repo.Create(ctx, rt); err != nil {
			t.Fatalf("create record type %q: %v", key, err)
		}
		return rt
	}

	base := add("eo_base", nil)
	mid := add("eo_correspondence", &base.ID)
	leaf := add("eo_directive", &mid.ID)
	sibling := add("eo_task", &base.ID)
	unrelated := add("eo_other_root", nil)

	got, err := repo.DescendantIDs(ctx, base.ID)
	if err != nil {
		t.Fatalf("DescendantIDs: %v", err)
	}
	want := map[uuid.UUID]bool{base.ID: true, mid.ID: true, leaf.ID: true, sibling.ID: true}
	if len(got) != len(want) {
		t.Fatalf("DescendantIDs returned %d ids, want %d: %v", len(got), len(want), got)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("DescendantIDs returned %s, which is not in the subtree", id)
		}
		if id == unrelated.ID {
			t.Errorf("DescendantIDs returned the unrelated root %s", id)
		}
	}

	// A leaf is its own subtree, and nothing more.
	leafIDs, err := repo.DescendantIDs(ctx, leaf.ID)
	if err != nil {
		t.Fatalf("DescendantIDs of a leaf: %v", err)
	}
	if len(leafIDs) != 1 || leafIDs[0] != leaf.ID {
		t.Errorf("DescendantIDs of a leaf = %v, want just %s", leafIDs, leaf.ID)
	}

	children, err := repo.ChildTypes(ctx, base.ID)
	if err != nil {
		t.Fatalf("ChildTypes: %v", err)
	}
	// DIRECT children only: the grandchild is not one.
	if len(children) != 2 {
		t.Fatalf("ChildTypes returned %d types, want 2 (the direct children only)", len(children))
	}
	for _, c := range children {
		if c.ID == leaf.ID {
			t.Errorf("ChildTypes returned the grandchild %q", c.Key)
		}
	}
	if none, err := repo.ChildTypes(ctx, leaf.ID); err != nil || len(none) != 0 {
		t.Errorf("ChildTypes of a leaf = %v, %v, want no types and no error", none, err)
	}
}

// TestEORecordTypeDescendantIDsRefusesACycle proves the walk cannot loop: a
// corrupted parent chain is an error inside a request handler, not a hang.
func TestEORecordTypeDescendantIDsRefusesACycle(t *testing.T) {
	db := eoRecordDB(t)
	repo := NewEORecordTypeRepository(db)
	ctx := context.Background()

	a := &models.EORecordType{ID: uuid.New(), Key: "eo_a", Name: "A"}
	b := &models.EORecordType{ID: uuid.New(), Key: "eo_b", Name: "B", ParentTypeID: &a.ID}
	for _, rt := range []*models.EORecordType{a, b} {
		if err := repo.Create(ctx, rt); err != nil {
			t.Fatalf("create %q: %v", rt.Key, err)
		}
	}
	a.ParentTypeID = &b.ID
	if err := repo.Update(ctx, a); err != nil {
		t.Fatalf("close the cycle: %v", err)
	}

	if _, err := repo.DescendantIDs(ctx, a.ID); err == nil {
		t.Error("DescendantIDs accepted an inheritance cycle, want an error")
	} else if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("DescendantIDs error = %v, want it to name the cycle", err)
	}
}

// TestWorkflowListByRecordTypeIDMatchesOnTheColumn covers the indexed read
// that replaced List(ctx, true) in services.ResolveWorkflowForType: it matches
// on record_type_id and is not fooled by the record_type string's 'all' and
// 'both' wildcards.
func TestWorkflowListByRecordTypeIDMatchesOnTheColumn(t *testing.T) {
	db := eoMigratedDB(t)
	migrationDB := db.Session(&gorm.Session{})
	migrationDB.Config.DisableForeignKeyConstraintWhenMigrating = true
	if err := migrationDB.Migrator().CreateTable(&models.Workflow{}, &models.WorkflowState{}, &models.WorkflowTransition{}); err != nil {
		t.Fatalf("create workflow tables: %v", err)
	}
	repo := NewWorkflowRepository(db)
	ctx := context.Background()

	typeID := uuid.New()
	otherType := uuid.New()
	bound := &models.Workflow{ID: uuid.New(), Code: "eo-dir", Name: "EO Directive", RecordType: "eo", RecordTypeID: &typeID, IsActive: true}
	wildcard := &models.Workflow{ID: uuid.New(), Code: "inc-all", Name: "Incident All", RecordType: "all", IsActive: true}
	elsewhere := &models.Workflow{ID: uuid.New(), Code: "eo-task", Name: "EO Task", RecordType: "eo", RecordTypeID: &otherType, IsActive: true}
	inactive := &models.Workflow{ID: uuid.New(), Code: "eo-old", Name: "EO Retired", RecordType: "eo", RecordTypeID: &typeID, IsActive: false}
	for _, wf := range []*models.Workflow{bound, wildcard, elsewhere, inactive} {
		if err := repo.Create(ctx, wf); err != nil {
			t.Fatalf("create workflow %q: %v", wf.Code, err)
		}
	}
	// IsActive carries a `default:true` GORM tag, so Create omits the zero
	// value and the column takes the default. Save writes every column, which
	// is what actually makes this one inactive.
	inactive.IsActive = false
	if err := repo.Update(ctx, inactive); err != nil {
		t.Fatalf("deactivate %q: %v", inactive.Code, err)
	}
	if err := repo.CreateState(ctx, &models.WorkflowState{ID: uuid.New(), WorkflowID: bound.ID, Name: "Draft", StateType: "initial"}); err != nil {
		t.Fatalf("create state: %v", err)
	}

	got, err := repo.ListByRecordTypeID(ctx, typeID)
	if err != nil {
		t.Fatalf("ListByRecordTypeID: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListByRecordTypeID returned %d workflows, want 1 (the active one bound to this type)", len(got))
	}
	if got[0].Code != bound.Code {
		t.Errorf("workflow = %q, want %q", got[0].Code, bound.Code)
	}
	// States are preloaded: services.InitialState reads them off the result.
	if len(got[0].States) != 1 {
		t.Errorf("States = %d, want 1 preloaded state", len(got[0].States))
	}

	if none, err := repo.ListByRecordTypeID(ctx, uuid.New()); err != nil || len(none) != 0 {
		t.Errorf("ListByRecordTypeID for an unbound type = %v, %v, want none and no error", none, err)
	}
}
