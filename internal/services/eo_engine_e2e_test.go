package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/automax/backend/internal/database"
	"github.com/automax/backend/internal/database/migrations"
	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
)

// ── the test database ───────────────────────────────────────────────────────

// eoServicesTestDB returns a *gorm.DB on a freshly created, uniquely named
// Postgres schema carrying the whole EO engine's storage, dropped when the
// test ends. It skips the test unless AUTOMAX_TEST_DB_DSN is set.
//
// It deliberately does NOT have repository.eoTestDB's shape. That helper pins
// the pool to ONE connection so a session-level `SET search_path` reaches
// every statement, and that pin is fatal here: this test drives
// EORecordService.Create and EOTransitionService.Execute, both of which open
// EORecordRepository.Transaction and hold a FOR UPDATE row lock inside it. Any
// read that reached for a second pooled connection while the transaction held
// its own would wait for a free connection forever — database/sql has no
// timeout there.
//
// So it is repository.eoSequenceTestDB's shape instead: create and migrate the
// schema on a single-connection handle, read back current_schema(), then open
// a SECOND pool against dsn + "?search_path=<schema>" with a real connection
// count. The search_path then travels with the DSN, which means it is applied
// per connection and isolation survives a pool.
func eoServicesTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("AUTOMAX_TEST_DB_DSN")
	if dsn == "" {
		t.Skip("set AUTOMAX_TEST_DB_DSN to run DB tests")
	}

	single, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	singleSQL, err := single.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	singleSQL.SetMaxOpenConns(1)

	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("random schema suffix: %v", err)
	}
	schema := "eo_e2e_test_" + hex.EncodeToString(suffix)

	t.Cleanup(func() {
		if err := single.Exec(`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`).Error; err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
		singleSQL.Close()
	})

	if err := single.Exec(`CREATE SCHEMA "` + schema + `"`).Error; err != nil {
		t.Fatalf("create schema %s: %v", schema, err)
	}
	if err := single.Exec(`SET search_path TO "` + schema + `"`).Error; err != nil {
		t.Fatalf("set search_path: %v", err)
	}

	// eo_records is LIST-partitioned and cannot be expressed by AutoMigrate,
	// so the record family comes from the production migration — the same one
	// that asserts each table's column set.
	if err := migrations.MigrateEOPlatform(single); err != nil {
		t.Fatalf("migrate eo platform: %v", err)
	}
	// models.User.CallStatus is a Postgres ENUM, created by database.Migrate
	// in the schema it migrates. This schema is a fresh one, so it needs its
	// own copy before the users table can be created.
	if err := single.Exec(`CREATE TYPE user_call_status AS ENUM ('offline', 'online', 'busy', 'in_call', 'available')`).Error; err != nil {
		t.Fatalf("create user_call_status enum: %v", err)
	}
	migrationDB := single.Session(&gorm.Session{})
	migrationDB.Config.DisableForeignKeyConstraintWhenMigrating = true
	// AutoMigrate, not Migrator().CreateTable: only AutoMigrate also creates
	// the many2many join tables the workflow configuration hangs off.
	if err := migrationDB.AutoMigrate(eoE2ETables()...); err != nil {
		t.Fatalf("create engine tables: %v", err)
	}

	var live string
	if err := single.Raw(`SELECT current_schema()`).Scan(&live).Error; err != nil {
		t.Fatalf("current_schema: %v", err)
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	pooled, err := gorm.Open(postgres.Open(dsn+sep+"search_path="+live), &gorm.Config{})
	if err != nil {
		t.Fatalf("open pooled db: %v", err)
	}
	pooledSQL, err := pooled.DB()
	if err != nil {
		t.Fatalf("get pooled sql.DB: %v", err)
	}
	pooledSQL.SetMaxOpenConns(20)
	t.Cleanup(func() { pooledSQL.Close() })
	return pooled
}

// eoE2ETables is every table the engine reads on this path that
// MigrateEOPlatform does not create: the metamodel, the workflow
// configuration the EO engine reuses, and the rows the repositories preload
// alongside them.
func eoE2ETables() []any {
	return []any{
		&models.Permission{},
		&models.Role{},
		&models.Department{},
		&models.Classification{},
		&models.Location{},
		&models.User{},
		&models.EscalationPolicy{},
		&models.EscalationPolicyStep{},
		&models.EscalationPolicyStepTarget{},
		&models.Workflow{},
		&models.WorkflowState{},
		&models.WorkflowTransition{},
		&models.TransitionRequirement{},
		&models.TransitionAction{},
		&models.TransitionFieldChange{},
		&models.EORecordType{},
		&models.EOFieldDef{},
		&models.EOLinkType{},
	}
}

