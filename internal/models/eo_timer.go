package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// Timer status values (spec §7.10). A timer is claimed by exactly one worker
// via SELECT … FOR UPDATE SKIP LOCKED, then fired or cancelled.
const (
	EOTimerPending   = "pending"
	EOTimerClaimed   = "claimed"
	EOTimerFired     = "fired"
	EOTimerCancelled = "cancelled"
)

// EOTimer is a durable, record-scoped timer: an SLA clock, a reminder, a
// deferred capability call.
//
// The struct is declared here, with the record models, because the capability
// interface returns []EOTimer. Its table, repository and worker are built in
// Task 17 — eo_timers is deliberately NOT created by MigrateEOPlatform yet and
// EOTimer is not in the AutoMigrate list.
type EOTimer struct {
	ID            uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	RecordID      uuid.UUID      `gorm:"type:uuid;not null" json:"record_id"`
	RecordTypeID  uuid.UUID      `gorm:"type:uuid;not null" json:"record_type_id"`
	FireAt        time.Time      `gorm:"not null" json:"fire_at"`
	CapabilityKey string         `gorm:"size:100;not null" json:"capability_key"`
	Payload       datatypes.JSON `gorm:"type:jsonb;not null;default:'{}'::jsonb" json:"payload"`
	Status        string         `gorm:"size:20;not null;default:'pending'" json:"status"`
	ClaimedAt     *time.Time     `json:"claimed_at"`
	CreatedAt     time.Time      `json:"created_at"`
}

func (EOTimer) TableName() string { return "eo_timers" }

func (x *EOTimer) BeforeCreate(tx *gorm.DB) error {
	if x.ID == uuid.Nil {
		x.ID = uuid.New()
	}
	return nil
}

// EOEscalationEvent is one breached SLA step on one record.
//
// EO keeps its own breach log: the live one (escalation_sla.go) is typed on
// IncidentID with an *Incident foreign key, and making it polymorphic would
// change a table the incident product reads today (spec §7.8).
//
// As with EOTimer, this is the struct only — the table and the sweeper are
// Task 18.
type EOEscalationEvent struct {
	ID           uuid.UUID  `gorm:"type:uuid;primaryKey" json:"id"`
	RecordID     uuid.UUID  `gorm:"type:uuid;not null" json:"record_id"`
	RecordTypeID uuid.UUID  `gorm:"type:uuid;not null" json:"record_type_id"`
	StateID      uuid.UUID  `gorm:"type:uuid;not null" json:"state_id"`
	PolicyID     uuid.UUID  `gorm:"type:uuid;not null" json:"policy_id"`
	StepIndex    int        `gorm:"not null" json:"step_index"`
	BreachedAt   time.Time  `gorm:"not null" json:"breached_at"`
	NotifiedAt   *time.Time `json:"notified_at"`
	CreatedAt    time.Time  `json:"created_at"`
}

func (EOEscalationEvent) TableName() string { return "eo_escalation_events" }

func (x *EOEscalationEvent) BeforeCreate(tx *gorm.DB) error {
	if x.ID == uuid.Nil {
		x.ID = uuid.New()
	}
	return nil
}
