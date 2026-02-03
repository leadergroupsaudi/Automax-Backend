package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Workflow represents a reusable workflow template
type Workflow struct {
	ID          uuid.UUID          `gorm:"type:uuid;primary_key" json:"id"`
	Name        string             `gorm:"not null;size:100;uniqueIndex" json:"name"`
	Code        string             `gorm:"not null;size:50;uniqueIndex" json:"code"`
	Description string             `gorm:"size:500" json:"description"`
	Version     int                `gorm:"default:1" json:"version"`
	IsActive    bool               `gorm:"default:true" json:"is_active"`
	IsDefault   bool               `gorm:"default:false" json:"is_default"`
	RecordType  string             `gorm:"size:20;default:'incident'" json:"record_type"` // 'incident', 'request', 'complaint', 'both', 'all'

	// Visual designer metadata (stores canvas layout as JSON)
	CanvasLayout string `gorm:"type:text" json:"canvas_layout"`

	// Form configuration - stores which fields are required (JSON array of field names)
	// e.g., ["description", "classification_id", "priority", "severity", "assignee_id", "department_id", "location_id", "due_date", "reporter_name", "reporter_email", "source"]
	RequiredFields string `gorm:"type:text" json:"required_fields"`

	// Relationships
	States          []WorkflowState      `gorm:"foreignKey:WorkflowID" json:"states,omitempty"`
	Transitions     []WorkflowTransition `gorm:"foreignKey:WorkflowID" json:"transitions,omitempty"`
	Classifications []Classification     `gorm:"many2many:workflow_classifications;" json:"classifications,omitempty"`

	// Role-based convert-to-request permission (many-to-many) - empty = all users can convert
	ConvertToRequestRoles []Role `gorm:"many2many:workflow_convert_to_request_roles;" json:"convert_to_request_roles,omitempty"`

	CreatedByID *uuid.UUID     `gorm:"type:uuid" json:"created_by_id"`
	CreatedBy   *User          `gorm:"foreignKey:CreatedByID" json:"created_by,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`
}

func (w *Workflow) BeforeCreate(tx *gorm.DB) error {
	if w.ID == uuid.Nil {
		w.ID = uuid.New()
	}
	return nil
}

