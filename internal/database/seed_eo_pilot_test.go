package database

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/automax/backend/internal/database/migrations"
	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/internal/services/eoformula"
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
		name        string
		in          string
		wantKey     string
		wantColumn  bool
		wantManaged bool
	}{
		{"classification renamed", "classification", "sensitivity", true, false},
		{"owner_department renamed", "owner_department", "owning_body_id", true, false},
		{"assigned_user renamed", "assigned_user", "assigned_user_id", true, false},
		{"closed_on renamed", "closed_on", "closed_at", true, false},
		{"created_by renamed", "created_by", "created_by_id", true, false},
		{"parent_ticket renamed", "parent_ticket", "parent_record_id", true, false},
		{"ticket_type is a column, no field", "ticket_type", "record_type_id", true, true},
		{"state is a column, no field", "state", "state_id", true, true},
		// A1 marked this engine-managed; A2's fix round corrected it. It is a
		// plain values-bag key, so a pilot type may declare it — which is how
		// Incoming Correspondence gets the matrix's Required: Yes.
		{"attachments live in the bag and may be declared", "attachments", "attachments", false, false},
		{"identical spelling stays", "priority", "priority", true, false},
		{"unknown key passes through to the bag", "meeting_date", "meeting_date", false, false},
		{"a type-specific rename onto a column", "sender_deadline", "due_date", true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			key, isCol, managed := eoMatrixKey(tc.in)
			if key != tc.wantKey {
				t.Errorf("engine key = %q, want %q", key, tc.wantKey)
			}
			if isCol != tc.wantColumn {
				t.Errorf("isColumn = %v, want %v", isCol, tc.wantColumn)
			}
			if managed != tc.wantManaged {
				t.Errorf("isEngineManaged = %v, want %v", managed, tc.wantManaged)
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

// eoRootFieldCount is how many field definitions the three root types seed,
// taken from the specs themselves rather than written out as a number: this is
// a test ABOUT THE SMOKE REMOVAL, and it must not fail every time a field is
// added. TestRootTypeFieldsMatchTheMatrix is what holds the count to the
// client's document.
func eoRootFieldCount(t *testing.T) int64 {
	t.Helper()
	base := eoBaseFieldDefs(uuid.New())
	var n int64
	for _, spec := range eoPilotRootTypes {
		defs, err := eoPilotFieldDefs(spec, uuid.New(), base)
		if err != nil {
			t.Fatalf("build the fields of %q: %v", spec.key, err)
		}
		n += int64(len(defs))
	}
	return n
}

// eoKeptStamps is after, narrowed to the rows that were already in before. The
// pilot seed ADDS record types, so a whole-table comparison would fail on the
// additions; what these tests are about is that nothing which was already
// there was re-created or edited.
func eoKeptStamps(before, after []eoRowStamp) []eoRowStamp {
	was := make(map[uuid.UUID]bool, len(before))
	for _, s := range before {
		was[s.ID] = true
	}
	out := make([]eoRowStamp, 0, len(before))
	for _, s := range after {
		if was[s.ID] {
			out = append(out, s)
		}
	}
	return out
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
	// Nothing of the smoke configuration survives, and the permission codes
	// are the only ones the smoke types generated (view/create/update x 2).
	// What is left is the abstract base type with its 17 inherited fields,
	// plus the three root types this seed adds and their fields.
	want := eoSnapshot{
		types:  1 + int64(len(eoPilotRootTypes)),
		fields: 17 + eoRootFieldCount(t),
		grants: before.grants - 12,
	}
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
			// snapBefore was taken BEFORE this first run. The seed ADDS the
			// three root types even on a populated database — that is the
			// point of them — but it must leave everything already there
			// alone, and the second run below must not touch any record type.
			want := snapBefore
			want.types += int64(len(eoPilotRootTypes))
			want.fields += eoRootFieldCount(t)
			seedEOPilot(db)
			if got := eoTake(t, db); got != want {
				t.Errorf("a populated database changed: %+v, want %+v (was %+v)", got, want, snapBefore)
			}
			typesMid := eoKeptStamps(typesBefore, eoStamps(t, db, "eo_record_types"))
			if !reflect.DeepEqual(typesMid, typesBefore) {
				t.Errorf("record types that were already there were re-created or edited:\n got %v\nwant %v", typesMid, typesBefore)
			}
			if !reflect.DeepEqual(eoKeptStamps(wfBefore, eoStamps(t, db, "workflows")), wfBefore) {
				t.Error("workflows were re-created or edited")
			}

			seedEOPilot(db)
			if !reflect.DeepEqual(eoKeptStamps(typesBefore, eoStamps(t, db, "eo_record_types")), typesBefore) {
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

// An administrator's approval must not be undone by the next boot, and the
// guard that stops it must be tested where it is the ONLY guard: a smoke type
// holding a record keeps the removal step unapplied, so the fast path stays
// open and the run reaches the in-transaction applied-step check. ALL labels
// are unmarked, the case where a content-based guard would see nothing marked
// and re-mark the lot.
// Mutation: delete the `if n > 0 { continue }` in eoSeedPilotTx.
func TestSeedEOPilotDoesNotRemarkApprovedLabels(t *testing.T) {
	db := eoPilotDB(t)
	var child models.EORecordType
	if err := db.Where("key = ?", eoSmokeChildTypeKey).First(&child).Error; err != nil {
		t.Fatal(err)
	}
	eoInsertRecord(t, db, child.ID)
	seedEOPilot(db)
	if open, err := eoPilotSeedNeeded(db); err != nil || !open {
		t.Fatalf("fixture error: fast path should be open (needed=%v err=%v)", open, err)
	}
	if err := db.Model(&models.EOFieldDef{}).Where("1 = 1").Update("ar_untranslated", false).Error; err != nil {
		t.Fatal(err)
	}
	// A re-run of an applied step would hit the step table's primary key and
	// roll back, which would ALSO leave the labels unmarked; the log is what
	// tells "skipped correctly" from "failed and rolled back".
	var logged strings.Builder
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	seedEOPilot(db)
	log.SetOutput(os.Stderr)
	if strings.Contains(logged.String(), "FAILED") {
		t.Errorf("the second run failed instead of skipping the applied step: %s", logged.String())
	}
	var n int64
	db.Model(&models.EOFieldDef{}).Where("ar_untranslated = ?", true).Count(&n)
	if n != 0 {
		t.Errorf("a boot re-marked %d labels an administrator had approved", n)
	}
}

// A kept removal is not recorded as applied, so it is retried once the records
// are gone. Mutation: delete `if !done { continue }` in eoSeedPilotTx.
func TestSeedEOPilotRetriesAKeptRemovalOnceTheRecordsAreGone(t *testing.T) {
	db := eoPilotDB(t)
	var child models.EORecordType
	if err := db.Where("key = ?", eoSmokeChildTypeKey).First(&child).Error; err != nil {
		t.Fatal(err)
	}
	eoInsertRecord(t, db, child.ID)
	seedEOPilot(db)
	if err := db.Exec(`DELETE FROM eo_records`).Error; err != nil {
		t.Fatal(err)
	}
	seedEOPilot(db)
	var left int64
	db.Model(&models.EORecordType{}).Where("key IN ?", eoSmokeTypeKeys).Count(&left)
	if left != 0 {
		t.Errorf("%d smoke types remain after the records were removed and the seed re-ran", left)
	}
}

// The regression the applied-step record exists to prevent: a step added after
// the others were applied on a database must still run. The fixture applies
// every existing step first, then appends one.
// Mutation: in eoPilotSeedNeeded return `applied < 2` instead of
// `applied < int64(len(keys))`.
func TestEOPilotRunsAStepAddedAfterTheOthersWereApplied(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)
	if open, err := eoPilotSeedNeeded(db); err != nil || open {
		t.Fatalf("fixture error: everything should be applied (needed=%v err=%v)", open, err)
	}
	saved := eoPilotSteps
	t.Cleanup(func() { eoPilotSteps = saved })
	ran := false
	eoPilotSteps = append(append([]eoPilotStep{}, saved...), eoPilotStep{
		key: "a_later_step",
		run: func(tx *gorm.DB) (bool, error) { ran = true; return true, nil },
	})
	seedEOPilot(db)
	if !ran {
		t.Error("a step added after the others were applied never ran")
	}
	ran = false
	seedEOPilot(db)
	if ran {
		t.Error("an applied step ran again")
	}
}

// The descendant-keep branch: a type beneath a smoke type, holding NO records,
// still blocks removal (deleting the parent would orphan it).
// Mutation: delete the `if len(subtree) != len(smokeIDs) { ... }` block.
func TestSeedEOPilotKeepsSmokeTypesWithDescendants(t *testing.T) {
	db := eoPilotDB(t)
	var parent models.EORecordType
	if err := db.Where("key = ?", eoSmokeParentTypeKey).First(&parent).Error; err != nil {
		t.Fatal(err)
	}
	sub := models.EORecordType{ID: uuid.New(), Key: "eo_empty_sub", Name: "Sub", ParentTypeID: &parent.ID,
		ReferenceMode: models.EOReferenceModeOwn, ReferencePrefix: "SUB", ReferencePattern: "SUB-YYYY-NNN", IsActive: true}
	if err := db.Create(&sub).Error; err != nil {
		t.Fatal(err)
	}
	seedEOPilot(db)
	var n int64
	db.Model(&models.EORecordType{}).Where("key IN ?", append(append([]string{}, eoSmokeTypeKeys...), "eo_empty_sub")).Count(&n)
	if n != 3 {
		t.Errorf("%d of the 3 types remain, want all 3", n)
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

// ── Task A2: the three root types ───────────────────────────────────────────

// ── reading the client's field matrix ───────────────────────────────────────

// eoFieldMatrixPath is the client's own document, relative to this package.
// The tests below READ it rather than restating it: a hand transcription is
// checked once, by the person who wrote it, and every value it gets wrong is
// a value no test constrains. This file is the authority for which fields each
// type has and when each is required.
const eoFieldMatrixPath = "../../docs/superpowers/specs/eo-field-matrix.md"

// eoMatrixRow is one row of one of the matrix's field tables.
type eoMatrixRow struct {
	// Key is the matrix's spelling; run it through eoMatrixKey for the
	// engine's.
	Key string
	// Required is the Required cell verbatim: Yes, No, Conditional, Auto,
	// Derived or "Yes at closure" (the matrix's own five-valued column, plus
	// No).
	Required string
}

// eoMatrixSections names the heading each record type's table sits under.
var eoMatrixSections = map[string]string{
	eoDirectiveTypeKey:  "Directive",
	eoDraftStudyTypeKey: "Draft Decision Study",
	eoIncomingTypeKey:   "Incoming Correspondence",
}

// eoMatrixInheritedSection is the "All types" table every type inherits.
const eoMatrixInheritedSection = "All types (inherited)"

// eoMatrixRowCounts is how many field rows each section must yield. It is the
// guard on the PARSER: without it a parser that stopped matching — a changed
// table format, a renamed heading — would yield nothing, and every test built
// on it would pass vacuously, which is this programme's signature defect
// wearing a different hat.
var eoMatrixRowCounts = map[string]int{
	eoMatrixInheritedSection:  17,
	"Directive":               13,
	"Draft Decision Study":    21,
	"Incoming Correspondence": 16,
}

// eoMatrixRowPattern pulls the backticked keys out of a table cell.
var eoMatrixRowPattern = regexp.MustCompile("`([^`]+)`")

// eoReadFieldMatrix parses the client's field matrix into section -> rows.
//
// A field table row is a pipe row of at least six cells whose SECOND cell
// carries a backticked key, which excludes the header, the separator and the
// Contents table. A cell naming several keys ("`created_by`, `created_at`")
// yields the first: it is the one the engine stores a definition for, and the
// base type's own comment says why.
func eoReadFieldMatrix(t *testing.T) map[string][]eoMatrixRow {
	t.Helper()
	raw, err := os.ReadFile(eoFieldMatrixPath)
	if err != nil {
		t.Fatalf("read the client field matrix: %v", err)
	}
	out := map[string][]eoMatrixRow{}
	section := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		if h := strings.TrimLeft(line, "#"); len(line)-len(h) >= 2 && len(line)-len(h) <= 3 {
			section = strings.TrimSpace(h)
			continue
		}
		if section == "" || !strings.HasPrefix(line, "|") {
			continue
		}
		cells := strings.Split(strings.Trim(strings.TrimSpace(line), "|"), "|")
		if len(cells) < 6 {
			continue
		}
		keys := eoMatrixRowPattern.FindStringSubmatch(strings.TrimSpace(cells[1]))
		if keys == nil {
			continue
		}
		out[section] = append(out[section], eoMatrixRow{
			Key:      keys[1],
			Required: strings.TrimSpace(cells[3]),
		})
	}
	for section, want := range eoMatrixRowCounts {
		if got := len(out[section]); got != want {
			t.Fatalf("parsed %d field rows under %q, want %d: the matrix's table format or heading has changed and every test reading it is now proving nothing",
				got, section, want)
		}
	}
	return out
}

// eoMatrixConditionalFields is every Conditional cell the matrix has for the
// three root types, in the matrix's own spelling, READ FROM THE MATRIX. The
// inherited table's cells count for all three types unless the type's own
// table overrides the key.
func eoMatrixConditionalFields(t *testing.T) map[string][]string {
	t.Helper()
	m := eoReadFieldMatrix(t)
	out := map[string][]string{}
	for typeKey, section := range eoMatrixSections {
		// Deliberately NOT eoMatrixExpectations: that resolves one engine
		// field from two matrix names, and the question here is which CELLS
		// the matrix marks Conditional. The inherited table marks `due_date`
		// Conditional on every type, including Incoming Correspondence, whose
		// own table names the same engine field `sender_deadline` and marks
		// it Yes. Both statements deserve a recorded decision; resolving them
		// first would hide the inherited one.
		for _, rows := range [][]eoMatrixRow{m[eoMatrixInheritedSection], m[section]} {
			for _, r := range rows {
				if _, _, managed := eoMatrixKey(r.Key); managed {
					continue
				}
				if r.Required == "Conditional" {
					out[typeKey] = append(out[typeKey], r.Key)
				}
			}
		}
		sort.Strings(out[typeKey])
	}
	return out
}

// eoMatrixExpectations is what the matrix says about one type, keyed by ENGINE
// key: the inherited table, then the type's own table laid over it. Both are
// needed, and the override must happen on the engine key rather than the
// matrix key — Incoming Correspondence's `sender_deadline` and the inherited
// `due_date` are two matrix names for one engine field, and the type's own
// row is the one that governs.
//
// Engine-managed keys are left out. The matrix marks `ticket_type` Yes and
// `state` Auto, but the client chooses them on the create REQUEST rather than
// as values, so the engine seeds them `auto`/`never` and no type may say
// otherwise (seed_eo_platform.go, eoBaseFieldDefs' PROVENANCE note).
func eoMatrixExpectations(t *testing.T, m map[string][]eoMatrixRow, typeKey string) map[string]eoMatrixRow {
	t.Helper()
	out := map[string]eoMatrixRow{}
	add := func(rows []eoMatrixRow) {
		for _, r := range rows {
			key, _, managed := eoMatrixKey(r.Key)
			if managed {
				continue
			}
			out[key] = r
		}
	}
	add(m[eoMatrixInheritedSection])
	add(m[eoMatrixSections[typeKey]])
	return out
}

// eoRootType reads one seeded record type. Fixture; no mutation of its own.
func eoRootType(t *testing.T, db *gorm.DB, key string) models.EORecordType {
	t.Helper()
	var ty models.EORecordType
	if err := db.Where("key = ?", key).Limit(1).Find(&ty).Error; err != nil {
		t.Fatalf("read record type %q: %v", key, err)
	}
	if ty.ID == uuid.Nil {
		t.Fatalf("record type %q was not seeded", key)
	}
	return ty
}

// eoOwnFieldDefs are the definitions a type declares itself, sorted.
func eoOwnFieldDefs(t *testing.T, db *gorm.DB, typeKey string) []models.EOFieldDef {
	t.Helper()
	ty := eoRootType(t, db, typeKey)
	var defs []models.EOFieldDef
	if err := db.Where("record_type_id = ?", ty.ID).Order("sort_order, key").Find(&defs).Error; err != nil {
		t.Fatalf("read field defs of %q: %v", typeKey, err)
	}
	return defs
}

// eoEffectiveField is the definition that governs a key on a type: the type's
// own redefinition if it has one, else the base type's.
func eoEffectiveField(t *testing.T, db *gorm.DB, typeKey, matrixKey string) models.EOFieldDef {
	t.Helper()
	engineKey, _, _ := eoMatrixKey(matrixKey)
	for _, owner := range []string{typeKey, eoBaseTypeKey} {
		ty := eoRootType(t, db, owner)
		var def models.EOFieldDef
		if err := db.Where("record_type_id = ? AND key = ?", ty.ID, engineKey).Limit(1).Find(&def).Error; err != nil {
			t.Fatalf("read field %q on %q: %v", engineKey, owner, err)
		}
		if def.ID != uuid.Nil {
			return def
		}
	}
	t.Fatalf("no effective field %q (matrix %q) on record type %q", engineKey, matrixKey, typeKey)
	return models.EOFieldDef{}
}

// Addendum 1: a duplicate key makes len(eoPilotSteps) exceed the number of
// applied rows that can ever exist, so the fast path stays open forever with
// no log and no error. Mutation: append a second {"seed_root_types", ...} to
// eoPilotSteps.
func TestEOPilotStepKeysAreUnique(t *testing.T) {
	seen := map[string]int{}
	for i, st := range eoPilotSteps {
		if st.key == "" {
			t.Errorf("step %d has an empty key", i)
		}
		if first, dup := seen[st.key]; dup {
			t.Errorf("step %d repeats the key %q already used by step %d; the applied-row fast path can never close", i, st.key, first)
			continue
		}
		seen[st.key] = i
	}
	if len(seen) != len(eoPilotSteps) {
		t.Errorf("%d distinct step keys for %d steps", len(seen), len(eoPilotSteps))
	}
}

// Mutation: change eoDirectiveTypeSpec's prefix to "EO-DIRECTIVE", its pattern
// to "EO-DIR-YYYY-NNNN", or its mode to models.EOReferenceModeParent.
func TestSeedsTheThreeRootTypes(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	base := eoRootType(t, db, eoBaseTypeKey)
	tests := []struct {
		name, key, prefix, pattern string
	}{
		{"Directive", eoDirectiveTypeKey, "EO-DIR", "EO-DIR-YYYY-NNN"},
		{"Draft Decision Study", eoDraftStudyTypeKey, "DRF", "DRF-YYYY-NNN"},
		{"Incoming Correspondence", eoIncomingTypeKey, "IN", "IN-YYYY-NNNN"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ty := eoRootType(t, db, tc.key)
			if ty.ReferencePrefix != tc.prefix {
				t.Errorf("reference prefix = %q, want %q", ty.ReferencePrefix, tc.prefix)
			}
			if ty.ReferencePattern != tc.pattern {
				t.Errorf("reference pattern = %q, want %q", ty.ReferencePattern, tc.pattern)
			}
			if ty.ReferenceMode != models.EOReferenceModeOwn {
				t.Errorf("reference mode = %q, want %q", ty.ReferenceMode, models.EOReferenceModeOwn)
			}
			if ty.IsAbstract {
				t.Error("the type is abstract; a root type holds records")
			}
			if !ty.IsActive {
				t.Error("the type is inactive")
			}
			if ty.ParentTypeID == nil || *ty.ParentTypeID != base.ID {
				t.Errorf("parent type = %v, want the base type %v", ty.ParentTypeID, base.ID)
			}
			if ty.PartitionPending {
				t.Error("partition_pending is true although the seed creates this type's partition")
			}
			if ty.WorkflowID != nil {
				t.Errorf("workflow = %v, want none until Task A5 binds one", ty.WorkflowID)
			}
		})
	}
}

// Mutation: delete the completion_rate entry from eoDirectiveTypeSpec, or set
// its provenance to models.EOProvenanceUser.
func TestRootTypeFieldsMatchTheMatrix(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)
	matrix := eoReadFieldMatrix(t)

	for _, typeKey := range []string{eoDirectiveTypeKey, eoDraftStudyTypeKey, eoIncomingTypeKey} {
		t.Run(typeKey, func(t *testing.T) {
			// What the seed MUST declare: every key of THIS TYPE'S OWN table
			// in the matrix that is neither an eo_records column nor
			// engine-managed, under the ENGINE spelling.
			want := map[string]bool{}
			for _, r := range matrix[eoMatrixSections[typeKey]] {
				key, isColumn, managed := eoMatrixKey(r.Key)
				if isColumn || managed {
					continue
				}
				want[key] = true
			}
			got := map[string]bool{}
			for _, d := range eoOwnFieldDefs(t, db, typeKey) {
				got[d.Key] = true
			}
			for k := range want {
				if !got[k] {
					t.Errorf("field %q is in the matrix but not seeded", k)
				}
			}
			for k := range got {
				if !want[k] {
					// A narrowing of an inherited column key is legitimate and
					// is pinned by its own test; everything else is a surprise.
					if _, isColumn, _ := eoMatrixKey(k); isColumn {
						continue
					}
					t.Errorf("field %q is seeded but is not in the matrix for this type", k)
				}
			}
		})
	}

	// The Directive's own tail is exactly the matrix's thirteen rows, plus the
	// one inherited key it narrows.
	t.Run("Directive declares 13 own fields and one narrowing", func(t *testing.T) {
		defs := eoOwnFieldDefs(t, db, eoDirectiveTypeKey)
		own, narrowed := 0, 0
		for _, d := range defs {
			if _, isColumn, _ := eoMatrixKey(d.Key); isColumn {
				narrowed++
				continue
			}
			own++
		}
		if own != 13 {
			t.Errorf("%d own fields, want 13", own)
		}
		if narrowed != 1 {
			t.Errorf("%d narrowed inherited fields, want 1 (due_date)", narrowed)
		}
	})

	t.Run("completion_rate is derived and escalation_level is auto", func(t *testing.T) {
		rate := eoEffectiveField(t, db, eoDirectiveTypeKey, "completion_rate")
		if rate.Provenance != models.EOProvenanceDerived {
			t.Errorf("completion_rate provenance = %q, want %q", rate.Provenance, models.EOProvenanceDerived)
		}
		if strings.TrimSpace(rate.Formula) == "" {
			t.Error("completion_rate is derived with no formula: it would compute nothing and look configured")
		}
		if len(rate.RecomputeOn) == 0 || string(rate.RecomputeOn) == "[]" {
			t.Error("completion_rate has a formula but nothing recomputes it")
		}
		level := eoEffectiveField(t, db, eoDirectiveTypeKey, "escalation_level")
		if level.Provenance != models.EOProvenanceAuto {
			t.Errorf("escalation_level provenance = %q, want %q", level.Provenance, models.EOProvenanceAuto)
		}
	})

	// `attachments` is a plain values-bag key, so a pilot type may declare it
	// — and Incoming Correspondence must, because the matrix marks it
	// Required: Yes there while the base type seeds it `never`. Without the
	// declaration the pilot files a government letter with no letter attached.
	// A1 marked the key engine-managed; A2's fix round corrected that.
	t.Run("attachments is declared and required on Incoming Correspondence", func(t *testing.T) {
		got := eoEffectiveField(t, db, eoIncomingTypeKey, "attachments")
		if got.RequiredMode != models.EORequiredAlways {
			t.Errorf("attachments required_mode = %q, want %q", got.RequiredMode, models.EORequiredAlways)
		}
		// The override, not a second field: it replaces the base's definition
		// at the base's slot, so the key appears once.
		base := eoEffectiveField(t, db, eoBaseTypeKey, "attachments")
		if got.ID == base.ID {
			t.Error("Incoming Correspondence does not redefine attachments at all")
		}
		if base.RequiredMode != models.EORequiredNever {
			t.Errorf("the base type's attachments is %q; the override is only meaningful against %q",
				base.RequiredMode, models.EORequiredNever)
		}
		// And the other two types keep the inherited definition.
		for _, typeKey := range []string{eoDirectiveTypeKey, eoDraftStudyTypeKey} {
			if d := eoEffectiveField(t, db, typeKey, "attachments"); d.ID != base.ID {
				t.Errorf("%s redefines attachments; the matrix only requires it on Incoming Correspondence", typeKey)
			}
		}
	})
}

// Addendum 5: every field definition on the three new types is marked as
// awaiting Arabic sign-off, so the §9 round is a query rather than a reread of
// this file. Mutation: build one field with models.EOFieldDef{Label: ...}
// directly instead of through eoUntranslatedLabel.
func TestRootTypeFieldsAwaitArabicSignOff(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	marked := map[string]bool{}
	set, err := eoUntranslatedFields(db)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range set {
		marked[f.TypeKey+"."+f.FieldKey] = true
	}
	for _, typeKey := range []string{eoDirectiveTypeKey, eoDraftStudyTypeKey, eoIncomingTypeKey} {
		defs := eoOwnFieldDefs(t, db, typeKey)
		if len(defs) == 0 {
			t.Fatalf("%s seeds no fields", typeKey)
		}
		for _, d := range defs {
			if !marked[typeKey+"."+d.Key] {
				t.Errorf("%s.%s is not marked as awaiting Arabic sign-off", typeKey, d.Key)
			}
			if _, isColumn, _ := eoMatrixKey(d.Key); isColumn {
				// A narrowing of an inherited field COPIES the base type's
				// definition, Arabic and all (which the base already marks
				// unsigned). Its own test pins that the copy has not drifted.
				continue
			}
			if d.LabelAr != d.Label {
				t.Errorf("%s.%s label_ar = %q, want the English label %q standing in until sign-off",
					typeKey, d.Key, d.LabelAr, d.Label)
			}
		}
	}
}

// Review Focus 1. reference is unique per PARTITION only, so global uniqueness
// rests entirely on prefixes not overlapping — and all three roots are
// own-mode, the only kind the rule governs.
// Mutation: change eoDraftStudyTypeSpec's prefix to "EO-DIR-DRF".
func TestRootPrefixesDoNotOverlap(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	var all []models.EORecordType
	if err := db.Find(&all).Error; err != nil {
		t.Fatal(err)
	}
	roots := map[string]models.EORecordType{}
	for _, ty := range all {
		roots[ty.Key] = ty
	}
	keys := []string{eoDirectiveTypeKey, eoDraftStudyTypeKey, eoIncomingTypeKey}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			ty, ok := roots[key]
			if !ok {
				t.Fatalf("record type %q was not seeded", key)
			}
			others := make([]models.EORecordType, 0, len(all))
			for _, o := range all {
				if o.ID != ty.ID {
					others = append(others, o)
				}
			}
			if err := models.ValidateReferencePrefix(ty, others); err != nil {
				t.Errorf("prefix %q is rejected against the rest of the register: %v", ty.ReferencePrefix, err)
			}
		})
	}

	// The negative half: the rule must actually be able to refuse. A type
	// extending a seeded prefix would issue references the seeded type's own
	// sequence can also produce.
	for _, tc := range []struct{ name, prefix string }{
		{"a longer prefix under EO-DIR", "EO-DIR-X"},
		{"a shorter prefix above DRF", "D"},
		{"the same prefix in another case", "in"},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			candidate := models.EORecordType{
				Key: "eo_overlapping_candidate", Name: "Overlapping",
				ReferenceMode: models.EOReferenceModeOwn, ReferencePrefix: tc.prefix,
			}
			if err := models.ValidateReferencePrefix(candidate, all); err == nil {
				t.Errorf("prefix %q was accepted beside the seeded roots", tc.prefix)
			}
		})
	}
}

