package database

import (
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
	// (ticket_type), workflow state (state), or the attachment store. The
	// BASE type does seed definitions named record_type_id, state_id and
	// attachments (all engine-written or engine-handled), so this does NOT mean
	// "there is no definition"; it means a pilot type must never declare one.
	isEngineManaged bool
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
// is copied from the base type's so that a later change to the base's label,
// kind or options cannot leave a stale copy behind on a pilot type.
type eoInheritedNarrowing struct {
	matrixKey string
	required  string
	rule      string
}

// eoPilotTypeSpec is one root record type.
type eoPilotTypeSpec struct {
	key, name, icon string
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

// eoConditionalDecisions is the registry. Read it with the matrix open.
var eoConditionalDecisions = []eoConditionalDecision{
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
		models.EORequiredNever,
		"The clock that watches an Incoming Correspondence is `sender_deadline` — the matrix says \"every internal clock is derived from this date\" — and that field is seeded `always`. due_date is not this type's clock."},

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
		prefix: "EO-DIR", pattern: "EO-DIR-YYYY-NNN",
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
			// resolves to nothing on a directive with no assignments yet and
			// is reported as waiting on data, not as a misconfiguration.
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
		prefix: "DRF", pattern: "DRF-YYYY-NNN",
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
		prefix: "IN", pattern: "IN-YYYY-NNNN",
		fields: []eoPilotFieldSpec{
			{matrixKey: "sender", label: "Sending body",
				kind: models.EOKindExternalBody, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "sender_ref", label: "Their reference",
				kind: models.EOKindText, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "letter_date", label: "Letter date",
				kind: models.EOKindDate, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "received_date", label: "Date received",
				kind: models.EOKindDate, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			{matrixKey: "sender_deadline", label: "Their deadline",
				kind: models.EOKindDate, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
			// The matrix marks `attachments` Required YES on this type. It is
			// ENGINE-MANAGED (eoMatrixKey), so no pilot type may declare it
			// and eoSeedRootTypes drops it: the base type's definition, which
			// is `never`, governs. This is the one place the three root types
			// say something WEAKER than the matrix, and it is kept in the list
			// so the drop happens through the helper and is visible here.
			{matrixKey: "attachments", label: "Attachments",
				kind: models.EOKindMultiFile, provenance: models.EOProvenanceUser, required: models.EORequiredAlways},
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
// own-mode, which is the only kind the rule governs.
//
// Every sub-step returns its error; nothing is logged and skipped.
func eoSeedRootTypes(tx *gorm.DB) (bool, error) {
	var base models.EORecordType
	if err := tx.Where("key = ?", eoBaseTypeKey).Limit(1).Find(&base).Error; err != nil {
		return false, fmt.Errorf("read record type %q: %w", eoBaseTypeKey, err)
	}
	if base.ID == uuid.Nil {
		return false, fmt.Errorf("record type %q is missing; the platform seed must run before the pilot seed", eoBaseTypeKey)
	}
	baseFields := eoBaseFieldDefs(base.ID)

	for _, spec := range eoPilotRootTypes {
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
			ReferenceMode:    models.EOReferenceModeOwn,
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

		// Read the register back for every type, so the second and third are
		// validated against the first as well as against what was already
		// there. One query on the transaction's own handle.
		var existing []models.EORecordType
		if err := tx.Find(&existing).Error; err != nil {
			return false, fmt.Errorf("read the record type register: %w", err)
		}
		if err := models.ValidateReferencePrefix(t, existing); err != nil {
			return false, fmt.Errorf("record type %q: %w", spec.key, err)
		}
		if err := tx.Create(&t).Error; err != nil {
			return false, fmt.Errorf("create record type %q: %w", spec.key, err)
		}
		if err := migrations.CreateEORecordPartition(tx, t.Key, t.ID); err != nil {
			return false, err
		}
		// Only now: the label says "this type's records are in the default
		// partition", and that stopped being true one statement ago.
		if err := tx.Model(&models.EORecordType{}).Where("id = ?", t.ID).
			Update("partition_pending", false).Error; err != nil {
			return false, fmt.Errorf("clear partition_pending on %q: %w", spec.key, err)
		}

		defs, err := eoPilotFieldDefs(spec, t.ID, baseFields)
		if err != nil {
			return false, err
		}
		for _, d := range defs {
			if err := tx.Create(&d).Error; err != nil {
				return false, fmt.Errorf("create field %q on %q: %w", d.Key, spec.key, err)
			}
		}
		log.Printf("EO pilot: seeded record type %q (%s) with %d fields and its own partition %s",
			spec.key, spec.prefix, len(defs), migrations.EORecordPartitionName(spec.key))
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
		if isColumn || isEngineManaged {
			// Declared by the base type already (a column), or never
			// declarable by a pilot type (engine-managed). A narrowing of a
			// column key goes through spec.narrowed, which copies the base's
			// definition instead of writing a second one.
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
