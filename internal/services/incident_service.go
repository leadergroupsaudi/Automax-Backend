package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/google/uuid"
)

type IncidentService interface {
	// Incident CRUD
	CreateIncident(ctx context.Context, req *models.IncidentCreateRequest, reporterID uuid.UUID) (*models.IncidentResponse, error)
	GetIncident(ctx context.Context, id uuid.UUID) (*models.IncidentDetailResponse, error)
	ListIncidents(ctx context.Context, filter *models.IncidentFilter) ([]models.IncidentResponse, int64, error)
	UpdateIncident(ctx context.Context, id uuid.UUID, req *models.IncidentUpdateRequest, userID uuid.UUID) (*models.IncidentResponse, error)
	DeleteIncident(ctx context.Context, id uuid.UUID) error

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

	// Assignment
	AssignIncident(ctx context.Context, incidentID, assigneeID, userID uuid.UUID) (*models.IncidentResponse, error)

	// Stats and user queries
	GetStats(ctx context.Context, filter *models.IncidentFilter) (*models.IncidentStatsResponse, error)
	GetMyAssigned(ctx context.Context, userID uuid.UUID, page, limit int) ([]models.IncidentResponse, int64, error)
	GetMyReported(ctx context.Context, userID uuid.UUID, page, limit int) ([]models.IncidentResponse, int64, error)
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
}

func NewIncidentService(incidentRepo repository.IncidentRepository, workflowRepo repository.WorkflowRepository) IncidentService {
	return &incidentService{
		incidentRepo: incidentRepo,
		workflowRepo: workflowRepo,
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
		Priority:       req.Priority,
		Severity:       req.Severity,
		ReporterID:     &reporterID,
		ReporterEmail:  req.ReporterEmail,
		ReporterName:   req.ReporterName,
		CustomFields:   req.CustomFields,
	}

	// Set default priority and severity if not provided
	if incident.Priority == 0 {
		incident.Priority = 3
	}
	if incident.Severity == 0 {
		incident.Severity = 3
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

	resp := models.ToIncidentDetailResponse(incident)
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
	if req.Priority != nil && *req.Priority != incident.Priority {
		oldVal := fmt.Sprintf("%d", incident.Priority)
		newVal := fmt.Sprintf("%d", *req.Priority)
		changes = append(changes, models.IncidentFieldChange{
			FieldName:  "priority",
			FieldLabel: "Priority",
			OldValue:   &oldVal,
			NewValue:   &newVal,
		})
		descriptions = append(descriptions, fmt.Sprintf("Priority changed from %s to %s", oldVal, newVal))
		incident.Priority = *req.Priority
	}
	if req.Severity != nil && *req.Severity != incident.Severity {
		oldVal := fmt.Sprintf("%d", incident.Severity)
		newVal := fmt.Sprintf("%d", *req.Severity)
		changes = append(changes, models.IncidentFieldChange{
			FieldName:  "severity",
			FieldLabel: "Severity",
			OldValue:   &oldVal,
			NewValue:   &newVal,
		})
		descriptions = append(descriptions, fmt.Sprintf("Severity changed from %s to %s", oldVal, newVal))
		incident.Severity = *req.Severity
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
	if transition.AssignUserID != nil {
		// Static user assignment
		updates["assignee_id"] = *transition.AssignUserID
	} else if transition.AutoMatchUser && transition.AssignmentRoleID != nil && req.UserID != nil && *req.UserID != "" {
		// Auto-match with user selection
		userAssignID, err := uuid.Parse(*req.UserID)
		if err == nil {
			updates["assignee_id"] = userAssignID
		}
	}

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
	if err := s.incidentRepo.UpdateFields(ctx, incidentID, updates); err != nil {
		return nil, err
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

	resp := models.ToIncidentAttachmentResponse(created)
	return &resp, nil
}

func (s *incidentService) ListAttachments(ctx context.Context, incidentID uuid.UUID) ([]models.IncidentAttachmentResponse, error) {
	attachments, err := s.incidentRepo.ListAttachments(ctx, incidentID)
	if err != nil {
		return nil, err
	}

	responses := make([]models.IncidentAttachmentResponse, len(attachments))
	for i, a := range attachments {
		responses[i] = models.ToIncidentAttachmentResponse(&a)
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

func (s *incidentService) GetMyAssigned(ctx context.Context, userID uuid.UUID, page, limit int) ([]models.IncidentResponse, int64, error) {
	incidents, total, err := s.incidentRepo.GetAssignedToUser(ctx, userID, page, limit)
	if err != nil {
		return nil, 0, err
	}

	responses := make([]models.IncidentResponse, len(incidents))
	for i, inc := range incidents {
		responses[i] = models.ToIncidentResponse(&inc)
	}

	return responses, total, nil
}

func (s *incidentService) GetMyReported(ctx context.Context, userID uuid.UUID, page, limit int) ([]models.IncidentResponse, int64, error) {
	incidents, total, err := s.incidentRepo.GetReportedByUser(ctx, userID, page, limit)
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

// Helper function to truncate string for descriptions
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