// Addendum 3: `never` is an answer only when the seed says which condition it
// is and why it is not expressible. This makes a SILENT never impossible: the
// decision registry must cover exactly the matrix's Conditional cells, every
// entry must carry both the condition and the reason, and the seeded
// required_mode must be the one the entry claims.
// Mutation: delete any entry from eoConditionalDecisions, or blank its Why.
func TestEveryConditionalMatrixFieldHasAStatedDecision(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	declared := map[string]eoConditionalDecision{}
	for _, d := range eoConditionalDecisions {
		id := d.TypeKey + "." + d.MatrixKey
		if _, dup := declared[id]; dup {
			t.Errorf("two decisions for %s", id)
		}
		declared[id] = d
	}

	wanted := map[string]bool{}
	conditional := eoMatrixConditionalFields(t)
	for typeKey, keys := range conditional {
		for _, mk := range keys {
			wanted[typeKey+"."+mk] = true
		}
	}
	// The count is the guard on the READING of the matrix: five inherited
	// Conditional cells on each of three types, plus three of the Directive's
	// own, one of the Draft Decision Study's and one of Incoming
	// Correspondence's. A parse that found none would make both halves of the
	// comparison below empty and the test vacuous.
	if len(wanted) != 20 {
		t.Fatalf("the matrix yields %d Conditional cells across the three root types, want 20: %v", len(wanted), conditional)
	}
	for id := range wanted {
		if _, ok := declared[id]; !ok {
			t.Errorf("the matrix marks %s Conditional and the seed records no decision for it", id)
		}
	}
	for id := range declared {
		if !wanted[id] {
			t.Errorf("a decision is recorded for %s, which the matrix does not mark Conditional", id)
		}
	}

	for id, d := range declared {
		if !wanted[id] {
			continue
		}
		t.Run(id, func(t *testing.T) {
			if strings.TrimSpace(d.Condition) == "" {
				t.Error("the decision does not say WHICH condition the matrix means")
			}
			if strings.TrimSpace(d.Why) == "" {
				t.Error("the decision does not say why; a silent `never` is indistinguishable from a deliberate one")
			}
			got := eoEffectiveField(t, db, d.TypeKey, d.MatrixKey)
			if got.RequiredMode != d.Mode {
				t.Errorf("seeded required_mode = %q, the recorded decision says %q", got.RequiredMode, d.Mode)
			}
			switch d.Mode {
			case models.EORequiredConditional:
				if strings.TrimSpace(got.RequiredRule) == "" {
					// ValidateRecordValues returns a HARD error for this, so
					// every save of this type would fail.
					t.Error("conditional with no required_rule: every save of this type would fail")
				}
			case models.EORequiredNever, models.EORequiredAlways:
			default:
				t.Errorf("unexpected decision mode %q", d.Mode)
			}
		})
	}
}

