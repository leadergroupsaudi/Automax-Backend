package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// ── fixtures ────────────────────────────────────────────────────────────────

// eoDecBaseReader stands in for the reader the engine hands each hook. It
// EMBEDS the interface and overrides only RecordsOfType — the one method this
// capability calls — which is the convention for repository-shaped fakes: a
// fake must not become more capable than the thing it stands in for. (The note
// at eoReadCap explains why CAPABILITY fakes go the other way; this is not
// one.)
type eoDecBaseReader struct {
	EOCapabilityReader
	// base is record type key -> the records of that type, in the order the
	// repository returns them.
	base map[string][]models.EORecord
	// asked records every key the capability read, so a test can prove the
	// capability did NOT read as well as that it did.
	asked []string
	err   error
}

func (r *eoDecBaseReader) RecordsOfType(_ context.Context, key string) ([]models.EORecord, error) {
	r.asked = append(r.asked, key)
	if r.err != nil {
		return nil, r.err
	}
	recs, ok := r.base[key]
	if !ok {
		// Same contract as Children: a key that names no type is an error,
		// never an empty result.
		return nil, fmt.Errorf("no record type with key %q", key)
	}
	return recs, nil
}

// eoDecBaseTypeKey is the standing base in these tests; eoDecBaseDraftKey is
// the draft's own type. They are the pilot's keys, but nothing in the
// capability knows them: they arrive as configuration.
const (
	eoDecBaseTypeKey  = "eo_decision_record"
	eoDecBaseDraftKey = "eo_draft_decision_study"
)

// eoDecBaseRec builds a stored record with these values. CreatedAt is never
// set in Go.
func eoDecBaseRec(t *testing.T, reference string, values map[string]any) models.EORecord {
	t.Helper()
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatalf("encode values of %s: %v", reference, err)
	}
	return models.EORecord{ID: uuid.New(), Reference: reference, Values: datatypes.JSON(raw)}
}

// eoDecBaseConfig encodes a capability config blob.
func eoDecBaseConfig(t *testing.T, cfg map[string]any) datatypes.JSON {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("encode config: %v", err)
	}
	return datatypes.JSON(raw)
}

// eoDecBaseStdConfig is the pilot's configuration: the standing Decision
// Records, filed under the subject taxonomy both sides share.
func eoDecBaseStdConfig(t *testing.T) datatypes.JSON {
	t.Helper()
	return eoDecBaseConfig(t, map[string]any{
		"base_type_key":   eoDecBaseTypeKey,
		"subject_key":     "keywords",
		"standing_key":    "standing",
		"standing_values": []string{"in_force"},
	})
}

// eoDecBaseRefs turns what Derive produced for the target key into the
// REFERENCES of the records it names, so a failure reads as decision numbers
// rather than as uuids.
func eoDecBaseRefs(t *testing.T, derived map[string]any, target string, base []models.EORecord) []string {
	t.Helper()
	raw, ok := derived[target]
	if !ok {
		t.Fatalf("Derive wrote no %q at all; keys = %v", target, eoDecBaseKeys(derived))
	}
	byID := map[string]string{}
	for _, r := range base {
		byID[r.ID.String()] = r.Reference
	}
	var ids []string
	switch x := raw.(type) {
	case []string:
		ids = x
	case []any:
		for _, v := range x {
			s, ok := v.(string)
			if !ok {
				t.Fatalf("%s holds %T, want a record id string", target, v)
			}
			ids = append(ids, s)
		}
	default:
		t.Fatalf("%s holds %T, want a list of record ids", target, raw)
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		ref, ok := byID[id]
		if !ok {
			t.Fatalf("%s names %s, which is not a record of the base", target, id)
		}
		out = append(out, ref)
	}
	return out
}

func eoDecBaseKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ── the matching rule ───────────────────────────────────────────────────────

