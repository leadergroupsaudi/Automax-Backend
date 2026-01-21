package models

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/automax/backend/internal/storage"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Incident represents an actual incident or request record
type Incident struct {
	ID             uuid.UUID `gorm:"type:uuid;primary_key" json:"id"`
	IncidentNumber string    `gorm:"size:50;uniqueIndex;not null" json:"incident_number"`
	Title          string    `gorm:"size:200;not null" json:"title"`
	Description    string    `gorm:"type:text" json:"description"`

	// Record Type: 'incident', 'request', or 'complaint'
	RecordType       string     `gorm:"size:20;default:'incident';index" json:"record_type"`
	SourceIncidentID *uuid.UUID `gorm:"type:uuid;index" json:"source_incident_id"`
	SourceIncident   *Incident  `gorm:"foreignKey:SourceIncidentID" json:"source_incident,omitempty"`

	// Classification
	ClassificationID *uuid.UUID      `gorm:"type:uuid;index" json:"classification_id"`
	Classification   *Classification `gorm:"foreignKey:ClassificationID" json:"classification,omitempty"`

	// Workflow State
	WorkflowID     uuid.UUID      `gorm:"type:uuid;index;not null" json:"workflow_id"`
	Workflow       *Workflow      `gorm:"foreignKey:WorkflowID" json:"workflow,omitempty"`
	CurrentStateID uuid.UUID      `gorm:"type:uuid;index;not null" json:"current_state_id"`
	CurrentState   *WorkflowState `gorm:"foreignKey:CurrentStateID" json:"current_state,omitempty"`

	// Dynamic Attributes from Lookup
	LookupValues []LookupValue `gorm:"many2many:incident_lookup_values;" json:"lookup_values,omitempty"`

	// Assignment
	AssigneeID   *uuid.UUID  `gorm:"type:uuid;index" json:"assignee_id"`
	Assignee     *User       `gorm:"foreignKey:AssigneeID" json:"assignee,omitempty"`
	DepartmentID *uuid.UUID  `gorm:"type:uuid;index" json:"department_id"`
	Department   *Department `gorm:"foreignKey:DepartmentID" json:"department,omitempty"`

	// Location
	LocationID *uuid.UUID `gorm:"type:uuid;index" json:"location_id"`
	Location   *Location  `gorm:"foreignKey:LocationID" json:"location,omitempty"`

	// Geolocation (independent of Location reference)
	Latitude  *float64 `gorm:"type:decimal(10,8)" json:"latitude"`
	Longitude *float64 `gorm:"type:decimal(11,8)" json:"longitude"`

	// Dates
	DueDate    *time.Time `json:"due_date"`
	ResolvedAt *time.Time `json:"resolved_at"`
	ClosedAt   *time.Time `json:"closed_at"`

	// SLA Tracking
	SLABreached bool       `gorm:"default:false" json:"sla_breached"`
	SLADeadline *time.Time `json:"sla_deadline"`

	// Reporter
	ReporterID    *uuid.UUID `gorm:"type:uuid;index" json:"reporter_id"`
	Reporter      *User      `gorm:"foreignKey:ReporterID" json:"reporter,omitempty"`
	ReporterEmail string     `gorm:"size:100" json:"reporter_email"`
	ReporterName  string     `gorm:"size:200" json:"reporter_name"`

	// Complaint-specific fields
	Channel         string `gorm:"size:100" json:"channel"`
	CreatedByName   string `gorm:"size:255" json:"created_by_name"`
	CreatedByMobile string `gorm:"size:50" json:"created_by_mobile"`
	EvaluationCount int    `gorm:"default:0" json:"evaluation_count"`

	// Custom Fields (JSON)
	CustomFields string `gorm:"type:text" json:"custom_fields"`

	// Multiple Assignees (many-to-many)
	Assignees []User `gorm:"many2many:incident_assignees;" json:"assignees,omitempty"`

	// Related records
	Comments          []IncidentComment           `gorm:"foreignKey:IncidentID" json:"comments,omitempty"`
	Attachments       []IncidentAttachment        `gorm:"foreignKey:IncidentID" json:"attachments,omitempty"`
	TransitionHistory []IncidentTransitionHistory `gorm:"foreignKey:IncidentID" json:"transition_history,omitempty"`
	Revisions         []IncidentRevision          `gorm:"foreignKey:IncidentID" json:"revisions,omitempty"`

	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

func (i *Incident) BeforeCreate(tx *gorm.DB) error {
	if i.ID == uuid.Nil {
		i.ID = uuid.New()
	}
	return nil
}

// IncidentComment represents a comment on an incident
type IncidentComment struct {
	ID         uuid.UUID `gorm:"type:uuid;primary_key" json:"id"`
	IncidentID uuid.UUID `gorm:"type:uuid;index;not null" json:"incident_id"`
	Incident   *Incident `gorm:"foreignKey:IncidentID" json:"incident,omitempty"`

	AuthorID uuid.UUID `gorm:"type:uuid;index;not null" json:"author_id"`
	Author   *User     `gorm:"foreignKey:AuthorID" json:"author,omitempty"`

	Content    string `gorm:"type:text;not null" json:"content"`
	IsInternal bool   `gorm:"default:false" json:"is_internal"` // Internal vs public comment

	// Link to transition if comment was part of a transition
	TransitionHistoryID *uuid.UUID `gorm:"type:uuid" json:"transition_history_id"`

	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

func (c *IncidentComment) BeforeCreate(tx *gorm.DB) error {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	return nil
}

// IncidentAttachment represents a file attached to an incident
type IncidentAttachment struct {
	ID         uuid.UUID `gorm:"type:uuid;primary_key" json:"id"`
	IncidentID uuid.UUID `gorm:"type:uuid;index;not null" json:"incident_id"`
	Incident   *Incident `gorm:"foreignKey:IncidentID" json:"incident,omitempty"`

	FileName string `gorm:"size:255;not null" json:"file_name"`
	FileSize int64  `json:"file_size"`
	MimeType string `gorm:"size:100" json:"mime_type"`
	FilePath string `gorm:"size:500;not null" json:"file_path"`

	UploadedByID uuid.UUID `gorm:"type:uuid;index;not null" json:"uploaded_by_id"`
	UploadedBy   *User     `gorm:"foreignKey:UploadedByID" json:"uploaded_by,omitempty"`

	// Link to transition if attachment was part of a transition
	TransitionHistoryID *uuid.UUID `gorm:"type:uuid" json:"transition_history_id"`

	CreatedAt time.Time      `json:"created_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

func (a *IncidentAttachment) BeforeCreate(tx *gorm.DB) error {
	if a.ID == uuid.Nil {
		a.ID = uuid.New()
	}
	return nil
}

// IncidentFeedback represents feedback collected during a transition
type IncidentFeedback struct {
	ID         uuid.UUID `gorm:"type:uuid;primary_key" json:"id"`
	IncidentID uuid.UUID `gorm:"type:uuid;index;not null" json:"incident_id"`
	Incident   *Incident `gorm:"foreignKey:IncidentID" json:"incident,omitempty"`

	Rating  int    `gorm:"not null" json:"rating"` // 1-5 stars
	Comment string `gorm:"type:text" json:"comment"`

	CreatedByID uuid.UUID `gorm:"type:uuid;index;not null" json:"created_by_id"`
	CreatedBy   *User     `gorm:"foreignKey:CreatedByID" json:"created_by,omitempty"`

	// Link to transition if feedback was part of a transition
	TransitionHistoryID *uuid.UUID `gorm:"type:uuid" json:"transition_history_id"`

	CreatedAt time.Time `json:"created_at"`
}

func (f *IncidentFeedback) BeforeCreate(tx *gorm.DB) error {
	if f.ID == uuid.Nil {
		f.ID = uuid.New()
	}
	return nil
}

// IncidentTransitionHistory records all state transitions for an incident
type IncidentTransitionHistory struct {
	ID         uuid.UUID `gorm:"type:uuid;primary_key" json:"id"`
	IncidentID uuid.UUID `gorm:"type:uuid;index;not null" json:"incident_id"`
	Incident   *Incident `gorm:"foreignKey:IncidentID" json:"incident,omitempty"`

	TransitionID uuid.UUID           `gorm:"type:uuid;index;not null" json:"transition_id"`
	Transition   *WorkflowTransition `gorm:"foreignKey:TransitionID" json:"transition,omitempty"`

	FromStateID uuid.UUID      `gorm:"type:uuid;index;not null" json:"from_state_id"`
	FromState   *WorkflowState `gorm:"foreignKey:FromStateID" json:"from_state,omitempty"`
	ToStateID   uuid.UUID      `gorm:"type:uuid;index;not null" json:"to_state_id"`
	ToState     *WorkflowState `gorm:"foreignKey:ToStateID" json:"to_state,omitempty"`

	PerformedByID uuid.UUID `gorm:"type:uuid;index;not null" json:"performed_by_id"`
	PerformedBy   *User     `gorm:"foreignKey:PerformedByID" json:"performed_by,omitempty"`

	Comment string `gorm:"type:text" json:"comment"`

	// Snapshot of field changes (JSON)
	OldValues string `gorm:"type:text" json:"old_values"`
	NewValues string `gorm:"type:text" json:"new_values"`

	// Action execution results (JSON)
	ActionResults string `gorm:"type:text" json:"action_results"`

	TransitionedAt time.Time `gorm:"index" json:"transitioned_at"`
	CreatedAt      time.Time `json:"created_at"`
}

func (h *IncidentTransitionHistory) BeforeCreate(tx *gorm.DB) error {
	if h.ID == uuid.Nil {
		h.ID = uuid.New()
	}
	return nil
}

// IncidentRevisionActionType represents the type of revision action
type IncidentRevisionActionType string

const (
	RevisionActionFieldChange       IncidentRevisionActionType = "field_change"
	RevisionActionCommentAdded      IncidentRevisionActionType = "comment_added"
	RevisionActionCommentModified   IncidentRevisionActionType = "comment_modified"
	RevisionActionCommentDeleted    IncidentRevisionActionType = "comment_deleted"
	RevisionActionAttachmentAdded   IncidentRevisionActionType = "attachment_added"
	RevisionActionAttachmentRemoved IncidentRevisionActionType = "attachment_removed"
	RevisionActionAssigneeChanged   IncidentRevisionActionType = "assignee_changed"
	RevisionActionStatusChanged     IncidentRevisionActionType = "status_changed"
	RevisionActionCreated           IncidentRevisionActionType = "created"
)

// IncidentRevision records detailed change history for an incident
type IncidentRevision struct {
	ID             uuid.UUID `gorm:"type:uuid;primary_key" json:"id"`
	IncidentID     uuid.UUID `gorm:"type:uuid;index;not null" json:"incident_id"`
	Incident       *Incident `gorm:"foreignKey:IncidentID" json:"incident,omitempty"`
	RevisionNumber int       `gorm:"not null" json:"revision_number"`

	ActionType        IncidentRevisionActionType `gorm:"size:50;not null;index" json:"action_type"`
	ActionDescription string                     `gorm:"type:text;not null" json:"action_description"`

	// JSON array of field changes
	Changes string `gorm:"type:text" json:"changes"`

	// Who made the change
	PerformedByID    uuid.UUID `gorm:"type:uuid;index;not null" json:"performed_by_id"`
	PerformedBy      *User     `gorm:"foreignKey:PerformedByID" json:"performed_by,omitempty"`
	PerformedByRoles string    `gorm:"type:text" json:"performed_by_roles"` // JSON array of role names
	PerformedByPhone string    `gorm:"size:50" json:"performed_by_phone"`

	// Optional links to related entities
	CommentID           *uuid.UUID `gorm:"type:uuid" json:"comment_id"`
	AttachmentID        *uuid.UUID `gorm:"type:uuid" json:"attachment_id"`
	TransitionHistoryID *uuid.UUID `gorm:"type:uuid" json:"transition_history_id"`

	CreatedAt time.Time `gorm:"index" json:"created_at"`
}

func (r *IncidentRevision) BeforeCreate(tx *gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	return nil
}

// IncidentFieldChange represents a single field change
type IncidentFieldChange struct {
	FieldName  string  `json:"field_name"`
	FieldLabel string  `json:"field_label"`
	OldValue   *string `json:"old_value"`
	NewValue   *string `json:"new_value"`
}

// IncidentRevisionFilter for querying revisions
type IncidentRevisionFilter struct {
	IncidentID    uuid.UUID                   `json:"incident_id"`
	ActionType    *IncidentRevisionActionType `json:"action_type"`
	PerformedByID *uuid.UUID                  `json:"performed_by_id"`
	StartDate     *time.Time                  `json:"start_date"`
	EndDate       *time.Time                  `json:"end_date"`
	Page          int                         `json:"page"`
	Limit         int                         `json:"limit"`
}

// Request types

type IncidentCreateRequest struct {
	Title            string   `json:"title" validate:"required,min=5,max=200"`
	Description      string   `json:"description"`
	ClassificationID *string  `json:"classification_id" validate:"omitempty,uuid"`
	WorkflowID       string   `json:"workflow_id" validate:"required,uuid"`
	AssigneeID       *string  `json:"assignee_id" validate:"omitempty,uuid"`
	DepartmentID     *string  `json:"department_id" validate:"omitempty,uuid"`
	LocationID       *string  `json:"location_id" validate:"omitempty,uuid"`
	Latitude         *float64 `json:"latitude" validate:"omitempty,min=-90,max=90"`
	Longitude        *float64 `json:"longitude" validate:"omitempty,min=-180,max=180"`
	DueDate          *string  `json:"due_date"`
	ReporterEmail    string   `json:"reporter_email" validate:"omitempty,email"`
	ReporterName     string   `json:"reporter_name" validate:"omitempty,max=200"`
	CustomFields     string   `json:"custom_fields"`
	LookupValueIDs   []string `json:"lookup_value_ids" validate:"omitempty,dive,uuid"`
}

type IncidentUpdateRequest struct {
	Title            string   `json:"title" validate:"omitempty,min=5,max=200"`
	Description      string   `json:"description"`
	ClassificationID *string  `json:"classification_id" validate:"omitempty,uuid"`
	AssigneeID       *string  `json:"assignee_id" validate:"omitempty,uuid"`
	DepartmentID     *string  `json:"department_id" validate:"omitempty,uuid"`
	LocationID       *string  `json:"location_id" validate:"omitempty,uuid"`
	Latitude         *float64 `json:"latitude" validate:"omitempty,min=-90,max=90"`
	Longitude        *float64 `json:"longitude" validate:"omitempty,min=-180,max=180"`
	DueDate          *string  `json:"due_date"`
	CustomFields     string   `json:"custom_fields"`
	LookupValueIDs   []string `json:"lookup_value_ids" validate:"omitempty,dive,uuid"`
}

type IncidentTransitionRequest struct {
	TransitionID string   `json:"transition_id" validate:"required,uuid"`
	Comment      string   `json:"comment"`
	Attachments  []string `json:"attachments"` // attachment IDs to link to this transition

	// Feedback (collected during transition if required)
	Feedback *IncidentFeedbackRequest `json:"feedback"`

	// Assignment overrides (used when auto-detect finds multiple matches)
	DepartmentID *string `json:"department_id" validate:"omitempty,uuid"`
	UserID       *string `json:"user_id" validate:"omitempty,uuid"`
}

type IncidentFeedbackRequest struct {
	Rating  int    `json:"rating" validate:"required,min=1,max=5"`
	Comment string `json:"comment"`
}

type IncidentCommentRequest struct {
	Content    string `json:"content" validate:"required,min=1"`
	IsInternal bool   `json:"is_internal"`
}

// CreateComplaintRequest for creating a new complaint
type CreateComplaintRequest struct {
	Title            string   `json:"title" validate:"required,min=5,max=200"`
	Description      string   `json:"description"`
	ClassificationID string   `json:"classification_id" validate:"required,uuid"`
	WorkflowID       string   `json:"workflow_id" validate:"required,uuid"`
	SourceIncidentID *string  `json:"source_incident_id" validate:"omitempty,uuid"` // optional reference to source incident
	Channel          string   `json:"channel"`
	ReporterID       *string  `json:"reporter_id" validate:"omitempty,uuid"` // link to user who created the complaint
	DepartmentID     *string  `json:"department_id" validate:"omitempty,uuid"`
	AssigneeID       *string  `json:"assignee_id" validate:"omitempty,uuid"`
	LocationID       *string  `json:"location_id" validate:"omitempty,uuid"`
	LookupValueIDs   []string `json:"lookup_value_ids" validate:"omitempty,dive,uuid"`
}

// ConvertToRequestRequest for converting an incident to a request
type ConvertToRequestRequest struct {
	TransitionID      *string                  `json:"transition_id" validate:"omitempty,uuid"`
	TransitionComment string                   `json:"transition_comment"`
	ClassificationID  string                   `json:"classification_id" validate:"required,uuid"`
	WorkflowID        string                   `json:"workflow_id" validate:"required,uuid"`
	Title             *string                  `json:"title"`
	Description       *string                  `json:"description"`
	AssigneeID        *string                  `json:"assignee_id" validate:"omitempty,uuid"`
	DepartmentID      *string                  `json:"department_id" validate:"omitempty,uuid"`
	DueDate           *string                  `json:"due_date"`
	Feedback          *IncidentFeedbackRequest `json:"feedback"`
}

// ConvertToRequestResponse for the conversion result
type ConvertToRequestResponse struct {
	OriginalIncident *IncidentResponse `json:"original_incident"`
	NewRequest       *IncidentResponse `json:"new_request"`
}

type IncidentFilter struct {
	Search           string      `json:"search"`
	WorkflowID       *uuid.UUID  `json:"workflow_id"`
	CurrentStateID   *uuid.UUID  `json:"current_state_id"`
	ClassificationID *uuid.UUID  `json:"classification_id"`
	AssigneeID       *uuid.UUID  `json:"assignee_id"`
	DepartmentID     *uuid.UUID  `json:"department_id"`
	LocationID       *uuid.UUID  `json:"location_id"`
	ReporterID       *uuid.UUID  `json:"reporter_id"`
	SLABreached      *bool       `json:"sla_breached"`
	RecordType       *string     `json:"record_type"` // 'incident', 'request', or 'complaint'
	Channel          *string     `json:"channel"`     // for complaints
	StartDate        *time.Time  `json:"start_date"`
	EndDate          *time.Time  `json:"end_date"`
	Page             int         `json:"page"`
	Limit            int         `json:"limit"`
	UserRoleIDs      []uuid.UUID `json:"-"` // For filtering stats by user's roles
}

// Response types

type IncidentResponse struct {
	ID               uuid.UUID               `json:"id"`
	IncidentNumber   string                  `json:"incident_number"`
	Title            string                  `json:"title"`
	Description      string                  `json:"description"`
	RecordType       string                  `json:"record_type"`
	SourceIncidentID *uuid.UUID              `json:"source_incident_id,omitempty"`
	SourceIncident   *IncidentResponse       `json:"source_incident,omitempty"`
	Classification   *ClassificationResponse `json:"classification,omitempty"`
	Workflow         *WorkflowResponse       `json:"workflow,omitempty"`
	CurrentState     *WorkflowStateResponse  `json:"current_state,omitempty"`
	Assignee         *UserResponse           `json:"assignee,omitempty"`
	Assignees        []UserResponse          `json:"assignees,omitempty"`
	Department       *DepartmentResponse     `json:"department,omitempty"`
	Location         *LocationResponse       `json:"location,omitempty"`
	Latitude         *float64                `json:"latitude,omitempty"`
	Longitude        *float64                `json:"longitude,omitempty"`
	DueDate          *time.Time              `json:"due_date"`
	ResolvedAt       *time.Time              `json:"resolved_at"`
	ClosedAt         *time.Time              `json:"closed_at"`
	SLABreached      bool                    `json:"sla_breached"`
	SLADeadline      *time.Time              `json:"sla_deadline"`
	Reporter         *UserResponse           `json:"reporter,omitempty"`
	ReporterEmail    string                  `json:"reporter_email"`
	ReporterName     string                  `json:"reporter_name"`
	Channel          string                  `json:"channel,omitempty"`
	CreatedByName    string                  `json:"created_by_name,omitempty"`
	CreatedByMobile  string                  `json:"created_by_mobile,omitempty"`
	EvaluationCount  int                     `json:"evaluation_count,omitempty"`
	CustomFields     string                  `json:"custom_fields,omitempty"`
	CommentsCount    int                     `json:"comments_count"`
	AttachmentsCount int                     `json:"attachments_count"`
	CreatedAt        time.Time               `json:"created_at"`
	UpdatedAt        time.Time               `json:"updated_at"`
	LookupValues     []LookupValueResponse   `json:"lookup_values,omitempty"`
}

type IncidentDetailResponse struct {
	IncidentResponse
	Comments          []IncidentCommentResponse    `json:"comments,omitempty"`
	Attachments       []IncidentAttachmentResponse `json:"attachments,omitempty"`
	TransitionHistory []TransitionHistoryResponse  `json:"transition_history,omitempty"`
}

type IncidentCommentResponse struct {
	ID                  uuid.UUID     `json:"id"`
	IncidentID          uuid.UUID     `json:"incident_id"`
	Author              *UserResponse `json:"author,omitempty"`
	Content             string        `json:"content"`
	IsInternal          bool          `json:"is_internal"`
	TransitionHistoryID *uuid.UUID    `json:"transition_history_id,omitempty"`
	CreatedAt           time.Time     `json:"created_at"`
}

type IncidentAttachmentResponse struct {
	ID                  uuid.UUID     `json:"id"`
	IncidentID          uuid.UUID     `json:"incident_id"`
	FileName            string        `json:"file_name"`
	FileSize            int64         `json:"file_size"`
	MimeType            string        `json:"mime_type"`
	URL                 string        `json:"url,omitempty"`
	UploadedBy          *UserResponse `json:"uploaded_by,omitempty"`
	TransitionHistoryID *uuid.UUID    `json:"transition_history_id,omitempty"`
	CreatedAt           time.Time     `json:"created_at"`
}

type IncidentFeedbackResponse struct {
	ID                  uuid.UUID     `json:"id"`
	IncidentID          uuid.UUID     `json:"incident_id"`
	Rating              int           `json:"rating"`
	Comment             string        `json:"comment,omitempty"`
	CreatedBy           *UserResponse `json:"created_by,omitempty"`
	TransitionHistoryID *uuid.UUID    `json:"transition_history_id,omitempty"`
	CreatedAt           time.Time     `json:"created_at"`
}

type TransitionHistoryResponse struct {
	ID            uuid.UUID                   `json:"id"`
	IncidentID    uuid.UUID                   `json:"incident_id"`
	Transition    *WorkflowTransitionResponse `json:"transition,omitempty"`
	FromState     *WorkflowStateResponse      `json:"from_state,omitempty"`
	ToState       *WorkflowStateResponse      `json:"to_state,omitempty"`
	PerformedBy   *UserResponse               `json:"performed_by,omitempty"`
	Comment       string                      `json:"comment,omitempty"`
	OldValues     string                      `json:"old_values,omitempty"`
	NewValues     string                      `json:"new_values,omitempty"`
	ActionResults string                      `json:"action_results,omitempty"`
	TransitionedAt time.Time                  `json:"transitioned_at"`
}

type AvailableTransitionResponse struct {
	Transition   WorkflowTransitionResponse `json:"transition"`
	CanExecute   bool                       `json:"can_execute"`
	Requirements []TransitionRequirementResponse `json:"requirements,omitempty"`
	Reason       string                     `json:"reason,omitempty"`
}

type StateStatDetail struct {
	ID    uuid.UUID `json:"id"`
	Name  string    `json:"name"`
	Count int64     `json:"count"`
}

type IncidentStatsResponse struct {
	Total          int64              `json:"total"`
	Open           int64              `json:"open"`
	InProgress     int64              `json:"in_progress"`
	Resolved       int64              `json:"resolved"`
	Closed         int64              `json:"closed"`
	SLABreached    int64              `json:"sla_breached"`
	ByPriority     map[int]int64      `json:"by_priority"`
	BySeverity     map[int]int64      `json:"by_severity"`
	ByState        map[string]int64   `json:"by_state"`
	ByStateDetails []StateStatDetail  `json:"by_state_details,omitempty"`
}

// Converter functions

func ToIncidentResponse(i *Incident) IncidentResponse {
	resp := IncidentResponse{
		ID:               i.ID,
		IncidentNumber:   i.IncidentNumber,
		Title:            i.Title,
		Description:      i.Description,
		RecordType:       i.RecordType,
		SourceIncidentID: i.SourceIncidentID,
		Latitude:         i.Latitude,
		Longitude:        i.Longitude,
		DueDate:          i.DueDate,
		ResolvedAt:       i.ResolvedAt,
		ClosedAt:         i.ClosedAt,
		SLABreached:      i.SLABreached,
		SLADeadline:      i.SLADeadline,
		ReporterEmail:    i.ReporterEmail,
		ReporterName:     i.ReporterName,
		Channel:          i.Channel,
		CreatedByName:    i.CreatedByName,
		CreatedByMobile:  i.CreatedByMobile,
		EvaluationCount:  i.EvaluationCount,
		CustomFields:     i.CustomFields,
		CommentsCount:    len(i.Comments),
		AttachmentsCount: len(i.Attachments),
		CreatedAt:        i.CreatedAt,
		UpdatedAt:        i.UpdatedAt,
	}

	if i.SourceIncident != nil {
		sourceResp := ToIncidentResponse(i.SourceIncident)
		resp.SourceIncident = &sourceResp
	}

	if i.Classification != nil {
		classResp := ToClassificationResponse(i.Classification)
		resp.Classification = &classResp
	}

	if i.Workflow != nil {
		wfResp := ToWorkflowResponse(i.Workflow)
		resp.Workflow = &wfResp
	}

	if i.CurrentState != nil {
		stateResp := ToWorkflowStateResponse(i.CurrentState)
		resp.CurrentState = &stateResp
	}

	if i.Assignee != nil {
		userResp := ToUserResponse(i.Assignee)
		resp.Assignee = &userResp
	}

	// Convert multiple assignees
	if len(i.Assignees) > 0 {
		resp.Assignees = make([]UserResponse, len(i.Assignees))
		for idx, user := range i.Assignees {
			resp.Assignees[idx] = ToUserResponse(&user)
		}
	}

	if len(i.LookupValues) > 0 {
		resp.LookupValues = make([]LookupValueResponse, len(i.LookupValues))
		for idx, val := range i.LookupValues {
			resp.LookupValues[idx] = ToLookupValueResponse(&val)
		}
	}

	if i.Department != nil {
		deptResp := ToDepartmentResponse(i.Department)
		resp.Department = &deptResp
	}

	if i.Location != nil {
		locResp := ToLocationResponse(i.Location)
		resp.Location = &locResp
	}

	if i.Reporter != nil {
		reporterResp := ToUserResponse(i.Reporter)
		resp.Reporter = &reporterResp
	}

	return resp
}

func ToIncidentDetailResponse(storage *storage.MinIOStorage, i *Incident) IncidentDetailResponse {
	resp := IncidentDetailResponse{
		IncidentResponse: ToIncidentResponse(i),
	}

	if len(i.Comments) > 0 {
		resp.Comments = make([]IncidentCommentResponse, len(i.Comments))
		for idx, c := range i.Comments {
			resp.Comments[idx] = ToIncidentCommentResponse(&c)
		}
	}

	if len(i.Attachments) > 0 {
		resp.Attachments = make([]IncidentAttachmentResponse, len(i.Attachments))
		for idx, a := range i.Attachments {
			url, err := storage.GetFileURL(context.Background(), a.FilePath)
			if err != nil {
				fmt.Printf("failed to get file URL: %v", err)
			}
			resp.Attachments[idx] = ToIncidentAttachmentResponse(&a, url)
		}
	}

	if len(i.TransitionHistory) > 0 {
		resp.TransitionHistory = make([]TransitionHistoryResponse, len(i.TransitionHistory))
		for idx, h := range i.TransitionHistory {
			resp.TransitionHistory[idx] = ToTransitionHistoryResponse(&h)
		}
	}

	return resp
}

func ToIncidentCommentResponse(c *IncidentComment) IncidentCommentResponse {
	resp := IncidentCommentResponse{
		ID:                  c.ID,
		IncidentID:          c.IncidentID,
		Content:             c.Content,
		IsInternal:          c.IsInternal,
		TransitionHistoryID: c.TransitionHistoryID,
		CreatedAt:           c.CreatedAt,
	}

	if c.Author != nil {
		authorResp := ToUserResponse(c.Author)
		resp.Author = &authorResp
	}

	return resp
}

func ToIncidentAttachmentResponse(a *IncidentAttachment, url string) IncidentAttachmentResponse {
	resp := IncidentAttachmentResponse{
		ID:                  a.ID,
		IncidentID:          a.IncidentID,
		FileName:            a.FileName,
		FileSize:            a.FileSize,
		MimeType:            a.MimeType,
		URL:                 url,
		TransitionHistoryID: a.TransitionHistoryID,
		CreatedAt:           a.CreatedAt,
	}

	if a.UploadedBy != nil {
		uploaderResp := ToUserResponse(a.UploadedBy)
		resp.UploadedBy = &uploaderResp
	}

	return resp
}

func ToIncidentFeedbackResponse(f *IncidentFeedback) IncidentFeedbackResponse {
	resp := IncidentFeedbackResponse{
		ID:                  f.ID,
		IncidentID:          f.IncidentID,
		Rating:              f.Rating,
		Comment:             f.Comment,
		TransitionHistoryID: f.TransitionHistoryID,
		CreatedAt:           f.CreatedAt,
	}

	if f.CreatedBy != nil {
		createdByResp := ToUserResponse(f.CreatedBy)
		resp.CreatedBy = &createdByResp
	}

	return resp
}

func ToTransitionHistoryResponse(h *IncidentTransitionHistory) TransitionHistoryResponse {
	resp := TransitionHistoryResponse{
		ID:             h.ID,
		IncidentID:     h.IncidentID,
		Comment:        h.Comment,
		OldValues:      h.OldValues,
		NewValues:      h.NewValues,
		ActionResults:  h.ActionResults,
		TransitionedAt: h.TransitionedAt,
	}

	if h.Transition != nil {
		transResp := ToWorkflowTransitionResponse(h.Transition)
		resp.Transition = &transResp
	}

	if h.FromState != nil {
		fromResp := ToWorkflowStateResponse(h.FromState)
		resp.FromState = &fromResp
	}

	if h.ToState != nil {
		toResp := ToWorkflowStateResponse(h.ToState)
		resp.ToState = &toResp
	}

	if h.PerformedBy != nil {
		perfResp := ToUserResponse(h.PerformedBy)
		resp.PerformedBy = &perfResp
	}

	return resp
}

// IncidentRevisionResponse is the API response for an incident revision
type IncidentRevisionResponse struct {
	ID                  uuid.UUID                  `json:"id"`
	IncidentID          uuid.UUID                  `json:"incident_id"`
	RevisionNumber      int                        `json:"revision_number"`
	ActionType          IncidentRevisionActionType `json:"action_type"`
	ActionDescription   string                     `json:"action_description"`
	Changes             []IncidentFieldChange      `json:"changes"`
	PerformedByID       uuid.UUID                  `json:"performed_by_id"`
	PerformedBy         *UserResponse              `json:"performed_by,omitempty"`
	PerformedByRoles    []string                   `json:"performed_by_roles"`
	PerformedByPhone    string                     `json:"performed_by_phone"`
	CommentID           *uuid.UUID                 `json:"comment_id,omitempty"`
	AttachmentID        *uuid.UUID                 `json:"attachment_id,omitempty"`
	TransitionHistoryID *uuid.UUID                 `json:"transition_history_id,omitempty"`
	CreatedAt           time.Time                  `json:"created_at"`
}

// ToIncidentRevisionResponse converts an IncidentRevision to IncidentRevisionResponse
func ToIncidentRevisionResponse(r *IncidentRevision) IncidentRevisionResponse {
	var changes []IncidentFieldChange
	if r.Changes != "" {
		_ = json.Unmarshal([]byte(r.Changes), &changes)
	}

	var roles []string
	if r.PerformedByRoles != "" {
		_ = json.Unmarshal([]byte(r.PerformedByRoles), &roles)
	}

	resp := IncidentRevisionResponse{
		ID:                  r.ID,
		IncidentID:          r.IncidentID,
		RevisionNumber:      r.RevisionNumber,
		ActionType:          r.ActionType,
		ActionDescription:   r.ActionDescription,
		Changes:             changes,
		PerformedByID:       r.PerformedByID,
		PerformedByRoles:    roles,
		PerformedByPhone:    r.PerformedByPhone,
		CommentID:           r.CommentID,
		AttachmentID:        r.AttachmentID,
		TransitionHistoryID: r.TransitionHistoryID,
		CreatedAt:           r.CreatedAt,
	}

	if r.PerformedBy != nil {
		perfResp := ToUserResponse(r.PerformedBy)
		resp.PerformedBy = &perfResp
	}

	return resp
}
