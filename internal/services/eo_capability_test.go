package services

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// eoCapFake embeds the interface and overrides only Key: the registry never
// calls anything else.
type eoCapFake struct {
	Capability
	key string
}

func (f eoCapFake) Key() string { return f.key }

func eoCapType(t *testing.T, caps any) *models.EORecordType {
	t.Helper()
	b, err := json.Marshal(caps)
	if err != nil {
		t.Fatal(err)
	}
	return &models.EORecordType{Key: "eo_cap_type", Capabilities: b}
}

func TestRegisterAndResolveCapabilities(t *testing.T) {
	reg := newEOCapabilityRegistry()
	reg.register(eoCapFake{key: "alpha"})
	reg.register(eoCapFake{key: "beta"})

	t.Run("resolves in the type's order with each config", func(t *testing.T) {
		got, err := reg.resolve(eoCapType(t, []map[string]any{
			{"key": "beta", "config": map[string]any{"n": 2}},
			{"key": "alpha"},
		}))
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 || got[0].Key() != "beta" || got[1].Key() != "alpha" {
			t.Fatalf("order not preserved: %+v", got)
		}
		if string(got[0].Config) != `{"n":2}` {
			t.Fatalf("config not bound: %s", got[0].Config)
		}
	})
	t.Run("a type with no capabilities resolves to none", func(t *testing.T) {
		got, err := reg.resolve(&models.EORecordType{})
		if err != nil || len(got) != 0 {
			t.Fatalf("got %v, %v", got, err)
		}
	})
	t.Run("an unknown key is an error, not a skip", func(t *testing.T) {
		got, err := reg.resolve(eoCapType(t, []map[string]any{{"key": "alpha"}, {"key": "nope"}}))
		if !errors.Is(err, ErrEOUnknownCapability) {
			t.Fatalf("want ErrEOUnknownCapability, got %v (%v)", err, got)
		}
	})
	t.Run("a malformed list is an error", func(t *testing.T) {
		for name, raw := range map[string]string{
			"not a list":    `{"key":"alpha"}`,
			"bare string":   `["alpha"]`,
			"unknown field": `[{"key":"alpha","cfg":{}}]`,
		} {
			_, err := reg.resolve(&models.EORecordType{Capabilities: datatypes.JSON(raw)})
			if err == nil {
				t.Errorf("%s: want error", name)
			}
		}
	})
	t.Run("the same key twice is an error", func(t *testing.T) {
		if _, err := reg.resolve(eoCapType(t, []map[string]any{{"key": "alpha"}, {"key": "alpha"}})); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("registering a key twice panics", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Fatal("want panic")
			}
		}()
		reg.register(eoCapFake{key: "alpha"})
	})
	t.Run("the wiring call registers approval and is not an init", func(t *testing.T) {
		saved := eoDefaultCapabilities
		eoDefaultCapabilities = newEOCapabilityRegistry()
		defer func() { eoDefaultCapabilities = saved }()
		typ := eoCapType(t, []map[string]any{{"key": EOApprovalKey, "config": map[string]any{"transitions": []string{"x"}}}})
		if _, err := CapabilitiesFor(typ); !errors.Is(err, ErrEOUnknownCapability) {
			t.Fatalf("registered before wiring: %v", err)
		}
		RegisterEOCapabilities()
		got, err := CapabilitiesFor(typ)
		if err != nil || len(got) != 1 {
			t.Fatalf("got %v, %v", got, err)
		}
	})
}

var eoApprovalTestCfg = datatypes.JSON(`{"transitions":["approve"]}`)

func eoApprovalFixture(t *testing.T, values map[string]any) (Capability, *models.EORecord, *models.User, context.Context) {
	t.Helper()
	b, _ := json.Marshal(values)
	c := &eoApprovalCapability{now: func() time.Time { return time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC) }}
	actor := &models.User{ID: uuid.New()}
	return c, &models.EORecord{Reference: "EO-1", Values: b}, actor, WithEOActor(context.Background(), actor)
}

func eoApprovalStampOf(t *testing.T, rec *models.EORecord) map[string]any {
	t.Helper()
	s, _ := eoDecodeValues(rec.Values)["approval"].(map[string]any)
	return s
}

