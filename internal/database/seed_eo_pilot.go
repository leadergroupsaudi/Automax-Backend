package database

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"

	"github.com/automax/backend/internal/database/migrations"
	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// ── the matrix-to-engine key map ────────────────────────────────────────────

// eoMatrixKeyEntry is one row of the translation table.
type eoMatrixKeyEntry struct {
	engineKey string
	// isColumn: the engine stores the value in a column of eo_records, not in
	// the values bag.
	isColumn bool
	// isEngineManaged: the engine owns this key — record identity
	// (ticket_type) or workflow state (state). The BASE type does seed
	// definitions named record_type_id and state_id (both engine-written), so
	// this does NOT mean "there is no definition"; it means a pilot type must
	// never declare one, because the engine, not a form, decides the value.
	isEngineManaged bool
}

// eoMatrixKeys is the client field matrix's spelling of the seventeen
// inherited keys, mapped to the engine's, PLUS the type-specific renames a
// pilot type needs. The inherited block is deliberately TOTAL over the
// seventeen — the keys spelled identically are listed too — so that
// TestEOMatrixKeyCoversTheBaseType can prove it covers exactly the keys the
// base type seeds; that test reads the inherited seventeen and is unaffected
// by entries below them. The six renames are the plan's Global Constraints
// table.
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

	// The two with no field of their own. The engine key is still reported
	// so a caller can say what the matrix key MEANS.
	"ticket_type": {"record_type_id", true, true},
	"state":       {"state_id", true, true},

	// `attachments` is a VALUES-BAG key like any other, and a pilot type may
	// declare it. It was marked engine-managed in Task A1 and that was wrong:
	// it is absent from services.eoEngineOwnedKeys, from
	// services.eoRecordColumnWriters and from services.eoFlattenStored, so
	// eoApplyValues routes it into the bag exactly as it routes a user field,
	// and services.EOTransitionInput.Attachments is a separate thing (its ids
	// are checked for presence and dropped, never stored here). The
	// consequence of the old marking was concrete: the matrix marks
	// `attachments` **Yes** on Incoming Correspondence and the seed could not
	// say so, so the pilot would file a government letter with no letter
	// attached. Corrected in A2's fix round.
	"attachments": {"attachments", false, false},

	// TYPE-SPECIFIC RENAMES. The table above is total over the seventeen
	// INHERITED keys; these are keys one pilot type spells its own way for a
	// field the engine already has. A type-specific key is normally the
	// engine's key already (the fall-through below), so an entry here is a
	// deliberate statement that two names are one field.
	//
	// TASK A3's five. Each is a matrix cell an ENGINE MECHANISM reads or
	// writes UNDER A DIFFERENT SPELLING — four of them an eo_records COLUMN,
	// and one (`visibility`) a values-bag key the repository reads by a name
	// of its own. Seeded under the matrix spelling, the four WRITTEN ones
	// would be values-bag keys that shadow nothing, sitting empty beside the
	// column the engine had already written; the one READ one would be a
	// confidentiality control that looks configured while the mechanism
	// searches for a word that is not there. Nothing reports either. See
	// eoEngineKeyDecisions, which names the mechanism for each, says which
	// direction the value travels, and is checked against the seeded rows.
	//
	// Referral's `deadline` is "Derived / Counted back from the sender's
	// deadline" (matrix:300) and services.DeriveDeadlineFromParent, the only
	// mechanism that derives one, writes the child's rec.DueDate COLUMN.
	"deadline": {"due_date", true, false},
	// Decision Record's `decision_number` and Outgoing Reply's `out_ref` are
	// both "Auto number / Auto" with the type's OWN reference pattern in the
	// Options cell (DEC-YYYY-NNN at matrix:171, OUT-YYYY-NNNN at matrix:316).
	// That is the engine's `reference`: eoRecordService.allocateReference
	// renders the type's ReferencePattern into rec.Reference on create. A
	// second auto-number field beside it would be a number nothing allocates.
	"decision_number": {"reference", true, false},
	"out_ref":         {"reference", true, false},
	// Decision Record's `source_study` is "Link (ticket) / Auto / The parent
	// draft study" (matrix:179) — the record's parent, which is
	// parent_record_id, taken from the create request.
	"source_study": {"parent_record_id", true, false},
	// Referral's `visibility` is the one rename that is NOT a column, and it
	// is why this table's rule is "a different SPELLING" rather than "a
	// column". Spec §7.11's row-level visibility is LIVE and reads one values
	// key: repository.eoVisibleToKey = "visible_to"
	// (internal/repository/eo_record_repository.go:295), enforced by
	// applyEORecordVisibility against EORecordFilter.VisibleToDepartmentIDs,
	// which the record handler sets from the caller's bodies on every list
	// path. That constant's own doc comment quotes this matrix row: "the
	// values key a record carries when it is restricted to one body".
	//
	// Seeded as `visibility` the field is a values-bag key the engine has
	// never heard of: the configuration would LOOK like a confidentiality
	// control while every Referral shipped unrestricted, in a government
	// register, with nothing reporting it — and models.ValidateTypeEdit makes
	// the key permanent at the first Referral record.
	"visibility": {"visible_to", false, false},

	// `sender_deadline` IS the engine's due_date for Incoming Correspondence.
	// The matrix says of it "every internal clock is derived from this date"
	// (eo-field-matrix.md:282) and of its Referral child "Deadline derived
	// from the parent, not typed" — and the only mechanism that derives a
	// child's deadline, services.DeriveDeadlineFromParent, reads
	// parent.DueDate, THE COLUMN, and nothing else. Seeded as a values-bag key
	// it would be a second date the clerk types beside due_date, with every
	// derived child deadline silently absent. See eoConditionalDecisions.
	"sender_deadline": {"due_date", true, false},
}

