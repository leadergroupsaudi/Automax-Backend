package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/automax/backend/internal/models"

	"gorm.io/datatypes"
)

// EOPlanGateKey is the registry key of the plan-gate capability.
const EOPlanGateKey = "plan_gate"


type eoPlanGateCapability struct {
	eoReservesNoKeys
	eoConstrainsNoWorkflow
}

type eoPlanGateConfig struct {
	PlanTypeKey     string   `json:"plan_type_key"`
	ApprovedStates  []string `json:"approved_states"`
	PlanRequiredKey string   `json:"plan_required_key"`
}

// The field the refusal is reported on: the create request's parent.
const eoPlanGateErrorKey = "parent_record_id"

// NewEOPlanGateCapability returns the plan-gate capability.
func NewEOPlanGateCapability() Capability { return &eoPlanGateCapability{} }

func (c *eoPlanGateCapability) Key() string { return EOPlanGateKey }

func (c *eoPlanGateCapability) parse(cfg datatypes.JSON) (eoPlanGateConfig, error) {
	var out eoPlanGateConfig
	if len(bytes.TrimSpace(cfg)) == 0 {
		return out, fmt.Errorf("plan_gate: config is required (plan_type_key, approved_states)")
	}
	dec := json.NewDecoder(bytes.NewReader(cfg))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return out, fmt.Errorf("plan_gate: bad config: %w", err)
	}
	out.PlanTypeKey = strings.TrimSpace(out.PlanTypeKey)
	out.PlanRequiredKey = strings.TrimSpace(out.PlanRequiredKey)
	if out.PlanTypeKey == "" {
		return out, fmt.Errorf("plan_gate: config names no plan_type_key")
	}
	states := make([]string, 0, len(out.ApprovedStates))
	for _, s := range out.ApprovedStates {
		s = strings.TrimSpace(s)
		if s == "" {
			return out, fmt.Errorf("plan_gate: approved_states holds an empty state code")
		}
		states = append(states, s)
	}
	if len(states) == 0 {
		return out, fmt.Errorf("plan_gate: config names no approved_states, so nothing could ever be issued")
	}
	out.ApprovedStates = states
	return out, nil
}

func (cfg eoPlanGateConfig) approved(code string) bool {
	for _, s := range cfg.ApprovedStates {
		if s == code {
			return true
		}
	}
	return false
}

// eoTruthy reads a yes_no value as it may arrive: a bool from JSON, or a
// string or number from a looser client.
func eoTruthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true", "yes", "1":
			return true
		}
	case float64:
		return x != 0
	case int:
		return x != 0
	}
	return false
}

func (c *eoPlanGateCapability) Validate(ctx context.Context, read EOCapabilityReader, rec *models.EORecord, cfg datatypes.JSON) (map[string]string, error) {
	conf, err := c.parse(cfg)
	if err != nil {
		return nil, err
	}
	if rec.ParentRecordID == nil {
		return nil, nil
	}
	// Only the issue is gated: an existing record is being edited or moved.
	stored, err := read.Record(ctx, rec.ID)
	if err != nil {
		return nil, fmt.Errorf("plan_gate: load stored record: %w", err)
	}
	if stored != nil {
		return nil, nil
	}
	parent, err := read.Record(ctx, *rec.ParentRecordID)
	if err != nil {
		return nil, fmt.Errorf("plan_gate: load parent: %w", err)
	}
	if parent == nil {
		return nil, nil // the create pipeline refuses a missing parent itself
	}
	planType, err := read.RecordType(ctx, conf.PlanTypeKey)
	if err != nil {
		return nil, fmt.Errorf("plan_gate: %w", err)
	}
	if planType == nil {
		return nil, fmt.Errorf("plan_gate: plan_type_key %q names no record type", conf.PlanTypeKey)
	}

	// Issued under the plan itself.
	if parent.RecordTypeID == planType.ID {
		code, err := read.StateCode(ctx, parent.StateID)
		if err != nil {
			return nil, fmt.Errorf("plan_gate: read plan state: %w", err)
		}
		if conf.approved(code) {
			return nil, nil
		}
		return map[string]string{eoPlanGateErrorKey: fmt.Sprintf(
			"execution plan %s is not approved yet; assignments can be issued once it is approved", parent.Reference)}, nil
	}

	// Issued under the directive: its plan, if it has one, must be approved.
	plans, err := read.Children(ctx, parent.ID, conf.PlanTypeKey)
	if err != nil {
		return nil, fmt.Errorf("plan_gate: read plans: %w", err)
	}
	for i := range plans {
		code, err := read.StateCode(ctx, plans[i].StateID)
		if err != nil {
			return nil, fmt.Errorf("plan_gate: read plan state: %w", err)
		}
		if conf.approved(code) {
			return nil, nil
		}
	}
	if len(plans) > 0 {
		return map[string]string{eoPlanGateErrorKey: fmt.Sprintf(
			"the execution plan of %s is not approved yet; assignments can be issued once it is approved", parent.Reference)}, nil
	}
	if conf.PlanRequiredKey != "" && eoTruthy(eoFlattenStored(parent)[conf.PlanRequiredKey]) {
		return map[string]string{eoPlanGateErrorKey: fmt.Sprintf(
			"%s requires an execution plan; prepare it and have it approved before issuing assignments", parent.Reference)}, nil
	}
	return nil, nil
}

func (c *eoPlanGateCapability) Derive(ctx context.Context, read EOCapabilityReader, rec *models.EORecord, cfg datatypes.JSON) (map[string]any, error) {
	if _, err := c.parse(cfg); err != nil {
		return nil, err
	}
	return nil, nil
}

func (c *eoPlanGateCapability) OnTransition(ctx context.Context, read EOCapabilityReader, rec *models.EORecord, t *models.WorkflowTransition, cfg datatypes.JSON) error {
	return nil
}

func (c *eoPlanGateCapability) Timers(ctx context.Context, read EOCapabilityReader, rec *models.EORecord, cfg datatypes.JSON) ([]models.EOTimer, error) {
	return nil, nil
}
