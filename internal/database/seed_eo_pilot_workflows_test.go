package database

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/automax/backend/internal/models"
)

// ── Task A5: what the seeded state machines are, as data ────────────────────
//
// WHAT IS NOT HERE. "Call ResolveWorkflowForType for all ten" and every test
// that shows a seeded rule REFUSING something live in
// internal/services/eo_pilot_workflows_test.go, because package database
// cannot import package services (services imports database). A test over here
// can only assert that a column holds the value somebody typed into the seed.
// That is Task A4's precedent and this task follows it.

// eoStepMarker is a CFF step marker as the matrix's Why column writes one:
// "6.1", "5.1", "Q3". A decision recorded on eoClosureBasisStep must quote one
// of these ALONE, so that an inference cannot be dressed up as a quotation.
var eoStepMarker = regexp.MustCompile(`^(Q\d+|\d+\.\d+)$`)

// eoSeededWorkflow is one workflow read back with its states and transitions.
type eoSeededWorkflow struct {
	wf          models.Workflow
	states      []models.WorkflowState
	transitions []models.WorkflowTransition
}

func (w eoSeededWorkflow) state(code string) (models.WorkflowState, bool) {
	for _, s := range w.states {
		if s.Code == code {
			return s, true
		}
	}
	return models.WorkflowState{}, false
}

func (w eoSeededWorkflow) stateByID(id uuid.UUID) (models.WorkflowState, bool) {
	for _, s := range w.states {
		if s.ID == id {
			return s, true
		}
	}
	return models.WorkflowState{}, false
}

// eoSeededWorkflows reads back the workflow bound to each pilot record type,
// by the binding services.ResolveWorkflowForType reads and only that one:
// workflows.record_type_id. A type with no workflow is a fatal error here, not
// a skipped entry, because that is the whole claim.
func eoSeededWorkflows(t *testing.T, db *gorm.DB) map[string]eoSeededWorkflow {
	t.Helper()
	out := map[string]eoSeededWorkflow{}
	for _, typeKey := range eoPilotTypeKeys {
		var ty models.EORecordType
		if err := db.Where("key = ?", typeKey).Limit(1).Find(&ty).Error; err != nil {
			t.Fatalf("read record type %q: %v", typeKey, err)
		}
		if ty.ID == uuid.Nil {
			t.Fatalf("record type %q is not seeded", typeKey)
		}
		var wfs []models.Workflow
		if err := db.Where("record_type_id = ? AND is_active = ?", ty.ID, true).Find(&wfs).Error; err != nil {
			t.Fatalf("read the workflows bound to %q: %v", typeKey, err)
		}
		if len(wfs) != 1 {
			t.Fatalf("record type %q is bound to %d active workflows, want exactly 1 (services.ResolveWorkflowForType errors on zero AND on more than one, on every create and every transition)", typeKey, len(wfs))
		}
		w := eoSeededWorkflow{wf: wfs[0]}
		if err := db.Where("workflow_id = ?", w.wf.ID).Order("sort_order").Find(&w.states).Error; err != nil {
			t.Fatalf("read the states of %q: %v", w.wf.Code, err)
		}
		if err := db.Where("workflow_id = ?", w.wf.ID).Order("sort_order").Find(&w.transitions).Error; err != nil {
			t.Fatalf("read the transitions of %q: %v", w.wf.Code, err)
		}
		out[typeKey] = w
	}
	return out
}

// THE BINDING ITSELF. One active workflow per concrete type, found on
// record_type_id, under the code eoWorkflowCode derives from the type key —
// and the record type's own workflow_id agreeing with it, so the designer
// shows what the engine resolves.
//
// Mutation: delete any entry from eoPilotWorkflows; or drop the
// `Update("workflow_id", wf.ID)` at the end of eoSeedOnePilotWorkflow (the
// designer then shows no workflow for a type the engine runs one for).
func TestEoPilotBindsOneWorkflowToEveryConcreteType(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	if len(eoPilotWorkflows) != len(eoPilotTypeKeys) {
		t.Fatalf("eoPilotWorkflows has %d machines and the pilot has %d concrete types; every type needs exactly one", len(eoPilotWorkflows), len(eoPilotTypeKeys))
	}
	got := eoSeededWorkflows(t, db)
	for _, typeKey := range eoPilotTypeKeys {
		t.Run(typeKey, func(t *testing.T) {
			w := got[typeKey]
			if w.wf.Code != eoWorkflowCode(typeKey) {
				t.Errorf("workflow code = %q, want %q", w.wf.Code, eoWorkflowCode(typeKey))
			}
			if w.wf.RecordType != "eo" {
				t.Errorf("record_type = %q, want \"eo\"; \"all\" and \"both\" are wildcards the incident matcher reads", w.wf.RecordType)
			}
			if w.wf.IsDefault {
				t.Errorf("is_default is true, which offers an EO workflow to the incident product")
			}
			var ty models.EORecordType
			if err := db.Where("key = ?", typeKey).Limit(1).Find(&ty).Error; err != nil {
				t.Fatalf("read record type: %v", err)
			}
			if ty.WorkflowID == nil || *ty.WorkflowID != w.wf.ID {
				t.Errorf("record type %q points at workflow %v, the binding the engine reads is %v", typeKey, ty.WorkflowID, w.wf.ID)
			}
		})
	}
}

// EXACTLY ONE INITIAL STATE, AT LEAST ONE TERMINAL, and a machine that hangs
// together: every transition joins two states of its own workflow, every
// state but the initial one is reachable from it, and a terminal state is
// reachable. services.InitialState errors unless exactly one state is
// initial, and it is called on every create — so a workflow with none or with
// two makes every record of that type uncreatable.
//
// Mutation: change any `stateType: eoStateInitial` to eoStateNormal in
// eoPilotWorkflows; or make a terminal state normal; or point a transition's
// `to` at a state code the spec does not declare.
func TestEoPilotWorkflowsAreCoherentMachines(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	for _, typeKey := range eoPilotTypeKeys {
		t.Run(typeKey, func(t *testing.T) {
			w := eoSeededWorkflows(t, db)[typeKey]
			initial, terminal := 0, 0
			var initialID uuid.UUID
			for _, s := range w.states {
				switch s.StateType {
				case eoStateInitial:
					initial++
					initialID = s.ID
				case eoStateTerminal:
					terminal++
				case eoStateNormal:
				default:
					t.Errorf("state %q has state_type %q", s.Code, s.StateType)
				}
			}
			if initial != 1 {
				t.Fatalf("%d initial states, want exactly 1: services.InitialState errors otherwise, on every create", initial)
			}
			if terminal == 0 {
				t.Fatalf("no terminal state, so no record of this type could ever be closed")
			}
			if len(w.transitions) == 0 {
				t.Fatalf("no transitions, so the initial state is also the last one")
			}

			// Reachability from the one initial state.
			reached := map[uuid.UUID]bool{initialID: true}
			for range w.states {
				for _, tr := range w.transitions {
					if reached[tr.FromStateID] {
						reached[tr.ToStateID] = true
					}
				}
			}
			reachedTerminal := false
			for _, s := range w.states {
				if !reached[s.ID] {
					t.Errorf("state %q is reachable from no transition out of the initial state", s.Code)
				}
				if s.StateType == eoStateTerminal && reached[s.ID] {
					reachedTerminal = true
				}
			}
			if !reachedTerminal {
				t.Errorf("no terminal state is reachable from the initial state")
			}
			for _, tr := range w.transitions {
				if _, ok := w.stateByID(tr.FromStateID); !ok {
					t.Errorf("transition %q leaves a state of another workflow", tr.Code)
				}
				if _, ok := w.stateByID(tr.ToStateID); !ok {
					t.Errorf("transition %q enters a state of another workflow", tr.Code)
				}
				if !tr.IsActive {
					t.Errorf("transition %q is inactive, so it is configuration nobody can run", tr.Code)
				}
				// An empty allowed-role set is read as NO RESTRICTION by
				// services.checkActor, which would hand every authenticated
				// user the power to move a record in a statutory register.
				var n int64
				if err := db.Table("transition_allowed_roles").
					Where("workflow_transition_id = ?", tr.ID).Count(&n).Error; err != nil {
					t.Fatalf("count the allowed roles of %q: %v", tr.Code, err)
				}
				if n == 0 {
					t.Errorf("transition %q is restricted to no role, which services.checkActor reads as NO RESTRICTION", tr.Code)
				}
			}
		})
	}
}

