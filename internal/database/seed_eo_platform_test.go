package database

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/automax/backend/internal/models"
)

// eoSeedTestDB returns a *gorm.DB bound to a freshly created, uniquely named
// Postgres schema holding only the tables seedEOPlatform writes to. The schema
// is dropped when the test ends. It skips the test unless AUTOMAX_TEST_DB_DSN
// is set.
//
// It is the shape of repository.eoTestDB — the pool is pinned to one
// connection so the session-level search_path applies to every statement — and
// that pin is safe here because nothing in this package opens a transaction
// that then reads on a second connection. The services package's end-to-end
// test needs a real pool and so builds its handle differently
// (services.eoServicesTestDB).
//
// database.Connect and database.Migrate are deliberately NOT used: the first
// assigns the package global DB and the second builds the whole application
// schema, neither of which a test may do to a shared database.
func eoSeedTestDB(t *testing.T) *gorm.DB {
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
	schema := "eo_seed_test_" + hex.EncodeToString(suffix)

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

	// AutoMigrate reaches the users table through the workflow relations, and
	// models.User.CallStatus is a Postgres ENUM that Migrate creates in the
	// schema it migrates. A fresh schema needs its own copy.
	if err := db.Exec(`CREATE TYPE user_call_status AS ENUM ('offline', 'online', 'busy', 'in_call', 'available')`).Error; err != nil {
		t.Fatalf("create user_call_status enum: %v", err)
	}
	migrationDB := db.Session(&gorm.Session{})
	migrationDB.Config.DisableForeignKeyConstraintWhenMigrating = true
	// AutoMigrate, not Migrator().CreateTable: only AutoMigrate also creates
	// the many2many join tables (role_permissions, transition_allowed_roles)
	// that the seed writes through.
	if err := migrationDB.AutoMigrate(eoSeedTables()...); err != nil {
		t.Fatalf("create seed tables: %v", err)
	}
	return db
}

