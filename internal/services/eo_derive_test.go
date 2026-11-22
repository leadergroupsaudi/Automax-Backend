package services

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/services/eoformula"
	"github.com/google/uuid"
	"gorm.io/datatypes"
)

type eoFakeDeps struct {
	children  map[string][]models.EORecord // by type key
	ancestors map[string]*models.EORecord  // by direction
	err       error
	calls     int
}

func (f *eoFakeDeps) Children(_ context.Context, _ uuid.UUID, key string) ([]models.EORecord, error) {
	f.calls++
	return f.children[key], f.err
}

func (f *eoFakeDeps) Ancestor(_ context.Context, _ *models.EORecord, direction string) (*models.EORecord, error) {
	f.calls++
	return f.ancestors[direction], f.err
}

func eoRec(values string) models.EORecord {
	return models.EORecord{ID: uuid.New(), Values: datatypes.JSON(values)}
}

func eoDerivedField(key, formula string, recomputeOn any) models.EOFieldDef {
	b, _ := json.Marshal(recomputeOn)
	return models.EOFieldDef{Key: key, Provenance: models.EOProvenanceDerived, Formula: formula, RecomputeOn: datatypes.JSON(b)}
}

func eoOwn(fields ...string) []map[string]any {
	var out []map[string]any
	for _, f := range fields {
		out = append(out, map[string]any{"scope": "own", "field": f})
	}
	return out
}

func TestRecomputeOwnFields(t *testing.T) {
	rec := eoRec(`{"visit_date":"2026-03-10","notice_days":1}`)
	fields := []models.EOFieldDef{
		eoDerivedField("freeze_date", "visit_date - days(2)", eoOwn("visit_date")),
		eoDerivedField("short_notice", "notice_days < 2", eoOwn("notice_days")),
	}
	got, err := RecomputeDerived(context.Background(), fields, &rec, &eoFakeDeps{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC)
	if d, ok := got["freeze_date"].(time.Time); !ok || !d.Equal(want) {
		t.Errorf("freeze_date = %v, want %v", got["freeze_date"], want)
	}
	if got["short_notice"] != true {
		t.Errorf("short_notice = %v, want true", got["short_notice"])
	}
}

func TestRecomputeChildAggregate(t *testing.T) {
	parent := eoRec(`{}`)
	deps := &eoFakeDeps{children: map[string][]models.EORecord{
		"execution_plan_stage": {eoRec(`{"weeks":2}`), eoRec(`{"weeks":3.5}`), eoRec(`{"weeks":null}`), eoRec(`{}`)},
	}}
	on := []map[string]any{{"scope": "child", "type": "execution_plan_stage", "field": "weeks", "aggregates": []string{"sum", "max"}}}
	fields := []models.EOFieldDef{
		eoDerivedField("total_weeks", "children_execution_plan_stage_sum_weeks", on),
		eoDerivedField("longest", "children_execution_plan_stage_max_weeks", on),
		eoDerivedField("stage_count", "children_execution_plan_stage_count", on),
	}
	got, err := RecomputeDerived(context.Background(), fields, &parent, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for key, want := range map[string]float64{"total_weeks": 5.5, "longest": 3.5, "stage_count": 4} {
		if got[key] != want {
			t.Errorf("%s = %v, want %v", key, got[key], want)
		}
	}
}

func TestRecomputeAncestorField(t *testing.T) {
	parent := eoRec(`{"acknowledged_on":"2026-04-01"}`)
	child := eoRec(`{}`)
	on := []map[string]any{{"scope": "ancestor", "type": "office_direction", "field": "acknowledged_on"}}
	fields := []models.EOFieldDef{eoDerivedField("parent_ack_deadline", "ancestor_office_direction_acknowledged_on + weeks(1)", on)}

	t.Run("ancestor present", func(t *testing.T) {
		deps := &eoFakeDeps{ancestors: map[string]*models.EORecord{"office_direction": &parent}}
		got, err := RecomputeDerived(context.Background(), fields, &child, deps)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC)
		if d, ok := got["parent_ack_deadline"].(time.Time); !ok || !d.Equal(want) {
			t.Errorf("got %v, want %v", got["parent_ack_deadline"], want)
		}
	})
	t.Run("no ancestor is unresolved, not zero", func(t *testing.T) {
		got, err := RecomputeDerived(context.Background(), fields, &child, &eoFakeDeps{})
		if !errors.Is(err, eoformula.ErrUnresolvedReference) {
			t.Fatalf("err = %v, want ErrUnresolvedReference", err)
		}
		if _, set := got["parent_ack_deadline"]; set {
			t.Errorf("field must be unset, got %v", got["parent_ack_deadline"])
		}
	})
}

func TestRecomputeSkipsFieldWithUnresolvedReference(t *testing.T) {
	// "notice_days" is JSON null and "other" is absent: neither may reach the
	// env as a nil key, or the formula would be treated as resolved.
	rec := eoRec(`{"visit_date":"2026-03-10","notice_days":null}`)
	fields := []models.EOFieldDef{
		eoDerivedField("bad_null", "notice_days + 1", eoOwn("notice_days")),
		eoDerivedField("bad_absent", "other * 2", eoOwn("other")),
		eoDerivedField("freeze_date", "visit_date - days(2)", eoOwn("visit_date")),
	}
	got, err := RecomputeDerived(context.Background(), fields, &rec, &eoFakeDeps{})
	if !errors.Is(err, eoformula.ErrUnresolvedReference) {
		t.Fatalf("err = %v, want ErrUnresolvedReference", err)
	}
	for _, k := range []string{"bad_null", "bad_absent"} {
		if _, set := got[k]; set {
			t.Errorf("%s must be unset, got %v", k, got[k])
		}
	}
	if _, ok := got["freeze_date"]; !ok {
		t.Errorf("freeze_date should still be computed, got %v", got)
	}
}

func TestRecomputeReportsLoaderAndConfigErrors(t *testing.T) {
	rec := eoRec(`{"x":1}`)
	loadErr := errors.New("db down")
	child := []map[string]any{{"scope": "child", "type": "stage", "field": "w"}}
	cases := map[string]struct {
		field models.EOFieldDef
		deps  *eoFakeDeps
		want  error
	}{
		"loader error":      {eoDerivedField("a", "children_stage_sum_w", child), &eoFakeDeps{err: loadErr}, loadErr},
		"unknown scope":     {eoDerivedField("a", "x", []map[string]any{{"scope": "sideways", "field": "x"}}), &eoFakeDeps{}, nil},
		"unknown aggregate": {eoDerivedField("a", "x", []map[string]any{{"scope": "child", "type": "s", "field": "w", "aggregates": []string{"median"}}}), &eoFakeDeps{}, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := RecomputeDerived(context.Background(), []models.EOFieldDef{tc.field}, &rec, tc.deps)
			if err == nil {
				t.Fatal("expected an error")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want it to wrap %v", err, tc.want)
			}
			if len(got) != 0 {
				t.Errorf("no field should be set, got %v", got)
			}
		})
	}
}

