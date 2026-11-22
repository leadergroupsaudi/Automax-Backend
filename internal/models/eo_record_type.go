package models

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Field kinds (spec §7.3). `table` is deliberately not a kind: execution-plan
// stages are a child record type instead. `org unit` and `external body` both
// resolve to departments; the internal/external distinction lives in Config.
const (
	EOKindAutoNumber      = "auto_number"
	EOKindText            = "text"
	EOKindLongText        = "long_text"
	EOKindDropdown        = "dropdown"
	EOKindMultiSelect     = "multi_select"
	EOKindNumber          = "number"
	EOKindPercent         = "percent"
	EOKindYesNo           = "yes_no"
	EOKindDate            = "date"
	EOKindDateTime        = "date_time"
	EOKindTime            = "time"
	EOKindFile            = "file"
	EOKindMultiFile       = "multi_file"
	EOKindUser            = "user"
	EOKindMultiUser       = "multi_user"
	EOKindUserDate        = "user_date" // approval/signature primitive: one kind, not two fields
	EOKindOrgUnit         = "org_unit"
	EOKindMultiOrgUnit    = "multi_org_unit"
	EOKindExternalBody    = "external_body"
	EOKindEmail           = "email"
	EOKindPhone           = "phone"
	EOKindURL             = "url"
	EOKindLink            = "link"
	EOKindMultiLink       = "multi_link"
	EOKindWorkflowState   = "workflow_state"
	EOKindMultiUserStatus = "multi_user_status" // present | absent | excused per user
	EOKindMultiUserAck    = "multi_user_ack"    // agreed | reservation (+ reservation text) per user
)

// EOFieldKinds is the allow-list of field kinds, built from the constants
// above rather than from a second hand-written list of strings: renaming a
// kind's value changes this list with it, because each entry IS the constant.
//
// The remaining drift — declaring a new EOKind* constant and forgetting to add
// it here — is what TestEOFieldKindsCoversEveryKindConstant catches: it parses
// this file and fails if any EOKind* constant is missing from the slice.
//
// The designer validates `kind` against this (handlers.eoValidateFieldShape).
// It validates the NAME of the kind only. Per-kind validation of a stored
// VALUE at save time — that a `yes_no` holds a boolean, that a `dropdown`
// holds one of its configured options — is deliberately not here: that is Plan
// 2's work, alongside the forms that render each kind.
var EOFieldKinds = []string{
	EOKindAutoNumber,
	EOKindText,
	EOKindLongText,
	EOKindDropdown,
	EOKindMultiSelect,
	EOKindNumber,
	EOKindPercent,
	EOKindYesNo,
	EOKindDate,
	EOKindDateTime,
	EOKindTime,
	EOKindFile,
	EOKindMultiFile,
	EOKindUser,
	EOKindMultiUser,
	EOKindUserDate,
	EOKindOrgUnit,
	EOKindMultiOrgUnit,
	EOKindExternalBody,
	EOKindEmail,
	EOKindPhone,
	EOKindURL,
	EOKindLink,
	EOKindMultiLink,
	EOKindWorkflowState,
	EOKindMultiUserStatus,
	EOKindMultiUserAck,
}

// eoFieldKindSet is EOFieldKinds as a set, built once.
var eoFieldKindSet = func() map[string]bool {
	out := make(map[string]bool, len(EOFieldKinds))
	for _, k := range EOFieldKinds {
		out[k] = true
	}
	return out
}()

// IsEOFieldKind reports whether kind is one of the configured field kinds.
func IsEOFieldKind(kind string) bool { return eoFieldKindSet[kind] }

// Provenance: who or what sets a field's value (spec §7.4).
const (
	EOProvenanceUser    = "user"
	EOProvenanceAuto    = "auto"
	EOProvenanceDerived = "derived"
	EOProvenanceSystem  = "system"
)

// RequiredMode: when a field must have a value (spec §7.4).
const (
	EORequiredNever       = "never"
	EORequiredAlways      = "always"
	EORequiredConditional = "conditional"
	EORequiredAtState     = "at_state"
)

