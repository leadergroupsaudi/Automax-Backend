package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Workflow represents a reusable workflow template
type Workflow struct {
	ID            uuid.UUID `gorm:"type:uuid;primary_key" json:"id"`
	Name          string    `gorm:"not null;size:100;uniqueIndex" json:"name"`
	NameAr        string    `gorm:"size:100" json:"name_ar"`
	Code          string    `gorm:"not null;size:100;uniqueIndex" json:"code"`
	Description   string    `gorm:"size:500" json:"description"`
	DescriptionAr string    `gorm:"size:500" json:"description_ar"`
	Version       int       `gorm:"default:1" json:"version"`
	IsActive      bool      `gorm:"default:true" json:"is_active"`
	IsDefault     bool      `gorm:"default:false" json:"is_default"`
	RecordType    string    `gorm:"size:20;default:'incident'" json:"record_type"` // 'incident', 'request', 'complaint', 'query', 'evidence', 'both', 'all'

	// Matching criteria - stored as JSON arrays, empty/null means matches any value
	// Sources: ["email", "phone", "web", "mobile", "emergency_hotline", etc.]
	Sources string `gorm:"type:text" json:"sources"` // JSON array of source strings
	// Priorities: [1, 2, 3, 4, 5] where 1=Low, 2=Medium, 3=High, 4=Urgent, 5=Critical
	Priorities string `gorm:"type:text" json:"priorities"` // JSON array of priority integers

	// Visual designer metadata (stores canvas layout as JSON)
	CanvasLayout string `gorm:"type:text" json:"canvas_layout"`

	// Form configuration - stores which fields are required (JSON array of field names)
	// e.g., ["description", "classification_id", "priority", "assignee_id", "department_id", "location_id", "due_date", "reporter_name", "reporter_email", "source"]
	RequiredFields string `gorm:"type:text" json:"required_fields"`
	// OptionalFields stores fields that appear in the creation form but are not mandatory
	OptionalFields string `gorm:"type:text" json:"optional_fields"`

	// Relationships
	States          []WorkflowState      `gorm:"foreignKey:WorkflowID" json:"states,omitempty"`
	Transitions     []WorkflowTransition `gorm:"foreignKey:WorkflowID" json:"transitions,omitempty"`
	Classifications []Classification     `gorm:"many2many:workflow_classifications;" json:"classifications,omitempty"`
	Locations       []Location           `gorm:"many2many:workflow_locations;" json:"locations,omitempty"`

	// Role-based convert-to-request permission (many-to-many) - empty = all users can convert
	ConvertToRequestRoles []Role `gorm:"many2many:workflow_convert_to_request_roles;" json:"convert_to_request_roles,omitempty"`

	// Role-based merge permission (many-to-many) - empty = all users can merge
	MergeAllowedRoles []Role `gorm:"many2many:workflow_merge_allowed_roles;" json:"merge_allowed_roles,omitempty"`

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
	ID            uuid.UUID `gorm:"type:uuid;primary_key" json:"id"`
	WorkflowID    uuid.UUID `gorm:"type:uuid;index;not null" json:"workflow_id"`
	Workflow      *Workflow `gorm:"foreignKey:WorkflowID" json:"workflow,omitempty"`
	Name          string    `gorm:"not null;size:100" json:"name"`
	NameAr        string    `gorm:"size:100" json:"name_ar"`
	Code          string    `gorm:"not null;size:50" json:"code"`
	Description   string    `gorm:"size:500" json:"description"`
	DescriptionAr string    `gorm:"size:500" json:"description_ar"`
	StateType     string    `gorm:"size:20;default:'normal'" json:"state_type"` // initial, normal, terminal
	Color         string    `gorm:"size:20;default:'#6366f1'" json:"color"`

	// Visual position on canvas
	PositionX int `gorm:"default:0" json:"position_x"`
	PositionY int `gorm:"default:0" json:"position_y"`

	// SLA Configuration
	SLAHours *int   `gorm:"default:null" json:"sla_hours"`
	SLAUnit  string `gorm:"size:10;default:'hours'" json:"sla_unit"` // minutes, hours, days, months

	// EscalationPolicyID links the escalation policy to fire when this state's SLA is breached.
	EscalationPolicyID *uuid.UUID        `gorm:"type:uuid;index" json:"escalation_policy_id,omitempty"`
	EscalationPolicy   *EscalationPolicy `gorm:"foreignKey:EscalationPolicyID" json:"escalation_policy,omitempty"`

	// Merge Configuration - whether incidents in this state can be merged
	IsMergable bool `gorm:"default:false" json:"is_mergable"`

	// IsAIQA marks this state as requiring AI Quality Assurance.
	// When an incident with is_ai_verified=true reaches a state where is_ai_qa=true,
	// the AI quality monitor will process it.
	IsAIQA bool `gorm:"column:is_ai_qa;default:false" json:"is_ai_qa"`

	// IsReadyToClose marks this state as a "Ready to Close" state that requires a
	IsReadyToClose bool `gorm:"default:false" json:"is_ready_to_close"`
	// IsPartialClose marks this state as a "Partial Close" state that requires a duration selection.
	IsPartialClose bool `gorm:"default:false" json:"is_partial_close"`
	// DurationOptions is a JSON array of duration label strings (e.g. ["1 Day","1 Week"]).When non-empty it overrides the global READY_TO_CLOSE_DURATION_OPTIONS env variable.
	DurationOptions string `gorm:"type:text" json:"duration_options"`

	// Role-based visibility (many-to-many) - empty = visible to all
	ViewableRoles []Role `gorm:"many2many:state_viewable_roles;" json:"viewable_roles,omitempty"`

	// Role-based editability (many-to-many) - empty = no state-level restriction (falls back to incidents:update permission)
	EditableRoles []Role `gorm:"many2many:state_editable_roles;" json:"editable_roles,omitempty"`

	// Creation-time assignment (applied when an incident is placed into this state on creation)
	AssignUserID     *uuid.UUID `gorm:"type:uuid" json:"assign_user_id"`
	AssignUser       *User      `gorm:"foreignKey:AssignUserID" json:"assign_user,omitempty"`
	AssignmentRoles  []Role     `gorm:"many2many:state_assignment_roles;" json:"assignment_roles,omitempty"`
	AutoMatchUser    bool       `gorm:"default:false" json:"auto_match_user"`
	ManualSelectUser bool       `gorm:"default:false" json:"manual_select_user"`

	// Notification template codes sent when a new incident enters this state (initial states only)
	NewIncidentEmailTemplateCode string `gorm:"size:100" json:"new_incident_email_template_code"`
	NewIncidentSMSTemplateCode   string `gorm:"size:100" json:"new_incident_sms_template_code"`

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

// SLADuration converts SLAHours + SLAUnit into a time.Duration.
// Returns 0 if SLAHours is nil or zero.
func (s *WorkflowState) SLADuration() time.Duration {
	if s.SLAHours == nil || *s.SLAHours == 0 {
		return 0
	}
	val := *s.SLAHours
	switch s.SLAUnit {
	case "minutes":
		return time.Duration(val) * time.Minute
	case "days":
		return time.Duration(val) * 24 * time.Hour
	case "months":
		return time.Duration(val) * 30 * 24 * time.Hour
	default: // "hours"
		return time.Duration(val) * time.Hour
	}
}

// SLAAsHours returns the SLA duration expressed as hours (float64).
// Returns 0 if SLAHours is nil or zero.
func (s *WorkflowState) SLAAsHours() float64 {
	return s.SLADuration().Hours()
}

// WorkflowTransition represents a transition between states
type WorkflowTransition struct {
	ID            uuid.UUID `gorm:"type:uuid;primary_key" json:"id"`
	WorkflowID    uuid.UUID `gorm:"type:uuid;index;not null" json:"workflow_id"`
	Workflow      *Workflow `gorm:"foreignKey:WorkflowID" json:"workflow,omitempty"`
	Name          string    `gorm:"not null;size:100" json:"name"`
	NameAr        string    `gorm:"size:100" json:"name_ar"`
	Code          string    `gorm:"not null;size:50" json:"code"`
	Description   string    `gorm:"size:500" json:"description"`
	DescriptionAr string    `gorm:"size:500" json:"description_ar"`

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
	DepartmentTypeFilter string      `gorm:"size:20" json:"department_type_filter"` // "", "internal", or "external"
	// If auto_detect_department=true: find departments matching incident's classification+location
	// department_type_filter restricts which type of departments are shown
	// If one match: auto-assign. If multiple: show selection in transition modal

	// User Assignment
	AssignUserID    *uuid.UUID `gorm:"type:uuid" json:"assign_user_id"`
	AssignUser      *User      `gorm:"foreignKey:AssignUserID" json:"assign_user,omitempty"`
	AssignmentRoles []Role     `gorm:"many2many:transition_assignment_roles;" json:"assignment_roles,omitempty"`
	AutoMatchUser   bool       `gorm:"default:false" json:"auto_match_user"`
	// If auto_match_user=true with assignment_roles:
	//   Find users with any of those roles + matching incident's classification/location/department
	//   If multiple match: assign to ALL matched users
	ManualSelectUser bool `gorm:"default:false" json:"manual_select_user"`
	// If manual_select_user=true: user performing transition manually selects the assignee from dropdown

	// Requirements, Actions and Field Changes
	Requirements []TransitionRequirement `gorm:"foreignKey:TransitionID" json:"requirements,omitempty"`
	Actions      []TransitionAction      `gorm:"foreignKey:TransitionID" json:"actions,omitempty"`
	FieldChanges []TransitionFieldChange `gorm:"foreignKey:TransitionID" json:"field_changes,omitempty"`

	// IsRejection marks this transition as a rejection action.
	// When true, executing this transition will create an IncidentRejectionLog record.
	IsRejection     bool           `gorm:"default:false" json:"is_rejection"`
	IsNotBelong     bool           `gorm:"default:false" json:"is_not_belong"`    // IsNotBelong marks this transition as a "Not Belong" closure action.
	IsMissingInfo   bool           `gorm:"default:false" json:"is_missing_info"`  // IsMissingInfo marks this transition as a "Missing Incident Information" closure action — triggers SMS to citizen.
	IsReopen        bool           `gorm:"default:false" json:"is_reopen"`        // IsReopen marks this transition as a reopen action — used by AI Quality Audit to identify the reopen transition.
	RequireAssignee bool           `gorm:"default:false" json:"require_assignee"` // RequireAssignee: when true, only the assigned user(s) can execute this transition
	IsActive        bool           `gorm:"default:true" json:"is_active"`
	IsFinalClose    bool           `gorm:"default:false" json:"is_final_close"` // IsFinalClose marks this transition as the definitive closure from a Ready-to-Close state — triggers SMS to citizen.
	SortOrder       int            `gorm:"default:0" json:"sort_order"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `gorm:"index" json:"-"`
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

// TransitionFieldChange defines which incident fields the user can modify during a transition
type TransitionFieldChange struct {
	ID           uuid.UUID           `gorm:"type:uuid;primary_key" json:"id"`
	TransitionID uuid.UUID           `gorm:"type:uuid;index;not null" json:"transition_id"`
	Transition   *WorkflowTransition `gorm:"foreignKey:TransitionID" json:"transition,omitempty"`

	// FieldName is the incident field key, e.g. "priority", "department_id", "location_id",
	// "classification_id", "title", "description"
	FieldName            string `gorm:"size:100;not null" json:"field_name"`
	Label                string `gorm:"size:100" json:"label"`                 // Display label shown in the transition modal
	IsRequired           bool   `gorm:"default:false" json:"is_required"`      // Whether the field must be filled
	DepartmentTypeFilter string `gorm:"size:20" json:"department_type_filter"` // For department_id fields: "internal" | "external" | ""
	SortOrder            int    `gorm:"default:0" json:"sort_order"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (f *TransitionFieldChange) BeforeCreate(tx *gorm.DB) error {
	if f.ID == uuid.Nil {
		f.ID = uuid.New()
	}
	return nil
}

// Request/Response types

type WorkflowCreateRequest struct {
	Name              string   `json:"name" validate:"required,min=2,max=100"`
	NameAr            string   `json:"name_ar" validate:"max=100"`
	Code              string   `json:"code" validate:"required,min=2,max=50"`
	Description       string   `json:"description" validate:"max=500"`
	DescriptionAr     string   `json:"description_ar" validate:"max=500"`
	RecordType        string   `json:"record_type" validate:"omitempty,oneof=incident request complaint query evidence both all"`
	Sources           []string `json:"sources"`    // Array of source strings
	Priorities        []int    `json:"priorities"` // Array of priority integers
	ClassificationIDs []string `json:"classification_ids"`
	LocationIDs       []string `json:"location_ids"`
	RequiredFields    []string `json:"required_fields"`
	OptionalFields    []string `json:"optional_fields"`
}

type WorkflowUpdateRequest struct {
	Name                    string   `json:"name" validate:"omitempty,min=2,max=100"`
	NameAr                  string   `json:"name_ar" validate:"max=100"`
	Code                    string   `json:"code" validate:"omitempty,min=2,max=100"`
	Description             string   `json:"description" validate:"max=500"`
	DescriptionAr           string   `json:"description_ar" validate:"max=500"`
	RecordType              *string  `json:"record_type" validate:"omitempty,oneof=incident request complaint query evidence both all"`
	Sources                 []string `json:"sources"`    // Array of source strings (nil means not updating)
	Priorities              []int    `json:"priorities"` // Array of priority integers (nil means not updating)
	IsActive                *bool    `json:"is_active"`
	IsDefault               *bool    `json:"is_default"`
	CanvasLayout            string   `json:"canvas_layout"`
	ClassificationIDs       []string `json:"classification_ids"`
	LocationIDs             []string `json:"location_ids"`
	RequiredFields          []string `json:"required_fields"`
	OptionalFields          []string `json:"optional_fields"`
	ConvertToRequestRoleIDs []string `json:"convert_to_request_role_ids"`
	MergeAllowedRoleIDs     []string `json:"merge_allowed_role_ids"`
}

type WorkflowStateCreateRequest struct {
	Name               string   `json:"name" validate:"required,min=2,max=100"`
	NameAr             string   `json:"name_ar" validate:"max=100"`
	Code               string   `json:"code" validate:"required,min=2,max=50"`
	Description        string   `json:"description" validate:"max=500"`
	DescriptionAr      string   `json:"description_ar" validate:"max=500"`
	StateType          string   `json:"state_type" validate:"omitempty,oneof=initial normal terminal"`
	Color              string   `json:"color" validate:"omitempty,max=20"`
	PositionX          int      `json:"position_x"`
	PositionY          int      `json:"position_y"`
	SLAHours           *int     `json:"sla_hours"`
	SLAUnit            string   `json:"sla_unit" validate:"omitempty,oneof=minutes hours days months"`
	EscalationPolicyID *string  `json:"escalation_policy_id" validate:"omitempty,uuid"`
	IsMergable         bool     `json:"is_mergable"`
	IsAIQA             bool     `json:"is_ai_qa"`
	IsReadyToClose     bool     `json:"is_ready_to_close"`
	IsPartialClose     bool     `json:"is_partial_close"`
	DurationOptions    []string `json:"duration_options"`
	SortOrder          int      `json:"sort_order"`
	ViewableRoleIDs    []string `json:"viewable_role_ids"`
	EditableRoleIDs    []string `json:"editable_role_ids"`
	// Creation-time assignment
	AssignUserID      *string  `json:"assign_user_id" validate:"omitempty,uuid"`
	AssignmentRoleIDs []string `json:"assignment_role_ids"`
	AutoMatchUser     bool     `json:"auto_match_user"`
	ManualSelectUser  bool     `json:"manual_select_user"`
	// New incident notification templates (initial states only)
	NewIncidentEmailTemplateCode string `json:"new_incident_email_template_code"`
	NewIncidentSMSTemplateCode   string `json:"new_incident_sms_template_code"`
}

type WorkflowStateUpdateRequest struct {
	Name               string   `json:"name" validate:"omitempty,min=2,max=100"`
	NameAr             string   `json:"name_ar" validate:"max=100"`
	Code               string   `json:"code" validate:"omitempty,min=2,max=50"`
	Description        string   `json:"description" validate:"max=500"`
	DescriptionAr      string   `json:"description_ar" validate:"max=500"`
	StateType          string   `json:"state_type" validate:"omitempty,oneof=initial normal terminal"`
	Color              string   `json:"color" validate:"omitempty,max=20"`
	PositionX          *int     `json:"position_x"`
	PositionY          *int     `json:"position_y"`
	SLAHours           *int     `json:"sla_hours"`
	SLAUnit            string   `json:"sla_unit" validate:"omitempty,oneof=minutes hours days months"`
	EscalationPolicyID *string  `json:"escalation_policy_id" validate:"omitempty,uuid"`
	IsMergable         *bool    `json:"is_mergable"`
	IsAIQA             *bool    `json:"is_ai_qa"`
	IsReadyToClose     *bool    `json:"is_ready_to_close"`
	IsPartialClose     *bool    `json:"is_partial_close"`
	DurationOptions    []string `json:"duration_options"`
	SortOrder          *int     `json:"sort_order"`
	IsActive           *bool    `json:"is_active"`
	ViewableRoleIDs    []string `json:"viewable_role_ids"`
	EditableRoleIDs    []string `json:"editable_role_ids"`
	// Creation-time assignment
	AssignUserID      *string  `json:"assign_user_id" validate:"omitempty,uuid"`
	AssignmentRoleIDs []string `json:"assignment_role_ids"`
	AutoMatchUser     *bool    `json:"auto_match_user"`
	ManualSelectUser  *bool    `json:"manual_select_user"`
	// New incident notification templates (initial states only)
	NewIncidentEmailTemplateCode *string `json:"new_incident_email_template_code"`
	NewIncidentSMSTemplateCode   *string `json:"new_incident_sms_template_code"`
}

type WorkflowTransitionCreateRequest struct {
	Name            string   `json:"name" validate:"required,min=2,max=100"`
	NameAr          string   `json:"name_ar" validate:"max=100"`
	Code            string   `json:"code" validate:"required,min=2,max=50"`
	Description     string   `json:"description" validate:"max=500"`
	DescriptionAr   string   `json:"description_ar" validate:"max=500"`
	FromStateID     string   `json:"from_state_id" validate:"required,uuid"`
	ToStateID       string   `json:"to_state_id" validate:"required,uuid"`
	RoleIDs         []string `json:"role_ids"`
	SortOrder       int      `json:"sort_order"`
	IsRejection     bool     `json:"is_rejection"`
	IsNotBelong     bool     `json:"is_not_belong"`
	IsMissingInfo   bool     `json:"is_missing_info"`
	IsReopen        bool     `json:"is_reopen"`
	IsFinalClose    bool     `json:"is_final_close"`
	RequireAssignee bool     `json:"require_assignee"`

	// Department Assignment
	AssignDepartmentID   *string `json:"assign_department_id" validate:"omitempty"`
	AutoDetectDepartment bool    `json:"auto_detect_department"`
	DepartmentTypeFilter string  `json:"department_type_filter" validate:"omitempty,oneof=internal external"`

	// User Assignment
	AssignUserID      *string  `json:"assign_user_id" validate:"omitempty,uuid"`
	AssignmentRoleIDs []string `json:"assignment_role_ids"`
	AutoMatchUser     bool     `json:"auto_match_user"`
	ManualSelectUser  bool     `json:"manual_select_user"`
}

type WorkflowTransitionUpdateRequest struct {
	Name            string   `json:"name" validate:"omitempty,min=2,max=100"`
	NameAr          string   `json:"name_ar" validate:"max=100"`
	Code            string   `json:"code" validate:"omitempty,min=2,max=50"`
	Description     string   `json:"description" validate:"max=500"`
	DescriptionAr   string   `json:"description_ar" validate:"max=500"`
	FromStateID     string   `json:"from_state_id" validate:"omitempty,uuid"`
	ToStateID       string   `json:"to_state_id" validate:"omitempty,uuid"`
	RoleIDs         []string `json:"role_ids"`
	SortOrder       *int     `json:"sort_order"`
	IsActive        *bool    `json:"is_active"`
	IsRejection     *bool    `json:"is_rejection"`
	IsNotBelong     *bool    `json:"is_not_belong"`
	IsMissingInfo   *bool    `json:"is_missing_info"`
	IsReopen        *bool    `json:"is_reopen"`
	IsFinalClose    *bool    `json:"is_final_close"`
	RequireAssignee *bool    `json:"require_assignee"`

	// Department Assignment
	AssignDepartmentID   *string `json:"assign_department_id" validate:"omitempty,uuid"`
	AutoDetectDepartment *bool   `json:"auto_detect_department"`
	DepartmentTypeFilter *string `json:"department_type_filter" validate:"omitempty,oneof=internal external"`

	// User Assignment
	AssignUserID      *string  `json:"assign_user_id" validate:"omitempty,uuid"`
	AssignmentRoleIDs []string `json:"assignment_role_ids"`
	AutoMatchUser     *bool    `json:"auto_match_user"`
	ManualSelectUser  *bool    `json:"manual_select_user"`
}

type TransitionRequirementRequest struct {
	RequirementType string `json:"requirement_type" validate:"required,oneof=comment attachment feedback rating field_value"`
	FieldName       string `json:"field_name"`
	FieldValue      string `json:"field_value"`
	IsMandatory     *bool  `json:"is_mandatory"`
	ErrorMessage    string `json:"error_message"`
}

type TransitionActionRequest struct {
	ActionType     string `json:"action_type" validate:"required,oneof=email field_update webhook notification sms"`
	Name           string `json:"name" validate:"required,min=2,max=100"`
	Description    string `json:"description"`
	Config         string `json:"config"`
	ExecutionOrder int    `json:"execution_order"`
	IsAsync        bool   `json:"is_async"`
	IsActive       bool   `json:"is_active"`
}

type TransitionFieldChangeRequest struct {
	FieldName            string `json:"field_name" validate:"required"`
	Label                string `json:"label"`
	IsRequired           bool   `json:"is_required"`
	DepartmentTypeFilter string `json:"department_type_filter" validate:"omitempty,oneof=internal external"`
	SortOrder            int    `json:"sort_order"`
}

// Workflow matching request - used by mobile apps and other clients
type WorkflowMatchRequest struct {
	ClassificationID string `json:"classification_id"`
	LocationID       string `json:"location_id"`
	Source           string `json:"source"`
	Priority         int    `json:"priority"`
	RecordType       string `json:"record_type"` // "incident", "request", "complaint", "query" — required for matching
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
	RecordType     *string                   `json:"record_type,omitempty"`
	RequiredFields []string                  `json:"required_fields"`
	OptionalFields []string                  `json:"optional_fields"`
	FormFields     []IncidentFormFieldConfig `json:"form_fields"`
	InitialStateID *string                   `json:"initial_state_id,omitempty"`
	InitialState   *string                   `json:"initial_state,omitempty"`
}

// Response types

type WorkflowResponse struct {
	ID                    uuid.UUID                    `json:"id"`
	Name                  string                       `json:"name"`
	NameAr                string                       `json:"name_ar,omitempty"`
	Code                  string                       `json:"code"`
	Description           string                       `json:"description"`
	DescriptionAr         string                       `json:"description_ar,omitempty"`
	Version               int                          `json:"version"`
	IsActive              bool                         `json:"is_active"`
	IsDefault             bool                         `json:"is_default"`
	RecordType            string                       `json:"record_type"`
	Sources               []string                     `json:"sources"`    // Array of sources
	Priorities            []int                        `json:"priorities"` // Array of priorities
	CanvasLayout          string                       `json:"canvas_layout,omitempty"`
	RequiredFields        []string                     `json:"required_fields"`
	OptionalFields        []string                     `json:"optional_fields"`
	States                []WorkflowStateResponse      `json:"states,omitempty"`
	Transitions           []WorkflowTransitionResponse `json:"transitions,omitempty"`
	Classifications       []ClassificationResponse     `json:"classifications,omitempty"`
	Locations             []LocationResponse           `json:"locations,omitempty"`
	ConvertToRequestRoles []RoleResponse               `json:"convert_to_request_roles,omitempty"`
	MergeAllowedRoles     []RoleResponse               `json:"merge_allowed_roles,omitempty"`
	StatesCount           int                          `json:"states_count"`
	TransitionsCount      int                          `json:"transitions_count"`
	CreatedBy             *UserResponse                `json:"created_by,omitempty"`
	CreatedAt             time.Time                    `json:"created_at"`
	UpdatedAt             time.Time                    `json:"updated_at"`
}

type WorkflowStateResponse struct {
	ID                 uuid.UUID                 `json:"id"`
	WorkflowID         uuid.UUID                 `json:"workflow_id"`
	Name               string                    `json:"name"`
	NameAr             string                    `json:"name_ar,omitempty"`
	Code               string                    `json:"code"`
	Description        string                    `json:"description"`
	DescriptionAr      string                    `json:"description_ar,omitempty"`
	StateType          string                    `json:"state_type"`
	Color              string                    `json:"color"`
	PositionX          int                       `json:"position_x"`
	PositionY          int                       `json:"position_y"`
	SLAHours           *int                      `json:"sla_hours"`
	SLAUnit            string                    `json:"sla_unit"`
	EscalationPolicyID *uuid.UUID                `json:"escalation_policy_id,omitempty"`
	EscalationPolicy   *EscalationPolicyResponse `json:"escalation_policy,omitempty"`
	IsMergable         bool                      `json:"is_mergable"`
	IsAIQA             bool                      `json:"is_ai_qa"`
	IsReadyToClose     bool                      `json:"is_ready_to_close"`
	IsPartialClose     bool                      `json:"is_partial_close"`
	DurationOptions    []string                  `json:"duration_options,omitempty"`
	SortOrder          int                       `json:"sort_order"`
	IsActive           bool                      `json:"is_active"`
	ViewableRoles      []RoleResponse            `json:"viewable_roles,omitempty"`
	EditableRoles      []RoleResponse            `json:"editable_roles,omitempty"`
	// Creation-time assignment
	AssignUserID     *uuid.UUID     `json:"assign_user_id,omitempty"`
	AssignUser       *UserResponse  `json:"assign_user,omitempty"`
	AssignmentRoles  []RoleResponse `json:"assignment_roles,omitempty"`
	AutoMatchUser    bool           `json:"auto_match_user"`
	ManualSelectUser bool           `json:"manual_select_user"`
	// New incident notification templates (initial states only)
	NewIncidentEmailTemplateCode string    `json:"new_incident_email_template_code,omitempty"`
	NewIncidentSMSTemplateCode   string    `json:"new_incident_sms_template_code,omitempty"`
	CreatedAt                    time.Time `json:"created_at"`
}

type WorkflowTransitionResponse struct {
	ID            uuid.UUID              `json:"id"`
	WorkflowID    uuid.UUID              `json:"workflow_id"`
	Name          string                 `json:"name"`
	NameAr        string                 `json:"name_ar,omitempty"`
	Code          string                 `json:"code"`
	Description   string                 `json:"description"`
	DescriptionAr string                 `json:"description_ar,omitempty"`
	FromStateID   uuid.UUID              `json:"from_state_id"`
	FromState     *WorkflowStateResponse `json:"from_state,omitempty"`
	ToStateID     uuid.UUID              `json:"to_state_id"`
	ToState       *WorkflowStateResponse `json:"to_state,omitempty"`
	AllowedRoles  []RoleResponse         `json:"allowed_roles,omitempty"`

	// Department Assignment
	AssignDepartmentID   *uuid.UUID          `json:"assign_department_id,omitempty"`
	AssignDepartment     *DepartmentResponse `json:"assign_department,omitempty"`
	AutoDetectDepartment bool                `json:"auto_detect_department"`

	// User Assignment
	AssignUserID     *uuid.UUID     `json:"assign_user_id,omitempty"`
	AssignUser       *UserResponse  `json:"assign_user,omitempty"`
	AssignmentRoles  []RoleResponse `json:"assignment_roles,omitempty"`
	AutoMatchUser    bool           `json:"auto_match_user"`
	ManualSelectUser bool           `json:"manual_select_user"`

	Requirements    []TransitionRequirementResponse `json:"requirements,omitempty"`
	Actions         []TransitionActionResponse      `json:"actions,omitempty"`
	FieldChanges    []TransitionFieldChangeResponse `json:"field_changes,omitempty"`
	IsRejection     bool                            `json:"is_rejection"`
	IsNotBelong     bool                            `json:"is_not_belong"`
	IsMissingInfo   bool                            `json:"is_missing_info"`
	IsReopen        bool                            `json:"is_reopen"`
	IsFinalClose    bool                            `json:"is_final_close"`
	RequireAssignee bool                            `json:"require_assignee"`
	IsActive        bool                            `json:"is_active"`
	SortOrder       int                             `json:"sort_order"`
	CreatedAt       time.Time                       `json:"created_at"`
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

type TransitionFieldChangeResponse struct {
	ID                   uuid.UUID `json:"id"`
	TransitionID         uuid.UUID `json:"transition_id"`
	FieldName            string    `json:"field_name"`
	Label                string    `json:"label"`
	IsRequired           bool      `json:"is_required"`
	DepartmentTypeFilter string    `json:"department_type_filter"`
	SortOrder            int       `json:"sort_order"`
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

	// Parse OptionalFields JSON string to array
	var optionalFields []string
	if w.OptionalFields != "" {
		json.Unmarshal([]byte(w.OptionalFields), &optionalFields)
	}
	if optionalFields == nil {
		optionalFields = []string{}
	}

	// Parse Sources JSON string to array
	var sources []string
	if w.Sources != "" {
		json.Unmarshal([]byte(w.Sources), &sources)
	}
	if sources == nil {
		sources = []string{}
	}

	// Parse Priorities JSON string to array
	var priorities []int
	if w.Priorities != "" {
		json.Unmarshal([]byte(w.Priorities), &priorities)
	}
	if priorities == nil {
		priorities = []int{}
	}

	resp := WorkflowResponse{
		ID:               w.ID,
		Name:             w.Name,
		NameAr:           w.NameAr,
		Code:             w.Code,
		Description:      w.Description,
		DescriptionAr:    w.DescriptionAr,
		Version:          w.Version,
		IsActive:         w.IsActive,
		IsDefault:        w.IsDefault,
		RecordType:       w.RecordType,
		Sources:          sources,
		Priorities:       priorities,
		CanvasLayout:     w.CanvasLayout,
		RequiredFields:   requiredFields,
		OptionalFields:   optionalFields,
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

	if len(w.Locations) > 0 {
		resp.Locations = make([]LocationResponse, len(w.Locations))
		for i, loc := range w.Locations {
			resp.Locations[i] = ToLocationResponse(&loc)
		}
	}

	if len(w.ConvertToRequestRoles) > 0 {
		resp.ConvertToRequestRoles = make([]RoleResponse, len(w.ConvertToRequestRoles))
		for i, r := range w.ConvertToRequestRoles {
			resp.ConvertToRequestRoles[i] = ToRoleResponse(&r)
		}
	}

	if len(w.MergeAllowedRoles) > 0 {
		resp.MergeAllowedRoles = make([]RoleResponse, len(w.MergeAllowedRoles))
		for i, r := range w.MergeAllowedRoles {
			resp.MergeAllowedRoles[i] = ToRoleResponse(&r)
		}
	}

	return resp
}

func ToWorkflowStateResponse(s *WorkflowState) WorkflowStateResponse {
	resp := WorkflowStateResponse{
		ID:                 s.ID,
		WorkflowID:         s.WorkflowID,
		Name:               s.Name,
		NameAr:             s.NameAr,
		Code:               s.Code,
		Description:        s.Description,
		DescriptionAr:      s.DescriptionAr,
		StateType:          s.StateType,
		Color:              s.Color,
		PositionX:          s.PositionX,
		PositionY:          s.PositionY,
		SLAHours:           s.SLAHours,
		SLAUnit:            s.SLAUnit,
		EscalationPolicyID: s.EscalationPolicyID,
		IsMergable:         s.IsMergable,
		IsAIQA:             s.IsAIQA,
		IsReadyToClose:     s.IsReadyToClose,
		IsPartialClose:     s.IsPartialClose,
		SortOrder:          s.SortOrder,
		IsActive:           s.IsActive,
		CreatedAt:          s.CreatedAt,
	}
	if s.EscalationPolicy != nil {
		r := ToEscalationPolicyResponse(s.EscalationPolicy)
		resp.EscalationPolicy = &r
	}

	// Parse DurationOptions from JSON string
	if s.DurationOptions != "" {
		var opts []string
		if err := json.Unmarshal([]byte(s.DurationOptions), &opts); err == nil {
			resp.DurationOptions = opts
		}
	}

	if len(s.ViewableRoles) > 0 {
		resp.ViewableRoles = make([]RoleResponse, len(s.ViewableRoles))
		for i, r := range s.ViewableRoles {
			resp.ViewableRoles[i] = ToRoleResponse(&r)
		}
	}

	if len(s.EditableRoles) > 0 {
		resp.EditableRoles = make([]RoleResponse, len(s.EditableRoles))
		for i, r := range s.EditableRoles {
			resp.EditableRoles[i] = ToRoleResponse(&r)
		}
	}

	resp.AssignUserID = s.AssignUserID
	if s.AssignUser != nil {
		u := ToUserResponse(s.AssignUser)
		resp.AssignUser = &u
	}
	resp.AutoMatchUser = s.AutoMatchUser
	resp.ManualSelectUser = s.ManualSelectUser
	if len(s.AssignmentRoles) > 0 {
		resp.AssignmentRoles = make([]RoleResponse, len(s.AssignmentRoles))
		for i, r := range s.AssignmentRoles {
			resp.AssignmentRoles[i] = ToRoleResponse(&r)
		}
	}

	resp.NewIncidentEmailTemplateCode = s.NewIncidentEmailTemplateCode
	resp.NewIncidentSMSTemplateCode = s.NewIncidentSMSTemplateCode

	return resp
}

func ToWorkflowTransitionResponse(t *WorkflowTransition) WorkflowTransitionResponse {
	resp := WorkflowTransitionResponse{
		ID:                   t.ID,
		WorkflowID:           t.WorkflowID,
		Name:                 t.Name,
		NameAr:               t.NameAr,
		Code:                 t.Code,
		Description:          t.Description,
		DescriptionAr:        t.DescriptionAr,
		FromStateID:          t.FromStateID,
		ToStateID:            t.ToStateID,
		AssignDepartmentID:   t.AssignDepartmentID,
		AutoDetectDepartment: t.AutoDetectDepartment,
		AssignUserID:         t.AssignUserID,
		AutoMatchUser:        t.AutoMatchUser,
		ManualSelectUser:     t.ManualSelectUser,
		IsRejection:          t.IsRejection,
		IsNotBelong:          t.IsNotBelong,
		IsMissingInfo:        t.IsMissingInfo,
		IsReopen:             t.IsReopen,
		IsFinalClose:         t.IsFinalClose,
		RequireAssignee:      t.RequireAssignee,
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

	if len(t.AssignmentRoles) > 0 {
		resp.AssignmentRoles = make([]RoleResponse, len(t.AssignmentRoles))
		for i, r := range t.AssignmentRoles {
			resp.AssignmentRoles[i] = ToRoleResponse(&r)
		}
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

	if len(t.FieldChanges) > 0 {
		resp.FieldChanges = make([]TransitionFieldChangeResponse, len(t.FieldChanges))
		for i, f := range t.FieldChanges {
			resp.FieldChanges[i] = ToTransitionFieldChangeResponse(&f)
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

func ToTransitionFieldChangeResponse(f *TransitionFieldChange) TransitionFieldChangeResponse {
	return TransitionFieldChangeResponse{
		ID:                   f.ID,
		TransitionID:         f.TransitionID,
		FieldName:            f.FieldName,
		Label:                f.Label,
		IsRequired:           f.IsRequired,
		DepartmentTypeFilter: f.DepartmentTypeFilter,
		SortOrder:            f.SortOrder,
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
	ExportVersion string                `json:"export_version" validate:"required"`
	ExportedAt    string                `json:"exported_at" validate:"required,datetime=2006-01-02T15:04:05Z07:00"`
	Workflow      WorkflowExportContent `json:"workflow" validate:"required"`
}

// WorkflowExportContent contains workflow data with codes instead of IDs
type WorkflowExportContent struct {
	Name        string `json:"name"        validate:"required,min=2,max=100"`
	Code        string `json:"code"        validate:"required,min=2,max=50"`
	Description string `json:"description" validate:"omitempty,max=500"`

	// FIX: oneof belongs on the string itself, not a []string slice
	RecordType string   `json:"record_type,omitempty" validate:"omitempty,oneof=incident request complaint query evidence both all"`
	Sources    []string `json:"sources,omitempty"    validate:"omitempty,dive,min=1"`

	// FIX: []int can't use oneof with string values — validate the range differently
	Priorities []int `json:"priorities,omitempty" validate:"omitempty,dive,min=1,max=5"`

	// FIX: dive into slices for per-element validation
	RequiredFields  []string                   `json:"required_fields,omitempty"   validate:"omitempty,dive,min=1"`
	States          []WorkflowStateExport      `json:"states,omitempty"            validate:"omitempty,dive"`
	Transitions     []WorkflowTransitionExport `json:"transitions,omitempty"       validate:"omitempty,dive"`
	Classifications []CodeNamePair             `json:"classifications,omitempty"   validate:"omitempty,dive"`

	ConvertToRequestRoles []CodeNamePair `json:"convert_to_request_roles,omitempty" validate:"omitempty,dive"`
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
	SLAUnit       string         `json:"sla_unit,omitempty"`
	SortOrder     int            `json:"sort_order"`
	ViewableRoles []CodeNamePair `json:"viewable_roles,omitempty"`
	EditableRoles []CodeNamePair `json:"editable_roles,omitempty"`
}

// WorkflowTransitionExport represents a transition with codes and nested requirements/actions
type WorkflowTransitionExport struct {
	Name                 string                        `json:"name"`
	Code                 string                        `json:"code"`
	Description          string                        `json:"description"`
	FromStateCode        string                        `json:"from_state_code"`
	ToStateCode          string                        `json:"to_state_code"`
	AllowedRoles         []CodeNamePair                `json:"allowed_roles,omitempty"`
	AssignDepartment     *CodeNamePair                 `json:"assign_department,omitempty"`
	AutoDetectDepartment bool                          `json:"auto_detect_department"`
	AssignUser           *CodeNamePair                 `json:"assign_user,omitempty"`
	AssignmentRoles      []CodeNamePair                `json:"assignment_roles,omitempty"`
	AutoMatchUser        bool                          `json:"auto_match_user"`
	ManualSelectUser     bool                          `json:"manual_select_user"`
	Requirements         []TransitionRequirementExport `json:"requirements,omitempty"`
	Actions              []TransitionActionExport      `json:"actions,omitempty"`
	SortOrder            int                           `json:"sort_order"`
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
