package services

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
)

func TestPushLandsOnInitialStateAndStacksSuspendedFrame(t *testing.T) {
	callerWorkflow := uuid.New()
	callerState := uuid.New()
	targetWorkflow := uuid.New()
	initialState := uuid.New()
	incident := &models.Incident{WorkflowID: callerWorkflow, CurrentStateID: callerState, WorkflowStack: "[]"}
	transition := &models.WorkflowTransition{TargetWorkflowID: &targetWorkflow, ToStateID: uuid.New()}

	dest, moved, err := applySubworkflowTransition(incident, transition, []models.WorkflowState{{ID: initialState}})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if !moved || dest != initialState {
		t.Fatalf("dest=%s moved=%v, want initial %s", dest, moved, initialState)
	}
	if incident.WorkflowID != targetWorkflow || incident.CurrentStateID != initialState {
		t.Fatalf("incident landed on workflow %s state %s", incident.WorkflowID, incident.CurrentStateID)
	}
	var stack []models.WorkflowStackFrame
	if err := json.Unmarshal([]byte(incident.WorkflowStack), &stack); err != nil {
		t.Fatalf("stack: %v", err)
	}
	if len(stack) != 1 || stack[0].WorkflowID != callerWorkflow || stack[0].StateID != callerState {
		t.Fatalf("stack = %+v", stack)
	}
}

func TestReturnRestoresPoppedFrame(t *testing.T) {
	callerWorkflow := uuid.New()
	callerState := uuid.New()
	targetWorkflow := uuid.New()
	initialState := uuid.New()
	incident := &models.Incident{WorkflowID: callerWorkflow, CurrentStateID: callerState, WorkflowStack: "[]"}
	push := &models.WorkflowTransition{TargetWorkflowID: &targetWorkflow}
	if _, _, err := applySubworkflowTransition(incident, push, []models.WorkflowState{{ID: initialState}}); err != nil {
		t.Fatalf("push: %v", err)
	}

	ret := &models.WorkflowTransition{IsReturnTransition: true, ToStateID: uuid.New()}
	dest, moved, err := applySubworkflowTransition(incident, ret, nil)
	if err != nil {
		t.Fatalf("return: %v", err)
	}
	if !moved || dest != callerState {
		t.Fatalf("dest=%s moved=%v, want %s", dest, moved, callerState)
	}
	if incident.WorkflowID != callerWorkflow || incident.CurrentStateID != callerState {
		t.Fatalf("restored workflow %s state %s", incident.WorkflowID, incident.CurrentStateID)
	}
	if incident.WorkflowStack != "[]" {
		t.Fatalf("stack = %s, want []", incident.WorkflowStack)
	}
}

func TestReturnOnEmptyStackRejected(t *testing.T) {
	incident := &models.Incident{WorkflowID: uuid.New(), CurrentStateID: uuid.New(), WorkflowStack: "[]"}
	transition := &models.WorkflowTransition{IsReturnTransition: true, ToStateID: uuid.New()}
	dest, moved, err := applySubworkflowTransition(incident, transition, nil)
	if err == nil || !strings.Contains(err.Error(), "workflow stack is empty") {
		t.Fatalf("err=%v, want empty-stack error", err)
	}
	if moved || dest != uuid.Nil {
		t.Fatalf("dest=%s moved=%v", dest, moved)
	}
	if incident.WorkflowStack != "[]" {
		t.Fatalf("stack rewritten to %s", incident.WorkflowStack)
	}
}

func TestPushRejectsZeroOrManyInitialStates(t *testing.T) {
	target := uuid.New()
	incident := &models.Incident{WorkflowID: uuid.New(), CurrentStateID: uuid.New(), WorkflowStack: "[]"}
	transition := &models.WorkflowTransition{TargetWorkflowID: &target, ToStateID: uuid.New()}

	if _, _, err := applySubworkflowTransition(incident, transition, nil); err == nil || !strings.Contains(err.Error(), "no initial state") {
		t.Fatalf("zero initial: %v", err)
	}
	if _, _, err := applySubworkflowTransition(incident, transition, []models.WorkflowState{{ID: uuid.New()}, {ID: uuid.New()}}); err == nil || !strings.Contains(err.Error(), "more than one") {
		t.Fatalf("many initial: %v", err)
	}
	if incident.WorkflowStack != "[]" {
		t.Fatalf("failed push rewrote stack to %s", incident.WorkflowStack)
	}
}

func TestNormalTransitionUnchanged(t *testing.T) {
	workflowID := uuid.New()
	stateID := uuid.New()
	toStateID := uuid.New()
	incident := &models.Incident{WorkflowID: workflowID, CurrentStateID: stateID, WorkflowStack: "[]"}
	transition := &models.WorkflowTransition{ToStateID: toStateID}

	dest, moved, err := applySubworkflowTransition(incident, transition, nil)
	if err != nil {
		t.Fatalf("normal: %v", err)
	}
	if moved || dest != toStateID {
		t.Fatalf("dest=%s moved=%v", dest, moved)
	}
	if incident.WorkflowID != workflowID || incident.CurrentStateID != stateID || incident.WorkflowStack != "[]" {
		t.Fatal("normal transition changed the incident")
	}
}
