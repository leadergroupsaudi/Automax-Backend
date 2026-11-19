package models

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type CallStatus string

const (
	CallStatusOffline CallStatus = "offline"
	CallStatusOnline  CallStatus = "online"
	CallStatusBusy    CallStatus = "busy"
	CallStatusInCall  CallStatus = "in_call"
)

type User struct {
	ID                          uuid.UUID        `gorm:"type:uuid;primary_key" json:"id"`
	NationalID                  string           `gorm:"not null;default:''" json:"national_id"`
	Email                       string           `gorm:"uniqueIndex;not null" json:"email"`
	Username                    string           `gorm:"uniqueIndex;not null" json:"username"`
	Password                    string           `gorm:"not null" json:"-"`
	FirstName                   string           `gorm:"size:100" json:"first_name"`
	MiddleName                  string           `gorm:"size:100" json:"middle_name"`
	LastName                    string           `gorm:"size:100" json:"last_name"`
	Phone                       string           `gorm:"size:20" json:"phone"`
	MobileVerified              bool             `gorm:"default:false" json:"mobile_verified"`
	Avatar                      string           `gorm:"size:500" json:"avatar"`
	DepartmentID                *uuid.UUID       `gorm:"type:uuid;index" json:"department_id"`
	Department                  *Department      `gorm:"foreignKey:DepartmentID" json:"department,omitempty"`
	Departments                 []Department     `gorm:"many2many:user_departments;" json:"departments,omitempty"`
	LocationID                  *uuid.UUID       `gorm:"type:uuid;index" json:"location_id"`
	Location                    *Location        `gorm:"foreignKey:LocationID" json:"location,omitempty"`
	Locations                   []Location       `gorm:"many2many:user_locations;" json:"locations,omitempty"`
	Classifications             []Classification `gorm:"many2many:user_classifications;" json:"classifications,omitempty"`
	Roles                       []Role           `gorm:"many2many:user_roles;" json:"roles,omitempty"`
	IsActive                    bool             `gorm:"default:true" json:"is_active"`
	IsSuperAdmin                bool             `gorm:"default:false" json:"is_super_admin"`
	IsADUser                    bool             `gorm:"default:false" json:"is_ad_user"`
	BypassLoginTotp             bool             `gorm:"default:false" json:"bypass_login_totp"`
	EnableLoginTotp             bool             `gorm:"default:false" json:"enable_login_totp"`
	DeptManagerDepartmentID     *uuid.UUID       `gorm:"type:uuid;index" json:"dept_manager_department_id,omitempty"`
	DeptManagerDepartment       *Department      `gorm:"foreignKey:DeptManagerDepartmentID" json:"dept_manager_department,omitempty"`
	DeptManagerClassificationID *uuid.UUID       `gorm:"type:uuid;index" json:"dept_manager_classification_id,omitempty"`
	DeptManagerClassification   *Classification  `gorm:"foreignKey:DeptManagerClassificationID" json:"dept_manager_classification,omitempty"`
	DeptManagerLocationID       *uuid.UUID       `gorm:"type:uuid;index" json:"dept_manager_location_id,omitempty"`
	DeptManagerLocation         *Location        `gorm:"foreignKey:DeptManagerLocationID" json:"dept_manager_location,omitempty"`
	// Extension is transient (no DB column): the current PBX extension lives in the
	// extension_assignments table. It is populated from CurrentExtension by AfterFind
	// so API responses keep exposing it. Managed only via the extensions API.
	Extension string `gorm:"-" json:"extension"`
	// CurrentExtension is the user's current extension assignment (if any). Preload it
	// to populate Extension on read.
	CurrentExtension *ExtensionAssignment `gorm:"foreignKey:UserID" json:"-"`
	CallStatus       CallStatus           `gorm:"type:user_call_status;default:offline" json:"call_status"`
	LastLoginAt      *time.Time           `json:"last_login_at"`
	CreatedAt        time.Time            `json:"created_at"`
	UpdatedAt        time.Time            `json:"updated_at"`
	DeletedAt        gorm.DeletedAt       `gorm:"index" json:"-"`
}

func (u *User) BeforeCreate(tx *gorm.DB) error {
	if u.ID == uuid.Nil {
		u.ID = uuid.New()
	}
	return nil
}

