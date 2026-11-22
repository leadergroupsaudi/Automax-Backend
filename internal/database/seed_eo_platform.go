package database

import (
	"fmt"
	"log"

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
// it could not write. It runs on EVERY container boot, so the whole thing is
// behind ONE marker check — the presence of the base record type — rather
// than a per-row existence query across 17+ field definitions.
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

	base := eoSeedBaseType(db)
	if base == nil {
		return
	}
	policyID := eoSeedEscalationPolicy(db)

	parent := eoSeedSmokeParentType(db, base.ID)
	child := eoSeedSmokeChildType(db, base.ID)
	if parent == nil || child == nil {
		return
	}
	eoSeedSmokeLinkType(db, parent.ID, child.ID)
	eoSeedSmokeWorkflow(db, parent, "eo_smoke_directive_wf", "EO Smoke — Directive", policyID)
	eoSeedSmokeWorkflow(db, child, "eo_smoke_task_wf", "EO Smoke — Task", policyID)
	eoSeedTypePermissions(db)

	log.Println("EO platform base configuration seeded")
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
func eoSeedBaseType(db *gorm.DB) *models.EORecordType {
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
		log.Printf("EO platform seed: create record type %q: %v", base.Key, err)
		return nil
	}
	for _, f := range eoBaseFieldDefs(base.ID) {
		if err := db.Create(&f).Error; err != nil {
			log.Printf("EO platform seed: create field %q on %q: %v", f.Key, base.Key, err)
		}
	}
	return &base
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
func eoSeedSmokeParentType(db *gorm.DB, baseID uuid.UUID) *models.EORecordType {
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
		log.Printf("EO platform seed: create record type %q: %v", t.Key, err)
		return nil
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
		log.Printf("EO platform seed: create field %q on %q: %v", rollup.Key, t.Key, err)
	}
	return &t
}

// eoSeedSmokeChildType is numbered INSIDE its parent's reference: the pattern's
// last segment is a suffix letter and a sequence counted per parent, so the
// first task of EO-SMOKE-2026-001 is EO-SMOKE-2026-001-T1.
//
// ReferencePrefix is "EOT-SMOKE" and not "EO-SMOKE". A parent-derived type
// issues every reference inside its parent's prefix and owns no prefix of its
// own, but models.ValidateReferencePrefix rejects a blank prefix AND one that
// overlaps another type's, and models.ValidateTypeEdit calls a later change to
// the prefix destructive once records exist. A blank prefix here would make
// this type uneditable through the designer forever, so it carries a distinct,
// non-overlapping placeholder instead. See the report for the alternative.
func eoSeedSmokeChildType(db *gorm.DB, baseID uuid.UUID) *models.EORecordType {
	t := models.EORecordType{
		ID:               uuid.New(),
		Key:              eoSmokeChildTypeKey,
		Name:             "EO Smoke Task",
		NameAr:           "مهمة اختبارية",
		Icon:             "check-square",
		ParentTypeID:     &baseID,
		IsAbstract:       false,
		ReferencePrefix:  "EOT-SMOKE",
		ReferenceMode:    models.EOReferenceModeParent,
		ReferencePattern: "EO-SMOKE-YYYY-NNN-T#",
		Capabilities:     datatypes.JSON(`[]`),
		IsActive:         true,
		PartitionPending: true,
	}
	if err := db.Create(&t).Error; err != nil {
		log.Printf("EO platform seed: create record type %q: %v", t.Key, err)
		return nil
	}
	return &t
}

// eoSeedSmokeLinkType carries the three rules of spec §7.6 that a parent/child
// link can hold: a deadline counted back from the parent's, the parent's
// sensitivity label, and the block on closing a parent whose child is open.
//
// Direction: the save pipeline writes the link FROM the child TO the parent,
// so the child's type is the source and the parent's the target.
func eoSeedSmokeLinkType(db *gorm.DB, parentTypeID, childTypeID uuid.UUID) {
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
		log.Printf("EO platform seed: create link type %q: %v", lt.Key, err)
	}
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
func eoSeedSmokeWorkflow(db *gorm.DB, t *models.EORecordType, code, name string, policyID *uuid.UUID) {
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
		log.Printf("EO platform seed: create workflow %q: %v", code, err)
		return
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
	for _, s := range []models.WorkflowState{open, closed} {
		if err := db.Create(&s).Error; err != nil {
			log.Printf("EO platform seed: create state %q of workflow %q: %v", s.Code, code, err)
			return
		}
	}

	transition := models.WorkflowTransition{
		ID: uuid.New(), WorkflowID: wf.ID,
		Name: "Close", NameAr: "إغلاق", Code: "close",
		Description: "Close the record. An open child record blocks it.",
		FromStateID: open.ID, ToStateID: closed.ID,
		IsActive: true, SortOrder: 1,
	}
	var eoRole models.Role
	if err := db.Where("code = ?", "executive_office").First(&eoRole).Error; err == nil {
		transition.AllowedRoles = []models.Role{eoRole}
	} else {
		log.Printf("EO platform seed: role executive_office not found, transition %q of workflow %q is left open to every role: %v",
			transition.Code, code, err)
	}
	if err := db.Create(&transition).Error; err != nil {
		log.Printf("EO platform seed: create transition %q of workflow %q: %v", transition.Code, code, err)
		return
	}

	// The type's own WorkflowID is kept in step with the binding, so the
	// designer shows the same workflow the engine resolves.
	if err := db.Model(&models.EORecordType{}).Where("id = ?", t.ID).Update("workflow_id", wf.ID).Error; err != nil {
		log.Printf("EO platform seed: bind workflow %q to record type %q: %v", code, t.Key, err)
	}
}

