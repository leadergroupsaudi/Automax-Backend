package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/internal/storage"
	"github.com/google/uuid"
)

type IncidentService interface {
	// Incident CRUD
	CreateIncident(ctx context.Context, req *models.IncidentCreateRequest, reporterID uuid.UUID) (*models.IncidentResponse, error)
	GetIncident(ctx context.Context, id uuid.UUID) (*models.IncidentDetailResponse, error)
	ListIncidents(ctx context.Context, filter *models.IncidentFilter) ([]models.IncidentResponse, int64, error)
	UpdateIncident(ctx context.Context, id uuid.UUID, req *models.IncidentUpdateRequest, userID uuid.UUID) (*models.IncidentResponse, error)
	DeleteIncident(ctx context.Context, id uuid.UUID) error

	// Convert incident to request
	ConvertToRequest(ctx context.Context, incidentID uuid.UUID, req *models.ConvertToRequestRequest, userID uuid.UUID, userRoleIDs []uuid.UUID) (*models.ConvertToRequestResponse, error)

	// Complaint operations
	CreateComplaint(ctx context.Context, req *models.CreateComplaintRequest, creatorID uuid.UUID) (*models.IncidentResponse, error)
	IncrementEvaluationCount(ctx context.Context, id uuid.UUID) error

	// Query operations
	CreateQuery(ctx context.Context, req *models.CreateQueryRequest, creatorID uuid.UUID) (*models.IncidentResponse, error)

	// State transitions
	ExecuteTransition(ctx context.Context, incidentID uuid.UUID, req *models.IncidentTransitionRequest, userID uuid.UUID, userRoleIDs []uuid.UUID) (*models.IncidentResponse, error)
	GetAvailableTransitions(ctx context.Context, incidentID uuid.UUID, userRoleIDs []uuid.UUID) ([]models.AvailableTransitionResponse, error)
	GetTransitionHistory(ctx context.Context, incidentID uuid.UUID) ([]models.TransitionHistoryResponse, error)

	// Comments
	AddComment(ctx context.Context, incidentID uuid.UUID, req *models.IncidentCommentRequest, authorID uuid.UUID) (*models.IncidentCommentResponse, error)
	ListComments(ctx context.Context, incidentID uuid.UUID) ([]models.IncidentCommentResponse, error)
	UpdateComment(ctx context.Context, commentID uuid.UUID, req *models.IncidentCommentRequest, userID uuid.UUID) (*models.IncidentCommentResponse, error)
	DeleteComment(ctx context.Context, commentID uuid.UUID, userID uuid.UUID) error

	// Attachments
	AddAttachment(ctx context.Context, incidentID uuid.UUID, attachment *models.IncidentAttachment) (*models.IncidentAttachmentResponse, error)
	ListAttachments(ctx context.Context, incidentID uuid.UUID) ([]models.IncidentAttachmentResponse, error)
	DeleteAttachment(ctx context.Context, attachmentID uuid.UUID, userID uuid.UUID) error
	GetAttachment(ctx context.Context, attachmentID uuid.UUID) (*models.IncidentAttachment, error)

	// Assignment
	AssignIncident(ctx context.Context, incidentID, assigneeID, userID uuid.UUID) (*models.IncidentResponse, error)

	// Stats and user queries
	GetStats(ctx context.Context, filter *models.IncidentFilter) (*models.IncidentStatsResponse, error)
	GetMyAssigned(ctx context.Context, userID uuid.UUID, recordType string, page, limit int) ([]models.IncidentResponse, int64, error)
	GetMyReported(ctx context.Context, userID uuid.UUID, recordType string, page, limit int) ([]models.IncidentResponse, int64, error)
	GetSLABreached(ctx context.Context) ([]models.IncidentResponse, error)

	// SLA monitoring
	CheckAndUpdateSLABreaches(ctx context.Context) error

	// Revisions
	ListRevisions(ctx context.Context, incidentID uuid.UUID, filter *models.IncidentRevisionFilter) ([]models.IncidentRevisionResponse, int64, error)
	CreateRevision(ctx context.Context, incidentID uuid.UUID, actionType models.IncidentRevisionActionType, description string, changes []models.IncidentFieldChange, userID uuid.UUID) error
}

type incidentService struct {
	incidentRepo repository.IncidentRepository
	workflowRepo repository.WorkflowRepository
	userRepo     repository.UserRepository
	storage      *storage.MinIOStorage
}

func NewIncidentService(incidentRepo repository.IncidentRepository, workflowRepo repository.WorkflowRepository, userRepo repository.UserRepository, storage *storage.MinIOStorage) IncidentService {
	return &incidentService{
		incidentRepo: incidentRepo,
		workflowRepo: workflowRepo,
		userRepo:     userRepo,
		storage:      storage,
	}
}

// Incident CRUD

func (s *incidentService) CreateIncident(ctx context.Context, req *models.IncidentCreateRequest, reporterID uuid.UUID) (*models.IncidentResponse, error) {
	// Parse workflow ID
	workflowID, err := uuid.Parse(req.WorkflowID)
	if err != nil {
		return nil, errors.New("invalid workflow_id")
	}

	// Get the initial state of the workflow
	initialState, err := s.workflowRepo.GetInitialState(ctx, workflowID)
	if err != nil {
		return nil, errors.New("workflow has no initial state configured")
	}

	// Generate incident number
	incidentNumber, err := s.incidentRepo.GenerateIncidentNumber(ctx)
	if err != nil {
		return nil, err
	}

	incident := &models.Incident{
		IncidentNumber: incidentNumber,
		Title:          req.Title,
		Description:    req.Description,
		WorkflowID:     workflowID,
		CurrentStateID: initialState.ID,
		ReporterID:     &reporterID,
		ReporterEmail:  req.ReporterEmail,
		ReporterName:   req.ReporterName,
		CustomFields:   req.CustomFields,
	}

	// Parse optional UUIDs
	if req.ClassificationID != nil && *req.ClassificationID != "" {
		classID, err := uuid.Parse(*req.ClassificationID)
		if err == nil {
			incident.ClassificationID = &classID
		}
	}

	if req.AssigneeID != nil && *req.AssigneeID != "" {
		assigneeID, err := uuid.Parse(*req.AssigneeID)
		if err == nil {
			incident.AssigneeID = &assigneeID
		}
	}

	if req.DepartmentID != nil && *req.DepartmentID != "" {
		deptID, err := uuid.Parse(*req.DepartmentID)
		if err == nil {
			incident.DepartmentID = &deptID
		}
	}

	if req.LocationID != nil && *req.LocationID != "" {
		locID, err := uuid.Parse(*req.LocationID)
		if err == nil {
			incident.LocationID = &locID
		}
	}

	if req.DueDate != nil && *req.DueDate != "" {
		dueDate, err := time.Parse(time.RFC3339, *req.DueDate)
		if err == nil {
			incident.DueDate = &dueDate
		}
	}

	// Calculate SLA deadline based on initial state
	if initialState.SLAHours != nil && *initialState.SLAHours > 0 {
		deadline := time.Now().Add(time.Duration(*initialState.SLAHours) * time.Hour)
		incident.SLADeadline = &deadline
	}

	if err := s.incidentRepo.Create(ctx, incident); err != nil {
		return nil, err
	}

	// Set lookup values using Association API (GORM many-to-many requires this after create)
	if len(req.LookupValueIDs) > 0 {
		var lookupValues []models.LookupValue
		for _, idStr := range req.LookupValueIDs {
			id, err := uuid.Parse(idStr)
			if err == nil {
				lookupValues = append(lookupValues, models.LookupValue{ID: id})
			}
		}
		if err := s.incidentRepo.SetLookupValues(ctx, incident.ID, lookupValues); err != nil {
			fmt.Printf("Warning: failed to set lookup values: %v\n", err)
		}
	}

	// Fetch with relations
	created, err := s.incidentRepo.FindByIDWithRelations(ctx, incident.ID)
	if err != nil {
		return nil, err
	}

	resp := models.ToIncidentResponse(created)
	return &resp, nil
}

