package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/automax/backend/internal/database"
	"github.com/automax/backend/internal/database/migrations"
	"github.com/automax/backend/internal/models"
)

// ── the fixture ─────────────────────────────────────────────────────────────

// eoSignoffTestDB is a *gorm.DB on a freshly created, uniquely named Postgres
// schema holding the EO platform, the pilot configuration as the real seed
// writes it, and the action_logs table the discharge records itself in. The
// schema is dropped when the test ends, and the test is skipped unless
// AUTOMAX_TEST_DB_DSN is set.
//
// It is database.eoPilotDB's shape, reproduced here because that helper is
// unexported: one pinned connection so the session-level search_path applies
// to every statement, which is safe because the only transaction this package
// opens on it is the discharge's own and nothing reads outside it.
//
// THE CONFIGURATION UNDER TEST IS THE SEEDED ONE. A hand-built fixture with
// three marked labels would pass while the route disagreed with the boot
// report about every real one.
func eoSignoffTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("AUTOMAX_TEST_DB_DSN")
	if dsn == "" {
		t.Skip("set AUTOMAX_TEST_DB_DSN to run DB tests")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("random schema suffix: %v", err)
	}
	schema := "eo_signoff_test_" + hex.EncodeToString(suffix)

	t.Cleanup(func() {
		if err := db.Exec(`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`).Error; err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
		sqlDB.Close()
	})

	if err := db.Exec(`CREATE SCHEMA "` + schema + `"`).Error; err != nil {
		t.Fatalf("create schema %s: %v", schema, err)
	}
	if err := db.Exec(`SET search_path TO "` + schema + `"`).Error; err != nil {
		t.Fatalf("set search_path: %v", err)
	}
	// models.User.CallStatus is a Postgres ENUM that database.Migrate creates
	// in the schema it migrates; a fresh schema needs its own copy before the
	// users table (reached from action_logs and the workflow relations) can be
	// created.
	if err := db.Exec(`CREATE TYPE user_call_status AS ENUM ('offline', 'online', 'busy', 'in_call', 'available')`).Error; err != nil {
		t.Fatalf("create user_call_status enum: %v", err)
	}
	migrationDB := db.Session(&gorm.Session{})
	migrationDB.Config.DisableForeignKeyConstraintWhenMigrating = true
	// AutoMigrate, not Migrator().CreateTable: only AutoMigrate also creates
	// the many2many join tables the seed writes through.
	if err := migrationDB.AutoMigrate(eoSignoffTables()...); err != nil {
		t.Fatalf("create tables: %v", err)
	}
	// eo_records is LIST-partitioned and cannot be expressed by AutoMigrate;
	// the pilot seed asks whether records exist, so the real table is needed.
	if err := migrations.MigrateEOPlatform(db); err != nil {
		t.Fatalf("migrate eo platform: %v", err)
	}

	for _, r := range []models.Role{
		{Name: "Administrator", Code: "admin", IsSystem: true, IsActive: true},
		{Name: "Executive Office", Code: "executive_office", IsSystem: true, IsActive: true},
	} {
		if err := db.Create(&r).Error; err != nil {
			t.Fatalf("create role %s: %v", r.Code, err)
		}
	}
	database.SeedEOPlatform(db)
	database.SeedEOPilot(db)
	return db
}

// eoSignoffTables is every table the two seeds and the discharge touch that
// migrations.MigrateEOPlatform does not create.
func eoSignoffTables() []any {
	return []any{
		&models.Permission{},
		&models.Role{},
		&models.User{},
		&models.ActionLog{},
		&models.EscalationPolicy{},
		&models.EscalationPolicyStep{},
		&models.Workflow{},
		&models.WorkflowState{},
		&models.WorkflowTransition{},
		&models.TransitionRequirement{},
		&models.TransitionAction{},
		&models.TransitionFieldChange{},
		&models.EORecordType{},
		&models.EOFieldDef{},
		&models.EOLinkType{},
		&models.EOSeedStep{},
		&models.LookupCategory{},
		&models.LookupValue{},
	}
}

// eoSignoffApp registers the real routes behind a stand-in for
// authMiddleware.RequirePermission holding exactly `granted`, with `actor` in
// Locals as the authenticated user.
func eoSignoffApp(db *gorm.DB, actor *models.User, granted ...string) *fiber.App {
	app := fiber.New()
	app.Use(eoTestActor(actor))
	NewEOSignoffHandler(db).RegisterRoutes(app.Group("/eo-signoff"), eoPermFor(granted...))
	return app
}

