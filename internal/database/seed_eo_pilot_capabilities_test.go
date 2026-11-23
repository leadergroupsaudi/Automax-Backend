package database

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// ── Task A6: the per-type permission codes and the capability entries ───────
//
// WHAT CAN BE PROVED HERE AND WHAT CANNOT. This package seeds the
// configuration; `package services` holds the engine that reads it, and
// cannot be imported from here (services imports database). So these tests
// assert what the seed WROTE — the permission rows, their grants, their
// wording, the capability blobs, and the field definition the capability's
// reservation required removing. The tests that put the seeded configuration
// in front of the engine that reads it — CapabilitiesFor, the reserved-key
// refusal, ValidateWorkflow over the bound workflow — live in
// internal/services/eo_pilot_capabilities_test.go, on the precedent of
// eo_pilot_links_test.go.

// eoCapTypePermissionActions is the three actions spec §7.11 names, in the
// order eoSeedTypePermissions writes them.
var eoCapTypePermissionActions = []string{"view", "create", "update"}

// TestEoEveryConcreteTypeHasItsThreePermissions is spec §7.11's per-type gate
// applied to the PILOT's eleven types. eoSeedTypePermissions already ran for
// Plan 1's smoke types, from the platform seed; the pilot types are seeded
// afterwards and nothing called it again, so until Task A6 none of the eleven
// had a single code (docs/superpowers/eo-plan1-carry-forward.md records this
// as found-absent).
//
// Mutation that makes it fail: drop the seed_type_permissions step from
// eoPilotSteps, or change its key (a renamed key is a step that has never
// run, so on a fresh database it would still pass — which is why the key's
// permanence has its own test, TestEOPilotStepKeysAreUnique, and why this one
// asserts the ROWS rather than the step).
func TestEoEveryConcreteTypeHasItsThreePermissions(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	var concrete []models.EORecordType
	if err := db.Where("is_abstract = ?", false).Order("key").Find(&concrete).Error; err != nil {
		t.Fatalf("read concrete record types: %v", err)
	}
	seeded := map[string]bool{}
	for _, ct := range concrete {
		seeded[ct.Key] = true
	}
	for _, key := range eoPilotTypeKeys {
		if !seeded[key] {
			t.Fatalf("record type %q is not seeded concrete, so this test would prove nothing about it", key)
		}
	}

	held := map[string]map[string]bool{}
	for _, roleCode := range []string{"admin", "executive_office"} {
		var role models.Role
		if err := db.Preload("Permissions").Where("code = ?", roleCode).First(&role).Error; err != nil {
			t.Fatalf("read role %q: %v", roleCode, err)
		}
		codes := map[string]bool{}
		for _, p := range role.Permissions {
			codes[p.Code] = true
		}
		held[roleCode] = codes
	}

	for _, ct := range concrete {
		for _, action := range eoCapTypePermissionActions {
			code := fmt.Sprintf("eo:%s:%s", ct.Key, action)
			t.Run(code, func(t *testing.T) {
				var perm models.Permission
				if err := db.Where("code = ?", code).First(&perm).Error; err != nil {
					t.Fatalf("permission %q was not seeded: %v", code, err)
				}
				// licensing/catalog_test.go resolves a permission to its
				// catalogued feature through the module, so a per-type code
				// under any other module is a code the licence does not cover.
				if perm.Module != "eo" {
					t.Errorf("module %q, want %q", perm.Module, "eo")
				}
				for _, roleCode := range []string{"admin", "executive_office"} {
					if !held[roleCode][code] {
						t.Errorf("role %q does not hold %q", roleCode, code)
					}
				}
			})
		}
	}
}

// TestEoPerTypePermissionCodesAreTheCodesTheGateBuilds is the cross-check
// Task A2 rests on. The route gate asks HasPermission for
// models.EOTypePermissionCode(key, action); the register's list filter reads
// the caller's codes back through EOTypeKeysFromPermissionCodes. Both are
// answered by the rows THIS seed writes, and neither can see them.
//
// Plan 1 shipped a Critical of exactly this shape: Referral visibility was
// seeded under `visibility` while the engine read `visible_to`, so every
// Referral was unrestricted while the code looked enforced. A gate that builds
// a code nobody holds refuses everyone; a filter that builds one everybody
// holds filters nothing.
//
// Mutation that makes it fail: spell the Code in eoSeedTypePermissions by
// hand again (fmt.Sprintf("eo:%s:%s", …)) and change one character of it.
func TestEoPerTypePermissionCodesAreTheCodesTheGateBuilds(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	var types []models.EORecordType
	if err := db.Where("is_abstract = ?", false).Order("key").Find(&types).Error; err != nil {
		t.Fatalf("read concrete record types: %v", err)
	}
	if len(types) == 0 {
		t.Fatal("no concrete record types, so every assertion below is checked against nothing")
	}

	var perms []models.Permission
	if err := db.Where("module = ?", "eo").Order("code").Find(&perms).Error; err != nil {
		t.Fatalf("read the eo permissions: %v", err)
	}
	seeded := map[string]models.Permission{}
	for _, p := range perms {
		seeded[p.Code] = p
	}

	held := []string{}
	for _, ty := range types {
		for _, action := range eoCapTypePermissionActions {
			code := models.EOTypePermissionCode(ty.Key, action)
			if _, ok := seeded[code]; !ok {
				t.Errorf("the gate would ask for %q and the seed wrote no such row", code)
				continue
			}
			if action == models.EOTypePermissionView {
				held = append(held, code)
			}
		}
	}

	// The whole eo module's codes, read back as the register reads a caller's
	// permissions: every concrete type, and nothing that is not one. A
	// module-level code such as eo:view must not be read as a type key.
	all := make([]string, 0, len(perms))
	for _, p := range perms {
		all = append(all, p.Code)
	}
	gotKeys := models.EOTypeKeysFromPermissionCodes(all, models.EOTypePermissionView)
	wantKeys := make([]string, 0, len(types))
	for _, ty := range types {
		wantKeys = append(wantKeys, ty.Key)
	}
	sort.Strings(gotKeys)
	sort.Strings(wantKeys)
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Errorf("the register would restrict to %v, want exactly the concrete types %v", gotKeys, wantKeys)
	}
	if len(held) != len(types) {
		t.Errorf("%d view codes for %d types", len(held), len(types))
	}
}