// eoNeutralEnv is every effective field key of a type, each empty. A rule
// evaluated against it must COMPILE and RESOLVE: eoformula treats an
// identifier absent from the env as a hard error, so a rule naming a key this
// type does not have fails here rather than at a clerk's first save.
func eoNeutralEnv(t *testing.T, db *gorm.DB, typeKey string) map[string]any {
	t.Helper()
	env := map[string]any{}
	for _, d := range eoOwnFieldDefs(t, db, eoBaseTypeKey) {
		env[d.Key] = ""
	}
	for _, d := range eoOwnFieldDefs(t, db, typeKey) {
		env[d.Key] = ""
	}
	return env
}

// The rules refusing something, and staying quiet when they should. The
// evaluator is eoformula.New(0), which is exactly what the record and
// transition services hand ValidateRecordValues.
// Mutation: in eoDirectiveTypeSpec change corrective_action's rule to
// `delay_reason == ""`, or origin_record's list to one that omits
// "official_visit".
func TestRootConditionalRulesFireOnlyOnTheConditionTheyName(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)
	ev := eoformula.New(0)

	tests := []struct {
		name      string
		typeKey   string
		matrixKey string
		fires     []map[string]any
		quiet     []map[string]any
	}{
		{
			name:    "a directive out of a letter, a visit or an appearance needs its origin record",
			typeKey: eoDirectiveTypeKey, matrixKey: "origin_record",
			fires: []map[string]any{
				{"directive_origin": "correspondence"},
				{"directive_origin": "official_visit"},
				{"directive_origin": "media_commitment"},
			},
			quiet: []map[string]any{
				{"directive_origin": "direct_instruction"},
				{"directive_origin": "weekly_meeting"},
				{"directive_origin": "committee"},
			},
		},
		{
			name:    "a recorded delay needs the corrective action beside it",
			typeKey: eoDirectiveTypeKey, matrixKey: "corrective_action",
			fires: []map[string]any{{"delay_reason": "the supplier withdrew"}},
			quiet: []map[string]any{{"delay_reason": ""}, {"delay_reason": "   "}},
		},
		{
			name:    "a redraft recommendation needs the redraft",
			typeKey: eoDraftStudyTypeKey, matrixKey: "redraft_text",
			fires: []map[string]any{{"recommendation": "approve_as_redrafted"}},
			quiet: []map[string]any{
				{"recommendation": "approve_as_proposed"},
				{"recommendation": "reject"},
				{"recommendation": "return_for_clarification"},
			},
		},
		{
			name:    "refer and consolidate needs the body that consolidates",
			typeKey: eoIncomingTypeKey, matrixKey: "coordinator",
			fires: []map[string]any{{"direction_pattern": "refer_and_consolidate"}},
			quiet: []map[string]any{
				{"direction_pattern": "refer_out"},
				{"direction_pattern": "finish_in_office"},
				{"direction_pattern": "file_for_information"},
				{"direction_pattern": "no_action"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			def := eoEffectiveField(t, db, tc.typeKey, tc.matrixKey)
			if def.RequiredMode != models.EORequiredConditional {
				t.Fatalf("required_mode = %q, want %q", def.RequiredMode, models.EORequiredConditional)
			}
			run := func(overlay map[string]any) bool {
				t.Helper()
				env := eoNeutralEnv(t, db, tc.typeKey)
				for k, v := range overlay {
					if _, ok := env[k]; !ok {
						t.Fatalf("the rule's env has no key %q: no field of %q or of the base type defines it", k, tc.typeKey)
					}
					env[k] = v
				}
				got, err := ev.EvalBool(def.RequiredRule, env)
				if err != nil {
					t.Fatalf("rule %q over %v: %v", def.RequiredRule, overlay, err)
				}
				return got
			}
			for _, o := range tc.fires {
				if !run(o) {
					t.Errorf("the rule did not fire on %v, so the field would never be required", o)
				}
			}
			for _, o := range tc.quiet {
				if run(o) {
					t.Errorf("the rule fired on %v, so the field is required when the matrix does not ask for it", o)
				}
			}
			// Nothing supplied at all: the condition is not met, so the field
			// is not required. A rule that fired here would make every blank
			// form invalid.
			if run(map[string]any{}) {
				t.Error("the rule fires on an empty record")
			}
		})
	}
}