// eoSignoffActor is a caller with an id, which is what the discharge records
// the sign-off against. It is NOT a super admin: eoPermFor has no bypass, and
// a super admin here would make the permission assertions vacuous.
func eoSignoffActor(t *testing.T, db *gorm.DB) *models.User {
	t.Helper()
	u := &models.User{
		ID: uuid.New(), Email: "eo.registrar@example.test", Username: "eo_registrar",
		Password: "x", FirstName: "Reg", LastName: "Istrar", IsActive: true,
	}
	if err := db.Omit("Roles", "Departments", "Locations", "Classifications").Create(u).Error; err != nil {
		t.Fatalf("create actor: %v", err)
	}
	return u
}

// ── the wire shapes the tests read ──────────────────────────────────────────

type eoSignoffTestLabel struct {
	ID       string `json:"id"`
	TypeKey  string `json:"type_key"`
	FieldKey string `json:"field_key"`
	Label    string `json:"label"`
}

type eoSignoffTestStep struct {
	ID           string `json:"id"`
	WorkflowCode string `json:"workflow_code"`
	Code         string `json:"code"`
	Name         string `json:"name"`
}

type eoSignoffTestList struct {
	Success bool `json:"success"`
	Data    struct {
		UntranslatedLabels     []eoSignoffTestLabel `json:"untranslated_labels"`
		UnconfirmedStates      []eoSignoffTestStep  `json:"unconfirmed_states"`
		UnconfirmedTransitions []eoSignoffTestStep  `json:"unconfirmed_transitions"`
		Counts                 struct {
			UntranslatedLabels     int `json:"untranslated_labels"`
			UnconfirmedStates      int `json:"unconfirmed_states"`
			UnconfirmedTransitions int `json:"unconfirmed_transitions"`
		} `json:"counts"`
	} `json:"data"`
}

type eoSignoffTestDischarge struct {
	Success bool   `json:"success"`
	Error   string `json:"error"`
	Data    struct {
		Discharged struct {
			FieldDefs   int `json:"field_defs"`
			States      int `json:"states"`
			Transitions int `json:"transitions"`
		} `json:"discharged"`
		AlreadyClear []string `json:"already_clear"`
	} `json:"data"`
}