func (s *incidentService) GetIncident(ctx context.Context, id uuid.UUID) (*models.IncidentDetailResponse, error) {
	incident, err := s.incidentRepo.FindByIDWithRelations(ctx, id)
	if err != nil {
		return nil, err
	}

	resp := models.ToIncidentDetailResponse(s.storage, incident)
	return &resp, nil
}

func (s *incidentService) ListIncidents(ctx context.Context, filter *models.IncidentFilter) ([]models.IncidentResponse, int64, error) {
	incidents, total, err := s.incidentRepo.List(ctx, filter)
	if err != nil {
		return nil, 0, err
	}

	responses := make([]models.IncidentResponse, len(incidents))
	for i, inc := range incidents {
		responses[i] = models.ToIncidentResponse(&inc)
	}

	return responses, total, nil
}

func (s *incidentService) UpdateIncident(ctx context.Context, id uuid.UUID, req *models.IncidentUpdateRequest, userID uuid.UUID) (*models.IncidentResponse, error) {
	incident, err := s.incidentRepo.FindByIDWithRelations(ctx, id)
	if err != nil {
		return nil, err
	}

	// Track changes for revision
	var changes []models.IncidentFieldChange
	var descriptions []string

	if req.Title != "" && req.Title != incident.Title {
		oldVal := incident.Title
		changes = append(changes, models.IncidentFieldChange{
			FieldName:  "title",
			FieldLabel: "Title",
			OldValue:   &oldVal,
			NewValue:   &req.Title,
		})
		descriptions = append(descriptions, fmt.Sprintf("Title changed from %s to %s", oldVal, req.Title))
		incident.Title = req.Title
	}
	if req.Description != "" && req.Description != incident.Description {
		oldVal := incident.Description
		changes = append(changes, models.IncidentFieldChange{
			FieldName:  "description",
			FieldLabel: "Description",
			OldValue:   &oldVal,
			NewValue:   &req.Description,
		})
		descriptions = append(descriptions, "Description changed")
		incident.Description = req.Description
	}

	if req.LookupValueIDs != nil {
		var newValues []models.LookupValue
		for _, idStr := range req.LookupValueIDs {
			id, err := uuid.Parse(idStr)
			if err == nil {
				newValues = append(newValues, models.LookupValue{ID: id})
			}
		}
		// This will replace existing lookup values
		if err := s.incidentRepo.SetLookupValues(ctx, incident.ID, newValues); err != nil {
			// Log or handle error, for now we'll just log
			fmt.Printf("Error setting lookup values: %v\n", err)
		} else {
			descriptions = append(descriptions, "Dynamic attributes updated")
			// For revision history, we'd need to compare old and new, which is more complex.
			// For now, we just note that they were updated.
		}
	}

	if req.CustomFields != "" && req.CustomFields != incident.CustomFields {
		incident.CustomFields = req.CustomFields
	}

	// Parse optional UUIDs
	if req.ClassificationID != nil {
		if *req.ClassificationID == "" {
			incident.ClassificationID = nil
		} else {
			classID, err := uuid.Parse(*req.ClassificationID)
			if err == nil {
				incident.ClassificationID = &classID
			}
		}
	}

	if req.AssigneeID != nil {
		oldAssigneeName := ""
		if incident.Assignee != nil {
			oldAssigneeName = incident.Assignee.FirstName + " " + incident.Assignee.LastName
		}

		if *req.AssigneeID == "" {
			if incident.AssigneeID != nil {
				changes = append(changes, models.IncidentFieldChange{
					FieldName:  "assignee_id",
					FieldLabel: "Assigned To",
					OldValue:   &oldAssigneeName,
					NewValue:   nil,
				})
				descriptions = append(descriptions, fmt.Sprintf("AssignedTo changed from %s to Unassigned", oldAssigneeName))
			}
			incident.AssigneeID = nil
		} else {
			assigneeID, err := uuid.Parse(*req.AssigneeID)
			if err == nil {
				if incident.AssigneeID == nil || *incident.AssigneeID != assigneeID {
					newVal := *req.AssigneeID // Will be resolved to name later
					changes = append(changes, models.IncidentFieldChange{
						FieldName:  "assignee_id",
						FieldLabel: "Assigned To",
						OldValue:   &oldAssigneeName,
						NewValue:   &newVal,
					})
					descriptions = append(descriptions, fmt.Sprintf("AssignedTo changed from %s", oldAssigneeName))
				}
				incident.AssigneeID = &assigneeID
			}
		}
	}

	if req.DepartmentID != nil {
		if *req.DepartmentID == "" {
			incident.DepartmentID = nil
		} else {
			deptID, err := uuid.Parse(*req.DepartmentID)
			if err == nil {
				incident.DepartmentID = &deptID
			}
		}
	}

	if req.LocationID != nil {
		if *req.LocationID == "" {
			incident.LocationID = nil
		} else {
			locID, err := uuid.Parse(*req.LocationID)
			if err == nil {
				incident.LocationID = &locID
			}
		}
	}

	if req.DueDate != nil {
		if *req.DueDate == "" {
			incident.DueDate = nil
		} else {
			dueDate, err := time.Parse(time.RFC3339, *req.DueDate)
			if err == nil {
				incident.DueDate = &dueDate
			}
		}
	}

	if err := s.incidentRepo.Update(ctx, incident); err != nil {
		return nil, err
	}

	// Create revision if there were changes
	if len(changes) > 0 {
		description := "Fields updated"
		if len(descriptions) > 0 {
			description = descriptions[0]
			if len(descriptions) > 1 {
				description = fmt.Sprintf("%s and %d more changes", description, len(descriptions)-1)
			}
		}
		_ = s.CreateRevision(ctx, id, models.RevisionActionFieldChange, description, changes, userID)
	}

	updated, err := s.incidentRepo.FindByIDWithRelations(ctx, id)
	if err != nil {
		return nil, err
	}

	resp := models.ToIncidentResponse(updated)
	return &resp, nil
}

