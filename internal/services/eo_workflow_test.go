package services

import (
	"context"
	"testing"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/google/uuid"
)

type eoWorkflowFakeRepo struct {
	repository.WorkflowRepository
	workflows []models.Workflow
}

func (f *eoWorkflowFakeRepo) List(_ context.Context, _ bool) ([]models.Workflow, error) {
	return f.workflows, nil
}

func eoWfBound(code string, typeID *uuid.UUID, recordType string) models.Workflow {
	return models.Workflow{ID: uuid.New(), Code: code, RecordType: recordType, RecordTypeID: typeID}
}

func TestResolveWorkflowIgnoresRecordTypeAllWorkflows(t *testing.T) {
	typeID := uuid.New()
	t.Run("returns the bound workflow, not the all workflow", func(t *testing.T) {
		repo := &eoWorkflowFakeRepo{workflows: []models.Workflow{
			eoWfBound("incident-all", nil, "all"),
			eoWfBound("eo", &typeID, "incident"),
		}}
		wf, err := ResolveWorkflowForType(context.Background(), typeID, repo)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if wf.Code != "eo" {
			t.Fatalf("got workflow %q, want eo", wf.Code)
		}
	})
	t.Run("only an all workflow present is an error", func(t *testing.T) {
		repo := &eoWorkflowFakeRepo{workflows: []models.Workflow{
			eoWfBound("incident-all", nil, "all"),
			eoWfBound("incident-both", nil, "both"),
		}}
		if wf, err := ResolveWorkflowForType(context.Background(), typeID, repo); err == nil {
			t.Fatalf("expected error, got workflow %q", wf.Code)
		}
	})
}

func TestResolveWorkflowRequiresExactlyOne(t *testing.T) {
	typeID := uuid.New()
	other := uuid.New()
	tests := []struct {
		name    string
		wfs     []models.Workflow
		wantErr bool
	}{
		{"zero", nil, true},
		{"bound to a different type only", []models.Workflow{eoWfBound("a", &other, "")}, true},
		{"one", []models.Workflow{eoWfBound("a", &typeID, ""), eoWfBound("b", &other, "")}, false},
		{"two", []models.Workflow{eoWfBound("a", &typeID, ""), eoWfBound("b", &typeID, "")}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wf, err := ResolveWorkflowForType(context.Background(), typeID, &eoWorkflowFakeRepo{workflows: tc.wfs})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && wf.Code != "a" {
				t.Fatalf("got %q, want a", wf.Code)
			}
		})
	}
}

func TestInitialStateRequiresExactlyOneInitial(t *testing.T) {
	st := func(code, typ string) models.WorkflowState { return models.WorkflowState{Code: code, StateType: typ} }
	tests := []struct {
		name     string
		states   []models.WorkflowState
		wantCode string
		wantErr  bool
	}{
		{"none", []models.WorkflowState{st("a", "normal")}, "", true},
		{"one", []models.WorkflowState{st("a", "normal"), st("b", "initial"), st("c", "terminal")}, "b", false},
		{"two", []models.WorkflowState{st("a", "initial"), st("b", "initial")}, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, err := InitialState(&models.Workflow{Code: "w", States: tc.states})
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && s.Code != tc.wantCode {
				t.Fatalf("got %q, want %q", s.Code, tc.wantCode)
			}
		})
	}
}

func TestIsTerminalMatchesStateType(t *testing.T) {
	tests := []struct {
		name string
		s    *models.WorkflowState
		want bool
	}{
		{"terminal", &models.WorkflowState{StateType: "terminal"}, true},
		{"initial", &models.WorkflowState{StateType: "initial"}, false},
		{"normal", &models.WorkflowState{StateType: "normal"}, false},
		{"empty", &models.WorkflowState{}, false},
		{"nil", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsTerminal(tc.s); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
