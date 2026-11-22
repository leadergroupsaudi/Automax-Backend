package services

import (
	"context"
	"errors"
	"testing"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// eoWireCap records which hooks the pipelines call and with what.
type eoWireCap struct {
	Capability
	validateCalls, transitionCalls, deriveCalls int
	validateErrs                                map[string]string
	transitionErr                               error
	derived                                     map[string]any
	actorSeen                                   *models.User
	transitionSeen                              string
	valuesSeenByValidate                        string
}

func (c *eoWireCap) Key() string { return "eo_wire" }

func (c *eoWireCap) Validate(_ context.Context, rec *models.EORecord, _ datatypes.JSON) (map[string]string, error) {
	c.validateCalls++
	c.valuesSeenByValidate = string(rec.Values)
	return c.validateErrs, nil
}

func (c *eoWireCap) Derive(context.Context, *models.EORecord, datatypes.JSON) (map[string]any, error) {
	c.deriveCalls++
	return c.derived, nil
}

func (c *eoWireCap) OnTransition(ctx context.Context, rec *models.EORecord, t *models.WorkflowTransition, _ datatypes.JSON) error {
	c.transitionCalls++
	c.transitionSeen = t.Code
	c.actorSeen, _ = ctx.Value(eoActorCtxKey{}).(*models.User)
	return c.transitionErr
}

func eoWireResolver(c Capability) eoCapabilityResolver {
	return func(*models.EORecordType) ([]EOEnabledCapability, error) {
		return []EOEnabledCapability{{Capability: c}}, nil
	}
}

func TestCapabilityHooksFireOnSave(t *testing.T) {
	create := func(f *eoSvcFixture) (*models.EORecord, error) {
		return f.svc.Create(context.Background(), &models.EORecordCreateRequest{
			RecordTypeID: f.recordType.ID, Values: map[string]any{"subject_ar": "x"},
		}, f.actor)
	}

	t.Run("create runs Validate and Derive, and a derived key needs no field definition", func(t *testing.T) {
		f := eoNewSvcFixture(t)
		c := &eoWireCap{derived: map[string]any{"eo_wire_flag": true}}
		f.svc.(*eoRecordService).caps = eoWireResolver(c)
		rec, err := create(f)
		if err != nil {
			t.Fatal(err)
		}
		if c.validateCalls != 1 || c.deriveCalls != 1 {
			t.Fatalf("validate=%d derive=%d, want 1 and 1", c.validateCalls, c.deriveCalls)
		}
		if eoSvcValues(t, rec.Values)["eo_wire_flag"] != true {
			t.Errorf("derived value not on the record: %s", rec.Values)
		}
	})
	t.Run("create: capability validation errors reject the save and nothing is persisted", func(t *testing.T) {
		f := eoNewSvcFixture(t)
		f.svc.(*eoRecordService).caps = eoWireResolver(&eoWireCap{validateErrs: map[string]string{"x": "bad"}})
		_, err := create(f)
		if eoSvcFieldErrors(t, err)["x"] != "bad" || len(f.records.created) != 0 {
			t.Fatalf("err=%v created=%d", err, len(f.records.created))
		}
	})
	t.Run("create: an unknown capability on the type is an error", func(t *testing.T) {
		f := eoNewSvcFixture(t)
		f.recordType.Capabilities = datatypes.JSON(`[{"key":"eo_nonexistent_cap"}]`)
		_, err := create(f)
		if !errors.Is(err, ErrEOUnknownCapability) || len(f.records.created) != 0 {
			t.Fatalf("err=%v created=%d", err, len(f.records.created))
		}
	})
	t.Run("update runs Validate and Derive", func(t *testing.T) {
		f := eoNewSvcFixture(t, eoSvcField("note", models.EORequiredNever))
		rec := eoSvcExisting(f, nil)
		c := &eoWireCap{derived: map[string]any{"eo_wire_flag": 1}}
		f.svc.(*eoRecordService).caps = eoWireResolver(c)
		got, err := f.svc.Update(context.Background(), rec.ID, &models.EORecordUpdateRequest{
			RecordTypeID: rec.RecordTypeID, Values: map[string]any{"note": "n"},
		}, f.actor)
		if err != nil {
			t.Fatal(err)
		}
		if c.validateCalls != 1 || c.deriveCalls != 1 {
			t.Fatalf("validate=%d derive=%d", c.validateCalls, c.deriveCalls)
		}
		if eoSvcValues(t, got.Values)["eo_wire_flag"] != float64(1) {
			t.Errorf("derived value missing: %s", got.Values)
		}
	})
	t.Run("update: capability validation errors reject the save", func(t *testing.T) {
		f := eoNewSvcFixture(t, eoSvcField("note", models.EORequiredNever))
		rec := eoSvcExisting(f, nil)
		f.svc.(*eoRecordService).caps = eoWireResolver(&eoWireCap{validateErrs: map[string]string{"x": "bad"}})
		_, err := f.svc.Update(context.Background(), rec.ID, &models.EORecordUpdateRequest{
			RecordTypeID: rec.RecordTypeID, Values: map[string]any{"note": "n"},
		}, f.actor)
		if eoSvcFieldErrors(t, err)["x"] != "bad" || len(f.records.updated) != 0 {
			t.Fatalf("err=%v updated=%d", err, len(f.records.updated))
		}
	})
	t.Run("a request cannot write the sealed approval stamp key", func(t *testing.T) {
		f := eoNewSvcFixture(t)
		_, err := f.svc.Create(context.Background(), &models.EORecordCreateRequest{
			RecordTypeID: f.recordType.ID,
			Values:       map[string]any{"subject_ar": "x", "approval": map[string]any{"recorded_by": "forged"}},
		}, f.actor)
		if eoSvcFieldErrors(t, err)["approval"] == "" {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestCapabilityHooksFireOnTransition(t *testing.T) {
	t.Run("Validate, OnTransition and Derive run, with the actor in ctx, and the result is committed", func(t *testing.T) {
		f := eoNewTxFixture(t)
		c := &eoWireCap{derived: map[string]any{"eo_wire_flag": true}}
		f.svc.(*eoTransitionService).caps = eoWireResolver(c)
		got, err := f.run("raise", f.actor, models.EOTransitionInput{})
		if err != nil {
			t.Fatal(err)
		}
		if c.validateCalls != 1 || c.transitionCalls != 1 || c.deriveCalls != 1 {
			t.Fatalf("validate=%d transition=%d derive=%d", c.validateCalls, c.transitionCalls, c.deriveCalls)
		}
		if c.transitionSeen != "raise" || c.actorSeen != f.actor {
			t.Errorf("transition=%q actor=%v", c.transitionSeen, c.actorSeen)
		}
		if eoSvcValues(t, got.Values)["eo_wire_flag"] != true || !f.records.committed {
			t.Errorf("derived=%s committed=%v", got.Values, f.records.committed)
		}
	})
	t.Run("an OnTransition error rolls the transition back", func(t *testing.T) {
		f := eoNewTxFixture(t)
		f.svc.(*eoTransitionService).caps = eoWireResolver(&eoWireCap{transitionErr: errors.New("no")})
		if _, err := f.run("raise", f.actor, models.EOTransitionInput{}); err == nil {
			t.Fatal("want error")
		}
		if f.records.committed || len(f.records.updated) != 0 {
			t.Error("the record was written despite the hook error")
		}
	})
	t.Run("a Validate error rejects the transition", func(t *testing.T) {
		f := eoNewTxFixture(t)
		f.svc.(*eoTransitionService).caps = eoWireResolver(&eoWireCap{validateErrs: map[string]string{"x": "bad"}})
		_, err := f.run("raise", f.actor, models.EOTransitionInput{})
		if eoSvcFieldErrors(t, err)["x"] != "bad" || f.records.committed {
			t.Fatalf("err=%v committed=%v", err, f.records.committed)
		}
	})
	t.Run("an unknown capability on the type is an error before anything is written", func(t *testing.T) {
		f := eoNewTxFixture(t)
		f.recordType.Capabilities = datatypes.JSON(`[{"key":"eo_nonexistent_cap"}]`)
		_, err := f.run("raise", f.actor, models.EOTransitionInput{})
		if !errors.Is(err, ErrEOUnknownCapability) || f.records.committed {
			t.Fatalf("err=%v committed=%v", err, f.records.committed)
		}
	})
}

// The approval capability end to end through the real transition pipeline.
func TestApprovalStampThroughTransition(t *testing.T) {
	setup := func(t *testing.T) *eoTxFixture {
		f := eoNewTxFixture(t)
		f.types.defs = append(f.types.defs,
			eoTxField("approval_authority", models.EOKindText, models.EORequiredNever, f.recordType.ID),
			eoTxField("approval_delegation_note", models.EOKindText, models.EORequiredNever, f.recordType.ID))
		f.transition("raise").FieldChanges = []models.TransitionFieldChange{
			{FieldName: "approval_authority", Label: "Authority"},
			{FieldName: "approval_delegation_note", Label: "Delegation"},
		}
		f.svc.(*eoTransitionService).caps = func(*models.EORecordType) ([]EOEnabledCapability, error) {
			return []EOEnabledCapability{{Capability: NewEOApprovalCapability(), Config: datatypes.JSON(`{"transitions":["raise"]}`)}}, nil
		}
		return f
	}
	t.Run("the stamp records the actor and the authority separately", func(t *testing.T) {
		f := setup(t)
		got, err := f.run("raise", f.actor, models.EOTransitionInput{Values: map[string]any{
			"approval_authority": "H.E. the Secretary", "approval_delegation_note": "via his office",
		}})
		if err != nil {
			t.Fatal(err)
		}
		s, _ := eoSvcValues(t, got.Values)["approval"].(map[string]any)
		if s["recorded_by"] != f.actor.ID.String() || s["authority"] != "H.E. the Secretary" || s["delegation_note"] != "via his office" {
			t.Fatalf("stamp = %v", s)
		}
	})
	t.Run("no authority: the transition is refused and nothing is written", func(t *testing.T) {
		f := setup(t)
		_, err := f.run("raise", f.actor, models.EOTransitionInput{})
		if eoSvcFieldErrors(t, err)["approval_authority"] == "" || f.records.committed {
			t.Fatalf("err=%v committed=%v", err, f.records.committed)
		}
	})
	t.Run("an unlisted transition does not stamp", func(t *testing.T) {
		f := setup(t)
		f.svc.(*eoTransitionService).caps = func(*models.EORecordType) ([]EOEnabledCapability, error) {
			return []EOEnabledCapability{{Capability: NewEOApprovalCapability(), Config: datatypes.JSON(`{"transitions":["close"]}`)}}, nil
		}
		got, err := f.run("raise", f.actor, models.EOTransitionInput{})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := eoSvcValues(t, got.Values)["approval"]; ok {
			t.Error("stamped on an unlisted transition")
		}
	})
}

// eoInTxRecords marks the window in which a transaction is open.
type eoInTxRecords struct {
	*eoTxRecordRepo
	open *bool
}

func (r eoInTxRecords) Transaction(ctx context.Context, fn func(repository.EORecordRepository) error) error {
	*r.open = true
	defer func() { *r.open = false }()
	return r.eoTxRecordRepo.Transaction(ctx, fn)
}

// eoInTxTypes counts type-repository reads taken while a transaction is open.
type eoInTxTypes struct {
	*eoTxTypeRepo
	open *bool
	inTx *[]string
}

func (r eoInTxTypes) note(name string) {
	if *r.open {
		*r.inTx = append(*r.inTx, name)
	}
}

func (r eoInTxTypes) GetByID(ctx context.Context, id uuid.UUID) (*models.EORecordType, error) {
	r.note("GetByID")
	return r.eoTxTypeRepo.GetByID(ctx, id)
}

func (r eoInTxTypes) Ancestors(ctx context.Context, t *models.EORecordType) ([]models.EORecordType, error) {
	r.note("Ancestors")
	return r.eoTxTypeRepo.Ancestors(ctx, t)
}

func (r eoInTxTypes) FieldDefs(ctx context.Context, ids []uuid.UUID) ([]models.EOFieldDef, error) {
	r.note("FieldDefs")
	return r.eoTxTypeRepo.FieldDefs(ctx, ids)
}

// The capability list and the effective fields are read on the pooled handle
// BEFORE the transaction, never inside it.
func TestTransitionReadsTypeAndFieldsBeforeTheTransaction(t *testing.T) {
	f := eoNewTxFixture(t)
	open := false
	var inTx []string
	svc := NewEOTransitionService(
		eoInTxRecords{f.records, &open},
		eoInTxTypes{eoTxTypeRepo: f.types, open: &open, inTx: &inTx},
		f.workflows, f.actions)
	if _, err := svc.Execute(context.Background(), f.rec, f.transition("raise").ID, f.actor, models.EOTransitionInput{}); err != nil {
		t.Fatal(err)
	}
	if len(inTx) != 0 {
		t.Fatalf("pooled type reads inside the transaction: %v", inTx)
	}
}

func TestMergeFieldErrorsToleratesANilDestination(t *testing.T) {
	got := eoMergeFieldErrors(nil, map[string]string{"a": "bad"})
	if got["a"] != "bad" {
		t.Fatalf("got %v", got)
	}
	if got := eoMergeFieldErrors(nil, nil); got == nil {
		t.Error("want a usable map back, not nil")
	}
}
