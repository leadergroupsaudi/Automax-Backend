package services

import (
	"fmt"
	"strings"
	"testing"

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
		t.Fatalf("keys = %s, want title,zeta,own (override keeps ancestor slot, once)", eoKeys(got))
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
