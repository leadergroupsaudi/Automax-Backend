package services

import (
	"context"
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

// ── Task A4's link rules, in front of the engine that reads them ────────────
//
// WHY THESE TESTS LIVE HERE rather than beside the seed in package database:
// every rule Task A4 seeds is read by a function in THIS package —
// ValidateLink (the endpoints and the cardinality), DeriveDeadlineFromParent
// (the Referral's deadline), BlockingChildren (both block semantics) — and
// package database cannot import package services, because services imports
// database. A test over there can only assert that a column holds the value
// somebody typed into the seed; it cannot show the rule refusing anything.
//
// The configuration under test is the SEEDED one, through database.SeedEOPilot
// (database.SeedEOPlatform's precedent), never a hand-built copy.
//
// The KEYS are repeated here as literals because they are unexported in
// package database. Every fixture below fails loudly when one does not
// resolve, which is what a rename would do.
const (
	eoPilotDirectiveTypeKey  = "eo_directive"
	eoPilotDraftTypeKey      = "eo_draft_decision_study"
	eoPilotIncomingTypeKey   = "eo_incoming_correspondence"
	eoPilotPlanTypeKey       = "eo_execution_plan"
	eoPilotStageTypeKey      = "eo_execution_plan_stage"
	eoPilotAssignmentTypeKey = "eo_assignment"
	eoPilotObjectionTypeKey  = "eo_objection"
	eoPilotDecisionTypeKey   = "eo_decision_record"
	eoPilotReferralTypeKey   = "eo_referral"
	eoPilotOutgoingTypeKey   = "eo_outgoing_reply"
	eoPilotTaskTypeKey       = "eo_task"

	eoPilotPlanLinkKey       = "eo_execution_plan_parent"
	eoPilotStageLinkKey      = "eo_execution_plan_stage_parent"
	eoPilotAssignmentLinkKey = "eo_assignment_parent"
	eoPilotObjectionLinkKey  = "eo_objection_parent"
	eoPilotDecisionLinkKey   = "eo_decision_record_parent"
	eoPilotReferralLinkKey   = "eo_referral_parent"
	eoPilotOutgoingLinkKey   = "eo_outgoing_reply_parent"
	eoPilotTaskLinkKey       = "eo_task_parent"

	// The transition code the Objection's rule names, which TASK A5 must
	// seed on the Draft Decision Study's workflow. Until it does, the
	// workflows below are this test's own: they are the only thing that lets
	// the rule be executed at all, and they are scaffolding, not
	// configuration under test.
	eoPilotRaiseToHECode = "raise_to_he"
)

// eoPilotTypeKeysUnderTest is every pilot record type the fixture gives a
// workflow, so that any of them can be created and transitioned.
var eoPilotTypeKeysUnderTest = []string{
	eoPilotDirectiveTypeKey, eoPilotDraftTypeKey, eoPilotIncomingTypeKey,
	eoPilotPlanTypeKey, eoPilotStageTypeKey, eoPilotAssignmentTypeKey,
	eoPilotObjectionTypeKey, eoPilotDecisionTypeKey, eoPilotReferralTypeKey,
	eoPilotOutgoingTypeKey, eoPilotTaskTypeKey,
}

type eoPilotFixture struct {
	db        *gorm.DB
	records   EORecordService
	transits  EOTransitionService
	repo      repository.EORecordRepository
	types     repository.EORecordTypeRepository
	actor     *models.User
	bodyID    uuid.UUID
	typeIDs   map[string]uuid.UUID
	linkTypes map[string]models.EOLinkType
	// closeID and raiseID are the transition ids per record type key.
	closeID map[string]uuid.UUID
	raiseID map[string]uuid.UUID
}

// eoPilotNewFixture seeds the platform, then the pilot, then gives every pilot
// type a three-state workflow so the real Create and Execute pipelines can run
// on it. The workflows are TEST SCAFFOLDING — Task A5 seeds the real ones —
// and nothing below asserts anything about them.
func eoPilotNewFixture(t *testing.T) *eoPilotFixture {
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
	// The pilot seed records which of its steps have run in a durable table
	// that MigrateEOPlatform does not create (it is the SEED's bookkeeping,
	// not the engine's storage) and eoE2ETables therefore does not list. It
	// is created here rather than there so that Plan 1's acceptance fixture
	// is left exactly as it was.
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
		ID: uuid.New(), Email: "eo.link@example.test", Username: "eo_link",
		Password: "x", FirstName: "Office", LastName: "Officer", IsActive: true,
		Roles: []models.Role{eoRole},
	}
	if err := db.Omit("Roles", "Departments", "Locations", "Classifications").Create(actor).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	actor.Roles = []models.Role{eoRole}

	f := &eoPilotFixture{
		db:        db,
		repo:      repository.NewEORecordRepository(db),
		types:     repository.NewEORecordTypeRepository(db),
		actor:     actor,
		bodyID:    body.ID,
		typeIDs:   map[string]uuid.UUID{},
		linkTypes: map[string]models.EOLinkType{},
		closeID:   map[string]uuid.UUID{},
		raiseID:   map[string]uuid.UUID{},
	}
	f.records = NewEORecordServiceWithSLA(f.repo, f.types, repository.NewEOSequenceRepository(db), repository.NewWorkflowRepository(db), repository.NewEscalationPolicyRepository(db))
	f.transits = NewEOTransitionServiceWithSLA(f.repo, f.types, repository.NewWorkflowRepository(db), nil, repository.NewEscalationPolicyRepository(db))

	for _, key := range eoPilotTypeKeysUnderTest {
		ty, err := f.types.GetByKey(context.Background(), key)
		if err != nil || ty == nil {
			t.Fatalf("the pilot seed did not create record type %q: %v", key, err)
		}
		f.typeIDs[key] = ty.ID
		f.eoPilotWorkflow(t, ty, eoRole)
	}
	var lts []models.EOLinkType
	if err := db.Find(&lts).Error; err != nil {
		t.Fatalf("read the seeded link types: %v", err)
	}
	for _, lt := range lts {
		f.linkTypes[lt.Key] = lt
	}
	for _, key := range []string{
		eoPilotPlanLinkKey, eoPilotStageLinkKey, eoPilotAssignmentLinkKey,
		eoPilotObjectionLinkKey, eoPilotDecisionLinkKey, eoPilotReferralLinkKey,
		eoPilotOutgoingLinkKey, eoPilotTaskLinkKey,
	} {
		if _, ok := f.linkTypes[key]; !ok {
			t.Fatalf("the pilot seed did not create link type %q", key)
		}
	}
	return f
}

