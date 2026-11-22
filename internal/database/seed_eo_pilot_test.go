package database

import (
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/automax/backend/internal/database/migrations"
	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
)

// eoMatrixInheritedKeys is the client matrix's seventeen inherited keys, in the
// matrix's own spelling, written out here rather than derived from the map
// under test.
var eoMatrixInheritedKeys = []string{
	"reference", "subject_ar", "subject_en", "ticket_type", "parent_ticket", "state",
	"priority", "classification", "source", "owner_department", "assigned_user",
	"due_date", "created_by", "closed_on", "attachments", "change_reason", "delay_reason",
}

// Mutation: rename any engine key in eoMatrixKeys, e.g. "classification" ->
// {"classification", true, false}.
func TestEOMatrixKeyTranslatesInheritedKeys(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		wantKey    string
		wantColumn bool
		wantNoFld  bool
	}{
		{"classification renamed", "classification", "sensitivity", true, false},
		{"owner_department renamed", "owner_department", "owning_body_id", true, false},
		{"assigned_user renamed", "assigned_user", "assigned_user_id", true, false},
		{"closed_on renamed", "closed_on", "closed_at", true, false},
		{"created_by renamed", "created_by", "created_by_id", true, false},
		{"parent_ticket renamed", "parent_ticket", "parent_record_id", true, false},
		{"ticket_type is a column, no field", "ticket_type", "record_type_id", true, true},
		{"state is a column, no field", "state", "state_id", true, true},
		{"attachments live in the bag, no field", "attachments", "attachments", false, true},
		{"identical spelling stays", "priority", "priority", true, false},
		{"unknown key passes through to the bag", "meeting_date", "meeting_date", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			key, isCol, noField := eoMatrixKey(tc.in)
			if key != tc.wantKey {
				t.Errorf("engine key = %q, want %q", key, tc.wantKey)
			}
			if isCol != tc.wantColumn {
				t.Errorf("isColumn = %v, want %v", isCol, tc.wantColumn)
			}
			if noField != tc.wantNoFld {
				t.Errorf("hasNoField = %v, want %v", noField, tc.wantNoFld)
			}
		})
	}
}

// TestEOMatrixKeyCoversTheBaseType ties the map to the two things it describes
// instead of to itself: the keys the base type really seeds, and the columns
// models.EORecord really has. Without it the map could be correct about
// nothing. Mutation: change `{"closed_at", true, false}`'s key to "closed_on".
func TestEOMatrixKeyCoversTheBaseType(t *testing.T) {
	seeded := map[string]bool{}
	for _, d := range eoBaseFieldDefs(uuid.New()) {
		seeded[d.Key] = true
	}

	columns := map[string]bool{}
	rt := reflect.TypeOf(models.EORecord{})
	for i := 0; i < rt.NumField(); i++ {
		if tag := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]; tag != "" && tag != "-" {
			columns[tag] = true
		}
	}

	got := map[string]bool{}
	for _, mk := range eoMatrixInheritedKeys {
		key, isCol, _ := eoMatrixKey(mk)
		got[key] = true
		if !seeded[key] {
			t.Errorf("matrix key %q maps to %q, which the base type does not seed", mk, key)
		}
		if isCol && !columns[key] {
			t.Errorf("matrix key %q maps to %q, reported as a column, but models.EORecord has no such column", mk, key)
		}
		if !isCol && columns[key] {
			t.Errorf("matrix key %q maps to %q, reported as values-bag, but it is an eo_records column", mk, key)
		}
	}
	if len(got) != len(seeded) {
		t.Errorf("the map reaches %d distinct engine keys, the base type seeds %d", len(got), len(seeded))
	}
	for k := range seeded {
		if !got[k] {
			t.Errorf("base field %q is reached by no matrix key", k)
		}
	}
}

// eoPilotDB is a seed test database with roles, the platform seed applied, and
// the real eo_records partitioned table (the pilot seed asks whether records
// exist). Mutation (for everything below that seeds): none; it is a fixture.
func eoPilotDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := eoSeedTestDB(t)
	eoSeedRoles(t, db)
	if err := migrations.MigrateEOPlatform(db); err != nil {
		t.Fatalf("create eo_records and its siblings: %v", err)
	}
	seedEOPlatform(db)
	return db
}

type eoSnapshot struct {
	types, fields, links, workflows, states, transitions, requirements, changes, policies, steps, perms, grants int64
}