// TestDecisionsBasePopulatesRelatedRefs pins the matching rule: a standing
// record of the configured base whose subject terms overlap the draft's is
// linked, and nothing else is.
func TestDecisionsBasePopulatesRelatedRefs(t *testing.T) {
	ctx := context.Background()
	capability := NewEODecisionsBaseCapability()
	cfg := eoDecBaseStdConfig(t)

	restricted := uuid.New().String()
	base := []models.EORecord{
		eoDecBaseRec(t, "DEC-2026-001", map[string]any{"keywords": []string{"water_tariff", "utilities"}, "standing": "in_force"}),
		eoDecBaseRec(t, "DEC-2026-002", map[string]any{"keywords": []string{"roads"}, "standing": "in_force"}),
		// Case and surrounding space are the officer's typing, not a
		// different subject.
		eoDecBaseRec(t, "DEC-2026-003", map[string]any{"keywords": []string{" Water_Tariff "}, "standing": "in_force"}),
		// Not standing: superseded and revoked decisions are history.
		eoDecBaseRec(t, "DEC-2026-004", map[string]any{"keywords": []string{"water_tariff"}, "standing": "superseded"}),
		// Restricted under the live §7.11 control: never pulled up.
		eoDecBaseRec(t, "DEC-2026-005", map[string]any{"keywords": []string{"water_tariff"}, "standing": "in_force", "visible_to": restricted}),
		// Filed under nothing at all — today's state of the whole base.
		eoDecBaseRec(t, "DEC-2026-006", map[string]any{"standing": "in_force"}),
		// Standing is missing: the filter is positive, so it is excluded.
		eoDecBaseRec(t, "DEC-2026-007", map[string]any{"keywords": []string{"water_tariff"}}),
	}

	for _, tc := range []struct {
		name  string
		draft map[string]any
		want  []string
	}{
		{
			name:  "a shared term pulls the standing record up",
			draft: map[string]any{"keywords": []string{"water_tariff"}},
			want:  []string{"DEC-2026-001", "DEC-2026-003"},
		},
		{
			name:  "several terms are a union, in the base's own order",
			draft: map[string]any{"keywords": []string{"roads", "utilities"}},
			want:  []string{"DEC-2026-001", "DEC-2026-002"},
		},
		{
			name:  "a term nothing is filed under pulls nothing up",
			draft: map[string]any{"keywords": []string{"fisheries"}},
			want:  []string{},
		},
		{
			name:  "the draft's own case and spacing do not change the match",
			draft: map[string]any{"keywords": []string{"  WATER_TARIFF"}},
			want:  []string{"DEC-2026-001", "DEC-2026-003"},
		},
		{
			name:  "a single term may be stored unwrapped",
			draft: map[string]any{"keywords": "roads"},
			want:  []string{"DEC-2026-002"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := &eoDecBaseReader{base: map[string][]models.EORecord{eoDecBaseTypeKey: base}}
			rec := eoDecBaseRec(t, "DRF-2026-010", tc.draft)
			got, err := capability.Derive(ctx, read, &rec, cfg)
			if err != nil {
				t.Fatalf("derive: %v", err)
			}
			if refs := eoDecBaseRefs(t, got, "related_refs", base); !reflect.DeepEqual(refs, tc.want) {
				t.Errorf("related_refs = %v, want %v", refs, tc.want)
			}
			if want := []string{eoDecBaseTypeKey}; !reflect.DeepEqual(read.asked, want) {
				t.Errorf("read %v, want exactly one read of %v", read.asked, want)
			}
		})
	}

	t.Run("a record is never linked to itself", func(t *testing.T) {
		self := eoDecBaseRec(t, "DEC-2026-008", map[string]any{"keywords": []string{"water_tariff"}, "standing": "in_force"})
		withSelf := append([]models.EORecord{self}, base...)
		read := &eoDecBaseReader{base: map[string][]models.EORecord{eoDecBaseTypeKey: withSelf}}
		got, err := capability.Derive(ctx, read, &self, cfg)
		if err != nil {
			t.Fatalf("derive: %v", err)
		}
		want := []string{"DEC-2026-001", "DEC-2026-003"}
		if refs := eoDecBaseRefs(t, got, "related_refs", withSelf); !reflect.DeepEqual(refs, want) {
			t.Errorf("related_refs = %v, want %v", refs, want)
		}
	})

	t.Run("the list is capped and the cap is configurable", func(t *testing.T) {
		read := &eoDecBaseReader{base: map[string][]models.EORecord{eoDecBaseTypeKey: base}}
		rec := eoDecBaseRec(t, "DRF-2026-011", map[string]any{"keywords": []string{"water_tariff"}})
		got, err := capability.Derive(ctx, read, &rec, eoDecBaseConfig(t, map[string]any{
			"base_type_key":   eoDecBaseTypeKey,
			"subject_key":     "keywords",
			"standing_key":    "standing",
			"standing_values": []string{"in_force"},
			"max_refs":        1,
		}))
		if err != nil {
			t.Fatalf("derive: %v", err)
		}
		if refs := eoDecBaseRefs(t, got, "related_refs", base); !reflect.DeepEqual(refs, []string{"DEC-2026-001"}) {
			t.Errorf("related_refs = %v, want the first match only", refs)
		}
	})

	t.Run("the base and the subject key are configuration, not constants", func(t *testing.T) {
		other := []models.EORecord{
			eoDecBaseRec(t, "POL-2026-001", map[string]any{"topics": []string{"ports"}, "state": "live"}),
		}
		read := &eoDecBaseReader{base: map[string][]models.EORecord{"eo_policy": other}}
		rec := eoDecBaseRec(t, "DRF-2026-012", map[string]any{"subjects": []string{"ports"}})
		got, err := capability.Derive(ctx, read, &rec, eoDecBaseConfig(t, map[string]any{
			"base_type_key":   "eo_policy",
			"subject_key":     "subjects",
			"match_key":       "topics",
			"standing_key":    "state",
			"standing_values": []string{"live"},
			"target_key":      "eo_related",
		}))
		if err != nil {
			t.Fatalf("derive: %v", err)
		}
		if refs := eoDecBaseRefs(t, got, "eo_related", other); !reflect.DeepEqual(refs, []string{"POL-2026-001"}) {
			t.Errorf("eo_related = %v, want the matching policy", refs)
		}
	})

	t.Run("a base that does not exist is an error, not an empty list", func(t *testing.T) {
		read := &eoDecBaseReader{base: map[string][]models.EORecord{}}
		rec := eoDecBaseRec(t, "DRF-2026-013", map[string]any{"keywords": []string{"water_tariff"}})
		if _, err := capability.Derive(ctx, read, &rec, cfg); err == nil {
			t.Fatal("want an error naming the missing base type")
		}
	})
}