// eoPilotWorkflow gives one record type open -> raised -> closed, with a
// `close` transition into the terminal state and a transition coded
// eoPilotRaiseToHECode that is NOT terminal — which is the only arrangement in
// which the two block semantics can be told apart.
func (f *eoPilotFixture) eoPilotWorkflow(t *testing.T, ty *models.EORecordType, role models.Role) {
	t.Helper()
	wf := models.Workflow{
		ID: uuid.New(), Name: "Test " + ty.Key, Code: "test_" + ty.Key,
		RecordType: "eo", RecordTypeID: &ty.ID, IsActive: true,
	}
	if err := f.db.Create(&wf).Error; err != nil {
		t.Fatalf("create workflow for %q: %v", ty.Key, err)
	}
	open := models.WorkflowState{ID: uuid.New(), WorkflowID: wf.ID, Name: "Open", Code: "open", StateType: "initial", SortOrder: 1, IsActive: true}
	raised := models.WorkflowState{ID: uuid.New(), WorkflowID: wf.ID, Name: "Raised", Code: "raised", StateType: "normal", SortOrder: 2, IsActive: true}
	closed := models.WorkflowState{ID: uuid.New(), WorkflowID: wf.ID, Name: "Closed", Code: "closed", StateType: "terminal", SortOrder: 3, IsActive: true}
	for _, st := range []models.WorkflowState{open, raised, closed} {
		if err := f.db.Create(&st).Error; err != nil {
			t.Fatalf("create state %q: %v", st.Code, err)
		}
	}
	raise := models.WorkflowTransition{
		ID: uuid.New(), WorkflowID: wf.ID, Name: "Raise to H.E.", Code: eoPilotRaiseToHECode,
		FromStateID: open.ID, ToStateID: raised.ID, IsActive: true, SortOrder: 1,
		AllowedRoles: []models.Role{role},
	}
	closeIt := models.WorkflowTransition{
		ID: uuid.New(), WorkflowID: wf.ID, Name: "Close", Code: "close",
		FromStateID: open.ID, ToStateID: closed.ID, IsActive: true, SortOrder: 2,
		AllowedRoles: []models.Role{role},
	}
	for _, tr := range []models.WorkflowTransition{raise, closeIt} {
		if err := f.db.Create(&tr).Error; err != nil {
			t.Fatalf("create transition %q: %v", tr.Code, err)
		}
	}
	f.raiseID[ty.Key] = raise.ID
	f.closeID[ty.Key] = closeIt.ID
	if err := f.db.Model(&models.EORecordType{}).Where("id = ?", ty.ID).Update("workflow_id", wf.ID).Error; err != nil {
		t.Fatalf("bind the workflow to %q: %v", ty.Key, err)
	}
}

