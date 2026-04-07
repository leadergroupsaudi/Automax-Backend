package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/internal/storage"
	"github.com/automax/backend/internal/utils"
	"github.com/automax/backend/pkg/constants"
	pkgutils "github.com/automax/backend/pkg/utils"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var ErrDuplicateIncident = errors.New("duplicate_incident")
var ErrInvalidLocation = errors.New("invalid_location")
var ErrEditNotAllowed = errors.New("edit_not_allowed_in_current_state")

type IncidentService interface {
	// Incident CRUD
	CreateIncident(ctx context.Context, req *models.IncidentCreateRequest, reporterID uuid.UUID) (*models.IncidentResponse, error)
	GetIncident(ctx context.Context, id uuid.UUID) (*models.IncidentDetailResponse, error)
	ListIncidents(ctx context.Context, filter *models.IncidentFilter) ([]models.IncidentResponse, int64, error)
	UpdateIncident(ctx context.Context, id uuid.UUID, req *models.IncidentUpdateRequest, userID uuid.UUID, userRoleIDs []uuid.UUID) (*models.IncidentResponse, error)
	DeleteIncident(ctx context.Context, id uuid.UUID) error
	FindByIDWithLast6DigitValidation(ctx context.Context, id uuid.UUID, last6Digits string) (*models.IncidentResponse, error)

	// Convert incident to request
	ConvertToRequest(ctx context.Context, incidentID uuid.UUID, req *models.ConvertToRequestRequest, userID uuid.UUID, userRoleIDs []uuid.UUID) (*models.ConvertToRequestResponse, error)
	CanConvertToRequest(ctx context.Context, incidentID uuid.UUID, userRoleIDs []uuid.UUID) (bool, string, error)
	BulkConvertToRequest(ctx context.Context, req *models.BulkConvertToRequestRequest, userID uuid.UUID, userRoleIDs []uuid.UUID) (*models.BulkConvertToRequestResponse, error)

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
	GetStatsV2(ctx context.Context, filter *models.IncidentFilter) (*models.IncidentStatsResponseV2, error)
	GetPriorityCounts(ctx context.Context, filter *models.IncidentFilter) (map[string]int64, error)
	GetMyAssigned(ctx context.Context, userID uuid.UUID, recordType string, page, limit int) ([]models.IncidentResponse, int64, error)
	GetMyReported(ctx context.Context, userID uuid.UUID, recordType string, page, limit int) ([]models.IncidentResponse, int64, error)
	GetSLABreached(ctx context.Context) ([]models.IncidentResponse, error)

	// SLA monitoring
	CheckAndUpdateSLABreaches(ctx context.Context) error

	// SetReadyToCloseService wires in the ReadyToCloseService (called post-construction).
	SetReadyToCloseService(rtcService ReadyToCloseService)
	// SetNotificationService wires in the NotificationService (called post-construction).
	SetNotificationService(ns *NotificationService)
	// SetFCMService wires in the FCMService for push notifications (called post-construction).
	SetFCMService(fcm *FCMService)

	// Revisions
	ListRevisions(ctx context.Context, incidentID uuid.UUID, filter *models.IncidentRevisionFilter) ([]models.IncidentRevisionResponse, int64, error)
	CreateRevision(ctx context.Context, incidentID uuid.UUID, actionType models.IncidentRevisionActionType, description string, changes []models.IncidentFieldChange, userID uuid.UUID) error
	// SetUserService wires in the UserService (called post-construction to avoid circular deps).
	SetUserService(us UserService)

	// Closed incident editing
	UpdateClosedIncidentSummary(ctx context.Context, incidentID uuid.UUID, userID uuid.UUID, newDescription string, reason string) (*models.IncidentResponse, error)
}

type incidentService struct {
	incidentRepo        repository.IncidentRepository
	incidentMergeRepo   repository.IncidentMergeRepository
	workflowRepo        repository.WorkflowRepository
	userRepo            repository.UserRepository
	deptRepo            repository.DepartmentRepository
	rejectionLogRepo    repository.RejectionLogRepository
	classificationRepo  repository.ClassificationRepository
	roleRepo            repository.RoleRepository
	storage             *storage.MinIOStorage
	db                  *gorm.DB
	wsHub               *WSHub
	readyToCloseService ReadyToCloseService
	notificationService *NotificationService
	fcmService          *FCMService
	userService         UserService
}

func NewIncidentService(
	incidentRepo repository.IncidentRepository,
	incidentMergeRepo repository.IncidentMergeRepository,
	workflowRepo repository.WorkflowRepository,
	userRepo repository.UserRepository,
	deptRepo repository.DepartmentRepository,
	classificationRepo repository.ClassificationRepository,
	rejectionLogRepo repository.RejectionLogRepository,
	roleRepo repository.RoleRepository,
	storage *storage.MinIOStorage,
	db *gorm.DB,
	wsHub *WSHub,
) IncidentService {
	return &incidentService{
		incidentRepo:       incidentRepo,
		incidentMergeRepo:  incidentMergeRepo,
		workflowRepo:       workflowRepo,
		userRepo:           userRepo,
		deptRepo:           deptRepo,
		rejectionLogRepo:   rejectionLogRepo,
		classificationRepo: classificationRepo,
		roleRepo:           roleRepo,
		storage:            storage,
		db:                 db,
		wsHub:              wsHub,
	}
}

// SetReadyToCloseService wires the ReadyToCloseService into the incident service.
// Called after both services are constructed to avoid circular dependency.
func (s *incidentService) SetReadyToCloseService(rtcService ReadyToCloseService) {
	s.readyToCloseService = rtcService
}

// SetNotificationService wires the NotificationService into the incident service.
func (s *incidentService) SetNotificationService(ns *NotificationService) {
	s.notificationService = ns
}

// SetFCMService wires the FCMService into the incident service for push notifications.
func (s *incidentService) SetFCMService(fcm *FCMService) {
	s.fcmService = fcm
}

// SetUserService wires the UserService into the incident service.
func (s *incidentService) SetUserService(us UserService) {
	s.userService = us
}

// calculateSLADeadline calculates the SLA deadline based on classification criticality
// Falls back to workflow state SLA hours if no criticality-based setting exists
func (s *incidentService) calculateSLADeadline(ctx context.Context, classificationID *uuid.UUID, lookupValueIDs []string, workflowSLAHours *int) (*time.Time, error) {
	var deadline *time.Time

	// Try to get classification-based criticality SLA first
	if classificationID != nil && len(lookupValueIDs) > 0 {
		// Get priority code from lookup values
		for _, lookupIDStr := range lookupValueIDs {
			lookupID, err := uuid.Parse(lookupIDStr)
			if err != nil {
				continue
			}

			// Get the lookup value to check if it's a priority
			var lookupValue models.LookupValue
			err = s.db.WithContext(ctx).
				Preload("Category").
				First(&lookupValue, "id = ?", lookupID).Error
			if err != nil {
				continue
			}

			// Check if this lookup value belongs to PRIORITY category
			if lookupValue.Category == nil || lookupValue.Category.Code != "PRIORITY" {
				continue
			}

			// Found priority - get classification criticality setting
			criticality, err := s.classificationRepo.GetCriticalityByClassificationAndPriorityCode(ctx, *classificationID, lookupValue.Code)
			if err != nil {
				// No criticality setting found for this priority, continue to fallback
				break
			}

			// Calculate deadline from hours and minutes
			if criticality.MaxClosingHours > 0 || criticality.MaxClosingMinutes > 0 {
				totalDuration := time.Duration(criticality.MaxClosingHours)*time.Hour + time.Duration(criticality.MaxClosingMinutes)*time.Minute
				deadlineTime := time.Now().Add(totalDuration)
				deadline = &deadlineTime
				return deadline, nil
			}
		}
	}

	// Fallback to workflow state SLA hours
	if workflowSLAHours != nil && *workflowSLAHours > 0 {
		deadlineTime := time.Now().Add(time.Duration(*workflowSLAHours) * time.Hour)
		deadline = &deadlineTime
	}

	return deadline, nil
}

// Incident CRUD