// ADDENDUM 5: THE MACHINES ARE DERIVED AND THE DATA SAYS SO. The seven CFFs
// are not in this repository; spec §9 says the machines are "approved as
// derived from the CFFs" and that the CFFs are the normative source, so there
// is no approved machine to transcribe. Every seeded state and transition
// carries the marker, the set is a QUERY rather than a memory, and nothing
// that was NOT seeded here carries it.
//
// Mutation: drop `EOClientUnconfirmed: true` from eoSeedPilotState or
// eoSeedPilotTransition.
func TestEoPilotStateMachinesAreMarkedClientUnconfirmed(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	got := eoSeededWorkflows(t, db)
	seeded := map[string]bool{}
	for _, typeKey := range eoPilotTypeKeys {
		w := got[typeKey]
		for _, s := range w.states {
			if !s.EOClientUnconfirmed {
				t.Errorf("state %q of %q is not marked client-unconfirmed; the §9 sign-off query cannot see it", s.Code, w.wf.Code)
			}
			seeded[w.wf.Code+"|state|"+s.Code] = true
		}
		for _, tr := range w.transitions {
			if !tr.EOClientUnconfirmed {
				t.Errorf("transition %q of %q is not marked client-unconfirmed", tr.Code, w.wf.Code)
			}
			seeded[w.wf.Code+"|transition|"+tr.Code] = true
		}
	}
	if len(seeded) == 0 {
		t.Fatalf("no state or transition was seeded, so this test proves nothing")
	}

	set, err := eoUnconfirmedStateMachines(db)
	if err != nil {
		t.Fatalf("the sign-off query: %v", err)
	}
	queried := map[string]bool{}
	for _, s := range set {
		queried[s.WorkflowCode+"|"+s.Kind+"|"+s.Code] = true
		if s.Name == "" {
			t.Errorf("%+v has no name, so the sign-off list is unreadable", s)
		}
	}
	for id := range seeded {
		if !queried[id] {
			t.Errorf("%s is seeded by this task and the sign-off query does not return it", id)
		}
	}
	for id := range queried {
		if !seeded[id] {
			t.Errorf("%s is reported as awaiting sign-off and this task did not seed it", id)
		}
	}
	// Stable order, so the list can be diffed between boots.
	codes := make([]string, len(set))
	for i, s := range set {
		codes[i] = s.WorkflowCode + "|" + s.Kind + "|" + s.Code
	}
	if !sort.StringsAreSorted(codes) {
		t.Errorf("the sign-off list is not in a stable order: %v", codes)
	}
}

// THE FIRST OF THE TWO SHIPPED-AHEAD FUNCTIONS, ARMED. A2 seeded the Draft
// Decision Study's `draft_text` and `draft_file` UNLOCKED, because
// is_locked_after_state_id names a workflow state and none existed; the
// matrix's "locked after submit" and §7.12's "the original is never edited"
// were configured by nobody. This asserts the lock now names the `submitted`
// state of that type's own workflow. The refusal itself is in package
// services, where ValidateRecordValues can be put in front of an edit.
//
// Mutation: clear `lockDraftFieldsAfter` on the Draft Decision Study's entry
// in eoPilotWorkflows.
func TestEoDraftOriginalIsLockedAfterTheSubmittedState(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	w := eoSeededWorkflows(t, db)[eoDraftStudyTypeKey]
	submitted, ok := w.state("submitted")
	if !ok {
		t.Fatalf("the Draft Decision Study's workflow has no `submitted` state; the matrix's \"locked after submit\" has nothing to name")
	}
	if submitted.StateType == eoStateInitial {
		t.Errorf("`submitted` is the INITIAL state, so the original would be locked before it was ever typed")
	}
	for _, key := range eoDraftLockedFieldKeys {
		def := eoEffectiveField(t, db, eoDraftStudyTypeKey, key)
		if def.IsLockedAfterStateID == nil {
			t.Errorf("%s.%s is locked after no state: §7.12's \"the original is never edited\" is configured by nobody", eoDraftStudyTypeKey, key)
			continue
		}
		if *def.IsLockedAfterStateID != submitted.ID {
			t.Errorf("%s.%s is locked after state %v, want the `submitted` state %v", eoDraftStudyTypeKey, key, *def.IsLockedAfterStateID, submitted.ID)
		}
	}
}

// THE SECOND SHIPPED-AHEAD FUNCTION, ARMED. A3 seeded both `evidence` fields
// `at_state` with an EMPTY state list — "required at no state", a rule that
// cannot fire — precisely so that A5 could point them at a real closing
// state. This asserts every closure artefact names the TERMINAL state of its
// own type's workflow, and that no other field acquired one.
//
// Mutation: clear `closureState` on any entry of eoPilotWorkflows; or point
// one at a non-terminal state code.
func TestEoClosureArtefactsNameTheirTypesTerminalState(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)
	got := eoSeededWorkflows(t, db)

	byType := map[string][]string{}
	for _, typeKey := range eoPilotTypeKeys {
		if keys := eoClosureArtefactFieldKeys(typeKey); len(keys) > 0 {
			byType[typeKey] = keys
		}
	}
	if len(byType) == 0 {
		t.Fatalf("no type has a closure artefact, so this test proves nothing")
	}
	for typeKey, keys := range byType {
		t.Run(typeKey, func(t *testing.T) {
			w := got[typeKey]
			var closing *models.WorkflowState
			for i := range w.states {
				if w.states[i].StateType == eoStateTerminal {
					closing = &w.states[i]
				}
			}
			if closing == nil {
				t.Fatalf("no terminal state to require the closure artefacts at")
			}
			for _, key := range keys {
				def := eoEffectiveField(t, db, typeKey, key)
				if def.RequiredMode != models.EORequiredAtState {
					t.Errorf("%s.%s required_mode = %q, want %q", typeKey, key, def.RequiredMode, models.EORequiredAtState)
				}
				var ids []uuid.UUID
				if s := strings.TrimSpace(string(def.RequiredAtStateIDs)); s == "" || s == "null" {
					t.Errorf("%s.%s names NO state, which ValidateRecordValues reads as \"required at no state\" — a rule that cannot fire", typeKey, key)
					continue
				} else if err := json.Unmarshal(def.RequiredAtStateIDs, &ids); err != nil {
					t.Fatalf("%s.%s required_at_state_ids: %v", typeKey, key, err)
				}
				if len(ids) != 1 || ids[0] != closing.ID {
					t.Errorf("%s.%s is required at %v, want the terminal state %q (%v)", typeKey, key, ids, closing.Code, closing.ID)
				}
			}
		})
	}

	// Nothing else acquired a state list: an operation that updated every
	// at_state field everywhere would be a different, and wrong, operation.
	var rows []struct {
		TypeKey string
		Key     string
	}
	if err := db.Raw(`
		SELECT t.key AS type_key, f.key AS key
		FROM eo_field_defs f JOIN eo_record_types t ON t.id = f.record_type_id
		WHERE f.deleted_at IS NULL AND f.required_at_state_ids IS NOT NULL
		  AND f.required_at_state_ids::text NOT IN ('[]', 'null')
		ORDER BY t.key, f.key`).Scan(&rows).Error; err != nil {
		t.Fatalf("read every field that names a state: %v", err)
	}
	want := map[string]bool{}
	for typeKey, keys := range byType {
		for _, k := range keys {
			engineKey, _, _ := eoMatrixKey(k)
			want[typeKey+"."+engineKey] = true
		}
	}
	for _, r := range rows {
		if !want[r.TypeKey+"."+r.Key] {
			t.Errorf("%s.%s names a required-at state and is not a declared closure artefact", r.TypeKey, r.Key)
		}
	}
	if len(rows) != len(want) {
		t.Errorf("%d fields name a required-at state, %d closure artefacts are declared", len(rows), len(want))
	}
}