// eoPilotValues is a submission that satisfies every field the SEEDED
// configuration requires at creation of typeKey, and nothing else: the
// definitions are read back from the database, so a field added to the pilot
// cannot leave this helper behind, and an `auto` or `derived` field is never
// submitted (ValidateRecordValues refuses one).
func (f *eoPilotFixture) eoPilotValues(t *testing.T, typeKey string) map[string]any {
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

// eoPilotCreate creates one record of typeKey, under parent when given.
func (f *eoPilotFixture) eoPilotCreate(t *testing.T, typeKey string, parent *models.EORecord, linkTypeKey string, overrides map[string]any) *models.EORecord {
	t.Helper()
	rec, err := f.eoPilotTryCreate(t, typeKey, parent, linkTypeKey, overrides)
	if err != nil {
		t.Fatalf("create %s: %v", typeKey, err)
	}
	return rec
}

func (f *eoPilotFixture) eoPilotTryCreate(t *testing.T, typeKey string, parent *models.EORecord, linkTypeKey string, overrides map[string]any) (*models.EORecord, error) {
	t.Helper()
	values := f.eoPilotValues(t, typeKey)
	for k, v := range overrides {
		values[k] = v
	}
	req := &models.EORecordCreateRequest{RecordTypeID: f.typeIDs[typeKey], Values: values}
	if parent != nil {
		req.ParentRecordID = &parent.ID
		req.ParentLinkTypeKey = linkTypeKey
	}
	return f.records.Create(context.Background(), req, f.actor)
}

// ── the Referral's derived deadline ─────────────────────────────────────────

// THE RULE TASK A3 LEFT UNARMED. A Referral's `deadline` is the inherited
// due_date column, narrowed to provenance `auto` so no clerk may type one, and
// services.DeriveDeadlineFromParent is the only thing that can fill it — which
// it does only when the LINK TYPE carries derive_deadline_from_parent. This
// creates a real Referral under a real letter through the real save pipeline
// and reads the stored row back.
//
// Mutation: set `deriveDeadline: false` on eoReferralLinkKey in
// database.eoPilotLinkTypes (the referral then stores no deadline at all,
// which is what shipped before this task).
func TestEoPilotReferralUnderALetterInheritsItsDeadline(t *testing.T) {
	f := eoPilotNewFixture(t)
	ctx := context.Background()

	letterDue := time.Now().Add(20 * 24 * time.Hour).Truncate(time.Second)
	letter := f.eoPilotCreate(t, eoPilotIncomingTypeKey, nil, "", map[string]any{
		"due_date": letterDue.Format(time.RFC3339),
	})
	if letter.DueDate == nil {
		t.Fatalf("the letter stored no deadline; the matrix's sender_deadline IS the due_date column")
	}

	referral := f.eoPilotCreate(t, eoPilotReferralTypeKey, letter, eoPilotReferralLinkKey, nil)
	stored, err := f.repo.GetByID(ctx, referral.ID, referral.RecordTypeID)
	if err != nil || stored == nil {
		t.Fatalf("re-read the referral: %v", err)
	}
	if stored.DueDate == nil {
		t.Fatalf("the referral has NO deadline: nothing armed services.DeriveDeadlineFromParent, and an absent deadline is not an error anywhere in the engine")
	}
	offset := f.linkTypes[eoPilotReferralLinkKey].DeadlineOffsetDays
	want := letter.DueDate.AddDate(0, 0, -offset)
	if !stored.DueDate.Equal(want) {
		t.Errorf("the referral is due %s, want %s (the letter's %s counted back %d days)",
			stored.DueDate.Format(time.RFC3339), want.Format(time.RFC3339),
			letter.DueDate.Format(time.RFC3339), offset)
	}
	if !stored.DueDate.Before(*letter.DueDate) {
		t.Errorf("the referral's deadline (%s) is not BEFORE the letter's (%s); it is not counted back from anything",
			stored.DueDate, letter.DueDate)
	}

	// The other half of "derived, not typed": a clerk cannot supply one.
	t.Run("a clerk may not type a referral deadline", func(t *testing.T) {
		_, err := f.eoPilotTryCreate(t, eoPilotReferralTypeKey, letter, eoPilotReferralLinkKey, map[string]any{
			"due_date": time.Now().Add(400 * 24 * time.Hour).Format(time.RFC3339),
		})
		var invalid *EOValidationError
		if !errors.As(err, &invalid) {
			t.Fatalf("a typed deadline was accepted: %v", err)
		}
		if _, ok := invalid.Fields["due_date"]; !ok {
			t.Errorf("the refusal does not name due_date: %v", invalid.Fields)
		}
	})

	// EVERY referral counts back from the LETTER, sub-referrals included: the
	// client's pattern IN-YYYY-NNNN-R# cannot be rendered under a referral's
	// own four-segment reference, so a referral under a referral is refused
	// before any link rule runs — which is why the seeded link type targets
	// the letter alone and the matrix carries the sub-referral on the
	// `parent_referral` link field instead. This pins the refusal, so the
	// narrow target list is a decision on record rather than an omission.
	t.Run("a referral cannot be a record child of another referral", func(t *testing.T) {
		_, err := f.eoPilotTryCreate(t, eoPilotReferralTypeKey, stored, eoPilotReferralLinkKey, nil)
		if err == nil {
			t.Fatalf("a sub-referral was created under a referral; the seeded target list should be widened to match")
		}
		if !strings.Contains(err.Error(), "segments") {
			t.Errorf("refused for an unexpected reason: %v", err)
		}
	})
}

// ── the polymorphic Task ────────────────────────────────────────────────────

// Addendum 1. "Hangs off anything, including across subsystems" is the literal
// sentinel "any", NOT an empty target list — an empty one permits nothing. So
// this hangs a Task off two UNRELATED types through the real create pipeline
// and asserts both succeed.
//
// Mutation: remove `parentTypeKeys` from eoTaskLinkKey's spec in
// database.eoPilotLinkTypes (the seed refuses it); or change eoAnyTargetType
// to any other string, which is accepted by the seed and refuses every Task.
func TestEoPilotTaskLinkAcceptsAnyTarget(t *testing.T) {
	f := eoPilotNewFixture(t)

	directive := f.eoPilotCreate(t, eoPilotDirectiveTypeKey, nil, "", nil)
	letter := f.eoPilotCreate(t, eoPilotIncomingTypeKey, nil, "", nil)

	for _, tc := range []struct {
		name   string
		parent *models.EORecord
	}{
		{"a task under a directive (EO-DIR)", directive},
		{"a task under an incoming letter (EO-COM)", letter},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task, err := f.eoPilotTryCreate(t, eoPilotTaskTypeKey, tc.parent, eoPilotTaskLinkKey, nil)
			if err != nil {
				t.Fatalf("the universal task could not be hung off %s: %v", tc.parent.Reference, err)
			}
			links, err := f.repo.LinksFrom(context.Background(), task.ID)
			if err != nil {
				t.Fatalf("read the task's links: %v", err)
			}
			if len(links) != 1 || links[0].ToRecordID != tc.parent.ID || links[0].LinkTypeKey != eoPilotTaskLinkKey {
				t.Errorf("the task's links = %+v, want one %q link to %s", links, eoPilotTaskLinkKey, tc.parent.ID)
			}
		})
	}

	// The sentinel is on the TARGET only: the source is the Task type, so the
	// link type refuses a record of any other type trying to use it.
	t.Run("but the source is still only a task", func(t *testing.T) {
		_, err := f.eoPilotTryCreate(t, eoPilotReferralTypeKey, letter, eoPilotTaskLinkKey, nil)
		if err == nil {
			t.Fatalf("a referral was allowed to use the task's link type")
		}
		// The MESSAGE, not merely a failure: a create has many ways to fail
		// (a missing required field, a pattern that will not render), and any
		// of them would satisfy `err != nil` while proving nothing about the
		// source list. ValidateLink's own wording is what says the SOURCE was
		// what refused it.
		if !strings.Contains(err.Error(), "does not allow a source record of type") {
			t.Errorf("refused, but not by the link type's source list: %v", err)
		}
	})
}