func TestApprovalStampRecordsBothIdentities(t *testing.T) {
	c, rec, actor, ctx := eoApprovalFixture(t, map[string]any{
		"approval_authority":       "H.E. the Secretary",
		"approval_delegation_note": "by delegation of H.E., minute 4",
		"other":                    "kept",
	})
	if err := c.OnTransition(ctx, rec, &models.WorkflowTransition{Code: "approve"}, eoApprovalTestCfg); err != nil {
		t.Fatal(err)
	}
	s := eoApprovalStampOf(t, rec)
	if s["recorded_by"] != actor.ID.String() {
		t.Errorf("recorded_by = %v, want the actor %s", s["recorded_by"], actor.ID)
	}
	if s["authority"] != "H.E. the Secretary" || s["authority"] == s["recorded_by"] {
		t.Errorf("authority = %v", s["authority"])
	}
	if s["delegation_note"] != "by delegation of H.E., minute 4" {
		t.Errorf("delegation_note = %v", s["delegation_note"])
	}
	if s["recorded_at"] != "2026-11-22T09:00:00Z" {
		t.Errorf("recorded_at = %v", s["recorded_at"])
	}
	if eoDecodeValues(rec.Values)["other"] != "kept" {
		t.Error("the stamp clobbered other values")
	}

	t.Run("a transition not listed does not stamp", func(t *testing.T) {
		c, rec, _, ctx := eoApprovalFixture(t, map[string]any{"approval_authority": "H.E."})
		if err := c.OnTransition(ctx, rec, &models.WorkflowTransition{Code: "other"}, eoApprovalTestCfg); err != nil {
			t.Fatal(err)
		}
		if eoApprovalStampOf(t, rec) != nil {
			t.Error("stamped on an unlisted transition")
		}
	})
	t.Run("no acting user is an error", func(t *testing.T) {
		c, rec, _, _ := eoApprovalFixture(t, map[string]any{"approval_authority": "H.E."})
		if err := c.OnTransition(context.Background(), rec, &models.WorkflowTransition{Code: "approve"}, eoApprovalTestCfg); err == nil {
			t.Fatal("want error")
		}
	})
}

func TestApprovalStampRejectsMissingAuthority(t *testing.T) {
	for name, values := range map[string]map[string]any{
		"absent":     {},
		"empty":      {"approval_authority": ""},
		"whitespace": {"approval_authority": "   "},
		"not text":   {"approval_authority": 7},
	} {
		t.Run(name, func(t *testing.T) {
			c, rec, _, ctx := eoApprovalFixture(t, values)
			before := string(rec.Values)
			err := c.OnTransition(ctx, rec, &models.WorkflowTransition{Code: "approve"}, eoApprovalTestCfg)
			var ve *EOValidationError
			if !errors.As(err, &ve) || ve.Fields["approval_authority"] == "" {
				t.Fatalf("want a validation error on approval_authority, got %v", err)
			}
			if string(rec.Values) != before {
				t.Error("a stamp was written without an authority (defaulted to the actor?)")
			}
		})
	}
}

func TestApprovalStampIsImmutableOnceSet(t *testing.T) {
	c, rec, _, ctx := eoApprovalFixture(t, map[string]any{"approval_authority": "H.E."})
	tr := &models.WorkflowTransition{Code: "approve"}
	if err := c.OnTransition(ctx, rec, tr, eoApprovalTestCfg); err != nil {
		t.Fatal(err)
	}
	sealed := string(rec.Values)

	if err := c.OnTransition(ctx, rec, tr, eoApprovalTestCfg); err == nil {
		t.Error("a second stamp replaced the first")
	}
	if string(rec.Values) != sealed {
		t.Error("the sealed stamp changed")
	}

	t.Run("unchanged inputs validate", func(t *testing.T) {
		errs, err := c.Validate(ctx, rec, eoApprovalTestCfg)
		if err != nil || len(errs) != 0 {
			t.Fatalf("%v %v", errs, err)
		}
	})
	t.Run("an edited authority is rejected", func(t *testing.T) {
		bag := eoDecodeValues(rec.Values)
		bag["approval_authority"] = "Head of Office"
		rec.Values, _ = json.Marshal(bag)
		errs, err := c.Validate(ctx, rec, eoApprovalTestCfg)
		if err != nil || errs["approval_authority"] == "" {
			t.Fatalf("%v %v", errs, err)
		}
	})
	t.Run("an edited delegation note is rejected", func(t *testing.T) {
		bag := eoDecodeValues(rec.Values)
		bag["approval_authority"] = "H.E."
		bag["approval_delegation_note"] = "added later"
		rec.Values, _ = json.Marshal(bag)
		errs, _ := c.Validate(ctx, rec, eoApprovalTestCfg)
		if errs["approval_delegation_note"] == "" {
			t.Fatalf("%v", errs)
		}
	})
}

func TestApprovalConfigMalformedIsAnErrorNotAPanic(t *testing.T) {
	for name, cfg := range map[string]string{
		"empty":          ``,
		"not json":       `{`,
		"wrong type":     `{"transitions":"approve"}`,
		"unknown key":    `{"transitions":["a"],"stampkey":"x"}`,
		"no transitions": `{"stamp_key":"approval"}`,
		"json null":      `null`,
		"array":          `[]`,
	} {
		t.Run(name, func(t *testing.T) {
			c, rec, _, ctx := eoApprovalFixture(t, map[string]any{"approval_authority": "H.E."})
			tr := &models.WorkflowTransition{Code: "approve"}
			if err := c.OnTransition(ctx, rec, tr, datatypes.JSON(cfg)); err == nil {
				t.Error("OnTransition: want error")
			}
			if _, err := c.Validate(ctx, rec, datatypes.JSON(cfg)); err == nil {
				t.Error("Validate: want error")
			}
		})
	}
}