// TestEoPerTypePermissionDescriptionsSayTheyAreEnforced is the other half of
// what used to be asserted here. Until Task A2 nothing consulted these codes
// and the seeded wording said so, in both languages; the gate is now wired, so
// the wording must say THAT instead. A box that tells an administrator it
// applies no control while refusing their users is the same defect the inert
// sentence existed to prevent, pointing the other way.
//
// Mutation that makes it fail: restore eoTypePermissionInertEN's text.
func TestEoPerTypePermissionDescriptionsSayTheyAreEnforced(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	var perms []models.Permission
	if err := db.Where("module = ? AND code LIKE ?", "eo", "eo:eo\\_%").Order("code").Find(&perms).Error; err != nil {
		t.Fatalf("read the per-type permissions: %v", err)
	}
	if len(perms) == 0 {
		t.Fatal("no per-type permission rows, so the wording below is checked against nothing")
	}
	// strings.Contains(x, "") is true for every x, so an emptied constant
	// would make every assertion below pass while the administrator read a
	// description that says nothing. Checked first, and loudly.
	if len(models.EOTypePermissionEnforcedEN) < 20 || len(models.EOTypePermissionEnforcedAR) < 20 {
		t.Fatalf("the enforcement sentences are %d and %d characters; substring assertions against a near-empty needle prove nothing",
			len(models.EOTypePermissionEnforcedEN), len(models.EOTypePermissionEnforcedAR))
	}
	if !strings.Contains(strings.ToLower(models.EOTypePermissionEnforcedEN), "enforced") {
		t.Errorf("the English sentence %q does not tell the administrator the code is enforced", models.EOTypePermissionEnforcedEN)
	}
	if strings.Contains(strings.ToLower(models.EOTypePermissionEnforcedEN), "not yet enforced") {
		t.Errorf("the English sentence %q still says the code is inert", models.EOTypePermissionEnforcedEN)
	}
	for _, p := range perms {
		t.Run(p.Code, func(t *testing.T) {
			if !strings.Contains(p.Description, models.EOTypePermissionEnforcedEN) {
				t.Errorf("Description %q does not carry %q", p.Description, models.EOTypePermissionEnforcedEN)
			}
			if !strings.Contains(p.DescriptionAr, models.EOTypePermissionEnforcedAR) {
				t.Errorf("DescriptionAr %q does not carry %q", p.DescriptionAr, models.EOTypePermissionEnforcedAR)
			}
		})
	}
}

// TestEoStaleInertDescriptionsAreRefreshedByTheRealSeed drives seedEOPilot,
// NOT the writer.
//
// The first attempt at this fix tested eoSeedTypePermissions directly. It
// proved the function refreshes a stale row and could not see that nothing
// ever called it: seedEOPlatform returns early once eo_base exists, and the
// pilot step's gate asked only whether a CODE WAS MISSING — a stale
// description is not a missing code. Every production database would have
// kept reading "NOT YET ENFORCED: nothing in the platform consults this code"
// while the gate refused their users. The gate is the thing under test, so
// the test goes through it.
//
// Mutation that makes it fail: drop the stale check from
// eoTypePermissionsPending and the step never opens.
func TestEoStaleInertDescriptionsAreRefreshedByTheRealSeed(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	var row models.Permission
	if err := db.Where("code LIKE ?", "eo:eo\\_%").Order("code").First(&row).Error; err != nil {
		t.Fatalf("read a per-type permission: %v", err)
	}
	fresh := row.Description
	freshAr := row.DescriptionAr

	// Exactly what a database seeded before Task A2 holds.
	stale := "View executive-office records of type Assignment (eo_assignment). " + eoTypePermissionLegacyInertEN
	staleAr := "عرض سجلات المكتب التنفيذي من نوع تكليف. " + eoTypePermissionLegacyInertAR
	if err := db.Model(&models.Permission{}).Where("id = ?", row.ID).
		Updates(map[string]any{"description": stale, "description_ar": staleAr}).Error; err != nil {
		t.Fatalf("write the stale description: %v", err)
	}

	// The gate must OPEN for it. Without this the assertion below could pass
	// because the seed ran for some other reason.
	open, err := eoTypePermissionsPending(db)
	if err != nil {
		t.Fatalf("ask the gate: %v", err)
	}
	if !open {
		t.Fatal("the convergent step is shut on a database carrying the pre-A2 inert sentence, so nothing will ever rewrite it")
	}

	seedEOPilot(db)

	var after models.Permission
	if err := db.First(&after, "id = ?", row.ID).Error; err != nil {
		t.Fatalf("read it back: %v", err)
	}
	if after.Description != fresh || after.DescriptionAr != freshAr {
		t.Errorf("description = %q / %q, want it refreshed to %q / %q", after.Description, after.DescriptionAr, fresh, freshAr)
	}
	if after.ID != row.ID || after.Code != row.Code || after.Name != row.Name {
		t.Errorf("the refresh rewrote more than the description: %+v", after)
	}

	// And it must SHUT again, or the seed transaction runs on every boot.
	open, err = eoTypePermissionsPending(db)
	if err != nil {
		t.Fatalf("ask the gate again: %v", err)
	}
	if open {
		t.Error("the gate is still open after the rewrite: the seed would run on every boot for ever")
	}
}

// eoAdminEditsDescription does what PUT /api/v1/admin/permissions/:id does,
// and ONLY that: it writes `description`. There is no description_ar in that
// request (handlers.UpdatePermission), so this is the exact state an
// administrator's edit can produce — and the state the first version of these
// tests could not reach, because it wrote both columns.
func eoAdminEditsDescription(t *testing.T, db *gorm.DB, id uuid.UUID, text string) {
	t.Helper()
	if err := db.Model(&models.Permission{}).Where("id = ?", id).
		Update("description", text).Error; err != nil {
		t.Fatalf("apply the administrator's edit: %v", err)
	}
}

