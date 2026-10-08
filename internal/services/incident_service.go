package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/automax/backend/internal/config"
	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/natsclient"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/internal/storage"
	"github.com/automax/backend/internal/utils"
	"github.com/automax/backend/pkg/constants"
	"github.com/automax/backend/pkg/i18n"
	pkgutils "github.com/automax/backend/pkg/utils"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

var ErrDuplicateIncident = errors.New("duplicate_incident")
var ErrInvalidLocation = errors.New("invalid_location")
var ErrEmptyWorkflow = errors.New("empty_workflow")
var ErrEditNotAllowed = errors.New("edit_not_allowed_in_current_state")

// An incident must be filed against a specific place and category, not an umbrella
// one, so its location and classification have to be leaves of their hierarchies.
// Following the convention above, each sentinel's message is its i18n key, which is
// what lets the handlers translate them without a per-error mapping table.
var ErrLocationNotFound = errors.New("location_not_found")
var ErrClassificationNotFound = errors.New("classification_not_found")
var ErrLocationNotSelectable = errors.New("location_not_selectable")
var ErrClassificationNotSelectable = errors.New("classification_not_selectable")

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
	TriggerEvaluation(ctx context.Context, id uuid.UUID) error

	// Query operations
	CreateQuery(ctx context.Context, req *models.CreateQueryRequest, creatorID uuid.UUID) (*models.IncidentResponse, error)

	// State transitions
	ExecuteTransition(ctx context.Context, incidentID uuid.UUID, req *models.IncidentTransitionRequest, userID uuid.UUID, userRoleIDs []uuid.UUID) (*models.IncidentResponse, error)
	GetAvailableTransitions(ctx context.Context, incidentID uuid.UUID, userID uuid.UUID, userRoleIDs []uuid.UUID) ([]models.AvailableTransitionResponse, error)
	GetTransitionHistory(ctx context.Context, incidentID uuid.UUID) ([]models.TransitionHistoryResponse, error)

	// Comments
	AddComment(ctx context.Context, incidentID uuid.UUID, req *models.IncidentCommentRequest, authorID uuid.UUID) (*models.IncidentCommentResponse, error)
	ListComments(ctx context.Context, incidentID uuid.UUID) ([]models.IncidentCommentResponse, error)
	UpdateComment(ctx context.Context, commentID uuid.UUID, req *models.IncidentCommentRequest, userID uuid.UUID) (*models.IncidentCommentResponse, error)
	DeleteComment(ctx context.Context, commentID uuid.UUID, userID uuid.UUID) error

	// Feedback
	ListFeedbacks(ctx context.Context, incidentID uuid.UUID) ([]models.IncidentFeedbackResponse, error)

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

	// Revisions
	ListRevisions(ctx context.Context, incidentID uuid.UUID, filter *models.IncidentRevisionFilter) ([]models.IncidentRevisionResponse, int64, error)
	CreateRevision(ctx context.Context, incidentID uuid.UUID, actionType models.IncidentRevisionActionType, description string, changes []models.IncidentFieldChange, userID uuid.UUID) error
	// SetUserService wires in the UserService (called post-construction to avoid circular deps).
	SetUserService(us UserService)
	// SetFCMService wires in the FCMService (called post-construction).
	SetFCMService(fcm *FCMService)
	// SetIntegrationExecutor wires in the IntegrationExecutor (called post-construction).
	SetIntegrationExecutor(exec IntegrationExecutor)
	// SetMOMRAStatusSyncService wires in the MOMRA outbound status sync (called
	// post-construction, same pattern as SetIntegrationExecutor).
	SetMOMRAStatusSyncService(svc MOMRAStatusSyncService)
	// SetActionExecutor wires in the ActionExecutor (called post-construction).
	SetActionExecutor(ae ActionExecutor)
	// SetPublicFeedbackRepo wires in the feedback repo so IsFinalClose transitions can
	// pre-create a feedback record and embed a direct GenerateFeedbackToken URL in the SMS.
	SetPublicFeedbackRepo(repo repository.IncidentPublicFeedbackRepository)
	// SetSmsFeedbackPendingRepo wires in the SMS feedback pending repo and delay hours.
	// The SLA monitor will send the SMS only if no WhatsApp response is received within delayHours.
	SetSmsFeedbackPendingRepo(repo repository.SmsFeedbackPendingRepository, delayHours int)
	// SetIvrSmsLinkRepo wires in the IvrSmsLinkRepository (called post-construction).
	SetIvrSmsLinkRepo(repo repository.IvrSmsLinkRepository)
	// SetConfig wires in the app config (called post-construction).
	SetConfig(cfg *config.Config)
	// SetNATSClient wires the process NATS client. A nil client skips the state-changed publish.
	SetNATSClient(client *natsclient.Client)

	// Closed incident editing
	UpdateClosedIncidentSummary(ctx context.Context, incidentID uuid.UUID, userID uuid.UUID, newDescription string, reason string) (*models.IncidentResponse, error)

	// Auto-assign monitor
	AutoAssignUnassigned(ctx context.Context) error
}

type incidentService struct {
	incidentRepo            repository.IncidentRepository
	incidentMergeRepo       repository.IncidentMergeRepository
	workflowRepo            repository.WorkflowRepository
	workflowService         WorkflowService
	userRepo                repository.UserRepository
	deptRepo                repository.DepartmentRepository
	rejectionLogRepo        repository.RejectionLogRepository
	classificationRepo      repository.ClassificationRepository
	locationRepo            repository.LocationRepository
	roleRepo                repository.RoleRepository
	storage                 *storage.MinIOStorage
	db                      *gorm.DB
	wsHub                   *WSHub
	readyToCloseService     ReadyToCloseService
	notificationService     *NotificationService
	userService             UserService
	fcmService              *FCMService
	integrationExecutor     IntegrationExecutor
	momraStatusSyncService  MOMRAStatusSyncService
	actionExecutor          ActionExecutor
	publicFeedbackRepo      repository.IncidentPublicFeedbackRepository
	smsFeedbackPendingRepo  repository.SmsFeedbackPendingRepository
	smsFeedbackDelayMinutes int
	ivrSmsLinkRepo          repository.IvrSmsLinkRepository
	rrCounters              map[string]int64
	rrMu                    sync.Mutex
	cfg                     *config.Config
	nats                    *natsclient.Client
}