// ── what the seeded endpoints and cardinality refuse ────────────────────────

// Every refusal here is services.ValidateLink reading a SEEDED row. A link
// type that permits nothing and one that permits everything both look
// plausible in the database; only a refusal and a permission together say
// which was seeded.
//
// Mutation: in database.eoPilotLinkTypes, give eoReferralLinkKey the parent
// type eoDirectiveTypeKey (or add it), and the first case stops refusing.
func TestEoPilotLinkTypesRefuseTheWrongEndpoints(t *testing.T) {
	f := eoPilotNewFixture(t)

	rec := func(typeKey string) *models.EORecord {
		return &models.EORecord{ID: uuid.New(), RecordTypeID: f.typeIDs[typeKey]}
	}

	tests := []struct {
		name    string
		link    string
		from    *models.EORecord
		to      *models.EORecord
		refused bool
	}{
		{"a referral under its letter", eoPilotReferralLinkKey, rec(eoPilotReferralTypeKey), rec(eoPilotIncomingTypeKey), false},
		{"a referral under another referral, which the numbering cannot express", eoPilotReferralLinkKey, rec(eoPilotReferralTypeKey), rec(eoPilotReferralTypeKey), true},
		{"a referral under a DIRECTIVE, which the matrix does not allow", eoPilotReferralLinkKey, rec(eoPilotReferralTypeKey), rec(eoPilotDirectiveTypeKey), true},
		{"an objection using the referral's link type", eoPilotReferralLinkKey, rec(eoPilotObjectionTypeKey), rec(eoPilotIncomingTypeKey), true},
		{"an assignment under a directive", eoPilotAssignmentLinkKey, rec(eoPilotAssignmentTypeKey), rec(eoPilotDirectiveTypeKey), false},
		{"an assignment under an execution plan", eoPilotAssignmentLinkKey, rec(eoPilotAssignmentTypeKey), rec(eoPilotPlanTypeKey), false},
		{"an assignment under a letter", eoPilotAssignmentLinkKey, rec(eoPilotAssignmentTypeKey), rec(eoPilotIncomingTypeKey), true},
		{"an execution plan under its directive", eoPilotPlanLinkKey, rec(eoPilotPlanTypeKey), rec(eoPilotDirectiveTypeKey), false},
		{"an execution plan under a draft study", eoPilotPlanLinkKey, rec(eoPilotPlanTypeKey), rec(eoPilotDraftTypeKey), true},
		{"a stage under its plan", eoPilotStageLinkKey, rec(eoPilotStageTypeKey), rec(eoPilotPlanTypeKey), false},
		{"a stage under the directive, skipping the plan", eoPilotStageLinkKey, rec(eoPilotStageTypeKey), rec(eoPilotDirectiveTypeKey), true},
		{"an objection under its draft study", eoPilotObjectionLinkKey, rec(eoPilotObjectionTypeKey), rec(eoPilotDraftTypeKey), false},
		{"a decision record under its draft study", eoPilotDecisionLinkKey, rec(eoPilotDecisionTypeKey), rec(eoPilotDraftTypeKey), false},
		{"a decision record under a letter", eoPilotDecisionLinkKey, rec(eoPilotDecisionTypeKey), rec(eoPilotIncomingTypeKey), true},
		{"an outgoing reply under its letter", eoPilotOutgoingLinkKey, rec(eoPilotOutgoingTypeKey), rec(eoPilotIncomingTypeKey), false},
		{"an outgoing reply under a draft study", eoPilotOutgoingLinkKey, rec(eoPilotOutgoingTypeKey), rec(eoPilotDraftTypeKey), true},
		{"a task under a stage, three levels down another subsystem", eoPilotTaskLinkKey, rec(eoPilotTaskTypeKey), rec(eoPilotStageTypeKey), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lt := f.linkTypes[tc.link]
			err := ValidateLink(&lt, tc.from, tc.to, nil)
			if tc.refused && err == nil {
				t.Errorf("link type %q permitted it", tc.link)
			}
			if !tc.refused && err != nil {
				t.Errorf("link type %q refused it: %v", tc.link, err)
			}
		})
	}

	// Cardinality: every pilot link type is `one`, counted per source record,
	// so a child cannot acquire a SECOND parent of the same link type.
	//
	// Mutation: write models.EOCardinalityMany on any spec.
	t.Run("a second parent of the same link type is refused", func(t *testing.T) {
		lt := f.linkTypes[eoPilotReferralLinkKey]
		child, first, second := rec(eoPilotReferralTypeKey), rec(eoPilotIncomingTypeKey), rec(eoPilotIncomingTypeKey)
		existing := []models.EORecordLink{{
			ID: uuid.New(), FromRecordID: child.ID, FromRecordTypeID: child.RecordTypeID,
			ToRecordID: first.ID, ToRecordTypeID: first.RecordTypeID, LinkTypeKey: lt.Key,
		}}
		if err := ValidateLink(&lt, child, second, existing); err == nil {
			t.Errorf("cardinality %q let one referral hang off two letters", lt.Cardinality)
		}
		if err := ValidateLink(&lt, child, first, existing); err == nil {
			t.Errorf("the same link was accepted twice")
		}
	})
}

