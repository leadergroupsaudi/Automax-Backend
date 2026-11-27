package services

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// eoPlanGateReader serves what plan_gate reads: records by id, the plan type
// by key, a parent's plans, and state codes.
type eoPlanGateReader struct {
	EOCapabilityReader
	records  map[uuid.UUID]models.EORecord
	planType models.EORecordType
	children map[uuid.UUID][]models.EORecord
	codes    map[uuid.UUID]string
}

func (r *eoPlanGateReader) Record(_ context.Context, id uuid.UUID) (*models.EORecord, error) {
	if rec, ok := r.records[id]; ok {
		return &rec, nil
	}
	return nil, nil
}

func (r *eoPlanGateReader) RecordType(_ context.Context, key string) (*models.EORecordType, error) {
	if key == r.planType.Key {
		return &r.planType, nil
	}
	return nil, nil
}

func (r *eoPlanGateReader) Children(_ context.Context, parentID uuid.UUID, key string) ([]models.EORecord, error) {
	return r.children[parentID], nil
}

func (r *eoPlanGateReader) StateCode(_ context.Context, id *uuid.UUID) (string, error) {
	if id == nil {
		return "", nil
	}
	return r.codes[*id], nil
}

var eoPlanGateCfg = datatypes.JSON(`{"plan_type_key":"eo_execution_plan","approved_states":["approved"],"plan_required_key":"plan_required"}`)

type eoPlanGateFixture struct {
	read                  *eoPlanGateReader
	directive, plan       models.EORecord
	drafting, approvedID  uuid.UUID
	directiveTypeID, asgT uuid.UUID
}

func newEOPlanGateFixture(planRequired bool) *eoPlanGateFixture {
	f := &eoPlanGateFixture{drafting: uuid.New(), approvedID: uuid.New(),
		directiveTypeID: uuid.New(), asgT: uuid.New()}
	planType := models.EORecordType{ID: uuid.New(), Key: "eo_execution_plan"}
	vals, _ := json.Marshal(map[string]any{"plan_required": planRequired})
	f.directive = models.EORecord{ID: uuid.New(), RecordTypeID: f.directiveTypeID,
		Reference: "EO-DIR-2026-010", Values: datatypes.JSON(vals)}
	f.plan = models.EORecord{ID: uuid.New(), RecordTypeID: planType.ID, Reference: "EO-DIR-2026-010-PLN",
		ParentRecordID: &f.directive.ID, StateID: &f.drafting}
	f.read = &eoPlanGateReader{
		records:  map[uuid.UUID]models.EORecord{f.directive.ID: f.directive, f.plan.ID: f.plan},
		planType: planType,
		children: map[uuid.UUID][]models.EORecord{},
		codes:    map[uuid.UUID]string{f.drafting: "drafting", f.approvedID: "approved"},
	}
	return f
}

func (f *eoPlanGateFixture) withPlan(approved bool) {
	if approved {
		f.plan.StateID = &f.approvedID
	}
	f.read.records[f.plan.ID] = f.plan
	f.read.children[f.directive.ID] = []models.EORecord{f.plan}
}

func (f *eoPlanGateFixture) newAssignment(parent uuid.UUID) *models.EORecord {
	return &models.EORecord{ID: uuid.New(), RecordTypeID: f.asgT, ParentRecordID: &parent}
}

func eoPlanGateRun(t *testing.T, f *eoPlanGateFixture, rec *models.EORecord) map[string]string {
	t.Helper()
	errs, err := NewEOPlanGateCapability().Validate(context.Background(), f.read, rec, eoPlanGateCfg)
	if err != nil {
		t.Fatal(err)
	}
	return errs
}

// AC-05.2 under the plan: a plan still in drafting refuses the assignment.
func TestPlanGateRefusesAnAssignmentUnderADraftPlan(t *testing.T) {
	f := newEOPlanGateFixture(true)
	f.withPlan(false)
	if errs := eoPlanGateRun(t, f, f.newAssignment(f.plan.ID)); errs[eoPlanGateErrorKey] == "" {
		t.Fatalf("assignment under a draft plan was issued: %v", errs)
	}
}

