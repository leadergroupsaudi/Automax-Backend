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
	SubjectEn string `gorm:"size:500;not null" json:"subject_en"`

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

	CreatedByID uuid.UUID `gorm:"type:uuid" json:"created_by_id"`

	ChangeReason string `gorm:"type:text;not null" json:"change_reason"`
	DelayReason  string `gorm:"type:text;not null" json:"delay_reason"`

	// Values holds the type-specific fields, served by a GIN index.
	Values datatypes.JSON `gorm:"column:values;type:jsonb;not null;default:'{}'::jsonb" json:"values"`

	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `json:"-"`
}

func (EORecord) TableName() string { return "eo_records" }

func (x *EORecord) BeforeCreate(tx *gorm.DB) error {
	if x.ID == uuid.Nil {
		x.ID = uuid.New()
	}
	return nil
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
