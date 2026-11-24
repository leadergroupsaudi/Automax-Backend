package database

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/automax/backend/internal/models"
)

// eoOwnWorkflowStateIDs returns the state ids of the workflow a record type is
// bound to, keyed by id to the state's code.
func eoOwnWorkflowStateIDs(t *testing.T, db *gorm.DB, typeID uuid.UUID) map[uuid.UUID]string {
	t.Helper()
	var states []models.WorkflowState
	if err := db.Raw(`
		SELECT s.* FROM workflow_states s
		JOIN eo_record_types t ON t.workflow_id = s.workflow_id
		WHERE t.id = ?`, typeID).Scan(&states).Error; err != nil {
		t.Fatalf("read the states of type %s's workflow: %v", typeID, err)
	}
	out := make(map[uuid.UUID]string, len(states))
	for _, s := range states {
		out[s.ID] = s.Code
	}
	return out
}

func eoDecodeStateIDs(t *testing.T, label string, raw []byte) []uuid.UUID {
	t.Helper()
	s := strings.TrimSpace(string(raw))
	if s == "" || s == "null" {
		return nil
	}
	var ids []uuid.UUID
	if err := json.Unmarshal(raw, &ids); err != nil {
		t.Fatalf("%s required_at_state_ids %q: %v", label, s, err)
	}
	return ids
}