// TestEoSeedDoesNotRevertAnAdministratorsDescriptionEdit is the other side of
// the narrowing. PUT /api/v1/admin/permissions/:id writes Description, so an
// edited description is a deliberate administrative act, not drift. A refresh
// keyed on "differs from the computed text" reverts every one of them — Plan
// 2's fix wave shipped that regression once already.
//
// The two subtests are the two states the API can leave a row in, and the
// FIRST is the one a row-level staleness predicate gets wrong: an edited
// English description beside the legacy Arabic one, which no administrator can
// clear because no request writes that column.
func TestEoSeedDoesNotRevertAnAdministratorsDescriptionEdit(t *testing.T) {
	edited := "Only the Minister's office may read these. Ask the Secretary General before granting."

	t.Run("edited English beside the legacy Arabic: the edit survives, the Arabic is refreshed", func(t *testing.T) {
		db := eoPilotDB(t)
		seedEOPilot(db)

		var row models.Permission
		if err := db.Where("code LIKE ?", "eo:eo\\_%").Order("code").First(&row).Error; err != nil {
			t.Fatalf("read a per-type permission: %v", err)
		}
		freshAr := row.DescriptionAr

		// A pre-A2 row: both columns carry the inert sentence …
		if err := db.Model(&models.Permission{}).Where("id = ?", row.ID).
			Updates(map[string]any{
				"description":    "View executive-office records of type Assignment (eo_assignment). " + eoTypePermissionLegacyInertEN,
				"description_ar": "عرض سجلات المكتب التنفيذي من نوع تكليف. " + eoTypePermissionLegacyInertAR,
			}).Error; err != nil {
			t.Fatalf("write the pre-A2 row: %v", err)
		}
		// … and then an administrator edits it, which touches the English only.
		eoAdminEditsDescription(t, db, row.ID, edited)

		seedEOPilot(db)

		var after models.Permission
		if err := db.First(&after, "id = ?", row.ID).Error; err != nil {
			t.Fatalf("read it back: %v", err)
		}
		if after.Description != edited {
			t.Errorf("CLOBBERED: description = %q, want the administrator's %q", after.Description, edited)
		}
		if after.DescriptionAr != freshAr {
			t.Errorf("description_ar = %q, want the refreshed %q: the Arabic still carries a sentence no request can clear", after.DescriptionAr, freshAr)
		}
		// The gate must shut: the row has nothing stale left in it.
		open, err := eoTypePermissionsPending(db)
		if err != nil {
			t.Fatalf("ask the gate: %v", err)
		}
		if open {
			t.Error("the gate is still open, so the seed would run on every boot for ever")
		}
	})

	t.Run("an edit on a fully refreshed row survives a twelfth type", func(t *testing.T) {
		db := eoPilotDB(t)
		seedEOPilot(db)

		var row models.Permission
		if err := db.Where("code LIKE ?", "eo:eo\\_%").Order("code").First(&row).Error; err != nil {
			t.Fatalf("read a per-type permission: %v", err)
		}
		freshAr := row.DescriptionAr
		eoAdminEditsDescription(t, db, row.ID, edited)

		// A twelfth concrete type: the case the convergent step exists for.
		// It opens the gate through the MISSING-CODE question, so the writer
		// runs over every row — including the edited one.
		twelfth := models.EORecordType{ID: uuid.New(), Key: "eo_board_resolution", Name: "Board Resolution", NameAr: "قرار مجلس", IsActive: true}
		if err := db.Create(&twelfth).Error; err != nil {
			t.Fatalf("create a twelfth type: %v", err)
		}
		open, err := eoTypePermissionsPending(db)
		if err != nil {
			t.Fatalf("ask the gate: %v", err)
		}
		if !open {
			t.Fatal("a type with no codes did not open the step, so the rest of this subtest proves nothing")
		}

		seedEOPilot(db)

		var after models.Permission
		if err := db.First(&after, "id = ?", row.ID).Error; err != nil {
			t.Fatalf("read it back: %v", err)
		}
		if after.Description != edited || after.DescriptionAr != freshAr {
			t.Errorf("the seed reverted an administrator's edit: %q / %q, want %q / %q", after.Description, after.DescriptionAr, edited, freshAr)
		}
		for _, a := range models.EOTypePermissionActions {
			code := models.EOTypePermissionCode(twelfth.Key, a.Action)
			var n int64
			if err := db.Model(&models.Permission{}).Where("code = ?", code).Count(&n).Error; err != nil {
				t.Fatalf("count %q: %v", code, err)
			}
			if n != 1 {
				t.Errorf("%d rows for %q, want 1", n, code)
			}
		}
	})
}

// TestEoSeededCapabilitiesAreTheRegistry pins the seeded blob to the registry
// that declares it, so the two cannot drift: a capability entry written
// straight onto a type would be enabled with nothing recording why.
//
// Mutation that makes it fail: change a config in eoPilotCapabilities without
// re-running the seed step, or add a type to the registry that the step skips.
func TestEoSeededCapabilitiesAreTheRegistry(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	if len(eoPilotCapabilities) == 0 {
		t.Fatal("the registry is empty; every loop below proves nothing")
	}

	want := map[string][]eoCapabilityEnablement{}
	for _, e := range eoPilotCapabilities {
		want[e.TypeKey] = append(want[e.TypeKey], e)
	}

	var types []models.EORecordType
	if err := db.Where("is_abstract = ?", false).Order("key").Find(&types).Error; err != nil {
		t.Fatalf("read concrete record types: %v", err)
	}
	for _, ty := range types {
		ty := ty
		t.Run(ty.Key, func(t *testing.T) {
			var got []struct {
				Key    string          `json:"key"`
				Config json.RawMessage `json:"config"`
			}
			if err := json.Unmarshal(ty.Capabilities, &got); err != nil {
				t.Fatalf("capabilities blob %q does not decode: %v", ty.Capabilities, err)
			}
			entries := want[ty.Key]
			if len(got) != len(entries) {
				t.Fatalf("%d capability entries, want %d (%v)", len(got), len(entries), entries)
			}
			for i, e := range entries {
				if got[i].Key != e.CapabilityKey {
					t.Errorf("entry %d is %q, want %q", i, got[i].Key, e.CapabilityKey)
				}
				if !eoJSONEqual(t, string(got[i].Config), e.Config) {
					t.Errorf("entry %d config is %s, want %s", i, got[i].Config, e.Config)
				}
			}
		})
	}

	t.Run("every entry names a seeded type and says why", func(t *testing.T) {
		for _, e := range eoPilotCapabilities {
			if strings.TrimSpace(e.Why) == "" {
				t.Errorf("%s/%s is enabled with no reason recorded", e.TypeKey, e.CapabilityKey)
			}
			var n int64
			if err := db.Model(&models.EORecordType{}).Where("key = ?", e.TypeKey).Count(&n).Error; err != nil {
				t.Fatal(err)
			}
			if n != 1 {
				t.Errorf("%s/%s names record type %q, which is seeded %d times", e.TypeKey, e.CapabilityKey, e.TypeKey, n)
			}
		}
	})
}