func NewIncidentService(
	incidentRepo repository.IncidentRepository,
	incidentMergeRepo repository.IncidentMergeRepository,
	workflowRepo repository.WorkflowRepository,
	workflowService WorkflowService,
	userRepo repository.UserRepository,
	deptRepo repository.DepartmentRepository,
	classificationRepo repository.ClassificationRepository,
	locationRepo repository.LocationRepository,
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
		workflowService:    workflowService,
		userRepo:           userRepo,
		deptRepo:           deptRepo,
		rejectionLogRepo:   rejectionLogRepo,
		classificationRepo: classificationRepo,
		locationRepo:       locationRepo,
		roleRepo:           roleRepo,
		storage:            storage,
		db:                 db,
		wsHub:              wsHub,
		rrCounters:         make(map[string]int64),
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

// SetUserService wires the UserService into the incident service.
func (s *incidentService) SetUserService(us UserService) {
	s.userService = us
}

// SetConfig wires the app config into the incident service.
func (s *incidentService) SetConfig(cfg *config.Config) {
	s.cfg = cfg
}

func (s *incidentService) SetNATSClient(client *natsclient.Client) {
	s.nats = client
}

// SetIvrSmsLinkRepo wires the IvrSmsLinkRepository into the incident service.
func (s *incidentService) SetIvrSmsLinkRepo(repo repository.IvrSmsLinkRepository) {
	s.ivrSmsLinkRepo = repo
}

// SetFCMService wires the FCMService into the incident service.
func (s *incidentService) SetFCMService(fcm *FCMService) {
	s.fcmService = fcm
}

// SetMOMRAStatusSyncService wires the MOMRA outbound status sync into the incident service.
func (s *incidentService) SetMOMRAStatusSyncService(svc MOMRAStatusSyncService) {
	s.momraStatusSyncService = svc
}

// SetIntegrationExecutor wires the IntegrationExecutor into the incident service.
func (s *incidentService) SetIntegrationExecutor(exec IntegrationExecutor) {
	s.integrationExecutor = exec
}

func (s *incidentService) SetActionExecutor(ae ActionExecutor) {
	s.actionExecutor = ae
}

func (s *incidentService) SetPublicFeedbackRepo(repo repository.IncidentPublicFeedbackRepository) {
	s.publicFeedbackRepo = repo
}

func (s *incidentService) SetSmsFeedbackPendingRepo(repo repository.SmsFeedbackPendingRepository, delayMinutes int) {
	s.smsFeedbackPendingRepo = repo
	s.smsFeedbackDelayMinutes = delayMinutes
}

// convertibleStateCode returns the workflow state code an incident must be in
// to be eligible for conversion to a request. Configurable via
// CONVERT_TO_REQUEST_STATE_CODE (default "under_resolution").
func (s *incidentService) convertibleStateCode() string {
	if code := os.Getenv("CONVERT_TO_REQUEST_STATE_CODE"); code != "" {
		return code
	}
	return "under_resolution"
}

// isInConvertibleState reports whether the incident's current state permits
// conversion to a request.
func (s *incidentService) isInConvertibleState(inc *models.Incident) bool {
	return inc.CurrentState != nil && inc.CurrentState.Code == s.convertibleStateCode()
}

// calculateSLADeadline calculates the SLA deadline based on classification criticality.
// Falls back to slaDuration (from the workflow state) if no criticality-based setting exists.
func (s *incidentService) calculateSLADeadline(ctx context.Context, classificationID *uuid.UUID, lookupValueIDs []string, slaDuration time.Duration) (*time.Time, error) {
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

	// Fallback to workflow state SLA duration
	if slaDuration > 0 {
		deadlineTime := time.Now().Add(slaDuration)
		deadline = &deadlineTime
	}

	return deadline, nil
}

// UserHasAssignmentRole returns true if the user holds at least one of the given roles.
func (s *incidentService) UserHasAssignmentRole(ctx context.Context, userID uuid.UUID, assignmentRoles []models.Role) bool {
	if len(assignmentRoles) == 0 {
		return false
	}
	userRoles, err := s.userRepo.GetUserRoles(ctx, userID)
	if err != nil || len(userRoles) == 0 {
		return false
	}
	allowed := make(map[uuid.UUID]bool, len(assignmentRoles))
	for _, r := range assignmentRoles {
		allowed[r.ID] = true
	}
	for _, r := range userRoles {
		if allowed[r.ID] {
			return true
		}
	}
	return false
}

// roundRobinPoolKey generates a deterministic key for a set of role IDs.
// Only role IDs are used — classification/location/department filter the pool
// at query time but should not create separate round-robin sequences.
func roundRobinPoolKey(roleIDs []uuid.UUID) string {
	if len(roleIDs) == 0 {
		return "__all_agents__"
	}
	sortedRoles := make([]string, len(roleIDs))
	for i, id := range roleIDs {
		sortedRoles[i] = id.String()
	}
	sort.Strings(sortedRoles)

	hash := sha256.Sum256([]byte(strings.Join(sortedRoles, ",")))
	return hex.EncodeToString(hash[:])
}

// getNextRoundRobinAssignee picks the next agent from the online eligible pool using round-robin.
func (s *incidentService) getNextRoundRobinAssignee(ctx context.Context, roleIDs []uuid.UUID, classificationID, locationID, departmentID *uuid.UUID) (*uuid.UUID, error) {
	var users []models.User
	if os.Getenv("CALLING_ENABLED") == "true" {
		// For EPM940, use a different logic for finding online agents
		onlineUsers, err := s.userRepo.FindMatchingOnline(ctx, roleIDs, classificationID, locationID, departmentID, nil)
		if err != nil {
			return nil, fmt.Errorf("find online agents: %w", err)
		}
		if len(onlineUsers) == 0 {
			return nil, fmt.Errorf("%s", i18n.T(ctx, "no_online_agents"))
		}

		users = onlineUsers
	} else {
		// For other clients, use the default logic
		offlineUsers, err := s.userRepo.FindMatching(ctx, roleIDs, classificationID, locationID, departmentID, nil)
		if err != nil {
			return nil, fmt.Errorf("find agents: %w", err)
		}

		if len(offlineUsers) == 0 {
			return nil, fmt.Errorf("%s", i18n.T(ctx, "no_agents"))
		}
		users = offlineUsers
	}

	sort.Slice(users, func(i, j int) bool {
		return users[i].ID.String() < users[j].ID.String()
	})

	poolKey := roundRobinPoolKey(roleIDs)

	s.rrMu.Lock()
	s.rrCounters[poolKey]++
	idx := int(s.rrCounters[poolKey]-1) % len(users)
	s.rrMu.Unlock()

	return &users[idx].ID, nil
}

// Incident CRUD

// splitReporterName splits a full name on whitespace into first/middle/last parts:
// the first word is the first name, the last word (if more than one word) is the
// last name, and anything in between is the middle name.
func splitReporterName(name string) (first, middle, last string) {
	parts := strings.Fields(name)
	switch len(parts) {
	case 0:
		return "", "", ""
	case 1:
		return parts[0], "", ""
	case 2:
		return parts[0], "", parts[1]
	default:
		return parts[0], strings.Join(parts[1:len(parts)-1], " "), parts[len(parts)-1]
	}
}

func (s *incidentService) CreateIncident(ctx context.Context, req *models.IncidentCreateRequest, reporterID uuid.UUID) (*models.IncidentResponse, error) {
	// Validated first, before anything with a side effect: the IVR branch below can
	// register a brand-new citizen user, and a request we are going to reject must not
	// leave one behind.
	if err := s.validateIncidentHierarchySelection(ctx, req.LocationID, req.ClassificationID); err != nil {
		return nil, err
	}

	creatorID := reporterID // preserve before auto-registration block may overwrite reporterID
	clientCode := strings.TrimSpace(s.cfg.ClientCode)
	// For EPM940, any source other than web (IVR, WhatsApp, Mobile, etc.)
	// is an unauthenticated channel where the citizen has no account yet,
	// so fetch or auto-register a user based on their mobile number.
	isWebSource := strings.EqualFold(req.Source, constants.INCIDENT_SOURCE.WEB)
	if req.Source != "" && req.ReporterName != "" && req.ReporterPhone != "" && !isWebSource && strings.EqualFold(clientCode, constants.CLIENT_CODE.EPM940) {
		user, err := s.userRepo.FindByMobile(ctx, req.ReporterPhone)
		if err != nil && err != gorm.ErrRecordNotFound {
			fmt.Printf("CreateIncident: Error fetching user by mobile: %v\n", err)
			return nil, err
		}

		if user == nil || user.ID == uuid.Nil {
			role, err := s.roleRepo.FindByCode(ctx, constants.ROLES.CITIZEN)
			if err != nil {
				return nil, err
			}

			sourceSlug := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(req.Source)), " ", "")
			epoch := time.Now().Unix()
			normalizedPhone := pkgutils.NormalizeMobile(req.ReporterPhone, pkgutils.SystemCountryCode())
			registerReq := &models.UserRegisterRequest{
				Phone:    normalizedPhone,
				Email:    fmt.Sprintf("%s_%s_%d@%s", sourceSlug, strings.TrimPrefix(normalizedPhone, "+"), epoch, constants.APP.DOMAIN),
				Username: fmt.Sprintf("%s_%s_%d", constants.ROLES.CITIZEN, strings.TrimPrefix(normalizedPhone, "+"), epoch),
				Password: pkgutils.GenerateRandomPassword(12),
			}
			registerReq.FirstName, registerReq.MiddleName, registerReq.LastName = splitReporterName(req.ReporterName)

			if role != nil && role.ID != uuid.Nil {
				registerReq.RoleIDs = []uuid.UUID{role.ID}
			}

			authResp, err := s.userService.Register(ctx, registerReq)
			if err != nil {
				fmt.Printf("CreateIncident: Error registering %s citizen user: %v\n", sourceSlug, err)
				return nil, err
			}

			reporterID = authResp.User.ID
		} else {
			reporterID = user.ID
			first, middle, last := splitReporterName(req.ReporterName)
			if first != user.FirstName || middle != user.MiddleName || last != user.LastName {
				if err := s.userRepo.UpdateProfile(ctx, map[string]interface{}{
					"id":          user.ID,
					"first_name":  first,
					"middle_name": middle,
					"last_name":   last,
				}); err != nil {
					fmt.Printf("CreateIncident: Error updating reporter name for user %s: %v\n", user.ID, err)
					return nil, err
				}
			}
		}
		if err := s.incidentRepo.UpdateReporterNameByPhone(ctx, req.ReporterPhone, req.ReporterName); err != nil {
			fmt.Printf("CreateIncident: Error backfilling reporter name for phone %s: %v\n", req.ReporterPhone, err)
			return nil, err
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

	// Extract lat/lng from gis_location when not provided explicitly
	if req.Latitude == nil && req.Longitude == nil && len(req.GisLocation) > 0 {
		var gis struct {
			Data struct {
				GeoJson struct {
					DrawedPoint struct {
						Coordinates []float64 `json:"coordinates"`
					} `json:"drawedPoint"`
				} `json:"geoJson"`
			} `json:"data"`
		}
		if err := json.Unmarshal(req.GisLocation, &gis); err == nil {
			coords := gis.Data.GeoJson.DrawedPoint.Coordinates
			if len(coords) >= 2 {
				lng := coords[0]
				lat := coords[1]
				req.Latitude = &lat
				req.Longitude = &lng
			}
		}
	}

	if strings.EqualFold(clientCode, "EPM940") && !sourceSkipped {
		// Check if the incoming request has latitude, longitude, and classification
		if req.Latitude != nil && req.Longitude != nil && req.ClassificationID != nil {
			classificationID, err := uuid.Parse(*req.ClassificationID)
			if err != nil {
				return nil, ErrInvalidLocation
			}

			// Determine whether the original creating user is a Call Center Agent.
			// If the role lookup fails we fall through to the reporter-based check (safe default).
			isAgent := false
			if creatorRoles, err := s.userRepo.GetUserRoles(ctx, creatorID); err == nil {
				for _, r := range creatorRoles {
					if strings.EqualFold(r.Code, constants.ROLES.AGENT) {
						isAgent = true
						break
					}
				}
			}

			// WHATSAPP_SOURCE holds the chatbot's source string (e.g. "WhatsApp Chatbot").
			whatsappSource := strings.TrimSpace(os.Getenv("WHATSAPP_SOURCE"))
			isWhatsApp := whatsappSource != "" && strings.EqualFold(strings.TrimSpace(req.Source), whatsappSource)

			// For agents (and the WhatsApp chatbot) acting on behalf of citizens, scope the
			// duplicate check to the citizen's identity rather than the agent's reporterID, so
			// incidents filed for different citizens are never incorrectly blocked.
			var openIncidents []models.Incident
			if (isAgent || isWhatsApp) && req.ReporterPhone != "" {
				openIncidents, err = s.incidentRepo.FindOpenIncidentsForDuplicateCheckByCaller(
					ctx, req.ReporterPhone,
				)
			} else {
				openIncidents, err = s.incidentRepo.FindUserOpenIncidentsForDuplicateCheck(ctx, reporterID)
			}
			if err != nil {
				log.Printf("[IncidentService] Error occurred while fetching open incidents: %v", err)
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

	// Auto-resolve workflow for epmportal when the caller omits workflow_id
	if req.WorkflowID == "" && strings.EqualFold(req.Source, constants.INCIDENT_SOURCE.EPMPORTAL) {
		var locID, classID *uuid.UUID
		if req.LocationID != nil {
			if id, err := uuid.Parse(*req.LocationID); err == nil {
				locID = &id
			}
		}
		if req.ClassificationID != nil {
			if id, err := uuid.Parse(*req.ClassificationID); err == nil {
				classID = &id
			}
		}
		rt := req.RecordType
		if rt == "" {
			rt = "incident"
		}
		resolvedID, err := s.workflowService.ResolveWorkflow(ctx, locID, classID, rt)
		if err != nil {
			return nil, err
		}
		req.WorkflowID = resolvedID.String()
	}

	if req.WorkflowID == "" {
		return nil, ErrEmptyWorkflow
	}
	// Parse workflow ID
	workflowID, err := uuid.Parse(req.WorkflowID)
	if err != nil {
		return nil, errors.New(i18n.T(ctx, "invalid_workflow_id_lower"))
	}

	// Get the initial state of the workflow
	initialState, err := s.workflowRepo.GetInitialState(ctx, workflowID)
	if err != nil {
		return nil, errors.New(i18n.T(ctx, "workflow_no_initial_state"))
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

	// Merge custom lookup fields and extra request fields into CustomFields JSON
	var customFieldsJSON string
	{
		customFields := make(map[string]interface{})
		if len(req.CustomFields) > 0 {
			if err := json.Unmarshal(req.CustomFields, &customFields); err != nil {
				// Client sent custom_fields as a JSON-encoded string; unwrap and parse it.
				var s string
				if json.Unmarshal(req.CustomFields, &s) == nil {
					_ = json.Unmarshal([]byte(s), &customFields)
				}
			}
		}
		for key, value := range req.CustomLookupFields {
			customFields[key] = value
		}
		if len(customFields) > 0 {
			if b, err := json.Marshal(customFields); err == nil {
				customFieldsJSON = string(b)
			}
		}
	}

	incident := &models.Incident{
		IncidentNumber: incidentNumber,
		Title:          req.Title,
		Description:    req.Description,
		WorkflowID:     workflowID,
		CurrentStateID: initialState.ID,
		ReporterID:     &reporterID,
		// ReporterEmail:  req.ReporterEmail,
		// ReporterName:   req.ReporterName,
		// ReporterPhone:  req.ReporterPhone,
		CallerIdentity: req.CallerIdentity,
		CustomFields:   customFieldsJSON,
		GisLocation:    datatypes.JSON(req.GisLocation),
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

	if strings.EqualFold(req.Source, constants.INCIDENT_SOURCE.WEB) ||
		!strings.EqualFold(s.cfg.ClientCode, constants.CLIENT_CODE.EPM940) {
		incident.ReporterName = req.ReporterName
		incident.ReporterEmail = req.ReporterEmail
		incident.ReporterPhone = req.ReporterPhone
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
	deadline, err := s.calculateSLADeadline(ctx, classificationID, req.LookupValueIDs, initialState.SLADuration())
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

	// Apply creation-time assignment rules
	s.applyCreationTimeAssignment(ctx, incident, initialState, reporterID, req.Source)

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

	// IvrInstSms := strings.TrimSpace(os.Getenv("IVR_INST_SMS"))
	if strings.EqualFold(req.Source, constants.INCIDENT_SOURCE.IVR) &&
		strings.EqualFold(clientCode, constants.CLIENT_CODE.EPM940) {

		// const ivrSmsDuration = 24 * time.Hour
		// rawToken := pkgutils.GenerateIncidentToken(incident.ID.String(), ivrSmsDuration)
		// smsLink := pkgutils.BuildSMSLinkFromToken(ctx, incident.ID.String(), rawToken)
		// log.Printf("Generated IVR SMS link for incident %s", incident.ID)

		// // Track link so old links can be invalidated and submission state can be detected.
		// if s.ivrSmsLinkRepo != nil {
		// 	_ = s.ivrSmsLinkRepo.DeactivateAllForIncident(ctx, incident.ID)
		// 	_ = s.ivrSmsLinkRepo.Create(ctx, &models.IvrSmsLink{
		// 		IncidentID: incident.ID,
		// 		TokenHash:  pkgutils.HashToken(rawToken),
		// 		SentAt:     time.Now(),
		// 		ExpiresAt:  time.Now().Add(ivrSmsDuration),
		// 		IsActive:   true,
		// 	})
		// }

		var sent []string
		if req.ReporterPhone == "" {
			log.Printf("[IncidentService] No reporter phone for IVR incident %s, skipping SMS", incident.ID)
		}
		if req.ReporterPhone != "" {
			smsBody := fmt.Sprintf("Thank you for contacting Eastern Province Municipality. Your incident %s has been created.", incident.IncidentNumber)
			result, err := s.notificationService.SendNotification(
				ctx,
				"sms",
				nil,
				"en",
				[]string{req.ReporterPhone},
				nil,
				nil,
				"",
				smsBody,
				nil,
				nil,
				&reporterID,
				nil,
			)
			if result != nil && result.SentLog != nil {
				_ = s.notificationService.SetIncidentIDOnLogs(ctx, []uuid.UUID{result.SentLog.ID}, incident.ID)
			}
			if err != nil {
				log.Printf("[IncidentService] SMS failed for %s: %v", req.ReporterPhone, err)
			} else {
				sent = append(sent, "SMS")
			}
		}
		log.Printf("IVR incident created with ID %s, SMS sent: %v", incident.ID, sent)
		// log.Println("ivr sms link send: ", smsLink)

		// Log revision so agents can see when the initial SMS was sent.
		_ = s.CreateRevision(ctx, incident.ID, models.RevisionActionIVRSmsSent,
			"IVR SMS sent to citizen on incident creation", nil, reporterID)
	}

	// Send template-based email/SMS notifications for the initial state (if configured)
	if s.notificationService != nil && (initialState.NewIncidentEmailTemplateCode != "" || initialState.NewIncidentSMSTemplateCode != "") {
		bgCtx := context.WithValue(context.Background(), constants.ContextKeys.ACCEPT_LANGUAGE, ctx.Value(constants.ContextKeys.ACCEPT_LANGUAGE))
		capturedCreated := created
		capturedInitialState := initialState
		capturedReporterID := reporterID
		go func() {
			vars := BuildIncidentVariables(capturedCreated, nil, nil)
			if capturedCreated.Assignee != nil {
				vars["first_name"] = capturedCreated.Assignee.FirstName
				vars["last_name"] = capturedCreated.Assignee.LastName
			}
			log.Printf("[NEW-INCIDENT-NOTIFY] Built %d variable(s) for incident %s", len(vars), capturedCreated.IncidentNumber)
			if capturedInitialState.NewIncidentEmailTemplateCode != "" {
				var emails []string
				if capturedCreated.Assignee != nil && capturedCreated.Assignee.Email != "" {
					emails = append(emails, capturedCreated.Assignee.Email)
				}
				if capturedCreated.Reporter != nil && capturedCreated.Reporter.Email != "" {
					emails = append(emails, capturedCreated.Reporter.Email)
				} else if capturedCreated.ReporterEmail != "" {
					emails = append(emails, capturedCreated.ReporterEmail)
				}
				if len(emails) > 0 {
					code := capturedInitialState.NewIncidentEmailTemplateCode
					result, err := s.notificationService.SendNotification(
						bgCtx, "email", &code, "en",
						emails, nil, nil,
						"", "",
						vars, nil, &capturedReporterID, nil,
					)
					if result != nil && result.SentLog != nil {
						_ = s.notificationService.SetIncidentIDOnLogs(bgCtx, []uuid.UUID{result.SentLog.ID}, capturedCreated.ID)
					}
					if err != nil {
						log.Printf("NEW-INCIDENT-EMAIL: Failed for incident %s: %v", capturedCreated.IncidentNumber, err)
					} else {
						log.Printf("NEW-INCIDENT-EMAIL: Sent to %v for incident %s", emails, capturedCreated.IncidentNumber)
					}
				}
			}
			if capturedInitialState.NewIncidentSMSTemplateCode != "" {
				var phones []string
				if capturedCreated.Assignee != nil && capturedCreated.Assignee.Phone != "" {
					phones = append(phones, capturedCreated.Assignee.Phone)
				}
				if capturedCreated.Reporter != nil && capturedCreated.Reporter.Phone != "" {
					phones = append(phones, capturedCreated.Reporter.Phone)
				}
				if len(phones) > 0 {
					code := capturedInitialState.NewIncidentSMSTemplateCode
					result, err := s.notificationService.SendNotification(
						bgCtx, "sms", &code, "en",
						phones, nil, nil,
						"", "",
						vars, nil, &capturedReporterID, nil,
					)
					if result != nil && result.SentLog != nil {
						_ = s.notificationService.SetIncidentIDOnLogs(bgCtx, []uuid.UUID{result.SentLog.ID}, capturedCreated.ID)
					}
					if err != nil {
						log.Printf("NEW-INCIDENT-SMS: Failed for incident %s: %v", capturedCreated.IncidentNumber, err)
					} else {
						log.Printf("NEW-INCIDENT-SMS: Sent to %v for incident %s", phones, capturedCreated.IncidentNumber)
					}
				}
			}
		}()
	}

	// Send FCM push notification to the initial assignee (employee)
	if s.fcmService != nil && incident.AssigneeID != nil {
		bgCtx := context.WithValue(context.Background(), constants.ContextKeys.ACCEPT_LANGUAGE, ctx.Value(constants.ContextKeys.ACCEPT_LANGUAGE))
		capturedAssignee := *incident.AssigneeID
		capturedID := incident.ID
		capturedNumber := incident.IncidentNumber
		capturedTitle := incident.Title
		capturedDesc := incident.Description
		capturedType := strings.ToUpper(incident.RecordType)
		go func() {
			pushReq := &models.PushRequest{
				UserID: capturedAssignee,
				Title:  fmt.Sprintf("New %s Assigned: %s", capturedType, capturedNumber),
				Body:   fmt.Sprintf("Incident \"%s\" has been assigned to you. %s", capturedTitle, capturedDesc),
				Data: map[string]string{
					"id":   capturedID.String(),
					"type": capturedType,
				},
			}
			if err := s.fcmService.Push(bgCtx, pushReq); err != nil {
				log.Printf("FCM-NEW-INCIDENT: Failed for assignee %s: %v", capturedAssignee, err)
			} else {
				log.Printf("FCM-NEW-INCIDENT: Sent to assignee %s", capturedAssignee)
			}
		}()
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
		return nil, errors.New(i18n.T(ctx, "invalid_incident_lower"))
	}
	if incident.ReporterID == nil {
		return nil, errors.New(i18n.T(ctx, "invalid_incident_lower"))
	}
	log.Println("incident fetch via last 6 digit")

	resp := &models.IncidentResponse{
		ID:             incident.ID,
		IncidentNumber: incident.IncidentNumber,
		Title:          incident.Title,
		Description:    incident.Description,
		ReporterEmail:  incident.ReporterEmail,
		ReporterName:   incident.ReporterName,
		ReporterPhone:  incident.ReporterPhone,
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
		Version:        incident.Version,
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

		// Populate merged incidents count for master incidents
		// This is needed because MergedIncidents is not a GORM relation (gorm:"-")
		if s.incidentMergeRepo != nil {
			mergedIncidents, _ := s.incidentMergeRepo.GetMergedIncidents(ctx, inc.ID)
			responses[i].MergedIncidentsCount = len(mergedIncidents)
		}
	}

	// For EPM940: enrich IVR incidents with SMS link submission state.
	// Collect IVR incident IDs, do a single batch lookup, then stamp each response.
	clientCode := strings.TrimSpace(s.cfg.ClientCode)
	if strings.EqualFold(clientCode, constants.CLIENT_CODE.EPM940) && s.ivrSmsLinkRepo != nil {
		var ivrIDs []uuid.UUID
		ivrIdx := make(map[uuid.UUID]int, len(incidents))
		for i, inc := range incidents {
			if strings.EqualFold(inc.Source, constants.INCIDENT_SOURCE.IVR) {
				ivrIDs = append(ivrIDs, inc.ID)
				ivrIdx[inc.ID] = i
			}
		}
		if len(ivrIDs) > 0 {
			submittedLinks, err := s.ivrSmsLinkRepo.FindSubmittedByIncidentIDs(ctx, ivrIDs)
			if err == nil {
				for incID, idx := range ivrIdx {
					if link, ok := submittedLinks[incID]; ok {
						responses[idx].IvrSubmitted = true
						responses[idx].IvrSubmittedAt = link.SubmittedAt
					}
				}
			}
		}
	}

	return responses, total, nil
}

// momraEEListEntry mirrors epm_incident_handler.go's EPMExternalEntity JSON shape
// (EntityID/EECode/EEName) for unmarshaling Incident.AvailableEEList (a jsonb column,
// see models/incident.go). Duplicated here rather than imported since this services
// package doesn't depend on the handlers package.
type momraEEListEntry struct {
	EntityID string `json:"EntityID"`
	EECode   string `json:"EECode"`
	EEName   string `json:"EEName"`
}

// resolveIncidentEEDepartmentIDs resolves the set of Department IDs that MOMRA
// declared eligible for THIS specific incident at submission time
// (Incident.AvailableEEList — see epm_incident_handler.go's EEList/EPMExternalEntity
// handling). This is narrower than the classification-wide EE-classification links
// (deptRepo.FindMatching): MOMRA may name fewer EEs eligible for a given incident than
// are generally linked to its special classification, so this incident-specific list
// is the authoritative source for validateExternalDepartmentAssignment. Entries are
// resolved the same way epm_incident_handler.go's resolveEERoutingDepartmentByCodeOrName
// does: EntityID/EECode first (the stable key synced from MOMRA's EE master into
// Department.Code), EEName as a fallback.
func (s *incidentService) resolveIncidentEEDepartmentIDs(ctx context.Context, incident *models.Incident) map[uuid.UUID]bool {
	allowed := map[uuid.UUID]bool{}
	for _, d := range s.resolveIncidentEEDepartments(ctx, incident) {
		allowed[d.ID] = true
	}
	return allowed
}

// resolveIncidentEEDepartments is the full-record counterpart of
// resolveIncidentEEDepartmentIDs: same resolution (EntityID/EECode first, EEName
// fallback), but returns the actual Department records rather than just a
// membership set. Used by ExecuteTransition's auto-detect block below to merge these
// incident-specific EEs into the classification/location-matched candidate list —
// mirrors department_handler.go's MatchDepartment, which the frontend's picker uses,
// so both stay in agreement about which departments are actually selectable.
func (s *incidentService) resolveIncidentEEDepartments(ctx context.Context, incident *models.Incident) []models.Department {
	if len(incident.AvailableEEList) == 0 {
		return nil
	}
	var entries []momraEEListEntry
	if err := json.Unmarshal(incident.AvailableEEList, &entries); err != nil {
		return nil
	}
	var result []models.Department
	seen := make(map[uuid.UUID]bool, len(entries))
	for _, e := range entries {
		var dept *models.Department
		code := strings.TrimSpace(e.EntityID)
		if code == "" {
			code = strings.TrimSpace(e.EECode)
		}
		if code != "" {
			if d, err := s.deptRepo.FindByCode(ctx, code); err == nil {
				dept = d
			}
		}
		if dept == nil && strings.TrimSpace(e.EEName) != "" {
			if d, err := s.deptRepo.FindByNameOrNameAr(ctx, e.EEName, e.EEName); err == nil {
				dept = d
			}
		}
		if dept != nil && dept.Type == externalEntityDepartmentType && dept.IsActive && !seen[dept.ID] {
			seen[dept.ID] = true
			result = append(result, *dept)
		}
	}
	return result
}

// validateExternalDepartmentAssignment enforces that, for an incident originally
// submitted by MOMRA (Source == "MOMRA"), assigning an external-type department only
// succeeds if that department is one of the External Entities MOMRA declared eligible
// for this specific incident at submission time (see resolveIncidentEEDepartmentIDs).
// Internal-type departments and non-MOMRA incidents are unrestricted — this rule only
// protects MOMRA-sourced external-entity assignment.
func (s *incidentService) validateExternalDepartmentAssignment(ctx context.Context, incident *models.Incident, departmentID uuid.UUID) error {
	if incident.Source != "MOMRA" {
		return nil
	}
	dept, err := s.deptRepo.FindByID(ctx, departmentID)
	if err != nil {
		return fmt.Errorf("department not found: %w", err)
	}
	if dept.Type != externalEntityDepartmentType {
		return nil
	}
	if !s.resolveIncidentEEDepartmentIDs(ctx, incident)[departmentID] {
		return fmt.Errorf("external entity %s is not in this incident's MOMRA-provided EE list", dept.Name)
	}
	return nil
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
		return nil, errors.New(i18n.T(ctx, "child_incidents_locked"))
	}

	// // Source validation: for IVR calls from EPM940, source must be provided.
	clientCode := strings.TrimSpace(s.cfg.ClientCode)
	if strings.EqualFold(req.Source, constants.INCIDENT_SOURCE.IVR) && strings.EqualFold(clientCode, constants.CLIENT_CODE.EPM940) {
		if req.Source == "" || req.Source != constants.INCIDENT_SOURCE.IVR || incident.Comments == nil {
			tx.Rollback()
			return nil, errors.New(i18n.T(ctx, "source_required_ivr"))
		}
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
				if newDeadline, err := s.calculateSLADeadline(ctx, effectiveClassID, req.LookupValueIDs, 0); err == nil && newDeadline != nil {
					incident.SLADeadline = newDeadline
					incident.SLABreached = false
				}
			}
		}
	}

	// Handle custom fields and custom lookup fields
	if len(req.CustomFields) > 0 || len(req.CustomLookupFields) > 0 {
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
		if len(req.CustomFields) > 0 {
			var newCustomFields map[string]interface{}
			if err := json.Unmarshal(req.CustomFields, &newCustomFields); err != nil {
				var s string
				if json.Unmarshal(req.CustomFields, &s) == nil {
					_ = json.Unmarshal([]byte(s), &newCustomFields)
				}
			}
			for k, v := range newCustomFields {
				customFields[k] = v
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
			// Only audit when the merged result actually differs - the merge above is additive, so
			// re-submitting identical values is a no-op and must not produce a revision.
			if string(customFieldsBytes) != incident.CustomFields {
				changes = append(changes, models.IncidentFieldChange{
					FieldName:  "custom_fields",
					FieldLabel: "Custom Fields",
				})
				descriptions = append(descriptions, "Custom fields updated")
			}
			incident.CustomFields = string(customFieldsBytes)
		}
	}

	if len(req.GisLocation) > 0 {
		incident.GisLocation = datatypes.JSON(req.GisLocation)
		changes = append(changes, models.IncidentFieldChange{
			FieldName:  "gis_location",
			FieldLabel: "GIS Location",
		})
	}

	if len(req.ReporterEmail) > 0 {
		incident.ReporterEmail = req.ReporterEmail
		changes = append(changes, models.IncidentFieldChange{
			FieldName:  "reporter_email",
			FieldLabel: "Reporter Email",
		})
	}

	if len(req.ReporterPhone) > 0 {
		incident.ReporterPhone = req.ReporterPhone
		changes = append(changes, models.IncidentFieldChange{
			FieldName:  "reporter_phone",
			FieldLabel: "Reporter Phone",
		})
	}

	if len(req.ReporterName) > 0 {
		incident.ReporterName = req.ReporterName
		changes = append(changes, models.IncidentFieldChange{
			FieldName:  "reporter_name",
			FieldLabel: "Reporter Name",
		})
	}

	// Parse optional UUIDs
	if req.ClassificationID != nil {
		oldName := ""
		if incident.Classification != nil {
			oldName = incident.Classification.Name
		} else if incident.ClassificationID != nil {
			oldName = incident.ClassificationID.String()
		}
		if *req.ClassificationID == "" {
			if incident.ClassificationID != nil {
				changes = append(changes, models.IncidentFieldChange{
					FieldName:  "classification_id",
					FieldLabel: "Classification",
					OldValue:   &oldName,
					NewValue:   nil,
				})
				descriptions = append(descriptions, fmt.Sprintf("Classification cleared (was %s)", oldName))
			}
			incident.ClassificationID = nil
		} else {
			classID, err := uuid.Parse(*req.ClassificationID)
			if err == nil {
				if incident.ClassificationID == nil || *incident.ClassificationID != classID {
					// Only a genuinely new value is validated. Incidents created before
					// this rule may sit on a non-leaf classification, and resubmitting
					// the unchanged value must not make them uneditable.
					if _, err := s.validateSelectableClassification(ctx, *req.ClassificationID); err != nil {
						return nil, err
					}
					newVal := *req.ClassificationID
					changes = append(changes, models.IncidentFieldChange{
						FieldName:  "classification_id",
						FieldLabel: "Classification",
						OldValue:   &oldName,
						NewValue:   &newVal,
					})
					descriptions = append(descriptions, fmt.Sprintf("Classification changed from %s", oldName))
				}
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
		oldName := ""
		if incident.Department != nil {
			oldName = incident.Department.Name
		} else if incident.DepartmentID != nil {
			oldName = incident.DepartmentID.String()
		}
		if *req.DepartmentID == "" {
			if incident.DepartmentID != nil {
				changes = append(changes, models.IncidentFieldChange{
					FieldName:  "department_id",
					FieldLabel: "Department",
					OldValue:   &oldName,
					NewValue:   nil,
				})
				descriptions = append(descriptions, fmt.Sprintf("Department cleared (was %s)", oldName))
			}
			incident.DepartmentID = nil
		} else {
			deptID, err := uuid.Parse(*req.DepartmentID)
			if err == nil {
				if err := s.validateExternalDepartmentAssignment(ctx, incident, deptID); err != nil {
					tx.Rollback()
					return nil, err
				}
				if incident.DepartmentID == nil || *incident.DepartmentID != deptID {
					newVal := *req.DepartmentID
					changes = append(changes, models.IncidentFieldChange{
						FieldName:  "department_id",
						FieldLabel: "Department",
						OldValue:   &oldName,
						NewValue:   &newVal,
					})
					descriptions = append(descriptions, fmt.Sprintf("Department changed from %s", oldName))
				}
				incident.DepartmentID = &deptID
			}
		}
	}

	if req.LocationID != nil {
		oldName := ""
		if incident.Location != nil {
			oldName = incident.Location.Name
		} else if incident.LocationID != nil {
			oldName = incident.LocationID.String()
		}
		if *req.LocationID == "" {
			if incident.LocationID != nil {
				changes = append(changes, models.IncidentFieldChange{
					FieldName:  "location_id",
					FieldLabel: "Location",
					OldValue:   &oldName,
					NewValue:   nil,
				})
				descriptions = append(descriptions, fmt.Sprintf("Location cleared (was %s)", oldName))
			}
			incident.LocationID = nil
		} else {
			locID, err := uuid.Parse(*req.LocationID)
			if err == nil {
				if incident.LocationID == nil || *incident.LocationID != locID {
					// Changed values only — see the classification block above.
					if _, err := s.validateSelectableLocation(ctx, *req.LocationID); err != nil {
						return nil, err
					}
					newVal := *req.LocationID
					changes = append(changes, models.IncidentFieldChange{
						FieldName:  "location_id",
						FieldLabel: "Location",
						OldValue:   &oldName,
						NewValue:   &newVal,
					})
					descriptions = append(descriptions, fmt.Sprintf("Location changed from %s", oldName))
				}
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

	// trackAddressField records an address-component edit. These were persisted without any
	// revision entry, so an address-only edit produced no audit row - and was rejected outright by
	// the len(changes)==0 guard below.
	trackAddressField := func(name, label, newValue string, target *string) {
		if newValue == "" || newValue == *target {
			return
		}
		oldVal := *target
		changes = append(changes, models.IncidentFieldChange{
			FieldName:  name,
			FieldLabel: label,
			OldValue:   &oldVal,
			NewValue:   &newValue,
		})
		descriptions = append(descriptions, fmt.Sprintf("%s changed from %s to %s", label, oldVal, newValue))
		*target = newValue
	}

	trackAddressField("address", "Address", req.Address, &incident.Address)
	trackAddressField("city", "City", req.City, &incident.City)
	trackAddressField("state", "State", req.State, &incident.State)
	trackAddressField("country", "Country", req.Country, &incident.Country)
	trackAddressField("postal_code", "Postal Code", req.PostalCode, &incident.PostalCode)

	if req.DueDate != nil {
		oldVal := ""
		if incident.DueDate != nil {
			oldVal = incident.DueDate.Format(time.RFC3339)
		}
		if *req.DueDate == "" {
			if incident.DueDate != nil {
				changes = append(changes, models.IncidentFieldChange{
					FieldName:  "due_date",
					FieldLabel: "Due Date",
					OldValue:   &oldVal,
					NewValue:   nil,
				})
				descriptions = append(descriptions, fmt.Sprintf("Due Date cleared (was %s)", oldVal))
			}
			incident.DueDate = nil
		} else {
			dueDate, err := time.Parse(time.RFC3339, *req.DueDate)
			if err == nil {
				if incident.DueDate == nil || !incident.DueDate.Equal(dueDate) {
					newVal := dueDate.Format(time.RFC3339)
					changes = append(changes, models.IncidentFieldChange{
						FieldName:  "due_date",
						FieldLabel: "Due Date",
						OldValue:   &oldVal,
						NewValue:   &newVal,
					})
					descriptions = append(descriptions, fmt.Sprintf("Due Date changed from %s to %s", oldVal, newVal))
				}
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
	// Newly added Reporter Phone,Reporter Email, Reporter Name
	if req.ReporterEmail != "" {
		updates["reporter_email"] = incident.ReporterEmail
	}
	if req.ReporterPhone != "" {
		updates["reporter_phone"] = incident.ReporterPhone
	}
	if req.ReporterName != "" {
		updates["reporter_name"] = incident.ReporterName
	}

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
	if len(incident.GisLocation) > 0 {
		updates["gis_location"] = incident.GisLocation
	}
	// Persist recalculated SLA deadline when lookup values were updated
	if req.LookupValueIDs != nil && incident.SLADeadline != nil {
		updates["sla_deadline"] = *incident.SLADeadline
		updates["sla_breached"] = incident.SLABreached
	}
	log.Println(len(updates), len(changes))
	if len(updates) == 0 || len(changes) == 0 {
		tx.Rollback()
		return nil, errors.New(i18n.T(ctx, "no_changes_detected"))
	}
	// Execute optimistic lock update
	if err := txRepo.UpdateFieldsWithVersion(ctx, id, updates, req.Version); err != nil {
		tx.Rollback()
		if err == repository.ErrVersionMismatch {
			return nil, fmt.Errorf("%s", i18n.T(ctx, "incident_conflict"))
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

	// Save optional comment attached to the update.
	if strings.TrimSpace(req.Comment) != "" {
		comment := &models.IncidentComment{
			IncidentID: id,
			AuthorID:   userID,
			Content:    strings.TrimSpace(req.Comment),
			IsInternal: false,
		}
		if err := s.incidentRepo.CreateComment(ctx, comment); err != nil {
			fmt.Printf("Warning: failed to save update comment: %v\n", err)
		}
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

	// Sync location change to merged child incidents
	if req.LocationID != nil && s.incidentMergeRepo != nil {
		hasMerged, _ := s.incidentMergeRepo.HasMergedIncidents(ctx, id)
		if hasMerged {
			newLocationName := ""
			if updated.Location != nil {
				newLocationName = updated.Location.Name
			}
			_ = s.SyncLocationToMergedIncidents(ctx, id, updated.LocationID, newLocationName, userID)
		}
	}

	resp := models.ToIncidentResponse(updated)

	if len(updated.Assignees) == 0 {
		log.Printf("No Assignees found for incident Number %s incident no. %s", incident.IncidentNumber, incident.ID.String())
	}

	// Mark SMS link as submitted whenever the citizen forwards their ivr_link_token.
	// This runs regardless of the source field — the token presence is the definitive signal.
	if req.IvrLinkToken != "" && s.ivrSmsLinkRepo != nil {
		var linkID uuid.UUID
		tokenHash := pkgutils.HashToken(req.IvrLinkToken)
		log.Printf("[IVR] looking up link by token hash %s for incident %s", tokenHash, id)
		if link, err := s.ivrSmsLinkRepo.FindByTokenHash(ctx, tokenHash, id); err == nil && link != nil {
			linkID = link.ID
		} else {
			log.Printf("[IVR] token hash lookup failed (%v), falling back to latest active link", err)
			if link, err := s.ivrSmsLinkRepo.FindLatestActiveByIncidentID(ctx, id); err == nil && link != nil {
				linkID = link.ID
			}
		}
		if linkID != uuid.Nil {
			if err := s.ivrSmsLinkRepo.MarkSubmitted(ctx, linkID, updated.ReporterPhone); err != nil {
				log.Printf("[IVR] MarkSubmitted failed for link %s: %v", linkID, err)
			} else {
				log.Printf("[IVR] link %s marked as submitted for incident %s", linkID, id)
			}
		} else {
			log.Printf("[IVR] no active link found to mark as submitted for incident %s", id)
		}
		_ = s.CreateRevision(ctx, id, models.RevisionActionIVRSmsSubmitted,
			"Citizen submitted additional information via IVR SMS link", changes, userID)
	}

	// Send in-app notification and revision for IVR updates from EPM940.
	if strings.EqualFold(req.Source, constants.INCIDENT_SOURCE.IVR) && strings.EqualFold(clientCode, constants.CLIENT_CODE.EPM940) {

		// Send in-app notification to all assigned agents
		if s.notificationService != nil && len(updated.Assignees) != 0 {
			var emails []string
			seen := make(map[string]bool)
			addEmail := func(email string) {
				if email != "" && !seen[email] {
					seen[email] = true
					emails = append(emails, email)
				}
			}
			if updated.Assignee != nil {
				addEmail(updated.Assignee.Email)
			}
			for _, a := range updated.Assignees {
				log.Printf("Iterating assignees: %v, %v, %v", a.ID, a.Username, a.Email)
				addEmail(a.Email)
			}
			// if len(emails) > 0 {
			// 	log.Printf("Sending update notification to emails: %v", emails)
			// 	bgCtx := context.Background()
			// 	capturedEmails := emails
			// 	capturedNumber := updated.IncidentNumber
			// 	capturedTitle := updated.Title
			// 	capturedID := updated.ID
			// 	capturedUserID := userID
			// 	go func() {
			// 		subject := fmt.Sprintf("Incident Updated: %s", capturedNumber)
			// 		notifBody := fmt.Sprintf("Incident \"%s\" (%s) has been updated.", capturedTitle, capturedNumber)
			// 		if _, err := s.notificationService.SendNotification(
			// 			bgCtx, "notification", nil, "en",
			// 			capturedEmails, nil, nil,
			// 			subject, notifBody,
			// 			map[string]string{
			// 				"incident_id":     capturedID.String(),
			// 				"incident_number": capturedNumber,
			// 				"incident_title":  capturedTitle,
			// 				"id":              capturedID.String(),
			// 			},
			// 			nil, &capturedUserID, nil,
			// 		); err != nil {
			// 			log.Printf("UPDATE-INCIDENT-NOTIFY: Failed for %s: %v", capturedNumber, err)
			// 		}
			// 	}()
			// }

			if len(emails) > 0 {
				subject := fmt.Sprintf("Incident %s updated", incident.IncidentNumber)
				body := fmt.Sprintf("Incident \"%s\" (%s) has been updated.", incident.Title, incident.IncidentNumber)
				subjectAr, bodyAr := IncidentUpdatedTextsAr(incident.IncidentNumber, incident.Title)

				if result, err := s.notificationService.SendNotification(
					ctx, "notification", nil, "en",
					emails, nil, nil,
					subject, body,
					nil, nil, &userID, nil,
				); err == nil && len(result.InboxLogIDs) > 0 {
					_ = s.notificationService.SetMetaOnLogs(ctx, result.InboxLogIDs, &models.NotificationMeta{
						ID:   id.String(),
						Type: strings.ToUpper(incident.RecordType),
					})
					_ = s.notificationService.SetArContentOnLogs(ctx, result.InboxLogIDs, subjectAr, bodyAr)
				}
			}
		}
	}
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
		return nil, errors.New(i18n.T(ctx, "incident_not_found"))
	}

	// Validate it's not already a request
	if sourceIncident.RecordType == "request" {
		return nil, errors.New(i18n.T(ctx, "cannot_convert_request"))
	}

	// Check if already converted
	if sourceIncident.ConvertedRequestID != nil {
		return nil, errors.New(i18n.T(ctx, "already_converted_to_request"))
	}

	// Only allow conversion while the incident is in the configured state (e.g. "Under Resolution")
	if !s.isInConvertibleState(sourceIncident) {
		return nil, errors.New(i18n.T(ctx, "convert_requires_resolution_state"))
	}

	// Handle existing request linking
	var existingRequest *models.Incident
	if req.ExistingRequestID != nil && *req.ExistingRequestID != "" {
		existingRequestID, err := uuid.Parse(*req.ExistingRequestID)
		if err != nil {
			return nil, errors.New(i18n.T(ctx, "invalid_existing_request_id"))
		}

		// Get the existing request
		existingRequest, err = s.incidentRepo.FindByIDWithRelations(ctx, existingRequestID)
		if err != nil {
			return nil, errors.New(i18n.T(ctx, "existing_request_not_found"))
		}

		// Validate it's a request
		if existingRequest.RecordType != "request" {
			return nil, errors.New(i18n.T(ctx, "id_not_a_request"))
		}

		// Append this incident to the existing request's source incidents
		existingSourceIDs := existingRequest.SourceIncidentIDs
		// If the array is empty but the singular field is set (request created before array was populated),
		// seed the array from the singular field so it isn't lost.
		if len(existingSourceIDs) == 0 && existingRequest.SourceIncidentID != nil {
			existingSourceIDs = []string{existingRequest.SourceIncidentID.String()}
		}
		sourceIncidentIDStrs := []string{incidentID.String()}
		if len(existingSourceIDs) > 0 {
			sourceIncidentIDStrs = append(existingSourceIDs, sourceIncidentIDStrs...)
		}
		sourceIncidentIDsJSON, err := json.Marshal(sourceIncidentIDStrs)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", i18n.T(ctx, "failed_to_marshal_source_incident_ids"), err)
		}

		// Update existing request with new source incident
		updateFieldsReq := map[string]interface{}{
			"source_incident_ids": sourceIncidentIDsJSON,
		}
		if existingRequest.SourceIncidentID == nil {
			updateFieldsReq["source_incident_id"] = &incidentID
		}
		if err := s.incidentRepo.UpdateFields(ctx, existingRequestID, updateFieldsReq); err != nil {
			fmt.Printf("Warning: failed to update existing request source incidents: %v\n", err)
		}

		// Append the source incident's lookup values (e.g. priority) to the existing request
		if len(sourceIncident.LookupValues) > 0 {
			if err := s.incidentRepo.AppendLookupValues(ctx, existingRequestID, sourceIncident.LookupValues); err != nil {
				fmt.Printf("Warning: failed to append lookup values to existing request: %v\n", err)
			}
		}

		// Link incident to existing request
		updateFields := map[string]interface{}{
			"converted_request_id": existingRequest.ID,
		}

		// Find a terminal state to close the incident
		terminalState, err := s.getTerminalStateForWorkflow(ctx, sourceIncident.WorkflowID)
		if err != nil {
			fmt.Printf("Warning: failed to find terminal state for workflow: %v\n", err)
		}

		if terminalState != nil {
			updateFields["current_state_id"] = terminalState.ID
			updateFields["closed_at"] = time.Now()
		}

		if err := s.incidentRepo.UpdateFields(ctx, incidentID, updateFields); err != nil {
			return nil, fmt.Errorf("%s: %w", i18n.T(ctx, "failed_to_update_source_incident"), err)
		}

		// Create transition history
		now := time.Now()
		convertHistory := &models.IncidentTransitionHistory{
			IncidentID:     incidentID,
			TransitionID:   nil,
			FromStateID:    sourceIncident.CurrentStateID,
			ToStateID:      terminalState.ID,
			PerformedByID:  userID,
			Comment:        fmt.Sprintf("Linked to existing request %s", existingRequest.IncidentNumber),
			TransitionedAt: now,
			OldValues:      fmt.Sprintf(`{"record_type": "incident", "incident_number": "%s"}`, sourceIncident.IncidentNumber),
			NewValues:      fmt.Sprintf(`{"record_type": "request", "request_number": "%s"}`, existingRequest.IncidentNumber),
		}
		if histErr := s.incidentRepo.CreateTransitionHistory(ctx, convertHistory); histErr != nil {
			fmt.Printf("Warning: failed to create transition history: %v\n", histErr)
		}

		// Create revision for source incident
		sourceIncidentNumber := sourceIncident.IncidentNumber
		changes := []models.IncidentFieldChange{
			{
				FieldName:  "linked_to_request",
				FieldLabel: "Linked to Request",
				OldValue:   nil,
				NewValue:   &existingRequest.IncidentNumber,
			},
		}
		description := fmt.Sprintf("Incident linked to existing request %s", existingRequest.IncidentNumber)
		_ = s.CreateRevision(ctx, incidentID, models.RevisionActionFieldChange, description, changes, userID)

		// Create revision for existing request
		changes = []models.IncidentFieldChange{
			{
				FieldName:  "linked_incident",
				FieldLabel: "Incident converted and linked",
				OldValue:   nil,
				NewValue:   &sourceIncidentNumber,
			},
		}
		description = fmt.Sprintf("Incident %s converted and linked to this request", sourceIncidentNumber)
		_ = s.CreateRevision(ctx, existingRequest.ID, models.RevisionActionFieldChange, description, changes, userID)

		// Create comment on existing request with feedback
		if req.Feedback != nil && req.Feedback.Comment != "" {
			feedbackComment := &models.IncidentComment{
				IncidentID: existingRequest.ID,
				Content:    req.Feedback.Comment,
				AuthorID:   userID,
			}
			if err := s.incidentRepo.CreateComment(ctx, feedbackComment); err != nil {
				fmt.Printf("Warning: failed to create feedback comment on existing request: %v\n", err)
			}

			// Also create comment on source incident
			sourceFeedbackComment := &models.IncidentComment{
				IncidentID: incidentID,
				Content:    req.Feedback.Comment,
				AuthorID:   userID,
			}
			if err := s.incidentRepo.CreateComment(ctx, sourceFeedbackComment); err != nil {
				fmt.Printf("Warning: failed to create feedback comment on source incident: %v\n", err)
			}
		}

		// Send SMS to citizen
		bgCtxExist := context.WithValue(context.Background(), constants.ContextKeys.ACCEPT_LANGUAGE, ctx.Value(constants.ContextKeys.ACCEPT_LANGUAGE))
		existReqNum := existingRequest.IncidentNumber
		go func(inc *models.Incident) {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("CONVERT-TO-REQUEST-SMS: Panic recovered for incident %s: %v", inc.IncidentNumber, r)
				}
			}()
			s.sendConvertToRequestSMS(bgCtxExist, inc, existReqNum, userID)
		}(sourceIncident)

		// Build response
		originalResp := models.ToIncidentResponse(sourceIncident)
		existingResp := models.ToIncidentResponse(existingRequest)

		return &models.ConvertToRequestResponse{
			OriginalIncident: &originalResp,
			NewRequest:       &existingResp,
		}, nil
	}

	// Check role-based permission for converting to request
	workflow, err := s.workflowRepo.FindByIDWithRelations(ctx, sourceIncident.WorkflowID)
	if err != nil {
		return nil, errors.New(i18n.T(ctx, "workflow_not_found"))
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
			return nil, errors.New(i18n.T(ctx, "no_permission_convert"))
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
			return nil, errors.New(i18n.T(ctx, "failed_reload_after_transition"))
		}
	}

	// Parse workflow ID
	workflowID, err := uuid.Parse(req.WorkflowID)
	if err != nil {
		return nil, errors.New(i18n.T(ctx, "invalid_workflow_id_lower"))
	}

	// Get the initial state of the request workflow
	initialState, err := s.workflowRepo.GetInitialState(ctx, workflowID)
	if err != nil {
		return nil, errors.New(i18n.T(ctx, "workflow_no_initial_state"))
	}

	// Parse classification ID
	classificationID, err := uuid.Parse(req.ClassificationID)
	if err != nil {
		return nil, errors.New(i18n.T(ctx, "invalid_classification_id"))
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
		IncidentNumber:    requestNumber,
		Title:             title,
		Description:       description,
		RecordType:        "request",
		SourceIncidentID:  &incidentID,
		SourceIncidentIDs: []string{incidentID.String()},
		ClassificationID:  &classificationID,
		WorkflowID:        workflowID,
		CurrentStateID:    initialState.ID,
		ReporterID:        sourceIncident.ReporterID,
		ReporterEmail:     sourceIncident.ReporterEmail,
		ReporterName:      sourceIncident.ReporterName,
		LocationID:        sourceIncident.LocationID,
		Latitude:          sourceIncident.Latitude,
		Longitude:         sourceIncident.Longitude,
		CustomFields:      sourceIncident.CustomFields,
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
	slaDeadline, err = s.calculateSLADeadline(ctx, &slaClassificationID, []string{}, initialState.SLADuration())
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

	// Create comment on new request with feedback
	if req.Feedback != nil && req.Feedback.Comment != "" {
		feedbackComment := &models.IncidentComment{
			IncidentID: newRequest.ID,
			Content:    req.Feedback.Comment,
			AuthorID:   userID,
		}
		if err := s.incidentRepo.CreateComment(ctx, feedbackComment); err != nil {
			fmt.Printf("Warning: failed to create feedback comment on new request: %v\n", err)
		}

		// Also create comment on source incident
		sourceFeedbackComment := &models.IncidentComment{
			IncidentID: incidentID,
			Content:    req.Feedback.Comment,
			AuthorID:   userID,
		}
		if err := s.incidentRepo.CreateComment(ctx, sourceFeedbackComment); err != nil {
			fmt.Printf("Warning: failed to create feedback comment on source incident: %v\n", err)
		}
	}

	// Send SMS to citizen
	bgCtxNew := context.WithValue(context.Background(), constants.ContextKeys.ACCEPT_LANGUAGE, ctx.Value(constants.ContextKeys.ACCEPT_LANGUAGE))
	go func(inc *models.Incident) {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("CONVERT-TO-REQUEST-SMS: Panic recovered for incident %s: %v", inc.IncidentNumber, r)
			}
		}()
		s.sendConvertToRequestSMS(bgCtxNew, inc, requestNumber, userID)
	}(sourceIncident)

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
		return false, "", err
	}

	// Check if it's already a request
	if sourceIncident.RecordType == "request" {
		return false, i18n.T(ctx, "cannot_convert_request"), nil
	}

	// Check if it has already been converted
	if sourceIncident.ConvertedRequestID != nil {
		return false, i18n.T(ctx, "already_converted_to_request"), nil
	}

	// Only allow conversion while the incident is in the configured state (e.g. "Under Resolution")
	if !s.isInConvertibleState(sourceIncident) {
		return false, i18n.T(ctx, "convert_requires_resolution_state"), nil
	}

	// Get the workflow with ConvertToRequestRoles
	workflow, err := s.workflowRepo.FindByIDWithRelations(ctx, sourceIncident.WorkflowID)
	if err != nil {
		return false, "", errors.New(i18n.T(ctx, "workflow_not_found"))
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

	return false, i18n.T(ctx, "no_permission_convert"), nil
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
			return nil, errors.New(i18n.T(ctx, "feedback_required_bulk"))
		}
	}

	// Parse common fields
	workflowID, err := uuid.Parse(req.WorkflowID)
	if err != nil {
		return nil, errors.New(i18n.T(ctx, "invalid_workflow_id_lower"))
	}

	classificationID, err := uuid.Parse(req.ClassificationID)
	if err != nil {
		return nil, errors.New(i18n.T(ctx, "invalid_classification_id"))
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
			return nil, errors.New(i18n.T(ctx, "invalid_existing_request_id"))
		}
		existingRequest, err = s.incidentRepo.FindByIDWithRelations(ctx, id)
		if err != nil {
			return nil, errors.New(i18n.T(ctx, "existing_request_not_found"))
		}
		if existingRequest.RecordType != "request" {
			return nil, errors.New(i18n.T(ctx, "request_id_not_incident"))
		}
		existingRequestID = &id
	}

	// Get the initial state of the request workflow (only needed if creating new request)
	var initialState *models.WorkflowState
	if existingRequestID == nil {
		initialState, err = s.workflowRepo.GetInitialState(ctx, workflowID)
		if err != nil {
			return nil, errors.New(i18n.T(ctx, "workflow_no_initial_state"))
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
			errMsg := i18n.T(ctx, "incident_not_found")
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
			errMsg := i18n.T(ctx, "workflow_not_found")
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
				errMsg := i18n.T(ctx, "no_permission_convert")
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
		if len(existingSourceIDs) == 0 && existingRequest.SourceIncidentID != nil {
			existingSourceIDs = []string{existingRequest.SourceIncidentID.String()}
		}
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
		if slaDur := initialState.SLADuration(); slaDur > 0 {
			deadline := time.Now().Add(slaDur)
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
		if existingRequestID != nil {
			// Existing request: append so we don't wipe lookup values from previous conversions
			if err := s.incidentRepo.AppendLookupValues(ctx, newRequest.ID, lookupValues); err != nil {
				fmt.Printf("Warning: failed to append lookup values to existing request: %v\n", err)
			}
		} else {
			// New request: replace is fine since there are no pre-existing values
			if err := s.incidentRepo.SetLookupValues(ctx, newRequest.ID, lookupValues); err != nil {
				fmt.Printf("Warning: failed to copy lookup values: %v\n", err)
			}
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

				// Also create comment on source incident
				sourceFeedbackComment := &models.IncidentComment{
					IncidentID: sourceIncident.ID,
					Content:    feedback.Comment,
					AuthorID:   userID,
				}
				if err := s.incidentRepo.CreateComment(ctx, sourceFeedbackComment); err != nil {
					fmt.Printf("Warning: failed to create feedback comment on source incident %s: %v\n", sourceIncident.IncidentNumber, err)
				}
			}
			feedbackDesc := fmt.Sprintf("Feedback provided during conversion to request %s", requestNumber)
			_ = s.CreateRevision(ctx, sourceIncident.ID, models.RevisionActionFieldChange, feedbackDesc, feedbackChanges, userID)
		}

		// Send SMS to citizen for each converted incident
		bgCtxBulk := context.WithValue(context.Background(), constants.ContextKeys.ACCEPT_LANGUAGE, ctx.Value(constants.ContextKeys.ACCEPT_LANGUAGE))
		bulkReqNum := requestNumber
		go func(inc *models.Incident) {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("CONVERT-TO-REQUEST-SMS: Panic recovered for incident %s: %v", inc.IncidentNumber, r)
				}
			}()
			s.sendConvertToRequestSMS(bgCtxBulk, inc, bulkReqNum, userID)
		}(sourceIncident)
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

	// Create comment on request with feedback if provided
	if req.Feedback != nil && req.Feedback.Comment != "" {
		feedbackComment := &models.IncidentComment{
			IncidentID: newRequest.ID,
			Content:    req.Feedback.Comment,
			AuthorID:   userID,
		}
		if err := s.incidentRepo.CreateComment(ctx, feedbackComment); err != nil {
			fmt.Printf("Warning: failed to create feedback comment on bulk convert request: %v\n", err)
		}
	}

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
		return nil, errors.New(i18n.T(ctx, "incident_locked"))
	}

	// Capture ALL pre-transition assignee IDs INSIDE the transaction BEFORE SetAssignees modifies the table.
	// This is the only reliable way to get the true previous multi-assignee list.
	var preTxAssigneeIDs []uuid.UUID
	if incident.AssigneeID != nil && *incident.AssigneeID != uuid.Nil {
		preTxAssigneeIDs = append(preTxAssigneeIDs, *incident.AssigneeID)
	}
	var multiIDs []uuid.UUID
	tx.Table("incident_assignees").Where("incident_id = ?", incidentID).Pluck("user_id", &multiIDs)
	seen := make(map[uuid.UUID]bool)
	for _, uid := range preTxAssigneeIDs {
		seen[uid] = true
	}
	for _, uid := range multiIDs {
		if uid != uuid.Nil && !seen[uid] {
			seen[uid] = true
			preTxAssigneeIDs = append(preTxAssigneeIDs, uid)
		}
	}

	// BLOCK: Prevent manual transitions on child incidents (merged into another)
	if incident.IsMerged && incident.MasterIncidentID != nil {
		tx.Rollback()
		return nil, errors.New(i18n.T(ctx, "child_incidents_no_manual_transition"))
	}

	// Verify version still matches (double-check optimistic lock)
	if incident.Version != req.Version {
		tx.Rollback()
		return nil, fmt.Errorf("%s", i18n.T(ctx, "incident_conflict"))
	}

	// Parse transition ID
	transitionID, err := uuid.Parse(req.TransitionID)
	if err != nil {
		tx.Rollback()
		return nil, errors.New(i18n.T(ctx, "invalid_transition_id_svc"))
	}
	// Get the transition with relations
	transition, err := s.workflowRepo.FindTransitionByIDWithRelations(ctx, transitionID)
	if err != nil {
		return nil, errors.New(i18n.T(ctx, "transition_not_found"))
	}

	// Verify the transition belongs to this workflow
	if transition.WorkflowID != incident.WorkflowID {
		tx.Rollback()
		return nil, errors.New(i18n.T(ctx, "transition_not_in_workflow"))
	}

	// Verify the transition starts from the current state
	if transition.FromStateID != incident.CurrentStateID {
		tx.Rollback()
		return nil, errors.New(i18n.T(ctx, "transition_not_from_current"))
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
			return nil, errors.New(i18n.T(ctx, "no_permission_transition"))
		}
	}

	// Check assignee requirement
	if transition.RequireAssignee {
		isAssignee := incident.AssigneeID != nil && *incident.AssigneeID == userID
		if !isAssignee {
			var count int64
			tx.Table("incident_assignees").
				Where("incident_id = ? AND user_id = ?", incidentID, userID).
				Count(&count)
			isAssignee = count > 0
		}
		if !isAssignee {
			tx.Rollback()
			return nil, errors.New(i18n.T(ctx, "only_assigned_user_transition"))
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
			if (requirement.IsMultiple == nil || !*requirement.IsMultiple) && len(req.Attachments) > 1 {
				tx.Rollback()
				return nil, errors.New("Only one attachment is allowed for this transition")
			}
		case "feedback":
			if req.Feedback == nil {
				errMsg := requirement.ErrorMessage
				if errMsg == "" {
					errMsg = "Feedback is required for this transition"
				}
				tx.Rollback()
				return nil, errors.New(errMsg)
			}
		}
	}

	// Sub-workflow push/pop. A transition that sets neither field keeps the
	// existing destination (transition.ToStateID) and does not touch the stack.
	fromStateID := incident.CurrentStateID
	destStateID := transition.ToStateID
	subworkflowMove := false
	if transition.TargetWorkflowID != nil || transition.IsReturnTransition {
		var initialStates []models.WorkflowState
		if transition.TargetWorkflowID != nil {
			states, err := s.workflowRepo.ListStatesByWorkflowID(ctx, *transition.TargetWorkflowID)
			if err != nil {
				tx.Rollback()
				return nil, err
			}
			for _, st := range states {
				if st.StateType == "initial" {
					initialStates = append(initialStates, st)
				}
			}
		}
		dest, moved, err := applySubworkflowTransition(incident, transition, initialStates)
		if err != nil {
			tx.Rollback()
			return nil, err
		}
		destStateID = dest
		subworkflowMove = moved
	}

	// Create transition history record
	history := &models.IncidentTransitionHistory{
		IncidentID:     incidentID,
		TransitionID:   &transitionID,
		FromStateID:    fromStateID,
		ToStateID:      destStateID,
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
	if req.Feedback != nil {
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
	newState, err := s.workflowRepo.FindStateByID(ctx, destStateID)
	if err != nil {
		tx.Rollback()
		return nil, errors.New(i18n.T(ctx, "target_state_not_found"))
	}

	// Validate duration when transitioning INTO a partial_close state
	if newState.IsPartialClose {
		if req.ReadyToCloseDuration == "" {
			tx.Rollback()
			return nil, errors.New(i18n.T(ctx, "partial_close_duration_required"))
		}
		if s.readyToCloseService != nil {
			if _, err := s.readyToCloseService.ParseDuration(req.ReadyToCloseDuration); err != nil {
				tx.Rollback()
				return nil, errors.New(i18n.T(ctx, "partial_close_duration_invalid"))
			}
		}
	}

	// Prepare updates map for all fields that need to change
	updates := map[string]interface{}{
		"current_state_id": destStateID,
		"updated_at":       time.Now(),
	}
	if subworkflowMove {
		updates["workflow_id"] = incident.WorkflowID
		updates["workflow_stack"] = incident.WorkflowStack
	}

	// When leaving a partial_close state, clear partial close fields
	if transition.FromState != nil && transition.FromState.IsPartialClose {
		updates["partial_close_expires_at"] = nil
		updates["partial_close_duration"] = ""
		updates["partial_close_notified"] = false
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

		// For a MOMRA-sourced incident, auto-detect candidates must include this
		// incident's own EEList-resolved departments (Incident.AvailableEEList), not
		// just classification/location linkage — MOMRA can declare an EE eligible for
		// a specific incident with no general classification link at all. Without
		// this merge, a classification-linked EE and an EEList-only EE would produce
		// len(matchedDepts)==1 (only the classification-linked one visible here),
		// causing the "single match — auto-assign" branch below to silently override
		// whatever the user actually selected in the frontend picker — which already
		// shows both, since department_handler.go's MatchDepartment does this same
		// merge. Kept in sync with that handler rather than sharing code directly,
		// since handlers and services don't depend on each other.
		wantsExternalMatch := deptTypeFilter == nil || *deptTypeFilter == externalEntityDepartmentType
		if incident.Source == "MOMRA" && wantsExternalMatch && len(incident.AvailableEEList) > 0 {
			eeDepartments := s.resolveIncidentEEDepartments(ctx, incident)
			merged := make([]models.Department, 0, len(matchedDepts)+len(eeDepartments))
			for _, d := range matchedDepts {
				if d.Type != externalEntityDepartmentType {
					merged = append(merged, d)
				}
			}
			seen := make(map[uuid.UUID]bool, len(eeDepartments))
			for _, d := range eeDepartments {
				if !seen[d.ID] {
					seen[d.ID] = true
					merged = append(merged, d)
				}
			}
			matchedDepts = merged
		}

		if len(matchedDepts) == 1 {
			// Single match — auto-assign
			updates["department_id"] = matchedDepts[0].ID
		} else if len(matchedDepts) > 1 {
			// Multiple matches — user must have selected one
			if req.DepartmentID == nil || *req.DepartmentID == "" {
				tx.Rollback()
				return nil, errors.New(i18n.T(ctx, "department_required_transition"))
			}
			deptID, err := uuid.Parse(*req.DepartmentID)
			if err != nil {
				tx.Rollback()
				return nil, errors.New(i18n.T(ctx, "invalid_department_id"))
			}
			isMatched := false
			for _, d := range matchedDepts {
				if d.ID == deptID {
					isMatched = true
					break
				}
			}
			if !isMatched {
				tx.Rollback()
				return nil, errors.New(i18n.T(ctx, "invalid_department_id"))
			}
			updates["department_id"] = deptID
		}
		// If no departments match, keep current department (graceful fallback)
	}

	// For a MOMRA-sourced incident, an external-type department assigned here (from
	// any of the three branches above) must be one of the External Entities MOMRA
	// declared eligible for THIS specific incident at submission time — see
	// validateExternalDepartmentAssignment. Checked once here rather than in each
	// branch since all three write the same "department_id" key into updates.
	if deptID, ok := updates["department_id"].(uuid.UUID); ok {
		if err := s.validateExternalDepartmentAssignment(ctx, incident, deptID); err != nil {
			tx.Rollback()
			return nil, err
		}
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
				return nil, errors.New(i18n.T(ctx, "user_required_transition"))
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
					return nil, errors.New(i18n.T(ctx, "user_not_in_scope"))
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
	if slaDur := newState.SLADuration(); slaDur > 0 {
		deadline := time.Now().Add(slaDur)
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
	var transitionLookupChanges []models.IncidentFieldChange
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
					if err := s.validateExternalDepartmentAssignment(ctx, incident, id); err != nil {
						tx.Rollback()
						return nil, err
					}
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
			default:
				if !strings.HasPrefix(fieldName, "lookup:") {
					continue
				}
				categoryCode := strings.TrimPrefix(fieldName, "lookup:")
				if categoryCode == "" {
					continue
				}
				// Resolve the category by code
				var category models.LookupCategory
				if err := s.db.WithContext(ctx).
					Where("code = ? AND is_active = ?", categoryCode, true).
					First(&category).Error; err != nil {
					fmt.Printf("Warning: lookup category not found for code %s: %v\n", categoryCode, err)
					continue
				}
				// Find or create the lookup value by code within that category
				code := fieldValue
				if len(code) > 50 {
					code = code[:50]
				}
				name := fieldValue
				if len(name) > 200 {
					name = name[:200]
				}
				var lookupVal models.LookupValue
				err := tx.WithContext(ctx).
					Where("category_id = ? AND code = ?", category.ID, code).
					First(&lookupVal).Error
				if err != nil {
					lookupVal = models.LookupValue{
						CategoryID: category.ID,
						Code:       code,
						Name:       name,
						// Description: fieldValue,
						IsActive: true,
					}

					if createErr := tx.WithContext(ctx).Create(&lookupVal).Error; createErr != nil {
						fmt.Printf("Warning: failed to create lookup value for category %s code %s: %v\n", categoryCode, fieldValue, createErr)
						continue
					}
				}
				// Replace any existing value from this category on the incident, then append the new one
				if err := tx.WithContext(ctx).Exec(
					"DELETE FROM incident_lookup_values WHERE incident_id = ? AND lookup_value_id IN (SELECT id FROM lookup_values WHERE category_id = ?)",
					incidentID, category.ID,
				).Error; err != nil {
					fmt.Printf("Warning: failed to clear old lookup values for category %s: %v\n", categoryCode, err)
				}
				incRef := models.Incident{}
				incRef.ID = incidentID
				if err := tx.WithContext(ctx).Model(&incRef).Association("LookupValues").Append(&lookupVal); err != nil {
					fmt.Printf("Warning: failed to append lookup value for category %s: %v\n", categoryCode, err)
				} else {
					transitionLookupChanges = append(transitionLookupChanges, models.IncidentFieldChange{
						FieldName:  fieldName,
						FieldLabel: category.Name,
						NewValue:   &lookupVal.Name,
					})
				}
			}
		}
	}

	// Apply all updates using optimistic locking with version
	if err := txRepo.UpdateFieldsWithVersion(ctx, incidentID, updates, req.Version); err != nil {
		tx.Rollback()
		if err == repository.ErrVersionMismatch {
			return nil, fmt.Errorf("%s", i18n.T(ctx, "incident_conflict"))
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

	// If the destination state is an AI-QA state, reset so the monitor re-triggers:
	// - is_ai_verified = false on the incident (monitor only picks up false)
	// - is_reopened = false on the feedback record (button shows "Reopen" again)
	// The feedback record itself is kept so the incident stays visible in the QA list.
	// The monitor's Create uses upsert, so it overwrites the stale audit data in place.
	// If the destination state is an AI-QA state, reset so the monitor re-triggers:
	// - is_ai_verified = false on the incident (monitor only picks up false)
	// - clear stale QA result fields so the UI shows the ticket is pending re-evaluation
	// - is_reopened = false so the "Reopen" button reappears after re-evaluation
	// raw_response is intentionally preserved until the new evaluation overwrites it.
	// The feedback record itself is kept so the incident stays visible in the QA list.
	if newState.IsAIQA {
		var existingFeedback models.AIQualityFeedback
		var setIsReopened bool
		err := tx.WithContext(ctx).
			Where("incident_id = ?", incidentID).
			First(&existingFeedback).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			fmt.Printf("Warning: failed to query existing AI quality feedback: %v\n", err)
		}
		if existingFeedback.ID != uuid.Nil {
			setIsReopened = true
		}

		if err := tx.WithContext(ctx).
			Model(&models.Incident{}).
			Where("id = ?", incidentID).
			Update("is_ai_verified", false).Error; err != nil {
			fmt.Printf("Warning: failed to reset is_ai_verified for AI-QA state: %v\n", err)
		}
		if err := tx.WithContext(ctx).
			Model(&models.AIQualityFeedback{}).
			Where("incident_id = ?", incidentID).
			Updates(map[string]interface{}{
				"is_reopened":       setIsReopened, // Only set to true if there was an existing feedback record (i.e. this is not the first time entering an AI-QA state)
				"changed_summary":   "",
				"resolution_status": "",
				"distance_meters":   0,
			}).Error; err != nil {
			fmt.Printf("Warning: failed to reset ai_quality_feedback fields for AI-QA state: %v\n", err)
		}
	}

	// If this is a reopen transition and the incident has an AI quality feedback record,
	// mark it as reopened so the QA page reflects the action regardless of which
	// surface (incident details or QA page) triggered the reopen.
	if transition.IsReopen {
		if err := tx.WithContext(ctx).
			Model(&models.AIQualityFeedback{}).
			Where("incident_id = ?", incidentID).
			Update("is_reopened", true).Error; err != nil {
			fmt.Printf("Warning: failed to set is_reopened on reopen transition: %v\n", err)
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
	changes = append(changes, transitionLookupChanges...)
	revDescription := fmt.Sprintf("Status changed from %s to %s", oldStateName, newStateName)
	for _, lc := range transitionLookupChanges {
		val := ""
		if lc.NewValue != nil {
			val = *lc.NewValue
		}
		revDescription += fmt.Sprintf("; %s: %s", lc.FieldLabel, val)
	}
	if len(transitionLookupChanges) > 0 {
		historyComment := revDescription
		if history.Comment != "" {
			historyComment = history.Comment + " | " + revDescription
		}
		tx.WithContext(ctx).Model(history).Update("comment", historyComment)
		// Do NOT mutate history.Comment — syncTransitionToMergedIncidents uses it
		// to propagate only the user-typed comment to child incidents.
	}
	revNum, _ := txRepo.GetNextRevisionNumber(ctx, incidentID)
	changesBytes, _ := json.Marshal(changes)

	// Get merged incident numbers to include in revision (for tracking child tickets)
	var syncedIncidentNumbers []string
	mergedIncidents, _ := s.incidentMergeRepo.GetMergedIncidents(ctx, incidentID)
	for _, merged := range mergedIncidents {
		syncedIncidentNumbers = append(syncedIncidentNumbers, merged.IncidentNumber)
	}
	syncedNumbersJSON := ""
	if len(syncedIncidentNumbers) > 0 {
		if numsBytes, err := json.Marshal(syncedIncidentNumbers); err == nil {
			syncedNumbersJSON = string(numsBytes)
		}
	}

	txRepo.CreateRevision(ctx, &models.IncidentRevision{
		IncidentID:            incidentID,
		RevisionNumber:        revNum,
		ActionType:            models.RevisionActionStatusChanged,
		ActionDescription:     revDescription,
		Changes:               string(changesBytes),
		PerformedByID:         userID,
		TransitionHistoryID:   &history.ID,
		SyncedIncidentNumbers: syncedNumbersJSON,
		CreatedAt:             time.Now(),
	})

	// Create revision entry for partial_close duration selection
	if newState.IsPartialClose && req.ReadyToCloseDuration != "" {
		pcDescription := fmt.Sprintf(
			"Status changed from %s to %s — Partial Close Duration: %s",
			transition.FromState.Name, newState.Name, req.ReadyToCloseDuration,
		)
		if req.Comment != "" {
			pcDescription += fmt.Sprintf("; Comment: %s", req.Comment)
		}
		pcRevNum, _ := txRepo.GetNextRevisionNumber(ctx, incidentID)
		pcChangesBytes, _ := json.Marshal([]models.IncidentFieldChange{
			{FieldName: "partial_close_duration", FieldLabel: "Partial Close Duration", OldValue: nil, NewValue: &req.ReadyToCloseDuration},
		})
		txRepo.CreateRevision(ctx, &models.IncidentRevision{
			IncidentID:            incidentID,
			RevisionNumber:        pcRevNum,
			ActionType:            models.RevisionActionStatusChanged,
			ActionDescription:     pcDescription,
			Changes:               string(pcChangesBytes),
			PerformedByID:         userID,
			SyncedIncidentNumbers: syncedNumbersJSON,
			CreatedAt:             time.Now(),
		})
	}

	// Commit transaction first so all master incident changes are visible
	if err := tx.Commit().Error; err != nil {
		return nil, err
	}

	// Fire integration triggers AFTER commit so the new state is visible.
	if s.integrationExecutor != nil {
		updatedForExec, execErr := s.incidentRepo.FindByIDWithRelations(ctx, incidentID)
		if execErr == nil {
			// Transition triggers
			s.integrationExecutor.RunTransitionTriggers(ctx, updatedForExec, transitionID, transition.Name)
			// State-enter triggers on the destination state
			s.integrationExecutor.RunStateTriggers(ctx, updatedForExec, destStateID, newState.Name, "enter")
			// State-exit triggers on the source state
			s.integrationExecutor.RunStateTriggers(ctx, updatedForExec, transition.FromStateID, transition.FromState.Name, "exit")

			// MOMRA outbound status sync (docs/MOMRA_Outbound_Integration_Spec_v1.0.md
			// §3 Story B) — async so an outbound MOMRA call/retry never adds latency to
			// this request. No-ops internally if no mapping exists for the new state.
			// operatorName/operatorID/eeNotesFromMUN (TFIS v1.0 §11.3) are resolved
			// synchronously here — before the goroutine, which uses context.Background()
			// and so can't read the request-scoped ctx/req itself — then closed over.
			if s.momraStatusSyncService != nil {
				var operatorName, operatorID string
				if actor, actorErr := s.userRepo.FindByID(ctx, userID); actorErr == nil && actor != nil {
					operatorName = strings.TrimSpace(actor.FirstName + " " + actor.LastName)
					if operatorName == "" {
						operatorName = actor.Username
					}
					operatorID = actor.Username
				}
				eeNotesFromMUN := req.Comment
				go s.momraStatusSyncService.SyncIncidentStatus(context.Background(), updatedForExec, destStateID, operatorName, operatorID, eeNotesFromMUN)
			}
		}
	}

	// Handle Partial-Close entry lifecycle AFTER commit
	if s.readyToCloseService != nil {
		// Deactivate any prior entry when leaving a partial_close state
		if transition.FromState != nil && transition.FromState.IsPartialClose {
			if err := s.readyToCloseService.DeactivateForIncident(ctx, incidentID); err != nil {
				// Non-fatal: log and continue
				fmt.Printf("Warning: failed to deactivate partial_close entry for incident %s: %v\n", incidentID, err)
			}
		}
		// Create new entry when entering partial_close state (sets partial_close_expires_at/duration on incident)
		if newState.IsPartialClose && req.ReadyToCloseDuration != "" {
			if err := s.readyToCloseService.CreateEntry(ctx, incidentID, req.ReadyToCloseDuration, req.Comment, userID); err != nil {
				// Non-fatal: log and continue — transition already committed
				fmt.Printf("Warning: failed to create partial_close entry for incident %s: %v\n", incidentID, err)
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
					bgCtx := context.WithValue(context.Background(), constants.ContextKeys.ACCEPT_LANGUAGE, ctx.Value(constants.ContextKeys.ACCEPT_LANGUAGE))
					fmt.Println("[DEBUG] Starting goroutine: autoCloseMergedIncidents")
					go func() {
						_ = s.autoCloseMergedIncidents(bgCtx, incidentID, req, userID)
					}()
				} else {
					fmt.Println("[DEBUG] Non-terminal state - syncing status and sending SMS")
					// Non-terminal state: sync the status and send SMS notifications
					_ = s.incidentMergeRepo.SyncStatusToMergedIncidents(ctx, incidentID, destStateID)

					// Sync transition data (revision, history, comment) to children
					_ = s.syncTransitionToMergedIncidents(ctx, incidentID, transition, history, userID)

					// Send SMS notifications in background
					bgCtx := context.WithValue(context.Background(), constants.ContextKeys.ACCEPT_LANGUAGE, ctx.Value(constants.ContextKeys.ACCEPT_LANGUAGE))
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
			subjectAr, bodyAr := IncidentAssignedTransitionTextsAr(incident.IncidentNumber, incident.Title, newStateName)

			if result, err := s.notificationService.SendNotification(
				ctx, "notification", nil, "en",
				assigneeEmails, nil, nil,
				subject, body,
				nil, nil, &userID, nil,
			); err == nil && len(result.InboxLogIDs) > 0 {
				_ = s.notificationService.SetMetaOnLogs(ctx, result.InboxLogIDs, &models.NotificationMeta{
					ID:   incidentID.String(),
					Type: strings.ToUpper(incident.RecordType),
				})
				_ = s.notificationService.SetArContentOnLogs(ctx, result.InboxLogIDs, subjectAr, bodyAr)
			}
		}
	}
	// Send FCM push notification to next assignee(s) only on the "approve" transition
	if s.fcmService != nil && transition.Code == "approve" && len(assigneeUserIDs) > 0 {
		log.Printf("FCM-ASSIGN: assignee user %s:, transition_code: %s", assigneeUserIDs, transition.Code)
		bgCtx := context.WithValue(context.Background(), constants.ContextKeys.ACCEPT_LANGUAGE, ctx.Value(constants.ContextKeys.ACCEPT_LANGUAGE))
		capturedID := incidentID
		capturedNumber := incident.IncidentNumber
		capturedTitle := incident.Title
		capturedType := strings.ToUpper(incident.RecordType)
		capturedState := newStateName
		capturedAssignees := append([]uuid.UUID{}, assigneeUserIDs...)
		go func() {
			for _, assigneeID := range capturedAssignees {
				pushReq := &models.PushRequest{
					UserID: assigneeID,
					Title:  fmt.Sprintf("New %s Assigned: %s", capturedType, capturedNumber),
					Body:   fmt.Sprintf("Incident \"%s\" has been assigned to you. Status: %s.", capturedTitle, capturedState),
					Data: map[string]string{
						"id":   capturedID.String(),
						"type": capturedType,
					},
				}
				if err := s.fcmService.Push(bgCtx, pushReq); err != nil {
					log.Printf("FCM-ASSIGN: Failed for user %s: %v | pushReq: %+v", assigneeID, err, pushReq)
				} else {
					log.Printf("FCM-ASSIGN: Sent to user %s | pushReq: %+v", assigneeID, pushReq)
				}
			}
		}()
	}

	// Send FCM push notification + in-app notification to the citizen reporter on incident closure
	if (s.fcmService != nil || s.notificationService != nil) && newState.StateType == "terminal" && incident.ReporterID != nil {
		bgCtx := context.WithValue(context.Background(), constants.ContextKeys.ACCEPT_LANGUAGE, ctx.Value(constants.ContextKeys.ACCEPT_LANGUAGE))
		reporterID := *incident.ReporterID
		closedAt := time.Now()
		comment := req.Comment
		incidentNumber := incident.IncidentNumber
		incidentTitle := incident.Title
		recordType := strings.ToUpper(incident.RecordType)
		capturedID := incidentID
		reporterEmail := incident.ReporterEmail
		capturedByUser := userID
		go func() {
			// Only send to citizens — skip internal staff
			roles, err := s.userRepo.GetUserRoles(bgCtx, reporterID)
			if err != nil {
				log.Printf("FCM-CLOSURE: Could not fetch roles for reporter %s: %v", reporterID, err)
				return
			}
			isCitizen := false
			for _, r := range roles {
				if r.Code == constants.USER_ROLE.CITIZEN {
					isCitizen = true
					break
				}
			}
			if !isCitizen {
				log.Printf("FCM-CLOSURE: Reporter %s is not a citizen, skipping", reporterID)
				return
			}

			subject := fmt.Sprintf("Your Incident %s Has Been Closed", incidentNumber)
			body := fmt.Sprintf(
				"Your incident \"%s\" (ID: %s) has been closed on %s.",
				incidentTitle, incidentNumber, closedAt.Format("02 Jan 2006"),
			)
			if comment != "" {
				body += fmt.Sprintf(" Comment: %s", comment)
			}

			// Send in-app notification
			if s.notificationService != nil {
				email := reporterEmail
				if email == "" {
					if u, ferr := s.userRepo.FindByID(bgCtx, reporterID); ferr == nil {
						email = u.Email
					}
				}
				if email != "" {
					if result, nerr := s.notificationService.SendNotification(
						bgCtx, "notification", nil, "en",
						[]string{email}, nil, nil,
						subject, body,
						nil, nil, &capturedByUser, nil,
					); nerr == nil && len(result.InboxLogIDs) > 0 {
						_ = s.notificationService.SetMetaOnLogs(bgCtx, result.InboxLogIDs, &models.NotificationMeta{
							ID:   capturedID.String(),
							Type: recordType,
						})
					} else if nerr != nil {
						log.Printf("INAPP-CLOSURE: Failed for citizen %s: %v", reporterID, nerr)
					}
				}
			}

			// Send FCM push notification
			if s.fcmService != nil {
				pushReq := &models.PushRequest{
					UserID: reporterID,
					Title:  "Your Incident Has Been Closed",
					Body:   body,
					Data: map[string]string{
						"id":        capturedID.String(),
						"type":      recordType,
						"closed_at": closedAt.Format(time.RFC3339),
						"comment":   comment,
					},
				}
				log.Printf("FCM-CLOSURE: Sending push to citizen %s | title: %q | body: %q | data: %v",
					reporterID, pushReq.Title, pushReq.Body, pushReq.Data)
				if err := s.fcmService.Push(bgCtx, pushReq); err != nil {
					log.Printf("FCM-CLOSURE: Failed for citizen %s: %v", reporterID, err)
				} else {
					log.Printf("FCM-CLOSURE: Sent successfully to citizen %s", reporterID)
				}
			}
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
		bgCtx := context.WithValue(context.Background(), constants.ContextKeys.ACCEPT_LANGUAGE, ctx.Value(constants.ContextKeys.ACCEPT_LANGUAGE))
		go s.createRejectionLog(bgCtx, incidentID, incident, transition, history, userID, userRoleIDs)
	}

	// Send SMS to citizen when Not Belong transition closes the incident.
	if transition.IsNotBelong {
		log.Printf("NOT-BELONG-SMS: Triggered for incident %s and transition.IsNotBelong: %v", incident.IncidentNumber, transition.IsNotBelong)
		var assignedDeptID *uuid.UUID
		if deptIDVal, ok := updates["department_id"]; ok {
			if deptID, ok := deptIDVal.(uuid.UUID); ok {
				assignedDeptID = &deptID
			}
		}
		bgCtx := context.WithValue(context.Background(), constants.ContextKeys.ACCEPT_LANGUAGE, ctx.Value(constants.ContextKeys.ACCEPT_LANGUAGE))
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("NOT-BELONG-SMS: Panic recovered for incident %s: %v", incident.IncidentNumber, r)
				}
			}()

			log.Printf("NOT-BELONG-SMS: Sending SMS for incident %s", incident.IncidentNumber)
			s.SendNotBelongClosureSMS(bgCtx, incident, incident.CreatedByMobile, incident.ReporterID, transition, assignedDeptID, userID)
			log.Printf("NOT-BELONG-SMS: SMS process completed for incident %s", incident.IncidentNumber)
		}()
	}

	// Send SMS to citizen when Missing Incident Information transition closes the incident.

	if transition.IsMissingInfo {
		log.Printf("MISSING-INFO-SMS: Triggered for incident %s and transition.IsMissingInfo: %v", incident.IncidentNumber, transition.IsMissingInfo)
		bgCtx := context.WithValue(context.Background(), constants.ContextKeys.ACCEPT_LANGUAGE, ctx.Value(constants.ContextKeys.ACCEPT_LANGUAGE))
		//go s.SendMissingInfoClosureSMS(bgCtx, incident.IncidentNumber, incident.CreatedByMobile, incident.ReporterID, userID)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("MISSING-INFO-SMS: Panic recovered for incident %s: %v", incident.IncidentNumber, r)
				}
			}()

			log.Printf("MISSING-INFO-SMS: Sending SMS for incident %s", incident.IncidentNumber)
			s.SendMissingInfoClosureSMS(bgCtx, incident, incident.CreatedByMobile, incident.ReporterID, userID)
			log.Printf("MISSING-INFO-SMS: SMS process completed for incident %s", incident.IncidentNumber)
		}()
	}

	// NOTE: Final Close SMS is handled by the action executor below.
	// Add an SMS automation action on the Final Close transition in the workflow editor;
	// the action executor will send it with the correct recipients and template.

	// Fetch updated incident (outside transaction) with all relations for response and action execution.
	updated, err := s.incidentRepo.FindByIDWithRelations(ctx, incidentID)
	if err != nil {
		return nil, err
	}

	// Use the pre-transaction snapshot captured at the top of ExecuteTransition.
	updated.PreviousAssigneeIDs = preTxAssigneeIDs
	log.Printf("[ExecuteTransition] incident=%s transition=%s — PreviousAssigneeIDs(%d) CurrentAssigneeID=%v",
		incident.IncidentNumber, transition.Name, len(preTxAssigneeIDs), updated.AssigneeID)

	// IsFinalClose: pre-create a pending feedback record so BuildIncidentVariables can call
	// GenerateFeedbackToken with a real feedbackID, producing a direct submit URL in {{feedback_url}}.
	// Also schedule the SMS fallback via SmsFeedbackPending — the SMS is NOT sent immediately;
	// the SLA monitor will send it only if the WhatsApp chatbot receives no response within the delay window.
	if transition.IsFinalClose && s.publicFeedbackRepo != nil {
		mobileNo := updated.ReporterPhone
		if mobileNo == "" && updated.Reporter != nil {
			mobileNo = updated.Reporter.Phone
		}
		f := &models.IncidentPublicFeedback{
			IncidentID: incidentID,
			MobileNo:   mobileNo,
			Source:     "sms",
			CreatedBy:  userID,
		}
		if err := s.publicFeedbackRepo.Create(ctx, f); err == nil {
			updated.FeedbackID = &f.ID
			// Schedule delayed SMS fallback.
			if s.smsFeedbackPendingRepo != nil {
				delayMinutes := s.smsFeedbackDelayMinutes
				closedAt := time.Now()
				pending := &models.SmsFeedbackPending{
					IncidentID:  incidentID,
					FeedbackID:  f.ID,
					MobileNo:    mobileNo,
					ClosedAt:    closedAt,
					ScheduledAt: closedAt.Add(time.Duration(delayMinutes) * time.Minute),
				}
				if err := s.smsFeedbackPendingRepo.Create(ctx, pending); err != nil {
					log.Printf("[SmsFeedback] failed to create pending SMS record for incident %s: %v", incidentID, err)
				} else {
					log.Printf("[SmsFeedback] incident=%s — SMS fallback scheduled at %s (delay=%dm)",
						incidentID, pending.ScheduledAt.Format(time.RFC3339), delayMinutes)
				}
			}
		} else {
			log.Printf("[ExecuteTransition] failed to pre-create feedback record for IsFinalClose incident %s: %v", incidentID, err)
		}
	}

	// Execute automation actions configured on this transition.
	// Uses the fully-preloaded incident so recipient fields (Assignee.Email, Reporter.Email, etc.) are available.
	if s.actionExecutor != nil && len(transition.Actions) > 0 {
		capturedTransition := transition
		capturedIncident := updated
		capturedUserID := userID
		bgCtx := context.WithValue(context.Background(), constants.ContextKeys.ACCEPT_LANGUAGE, ctx.Value(constants.ContextKeys.ACCEPT_LANGUAGE))
		go func() {
			var performer *models.User
			if u, err := s.userRepo.FindByID(bgCtx, capturedUserID); err == nil {
				performer = u
			}
			if err := s.actionExecutor.ExecuteActions(bgCtx, capturedIncident, capturedTransition, performer); err != nil {
				log.Printf("ExecuteActions failed for transition %s: %v", capturedTransition.Name, err)
			}
		}()
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
		mins := int64(transition.FromState.SLADuration().Minutes())
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

func (s *incidentService) GetAvailableTransitions(ctx context.Context, incidentID uuid.UUID, userID uuid.UUID, userRoleIDs []uuid.UUID) ([]models.AvailableTransitionResponse, error) {
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

	// Pre-compute assignee status for require_assignee checks
	isAssignee := incident.AssigneeID != nil && *incident.AssigneeID == userID
	if !isAssignee {
		var count int64
		s.db.Table("incident_assignees").
			Where("incident_id = ? AND user_id = ?", incidentID, userID).
			Count(&count)
		isAssignee = count > 0
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

		// Check assignee requirement
		if canExecute && trans.RequireAssignee && !isAssignee {
			canExecute = false
			reason = "Only the assigned user can perform this transition"
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

	// If this is a request with a source incident, also show comments from the source incident
	if incident.SourceIncidentID != nil {
		sourceComments, err := s.incidentRepo.ListComments(ctx, *incident.SourceIncidentID)
		if err == nil && len(sourceComments) > 0 {
			// Build signature set for existing comments to avoid duplicates
			existingSignatures := make(map[string]bool)
			for _, c := range comments {
				sig := fmt.Sprintf("%s|%s", c.Content, c.AuthorID)
				existingSignatures[sig] = true
			}

			// Only add source incident comments that don't already exist
			for _, sc := range sourceComments {
				sig := fmt.Sprintf("%s|%s", sc.Content, sc.AuthorID)
				if !existingSignatures[sig] {
					comments = append(comments, sc)
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
		return nil, errors.New(i18n.T(ctx, "comment_not_found"))
	}

	// Only author can update their comment
	if comment.AuthorID != userID {
		return nil, errors.New(i18n.T(ctx, "only_edit_own_comments"))
	}

	oldContent := comment.Content
	incidentID := comment.IncidentID

	comment.Content = req.Content
	comment.IsInternal = req.IsInternal

	if err := s.incidentRepo.UpdateComment(ctx, comment); err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.T(ctx, "failed_to_update_comment"), err)
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
		return errors.New(i18n.T(ctx, "comment_not_found"))
	}

	// Only author can delete their comment
	if comment.AuthorID != userID {
		return errors.New(i18n.T(ctx, "only_delete_own_comments"))
	}

	incidentID := comment.IncidentID
	oldContent := comment.Content

	if err := s.incidentRepo.DeleteComment(ctx, commentID); err != nil {
		return fmt.Errorf("%s: %w", i18n.T(ctx, "failed_to_delete_comment"), err)
	}

	// Create revision for comment deleted
	description := fmt.Sprintf("Comment deleted - %s", truncateString(oldContent, 50))
	_ = s.CreateRevision(ctx, incidentID, models.RevisionActionCommentDeleted, description, nil, userID)

	return nil
}

// Feedback

func (s *incidentService) ListFeedbacks(ctx context.Context, incidentID uuid.UUID) ([]models.IncidentFeedbackResponse, error) {
	feedbackList, err := s.incidentRepo.ListFeedback(ctx, incidentID)
	if err != nil {
		return nil, err
	}

	responses := make([]models.IncidentFeedbackResponse, len(feedbackList))
	for i, f := range feedbackList {
		responses[i] = models.ToIncidentFeedbackResponse(&f)
	}

	return responses, nil
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
			"Attachment added to master incident %s - %s",
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
		return errors.New(i18n.T(ctx, "attachment_not_found"))
	}

	// Only uploader can delete their attachment
	if attachment.UploadedByID != userID {
		return errors.New(i18n.T(ctx, "only_delete_own_attachments"))
	}

	incidentID := attachment.IncidentID
	fileName := attachment.FileName

	// TODO: Delete file from storage

	if err := s.incidentRepo.DeleteAttachment(ctx, attachmentID); err != nil {
		return fmt.Errorf("%s: %w", i18n.T(ctx, "failed_to_delete_attachment"), err)
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

	// Send in-app + FCM push notification to the new assignee
	if assigneeID != uuid.Nil {
		if s.notificationService != nil {
			if assigneeUser, err := s.userRepo.FindByID(ctx, assigneeID); err == nil && assigneeUser.Email != "" {
				subject := fmt.Sprintf("Incident %s assigned to you", updated.IncidentNumber)
				body := fmt.Sprintf("Incident \"%s\" has been assigned to you.", updated.Title)
				subjectAr, bodyAr := IncidentAssignedDirectTextsAr(updated.IncidentNumber, updated.Title)

				if result, err := s.notificationService.SendNotification(
					ctx, "notification", nil, "en",
					[]string{assigneeUser.Email}, nil, nil,
					subject, body,
					nil, nil, &userID, nil,
				); err == nil && len(result.InboxLogIDs) > 0 {
					_ = s.notificationService.SetMetaOnLogs(ctx, result.InboxLogIDs, &models.NotificationMeta{
						ID:   incidentID.String(),
						Type: strings.ToUpper(updated.RecordType),
					})
					_ = s.notificationService.SetArContentOnLogs(ctx, result.InboxLogIDs, subjectAr, bodyAr)
				}
			}
		}
		if s.fcmService != nil {
			bgCtx := context.WithValue(context.Background(), constants.ContextKeys.ACCEPT_LANGUAGE, ctx.Value(constants.ContextKeys.ACCEPT_LANGUAGE))
			capturedAssignee := assigneeID
			capturedID := incidentID
			capturedNumber := updated.IncidentNumber
			capturedTitle := updated.Title
			capturedType := strings.ToUpper(updated.RecordType)
			go func() {
				pushReq := &models.PushRequest{
					UserID: capturedAssignee,
					Title:  fmt.Sprintf("New %s Assigned: %s", capturedType, capturedNumber),
					Body:   fmt.Sprintf("Incident \"%s\" has been assigned to you.", capturedTitle),
					Data: map[string]string{
						"id":   capturedID.String(),
						"type": capturedType,
					},
				}
				if err := s.fcmService.Push(bgCtx, pushReq); err != nil {
					log.Printf("FCM-REASSIGN: Failed for user %s: %v | pushReq: %+v", capturedAssignee, err, pushReq)
				} else {
					log.Printf("FCM-REASSIGN: Sent to user %s | pushReq: %+v", capturedAssignee, pushReq)
				}
			}()
		}
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
			"Master incident %s assignee changed to %s",
			masterIncident.IncidentNumber,
			newAssigneeName,
		)

		if revErr := s.CreateRevision(ctx, merged.ID, models.RevisionActionAssigneeChanged, description, changes, userID); revErr != nil {
			fmt.Printf("[DEBUG] Failed to create revision for child %s: %v\n", merged.IncidentNumber, revErr)
		}
	}

	return nil
}

// SyncLocationToMergedIncidents propagates a master incident's location change to all
// child (merged) incidents so they stay consistent with the master.
func (s *incidentService) SyncLocationToMergedIncidents(ctx context.Context, masterIncidentID uuid.UUID, newLocationID *uuid.UUID, newLocationName string, userID uuid.UUID) error {
	mergedIncidents, err := s.incidentMergeRepo.GetMergedIncidents(ctx, masterIncidentID)
	if err != nil {
		return err
	}
	if len(mergedIncidents) == 0 {
		return nil
	}

	masterIncident, err := s.incidentRepo.FindByID(ctx, masterIncidentID)
	if err != nil {
		return err
	}

	for _, merged := range mergedIncidents {
		updates := map[string]interface{}{}
		if newLocationID != nil {
			updates["location_id"] = *newLocationID
		} else {
			updates["location_id"] = nil
		}

		if err := s.db.WithContext(ctx).Model(&models.Incident{}).
			Where("id = ?", merged.ID).
			Updates(updates).Error; err != nil {
			log.Printf("[SyncLocation] Failed to update location for child %s: %v", merged.IncidentNumber, err)
			continue
		}

		changes := []models.IncidentFieldChange{
			{
				FieldName:  "location_id",
				FieldLabel: "Location",
				OldValue:   nil,
				NewValue:   &newLocationName,
			},
		}
		description := fmt.Sprintf(
			"Location changed to '%s' (synced from master incident %s)",
			newLocationName, masterIncident.IncidentNumber,
		)
		if revErr := s.CreateRevision(ctx, merged.ID, models.RevisionActionFieldChange, description, changes, userID); revErr != nil {
			log.Printf("[SyncLocation] Failed to create revision for child %s: %v", merged.IncidentNumber, revErr)
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

	// If this is a master incident (standalone or master with children)
	// Only return its own revisions (don't aggregate child revisions to avoid duplicates)
	if incident.MasterIncidentID == nil {
		filter.IncidentID = incidentID
		revisions, total, err = s.incidentRepo.ListRevisions(ctx, filter)
		if err != nil {
			return nil, 0, err
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
			_, smsErr := utils.SendSMS(ctx, merged.Reporter.Phone, smsMessage)
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
				IncidentID: &merged.ID,
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
				notification.FailureCode = ClassifyFailureCode(smsErr)
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
			_, smsErr := utils.SendSMS(ctx, merged.Reporter.Phone, smsMessage)
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
				IncidentID: &merged.ID,
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
				notification.FailureCode = ClassifyFailureCode(smsErr)
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
			"Master incident %s transitioned from %s to %s",
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

// applyCreationTimeAssignment applies workflow initial-state assignment rules
// to a newly created incident/complaint/query. This is the shared logic extracted
// from CreateIncident so that CreateComplaint and CreateQuery get the same behaviour.
func (s *incidentService) applyCreationTimeAssignment(ctx context.Context, incident *models.Incident, initialState *models.WorkflowState, creatorID uuid.UUID, source string) {
	distributeAssign := strings.TrimSpace(os.Getenv("DISTRIBUTE_INCIDENT_ASSIGN"))
	if strings.EqualFold(distributeAssign, "true") {
		// VD2: distribute every source (incl. web) uniformly via round-robin,
		// so a web incident is not self-assigned to its creator.
		clientCode := strings.TrimSpace(s.cfg.ClientCode)
		uniformRoundRobin := strings.EqualFold(clientCode, constants.CLIENT_CODE.VD2) &&
			strings.EqualFold(incident.RecordType, "incident")

		if !uniformRoundRobin &&
			strings.EqualFold(source, constants.INCIDENT_SOURCE.WEB) &&
			s.UserHasAssignmentRole(ctx, creatorID, initialState.AssignmentRoles) {
			if err := s.incidentRepo.AssignIncident(ctx, incident.ID, creatorID); err != nil {
				fmt.Printf("Warning: creation assignment (self-assign for web) failed: %v\n", err)
			} else {
				if err := s.incidentRepo.SetAssignees(ctx, incident.ID, []uuid.UUID{creatorID}); err != nil {
					fmt.Printf("Warning: SetAssignees (self-assign for web) failed: %v\n", err)
				}
			}
		} else {
			var classID, locID, deptID *uuid.UUID
			if incident.ClassificationID != nil {
				classID = incident.ClassificationID
			}
			if incident.LocationID != nil {
				locID = incident.LocationID
			}
			if incident.DepartmentID != nil {
				deptID = incident.DepartmentID
			}

			var roleIDs []uuid.UUID
			if len(initialState.AssignmentRoles) > 0 {
				roleIDs = make([]uuid.UUID, len(initialState.AssignmentRoles))
				for i, r := range initialState.AssignmentRoles {
					roleIDs[i] = r.ID
				}
			}

			nextAssigneeID, err := s.getNextRoundRobinAssignee(ctx, roleIDs, classID, locID, deptID)
			if err == nil && nextAssigneeID != nil {
				if err := s.incidentRepo.AssignIncident(ctx, incident.ID, *nextAssigneeID); err != nil {
					fmt.Printf("Warning: creation assignment (round-robin) failed: %v\n", err)
				} else {
					if err := s.incidentRepo.SetAssignees(ctx, incident.ID, []uuid.UUID{*nextAssigneeID}); err != nil {
						fmt.Printf("Warning: SetAssignees (round-robin) failed: %v\n", err)
					}
				}
			} else {
				fmt.Printf("Warning: no online eligible agents for round-robin, %s %s left unassigned: %v\n", incident.RecordType, incident.IncidentNumber, err)
			}
		}
	} else {
		if initialState.AssignUserID != nil {
			if err := s.incidentRepo.AssignIncident(ctx, incident.ID, *initialState.AssignUserID); err != nil {
				fmt.Printf("Warning: creation assignment (assign_user_id) failed: %v\n", err)
			} else {
				if err := s.incidentRepo.SetAssignees(ctx, incident.ID, []uuid.UUID{*initialState.AssignUserID}); err != nil {
					fmt.Printf("Warning: SetAssignees (assign_user_id) failed: %v\n", err)
				}
			}
		} else if initialState.ManualSelectUser && len(initialState.AssignmentRoles) > 0 {
			var classID, locID, deptID *uuid.UUID
			if incident.ClassificationID != nil {
				classID = incident.ClassificationID
			}
			if incident.LocationID != nil {
				locID = incident.LocationID
			}
			if incident.DepartmentID != nil {
				deptID = incident.DepartmentID
			}
			var roleIDs []uuid.UUID
			for _, r := range initialState.AssignmentRoles {
				roleIDs = append(roleIDs, r.ID)
			}
			availableUsers, _ := s.userRepo.FindMatching(ctx, roleIDs, classID, locID, deptID, nil)
			if len(availableUsers) > 0 {
				if err := s.incidentRepo.AssignIncident(ctx, incident.ID, availableUsers[0].ID); err != nil {
					fmt.Printf("Warning: creation assignment (manual select) failed: %v\n", err)
				} else {
					if err := s.incidentRepo.SetAssignees(ctx, incident.ID, []uuid.UUID{availableUsers[0].ID}); err != nil {
						fmt.Printf("Warning: SetAssignees (manual select) failed: %v\n", err)
					}
				}
			}
		} else if initialState.AutoMatchUser && len(initialState.AssignmentRoles) > 0 {
			var classID, locID, deptID *uuid.UUID
			if incident.ClassificationID != nil {
				classID = incident.ClassificationID
			}
			if incident.LocationID != nil {
				locID = incident.LocationID
			}
			if incident.DepartmentID != nil {
				deptID = incident.DepartmentID
			}
			var roleIDs []uuid.UUID
			for _, r := range initialState.AssignmentRoles {
				roleIDs = append(roleIDs, r.ID)
			}
			matchedUsers, err := s.userRepo.FindMatching(ctx, roleIDs, classID, locID, deptID, nil)
			if err == nil && len(matchedUsers) > 0 {
				if err := s.incidentRepo.AssignIncident(ctx, incident.ID, matchedUsers[0].ID); err != nil {
					fmt.Printf("Warning: creation assignment (auto match) failed: %v\n", err)
				} else {
					allIDs := make([]uuid.UUID, len(matchedUsers))
					for i, u := range matchedUsers {
						allIDs[i] = u.ID
					}
					if err := s.incidentRepo.SetAssignees(ctx, incident.ID, allIDs); err != nil {
						fmt.Printf("Warning: SetAssignees (auto match) failed: %v\n", err)
					}
				}
			}
		}
	}
}

// Complaint operations

func (s *incidentService) CreateComplaint(ctx context.Context, req *models.CreateComplaintRequest, creatorID uuid.UUID) (*models.IncidentResponse, error) {
	// Parse workflow ID
	workflowID, err := uuid.Parse(req.WorkflowID)
	if err != nil {
		return nil, errors.New(i18n.T(ctx, "invalid_workflow_id_lower"))
	}

	// Get the initial state of the workflow
	initialState, err := s.workflowRepo.GetInitialState(ctx, workflowID)
	if err != nil {
		return nil, errors.New(i18n.T(ctx, "workflow_no_initial_state"))
	}

	// Generate complaint number
	complaintNumber, err := s.incidentRepo.GenerateComplaintNumber(ctx)
	if err != nil {
		return nil, err
	}

	// Parse classification ID
	classificationID, err := uuid.Parse(req.ClassificationID)
	if err != nil {
		return nil, errors.New(i18n.T(ctx, "invalid_classification_id"))
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
		ReporterName:     req.ReporterName,
		ReporterEmail:    req.ReporterEmail,
		ReporterPhone:    req.ReporterPhone,
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
				return nil, errors.New(i18n.T(ctx, "source_incident_not_found"))
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
	slaDeadline, slaErr = s.calculateSLADeadline(ctx, &slaClassificationID, req.LookupValueIDs, initialState.SLADuration())
	if slaErr == nil && slaDeadline != nil {
		complaint.SLADeadline = slaDeadline
	}

	if err := s.incidentRepo.Create(ctx, complaint); err != nil {
		return nil, err
	}

	// Apply creation-time assignment rules (only if no explicit assignee was provided)
	if complaint.AssigneeID == nil {
		s.applyCreationTimeAssignment(ctx, complaint, initialState, creatorID, req.Source)
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
		return errors.New(i18n.T(ctx, "complaint_not_found"))
	}

	if incident.RecordType != "complaint" {
		return errors.New(i18n.T(ctx, "can_only_evaluate_complaints"))
	}

	// Check if complaint is in a terminal state (closed)
	if incident.CurrentState == nil || incident.CurrentState.StateType != "terminal" {
		return errors.New(i18n.T(ctx, "can_only_evaluate_closed"))
	}

	return s.incidentRepo.IncrementEvaluationCount(ctx, id)
}

// TriggerEvaluation fires integration triggers for manual feedback evaluation.
// It finds the last transition that brought the incident to its current state,
// verifies that transition has active integration triggers, then increments the
// evaluation count and fires those triggers.
// Returns error if no triggers are configured (button should not have been clickable).
func (s *incidentService) TriggerEvaluation(ctx context.Context, id uuid.UUID) error {
	incident, err := s.incidentRepo.FindByIDWithRelations(ctx, id)
	if err != nil {
		return errors.New(i18n.T(ctx, "incident_not_found"))
	}

	if incident.CurrentState == nil {
		return errors.New(i18n.T(ctx, "incident_no_current_state"))
	}

	if incident.CurrentState.StateType != "terminal" {
		return errors.New(i18n.T(ctx, "only_trigger_on_closed"))
	}

	if s.integrationExecutor == nil {
		return errors.New(i18n.T(ctx, "integration_executor_not_configured"))
	}

	// Find the last transition that moved the incident to its current state
	var lastHistory models.IncidentTransitionHistory
	if err := s.db.Where("incident_id = ? AND to_state_id = ? AND transition_id IS NOT NULL", id, incident.CurrentStateID).
		Order("transitioned_at DESC").
		Preload("Transition").
		First(&lastHistory).Error; err != nil {
		return errors.New(i18n.T(ctx, "no_transition_history"))
	}

	if lastHistory.TransitionID == nil {
		return errors.New(i18n.T(ctx, "last_transition_no_id"))
	}

	// Check that the transition actually has active integration triggers
	hasTriggers, err := s.integrationExecutor.HasActiveTransitionTriggers(ctx, *lastHistory.TransitionID)
	if err != nil || !hasTriggers {
		return errors.New(i18n.T(ctx, "no_integration_triggers"))
	}

	// All checks passed — increment evaluation count
	if err := s.incidentRepo.IncrementEvaluationCount(ctx, id); err != nil {
		return fmt.Errorf("%s: %w", i18n.T(ctx, "failed_to_increment_evaluation_count"), err)
	}

	transitionName := ""
	if lastHistory.Transition != nil {
		transitionName = lastHistory.Transition.Name
	}

	// Fire the transition triggers — same as what happens during ExecuteTransition
	s.integrationExecutor.RunTransitionTriggers(ctx, incident, *lastHistory.TransitionID, transitionName)
	return nil
}

func (s *incidentService) CreateQuery(ctx context.Context, req *models.CreateQueryRequest, creatorID uuid.UUID) (*models.IncidentResponse, error) {
	// Parse workflow ID
	workflowID, err := uuid.Parse(req.WorkflowID)
	if err != nil {
		return nil, errors.New(i18n.T(ctx, "invalid_workflow_id_lower"))
	}

	// Get the initial state of the workflow
	initialState, err := s.workflowRepo.GetInitialState(ctx, workflowID)
	if err != nil {
		return nil, errors.New(i18n.T(ctx, "workflow_no_initial_state"))
	}

	// Generate query number
	queryNumber, err := s.incidentRepo.GenerateQueryNumber(ctx)
	if err != nil {
		return nil, err
	}

	// Parse classification ID
	classificationID, err := uuid.Parse(req.ClassificationID)
	if err != nil {
		return nil, errors.New(i18n.T(ctx, "invalid_classification_id"))
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
				return nil, errors.New(i18n.T(ctx, "source_incident_not_found"))
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
	slaDeadline, slaErr = s.calculateSLADeadline(ctx, &slaClassificationID, req.LookupValueIDs, initialState.SLADuration())
	if slaErr == nil && slaDeadline != nil {
		query.SLADeadline = slaDeadline
	}

	if err := s.incidentRepo.Create(ctx, query); err != nil {
		return nil, err
	}

	// Apply creation-time assignment rules (only if no explicit assignee was provided)
	if query.AssigneeID == nil {
		s.applyCreationTimeAssignment(ctx, query, initialState, creatorID, req.Source)
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
		return nil, fmt.Errorf("%s", i18n.T(ctx, "incident_not_closed"))
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

// SendNotBelongClosureSMS sends an SMS to the citizen when an incident is closed via a "Not Belong" transition.
// It renders the DB template INCIDENT_CLOSURE_NOT_BELONG_SMS (AR language) when available,
// falling back to a hardcoded English message if the notification service is not wired in.
func (s *incidentService) SendNotBelongClosureSMS(
	ctx context.Context,
	incident *models.Incident,
	citizenMobile string,
	reporterID *uuid.UUID,
	transition *models.WorkflowTransition,
	assignedDeptID *uuid.UUID,
	userID uuid.UUID,
) {
	mobile := citizenMobile
	// Only fall back to reporter's phone when the reporter is confirmed to be a citizen.
	if mobile == "" && reporterID != nil {
		roles, err := s.userRepo.GetUserRoles(ctx, *reporterID)
		if err == nil {
			isCitizen := false
			for _, r := range roles {
				if r.Code == constants.USER_ROLE.CITIZEN {
					isCitizen = true
					break
				}
			}
			if isCitizen {
				if reporter, err := s.userRepo.FindByID(ctx, *reporterID); err == nil {
					mobile = reporter.Phone
				}
			}
		}
	}
	if mobile == "" {
		log.Printf("NOT-BELONG-SMS: No citizen mobile for incident %s, skipping", incident.IncidentNumber)
		return
	}

	// Resolve external department name (for the {{department_name}} template variable)
	deptName := "External Department"
	if transition.AssignDepartment != nil && transition.AssignDepartment.Name != "" {
		deptName = transition.AssignDepartment.Name
	} else if assignedDeptID != nil {
		if dept, err := s.deptRepo.FindByID(ctx, *assignedDeptID); err == nil {
			deptName = dept.Name
		}
	}

	// Use the notification service + DB template when available.
	if s.notificationService != nil {
		vars := BuildIncidentVariables(incident, transition, nil)
		vars["department_name"] = deptName

		templateCode := "INCIDENT_CLOSURE_NOT_BELONG_SMS"
		result, err := s.notificationService.SendNotification(
			ctx, "sms", &templateCode, "ar",
			[]string{mobile}, nil, nil,
			"", "", vars, nil, &userID, nil,
		)
		if result != nil && result.SentLog != nil {
			_ = s.notificationService.SetIncidentIDOnLogs(ctx, []uuid.UUID{result.SentLog.ID}, incident.ID)
		}
		if err != nil {
			log.Printf("NOT-BELONG-SMS: Template send failed for incident %s: %v", incident.IncidentNumber, err)
		} else {
			log.Printf("NOT-BELONG-SMS: Sent via template to %s for incident %s", mobile, incident.IncidentNumber)
		}
		return
	}

	// Fallback: hardcoded message when notification service is not available.
	smsMessage := fmt.Sprintf(
		"Dear Citizen, your incident (ID: %s) has been reviewed. The issue belongs to the %s department and does not fall under our department's scope for processing. Kindly contact the concerned department for further assistance. We appreciate your understanding.",
		incident.IncidentNumber,
		deptName,
	)

	now := time.Now()
	_, smsErr := utils.SendSMS(ctx, mobile, smsMessage)
	status := "sent"
	if smsErr != nil {
		status = "failed"
		log.Printf("NOT-BELONG-SMS: Failed for incident %s to %s: %v", incident.IncidentNumber, mobile, smsErr)
	} else {
		log.Printf("NOT-BELONG-SMS: Sent successfully to %s for incident %s", mobile, incident.IncidentNumber)
	}

	notification := &models.NotificationLog{
		Channel:    "sms",
		Direction:  "outbound",
		Category:   "sent",
		Language:   "en",
		Recipients: models.RecipientArray{{Email: mobile, Type: "to", Status: status}},
		IncidentID: &incident.ID,
		Subject:    "Incident Not Belong Closure",
		Body:       smsMessage,
		Status:     status,
		Provider:   "twilio",
		IsRead:     false,
		SentBy:     &userID,
		SentAt:     &now,
	}
	if smsErr != nil {
		notification.ErrorMessage = smsErr.Error()
		notification.FailureCode = ClassifyFailureCode(smsErr)
	}
	if err := s.incidentRepo.CreateNotification(ctx, notification); err != nil {
		log.Printf("NOT-BELONG-SMS: Failed to log notification for incident %s: %v", incident.IncidentNumber, err)
	}
}

// sendConvertToRequestSMS sends an SMS to the citizen when an incident is converted to a request.
// It uses template INC_TO_REQ_SMS when available; falls back to a hardcoded Arabic message otherwise.
func (s *incidentService) sendConvertToRequestSMS(ctx context.Context, incident *models.Incident, requestNumber string, userID uuid.UUID) {
	mobile := incident.CreatedByMobile
	if mobile == "" && incident.ReporterID != nil {
		roles, err := s.userRepo.GetUserRoles(ctx, *incident.ReporterID)
		if err == nil {
			isCitizen := false
			for _, r := range roles {
				if r.Code == constants.USER_ROLE.CITIZEN {
					isCitizen = true
					break
				}
			}
			if isCitizen {
				if reporter, err := s.userRepo.FindByID(ctx, *incident.ReporterID); err == nil {
					mobile = reporter.Phone
				}
			}
		}
	}
	if mobile == "" {
		log.Printf("CONVERT-TO-REQUEST-SMS: No citizen mobile for incident %s, skipping", incident.IncidentNumber)
		return
	}

	vars := BuildIncidentVariables(incident, nil, nil)
	vars["request_number"] = requestNumber

	if s.notificationService != nil {
		err := s.notificationService.SendByActionTypeForIncident(
			ctx, models.TemplateActionConvertToRequest, "sms", "ar",
			[]string{mobile}, vars, &userID, incident.ID,
		)
		if err != nil {
			log.Printf("CONVERT-TO-REQUEST-SMS: No active templates found for incident %s, falling back to hardcoded: %v", incident.IncidentNumber, err)
		} else {
			log.Printf("CONVERT-TO-REQUEST-SMS: Sent via template(s) to %s for incident %s", mobile, incident.IncidentNumber)
			return
		}
	}

	// Hardcoded Arabic fallback
	smsMessage := fmt.Sprintf("تم تحويل بلاغك رقم %s إلى طلب رقم %s", incident.IncidentNumber, requestNumber)
	now := time.Now()
	_, smsErr := utils.SendSMS(ctx, mobile, smsMessage)
	status := "sent"
	if smsErr != nil {
		status = "failed"
		log.Printf("CONVERT-TO-REQUEST-SMS: Fallback failed for incident %s to %s: %v", incident.IncidentNumber, mobile, smsErr)
	} else {
		log.Printf("CONVERT-TO-REQUEST-SMS: Fallback sent to %s for incident %s", mobile, incident.IncidentNumber)
	}

	notification := &models.NotificationLog{
		Channel:    "sms",
		Direction:  "outbound",
		Category:   "sent",
		Language:   "ar",
		Recipients: models.RecipientArray{{Email: mobile, Type: "to", Status: status}},
		IncidentID: &incident.ID,
		Subject:    "Incident Converted to Request",
		Body:       smsMessage,
		Status:     status,
		Provider:   "twilio",
		IsRead:     false,
		SentBy:     &userID,
		SentAt:     &now,
	}
	if smsErr != nil {
		notification.ErrorMessage = smsErr.Error()
		notification.FailureCode = ClassifyFailureCode(smsErr)
	}
	if err := s.incidentRepo.CreateNotification(ctx, notification); err != nil {
		log.Printf("CONVERT-TO-REQUEST-SMS: Failed to log notification for incident %s: %v", incident.IncidentNumber, err)
	}
}

// SendMissingInfoClosureSMS sends an SMS to the citizen when an incident is closed via a "Missing Incident Information" transition.
// Uses active templates with action_type=missing_info first; falls back to hardcoded Arabic message.
func (s *incidentService) SendMissingInfoClosureSMS(
	ctx context.Context,
	incident *models.Incident,
	citizenMobile string,
	reporterID *uuid.UUID,
	userID uuid.UUID,
) {
	mobile := citizenMobile
	log.Printf("MISSING-INFO-SMS: citizenMobile=%q reporterID=%v for incident %s", citizenMobile, reporterID, incident.IncidentNumber)
	if mobile == "" && reporterID != nil {
		roles, err := s.userRepo.GetUserRoles(ctx, *reporterID)
		if err == nil {
			isCitizen := false
			for _, r := range roles {
				if r.Code == constants.USER_ROLE.CITIZEN {
					isCitizen = true
					break
				}
			}
			if isCitizen {
				if reporter, err := s.userRepo.FindByID(ctx, *reporterID); err == nil {
					mobile = reporter.Phone
					log.Printf("MISSING-INFO-SMS: resolved citizen mobile from reporter: %q", mobile)
				}
			} else {
				log.Printf("MISSING-INFO-SMS: reporter is not a citizen, skipping phone lookup")
			}
		}
	}
	if mobile == "" {
		log.Printf("MISSING-INFO-SMS: No citizen mobile for incident %s, skipping", incident.IncidentNumber)
		return
	}

	log.Printf("MISSING-INFO-SMS: targeting mobile=%q for incident %s", mobile, incident.IncidentNumber)
	vars := BuildIncidentVariables(incident, nil, nil)

	if s.notificationService != nil {
		err := s.notificationService.SendByActionTypeForIncident(
			ctx, models.TemplateActionMissingInfo, "sms", "ar",
			[]string{mobile}, vars, &userID, incident.ID,
		)
		if err != nil {
			log.Printf("MISSING-INFO-SMS: No active templates for incident %s, falling back to hardcoded: %v", incident.IncidentNumber, err)
		} else {
			log.Printf("MISSING-INFO-SMS: Sent via template to %s for incident %s", mobile, incident.IncidentNumber)
			return
		}
	}

	// Hardcoded fallback
	smsMessage := fmt.Sprintf(
		"Dear Citizen, your incident (ID: %s) has been closed due to insufficient information. We kindly request that you raise it again with all the required details so we can assist you better. Thank you!",
		incident.IncidentNumber,
	)
	now := time.Now()
	_, smsErr := utils.SendSMS(ctx, mobile, smsMessage)
	status := "sent"
	if smsErr != nil {
		status = "failed"
		log.Printf("MISSING-INFO-SMS: Fallback failed for incident %s to %s: %v", incident.IncidentNumber, mobile, smsErr)
	} else {
		log.Printf("MISSING-INFO-SMS: Fallback sent to %s for incident %s", mobile, incident.IncidentNumber)
	}

	notification := &models.NotificationLog{
		Channel:    "sms",
		Direction:  "outbound",
		Category:   "sent",
		Language:   "en",
		Recipients: models.RecipientArray{{Email: mobile, Type: "to", Status: status}},
		IncidentID: &incident.ID,
		Subject:    "Incident Closed - Missing Information",
		Body:       smsMessage,
		Status:     status,
		Provider:   "twilio",
		IsRead:     false,
		SentBy:     &userID,
		SentAt:     &now,
	}
	if smsErr != nil {
		notification.ErrorMessage = smsErr.Error()
		notification.FailureCode = ClassifyFailureCode(smsErr)
	}
	if err := s.incidentRepo.CreateNotification(ctx, notification); err != nil {
		log.Printf("MISSING-INFO-SMS: Failed to log notification for incident %s: %v", incident.IncidentNumber, err)
	}
}

// AutoAssignUnassigned finds incidents in the state configured by AUTO_ASSIGN_STATE_CODE that
// have no rows in incident_assignees, and assigns them to an online eligible agent using the
// same logic as CreateIncident (respects DISTRIBUTE_INCIDENT_ASSIGN and workflow state rules).
func (s *incidentService) AutoAssignUnassigned(ctx context.Context) error {
	stateCode := strings.TrimSpace(os.Getenv("AUTO_ASSIGN_STATE_CODE"))
	if stateCode == "" {
		return nil
	}

	incidents, err := s.incidentRepo.FindUnassignedByStateCode(ctx, stateCode)
	if err != nil {
		return fmt.Errorf("auto-assign: query unassigned: %w", err)
	}
	if len(incidents) == 0 {
		return nil
	}

	log.Printf("[AutoAssign] Found %d unassigned incident(s) in state %q", len(incidents), stateCode)

	distributeAssign := strings.EqualFold(strings.TrimSpace(os.Getenv("DISTRIBUTE_INCIDENT_ASSIGN")), "true")

	for i := range incidents {
		incident := &incidents[i]
		if incident.CurrentState == nil {
			log.Printf("[AutoAssign] Skipping %s: CurrentState not loaded", incident.IncidentNumber)
			continue
		}
		currentState := incident.CurrentState

		var classID, locID, deptID *uuid.UUID
		if incident.ClassificationID != nil {
			classID = incident.ClassificationID
		}
		if incident.LocationID != nil {
			locID = incident.LocationID
		}
		if incident.DepartmentID != nil {
			deptID = incident.DepartmentID
		}

		var assigneeID *uuid.UUID

		// @Maybe add logic source basis also
		if distributeAssign {
			var roleIDs []uuid.UUID
			for _, r := range currentState.AssignmentRoles {
				roleIDs = append(roleIDs, r.ID)
			}
			nextID, err := s.getNextRoundRobinAssignee(ctx, roleIDs, classID, locID, deptID)
			if err != nil || nextID == nil {
				log.Printf("[AutoAssign] %s: no online agents for round-robin: %v", incident.IncidentNumber, err)
				continue
			}
			assigneeID = nextID
		} else if currentState.AssignUserID != nil {
			online, err := s.userRepo.IsUserOnline(ctx, *currentState.AssignUserID)
			if err != nil || !online {
				log.Printf("[AutoAssign] %s: fixed assignee offline or error: %v", incident.IncidentNumber, err)
				continue
			}
			assigneeID = currentState.AssignUserID
		} else if len(currentState.AssignmentRoles) > 0 {
			var roleIDs []uuid.UUID
			for _, r := range currentState.AssignmentRoles {
				roleIDs = append(roleIDs, r.ID)
			}
			users, err := s.userRepo.FindMatchingOnline(ctx, roleIDs, classID, locID, deptID, nil)
			if err != nil || len(users) == 0 {
				log.Printf("[AutoAssign] %s: no online matching users: %v", incident.IncidentNumber, err)
				continue
			}
			assigneeID = &users[0].ID

			if currentState.AutoMatchUser {
				allIDs := make([]uuid.UUID, len(users))
				for j, u := range users {
					allIDs[j] = u.ID
				}
				if err := s.incidentRepo.AssignIncident(ctx, incident.ID, *assigneeID); err != nil {
					log.Printf("[AutoAssign] %s: AssignIncident failed: %v", incident.IncidentNumber, err)
					continue
				}
				if err := s.incidentRepo.SetAssignees(ctx, incident.ID, allIDs); err != nil {
					log.Printf("[AutoAssign] %s: SetAssignees failed: %v", incident.IncidentNumber, err)
				} else {
					log.Printf("[AutoAssign] %s: assigned to %s (+%d total)", incident.IncidentNumber, assigneeID, len(allIDs))
				}
				continue
			}
		} else {
			log.Printf("[AutoAssign] %s: no assignment rule configured on state %q", incident.IncidentNumber, currentState.Code)
			continue
		}

		if err := s.incidentRepo.AssignIncident(ctx, incident.ID, *assigneeID); err != nil {
			log.Printf("[AutoAssign] %s: AssignIncident failed: %v", incident.IncidentNumber, err)
			continue
		}
		if err := s.incidentRepo.SetAssignees(ctx, incident.ID, []uuid.UUID{*assigneeID}); err != nil {
			log.Printf("[AutoAssign] %s: SetAssignees failed: %v", incident.IncidentNumber, err)
		} else {
			log.Printf("[AutoAssign] %s: assigned to %s", incident.IncidentNumber, assigneeID)
		}
	}

	return nil
}
