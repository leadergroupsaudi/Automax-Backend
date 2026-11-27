package database

import (
	"fmt"
	"log"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// eoDeriveDirectiveSourceStepKey is a PERMANENT identifier; see the comment
// on eoPilotSteps. Never rename or reuse it.
const eoDeriveDirectiveSourceStepKey = "derive_directive_source_from_origin"

// eoDirectiveSourceFormula is the whole formula: the Directive's source IS its
// origin. RecomputeOn names the one field it reads, on the record itself, the
// same shape eo_referral.visible_to uses for referred_body.
const (
	eoDirectiveSourceFormula     = "directive_origin"
	eoDirectiveSourceRecomputeOn = `[{"field": "directive_origin", "scope": "own"}]`
)

// eoDeriveDirectiveSource stops the Directive's create form asking the same
// question twice.
//
// THE DEFECT. Every type inherits `source` from eo_base ("how the record
// reached the Office", matrix row 54), required at creation. The Directive
// also declares `directive_origin` (matrix row 74, CFF step 1.2, "where the
// directive came from"), required at creation, and the seed gave both the
// same six options. An officer registering a directive answered the same
// question in two dropdowns, and could answer them differently.
//
// THE RULING. `directive_origin` stays: it is the CFF's own field and
// `origin_record`'s conditional rule reads it. `source` cannot be removed —
// it is a real eo_records column, the register's Source filter and column
// read it, and every other type keeps it as a user field — so on the
// Directive ALONE it is overridden (ResolveEffectiveFields: a type's own
// definition of an inherited key replaces the ancestor's, in the ancestor's
// position) by a DERIVED field whose formula is `directive_origin`. The form
// stops rendering it (the client treats a derived field as read-only), a
// request can no longer write it, and the engine fills it on create, on every
// edit and on every transition.
//
// ONE-SHOT, and it inserts only: if the Directive already has its own
// `source` definition — an administrator's, through the designer — it is left
// alone and the step says so. Existing Directive records are NOT rewritten:
// their `source` was entered by hand and is left as the officer gave it until
// the record is next saved, when the derivation recomputes it. No record is
// written without a history row.
func eoDeriveDirectiveSource(tx *gorm.DB) (bool, error) {
	var dir models.EORecordType
	if err := tx.Where("key = ?", eoDirectiveTypeKey).Limit(1).Find(&dir).Error; err != nil {
		return false, fmt.Errorf("find record type %q: %w", eoDirectiveTypeKey, err)
	}
	if dir.ID == uuid.Nil {
		return false, fmt.Errorf("record type %q is not seeded, so its source cannot be derived", eoDirectiveTypeKey)
	}
	if dir.ParentTypeID == nil {
		return false, fmt.Errorf("record type %q has no parent type, so it inherits no `source` to override", eoDirectiveTypeKey)
	}

	var own []models.EOFieldDef
	if err := tx.Where("record_type_id = ? AND key IN ?", dir.ID, []string{"source", eoDirectiveSourceFormula}).
		Find(&own).Error; err != nil {
		return false, fmt.Errorf("read %s field definitions: %w", eoDirectiveTypeKey, err)
	}
	hasOrigin := false
	for _, f := range own {
		switch f.Key {
		case "source":
			log.Printf("EO pilot: %s already defines its own `source` (provenance %q); left as it is", eoDirectiveTypeKey, f.Provenance)
			return true, nil
		case eoDirectiveSourceFormula:
			hasOrigin = true
		}
	}
	if !hasOrigin {
		return false, fmt.Errorf("%s has no %q field, which its derived `source` would read", eoDirectiveTypeKey, eoDirectiveSourceFormula)
	}

	// The inherited definition: its labels, kind, options and position are
	// kept, so the record page shows the same "Source" with the same option
	// labels it showed before.
	var base models.EOFieldDef
	if err := tx.Where("record_type_id = ? AND key = ?", *dir.ParentTypeID, "source").Limit(1).Find(&base).Error; err != nil {
		return false, fmt.Errorf("read the inherited `source` definition: %w", err)
	}
	if base.ID == uuid.Nil {
		return false, fmt.Errorf("the parent of %s defines no `source` field to override", eoDirectiveTypeKey)
	}

	def := models.EOFieldDef{
		RecordTypeID:   dir.ID,
		Key:            "source",
		Label:          base.Label,
		LabelAr:        base.LabelAr,
		Kind:           base.Kind,
		Config:         base.Config,
		Provenance:     models.EOProvenanceDerived,
		RequiredMode:   models.EORequiredNever,
		Formula:        eoDirectiveSourceFormula,
		RecomputeOn:    datatypes.JSON(eoDirectiveSourceRecomputeOn),
		SortOrder:      base.SortOrder,
		HelpText:       "Filled from Directive origin.",
		HelpTextAr:     base.HelpTextAr,
		ArUntranslated: base.ArUntranslated,
	}
	if err := tx.Create(&def).Error; err != nil {
		return false, fmt.Errorf("create the derived `source` on %s: %w", eoDirectiveTypeKey, err)
	}
	log.Printf("EO pilot: %s.source is now derived from %s; the create form no longer asks for it", eoDirectiveTypeKey, eoDirectiveSourceFormula)
	return true, nil
}