// TestEoCapabilityReservedFieldDefinitionsAreRemoved is the BLOCKING item.
//
// decisions_base returns `related_refs` from ReservedKeys, and
// services.eoCapabilitiesCheckConfig refuses a record type whose field
// definitions name a reserved key — "field %q is written by the capability
// and must not be defined". It fires at the FIRST REQUEST against the type,
// not at seed time, so the Draft Decision Study would be seeded looking
// healthy and be dead on arrival. Task A3 declares `related_refs` as an Auto
// multi_link field; the capability and the declaration cannot both stand.
//
// The removal is a STEP OF THIS SEED rather than an edit to A3's spec,
// because A3's step has already written its applied row on every database
// that has booted: a spec edit would never re-run there, and the two changes
// must land together or the type is unusable in between.
//
// That the type is USABLE afterwards is not provable here — it needs the save
// pipeline, which is in package services. See
// TestEoPilotDraftDecisionStudyIsUsableWithDecisionsBase.
//
// Mutation that makes it fail: drop the removal from eoEnableCapabilities, or
// remove the eoMatrixFieldsNotSeeded entry that records the consequence.
func TestEoCapabilityReservedFieldDefinitionsAreRemoved(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	if len(eoCapabilityReservedFieldRemovals) == 0 {
		t.Fatal("nothing is recorded as removed for a capability's reserved key; the loop proves nothing")
	}
	for _, r := range eoCapabilityReservedFieldRemovals {
		t.Run(r.TypeKey+"."+r.FieldKey, func(t *testing.T) {
			for _, d := range eoOwnFieldDefs(t, db, r.TypeKey) {
				if d.Key == r.FieldKey {
					t.Fatalf("field %q is still defined on %q; the capability that reserves it refuses the type at the first request", r.FieldKey, r.TypeKey)
				}
			}
			// Soft-deleted is not enough: the effective field set is read
			// with the default scope, but a row left behind is a row a later
			// seed could resurrect, and the removal is meant to be final.
			var n int64
			if err := db.Unscoped().Table("eo_field_defs AS f").
				Joins("JOIN eo_record_types t ON t.id = f.record_type_id").
				Where("t.key = ? AND f.key = ?", r.TypeKey, r.FieldKey).
				Count(&n).Error; err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Errorf("%d rows for %q on %q survive unscoped; the removal must be a hard delete", n, r.FieldKey, r.TypeKey)
			}
			// The consequence the client can see: the matrix lists it as a
			// labelled field and the seeded configuration now has no label,
			// no Arabic and no entry in the effective field set for it. That
			// is recorded, with what carries it instead, or nothing tells
			// the client's sign-off round.
			if !eoMatrixFieldIsNotSeeded(r.TypeKey, r.MatrixKey) {
				t.Errorf("%s.%s is removed for a capability's reserved key and is not recorded in eoMatrixFieldsNotSeeded", r.TypeKey, r.MatrixKey)
			}
			if strings.TrimSpace(r.Why) == "" || strings.TrimSpace(r.CapabilityKey) == "" {
				t.Errorf("%s.%s is removed without naming the capability or saying why", r.TypeKey, r.FieldKey)
			}
		})
	}
}

// eoJSONEqual compares two JSON documents by value, so a reformatted config is
// not a failure and a changed key is.
func eoJSONEqual(t *testing.T, a, b string) bool {
	t.Helper()
	var x, y any
	if err := json.Unmarshal([]byte(a), &x); err != nil {
		t.Fatalf("left-hand JSON %q: %v", a, err)
	}
	if err := json.Unmarshal([]byte(b), &y); err != nil {
		t.Fatalf("right-hand JSON %q: %v", b, err)
	}
	ax, err := json.Marshal(x)
	if err != nil {
		t.Fatal(err)
	}
	by, err := json.Marshal(y)
	if err != nil {
		t.Fatal(err)
	}
	return string(ax) == string(by)
}

// TestEoCapabilitiesNotEnabledAreRecordedAndAbsent is the other half of the
// enablement decision: a registered capability that no type carries is a
// DECISION, and the registry is where it is written down. Without this test
// the registry is prose nobody checks, and "nothing enables approval" is
// indistinguishable from an oversight.
//
// Mutation that makes it fail: enable `approval` on a type without removing
// its eoCapabilitiesNotEnabled entry, or empty the entry's Why.
func TestEoCapabilitiesNotEnabledAreRecordedAndAbsent(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	if len(eoCapabilitiesNotEnabled) == 0 {
		t.Fatal("the registry is empty; the loop below proves nothing")
	}
	enabled := map[string]string{}
	for _, e := range eoPilotCapabilities {
		enabled[e.CapabilityKey] = e.TypeKey
	}
	for _, e := range eoCapabilitiesNotEnabled {
		t.Run(e.CapabilityKey, func(t *testing.T) {
			if strings.TrimSpace(e.Why) == "" {
				t.Errorf("%q is recorded as enabled nowhere without saying why", e.CapabilityKey)
			}
			if on, found := enabled[e.CapabilityKey]; found {
				t.Errorf("%q is recorded as enabled nowhere and is enabled on %q", e.CapabilityKey, on)
			}
			// The seeded rows, not just the registry: a type could carry the
			// key from an earlier boot or a hand edit.
			var types []models.EORecordType
			if err := db.Where("is_abstract = ?", false).Find(&types).Error; err != nil {
				t.Fatal(err)
			}
			for _, ty := range types {
				if strings.Contains(string(ty.Capabilities), `"`+e.CapabilityKey+`"`) {
					t.Errorf("record type %q carries %q in its capabilities blob %s", ty.Key, e.CapabilityKey, ty.Capabilities)
				}
			}
		})
	}
}

