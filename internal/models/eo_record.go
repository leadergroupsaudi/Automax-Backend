package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// EORecord is one record of any configured type.
//
// Its table is NOT created by AutoMigrate and models.EORecord must never be
// added to that list: eo_records is LIST-partitioned by record_type_id, which
// GORM cannot express, so migrations.MigrateEOPlatform creates it with raw SQL.
// AutoMigrate would create a plain table first and that DDL would then fail
// against an existing relation.
//
// Postgres requires the partition key in the primary key and in every unique
// constraint, so the primary key is composite — (id, record_type_id) — and
// reference is unique per partition only. Global uniqueness of reference rests
// on ValidateReferencePrefix keeping type prefixes from overlapping (§7.7).
//
// No GORM associations are declared here on purpose. First(&rec, id), Preload
// and the association machinery assume a single-column primary key and
// misbehave with a composite one; the record repository queries with explicit
// Where clauses instead (plan decision 3).
//
// The 17 fields every type inherits are real columns, not values keys, because
// they are filtered, joined and sorted on every register screen (§7.7). The
// matrix's attachments field lives in Values.
type EORecord struct {
	ID           uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	RecordTypeID uuid.UUID `gorm:"type:uuid;primaryKey" json:"record_type_id"`

	Reference string `gorm:"size:100;not null" json:"reference"`
	SubjectAr string `gorm:"size:500;not null" json:"subject_ar"`
	// SubjectEn is the secondary display name and is nullable: the field
	// matrix marks subject_ar Required Yes and subject_en Required No.
	SubjectEn string `gorm:"size:500" json:"subject_en"`

	ParentRecordID *uuid.UUID `gorm:"type:uuid" json:"parent_record_id"`
	StateID        *uuid.UUID `gorm:"type:uuid" json:"state_id"`

	Priority string `gorm:"size:50;not null" json:"priority"`
	// Sensitivity is the field matrix's `classification`, renamed throughout
	// this design because Classification already means the incident taxonomy
	// in this codebase (§7.11). It is captured, displayed and reportable; it
	// does not gate access. Its options are configured on the field def.
	Sensitivity string `gorm:"size:50;not null" json:"sensitivity"`
	Source      string `gorm:"size:100;not null" json:"source"`

	OwningBodyID   *uuid.UUID `gorm:"type:uuid" json:"owning_body_id"`
	AssignedUserID *uuid.UUID `gorm:"type:uuid" json:"assigned_user_id"`

	DueDate  *time.Time `json:"due_date"`
	ClosedAt *time.Time `json:"closed_at"`

	// CreatedByID is a value type, never a pointer: every record has a
	// creator, so the column is NOT NULL.
	CreatedByID uuid.UUID `gorm:"type:uuid;not null" json:"created_by_id"`

	ChangeReason string `gorm:"type:text;not null" json:"change_reason"`
	DelayReason  string `gorm:"type:text;not null" json:"delay_reason"`

	// Values holds the type-specific fields, served by a GIN index.
	// "values" is a Postgres reserved word. GORM quotes it when it builds a
	// statement from this tag, but it will NOT quote it inside a raw fragment:
	// Where(`"values" @> ?`) and Order(`"values"->>'key'`) must quote it by hand.
	Values datatypes.JSON `gorm:"column:values;type:jsonb;not null;default:'{}'::jsonb" json:"values"`

	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

func (EORecord) TableName() string { return "eo_records" }

func (x *EORecord) BeforeCreate(tx *gorm.DB) error {
	if x.ID == uuid.Nil {
		x.ID = uuid.New()
	}
	return nil
}

// EORecordCreateRequest is the payload of the record create pipeline.
//
// Values is keyed by FIELD KEY and spans both kinds of storage: the inherited
// fields that are real columns (subject_ar, priority, due_date, …) and the
// type-specific tail that lives in the values jsonb. Which key goes where is
// the engine's business, not the caller's — a client fills in the form the
// field definitions describe and nothing else. Keys the engine owns
// (reference, state_id, created_by_id, the timestamps) are rejected rather
// than written.
type EORecordCreateRequest struct {
	RecordTypeID   uuid.UUID  `json:"record_type_id"`
	ParentRecordID *uuid.UUID `json:"parent_record_id"`
	// ParentLinkTypeKey names the link type tying this record to its parent.
	// Its rules — derive_deadline_from_parent, cascade_sensitivity — are
	// applied while saving, and the link row is written with it. Empty means
	// the hierarchy is recorded by parent_record_id alone, with no typed link
	// and therefore no link rules.
	ParentLinkTypeKey string         `json:"parent_link_type_key"`
	Values            map[string]any `json:"values"`
}

// EORecordUpdateRequest is a partial edit: keys absent from Values keep their
// stored value. RecordTypeID is required beside the record id because the
// primary key is composite.
type EORecordUpdateRequest struct {
	RecordTypeID uuid.UUID      `json:"record_type_id"`
	Values       map[string]any `json:"values"`
}

// EOTransitionInput is what an actor supplies when running a transition: the
// comment and attachments the transition's requirements ask for, and the
// values of the fields the transition declares as field changes.
//
// Values is keyed by FIELD KEY, like the create and update requests, and only
// keys the transition declares in its FieldChanges may appear: the transition
// modal is a form the configuration describes, not a second edit path around
// the field rules.
type EOTransitionInput struct {
	Comment string `json:"comment"`
	// Attachments satisfies an "attachment" transition requirement and is NOT
	// stored: spec §7.12 puts file handling in Documenta and the attachments
	// field lives in the values bag, so persistence belongs with that
	// integration. Until it lands, these ids are checked for presence and
	// dropped.
	Attachments []uuid.UUID    `json:"attachments"`
	Values      map[string]any `json:"values"`
}

// EORecordLink is one typed relationship between two records (spec §7.6).
// Both endpoints carry the record's type because a foreign key referencing a
// partitioned table must name the partition key; the link resolver reads the
// type from the row instead of a second lookup.
type EORecordLink struct {
	ID               uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	FromRecordID     uuid.UUID  `gorm:"type:uuid;not null" json:"from_record_id"`
	FromRecordTypeID uuid.UUID  `gorm:"type:uuid;not null" json:"from_record_type_id"`
	ToRecordID       uuid.UUID  `gorm:"type:uuid;not null" json:"to_record_id"`
	ToRecordTypeID   uuid.UUID  `gorm:"type:uuid;not null" json:"to_record_type_id"`
	LinkTypeKey      string     `gorm:"size:100;not null" json:"link_type_key"`
	CreatedByID      *uuid.UUID `gorm:"type:uuid" json:"created_by_id"`
	CreatedAt        time.Time  `json:"created_at"`
}

func (EORecordLink) TableName() string { return "eo_record_links" }

func (x *EORecordLink) BeforeCreate(tx *gorm.DB) error {
	if x.ID == uuid.Nil {
		x.ID = uuid.New()
	}
	return nil
}

// EORecordHistory is the audit trail of one record: transitions and the field
// changes that came with them. It carries the record's type for the same
// reason EORecordLink does.
type EORecordHistory struct {
	ID           uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	RecordID     uuid.UUID `gorm:"type:uuid;not null" json:"record_id"`
	RecordTypeID uuid.UUID `gorm:"type:uuid;not null" json:"record_type_id"`

	// TransitionID is nil for a change that was not a configured transition.
	TransitionID *uuid.UUID `gorm:"type:uuid" json:"transition_id"`
	FromStateID  *uuid.UUID `gorm:"type:uuid" json:"from_state_id"`
	ToStateID    *uuid.UUID `gorm:"type:uuid" json:"to_state_id"`

	// PerformedByID is nil for a system action (a timer, an escalation sweep).
	PerformedByID *uuid.UUID `gorm:"type:uuid" json:"performed_by_id"`

	Comment        string         `gorm:"type:text;not null" json:"comment"`
	OldValues      datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'::jsonb" json:"old_values"`
	NewValues      datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'::jsonb" json:"new_values"`
	IsSystemAction bool           `gorm:"not null;default:false" json:"is_system_action"`

	CreatedAt time.Time `json:"created_at"`
}

func (EORecordHistory) TableName() string { return "eo_record_history" }

func (x *EORecordHistory) BeforeCreate(tx *gorm.DB) error {
	if x.ID == uuid.Nil {
		x.ID = uuid.New()
	}
	return nil
}

// EORecordFilter selects and pages the records of one register (spec §7.7).
//
// The query-tagged fields are client-settable. The json:"-" fields are filled
// by the handler from the authenticated session. They carry query:"-" as well
// as json:"-": json:"-" stops body binding only, and Fiber's QueryParser falls
// back to the Go field name, so without it ?IsAdmin=true would switch
// row-level visibility off. A zero-value filter is the most
// restrictive one: IsAdmin false and an empty VisibleToDepartmentIDs hide every
// record that is restricted to a body (§7.11).
type EORecordFilter struct {
	// Search matches reference, subject_ar and subject_en, case-insensitively.
	Search       string   `query:"search" json:"search" validate:"omitempty,max=200"`
	RecordTypeID []string `query:"record_type_id" json:"record_type_id" validate:"omitempty,dive,uuid"`
	StateID      []string `query:"state_id" json:"state_id" validate:"omitempty,dive,uuid"`
	OwningBodyID []string `query:"owning_body_id" json:"owning_body_id" validate:"omitempty,dive,uuid"`
	AssignedUser []string `query:"assigned_user_id" json:"assigned_user_id" validate:"omitempty,dive,uuid"`
	ParentID     *string  `query:"parent_record_id" json:"parent_record_id" validate:"omitempty,uuid"`
	Priority     []string `query:"priority" json:"priority" validate:"omitempty,dive,max=50"`
	Sensitivity  []string `query:"sensitivity" json:"sensitivity" validate:"omitempty,dive,max=50"`
	Source       []string `query:"source" json:"source" validate:"omitempty,dive,max=50"`

	// Values filters on type-specific fields: every key must hold the given
	// value (jsonb containment, served by the GIN index). Request body only.
	Values map[string]any `query:"-" json:"values"`

	// The two periods a register is searched by. Request body only.
	CreatedFrom *time.Time `query:"-" json:"created_from"`
	CreatedTo   *time.Time `query:"-" json:"created_to"`
	DueFrom     *time.Time `query:"-" json:"due_from"`
	DueTo       *time.Time `query:"-" json:"due_to"`

	// SortBy is one of the repository's allow-listed keys; any other value is
	// rejected. SortDir is "asc" or "desc" (default desc).
	SortBy  string `query:"sort_by" json:"sort_by" validate:"omitempty,max=50"`
	SortDir string `query:"sort_dir" json:"sort_dir" validate:"omitempty,oneof=asc desc"`
	Page    int    `query:"page" json:"page" validate:"omitempty,min=1"`
	Limit   int    `query:"limit" json:"limit" validate:"omitempty,min=1,max=100"`

	// Set by the handler. VisibleToDepartmentIDs are the bodies the caller
	// belongs to; UserID is the caller, who always sees records they created;
	// IsAdmin bypasses row-level visibility.
	UserID                 uuid.UUID   `query:"-" json:"-"`
	IsAdmin                bool        `query:"-" json:"-"`
	VisibleToDepartmentIDs []uuid.UUID `query:"-" json:"-"`
}