// ReferenceMode: whether a type numbers its own references or uses its parent's (spec §7.9).
const (
	EOReferenceModeOwn    = "own"
	EOReferenceModeParent = "parent"
)

// Link cardinality: how many targets a source record may link to via a link type.
const (
	EOCardinalityOne  = "one"
	EOCardinalityMany = "many"
)

// EORecordType describes a configurable record type.
type EORecordType struct {
	ID              uuid.UUID  `gorm:"type:uuid;primary_key" json:"id"`
	Key             string     `gorm:"size:100;not null;uniqueIndex:idx_eo_record_types_key,where:deleted_at IS NULL" json:"key"`
	Name            string     `gorm:"size:255;not null" json:"name"`
	NameAr          string     `gorm:"size:255" json:"name_ar"`
	Icon            string     `gorm:"size:100" json:"icon"`
	ParentTypeID    *uuid.UUID `gorm:"type:uuid;index" json:"parent_type_id"`
	IsAbstract      bool       `gorm:"default:false" json:"is_abstract"`
	ReferencePrefix string     `gorm:"size:50" json:"reference_prefix"`
	ReferenceMode   string     `gorm:"size:20;default:'own'" json:"reference_mode"`
	// ReferencePattern is the type's numbering pattern, in the grammar
	// services.RenderReference parses: an own-sequence pattern such as
	// EO-DIR-YYYY-NNN, or a parent-derived one such as EO-DIR-YYYY-NNN-A#
	// (spec §7.9). ReferencePrefix states which prefixes a type owns, and
	// ValidateReferencePrefix keeps those from overlapping; the pattern says
	// how a reference is rendered, which the prefix alone cannot (it carries
	// neither the sequence width nor a child's suffix letter). A type cannot
	// be numbered without one.
	ReferencePattern string         `gorm:"size:100" json:"reference_pattern"`
	WorkflowID       *uuid.UUID     `gorm:"type:uuid;index" json:"workflow_id"`
	Capabilities     datatypes.JSON `gorm:"type:jsonb;default:'[]'::jsonb" json:"capabilities"`
	IsActive         bool           `gorm:"default:true" json:"is_active"`
	// PartitionPending records that this type has no partition of its own and
	// its records live in eo_records_default (spec §7.7). In Plan 1 NOTHING
	// EVER CLEARS IT: no per-type partition is created, by the designer or by
	// any command, so every record of every type is in the default partition
	// and the platform is fully functional there. It is a LABEL — the designer
	// displays it — and nothing in the engine reads it.
	//
	// It is written true on every type the designer creates, and the column
	// defaults to true. The seed writes it FALSE for the abstract base type
	// eo_base, which holds no records and will never want a partition
	// (seed_eo_platform.go). So "always true" would be wrong as a claim about
	// the data, even though nothing ever flips a true to a false.
	//
	// Promotion is a ONE-WAY DOOR and it closes at the type's first record.
	// Creating a partition of a table that already has a DEFAULT partition
	// makes Postgres take an ACCESS EXCLUSIVE lock on that default and
	// FULL-SCAN it to prove no existing row would belong to the new partition.
	// That scan happens whether or not the type being promoted has records, and
	// here the default holds every record of every type — so promoting an EMPTY
	// type is the cheap case only RELATIVE to the other: a plain CREATE TABLE …
	// PARTITION OF, still scanning the whole register under lock. Promoting a
	// POPULATED one adds a data move on top (create detached, copy, delete,
	// attach), with the lock held across it. Neither is free on a populated
	// register. Plan 2 must therefore create each type's partition BEFORE that
	// type's first record, at type creation rather than at the next release.
	// Nothing here enforces that ordering. Recorded in spec §7.7 during the
	// Plan 1 fix wave.
	PartitionPending bool           `gorm:"default:true" json:"partition_pending"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
	DeletedAt        gorm.DeletedAt `gorm:"index" json:"-"`

	ParentType *EORecordType `gorm:"foreignKey:ParentTypeID" json:"parent_type,omitempty"`
	Workflow   *Workflow     `gorm:"foreignKey:WorkflowID" json:"workflow,omitempty"`
	Fields     []EOFieldDef  `gorm:"foreignKey:RecordTypeID" json:"fields,omitempty"`
}

func (EORecordType) TableName() string { return "eo_record_types" }

func (x *EORecordType) BeforeCreate(tx *gorm.DB) error {
	if x.ID == uuid.Nil {
		x.ID = uuid.New()
	}
	return nil
}

// EOFieldDef describes one field of a record type.
type EOFieldDef struct {
	ID                   uuid.UUID      `gorm:"type:uuid;primary_key" json:"id"`
	RecordTypeID         uuid.UUID      `gorm:"type:uuid;not null;uniqueIndex:idx_eo_field_defs_type_key,where:deleted_at IS NULL" json:"record_type_id"`
	Key                  string         `gorm:"size:100;not null;uniqueIndex:idx_eo_field_defs_type_key,where:deleted_at IS NULL" json:"key"`
	Label                string         `gorm:"size:255;not null" json:"label"`
	LabelAr              string         `gorm:"size:255" json:"label_ar"`
	Kind                 string         `gorm:"size:50;not null" json:"kind"`
	Config               datatypes.JSON `gorm:"type:jsonb;default:'{}'::jsonb" json:"config"`
	Provenance           string         `gorm:"size:20;default:'user'" json:"provenance"`
	RequiredMode         string         `gorm:"size:20;default:'never'" json:"required_mode"`
	RequiredRule         string         `gorm:"type:text" json:"required_rule"`
	RequiredAtStateIDs   datatypes.JSON `gorm:"type:jsonb;default:'[]'::jsonb" json:"required_at_state_ids"`
	Formula              string         `gorm:"type:text" json:"formula"`
	RecomputeOn          datatypes.JSON `gorm:"type:jsonb;default:'[]'::jsonb" json:"recompute_on"`
	IsIndexed            bool           `gorm:"default:false" json:"is_indexed"`
	IsLockedAfterStateID *uuid.UUID     `gorm:"type:uuid" json:"is_locked_after_state_id"`
	SortOrder            int            `gorm:"default:0" json:"sort_order"`
	HelpText             string         `gorm:"size:1000" json:"help_text"`
	HelpTextAr           string         `gorm:"size:1000" json:"help_text_ar"`
	CreatedAt            time.Time      `json:"created_at"`
	UpdatedAt            time.Time      `json:"updated_at"`
	DeletedAt            gorm.DeletedAt `gorm:"index" json:"-"`

	RecordType *EORecordType `gorm:"foreignKey:RecordTypeID" json:"-"`
}

func (EOFieldDef) TableName() string { return "eo_field_defs" }

func (x *EOFieldDef) BeforeCreate(tx *gorm.DB) error {
	if x.ID == uuid.Nil {
		x.ID = uuid.New()
	}
	return nil
}

// EOLinkType describes a typed relationship between records (spec §7.6).
// BlocksParentTransitionKey, when set, names the transition that is blocked
// instead of closure.
type EOLinkType struct {
	ID            uuid.UUID      `gorm:"type:uuid;primary_key" json:"id"`
	Key           string         `gorm:"size:100;not null;uniqueIndex:idx_eo_link_types_key,where:deleted_at IS NULL" json:"key"`
	Label         string         `gorm:"size:255;not null" json:"label"`
	LabelAr       string         `gorm:"size:255" json:"label_ar"`
	SourceTypeIDs datatypes.JSON `gorm:"type:jsonb;default:'[]'::jsonb" json:"source_type_ids"`
	TargetTypeIDs datatypes.JSON `gorm:"type:jsonb;default:'[]'::jsonb" json:"target_type_ids"`
	// Cardinality is EOCardinalityOne or EOCardinalityMany.
	Cardinality               string         `gorm:"size:50" json:"cardinality"`
	DeriveDeadlineFromParent  bool           `gorm:"default:false" json:"derive_deadline_from_parent"`
	DeadlineOffsetDays        int            `gorm:"default:0" json:"deadline_offset_days"`
	BlocksParentClosure       bool           `gorm:"default:false" json:"blocks_parent_closure"`
	BlocksParentTransitionKey string         `gorm:"size:100" json:"blocks_parent_transition_key"`
	CascadeSensitivity        bool           `gorm:"default:false" json:"cascade_sensitivity"`
	CreatedAt                 time.Time      `json:"created_at"`
	UpdatedAt                 time.Time      `json:"updated_at"`
	DeletedAt                 gorm.DeletedAt `gorm:"index" json:"-"`
}

func (EOLinkType) TableName() string { return "eo_link_types" }

func (x *EOLinkType) BeforeCreate(tx *gorm.DB) error {
	if x.ID == uuid.Nil {
		x.ID = uuid.New()
	}
	return nil
}

// ValidateReferencePrefix rejects a prefix that is blank, or that equals or is
// a prefix of (or is prefixed by) an existing type's prefix, case-insensitively.
// reference is unique only within a partition, so global uniqueness rests
// entirely on prefixes never overlapping. Types without a prefix are ignored.
//
// It takes the whole type, not just the string, because TWO kinds of type own
// no prefix at all and the rule must not be asked of them:
//
//   - an ABSTRACT type holds no records of its own, so it issues no references;
//   - a PARENT-DERIVED type (ReferenceMode "parent") issues every reference
//     inside its parent's — EO-SMOKE-2026-001-T1 under EO-SMOKE-2026-001 — so
//     a truthful prefix for it ALWAYS overlaps its parent's and a blank one
//     would be rejected outright. Global uniqueness still holds for it without
//     a prefix of its own: a child reference is its parent's plus a suffix the
//     parent's own sequence can never produce, and the parent's prefix is
//     already guarded here. Without this exemption every parent-derived type
//     has to carry a prefix that is a small lie about the references it issues
//     — and ValidateTypeEdit makes that lie permanent, because changing a
//     prefix is a destructive edit once records exist.
//
// On an update, the caller MUST exclude the type being edited from existing,
// otherwise a type that keeps its own prefix is rejected against itself.
// MESSAGES ARE ENGLISH-ONLY, against spec §5's bilingual requirement. The
// error returned here is passed to the client verbatim by the handler, so a
// refusal reaches an Arabic UI in English. Noted, not fixed, in the Plan 1 fix
// wave (2026-11-22): the fix is to return an i18n KEY plus its arguments
// instead of a sentence, and it belongs with Plan 2's forms, which is where
// the client-signed Arabic labels for each field and kind arrive. Writing
// Arabic wording the client has not approved now would only have to be
// revised then. See spec §5.
func ValidateReferencePrefix(t EORecordType, existing []EORecordType) error {
	if t.IsAbstract || t.ReferenceMode == EOReferenceModeParent {
		return nil
	}
	p := strings.ToUpper(strings.TrimSpace(t.ReferencePrefix))
	if p == "" {
		return fmt.Errorf("reference prefix is required")
	}
	for _, e := range existing {
		o := strings.ToUpper(strings.TrimSpace(e.ReferencePrefix))
		if o == "" {
			continue
		}
		if strings.HasPrefix(p, o) || strings.HasPrefix(o, p) {
			return fmt.Errorf("reference prefix %q overlaps %q used by record type %q", t.ReferencePrefix, e.ReferencePrefix, e.Key)
		}
	}
	return nil
}

// ValidateTypeEdit applies the guard rails of spec §7.14. Additive changes are
// always allowed. While records exist, removing a field, changing its kind,
// making an existing field required, changing the reference prefix or pattern,
// re-keying the type, repointing its workflow or its parent, or turning it
// abstract is rejected. It never deletes anything: a removed field's data
// stays in the values jsonb so a mistaken removal is recoverable. Fields are
// matched by Key.
//
// Every rule here is conditional on hasRecords. None of them is "never": a
// type nobody has used yet is still being designed and may be changed freely.
// MESSAGES ARE ENGLISH-ONLY, against spec §5's bilingual requirement. The
// error returned here is passed to the client verbatim by the handler, so a
// refusal reaches an Arabic UI in English. Noted, not fixed, in the Plan 1 fix
// wave (2026-11-22): the fix is to return an i18n KEY plus its arguments
// instead of a sentence, and it belongs with Plan 2's forms, which is where
// the client-signed Arabic labels for each field and kind arrive. Writing
// Arabic wording the client has not approved now would only have to be
// revised then. See spec §5.
func ValidateTypeEdit(before, after EORecordType, fieldsBefore, fieldsAfter []EOFieldDef, hasRecords bool) error {
	if !hasRecords {
		return nil
	}
	// A record's state_id names a state of the type's CURRENT workflow.
	// Repointing the workflow leaves every existing record in a state its
	// workflow does not contain: no transition runs from it, AvailableTransitions
	// returns nothing, and no API path can put the record back — in a register
	// that is a statutory audit trail. This is the worst of the five.
	if !eoSameUUID(before.WorkflowID, after.WorkflowID) {
		return fmt.Errorf("changing workflow from %s to %s is destructive: every existing record's state_id names a state of the old workflow, and no transition runs from a state the new workflow does not contain",
			eoUUIDText(before.WorkflowID), eoUUIDText(after.WorkflowID))
	}
	// Inherited fields are resolved at read time from the ancestor chain
	// (spec §7.2), so re-parenting changes which definitions a record's
	// stored values have. Values under a key the new chain does not define
	// stop being validated, displayed or derivable — they are still in the
	// jsonb, but nothing knows what they are.
	if !eoSameUUID(before.ParentTypeID, after.ParentTypeID) {
		return fmt.Errorf("changing parent type from %s to %s is destructive: effective fields are resolved from the ancestor chain at read time, so stored values can lose the definitions that give them meaning",
			eoUUIDText(before.ParentTypeID), eoUUIDText(after.ParentTypeID))
	}
	// The key identifies the type to configuration that names it by key —
	// the reference prefix it owns, formulas that address a child type by key
	// (eoRecordDeps.Children, .Ancestor), and seeded capability config.
	if before.Key != after.Key {
		return fmt.Errorf("changing key %q to %q is destructive: the key identifies this type to the reference prefix it owns and to every formula that names it",
			before.Key, after.Key)
	}
	// An abstract type holds no records of its own: the save pipeline refuses
	// to create one. Flipping the flag on a type that already has records
	// declares that those records cannot exist.
	if !before.IsAbstract && after.IsAbstract {
		return fmt.Errorf("making record type %q abstract is destructive: an abstract type holds no records of its own, and this one already has some",
			before.Key)
	}
	if before.ReferencePrefix != after.ReferencePrefix {
		return fmt.Errorf("changing reference prefix %q to %q is destructive: existing references were issued under the old prefix",
			before.ReferencePrefix, after.ReferencePrefix)
	}
	// The sequence scope is keyed on the pattern (services.EOSequenceScope),
	// so changing it restarts the counter at 1 under a new scope and re-issues
	// references that are already in use.
	if before.ReferencePattern != after.ReferencePattern {
		return fmt.Errorf("changing reference pattern %q to %q is destructive: the sequence counter is keyed on the pattern, so a new one restarts at 1 and re-issues references already in use",
			before.ReferencePattern, after.ReferencePattern)
	}
	next := make(map[string]EOFieldDef, len(fieldsAfter))
	for _, f := range fieldsAfter {
		next[f.Key] = f
	}
	for _, old := range fieldsBefore {
		cur, ok := next[old.Key]
		if !ok {
			return fmt.Errorf("removing field %q is destructive while records exist", old.Key)
		}
		if cur.Kind != old.Kind {
			return fmt.Errorf("changing kind of field %q from %q to %q is destructive while records exist", old.Key, old.Kind, cur.Kind)
		}
		if isNeverRequired(old.RequiredMode) && !isNeverRequired(cur.RequiredMode) {
			return fmt.Errorf("making field %q required is destructive while records exist", old.Key)
		}
	}
	return nil
}

func isNeverRequired(mode string) bool { return mode == "" || mode == EORequiredNever }

// eoSameUUID compares two optional ids, treating nil as its own value: nil to
// set and set to nil are both changes, and a == b on the pointers would
// compare addresses.
func eoSameUUID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// eoUUIDText renders an optional id for an error message.
func eoUUIDText(id *uuid.UUID) string {
	if id == nil {
		return "none"
	}
	return id.String()
}
