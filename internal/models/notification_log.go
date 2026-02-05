package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// type NotificationLog struct {
// 	ID           uuid.UUID `gorm:"type:uuid;primaryKey"`
// 	Channel      string
// 	TemplateCode string
// 	Language     string
// 	Recipient    string
// 	Subject      string
// 	Body         string `gorm:"type:text"`
// 	Status       string // sent | failed | mock-sent
// 	Provider     string // smtp | twilio | mock
// 	ErrorMessage string
// 	CreatedAt    time.Time
// }

type NotificationLog struct {
	ID           uuid.UUID `gorm:"type:uuid;primaryKey" json:"id"`
	Channel      string    `gorm:"size:20;not null" json:"channel"`
	TemplateCode string    `gorm:"size:100" json:"template_code"`
	Language     string    `gorm:"size:10" json:"language"`
	Recipient    string    `gorm:"size:255;not null" json:"recipient"`
	Subject      string    `gorm:"type:text" json:"subject,omitempty"`
	Body         string    `gorm:"type:text;not null" json:"body"`
	Status       string    `gorm:"size:20;not null" json:"status"` // sent | failed | mock-sent
	Provider     string    `gorm:"size:50" json:"provider"`        // smtp | twilio | mock
	ErrorMessage string    `gorm:"type:text" json:"error_message,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

func (l *NotificationLog) BeforeCreate(tx *gorm.DB) error {
	if l.ID == uuid.Nil {
		l.ID = uuid.New()
	}
	return nil
}