// §7.12's "the original is never edited": the only field-level immutability in
// the pilot. The two fields are seeded here; the state they lock after is
// Task A5's, so the seed exposes the operation A5 calls and this pins it.
// Mutation: drop "draft_file" from eoDraftLockedFieldKeys (the row count check
// in eoLockDraftFieldsAfterState then reports one of two), or make that
// function return nil without updating.
func TestDraftFieldsLockAfterSubmit(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	for _, key := range eoDraftLockedFieldKeys {
		def := eoEffectiveField(t, db, eoDraftStudyTypeKey, key)
		if def.IsLockedAfterStateID != nil {
			t.Errorf("%s is already locked after state %v; Task A5 owns the state", key, def.IsLockedAfterStateID)
		}
	}

	submitted := uuid.New()
	if err := eoLockDraftFieldsAfterState(db, submitted); err != nil {
		t.Fatalf("lock the draft fields: %v", err)
	}
	for _, key := range eoDraftLockedFieldKeys {
		def := eoEffectiveField(t, db, eoDraftStudyTypeKey, key)
		if def.IsLockedAfterStateID == nil || *def.IsLockedAfterStateID != submitted {
			t.Errorf("%s locked after %v, want %v", key, def.IsLockedAfterStateID, submitted)
		}
	}
	// No other field of the three types is locked: a lock on the wrong field
	// is as bad as a missing one.
	for _, typeKey := range []string{eoDirectiveTypeKey, eoDraftStudyTypeKey, eoIncomingTypeKey} {
		for _, d := range eoOwnFieldDefs(t, db, typeKey) {
			locked := d.IsLockedAfterStateID != nil
			want := typeKey == eoDraftStudyTypeKey && (d.Key == "draft_text" || d.Key == "draft_file")
			if locked != want {
				t.Errorf("%s.%s locked = %v, want %v", typeKey, d.Key, locked, want)
			}
		}
	}

	// It fails closed on a state that is not one: a nil id would lock nothing
	// and report success.
	if err := eoLockDraftFieldsAfterState(db, uuid.Nil); err == nil {
		t.Error("locking after the nil state was accepted")
	}
}