// TestEoCapabilityBlobRefusesAnUnusableRegistry covers the three guards
// eoCapabilityBlob carries, which nothing else reaches: the seeded registry
// is well-formed, so on the happy path they never fire. Each is a
// configuration mistake that would otherwise reach the engine as a column of
// text and fail at the first request against the type.
//
// Mutation that makes it fail: delete any of the three `return nil, ...`
// guards in eoCapabilityBlob.
func TestEoCapabilityBlobRefusesAnUnusableRegistry(t *testing.T) {
	cases := []struct {
		name    string
		entries []eoCapabilityEnablement
		wantErr string
	}{
		{
			name:    "a capability with no key",
			entries: []eoCapabilityEnablement{{TypeKey: "eo_x", CapabilityKey: "", Config: `{}`}},
			wantErr: "with no key",
		},
		{
			name: "the same capability twice",
			entries: []eoCapabilityEnablement{
				{TypeKey: "eo_x", CapabilityKey: "decisions_base", Config: `{}`},
				{TypeKey: "eo_x", CapabilityKey: "decisions_base", Config: `{}`},
			},
			wantErr: "twice",
		},
		{
			name:    "a config that is not JSON",
			entries: []eoCapabilityEnablement{{TypeKey: "eo_x", CapabilityKey: "decisions_base", Config: `{"base_type_key":}`}},
			wantErr: "is not JSON",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := eoCapabilityBlob(tc.entries)
			if err == nil {
				t.Fatalf("accepted %+v", tc.entries)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not carry %q", err, tc.wantErr)
			}
		})
	}

	t.Run("the real registry renders", func(t *testing.T) {
		blob, err := eoCapabilityBlob(eoPilotCapabilities[:1])
		if err != nil {
			t.Fatalf("the seeded registry does not render: %v", err)
		}
		if !strings.Contains(string(blob), `"key":"`+eoPilotCapabilities[0].CapabilityKey+`"`) {
			t.Errorf("rendered %s, which does not carry the capability key", blob)
		}
	})
}

// TestEoEnableCapabilitiesIsFailClosed: the step names record types by key,
// and a key that resolves to nothing must roll the seed back rather than
// enable a capability on nothing or leave a reserved field definition
// standing. Both halves are run on a REAL database with the type removed,
// inside a transaction that is rolled back.
//
// Mutation that makes it fail: replace either `if ty.ID == uuid.Nil { return
// false, ... }` in eoEnableCapabilities with `continue`.
func TestEoEnableCapabilitiesIsFailClosed(t *testing.T) {
	cases := []struct {
		name    string
		absent  string
		wantErr string
	}{
		{
			name:    "the type whose reserved field must be removed is gone",
			absent:  eoDraftStudyTypeKey,
			wantErr: "which is not seeded",
		},
		{
			name:    "the type a capability is enabled on is gone",
			absent:  eoIncomingTypeKey,
			wantErr: "capabilities are enabled on record type",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := eoPilotDB(t)
			seedEOPilot(db)
			sentinel := fmt.Errorf("roll back")
			err := db.Transaction(func(tx *gorm.DB) error {
				if e := tx.Where("key = ?", tc.absent).Delete(&models.EORecordType{}).Error; e != nil {
					return e
				}
				_, e := eoEnableCapabilities(tx)
				if e == nil {
					t.Errorf("record type %q is gone and the step reported success", tc.absent)
					return sentinel
				}
				if !strings.Contains(e.Error(), tc.wantErr) || !strings.Contains(e.Error(), tc.absent) {
					t.Errorf("error %q names neither %q nor the missing type", e, tc.wantErr)
				}
				return sentinel
			})
			if err != sentinel {
				t.Fatalf("transaction returned %v, want the sentinel", err)
			}
		})
	}
}

// TestEoCapabilityInputsWithoutFieldDefsReportsTheOneThatIsMissing is the
// query behind the boot-time line, checked against the REAL seeded
// configuration rather than a fixture.
//
// It is the one fact an operator wants on first boot and cannot get anywhere
// else: `decisions_base` is enabled on the Draft Decision Study and matches
// nothing, because its `subject_key` names `keywords` and the draft type has
// no such field. A clean startup log would otherwise say nothing about a
// conflict check the client is expecting.
//
// BOTH DIRECTIONS. The first half asserts the gap is reported, exactly once,
// with every field filled; the second asserts the check can return NOTHING —
// a reporter that reports every key would say the same thing on the day the
// field arrives, and the line would then be noise nobody reads.
//
// Mutation that makes it fail: delete the `subject_key` entry from
// eoCapabilityOwnTypeInputKeys, or make eoTypeHasEffectiveField return
// `n >= 0`.
func TestEoCapabilityInputsWithoutFieldDefsReportsTheOneThatIsMissing(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	if len(eoCapabilityOwnTypeInputKeys) == 0 {
		t.Fatal("no capability declares a config key that names a values key on its own type; the query inspects nothing")
	}

	gaps, err := eoCapabilityInputsWithoutFieldDefs(db, eoPilotCapabilities)
	if err != nil {
		t.Fatalf("eoCapabilityInputsWithoutFieldDefs: %v", err)
	}
	want := []eoCapabilityInputGap{
		{TypeKey: eoDraftStudyTypeKey, CapabilityKey: eoDecisionsBaseCapabilityKey,
			ConfigKey: "subject_key", ValuesKey: "keywords"},
	}
	if len(gaps) != len(want) {
		t.Fatalf("reported %v, want %v", gaps, want)
	}
	for i := range want {
		if gaps[i] != want[i] {
			t.Errorf("gap %d is %+v, want %+v", i, gaps[i], want[i])
		}
	}

	t.Run("a config that does not name the key at all is not reported", func(t *testing.T) {
		// The capability's own default then applies, and the defaults are
		// not modelled here — so the honest answer is to say nothing rather
		// than to report a key the configuration never mentioned.
		gaps, err := eoCapabilityInputsWithoutFieldDefs(db, []eoCapabilityEnablement{
			{TypeKey: eoDraftStudyTypeKey, CapabilityKey: eoDecisionsBaseCapabilityKey,
				Config: `{"base_type_key":"` + eoDecisionTypeKey + `"}`},
			{TypeKey: eoDraftStudyTypeKey, CapabilityKey: eoDecisionsBaseCapabilityKey,
				Config: `{"subject_key":"   "}`},
		})
		if err != nil {
			t.Fatalf("eoCapabilityInputsWithoutFieldDefs: %v", err)
		}
		if len(gaps) != 0 {
			t.Errorf("reported %v for configs that name no subject_key", gaps)
		}
	})

	t.Run("a config that is not JSON is an error, never an empty answer", func(t *testing.T) {
		// Unreachable from the seeded registry — eoCapabilityBlob refuses a
		// non-JSON config at seed time — but a reporter that swallowed a
		// decode failure would say "no gaps" about a blob it never read.
		_, err := eoCapabilityInputsWithoutFieldDefs(db, []eoCapabilityEnablement{
			{TypeKey: eoDraftStudyTypeKey, CapabilityKey: eoDecisionsBaseCapabilityKey, Config: `{"subject_key":}`},
		})
		if err == nil {
			t.Fatal("a config that is not JSON was reported as having no gaps")
		}
		if !strings.Contains(err.Error(), "does not decode") {
			t.Errorf("error %q does not say the config could not be read", err)
		}
	})

	t.Run("a key the type DOES provide is not reported", func(t *testing.T) {
		// `standing` is on Decision Record and `grounds` is on the Draft
		// Decision Study; both are real seeded field definitions, so a
		// reporter that cannot tell present from absent fails here.
		for _, tc := range []struct{ typeKey, key string }{
			{eoDraftStudyTypeKey, "grounds"},
			{eoDecisionTypeKey, "keywords"},
			{eoDecisionTypeKey, "standing"},
			// Inherited from the base type, through the ancestor chain.
			{eoDraftStudyTypeKey, "subject_ar"},
		} {
			has, err := eoTypeHasEffectiveField(db, tc.typeKey, tc.key)
			if err != nil {
				t.Fatal(err)
			}
			if !has {
				t.Errorf("%q is seeded on %q and the check says it is not", tc.key, tc.typeKey)
			}
		}
		has, err := eoTypeHasEffectiveField(db, eoDraftStudyTypeKey, "keywords")
		if err != nil {
			t.Fatal(err)
		}
		if has {
			t.Error("the Draft Decision Study has a `keywords` field; the whole premise of the reported gap is gone")
		}
	})
}