// ── the fixture ─────────────────────────────────────────────────────────────

// eoE2EFixture is the seeded configuration plus the services under test,
// wired exactly as cmd/server/main.go wires them.
type eoE2EFixture struct {
	db        *gorm.DB
	records   EORecordService
	transits  EOTransitionService
	repo      repository.EORecordRepository
	types     repository.EORecordTypeRepository
	parentTyp *models.EORecordType
	childTyp  *models.EORecordType
	actor     *models.User
	outsider  *models.User
	bodyID    uuid.UUID
}

func eoE2ENewFixture(t *testing.T) *eoE2EFixture {
	t.Helper()
	db := eoServicesTestDB(t)
	ctx := context.Background()

	// The roles the seed grants its per-type codes to, and that its smoke
	// workflow attaches to the closing transition. In production the role
	// block of database.Seed writes them before the EO seed runs; here that
	// ordering is reproduced explicitly.
	roles := map[string]models.Role{}
	for _, r := range []models.Role{
		{Name: "Administrator", Code: "admin", IsSystem: true, IsActive: true},
		{Name: "Executive Office", Code: "executive_office", IsSystem: true, IsActive: true},
	} {
		if err := db.Create(&r).Error; err != nil {
			t.Fatalf("create role %s: %v", r.Code, err)
		}
		roles[r.Code] = r
	}

	// The configuration under test is the SEEDED one, not a hand-built copy:
	// a copy would drift from the seed and this test would stop being the
	// plan's acceptance evidence.
	database.SeedEOPlatform(db)

	body := models.Department{Name: "Executive Office", Code: "EO", IsActive: true}
	if err := db.Create(&body).Error; err != nil {
		t.Fatalf("create owning body: %v", err)
	}

	eoRole := roles["executive_office"]
	actor := &models.User{
		ID: uuid.New(), Email: "eo.officer@example.test", Username: "eo_officer",
		Password: "x", FirstName: "Office", LastName: "Officer", IsActive: true,
		Roles: []models.Role{eoRole},
	}
	outsider := &models.User{
		ID: uuid.New(), Email: "eo.outsider@example.test", Username: "eo_outsider",
		Password: "x", FirstName: "Out", LastName: "Sider", IsActive: true,
	}
	for _, u := range []*models.User{actor, outsider} {
		if err := db.Omit("Roles", "Departments", "Locations", "Classifications").Create(u).Error; err != nil {
			t.Fatalf("create user %s: %v", u.Username, err)
		}
	}

	types := repository.NewEORecordTypeRepository(db)
	parentTyp, err := types.GetByKey(ctx, "eo_smoke_directive")
	if err != nil || parentTyp == nil {
		t.Fatalf("the seed did not create record type eo_smoke_directive: %v", err)
	}
	childTyp, err := types.GetByKey(ctx, "eo_smoke_task")
	if err != nil || childTyp == nil {
		t.Fatalf("the seed did not create record type eo_smoke_task: %v", err)
	}

	recordRepo := repository.NewEORecordRepository(db)
	sequences := repository.NewEOSequenceRepository(db)
	workflows := repository.NewWorkflowRepository(db)
	policies := repository.NewEscalationPolicyRepository(db)

	// The WithSLA constructors, as main.go uses them. NewEORecordService and
	// NewEOTransitionService pass a nil policy repository, so an SLA clock
	// would never start and nothing would fail — this test would then prove
	// less than it appears to.
	return &eoE2EFixture{
		db:        db,
		records:   NewEORecordServiceWithSLA(recordRepo, types, sequences, workflows, policies),
		transits:  NewEOTransitionServiceWithSLA(recordRepo, types, workflows, nil, policies),
		repo:      recordRepo,
		types:     types,
		parentTyp: parentTyp,
		childTyp:  childTyp,
		actor:     actor,
		outsider:  outsider,
		bodyID:    body.ID,
	}
}

// eoE2ECloseTransition is the one transition the actor may run out of the
// record's current state. Going through AvailableTransitions rather than
// reading the row proves the actor filter as well as the state filter.
func (f *eoE2EFixture) eoE2ECloseTransition(t *testing.T, rec *models.EORecord) models.WorkflowTransition {
	t.Helper()
	available, err := f.transits.AvailableTransitions(context.Background(), rec, f.actor)
	if err != nil {
		t.Fatalf("available transitions for %s: %v", rec.Reference, err)
	}
	if len(available) != 1 || available[0].Code != "close" {
		got := make([]string, 0, len(available))
		for _, a := range available {
			got = append(got, a.Code)
		}
		t.Fatalf("available transitions for %s = %v, want exactly [close]", rec.Reference, got)
	}
	return available[0]
}