// Addendum 2. Creating a partition makes Postgres take an ACCESS EXCLUSIVE
// lock on the default partition and scan it, so a type that already has
// records there can never be given one without a data move. The only free
// moment is the one that creates the type — which is what this proves.
// Mutation: set PartitionPending: true and delete the
// migrations.CreateEORecordPartition call from eoSeedRootTypes.
func TestSeededTypeRecordsLandInTheirOwnPartition(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	for _, typeKey := range []string{eoDirectiveTypeKey, eoDraftStudyTypeKey, eoIncomingTypeKey} {
		t.Run(typeKey, func(t *testing.T) {
			ty := eoRootType(t, db, typeKey)
			rec := models.EORecord{
				ID: uuid.New(), RecordTypeID: ty.ID, Reference: ty.ReferencePrefix + "-2026-001",
				SubjectAr: "x", Priority: "normal", Sensitivity: "internal", Source: "correspondence",
				CreatedByID: uuid.New(),
			}
			if err := db.Create(&rec).Error; err != nil {
				t.Fatalf("insert a record of %q: %v", typeKey, err)
			}
			var partition string
			if err := db.Raw(`SELECT tableoid::regclass::text FROM eo_records WHERE id = ?`, rec.ID).
				Scan(&partition).Error; err != nil {
				t.Fatalf("read the record's partition: %v", err)
			}
			want := migrations.EORecordPartitionName(typeKey)
			if partition != want {
				t.Errorf("the record landed in %q, want %q", partition, want)
			}
		})
	}

	// The control: a type with no partition of its own still lands in the
	// default, so the assertion above is about the partition and not about
	// every record everywhere.
	t.Run("a type without a partition still lands in the default", func(t *testing.T) {
		other := models.EORecordType{
			ID: uuid.New(), Key: "eo_unpartitioned_probe", Name: "Probe",
			ReferenceMode: models.EOReferenceModeOwn, ReferencePrefix: "PRB",
			ReferencePattern: "PRB-YYYY-NNN", IsActive: true, PartitionPending: true,
		}
		if err := db.Create(&other).Error; err != nil {
			t.Fatal(err)
		}
		rec := models.EORecord{
			ID: uuid.New(), RecordTypeID: other.ID, Reference: "PRB-2026-001",
			SubjectAr: "x", Priority: "normal", Sensitivity: "internal", Source: "correspondence",
			CreatedByID: uuid.New(),
		}
		if err := db.Create(&rec).Error; err != nil {
			t.Fatal(err)
		}
		var partition string
		if err := db.Raw(`SELECT tableoid::regclass::text FROM eo_records WHERE id = ?`, rec.ID).
			Scan(&partition).Error; err != nil {
			t.Fatal(err)
		}
		if partition != "eo_records_default" {
			t.Errorf("an unpartitioned type's record landed in %q, want eo_records_default", partition)
		}
	})
}

