package database

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Both step keys are PERMANENT identifiers; see the comment on eoPilotSteps.
// Never rename or reuse them.
const (
	eoPlanApprovalAtStateStepKey      = "plan_approval_required_at_approved"
	eoEnableDirectivePlanRouteStepKey = "enable_directive_plan_route"
)

// eoDirectivePlanRouteConfig is plan_route's config on the Directive. It is
// shared by the registry entry (fresh databases) and the one-shot step
// (existing ones), so the two cannot write different configurations.
const eoDirectivePlanRouteConfig = `{"plan_type_key":"` + eoExecutionPlanTypeKey + `",` +
	`"approved_states":["approved"],` +
	`"plan_required_key":"plan_required",` +
	`"plan_transitions":["require_plan"],` +
	`"direct_transitions":["assign_directly"],` +
	`"approve_transitions":["plan_approved"]}`

// The plan's approval record and the step that makes it.
const (
	eoPlanApprovedStateCode     = "approved"
	eoPlanApproveTransitionCode = "approve"
)

var eoPlanApprovalFieldKeys = []string{"approved_by", "approval_date"}

// eoPlanApprovalAtState fixes an Execution Plan that could not be drafted.
//
// THE DEFECT. `approved_by` and `approval_date` were seeded required ALWAYS,
// so a plan in `drafting` — not yet approved by anyone — could not be saved
// without inventing who approved it and when. The record of approval belongs
// to the approval (EO-DIR 2.2; the `approve` transition's own why says
// "`approved_by` and `approval_date` are the record of it").
//
// THE RULING. Both are required AT the `approved` state: not asked while the
// plan is drafted, demanded by the `approve` transition (which validates
// against the state it enters), and kept required while the plan stays
// approved. The `approve` transition DECLARES both as field changes, because
// a transition may write only the keys it declares; IsRequired is false on
// both, as on the closure artefacts, so the state requirement is the one rule.
//
// ONE-SHOT. A field that is no longer `always` was changed by an
// administrator and is left alone. Existing plans are not rewritten: one in
// `approved` already carries both values, since they were required from the
// start.
func eoPlanApprovalAtState(tx *gorm.DB) (bool, error) {
	var ty models.EORecordType
	if err := tx.Where("key = ?", eoExecutionPlanTypeKey).Limit(1).Find(&ty).Error; err != nil {
		return false, fmt.Errorf("find record type %q: %w", eoExecutionPlanTypeKey, err)
	}
	if ty.ID == uuid.Nil {
		return false, fmt.Errorf("record type %q is not seeded", eoExecutionPlanTypeKey)
	}

	code := eoWorkflowCode(eoExecutionPlanTypeKey)
	var wf models.Workflow
	if err := tx.Where("code = ?", code).Limit(1).Find(&wf).Error; err != nil {
		return false, fmt.Errorf("find workflow %q: %w", code, err)
	}
	if wf.ID == uuid.Nil {
		return false, fmt.Errorf("workflow %q is not seeded", code)
	}
	var states []models.WorkflowState
	if err := tx.Where("workflow_id = ? AND code = ?", wf.ID, eoPlanApprovedStateCode).Find(&states).Error; err != nil {
		return false, fmt.Errorf("read state %q of workflow %q: %w", eoPlanApprovedStateCode, code, err)
	}
	if len(states) != 1 {
		return false, fmt.Errorf("workflow %q has %d states coded %q, want exactly 1", code, len(states), eoPlanApprovedStateCode)
	}
	if states[0].StateType == eoStateTerminal {
		return false, fmt.Errorf("state %q of workflow %q is terminal; a field required there would make approved plans uneditable", eoPlanApprovedStateCode, code)
	}
	var transition models.WorkflowTransition
	if err := tx.Where("workflow_id = ? AND code = ? AND to_state_id = ?", wf.ID, eoPlanApproveTransitionCode, states[0].ID).
		Limit(1).Find(&transition).Error; err != nil {
		return false, fmt.Errorf("read transition %q of workflow %q: %w", eoPlanApproveTransitionCode, code, err)
	}
	if transition.ID == uuid.Nil {
		return false, fmt.Errorf("workflow %q has no transition %q into %q, so the approval record could never be supplied", code, eoPlanApproveTransitionCode, eoPlanApprovedStateCode)
	}
	ids, err := json.Marshal([]uuid.UUID{states[0].ID})
	if err != nil {
		return false, fmt.Errorf("encode state %q: %w", eoPlanApprovedStateCode, err)
	}

	for i, key := range eoPlanApprovalFieldKeys {
		var def models.EOFieldDef
		if err := tx.Where("record_type_id = ? AND key = ?", ty.ID, key).Limit(1).Find(&def).Error; err != nil {
			return false, fmt.Errorf("read field %s.%s: %w", eoExecutionPlanTypeKey, key, err)
		}
		if def.ID == uuid.Nil {
			return false, fmt.Errorf("%s has no field %q", eoExecutionPlanTypeKey, key)
		}

		if def.RequiredMode == models.EORequiredAlways {
			if err := tx.Model(&models.EOFieldDef{}).Where("id = ?", def.ID).
				Updates(map[string]any{
					"required_mode":         models.EORequiredAtState,
					"required_at_state_ids": datatypes.JSON(ids),
				}).Error; err != nil {
				return false, fmt.Errorf("move %s.%s to state %q: %w", eoExecutionPlanTypeKey, key, eoPlanApprovedStateCode, err)
			}
			log.Printf("EO pilot: %s.%s is now required at state %q, not at creation", eoExecutionPlanTypeKey, key, eoPlanApprovedStateCode)
		} else {
			log.Printf("EO pilot: %s.%s is %q, not `always`; its requiredness is left as it is", eoExecutionPlanTypeKey, key, def.RequiredMode)
		}

		var n int64
		if err := tx.Model(&models.TransitionFieldChange{}).
			Where("transition_id = ? AND field_name = ?", transition.ID, key).Count(&n).Error; err != nil {
			return false, fmt.Errorf("check field change %q of transition %q: %w", key, eoPlanApproveTransitionCode, err)
		}
		if n > 0 {
			continue
		}
		if err := tx.Create(&models.TransitionFieldChange{
			ID:           uuid.New(),
			TransitionID: transition.ID,
			FieldName:    key,
			Label:        def.Label,
			LabelAr:      def.LabelAr,
			IsRequired:   false,
			SortOrder:    i + 1,
		}).Error; err != nil {
			return false, fmt.Errorf("declare field change %q on transition %q: %w", key, eoPlanApproveTransitionCode, err)
		}
	}
	return true, nil
}

