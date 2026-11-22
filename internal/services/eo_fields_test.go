package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
)

func eoField(typeID uuid.UUID, key string, sort int) models.EOFieldDef {
	return models.EOFieldDef{ID: uuid.New(), RecordTypeID: typeID, Key: key, SortOrder: sort}
}

func eoKeys(fs []models.EOFieldDef) string {
	ks := make([]string, len(fs))
	for i, f := range fs {
		ks[i] = f.Key
	}
	return strings.Join(ks, ",")
}

func TestResolveEffectiveFieldsIncludesAncestors(t *testing.T) {
	base := models.EORecordType{ID: uuid.New(), Key: "base", IsAbstract: true}
	mid := models.EORecordType{ID: uuid.New(), Key: "mid", ParentTypeID: &base.ID}
	leaf := models.EORecordType{ID: uuid.New(), Key: "leaf", ParentTypeID: &mid.ID}
	other := uuid.New()

	var defs []models.EOFieldDef
	var want []string
	for i := 0; i < 17; i++ {
		k := fmt.Sprintf("base_%02d", i)
		defs = append(defs, eoField(base.ID, k, i))
		want = append(want, k)
	}
	defs = append(defs, eoField(mid.ID, "mid_a", 1), eoField(leaf.ID, "leaf_a", 1), eoField(other, "unrelated", 0))
	want = append(want, "mid_a", "leaf_a")

	// Supply ancestors in the "wrong" order to show the chain is built, not assumed.
	got, err := ResolveEffectiveFields(&leaf, []models.EORecordType{base, mid}, defs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eoKeys(got) != strings.Join(want, ",") {
		t.Errorf("keys = %s, want %s", eoKeys(got), strings.Join(want, ","))
	}
}

func TestResolveEffectiveFieldsOverrideByKey(t *testing.T) {
	base := models.EORecordType{ID: uuid.New(), Key: "base", IsAbstract: true}
	child := models.EORecordType{ID: uuid.New(), Key: "child", ParentTypeID: &base.ID}

	baseTitle := eoField(base.ID, "title", 0)
	baseTitle.RequiredMode = models.EORequiredNever
	// SortOrder 5 would place title after zeta (1) and own (0) if the child's
	// value were honoured; it must be ignored and title keep the base slot.
	childTitle := eoField(child.ID, "title", 5)
	childTitle.RequiredMode = models.EORequiredAlways
	defs := []models.EOFieldDef{
		baseTitle, eoField(base.ID, "zeta", 1), childTitle, eoField(child.ID, "own", 0),
	}

	got, err := ResolveEffectiveFields(&child, []models.EORecordType{base}, defs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if eoKeys(got) != "title,zeta,own" {
		t.Fatalf("keys = %s, want title,zeta,own (override keeps ancestor slot despite child SortOrder 5, once)", eoKeys(got))
	}
	if got[0].ID != childTitle.ID || got[0].RequiredMode != models.EORequiredAlways {
		t.Errorf("title = %+v, want the child's definition", got[0])
	}
}

func TestResolveEffectiveFieldsDetectsCycle(t *testing.T) {
	a := models.EORecordType{ID: uuid.New(), Key: "a"}
	b := models.EORecordType{ID: uuid.New(), Key: "b"}
	a.ParentTypeID = &b.ID
	b.ParentTypeID = &a.ID
	self := models.EORecordType{ID: uuid.New(), Key: "self"}
	self.ParentTypeID = &self.ID

	tests := []struct {
		name      string
		typ       models.EORecordType
		ancestors []models.EORecordType
	}{
		{"two-type loop", a, []models.EORecordType{a, b}},
		{"self parent", self, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ResolveEffectiveFields(&tc.typ, tc.ancestors, nil); err == nil {
				t.Error("err = nil, want a cycle error")
			}
		})
	}
}

func TestResolveEffectiveFieldsRejectsMissingAncestor(t *testing.T) {
	missing := uuid.New()
	typ := models.EORecordType{ID: uuid.New(), Key: "t", ParentTypeID: &missing}
	if _, err := ResolveEffectiveFields(&typ, nil, nil); err == nil {
		t.Error("err = nil, want an error for an absent ancestor")
	}
}

func TestResolveEffectiveFieldsRejectsExcessiveDepth(t *testing.T) {
	var chain []models.EORecordType
	var parent *uuid.UUID
	for i := 0; i < eoMaxInheritanceDepth+5; i++ {
		rt := models.EORecordType{ID: uuid.New(), Key: fmt.Sprintf("t%d", i), ParentTypeID: parent}
		chain = append(chain, rt)
		id := rt.ID
		parent = &id
	}
	leaf := chain[len(chain)-1]
	if _, err := ResolveEffectiveFields(&leaf, chain[:len(chain)-1], nil); err == nil {
		t.Error("err = nil, want a depth error")
	}
}

func TestResolveEffectiveFieldsIsDeterministic(t *testing.T) {
	typ := models.EORecordType{ID: uuid.New(), Key: "t"}
	input := []models.EOFieldDef{
		eoField(typ.ID, "charlie", 0), eoField(typ.ID, "alpha", 0), eoField(typ.ID, "bravo", 0), eoField(typ.ID, "first", -1),
	}
	reversed := []models.EOFieldDef{input[3], input[2], input[1], input[0]}

	for name, defs := range map[string][]models.EOFieldDef{"forward": input, "reversed": reversed} {
		t.Run(name, func(t *testing.T) {
			got, err := ResolveEffectiveFields(&typ, nil, defs)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if eoKeys(got) != "first,alpha,bravo,charlie" {
				t.Errorf("keys = %s, want first,alpha,bravo,charlie", eoKeys(got))
			}
		})
	}
}

// eoStubEval is a stub FormulaEvaluator: it returns a fixed result per
// expression and records the env it was given.
type eoStubEval struct {
	results map[string]bool
	err     error
	lastEnv map[string]any
	calls   int
}

func (s *eoStubEval) EvalBool(expr string, env map[string]any) (bool, error) {
	s.calls++
	s.lastEnv = env
	if s.err != nil {
		return false, s.err
	}
	return s.results[expr], nil
}

func eoValidate(t *testing.T, fields []models.EOFieldDef, stored map[string]any, values map[string]any, target *uuid.UUID, entered map[uuid.UUID]bool, ev FormulaEvaluator) map[string]string {
	t.Helper()
	errs, err := ValidateRecordValues(fields, stored, values, target, entered, ev)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return errs
}

func TestValidateRequiredModeAlways(t *testing.T) {
	f := []models.EOFieldDef{{Key: "title", Label: "Title", RequiredMode: models.EORequiredAlways}}
	rec := map[string]any{"title": "stored"}
	tests := []struct {
		name    string
		rec     map[string]any
		values  map[string]any
		wantErr bool
	}{
		{"absent on create", nil, map[string]any{}, true},
		{"nil values map", nil, nil, true},
		{"explicit nil", nil, map[string]any{"title": nil}, true},
		{"blank string", nil, map[string]any{"title": "  "}, true},
		{"present", nil, map[string]any{"title": "x"}, false},
		{"zero is a value", nil, map[string]any{"title": 0}, false},
		{"false is a value", nil, map[string]any{"title": false}, false},
		{"absent but stored", rec, map[string]any{}, false},
		{"explicit nil clears stored", rec, map[string]any{"title": nil}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := eoValidate(t, f, tc.rec, tc.values, nil, nil, nil)
			if _, got := errs["title"]; got != tc.wantErr {
				t.Fatalf("errs = %v, want error %v", errs, tc.wantErr)
			}
		})
	}
}