func (s *incidentService) DeleteIncident(ctx context.Context, id uuid.UUID) error {
	return s.incidentRepo.Delete(ctx, id)
}

// ConvertToRequest converts an incident to a request
func (s *incidentService) ConvertToRequest(ctx context.Context, incidentID uuid.UUID, req *models.ConvertToRequestRequest, userID uuid.UUID, userRoleIDs []uuid.UUID) (*models.ConvertToRequestResponse, error) {
	// Get the source incident
	sourceIncident, err := s.incidentRepo.FindByIDWithRelations(ctx, incidentID)
	if err != nil {
		return nil, errors.New("incident not found")
	}

	// Validate it's not already a request
	if sourceIncident.RecordType == "request" {
		return nil, errors.New("cannot convert a request to another request")
	}

	// Execute transition if provided
	if req.TransitionID != nil && *req.TransitionID != "" {
		transitionReq := &models.IncidentTransitionRequest{
			TransitionID: *req.TransitionID,
			Comment:      req.TransitionComment,
			Feedback:     req.Feedback,
		}

		_, err := s.ExecuteTransition(ctx, incidentID, transitionReq, userID, userRoleIDs)
		if err != nil {
			return nil, fmt.Errorf("failed to execute transition: %w", err)
		}

		// Reload the incident after transition
		sourceIncident, err = s.incidentRepo.FindByIDWithRelations(ctx, incidentID)
		if err != nil {
			return nil, errors.New("failed to reload incident after transition")
		}
	}

	// Parse workflow ID
	workflowID, err := uuid.Parse(req.WorkflowID)
	if err != nil {
		return nil, errors.New("invalid workflow_id")
	}

	// Get the initial state of the request workflow
	initialState, err := s.workflowRepo.GetInitialState(ctx, workflowID)
	if err != nil {
		return nil, errors.New("workflow has no initial state configured")
	}

	// Parse classification ID
	classificationID, err := uuid.Parse(req.ClassificationID)
	if err != nil {
		return nil, errors.New("invalid classification_id")
	}

	// Generate request number
	requestNumber, err := s.incidentRepo.GenerateRequestNumber(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to generate request number: %w", err)
	}

	// Create the new request, copying relevant data from source incident
	title := sourceIncident.Title
	if req.Title != nil && *req.Title != "" {
		title = *req.Title
	}

	description := sourceIncident.Description
	if req.Description != nil && *req.Description != "" {
		description = *req.Description
	}

	newRequest := &models.Incident{
		IncidentNumber:   requestNumber,
		Title:            title,
		Description:      description,
		RecordType:       "request",
		SourceIncidentID: &incidentID,
		ClassificationID: &classificationID,
		WorkflowID:       workflowID,
		CurrentStateID:   initialState.ID,
		ReporterID:       sourceIncident.ReporterID,
		ReporterEmail:    sourceIncident.ReporterEmail,
		ReporterName:     sourceIncident.ReporterName,
		LocationID:       sourceIncident.LocationID,
		Latitude:         sourceIncident.Latitude,
		Longitude:        sourceIncident.Longitude,
		CustomFields:     sourceIncident.CustomFields,
	}

	// Handle optional assignee override
	if req.AssigneeID != nil && *req.AssigneeID != "" {
		assigneeID, err := uuid.Parse(*req.AssigneeID)
		if err == nil {
			newRequest.AssigneeID = &assigneeID
		}
	} else {
		newRequest.AssigneeID = sourceIncident.AssigneeID
	}

	// Handle optional department override
	if req.DepartmentID != nil && *req.DepartmentID != "" {
		deptID, err := uuid.Parse(*req.DepartmentID)
		if err == nil {
			newRequest.DepartmentID = &deptID
		}
	} else {
		newRequest.DepartmentID = sourceIncident.DepartmentID
	}

	// Handle due date
	if req.DueDate != nil && *req.DueDate != "" {
		dueDate, err := time.Parse(time.RFC3339, *req.DueDate)
		if err == nil {
			newRequest.DueDate = &dueDate
		}
	}

	// Calculate SLA deadline based on initial state
	if initialState.SLAHours != nil && *initialState.SLAHours > 0 {
		deadline := time.Now().Add(time.Duration(*initialState.SLAHours) * time.Hour)
		newRequest.SLADeadline = &deadline
	}

	// Create the request
	if err := s.incidentRepo.Create(ctx, newRequest); err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Copy lookup values from source incident
	if len(sourceIncident.LookupValues) > 0 {
		if err := s.incidentRepo.SetLookupValues(ctx, newRequest.ID, sourceIncident.LookupValues); err != nil {
			fmt.Printf("Warning: failed to copy lookup values: %v\n", err)
		}
	}

	// Update source incident with reference to the converted request
	if err := s.incidentRepo.UpdateFields(ctx, incidentID, map[string]interface{}{
		"converted_request_id": newRequest.ID,
	}); err != nil {
		fmt.Printf("Warning: failed to update converted_request_id on source incident: %v\n", err)
	}

	// Fetch the created request with relations
	createdRequest, err := s.incidentRepo.FindByIDWithRelations(ctx, newRequest.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch created request: %w", err)
	}

	// Create revision for source incident
	sourceIncidentNumber := sourceIncident.IncidentNumber
	changes := []models.IncidentFieldChange{
		{
			FieldName:  "converted_to_request",
			FieldLabel: "Converted to Request",
			OldValue:   nil,
			NewValue:   &requestNumber,
		},
	}
	description = fmt.Sprintf("Incident converted to request %s", requestNumber)
	_ = s.CreateRevision(ctx, incidentID, models.RevisionActionFieldChange, description, changes, userID)

	// Create revision for new request
	changes = []models.IncidentFieldChange{
		{
			FieldName:  "source_incident",
			FieldLabel: "Created from Incident",
			OldValue:   nil,
			NewValue:   &sourceIncidentNumber,
		},
	}
	description = fmt.Sprintf("Request created from incident %s", sourceIncidentNumber)
	_ = s.CreateRevision(ctx, newRequest.ID, models.RevisionActionCreated, description, changes, userID)

	// Build response
	originalResp := models.ToIncidentResponse(sourceIncident)
	newResp := models.ToIncidentResponse(createdRequest)

	return &models.ConvertToRequestResponse{
		OriginalIncident: &originalResp,
		NewRequest:       &newResp,
	}, nil
}

// State transitions