func eoTake(t *testing.T, db *gorm.DB) eoSnapshot {
	t.Helper()
	return eoSnapshot{
		types:        eoCountRows(t, db, &models.EORecordType{}),
		fields:       eoCountRows(t, db, &models.EOFieldDef{}),
		links:        eoCountRows(t, db, &models.EOLinkType{}),
		workflows:    eoCountRows(t, db, &models.Workflow{}),
		states:       eoCountRows(t, db, &models.WorkflowState{}),
		transitions:  eoCountRows(t, db, &models.WorkflowTransition{}),
		requirements: eoCountRows(t, db, &models.TransitionRequirement{}),
		changes:      eoCountRows(t, db, &models.TransitionFieldChange{}),
		policies:     eoCountRows(t, db, &models.EscalationPolicy{}),
		steps:        eoCountRows(t, db, &models.EscalationPolicyStep{}),
		perms:        eoCountRows(t, db, &models.Permission{}),
		grants:       eoCountRaw(t, db, "role_permissions"),
	}
}

func eoCountRaw(t *testing.T, db *gorm.DB, table string) int64 {
	t.Helper()
	var n int64
	if err := db.Table(table).Count(&n).Error; err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// Mutation: delete the `if err := eoRemoveSmokeConfiguration(tx)` call from
// eoSeedPilotTx (or the policy delete from eoRemoveSmokeConfiguration).
func TestSeedEOPilotRemovesTheSmokeConfiguration(t *testing.T) {
	db := eoPilotDB(t)
	before := eoTake(t, db)
	if before.policies != 1 || before.workflows != 2 || before.links != 1 {
		t.Fatalf("fixture does not hold the smoke configuration: %+v", before)
	}

	seedEOPilot(db)

	got := eoTake(t, db)
	// Only the abstract base type and its 17 fields survive; nothing of the
	// smoke configuration does, and the permission codes are the only ones the
	// smoke types generated (view/create/update x 2).
	want := eoSnapshot{types: 1, fields: 17, grants: before.grants - 12}
	want.perms = before.perms - 6
	if got != want {
		t.Errorf("after the pilot seed: %+v, want %+v", got, want)
	}
	var left int64
	if err := db.Model(&models.EORecordType{}).Where("key IN ?", eoSmokeTypeKeys).Count(&left).Error; err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("%d smoke types remain", left)
	}
	// The unique-name collision a half-removal leaves: the platform seed's
	// policy name must be free again.
	if err := db.Create(&models.EscalationPolicy{Name: eoSmokePolicyName, IsActive: true}).Error; err != nil {
		t.Errorf("policy name %q is still taken: %v", eoSmokePolicyName, err)
	}
}

// eoInsertRecord writes one record straight into eo_records, bypassing the
// engine: this test is about the seed's reaction to a record, not about how
// records are made.
func eoInsertRecord(t *testing.T, db *gorm.DB, typeID uuid.UUID) {
	t.Helper()
	rec := models.EORecord{
		ID: uuid.New(), RecordTypeID: typeID, Reference: "EO-SMOKE-2026-001-" + uuid.NewString()[:4],
		SubjectAr: "x", Priority: "normal", Sensitivity: "internal", Source: "correspondence",
		CreatedByID: uuid.New(),
	}
	if err := db.Create(&rec).Error; err != nil {
		t.Fatalf("insert a record: %v", err)
	}
}

type eoRowStamp struct {
	ID        uuid.UUID
	UpdatedAt time.Time
}

func eoStamps(t *testing.T, db *gorm.DB, table string) []eoRowStamp {
	t.Helper()
	var out []eoRowStamp
	if err := db.Table(table).Select("id, updated_at").Order("id").Scan(&out).Error; err != nil {
		t.Fatalf("stamps of %s: %v", table, err)
	}
	return out
}

