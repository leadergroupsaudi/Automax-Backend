package database

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/automax/backend/internal/models"
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

// TestEoPerTypePermissionDescriptionsSayNothingEnforcesThem is the addendum's
// item 3. Nothing consults eo:<type_key>:view|create|update — every EO route
// is gated by the MODULE-level codes and row-level visibility is the
// repository's §7.11 clause — so an administrator ticking one of these boxes
// is expressing an intention, not applying a control. The text they read must
// say so; it is the only place the person meeting the code is told.
//
// Mutation that makes it fail: restore the old wording, "%s executive-office
// records of type %s (%s)", in eoSeedTypePermissions.
func TestEoPerTypePermissionDescriptionsSayNothingEnforcesThem(t *testing.T) {
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
	if len(eoTypePermissionInertEN) < 20 || len(eoTypePermissionInertAR) < 20 {
		t.Fatalf("the inert-code sentences are %d and %d characters; substring assertions against a near-empty needle prove nothing",
			len(eoTypePermissionInertEN), len(eoTypePermissionInertAR))
	}
	if !strings.Contains(strings.ToLower(eoTypePermissionInertEN), "not yet enforced") {
		t.Errorf("the English sentence %q does not tell the administrator the code is unenforced", eoTypePermissionInertEN)
	}
	for _, p := range perms {
		t.Run(p.Code, func(t *testing.T) {
			if !strings.Contains(p.Description, eoTypePermissionInertEN) {
				t.Errorf("Description %q does not carry %q", p.Description, eoTypePermissionInertEN)
			}
			if !strings.Contains(p.DescriptionAr, eoTypePermissionInertAR) {
				t.Errorf("DescriptionAr %q does not carry %q", p.DescriptionAr, eoTypePermissionInertAR)
			}
		})
	}
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

// TestEoCapabilityEnablementSurvivesASecondRun is the idempotency shape the
// rest of the seed carries: the step's applied row stops it re-running, and a
// run that did re-run must not double the blob or resurrect the removed field.
func TestEoCapabilityEnablementSurvivesASecondRun(t *testing.T) {
	db := eoPilotDB(t)
	seedEOPilot(db)
	first := eoCapabilityBlobs(t, db)
	seedEOPilot(db)
	second := eoCapabilityBlobs(t, db)
	for key, blob := range first {
		if second[key] != blob {
			t.Errorf("record type %q: capabilities changed on the second run:\n got %s\nwant %s", key, second[key], blob)
		}
	}
}

func eoCapabilityBlobs(t *testing.T, db *gorm.DB) map[string]string {
	t.Helper()
	var types []models.EORecordType
	if err := db.Where("is_abstract = ?", false).Order("key").Find(&types).Error; err != nil {
		t.Fatalf("read concrete record types: %v", err)
	}
	out := map[string]string{}
	for _, ty := range types {
		out[ty.Key] = string(ty.Capabilities)
	}
	return out
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
