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
	// EOTimerFailed is terminal: the handler failed EOTimerMaxAttempts times
	// (or the worker died that often mid-timer) and the timer was given up on.
	// It never fired; last_error says why.
	EOTimerFailed = "failed"
)

// EOTimer is a durable, record-scoped timer: an SLA clock, a reminder, a
// deferred capability call.
//
// The struct is declared here, with the record models, because the capability
// interface returns []EOTimer. The table is created by MigrateEOPlatform and
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
	Attempts      int            `gorm:"not null;default:0" json:"attempts"`
	LastError     string         `gorm:"type:text;not null;default:''" json:"last_error"`
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
// StepIndex is the policy step's StepOrder, which is stable when a policy is
// edited, unlike a position in the list. BreachedAt is when the step was due
// (its timer's fire_at), not when the handler happened to run.
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
