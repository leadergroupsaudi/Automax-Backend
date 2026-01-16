package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Permission represents a granular permission for accessing features
type Permission struct {
	ID          uuid.UUID      `gorm:"type:uuid;primary_key" json:"id"`
	Name        string         `gorm:"not null;size:100;uniqueIndex" json:"name"`
	Code        string         `gorm:"not null;size:100;uniqueIndex" json:"code"` // e.g., "users.create", "tickets.view"
	Description string         `gorm:"size:500" json:"description"`
	Module      string         `gorm:"size:50;index" json:"module"` // e.g., "users", "tickets", "reports"
	Action      string         `gorm:"size:50" json:"action"`       // e.g., "create", "read", "update", "delete"
	IsActive    bool           `gorm:"default:true" json:"is_active"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`
}

func (p *Permission) BeforeCreate(tx *gorm.DB) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	return nil
}

// Role represents a user role with associated permissions
type Role struct {
	ID          uuid.UUID      `gorm:"type:uuid;primary_key" json:"id"`
	Name        string         `gorm:"not null;size:100;uniqueIndex" json:"name"`
	Code        string         `gorm:"not null;size:50;uniqueIndex" json:"code"`
	Description string         `gorm:"size:500" json:"description"`
	IsSystem    bool           `gorm:"default:false" json:"is_system"` // System roles cannot be deleted
	IsActive    bool           `gorm:"default:true" json:"is_active"`
	Permissions []Permission   `gorm:"many2many:role_permissions;" json:"permissions,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`
}

func (r *Role) BeforeCreate(tx *gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	return nil
}

// PermissionCreateRequest for creating a new permission
type PermissionCreateRequest struct {
	Name        string `json:"name" validate:"required,min=1,max=100"`
	Code        string `json:"code" validate:"required,min=1,max=100"`
	Description string `json:"description" validate:"max=500"`
	Module      string `json:"module" validate:"required,max=50"`
	Action      string `json:"action" validate:"required,max=50"`
}

// PermissionUpdateRequest for updating a permission
type PermissionUpdateRequest struct {
	Name        string `json:"name" validate:"min=1,max=100"`
	Description string `json:"description" validate:"max=500"`
	IsActive    *bool  `json:"is_active"`
}

// PermissionResponse for API responses
type PermissionResponse struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Code        string    `json:"code"`
	Description string    `json:"description"`
	Module      string    `json:"module"`
	Action      string    `json:"action"`
	IsActive    bool      `json:"is_active"`
	CreatedAt   time.Time `json:"created_at"`
}

func ToPermissionResponse(p *Permission) PermissionResponse {
	return PermissionResponse{
		ID:          p.ID,
		Name:        p.Name,
		Code:        p.Code,
		Description: p.Description,
		Module:      p.Module,
		Action:      p.Action,
		IsActive:    p.IsActive,
		CreatedAt:   p.CreatedAt,
	}
}

// RoleCreateRequest for creating a new role
type RoleCreateRequest struct {
	Name          string      `json:"name" validate:"required,min=1,max=100"`
	Code          string      `json:"code" validate:"required,min=1,max=50"`
	Description   string      `json:"description" validate:"max=500"`
	PermissionIDs []uuid.UUID `json:"permission_ids"`
}

// RoleUpdateRequest for updating a role
type RoleUpdateRequest struct {
	Name          string      `json:"name" validate:"min=1,max=100"`
	Description   string      `json:"description" validate:"max=500"`
	PermissionIDs []uuid.UUID `json:"permission_ids"`
	IsActive      *bool       `json:"is_active"`
}

// RoleResponse for API responses
type RoleResponse struct {
	ID          uuid.UUID            `json:"id"`
	Name        string               `json:"name"`
	Code        string               `json:"code"`
	Description string               `json:"description"`
	IsSystem    bool                 `json:"is_system"`
	IsActive    bool                 `json:"is_active"`
	Permissions []PermissionResponse `json:"permissions,omitempty"`
	CreatedAt   time.Time            `json:"created_at"`
}

func ToRoleResponse(r *Role) RoleResponse {
	resp := RoleResponse{
		ID:          r.ID,
		Name:        r.Name,
		Code:        r.Code,
		Description: r.Description,
		IsSystem:    r.IsSystem,
		IsActive:    r.IsActive,
		CreatedAt:   r.CreatedAt,
	}

	if len(r.Permissions) > 0 {
		resp.Permissions = make([]PermissionResponse, len(r.Permissions))
		for i, perm := range r.Permissions {
			resp.Permissions[i] = ToPermissionResponse(&perm)
		}
	}

	return resp
}

// UserRole represents the many-to-many relationship between users and roles
type UserRole struct {
	UserID    uuid.UUID `gorm:"type:uuid;primaryKey" json:"user_id"`
	RoleID    uuid.UUID `gorm:"type:uuid;primaryKey" json:"role_id"`
	AssignedAt time.Time `gorm:"autoCreateTime" json:"assigned_at"`
	AssignedBy *uuid.UUID `gorm:"type:uuid" json:"assigned_by"`
}