// ── the verdict stays the officer's ─────────────────────────────────────────

// TestDecisionsBaseNeverWritesTheVerdict proves it by DIFFING, not by naming
// the key: every hook runs, and any values key other than the one the
// capability owns that moved is a failure. A future edit that started writing
// conflict_verdict — or anything else — fails this without the test being
// changed.
func TestDecisionsBaseNeverWritesTheVerdict(t *testing.T) {
	ctx := context.Background()
	capability := NewEODecisionsBaseCapability()
	cfg := eoDecBaseStdConfig(t)
	base := []models.EORecord{
		eoDecBaseRec(t, "DEC-2026-001", map[string]any{"keywords": []string{"water_tariff"}, "standing": "in_force"}),
	}

	t.Run("Derive returns the one key the capability owns and no other", func(t *testing.T) {
		read := &eoDecBaseReader{base: map[string][]models.EORecord{eoDecBaseTypeKey: base}}
		rec := eoDecBaseRec(t, "DRF-2026-020", map[string]any{"keywords": []string{"water_tariff"}})
		got, err := capability.Derive(ctx, read, &rec, cfg)
		if err != nil {
			t.Fatalf("derive: %v", err)
		}
		if keys := eoDecBaseKeys(got); len(keys) != 1 || keys[0] != "related_refs" {
			t.Errorf("Derive wrote %v, want exactly [related_refs]", keys)
		}
	})

	t.Run("every hook leaves every value but that one exactly as it was", func(t *testing.T) {
		read := &eoDecBaseReader{base: map[string][]models.EORecord{eoDecBaseTypeKey: base}}
		// The officer has judged the verdict already; the draft also carries
		// an earlier related_refs, which IS the capability's to replace.
		before := map[string]any{
			"keywords":         []string{"water_tariff"},
			"conflict_verdict": "conflicts",
			"related_refs":     []string{uuid.New().String()},
			"grounds":          "the tariff was set in 2019",
		}
		rec := eoDecBaseRec(t, "DRF-2026-021", before)
		was := eoDecodeValues(rec.Values)

		if _, err := capability.Validate(ctx, read, &rec, cfg); err != nil {
			t.Fatalf("validate: %v", err)
		}
		if err := capability.OnTransition(ctx, read, &rec, &models.WorkflowTransition{Code: "submit"}, cfg); err != nil {
			t.Fatalf("on transition: %v", err)
		}
		if _, err := capability.Timers(ctx, read, &rec, cfg); err != nil {
			t.Fatalf("timers: %v", err)
		}
		derived, err := capability.Derive(ctx, read, &rec, cfg)
		if err != nil {
			t.Fatalf("derive: %v", err)
		}
		// Derived values reach the record the way the engine applies them.
		if errs := eoApplyValues(&rec, derived); len(errs) > 0 {
			t.Fatalf("apply derived values: %v", errs)
		}

		owned, err := capability.ReservedKeys(cfg)
		if err != nil {
			t.Fatalf("reserved keys: %v", err)
		}
		isOwned := map[string]bool{}
		for _, k := range owned {
			isOwned[k] = true
		}
		now := eoDecodeValues(rec.Values)
		for k, v := range now {
			if isOwned[k] {
				continue
			}
			if !reflect.DeepEqual(was[k], v) {
				t.Errorf("the capability changed %q from %#v to %#v; it owns only %v", k, was[k], v, owned)
			}
		}
		for k, v := range was {
			if _, still := now[k]; !still && !isOwned[k] {
				t.Errorf("the capability removed %q (%#v)", k, v)
			}
		}
	})

	// A source-level test, in the shape of the registration-order one in
	// eo_capability_test.go. The behavioural test above can only catch a write
	// on the paths it drives; this catches the key being named at all —
	// including on a path nothing yet calls.
	t.Run("the capability's source never names the verdict", func(t *testing.T) {
		const src = "eo_capability_decisions_base.go"
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, src, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", src, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			if strings.Contains(strings.ToLower(lit.Value), "verdict") {
				t.Errorf("%s names %s in code at %s; conflict_verdict is the officer's judgement and the engine must not reach it",
					src, lit.Value, fset.Position(lit.Pos()))
			}
			return true
		})
	})
}