// Review Focus 3. Mutation: delete the `if len(holders) > 0 { ... return nil }`
// block's `return nil` in eoRemoveSmokeConfiguration (the seed then removes a
// populated type).
func TestSeedEOPilotIsSafeOnADatabaseWithRecords(t *testing.T) {
	cases := []struct {
		name string
		// place creates the record and returns the type that holds it.
		place func(t *testing.T, db *gorm.DB) uuid.UUID
	}{
		{"record on a smoke type", func(t *testing.T, db *gorm.DB) uuid.UUID {
			var ty models.EORecordType
			if err := db.Where("key = ?", eoSmokeChildTypeKey).First(&ty).Error; err != nil {
				t.Fatal(err)
			}
			return ty.ID
		}},
		{"record on a type BENEATH a smoke type", func(t *testing.T, db *gorm.DB) uuid.UUID {
			var parent models.EORecordType
			if err := db.Where("key = ?", eoSmokeParentTypeKey).First(&parent).Error; err != nil {
				t.Fatal(err)
			}
			sub := models.EORecordType{ID: uuid.New(), Key: "eo_sub_of_smoke", Name: "Sub", ParentTypeID: &parent.ID,
				ReferenceMode: models.EOReferenceModeOwn, ReferencePrefix: "SUB", ReferencePattern: "SUB-YYYY-NNN", IsActive: true}
			if err := db.Create(&sub).Error; err != nil {
				t.Fatal(err)
			}
			return sub.ID
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := eoPilotDB(t)
			holder := tc.place(t, db)
			eoInsertRecord(t, db, holder)

			// First run: the pilot seed must see the record and keep everything.
			typesBefore := eoStamps(t, db, "eo_record_types")
			wfBefore := eoStamps(t, db, "workflows")
			snapBefore := eoTake(t, db)
			// The seed's marking of base labels is the one permitted write;
			// take the baseline after the first run, then prove the SECOND is inert.
			seedEOPilot(db)
			if got := eoTake(t, db); got != snapBefore {
				t.Errorf("a populated database changed: %+v, was %+v", got, snapBefore)
			}
			typesMid := eoStamps(t, db, "eo_record_types")
			if !reflect.DeepEqual(typesMid, typesBefore) {
				t.Errorf("record types were re-created or edited:\n got %v\nwant %v", typesMid, typesBefore)
			}
			if !reflect.DeepEqual(eoStamps(t, db, "workflows"), wfBefore) {
				t.Error("workflows were re-created or edited")
			}

			seedEOPilot(db)
			if !reflect.DeepEqual(eoStamps(t, db, "eo_record_types"), typesBefore) {
				t.Error("a second run re-created or edited a record type")
			}
			var survivors int64
			if err := db.Model(&models.EORecord{}).Where("record_type_id = ?", holder).Count(&survivors).Error; err != nil {
				t.Fatal(err)
			}
			if survivors != 1 {
				t.Errorf("the record did not survive: %d rows", survivors)
			}
			for _, k := range eoSmokeTypeKeys {
				var n int64
				db.Model(&models.EORecordType{}).Where("key = ?", k).Count(&n)
				if n != 1 {
					t.Errorf("smoke type %q count = %d, want 1 (kept)", k, n)
				}
			}
		})
	}
}

// Mutation: delete the `if marked > 0 { return nil }` guard in
// eoMarkBaseLabelsUnsigned, or make eoRemoveSmokeConfiguration insert instead
// of delete.
func TestSeedEOPilotIsIdempotent(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)
	first := eoTake(t, db)
	seedEOPilot(db)
	seedEOPilot(db)
	if got := eoTake(t, db); got != first {
		t.Errorf("a later run changed the configuration: %+v, was %+v", got, first)
	}
	if first.types == 0 || first.fields == 0 {
		t.Fatalf("nothing is seeded: %+v", first)
	}
}

// An administrator's approval must not be undone by the next boot.
// Mutation: change `marked == 0` in eoPilotSeedNeeded to `true` and the guard
// in eoMarkBaseLabelsUnsigned to `if false`.
func TestSeedEOPilotDoesNotRemarkApprovedLabels(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)
	if err := db.Model(&models.EOFieldDef{}).Where("key = ?", "priority").Update("ar_untranslated", false).Error; err != nil {
		t.Fatal(err)
	}
	seedEOPilot(db)
	var n int64
	db.Model(&models.EOFieldDef{}).Where("key = ? AND ar_untranslated = ?", "priority", true).Count(&n)
	if n != 0 {
		t.Error("a boot re-marked a label an administrator had approved")
	}
}

// Atomicity: the failure is real, not injected. With the escalation step table
// gone the policy step of the removal fails AFTER the smoke types' workflows
// and bindings were already deleted in the same transaction; none of it may
// stick. Mutation: replace db.Transaction(eoSeedPilotTx) in seedEOPilot with
// eoSeedPilotTx(db).
func TestSeedEOPilotIsAtomic(t *testing.T) {
	db := eoPilotDB(t)
	before := eoTake(t, db)
	if err := db.Exec(`DROP TABLE escalation_policy_steps`).Error; err != nil {
		t.Fatal(err)
	}
	seedEOPilot(db)
	var types, workflows int64
	db.Model(&models.EORecordType{}).Count(&types)
	db.Model(&models.Workflow{}).Count(&workflows)
	if types != before.types || workflows != before.workflows {
		t.Errorf("a failed pilot seed left a partial result: %d types (was %d), %d workflows (was %d)",
			types, before.types, workflows, before.workflows)
	}
}

