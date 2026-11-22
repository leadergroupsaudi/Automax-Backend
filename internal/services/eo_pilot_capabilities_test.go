package services

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// ── Task A6's capability enablement, in front of the engine that reads it ───
//
// WHY THESE TESTS LIVE HERE. Everything Task A6 turns on is read by a
// function in THIS package — CapabilitiesFor (the registry lookup and the
// strict decode), eoCapabilitiesCheckConfig (the reserved-key refusal and the
// transition-code check), Derive (what the capability writes) — and package
// database cannot import package services. A test over there can assert only
// that a jsonb column holds the text somebody typed into the seed; THESE are
// the ones that show the seeded blob being accepted, and the guard refusing
// the configuration it exists to refuse.
//
// The configuration under test is the SEEDED one, through
// database.SeedEOPilot, never a hand-built copy.

// eoPilotRegisterCapabilities puts the process-wide registry into the state
// cmd/server/main.go puts it in, for the duration of one test.
//
// IT IS NOT OPTIONAL SCAFFOLDING. From Task A6 the seeded configuration
// NAMES capabilities, and CapabilitiesFor returns ErrEOUnknownCapability for
// a key nothing registered — deliberately, so configuration naming a
// capability that will never run fails loudly. Without this call every create
// and every transition on the Draft Decision Study and on Incoming
// Correspondence fails, in this file and in the two older pilot fixtures.
//
// The registry is SEALED by RegisterEOCapabilities, so it is replaced rather
// than added to, and restored afterwards — the pattern eo_capability_test.go
// established.
func eoPilotRegisterCapabilities(t *testing.T) {
	t.Helper()
	saved := eoDefaultCapabilities
	eoDefaultCapabilities = newEOCapabilityRegistry()
	t.Cleanup(func() { eoDefaultCapabilities = saved })
	RegisterEOCapabilities()
}

// TestEoPilotCapabilityConfigsResolve is addendum item 6: CapabilitiesFor on
// EVERY seeded type, asserting no error.
//
// WHAT IT CATCHES THAT NOTHING ELSE DOES. The capabilities list and every
// capability's own config are decoded with DisallowUnknownFields, so a stray
// or misspelled JSON key — `base_type` for `base_type_key`, `transition` for
// `transitions` — is a hard error. It is raised at the FIRST REQUEST against
// the type, not at seed time, so a typo ships looking healthy and the type is
// dead on arrival. This is the test that moves that failure to CI.
//
// Mutation that makes it fail: misspell any key of any config in
// database.eoPilotCapabilities, or name a capability key nothing registers.
func TestEoPilotCapabilityConfigsResolve(t *testing.T) {
	f := eoWfNewFixture(t)

	enabled := 0
	for _, key := range eoPilotTypeKeysUnderTest {
		key := key
		t.Run(key, func(t *testing.T) {
			ty, err := f.types.GetByKey(context.Background(), key)
			if err != nil || ty == nil {
				t.Fatalf("read record type %q: %v", key, err)
			}
			caps, err := CapabilitiesFor(ty)
			if err != nil {
				t.Fatalf("CapabilitiesFor(%q): %v", key, err)
			}
			// A capability whose Config cannot be parsed resolves fine and
			// fails at the first hook, so the configs are parsed here too —
			// through the only door the interface offers, ReservedKeys, which
			// every one of the three implements by parsing its config first.
			for _, c := range caps {
				if _, err := c.ReservedKeys(c.Config); err != nil {
					t.Errorf("capability %q on %q: its config does not parse: %v", c.Key(), key, err)
				}
			}
			if len(caps) > 0 {
				enabled++
			}
		})
	}
	if enabled == 0 {
		t.Fatal("no seeded type enables a capability, so this test resolved nothing; Task A6 enables decisions_base and referral_tree")
	}
}