func eoSeedInt(v int) *int { return &v }

// eoSeedEscalationPolicy writes the one-step policy the smoke states' SLA
// points at, so the SLA clock the WithSLA constructors start has something to
// schedule. A nil return means no policy, and the states are then seeded
// without one: a missing policy must not fail the seed.
func eoSeedEscalationPolicy(db *gorm.DB) *uuid.UUID {
	policy := models.EscalationPolicy{
		ID:          uuid.New(),
		Name:        eoSmokePolicyName,
		Description: "One escalation step at the SLA deadline, for the EO smoke workflows.",
		IsActive:    true,
	}
	if err := db.Create(&policy).Error; err != nil {
		log.Printf("EO platform seed: create escalation policy %q: %v", policy.Name, err)
		return nil
	}
	step := models.EscalationPolicyStep{
		ID:         uuid.New(),
		PolicyID:   policy.ID,
		StepOrder:  1,
		DelayHours: 0,
		Channel:    "both",
	}
	if err := db.Create(&step).Error; err != nil {
		log.Printf("EO platform seed: create escalation step of %q: %v", policy.Name, err)
		return nil
	}
	return &policy.ID
}

// ── the per-type permission codes (spec §7.11) ──────────────────────────────

// eoSeedTypePermissions writes eo:<type_key>:view|create|update for every
// CONCRETE record type and grants the lot to admin and executive_office.
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
func eoSeedTypePermissions(db *gorm.DB) {
	var concrete []models.EORecordType
	if err := db.Where("is_abstract = ?", false).Order("key").Find(&concrete).Error; err != nil {
		log.Printf("EO platform seed: list concrete record types: %v", err)
		return
	}

	actions := []struct{ action, verb, verbAr string }{
		{"view", "View", "عرض"},
		{"create", "Create", "إنشاء"},
		{"update", "Update", "تحديث"},
	}
	var codes []string
	for _, t := range concrete {
		for _, a := range actions {
			perm := models.Permission{
				ID:            uuid.New(),
				Name:          fmt.Sprintf("EO %s %s", a.verb, t.Name),
				Code:          fmt.Sprintf("eo:%s:%s", t.Key, a.action),
				Module:        "eo",
				Action:        fmt.Sprintf("%s:%s", t.Key, a.action),
				ActionAr:      a.verbAr,
				Description:   fmt.Sprintf("%s executive-office records of type %s", a.verb, t.Name),
				DescriptionAr: fmt.Sprintf("%s سجلات المكتب التنفيذي من نوع %s", a.verbAr, t.NameAr),
			}
			var existing int64
			if err := db.Model(&models.Permission{}).Where("code = ?", perm.Code).Count(&existing).Error; err != nil {
				log.Printf("EO platform seed: check permission %q: %v", perm.Code, err)
				continue
			}
			if existing == 0 {
				if err := db.Create(&perm).Error; err != nil {
					log.Printf("EO platform seed: create permission %q: %v", perm.Code, err)
					continue
				}
			}
			codes = append(codes, perm.Code)
		}
	}
	if len(codes) == 0 {
		return
	}

	var perms []models.Permission
	if err := db.Where("code IN ?", codes).Find(&perms).Error; err != nil {
		log.Printf("EO platform seed: read back the per-type permissions: %v", err)
		return
	}
	for _, roleCode := range []string{"admin", "executive_office"} {
		var role models.Role
		if err := db.Where("code = ?", roleCode).First(&role).Error; err != nil {
			log.Printf("EO platform seed: role %q not found, per-type permissions not granted to it: %v", roleCode, err)
			continue
		}
		if err := db.Model(&role).Association("Permissions").Append(perms); err != nil {
			log.Printf("EO platform seed: grant per-type permissions to role %q: %v", roleCode, err)
		}
	}
}
