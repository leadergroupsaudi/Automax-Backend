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
	"github.com/automax/backend/internal/services/eoformula"
	"github.com/automax/backend/internal/storage"
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

// The real evaluator must satisfy the consumer interface. A compile error here
// is the test.
var _ FormulaEvaluator = (*eoformula.Evaluator)(nil)

// ── file and multi_file carry Documenta ids (spec §7.12) ────────────────────

// eoDocID is a canonical Documenta node id: what the HTTP client reports as
// DmsFile.UUID and what UploadFile returns.
func eoDocID() string { return uuid.NewString() }

// eoStubDocID is what storage.StubDocumentaClient.UploadFile returns when
// Documenta is disabled, which a deployment running the stub genuinely holds.
func eoStubDocID() string { return storage.StubFileIDPrefix + uuid.NewString() }

func TestFileFieldsRejectANonDocumentaValue(t *testing.T) {
	fields := []models.EOFieldDef{
		{Key: "letter", Label: "Letter", Kind: models.EOKindFile},
		{Key: "attachments", Label: "Attachments", Kind: models.EOKindMultiFile},
	}
	good, stub := eoDocID(), eoStubDocID()

	tests := []struct {
		name    string
		values  map[string]any
		wantErr []string // keys expected to carry a message
	}{
		// The live reproduction: POST /eo-records with these two values
		// answered 201 and stored both verbatim — a bare string and an
		// integer in a multi_file field.
		{"the live repro", map[string]any{"attachments": []any{"not-a-documenta-id-at-all", 12345}}, []string{"attachments"}},
		{"a bare word in a file field", map[string]any{"letter": "not-a-documenta-id-at-all"}, []string{"letter"}},
		{"a number in a file field", map[string]any{"letter": 12345}, []string{"letter"}},
		{"a list in a single file field", map[string]any{"letter": []any{good}}, []string{"letter"}},
		{"a bare id where a set belongs", map[string]any{"attachments": good}, []string{"attachments"}},
		{"a nested list", map[string]any{"attachments": []any{[]any{good}}}, []string{"attachments"}},
		{"an id with spaces around it", map[string]any{"letter": " " + good + " "}, []string{"letter"}},
		{"an unhyphenated uuid is not the id Documenta gave", map[string]any{"letter": strings.ReplaceAll(good, "-", "")}, []string{"letter"}},
		{"a urn-wrapped uuid is not the id Documenta gave", map[string]any{"letter": "urn:uuid:" + good}, []string{"letter"}},
		{"both fields wrong are both reported", map[string]any{"letter": "x", "attachments": []any{"y"}}, []string{"attachments", "letter"}},

		{"a Documenta id", map[string]any{"letter": good}, nil},
		{"a stub id, so a disabled deployment behaves the same", map[string]any{"letter": stub}, nil},
		{"an empty file field is a requiredness question, not a shape one", map[string]any{"letter": ""}, nil},
		{"an explicit nil likewise", map[string]any{"letter": nil}, nil},
		{"an empty set likewise", map[string]any{"attachments": []any{}}, nil},
		{"a set of ids", map[string]any{"attachments": []any{good, stub}}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			errs := eoValidate(t, fields, nil, tc.values, nil, nil, nil)
			var got []string
			for k := range errs {
				got = append(got, k)
			}
			sort.Strings(got)
			if strings.Join(got, ",") != strings.Join(tc.wantErr, ",") {
				t.Fatalf("errs = %v, want messages on %v", errs, tc.wantErr)
			}
		})
	}

	t.Run("a stored value is not re-validated", func(t *testing.T) {
		// Only what is SUBMITTED is checked: a record created before this
		// validation existed must still be loadable and editable.
		stored := map[string]any{"attachments": []any{"not-a-documenta-id-at-all"}}
		if errs := eoValidate(t, fields, stored, map[string]any{"letter": good}, nil, nil, nil); len(errs) != 0 {
			t.Fatalf("errs = %v, want none", errs)
		}
	})

	t.Run("no Documenta call is made either way", func(t *testing.T) {
		// The shape check takes no client and cannot reach one: EO Documenta
		// is disabled in the pilot's default deployment, and a check that
		// asked Documenta would pass there and fail here, or the reverse.
		if errs := eoValidate(t, fields, nil, map[string]any{"letter": eoDocID()}, nil, nil, nil); len(errs) != 0 {
			t.Fatalf("errs = %v: a well-formed id that no Documenta holds must still pass", errs)
		}
	})
}

