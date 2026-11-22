package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/automax/backend/internal/database"
	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
)

// ── Task A5's state machines, in front of the engine that runs them ─────────
//
// WHY THESE TESTS LIVE HERE. Every rule this task arms is read by a function
// in THIS package — ValidateRecordValues (the at-closure requirement and the
// §7.12 field lock), BlockingChildren (the objection's hold on the raise),
// eoActionExecutor (the "Closes the parent" action), ScheduleStateSLA (the
// 3/7/14 ladder) — and package database cannot import package services. A test
// over there can only assert that a column holds a value somebody typed into
// the seed; THESE are the ones that show a seeded rule refusing something.
//
// NOTHING IS DEACTIVATED HERE. eo_pilot_links_test.go deactivates A5's
// machines so its link-rule tests can use a two-transition scaffold; this file
// uses the SEEDED ones, through database.SeedEOPilot, and walks each record
// along them by transition CODE.

// eoWfFixture is the seeded configuration plus the whole service stack, wired
// exactly as cmd/server/main.go wires it — INCLUDING SetTransitions, without
// which the close_parent action has no way to move a parent and fails.
type eoWfFixture struct {
	db       *gorm.DB
	records  EORecordService
	transits EOTransitionService
	repo     repository.EORecordRepository
	types    repository.EORecordTypeRepository
	wfRepo   repository.WorkflowRepository
	actor    *models.User
	bodyID   uuid.UUID
	typeIDs  map[string]uuid.UUID
}

func eoWfNewFixture(t *testing.T) *eoWfFixture {
	t.Helper()
	db := eoServicesTestDB(t)

	for _, r := range []models.Role{
		{Name: "Administrator", Code: "admin", IsSystem: true, IsActive: true},
		{Name: "Executive Office", Code: "executive_office", IsSystem: true, IsActive: true},
	} {
		if err := db.Create(&r).Error; err != nil {
			t.Fatalf("create role %s: %v", r.Code, err)
		}
	}
	database.SeedEOPlatform(db)
	// The pilot seed's own bookkeeping table; eoE2ETables does not list it
	// because it is the SEED's, not the engine's storage.
	if err := db.AutoMigrate(&models.EOSeedStep{}); err != nil {
		t.Fatalf("create the pilot seed's applied-step table: %v", err)
	}
	database.SeedEOPilot(db)

	body := models.Department{Name: "Executive Office", Code: "EO", IsActive: true}
	if err := db.Create(&body).Error; err != nil {
		t.Fatalf("create owning body: %v", err)
	}
	var eoRole models.Role
	if err := db.Where("code = ?", "executive_office").First(&eoRole).Error; err != nil {
		t.Fatalf("read the executive_office role: %v", err)
	}
	actor := &models.User{
		ID: uuid.New(), Email: "eo.wf@example.test", Username: "eo_wf",
		Password: "x", FirstName: "Office", LastName: "Officer", IsActive: true,
	}
	if err := db.Omit("Roles", "Departments", "Locations", "Classifications").Create(actor).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	actor.Roles = []models.Role{eoRole}

	f := &eoWfFixture{
		db:      db,
		repo:    repository.NewEORecordRepository(db),
		types:   repository.NewEORecordTypeRepository(db),
		wfRepo:  repository.NewWorkflowRepository(db),
		actor:   actor,
		bodyID:  body.ID,
		typeIDs: map[string]uuid.UUID{},
	}
	f.records = NewEORecordServiceWithSLA(f.repo, f.types, repository.NewEOSequenceRepository(db), f.wfRepo, repository.NewEscalationPolicyRepository(db))
	exec := NewEOActionExecutor(f.records, f.repo, f.types, repository.NewUserRepository(db), repository.NewDepartmentRepository(db), nil)
	f.transits = NewEOTransitionServiceWithSLA(f.repo, f.types, f.wfRepo, exec, repository.NewEscalationPolicyRepository(db))
	// THE LINE main.go CARRIES. Without it the close_parent action has no
	// transition service and fails on every record, loudly.
	exec.SetTransitions(f.transits)

	for _, key := range eoPilotTypeKeysUnderTest {
		ty, err := f.types.GetByKey(context.Background(), key)
		if err != nil || ty == nil {
			t.Fatalf("the pilot seed did not create record type %q: %v", key, err)
		}
		f.typeIDs[key] = ty.ID
	}
	return f
}

