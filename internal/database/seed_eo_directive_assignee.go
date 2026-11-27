package database

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// eoDirectiveAssigneeStepKey is a PERMANENT identifier; see the comment on
// eoPilotSteps. Never rename or reuse it.
const eoDirectiveAssigneeStepKey = "directive_assigned_user_replaces_responsible_officer"

const (
	eoDirectiveResponsibleOfficerKey = "responsible_officer"
	eoAssignedUserKey                = "assigned_user_id"
)

// eoDirectiveTaskFieldMapRewrites is what the step changes in the stored
// Directive -> Task spawn map: target key -> {old placeholder, new}.
//
//   - owner: the Task's owner came from the removed `responsible_officer`; it
//     now comes from the Directive's assigned user.
//   - source: the Directive's `source` is derived from `directive_origin`
//     (eoDeriveDirectiveSource), so the Task takes it from the field the
//     officer actually answers. The value is the same; the spawn check can
//     now prove it is always there.
var eoDirectiveTaskFieldMapRewrites = map[string][2]string{
	"owner":  {"{{" + eoDirectiveResponsibleOfficerKey + "}}", "{{" + eoAssignedUserKey + "}}"},
	"source": {"{{source}}", "{{" + eoDirectiveSourceFormula + "}}"},
}

// eoDirectiveAssigneeReplacesResponsibleOfficer stops the Directive asking
// for the same person twice.
//
// THE DEFECT. Every type inherits `assigned_user_id` ("Assigned user", the
// column My Assigned and the assignee transition rules read). The Directive
// also declared its own `responsible_officer` (matrix row 68, 1.3), required
// at creation, for the same person. The client ruled: keep Assigned user,
// remove Responsible officer.
//
// THE RULING, in one transaction:
//  1. the Directive gets its own `assigned_user_id`, copied from the base
//     definition but required ALWAYS, so a directive still cannot be
//     registered without the person who runs it;
//  2. the Directive's `responsible_officer` definition is soft-deleted, so the
//     form stops asking for it and a request can no longer write it;
//  3. the stored Directive -> Task spawn map is rewritten
//     (eoDirectiveTaskFieldMapRewrites) and checked by eoCheckSpawnFieldMap
//     against the configuration as it now stands. Without this the map would
//     name a field that no longer exists and every "Assign directly" would
//     commit and then fail to open its Task, post-commit and unretried.
//
// ONE-SHOT. Existing Directive records are NOT rewritten: a stored
// `responsible_officer` value stays in the values bag, unread and unshown, and
// an existing directive with no assigned user must be given one the next time
// it is edited.
func eoDirectiveAssigneeReplacesResponsibleOfficer(tx *gorm.DB) (bool, error) {
	var dir models.EORecordType
	if err := tx.Where("key = ?", eoDirectiveTypeKey).Limit(1).Find(&dir).Error; err != nil {
		return false, fmt.Errorf("find record type %q: %w", eoDirectiveTypeKey, err)
	}
	if dir.ID == uuid.Nil {
		return false, fmt.Errorf("record type %q is not seeded", eoDirectiveTypeKey)
	}
	if dir.ParentTypeID == nil {
		return false, fmt.Errorf("record type %q has no parent type, so it inherits no %q to require", eoDirectiveTypeKey, eoAssignedUserKey)
	}

	// 1. Assigned user, required on the Directive.
	var own models.EOFieldDef
	if err := tx.Where("record_type_id = ? AND key = ?", dir.ID, eoAssignedUserKey).Limit(1).Find(&own).Error; err != nil {
		return false, fmt.Errorf("read %s.%s: %w", eoDirectiveTypeKey, eoAssignedUserKey, err)
	}
	if own.ID != uuid.Nil {
		log.Printf("EO pilot: %s already defines its own %s (required %q); left as it is", eoDirectiveTypeKey, eoAssignedUserKey, own.RequiredMode)
	} else {
		var base models.EOFieldDef
		if err := tx.Where("record_type_id = ? AND key = ?", *dir.ParentTypeID, eoAssignedUserKey).Limit(1).Find(&base).Error; err != nil {
			return false, fmt.Errorf("read the inherited %s: %w", eoAssignedUserKey, err)
		}
		if base.ID == uuid.Nil {
			return false, fmt.Errorf("the parent of %s defines no %s to require", eoDirectiveTypeKey, eoAssignedUserKey)
		}
		def := models.EOFieldDef{
			RecordTypeID:   dir.ID,
			Key:            eoAssignedUserKey,
			Label:          base.Label,
			LabelAr:        base.LabelAr,
			Kind:           base.Kind,
			Config:         base.Config,
			Provenance:     models.EOProvenanceUser,
			RequiredMode:   models.EORequiredAlways,
			SortOrder:      base.SortOrder,
			HelpText:       "Who runs the directive inside the owning body.",
			HelpTextAr:     base.HelpTextAr,
			ArUntranslated: base.ArUntranslated,
		}
		if err := tx.Create(&def).Error; err != nil {
			return false, fmt.Errorf("require %s on %s: %w", eoAssignedUserKey, eoDirectiveTypeKey, err)
		}
		log.Printf("EO pilot: %s.%s is now required at creation", eoDirectiveTypeKey, eoAssignedUserKey)
	}

	// 2. Responsible officer goes.
	res := tx.Where("record_type_id = ? AND key = ?", dir.ID, eoDirectiveResponsibleOfficerKey).Delete(&models.EOFieldDef{})
	if res.Error != nil {
		return false, fmt.Errorf("remove %s.%s: %w", eoDirectiveTypeKey, eoDirectiveResponsibleOfficerKey, res.Error)
	}
	log.Printf("EO pilot: removed %s.%s (%d definition(s)); %s replaces it", eoDirectiveTypeKey, eoDirectiveResponsibleOfficerKey, res.RowsAffected, eoAssignedUserKey)

	// 3. The spawn map that read it.
	for _, spec := range eoPilotSpawnActions {
		if spec.sourceTypeKey != eoDirectiveTypeKey {
			continue
		}
		if err := eoRewriteDirectiveSpawnMap(tx, spec); err != nil {
			return false, fmt.Errorf("the spawn_record action on %s.%s: %w", spec.sourceTypeKey, spec.transitionCode, err)
		}
	}
	return true, nil
}