// TestSeedEOPilot_RequiredAtStateIDsBelongToOwnWorkflow checks EVERY field on
// EVERY type, not only Task B6's four. A state id from another type's workflow
// is a rule that can never fire (the engine compares ids to the target state of
// a transition on THIS type's machine) and reads as configured, which is the
// failure this guards. Fields that name no state are not its business; fields
// that name one must name states of their own type's workflow.
//
// Mutation: point any field's required_at_state_ids at a state of another
// type's workflow (e.g. in eoMoveFileRequirementToState, look the state up
// under the wrong type key).
func TestSeedEOPilot_RequiredAtStateIDsBelongToOwnWorkflow(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	var rows []struct {
		TypeID   uuid.UUID
		TypeKey  string
		Key      string
		Mode     string
		StateIDs []byte
	}
	if err := db.Raw(`
		SELECT t.id AS type_id, t.key AS type_key, f.key AS key, f.required_mode AS mode,
		       f.required_at_state_ids AS state_ids
		FROM eo_field_defs f JOIN eo_record_types t ON t.id = f.record_type_id AND t.deleted_at IS NULL
		WHERE f.deleted_at IS NULL ORDER BY t.key, f.key`).Scan(&rows).Error; err != nil {
		t.Fatalf("read every field: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("no fields read; this test would prove nothing")
	}
	checked := 0
	for _, r := range rows {
		ids := eoDecodeStateIDs(t, r.TypeKey+"."+r.Key, r.StateIDs)
		if len(ids) == 0 {
			continue
		}
		checked++
		own := eoOwnWorkflowStateIDs(t, db, r.TypeID)
		for _, id := range ids {
			if _, ok := own[id]; !ok {
				t.Errorf("%s.%s (mode %q) names state %s, which is not a state of %s's own workflow", r.TypeKey, r.Key, r.Mode, id, r.TypeKey)
			}
		}
	}
	// 2 closure files + the closure artefacts + the four moved ones: far more
	// than the four, so a regression that empties the lists is not a pass.
	if checked < len(eoFileFieldsMovedToState) {
		t.Errorf("only %d fields name a state; at least the %d moved file fields must", checked, len(eoFileFieldsMovedToState))
	}
}

// TestSeedEOPilot_CreateTimeFileFieldsAreRequiredAtAState is the POSITIVE
// assertion. "No file field is `always`" is true before the change as well
// (all ten are `never`), so it is kept only as a guard.
//
// Mutation: delete the eoMoveFileRequirementToStateStepKey entry from
// eoPilotSteps, or change a StateCode in eoFileFieldsMovedToState.
func TestSeedEOPilot_CreateTimeFileFieldsAreRequiredAtAState(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	want := map[string][]string{
		"eo_incoming_correspondence.attachments": {"analysis"},
		"eo_draft_decision_study.draft_file":     {"submitted"},
		"eo_draft_decision_study.study_report":   {"with_he"},
	}
	if len(eoFileFieldsMovedToState) != len(want) {
		t.Fatalf("registry has %d entries, this test expects %d", len(eoFileFieldsMovedToState), len(want))
	}
	for name, codes := range want {
		parts := strings.SplitN(name, ".", 2)
		t.Run(name, func(t *testing.T) {
			def := eoEffectiveField(t, db, parts[0], parts[1])
			if def.RequiredMode != models.EORequiredAtState {
				t.Fatalf("required_mode = %q, want at_state", def.RequiredMode)
			}
			ids := eoDecodeStateIDs(t, name, def.RequiredAtStateIDs)
			if len(ids) == 0 {
				t.Fatal("required_at_state_ids is empty: at_state with no states is a rule that never fires")
			}
			ty := eoRootType(t, db, parts[0])
			own := eoOwnWorkflowStateIDs(t, db, ty.ID)
			got := []string{}
			for _, id := range ids {
				code, ok := own[id]
				if !ok {
					t.Fatalf("state %s is not a state of %s's own workflow", id, parts[0])
				}
				got = append(got, code)
			}
			if strings.Join(got, ",") != strings.Join(codes, ",") {
				t.Errorf("required at %v, want %v", got, codes)
			}
			// NEVER A TERMINAL STATE: at least one transition must leave it,
			// or the record can never move on and (updates validate against
			// the current state) completed records become uneditable.
			// No contradiction with
			// TestEoClosureArtefactsNameTheirTypesTerminalState, which
			// requires the exact opposite of the closure artefacts:
			// there the terminal state is the one the record moves INTO and
			// the artefact is the price of entering it, while a field moved
			// here is newly placed on a state the record must still leave.
			for _, id := range ids {
				var out int64
				if err := db.Raw(`SELECT COUNT(*) FROM workflow_transitions WHERE from_state_id = ?`, id).Scan(&out).Error; err != nil {
					t.Fatal(err)
				}
				if out == 0 {
					t.Errorf("required at %q, from which no transition leaves", got)
				}
			}
			// Never the initial state itself: creation validates against it
			// and a create form has no record id to attach a file to.
			for _, id := range ids {
				var typ string
				if err := db.Raw(`SELECT state_type FROM workflow_states WHERE id = ?`, id).Scan(&typ).Error; err != nil {
					t.Fatal(err)
				}
				if typ == eoStateInitial {
					t.Errorf("required at the INITIAL state: the create form would demand a file it cannot attach")
				}
			}
		})
	}

	// eo_objection.proof has no non-terminal state to be required at, so it
	// stays `never` pending a client decision; this pins that it is not
	// silently put on its terminal state.
	if got := eoEffectiveField(t, db, eoObjectionTypeKey, "proof").RequiredMode; got != models.EORequiredNever {
		t.Errorf("eo_objection.proof is %q; its only post-creation state is terminal, so it stays never until the client decides", got)
	}

	// The guard: no file field anywhere is required at creation.
	var always []string
	if err := db.Raw(`
		SELECT t.key || '.' || f.key FROM eo_field_defs f
		JOIN eo_record_types t ON t.id = f.record_type_id AND t.deleted_at IS NULL
		WHERE f.deleted_at IS NULL AND f.kind IN ('file','multi_file') AND f.required_mode = 'always'`).
		Scan(&always).Error; err != nil {
		t.Fatal(err)
	}
	if len(always) > 0 {
		t.Errorf("file fields required at creation: %v", always)
	}
}

// The step is ONE-SHOT: a second boot, and an administrator's edit, must both
// leave the fields as they are. It also reaches a database that applied B2 but
// not this step, which is every pilot database in existence.
//
// Mutation: make the step convergent, or drop its `required_mode = never`
// guard.
func TestSeedEOPilot_FileStateMoveIsIdempotentAndReachesAnExistingDatabase(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)
	before := map[string]string{}
	snap := func() map[string]string {
		out := map[string]string{}
		for _, m := range eoFileFieldsMovedToState {
			d := eoEffectiveField(t, db, m.TypeKey, m.FieldKey)
			out[m.TypeKey+"."+m.FieldKey] = d.RequiredMode + string(d.RequiredAtStateIDs)
		}
		return out
	}
	before = snap()
	for i := 0; i < 3; i++ {
		seedEOPilot(db)
	}
	for k, v := range snap() {
		if before[k] != v {
			t.Errorf("%s changed across boots: %s -> %s", k, before[k], v)
		}
	}

	// An administrator's edit survives.
	adm := eoEffectiveField(t, db, eoDraftStudyTypeKey, "study_report")
	if err := db.Model(&models.EOFieldDef{}).Where("id = ?", adm.ID).Update("required_mode", models.EORequiredNever).Error; err != nil {
		t.Fatal(err)
	}
	seedEOPilot(db)
	if got := eoEffectiveField(t, db, eoDraftStudyTypeKey, "study_report").RequiredMode; got != models.EORequiredNever {
		t.Errorf("the seed reverted an administrator's edit: %q", got)
	}

	// An existing database: B2 applied, this step not.
	for _, m := range eoFileFieldsMovedToState {
		d := eoEffectiveField(t, db, m.TypeKey, m.FieldKey)
		if err := db.Model(&models.EOFieldDef{}).Where("id = ?", d.ID).
			Updates(map[string]any{"required_mode": models.EORequiredNever, "required_at_state_ids": []byte("[]")}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Where("key = ?", eoMoveFileRequirementToStateStepKey).Delete(&models.EOSeedStep{}).Error; err != nil {
		t.Fatal(err)
	}
	open, err := eoPilotSeedNeeded(db)
	if err != nil || !open {
		t.Fatalf("the fast path is shut on a database that has not applied %q (open=%v err=%v)", eoMoveFileRequirementToStateStepKey, open, err)
	}
	seedEOPilot(db)
	if got := snap(); got["eo_draft_decision_study.study_report"] != before["eo_draft_decision_study.study_report"] {
		t.Errorf("existing database not moved: %s, want %s", got["eo_draft_decision_study.study_report"], before["eo_draft_decision_study.study_report"])
	}
}