// eoWfValues is a submission satisfying everything the SEEDED configuration
// requires AT CREATION of typeKey — required_mode `always` and provenance
// `user` — and nothing else. A field the matrix marks "Yes at closure", or
// that Task A5 resolved as a closure artefact, is `at_state` and so is
// deliberately absent: that absence is what several tests below are about.
func (f *eoWfFixture) eoWfValues(t *testing.T, typeKey string) map[string]any {
	t.Helper()
	var defs []models.EOFieldDef
	err := f.db.Raw(`
		WITH RECURSIVE chain AS (
			SELECT id, parent_type_id FROM eo_record_types WHERE key = ?
			UNION ALL
			SELECT p.id, p.parent_type_id FROM eo_record_types p JOIN chain c ON p.id = c.parent_type_id
		)
		SELECT * FROM eo_field_defs WHERE record_type_id IN (SELECT id FROM chain) AND deleted_at IS NULL`, typeKey).
		Scan(&defs).Error
	if err != nil {
		t.Fatalf("read the field definitions of %q: %v", typeKey, err)
	}
	if len(defs) == 0 {
		t.Fatalf("record type %q has no field definitions", typeKey)
	}
	out := map[string]any{}
	for _, d := range defs {
		if d.Provenance != models.EOProvenanceUser || d.RequiredMode != models.EORequiredAlways {
			continue
		}
		switch d.Key {
		case "owning_body_id":
			out[d.Key] = f.bodyID.String()
		case "due_date":
			out[d.Key] = time.Now().Add(30 * 24 * time.Hour).Format(time.RFC3339)
		default:
			out[d.Key] = "x"
		}
	}
	out["subject_ar"] = "اختبار"
	return out
}

func (f *eoWfFixture) create(t *testing.T, typeKey string, parent *models.EORecord, linkTypeKey string, overrides map[string]any) *models.EORecord {
	t.Helper()
	values := f.eoWfValues(t, typeKey)
	for k, v := range overrides {
		values[k] = v
	}
	req := &models.EORecordCreateRequest{RecordTypeID: f.typeIDs[typeKey], Values: values}
	if parent != nil {
		req.ParentRecordID = &parent.ID
		req.ParentLinkTypeKey = linkTypeKey
	}
	rec, err := f.records.Create(context.Background(), req, f.actor)
	if err != nil {
		t.Fatalf("create %s: %v", typeKey, err)
	}
	return rec
}

// transitionID finds the id of the transition with this CODE out of the
// record's current state, on the record's own seeded workflow.
func (f *eoWfFixture) transitionID(t *testing.T, rec *models.EORecord, code string) uuid.UUID {
	t.Helper()
	available, err := f.transits.AvailableTransitions(context.Background(), rec, f.actor)
	if err != nil {
		t.Fatalf("available transitions on %s: %v", rec.Reference, err)
	}
	codes := make([]string, len(available))
	for i := range available {
		codes[i] = available[i].Code
		if available[i].Code == code {
			return available[i].ID
		}
	}
	t.Fatalf("record %s offers %v out of its current state, not %q", rec.Reference, codes, code)
	return uuid.Nil
}

// try runs one transition by code and returns its error.
func (f *eoWfFixture) try(t *testing.T, rec *models.EORecord, code string, in models.EOTransitionInput) (*models.EORecord, error) {
	t.Helper()
	if in.Comment == "" {
		in.Comment = "step"
	}
	return f.transits.Execute(context.Background(), rec, f.transitionID(t, rec, code), f.actor, in)
}