// eoE2ECloseInput is what the seeded `close` transition asks for: the comment
// its mandatory requirement wants, and the one field change it declares.
func eoE2ECloseInput(reason string) models.EOTransitionInput {
	return models.EOTransitionInput{
		Comment: reason,
		Values:  map[string]any{"change_reason": reason},
	}
}

// eoE2EValues decodes a record's jsonb bag.
func eoE2EValues(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	out := map[string]any{}
	if len(raw) == 0 {
		return out
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode values %s: %v", raw, err)
	}
	return out
}

// eoE2ERollup forces the parent's derived roll-up to recompute and returns the
// value that was stored.
//
// The engine has no write-through recompute: a parent's roll-up is recomputed
// when the PARENT is saved or transitioned, not when a child changes under it
// (services.RecomputeDerived runs inside the save and transition pipelines
// only). So this re-saves the parent with an empty edit, which is a real pass
// through EORecordService.Update — validation, derivation, persistence — and
// reads back what landed in the database rather than what the service
// returned.
func (f *eoE2EFixture) eoE2ERecompute(t *testing.T, rec *models.EORecord) float64 {
	t.Helper()
	if _, err := f.records.Update(context.Background(), rec.ID,
		&models.EORecordUpdateRequest{RecordTypeID: rec.RecordTypeID, Values: map[string]any{}},
		f.actor); err != nil {
		t.Fatalf("re-save %s to recompute its roll-up: %v", rec.Reference, err)
	}
	return f.eoE2EStoredRollup(t, rec)
}

// eoE2EStoredRollup reads the roll-up straight out of the row.
func (f *eoE2EFixture) eoE2EStoredRollup(t *testing.T, rec *models.EORecord) float64 {
	t.Helper()
	stored, err := f.repo.GetByID(context.Background(), rec.ID, rec.RecordTypeID)
	if err != nil || stored == nil {
		t.Fatalf("re-read %s: %v", rec.Reference, err)
	}
	v, ok := eoE2EValues(t, stored.Values)["child_task_count"]
	if !ok {
		t.Fatalf("record %s has no derived child_task_count; values = %s", rec.Reference, stored.Values)
	}
	n, ok := v.(float64)
	if !ok {
		t.Fatalf("record %s child_task_count = %T(%v), want a number", rec.Reference, v, v)
	}
	return n
}

// eoE2ETimerStatuses is the status of every escalation timer of a record.
func (f *eoE2EFixture) eoE2ETimerStatuses(t *testing.T, recordID uuid.UUID) []string {
	t.Helper()
	var out []string
	if err := f.db.Raw(
		`SELECT status FROM eo_timers WHERE record_id = ? AND capability_key = ? ORDER BY fire_at`,
		recordID, EOEscalationCapabilityKey).Scan(&out).Error; err != nil {
		t.Fatalf("read timers of %s: %v", recordID, err)
	}
	return out
}

// ── the acceptance gate ─────────────────────────────────────────────────────

