package database

import (
	"encoding/json"
	"testing"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/datatypes"
)

func TestEoPlanApprovalIsRequiredAtApprovedAndDeclaredOnApprove(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	var approved models.WorkflowState
	if err := db.Joins("JOIN workflows w ON w.id = workflow_states.workflow_id").
		Where("w.code = ? AND workflow_states.code = ?", eoWorkflowCode(eoExecutionPlanTypeKey), eoPlanApprovedStateCode).
		Limit(1).Find(&approved).Error; err != nil || approved.ID == uuid.Nil {
		t.Fatalf("the plan's approved state: %v", err)
	}
	var tr models.WorkflowTransition
	if err := db.Where("code = ? AND to_state_id = ?", eoPlanApproveTransitionCode, approved.ID).
		Limit(1).Find(&tr).Error; err != nil || tr.ID == uuid.Nil {
		t.Fatalf("the approve transition: %v", err)
	}
	for _, key := range eoPlanApprovalFieldKeys {
		var def models.EOFieldDef
		if err := db.Joins("JOIN eo_record_types t ON t.id = eo_field_defs.record_type_id").
			Where("t.key = ? AND eo_field_defs.key = ?", eoExecutionPlanTypeKey, key).Limit(1).Find(&def).Error; err != nil {
			t.Fatal(err)
		}
		if def.RequiredMode != models.EORequiredAtState {
			t.Errorf("%s is %q, want at_state", key, def.RequiredMode)
		}
		var ids []uuid.UUID
		_ = json.Unmarshal(def.RequiredAtStateIDs, &ids)
		if len(ids) != 1 || ids[0] != approved.ID {
			t.Errorf("%s is required at %v, want only approved (%v)", key, ids, approved.ID)
		}
		var n int64
		db.Model(&models.TransitionFieldChange{}).Where("transition_id = ? AND field_name = ?", tr.ID, key).Count(&n)
		if n != 1 {
			t.Errorf("approve declares %s %d times, want 1", key, n)
		}
	}
}

func eoDirectiveCapabilityKeys(t *testing.T, raw datatypes.JSON) []string {
	t.Helper()
	var caps []struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(raw, &caps); err != nil {
		t.Fatalf("capabilities %s: %v", raw, err)
	}
	keys := make([]string, 0, len(caps))
	for _, c := range caps {
		keys = append(keys, c.Key)
	}
	return keys
}

// An existing database: the Directive was configured with unique_values
// before plan_route existed. The step appends plan_route and keeps the rest;
// a second run changes nothing.
func TestEoDirectivePlanRouteReachesAConfiguredDirective(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	old := `[{"key":"unique_values","config":{"keys":["subject_ar","subject_en"]}}]`
	if err := db.Model(&models.EORecordType{}).Where("key = ?", eoDirectiveTypeKey).
		Update("capabilities", datatypes.JSON(old)).Error; err != nil {
		t.Fatal(err)
	}
	for run := 1; run <= 2; run++ {
		if _, err := eoEnableDirectivePlanRoute(db); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		var ty models.EORecordType
		db.Where("key = ?", eoDirectiveTypeKey).Limit(1).Find(&ty)
		keys := eoDirectiveCapabilityKeys(t, ty.Capabilities)
		if len(keys) != 2 || keys[0] != eoUniqueValuesCapabilityKey || keys[1] != eoPlanRouteCapabilityKey {
			t.Fatalf("run %d: capabilities %v, want [unique_values plan_route]", run, keys)
		}
	}
}

// plan_route tolerates codes a workflow lacks, so a typo in the seeded config
// would silently never fire. This holds it to the seeded workflow.
func TestEoDirectivePlanRouteNamesSeededTransitions(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)
	var cfg struct {
		Plan    []string `json:"plan_transitions"`
		Direct  []string `json:"direct_transitions"`
		Approve []string `json:"approve_transitions"`
		States  []string `json:"approved_states"`
	}
	if err := json.Unmarshal([]byte(eoDirectivePlanRouteConfig), &cfg); err != nil {
		t.Fatal(err)
	}
	for _, code := range append(append(cfg.Plan, cfg.Direct...), cfg.Approve...) {
		var n int64
		db.Model(&models.WorkflowTransition{}).
			Joins("JOIN workflows w ON w.id = workflow_transitions.workflow_id").
			Where("w.code = ? AND workflow_transitions.code = ?", eoWorkflowCode(eoDirectiveTypeKey), code).Count(&n)
		if n != 1 {
			t.Errorf("plan_route names transition %q; the seeded Directive workflow has %d", code, n)
		}
	}
	for _, code := range cfg.States {
		var n int64
		db.Model(&models.WorkflowState{}).
			Joins("JOIN workflows w ON w.id = workflow_states.workflow_id").
			Where("w.code = ? AND workflow_states.code = ?", eoWorkflowCode(eoExecutionPlanTypeKey), code).Count(&n)
		if n != 1 {
			t.Errorf("plan_route names plan state %q; the seeded plan workflow has %d", code, n)
		}
	}
}