// ── what the seeded blocking rules refuse ───────────────────────────────────

// services.BlockingChildren over the SEEDED link types, asked both of spec
// §7.6's questions. The two are not interchangeable: the closure question is
// asked only on a move into a terminal state, the named one on every coded
// transition, and a child that blocks one does not block the other.
//
// Mutation: set `blocksClosure: false` on eoExecutionPlanLinkKey (or any of
// the four), or move eoObjectionLinkKey's rule to blocksClosure.
func TestEoPilotBlockingRulesRefuseUsingTheSeededLinkTypes(t *testing.T) {
	f := eoPilotNewFixture(t)

	tests := []struct {
		name string
		// childTypeKey hangs off parentTypeKey through link.
		childTypeKey string
		link         string
		blocksClose  bool
		blocksRaise  bool
	}{
		{"an open execution plan holds its directive open", eoPilotPlanTypeKey, eoPilotPlanLinkKey, true, false},
		{"an open assignment holds its parent open", eoPilotAssignmentTypeKey, eoPilotAssignmentLinkKey, true, false},
		{"an open referral holds its letter open", eoPilotReferralTypeKey, eoPilotReferralLinkKey, true, false},
		{"an open task holds its parent open", eoPilotTaskTypeKey, eoPilotTaskLinkKey, true, false},
		{"an open objection holds the RAISE, not the closure", eoPilotObjectionTypeKey, eoPilotObjectionLinkKey, false, true},
		{"a decision record holds nothing: it closes its parent", eoPilotDecisionTypeKey, eoPilotDecisionLinkKey, false, false},
		{"an outgoing reply holds nothing: it closes its parent", eoPilotOutgoingTypeKey, eoPilotOutgoingLinkKey, false, false},
		{"a plan stage holds nothing the client's document asks for", eoPilotStageTypeKey, eoPilotStageLinkKey, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lt := f.linkTypes[tc.link]
			parent := models.EORecord{ID: uuid.New(), Reference: "PARENT"}
			child := models.EORecord{ID: uuid.New(), RecordTypeID: f.typeIDs[tc.childTypeKey], Reference: "CHILD"}
			children := []models.EORecord{child}
			links := []models.EORecordLink{{
				ID: uuid.New(), FromRecordID: child.ID, FromRecordTypeID: child.RecordTypeID,
				ToRecordID: parent.ID, LinkTypeKey: lt.Key,
			}}
			lts := map[string]models.EOLinkType{lt.Key: lt}

			if got := len(BlockingChildren(children, links, lts, "")) > 0; got != tc.blocksClose {
				t.Errorf("blocks closure = %v, want %v", got, tc.blocksClose)
			}
			if got := len(BlockingChildren(children, links, lts, eoPilotRaiseToHECode)) > 0; got != tc.blocksRaise {
				t.Errorf("blocks %q = %v, want %v", eoPilotRaiseToHECode, got, tc.blocksRaise)
			}
			// A CLOSED child blocks nothing, whichever rule it carries: that
			// is how the parent is ever released.
			closedChild := child
			now := time.Now()
			closedChild.ClosedAt = &now
			if got := BlockingChildren([]models.EORecord{closedChild}, links, lts, ""); len(got) != 0 {
				t.Errorf("a closed child still blocks closure")
			}
			if got := BlockingChildren([]models.EORecord{closedChild}, links, lts, eoPilotRaiseToHECode); len(got) != 0 {
				t.Errorf("a closed child still blocks %q", eoPilotRaiseToHECode)
			}
		})
	}
}