// ADDENDUM 4, AGAINST THE CLIENT'S DOCUMENT. Every row this task resolved as
// a closure artefact really is marked "Yes" in the matrix (not "Yes at
// closure", which A3 already handled, and not something else), and every row
// recorded as LEFT FOR THE CLIENT really is still seeded `always`. The two
// registries are what keeps "we resolved the ones we could justify" from
// reading as "we resolved them all".
//
// Mutation: add a row to eoClosureArtefacts whose matrix cell is not "Yes";
// or move a row from eoClosureArtefactsLeftForTheClient into
// eoClosureArtefacts without changing its seeded mode.
func TestEoClosureArtefactDecisionsMatchTheMatrix(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)
	matrix := eoReadFieldMatrix(t)

	row := func(t *testing.T, typeKey, fieldKey string) eoMatrixRow {
		t.Helper()
		for _, r := range matrix[eoMatrixSections[typeKey]] {
			if r.Key == fieldKey {
				return r
			}
		}
		t.Fatalf("the matrix section %q has no row %q", eoMatrixSections[typeKey], fieldKey)
		return eoMatrixRow{}
	}

	if len(eoClosureArtefacts) == 0 {
		t.Fatalf("no \"Yes\" row was resolved as a closure artefact and none is recorded as left for the client; addendum 4 is answered by nobody")
	}
	seen := map[string]bool{}
	for _, a := range eoClosureArtefacts {
		t.Run("resolved/"+a.TypeKey+"."+a.FieldKey, func(t *testing.T) {
			if seen[a.TypeKey+"."+a.FieldKey] {
				t.Fatalf("declared twice")
			}
			seen[a.TypeKey+"."+a.FieldKey] = true
			r := row(t, a.TypeKey, a.FieldKey)
			if r.Required != a.MatrixRequired {
				t.Errorf("the decision quotes the matrix as %q; the matrix says %q", a.MatrixRequired, r.Required)
			}
			if r.Required != "Yes" {
				t.Errorf("only a \"Yes\" row is this registry's business; %q is %q and belongs elsewhere", a.FieldKey, r.Required)
			}
			if !strings.Contains(r.Why, a.MatrixWhy) {
				t.Errorf("the decision quotes the Why cell as %q; the matrix says %q", a.MatrixWhy, r.Why)
			}
			if strings.TrimSpace(a.Why) == "" {
				t.Errorf("no reason is recorded")
			}
			// THE CHECK ABOVE CANNOT FAIL ON ITS OWN when MatrixWhy is a
			// bare step marker like "6.1" — every Why cell of that row
			// contains it by construction. The basis is what makes the two
			// kinds of claim separable, and each gets the check it needs.
			switch a.Basis {
			case eoClosureBasisPhrase:
				// A phrase must actually SAY the moment. A step marker
				// filed as a phrase fails here, which is the whole point.
				low := strings.ToLower(a.MatrixWhy)
				if !strings.Contains(low, "clos") && !strings.Contains(low, "verif") {
					t.Errorf("%q is recorded as naming the moment in words and names neither closing nor verification; it is an inference from the step marker and belongs under %q", a.MatrixWhy, eoClosureBasisStep)
				}
				if len([]rune(a.MatrixWhy)) < 12 {
					t.Errorf("%q is too short to be the client's own phrase", a.MatrixWhy)
				}
			case eoClosureBasisStep:
				// A step entry quotes the marker ALONE — not a sentence that
				// happens to contain one — and the marker must be the one
				// this row's cell opens with.
				if !eoStepMarker.MatchString(a.MatrixWhy) {
					t.Errorf("%q is recorded as a step marker and is not one (want Q<n> or <n>.<n>); quote the marker alone, so the claim is visibly an inference", a.MatrixWhy)
				}
				if !strings.HasPrefix(r.Why, a.MatrixWhy) {
					t.Errorf("the decision claims step %q and the matrix row's Why cell is %q, which does not begin with it", a.MatrixWhy, r.Why)
				}
			default:
				t.Errorf("basis is %q, want %q or %q: the registry must say which kind of claim this is", a.Basis, eoClosureBasisPhrase, eoClosureBasisStep)
			}
			def := eoEffectiveField(t, db, a.TypeKey, a.FieldKey)
			if def.RequiredMode != models.EORequiredAtState {
				t.Errorf("seeded %q, a resolved closure artefact must be %q", def.RequiredMode, models.EORequiredAtState)
			}
		})
	}

	// THE CHECK THAT MAKES A STEP-NUMBER INFERENCE FALSIFIABLE. The matrix
	// lists a type's rows in CFF order, so "this field belongs to the type's
	// last step" is the claim that it sits at the END of the type's section.
	// Every step-inferred artefact of a type must therefore form a CONTIGUOUS
	// RUN ENDING AT THE SECTION'S LAST ROW. Filing a mid-process row (say
	// `study_report`, four rows from the end) on a step basis breaks this.
	for _, typeKey := range eoPilotTypeKeys {
		var inferred []string
		for _, a := range eoClosureArtefacts {
			if a.TypeKey == typeKey && a.Basis == eoClosureBasisStep {
				inferred = append(inferred, a.FieldKey)
			}
		}
		if len(inferred) == 0 {
			continue
		}
		t.Run("the step-inferred run of "+typeKey+" ends the section", func(t *testing.T) {
			rows := matrix[eoMatrixSections[typeKey]]
			if len(rows) == 0 {
				t.Fatalf("the matrix has no section for %q, so a step inference has nothing to rest on", typeKey)
			}
			want := map[string]bool{}
			for _, k := range inferred {
				want[k] = true
			}
			// Walk back from the last row: exactly len(inferred) rows, all
			// declared, and nothing declared above them.
			for i := len(rows) - 1; i >= 0; i-- {
				declared := want[rows[i].Key]
				inRun := i >= len(rows)-len(inferred)
				if inRun && !declared {
					t.Errorf("row %d of the section is %q and is NOT a step-inferred artefact, so the run %v does not end the section; the inference \"these are the type's last steps\" is not what the document shows", i, rows[i].Key, inferred)
				}
				if !inRun && declared {
					t.Errorf("%q is step-inferred and sits %d rows from the end of the section; a step inference only holds for the rows the matrix lists last", rows[i].Key, len(rows)-1-i)
				}
			}
		})
	}
	for _, a := range eoClosureArtefactsLeftForTheClient {
		t.Run("left/"+a.TypeKey+"."+a.FieldKey, func(t *testing.T) {
			if seen[a.TypeKey+"."+a.FieldKey] {
				t.Fatalf("is recorded as both resolved and left for the client")
			}
			r := row(t, a.TypeKey, a.FieldKey)
			if r.Required != a.MatrixRequired {
				t.Errorf("the decision quotes the matrix as %q; the matrix says %q", a.MatrixRequired, r.Required)
			}
			def := eoEffectiveField(t, db, a.TypeKey, a.FieldKey)
			if def.RequiredMode != models.EORequiredAlways {
				t.Errorf("seeded %q; a row left for the client stays %q — strict, and recoverable in both directions", def.RequiredMode, models.EORequiredAlways)
			}
		})
	}
	// Every "Yes at closure" cell is still A3's business and must not have
	// been moved into this task's registry.
	for _, f := range eoAtClosureFields {
		if seen[f.TypeKey+"."+f.FieldKey] {
			t.Errorf("%s.%s is marked \"Yes at closure\" by the matrix and is declared in eoClosureArtefacts, which is for \"Yes\" rows only", f.TypeKey, f.FieldKey)
		}
	}
}

// ADDENDUM 1, FROM THIS SIDE. Task A4's eo_objection_parent blocks a
// transition by STRING EQUALITY against eoDraftRaiseToHETransitionCode, and
// the rule matches nothing under any other code — an objection raised,
// recorded and silently ignored by the step it exists to hold up.
// TestEoBlockedTransitionNamesATransitionItsWorkflowHas is the general guard
// and arms the moment this task binds a workflow; this is the specific one,
// and it names the STATE the raise comes out of as well, because a transition
// under the right code out of an unreachable state would satisfy the general
// guard and refuse nothing.
//
// Mutation: change the `code:` of the Draft Decision Study's raise transition
// in eoPilotWorkflows to a literal instead of the constant.
func TestEoDraftWorkflowSeedsTheRaiseUnderTheSharedConstant(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	w := eoSeededWorkflows(t, db)[eoDraftStudyTypeKey]
	var raise *models.WorkflowTransition
	for i := range w.transitions {
		if w.transitions[i].Code == eoDraftRaiseToHETransitionCode {
			raise = &w.transitions[i]
		}
	}
	if raise == nil {
		codes := make([]string, len(w.transitions))
		for i, tr := range w.transitions {
			codes[i] = tr.Code
		}
		t.Fatalf("the Draft Decision Study's workflow has no transition coded %q (it has %v); eo_objection_parent.blocks_parent_transition_key names that code and would match nothing",
			eoDraftRaiseToHETransitionCode, codes)
	}
	to, _ := w.stateByID(raise.ToStateID)
	if to.StateType == eoStateTerminal {
		t.Errorf("the raise to H.E. enters the TERMINAL state %q; the matrix says an objection blocks the raising step AND NOT the closure, which only differ when the two are different transitions", to.Code)
	}
	// The link type really does name this code, read from the seeded row
	// rather than from the constant on both sides.
	var lt models.EOLinkType
	if err := db.Where("key = ?", eoObjectionLinkKey).Limit(1).Find(&lt).Error; err != nil {
		t.Fatalf("read link type %q: %v", eoObjectionLinkKey, err)
	}
	if lt.BlocksParentTransitionKey != raise.Code {
		t.Errorf("link type %q blocks %q and the seeded transition is %q", eoObjectionLinkKey, lt.BlocksParentTransitionKey, raise.Code)
	}
}

// ADDENDUM 3: "CLOSES THE PARENT" IS WIRED TO SOMETHING. Every close_parent
// action names a link type that is seeded, whose CHILD type is the type whose
// workflow carries the action, and a transition CODE that the PARENT type's
// own workflow actually has. Each of those three is a silent failure on its
// own: a wrong link type finds no parent, a wrong direction finds no parent,
// and a wrong transition code finds no transition — and the executor logs one
// line while the transition succeeds.
//
// Mutation: change `closesParentTransition` on the Decision Record's `issue`
// to anything the Draft Decision Study's workflow does not have.
func TestEoCloseParentActionsNameASeededLinkTypeAndARealTransition(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)
	got := eoSeededWorkflows(t, db)

	found := 0
	for _, typeKey := range eoPilotTypeKeys {
		w := got[typeKey]
		for _, tr := range w.transitions {
			var actions []models.TransitionAction
			if err := db.Where("transition_id = ? AND action_type = ?", tr.ID, eoCloseParentActionType).Find(&actions).Error; err != nil {
				t.Fatalf("read the actions of %q: %v", tr.Code, err)
			}
			for _, a := range actions {
				found++
				t.Run(typeKey+"."+tr.Code, func(t *testing.T) {
					if !a.IsActive {
						t.Errorf("the action is inactive, so the client's \"Closes the parent\" runs nowhere")
					}
					var cfg eoCloseParentConfig
					if err := json.Unmarshal([]byte(a.Config), &cfg); err != nil {
						t.Fatalf("config %q: %v", a.Config, err)
					}
					var lt models.EOLinkType
					if err := db.Where("key = ?", cfg.LinkType).Limit(1).Find(&lt).Error; err != nil {
						t.Fatalf("read link type %q: %v", cfg.LinkType, err)
					}
					if lt.ID == uuid.Nil {
						t.Fatalf("names link type %q, which is not seeded", cfg.LinkType)
					}
					// DIRECTION: the save pipeline writes the link FROM the
					// child TO the parent, so this record's type must be the
					// link type's SOURCE.
					var sources []string
					if err := json.Unmarshal(lt.SourceTypeIDs, &sources); err != nil {
						t.Fatalf("source_type_ids of %q: %v", cfg.LinkType, err)
					}
					var ty models.EORecordType
					if err := db.Where("key = ?", typeKey).Limit(1).Find(&ty).Error; err != nil {
						t.Fatalf("read record type: %v", err)
					}
					ok := false
					for _, s := range sources {
						if s == ty.ID.String() {
							ok = true
						}
					}
					if !ok {
						t.Errorf("link type %q does not have %q as a source, so this record carries no link of that type and the action would find no parent", cfg.LinkType, typeKey)
					}
					// The transition code must exist on the PARENT's workflow.
					var targets []string
					if err := json.Unmarshal(lt.TargetTypeIDs, &targets); err != nil {
						t.Fatalf("target_type_ids of %q: %v", cfg.LinkType, err)
					}
					if len(targets) == 0 {
						t.Fatalf("link type %q names no target type", cfg.LinkType)
					}
					for _, raw := range targets {
						parentID, err := uuid.Parse(raw)
						if err != nil {
							t.Fatalf("unparseable target %q", raw)
						}
						var codes []string
						if err := db.Model(&models.WorkflowTransition{}).
							Joins("JOIN workflows w ON w.id = workflow_transitions.workflow_id").
							Where("w.record_type_id = ? AND w.is_active AND workflow_transitions.is_active", parentID).
							Pluck("workflow_transitions.code", &codes).Error; err != nil {
							t.Fatalf("read the parent's transitions: %v", err)
						}
						has := false
						for _, c := range codes {
							if c == cfg.Transition {
								has = true
							}
						}
						if !has {
							t.Errorf("closes the parent through transition %q and the workflow bound to parent type %s has %v; the action would find no transition and log one line while this transition succeeded", cfg.Transition, parentID, codes)
						}
					}
				})
			}
		}
	}
	// The matrix states the rule twice — Decision Record (matrix:165) and
	// Outgoing Reply (matrix:311) — so two is the number, and a seed that
	// stopped writing them would make the loop above prove nothing.
	if found != 2 {
		t.Errorf("%d close_parent actions are seeded, want 2 (Decision Record and Outgoing Reply)", found)
	}
}

