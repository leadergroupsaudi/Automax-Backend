package models

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestPushThenPopReturnsExactFrame(t *testing.T) {
	incident := &Incident{WorkflowStack: "[]"}
	workflowID := uuid.New()
	stateID := uuid.New()

	if err := incident.PushWorkflowFrame(workflowID, stateID); err != nil {
		t.Fatalf("push: %v", err)
	}
	got, ok := incident.PopWorkflowFrame()
	if !ok {
		t.Fatal("pop: expected ok")
	}
	if got.WorkflowID != workflowID || got.StateID != stateID {
		t.Fatalf("pop = %+v, want workflow %s state %s", got, workflowID, stateID)
	}
	if incident.WorkflowStack != "[]" {
		t.Fatalf("stack after pop = %q, want []", incident.WorkflowStack)
	}
}

func TestWorkflowStackOldestFirstLIFO(t *testing.T) {
	incident := &Incident{}
	first := WorkflowStackFrame{WorkflowID: uuid.New(), StateID: uuid.New()}
	second := WorkflowStackFrame{WorkflowID: uuid.New(), StateID: uuid.New()}

	if err := incident.PushWorkflowFrame(first.WorkflowID, first.StateID); err != nil {
		t.Fatalf("push first: %v", err)
	}
	if err := incident.PushWorkflowFrame(second.WorkflowID, second.StateID); err != nil {
		t.Fatalf("push second: %v", err)
	}

	var stored []WorkflowStackFrame
	if err := json.Unmarshal([]byte(incident.WorkflowStack), &stored); err != nil {
		t.Fatalf("stored json: %v", err)
	}
	if len(stored) != 2 || stored[0] != first || stored[1] != second {
		t.Fatalf("stored order = %+v, want oldest first [%+v %+v]", stored, first, second)
	}

	got, ok := incident.PopWorkflowFrame()
	if !ok || got != second {
		t.Fatalf("first pop = %+v ok=%v, want newest %+v", got, ok, second)
	}
	got, ok = incident.PopWorkflowFrame()
	if !ok || got != first {
		t.Fatalf("second pop = %+v ok=%v, want oldest %+v", got, ok, first)
	}
}

func TestPopEmptyStack(t *testing.T) {
	cases := []string{"", "   ", "[]", "null"}
	for _, raw := range cases {
		incident := &Incident{WorkflowStack: raw}
		got, ok := incident.PopWorkflowFrame()
		if ok || got != (WorkflowStackFrame{}) {
			t.Fatalf("raw %q: got %+v ok=%v, want empty frame and ok=false", raw, got, ok)
		}
		if incident.WorkflowStack != raw {
			t.Fatalf("raw %q was rewritten to %q", raw, incident.WorkflowStack)
		}
	}
}

func TestPopCorruptStackLeavesColumn(t *testing.T) {
	cases := []string{`{"workflow_id":"x"}`, `[`, `["not-a-frame"]`, `{`}
	for _, raw := range cases {
		incident := &Incident{WorkflowStack: raw}
		got, ok := incident.PopWorkflowFrame()
		if ok || got != (WorkflowStackFrame{}) {
			t.Fatalf("raw %q: got %+v ok=%v", raw, got, ok)
		}
		if incident.WorkflowStack != raw {
			t.Fatalf("corrupt stack rewritten from %q to %q", raw, incident.WorkflowStack)
		}
		if err := incident.PushWorkflowFrame(uuid.New(), uuid.New()); err == nil {
			t.Fatalf("push on corrupt stack %q succeeded", raw)
		}
		if incident.WorkflowStack != raw {
			t.Fatalf("failed push rewrote stack from %q to %q", raw, incident.WorkflowStack)
		}
	}
}

func TestPushRejectsNilIDs(t *testing.T) {
	incident := &Incident{WorkflowStack: "[]"}
	valid := uuid.New()
	cases := []struct {
		name       string
		workflowID uuid.UUID
		stateID    uuid.UUID
	}{
		{"nil workflow", uuid.Nil, valid},
		{"nil state", valid, uuid.Nil},
		{"both nil", uuid.Nil, uuid.Nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := incident.PushWorkflowFrame(tc.workflowID, tc.stateID)
			if err == nil {
				t.Fatal("expected error")
			}
			if incident.WorkflowStack != "[]" {
				t.Fatalf("stack = %q, want []", incident.WorkflowStack)
			}
		})
	}
}

func TestPushOnNilIncident(t *testing.T) {
	var incident *Incident
	if err := incident.PushWorkflowFrame(uuid.New(), uuid.New()); err == nil {
		t.Fatal("expected error")
	}
	if _, ok := incident.PopWorkflowFrame(); ok {
		t.Fatal("expected ok=false")
	}
}

func TestPushEncodesWorkflowAndStateIDs(t *testing.T) {
	incident := &Incident{WorkflowStack: "[]"}
	workflowID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	stateID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	if err := incident.PushWorkflowFrame(workflowID, stateID); err != nil {
		t.Fatalf("push: %v", err)
	}
	if !strings.Contains(incident.WorkflowStack, `"workflow_id":"11111111-1111-1111-1111-111111111111"`) {
		t.Fatalf("missing workflow_id in %s", incident.WorkflowStack)
	}
	if !strings.Contains(incident.WorkflowStack, `"state_id":"22222222-2222-2222-2222-222222222222"`) {
		t.Fatalf("missing state_id in %s", incident.WorkflowStack)
	}
}