// The closure rule through the WHOLE transition pipeline: an open Referral
// really does refuse to let its letter close, and closing the referral
// releases it.
//
// Mutation: set `blocksClosure: false` on eoReferralLinkKey.
func TestEoPilotOpenReferralBlocksTheLetterClosing(t *testing.T) {
	f := eoPilotNewFixture(t)
	ctx := context.Background()

	letter := f.eoPilotCreate(t, eoPilotIncomingTypeKey, nil, "", nil)
	referral := f.eoPilotCreate(t, eoPilotReferralTypeKey, letter, eoPilotReferralLinkKey, nil)

	_, err := f.transits.Execute(ctx, letter, f.closeID[eoPilotIncomingTypeKey], f.actor, models.EOTransitionInput{Comment: "closing"})
	if !errors.Is(err, ErrEOTransitionBlocked) {
		t.Fatalf("closing a letter with an open referral returned %v, want ErrEOTransitionBlocked", err)
	}

	if _, err := f.transits.Execute(ctx, referral, f.closeID[eoPilotReferralTypeKey], f.actor, models.EOTransitionInput{Comment: "answered"}); err != nil {
		t.Fatalf("close the referral: %v", err)
	}
	fresh, err := f.repo.GetByID(ctx, letter.ID, letter.RecordTypeID)
	if err != nil || fresh == nil {
		t.Fatalf("re-read the letter: %v", err)
	}
	if _, err := f.transits.Execute(ctx, fresh, f.closeID[eoPilotIncomingTypeKey], f.actor, models.EOTransitionInput{Comment: "closing"}); err != nil {
		t.Fatalf("closing the letter once its referral is closed: %v", err)
	}
}