// Mutation: remove `d.ArUntranslated = true` from eoUntranslatedLabel, or set
// `ArUntranslated: true` to false in eoBaseFieldDefs.
func TestUntranslatedLabelsAreQueryable(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	set, err := eoUntranslatedFields(db)
	if err != nil {
		t.Fatal(err)
	}
	total := eoCountRows(t, db, &models.EOFieldDef{})
	if len(set) == 0 {
		t.Fatal("the untranslated set is empty")
	}
	if int64(len(set)) != total {
		t.Errorf("%d of %d seeded field labels are marked untranslated, want all", len(set), total)
	}

	// Round trip, through the production repository's own read and write: an
	// administrator edits an UNRELATED attribute of one field the way the
	// designer does (load, change, save the whole struct).
	repo := repository.NewEORecordTypeRepository(db)
	var def models.EOFieldDef
	if err := db.Where("key = ?", "source").First(&def).Error; err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetFieldDef(t.Context(), def.ID)
	if err != nil || stored == nil {
		t.Fatalf("read field def: %v", err)
	}
	stored.HelpText = "Where the directive came from"
	stored.HelpTextAr = "مصدر التوجيه"
	stored.SortOrder = 99
	if err := repo.UpdateFieldDef(t.Context(), stored); err != nil {
		t.Fatal(err)
	}
	after, err := eoUntranslatedFields(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(set) {
		t.Errorf("an edit to an unrelated attribute changed the untranslated set from %d to %d", len(set), len(after))
	}
}

// Mutation: delete `d.LabelAr = english`.
func TestEOUntranslatedLabelHelper(t *testing.T) {
	d := eoUntranslatedLabel(models.EOFieldDef{Key: "k"}, "Meeting date")
	if d.Label != "Meeting date" || d.LabelAr != "Meeting date" {
		t.Errorf("labels = %q / %q, want the English text in both", d.Label, d.LabelAr)
	}
	if !d.ArUntranslated {
		t.Error("the field is not marked untranslated")
	}
	if d.Key != "k" {
		t.Errorf("the helper changed the key to %q", d.Key)
	}
}

// Mutation: change `if !enabled` to `if enabled` in seedEOPilotWhenEnabled.
func TestSeedEOPilotRunsOnlyWhenEnabled(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		db := eoPilotDB(t)
		seedEOPilotWhenEnabled(db, enabled)
		var smoke int64
		db.Model(&models.EORecordType{}).Where("key IN ?", eoSmokeTypeKeys).Count(&smoke)
		if enabled && smoke != 0 {
			t.Errorf("enabled: %d smoke types remain", smoke)
		}
		if !enabled && smoke != 2 {
			t.Errorf("disabled: the gate let the seed run (%d smoke types, want 2)", smoke)
		}
	}
}

// TestSeedCallsTheEOPilotSeedAfterThePlatformSeed is the wiring proof: a
// seed function that nothing calls passes every test above. It reads the AST
// of Seed in postgres.go, so it needs no database. Mutation: delete the
// `seedEOPilotWhenEnabled(db, cfg.EOPlatform.Enabled)` line from postgres.go
// (or move it above seedEOPlatformWhenEnabled).
func TestSeedCallsTheEOPilotSeedAfterThePlatformSeed(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "postgres.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var seed *ast.FuncDecl
	for _, d := range file.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "Seed" && fd.Recv == nil {
			seed = fd
		}
	}
	if seed == nil {
		t.Fatal("no func Seed in postgres.go")
	}
	pos := map[string]token.Pos{}
	var args []string
	ast.Inspect(seed.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		id, ok := call.Fun.(*ast.Ident)
		if !ok {
			return true
		}
		switch id.Name {
		case "seedEOPlatformWhenEnabled", "seedEOPilotWhenEnabled":
			pos[id.Name] = call.Pos()
			if id.Name == "seedEOPilotWhenEnabled" {
				for _, a := range call.Args {
					if sel, ok := a.(*ast.SelectorExpr); ok {
						args = append(args, sel.Sel.Name)
					}
				}
			}
		}
		return true
	})
	pilot, ok := pos["seedEOPilotWhenEnabled"]
	if !ok {
		t.Fatal("Seed does not call seedEOPilotWhenEnabled; the pilot seed is wired to nothing")
	}
	platform, ok := pos["seedEOPlatformWhenEnabled"]
	if !ok {
		t.Fatal("Seed does not call seedEOPlatformWhenEnabled")
	}
	if pilot < platform {
		t.Error("Seed calls the pilot seed BEFORE the platform seed; it removes what the platform seed writes")
	}
	sort.Strings(args)
	if len(args) != 1 || args[0] != "Enabled" {
		t.Errorf("the pilot seed is not gated on cfg.EOPlatform.Enabled: selector args %v", args)
	}
}

// A fresh database is marked at creation, before the pilot seed ever runs; the
// pilot seed's backfill is for databases that predate the column.
// Mutation: change `ArUntranslated: true,` to false in eoBaseFieldDefs.
func TestBaseFieldsAreCreatedMarkedUntranslated(t *testing.T) {
	db := eoSeedTestDB(t)
	eoSeedRoles(t, db)
	seedEOPlatform(db)
	set, err := eoUntranslatedFields(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(set) != 17 {
		t.Errorf("%d base labels marked straight after the platform seed, want 17", len(set))
	}
}