func (s *incidentService) CreateIncident(ctx context.Context, req *models.IncidentCreateRequest, reporterID uuid.UUID) (*models.IncidentResponse, error) {
	clientCode := strings.TrimSpace(os.Getenv("CLIENT_CODE"))
	if strings.EqualFold(req.Source, constants.INCIDENT_SOURCE.IVR) && strings.EqualFold(clientCode, constants.CLIENT_CODE.EPM940) {
		// For EPM940, if source is IVR, then fetch user based on mobile no. of citizen
		user, err := s.userRepo.FindByMobile(ctx, req.ReporterPhone)
		if err != nil {
			fmt.Printf("CreateIncident: Errro fetching user by mobile: %v\n", err)
			return nil, err
		}

		if user == nil || user.ID == uuid.Nil {
			role, err := s.roleRepo.FindByCode(ctx, constants.ROLES.CITIZEN)
			if err != nil {
				return nil, err
			}

			registerReq := &models.UserRegisterRequest{
				Phone:     req.ReporterPhone,
				Email:     fmt.Sprintf("%s_%s@%s", constants.PREFIX.IVR_EMAIL, req.ReporterPhone, constants.APP.DOMAIN),
				FirstName: constants.ROLES.CITIZEN,
				LastName:  req.ReporterName,
				Username:  fmt.Sprintf("%s_%s", constants.ROLES.CITIZEN, req.ReporterPhone),
				Password:  pkgutils.GenerateRandomPassword(12),
			}

			if role != nil && role.ID != uuid.Nil {
				registerReq.RoleIDs = []uuid.UUID{role.ID}
			}

			authResp, err := s.userService.Register(ctx, registerReq)
			if err != nil {
				fmt.Printf("CreateIncident: Error registering IVR citizen user: %v\n", err)
				return nil, err
			}

			reporterID = authResp.User.ID
		} else {
			reporterID = user.ID
		}

	}
	// Sources that bypass the 500m duplicate check, configurable via SKIP_DUPLICATE_CHECK_SOURCES (comma-separated)
	skipSourcesEnv := os.Getenv("SKIP_DUPLICATE_CHECK_SOURCES")
	skipSources := []string{"viusional"} // default
	if skipSourcesEnv != "" {
		parts := strings.Split(skipSourcesEnv, ",")
		skipSources = make([]string, 0, len(parts))
		for _, p := range parts {
			if trimmed := strings.TrimSpace(p); trimmed != "" {
				skipSources = append(skipSources, trimmed)
			}
		}
	}
	sourceSkipped := false
	for _, skip := range skipSources {
		if strings.EqualFold(req.Source, skip) {
			sourceSkipped = true
			break
		}
	}

	if strings.EqualFold(clientCode, "EPM940") && !sourceSkipped {
		// Check if the incoming request has latitude, longitude, and classification
		if req.Latitude != nil && req.Longitude != nil && req.ClassificationID != nil {
			classificationID, err := uuid.Parse(*req.ClassificationID)
			if err != nil {
				return nil, ErrInvalidLocation
			}

			// Find user's open incidents that have location coordinates
			openIncidents, err := s.incidentRepo.FindUserOpenIncidentsForDuplicateCheck(ctx, reporterID)
			if err != nil {
				return nil, err
			}

			if len(openIncidents) > 0 {
				// Distance threshold: block only if same classification AND within 500 meters
				maxDistanceStr := os.Getenv("MAX_INCIDENT_DISTANCE")
				incidentDistance, err := strconv.ParseFloat(maxDistanceStr, 64)
				if err != nil || incidentDistance <= 0 {
					incidentDistance = 500 // Default to 500 meters
				}

				for _, existing := range openIncidents {
					if existing.Latitude == nil || existing.Longitude == nil || existing.ClassificationID == nil {
						continue // Skip incidents without coordinates or classification
					}

					// Check if classification matches
					if *existing.ClassificationID != classificationID {
						continue // Different classification - allow creation
					}

					// Calculate distance between new incident and existing incident
					distance := utils.CalculateDistance(
						*req.Latitude,
						*req.Longitude,
						*existing.Latitude,
						*existing.Longitude,
					)

					// Block only if BOTH: same classification AND within distance threshold
					if distance <= incidentDistance {
						return nil, ErrDuplicateIncident
					}
				}
			}
		}
	}

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

	// Set record type, default to 'incident' if not provided
	recordType := req.RecordType
	if recordType == "" {
		recordType = "incident"
	}

	// Generate number based on record type
	var incidentNumber string
	switch recordType {
	case "request":
		incidentNumber, err = s.incidentRepo.GenerateRequestNumber(ctx)
	case "complaint":
		incidentNumber, err = s.incidentRepo.GenerateComplaintNumber(ctx)
	case "query":
		incidentNumber, err = s.incidentRepo.GenerateQueryNumber(ctx)
	default:
		incidentNumber, err = s.incidentRepo.GenerateIncidentNumber(ctx)
	}
	if err != nil {
		return nil, err
	}

	// Merge custom lookup fields into CustomFields JSON
	customFieldsJSON := req.CustomFields
	if len(req.CustomLookupFields) > 0 {
		// Parse existing custom fields
		var customFields map[string]interface{}
		if customFieldsJSON != "" {
			if err := json.Unmarshal([]byte(customFieldsJSON), &customFields); err != nil {
				customFields = make(map[string]interface{})
			}
		} else {
			customFields = make(map[string]interface{})
		}

		// Merge custom lookup fields
		for key, value := range req.CustomLookupFields {
			customFields[key] = value
		}

		// Convert back to JSON
		customFieldsBytes, err := json.Marshal(customFields)
		if err == nil {
			customFieldsJSON = string(customFieldsBytes)
		}
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
		CustomFields:   customFieldsJSON,
		Latitude:       req.Latitude,
		Longitude:      req.Longitude,
		Address:        req.Address,
		City:           req.City,
		State:          req.State,
		Country:        req.Country,
		PostalCode:     req.PostalCode,
		RecordType:     recordType,
		Source:         req.Source,
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

	// Calculate SLA deadline based on classification criticality (with fallback to workflow state SLA)
	var classificationID *uuid.UUID
	if req.ClassificationID != nil {
		id, err := uuid.Parse(*req.ClassificationID)
		if err == nil {
			classificationID = &id
		}
	}
	deadline, err := s.calculateSLADeadline(ctx, classificationID, req.LookupValueIDs, initialState.SLAHours)
	if err == nil && deadline != nil {
		incident.SLADeadline = deadline
	}

	// Retry logic for duplicate key errors (race condition on incident_number)
	maxRetries := 3
	for attempt := 0; attempt < maxRetries; attempt++ {
		if err := s.incidentRepo.Create(ctx, incident); err != nil {
			// Check if it's a duplicate key error
			if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "23505") {
				// Regenerate incident number and retry
				switch recordType {
				case "request":
					incident.IncidentNumber, _ = s.incidentRepo.GenerateRequestNumber(ctx)
				case "complaint":
					incident.IncidentNumber, _ = s.incidentRepo.GenerateComplaintNumber(ctx)
				case "query":
					incident.IncidentNumber, _ = s.incidentRepo.GenerateQueryNumber(ctx)
				default:
					incident.IncidentNumber, _ = s.incidentRepo.GenerateIncidentNumber(ctx)
				}
				incident.ID = uuid.New() // Generate new UUID for retry
				continue
			}
			return nil, err
		}
		break // Success, exit retry loop
	}

	// Save creation comment
	if strings.TrimSpace(req.Comment) != "" {
		comment := &models.IncidentComment{
			IncidentID: incident.ID,
			AuthorID:   reporterID,
			Content:    strings.TrimSpace(req.Comment),
			IsInternal: false,
		}
		if err := s.incidentRepo.CreateComment(ctx, comment); err != nil {
			fmt.Printf("Warning: failed to save creation comment: %v\n", err)
		}
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

	// Create initial revision to log incident creation
	description := fmt.Sprintf("%s %s created", recordType, incidentNumber)
	_ = s.CreateRevision(ctx, incident.ID, models.RevisionActionCreated, description, nil, reporterID)

	resp := models.ToIncidentResponse(created)

	// Broadcast incident creation to all broadcast clients
	if s.wsHub != nil {
		// Collect role IDs that can transition from the initial state — used by the
		// frontend to show the toast only to users who can actually act on the incident.
		var transitionRoleIDs []string
		if created.Workflow != nil {
			// Find the initial state
			var initialStateID *uuid.UUID
			for _, state := range created.Workflow.States {
				if state.StateType == "initial" {
					id := state.ID
					initialStateID = &id
					break
				}
			}
			// Collect AllowedRoles from transitions originating at the initial state
			if initialStateID != nil {
				seen := make(map[uuid.UUID]bool)
				for _, t := range created.Workflow.Transitions {
					if t.FromStateID == *initialStateID {
						for _, role := range t.AllowedRoles {
							if !seen[role.ID] {
								transitionRoleIDs = append(transitionRoleIDs, role.ID.String())
								seen[role.ID] = true
							}
						}
					}
				}
			}
		}
		s.wsHub.BroadcastToAll("incident_created", map[string]interface{}{
			"incident":            resp,
			"transition_role_ids": transitionRoleIDs, // empty slice = all users can see it
		})
	}

	return &resp, nil
}

func (s *incidentService) GetIncident(ctx context.Context, id uuid.UUID) (*models.IncidentDetailResponse, error) {
	incident, err := s.incidentRepo.FindByIDWithRelations(ctx, id)
	if err != nil {
		return nil, err
	}

	resp := models.ToIncidentDetailResponse(incident)

	// For requests created from bulk conversion, fetch all source incidents
	if incident.RecordType == "request" && len(incident.SourceIncidentIDs) > 0 {
		sourceIDs := make([]uuid.UUID, 0, len(incident.SourceIncidentIDs))
		for _, idStr := range incident.SourceIncidentIDs {
			if sourceID, err := uuid.Parse(idStr); err == nil {
				sourceIDs = append(sourceIDs, sourceID)
			}
		}

		if len(sourceIDs) > 0 {
			sourceIncidents, err := s.incidentRepo.FindByIDs(ctx, sourceIDs)
			if err == nil {
				resp.SourceIncidents = make([]models.IncidentResponse, len(sourceIncidents))
				for i, src := range sourceIncidents {
					resp.SourceIncidents[i] = models.ToIncidentResponse(&src)
				}
			} else {
				fmt.Printf("Warning: failed to fetch source incidents for request %s: %v\n", incident.IncidentNumber, err)
			}
		}
	}

	return &resp, nil
}

func (s *incidentService) FindByIDWithLast6DigitValidation(ctx context.Context, id uuid.UUID, last6Digits string) (*models.IncidentResponse, error) {
	ivrIncidentReq := &models.IncidentUpdateIVRRequest{
		IncidentID:      id,
		LastPhoneDigits: last6Digits,
	}
	incident, err := s.incidentRepo.FindByIDWithLast6DigitValidation(ctx, ivrIncidentReq)
	if err != nil {
		return nil, err
	}

	if incident == nil || incident.ID == uuid.Nil {
		return nil, errors.New("invalid incident")
	}
	log.Println("incident fetch via last 6 digit  ")

	if incident.Latitude != nil || incident.Longitude != nil || incident.Version > 1 || incident.ReporterID == nil {
		return nil, errors.New("incident has already updated location or has been updated since creation, cannot validate with last 6 digits")
	}

	resp := &models.IncidentResponse{
		ID:             incident.ID,
		IncidentNumber: incident.IncidentNumber,
		Title:          incident.Title,
		Description:    incident.Description,
		ReporterEmail:  incident.ReporterEmail,
		ReporterName:   incident.ReporterName,
		ReporterID:     *incident.ReporterID,
		CustomFields:   incident.CustomFields,
		Latitude:       incident.Latitude,
		Longitude:      incident.Longitude,
		Address:        incident.Address,
		City:           incident.City,
		State:          incident.State,
		Country:        incident.Country,
		PostalCode:     incident.PostalCode,
		SLADeadline:    incident.SLADeadline,
	}

	return resp, nil
}
func (s *incidentService) ListIncidents(ctx context.Context, filter *models.IncidentFilter) ([]models.IncidentResponse, int64, error) {
	incidents, total, err := s.incidentRepo.List(ctx, filter)
	if err != nil {
		return nil, 0, err
	}

	responses := make([]models.IncidentResponse, len(incidents))
	for i, inc := range incidents {
		responses[i] = models.ToIncidentResponse(&inc)

		// Add active viewers count from WebSocket hub
		if s.wsHub != nil {
			responses[i].ActiveViewers = s.wsHub.GetSubscriberCount(inc.ID)
		}
	}

	return responses, total, nil
}

func (s *incidentService) UpdateIncident(ctx context.Context, id uuid.UUID, req *models.IncidentUpdateRequest, userID uuid.UUID, userRoleIDs []uuid.UUID) (*models.IncidentResponse, error) {
	// Begin transaction
	tx := s.db.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	txRepo := s.incidentRepo.WithTx(tx)

	incident, err := txRepo.FindByIDWithRelations(ctx, id)
	if err != nil {
		tx.Rollback()
		return nil, err
	}

	// Enforce state-level edit restriction: if the current state has editable_roles configured,
	// the calling user must have one of those roles.
	currentState, err := s.workflowRepo.FindStateByID(ctx, incident.CurrentStateID)
	if err == nil && len(currentState.EditableRoles) > 0 {
		allowed := false
		for _, editableRole := range currentState.EditableRoles {
			for _, userRoleID := range userRoleIDs {
				if editableRole.ID == userRoleID {
					allowed = true
					break
				}
			}
			if allowed {
				break
			}
		}
		if !allowed {
			tx.Rollback()
			return nil, ErrEditNotAllowed
		}
	}

	// BLOCK: Prevent editing child incidents (merged into another)
	if incident.IsMerged && incident.MasterIncidentID != nil {
		tx.Rollback()
		return nil, errors.New("child incidents cannot be edited - they are locked after merging")
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
			changes = append(changes, models.IncidentFieldChange{
				FieldName:  "lookup_values",
				FieldLabel: "Dynamic Attributes",
			})

			// Recalculate SLA deadline when priority (or any lookup value) changes.
			// Uses the incident's effective classification at time of edit.
			effectiveClassID := incident.ClassificationID
			if req.ClassificationID != nil && *req.ClassificationID != "" {
				if parsedID, err := uuid.Parse(*req.ClassificationID); err == nil {
					effectiveClassID = &parsedID
				}
			}
			if effectiveClassID != nil {
				if newDeadline, err := s.calculateSLADeadline(ctx, effectiveClassID, req.LookupValueIDs, nil); err == nil && newDeadline != nil {
					incident.SLADeadline = newDeadline
					incident.SLABreached = false
				}
			}
		}
	}

	// Handle custom fields and custom lookup fields
	if req.CustomFields != "" || len(req.CustomLookupFields) > 0 {
		var customFields map[string]interface{}

		// Start with existing custom fields
		if incident.CustomFields != "" {
			if err := json.Unmarshal([]byte(incident.CustomFields), &customFields); err != nil {
				customFields = make(map[string]interface{})
			}
		} else {
			customFields = make(map[string]interface{})
		}

		// Update with new custom fields if provided
		if req.CustomFields != "" {
			var newCustomFields map[string]interface{}
			if err := json.Unmarshal([]byte(req.CustomFields), &newCustomFields); err == nil {
				for k, v := range newCustomFields {
					customFields[k] = v
				}
			}
		}

		// Merge custom lookup fields
		if len(req.CustomLookupFields) > 0 {
			for key, value := range req.CustomLookupFields {
				customFields[key] = value
			}
		}

		// Convert back to JSON
		customFieldsBytes, err := json.Marshal(customFields)
		if err == nil {
			incident.CustomFields = string(customFieldsBytes)
		}
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

	// Update geolocation fields
	if req.Latitude != nil && req.Latitude != incident.Latitude {
		latitude := fmt.Sprintf("%f", *req.Latitude)
		changes = append(changes, models.IncidentFieldChange{
			FieldName:  "latitude",
			FieldLabel: "Latitude",
			OldValue:   &latitude,
			NewValue:   nil,
		})
		incident.Latitude = req.Latitude
	}

	if req.Longitude != nil && req.Longitude != incident.Longitude {
		longitude := fmt.Sprintf("%f", *req.Longitude)
		changes = append(changes, models.IncidentFieldChange{
			FieldName:  "longitude",
			FieldLabel: "Longitude",
			OldValue:   &longitude,
			NewValue:   nil,
		})
		incident.Longitude = req.Longitude
	}

	if req.Address != "" {
		incident.Address = req.Address
	}
	if req.City != "" {
		incident.City = req.City
	}
	if req.State != "" {
		incident.State = req.State
	}
	if req.Country != "" {
		incident.Country = req.Country
	}
	if req.PostalCode != "" {
		incident.PostalCode = req.PostalCode
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

	// Build updates map for optimistic locking
	updates := make(map[string]interface{})
	if req.Title != "" {
		updates["title"] = incident.Title
	}
	if req.Description != "" {
		updates["description"] = incident.Description
	}
	if incident.ClassificationID != nil {
		updates["classification_id"] = *incident.ClassificationID
	} else if req.ClassificationID != nil && *req.ClassificationID == "" {
		updates["classification_id"] = nil
	}
	if incident.AssigneeID != nil {
		updates["assignee_id"] = *incident.AssigneeID
	} else if req.AssigneeID != nil && *req.AssigneeID == "" {
		updates["assignee_id"] = nil
	}
	if incident.DepartmentID != nil {
		updates["department_id"] = *incident.DepartmentID
	} else if req.DepartmentID != nil && *req.DepartmentID == "" {
		updates["department_id"] = nil
	}
	if incident.LocationID != nil {
		updates["location_id"] = *incident.LocationID
	} else if req.LocationID != nil && *req.LocationID == "" {
		updates["location_id"] = nil
	}

	log.Println("req", req.Latitude, req.Longitude)
	log.Println(req)

	if req.Latitude != nil {
		updates["latitude"] = *req.Latitude
	}
	if req.Longitude != nil {
		updates["longitude"] = *req.Longitude
	}
	if req.Address != "" {
		updates["address"] = incident.Address
	}
	if req.City != "" {
		updates["city"] = incident.City
	}
	if req.State != "" {
		updates["state"] = incident.State
	}
	if req.Country != "" {
		updates["country"] = incident.Country
	}
	if req.PostalCode != "" {
		updates["postal_code"] = incident.PostalCode
	}
	if incident.DueDate != nil {
		updates["due_date"] = *incident.DueDate
	} else if req.DueDate != nil && *req.DueDate == "" {
		updates["due_date"] = nil
	}
	if incident.CustomFields != "" {
		updates["custom_fields"] = incident.CustomFields
	}
	// Persist recalculated SLA deadline when lookup values were updated
	if req.LookupValueIDs != nil && incident.SLADeadline != nil {
		updates["sla_deadline"] = *incident.SLADeadline
		updates["sla_breached"] = incident.SLABreached
	}

	log.Println(len(updates), len(changes))
	if len(updates) == 0 || len(changes) == 0 {
		tx.Rollback()
		return nil, errors.New("no changes detected")
	}
	// Execute optimistic lock update
	if err := txRepo.UpdateFieldsWithVersion(ctx, id, updates, req.Version); err != nil {
		tx.Rollback()
		if err == repository.ErrVersionMismatch {
			return nil, fmt.Errorf("conflict: incident was modified by another user")
		}
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

	// Commit transaction
	if err := tx.Commit().Error; err != nil {
		return nil, err
	}

	// Keep incident_assignees junction table in sync when assignee changed via edit
	if req.AssigneeID != nil {
		if *req.AssigneeID == "" {
			// Assignee cleared
			if err := s.incidentRepo.ClearAssignees(ctx, id); err != nil {
				fmt.Printf("Warning: ClearAssignees failed during update: %v\n", err)
			}
		} else if incident.AssigneeID != nil {
			// Assignee set to a specific user
			if err := s.incidentRepo.SetAssignees(ctx, id, []uuid.UUID{*incident.AssigneeID}); err != nil {
				fmt.Printf("Warning: SetAssignees failed during update: %v\n", err)
			}
		}
	}

	// Fetch updated incident first to get full data
	updated, err := s.incidentRepo.FindByIDWithRelations(ctx, id)
	if err != nil {
		return nil, err
	}

	resp := models.ToIncidentResponse(updated)

	// Broadcast update to WebSocket subscribers
	if s.wsHub != nil {
		// Broadcast to incident-specific subscribers
		s.wsHub.BroadcastToIncident(id, "incident_updated", map[string]interface{}{
			"incident_id": id,
			"changes":     changes,
			"description": descriptions,
		}, userID)

		// Check if assignee changed
		assigneeChanged := false
		for _, change := range changes {
			if change.FieldName == "assignee_id" {
				assigneeChanged = true
				break
			}
		}

		// Broadcast to all broadcast clients (incident list pages)
		messageType := "incident_updated"
		if assigneeChanged {
			messageType = "assignee_changed"
		}

		s.wsHub.BroadcastToAll(messageType, map[string]interface{}{
			"incident":         resp,
			"assignee_changed": assigneeChanged,
		})
	}

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

	// Check if already converted
	if sourceIncident.ConvertedRequestID != nil {
		return nil, errors.New("this incident has already been converted to a request")
	}

	// Check role-based permission for converting to request
	workflow, err := s.workflowRepo.FindByIDWithRelations(ctx, sourceIncident.WorkflowID)
	if err != nil {
		return nil, errors.New("workflow not found")
	}

	if len(workflow.ConvertToRequestRoles) > 0 {
		hasPermission := false
		for _, allowedRole := range workflow.ConvertToRequestRoles {
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
			return nil, errors.New("you do not have permission to convert this incident to a request")
		}
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

	// Calculate SLA deadline based on classification criticality (with fallback to workflow state SLA)
	var slaClassificationID uuid.UUID
	if req.ClassificationID != "" {
		slaClassificationID, err = uuid.Parse(req.ClassificationID)
		if err != nil {
			slaClassificationID = uuid.Nil
		}
	} else {
		slaClassificationID = uuid.Nil
	}
	// For convert to request, we don't have lookup values at this point, so pass empty array
	var slaDeadline *time.Time
	slaDeadline, err = s.calculateSLADeadline(ctx, &slaClassificationID, []string{}, initialState.SLAHours)
	if err == nil && slaDeadline != nil {
		newRequest.SLADeadline = slaDeadline
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

	// Copy attachments from source incident
	attachments, err := s.incidentRepo.ListAttachments(ctx, incidentID)
	if err == nil && len(attachments) > 0 {
		for _, attachment := range attachments {
			newAttachment := &models.IncidentAttachment{
				IncidentID:   newRequest.ID,
				FileName:     attachment.FileName,
				FileSize:     attachment.FileSize,
				MimeType:     attachment.MimeType,
				FilePath:     attachment.FilePath,
				UploadedByID: attachment.UploadedByID,
			}
			if err := s.incidentRepo.CreateAttachment(ctx, newAttachment); err != nil {
				fmt.Printf("Warning: failed to copy attachment %s: %v\n", attachment.FileName, err)
			}
		}
	}

	// Find a terminal state from the source incident's workflow to close it
	terminalState, err := s.getTerminalStateForWorkflow(ctx, sourceIncident.WorkflowID)
	if err != nil {
		fmt.Printf("Warning: failed to find terminal state for workflow: %v\n", err)
	}

	// Update source incident with reference to the converted request and close it
	updateFields := map[string]interface{}{
		"converted_request_id": newRequest.ID,
	}
	if terminalState != nil {
		updateFields["current_state_id"] = terminalState.ID
		updateFields["closed_at"] = time.Now()
	}
	if err := s.incidentRepo.UpdateFields(ctx, incidentID, updateFields); err != nil {
		fmt.Printf("Warning: failed to update source incident after conversion: %v\n", err)
	}

	// Fetch the created request with relations
	createdRequest, err := s.incidentRepo.FindByIDWithRelations(ctx, newRequest.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch created request: %w", err)
	}

	// Create transition history entry for convert-to-request action
	now := time.Now()
	convertHistory := &models.IncidentTransitionHistory{
		IncidentID:     incidentID,
		TransitionID:   nil, // No specific transition for convert action
		FromStateID:    sourceIncident.CurrentStateID,
		ToStateID:      terminalState.ID,
		PerformedByID:  userID,
		Comment:        fmt.Sprintf("Converted to request %s", requestNumber),
		TransitionedAt: now,
		OldValues:      fmt.Sprintf(`{"record_type": "incident", "incident_number": "%s"}`, sourceIncident.IncidentNumber),
		NewValues:      fmt.Sprintf(`{"record_type": "request", "request_number": "%s"}`, requestNumber),
	}
	if histErr := s.incidentRepo.CreateTransitionHistory(ctx, convertHistory); histErr != nil {
		fmt.Printf("Warning: failed to create transition history for convert-to-request: %v\n", histErr)
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

// CanConvertToRequest checks if the user can convert the incident to a request
func (s *incidentService) CanConvertToRequest(ctx context.Context, incidentID uuid.UUID, userRoleIDs []uuid.UUID) (bool, string, error) {
	// Get the source incident
	sourceIncident, err := s.incidentRepo.FindByIDWithRelations(ctx, incidentID)
	if err != nil {
		return false, "", errors.New("incident not found")
	}

	// Check if it's already a request
	if sourceIncident.RecordType == "request" {
		return false, "This is already a request", nil
	}

	// Check if it has already been converted
	if sourceIncident.ConvertedRequestID != nil {
		return false, "This incident has already been converted to a request", nil
	}

	// Get the workflow with ConvertToRequestRoles
	workflow, err := s.workflowRepo.FindByIDWithRelations(ctx, sourceIncident.WorkflowID)
	if err != nil {
		return false, "", errors.New("workflow not found")
	}

	// If no roles specified, all users can convert (backwards compatible)
	if len(workflow.ConvertToRequestRoles) == 0 {
		return true, "", nil
	}

	// Check if user has any of the allowed roles
	for _, allowedRole := range workflow.ConvertToRequestRoles {
		for _, userRoleID := range userRoleIDs {
			if allowedRole.ID == userRoleID {
				return true, "", nil
			}
		}
	}

	return false, "You do not have permission to convert this incident to a request", nil
}

// getTerminalStateForWorkflow finds a terminal state for the given workflow
func (s *incidentService) getTerminalStateForWorkflow(ctx context.Context, workflowID uuid.UUID) (*models.WorkflowState, error) {
	var state models.WorkflowState
	err := s.db.WithContext(ctx).
		Where("workflow_id = ? AND state_type = ? AND is_active = ?", workflowID, "terminal", true).
		Order("sort_order, id").
		First(&state).Error
	if err != nil {
		return nil, err
	}
	return &state, nil
}

// BulkConvertToRequest converts multiple incidents to a single request in bulk
func (s *incidentService) BulkConvertToRequest(ctx context.Context, req *models.BulkConvertToRequestRequest, userID uuid.UUID, userRoleIDs []uuid.UUID) (*models.BulkConvertToRequestResponse, error) {
	response := &models.BulkConvertToRequestResponse{
		Total:   len(req.IncidentIDs),
		Results: make([]models.BulkConvertToRequestResult, 0, len(req.IncidentIDs)),
	}

	// Build a map of item-specific transitions and feedback
	transitionMap := make(map[string]*models.BulkConvertToRequestItem)
	feedbackMap := make(map[string]*models.IncidentFeedbackRequest)
	for _, item := range req.Items {
		transitionMap[item.IncidentID] = &item
		if item.Feedback != nil {
			feedbackMap[item.IncidentID] = item.Feedback
		}
	}

	// Validate feedback is provided (either globally or for each incident)
	if req.ExistingRequestID == nil || *req.ExistingRequestID == "" {
		hasGlobalFeedback := req.Feedback != nil
		hasAnyItemFeedback := len(feedbackMap) > 0
		if !hasGlobalFeedback && !hasAnyItemFeedback {
			return nil, errors.New("feedback is required for bulk conversion (provide global feedback or per-item feedback)")
		}
	}

	// Parse common fields
	workflowID, err := uuid.Parse(req.WorkflowID)
	if err != nil {
		return nil, errors.New("invalid workflow_id")
	}

	classificationID, err := uuid.Parse(req.ClassificationID)
	if err != nil {
		return nil, errors.New("invalid classification_id")
	}

	// Parse optional fields
	var assigneeID *uuid.UUID
	if req.AssigneeID != nil && *req.AssigneeID != "" {
		id, err := uuid.Parse(*req.AssigneeID)
		if err == nil {
			assigneeID = &id
		}
	}

	var departmentID *uuid.UUID
	if req.DepartmentID != nil && *req.DepartmentID != "" {
		id, err := uuid.Parse(*req.DepartmentID)
		if err == nil {
			departmentID = &id
		}
	}

	var dueDate *time.Time
	if req.DueDate != nil && *req.DueDate != "" {
		parsed, err := time.Parse(time.RFC3339, *req.DueDate)
		if err == nil {
			dueDate = &parsed
		}
	}

	// Handle existing request ID (optional)
	var existingRequest *models.Incident
	var existingRequestID *uuid.UUID
	if req.ExistingRequestID != nil && *req.ExistingRequestID != "" {
		id, err := uuid.Parse(*req.ExistingRequestID)
		if err != nil {
			return nil, errors.New("invalid existing_request_id")
		}
		existingRequest, err = s.incidentRepo.FindByIDWithRelations(ctx, id)
		if err != nil {
			return nil, errors.New("existing request not found")
		}
		if existingRequest.RecordType != "request" {
			return nil, errors.New("existing_request_id must reference a request, not an incident")
		}
		existingRequestID = &id
	}

	// Get the initial state of the request workflow (only needed if creating new request)
	var initialState *models.WorkflowState
	if existingRequestID == nil {
		initialState, err = s.workflowRepo.GetInitialState(ctx, workflowID)
		if err != nil {
			return nil, errors.New("workflow has no initial state configured")
		}
	}

	// First pass: Validate all incidents and collect data
	validIncidents := make([]*models.Incident, 0, len(req.IncidentIDs))
	validIncidentIDs := make([]uuid.UUID, 0, len(req.IncidentIDs))
	var firstIncident *models.Incident

	for i, incidentIDStr := range req.IncidentIDs {
		result := models.BulkConvertToRequestResult{}
		incidentID, err := uuid.Parse(incidentIDStr)
		if err != nil {
			result.IncidentID = uuid.Nil
			result.Success = false
			errMsg := "invalid incident_id: " + incidentIDStr
			result.Error = &errMsg
			response.Results = append(response.Results, result)
			response.Failed++
			continue
		}
		result.IncidentID = incidentID

		// Get the source incident
		sourceIncident, err := s.incidentRepo.FindByIDWithRelations(ctx, incidentID)
		if err != nil {
			result.Success = false
			errMsg := "incident not found"
			result.Error = &errMsg
			response.Results = append(response.Results, result)
			response.Failed++
			continue
		}

		// Validate it's not already a request
		if sourceIncident.RecordType == "request" {
			result.Success = false
			errMsg := "cannot convert a request to another request"
			result.Error = &errMsg
			response.Results = append(response.Results, result)
			response.Failed++
			continue
		}

		// Check if already converted
		if sourceIncident.ConvertedRequestID != nil {
			result.Success = false
			errMsg := "this incident has already been converted to a request"
			result.Error = &errMsg
			response.Results = append(response.Results, result)
			response.Failed++
			continue
		}

		// Check role-based permission for converting to request
		workflow, err := s.workflowRepo.FindByIDWithRelations(ctx, sourceIncident.WorkflowID)
		if err != nil {
			result.Success = false
			errMsg := "workflow not found"
			result.Error = &errMsg
			response.Results = append(response.Results, result)
			response.Failed++
			continue
		}

		if len(workflow.ConvertToRequestRoles) > 0 {
			hasPermission := false
			for _, allowedRole := range workflow.ConvertToRequestRoles {
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
				result.Success = false
				errMsg := "you do not have permission to convert this incident to a request"
				result.Error = &errMsg
				response.Results = append(response.Results, result)
				response.Failed++
				continue
			}
		}

		// Execute transition if provided for this specific incident
		if item, ok := transitionMap[incidentIDStr]; ok && item.TransitionID != "" {
			_, err := uuid.Parse(item.TransitionID)
			if err == nil {
				transitionReq := &models.IncidentTransitionRequest{
					TransitionID: item.TransitionID,
					Comment:      item.TransitionComment,
				}

				_, err := s.ExecuteTransition(ctx, incidentID, transitionReq, userID, userRoleIDs)
				if err != nil {
					result.Success = false
					errMsg := fmt.Sprintf("failed to execute transition: %v", err)
					result.Error = &errMsg
					response.Results = append(response.Results, result)
					response.Failed++
					continue
				}

				// Reload the incident after transition
				sourceIncident, err = s.incidentRepo.FindByIDWithRelations(ctx, incidentID)
				if err != nil {
					result.Success = false
					errMsg := "failed to reload incident after transition"
					result.Error = &errMsg
					response.Results = append(response.Results, result)
					response.Failed++
					continue
				}
			}
		}

		// Validate classification and location match across incidents
		if i == 0 {
			firstIncident = sourceIncident
		} else {
			// Check classification matches
			if firstIncident.ClassificationID != nil && sourceIncident.ClassificationID != nil {
				if *firstIncident.ClassificationID != *sourceIncident.ClassificationID {
					result.Success = false
					errMsg := "all incidents must have the same classification for bulk conversion"
					result.Error = &errMsg
					response.Results = append(response.Results, result)
					response.Failed++
					continue
				}
			} else if firstIncident.ClassificationID != sourceIncident.ClassificationID {
				result.Success = false
				errMsg := "all incidents must have the same classification for bulk conversion"
				result.Error = &errMsg
				response.Results = append(response.Results, result)
				response.Failed++
				continue
			}

			// Check location matches
			if firstIncident.LocationID != nil && sourceIncident.LocationID != nil {
				if *firstIncident.LocationID != *sourceIncident.LocationID {
					result.Success = false
					errMsg := "all incidents must have the same location for bulk conversion"
					result.Error = &errMsg
					response.Results = append(response.Results, result)
					response.Failed++
					continue
				}
			} else if firstIncident.LocationID != sourceIncident.LocationID {
				result.Success = false
				errMsg := "all incidents must have the same location for bulk conversion"
				result.Error = &errMsg
				response.Results = append(response.Results, result)
				response.Failed++
				continue
			}
		}

		// Add to valid incidents list
		validIncidents = append(validIncidents, sourceIncident)
		validIncidentIDs = append(validIncidentIDs, incidentID)
	}

	// Check if we have any valid incidents to process
	if len(validIncidents) == 0 {
		return response, nil
	}

	// Build source incident IDs list for the request
	sourceIncidentIDStrs := make([]string, len(validIncidentIDs))
	for i, id := range validIncidentIDs {
		sourceIncidentIDStrs[i] = id.String()
	}

	// Handle creating new request or using existing request
	var newRequest *models.Incident
	var requestNumber string

	if existingRequestID != nil {
		// Use existing request - update its source incident references
		newRequest = existingRequest
		requestNumber = existingRequest.IncidentNumber

		// Append new source incident IDs to existing ones
		existingSourceIDs := existingRequest.SourceIncidentIDs
		if len(existingSourceIDs) > 0 {
			sourceIncidentIDStrs = append(existingSourceIDs, sourceIncidentIDStrs...)
		}

		// Marshal to JSON explicitly for GORM Updates() to handle JSON column correctly
		sourceIncidentIDsJSON, err := json.Marshal(sourceIncidentIDStrs)
		if err != nil {
			for _, incidentID := range validIncidentIDs {
				result := models.BulkConvertToRequestResult{
					IncidentID: incidentID,
					Success:    false,
					Error:      stringPtr(fmt.Sprintf("failed to marshal source incident IDs: %v", err)),
				}
				response.Results = append(response.Results, result)
				response.Failed++
			}
			return response, nil
		}

		// Update the request with additional source incidents
		updateFields := map[string]interface{}{
			"source_incident_ids": sourceIncidentIDsJSON,
		}
		// If no primary source incident, set it
		if existingRequest.SourceIncidentID == nil {
			updateFields["source_incident_id"] = &validIncidentIDs[0]
		}
		if err := s.incidentRepo.UpdateFields(ctx, newRequest.ID, updateFields); err != nil {
			// Mark all as failed
			for _, incidentID := range validIncidentIDs {
				result := models.BulkConvertToRequestResult{
					IncidentID: incidentID,
					Success:    false,
					Error:      stringPtr(fmt.Sprintf("failed to update existing request: %v", err)),
				}
				response.Results = append(response.Results, result)
				response.Failed++
			}
			return response, nil
		}

		// Reload the request to get updated data
		newRequest, err = s.incidentRepo.FindByIDWithRelations(ctx, *existingRequestID)
		if err != nil {
			// Mark all as failed
			for _, incidentID := range validIncidentIDs {
				result := models.BulkConvertToRequestResult{
					IncidentID: incidentID,
					Success:    false,
					Error:      stringPtr("failed to reload existing request"),
				}
				response.Results = append(response.Results, result)
				response.Failed++
			}
			return response, nil
		}
	} else {
		// Generate single request number for all incidents
		requestNumber, err = s.incidentRepo.GenerateRequestNumber(ctx)
		if err != nil {
			// Mark all remaining as failed
			for _, incidentID := range validIncidentIDs {
				result := models.BulkConvertToRequestResult{
					IncidentID: incidentID,
					Success:    false,
					Error:      stringPtr(fmt.Sprintf("failed to generate request number: %v", err)),
				}
				response.Results = append(response.Results, result)
				response.Failed++
			}
			return response, nil
		}

		// Create the single new request using the first incident as primary
		title := firstIncident.Title
		description := firstIncident.Description

		newRequest = &models.Incident{
			IncidentNumber:    requestNumber,
			Title:             title,
			Description:       description,
			RecordType:        "request",
			SourceIncidentID:  &validIncidentIDs[0],
			SourceIncidentIDs: sourceIncidentIDStrs,
			ClassificationID:  &classificationID,
			WorkflowID:        workflowID,
			CurrentStateID:    initialState.ID,
			ReporterID:        firstIncident.ReporterID,
			ReporterEmail:     firstIncident.ReporterEmail,
			ReporterName:      firstIncident.ReporterName,
			LocationID:        firstIncident.LocationID,
			Latitude:          firstIncident.Latitude,
			Longitude:         firstIncident.Longitude,
			CustomFields:      firstIncident.CustomFields,
		}

		// Handle assignee
		if assigneeID != nil {
			newRequest.AssigneeID = assigneeID
		} else {
			newRequest.AssigneeID = firstIncident.AssigneeID
		}

		// Handle department
		if departmentID != nil {
			newRequest.DepartmentID = departmentID
		} else {
			newRequest.DepartmentID = firstIncident.DepartmentID
		}

		// Handle due date
		if dueDate != nil {
			newRequest.DueDate = dueDate
		}

		// Calculate SLA deadline
		if initialState.SLAHours != nil && *initialState.SLAHours > 0 {
			deadline := time.Now().Add(time.Duration(*initialState.SLAHours) * time.Hour)
			newRequest.SLADeadline = &deadline
		}

		// Create the single request
		if err := s.incidentRepo.Create(ctx, newRequest); err != nil {
			// Mark all as failed
			for _, incidentID := range validIncidentIDs {
				result := models.BulkConvertToRequestResult{
					IncidentID: incidentID,
					Success:    false,
					Error:      stringPtr(fmt.Sprintf("failed to create request: %v", err)),
				}
				response.Results = append(response.Results, result)
				response.Failed++
			}
			return response, nil
		}
	}

	// Copy lookup values from all valid incidents
	// Use a map to avoid duplicates based on ID
	lookupValueMap := make(map[uuid.UUID]models.LookupValue)
	for _, sourceIncident := range validIncidents {
		for _, lv := range sourceIncident.LookupValues {
			lookupValueMap[lv.ID] = lv
		}
	}
	if len(lookupValueMap) > 0 {
		lookupValues := make([]models.LookupValue, 0, len(lookupValueMap))
		for _, lv := range lookupValueMap {
			lookupValues = append(lookupValues, lv)
		}
		if err := s.incidentRepo.SetLookupValues(ctx, newRequest.ID, lookupValues); err != nil {
			fmt.Printf("Warning: failed to copy lookup values: %v\n", err)
		}
	}

	// Copy attachments from ALL valid incidents to the single request
	for _, sourceIncident := range validIncidents {
		attachments, err := s.incidentRepo.ListAttachments(ctx, sourceIncident.ID)
		if err == nil && len(attachments) > 0 {
			for _, attachment := range attachments {
				newAttachment := &models.IncidentAttachment{
					IncidentID:   newRequest.ID,
					FileName:     attachment.FileName,
					FileSize:     attachment.FileSize,
					MimeType:     attachment.MimeType,
					FilePath:     attachment.FilePath,
					UploadedByID: attachment.UploadedByID,
				}
				if err := s.incidentRepo.CreateAttachment(ctx, newAttachment); err != nil {
					fmt.Printf("Warning: failed to copy attachment %s from incident %s: %v\n", attachment.FileName, sourceIncident.IncidentNumber, err)
				}
			}
		}
	}

	// Find a terminal state to close source incidents
	terminalState, err := s.getTerminalStateForWorkflow(ctx, firstIncident.WorkflowID)
	if err != nil {
		fmt.Printf("Warning: failed to find terminal state for workflow: %v\n", err)
	}

	// Update ALL source incidents to reference the same request and close them
	for _, sourceIncident := range validIncidents {
		updateFields := map[string]interface{}{
			"converted_request_id": newRequest.ID,
		}
		if terminalState != nil {
			updateFields["current_state_id"] = terminalState.ID
			updateFields["closed_at"] = time.Now()
		}
		if err := s.incidentRepo.UpdateFields(ctx, sourceIncident.ID, updateFields); err != nil {
			fmt.Printf("Warning: failed to update source incident %s after conversion: %v\n", sourceIncident.IncidentNumber, err)
		}

		// Create transition history entry for convert-to-request action (bulk)
		now := time.Now()
		convertHistory := &models.IncidentTransitionHistory{
			IncidentID:     sourceIncident.ID,
			TransitionID:   nil,
			FromStateID:    sourceIncident.CurrentStateID,
			ToStateID:      terminalState.ID,
			PerformedByID:  userID,
			Comment:        fmt.Sprintf("Converted to request %s (bulk)", requestNumber),
			TransitionedAt: now,
			OldValues:      fmt.Sprintf(`{"record_type": "incident", "incident_number": "%s"}`, sourceIncident.IncidentNumber),
			NewValues:      fmt.Sprintf(`{"record_type": "request", "request_number": "%s"}`, requestNumber),
		}
		if histErr := s.incidentRepo.CreateTransitionHistory(ctx, convertHistory); histErr != nil {
			fmt.Printf("Warning: failed to create transition history for bulk convert-to-request: %v\n", histErr)
		}

		// Create revision for source incident
		changes := []models.IncidentFieldChange{
			{
				FieldName:  "converted_to_request",
				FieldLabel: "Converted to Request",
				OldValue:   nil,
				NewValue:   &requestNumber,
			},
		}
		desc := fmt.Sprintf("Incident converted to request %s (bulk conversion)", requestNumber)
		_ = s.CreateRevision(ctx, sourceIncident.ID, models.RevisionActionFieldChange, desc, changes, userID)

		// Apply feedback if provided for this incident
		feedback, hasFeedback := feedbackMap[sourceIncident.ID.String()]
		if feedback == nil && req.Feedback != nil {
			feedback = req.Feedback
			hasFeedback = true
		}
		if hasFeedback && feedback != nil {
			feedbackChanges := []models.IncidentFieldChange{
				{
					FieldName:  "feedback_rating",
					FieldLabel: "Feedback Rating",
					OldValue:   nil,
					NewValue:   stringPtr(fmt.Sprintf("%d", feedback.Rating)),
				},
			}
			if feedback.Comment != "" {
				feedbackChanges = append(feedbackChanges, models.IncidentFieldChange{
					FieldName:  "feedback_comment",
					FieldLabel: "Feedback Comment",
					OldValue:   nil,
					NewValue:   &feedback.Comment,
				})
			}
			feedbackDesc := fmt.Sprintf("Feedback provided during conversion to request %s", requestNumber)
			_ = s.CreateRevision(ctx, sourceIncident.ID, models.RevisionActionFieldChange, feedbackDesc, feedbackChanges, userID)
		}
	}

	// Create revision for new request
	allIncidentNumbers := make([]string, len(validIncidents))
	for i, inc := range validIncidents {
		allIncidentNumbers[i] = inc.IncidentNumber
	}
	sourceIncidentsList := fmt.Sprintf("Created from incidents: %s", fmt.Sprint(allIncidentNumbers))
	changes := []models.IncidentFieldChange{
		{
			FieldName:  "source_incidents",
			FieldLabel: "Created from Incidents",
			OldValue:   nil,
			NewValue:   &sourceIncidentsList,
		},
	}
	var desc string
	if existingRequestID != nil {
		desc = fmt.Sprintf("%d incidents added to existing request via bulk conversion", len(validIncidents))
	} else {
		desc = fmt.Sprintf("Request created from %d incidents via bulk conversion", len(validIncidents))
	}
	_ = s.CreateRevision(ctx, newRequest.ID, models.RevisionActionCreated, desc, changes, userID)

	// Fetch the created request with relations
	createdRequest, err := s.incidentRepo.FindByIDWithRelations(ctx, newRequest.ID)
	if err != nil {
		// Still return success but note the fetch error
		fmt.Printf("Warning: failed to fetch created request: %v\n", err)
	}

	// Build successful results for all valid incidents
	newResp := models.ToIncidentResponse(createdRequest)
	for _, sourceIncident := range validIncidents {
		originalResp := models.ToIncidentResponse(sourceIncident)
		result := models.BulkConvertToRequestResult{
			IncidentID:       sourceIncident.ID,
			Success:          true,
			RequestID:        &newRequest.ID,
			RequestNumber:    &requestNumber,
			OriginalIncident: &originalResp,
			NewRequest:       &newResp,
		}
		response.Results = append(response.Results, result)
		response.Success++
	}

	return response, nil
}

// Helper function to create string pointer
func stringPtr(s string) *string {
	return &s
}

// State transitions

func (s *incidentService) ExecuteTransition(ctx context.Context, incidentID uuid.UUID, req *models.IncidentTransitionRequest, userID uuid.UUID, userRoleIDs []uuid.UUID) (*models.IncidentResponse, error) {
	// Begin transaction
	tx := s.db.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	txRepo := s.incidentRepo.WithTx(tx)

	// PESSIMISTIC LOCK: Acquire lock on the incident to prevent concurrent state transitions
	incident, err := txRepo.LockForUpdate(ctx, tx, incidentID)
	if err != nil {
		tx.Rollback()
		return nil, errors.New("incident not found or locked by another transaction")
	}

	// BLOCK: Prevent manual transitions on child incidents (merged into another)
	if incident.IsMerged && incident.MasterIncidentID != nil {
		tx.Rollback()
		return nil, errors.New("child incidents cannot be transitioned manually - they follow the master incident's status")
	}

	// Verify version still matches (double-check optimistic lock)
	if incident.Version != req.Version {
		tx.Rollback()
		return nil, fmt.Errorf("conflict: incident was modified by another user")
	}

	// Parse transition ID
	transitionID, err := uuid.Parse(req.TransitionID)
	if err != nil {
		tx.Rollback()
		return nil, errors.New("invalid transition_id")
	}

	// Get the transition with relations
	transition, err := s.workflowRepo.FindTransitionByIDWithRelations(ctx, transitionID)
	if err != nil {
		return nil, errors.New("transition not found")
	}

	// Verify the transition belongs to this workflow
	if transition.WorkflowID != incident.WorkflowID {
		tx.Rollback()
		return nil, errors.New("transition does not belong to this workflow")
	}

	// Verify the transition starts from the current state
	if transition.FromStateID != incident.CurrentStateID {
		tx.Rollback()
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
			tx.Rollback()
			return nil, errors.New("you do not have permission to execute this transition")
		}
	}

	// Validate requirements
	for _, requirement := range transition.Requirements {
		if requirement.IsMandatory == nil || !*requirement.IsMandatory {
			continue
		}

		switch requirement.RequirementType {
		case "comment":
			if req.Comment == "" {
				errMsg := requirement.ErrorMessage
				if errMsg == "" {
					errMsg = "Comment is required for this transition"
				}
				tx.Rollback()
				return nil, errors.New(errMsg)
			}
		case "attachment":
			if len(req.Attachments) == 0 {
				errMsg := requirement.ErrorMessage
				if errMsg == "" {
					errMsg = "Attachment is required for this transition"
				}
				tx.Rollback()
				return nil, errors.New(errMsg)
			}
		case "feedback":
			if req.Feedback == nil || req.Feedback.Rating == 0 {
				errMsg := requirement.ErrorMessage
				if errMsg == "" {
					errMsg = "Feedback is required for this transition"
				}
				tx.Rollback()
				return nil, errors.New(errMsg)
			}
		}
	}

	// Create transition history record
	history := &models.IncidentTransitionHistory{
		IncidentID:     incidentID,
		TransitionID:   &transitionID,
		FromStateID:    incident.CurrentStateID,
		ToStateID:      transition.ToStateID,
		PerformedByID:  userID,
		Comment:        req.Comment,
		TransitionedAt: time.Now(),
	}

	if err := txRepo.CreateTransitionHistory(ctx, history); err != nil {
		tx.Rollback()
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
			txRepo.LinkAttachmentsToTransition(ctx, attachmentIDs, history.ID)
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
		txRepo.CreateComment(ctx, comment)
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
		if err := txRepo.CreateFeedback(ctx, feedback); err != nil {
			fmt.Printf("Warning: failed to create feedback: %v\n", err)
		}
	}

	// Get new state for SLA calculation
	newState, err := s.workflowRepo.FindStateByID(ctx, transition.ToStateID)
	if err != nil {
		tx.Rollback()
		return nil, errors.New("target state not found")
	}

	// Validate Ready-to-Close duration when transitioning INTO a ready_to_close state
	if newState.IsReadyToClose && req.ReadyToCloseDuration == "" {
		tx.Rollback()
		return nil, errors.New("ready_to_close_duration is required when transitioning to this state")
	}

	// Prepare updates map for all fields that need to change
	updates := map[string]interface{}{
		"current_state_id": transition.ToStateID,
		"updated_at":       time.Now(),
	}

	// When leaving a ready_to_close state, clear expiry fields
	if transition.FromState != nil && transition.FromState.IsReadyToClose {
		updates["ready_to_close_expires_at"] = nil
		updates["ready_to_close_duration"] = ""
		updates["ready_to_close_notified"] = false
	}

	// Handle department assignment from transition settings
	if transition.AssignDepartmentID != nil {
		// Static department assignment
		updates["department_id"] = *transition.AssignDepartmentID
	} else if transition.AutoDetectDepartment {
		// Auto-detect: check how many departments match
		var classID, locID *uuid.UUID
		if incident.ClassificationID != nil {
			classID = incident.ClassificationID
		}
		if incident.LocationID != nil {
			locID = incident.LocationID
		}
		var deptTypeFilter *string
		if transition.DepartmentTypeFilter != "" {
			deptTypeFilter = &transition.DepartmentTypeFilter
		}
		matchedDepts, _ := s.deptRepo.FindMatching(ctx, classID, locID, deptTypeFilter)

		if len(matchedDepts) == 1 {
			// Single match — auto-assign
			updates["department_id"] = matchedDepts[0].ID
		} else if len(matchedDepts) > 1 {
			// Multiple matches — user must have selected one
			if req.DepartmentID == nil || *req.DepartmentID == "" {
				tx.Rollback()
				return nil, errors.New("department selection is required for this transition")
			}
			deptID, err := uuid.Parse(*req.DepartmentID)
			if err != nil {
				tx.Rollback()
				return nil, errors.New("invalid department_id")
			}
			updates["department_id"] = deptID
		}
		// If no departments match, keep current department (graceful fallback)
	}

	// Handle user assignment from transition settings
	var assigneeUserIDs []uuid.UUID

	// Build assignment role IDs slice from the many-to-many relation
	var assignmentRoleIDs []uuid.UUID
	for _, r := range transition.AssignmentRoles {
		assignmentRoleIDs = append(assignmentRoleIDs, r.ID)
	}

	if transition.AssignUserID != nil {
		// Static user assignment - single user
		updates["assignee_id"] = *transition.AssignUserID
		assigneeUserIDs = append(assigneeUserIDs, *transition.AssignUserID)
	} else if transition.ManualSelectUser && len(assignmentRoleIDs) > 0 {
		// Manual selection mode - operator selects one or more users from the list
		var classificationID, locationID, departmentID *uuid.UUID
		if incident.ClassificationID != nil {
			classificationID = incident.ClassificationID
		}
		if incident.LocationID != nil {
			locationID = incident.LocationID
		}
		if incident.DepartmentID != nil {
			departmentID = incident.DepartmentID
		}
		availableUsers, _ := s.userRepo.FindMatching(ctx, assignmentRoleIDs, classificationID, locationID, departmentID, nil)

		if len(availableUsers) > 0 {
			if len(req.UserIDs) == 0 {
				tx.Rollback()
				return nil, errors.New("user selection is required for this transition")
			}
			// Build a lookup set of valid user IDs
			validUserIDs := make(map[uuid.UUID]bool, len(availableUsers))
			for _, u := range availableUsers {
				validUserIDs[u.ID] = true
			}
			for _, rawID := range req.UserIDs {
				userAssignID, err := uuid.Parse(rawID)
				if err != nil {
					tx.Rollback()
					return nil, errors.New("invalid user_id: " + rawID)
				}
				if !validUserIDs[userAssignID] {
					tx.Rollback()
					return nil, errors.New("selected user does not belong to the incident's assigned location, classification, or department")
				}
				assigneeUserIDs = append(assigneeUserIDs, userAssignID)
			}
			// Primary assignee is the first selected user
			updates["assignee_id"] = assigneeUserIDs[0]
		}
		// If no users match the criteria, keep current assignee (graceful fallback)
	} else if transition.AutoMatchUser && len(assignmentRoleIDs) > 0 {
		// Auto-match mode - find ALL matching users and assign to all of them
		var classificationID, locationID, departmentID, excludeUserID *uuid.UUID
		if incident.ClassificationID != nil {
			classificationID = incident.ClassificationID
		}
		if incident.LocationID != nil {
			locationID = incident.LocationID
		}
		if incident.DepartmentID != nil {
			departmentID = incident.DepartmentID
		}
		if incident.AssigneeID != nil {
			excludeUserID = incident.AssigneeID
		}

		// First try matching with all criteria
		matchedUsers, err := s.userRepo.FindMatching(ctx, assignmentRoleIDs, classificationID, locationID, departmentID, excludeUserID)
		if err == nil && len(matchedUsers) > 0 {
			for _, user := range matchedUsers {
				assigneeUserIDs = append(assigneeUserIDs, user.ID)
			}
			updates["assignee_id"] = matchedUsers[0].ID
		} else if err == nil && len(matchedUsers) == 0 {
			// No exact matches - try matching by role only (more permissive)
			roleOnlyUsers, roleErr := s.userRepo.FindMatching(ctx, assignmentRoleIDs, nil, nil, nil, excludeUserID)
			if roleErr == nil && len(roleOnlyUsers) > 0 {
				for _, user := range roleOnlyUsers {
					assigneeUserIDs = append(assigneeUserIDs, user.ID)
				}
				updates["assignee_id"] = roleOnlyUsers[0].ID
			}
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

	// Apply user-provided field changes configured on the transition
	if len(req.FieldChanges) > 0 {
		for fieldName, fieldValue := range req.FieldChanges {
			if fieldValue == "" {
				continue
			}
			switch fieldName {
			case "priority":
				if p, err := strconv.Atoi(fieldValue); err == nil && p >= 1 && p <= 5 {
					updates["priority"] = p
				}
			case "department_id":
				if id, err := uuid.Parse(fieldValue); err == nil {
					updates["department_id"] = id
				}
			case "location_id":
				if id, err := uuid.Parse(fieldValue); err == nil {
					updates["location_id"] = id
				}
			case "classification_id":
				if id, err := uuid.Parse(fieldValue); err == nil {
					updates["classification_id"] = id
				}
			case "title":
				updates["title"] = fieldValue
			case "description":
				updates["description"] = fieldValue
			}
		}
	}

	// Apply all updates using optimistic locking with version
	if err := txRepo.UpdateFieldsWithVersion(ctx, incidentID, updates, req.Version); err != nil {
		tx.Rollback()
		if err == repository.ErrVersionMismatch {
			return nil, fmt.Errorf("conflict: incident was modified by another user")
		}
		return nil, err
	}

	// Set multiple assignees if applicable
	if len(assigneeUserIDs) > 0 {
		if err := txRepo.SetAssignees(ctx, incidentID, assigneeUserIDs); err != nil {
			// Log error but don't fail the transition
			fmt.Printf("Warning: SetAssignees failed: %v\n", err)
		}
	}

	// Create revision for state change (using txRepo to stay within the transaction)
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
	revDescription := fmt.Sprintf("Status changed from %s to %s", oldStateName, newStateName)
	revNum, _ := txRepo.GetNextRevisionNumber(ctx, incidentID)
	changesBytes, _ := json.Marshal(changes)
	txRepo.CreateRevision(ctx, &models.IncidentRevision{
		IncidentID:          incidentID,
		RevisionNumber:      revNum,
		ActionType:          models.RevisionActionStatusChanged,
		ActionDescription:   revDescription,
		Changes:             string(changesBytes),
		PerformedByID:       userID,
		TransitionHistoryID: &history.ID,
		CreatedAt:           time.Now(),
	})

	// Create revision entry that includes duration/comment info for ready_to_close
	if newState.IsReadyToClose && req.ReadyToCloseDuration != "" {
		rtcDescription := fmt.Sprintf(
			"Status changed from %s to %s — Duration: %s",
			transition.FromState.Name, newState.Name, req.ReadyToCloseDuration,
		)
		if req.Comment != "" {
			rtcDescription += fmt.Sprintf("; Comment: %s", req.Comment)
		}
		rtcRevNum, _ := txRepo.GetNextRevisionNumber(ctx, incidentID)
		rtcChangesBytes, _ := json.Marshal([]models.IncidentFieldChange{
			{FieldName: "ready_to_close_duration", FieldLabel: "Close Duration", OldValue: nil, NewValue: &req.ReadyToCloseDuration},
		})
		txRepo.CreateRevision(ctx, &models.IncidentRevision{
			IncidentID:        incidentID,
			RevisionNumber:    rtcRevNum,
			ActionType:        models.RevisionActionStatusChanged,
			ActionDescription: rtcDescription,
			Changes:           string(rtcChangesBytes),
			PerformedByID:     userID,
			CreatedAt:         time.Now(),
		})
	}

	// Commit transaction first so all master incident changes are visible
	if err := tx.Commit().Error; err != nil {
		return nil, err
	}

	// Handle Ready-to-Close entry lifecycle AFTER commit
	if s.readyToCloseService != nil {
		// Deactivate any prior entry when leaving ready_to_close state
		if transition.FromState != nil && transition.FromState.IsReadyToClose {
			if err := s.readyToCloseService.DeactivateForIncident(ctx, incidentID); err != nil {
				// Non-fatal: log and continue
				fmt.Printf("Warning: failed to deactivate ready_to_close entry for incident %s: %v\n", incidentID, err)
			}
		}
		// Create new entry when entering ready_to_close state
		if newState.IsReadyToClose && req.ReadyToCloseDuration != "" {
			if err := s.readyToCloseService.CreateEntry(ctx, incidentID, req.ReadyToCloseDuration, req.Comment, userID); err != nil {
				// Non-fatal: log and continue — transition already committed
				fmt.Printf("Warning: failed to create ready_to_close entry for incident %s: %v\n", incidentID, err)
			}
		}
	}

	// Handle merge-related operations AFTER commit so they can see committed data
	if s.incidentMergeRepo != nil {
		// Check if this incident has merged incidents
		hasMerged, mergeErr := s.incidentMergeRepo.HasMergedIncidents(ctx, incidentID)
		fmt.Printf("[DEBUG] HasMergedIncidents check: hasMerged=%v, err=%v\n", hasMerged, mergeErr)
		if mergeErr == nil && hasMerged {
			// Check if this is a reopen (transitioning FROM a terminal state to a non-terminal state)
			if transition.FromState != nil && transition.FromState.StateType == "terminal" && newState.StateType != "terminal" {
				fmt.Println("[DEBUG] Reopen detected - calling AutoUnmergeOnReopen")
				_ = s.incidentMergeRepo.AutoUnmergeOnReopen(ctx, incidentID)
			} else {
				if newState.StateType == "terminal" {
					fmt.Println("[DEBUG] Terminal state - closing merged incidents")
					// Terminal state: close all merged incidents
					_ = s.incidentMergeRepo.CloseMergedIncidents(ctx, incidentID)

					// Sync transition data (revision, history, comment) to children
					_ = s.syncTransitionToMergedIncidents(ctx, incidentID, transition, history, userID)

					// Run feedback/attachment copy and SMS in background
					bgCtx := context.Background()
					fmt.Println("[DEBUG] Starting goroutine: autoCloseMergedIncidents")
					go func() {
						_ = s.autoCloseMergedIncidents(bgCtx, incidentID, req, userID)
					}()
				} else {
					fmt.Println("[DEBUG] Non-terminal state - syncing status and sending SMS")
					// Non-terminal state: sync the status and send SMS notifications
					_ = s.incidentMergeRepo.SyncStatusToMergedIncidents(ctx, incidentID, transition.ToStateID)

					// Sync transition data (revision, history, comment) to children
					_ = s.syncTransitionToMergedIncidents(ctx, incidentID, transition, history, userID)

					// Send SMS notifications in background
					bgCtx := context.Background()
					fmt.Println("[DEBUG] Starting goroutine: notifyStatusChangeToMergedIncidents")
					go func() {
						_ = s.notifyStatusChangeToMergedIncidents(bgCtx, incidentID, newStateName, req.Comment, userID)
					}()
				}
			}
		} else {
			fmt.Println("[DEBUG] No merged incidents found or error checking")
		}
	}

	// Send in-app notifications to next assignee(s)
	if s.notificationService != nil && len(assigneeUserIDs) > 0 {
		var assigneeEmails []string
		for _, assigneeID := range assigneeUserIDs {
			if u, err := s.userRepo.FindByID(ctx, assigneeID); err == nil && u.Email != "" {
				assigneeEmails = append(assigneeEmails, u.Email)
			}
		}
		if len(assigneeEmails) > 0 {
			subject := fmt.Sprintf("Incident %s assigned to you", incident.IncidentNumber)
			body := fmt.Sprintf(
				"Incident \"%s\" has been assigned to you. Status changed to: %s.",
				incident.Title, newStateName,
			)
			_, _ = s.notificationService.SendNotification(
				ctx, "notification", nil, "en",
				assigneeEmails, nil, nil,
				subject, body,
				nil, nil, &userID, nil,
			)
		}
	}

	// Send push notifications to auto-assigned employee(s)
	if s.fcmService != nil && len(assigneeUserIDs) > 0 {
		subject := fmt.Sprintf("Incident %s assigned to you", incident.IncidentNumber)
		body := fmt.Sprintf(
			"Incident \"%s\" has been assigned to you. Status changed to: %s.",
			incident.Title, newStateName,
		)
		go func() {
			bgCtx := context.Background()
			for _, aid := range assigneeUserIDs {
				_ = s.fcmService.Push(bgCtx, &models.PushRequest{
					UserID: aid,
					Title:  subject,
					Body:   body,
					Data: map[string]string{
						"type":            "incident_assigned",
						"incident_id":     incidentID.String(),
						"incident_number": incident.IncidentNumber,
					},
				})
			}
		}()
	}

	// Send push notification to reporter (citizen) when incident reaches terminal state
	if s.fcmService != nil && newState.StateType == "terminal" && incident.ReporterID != nil {
		closedAt := time.Now().Format("January 2, 2006 at 3:04 PM")
		body := fmt.Sprintf(
			"Your incident #%s \"%s\" has been resolved and closed on %s.",
			incident.IncidentNumber, incident.Title, closedAt,
		)
		if req.Comment != "" {
			body += fmt.Sprintf(" Resolution note: %s", req.Comment)
		}
		reporterID := *incident.ReporterID
		go func() {
			bgCtx := context.Background()
			_ = s.fcmService.Push(bgCtx, &models.PushRequest{
				UserID: reporterID,
				Title:  "Your Incident Has Been Closed",
				Body:   body,
				Data: map[string]string{
					"type":            "incident_closed",
					"incident_id":     incidentID.String(),
					"incident_number": incident.IncidentNumber,
				},
			})
		}()
	}

	// Broadcast state change to WebSocket subscribers
	if s.wsHub != nil {
		s.wsHub.BroadcastToIncident(incidentID, "state_changed", map[string]interface{}{
			"incident_id":   incidentID,
			"from_state":    oldStateName,
			"to_state":      newStateName,
			"transition_id": transitionID,
			"comment":       req.Comment,
			"performed_by":  userID,
		}, userID)
	}

	// Create rejection log asynchronously if this is a rejection transition.
	// Run in background so a log creation failure never blocks the response.
	if transition.IsRejection && s.rejectionLogRepo != nil {
		bgCtx := context.Background()
		go s.createRejectionLog(bgCtx, incidentID, incident, transition, history, userID, userRoleIDs)
	}

	// Fetch updated incident (outside transaction)
	updated, err := s.incidentRepo.FindByIDWithRelations(ctx, incidentID)
	if err != nil {
		return nil, err
	}

	resp := models.ToIncidentResponse(updated)
	return &resp, nil
}

// createRejectionLog builds and persists an IncidentRejectionLog record.
// Called in a goroutine after a rejection transition commits — failures are non-fatal.
func (s *incidentService) createRejectionLog(
	ctx context.Context,
	incidentID uuid.UUID,
	incident *models.Incident,
	transition *models.WorkflowTransition,
	history *models.IncidentTransitionHistory,
	rejectedByID uuid.UUID,
	rejectedByRoleIDs []uuid.UUID,
) {
	// 1. Determine ReceivedAt: when the incident entered the from-state.
	//    Look for the most recent transition that moved the incident INTO from-state.
	//    Fall back to incident.CreatedAt if no prior transition exists.
	receivedAt := incident.CreatedAt
	if prevHistory, err := s.rejectionLogRepo.GetLastTransitionIntoState(ctx, incidentID, transition.FromStateID); err == nil {
		receivedAt = prevHistory.TransitionedAt
	}

	rejectedAt := history.TransitionedAt
	reactionMinutes := int64(rejectedAt.Sub(receivedAt).Minutes())
	if reactionMinutes < 0 {
		reactionMinutes = 0
	}

	// 2. Count existing rejections for this incident (to determine sequence).
	existingCount, _ := s.rejectionLogRepo.CountByIncident(ctx, incidentID)
	sequence := existingCount + 1

	// 3. Get the from-state to snapshot SLA threshold.
	var slaThresholdHours *int
	var slaThresholdMinutes *int64
	if transition.FromState != nil && transition.FromState.SLAHours != nil && *transition.FromState.SLAHours > 0 {
		slaThresholdHours = transition.FromState.SLAHours
		mins := int64(*transition.FromState.SLAHours) * 60
		slaThresholdMinutes = &mins
	}

	// 4. Determine SLA status.
	slaStatus := "within_sla"
	if incident.SLABreached {
		slaStatus = "breached"
	} else if slaThresholdMinutes != nil && reactionMinutes > *slaThresholdMinutes {
		slaStatus = "breached"
	}

	// 5. Snapshot the rejecting user's role names.
	roles, _ := s.userRepo.GetUserRoles(ctx, rejectedByID)
	roleNames := make([]string, 0, len(roles))
	for _, r := range roles {
		roleNames = append(roleNames, r.Name)
	}
	rolesJSON, _ := json.Marshal(roleNames)

	// 6. Get username for denormalized snapshot.
	username := ""
	if user, err := s.userRepo.FindByID(ctx, rejectedByID); err == nil {
		username = user.Username
	}

	logEntry := &models.IncidentRejectionLog{
		IncidentID:              incidentID,
		RejectionSequence:       sequence,
		TotalRejectionCount:     sequence,
		ReceivedAt:              receivedAt,
		RejectedAt:              rejectedAt,
		ReactionTimeMinutes:     reactionMinutes,
		TransitionID:            transition.ID,
		FromStateID:             transition.FromStateID,
		ToStateID:               transition.ToStateID,
		RejectionReason:         history.Comment,
		RejectedByID:            rejectedByID,
		RejectedByUsername:      username,
		RejectedByRolesSnapshot: string(rolesJSON),
		SLAThresholdHours:       slaThresholdHours,
		SLAThresholdMinutes:     slaThresholdMinutes,
		SLABreachedAtRejection:  incident.SLABreached,
		SLAStatus:               slaStatus,
		IncidentNumber:          incident.IncidentNumber,
		IncidentTitle:           incident.Title,
		RecordType:              incident.RecordType,
		DepartmentID:            incident.DepartmentID,
		ClassificationID:        incident.ClassificationID,
		TransitionHistoryID:     history.ID,
	}

	if err := s.rejectionLogRepo.Create(ctx, logEntry); err != nil {
		fmt.Printf("Warning: failed to create rejection log for incident %s: %v\n", incidentID, err)
	}
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
	// Get the incident to check if it's a master or child
	incident, err := s.incidentRepo.FindByID(ctx, incidentID)
	if err != nil {
		return nil, err
	}

	var comments []models.IncidentComment

	// If this is a master incident, show master's own comments + unique child comments
	// If this is a child incident, show child's own comments + master's comments (not other children's)
	if incident.MasterIncidentID == nil {
		// This is either a standalone incident or a master incident
		// Get comments from this incident first
		comments, err = s.incidentRepo.ListComments(ctx, incidentID)
		if err != nil {
			return nil, err
		}

		// If it's a master, also get comments from merged children
		// But exclude comments that are synced copies from master (same content + author)
		mergedIncidents, err := s.incidentMergeRepo.GetMergedIncidents(ctx, incidentID)
		if err == nil && len(mergedIncidents) > 0 {
			// Build a set of master comment signatures for deduplication
			// Use content + author (synced comments have same content and author)
			masterCommentSignatures := make(map[string]bool)
			for _, c := range comments {
				// Signature: content + author (synced comments have same content and author)
				sig := fmt.Sprintf("%s|%s", c.Content, c.AuthorID)
				masterCommentSignatures[sig] = true
			}

			for _, child := range mergedIncidents {
				childComments, err := s.incidentRepo.ListComments(ctx, child.ID)
				if err == nil {
					for _, cc := range childComments {
						// Check if this child comment is a synced copy from master
						sig := fmt.Sprintf("%s|%s", cc.Content, cc.AuthorID)
						if !masterCommentSignatures[sig] {
							// This is a unique child comment, not a synced copy
							comments = append(comments, cc)
						}
					}
				}
			}
			// Sort by created_at DESC
			sort.Slice(comments, func(i, j int) bool {
				return comments[i].CreatedAt.After(comments[j].CreatedAt)
			})
		}
	} else {
		// This is a child incident - get its own comments AND master's comments
		comments, err = s.incidentRepo.ListComments(ctx, incidentID)
		if err != nil {
			return nil, err
		}

		// Also get comments from master incident
		masterComments, err := s.incidentRepo.ListComments(ctx, *incident.MasterIncidentID)
		if err == nil && len(masterComments) > 0 {
			// Build signature set for child's own comments to avoid duplicates
			childCommentSignatures := make(map[string]bool)
			for _, c := range comments {
				sig := fmt.Sprintf("%s|%s", c.Content, c.AuthorID)
				childCommentSignatures[sig] = true
			}

			// Only add master comments that don't already exist in child
			for _, mc := range masterComments {
				sig := fmt.Sprintf("%s|%s", mc.Content, mc.AuthorID)
				if !childCommentSignatures[sig] {
					comments = append(comments, mc)
				}
			}
			// Sort by created_at DESC
			sort.Slice(comments, func(i, j int) bool {
				return comments[i].CreatedAt.After(comments[j].CreatedAt)
			})
		}
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

	// Sync attachment to merged child incidents if this is a master incident
	if s.incidentMergeRepo != nil {
		hasMerged, _ := s.incidentMergeRepo.HasMergedIncidents(ctx, incidentID)
		if hasMerged {
			_ = s.syncAttachmentToMergedIncidents(ctx, incidentID, created, attachment.UploadedByID)
		}
	}

	url, err := s.storage.GetFileURL(ctx, created.FilePath)
	if err != nil {
		// Log the error but don't fail the operation
		fmt.Printf("Warning: failed to get presigned URL for attachment %s: %v\n", created.ID, err)
	}

	resp := models.ToIncidentAttachmentResponse(created, url)
	return &resp, nil
}

// syncAttachmentToMergedIncidents syncs attachment to all merged child incidents
func (s *incidentService) syncAttachmentToMergedIncidents(ctx context.Context, masterIncidentID uuid.UUID, masterAttachment *models.IncidentAttachment, uploadedBy uuid.UUID) error {
	// Get merged incidents
	mergedIncidents, err := s.incidentMergeRepo.GetMergedIncidents(ctx, masterIncidentID)
	if err != nil {
		return err
	}
	if len(mergedIncidents) == 0 {
		return nil
	}

	// Get master incident for revision description
	masterIncident, err := s.incidentRepo.FindByID(ctx, masterIncidentID)
	if err != nil {
		return err
	}

	// Process each merged incident
	for _, merged := range mergedIncidents {
		// Create attachment record for child (same file path, different incident ID)
		childAttachment := &models.IncidentAttachment{
			IncidentID:          merged.ID,
			FileName:            masterAttachment.FileName,
			FileSize:            masterAttachment.FileSize,
			MimeType:            masterAttachment.MimeType,
			FilePath:            masterAttachment.FilePath,
			UploadedByID:        uploadedBy,
			TransitionHistoryID: masterAttachment.TransitionHistoryID,
		}

		if attErr := s.incidentRepo.CreateAttachment(ctx, childAttachment); attErr != nil {
			fmt.Printf("[DEBUG] Failed to create attachment for child %s: %v\n", merged.IncidentNumber, attErr)
			continue
		}

		// Create revision for child
		description := fmt.Sprintf(
			"Attachment added to parent incident %s - %s",
			masterIncident.IncidentNumber,
			masterAttachment.FileName,
		)

		if revErr := s.CreateRevision(ctx, merged.ID, models.RevisionActionAttachmentAdded, description, nil, uploadedBy); revErr != nil {
			fmt.Printf("[DEBUG] Failed to create revision for child %s: %v\n", merged.IncidentNumber, revErr)
		}
	}

	return nil
}

func (s *incidentService) ListAttachments(ctx context.Context, incidentID uuid.UUID) ([]models.IncidentAttachmentResponse, error) {
	// Get the incident to check if it's a master or child
	incident, err := s.incidentRepo.FindByID(ctx, incidentID)
	if err != nil {
		return nil, err
	}

	var attachments []models.IncidentAttachment

	// If this is a master incident, show master's own attachments + unique child attachments
	// If this is a child incident, show child's own attachments + master's attachments (not other children's)
	if incident.MasterIncidentID == nil {
		// This is either a standalone incident or a master incident
		// Get attachments from this incident first
		attachments, err = s.incidentRepo.ListAttachments(ctx, incidentID)
		if err != nil {
			return nil, err
		}

		// If it's a master, also get attachments from merged children
		// But exclude attachments that are synced copies from master (same file name + uploader)
		mergedIncidents, err := s.incidentMergeRepo.GetMergedIncidents(ctx, incidentID)
		if err == nil && len(mergedIncidents) > 0 {
			// Build a set of master attachment signatures for deduplication
			masterAttachmentSignatures := make(map[string]bool)
			for _, a := range attachments {
				// Signature: file_name + uploaded_by (synced attachments have same file name and uploader)
				sig := fmt.Sprintf("%s|%s", a.FileName, a.UploadedByID)
				masterAttachmentSignatures[sig] = true
			}

			for _, child := range mergedIncidents {
				childAttachments, err := s.incidentRepo.ListAttachments(ctx, child.ID)
				if err == nil {
					for _, ca := range childAttachments {
						// Check if this child attachment is a synced copy from master
						sig := fmt.Sprintf("%s|%s", ca.FileName, ca.UploadedByID)
						if !masterAttachmentSignatures[sig] {
							// This is a unique child attachment, not a synced copy
							attachments = append(attachments, ca)
						}
					}
				}
			}
			// Sort by created_at DESC
			sort.Slice(attachments, func(i, j int) bool {
				return attachments[i].CreatedAt.After(attachments[j].CreatedAt)
			})
		}
	} else {
		// This is a child incident - get its own attachments AND master's attachments
		attachments, err = s.incidentRepo.ListAttachments(ctx, incidentID)
		if err != nil {
			return nil, err
		}

		// Also get attachments from master incident
		masterAttachments, err := s.incidentRepo.ListAttachments(ctx, *incident.MasterIncidentID)
		if err == nil && len(masterAttachments) > 0 {
			// Build signature set for child's own attachments to avoid duplicates
			childAttachmentSignatures := make(map[string]bool)
			for _, a := range attachments {
				sig := fmt.Sprintf("%s|%s", a.FileName, a.UploadedByID)
				childAttachmentSignatures[sig] = true
			}

			// Only add master attachments that don't already exist in child
			for _, ma := range masterAttachments {
				sig := fmt.Sprintf("%s|%s", ma.FileName, ma.UploadedByID)
				if !childAttachmentSignatures[sig] {
					attachments = append(attachments, ma)
				}
			}
			// Sort by created_at DESC
			sort.Slice(attachments, func(i, j int) bool {
				return attachments[i].CreatedAt.After(attachments[j].CreatedAt)
			})
		}
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

	// Keep the incident_assignees junction table in sync so the frontend
	// "assignees" array reflects the new assignee (not a stale previous one).
	if err := s.incidentRepo.SetAssignees(ctx, incidentID, []uuid.UUID{assigneeID}); err != nil {
		fmt.Printf("Warning: SetAssignees failed during direct assignment: %v\n", err)
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

	// Sync assignee change to merged child incidents
	if s.incidentMergeRepo != nil {
		hasMerged, _ := s.incidentMergeRepo.HasMergedIncidents(ctx, incidentID)
		if hasMerged {
			_ = s.syncAssigneeToMergedIncidents(ctx, incidentID, assigneeID, userID)
		}
	}

	// Send push notification to the new assignee
	if s.fcmService != nil && assigneeID != uuid.Nil {
		go func() {
			bgCtx := context.Background()
			body := fmt.Sprintf("Incident #%s \"%s\" has been assigned to you.", updated.IncidentNumber, updated.Title)
			_ = s.fcmService.Push(bgCtx, &models.PushRequest{
				UserID: assigneeID,
				Title:  "New Incident Assigned",
				Body:   body,
				Data: map[string]string{
					"type":            "incident_assigned",
					"incident_id":     updated.ID.String(),
					"incident_number": updated.IncidentNumber,
				},
			})
		}()
	}

	resp := models.ToIncidentResponse(updated)
	return &resp, nil
}

// syncAssigneeToMergedIncidents syncs assignee change to all merged child incidents
func (s *incidentService) syncAssigneeToMergedIncidents(ctx context.Context, masterIncidentID uuid.UUID, assigneeID uuid.UUID, userID uuid.UUID) error {
	// Get merged incidents
	mergedIncidents, err := s.incidentMergeRepo.GetMergedIncidents(ctx, masterIncidentID)
	if err != nil {
		return err
	}
	if len(mergedIncidents) == 0 {
		return nil
	}

	// Get master incident for revision description
	masterIncident, err := s.incidentRepo.FindByID(ctx, masterIncidentID)
	if err != nil {
		return err
	}

	// Get assignee name for revision
	assignee, err := s.userRepo.FindByID(ctx, assigneeID)
	if err != nil {
		return err
	}
	newAssigneeName := assignee.FirstName + " " + assignee.LastName

	// Process each merged incident
	for _, merged := range mergedIncidents {
		// Update assignee
		if assignErr := s.incidentRepo.AssignIncident(ctx, merged.ID, assigneeID); assignErr != nil {
			fmt.Printf("[DEBUG] Failed to update assignee for child %s: %v\n", merged.IncidentNumber, assignErr)
			continue
		}

		// Keep assignees junction table in sync
		if setErr := s.incidentRepo.SetAssignees(ctx, merged.ID, []uuid.UUID{assigneeID}); setErr != nil {
			fmt.Printf("[DEBUG] SetAssignees failed for child %s: %v\n", merged.IncidentNumber, setErr)
		}

		// Create revision for child
		changes := []models.IncidentFieldChange{
			{
				FieldName:  "assignee_id",
				FieldLabel: "Assigned To",
				OldValue:   strPtr("Unassigned"),
				NewValue:   &newAssigneeName,
			},
		}
		description := fmt.Sprintf(
			"Parent incident %s assignee changed to %s",
			masterIncident.IncidentNumber,
			newAssigneeName,
		)

		if revErr := s.CreateRevision(ctx, merged.ID, models.RevisionActionAssigneeChanged, description, changes, userID); revErr != nil {
			fmt.Printf("[DEBUG] Failed to create revision for child %s: %v\n", merged.IncidentNumber, revErr)
		}
	}

	return nil
}

// Stats and user queries

func (s *incidentService) GetStats(ctx context.Context, filter *models.IncidentFilter) (*models.IncidentStatsResponse, error) {
	return s.incidentRepo.GetStats(ctx, filter)
}
func (s *incidentService) GetStatsV2(ctx context.Context, filter *models.IncidentFilter) (*models.IncidentStatsResponseV2, error) {
	return s.incidentRepo.GetStatsV2(ctx, filter)
}

func (s *incidentService) GetPriorityCounts(ctx context.Context, filter *models.IncidentFilter) (map[string]int64, error) {
	return s.incidentRepo.GetPriorityCounts(ctx, filter)
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
	// Get the incident to check if it's a master or child
	incident, err := s.incidentRepo.FindByID(ctx, incidentID)
	if err != nil {
		return nil, 0, err
	}

	var revisions []models.IncidentRevision
	var total int64

	// If this is a master incident, include revisions from all merged (child) incidents
	if incident.MasterIncidentID == nil {
		// This is either a standalone incident or a master incident
		// Get revisions from this incident
		filter.IncidentID = incidentID
		revisions, total, err = s.incidentRepo.ListRevisions(ctx, filter)
		if err != nil {
			return nil, 0, err
		}

		// If it's a master, also get revisions from merged children
		mergedIncidents, err := s.incidentMergeRepo.GetMergedIncidents(ctx, incidentID)
		if err == nil && len(mergedIncidents) > 0 {
			for _, child := range mergedIncidents {
				filter.IncidentID = child.ID
				childRevisions, _, err := s.incidentRepo.ListRevisions(ctx, filter)
				if err == nil {
					revisions = append(revisions, childRevisions...)
				}
			}
			// Sort by created_at DESC
			sort.Slice(revisions, func(i, j int) bool {
				return revisions[i].CreatedAt.After(revisions[j].CreatedAt)
			})
			total = int64(len(revisions))
		}
	} else {
		// This is a child incident - get its own revisions AND master's revisions
		filter.IncidentID = incidentID
		revisions, total, err = s.incidentRepo.ListRevisions(ctx, filter)
		if err != nil {
			return nil, 0, err
		}

		// Also get revisions from master incident
		filter.IncidentID = *incident.MasterIncidentID
		masterRevisions, _, err := s.incidentRepo.ListRevisions(ctx, filter)
		if err == nil && len(masterRevisions) > 0 {
			revisions = append(revisions, masterRevisions...)
			// Sort by created_at DESC
			sort.Slice(revisions, func(i, j int) bool {
				return revisions[i].CreatedAt.After(revisions[j].CreatedAt)
			})
			total = int64(len(revisions))
		}
	}

	responses := make([]models.IncidentRevisionResponse, len(revisions))
	for i, rev := range revisions {
		responses[i] = models.ToIncidentRevisionResponse(&rev)
	}

	return responses, total, nil
}

// autoCloseMergedIncidents handles closing merged incidents when master is closed
// Copies feedback and attachments, and sends SMS notifications
func (s *incidentService) autoCloseMergedIncidents(ctx context.Context, masterIncidentID uuid.UUID, transitionReq *models.IncidentTransitionRequest, userID uuid.UUID) error {
	fmt.Println("=== [DEBUG] autoCloseMergedIncidents START ===")

	// Get merged incidents
	mergedIncidents, err := s.incidentMergeRepo.GetMergedIncidents(ctx, masterIncidentID)
	if err != nil {
		fmt.Printf("[DEBUG] Error getting merged incidents: %v\n", err)
		return err
	}
	if len(mergedIncidents) == 0 {
		fmt.Println("[DEBUG] No merged incidents found")
		return nil
	}
	fmt.Printf("[DEBUG] Found %d merged incidents\n", len(mergedIncidents))

	// Get master incident for feedback and attachments
	masterIncident, err := s.incidentRepo.FindByIDWithRelations(ctx, masterIncidentID)
	if err != nil {
		fmt.Printf("[DEBUG] Error getting master incident: %v\n", err)
		return err
	}
	fmt.Printf("[DEBUG] Master incident: %s\n", masterIncident.IncidentNumber)

	// Get current user who performed the closure (for audit purposes)
	_, _ = s.userRepo.FindByID(ctx, userID)

	now := time.Now()

	// Process each merged incident
	for i, merged := range mergedIncidents {
		fmt.Printf("\n[DEBUG] === Processing merged incident %d/%d: %s ===\n", i+1, len(mergedIncidents), merged.IncidentNumber)

		// Copy feedback from master to merged incident
		if transitionReq.Feedback != nil {
			fmt.Println("[DEBUG] Copying feedback...")
			feedback := &models.IncidentFeedback{
				IncidentID:          merged.ID,
				Rating:              transitionReq.Feedback.Rating,
				Comment:             transitionReq.Feedback.Comment,
				CreatedByID:         userID,
				TransitionHistoryID: nil,
			}
			if fbErr := s.incidentRepo.CreateFeedback(ctx, feedback); fbErr != nil {
				fmt.Printf("[DEBUG] Failed to create feedback: %v\n", fbErr)
			} else {
				fmt.Println("[DEBUG] Feedback copied successfully")
			}
		}

		// Note: Attachments are NOT copied here because they are already synced in real-time
		// when uploaded via syncAttachmentToMergedIncidents(). Copying them again would cause duplicates.

		// Send SMS notification to incident owner (reporter)
		fmt.Printf("[DEBUG] Checking reporter for SMS - Reporter: %+v\n", merged.Reporter)
		if merged.Reporter != nil && merged.Reporter.Phone != "" {
			fmt.Printf("[DEBUG] Sending SMS to: %s\n", merged.Reporter.Phone)

			smsMessage := fmt.Sprintf(
				"Your incident %s has been automatically closed as it was merged with master incident %s. The master incident has been resolved.",
				merged.IncidentNumber,
				masterIncident.IncidentNumber,
			)
			fmt.Printf("[DEBUG] SMS Message: %s\n", smsMessage)

			// Send actual SMS via Twilio
			fmt.Println("[DEBUG] Calling utils.SendSMS...")
			smsErr := utils.SendSMS(merged.Reporter.Phone, smsMessage)
			if smsErr != nil {
				fmt.Printf("[DEBUG] SMS send failed: %v\n", smsErr)
			} else {
				fmt.Println("[DEBUG] SMS sent successfully!")
			}

			// Log notification regardless of SMS success
			notification := &models.NotificationLog{
				Channel:    "sms",
				Direction:  "outbound",
				Category:   "sent",
				Language:   "en",
				Recipients: models.RecipientArray{{Email: merged.Reporter.Phone, Type: "to", Status: "sent"}},
				Subject:    "Incident Closed",
				Body:       smsMessage,
				Status:     "sent",
				Provider:   "twilio",
				IsRead:     false,
				SentBy:     &userID,
				SentAt:     &now,
			}
			if smsErr != nil {
				notification.Status = "failed"
				notification.ErrorMessage = smsErr.Error()
			}

			fmt.Println("[DEBUG] Creating notification log...")
			if notifErr := s.incidentRepo.CreateNotification(ctx, notification); notifErr != nil {
				fmt.Printf("[DEBUG] Failed to create notification log: %v\n", notifErr)
			} else {
				fmt.Printf("[DEBUG] Notification logged (status: %s)\n", notification.Status)
			}
		} else {
			fmt.Println("[DEBUG] SKIP SMS: No reporter or phone number")
		}

		// Create revision for auto-close
		changes := []models.IncidentFieldChange{
			{
				FieldName:  "current_state_id",
				FieldLabel: "Status",
				OldValue:   strPtr(merged.CurrentState.Name),
				NewValue:   strPtr(masterIncident.CurrentState.Name),
			},
			{
				FieldName:  "closed_at",
				FieldLabel: "Closed At",
				OldValue:   nil,
				NewValue:   strPtr(time.Now().Format(time.RFC3339)),
			},
		}

		description := fmt.Sprintf(
			"Automatically closed due to master incident %s being closed",
			masterIncident.IncidentNumber,
		)

		if revErr := s.CreateRevision(ctx, merged.ID, models.RevisionActionStatusChanged, description, changes, userID); revErr != nil {
			fmt.Printf("[DEBUG] Failed to create revision: %v\n", revErr)
		}
	}

	// Auto-unmerge after closing
	fmt.Println("[DEBUG] Calling AutoUnmergeOnClose...")
	if unmergeErr := s.incidentMergeRepo.AutoUnmergeOnClose(ctx, masterIncidentID); unmergeErr != nil {
		fmt.Printf("[DEBUG] Auto-unmerge failed: %v\n", unmergeErr)
	} else {
		fmt.Println("[DEBUG] Auto-unmerge completed")
	}

	fmt.Println("=== [DEBUG] autoCloseMergedIncidents END ===")
	return nil
}

// notifyStatusChangeToMergedIncidents sends SMS notifications to merged incident owners when master status changes
func (s *incidentService) notifyStatusChangeToMergedIncidents(ctx context.Context, masterIncidentID uuid.UUID, newStateName string, comment string, userID uuid.UUID) error {
	fmt.Println("=== [DEBUG] notifyStatusChangeToMergedIncidents START ===")

	// Get merged incidents with reporter details (already preloaded)
	mergedIncidents, err := s.incidentMergeRepo.GetMergedIncidents(ctx, masterIncidentID)
	if err != nil {
		fmt.Printf("[DEBUG] Error getting merged incidents: %v\n", err)
		return err
	}
	if len(mergedIncidents) == 0 {
		fmt.Println("[DEBUG] No merged incidents found")
		return nil
	}
	fmt.Printf("[DEBUG] Found %d merged incidents\n", len(mergedIncidents))

	// Get master incident number
	masterIncident, err := s.incidentRepo.FindByID(ctx, masterIncidentID)
	if err != nil {
		fmt.Printf("[DEBUG] Error getting master incident: %v\n", err)
		return err
	}
	fmt.Printf("[DEBUG] Master incident: %s, New state: %s\n", masterIncident.IncidentNumber, newStateName)

	now := time.Now()

	// Process each merged incident
	for i, merged := range mergedIncidents {
		fmt.Printf("\n[DEBUG] === Processing incident %d/%d: %s ===\n", i+1, len(mergedIncidents), merged.IncidentNumber)
		fmt.Printf("[DEBUG] Reporter: %+v\n", merged.Reporter)
		fmt.Println("under in loop")

		if merged.Reporter != nil && merged.Reporter.Phone != "" {
			fmt.Printf("[DEBUG] Sending SMS to: %s\n", merged.Reporter.Phone)

			smsMessage := fmt.Sprintf(
				"Your incident %s status has been updated to '%s' (master incident: %s). Comment: %s",
				merged.IncidentNumber,
				newStateName,
				masterIncident.IncidentNumber,
				comment,
			)
			fmt.Printf("[DEBUG] SMS Message: %s\n", smsMessage)

			// Send actual SMS via Twilio
			fmt.Println("[DEBUG] Calling utils.SendSMS...")
			smsErr := utils.SendSMS(merged.Reporter.Phone, smsMessage)
			if smsErr != nil {
				fmt.Printf("[DEBUG] SMS send failed: %v\n", smsErr)
			} else {
				fmt.Println("[DEBUG] SMS sent successfully!")
			}

			// Log notification regardless of SMS success
			notification := &models.NotificationLog{
				Channel:    "sms",
				Direction:  "outbound",
				Category:   "sent",
				Language:   "en",
				Recipients: models.RecipientArray{{Email: merged.Reporter.Phone, Type: "to", Status: "sent"}},
				Subject:    "Incident Status Updated",
				Body:       smsMessage,
				Status:     "sent",
				Provider:   "twilio",
				IsRead:     false,
				SentBy:     &userID,
				SentAt:     &now,
			}
			if smsErr != nil {
				notification.Status = "failed"
				notification.ErrorMessage = smsErr.Error()
			}

			fmt.Println("[DEBUG] Creating notification log...")
			if notifErr := s.incidentRepo.CreateNotification(ctx, notification); notifErr != nil {
				fmt.Printf("[DEBUG] Failed to create notification log: %v\n", notifErr)
			} else {
				fmt.Printf("[DEBUG] Notification logged (status: %s)\n", notification.Status)
			}
		} else {
			fmt.Println("[DEBUG] SKIP SMS: No reporter or phone number")
		}
	}

	fmt.Println("=== [DEBUG] notifyStatusChangeToMergedIncidents END ===")
	return nil
}

// syncTransitionToMergedIncidents syncs transition data (revision, history, comment) to all merged child incidents
func (s *incidentService) syncTransitionToMergedIncidents(ctx context.Context, masterIncidentID uuid.UUID, transition *models.WorkflowTransition, history *models.IncidentTransitionHistory, userID uuid.UUID) error {
	// Get merged incidents
	mergedIncidents, err := s.incidentMergeRepo.GetMergedIncidents(ctx, masterIncidentID)
	if err != nil {
		return err
	}
	if len(mergedIncidents) == 0 {
		return nil
	}

	now := time.Now()
	masterIncident, err := s.incidentRepo.FindByID(ctx, masterIncidentID)
	if err != nil {
		return err
	}

	oldStateName := transition.FromState.Name
	newStateName := transition.ToState.Name

	// Process each merged incident
	for _, merged := range mergedIncidents {
		// 1. Create transition history record for child
		childHistory := &models.IncidentTransitionHistory{
			IncidentID:     merged.ID,
			TransitionID:   &transition.ID,
			FromStateID:    transition.FromStateID,
			ToStateID:      transition.ToStateID,
			PerformedByID:  userID,
			Comment:        history.Comment,
			TransitionedAt: now,
		}
		if histErr := s.incidentRepo.CreateTransitionHistory(ctx, childHistory); histErr != nil {
			fmt.Printf("[DEBUG] Failed to create transition history for child %s: %v\n", merged.IncidentNumber, histErr)
		}

		// 2. Create revision record for child
		changes := []models.IncidentFieldChange{
			{
				FieldName:  "current_state_id",
				FieldLabel: "Status",
				OldValue:   &oldStateName,
				NewValue:   &newStateName,
			},
		}
		description := fmt.Sprintf(
			"Parent incident %s transitioned from %s to %s",
			masterIncident.IncidentNumber,
			oldStateName,
			newStateName,
		)

		if revErr := s.CreateRevision(ctx, merged.ID, models.RevisionActionStatusChanged, description, changes, userID); revErr != nil {
			fmt.Printf("[DEBUG] Failed to create revision for child %s: %v\n", merged.IncidentNumber, revErr)
		}

		// 3. If master had a comment, create a comment record for child too
		if history.Comment != "" {
			childComment := &models.IncidentComment{
				IncidentID:          merged.ID,
				AuthorID:            userID,
				Content:             history.Comment,
				IsInternal:          true,
				TransitionHistoryID: &childHistory.ID,
			}
			if commentErr := s.incidentRepo.CreateComment(ctx, childComment); commentErr != nil {
				fmt.Printf("[DEBUG] Failed to create comment for child %s: %v\n", merged.IncidentNumber, commentErr)
			}
		}
	}

	return nil
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

	// Calculate SLA deadline based on classification criticality (with fallback to workflow state SLA)
	var slaClassificationID uuid.UUID
	var slaErr error
	slaClassificationID, slaErr = uuid.Parse(req.ClassificationID)
	if slaErr != nil {
		slaClassificationID = uuid.Nil
	}
	var slaDeadline *time.Time
	slaDeadline, slaErr = s.calculateSLADeadline(ctx, &slaClassificationID, req.LookupValueIDs, initialState.SLAHours)
	if slaErr == nil && slaDeadline != nil {
		complaint.SLADeadline = slaDeadline
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
		// Geolocation fields
		Latitude:   req.Latitude,
		Longitude:  req.Longitude,
		Address:    req.Address,
		City:       req.City,
		State:      req.State,
		Country:    req.Country,
		PostalCode: req.PostalCode,
		// Reporter fields
		ReporterEmail: req.ReporterEmail,
		ReporterName:  req.ReporterName,
	}

	// Set Source if provided
	if req.Source != "" {
		query.Source = req.Source
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

	// Calculate SLA deadline based on classification criticality (with fallback to workflow state SLA)
	var slaClassificationID uuid.UUID
	var slaErr error
	slaClassificationID, slaErr = uuid.Parse(req.ClassificationID)
	if slaErr != nil {
		slaClassificationID = uuid.Nil
	}
	var slaDeadline *time.Time
	slaDeadline, slaErr = s.calculateSLADeadline(ctx, &slaClassificationID, req.LookupValueIDs, initialState.SLAHours)
	if slaErr == nil && slaDeadline != nil {
		query.SLADeadline = slaDeadline
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

// UpdateClosedIncidentSummary allows editing the description of a closed incident
func (s *incidentService) UpdateClosedIncidentSummary(
	ctx context.Context,
	incidentID uuid.UUID,
	userID uuid.UUID,
	newDescription string,
	reason string,
) (*models.IncidentResponse, error) {
	// Get incident with relations
	incident, err := s.incidentRepo.FindByIDWithRelations(ctx, incidentID)
	if err != nil {
		return nil, fmt.Errorf("incident not found: %w", err)
	}

	// Verify incident is in terminal (closed) state
	if incident.CurrentState == nil || incident.CurrentState.StateType != "terminal" {
		return nil, fmt.Errorf("incident is not closed")
	}

	// Store old description
	oldDescription := incident.Description

	// Create edit record
	editRecord := map[string]interface{}{
		"edited_by":       userID.String(),
		"edited_at":       time.Now().Format(time.RFC3339),
		"old_description": oldDescription,
		"new_description": newDescription,
		"reason":          reason,
	}

	// Get existing edits
	var existingEdits []map[string]interface{}
	if incident.PostClosureEdits != nil && len(incident.PostClosureEdits) > 0 {
		if err := json.Unmarshal(incident.PostClosureEdits, &existingEdits); err != nil {
			existingEdits = []map[string]interface{}{}
		}
	}

	// Append new edit
	existingEdits = append(existingEdits, editRecord)

	// Marshal back to JSON
	editsJSON, err := json.Marshal(existingEdits)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal edit history: %w", err)
	}

	// Update incident
	incident.Description = newDescription
	incident.PostClosureEdits = editsJSON
	incident.ClosedBy = &userID

	if err := s.incidentRepo.Update(ctx, incident); err != nil {
		return nil, fmt.Errorf("failed to update incident: %w", err)
	}

	// Fetch updated incident with relations
	updated, err := s.incidentRepo.FindByIDWithRelations(ctx, incidentID)
	if err != nil {
		return nil, err
	}

	resp := models.ToIncidentResponse(updated)
	return &resp, nil
}