func (s *incidentService) ExecuteTransition(ctx context.Context, incidentID uuid.UUID, req *models.IncidentTransitionRequest, userID uuid.UUID, userRoleIDs []uuid.UUID) (*models.IncidentResponse, error) {
	// Get the incident
	incident, err := s.incidentRepo.FindByID(ctx, incidentID)
	if err != nil {
		return nil, errors.New("incident not found")
	}

	// Parse transition ID
	transitionID, err := uuid.Parse(req.TransitionID)
	if err != nil {
		return nil, errors.New("invalid transition_id")
	}

	// Get the transition with relations
	transition, err := s.workflowRepo.FindTransitionByIDWithRelations(ctx, transitionID)
	if err != nil {
		return nil, errors.New("transition not found")
	}

	// Verify the transition belongs to this workflow
	if transition.WorkflowID != incident.WorkflowID {
		return nil, errors.New("transition does not belong to this workflow")
	}

	// Verify the transition starts from the current state
	if transition.FromStateID != incident.CurrentStateID {
		return nil, errors.New("transition cannot be executed from current state")
	}

	// Check role authorization
	if len(transition.AllowedRoles) > 0 {
		hasPermission := false
		for _, allowedRole := range transition.AllowedRoles {
			for _, userRoleID := range userRoleIDs {
				if allowedRole.ID == userRoleID {
					hasPermission = true
					break
				}
			}
			if hasPermission {
				break
			}
		}
		if !hasPermission {
			return nil, errors.New("you do not have permission to execute this transition")
		}
	}

	// Validate requirements
	for _, requirement := range transition.Requirements {
		if !requirement.IsMandatory {
			continue
		}

		switch requirement.RequirementType {
		case "comment":
			if req.Comment == "" {
				errMsg := requirement.ErrorMessage
				if errMsg == "" {
					errMsg = "Comment is required for this transition"
				}
				return nil, errors.New(errMsg)
			}
		case "attachment":
			if len(req.Attachments) == 0 {
				errMsg := requirement.ErrorMessage
				if errMsg == "" {
					errMsg = "Attachment is required for this transition"
				}
				return nil, errors.New(errMsg)
			}
		case "feedback":
			if req.Feedback == nil || req.Feedback.Rating == 0 {
				errMsg := requirement.ErrorMessage
				if errMsg == "" {
					errMsg = "Feedback is required for this transition"
				}
				return nil, errors.New(errMsg)
			}
		}
	}

	// Create transition history record
	history := &models.IncidentTransitionHistory{
		IncidentID:     incidentID,
		TransitionID:   transitionID,
		FromStateID:    incident.CurrentStateID,
		ToStateID:      transition.ToStateID,
		PerformedByID:  userID,
		Comment:        req.Comment,
		TransitionedAt: time.Now(),
	}

	if err := s.incidentRepo.CreateTransitionHistory(ctx, history); err != nil {
		return nil, err
	}

	// Link attachments to this transition if provided
	if len(req.Attachments) > 0 {
		attachmentIDs := make([]uuid.UUID, 0, len(req.Attachments))
		for _, idStr := range req.Attachments {
			attachID, err := uuid.Parse(idStr)
			if err == nil {
				attachmentIDs = append(attachmentIDs, attachID)
			}
		}
		if len(attachmentIDs) > 0 {
			s.incidentRepo.LinkAttachmentsToTransition(ctx, attachmentIDs, history.ID)
		}
	}

	// If comment was provided, also create a comment record
	if req.Comment != "" {
		comment := &models.IncidentComment{
			IncidentID:          incidentID,
			AuthorID:            userID,
			Content:             req.Comment,
			IsInternal:          true,
			TransitionHistoryID: &history.ID,
		}
		s.incidentRepo.CreateComment(ctx, comment)
	}

	// If feedback was provided, create a feedback record
	if req.Feedback != nil && req.Feedback.Rating > 0 {
		feedback := &models.IncidentFeedback{
			IncidentID:          incidentID,
			Rating:              req.Feedback.Rating,
			Comment:             req.Feedback.Comment,
			CreatedByID:         userID,
			TransitionHistoryID: &history.ID,
		}
		if err := s.incidentRepo.CreateFeedback(ctx, feedback); err != nil {
			fmt.Printf("Warning: failed to create feedback: %v\n", err)
		}
	}

	// Get new state for SLA calculation
	newState, err := s.workflowRepo.FindStateByID(ctx, transition.ToStateID)
	if err != nil {
		return nil, errors.New("target state not found")
	}

	// Prepare updates map for all fields that need to change
	updates := map[string]interface{}{
		"current_state_id": transition.ToStateID,
		"updated_at":       time.Now(),
	}

	// Handle department assignment from transition settings
	if transition.AssignDepartmentID != nil {
		// Static department assignment
		updates["department_id"] = *transition.AssignDepartmentID
	} else if transition.AutoDetectDepartment && req.DepartmentID != nil && *req.DepartmentID != "" {
		// Auto-detect with user selection
		deptID, err := uuid.Parse(*req.DepartmentID)
		if err == nil {
			updates["department_id"] = deptID
		}
	}

	// Handle user assignment from transition settings
	var assigneeUserIDs []uuid.UUID

	fmt.Printf("[DEBUG] === USER ASSIGNMENT START ===\n")
	fmt.Printf("[DEBUG] Transition: %s (ID: %s)\n", transition.Name, transition.ID)
	fmt.Printf("[DEBUG] AssignUserID: %v\n", transition.AssignUserID)
	fmt.Printf("[DEBUG] ManualSelectUser: %v\n", transition.ManualSelectUser)
	fmt.Printf("[DEBUG] AutoMatchUser: %v\n", transition.AutoMatchUser)
	fmt.Printf("[DEBUG] AssignmentRoleID: %v\n", transition.AssignmentRoleID)

	if transition.AssignUserID != nil {
		// Static user assignment - single user
		fmt.Printf("[DEBUG] Using STATIC user assignment: %s\n", *transition.AssignUserID)
		updates["assignee_id"] = *transition.AssignUserID
		assigneeUserIDs = append(assigneeUserIDs, *transition.AssignUserID)
	} else if transition.ManualSelectUser && transition.AssignmentRoleID != nil {
		// Manual selection mode - user must select from dropdown
		fmt.Printf("[DEBUG] Using MANUAL SELECT mode\n")
		if req.UserID != nil && *req.UserID != "" {
			fmt.Printf("[DEBUG] User selected: %s\n", *req.UserID)
			userAssignID, err := uuid.Parse(*req.UserID)
			if err == nil {
				updates["assignee_id"] = userAssignID
				assigneeUserIDs = append(assigneeUserIDs, userAssignID)
			}
		} else {
			fmt.Printf("[DEBUG] No user selected in manual mode\n")
		}
		// If no user selected, keep current assignee (don't fail the transition)
	} else if transition.AutoMatchUser && transition.AssignmentRoleID != nil {
		// Auto-match mode - find ALL matching users and assign to all of them
		fmt.Printf("[DEBUG] Using AUTO MATCH mode with role: %s\n", *transition.AssignmentRoleID)
		var classificationID, locationID, departmentID, excludeUserID *uuid.UUID
		if incident.ClassificationID != nil {
			classificationID = incident.ClassificationID
			fmt.Printf("[DEBUG] ClassificationID: %s\n", *classificationID)
		}
		if incident.LocationID != nil {
			locationID = incident.LocationID
			fmt.Printf("[DEBUG] LocationID: %s\n", *locationID)
		}
		if incident.DepartmentID != nil {
			departmentID = incident.DepartmentID
			fmt.Printf("[DEBUG] DepartmentID: %s\n", *departmentID)
		}
		if incident.AssigneeID != nil {
			excludeUserID = incident.AssigneeID
			fmt.Printf("[DEBUG] ExcludeUserID (current assignee): %s\n", *excludeUserID)
		}

		// First try matching with all criteria
		fmt.Printf("[DEBUG] Calling FindMatching with all criteria...\n")
		matchedUsers, err := s.userRepo.FindMatching(ctx, transition.AssignmentRoleID, classificationID, locationID, departmentID, excludeUserID)
		fmt.Printf("[DEBUG] FindMatching result: %d users, error: %v\n", len(matchedUsers), err)
		if err == nil && len(matchedUsers) > 0 {
			// Assign ALL matched users
			fmt.Printf("[DEBUG] Found %d matching users with full criteria:\n", len(matchedUsers))
			for _, user := range matchedUsers {
				fmt.Printf("[DEBUG]   - %s (%s)\n", user.Username, user.ID)
				assigneeUserIDs = append(assigneeUserIDs, user.ID)
			}
			// Set primary assignee to first matched user
			updates["assignee_id"] = matchedUsers[0].ID
			fmt.Printf("[DEBUG] Primary assignee set to: %s\n", matchedUsers[0].Username)
		} else if err == nil && len(matchedUsers) == 0 {
			// No exact matches - try matching by role only (more permissive)
			fmt.Printf("[DEBUG] No exact matches, trying role-only match...\n")
			roleOnlyUsers, roleErr := s.userRepo.FindMatching(ctx, transition.AssignmentRoleID, nil, nil, nil, excludeUserID)
			fmt.Printf("[DEBUG] Role-only match result: %d users, error: %v\n", len(roleOnlyUsers), roleErr)
			if roleErr == nil && len(roleOnlyUsers) > 0 {
				// Assign ALL users with that role
				fmt.Printf("[DEBUG] Found %d users with role only:\n", len(roleOnlyUsers))
				for _, user := range roleOnlyUsers {
					fmt.Printf("[DEBUG]   - %s (%s)\n", user.Username, user.ID)
					assigneeUserIDs = append(assigneeUserIDs, user.ID)
				}
				// Set primary assignee to first matched user
				updates["assignee_id"] = roleOnlyUsers[0].ID
				fmt.Printf("[DEBUG] Primary assignee set to: %s\n", roleOnlyUsers[0].Username)
			} else {
				fmt.Printf("[DEBUG] No users found even with role-only match\n")
			}
		} else if err != nil {
			fmt.Printf("[DEBUG] Error in FindMatching: %v\n", err)
		}
	} else {
		fmt.Printf("[DEBUG] No assignment mode matched - skipping user assignment\n")
	}
	fmt.Printf("[DEBUG] Final assigneeUserIDs: %v\n", assigneeUserIDs)
	fmt.Printf("[DEBUG] === USER ASSIGNMENT END ===\n")

	// Update SLA deadline based on new state
	if newState.SLAHours != nil && *newState.SLAHours > 0 {
		deadline := time.Now().Add(time.Duration(*newState.SLAHours) * time.Hour)
		updates["sla_deadline"] = deadline
		updates["sla_breached"] = false // Reset breach status
	}

	// Check if this is a terminal state
	if newState.StateType == "terminal" {
		now := time.Now()
		if newState.Code == "resolved" || newState.Name == "Resolved" {
			updates["resolved_at"] = now
		}
		updates["closed_at"] = now
	}

	// Apply all updates in a single query
	fmt.Printf("[DEBUG] Applying updates: %+v\n", updates)
	if err := s.incidentRepo.UpdateFields(ctx, incidentID, updates); err != nil {
		fmt.Printf("[DEBUG] ERROR in UpdateFields: %v\n", err)
		return nil, err
	}
	fmt.Printf("[DEBUG] UpdateFields successful\n")

	// Set multiple assignees if applicable
	fmt.Printf("[DEBUG] Setting multiple assignees, count: %d\n", len(assigneeUserIDs))
	if len(assigneeUserIDs) > 0 {
		fmt.Printf("[DEBUG] Calling SetAssignees with IDs: %v\n", assigneeUserIDs)
		if err := s.incidentRepo.SetAssignees(ctx, incidentID, assigneeUserIDs); err != nil {
			// Log error but don't fail the transition
			fmt.Printf("[DEBUG] ERROR in SetAssignees: %v\n", err)
		} else {
			fmt.Printf("[DEBUG] SetAssignees successful\n")
		}
	} else {
		fmt.Printf("[DEBUG] No assignees to set (assigneeUserIDs is empty)\n")
	}

	// TODO: Execute transition actions (email, webhook, field updates)
	// This would be implemented in a separate action executor service

	// Fetch updated incident
	updated, err := s.incidentRepo.FindByIDWithRelations(ctx, incidentID)
	if err != nil {
		return nil, err
	}

	// Create revision for state change
	oldStateName := transition.FromState.Name
	newStateName := newState.Name
	changes := []models.IncidentFieldChange{
		{
			FieldName:  "current_state_id",
			FieldLabel: "Status",
			OldValue:   &oldStateName,
			NewValue:   &newStateName,
		},
	}
	description := fmt.Sprintf("Status changed from %s to %s", oldStateName, newStateName)
	_ = s.CreateRevision(ctx, incidentID, models.RevisionActionStatusChanged, description, changes, userID)

	resp := models.ToIncidentResponse(updated)
	return &resp, nil
}

