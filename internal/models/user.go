package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type CallStatus string

const (
	CallStatusOffline CallStatus = "offline"
	CallStatusOnline  CallStatus = "online"
	CallStatusBusy    CallStatus = "busy"
)

type User struct {
	ID              uuid.UUID        `gorm:"type:uuid;primary_key" json:"id"`
	Email           string           `gorm:"uniqueIndex;not null" json:"email"`
	Username        string           `gorm:"uniqueIndex;not null" json:"username"`
	Password        string           `gorm:"not null" json:"-"`
	FirstName       string           `gorm:"size:100" json:"first_name"`
	LastName        string           `gorm:"size:100" json:"last_name"`
	Phone           string           `gorm:"size:20" json:"phone"`
	Avatar          string           `gorm:"size:500" json:"avatar"`
	DepartmentID    *uuid.UUID       `gorm:"type:uuid;index" json:"department_id"`
	Department      *Department      `gorm:"foreignKey:DepartmentID" json:"department,omitempty"`
	Departments     []Department     `gorm:"many2many:user_departments;" json:"departments,omitempty"`
	LocationID      *uuid.UUID       `gorm:"type:uuid;index" json:"location_id"`
	Location        *Location        `gorm:"foreignKey:LocationID" json:"location,omitempty"`
	Locations       []Location       `gorm:"many2many:user_locations;" json:"locations,omitempty"`
	Classifications []Classification `gorm:"many2many:user_classifications;" json:"classifications,omitempty"`
	Roles           []Role           `gorm:"many2many:user_roles;" json:"roles,omitempty"`
	IsActive        bool             `gorm:"default:true" json:"is_active"`
	IsSuperAdmin    bool             `gorm:"default:false" json:"is_super_admin"`
	Extension       string           `gorm:"size:20" json:"extension"`
	CallStatus      CallStatus       `gorm:"type:user_call_status;default:offline" json:"call_status"`
	LastLoginAt     *time.Time       `json:"last_login_at"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
	DeletedAt       gorm.DeletedAt   `gorm:"index" json:"-"`
}

func (u *User) BeforeCreate(tx *gorm.DB) error {
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	return nil
}

// HasPermission checks if user has a specific permission
func (u *User) HasPermission(permissionCode string) bool {
	if u.IsSuperAdmin {
		return true
	}
	for _, role := range u.Roles {
		for _, perm := range role.Permissions {
			if perm.Code == permissionCode && perm.IsActive {
				return true
			}
		}
	}
	return false
}

// HasRole checks if user has a specific role
func (u *User) HasRole(roleCode string) bool {
	if u.IsSuperAdmin {
		return true
	}
	for _, role := range u.Roles {
		if role.Code == roleCode && role.IsActive {
			return true
		}
	}
	return false
}

// GetPermissions returns all unique permission codes for the user
func (u *User) GetPermissions() []string {
	if u.IsSuperAdmin {
		return []string{"*"} // Super admin has all permissions
	}

	permMap := make(map[string]bool)
	for _, role := range u.Roles {
		if !role.IsActive {
			continue
		}
		for _, perm := range role.Permissions {
			if perm.IsActive {
				permMap[perm.Code] = true
			}
		}
	}

	perms := make([]string, 0, len(permMap))
	for code := range permMap {
		perms = append(perms, code)
	}
	return perms
}

type UserRegisterRequest struct {
	Email             string      `json:"email" validate:"required,email"`
	Username          string      `json:"username" validate:"required,min=3,max=50"`
	Password          string      `json:"password" validate:"required,min=6"`
	FirstName         string      `json:"first_name" validate:"max=100"`
	LastName          string      `json:"last_name" validate:"max=100"`
	Phone             string      `json:"phone" validate:"max=20"`
	DepartmentID      *uuid.UUID  `json:"department_id"`
	LocationID        *uuid.UUID  `json:"location_id"`
	DepartmentIDs     []uuid.UUID `json:"department_ids"`
	LocationIDs       []uuid.UUID `json:"location_ids"`
	ClassificationIDs []uuid.UUID `json:"classification_ids"`
	RoleIDs           []uuid.UUID `json:"role_ids"`
}

type UserLoginRequest struct {
	Email    string `json:"email" validate:"required,email"`
	Password string `json:"password" validate:"required"`
}

type UserUpdateRequest struct {
	FirstName         string      `json:"first_name" validate:"max=100"`
	LastName          string      `json:"last_name" validate:"max=100"`
	Username          string      `json:"username" validate:"min=3,max=50"`
	Phone             string      `json:"phone" validate:"max=20"`
	Extension         string      `json:"extension" validate:"max=20"`
	DepartmentID      *uuid.UUID  `json:"department_id"`
	LocationID        *uuid.UUID  `json:"location_id"`
	DepartmentIDs     []uuid.UUID `json:"department_ids"`
	LocationIDs       []uuid.UUID `json:"location_ids"`
	ClassificationIDs []uuid.UUID `json:"classification_ids"`
	RoleIDs           []uuid.UUID `json:"role_ids"`
	IsActive          *bool       `json:"is_active"`
}

type UserResponse struct {
	ID              uuid.UUID                `json:"id"`
	Email           string                   `json:"email"`
	Username        string                   `json:"username"`
	FirstName       string                   `json:"first_name"`
	LastName        string                   `json:"last_name"`
	Phone           string                   `json:"phone"`
	Avatar          string                   `json:"avatar"`
	DepartmentID    *uuid.UUID               `json:"department_id"`
	Department      *DepartmentResponse      `json:"department,omitempty"`
	Departments     []DepartmentResponse     `json:"departments,omitempty"`
	LocationID      *uuid.UUID               `json:"location_id"`
	Location        *LocationResponse        `json:"location,omitempty"`
	Locations       []LocationResponse       `json:"locations,omitempty"`
	Classifications []ClassificationResponse `json:"classifications,omitempty"`
	Roles           []RoleResponse           `json:"roles,omitempty"`
	Permissions     []string                 `json:"permissions,omitempty"`
	IsActive        bool                     `json:"is_active"`
	IsSuperAdmin    bool                     `json:"is_super_admin"`
	Extension       string                   `json:"extension"`
	CallStatus      string                   `json:"call_status"`
	LastLoginAt     *time.Time               `json:"last_login_at"`
	CreatedAt       time.Time                `json:"created_at"`
}

type AuthResponse struct {
	User         UserResponse `json:"user"`
	Token        string       `json:"token"`
	RefreshToken string       `json:"refresh_token,omitempty"`
	ExpiresIn    int64        `json:"expires_in,omitempty"` // seconds until access token expires
}

type RefreshTokenRequest struct {
	RefreshToken string `json:"refresh_token" validate:"required"`
}

type ChangePasswordRequest struct {
	OldPassword string `json:"old_password" validate:"required"`
	NewPassword string `json:"new_password" validate:"required,min=6"`
}

// UserMatchRequest for finding users that match given criteria
type UserMatchRequest struct {
	RoleID           *string `json:"role_id" validate:"omitempty,uuid"`
	ClassificationID *string `json:"classification_id" validate:"omitempty,uuid"`
	LocationID       *string `json:"location_id" validate:"omitempty,uuid"`
	DepartmentID     *string `json:"department_id" validate:"omitempty,uuid"`
	ExcludeUserID    *string `json:"exclude_user_id" validate:"omitempty,uuid"` // Exclude current assignee
}

// UserMatchResponse for returning matched users
type UserMatchResponse struct {
	Users         []UserResponse `json:"users"`
	SingleMatch   bool           `json:"single_match"`
	MatchedUserID *string        `json:"matched_user_id,omitempty"`
}

func ToUserResponse(user *User) UserResponse {
	resp := UserResponse{
		ID:           user.ID,
		Email:        user.Email,
		Username:     user.Username,
		FirstName:    user.FirstName,
		LastName:     user.LastName,
		Phone:        user.Phone,
		Avatar:       user.Avatar,
		DepartmentID: user.DepartmentID,
		LocationID:   user.LocationID,
		IsActive:     user.IsActive,
		IsSuperAdmin: user.IsSuperAdmin,
		Extension:    user.Extension,
		CallStatus:   string(user.CallStatus),
		LastLoginAt:  user.LastLoginAt,
		CreatedAt:    user.CreatedAt,
		Permissions:  user.GetPermissions(),
	}

	if user.Department != nil {
		dept := ToDepartmentResponse(user.Department)
		resp.Department = &dept
	}

	if user.Location != nil {
		loc := ToLocationResponse(user.Location)
		resp.Location = &loc
	}

	if len(user.Departments) > 0 {
		resp.Departments = make([]DepartmentResponse, len(user.Departments))
		for i, dept := range user.Departments {
			resp.Departments[i] = ToDepartmentResponse(&dept)
		}
	}

	if len(user.Locations) > 0 {
		resp.Locations = make([]LocationResponse, len(user.Locations))
		for i, loc := range user.Locations {
			resp.Locations[i] = ToLocationResponse(&loc)
		}
	}

	if len(user.Classifications) > 0 {
		resp.Classifications = make([]ClassificationResponse, len(user.Classifications))
		for i, cls := range user.Classifications {
			resp.Classifications[i] = ToClassificationResponse(&cls)
		}
	}

	if len(user.Roles) > 0 {
		resp.Roles = make([]RoleResponse, len(user.Roles))
		for i, role := range user.Roles {
			resp.Roles[i] = ToRoleResponse(&role)
		}
	}

	return resp
}