// AC-05.3 under the plan: once approved, the assignment is issued.
func TestPlanGateIssuesAnAssignmentUnderAnApprovedPlan(t *testing.T) {
	f := newEOPlanGateFixture(true)
	f.withPlan(true)
	if errs := eoPlanGateRun(t, f, f.newAssignment(f.plan.ID)); len(errs) != 0 {
		t.Fatalf("assignment under an approved plan refused: %v", errs)
	}
}

// AC-05.2 under the directive: a directive whose plan is not approved refuses.
func TestPlanGateRefusesAnAssignmentUnderADirectiveWithADraftPlan(t *testing.T) {
	f := newEOPlanGateFixture(true)
	f.withPlan(false)
	if errs := eoPlanGateRun(t, f, f.newAssignment(f.directive.ID)); errs[eoPlanGateErrorKey] == "" {
		t.Fatalf("assignment under a directive with a draft plan was issued: %v", errs)
	}
}

// AC-05.3 under the directive.
func TestPlanGateIssuesAnAssignmentUnderADirectiveWithAnApprovedPlan(t *testing.T) {
	f := newEOPlanGateFixture(true)
	f.withPlan(true)
	if errs := eoPlanGateRun(t, f, f.newAssignment(f.directive.ID)); len(errs) != 0 {
		t.Fatalf("assignment under a directive with an approved plan refused: %v", errs)
	}
}

// A directive that needs a plan but has none yet refuses.
func TestPlanGateRefusesWhenAPlanIsRequiredButMissing(t *testing.T) {
	f := newEOPlanGateFixture(true)
	if errs := eoPlanGateRun(t, f, f.newAssignment(f.directive.ID)); errs[eoPlanGateErrorKey] == "" {
		t.Fatalf("assignment issued although the directive requires a plan and has none: %v", errs)
	}
}

// A directive that needs no plan is assigned directly (Q1 = no).
func TestPlanGateAllowsDirectAssignmentWhenNoPlanIsRequired(t *testing.T) {
	f := newEOPlanGateFixture(false)
	if errs := eoPlanGateRun(t, f, f.newAssignment(f.directive.ID)); len(errs) != 0 {
		t.Fatalf("direct assignment refused on a directive that needs no plan: %v", errs)
	}
}

// Only the issue is gated: an existing assignment can still be edited and
// moved through its workflow even if its plan is not (or no longer) approved.
func TestPlanGateIgnoresAnExistingAssignment(t *testing.T) {
	f := newEOPlanGateFixture(true)
	f.withPlan(false)
	asg := f.newAssignment(f.plan.ID)
	f.read.records[asg.ID] = *asg
	if errs := eoPlanGateRun(t, f, asg); len(errs) != 0 {
		t.Fatalf("existing assignment refused on save: %v", errs)
	}
}

func TestPlanGateIgnoresARecordWithNoParent(t *testing.T) {
	f := newEOPlanGateFixture(true)
	if errs := eoPlanGateRun(t, f, &models.EORecord{ID: uuid.New()}); len(errs) != 0 {
		t.Fatalf("record with no parent refused: %v", errs)
	}
}

func TestPlanGateRefusesABadConfig(t *testing.T) {
	c := NewEOPlanGateCapability()
	for _, cfg := range []string{``, `{}`, `{"plan_type_key":"x"}`, `{"plan_type_key":"x","approved_states":[" "]}`,
		`{"plan_type_key":"x","approved_states":["approved"],"stray":1}`} {
		if _, err := c.Validate(context.Background(), &eoPlanGateReader{}, &models.EORecord{}, datatypes.JSON(cfg)); err == nil {
			t.Errorf("config %q accepted", cfg)
		}
	}
}