func (s *incidentService) GetAvailableTransitions(ctx context.Context, incidentID uuid.UUID, userRoleIDs []uuid.UUID) ([]models.AvailableTransitionResponse, error) {
	// Get the incident
	incident, err := s.incidentRepo.FindByID(ctx, incidentID)
	if err != nil {
		return nil, err
	}

	// Get all transitions from current state
	transitions, err := s.workflowRepo.ListTransitionsFromState(ctx, incident.CurrentStateID)
	if err != nil {
		return nil, err
	}

	responses := make([]models.AvailableTransitionResponse, len(transitions))
	for i, trans := range transitions {
		canExecute := true
		reason := ""

		// Check if transition is active
		if !trans.IsActive {
			canExecute = false
			reason = "Transition is inactive"
		}

		// Check role authorization
		if canExecute && len(trans.AllowedRoles) > 0 {
			hasPermission := false
			for _, allowedRole := range trans.AllowedRoles {
				for _, userRoleID := range userRoleIDs {
					if allowedRole.ID == userRoleID {
						hasPermission = true
						break
					}
				}
				if hasPermission {
					break
				}
			}
			if !hasPermission {
				canExecute = false
				reason = "Insufficient permissions"
			}
		}

		// Convert requirements
		var requirements []models.TransitionRequirementResponse
		for _, req := range trans.Requirements {
			requirements = append(requirements, models.ToTransitionRequirementResponse(&req))
		}

		responses[i] = models.AvailableTransitionResponse{
			Transition:   models.ToWorkflowTransitionResponse(&trans),
			CanExecute:   canExecute,
			Requirements: requirements,
			Reason:       reason,
		}
	}

	return responses, nil
}