// TestEoSeedEOPilotLogsTheUnprovidedCapabilityInput asserts the LINE, not just
// the query behind it. The two lines beside it in seedEOPilot — the
// untranslated labels and the unconfirmed state machines — have their queries
// tested and their log calls untested; this one is cheap to assert because the
// package's tests run sequentially, so capturing the standard logger is safe.
//
// Mutation that makes it fail: delete the eoLogCapabilityInputGaps(db) call
// from seedEOPilot.
func TestEoSeedEOPilotLogsTheUnprovidedCapabilityInput(t *testing.T) {
	db := eoPilotDB(t)

	var buf bytes.Buffer
	out := log.Writer()
	flags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(out); log.SetFlags(flags) })

	seedEOPilot(db)

	got := buf.String()
	for _, want := range []string{
		`capability "decisions_base" on "eo_draft_decision_study"`,
		`subject_key="keywords"`,
		"no field definition on that type provides",
		"expected until the client supplies it",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the seed's output does not carry %q; an operator reading a clean startup log would never learn the conflict check cannot fire.\n--- output ---\n%s", want, got)
		}
	}
}

// TestEoSeedSurvivesADeletedPerTypePermission is the MUST FIX of fix round 3.
//
// permissions.code is uniquely indexed with NO deleted_at predicate, so a row
// deleted through DELETE /api/v1/admin/permissions/:id — an ordinary admin
// action — still occupies its code. While the seed's reads were scoped, that
// code read as MISSING: the convergent step opened, the Create violated the
// unique index, and the WHOLE pilot seed transaction rolled back. On every
// boot, for ever, taking every other pending convergent step with it.
//
// Mutation that makes it fail: drop Unscoped() from eoSeedTypePermissions's
// existence read, or from either query in eoTypePermissionsPending.
func TestEoSeedSurvivesADeletedPerTypePermission(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	code := models.EOTypePermissionCode("eo_objection", models.EOTypePermissionView)
	if err := db.Where("code = ?", code).Delete(&models.Permission{}).Error; err != nil {
		t.Fatalf("delete %q: %v", code, err)
	}
	var live int64
	if err := db.Model(&models.Permission{}).Where("code = ?", code).Count(&live).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if live != 0 {
		t.Fatalf("%q is still live, so the premise of this test is gone", code)
	}

	// The writer must not blow up on the row that still occupies the code …
	if err := eoSeedTypePermissions(db); err != nil {
		t.Fatalf("the seed failed over a deleted permission: %v", err)
	}
	// … and the whole pilot seed must complete.
	seedEOPilot(db)

	// A second boot must find nothing to do, or the seed transaction runs for
	// ever and its log says so on every start.
	open, err := eoTypePermissionsPending(db)
	if err != nil {
		t.Fatalf("ask the gate: %v", err)
	}
	if open {
		t.Error("the gate is open after the seed ran: every boot would re-run the whole pilot seed transaction")
	}

	// The deleted code is NOT resurrected: deleting a permission is an
	// administrative act, like revoking a grant.
	if err := db.Model(&models.Permission{}).Where("code = ?", code).Count(&live).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if live != 0 {
		t.Errorf("the seed resurrected %q, which an administrator deleted", code)
	}
	// Its two siblings are untouched and the rest of the pilot is intact.
	for _, a := range []string{models.EOTypePermissionCreate, models.EOTypePermissionUpdate} {
		sibling := models.EOTypePermissionCode("eo_objection", a)
		var n int64
		if err := db.Model(&models.Permission{}).Where("code = ?", sibling).Count(&n).Error; err != nil {
			t.Fatalf("count %q: %v", sibling, err)
		}
		if n != 1 {
			t.Errorf("%d live rows for %q, want 1", n, sibling)
		}
	}
}