// ── the empty base, which is also the empty taxonomy ────────────────────────

// TestDecisionsBaseIsEmptyWhenNothingMatches covers the state the pilot is
// actually in: the subject taxonomy is a client vocabulary that exists in no
// document, so `keywords` is empty on every record. A first draft in an empty
// base must still save.
func TestDecisionsBaseIsEmptyWhenNothingMatches(t *testing.T) {
	ctx := context.Background()
	capability := NewEODecisionsBaseCapability()
	cfg := eoDecBaseStdConfig(t)

	for _, tc := range []struct {
		name string
		// base of the configured type; may be empty but must exist.
		base []models.EORecord
		// draft values.
		draft map[string]any
		// reads is how many times the base was read.
		reads int
	}{
		{
			name:  "an empty base",
			base:  nil,
			draft: map[string]any{"keywords": []string{"water_tariff"}},
			reads: 1,
		},
		{
			name: "a base nothing is filed under — today's state of the register",
			base: []models.EORecord{
				eoDecBaseRec(t, "DEC-2026-001", map[string]any{"standing": "in_force"}),
				eoDecBaseRec(t, "DEC-2026-002", map[string]any{"keywords": []any{}, "standing": "in_force"}),
			},
			draft: map[string]any{"keywords": []string{"water_tariff"}},
			reads: 1,
		},
		{
			name: "a draft filed under nothing — today's state of every draft",
			base: []models.EORecord{
				eoDecBaseRec(t, "DEC-2026-001", map[string]any{"keywords": []string{"water_tariff"}, "standing": "in_force"}),
			},
			draft: map[string]any{"grounds": "the tariff was set in 2019"},
			reads: 0,
		},
		{
			name: "a draft whose terms are blank",
			base: []models.EORecord{
				eoDecBaseRec(t, "DEC-2026-001", map[string]any{"keywords": []string{"water_tariff"}, "standing": "in_force"}),
			},
			draft: map[string]any{"keywords": []string{"  ", ""}},
			reads: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := &eoDecBaseReader{base: map[string][]models.EORecord{eoDecBaseTypeKey: tc.base}}
			rec := eoDecBaseRec(t, "DRF-2026-030", tc.draft)
			got, err := capability.Derive(ctx, read, &rec, cfg)
			if err != nil {
				t.Fatalf("derive: %v", err)
			}
			if refs := eoDecBaseRefs(t, got, "related_refs", tc.base); len(refs) != 0 {
				t.Errorf("related_refs = %v, want none", refs)
			}
			if len(read.asked) != tc.reads {
				t.Errorf("read the base %d times, want %d", len(read.asked), tc.reads)
			}
			// The empty answer must still reach the record, and the record
			// must still be storable.
			if errs := eoApplyValues(&rec, got); len(errs) > 0 {
				t.Fatalf("a first draft in an empty base could not be stored: %v", errs)
			}
			stored, ok := eoDecodeValues(rec.Values)["related_refs"]
			if !ok {
				t.Fatal("related_refs was not stored at all; an absent key says 'never computed', not 'nothing bears on this'")
			}
			if list, ok := stored.([]any); !ok || len(list) != 0 {
				t.Errorf("stored related_refs = %#v, want an empty list", stored)
			}
		})
	}

	t.Run("a list that no longer matches is cleared, not left standing", func(t *testing.T) {
		stale := uuid.New().String()
		read := &eoDecBaseReader{base: map[string][]models.EORecord{eoDecBaseTypeKey: nil}}
		rec := eoDecBaseRec(t, "DRF-2026-031", map[string]any{
			"keywords":     []string{"water_tariff"},
			"related_refs": []string{stale},
		})
		got, err := capability.Derive(ctx, read, &rec, cfg)
		if err != nil {
			t.Fatalf("derive: %v", err)
		}
		if errs := eoApplyValues(&rec, got); len(errs) > 0 {
			t.Fatalf("apply: %v", errs)
		}
		if list, _ := eoDecodeValues(rec.Values)["related_refs"].([]any); len(list) != 0 {
			t.Errorf("related_refs = %#v, want the stale reference gone", list)
		}
	})
}

