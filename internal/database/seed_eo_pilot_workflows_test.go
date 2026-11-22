package database

import (
	"encoding/json"
	"fmt"
	"os"
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
			if !strings.Contains(r.Why, strings.TrimSuffix(a.MatrixWhy, ".")) {
				t.Errorf("the decision quotes the Why cell as %q; the matrix says %q", a.MatrixWhy, r.Why)
			}
			if strings.TrimSpace(a.Why) == "" {
				t.Errorf("no reason is recorded")
			}
			def := eoEffectiveField(t, db, a.TypeKey, a.FieldKey)
			if def.RequiredMode != models.EORequiredAtState {
				t.Errorf("seeded %q, a resolved closure artefact must be %q", def.RequiredMode, models.EORequiredAtState)
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
// Mutation: rename eoCloseParentActionType, or rename the case label in
// services.eoActionExecutor.dispatch.
func TestEoCloseParentActionTypeMatchesTheEngine(t *testing.T) {
	const src = "../services/eo_action_executor.go"
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	want := fmt.Sprintf("case %q:", eoCloseParentActionType)
	if !strings.Contains(string(raw), want) {
		t.Fatalf("%s has no %s in its dispatch switch: an action seeded under %q would be logged as an unknown action type and the transition would succeed with the parent left open",
			src, want, eoCloseParentActionType)
	}
	// And the config keys the executor decodes are the ones the seed writes.
	for _, tag := range []string{`json:"link_type"`, `json:"transition"`} {
		if !strings.Contains(string(raw), tag) {
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

// Every terminal transition carries a mandatory comment requirement, so a
// statutory register never closes a record with no note saying why — except
// the two a close_parent ACTION runs, where there is no person at a form to
// type one and the requirement would make the client's "Closes the parent"
// fail every time.
//
// Mutation: drop `requireComment` from any terminal transition; or add it to
// the Draft Decision Study's or Incoming Correspondence's `close`.
func TestEoTerminalTransitionsRequireANoteUnlessAnActionRunsThem(t *testing.T) {
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
		t.Fatalf("no transition is run by an action, so the exemption below is untested")
	}

	checked := 0
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
			exempt := actionRun[typeKey+"."+tr.Code]
			if exempt && n > 0 {
				t.Errorf("%s.%s is run by a close_parent action and requires a comment, which no action can supply", typeKey, tr.Code)
			}
			if !exempt && n == 0 {
				t.Errorf("%s.%s ends a record in a statutory register and requires no note", typeKey, tr.Code)
			}
		}
	}
	if checked == 0 {
		t.Fatalf("no terminal transition was found")
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
		var zero models.EORecordType
		_ = zero
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

type eoWorkflowCount struct{ workflows, states, transitions, requirements, actions, policySteps int64 }

func eoWorkflowCounts(t *testing.T, db *gorm.DB) eoWorkflowCount {
	t.Helper()
	return eoWorkflowCount{
		workflows:    eoCountRows(t, db, &models.Workflow{}),
		states:       eoCountRows(t, db, &models.WorkflowState{}),
		transitions:  eoCountRows(t, db, &models.WorkflowTransition{}),
		requirements: eoCountRows(t, db, &models.TransitionRequirement{}),
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