// TestEOEngineEndToEnd drives the whole engine over the SEEDED configuration:
// hierarchical numbering, inherited and type-specific fields, link rules, the
// transition executor, the open-child block, SLA clocks and derived roll-ups.
// It is the acceptance evidence for the EO platform plan.
//
// It is a SERVICE-level test on purpose. The HTTP layer is Task 19's and has
// its own tests; driving the services directly is what makes a failure here
// diagnostic rather than ambiguous.
func TestEOEngineEndToEnd(t *testing.T) {
	f := eoE2ENewFixture(t)
	ctx := context.Background()
	parentDue := time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Second)
	// Captured BEFORE the first record is created. An own-sequence reference
	// carries the year the engine read while allocating it, so reading the
	// year afterwards would disagree with it for one run a year.
	year := time.Now().Format("2006")

	// ── 1. a parent record, numbered by its own type's pattern ──────────
	//
	// First, the point of the abstract base type: a concrete type that
	// declares none of the 17 fields itself still has them, and a required
	// one of them still refuses an empty submission. Inheritance is resolved
	// at read time, so nothing but this proves the seeded base reaches here.
	_, err := f.records.Create(ctx, &models.EORecordCreateRequest{
		RecordTypeID: f.parentTyp.ID,
		Values:       map[string]any{"subject_ar": "بلا أولوية"},
	}, f.actor)
	var invalid *EOValidationError
	if !errors.As(err, &invalid) {
		t.Fatalf("step 1 — a record missing its inherited required fields was accepted: %v", err)
	}
	for _, key := range []string{"priority", "sensitivity", "source", "owning_body_id"} {
		if _, ok := invalid.Fields[key]; !ok {
			t.Errorf("step 1 — inherited field %q is required on the base type but was not reported: %v", key, invalid.Fields)
		}
	}

	parent, err := f.records.Create(ctx, &models.EORecordCreateRequest{
		RecordTypeID: f.parentTyp.ID,
		Values: map[string]any{
			"subject_ar":     "توجيه اختبار",
			"subject_en":     "Smoke directive",
			"priority":       "high",
			"sensitivity":    "confidential",
			"source":         "direct_instruction",
			"owning_body_id": f.bodyID.String(),
			"due_date":       parentDue.Format(time.RFC3339),
		},
	}, f.actor)
	if err != nil {
		t.Fatalf("step 1 — create the parent: %v", err)
	}
	if want := "EO-SMOKE-" + year + "-001"; parent.Reference != want {
		t.Errorf("step 1 — parent reference %q does not match the type's own pattern %q (want %q)",
			parent.Reference, f.parentTyp.ReferencePattern, "EO-SMOKE-"+year+"-001")
	}
	if parent.StateID == nil {
		t.Fatal("step 1 — the parent was created into no state")
	}
	if got := f.eoE2EStoredRollup(t, parent); got != 0 {
		t.Errorf("step 1 — the parent's roll-up is %v with no children, want 0", got)
	}
	// The SLA clock starts WITH the record: an untouched record is the one
	// that has to escalate, so the clock cannot wait for the first transition.
	if got := f.eoE2ETimerStatuses(t, parent.ID); len(got) != 1 || got[0] != "pending" {
		t.Errorf("step 1 — escalation timers of the new parent = %v, want one pending", got)
	}

	// ── 2. a child under it: parent-derived reference, derived deadline ──
	childA, err := f.records.Create(ctx, &models.EORecordCreateRequest{
		RecordTypeID:      f.childTyp.ID,
		ParentRecordID:    &parent.ID,
		ParentLinkTypeKey: "eo_smoke_parent",
		Values: map[string]any{
			"subject_ar":     "مهمة أولى",
			"priority":       "normal",
			"sensitivity":    "internal", // the link type's cascade must overrule this
			"source":         "direct_instruction",
			"owning_body_id": f.bodyID.String(),
		},
	}, f.actor)
	if err != nil {
		t.Fatalf("step 2 — create the first child: %v", err)
	}
	if want := parent.Reference + "-T1"; childA.Reference != want {
		t.Errorf("step 2 — child reference = %q, want the parent-derived %q", childA.Reference, want)
	}
	if childA.DueDate == nil {
		t.Fatal("step 2 — the child inherited no deadline from its parent")
	}
	if want := parentDue.AddDate(0, 0, -3); !childA.DueDate.UTC().Truncate(time.Second).Equal(want) {
		t.Errorf("step 2 — child deadline = %s, want the parent's less the link type's 3 days (%s)",
			childA.DueDate.UTC(), want)
	}
	if childA.Sensitivity != "confidential" {
		t.Errorf("step 2 — child sensitivity = %q, want the parent's %q cascaded by the link type",
			childA.Sensitivity, parent.Sensitivity)
	}
	if got := f.eoE2ERecompute(t, parent); got != 1 {
		t.Errorf("step 2 — the parent's roll-up is %v with one child, want 1", got)
	}

	// ── 3. close the child: history written, closed_at set ───────────────
	closeChild := f.eoE2ECloseTransition(t, childA)

	// Who may act. An empty AllowedRoles means NO restriction, so this is the
	// assertion that proves the seed attached the role at all.
	if _, err := f.transits.Execute(ctx, childA, closeChild.ID, f.outsider,
		eoE2ECloseInput("not my record")); !errors.Is(err, ErrEOTransitionNotPermitted) {
		t.Errorf("step 3 — a user without the executive_office role closed the child: %v", err)
	}
	// The transition's REQUIREMENT: the seeded close carries a mandatory
	// comment, and the configuration's own wording has to reach the actor.
	_, err = f.transits.Execute(ctx, childA, closeChild.ID, f.actor,
		models.EOTransitionInput{Values: map[string]any{"change_reason": "no comment given"}})
	if err == nil {
		t.Error("step 3 — the child closed with no comment, though the transition requires one")
	} else if !strings.Contains(err.Error(), "A closing note is required") {
		t.Errorf("step 3 — the refusal does not carry the configured message: %v", err)
	}
	// The transition's FIELD CHANGE: change_reason is declared and required,
	// and it is an INHERITED field the concrete type never defines itself.
	_, err = f.transits.Execute(ctx, childA, closeChild.ID, f.actor,
		models.EOTransitionInput{Comment: "done"})
	var missingField *EOValidationError
	if !errors.As(err, &missingField) {
		t.Errorf("step 3 — the child closed without the required field change: %v", err)
	} else if _, ok := missingField.Fields["change_reason"]; !ok {
		t.Errorf("step 3 — the refusal does not name change_reason: %v", missingField.Fields)
	}
	// And only DECLARED keys may be written: a transition is the form its
	// configuration describes, not a second edit path around the field rules.
	_, err = f.transits.Execute(ctx, childA, closeChild.ID, f.actor,
		models.EOTransitionInput{Comment: "done", Values: map[string]any{
			"change_reason": "work finished",
			"priority":      "urgent",
		}})
	var undeclared *EOValidationError
	if !errors.As(err, &undeclared) {
		t.Errorf("step 3 — the transition wrote a field it does not declare: %v", err)
	} else if _, ok := undeclared.Fields["priority"]; !ok {
		t.Errorf("step 3 — the refusal does not name the undeclared key: %v", undeclared.Fields)
	}

	closedA, err := f.transits.Execute(ctx, childA, closeChild.ID, f.actor,
		eoE2ECloseInput("work finished"))
	if err != nil {
		t.Fatalf("step 3 — close the first child: %v", err)
	}
	if closedA.ClosedAt == nil {
		t.Error("step 3 — the child reached a terminal state with no closed_at")
	}
	if closedA.ChangeReason != "work finished" {
		t.Errorf("step 3 — change_reason = %q, want the value the transition's field change carried", closedA.ChangeReason)
	}
	var history []models.EORecordHistory
	if err := f.db.Where("record_id = ?", childA.ID).Order("created_at").Find(&history).Error; err != nil {
		t.Fatalf("step 3 — read the child's history: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("step 3 — the child has %d history rows, want creation plus the closing transition", len(history))
	}
	last := history[len(history)-1]
	if last.TransitionID == nil || *last.TransitionID != closeChild.ID {
		t.Errorf("step 3 — the last history row names transition %v, want %s", last.TransitionID, closeChild.ID)
	}
	if last.ToStateID == nil || *last.ToStateID != closeChild.ToStateID {
		t.Errorf("step 3 — the last history row enters state %v, want %s", last.ToStateID, closeChild.ToStateID)
	}
	if last.PerformedByID == nil || *last.PerformedByID != f.actor.ID {
		t.Errorf("step 3 — the last history row was performed by %v, want %s", last.PerformedByID, f.actor.ID)
	}
	if got := f.eoE2ERecompute(t, parent); got != 1 {
		t.Errorf("step 3 — the parent's roll-up is %v, want 1: closing a child does not remove it", got)
	}

	// ── 4. a second, OPEN child blocks the parent's closure ──────────────
	childB, err := f.records.Create(ctx, &models.EORecordCreateRequest{
		RecordTypeID:      f.childTyp.ID,
		ParentRecordID:    &parent.ID,
		ParentLinkTypeKey: "eo_smoke_parent",
		Values: map[string]any{
			"subject_ar":     "مهمة ثانية",
			"priority":       "normal",
			"sensitivity":    "internal",
			"source":         "direct_instruction",
			"owning_body_id": f.bodyID.String(),
		},
	}, f.actor)
	if err != nil {
		t.Fatalf("step 4 — create the second child: %v", err)
	}
	if want := parent.Reference + "-T2"; childB.Reference != want {
		t.Errorf("step 4 — second child reference = %q, want %q", childB.Reference, want)
	}

	closeParent := f.eoE2ECloseTransition(t, parent)
	// The comment and the field change are supplied, so the ONLY thing left
	// to refuse this is the open child. Without them it would fail at the
	// requirement check, two steps earlier, and prove nothing about blocking.
	_, err = f.transits.Execute(ctx, parent, closeParent.ID, f.actor,
		eoE2ECloseInput("closing early"))
	if !errors.Is(err, ErrEOTransitionBlocked) {
		t.Fatalf("step 4 — closing the parent while %s is open returned %v, want ErrEOTransitionBlocked",
			childB.Reference, err)
	}
	if !strings.Contains(err.Error(), childB.Reference) {
		t.Errorf("step 4 — the refusal does not name the blocking child %s: %v", childB.Reference, err)
	}
	// The refusal rolled its transaction back, so nothing moved.
	stillOpen, err := f.repo.GetByID(ctx, parent.ID, parent.RecordTypeID)
	if err != nil || stillOpen == nil {
		t.Fatalf("step 4 — re-read the parent: %v", err)
	}
	if stillOpen.ClosedAt != nil || stillOpen.StateID == nil || *stillOpen.StateID != *parent.StateID {
		t.Errorf("step 4 — the blocked transition still moved the parent: state %v, closed_at %v",
			stillOpen.StateID, stillOpen.ClosedAt)
	}

	// ── 4b. the child sequence is counted PER PARENT, not per pattern ────
	//
	// With one parent, a per-parent counter and a global one are
	// indistinguishable: both produce T1 then T2. A SECOND directive settles
	// it — its first task must be T1 again, not T3.
	other, err := f.records.Create(ctx, &models.EORecordCreateRequest{
		RecordTypeID: f.parentTyp.ID,
		Values: map[string]any{
			"subject_ar":     "توجيه آخر",
			"priority":       "normal",
			"sensitivity":    "internal",
			"source":         "direct_instruction",
			"owning_body_id": f.bodyID.String(),
		},
	}, f.actor)
	if err != nil {
		t.Fatalf("step 4b — create a second directive: %v", err)
	}
	if want := "EO-SMOKE-" + year + "-002"; other.Reference != want {
		t.Errorf("step 4b — second directive reference = %q, want %q", other.Reference, want)
	}
	otherTask, err := f.records.Create(ctx, &models.EORecordCreateRequest{
		RecordTypeID:      f.childTyp.ID,
		ParentRecordID:    &other.ID,
		ParentLinkTypeKey: "eo_smoke_parent",
		Values: map[string]any{
			"subject_ar":     "مهمة تابعة لتوجيه آخر",
			"priority":       "normal",
			"sensitivity":    "internal",
			"source":         "direct_instruction",
			"owning_body_id": f.bodyID.String(),
		},
	}, f.actor)
	if err != nil {
		t.Fatalf("step 4b — create a task under the second directive: %v", err)
	}
	if want := other.Reference + "-T1"; otherTask.Reference != want {
		t.Errorf("step 4b — the first task of a second directive is %q, want %q: the child sequence is counted per parent, not per pattern",
			otherTask.Reference, want)
	}

	// ── 5. close the second child, then the parent ───────────────────────
	closeChildB := f.eoE2ECloseTransition(t, childB)
	if _, err := f.transits.Execute(ctx, childB, closeChildB.ID, f.actor,
		eoE2ECloseInput("work finished")); err != nil {
		t.Fatalf("step 5 — close the second child: %v", err)
	}
	closedParent, err := f.transits.Execute(ctx, parent, closeParent.ID, f.actor,
		eoE2ECloseInput("every task is closed"))
	if err != nil {
		t.Fatalf("step 5 — close the parent once no child is open: %v", err)
	}
	if closedParent.ClosedAt == nil {
		t.Error("step 5 — the parent reached a terminal state with no closed_at")
	}
	// A closed record does not escalate: leaving the state cancels its clock.
	if got := f.eoE2ETimerStatuses(t, parent.ID); len(got) != 1 || got[0] != "cancelled" {
		t.Errorf("step 5 — escalation timers of the closed parent = %v, want one cancelled", got)
	}

	// ── 6. the roll-up recomputed on the transition path too ─────────────
	//
	// The parent's stored roll-up was left at 1 after step 3 and nothing
	// re-saved it while childB was created, so the only thing that can have
	// moved it to 2 is the closing transition's own recompute — which runs
	// inside the transaction, on the transaction's repository handles.
	if got := eoE2EValues(t, closedParent.Values)["child_task_count"]; got != float64(2) {
		t.Errorf("step 6 — the closing transition returned a roll-up of %v, want 2", got)
	}
	if got := f.eoE2EStoredRollup(t, parent); got != 2 {
		t.Errorf("step 6 — the stored roll-up is %v after closure, want 2", got)
	}
}