// The action type is a STRING SHARED WITH AN ENGINE SWITCH IN ANOTHER
// PACKAGE, and package database cannot import package services. An action
// type the executor does not know is logged as "unknown action type" while
// the transition succeeds — the governance rule would run nowhere and nothing
// would report it. So this reads the executor's source and compares, the way
// TestEoAnyTargetSentinelMatchesTheEngineConstant does for the "any" sentinel.
//
// THE CASE LABEL MAY BE THE LITERAL OR A CONSTANT BOUND TO IT. The engine
// declares `const eoActionTypeCloseParent = "close_parent"` so its
// post-commit dispatch and its pre-commit refusal cannot disagree about which
// actions close a parent; a test that insisted on the literal would forbid
// exactly that. Either form is accepted, and a constant is only accepted when
// its VALUE is this string.
//
// Mutation: rename eoCloseParentActionType, or rename the case label in
// services.eoActionExecutor.dispatch, or change the engine constant's value.
func TestEoCloseParentActionTypeMatchesTheEngine(t *testing.T) {
	const src = "../services/eo_action_executor.go"
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	text := string(raw)

	// The literal, plus the name of any constant in that file bound to it.
	labels := []string{fmt.Sprintf("%q", eoCloseParentActionType)}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) >= 4 && fields[0] == "const" && fields[2] == "=" && fields[3] == fmt.Sprintf("%q", eoCloseParentActionType) {
			labels = append(labels, fields[1])
		}
	}
	found := false
	for _, label := range labels {
		if strings.Contains(text, "case "+label+":") {
			found = true
		}
	}
	if !found {
		t.Fatalf("%s dispatches on none of %v in its switch: an action seeded under %q would be logged as an unknown action type and the transition would succeed with the parent left open",
			src, labels, eoCloseParentActionType)
	}
	// And the config keys the executor decodes are the ones the seed writes.
	for _, tag := range []string{`json:"link_type"`, `json:"transition"`} {
		if !strings.Contains(text, tag) {
			t.Errorf("%s does not decode %s, which the seeded config carries", src, tag)
		}
	}
}

// ADDENDUM 6: THE 3/7/14 LADDER IS CONFIGURATION, NOT CODE. WorkflowState
// already carries SLAHours, SLAUnit and EscalationPolicyID, and
// services.ScheduleStateSLA writes one timer per policy step at
// `state entered + SLA + step.DelayHours`. This asserts the three rungs land
// on +3, +7 and +14 days, arithmetic included — the numbers in the seed are
// delays from the SLA deadline, which is not what the matrix's three numbers
// are, and the conversion is exactly where this would go quietly wrong.
//
// Mutation: change any element of eoDirectiveEscalationDelays, or
// eoDirectiveExecutionSLAHours.
func TestEoDirectiveEscalationLadderIsThreeSevenFourteen(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	w := eoSeededWorkflows(t, db)[eoDirectiveTypeKey]
	st, ok := w.state("in_execution")
	if !ok {
		t.Fatalf("the Directive's workflow has no `in_execution` state to hang the ladder on")
	}
	if st.SLAHours == nil || *st.SLAHours == 0 {
		t.Fatalf("`in_execution` has no SLA, so services.ScheduleStateSLA schedules nothing and the ladder is configured by nobody")
	}
	if st.SLAUnit != "hours" {
		t.Errorf("sla_unit = %q, want \"hours\" (the delays below are hours)", st.SLAUnit)
	}
	if st.EscalationPolicyID == nil {
		t.Fatalf("`in_execution` points at no escalation policy; ScheduleStateSLA returns early without one")
	}
	var policy models.EscalationPolicy
	if err := db.Preload("Steps").Where("id = ?", *st.EscalationPolicyID).Limit(1).Find(&policy).Error; err != nil {
		t.Fatalf("read the escalation policy: %v", err)
	}
	if policy.ID == uuid.Nil {
		t.Fatalf("`in_execution` points at escalation policy %v, which does not exist", *st.EscalationPolicyID)
	}
	if !policy.IsActive {
		t.Errorf("the policy is inactive; ScheduleStateSLA schedules nothing for one")
	}
	sort.Slice(policy.Steps, func(i, j int) bool { return policy.Steps[i].StepOrder < policy.Steps[j].StepOrder })
	wantDays := []int{3, 7, 14}
	if len(policy.Steps) != len(wantDays) {
		t.Fatalf("the policy has %d steps, want %d (the matrix's +3, +7 and +14 days)", len(policy.Steps), len(wantDays))
	}
	for i, step := range policy.Steps {
		if step.StepOrder != i+1 {
			t.Errorf("step %d has step_order %d; ScheduleStateSLA refuses two steps with the same order", i, step.StepOrder)
		}
		// The arithmetic ScheduleStateSLA does: entered + SLA + delay.
		gotHours := *st.SLAHours + step.DelayHours
		if gotHours != wantDays[i]*24 {
			t.Errorf("rung %d fires %d hours after the state is entered (%d days), want %d days", i+1, gotHours, gotHours/24, wantDays[i])
		}
	}
}