// CREATE TABLE IF NOT EXISTS matches on the NAME, so a partition left over
// from a type that was deleted and recreated under the same key would be
// silently kept — bound to a dead id, with every new record going to the
// default and promotion no longer free. Mutation: delete the relpartbound
// check from migrations.CreateEORecordPartition.
func TestCreateEORecordPartitionRefusesAMismatchedBound(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	key := eoDirectiveTypeKey
	ty := eoRootType(t, db, key)
	if err := migrations.CreateEORecordPartition(db, key, ty.ID); err != nil {
		t.Errorf("re-running the same partition is not idempotent: %v", err)
	}
	if err := migrations.CreateEORecordPartition(db, key, uuid.New()); err == nil {
		t.Error("a partition bound to a different record type id was accepted")
	}
	for _, bad := range []string{"", "Directive", "eo-directive", `eo"; DROP TABLE eo_records; --`} {
		if err := migrations.CreateEORecordPartition(db, bad, uuid.New()); err == nil {
			t.Errorf("record type key %q was accepted as a partition name", bad)
		}
	}
	if err := migrations.CreateEORecordPartition(db, "eo_nil_probe", uuid.Nil); err == nil {
		t.Error("the nil record type id was accepted")
	}
}

// The designer refuses a field whose kind is not a configured kind, whose
// provenance or required_mode is outside the vocabulary, or which is derived
// with no formula. Seeded data goes in behind the HTTP path, so nothing would
// otherwise hold it to the same rules.
//
// THIS RE-IMPLEMENTS handlers.eoValidateFieldShape
// (internal/handlers/eo_record_type_handler.go, func eoValidateFieldShape) and
// cannot call it: that function is unexported, takes a *fiber.Ctx for its i18n
// lookups, and package handlers imports package database. The two must be kept
// in step by hand — a rule added there should be added here. The extra checks
// below (valid JSON config, dropdowns with options, no invented Arabic in an
// option label) are not in the designer's set; they are properties of SEEDED
// configuration specifically.
// Mutation: give any seeded field Kind: "drop_down".
func TestRootTypeFieldsPassTheDesignerShapeRules(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	for _, typeKey := range []string{eoDirectiveTypeKey, eoDraftStudyTypeKey, eoIncomingTypeKey} {
		for _, d := range eoOwnFieldDefs(t, db, typeKey) {
			t.Run(typeKey+"."+d.Key, func(t *testing.T) {
				if d.Label == "" {
					t.Error("no label")
				}
				if !models.IsEOFieldKind(d.Kind) {
					t.Errorf("kind %q is not one of models.EOFieldKinds", d.Kind)
				}
				switch d.Provenance {
				case models.EOProvenanceUser, models.EOProvenanceAuto, models.EOProvenanceDerived, models.EOProvenanceSystem:
				default:
					t.Errorf("provenance %q is outside the vocabulary", d.Provenance)
				}
				switch d.RequiredMode {
				case models.EORequiredNever, models.EORequiredAlways, models.EORequiredConditional, models.EORequiredAtState:
				default:
					t.Errorf("required_mode %q is outside the vocabulary", d.RequiredMode)
				}
				if d.Provenance == models.EOProvenanceDerived && strings.TrimSpace(d.Formula) == "" {
					t.Error("derived with no formula: it computes nothing and looks configured")
				}
				if d.Provenance != models.EOProvenanceDerived && strings.TrimSpace(d.Formula) != "" {
					t.Error("a formula on a field nothing recomputes never runs")
				}
				if !json.Valid(d.Config) {
					t.Errorf("config is not valid JSON: %s", d.Config)
				}
				if d.Kind == models.EOKindDropdown || d.Kind == models.EOKindMultiSelect {
					var cfg struct {
						Options []struct {
							Value   string `json:"value"`
							Label   string `json:"label"`
							LabelAr string `json:"label_ar"`
						} `json:"options"`
					}
					if err := json.Unmarshal(d.Config, &cfg); err != nil {
						t.Fatalf("config: %v", err)
					}
					if len(cfg.Options) == 0 {
						t.Error("a dropdown with no options can never be filled in")
					}
					for _, o := range cfg.Options {
						if o.Value == "" || o.Label == "" {
							t.Errorf("option %+v has no value or no label", o)
						}
						if o.LabelAr != o.Label {
							t.Errorf("option %q carries Arabic %q the client has not signed off", o.Value, o.LabelAr)
						}
					}
				}
			})
		}
	}
}

