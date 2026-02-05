package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// type NotificationTemplate1 struct {
// 	ID        uuid.UUID `gorm:"type:uuid;primaryKey"`
// 	Code      string    `gorm:"size:100;not null"`
// 	Channel   string    `gorm:"size:20;not null"` // email | sms
// 	Language  string    `gorm:"size:10;not null"` // en | ar
// 	Subject   string
// 	Body      string `gorm:"type:text;not null"`
// 	IsActive  bool   `gorm:"default:true"`
// 	CreatedAt time.Time
// 	UpdatedAt time.Time
// }

type NotificationTemplate struct {
	ID        uuid.UUID      `gorm:"type:uuid;primaryKey" json:"id"`
	Code      string         `gorm:"size:100;not null;index" json:"code"`    // INCIDENT_ASSIGNED
	Channel   string         `gorm:"size:20;not null;index" json:"channel"`  // email | sms
	Language  string         `gorm:"size:10;not null;index" json:"language"` // en | ar
	Subject   string         `gorm:"type:text" json:"subject,omitempty"`     // email only
	Body      string         `gorm:"type:text;not null" json:"body"`
	IsActive  bool           `gorm:"default:true" json:"is_active"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

func (t *NotificationTemplate) BeforeCreate(tx *gorm.DB) error {
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	return nil
}
