package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ActionLog struct {
	ID          uuid.UUID `gorm:"type:uuid;primary_key" json:"id"`
	UserID      uuid.UUID `gorm:"type:uuid;index;not null" json:"user_id"`
	User        *User     `gorm:"foreignKey:UserID" json:"user,omitempty"`
	Action      string    `gorm:"size:50;index;not null" json:"action"` // create, update, delete, login, logout, view
	Module      string    `gorm:"size:50;index;not null" json:"module"` // users, roles, departments, etc.
	ResourceID  string    `gorm:"size:36;index" json:"resource_id"`     // ID of the affected resource
	Description string    `gorm:"size:500" json:"description"`          // Human-readable description
	OldValue    string    `gorm:"type:text" json:"old_value,omitempty"` // JSON of old values
	NewValue    string    `gorm:"type:text" json:"new_value,omitempty"` // JSON of new values
	IPAddress   string    `gorm:"size:45" json:"ip_address"`            // Support IPv6
	UserAgent   string    `gorm:"size:500" json:"user_agent"`
	Status      string    `gorm:"size:20;default:'success'" json:"status"` // success, failed
	ErrorMsg    string    `gorm:"size:500" json:"error_msg,omitempty"`
	Duration    int64     `json:"duration"` // Request duration in milliseconds
	CreatedAt   time.Time `gorm:"index" json:"created_at"`
}

func (a *ActionLog) BeforeCreate(tx *gorm.DB) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	return nil
}

// ActionLogFilter holds filter parameters for querying action logs
type ActionLogFilter struct {
	UserID     *uuid.UUID `json:"user_id"`
	Action     string     `json:"action"`
	Module     string     `json:"module"`
	Status     string     `json:"status"`
	ResourceID string     `json:"resource_id"`
	StartDate  *time.Time `json:"start_date"`
	EndDate    *time.Time `json:"end_date"`
	Search     string     `json:"search"`
	Page       int        `json:"page"`
	Limit      int        `json:"limit"`
}

// ActionLogResponse is the response structure for action logs
type ActionLogResponse struct {
	ID          uuid.UUID     `json:"id"`
	UserID      uuid.UUID     `json:"user_id"`
	User        *UserResponse `json:"user,omitempty"`
	Action      string        `json:"action"`
	Module      string        `json:"module"`
	ResourceID  string        `json:"resource_id"`
	Description string        `json:"description"`
	OldValue    string        `json:"old_value,omitempty"`
	NewValue    string        `json:"new_value,omitempty"`
	IPAddress   string        `json:"ip_address"`
	UserAgent   string        `json:"user_agent"`
	Status      string        `json:"status"`
	ErrorMsg    string        `json:"error_msg,omitempty"`
	Duration    int64         `json:"duration"`
	CreatedAt   time.Time     `json:"created_at"`
}

// ActionLogStats holds statistics for action logs
type ActionLogStats struct {
	TotalActions    int64            `json:"total_actions"`
	TodayActions    int64            `json:"today_actions"`
	SuccessRate     float64          `json:"success_rate"`
	ActionsByModule map[string]int64 `json:"actions_by_module"`
	ActionsByType   map[string]int64 `json:"actions_by_type"`
}

func ToActionLogResponse(log *ActionLog) *ActionLogResponse {
	response := &ActionLogResponse{
		ID:          log.ID,
		UserID:      log.UserID,
		Action:      log.Action,
		Module:      log.Module,
		ResourceID:  log.ResourceID,
		Description: log.Description,
		OldValue:    log.OldValue,
		NewValue:    log.NewValue,
		IPAddress:   log.IPAddress,
		UserAgent:   log.UserAgent,
		Status:      log.Status,
		ErrorMsg:    log.ErrorMsg,
		Duration:    log.Duration,
		CreatedAt:   log.CreatedAt,
	}

	if log.User != nil {
		userResp := ToUserResponse(log.User)
		response.User = &userResp
	}

	return response
}