// move runs one transition by code and fails the test if it is refused.
func (f *eoWfFixture) move(t *testing.T, rec *models.EORecord, code string, in models.EOTransitionInput) *models.EORecord {
	t.Helper()
	out, err := f.try(t, rec, code, in)
	if err != nil {
		t.Fatalf("run %q on %s: %v", code, rec.Reference, err)
	}
	return out
}

func (f *eoWfFixture) reread(t *testing.T, rec *models.EORecord) *models.EORecord {
	t.Helper()
	fresh, err := f.repo.GetByID(context.Background(), rec.ID, rec.RecordTypeID)
	if err != nil || fresh == nil {
		t.Fatalf("re-read %s: %v", rec.Reference, err)
	}
	return fresh
}

// THE TWO FUNCTIONS THE BRIEF NAMES, over the seeded configuration: every
// pilot type resolves EXACTLY ONE workflow and that workflow has EXACTLY ONE
// initial state. Both error on zero and on more than one, and both are called
// on every create — so either failing makes every record of that type
// uncreatable. A record is then created through the real pipeline to show the
// resolution is not merely well-formed.
//
// Mutation: in database.eoPilotWorkflows, give two types the same workflow
// (duplicate a typeKey), or make a second state `initial`.
func TestEoPilotEveryTypeResolvesOneWorkflowWithOneInitialState(t *testing.T) {
	f := eoWfNewFixture(t)
	ctx := context.Background()

	for _, typeKey := range eoPilotTypeKeysUnderTest {
		t.Run(typeKey, func(t *testing.T) {
			wf, err := ResolveWorkflowForType(ctx, f.typeIDs[typeKey], f.wfRepo)
			if err != nil {
				t.Fatalf("ResolveWorkflowForType: %v", err)
			}
			initial, err := InitialState(wf)
			if err != nil {
				t.Fatalf("InitialState of %q: %v", wf.Code, err)
			}
			terminal := 0
			for i := range wf.States {
				if IsTerminal(&wf.States[i]) {
					terminal++
				}
			}
			if terminal == 0 {
				t.Errorf("workflow %q has no terminal state, so no record of this type could be closed", wf.Code)
			}
			// The three ROOT types are the only ones that can be created
			// without a parent; the rest are checked by the tests below,
			// each of which builds its own hierarchy.
			switch typeKey {
			case eoPilotDirectiveTypeKey, eoPilotDraftTypeKey, eoPilotIncomingTypeKey:
				rec := f.create(t, typeKey, nil, "", nil)
				if rec.StateID == nil || *rec.StateID != initial.ID {
					t.Errorf("a new %s is in state %v, want the initial state %q (%v)", typeKey, rec.StateID, initial.Code, initial.ID)
				}
			}
		})
	}
}

