package models

import (
	"testing"

	"github.com/go-playground/validator/v10"
)

func TestWorkflowRecordTypeValidation(t *testing.T) {
	v := validator.New()
	previouslyValid := []string{"", "incident", "request", "complaint", "query", "evidence", "both", "all"}

	for _, recordType := range previouslyValid {
		req := WorkflowCreateRequest{Name: "Existing type", Code: "existing-type", RecordType: recordType}
		if err := v.Struct(req); err != nil {
			t.Fatalf("record_type %q should still be valid: %v", recordType, err)
		}
	}

	dispatch := WorkflowCreateRequest{Name: "Dispatch workflow", Code: "dispatch-wf", RecordType: "dispatch"}
	if err := v.Struct(dispatch); err != nil {
		t.Fatalf("record_type dispatch should be valid: %v", err)
	}

	updateType := "dispatch"
	update := WorkflowUpdateRequest{RecordType: &updateType}
	if err := v.Struct(update); err != nil {
		t.Fatalf("update record_type dispatch should be valid: %v", err)
	}

	imported := WorkflowExportContent{Name: "Imported", Code: "imported", RecordType: "dispatch"}
	if err := v.Struct(imported); err != nil {
		t.Fatalf("import record_type dispatch should be valid: %v", err)
	}

	bogus := WorkflowCreateRequest{Name: "Bad type", Code: "bad-type", RecordType: "bogus"}
	if err := v.Struct(bogus); err == nil {
		t.Fatal("record_type bogus should be rejected")
	}
}