// eoSeedTables is every table seedEOPlatform reads or writes, parents first.
func eoSeedTables() []any {
	return []any{
		&models.Permission{},
		&models.Role{},
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

// eoSeedRoles creates the roles seedEOPlatform grants its per-type permission
// codes to. In production these come from the role block of Seed, which runs
// first; here they are the precondition the seed is given.
func eoSeedRoles(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, r := range []models.Role{
		{Name: "Administrator", Code: "admin", IsSystem: true, IsActive: true},
		{Name: "Executive Office", Code: "executive_office", IsSystem: true, IsActive: true},
		// Task B5: the pilot seed grants register codes to these six, and
		// does not record that grant as applied while any is absent, so a
		// fixture without them would re-run the seed on every boot. In
		// production the role block of Seed creates all of them first.
		{Name: "Head of Office", Code: "head_of_office", IsSystem: true, IsActive: true},
		{Name: "Public Relations", Code: "public_relations", IsSystem: true, IsActive: true},
		{Name: "Protocol", Code: "protocol", IsSystem: true, IsActive: true},
		{Name: "Department User", Code: "department_user", IsSystem: true, IsActive: true},
		{Name: "Escalation Manager", Code: "escalation_manager", IsSystem: true, IsActive: true},
		{Name: "EO Administrator", Code: "eo_admin", IsSystem: true, IsActive: true},
	} {
		if err := db.Create(&r).Error; err != nil {
			t.Fatalf("create role %s: %v", r.Code, err)
		}
	}
}

func eoCountRows(t *testing.T, db *gorm.DB, model any) int64 {
	t.Helper()
	var n int64
	if err := db.Model(model).Count(&n).Error; err != nil {
		t.Fatalf("count %T: %v", model, err)
	}
	return n
}

// TestSeedEOPlatformIsIdempotent runs the seed twice over one schema. The
// second run must write nothing: the seed runs on every container boot, so a
// second pass that duplicated field definitions would double the base type's
// form on every restart.
func TestSeedEOPlatformIsIdempotent(t *testing.T) {
	db := eoSeedTestDB(t)
	eoSeedRoles(t, db)

	seedEOPlatform(db)
	fields := eoCountRows(t, db, &models.EOFieldDef{})
	types := eoCountRows(t, db, &models.EORecordType{})
	links := eoCountRows(t, db, &models.EOLinkType{})
	workflows := eoCountRows(t, db, &models.Workflow{})
	states := eoCountRows(t, db, &models.WorkflowState{})
	transitions := eoCountRows(t, db, &models.WorkflowTransition{})
	perms := eoCountRows(t, db, &models.Permission{})
	if fields == 0 || types == 0 {
		t.Fatalf("first run seeded nothing: %d field definitions over %d record types", fields, types)
	}

	seedEOPlatform(db)
	for _, c := range []struct {
		label string
		want  int64
		model any
	}{
		{"field definitions", fields, &models.EOFieldDef{}},
		{"record types", types, &models.EORecordType{}},
		{"link types", links, &models.EOLinkType{}},
		{"workflows", workflows, &models.Workflow{}},
		{"workflow states", states, &models.WorkflowState{}},
		{"workflow transitions", transitions, &models.WorkflowTransition{}},
		{"permissions", perms, &models.Permission{}},
	} {
		if got := eoCountRows(t, db, c.model); got != c.want {
			t.Errorf("after a second seed run there are %d %s, want %d", got, c.label, c.want)
		}
	}
}

// TestBaseTypeHasSeventeenInheritedFields pins the field matrix's count. The
// number is written out rather than derived from the seed, and compared with
// == rather than >=: a >= would stay green while one field was dropped and
// another duplicated.
func TestBaseTypeHasSeventeenInheritedFields(t *testing.T) {
	db := eoSeedTestDB(t)
	eoSeedRoles(t, db)
	seedEOPlatform(db)

	var base models.EORecordType
	if err := db.Where("key = ?", "eo_base").First(&base).Error; err != nil {
		t.Fatalf("the abstract base record type was not seeded: %v", err)
	}
	if !base.IsAbstract {
		t.Errorf("record type %q is not abstract; it carries the inherited fields and holds no records of its own", base.Key)
	}
	if base.ReferencePattern == "" {
		t.Errorf("record type %q has no reference pattern", base.Key)
	}

	var fields []models.EOFieldDef
	if err := db.Where("record_type_id = ?", base.ID).Order("sort_order").Find(&fields).Error; err != nil {
		t.Fatalf("read base fields: %v", err)
	}
	if len(fields) != 17 {
		keys := make([]string, 0, len(fields))
		for _, f := range fields {
			keys = append(keys, f.Key)
		}
		t.Fatalf("the base record type has %d field definitions, want the field matrix's 17: %v", len(fields), keys)
	}

	seen := map[string]bool{}
	for _, f := range fields {
		if seen[f.Key] {
			t.Errorf("field key %q is defined twice on the base type", f.Key)
		}
		seen[f.Key] = true
		if f.Kind == "" {
			t.Errorf("field %q has no kind", f.Key)
		}
		if f.Label == "" {
			t.Errorf("field %q has no label", f.Key)
		}
		if f.LabelAr == "" {
			t.Errorf("field %q has no Arabic label; §5 wants every label as an EN/AR pair", f.Key)
		}
	}
}

// TestSeedEOPlatformRunsOnlyWhenEnabled drives the gate Seed applies. With
// EO_PLATFORM off nothing is written at all — not a type, not a permission —
// so a deployment that has not bought the platform does not grow its tables.
func TestSeedEOPlatformRunsOnlyWhenEnabled(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		db := eoSeedTestDB(t)
		eoSeedRoles(t, db)
		seedEOPlatformWhenEnabled(db, false)
		if n := eoCountRows(t, db, &models.EORecordType{}); n != 0 {
			t.Errorf("EO_PLATFORM is off but %d record types were seeded", n)
		}
		if n := eoCountRows(t, db, &models.EOFieldDef{}); n != 0 {
			t.Errorf("EO_PLATFORM is off but %d field definitions were seeded", n)
		}
		if n := eoCountRows(t, db, &models.Permission{}); n != 0 {
			t.Errorf("EO_PLATFORM is off but %d permissions were seeded", n)
		}
	})
	t.Run("enabled", func(t *testing.T) {
		db := eoSeedTestDB(t)
		eoSeedRoles(t, db)
		seedEOPlatformWhenEnabled(db, true)
		if n := eoCountRows(t, db, &models.EORecordType{}); n == 0 {
			t.Error("EO_PLATFORM is on but no record type was seeded")
		}
	})
}

// TestSeedEOPlatformIsAtomic is the guard on the marker. The marker row IS the
// base record type and it is written first, so a seed that failed partway
// through without a transaction would leave a database that says "seeded" and
// is not — and every later boot would return early at the marker and never
// repair it.
//
// The failure is a REAL one, not an injected seam: the escalation policy's
// name is uniquely indexed, so a row already sitting under that name makes the
// policy insert fail at a step that comes after the base type has been
// written. Nothing is mocked and no test-only hook exists in the seed.
func TestSeedEOPlatformIsAtomic(t *testing.T) {
	db := eoSeedTestDB(t)
	eoSeedRoles(t, db)

	blocker := models.EscalationPolicy{Name: eoSmokePolicyName, IsActive: true}
	if err := db.Create(&blocker).Error; err != nil {
		t.Fatalf("create the colliding escalation policy: %v", err)
	}

	seedEOPlatform(db)

	// The marker must be absent, or the next boot will skip the repair.
	var marker int64
	if err := db.Model(&models.EORecordType{}).Where("key = ?", eoBaseTypeKey).Count(&marker).Error; err != nil {
		t.Fatalf("count the marker: %v", err)
	}
	if marker != 0 {
		t.Error("the seed failed partway through but left its marker behind; every later boot will skip the repair")
	}
	for _, c := range []struct {
		label string
		model any
	}{
		{"record types", &models.EORecordType{}},
		{"field definitions", &models.EOFieldDef{}},
		{"link types", &models.EOLinkType{}},
		{"workflows", &models.Workflow{}},
		{"permissions", &models.Permission{}},
	} {
		if got := eoCountRows(t, db, c.model); got != 0 {
			t.Errorf("a failed seed left %d %s behind, want none", got, c.label)
		}
	}

	// Clear the obstruction: the next run must complete the configuration,
	// which is the repair the marker would otherwise have prevented.
	if err := db.Unscoped().Delete(&blocker).Error; err != nil {
		t.Fatalf("remove the colliding escalation policy: %v", err)
	}
	seedEOPlatform(db)

	if got := eoCountRows(t, db, &models.EOFieldDef{}); got != 18 {
		t.Errorf("after the repairing run there are %d field definitions, want 18 (17 inherited plus the smoke roll-up)", got)
	}
	var concrete int64
	if err := db.Model(&models.EORecordType{}).Where("is_abstract = ?", false).Count(&concrete).Error; err != nil {
		t.Fatalf("count concrete types: %v", err)
	}
	if concrete != 2 {
		t.Errorf("after the repairing run there are %d concrete record types, want 2", concrete)
	}
}