// THE "Yes at closure" REQUIREMENT, REFUSING A CLOSURE. A3 seeded Assignment's
// and Task's `evidence` `at_state` with an EMPTY state list — "required at no
// state", a rule that cannot fire — so that A5 could point it at a real
// closing state. This closes a record both ways round: without the evidence,
// which must be refused and must NAME the field, and then with it, which must
// succeed. A test that only showed the second would pass against a rule
// pointed at nothing.
//
// The same table covers the "Yes" rows addendum 4 resolved as closure
// artefacts (`verified_by`, `verified_on`), which are required at the same
// state and for the same reason.
//
// Mutation: clear `closureState` on the Assignment's or the Task's entry in
// database.eoPilotWorkflows — the closure then succeeds with no evidence at
// all, which is what shipped before this task.
func TestEoPilotClosureIsRefusedWithoutItsClosureArtefacts(t *testing.T) {
	f := eoWfNewFixture(t)

	tests := []struct {
		name string
		// build returns a record sitting one transition short of closure.
		build func(t *testing.T, f *eoWfFixture) *models.EORecord
		close string
		// artefacts are the field keys the closure must refuse to go without.
		artefacts []string
		supply    map[string]any
	}{
		{
			name: "an assignment closes with its evidence or it does not close",
			build: func(t *testing.T, f *eoWfFixture) *models.EORecord {
				directive := f.create(t, eoPilotDirectiveTypeKey, nil, "", nil)
				a := f.create(t, eoPilotAssignmentTypeKey, directive, eoPilotAssignmentLinkKey, nil)
				a = f.move(t, a, "start", models.EOTransitionInput{})
				return f.move(t, a, "submit_evidence", models.EOTransitionInput{})
			},
			close:     "verify_and_close",
			artefacts: []string{"evidence", "verified_by", "verified_on"},
			supply: map[string]any{
				"evidence":    []any{"evidence.pdf"},
				"verified_by": uuid.New().String(),
				"verified_on": time.Now().Format(time.RFC3339),
			},
		},
		{
			name: "a task closes with its evidence or it does not close",
			build: func(t *testing.T, f *eoWfFixture) *models.EORecord {
				letter := f.create(t, eoPilotIncomingTypeKey, nil, "", nil)
				task := f.create(t, eoPilotTaskTypeKey, letter, eoPilotTaskLinkKey, nil)
				return f.move(t, task, "start", models.EOTransitionInput{})
			},
			close:     "verify_and_close",
			artefacts: []string{"evidence", "verified_by"},
			supply: map[string]any{
				"evidence":    []any{"proof.pdf"},
				"verified_by": map[string]any{"user_id": uuid.New().String(), "at": time.Now().Format(time.RFC3339)},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := tc.build(t, f)

			_, err := f.try(t, rec, tc.close, models.EOTransitionInput{})
			if err == nil {
				t.Fatalf("the record closed with none of %v supplied; the at-closure rule is pointed at no state", tc.artefacts)
			}
			var invalid *EOValidationError
			if !errors.As(err, &invalid) {
				t.Fatalf("refused, but not by field validation: %v", err)
			}
			for _, key := range tc.artefacts {
				if _, named := invalid.Fields[key]; !named {
					t.Errorf("the refusal does not name %q: %v", key, invalid.Fields)
				}
			}

			// And with them, it closes — so the rule is a requirement and
			// not a wall.
			fresh := f.reread(t, rec)
			closed, err := f.try(t, fresh, tc.close, models.EOTransitionInput{Values: tc.supply})
			if err != nil {
				t.Fatalf("the closure was refused although every artefact was supplied: %v", err)
			}
			if closed.ClosedAt == nil {
				t.Errorf("the record moved into its terminal state and closed_at was not written")
			}
		})
	}
}

// §7.12's "the original is never edited", REFUSING AN EDIT. A2 seeded
// `draft_text` and `draft_file` with no lock state, because none existed; A5
// points them at the Draft Decision Study's `submitted` state, and this shows
// the lock doing its work on both sides of that state.
//
// Mutation: clear `lockDraftFieldsAfter` on the Draft Decision Study's entry
// in database.eoPilotWorkflows.
func TestEoPilotDraftOriginalIsRefusedAnEditAfterSubmit(t *testing.T) {
	f := eoWfNewFixture(t)
	ctx := context.Background()

	study := f.create(t, eoPilotDraftTypeKey, nil, "", map[string]any{"draft_text": "the original as proposed"})

	// BEFORE the submit stamp the original is still editable: the lock is
	// "after this state", not "always".
	if _, err := f.records.Update(ctx, study.ID, &models.EORecordUpdateRequest{
		RecordTypeID: study.RecordTypeID,
		Values:       map[string]any{"draft_text": "a correction before the stamp"},
	}, f.actor); err != nil {
		t.Fatalf("the draft was refused an edit BEFORE it was submitted, so the lock is not the one §7.12 describes: %v", err)
	}

	study = f.move(t, f.reread(t, study), "submit", models.EOTransitionInput{})

	_, err := f.records.Update(ctx, study.ID, &models.EORecordUpdateRequest{
		RecordTypeID: study.RecordTypeID,
		Values:       map[string]any{"draft_text": "a rewrite of the original"},
	}, f.actor)
	if err == nil {
		t.Fatalf("the original was rewritten after the submit stamp; §7.12's \"the original is never edited\" is configured by nobody")
	}
	var invalid *EOValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("refused, but not by field validation: %v", err)
	}
	if _, named := invalid.Fields["draft_text"]; !named {
		t.Errorf("the refusal does not name draft_text: %v", invalid.Fields)
	}

	// An UNCHANGED echo is still accepted, so a client may resend a whole
	// form — the lock refuses a change, not a submission.
	if _, err := f.records.Update(ctx, study.ID, &models.EORecordUpdateRequest{
		RecordTypeID: study.RecordTypeID,
		Values:       map[string]any{"draft_text": "a correction before the stamp"},
	}, f.actor); err != nil {
		t.Errorf("an unchanged echo of the locked field was refused: %v", err)
	}
}