func TestRecomputeIsOrderIndependent(t *testing.T) {
	rec := eoRec(`{"base":10,"a":999}`)
	// "b" reads derived "a", but may arrive first; a stale stored "a" must not be used.
	a := eoDerivedField("a", "base * 2", eoOwn("base"))
	b := eoDerivedField("b", "a + 1", eoOwn("a"))
	for name, fields := range map[string][]models.EOFieldDef{"b before a": {b, a}, "a before b": {a, b}} {
		t.Run(name, func(t *testing.T) {
			got, err := RecomputeDerived(context.Background(), fields, &rec, &eoFakeDeps{})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got["a"] != 20.0 || got["b"] != 21.0 {
				t.Errorf("got a=%v b=%v, want 20 and 21", got["a"], got["b"])
			}
		})
	}

	t.Run("cycle is reported, independent field still computed", func(t *testing.T) {
		x := eoDerivedField("x", "y + 1", eoOwn("y"))
		y := eoDerivedField("y", "x + 1", eoOwn("x"))
		ok := eoDerivedField("ok", "base", eoOwn("base"))
		got, err := RecomputeDerived(context.Background(), []models.EOFieldDef{x, y, ok}, &rec, &eoFakeDeps{})
		if !errors.Is(err, ErrEODeriveCycle) {
			t.Fatalf("err = %v, want ErrEODeriveCycle", err)
		}
		if _, set := got["x"]; set {
			t.Error("x must be unset")
		}
		if _, set := got["ok"]; !set {
			t.Error("ok should still be computed")
		}
	})
}