// TestEoGateShutsOnADeletedStaleDescription is fix round 4's NEW-1: the
// never-terminating gate, reintroduced one line from where round 3 closed it.
//
// The three reads around the per-type codes have three different jobs. The
// COUNT must be unscoped, because a deleted row occupies its unique code. The
// STALE read must be SCOPED, because eoSeedTypePermissions's refresh is a
// scoped Updates: for a deleted row it matches nothing. Unscoped there, a
// pre-A2 row that an administrator later deleted opens the step on every
// boot, the writer converges nothing, and the whole pilot seed transaction
// re-runs for ever.
//
// Mutation that makes it fail: put Unscoped() back on the stale read in
// eoTypePermissionsPending.
func TestEoGateShutsOnADeletedStaleDescription(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	code := models.EOTypePermissionCode("eo_objection", models.EOTypePermissionView)
	var row models.Permission
	if err := db.Where("code = ?", code).First(&row).Error; err != nil {
		t.Fatalf("read %q: %v", code, err)
	}
	// A row as a pre-A2 database holds it …
	if err := db.Model(&models.Permission{}).Where("id = ?", row.ID).
		Updates(map[string]any{
			"description":    "View executive-office records of type Objection (eo_objection). " + eoTypePermissionLegacyInertEN,
			"description_ar": "عرض سجلات المكتب التنفيذي من نوع اعتراض. " + eoTypePermissionLegacyInertAR,
		}).Error; err != nil {
		t.Fatalf("write the stale description: %v", err)
	}
	// … which an administrator then deletes.
	if err := db.Where("id = ?", row.ID).Delete(&models.Permission{}).Error; err != nil {
		t.Fatalf("delete it: %v", err)
	}

	seedEOPilot(db)

	open, err := eoTypePermissionsPending(db)
	if err != nil {
		t.Fatalf("ask the gate: %v", err)
	}
	if open {
		t.Fatal("the gate is open on a DELETED stale row the writer cannot converge: every boot would re-run the whole pilot seed transaction")
	}
	// And the row is still deleted: nothing resurrected it to shut the gate.
	var live int64
	if err := db.Model(&models.Permission{}).Where("code = ?", code).Count(&live).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if live != 0 {
		t.Errorf("%q is live again; the gate shut for the wrong reason", code)
	}
}

// TestEoReportPilotObligationsLogsATypeWithNoLiveCodes is fix round 4's
// NEW-2, and it asserts the LINE on a boot where the convergent step is SHUT
// — which is the only state the line exists for. Inside the writer it could
// never fire: the deleted codes still occupy their unique codes, so the gate
// finds nothing missing and the writer does not run.
//
// It follows TestEoSeedEOPilotLogsTheUnprovidedCapabilityInput, the sibling
// line in this file that is asserted the same way.
//
// Mutation that makes it fail: move the eoLogTypesWithNoLiveCodes call back
// inside eoSeedTypePermissions, or delete it from eoReportPilotObligations.
func TestEoReportPilotObligationsLogsATypeWithNoLiveCodes(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	// Every code of one type, deleted the way DELETE /admin/permissions/:id
	// deletes them.
	for _, a := range models.EOTypePermissionActions {
		code := models.EOTypePermissionCode("eo_objection", a.Action)
		if err := db.Where("code = ?", code).Delete(&models.Permission{}).Error; err != nil {
			t.Fatalf("delete %q: %v", code, err)
		}
	}
	// The gate is SHUT in this state, which is the whole point.
	open, err := eoTypePermissionsPending(db)
	if err != nil {
		t.Fatalf("ask the gate: %v", err)
	}
	if open {
		t.Fatal("the gate is open with all three codes deleted; this test no longer covers the state it was written for")
	}

	var buf bytes.Buffer
	out := log.Writer()
	flags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(out); log.SetFlags(flags) })

	seedEOPilot(db)

	got := buf.String()
	for _, want := range []string{
		`record type "eo_objection" has NO live per-type permission code`,
		"eo:eo_objection:view",
		"only a super admin can see its records",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the boot log does not carry %q; an operator looking at a record type nobody can see has nothing to go on.\n--- output ---\n%s", want, got)
		}
	}
}

// ── Task A3 (Plan 3): the capability column is an administrator's to edit ──

// eoAdminEditsCapabilities does what PUT /api/v1/admin/eo-record-types/:id
// does to this column, and only that: handlers.eoRecordTypeRequest.applyTo
// writes `capabilities` WHOLE when the request carries a non-empty value
// (`if len(r.Capabilities) > 0 { t.Capabilities = r.Capabilities }`), so a
// column replace is the exact state that route can leave behind.
func eoAdminEditsCapabilities(t *testing.T, db *gorm.DB, id uuid.UUID, blob string) {
	t.Helper()
	if err := db.Model(&models.EORecordType{}).Where("id = ?", id).
		Update("capabilities", datatypes.JSON([]byte(blob))).Error; err != nil {
		t.Fatalf("apply the administrator's edit: %v", err)
	}
}

// eoCapabilitiesOf reads the column back as the database holds it.
func eoCapabilitiesOf(t *testing.T, db *gorm.DB, typeKey string) string {
	t.Helper()
	var ty models.EORecordType
	if err := db.Where("key = ?", typeKey).First(&ty).Error; err != nil {
		t.Fatalf("read record type %q: %v", typeKey, err)
	}
	return string(ty.Capabilities)
}

// eoAdminEditedCapabilityBlob is a REAL edit an administrator might make: the
// seeded decisions_base config with max_refs narrowed. It is valid JSON of
// the shape the engine decodes, so nothing else in the pilot objects to it,
// and it differs from the registry value in exactly one key.
const eoAdminEditedCapabilityBlob = `[{"key":"decisions_base","config":{"base_type_key":"eo_decision_record","subject_key":"keywords","standing_key":"standing","standing_values":["in_force"],"max_refs":5}}]`