// The closure rule through the whole pipeline a SECOND time, for the
// polymorphic link type — because the mechanism differs by target type: the
// Referral's letter is permitted by an id in the list, a Task's parent by the
// "any" sentinel, which is a different branch of services.eoTypeAllowed. A
// task holding a DIRECTIVE open is also the matrix's own example of
// "partial closure is not closure" (spec §7.6, EO-DIR).
//
// Mutation: set `blocksClosure: false` on eoTaskLinkKey in
// database.eoPilotLinkTypes.
func TestEoPilotOpenTaskBlocksTheDirectiveClosing(t *testing.T) {
	f := eoPilotNewFixture(t)
	ctx := context.Background()

	directive := f.eoPilotCreate(t, eoPilotDirectiveTypeKey, nil, "", nil)
	task := f.eoPilotCreate(t, eoPilotTaskTypeKey, directive, eoPilotTaskLinkKey, nil)

	_, err := f.transits.Execute(ctx, directive, f.closeID[eoPilotDirectiveTypeKey], f.actor, models.EOTransitionInput{Comment: "closing"})
	if !errors.Is(err, ErrEOTransitionBlocked) {
		t.Fatalf("closing a directive with an open task returned %v, want ErrEOTransitionBlocked", err)
	}
	if !strings.Contains(err.Error(), task.Reference) {
		t.Errorf("the refusal does not name the task holding it open (%s): %v", task.Reference, err)
	}

	if _, err := f.transits.Execute(ctx, task, f.closeID[eoPilotTaskTypeKey], f.actor, models.EOTransitionInput{Comment: "done"}); err != nil {
		t.Fatalf("close the task: %v", err)
	}
	fresh, err := f.repo.GetByID(ctx, directive.ID, directive.RecordTypeID)
	if err != nil || fresh == nil {
		t.Fatalf("re-read the directive: %v", err)
	}
	if _, err := f.transits.Execute(ctx, fresh, f.closeID[eoPilotDirectiveTypeKey], f.actor, models.EOTransitionInput{Comment: "closing"}); err != nil {
		t.Fatalf("closing the directive once its task is closed: %v", err)
	}
}