// TestEoPilotCapabilityTransitionCodesExistInTheWorkflow is Review Focus 2.
//
// A capability config naming a transition code the BOUND workflow does not
// have surfaces as "every transition on this type fails", long after seeding
// — eoCapabilitiesCheckConfig calls ValidateWorkflow on the transition path
// and nowhere else. The first half below runs that check over every seeded
// type against its OWN seeded workflow. The second half proves the check can
// refuse, with a deliberately typo'd code, against a REAL seeded workflow —
// so a first half that passes because it inspected nothing is visible.
//
// Mutation that makes it fail: change the transition list of any capability
// config in database.eoPilotCapabilities to a code its workflow lacks (the
// first half), or make eoApprovalCapability.ValidateWorkflow return nil (the
// second).
func TestEoPilotCapabilityTransitionCodesExistInTheWorkflow(t *testing.T) {
	f := eoWfNewFixture(t)
	ctx := context.Background()

	// How many SEEDED configs name a transition code at all. It is asserted
	// rather than logged: today it is zero, because the two capabilities Task
	// A6 enables (decisions_base, referral_tree) both embed
	// eoConstrainsNoWorkflow and the one that does name codes — approval —
	// is enabled on no type (Task A6's report, addendum item 4). The loop
	// above is therefore a guard that binds the moment a config names a code,
	// in the same position Task A4's blocked-transition guard was in before
	// Task A5 seeded the workflows. The NUMBER is what stops that being
	// invisible: enable approval and this test fails until the number is
	// updated, which is the moment somebody reads the loop.
	namingCodes := 0

	for _, key := range eoPilotTypeKeysUnderTest {
		key := key
		t.Run(key, func(t *testing.T) {
			ty, err := f.types.GetByKey(ctx, key)
			if err != nil || ty == nil {
				t.Fatalf("read record type %q: %v", key, err)
			}
			caps, err := CapabilitiesFor(ty)
			if err != nil {
				t.Fatalf("CapabilitiesFor(%q): %v", key, err)
			}
			if len(caps) == 0 {
				return
			}
			wf, err := ResolveWorkflowForType(ctx, ty.ID, f.wfRepo)
			if err != nil || wf == nil {
				t.Fatalf("record type %q resolves no workflow: %v", key, err)
			}
			if len(wf.Transitions) == 0 {
				t.Fatalf("workflow %q has no transitions loaded, so the check below could never refuse anything", wf.Code)
			}
			for _, c := range caps {
				if err := c.ValidateWorkflow(c.Config, wf); err != nil {
					t.Errorf("capability %q on %q names something workflow %q does not have: %v", c.Key(), key, wf.Code, err)
				}
				if eoPilotConfigNamesTransitions(c.Config) {
					namingCodes++
				}
			}
		})
	}

	t.Run("a typo'd transition code is caught, against a real seeded workflow", func(t *testing.T) {
		ty, err := f.types.GetByKey(ctx, eoPilotOutgoingTypeKey)
		if err != nil || ty == nil {
			t.Fatalf("read record type %q: %v", eoPilotOutgoingTypeKey, err)
		}
		wf, err := ResolveWorkflowForType(ctx, ty.ID, f.wfRepo)
		if err != nil || wf == nil {
			t.Fatalf("record type %q resolves no workflow: %v", eoPilotOutgoingTypeKey, err)
		}
		real := ""
		for _, tr := range wf.Transitions {
			if tr.Code == "approve" {
				real = tr.Code
			}
		}
		if real == "" {
			t.Fatalf("workflow %q has no `approve` transition; this subtest's premise is gone", wf.Code)
		}
		approval := NewEOApprovalCapability()
		if err := approval.ValidateWorkflow(datatypes.JSON(`{"transitions":["approve"]}`), wf); err != nil {
			t.Errorf("a config naming the real transition %q was refused: %v", real, err)
		}
		err = approval.ValidateWorkflow(datatypes.JSON(`{"transitions":["aprove"]}`), wf)
		if err == nil {
			t.Fatal("a config naming the transition code \"aprove\" was accepted; every transition on the type would then fail at the first request")
		}
		if !strings.Contains(err.Error(), "aprove") || !strings.Contains(err.Error(), wf.Code) {
			t.Errorf("the refusal %q names neither the bad code nor the workflow, so nobody could find it", err)
		}
	})

	t.Run("how many seeded configs name a transition code", func(t *testing.T) {
		if namingCodes != 0 {
			t.Errorf("%d seeded capability configs name transition codes, and this test still says 0. "+
				"That number is the whole of what makes the loop above binding: update it, and check the loop ran over the config you added.", namingCodes)
		}
	})
}