// A narrowing redefines an inherited field AT THE ANCESTOR'S SLOT, so the copy
// must be the base's STORED definition in every respect but its requiredness
// and, where the type has its own name for the field, its label. A hand-written
// override would quietly freeze the kind, options and provenance of the day it
// was written. Mutation: in eoPilotFieldDefs, build the narrowing from
// models.EOFieldDef{Key: key, Kind: models.EOKindDate} instead of copying src;
// or read baseFields from eoBaseFieldDefs again instead of from tx.
func TestNarrowedInheritedFieldsCopyTheBaseDefinition(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	base := eoEffectiveField(t, db, eoBaseTypeKey, "due_date")
	tests := []struct {
		name, typeKey, wantLabel string
	}{
		// The Directive narrows requiredness only: the field is still "Due
		// date" and keeps the base's Arabic.
		{"Directive narrows requiredness only", eoDirectiveTypeKey, base.Label},
		// Incoming Correspondence also renames it: the matrix calls this
		// field "Their deadline" (`sender_deadline`), and it IS the engine's
		// due_date.
		{"Incoming Correspondence renames it too", eoIncomingTypeKey, "Their deadline"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := eoEffectiveField(t, db, tc.typeKey, "due_date")
			if got.ID == base.ID {
				t.Fatal("the type does not redefine due_date at all")
			}
			if got.RequiredMode != models.EORequiredAlways {
				t.Errorf("required_mode = %q, want %q: this type's clock watches it",
					got.RequiredMode, models.EORequiredAlways)
			}
			if got.RequiredRule != "" {
				t.Errorf("required_rule = %q on an `always` field; it is never evaluated", got.RequiredRule)
			}
			if got.Label != tc.wantLabel {
				t.Errorf("label = %q, want %q", got.Label, tc.wantLabel)
			}
			// Everything the narrowing does not set comes from the base.
			if got.Key != base.Key || got.Kind != base.Kind ||
				got.Provenance != base.Provenance || string(got.Config) != string(base.Config) {
				t.Errorf("the narrowing drifted from the inherited definition:\n got %+v\nbase %+v", got, base)
			}
			if !got.ArUntranslated {
				t.Error("the narrowing is not marked as awaiting Arabic sign-off")
			}
			if tc.wantLabel != base.Label && got.LabelAr != tc.wantLabel {
				t.Errorf("label_ar = %q; a renamed field carries the English until sign-off, not the base's Arabic for a different field", got.LabelAr)
			}
		})
	}

	// The Draft Decision Study does not narrow it, so the inherited definition
	// still governs there: an override is per type, not global.
	if d := eoEffectiveField(t, db, eoDraftStudyTypeKey, "due_date"); d.ID != base.ID {
		t.Error("the Draft Decision Study redefines due_date; its clock is study_deadline")
	}
}

// The wiring proof for Review Focus 1: a test that only calls
// models.ValidateReferencePrefix itself passes whether or not the SEED calls
// it. Here the register already holds a type whose prefix extends EO-DIR, so
// the seed must refuse — and, being transactional, must leave no root type
// behind. Mutation: delete the `models.ValidateReferencePrefix` call from
// eoSeedRootTypes.
func TestSeedRefusesARootTypeWhosePrefixOverlaps(t *testing.T) {
	db := eoPilotDB(t)
	squatter := models.EORecordType{
		ID: uuid.New(), Key: "eo_prefix_squatter", Name: "Squatter",
		ReferenceMode: models.EOReferenceModeOwn, ReferencePrefix: "EO-DIR-X",
		ReferencePattern: "EO-DIR-X-YYYY-NNN", IsActive: true,
	}
	if err := db.Create(&squatter).Error; err != nil {
		t.Fatal(err)
	}

	var logged strings.Builder
	prev := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(prev) })
	seedEOPilot(db)
	log.SetOutput(prev)

	if !strings.Contains(logged.String(), "FAILED") {
		t.Errorf("the seed did not fail on an overlapping prefix; log was:\n%s", logged.String())
	}
	for _, key := range []string{eoDirectiveTypeKey, eoDraftStudyTypeKey, eoIncomingTypeKey} {
		var n int64
		if err := db.Model(&models.EORecordType{}).Where("key = ?", key).Count(&n).Error; err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("record type %q was created although the step failed; the step is not atomic", key)
		}
	}
	// And the partition DDL rolled back with it: a leftover partition would
	// survive to be bound to a different id on the next attempt.
	var partitions int64
	if err := db.Raw(
		`SELECT count(*) FROM pg_class WHERE relname = ? AND relnamespace = current_schema()::regnamespace`,
		migrations.EORecordPartitionName(eoDirectiveTypeKey)).Scan(&partitions).Error; err != nil {
		t.Fatal(err)
	}
	if partitions != 0 {
		t.Error("the partition survived a rolled-back seed")
	}
}