// ADDENDUM 1, THROUGH THE WHOLE PIPELINE AND ON THE SEEDED WORKFLOW. Task A4
// proved this rule on a workflow of its own, because none was seeded; the
// transition code was a constant both tasks read precisely so that this test
// could exist. An open objection refuses the SEEDED raise-to-H.E. and does NOT
// refuse the study's closure, which is what the matrix says and the opposite
// of what blocks_parent_closure would do.
//
// Mutation: change the `code:` of the Draft Decision Study's raise transition
// in database.eoPilotWorkflows away from eoDraftRaiseToHETransitionCode.
func TestEoPilotOpenObjectionBlocksTheSeededRaiseToHE(t *testing.T) {
	f := eoWfNewFixture(t)

	study := f.create(t, eoPilotDraftTypeKey, nil, "", nil)
	study = f.move(t, study, "submit", models.EOTransitionInput{})
	study = f.move(t, f.reread(t, study), "begin_study", models.EOTransitionInput{})

	objection := f.create(t, eoPilotObjectionTypeKey, f.reread(t, study), eoPilotObjectionLinkKey, nil)

	_, err := f.try(t, f.reread(t, study), eoPilotRaiseToHECode, models.EOTransitionInput{})
	if !errors.Is(err, ErrEOTransitionBlocked) {
		t.Fatalf("raising a study with an open objection returned %v, want ErrEOTransitionBlocked", err)
	}
	if !strings.Contains(err.Error(), objection.Reference) {
		t.Errorf("the refusal does not name the objection holding it up (%s): %v", objection.Reference, err)
	}

	// Resolving it releases the raise.
	f.move(t, objection, "resolve", models.EOTransitionInput{Comment: "dismissed"})
	if _, err := f.try(t, f.reread(t, study), eoPilotRaiseToHECode, models.EOTransitionInput{}); err != nil {
		t.Fatalf("the raise was still blocked once the objection was resolved: %v", err)
	}
}