// An inherited base field (stored in a real column, flattened into stored by
// the caller) that is always-required must not be reported missing on a
// partial update that does not resend it.
func TestValidateStoredValuesSatisfyRequiredOnPartialUpdate(t *testing.T) {
	f := []models.EOFieldDef{{Key: "subject_ar", Label: "Subject", RequiredMode: models.EORequiredAlways}}
	stored := map[string]any{"subject_ar": "موضوع"}
	if errs := eoValidate(t, f, stored, map[string]any{"other": 1}, nil, nil, nil); len(errs) != 0 {
		t.Fatalf("errs = %v, want none", errs)
	}
	if errs := eoValidate(t, f, nil, map[string]any{}, nil, nil, nil); len(errs) != 1 {
		t.Fatalf("create without subject_ar: errs = %v, want one", errs)
	}
}

func TestValidateRequiredModeNeverIgnoresEmpty(t *testing.T) {
	f := []models.EOFieldDef{{Key: "notes", RequiredMode: models.EORequiredNever}}
	if errs := eoValidate(t, f, nil, map[string]any{}, nil, nil, nil); len(errs) != 0 {
		t.Fatalf("errs = %v, want none", errs)
	}
}

func TestValidateRequiredModeAtState(t *testing.T) {
	closed, open := uuid.New(), uuid.New()
	ids, _ := json.Marshal([]uuid.UUID{closed})
	f := []models.EOFieldDef{{Key: "evidence", RequiredMode: models.EORequiredAtState, RequiredAtStateIDs: ids}}
	tests := []struct {
		name    string
		target  *uuid.UUID
		values  map[string]any
		wantErr bool
	}{
		{"plain save, no target", nil, map[string]any{}, false},
		{"other target", &open, map[string]any{}, false},
		{"closure target, missing", &closed, map[string]any{}, true},
		{"closure target, present", &closed, map[string]any{"evidence": "doc"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := eoValidate(t, f, nil, tc.values, tc.target, nil, nil)
			if _, got := errs["evidence"]; got != tc.wantErr {
				t.Fatalf("errs = %v, want error %v", errs, tc.wantErr)
			}
		})
	}
}

