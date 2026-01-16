package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Location represents a hierarchical location (e.g., Country > State > City > Building > Floor)
type Location struct {
	ID          uuid.UUID      `gorm:"type:uuid;primary_key" json:"id"`
	Name        string         `gorm:"not null;size:100" json:"name"`
	Code        string         `gorm:"size:50;index" json:"code"` // Short code like "US", "NY", "NYC-HQ"
	Description string         `gorm:"size:500" json:"description"`
	Type        string         `gorm:"size:50" json:"type"` // country, state, city, building, floor, room
	ParentID    *uuid.UUID     `gorm:"type:uuid;index" json:"parent_id"`
	Parent      *Location      `gorm:"foreignKey:ParentID" json:"parent,omitempty"`
	Children    []Location     `gorm:"foreignKey:ParentID" json:"children,omitempty"`
	Level       int            `gorm:"default:0" json:"level"`
	Path        string         `gorm:"size:1000" json:"path"`
	Address     string         `gorm:"size:500" json:"address"`
	Latitude    *float64       `gorm:"type:decimal(10,8)" json:"latitude"`
	Longitude   *float64       `gorm:"type:decimal(11,8)" json:"longitude"`
	IsActive    bool           `gorm:"default:true" json:"is_active"`
	SortOrder   int            `gorm:"default:0" json:"sort_order"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`
}

func (l *Location) BeforeCreate(tx *gorm.DB) error {
	if l.ID == uuid.Nil {
		l.ID = uuid.New()
	}
	return nil
}

// LocationCreateRequest for creating a new location
type LocationCreateRequest struct {
	Name        string     `json:"name" validate:"required,min=1,max=100"`
	Code        string     `json:"code" validate:"max=50"`
	Description string     `json:"description" validate:"max=500"`
	Type        string     `json:"type" validate:"max=50"`
	ParentID    *uuid.UUID `json:"parent_id"`
	Address     string     `json:"address" validate:"max=500"`
	Latitude    *float64   `json:"latitude" validate:"omitempty,min=-90,max=90"`
	Longitude   *float64   `json:"longitude" validate:"omitempty,min=-180,max=180"`
	SortOrder   int        `json:"sort_order"`
}

// LocationUpdateRequest for updating a location
type LocationUpdateRequest struct {
	Name        string   `json:"name" validate:"min=1,max=100"`
	Code        string   `json:"code" validate:"max=50"`
	Description string   `json:"description" validate:"max=500"`
	Type        string   `json:"type" validate:"max=50"`
	Address     string   `json:"address" validate:"max=500"`
	Latitude    *float64 `json:"latitude" validate:"omitempty,min=-90,max=90"`
	Longitude   *float64 `json:"longitude" validate:"omitempty,min=-180,max=180"`
	IsActive    *bool    `json:"is_active"`
	SortOrder   *int     `json:"sort_order"`
}

// LocationResponse for API responses
type LocationResponse struct {
	ID          uuid.UUID          `json:"id"`
	Name        string             `json:"name"`
	Code        string             `json:"code"`
	Description string             `json:"description"`
	Type        string             `json:"type"`
	ParentID    *uuid.UUID         `json:"parent_id"`
	Level       int                `json:"level"`
	Path        string             `json:"path"`
	Address     string             `json:"address"`
	Latitude    *float64           `json:"latitude,omitempty"`
	Longitude   *float64           `json:"longitude,omitempty"`
	IsActive    bool               `json:"is_active"`
	SortOrder   int                `json:"sort_order"`
	Children    []LocationResponse `json:"children,omitempty"`
	CreatedAt   time.Time          `json:"created_at"`
}

func ToLocationResponse(l *Location) LocationResponse {
	resp := LocationResponse{
		ID:          l.ID,
		Name:        l.Name,
		Code:        l.Code,
		Description: l.Description,
		Type:        l.Type,
		ParentID:    l.ParentID,
		Level:       l.Level,
		Path:        l.Path,
		Address:     l.Address,
		Latitude:    l.Latitude,
		Longitude:   l.Longitude,
		IsActive:    l.IsActive,
		SortOrder:   l.SortOrder,
		CreatedAt:   l.CreatedAt,
	}

	if len(l.Children) > 0 {
		resp.Children = make([]LocationResponse, len(l.Children))
		for i, child := range l.Children {
			resp.Children[i] = ToLocationResponse(&child)
		}
	}

	return resp
}