// eoPilotConfigNamesTransitions reports whether a capability config carries a
// non-empty `transitions` list. It decodes loosely on purpose: the question is
// "does this blob name transition codes", not "is it valid".
func eoPilotConfigNamesTransitions(cfg datatypes.JSON) bool {
	var probe struct {
		Transitions []string `json:"transitions"`
	}
	if err := json.Unmarshal(cfg, &probe); err != nil {
		return false
	}
	return len(probe.Transitions) > 0
}

// TestEoPilotDraftDecisionStudyIsUsableWithDecisionsBase is the BLOCKING
// item's proof, and it is the pair of halves that matters.
//
// FIRST: a Draft Decision Study is created through the real pipeline, on the
// real seeded configuration, with decisions_base enabled — which is the first
// request against the type, and therefore the moment
// eoCapabilitiesCheckConfig runs. It passes only because the step that
// enabled the capability also removed A3's `related_refs` field definition.
//
// SECOND: the field definition is put back and the same create is refused,
// naming the key. Without this half the first half would pass just as well on
// a type that enables nothing at all, and the removal would be proved to be
// load-bearing by nothing.
//
// Mutation that makes it fail: drop the removal from
// database.eoEnableCapabilities (the first half), or make
// decisions_base.ReservedKeys return nil (the second).
func TestEoPilotDraftDecisionStudyIsUsableWithDecisionsBase(t *testing.T) {
	f := eoWfNewFixture(t)

	ty, err := f.types.GetByKey(context.Background(), eoPilotDraftTypeKey)
	if err != nil || ty == nil {
		t.Fatalf("read record type %q: %v", eoPilotDraftTypeKey, err)
	}
	caps, err := CapabilitiesFor(ty)
	if err != nil {
		t.Fatalf("CapabilitiesFor(%q): %v", eoPilotDraftTypeKey, err)
	}
	found := ""
	for _, c := range caps {
		if c.Key() == EODecisionsBaseKey {
			found = c.Key()
		}
	}
	if found == "" {
		t.Fatalf("record type %q enables %d capabilities and none of them is %q; the rest of this test would prove nothing",
			eoPilotDraftTypeKey, len(caps), EODecisionsBaseKey)
	}

	t.Run("the type is usable: a draft is created and the capability writes its key", func(t *testing.T) {
		rec := f.create(t, eoPilotDraftTypeKey, nil, "", nil)
		values := map[string]any{}
		if err := json.Unmarshal(rec.Values, &values); err != nil {
			t.Fatalf("decode the saved values: %v", err)
		}
		got, present := values["related_refs"]
		if !present {
			t.Fatalf("the capability wrote no %q on %s; its own contract is an empty list, never an absent key", "related_refs", rec.Reference)
		}
		list, ok := got.([]any)
		if !ok {
			t.Fatalf("%q is %T, want a list", "related_refs", got)
		}
		// The base is empty and no subject taxonomy exists yet, so the honest
		// answer is an empty list. See the Task B2 carry-forward.
		if len(list) != 0 {
			t.Errorf("%q is %v on a database with no Decision Record; want an empty list", "related_refs", list)
		}
	})

	t.Run("the removal is load-bearing: the declaration put back refuses the type", func(t *testing.T) {
		def := models.EOFieldDef{
			ID:           uuid.New(),
			RecordTypeID: ty.ID,
			Key:          "related_refs",
			Label:        "Related references",
			LabelAr:      "Related references",
			Kind:         models.EOKindMultiLink,
			Provenance:   models.EOProvenanceAuto,
			RequiredMode: models.EORequiredNever,
			SortOrder:    900,
		}
		if err := f.db.Create(&def).Error; err != nil {
			t.Fatalf("put the field definition back: %v", err)
		}
		t.Cleanup(func() { f.db.Unscoped().Delete(&models.EOFieldDef{}, "id = ?", def.ID) })

		values := f.eoWfValues(t, eoPilotDraftTypeKey)
		_, err := f.records.Create(context.Background(), &models.EORecordCreateRequest{
			RecordTypeID: ty.ID, Values: values,
		}, f.actor)
		if err == nil {
			t.Fatal("a Draft Decision Study was created with related_refs declared AND reserved; the guard that makes the removal necessary did not fire")
		}
		if !strings.Contains(err.Error(), "related_refs") {
			t.Errorf("the refusal %q does not name the key, so nobody meeting it could act on it", err)
		}
		// It must be THE reserved-key guard and not some other refusal that
		// happens to mention the key — a create can fail for a dozen reasons.
		if !strings.Contains(err.Error(), "must not be defined") || !errors.Is(err, ErrEOCapabilityConfig) {
			t.Errorf("the refusal %q is not eoCapabilitiesCheckConfig's reserved-key refusal", err)
		}
	})
}