// EVERY terminal transition carries a mandatory comment requirement, with no
// exception, so a statutory register never closes a record with no note
// saying why.
//
// THE EARLIER VERSION OF THIS TEST EXEMPTED THE TWO A close_parent ACTION
// RUNS, and actively forbade the fix: it errored if an action-run closure
// required a note, on the stated ground that "no action can supply one". That
// ground was false. services.eoActionExecutor.closeParent always passes a
// non-empty Comment and services.eoCheckRequirements only tests for an empty
// one, so the requirement is satisfied by the action — and the exemption
// meant the Draft Decision Study's and the Incoming Correspondence's `close`,
// both runnable by hand by anyone holding either seeded role, closed a
// statutory record with no note. A false reason inside a passing test is
// worse than no test, because the next person to notice reads it and moves
// on.
//
// The action-run pair is still identified here, but to assert that it is
// INCLUDED rather than excused — and the fact that the action really does
// supply a comment is proved on the other side, by
// services.TestEoPilotChildrenThatCloseTheirParent, which now closes a parent
// whose closing transition carries this requirement.
//
// Mutation: drop `requireComment` from any terminal transition, the two
// action-run ones included.
func TestEoTerminalTransitionsRequireANote(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)
	got := eoSeededWorkflows(t, db)

	// The transitions an action runs, read from the seeded actions rather
	// than restated: (type key, transition code).
	actionRun := map[string]bool{}
	for _, typeKey := range eoPilotTypeKeys {
		for _, tr := range got[typeKey].transitions {
			var actions []models.TransitionAction
			if err := db.Where("transition_id = ? AND action_type = ?", tr.ID, eoCloseParentActionType).Find(&actions).Error; err != nil {
				t.Fatalf("read actions: %v", err)
			}
			for _, a := range actions {
				var cfg eoCloseParentConfig
				if err := json.Unmarshal([]byte(a.Config), &cfg); err != nil {
					t.Fatalf("config: %v", err)
				}
				var lt models.EOLinkType
				if err := db.Where("key = ?", cfg.LinkType).Limit(1).Find(&lt).Error; err != nil {
					t.Fatalf("read link type: %v", err)
				}
				var targets []string
				if err := json.Unmarshal(lt.TargetTypeIDs, &targets); err != nil {
					t.Fatalf("targets: %v", err)
				}
				for _, raw := range targets {
					for _, pk := range eoPilotTypeKeys {
						var ty models.EORecordType
						if err := db.Where("key = ?", pk).Limit(1).Find(&ty).Error; err != nil {
							t.Fatalf("read type: %v", err)
						}
						if ty.ID.String() == raw {
							actionRun[pk+"."+cfg.Transition] = true
						}
					}
				}
			}
		}
	}
	if len(actionRun) == 0 {
		t.Fatalf("no transition is run by an action, so the clause below is untested")
	}

	checked, ranByAction := 0, 0
	for _, typeKey := range eoPilotTypeKeys {
		w := got[typeKey]
		for _, tr := range w.transitions {
			to, _ := w.stateByID(tr.ToStateID)
			if to.StateType != eoStateTerminal {
				continue
			}
			checked++
			var n int64
			if err := db.Model(&models.TransitionRequirement{}).
				Where("transition_id = ? AND requirement_type = ?", tr.ID, "comment").Count(&n).Error; err != nil {
				t.Fatalf("count the requirements of %q: %v", tr.Code, err)
			}
			if n == 0 {
				t.Errorf("%s.%s ends a record in a statutory register and requires no note", typeKey, tr.Code)
			}
			if actionRun[typeKey+"."+tr.Code] {
				ranByAction++
				if n == 0 {
					t.Errorf("%s.%s is the transition a close_parent action runs and requires no note; the action supplies one, so there is no reason to exempt it and a person running it by hand would close the record silently", typeKey, tr.Code)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatalf("no terminal transition was found")
	}
	if ranByAction != len(actionRun) {
		t.Errorf("%d of the %d action-run transitions were reached by the loop; the rest are not terminal, so the clause above checked less than it claims", ranByAction, len(actionRun))
	}
}

// The workflow seeding has to be REACHABLE FROM seedEOPilot (addendum 1):
// Task A4's forcing test calls seedEOPilot and sees only the workflows that
// call creates, so a workflow seeded from a function the pilot seed does not
// reach leaves that guard vacuous for ever. The step is also keyed, like every
// other, and the key is a permanent identifier.
//
// Mutation: remove the workflow step from eoPilotSteps.
func TestEoWorkflowSeedingIsAStepOfThePilotSeed(t *testing.T) {
	found := ""
	for _, st := range eoPilotSteps {
		if st.key == eoSeedWorkflowsStepKey {
			found = st.key
		}
	}
	if found == "" {
		keys := make([]string, len(eoPilotSteps))
		for i, st := range eoPilotSteps {
			keys[i] = st.key
		}
		t.Fatalf("eoPilotSteps is %v and has no %q step; the workflows would be seeded by nobody and TestEoBlockedTransitionNamesATransitionItsWorkflowHas would stay vacuous for ever", keys, eoSeedWorkflowsStepKey)
	}
}

// A second run of the whole pilot seed on a database whose applied-step rows
// have been removed must converge rather than collide: the per-row idempotency
// (workflow by code, state by (workflow, code), transition by (workflow, code,
// from_state)) is what makes a database that failed mid-transaction on an
// earlier boot recoverable. TestSeedEOPilotIsIdempotent covers the ordinary
// path, where the applied row short-circuits the step.
//
// Mutation: drop the `if existing.ID != uuid.Nil` early return from
// eoSeedPilotState, or the count check from eoSeedPilotTransition.
func TestEoWorkflowSeedingConvergesWithoutItsAppliedRow(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	before := eoWorkflowCounts(t, db)
	if err := db.Where("key = ?", eoSeedWorkflowsStepKey).Delete(&models.EOSeedStep{}).Error; err != nil {
		t.Fatalf("remove the applied-step row: %v", err)
	}
	seedEOPilot(db)
	after := eoWorkflowCounts(t, db)
	if before != after {
		t.Errorf("a re-run without the applied row changed the configuration: before %+v, after %+v", before, after)
	}
}

type eoWorkflowCount struct {
	workflows, states, transitions, requirements, fieldChanges, actions, policySteps int64
}

func eoWorkflowCounts(t *testing.T, db *gorm.DB) eoWorkflowCount {
	t.Helper()
	return eoWorkflowCount{
		workflows:    eoCountRows(t, db, &models.Workflow{}),
		states:       eoCountRows(t, db, &models.WorkflowState{}),
		transitions:  eoCountRows(t, db, &models.WorkflowTransition{}),
		requirements: eoCountRows(t, db, &models.TransitionRequirement{}),
		// The one row kind whose idempotency lives in a code path of its own
		// (eoDeclareClosureArtefacts), so it is the one most likely to double
		// on a re-run and the one worth counting here.
		fieldChanges: eoCountRows(t, db, &models.TransitionFieldChange{}),
		actions:      eoCountRows(t, db, &models.TransitionAction{}),
		policySteps:  eoCountRows(t, db, &models.EscalationPolicyStep{}),
	}
}

// Every state and transition CODE a spec declares is distinct within its own
// workflow, and every transition names states the spec declares. A duplicate
// state code would make the (workflow, code) idempotency key ambiguous; a
// duplicate transition code out of the same state would do the same, and a
// duplicate transition code out of a DIFFERENT state is legitimate (`close`
// appears on several workflows) and must stay legitimate.
//
// This reads the SPECS rather than the database, so it runs without one.
func TestEoWorkflowSpecsAreInternallyConsistent(t *testing.T) {
	seenType := map[string]bool{}
	for _, spec := range eoPilotWorkflows {
		t.Run(spec.typeKey, func(t *testing.T) {
			if seenType[spec.typeKey] {
				t.Fatalf("a second machine is declared for this type; ResolveWorkflowForType errors on more than one")
			}
			seenType[spec.typeKey] = true
			if strings.TrimSpace(spec.derivation) == "" {
				t.Errorf("no derivation is recorded, and the CFFs are not in this repository")
			}
			states := map[string]bool{}
			for _, st := range spec.states {
				if states[st.code] {
					t.Errorf("state code %q is declared twice", st.code)
				}
				states[st.code] = true
				if strings.TrimSpace(st.why) == "" {
					t.Errorf("state %q records nothing about what it was derived from", st.code)
				}
			}
			pairs := map[string]bool{}
			for _, tr := range spec.transitions {
				if pairs[tr.code+"|"+tr.from] {
					t.Errorf("transition %q out of %q is declared twice", tr.code, tr.from)
				}
				pairs[tr.code+"|"+tr.from] = true
				if !states[tr.from] {
					t.Errorf("transition %q leaves the undeclared state %q", tr.code, tr.from)
				}
				if !states[tr.to] {
					t.Errorf("transition %q enters the undeclared state %q", tr.code, tr.to)
				}
				if strings.TrimSpace(tr.why) == "" {
					t.Errorf("transition %q records nothing about what it was derived from", tr.code)
				}
				if (tr.closesParentLinkType == "") != (tr.closesParentTransition == "") {
					t.Errorf("transition %q names half of a close_parent action (%q, %q)", tr.code, tr.closesParentLinkType, tr.closesParentTransition)
				}
			}
			if spec.lockDraftFieldsAfter != "" && !states[spec.lockDraftFieldsAfter] {
				t.Errorf("the draft lock names the undeclared state %q", spec.lockDraftFieldsAfter)
			}
			if spec.closureState != "" && !states[spec.closureState] {
				t.Errorf("the closure state %q is not declared", spec.closureState)
			}
			// A type with closure artefacts MUST name a closure state, or
			// eoRequireAtClosureState is never called for it and its
			// `at_state` fields stay required at no state.
			if len(eoClosureArtefactFieldKeys(spec.typeKey)) > 0 && spec.closureState == "" {
				t.Errorf("has closure artefacts %v and names no closureState, so they would be required at no state", eoClosureArtefactFieldKeys(spec.typeKey))
			}
			if len(eoClosureArtefactFieldKeys(spec.typeKey)) == 0 && spec.closureState != "" {
				t.Errorf("names closureState %q and has no closure artefact; eoRequireAtClosureState refuses that", spec.closureState)
			}
		})
	}
}

// eoPilotWorkflowSpecCounts is how many rows the workflow step writes, taken
// FROM THE SPECS rather than written out as numbers: the whole-seed snapshot
// tests are about what else the seed does and must not fail every time a state
// is added. The tests above are what hold the machines to the matrix.
type eoPilotWorkflowSpecCount struct {
	workflows, states, transitions, requirements, fieldChanges, policies, policySteps int64
}

func eoPilotWorkflowSpecCounts() eoPilotWorkflowSpecCount {
	c := eoPilotWorkflowSpecCount{
		workflows:   int64(len(eoPilotWorkflows)),
		policies:    1,
		policySteps: int64(len(eoDirectiveEscalationDelays)),
	}
	for _, spec := range eoPilotWorkflows {
		c.states += int64(len(spec.states))
		c.transitions += int64(len(spec.transitions))
		for _, tr := range spec.transitions {
			if tr.requireComment {
				c.requirements++
			}
			// One field change per closure artefact, on every transition
			// that enters the closure state.
			if spec.closureState != "" && tr.to == spec.closureState {
				c.fieldChanges += int64(len(eoClosureArtefactFieldKeys(spec.typeKey)))
			}
		}
	}
	return c
}

// ── Task A7: the spawn_record actions, as data ──────────────────────────────
//
// WHAT A TEST OVER HERE CAN AND CANNOT SHOW. These read the seeded rows and
// the field definitions they name; the one that shows a Directive REALLY
// creating a Task is services.TestEoDirectiveSpawnsATaskOnItsTransition,
// because package database cannot import package services. The division is
// Task A4's and A5's, and this task follows it.

// eoSeededSpawn is one seeded spawn_record action with the configuration
// around it already resolved: which type carries the transition, and what the
// action's config says.
type eoSeededSpawn struct {
	sourceTypeKey  string
	transitionCode string
	action         models.TransitionAction
	cfg            eoSpawnConfig
}

// eoSeededSpawns reads back every spawn_record action on the pilot's
// workflows, by the action type the engine's dispatch switch matches.
func eoSeededSpawns(t *testing.T, db *gorm.DB) []eoSeededSpawn {
	t.Helper()
	var out []eoSeededSpawn
	got := eoSeededWorkflows(t, db)
	for _, typeKey := range eoPilotTypeKeys {
		w := got[typeKey]
		for _, tr := range w.transitions {
			var actions []models.TransitionAction
			if err := db.Where("transition_id = ? AND action_type = ?", tr.ID, eoSpawnRecordActionType).
				Order("execution_order").Find(&actions).Error; err != nil {
				t.Fatalf("read the actions of %q: %v", tr.Code, err)
			}
			for _, a := range actions {
				var cfg eoSpawnConfig
				if err := json.Unmarshal([]byte(a.Config), &cfg); err != nil {
					t.Fatalf("%s.%s: spawn config %q does not decode: %v", typeKey, tr.Code, a.Config, err)
				}
				out = append(out, eoSeededSpawn{sourceTypeKey: typeKey, transitionCode: tr.Code, action: a, cfg: cfg})
			}
		}
	}
	return out
}

// eoReadEffectiveFieldDefs is the test's own resolution of a type's effective
// field set, written out of the inheritance chain rather than borrowed from
// the seed, so that a bug in eoEffectiveFieldDefs cannot hide itself here.
func eoReadEffectiveFieldDefs(t *testing.T, db *gorm.DB, typeKey string) map[string]models.EOFieldDef {
	t.Helper()
	type row struct {
		models.EOFieldDef
		Depth int
	}
	var rows []row
	err := db.Raw(`
		WITH RECURSIVE chain AS (
			SELECT id, parent_type_id, 0 AS depth FROM eo_record_types WHERE key = ? AND deleted_at IS NULL
			UNION ALL
			SELECT p.id, p.parent_type_id, c.depth + 1 FROM eo_record_types p
			JOIN chain c ON p.id = c.parent_type_id WHERE p.deleted_at IS NULL
		)
		SELECT f.*, c.depth FROM eo_field_defs f JOIN chain c ON c.id = f.record_type_id
		WHERE f.deleted_at IS NULL ORDER BY c.depth DESC`, typeKey).Scan(&rows).Error
	if err != nil {
		t.Fatalf("read the effective fields of %q: %v", typeKey, err)
	}
	if len(rows) == 0 {
		t.Fatalf("record type %q has no field definitions at all", typeKey)
	}
	out := map[string]models.EOFieldDef{}
	for _, r := range rows { // farthest ancestor first, so the type's own wins
		out[r.Key] = r.EOFieldDef
	}
	return out
}

// THE TRAP THE PLAN NAMES. services.eoActionExecutor.spawn refuses a field_map
// key that the TARGET type does not define, and the refusal fails the WHOLE
// spawn — not the one key. A6 has just removed `related_refs` and B2/B3
// reserve keys, so "defined on the target type" is a moving target and this
// test asks the DATABASE AS THE SEED LEAVES IT rather than the client matrix.
//
// It also refuses an `auto` or `derived` target key, which is the same defect
// one step further on: the key IS defined, so the executor's own check passes,
// and ValidateRecordValues then rejects it with "is set by the system and
// cannot be written" — failing the whole spawn at Create instead, post-commit,
// where the only trace is a log line and a history note.
//
// Mutation: add any key to the field map of a spawn in database.eoPilotSpawnActions
// that eo_task does not define (`related_refs`), or map `origin_cff`, which it
// defines as `auto`.
func TestEoSpawnFieldMapUsesOnlyDefinedKeys(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	spawns := eoSeededSpawns(t, db)
	// The pilot configures exactly one: EO-DIR's Directive -> Task (spec §12).
	// Without this the loop below would pass over an empty set.
	if len(spawns) != 1 {
		t.Fatalf("%d spawn_record actions are seeded, want 1 (EO-DIR's Directive -> Task); spec §12 names it and §7.8 calls it the pattern that recurs in four CFFs", len(spawns))
	}
	for _, s := range spawns {
		t.Run(s.sourceTypeKey+"."+s.transitionCode, func(t *testing.T) {
			if !s.action.IsActive {
				t.Errorf("the action is inactive, so the configured spawn runs nowhere")
			}
			if strings.TrimSpace(s.cfg.TargetType) == "" || strings.TrimSpace(s.cfg.LinkType) == "" {
				t.Fatalf("config %+v names no target type or no link type; the executor refuses both", s.cfg)
			}
			if len(s.cfg.FieldMap) == 0 {
				t.Fatalf("the field map is empty, so the spawned record would carry nothing")
			}
			defs := eoReadEffectiveFieldDefs(t, db, s.cfg.TargetType)
			for key := range s.cfg.FieldMap {
				d, ok := defs[key]
				if !ok {
					t.Errorf("field_map writes %q, which %q does not define; services.eoActionExecutor.spawn fails the WHOLE spawn for it", key, s.cfg.TargetType)
					continue
				}
				if d.Provenance != models.EOProvenanceUser {
					t.Errorf("field_map writes %q, whose provenance on %q is %q; ValidateRecordValues rejects a submitted value for an auto or derived field and the whole spawn fails at Create",
						key, s.cfg.TargetType, d.Provenance)
				}
			}
		})
	}
}

// THE OTHER HALF OF THE SAME QUESTION, and the one that decides whether a
// seeded spawn FIRES AND FAILS rather than never firing. The action is
// dispatched POST-COMMIT and its failure is logged, not retried, so a map that
// leaves a required field of the target unset produces a transition that
// succeeds and a child that never exists.
//
// Two rules, both fail-closed in eoSeedOneSpawnAction as well:
//
//	every `always`-required USER field of the TARGET type is in the map; and
//	whatever fills one of those is a whole {{placeholder}} naming a field that
//	is itself `always`-required and USER-provided on the SOURCE type.
//
// The second is the spawn's form of the rule A5's review derived for
// close_parent — "a requirement at a state an action closes into must be
// satisfiable from what is already STORED on the parent". Here: a required
// field of the child must be satisfiable from a field the parent always has.
//
// Mutation: drop any entry from the Directive's field map in
// database.eoPilotSpawnActions, or point `owner` at `{{decision_reference}}`,
// which the Directive defines but does not always require.
func TestEoSpawnFieldMapFillsEveryAlwaysRequiredTargetField(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	spawns := eoSeededSpawns(t, db)
	// Its sibling has this and this one lacked it: with no spawn seeded the
	// loop below passes over an empty set and proves nothing.
	if len(spawns) != 1 {
		t.Fatalf("%d spawn_record actions are seeded, want 1 (EO-DIR's Directive -> Task)", len(spawns))
	}
	for _, s := range spawns {
		t.Run(s.sourceTypeKey+"."+s.transitionCode, func(t *testing.T) {
			targetDefs := eoReadEffectiveFieldDefs(t, db, s.cfg.TargetType)
			sourceDefs := eoReadEffectiveFieldDefs(t, db, s.sourceTypeKey)

			required := map[string]bool{}
			for key, d := range targetDefs {
				if d.Provenance == models.EOProvenanceUser && d.RequiredMode == models.EORequiredAlways {
					required[key] = true
					if _, mapped := s.cfg.FieldMap[key]; !mapped {
						t.Errorf("%q is required on every %q at creation and the field map does not fill it; the transition would commit and the spawn would fail at Create, post-commit, with nothing retried",
							key, s.cfg.TargetType)
					}
				}
			}
			if len(required) == 0 {
				t.Fatalf("%q has no always-required user field, so this test proves nothing", s.cfg.TargetType)
			}

			for key, raw := range s.cfg.FieldMap {
				expr, ok := raw.(string)
				if !ok {
					continue // a literal: it carries its own value
				}
				m := eoSpawnWholePlaceholder.FindStringSubmatch(expr)
				if m == nil {
					t.Errorf("field_map[%q] = %q; the pilot's maps are whole {{placeholders}} only, so that the check below can be made at all", key, expr)
					continue
				}
				src, defined := sourceDefs[m[1]]
				if !defined {
					t.Errorf("field_map[%q] reads {{%s}}, which %q does not define; the executor fails the WHOLE spawn on an unknown reference", key, m[1], s.sourceTypeKey)
					continue
				}
				// THE KINDS, on every mapping and not only the required
				// ones: a whole {{placeholder}} carries the source's value
				// with its own type, so a date filled from a text is refused
				// at Create, post-commit, exactly like a missing one.
				if targetDefs[key].Kind != src.Kind {
					t.Errorf("field_map[%q] fills a %q on %q from %q.%s, which is a %q; the value is carried with its own type and the spawn would be refused at Create",
						key, targetDefs[key].Kind, s.cfg.TargetType, s.sourceTypeKey, m[1], src.Kind)
				}
				if !required[key] {
					continue
				}
				if src.Provenance != models.EOProvenanceUser || src.RequiredMode != models.EORequiredAlways {
					t.Errorf("field_map[%q] fills a field %q requires, out of %q.%s, which is %s/%s rather than user/always; a %s with that field blank would commit its transition and then fail to spawn, post-commit and unretried",
						key, s.cfg.TargetType, s.sourceTypeKey, m[1], src.Provenance, src.RequiredMode, s.sourceTypeKey)
				}
			}
		})
	}
}

// THE ACTION TYPE AND ITS CONFIG KEYS ARE STRINGS SHARED WITH AN ENGINE SWITCH
// IN ANOTHER PACKAGE, and package database cannot import package services. An
// action type the executor does not know is logged as "unknown action type"
// while the transition succeeds — a configured spawn that runs nowhere, which
// is the exact shape this programme keeps catching. So this reads the
// executor's source and compares, as TestEoCloseParentActionTypeMatchesTheEngine
// does for close_parent.
//
// Mutation: rename eoSpawnRecordActionType, or the case label in
// services.eoActionExecutor.dispatch, or any json tag on services.eoSpawnConfig.
func TestEoSpawnRecordActionTypeMatchesTheEngine(t *testing.T) {
	const src = "../services/eo_action_executor.go"
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	want := fmt.Sprintf("case %q:", eoSpawnRecordActionType)
	if !strings.Contains(string(raw), want) {
		t.Fatalf("%s has no %s in its dispatch switch: an action seeded under %q would be logged as an unknown action type and the transition would succeed with no child created",
			src, want, eoSpawnRecordActionType)
	}
	for _, tag := range []string{`json:"target_type"`, `json:"link_type"`, `json:"field_map"`} {
		if !strings.Contains(string(raw), tag) {
			t.Errorf("%s does not decode %s, which the seeded config carries", src, tag)
		}
	}
}

// DECISION 3, `spawned_by_action_id`, AS A STANDING CHECK RATHER THAN A
// SENTENCE IN A REPORT. Plan 1's skip key is (parent, target type, link type),
// so two shapes of configuration would silently collapse into one child:
//
//	(a) two spawn_record actions on ONE transition naming the same target type
//	    through the same link type; and
//	(b) a record RE-ENTERING a spawning transition through a return loop.
//
// Task A7 answers "the pilot does not exercise it". This is that answer, kept
// true: (a) is counted over the seeded actions, (b) is a reachability walk
// over eoPilotWorkflows. The day either becomes false, the column is owed.
//
// Mutation: add a second spawn to the Directive's `assign_directly` in
// eoPilotSpawnActions; or add a transition from `in_execution` back to
// `recorded` in eoPilotWorkflows.
func TestEoSpawnsCannotCollapseInThePilotConfiguration(t *testing.T) {
	t.Run("no two spawns on one transition share a target type and link type", func(t *testing.T) {
		db := eoPilotDB(t)
		seedEOPilot(db)
		seen := map[string]string{}
		for _, s := range eoSeededSpawns(t, db) {
			k := s.action.TransitionID.String() + "|" + s.cfg.TargetType + "|" + s.cfg.LinkType
			if prev, dup := seen[k]; dup {
				t.Errorf("%s.%s carries both %q and %q, which name one target type through one link type; Plan 1's skip key cannot tell them apart and only one child would ever be created",
					s.sourceTypeKey, s.transitionCode, prev, s.action.Name)
			}
			seen[k] = s.action.Name
		}
	})

	t.Run("no spawning transition can be re-entered", func(t *testing.T) {
		spawning := map[string]map[string]bool{} // type key -> transition code
		for _, s := range eoPilotSpawnActions {
			if spawning[s.sourceTypeKey] == nil {
				spawning[s.sourceTypeKey] = map[string]bool{}
			}
			spawning[s.sourceTypeKey][s.transitionCode] = true
		}
		if len(spawning) == 0 {
			t.Fatalf("eoPilotSpawnActions is empty, so this test proves nothing")
		}
		for _, spec := range eoPilotWorkflows {
			codes := spawning[spec.typeKey]
			if len(codes) == 0 {
				continue
			}
			for _, tr := range spec.transitions {
				if !codes[tr.code] {
					continue
				}
				t.Run(spec.typeKey+"."+tr.code, func(t *testing.T) {
					if eoStateReaches(spec, tr.to, tr.from) {
						t.Errorf("state %q reaches %q again, so a record can re-enter %q; the second run would find the first child and skip, and nothing would be created for the second round",
							tr.to, tr.from, tr.code)
					}
				})
			}
		}
	})
}

// eoStateReaches reports whether `to` can reach `from` by following this
// workflow's transitions — a return loop, in other words.
//
// THE TARGET IS TESTED BEFORE THE VISITED SET, and that ordering is the whole
// of the self-loop case. `start` is marked visited before the walk begins, so
// testing `seen` first would make a transition with from == to — the shortest
// return loop there is, and a record re-entering a spawning transition on the
// spot — report "cannot be re-entered". No pilot transition is a self-loop
// today; this function exists to catch the configuration that does not exist
// yet, which is exactly where one would come from.
func eoStateReaches(spec eoWorkflowSpec, start, target string) bool {
	seen := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, tr := range spec.transitions {
			if tr.from != cur {
				continue
			}
			if tr.to == target {
				return true
			}
			if seen[tr.to] {
				continue
			}
			seen[tr.to] = true
			queue = append(queue, tr.to)
		}
	}
	return false
}

// eoStateReaches is driven by the seeded specs, where no self-loop and no
// return loop exists — so the branch that matters has never run. This gives it
// three hand-made machines: the self-loop the ordering above is for, a longer
// return loop, and the forward DAG that must still come out false.
//
// Mutation: in eoStateReaches, test `seen[tr.to]` before `tr.to == target`.
func TestEoStateReachesFindsAReturnLoop(t *testing.T) {
	tr := func(code, from, to string) eoTransitionSpec {
		return eoTransitionSpec{code: code, from: from, to: to}
	}
	for _, tc := range []struct {
		name        string
		transitions []eoTransitionSpec
		start, want string
		reaches     bool
	}{
		{"a self-loop on the spawning transition",
			[]eoTransitionSpec{tr("rework", "open", "open")}, "open", "open", true},
		{"a return loop two states long",
			[]eoTransitionSpec{tr("a", "one", "two"), tr("b", "two", "three"), tr("c", "three", "one")}, "two", "one", true},
		{"a forward DAG with two paths to one state",
			[]eoTransitionSpec{tr("a", "one", "two"), tr("b", "one", "three"), tr("c", "two", "three")}, "three", "one", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := eoWorkflowSpec{typeKey: "eo_fake", transitions: tc.transitions}
			if got := eoStateReaches(spec, tc.start, tc.want); got != tc.reaches {
				t.Errorf("eoStateReaches(%q, %q) = %v, want %v", tc.start, tc.want, got, tc.reaches)
			}
		})
	}
}

