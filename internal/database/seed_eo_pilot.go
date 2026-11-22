package database

import (
	"fmt"
	"log"
	"sort"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ── the matrix-to-engine key map ────────────────────────────────────────────

// eoMatrixKeyEntry is one row of the translation table.
type eoMatrixKeyEntry struct {
	engineKey string
	// isColumn: the engine stores the value in a column of eo_records, not in
	// the values bag.
	isColumn bool
	// hasNoField: a pilot type must NOT declare a field under the matrix
	// spelling, because the matrix key is not a field of its own — it is
	// either a column on the record itself (ticket_type, state) or lives in
	// the values bag under the inherited definition (attachments).
	hasNoField bool
}

// eoMatrixKeys is the client field matrix's spelling of the seventeen
// inherited keys, mapped to the engine's. It is deliberately TOTAL over the
// seventeen — the eight keys spelled identically are listed too — so that
// TestEOMatrixKeyCoversTheBaseType can prove it covers exactly the keys the
// base type seeds. The six renames are the plan's Global Constraints table.
var eoMatrixKeys = map[string]eoMatrixKeyEntry{
	"reference":  {"reference", true, false},
	"subject_ar": {"subject_ar", true, false},
	"subject_en": {"subject_en", true, false},
	"priority":   {"priority", true, false},
	"source":     {"source", true, false},
	"due_date":   {"due_date", true, false},

	"change_reason": {"change_reason", true, false},
	"delay_reason":  {"delay_reason", true, false},

	// The six renames.
	"classification":   {"sensitivity", true, false},
	"owner_department": {"owning_body_id", true, false},
	"assigned_user":    {"assigned_user_id", true, false},
	"closed_on":        {"closed_at", true, false},
	"created_by":       {"created_by_id", true, false},
	"parent_ticket":    {"parent_record_id", true, false},

	// The three with no field of their own. The engine key is still reported
	// so a caller can say what the matrix key MEANS.
	"ticket_type": {"record_type_id", true, true},
	"state":       {"state_id", true, true},
	"attachments": {"attachments", false, true},
}

// eoMatrixKey translates a key as the client's field matrix spells it into the
// engine's. A pilot type's fields must be seeded under engineKey: a field
// keyed with the matrix spelling lands in the values bag and shadows nothing.
//
//	isColumn   — the value lives in an eo_records column.
//	hasNoField — do not declare a field for this matrix key at all.
//
// A key the table does not know passes through unchanged as a values-bag key
// (isColumn false, hasNoField false): type-specific matrix keys are the
// engine's keys already.
func eoMatrixKey(matrixKey string) (engineKey string, isColumn bool, hasNoField bool) {
	if e, ok := eoMatrixKeys[matrixKey]; ok {
		return e.engineKey, e.isColumn, e.hasNoField
	}
	return matrixKey, false, false
}

// ── the untranslated-label marker ───────────────────────────────────────────

// eoUntranslatedLabel sets a field's Label and LabelAr from ONE English
// string and marks it as awaiting Arabic sign-off. The English text stands in
// for the Arabic: a visibly-English label is findable by a person and by the
// query below, where an empty one would render as a blank form row.
//
// The marker is the eo_field_defs.ar_untranslated column; see
// eoUntranslatedFields for the query and the report for why a column rather
// than a help-text prefix.
func eoUntranslatedLabel(d models.EOFieldDef, english string) models.EOFieldDef {
	d.Label = english
	d.LabelAr = english
	d.ArUntranslated = true
	return d
}

// eoUntranslatedField names one field awaiting Arabic sign-off.
type eoUntranslatedField struct {
	TypeKey  string
	FieldKey string
}

// eoUntranslatedFields is the set, in a stable order.
func eoUntranslatedFields(db *gorm.DB) ([]eoUntranslatedField, error) {
	var out []eoUntranslatedField
	err := db.Table("eo_field_defs AS f").
		Select("t.key AS type_key, f.key AS field_key").
		Joins("JOIN eo_record_types t ON t.id = f.record_type_id AND t.deleted_at IS NULL").
		Where("f.ar_untranslated = ? AND f.deleted_at IS NULL", true).
		Order("t.key, f.sort_order, f.key").
		Scan(&out).Error
	if err != nil {
		return nil, fmt.Errorf("list untranslated field labels: %w", err)
	}
	return out, nil
}

// ── the pilot seed ──────────────────────────────────────────────────────────

// eoSmokeTypeKeys are the two record types of Plan 1's smoke configuration.
var eoSmokeTypeKeys = []string{eoSmokeParentTypeKey, eoSmokeChildTypeKey}

// seedEOPilot applies Plan 2's configuration on top of the platform seed. In
// this task that is the scaffolding only: it removes Plan 1's smoke
// configuration and marks the inherited labels as awaiting Arabic sign-off.
// Later tasks add their types as further steps of eoSeedPilotTx.
//
// Same shape as seedEOPlatform and for the same reason: a cheap check outside
// the transaction is the boot fast path, and everything else runs inside one
// transaction with every step returning its error, so a half-applied pilot
// seed cannot leave a marker that says it is done.
//
// It must run AFTER seedEOPlatform (it removes what that seed wrote).
func seedEOPilot(db *gorm.DB) {
	needed, err := eoPilotSeedNeeded(db)
	if err != nil {
		log.Printf("EO pilot seed: could not check whether it is needed: %v", err)
		return
	}
	if !needed {
		return
	}
	log.Println("Applying the EO pilot configuration...")
	if err := db.Transaction(eoSeedPilotTx); err != nil {
		log.Printf("EO pilot seed FAILED and was rolled back: %v", err)
		log.Printf("EO pilot: PILOT CONFIGURATION WAS NOT APPLIED. This will repeat on every boot until the cause is removed.")
		return
	}
	if set, err := eoUntranslatedFields(db); err != nil {
		log.Printf("EO pilot seed: %v", err)
	} else if len(set) > 0 {
		log.Printf("EO pilot: %d field labels await Arabic sign-off (eo_field_defs.ar_untranslated)", len(set))
	}
}

// eoPilotSeedNeeded is the fast path: two counts. Work is needed while a smoke
// type exists (either to remove it, or — when it holds records — to say again
// that it was kept), or while the base type exists and nothing has ever been
// marked.
func eoPilotSeedNeeded(db *gorm.DB) (bool, error) {
	var smoke int64
	if err := db.Model(&models.EORecordType{}).Where("key IN ?", eoSmokeTypeKeys).Count(&smoke).Error; err != nil {
		return false, err
	}
	if smoke > 0 {
		return true, nil
	}
	var base, marked int64
	if err := db.Model(&models.EORecordType{}).Where("key = ?", eoBaseTypeKey).Count(&base).Error; err != nil {
		return false, err
	}
	if base == 0 {
		// The platform seed has not run (EO_PLATFORM off, or it rolled back);
		// there is nothing to build on.
		return false, nil
	}
	if err := db.Model(&models.EOFieldDef{}).Where("ar_untranslated = ?", true).Count(&marked).Error; err != nil {
		return false, err
	}
	return marked == 0, nil
}

func eoSeedPilotTx(tx *gorm.DB) error {
	if err := eoRemoveSmokeConfiguration(tx); err != nil {
		return err
	}
	return eoMarkBaseLabelsUnsigned(tx)
}

// eoMarkBaseLabelsUnsigned marks the base type's inherited labels as awaiting
// sign-off: the Arabic on them was written by an implementer, not supplied by
// the client (docs/superpowers/eo-plan1-carry-forward.md). It runs only while
// NOTHING in the database is marked, so it fills in a database that predates
// the column and never re-marks a label an administrator has since approved.
// (A fresh database never needs it: eoBaseFieldDefs marks them at creation.)
func eoMarkBaseLabelsUnsigned(tx *gorm.DB) error {
	var marked int64
	if err := tx.Model(&models.EOFieldDef{}).Where("ar_untranslated = ?", true).Count(&marked).Error; err != nil {
		return fmt.Errorf("count marked field labels: %w", err)
	}
	if marked > 0 {
		return nil
	}
	var base models.EORecordType
	if err := tx.Where("key = ?", eoBaseTypeKey).Limit(1).Find(&base).Error; err != nil {
		return fmt.Errorf("read base record type: %w", err)
	}
	if base.ID == uuid.Nil {
		return nil
	}
	if err := tx.Model(&models.EOFieldDef{}).Where("record_type_id = ?", base.ID).
		Update("ar_untranslated", true).Error; err != nil {
		return fmt.Errorf("mark inherited labels: %w", err)
	}
	return nil
}

// ── removing Plan 1's smoke configuration ───────────────────────────────────

// eoRemoveSmokeConfiguration deletes eo_smoke_directive and eo_smoke_task and
// everything the platform seed hung off them — field definitions, the link
// type, both workflows (states, transitions, requirements, field changes), the
// escalation policy, and the six per-type permission codes — but ONLY while
// nothing holds a record under them.
//
// A type with records is protected by spec §7.14 (it cannot be deleted,
// re-keyed, re-parented or re-pointed), and a dev box with demo data is not an
// error: with records present this changes nothing, says so, and returns nil.
// It is all-or-nothing: removing one smoke type and keeping the other would
// strand the link type between them.
//
// Hard deletes (Unscoped) throughout: the escalation policy name is on a unique
// index that a soft-deleted row still occupies, and a leftover would make
// every later boot's EO seed collide and roll back.
func eoRemoveSmokeConfiguration(tx *gorm.DB) error {
	var smoke []models.EORecordType
	if err := tx.Where("key IN ?", eoSmokeTypeKeys).Find(&smoke).Error; err != nil {
		return fmt.Errorf("find smoke record types: %w", err)
	}
	if len(smoke) == 0 {
		return nil
	}
	smokeIDs := make([]uuid.UUID, 0, len(smoke))
	for _, t := range smoke {
		smokeIDs = append(smokeIDs, t.ID)
	}

	var subtree []uuid.UUID
	if err := tx.Raw(`
		WITH RECURSIVE sub AS (
			SELECT id FROM eo_record_types WHERE id IN ? AND deleted_at IS NULL
			UNION
			SELECT t.id FROM eo_record_types t JOIN sub ON t.parent_type_id = sub.id WHERE t.deleted_at IS NULL
		) SELECT id FROM sub`, smokeIDs).Scan(&subtree).Error; err != nil {
		return fmt.Errorf("walk the smoke types' subtree: %w", err)
	}

	var holders []struct {
		Key string
		N   int64
	}
	if err := tx.Raw(`
		SELECT t.key AS key, COUNT(*) AS n
		FROM eo_records r JOIN eo_record_types t ON t.id = r.record_type_id
		WHERE r.record_type_id IN ? GROUP BY t.key ORDER BY t.key`, subtree).Scan(&holders).Error; err != nil {
		return fmt.Errorf("count records under the smoke types: %w", err)
	}
	if len(holders) > 0 {
		desc := make([]string, 0, len(holders))
		for _, h := range holders {
			desc = append(desc, fmt.Sprintf("%s (%d)", h.Key, h.N))
		}
		sort.Strings(desc)
		log.Printf("EO pilot: the smoke record types were KEPT because records exist under them: %v. They cannot be removed while populated (spec §7.14); delete those records and restart to remove them.", desc)
		return nil
	}
	if len(subtree) != len(smokeIDs) {
		log.Printf("EO pilot: the smoke record types were KEPT because other record types are defined beneath them (%d in the subtree, %d smoke types).", len(subtree), len(smokeIDs))
		return nil
	}

	// Workflows bound to the smoke types.
	var workflowIDs []uuid.UUID
	if err := tx.Unscoped().Model(&models.Workflow{}).Where("record_type_id IN ?", smokeIDs).Pluck("id", &workflowIDs).Error; err != nil {
		return fmt.Errorf("find smoke workflows: %w", err)
	}
	// The type's own workflow_id points at the workflow; release it first so
	// neither delete order trips a foreign key.
	if err := tx.Model(&models.EORecordType{}).Where("id IN ?", smokeIDs).Update("workflow_id", nil).Error; err != nil {
		return fmt.Errorf("unbind smoke workflows: %w", err)
	}

	var policyIDs []uuid.UUID
	if len(workflowIDs) > 0 {
		if err := tx.Unscoped().Model(&models.WorkflowState{}).
			Where("workflow_id IN ? AND escalation_policy_id IS NOT NULL", workflowIDs).
			Distinct().Pluck("escalation_policy_id", &policyIDs).Error; err != nil {
			return fmt.Errorf("find smoke escalation policies: %w", err)
		}
		var transitions []models.WorkflowTransition
		if err := tx.Unscoped().Where("workflow_id IN ?", workflowIDs).Find(&transitions).Error; err != nil {
			return fmt.Errorf("find smoke transitions: %w", err)
		}
		for i := range transitions {
			tr := &transitions[i]
			if err := tx.Model(tr).Association("AllowedRoles").Clear(); err != nil {
				return fmt.Errorf("clear allowed roles of transition %q: %w", tr.Code, err)
			}
			if err := tx.Model(tr).Association("AssignmentRoles").Clear(); err != nil {
				return fmt.Errorf("clear assignment roles of transition %q: %w", tr.Code, err)
			}
			if err := tx.Unscoped().Where("transition_id = ?", tr.ID).Delete(&models.TransitionRequirement{}).Error; err != nil {
				return fmt.Errorf("delete requirements of transition %q: %w", tr.Code, err)
			}
			if err := tx.Unscoped().Where("transition_id = ?", tr.ID).Delete(&models.TransitionFieldChange{}).Error; err != nil {
				return fmt.Errorf("delete field changes of transition %q: %w", tr.Code, err)
			}
		}
		if err := tx.Unscoped().Where("workflow_id IN ?", workflowIDs).Delete(&models.WorkflowTransition{}).Error; err != nil {
			return fmt.Errorf("delete smoke transitions: %w", err)
		}
		if err := tx.Unscoped().Where("workflow_id IN ?", workflowIDs).Delete(&models.WorkflowState{}).Error; err != nil {
			return fmt.Errorf("delete smoke states: %w", err)
		}
		if err := tx.Unscoped().Where("id IN ?", workflowIDs).Delete(&models.Workflow{}).Error; err != nil {
			return fmt.Errorf("delete smoke workflows: %w", err)
		}
	}

	// The policy is found by the states that pointed at it AND by its name: a
	// half-removal that left the name behind is the collision this guards.
	var byName []uuid.UUID
	if err := tx.Unscoped().Model(&models.EscalationPolicy{}).Where("name = ?", eoSmokePolicyName).Pluck("id", &byName).Error; err != nil {
		return fmt.Errorf("find the smoke escalation policy by name: %w", err)
	}
	policyIDs = append(policyIDs, byName...)
	if len(policyIDs) > 0 {
		var stillUsed int64
		if err := tx.Model(&models.WorkflowState{}).Where("escalation_policy_id IN ?", policyIDs).Count(&stillUsed).Error; err != nil {
			return fmt.Errorf("check the smoke policy is unused: %w", err)
		}
		if stillUsed > 0 {
			return fmt.Errorf("escalation policy %q is still referenced by %d workflow states outside the smoke workflows; refusing to delete it", eoSmokePolicyName, stillUsed)
		}
		if err := tx.Unscoped().Where("policy_id IN ?", policyIDs).Delete(&models.EscalationPolicyStep{}).Error; err != nil {
			return fmt.Errorf("delete smoke escalation steps: %w", err)
		}
		if err := tx.Unscoped().Where("id IN ?", policyIDs).Delete(&models.EscalationPolicy{}).Error; err != nil {
			return fmt.Errorf("delete smoke escalation policy: %w", err)
		}
	}

	if err := tx.Unscoped().Where("key = ?", eoSmokeLinkTypeKey).Delete(&models.EOLinkType{}).Error; err != nil {
		return fmt.Errorf("delete smoke link type: %w", err)
	}
	if err := tx.Unscoped().Where("record_type_id IN ?", smokeIDs).Delete(&models.EOFieldDef{}).Error; err != nil {
		return fmt.Errorf("delete smoke field definitions: %w", err)
	}

	// The six per-type permission codes, and their grants.
	var codes []string
	for _, k := range eoSmokeTypeKeys {
		for _, a := range []string{"view", "create", "update"} {
			codes = append(codes, fmt.Sprintf("eo:%s:%s", k, a))
		}
	}
	var permIDs []uuid.UUID
	if err := tx.Unscoped().Model(&models.Permission{}).Where("code IN ?", codes).Pluck("id", &permIDs).Error; err != nil {
		return fmt.Errorf("find smoke permissions: %w", err)
	}
	if len(permIDs) > 0 {
		if err := tx.Exec(`DELETE FROM role_permissions WHERE permission_id IN ?`, permIDs).Error; err != nil {
			return fmt.Errorf("revoke smoke permissions: %w", err)
		}
		if err := tx.Unscoped().Where("id IN ?", permIDs).Delete(&models.Permission{}).Error; err != nil {
			return fmt.Errorf("delete smoke permissions: %w", err)
		}
	}

	if err := tx.Unscoped().Where("id IN ?", smokeIDs).Delete(&models.EORecordType{}).Error; err != nil {
		return fmt.Errorf("delete smoke record types: %w", err)
	}
	log.Println("EO pilot: removed Plan 1's smoke configuration (eo_smoke_directive, eo_smoke_task)")
	return nil
}

// seedEOPilotWhenEnabled is the gate Seed applies, a function so that the gate
// and the call site are both under test. The pilot seed is part of the EO
// platform: it runs only where the platform does.
func seedEOPilotWhenEnabled(db *gorm.DB, enabled bool) {
	if !enabled {
		return
	}
	seedEOPilot(db)
}