// TestSeedEOPlatformRefusesToSeedAnOpenClosingTransition: an empty
// AllowedRoles means "no restriction" to the transition guard, so a closing
// transition seeded without its role would be runnable by every authenticated
// user on a statutory register. The seed must stop instead of widening access,
// and must leave no marker, so a later boot retries once the role exists.
func TestSeedEOPlatformRefusesToSeedAnOpenClosingTransition(t *testing.T) {
	db := eoSeedTestDB(t)
	// Deliberately NOT eoSeedRoles: executive_office does not exist.

	seedEOPlatform(db)

	var marker int64
	if err := db.Model(&models.EORecordType{}).Where("key = ?", eoBaseTypeKey).Count(&marker).Error; err != nil {
		t.Fatalf("count the marker: %v", err)
	}
	if marker != 0 {
		t.Error("the seed ran without the executive_office role and marked itself done")
	}
	if got := eoCountRows(t, db, &models.WorkflowTransition{}); got != 0 {
		t.Fatalf("%d transitions were seeded without the role that restricts them", got)
	}

	// With the role present, the same seed completes and the transition is
	// restricted.
	eoSeedRoles(t, db)
	seedEOPlatform(db)

	var transitions []models.WorkflowTransition
	if err := db.Preload("AllowedRoles").Where("code = ?", "close").Find(&transitions).Error; err != nil {
		t.Fatalf("read the closing transitions: %v", err)
	}
	if len(transitions) != 2 {
		t.Fatalf("got %d closing transitions, want one per concrete type", len(transitions))
	}
	for _, tr := range transitions {
		if len(tr.AllowedRoles) != 1 || tr.AllowedRoles[0].Code != "executive_office" {
			t.Errorf("transition %s has AllowedRoles %v; an empty list means NO restriction", tr.ID, tr.AllowedRoles)
		}
	}
}

// TestSeedEOPlatformSeedsPerTypePermissionCodes is spec §7.11's per-type
// gate: every CONCRETE type gets eo:<key>:view|create|update, seeded rather
// than generated at runtime, and granted to admin and executive_office.
func TestSeedEOPlatformSeedsPerTypePermissionCodes(t *testing.T) {
	db := eoSeedTestDB(t)
	eoSeedRoles(t, db)
	seedEOPlatform(db)

	var concrete []models.EORecordType
	if err := db.Where("is_abstract = ?", false).Find(&concrete).Error; err != nil {
		t.Fatalf("read concrete types: %v", err)
	}
	if len(concrete) == 0 {
		t.Fatal("no concrete record type was seeded, so the per-type codes prove nothing")
	}

	granted := map[string]map[string]bool{}
	for _, code := range []string{"admin", "executive_office"} {
		var role models.Role
		if err := db.Preload("Permissions").Where("code = ?", code).First(&role).Error; err != nil {
			t.Fatalf("read role %s: %v", code, err)
		}
		held := map[string]bool{}
		for _, p := range role.Permissions {
			held[p.Code] = true
		}
		granted[code] = held
	}

	for _, ct := range concrete {
		for _, action := range []string{"view", "create", "update"} {
			code := fmt.Sprintf("eo:%s:%s", ct.Key, action)
			var perm models.Permission
			if err := db.Where("code = ?", code).First(&perm).Error; err != nil {
				t.Errorf("permission %q was not seeded for concrete type %q: %v", code, ct.Key, err)
				continue
			}
			if perm.Module != "eo" {
				t.Errorf("permission %q has module %q, want \"eo\" so licensing.FeatureForModule still resolves it", code, perm.Module)
			}
			for _, role := range []string{"admin", "executive_office"} {
				if !granted[role][code] {
					t.Errorf("role %q was not granted %q", role, code)
				}
			}
		}
	}
}
