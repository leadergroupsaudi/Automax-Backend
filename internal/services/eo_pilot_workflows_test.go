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
	// TASK A6 gave the seeded types capability entries, and
	// CapabilitiesFor errors on a key nothing registered — so without
	// this call every create and every transition on the Draft Decision
	// Study and on Incoming Correspondence fails. main.go registers them
	// at startup; this is the same wiring, scoped to one test.
	eoPilotRegisterCapabilities(t)
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
	out := map[string]any{}
	for _, d := range eoPilotEffectiveDefs(t, f.db, typeKey) {
		if d.Provenance != models.EOProvenanceUser || d.RequiredMode != models.EORequiredAlways {
			continue
		}
		out[d.Key] = eoPilotFieldValue(d, f.bodyID, f.actor.ID)
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
		// relaxed are field keys the matrix marks "Yes at closure" that the
		// closure must NOT demand, because they hold Documenta file ids and
		// Task B2 relaxed them to `never` while EO_DOCUMENTA_ENABLED=false.
		// They are listed rather than deleted: a demand nothing can satisfy
		// would deadlock the closure, and this is the assertion that the
		// relaxation reached the TRANSITION path and not only the create
		// path. (database.eoFileFieldsRelaxedUntilDocumenta.)
		relaxed []string
		supply  map[string]any
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
			artefacts: []string{"verified_by", "verified_on"},
			relaxed:   []string{"evidence"},
			supply: map[string]any{
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
			artefacts: []string{"verified_by"},
			relaxed:   []string{"evidence"},
			supply: map[string]any{
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
			for _, key := range tc.relaxed {
				if _, named := invalid.Fields[key]; named {
					t.Errorf("the closure demands %q, which holds a Documenta file id that no route can mint while EO_DOCUMENTA_ENABLED=false; the record would be created and then never closable: %v", key, invalid.Fields)
				}
			}

			// And with them, it closes — so the rule is a requirement and
			// not a wall. `supply` no longer carries an evidence id, so this
			// is also the proof that the closure goes through on what a real
			// officer could type in the pilot deployment.
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

	study := f.create(t, eoPilotDraftTypeKey, nil, "", map[string]any{"draft_text": "the original as proposed", "draft_file": eoRequiredFileID()})

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

	study := f.create(t, eoPilotDraftTypeKey, nil, "", map[string]any{"draft_file": eoRequiredFileID(), "study_report": eoRequiredFileID()})
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
			"draft_file":    eoRequiredFileID(),
			"study_report":  eoRequiredFileID(),
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
		letter = f.move(t, letter, "analyse", eoAnalyseInput("analyse"))
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

// THE COMPOSITION THAT HAD NO OWNER: a parentless child running a transition
// that closes a parent.
//
// Decision Record, Outgoing Reply and Task take their OWN reference sequences,
// so nothing in the engine requires them to have a parent
// (docs/superpowers/eo-plan1-carry-forward.md). Put that beside `close_parent`
// — dispatched after the commit, never retried — and a clerk who issues a
// Decision Record they never linked used to get: `issue` succeeds, the record
// reaches its terminal state and LOOKS FINISHED, the action returns "carries
// no link", and the Draft Decision Study stays open for ever. The parent-side
// history note that stands in for the missing retry cannot help here, because
// there is no parent record to write it on — the engine does not know which
// study was meant. One log line was the whole trace.
//
// The transition is refused instead, before it commits, with the remedy in
// the message. Both configured close_parent paths are covered: the Decision
// Record's `issue` and the Outgoing Reply's `send`.
//
// Mutation: delete the eoCheckCloseParentHasAParent call from
// eoTransitionService.Execute, or make it return nil for a missing link.
func TestEoPilotAParentlessChildIsRefusedItsClosingTransition(t *testing.T) {
	t.Run("a decision record with no draft study", func(t *testing.T) {
		f := eoWfNewFixture(t)
		decision := f.create(t, eoPilotDecisionTypeKey, nil, "", nil)

		_, err := f.try(t, decision, "issue", models.EOTransitionInput{Comment: "issued"})
		if err == nil {
			t.Fatal("a decision record with no parent was issued; its close_parent action had nothing to close and the study it was meant to close stays open for ever, with one log line to say so")
		}
		if !errors.Is(err, ErrEOCloseParentHasNoParent) {
			t.Fatalf("err = %v, want ErrEOCloseParentHasNoParent; the refusal must be the one that names this cause, so a caller can tell it from a requirement or a role refusal", err)
		}
		if !strings.Contains(err.Error(), eoPilotDecisionLinkKey) {
			t.Errorf("err = %v, and it does not name the link the record is missing, so a clerk cannot act on it", err)
		}
		// AND THE CHILD DID NOT MOVE. A refusal that still closed the record
		// would be the same outcome with a louder log.
		stayed := f.reread(t, decision)
		if stayed.ClosedAt != nil {
			t.Error("the decision record closed although its transition was refused")
		}
		if stayed.StateID == nil || decision.StateID == nil || *stayed.StateID != *decision.StateID {
			t.Errorf("the record left state %v for %v although its transition was refused", decision.StateID, stayed.StateID)
		}
	})

	t.Run("an outgoing reply with no letter", func(t *testing.T) {
		f := eoWfNewFixture(t)
		reply := f.create(t, eoPilotOutgoingTypeKey, nil, "", nil)

		// `approve` carries no close_parent action and must still run: the
		// refusal is scoped to the transition that closes a parent, not to
		// every transition of a parentless record.
		reply = f.move(t, reply, "approve", models.EOTransitionInput{})

		_, err := f.try(t, f.reread(t, reply), "send", models.EOTransitionInput{Comment: "sent"})
		if err == nil {
			t.Fatal("an outgoing reply with no incoming correspondence was sent; the letter it was meant to close stays open for ever")
		}
		if !errors.Is(err, ErrEOCloseParentHasNoParent) {
			t.Fatalf("err = %v, want ErrEOCloseParentHasNoParent", err)
		}
		if f.reread(t, reply).ClosedAt != nil {
			t.Error("the reply closed although its transition was refused")
		}
	})
}

// THE FAILURE THE RETRY GAP MAKES EXPENSIVE, AND WHAT STANDS IN FOR IT.
// `close_parent` passes no `Values` (see services.closeParent's note on why),
// and this same task made the Draft Decision Study's `he_decision`,
// `he_reason` and `decision_date` required AT the state its `close` enters. So
// issuing a Decision Record against a study whose decision fields are blank
// refuses the parent's closure — the `issue` still succeeds, because actions
// run after the commit and never fail the transition, and the study stays
// open. Nothing retries it.
//
// TestEoPilotChildrenThatCloseTheirParent pre-fills those three fields and so
// never meets this; this test leaves them blank ON PURPOSE. What it pins is
// that the failure is VISIBLE on both records — including the parent, which is
// the one a clerk later opens asking why it is still open, and which got
// nothing at all before this fix round.
//
// Mutation: delete the `if parent != nil { e.recordActionFailure(... parent
// ...) }` block from eoActionExecutor.executeCloseParent.
func TestEoPilotCloseParentFailureIsVisibleOnTheParent(t *testing.T) {
	f := eoWfNewFixture(t)

	// No he_decision / he_reason / decision_date: all three are required at
	// the state `close` enters.
	study := f.create(t, eoPilotDraftTypeKey, nil, "", map[string]any{"draft_file": eoRequiredFileID(), "study_report": eoRequiredFileID()})
	study = f.move(t, study, "submit", models.EOTransitionInput{})
	study = f.move(t, f.reread(t, study), "begin_study", models.EOTransitionInput{})
	study = f.move(t, f.reread(t, study), eoPilotRaiseToHECode, models.EOTransitionInput{})

	decision := f.create(t, eoPilotDecisionTypeKey, f.reread(t, study), eoPilotDecisionLinkKey, nil)
	// The child's own step SUCCEEDS: an action's failure never fails the
	// transition that dispatched it, which is the engine's contract and not
	// this task's choice.
	issued := f.move(t, decision, "issue", models.EOTransitionInput{Comment: "issued"})
	if issued.ClosedAt == nil {
		t.Fatalf("the decision record did not reach its terminal state, so this test is not exercising what it claims")
	}

	stillOpen := f.reread(t, study)
	if stillOpen.ClosedAt != nil {
		t.Fatalf("the study closed although three fields required at that state are blank; the at-closure rule did not apply to the action's transition")
	}

	for _, tc := range []struct {
		name     string
		recordID uuid.UUID
		like     string
	}{
		{"the child says its action did not close the parent", decision.ID, "close_parent action%"},
		{"THE PARENT says why it is still open", study.ID, "%close_parent%NOT CLOSED%"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var notes []string
			if err := f.db.Model(&models.EORecordHistory{}).
				Where("record_id = ? AND comment LIKE ?", tc.recordID, tc.like).
				Pluck("comment", &notes).Error; err != nil {
				t.Fatalf("read the history: %v", err)
			}
			if len(notes) == 0 {
				t.Fatalf("no history note matching %q; the failure is invisible on this record", tc.like)
			}
			// The note has to name the field that blocked it, or the clerk
			// reading it learns only that something went wrong.
			if !strings.Contains(notes[0], "he_decision") {
				t.Errorf("the note does not name the field that blocked the closure: %q", notes[0])
			}
		})
	}
}

// The idempotency the doc comment on executeCloseParent claims: an
// already-closed parent is a no-op rather than an error, so a transition
// dispatched twice does not fail the second time. Nothing tested this, which
// is the defect class this programme keeps catching — a claim in a comment
// with no caller behind it.
//
// Mutation: delete the `if parent.ClosedAt != nil { … return parent, nil }`
// early return from eoActionExecutor.closeParent — the second dispatch then
// fails, because the letter offers no `close` out of its terminal state.
func TestEoPilotCloseParentOnAnAlreadyClosedParentIsANoOp(t *testing.T) {
	f := eoWfNewFixture(t)

	letter := f.create(t, eoPilotIncomingTypeKey, nil, "", nil)
	for _, code := range []string{"analyse", "brief", "direct", "consolidate"} {
		letter = f.move(t, f.reread(t, letter), code, eoAnalyseInput(code))
	}
	reply := f.create(t, eoPilotOutgoingTypeKey, f.reread(t, letter), eoPilotOutgoingLinkKey, nil)
	reply = f.move(t, reply, "approve", models.EOTransitionInput{})
	reply = f.move(t, f.reread(t, reply), "send", models.EOTransitionInput{Comment: "sent"})

	closed := f.reread(t, letter)
	if closed.ClosedAt == nil {
		t.Fatalf("the letter did not close, so the second dispatch below proves nothing")
	}
	closedAt := *closed.ClosedAt

	// Dispatch the same action a second time by hand, as a redelivery would.
	exec := NewEOActionExecutor(f.records, f.repo, f.types, repository.NewUserRepository(f.db), repository.NewDepartmentRepository(f.db), nil)
	exec.SetTransitions(f.transits)
	var action models.TransitionAction
	if err := f.db.Where("action_type = ?", "close_parent").
		Joins("JOIN workflow_transitions t ON t.id = transition_actions.transition_id").
		Where("t.code = ?", "send").Limit(1).Find(&action).Error; err != nil {
		t.Fatalf("read the reply's close_parent action: %v", err)
	}
	if action.ID == uuid.Nil {
		t.Fatalf("the Outgoing Reply's `send` carries no close_parent action")
	}
	var owner models.WorkflowTransition
	if err := f.db.Preload("Actions").Where("id = ?", action.TransitionID).Limit(1).Find(&owner).Error; err != nil {
		t.Fatalf("read the transition: %v", err)
	}
	if err := exec.ExecuteActions(context.Background(), f.reread(t, reply), &owner, f.actor); err != nil {
		t.Fatalf("the second dispatch returned an error: %v", err)
	}

	again := f.reread(t, letter)
	if again.ClosedAt == nil || !again.ClosedAt.Equal(closedAt) {
		t.Errorf("closed_at moved from %v to %v; the second dispatch was not a no-op", closedAt, again.ClosedAt)
	}
	// And it said nothing was wrong: no failure note was written for it.
	var notes int64
	if err := f.db.Model(&models.EORecordHistory{}).
		Where("record_id = ? AND comment LIKE ?", letter.ID, "%close_parent%NOT CLOSED%").Count(&notes).Error; err != nil {
		t.Fatalf("read the letter's history: %v", err)
	}
	if notes != 0 {
		t.Errorf("the second dispatch wrote %d failure notes on a letter that was already correctly closed", notes)
	}
}

// ── Task A7: spawn_record, against the real seeded configuration ────────────
//
// THIS IS THE FIRST TIME PLAN 1'S spawn_record EXECUTOR RUNS AGAINST SEEDED
// CONFIGURATION. Spec §12 names the pilot's instance — "spawn_record |
// Directive and Visitor Direction → Task" — and §7.8 calls it "the On-air
// Commitment → Task, Visitor Direction → Task, Official Visit → Protocol Task
// pattern, which recurs in four CFFs". database.eoPilotSpawnActions puts it on
// the Directive's `assign_directly`, the Q1 = no step where the work is
// assigned with no Execution Plan to carry it.
//
// The database-side tests check the configuration as rows. These four show the
// engine doing the work, or visibly failing to.
func TestEoDirectiveSpawnsATaskOnItsTransition(t *testing.T) {
	// The values that make the mapping checkable: each is distinct, so a map
	// that filled the task from the wrong source would not pass by accident.
	origin := map[string]any{
		"directive_text": "Issue building permits within five working days",
		"priority":       "urgent",
		"sensitivity":    "confidential",
		// The Directive's `source` is derived from this (client ruling), so
		// the Task's source is too.
		"directive_origin": "weekly_meeting",
		"subject_ar":       "تقليص مدة إصدار التراخيص",
		"subject_en":       "Shorten the permit cycle",
		// assigned_user_id is the fixture's actor (eoPilotFieldValue): the
		// Task's owner comes from it since Responsible officer was removed.
	}

	// eoA7TaskChildren reads the Tasks hanging off a record, through the same
	// link the executor's own idempotency check reads.
	children := func(t *testing.T, f *eoWfFixture, parent *models.EORecord) []models.EORecord {
		t.Helper()
		taskTypeID := f.typeIDs[eoPilotTaskTypeKey]
		kids, err := f.repo.Children(context.Background(), parent.ID, &taskTypeID)
		if err != nil {
			t.Fatalf("read the tasks under %s: %v", parent.Reference, err)
		}
		return kids
	}

	t.Run("the transition creates the task, filled from the directive", func(t *testing.T) {
		f := eoWfNewFixture(t)
		directive := f.create(t, eoPilotDirectiveTypeKey, nil, "", origin)
		if n := len(children(t, f, directive)); n != 0 {
			t.Fatalf("a new directive already has %d tasks", n)
		}

		f.move(t, directive, "assign_directly", models.EOTransitionInput{})

		kids := children(t, f, directive)
		if len(kids) != 1 {
			t.Fatalf("the directive has %d tasks after `assign_directly`, want 1; the configured spawn created nothing", len(kids))
		}
		task := kids[0]
		if !strings.HasPrefix(task.Reference, "TSK-") {
			t.Errorf("the task's reference is %q, want the seeded TSK- pattern; it was not created through the real pipeline", task.Reference)
		}
		// THE LINK, not merely the parent column: the link type is what
		// carries the rules, and Create refuses a parent given without one.
		links, err := f.repo.LinksFrom(context.Background(), task.ID)
		if err != nil {
			t.Fatalf("read the task's links: %v", err)
		}
		if len(links) != 1 || links[0].LinkTypeKey != eoPilotTaskLinkKey || links[0].ToRecordID != directive.ID {
			t.Fatalf("the task's links = %+v, want one %q link to %s", links, eoPilotTaskLinkKey, directive.ID)
		}
		// The initial state of the TASK's own workflow, because it went
		// through Create and not through an insert.
		if task.StateID == nil {
			t.Errorf("the spawned task has no state")
		}

		bag := map[string]any{}
		if len(task.Values) > 0 {
			if err := json.Unmarshal(task.Values, &bag); err != nil {
				t.Fatalf("decode the task's values: %v", err)
			}
		}
		for _, tc := range []struct {
			name, got, want string
		}{
			{"task_text from directive_text", eoA7String(bag["task_text"]), origin["directive_text"].(string)},
			{"owner from assigned_user_id", eoA7String(bag["owner"]), f.actor.ID.String()},
			{"subject_ar", task.SubjectAr, origin["subject_ar"].(string)},
			{"subject_en", task.SubjectEn, origin["subject_en"].(string)},
			{"priority", task.Priority, origin["priority"].(string)},
			{"sensitivity", task.Sensitivity, origin["sensitivity"].(string)},
			{"source from directive_origin", task.Source, origin["directive_origin"].(string)},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if tc.got != tc.want {
					t.Errorf("got %q, want %q", tc.got, tc.want)
				}
			})
		}
		if task.OwningBodyID == nil || *task.OwningBodyID != f.bodyID {
			t.Errorf("the task's owning body is %v, want the directive's %s", task.OwningBodyID, f.bodyID)
		}
		// The due date is the column the clock reads, and it is the
		// directive's own: the Task's `due_date` is narrowed to Always, so a
		// spawn that did not carry it would have failed at Create.
		fresh := f.reread(t, directive)
		if task.DueDate == nil || fresh.DueDate == nil || !task.DueDate.Equal(*fresh.DueDate) {
			t.Errorf("the task's due date is %v, want the directive's %v", task.DueDate, fresh.DueDate)
		}
	})

	t.Run("the spawned task holds the directive open until it is verified", func(t *testing.T) {
		// This is what makes the spawn more than a row: eo_task_parent
		// carries "Always blocks its parent from closing" (matrix:520), so
		// the task the TRANSITION created is a real blocking child.
		f := eoWfNewFixture(t)
		directive := f.create(t, eoPilotDirectiveTypeKey, nil, "", origin)
		f.move(t, directive, "assign_directly", models.EOTransitionInput{})
		kids := children(t, f, directive)
		if len(kids) != 1 {
			t.Fatalf("the directive has %d tasks, want 1", len(kids))
		}
		task := &kids[0]

		d := f.move(t, f.reread(t, directive), "submit_for_verification", models.EOTransitionInput{})
		_, err := f.try(t, f.reread(t, d), "close", models.EOTransitionInput{
			Comment: "checked",
			Values:  map[string]any{"verification_notes": "sampled ten permits"},
		})
		if err == nil {
			t.Fatalf("the directive closed with its spawned task still open; the task the spawn created blocks nothing")
		}

		task = f.move(t, task, "start", models.EOTransitionInput{})
		f.move(t, f.reread(t, task), "verify_and_close", models.EOTransitionInput{
			Comment: "verified",
			Values:  map[string]any{"evidence": []any{uuid.NewString()}, "verified_by": "officer-99"},
		})
		if _, err := f.try(t, f.reread(t, d), "close", models.EOTransitionInput{
			Comment: "checked",
			Values:  map[string]any{"verification_notes": "sampled ten permits"},
		}); err != nil {
			t.Fatalf("the directive still refuses to close once its spawned task is closed: %v", err)
		}
	})

	t.Run("a second dispatch does not spawn a second task", func(t *testing.T) {
		// Plan 1's skip key is (parent, target type, link type) and it has no
		// schema behind it, so this is the property decision 3 turns on.
		f := eoWfNewFixture(t)
		directive := f.create(t, eoPilotDirectiveTypeKey, nil, "", origin)
		f.move(t, directive, "assign_directly", models.EOTransitionInput{})

		var action models.TransitionAction
		if err := f.db.Where("action_type = ?", "spawn_record").Limit(1).Find(&action).Error; err != nil {
			t.Fatalf("read the spawn action: %v", err)
		}
		if action.ID == uuid.Nil {
			t.Fatalf("no spawn_record action is seeded")
		}
		var tr models.WorkflowTransition
		if err := f.db.Preload("Actions").Where("id = ?", action.TransitionID).Limit(1).Find(&tr).Error; err != nil {
			t.Fatalf("read the transition: %v", err)
		}
		exec := NewEOActionExecutor(f.records, f.repo, f.types, repository.NewUserRepository(f.db), repository.NewDepartmentRepository(f.db), nil)
		exec.SetTransitions(f.transits)
		if err := exec.ExecuteActions(context.Background(), f.reread(t, directive), &tr, f.actor); err != nil {
			t.Fatalf("re-dispatch: %v", err)
		}
		if n := len(children(t, f, directive)); n != 1 {
			t.Errorf("the directive has %d tasks after the action ran twice, want 1", n)
		}
	})

	t.Run("a spawn that fails is written to the directive's own history", func(t *testing.T) {
		// THE ANSWER TO "CAN A SPAWN FAIL THE WAY close_parent CAN": yes. The
		// dispatch is post-commit, the error is logged and nothing retries,
		// so the transition succeeds and the task never appears. What the
		// operator sees is this note, on the record they are looking at.
		//
		// The failure is forced the least artificial way available: the
		// target type is deactivated, which is a thing an administrator can
		// do in the designer, and eoRecordService.Create refuses it.
		f := eoWfNewFixture(t)
		if err := f.db.Model(&models.EORecordType{}).
			Where("key = ?", eoPilotTaskTypeKey).Update("is_active", false).Error; err != nil {
			t.Fatalf("deactivate the task type: %v", err)
		}
		directive := f.create(t, eoPilotDirectiveTypeKey, nil, "", origin)
		if _, err := f.try(t, directive, "assign_directly", models.EOTransitionInput{}); err != nil {
			t.Fatalf("the transition itself failed: a failing action must never undo a committed state change: %v", err)
		}
		if n := len(children(t, f, directive)); n != 0 {
			t.Fatalf("a task was created although its type is inactive")
		}
		var notes []models.EORecordHistory
		if err := f.db.Where("record_id = ? AND comment LIKE ?", directive.ID, "spawn_record%").Find(&notes).Error; err != nil {
			t.Fatalf("read the directive's history: %v", err)
		}
		if len(notes) == 0 {
			t.Fatalf("the spawn failed and wrote nothing to the directive's history; the task that never appeared would be invisible to the person looking at the record")
		}
		if !strings.Contains(notes[0].Comment, "did not create a record") {
			t.Errorf("the note is %q, which does not say that nothing was created", notes[0].Comment)
		}
	})
}

// eoA7String renders a values-bag entry for comparison.
func eoA7String(v any) string {
	s, _ := v.(string)
	return s
}