// ADDENDUM 3: "CLOSES THE PARENT", RUNNING. The matrix says a Decision Record
// "Closes the parent" (matrix:165) and an Outgoing Reply the same
// (matrix:311). No field of models.EOLinkType closes anything, so the
// mechanism is a §7.8 transition action — and this is the test that says the
// action is wired to the engine rather than merely present in the
// configuration.
//
// IT ALSO SHOWS THE ACTION OBEYING THE OTHER RULES, which is the reason it
// runs a real transition rather than writing a state id: a letter with an open
// referral REFUSES to be closed by its reply, because Task A4's
// blocks_parent_closure is evaluated on that closure like any other.
//
// Mutation: clear `closesParentLinkType` on the Decision Record's `issue` or
// the Outgoing Reply's `send`; or remove the `case "close_parent"` arm from
// eoActionExecutor.dispatch; or delete the SetTransitions call above.
func TestEoPilotChildrenThatCloseTheirParent(t *testing.T) {
	t.Run("a decision record closes its draft study", func(t *testing.T) {
		f := eoWfNewFixture(t)

		// H.E.'s decision is a CLOSURE ARTEFACT on the study (addendum 4):
		// required at the state the close enters, so it is supplied before
		// the decision record closes it.
		study := f.create(t, eoPilotDraftTypeKey, nil, "", map[string]any{
			"he_decision":   "approved_original",
			"he_reason":     "as proposed",
			"decision_date": time.Now().Format(time.RFC3339),
		})
		study = f.move(t, study, "submit", models.EOTransitionInput{})
		study = f.move(t, f.reread(t, study), "begin_study", models.EOTransitionInput{})
		study = f.move(t, f.reread(t, study), eoPilotRaiseToHECode, models.EOTransitionInput{})
		if f.reread(t, study).ClosedAt != nil {
			t.Fatalf("the study is already closed before its decision record exists")
		}

		decision := f.create(t, eoPilotDecisionTypeKey, f.reread(t, study), eoPilotDecisionLinkKey, nil)
		f.move(t, decision, "issue", models.EOTransitionInput{Comment: "issued"})

		closed := f.reread(t, study)
		if closed.ClosedAt == nil {
			t.Fatalf("the draft study is still open after its decision record was issued; the matrix says the decision record closes it")
		}
		// The closure went through the PARENT'S OWN WORKFLOW, so it landed in
		// that workflow's terminal state and left a history row.
		wf, err := ResolveWorkflowForType(context.Background(), closed.RecordTypeID, f.wfRepo)
		if err != nil {
			t.Fatalf("resolve the study's workflow: %v", err)
		}
		found := false
		for i := range wf.States {
			if closed.StateID != nil && wf.States[i].ID == *closed.StateID && IsTerminal(&wf.States[i]) {
				found = true
			}
		}
		if !found {
			t.Errorf("the study is in state %v, which is not a terminal state of %q", closed.StateID, wf.Code)
		}
	})

	t.Run("an outgoing reply closes its letter, and an open referral still refuses", func(t *testing.T) {
		f := eoWfNewFixture(t)

		letter := f.create(t, eoPilotIncomingTypeKey, nil, "", nil)
		letter = f.move(t, letter, "analyse", models.EOTransitionInput{})
		letter = f.move(t, f.reread(t, letter), "brief", models.EOTransitionInput{})
		letter = f.move(t, f.reread(t, letter), "direct", models.EOTransitionInput{})

		referral := f.create(t, eoPilotReferralTypeKey, f.reread(t, letter), eoPilotReferralLinkKey, nil)
		letter = f.move(t, f.reread(t, letter), "consolidate", models.EOTransitionInput{})

		reply := f.create(t, eoPilotOutgoingTypeKey, f.reread(t, letter), eoPilotOutgoingLinkKey, nil)
		reply = f.move(t, reply, "approve", models.EOTransitionInput{})

		// THE REPLY SENDS, BUT THE LETTER DOES NOT CLOSE: the open referral
		// blocks the parent's closure, and the action runs a real transition
		// rather than writing a state id, so that rule applies to it too.
		f.move(t, f.reread(t, reply), "send", models.EOTransitionInput{Comment: "sent"})
		if f.reread(t, letter).ClosedAt != nil {
			t.Fatalf("the letter closed although a referral is still open; close_parent bypassed the blocking-children rule")
		}
		// And the failure is on the CHILD's history, where the person
		// looking at the reply can see it.
		var notes int64
		if err := f.db.Model(&models.EORecordHistory{}).
			Where("record_id = ? AND comment LIKE ?", reply.ID, "close_parent%").Count(&notes).Error; err != nil {
			t.Fatalf("read the reply's history: %v", err)
		}
		if notes == 0 {
			t.Errorf("close_parent failed and wrote nothing to the reply's history, so the open parent is invisible to the person looking at it")
		}

		// Close the referral and run the reply's step again: now the letter
		// closes.
		f.move(t, referral, "record_response", models.EOTransitionInput{Comment: "answered"})
		sent := f.reread(t, reply)
		var actions []models.TransitionAction
		if err := f.db.Where("action_type = ?", "close_parent").Find(&actions).Error; err != nil {
			t.Fatalf("read the close_parent actions: %v", err)
		}
		if len(actions) != 2 {
			t.Fatalf("%d close_parent actions are seeded, want 2", len(actions))
		}
		// Re-dispatch the same action by hand: the reply is already in its
		// terminal state, so the transition cannot be run a second time.
		exec := NewEOActionExecutor(f.records, f.repo, f.types, repository.NewUserRepository(f.db), repository.NewDepartmentRepository(f.db), nil)
		exec.SetTransitions(f.transits)
		var tr models.WorkflowTransition
		if err := f.db.Preload("Actions").Where("id = ?", actions[0].TransitionID).Limit(1).Find(&tr).Error; err != nil {
			t.Fatalf("read the transition: %v", err)
		}
		for _, a := range actions {
			var cfg struct {
				LinkType string `json:"link_type"`
			}
			if err := json.Unmarshal([]byte(a.Config), &cfg); err != nil {
				t.Fatalf("config: %v", err)
			}
			if cfg.LinkType != eoPilotOutgoingLinkKey {
				continue
			}
			var owner models.WorkflowTransition
			if err := f.db.Preload("Actions").Where("id = ?", a.TransitionID).Limit(1).Find(&owner).Error; err != nil {
				t.Fatalf("read the reply's send transition: %v", err)
			}
			if err := exec.ExecuteActions(context.Background(), sent, &owner, f.actor); err != nil {
				t.Fatalf("re-dispatch: %v", err)
			}
		}
		if f.reread(t, letter).ClosedAt == nil {
			t.Errorf("the letter is still open once its only referral was closed and the reply's action ran again")
		}
	})
}