func eoRewriteDirectiveSpawnMap(tx *gorm.DB, spec eoSpawnSpec) error {
	var actions []models.TransitionAction
	if err := tx.Joins("JOIN workflow_transitions tr ON tr.id = transition_actions.transition_id").
		Joins("JOIN workflows w ON w.id = tr.workflow_id").
		Where("w.code = ? AND tr.code = ? AND transition_actions.action_type = ? AND transition_actions.name = ?",
			eoWorkflowCode(spec.sourceTypeKey), spec.transitionCode, eoSpawnRecordActionType, spec.name).
		Find(&actions).Error; err != nil {
		return fmt.Errorf("read the action: %w", err)
	}
	if len(actions) == 0 {
		log.Printf("EO pilot: no %q action on %s.%s to rewrite", spec.name, spec.sourceTypeKey, spec.transitionCode)
		return nil
	}
	for _, a := range actions {
		var cfg eoSpawnConfig
		if err := json.Unmarshal([]byte(a.Config), &cfg); err != nil {
			return fmt.Errorf("decode action %s config: %w", a.ID, err)
		}
		changed := false
		for key, rw := range eoDirectiveTaskFieldMapRewrites {
			if v, ok := cfg.FieldMap[key].(string); ok && v == rw[0] {
				cfg.FieldMap[key] = rw[1]
				changed = true
			}
		}
		// The map as it will be stored, checked against the configuration
		// as it now stands: a reference left pointing at the removed field
		// fails here, at boot, not after a transition has committed.
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
		if !changed {
			continue
		}
		blob, err := json.Marshal(cfg)
		if err != nil {
			return fmt.Errorf("encode action %s config: %w", a.ID, err)
		}
		if err := tx.Model(&models.TransitionAction{}).Where("id = ?", a.ID).Update("config", string(blob)).Error; err != nil {
			return fmt.Errorf("store action %s config: %w", a.ID, err)
		}
		log.Printf("EO pilot: the %q action now fills the Task's owner from %s and its source from %s", spec.name, eoAssignedUserKey, eoDirectiveSourceFormula)
	}
	return nil
}

// eoClientRulingOverride is a field a CLIENT RULING made differ from the
// matrix after the pilot was seeded: a type's own definition of an inherited
// key, written by a one-shot step. The matrix checks read it so the ruling is
// a recorded exception and not drift.
type eoClientRulingOverride struct {
	TypeKey, Key     string
	Mode, Provenance string
	Step             string
}

var eoClientRulingOverrides = []eoClientRulingOverride{
	{eoDirectiveTypeKey, eoAssignedUserKey, models.EORequiredAlways, models.EOProvenanceUser, eoDirectiveAssigneeStepKey},
}

// eoClientRulingRemovals is a type's own field a client ruling removed. Each
// is also in eoMatrixFieldsNotSeeded, which says what carries it instead.
var eoClientRulingRemovals = []struct{ TypeKey, Key string }{
	{eoDirectiveTypeKey, eoDirectiveResponsibleOfficerKey},
}

func eoClientRuling(typeKey, key string) (eoClientRulingOverride, bool) {
	for _, r := range eoClientRulingOverrides {
		if r.TypeKey == typeKey && r.Key == key {
			return r, true
		}
	}
	return eoClientRulingOverride{}, false
}

// eoEffectiveSpawnSpec is spec with the stored-map rewrites a client ruling
// made (eoDirectiveTaskFieldMapRewrites) applied: the map as a database that
// has run every step carries it.
func eoEffectiveSpawnSpec(spec eoSpawnSpec) eoSpawnSpec {
	out := spec
	out.fieldMap = make(map[string]string, len(spec.fieldMap))
	for k, v := range spec.fieldMap {
		out.fieldMap[k] = v
	}
	if spec.sourceTypeKey != eoDirectiveTypeKey {
		return out
	}
	for key, rw := range eoDirectiveTaskFieldMapRewrites {
		if out.fieldMap[key] == rw[0] {
			out.fieldMap[key] = rw[1]
		}
	}
	return out
}
