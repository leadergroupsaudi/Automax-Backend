package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"gorm.io/datatypes"
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
// eoTypesAwaitingPermissions counts the CONCRETE record types that do not yet
// carry spec §7.11's per-type codes. It asks for the `view` code only: the
// three are written together, in one loop, inside one transaction.
func eoTypesAwaitingPermissions(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	var types []models.EORecordType
	if err := db.Where("is_abstract = ?", false).Find(&types).Error; err != nil {
		t.Fatalf("read concrete record types: %v", err)
	}
	var pending int64
	for _, ty := range types {
		var n int64
		if err := db.Model(&models.Permission{}).Where("code = ?", "eo:"+ty.Key+":view").Count(&n).Error; err != nil {
			t.Fatalf("count the per-type codes of %q: %v", ty.Key, err)
		}
		if n == 0 {
			pending++
		}
	}
	return pending
}

func eoRootFieldCount(t *testing.T) int64 {
	t.Helper()
	base := eoBaseFieldDefs(uuid.New())
	var n int64
	specs := append(append([]eoPilotTypeSpec{}, eoPilotRootTypes...), eoPilotChildTypes...)
	for _, spec := range specs {
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
		types: 1 + int64(len(eoPilotRootTypes)+len(eoPilotChildTypes)),
		// Task A6 REMOVES a field definition an enabled capability reserves
		// (eoCapabilityReservedFieldRemovals), so the root types end the seed
		// with one fewer than their specs declare.
		fields: 17 + eoRootFieldCount(t) - int64(len(eoCapabilityReservedFieldRemovals)),
		// Plan 1's one smoke link type goes; Task A4's eight replace it,
		// counted from the specs for the same reason eoRootFieldCount is:
		// this test is about the smoke REMOVAL and must not fail every time
		// the pilot gains a link type.
		links: int64(len(eoPilotLinkTypes)),
		// The smoke types' six codes and their twelve grants go; Task A6
		// seeds three codes per pilot type and grants each to admin and
		// executive_office. TestEoEveryConcreteTypeHasItsThreePermissions is
		// what holds those rows to spec §7.11; this is the arithmetic that
		// stops the smoke removal hiding behind them.
		grants: before.grants - 12 + 6*int64(len(eoPilotTypeKeys)),
	}
	want.perms = before.perms - 6 + 3*int64(len(eoPilotTypeKeys))
	// Plan 1's two smoke workflows and its one-step escalation policy go;
	// Task A5's eleven machines and its 3/7/14 ladder replace them, counted
	// from the specs for the same reason.
	wfc := eoPilotWorkflowSpecCounts()
	want.workflows, want.states, want.transitions = wfc.workflows, wfc.states, wfc.transitions
	want.requirements, want.policies, want.steps = wfc.requirements, wfc.policies, wfc.policySteps
	want.changes = wfc.fieldChanges
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
			// eleven pilot types even on a populated database — that is the
			// point of them — but it must leave everything already there
			// alone, and the second run below must not touch any record type.
			want := snapBefore
			want.types += int64(len(eoPilotRootTypes) + len(eoPilotChildTypes))
			want.fields += eoRootFieldCount(t)
			// The smoke link type is KEPT here (its types hold records), and
			// the pilot's own link types are added beside it.
			want.links += int64(len(eoPilotLinkTypes))
			// The smoke WORKFLOWS are kept here too, and Task A5's eleven
			// are added beside them.
			wfc := eoPilotWorkflowSpecCounts()
			want.workflows += wfc.workflows
			want.states += wfc.states
			want.transitions += wfc.transitions
			want.requirements += wfc.requirements
			want.changes += wfc.fieldChanges
			want.policies += wfc.policies
			want.steps += wfc.policySteps
			// TASK A6's per-type codes: three per concrete type that has
			// none yet, each granted to admin and executive_office. The
			// pending count is taken BEFORE the seed and includes the eleven
			// types it is about to create, because in this test the smoke
			// types are KEPT — they hold records — and they already carry
			// theirs, while a type the case above invented does not.
			pending := eoTypesAwaitingPermissions(t, db) + int64(len(eoPilotRootTypes)+len(eoPilotChildTypes))
			want.perms += 3 * pending
			want.grants += 6 * pending
			// And A6's removal of a field definition an enabled capability
			// reserves.
			want.fields -= int64(len(eoCapabilityReservedFieldRemovals))
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

// eoCaptureSeed runs seedEOPilot with the standard logger redirected and
// returns everything it wrote. The package's tests run sequentially, so
// capturing the global logger is safe — TestEoSeedEOPilotLogsTheUnprovidedCapabilityInput
// already does it.
func eoCaptureSeed(t *testing.T, db *gorm.DB) string {
	t.Helper()
	var buf bytes.Buffer
	out, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() { log.SetOutput(out); log.SetFlags(flags) }()
	seedEOPilot(db)
	return buf.String()
}

// eoObligationReportLines is one marker per boot-time obligation report that
// fires in the DEFAULT environment, in the order seedEOPilot writes them: the
// untranslated labels, the unconfirmed state machines, the capability inputs
// the configuration does not provide, and the relaxed file fields.
//
// IT IS NOT ONE LINE PER CALL. eoReportPilotObligations makes SEVEN calls;
// the three missing here are conditional on a state a clean database is not
// in — eoLogCapabilitiesAnAdministratorHasEdited (a capability column edited
// away from the registry), eoLogTypesWithNoLiveCodes (a deleted permission)
// and eoLogRolesWithNoTypeCodes (a role holding an EO module permission,
// which eoSeedRoles does not grant) — and each has a test of its own that
// puts the database into that state.
var eoObligationReportLines = []string{
	"field labels await Arabic sign-off",
	"await client sign-off (workflow_states/workflow_transitions.eo_client_unconfirmed)",
	`capability "decisions_base" on "eo_draft_decision_study"`,
	// TASK B2. The default deployment has EO_DOCUMENTA_ENABLED unset, so
	// this is the disabled wording; the enabled one has a test of its own,
	// because no run in the default environment can reach it.
	"file field(s) are optional while EO Documenta is disabled",
}

// TestSeedEOPilotReportsOnEveryBoot is the guard against the obligation
// reports drifting back under a short-circuit return.
//
// They used to sit at the BOTTOM of seedEOPilot, below the `!needed` return,
// so they printed only on the boot that applied the seed — on every database
// in the world, the lists the client sign-off round depends on existed only
// in the log of the day the database was created. Each report arrived in a
// different task, and each inherited the same gate without anybody noticing
// it was shared. There were three then and there are seven now; this asserts
// the four that fire in the default environment (eoObligationReportLines),
// and the other three are covered where their state is set up.
//
// IT SEEDS TWICE ON PURPOSE. Every existing test of these lines calls
// seedEOPilot once, on a fresh database, where the gate is open and the bug
// is invisible; no such test can catch this, however many of them there are.
// The SECOND call is the whole test.
//
// Mutation that makes it fail: move any report call from
// eoReportPilotObligations back below the `!needed` return in seedEOPilot, or
// drop the `defer`.
func TestSeedEOPilotReportsOnEveryBoot(t *testing.T) {
	db := eoPilotDB(t)

	first := eoCaptureSeed(t, db)
	for _, want := range eoObligationReportLines {
		if !strings.Contains(first, want) {
			t.Fatalf("fixture error: the boot that APPLIES the seed does not report %q, so a second boot proves nothing.\n--- output ---\n%s", want, first)
		}
	}
	if open, err := eoPilotSeedNeeded(db); err != nil || open {
		t.Fatalf("fixture error: the seed should be fully applied (needed=%v err=%v)", open, err)
	}

	second := eoCaptureSeed(t, db)
	if strings.Contains(second, "Applying the EO pilot configuration") {
		t.Fatalf("fixture error: the second boot applied seed steps, so it is not the boot this test is about.\n--- output ---\n%s", second)
	}
	for _, want := range eoObligationReportLines {
		if !strings.Contains(second, want) {
			t.Errorf("a boot that applied no seed step does not report %q; the client sign-off round can then read that list only from the log of the day the database was created.\n--- output ---\n%s", want, second)
		}
	}
}

// TestEveryConvergentStepIsAskedWhetherItHasWork is the structural half of the
// convergent mechanism: a step marked convergent with no `pending` would panic
// at boot, and a one-shot step carrying a `pending` would have it silently
// ignored.
//
// Mutation: drop `pending` from either convergent entry of eoPilotSteps, or
// add one to a one-shot entry.
func TestEveryConvergentStepIsAskedWhetherItHasWork(t *testing.T) {
	for _, st := range eoPilotSteps {
		switch {
		case st.convergent && st.pending == nil:
			t.Errorf("step %q is convergent and has no pending check; it is gated on nothing and would panic at boot", st.key)
		case !st.convergent && st.pending != nil:
			t.Errorf("step %q is one-shot and carries a pending check, which nothing reads; mark it convergent or remove it", st.key)
		case st.run == nil:
			t.Errorf("step %q has no run function", st.key)
		}
	}
}

// TestEOPilotGivesARecordTypeAddedLaterItsPermissionCodes is Important B: the
// class of defect A6 fixed one instance of.
//
// `seed_type_permissions` and `enable_capabilities` exist to cover EVERY
// concrete type, and both had applied rows on every database that had booted
// Task A6. Under a pure one-shot gate, a twelfth record type — added the way
// the mechanism's own instructions say a later change is made, by appending a
// step — would get no permission codes and no capability configuration
// anywhere except on a database created after the change. The engine is not
// the problem: eoSeedTypePermissions is written to be re-runnable and
// incremental, and the gate was the only thing defeating it.
//
// THE NEW TYPE IS CREATED BY A STEP APPENDED AFTER BOTH OF THEM, which is
// where a real one would go, and the codes must still be there when the same
// boot ends — that is what the second convergent pass in eoSeedPilotTx is for.
//
// Mutations that make it fail: take `convergent: true` off the
// seed_type_permissions entry; or delete the second convergent pass at the
// end of eoSeedPilotTx.
func TestEOPilotGivesARecordTypeAddedLaterItsPermissionCodes(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)
	if open, err := eoPilotSeedNeeded(db); err != nil || open {
		t.Fatalf("fixture error: everything should be applied (needed=%v err=%v)", open, err)
	}
	if pending := eoTypesAwaitingPermissions(t, db); pending != 0 {
		t.Fatalf("fixture error: %d types still await their codes before the twelfth is added", pending)
	}

	const newKey = "eo_twelfth_record_type"
	savedSteps := eoPilotSteps
	t.Cleanup(func() { eoPilotSteps = savedSteps })
	eoPilotSteps = append(append([]eoPilotStep{}, savedSteps...), eoPilotStep{
		key: "seed_a_twelfth_record_type",
		run: func(tx *gorm.DB) (bool, error) {
			var base models.EORecordType
			if err := tx.Where("key = ?", eoBaseTypeKey).First(&base).Error; err != nil {
				return false, err
			}
			return true, tx.Create(&models.EORecordType{
				ID: uuid.New(), Key: newKey, Name: "Twelfth", NameAr: "الثاني عشر",
				ParentTypeID: &base.ID, ReferenceMode: models.EOReferenceModeOwn,
				ReferencePrefix: "TWF", ReferencePattern: "TWF-YYYY-NNN", IsActive: true,
			}).Error
		},
	})

	seedEOPilot(db)

	for _, action := range []string{"view", "create", "update"} {
		code := "eo:" + newKey + ":" + action
		var n int64
		if err := db.Model(&models.Permission{}).Where("code = ?", code).Count(&n).Error; err != nil {
			t.Fatalf("count %q: %v", code, err)
		}
		if n != 1 {
			t.Errorf("the record type added after the steps had run carries %d rows for %q, want 1; a type seeded on a live database gets no permission codes at all", n, code)
		}
	}

	// The type really is there, so the codes above are about a real type and
	// not about three rows nobody can use.
	var ty models.EORecordType
	if err := db.Where("key = ?", newKey).First(&ty).Error; err != nil {
		t.Fatalf("read the added record type: %v", err)
	}
	if pending := eoTypesAwaitingPermissions(t, db); pending != 0 {
		t.Errorf("%d concrete types still await their per-type codes after the seed ran", pending)
	}

	// And it converges: a boot with nothing left to do changes nothing and
	// does not re-enter the transaction.
	out := eoCaptureSeed(t, db)
	if strings.Contains(out, "Applying the EO pilot configuration") {
		t.Errorf("the convergent steps report work to do on a database that already has everything; they would rewrite the configuration on every boot.\n--- output ---\n%s", out)
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
	// Kind is the "Field type" cell verbatim: Text, Long text, Dropdown,
	// Date, and — for the one row spec §7.3 drops from the catalogue —
	// "Table (stage, weeks, body, output)", whose parenthesis is the only
	// description the client's document gives of the stage child type.
	Kind string
	// Required is the Required cell verbatim: Yes, No, Conditional, Auto,
	// Derived or "Yes at closure" (the matrix's own five-valued column, plus
	// No).
	Required string
	// Why is the last cell verbatim: the CFF step number and the client's own
	// sentence about the field. Task A5 reads it, because the step numbers
	// are what order the derived state machines and because a "Yes" row whose
	// Why cell names CLOSING is how eoClosureArtefacts is justified against
	// the document rather than against a reading of it.
	Why string
}

// eoMatrixSections names the heading each record type's table sits under.
//
// eo_execution_plan_stage is DELIBERATELY ABSENT and must stay absent: the
// client's document has no section for it, because it is a consequence of
// spec §7.3 dropping the `table` kind rather than a client requirement. A
// missing map entry yields "", and no section of the matrix is named "", so
// the helpers below see the inherited table alone for that type — which is
// the truth: its seventeen inherited fields ARE described by the client's
// document and its own four are not. What guards the four instead is
// eoStageMatrixFields, the roll-up test that reads `weeks` by name, and
// the shape rules every seeded field passes.
var eoMatrixSections = map[string]string{
	eoDirectiveTypeKey:     "Directive",
	eoDraftStudyTypeKey:    "Draft Decision Study",
	eoIncomingTypeKey:      "Incoming Correspondence",
	eoExecutionPlanTypeKey: "Execution Plan",
	eoAssignmentTypeKey:    "Assignment",
	eoObjectionTypeKey:     "Objection",
	eoDecisionTypeKey:      "Decision Record",
	eoReferralTypeKey:      "Referral",
	eoOutgoingTypeKey:      "Outgoing Reply",
	eoTaskTypeKey:          "Task",
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
	"Execution Plan":          8,
	"Assignment":              10,
	"Objection":               6,
	"Decision Record":         8,
	"Referral":                9,
	"Outgoing Reply":          8,
	"Task":                    10,
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
			Kind:     strings.TrimSpace(cells[2]),
			Required: strings.TrimSpace(cells[3]),
			Why:      strings.TrimSpace(cells[5]),
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

// eoMatrixConditionalFields is every Conditional cell the matrix has for
// every pilot type, in the matrix's own spelling, READ FROM THE MATRIX. The
// inherited table's cells count for every type unless the type's own table
// overrides the key. eo_execution_plan_stage has no section of its own, so it
// contributes the inherited five and nothing else.
func eoMatrixConditionalFields(t *testing.T) map[string][]string {
	t.Helper()
	m := eoReadFieldMatrix(t)
	out := map[string][]string{}
	for _, typeKey := range eoPilotTypeKeys {
		section := eoMatrixSections[typeKey]
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
			// TASK A5 NOW BINDS ONE, and the designer's copy of the
			// binding must agree with the one the engine reads
			// (workflows.record_type_id). The earlier reading of this
			// assertion was "want none until Task A5 binds one".
			if ty.WorkflowID == nil {
				t.Errorf("no workflow is bound, so services.ResolveWorkflowForType errors on every create of this type")
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
				// Recorded as deliberately unseeded, with what carries the
				// matrix's requirement instead. The child-type twin of this
				// test has consulted the registry since Task A3; the root
				// types needed it from Task A6, which removes
				// `related_refs` from the Draft Decision Study because the
				// decisions_base capability writes and RESERVES that key.
				// The registry's own subtest in TestEoChildTypeFieldsMatchTheMatrix
				// checks every entry against the database, so this is a
				// recorded exemption and not a way round the matrix.
				if eoMatrixFieldIsNotSeeded(typeKey, r.Key) {
					continue
				}
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
	//
	// TASK B2 RELAXED THE REQUIREDNESS AND NOTHING ELSE. The narrowing still
	// exists — its own definition, at the base's slot — because `always` was
	// a demand no clerk could meet while B1's upload route mints no file id
	// (eoFileFieldsRelaxedUntilDocumenta). Keeping the narrowing means
	// restoring the field is one column rather than a re-seed — which is
	// worth having even though it does NOT mean the type designer will
	// accept it: models.ValidateTypeEdit refuses to make a field required
	// once the type holds records. This subtest therefore still checks the
	// override's identity below.
	t.Run("attachments is declared on Incoming Correspondence and relaxed for want of Documenta", func(t *testing.T) {
		got := eoEffectiveField(t, db, eoIncomingTypeKey, "attachments")
		if got.RequiredMode != models.EORequiredNever {
			t.Errorf("attachments required_mode = %q, want %q", got.RequiredMode, models.EORequiredNever)
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

// Addendum 5: every field definition on every pilot type is marked as
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
	inherited := map[string]bool{}
	for _, d := range eoOwnFieldDefs(t, db, eoBaseTypeKey) {
		inherited[d.Key] = true
	}
	for _, typeKey := range eoPilotTypeKeys {
		defs := eoOwnFieldDefs(t, db, typeKey)
		if len(defs) == 0 {
			t.Fatalf("%s seeds no fields", typeKey)
		}
		for _, d := range defs {
			if !marked[typeKey+"."+d.Key] {
				t.Errorf("%s.%s is not marked as awaiting Arabic sign-off", typeKey, d.Key)
			}
			if inherited[d.Key] {
				// A narrowing of an inherited field COPIES the base type's
				// definition, Arabic and all (which the base already marks
				// unsigned). Its own test pins that the copy has not drifted.
				// The predicate is "the BASE type defines this key", not "the
				// key is a column": Incoming Correspondence narrows
				// `attachments`, which is a values-bag key, and a copy of the
				// base row carries the base's Arabic exactly as a column
				// narrowing does.
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
	// Conditional cells on each of the ELEVEN pilot types (55), plus three of
	// the Directive's own, one of the Draft Decision Study's, one of Incoming
	// Correspondence's, and one each on Execution Plan (`supersedes`),
	// Assignment (`from_stage`), Decision Record (`supersedes`), Referral
	// (`parent_referral`) and Outgoing Reply (`delegation_note`) — 10 own
	// cells in all. Objection, Task and the stage type have none of their
	// own. A parse that found none would make both halves of the comparison
	// below empty and the test vacuous.
	if len(wanted) != 65 {
		t.Fatalf("the matrix yields %d Conditional cells across the eleven pilot types, want 65: %v", len(wanted), conditional)
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
// the pilot. A2 seeded the two fields and exposed the operation; TASK A5 CALLS
// IT, with the id of the Draft Decision Study's `submitted` state — which is
// what TestEoDraftOriginalIsLockedAfterTheSubmittedState checks, and what the
// paragraph this test used to carry ("the state they lock after is Task A5's")
// was waiting for. What remains HERE is the operation's own behaviour: it
// moves both fields together, and it fails closed.
// Mutation: drop "draft_file" from eoDraftLockedFieldKeys (the row count check
// in eoLockDraftFieldsAfterState then reports one of two), or make that
// function return nil without updating.
func TestDraftFieldsLockAfterSubmit(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	for _, key := range eoDraftLockedFieldKeys {
		def := eoEffectiveField(t, db, eoDraftStudyTypeKey, key)
		if def.IsLockedAfterStateID == nil {
			t.Errorf("%s is locked after NO state, so §7.12's \"the original is never edited\" is configured by nobody", key)
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

	for _, typeKey := range eoPilotTypeKeys {
		t.Run(typeKey, func(t *testing.T) {
			ty := eoRootType(t, db, typeKey)
			// The reference is built from the type KEY rather than the
			// prefix: a parent-derived type owns no prefix, and this test is
			// about which partition the row lands in, not about numbering.
			rec := models.EORecord{
				ID: uuid.New(), RecordTypeID: ty.ID, Reference: typeKey + "-2026-001",
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

	for _, typeKey := range eoPilotTypeKeys {
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
						// OptionSource is the alternative spec §7.3 allows:
						// a dropdown may draw its values from a
						// LookupCategory, org units, users by role, workflow
						// states or a query, instead of a static list. The
						// pilot uses it once, for Decision Record's
						// `keywords`, whose Options cell names the client's
						// "Subject taxonomy" — a vocabulary no document in
						// this repository contains and which the seed must
						// not invent, because the options of a seeded
						// dropdown are what every record is filed under.
						OptionSource string `json:"option_source"`
						LookupCode   string `json:"lookup_category_code"`
					}
					if err := json.Unmarshal(d.Config, &cfg); err != nil {
						t.Fatalf("config: %v", err)
					}
					if len(cfg.Options) == 0 && cfg.OptionSource == "" {
						t.Error("a dropdown with neither options nor an option_source can never be filled in")
					}
					if len(cfg.Options) > 0 && cfg.OptionSource != "" {
						t.Error("the field declares BOTH a static option list and an option_source; which one governs is then whatever the client reads first")
					}
					if cfg.OptionSource == "lookup_category" && strings.TrimSpace(cfg.LookupCode) == "" {
						t.Error("option_source is a lookup category and no category code names it, so nothing can resolve the options")
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
		// Addendum 2. Until Task A3 this was DROPPED silently: no error, no
		// log, and a type that spelled a column-backed field in `fields:`
		// instead of `narrowed:` simply lost it. Both A2's cases now go
		// through `narrowed:`, so nothing else would exercise the branch.
		{"a column key declared under fields:", eoPilotTypeSpec{key: "t", fields: []eoPilotFieldSpec{
			{matrixKey: "due_date", label: "Due date", kind: models.EOKindDate,
				provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
		}}},
		{"a RENAMED column key declared under fields:", eoPilotTypeSpec{key: "t", fields: []eoPilotFieldSpec{
			{matrixKey: "deadline", label: "Deadline", kind: models.EOKindDate,
				provenance: models.EOProvenanceUser, required: models.EORequiredNever},
		}}},
		// An INHERITED VALUES-BAG key, which is not a column and so slipped
		// through the first version of this guard. It is not dropped — it is
		// written as a second, hand-made definition that shadows the base row
		// while freezing its kind, config and Arabic. Task A2 shipped exactly
		// this for `attachments` on Incoming Correspondence.
		{"an inherited values-bag key declared under fields:", eoPilotTypeSpec{key: "t", fields: []eoPilotFieldSpec{
			{matrixKey: "attachments", label: "Attachments", kind: models.EOKindMultiFile,
				provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
		}}},
		{"narrowing to a provenance a narrowing may not set", eoPilotTypeSpec{key: "t",
			narrowed: []eoInheritedNarrowing{{matrixKey: "due_date", required: models.EORequiredNever,
				provenance: models.EOProvenanceDerived}}}},
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
	for _, spec := range append(append([]eoPilotTypeSpec{}, eoPilotRootTypes...), eoPilotChildTypes...) {
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

	renamed := map[string]eoEngineKeyDecision{}
	for _, d := range eoEngineKeyDecisions {
		renamed[d.TypeKey+"."+d.MatrixKey] = d
	}

	checked := 0
	for _, typeKey := range eoPilotTypeKeys {
		for engineKey, row := range eoMatrixExpectations(t, matrix, typeKey) {
			if eoMatrixFieldIsNotSeeded(typeKey, row.Key) {
				// Recorded as deliberately unseeded, with what carries the
				// requirement instead; its own test checks the registry.
				continue
			}
			checked++
			t.Run(typeKey+"."+engineKey, func(t *testing.T) {
				got := eoEffectiveField(t, db, typeKey, row.Key)
				// A cell whose value an engine mechanism writes to a COLUMN
				// is excused from the naive mapping below, and only with a
				// recorded decision that names the mechanism and states what
				// was seeded. The registry's own test checks the same rows
				// against the database, so this is not a way round the
				// matrix — it is the place where "the matrix says Derived and
				// the engine writes a column" is written down.
				if d, ok := renamed[typeKey+"."+row.Key]; ok {
					if d.EngineKey != engineKey {
						t.Errorf("the decision says %q lives in column %q; the matrix key resolves to %q", row.Key, d.EngineKey, engineKey)
					}
					if d.MatrixRequired != row.Required {
						t.Errorf("the decision quotes the matrix as %q; the matrix says %q", d.MatrixRequired, row.Required)
					}
					if got.RequiredMode != d.SeededMode || got.Provenance != d.SeededProvenance {
						t.Errorf("seeded %q/%q, the recorded decision says %q/%q",
							got.RequiredMode, got.Provenance, d.SeededMode, d.SeededProvenance)
					}
					return
				}
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
				// TASK B2, BEFORE THE SWITCH BECAUSE IT SPANS TWO OF ITS
				// ARMS. A row that holds a DOCUMENTA FILE ID is seeded by
				// its spec as the matrix asks — "Yes" becomes `always`,
				// "Yes at closure" becomes `at_state` — and is then relaxed
				// to `never` by the last step of the seed, because B1's
				// upload route mints no id while Documenta is disabled, so
				// neither demand could ever be met. The registry says which
				// rows, why, and which mode each is restored to, and
				// TestNoPilotFileFieldIsDemandedAtAll checks the converse,
				// that no file field anywhere is left demanded. This is
				// therefore not a way round the matrix.
				if eoIsRelaxedUntilDocumenta(typeKey, row.Key) {
					want(models.EORequiredNever, models.EOProvenanceUser)
					return
				}
				switch row.Required {
				case "Yes":
					// TASK A5, ADDENDUM 4. A "Yes" row the matrix's own Why
					// cell places at CLOSING is seeded `at_state` and
					// pointed at the terminal state by
					// eoRequireAtClosureState, with a recorded decision in
					// eoClosureArtefacts. That registry's own test checks
					// each entry against this document and against the
					// seeded row, so this is not a way round the matrix — it
					// is the place where "the matrix says Yes and the step
					// it names is the last one" is written down. Every other
					// "Yes" stays `always`.
					if eoIsResolvedClosureArtefact(typeKey, row.Key) {
						want(models.EORequiredAtState, models.EOProvenanceUser)
						return
					}
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
	// 15 inherited keys (17 less the two engine-managed) on each of ELEVEN
	// types, plus each type's own rows that are not already one of those 15
	// and are not recorded as deliberately unseeded:
	//
	//	Directive               15+13   Execution Plan      15+7
	//	Draft Decision Study    15+20     (8 rows less `plan_stages`)
	//	  (`related_refs` is removed by Task A6: decisions_base WRITES and
	//	   reserves that key, so no field definition may declare it)
	//	Incoming Correspondence 15+14   Execution Plan Stage 15+0
	//	  (`sender_deadline` and          (no matrix section)
	//	   `attachments` name engine    Assignment          15+10
	//	   keys the inherited table     Objection           15+6
	//	   already contributed)         Decision Record     15+6
	//	                                  (`decision_number` -> reference,
	//	Referral            15+8           `source_study` -> parent_record_id)
	//	  (`deadline` -> due_date)      Outgoing Reply      15+7
	//	Task                15+7           (`out_ref` -> reference)
	//	  (`parent_ticket`, `due_date` and `owner_department` all name
	//	   inherited engine keys)
	//
	// The per-section row counts in eoReadFieldMatrix are the primary guard
	// against a parse that finds nothing; this is the second.
	if checked != 28+35+29+22+15+25+21+21+23+22+22 {
		t.Errorf("checked %d (type, field) pairs, want %d", checked, 28+35+29+22+15+25+21+21+23+22+22)
	}
}

// ── Task A3: the eight child types ──────────────────────────────────────────

// eoStageMatrixFields READS the stage type's four field names out of the
// client's document, from the only place it describes them: the parenthesis of
// Execution Plan's `plan_stages` kind cell, "Table (stage, weeks, body,
// output)" (matrix:95).
//
// HOW THE ROW-COUNT GUARD STAYS HONEST FOR A TYPE THE CLIENT DID NOT DESCRIBE.
// Every other pilot type is checked against a parsed section whose row count is
// pinned, so a parser that silently stopped matching fails loudly.
// eo_execution_plan_stage has no section of its own — it is a consequence of
// spec §7.3 dropping the `table` kind — so that guard cannot reach its four own
// fields. It still reaches its seventeen INHERITED ones through
// eoMatrixExpectations. The four are held by three things that are not
// transcriptions of each other:
//
//	this function, which PARSES the client's own cell rather than restating it,
//	so renaming a seeded field and its expectation together no longer passes;
//
//	the roll-up test, which fails if `weeks` is renamed, removed or made
//	non-numeric, because Execution Plan's total_weeks addresses it by key;
//
//	and the shape rules every seeded field of every type passes.
//
// It carries its own guard, like every other reading of the matrix: four names
// out of a parenthesis of a Table cell, or the row has changed shape and this
// proves nothing.
func eoStageMatrixFields(t *testing.T) []string {
	t.Helper()
	m := eoReadFieldMatrix(t)
	for _, r := range m[eoMatrixSections[eoExecutionPlanTypeKey]] {
		if r.Key != "plan_stages" {
			continue
		}
		if !strings.HasPrefix(r.Kind, "Table") {
			t.Fatalf("the `plan_stages` kind cell is %q; the stage child type exists because spec §7.3 drops the `table` kind, and this is no longer that row", r.Kind)
		}
		lo, hi := strings.Index(r.Kind, "("), strings.LastIndex(r.Kind, ")")
		if lo < 0 || hi < lo {
			t.Fatalf("the `plan_stages` kind cell is %q and names no columns in parentheses; the stage type's fields are described nowhere else", r.Kind)
		}
		var out []string
		for _, part := range strings.Split(r.Kind[lo+1:hi], ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
		if len(out) != 4 {
			t.Fatalf("the `plan_stages` kind cell %q names %d columns, want 4", r.Kind, len(out))
		}
		return out
	}
	t.Fatalf("the matrix has no `plan_stages` row under %q", eoMatrixSections[eoExecutionPlanTypeKey])
	return nil
}

// eoParentDerivedChildTypes are the pilot types whose reference extends their
// parent's, from the matrix's own patterns (addendum 4). They are EXEMPT from
// models.ValidateReferencePrefix and own no prefix.
var eoParentDerivedChildTypes = []string{
	eoExecutionPlanTypeKey, eoStageTypeKey, eoAssignmentTypeKey,
	eoObjectionTypeKey, eoReferralTypeKey,
}

// eoOwnSequenceChildTypes are the sub types that take a root sequence of their
// own — spec §7.9's two-mode case. Task is the sharp one: own numbering AND a
// parent.
var eoOwnSequenceChildTypes = map[string]string{
	eoDecisionTypeKey: "DEC",
	eoOutgoingTypeKey: "OUT",
	eoTaskTypeKey:     "TSK",
}

// Mutation: delete any entry from eoPilotChildTypes, or change
// eoAssignmentTypeKey's spec key to anything but "eo_assignment".
func TestSeedsEveryPilotChildType(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	base := eoRootType(t, db, eoBaseTypeKey)
	want := map[string]string{
		eoExecutionPlanTypeKey: "Execution Plan",
		eoStageTypeKey:         "Execution Plan Stage",
		eoAssignmentTypeKey:    "Assignment",
		eoObjectionTypeKey:     "Objection",
		eoDecisionTypeKey:      "Decision Record",
		eoReferralTypeKey:      "Referral",
		eoOutgoingTypeKey:      "Outgoing Reply",
		eoTaskTypeKey:          "Task",
	}
	if len(want) != 8 {
		t.Fatalf("the task seeds eight child types; this test names %d", len(want))
	}
	for key, name := range want {
		t.Run(key, func(t *testing.T) {
			ty := eoRootType(t, db, key)
			if ty.Name != name {
				t.Errorf("name = %q, want %q", ty.Name, name)
			}
			if ty.IsAbstract {
				t.Error("the type is abstract; a sub type holds records")
			}
			if !ty.IsActive {
				t.Error("the type is inactive")
			}
			// The type parent is the BASE type, not the matrix's "sub of"
			// parent: the record-type parent is field inheritance, and
			// pointing Execution Plan at the Directive type would give every
			// plan the Directive's thirteen fields.
			if ty.ParentTypeID == nil || *ty.ParentTypeID != base.ID {
				t.Errorf("parent type = %v, want the base type %v", ty.ParentTypeID, base.ID)
			}
			if ty.PartitionPending {
				t.Error("partition_pending is true although the seed creates this type's partition")
			}
			// TASK A5 NOW BINDS ONE, and the designer's copy of the
			// binding must agree with the one the engine reads
			// (workflows.record_type_id). The earlier reading of this
			// assertion was "want none until Task A5 binds one".
			if ty.WorkflowID == nil {
				t.Errorf("no workflow is bound, so services.ResolveWorkflowForType errors on every create of this type")
			}
			if strings.TrimSpace(ty.ReferencePattern) == "" {
				t.Error("no reference pattern: services.allocateReference refuses to create a record of a type without one")
			}
		})
	}
	// Task A2 left the Directive's completion_rate rolling up over
	// eoAssignmentTypeKey, a type that did not exist, so every Directive save
	// logged a broken-configuration error. The constant is the hand-off.
	if _, ok := want[eoAssignmentTypeKey]; !ok {
		t.Fatal("the Assignment type is not seeded under eoAssignmentTypeKey")
	}
}

// Addendum 4. A parent-derived type issues every reference inside its
// parent's, so it owns no prefix and ValidateReferencePrefix must not be asked
// of it — a blank prefix would be rejected outright and an invented one is
// then held in place by ValidateTypeEdit once records exist, correctable
// after that only as a §7.14 confirmed destructive edit.
// Mutation: set eoExecutionPlanTypeKey's mode to models.EOReferenceModeOwn.
func TestChildTypesUseParentDerivedReferences(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	for _, key := range eoParentDerivedChildTypes {
		t.Run(key, func(t *testing.T) {
			ty := eoRootType(t, db, key)
			if ty.ReferenceMode != models.EOReferenceModeParent {
				t.Errorf("reference mode = %q, want %q", ty.ReferenceMode, models.EOReferenceModeParent)
			}
			if ty.ReferencePrefix != "" {
				t.Errorf("reference prefix = %q; a parent-derived type owns none, and a truthful one would always overlap its parent's", ty.ReferencePrefix)
			}
			// The exemption is what lets a blank prefix through, so prove the
			// rule is still being applied and still accepts this type.
			var all []models.EORecordType
			if err := db.Find(&all).Error; err != nil {
				t.Fatal(err)
			}
			others := make([]models.EORecordType, 0, len(all))
			for _, o := range all {
				if o.ID != ty.ID {
					others = append(others, o)
				}
			}
			if err := models.ValidateReferencePrefix(ty, others); err != nil {
				t.Errorf("the parent-derived type was rejected by the prefix rule: %v", err)
			}
			// And the rule can still refuse: the same blank prefix on an
			// own-mode type is rejected, so the pass above is the exemption
			// and not a rule that accepts everything.
			own := ty
			own.ReferenceMode = models.EOReferenceModeOwn
			if err := models.ValidateReferencePrefix(own, others); err == nil {
				t.Error("a blank prefix was accepted on an own-mode type, so the exemption proves nothing")
			}
		})
	}
}

// Addendum 4 and spec §7.9's two-mode case: a child may take its own root
// sequence. These three are the ones the rule governs, and their prefixes must
// not overlap the roots' EO-DIR, DRF or IN in either direction.
// Mutation: change eoDecisionTypeKey's prefix to "DRF-DEC".
func TestOutgoingReplyAndDecisionRecordTakeTheirOwnSequence(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	var all []models.EORecordType
	if err := db.Find(&all).Error; err != nil {
		t.Fatal(err)
	}
	patterns := map[string]string{
		eoDecisionTypeKey: "DEC-YYYY-NNN",
		eoOutgoingTypeKey: "OUT-YYYY-NNNN",
		eoTaskTypeKey:     "TSK-YYYY-NNNNN",
	}
	for key, prefix := range eoOwnSequenceChildTypes {
		t.Run(key, func(t *testing.T) {
			ty := eoRootType(t, db, key)
			if ty.ReferenceMode != models.EOReferenceModeOwn {
				t.Errorf("reference mode = %q, want %q", ty.ReferenceMode, models.EOReferenceModeOwn)
			}
			if ty.ReferencePrefix != prefix {
				t.Errorf("reference prefix = %q, want %q", ty.ReferencePrefix, prefix)
			}
			if ty.ReferencePattern != patterns[key] {
				t.Errorf("reference pattern = %q, want %q", ty.ReferencePattern, patterns[key])
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
	// Task is the two-mode case and must not be collapsed into parent-derived
	// numbering: own sequence AND a parent.
	t.Run("Task keeps its own sequence although it hangs off a parent", func(t *testing.T) {
		ty := eoRootType(t, db, eoTaskTypeKey)
		if strings.Contains(ty.ReferencePattern, "#") {
			t.Errorf("pattern %q is parent-derived; the matrix gives the universal type its own TSK sequence", ty.ReferencePattern)
		}
	})
	// The negative half: a prefix extending one of these is refused, so the
	// acceptances above are a rule doing work.
	for _, tc := range []struct{ name, prefix string }{
		{"a longer prefix under DEC", "DEC-X"},
		{"a shorter prefix above OUT", "O"},
		{"the same prefix in another case", "tsk"},
	} {
		t.Run("rejects "+tc.name, func(t *testing.T) {
			candidate := models.EORecordType{
				Key: "eo_overlapping_child_candidate", Name: "Overlapping",
				ReferenceMode: models.EOReferenceModeOwn, ReferencePrefix: tc.prefix,
			}
			if err := models.ValidateReferencePrefix(candidate, all); err == nil {
				t.Errorf("prefix %q was accepted beside the seeded types", tc.prefix)
			}
		})
	}
}

// Spec §7.3 drops `table` from the kind catalogue, and the matrix's only table
// field becomes a child record type. This pins both halves: no seeded field is
// of a kind outside the catalogue (`table` least of all), and the type that
// replaces it exists with the four columns the matrix's cell names.
// Mutation: in eoPilotChildTypes, restore plan_stages as a field of kind
// "table" on Execution Plan; or delete the eo_execution_plan_stage spec.
func TestExecutionPlanHasNoTableField(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	var defs []models.EOFieldDef
	if err := db.Find(&defs).Error; err != nil {
		t.Fatal(err)
	}
	if len(defs) == 0 {
		t.Fatal("no field definitions at all; the test would pass vacuously")
	}
	for _, d := range defs {
		if d.Kind == "table" {
			t.Errorf("field %q is of kind `table`, which spec §7.3 drops from the catalogue", d.Key)
		}
		if !models.IsEOFieldKind(d.Kind) {
			t.Errorf("field %q is of kind %q, which is not in models.EOFieldKinds", d.Key, d.Kind)
		}
	}
	// `plan_stages` is a row of the matrix and must not be a field of any
	// name: a values-bag `plan_stages` of some other kind would satisfy the
	// loop above and still be the blob the stage type exists to replace.
	for _, d := range eoOwnFieldDefs(t, db, eoExecutionPlanTypeKey) {
		if d.Key == "plan_stages" {
			t.Errorf("Execution Plan declares a `plan_stages` field of kind %q; the stages are the child type %q", d.Kind, eoStageTypeKey)
		}
	}

	stage := eoRootType(t, db, eoStageTypeKey)
	if stage.ReferenceMode != models.EOReferenceModeParent {
		t.Errorf("the stage type's reference mode is %q; it is a child of the plan", stage.ReferenceMode)
	}
	got := map[string]models.EOFieldDef{}
	for _, d := range eoOwnFieldDefs(t, db, eoStageTypeKey) {
		got[d.Key] = d
	}
	stageFields := eoStageMatrixFields(t)
	for _, key := range stageFields {
		d, ok := got[key]
		if !ok {
			t.Errorf("the stage type has no %q field; the matrix's `plan_stages` cell names %v", key, stageFields)
			continue
		}
		// The whole cell is one row marked Required Yes, so every column of
		// it is required.
		if d.RequiredMode != models.EORequiredAlways {
			t.Errorf("stage.%s required_mode = %q, want %q: the matrix marks the `plan_stages` row Required Yes",
				key, d.RequiredMode, models.EORequiredAlways)
		}
	}
	if len(got) != len(stageFields) {
		t.Errorf("the stage type declares %d own fields, want exactly the %d of the matrix's `plan_stages` cell %v", len(got), len(stageFields), stageFields)
	}
}

// eoPatternRenders mirrors services.RenderReference's grammar (spec §7.9,
// internal/services/eo_numbering.go).
//
// IT RE-IMPLEMENTS THAT FUNCTION AND CANNOT CALL IT: package services imports
// package database (otpService.go, user_service.go, extension_service.go), so
// a test in package database that imported services would be an import cycle.
// The same constraint is why Task A2's designer-shape test re-implements
// handlers.eoValidateFieldShape. The two must be kept in step by hand — a
// change to the pattern grammar belongs here too.
//
// It returns an error for exactly the inputs the real parser rejects: a
// parent-derived pattern is the parent's segments plus one segment of letters
// then `#`; an own-sequence pattern ends with YYYY then a run of N; and a
// parent-derived pattern needs a parent reference of exactly the pattern's
// segment count less one.
func eoPatternRenders(pattern, parentRef string) error {
	segs := strings.Split(pattern, "-")
	if pattern == "" {
		return fmt.Errorf("reference pattern is empty")
	}
	last := segs[len(segs)-1]
	if strings.Contains(last, "#") {
		if !regexp.MustCompile(`^[A-Za-z]+#$`).MatchString(last) {
			return fmt.Errorf("suffix segment %q must be letters followed by a single #", last)
		}
		if len(segs) < 2 {
			return fmt.Errorf("a parent-derived pattern needs the parent's segments before the suffix")
		}
		if parentRef == "" {
			return fmt.Errorf("pattern %q is parent-derived but no parent reference was given", pattern)
		}
		if got, want := len(strings.Split(parentRef, "-")), len(segs)-1; got != want {
			return fmt.Errorf("pattern %q expects a parent reference of %d segments, %q has %d", pattern, want, parentRef, got)
		}
		return nil
	}
	if len(segs) < 3 || !regexp.MustCompile(`^N+$`).MatchString(last) || segs[len(segs)-2] != "YYYY" {
		return fmt.Errorf("pattern %q: an own-sequence pattern must end with YYYY then a run of N", pattern)
	}
	for _, s := range segs[:len(segs)-2] {
		if !regexp.MustCompile(`^[A-Za-z0-9]+$`).MatchString(s) || s == "YYYY" || regexp.MustCompile(`^N+$`).MatchString(s) {
			return fmt.Errorf("pattern %q: prefix segment %q must be a literal", pattern, s)
		}
	}
	return nil
}

// Every seeded pattern must be one the engine can actually render, because
// allocateReference runs on every create and a pattern it refuses makes the
// type UNCREATABLE — with no record ever reaching the database to show it.
// This is the test that caught the matrix's own `EO-DIR-YYYY-NNN-P`
// (matrix:90): written verbatim, no Execution Plan could ever be created.
// Mutation: drop the `#` from eo_execution_plan's pattern.
func TestEoPilotReferencePatternsRenderAndRefuse(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	// A plausible parent reference per parent-derived type, in the shape the
	// parent's own pattern renders.
	parents := map[string]string{
		eoExecutionPlanTypeKey: "EO-DIR-2026-001",
		eoStageTypeKey:         "EO-DIR-2026-001-P1",
		eoAssignmentTypeKey:    "EO-DIR-2026-001",
		eoObjectionTypeKey:     "DRF-2026-001",
		eoReferralTypeKey:      "IN-2026-0001",
	}
	for _, key := range eoPilotTypeKeys {
		t.Run(key, func(t *testing.T) {
			ty := eoRootType(t, db, key)
			if err := eoPatternRenders(ty.ReferencePattern, parents[key]); err != nil {
				t.Errorf("pattern %q cannot be rendered, so no record of this type can ever be created: %v", ty.ReferencePattern, err)
			}
			if ty.ReferenceMode == models.EOReferenceModeParent {
				// The refusal that stands in for `parent_ticket`'s
				// requiredness on these types: a parent-derived record cannot
				// be numbered without a parent, so it cannot be created.
				if err := eoPatternRenders(ty.ReferencePattern, ""); err == nil {
					t.Error("the pattern renders with no parent reference, so a parentless record of a parent-derived type would be created")
				}
				// And a parent of the wrong depth is refused too.
				if err := eoPatternRenders(ty.ReferencePattern, parents[key]+"-X9"); err == nil {
					t.Error("the pattern renders against a parent one level too deep")
				}
			} else if _, isChild := eoOwnSequenceChildTypes[key]; isChild {
				// The other side of eoEngineKeyDecisions' Task entry: an
				// own-sequence child numbers cleanly with NO parent, so
				// nothing in the engine refuses a parentless one. Recorded as
				// a gap, pinned here so it cannot be closed silently or
				// quietly worsen.
				if err := eoPatternRenders(ty.ReferencePattern, ""); err != nil {
					t.Errorf("an own-sequence type failed to render without a parent: %v", err)
				}
			}
		})
	}
	// The matrix's own tension, pinned rather than hidden: the Assignment
	// pattern counts the DIRECTIVE's segments, while the matrix says an
	// Assignment may be "sub of Execution Plan, or of Directive directly".
	// Under a plan, the create is refused at numbering.
	t.Run("an Assignment under an Execution Plan is refused at numbering", func(t *testing.T) {
		ty := eoRootType(t, db, eoAssignmentTypeKey)
		if err := eoPatternRenders(ty.ReferencePattern, "EO-DIR-2026-001-P1"); err == nil {
			t.Error("it rendered; the report records this as the matrix's tension, so a change here is a decision, not a fix")
		}
	})
}

// Every row of each child type's own matrix table is seeded, under the ENGINE
// spelling, and nothing else is — with two recorded exceptions: a key the
// registry records as deliberately unseeded, and a narrowing of an inherited
// key.
// Mutation: delete any field from any spec in eoPilotChildTypes.
func TestEoChildTypeFieldsMatchTheMatrix(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)
	matrix := eoReadFieldMatrix(t)

	// The escape below is the keys each spec actually lists in `narrowed:`,
	// NOT every inherited key. Excusing the whole inherited seventeen would
	// mean a spurious own definition keyed `priority` or `source` — a
	// hand-written shadow of an inherited row, which is what `narrowed:`
	// exists to prevent — was reported by nothing.
	narrowedBySpec := map[string]bool{}
	for _, spec := range eoPilotChildTypes {
		for _, n := range spec.narrowed {
			key, _, _ := eoMatrixKey(n.matrixKey)
			narrowedBySpec[spec.key+"."+key] = true
		}
	}
	// A renamed cell is skipped from `want` under its matrix spelling, so its
	// ENGINE spelling must be allowed here — that is the whole point of the
	// rename. The registry's own test checks the field itself.
	for _, d := range eoEngineKeyDecisions {
		narrowedBySpec[d.TypeKey+"."+d.EngineKey] = true
	}
	for _, typeKey := range eoPilotChildTypeKeys() {
		if typeKey == eoStageTypeKey {
			continue // no matrix section; eoStageMatrixFields holds it
		}
		t.Run(typeKey, func(t *testing.T) {
			rows := matrix[eoMatrixSections[typeKey]]
			if len(rows) == 0 {
				t.Fatalf("no matrix rows parsed for %q", typeKey)
			}
			renamed := map[string]bool{}
			for _, d := range eoEngineKeyDecisions {
				renamed[d.TypeKey+"."+d.MatrixKey] = true
			}
			want := map[string]bool{}
			for _, r := range rows {
				if eoMatrixFieldIsNotSeeded(typeKey, r.Key) {
					continue
				}
				if renamed[typeKey+"."+r.Key] {
					// A column-backed cell is declared as its COLUMN, which
					// may or may not need a narrowing of its own (the base's
					// definition sometimes already says what the matrix
					// wants). TestEoEngineKeyDecisionsAreDeclaredAsTheEngineSpellsThem
					// checks the effective field for every one of them, which
					// is the question that matters; checking for a row ON
					// THIS TYPE would force a redundant override.
					continue
				}
				key, _, managed := eoMatrixKey(r.Key)
				if managed {
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
					t.Errorf("field %q is in the matrix for this type but not seeded on it", k)
				}
			}
			for k := range got {
				if !want[k] && !narrowedBySpec[typeKey+"."+k] {
					t.Errorf("field %q is seeded but is neither in the matrix for this type nor listed in its `narrowed:`", k)
				}
			}
		})
	}

	// The registry's claim, checked against the database: a row recorded as
	// deliberately unseeded really is absent, and really is in the matrix.
	t.Run("the unseeded rows are recorded and absent", func(t *testing.T) {
		if len(eoMatrixFieldsNotSeeded) == 0 {
			t.Fatal("the registry is empty; the loop below proves nothing")
		}
		for _, e := range eoMatrixFieldsNotSeeded {
			if strings.TrimSpace(e.Why) == "" || strings.TrimSpace(e.Instead) == "" {
				t.Errorf("%s.%s is recorded as unseeded without saying why or what carries it instead", e.TypeKey, e.MatrixKey)
			}
			inMatrix := false
			for _, r := range matrix[eoMatrixSections[e.TypeKey]] {
				if r.Key == e.MatrixKey {
					inMatrix = true
				}
			}
			if !inMatrix {
				t.Errorf("%s.%s is recorded as deliberately unseeded, but the matrix has no such row for that type", e.TypeKey, e.MatrixKey)
			}
			key, _, _ := eoMatrixKey(e.MatrixKey)
			for _, d := range eoOwnFieldDefs(t, db, e.TypeKey) {
				if d.Key == key {
					t.Errorf("%s.%s is recorded as deliberately unseeded and is seeded anyway", e.TypeKey, e.MatrixKey)
				}
			}
		}
	})
}

// Addendum 1, generalised. A matrix cell whose value an engine mechanism
// writes to a COLUMN must be declared as that column's key, not as its matrix
// spelling — otherwise the clerk reads an empty values-bag field beside a
// column the engine has already written, and nothing reports the difference.
// Mutation: delete the `"deadline": {"due_date", true, false}` entry from
// eoMatrixKeys (the Referral's deadline then becomes a values-bag field of its
// own and the narrowing disappears).
func TestEoEngineKeyDecisionsAreDeclaredAsTheEngineSpellsThem(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	if len(eoEngineKeyDecisions) == 0 {
		t.Fatal("the registry is empty")
	}
	seen := map[string]bool{}
	for _, d := range eoEngineKeyDecisions {
		id := d.TypeKey + "." + d.MatrixKey
		if seen[id] {
			t.Errorf("two decisions for %s", id)
		}
		seen[id] = true
		t.Run(id, func(t *testing.T) {
			if strings.TrimSpace(d.Mechanism) == "" {
				t.Error("the decision does not name what writes the column")
			}
			if strings.TrimSpace(d.Why) == "" {
				t.Error("the decision does not say why")
			}
			// The key map must agree that this matrix key IS that column.
			key, isColumn, managed := eoMatrixKey(d.MatrixKey)
			if managed {
				t.Fatalf("%q is engine-managed; no type may declare it at all", d.MatrixKey)
			}
			// The registry and the key map must agree about WHERE the value
			// lives, in both directions: a column entry marked values-bag
			// would excuse the naive sweep for a field that really is
			// inherited, and a values-bag entry marked column would reinstate
			// exactly the column-only blindness that let the §7.11 visibility
			// control through.
			if isColumn != d.IsColumn {
				t.Errorf("eoMatrixKey says is_column=%v for %q; the decision says %v", isColumn, d.MatrixKey, d.IsColumn)
			}
			if key != d.EngineKey {
				t.Errorf("eoMatrixKey maps %q to %q; the decision says %q", d.MatrixKey, key, d.EngineKey)
			}
			// And no values-bag field is seeded under the MATRIX spelling:
			// that is the failure itself, not a near miss.
			for _, f := range eoOwnFieldDefs(t, db, d.TypeKey) {
				if f.Key == d.MatrixKey && d.MatrixKey != d.EngineKey {
					t.Errorf("a field keyed %q is seeded beside the column %q; one of the two is always empty", d.MatrixKey, d.EngineKey)
				}
			}
			got := eoEffectiveField(t, db, d.TypeKey, d.MatrixKey)
			if got.Key != d.EngineKey {
				t.Errorf("the effective field is keyed %q, want the column %q", got.Key, d.EngineKey)
			}
			if got.RequiredMode != d.SeededMode {
				t.Errorf("required_mode = %q, the decision says %q", got.RequiredMode, d.SeededMode)
			}
			if got.Provenance != d.SeededProvenance {
				t.Errorf("provenance = %q, the decision says %q", got.Provenance, d.SeededProvenance)
			}
		})
	}

	// The Referral's deadline is the one that cost Task A2 a fix round, so it
	// is pinned by name as well as by the registry: provenance `auto` is what
	// stops a clerk typing a deadline the matrix says is "not typed", and
	// `never` is what stops the form demanding one.
	t.Run("the Referral's deadline is the inherited due_date column", func(t *testing.T) {
		got := eoEffectiveField(t, db, eoReferralTypeKey, "deadline")
		base := eoEffectiveField(t, db, eoBaseTypeKey, "due_date")
		if got.Key != "due_date" {
			t.Fatalf("key = %q, want due_date", got.Key)
		}
		if got.ID == base.ID {
			t.Error("the Referral does not redefine due_date at all, so it carries the base's label rather than the matrix's")
		}
		if got.Kind != base.Kind {
			t.Errorf("kind = %q, want the inherited %q: a narrowing copies the base definition", got.Kind, base.Kind)
		}
		if got.Provenance == base.Provenance {
			t.Errorf("provenance is still %q; the matrix marks this field Derived and \"not typed\"", got.Provenance)
		}
		if got.Label != "Deadline" {
			t.Errorf("label = %q, want the matrix's %q", got.Label, "Deadline")
		}
	})
}

// A derived field's formula names a record type and a field by KEY, through
// the env key services.eoAddChildAggregates publishes
// (children_<type>_<aggregate>_<field>). Nothing resolves those keys until a
// record is saved, and a formula naming a type or field that does not exist
// resolves to nothing for ever — reported as waiting on data, or, for a
// missing TYPE, as broken configuration on every save. Both roll-ups in the
// pilot are checked here against the seeded types.
// Mutation: rename the stage type's `weeks` field to `duration_weeks`, or
// change eo_execution_plan's recompute_on type to "execution_plan_stage".
func TestEoDerivedRollUpsNameASeededTypeAndField(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	numeric := map[string]bool{
		models.EOKindNumber: true, models.EOKindPercent: true,
	}
	var defs []models.EOFieldDef
	if err := db.Where("provenance = ?", models.EOProvenanceDerived).Find(&defs).Error; err != nil {
		t.Fatal(err)
	}
	if len(defs) < 2 {
		t.Fatalf("%d derived fields seeded; the pilot has at least the Directive's completion_rate and the plan's total_weeks", len(defs))
	}
	checked := 0
	for _, d := range defs {
		var deps []struct {
			Scope      string   `json:"scope"`
			Type       string   `json:"type"`
			Field      string   `json:"field"`
			Aggregates []string `json:"aggregates"`
		}
		if err := json.Unmarshal(d.RecomputeOn, &deps); err != nil {
			t.Errorf("%s: recompute_on is not a dependency list: %v", d.Key, err)
			continue
		}
		if len(deps) == 0 {
			t.Errorf("%s is derived and declares no dependency, so nothing ever recomputes it", d.Key)
			continue
		}
		for _, dep := range deps {
			if dep.Scope != "child" {
				continue
			}
			checked++
			t.Run(d.Key+" over "+dep.Type, func(t *testing.T) {
				child := eoRootType(t, db, dep.Type) // fatal if not seeded
				var target models.EOFieldDef
				if err := db.Where("record_type_id = ? AND key = ?", child.ID, dep.Field).
					Limit(1).Find(&target).Error; err != nil {
					t.Fatal(err)
				}
				if target.ID == uuid.Nil {
					t.Fatalf("record type %q declares no field %q, so the aggregate key is never published and the roll-up resolves to nothing for ever", dep.Type, dep.Field)
				}
				if !numeric[target.Kind] {
					t.Errorf("%s.%s is of kind %q; a child aggregate skips children with no NUMERIC value, so a non-numeric field aggregates to nothing", dep.Type, dep.Field, target.Kind)
				}
				for _, a := range dep.Aggregates {
					want := "children_" + dep.Type + "_" + a + "_" + dep.Field
					if !strings.Contains(d.Formula, want) {
						t.Errorf("the dependency publishes %q and the formula %q never names it", want, d.Formula)
					}
				}
			})
		}
	}
	if checked < 2 {
		t.Errorf("checked %d child roll-ups, want at least 2 (the Directive's completion_rate over assignments, the plan's total_weeks over stages)", checked)
	}
}

// A link field says which record type it points at. NOTHING IN THE ENGINE
// READS THAT CONFIG TODAY — the rule that actually permits or refuses a link
// is models.EOLinkType's TargetTypeIDs, which Task A4 seeds — so the only
// property that can be asserted here is the one that would be silently false:
// that the type a link field names exists. A dangling key is a target the
// designer offers and nothing can resolve.
// Mutation: change `from_stage`'s target_type_key to "eo_plan_stage".
func TestEoLinkFieldTargetsNameASeededType(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	seededTypes := map[string]bool{}
	var all []models.EORecordType
	if err := db.Find(&all).Error; err != nil {
		t.Fatal(err)
	}
	for _, ty := range all {
		seededTypes[ty.Key] = true
	}

	named := 0
	for _, typeKey := range eoPilotTypeKeys {
		for _, d := range eoOwnFieldDefs(t, db, typeKey) {
			if d.Kind != models.EOKindLink && d.Kind != models.EOKindMultiLink {
				continue
			}
			var cfg struct {
				TargetTypeKey string `json:"target_type_key"`
			}
			if err := json.Unmarshal(d.Config, &cfg); err != nil {
				t.Errorf("%s.%s: config: %v", typeKey, d.Key, err)
				continue
			}
			if cfg.TargetTypeKey == "" {
				continue // no claim made; nothing to check
			}
			named++
			if !seededTypes[cfg.TargetTypeKey] {
				t.Errorf("%s.%s points at record type %q, which is not seeded", typeKey, d.Key, cfg.TargetTypeKey)
			}
		}
	}
	// The link the eleventh type exists for must be one of them: without it
	// the whole reason for seeding Execution Plan Stage is unexpressed.
	t.Run("Assignment.from_stage points at the stage type", func(t *testing.T) {
		var cfg struct {
			TargetTypeKey string `json:"target_type_key"`
		}
		for _, d := range eoOwnFieldDefs(t, db, eoAssignmentTypeKey) {
			if d.Key == "from_stage" {
				if err := json.Unmarshal(d.Config, &cfg); err != nil {
					t.Fatal(err)
				}
			}
		}
		if cfg.TargetTypeKey != eoStageTypeKey {
			t.Errorf("from_stage points at %q, want %q — `Link (plan stage)` is why the stage is a record type at all", cfg.TargetTypeKey, eoStageTypeKey)
		}
	})
	if named < 4 {
		t.Errorf("%d link fields name a target type; the pilot's children seed four (supersedes twice, from_stage, parent_referral)", named)
	}
}

// The matrix's fifth Required value, "Yes at closure", is required_at_state —
// and the states are Task A5's. This pins what is true meanwhile: the fields
// are at_state with NO state ids, which requires them at no state at all, and
// the operation A5 calls fails closed.
// Mutation: make eoRequireAtClosureState return nil without updating, or drop
// one entry from eoAtClosureFields.
func TestEoAtClosureFieldsAwaitTheirState(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	matrix := eoReadFieldMatrix(t)
	// Read the set from the client's document rather than trusting the Go
	// list: a "Yes at closure" cell with no entry would otherwise be seeded
	// `always` and demand closing evidence at creation.
	wanted := map[string]bool{}
	for _, typeKey := range eoPilotTypeKeys {
		for _, r := range matrix[eoMatrixSections[typeKey]] {
			if r.Required == "Yes at closure" {
				wanted[typeKey+"."+r.Key] = true
			}
		}
	}
	if len(wanted) != 2 {
		t.Fatalf("the matrix yields %d \"Yes at closure\" cells across the pilot's types, want 2: %v", len(wanted), wanted)
	}
	declared := map[string]bool{}
	for _, f := range eoAtClosureFields {
		declared[f.TypeKey+"."+f.FieldKey] = true
	}
	for id := range wanted {
		if !declared[id] {
			t.Errorf("the matrix marks %s \"Yes at closure\" and eoAtClosureFields does not name it", id)
		}
	}
	for id := range declared {
		if !wanted[id] {
			t.Errorf("%s is named as at-closure and the matrix does not mark it so", id)
		}
	}

	for _, f := range eoAtClosureFields {
		def := eoEffectiveField(t, db, f.TypeKey, f.FieldKey)
		// TASK B2's ADDENDUM. Both of these hold Documenta file ids, so both
		// are relaxed to `never`: an at-closure demand no client can satisfy
		// does not protect the closure, it deadlocks it. THE STATE POINTER
		// BELOW IS DELIBERATELY STILL CHECKED — the step leaves
		// required_at_state_ids intact, the engine ignores it while the mode
		// is `never`, and it is what makes restoring the mode restore a
		// working control rather than an unarmed one.
		wantMode := models.EORequiredAtState
		if eoIsRelaxedUntilDocumenta(f.TypeKey, f.FieldKey) {
			wantMode = models.EORequiredNever
		}
		if def.RequiredMode != wantMode {
			t.Errorf("%s.%s required_mode = %q, want %q", f.TypeKey, f.FieldKey, def.RequiredMode, wantMode)
		}
		// TASK A5 HAS NOW POINTED THEM AT A STATE, and the empty list this
		// test used to assert is the thing that is gone. The original
		// paragraph read "already names states … Task A5 owns the workflow";
		// the state ids themselves are checked against the seeded terminal
		// state by TestEoClosureArtefactsNameTheirTypesTerminalState, which
		// is the test that would fail if the arming were removed.
		if s := strings.TrimSpace(string(def.RequiredAtStateIDs)); s == "" || s == "[]" || s == "null" {
			t.Errorf("%s.%s names NO state: ValidateRecordValues reads that as \"required at no state\", so the matrix's \"Yes at closure\" is enforced by nobody", f.TypeKey, f.FieldKey)
		}
	}

	closing := uuid.New()
	if err := eoRequireAtClosureState(db, eoAssignmentTypeKey, closing); err != nil {
		t.Fatalf("require the at-closure fields: %v", err)
	}
	def := eoEffectiveField(t, db, eoAssignmentTypeKey, "evidence")
	var ids []uuid.UUID
	if err := json.Unmarshal(def.RequiredAtStateIDs, &ids); err != nil {
		t.Fatalf("required_at_state_ids: %v", err)
	}
	if len(ids) != 1 || ids[0] != closing {
		t.Errorf("required_at_state_ids = %v, want [%v]", ids, closing)
	}
	// The other type is untouched: an operation that updated every at-closure
	// field everywhere would be a different, and wrong, operation. The Task's
	// evidence still names the state the SEED gave it, which is its own
	// workflow's terminal state and not the id above.
	other := eoEffectiveField(t, db, eoTaskTypeKey, "evidence")
	var otherIDs []uuid.UUID
	if err := json.Unmarshal(other.RequiredAtStateIDs, &otherIDs); err != nil {
		t.Fatalf("the Task's required_at_state_ids: %v", err)
	}
	if len(otherIDs) != 1 || otherIDs[0] == closing {
		t.Errorf("the Task's evidence names %v; a call naming the Assignment must not have touched it", otherIDs)
	}

	// Fails closed on both halves.
	if err := eoRequireAtClosureState(db, eoAssignmentTypeKey, uuid.Nil); err == nil {
		t.Error("the nil state was accepted; it would require nothing and report success")
	}
	// A type with NO closure artefact at all: neither a matrix "Yes at
	// closure" cell nor a "Yes" row Task A5 resolved as one.
	if err := eoRequireAtClosureState(db, eoObjectionTypeKey, closing); err == nil {
		t.Error("a type with no closure artefact was accepted")
	}
}

// eoSeedPilotTypes' own refusals, which eoPilotFieldDefs cannot make because
// they are about the TYPE rather than its fields. Each is a configuration that
// would be accepted silently and then misnumber every record of the type —
// and models.ValidateTypeEdit refuses to change a prefix or pattern once
// records exist, which after that takes a §7.14 confirmed destructive edit
// with a stated backfill rule. (A reference MODE is not one of §7.14's
// guarded attributes at all, so nothing refuses changing it.)
// Mutation: delete either guard at the top of eoSeedPilotTypes.
func TestEoSeedRefusesAnIncoherentTypeSpec(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)
	base, baseFields, err := eoPilotBase(db)
	if err != nil {
		t.Fatalf("read the base type: %v", err)
	}

	tests := []struct {
		name string
		spec eoPilotTypeSpec
	}{
		{"no reference mode at all", eoPilotTypeSpec{
			key: "eo_probe_no_mode", name: "Probe", prefix: "PRB", pattern: "PRB-YYYY-NNN"}},
		{"an unknown reference mode", eoPilotTypeSpec{
			key: "eo_probe_bad_mode", name: "Probe", mode: "inherited",
			prefix: "PRB", pattern: "PRB-YYYY-NNN"}},
		{"a parent-derived type carrying a prefix", eoPilotTypeSpec{
			key: "eo_probe_prefixed_child", name: "Probe", mode: models.EOReferenceModeParent,
			prefix: "PRB", pattern: "EO-DIR-YYYY-NNN-Z#"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := db.Transaction(func(tx *gorm.DB) error {
				return eoSeedPilotTypes(tx, []eoPilotTypeSpec{tc.spec}, base, baseFields)
			})
			if err == nil {
				t.Error("accepted")
			}
			var n int64
			if err := db.Model(&models.EORecordType{}).Where("key = ?", tc.spec.key).Count(&n).Error; err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Error("the record type was created although the spec was refused")
			}
		})
	}

	// The control: a coherent spec of each mode IS accepted, so the guards
	// are not refusing everything. Rolled back, so it leaves nothing behind.
	for _, spec := range []eoPilotTypeSpec{
		{key: "eo_probe_own", name: "Probe", mode: models.EOReferenceModeOwn,
			prefix: "PRB", pattern: "PRB-YYYY-NNN"},
		{key: "eo_probe_child", name: "Probe", mode: models.EOReferenceModeParent,
			prefix: "", pattern: "EO-DIR-YYYY-NNN-Z#"},
	} {
		t.Run("accepts "+spec.key, func(t *testing.T) {
			errSentinel := fmt.Errorf("rollback")
			err := db.Transaction(func(tx *gorm.DB) error {
				if err := eoSeedPilotTypes(tx, []eoPilotTypeSpec{spec}, base, baseFields); err != nil {
					return err
				}
				return errSentinel
			})
			if err != errSentinel {
				t.Errorf("a coherent spec was refused: %v", err)
			}
		})
	}
}

// ── Task A3 fix round 1 ─────────────────────────────────────────────────────

// A type-specific rename in eoMatrixKeys is keyed by the MATRIX SPELLING
// alone, so it applies to every type that uses that word. Each of the five
// occurs exactly once in the client's document today — but `deadline`,
// `visibility` and `standing` are generic words, and a later CFF type meaning
// something else by one of them would be folded into the engine key of this
// one, silently, with the field it actually wanted never seeded.
//
// This is the cheap half of the fix: prove the premise the key map rests on.
// The expensive half — keying renames by (type, matrix key) — is not worth its
// weight while the premise holds, and this test is what tells the next task
// when it stops holding.
// Mutation: add `"standing": {"state_id", true, true}` to eoMatrixKeys
// (`standing` occurs in Decision Record and would then need a type scope), or
// re-spell any OTHER matrix section's row as one of the renamed keys.
func TestEoTypeSpecificRenamesAreUnambiguousAcrossTheMatrix(t *testing.T) {
	m := eoReadFieldMatrix(t)

	// The renames are the entries whose engine key differs from the matrix
	// key; the inherited block's identical entries are not renames.
	renames := map[string]string{}
	for matrixKey, e := range eoMatrixKeys {
		if e.engineKey != matrixKey {
			renames[matrixKey] = e.engineKey
		}
	}
	// Fourteen: Task A1's six Global Constraints renames (classification,
	// owner_department, assigned_user, closed_on, created_by, parent_ticket),
	// the two engine-managed keys whose engine name differs (ticket_type,
	// state), and the six TYPE-SPECIFIC ones — A2's sender_deadline and A3's
	// deadline, decision_number, out_ref, source_study and visibility. The
	// count is the guard on this test: a rename added without being listed
	// below would otherwise be checked by nothing.
	if len(renames) != 14 {
		t.Fatalf("eoMatrixKeys holds %d renames, want 14: %v", len(renames), renames)
	}

	// Where each matrix key appears, by section.
	where := map[string][]string{}
	for section, rows := range m {
		if section == eoMatrixInheritedSection {
			continue // the inherited block is every type's, by definition
		}
		for _, r := range rows {
			where[r.Key] = append(where[r.Key], section)
		}
	}

	// Only the TYPE-SPECIFIC renames are at risk: the inherited six are
	// declared once in the inherited block and mean the same thing on every
	// type by construction.
	typeSpecific := []string{"sender_deadline", "deadline", "decision_number", "out_ref", "source_study", "visibility"}
	for _, key := range typeSpecific {
		t.Run(key, func(t *testing.T) {
			if _, ok := renames[key]; !ok {
				t.Fatalf("%q is not a rename in eoMatrixKeys; this list and the map disagree", key)
			}
			sections := where[key]
			sort.Strings(sections)
			if len(sections) == 0 {
				t.Fatalf("no section of the matrix has a %q row, so the rename applies to nothing", key)
			}
			if len(sections) > 1 {
				t.Errorf("%q appears in %v; the rename is keyed by matrix spelling alone, so every one of those types is folded onto the engine key %q — key the rename by (type, matrix key) before seeding any of them",
					key, sections, renames[key])
			}
		})
	}
}

// eoReferralVisibilityEnv is the environment services.eoOwnEnv builds for a
// derived field: the record's own values. The formula on `visible_to` must
// RESOLVE against it — eoformula treats an identifier absent from the env as a
// hard error, so a formula naming a field the type does not have would fail at
// the first referral rather than here.
//
// It cannot call RecomputeDerived: package services imports package database,
// so this package cannot import services. The evaluator is the same one
// (eoformula.New(0)), which is what RecomputeDerived itself uses.
func eoReferralVisibilityEnv(t *testing.T, db *gorm.DB, body string) map[string]any {
	t.Helper()
	env := map[string]any{}
	for _, d := range eoOwnFieldDefs(t, db, eoBaseTypeKey) {
		env[d.Key] = ""
	}
	for _, d := range eoOwnFieldDefs(t, db, eoReferralTypeKey) {
		if d.Provenance == models.EOProvenanceDerived {
			continue // eoOwnEnv deletes derived keys from the env
		}
		env[d.Key] = ""
	}
	env["referred_body"] = body
	return env
}

// THE CRITICAL FIX. §7.11 row-level visibility is live in the repository and
// reads ONE values key, repository.eoVisibleToKey = "visible_to". A Referral
// seeded under the matrix's own `visibility` would be a confidentiality
// control that looks configured and enforces nothing.
//
// This proves the control end to end against the REAL clause — the repository
// is importable from here, unlike package services — rather than proving the
// key is spelled right. It also proves the clause can refuse, so a passing
// first half is not a filter that lets everything through.
// Mutation: change the eoMatrixKeys entry to `"visibility": {"visibility",
// false, false}`, or drop it altogether.
func TestEoReferralVisibilityIsEnforcedByTheLiveClause(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	def := eoEffectiveField(t, db, eoReferralTypeKey, "visibility")
	if def.Key != "visible_to" {
		t.Fatalf("the field is keyed %q; repository.applyEORecordVisibility reads %q and nothing else", def.Key, "visible_to")
	}
	if def.Provenance != models.EOProvenanceDerived || strings.TrimSpace(def.Formula) == "" {
		t.Fatalf("provenance %q / formula %q: nothing writes the key, so every referral is unrestricted",
			def.Provenance, def.Formula)
	}

	// THE SINGLE FACT THE WHOLE CONTROL RESTS ON, asserted where the control
	// is tested rather than only in the matrix sweep, which lives in another
	// test that never mentions visibility.
	//
	// services.eoEnvValue DROPS a blank string from the derive environment,
	// so a Referral saved with an empty `referred_body` does not evaluate the
	// formula to "" — it raises eoformula.ErrUnresolvedReference, the derived
	// key is left out of the result, and eoApplyDerived LOGS AND CARRIES ON
	// (it never blocks a save). The record is then stored with no visible_to
	// at all, and repository.applyEORecordVisibility treats an absent key as
	// UNRESTRICTED. The derivation therefore FAILS OPEN, and
	// `referred_body` being required at creation is the only thing standing
	// between a blank body and a referral the whole organisation can read.
	//
	// Mutation: change `referred_body`'s `required:` in eoPilotChildTypes
	// from models.EORequiredAlways to models.EORequiredNever.
	if src := eoEffectiveField(t, db, eoReferralTypeKey, "referred_body"); src.RequiredMode != models.EORequiredAlways {
		t.Errorf("referred_body is %q, want %q: the visibility key is derived from it and a blank body derives NOTHING, which the repository reads as unrestricted",
			src.RequiredMode, models.EORequiredAlways)
	}

	// The formula resolves against the type's own values.
	referred, other := uuid.New(), uuid.New()
	got, err := eoformula.New(0).Eval(def.Formula, eoReferralVisibilityEnv(t, db, referred.String()))
	if err != nil {
		t.Fatalf("formula %q over a referral's own values: %v", def.Formula, err)
	}
	if got != referred.String() {
		t.Errorf("the formula computed %v, want the referred body %v", got, referred)
	}

	// And the live clause restricts a record carrying what it computes. The
	// records are written directly: the save pipeline lives in package
	// services, which cannot be imported here, and the question is what the
	// REPOSITORY does with a stored value.
	ty := eoRootType(t, db, eoReferralTypeKey)
	office := uuid.New()
	mk := func(ref string, values string) {
		t.Helper()
		rec := models.EORecord{
			ID: uuid.New(), RecordTypeID: ty.ID, Reference: ref,
			SubjectAr: "x", Priority: "normal", Sensitivity: "internal", Source: "correspondence",
			CreatedByID: office, Values: datatypes.JSON(values),
		}
		if err := db.Create(&rec).Error; err != nil {
			t.Fatalf("insert %s: %v", ref, err)
		}
	}
	mk("IN-2026-0001-R1", `{"`+def.Key+`":"`+referred.String()+`"}`)
	mk("IN-2026-0001-R2", `{"`+def.Key+`":"`+other.String()+`"}`)

	repo := repository.NewEORecordRepository(db)
	list := func(f models.EORecordFilter) []string {
		t.Helper()
		f.RecordTypeID = []string{ty.ID.String()}
		// §7.11's per-type gate is a SEPARATE clause and this test is about
		// the row-level one: the caller holds the type's view code
		// throughout, or the register would answer empty for every row below
		// and each assertion would pass for the wrong reason.
		f.PermissionCodes = []string{models.EOTypePermissionCode(ty.Key, models.EOTypePermissionView)}
		f.SortBy, f.SortDir = "reference", "asc"
		recs, _, err := repo.List(context.Background(), &f)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		out := make([]string, 0, len(recs))
		for _, r := range recs {
			out = append(out, r.Reference)
		}
		return out
	}

	tests := []struct {
		name   string
		filter models.EORecordFilter
		want   []string
	}{
		{"the referred body sees its own referral and nothing more",
			models.EORecordFilter{UserID: uuid.New(), VisibleToDepartmentIDs: []uuid.UUID{referred}},
			[]string{"IN-2026-0001-R1"}},
		{"another body sees neither of them",
			models.EORecordFilter{UserID: uuid.New(), VisibleToDepartmentIDs: []uuid.UUID{uuid.New()}},
			nil},
		{"the Office that issued them sees both, to consolidate",
			models.EORecordFilter{UserID: office},
			[]string{"IN-2026-0001-R1", "IN-2026-0001-R2"}},
		{"an admin sees both",
			models.EORecordFilter{IsAdmin: true},
			[]string{"IN-2026-0001-R1", "IN-2026-0001-R2"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := list(tc.filter)
			if len(got) != len(tc.want) {
				t.Fatalf("saw %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("saw %v, want %v", got, tc.want)
					break
				}
			}
		})
	}

	// The control that makes the rows above mean something: the SAME records
	// stored under the matrix's spelling are visible to everyone, because the
	// clause reads a key that is then not there. This is the bug the fix
	// closes, reproduced.
	//
	// IT IS AN ATTRIBUTION, NOT A REVERT DETECTOR. Reverting the production
	// fix trips the t.Fatalf at the top of this function and the subtest
	// below never runs; what it proves is that the passing rows above are
	// caused by the KEY NAME and not by some other property of the records.
	t.Run("stored under the matrix spelling, nothing is restricted", func(t *testing.T) {
		mk("IN-2026-0002-R1", `{"visibility":"`+referred.String()+`"}`)
		got := list(models.EORecordFilter{UserID: uuid.New(), VisibleToDepartmentIDs: []uuid.UUID{uuid.New()}})
		found := false
		for _, r := range got {
			if r == "IN-2026-0002-R1" {
				found = true
			}
		}
		if !found {
			t.Error("a record keyed `visibility` was restricted, so the engine does read that key after all and the rename is wrong")
		}
	})
}

// ── Task A4: the link types and the rules they carry ────────────────────────

// eoSeededLinkTypes reads every link type in the database, by key. Fixture.
func eoSeededLinkTypes(t *testing.T, db *gorm.DB) map[string]models.EOLinkType {
	t.Helper()
	var rows []models.EOLinkType
	if err := db.Order("key").Find(&rows).Error; err != nil {
		t.Fatalf("read the link types: %v", err)
	}
	out := make(map[string]models.EOLinkType, len(rows))
	for _, lt := range rows {
		out[lt.Key] = lt
	}
	return out
}

// eoLinkTypeIDs decodes a link type's jsonb endpoint list. It accepts both
// spellings services.eoTypeAllowed accepts — a bare JSON string and an array —
// so a test reading it cannot be fooled by the one the seed does not use.
func eoLinkTypeIDs(t *testing.T, what string, raw []byte) []string {
	t.Helper()
	if len(raw) == 0 {
		return nil
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return []string{single}
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatalf("%s is neither a JSON string nor an array of strings: %s", what, raw)
	}
	return list
}

// eoReadMatrixProse is the paragraph under each `### Type` heading of the
// client's matrix — the sentences that state the GOVERNANCE RULES ("Blocks the
// parent from closing", "Blocks the raising step, not closure", "Deadline
// derived from the parent, not typed", "Closes the parent"). The field tables
// say nothing about any of them, so the prose is the authority for everything
// Task A4 seeds.
//
// It carries the same guard as the table parser: every section the pilot reads
// must yield a paragraph, and every paragraph must carry the "Reference `...`"
// the matrix ends each one with. A prose reader that quietly matched nothing
// would make every expectation below vacuously true, which is the failure this
// programme keeps shipping.
func eoReadMatrixProse(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(eoFieldMatrixPath)
	if err != nil {
		t.Fatalf("read the client field matrix: %v", err)
	}
	out := map[string]string{}
	section := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if h := strings.TrimLeft(line, "#"); len(line)-len(h) == 3 {
			section = strings.TrimSpace(h)
			continue
		}
		if section == "" || line == "" || strings.HasPrefix(line, "|") {
			continue
		}
		if _, ok := out[section]; !ok {
			out[section] = line
		}
	}
	for _, section := range eoMatrixSections {
		prose, ok := out[section]
		if !ok || !strings.Contains(prose, "Reference `") {
			t.Fatalf("parsed no type paragraph under %q (got %q): the matrix's layout has changed and every rule read from its prose is now proving nothing", section, prose)
		}
	}
	return out
}

// eoLinkProseRules is what the client's own paragraph says about one sub type,
// read from the matrix rather than written out here.
type eoLinkProseRules struct {
	blocksClosure   bool
	blocksRaiseStep bool
	derivesDeadline bool
	closesTheParent bool
}

func eoLinkRulesFromProse(prose string) eoLinkProseRules {
	low := strings.ToLower(prose)
	return eoLinkProseRules{
		blocksClosure: strings.Contains(low, "blocks the parent from closing") ||
			strings.Contains(low, "blocks its parent from closing"),
		blocksRaiseStep: strings.Contains(low, "blocks the raising step, not closure"),
		derivesDeadline: strings.Contains(low, "deadline derived from the parent"),
		closesTheParent: strings.Contains(low, "closes the parent"),
	}
}

// THE RULE TEST. Every seeded link type is compared with the CLIENT'S OWN
// SENTENCE for its type, so a rule cannot be seeded that the matrix does not
// state and a stated rule cannot be left off.
//
// Mutation: set `blocksClosure: false` on eoReferralLinkKey's spec (the matrix
// says "Blocks the parent from closing"), or give eoObjectionLinkKey
// `blocksClosure: true` (the matrix says "not closure").
func TestEoLinkRulesMatchTheMatrixProse(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)
	seeded := eoSeededLinkTypes(t, db)
	prose := eoReadMatrixProse(t)

	for _, spec := range eoPilotLinkTypes {
		t.Run(spec.childTypeKey, func(t *testing.T) {
			lt, ok := seeded[spec.key]
			if !ok {
				t.Fatalf("link type %q was not seeded", spec.key)
			}
			// The stage type has no section in the client's document, so its
			// link type must carry NO rule: an unstated rule is invented
			// client policy. eoMatrixSections has no entry for it, and no
			// section is named "", so want is the zero value.
			want := eoLinkRulesFromProse(prose[eoMatrixSections[spec.childTypeKey]])

			if lt.BlocksParentClosure != want.blocksClosure {
				t.Errorf("blocks_parent_closure = %v, the matrix says %v: %q",
					lt.BlocksParentClosure, want.blocksClosure, prose[eoMatrixSections[spec.childTypeKey]])
			}
			if named := lt.BlocksParentTransitionKey != ""; named != want.blocksRaiseStep {
				t.Errorf("blocks_parent_transition_key = %q, the matrix names a blocked step: %v",
					lt.BlocksParentTransitionKey, want.blocksRaiseStep)
			}
			if lt.DeriveDeadlineFromParent != want.derivesDeadline {
				t.Errorf("derive_deadline_from_parent = %v, the matrix says %v",
					lt.DeriveDeadlineFromParent, want.derivesDeadline)
			}
			// "Closes the parent" is the opposite of blocking it: a child
			// that closes its parent cannot also hold it open.
			if want.closesTheParent && (lt.BlocksParentClosure || lt.BlocksParentTransitionKey != "") {
				t.Errorf("the matrix says this type CLOSES its parent, but the link type blocks it (closure %v, transition %q)",
					lt.BlocksParentClosure, lt.BlocksParentTransitionKey)
			}
		})
	}
}

// The two block semantics are DISTINCT rules (services.BlockingChildren asks
// one question or the other, never both), so the closure blockers must leave
// the transition key empty and the Objection must leave closure alone.
//
// This is the same statement as the prose test above made against the engine's
// two fields directly, written out so that a change to either is caught by a
// test naming the type rather than only by the parser.
//
// Mutation: in eoPilotLinkTypes, give eoObjectionLinkKey `blocksClosure: true`
// alongside its transition code — the seed refuses it, which is the point.
func TestEoBlockingLinkTypesCarryTheRightSemantic(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)
	seeded := eoSeededLinkTypes(t, db)

	tests := []struct {
		name       string
		key        string
		closure    bool
		transition string
	}{
		{"an execution plan blocks its directive from closing", eoExecutionPlanLinkKey, true, ""},
		{"an assignment blocks its parent from closing", eoAssignmentLinkKey, true, ""},
		{"a referral blocks its letter from closing", eoReferralLinkKey, true, ""},
		{"a task always blocks its parent from closing", eoTaskLinkKey, true, ""},
		{"an objection blocks the raising step, not closure", eoObjectionLinkKey, false, eoDraftRaiseToHETransitionCode},
		{"a decision record closes its parent, so it blocks nothing", eoDecisionLinkKey, false, ""},
		{"an outgoing reply closes its parent, so it blocks nothing", eoOutgoingLinkKey, false, ""},
		{"a plan stage carries no rule the client's document states", eoStageLinkKey, false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lt, ok := seeded[tc.key]
			if !ok {
				t.Fatalf("link type %q was not seeded", tc.key)
			}
			if lt.BlocksParentClosure != tc.closure {
				t.Errorf("blocks_parent_closure = %v, want %v", lt.BlocksParentClosure, tc.closure)
			}
			if lt.BlocksParentTransitionKey != tc.transition {
				t.Errorf("blocks_parent_transition_key = %q, want %q", lt.BlocksParentTransitionKey, tc.transition)
			}
		})
	}
}

// Only the Referral derives a deadline, and it derives it with an offset: a
// derivation flag with a zero offset would copy the letter's own deadline onto
// every referral, which is not "counted back" from anything.
//
// That a Referral CREATED UNDER A LETTER actually receives the derived date is
// proved where the save pipeline lives, in
// services.TestEoPilotReferralUnderALetterInheritsItsDeadline: package
// database cannot import services (services imports database).
//
// Mutation: set `deriveDeadline: false` on eoReferralLinkKey's spec.
func TestEoReferralLinkDerivesItsDeadlineFromTheLetter(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)
	seeded := eoSeededLinkTypes(t, db)

	for key, lt := range seeded {
		derives := key == eoReferralLinkKey
		if lt.DeriveDeadlineFromParent != derives {
			t.Errorf("link type %q derive_deadline_from_parent = %v, want %v", key, lt.DeriveDeadlineFromParent, derives)
		}
		if !derives && lt.DeadlineOffsetDays != 0 {
			t.Errorf("link type %q carries an offset of %d days that nothing reads: services.DeriveDeadlineFromParent returns before it unless the flag is set",
				key, lt.DeadlineOffsetDays)
		}
	}
	if got := seeded[eoReferralLinkKey].DeadlineOffsetDays; got != eoReferralDeadlineOffsetDays || got <= 0 {
		t.Errorf("the referral deadline offset is %d days, want %d: a zero or negative offset is not a deadline counted BACK from the letter's",
			got, eoReferralDeadlineOffsetDays)
	}
}

// Mutation: write `cardinality: "one"` instead of models.EOCardinalityOne on
// any spec in eoPilotLinkTypes.
func TestEoLinkCardinalityIsNamed(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	for key, lt := range eoSeededLinkTypes(t, db) {
		if key == eoSmokeLinkTypeKey {
			continue // Plan 1's, removed by the pilot seed; not under test here
		}
		switch lt.Cardinality {
		case models.EOCardinalityOne, models.EOCardinalityMany:
		default:
			t.Errorf("link type %q has cardinality %q, which is neither %q nor %q: the spec enumerates no values, so a free-form spelling is one each consumer invents differently",
				key, lt.Cardinality, models.EOCardinalityOne, models.EOCardinalityMany)
		}
	}

	// The value test above cannot tell a named constant from a literal that
	// happens to spell it the same way, so the source is read: every
	// `cardinality:` in eoPilotLinkTypes must be a models.EOCardinality*
	// selector.
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "seed_eo_pilot.go", nil, 0)
	if err != nil {
		t.Fatalf("parse seed_eo_pilot.go: %v", err)
	}
	seen := 0
	ast.Inspect(file, func(n ast.Node) bool {
		kv, ok := n.(*ast.KeyValueExpr)
		if !ok {
			return true
		}
		if id, ok := kv.Key.(*ast.Ident); !ok || id.Name != "cardinality" {
			return true
		}
		seen++
		sel, ok := kv.Value.(*ast.SelectorExpr)
		if !ok || !strings.HasPrefix(sel.Sel.Name, "EOCardinality") {
			t.Errorf("a cardinality is written as %T rather than a models.EOCardinality* constant", kv.Value)
		}
		return true
	})
	if seen != len(eoPilotLinkTypes) {
		t.Errorf("read %d cardinality assignments from the source, %d link types are seeded", seen, len(eoPilotLinkTypes))
	}
}

// Addendum 1, the defect this task is most likely to ship: an endpoint list
// that permits NOTHING while looking correct. services.eoTypeAllowed returns
// false for an empty list and the column's DB default is '[]'::jsonb, so a
// link type seeded without endpoints refuses every link of its type.
//
// Mutation: drop `parentTypeKeys` from any spec in eoPilotLinkTypes.
func TestEoLinkEndpointsNameSeededTypes(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)
	seeded := eoSeededLinkTypes(t, db)

	byID := map[string]string{}
	for _, key := range eoPilotTypeKeys {
		byID[eoRootType(t, db, key).ID.String()] = key
	}

	for _, spec := range eoPilotLinkTypes {
		t.Run(spec.key, func(t *testing.T) {
			lt, ok := seeded[spec.key]
			if !ok {
				t.Fatalf("link type %q was not seeded", spec.key)
			}
			sources := eoLinkTypeIDs(t, "source_type_ids", lt.SourceTypeIDs)
			if len(sources) != 1 || byID[sources[0]] != spec.childTypeKey {
				t.Errorf("source types = %v, want exactly the id of %q", sources, spec.childTypeKey)
			}
			targets := eoLinkTypeIDs(t, "target_type_ids", lt.TargetTypeIDs)
			if len(targets) == 0 {
				t.Fatalf("target types are empty: services.eoTypeAllowed refuses EVERY target for an empty list, so this link type permits nothing")
			}
			for _, got := range targets {
				if got == eoAnyTargetType {
					continue
				}
				if _, ok := byID[got]; !ok {
					t.Errorf("target type %q is not a seeded pilot record type", got)
				}
			}
		})
	}
}

// A parent-derived type with no link type is UNCREATABLE: it must be created
// with a parent (services.RenderReference refuses to number it otherwise), and
// eoRecordService.Create refuses a parent given with no link type key. So the
// set of link types is not a matter of taste — one per sub type is what makes
// the eight seeded types usable at all.
//
// TASK A7 ALSO RESTS ON THE "EXACTLY ONE" HALF OF THIS, so do not weaken it to
// "at least one". Plan 1 deferred models.EOLinkType's DefaultParentLinkTypeKey
// because a client chooses ParentLinkTypeKey on the create request and could
// name a permitted-but-ruleless link type instead of the one carrying the
// derived deadline or the blocks_parent_closure. A7 answered that the pilot
// does not need the server-side default, and THIS TEST IS THE WHOLE REASON:
// with one link type per child type there is no second key to name, and
// services.ValidateLink refuses every other. A second link type on any pilot
// child type makes that default owed.
//
// Mutation: delete any entry from eoPilotLinkTypes.
func TestEoEveryPilotChildTypeHasOneParentLinkType(t *testing.T) {
	bySource := map[string][]string{}
	for _, spec := range eoPilotLinkTypes {
		bySource[spec.childTypeKey] = append(bySource[spec.childTypeKey], spec.key)
	}
	for _, key := range eoPilotChildTypeKeys() {
		got := bySource[key]
		if len(got) != 1 {
			t.Errorf("record type %q has %d parent link types (%v), want exactly 1: with none it cannot be created with a parent at all, and with two the create request chooses which rules apply",
				key, len(got), got)
		}
	}
	if len(eoPilotLinkTypes) != len(eoPilotChildTypeKeys()) {
		t.Errorf("%d link types for %d sub types", len(eoPilotLinkTypes), len(eoPilotChildTypeKeys()))
	}
}

// The polymorphic target is the literal string services.eoAnyType carries, and
// that constant is UNEXPORTED in a package this one cannot import — so the
// seed repeats the literal and this test is the only thing tying the two
// together. Renaming the sentinel on either side turns the universal Task's
// link type into one that permits nothing.
//
// Mutation: change eoAnyTargetType to "ANY" (or to "*").
func TestEoAnyTargetSentinelMatchesTheEngineConstant(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "../services/eo_links.go", nil, 0)
	if err != nil {
		t.Fatalf("parse services/eo_links.go: %v", err)
	}
	got := ""
	ast.Inspect(file, func(n ast.Node) bool {
		vs, ok := n.(*ast.ValueSpec)
		if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "eoAnyType" || len(vs.Values) != 1 {
			return true
		}
		if lit, ok := vs.Values[0].(*ast.BasicLit); ok {
			got = strings.Trim(lit.Value, `"`)
		}
		return true
	})
	if got == "" {
		t.Fatalf("services.eoAnyType was not found; the sentinel the Task link type is seeded with is tied to nothing")
	}
	if got != eoAnyTargetType {
		t.Errorf("services.eoAnyType = %q, the seed writes %q: the universal Task's link type permits nothing", got, eoAnyTargetType)
	}
}

// The seed's own refusals, which are what stop an inert link type reaching the
// database. No DB: eoLinkTypeRow is pure.
//
// Mutation: delete any one of the guards in eoLinkTypeRow.
func TestEoLinkTypeRowRefusesAnIncoherentSpec(t *testing.T) {
	ids := map[string]uuid.UUID{}
	for _, key := range eoPilotTypeKeys {
		ids[key] = uuid.New()
	}
	ok := eoLinkTypeSpec{
		key: "eo_test_parent", label: "Parent", childTypeKey: eoTaskTypeKey,
		parentTypeKeys: []string{eoDirectiveTypeKey}, cardinality: models.EOCardinalityOne,
	}
	with := func(f func(*eoLinkTypeSpec)) eoLinkTypeSpec {
		s := ok
		s.parentTypeKeys = append([]string{}, ok.parentTypeKeys...)
		f(&s)
		return s
	}

	tests := []struct {
		name string
		spec eoLinkTypeSpec
		want string
	}{
		{"a free-form cardinality", with(func(s *eoLinkTypeSpec) { s.cardinality = "1" }), "cardinality"},
		{"no cardinality at all", with(func(s *eoLinkTypeSpec) { s.cardinality = "" }), "cardinality"},
		{"a child type that is not seeded", with(func(s *eoLinkTypeSpec) { s.childTypeKey = "eo_nonesuch" }), "eo_nonesuch"},
		{"a parent type that is not seeded", with(func(s *eoLinkTypeSpec) { s.parentTypeKeys = []string{"eo_nonesuch"} }), "eo_nonesuch"},
		{"no parent types, which permits nothing", with(func(s *eoLinkTypeSpec) { s.parentTypeKeys = nil }), "permits nothing"},
		{"a polymorphic SOURCE, which no matrix row asks for", with(func(s *eoLinkTypeSpec) { s.childTypeKey = eoAnyTargetType }), "source"},
		{"both block rules at once", with(func(s *eoLinkTypeSpec) {
			s.blocksClosure = true
			s.blocksTransitionCode = eoDraftRaiseToHETransitionCode
		}), "one or the other"},
		{"an offset nothing reads", with(func(s *eoLinkTypeSpec) { s.deadlineOffsetDays = 2 }), "offset"},
		{"a derivation with no offset", with(func(s *eoLinkTypeSpec) { s.deriveDeadline = true }), "offset"},
		// The shape the derive/offset equality lets through, and it INVERTS
		// the rule instead of breaking it: DeriveDeadlineFromParent subtracts
		// the offset, so a negative one makes the child due LATER than the
		// parent it was counted back from.
		{"a derivation that moves the deadline later", with(func(s *eoLinkTypeSpec) {
			s.deriveDeadline = true
			s.deadlineOffsetDays = -3
		}), "counted back"},
		{"an empty key", with(func(s *eoLinkTypeSpec) { s.key = "" }), "key"},
		{"no label", with(func(s *eoLinkTypeSpec) { s.label = "" }), "label"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := eoLinkTypeRow(tc.spec, ids)
			if err == nil {
				t.Fatalf("the seed accepted %+v", tc.spec)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}

	if _, err := eoLinkTypeRow(ok, ids); err != nil {
		t.Errorf("a coherent spec was refused: %v", err)
	}
}

// Every link type's rule is a reading of one sentence of the client's
// document, and the registry is where that reading is written down —
// including for the two types whose rule is "none" and the one type the
// document does not mention at all. Without it a missing rule and a
// deliberately absent one look identical.
//
// Mutation: delete any entry from eoLinkRuleDecisions, or change an entry's
// MatrixProse to a sentence the matrix does not contain.
func TestEoLinkRuleDecisionsCoverEverySeededLinkType(t *testing.T) {
	prose := eoReadMatrixProse(t)
	byKey := map[string]eoLinkRuleDecision{}
	for _, d := range eoLinkRuleDecisions {
		if _, dup := byKey[d.LinkKey]; dup {
			t.Errorf("two decisions for link type %q", d.LinkKey)
		}
		byKey[d.LinkKey] = d
	}
	for _, spec := range eoPilotLinkTypes {
		t.Run(spec.key, func(t *testing.T) {
			d, ok := byKey[spec.key]
			if !ok {
				t.Fatalf("link type %q carries no stated decision", spec.key)
			}
			if d.ChildTypeKey != spec.childTypeKey {
				t.Errorf("the decision names child type %q, the spec seeds %q", d.ChildTypeKey, spec.childTypeKey)
			}
			if strings.TrimSpace(d.Mechanism) == "" || strings.TrimSpace(d.Why) == "" {
				t.Errorf("the decision names no mechanism (%q) or no reason (%q)", d.Mechanism, d.Why)
			}
			section := eoMatrixSections[spec.childTypeKey]
			if section == "" {
				// The stage type: the matrix has no section for it, so the
				// decision must say so rather than quote a sentence.
				if d.MatrixProse != "" {
					t.Errorf("the decision quotes %q, but the client's matrix has no section for %q", d.MatrixProse, spec.childTypeKey)
				}
				return
			}
			if d.MatrixProse == "" {
				t.Fatalf("the decision quotes nothing from the matrix's %q paragraph", section)
			}
			if !strings.Contains(prose[section], d.MatrixProse) {
				t.Errorf("the decision quotes %q, which is not in the matrix's %q paragraph: %q", d.MatrixProse, section, prose[section])
			}
		})
	}
	if len(eoLinkRuleDecisions) != len(eoPilotLinkTypes) {
		t.Errorf("%d decisions for %d link types", len(eoLinkRuleDecisions), len(eoPilotLinkTypes))
	}
}

// The labels of the seeded link types await the client's Arabic exactly as the
// field labels do — and there is nowhere to record it: eo_link_types has no
// ar_untranslated column, so eoUntranslatedFields cannot see them. This test
// is the only place the obligation is visible, so it states the rule it can:
// no Arabic is invented.
//
// Mutation: write a real Arabic label into eoUntranslatedLinkLabel.
func TestEoLinkLabelsAwaitArabicSignOff(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)
	for _, spec := range eoPilotLinkTypes {
		lt := eoSeededLinkTypes(t, db)[spec.key]
		if lt.Label == "" || lt.LabelAr == "" {
			t.Errorf("link type %q has an empty label (%q/%q); it would render as a blank row", spec.key, lt.Label, lt.LabelAr)
		}
		if lt.LabelAr != lt.Label {
			t.Errorf("link type %q carries Arabic (%q) the client has not signed off; the English stands in until it does", spec.key, lt.LabelAr)
		}
	}
}

// THE TEST THAT FORCES TASK A5'S HAND.
//
// A link type that names a blocked transition is only a rule while some
// workflow HAS a transition under that code: services.checkBlockingChildren
// compares BlocksParentTransitionKey with transition.Code by string equality,
// so a transition seeded under any other code leaves the rule matching nothing
// — an objection raised, recorded, and silently ignored by the step it exists
// to hold up. Nothing in the engine reports that, because an unmatched key is
// not an error, just a comparison that never comes out true. Four documents
// telling A5 to use the constant are four documents A5 need not open; this is
// the one that fails.
//
// IT IS VACUOUS TODAY, DELIBERATELY: the pilot seed binds no workflow to any
// of its types (Task A5 seeds the workflows), so the inner loop runs zero
// times and A4 does not ship a red test for work it does not own.
//
// IT BECOMES BINDING THE MOMENT A5 BINDS A WORKFLOW to the Draft Decision
// Study, and it cannot lapse back: A5's own acceptance requires a workflow on
// every pilot type. The guard below is what stops it going vacuous for the
// OTHER reason — a link type set that no longer names a transition at all.
//
// Mutation (once A5 has run): seed the raise step under any code other than
// eoDraftRaiseToHETransitionCode. Today: delete blocksTransitionCode from
// eoObjectionLinkKey's spec, and the guard fires.
func TestEoBlockedTransitionNamesATransitionItsWorkflowHas(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	named := 0
	bound := 0
	for key, lt := range eoSeededLinkTypes(t, db) {
		if lt.BlocksParentTransitionKey == "" {
			continue
		}
		named++
		for _, raw := range eoLinkTypeIDs(t, "target_type_ids", lt.TargetTypeIDs) {
			if raw == eoAnyTargetType {
				// A polymorphic target names every type, so there is no one
				// workflow to check. No pilot link type is in this position;
				// if one ever is, the rule needs a different guard.
				continue
			}
			parentTypeID, err := uuid.Parse(raw)
			if err != nil {
				t.Fatalf("link type %q has the unparseable target %q", key, raw)
			}
			// The binding services.ResolveWorkflowForType reads, and only it:
			// workflows.record_type_id. The record type's own workflow_id is
			// the designer's copy of the same fact.
			var workflows []models.Workflow
			if err := db.Where("record_type_id = ? AND is_active = ?", parentTypeID, true).Find(&workflows).Error; err != nil {
				t.Fatalf("read the workflows bound to %s: %v", parentTypeID, err)
			}
			for _, wf := range workflows {
				bound++
				var codes []string
				if err := db.Model(&models.WorkflowTransition{}).
					Where("workflow_id = ? AND is_active = ?", wf.ID, true).
					Pluck("code", &codes).Error; err != nil {
					t.Fatalf("read the transitions of workflow %q: %v", wf.Code, err)
				}
				found := false
				for _, c := range codes {
					if c == lt.BlocksParentTransitionKey {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("link type %q blocks the transition %q, and the workflow %q bound to its parent type %s has no active transition with that code (it has %v): the rule matches nothing, an objection is raised and silently ignored, and the engine reports neither",
						key, lt.BlocksParentTransitionKey, wf.Code, parentTypeID, codes)
				}
			}
		}
	}
	// The guard against the other kind of vacuity: a link type set that names
	// no transition at all would make the loop above prove nothing.
	if named == 0 {
		t.Fatalf("no seeded link type names a blocked transition; eo_objection_parent is supposed to, and without it this test checks nothing")
	}
	if bound == 0 {
		t.Logf("vacuous as expected: %d link type(s) name a blocked transition and no workflow is bound to any of their parent types yet. Task A5 binds them, and this test becomes binding then.", named)
	}
}

// eoIsResolvedClosureArtefact reports whether Task A5 resolved this matrix
// "Yes" cell as required at closure instead of at creation.
func eoIsResolvedClosureArtefact(typeKey, matrixKey string) bool {
	for _, a := range eoClosureArtefacts {
		if a.TypeKey == typeKey && a.FieldKey == matrixKey {
			return true
		}
	}
	return false
}

// ── Task B2: the file fields are not required while Documenta is off ────────

// TestEoFileFieldsAreNotRequiredWithoutDocumenta is the task's first
// requirement: after the real seed, none of the registered file fields demands
// a value.
//
// THE PRODUCT FACT IT ENCODES. B1's upload route refuses with 503 and mints no
// id while EO_DOCUMENTA_ENABLED=false, which is the pilot's default. A field
// that is `always` and holds a file is therefore a demand no user can meet:
// the form asks for a document id and nothing in the running system can issue
// one, so the register cannot be created at all. The ruling is to relax the
// requirement rather than weaken B1 into minting stubs, because a register
// that stores a fake document reference is worse than one that does not
// require a document yet.
//
// IT GOES THROUGH seedEOPilot, not through the writer. A test that called
// eoRelaxFileRequirednessUntilDocumenta directly would pass with the step
// absent from eoPilotSteps, which is this programme's recurring defect: a
// seed step that is never reached, proved correct by a test that bypassed the
// gate.
//
// Mutation: delete the eoRelaxFileRequirednessStepKey entry from eoPilotSteps.
func TestEoFileFieldsAreNotRequiredWithoutDocumenta(t *testing.T) {
	if len(eoFileFieldsRelaxedUntilDocumenta) == 0 {
		t.Fatal("eoFileFieldsRelaxedUntilDocumenta is empty; this test would prove nothing")
	}
	db := eoPilotDB(t)
	seedEOPilot(db)

	for _, r := range eoFileFieldsRelaxedUntilDocumenta {
		t.Run(r.TypeKey+"."+r.FieldKey, func(t *testing.T) {
			got := eoEffectiveField(t, db, r.TypeKey, r.FieldKey)
			switch got.Kind {
			case models.EOKindFile, models.EOKindMultiFile:
			default:
				t.Fatalf("the registry relaxes %q on %q, whose kind is %q; this step is only about fields that hold a Documenta file id",
					r.FieldKey, r.TypeKey, got.Kind)
			}
			if got.RequiredMode != models.EORequiredNever {
				t.Errorf("required_mode is %q; while EO Documenta is disabled no file id can be minted, so a %q file field makes the register uncreatable",
					got.RequiredMode, got.RequiredMode)
			}
		})
	}
}

// TestNoPilotFileFieldIsDemandedAtAll is the COMPLETENESS half, and the
// reason a one-shot step is safe here.
//
// The objection to a one-shot step is that it cannot reach a field definition
// created after it ran. The only two things that create one are a later seed
// step and an administrator in the type designer; the second is a deliberate
// choice this step must not undo, and the first is what this test catches. A
// twelfth record type with a demanded file field fails CI here, with the
// registry named, long before it reaches a pilot database.
//
// IT ASKS FOR ANYTHING THAT IS NOT `never`, not for the modes this task
// happened to find. The first draft asked for `always` and would have missed
// the two `at_state` evidence fields it was written beside — a requirement
// satisfied at a CLOSING TRANSITION is no more satisfiable than one satisfied
// at creation when no file id can be minted either way, and it fails worse:
// the record is created and then deadlocks. A query for the modes already
// known would miss the next one too, so it asks the complement.
//
// It asks the DATABASE, over every seeded type, rather than the spec tables,
// so a field that arrives by any route at all is in scope.
//
// Mutation: add a `file` field with any required mode but `never` to a pilot
// type spec without registering it.
func TestNoPilotFileFieldIsDemandedAtAll(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	type row struct {
		TypeKey      string
		Key          string
		Kind         string
		RequiredMode string
	}
	var rows []row
	err := db.Table("eo_field_defs AS f").
		Select("t.key AS type_key, f.key AS key, f.kind AS kind, f.required_mode AS required_mode").
		Joins("JOIN eo_record_types t ON t.id = f.record_type_id AND t.deleted_at IS NULL").
		// COALESCE: the column defaults to 'never' but a row written before
		// it existed carries NULL, which is the same thing and must not be
		// reported.
		Where("f.deleted_at IS NULL AND f.kind IN ? AND COALESCE(f.required_mode, ?) NOT IN ?",
			[]string{models.EOKindFile, models.EOKindMultiFile}, models.EORequiredNever,
			[]string{"", models.EORequiredNever}).
		Order("t.key, f.key").Scan(&rows).Error
	if err != nil {
		t.Fatalf("look for file fields that are still demanded: %v", err)
	}
	for _, r := range rows {
		t.Errorf("%s.%s is kind %q and required_mode %q: no upload route can mint a file id while EO Documenta is disabled, so nothing can satisfy it — at creation or at a closing transition. Register it in eoFileFieldsRelaxedUntilDocumenta or seed it `never`.",
			r.TypeKey, r.Key, r.Kind, r.RequiredMode)
	}
}

// TestEoFileRelaxationDoesNotRevertAnAdministratorsRetightening is the
// ADMINISTRATOR'S EDIT guard, and the reason this step is NOT convergent.
//
// The ruling says re-tightening is a configuration change through the type
// designer once the client's Documenta is connected — and ONLY WHILE THE
// TYPE AND EVERY TYPE BENEATH IT HOLD NO RECORDS. That qualifier is not
// decoration: models.ValidateTypeEdit refuses `never -> always` and
// `never -> at_state` outright once records exist, so after the first
// filing it takes §7.14's confirm_destructive block, naming the type, the
// field and the change, with a stated backfill rule (eoFileFieldsRelaxedUntilDocumenta
// has the whole of it, and it is the version to copy if this needs restating
// anywhere else). What matters HERE is only that whatever an administrator
// managed to re-tighten inside that window must survive the next boot. A
// convergent step gated on "any registered field is not `never`" would see
// that change as work to do and undo it on the very next boot, silently, which
// is a defect this programme has shipped twice. A one-shot step cannot: its
// applied row is written on the boot that ran it and nothing asks again.
//
// Mutation: mark the step `convergent: true` with a pending check that looks
// for a field that is not `never`.
func TestEoFileRelaxationDoesNotRevertAnAdministratorsRetightening(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	// EVERY ENTRY, EACH RESTORED TO ITS OWN MODE. Asking only about entry
	// [0] was not enough and that is measured, not assumed: with the step
	// made convergent and its pending check recognising `at_state` alone,
	// the single-field version PASSED while this one fails on both evidence
	// fields. A guard that covers one mode of a two-mode registry is a
	// guard with a hole exactly where the newer half lives.
	for _, r := range eoFileFieldsRelaxedUntilDocumenta {
		def := eoEffectiveField(t, db, r.TypeKey, r.FieldKey)
		if def.RequiredMode != models.EORequiredNever {
			t.Fatalf("fixture error: %s.%s is %q after the seed, so there is nothing for an administrator to restore", r.TypeKey, r.FieldKey, def.RequiredMode)
		}
		// What an administrator does, by whatever route is open to them.
		if err := db.Model(&models.EOFieldDef{}).Where("id = ?", def.ID).
			Update("required_mode", r.From).Error; err != nil {
			t.Fatalf("restore %s.%s to %q: %v", r.TypeKey, r.FieldKey, r.From, err)
		}
	}

	for i := 0; i < 3; i++ {
		seedEOPilot(db)
	}

	for _, r := range eoFileFieldsRelaxedUntilDocumenta {
		again := eoEffectiveField(t, db, r.TypeKey, r.FieldKey)
		if again.RequiredMode != r.From {
			t.Errorf("after three more boots %s.%s is %q; an administrator set it to %q and the seed reverted it",
				r.TypeKey, r.FieldKey, again.RequiredMode, r.From)
		}
	}
}

// TestEoAStrandedFileFieldIsReportedOnEveryBoot is the complement of the
// step's WHERE clause.
//
// eoRelaxFileRequirednessUntilDocumenta deliberately SKIPS a field that has
// been moved off the mode the seed wrote — that is how an administrator's
// edit stays theirs. But the step is one-shot and logs the skip once, on the
// boot it happened, so a field left at some third mode (`conditional`, say)
// was stranded in total silence: still demanded, still unsatisfiable while
// Documenta is off, and absent from every report for ever because the report
// listed only fields that were already `never`.
//
// Mutation: drop the `demanded` arm of eoLogFileFieldsAwaitingDocumenta, or
// make it report only when Documenta is enabled.
func TestEoAStrandedFileFieldIsReportedOnEveryBoot(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	r := eoFileFieldsRelaxedUntilDocumenta[0]
	def := eoEffectiveField(t, db, r.TypeKey, r.FieldKey)
	// A mode that is neither `never` nor the one the step relaxes, which is
	// exactly the case the step walks past.
	if err := db.Model(&models.EOFieldDef{}).Where("id = ?", def.ID).
		Update("required_mode", models.EORequiredConditional).Error; err != nil {
		t.Fatalf("strand %s.%s: %v", r.TypeKey, r.FieldKey, err)
	}

	out := eoCaptureSeed(t, db)
	if strings.Contains(out, "Applying the EO pilot configuration") {
		t.Fatalf("fixture error: this boot applied seed steps, so it is not the every-boot report under test.\n--- output ---\n%s", out)
	}
	if !strings.Contains(out, "STILL DEMANDED") {
		t.Errorf("a field stranded at %q is not reported; the step skipped it once and nothing mentions it again, so the register stays unusable in silence.\n--- output ---\n%s", models.EORequiredConditional, out)
	}
	if !strings.Contains(out, r.TypeKey+"."+r.FieldKey) {
		t.Errorf("the report does not name %s.%s.\n--- output ---\n%s", r.TypeKey, r.FieldKey, out)
	}
}

// TestEoFileFieldReportSurvivesANullRequiredMode: required_mode is NULLABLE,
// and the report read it into a string. One NULL row made the whole handler
// error and return, so EVERY remaining field went unreported and the
// obligation line vanished entirely — a report that goes silent in the one
// state it exists to describe.
//
// Mutation: drop the COALESCE, or turn the per-field `continue` back into a
// `return`.
func TestEoFileFieldReportSurvivesANullRequiredMode(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	r := eoFileFieldsRelaxedUntilDocumenta[0]
	def := eoEffectiveField(t, db, r.TypeKey, r.FieldKey)
	if err := db.Exec("UPDATE eo_field_defs SET required_mode = NULL WHERE id = ?", def.ID).Error; err != nil {
		t.Fatalf("null the required_mode of %s.%s: %v", r.TypeKey, r.FieldKey, err)
	}

	out := eoCaptureSeed(t, db)
	if !strings.Contains(out, "file field(s) are optional while EO Documenta is disabled") {
		t.Errorf("one NULL required_mode silenced the whole obligation report.\n--- output ---\n%s", out)
	}
	// A NULL mode IS `never`, so the field is reported as relaxed like the
	// rest rather than dropped or counted as demanded.
	if !strings.Contains(out, r.TypeKey+"."+r.FieldKey+" (restore: "+r.From+")") {
		t.Errorf("the field whose mode is NULL is not listed as relaxed; NULL and `never` are the same state.\n--- output ---\n%s", out)
	}
	if strings.Contains(out, "STILL DEMANDED") {
		t.Errorf("a NULL required_mode is reported as demanded.\n--- output ---\n%s", out)
	}
}

// TestEoAMissingFileFieldIsReportedAsMissing: no row at all is NOT the same
// as a row whose mode is empty, and the first draft could not tell them
// apart — a deleted definition read back as "" and was reported as relaxed
// for ever, which is a field that does not exist described as working.
//
// Mutation: fold the `missing` arm into the `relaxed` one.
func TestEoAMissingFileFieldIsReportedAsMissing(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	r := eoFileFieldsRelaxedUntilDocumenta[0]
	def := eoEffectiveField(t, db, r.TypeKey, r.FieldKey)
	if err := db.Unscoped().Where("id = ?", def.ID).Delete(&models.EOFieldDef{}).Error; err != nil {
		t.Fatalf("remove %s.%s: %v", r.TypeKey, r.FieldKey, err)
	}

	out := eoCaptureSeed(t, db)
	if !strings.Contains(out, "have NO definition at all") {
		t.Errorf("a removed field definition is not reported as missing.\n--- output ---\n%s", out)
	}
	if strings.Contains(out, r.TypeKey+"."+r.FieldKey+" (restore: ") {
		t.Errorf("a removed field definition is listed among the relaxed ones, so a field that does not exist is described as working.\n--- output ---\n%s", out)
	}
}

// TestEoFileRelaxationReachesADatabaseSeededBeforeIt is the OTHER trap: a step
// whose gate can never open on the databases it was written for.
//
// Every pilot database in existence has already applied the type-seeding
// steps, so editing a field spec would change nothing there; the step is the
// only thing that reaches them. This test puts a fully seeded database back
// into the state the previous release left — the fields `always`, the step's
// applied row absent — and requires the ordinary boot path to notice and fix
// it. Nothing here calls the writer.
//
// Mutation: gate the step on anything other than its applied row, or seed the
// relaxation from the field specs instead of from a step.
func TestEoFileRelaxationReachesADatabaseSeededBeforeIt(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	for _, r := range eoFileFieldsRelaxedUntilDocumenta {
		def := eoEffectiveField(t, db, r.TypeKey, r.FieldKey)
		// Each field goes back to ITS OWN previous mode, not to a single
		// one: four were `always` and two `at_state`, and a step that only
		// recognised `always` would leave the two at-closure fields behind.
		if err := db.Model(&models.EOFieldDef{}).Where("id = ?", def.ID).
			Update("required_mode", r.From).Error; err != nil {
			t.Fatalf("put %s.%s back as the previous release left it: %v", r.TypeKey, r.FieldKey, err)
		}
	}
	if err := db.Where("key = ?", eoRelaxFileRequirednessStepKey).
		Delete(&models.EOSeedStep{}).Error; err != nil {
		t.Fatalf("remove the applied row of %q: %v", eoRelaxFileRequirednessStepKey, err)
	}

	open, err := eoPilotSeedNeeded(db)
	if err != nil {
		t.Fatalf("ask whether the seed is needed: %v", err)
	}
	if !open {
		t.Fatalf("the boot fast path stays SHUT on a database that has not applied %q, so the step can never run on any existing pilot database", eoRelaxFileRequirednessStepKey)
	}

	seedEOPilot(db)

	for _, r := range eoFileFieldsRelaxedUntilDocumenta {
		if got := eoEffectiveField(t, db, r.TypeKey, r.FieldKey); got.RequiredMode != models.EORequiredNever {
			t.Errorf("%s.%s is still %q on a database seeded before the step existed", r.TypeKey, r.FieldKey, got.RequiredMode)
		}
	}
}

// TestEoRelaxedFileFieldsAreAnObligationOnceDocumentaIsEnabled is the line the
// operator needs and the one a naive implementation cannot fire.
//
// The relaxation is UNCONDITIONAL — the seed reads no runtime configuration —
// and nothing re-tightens a deployment that later connects Documenta. That is
// accepted, and it is exactly why the boot report must say so: with Documenta
// enabled the relaxed fields stop being a workaround and become a gap in the
// client's matrix that NO ORDINARY DESIGNER EDIT CAN CLOSE once the registers
// hold records, because models.ValidateTypeEdit refuses it; closing it then
// takes §7.14's confirmed destructive edit, with a stated backfill rule and an
// audit row. The line is the only standing notice of that, which is why it
// must fire on every boot.
//
// The disabled wording is covered on every boot by
// TestSeedEOPilotReportsOnEveryBoot, through eoObligationReportLines. This
// test covers the OTHER branch, which no test run with the default
// environment can reach.
//
// Mutation: drop the enabled arm of eoLogFileFieldsAwaitingDocumenta, or read
// the flag at package init instead of per boot.
func TestEoRelaxedFileFieldsAreAnObligationOnceDocumentaIsEnabled(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	t.Setenv("EO_DOCUMENTA_ENABLED", "true")
	out := eoCaptureSeed(t, db)
	if strings.Contains(out, "Applying the EO pilot configuration") {
		t.Fatalf("fixture error: this boot applied seed steps, so it is not the every-boot report under test.\n--- output ---\n%s", out)
	}
	const want = "EO Documenta is ENABLED"
	if !strings.Contains(out, want) {
		t.Errorf("a boot with EO_DOCUMENTA_ENABLED=true does not report %q, so nothing tells the operator that the file fields relaxed for the pilot are still optional.\n--- output ---\n%s", want, out)
	}
	for _, r := range eoFileFieldsRelaxedUntilDocumenta {
		if !strings.Contains(out, r.TypeKey+"."+r.FieldKey) {
			t.Errorf("the obligation line does not name %s.%s, so the operator is not told which field to re-tighten.\n--- output ---\n%s", r.TypeKey, r.FieldKey, out)
		}
	}
}

// ── Plan 3 fix wave: the inert EO grants announce themselves ────────────────

// eoSeedTheRealEORoles creates the EO platform's module-level permissions and
// its roles FROM THE TABLES Seed itself reads (eoModulePermissions,
// eoSeededRoles). It is deliberately not a hand-written copy: a copy would
// keep passing after somebody granted head_of_office a per-type code, which
// is the one change the test below exists to notice.
//
// Roles eoPilotDB has already created (admin, executive_office) get the
// permissions APPENDED rather than being skipped, so that executive_office
// ends the fixture holding what it holds in production — its module codes
// from here and its per-type codes from the pilot seed.
func eoSeedTheRealEORoles(t *testing.T, db *gorm.DB) {
	t.Helper()
	byCode := make(map[string]models.Permission, len(eoModulePermissions))
	for _, p := range eoModulePermissions {
		perm := p
		perm.IsActive = true
		if err := db.Where("code = ?", perm.Code).FirstOrCreate(&perm).Error; err != nil {
			t.Fatalf("create permission %q: %v", perm.Code, err)
		}
		byCode[perm.Code] = perm
	}
	for _, r := range eoSeededRoles {
		perms := make([]models.Permission, 0, len(r.perms))
		for _, code := range r.perms {
			p, ok := byCode[code]
			if !ok {
				t.Fatalf("role %q is seeded with %q, which is not in eoModulePermissions", r.code, code)
			}
			perms = append(perms, p)
		}
		var role models.Role
		err := db.Where("code = ?", r.code).First(&role).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			role = models.Role{Name: r.name, NameAr: r.nameAr, Code: r.code, Description: r.desc, DescriptionAr: r.descAr, IsSystem: true, IsActive: true, Permissions: perms}
			if err := db.Create(&role).Error; err != nil {
				t.Fatalf("create role %q: %v", r.code, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("read role %q: %v", r.code, err)
		}
		if err := db.Model(&role).Association("Permissions").Append(perms); err != nil {
			t.Fatalf("grant the module codes to %q: %v", r.code, err)
		}
	}
}

// eoRolesTheSeedLeavesWithoutTypeCodes derives, from the real tables, which
// seeded roles hold a module-level EO permission and no per-type code. That
// is a PROPERTY OF THE SEED, not a list this test chose: spec §7.11 names
// only models.EOTypePermissionRoleCodes for the per-type grants, so every
// other EO role is in this state by arithmetic.
func eoRolesTheSeedLeavesWithoutTypeCodes() []string {
	holdsTypeCodes := make(map[string]bool, len(models.EOTypePermissionRoleCodes))
	for _, c := range models.EOTypePermissionRoleCodes {
		holdsTypeCodes[c] = true
	}
	module := make(map[string]bool, len(eoModulePermissions))
	for _, p := range eoModulePermissions {
		module[p.Code] = true
	}
	out := []string{}
	for _, r := range eoSeededRoles {
		if holdsTypeCodes[r.code] {
			continue
		}
		for _, code := range r.perms {
			if module[code] {
				out = append(out, r.code)
				break
			}
		}
	}
	return out
}

// TestEOBootReportNamesRolesWithNoPerTypeCodes is the Plan 3 fix wave's
// CRITICAL 2.
//
// Five seeded roles — head_of_office, public_relations, protocol,
// department_user and escalation_manager — hold eo:view, eo:update and
// eo:transition and no per-type code at all, and eo_admin holds eo:view and
// eo:types:manage on the same terms. Since Task A2 that means an empty
// register on every route and a 403 on every record, with nothing in the UI
// or the log to explain it: eoLogTypesWithNoLiveCodes cannot fire, because
// the codes exist and are merely ungranted.
//
// The seed does NOT grant anything to fix it — §7.11 names two roles and who
// else should see what is the client's decision. What it owes is that the
// state is not silent, and that is what this asserts.
//
// IT GOES THROUGH seedEOPilot, not through the reporter: a report that is
// written and never called is the defect this programme has shipped twice,
// and calling eoLogRolesWithNoTypeCodes directly could not see it.
//
// Mutation that makes it fail: drop the eoLogRolesWithNoTypeCodes call from
// eoReportPilotObligations, or move it back under seedEOPilot's `!needed`
// return.
func TestEOBootReportNamesRolesWithNoPerTypeCodes(t *testing.T) {
	db := eoPilotDB(t)
	eoSeedTheRealEORoles(t, db)
	seedEOPilot(db)

	want := eoRolesTheSeedLeavesWithoutTypeCodes()
	for _, required := range []string{"head_of_office", "public_relations", "protocol", "department_user", "escalation_manager", "eo_admin"} {
		found := false
		for _, w := range want {
			if w == required {
				found = true
			}
		}
		if !found {
			t.Fatalf("fixture error: %q is no longer a role with a module-level EO grant and no per-type code (derived: %v). If it was GRANTED per-type codes, that is a client decision somebody took and this list is what records it.", required, want)
		}
	}

	var buf bytes.Buffer
	out, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(out); log.SetFlags(flags) })

	// The SECOND boot: the seed is fully applied, so this is the boot every
	// real deployment does, and the one on which the line must still appear.
	seedEOPilot(db)
	got := buf.String()
	if strings.Contains(got, "Applying the EO pilot configuration") {
		t.Fatalf("fixture error: the second boot applied seed steps.\n--- output ---\n%s", got)
	}

	for _, role := range want {
		line := `role "` + role + `" holds EO module permissions`
		if !strings.Contains(got, line) {
			t.Errorf("the boot log does not carry %q: this role reaches every EO screen and sees an empty register, and nothing else in the system says so.\n--- output ---\n%s", line, got)
		}
	}
	for _, phrase := range []string{
		"every register is EMPTY for it and every record answers 403",
		"grant eo:<type_key>:view|create|update to this role through the permissions UI",
	} {
		if !strings.Contains(got, phrase) {
			t.Errorf("the boot log does not say %q, so the line names a state without naming what it means or what to do about it.\n--- output ---\n%s", phrase, got)
		}
	}

	// The other direction: a role that DOES hold the per-type codes must not
	// be named, or the line is noise on every boot of every deployment.
	for _, granted := range models.EOTypePermissionRoleCodes {
		if strings.Contains(got, `role "`+granted+`" holds EO module permissions`) {
			t.Errorf("the boot log names %q, which holds the per-type codes: its register is not empty.\n--- output ---\n%s", granted, got)
		}
	}
}

// ── Task B1 (remediation): the subject taxonomy ─────────────────────────────

// eoWantSubjectTaxonomy is the fixed set the category must hold, written out
// here rather than derived from eoSubjectTaxonomyValues so that dropping or
// renaming a value in the seed fails this test.
var eoWantSubjectTaxonomy = []string{
	"parking", "tariffs", "central_district", "food_trucks", "licensing",
	"building_permits", "road_safety", "drainage", "street_lighting", "food_safety",
}

func eoTaxonomyValues(t *testing.T, db *gorm.DB) ([]models.LookupCategory, []models.LookupValue) {
	t.Helper()
	var cats []models.LookupCategory
	if err := db.Where("code = ?", eoSubjectTaxonomyCode).Find(&cats).Error; err != nil {
		t.Fatal(err)
	}
	var vals []models.LookupValue
	if len(cats) == 1 {
		if err := db.Where("category_id = ?", cats[0].ID).Order("sort_order").Find(&vals).Error; err != nil {
			t.Fatal(err)
		}
	}
	return cats, vals
}

// Mutation: remove the step from eoPilotSteps; drop a value from
// eoSubjectTaxonomyValues; remove "provisional" from the description; blank a
// name_ar.
func TestSeedEOPilot_SeedsSubjectTaxonomy(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)
	cats, vals := eoTaxonomyValues(t, db)
	if len(cats) != 1 {
		t.Fatalf("category %q count = %d, want 1", eoSubjectTaxonomyCode, len(cats))
	}
	if !strings.Contains(strings.ToLower(cats[0].Description), "provisional") ||
		!strings.Contains(strings.ToLower(cats[0].Description), "sign-off") {
		t.Errorf("description %q does not say the list is provisional pending client sign-off", cats[0].Description)
	}
	if cats[0].Name == "" || cats[0].NameAr == "" || !cats[0].IsActive {
		t.Errorf("category lacks a name, an Arabic name, or is inactive: %+v", cats[0])
	}
	var got []string
	for _, v := range vals {
		got = append(got, v.Code)
		if v.Name == "" || v.NameAr == "" || !v.IsActive {
			t.Errorf("value %q lacks an English or Arabic name or is inactive: %+v", v.Code, v)
		}
	}
	want := append([]string(nil), eoWantSubjectTaxonomy...)
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("values = %v, want %v", got, want)
	}
}

// The demo script and the seed are two producers of the same vocabulary; this
// reads the script's slugs as data so they cannot drift. It also fails if it
// finds none, so a changed script format cannot make it vacuous.
// Mutation: add a slug to a keywords list in scripts/eo-demo-data.py that the
// taxonomy lacks.
func TestSubjectTaxonomyCoversTheDemoScriptSlugs(t *testing.T) {
	src, err := os.ReadFile("../../scripts/eo-demo-data.py")
	if err != nil {
		t.Fatal(err)
	}
	lists := regexp.MustCompile(`"keywords":\s*\[([^\]]*)\]`).FindAllStringSubmatch(string(src), -1)
	if len(lists) == 0 {
		t.Fatal("found no \"keywords\" list in the demo script; the pattern no longer matches it")
	}
	in := map[string]bool{}
	for _, c := range eoWantSubjectTaxonomy {
		in[c] = true
	}
	seen := 0
	for _, l := range lists {
		for _, m := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(l[1], -1) {
			seen++
			if !in[m[1]] {
				t.Errorf("the demo script stores keyword %q, which is not in the subject taxonomy", m[1])
			}
		}
	}
	if seen == 0 {
		t.Fatal("the demo script's keywords lists held no slugs")
	}
}

// Mutation: make the step create unconditionally, or make pending always true
// while the run does not check for an existing row.
func TestSeedEOPilot_SubjectTaxonomyIsIdempotent(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)
	seedEOPilot(db)
	seedEOPilot(db)
	cats, vals := eoTaxonomyValues(t, db)
	if len(cats) != 1 || len(vals) != len(eoWantSubjectTaxonomy) {
		t.Fatalf("after three seeds: %d categories, %d values; want 1 and %d", len(cats), len(vals), len(eoWantSubjectTaxonomy))
	}
	if open, err := eoSubjectTaxonomyPending(db); err != nil || open {
		t.Errorf("pending after seeding = %v (err %v), want false", open, err)
	}
}

// The convergent half: a value missing from an existing category is added
// back, and an administrator's rename is not undone.
// Mutation: gate the step on the category's existence alone.
func TestSeedEOPilot_SubjectTaxonomyAddsAMissingValueAndKeepsEdits(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)
	cats, _ := eoTaxonomyValues(t, db)
	if len(cats) != 1 {
		t.Fatalf("fixture: category count = %d", len(cats))
	}
	if err := db.Unscoped().Where("category_id = ? AND code = ?", cats[0].ID, "drainage").Delete(&models.LookupValue{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.LookupValue{}).Where("category_id = ? AND code = ?", cats[0].ID, "parking").
		Update("name", "Car parking (edited)").Error; err != nil {
		t.Fatal(err)
	}
	if open, err := eoSubjectTaxonomyPending(db); err != nil || !open {
		t.Fatalf("pending with a value missing = %v (err %v), want true", open, err)
	}
	seedEOPilot(db)
	_, vals := eoTaxonomyValues(t, db)
	if len(vals) != len(eoWantSubjectTaxonomy) {
		t.Errorf("values after re-seed = %d, want %d (the missing one added back)", len(vals), len(eoWantSubjectTaxonomy))
	}
	for _, v := range vals {
		if v.Code == "parking" && v.Name != "Car parking (edited)" {
			t.Errorf("an edited value was overwritten: %q", v.Name)
		}
	}
}

// A value soft-deleted through DeleteValue (which has no is_system guard) must
// come back, restored rather than duplicated.
// Mutation: make pending Unscoped again, or make the step insert.
func TestSeedEOPilot_SubjectTaxonomyRestoresASoftDeletedValue(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)
	cats, _ := eoTaxonomyValues(t, db)
	if len(cats) != 1 {
		t.Fatalf("fixture: category count = %d", len(cats))
	}
	if err := db.Where("category_id = ? AND code = ?", cats[0].ID, "drainage").Delete(&models.LookupValue{}).Error; err != nil {
		t.Fatal(err)
	}
	if _, vals := eoTaxonomyValues(t, db); len(vals) != len(eoWantSubjectTaxonomy)-1 {
		t.Fatalf("fixture: soft delete left %d live values", len(vals))
	}
	if open, err := eoSubjectTaxonomyPending(db); err != nil || !open {
		t.Fatalf("pending with a soft-deleted value = %v (err %v), want true", open, err)
	}
	seedEOPilot(db)
	_, vals := eoTaxonomyValues(t, db)
	if len(vals) != len(eoWantSubjectTaxonomy) {
		t.Errorf("live values after re-seed = %d, want %d", len(vals), len(eoWantSubjectTaxonomy))
	}
	var all int64
	db.Unscoped().Model(&models.LookupValue{}).Where("category_id = ? AND code = ?", cats[0].ID, "drainage").Count(&all)
	if all != 1 {
		t.Errorf("rows for drainage = %d, want 1 (restored, not duplicated)", all)
	}
}