func TestValidateRequiredModeAtStateMalformedIDsIsMachineryError(t *testing.T) {
	target := uuid.New()
	f := []models.EOFieldDef{{Key: "evidence", RequiredMode: models.EORequiredAtState, RequiredAtStateIDs: []byte(`{"bad":1}`)}}
	if _, err := ValidateRecordValues(f, nil, map[string]any{}, &target, nil, nil); err == nil {
		t.Fatal("want error for malformed required_at_state_ids")
	}
}

func TestValidateRequiredModeConditional(t *testing.T) {
	f := []models.EOFieldDef{{Key: "delay_reason", RequiredMode: models.EORequiredConditional, RequiredRule: "overdue"}}
	t.Run("rule true enforces", func(t *testing.T) {
		ev := &eoStubEval{results: map[string]bool{"overdue": true}}
		errs := eoValidate(t, f, nil, map[string]any{"due": "x"}, nil, nil, ev)
		if _, ok := errs["delay_reason"]; !ok {
			t.Fatalf("errs = %v, want delay_reason", errs)
		}
		if ev.lastEnv["due"] != "x" {
			t.Fatalf("env missing submitted values: %v", ev.lastEnv)
		}
	})
	t.Run("rule false does not", func(t *testing.T) {
		ev := &eoStubEval{results: map[string]bool{"overdue": false}}
		if errs := eoValidate(t, f, nil, map[string]any{}, nil, nil, ev); len(errs) != 0 {
			t.Fatalf("errs = %v, want none", errs)
		}
	})
	t.Run("value present, rule true, valid", func(t *testing.T) {
		ev := &eoStubEval{results: map[string]bool{"overdue": true}}
		if errs := eoValidate(t, f, nil, map[string]any{"delay_reason": "rain"}, nil, nil, ev); len(errs) != 0 {
			t.Fatalf("errs = %v, want none", errs)
		}
	})
	t.Run("env merges stored with submitted, submitted wins", func(t *testing.T) {
		ev := &eoStubEval{results: map[string]bool{"overdue": false}}
		rec := map[string]any{"a": "old", "b": "kept"}
		eoValidate(t, f, rec, map[string]any{"a": "new"}, nil, nil, ev)
		if ev.lastEnv["a"] != "new" || ev.lastEnv["b"] != "kept" {
			t.Fatalf("env = %v", ev.lastEnv)
		}
	})
	t.Run("evaluator error is a machinery error", func(t *testing.T) {
		boom := errors.New("boom")
		ev := &eoStubEval{err: boom}
		errs, err := ValidateRecordValues(f, nil, map[string]any{}, nil, nil, ev)
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want wrapped boom", err)
		}
		if errs != nil {
			t.Fatalf("errs = %v, want nil on machinery error", errs)
		}
	})
	t.Run("empty rule is a machinery error", func(t *testing.T) {
		g := []models.EOFieldDef{{Key: "x", RequiredMode: models.EORequiredConditional}}
		if _, err := ValidateRecordValues(g, nil, map[string]any{}, nil, nil, &eoStubEval{}); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("rule not evaluated when value present", func(t *testing.T) {
		ev := &eoStubEval{}
		eoValidate(t, f, nil, map[string]any{"delay_reason": "rain"}, nil, nil, ev)
		if ev.calls != 0 {
			t.Fatalf("evaluator called %d times", ev.calls)
		}
	})
}

func TestValidateRejectsWritesToDerivedAndAutoFields(t *testing.T) {
	f := []models.EOFieldDef{
		{Key: "total", Provenance: models.EOProvenanceDerived, RequiredMode: models.EORequiredAlways},
		{Key: "ref", Provenance: models.EOProvenanceAuto},
		{Key: "set_by_transition", Provenance: models.EOProvenanceSystem},
		{Key: "title", Provenance: models.EOProvenanceUser},
	}
	tests := []struct {
		name   string
		values map[string]any
		want   string // sorted error keys
	}{
		{"derived written", map[string]any{"total": 5}, "total"},
		{"auto written", map[string]any{"ref": "X-1"}, "ref"},
		{"explicit nil still a write", map[string]any{"ref": nil}, "ref"},
		{"both", map[string]any{"total": 1, "ref": "r"}, "ref,total"},
		{"system and user accepted", map[string]any{"set_by_transition": 1, "title": "t"}, ""},
		{"not supplied, derived always-required is exempt", map[string]any{}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := eoValidate(t, f, nil, tc.values, nil, nil, nil)
			keys := make([]string, 0, len(errs))
			for k := range errs {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			if got := strings.Join(keys, ","); got != tc.want {
				t.Fatalf("error keys = %q, want %q (%v)", got, tc.want, errs)
			}
		})
	}
}

func TestValidateRespectsIsLockedAfter(t *testing.T) {
	submitted, other := uuid.New(), uuid.New()
	f := []models.EOFieldDef{{Key: "draft_text", IsLockedAfterStateID: &submitted}}
	rec := map[string]any{"draft_text": "original", "n": float64(3)}
	n := []models.EOFieldDef{{Key: "n", IsLockedAfterStateID: &submitted}}
	passed := map[uuid.UUID]bool{submitted: true}
	tests := []struct {
		name    string
		fields  []models.EOFieldDef
		values  map[string]any
		entered map[uuid.UUID]bool
		wantErr bool
	}{
		{"changed after passing lock state", f, map[string]any{"draft_text": "edited"}, passed, true},
		{"cleared after passing lock state", f, map[string]any{"draft_text": nil}, passed, true},
		{"resubmitted unchanged", f, map[string]any{"draft_text": "original"}, passed, false},
		{"not mentioned", f, map[string]any{}, passed, false},
		{"numeric unchanged (float64 vs int)", n, map[string]any{"n": 3}, passed, false},
		{"numeric changed", n, map[string]any{"n": 4}, passed, true},
		{"lock state never entered", f, map[string]any{"draft_text": "edited"}, map[uuid.UUID]bool{other: true}, false},
		{"nil entered set", f, map[string]any{"draft_text": "edited"}, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := eoValidate(t, tc.fields, rec, tc.values, nil, tc.entered, nil)
			if len(errs) > 1 || (len(errs) == 1) != tc.wantErr {
				t.Fatalf("errs = %v, want error %v", errs, tc.wantErr)
			}
		})
	}
	t.Run("no lock configured", func(t *testing.T) {
		g := []models.EOFieldDef{{Key: "draft_text"}}
		if errs := eoValidate(t, g, rec, map[string]any{"draft_text": "edited"}, nil, passed, nil); len(errs) != 0 {
			t.Fatalf("errs = %v", errs)
		}
	})
}