// ── configuration ───────────────────────────────────────────────────────────

// TestDecisionsBaseConfigIsStrict: a stray or missing key makes the record
// type unusable at the first request against it, not at seed time, so every
// hook that parses must refuse it rather than quietly matching nothing.
func TestDecisionsBaseConfigIsStrict(t *testing.T) {
	ctx := context.Background()
	capability := NewEODecisionsBaseCapability()
	rec := eoDecBaseRec(t, "DRF-2026-040", map[string]any{"keywords": []string{"water_tariff"}})

	for _, tc := range []struct {
		name string
		cfg  string
	}{
		{"no config at all", ``},
		{"an empty object", `{}`},
		{"an unknown key", `{"base_type_key":"eo_decision_record","subject_key":"keywords","base_type":"oops"}`},
		{"a key of the right shape but the wrong name", `{"base_type_key":"eo_decision_record","subject_keys":["keywords"]}`},
		{"no base type, so it would read nothing", `{"subject_key":"keywords"}`},
		{"no subject key, so it would match nothing", `{"base_type_key":"eo_decision_record"}`},
		{"standing values with no key to read them from", `{"base_type_key":"eo_decision_record","subject_key":"keywords","standing_values":["in_force"]}`},
		{"a standing key with no value, so every record is filtered out", `{"base_type_key":"eo_decision_record","subject_key":"keywords","standing_key":"standing"}`},
		{"a negative cap", `{"base_type_key":"eo_decision_record","subject_key":"keywords","max_refs":-1}`},
		{"the target key is also the subject key the officer fills", `{"base_type_key":"eo_decision_record","subject_key":"related_refs","target_key":"related_refs"}`},
		{"a malformed blob", `{"base_type_key":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := datatypes.JSON(tc.cfg)
			read := &eoDecBaseReader{base: map[string][]models.EORecord{}}
			if _, err := capability.Derive(ctx, read, &rec, cfg); err == nil {
				t.Error("Derive accepted it")
			}
			if _, err := capability.Validate(ctx, read, &rec, cfg); err == nil {
				t.Error("Validate accepted it")
			}
			if err := capability.OnTransition(ctx, read, &rec, &models.WorkflowTransition{Code: "submit"}, cfg); err == nil {
				t.Error("OnTransition accepted it")
			}
			if _, err := capability.ReservedKeys(cfg); err == nil {
				t.Error("ReservedKeys accepted it, so the reserved key would go undeclared")
			}
			if len(read.asked) != 0 {
				t.Errorf("a misconfigured capability read the base anyway: %v", read.asked)
			}
		})
	}
}

// ── the reserved key ────────────────────────────────────────────────────────

// TestDecisionsBaseReservesItsTargetKey: related_refs is the engine's to
// write, so no request may set it. ReservedKeys is the whole of that guard
// (eoCapabilitiesCheckConfig), which is why this capability does not embed
// eoReservesNoKeys.
func TestDecisionsBaseReservesItsTargetKey(t *testing.T) {
	capability := NewEODecisionsBaseCapability()

	t.Run("the default target, and a configured one", func(t *testing.T) {
		for cfg, want := range map[string]string{
			`{"base_type_key":"eo_decision_record","subject_key":"keywords"}`:                           "related_refs",
			`{"base_type_key":"eo_decision_record","subject_key":"keywords","target_key":"eo_related"}`: "eo_related",
		} {
			got, err := capability.ReservedKeys(datatypes.JSON(cfg))
			if err != nil {
				t.Fatalf("reserved keys: %v", err)
			}
			if !reflect.DeepEqual(got, []string{want}) {
				t.Errorf("ReservedKeys(%s) = %v, want [%s]", cfg, got, want)
			}
		}
	})

	enabled := []EOEnabledCapability{{Capability: capability, Config: eoDecBaseStdConfig(t)}}

	t.Run("a transition field change may not write it", func(t *testing.T) {
		tr := &models.WorkflowTransition{Code: "submit", FieldChanges: []models.TransitionFieldChange{{FieldName: "related_refs"}}}
		err := eoCapabilitiesCheckConfig(enabled, nil, &models.Workflow{Code: "wf"}, tr)
		if !errors.Is(err, ErrEOCapabilityConfig) {
			t.Errorf("a transition that writes related_refs was accepted: %v", err)
		}
	})

	// The finding this pins: the pilot seed DECLARES related_refs as an Auto
	// field on the Draft Decision Study, and a declared reserved key is
	// exactly what eoCapabilitiesCheckConfig refuses. Whichever task enables
	// this capability on that type must drop the field definition — the
	// derived value needs none — or the type becomes unusable at its first
	// request. The test states the rule rather than the seed's current shape,
	// so it does not break when the seed is fixed.
	t.Run("a field definition may not declare it", func(t *testing.T) {
		fields := []models.EOFieldDef{{Key: "related_refs"}}
		err := eoCapabilitiesCheckConfig(enabled, fields, nil, nil)
		if !errors.Is(err, ErrEOCapabilityConfig) {
			t.Errorf("a type declaring related_refs was accepted: %v", err)
		}
	})

	t.Run("the config this capability names nothing of is the workflow", func(t *testing.T) {
		if err := capability.ValidateWorkflow(eoDecBaseStdConfig(t), &models.Workflow{Code: "wf"}); err != nil {
			t.Errorf("ValidateWorkflow: %v", err)
		}
	})
}

// ── wiring ──────────────────────────────────────────────────────────────────

// TestDecisionsBaseIsRegisteredByTheWiringCall: a capability a record type can
// name but nothing registered is an error at the first request, and a
// capability registered nowhere is one that never runs.
func TestDecisionsBaseIsRegisteredByTheWiringCall(t *testing.T) {
	saved := eoDefaultCapabilities
	eoDefaultCapabilities = newEOCapabilityRegistry()
	defer func() { eoDefaultCapabilities = saved }()
	RegisterEOCapabilities()

	typ := eoCapType(t, []map[string]any{{"key": EODecisionsBaseKey, "config": map[string]any{
		"base_type_key": eoDecBaseTypeKey,
		"subject_key":   "keywords",
	}}})
	typ.Key = eoDecBaseDraftKey
	got, err := CapabilitiesFor(typ)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(got) != 1 || got[0].Key() != "decisions_base" {
		t.Fatalf("resolved %+v, want the decisions_base capability", got)
	}
}

// ── the reader method ───────────────────────────────────────────────────────

// eoDecBaseRecordsRepo and eoDecBaseTypesRepo embed their interfaces and
// override only what the reader calls.
type eoDecBaseRecordsRepo struct {
	repository.EORecordRepository
	byType map[uuid.UUID][]models.EORecord
	asked  []uuid.UUID
}

func (r *eoDecBaseRecordsRepo) RecordsOfType(_ context.Context, typeID uuid.UUID) ([]models.EORecord, error) {
	r.asked = append(r.asked, typeID)
	return r.byType[typeID], nil
}

type eoDecBaseTypesRepo struct {
	repository.EORecordTypeRepository
	byKey map[string]*models.EORecordType
}

func (r *eoDecBaseTypesRepo) GetByKey(_ context.Context, key string) (*models.EORecordType, error) {
	return r.byKey[key], nil
}

// TestCapabilityReaderRecordsOfTypeResolvesTheKeyOnItsOwnHandles proves the
// new reader method goes to the handles the reader was BUILT with — the
// transaction's, wherever one is open — and that a key naming no type is an
// error rather than an empty base.
func TestCapabilityReaderRecordsOfTypeResolvesTheKeyOnItsOwnHandles(t *testing.T) {
	ctx := context.Background()
	typ := &models.EORecordType{ID: uuid.New(), Key: eoDecBaseTypeKey}
	rec := eoDecBaseRec(t, "DEC-2026-001", map[string]any{"standing": "in_force"})
	records := &eoDecBaseRecordsRepo{byType: map[uuid.UUID][]models.EORecord{typ.ID: {rec}}}
	types := &eoDecBaseTypesRepo{byKey: map[string]*models.EORecordType{typ.Key: typ}}
	read := eoNewCapabilityReader(records, types)

	got, err := read.RecordsOfType(ctx, eoDecBaseTypeKey)
	if err != nil {
		t.Fatalf("records of type: %v", err)
	}
	if len(got) != 1 || got[0].Reference != rec.Reference {
		t.Errorf("records of type = %+v, want the one stored record", got)
	}
	if !reflect.DeepEqual(records.asked, []uuid.UUID{typ.ID}) {
		t.Errorf("asked the record repository for %v, want the id the type repository resolved (%s)", records.asked, typ.ID)
	}

	for _, key := range []string{"", "   ", "eo_not_a_type"} {
		if _, err := read.RecordsOfType(ctx, key); err == nil {
			t.Errorf("RecordsOfType(%q) returned no error; a typo must not look like an empty base", key)
		}
	}
}