// eoEnableDirectivePlanRoute appends plan_route to the Directive's
// capabilities on a database whose Directive was configured before the
// registry entry existed: eoEnableCapabilities configures a type once and
// never again, so that the designer's edits survive a restart.
//
// ONE-SHOT, and it appends only. A Directive that already enables plan_route
// — a fresh database, where eoEnableCapabilities wrote it, or an
// administrator's choice — is left as it is, config included.
func eoEnableDirectivePlanRoute(tx *gorm.DB) (bool, error) {
	var ty models.EORecordType
	if err := tx.Where("key = ?", eoDirectiveTypeKey).Limit(1).Find(&ty).Error; err != nil {
		return false, fmt.Errorf("find record type %q: %w", eoDirectiveTypeKey, err)
	}
	if ty.ID == uuid.Nil {
		return false, fmt.Errorf("record type %q is not seeded", eoDirectiveTypeKey)
	}
	var caps []map[string]json.RawMessage
	if len(ty.Capabilities) > 0 {
		if err := json.Unmarshal(ty.Capabilities, &caps); err != nil {
			return false, fmt.Errorf("%s capabilities %s do not decode: %w", eoDirectiveTypeKey, ty.Capabilities, err)
		}
	}
	for _, c := range caps {
		var key string
		if err := json.Unmarshal(c["key"], &key); err == nil && key == eoPlanRouteCapabilityKey {
			log.Printf("EO pilot: %s already enables %s; left as it is", eoDirectiveTypeKey, eoPlanRouteCapabilityKey)
			return true, nil
		}
	}
	keyJSON, _ := json.Marshal(eoPlanRouteCapabilityKey)
	caps = append(caps, map[string]json.RawMessage{
		"key":    keyJSON,
		"config": json.RawMessage(eoDirectivePlanRouteConfig),
	})
	blob, err := json.Marshal(caps)
	if err != nil {
		return false, fmt.Errorf("encode %s capabilities: %w", eoDirectiveTypeKey, err)
	}
	if err := tx.Model(&models.EORecordType{}).Where("id = ?", ty.ID).
		Update("capabilities", datatypes.JSON(blob)).Error; err != nil {
		return false, fmt.Errorf("enable %s on %s: %w", eoPlanRouteCapabilityKey, eoDirectiveTypeKey, err)
	}
	log.Printf("EO pilot: %s now enables %s: its route follows plan_required and plan_approved needs an approved plan", eoDirectiveTypeKey, eoPlanRouteCapabilityKey)
	return true, nil
}

// eoIsPlanApprovalAtState reports a field eoPlanApprovalAtState moves from
// `always` to `at_state`, so the matrix checks know it is a ruling and not drift.
func eoIsPlanApprovalAtState(typeKey, fieldKey string) bool {
	if typeKey != eoExecutionPlanTypeKey {
		return false
	}
	for _, k := range eoPlanApprovalFieldKeys {
		if k == fieldKey {
			return true
		}
	}
	return false
}