// ADDENDUM 6, ARMED RATHER THAN MERELY CONFIGURED. The Directive's
// escalation_level is "at +3, +7 and +14 days past the date", and the engine
// expresses it as a state SLA plus a three-step policy. A state carrying an
// SLA that nothing schedules is the same defect as a rule matching nothing, so
// this moves a directive into the state and counts the timers the transition
// really wrote.
//
// Mutation: drop `escalationPolicy` from the `in_execution` state in
// database.eoPilotWorkflows (eoSeedPilotState refuses it), or remove a step
// from eoDirectiveEscalationDelays.
func TestEoPilotDirectiveArmsItsEscalationLadder(t *testing.T) {
	f := eoWfNewFixture(t)

	directive := f.create(t, eoPilotDirectiveTypeKey, nil, "", nil)
	var before int64
	if err := f.db.Table("eo_timers").Where("record_id = ?", directive.ID).Count(&before).Error; err != nil {
		t.Fatalf("count the timers: %v", err)
	}
	if before != 0 {
		t.Errorf("a new directive already has %d timers; the INITIAL state carries no SLA", before)
	}

	f.move(t, directive, "assign_directly", models.EOTransitionInput{})

	type timer struct {
		FireAt time.Time
	}
	var timers []timer
	if err := f.db.Table("eo_timers").
		Select("fire_at").
		Where("record_id = ? AND capability_key = ?", directive.ID, EOEscalationCapabilityKey).
		Order("fire_at").Scan(&timers).Error; err != nil {
		t.Fatalf("read the timers: %v", err)
	}
	if len(timers) != 3 {
		t.Fatalf("entering `in_execution` armed %d escalation timers, want 3 (the matrix's +3, +7 and +14 days)", len(timers))
	}
	wantDays := []float64{3, 7, 14}
	for i, tm := range timers {
		days := tm.FireAt.Sub(time.Now()).Hours() / 24
		if days < wantDays[i]-0.1 || days > wantDays[i]+0.1 {
			t.Errorf("rung %d fires in %.2f days, want %.0f", i+1, days, wantDays[i])
		}
	}
}