// TestEoPilotIncomingCorrespondenceIsUsableWithReferralTree is the same pair
// for Task B3's capability: the letter is created and saved with
// referral_tree enabled, and the consolidation the Office reads is written
// from the letter's real referral children.
//
// It is NOT a second copy of B3's own tests. B3 proved the tree builder over
// hand-made branches; this proves the SEEDED config reaches it — the branch
// type key resolves to the seeded Referral type, and the key the capability
// writes is one no seeded field definition claims.
//
// Mutation that makes it fail: change `branch_type_key` in
// database.eoPilotCapabilities to a type key that is not seeded (the create
// then fails, because an unresolvable branch type is an error and never an
// empty tree).
func TestEoPilotIncomingCorrespondenceIsUsableWithReferralTree(t *testing.T) {
	f := eoWfNewFixture(t)

	ty, err := f.types.GetByKey(context.Background(), eoPilotIncomingTypeKey)
	if err != nil || ty == nil {
		t.Fatalf("read record type %q: %v", eoPilotIncomingTypeKey, err)
	}
	caps, err := CapabilitiesFor(ty)
	if err != nil {
		t.Fatalf("CapabilitiesFor(%q): %v", eoPilotIncomingTypeKey, err)
	}
	found := false
	for _, c := range caps {
		if c.Key() == EOReferralTreeKey {
			found = true
		}
	}
	if !found {
		t.Fatalf("record type %q does not enable %q; the rest of this test would prove nothing", eoPilotIncomingTypeKey, EOReferralTreeKey)
	}

	letter := f.create(t, eoPilotIncomingTypeKey, nil, "", nil)
	tree := eoPilotTree(t, letter, "a letter with no referral")
	if tree["complete"] != true {
		t.Errorf("a letter with no branch is %v, want complete", tree["complete"])
	}

	branch := f.create(t, eoPilotReferralTypeKey, letter, eoPilotReferralLinkKey, nil)
	saved, err := f.records.Update(context.Background(), letter.ID, &models.EORecordUpdateRequest{
		RecordTypeID: letter.RecordTypeID,
		Values:       map[string]any{"subject_ar": "اختبار"},
	}, f.actor)
	if err != nil {
		t.Fatalf("save the letter again: %v", err)
	}
	tree = eoPilotTree(t, saved, "a letter with one open referral")
	branches, _ := tree["branches"].([]any)
	if len(branches) != 1 {
		t.Fatalf("%d branches, want 1 (%v)", len(branches), tree)
	}
	first, _ := branches[0].(map[string]any)
	if first["reference"] != branch.Reference {
		t.Errorf("branch reference %v, want %q", first["reference"], branch.Reference)
	}
	if tree["complete"] != false {
		t.Errorf("a letter with one OPEN branch is %v, want incomplete", tree["complete"])
	}
}

func eoPilotTree(t *testing.T, rec *models.EORecord, what string) map[string]any {
	t.Helper()
	values := map[string]any{}
	if err := json.Unmarshal(rec.Values, &values); err != nil {
		t.Fatalf("%s: decode the saved values: %v", what, err)
	}
	tree, ok := values["referral_tree"].(map[string]any)
	if !ok {
		t.Fatalf("%s: %s carries no referral_tree object (%v)", what, rec.Reference, values["referral_tree"])
	}
	return tree
}