func TestMultiFileAcceptsASet(t *testing.T) {
	fields := []models.EOFieldDef{{Key: "attachments", Label: "Attachments", Kind: models.EOKindMultiFile}}
	a, b, c := eoDocID(), eoDocID(), eoStubDocID()

	t.Run("several ids, as the body decodes them", func(t *testing.T) {
		if errs := eoValidate(t, fields, nil, map[string]any{"attachments": []any{a, b, c}}, nil, nil, nil); len(errs) != 0 {
			t.Fatalf("errs = %v, want none", errs)
		}
	})
	t.Run("several ids, as a Go caller passes them", func(t *testing.T) {
		if errs := eoValidate(t, fields, nil, map[string]any{"attachments": []string{a, b, c}}, nil, nil, nil); len(errs) != 0 {
			t.Fatalf("errs = %v, want none", errs)
		}
	})
	t.Run("one bad member spoils the set", func(t *testing.T) {
		errs := eoValidate(t, fields, nil, map[string]any{"attachments": []any{a, "not-a-documenta-id-at-all", b}}, nil, nil, nil)
		if len(errs) != 1 {
			t.Fatalf("errs = %v, want exactly one message", errs)
		}
	})
	t.Run("a set satisfies requiredness", func(t *testing.T) {
		req := []models.EOFieldDef{{Key: "attachments", Label: "Attachments", Kind: models.EOKindMultiFile, RequiredMode: models.EORequiredAlways}}
		if errs := eoValidate(t, req, nil, map[string]any{"attachments": []any{a}}, nil, nil, nil); len(errs) != 0 {
			t.Fatalf("errs = %v, want none", errs)
		}
		if errs := eoValidate(t, req, nil, map[string]any{"attachments": []any{}}, nil, nil, nil); len(errs) != 1 {
			t.Fatalf("errs = %v, want attachments required", errs)
		}
	})
}

// ── a required_rule that names a field nobody supplied ──────────────────────

// An unresolved reference in a required_rule means the referenced field was
// not supplied, so the condition is UNMET and the field is not required.
// Measured before this: POST /eo-records with values:{} answered 500 for
// three of the pilot's four conditional rules.
func TestValidateConditionalUnresolvedReferenceIsNotRequired(t *testing.T) {
	ev := eoformula.New(0)
	// The three rules measured at 500, verbatim from the pilot seed.
	for _, tc := range []struct{ field, rule string }{
		{"coordinator", `direction_pattern == "refer_and_consolidate"`},
		{"origin_record", `directive_origin == "incoming"`},
		{"redraft_text", `recommendation == "redraft"`},
	} {
		t.Run(tc.field, func(t *testing.T) {
			f := []models.EOFieldDef{{Key: tc.field, Label: tc.field, RequiredMode: models.EORequiredConditional, RequiredRule: tc.rule}}
			errs, err := ValidateRecordValues(f, nil, map[string]any{}, nil, nil, ev)
			if err != nil {
				t.Fatalf("err = %v, want none: an unresolved reference makes the handler answer 500 instead of a 400 field map", err)
			}
			if len(errs) != 0 {
				t.Fatalf("errs = %v, want none: the condition cannot be met by a value nobody supplied", errs)
			}
		})
	}

	t.Run("the rule still decides once the reference resolves", func(t *testing.T) {
		f := []models.EOFieldDef{{Key: "coordinator", Label: "Coordinating body", RequiredMode: models.EORequiredConditional,
			RequiredRule: `direction_pattern == "refer_and_consolidate"`}}
		if errs := eoValidate(t, f, nil, map[string]any{"direction_pattern": "refer_and_consolidate"}, nil, nil, ev); len(errs) != 1 {
			t.Fatalf("errs = %v, want coordinator required", errs)
		}
		if errs := eoValidate(t, f, nil, map[string]any{"direction_pattern": "refer_out"}, nil, nil, ev); len(errs) != 0 {
			t.Fatalf("errs = %v, want none", errs)
		}
		// Stored counts as supplied: the reference resolves from the record.
		if errs := eoValidate(t, f, map[string]any{"direction_pattern": "refer_and_consolidate"}, map[string]any{}, nil, nil, ev); len(errs) != 1 {
			t.Fatalf("errs = %v, want coordinator required from the stored value", errs)
		}
	})

	t.Run("every arm of a joined error unresolved is still unmet", func(t *testing.T) {
		f := []models.EOFieldDef{{Key: "x", RequiredMode: models.EORequiredConditional, RequiredRule: "r"}}
		joined := errors.Join(
			fmt.Errorf("a: %w", eoformula.ErrUnresolvedReference),
			fmt.Errorf("b: %w", eoformula.ErrUnresolvedReference),
		)
		if errs := eoValidate(t, f, nil, map[string]any{}, nil, nil, &eoStubEval{err: joined}); len(errs) != 0 {
			t.Fatalf("errs = %v, want none", errs)
		}
	})
}