// TestEoSeedDoesNotRevertAnAdministratorsCapabilityEdit is the behaviour this
// task restores.
//
// Plan 2's fix wave made `enable_capabilities` convergent so a record type
// appended later still gets its capability configuration — the right fix for
// a real gap. But its gate asked "does the stored blob differ from the
// registry's", and PUT /api/v1/admin/eo-record-types/:id writes exactly that
// column, so from that wave onwards an administrator's edit was silently
// reverted on the next restart. The wave's own author wrote the rule for the
// sibling step and never applied it here: a deliberate administrative act is
// not drift to be corrected.
//
// IT DRIVES THE REAL seedEOPilot, never eoEnableCapabilities. The GATE is
// what shipped the regression; a test that called the writer would prove the
// writer behaves while being structurally unable to see that the gate opens
// on an edited column.
//
// Mutation that makes it fail: make eoCapabilitiesPending compare the stored
// blob with the registry again, or drop the never-configured check from
// eoEnableCapabilities.
func TestEoSeedDoesNotRevertAnAdministratorsCapabilityEdit(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	var ty models.EORecordType
	if err := db.Where("key = ?", eoDraftStudyTypeKey).First(&ty).Error; err != nil {
		t.Fatalf("read %q: %v", eoDraftStudyTypeKey, err)
	}
	seeded := string(ty.Capabilities)
	if eoJSONEqual(t, seeded, eoAdminEditedCapabilityBlob) {
		t.Fatal("fixture error: the edit is the seeded value, so nothing below could tell a revert from a no-op")
	}

	eoAdminEditsCapabilities(t, db, ty.ID, eoAdminEditedCapabilityBlob)

	// The gate must be SHUT on an edited column. If it opens, the seed
	// transaction runs on every boot and the assertion below is the only
	// thing between the administrator and a rewrite.
	open, err := eoCapabilitiesPending(db)
	if err != nil {
		t.Fatalf("ask the gate: %v", err)
	}
	if open {
		t.Error("the convergent step reports work to do on a column an administrator edited; it would rewrite it on every boot")
	}

	seedEOPilot(db)

	if got := eoCapabilitiesOf(t, db, eoDraftStudyTypeKey); !eoJSONEqual(t, got, eoAdminEditedCapabilityBlob) {
		t.Errorf("CLOBBERED: capabilities = %s, want the administrator's %s", got, eoAdminEditedCapabilityBlob)
	}
}

// TestEoSeedStillConvergesATypeNeverConfigured pins the Plan 2 behaviour this
// task must NOT regress, and pins it in the SAME BOOT as an administrator's
// edit — because a "fix" that simply stopped converging would satisfy the
// test above on its own.
//
// The never-configured state is reached by clearing the column to the value
// models.EORecordType declares as its default (`'[]'::jsonb`), which is
// exactly what a record type appended by a later step arrives with. That is
// the state `enable_capabilities` exists to fix, so it is the state the gate
// must open on.
//
// Mutation that makes it fail: make eoCapabilitiesPending ignore an empty
// column, or make eoEnableCapabilities skip every type that is not byte-equal
// to the registry.
func TestEoSeedStillConvergesATypeNeverConfigured(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	var never, edited models.EORecordType
	if err := db.Where("key = ?", eoIncomingTypeKey).First(&never).Error; err != nil {
		t.Fatalf("read %q: %v", eoIncomingTypeKey, err)
	}
	if err := db.Where("key = ?", eoDraftStudyTypeKey).First(&edited).Error; err != nil {
		t.Fatalf("read %q: %v", eoDraftStudyTypeKey, err)
	}
	wantConverged := string(never.Capabilities)
	if strings.TrimSpace(wantConverged) == "" || wantConverged == "[]" {
		t.Fatalf("fixture error: %q carries no seeded capabilities (%q), so convergence cannot be observed", eoIncomingTypeKey, wantConverged)
	}

	// One type has never been configured; the other carries an
	// administrator's edit. Both states, one boot.
	eoAdminEditsCapabilities(t, db, never.ID, `[]`)
	eoAdminEditsCapabilities(t, db, edited.ID, eoAdminEditedCapabilityBlob)

	open, err := eoCapabilitiesPending(db)
	if err != nil {
		t.Fatalf("ask the gate: %v", err)
	}
	if !open {
		t.Fatal("the convergent step is shut on a type with no capability configuration, so a record type added later would never get one")
	}

	seedEOPilot(db)

	if got := eoCapabilitiesOf(t, db, eoIncomingTypeKey); !eoJSONEqual(t, got, wantConverged) {
		t.Errorf("the never-configured type was not converged: capabilities = %s, want %s", got, wantConverged)
	}
	if got := eoCapabilitiesOf(t, db, eoDraftStudyTypeKey); !eoJSONEqual(t, got, eoAdminEditedCapabilityBlob) {
		t.Errorf("CLOBBERED in the same boot that converged the other type: capabilities = %s, want %s", got, eoAdminEditedCapabilityBlob)
	}

	// And it terminates: with nothing left unconfigured the gate shuts, even
	// though one column still differs from the registry.
	open, err = eoCapabilitiesPending(db)
	if err != nil {
		t.Fatalf("ask the gate again: %v", err)
	}
	if open {
		t.Error("the gate is still open after the convergence: the seed transaction would run on every boot for ever")
	}
}

// TestEoReportPilotObligationsLogsAnAdministratorEditedCapability is how an
// operator discovers the case this task DECIDES NOT TO HANDLE: a release that
// changes a seeded capability config does not reach a type whose column has
// been edited, and nothing else would say so.
//
// Mutation that makes it fail: drop
// eoLogCapabilitiesAnAdministratorHasEdited from eoReportPilotObligations.
func TestEoReportPilotObligationsLogsAnAdministratorEditedCapability(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)

	var ty models.EORecordType
	if err := db.Where("key = ?", eoDraftStudyTypeKey).First(&ty).Error; err != nil {
		t.Fatalf("read %q: %v", eoDraftStudyTypeKey, err)
	}
	// A clean database says nothing: a line printed when nothing is wrong is
	// a line nobody reads.
	if out := eoCaptureSeed(t, db); strings.Contains(out, "differs from the configuration") {
		t.Errorf("the report fires on an unedited database:\n%s", out)
	}

	eoAdminEditsCapabilities(t, db, ty.ID, eoAdminEditedCapabilityBlob)

	out := eoCaptureSeed(t, db)
	if !strings.Contains(out, eoDraftStudyTypeKey) || !strings.Contains(out, "differs from the configuration") {
		t.Errorf("the boot report does not name the type whose capabilities diverge from the registry; a release that changes the seeded config would land nowhere and say nothing.\n--- output ---\n%s", out)
	}
}