// WorkflowState represents a state/node within a workflow
type WorkflowState struct {
	ID          uuid.UUID `gorm:"type:uuid;primary_key" json:"id"`
	WorkflowID  uuid.UUID `gorm:"type:uuid;index;not null" json:"workflow_id"`
	Workflow    *Workflow `gorm:"foreignKey:WorkflowID" json:"workflow,omitempty"`
	Name        string    `gorm:"not null;size:100" json:"name"`
	Code        string    `gorm:"not null;size:50" json:"code"`
	Description string    `gorm:"size:500" json:"description"`
	StateType   string    `gorm:"size:20;default:'normal'" json:"state_type"` // initial, normal, terminal
	Color       string    `gorm:"size:20;default:'#6366f1'" json:"color"`

	// Visual position on canvas
	PositionX int `gorm:"default:0" json:"position_x"`
	PositionY int `gorm:"default:0" json:"position_y"`

	// SLA Configuration (hours allowed in this state)
	SLAHours *int `gorm:"default:null" json:"sla_hours"`

	// Role-based visibility (many-to-many) - empty = visible to all
	ViewableRoles []Role `gorm:"many2many:state_viewable_roles;" json:"viewable_roles,omitempty"`

	SortOrder int            `gorm:"default:0" json:"sort_order"`
	IsActive  bool           `gorm:"default:true" json:"is_active"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

func (s *WorkflowState) BeforeCreate(tx *gorm.DB) error {
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	return nil
}

// WorkflowTransition represents a transition between states
type WorkflowTransition struct {
	ID          uuid.UUID      `gorm:"type:uuid;primary_key" json:"id"`
	WorkflowID  uuid.UUID      `gorm:"type:uuid;index;not null" json:"workflow_id"`
	Workflow    *Workflow      `gorm:"foreignKey:WorkflowID" json:"workflow,omitempty"`
	Name        string         `gorm:"not null;size:100" json:"name"`
	Code        string         `gorm:"not null;size:50" json:"code"`
	Description string         `gorm:"size:500" json:"description"`

	FromStateID uuid.UUID      `gorm:"type:uuid;index;not null" json:"from_state_id"`
	FromState   *WorkflowState `gorm:"foreignKey:FromStateID" json:"from_state,omitempty"`
	ToStateID   uuid.UUID      `gorm:"type:uuid;index;not null" json:"to_state_id"`
	ToState     *WorkflowState `gorm:"foreignKey:ToStateID" json:"to_state,omitempty"`

	// Role-based restrictions (many-to-many)
	AllowedRoles []Role `gorm:"many2many:transition_allowed_roles;" json:"allowed_roles,omitempty"`

	// Department Assignment
	AssignDepartmentID   *uuid.UUID  `gorm:"type:uuid" json:"assign_department_id"`
	AssignDepartment     *Department `gorm:"foreignKey:AssignDepartmentID" json:"assign_department,omitempty"`
	AutoDetectDepartment bool        `gorm:"default:false" json:"auto_detect_department"`
	// If auto_detect_department=true: find departments matching incident's classification+location
	// If one match: auto-assign. If multiple: show selection in transition modal

	// User Assignment
	AssignUserID     *uuid.UUID `gorm:"type:uuid" json:"assign_user_id"`
	AssignUser       *User      `gorm:"foreignKey:AssignUserID" json:"assign_user,omitempty"`
	AssignmentRoleID *uuid.UUID `gorm:"type:uuid" json:"assignment_role_id"`
	AssignmentRole   *Role      `gorm:"foreignKey:AssignmentRoleID" json:"assignment_role,omitempty"`
	AutoMatchUser    bool       `gorm:"default:false" json:"auto_match_user"`
	// If auto_match_user=true with assignment_role_id:
	//   Find users with that role + matching incident's classification/location/department
	//   If multiple match: assign to ALL matched users
	ManualSelectUser bool `gorm:"default:false" json:"manual_select_user"`
	// If manual_select_user=true: user performing transition manually selects the assignee from dropdown

	// Requirements and Actions
	Requirements []TransitionRequirement `gorm:"foreignKey:TransitionID" json:"requirements,omitempty"`
	Actions      []TransitionAction      `gorm:"foreignKey:TransitionID" json:"actions,omitempty"`

	IsActive  bool           `gorm:"default:true" json:"is_active"`
	SortOrder int            `gorm:"default:0" json:"sort_order"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

func (t *WorkflowTransition) BeforeCreate(tx *gorm.DB) error {
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	return nil
}

// TransitionRequirement defines mandatory requirements for a transition
type TransitionRequirement struct {
	ID           uuid.UUID           `gorm:"type:uuid;primary_key" json:"id"`
	TransitionID uuid.UUID           `gorm:"type:uuid;index;not null" json:"transition_id"`
	Transition   *WorkflowTransition `gorm:"foreignKey:TransitionID" json:"transition,omitempty"`

	RequirementType string `gorm:"size:50;not null" json:"requirement_type"` // comment, attachment, field_value
	FieldName       string `gorm:"size:100" json:"field_name"`               // for field_value type
	FieldValue      string `gorm:"size:500" json:"field_value"`              // expected value or validation rule
	IsMandatory     *bool  `gorm:"default:true" json:"is_mandatory"`
	ErrorMessage    string `gorm:"size:200" json:"error_message"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (r *TransitionRequirement) BeforeCreate(tx *gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	return nil
}

// TransitionAction defines automation actions triggered on transition
type TransitionAction struct {
	ID           uuid.UUID           `gorm:"type:uuid;primary_key" json:"id"`
	TransitionID uuid.UUID           `gorm:"type:uuid;index;not null" json:"transition_id"`
	Transition   *WorkflowTransition `gorm:"foreignKey:TransitionID" json:"transition,omitempty"`

	ActionType  string `gorm:"size:50;not null" json:"action_type"` // email, field_update, webhook, notification
	Name        string `gorm:"size:100;not null" json:"name"`
	Description string `gorm:"size:500" json:"description"`

	// Configuration (JSON for flexibility)
	Config string `gorm:"type:text" json:"config"`

	ExecutionOrder int  `gorm:"default:0" json:"execution_order"`
	IsAsync        bool `gorm:"default:false" json:"is_async"`
	IsActive       bool `gorm:"default:true" json:"is_active"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (a *TransitionAction) BeforeCreate(tx *gorm.DB) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	return nil
}

// Request/Response types

type WorkflowCreateRequest struct {
	Name              string   `json:"name" validate:"required,min=2,max=100"`
	Code              string   `json:"code" validate:"required,min=2,max=50"`
	Description       string   `json:"description" validate:"max=500"`
	RecordType        string   `json:"record_type" validate:"omitempty,oneof=incident request complaint both all"`
	ClassificationIDs []string `json:"classification_ids"`
	RequiredFields    []string `json:"required_fields"`
}

type WorkflowUpdateRequest struct {
	Name                    string   `json:"name" validate:"omitempty,min=2,max=100"`
	Code                    string   `json:"code" validate:"omitempty,min=2,max=50"`
	Description             string   `json:"description" validate:"max=500"`
	RecordType              *string  `json:"record_type" validate:"omitempty,oneof=incident request complaint both all"`
	IsActive                *bool    `json:"is_active"`
	IsDefault               *bool    `json:"is_default"`
	CanvasLayout            string   `json:"canvas_layout"`
	ClassificationIDs       []string `json:"classification_ids"`
	RequiredFields          []string `json:"required_fields"`
	ConvertToRequestRoleIDs []string `json:"convert_to_request_role_ids"`
}

type WorkflowStateCreateRequest struct {
	Name            string   `json:"name" validate:"required,min=2,max=100"`
	Code            string   `json:"code" validate:"required,min=2,max=50"`
	Description     string   `json:"description" validate:"max=500"`
	StateType       string   `json:"state_type" validate:"omitempty,oneof=initial normal terminal"`
	Color           string   `json:"color" validate:"omitempty,max=20"`
	PositionX       int      `json:"position_x"`
	PositionY       int      `json:"position_y"`
	SLAHours        *int     `json:"sla_hours"`
	SortOrder       int      `json:"sort_order"`
	ViewableRoleIDs []string `json:"viewable_role_ids"`
}

type WorkflowStateUpdateRequest struct {
	Name            string   `json:"name" validate:"omitempty,min=2,max=100"`
	Code            string   `json:"code" validate:"omitempty,min=2,max=50"`
	Description     string   `json:"description" validate:"max=500"`
	StateType       string   `json:"state_type" validate:"omitempty,oneof=initial normal terminal"`
	Color           string   `json:"color" validate:"omitempty,max=20"`
	PositionX       *int     `json:"position_x"`
	PositionY       *int     `json:"position_y"`
	SLAHours        *int     `json:"sla_hours"`
	SortOrder       *int     `json:"sort_order"`
	IsActive        *bool    `json:"is_active"`
	ViewableRoleIDs []string `json:"viewable_role_ids"`
}

type WorkflowTransitionCreateRequest struct {
	Name        string   `json:"name" validate:"required,min=2,max=100"`
	Code        string   `json:"code" validate:"required,min=2,max=50"`
	Description string   `json:"description" validate:"max=500"`
	FromStateID string   `json:"from_state_id" validate:"required,uuid"`
	ToStateID   string   `json:"to_state_id" validate:"required,uuid"`
	RoleIDs     []string `json:"role_ids"`
	SortOrder   int      `json:"sort_order"`

	// Department Assignment
	AssignDepartmentID   *string `json:"assign_department_id" validate:"omitempty,uuid"`
	AutoDetectDepartment bool    `json:"auto_detect_department"`

	// User Assignment
	AssignUserID     *string `json:"assign_user_id" validate:"omitempty,uuid"`
	AssignmentRoleID *string `json:"assignment_role_id" validate:"omitempty,uuid"`
	AutoMatchUser    bool    `json:"auto_match_user"`
	ManualSelectUser bool    `json:"manual_select_user"`
}

type WorkflowTransitionUpdateRequest struct {
	Name        string   `json:"name" validate:"omitempty,min=2,max=100"`
	Code        string   `json:"code" validate:"omitempty,min=2,max=50"`
	Description string   `json:"description" validate:"max=500"`
	FromStateID string   `json:"from_state_id" validate:"omitempty,uuid"`
	ToStateID   string   `json:"to_state_id" validate:"omitempty,uuid"`
	RoleIDs     []string `json:"role_ids"`
	SortOrder   *int     `json:"sort_order"`
	IsActive    *bool    `json:"is_active"`

	// Department Assignment
	AssignDepartmentID   *string `json:"assign_department_id" validate:"omitempty,uuid"`
	AutoDetectDepartment *bool   `json:"auto_detect_department"`

	// User Assignment
	AssignUserID     *string `json:"assign_user_id" validate:"omitempty,uuid"`
	AssignmentRoleID *string `json:"assignment_role_id" validate:"omitempty,uuid"`
	AutoMatchUser    *bool   `json:"auto_match_user"`
	ManualSelectUser *bool   `json:"manual_select_user"`
}

type TransitionRequirementRequest struct {
	RequirementType string `json:"requirement_type" validate:"required,oneof=comment attachment feedback field_value"`
	FieldName       string `json:"field_name"`
	FieldValue      string `json:"field_value"`
	IsMandatory     *bool  `json:"is_mandatory"`
	ErrorMessage    string `json:"error_message"`
}

type TransitionActionRequest struct {
	ActionType     string `json:"action_type" validate:"required,oneof=email field_update webhook notification"`
	Name           string `json:"name" validate:"required,min=2,max=100"`
	Description    string `json:"description"`
	Config         string `json:"config"`
	ExecutionOrder int    `json:"execution_order"`
	IsAsync        bool   `json:"is_async"`
	IsActive       bool   `json:"is_active"`
}

// Workflow matching request - used by mobile apps and other clients
type WorkflowMatchRequest struct {
	ClassificationID string `json:"classification_id"`
	LocationID       string `json:"location_id"`
	Source           string `json:"source"`
	Severity         int    `json:"severity"`
	Priority         int    `json:"priority"`
}

// Form field configuration for incident creation
type IncidentFormFieldConfig struct {
	Field       string `json:"field"`
	Label       string `json:"label"`
	Description string `json:"description"`
	IsRequired  bool   `json:"is_required"`
}

// Response for workflow matching - returns matched workflow and form configuration
type WorkflowMatchResponse struct {
	Matched        bool                      `json:"matched"`
	WorkflowID     *string                   `json:"workflow_id,omitempty"`
	WorkflowName   *string                   `json:"workflow_name,omitempty"`
	WorkflowCode   *string                   `json:"workflow_code,omitempty"`
	RequiredFields []string                  `json:"required_fields"`
	FormFields     []IncidentFormFieldConfig `json:"form_fields"`
	InitialStateID *string                   `json:"initial_state_id,omitempty"`
	InitialState   *string                   `json:"initial_state,omitempty"`
}

// Response types

type WorkflowResponse struct {
	ID                    uuid.UUID                    `json:"id"`
	Name                  string                       `json:"name"`
	Code                  string                       `json:"code"`
	Description           string                       `json:"description"`
	Version               int                          `json:"version"`
	IsActive              bool                         `json:"is_active"`
	IsDefault             bool                         `json:"is_default"`
	RecordType            string                       `json:"record_type"`
	CanvasLayout          string                       `json:"canvas_layout,omitempty"`
	RequiredFields        []string                     `json:"required_fields"`
	States                []WorkflowStateResponse      `json:"states,omitempty"`
	Transitions           []WorkflowTransitionResponse `json:"transitions,omitempty"`
	Classifications       []ClassificationResponse     `json:"classifications,omitempty"`
	ConvertToRequestRoles []RoleResponse               `json:"convert_to_request_roles,omitempty"`
	StatesCount           int                          `json:"states_count"`
	TransitionsCount      int                          `json:"transitions_count"`
	CreatedBy             *UserResponse                `json:"created_by,omitempty"`
	CreatedAt             time.Time                    `json:"created_at"`
	UpdatedAt             time.Time                    `json:"updated_at"`
}

type WorkflowStateResponse struct {
	ID            uuid.UUID      `json:"id"`
	WorkflowID    uuid.UUID      `json:"workflow_id"`
	Name          string         `json:"name"`
	Code          string         `json:"code"`
	Description   string         `json:"description"`
	StateType     string         `json:"state_type"`
	Color         string         `json:"color"`
	PositionX     int            `json:"position_x"`
	PositionY     int            `json:"position_y"`
	SLAHours      *int           `json:"sla_hours"`
	SortOrder     int            `json:"sort_order"`
	IsActive      bool           `json:"is_active"`
	ViewableRoles []RoleResponse `json:"viewable_roles,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
}

type WorkflowTransitionResponse struct {
	ID           uuid.UUID                       `json:"id"`
	WorkflowID   uuid.UUID                       `json:"workflow_id"`
	Name         string                          `json:"name"`
	Code         string                          `json:"code"`
	Description  string                          `json:"description"`
	FromStateID  uuid.UUID                       `json:"from_state_id"`
	FromState    *WorkflowStateResponse          `json:"from_state,omitempty"`
	ToStateID    uuid.UUID                       `json:"to_state_id"`
	ToState      *WorkflowStateResponse          `json:"to_state,omitempty"`
	AllowedRoles []RoleResponse                  `json:"allowed_roles,omitempty"`

	// Department Assignment
	AssignDepartmentID   *uuid.UUID          `json:"assign_department_id,omitempty"`
	AssignDepartment     *DepartmentResponse `json:"assign_department,omitempty"`
	AutoDetectDepartment bool                `json:"auto_detect_department"`

	// User Assignment
	AssignUserID     *uuid.UUID    `json:"assign_user_id,omitempty"`
	AssignUser       *UserResponse `json:"assign_user,omitempty"`
	AssignmentRoleID *uuid.UUID    `json:"assignment_role_id,omitempty"`
	AssignmentRole   *RoleResponse `json:"assignment_role,omitempty"`
	AutoMatchUser    bool          `json:"auto_match_user"`
	ManualSelectUser bool          `json:"manual_select_user"`

	Requirements []TransitionRequirementResponse `json:"requirements,omitempty"`
	Actions      []TransitionActionResponse      `json:"actions,omitempty"`
	IsActive     bool                            `json:"is_active"`
	SortOrder    int                             `json:"sort_order"`
	CreatedAt    time.Time                       `json:"created_at"`
}

type TransitionRequirementResponse struct {
	ID              uuid.UUID `json:"id"`
	TransitionID    uuid.UUID `json:"transition_id"`
	RequirementType string    `json:"requirement_type"`
	FieldName       string    `json:"field_name,omitempty"`
	FieldValue      string    `json:"field_value,omitempty"`
	IsMandatory     *bool     `json:"is_mandatory"`
	ErrorMessage    string    `json:"error_message,omitempty"`
}

type TransitionActionResponse struct {
	ID             uuid.UUID `json:"id"`
	TransitionID   uuid.UUID `json:"transition_id"`
	ActionType     string    `json:"action_type"`
	Name           string    `json:"name"`
	Description    string    `json:"description,omitempty"`
	Config         string    `json:"config,omitempty"`
	ExecutionOrder int       `json:"execution_order"`
	IsAsync        bool      `json:"is_async"`
	IsActive       bool      `json:"is_active"`
}

// Converter functions

func ToWorkflowResponse(w *Workflow) WorkflowResponse {
	// Parse RequiredFields JSON string to array
	var requiredFields []string
	if w.RequiredFields != "" {
		json.Unmarshal([]byte(w.RequiredFields), &requiredFields)
	}
	if requiredFields == nil {
		requiredFields = []string{}
	}

	resp := WorkflowResponse{
		ID:               w.ID,
		Name:             w.Name,
		Code:             w.Code,
		Description:      w.Description,
		Version:          w.Version,
		IsActive:         w.IsActive,
		IsDefault:        w.IsDefault,
		RecordType:       w.RecordType,
		CanvasLayout:     w.CanvasLayout,
		RequiredFields:   requiredFields,
		StatesCount:      len(w.States),
		TransitionsCount: len(w.Transitions),
		CreatedAt:        w.CreatedAt,
		UpdatedAt:        w.UpdatedAt,
	}

	if w.CreatedBy != nil {
		userResp := ToUserResponse(w.CreatedBy)
		resp.CreatedBy = &userResp
	}

	if len(w.States) > 0 {
		resp.States = make([]WorkflowStateResponse, len(w.States))
		for i, s := range w.States {
			resp.States[i] = ToWorkflowStateResponse(&s)
		}
	}

	if len(w.Transitions) > 0 {
		resp.Transitions = make([]WorkflowTransitionResponse, len(w.Transitions))
		for i, t := range w.Transitions {
			resp.Transitions[i] = ToWorkflowTransitionResponse(&t)
		}
	}

	if len(w.Classifications) > 0 {
		resp.Classifications = make([]ClassificationResponse, len(w.Classifications))
		for i, c := range w.Classifications {
			resp.Classifications[i] = ToClassificationResponse(&c)
		}
	}

	if len(w.ConvertToRequestRoles) > 0 {
		resp.ConvertToRequestRoles = make([]RoleResponse, len(w.ConvertToRequestRoles))
		for i, r := range w.ConvertToRequestRoles {
			resp.ConvertToRequestRoles[i] = ToRoleResponse(&r)
		}
	}

	return resp
}

func ToWorkflowStateResponse(s *WorkflowState) WorkflowStateResponse {
	resp := WorkflowStateResponse{
		ID:          s.ID,
		WorkflowID:  s.WorkflowID,
		Name:        s.Name,
		Code:        s.Code,
		Description: s.Description,
		StateType:   s.StateType,
		Color:       s.Color,
		PositionX:   s.PositionX,
		PositionY:   s.PositionY,
		SLAHours:    s.SLAHours,
		SortOrder:   s.SortOrder,
		IsActive:    s.IsActive,
		CreatedAt:   s.CreatedAt,
	}

	if len(s.ViewableRoles) > 0 {
		resp.ViewableRoles = make([]RoleResponse, len(s.ViewableRoles))
		for i, r := range s.ViewableRoles {
			resp.ViewableRoles[i] = ToRoleResponse(&r)
		}
	}

	return resp
}

func ToWorkflowTransitionResponse(t *WorkflowTransition) WorkflowTransitionResponse {
	resp := WorkflowTransitionResponse{
		ID:                   t.ID,
		WorkflowID:           t.WorkflowID,
		Name:                 t.Name,
		Code:                 t.Code,
		Description:          t.Description,
		FromStateID:          t.FromStateID,
		ToStateID:            t.ToStateID,
		AssignDepartmentID:   t.AssignDepartmentID,
		AutoDetectDepartment: t.AutoDetectDepartment,
		AssignUserID:         t.AssignUserID,
		AssignmentRoleID:     t.AssignmentRoleID,
		AutoMatchUser:        t.AutoMatchUser,
		ManualSelectUser:     t.ManualSelectUser,
		IsActive:             t.IsActive,
		SortOrder:            t.SortOrder,
		CreatedAt:            t.CreatedAt,
	}

	if t.FromState != nil {
		fromStateResp := ToWorkflowStateResponse(t.FromState)
		resp.FromState = &fromStateResp
	}

	if t.ToState != nil {
		toStateResp := ToWorkflowStateResponse(t.ToState)
		resp.ToState = &toStateResp
	}

	if len(t.AllowedRoles) > 0 {
		resp.AllowedRoles = make([]RoleResponse, len(t.AllowedRoles))
		for i, r := range t.AllowedRoles {
			resp.AllowedRoles[i] = ToRoleResponse(&r)
		}
	}

	// Department Assignment
	if t.AssignDepartment != nil {
		deptResp := ToDepartmentResponse(t.AssignDepartment)
		resp.AssignDepartment = &deptResp
	}

	// User Assignment
	if t.AssignUser != nil {
		userResp := ToUserResponse(t.AssignUser)
		resp.AssignUser = &userResp
	}

	if t.AssignmentRole != nil {
		roleResp := ToRoleResponse(t.AssignmentRole)
		resp.AssignmentRole = &roleResp
	}

	if len(t.Requirements) > 0 {
		resp.Requirements = make([]TransitionRequirementResponse, len(t.Requirements))
		for i, req := range t.Requirements {
			resp.Requirements[i] = ToTransitionRequirementResponse(&req)
		}
	}

	if len(t.Actions) > 0 {
		resp.Actions = make([]TransitionActionResponse, len(t.Actions))
		for i, a := range t.Actions {
			resp.Actions[i] = ToTransitionActionResponse(&a)
		}
	}

	return resp
}

func ToTransitionRequirementResponse(r *TransitionRequirement) TransitionRequirementResponse {
	return TransitionRequirementResponse{
		ID:              r.ID,
		TransitionID:    r.TransitionID,
		RequirementType: r.RequirementType,
		FieldName:       r.FieldName,
		FieldValue:      r.FieldValue,
		IsMandatory:     r.IsMandatory,
		ErrorMessage:    r.ErrorMessage,
	}
}

func ToTransitionActionResponse(a *TransitionAction) TransitionActionResponse {
	return TransitionActionResponse{
		ID:             a.ID,
		TransitionID:   a.TransitionID,
		ActionType:     a.ActionType,
		Name:           a.Name,
		Description:    a.Description,
		Config:         a.Config,
		ExecutionOrder: a.ExecutionOrder,
		IsAsync:        a.IsAsync,
		IsActive:       a.IsActive,
	}
}

// Export/Import structures

// CodeNamePair represents a portable reference using code and name
type CodeNamePair struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// WorkflowExportData is the top-level export structure
type WorkflowExportData struct {
	ExportVersion string                 `json:"export_version"`
	ExportedAt    string                 `json:"exported_at"`
	Workflow      WorkflowExportContent  `json:"workflow"`
}

// WorkflowExportContent contains workflow data with codes instead of IDs
type WorkflowExportContent struct {
	Name                  string                      `json:"name"`
	Code                  string                      `json:"code"`
	Description           string                      `json:"description"`
	RecordType            string                      `json:"record_type"`
	RequiredFields        []string                    `json:"required_fields"`
	States                []WorkflowStateExport       `json:"states"`
	Transitions           []WorkflowTransitionExport  `json:"transitions"`
	Classifications       []CodeNamePair              `json:"classifications"`
	ConvertToRequestRoles []CodeNamePair              `json:"convert_to_request_roles"`
}

// WorkflowStateExport represents a state with viewable role codes
type WorkflowStateExport struct {
	Name          string         `json:"name"`
	Code          string         `json:"code"`
	Description   string         `json:"description"`
	StateType     string         `json:"state_type"`
	Color         string         `json:"color"`
	PositionX     int            `json:"position_x"`
	PositionY     int            `json:"position_y"`
	SLAHours      *int           `json:"sla_hours,omitempty"`
	SortOrder     int            `json:"sort_order"`
	ViewableRoles []CodeNamePair `json:"viewable_roles,omitempty"`
}

// WorkflowTransitionExport represents a transition with codes and nested requirements/actions
type WorkflowTransitionExport struct {
	Name                 string                              `json:"name"`
	Code                 string                              `json:"code"`
	Description          string                              `json:"description"`
	FromStateCode        string                              `json:"from_state_code"`
	ToStateCode          string                              `json:"to_state_code"`
	AllowedRoles         []CodeNamePair                      `json:"allowed_roles,omitempty"`
	AssignDepartment     *CodeNamePair                       `json:"assign_department,omitempty"`
	AutoDetectDepartment bool                                `json:"auto_detect_department"`
	AssignUser           *CodeNamePair                       `json:"assign_user,omitempty"`
	AssignmentRole       *CodeNamePair                       `json:"assignment_role,omitempty"`
	AutoMatchUser        bool                                `json:"auto_match_user"`
	ManualSelectUser     bool                                `json:"manual_select_user"`
	Requirements         []TransitionRequirementExport       `json:"requirements,omitempty"`
	Actions              []TransitionActionExport            `json:"actions,omitempty"`
	SortOrder            int                                 `json:"sort_order"`
}

// TransitionRequirementExport represents a requirement without IDs
type TransitionRequirementExport struct {
	RequirementType string `json:"requirement_type"`
	FieldName       string `json:"field_name,omitempty"`
	FieldValue      string `json:"field_value,omitempty"`
	IsMandatory     *bool  `json:"is_mandatory"`
	ErrorMessage    string `json:"error_message,omitempty"`
}

// TransitionActionExport represents an action without IDs
type TransitionActionExport struct {
	ActionType     string `json:"action_type"`
	Name           string `json:"name"`
	Description    string `json:"description,omitempty"`
	Config         string `json:"config,omitempty"`
	ExecutionOrder int    `json:"execution_order"`
	IsAsync        bool   `json:"is_async"`
	IsActive       bool   `json:"is_active"`
}

// WorkflowImportData is an alias for import (same structure as export)
type WorkflowImportData = WorkflowExportData

// WorkflowImportResponse contains the imported workflow and any warnings
type WorkflowImportResponse struct {
	Workflow WorkflowResponse `json:"workflow"`
	Warnings []string         `json:"warnings"`
}