// eoPilotFieldDefs' own refusals. Each is a configuration that would be
// accepted silently and then misbehave at a clerk's first save.
// Mutation: delete any one of the three guards in eoPilotFieldDefs.
func TestEOPilotFieldDefsRefusesUnusableSpecs(t *testing.T) {
	base := eoBaseFieldDefs(uuid.New())
	tests := []struct {
		name string
		spec eoPilotTypeSpec
	}{
		{"conditional with no rule", eoPilotTypeSpec{key: "t", fields: []eoPilotFieldSpec{
			{matrixKey: "x", label: "X", kind: models.EOKindText,
				provenance: models.EOProvenanceUser, required: models.EORequiredConditional},
		}}},
		{"a rule on a field that is never conditional", eoPilotTypeSpec{key: "t", fields: []eoPilotFieldSpec{
			{matrixKey: "x", label: "X", kind: models.EOKindText,
				provenance: models.EOProvenanceUser, required: models.EORequiredAlways, rule: `true`},
		}}},
		{"derived with no formula", eoPilotTypeSpec{key: "t", fields: []eoPilotFieldSpec{
			{matrixKey: "x", label: "X", kind: models.EOKindNumber,
				provenance: models.EOProvenanceDerived, required: models.EORequiredNever},
		}}},
		{"a formula on a field nothing recomputes", eoPilotTypeSpec{key: "t", fields: []eoPilotFieldSpec{
			{matrixKey: "x", label: "X", kind: models.EOKindNumber,
				provenance: models.EOProvenanceUser, required: models.EORequiredNever, formula: "1 + 1"},
		}}},
		{"narrowing a key the base does not define", eoPilotTypeSpec{key: "t",
			narrowed: []eoInheritedNarrowing{{matrixKey: "no_such_key", required: models.EORequiredAlways}}}},
		{"narrowing an engine-managed key", eoPilotTypeSpec{key: "t",
			narrowed: []eoInheritedNarrowing{{matrixKey: "state", required: models.EORequiredAlways}}}},
		{"narrowing to conditional with no rule", eoPilotTypeSpec{key: "t",
			narrowed: []eoInheritedNarrowing{{matrixKey: "due_date", required: models.EORequiredConditional}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := eoPilotFieldDefs(tc.spec, uuid.New(), base); err == nil {
				t.Error("accepted")
			}
		})
	}

	// The control: the real specs are all accepted, so the guards above are
	// not simply refusing everything.
	for _, spec := range eoPilotRootTypes {
		if _, err := eoPilotFieldDefs(spec, uuid.New(), base); err != nil {
			t.Errorf("the seeded spec %q is refused by its own guards: %v", spec.key, err)
		}
	}
}

// Every field of the three root types, checked against the Required column of
// the client's own document — not against a transcription of it. The matrix's
// column is five-valued (Yes, Conditional, Yes at closure, Auto, Derived) plus
// No, and each value has one engine spelling; a Conditional must be covered by
// eoConditionalDecisions, which is what makes a weakened requirement a
// recorded decision rather than an accident.
//
// This is the test that holds the ~45 non-Conditional requirednesses, which
// nothing else constrains. Mutation: change any `required:` in
// eoPilotRootTypes — e.g. `grounds` from EORequiredAlways to EORequiredNever —
// or any `provenance:` on an Auto or Derived row.
func TestSeededRequiredModesMatchTheMatrix(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)
	matrix := eoReadFieldMatrix(t)

	decided := map[string]eoConditionalDecision{}
	for _, d := range eoConditionalDecisions {
		decided[d.TypeKey+"."+d.MatrixKey] = d
	}

	checked := 0
	for _, typeKey := range []string{eoDirectiveTypeKey, eoDraftStudyTypeKey, eoIncomingTypeKey} {
		for engineKey, row := range eoMatrixExpectations(t, matrix, typeKey) {
			checked++
			t.Run(typeKey+"."+engineKey, func(t *testing.T) {
				got := eoEffectiveField(t, db, typeKey, row.Key)
				want := func(mode, provenance string) {
					t.Helper()
					if got.RequiredMode != mode {
						t.Errorf("the matrix says %q, so required_mode should be %q; it is %q",
							row.Required, mode, got.RequiredMode)
					}
					if provenance != "" && got.Provenance != provenance {
						t.Errorf("the matrix says %q, so provenance should be %q; it is %q",
							row.Required, provenance, got.Provenance)
					}
				}
				switch row.Required {
				case "Yes":
					want(models.EORequiredAlways, models.EOProvenanceUser)
				case "No":
					want(models.EORequiredNever, models.EOProvenanceUser)
				case "Auto":
					want(models.EORequiredNever, models.EOProvenanceAuto)
				case "Derived":
					want(models.EORequiredNever, models.EOProvenanceDerived)
				case "Yes at closure":
					// Enforced as a workflow requirement on the terminal
					// transition, which is required_at_state. None of the
					// three root types has one; a sub type does (Assignment's
					// `evidence`), so this arm exists for Task A3.
					want(models.EORequiredAtState, models.EOProvenanceUser)
				case "Conditional":
					d, ok := decided[typeKey+"."+row.Key]
					if !ok {
						t.Fatalf("the matrix marks this Conditional and eoConditionalDecisions records no decision for it")
					}
					if got.RequiredMode != d.Mode {
						t.Errorf("required_mode is %q, the recorded decision says %q", got.RequiredMode, d.Mode)
					}
				default:
					t.Fatalf("unknown Required value %q in the matrix; the test does not know what to expect of it", row.Required)
				}
			})
		}
	}
	// 15 inherited keys (17 less the two engine-managed) on each of three
	// types, plus each type's own rows that are not already one of those 15:
	// Directive 15+13, Draft Decision Study 15+21, Incoming Correspondence
	// 15+14 (its `sender_deadline` and `attachments` rows name engine keys the
	// inherited table already contributed). The per-section row counts in
	// eoReadFieldMatrix are the primary guard against a parse that finds
	// nothing; this is the second.
	if checked != 28+36+29 {
		t.Errorf("checked %d (type, field) pairs, want %d", checked, 28+36+29)
	}
}