// AfterFind copies the preloaded current extension onto the transient Extension
// field so serialized responses continue to include it.
func (u *User) AfterFind(tx *gorm.DB) error {
	if u.CurrentExtension != nil {
		u.Extension = u.CurrentExtension.Extension
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

// IsDepartmentManager checks if the user has any role with is_department_manager = true
func (u *User) IsDepartmentManager() bool {
	if u.IsSuperAdmin {
		return false
	}
	for _, role := range u.Roles {
		if role.IsDepartmentManager && role.IsActive {
			return true
		}
	}
	return false
}

// ScopeDepartmentID returns the department ID to use for department-scoped filtering.
// Priority: DeptManagerDepartmentID > DepartmentID > first M2M department.
func (u *User) ScopeDepartmentID() *uuid.UUID {
	if u.DeptManagerDepartmentID != nil {
		return u.DeptManagerDepartmentID
	}
	if u.DepartmentID != nil {
		return u.DepartmentID
	}
	if len(u.Departments) > 0 {
		id := u.Departments[0].ID
		return &id
	}
	return nil
}

// ScopeDepartmentIDs returns all department IDs the user is scoped to.
// Priority: DeptManagerDepartmentID > DepartmentID > all M2M departments.
func (u *User) ScopeDepartmentIDs() []uuid.UUID {
	if u.DeptManagerDepartmentID != nil {
		return []uuid.UUID{*u.DeptManagerDepartmentID}
	}
	if u.DepartmentID != nil {
		return []uuid.UUID{*u.DepartmentID}
	}
	ids := make([]uuid.UUID, 0, len(u.Departments))
	for _, d := range u.Departments {
		ids = append(ids, d.ID)
	}
	return ids
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
	MiddleName        string      `json:"middle_name" validate:"max=100"`
	LastName          string      `json:"last_name" validate:"max=100"`
	Phone             string      `json:"phone" validate:"omitempty,mobile,max=20"`
	Extension         string      `json:"extension" validate:"max=20"`
	DepartmentID      *uuid.UUID  `json:"department_id"`
	LocationID        *uuid.UUID  `json:"location_id"`
	DepartmentIDs     []uuid.UUID `json:"department_ids"`
	LocationIDs       []uuid.UUID `json:"location_ids"`
	ClassificationIDs []uuid.UUID `json:"classification_ids"`
	RoleIDs           []uuid.UUID `json:"role_ids"`
	BypassLoginTotp   bool        `json:"bypass_login_totp"`
	EnableLoginTotp   bool        `json:"enable_login_totp"`
}

// UserIdentifier is the minimal projection of a user row needed for duplicate checks.
type UserIdentifier struct {
	Email    string `json:"email"`
	Username string `json:"username"`
	Phone    string `json:"phone"`
}

// ExistingUserIdentifiers tracks which emails, usernames and phones are already taken - both by
// existing users and by rows already created earlier in the same bulk import. Emails are keyed
// lower-cased to match how Register normalises them before writing.
type ExistingUserIdentifiers struct {
	Emails    map[string]struct{}
	Usernames map[string]struct{}
	Phones    map[string]struct{}
}

func NewExistingUserIdentifiers(size int) *ExistingUserIdentifiers {
	return &ExistingUserIdentifiers{
		Emails:    make(map[string]struct{}, size),
		Usernames: make(map[string]struct{}, size),
		Phones:    make(map[string]struct{}, size),
	}
}

// Add marks the given identifiers as taken, ignoring empty values.
func (e *ExistingUserIdentifiers) Add(email, username, phone string) {
	if e == nil {
		return
	}
	if email = normalizeEmail(email); email != "" {
		e.Emails[email] = struct{}{}
	}
	if username = strings.TrimSpace(username); username != "" {
		e.Usernames[username] = struct{}{}
	}
	if phone = strings.TrimSpace(phone); phone != "" {
		e.Phones[phone] = struct{}{}
	}
}

func (e *ExistingUserIdentifiers) HasEmail(email string) bool {
	if e == nil {
		return false
	}
	return isTaken(e.Emails, normalizeEmail(email))
}

func (e *ExistingUserIdentifiers) HasUsername(username string) bool {
	if e == nil {
		return false
	}
	return isTaken(e.Usernames, strings.TrimSpace(username))
}

func (e *ExistingUserIdentifiers) HasPhone(phone string) bool {
	if e == nil {
		return false
	}
	return isTaken(e.Phones, strings.TrimSpace(phone))
}

func isTaken(set map[string]struct{}, value string) bool {
	if value == "" {
		return false
	}
	_, ok := set[value]
	return ok
}

// normalizeEmail matches the normalisation Register applies before persisting an email.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

type UserLoginRequest struct {
	// Email/Password login
	Email    string `json:"email" validate:"omitempty,email"`
	Password string `json:"password" validate:"omitempty"`

	// Mobile/OTP login
	Phone     string `json:"phone" validate:"omitempty"`
	SessionID string `json:"session_id" validate:"omitempty"`
	OTP       string `json:"otp" validate:"omitempty"`

	// Remember me flag
	RememberMe bool `json:"remember_me"`
}

// LoginType returns the type of login request
func (r *UserLoginRequest) LoginType() string {
	if r.Email != "" && r.Password != "" {
		return "email_password"
	}
	if r.Phone != "" {
		if r.SessionID != "" && r.OTP != "" {
			return "mobile_otp"
		}
		// Only phone provided - pre-validation step
		return "mobile_validation"
	}
	return "unknown"
}

type SSORegisterRequest struct {
	Email             string      `json:"email" validate:"required,email"`
	Username          string      `json:"username" validate:"required,min=3,max=50"`
	NationalID        string      `json:"national_id" validate:"required"`
	FirstName         string      `json:"first_name" validate:"max=100"`
	MiddleName        string      `json:"middle_name" validate:"max=100"`
	LastName          string      `json:"last_name" validate:"max=100"`
	Phone             string      `json:"phone" validate:"required,max=20"`
	Extension         string      `json:"extension" validate:"max=20"`
	DepartmentID      *uuid.UUID  `json:"department_id"`
	LocationID        *uuid.UUID  `json:"location_id"`
	DepartmentIDs     []uuid.UUID `json:"department_ids"`
	LocationIDs       []uuid.UUID `json:"location_ids"`
	ClassificationIDs []uuid.UUID `json:"classification_ids"`
	RoleIDs           []uuid.UUID `json:"role_ids"`
}

type SSOLoginRequest struct {
	NationalID string `json:"national_id" validate:"required"`
}

type UserUpdateRequest struct {
	FirstName                   string      `json:"first_name" validate:"max=100"`
	MiddleName                  string      `json:"middle_name" validate:"max=100"`
	LastName                    string      `json:"last_name" validate:"max=100"`
	Username                    string      `json:"username" validate:"omitempty,min=3,max=50"`
	Phone                       string      `json:"phone" validate:"omitempty,mobile,max=20"`
	MobileVerified              *bool       `json:"mobile_verified"`
	Extension                   *string     `json:"extension" validate:"omitempty,max=20"`
	DepartmentID                *uuid.UUID  `json:"department_id"`
	LocationID                  *uuid.UUID  `json:"location_id"`
	DepartmentIDs               []uuid.UUID `json:"department_ids"`
	LocationIDs                 []uuid.UUID `json:"location_ids"`
	ClassificationIDs           []uuid.UUID `json:"classification_ids"`
	RoleIDs                     []uuid.UUID `json:"role_ids"`
	IsActive                    *bool       `json:"is_active"`
	DeptManagerDepartmentID     *uuid.UUID  `json:"dept_manager_department_id"`
	DeptManagerClassificationID *uuid.UUID  `json:"dept_manager_classification_id"`
	DeptManagerLocationID       *uuid.UUID  `json:"dept_manager_location_id"`
	BypassLoginTotp             *bool       `json:"bypass_login_totp"`
	EnableLoginTotp             *bool       `json:"enable_login_totp"`
}

type UserResponse struct {
	ID                          uuid.UUID                `json:"id"`
	NationalID                  string                   `json:"national_id"`
	Email                       string                   `json:"email"`
	Username                    string                   `json:"username"`
	FirstName                   string                   `json:"first_name"`
	MiddleName                  string                   `json:"middle_name"`
	LastName                    string                   `json:"last_name"`
	Phone                       string                   `json:"phone"`
	MobileVerified              bool                     `json:"mobile_verified"`
	Avatar                      string                   `json:"avatar"`
	DepartmentID                *uuid.UUID               `json:"department_id"`
	Department                  *DepartmentResponse      `json:"department,omitempty"`
	Departments                 []DepartmentResponse     `json:"departments,omitempty"`
	LocationID                  *uuid.UUID               `json:"location_id"`
	Location                    *LocationResponse        `json:"location,omitempty"`
	Locations                   []LocationResponse       `json:"locations,omitempty"`
	Classifications             []ClassificationResponse `json:"classifications,omitempty"`
	Roles                       []RoleResponse           `json:"roles,omitempty"`
	Permissions                 []string                 `json:"permissions,omitempty"`
	IsActive                    bool                     `json:"is_active"`
	IsSuperAdmin                bool                     `json:"is_super_admin"`
	IsADUser                    bool                     `json:"is_ad_user"`
	BypassLoginTotp             bool                     `json:"bypass_login_totp"`
	EnableLoginTotp             bool                     `json:"enable_login_totp"`
	DeptManagerDepartmentID     *uuid.UUID               `json:"dept_manager_department_id,omitempty"`
	DeptManagerDepartment       *DepartmentResponse      `json:"dept_manager_department,omitempty"`
	DeptManagerClassificationID *uuid.UUID               `json:"dept_manager_classification_id,omitempty"`
	DeptManagerClassification   *ClassificationResponse  `json:"dept_manager_classification,omitempty"`
	DeptManagerLocationID       *uuid.UUID               `json:"dept_manager_location_id,omitempty"`
	DeptManagerLocation         *LocationResponse        `json:"dept_manager_location,omitempty"`
	Extension                   string                   `json:"extension"`
	CallStatus                  string                   `json:"call_status"`
	LastLoginAt                 *time.Time               `json:"last_login_at"`
	CreatedAt                   time.Time                `json:"created_at"`
}

type AuthResponse struct {
	User          UserResponse `json:"user"`
	Token         string       `json:"token"`
	RefreshToken  string       `json:"refresh_token,omitempty"`
	ExpiresIn     int64        `json:"expires_in,omitempty"` // seconds until access token expires
	ValidationURL string       `json:"validation_url,omitempty"`
}

// RoleBasicResponse is a slimmed-down role for the login response (no nested permissions).
type RoleBasicResponse struct {
	ID                  uuid.UUID `json:"id"`
	Name                string    `json:"name"`
	Code                string    `json:"code"`
	IsSystem            bool      `json:"is_system"`
	IsActive            bool      `json:"is_active"`
	IsDepartmentManager bool      `json:"is_department_manager"`
}

// UserLoginResponse is a slimmed-down user for the login response.
// Heavy relations (departments, locations, classifications, role permissions) are omitted.
// Clients that need full data should call GET /users/me after login.
type UserLoginResponse struct {
	ID             uuid.UUID           `json:"id"`
	NationalID     string              `json:"national_id"`
	Email          string              `json:"email"`
	Username       string              `json:"username"`
	FirstName      string              `json:"first_name"`
	MiddleName     string              `json:"middle_name"`
	LastName       string              `json:"last_name"`
	Phone          string              `json:"phone"`
	MobileVerified bool                `json:"mobile_verified"`
	Avatar         string              `json:"avatar"`
	DepartmentID   *uuid.UUID          `json:"department_id"`
	LocationID     *uuid.UUID          `json:"location_id"`
	Roles          []RoleBasicResponse `json:"roles,omitempty"`
	Permissions    []string            `json:"permissions,omitempty"`
	IsActive       bool                `json:"is_active"`
	IsSuperAdmin   bool                `json:"is_super_admin"`
	IsADUser       bool                `json:"is_ad_user"`
	Extension      string              `json:"extension"`
	CallStatus     string              `json:"call_status"`
	LastLoginAt    *time.Time          `json:"last_login_at"`
	CreatedAt      time.Time           `json:"created_at"`
}

// AuthLoginResponse is the response for POST /auth/login.
type AuthLoginResponse struct {
	User          UserLoginResponse `json:"user"`
	Token         string            `json:"token,omitempty"`
	RefreshToken  string            `json:"refresh_token,omitempty"`
	ExpiresIn     int64             `json:"expires_in,omitempty"`
	ValidationURL string            `json:"validation_url,omitempty"` // 3rd party validation URL for SSO flow
}

type RefreshTokenRequest struct {
	RefreshToken string `json:"refresh_token" validate:"required"`
	RememberMe   bool   `json:"remember_me"`
}

type ChangePasswordRequest struct {
	OldPassword string `json:"old_password" validate:"required"`
	NewPassword string `json:"new_password" validate:"required,min=6"`
}

// UserMatchRequest for finding users that match given criteria
type UserMatchRequest struct {
	RoleIDs          []string `json:"role_ids"`
	ClassificationID *string  `json:"classification_id" validate:"omitempty,uuid"`
	LocationID       *string  `json:"location_id" validate:"omitempty,uuid"`
	DepartmentID     *string  `json:"department_id" validate:"omitempty,uuid"`
	ExcludeUserID    *string  `json:"exclude_user_id" validate:"omitempty,uuid"` // Exclude current assignee
}

// UserMatchResponse for returning matched users
type UserMatchResponse struct {
	Users         []UserResponse `json:"users"`
	SingleMatch   bool           `json:"single_match"`
	MatchedUserID *string        `json:"matched_user_id,omitempty"`
}

func ToRoleBasicResponse(role *Role) RoleBasicResponse {
	return RoleBasicResponse{
		ID:                  role.ID,
		Name:                role.Name,
		Code:                role.Code,
		IsSystem:            role.IsSystem,
		IsActive:            role.IsActive,
		IsDepartmentManager: role.IsDepartmentManager,
	}
}

func ToUserLoginResponse(user *User) UserLoginResponse {
	resp := UserLoginResponse{
		ID:             user.ID,
		NationalID:     user.NationalID,
		Email:          user.Email,
		Username:       user.Username,
		FirstName:      user.FirstName,
		MiddleName:     user.MiddleName,
		LastName:       user.LastName,
		Phone:          user.Phone,
		MobileVerified: user.MobileVerified,
		Avatar:         user.Avatar,
		DepartmentID:   user.DepartmentID,
		LocationID:     user.LocationID,
		IsActive:       user.IsActive,
		IsSuperAdmin:   user.IsSuperAdmin,
		IsADUser:       user.IsADUser,
		Extension:      user.Extension,
		CallStatus:     string(user.CallStatus),
		LastLoginAt:    user.LastLoginAt,
		CreatedAt:      user.CreatedAt,
		Permissions:    user.GetPermissions(),
	}
	if len(user.Roles) > 0 {
		resp.Roles = make([]RoleBasicResponse, len(user.Roles))
		for i, role := range user.Roles {
			resp.Roles[i] = ToRoleBasicResponse(&role)
		}
	}
	return resp
}

func ToUserResponse(user *User) UserResponse {
	resp := UserResponse{
		ID:              user.ID,
		NationalID:      user.NationalID,
		Email:           user.Email,
		Username:        user.Username,
		FirstName:       user.FirstName,
		MiddleName:      user.MiddleName,
		LastName:        user.LastName,
		Phone:           user.Phone,
		MobileVerified:  user.MobileVerified,
		Avatar:          user.Avatar,
		DepartmentID:    user.DepartmentID,
		LocationID:      user.LocationID,
		IsActive:        user.IsActive,
		IsSuperAdmin:    user.IsSuperAdmin,
		IsADUser:        user.IsADUser,
		BypassLoginTotp: user.BypassLoginTotp,
		EnableLoginTotp: user.EnableLoginTotp,
		Extension:       user.Extension,
		CallStatus:      string(user.CallStatus),
		LastLoginAt:     user.LastLoginAt,
		CreatedAt:       user.CreatedAt,
		Permissions:     user.GetPermissions(),
	}

	if user.Department != nil {
		dept := ToDepartmentResponse(user.Department)
		resp.Department = &dept
	}

	if user.Location != nil {
		loc := ToLocationResponse(user.Location)
		resp.Location = &loc
	}

	if user.DeptManagerDepartment != nil {
		dept := ToDepartmentResponse(user.DeptManagerDepartment)
		resp.DeptManagerDepartment = &dept
	}

	if user.DeptManagerClassification != nil {
		cls := ToClassificationResponse(user.DeptManagerClassification)
		resp.DeptManagerClassification = &cls
	}

	if user.DeptManagerLocation != nil {
		loc := ToLocationResponse(user.DeptManagerLocation)
		resp.DeptManagerLocation = &loc
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

type ForgotPasswordRequest struct {
	Channel string `json:"channel" validate:"required"` // email | phone
	Value   string `json:"value" validate:"required"`
}

type VerifyOTPRequest struct {
	Channel   string `json:"channel" validate:"required"`
	SessionID string `json:"session_id" validate:"required"`
	Value     string `json:"value" validate:"required"`
	OTP       string `json:"otp" validate:"required"`
}

type ResetPasswordRequest struct {
	Token       string `json:"token" validate:"required"`
	NewPassword string `json:"new_password" validate:"required"`
}

type RedisOTPData struct {
	Value    string `json:"value"`
	OTP      string `json:"otp"` // hashed OTP stored here
	Channel  string `json:"channel"`
	Attempts int    `json:"attempts"`
}