// THE BRANCH THAT HAS NEVER RUN. blocks_parent_transition_key was implemented
// in Plan 1 and left unseeded, so no seeded configuration has ever exercised
// it. This drives it through the real transition pipeline: an open Objection
// refuses the raise to H.E. and does NOT refuse the study's closure, which is
// exactly what the client's matrix says and the opposite of what
// blocks_parent_closure would do.
//
// THE WORKFLOW IS THIS TEST'S OWN — Task A5 seeds the real one — but the
// transition CODE is the shared constant the seed names, so a disagreement
// between the two tasks shows up here as a rule that stops refusing.
//
// Mutation: change the seed's eoDraftRaiseToHETransitionCode to anything else
// (the first half stops refusing); or give the link type blocksClosure instead
// (the second half starts refusing).
func TestEoPilotOpenObjectionBlocksTheRaiseToHEAndNotTheClosure(t *testing.T) {
	f := eoPilotNewFixture(t)
	ctx := context.Background()

	study := f.eoPilotCreate(t, eoPilotDraftTypeKey, nil, "", nil)
	if _, err := f.eoPilotTryCreate(t, eoPilotObjectionTypeKey, study, eoPilotObjectionLinkKey, nil); err != nil {
		t.Fatalf("create the objection: %v", err)
	}

	_, err := f.transits.Execute(ctx, study, f.raiseID[eoPilotDraftTypeKey], f.actor, models.EOTransitionInput{Comment: "raising"})
	if !errors.Is(err, ErrEOTransitionBlocked) {
		t.Fatalf("raising a study with an open objection returned %v, want ErrEOTransitionBlocked", err)
	}

	fresh, err := f.repo.GetByID(ctx, study.ID, study.RecordTypeID)
	if err != nil || fresh == nil {
		t.Fatalf("re-read the study: %v", err)
	}
	if _, err := f.transits.Execute(ctx, fresh, f.closeID[eoPilotDraftTypeKey], f.actor, models.EOTransitionInput{Comment: "withdrawn"}); err != nil {
		t.Fatalf("an open objection blocked the study's CLOSURE, which the matrix says it must not: %v", err)
	}
}