func eoSignoffGet(t *testing.T, app *fiber.App) eoSignoffTestList {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest("GET", "/eo-signoff", nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("GET /eo-signoff status = %d, want 200", resp.StatusCode)
	}
	var body eoSignoffTestList
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

func eoSignoffDischarge(t *testing.T, app *fiber.App, payload string) (int, eoSignoffTestDischarge) {
	t.Helper()
	req := httptest.NewRequest("POST", "/eo-signoff/discharge", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	var body eoSignoffTestDischarge
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

// eoSignoffMarked counts the rows still carrying each marker, straight from
// the tables. The route's own list is what is under test, so the invariants
// are asserted against the database rather than against the route's answer.
func eoSignoffMarked(t *testing.T, db *gorm.DB) (labels, states, transitions int64) {
	t.Helper()
	if err := db.Model(&models.EOFieldDef{}).Where("ar_untranslated = ?", true).Count(&labels).Error; err != nil {
		t.Fatalf("count marked labels: %v", err)
	}
	if err := db.Model(&models.WorkflowState{}).Where("eo_client_unconfirmed = ?", true).Count(&states).Error; err != nil {
		t.Fatalf("count unconfirmed states: %v", err)
	}
	if err := db.Model(&models.WorkflowTransition{}).Where("eo_client_unconfirmed = ?", true).Count(&transitions).Error; err != nil {
		t.Fatalf("count unconfirmed transitions: %v", err)
	}
	return labels, states, transitions
}

// ── the tests ───────────────────────────────────────────────────────────────

// eoSignoffSeededLabels, eoSignoffSeededStates and eoSignoffSeededTransitions
// are the pilot's obligations as the seed leaves them. They are the numbers
// the boot report prints and the numbers the client's sign-off round works
// from, so a change to them is a change to what the client was told is
// outstanding and must be a deliberate one.
//
// Labels: 127 as Plan 6 left them, then two client rulings on the Directive.
// Its own `source` (derived from directive_origin) and its own
// `assigned_user_id` (required) each copy the base definition and with it the
// base's awaiting-Arabic marker (+2); `responsible_officer` is removed (-1).
const (
	eoSignoffSeededLabels      = 128
	eoSignoffSeededStates      = 37
	eoSignoffSeededTransitions = 27
)

// TestEOSignoffListsUntranslatedLabels is the live query for the Arabic
// obligation. Until this route existed the only way to ask was the boot log of
// the day the database was created.
func TestEOSignoffListsUntranslatedLabels(t *testing.T) {
	db := eoSignoffTestDB(t)
	app := eoSignoffApp(db, eoSignoffActor(t, db), "eo:types:manage")

	body := eoSignoffGet(t, app)
	if got := len(body.Data.UntranslatedLabels); got != eoSignoffSeededLabels {
		t.Errorf("the route lists %d field labels awaiting Arabic, want %d", got, eoSignoffSeededLabels)
	}
	if body.Data.Counts.UntranslatedLabels != len(body.Data.UntranslatedLabels) {
		t.Errorf("count says %d and the list holds %d", body.Data.Counts.UntranslatedLabels, len(body.Data.UntranslatedLabels))
	}

	// The list must agree with the marker column itself, which is what the
	// boot report counts.
	marked, _, _ := eoSignoffMarked(t, db)
	if int64(len(body.Data.UntranslatedLabels)) != marked {
		t.Errorf("the route lists %d labels and %d rows carry ar_untranslated", len(body.Data.UntranslatedLabels), marked)
	}

	// Each entry must be actionable: an id to discharge and the type and
	// field a translator needs in order to know what to translate.
	for _, l := range body.Data.UntranslatedLabels {
		if l.ID == "" || l.TypeKey == "" || l.FieldKey == "" {
			t.Fatalf("entry %+v is not actionable: it needs an id, a type key and a field key", l)
		}
		if _, err := uuid.Parse(l.ID); err != nil {
			t.Fatalf("entry id %q is not a uuid", l.ID)
		}
	}
}

// TestEOSignoffListsUnconfirmedStates is the same for the derived state
// machines: 37 states and 27 transitions were built from the client's field
// matrix rather than transcribed from the CFFs, and nobody has signed them
// off.
func TestEOSignoffListsUnconfirmedStates(t *testing.T) {
	db := eoSignoffTestDB(t)
	app := eoSignoffApp(db, eoSignoffActor(t, db), "eo:types:manage")

	body := eoSignoffGet(t, app)
	if got := len(body.Data.UnconfirmedStates); got != eoSignoffSeededStates {
		t.Errorf("the route lists %d states awaiting sign-off, want %d", got, eoSignoffSeededStates)
	}
	if got := len(body.Data.UnconfirmedTransitions); got != eoSignoffSeededTransitions {
		t.Errorf("the route lists %d transitions awaiting sign-off, want %d", got, eoSignoffSeededTransitions)
	}

	_, states, transitions := eoSignoffMarked(t, db)
	if int64(len(body.Data.UnconfirmedStates)) != states || int64(len(body.Data.UnconfirmedTransitions)) != transitions {
		t.Errorf("the route lists %d/%d and the columns carry %d/%d",
			len(body.Data.UnconfirmedStates), len(body.Data.UnconfirmedTransitions), states, transitions)
	}
	for _, s := range append(body.Data.UnconfirmedStates, body.Data.UnconfirmedTransitions...) {
		if s.ID == "" || s.WorkflowCode == "" || s.Code == "" || s.Name == "" {
			t.Fatalf("entry %+v is not actionable: it needs an id, a workflow code, a code and a name", s)
		}
	}
}

// TestEOSignoffDischargeClearsOnlyNamedRows is the whole point of the marker:
// a human looked at THIS one. Discharging one field label must leave the other
// 126 exactly as they were, and the COUNT is what says so — a spot check on
// one neighbour would pass against a clear-all.
func TestEOSignoffDischargeClearsOnlyNamedRows(t *testing.T) {
	db := eoSignoffTestDB(t)
	app := eoSignoffApp(db, eoSignoffActor(t, db), "eo:types:manage")

	before := eoSignoffGet(t, app)
	target := before.Data.UntranslatedLabels[0]
	targetState := before.Data.UnconfirmedStates[0]

	status, out := eoSignoffDischarge(t, app, fmt.Sprintf(
		`{"field_def_ids":[%q],"state_ids":[%q]}`, target.ID, targetState.ID))
	if status != fiber.StatusOK {
		t.Fatalf("discharge status = %d, want 200 (%s)", status, out.Error)
	}
	if out.Data.Discharged.FieldDefs != 1 || out.Data.Discharged.States != 1 || out.Data.Discharged.Transitions != 0 {
		t.Errorf("discharged %+v, want one field def and one state", out.Data.Discharged)
	}

	labels, states, transitions := eoSignoffMarked(t, db)
	if labels != eoSignoffSeededLabels-1 {
		t.Errorf("%d labels still marked after discharging ONE, want %d: the discharge cleared rows it was not asked to",
			labels, eoSignoffSeededLabels-1)
	}
	if states != eoSignoffSeededStates-1 {
		t.Errorf("%d states still unconfirmed after discharging ONE, want %d", states, eoSignoffSeededStates-1)
	}
	if transitions != eoSignoffSeededTransitions {
		t.Errorf("%d transitions still unconfirmed, want all %d: nothing named one", transitions, eoSignoffSeededTransitions)
	}

	// And the named rows are the ones that changed.
	var def models.EOFieldDef
	if err := db.Where("id = ?", target.ID).First(&def).Error; err != nil {
		t.Fatalf("read the discharged field def: %v", err)
	}
	if def.ArUntranslated {
		t.Errorf("field def %s (%s.%s) is still marked", target.ID, target.TypeKey, target.FieldKey)
	}

	after := eoSignoffGet(t, app)
	if len(after.Data.UntranslatedLabels) != eoSignoffSeededLabels-1 {
		t.Errorf("the list still shows %d labels, want %d: it is not reading the database now",
			len(after.Data.UntranslatedLabels), eoSignoffSeededLabels-1)
	}
	for _, l := range after.Data.UntranslatedLabels {
		if l.ID == target.ID {
			t.Errorf("the discharged label %s is still listed as outstanding", target.ID)
		}
	}
}

// TestEOSignoffDischargeIsAudited: the discharge asserts that a human reviewed
// something, so the record of WHO and WHEN, and of WHICH ROWS, is the point of
// the route rather than a side effect of it.
func TestEOSignoffDischargeIsAudited(t *testing.T) {
	db := eoSignoffTestDB(t)
	actor := eoSignoffActor(t, db)
	app := eoSignoffApp(db, actor, "eo:types:manage")

	before := eoSignoffGet(t, app)
	target := before.Data.UntranslatedLabels[0]

	status, out := eoSignoffDischarge(t, app, fmt.Sprintf(`{"field_def_ids":[%q]}`, target.ID))
	if status != fiber.StatusOK {
		t.Fatalf("discharge status = %d, want 200 (%s)", status, out.Error)
	}

	var logs []models.ActionLog
	if err := db.Find(&logs).Error; err != nil {
		t.Fatalf("read action logs: %v", err)
	}
	// EXACTLY ONE HERE, TWO IN THE DEPLOYED SYSTEM, and the difference is not
	// a defect. This app registers the handler alone; cmd/server/main.go also
	// puts middleware.ActionLogger on the group, which writes its own row for
	// the CALL (module from the path, action "create", no request body). So
	// an operator reading the action-log screen sees two rows per discharge —
	// the middleware's, saying somebody posted to the route, and this one,
	// naming the rows. Nobody reconciling this count against that screen
	// should conclude one of them is missing.
	if len(logs) != 1 {
		t.Fatalf("%d action log entries after one discharge, want exactly 1 (this app has no ActionLogger middleware)", len(logs))
	}
	entry := logs[0]
	if entry.UserID != actor.ID {
		t.Errorf("the entry records user %s, want the caller %s", entry.UserID, actor.ID)
	}
	if entry.CreatedAt.IsZero() {
		t.Error("the entry has no timestamp, so it says when nothing")
	}
	if !strings.Contains(entry.NewValue, target.ID) {
		t.Errorf("the entry does not name the discharged row %s; new_value = %q", target.ID, entry.NewValue)
	}
	if entry.Module == "" || entry.Action == "" {
		t.Errorf("the entry is not findable: module = %q, action = %q", entry.Module, entry.Action)
	}

	// A refused discharge must leave no entry claiming one happened.
	status, _ = eoSignoffDischarge(t, app, fmt.Sprintf(`{"field_def_ids":[%q]}`, uuid.New()))
	if status == fiber.StatusOK {
		t.Fatalf("discharging an unknown id answered 200")
	}
	var n int64
	if err := db.Model(&models.ActionLog{}).Count(&n).Error; err != nil {
		t.Fatalf("count action logs: %v", err)
	}
	if n != 1 {
		t.Errorf("%d action log entries after a REFUSED discharge, want the original 1", n)
	}
}

// TestEOSignoffRequiresTypesManage. Both routes name the type designer's own
// code, and a caller without it is refused with 403 — never 401, which the
// frontend's axios interceptor treats as a logout.
func TestEOSignoffRequiresTypesManage(t *testing.T) {
	db := eoSignoffTestDB(t)
	actor := eoSignoffActor(t, db)
	viewer := eoSignoffApp(db, actor, "eo:view")
	manager := eoSignoffApp(db, actor, "eo:types:manage")

	cases := []struct {
		name, method, path, body string
	}{
		{"List", "GET", "/eo-signoff", ""},
		{"Discharge", "POST", "/eo-signoff/discharge", `{"field_def_ids":[]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			resp, err := viewer.Test(req)
			if err != nil {
				t.Fatalf("app.Test: %v", err)
			}
			if resp.StatusCode != fiber.StatusForbidden {
				t.Fatalf("%s %s status = %d for a caller holding only eo:view, want 403", tc.method, tc.path, resp.StatusCode)
			}

			// Control: the route exists and is reachable with the code, so
			// the 403 above is a refusal and not a 404 in disguise.
			ctl := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			ctl.Header.Set("Content-Type", "application/json")
			ctlResp, err := manager.Test(ctl)
			if err != nil {
				t.Fatalf("app.Test (manage): %v", err)
			}
			if ctlResp.StatusCode == fiber.StatusForbidden || ctlResp.StatusCode == fiber.StatusNotFound ||
				ctlResp.StatusCode == fiber.StatusUnauthorized {
				t.Fatalf("%s %s status = %d for a caller holding eo:types:manage, want the route reachable",
					tc.method, tc.path, ctlResp.StatusCode)
			}
		})
	}
}

// TestEOSignoffSeesATypeAddedAfterTheSeed is the live-query guard. Plan 2
// shipped a seed step whose gate could never open on the databases it was
// written for, and the obligation report inherited the same one-shot gate: a
// list computed when the pilot seed ran would answer the same thing for ever
// while the configuration moved underneath it.
func TestEOSignoffSeesATypeAddedAfterTheSeed(t *testing.T) {
	db := eoSignoffTestDB(t)
	app := eoSignoffApp(db, eoSignoffActor(t, db), "eo:types:manage")

	before := eoSignoffGet(t, app)

	// A twelfth record type, created AFTER the pilot seed has run, with one
	// field whose Arabic has not been signed off.
	ty := models.EORecordType{
		ID: uuid.New(), Key: "eo_twelfth_type", Name: "Twelfth Type", NameAr: "Twelfth Type",
		IsAbstract: false, IsActive: true,
	}
	if err := db.Create(&ty).Error; err != nil {
		t.Fatalf("create the twelfth type: %v", err)
	}
	def := models.EOFieldDef{
		ID: uuid.New(), RecordTypeID: ty.ID, Key: "late_field",
		Label: "Late field", LabelAr: "Late field", Kind: "text", ArUntranslated: true,
	}
	if err := db.Create(&def).Error; err != nil {
		t.Fatalf("create the late field def: %v", err)
	}

	after := eoSignoffGet(t, app)
	if len(after.Data.UntranslatedLabels) != len(before.Data.UntranslatedLabels)+1 {
		t.Fatalf("the list holds %d labels and held %d before a marked field was added: it is a snapshot, not a live query",
			len(after.Data.UntranslatedLabels), len(before.Data.UntranslatedLabels))
	}
	found := false
	for _, l := range after.Data.UntranslatedLabels {
		if l.ID == def.ID.String() {
			found = true
			if l.TypeKey != ty.Key || l.FieldKey != def.Key {
				t.Errorf("the late field is listed as %s.%s, want %s.%s", l.TypeKey, l.FieldKey, ty.Key, def.Key)
			}
		}
	}
	if !found {
		t.Fatalf("the field added after the seed is not in the list")
	}

	// And it can be discharged like any other.
	status, out := eoSignoffDischarge(t, app, fmt.Sprintf(`{"field_def_ids":[%q]}`, def.ID))
	if status != fiber.StatusOK || out.Data.Discharged.FieldDefs != 1 {
		t.Fatalf("discharging the late field: status %d, discharged %+v (%s)", status, out.Data.Discharged, out.Error)
	}
}

// TestEOSignoffDischargeRefusesWhatItCannotIdentify. There is deliberately no
// "clear all": an id the database does not know is a mistake in the list the
// operator is working from, and applying the rest of the request silently
// would discharge rows while the operator believed the whole batch had failed.
func TestEOSignoffDischargeRefusesWhatItCannotIdentify(t *testing.T) {
	db := eoSignoffTestDB(t)
	app := eoSignoffApp(db, eoSignoffActor(t, db), "eo:types:manage")

	before := eoSignoffGet(t, app)
	real := before.Data.UntranslatedLabels[0].ID

	// An empty request is refused: it is a no-op that would otherwise answer
	// 200 and look like a sign-off.
	if status, _ := eoSignoffDischarge(t, app, `{}`); status != fiber.StatusBadRequest {
		t.Errorf("an empty discharge answered %d, want 400", status)
	}

	// One good id and one unknown one: NOTHING is applied.
	status, out := eoSignoffDischarge(t, app, fmt.Sprintf(`{"field_def_ids":[%q,%q]}`, real, uuid.New()))
	if status != fiber.StatusBadRequest {
		t.Errorf("a discharge naming an unknown id answered %d, want 400 (%s)", status, out.Error)
	}
	labels, _, _ := eoSignoffMarked(t, db)
	if labels != eoSignoffSeededLabels {
		t.Errorf("%d labels still marked after a REFUSED discharge, want all %d", labels, eoSignoffSeededLabels)
	}

	// Discharging an already-clear row is accepted and reported, so a retry
	// of a batch that half-succeeded is not an error.
	if status, _ := eoSignoffDischarge(t, app, fmt.Sprintf(`{"field_def_ids":[%q]}`, real)); status != fiber.StatusOK {
		t.Fatalf("the first discharge answered %d", status)
	}
	status, out = eoSignoffDischarge(t, app, fmt.Sprintf(`{"field_def_ids":[%q]}`, real))
	if status != fiber.StatusOK {
		t.Errorf("re-discharging an already-clear row answered %d, want 200 (%s)", status, out.Error)
	}
	if out.Data.Discharged.FieldDefs != 0 || len(out.Data.AlreadyClear) != 1 {
		t.Errorf("the retry reported %+v / already clear %v, want nothing discharged and one already clear",
			out.Data.Discharged, out.Data.AlreadyClear)
	}
}

// TestEOSignoffDischargeNeverCallsAMarkedRowClear is fix round 1's F1.
//
// The outstanding list joins each row's parent with deleted_at IS NULL, so a
// row that is ALIVE and STILL MARKED under a soft-deleted record type or
// workflow is missing from the list while its marker stands. Partitioning on
// absence from that list put such a row in `already_clear`: the route answered
// 200 and the audit entry durably recorded that the id had needed no action,
// which is the opposite of the truth. The scenario is ordinary — an
// administrator retires a pilot type between the client producing a signed-off
// list and somebody running the discharge.
//
// Both halves are covered: a field definition under a deleted record type, and
// a workflow state under a deleted workflow.
func TestEOSignoffDischargeNeverCallsAMarkedRowClear(t *testing.T) {
	db := eoSignoffTestDB(t)
	app := eoSignoffApp(db, eoSignoffActor(t, db), "eo:types:manage")

	before := eoSignoffGet(t, app)
	label := before.Data.UntranslatedLabels[0]
	state := before.Data.UnconfirmedStates[0]

	// Soft-delete the parents, leaving both rows alive and still marked.
	var def models.EOFieldDef
	if err := db.Where("id = ?", label.ID).First(&def).Error; err != nil {
		t.Fatalf("read the field def: %v", err)
	}
	if err := db.Delete(&models.EORecordType{}, "id = ?", def.RecordTypeID).Error; err != nil {
		t.Fatalf("soft-delete the record type: %v", err)
	}
	var st models.WorkflowState
	if err := db.Where("id = ?", state.ID).First(&st).Error; err != nil {
		t.Fatalf("read the state: %v", err)
	}
	if err := db.Delete(&models.Workflow{}, "id = ?", st.WorkflowID).Error; err != nil {
		t.Fatalf("soft-delete the workflow: %v", err)
	}

	cases := []struct {
		name, payload, id string
		model             any
		marker            string
	}{
		{"FieldDef", fmt.Sprintf(`{"field_def_ids":[%q]}`, label.ID), label.ID, &models.EOFieldDef{}, "ar_untranslated"},
		{"State", fmt.Sprintf(`{"state_ids":[%q]}`, state.ID), state.ID, &models.WorkflowState{}, "eo_client_unconfirmed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, out := eoSignoffDischarge(t, app, tc.payload)
			for _, id := range out.Data.AlreadyClear {
				if id == tc.id {
					t.Errorf("%s is reported as ALREADY CLEAR while its marker is still set; the audit entry would record that it needed no action", id)
				}
			}
			if status != fiber.StatusBadRequest {
				t.Errorf("discharging a still-marked row hidden by a deleted parent answered %d, want 400 (%s)", status, out.Error)
			}
			var still bool
			if err := db.Model(tc.model).Where("id = ?", tc.id).Pluck(tc.marker, &still).Error; err != nil {
				t.Fatalf("read the marker back: %v", err)
			}
			if !still {
				t.Errorf("the marker on %s was cleared by a request that answered %d", tc.id, status)
			}
		})
	}
}

// TestEOSignoffDischargeRefusesTheZeroUUID is fix round 1's M2. The zero uuid
// used to be dropped before the partition saw it, so a script that emitted an
// empty cell among forty ids got "39 discharged" against forty pasted lines,
// with an audit entry naming 39 and nothing saying one had been discarded. The
// all-or-nothing contract has no exception for an id that happens to be zero.
func TestEOSignoffDischargeRefusesTheZeroUUID(t *testing.T) {
	db := eoSignoffTestDB(t)
	app := eoSignoffApp(db, eoSignoffActor(t, db), "eo:types:manage")

	before := eoSignoffGet(t, app)
	real := before.Data.UntranslatedLabels[0].ID

	status, out := eoSignoffDischarge(t, app, fmt.Sprintf(`{"field_def_ids":[%q,%q]}`, uuid.Nil, real))
	if status != fiber.StatusBadRequest {
		t.Errorf("a discharge naming the zero uuid answered %d, want 400 (%s)", status, out.Error)
	}
	if out.Data.Discharged.FieldDefs != 0 {
		t.Errorf("the zero uuid was dropped and the rest applied: discharged %+v", out.Data.Discharged)
	}
	labels, _, _ := eoSignoffMarked(t, db)
	if labels != eoSignoffSeededLabels {
		t.Errorf("%d labels still marked after a request carrying the zero uuid, want all %d", labels, eoSignoffSeededLabels)
	}
}

// TestEOSignoffDischargeIsAtomicAcrossBuckets is fix round 1's item 4, made
// order-independent in round 2. The all-or-nothing claim rests on the whole
// request being one transaction, and the only test of it put both ids in ONE
// bucket — where the partition fails before any write, so the transaction was
// never exercised.
//
// IT RUNS BOTH DIRECTIONS, and that is what makes it a test rather than a
// coincidence. A single direction proves a rollback only while `plan` happens
// to visit the valid bucket first; reordering `plan` made the round-1 version
// pass vacuously. Whatever order the buckets are visited in, one of these two
// cases has its valid UPDATE executed before the other bucket refuses, so the
// pair cannot all pass without a real rollback.
func TestEOSignoffDischargeIsAtomicAcrossBuckets(t *testing.T) {
	db := eoSignoffTestDB(t)
	app := eoSignoffApp(db, eoSignoffActor(t, db), "eo:types:manage")

	before := eoSignoffGet(t, app)
	label := before.Data.UntranslatedLabels[0].ID
	state := before.Data.UnconfirmedStates[0].ID

	cases := []struct {
		name, payload, valid string
		retry                string
	}{
		{
			"ValidLabelUnknownState",
			fmt.Sprintf(`{"field_def_ids":[%q],"state_ids":[%q]}`, label, uuid.New()),
			label, fmt.Sprintf(`{"field_def_ids":[%q]}`, label),
		},
		{
			"ValidStateUnknownLabel",
			fmt.Sprintf(`{"field_def_ids":[%q],"state_ids":[%q]}`, uuid.New(), state),
			state, fmt.Sprintf(`{"state_ids":[%q]}`, state),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, out := eoSignoffDischarge(t, app, tc.payload)
			if status != fiber.StatusBadRequest {
				t.Fatalf("a valid id in one bucket with an unknown id in another answered %d, want 400 (%s)", status, out.Error)
			}
			labels, states, transitions := eoSignoffMarked(t, db)
			if labels != eoSignoffSeededLabels || states != eoSignoffSeededStates || transitions != eoSignoffSeededTransitions {
				t.Errorf("markers = %d/%d/%d, want %d/%d/%d: a write in another bucket was not rolled back",
					labels, states, transitions,
					eoSignoffSeededLabels, eoSignoffSeededStates, eoSignoffSeededTransitions)
			}
			var n int64
			if err := db.Model(&models.ActionLog{}).Count(&n).Error; err != nil {
				t.Fatalf("count action logs: %v", err)
			}
			if n != 0 {
				t.Errorf("%d audit entries after a rolled-back discharge, want none", n)
			}

			// Non-vacuity: the valid id WAS dischargeable, so the assertions
			// above are about a write that bucket really attempted (when it
			// ran first) rather than about an id that would have been
			// rejected anyway. The row is put back afterwards so the next
			// case starts from the seeded counts.
			status, out = eoSignoffDischarge(t, app, tc.retry)
			if status != fiber.StatusOK || out.Data.Discharged.FieldDefs+out.Data.Discharged.States != 1 {
				t.Fatalf("the valid id alone answered %d / %+v, so the rollback above proved nothing (%s)",
					status, out.Data.Discharged, out.Error)
			}
			if err := db.Model(&models.EOFieldDef{}).Where("id = ?", tc.valid).
				Update("ar_untranslated", true).Error; err != nil {
				t.Fatalf("restore the label marker: %v", err)
			}
			if err := db.Model(&models.WorkflowState{}).Where("id = ?", tc.valid).
				Update("eo_client_unconfirmed", true).Error; err != nil {
				t.Fatalf("restore the state marker: %v", err)
			}
			if err := db.Where("1 = 1").Delete(&models.ActionLog{}).Error; err != nil {
				t.Fatalf("clear the action log: %v", err)
			}
		})
	}
}

// TestEOSignoffDischargeRefusesAnIDSentInTheWrongBucket is fix round 2's
// item 1. The three kinds live in three tables and an id says nothing about
// which, so pasting the wrong spreadsheet column is a plausible operator
// mistake. It used to answer 500: the outstanding set was one map shared by
// all three buckets, so an OUTSTANDING state id looked outstanding to the
// transition bucket, reached an UPDATE that matched nothing, and tripped the
// short-count guard. Both directions are covered, and the ids are deliberately
// OUTSTANDING ones — an unlisted id would have been refused either way and
// would prove nothing.
func TestEOSignoffDischargeRefusesAnIDSentInTheWrongBucket(t *testing.T) {
	db := eoSignoffTestDB(t)
	app := eoSignoffApp(db, eoSignoffActor(t, db), "eo:types:manage")

	before := eoSignoffGet(t, app)
	state := before.Data.UnconfirmedStates[0].ID
	transition := before.Data.UnconfirmedTransitions[0].ID

	cases := []struct{ name, payload string }{
		{"StateIDInTransitionIDs", fmt.Sprintf(`{"transition_ids":[%q]}`, state)},
		{"TransitionIDInStateIDs", fmt.Sprintf(`{"state_ids":[%q]}`, transition)},
		{"LabelIDInStateIDs", fmt.Sprintf(`{"state_ids":[%q]}`, before.Data.UntranslatedLabels[0].ID)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, out := eoSignoffDischarge(t, app, tc.payload)
			if status != fiber.StatusBadRequest {
				t.Errorf("an id sent in the wrong bucket answered %d, want 400 — not a server error on an operator's paste (%s)",
					status, out.Error)
			}
			labels, states, transitions := eoSignoffMarked(t, db)
			if labels != eoSignoffSeededLabels || states != eoSignoffSeededStates || transitions != eoSignoffSeededTransitions {
				t.Errorf("markers = %d/%d/%d, want the seeded %d/%d/%d",
					labels, states, transitions,
					eoSignoffSeededLabels, eoSignoffSeededStates, eoSignoffSeededTransitions)
			}
		})
	}
}
