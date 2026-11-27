package services

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/datatypes"
)

var eoPlanRouteCfg = datatypes.JSON(`{"plan_type_key":"eo_execution_plan","approved_states":["approved"],` +
	`"plan_required_key":"plan_required","plan_transitions":["require_plan"],` +
	`"direct_transitions":["assign_directly"],"approve_transitions":["plan_approved"]}`)

func eoPlanRouteDirective(f *eoPlanGateFixture, planRequired bool) *models.EORecord {
	vals, _ := json.Marshal(map[string]any{"plan_required": planRequired})
	d := f.directive
	d.Values = datatypes.JSON(vals)
	return &d
}

func eoPlanRouteRun(t *testing.T, f *eoPlanGateFixture, rec *models.EORecord, code string) map[string]string {
	t.Helper()
	err := NewEOPlanRouteCapability().OnTransition(context.Background(), f.read, rec,
		&models.WorkflowTransition{Code: code}, eoPlanRouteCfg)
	if err == nil {
		return nil
	}
	var invalid *EOValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("%s: want a validation refusal, got %v", code, err)
	}
	return invalid.Fields
}

// Issue 3: the route follows plan_required.
func TestPlanRouteRequirePlanOnlyWhenAPlanIsRequired(t *testing.T) {
	f := newEOPlanGateFixture(true)
	if errs := eoPlanRouteRun(t, f, eoPlanRouteDirective(f, true), "require_plan"); errs != nil {
		t.Fatalf("require_plan refused on a directive that needs a plan: %v", errs)
	}
	if errs := eoPlanRouteRun(t, f, eoPlanRouteDirective(f, false), "require_plan"); errs["plan_required"] == "" {
		t.Fatalf("require_plan ran on a directive that needs no plan: %v", errs)
	}
}

func TestPlanRouteAssignDirectlyOnlyWhenNoPlanIsRequired(t *testing.T) {
	f := newEOPlanGateFixture(false)
	if errs := eoPlanRouteRun(t, f, eoPlanRouteDirective(f, false), "assign_directly"); errs != nil {
		t.Fatalf("assign_directly refused on a directive that needs no plan: %v", errs)
	}
	if errs := eoPlanRouteRun(t, f, eoPlanRouteDirective(f, true), "assign_directly"); errs["plan_required"] == "" {
		t.Fatalf("assign_directly ran on a directive that needs a plan: %v", errs)
	}
}

// Issue 2: plan_approved needs an approved plan.
func TestPlanRoutePlanApprovedRefusedWithNoPlan(t *testing.T) {
	f := newEOPlanGateFixture(true)
	if errs := eoPlanRouteRun(t, f, eoPlanRouteDirective(f, true), "plan_approved"); errs[eoPlanRouteApproveErrorKey] == "" {
		t.Fatalf("plan_approved ran on a directive with no plan: %v", errs)
	}
}

func TestPlanRoutePlanApprovedRefusedWithADraftPlan(t *testing.T) {
	f := newEOPlanGateFixture(true)
	f.withPlan(false)
	if errs := eoPlanRouteRun(t, f, eoPlanRouteDirective(f, true), "plan_approved"); errs[eoPlanRouteApproveErrorKey] == "" {
		t.Fatalf("plan_approved ran while the plan is in drafting: %v", errs)
	}
}

func TestPlanRoutePlanApprovedRunsWithAnApprovedPlan(t *testing.T) {
	f := newEOPlanGateFixture(true)
	f.withPlan(true)
	if errs := eoPlanRouteRun(t, f, eoPlanRouteDirective(f, true), "plan_approved"); errs != nil {
		t.Fatalf("plan_approved refused with an approved plan: %v", errs)
	}
}

func TestPlanRouteIgnoresOtherTransitions(t *testing.T) {
	f := newEOPlanGateFixture(true)
	if errs := eoPlanRouteRun(t, f, eoPlanRouteDirective(f, false), "close"); errs != nil {
		t.Fatalf("an unrelated transition was refused: %v", errs)
	}
}

func TestPlanRouteRefusesABadConfig(t *testing.T) {
	c := NewEOPlanRouteCapability()
	for _, cfg := range []string{``, `{}`,
		`{"plan_type_key":"x","plan_required_key":"p"}`,
		`{"plan_type_key":"x","plan_required_key":"p","plan_transitions":["a"],"direct_transitions":["a"]}`,
		`{"plan_type_key":"x","plan_required_key":"p","approve_transitions":["a"]}`,
		`{"plan_type_key":"x","plan_required_key":"p","plan_transitions":["a"],"stray":1}`} {
		if _, err := c.Validate(context.Background(), &eoPlanGateReader{}, &models.EORecord{}, datatypes.JSON(cfg)); err == nil {
			t.Errorf("config %q accepted", cfg)
		}
	}
}

// A workflow without some of the codes is accepted: those rules never apply.
func TestPlanRouteValidateWorkflowToleratesMissingTransitions(t *testing.T) {
	c := NewEOPlanRouteCapability()
	wf := &models.Workflow{Code: "custom", Transitions: []models.WorkflowTransition{{ID: uuid.New(), Code: "close"}}}
	if err := c.ValidateWorkflow(eoPlanRouteCfg, wf); err != nil {
		t.Fatalf("a workflow without the plan transitions refused: %v", err)
	}
	if err := c.ValidateWorkflow(datatypes.JSON(`{}`), wf); err == nil {
		t.Fatal("a bad config was accepted")
	}
}
