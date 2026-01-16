package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Classification represents a hierarchical classification (e.g., Incident > Hardware > Laptop)
type Classification struct {
	ID          uuid.UUID      `gorm:"type:uuid;primary_key" json:"id"`
	Name        string         `gorm:"not null;size:100" json:"name"`
	Description string         `gorm:"size:500" json:"description"`
	ParentID    *uuid.UUID     `gorm:"type:uuid;index" json:"parent_id"`
	Parent      *Classification `gorm:"foreignKey:ParentID" json:"parent,omitempty"`
	Children    []Classification `gorm:"foreignKey:ParentID" json:"children,omitempty"`
	Level       int            `gorm:"default:0" json:"level"`
	Path        string         `gorm:"size:1000" json:"path"` // Materialized path for efficient queries
	IsActive    bool           `gorm:"default:true" json:"is_active"`
	SortOrder   int            `gorm:"default:0" json:"sort_order"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`
}

func (c *Classification) BeforeCreate(tx *gorm.DB) error {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	return nil
}

// ClassificationCreateRequest for creating a new classification
type ClassificationCreateRequest struct {
	Name        string     `json:"name" validate:"required,min=1,max=100"`
	Description string     `json:"description" validate:"max=500"`
	ParentID    *uuid.UUID `json:"parent_id"`
	SortOrder   int        `json:"sort_order"`
}

// ClassificationUpdateRequest for updating a classification
type ClassificationUpdateRequest struct {
	Name        string `json:"name" validate:"min=1,max=100"`
	Description string `json:"description" validate:"max=500"`
	IsActive    *bool  `json:"is_active"`
	SortOrder   *int   `json:"sort_order"`
}

// ClassificationResponse for API responses
type ClassificationResponse struct {
	ID          uuid.UUID                `json:"id"`
	Name        string                   `json:"name"`
	Description string                   `json:"description"`
	ParentID    *uuid.UUID               `json:"parent_id"`
	Level       int                      `json:"level"`
	Path        string                   `json:"path"`
	IsActive    bool                     `json:"is_active"`
	SortOrder   int                      `json:"sort_order"`
	Children    []ClassificationResponse `json:"children,omitempty"`
	CreatedAt   time.Time                `json:"created_at"`
}

func ToClassificationResponse(c *Classification) ClassificationResponse {
	resp := ClassificationResponse{
		ID:          c.ID,
		Name:        c.Name,
		Description: c.Description,
		ParentID:    c.ParentID,
		Level:       c.Level,
		Path:        c.Path,
		IsActive:    c.IsActive,
		SortOrder:   c.SortOrder,
		CreatedAt:   c.CreatedAt,
	}

	if len(c.Children) > 0 {
		resp.Children = make([]ClassificationResponse, len(c.Children))
		for i, child := range c.Children {
			resp.Children[i] = ToClassificationResponse(&child)
		}
	}

	return resp
}
