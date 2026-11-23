package database

import (
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// The keys the seed owns. They are the contract between this file, the
// per-type permission codes derived from them, and the end-to-end test in
// internal/services that drives the engine over this configuration.
const (
	// eoBaseTypeKey is the ABSTRACT base record type. It holds no records of
	// its own; it carries the 17 field definitions of the field matrix that
	// every concrete type inherits (spec §7.2 resolves inheritance at read
	// time — nothing is copied onto a child type).
	eoBaseTypeKey = "eo_base"
	// eoSmokeParentTypeKey and eoSmokeChildTypeKey are the smoke pair: a root
	// record numbered from its own sequence, and a child numbered inside its
	// parent's reference.
	eoSmokeParentTypeKey = "eo_smoke_directive"
	eoSmokeChildTypeKey  = "eo_smoke_task"
	// eoSmokeLinkTypeKey carries the rules that apply to a smoke child:
	// a deadline counted back from the parent's, the parent's sensitivity,
	// and the block on closing a parent whose child is still open.
	eoSmokeLinkTypeKey = "eo_smoke_parent"

	eoSmokePolicyName = "EO Smoke Escalation"
)

// seedEOPlatform writes the Executive Office platform's base configuration:
// the abstract base record type with the 17 inherited field definitions, the
// two concrete smoke types that exercise the engine end to end, their link
// type, their workflows, the escalation policy their open state's SLA points
// at, and the per-type permission codes of spec §7.11.
//
// House seed shape: unexported, takes the handle, returns nothing, logs what
// it could not write.
//
// TWO guards, and they do different jobs:
//
//  1. The MARKER — "does eo_base exist?" — is one query outside the
//     transaction, and it is the fast path on every container boot: a
//     per-row existence check across 17+ field definitions would be paid on
//     every restart forever.
//  2. The TRANSACTION is what makes the marker trustworthy. The marker row
//     IS the base record type, and it is written first, so without a
//     transaction a crash or one bad insert partway through would leave a
//     database whose marker says "seeded" and whose configuration is half
//     written — and every later boot would return early at the marker and
//     never repair it. The operator would see a clean startup and a broken
//     platform. Inside one transaction the marker and the content commit
//     together or not at all, so a failed seed leaves no marker and the NEXT
//     boot starts over.
//
// Every step therefore returns an error rather than logging and carrying on:
// a seed that half-succeeds is worse than one that does not run.
//
// Two boots racing is handled by the same mechanism and needs no extra
// locking: eo_record_types.key is uniquely indexed, so the second
// transaction's insert of eo_base collides and that whole transaction rolls
// back, leaving the first one's work intact.
//
// It must run AFTER the role block of Seed: the smoke workflows attach the
// executive_office role to their closing transition, and the per-type
// permission codes are granted to admin and executive_office.
func seedEOPlatform(db *gorm.DB) {
	// The marker. One query, before any other work.
	var seeded int64
	if err := db.Model(&models.EORecordType{}).Where("key = ?", eoBaseTypeKey).Count(&seeded).Error; err != nil {
		log.Printf("EO platform seed: could not check for record type %q: %v", eoBaseTypeKey, err)
		return
	}
	if seeded > 0 {
		return
	}
	log.Println("Seeding the EO platform base configuration...")

	if err := db.Transaction(eoSeedPlatformTx); err != nil {
		// Loud, and on its own lines. A single warning in a wall of startup
		// output is how this failure goes unnoticed, and it is a failure that
		// LOOKS like a clean boot: the whole seed rolls back, so the platform
		// is simply absent — no base type, no smoke types, no per-type
		// permissions — while the server starts and serves EO routes over
		// nothing. The case that actually produces it is a box that ran an
		// INTERMEDIATE commit of this branch and holds a partial
		// configuration: the marker row is missing, so the seed runs again
		// every boot, and a leftover row (a type key, a permission code, a
		// workflow code) collides with the one being written and rolls the
		// whole transaction back, forever, silently.
		log.Printf("EO platform seed FAILED and was rolled back: %v", err)
		log.Printf("EO platform: EO CONFIGURATION WAS NOT APPLIED. No record types, workflows or per-type permissions exist; any EO route served now will find an empty platform.")
		log.Printf("EO platform: this will repeat on every boot until the cause is removed. Inspect the database — see docs/eo-platform-deploying.md, 'Deploying', for the pre-deploy check for environments that ran this branch mid-flight.")
		return
	}
	log.Println("EO platform base configuration seeded")
}

// eoSeedPlatformTx is the whole seed, on one transaction handle. Every write
// goes through tx.
func eoSeedPlatformTx(tx *gorm.DB) error {
	base, err := eoSeedBaseType(tx)
	if err != nil {
		return err
	}
	policyID, err := eoSeedEscalationPolicy(tx)
	if err != nil {
		return err
	}
	parent, err := eoSeedSmokeParentType(tx, base.ID)
	if err != nil {
		return err
	}
	child, err := eoSeedSmokeChildType(tx, base.ID)
	if err != nil {
		return err
	}
	if err := eoSeedSmokeLinkType(tx, parent.ID, child.ID); err != nil {
		return err
	}
	if err := eoSeedSmokeWorkflow(tx, parent, "eo_smoke_directive_wf", "EO Smoke — Directive", policyID); err != nil {
		return err
	}
	if err := eoSeedSmokeWorkflow(tx, child, "eo_smoke_task_wf", "EO Smoke — Task", policyID); err != nil {
		return err
	}
	return eoSeedTypePermissions(tx)
}

// seedEOPlatformWhenEnabled is the gate Seed applies. It is a function rather
// than an inline `if` so the gate itself is under test: a seed that ran on a
// deployment which has not bought the platform would put record types and
// permission codes into a database whose owner never asked for them.
func seedEOPlatformWhenEnabled(db *gorm.DB, enabled bool) {
	if !enabled {
		return
	}
	seedEOPlatform(db)
}

// SeedEOPlatform applies the EO base configuration to db. It is the exported
// entry point seedEOPlatform backs, so that another package's test can put the
// REAL seeded configuration in front of the engine instead of a hand-built
// copy that would drift from it.
func SeedEOPlatform(db *gorm.DB) { seedEOPlatform(db) }

// ── the abstract base type and its 17 inherited fields ──────────────────────

// eoSeedBaseType writes the abstract base record type.
//
// ReferencePattern is set even though an abstract type never allocates a
// reference: models.EORecordType.ReferencePattern has no default,
// models.ValidateTypeEdit treats a later change to it as a DESTRUCTIVE edit,
// and a type seeded without one cannot be quietly corrected afterwards. It is
// the shape a concrete descendant is expected to follow.
//
// ReferencePrefix is deliberately empty: an abstract type owns no prefix (the
// designer's eoCheckReferencePrefix skips abstract types for exactly that
// reason), and a prefix here would overlap every descendant's.
func eoSeedBaseType(db *gorm.DB) (*models.EORecordType, error) {
	base := models.EORecordType{
		ID:               uuid.New(),
		Key:              eoBaseTypeKey,
		Name:             "Executive Office Base Record",
		NameAr:           "سجل المكتب التنفيذي الأساسي",
		Icon:             "file-text",
		IsAbstract:       true,
		ReferenceMode:    models.EOReferenceModeOwn,
		ReferencePattern: "EO-YYYY-NNNN",
		Capabilities:     datatypes.JSON(`[]`),
		IsActive:         true,
		// An abstract type holds no records, so no partition will ever be
		// wanted for it.
		PartitionPending: false,
	}
	if err := db.Create(&base).Error; err != nil {
		return nil, fmt.Errorf("create record type %q: %w", base.Key, err)
	}
	for _, f := range eoBaseFieldDefs(base.ID) {
		if err := db.Create(&f).Error; err != nil {
			return nil, fmt.Errorf("create field %q on %q: %w", f.Key, base.Key, err)
		}
	}
	return &base, nil
}

// eoBaseFieldDefs is the field matrix's "All types (inherited)" table: the
// seventeen fields every record type gets from the base record.
// TestBaseTypeHasSeventeenInheritedFields asserts the count exactly, so a
// dropped or duplicated field is a test failure rather than a field that
// silently never appears on any type.
//
// THE KEYS ARE THE ENGINE'S, NOT THE MATRIX'S. The matrix names a field by
// what the client calls it; services.eoRecordColumnWriters and
// services.eoEngineOwnedKeys name it by the eo_records column it writes, and a
// definition keyed anything else would land in the values jsonb instead of its
// column. The three renames are:
//
//	matrix `classification`    -> `sensitivity`      (Classification already
//	                                                  means the incident
//	                                                  taxonomy here — §7.11)
//	matrix `owner_department`  -> `owning_body_id`
//	matrix `assigned_user`     -> `assigned_user_id`
//
// and the matrix's `ticket_type`, `parent_ticket`, `state` and `closed_on` are
// the engine's `record_type_id`, `parent_record_id`, `state_id` and
// `closed_at`. `attachments` has no column and lives in the values bag, which
// is what models.EORecord's comment records.
//
// PROVENANCE. Everything the engine writes itself is `auto`:
// services.ValidateRecordValues rejects a submitted value for an `auto` or
// `derived` field with a field-level message, while `system` falls through to
// the requiredness checks and would only be caught later by the
// eoEngineOwnedKeys backstop. That includes `record_type_id` and
// `parent_record_id`, which the matrix marks Yes and Conditional: the client
// does choose them, but on the create REQUEST, not as values, so as far as the
// value pipeline is concerned the engine sets them.
//
// REQUIREDNESS. The matrix's five "Conditional" fields are seeded `never` on
// the BASE type, because a conditional field needs a required_rule and none of
// the five conditions can be written as one here:
//
//   - `parent_ticket` is "mandatory on every sub type" — a per-TYPE statement,
//     so a concrete sub type narrows it, not the base;
//   - `assigned_user` is "set by the transition's assignment rules" — a
//     workflow statement, not a form condition;
//   - `due_date` is "required on any type the clock watches" — again per type;
//   - `change_reason` is "material changes are logged" — the engine cannot
//     tell a material change from any other;
//   - `delay_reason` is "mandatory once the due date passes" — a clock
//     condition, and eoformula has no now()/today(), by design (it is a pure
//     evaluator).
//
// Narrowing an inherited field is a first-class operation
// (services.ResolveEffectiveFields lets a type redefine an ancestor's key in
// place), so each concrete type states its own answer. A conditional field
// seeded with no rule would NOT be a lenient default: ValidateRecordValues
// returns a hard error for it and every save of every type would fail.
func eoBaseFieldDefs(typeID uuid.UUID) []models.EOFieldDef {
	f := func(order int, key, label, labelAr, kind, provenance, required string) models.EOFieldDef {
		return models.EOFieldDef{
			ID:           uuid.New(),
			RecordTypeID: typeID,
			Key:          key,
			Label:        label,
			LabelAr:      labelAr,
			Kind:         kind,
			Provenance:   provenance,
			RequiredMode: required,
			Config:       datatypes.JSON(`{}`),
			SortOrder:    order,
			// Written by an implementer, not supplied by the client: awaiting
			// Arabic sign-off. See eoUntranslatedLabel.
			ArUntranslated: true,
		}
	}
	withOptions := func(d models.EOFieldDef, cfg string) models.EOFieldDef {
		d.Config = datatypes.JSON(cfg)
		return d
	}

	return []models.EOFieldDef{
		f(1, "reference", "Reference", "الرقم المرجعي",
			models.EOKindAutoNumber, models.EOProvenanceAuto, models.EORequiredNever),
		f(2, "subject_ar", "Subject (Arabic)", "الموضوع (عربي)",
			models.EOKindText, models.EOProvenanceUser, models.EORequiredAlways),
		f(3, "subject_en", "Subject (English)", "الموضوع (إنجليزي)",
			models.EOKindText, models.EOProvenanceUser, models.EORequiredNever),
		f(4, "record_type_id", "Ticket type", "نوع السجل",
			models.EOKindLink, models.EOProvenanceAuto, models.EORequiredNever),
		f(5, "parent_record_id", "Parent ticket", "السجل الأصل",
			models.EOKindLink, models.EOProvenanceAuto, models.EORequiredNever),
		f(6, "state_id", "State", "الحالة",
			models.EOKindWorkflowState, models.EOProvenanceAuto, models.EORequiredNever),
		withOptions(f(7, "priority", "Priority", "الأولوية",
			models.EOKindDropdown, models.EOProvenanceUser, models.EORequiredAlways),
			`{"options":[{"value":"normal","label":"Normal","label_ar":"عادي"},{"value":"high","label":"High","label_ar":"عالي"},{"value":"urgent","label":"Urgent","label_ar":"عاجل"}]}`),
		withOptions(f(8, "sensitivity", "Classification", "التصنيف",
			models.EOKindDropdown, models.EOProvenanceUser, models.EORequiredAlways),
			`{"options":[{"value":"public","label":"Public","label_ar":"عام"},{"value":"internal","label":"Internal","label_ar":"داخلي"},{"value":"confidential","label":"Confidential","label_ar":"سري"},{"value":"highly_confidential","label":"Highly confidential","label_ar":"سري للغاية"}]}`),
		withOptions(f(9, "source", "Source", "المصدر",
			models.EOKindDropdown, models.EOProvenanceUser, models.EORequiredAlways),
			`{"options":[{"value":"weekly_meeting","label":"Weekly meeting","label_ar":"الاجتماع الأسبوعي"},{"value":"direct_instruction","label":"Direct instruction","label_ar":"توجيه مباشر"},{"value":"correspondence","label":"Correspondence","label_ar":"مراسلة"},{"value":"official_visit","label":"Official visit","label_ar":"زيارة رسمية"},{"value":"media_commitment","label":"Media commitment","label_ar":"التزام إعلامي"},{"value":"committee","label":"Committee","label_ar":"لجنة"}]}`),
		f(10, "owning_body_id", "Owning body", "الجهة المالكة",
			models.EOKindOrgUnit, models.EOProvenanceUser, models.EORequiredAlways),
		f(11, "assigned_user_id", "Assigned user", "المستخدم المكلّف",
			models.EOKindUser, models.EOProvenanceUser, models.EORequiredNever),
		f(12, "due_date", "Due date", "تاريخ الاستحقاق",
			models.EOKindDate, models.EOProvenanceUser, models.EORequiredNever),
		// One definition, not two: the matrix's "Created by and on" row names
		// created_by and created_at together, and models.EOKindUserDate is
		// exactly that primitive — one kind, not two fields. It is keyed on
		// the column that carries the identity; the instant beside it is the
		// DB-stamped created_at.
		f(13, "created_by_id", "Created by and on", "أنشئ بواسطة وفي",
			models.EOKindUserDate, models.EOProvenanceAuto, models.EORequiredNever),
		f(14, "closed_at", "Closed on", "تاريخ الإغلاق",
			models.EOKindDateTime, models.EOProvenanceAuto, models.EORequiredNever),
		f(15, "attachments", "Attachments", "المرفقات",
			models.EOKindMultiFile, models.EOProvenanceUser, models.EORequiredNever),
		f(16, "change_reason", "Change reason", "سبب التغيير",
			models.EOKindLongText, models.EOProvenanceUser, models.EORequiredNever),
		f(17, "delay_reason", "Delay reason", "سبب التأخير",
			models.EOKindLongText, models.EOProvenanceUser, models.EORequiredNever),
	}
}

// ── the smoke pair ──────────────────────────────────────────────────────────

// eoSeedSmokeParentType is the root of the smoke hierarchy: its own sequence,
// restarting each year, and one derived roll-up over its children.
func eoSeedSmokeParentType(db *gorm.DB, baseID uuid.UUID) (*models.EORecordType, error) {
	t := models.EORecordType{
		ID:               uuid.New(),
		Key:              eoSmokeParentTypeKey,
		Name:             "EO Smoke Directive",
		NameAr:           "توجيه اختباري",
		Icon:             "clipboard-list",
		ParentTypeID:     &baseID,
		IsAbstract:       false,
		ReferencePrefix:  "EO-SMOKE",
		ReferenceMode:    models.EOReferenceModeOwn,
		ReferencePattern: "EO-SMOKE-YYYY-NNN",
		Capabilities:     datatypes.JSON(`[]`),
		IsActive:         true,
		PartitionPending: true,
	}
	if err := db.Create(&t).Error; err != nil {
		return nil, fmt.Errorf("create record type %q: %w", t.Key, err)
	}
	// The one derived field: a roll-up over the children of the smoke child
	// type. `child` dependencies always publish children_<type>_count, so the
	// formula resolves to 0 on a record that has no children yet rather than
	// failing as an unresolved reference.
	rollup := models.EOFieldDef{
		ID:           uuid.New(),
		RecordTypeID: t.ID,
		Key:          "child_task_count",
		Label:        "Tasks",
		LabelAr:      "عدد المهام",
		Kind:         models.EOKindNumber,
		Provenance:   models.EOProvenanceDerived,
		RequiredMode: models.EORequiredNever,
		Config:       datatypes.JSON(`{}`),
		Formula:      "children_" + eoSmokeChildTypeKey + "_count",
		RecomputeOn:  datatypes.JSON(`[{"scope":"child","type":"` + eoSmokeChildTypeKey + `"}]`),
		SortOrder:    100,
		HelpText:     "How many tasks hang off this directive. Recomputed whenever the directive is saved or transitioned.",
		HelpTextAr:   "عدد المهام المرتبطة بهذا التوجيه.",
	}
	if err := db.Create(&rollup).Error; err != nil {
		return nil, fmt.Errorf("create field %q on %q: %w", rollup.Key, t.Key, err)
	}
	return &t, nil
}

// eoSeedSmokeChildType is numbered INSIDE its parent's reference: the pattern's
// last segment is a suffix letter and a sequence counted per parent, so the
// first task of EO-SMOKE-2026-001 is EO-SMOKE-2026-001-T1.
//
// ReferencePrefix is EMPTY, and that is the honest configuration: this type
// owns no prefix, because every reference it issues is inside its parent's.
// models.ValidateReferencePrefix exempts ReferenceMode "parent" types for
// exactly that reason, so the designer accepts it; before that exemption
// existed this type had to carry a placeholder prefix that was a small lie
// about the references it issues, made permanent by ValidateTypeEdit's rule
// that a prefix change is destructive once records exist.
func eoSeedSmokeChildType(db *gorm.DB, baseID uuid.UUID) (*models.EORecordType, error) {
	t := models.EORecordType{
		ID:               uuid.New(),
		Key:              eoSmokeChildTypeKey,
		Name:             "EO Smoke Task",
		NameAr:           "مهمة اختبارية",
		Icon:             "check-square",
		ParentTypeID:     &baseID,
		IsAbstract:       false,
		ReferencePrefix:  "",
		ReferenceMode:    models.EOReferenceModeParent,
		ReferencePattern: "EO-SMOKE-YYYY-NNN-T#",
		Capabilities:     datatypes.JSON(`[]`),
		IsActive:         true,
		PartitionPending: true,
	}
	if err := db.Create(&t).Error; err != nil {
		return nil, fmt.Errorf("create record type %q: %w", t.Key, err)
	}
	return &t, nil
}

// eoSeedSmokeLinkType carries the three rules of spec §7.6 that a parent/child
// link can hold: a deadline counted back from the parent's, the parent's
// sensitivity label, and the block on closing a parent whose child is open.
//
// Direction: the save pipeline writes the link FROM the child TO the parent,
// so the child's type is the source and the parent's the target.
func eoSeedSmokeLinkType(db *gorm.DB, parentTypeID, childTypeID uuid.UUID) error {
	lt := models.EOLinkType{
		ID:                       uuid.New(),
		Key:                      eoSmokeLinkTypeKey,
		Label:                    "Parent directive",
		LabelAr:                  "التوجيه الأصل",
		SourceTypeIDs:            datatypes.JSON(`["` + childTypeID.String() + `"]`),
		TargetTypeIDs:            datatypes.JSON(`["` + parentTypeID.String() + `"]`),
		Cardinality:              models.EOCardinalityOne,
		DeriveDeadlineFromParent: true,
		DeadlineOffsetDays:       3,
		BlocksParentClosure:      true,
		CascadeSensitivity:       true,
	}
	if err := db.Create(&lt).Error; err != nil {
		return fmt.Errorf("create link type %q: %w", lt.Key, err)
	}
	return nil
}

// ── the smoke workflows ─────────────────────────────────────────────────────

// eoSeedSmokeWorkflow writes one two-state workflow (open -> closed) bound to a
// record type by RecordTypeID, which is the ONLY thing
// services.ResolveWorkflowForType matches on. RecordType is set to "eo" and
// IsDefault to false so the incident product's workflow queries — which match
// record_type against 'incident', 'both' and 'all' — never see it.
//
// The closing transition is restricted to the executive_office role, which is
// why this seed has to run after the role block of Seed.
func eoSeedSmokeWorkflow(db *gorm.DB, t *models.EORecordType, code, name string, policyID *uuid.UUID) error {
	wf := models.Workflow{
		ID:           uuid.New(),
		Name:         name,
		NameAr:       t.NameAr,
		Code:         code,
		Description:  "Smoke lifecycle for " + t.Name + ": Open -> Closed.",
		RecordType:   "eo",
		RecordTypeID: &t.ID,
		IsActive:     true,
		IsDefault:    false,
	}
	if err := db.Create(&wf).Error; err != nil {
		return fmt.Errorf("create workflow %q: %w", code, err)
	}

	open := models.WorkflowState{
		ID: uuid.New(), WorkflowID: wf.ID,
		Name: "Open", NameAr: "مفتوح", Code: "open",
		StateType: "initial", Color: "#3b82f6", SortOrder: 1,
		PositionX: 100, PositionY: 200, IsActive: true,
		// The SLA clock the WithSLA constructors start at creation. 24 hours
		// is short enough to be visibly configured and long enough never to
		// fire during a test run.
		SLAHours: eoSeedInt(24), SLAUnit: "hours",
		EscalationPolicyID: policyID,
	}
	closed := models.WorkflowState{
		ID: uuid.New(), WorkflowID: wf.ID,
		Name: "Closed", NameAr: "مغلق", Code: "closed",
		StateType: "terminal", Color: "#64748b", SortOrder: 2,
		PositionX: 400, PositionY: 200, IsActive: true,
	}
	for _, st := range []models.WorkflowState{open, closed} {
		if err := db.Create(&st).Error; err != nil {
			return fmt.Errorf("create state %q of workflow %q: %w", st.Code, code, err)
		}
	}

	// The role that may close. A MISSING role is a reason to stop, not a
	// reason to widen access: services.eoTransitionService.checkActor treats
	// an EMPTY AllowedRoles as "no restriction", so creating the transition
	// without the role would hand every authenticated user the power to close
	// a record in a statutory register — and, because the marker would commit
	// with it, that state would be permanent. Aborting instead rolls the whole
	// seed back, and the next boot (after the role block has run) retries.
	var eoRole models.Role
	if err := db.Where("code = ?", "executive_office").First(&eoRole).Error; err != nil {
		return fmt.Errorf("role executive_office is required by transition \"close\" of workflow %q and was not found; the EO seed must run after the role block of Seed: %w", code, err)
	}
	transition := models.WorkflowTransition{
		ID: uuid.New(), WorkflowID: wf.ID,
		Name: "Close", NameAr: "إغلاق", Code: "close",
		Description: "Close the record. An open child record blocks it.",
		FromStateID: open.ID, ToStateID: closed.ID,
		IsActive: true, SortOrder: 1,
		AllowedRoles: []models.Role{eoRole},
	}
	if err := db.Create(&transition).Error; err != nil {
		return fmt.Errorf("create transition %q of workflow %q: %w", transition.Code, code, err)
	}

	// One REQUIREMENT and one FIELD CHANGE, so the closing transition
	// exercises both halves of the executor that configuration drives:
	// services.eoCheckRequirements (a mandatory comment) and
	// applyFieldChanges (a declared key the actor may write, and only that
	// key). Without them the transition is a bare state move and those two
	// paths never run on any seeded configuration.
	mandatory := true
	requirement := models.TransitionRequirement{
		ID: uuid.New(), TransitionID: transition.ID,
		RequirementType: "comment",
		IsMandatory:     &mandatory,
		ErrorMessage:    "A closing note is required before this record can be closed.",
	}
	if err := db.Create(&requirement).Error; err != nil {
		return fmt.Errorf("create the comment requirement of transition %q of workflow %q: %w", transition.Code, code, err)
	}
	// change_reason is an INHERITED field (the field matrix's "Change
	// reason"), so declaring it here also proves a transition can drive a
	// field the concrete type never defines itself.
	fieldChange := models.TransitionFieldChange{
		ID: uuid.New(), TransitionID: transition.ID,
		FieldName: "change_reason", Label: "Reason for closing", LabelAr: "سبب الإغلاق",
		IsRequired: true, SortOrder: 1,
	}
	if err := db.Create(&fieldChange).Error; err != nil {
		return fmt.Errorf("create the field change of transition %q of workflow %q: %w", transition.Code, code, err)
	}

	// The type's own WorkflowID is kept in step with the binding, so the
	// designer shows the same workflow the engine resolves.
	if err := db.Model(&models.EORecordType{}).Where("id = ?", t.ID).Update("workflow_id", wf.ID).Error; err != nil {
		return fmt.Errorf("bind workflow %q to record type %q: %w", code, t.Key, err)
	}
	return nil
}

func eoSeedInt(v int) *int { return &v }

// eoSeedEscalationPolicy writes the one-step policy the smoke states' SLA
// points at, so the SLA clock the WithSLA constructors start has something to
// schedule. A failure here aborts the whole seed: a state whose SLA points at
// nothing starts no clock, and a register that silently stopped escalating is
// the kind of half-configuration the transaction exists to prevent.
func eoSeedEscalationPolicy(db *gorm.DB) (*uuid.UUID, error) {
	policy := models.EscalationPolicy{
		ID:          uuid.New(),
		Name:        eoSmokePolicyName,
		Description: "One escalation step at the SLA deadline, for the EO smoke workflows.",
		IsActive:    true,
	}
	if err := db.Create(&policy).Error; err != nil {
		return nil, fmt.Errorf("create escalation policy %q: %w", policy.Name, err)
	}
	step := models.EscalationPolicyStep{
		ID:         uuid.New(),
		PolicyID:   policy.ID,
		StepOrder:  1,
		DelayHours: 0,
		Channel:    "both",
	}
	if err := db.Create(&step).Error; err != nil {
		return nil, fmt.Errorf("create escalation step of %q: %w", policy.Name, err)
	}
	return &policy.ID, nil
}

// ── the per-type permission codes (spec §7.11) ──────────────────────────────

// eoTypePermissionLegacyInertEN and eoTypePermissionLegacyInertAR are the
// sentences this seed wrote through Plan 1 and Plan 2, when nothing consulted
// the per-type codes: "NOT YET ENFORCED… granting it records an intention and
// applies no control." They are NEVER WRITTEN any more. Task A2 wired the
// control and models.EOTypePermissionEnforcedEN/AR replaced them, because a
// box that says it enforces nothing while enforcing something is the same
// defect pointing the other way.
//
// THEY ARE KEPT SO A STALE ROW CAN BE RECOGNISED. A database seeded before
// A2 holds 33 rows carrying these sentences, and that text is the only mark
// that tells them apart from a description an administrator has deliberately
// edited — PUT /api/v1/admin/permissions/:id writes Description, so an edit
// is a legitimate state this seed must not revert (Plan 2's fix wave shipped
// exactly that regression, a convergent step undoing an administrator's act).
//
// So the refresh is NARROW: eoTypePermissionsPending opens the convergent
// step when a row still carries one of these, and eoSeedTypePermissions
// rewrites ONLY the COLUMN that carries one.
//
// PER COLUMN, NOT PER ROW, and that distinction is the whole correctness of
// this. PUT /api/v1/admin/permissions/:id writes `description` and NOTHING
// ELSE — there is no description_ar in that request (handlers.UpdatePermission).
// So the state an administrator's edit actually produces on a pre-A2 row is
// an edited ENGLISH description beside the legacy ARABIC one. A row-level
// predicate sees the stale Arabic, declares the row stale, and rewrites both
// columns — destroying the edit. That was reproduced, and the first attempt's
// tests missed it because both of them set description AND description_ar to
// the edited text, which is a state the API cannot produce: a test built on
// an unreachable input proves nothing about the reachable ones.
//
// The result is reachable (a stale column opens the gate), idempotent (after
// the rewrite no column carries the sentence and the gate shuts), and it
// cannot touch a column an administrator has edited, because an edited column
// no longer carries the sentence that selects it.
//
// Pinned by TestEoStaleInertDescriptionsAreRefreshedByTheRealSeed and
// TestEoSeedDoesNotRevertAnAdministratorsDescriptionEdit, both of which drive
// seedEOPilot rather than calling the writer directly: the GATE is the thing
// under test.
const (
	eoTypePermissionLegacyInertEN = "NOT YET ENFORCED: nothing in the platform consults this code, so granting it records an intention and applies no control."
	eoTypePermissionLegacyInertAR = "غير مُطبَّق بعد: لا شيء في المنصة يقرأ هذا الرمز، ومنحه يسجّل نيّة ولا يطبّق أي ضابط."
)

// eoLogTypesWithNoLiveCodes writes one line per concrete record type that has
// no LIVE per-type permission code, which makes it invisible to everybody but
// a super admin.
//
// It is a log and not a failure because the only way to reach it is an
// administrator deleting the codes, and the seed does not undo administrative
// acts — it does not resurrect a deleted permission any more than it
// reinstates a revoked grant. But an operator looking at a record type nobody
// can see has nothing else to go on: the type is there, the register is
// empty, and the reason is three deleted rows in another table.
func eoLogTypesWithNoLiveCodes(db *gorm.DB, concrete []models.EORecordType) {
	for _, t := range concrete {
		codes := make([]string, 0, len(models.EOTypePermissionActions))
		for _, a := range models.EOTypePermissionActions {
			codes = append(codes, models.EOTypePermissionCode(t.Key, a.Action))
		}
		var live int64
		if err := db.Model(&models.Permission{}).Where("code IN ?", codes).Count(&live).Error; err != nil {
			log.Printf("EO seed: could not check the per-type permission codes of %q: %v", t.Key, err)
			continue
		}
		if live == 0 {
			log.Printf("EO seed: record type %q has NO live per-type permission code (%s): only a super admin can see its records. The codes exist but are deleted; the seed does not resurrect a deleted permission.",
				t.Key, strings.Join(codes, ", "))
		}
	}
}

// eoTypePermissionENIsStale and eoTypePermissionARIsStale ask the question one
// COLUMN at a time, because that is the granularity an administrator's edit
// has: PUT /api/v1/admin/permissions/:id writes the English description only.
func eoTypePermissionENIsStale(p models.Permission) bool {
	return strings.Contains(p.Description, eoTypePermissionLegacyInertEN)
}

func eoTypePermissionARIsStale(p models.Permission) bool {
	return strings.Contains(p.DescriptionAr, eoTypePermissionLegacyInertAR)
}

// eoTypePermissionIsStale is the gate's question: is there anything here to
// rewrite? It is the OR of the two above and nothing else, so the step cannot
// open on a row the writer then declines to touch — which would make the seed
// transaction run on every boot for ever.
func eoTypePermissionIsStale(p models.Permission) bool {
	return eoTypePermissionENIsStale(p) || eoTypePermissionARIsStale(p)
}

// eoSeedTypePermissions writes eo:<type_key>:view|create|update for every
// CONCRETE record type and grants the lot to admin and executive_office.
//
// ENFORCED AS OF PLAN 3 TASK A2, where it was inert through Plans 1 and 2.
// Two surfaces consult these codes now: middleware.EOTypePermission refuses a
// record of a type the caller was not granted, and
// repository.applyEORecordTypePermissions restricts the register to the types
// whose view code the caller holds. The module-level codes (eo:view,
// eo:create, eo:update, eo:transition, eo:types:manage) still gate the API
// surface and row-level visibility is still the repository's other §7.11
// clause; the per-type codes NARROW both. Granting or revoking
// eo:<type_key>:view now changes what that role can see and do.
//
// They are seeded rather than created on demand because permissions are a
// seeded table (licensing/catalog_test.go asserts every module a catalogued
// feature names is seeded). A type an administrator creates at RUNTIME gets
// its codes from repository.eoRecordTypeRepository.Create, which writes them
// through the same models.EOTypePermissionsFor constructor — a type whose
// codes waited for the next boot would be invisible to everyone until then.
//
// The module-level eo:* codes seeded in postgres.go gate the API surface;
// these gate individual types. They are seeded rather than generated at
// runtime because permissions are a seeded table — licensing/catalog_test.go
// asserts that every module a catalogued feature names is seeded — and every
// one of them carries the literal Module: "eo" so licensing.FeatureForModule
// keeps resolving them to the eo_platform feature.
//
// It is here, and not in postgres.go's retro-fit block, purely because of
// ordering: that block runs while the roles are being created, and these codes
// do not exist until the types they name have been seeded, which must happen
// after the roles exist.
//
// IT IS CALLED TWICE: once from the platform seed and once from the pilot
// seed's seed_type_permissions step, because the eleven pilot types are
// created after the platform seed has already applied. The existence check
// below keys on Code, so the second call adds exactly the types seeded since
// the first. The ONE thing it changes about an existing row is a description
// column still carrying the pre-A2 inert sentence (see
// eoTypePermissionLegacyInertEN above): nothing else about a row that already
// exists is touched, and a column an administrator has edited is not, because
// the sentence is what selects it. A later rewording of the ENFORCED sentence
// would again not reach rows already in a customer's database — that is a
// known limit of keying the rewrite on the old text, and the honest price of
// not reverting administrators' edits. The grant is re-applied on every call
// and is safe: GORM's many-to-many Append writes the join rows ON CONFLICT DO
// NOTHING.

func eoSeedTypePermissions(db *gorm.DB) error {
	var concrete []models.EORecordType
	if err := db.Where("is_abstract = ?", false).Order("key").Find(&concrete).Error; err != nil {
		return fmt.Errorf("list concrete record types: %w", err)
	}

	var codes []string
	for _, t := range concrete {
		// The ROWS are built by models.EOTypePermissionsFor, which the type
		// designer also writes through (repository.Create). One constructor,
		// so a type created at runtime carries exactly what a seeded type
		// carries — including the code the gate asks for.
		for _, perm := range models.EOTypePermissionsFor(t) {
			// UNSCOPED, like the designer's checks in
			// repository.eoWriteTypePermissions and for the same reason:
			// permissions.code is uniquely indexed with NO deleted_at
			// predicate, so a SOFT-DELETED row still occupies its code. A
			// scoped read reports that code absent, the Create below violates
			// the unique index, and the WHOLE pilot seed transaction rolls
			// back — on every boot, taking every other pending convergent
			// step with it, because the gate below never shuts either. One
			// DELETE /api/v1/admin/permissions/:id is enough to put a
			// deployment into that state permanently.
			var stored models.Permission
			err := db.Unscoped().Where("code = ?", perm.Code).First(&stored).Error
			switch {
			case errors.Is(err, gorm.ErrRecordNotFound):
				if err := db.Create(&perm).Error; err != nil {
					return fmt.Errorf("create permission %q: %w", perm.Code, err)
				}
			case err != nil:
				return fmt.Errorf("check permission %q: %w", perm.Code, err)
			default:
				// A row written before Task A2 is still telling its
				// administrator that the code applies no control. Each
				// description column is rewritten ONLY if that column itself
				// carries the old sentence: an administrator who edited the
				// English keeps their English, and the Arabic beside it —
				// which their edit could not have touched — is brought up to
				// date.
				updates := map[string]any{}
				if eoTypePermissionENIsStale(stored) {
					updates["description"] = perm.Description
				}
				if eoTypePermissionARIsStale(stored) {
					updates["description_ar"] = perm.DescriptionAr
				}
				if len(updates) == 0 {
					break
				}
				if err := db.Model(&models.Permission{}).Where("id = ?", stored.ID).
					Updates(updates).Error; err != nil {
					return fmt.Errorf("refresh the description of permission %q: %w", perm.Code, err)
				}
			}
			codes = append(codes, perm.Code)
		}
	}
	if len(codes) == 0 {
		return nil
	}

	// The GRANT read is deliberately scoped, unlike the existence check
	// above: a deleted permission is not one to hand anybody. A type whose
	// codes an administrator deleted is left with no live code, which is
	// their act and fails closed — eoLogTypesWithNoLiveCodes says so in the
	// boot log, because nothing else would.
	var perms []models.Permission
	if err := db.Where("code IN ?", codes).Find(&perms).Error; err != nil {
		return fmt.Errorf("read back the per-type permissions: %w", err)
	}
	eoLogTypesWithNoLiveCodes(db, concrete)
	// A missing role aborts for the same reason the transition's does: a
	// permission code nobody holds is a type nobody can use, and the marker
	// would commit alongside it.
	for _, roleCode := range models.EOTypePermissionRoleCodes {
		var role models.Role
		if err := db.Where("code = ?", roleCode).First(&role).Error; err != nil {
			return fmt.Errorf("role %q is required to hold the per-type permission codes and was not found; the EO seed must run after the role block of Seed: %w", roleCode, err)
		}
		if err := db.Model(&role).Association("Permissions").Append(perms); err != nil {
			return fmt.Errorf("grant per-type permissions to role %q: %w", roleCode, err)
		}
	}
	return nil
}
