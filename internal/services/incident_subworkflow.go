package services

import (
	"errors"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
)

// applySubworkflowTransition runs the AMAX-4122 push or pop branch.
// A transition with neither TargetWorkflowID nor IsReturnTransition returns
// transition.ToStateID and does not change the incident.
// moved is true only when the incident's workflow and stack were updated.
func applySubworkflowTransition(incident *models.Incident, transition *models.WorkflowTransition, initialStates []models.WorkflowState) (uuid.UUID, bool, error) {
	if incident == nil || transition == nil {
		return uuid.Nil, false, errors.New("incident and transition are required")
	}
	if transition.TargetWorkflowID != nil {
		switch len(initialStates) {
		case 0:
			return uuid.Nil, false, errors.New("target workflow has no initial state")
		case 1:
			// one initial state
		default:
			return uuid.Nil, false, errors.New("target workflow has more than one initial state")
		}
		if err := incident.PushWorkflowFrame(incident.WorkflowID, incident.CurrentStateID); err != nil {
			return uuid.Nil, false, err
		}
		incident.WorkflowID = *transition.TargetWorkflowID
		incident.CurrentStateID = initialStates[0].ID
		return initialStates[0].ID, true, nil
	}
	if transition.IsReturnTransition {
		frame, ok := incident.PopWorkflowFrame()
		if !ok {
			return uuid.Nil, false, errors.New("cannot return: workflow stack is empty")
		}
		incident.WorkflowID = frame.WorkflowID
		incident.CurrentStateID = frame.StateID
		return frame.StateID, true, nil
	}
	return transition.ToStateID, false, nil
}