func (s *incidentService) GetTransitionHistory(ctx context.Context, incidentID uuid.UUID) ([]models.TransitionHistoryResponse, error) {
	history, err := s.incidentRepo.GetTransitionHistory(ctx, incidentID)
	if err != nil {
		return nil, err
	}

	responses := make([]models.TransitionHistoryResponse, len(history))
	for i, h := range history {
		responses[i] = models.ToTransitionHistoryResponse(&h)
	}

	return responses, nil
}

// Comments

func (s *incidentService) AddComment(ctx context.Context, incidentID uuid.UUID, req *models.IncidentCommentRequest, authorID uuid.UUID) (*models.IncidentCommentResponse, error) {
	comment := &models.IncidentComment{
		IncidentID: incidentID,
		AuthorID:   authorID,
		Content:    req.Content,
		IsInternal: req.IsInternal,
	}

	if err := s.incidentRepo.CreateComment(ctx, comment); err != nil {
		return nil, err
	}

	created, err := s.incidentRepo.FindCommentByID(ctx, comment.ID)
	if err != nil {
		return nil, err
	}

	// Create revision for comment added
	authorName := ""
	if created.Author != nil {
		authorName = created.Author.Email
	}
	description := fmt.Sprintf("Comment added by %s - %s", authorName, truncateString(req.Content, 50))
	_ = s.CreateRevision(ctx, incidentID, models.RevisionActionCommentAdded, description, nil, authorID)

	resp := models.ToIncidentCommentResponse(created)
	return &resp, nil
}

func (s *incidentService) ListComments(ctx context.Context, incidentID uuid.UUID) ([]models.IncidentCommentResponse, error) {
	comments, err := s.incidentRepo.ListComments(ctx, incidentID)
	if err != nil {
		return nil, err
	}

	responses := make([]models.IncidentCommentResponse, len(comments))
	for i, c := range comments {
		responses[i] = models.ToIncidentCommentResponse(&c)
	}

	return responses, nil
}

func (s *incidentService) UpdateComment(ctx context.Context, commentID uuid.UUID, req *models.IncidentCommentRequest, userID uuid.UUID) (*models.IncidentCommentResponse, error) {
	comment, err := s.incidentRepo.FindCommentByID(ctx, commentID)
	if err != nil {
		return nil, err
	}

	// Only author can update their comment
	if comment.AuthorID != userID {
		return nil, errors.New("you can only edit your own comments")
	}

	oldContent := comment.Content
	incidentID := comment.IncidentID

	comment.Content = req.Content
	comment.IsInternal = req.IsInternal

	if err := s.incidentRepo.UpdateComment(ctx, comment); err != nil {
		return nil, err
	}

	// Create revision for comment modified
	changes := []models.IncidentFieldChange{
		{
			FieldName:  "comment",
			FieldLabel: "Comment",
			OldValue:   &oldContent,
			NewValue:   &req.Content,
		},
	}
	description := fmt.Sprintf("Comment modified - %s", truncateString(req.Content, 50))
	_ = s.CreateRevision(ctx, incidentID, models.RevisionActionCommentModified, description, changes, userID)

	resp := models.ToIncidentCommentResponse(comment)
	return &resp, nil
}

func (s *incidentService) DeleteComment(ctx context.Context, commentID uuid.UUID, userID uuid.UUID) error {
	comment, err := s.incidentRepo.FindCommentByID(ctx, commentID)
	if err != nil {
		return err
	}

	// Only author can delete their comment
	if comment.AuthorID != userID {
		return errors.New("you can only delete your own comments")
	}

	incidentID := comment.IncidentID
	oldContent := comment.Content

	if err := s.incidentRepo.DeleteComment(ctx, commentID); err != nil {
		return err
	}

	// Create revision for comment deleted
	description := fmt.Sprintf("Comment deleted - %s", truncateString(oldContent, 50))
	_ = s.CreateRevision(ctx, incidentID, models.RevisionActionCommentDeleted, description, nil, userID)

	return nil
}

// Attachments

func (s *incidentService) AddAttachment(ctx context.Context, incidentID uuid.UUID, attachment *models.IncidentAttachment) (*models.IncidentAttachmentResponse, error) {
	attachment.IncidentID = incidentID

	if err := s.incidentRepo.CreateAttachment(ctx, attachment); err != nil {
		return nil, err
	}

	created, err := s.incidentRepo.FindAttachmentByID(ctx, attachment.ID)
	if err != nil {
		return nil, err
	}

	// Create revision for attachment added
	description := fmt.Sprintf("Attachment added - %s", attachment.FileName)
	_ = s.CreateRevision(ctx, incidentID, models.RevisionActionAttachmentAdded, description, nil, attachment.UploadedByID)

	url, err := s.storage.GetFileURL(ctx, created.FilePath)
	if err != nil {
		// Log the error but don't fail the operation
		fmt.Printf("Warning: failed to get presigned URL for attachment %s: %v\n", created.ID, err)
	}

	resp := models.ToIncidentAttachmentResponse(created, url)
	return &resp, nil
}

func (s *incidentService) ListAttachments(ctx context.Context, incidentID uuid.UUID) ([]models.IncidentAttachmentResponse, error) {
	attachments, err := s.incidentRepo.ListAttachments(ctx, incidentID)
	if err != nil {
		return nil, err
	}

	responses := make([]models.IncidentAttachmentResponse, len(attachments))
	for i, a := range attachments {
		url, err := s.storage.GetFileURL(ctx, a.FilePath)
		if err != nil {
			// Log the error but don't fail the operation
			fmt.Printf("Warning: failed to get presigned URL for attachment %s: %v\n", a.ID, err)
		}
		responses[i] = models.ToIncidentAttachmentResponse(&a, url)
	}

	return responses, nil
}

func (s *incidentService) DeleteAttachment(ctx context.Context, attachmentID uuid.UUID, userID uuid.UUID) error {
	attachment, err := s.incidentRepo.FindAttachmentByID(ctx, attachmentID)
	if err != nil {
		return err
	}

	// Only uploader can delete their attachment
	if attachment.UploadedByID != userID {
		return errors.New("you can only delete your own attachments")
	}

	incidentID := attachment.IncidentID
	fileName := attachment.FileName

	// TODO: Delete file from storage

	if err := s.incidentRepo.DeleteAttachment(ctx, attachmentID); err != nil {
		return err
	}

	// Create revision for attachment removed
	description := fmt.Sprintf("Attachment removed - %s", fileName)
	_ = s.CreateRevision(ctx, incidentID, models.RevisionActionAttachmentRemoved, description, nil, userID)

	return nil
}

