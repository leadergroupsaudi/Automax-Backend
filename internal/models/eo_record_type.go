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
// On an update, the caller MUST exclude the type being edited from existing,
// otherwise a type that keeps its own prefix is rejected against itself.
func ValidateReferencePrefix(prefix string, existing []EORecordType) error {
	p := strings.ToUpper(strings.TrimSpace(prefix))
	if p == "" {
		return fmt.Errorf("reference prefix is required")
	}
	for _, e := range existing {
		o := strings.ToUpper(strings.TrimSpace(e.ReferencePrefix))
		if o == "" {
			continue
		}
		if strings.HasPrefix(p, o) || strings.HasPrefix(o, p) {
			return fmt.Errorf("reference prefix %q overlaps %q used by record type %q", prefix, e.ReferencePrefix, e.Key)
		}
	}
	return nil
}

// ValidateTypeEdit applies the guard rails of spec §7.14. Additive changes are
// always allowed. While records exist, removing a field, changing its kind,
// making an existing field required, or changing the reference prefix is
// rejected. It never deletes anything: a removed field's data stays in the
// values jsonb so a mistaken removal is recoverable. Fields are matched by Key.
func ValidateTypeEdit(before, after EORecordType, fieldsBefore, fieldsAfter []EOFieldDef, hasRecords bool) error {
	if !hasRecords {
		return nil
	}
	if before.ReferencePrefix != after.ReferencePrefix {
		return fmt.Errorf("changing reference prefix %q to %q is destructive: existing references were issued under the old prefix",
			before.ReferencePrefix, after.ReferencePrefix)
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
