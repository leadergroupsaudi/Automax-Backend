package services

import (
	"context"
	"fmt"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/google/uuid"
)

// ResolveWorkflowForType returns the single active workflow bound to an EO
// record type. It matches on RecordTypeID only. It deliberately never reads
// the Workflow.RecordType string: the incident matcher treats 'all' and 'both'
// as wildcards, which would hand an EO type an incident workflow's states.
// Zero or more than one bound workflow is an error; there is no scoring or
// fallback, because is_locked_after and required_at_state_ids need an
// unambiguous state set.
func ResolveWorkflowForType(ctx context.Context, typeID uuid.UUID, repo repository.WorkflowRepository) (*models.Workflow, error) {
	all, err := repo.List(ctx, true)
	if err != nil {
		return nil, fmt.Errorf("list workflows: %w", err)
	}
	var matches []*models.Workflow
	for i := range all {
		if all[i].RecordTypeID != nil && *all[i].RecordTypeID == typeID {
			matches = append(matches, &all[i])
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no workflow is bound to record type %s", typeID)
	case 1:
		return matches[0], nil
	default:
		return nil, fmt.Errorf("record type %s is bound to %d workflows, expected exactly one", typeID, len(matches))
	}
}

// InitialState returns the workflow's one state of type "initial". Zero or
// several is an error.
func InitialState(wf *models.Workflow) (*models.WorkflowState, error) {
	var found *models.WorkflowState
	n := 0
	for i := range wf.States {
		if wf.States[i].StateType == "initial" {
			if found == nil {
				found = &wf.States[i]
			}
			n++
		}
	}
	switch n {
	case 0:
		return nil, fmt.Errorf("workflow %s has no initial state", wf.Code)
	case 1:
		return found, nil
	default:
		return nil, fmt.Errorf("workflow %s has %d initial states, expected exactly one", wf.Code, n)
	}
}

// IsTerminal reports whether a state ends the workflow.
func IsTerminal(s *models.WorkflowState) bool {
	return s != nil && s.StateType == "terminal"
}
