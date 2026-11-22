package models

import "time"

// EOSeedStep records that one step of the EO pilot seed has been applied. The
// row is written in the same transaction as the step's work, so "applied" is a
// durable fact, never an inference from database content. See
// internal/database/seed_eo_pilot.go (eoPilotStep).
type EOSeedStep struct {
	Key       string    `gorm:"size:100;primaryKey" json:"key"`
	AppliedAt time.Time `gorm:"not null;default:now()" json:"applied_at"`
}

func (EOSeedStep) TableName() string { return "eo_seed_steps" }