// The spawn actions are their OWN step of the pilot seed, not an extension of
// seed_workflows: that step's applied row already exists on every database
// that has booted Task A5, so an action folded into it would never be written
// there — configuration that is complete on a fresh database and absent on
// every real one. The key is a permanent identifier like every other.
//
// The second half is the same per-row idempotency the workflow step has: with
// the applied row removed the step runs again and must converge.
//
// Mutation: remove the step from eoPilotSteps; or drop the `existing > 0`
// early return from eoSeedOneSpawnAction.
func TestEoSpawnSeedingIsAStepOfThePilotSeedAndConverges(t *testing.T) {
	found := false
	for _, st := range eoPilotSteps {
		if st.key == eoSeedSpawnActionsStepKey {
			found = true
		}
	}
	if !found {
		keys := make([]string, len(eoPilotSteps))
		for i, st := range eoPilotSteps {
			keys[i] = st.key
		}
		t.Fatalf("eoPilotSteps is %v and has no %q step; the spawn actions would be seeded on a fresh database only, and never on one that has already booted Task A5", keys, eoSeedSpawnActionsStepKey)
	}

	db := eoPilotDB(t)
	seedEOPilot(db)
	before := eoWorkflowCounts(t, db)
	if err := db.Where("key = ?", eoSeedSpawnActionsStepKey).Delete(&models.EOSeedStep{}).Error; err != nil {
		t.Fatalf("remove the applied-step row: %v", err)
	}
	seedEOPilot(db)
	if after := eoWorkflowCounts(t, db); before != after {
		t.Errorf("a re-run without the applied row changed the configuration: before %+v, after %+v", before, after)
	}
}

