package services

import (
	"context"
	"errors"
	"testing"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
)

// TestResolveWorkflowUsesTheIndexedRead covers I9.
//
// ResolveWorkflowForType runs on every EO create, every transition and every
// record-detail load. List(ctx, true) loads EVERY active workflow in the
// database with seven preloads — States, Transitions, Classifications,
// Locations, CreatedBy, ConvertToRequestRoles, MergeAllowedRoles — and then
// throws all but one away in Go.
//
// The single-line mutation that makes this fail: in ResolveWorkflowForType,
// put `all, err := repo.List(ctx, true)` back.
func TestResolveWorkflowUsesTheIndexedRead(t *testing.T) {
	typeID := uuid.New()
	other := uuid.New()
	repo := &eoWorkflowFakeRepo{workflows: []models.Workflow{
		eoWfBound("eo-directive", &typeID, "eo"),
		eoWfBound("eo-task", &other, "eo"),
		eoWfBound("incident-all", nil, "all"),
	}}

	wf, err := ResolveWorkflowForType(context.Background(), typeID, repo)
	if err != nil {
		t.Fatalf("ResolveWorkflowForType: %v", err)
	}
	if wf.Code != "eo-directive" {
		t.Errorf("workflow = %q, want eo-directive", wf.Code)
	}
	if repo.byTypeCalls != 1 {
		t.Errorf("ListByRecordTypeID calls = %d, want 1", repo.byTypeCalls)
	}
	if repo.listCalls != 0 {
		t.Errorf("List calls = %d, want 0: resolving one type's workflow must not load every active workflow with seven preloads", repo.listCalls)
	}
}

// TestResolveWorkflowKeepsItsErrorSemantics: the read changed, the contract
// did not. Zero bound workflows and more than one are both errors, with the
// messages callers and their tests already read.
func TestResolveWorkflowKeepsItsErrorSemantics(t *testing.T) {
	typeID := uuid.New()

	t.Run("zero is an error", func(t *testing.T) {
		repo := &eoWorkflowFakeRepo{workflows: []models.Workflow{eoWfBound("incident-all", nil, "all")}}
		if _, err := ResolveWorkflowForType(context.Background(), typeID, repo); err == nil {
			t.Fatal("want an error for a type with no bound workflow")
		}
	})
	t.Run("more than one is an error", func(t *testing.T) {
		repo := &eoWorkflowFakeRepo{workflows: []models.Workflow{
			eoWfBound("a", &typeID, "eo"),
			eoWfBound("b", &typeID, "eo"),
		}}
		if _, err := ResolveWorkflowForType(context.Background(), typeID, repo); err == nil {
			t.Fatal("want an error for a type bound to two workflows")
		}
	})
	t.Run("an inactive workflow is not a match", func(t *testing.T) {
		inactive := eoWfBound("retired", &typeID, "eo")
		inactive.IsActive = false
		repo := &eoWorkflowFakeRepo{workflows: []models.Workflow{inactive}}
		if _, err := ResolveWorkflowForType(context.Background(), typeID, repo); err == nil {
			t.Fatal("want an error: the only bound workflow is inactive")
		}
	})
}

// TestUpdateEORecordTakesTheRowLock covers the lost-update race.
//
// Update wrote every column of the record back (eoUpdatableColumns lists them,
// so that clearing a due date is not mistaken for leaving it alone) from a
// snapshot it had read on a pooled connection. Two concurrent updates both
// read the same pre-image — a transaction alone does not prevent that under
// READ COMMITTED — and the second write silently reverted the first's edits,
// including to keys it never mentioned. Execute has taken the row lock since
// Task 12; this is the same lock on the same table.
//
// The single-line mutation that makes this fail: in Update, replace
// `fresh, err := r.LockForUpdate(ctx, id, rec.RecordTypeID)` with
// `fresh, err := r.GetByID(ctx, id, rec.RecordTypeID)`.
func TestUpdateEORecordTakesTheRowLock(t *testing.T) {
	f := eoNewSvcFixture(t)
	rec := eoCreateForUpdate(t, f)

	if f.records.lockCalls != 0 {
		t.Fatalf("lockCalls = %d before the update, want 0", f.records.lockCalls)
	}
	got, err := f.svc.Update(context.Background(), rec.ID, &models.EORecordUpdateRequest{
		RecordTypeID: rec.RecordTypeID,
		Values:       map[string]any{"priority": "high"},
	}, f.actor)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if f.records.lockCalls != 1 {
		t.Errorf("LockForUpdate calls = %d, want 1: Update must hold the row lock, not merely open a transaction", f.records.lockCalls)
	}
	if got.Priority != "high" {
		t.Errorf("priority = %q, want high", got.Priority)
	}
	if len(f.records.updated) != 1 {
		t.Errorf("repository Update calls = %d, want 1", len(f.records.updated))
	}
}

// TestUpdateEORecordValidatesAgainstTheLockedRow: the lock is not only a
// serialisation device. The pipeline's baseline — what is_locked_after and the
// locked-field check compare a submitted value against — must be the row the
// transaction holds, not the snapshot read before it opened. Here the stored
// record is changed behind the service's back between the pre-read and the
// lock, and the write must carry the newer value forward rather than reverting
// it.
func TestUpdateEORecordValidatesAgainstTheLockedRow(t *testing.T) {
	f := eoNewSvcFixture(t)
	rec := eoCreateForUpdate(t, f)

	// Somebody else's committed edit, landing after this service's own
	// pre-read would have been taken.
	f.records.onLock = func(stored *models.EORecord) {
		stored.Source = "ministerial_office"
	}

	got, err := f.svc.Update(context.Background(), rec.ID, &models.EORecordUpdateRequest{
		RecordTypeID: rec.RecordTypeID,
		Values:       map[string]any{"priority": "high"},
	}, f.actor)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.Source != "ministerial_office" {
		t.Errorf("source = %q, want %q: the update reverted a field it never mentioned", got.Source, "ministerial_office")
	}
	if got.Priority != "high" {
		t.Errorf("priority = %q, want high", got.Priority)
	}
}

// TestUpdateEORecordRefusesAMissingRow keeps the not-found sentinel intact now
// that the read happens inside the transaction.
func TestUpdateEORecordRefusesAMissingRow(t *testing.T) {
	f := eoNewSvcFixture(t)
	rec := eoCreateForUpdate(t, f)

	// Gone between the pre-read and the lock.
	f.records.onLock = func(stored *models.EORecord) { delete(f.records.stored, rec.ID) }

	_, err := f.svc.Update(context.Background(), rec.ID, &models.EORecordUpdateRequest{
		RecordTypeID: rec.RecordTypeID,
		Values:       map[string]any{"priority": "high"},
	}, f.actor)
	if err == nil {
		t.Fatal("want an error when the row vanishes before the lock")
	}
	if !errors.Is(err, ErrEORecordNotFound) {
		t.Errorf("error = %v, want ErrEORecordNotFound", err)
	}
}

// eoCreateForUpdate writes one record through the real pipeline, so the
// fixture's stored state is what the engine itself would have produced.
func eoCreateForUpdate(t *testing.T, f *eoSvcFixture) *models.EORecord {
	t.Helper()
	rec, err := f.svc.Create(context.Background(), &models.EORecordCreateRequest{
		RecordTypeID: f.recordType.ID,
		Values:       map[string]any{"subject_ar": "موضوع", "priority": "normal", "source": "portal"},
	}, f.actor)
	if err != nil {
		t.Fatalf("create the record under test: %v", err)
	}
	f.records.lockCalls = 0
	f.records.updated = nil
	return rec
}
