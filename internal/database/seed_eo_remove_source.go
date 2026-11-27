package database

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/automax/backend/internal/models"
	"gorm.io/gorm"
)

// eoRemoveInheritedSourceStepKey is a PERMANENT identifier; see the comment on
// eoPilotSteps. Never rename or reuse it.
const eoRemoveInheritedSourceStepKey = "remove_inherited_source_field"

// eoInheritedSourceKey is the base field the client ruled out.
const eoInheritedSourceKey = "source"

// eoDirectiveOriginLabel is the Directive's `directive_origin` as the client
// now names it: it is the only "source" a record carries.
const (
	eoDirectiveOriginOldLabel = "Directive origin"
	eoDirectiveOriginLabel    = "Directive source"
)

// eoInheritedFieldsRemovedByRuling are base fields a client ruling removed from
// every record type after the matrix was written. The matrix checks skip them.
var eoInheritedFieldsRemovedByRuling = []string{eoInheritedSourceKey}

func eoInheritedFieldRemovedByRuling(engineKey string) bool {
	for _, k := range eoInheritedFieldsRemovedByRuling {
		if k == engineKey {
			return true
		}
	}
	return false
}

// eoRemoveInheritedSource removes the inherited `source` field.
//
// THE RULING. "Source" (matrix row 54, inherited by every type) is removed;
// the only source a record carries is the Directive's own `directive_origin`,
// now labelled "Directive source". eoBaseFieldDefs no longer seeds it, so a
// fresh database never has it; this step brings an existing one level:
//
//  1. every `source` field definition of an EO record type is soft-deleted —
//     the base one, and the Directive's derived override of it
//     (eoDeriveDirectiveSource), which has nothing left to override;
//  2. the stored Directive -> Task spawn map stops writing `source`, which the
//     Task no longer defines (the executor would refuse the whole spawn), and
//     is re-checked by eoCheckSpawnFieldMap;
//  3. `directive_origin` is relabelled "Directive source", unless an
//     administrator has already renamed it.
//
// THE COLUMN STAYS. eo_records.source is NOT NULL DEFAULT ” and is left in
// place: dropping it is a destructive migration for no gain. Nothing writes it
// any more (no field defines it, so a request naming it is refused), and
// stored values are left as they were.
//
// ONE-SHOT.
func eoRemoveInheritedSource(tx *gorm.DB) (bool, error) {
	// 1. the field, everywhere it is defined.
	res := tx.Where("key = ? AND record_type_id IN (SELECT id FROM eo_record_types)", eoInheritedSourceKey).
		Delete(&models.EOFieldDef{})
	if res.Error != nil {
		return false, fmt.Errorf("remove the `source` field definitions: %w", res.Error)
	}
	log.Printf("EO pilot: removed the inherited `source` field (%d definition(s)); Directive source (directive_origin) is the only source a record carries", res.RowsAffected)

	// 2. the spawn map that wrote it.
	for _, spec := range eoPilotSpawnActions {
		if err := eoDropSpawnMapKey(tx, spec, eoInheritedSourceKey); err != nil {
			return false, fmt.Errorf("the spawn_record action on %s.%s: %w", spec.sourceTypeKey, spec.transitionCode, err)
		}
	}

	// 3. the label.
	var dir struct{ ID string }
	if err := tx.Raw(`SELECT id FROM eo_record_types WHERE key = ?`, eoDirectiveTypeKey).Scan(&dir).Error; err != nil {
		return false, fmt.Errorf("find record type %q: %w", eoDirectiveTypeKey, err)
	}
	if dir.ID != "" {
		res := tx.Model(&models.EOFieldDef{}).
			Where("record_type_id = ? AND key = ? AND label = ?", dir.ID, eoDirectiveSourceFormula, eoDirectiveOriginOldLabel).
			Updates(map[string]any{"label": eoDirectiveOriginLabel})
		if res.Error != nil {
			return false, fmt.Errorf("relabel %s.%s: %w", eoDirectiveTypeKey, eoDirectiveSourceFormula, res.Error)
		}
		// The Arabic label is the English one until the client signs it off
		// (eoUntranslatedLabel); keep the two in step while it is.
		if err := tx.Model(&models.EOFieldDef{}).
			Where("record_type_id = ? AND key = ? AND label_ar = ? AND ar_untranslated", dir.ID, eoDirectiveSourceFormula, eoDirectiveOriginOldLabel).
			Update("label_ar", eoDirectiveOriginLabel).Error; err != nil {
			return false, fmt.Errorf("relabel %s.%s (Arabic): %w", eoDirectiveTypeKey, eoDirectiveSourceFormula, err)
		}
		if res.RowsAffected > 0 {
			log.Printf("EO pilot: %s.%s is now labelled %q", eoDirectiveTypeKey, eoDirectiveSourceFormula, eoDirectiveOriginLabel)
		}
	}
	return true, nil
}

// eoDropSpawnMapKey removes key from the stored field map of spec's action(s)
// and re-checks what is left against the configuration as it now stands.
func eoDropSpawnMapKey(tx *gorm.DB, spec eoSpawnSpec, key string) error {
	var actions []models.TransitionAction
	if err := tx.Joins("JOIN workflow_transitions tr ON tr.id = transition_actions.transition_id").
		Joins("JOIN workflows w ON w.id = tr.workflow_id").
		Where("w.code = ? AND tr.code = ? AND transition_actions.action_type = ? AND transition_actions.name = ?",
			eoWorkflowCode(spec.sourceTypeKey), spec.transitionCode, eoSpawnRecordActionType, spec.name).
		Find(&actions).Error; err != nil {
		return fmt.Errorf("read the action: %w", err)
	}
	for _, a := range actions {
		var cfg eoSpawnConfig
		if err := json.Unmarshal([]byte(a.Config), &cfg); err != nil {
			return fmt.Errorf("decode action %s config: %w", a.ID, err)
		}
		_, had := cfg.FieldMap[key]
		delete(cfg.FieldMap, key)

		check := spec
		check.fieldMap = make(map[string]string, len(cfg.FieldMap))
		for k, v := range cfg.FieldMap {
			s, ok := v.(string)
			if !ok {
				return fmt.Errorf("action %s maps %q to a non-string %v", a.ID, k, v)
			}
			check.fieldMap[k] = s
		}
		if err := eoCheckSpawnFieldMap(tx, check); err != nil {
			return err
		}
		if !had {
			continue
		}
		blob, err := json.Marshal(cfg)
		if err != nil {
			return fmt.Errorf("encode action %s config: %w", a.ID, err)
		}
		if err := tx.Model(&models.TransitionAction{}).Where("id = ?", a.ID).Update("config", string(blob)).Error; err != nil {
			return fmt.Errorf("store action %s config: %w", a.ID, err)
		}
		log.Printf("EO pilot: the %q action no longer writes %q", spec.name, key)
	}
	return nil
}
