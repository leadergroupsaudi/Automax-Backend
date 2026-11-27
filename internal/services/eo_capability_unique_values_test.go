package services

import (
	"context"
	"testing"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// eoUniqueReader serves the stored rows unique_values reads: Record (the
// before-image) and RecordsOfTypeID (the rest of the type).
type eoUniqueReader struct {
	EOCapabilityReader
	stored map[uuid.UUID]models.EORecord
	ofType map[uuid.UUID][]models.EORecord
}

func (r *eoUniqueReader) Record(_ context.Context, id uuid.UUID) (*models.EORecord, error) {
	if rec, ok := r.stored[id]; ok {
		return &rec, nil
	}
	return nil, nil
}

func (r *eoUniqueReader) RecordsOfTypeID(_ context.Context, id uuid.UUID) ([]models.EORecord, error) {
	return r.ofType[id], nil
}

var eoUniqueSubjectCfg = datatypes.JSON(`{"keys":["subject_ar","subject_en"]}`)

func eoUniqueFixture() (typeID uuid.UUID, existing models.EORecord, read *eoUniqueReader) {
	typeID = uuid.New()
	existing = models.EORecord{ID: uuid.New(), RecordTypeID: typeID, Reference: "EO-DIR-2026-005",
		SubjectAr: "تحديث بروتوكول الزوار", SubjectEn: "Update the visitor protocol"}
	read = &eoUniqueReader{
		stored: map[uuid.UUID]models.EORecord{existing.ID: existing},
		ofType: map[uuid.UUID][]models.EORecord{typeID: {existing}},
	}
	return
}

func TestUniqueValuesRefusesADuplicateSubjectOnCreate(t *testing.T) {
	typeID, _, read := eoUniqueFixture()
	c := NewEOUniqueValuesCapability()
	// Different case and spacing is still the same subject.
	rec := &models.EORecord{ID: uuid.New(), RecordTypeID: typeID,
		SubjectAr: "عنوان آخر", SubjectEn: "  update the  Visitor protocol "}
	errs, err := c.Validate(context.Background(), read, rec, eoUniqueSubjectCfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := errs["subject_en"]; !ok {
		t.Fatalf("duplicate English subject accepted: %v", errs)
	}
	if _, ok := errs["subject_ar"]; ok {
		t.Fatalf("distinct Arabic subject refused: %v", errs)
	}
}

func TestUniqueValuesAcceptsADistinctSubjectAndBlanks(t *testing.T) {
	typeID, _, read := eoUniqueFixture()
	read.ofType[typeID] = append(read.ofType[typeID],
		models.EORecord{ID: uuid.New(), RecordTypeID: typeID, SubjectAr: "أ", SubjectEn: ""})
	c := NewEOUniqueValuesCapability()
	// Empty English subject on both records is not a duplicate.
	rec := &models.EORecord{ID: uuid.New(), RecordTypeID: typeID, SubjectAr: "ب", SubjectEn: ""}
	errs, err := c.Validate(context.Background(), read, rec, eoUniqueSubjectCfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 0 {
		t.Fatalf("distinct record refused: %v", errs)
	}
}

// Two duplicates that predate the capability must still be editable and
// transitionable: an unchanged value is not checked.
func TestUniqueValuesSkipsAnUnchangedValue(t *testing.T) {
	typeID, existing, read := eoUniqueFixture()
	twin := existing
	twin.ID = uuid.New()
	twin.Reference = "EO-DIR-2026-006"
	read.stored[twin.ID] = twin
	read.ofType[typeID] = append(read.ofType[typeID], twin)

	c := NewEOUniqueValuesCapability()
	saving := twin // the record as a transition would save it, subject untouched
	errs, err := c.Validate(context.Background(), read, &saving, eoUniqueSubjectCfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) != 0 {
		t.Fatalf("pre-existing duplicate refused on an unchanged save: %v", errs)
	}

	// Editing the subject TO another record's value is refused.
	other := models.EORecord{ID: uuid.New(), RecordTypeID: typeID, SubjectAr: "مختلف", SubjectEn: "Different"}
	read.stored[other.ID] = other
	read.ofType[typeID] = append(read.ofType[typeID], other)
	edit := other
	edit.SubjectEn = "Update the visitor protocol"
	errs, err = c.Validate(context.Background(), read, &edit, eoUniqueSubjectCfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := errs["subject_en"]; !ok {
		t.Fatalf("edit to a duplicate subject accepted: %v", errs)
	}
}

func TestUniqueValuesRefusesABadConfig(t *testing.T) {
	c := NewEOUniqueValuesCapability()
	for _, cfg := range []string{``, `{}`, `{"keys":[]}`, `{"keys":[" "]}`, `{"keys":["a","a"]}`, `{"key":["a"]}`} {
		if _, err := c.Validate(context.Background(), &eoUniqueReader{}, &models.EORecord{}, datatypes.JSON(cfg)); err == nil {
			t.Errorf("config %q accepted", cfg)
		}
	}
}