func TestValidateUnknownVocabularyFailsClosed(t *testing.T) {
	tests := []struct {
		name string
		f    models.EOFieldDef
	}{
		{"unknown required_mode", models.EOFieldDef{Key: "a", RequiredMode: "alway"}},
		{"unknown at_state typo", models.EOFieldDef{Key: "a", RequiredMode: "at_stage"}},
		{"unknown provenance", models.EOFieldDef{Key: "a", Provenance: "derrived"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateRecordValues([]models.EOFieldDef{tc.f}, nil, map[string]any{"a": 1}, nil, nil, nil)
			if err == nil || !strings.Contains(err.Error(), `"a"`) {
				t.Fatalf("err = %v, want error naming the field", err)
			}
		})
	}
	t.Run("empty strings keep their defaults", func(t *testing.T) {
		f := []models.EOFieldDef{{Key: "a"}}
		if errs := eoValidate(t, f, nil, map[string]any{}, nil, nil, nil); len(errs) != 0 {
			t.Fatalf("errs = %v", errs)
		}
	})
}

func TestValidateTypedNilStoredValuesAreEmpty(t *testing.T) {
	var ptr *time.Time
	var ids []string
	var m map[string]any
	for name, v := range map[string]any{"nil pointer": ptr, "nil slice": ids, "nil map": m, "empty slice": []string{}} {
		t.Run(name, func(t *testing.T) {
			f := []models.EOFieldDef{{Key: "due_date", RequiredMode: models.EORequiredAlways}}
			errs := eoValidate(t, f, map[string]any{"due_date": v}, map[string]any{}, nil, nil, nil)
			if _, ok := errs["due_date"]; !ok {
				t.Fatalf("errs = %v, want due_date required", errs)
			}
		})
	}
	t.Run("non-nil pointer is a value", func(t *testing.T) {
		now := time.Now()
		f := []models.EOFieldDef{{Key: "due_date", RequiredMode: models.EORequiredAlways}}
		if errs := eoValidate(t, f, map[string]any{"due_date": &now}, map[string]any{}, nil, nil, nil); len(errs) != 0 {
			t.Fatalf("errs = %v", errs)
		}
	})
}
