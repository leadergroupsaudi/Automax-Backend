package services

import (
	"context"
	"testing"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// The Directive overrides the inherited `source` with a field derived from
// `directive_origin` (database.eoDeriveDirectiveSource). These pin what that
// configuration does through the engine's own resolution, validation and
// derivation.
func eoDirectiveSourceFields(t *testing.T) []models.EOFieldDef {
	t.Helper()
	base := models.EORecordType{ID: uuid.New(), Key: "eo_base", IsAbstract: true}
	dir := models.EORecordType{ID: uuid.New(), Key: "eo_directive", ParentTypeID: &base.ID}
	defs := []models.EOFieldDef{
		{RecordTypeID: base.ID, Key: "subject_ar", Kind: models.EOKindText,
			Provenance: models.EOProvenanceUser, RequiredMode: models.EORequiredAlways, SortOrder: 1},
		{RecordTypeID: base.ID, Key: "source", Kind: models.EOKindDropdown,
			Provenance: models.EOProvenanceUser, RequiredMode: models.EORequiredAlways, SortOrder: 9},
		{RecordTypeID: dir.ID, Key: "directive_origin", Kind: models.EOKindDropdown,
			Provenance: models.EOProvenanceUser, RequiredMode: models.EORequiredAlways, SortOrder: 1},
		{RecordTypeID: dir.ID, Key: "source", Kind: models.EOKindDropdown,
			Provenance: models.EOProvenanceDerived, RequiredMode: models.EORequiredNever, SortOrder: 9,
			Formula: "directive_origin", RecomputeOn: datatypes.JSON(`[{"field": "directive_origin", "scope": "own"}]`)},
	}
	fields, err := ResolveEffectiveFields(&dir, []models.EORecordType{base}, defs)
	if err != nil {
		t.Fatal(err)
	}
	return fields
}

func TestDirectiveSourceIsOverriddenOnce(t *testing.T) {
	n := 0
	for _, f := range eoDirectiveSourceFields(t) {
		if f.Key == "source" {
			n++
			if f.Provenance != models.EOProvenanceDerived {
				t.Fatalf("the Directive's source resolved to provenance %q, want derived", f.Provenance)
			}
		}
	}
	if n != 1 {
		t.Fatalf("source appears %d times in the effective fields, want 1", n)
	}
}

// A create that sends no source is valid; one that sends it is refused.
func TestDirectiveCreateNeedsNoSourceAndCannotWriteIt(t *testing.T) {
	fields := eoDirectiveSourceFields(t)
	ok := map[string]any{"subject_ar": "تحديث", "directive_origin": "committee"}
	errs, err := ValidateRecordValues(fields, map[string]any{}, ok, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 0 {
		t.Fatalf("create without source refused: %v", errs)
	}

	withSource := map[string]any{"subject_ar": "تحديث", "directive_origin": "committee", "source": "weekly_meeting"}
	errs, err = ValidateRecordValues(fields, map[string]any{}, withSource, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if errs["source"] == "" {
		t.Fatalf("a request was allowed to write the derived source: %v", errs)
	}
}

func TestDirectiveSourceIsDerivedFromOrigin(t *testing.T) {
	fields := eoDirectiveSourceFields(t)
	rec := &models.EORecord{ID: uuid.New(), SubjectAr: "تحديث"}
	if errs := eoApplyValues(rec, map[string]any{"directive_origin": "committee"}); len(errs) > 0 {
		t.Fatal(errs)
	}
	got, err := RecomputeDerived(context.Background(), fields, rec, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got["source"] != "committee" {
		t.Fatalf("source derived as %v, want committee", got["source"])
	}
}