func (s *incidentService) GetAttachment(ctx context.Context, attachmentID uuid.UUID) (*models.IncidentAttachment, error) {
	return s.incidentRepo.FindAttachmentByID(ctx, attachmentID)
}

// Assignment

func (s *incidentService) AssignIncident(ctx context.Context, incidentID, assigneeID, userID uuid.UUID) (*models.IncidentResponse, error) {
	// Get incident before change to track old assignee
	incident, err := s.incidentRepo.FindByIDWithRelations(ctx, incidentID)
	if err != nil {
		return nil, err
	}

	oldAssigneeName := "Unassigned"
	if incident.Assignee != nil {
		oldAssigneeName = incident.Assignee.FirstName + " " + incident.Assignee.LastName
	}

	if err := s.incidentRepo.AssignIncident(ctx, incidentID, assigneeID); err != nil {
		return nil, err
	}

	// Fetch updated incident to get new assignee name
	updated, err := s.incidentRepo.FindByIDWithRelations(ctx, incidentID)
	if err != nil {
		return nil, err
	}

	newAssigneeName := "Unassigned"
	if updated.Assignee != nil {
		newAssigneeName = updated.Assignee.FirstName + " " + updated.Assignee.LastName
	}

	// Create revision for assignment change
	changes := []models.IncidentFieldChange{
		{
			FieldName:  "assignee_id",
			FieldLabel: "Assigned To",
			OldValue:   &oldAssigneeName,
			NewValue:   &newAssigneeName,
		},
	}
	description := fmt.Sprintf("AssignedTo changed from %s to %s", oldAssigneeName, newAssigneeName)
	_ = s.CreateRevision(ctx, incidentID, models.RevisionActionAssigneeChanged, description, changes, userID)

	resp := models.ToIncidentResponse(updated)
	return &resp, nil
}

// Stats and user queries

func (s *incidentService) GetStats(ctx context.Context, filter *models.IncidentFilter) (*models.IncidentStatsResponse, error) {
	return s.incidentRepo.GetStats(ctx, filter)
}

func (s *incidentService) GetMyAssigned(ctx context.Context, userID uuid.UUID, recordType string, page, limit int) ([]models.IncidentResponse, int64, error) {
	incidents, total, err := s.incidentRepo.GetAssignedToUser(ctx, userID, recordType, page, limit)
	if err != nil {
		return nil, 0, err
	}

	responses := make([]models.IncidentResponse, len(incidents))
	for i, inc := range incidents {
		responses[i] = models.ToIncidentResponse(&inc)
	}

	return responses, total, nil
}

func (s *incidentService) GetMyReported(ctx context.Context, userID uuid.UUID, recordType string, page, limit int) ([]models.IncidentResponse, int64, error) {
	incidents, total, err := s.incidentRepo.GetReportedByUser(ctx, userID, recordType, page, limit)
	if err != nil {
		return nil, 0, err
	}

	responses := make([]models.IncidentResponse, len(incidents))
	for i, inc := range incidents {
		responses[i] = models.ToIncidentResponse(&inc)
	}

	return responses, total, nil
}

func (s *incidentService) GetSLABreached(ctx context.Context) ([]models.IncidentResponse, error) {
	incidents, err := s.incidentRepo.GetSLABreachedIncidents(ctx)
	if err != nil {
		return nil, err
	}

	responses := make([]models.IncidentResponse, len(incidents))
	for i, inc := range incidents {
		responses[i] = models.ToIncidentResponse(&inc)
	}

	return responses, nil
}

// SLA monitoring

func (s *incidentService) CheckAndUpdateSLABreaches(ctx context.Context) error {
	incidents, err := s.incidentRepo.GetSLABreachedIncidents(ctx)
	if err != nil {
		return err
	}

	now := time.Now()
	for _, incident := range incidents {
		if incident.SLADeadline != nil && incident.SLADeadline.Before(now) && !incident.SLABreached {
			if err := s.incidentRepo.UpdateSLABreached(ctx, incident.ID, true); err != nil {
				// Log error but continue
				fmt.Printf("Failed to update SLA breach for incident %s: %v\n", incident.ID, err)
			}
		}
	}

	return nil
}

// Revisions

func (s *incidentService) ListRevisions(ctx context.Context, incidentID uuid.UUID, filter *models.IncidentRevisionFilter) ([]models.IncidentRevisionResponse, int64, error) {
	filter.IncidentID = incidentID
	revisions, total, err := s.incidentRepo.ListRevisions(ctx, filter)
	if err != nil {
		return nil, 0, err
	}

	responses := make([]models.IncidentRevisionResponse, len(revisions))
	for i, rev := range revisions {
		responses[i] = models.ToIncidentRevisionResponse(&rev)
	}

	return responses, total, nil
}

func (s *incidentService) CreateRevision(ctx context.Context, incidentID uuid.UUID, actionType models.IncidentRevisionActionType, description string, changes []models.IncidentFieldChange, userID uuid.UUID) error {
	// Get the next revision number
	revNum, err := s.incidentRepo.GetNextRevisionNumber(ctx, incidentID)
	if err != nil {
		return err
	}

	// Marshal changes to JSON
	var changesJSON string
	if len(changes) > 0 {
		changesBytes, err := json.Marshal(changes)
		if err != nil {
			return err
		}
		changesJSON = string(changesBytes)
	}

	revision := &models.IncidentRevision{
		IncidentID:        incidentID,
		RevisionNumber:    revNum,
		ActionType:        actionType,
		ActionDescription: description,
		Changes:           changesJSON,
		PerformedByID:     userID,
		CreatedAt:         time.Now(),
	}

	return s.incidentRepo.CreateRevision(ctx, revision)
}

// Complaint operations

