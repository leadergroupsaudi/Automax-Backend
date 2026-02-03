package models

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// UUIDArray represents an array of UUIDs for PostgreSQL
type UUIDArray []uuid.UUID

// Value implements the driver.Valuer interface
func (u UUIDArray) Value() (driver.Value, error) {
	return json.Marshal(u)
}

// Scan implements the sql.Scanner interface
func (u *UUIDArray) Scan(value interface{}) error {
	if value == nil {
		*u = nil
		return nil
	}
	
	var bytes []byte
	switch v := value.(type) {
	case []byte:
		bytes = v
	case string:
		bytes = []byte(v)
	default:
		return fmt.Errorf("unsupported type: %T", value)
	}
	
	return json.Unmarshal(bytes, u)
}

// MarshalJSON implements json.Marshaler
func (u UUIDArray) MarshalJSON() ([]byte, error) {
	return json.Marshal([]uuid.UUID(u))
}

// UnmarshalJSON implements json.Unmarshaler
func (u *UUIDArray) UnmarshalJSON(data []byte) error {
	var uuids []uuid.UUID
	if err := json.Unmarshal(data, &uuids); err != nil {
		return err
	}
	*u = UUIDArray(uuids)
	return nil
}

type CallLog struct {
	ID           uuid.UUID      `gorm:"type:uuid;primary_key" json:"id"`
	CallUuid     string         `gorm:"size:36;uniqueIndex" json:"call_uuid,omitempty"`
	CreatedBy    uuid.UUID      `gorm:"type:uuid;index;not null" json:"created_by"`
	Creator      *User          `gorm:"foreignKey:CreatedBy" json:"creator,omitempty"`
	StartAt      *time.Time     `json:"start_at,omitempty"`
	EndAt        *time.Time     `json:"end_at,omitempty"`
	Status       string         `gorm:"size:20;not null" json:"status"`
	Participants UUIDArray      `gorm:"type:text" json:"participants,omitempty"`
	JoinedUsers  UUIDArray      `gorm:"type:text" json:"joined_users,omitempty"`
	InvitedUsers UUIDArray      `gorm:"type:text" json:"invited_users,omitempty"`
	RecordingUrl string         `gorm:"size:500" json:"recording_url,omitempty"`
	Meta         string         `gorm:"type:json" json:"meta,omitempty"` // JSON string for metadata
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    *time.Time     `json:"updated_at,omitempty"`
	DeletedAt    gorm.DeletedAt `gorm:"index" json:"-"`
}

func (c *CallLog) BeforeCreate(tx *gorm.DB) error {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	return nil
}

type CallLogMeta struct {
	Duration   int                    `json:"duration,omitempty"`  // Duration in seconds
	CallType   string                 `json:"call_type,omitempty"` // audio, video, etc.
	Platform   string                 `json:"platform,omitempty"`  // web, mobile, etc.
	DeviceInfo map[string]interface{} `json:"device_info,omitempty"`
	Quality    string                 `json:"quality,omitempty"` // hd, sd, etc.
	Notes      string                 `json:"notes,omitempty"`
}

// CallLogCreateRequest for creating a new call log
type CallLogCreateRequest struct {
	CallUuid     string      `json:"call_uuid,omitempty" validate:"omitempty,max=36"`
	StartAt      *time.Time  `json:"start_at,omitempty"`
	EndAt        *time.Time  `json:"end_at,omitempty"`
	Status       string      `json:"status" validate:"required,max=20"`
	Participants UUIDArray   `json:"participants,omitempty"`
	InvitedUsers UUIDArray   `json:"invited_users,omitempty"`
	RecordingUrl string      `json:"recording_url,omitempty" validate:"omitempty,max=500"`
	Meta         string      `json:"meta,omitempty"`
}

// CallLogUpdateRequest for updating a call log
type CallLogUpdateRequest struct {
	StartAt      *time.Time  `json:"start_at,omitempty"`
	EndAt        *time.Time  `json:"end_at,omitempty"`
	Status       string      `json:"status,omitempty" validate:"omitempty,max=20"`
	Participants UUIDArray   `json:"participants,omitempty"`
	JoinedUsers  UUIDArray   `json:"joined_users,omitempty"`
	InvitedUsers UUIDArray   `json:"invited_users,omitempty"`
	RecordingUrl string      `json:"recording_url,omitempty" validate:"omitempty,max=500"`
	Meta         string      `json:"meta,omitempty"`
}

// CallLogResponse for API responses
type CallLogResponse struct {
	ID           uuid.UUID      `json:"id"`
	CallUuid     string         `json:"call_uuid,omitempty"`
	CreatedBy    uuid.UUID      `json:"created_by"`
	Creator      *UserResponse  `json:"creator,omitempty"`
	StartAt      *time.Time     `json:"start_at,omitempty"`
	EndAt        *time.Time     `json:"end_at,omitempty"`
	Status       string         `json:"status"`
	Participants []UserResponse `json:"participants,omitempty"`
	JoinedUsers  []UserResponse `json:"joined_users,omitempty"`
	InvitedUsers []UserResponse `json:"invited_users,omitempty"`
	RecordingUrl string         `json:"recording_url,omitempty"`
	Meta         string         `json:"meta,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    *time.Time     `json:"updated_at,omitempty"`
}

// CallLogFilter for filtering call logs
type CallLogFilter struct {
	CreatedBy *uuid.UUID `json:"created_by"`
	Status    string     `json:"status"`
	StartDate *time.Time `json:"start_date"`
	EndDate   *time.Time `json:"end_date"`
	Search    string     `json:"search"`
	Page      int        `json:"page"`
	Limit     int        `json:"limit"`
}

// CallLogStats represents statistics for call logs
type CallLogStats struct {
	TotalCalls    int64            `json:"total_calls"`
	RecentCalls   int64            `json:"recent_calls"`
	CallsByStatus map[string]int64 `json:"calls_by_status"`
}

func ToCallLogResponse(callLog *CallLog, userRepo interface{}) CallLogResponse {
	resp := CallLogResponse{
		ID:           callLog.ID,
		CallUuid:     callLog.CallUuid,
		CreatedBy:    callLog.CreatedBy,
		StartAt:      callLog.StartAt,
		EndAt:        callLog.EndAt,
		Status:       callLog.Status,
		RecordingUrl: callLog.RecordingUrl,
		Meta:         callLog.Meta,
		CreatedAt:    callLog.CreatedAt,
		UpdatedAt:    callLog.UpdatedAt,
	}

	if callLog.Creator != nil {
		creatorResp := ToUserResponse(callLog.Creator)
		resp.Creator = &creatorResp
	}

	// Note: For participants, joined_users, and invited_users,
	// you would need to fetch the users separately and populate them
	// This is a simplified version - in a real implementation,
	// you'd want to preload or fetch these users

	return resp
}
