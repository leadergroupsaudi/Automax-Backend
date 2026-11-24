package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Permission represents a granular permission for accessing features
type Permission struct {
	ID          uuid.UUID `gorm:"type:uuid;primary_key" json:"id"`
	Name        string    `gorm:"not null;size:100;uniqueIndex:idx_permissions_name,where:deleted_at IS NULL" json:"name"`
	Code        string    `gorm:"not null;size:100;uniqueIndex:idx_permissions_code,where:deleted_at IS NULL" json:"code"` // e.g., "users.create", "tickets.view"
	Description string    `gorm:"size:500" json:"description"`
	Module      string    `gorm:"size:50;index" json:"module"` // e.g., "users", "tickets", "reports"
	Action      string    `gorm:"size:50" json:"action"`       // e.g., "create", "read", "update", "delete"
	ActionAr    string    `gorm:"size:50" json:"action_ar"`    // Arabic translation of Action
	// Arabic counterparts of Name/Description/Module. Returned alongside the
	// English values rather than replacing them — the API serves both languages
	// unconditionally and the client picks which to render.
	//
	// Not unique-indexed (unlike Name) because they are optional and may
	// legitimately be blank on permissions that have not been translated yet.
	// Backfilled by migrations.MigratePermissionArabic.
	NameAr        string         `gorm:"size:100" json:"name_ar"`
	DescriptionAr string         `gorm:"size:500" json:"description_ar"`
	ModuleAr      string         `gorm:"size:50" json:"module_ar"`
	IsActive      bool           `gorm:"default:true" json:"is_active"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	DeletedAt     gorm.DeletedAt `gorm:"index" json:"-"`
}

func (p *Permission) BeforeCreate(tx *gorm.DB) error {
	if p.ID == uuid.Nil {
		p.ID = uuid.New()
	}
	return nil
}

// Role represents a user role with associated permissions
type Role struct {
	ID                  uuid.UUID      `gorm:"type:uuid;primary_key" json:"id"`
	Name                string         `gorm:"not null;size:100;uniqueIndex" json:"name"`
	NameAr              string         `gorm:"size:100" json:"name_ar"`
	Code                string         `gorm:"not null;size:50;uniqueIndex" json:"code"`
	Description         string         `gorm:"size:500" json:"description"`
	DescriptionAr       string         `gorm:"size:500" json:"description_ar"`
	IsSystem            bool           `gorm:"default:false" json:"is_system"` // System roles cannot be deleted
	IsActive            bool           `gorm:"default:true" json:"is_active"`
	IsDepartmentManager bool           `gorm:"default:false" json:"is_department_manager"`
	BypassLoginTotp     bool           `gorm:"default:false" json:"bypass_login_totp"`
	Permissions         []Permission   `gorm:"many2many:role_permissions;" json:"permissions,omitempty"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
	DeletedAt           gorm.DeletedAt `gorm:"index" json:"-"`
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

// PermissionResponse for API responses.
//
// Both languages are always returned: Name/Description/Module hold the English
// values and the *Ar fields hold the Arabic ones. The response does not vary with
// Accept-Language — every field is emitted unconditionally (no omitempty) so the
// key set is identical for every request and the client chooses what to render.
type PermissionResponse struct {
	ID            uuid.UUID `json:"id"`
	Name          string    `json:"name"`
	NameAr        string    `json:"name_ar"`
	Code          string    `json:"code"`
	Description   string    `json:"description"`
	DescriptionAr string    `json:"description_ar"`
	Module        string    `json:"module"`
	ModuleAr      string    `json:"module_ar"`
	Action        string    `json:"action"`
	ActionAr      string    `json:"action_ar"`
	IsActive      bool      `json:"is_active"`
	CreatedAt     time.Time `json:"created_at"`
}

func ToPermissionResponse(p *Permission) PermissionResponse {
	return PermissionResponse{
		ID:            p.ID,
		Name:          p.Name,
		NameAr:        p.NameAr,
		Code:          p.Code,
		Description:   p.Description,
		DescriptionAr: p.DescriptionAr,
		Module:        p.Module,
		ModuleAr:      p.ModuleAr,
		Action:        p.Action,
		ActionAr:      p.ActionAr,
		IsActive:      p.IsActive,
		CreatedAt:     p.CreatedAt,
	}
}

// RoleCreateRequest for creating a new role
type RoleCreateRequest struct {
	Name            string      `json:"name" validate:"required,notblank,name,min=1,max=100"`
	NameAr          string      `json:"name_ar" validate:"omitempty,notblank,name,max=100"`
	Code            string      `json:"code" validate:"omitempty,min=1,max=50"`
	Description     string      `json:"description" validate:"max=500"`
	DescriptionAr   string      `json:"description_ar" validate:"max=500"`
	PermissionIDs   []uuid.UUID `json:"permission_ids"`
	BypassLoginTotp bool        `json:"bypass_login_totp"`
}

// RoleUpdateRequest for updating a role
type RoleUpdateRequest struct {
	Name            string      `json:"name" validate:"omitempty,notblank,name,min=1,max=100"`
	NameAr          string      `json:"name_ar" validate:"omitempty,notblank,name,max=100"`
	Description     string      `json:"description" validate:"max=500"`
	DescriptionAr   string      `json:"description_ar" validate:"max=500"`
	PermissionIDs   []uuid.UUID `json:"permission_ids"`
	IsActive        *bool       `json:"is_active"`
	BypassLoginTotp *bool       `json:"bypass_login_totp"`
}

// RoleResponse for API responses
type RoleResponse struct {
	ID                  uuid.UUID            `json:"id"`
	Name                string               `json:"name"`
	NameAr              string               `json:"name_ar"`
	Code                string               `json:"code"`
	Description         string               `json:"description"`
	DescriptionAr       string               `json:"description_ar"`
	IsSystem            bool                 `json:"is_system"`
	IsActive            bool                 `json:"is_active"`
	IsDepartmentManager bool                 `json:"is_department_manager"`
	BypassLoginTotp     bool                 `json:"bypass_login_totp"`
	Permissions         []PermissionResponse `json:"permissions,omitempty"`
	CreatedAt           time.Time            `json:"created_at"`
}

func ToRoleResponse(r *Role) RoleResponse {
	resp := RoleResponse{
		ID:                  r.ID,
		Name:                r.Name,
		NameAr:              r.NameAr,
		Code:                r.Code,
		Description:         r.Description,
		DescriptionAr:       r.DescriptionAr,
		IsSystem:            r.IsSystem,
		IsActive:            r.IsActive,
		IsDepartmentManager: r.IsDepartmentManager,
		BypassLoginTotp:     r.BypassLoginTotp,
		CreatedAt:           r.CreatedAt,
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
	UserID     uuid.UUID  `gorm:"type:uuid;primaryKey" json:"user_id"`
	RoleID     uuid.UUID  `gorm:"type:uuid;primaryKey" json:"role_id"`
	AssignedAt time.Time  `gorm:"autoCreateTime" json:"assigned_at"`
	AssignedBy *uuid.UUID `gorm:"type:uuid" json:"assigned_by"`
}