// eoMatrixKey translates a key as the client's field matrix spells it into the
// engine's. A pilot type's fields must be seeded under engineKey.
//
// WHAT GOES WRONG WITHOUT IT depends on which way the value travels, and the
// column story is only one of the two. A key the engine WRITES, seeded under
// the matrix spelling, lands in the values bag and shadows nothing: the clerk
// reads an empty field beside the column the engine already filled. A key the
// engine READS — Referral's `visibility`, which the repository knows as
// `visible_to` — is worse: the configuration looks enforced while the
// mechanism searches for a word that is not there. See eoEngineKeyDecisions.
//
//	isColumn        — the value lives in an eo_records column.
//	isEngineManaged — the engine owns the key (see eoMatrixKeyEntry).
//
// THE RULE: declare a field for a matrix key iff !isColumn && !isEngineManaged.
// Every column key is already declared by the base type (override it only to
// narrow it, under engineKey); an engine-managed key is never declared.
//
// A key the table does not know passes through unchanged as a values-bag key
// (isColumn false, isEngineManaged false): type-specific matrix keys are the
// engine's keys already.
func eoMatrixKey(matrixKey string) (engineKey string, isColumn bool, isEngineManaged bool) {
	if e, ok := eoMatrixKeys[matrixKey]; ok {
		return e.engineKey, e.isColumn, e.isEngineManaged
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
	// Nothing to build on until the platform seed has written the base type.
	var base int64
	if err := db.Model(&models.EORecordType{}).Where("key = ?", eoBaseTypeKey).Count(&base).Error; err != nil {
		log.Printf("EO pilot seed: could not check for record type %q: %v", eoBaseTypeKey, err)
		return
	}
	if base == 0 {
		return
	}
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

// eoPilotStep is one unit of the pilot seed.
//
// Whether a step has run is recorded in a DURABLE row (models.EOSeedStep, one
// per step key), written in the same transaction as the step's own work. It is
// never inferred from what the database currently contains: an inference such
// as "a smoke type exists" is true only until the step has run once, after
// which the fast path would shut and every step added later would never
// execute on a database that had already booted an earlier version.
//
// ADDING A STEP: append it to eoPilotSteps. That is the whole of it — the fast
// path compares applied rows with len(eoPilotSteps), so a new step reopens it
// on every existing database by construction, and there is no predicate to
// forget. (TestEOPilotRunsAStepAddedAfterTheOthersWereApplied pins this.)
//
// THE KEY IS A PERMANENT IDENTIFIER, NOT A DESCRIPTION, AND MUST NEVER BE
// RENAMED. It is the whole of what "this step has run" means: the applied row
// is keyed on it, nothing else is compared, and a renamed key is therefore a
// step that has never run. On every existing database the step would execute
// again — for "mark_base_labels_unsigned" that means re-marking labels the
// client has since approved, and for a type-seeding step it means a key
// collision that rolls the whole pilot seed back on every boot. Reword the
// comment, never the key. Two entries sharing a key are the same failure seen
// from the other side: len(eoPilotSteps) then exceeds the number of applied
// rows that can ever exist, so the fast path stays open for ever with no log
// and no error (TestEOPilotStepKeysAreUnique).
//
// run returns done=false when it deliberately did nothing and wants to be
// asked again next boot (smoke types kept because they hold records).
type eoPilotStep struct {
	key string
	run func(tx *gorm.DB) (done bool, err error)
}

var eoPilotSteps = []eoPilotStep{
	{"remove_smoke_configuration", eoRemoveSmokeConfiguration},
	{"mark_base_labels_unsigned", func(tx *gorm.DB) (bool, error) { return true, eoMarkBaseLabelsUnsigned(tx) }},
	{"seed_root_types", eoSeedRootTypes},
	{"seed_child_types", eoSeedChildTypes},
	{"seed_link_types", eoSeedLinkTypes},
}

// eoPilotSeedNeeded is the fast path: one count of applied-step rows.
func eoPilotSeedNeeded(db *gorm.DB) (bool, error) {
	keys := make([]string, 0, len(eoPilotSteps))
	for _, st := range eoPilotSteps {
		keys = append(keys, st.key)
	}
	var applied int64
	if err := db.Model(&models.EOSeedStep{}).Where("key IN ?", keys).Count(&applied).Error; err != nil {
		return false, err
	}
	return applied < int64(len(keys)), nil
}

func eoSeedPilotTx(tx *gorm.DB) error {
	for _, st := range eoPilotSteps {
		var n int64
		if err := tx.Model(&models.EOSeedStep{}).Where("key = ?", st.key).Count(&n).Error; err != nil {
			return fmt.Errorf("check step %q: %w", st.key, err)
		}
		if n > 0 {
			continue
		}
		done, err := st.run(tx)
		if err != nil {
			return fmt.Errorf("step %q: %w", st.key, err)
		}
		if !done {
			continue
		}
		if err := tx.Create(&models.EOSeedStep{Key: st.key}).Error; err != nil {
			return fmt.Errorf("record step %q: %w", st.key, err)
		}
	}
	return nil
}

// eoMarkBaseLabelsUnsigned marks the base type's inherited labels as awaiting
// sign-off: the Arabic on them was written by an implementer, not supplied by
// the client (docs/superpowers/eo-plan1-carry-forward.md). It is a one-shot
// step guarded ONLY by its applied-step row, so it fills in a database that
// predates the column and can never re-mark a label an administrator has since
// approved, however many labels remain marked. (A fresh database is marked at
// creation by eoBaseFieldDefs; this is then a harmless first run.)
func eoMarkBaseLabelsUnsigned(tx *gorm.DB) error {
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
func eoRemoveSmokeConfiguration(tx *gorm.DB) (bool, error) {
	var smoke []models.EORecordType
	if err := tx.Where("key IN ?", eoSmokeTypeKeys).Find(&smoke).Error; err != nil {
		return false, fmt.Errorf("find smoke record types: %w", err)
	}
	if len(smoke) == 0 {
		return true, nil
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
		return false, fmt.Errorf("walk the smoke types' subtree: %w", err)
	}

	var holders []struct {
		Key string
		N   int64
	}
	if err := tx.Raw(`
		SELECT t.key AS key, COUNT(*) AS n
		FROM eo_records r JOIN eo_record_types t ON t.id = r.record_type_id
		WHERE r.record_type_id IN ? GROUP BY t.key ORDER BY t.key`, subtree).Scan(&holders).Error; err != nil {
		return false, fmt.Errorf("count records under the smoke types: %w", err)
	}
	if len(holders) > 0 {
		desc := make([]string, 0, len(holders))
		for _, h := range holders {
			desc = append(desc, fmt.Sprintf("%s (%d)", h.Key, h.N))
		}
		sort.Strings(desc)
		log.Printf("EO pilot: the smoke record types were KEPT because records exist under them: %v. They cannot be removed while populated (spec §7.14); delete those records and restart to remove them.", desc)
		return false, nil
	}
	if len(subtree) != len(smokeIDs) {
		log.Printf("EO pilot: the smoke record types were KEPT because other record types are defined beneath them (%d in the subtree, %d smoke types).", len(subtree), len(smokeIDs))
		return false, nil
	}

	// Workflows bound to the smoke types.
	var workflowIDs []uuid.UUID
	if err := tx.Unscoped().Model(&models.Workflow{}).Where("record_type_id IN ?", smokeIDs).Pluck("id", &workflowIDs).Error; err != nil {
		return false, fmt.Errorf("find smoke workflows: %w", err)
	}
	// The type's own workflow_id points at the workflow; release it first so
	// neither delete order trips a foreign key.
	if err := tx.Model(&models.EORecordType{}).Where("id IN ?", smokeIDs).Update("workflow_id", nil).Error; err != nil {
		return false, fmt.Errorf("unbind smoke workflows: %w", err)
	}

	var policyIDs []uuid.UUID
	if len(workflowIDs) > 0 {
		if err := tx.Unscoped().Model(&models.WorkflowState{}).
			Where("workflow_id IN ? AND escalation_policy_id IS NOT NULL", workflowIDs).
			Distinct().Pluck("escalation_policy_id", &policyIDs).Error; err != nil {
			return false, fmt.Errorf("find smoke escalation policies: %w", err)
		}
		var transitions []models.WorkflowTransition
		if err := tx.Unscoped().Where("workflow_id IN ?", workflowIDs).Find(&transitions).Error; err != nil {
			return false, fmt.Errorf("find smoke transitions: %w", err)
		}
		for i := range transitions {
			tr := &transitions[i]
			if err := tx.Model(tr).Association("AllowedRoles").Clear(); err != nil {
				return false, fmt.Errorf("clear allowed roles of transition %q: %w", tr.Code, err)
			}
			if err := tx.Model(tr).Association("AssignmentRoles").Clear(); err != nil {
				return false, fmt.Errorf("clear assignment roles of transition %q: %w", tr.Code, err)
			}
			if err := tx.Unscoped().Where("transition_id = ?", tr.ID).Delete(&models.TransitionRequirement{}).Error; err != nil {
				return false, fmt.Errorf("delete requirements of transition %q: %w", tr.Code, err)
			}
			if err := tx.Unscoped().Where("transition_id = ?", tr.ID).Delete(&models.TransitionFieldChange{}).Error; err != nil {
				return false, fmt.Errorf("delete field changes of transition %q: %w", tr.Code, err)
			}
		}
		if err := tx.Unscoped().Where("workflow_id IN ?", workflowIDs).Delete(&models.WorkflowTransition{}).Error; err != nil {
			return false, fmt.Errorf("delete smoke transitions: %w", err)
		}
		if err := tx.Unscoped().Where("workflow_id IN ?", workflowIDs).Delete(&models.WorkflowState{}).Error; err != nil {
			return false, fmt.Errorf("delete smoke states: %w", err)
		}
		if err := tx.Unscoped().Where("id IN ?", workflowIDs).Delete(&models.Workflow{}).Error; err != nil {
			return false, fmt.Errorf("delete smoke workflows: %w", err)
		}
	}

	// The policy is found by the states that pointed at it AND by its name: a
	// half-removal that left the name behind is the collision this guards.
	var byName []uuid.UUID
	if err := tx.Unscoped().Model(&models.EscalationPolicy{}).Where("name = ?", eoSmokePolicyName).Pluck("id", &byName).Error; err != nil {
		return false, fmt.Errorf("find the smoke escalation policy by name: %w", err)
	}
	policyIDs = append(policyIDs, byName...)
	if len(policyIDs) > 0 {
		var stillUsed int64
		if err := tx.Model(&models.WorkflowState{}).Where("escalation_policy_id IN ?", policyIDs).Count(&stillUsed).Error; err != nil {
			return false, fmt.Errorf("check the smoke policy is unused: %w", err)
		}
		if stillUsed > 0 {
			return false, fmt.Errorf("escalation policy %q is still referenced by %d workflow states outside the smoke workflows; refusing to delete it", eoSmokePolicyName, stillUsed)
		}
		if err := tx.Unscoped().Where("policy_id IN ?", policyIDs).Delete(&models.EscalationPolicyStep{}).Error; err != nil {
			return false, fmt.Errorf("delete smoke escalation steps: %w", err)
		}
		if err := tx.Unscoped().Where("id IN ?", policyIDs).Delete(&models.EscalationPolicy{}).Error; err != nil {
			return false, fmt.Errorf("delete smoke escalation policy: %w", err)
		}
	}

	if err := tx.Unscoped().Where("key = ?", eoSmokeLinkTypeKey).Delete(&models.EOLinkType{}).Error; err != nil {
		return false, fmt.Errorf("delete smoke link type: %w", err)
	}
	if err := tx.Unscoped().Where("record_type_id IN ?", smokeIDs).Delete(&models.EOFieldDef{}).Error; err != nil {
		return false, fmt.Errorf("delete smoke field definitions: %w", err)
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
		return false, fmt.Errorf("find smoke permissions: %w", err)
	}
	if len(permIDs) > 0 {
		if err := tx.Exec(`DELETE FROM role_permissions WHERE permission_id IN ?`, permIDs).Error; err != nil {
			return false, fmt.Errorf("revoke smoke permissions: %w", err)
		}
		if err := tx.Unscoped().Where("id IN ?", permIDs).Delete(&models.Permission{}).Error; err != nil {
			return false, fmt.Errorf("delete smoke permissions: %w", err)
		}
	}

	if err := tx.Unscoped().Where("id IN ?", smokeIDs).Delete(&models.EORecordType{}).Error; err != nil {
		return false, fmt.Errorf("delete smoke record types: %w", err)
	}
	log.Println("EO pilot: removed Plan 1's smoke configuration (eo_smoke_directive, eo_smoke_task)")
	return true, nil
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

// SeedEOPilot applies the EO pilot configuration to db, on top of the platform
// seed. It is the exported entry point seedEOPilot backs, for the same reason
// SeedEOPlatform exists: package services cannot be imported from here (it
// imports this package), so the tests that put the pilot's LINK RULES in front
// of the real save and transition pipelines live over there and need the REAL
// seeded configuration rather than a hand-built copy that would drift from it.
func SeedEOPilot(db *gorm.DB) { seedEOPilot(db) }

// ── Task A2: the three root types of the pilot ──────────────────────────────

// The keys of the three root record types of the client's field matrix
// (docs/superpowers/specs/eo-field-matrix.md). A record type key is a
// PERMANENT identifier: models.ValidateTypeEdit refuses to change it once
// records exist, because the key is what the reference prefix, the seeded
// capability config and every formula that names a child type address.
const (
	eoDirectiveTypeKey  = "eo_directive"               // EO-DIR, matrix line 68
	eoDraftStudyTypeKey = "eo_draft_decision_study"    // EO-DRF, matrix line 124
	eoIncomingTypeKey   = "eo_incoming_correspondence" // EO-COM, matrix line 272

	// eoAssignmentTypeKey names the Directive's Assignment sub type, which
	// TASK A3 SEEDS, not this one. It is declared here because the Directive's
	// derived completion_rate rolls up over that type and names it in its
	// formula and its recompute_on: a child type key is part of a parent
	// type's configuration. A3 must seed its Assignment type under THIS
	// constant; under any other key the roll-up resolves to nothing for ever.
	eoAssignmentTypeKey = "eo_assignment"
)

// ── Task A3: the eight child types ──────────────────────────────────────────

// The keys of the eight sub types. Seven come from the client's field matrix;
// eoStageTypeKey does not, and the comment on eoPilotChildTypes says why.
// Like the root keys these are PERMANENT identifiers.
const (
	eoExecutionPlanTypeKey = "eo_execution_plan"       // matrix line 88
	eoObjectionTypeKey     = "eo_objection"            // matrix line 152
	eoDecisionTypeKey      = "eo_decision_record"      // matrix line 165
	eoReferralTypeKey      = "eo_referral"             // matrix line 295
	eoOutgoingTypeKey      = "eo_outgoing_reply"       // matrix line 311
	eoTaskTypeKey          = "eo_task"                 // matrix line 518
	eoStageTypeKey         = "eo_execution_plan_stage" // NO matrix section: see eoPilotChildTypes
)

// eoPilotTypeKeys is every concrete record type the pilot seeds, roots first.
// The tests that read the client's matrix iterate it, so a type added to the
// seed without being added here is checked against the matrix by nothing.
var eoPilotTypeKeys = []string{
	eoDirectiveTypeKey,
	eoDraftStudyTypeKey,
	eoIncomingTypeKey,
	eoExecutionPlanTypeKey,
	eoStageTypeKey,
	eoAssignmentTypeKey,
	eoObjectionTypeKey,
	eoDecisionTypeKey,
	eoReferralTypeKey,
	eoOutgoingTypeKey,
	eoTaskTypeKey,
}

// eoPilotFieldSpec is one row of a type's table in the client field matrix,
// written in the MATRIX's own spelling. eoSeedRootTypes runs matrixKey through
// eoMatrixKey, which decides both the engine key and whether a field may be
// declared at all, so a renamed or engine-managed key cannot be seeded by
// writing it out here.
type eoPilotFieldSpec struct {
	matrixKey  string
	label      string // English only; eoUntranslatedLabel puts it in both labels
	kind       string
	provenance string
	required   string
	// rule is the required_rule, and is set exactly when required is
	// models.EORequiredConditional: a conditional field without one makes
	// services.ValidateRecordValues return a HARD error, so every save of the
	// type would fail.
	rule        string
	config      string // JSON; empty means {}
	formula     string
	recomputeOn string // JSON; empty means []
}

// eoInheritedNarrowing redefines an inherited field on one type. A child
// redefining an ancestor's key replaces it AT THE ANCESTOR'S SLOT
// (services.ResolveEffectiveFields), so the field keeps its position on the
// form and the override is a narrowing, not a new field. The whole definition
// is copied from the base type's STORED row — read from the transaction, not
// from eoBaseFieldDefs in memory — so that a base field the designer has since
// edited is what gets narrowed, rather than what the Go source said on the day
// this was written.
type eoInheritedNarrowing struct {
	matrixKey string
	required  string
	rule      string
	// label, when set, is the client matrix's wording for this field ON THIS
	// TYPE — used where a type spells an inherited field its own way
	// (Incoming Correspondence's "Their deadline" IS the engine's due_date).
	// It goes through eoUntranslatedLabel like any other label the seed
	// writes, so it carries the Arabic sign-off marker. Empty keeps the base
	// type's label, Arabic and all.
	label string
	// provenance, when set, replaces the base type's. It is a LARGER claim
	// than a requiredness narrowing and is used in exactly one place: where
	// the client's matrix says a column-backed field is written by the engine
	// and "not typed" (Referral's deadline). services.ValidateRecordValues
	// refuses a submitted value for an `auto` field, which is what "not
	// typed" means in the engine's terms. Empty keeps the base's.
	provenance string
}

// eoPilotTypeSpec is one record type of the pilot.
type eoPilotTypeSpec struct {
	key, name, icon string
	// mode is models.EOReferenceModeOwn or ...Parent. It is not defaulted:
	// the zero value would silently make every type own-sequence, and a
	// parent-derived type seeded own-mode is rejected by
	// models.ValidateReferencePrefix for its blank prefix — or, worse,
	// accepted with an invented one that ValidateTypeEdit then makes
	// permanent.
	mode            string
	prefix, pattern string
	fields          []eoPilotFieldSpec
	narrowed        []eoInheritedNarrowing
}

// eoConditionalDecision records what the seed did with one field the client's
// matrix marks **Conditional**, and why.
//
// It exists because `required_mode: never` on a Conditional field is a
// WEAKENING of the client's authoritative document, and a silent weakening is
// indistinguishable from a deliberate one. Every Conditional cell of the three
// root types has an entry here naming the condition in the matrix's own terms,
// the mode actually seeded, and the reason. TestEveryConditionalMatrixFieldHas
// AStatedDecision checks this registry against both the matrix and the seeded
// rows, so a decision cannot drift from what was seeded and a field cannot be
// seeded `never` without one.
type eoConditionalDecision struct {
	TypeKey   string
	MatrixKey string
	// Condition is the matrix's own statement of when the field is required.
	Condition string
	// Mode is the required_mode seeded: conditional (the condition is
	// expressed as a rule), always (the condition holds unconditionally for
	// this type) or never (it is not expressible — see Why).
	Mode string
	Why  string
}

// eoConditionalDecisions is the registry, roots then children. Read it with
// the matrix open.
var eoConditionalDecisions = append(append([]eoConditionalDecision{}, eoRootConditionalDecisions...), eoChildConditionalDecisions()...)

// eoRootConditionalDecisions is Task A2's three root types.
var eoRootConditionalDecisions = []eoConditionalDecision{
	// ── the five inherited Conditional fields, per type ──────────────────
	// Plan 1 seeded all five `never` on the ABSTRACT base because none of the
	// conditions is expressible at base level (seed_eo_platform.go). These are
	// the per-type answers.
	{eoDirectiveTypeKey, "parent_ticket",
		"Mandatory on every sub and sub-sub type.",
		models.EORequiredNever,
		"Directive is a ROOT type: it has no parent, so the condition is false for it by construction, and the inherited `never` is already the right answer. The key is also provenance `auto` (the engine sets parent_record_id from the create request), and ValidateRecordValues exempts auto fields from requiredness entirely, so no override could change anything. Task A3's sub types are where this condition becomes true."},
	{eoDraftStudyTypeKey, "parent_ticket",
		"Mandatory on every sub and sub-sub type.",
		models.EORequiredNever,
		"Root type; see eo_directive.parent_ticket."},
	{eoIncomingTypeKey, "parent_ticket",
		"Mandatory on every sub and sub-sub type.",
		models.EORequiredNever,
		"Root type; see eo_directive.parent_ticket."},

	{eoDirectiveTypeKey, "assigned_user",
		"Set by the transition's assignment rules (auto_detect_department, auto_match_user).",
		models.EORequiredNever,
		"A workflow statement, not a form condition: the value is written by a transition's assignment rules, which belong to the workflow Task A5 binds. There is no expression over the record's own values that means \"a transition has run\", and making it required at creation would refuse the very first save, before any transition exists to assign anyone."},
	{eoDraftStudyTypeKey, "assigned_user",
		"Set by the transition's assignment rules.",
		models.EORequiredNever,
		"See eo_directive.assigned_user."},
	{eoIncomingTypeKey, "assigned_user",
		"Set by the transition's assignment rules.",
		models.EORequiredNever,
		"See eo_directive.assigned_user."},

	{eoDirectiveTypeKey, "due_date",
		"Required on any type the clock watches.",
		models.EORequiredAlways,
		"The clock DOES watch a Directive: the matrix's own escalation_level row escalates \"at +3, +7 and +14 days past the date\", and delay_reason is \"mandatory once the due date passes\". Neither can mean anything without a due date, so for this type the condition holds unconditionally and `always` is the engine's way of saying so. Seeded as a narrowing of the inherited definition, which replaces it at the base type's slot."},
	{eoDraftStudyTypeKey, "due_date",
		"Required on any type the clock watches.",
		models.EORequiredNever,
		"The clock that watches a Draft Decision Study is its own `study_deadline`, which the matrix marks Auto and derives from the claimed urgency — not due_date. Requiring due_date here would demand a second, unrelated date at creation that no CFF step asks for."},
	{eoIncomingTypeKey, "due_date",
		"Required on any type the clock watches.",
		models.EORequiredAlways,
		"The clock that watches an Incoming Correspondence is the matrix's `sender_deadline` — \"every internal clock is derived from this date\" — AND THAT IS THIS FIELD: eoMatrixKey maps sender_deadline onto the inherited due_date column, so the two names are one field with the matrix's label (\"Their deadline\"). Seeding them apart would have been the quiet failure: services.DeriveDeadlineFromParent is the only mechanism that gives a child the deadline the matrix calls \"derived from the parent, not typed\", it reads parent.DueDate (THE COLUMN) and nothing else, and an absent deadline is not an error — so Task A3's Referral would have derived no deadline at all, silently. The condition therefore holds unconditionally for this type, exactly as it does for the Directive."},

	{eoDirectiveTypeKey, "change_reason",
		"Material changes are logged — moving a date, changing an owner, altering scope.",
		models.EORequiredNever,
		"Not expressible: the engine cannot tell a material change from any other. The rule grammar evaluates the record's values, not the DIFFERENCE between a submission and what is stored, so there is no expression that means \"the due date moved\". Enforcing it needs either a diff-aware condition in the save pipeline or a per-transition requirement; neither exists today."},
	{eoDraftStudyTypeKey, "change_reason",
		"Material changes are logged.",
		models.EORequiredNever,
		"See eo_directive.change_reason."},
	{eoIncomingTypeKey, "change_reason",
		"Material changes are logged.",
		models.EORequiredNever,
		"See eo_directive.change_reason."},

	{eoDirectiveTypeKey, "delay_reason",
		"Mandatory once the due date passes.",
		models.EORequiredNever,
		"Needs a CLOCK, and eoformula deliberately has none: it is a pure evaluator with no now()/today() primitive, so \"the due date has passed\" cannot be written as a required_rule. A formula such as `due_date < due_date` would compile, never fire and look configured — this programme's signature defect — so it is not written. What it would take: either a time primitive in eoformula (a `now()` the evaluator injects into the env, which makes every evaluation non-deterministic and must be fed from one clock, not time.Now() at each call site), or enforcement off the field entirely — an overdue-record timer (spec §7.10, already durable) that requires delay_reason through the workflow rather than the form."},
	{eoDraftStudyTypeKey, "delay_reason",
		"Mandatory once the due date passes.",
		models.EORequiredNever,
		"See eo_directive.delay_reason: no clock in the rule grammar."},
	{eoIncomingTypeKey, "delay_reason",
		"Mandatory once the due date passes.",
		models.EORequiredNever,
		"See eo_directive.delay_reason: no clock in the rule grammar."},

	// ── the root types' own Conditional fields ───────────────────────────
	{eoDirectiveTypeKey, "origin_record",
		"Set when the directive came out of a visit, an appearance or a letter.",
		models.EORequiredConditional,
		"Expressed as a rule over directive_origin, which is the matrix's own field for \"where the directive came from\" and is required at creation on this type. \"A visit, an appearance or a letter\" are its official_visit, media_commitment and correspondence options; the other three (weekly_meeting, direct_instruction, committee) are not records of another type."},
	{eoDirectiveTypeKey, "decision_reference",
		"The matrix marks the field Conditional and names NO condition: the Options column is empty and the Why column gives only an example reference (EO-DEC-2026-311).",
		models.EORequiredNever,
		"Not expressible because the condition is UNSTATED, not because the engine cannot evaluate it. Any rule written here would be invented client policy — the plausible reading is \"when directive_origin is committee or weekly_meeting\", but the matrix does not say so and the cost of guessing is permanent: models.ValidateTypeEdit treats never -> required as destructive once records exist, so the designer could not correct it. THIS ONE NEEDS THE CLIENT'S ANSWER BEFORE THE FIRST DIRECTIVE RECORD; after that it takes a migration rather than an edit."},
	{eoDirectiveTypeKey, "corrective_action",
		"5.2 — recorded alongside the delay reason.",
		models.EORequiredConditional,
		"Expressed as a rule over delay_reason, which is an eo_records COLUMN and so is present in the rule's environment on every save of every record, whether or not the client submitted it — the one conditional rule here that can never meet an unresolved reference. \"Alongside the delay reason\" means: once a delay has been recorded, the corrective action is not optional. The rule tests for a non-space character rather than a non-empty string, to match services.eoIsEmpty, which trims."},
	{eoDraftStudyTypeKey, "redraft_text",
		"3.2 — the Office's rewrite, with the differences shown.",
		models.EORequiredConditional,
		"Expressed as a rule over recommendation, which is required on this type: the Office's rewrite is what \"Approve as redrafted\" recommends, so recommending it without supplying the redraft is the state the rule refuses. The other three recommendations (approve as proposed, reject, return for clarification) leave the original text standing and need no redraft."},
	{eoIncomingTypeKey, "coordinator",
		"4.2 — who pulls the answers together.",
		models.EORequiredConditional,
		"Expressed as a rule over direction_pattern, which is required on this type. \"Refer and consolidate\" is the one pattern of the five that produces several answers needing to be pulled together into one; \"refer out\" sends the letter on without consolidating it, and the other three keep it inside the Office."},
}

// eoDraftLockedFieldKeys are the two Draft Decision Study fields the matrix
// marks "locked after submit" / "File, locked" — the ONLY field-level
// immutability in the pilot, and §7.12's "the original is never edited".
var eoDraftLockedFieldKeys = []string{"draft_text", "draft_file"}

// eoLockDraftFieldsAfterState points both of eoDraftLockedFieldKeys at
// stateID, after which services.ValidateRecordValues refuses any submission
// that CHANGES their stored value (an unchanged echo is still accepted, so a
// client may resend a whole form).
//
// IT HAS NO PRODUCTION CALLER IN THIS TASK, and that is an ordering fact, not
// an oversight: IsLockedAfterStateID names a workflow state, the Draft
// Decision Study's workflow and its "submitted" state are TASK A5's, and no
// state exists to point at while this seed runs. The fields are therefore
// seeded here unlocked and A5 calls this inside its own step. Until it does,
// the two fields are editable — the lock is configured by nobody.
//
// It fails closed on both halves: the nil state, and a row count other than
// the number of keys (a renamed or missing field would otherwise lock nothing
// and report success).
func eoLockDraftFieldsAfterState(tx *gorm.DB, stateID uuid.UUID) error {
	if stateID == uuid.Nil {
		return fmt.Errorf("lock the draft fields: no state given; a nil state would lock nothing")
	}
	var ty models.EORecordType
	if err := tx.Where("key = ?", eoDraftStudyTypeKey).Limit(1).Find(&ty).Error; err != nil {
		return fmt.Errorf("read record type %q: %w", eoDraftStudyTypeKey, err)
	}
	if ty.ID == uuid.Nil {
		return fmt.Errorf("record type %q is not seeded, so its draft fields cannot be locked", eoDraftStudyTypeKey)
	}
	res := tx.Model(&models.EOFieldDef{}).
		Where("record_type_id = ? AND key IN ?", ty.ID, eoDraftLockedFieldKeys).
		Update("is_locked_after_state_id", stateID)
	if res.Error != nil {
		return fmt.Errorf("lock the draft fields of %q: %w", eoDraftStudyTypeKey, res.Error)
	}
	if res.RowsAffected != int64(len(eoDraftLockedFieldKeys)) {
		return fmt.Errorf("locked %d of %d fields on %q (%v); §7.12 wants the original locked, and a partial lock leaves it editable",
			res.RowsAffected, len(eoDraftLockedFieldKeys), eoDraftStudyTypeKey, eoDraftLockedFieldKeys)
	}
	return nil
}

// eoPilotRootTypes is the three root types, in the matrix's order. Every
// `label` is the matrix's English wording; no Arabic is invented here (the
// client has signed none off, and eoUntranslatedLabel is what makes the §9
// sign-off round a query rather than a reread of this file).
var eoPilotRootTypes = []eoPilotTypeSpec{
	{
		key: eoDirectiveTypeKey, name: "Directive", icon: "clipboard-list",
		mode: models.EOReferenceModeOwn, prefix: "EO-DIR", pattern: "EO-DIR-YYYY-NNN",
		fields: []eoPilotFieldSpec{
			{matrixKey: "directive_origin", label: "Directive origin",
				kind: models.EOKindDropdown, provenance: models.EOProvenanceUser, required: models.EORequiredAlways,
				config: `{"options":[` +
					`{"value":"weekly_meeting","label":"Weekly meeting","label_ar":"Weekly meeting"},` +
					`{"value":"direct_instruction","label":"Direct instruction","label_ar":"Direct instruction"},` +
					`{"value":"correspondence","label":"Correspondence","label_ar":"Correspondence"},` +
					`{"value":"official_visit","label":"Official visit","label_ar":"Official visit"},` +
					`{"value":"media_commitment","label":"Media commitment","label_ar":"Media commitment"},` +
					`{"value":"committee","label":"Committee","label_ar":"Committee"}]}`},
			{matrixKey: "origin_date", label: "Origin date",
				kind: models.EOKindDate, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "origin_record", label: "Origin record",
				kind: models.EOKindLink, provenance: models.EOProvenanceUser, required: models.EORequiredConditional,
				rule: `directive_origin in ["correspondence", "official_visit", "media_commitment"]`},
			{matrixKey: "decision_reference", label: "Decision reference",
				kind: models.EOKindText, provenance: models.EOProvenanceUser, required: models.EORequiredNever},
			{matrixKey: "directive_text", label: "Directive text",
				kind: models.EOKindLongText, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "required_outcome", label: "Required outcome",
				kind: models.EOKindLongText, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "responsible_officer", label: "Responsible officer",
				kind: models.EOKindUser, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "expected_deliverable", label: "Expected deliverable",
				kind: models.EOKindLongText, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "plan_required", label: "Plan required",
				kind: models.EOKindYesNo, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			// "Rolled up from its assignments." The aggregate key is published
			// by the child dependency below; it is OMITTED from the env while
			// no assignment carries a numeric completion rate, so the field
			// resolves to nothing on a directive whose assignments have no
			// figures yet and is reported as WAITING ON DATA.
			//
			// THAT IS TRUE ONLY ONCE TASK A3 HAS SEEDED eoAssignmentTypeKey.
			// Before it does, services.eoRecordDeps.Children fails outright
			// ("no record type with key %q"), and that error is not an
			// unresolved reference, so eoClassifyDeriveError files it as
			// BROKEN CONFIGURATION — the one signal that classifier exists to
			// keep clean. An A2-only deployment therefore logs a
			// broken-configuration error on every Directive save. It is
			// expected, it is not data loss (the save succeeds; the field is
			// left unset), and A3 ends it.
			{matrixKey: "completion_rate", label: "Completion rate",
				kind: models.EOKindPercent, provenance: models.EOProvenanceDerived, required: models.EORequiredNever,
				formula:     "children_" + eoAssignmentTypeKey + "_avg_completion_rate",
				recomputeOn: `[{"scope":"child","type":"` + eoAssignmentTypeKey + `","field":"completion_rate","aggregates":["avg"]}]`},
			// `matches "\S"` rather than `!= ""`: services.eoIsEmpty TRIMS
			// before deciding a value is empty, so a delay_reason of spaces
			// is no delay as far as the rest of the engine is concerned, and
			// a rule that read it as one would demand a corrective action for
			// a delay nothing else can see. eoformula has no trim().
			{matrixKey: "corrective_action", label: "Corrective action",
				kind: models.EOKindLongText, provenance: models.EOProvenanceUser, required: models.EORequiredConditional,
				rule: `delay_reason matches "\\S"`},
			{matrixKey: "escalation_level", label: "Escalation level",
				kind: models.EOKindDropdown, provenance: models.EOProvenanceAuto, required: models.EORequiredNever,
				config: `{"options":[` +
					`{"value":"none","label":"None","label_ar":"None"},` +
					`{"value":"direct_manager","label":"Direct manager","label_ar":"Direct manager"},` +
					`{"value":"grade_above","label":"Grade above","label_ar":"Grade above"},` +
					`{"value":"executive_office","label":"Executive Office","label_ar":"Executive Office"}]}`},
			{matrixKey: "verification_notes", label: "Verification notes",
				kind: models.EOKindLongText, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
		},
		narrowed: []eoInheritedNarrowing{
			{matrixKey: "due_date", required: models.EORequiredAlways},
		},
	},
	{
		key: eoDraftStudyTypeKey, name: "Draft Decision Study", icon: "file-search",
		mode: models.EOReferenceModeOwn, prefix: "DRF", pattern: "DRF-YYYY-NNN",
		fields: []eoPilotFieldSpec{
			{matrixKey: "proposer", label: "Proposer",
				kind: models.EOKindUser, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "proposing_body", label: "Proposing body",
				kind: models.EOKindOrgUnit, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "claimed_urgency", label: "Claimed urgency",
				kind: models.EOKindDropdown, provenance: models.EOProvenanceUser, required: models.EORequiredAlways,
				config: `{"options":[` +
					`{"value":"normal","label":"Normal","label_ar":"Normal"},` +
					`{"value":"urgent","label":"Urgent","label_ar":"Urgent"},` +
					`{"value":"immediate","label":"Immediate","label_ar":"Immediate"}]}`},
			{matrixKey: "grounds", label: "Grounds",
				kind: models.EOKindLongText, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			// draft_text and draft_file are the pilot's only locked fields;
			// eoLockDraftFieldsAfterState points them at A5's submitted state.
			{matrixKey: "draft_text", label: "Draft text",
				kind: models.EOKindLongText, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "draft_file", label: "Original attachment",
				kind: models.EOKindFile, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "received_at", label: "Received at",
				kind: models.EOKindDateTime, provenance: models.EOProvenanceAuto, required: models.EORequiredNever},
			{matrixKey: "study_deadline", label: "Study deadline",
				kind: models.EOKindDate, provenance: models.EOProvenanceAuto, required: models.EORequiredNever},
			{matrixKey: "related_refs", label: "Related references",
				kind: models.EOKindMultiLink, provenance: models.EOProvenanceAuto, required: models.EORequiredNever},
			{matrixKey: "conflict_verdict", label: "Conflict verdict",
				kind: models.EOKindDropdown, provenance: models.EOProvenanceUser, required: models.EORequiredAlways,
				config: `{"options":[` +
					`{"value":"no_conflict","label":"No conflict","label_ar":"No conflict"},` +
					`{"value":"conflicts","label":"Conflicts","label_ar":"Conflicts"},` +
					`{"value":"duplicates","label":"Duplicates","label_ar":"Duplicates"},` +
					`{"value":"consistent","label":"Consistent","label_ar":"Consistent"}]}`},
			{matrixKey: "feasibility", label: "Feasibility verdict",
				kind: models.EOKindDropdown, provenance: models.EOProvenanceUser, required: models.EORequiredAlways,
				config: `{"options":[` +
					`{"value":"feasible","label":"Feasible","label_ar":"Feasible"},` +
					`{"value":"feasible_with_conditions","label":"Feasible with conditions","label_ar":"Feasible with conditions"},` +
					`{"value":"not_feasible","label":"Not feasible","label_ar":"Not feasible"}]}`},
			{matrixKey: "feasibility_notes", label: "Feasibility notes",
				kind: models.EOKindLongText, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "issuing_authority", label: "Issuing authority",
				kind: models.EOKindText, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "affected_bodies", label: "Affected bodies",
				kind: models.EOKindMultiOrgUnit, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "redraft_text", label: "Proposed redraft",
				kind: models.EOKindLongText, provenance: models.EOProvenanceUser, required: models.EORequiredConditional,
				rule: `recommendation == "approve_as_redrafted"`},
			{matrixKey: "version", label: "Version",
				kind: models.EOKindNumber, provenance: models.EOProvenanceAuto, required: models.EORequiredNever},
			{matrixKey: "recommendation", label: "Office recommendation",
				kind: models.EOKindDropdown, provenance: models.EOProvenanceUser, required: models.EORequiredAlways,
				config: `{"options":[` +
					`{"value":"approve_as_proposed","label":"Approve as proposed","label_ar":"Approve as proposed"},` +
					`{"value":"approve_as_redrafted","label":"Approve as redrafted","label_ar":"Approve as redrafted"},` +
					`{"value":"reject","label":"Reject","label_ar":"Reject"},` +
					`{"value":"return_for_clarification","label":"Return for clarification","label_ar":"Return for clarification"}]}`},
			{matrixKey: "study_report", label: "Study report",
				kind: models.EOKindFile, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "he_decision", label: "H.E. decision",
				kind: models.EOKindDropdown, provenance: models.EOProvenanceUser, required: models.EORequiredAlways,
				config: `{"options":[` +
					`{"value":"approved_original","label":"Approved original","label_ar":"Approved original"},` +
					`{"value":"approved_redrafted","label":"Approved redrafted","label_ar":"Approved redrafted"},` +
					`{"value":"amended_by_he","label":"Amended by H.E.","label_ar":"Amended by H.E."},` +
					`{"value":"rejected","label":"Rejected","label_ar":"Rejected"},` +
					`{"value":"returned_for_further_study","label":"Returned for further study","label_ar":"Returned for further study"}]}`},
			{matrixKey: "he_reason", label: "H.E. reason",
				kind: models.EOKindLongText, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "decision_date", label: "Decision date",
				kind: models.EOKindDate, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
		},
	},
	{
		key: eoIncomingTypeKey, name: "Incoming Correspondence", icon: "mail",
		mode: models.EOReferenceModeOwn, prefix: "IN", pattern: "IN-YYYY-NNNN",
		fields: []eoPilotFieldSpec{
			{matrixKey: "sender", label: "Sending body",
				kind: models.EOKindExternalBody, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "sender_ref", label: "Their reference",
				kind: models.EOKindText, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "letter_date", label: "Letter date",
				kind: models.EOKindDate, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "received_date", label: "Date received",
				kind: models.EOKindDate, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			// `sender_deadline` is NOT here: eoMatrixKey maps it onto the
			// inherited due_date COLUMN, and it is seeded as the narrowing
			// below. See that entry and eoConditionalDecisions.
			//
			// The matrix marks `attachments` Required YES on this type.
			// It is an INHERITED key, so the override goes through
			// `narrowed:` below rather than being written out here: a
			// hand-written copy freezes the kind, the config and the
			// Arabic of the day it was typed, so a later designer edit to
			// the base row would never reach this type — the exact drift
			// the narrowing mechanism exists to prevent.
			{matrixKey: "real_ask", label: "What is actually wanted",
				kind: models.EOKindLongText, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "affected_bodies", label: "Affected bodies",
				kind: models.EOKindMultiOrgUnit, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "brief_view", label: "Brief — initial view",
				kind: models.EOKindLongText, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "brief_recommendation", label: "Brief — recommendation",
				kind: models.EOKindLongText, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "brief_file", label: "Brief file",
				kind: models.EOKindFile, provenance: models.EOProvenanceAuto, required: models.EORequiredNever},
			{matrixKey: "direction_pattern", label: "Direction pattern",
				kind: models.EOKindDropdown, provenance: models.EOProvenanceUser, required: models.EORequiredAlways,
				config: `{"options":[` +
					`{"value":"finish_in_office","label":"Finish in the Office","label_ar":"Finish in the Office"},` +
					`{"value":"refer_out","label":"Refer out","label_ar":"Refer out"},` +
					`{"value":"refer_and_consolidate","label":"Refer and consolidate","label_ar":"Refer and consolidate"},` +
					`{"value":"file_for_information","label":"File for information","label_ar":"File for information"},` +
					`{"value":"no_action","label":"No action","label_ar":"No action"}]}`},
			{matrixKey: "direction_text", label: "Direction wording",
				kind: models.EOKindLongText, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "coordinator", label: "Coordinating body",
				kind: models.EOKindOrgUnit, provenance: models.EOProvenanceUser, required: models.EORequiredConditional,
				rule: `direction_pattern == "refer_and_consolidate"`},
			{matrixKey: "direction_round", label: "Direction round",
				kind: models.EOKindNumber, provenance: models.EOProvenanceAuto, required: models.EORequiredNever},
			{matrixKey: "consolidated", label: "Consolidated output",
				kind: models.EOKindLongText, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
		},
		narrowed: []eoInheritedNarrowing{
			// A letter filed with no letter attached is what this
			// prevents: the base type seeds `attachments` never and the
			// matrix marks it Yes here. The narrowing keeps the base's
			// kind, label and Arabic and changes the requiredness alone.
			{matrixKey: "attachments", required: models.EORequiredAlways},
			// The sender's deadline IS this record's due date. Seeding it as
			// a field of its own would leave the due_date COLUMN empty, and
			// services.DeriveDeadlineFromParent — the only thing that gives a
			// Referral the deadline the matrix says is "derived from the
			// parent, not typed" — reads that column and nothing else.
			{matrixKey: "sender_deadline", required: models.EORequiredAlways, label: "Their deadline"},
		},
	},
}

// eoSeedRootTypes is the pilot seed's "seed_root_types" step: the three root
// record types of the client's field matrix, their fields, and a partition of
// eo_records for each.
//
// THE PARTITION IS CREATED IN THE SAME TRANSACTION AS THE TYPE, and that is
// the whole reason the DDL is here rather than in a later migration: Postgres
// validates the DEFAULT partition under ACCESS EXCLUSIVE when a new partition
// is created and errors if any row would belong to it, so the only moment a
// type can be given a partition for free is before its first record exists
// (spec §7.7, docs/superpowers/eo-plan1-carry-forward.md).
//
// models.ValidateReferencePrefix is called for each type BEFORE it is created,
// against everything already in the register, rather than trusting the data
// here: `reference` is unique per PARTITION only, so global uniqueness of
// references rests entirely on prefixes never overlapping. All three types are
// own-mode, which is the only kind the rule governs (eoSeedPilotTypes carries
// the call, and Task A3's parent-derived children are exempt from it).
//
// Every sub-step returns its error; nothing is logged and skipped.
func eoSeedRootTypes(tx *gorm.DB) (bool, error) {
	base, baseFields, err := eoPilotBase(tx)
	if err != nil {
		return false, err
	}
	if err := eoSeedPilotTypes(tx, eoPilotRootTypes, base, baseFields); err != nil {
		return false, err
	}
	return true, nil
}

// eoPilotBase reads the abstract base type and its STORED field definitions,
// which every pilot type inherits and a narrowing copies.
//
// The definitions come from the DATABASE, not from eoBaseFieldDefs in memory:
// the designer can edit a base field, and copying the Go source would make
// every override a snapshot of what the base type looked like on the day this
// file was written — the drift the copy exists to avoid.
func eoPilotBase(tx *gorm.DB) (models.EORecordType, []models.EOFieldDef, error) {
	var base models.EORecordType
	if err := tx.Where("key = ?", eoBaseTypeKey).Limit(1).Find(&base).Error; err != nil {
		return base, nil, fmt.Errorf("read record type %q: %w", eoBaseTypeKey, err)
	}
	if base.ID == uuid.Nil {
		return base, nil, fmt.Errorf("record type %q is missing; the platform seed must run before the pilot seed", eoBaseTypeKey)
	}
	var baseFields []models.EOFieldDef
	if err := tx.Where("record_type_id = ?", base.ID).Order("sort_order, key").Find(&baseFields).Error; err != nil {
		return base, nil, fmt.Errorf("read the inherited field definitions: %w", err)
	}
	if len(baseFields) == 0 {
		return base, nil, fmt.Errorf("record type %q has no field definitions; the platform seed must run before the pilot seed", eoBaseTypeKey)
	}
	return base, baseFields, nil
}

// eoSeedPilotTypes creates one group of pilot record types: the type row, its
// own partition of eo_records, and its field definitions. Both type-seeding
// steps go through it, so a rule added here reaches the roots and the children
// alike.
//
// ParentTypeID is the BASE type for every pilot type, including the sub types.
// The record-type parent is the FIELD-INHERITANCE parent
// (services.ResolveEffectiveFields walks it); the matrix's "Sub of Directive"
// is a hierarchy of RECORDS, carried by parent_record_id and the reference
// pattern. Pointing Execution Plan's type at the Directive type would give
// every plan the Directive's thirteen fields, which the matrix does not ask
// for.
func eoSeedPilotTypes(tx *gorm.DB, specs []eoPilotTypeSpec, base models.EORecordType, baseFields []models.EOFieldDef) error {
	for _, spec := range specs {
		switch spec.mode {
		case models.EOReferenceModeOwn, models.EOReferenceModeParent:
		default:
			return fmt.Errorf("record type %q has reference mode %q; it must be %q or %q, and defaulting it would silently number a child type as a root", spec.key, spec.mode, models.EOReferenceModeOwn, models.EOReferenceModeParent)
		}
		// A parent-derived type issues every reference inside its parent's, so
		// a prefix of its own would be a small lie that ValidateTypeEdit then
		// makes permanent (a prefix change is destructive once records exist).
		if spec.mode == models.EOReferenceModeParent && spec.prefix != "" {
			return fmt.Errorf("record type %q is parent-derived and carries the prefix %q; a parent-derived type owns no prefix", spec.key, spec.prefix)
		}
		t := models.EORecordType{
			ID:           uuid.New(),
			Key:          spec.key,
			Name:         spec.name,
			NameAr:       spec.name, // English stands in; no Arabic is invented
			Icon:         spec.icon,
			ParentTypeID: &base.ID,
			IsAbstract:   false,
			// ReferencePattern has no default and models.ValidateTypeEdit
			// treats a later change to it as destructive once records exist,
			// so it is set deliberately, from the matrix, for every type.
			ReferencePrefix:  spec.prefix,
			ReferenceMode:    spec.mode,
			ReferencePattern: spec.pattern,
			Capabilities:     datatypes.JSON(`[]`),
			IsActive:         true,
			// PartitionPending is cleared AFTER the partition exists, by the
			// explicit update below rather than here: the column carries
			// `gorm:"default:true"`, and GORM omits a zero-valued field with a
			// default tag from its INSERT, so `PartitionPending: false` here
			// would silently insert the DEFAULT and the label would claim the
			// opposite of the truth.
		}

		// Read the register back for every type, so each is validated against
		// the ones already written by this same step as well as against what
		// was there before. One query on the transaction's own handle.
		var existing []models.EORecordType
		if err := tx.Find(&existing).Error; err != nil {
			return fmt.Errorf("read the record type register: %w", err)
		}
		if err := models.ValidateReferencePrefix(t, existing); err != nil {
			return fmt.Errorf("record type %q: %w", spec.key, err)
		}
		if err := tx.Create(&t).Error; err != nil {
			return fmt.Errorf("create record type %q: %w", spec.key, err)
		}
		if err := migrations.CreateEORecordPartition(tx, t.Key, t.ID); err != nil {
			return err
		}
		// Only now: the label says "this type's records are in the default
		// partition", and that stopped being true one statement ago.
		if err := tx.Model(&models.EORecordType{}).Where("id = ?", t.ID).
			Update("partition_pending", false).Error; err != nil {
			return fmt.Errorf("clear partition_pending on %q: %w", spec.key, err)
		}

		defs, err := eoPilotFieldDefs(spec, t.ID, baseFields)
		if err != nil {
			return err
		}
		for _, d := range defs {
			if err := tx.Create(&d).Error; err != nil {
				return fmt.Errorf("create field %q on %q: %w", d.Key, spec.key, err)
			}
		}
		log.Printf("EO pilot: seeded record type %q (%s, %s) with %d fields and its own partition %s",
			spec.key, spec.pattern, spec.mode, len(defs), migrations.EORecordPartitionName(spec.key))
	}
	return nil
}

// eoSeedChildTypes is the pilot seed's "seed_child_types" step: the eight sub
// types of the pilot, their fields, and a partition of eo_records for each.
//
// It is a STEP OF ITS OWN, not an extension of seed_root_types: the applied
// row for that step already exists on every database that has booted Task A2,
// so work folded into it would never run there (see eoPilotStep).
func eoSeedChildTypes(tx *gorm.DB) (bool, error) {
	base, baseFields, err := eoPilotBase(tx)
	if err != nil {
		return false, err
	}
	// The roots must exist first: the children's reference patterns extend
	// them, their roll-ups name them, and a child seeded against a missing
	// root would be a type nothing can parent.
	for _, key := range []string{eoDirectiveTypeKey, eoDraftStudyTypeKey, eoIncomingTypeKey} {
		var n int64
		if err := tx.Model(&models.EORecordType{}).Where("key = ?", key).Count(&n).Error; err != nil {
			return false, fmt.Errorf("check root record type %q: %w", key, err)
		}
		if n == 0 {
			return false, fmt.Errorf("record type %q is missing; the pilot's root types must be seeded before its child types", key)
		}
	}
	if err := eoSeedPilotTypes(tx, eoPilotChildTypes, base, baseFields); err != nil {
		return false, err
	}
	return true, nil
}

// eoPilotFieldDefs turns one type spec into field definitions.
//
// Every matrix key goes through eoMatrixKey and A1's rule — declare a field
// iff !isColumn && !isEngineManaged — so a renamed key is declared under the
// ENGINE spelling (a field keyed with the matrix spelling would land in the
// values bag and shadow nothing) and an engine-managed key is dropped however
// the matrix marks it.
//
// Every label goes through eoUntranslatedLabel, which puts the English in both
// labels and marks the field as awaiting the client's Arabic sign-off. Writing
// the two assignments by hand would bypass the marker invisibly and nothing
// would fail, so TestRootTypeFieldsAwaitArabicSignOff asserts the result.
func eoPilotFieldDefs(spec eoPilotTypeSpec, typeID uuid.UUID, baseFields []models.EOFieldDef) ([]models.EOFieldDef, error) {
	out := make([]models.EOFieldDef, 0, len(spec.fields)+len(spec.narrowed))
	// Own fields sort after the seventeen inherited ones, which use 1..17.
	order := 100
	for _, f := range spec.fields {
		key, isColumn, isEngineManaged := eoMatrixKey(f.matrixKey)
		// REFUSED, not skipped, for ANY key the base type defines — not only
		// for a COLUMN key. The two failures differ and both are silent:
		//
		//   a COLUMN key written here used to be DROPPED outright (no error,
		//   no log), so the type lost the field entirely;
		//
		//   a VALUES-BAG key the base defines — `attachments` is the only one
		//   today — is not dropped, it is written as a second, hand-made
		//   definition that shadows the inherited row at the ancestor's slot
		//   while freezing the kind, config and Arabic of the day it was
		//   typed. That is the drift `narrowed:` exists to prevent, and
		//   Task A2 shipped exactly it before addendum 3.
		//
		// The base type's STORED definitions are the authority here, not
		// eoMatrixKey's isColumn flag, which only knows about columns.
		if _, inherited := eoFindFieldDef(baseFields, key); inherited {
			what := "an inherited field"
			if isColumn {
				what = "an eo_records COLUMN"
			}
			return nil, fmt.Errorf("record type %q declares %q (matrix %q) under `fields:`, but it is %s the base type already defines; narrow it through `narrowed:` instead, which copies the base's stored row", spec.key, key, f.matrixKey, what)
		}
		if isEngineManaged {
			// Never declarable by a pilot type: the engine owns the value
			// (record identity, workflow state), so no form may ask for it.
			continue
		}
		if f.required == models.EORequiredConditional && strings.TrimSpace(f.rule) == "" {
			return nil, fmt.Errorf("field %q on %q is conditional with no required_rule; services.ValidateRecordValues returns a hard error for that, so every save of the type would fail", key, spec.key)
		}
		if f.required != models.EORequiredConditional && strings.TrimSpace(f.rule) != "" {
			return nil, fmt.Errorf("field %q on %q carries a required_rule but is %q, so the rule is never evaluated", key, spec.key, f.required)
		}
		if (f.provenance == models.EOProvenanceDerived) != (strings.TrimSpace(f.formula) != "") {
			return nil, fmt.Errorf("field %q on %q: a derived field needs a formula and a formula needs a derived field", key, spec.key)
		}
		d := models.EOFieldDef{
			ID:           uuid.New(),
			RecordTypeID: typeID,
			Key:          key,
			Kind:         f.kind,
			Provenance:   f.provenance,
			RequiredMode: f.required,
			RequiredRule: f.rule,
			Config:       datatypes.JSON(eoJSONOr(f.config, `{}`)),
			Formula:      f.formula,
			RecomputeOn:  datatypes.JSON(eoJSONOr(f.recomputeOn, `[]`)),
			SortOrder:    order,
		}
		out = append(out, eoUntranslatedLabel(d, f.label))
		order++
	}

	for _, n := range spec.narrowed {
		key, _, isEngineManaged := eoMatrixKey(n.matrixKey)
		if isEngineManaged {
			return nil, fmt.Errorf("record type %q narrows %q, which the engine owns", spec.key, key)
		}
		src, ok := eoFindFieldDef(baseFields, key)
		if !ok {
			return nil, fmt.Errorf("record type %q narrows inherited field %q, which the base type does not define", spec.key, key)
		}
		// A copy of the base's definition, so the label, kind, options and
		// provenance cannot drift from the inherited field this replaces.
		d := src
		d.ID = uuid.New()
		d.RecordTypeID = typeID
		d.RequiredMode = n.required
		d.RequiredRule = n.rule
		if n.label != "" {
			d = eoUntranslatedLabel(d, n.label)
		}
		if n.provenance != "" {
			switch n.provenance {
			case models.EOProvenanceUser, models.EOProvenanceAuto, models.EOProvenanceSystem:
			default:
				// NOT `derived`: a derived field needs a formula (the guard on
				// the own-field path above says so, and RecomputeDerived skips
				// a derived field without one), and the mechanisms that write
				// these columns are link rules and the create request, not
				// §7.5 formulas. A narrowing to `derived` would therefore ask
				// for a formula that would compute a SECOND value for a column
				// something else already writes.
				return nil, fmt.Errorf("narrowing of %q on %q: provenance %q cannot be set here", key, spec.key, n.provenance)
			}
			d.Provenance = n.provenance
		}
		if (n.required == models.EORequiredConditional) != (strings.TrimSpace(n.rule) != "") {
			return nil, fmt.Errorf("narrowing of %q on %q: a conditional needs a rule and a rule needs a conditional", key, spec.key)
		}
		out = append(out, d)
	}
	return out, nil
}

// eoFindFieldDef returns the definition with key, if defs has one.
func eoFindFieldDef(defs []models.EOFieldDef, key string) (models.EOFieldDef, bool) {
	for _, d := range defs {
		if d.Key == key {
			return d, true
		}
	}
	return models.EOFieldDef{}, false
}

// eoJSONOr is s, or fallback when s is blank. The columns are jsonb and an
// empty []byte is not valid JSON.
func eoJSONOr(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// ── Task A3: the eight child types ──────────────────────────────────────────

// eoMatrixFieldNotSeeded records one row of the client's field matrix that the
// seed deliberately does NOT turn into a field definition.
//
// There is exactly one, and it is a modelling consequence rather than a
// refusal: spec §7.3 drops the `table` kind from the catalogue, so Execution
// Plan's `plan_stages` becomes a child record type. Without this registry the
// omission would be indistinguishable from a field somebody forgot — and the
// matrix-reading tests, which demand that every row of a type's table is
// seeded, would have to be weakened with an unexplained exception.
type eoMatrixFieldNotSeeded struct {
	TypeKey   string
	MatrixKey string
	// Instead names what carries the matrix's requirement instead.
	Instead string
	Why     string
}

// eoMatrixFieldsNotSeeded is the whole of it.
var eoMatrixFieldsNotSeeded = []eoMatrixFieldNotSeeded{
	{eoExecutionPlanTypeKey, "plan_stages",
		"the child record type " + eoStageTypeKey,
		"The matrix gives it the kind `Table (stage, weeks, body, output)` and spec §7.3 DROPS `table` from the kind catalogue: Assignment's `from_stage` is a `Link (plan stage)`, and a link needs a RECORD to point at — you cannot cleanly reference a row inside a table field. So the four columns become the four fields of a child type, the plan's stages are its child records, and `total_weeks` (`Sum of the stages`) is a real roll-up over them instead of arithmetic over a blob nothing can address.",
	},
}

// eoMatrixFieldIsNotSeeded reports whether (typeKey, matrixKey) is recorded as
// deliberately unseeded.
func eoMatrixFieldIsNotSeeded(typeKey, matrixKey string) bool {
	for _, e := range eoMatrixFieldsNotSeeded {
		if e.TypeKey == typeKey && e.MatrixKey == matrixKey {
			return true
		}
	}
	return false
}

// eoEngineKeyDecision records one matrix cell that AN ENGINE MECHANISM READS
// OR WRITES UNDER A SPELLING DIFFERENT FROM THE MATRIX'S.
//
// THE RULE WAS ORIGINALLY STATED TOO NARROWLY — "a field the matrix marks
// Derived or Auto whose value an engine mechanism writes to a COLUMN" — and a
// confidentiality control slipped through it in Task A3's first round.
// Referral's `visibility` is not a column and is not written by the engine at
// all: it is a VALUES-BAG key the repository READS, under the name
// `visible_to`, to decide who may see the record. A column-shaped rule is
// structurally blind to it. The rule is therefore: ANY KEY AN ENGINE
// MECHANISM READS OR WRITES UNDER A DIFFERENT SPELLING, whichever direction
// the value travels and whichever storage it uses.
//
// Why it matters in both directions. A key the engine WRITES, seeded under the
// matrix spelling, leaves the clerk reading an empty field beside a column the
// engine has already filled. A key the engine READS, seeded under the matrix
// spelling, is worse: the configuration looks enforced and the mechanism never
// fires, because it is looking for a word that is not there. Neither is
// reported by anything — validation sees a well-formed field in both cases.
//
// The registry is the statement of which cells those are and what reads or
// writes each. TestEoEngineKeyDecisionsAreDeclaredAsTheEngineSpellsThem checks
// every entry against the seeded rows, and the required-mode sweep consults it
// before applying its naive matrix-to-engine mapping, so an entry cannot drift
// from what was seeded and a cell cannot be excused from the sweep without one.
type eoEngineKeyDecision struct {
	TypeKey   string
	MatrixKey string
	// EngineKey is the key the engine knows the value by.
	EngineKey string
	// IsColumn is true when EngineKey is an eo_records column rather than a
	// values-bag key. It is checked against eoMatrixKey, so the registry and
	// the key map cannot disagree about where a value lives.
	IsColumn bool
	// MatrixRequired is the Required cell verbatim.
	MatrixRequired string
	// Mechanism names the code that reads or writes the key.
	Mechanism string
	// SeededMode and SeededProvenance are what the narrowing actually says.
	SeededMode       string
	SeededProvenance string
	Why              string
}

// eoEngineKeyDecisions is the registry, total over the pilot — Task A2's
// `sender_deadline` included, so that the inventory is in one place.
var eoEngineKeyDecisions = []eoEngineKeyDecision{
	{eoIncomingTypeKey, "sender_deadline", "due_date", true, "Yes",
		"the clerk types it; services.eoRecordColumnWriters writes rec.DueDate",
		models.EORequiredAlways, models.EOProvenanceUser,
		"Task A2's. \"Every internal clock is derived from this date\" (matrix:282), and the only mechanism that derives a child's deadline reads parent.DueDate, the column. Kept here so the inventory is complete."},

	{eoReferralTypeKey, "deadline", "due_date", true, "Derived",
		"services.DeriveDeadlineFromParent, applied by the save pipeline from the parent LINK TYPE's derive_deadline_from_parent and its offset; it writes rec.DueDate",
		models.EORequiredNever, models.EOProvenanceAuto,
		"\"Counted back from the sender's deadline\" (matrix:300) and \"Deadline derived from the parent, not typed\" (matrix:295). Provenance `auto`, not `derived`: services.ValidateRecordValues refuses a submitted value for an auto field, which is exactly what \"not typed\" means — while `derived` additionally demands a §7.5 FORMULA, and the mechanism here is a link rule. A formula would compute a second value for a column something else already writes. REQUIRES TASK A4: the mechanism is armed by the link type between Incoming Correspondence and Referral, and until that link type carries derive_deadline_from_parent no Referral gets a deadline at all and no clerk may type one. DeriveDeadlineFromParent returns nil silently when the parent has no due date, which is why Incoming's sender_deadline is `always`."},

	{eoDecisionTypeKey, "decision_number", "reference", true, "Auto",
		"services.RenderReference, through eoRecordService.allocateReference, which renders this type's ReferencePattern into rec.Reference",
		models.EORequiredNever, models.EOProvenanceAuto,
		"The matrix's Options cell IS the reference pattern (DEC-YYYY-NNN, matrix:171) and the type's own sequence renders it. A separate auto-number field would be a number nothing allocates, sitting blank beside the reference the engine had already issued. Seeded as a narrowing that carries the matrix's label, \"Decision number\", over the inherited `reference`."},

	{eoOutgoingTypeKey, "out_ref", "reference", true, "Auto",
		"services.RenderReference, through eoRecordService.allocateReference",
		models.EORequiredNever, models.EOProvenanceAuto,
		"OUT-YYYY-NNNN (matrix:316) is this type's own reference pattern; see eo_decision_record.decision_number."},

	{eoDecisionTypeKey, "source_study", "parent_record_id", true, "Auto",
		"the create request's parent_record_id, denormalised onto eo_records (spec §7.6)",
		models.EORequiredNever, models.EOProvenanceAuto,
		"\"Link (ticket) / Auto / The parent draft study\" (matrix:179) is the record's parent, and the parent is a column. A values-bag `source_study` would be a second, hand-typed pointer at the record the hierarchy already names, free to disagree with it."},

	{eoTaskTypeKey, "parent_ticket", "parent_record_id", true, "Yes",
		"the create request's parent_record_id",
		models.EORequiredNever, models.EOProvenanceAuto,
		"The matrix marks it Yes — \"Hangs off anything, including across subsystems\" — and the key is the parent_record_id COLUMN, whose base definition is provenance `auto` because the client chooses the parent on the create REQUEST, not as a value. services.ValidateRecordValues EXEMPTS auto fields from requiredness entirely (it rejects a submitted value and moves on before the required check), so `always` here would be configuration that looks like enforcement and can never refuse anything — this programme's signature defect. It is seeded `never` and the requirement is recorded here instead. WHAT DOES ENFORCE IT: the four parent-derived types and the stage type cannot be created without a parent at all, because services.RenderReference refuses a parent-derived pattern with no parent reference (TestEoPilotReferencePatternsRenderAndRefuse pins this). Task takes its OWN sequence, so that refusal does not apply to it: nothing today stops a parentless Task. CLOSING IT NEEDS A CREATE-PATH RULE, AND A LINK TYPE CANNOT DO IT: EOLinkType.Cardinality is an UPPER bound (services.ValidateLink reads it as \"at most one outgoing link of this type\"), and ValidateLink is only reached when a parent was supplied at all — eoRecordService.applyLinkRules returns early on a nil parent. So Task A4 should seed Task's polymorphic link type and leave this item OPEN; the work is a rule in the create path, and whoever writes it owns the Task question, whose parent may be of any type in any subsystem."},

	{eoTaskTypeKey, "due_date", "due_date", true, "Yes",
		"the clerk types it; services.eoRecordColumnWriters writes rec.DueDate",
		models.EORequiredAlways, models.EOProvenanceUser,
		"\"The clock that drives reminders and escalation\" (matrix:526). An ordinary narrowing of the inherited column to `always`: the clock really does watch a Task, so the inherited Conditional holds unconditionally here."},

	{eoTaskTypeKey, "owner_department", "owning_body_id", true, "Yes",
		"the clerk types it; services.eoRecordColumnWriters writes rec.OwningBodyID",
		models.EORequiredAlways, models.EOProvenanceUser,
		"One of Task A1's six renames, and the base type ALREADY seeds owning_body_id `always`+`user`. No narrowing is seeded, deliberately: an override that changes nothing is a second row to keep in step with the first for no gain. Listed here because it is a renamed matrix cell and the inventory is meant to be total."},

	// The entry that the column-only rule could not see.
	{eoReferralTypeKey, "visibility", "visible_to", false, "Auto",
		"repository.applyEORecordVisibility READS it (internal/repository/eo_record_repository.go:438-463) through repository.eoVisibleToKey, on every list, against EORecordFilter.VisibleToDepartmentIDs; the pilot seed writes it, as a §7.5 own-scope derivation from referred_body",
		models.EORequiredNever, models.EOProvenanceDerived,
		"Spec §7.11's row-level visibility, and the ONE registry entry whose engine key is a values-bag key rather than a column — which is why the rule is now about SPELLING rather than storage. The engine does not write this key; it READS it, so a field seeded under the matrix's `visibility` is not an empty field beside a full column but something worse: a confidentiality control that looks configured while the mechanism searches for a word that is not there, and every Referral in a government register is visible to everyone. Seeded `derived` with the formula `referred_body` so the matrix's \"the referred body only\" is computed on every save rather than waiting for a writer that does not exist, and so that ValidateRecordValues refuses a clerk's attempt to set their own record's visibility. The repository's own clause treats an ABSENT visible_to as unrestricted, so a derivation that failed would fail OPEN — which is why referred_body being `always` on this type is load-bearing, not incidental."},
}

// eoPilotChildTypes is the eight sub types, in the matrix's order within each
// CFF. Every `label` is the matrix's English wording; no Arabic is invented.
//
// THE ELEVENTH TYPE. eo_execution_plan_stage has no section in the client's
// matrix, and that is the point: it is a consequence of spec §7.3 dropping the
// `table` kind, not a client requirement. Its four fields are the four columns
// the matrix names inside Execution Plan's `plan_stages` cell —
// `Table (stage, weeks, body, output)` — and the row is marked Required Yes,
// so all four are `always`. See eoMatrixFieldsNotSeeded.
//
// REFERENCE MODES come from the matrix's own patterns and are not re-derived
// here (the brief's addendum 4):
//
//	parent-derived: Execution Plan, Execution Plan Stage, Assignment,
//	                Objection, Referral — exempt from ValidateReferencePrefix,
//	                because a parent-derived reference is its parent's plus a
//	                suffix the parent's sequence can never produce.
//	own sequence:   Decision Record (DEC), Outgoing Reply (OUT), Task (TSK) —
//	                governed by the rule, and none of the three overlaps the
//	                roots' EO-DIR, DRF or IN in either direction.
//
// Task is the §7.9 TWO-MODE CASE and must not be collapsed into parent-derived
// numbering: "Universal type. Hangs off anything, including across subsystems.
// Always blocks its parent from closing" (matrix:520) — its own sequence AND a
// parent.
var eoPilotChildTypes = []eoPilotTypeSpec{
	// ── EO-DIR ───────────────────────────────────────────────────────────
	{
		key: eoExecutionPlanTypeKey, name: "Execution Plan", icon: "map",
		mode: models.EOReferenceModeParent, prefix: "",
		// The matrix writes this pattern `EO-DIR-YYYY-NNN-P` (matrix:90),
		// WITHOUT the `#`. That text cannot be rendered: services'
		// parseEOPattern reads a parent-derived pattern as the parent's
		// segments followed by one segment of letters then `#`, so
		// `...-NNN-P` falls through to the own-sequence branch and is
		// rejected for not ending in YYYY then a run of N. Seeded verbatim it
		// would not misnumber a plan — it would make every Execution Plan
		// UNCREATABLE, at allocateReference, with no plan ever reaching the
		// database to show it.
		//
		// Adding the `#` is right rather than merely expedient, for two
		// reasons. EOSequenceScope partitions the counter PER PARENT
		// (pattern + "|" + parentRef), so -P1 is the first plan on THAT
		// directive and the client's "one per directive" is satisfied by the
		// number always reading 1; a replan is -P2, which is what
		// `supersedes` is for. And relaxing the grammar instead would be
		// worse: RenderReference would emit no sequence at all, so two plans
		// on one directive would collide on a `reference` that is unique per
		// PARTITION only.
		//
		// IT IS NOT A ONE-OFF. Four more matrix types use the same
		// unrenderable bare-suffix form, all of them "one per parent":
		// CMT-YYYY-NN-R (:256), MED-YYYY-NNN-BP (:357), REQ-YYYY-NNNN-APT
		// (:416) and REQ-YYYY-NNNN-DIR (:430). The designed answer is a
		// parent-derived SINGLETON pattern with a cardinality guarantee,
		// which is engine work; see the carry-forward doc, where it is framed
		// for the client as a choice rather than as a patch to one reference.
		pattern: "EO-DIR-YYYY-NNN-P#",
		fields: []eoPilotFieldSpec{
			{matrixKey: "plan_summary", label: "Plan summary",
				kind: models.EOKindLongText, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			// `plan_stages` is NOT here: it is the stage child type. See
			// eoMatrixFieldsNotSeeded.
			//
			// `total_weeks` is "Sum of the stages", and with the stages as
			// records that is a §7.5 child roll-up rather than arithmetic
			// over a blob. The env key services.eoAddChildAggregates
			// publishes is children_<type key>_<aggregate>_<field>, so the
			// formula names the stage type and its `weeks` field by key:
			// TestEoDerivedRollUpsNameASeededTypeAndField proves both exist,
			// because a formula over a type that is not seeded resolves to
			// nothing for ever and is logged as waiting on data.
			{matrixKey: "total_weeks", label: "Total duration (weeks)",
				kind: models.EOKindNumber, provenance: models.EOProvenanceDerived, required: models.EORequiredNever,
				formula:     "children_" + eoStageTypeKey + "_sum_weeks",
				recomputeOn: `[{"scope":"child","type":"` + eoStageTypeKey + `","field":"weeks","aggregates":["sum"]}]`},
			{matrixKey: "plan_start", label: "Plan start",
				kind: models.EOKindDate, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "plan_end", label: "Plan end",
				kind: models.EOKindDate, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "approved_by", label: "Approved by",
				kind: models.EOKindUser, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "approval_date", label: "Approval date",
				kind: models.EOKindDate, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "supersedes", label: "Supersedes",
				kind: models.EOKindLink, provenance: models.EOProvenanceUser, required: models.EORequiredNever,
				config: `{"target_type_key":"` + eoExecutionPlanTypeKey + `"}`},
		},
	},
	{
		key: eoStageTypeKey, name: "Execution Plan Stage", icon: "list-ordered",
		mode: models.EOReferenceModeParent, prefix: "",
		// Three levels, like the spec's own CMT-YYYY-NN-M#-I# example: the
		// parent is an Execution Plan, whose reference already carries the
		// directive's.
		pattern: "EO-DIR-YYYY-NNN-P#-S#",
		fields: []eoPilotFieldSpec{
			{matrixKey: "stage", label: "Stage",
				kind: models.EOKindText, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			// `weeks` is what Execution Plan's total_weeks sums. Renaming it
			// breaks that roll-up silently in production and loudly in
			// TestEoDerivedRollUpsNameASeededTypeAndField.
			{matrixKey: "weeks", label: "Duration (weeks)",
				kind: models.EOKindNumber, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "body", label: "Owning body for this stage",
				kind: models.EOKindOrgUnit, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "output", label: "Stage output",
				kind: models.EOKindLongText, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
		},
	},
	{
		key: eoAssignmentTypeKey, name: "Assignment", icon: "user-check",
		mode: models.EOReferenceModeParent, prefix: "",
		// The matrix says "Sub of Execution Plan, OR of Directive directly"
		// and gives one pattern, EO-DIR-YYYY-NNN-A#, whose parent segment
		// count is the Directive's. An assignment created under a PLAN
		// (EO-DIR-2026-001-P1, five segments) is refused by RenderReference,
		// which checks the parent reference's segment count against the
		// pattern. That is the matrix's own tension, not a seeding choice;
		// the report carries it, and the conservative reading — the pattern
		// the client wrote — is what is seeded, because ValidateTypeEdit
		// makes a pattern change destructive once records exist.
		pattern: "EO-DIR-YYYY-NNN-A#",
		fields: []eoPilotFieldSpec{
			{matrixKey: "assigned_body", label: "Assigned body",
				kind: models.EOKindOrgUnit, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "assignment_owner", label: "Assignment owner",
				kind: models.EOKindUser, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "scope", label: "Scope of work",
				kind: models.EOKindLongText, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			// The link the eleventh type exists for: `Link (plan stage)`.
			{matrixKey: "from_stage", label: "From stage",
				kind: models.EOKindLink, provenance: models.EOProvenanceUser, required: models.EORequiredNever,
				config: `{"target_type_key":"` + eoStageTypeKey + `"}`},
			{matrixKey: "start_date", label: "Start date",
				kind: models.EOKindDate, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			// THE DIRECTIVE'S ROLL-UP READS THIS FIELD, on this type key:
			// children_eo_assignment_avg_completion_rate. Task A2 seeded that
			// formula against a type that did not yet exist, so every
			// Directive save logged a broken-configuration error; seeding
			// this ends it.
			{matrixKey: "completion_rate", label: "Completion rate",
				kind: models.EOKindPercent, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			// "Yes at closure" — the matrix's own fifth value, and the one
			// the engine spells required_at_state. The STATE IDS ARE EMPTY
			// until Task A5 creates the workflow: see eoAtClosureFields and
			// eoRequireAtClosureState.
			{matrixKey: "evidence", label: "Evidence documents",
				kind: models.EOKindMultiFile, provenance: models.EOProvenanceUser, required: models.EORequiredAtState},
			{matrixKey: "evidence_note", label: "Evidence description",
				kind: models.EOKindLongText, provenance: models.EOProvenanceUser, required: models.EORequiredNever},
			// Two rows in the matrix, two fields here. models.EOKindUserDate
			// is the one-field primitive (§7.3) and is used where the matrix
			// gives ONE row for a user and a date together — Task's
			// "Verified by and on". Assignment's matrix gives two rows with
			// two kinds, User and Date, and the row-count guard reads the
			// document as written.
			{matrixKey: "verified_by", label: "Verified by",
				kind: models.EOKindUser, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "verified_on", label: "Verification date",
				kind: models.EOKindDate, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
		},
	},

	// ── EO-DRF ───────────────────────────────────────────────────────────
	{
		key: eoObjectionTypeKey, name: "Objection", icon: "alert-triangle",
		mode: models.EOReferenceModeParent, prefix: "",
		pattern: "DRF-YYYY-NNN-O#",
		fields: []eoPilotFieldSpec{
			{matrixKey: "clause_ref", label: "Point referenced",
				kind: models.EOKindText, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "objection_reason", label: "Objection reason",
				kind: models.EOKindLongText, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "proof", label: "Supporting document",
				kind: models.EOKindFile, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "raised_by", label: "Raised by",
				kind: models.EOKindUser, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "outcome", label: "Outcome",
				kind: models.EOKindDropdown, provenance: models.EOProvenanceUser, required: models.EORequiredAlways,
				config: `{"options":[` +
					`{"value":"accepted","label":"Accepted","label_ar":"Accepted"},` +
					`{"value":"dismissed","label":"Dismissed","label_ar":"Dismissed"}]}`},
			{matrixKey: "outcome_reason", label: "Outcome reason",
				kind: models.EOKindLongText, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
		},
	},
	{
		key: eoDecisionTypeKey, name: "Decision Record", icon: "gavel",
		mode: models.EOReferenceModeOwn, prefix: "DEC", pattern: "DEC-YYYY-NNN",
		fields: []eoPilotFieldSpec{
			// `decision_number` and `source_study` are NOT here: both are
			// eo_records columns, and eoPilotFieldDefs now REFUSES a column
			// key written under `fields:` rather than dropping it. They are
			// the narrowings below. See eoEngineKeyDecisions.
			{matrixKey: "decision_text", label: "Decision text",
				kind: models.EOKindLongText, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "issued_by", label: "Issued by",
				kind: models.EOKindUser, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "effective_date", label: "Effective date",
				kind: models.EOKindDate, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			// "Subject taxonomy" is a CLIENT VOCABULARY that no document in
			// this repository contains, and it is what the conflict check on
			// the next draft matches against (matrix:176). Inventing a list
			// of subjects would be inventing the client's taxonomy, and the
			// options of a seeded dropdown are what every future record is
			// filed under. The field therefore declares its option SOURCE —
			// one of the five spec §7.3 allows beside a static list — and the
			// category it names must be supplied before the first Decision
			// Record. Recorded in the report as a §9 sign-off obligation.
			{matrixKey: "keywords", label: "Subject keywords",
				kind: models.EOKindMultiSelect, provenance: models.EOProvenanceUser, required: models.EORequiredAlways,
				config: `{"option_source":"lookup_category","lookup_category_code":"eo_subject_taxonomy"}`},
			{matrixKey: "supersedes", label: "Supersedes",
				kind: models.EOKindLink, provenance: models.EOProvenanceUser, required: models.EORequiredNever,
				config: `{"target_type_key":"` + eoDecisionTypeKey + `"}`},
			{matrixKey: "standing", label: "Standing",
				kind: models.EOKindDropdown, provenance: models.EOProvenanceUser, required: models.EORequiredAlways,
				config: `{"options":[` +
					`{"value":"in_force","label":"In force","label_ar":"In force"},` +
					`{"value":"superseded","label":"Superseded","label_ar":"Superseded"},` +
					`{"value":"revoked","label":"Revoked","label_ar":"Revoked"}]}`},
		},
		narrowed: []eoInheritedNarrowing{
			{matrixKey: "decision_number", required: models.EORequiredNever, label: "Decision number"},
			{matrixKey: "source_study", required: models.EORequiredNever, label: "Source study"},
		},
	},

	// ── EO-COM ───────────────────────────────────────────────────────────
	{
		key: eoReferralTypeKey, name: "Referral", icon: "git-branch",
		mode: models.EOReferenceModeParent, prefix: "",
		pattern: "IN-YYYY-NNNN-R#",
		fields: []eoPilotFieldSpec{
			// `referred_body` is a field of its own and NOT the inherited
			// owning_body_id: the matrix gives Referral both — the inherited
			// "Owning body (the unit answerable for the record)" and its own
			// "Referred to (each referral carries a body)" — and the parsed
			// row counts would not balance if one were folded into the other.
			{matrixKey: "referred_body", label: "Referred to",
				kind: models.EOKindOrgUnit, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "referral_owner", label: "Named owner",
				kind: models.EOKindUser, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "ask_type", label: "What is wanted",
				kind: models.EOKindDropdown, provenance: models.EOProvenanceUser, required: models.EORequiredAlways,
				config: `{"options":[` +
					`{"value":"information","label":"Information","label_ar":"Information"},` +
					`{"value":"position","label":"Position","label_ar":"Position"},` +
					`{"value":"comment_on_study","label":"Comment on the study","label_ar":"Comment on the study"},` +
					`{"value":"deeper_study","label":"Deeper study","label_ar":"Deeper study"}]}`},
			{matrixKey: "instruction", label: "Instruction",
				kind: models.EOKindLongText, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			// `deadline` is NOT here: it is the inherited due_date COLUMN,
			// narrowed below. See eoEngineKeyDecisions.
			{matrixKey: "response", label: "View submitted",
				kind: models.EOKindLongText, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "response_files", label: "Response attachments",
				kind: models.EOKindMultiFile, provenance: models.EOProvenanceUser, required: models.EORequiredNever},
			{matrixKey: "parent_referral", label: "Parent referral",
				kind: models.EOKindLink, provenance: models.EOProvenanceUser, required: models.EORequiredNever,
				config: `{"target_type_key":"` + eoReferralTypeKey + `"}`},
			// THE §7.11 ROW-LEVEL VISIBILITY CONTROL, and it is live.
			// eoMatrixKey seeds it under the engine's spelling,
			// `visible_to`, which repository.applyEORecordVisibility reads
			// on every list: a record with a visible_to value is shown only
			// to an admin, to its creator (the Office must see its own
			// referrals to consolidate them) and to callers whose bodies
			// include that body. Under the matrix's own spelling the field
			// would be a key the engine has never heard of and every
			// Referral would be visible to everyone.
			//
			// DERIVED, not auto, and that is the difference between a
			// control and a label. "The referred body only" is literally
			// `visible_to = referred_body`, which is a §7.5 own-scope
			// dependency — the one derivation in the pilot that needs no
			// other record — so the value is computed on every save from the
			// body the clerk already named, rather than left for a mechanism
			// that does not exist to write one day. Provenance `derived` also
			// makes services.ValidateRecordValues REFUSE a submitted
			// visible_to, so no clerk can widen or narrow the visibility of
			// their own referral.
			//
			// IT FAILS OPEN, and `referred_body` being `always` is what
			// holds it shut. applyEORecordVisibility reads an ABSENT
			// visible_to as unrestricted, and eoApplyDerived never blocks a
			// save — a derived field that cannot resolve is logged and
			// dropped. services.eoEnvValue also DROPS a blank string from the
			// env, so a blank referred_body does not evaluate to "": it
			// raises ErrUnresolvedReference and the key goes absent. The
			// requiredness below is therefore load-bearing, not incidental,
			// and TestEoReferralVisibilityIsEnforcedByTheLiveClause asserts
			// it beside this field for exactly that reason.
			//
			// The exposure is FIRST SAVE ONLY: a non-empty but wrong value
			// fails closed (it matches no body, so only the creator and
			// admins see the record), and a later failed recompute cannot
			// re-open a restricted record, because eoApplyValues merges and
			// never deletes. It still cannot do worse than `auto`, which
			// leaves the key absent on every save rather than on one.
			// Whether the engine should REFUSE a save whose restriction key
			// did not derive is carried forward as an engine question.
			{matrixKey: "visibility", label: "Visible to",
				kind: models.EOKindOrgUnit, provenance: models.EOProvenanceDerived, required: models.EORequiredNever,
				formula:     "referred_body",
				recomputeOn: `[{"scope":"own","field":"referred_body"}]`},
		},
		narrowed: []eoInheritedNarrowing{
			{matrixKey: "deadline", required: models.EORequiredNever,
				provenance: models.EOProvenanceAuto, label: "Deadline"},
		},
	},
	{
		key: eoOutgoingTypeKey, name: "Outgoing Reply", icon: "send",
		mode: models.EOReferenceModeOwn, prefix: "OUT", pattern: "OUT-YYYY-NNNN",
		fields: []eoPilotFieldSpec{
			{matrixKey: "addressee", label: "Addressee",
				kind: models.EOKindExternalBody, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			// `out_ref` is NOT here: it is this type's own reference.
			{matrixKey: "reply_text", label: "Reply text",
				kind: models.EOKindLongText, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "reply_files", label: "Reply attachments",
				kind: models.EOKindMultiFile, provenance: models.EOProvenanceUser, required: models.EORequiredNever},
			{matrixKey: "approved_by", label: "Approved by",
				kind: models.EOKindUser, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "delegation_note", label: "Delegation note",
				kind: models.EOKindText, provenance: models.EOProvenanceUser, required: models.EORequiredNever},
			{matrixKey: "sent_on", label: "Sent on",
				kind: models.EOKindDate, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "channel", label: "Channel",
				kind: models.EOKindDropdown, provenance: models.EOProvenanceUser, required: models.EORequiredAlways,
				config: `{"options":[` +
					`{"value":"admin_communications_system","label":"Administrative communications system","label_ar":"Administrative communications system"},` +
					`{"value":"official_email","label":"Official email","label_ar":"Official email"},` +
					`{"value":"hand_delivery","label":"Hand delivery","label_ar":"Hand delivery"}]}`},
		},
		narrowed: []eoInheritedNarrowing{
			{matrixKey: "out_ref", required: models.EORequiredNever, label: "Outgoing reference"},
		},
	},

	// ── Universal ────────────────────────────────────────────────────────
	{
		key: eoTaskTypeKey, name: "Task", icon: "check-square",
		mode: models.EOReferenceModeOwn, prefix: "TSK", pattern: "TSK-YYYY-NNNNN",
		fields: []eoPilotFieldSpec{
			{matrixKey: "task_text", label: "Task description",
				kind: models.EOKindLongText, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			// `parent_ticket`, `owner_department` and `due_date` are NOT
			// here: all three are eo_records columns. See
			// eoEngineKeyDecisions, which also says why only two of them
			// are narrowed.
			//
			// `origin_cff` is Auto and "taken from the parent", and it is NOT
			// a §7.5 derived field: an ancestor dependency names ONE record
			// type, and a Task hangs off any type in any subsystem, so there
			// is no single ancestor type to name. Provenance `auto` with no
			// formula is the honest statement — the engine will set it, and
			// nothing sets it yet.
			{matrixKey: "origin_cff", label: "Originating subsystem",
				kind: models.EOKindText, provenance: models.EOProvenanceAuto, required: models.EORequiredNever},
			{matrixKey: "owner", label: "Named owner",
				kind: models.EOKindUser, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "start_date", label: "Start date",
				kind: models.EOKindDate, provenance: models.EOProvenanceUser, required: models.EORequiredNever},
			// "Rolls up to the parent's completion rate" — and NOTHING ROLLS
			// IT UP: the only roll-up in the pilot is the Directive's, over
			// eo_assignment. A Task's rate reaches no parent today. Reported.
			{matrixKey: "completion_rate", label: "Completion rate",
				kind: models.EOKindPercent, provenance: models.EOProvenanceUser, required: models.EORequiredNever},
			{matrixKey: "evidence", label: "Evidence",
				kind: models.EOKindMultiFile, provenance: models.EOProvenanceUser, required: models.EORequiredAtState},
			// ONE row in the matrix — "Verified by and on", kind "User +
			// Date" — so one field of models.EOKindUserDate, the §7.3
			// primitive, keyed on the first of the two keys the cell names,
			// exactly as the base type's created_by_id is.
			{matrixKey: "verified_by", label: "Verified by and on",
				kind: models.EOKindUserDate, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
		},
		narrowed: []eoInheritedNarrowing{
			{matrixKey: "due_date", required: models.EORequiredAlways},
		},
	},
}

// eoAtClosureField names one field the client's matrix marks "Yes at closure"
// — its own fifth Required value, "enforced as a workflow requirement on the
// terminal transition, not as form validation", which the engine spells
// required_mode `at_state`.
type eoAtClosureField struct {
	TypeKey  string
	FieldKey string
}

// eoAtClosureFields is the pilot's whole set: both are evidence, and both
// carry the matrix's own words, "the proof tied to each output" and "always
// blocks its parent from closing until evidence is attached".
var eoAtClosureFields = []eoAtClosureField{
	{eoAssignmentTypeKey, "evidence"},
	{eoTaskTypeKey, "evidence"},
}

// eoRequireAtClosureState points the eoAtClosureFields of one type at the
// workflow state that closes it, after which services.ValidateRecordValues
// requires them of any transition INTO that state.
//
// IT HAS NO PRODUCTION CALLER IN THIS TASK, and that is an ordering fact
// rather than an oversight — the same one eoLockDraftFieldsAfterState records.
// required_at_state_ids names workflow STATES; the workflows are Task A5's and
// no state exists to point at while this seed runs. UNTIL A5 CALLS IT THE TWO
// FIELDS ARE REQUIRED BY NOBODY: ValidateRecordValues reads an empty
// required_at_state_ids as "required at no state", so `at_state` with no ids
// is a rule that can never fire. That is the weaker of the two available
// wrongs — the other is `always`, which would demand the closing evidence at
// CREATION and, per models.ValidateTypeEdit, could then never be relaxed once
// records existed.
//
// It fails closed on both halves: a nil state, and a row count other than the
// number of fields named for that type (a renamed or missing field would
// otherwise require nothing and report success).
func eoRequireAtClosureState(tx *gorm.DB, typeKey string, stateID uuid.UUID) error {
	if stateID == uuid.Nil {
		return fmt.Errorf("require the at-closure fields of %q: no state given; a nil state would require nothing", typeKey)
	}
	var keys []string
	for _, f := range eoAtClosureFields {
		if f.TypeKey == typeKey {
			keys = append(keys, f.FieldKey)
		}
	}
	if len(keys) == 0 {
		return fmt.Errorf("record type %q has no fields marked \"Yes at closure\" in the client's matrix", typeKey)
	}
	var ty models.EORecordType
	if err := tx.Where("key = ?", typeKey).Limit(1).Find(&ty).Error; err != nil {
		return fmt.Errorf("read record type %q: %w", typeKey, err)
	}
	if ty.ID == uuid.Nil {
		return fmt.Errorf("record type %q is not seeded, so its at-closure fields cannot be required", typeKey)
	}
	ids, err := json.Marshal([]uuid.UUID{stateID})
	if err != nil {
		return fmt.Errorf("encode the closing state of %q: %w", typeKey, err)
	}
	res := tx.Model(&models.EOFieldDef{}).
		Where("record_type_id = ? AND key IN ?", ty.ID, keys).
		Update("required_at_state_ids", datatypes.JSON(ids))
	if res.Error != nil {
		return fmt.Errorf("require the at-closure fields of %q: %w", typeKey, res.Error)
	}
	if res.RowsAffected != int64(len(keys)) {
		return fmt.Errorf("required %d of %d at-closure fields on %q (%v); a partial result leaves the rest enforced by nobody",
			res.RowsAffected, len(keys), typeKey, keys)
	}
	return nil
}

// eoChildConditionalDecisions is Task A3's: the five inherited Conditional
// cells on each of the eight sub types, and the five Conditional cells of
// their own tables.
//
// READ THE OWN-TABLE FIVE FIRST, because all five are `never` and that is a
// claim worth testing. Each names a condition the rule grammar cannot reach,
// and the reason is the same shape every time: the condition is a fact about
// the record's NEIGHBOURS (is there an earlier plan? is my parent a plan or a
// directive? is this referral a sub-referral?) and services.ValidateRecordValues
// evaluates a required_rule against the record's OWN values alone. The
// alternative — a plausible rule over an unrelated field — would compile,
// never fire, and look configured, which is the defect this registry exists to
// make impossible to ship silently.
func eoChildConditionalDecisions() []eoConditionalDecision {
	// ── the inherited five, per type ─────────────────────────────────────
	out := []eoConditionalDecision{
		// `parent_ticket`. The condition is TRUE for all eight — they are all
		// sub types — and that is exactly why the mode is not `always`: the
		// key is the parent_record_id COLUMN, whose base definition is
		// provenance `auto`, and ValidateRecordValues exempts auto fields
		// from requiredness entirely (it rejects a submitted value and
		// `continue`s before the required check). `always` here would be
		// configuration that reads as enforcement and can refuse nothing.
		// What enforces it instead differs by numbering mode, so the two
		// groups carry different reasons.
		{eoExecutionPlanTypeKey, "parent_ticket",
			"Mandatory on every sub and sub-sub type.",
			models.EORequiredNever,
			"True for this type, and not enforceable through requiredness: parent_record_id is provenance `auto` and ValidateRecordValues exempts auto fields. IT IS ENFORCED ANYWAY, and loudly: this type is parent-derived, so services.RenderReference refuses to number a record with no parent reference and the create fails outright. A parentless Execution Plan cannot exist."},
		{eoStageTypeKey, "parent_ticket",
			"Mandatory on every sub and sub-sub type.",
			models.EORequiredNever,
			"Parent-derived; see eo_execution_plan.parent_ticket. Its parent is an Execution Plan, three levels below the Directive."},
		{eoAssignmentTypeKey, "parent_ticket",
			"Mandatory on every sub and sub-sub type.",
			models.EORequiredNever,
			"Parent-derived; see eo_execution_plan.parent_ticket."},
		{eoObjectionTypeKey, "parent_ticket",
			"Mandatory on every sub and sub-sub type.",
			models.EORequiredNever,
			"Parent-derived; see eo_execution_plan.parent_ticket."},
		{eoReferralTypeKey, "parent_ticket",
			"Mandatory on every sub and sub-sub type.",
			models.EORequiredNever,
			"Parent-derived; see eo_execution_plan.parent_ticket."},
		{eoDecisionTypeKey, "parent_ticket",
			"Mandatory on every sub and sub-sub type.",
			models.EORequiredNever,
			"True for this type and ENFORCED BY NOTHING, unlike the parent-derived sub types: a Decision Record takes its own DEC sequence, so RenderReference never asks for a parent reference and a parentless record numbers cleanly. The matrix's own `source_study` row (Auto, \"The parent draft study\") is the same statement from the other side, and it is the narrowing of parent_record_id. Closing this needs a CREATE-PATH RULE — a type that declares a parent link must be created with one. Requiredness on an auto field cannot do it, and NEITHER CAN A LINK TYPE: Cardinality is an upper bound (\"at most one\"), and services.ValidateLink is only reached when a parent was supplied, because applyLinkRules returns early on a nil parent. See eo_task.parent_ticket in eoEngineKeyDecisions, which says the same."},
		{eoOutgoingTypeKey, "parent_ticket",
			"Mandatory on every sub and sub-sub type.",
			models.EORequiredNever,
			"Own sequence (OUT); see eo_decision_record.parent_ticket — nothing refuses a parentless record of this type today."},
		{eoTaskTypeKey, "parent_ticket",
			"Mandatory on every sub and sub-sub type.",
			models.EORequiredNever,
			"Own sequence (TSK) and the matrix marks the type's OWN `parent_ticket` row Required Yes, not Conditional — the universal type \"hangs off anything\". Both statements reach the same column and the same answer; see eoEngineKeyDecisions, which carries the full reasoning and the enforcement gap."},

		// `due_date`: "required on any type the clock watches". Per type,
		// which clock — if any — watches it.
		{eoExecutionPlanTypeKey, "due_date",
			"Required on any type the clock watches.",
			models.EORequiredNever,
			"A plan's dates are `plan_start` and `plan_end`, both Required Yes on this type, and the matrix adds that the end \"must not run past the directive's due date\" — the directive's, not its own. No escalation, delay or reminder in the matrix watches a plan. Requiring due_date would demand a third date at creation that no CFF step asks for."},
		{eoStageTypeKey, "due_date",
			"Required on any type the clock watches.",
			models.EORequiredNever,
			"The stage type has no matrix section at all and no clock: a stage carries a DURATION (`weeks`), which the plan sums, not a date of its own."},
		{eoAssignmentTypeKey, "due_date",
			"Required on any type the clock watches.",
			models.EORequiredNever,
			"The matrix gives Assignment a `start_date` and no end date; the clock that escalates at +3, +7 and +14 days is the DIRECTIVE's, whose due_date Task A2 seeded `always` for that reason. Requiring one here would invent a per-assignment deadline the client's document does not describe, and ValidateTypeEdit would then refuse to remove it once records existed."},
		{eoObjectionTypeKey, "due_date",
			"Required on any type the clock watches.",
			models.EORequiredNever,
			"An objection blocks the raising step rather than a date: it \"must be resolved or dismissed before the parent can go to H.E.\". The matrix gives it no date field of any kind."},
		{eoDecisionTypeKey, "due_date",
			"Required on any type the clock watches.",
			models.EORequiredNever,
			"A decision is the OUTCOME of the study's clock, not subject to one; its dates are `effective_date` and the parent study's."},
		{eoReferralTypeKey, "due_date",
			"Required on any type the clock watches.",
			models.EORequiredNever,
			"The clock DOES watch a referral — and this field IS that clock, under the matrix's own name for it, `deadline`. It is `never` rather than `always` because the matrix marks it Derived and \"not typed\": requiring it of the clerk is precisely what the client's document refuses. The narrowing sets provenance `auto` so a clerk cannot supply one, and services.DeriveDeadlineFromParent writes the column. See eoEngineKeyDecisions, including what Task A4 must seed for that to happen at all."},
		{eoOutgoingTypeKey, "due_date",
			"Required on any type the clock watches.",
			models.EORequiredNever,
			"The reply's date is `sent_on`, Required Yes, and it is measured against the PARENT letter's deadline (\"two days inside the Ministry's deadline\"). A due date of its own would be a second deadline beside the one the letter already carries."},
		{eoTaskTypeKey, "due_date",
			"Required on any type the clock watches.",
			models.EORequiredAlways,
			"The clock watches a Task explicitly: the matrix's own table marks `due_date` Required Yes and calls it \"the clock that drives reminders and escalation\". The inherited condition therefore holds unconditionally for this type, and the narrowing says so."},

		// ── the sub types' own Conditional cells, all five `never` ───────
		{eoExecutionPlanTypeKey, "supersedes",
			"A replanned directive keeps the earlier version. Options: an earlier plan on the same directive.",
			models.EORequiredNever,
			"Not expressible: the condition is \"this plan replaces an earlier one on the same directive\", which is a fact about the record's SIBLINGS. A required_rule is evaluated against this record's own values (services.ValidateRecordValues passes eoFlattenStored(rec) plus the submission), and there is no sibling scope in the grammar — recompute_on's `child` and `ancestor` scopes belong to derived fields and run at save time, not inside a rule. Nothing on the plan itself says \"this is a replan\". What it would take: a Yes/No `is_replan` the client's matrix does not have, or enforcement on the transition that opens a second plan."},
		{eoAssignmentTypeKey, "from_stage",
			"3.3 — tasks are drawn from the approved plan. Options: stages on the approved plan.",
			models.EORequiredNever,
			"Not expressible: the matrix says an Assignment is \"sub of Execution Plan, OR of Directive directly\", so the condition is \"my parent is a plan\" — a fact about the PARENT'S TYPE. The rule environment carries parent_record_id (an id, from the column) and nothing else about the parent; eoformula is a pure evaluator with no lookup, so there is no expression that means \"my parent is of type eo_execution_plan\". The Directive's `plan_required` would answer it, but it is a field of the grandparent, not of this record. What it would take: the parent's type key in the rule environment, or a per-transition requirement on the step that creates an assignment under a plan."},
		{eoDecisionTypeKey, "supersedes",
			"6.3 — keeps the base consistent.",
			models.EORequiredNever,
			"Not expressible, and NOT for want of a candidate field: `standing` (In force | Superseded | Revoked) is the tempting one and it is the wrong way round — it records that THIS decision has been superseded by a later one, not that it supersedes an earlier one. A rule over it would fire on exactly the records that should not carry a link and stay silent on the ones that should. The real condition is a fact about another record in the decisions base."},
		{eoReferralTypeKey, "parent_referral",
			"5.3 — a sub-referral stays visible in the parent letter's tree.",
			models.EORequiredNever,
			"Not expressible: the condition is \"this referral is a SUB-referral\", and both kinds have a parent — a top-level referral's parent is the letter, a sub-referral's is another referral. The rule environment has parent_record_id for both and no way to tell their types apart, which is the same limit as eo_assignment.from_stage. The referral tree itself is a §7.9 capability (`referral_tree`) and the structure lives in eo_record_links, not in a value on the record."},
		{eoOutgoingTypeKey, "delegation_note",
			"6.2 — recorded when H.E. delegates the signature.",
			models.EORequiredNever,
			"Not expressible: the condition is \"the approver was a delegate rather than H.E. himself\", and the matrix gives this type no field that says which. `approved_by` is a User whose Options cell reads \"H.E. or his delegate\"; a rule comparing it to H.E.'s identity would have to hard-code a person's user id into seeded configuration, which is both invented client policy and a value that changes with the post-holder. What it would take: a Yes/No `signed_by_delegate`, or the delegation recorded by the transition that approves the reply."},
	}

	// `assigned_user`, `change_reason` and `delay_reason` carry the same
	// answer on every pilot type, for reasons that are properties of the
	// ENGINE rather than of any type. They are generated so that adding a
	// type cannot leave one of them behind; the text is Task A2's, which the
	// three root types already carry.
	for _, key := range eoPilotChildTypeKeys() {
		out = append(out,
			eoConditionalDecision{key, "assigned_user",
				"Set by the transition's assignment rules (auto_detect_department, auto_match_user).",
				models.EORequiredNever,
				"A workflow statement, not a form condition: the value is written by a transition's assignment rules, which belong to the workflow Task A5 binds. There is no expression over the record's own values that means \"a transition has run\", and making it required at creation would refuse the very first save, before any transition exists to assign anyone."},
			eoConditionalDecision{key, "change_reason",
				"Material changes are logged — moving a date, changing an owner, altering scope.",
				models.EORequiredNever,
				"Not expressible: the engine cannot tell a material change from any other. The rule grammar evaluates the record's values, not the DIFFERENCE between a submission and what is stored, so there is no expression that means \"the due date moved\". Enforcing it needs either a diff-aware condition in the save pipeline or a per-transition requirement; neither exists today."},
			eoConditionalDecision{key, "delay_reason",
				"Mandatory once the due date passes.",
				models.EORequiredNever,
				"Needs a CLOCK, and eoformula deliberately has none: it is a pure evaluator with no now()/today() primitive, so \"the due date has passed\" cannot be written as a required_rule. A formula that compiled, never fired and looked configured is this programme's signature defect, so it is not written. See eo_directive.delay_reason for what it would take."},
		)
	}
	return out
}

// eoPilotChildTypeKeys is the eight sub types' keys, read from the specs so
// that a type added to eoPilotChildTypes cannot be missed by the registry
// above.
func eoPilotChildTypeKeys() []string {
	out := make([]string, 0, len(eoPilotChildTypes))
	for _, spec := range eoPilotChildTypes {
		out = append(out, spec.key)
	}
	return out
}

// ── Task A4: the link types and the rules the matrix states in prose ────────

// The keys of the pilot's link types: ONE PER SUB TYPE, because a link type
// carries the rules that apply to a child OF THAT TYPE and the client's matrix
// states a different rule for each — an Execution Plan blocks its directive
// from closing, an Objection blocks one named step, a Referral's deadline is
// counted back from its letter's, a Decision Record closes its parent outright.
//
// LIKE A RECORD TYPE KEY THESE ARE PERMANENT IDENTIFIERS, and for a sharper
// reason: models.EORecordLink stores the link type KEY itself rather than an
// id, and models.EORecordCreateRequest.ParentLinkTypeKey names it. A renamed
// key orphans every link already written (services.BlockingChildren looks the
// type up by key and silently ignores a link whose type it cannot find, so a
// rename turns every existing blocking child into a non-blocking one) and
// breaks every client that creates a record under a parent.
const (
	eoExecutionPlanLinkKey = "eo_execution_plan_parent"
	eoStageLinkKey         = "eo_execution_plan_stage_parent"
	eoAssignmentLinkKey    = "eo_assignment_parent"
	eoObjectionLinkKey     = "eo_objection_parent"
	eoDecisionLinkKey      = "eo_decision_record_parent"
	eoReferralLinkKey      = "eo_referral_parent"
	eoOutgoingLinkKey      = "eo_outgoing_reply_parent"
	eoTaskLinkKey          = "eo_task_parent"
)

// eoAnyTargetType is the sentinel a link type's endpoint list carries to mean
// "a record of any type" — the matrix's "Hangs off anything, including across
// subsystems" for the universal Task (matrix:520).
//
// IT IS NOT AN EMPTY LIST, and the difference is the whole of this task's
// signature risk. services.eoTypeAllowed returns FALSE for an empty list and
// the column's DB default is '[]'::jsonb, so a Task link type seeded without
// targets would permit NOTHING while reading as the polymorphic rule the
// client asked for: every attempt to hang a Task off anything refused at
// validation, in configuration that looks right.
//
// THE CONSTANT IT MIRRORS IS UNEXPORTED (services.eoAnyType,
// internal/services/eo_links.go) and package services imports package
// database, so this package cannot import it back and the literal has to be
// repeated. TestEoAnyTargetSentinelMatchesTheEngineConstant reads the other
// side's source and compares, because nothing else ties the two together.
//
// services.eoTypeAllowed accepts it as a bare JSON string or inside the array;
// the seed uses the ARRAY spelling for every link type, so one decoder reads
// them all.
const eoAnyTargetType = "any"

// eoDraftRaiseToHETransitionCode is the workflow transition an open Objection
// blocks: EO-DRF's raise-to-H.E. step. The matrix says an Objection "must be
// resolved or dismissed before the parent can go to H.E. Blocks the raising
// step, not closure" (matrix:154).
//
// TASK A5 MUST SEED THE DRAFT DECISION STUDY'S WORKFLOW WITH A TRANSITION
// WHOSE Code IS THIS CONSTANT. It is the same arrangement A2 used for
// eoAssignmentTypeKey, and for the same reason: the configuration that NAMES a
// thing is seeded before the thing exists, so the name has to be one both
// tasks read rather than one each task spells.
//
// WHAT IT IS COMPARED AGAINST, exactly: services.checkBlockingChildren passes
// `transition.Code` — models.WorkflowTransition.Code, not its name, not a
// state code — to services.BlockingChildren, which compares it with
// BlocksParentTransitionKey by string equality. A transition seeded under any
// other code leaves this rule matching nothing: an objection would be raised,
// recorded, and silently ignored by the step it exists to hold up. Nothing
// reports that, because an unknown transition key is not an error anywhere —
// it is simply a comparison that never comes out true.
//
// UNTIL A5 RUNS, THE RULE CANNOT FIRE AT ALL: there is no workflow bound to
// eo_draft_decision_study, so no transition can be executed on one. That is an
// ordering fact, not an unarmed mechanism — the rule is complete on this side.
const eoDraftRaiseToHETransitionCode = "raise_to_he"

// eoReferralDeadlineOffsetDays is how many days before the LETTER'S OWN
// deadline a Referral is due. services.DeriveDeadlineFromParent computes
// parent.DueDate minus this many calendar days and writes the child's
// rec.DueDate.
//
// THE CLIENT'S DOCUMENT GIVES NO NUMBER. It says only "Counted back from the
// sender's deadline" (matrix:300) and "Deadlines are derived, not invented".
// Three days is read from the one quantity the matrix does give: the reply
// goes out "two days inside the Ministry's deadline" (matrix:318), and the
// Office cannot send a consolidated answer it has not yet consolidated — so
// the bodies' answers are due a day before the reply is prepared. It is a
// reading, not a client instruction, and it is in the report as a §9 sign-off
// item.
//
// It is also the one value here that is SAFE TO CORRECT LATER: a link type is
// not a record type, so models.ValidateTypeEdit does not guard it, and the
// offset is read at create time only — changing it moves no deadline already
// derived. A zero offset is refused by eoLinkTypeRow, because "counted back"
// with nothing counted back is the letter's own date copied onto every branch.
const eoReferralDeadlineOffsetDays = 3

// eoLinkTypeSpec is one link type of the pilot, written in record-type KEYS.
// eoLinkTypeRow resolves them to ids and refuses a spec that would seed a rule
// nothing can apply.
//
// DIRECTION. The save pipeline writes the link FROM the child TO the parent
// (services.eoRecordService.applyLinkRules), so the CHILD's type is the source
// and the parent's the target. Seeding them the other way round would make
// every create fail validation with "does not allow a source record of type …".
type eoLinkTypeSpec struct {
	key   string
	label string // English only; eoUntranslatedLinkLabel puts it in both labels
	// childTypeKey is the sub type whose records carry this link.
	childTypeKey string
	// parentTypeKeys are the types a record of childTypeKey may hang off, or
	// the single entry eoAnyTargetType for the universal Task.
	parentTypeKeys []string
	// cardinality is models.EOCardinalityOne or ...Many, counted PER SOURCE
	// RECORD: "one" means a child may carry at most one outgoing link of this
	// type. It is an UPPER bound and says nothing about a minimum — it cannot
	// express "a record of this type must have a parent" (see the parentless
	// child item, which this task deliberately leaves open).
	cardinality string
	// blocksClosure is the matrix's "Blocks the parent from closing".
	blocksClosure bool
	// blocksTransitionCode is the matrix's "Blocks the raising step, not
	// closure": the Code of the one transition an open child holds up.
	blocksTransitionCode string
	// deriveDeadline and deadlineOffsetDays are the matrix's "Deadline
	// derived from the parent, not typed … counted back from the sender's
	// deadline".
	deriveDeadline     bool
	deadlineOffsetDays int
}

// eoPilotLinkTypes is the pilot's eight link types, in the matrix's order.
//
// WHAT IS NOT HERE, DELIBERATELY:
//
//	cascade_sensitivity. Spec §7.6 offers it ("a confidential parent's
//	children inherit its label") and Plan 1's smoke link type sets it, but no
//	paragraph of the client's matrix asks for it on any pilot type, and the
//	engine's cascade is NOT monotonic: applyLinkRules assigns
//	rec.Sensitivity = parent.Sensitivity outright, so a Confidential child
//	created under an Internal parent is silently DOWNGRADED. Turning it on
//	would be inventing client policy in the direction that loses
//	classification. It is in the report as a question for the client, with
//	that engine behaviour named. See eoLinkRuleDecisions.
//
//	a link type for the three ROOT types. They have no parent.
var eoPilotLinkTypes = []eoLinkTypeSpec{
	// ── EO-DIR ───────────────────────────────────────────────────────────
	{
		key: eoExecutionPlanLinkKey, label: "Parent directive",
		childTypeKey: eoExecutionPlanTypeKey, parentTypeKeys: []string{eoDirectiveTypeKey},
		cardinality: models.EOCardinalityOne,
		// "Blocks the parent from closing." (matrix:88)
		blocksClosure: true,
	},
	{
		key: eoStageLinkKey, label: "Parent execution plan",
		childTypeKey: eoStageTypeKey, parentTypeKeys: []string{eoExecutionPlanTypeKey},
		cardinality: models.EOCardinalityOne,
		// NO RULE: the eleventh type has no paragraph in the client's
		// document, so there is no statement to implement. See
		// eoLinkRuleDecisions.
	},
	{
		key: eoAssignmentLinkKey, label: "Parent directive or execution plan",
		childTypeKey: eoAssignmentTypeKey,
		// "Sub of Execution Plan, or of Directive directly." (matrix:105)
		// Both, in that order. The matrix's own tension — an Assignment
		// numbered EO-DIR-YYYY-NNN-A# cannot be rendered under a five-segment
		// plan reference — is A3's, is pinned by
		// TestEoPilotReferencePatternsRenderAndRefuse, and is refused EARLIER
		// than this rule (allocateReference runs before applyLinkRules). The
		// link type states what the client's document states; narrowing it to
		// the Directive alone would hide the tension rather than resolve it.
		parentTypeKeys: []string{eoDirectiveTypeKey, eoExecutionPlanTypeKey},
		cardinality:    models.EOCardinalityOne,
		blocksClosure:  true,
	},

	// ── EO-DRF ───────────────────────────────────────────────────────────
	{
		key: eoObjectionLinkKey, label: "Parent draft decision study",
		childTypeKey: eoObjectionTypeKey, parentTypeKeys: []string{eoDraftStudyTypeKey},
		cardinality: models.EOCardinalityOne,
		// "Blocks the raising step, not closure." (matrix:154) The two rules
		// are distinct in services.BlockingChildren, which asks one question
		// or the other and never both.
		blocksTransitionCode: eoDraftRaiseToHETransitionCode,
	},
	{
		key: eoDecisionLinkKey, label: "Parent draft decision study",
		childTypeKey: eoDecisionTypeKey, parentTypeKeys: []string{eoDraftStudyTypeKey},
		cardinality: models.EOCardinalityOne,
		// "Closes the parent" (matrix:165) — the opposite of blocking it, and
		// not a link-type rule at all: no field of models.EOLinkType closes
		// anything. It is a workflow statement, and Task A5's. See
		// eoLinkRuleDecisions.
	},

	// ── EO-COM ───────────────────────────────────────────────────────────
	{
		key: eoReferralLinkKey, label: "Parent letter",
		childTypeKey: eoReferralTypeKey,
		// THE LETTER, AND ONLY THE LETTER — including for a SUB-REFERRAL.
		//
		// The matrix's sub-referral (`parent_referral`, "a sub-referral stays
		// visible in the parent letter's tree", matrix:304) is a LINK FIELD
		// on the referral, not a different record parent: the type's
		// paragraph says "Sub of Incoming Correspondence" and nothing else,
		// and the client's own sentence puts a sub-referral in THE PARENT
		// LETTER'S tree rather than under the referral it branched from.
		//
		// The client's own reference pattern says the same thing and the
		// engine enforces it: services.RenderReference reads IN-YYYY-NNNN-R#
		// as "a three-segment parent reference plus one segment", so a
		// referral created under another referral is refused at
		// allocateReference — "expects a parent reference of 3 segments,
		// IN-2026-0001-R1 has 4" — BEFORE any link rule is consulted.
		// Permitting a Referral target here would therefore be a permission
		// nothing could ever use: present, plausible and dead.
		//
		// The consequence for the deadline is the matrix's own wording:
		// every referral, however deep the branch, counts back from THE
		// SENDER'S deadline (matrix:300), not from a sibling referral's.
		parentTypeKeys: []string{eoIncomingTypeKey},
		cardinality:    models.EOCardinalityOne,
		// "Blocks the parent from closing." (matrix:295)
		blocksClosure: true,
		// "Deadline derived from the parent, not typed … Counted back from
		// the sender's deadline." (matrix:295, matrix:300) THIS IS THE ONLY
		// THING THAT GIVES A REFERRAL A DEADLINE: A3 seeded the field `auto`
		// so no clerk may type one, and services.DeriveDeadlineFromParent
		// writes nothing unless this flag is set.
		deriveDeadline:     true,
		deadlineOffsetDays: eoReferralDeadlineOffsetDays,
	},
	{
		key: eoOutgoingLinkKey, label: "Parent letter",
		childTypeKey: eoOutgoingTypeKey, parentTypeKeys: []string{eoIncomingTypeKey},
		cardinality: models.EOCardinalityOne,
		// "Closes the parent." (matrix:311) See eo_decision_record above.
	},

	// ── Universal ────────────────────────────────────────────────────────
	{
		key: eoTaskLinkKey, label: "Parent record",
		childTypeKey: eoTaskTypeKey,
		// "Hangs off anything, including across subsystems." (matrix:520)
		// The sentinel, NOT an empty list: see eoAnyTargetType.
		parentTypeKeys: []string{eoAnyTargetType},
		// One parent per task, which is what the universal type's own
		// `parent_ticket` row means by "Link (any type)". The polymorphism is
		// in the TARGET list, not in the count.
		cardinality: models.EOCardinalityOne,
		// "Always blocks its parent from closing." (matrix:520)
		blocksClosure: true,
	},
}

// eoUntranslatedLinkLabel puts ONE English string in both of a link type's
// labels, the way eoUntranslatedLabel does for a field.
//
// IT CANNOT MARK THE ROW, and that is a gap rather than a decision:
// eo_link_types has no ar_untranslated column, so eoUntranslatedFields — the
// §9 sign-off query — cannot see these eight labels. Adding the column is a
// migration this task does not own; the report carries the obligation, and
// TestEoLinkLabelsAwaitArabicSignOff is what keeps invented Arabic out in the
// meantime.
func eoUntranslatedLinkLabel(lt models.EOLinkType, english string) models.EOLinkType {
	lt.Label = english
	lt.LabelAr = english
	return lt
}

// eoLinkTypeRow turns one spec into the row to write, resolving record type
// keys to ids. It is pure, so its refusals are testable without a database.
//
// EVERY GUARD HERE EXISTS BECAUSE THE FAILURE IT CATCHES IS SILENT. A link
// type with no endpoints permits nothing and says so only at the first create;
// a free-form cardinality is read as "not one" and so as unbounded; an offset
// with no derivation flag is a number services.DeriveDeadlineFromParent
// returns before ever reading; both block rules at once is a claim the
// client's document makes for no type. None of them is an error to the engine
// — each is configuration that looks complete and governs nothing.
func eoLinkTypeRow(spec eoLinkTypeSpec, ids map[string]uuid.UUID) (models.EOLinkType, error) {
	var lt models.EOLinkType
	if strings.TrimSpace(spec.key) == "" {
		return lt, fmt.Errorf("a link type with no key cannot be named by a create request or found by services.BlockingChildren")
	}
	if strings.TrimSpace(spec.label) == "" {
		return lt, fmt.Errorf("link type %q has no label; it would render as a blank row in the designer and on the record", spec.key)
	}
	switch spec.cardinality {
	case models.EOCardinalityOne, models.EOCardinalityMany:
	default:
		return lt, fmt.Errorf("link type %q has cardinality %q; it must be %q or %q, because the spec enumerates no values and services.ValidateLink reads anything else as unbounded",
			spec.key, spec.cardinality, models.EOCardinalityOne, models.EOCardinalityMany)
	}
	if spec.childTypeKey == eoAnyTargetType {
		return lt, fmt.Errorf("link type %q declares a polymorphic source; no row of the client's matrix asks for one, and %q as a source would let a record of ANY type carry this link type's rules",
			spec.key, eoAnyTargetType)
	}
	childID, ok := ids[spec.childTypeKey]
	if !ok || childID == uuid.Nil {
		return lt, fmt.Errorf("link type %q names the child record type %q, which the pilot does not seed", spec.key, spec.childTypeKey)
	}
	if len(spec.parentTypeKeys) == 0 {
		return lt, fmt.Errorf("link type %q names no parent record type, so it permits nothing: services.eoTypeAllowed refuses every target for an empty list, and the column's default is '[]'", spec.key)
	}
	targets := make([]string, 0, len(spec.parentTypeKeys))
	for _, key := range spec.parentTypeKeys {
		if key == eoAnyTargetType {
			if len(spec.parentTypeKeys) != 1 {
				return lt, fmt.Errorf("link type %q lists %q beside named parent types; %q already permits every type and the list would be two statements of different sizes",
					spec.key, eoAnyTargetType, eoAnyTargetType)
			}
			targets = append(targets, eoAnyTargetType)
			continue
		}
		id, ok := ids[key]
		if !ok || id == uuid.Nil {
			return lt, fmt.Errorf("link type %q names the parent record type %q, which the pilot does not seed", spec.key, key)
		}
		targets = append(targets, id.String())
	}
	if spec.blocksClosure && spec.blocksTransitionCode != "" {
		return lt, fmt.Errorf("link type %q blocks closure AND names the blocked transition %q; the client's matrix states one or the other for every type, and services.BlockingChildren asks one question or the other",
			spec.key, spec.blocksTransitionCode)
	}
	if spec.deriveDeadline != (spec.deadlineOffsetDays != 0) {
		return lt, fmt.Errorf("link type %q: derive_deadline_from_parent is %v with an offset of %d days; a derivation with no offset copies the parent's own deadline onto the child and an offset without the flag is a number services.DeriveDeadlineFromParent never reads",
			spec.key, spec.deriveDeadline, spec.deadlineOffsetDays)
	}
	// A NEGATIVE offset is the one shape the equality above lets through, and
	// it inverts the rule rather than breaking it: DeriveDeadlineFromParent
	// computes parent.DueDate.AddDate(0, 0, -offset), so -3 moves the CHILD'S
	// deadline three days LATER than its parent's. A child due after the
	// deadline it was counted back from is not a derived deadline at all — the
	// SLA clock arms on it, the parent goes overdue first, and the
	// configuration reads as if the child were being given time. The matrix
	// says "counted back", so the guard is "counted back by at least a day".
	if spec.deriveDeadline && spec.deadlineOffsetDays <= 0 {
		return lt, fmt.Errorf("link type %q derives a deadline with an offset of %d days; services.DeriveDeadlineFromParent subtracts it, so a non-positive offset makes the child due no earlier than its parent — the opposite of a deadline counted back",
			spec.key, spec.deadlineOffsetDays)
	}

	source, err := json.Marshal([]string{childID.String()})
	if err != nil {
		return lt, fmt.Errorf("encode the source types of link type %q: %w", spec.key, err)
	}
	target, err := json.Marshal(targets)
	if err != nil {
		return lt, fmt.Errorf("encode the target types of link type %q: %w", spec.key, err)
	}
	lt = models.EOLinkType{
		ID:                        uuid.New(),
		Key:                       spec.key,
		SourceTypeIDs:             datatypes.JSON(source),
		TargetTypeIDs:             datatypes.JSON(target),
		Cardinality:               spec.cardinality,
		DeriveDeadlineFromParent:  spec.deriveDeadline,
		DeadlineOffsetDays:        spec.deadlineOffsetDays,
		BlocksParentClosure:       spec.blocksClosure,
		BlocksParentTransitionKey: spec.blocksTransitionCode,
		// CascadeSensitivity is left false on every pilot link type; see
		// eoPilotLinkTypes and eoLinkRuleDecisions.
	}
	return eoUntranslatedLinkLabel(lt, spec.label), nil
}

// eoSeedLinkTypes is the pilot seed's "seed_link_types" step.
//
// A STEP OF ITS OWN, like A3's: the applied rows for seed_root_types and
// seed_child_types already exist on every database that has booted Task A2 or
// A3, so work folded into either would never run there (see eoPilotStep).
//
// It fails closed on a missing record type rather than skipping the link type:
// a sub type with no link type cannot be created with a parent at all
// (eoRecordService.Create refuses a parent given with no link type key, and
// the five parent-derived types cannot be numbered without one), so a
// half-seeded set of link types is a pilot that cannot record a referral.
func eoSeedLinkTypes(tx *gorm.DB) (bool, error) {
	var types []models.EORecordType
	if err := tx.Where("key IN ?", eoPilotTypeKeys).Find(&types).Error; err != nil {
		return false, fmt.Errorf("read the pilot record types: %w", err)
	}
	ids := make(map[string]uuid.UUID, len(types))
	for _, ty := range types {
		ids[ty.Key] = ty.ID
	}
	for _, key := range eoPilotTypeKeys {
		if ids[key] == uuid.Nil {
			return false, fmt.Errorf("record type %q is missing; the pilot's record types must be seeded before its link types", key)
		}
	}

	for _, spec := range eoPilotLinkTypes {
		lt, err := eoLinkTypeRow(spec, ids)
		if err != nil {
			return false, err
		}
		if err := tx.Create(&lt).Error; err != nil {
			return false, fmt.Errorf("create link type %q: %w", spec.key, err)
		}
		log.Printf("EO pilot: seeded link type %q (%s -> %v, %s)", spec.key, spec.childTypeKey, spec.parentTypeKeys, spec.cardinality)
	}
	return true, nil
}

// eoLinkRuleDecision records what one link type does with the sentence the
// client's matrix writes about its type — including the two types whose
// answer is "no rule" and the one type the document does not mention.
//
// It exists for the same reason eoConditionalDecisions does: a rule that is
// ABSENT and a rule that was deliberately NOT SEEDED look identical in a row
// of configuration, and the engine reports neither. MatrixProse is checked
// against the client's document and ChildTypeKey against the seeded spec by
// TestEoLinkRuleDecisionsCoverEverySeededLinkType, so an entry cannot drift
// from either.
type eoLinkRuleDecision struct {
	LinkKey      string
	ChildTypeKey string
	// MatrixProse is the client's own sentence, verbatim, from the paragraph
	// under that type's heading. Empty ONLY for eo_execution_plan_stage,
	// which has no section at all.
	MatrixProse string
	// Rule is what was seeded, in the engine's terms.
	Rule string
	// Mechanism names the code that reads the rule.
	Mechanism string
	Why       string
}

// eoLinkRuleDecisions is the registry, in the order the matrix states them.
var eoLinkRuleDecisions = []eoLinkRuleDecision{
	{eoExecutionPlanLinkKey, eoExecutionPlanTypeKey,
		"Blocks the parent from closing.",
		"blocks_parent_closure, no transition key",
		"services.BlockingChildren with an empty transition key, from eoTransitionService.checkBlockingChildren on every move into a TERMINAL state",
		"The matrix's \"partial closure is not closure\" (spec §7.6) for EO-DIR. An OPEN plan blocks; services.BlockingChildren drops a child whose closed_at is set, so closing the plan releases the directive without any further configuration."},

	{eoStageLinkKey, eoStageTypeKey,
		"",
		"no rule: hierarchy only",
		"services.ValidateLink (the endpoints and the cardinality) and eoRecordService.applyLinkRules, which needs A link type to create a child with a parent at all",
		"THE ELEVENTH TYPE, which the client's matrix has no section for: it exists because spec §7.3 drops the `table` kind, so Execution Plan's `plan_stages` became a child record type (see eoMatrixFieldsNotSeeded). There is therefore no client sentence to implement, and a blocking rule invented for it would hold a plan open on the strength of nobody's instruction. It is seeded BECAUSE the type is parent-derived and so uncreatable without a link type — services.RenderReference refuses to number it with no parent, and Create refuses a parent given with no link type key — not because it carries a rule. If the client wants an unfinished stage to hold its plan open, this is the row that changes."},

	{eoAssignmentLinkKey, eoAssignmentTypeKey,
		"Sub of Execution Plan, or of Directive directly. Blocks the parent from closing.",
		"blocks_parent_closure, with BOTH parent types permitted",
		"services.BlockingChildren with an empty transition key; the endpoint list is read by services.eoTypeAllowed through ValidateLink",
		"Two target types, because the client's sentence names two. The matrix's own tension — the single pattern EO-DIR-YYYY-NNN-A# cannot be rendered under a five-segment plan reference — is NOT resolved here and is not this rule's to resolve: services.RenderReference refuses it at allocateReference, which runs BEFORE applyLinkRules, so the link type never sees that create. Narrowing the targets to the Directive would make the configuration agree with the numbering and disagree with the client. A3 pinned the refusal; A4/A5 put the question to the client."},

	{eoObjectionLinkKey, eoObjectionTypeKey,
		"Must be resolved or dismissed before the parent can go to H.E. Blocks the raising step, not closure.",
		"blocks_parent_transition_key = " + eoDraftRaiseToHETransitionCode + ", blocks_parent_closure FALSE",
		"services.BlockingChildren with transition.Code, from checkBlockingChildren on EVERY transition that has a code",
		"The first producer of blocks_parent_transition_key in this programme: Plan 1 implemented it and left it unseeded, so the branch has never run on seeded configuration. The two rules are not interchangeable — closure is asked only on a move into a terminal state, the named one on every coded transition — and the matrix is explicit that an objection blocks the raising step and NOT closure, so a study with an open objection may still be closed (returned, withdrawn) while it may not be put in front of H.E. THE ORDERING: the transition this names is Task A5's to seed, under eoDraftRaiseToHETransitionCode. Until it does, there is no workflow on the type at all, so nothing can be transitioned and the rule cannot fire — it is unreachable, not unarmed."},

	{eoDecisionLinkKey, eoDecisionTypeKey,
		"Closes the parent, then joins the base that checks the next draft.",
		"no block of either kind",
		"services.ValidateLink and applyLinkRules; the CLOSING is not a link-type rule and no field of models.EOLinkType expresses it",
		"\"Closes the parent\" is the opposite of blocking it, and the engine's link types cannot say it: there is no close_parent flag, and nothing in the save or transition pipeline closes one record because another was created. It is a WORKFLOW statement — a transition on the Draft Decision Study that the Decision Record's creation drives — and so Task A5's, through a transition action (spec §7.8). Seeding blocks_parent_closure here would be the exact inversion of the client's sentence: a decision record that held its study open for ever."},

	{eoReferralLinkKey, eoReferralTypeKey,
		"Deadline derived from the parent, not typed. Blocks the parent from closing.",
		fmt.Sprintf("derive_deadline_from_parent with an offset of %d days, and blocks_parent_closure", eoReferralDeadlineOffsetDays),
		"services.DeriveDeadlineFromParent, called by applyLinkRules on create, writing the child's rec.DueDate column; and services.BlockingChildren for the closure half",
		"THE RULE A3 LEFT UNARMED AND THIS TASK ARMS. Referral's `deadline` is the inherited due_date column, narrowed to provenance `auto` so no clerk may supply one (ValidateRecordValues refuses a submitted value for an auto field), and DeriveDeadlineFromParent is the only mechanism that fills it. Until this flag was seeded every Referral had an empty deadline and no way to acquire one — and an absent deadline is not an error anywhere in the engine, so nothing would have reported it. The offset is a READING of the matrix, not a client figure; see eoReferralDeadlineOffsetDays. The parent value it reads is populated because A2 made the letter's `sender_deadline` BE the due_date column and required it. THE TARGET IS THE LETTER ALONE, sub-referrals included: the client's pattern IN-YYYY-NNNN-R# cannot be rendered under a four-segment referral reference, so a referral under a referral is refused at allocateReference before this rule is reached, and the matrix carries the sub-referral on the `parent_referral` LINK FIELD instead. Every referral therefore counts back from the sender's deadline, which is what matrix:300 says."},

	{eoOutgoingLinkKey, eoOutgoingTypeKey,
		"Closes the parent. One per letter, however many rounds of direction it took.",
		"no block of either kind",
		"services.ValidateLink and applyLinkRules; see eo_decision_record_parent for the closing half",
		"The same statement as the Decision Record's and the same answer. \"One per letter\" is NOT what Cardinality says — cardinality is counted per SOURCE record, so `one` here means one reply carries one parent link, not that one letter carries one reply. Nothing in the link-type model bounds the children of a parent; what makes a second reply visible is the OUT sequence, and holding the client to \"one per letter\" is a workflow matter."},

	{eoTaskLinkKey, eoTaskTypeKey,
		"Hangs off anything, including across subsystems. Always blocks its parent from closing.",
		"target types = the \"" + eoAnyTargetType + "\" sentinel, blocks_parent_closure",
		"services.eoTypeAllowed, which accepts the sentinel for any target type; services.BlockingChildren for the block",
		"The polymorphic type, and the one place where leaving a field UNSET would have produced this programme's signature defect in its purest form: an empty target list permits nothing, so a Task \"hanging off anything\" would have been refused by everything. The sentinel is the literal string services.eoAnyType carries, repeated here because that constant is unexported in a package this one cannot import, and tied to it by TestEoAnyTargetSentinelMatchesTheEngineConstant. WHAT THIS DOES NOT CLOSE: nothing refuses a PARENTLESS Task. Cardinality is an upper bound and applyLinkRules returns early on a nil parent, so ValidateLink is never reached; the work is a create-path rule. That item stays OPEN — see eoEngineKeyDecisions (eo_task.parent_ticket)."},
}