// A rule that is genuinely broken must STILL be a hard error: it is a
// configuration defect, not a missing value, and silently treating it as
// "not required" would make a misconfigured rule indistinguishable from a
// correct one that happens not to fire.
func TestValidateConditionalMalformedRuleIsMachineryError(t *testing.T) {
	ev := eoformula.New(0)
	for _, tc := range []struct{ name, rule string }{
		{"a syntax error", `direction_pattern ==`},
		{"a function formulas do not have", `sum(direction_pattern)`},
		{"a rule that is not a condition", `1 + 1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := []models.EOFieldDef{{Key: "coordinator", RequiredMode: models.EORequiredConditional, RequiredRule: tc.rule}}
			errs, err := ValidateRecordValues(f, nil, map[string]any{"direction_pattern": "x"}, nil, nil, ev)
			if err == nil {
				t.Fatalf("errs = %v, err = nil: a broken rule must not pass as an unmet condition", errs)
			}
			if errs != nil {
				t.Fatalf("errs = %v, want nil on a machinery error", errs)
			}
		})
	}

	t.Run("an unresolved reference joined to a real failure is still a failure", func(t *testing.T) {
		f := []models.EOFieldDef{{Key: "x", RequiredMode: models.EORequiredConditional, RequiredRule: "r"}}
		joined := errors.Join(fmt.Errorf("a: %w", eoformula.ErrUnresolvedReference), errors.New("boom"))
		if _, err := ValidateRecordValues(f, nil, map[string]any{}, nil, nil, &eoStubEval{err: joined}); err == nil {
			t.Fatal("want error")
		}
	})
}

// A key PRESENT with a nil value must behave exactly as an absent one.
//
// eoformula treats a present-but-nil key as RESOLVED (formula.go:225), so it
// raises no unresolved reference and the classification added for the absent
// case never fires. The pilot's `delay_reason matches "\S"` rule then fails to
// compile against an unknown type and the create answers 500 — reproduced live
// on eo_directive: values {} -> 400, {"delay_reason": null} -> 500,
// {"delay_reason": ""} -> 400. A UI that submits every writable field sends
// null for an untouched optional one, so this is ordinary input.
func TestValidateConditionalNilReferenceIsNotRequired(t *testing.T) {
	ev := eoformula.New(0)
	// One rule per operator the pilot uses, so the fix is not one operator's.
	rules := []struct{ name, rule string }{
		{"matches", `delay_reason matches "\\S"`},
		{"equality", `delay_reason == "late"`},
		{"membership", `delay_reason in ["late", "very late"]`},
		{"comparison", `delay_reason > 3`},
	}
	var typedNil *string

	for _, r := range rules {
		t.Run(r.name, func(t *testing.T) {
			f := []models.EOFieldDef{{Key: "corrective_action", Label: "Corrective action",
				RequiredMode: models.EORequiredConditional, RequiredRule: r.rule}}

			for _, tc := range []struct {
				name   string
				stored map[string]any
				values map[string]any
			}{
				{"submitted nil", nil, map[string]any{"delay_reason": nil}},
				{"stored nil", map[string]any{"delay_reason": nil}, map[string]any{}},
				{"stored typed nil", map[string]any{"delay_reason": typedNil}, map[string]any{}},
				{"submitted nil clears a stored value", map[string]any{"delay_reason": "late"}, map[string]any{"delay_reason": nil}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					errs, err := ValidateRecordValues(f, tc.stored, tc.values, nil, nil, ev)
					if err != nil {
						t.Fatalf("err = %v, want none: a nil value is a value nobody supplied, not a broken rule", err)
					}
					if len(errs) != 0 {
						t.Fatalf("errs = %v, want none", errs)
					}
				})
			}
		})
	}

	t.Run("a value that is there still decides", func(t *testing.T) {
		f := []models.EOFieldDef{{Key: "corrective_action", Label: "Corrective action",
			RequiredMode: models.EORequiredConditional, RequiredRule: `delay_reason matches "\\S"`}}
		if errs := eoValidate(t, f, nil, map[string]any{"delay_reason": "late"}, nil, nil, ev); len(errs) != 1 {
			t.Fatalf("errs = %v, want corrective_action required", errs)
		}
		if errs := eoValidate(t, f, map[string]any{"delay_reason": "late"}, map[string]any{}, nil, nil, ev); len(errs) != 1 {
			t.Fatalf("errs = %v, want corrective_action required from the stored value", errs)
		}
		// Spaces are no delay: eoIsEmpty trims, and seed_eo_pilot.go:907
		// chose `matches "\S"` so the rule agrees with it.
		if errs := eoValidate(t, f, nil, map[string]any{"delay_reason": "   "}, nil, nil, ev); len(errs) != 0 {
			t.Fatalf("errs = %v, want none", errs)
		}
	})
}