// THE FAIL-CLOSED GUARDS, EXERCISED. eoCheckSpawnFieldMap and
// eoCheckSpawnLinkType are the seed's refusals, and with the real registry
// every one of their branches is unreachable — which is how a guard ends up
// present, plausible and incapable of firing. These drive them with
// deliberately broken specs against the REAL seeded configuration, so each
// refusal is shown to refuse and to say why.
//
// Each case is one edit away from the shipped spec, and the message asserted
// is the one an operator would read out of a rolled-back boot.
func TestEoSpawnSeedRefusesAMisconfiguredSpawn(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	// The shipped spec, copied so a case can break one thing about it.
	base := func(edit func(*eoSpawnSpec)) eoSpawnSpec {
		s := eoPilotSpawnActions[0]
		fm := make(map[string]string, len(s.fieldMap))
		for k, v := range s.fieldMap {
			fm[k] = v
		}
		s.fieldMap = fm
		if edit != nil {
			edit(&s)
		}
		return s
	}

	t.Run("the field map", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			edit func(*eoSpawnSpec)
			want string
		}{
			{"the shipped spec is accepted", nil, ""},
			{"an empty map", func(s *eoSpawnSpec) { s.fieldMap = map[string]string{} },
				"empty field map"},
			{"a required field of the target left unfilled", func(s *eoSpawnSpec) { delete(s.fieldMap, "owner") },
				"does not fill owner"},
			{"a key the target type does not define", func(s *eoSpawnSpec) { s.fieldMap["related_refs"] = "{{directive_text}}" },
				`writes "related_refs", which "eo_task" does not define`},
			{"a key the target type defines as auto", func(s *eoSpawnSpec) { s.fieldMap["origin_cff"] = "{{directive_text}}" },
				`whose provenance on "eo_task" is "auto"`},
			{"a literal instead of a placeholder", func(s *eoSpawnSpec) { s.fieldMap["task_text"] = "do the thing" },
				"whole {{placeholders}}"},
			{"a placeholder the source type does not define", func(s *eoSpawnSpec) { s.fieldMap["task_text"] = "{{nope}}" },
				`from {{nope}}, which "eo_directive" does not define`},
			// `assigned_user_id` and not `decision_reference`: it is the one
			// optional field on the Directive of the SAME KIND as `owner`,
			// so this case tests the requiredness rule and nothing else. A
			// text source would be refused by the kind rule first and this
			// case would pass for the wrong reason.
			{"a required field filled from an optional one", func(s *eoSpawnSpec) { s.fieldMap["owner"] = "{{assigned_user_id}}" },
				"rather than user/always"},
			{"a field filled from a source of another kind", func(s *eoSpawnSpec) { s.fieldMap["due_date"] = "{{directive_text}}" },
				`from "eo_directive".directive_text, which is "long_text"`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				err := eoCheckSpawnFieldMap(db, base(tc.edit))
				if tc.want == "" {
					if err != nil {
						t.Fatalf("the shipped spec was refused: %v", err)
					}
					return
				}
				if err == nil {
					t.Fatalf("accepted, and the spawn would then fail at Create after the commit with nothing retried")
				}
				if !strings.Contains(err.Error(), tc.want) {
					t.Errorf("refused with %q, which does not contain %q", err, tc.want)
				}
			})
		}
	})

	t.Run("the link type", func(t *testing.T) {
		directiveID, err := eoRecordTypeID(db, eoDirectiveTypeKey)
		if err != nil {
			t.Fatalf("read the directive type: %v", err)
		}
		taskID, err := eoRecordTypeID(db, eoTaskTypeKey)
		if err != nil {
			t.Fatalf("read the task type: %v", err)
		}
		for _, tc := range []struct {
			name string
			edit func(*eoSpawnSpec)
			want string
		}{
			{"the shipped spec is accepted", nil, ""},
			{"a link type nothing seeded", func(s *eoSpawnSpec) { s.linkTypeKey = "eo_task_parent_v2" },
				"which is not seeded"},
			{"a link type whose source is another type", func(s *eoSpawnSpec) { s.linkTypeKey = eoReferralLinkKey },
				"must be its only source"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				err := eoCheckSpawnLinkType(db, base(tc.edit), directiveID, taskID)
				if tc.want == "" {
					if err != nil {
						t.Fatalf("the shipped spec was refused: %v", err)
					}
					return
				}
				if err == nil {
					t.Fatalf("accepted, and services.ValidateLink would then refuse every spawn, post-commit")
				}
				if !strings.Contains(err.Error(), tc.want) {
					t.Errorf("refused with %q, which does not contain %q", err, tc.want)
				}
			})
		}

		// The branch that needs a DIFFERENT spawn to reach: a link type whose
		// targets name neither the source type nor the `any` sentinel. The
		// Objection's link type is the one seeded pair that has that shape —
		// an objection hangs off a Draft Decision Study and off nothing else.
		t.Run("a link type that does not admit this parent", func(t *testing.T) {
			objectionID, err := eoRecordTypeID(db, eoObjectionTypeKey)
			if err != nil {
				t.Fatalf("read the objection type: %v", err)
			}
			spec := base(func(s *eoSpawnSpec) {
				s.targetTypeKey, s.linkTypeKey = eoObjectionTypeKey, eoObjectionLinkKey
			})
			err = eoCheckSpawnLinkType(db, spec, directiveID, objectionID)
			if err == nil {
				t.Fatalf("a directive was allowed to spawn an objection through %q, whose only target is the draft study", eoObjectionLinkKey)
			}
			if !strings.Contains(err.Error(), "would refuse every spawn") {
				t.Errorf("refused with %q, which does not name the consequence", err)
			}
		})
	})

	// And the seed function above them, which is what a boot actually runs:
	// a transition code no workflow carries is an action attached to nothing.
	t.Run("a transition code the source type's workflow does not have", func(t *testing.T) {
		err := eoSeedOneSpawnAction(db, base(func(s *eoSpawnSpec) { s.transitionCode = "assign_indirectly" }))
		if err == nil {
			t.Fatalf("accepted a transition code no workflow carries; the action would be attached to nothing")
		}
		if !strings.Contains(err.Error(), "want exactly 1") {
			t.Errorf("refused with %q, which does not say that the transition was not found", err)
		}
	})
	t.Run("a record type the pilot does not seed", func(t *testing.T) {
		err := eoSeedOneSpawnAction(db, base(func(s *eoSpawnSpec) { s.targetTypeKey = "eo_protocol_task" }))
		if err == nil {
			t.Fatalf("accepted a target type nothing seeded")
		}
		if !strings.Contains(err.Error(), "is not seeded") {
			t.Errorf("refused with %q, which does not say the type is missing", err)
		}
	})

	// THE TWO GUARDS' CALL SITES, which every case above leaves unpinned
	// because they drive eoCheckSpawnFieldMap and eoCheckSpawnLinkType
	// DIRECTLY. Deleting the two lines that call them from
	// eoSeedOneSpawnAction leaves a suite that is otherwise entirely green:
	// the helpers would still refuse everything they are asked about, and
	// nothing would ask them. These two cases go through the function a boot
	// actually runs, so the wiring is the thing under test and not the rule.
	//
	// Mutation: delete `if err := eoCheckSpawnFieldMap(tx, spec); err != nil
	// { return err }` from eoSeedOneSpawnAction, or its eoCheckSpawnLinkType
	// twin.
	t.Run("the seed calls the field-map guard", func(t *testing.T) {
		err := eoSeedOneSpawnAction(db, base(func(s *eoSpawnSpec) { delete(s.fieldMap, "owner") }))
		if err == nil {
			t.Fatalf("the seed accepted a map that does not fill a required field of the target; eoCheckSpawnFieldMap refuses it, so nothing is calling it")
		}
		if !strings.Contains(err.Error(), "does not fill owner") {
			t.Errorf("refused with %q, which is not the field-map guard's refusal", err)
		}
	})
	t.Run("the seed calls the link-type guard", func(t *testing.T) {
		err := eoSeedOneSpawnAction(db, base(func(s *eoSpawnSpec) { s.linkTypeKey = eoReferralLinkKey }))
		if err == nil {
			t.Fatalf("the seed accepted a link type whose source is another record type; eoCheckSpawnLinkType refuses it, so nothing is calling it")
		}
		if !strings.Contains(err.Error(), "must be its only source") {
			t.Errorf("refused with %q, which is not the link-type guard's refusal", err)
		}
	})
}

