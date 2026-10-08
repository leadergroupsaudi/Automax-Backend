package models

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// WorkflowStackFrame is one suspended caller frame on an incident.
// Frames are stored oldest-first in Incident.WorkflowStack.
type WorkflowStackFrame struct {
	WorkflowID uuid.UUID `json:"workflow_id"`
	StateID    uuid.UUID `json:"state_id"`
}

// PushWorkflowFrame appends a frame (newest last). Nil IDs and a corrupt
// existing stack are rejected so the stored text is left unchanged.
func (i *Incident) PushWorkflowFrame(workflowID, stateID uuid.UUID) error {
	if i == nil {
		return fmt.Errorf("incident is nil")
	}
	if workflowID == uuid.Nil || stateID == uuid.Nil {
		return fmt.Errorf("workflow stack frame requires workflow_id and state_id")
	}
	frames, err := decodeWorkflowStack(i.WorkflowStack)
	if err != nil {
		return err
	}
	frames = append(frames, WorkflowStackFrame{WorkflowID: workflowID, StateID: stateID})
	return i.writeWorkflowStack(frames)
}

// PopWorkflowFrame removes and returns the newest frame.
// ok is false when the stack is empty or the stored text cannot be decoded.
// A failed pop does not rewrite the column.
func (i *Incident) PopWorkflowFrame() (WorkflowStackFrame, bool) {
	if i == nil {
		return WorkflowStackFrame{}, false
	}
	frames, err := decodeWorkflowStack(i.WorkflowStack)
	if err != nil || len(frames) == 0 {
		return WorkflowStackFrame{}, false
	}
	frame := frames[len(frames)-1]
	if err := i.writeWorkflowStack(frames[:len(frames)-1]); err != nil {
		return WorkflowStackFrame{}, false
	}
	return frame, true
}

func (i *Incident) writeWorkflowStack(frames []WorkflowStackFrame) error {
	if frames == nil {
		frames = []WorkflowStackFrame{}
	}
	raw, err := json.Marshal(frames)
	if err != nil {
		return fmt.Errorf("encode workflow stack: %w", err)
	}
	i.WorkflowStack = string(raw)
	return nil
}

func decodeWorkflowStack(raw string) ([]WorkflowStackFrame, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "null" {
		return []WorkflowStackFrame{}, nil
	}
	var frames []WorkflowStackFrame
	if err := json.Unmarshal([]byte(trimmed), &frames); err != nil {
		return nil, fmt.Errorf("decode workflow stack: %w", err)
	}
	if frames == nil {
		frames = []WorkflowStackFrame{}
	}
	return frames, nil
}