func (s *incidentService) CreateComplaint(ctx context.Context, req *models.CreateComplaintRequest, creatorID uuid.UUID) (*models.IncidentResponse, error) {
	// Parse workflow ID
	workflowID, err := uuid.Parse(req.WorkflowID)
	if err != nil {
		return nil, errors.New("invalid workflow_id")
	}

	// Get the initial state of the workflow
	initialState, err := s.workflowRepo.GetInitialState(ctx, workflowID)
	if err != nil {
		return nil, errors.New("workflow has no initial state configured")
	}

	// Generate complaint number
	complaintNumber, err := s.incidentRepo.GenerateComplaintNumber(ctx)
	if err != nil {
		return nil, err
	}

	// Parse classification ID
	classificationID, err := uuid.Parse(req.ClassificationID)
	if err != nil {
		return nil, errors.New("invalid classification_id")
	}

	complaint := &models.Incident{
		IncidentNumber:   complaintNumber,
		Title:            req.Title,
		Description:      req.Description,
		RecordType:       "complaint",
		ClassificationID: &classificationID,
		WorkflowID:       workflowID,
		CurrentStateID:   initialState.ID,
		Channel:          req.Channel,
	}

	// Set reporter - use provided reporter_id or fall back to creator
	if req.ReporterID != nil && *req.ReporterID != "" {
		reporterID, err := uuid.Parse(*req.ReporterID)
		if err == nil {
			complaint.ReporterID = &reporterID
		}
	} else {
		complaint.ReporterID = &creatorID
	}

	// Parse optional source incident ID
	if req.SourceIncidentID != nil && *req.SourceIncidentID != "" {
		sourceID, err := uuid.Parse(*req.SourceIncidentID)
		if err == nil {
			// Validate source incident exists
			_, err := s.incidentRepo.FindByID(ctx, sourceID)
			if err != nil {
				return nil, errors.New("source incident not found")
			}
			complaint.SourceIncidentID = &sourceID
		}
	}

	// Parse optional UUIDs
	if req.AssigneeID != nil && *req.AssigneeID != "" {
		assigneeID, err := uuid.Parse(*req.AssigneeID)
		if err == nil {
			complaint.AssigneeID = &assigneeID
		}
	}

	if req.DepartmentID != nil && *req.DepartmentID != "" {
		deptID, err := uuid.Parse(*req.DepartmentID)
		if err == nil {
			complaint.DepartmentID = &deptID
		}
	}

	if req.LocationID != nil && *req.LocationID != "" {
		locID, err := uuid.Parse(*req.LocationID)
		if err == nil {
			complaint.LocationID = &locID
		}
	}

	// Calculate SLA deadline based on initial state
	if initialState.SLAHours != nil && *initialState.SLAHours > 0 {
		deadline := time.Now().Add(time.Duration(*initialState.SLAHours) * time.Hour)
		complaint.SLADeadline = &deadline
	}

	if err := s.incidentRepo.Create(ctx, complaint); err != nil {
		return nil, err
	}

	// Set lookup values if provided
	if len(req.LookupValueIDs) > 0 {
		var lookupValues []models.LookupValue
		for _, idStr := range req.LookupValueIDs {
			id, err := uuid.Parse(idStr)
			if err == nil {
				lookupValues = append(lookupValues, models.LookupValue{ID: id})
			}
		}
		if err := s.incidentRepo.SetLookupValues(ctx, complaint.ID, lookupValues); err != nil {
			fmt.Printf("Warning: failed to set lookup values: %v\n", err)
		}
	}

	// Fetch with relations
	created, err := s.incidentRepo.FindByIDWithRelations(ctx, complaint.ID)
	if err != nil {
		return nil, err
	}

	// Create initial revision
	description := fmt.Sprintf("Complaint %s created", complaintNumber)
	_ = s.CreateRevision(ctx, complaint.ID, models.RevisionActionCreated, description, nil, creatorID)

	resp := models.ToIncidentResponse(created)
	return &resp, nil
}

func (s *incidentService) IncrementEvaluationCount(ctx context.Context, id uuid.UUID) error {
	// Verify it's a complaint and is closed
	incident, err := s.incidentRepo.FindByIDWithRelations(ctx, id)
	if err != nil {
		return errors.New("complaint not found")
	}

	if incident.RecordType != "complaint" {
		return errors.New("can only evaluate complaints")
	}

	// Check if complaint is in a terminal state (closed)
	if incident.CurrentState == nil || incident.CurrentState.StateType != "terminal" {
		return errors.New("can only evaluate closed complaints")
	}

	return s.incidentRepo.IncrementEvaluationCount(ctx, id)
}

func (s *incidentService) CreateQuery(ctx context.Context, req *models.CreateQueryRequest, creatorID uuid.UUID) (*models.IncidentResponse, error) {
	// Parse workflow ID
	workflowID, err := uuid.Parse(req.WorkflowID)
	if err != nil {
		return nil, errors.New("invalid workflow_id")
	}

	// Get the initial state of the workflow
	initialState, err := s.workflowRepo.GetInitialState(ctx, workflowID)
	if err != nil {
		return nil, errors.New("workflow has no initial state configured")
	}

	// Generate query number
	queryNumber, err := s.incidentRepo.GenerateQueryNumber(ctx)
	if err != nil {
		return nil, err
	}

	// Parse classification ID
	classificationID, err := uuid.Parse(req.ClassificationID)
	if err != nil {
		return nil, errors.New("invalid classification_id")
	}

	query := &models.Incident{
		IncidentNumber:   queryNumber,
		Title:            req.Title,
		Description:      req.Description,
		RecordType:       "query",
		ClassificationID: &classificationID,
		WorkflowID:       workflowID,
		CurrentStateID:   initialState.ID,
		Channel:          req.Channel,
		ReporterID:       &creatorID,
	}

	// Parse optional source incident ID
	if req.SourceIncidentID != nil && *req.SourceIncidentID != "" {
		sourceID, err := uuid.Parse(*req.SourceIncidentID)
		if err == nil {
			// Validate source incident exists
			_, err := s.incidentRepo.FindByID(ctx, sourceID)
			if err != nil {
				return nil, errors.New("source incident not found")
			}
			query.SourceIncidentID = &sourceID
		}
	}

	// Parse optional UUIDs
	if req.AssigneeID != nil && *req.AssigneeID != "" {
		assigneeID, err := uuid.Parse(*req.AssigneeID)
		if err == nil {
			query.AssigneeID = &assigneeID
		}
	}

	if req.DepartmentID != nil && *req.DepartmentID != "" {
		deptID, err := uuid.Parse(*req.DepartmentID)
		if err == nil {
			query.DepartmentID = &deptID
		}
	}

	if req.LocationID != nil && *req.LocationID != "" {
		locID, err := uuid.Parse(*req.LocationID)
		if err == nil {
			query.LocationID = &locID
		}
	}

	// Calculate SLA deadline based on initial state
	if initialState.SLAHours != nil && *initialState.SLAHours > 0 {
		deadline := time.Now().Add(time.Duration(*initialState.SLAHours) * time.Hour)
		query.SLADeadline = &deadline
	}

	if err := s.incidentRepo.Create(ctx, query); err != nil {
		return nil, err
	}

	// Set lookup values if provided
	if len(req.LookupValueIDs) > 0 {
		var lookupValues []models.LookupValue
		for _, idStr := range req.LookupValueIDs {
			id, err := uuid.Parse(idStr)
			if err == nil {
				lookupValues = append(lookupValues, models.LookupValue{ID: id})
			}
		}
		if err := s.incidentRepo.SetLookupValues(ctx, query.ID, lookupValues); err != nil {
			fmt.Printf("Warning: failed to set lookup values: %v\n", err)
		}
	}

	// Fetch with relations
	created, err := s.incidentRepo.FindByIDWithRelations(ctx, query.ID)
	if err != nil {
		return nil, err
	}

	// Create initial revision
	description := fmt.Sprintf("Query %s created", queryNumber)
	_ = s.CreateRevision(ctx, query.ID, models.RevisionActionCreated, description, nil, creatorID)

	resp := models.ToIncidentResponse(created)
	return &resp, nil
}

// Helper function to truncate string for descriptions
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