// MINOR 5: THE ONE PROPERTY OF eoEffectiveFieldDefs NOTHING ELSE ASSERTS.
// Its contract is that a type's OWN definition replaces an ancestor's of the
// same key — spec §7.2's resolution, the one services.ResolveEffectiveFields
// performs. If the chain were walked the other way round, `due_date` on
// eo_task would resolve to the BASE definition, which is required `never`;
// eoCheckSpawnFieldMap would then quietly stop demanding it, and no other A7
// test would notice, because a map that fills MORE than the required set is
// not an error. The services test proves the map carries `due_date`; it does
// not prove the resolution order that makes it mandatory.
//
// The base's own value is asserted too, so this cannot pass by both
// definitions happening to agree.
//
// Mutation: in eoEffectiveFieldDefs, change the chain query's
// `ORDER BY depth DESC` to `ORDER BY depth`.
func TestEoEffectiveFieldDefsPrefersTheTypesOwnDefinition(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	taskID, err := eoRecordTypeID(db, eoTaskTypeKey)
	if err != nil {
		t.Fatalf("read the task type: %v", err)
	}
	defs, err := eoEffectiveFieldDefs(db, eoTaskTypeKey)
	if err != nil {
		t.Fatalf("resolve the effective fields of %q: %v", eoTaskTypeKey, err)
	}

	// The ancestor's value, so the comparison below is a real one.
	var base models.EOFieldDef
	if err := db.Raw(`
		SELECT f.* FROM eo_field_defs f
		JOIN eo_record_types t ON t.id = f.record_type_id
		JOIN eo_record_types child ON child.parent_type_id = t.id
		WHERE child.key = ? AND f.key = 'due_date' AND f.deleted_at IS NULL`, eoTaskTypeKey).
		Scan(&base).Error; err != nil {
		t.Fatalf("read the base type's due_date: %v", err)
	}
	if base.ID == uuid.Nil {
		t.Fatalf("the base type defines no due_date, so there is no override to prefer")
	}
	if base.RequiredMode != models.EORequiredNever {
		t.Fatalf("the base type's due_date is %q, not %q; this test can no longer tell the two definitions apart", base.RequiredMode, models.EORequiredNever)
	}

	got, ok := defs["due_date"]
	if !ok {
		t.Fatalf("due_date is absent from the effective fields of %q", eoTaskTypeKey)
	}
	if got.RecordTypeID != taskID {
		t.Errorf("due_date resolved to the definition on record type %s, want %q's own (%s); the ancestor chain is being walked nearest-last",
			got.RecordTypeID, eoTaskTypeKey, taskID)
	}
	if got.RequiredMode != models.EORequiredAlways {
		t.Errorf("due_date resolves to required_mode %q on %q, want %q; the Task's narrowing is being overwritten by the base's, and eoCheckSpawnFieldMap would stop demanding the one field that makes the spawn's clock work",
			got.RequiredMode, eoTaskTypeKey, models.EORequiredAlways)
	}
}
