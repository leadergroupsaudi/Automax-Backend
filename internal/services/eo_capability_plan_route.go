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

// EOPlanRouteKey is the registry key of the plan-route capability.
const EOPlanRouteKey = "plan_route"

// The plan-route capability keeps a Directive's workflow on the route its own
// Q1 answer (`plan_required`) chose, and keeps the plan branch honest:
//
//   - a transition in PlanTransitions (require_plan) runs only when the
//     directive needs a plan;
//   - a transition in DirectTransitions (assign_directly) runs only when it
//     does not;
//   - a transition in ApproveTransitions (plan_approved) runs only when the
//     directive has an Execution Plan in one of ApprovedStates.
//
// It refuses in OnTransition, which runs inside the transition's transaction
// and rolls it back, with a field error the transition modal shows.
type eoPlanRouteCapability struct {
	eoReservesNoKeys
}

type eoPlanRouteConfig struct {
	PlanTypeKey        string   `json:"plan_type_key"`
	ApprovedStates     []string `json:"approved_states"`
	PlanRequiredKey    string   `json:"plan_required_key"`
	PlanTransitions    []string `json:"plan_transitions"`
	DirectTransitions  []string `json:"direct_transitions"`
	ApproveTransitions []string `json:"approve_transitions"`
}

// The field a refusal about an unapproved plan is reported on. No field
// declares it; the client shows it in the refusal summary.
const eoPlanRouteApproveErrorKey = "execution_plan"

// NewEOPlanRouteCapability returns the plan-route capability.
func NewEOPlanRouteCapability() Capability { return &eoPlanRouteCapability{} }

func (c *eoPlanRouteCapability) Key() string { return EOPlanRouteKey }

func eoPlanRouteCodes(name string, in []string, seen map[string]string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, fmt.Errorf("plan_route: %s holds an empty transition code", name)
		}
		if prev, dup := seen[s]; dup {
			return nil, fmt.Errorf("plan_route: transition %q is named in both %s and %s", s, prev, name)
		}
		seen[s] = name
		out = append(out, s)
	}
	return out, nil
}

func (c *eoPlanRouteCapability) parse(cfg datatypes.JSON) (eoPlanRouteConfig, error) {
	var out eoPlanRouteConfig
	if len(bytes.TrimSpace(cfg)) == 0 {
		return out, fmt.Errorf("plan_route: config is required (plan_type_key, approved_states, plan_required_key and the transition lists)")
	}
	dec := json.NewDecoder(bytes.NewReader(cfg))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		return out, fmt.Errorf("plan_route: bad config: %w", err)
	}
	out.PlanTypeKey = strings.TrimSpace(out.PlanTypeKey)
	out.PlanRequiredKey = strings.TrimSpace(out.PlanRequiredKey)
	if out.PlanTypeKey == "" {
		return out, fmt.Errorf("plan_route: config names no plan_type_key")
	}
	if out.PlanRequiredKey == "" {
		return out, fmt.Errorf("plan_route: config names no plan_required_key")
	}
	states := make([]string, 0, len(out.ApprovedStates))
	for _, s := range out.ApprovedStates {
		s = strings.TrimSpace(s)
		if s == "" {
			return out, fmt.Errorf("plan_route: approved_states holds an empty state code")
		}
		states = append(states, s)
	}
	out.ApprovedStates = states

	seen := map[string]string{}
	var err error
	if out.PlanTransitions, err = eoPlanRouteCodes("plan_transitions", out.PlanTransitions, seen); err != nil {
		return out, err
	}
	if out.DirectTransitions, err = eoPlanRouteCodes("direct_transitions", out.DirectTransitions, seen); err != nil {
		return out, err
	}
	if out.ApproveTransitions, err = eoPlanRouteCodes("approve_transitions", out.ApproveTransitions, seen); err != nil {
		return out, err
	}
	if len(seen) == 0 {
		return out, fmt.Errorf("plan_route: config names no transitions, so it would never refuse anything")
	}
	if len(out.ApproveTransitions) > 0 && len(out.ApprovedStates) == 0 {
		return out, fmt.Errorf("plan_route: approve_transitions are named but approved_states is empty, so they could never run")
	}
	return out, nil
}

func eoPlanRouteHas(list []string, code string) bool {
	for _, s := range list {
		if s == code {
			return true
		}
	}
	return false
}

func (c *eoPlanRouteCapability) OnTransition(ctx context.Context, read EOCapabilityReader, rec *models.EORecord, t *models.WorkflowTransition, cfg datatypes.JSON) error {
	conf, err := c.parse(cfg)
	if err != nil {
		return err
	}
	if t == nil {
		return nil
	}
	needsPlan := eoTruthy(eoFlattenStored(rec)[conf.PlanRequiredKey])

	switch {
	case eoPlanRouteHas(conf.PlanTransitions, t.Code):
		if !needsPlan {
			return eoValidationError(map[string]string{conf.PlanRequiredKey: fmt.Sprintf(
				"%s does not require an execution plan; assign it directly instead", rec.Reference)})
		}
	case eoPlanRouteHas(conf.DirectTransitions, t.Code):
		if needsPlan {
			return eoValidationError(map[string]string{conf.PlanRequiredKey: fmt.Sprintf(
				"%s requires an execution plan; it cannot be assigned directly", rec.Reference)})
		}
	case eoPlanRouteHas(conf.ApproveTransitions, t.Code):
		plans, err := read.Children(ctx, rec.ID, conf.PlanTypeKey)
		if err != nil {
			return fmt.Errorf("plan_route: read plans: %w", err)
		}
		gate := eoPlanGateConfig{ApprovedStates: conf.ApprovedStates}
		for i := range plans {
			code, err := read.StateCode(ctx, plans[i].StateID)
			if err != nil {
				return fmt.Errorf("plan_route: read plan state: %w", err)
			}
			if gate.approved(code) {
				return nil
			}
		}
		if len(plans) == 0 {
			return eoValidationError(map[string]string{eoPlanRouteApproveErrorKey: fmt.Sprintf(
				"%s has no execution plan yet; prepare it and approve it first", rec.Reference)})
		}
		return eoValidationError(map[string]string{eoPlanRouteApproveErrorKey: fmt.Sprintf(
			"the execution plan of %s is not approved yet; approve the plan first", rec.Reference)})
	}
	return nil
}

// ValidateWorkflow checks only the config itself. A transition code the
// workflow does not have is NOT refused: an administrator may bind the
// Directive to a workflow without, say, the plan branch, and refusing would
// stop every transition of the type. Such a code never matches, so its rule
// simply does not apply. The seeded configuration is held to the seeded
// workflow by database.TestEoDirectivePlanRouteNamesSeededTransitions.
func (c *eoPlanRouteCapability) ValidateWorkflow(cfg datatypes.JSON, wf *models.Workflow) error {
	_, err := c.parse(cfg)
	return err
}

func (c *eoPlanRouteCapability) Validate(ctx context.Context, read EOCapabilityReader, rec *models.EORecord, cfg datatypes.JSON) (map[string]string, error) {
	if _, err := c.parse(cfg); err != nil {
		return nil, err
	}
	return nil, nil
}

func (c *eoPlanRouteCapability) Derive(ctx context.Context, read EOCapabilityReader, rec *models.EORecord, cfg datatypes.JSON) (map[string]any, error) {
	if _, err := c.parse(cfg); err != nil {
		return nil, err
	}
	return nil, nil
}

func (c *eoPlanRouteCapability) Timers(ctx context.Context, read EOCapabilityReader, rec *models.EORecord, cfg datatypes.JSON) ([]models.EOTimer, error) {
	return nil, nil
}
