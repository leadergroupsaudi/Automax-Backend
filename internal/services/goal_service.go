package services

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/automax/backend/internal/config"
	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/internal/services/metricformula"
	"github.com/automax/backend/internal/storage"
	"github.com/automax/backend/pkg/constants"
	"github.com/google/uuid"
	"github.com/xuri/excelize/v2"
	"gorm.io/gorm"
)

// ════════════════════════════════════════════════════
// GoalService Interface
// ════════════════════════════════════════════════════

type GoalService interface {
	// Goal CRUD
	CreateGoal(ctx context.Context, req *models.GoalCreateRequest, userID uuid.UUID) (*models.GoalResponse, error)
	GetGoal(ctx context.Context, id uuid.UUID, userID uuid.UUID) (*models.GoalResponse, error)
	ListGoals(ctx context.Context, filter *models.GoalFilter) ([]models.GoalResponse, int64, error)
	UpdateGoal(ctx context.Context, id uuid.UUID, req *models.GoalUpdateRequest, userID uuid.UUID) (*models.GoalResponse, error)
	DeleteGoal(ctx context.Context, id uuid.UUID, userID uuid.UUID) error

	// Status Transitions
	TransitionGoalStatus(ctx context.Context, id uuid.UUID, newStatus string, userID uuid.UUID) (*models.GoalResponse, error)

	// Collaborators
	AddCollaborator(ctx context.Context, goalID uuid.UUID, req *models.CollaboratorAddRequest, userID uuid.UUID) error
	RemoveCollaborator(ctx context.Context, goalID uuid.UUID, collaboratorUserID uuid.UUID, userID uuid.UUID) error

	// Metrics
	CreateMetric(ctx context.Context, goalID uuid.UUID, req *models.GoalMetricCreateRequest, userID uuid.UUID) (*models.GoalMetricResponse, error)
	UpdateMetric(ctx context.Context, id uuid.UUID, req *models.GoalMetricUpdateRequest, userID uuid.UUID) (*models.GoalMetricResponse, error)
	DeleteMetric(ctx context.Context, id uuid.UUID, userID uuid.UUID) error
	UpdateMetricValue(ctx context.Context, id uuid.UUID, req *models.MetricValueUpdateRequest, userID uuid.UUID) (*models.GoalMetricValueChangeResponse, error)
	GetMetricHistory(ctx context.Context, metricID uuid.UUID, page int, limit int) ([]models.MetricHistoryResponse, int64, error)
	ListMetricValueChanges(ctx context.Context, metricID uuid.UUID, userID uuid.UUID) ([]models.GoalMetricValueChangeResponse, error)

	// Metric Workflow Transitions
	GetAvailableMetricTransitions(ctx context.Context, metricID uuid.UUID, userID uuid.UUID) ([]models.AvailableTransitionResponse, error)
	TransitionMetric(ctx context.Context, metricID uuid.UUID, req *models.MetricTransitionRequest, userID uuid.UUID) (*models.GoalMetricResponse, error)
	GetAvailableMetricValueChangeTransitions(ctx context.Context, changeID uuid.UUID, userID uuid.UUID) ([]models.AvailableTransitionResponse, error)
	TransitionMetricValueChange(ctx context.Context, changeID uuid.UUID, req *models.MetricTransitionRequest, userID uuid.UUID) (*models.GoalMetricValueChangeResponse, error)
	ListPendingMetricApprovals(ctx context.Context, userID uuid.UUID, page int, limit int) ([]models.MetricApprovalListResponse, int64, error)
	ListPendingMetricValueChangeApprovals(ctx context.Context, userID uuid.UUID, page int, limit int) ([]models.MetricApprovalListResponse, int64, error)

	// Evidence
	CreateEvidence(ctx context.Context, goalID uuid.UUID, title string, evidenceType string, comment string, metricID *uuid.UUID, fileName string, fileSize int64, mimeType string, fileData []byte, userID uuid.UUID) (*models.EvidenceResponse, error)
	DeleteEvidence(ctx context.Context, id uuid.UUID, userID uuid.UUID) error
	ListEvidences(ctx context.Context, goalID uuid.UUID, filter *models.EvidenceFilter, userID uuid.UUID) ([]models.EvidenceResponse, int64, error)
	GetEvidence(ctx context.Context, id uuid.UUID) (*models.EvidenceResponse, error)
	GetEvidencePreview(ctx context.Context, evidenceID uuid.UUID) (string, error)
	GetEvidenceDownloadURL(ctx context.Context, evidenceID uuid.UUID) (string, error)
	DownloadEvidenceFile(ctx context.Context, evidenceID uuid.UUID) (io.ReadCloser, *storage.DmsFile, string, string, error)
	ReplaceEvidenceFile(ctx context.Context, evidenceID uuid.UUID, fileName string, fileSize int64, mimeType string, fileData []byte, userID uuid.UUID) (*models.EvidenceResponse, error)

	// Evidence Workflow Transitions
	GetAvailableEvidenceTransitions(ctx context.Context, evidenceID uuid.UUID, userID uuid.UUID) ([]models.AvailableTransitionResponse, error)
	ExecuteEvidenceTransition(ctx context.Context, evidenceID uuid.UUID, req *models.EvidenceTransitionRequest, userID uuid.UUID) (*models.EvidenceResponse, error)
	GetEvidenceTransitionHistory(ctx context.Context, evidenceID uuid.UUID) ([]models.EvidenceTransitionHistoryResponse, error)
	ListPendingApprovals(ctx context.Context, userID uuid.UUID, page int, limit int) ([]models.ApprovalListResponse, int64, error)
	ListCompletedApprovals(ctx context.Context, userID uuid.UUID, page int, limit int) ([]models.ApprovalListResponse, int64, error)

	// Export
	ExportGoalsCSV(ctx context.Context, filter *models.GoalFilter) ([]byte, error)
	ExportGoalsJSON(ctx context.Context, filter *models.GoalFilter) ([]models.GoalResponse, error)

	// Clone
	CloneGoal(ctx context.Context, id uuid.UUID, req *models.GoalCloneRequest, userID uuid.UUID) (*models.GoalResponse, error)

	// Import
	ImportGoals(ctx context.Context, fileData []byte, fileName string, dryRun bool, userID uuid.UUID) (*models.GoalImportResponse, error)

	// Bulk Operations
	BulkAction(ctx context.Context, req *models.BulkActionRequest, userID uuid.UUID) (*models.BulkActionResponse, error)

	// Comments
	AddComment(ctx context.Context, goalID uuid.UUID, content string, userID uuid.UUID) (*models.GoalCommentResponse, error)
	ListComments(ctx context.Context, goalID uuid.UUID, page, limit int) ([]models.GoalCommentResponse, int64, error)
	DeleteComment(ctx context.Context, commentID uuid.UUID, userID uuid.UUID) error

	// Hierarchy
	GetGoalTree(ctx context.Context, rootID uuid.UUID) (*models.GoalResponse, error)
	ListChildGoals(ctx context.Context, parentID uuid.UUID) ([]models.GoalResponse, error)

	// Check-ins
	CreateCheckIn(ctx context.Context, goalID uuid.UUID, req *models.CheckInCreateRequest, userID uuid.UUID) (*models.CheckInResponse, error)
	ListCheckIns(ctx context.Context, goalID uuid.UUID, page int, limit int) ([]models.CheckInResponse, int64, error)
	DeleteCheckIn(ctx context.Context, id uuid.UUID, userID uuid.UUID) error

	// Progress
	RecalculateProgress(ctx context.Context, goalID uuid.UUID) error

	// Metric Import/Export
	ExportMetricsTemplate(ctx context.Context, filter *models.GoalFilter, format string) ([]byte, error)
	ImportMetricsDryRun(ctx context.Context, fileData []byte, fileName string, userID uuid.UUID) (*models.MetricImportDryRunResponse, error)
	ImportMetricsCommit(ctx context.Context, fileData []byte, fileName string, title string, comment string, primaryGoalID uuid.UUID, userID uuid.UUID) (*models.MetricImportBatchResponse, error)
	GetMetricImportBatch(ctx context.Context, id uuid.UUID) (*models.MetricImportBatchResponse, error)
	ListMetricImportBatches(ctx context.Context, filter *models.MetricImportBatchFilter) ([]models.MetricImportBatchResponse, int64, error)
	DeleteMetricImportBatch(ctx context.Context, id uuid.UUID, userID uuid.UUID) error
	GetAvailableMetricBatchTransitions(ctx context.Context, batchID uuid.UUID, userID uuid.UUID) ([]models.AvailableTransitionResponse, error)
	ExecuteMetricBatchTransition(ctx context.Context, batchID uuid.UUID, req *models.MetricImportBatchTransitionRequest, userID uuid.UUID) (*models.MetricImportBatchResponse, error)
	GetMetricBatchTransitionHistory(ctx context.Context, batchID uuid.UUID) ([]models.MetricImportBatchTransitionHistoryResponse, error)
}

// ════════════════════════════════════════════════════
// Private Implementation
// ════════════════════════════════════════════════════

type goalService struct {
	goalRepo            repository.GoalRepository
	workflowRepo        repository.WorkflowRepository
	userRepo            repository.UserRepository
	departmentRepo      repository.DepartmentRepository
	documentaClient     storage.DocumentaClient
	notificationService *NotificationService
	actionLogService    ActionLogService
	cfg                 *config.Config
	wsHub               *WSHub
}

func NewGoalService(
	goalRepo repository.GoalRepository,
	workflowRepo repository.WorkflowRepository,
	userRepo repository.UserRepository,
	departmentRepo repository.DepartmentRepository,
	documentaClient storage.DocumentaClient,
	notificationService *NotificationService,
	actionLogService ActionLogService,
	cfg *config.Config,
	wsHub *WSHub,
) GoalService {
	return &goalService{
		goalRepo:            goalRepo,
		workflowRepo:        workflowRepo,
		userRepo:            userRepo,
		departmentRepo:      departmentRepo,
		documentaClient:     documentaClient,
		notificationService: notificationService,
		actionLogService:    actionLogService,
		cfg:                 cfg,
		wsHub:               wsHub,
	}
}

// ──────────────────────────────────────────────────
// Access Control Helpers
// ──────────────────────────────────────────────────

// canAccessGoal checks if the user can access the given goal.
// Access is granted if the user is:
//   - Super admin
//   - Goal owner
//   - A collaborator on the goal
//   - In the same department as the goal
func (s *goalService) canAccessGoal(ctx context.Context, goalID uuid.UUID, userID uuid.UUID) (bool, error) {
	// Check if super admin via context
	if s.isSuperAdmin(ctx) {
		return true, nil
	}

	goal, err := s.goalRepo.FindByID(ctx, goalID)
	if err != nil {
		return false, err
	}

	// Owner check
	if goal.OwnerID == userID {
		return true, nil
	}

	// Collaborator check
	collaborators, _ := s.goalRepo.ListCollaborators(ctx, goalID)
	for _, c := range collaborators {
		if c.UserID == userID {
			return true, nil
		}
	}

	// Department check
	if goal.DepartmentID != nil {
		user, err := s.userRepo.FindByID(ctx, userID)
		if err == nil && user != nil {
			if user.DepartmentID != nil && *user.DepartmentID == *goal.DepartmentID {
				return true, nil
			}
		}
	}

	return false, nil
}

// canSeeUnapprovedItems returns true if the caller should see metrics/evidence
// regardless of workflow state. This includes super admins, the goal owner, any
// goal collaborator, and users that hold a goals:approve permission. View-only
// callers (goals:view but no other privileges) get the filtered listing.
//
// We deliberately *do not* peek at the user's permissions through the role
// store here — instead we look at whether the caller already has approve
// authority via the *models.User snapshot in context. That's sufficient for
// the cases the platform exercises (approve permission is granted by role
// assignment which IsSuperAdmin or has goals:approve).
func (s *goalService) canSeeUnapprovedItems(ctx context.Context, goalID uuid.UUID, userID uuid.UUID) bool {
	if s.isSuperAdmin(ctx) {
		return true
	}
	if s.userHasPermission(ctx, "goals:approve") || s.userHasPermission(ctx, "goals:delete") {
		return true
	}
	goal, err := s.goalRepo.FindByID(ctx, goalID)
	if err != nil {
		return false
	}
	if goal.OwnerID == userID {
		return true
	}
	collaborators, _ := s.goalRepo.ListCollaborators(ctx, goalID)
	for _, c := range collaborators {
		if c.UserID == userID {
			return true
		}
	}
	return false
}

// userHasPermission checks the *models.User in context for a permission code.
func (s *goalService) userHasPermission(ctx context.Context, code string) bool {
	user, ok := ctx.Value(constants.ContextKeys.User).(*models.User)
	if !ok || user == nil {
		return false
	}
	return user.HasPermission(code)
}

// canModifyGoal checks if the user can modify the goal (owner, reviewer, or super admin)
func (s *goalService) canModifyGoal(ctx context.Context, goalID uuid.UUID, userID uuid.UUID) (bool, error) {
	if s.isSuperAdmin(ctx) {
		return true, nil
	}

	goal, err := s.goalRepo.FindByID(ctx, goalID)
	if err != nil {
		return false, err
	}

	// Owner can always modify
	if goal.OwnerID == userID {
		return true, nil
	}

	// Any collaborator on the goal can modify it. We used to gate this on
	// reviewer_l1/reviewer_l2 only, which forced admins to assign a "fake
	// reviewer" role to anyone who needed to add metrics or evidence. The
	// approval workflow now gates visibility (metrics, evidence, value
	// changes all need reviewer sign-off before non-owners see them), so
	// allowing any collaborator to author content is safe — published
	// state is still controlled.
	collaborators, _ := s.goalRepo.ListCollaborators(ctx, goalID)
	for _, c := range collaborators {
		if c.UserID == userID {
			return true, nil
		}
	}

	return false, nil
}

// metricSiblingsMap builds the name -> current value map consumed by the formula
// evaluator. Pass the result of ListMetricsByGoalID; the caller can exclude
// a specific metric (e.g. the one being recomputed) by nil'ing it first.
func metricSiblingsMap(metrics []models.GoalMetric) map[string]float64 {
	out := make(map[string]float64, len(metrics))
	for _, m := range metrics {
		out[m.Name] = m.CurrentValue
	}
	return out
}

// recomputeDependentFormulas re-evaluates every formula-bearing metric on the
// goal that references the changed metric by name. Errors are logged, not
// surfaced — one bad formula should not block a value update. changedMetricID
// is excluded so the just-updated metric doesn't re-run its own formula.
func (s *goalService) recomputeDependentFormulas(ctx context.Context, goalID, changedMetricID uuid.UUID) {
	all, err := s.goalRepo.ListMetricsByGoalID(ctx, goalID)
	if err != nil {
		return
	}
	// Find which metric name was changed so we can scan formulas for references.
	var changedName string
	for _, m := range all {
		if m.ID == changedMetricID {
			changedName = m.Name
			break
		}
	}
	for _, m := range all {
		if m.ID == changedMetricID || strings.TrimSpace(m.Formula) == "" {
			continue
		}
		if changedName != "" && !strings.Contains(m.Formula, changedName) {
			continue // formula doesn't reference the changed metric; skip
		}
		siblings := make([]models.GoalMetric, 0, len(all))
		for _, sib := range all {
			if sib.ID != m.ID {
				siblings = append(siblings, sib)
			}
		}
		computed, ferr := metricformula.Evaluate(m.Formula, metricSiblingsMap(siblings))
		if ferr != nil {
			log.Printf("[goal_service] recomputeDependentFormulas: metric %s formula %q failed: %v", m.ID, m.Formula, ferr)
			continue
		}
		if computed == m.CurrentValue {
			continue
		}
		history := &models.MetricHistory{
			MetricID: m.ID,
			OldValue: m.CurrentValue,
			NewValue: computed,
			Comment:  fmt.Sprintf("Auto-recomputed after %s changed", changedName),
		}
		_ = s.goalRepo.CreateMetricHistory(ctx, history)
		metricCopy := m
		metricCopy.CurrentValue = computed
		if err := s.goalRepo.UpdateMetric(ctx, &metricCopy); err != nil {
			log.Printf("[goal_service] recomputeDependentFormulas: update metric %s failed: %v", m.ID, err)
		}
	}
}

// isSuperAdmin checks if the current request is from a super admin
func (s *goalService) isSuperAdmin(ctx context.Context) bool {
	if user, ok := ctx.Value(constants.ContextKeys.User).(*models.User); ok {
		return user.IsSuperAdmin
	}
	return false
}

// getUserDepartmentIDs returns the department IDs the user belongs to
func (s *goalService) getUserDepartmentIDs(ctx context.Context, userID uuid.UUID) []uuid.UUID {
	user, err := s.userRepo.FindByID(ctx, userID)
	if err != nil || user == nil {
		return nil
	}
	var deptIDs []uuid.UUID
	if user.DepartmentID != nil {
		deptIDs = append(deptIDs, *user.DepartmentID)
	}
	return deptIDs
}

// ──────────────────────────────────────────────────
// 1. CreateGoal
// ──────────────────────────────────────────────────

func (s *goalService) CreateGoal(ctx context.Context, req *models.GoalCreateRequest, userID uuid.UUID) (*models.GoalResponse, error) {
	// Validate dates
	if err := req.Validate(); err != nil {
		return nil, err
	}

	// Validate parent goal hierarchy
	var parentPath string
	var parentLevel int
	if req.ParentGoalID != nil {
		parent, err := s.goalRepo.FindByID(ctx, *req.ParentGoalID)
		if err != nil {
			return nil, fmt.Errorf("parent goal not found: %w", err)
		}
		if parent.Level >= 2 {
			return nil, fmt.Errorf("maximum goal hierarchy depth is 3 levels (cannot add child to level %d goal)", parent.Level)
		}
		parentPath = parent.Path
		if parentPath == "" {
			parentPath = parent.ID.String()
		}
		parentLevel = parent.Level
	}

	// Ensure "Goal Management" folder at workspace root (idempotent)
	goalMgmtFolderID, err := s.documentaClient.EnsureFolder(ctx, s.cfg.Documenta.WorkspaceName, "", "Goal Management")
	if err != nil {
		return nil, fmt.Errorf("failed to ensure Goal Management folder: %w", err)
	}

	// Create this goal's folder under Goal Management
	folderID, err := s.documentaClient.CreateFolder(ctx, s.cfg.Documenta.WorkspaceName, goalMgmtFolderID, req.Title)
	if err != nil {
		return nil, fmt.Errorf("failed to create goal folder: %w", err)
	}

	// Default metadata to empty JSON object
	metadata := req.Metadata
	if strings.TrimSpace(metadata) == "" {
		metadata = "{}"
	}

	goalLevel := 0
	if req.ParentGoalID != nil {
		goalLevel = parentLevel + 1
	}

	goal := &models.Goal{
		Title:             req.Title,
		Description:       req.Description,
		Category:          req.Category,
		CategoryID:        req.CategoryID,
		Priority:          req.Priority,
		Status:            models.GoalStatusDraft,
		OwnerID:           req.OwnerID,
		DepartmentID:      req.DepartmentID,
		ParentGoalID:      req.ParentGoalID,
		Level:             goalLevel,
		StartDate:         req.StartDate,
		TargetDate:        req.TargetDate,
		ReviewDate:        req.ReviewDate,
		DocumentaFolderID: folderID,
		Metadata:          metadata,
		CreatedByID:       userID,
	}

	if err := s.goalRepo.Create(ctx, goal); err != nil {
		return nil, fmt.Errorf("failed to create goal: %w", err)
	}

	// Set materialized path after creation (need the generated ID)
	if parentPath != "" {
		goal.Path = parentPath + "." + goal.ID.String()
	} else {
		goal.Path = goal.ID.String()
	}
	if err := s.goalRepo.Update(ctx, goal); err != nil {
		return nil, fmt.Errorf("failed to set goal path: %w", err)
	}

	// Add the creator as a collaborator when they are not the owner. If
	// an admin/manager creates a goal on someone else's behalf they still
	// need access to the goal they authored; adding them as a collaborator
	// (not duplicating them when they are already the owner) preserves the
	// owner-vs-collaborator distinction without losing audit/access.
	if userID != req.OwnerID {
		collab := &models.GoalCollaborator{
			GoalID: goal.ID,
			UserID: userID,
			Role:   "collaborator",
		}
		if err := s.goalRepo.AddCollaborator(ctx, collab); err != nil {
			log.Printf("[goal_service] CreateGoal: failed to add creator %s as collaborator on goal %s: %v", userID, goal.ID, err)
		}
	}

	// Audit log
	s.actionLogService.LogAction(ctx, &LogActionParams{
		UserID:      userID,
		Action:      "create",
		Module:      "goals",
		ResourceID:  goal.ID.String(),
		Description: "Created goal: " + goal.Title,
		Status:      "success",
	})

	// Fetch back with relations
	goalWithRelations, err := s.goalRepo.FindByIDWithRelations(ctx, goal.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch created goal: %w", err)
	}

	resp := goalWithRelations.ToResponse()
	return &resp, nil
}

// ──────────────────────────────────────────────────
// 2. GetGoal
// ──────────────────────────────────────────────────

func (s *goalService) GetGoal(ctx context.Context, id uuid.UUID, userID uuid.UUID) (*models.GoalResponse, error) {
	// Access check
	canAccess, err := s.canAccessGoal(ctx, id, userID)
	if err != nil {
		return nil, fmt.Errorf("goal not found: %w", err)
	}
	if !canAccess {
		return nil, fmt.Errorf("access denied")
	}

	goal, err := s.goalRepo.FindByIDWithRelations(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("goal not found: %w", err)
	}

	// Visibility gate: view-only callers see only terminal-approved metrics
	// and evidences. Owners, collaborators, super admins, and approve-permission
	// holders see everything (so reviewers can see drafts).
	if !s.canSeeUnapprovedItems(ctx, id, userID) {
		filteredMetrics := make([]models.GoalMetric, 0, len(goal.Metrics))
		for _, m := range goal.Metrics {
			if m.CurrentStateID == nil {
				// Legacy metric (pre-gate) — visible.
				filteredMetrics = append(filteredMetrics, m)
				continue
			}
			if m.CurrentState != nil && m.CurrentState.StateType == "terminal" {
				filteredMetrics = append(filteredMetrics, m)
			}
		}
		goal.Metrics = filteredMetrics

		filteredEvidences := make([]models.Evidence, 0, len(goal.Evidences))
		for _, e := range goal.Evidences {
			if e.CurrentStateID == nil {
				filteredEvidences = append(filteredEvidences, e)
				continue
			}
			if e.CurrentState != nil && e.CurrentState.StateType == "terminal" {
				filteredEvidences = append(filteredEvidences, e)
			}
		}
		goal.Evidences = filteredEvidences
	}

	resp := goal.ToResponse()
	return &resp, nil
}

// ──────────────────────────────────────────────────
// 3. ListGoals
// ──────────────────────────────────────────────────

func (s *goalService) ListGoals(ctx context.Context, filter *models.GoalFilter) ([]models.GoalResponse, int64, error) {
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.Limit < 1 {
		filter.Limit = 10
	}

	goals, total, err := s.goalRepo.List(ctx, filter)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list goals: %w", err)
	}

	responses := make([]models.GoalResponse, len(goals))
	for i, g := range goals {
		responses[i] = g.ToResponse()
	}

	return responses, total, nil
}

// ──────────────────────────────────────────────────
// 4. UpdateGoal
// ──────────────────────────────────────────────────

func (s *goalService) UpdateGoal(ctx context.Context, id uuid.UUID, req *models.GoalUpdateRequest, userID uuid.UUID) (*models.GoalResponse, error) {
	// Access check
	canModify, _ := s.canModifyGoal(ctx, id, userID)
	if !canModify {
		return nil, fmt.Errorf("access denied: you can only modify goals you own or are assigned to review")
	}

	goal, err := s.goalRepo.FindByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("goal not found: %w", err)
	}

	// Apply non-nil fields
	if req.Title != nil {
		goal.Title = *req.Title
	}
	if req.Description != nil {
		goal.Description = *req.Description
	}
	if req.Category != nil {
		goal.Category = *req.Category
	}
	if req.CategoryID != nil {
		goal.CategoryID = req.CategoryID
	}
	if req.Priority != nil {
		goal.Priority = *req.Priority
	}
	if req.DepartmentID != nil {
		goal.DepartmentID = req.DepartmentID
	}
	if req.OwnerID != nil {
		goal.OwnerID = *req.OwnerID
	}
	if req.StartDate != nil {
		goal.StartDate = req.StartDate
	}
	if req.TargetDate != nil {
		goal.TargetDate = req.TargetDate
	}
	if req.ReviewDate != nil {
		goal.ReviewDate = req.ReviewDate
	}
	if req.Metadata != nil {
		goal.Metadata = *req.Metadata
	}

	if err := s.goalRepo.Update(ctx, goal); err != nil {
		return nil, fmt.Errorf("failed to update goal: %w", err)
	}

	// Audit log
	s.actionLogService.LogAction(ctx, &LogActionParams{
		UserID:      userID,
		Action:      "update",
		Module:      "goals",
		ResourceID:  goal.ID.String(),
		Description: "Updated goal: " + goal.Title,
		Status:      "success",
	})

	// WebSocket broadcast
	if s.wsHub != nil {
		s.wsHub.BroadcastToGoal(goal.ID, "goal_updated", map[string]interface{}{
			"goal_id":    goal.ID,
			"updated_by": userID,
		}, userID)
		s.wsHub.BroadcastGoalToAll("goal_updated", map[string]interface{}{
			"goal_id":    goal.ID,
			"updated_by": userID,
		})
	}

	// Fetch back with relations
	goalWithRelations, err := s.goalRepo.FindByIDWithRelations(ctx, goal.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch updated goal: %w", err)
	}

	resp := goalWithRelations.ToResponse()
	return &resp, nil
}

// ──────────────────────────────────────────────────
// 5. DeleteGoal
// ──────────────────────────────────────────────────

func (s *goalService) DeleteGoal(ctx context.Context, id uuid.UUID, userID uuid.UUID) error {
	// Access check
	canModify, _ := s.canModifyGoal(ctx, id, userID)
	if !canModify {
		return fmt.Errorf("access denied: only the goal owner can delete this goal")
	}

	goal, err := s.goalRepo.FindByID(ctx, id)
	if err != nil {
		return fmt.Errorf("goal not found: %w", err)
	}

	if err := s.goalRepo.Delete(ctx, id); err != nil {
		return fmt.Errorf("failed to delete goal: %w", err)
	}

	// Audit log
	s.actionLogService.LogAction(ctx, &LogActionParams{
		UserID:      userID,
		Action:      "delete",
		Module:      "goals",
		ResourceID:  goal.ID.String(),
		Description: "Deleted goal: " + goal.Title,
		Status:      "success",
	})

	return nil
}

// ──────────────────────────────────────────────────
// 6. TransitionGoalStatus
// ──────────────────────────────────────────────────

func (s *goalService) TransitionGoalStatus(ctx context.Context, id uuid.UUID, newStatus string, userID uuid.UUID) (*models.GoalResponse, error) {
	goal, err := s.goalRepo.FindByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("goal not found: %w", err)
	}

	if !models.IsValidGoalTransition(goal.Status, newStatus) {
		return nil, fmt.Errorf("invalid status transition from %s to %s", goal.Status, newStatus)
	}

	oldStatus := goal.Status
	goal.Status = newStatus

	if err := s.goalRepo.Update(ctx, goal); err != nil {
		return nil, fmt.Errorf("failed to update goal status: %w", err)
	}

	// Audit log
	s.actionLogService.LogAction(ctx, &LogActionParams{
		UserID:      userID,
		Action:      "transition",
		Module:      "goals",
		ResourceID:  goal.ID.String(),
		Description: fmt.Sprintf("Transitioned goal '%s' from %s to %s", goal.Title, oldStatus, newStatus),
		Status:      "success",
	})

	// Fetch back with relations
	goalWithRelations, err := s.goalRepo.FindByIDWithRelations(ctx, goal.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch updated goal: %w", err)
	}

	resp := goalWithRelations.ToResponse()
	return &resp, nil
}

// ──────────────────────────────────────────────────
// 7. AddCollaborator
// ──────────────────────────────────────────────────

func (s *goalService) AddCollaborator(ctx context.Context, goalID uuid.UUID, req *models.CollaboratorAddRequest, userID uuid.UUID) error {
	// Verify goal exists
	goal, err := s.goalRepo.FindByID(ctx, goalID)
	if err != nil {
		return fmt.Errorf("goal not found: %w", err)
	}

	// Only goal owner or super admin can manage collaborators
	if goal.OwnerID != userID && !s.isSuperAdmin(ctx) {
		return fmt.Errorf("access denied: only the goal owner can manage collaborators")
	}

	// Verify user exists
	user, err := s.userRepo.FindByID(ctx, req.UserID)
	if err != nil {
		return fmt.Errorf("user not found: %w", err)
	}

	collaborator := &models.GoalCollaborator{
		GoalID: goalID,
		UserID: req.UserID,
		Role:   req.Role,
	}

	if err := s.goalRepo.AddCollaborator(ctx, collaborator); err != nil {
		return fmt.Errorf("failed to add collaborator: %w", err)
	}

	// Send in-app notification
	s.notificationService.SendNotification(
		ctx,
		"notification",
		nil,
		"en",
		[]string{user.Email},
		nil,
		nil,
		"Goal Assignment",
		"You have been assigned as "+req.Role+" to goal: "+goal.Title,
		nil,
		nil,
		&userID,
		nil,
	)

	// Audit log
	s.actionLogService.LogAction(ctx, &LogActionParams{
		UserID:      userID,
		Action:      "create",
		Module:      "goals",
		ResourceID:  goalID.String(),
		Description: fmt.Sprintf("Added collaborator %s (%s) to goal: %s", user.Email, req.Role, goal.Title),
		Status:      "success",
	})

	// WebSocket broadcast
	if s.wsHub != nil {
		s.wsHub.BroadcastToGoal(goalID, "collaborator_changed", map[string]interface{}{
			"goal_id": goalID,
		}, userID)
	}

	return nil
}

// ──────────────────────────────────────────────────
// 8. RemoveCollaborator
// ──────────────────────────────────────────────────

func (s *goalService) RemoveCollaborator(ctx context.Context, goalID uuid.UUID, collaboratorUserID uuid.UUID, userID uuid.UUID) error {
	// Only goal owner or super admin can manage collaborators
	goal, err := s.goalRepo.FindByID(ctx, goalID)
	if err != nil {
		return fmt.Errorf("goal not found: %w", err)
	}
	if goal.OwnerID != userID && !s.isSuperAdmin(ctx) {
		return fmt.Errorf("access denied: only the goal owner can manage collaborators")
	}

	if err := s.goalRepo.RemoveCollaborator(ctx, goalID, collaboratorUserID); err != nil {
		return fmt.Errorf("failed to remove collaborator: %w", err)
	}

	// Audit log
	s.actionLogService.LogAction(ctx, &LogActionParams{
		UserID:      userID,
		Action:      "delete",
		Module:      "goals",
		ResourceID:  goalID.String(),
		Description: fmt.Sprintf("Removed collaborator %s from goal %s", collaboratorUserID.String(), goalID.String()),
		Status:      "success",
	})

	// WebSocket broadcast
	if s.wsHub != nil {
		s.wsHub.BroadcastToGoal(goalID, "collaborator_changed", map[string]interface{}{
			"goal_id": goalID,
		}, userID)
	}

	return nil
}

// ──────────────────────────────────────────────────
// 9. CreateMetric
// ──────────────────────────────────────────────────

func (s *goalService) CreateMetric(ctx context.Context, goalID uuid.UUID, req *models.GoalMetricCreateRequest, userID uuid.UUID) (*models.GoalMetricResponse, error) {
	// Access check
	canModify, _ := s.canModifyGoal(ctx, goalID, userID)
	if !canModify {
		return nil, fmt.Errorf("access denied: you can only modify goals you own or are assigned to review")
	}

	// Verify goal exists
	goal, err := s.goalRepo.FindByID(ctx, goalID)
	if err != nil {
		return nil, fmt.Errorf("goal not found: %w", err)
	}

	// Validate metric type
	if !models.IsValidMetricType(req.MetricType) {
		return nil, fmt.Errorf("invalid metric type: %s", req.MetricType)
	}

	weight := req.Weight
	if weight == 0 {
		weight = 1.0
	}

	metric := &models.GoalMetric{
		GoalID:        goalID,
		Name:          req.Name,
		MetricType:    req.MetricType,
		Unit:          req.Unit,
		BaselineValue: req.BaselineValue,
		CurrentValue:  req.CurrentValue,
		TargetValue:   req.TargetValue,
		Weight:        weight,
		Formula:       strings.TrimSpace(req.Formula),
		Version:       1,
	}

	// If a formula was provided, validate it compiles and pre-compute CurrentValue
	// from siblings so the metric isn't stuck at 0 until the next value update.
	if metric.Formula != "" {
		siblings, _ := s.goalRepo.ListMetricsByGoalID(ctx, goalID)
		if computed, ferr := metricformula.Evaluate(metric.Formula, metricSiblingsMap(siblings)); ferr != nil {
			return nil, fmt.Errorf("invalid formula: %w", ferr)
		} else {
			metric.CurrentValue = computed
		}
	}

	// Assign the metric workflow's initial state. Owners can see drafts but
	// view-only callers will be filtered out by ListMetrics until a transition
	// reaches the terminal-approved state.
	if wfs, wfErr := s.workflowRepo.ListByRecordType(ctx, "metric", true); wfErr == nil && len(wfs) > 0 {
		wf := wfs[0]
		if initial, ierr := s.workflowRepo.GetInitialState(ctx, wf.ID); ierr == nil && initial != nil {
			metric.WorkflowID = &wf.ID
			metric.CurrentStateID = &initial.ID
		}
	}

	if err := s.goalRepo.CreateMetric(ctx, metric); err != nil {
		return nil, fmt.Errorf("failed to create metric: %w", err)
	}

	// Recalculate goal progress
	if err := s.RecalculateProgress(ctx, goalID); err != nil {
		return nil, fmt.Errorf("failed to recalculate progress: %w", err)
	}

	// Audit log
	s.actionLogService.LogAction(ctx, &LogActionParams{
		UserID:      userID,
		Action:      "create",
		Module:      "goals",
		ResourceID:  metric.ID.String(),
		Description: fmt.Sprintf("Created metric '%s' for goal: %s", metric.Name, goal.Title),
		Status:      "success",
	})

	resp := metric.ToResponse()
	return &resp, nil
}

// ──────────────────────────────────────────────────
// 10. UpdateMetric
// ──────────────────────────────────────────────────

func (s *goalService) UpdateMetric(ctx context.Context, id uuid.UUID, req *models.GoalMetricUpdateRequest, userID uuid.UUID) (*models.GoalMetricResponse, error) {
	metric, err := s.goalRepo.FindMetricByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("metric not found: %w", err)
	}

	// Access check via parent goal
	canModify, _ := s.canModifyGoal(ctx, metric.GoalID, userID)
	if !canModify {
		return nil, fmt.Errorf("access denied: you can only modify goals you own or are assigned to review")
	}

	// Apply non-nil fields
	if req.Name != nil {
		metric.Name = *req.Name
	}
	if req.MetricType != nil {
		metric.MetricType = *req.MetricType
	}
	if req.Unit != nil {
		metric.Unit = *req.Unit
	}
	if req.BaselineValue != nil {
		metric.BaselineValue = *req.BaselineValue
	}
	if req.TargetValue != nil {
		metric.TargetValue = *req.TargetValue
	}
	if req.Weight != nil {
		metric.Weight = *req.Weight
	}
	if req.Formula != nil {
		metric.Formula = strings.TrimSpace(*req.Formula)
	}

	// If the (possibly new) formula is non-empty, validate and recompute now.
	if metric.Formula != "" {
		siblings, _ := s.goalRepo.ListMetricsByGoalID(ctx, metric.GoalID)
		// Exclude self so a formula can't read its own stale value.
		filtered := make([]models.GoalMetric, 0, len(siblings))
		for _, sib := range siblings {
			if sib.ID != metric.ID {
				filtered = append(filtered, sib)
			}
		}
		computed, ferr := metricformula.Evaluate(metric.Formula, metricSiblingsMap(filtered))
		if ferr != nil {
			return nil, fmt.Errorf("invalid formula: %w", ferr)
		}
		metric.CurrentValue = computed
	}

	if err := s.goalRepo.UpdateMetric(ctx, metric); err != nil {
		return nil, fmt.Errorf("failed to update metric: %w", err)
	}

	// Recalculate goal progress
	if err := s.RecalculateProgress(ctx, metric.GoalID); err != nil {
		return nil, fmt.Errorf("failed to recalculate progress: %w", err)
	}

	// Audit log
	s.actionLogService.LogAction(ctx, &LogActionParams{
		UserID:      userID,
		Action:      "update",
		Module:      "goals",
		ResourceID:  metric.ID.String(),
		Description: fmt.Sprintf("Updated metric: %s", metric.Name),
		Status:      "success",
	})

	resp := metric.ToResponse()
	return &resp, nil
}

// ──────────────────────────────────────────────────
// 11. DeleteMetric
// ──────────────────────────────────────────────────

func (s *goalService) DeleteMetric(ctx context.Context, id uuid.UUID, userID uuid.UUID) error {
	// Find metric to get goal ID
	metric, err := s.goalRepo.FindMetricByID(ctx, id)
	if err != nil {
		return fmt.Errorf("metric not found: %w", err)
	}

	// Access check via parent goal
	canModify, _ := s.canModifyGoal(ctx, metric.GoalID, userID)
	if !canModify {
		return fmt.Errorf("access denied: you can only modify goals you own or are assigned to review")
	}

	goalID := metric.GoalID

	if err := s.goalRepo.DeleteMetric(ctx, id); err != nil {
		return fmt.Errorf("failed to delete metric: %w", err)
	}

	// Recalculate goal progress
	if err := s.RecalculateProgress(ctx, goalID); err != nil {
		return fmt.Errorf("failed to recalculate progress: %w", err)
	}

	// Audit log
	s.actionLogService.LogAction(ctx, &LogActionParams{
		UserID:      userID,
		Action:      "delete",
		Module:      "goals",
		ResourceID:  id.String(),
		Description: fmt.Sprintf("Deleted metric: %s", metric.Name),
		Status:      "success",
	})

	return nil
}

// ──────────────────────────────────────────────────
// 12. UpdateMetricValue
// ──────────────────────────────────────────────────
//
// Per item #21, a value change is no longer mutated directly. Instead a
// GoalMetricValueChange is created and put through the metric_value_change
// approval workflow. The metric's CurrentValue is only updated when the
// value change reaches a terminal-approved state via TransitionMetricValueChange.
func (s *goalService) UpdateMetricValue(ctx context.Context, id uuid.UUID, req *models.MetricValueUpdateRequest, userID uuid.UUID) (*models.GoalMetricValueChangeResponse, error) {
	metric, err := s.goalRepo.FindMetricByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("metric not found: %w", err)
	}

	// Access check via parent goal
	canModify, _ := s.canModifyGoal(ctx, metric.GoalID, userID)
	if !canModify {
		return nil, fmt.Errorf("access denied: you can only modify goals you own or are assigned to review")
	}

	// If the metric has a formula, compute the proposed value from siblings —
	// the submitted raw value is ignored. Reviewers approve the computed value.
	newValue := req.Value
	if strings.TrimSpace(metric.Formula) != "" {
		siblings, _ := s.goalRepo.ListMetricsByGoalID(ctx, metric.GoalID)
		filtered := make([]models.GoalMetric, 0, len(siblings))
		for _, sib := range siblings {
			if sib.ID != metric.ID {
				filtered = append(filtered, sib)
			}
		}
		if computed, ferr := metricformula.Evaluate(metric.Formula, metricSiblingsMap(filtered)); ferr == nil {
			newValue = computed
		} else {
			log.Printf("[goal_service] UpdateMetricValue: formula %q on metric %s failed to evaluate (%v); falling back to submitted value",
				metric.Formula, metric.ID, ferr)
		}
	}

	// Resolve the metric_value_change workflow + initial state
	wfs, wfErr := s.workflowRepo.ListByRecordType(ctx, "metric_value_change", true)
	if wfErr != nil || len(wfs) == 0 {
		return nil, fmt.Errorf("no active metric value change workflow found")
	}
	wf := wfs[0]
	initial, ierr := s.workflowRepo.GetInitialState(ctx, wf.ID)
	if ierr != nil || initial == nil {
		return nil, fmt.Errorf("metric value change workflow has no initial state")
	}

	change := &models.GoalMetricValueChange{
		MetricID:       metric.ID,
		ProposedValue:  newValue,
		PreviousValue:  metric.CurrentValue,
		Comment:        req.Comment,
		SubmittedByID:  userID,
		WorkflowID:     wf.ID,
		CurrentStateID: initial.ID,
		Version:        1,
	}

	if err := s.goalRepo.CreateMetricValueChange(ctx, change); err != nil {
		return nil, fmt.Errorf("failed to create metric value change: %w", err)
	}

	// Audit log
	s.actionLogService.LogAction(ctx, &LogActionParams{
		UserID:      userID,
		Action:      "create",
		Module:      "goals",
		ResourceID:  change.ID.String(),
		Description: fmt.Sprintf("Proposed metric value change for '%s': %.2f -> %.2f (pending approval)", metric.Name, metric.CurrentValue, newValue),
		Status:      "success",
	})

	// Reload with relations
	reloaded, rerr := s.goalRepo.FindMetricValueChangeByIDWithRelations(ctx, change.ID)
	if rerr == nil {
		resp := reloaded.ToResponse()
		return &resp, nil
	}
	resp := change.ToResponse()
	return &resp, nil
}

// ──────────────────────────────────────────────────
// 13. GetMetricHistory
// ──────────────────────────────────────────────────

func (s *goalService) GetMetricHistory(ctx context.Context, metricID uuid.UUID, page int, limit int) ([]models.MetricHistoryResponse, int64, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}

	histories, total, err := s.goalRepo.ListMetricHistory(ctx, metricID, page, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list metric history: %w", err)
	}

	responses := make([]models.MetricHistoryResponse, len(histories))
	for i, h := range histories {
		responses[i] = h.ToResponse()
	}

	return responses, total, nil
}

// ──────────────────────────────────────────────────
// 14. CreateEvidence
// ──────────────────────────────────────────────────

func (s *goalService) CreateEvidence(ctx context.Context, goalID uuid.UUID, title string, evidenceType string, comment string, metricID *uuid.UUID, fileName string, fileSize int64, mimeType string, fileData []byte, userID uuid.UUID) (*models.EvidenceResponse, error) {
	// Access check
	canAccess, _ := s.canAccessGoal(ctx, goalID, userID)
	if !canAccess {
		return nil, fmt.Errorf("access denied")
	}

	// Validate comment is non-empty
	if strings.TrimSpace(comment) == "" {
		return nil, fmt.Errorf("comment is mandatory")
	}

	// Find goal to get DocumentaFolderID
	goal, err := s.goalRepo.FindByID(ctx, goalID)
	if err != nil {
		return nil, fmt.Errorf("goal not found: %w", err)
	}

	// Determine upload folder (metric subfolder if metricID provided)
	uploadFolderID := goal.DocumentaFolderID
	var metricName string
	if metricID != nil {
		metric, metricErr := s.goalRepo.FindMetricByID(ctx, *metricID)
		if metricErr == nil && metric != nil {
			metricName = metric.Name
			// Lazy-create metric subfolder under goal folder
			metricFolderID, ensureErr := s.documentaClient.EnsureFolder(
				ctx, s.cfg.Documenta.WorkspaceName, goal.DocumentaFolderID, metric.Name,
			)
			if ensureErr == nil {
				uploadFolderID = metricFolderID
			}
		}
	}

	// Resolve uploader info for metadata
	uploader, _ := s.userRepo.FindByID(ctx, userID)
	uploaderEmail := "unknown"
	if uploader != nil {
		uploaderEmail = uploader.Email
	}

	// Resolve department name
	departmentName := ""
	if goal.DepartmentID != nil {
		dept, _ := s.departmentRepo.FindByID(ctx, *goal.DepartmentID)
		if dept != nil {
			departmentName = dept.Name
		}
	}

	// Build full 11-tag metadata
	metadata := map[string]string{
		"goal_id":       goal.ID.String(),
		"goal_title":    goal.Title,
		"goal_priority": goal.Priority,
		"goal_status":   goal.Status,
		"evidence_type": evidenceType,
		"uploaded_by":   uploaderEmail,
		"uploaded_at":   time.Now().UTC().Format(time.RFC3339),
		"source_system": "automax",
		"metric_id":     "",
		"metric_name":   "",
		"department":    departmentName,
	}
	if metricID != nil {
		metadata["metric_id"] = metricID.String()
		metadata["metric_name"] = metricName
	}

	fileReader := bytes.NewReader(fileData)
	documentaFileID, err := s.documentaClient.UploadFile(ctx, uploadFolderID, fileName, fileReader, fileSize, metadata)
	if err != nil {
		return nil, fmt.Errorf("failed to upload file to documenta: %w", err)
	}

	// Persist metadata as searchable tags (upload-time metadata may not index)
	if tagErr := s.documentaClient.SetTags(ctx, documentaFileID, metadata); tagErr != nil {
		log.Printf("[goal_service] CreateEvidence: SetTags failed for file %s: %v", documentaFileID, tagErr)
	}

	// Resolve the evidence approval workflow and its initial state
	workflows, wfErr := s.workflowRepo.ListByRecordType(ctx, "evidence", true)
	if wfErr != nil || len(workflows) == 0 {
		return nil, fmt.Errorf("no active evidence approval workflow found")
	}
	wf := workflows[0]
	initialState, stateErr := s.workflowRepo.GetInitialState(ctx, wf.ID)
	if stateErr != nil || initialState == nil {
		return nil, fmt.Errorf("evidence workflow has no initial state")
	}

	evidence := &models.Evidence{
		GoalID:          goalID,
		MetricID:        metricID,
		Title:           title,
		EvidenceType:    evidenceType,
		Comment:         comment,
		Status:          models.EvidenceStatusDraft,
		DocumentaFileID: documentaFileID,
		FileName:        fileName,
		FileSize:        fileSize,
		MimeType:        mimeType,
		UploadedByID:    userID,
		WorkflowID:      &wf.ID,
		CurrentStateID:  &initialState.ID,
		Version:         1,
	}

	if err := s.goalRepo.CreateEvidence(ctx, evidence); err != nil {
		return nil, fmt.Errorf("failed to create evidence: %w", err)
	}

	// Audit log
	s.actionLogService.LogAction(ctx, &LogActionParams{
		UserID:      userID,
		Action:      "create",
		Module:      "goals",
		ResourceID:  evidence.ID.String(),
		Description: fmt.Sprintf("Created evidence '%s' for goal: %s", evidence.Title, goal.Title),
		Status:      "success",
	})

	// WebSocket broadcast
	if s.wsHub != nil {
		s.wsHub.BroadcastToGoal(goalID, "evidence_created", map[string]interface{}{
			"evidence_id": evidence.ID,
			"goal_id":     goalID,
			"title":       evidence.Title,
		}, userID)
	}

	// Reload with relations for response
	evidenceWithRelations, reloadErr := s.goalRepo.FindEvidenceByIDWithRelations(ctx, evidence.ID)
	if reloadErr != nil {
		resp := evidence.ToResponse()
		return &resp, nil
	}
	resp := evidenceWithRelations.ToResponse()
	return &resp, nil
}

// ──────────────────────────────────────────────────
// 15. ListEvidences
// ──────────────────────────────────────────────────

func (s *goalService) ListEvidences(ctx context.Context, goalID uuid.UUID, filter *models.EvidenceFilter, userID uuid.UUID) ([]models.EvidenceResponse, int64, error) {
	// Access check
	canAccess, _ := s.canAccessGoal(ctx, goalID, userID)
	if !canAccess {
		return nil, 0, fmt.Errorf("access denied")
	}

	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.Limit < 1 {
		filter.Limit = 10
	}

	// Visibility gate: view-only callers see only terminal-approved evidence.
	if !s.canSeeUnapprovedItems(ctx, goalID, userID) {
		filter.ApprovedOnly = true
	}

	evidences, total, err := s.goalRepo.ListEvidencesByGoalID(ctx, goalID, filter)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list evidences: %w", err)
	}

	responses := make([]models.EvidenceResponse, len(evidences))
	for i, e := range evidences {
		responses[i] = e.ToResponse()
	}

	return responses, total, nil
}

// ──────────────────────────────────────────────────
// 16. GetEvidence
// ──────────────────────────────────────────────────

func (s *goalService) GetEvidence(ctx context.Context, id uuid.UUID) (*models.EvidenceResponse, error) {
	evidence, err := s.goalRepo.FindEvidenceByIDWithRelations(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("evidence not found: %w", err)
	}

	resp := evidence.ToResponse()
	return &resp, nil
}

// ──────────────────────────────────────────────────
// 17. DeleteEvidence
// ──────────────────────────────────────────────────

func (s *goalService) DeleteEvidence(ctx context.Context, id uuid.UUID, userID uuid.UUID) error {
	evidence, err := s.goalRepo.FindEvidenceByIDWithRelations(ctx, id)
	if err != nil {
		return fmt.Errorf("evidence not found: %w", err)
	}

	// Block deletion only for Approved evidence
	if evidence.Status == models.EvidenceStatusApproved {
		return fmt.Errorf("approved evidence cannot be deleted (current: %s)", evidence.Status)
	}

	// Delete file from Documenta
	if evidence.DocumentaFileID != "" {
		if delErr := s.documentaClient.DeleteFile(ctx, evidence.DocumentaFileID); delErr != nil {
			log.Printf("Warning: failed to delete file from Documenta: %v", delErr)
		}
	}

	if err := s.goalRepo.DeleteEvidence(ctx, id); err != nil {
		return fmt.Errorf("failed to delete evidence: %w", err)
	}

	s.actionLogService.LogAction(ctx, &LogActionParams{
		UserID:      userID,
		Action:      "delete",
		Module:      "goals",
		ResourceID:  id.String(),
		Description: fmt.Sprintf("Deleted evidence '%s'", evidence.Title),
		Status:      "success",
	})

	return nil
}

// ──────────────────────────────────────────────────
// 17b. ReplaceEvidenceFile
// ──────────────────────────────────────────────────

func (s *goalService) ReplaceEvidenceFile(ctx context.Context, evidenceID uuid.UUID, fileName string, fileSize int64, mimeType string, fileData []byte, userID uuid.UUID) (*models.EvidenceResponse, error) {
	evidence, err := s.goalRepo.FindEvidenceByIDWithRelations(ctx, evidenceID)
	if err != nil {
		return nil, fmt.Errorf("evidence not found: %w", err)
	}

	// Only allow replacement in Draft or Changes_Requested state
	allowedCodes := map[string]bool{"draft": true, "changes_requested": true}
	if evidence.CurrentState == nil || !allowedCodes[evidence.CurrentState.Code] {
		return nil, fmt.Errorf("evidence file can only be replaced when in Draft or Changes Requested state")
	}

	// Delete old file from Documenta
	if evidence.DocumentaFileID != "" {
		if delErr := s.documentaClient.DeleteFile(ctx, evidence.DocumentaFileID); delErr != nil {
			log.Printf("Warning: failed to delete old file from Documenta: %v", delErr)
		}
	}

	// Get goal for folder ID
	goal, err := s.goalRepo.FindByID(ctx, evidence.GoalID)
	if err != nil {
		return nil, fmt.Errorf("goal not found: %w", err)
	}

	// Determine upload folder (metric subfolder if evidence has metricID)
	uploadFolderID := goal.DocumentaFolderID
	var metricName string
	if evidence.MetricID != nil {
		metric, metricErr := s.goalRepo.FindMetricByID(ctx, *evidence.MetricID)
		if metricErr == nil && metric != nil {
			metricName = metric.Name
			metricFolderID, ensureErr := s.documentaClient.EnsureFolder(
				ctx, s.cfg.Documenta.WorkspaceName, goal.DocumentaFolderID, metric.Name,
			)
			if ensureErr == nil {
				uploadFolderID = metricFolderID
			}
		}
	}

	// Resolve uploader info
	uploader, _ := s.userRepo.FindByID(ctx, userID)
	uploaderEmail := "unknown"
	if uploader != nil {
		uploaderEmail = uploader.Email
	}

	// Resolve department name
	departmentName := ""
	if goal.DepartmentID != nil {
		dept, _ := s.departmentRepo.FindByID(ctx, *goal.DepartmentID)
		if dept != nil {
			departmentName = dept.Name
		}
	}

	// Build full 11-tag metadata
	metadata := map[string]string{
		"goal_id":       goal.ID.String(),
		"goal_title":    goal.Title,
		"goal_priority": goal.Priority,
		"goal_status":   goal.Status,
		"evidence_type": evidence.EvidenceType,
		"uploaded_by":   uploaderEmail,
		"uploaded_at":   time.Now().UTC().Format(time.RFC3339),
		"source_system": "automax",
		"metric_id":     "",
		"metric_name":   "",
		"department":    departmentName,
	}
	if evidence.MetricID != nil {
		metadata["metric_id"] = evidence.MetricID.String()
		metadata["metric_name"] = metricName
	}

	fileReader := bytes.NewReader(fileData)
	newFileID, err := s.documentaClient.UploadFile(ctx, uploadFolderID, fileName, fileReader, fileSize, metadata)
	if err != nil {
		return nil, fmt.Errorf("failed to upload replacement file: %w", err)
	}

	// Persist metadata as searchable tags (upload-time metadata may not index)
	if tagErr := s.documentaClient.SetTags(ctx, newFileID, metadata); tagErr != nil {
		log.Printf("[goal_service] ReplaceEvidenceFile: SetTags failed for file %s: %v", newFileID, tagErr)
	}

	// Update evidence record
	evidence.DocumentaFileID = newFileID
	evidence.FileName = fileName
	evidence.FileSize = fileSize
	evidence.MimeType = mimeType
	evidence.Version++

	if err := s.goalRepo.UpdateEvidence(ctx, evidence); err != nil {
		return nil, fmt.Errorf("failed to update evidence: %w", err)
	}

	s.actionLogService.LogAction(ctx, &LogActionParams{
		UserID:      userID,
		Action:      "update",
		Module:      "goals",
		ResourceID:  evidenceID.String(),
		Description: fmt.Sprintf("Replaced evidence file '%s' with '%s'", evidence.Title, fileName),
		Status:      "success",
	})

	// Reload with relations
	updated, reloadErr := s.goalRepo.FindEvidenceByIDWithRelations(ctx, evidenceID)
	if reloadErr != nil {
		resp := evidence.ToResponse()
		return &resp, nil
	}
	resp := updated.ToResponse()
	return &resp, nil
}

// ──────────────────────────────────────────────────
// 18. GetAvailableEvidenceTransitions
// ──────────────────────────────────────────────────

func (s *goalService) GetAvailableEvidenceTransitions(ctx context.Context, evidenceID uuid.UUID, userID uuid.UUID) ([]models.AvailableTransitionResponse, error) {
	evidence, err := s.goalRepo.FindEvidenceByIDWithRelations(ctx, evidenceID)
	if err != nil {
		return nil, fmt.Errorf("evidence not found: %w", err)
	}

	if evidence.CurrentStateID == nil || evidence.WorkflowID == nil {
		return nil, fmt.Errorf("evidence has no workflow assigned")
	}

	// Get all transitions from current state
	transitions, err := s.workflowRepo.ListTransitionsFromState(ctx, *evidence.CurrentStateID)
	if err != nil {
		return nil, fmt.Errorf("failed to get transitions: %w", err)
	}

	// Get collaborators to determine L2 reviewer existence
	collaborators, err := s.goalRepo.ListCollaborators(ctx, evidence.GoalID)
	if err != nil {
		return nil, fmt.Errorf("failed to list collaborators: %w", err)
	}

	var hasL2Reviewer bool
	for _, c := range collaborators {
		if c.Role == models.CollaboratorRoleReviewerL2 {
			hasL2Reviewer = true
			break
		}
	}

	var results []models.AvailableTransitionResponse
	for _, t := range transitions {
		canExecute := true
		reason := ""

		// Load requirements
		requirements, _ := s.workflowRepo.GetTransitionRequirements(ctx, t.ID)

		switch t.Code {
		case "submit", "resubmit":
			// Only the evidence uploader or goal owner can submit/resubmit
			goal, _ := s.goalRepo.FindByID(ctx, evidence.GoalID)
			if evidence.UploadedByID != userID && (goal == nil || goal.OwnerID != userID) {
				canExecute = false
				reason = "Only the uploader or goal owner can submit"
			}
		case "approve_l1":
			// Only show if L2 reviewer exists; only assigned reviewer can execute
			if !hasL2Reviewer {
				continue // skip this transition, show approve_l1_final instead
			}
			if evidence.AssignedToID == nil || *evidence.AssignedToID != userID {
				canExecute = false
				reason = "Only the assigned reviewer can approve"
			}
		case "approve_l1_final":
			// Only show if NO L2 reviewer; only assigned reviewer can execute
			if hasL2Reviewer {
				continue // skip this transition, show approve_l1 instead
			}
			if evidence.AssignedToID == nil || *evidence.AssignedToID != userID {
				canExecute = false
				reason = "Only the assigned reviewer can approve"
			}
		case "request_changes_l1", "reject_l1", "request_changes_l2", "reject_l2":
			// Only assigned reviewer can reject/request changes
			if evidence.AssignedToID == nil || *evidence.AssignedToID != userID {
				canExecute = false
				reason = "Only the assigned reviewer can perform this action"
			}
		case "approve_l2":
			if evidence.AssignedToID == nil || *evidence.AssignedToID != userID {
				canExecute = false
				reason = "Only the assigned L2 reviewer can approve"
			}
		case "assign_l1":
			// System transition — not available to users
			continue
		}

		// Build requirements response
		reqResponses := make([]models.TransitionRequirementResponse, len(requirements))
		for i, r := range requirements {
			reqResponses[i] = models.ToTransitionRequirementResponse(&r)
		}

		results = append(results, models.AvailableTransitionResponse{
			Transition:   models.ToWorkflowTransitionResponse(&t),
			CanExecute:   canExecute,
			Requirements: reqResponses,
			Reason:       reason,
		})
	}

	return results, nil
}

// ──────────────────────────────────────────────────
// 19. ExecuteEvidenceTransition
// ──────────────────────────────────────────────────

func (s *goalService) ExecuteEvidenceTransition(ctx context.Context, evidenceID uuid.UUID, req *models.EvidenceTransitionRequest, userID uuid.UUID) (*models.EvidenceResponse, error) {
	transitionID, err := uuid.Parse(req.TransitionID)
	if err != nil {
		return nil, fmt.Errorf("invalid transition_id: %w", err)
	}

	// Begin transaction
	tx := s.goalRepo.BeginTx(ctx)
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	// Pessimistic lock on evidence
	evidence, err := s.goalRepo.FindEvidenceByIDForUpdate(ctx, tx, evidenceID)
	if err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("evidence not found: %w", err)
	}

	// Optimistic concurrency check
	if evidence.Version != req.Version {
		tx.Rollback()
		return nil, fmt.Errorf("evidence has been modified by another user (expected version %d, got %d)", evidence.Version, req.Version)
	}

	if evidence.CurrentStateID == nil || evidence.WorkflowID == nil {
		tx.Rollback()
		return nil, fmt.Errorf("evidence has no workflow assigned")
	}

	// Fetch transition with relations
	transition, err := s.workflowRepo.FindTransitionByIDWithRelations(ctx, transitionID)
	if err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("transition not found: %w", err)
	}

	// Validate transition belongs to evidence's workflow
	if transition.WorkflowID != *evidence.WorkflowID {
		tx.Rollback()
		return nil, fmt.Errorf("transition does not belong to evidence's workflow")
	}

	// Validate transition's from_state matches evidence's current state
	if transition.FromStateID != *evidence.CurrentStateID {
		tx.Rollback()
		return nil, fmt.Errorf("transition is not valid from current state")
	}

	// Authorization check
	switch transition.Code {
	case "submit", "resubmit":
		goal, _ := s.goalRepo.FindByID(ctx, evidence.GoalID)
		if evidence.UploadedByID != userID && (goal == nil || goal.OwnerID != userID) {
			tx.Rollback()
			return nil, fmt.Errorf("only the uploader or goal owner can submit evidence")
		}
	case "approve_l1", "approve_l1_final", "request_changes_l1", "reject_l1", "approve_l2", "request_changes_l2", "reject_l2":
		if evidence.AssignedToID == nil || *evidence.AssignedToID != userID {
			tx.Rollback()
			return nil, fmt.Errorf("only the assigned reviewer can perform this action")
		}
	}

	// Validate requirements (comment required for reject/request_changes)
	requirements, _ := s.workflowRepo.GetTransitionRequirements(ctx, transitionID)
	for _, r := range requirements {
		if r.RequirementType == "comment" && r.IsMandatory != nil && *r.IsMandatory {
			if strings.TrimSpace(req.Comment) == "" {
				tx.Rollback()
				errMsg := r.ErrorMessage
				if errMsg == "" {
					errMsg = "Comment is required for this transition"
				}
				return nil, fmt.Errorf("%s", errMsg)
			}
		}
	}

	fromStateID := *evidence.CurrentStateID

	// Create transition history record
	now := time.Now()
	history := &models.EvidenceTransitionHistory{
		EvidenceID:     evidenceID,
		TransitionID:   &transitionID,
		FromStateID:    fromStateID,
		ToStateID:      transition.ToStateID,
		PerformedByID:  userID,
		Comment:        req.Comment,
		IsSystemAction: false,
		TransitionedAt: now,
	}
	if err := s.goalRepo.CreateTransitionHistory(ctx, tx, history); err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("failed to create transition history: %w", err)
	}

	// Update evidence state
	evidence.CurrentStateID = &transition.ToStateID
	evidence.Version++

	// Get the target state to update the Status string for backward compat
	targetState, _ := s.workflowRepo.FindStateByID(ctx, transition.ToStateID)
	if targetState != nil {
		switch targetState.Code {
		case "draft":
			evidence.Status = models.EvidenceStatusDraft
		case "submitted":
			evidence.Status = models.EvidenceStatusSubmitted
		case "l1_review", "l2_review":
			evidence.Status = models.EvidenceStatusInReview
		case "approved":
			evidence.Status = models.EvidenceStatusApproved
		case "rejected":
			evidence.Status = models.EvidenceStatusRejected
		case "changes_requested":
			evidence.Status = models.EvidenceStatusChangesRequested
		}
	}

	// Assignment logic based on target state
	collaborators, _ := s.goalRepo.ListCollaborators(ctx, evidence.GoalID)

	switch transition.Code {
	case "submit", "resubmit":
		// Auto-transition to l1_review: find L1 reviewer and assign
		var reviewerL1 *models.GoalCollaborator
		for i := range collaborators {
			if collaborators[i].Role == models.CollaboratorRoleReviewerL1 {
				reviewerL1 = &collaborators[i]
				break
			}
		}
		if reviewerL1 == nil {
			tx.Rollback()
			return nil, fmt.Errorf("no L1 reviewer assigned to this goal")
		}

		// Find the l1_review state to auto-advance
		l1State, _ := s.workflowRepo.FindStateByCode(ctx, *evidence.WorkflowID, "l1_review")
		if l1State != nil {
			// Create system transition history for the auto-advance
			autoHistory := &models.EvidenceTransitionHistory{
				EvidenceID:     evidenceID,
				FromStateID:    transition.ToStateID,
				ToStateID:      l1State.ID,
				PerformedByID:  userID,
				Comment:        "Auto-assigned to L1 reviewer",
				IsSystemAction: true,
				TransitionedAt: now,
			}
			s.goalRepo.CreateTransitionHistory(ctx, tx, autoHistory)

			evidence.CurrentStateID = &l1State.ID
			evidence.Status = models.EvidenceStatusInReview
		}

		evidence.AssignedToID = &reviewerL1.UserID

	case "approve_l1":
		// L1 approved → move to L2 review
		var reviewerL2 *models.GoalCollaborator
		for i := range collaborators {
			if collaborators[i].Role == models.CollaboratorRoleReviewerL2 {
				reviewerL2 = &collaborators[i]
				break
			}
		}
		if reviewerL2 != nil {
			evidence.AssignedToID = &reviewerL2.UserID
		}

	case "approve_l1_final", "approve_l2":
		// Final approval → clear assignment, recalculate progress
		evidence.AssignedToID = nil

	case "request_changes_l1", "request_changes_l2", "reject_l1", "reject_l2":
		// Clear assignment
		evidence.AssignedToID = nil
	}

	// Save evidence
	if err := s.goalRepo.UpdateEvidenceInTx(ctx, tx, evidence); err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("failed to update evidence: %w", err)
	}

	// Commit
	if err := tx.Commit().Error; err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Post-commit actions (notifications, progress recalc)
	goal, _ := s.goalRepo.FindByID(ctx, evidence.GoalID)
	goalTitle := "a goal"
	if goal != nil {
		goalTitle = goal.Title
	}

	switch transition.Code {
	case "submit", "resubmit":
		// Notify reviewer
		if evidence.AssignedToID != nil {
			reviewer, rErr := s.userRepo.FindByID(ctx, *evidence.AssignedToID)
			if rErr == nil && reviewer != nil {
				s.notificationService.SendNotification(ctx, "notification", nil, "en",
					[]string{reviewer.Email}, nil, nil,
					"Evidence Submitted for Review",
					fmt.Sprintf("Evidence '%s' has been submitted for your review on goal: %s", evidence.Title, goalTitle),
					nil, nil, &userID, nil)
			}
		}

	case "approve_l1":
		// Notify L2 reviewer
		if evidence.AssignedToID != nil {
			reviewer, rErr := s.userRepo.FindByID(ctx, *evidence.AssignedToID)
			if rErr == nil && reviewer != nil {
				s.notificationService.SendNotification(ctx, "notification", nil, "en",
					[]string{reviewer.Email}, nil, nil,
					"Evidence Pending L2 Review",
					fmt.Sprintf("Evidence '%s' for goal '%s' requires your L2 review", evidence.Title, goalTitle),
					nil, nil, &userID, nil)
			}
		}

	case "approve_l1_final", "approve_l2":
		// Recalculate goal progress
		s.RecalculateProgress(ctx, evidence.GoalID)
		// Notify submitter
		submitter, sErr := s.userRepo.FindByID(ctx, evidence.UploadedByID)
		if sErr == nil && submitter != nil {
			s.notificationService.SendNotification(ctx, "notification", nil, "en",
				[]string{submitter.Email}, nil, nil,
				"Evidence Approved",
				fmt.Sprintf("Your evidence '%s' for goal '%s' has been approved", evidence.Title, goalTitle),
				nil, nil, &userID, nil)
		}

	case "reject_l1", "reject_l2":
		// Notify submitter
		submitter, sErr := s.userRepo.FindByID(ctx, evidence.UploadedByID)
		if sErr == nil && submitter != nil {
			s.notificationService.SendNotification(ctx, "notification", nil, "en",
				[]string{submitter.Email}, nil, nil,
				"Evidence Rejected",
				fmt.Sprintf("Your evidence '%s' for goal '%s' has been rejected. Comment: %s", evidence.Title, goalTitle, req.Comment),
				nil, nil, &userID, nil)
		}

	case "request_changes_l1", "request_changes_l2":
		// Notify submitter
		submitter, sErr := s.userRepo.FindByID(ctx, evidence.UploadedByID)
		if sErr == nil && submitter != nil {
			s.notificationService.SendNotification(ctx, "notification", nil, "en",
				[]string{submitter.Email}, nil, nil,
				"Evidence Returned for Changes",
				fmt.Sprintf("Your evidence '%s' for goal '%s' has been returned for changes. Comment: %s", evidence.Title, goalTitle, req.Comment),
				nil, nil, &userID, nil)
		}
	}

	// Audit log
	transitionName := transition.Name
	s.actionLogService.LogAction(ctx, &LogActionParams{
		UserID:      userID,
		Action:      "transition",
		Module:      "goals",
		ResourceID:  evidenceID.String(),
		Description: fmt.Sprintf("Executed '%s' on evidence '%s' for goal: %s", transitionName, evidence.Title, goalTitle),
		Status:      "success",
	})

	// WebSocket broadcast
	if s.wsHub != nil {
		wsData := map[string]interface{}{
			"evidence_id": evidenceID,
			"goal_id":     evidence.GoalID,
			"status":      evidence.Status,
			"transition":  transitionName,
		}
		s.wsHub.BroadcastToGoal(evidence.GoalID, "evidence_transitioned", wsData, userID)
		s.wsHub.BroadcastGoalToAll("evidence_transitioned", wsData)
	}

	// Reload with relations
	evidenceWithRelations, reloadErr := s.goalRepo.FindEvidenceByIDWithRelations(ctx, evidenceID)
	if reloadErr != nil {
		resp := evidence.ToResponse()
		return &resp, nil
	}
	resp := evidenceWithRelations.ToResponse()
	return &resp, nil
}

// ──────────────────────────────────────────────────
// 20. GetEvidenceTransitionHistory
// ──────────────────────────────────────────────────

func (s *goalService) GetEvidenceTransitionHistory(ctx context.Context, evidenceID uuid.UUID) ([]models.EvidenceTransitionHistoryResponse, error) {
	histories, err := s.goalRepo.ListTransitionHistory(ctx, evidenceID)
	if err != nil {
		return nil, fmt.Errorf("failed to get transition history: %w", err)
	}

	responses := make([]models.EvidenceTransitionHistoryResponse, len(histories))
	for i, h := range histories {
		responses[i] = h.ToResponse()
	}

	return responses, nil
}

// ──────────────────────────────────────────────────
// 21. ListPendingApprovals
// ──────────────────────────────────────────────────

func (s *goalService) ListPendingApprovals(ctx context.Context, userID uuid.UUID, page int, limit int) ([]models.ApprovalListResponse, int64, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 10
	}

	evidences, total, err := s.goalRepo.ListPendingApprovals(ctx, userID, page, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list pending approvals: %w", err)
	}

	responses := make([]models.ApprovalListResponse, len(evidences))
	for i, ev := range evidences {
		resp := models.ApprovalListResponse{
			ID:              ev.ID,
			EvidenceID:      ev.ID,
			EvidenceTitle:   ev.Title,
			EvidenceVersion: ev.Version,
			Status:          ev.Status,
			AssignedTo:      models.ToUserBriefResponse(ev.AssignedTo),
			SubmittedBy:     models.ToUserBriefResponse(ev.UploadedBy),
			CreatedAt:       ev.CreatedAt,
			UpdatedAt:       ev.UpdatedAt,
		}

		if ev.CurrentState != nil {
			resp.StateName = ev.CurrentState.Name
			resp.StateColor = ev.CurrentState.Color
		}

		if ev.Goal != nil {
			resp.GoalID = ev.Goal.ID
			resp.GoalTitle = ev.Goal.Title
			resp.GoalPriority = ev.Goal.Priority
		}

		responses[i] = resp
	}

	return responses, total, nil
}

// ──────────────────────────────────────────────────
// 22. ListCompletedApprovals
// ──────────────────────────────────────────────────

func (s *goalService) ListCompletedApprovals(ctx context.Context, userID uuid.UUID, page int, limit int) ([]models.ApprovalListResponse, int64, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 10
	}

	histories, total, err := s.goalRepo.ListCompletedApprovals(ctx, userID, page, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list completed approvals: %w", err)
	}

	responses := make([]models.ApprovalListResponse, len(histories))
	for i, h := range histories {
		resp := models.ApprovalListResponse{
			ID:        h.ID,
			CreatedAt: h.CreatedAt,
			UpdatedAt: h.TransitionedAt,
		}

		if h.PerformedBy != nil {
			resp.SubmittedBy = models.ToUserBriefResponse(h.PerformedBy)
		}

		if h.ToState != nil {
			resp.StateName = h.ToState.Name
			resp.StateColor = h.ToState.Color
			resp.Status = h.ToState.Name
		}

		if h.Evidence != nil {
			resp.EvidenceID = h.Evidence.ID
			resp.EvidenceTitle = h.Evidence.Title
			resp.AssignedTo = models.ToUserBriefResponse(h.Evidence.AssignedTo)

			if h.Evidence.Goal != nil {
				resp.GoalID = h.Evidence.Goal.ID
				resp.GoalTitle = h.Evidence.Goal.Title
				resp.GoalPriority = h.Evidence.Goal.Priority
			}
		}

		responses[i] = resp
	}

	return responses, total, nil
}

// ════════════════════════════════════════════════════
// METRIC APPROVAL WORKFLOW
// ════════════════════════════════════════════════════

// GetAvailableMetricTransitions mirrors GetAvailableEvidenceTransitions for metrics.
func (s *goalService) GetAvailableMetricTransitions(ctx context.Context, metricID uuid.UUID, userID uuid.UUID) ([]models.AvailableTransitionResponse, error) {
	metric, err := s.goalRepo.FindMetricByIDWithRelations(ctx, metricID)
	if err != nil {
		return nil, fmt.Errorf("metric not found: %w", err)
	}

	if metric.CurrentStateID == nil || metric.WorkflowID == nil {
		return nil, fmt.Errorf("metric has no workflow assigned")
	}

	transitions, err := s.workflowRepo.ListTransitionsFromState(ctx, *metric.CurrentStateID)
	if err != nil {
		return nil, fmt.Errorf("failed to get transitions: %w", err)
	}

	collaborators, err := s.goalRepo.ListCollaborators(ctx, metric.GoalID)
	if err != nil {
		return nil, fmt.Errorf("failed to list collaborators: %w", err)
	}

	var hasL2Reviewer bool
	for _, c := range collaborators {
		if c.Role == models.CollaboratorRoleReviewerL2 {
			hasL2Reviewer = true
			break
		}
	}

	results := s.buildAvailableTransitions(ctx, transitions, metric.GoalID, metric.AssignedToID, userID, hasL2Reviewer)
	return results, nil
}

// GetAvailableMetricValueChangeTransitions mirrors GetAvailableEvidenceTransitions for value changes.
func (s *goalService) GetAvailableMetricValueChangeTransitions(ctx context.Context, changeID uuid.UUID, userID uuid.UUID) ([]models.AvailableTransitionResponse, error) {
	change, err := s.goalRepo.FindMetricValueChangeByIDWithRelations(ctx, changeID)
	if err != nil {
		return nil, fmt.Errorf("metric value change not found: %w", err)
	}
	if change.Metric == nil {
		return nil, fmt.Errorf("orphaned metric value change")
	}

	transitions, err := s.workflowRepo.ListTransitionsFromState(ctx, change.CurrentStateID)
	if err != nil {
		return nil, fmt.Errorf("failed to get transitions: %w", err)
	}

	collaborators, err := s.goalRepo.ListCollaborators(ctx, change.Metric.GoalID)
	if err != nil {
		return nil, fmt.Errorf("failed to list collaborators: %w", err)
	}

	var hasL2Reviewer bool
	for _, c := range collaborators {
		if c.Role == models.CollaboratorRoleReviewerL2 {
			hasL2Reviewer = true
			break
		}
	}

	results := s.buildAvailableTransitions(ctx, transitions, change.Metric.GoalID, change.AssignedToID, userID, hasL2Reviewer)
	return results, nil
}

// buildAvailableTransitions implements the same gating logic used for evidence
// (submit/approve_l1/approve_l2/etc.) for any record participating in the
// shared 7-state workflow.
func (s *goalService) buildAvailableTransitions(ctx context.Context, transitions []models.WorkflowTransition, goalID uuid.UUID, assignedToID *uuid.UUID, userID uuid.UUID, hasL2Reviewer bool) []models.AvailableTransitionResponse {
	var results []models.AvailableTransitionResponse
	for _, t := range transitions {
		canExecute := true
		reason := ""

		requirements, _ := s.workflowRepo.GetTransitionRequirements(ctx, t.ID)

		switch t.Code {
		case "submit", "resubmit":
			goal, _ := s.goalRepo.FindByID(ctx, goalID)
			if goal == nil || (goal.OwnerID != userID && !s.isSuperAdmin(ctx)) {
				canExecute = false
				reason = "Only the goal owner can submit"
			}
		case "approve_l1":
			if !hasL2Reviewer {
				continue
			}
			if assignedToID == nil || *assignedToID != userID {
				canExecute = false
				reason = "Only the assigned reviewer can approve"
			}
		case "approve_l1_final":
			if hasL2Reviewer {
				continue
			}
			if assignedToID == nil || *assignedToID != userID {
				canExecute = false
				reason = "Only the assigned reviewer can approve"
			}
		case "request_changes_l1", "reject_l1", "request_changes_l2", "reject_l2":
			if assignedToID == nil || *assignedToID != userID {
				canExecute = false
				reason = "Only the assigned reviewer can perform this action"
			}
		case "approve_l2":
			if assignedToID == nil || *assignedToID != userID {
				canExecute = false
				reason = "Only the assigned L2 reviewer can approve"
			}
		case "assign_l1":
			continue
		}

		reqResponses := make([]models.TransitionRequirementResponse, len(requirements))
		for i, r := range requirements {
			reqResponses[i] = models.ToTransitionRequirementResponse(&r)
		}

		results = append(results, models.AvailableTransitionResponse{
			Transition:   models.ToWorkflowTransitionResponse(&t),
			CanExecute:   canExecute,
			Requirements: reqResponses,
			Reason:       reason,
		})
	}
	return results
}

// TransitionMetric runs a workflow transition on a metric definition.
func (s *goalService) TransitionMetric(ctx context.Context, metricID uuid.UUID, req *models.MetricTransitionRequest, userID uuid.UUID) (*models.GoalMetricResponse, error) {
	transitionID, err := uuid.Parse(req.TransitionID)
	if err != nil {
		return nil, fmt.Errorf("invalid transition_id: %w", err)
	}

	tx := s.goalRepo.BeginTx(ctx)
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	metric, err := s.goalRepo.FindMetricByIDForUpdate(ctx, tx, metricID)
	if err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("metric not found: %w", err)
	}

	if req.Version > 0 && metric.Version != req.Version {
		tx.Rollback()
		return nil, fmt.Errorf("metric has been modified by another user (expected version %d, got %d)", metric.Version, req.Version)
	}

	if metric.CurrentStateID == nil || metric.WorkflowID == nil {
		tx.Rollback()
		return nil, fmt.Errorf("metric has no workflow assigned")
	}

	transition, err := s.workflowRepo.FindTransitionByIDWithRelations(ctx, transitionID)
	if err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("transition not found: %w", err)
	}

	if transition.WorkflowID != *metric.WorkflowID {
		tx.Rollback()
		return nil, fmt.Errorf("transition does not belong to metric's workflow")
	}
	if transition.FromStateID != *metric.CurrentStateID {
		tx.Rollback()
		return nil, fmt.Errorf("transition is not valid from current state")
	}

	// Authorization
	switch transition.Code {
	case "submit", "resubmit":
		goal, _ := s.goalRepo.FindByID(ctx, metric.GoalID)
		if !s.isSuperAdmin(ctx) && (goal == nil || goal.OwnerID != userID) {
			tx.Rollback()
			return nil, fmt.Errorf("only the goal owner can submit")
		}
	case "approve_l1", "approve_l1_final", "request_changes_l1", "reject_l1", "approve_l2", "request_changes_l2", "reject_l2":
		if metric.AssignedToID == nil || *metric.AssignedToID != userID {
			tx.Rollback()
			return nil, fmt.Errorf("only the assigned reviewer can perform this action")
		}
	}

	// Validate requirements (mandatory comment for reject/request_changes)
	requirements, _ := s.workflowRepo.GetTransitionRequirements(ctx, transitionID)
	for _, r := range requirements {
		if r.RequirementType == "comment" && r.IsMandatory != nil && *r.IsMandatory {
			if strings.TrimSpace(req.Comment) == "" {
				tx.Rollback()
				errMsg := r.ErrorMessage
				if errMsg == "" {
					errMsg = "Comment is required for this transition"
				}
				return nil, fmt.Errorf("%s", errMsg)
			}
		}
	}

	fromStateID := *metric.CurrentStateID
	now := time.Now()

	history := &models.MetricTransitionHistory{
		MetricID:       metricID,
		TransitionID:   &transitionID,
		FromStateID:    fromStateID,
		ToStateID:      transition.ToStateID,
		PerformedByID:  userID,
		Comment:        req.Comment,
		IsSystemAction: false,
		TransitionedAt: now,
	}
	if err := s.goalRepo.CreateMetricTransitionHistory(ctx, tx, history); err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("failed to create transition history: %w", err)
	}

	metric.CurrentStateID = &transition.ToStateID
	metric.Version++

	collaborators, _ := s.goalRepo.ListCollaborators(ctx, metric.GoalID)
	switch transition.Code {
	case "submit", "resubmit":
		var reviewerL1 *models.GoalCollaborator
		for i := range collaborators {
			if collaborators[i].Role == models.CollaboratorRoleReviewerL1 {
				reviewerL1 = &collaborators[i]
				break
			}
		}
		if reviewerL1 == nil {
			tx.Rollback()
			return nil, fmt.Errorf("no L1 reviewer assigned to this goal")
		}
		l1State, _ := s.workflowRepo.FindStateByCode(ctx, *metric.WorkflowID, "l1_review")
		if l1State != nil {
			autoHistory := &models.MetricTransitionHistory{
				MetricID:       metricID,
				FromStateID:    transition.ToStateID,
				ToStateID:      l1State.ID,
				PerformedByID:  userID,
				Comment:        "Auto-assigned to L1 reviewer",
				IsSystemAction: true,
				TransitionedAt: now,
			}
			s.goalRepo.CreateMetricTransitionHistory(ctx, tx, autoHistory)
			metric.CurrentStateID = &l1State.ID
		}
		metric.AssignedToID = &reviewerL1.UserID
	case "approve_l1":
		var reviewerL2 *models.GoalCollaborator
		for i := range collaborators {
			if collaborators[i].Role == models.CollaboratorRoleReviewerL2 {
				reviewerL2 = &collaborators[i]
				break
			}
		}
		if reviewerL2 != nil {
			metric.AssignedToID = &reviewerL2.UserID
		}
	case "approve_l1_final", "approve_l2":
		metric.AssignedToID = nil
	case "request_changes_l1", "request_changes_l2", "reject_l1", "reject_l2":
		metric.AssignedToID = nil
	}

	if err := s.goalRepo.UpdateMetricInTx(ctx, tx, metric); err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("failed to update metric: %w", err)
	}

	if err := tx.Commit().Error; err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Audit log
	s.actionLogService.LogAction(ctx, &LogActionParams{
		UserID:      userID,
		Action:      "transition",
		Module:      "goals",
		ResourceID:  metricID.String(),
		Description: fmt.Sprintf("Executed '%s' on metric '%s'", transition.Name, metric.Name),
		Status:      "success",
	})

	if s.wsHub != nil {
		s.wsHub.BroadcastToGoal(metric.GoalID, "metric_transitioned", map[string]interface{}{
			"metric_id":  metricID,
			"goal_id":    metric.GoalID,
			"transition": transition.Name,
		}, userID)
	}

	reloaded, rerr := s.goalRepo.FindMetricByIDWithRelations(ctx, metricID)
	if rerr == nil {
		resp := reloaded.ToResponse()
		return &resp, nil
	}
	resp := metric.ToResponse()
	return &resp, nil
}

// TransitionMetricValueChange runs a workflow transition on a value change.
// On a transition into a terminal-approved state the parent metric's
// CurrentValue is updated and AppliedAt is set.
func (s *goalService) TransitionMetricValueChange(ctx context.Context, changeID uuid.UUID, req *models.MetricTransitionRequest, userID uuid.UUID) (*models.GoalMetricValueChangeResponse, error) {
	transitionID, err := uuid.Parse(req.TransitionID)
	if err != nil {
		return nil, fmt.Errorf("invalid transition_id: %w", err)
	}

	tx := s.goalRepo.BeginTx(ctx)
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	change, err := s.goalRepo.FindMetricValueChangeByIDForUpdate(ctx, tx, changeID)
	if err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("metric value change not found: %w", err)
	}

	if req.Version > 0 && change.Version != req.Version {
		tx.Rollback()
		return nil, fmt.Errorf("value change has been modified by another user (expected version %d, got %d)", change.Version, req.Version)
	}

	transition, err := s.workflowRepo.FindTransitionByIDWithRelations(ctx, transitionID)
	if err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("transition not found: %w", err)
	}
	if transition.WorkflowID != change.WorkflowID {
		tx.Rollback()
		return nil, fmt.Errorf("transition does not belong to value change's workflow")
	}
	if transition.FromStateID != change.CurrentStateID {
		tx.Rollback()
		return nil, fmt.Errorf("transition is not valid from current state")
	}

	// Need parent metric for goal/owner lookups + applying value
	metric, mErr := s.goalRepo.FindMetricByID(ctx, change.MetricID)
	if mErr != nil {
		tx.Rollback()
		return nil, fmt.Errorf("parent metric not found: %w", mErr)
	}

	// Authorization
	switch transition.Code {
	case "submit", "resubmit":
		goal, _ := s.goalRepo.FindByID(ctx, metric.GoalID)
		if change.SubmittedByID != userID && !s.isSuperAdmin(ctx) && (goal == nil || goal.OwnerID != userID) {
			tx.Rollback()
			return nil, fmt.Errorf("only the submitter or goal owner can submit")
		}
	case "approve_l1", "approve_l1_final", "request_changes_l1", "reject_l1", "approve_l2", "request_changes_l2", "reject_l2":
		if change.AssignedToID == nil || *change.AssignedToID != userID {
			tx.Rollback()
			return nil, fmt.Errorf("only the assigned reviewer can perform this action")
		}
	}

	requirements, _ := s.workflowRepo.GetTransitionRequirements(ctx, transitionID)
	for _, r := range requirements {
		if r.RequirementType == "comment" && r.IsMandatory != nil && *r.IsMandatory {
			if strings.TrimSpace(req.Comment) == "" {
				tx.Rollback()
				errMsg := r.ErrorMessage
				if errMsg == "" {
					errMsg = "Comment is required for this transition"
				}
				return nil, fmt.Errorf("%s", errMsg)
			}
		}
	}

	fromStateID := change.CurrentStateID
	now := time.Now()

	history := &models.MetricValueChangeTransitionHistory{
		MetricValueChangeID: changeID,
		TransitionID:        &transitionID,
		FromStateID:         fromStateID,
		ToStateID:           transition.ToStateID,
		PerformedByID:       userID,
		Comment:             req.Comment,
		IsSystemAction:      false,
		TransitionedAt:      now,
	}
	if err := s.goalRepo.CreateMetricValueChangeTransitionHistory(ctx, tx, history); err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("failed to create transition history: %w", err)
	}

	change.CurrentStateID = transition.ToStateID
	change.Version++

	collaborators, _ := s.goalRepo.ListCollaborators(ctx, metric.GoalID)
	applied := false

	switch transition.Code {
	case "submit", "resubmit":
		var reviewerL1 *models.GoalCollaborator
		for i := range collaborators {
			if collaborators[i].Role == models.CollaboratorRoleReviewerL1 {
				reviewerL1 = &collaborators[i]
				break
			}
		}
		if reviewerL1 == nil {
			tx.Rollback()
			return nil, fmt.Errorf("no L1 reviewer assigned to this goal")
		}
		l1State, _ := s.workflowRepo.FindStateByCode(ctx, change.WorkflowID, "l1_review")
		if l1State != nil {
			autoHistory := &models.MetricValueChangeTransitionHistory{
				MetricValueChangeID: changeID,
				FromStateID:         transition.ToStateID,
				ToStateID:           l1State.ID,
				PerformedByID:       userID,
				Comment:             "Auto-assigned to L1 reviewer",
				IsSystemAction:      true,
				TransitionedAt:      now,
			}
			s.goalRepo.CreateMetricValueChangeTransitionHistory(ctx, tx, autoHistory)
			change.CurrentStateID = l1State.ID
		}
		change.AssignedToID = &reviewerL1.UserID
	case "approve_l1":
		var reviewerL2 *models.GoalCollaborator
		for i := range collaborators {
			if collaborators[i].Role == models.CollaboratorRoleReviewerL2 {
				reviewerL2 = &collaborators[i]
				break
			}
		}
		if reviewerL2 != nil {
			change.AssignedToID = &reviewerL2.UserID
		}
	case "approve_l1_final", "approve_l2":
		// Apply the proposed value to the parent metric.
		change.AssignedToID = nil
		change.AppliedAt = &now

		oldValue := metric.CurrentValue
		metric.CurrentValue = change.ProposedValue
		if err := tx.Model(&models.GoalMetric{}).Where("id = ?", metric.ID).
			Update("current_value", change.ProposedValue).Error; err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("failed to apply value to metric: %w", err)
		}

		// Append a MetricHistory record so the change shows in the per-metric
		// history endpoint alongside legacy entries.
		histEntry := &models.MetricHistory{
			MetricID:    metric.ID,
			OldValue:    oldValue,
			NewValue:    change.ProposedValue,
			ChangedByID: userID,
			Comment:     fmt.Sprintf("Approved value change %s", changeID.String()[:8]),
		}
		if err := tx.Create(histEntry).Error; err != nil {
			log.Printf("[goal_service] TransitionMetricValueChange: history insert failed: %v", err)
		}
		applied = true

	case "request_changes_l1", "request_changes_l2", "reject_l1", "reject_l2":
		change.AssignedToID = nil
	}

	if err := s.goalRepo.UpdateMetricValueChangeInTx(ctx, tx, change); err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("failed to update value change: %w", err)
	}

	if err := tx.Commit().Error; err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Post-commit: cascade formula recompute & progress
	if applied {
		s.recomputeDependentFormulas(ctx, metric.GoalID, metric.ID)
		if err := s.RecalculateProgress(ctx, metric.GoalID); err != nil {
			log.Printf("[goal_service] TransitionMetricValueChange: progress recalc failed: %v", err)
		}
	}

	// Audit log
	s.actionLogService.LogAction(ctx, &LogActionParams{
		UserID:      userID,
		Action:      "transition",
		Module:      "goals",
		ResourceID:  changeID.String(),
		Description: fmt.Sprintf("Executed '%s' on metric value change for '%s'", transition.Name, metric.Name),
		Status:      "success",
	})

	if s.wsHub != nil {
		s.wsHub.BroadcastToGoal(metric.GoalID, "metric_value_change_transitioned", map[string]interface{}{
			"change_id":  changeID,
			"metric_id":  metric.ID,
			"goal_id":    metric.GoalID,
			"transition": transition.Name,
			"applied":    applied,
		}, userID)
	}

	reloaded, rerr := s.goalRepo.FindMetricValueChangeByIDWithRelations(ctx, changeID)
	if rerr == nil {
		resp := reloaded.ToResponse()
		return &resp, nil
	}
	resp := change.ToResponse()
	return &resp, nil
}

// ListMetricValueChanges returns all value changes for a given metric.
func (s *goalService) ListMetricValueChanges(ctx context.Context, metricID uuid.UUID, userID uuid.UUID) ([]models.GoalMetricValueChangeResponse, error) {
	metric, err := s.goalRepo.FindMetricByID(ctx, metricID)
	if err != nil {
		return nil, fmt.Errorf("metric not found: %w", err)
	}
	canAccess, _ := s.canAccessGoal(ctx, metric.GoalID, userID)
	if !canAccess {
		return nil, fmt.Errorf("access denied")
	}

	changes, err := s.goalRepo.ListMetricValueChangesByMetricID(ctx, metricID)
	if err != nil {
		return nil, fmt.Errorf("failed to list value changes: %w", err)
	}

	responses := make([]models.GoalMetricValueChangeResponse, len(changes))
	for i, c := range changes {
		responses[i] = c.ToResponse()
	}
	return responses, nil
}

// ListPendingMetricApprovals lists metric definitions awaiting the caller's approval.
func (s *goalService) ListPendingMetricApprovals(ctx context.Context, userID uuid.UUID, page int, limit int) ([]models.MetricApprovalListResponse, int64, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 10
	}

	metrics, total, err := s.goalRepo.ListPendingMetricApprovals(ctx, userID, page, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list pending metric approvals: %w", err)
	}

	responses := make([]models.MetricApprovalListResponse, len(metrics))
	for i, m := range metrics {
		resp := models.MetricApprovalListResponse{
			ID:         m.ID,
			MetricID:   m.ID,
			MetricName: m.Name,
			Version:    m.Version,
			AssignedTo: models.ToUserBriefResponse(m.AssignedTo),
			CreatedAt:  m.CreatedAt,
			UpdatedAt:  m.UpdatedAt,
		}
		if m.CurrentState != nil {
			resp.StateName = m.CurrentState.Name
			resp.StateColor = m.CurrentState.Color
		}
		if m.Goal != nil {
			resp.GoalID = m.Goal.ID
			resp.GoalTitle = m.Goal.Title
			resp.GoalPriority = m.Goal.Priority
		}
		responses[i] = resp
	}

	return responses, total, nil
}

// ListPendingMetricValueChangeApprovals lists value changes awaiting the caller's approval.
func (s *goalService) ListPendingMetricValueChangeApprovals(ctx context.Context, userID uuid.UUID, page int, limit int) ([]models.MetricApprovalListResponse, int64, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 10
	}

	changes, total, err := s.goalRepo.ListPendingMetricValueChangeApprovals(ctx, userID, page, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list pending value change approvals: %w", err)
	}

	responses := make([]models.MetricApprovalListResponse, len(changes))
	for i, c := range changes {
		changeID := c.ID
		proposed := c.ProposedValue
		previous := c.PreviousValue
		resp := models.MetricApprovalListResponse{
			ID:            c.ID,
			ChangeID:      &changeID,
			MetricID:      c.MetricID,
			ProposedValue: &proposed,
			PreviousValue: &previous,
			Version:       c.Version,
			SubmittedBy:   models.ToUserBriefResponse(c.SubmittedBy),
			AssignedTo:    models.ToUserBriefResponse(c.AssignedTo),
			CreatedAt:     c.CreatedAt,
			UpdatedAt:     c.UpdatedAt,
		}
		if c.CurrentState != nil {
			resp.StateName = c.CurrentState.Name
			resp.StateColor = c.CurrentState.Color
		}
		if c.Metric != nil {
			resp.MetricName = c.Metric.Name
			if c.Metric.Goal != nil {
				resp.GoalID = c.Metric.Goal.ID
				resp.GoalTitle = c.Metric.Goal.Title
				resp.GoalPriority = c.Metric.Goal.Priority
			}
		}
		responses[i] = resp
	}

	return responses, total, nil
}

// ──────────────────────────────────────────────────
// 21. RecalculateProgress
// ──────────────────────────────────────────────────

func (s *goalService) RecalculateProgress(ctx context.Context, goalID uuid.UUID) error {
	metrics, err := s.goalRepo.ListMetricsByGoalID(ctx, goalID)
	if err != nil {
		return fmt.Errorf("failed to list metrics: %w", err)
	}

	goal, err := s.goalRepo.FindByID(ctx, goalID)
	if err != nil {
		return fmt.Errorf("goal not found: %w", err)
	}

	if len(metrics) == 0 {
		goal.Progress = 0
		return s.goalRepo.Update(ctx, goal)
	}

	var weighted float64
	var totalWeight float64

	for _, m := range metrics {
		var metricProgress float64

		if m.MetricType == models.MetricTypeBoolean {
			// Boolean: if current >= 1 then 1.0 else 0.0
			if m.CurrentValue >= 1 {
				metricProgress = 1.0
			} else {
				metricProgress = 0.0
			}
		} else {
			// Non-boolean: min(current/target, 1.0)
			if m.TargetValue != 0 {
				metricProgress = math.Min(m.CurrentValue/m.TargetValue, 1.0)
			} else {
				metricProgress = 0.0
			}
		}

		weighted += metricProgress * m.Weight
		totalWeight += m.Weight
	}

	// Handle division by zero
	var progress float64
	if totalWeight > 0 {
		progress = (weighted / totalWeight) * 100
	} else {
		progress = 0
	}

	goal.Progress = progress
	if err := s.goalRepo.Update(ctx, goal); err != nil {
		return err
	}

	// Cascade progress to parent goal if this goal has a parent
	return s.cascadeProgressToParent(ctx, goalID)
}

// cascadeProgressToParent recalculates the parent's progress as the average of its children's progress
func (s *goalService) cascadeProgressToParent(ctx context.Context, goalID uuid.UUID) error {
	goal, err := s.goalRepo.FindByID(ctx, goalID)
	if err != nil {
		return nil // Goal deleted or not found, skip
	}

	if goal.ParentGoalID == nil {
		return nil // No parent, nothing to cascade
	}

	parent, err := s.goalRepo.FindByID(ctx, *goal.ParentGoalID)
	if err != nil {
		return nil // Parent not found, skip
	}

	children, err := s.goalRepo.FindChildren(ctx, parent.ID)
	if err != nil {
		return fmt.Errorf("failed to find children for progress cascade: %w", err)
	}

	if len(children) == 0 {
		return nil
	}

	var totalProgress float64
	for _, child := range children {
		totalProgress += child.Progress
	}
	parent.Progress = totalProgress / float64(len(children))

	if err := s.goalRepo.Update(ctx, parent); err != nil {
		return fmt.Errorf("failed to update parent progress: %w", err)
	}

	// Recursively cascade to grandparent
	return s.cascadeProgressToParent(ctx, parent.ID)
}

// ──────────────────────────────────────────────────
// 22. GetEvidencePreview
// ──────────────────────────────────────────────────

func (s *goalService) GetEvidencePreview(ctx context.Context, evidenceID uuid.UUID) (string, error) {
	evidence, err := s.goalRepo.FindEvidenceByID(ctx, evidenceID)
	if err != nil {
		return "", fmt.Errorf("evidence not found: %w", err)
	}

	previewURL, err := s.documentaClient.GetPreviewURL(ctx, evidence.DocumentaFileID)
	if err != nil {
		return "", fmt.Errorf("failed to get preview URL: %w", err)
	}

	return previewURL, nil
}

// ──────────────────────────────────────────────────
// 23. GetEvidenceDownloadURL
// ──────────────────────────────────────────────────

func (s *goalService) GetEvidenceDownloadURL(ctx context.Context, evidenceID uuid.UUID) (string, error) {
	evidence, err := s.goalRepo.FindEvidenceByID(ctx, evidenceID)
	if err != nil {
		return "", fmt.Errorf("evidence not found: %w", err)
	}

	downloadURL, err := s.documentaClient.GetDownloadURL(ctx, evidence.DocumentaFileID)
	if err != nil {
		return "", fmt.Errorf("failed to get download URL: %w", err)
	}

	return downloadURL, nil
}

// DownloadEvidenceFile proxies the evidence bytes from Documenta through the
// Automax backend. Returns the body reader plus the DmsFile metadata, a best-
// guess content type, and a filename for Content-Disposition. The caller owns
// the reader and must Close it.
//
// Replaces the redirect-based flow in GetEvidenceDownloadURL so the HTTP
// handler can stream bytes back to the client directly — this keeps downloads
// working under deployments that use a path prefix (VITE_BASE_PATH) where a
// root-relative 307 Location header would resolve to the wrong URL.
func (s *goalService) DownloadEvidenceFile(ctx context.Context, evidenceID uuid.UUID) (io.ReadCloser, *storage.DmsFile, string, string, error) {
	evidence, err := s.goalRepo.FindEvidenceByID(ctx, evidenceID)
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("evidence not found: %w", err)
	}

	reader, info, err := s.documentaClient.DownloadFile(ctx, evidence.DocumentaFileID)
	if err != nil {
		return nil, nil, "", "", fmt.Errorf("failed to download evidence: %w", err)
	}

	contentType := ""
	if info != nil && info.MimeType != "" {
		contentType = info.MimeType
	} else if evidence.MimeType != "" {
		contentType = evidence.MimeType
	}

	filename := evidence.FileName
	if filename == "" && info != nil {
		filename = info.Name
	}

	return reader, info, contentType, filename, nil
}

// ──────────────────────────────────────────────────
// Export Goals
// ──────────────────────────────────────────────────

func (s *goalService) ExportGoalsCSV(ctx context.Context, filter *models.GoalFilter) ([]byte, error) {
	goals, err := s.goalRepo.ListForExport(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to list goals for export: %w", err)
	}

	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)

	// Header row
	header := []string{
		"ID", "Title", "Description", "Category", "Priority", "Status",
		"Owner", "Department", "Start Date", "Target Date", "Review Date",
		"Progress", "Metric Name", "Metric Type", "Metric Unit",
		"Baseline Value", "Current Value", "Target Value", "Weight",
	}
	if err := writer.Write(header); err != nil {
		return nil, fmt.Errorf("failed to write CSV header: %w", err)
	}

	for _, g := range goals {
		if len(g.Metrics) == 0 {
			row := goalToCSVRow(&g, nil)
			if err := writer.Write(row); err != nil {
				return nil, fmt.Errorf("failed to write CSV row: %w", err)
			}
		} else {
			for _, m := range g.Metrics {
				row := goalToCSVRow(&g, &m)
				if err := writer.Write(row); err != nil {
					return nil, fmt.Errorf("failed to write CSV row: %w", err)
				}
			}
		}
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, fmt.Errorf("CSV writer error: %w", err)
	}

	return buf.Bytes(), nil
}

func goalToCSVRow(g *models.Goal, m *models.GoalMetric) []string {
	ownerName := ""
	if g.Owner != nil {
		ownerName = g.Owner.Email
	}
	deptName := ""
	if g.Department != nil {
		deptName = g.Department.Name
	}
	startDate := ""
	if g.StartDate != nil {
		startDate = g.StartDate.Format("2006-01-02")
	}
	targetDate := ""
	if g.TargetDate != nil {
		targetDate = g.TargetDate.Format("2006-01-02")
	}
	reviewDate := ""
	if g.ReviewDate != nil {
		reviewDate = g.ReviewDate.Format("2006-01-02")
	}

	row := []string{
		g.ID.String(), g.Title, g.Description, g.Category, g.Priority, g.Status,
		ownerName, deptName, startDate, targetDate, reviewDate,
		fmt.Sprintf("%.1f", g.Progress),
	}

	if m != nil {
		row = append(row,
			m.Name, m.MetricType, m.Unit,
			fmt.Sprintf("%.2f", m.BaselineValue),
			fmt.Sprintf("%.2f", m.CurrentValue),
			fmt.Sprintf("%.2f", m.TargetValue),
			fmt.Sprintf("%.2f", m.Weight),
		)
	} else {
		row = append(row, "", "", "", "", "", "", "")
	}

	return row
}

func (s *goalService) ExportGoalsJSON(ctx context.Context, filter *models.GoalFilter) ([]models.GoalResponse, error) {
	goals, err := s.goalRepo.ListForExport(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to list goals for export: %w", err)
	}

	responses := make([]models.GoalResponse, len(goals))
	for i, g := range goals {
		responses[i] = g.ToResponse()
	}

	return responses, nil
}

// ──────────────────────────────────────────────────
// Clone Goal
// ──────────────────────────────────────────────────

func (s *goalService) CloneGoal(ctx context.Context, id uuid.UUID, req *models.GoalCloneRequest, userID uuid.UUID) (*models.GoalResponse, error) {
	source, err := s.goalRepo.FindByIDWithRelations(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("source goal not found: %w", err)
	}

	// Determine title
	title := "Copy of " + source.Title
	if req.Title != "" {
		title = req.Title
	}

	// Determine owner
	ownerID := source.OwnerID
	if req.OwnerID != nil {
		ownerID = *req.OwnerID
	}

	// Determine dates
	startDate := source.StartDate
	if req.StartDate != nil {
		startDate = req.StartDate
	}
	targetDate := source.TargetDate
	if req.TargetDate != nil {
		targetDate = req.TargetDate
	}
	reviewDate := source.ReviewDate
	if req.ReviewDate != nil {
		reviewDate = req.ReviewDate
	}

	// Ensure Goal Management folder + create goal folder
	goalMgmtFolderID, _ := s.documentaClient.EnsureFolder(ctx, s.cfg.Documenta.WorkspaceName, "", "Goal Management")
	folderID, err := s.documentaClient.CreateFolder(ctx, s.cfg.Documenta.WorkspaceName, goalMgmtFolderID, title)
	if err != nil {
		log.Printf("[GoalService] CloneGoal: failed to create Documenta folder, continuing without it: %v", err)
		folderID = ""
	}

	clone := &models.Goal{
		Title:             title,
		Description:       source.Description,
		Category:          source.Category,
		Priority:          source.Priority,
		Status:            models.GoalStatusDraft,
		OwnerID:           ownerID,
		DepartmentID:      source.DepartmentID,
		StartDate:         startDate,
		TargetDate:        targetDate,
		ReviewDate:        reviewDate,
		Progress:          0,
		DocumentaFolderID: folderID,
		Metadata:          source.Metadata,
		CreatedByID:       userID,
	}

	if err := s.goalRepo.Create(ctx, clone); err != nil {
		return nil, fmt.Errorf("failed to create cloned goal: %w", err)
	}

	// Clone metrics (reset current value to baseline)
	for _, m := range source.Metrics {
		clonedMetric := &models.GoalMetric{
			GoalID:        clone.ID,
			Name:          m.Name,
			MetricType:    m.MetricType,
			Unit:          m.Unit,
			BaselineValue: m.BaselineValue,
			CurrentValue:  m.BaselineValue,
			TargetValue:   m.TargetValue,
			Weight:        m.Weight,
		}
		if err := s.goalRepo.CreateMetric(ctx, clonedMetric); err != nil {
			log.Printf("[GoalService] CloneGoal: failed to clone metric %s: %v", m.Name, err)
		}
	}

	// Clone collaborators
	for _, c := range source.Collaborators {
		clonedCollab := &models.GoalCollaborator{
			GoalID: clone.ID,
			UserID: c.UserID,
			Role:   c.Role,
		}
		if err := s.goalRepo.AddCollaborator(ctx, clonedCollab); err != nil {
			log.Printf("[GoalService] CloneGoal: failed to clone collaborator %s: %v", c.UserID, err)
		}
	}

	// Audit log
	s.actionLogService.LogAction(ctx, &LogActionParams{
		UserID:      userID,
		Action:      "clone",
		Module:      "goals",
		ResourceID:  clone.ID.String(),
		Description: fmt.Sprintf("Cloned goal from %s: %s", source.ID.String(), title),
		Status:      "success",
	})

	cloneWithRelations, err := s.goalRepo.FindByIDWithRelations(ctx, clone.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch cloned goal: %w", err)
	}

	resp := cloneWithRelations.ToResponse()
	return &resp, nil
}

// ──────────────────────────────────────────────────
// ImportGoals
// ──────────────────────────────────────────────────

func (s *goalService) ImportGoals(ctx context.Context, fileData []byte, fileName string, dryRun bool, userID uuid.UUID) (*models.GoalImportResponse, error) {
	ext := strings.ToLower(fileName[strings.LastIndex(fileName, "."):])

	var rows [][]string
	var err error
	switch ext {
	case ".csv":
		rows, err = s.parseCSV(fileData)
	case ".xlsx":
		rows, err = s.parseXLSX(fileData)
	default:
		return nil, fmt.Errorf("unsupported file type: %s", ext)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to parse file: %w", err)
	}

	if len(rows) < 2 {
		return nil, fmt.Errorf("file must contain a header row and at least one data row")
	}

	// Skip header row
	dataRows := rows[1:]

	// Group rows by title for multi-metric goals
	type parsedMetric struct {
		name      string
		mtype     string
		unit      string
		baseline  string
		current   string
		target    string
		weight    string
		rowNumber int
	}
	type parsedGoal struct {
		title       string
		description string
		category    string
		priority    string
		ownerStr    string
		deptStr     string
		startDate   string
		targetDate  string
		reviewDate  string
		metrics     []parsedMetric
		firstRow    int
	}

	goalMap := make(map[string]*parsedGoal)
	var goalOrder []string
	rowResults := make([]models.ImportRowResult, 0, len(dataRows))

	for i, row := range dataRows {
		rowNum := i + 2 // 1-indexed, skip header
		result := models.ImportRowResult{
			RowNumber: rowNum,
			Status:    "valid",
		}

		// Pad row to expected column count (19 columns matching export format)
		for len(row) < 19 {
			row = append(row, "")
		}

		// Columns: 0=ID, 1=Title, 2=Description, 3=Category, 4=Priority, 5=Status,
		// 6=Owner, 7=Department, 8=Start Date, 9=Target Date, 10=Review Date, 11=Progress,
		// 12=Metric Name, 13=Metric Type, 14=Metric Unit, 15=Baseline Value,
		// 16=Current Value, 17=Target Value, 18=Weight
		title := strings.TrimSpace(row[1])
		if title == "" {
			result.Status = "error"
			result.Errors = append(result.Errors, "Title is required")
			result.GoalTitle = "(empty)"
			rowResults = append(rowResults, result)
			continue
		}
		result.GoalTitle = title

		// Validate priority
		priority := strings.TrimSpace(row[4])
		if priority == "" {
			priority = models.GoalPriorityMedium
			result.Warnings = append(result.Warnings, "Priority defaulted to Medium")
		} else if !models.IsValidGoalPriority(priority) {
			result.Status = "error"
			result.Errors = append(result.Errors, fmt.Sprintf("Invalid priority: %s (must be Critical, High, Medium, or Low)", priority))
		}

		// Validate owner
		ownerStr := strings.TrimSpace(row[6])
		if ownerStr != "" {
			if _, uErr := uuid.Parse(ownerStr); uErr != nil {
				// Try as email
				if !strings.Contains(ownerStr, "@") {
					result.Status = "error"
					result.Errors = append(result.Errors, fmt.Sprintf("Owner must be a valid UUID or email address: %s", ownerStr))
				} else {
					user, uErr := s.userRepo.FindByEmail(ctx, ownerStr)
					if uErr != nil || user == nil {
						result.Status = "error"
						result.Errors = append(result.Errors, fmt.Sprintf("Owner not found: %s", ownerStr))
					}
				}
			} else {
				uid, _ := uuid.Parse(ownerStr)
				user, uErr := s.userRepo.FindByID(ctx, uid)
				if uErr != nil || user == nil {
					result.Status = "error"
					result.Errors = append(result.Errors, fmt.Sprintf("Owner not found: %s", ownerStr))
				}
			}
		} else {
			result.Status = "error"
			result.Errors = append(result.Errors, "Owner is required")
		}

		// Validate department
		deptStr := strings.TrimSpace(row[7])
		if deptStr != "" {
			if _, dErr := uuid.Parse(deptStr); dErr != nil {
				// Try as department name
				dept, dErr := s.departmentRepo.FindByNameAndParent(ctx, deptStr, nil)
				if dErr != nil || dept == nil {
					result.Warnings = append(result.Warnings, fmt.Sprintf("Department not found: %s", deptStr))
				}
			} else {
				did, _ := uuid.Parse(deptStr)
				dept, dErr := s.departmentRepo.FindByID(ctx, did)
				if dErr != nil || dept == nil {
					result.Warnings = append(result.Warnings, fmt.Sprintf("Department not found: %s", deptStr))
				}
			}
		}

		// Validate dates
		startDate := strings.TrimSpace(row[8])
		targetDate := strings.TrimSpace(row[9])
		if startDate != "" {
			if _, dErr := time.Parse("2006-01-02", startDate); dErr != nil {
				result.Status = "error"
				result.Errors = append(result.Errors, fmt.Sprintf("Invalid start date format: %s (use YYYY-MM-DD)", startDate))
			}
		}
		if targetDate != "" {
			if _, dErr := time.Parse("2006-01-02", targetDate); dErr != nil {
				result.Status = "error"
				result.Errors = append(result.Errors, fmt.Sprintf("Invalid target date format: %s (use YYYY-MM-DD)", targetDate))
			}
		}
		reviewDate := strings.TrimSpace(row[10])
		if reviewDate != "" {
			if _, dErr := time.Parse("2006-01-02", reviewDate); dErr != nil {
				result.Status = "error"
				result.Errors = append(result.Errors, fmt.Sprintf("Invalid review date format: %s (use YYYY-MM-DD)", reviewDate))
			}
		}

		// Validate metric fields (if present)
		metricName := strings.TrimSpace(row[12])
		metricType := strings.TrimSpace(row[13])
		if metricName != "" {
			if metricType == "" {
				metricType = models.MetricTypeNumeric
				result.Warnings = append(result.Warnings, "Metric type defaulted to Numeric")
			} else if !models.IsValidMetricType(metricType) {
				result.Status = "error"
				result.Errors = append(result.Errors, fmt.Sprintf("Invalid metric type: %s (must be Numeric, Percentage, Currency, or Boolean)", metricType))
			}
			targetVal := strings.TrimSpace(row[17])
			if targetVal != "" {
				if _, pErr := strconv.ParseFloat(targetVal, 64); pErr != nil {
					result.Status = "error"
					result.Errors = append(result.Errors, fmt.Sprintf("Invalid target value: %s", targetVal))
				}
			}
		}

		if len(result.Warnings) > 0 && result.Status == "valid" {
			result.Status = "warning"
		}

		rowResults = append(rowResults, result)

		// Group into goals
		if _, exists := goalMap[title]; !exists {
			goalMap[title] = &parsedGoal{
				title:       title,
				description: strings.TrimSpace(row[2]),
				category:    strings.TrimSpace(row[3]),
				priority:    priority,
				ownerStr:    ownerStr,
				deptStr:     deptStr,
				startDate:   startDate,
				targetDate:  targetDate,
				reviewDate:  reviewDate,
				firstRow:    rowNum,
			}
			goalOrder = append(goalOrder, title)
		}

		if metricName != "" {
			goalMap[title].metrics = append(goalMap[title].metrics, parsedMetric{
				name:      metricName,
				mtype:     metricType,
				unit:      strings.TrimSpace(row[14]),
				baseline:  strings.TrimSpace(row[15]),
				current:   strings.TrimSpace(row[16]),
				target:    strings.TrimSpace(row[17]),
				weight:    strings.TrimSpace(row[18]),
				rowNumber: rowNum,
			})
		}
	}

	// Count results
	errorCount := 0
	warningCount := 0
	validCount := 0
	metricsCount := 0
	for _, r := range rowResults {
		switch r.Status {
		case "error":
			errorCount++
		case "warning":
			warningCount++
		default:
			validCount++
		}
	}
	for _, g := range goalMap {
		metricsCount += len(g.metrics)
	}

	mode := "dry_run"
	var createdGoalIDs []string

	if !dryRun && errorCount == 0 {
		mode = "committed"
		tx := s.goalRepo.BeginTx(ctx)

		for _, title := range goalOrder {
			pg := goalMap[title]

			// Resolve owner
			var ownerID uuid.UUID
			if _, uErr := uuid.Parse(pg.ownerStr); uErr == nil {
				ownerID, _ = uuid.Parse(pg.ownerStr)
			} else {
				user, _ := s.userRepo.FindByEmail(ctx, pg.ownerStr)
				if user != nil {
					ownerID = user.ID
				}
			}

			// Resolve department
			var departmentID *uuid.UUID
			if pg.deptStr != "" {
				if did, dErr := uuid.Parse(pg.deptStr); dErr == nil {
					departmentID = &did
				} else {
					dept, _ := s.departmentRepo.FindByNameAndParent(ctx, pg.deptStr, nil)
					if dept != nil {
						departmentID = &dept.ID
					}
				}
			}

			// Parse dates
			var startDate, targetDate, reviewDate *time.Time
			if pg.startDate != "" {
				t, _ := time.Parse("2006-01-02", pg.startDate)
				startDate = &t
			}
			if pg.targetDate != "" {
				t, _ := time.Parse("2006-01-02", pg.targetDate)
				targetDate = &t
			}
			if pg.reviewDate != "" {
				t, _ := time.Parse("2006-01-02", pg.reviewDate)
				reviewDate = &t
			}

			// Ensure Goal Management folder + create goal folder
			importGoalMgmtID, _ := s.documentaClient.EnsureFolder(ctx, s.cfg.Documenta.WorkspaceName, "", "Goal Management")
			folderID, fErr := s.documentaClient.CreateFolder(ctx, s.cfg.Documenta.WorkspaceName, importGoalMgmtID, pg.title)
			if fErr != nil {
				log.Printf("[GoalService] ImportGoals: failed to create Documenta folder for %q: %v", pg.title, fErr)
				folderID = ""
			}

			goal := &models.Goal{
				ID:                uuid.New(),
				Title:             pg.title,
				Description:       pg.description,
				Category:          pg.category,
				Priority:          pg.priority,
				Status:            models.GoalStatusDraft,
				OwnerID:           ownerID,
				DepartmentID:      departmentID,
				StartDate:         startDate,
				TargetDate:        targetDate,
				ReviewDate:        reviewDate,
				Progress:          0,
				DocumentaFolderID: folderID,
				Metadata:          "{}",
				CreatedByID:       userID,
			}

			if err := tx.WithContext(ctx).Create(goal).Error; err != nil {
				tx.Rollback()
				return nil, fmt.Errorf("failed to create goal %q: %w", pg.title, err)
			}

			// Create metrics
			for _, pm := range pg.metrics {
				baseline, _ := strconv.ParseFloat(pm.baseline, 64)
				current, _ := strconv.ParseFloat(pm.current, 64)
				target, _ := strconv.ParseFloat(pm.target, 64)
				weight, _ := strconv.ParseFloat(pm.weight, 64)
				if weight == 0 {
					weight = 1
				}

				metric := &models.GoalMetric{
					ID:            uuid.New(),
					GoalID:        goal.ID,
					Name:          pm.name,
					MetricType:    pm.mtype,
					Unit:          pm.unit,
					BaselineValue: baseline,
					CurrentValue:  current,
					TargetValue:   target,
					Weight:        weight,
				}

				if err := tx.WithContext(ctx).Create(metric).Error; err != nil {
					tx.Rollback()
					return nil, fmt.Errorf("failed to create metric %q for goal %q: %w", pm.name, pg.title, err)
				}
			}

			createdGoalIDs = append(createdGoalIDs, goal.ID.String())
		}

		if err := tx.Commit().Error; err != nil {
			return nil, fmt.Errorf("failed to commit import transaction: %w", err)
		}

		// Recalculate progress for all created goals
		for _, idStr := range createdGoalIDs {
			gid, _ := uuid.Parse(idStr)
			_ = s.RecalculateProgress(ctx, gid)
		}

		// Audit log
		s.actionLogService.LogAction(ctx, &LogActionParams{
			UserID:      userID,
			Action:      "import",
			Module:      "goals",
			ResourceID:  fmt.Sprintf("%d goals", len(createdGoalIDs)),
			Description: fmt.Sprintf("Imported %d goals with %d metrics from %s", len(createdGoalIDs), metricsCount, fileName),
			Status:      "success",
		})
	}

	return &models.GoalImportResponse{
		Mode:           mode,
		TotalRows:      len(dataRows),
		GoalsCount:     len(goalOrder),
		MetricsCount:   metricsCount,
		ValidCount:     validCount,
		ErrorCount:     errorCount,
		WarningCount:   warningCount,
		Rows:           rowResults,
		CreatedGoalIDs: createdGoalIDs,
	}, nil
}

func (s *goalService) parseCSV(data []byte) ([][]string, error) {
	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = -1 // Allow variable fields
	return reader.ReadAll()
}

func (s *goalService) parseXLSX(data []byte) ([][]string, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sheetName := f.GetSheetName(0)
	if sheetName == "" {
		return nil, fmt.Errorf("no sheets found in XLSX file")
	}

	xlRows, err := f.GetRows(sheetName)
	if err != nil {
		return nil, err
	}

	return xlRows, nil
}

// ──────────────────────────────────────────────────
// BulkAction
// ──────────────────────────────────────────────────

func (s *goalService) BulkAction(ctx context.Context, req *models.BulkActionRequest, userID uuid.UUID) (*models.BulkActionResponse, error) {
	results := make([]models.BulkActionItemResult, len(req.GoalIDs))
	successCount := 0
	failureCount := 0

	for i, goalID := range req.GoalIDs {
		var actionErr error

		switch req.Action {
		case "transition":
			if req.NewStatus == "" {
				actionErr = fmt.Errorf("new_status is required for transition action")
			} else {
				_, actionErr = s.TransitionGoalStatus(ctx, goalID, req.NewStatus, userID)
			}

		case "reassign":
			if req.NewOwnerID == nil {
				actionErr = fmt.Errorf("new_owner_id is required for reassign action")
			} else {
				ownerID := *req.NewOwnerID
				_, actionErr = s.UpdateGoal(ctx, goalID, &models.GoalUpdateRequest{
					OwnerID: &ownerID,
				}, userID)
			}

		case "close":
			_, actionErr = s.TransitionGoalStatus(ctx, goalID, models.GoalStatusClosed, userID)

		default:
			actionErr = fmt.Errorf("unknown action: %s", req.Action)
		}

		result := models.BulkActionItemResult{
			GoalID:  goalID,
			Success: actionErr == nil,
		}
		if actionErr != nil {
			result.Error = actionErr.Error()
			failureCount++
		} else {
			successCount++
		}
		results[i] = result
	}

	// Audit log
	s.actionLogService.LogAction(ctx, &LogActionParams{
		UserID:      userID,
		Action:      "bulk_" + req.Action,
		Module:      "goals",
		ResourceID:  fmt.Sprintf("%d goals", len(req.GoalIDs)),
		Description: fmt.Sprintf("Bulk %s: %d succeeded, %d failed", req.Action, successCount, failureCount),
		Status:      "success",
	})

	return &models.BulkActionResponse{
		TotalRequested: len(req.GoalIDs),
		SuccessCount:   successCount,
		FailureCount:   failureCount,
		Results:        results,
	}, nil
}

// ──────────────────────────────────────────────────
// Hierarchy: GetGoalTree
// ──────────────────────────────────────────────────

func (s *goalService) GetGoalTree(ctx context.Context, rootID uuid.UUID) (*models.GoalResponse, error) {
	goal, err := s.goalRepo.FindByIDWithRelations(ctx, rootID)
	if err != nil {
		return nil, fmt.Errorf("goal not found: %w", err)
	}

	// Recursively load children for each child (depth-first)
	if err := s.loadChildrenRecursive(ctx, goal, 0); err != nil {
		return nil, err
	}

	resp := goal.ToResponse()
	return &resp, nil
}

func (s *goalService) loadChildrenRecursive(ctx context.Context, goal *models.Goal, depth int) error {
	if depth > 3 {
		return nil // Safety limit
	}

	children, err := s.goalRepo.FindChildren(ctx, goal.ID)
	if err != nil {
		return err
	}

	goal.Children = children
	for i := range goal.Children {
		if err := s.loadChildrenRecursive(ctx, &goal.Children[i], depth+1); err != nil {
			return err
		}
	}
	return nil
}

// ──────────────────────────────────────────────────
// Hierarchy: ListChildGoals
// ──────────────────────────────────────────────────

func (s *goalService) ListChildGoals(ctx context.Context, parentID uuid.UUID) ([]models.GoalResponse, error) {
	children, err := s.goalRepo.FindChildren(ctx, parentID)
	if err != nil {
		return nil, fmt.Errorf("failed to list child goals: %w", err)
	}

	responses := make([]models.GoalResponse, len(children))
	for i, child := range children {
		responses[i] = child.ToResponse()
	}
	return responses, nil
}

// ──────────────────────────────────────────────────
// Check-in: CreateCheckIn
// ──────────────────────────────────────────────────

func (s *goalService) CreateCheckIn(ctx context.Context, goalID uuid.UUID, req *models.CheckInCreateRequest, userID uuid.UUID) (*models.CheckInResponse, error) {
	// Access check
	canAccess, _ := s.canAccessGoal(ctx, goalID, userID)
	if !canAccess {
		return nil, fmt.Errorf("access denied")
	}

	// Verify goal exists
	goal, err := s.goalRepo.FindByID(ctx, goalID)
	if err != nil {
		return nil, fmt.Errorf("goal not found: %w", err)
	}

	// Validate check-in status
	if !models.IsValidCheckInStatus(req.Status) {
		return nil, fmt.Errorf("invalid check-in status: %s", req.Status)
	}

	// Process metric updates if any
	type metricChange struct {
		MetricID string  `json:"metric_id"`
		Name     string  `json:"name"`
		OldValue float64 `json:"old_value"`
		NewValue float64 `json:"new_value"`
	}
	var changes []metricChange

	for _, mu := range req.MetricUpdates {
		metric, err := s.goalRepo.FindMetricByID(ctx, mu.MetricID)
		if err != nil {
			return nil, fmt.Errorf("metric %s not found: %w", mu.MetricID.String(), err)
		}
		if metric.GoalID != goalID {
			return nil, fmt.Errorf("metric %s does not belong to this goal", mu.MetricID.String())
		}

		oldValue := metric.CurrentValue
		changes = append(changes, metricChange{
			MetricID: mu.MetricID.String(),
			Name:     metric.Name,
			OldValue: oldValue,
			NewValue: mu.Value,
		})

		// Use UpdateMetricValue to update + create history + recalculate progress
		_, err = s.UpdateMetricValue(ctx, mu.MetricID, &models.MetricValueUpdateRequest{
			Value:   mu.Value,
			Comment: mu.Comment,
		}, userID)
		if err != nil {
			return nil, fmt.Errorf("failed to update metric %s: %w", metric.Name, err)
		}
	}

	// Refresh goal to get updated progress
	goal, err = s.goalRepo.FindByID(ctx, goalID)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh goal: %w", err)
	}

	// Serialize metric changes to JSON
	metricUpdatesJSON := "[]"
	if len(changes) > 0 {
		jsonBytes, err := json.Marshal(changes)
		if err != nil {
			return nil, fmt.Errorf("failed to serialize metric updates: %w", err)
		}
		metricUpdatesJSON = string(jsonBytes)
	}

	checkIn := &models.GoalCheckIn{
		GoalID:        goalID,
		AuthorID:      userID,
		Status:        req.Status,
		Content:       req.Content,
		ProgressSnap:  goal.Progress,
		MetricUpdates: metricUpdatesJSON,
	}

	if err := s.goalRepo.CreateCheckIn(ctx, checkIn); err != nil {
		return nil, fmt.Errorf("failed to create check-in: %w", err)
	}

	// Audit log
	s.actionLogService.LogAction(ctx, &LogActionParams{
		UserID:      userID,
		Action:      "check_in",
		Module:      "goals",
		ResourceID:  goal.ID.String(),
		Description: fmt.Sprintf("Check-in on goal '%s': %s", goal.Title, req.Status),
		Status:      "success",
	})

	// WebSocket broadcast
	if s.wsHub != nil {
		s.wsHub.BroadcastToGoal(goalID, "check_in_created", map[string]interface{}{
			"goal_id":     goalID,
			"check_in_id": checkIn.ID,
		}, userID)
	}

	// Fetch back with author
	created, err := s.goalRepo.FindCheckInByID(ctx, checkIn.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch created check-in: %w", err)
	}

	resp := created.ToResponse()
	return &resp, nil
}

// ──────────────────────────────────────────────────
// Check-in: ListCheckIns
// ──────────────────────────────────────────────────

func (s *goalService) ListCheckIns(ctx context.Context, goalID uuid.UUID, page int, limit int) ([]models.CheckInResponse, int64, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 10
	}

	checkIns, total, err := s.goalRepo.ListCheckIns(ctx, goalID, page, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list check-ins: %w", err)
	}

	responses := make([]models.CheckInResponse, len(checkIns))
	for i, ci := range checkIns {
		responses[i] = ci.ToResponse()
	}

	return responses, total, nil
}

// ──────────────────────────────────────────────────
// Check-in: DeleteCheckIn
// ──────────────────────────────────────────────────

func (s *goalService) DeleteCheckIn(ctx context.Context, id uuid.UUID, userID uuid.UUID) error {
	checkIn, err := s.goalRepo.FindCheckInByID(ctx, id)
	if err != nil {
		return fmt.Errorf("check-in not found: %w", err)
	}

	// Access check via parent goal
	canModify, _ := s.canModifyGoal(ctx, checkIn.GoalID, userID)
	if !canModify {
		return fmt.Errorf("access denied: you can only delete check-ins on goals you own or are assigned to review")
	}

	if err := s.goalRepo.DeleteCheckIn(ctx, id); err != nil {
		return fmt.Errorf("failed to delete check-in: %w", err)
	}

	// Audit log
	s.actionLogService.LogAction(ctx, &LogActionParams{
		UserID:      userID,
		Action:      "delete_check_in",
		Module:      "goals",
		ResourceID:  checkIn.GoalID.String(),
		Description: fmt.Sprintf("Deleted check-in on goal %s", checkIn.GoalID.String()),
		Status:      "success",
	})

	return nil
}

// ════════════════════════════════════════════════════
// METRIC IMPORT/EXPORT
// ════════════════════════════════════════════════════

// ──────────────────────────────────────────────────
// ExportMetricsTemplate
// ──────────────────────────────────────────────────

func (s *goalService) ExportMetricsTemplate(ctx context.Context, filter *models.GoalFilter, format string) ([]byte, error) {
	goals, err := s.goalRepo.ListForExport(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to list goals for export: %w", err)
	}

	type metricRow struct {
		GoalID        string
		GoalTitle     string
		MetricID      string
		MetricName    string
		MetricType    string
		Unit          string
		BaselineValue float64
		CurrentValue  float64
		TargetValue   float64
	}

	var rows []metricRow
	for _, g := range goals {
		for _, m := range g.Metrics {
			rows = append(rows, metricRow{
				GoalID:        g.ID.String(),
				GoalTitle:     g.Title,
				MetricID:      m.ID.String(),
				MetricName:    m.Name,
				MetricType:    m.MetricType,
				Unit:          m.Unit,
				BaselineValue: m.BaselineValue,
				CurrentValue:  m.CurrentValue,
				TargetValue:   m.TargetValue,
			})
		}
	}

	header := []string{
		"Goal ID", "Goal Title", "Metric ID", "Metric Name",
		"Metric Type", "Unit", "Baseline Value", "Current Value",
		"Target Value", "New Value",
	}

	if format == "xlsx" {
		f := excelize.NewFile()
		sheet := "Metrics"
		f.SetSheetName("Sheet1", sheet)

		// Header style
		headerStyle, _ := f.NewStyle(&excelize.Style{
			Font:      &excelize.Font{Bold: true, Color: "FFFFFF"},
			Fill:      excelize.Fill{Type: "pattern", Color: []string{"4472C4"}, Pattern: 1},
			Alignment: &excelize.Alignment{Horizontal: "center"},
		})

		for i, h := range header {
			cell, _ := excelize.CoordinatesToCellName(i+1, 1)
			f.SetCellValue(sheet, cell, h)
			f.SetCellStyle(sheet, cell, cell, headerStyle)
		}

		for i, r := range rows {
			row := i + 2
			f.SetCellValue(sheet, cellName(1, row), r.GoalID)
			f.SetCellValue(sheet, cellName(2, row), r.GoalTitle)
			f.SetCellValue(sheet, cellName(3, row), r.MetricID)
			f.SetCellValue(sheet, cellName(4, row), r.MetricName)
			f.SetCellValue(sheet, cellName(5, row), r.MetricType)
			f.SetCellValue(sheet, cellName(6, row), r.Unit)
			f.SetCellValue(sheet, cellName(7, row), r.BaselineValue)
			f.SetCellValue(sheet, cellName(8, row), r.CurrentValue)
			f.SetCellValue(sheet, cellName(9, row), r.TargetValue)
			f.SetCellValue(sheet, cellName(10, row), "") // New Value — blank
		}

		// Auto-fit column widths
		for i := range header {
			col, _ := excelize.ColumnNumberToName(i + 1)
			f.SetColWidth(sheet, col, col, 18)
		}

		var buf bytes.Buffer
		if err := f.Write(&buf); err != nil {
			return nil, fmt.Errorf("failed to write XLSX: %w", err)
		}
		return buf.Bytes(), nil
	}

	// Default: CSV
	var buf bytes.Buffer
	writer := csv.NewWriter(&buf)

	if err := writer.Write(header); err != nil {
		return nil, fmt.Errorf("failed to write CSV header: %w", err)
	}

	for _, r := range rows {
		csvRow := []string{
			r.GoalID, r.GoalTitle, r.MetricID, r.MetricName,
			r.MetricType, r.Unit,
			fmt.Sprintf("%.2f", r.BaselineValue),
			fmt.Sprintf("%.2f", r.CurrentValue),
			fmt.Sprintf("%.2f", r.TargetValue),
			"", // New Value — blank
		}
		if err := writer.Write(csvRow); err != nil {
			return nil, fmt.Errorf("failed to write CSV row: %w", err)
		}
	}

	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, fmt.Errorf("CSV writer error: %w", err)
	}
	return buf.Bytes(), nil
}

func cellName(col, row int) string {
	name, _ := excelize.CoordinatesToCellName(col, row)
	return name
}

// ──────────────────────────────────────────────────
// ImportMetricsDryRun
// ──────────────────────────────────────────────────

func (s *goalService) ImportMetricsDryRun(ctx context.Context, fileData []byte, fileName string, userID uuid.UUID) (*models.MetricImportDryRunResponse, error) {
	items, totalRows, err := s.parseMetricImportFile(ctx, fileData, fileName)
	if err != nil {
		return nil, err
	}

	goalSet := make(map[uuid.UUID]bool)
	var validCount, errorCount, warningCount, skippedCount int
	for i := range items {
		switch items[i].Status {
		case "valid":
			validCount++
			gid, _ := uuid.Parse(items[i].GoalID)
			goalSet[gid] = true
		case "error":
			errorCount++
		case "warning":
			warningCount++
			gid, _ := uuid.Parse(items[i].GoalID)
			goalSet[gid] = true
		case "skipped":
			skippedCount++
		}
	}

	return &models.MetricImportDryRunResponse{
		TotalRows:    totalRows,
		ValidCount:   validCount,
		ErrorCount:   errorCount,
		WarningCount: warningCount,
		SkippedCount: skippedCount,
		GoalCount:    len(goalSet),
		Items:        items,
	}, nil
}

// ──────────────────────────────────────────────────
// ImportMetricsCommit
// ──────────────────────────────────────────────────

func (s *goalService) ImportMetricsCommit(ctx context.Context, fileData []byte, fileName string, title string, comment string, primaryGoalID uuid.UUID, userID uuid.UUID) (*models.MetricImportBatchResponse, error) {
	// Validate primary goal exists
	if _, err := s.goalRepo.FindByID(ctx, primaryGoalID); err != nil {
		return nil, fmt.Errorf("primary goal not found: %w", err)
	}

	// Parse and validate
	items, _, err := s.parseMetricImportFile(ctx, fileData, fileName)
	if err != nil {
		return nil, err
	}

	// Filter to only valid items
	var validItems []models.MetricImportValidationItem
	for _, item := range items {
		if item.Status == "valid" || item.Status == "warning" {
			validItems = append(validItems, item)
		}
	}

	if len(validItems) == 0 {
		return nil, fmt.Errorf("no valid metric values to import")
	}

	// Resolve workflow
	workflows, wfErr := s.workflowRepo.ListByRecordType(ctx, "evidence", true)
	if wfErr != nil || len(workflows) == 0 {
		return nil, fmt.Errorf("no active evidence approval workflow found")
	}
	wf := workflows[0]
	initialState, stateErr := s.workflowRepo.GetInitialState(ctx, wf.ID)
	if stateErr != nil || initialState == nil {
		return nil, fmt.Errorf("evidence workflow has no initial state")
	}

	// Count distinct goals
	goalSet := make(map[uuid.UUID]bool)
	var importItems []models.MetricImportItem
	for _, vi := range validItems {
		goalID, _ := uuid.Parse(vi.GoalID)
		metricID, _ := uuid.Parse(vi.MetricID)
		goalSet[goalID] = true

		importItems = append(importItems, models.MetricImportItem{
			ID:       uuid.New(),
			GoalID:   goalID,
			MetricID: metricID,
			OldValue: vi.CurrentValue,
			NewValue: vi.NewValue,
		})
	}

	// Create batch in transaction
	tx := s.goalRepo.BeginTx(ctx)
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	batch := &models.MetricImportBatch{
		Title:          title,
		Comment:        comment,
		Status:         "Draft",
		ItemCount:      len(importItems),
		GoalCount:      len(goalSet),
		FileName:       fileName,
		ImportedByID:   userID,
		PrimaryGoalID:  primaryGoalID,
		WorkflowID:     &wf.ID,
		CurrentStateID: &initialState.ID,
		Version:        1,
	}

	if err := tx.Create(batch).Error; err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("failed to create import batch: %w", err)
	}

	// Set batch ID on items
	for i := range importItems {
		importItems[i].BatchID = batch.ID
	}

	if err := s.goalRepo.CreateMetricImportItems(ctx, tx, importItems); err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("failed to create import items: %w", err)
	}

	if err := tx.Commit().Error; err != nil {
		return nil, fmt.Errorf("failed to commit: %w", err)
	}

	// Audit log
	s.actionLogService.LogAction(ctx, &LogActionParams{
		UserID:      userID,
		Action:      "create",
		Module:      "goals",
		ResourceID:  batch.ID.String(),
		Description: fmt.Sprintf("Created metric import batch '%s' with %d items across %d goals", title, len(importItems), len(goalSet)),
		Status:      "success",
	})

	// Reload with relations
	batchWithRelations, reloadErr := s.goalRepo.FindMetricImportBatchByIDWithRelations(ctx, batch.ID)
	if reloadErr != nil {
		resp := batch.ToResponse()
		return &resp, nil
	}
	resp := batchWithRelations.ToResponse()
	return &resp, nil
}

// parseMetricImportFile parses CSV/XLSX and validates each row
func (s *goalService) parseMetricImportFile(ctx context.Context, fileData []byte, fileName string) ([]models.MetricImportValidationItem, int, error) {
	var rows [][]string
	var parseErr error

	lowerName := strings.ToLower(fileName)
	if strings.HasSuffix(lowerName, ".xlsx") || strings.HasSuffix(lowerName, ".xls") {
		rows, parseErr = s.parseXLSX(fileData)
	} else if strings.HasSuffix(lowerName, ".csv") {
		rows, parseErr = s.parseCSV(fileData)
	} else {
		return nil, 0, fmt.Errorf("unsupported file format: must be CSV or XLSX")
	}

	if parseErr != nil {
		return nil, 0, fmt.Errorf("failed to parse file: %w", parseErr)
	}

	if len(rows) < 2 {
		return nil, 0, fmt.Errorf("file must contain a header row and at least one data row")
	}

	// Skip header row
	dataRows := rows[1:]
	totalRows := len(dataRows)

	var results []models.MetricImportValidationItem

	for i, row := range dataRows {
		rowNum := i + 2 // 1-indexed, +1 for header

		item := models.MetricImportValidationItem{
			RowNumber: rowNum,
		}

		// Need at least 10 columns
		if len(row) < 10 {
			item.Status = "error"
			item.Errors = append(item.Errors, fmt.Sprintf("expected 10 columns, got %d", len(row)))
			results = append(results, item)
			continue
		}

		goalIDStr := strings.TrimSpace(row[0])
		item.GoalID = goalIDStr
		item.GoalTitle = strings.TrimSpace(row[1])
		metricIDStr := strings.TrimSpace(row[2])
		item.MetricID = metricIDStr
		item.MetricName = strings.TrimSpace(row[3])
		newValueStr := strings.TrimSpace(row[9])

		// Skip empty new value
		if newValueStr == "" {
			item.Status = "skipped"
			results = append(results, item)
			continue
		}

		var hasErrors bool

		// Validate Goal ID
		goalID, err := uuid.Parse(goalIDStr)
		if err != nil {
			item.Errors = append(item.Errors, "invalid Goal ID (not a valid UUID)")
			hasErrors = true
		}

		// Validate Metric ID
		metricID, err := uuid.Parse(metricIDStr)
		if err != nil {
			item.Errors = append(item.Errors, "invalid Metric ID (not a valid UUID)")
			hasErrors = true
		}

		// Validate New Value
		newValue, err := strconv.ParseFloat(newValueStr, 64)
		if err != nil {
			item.Errors = append(item.Errors, fmt.Sprintf("invalid New Value '%s' (must be a number)", newValueStr))
			hasErrors = true
		}

		if hasErrors {
			item.Status = "error"
			results = append(results, item)
			continue
		}

		// Verify goal exists
		goal, err := s.goalRepo.FindByID(ctx, goalID)
		if err != nil {
			item.Errors = append(item.Errors, "goal not found")
			item.Status = "error"
			results = append(results, item)
			continue
		}
		item.GoalTitle = goal.Title

		// Verify metric exists and belongs to goal
		metric, err := s.goalRepo.FindMetricByID(ctx, metricID)
		if err != nil {
			item.Errors = append(item.Errors, "metric not found")
			item.Status = "error"
			results = append(results, item)
			continue
		}
		if metric.GoalID != goalID {
			item.Errors = append(item.Errors, "metric does not belong to the specified goal")
			item.Status = "error"
			results = append(results, item)
			continue
		}
		item.MetricName = metric.Name
		item.CurrentValue = metric.CurrentValue
		item.NewValue = newValue

		// Skip if new value equals current value
		if newValue == metric.CurrentValue {
			item.Status = "skipped"
			item.Warnings = append(item.Warnings, "new value equals current value")
			results = append(results, item)
			continue
		}

		item.Status = "valid"

		results = append(results, item)
	}

	return results, totalRows, nil
}

// ──────────────────────────────────────────────────
// GetMetricImportBatch
// ──────────────────────────────────────────────────

func (s *goalService) GetMetricImportBatch(ctx context.Context, id uuid.UUID) (*models.MetricImportBatchResponse, error) {
	batch, err := s.goalRepo.FindMetricImportBatchByIDWithRelations(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("batch not found: %w", err)
	}

	resp := batch.ToResponse()
	return &resp, nil
}

// ──────────────────────────────────────────────────
// ListMetricImportBatches
// ──────────────────────────────────────────────────

func (s *goalService) ListMetricImportBatches(ctx context.Context, filter *models.MetricImportBatchFilter) ([]models.MetricImportBatchResponse, int64, error) {
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.Limit < 1 {
		filter.Limit = 10
	}

	batches, total, err := s.goalRepo.ListMetricImportBatches(ctx, filter)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list metric import batches: %w", err)
	}

	responses := make([]models.MetricImportBatchResponse, len(batches))
	for i, b := range batches {
		responses[i] = b.ToResponse()
	}

	return responses, total, nil
}

// ──────────────────────────────────────────────────
// DeleteMetricImportBatch
// ──────────────────────────────────────────────────

func (s *goalService) DeleteMetricImportBatch(ctx context.Context, id uuid.UUID, userID uuid.UUID) error {
	batch, err := s.goalRepo.FindMetricImportBatchByID(ctx, id)
	if err != nil {
		return fmt.Errorf("batch not found: %w", err)
	}

	if batch.Status == "Approved" {
		return fmt.Errorf("approved batches cannot be deleted")
	}

	if err := s.goalRepo.DeleteMetricImportBatch(ctx, id); err != nil {
		return fmt.Errorf("failed to delete batch: %w", err)
	}

	s.actionLogService.LogAction(ctx, &LogActionParams{
		UserID:      userID,
		Action:      "delete",
		Module:      "goals",
		ResourceID:  id.String(),
		Description: fmt.Sprintf("Deleted metric import batch '%s'", batch.Title),
		Status:      "success",
	})

	return nil
}

// ──────────────────────────────────────────────────
// GetAvailableMetricBatchTransitions
// ──────────────────────────────────────────────────

func (s *goalService) GetAvailableMetricBatchTransitions(ctx context.Context, batchID uuid.UUID, userID uuid.UUID) ([]models.AvailableTransitionResponse, error) {
	batch, err := s.goalRepo.FindMetricImportBatchByIDWithRelations(ctx, batchID)
	if err != nil {
		return nil, fmt.Errorf("batch not found: %w", err)
	}

	if batch.CurrentStateID == nil || batch.WorkflowID == nil {
		return nil, fmt.Errorf("batch has no workflow assigned")
	}

	// Get all transitions from current state
	transitions, err := s.workflowRepo.ListTransitionsFromState(ctx, *batch.CurrentStateID)
	if err != nil {
		return nil, fmt.Errorf("failed to get transitions: %w", err)
	}

	// Get collaborators from primary goal to determine L2 reviewer existence
	collaborators, err := s.goalRepo.ListCollaborators(ctx, batch.PrimaryGoalID)
	if err != nil {
		return nil, fmt.Errorf("failed to list collaborators: %w", err)
	}

	var hasL2Reviewer bool
	for _, c := range collaborators {
		if c.Role == models.CollaboratorRoleReviewerL2 {
			hasL2Reviewer = true
			break
		}
	}

	var results []models.AvailableTransitionResponse
	for _, t := range transitions {
		canExecute := true
		reason := ""

		requirements, _ := s.workflowRepo.GetTransitionRequirements(ctx, t.ID)

		switch t.Code {
		case "submit", "resubmit":
			goal, _ := s.goalRepo.FindByID(ctx, batch.PrimaryGoalID)
			if batch.ImportedByID != userID && (goal == nil || goal.OwnerID != userID) {
				canExecute = false
				reason = "Only the importer or goal owner can submit"
			}
		case "approve_l1":
			if !hasL2Reviewer {
				continue
			}
			if batch.AssignedToID == nil || *batch.AssignedToID != userID {
				canExecute = false
				reason = "Only the assigned reviewer can approve"
			}
		case "approve_l1_final":
			if hasL2Reviewer {
				continue
			}
			if batch.AssignedToID == nil || *batch.AssignedToID != userID {
				canExecute = false
				reason = "Only the assigned reviewer can approve"
			}
		case "request_changes_l1", "reject_l1", "request_changes_l2", "reject_l2":
			if batch.AssignedToID == nil || *batch.AssignedToID != userID {
				canExecute = false
				reason = "Only the assigned reviewer can perform this action"
			}
		case "approve_l2":
			if batch.AssignedToID == nil || *batch.AssignedToID != userID {
				canExecute = false
				reason = "Only the assigned L2 reviewer can approve"
			}
		case "assign_l1":
			continue
		}

		reqResponses := make([]models.TransitionRequirementResponse, len(requirements))
		for i, r := range requirements {
			reqResponses[i] = models.ToTransitionRequirementResponse(&r)
		}

		results = append(results, models.AvailableTransitionResponse{
			Transition:   models.ToWorkflowTransitionResponse(&t),
			CanExecute:   canExecute,
			Requirements: reqResponses,
			Reason:       reason,
		})
	}

	return results, nil
}

// ──────────────────────────────────────────────────
// ExecuteMetricBatchTransition
// ──────────────────────────────────────────────────

func (s *goalService) ExecuteMetricBatchTransition(ctx context.Context, batchID uuid.UUID, req *models.MetricImportBatchTransitionRequest, userID uuid.UUID) (*models.MetricImportBatchResponse, error) {
	transitionID, err := uuid.Parse(req.TransitionID)
	if err != nil {
		return nil, fmt.Errorf("invalid transition_id: %w", err)
	}

	tx := s.goalRepo.BeginTx(ctx)
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	// Pessimistic lock
	batch, err := s.goalRepo.FindMetricImportBatchByIDForUpdate(ctx, tx, batchID)
	if err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("batch not found: %w", err)
	}

	// Optimistic concurrency check
	if batch.Version != req.Version {
		tx.Rollback()
		return nil, fmt.Errorf("batch has been modified by another user (expected version %d, got %d)", batch.Version, req.Version)
	}

	if batch.CurrentStateID == nil || batch.WorkflowID == nil {
		tx.Rollback()
		return nil, fmt.Errorf("batch has no workflow assigned")
	}

	// Fetch transition
	transition, err := s.workflowRepo.FindTransitionByIDWithRelations(ctx, transitionID)
	if err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("transition not found: %w", err)
	}

	if transition.WorkflowID != *batch.WorkflowID {
		tx.Rollback()
		return nil, fmt.Errorf("transition does not belong to batch's workflow")
	}

	if transition.FromStateID != *batch.CurrentStateID {
		tx.Rollback()
		return nil, fmt.Errorf("transition is not valid from current state")
	}

	// Authorization check
	switch transition.Code {
	case "submit", "resubmit":
		goal, _ := s.goalRepo.FindByID(ctx, batch.PrimaryGoalID)
		if batch.ImportedByID != userID && (goal == nil || goal.OwnerID != userID) {
			tx.Rollback()
			return nil, fmt.Errorf("only the importer or goal owner can submit")
		}
	case "approve_l1", "approve_l1_final", "request_changes_l1", "reject_l1", "approve_l2", "request_changes_l2", "reject_l2":
		if batch.AssignedToID == nil || *batch.AssignedToID != userID {
			tx.Rollback()
			return nil, fmt.Errorf("only the assigned reviewer can perform this action")
		}
	}

	// Validate requirements
	requirements, _ := s.workflowRepo.GetTransitionRequirements(ctx, transitionID)
	for _, r := range requirements {
		if r.RequirementType == "comment" && r.IsMandatory != nil && *r.IsMandatory {
			if strings.TrimSpace(req.Comment) == "" {
				tx.Rollback()
				errMsg := r.ErrorMessage
				if errMsg == "" {
					errMsg = "Comment is required for this transition"
				}
				return nil, fmt.Errorf("%s", errMsg)
			}
		}
	}

	fromStateID := *batch.CurrentStateID

	// Create transition history
	now := time.Now()
	history := &models.MetricImportBatchTransitionHistory{
		BatchID:        batchID,
		TransitionID:   &transitionID,
		FromStateID:    fromStateID,
		ToStateID:      transition.ToStateID,
		PerformedByID:  userID,
		Comment:        req.Comment,
		IsSystemAction: false,
		TransitionedAt: now,
	}
	if err := s.goalRepo.CreateMetricBatchTransitionHistory(ctx, tx, history); err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("failed to create transition history: %w", err)
	}

	// Update batch state
	batch.CurrentStateID = &transition.ToStateID
	batch.Version++

	targetState, _ := s.workflowRepo.FindStateByID(ctx, transition.ToStateID)
	if targetState != nil {
		switch targetState.Code {
		case "draft":
			batch.Status = "Draft"
		case "submitted":
			batch.Status = "Submitted"
		case "l1_review", "l2_review":
			batch.Status = "In_Review"
		case "approved":
			batch.Status = "Approved"
		case "rejected":
			batch.Status = "Rejected"
		case "changes_requested":
			batch.Status = "Changes_Requested"
		}
	}

	// Assignment logic — resolve collaborators from primary goal
	collaborators, _ := s.goalRepo.ListCollaborators(ctx, batch.PrimaryGoalID)

	var affectedGoalIDs []uuid.UUID

	switch transition.Code {
	case "submit", "resubmit":
		var reviewerL1 *models.GoalCollaborator
		for i := range collaborators {
			if collaborators[i].Role == models.CollaboratorRoleReviewerL1 {
				reviewerL1 = &collaborators[i]
				break
			}
		}
		if reviewerL1 == nil {
			tx.Rollback()
			return nil, fmt.Errorf("no L1 reviewer assigned to the primary goal")
		}

		l1State, _ := s.workflowRepo.FindStateByCode(ctx, *batch.WorkflowID, "l1_review")
		if l1State != nil {
			autoHistory := &models.MetricImportBatchTransitionHistory{
				BatchID:        batchID,
				FromStateID:    transition.ToStateID,
				ToStateID:      l1State.ID,
				PerformedByID:  userID,
				Comment:        "Auto-assigned to L1 reviewer",
				IsSystemAction: true,
				TransitionedAt: now,
			}
			s.goalRepo.CreateMetricBatchTransitionHistory(ctx, tx, autoHistory)
			batch.CurrentStateID = &l1State.ID
			batch.Status = "In_Review"
		}

		batch.AssignedToID = &reviewerL1.UserID

	case "approve_l1":
		var reviewerL2 *models.GoalCollaborator
		for i := range collaborators {
			if collaborators[i].Role == models.CollaboratorRoleReviewerL2 {
				reviewerL2 = &collaborators[i]
				break
			}
		}
		if reviewerL2 != nil {
			batch.AssignedToID = &reviewerL2.UserID
		}

	case "approve_l1_final", "approve_l2":
		// Final approval — apply metric values
		batch.AssignedToID = nil
		goalIDs, applyErr := s.applyMetricBatchValues(ctx, tx, batch, userID)
		if applyErr != nil {
			tx.Rollback()
			return nil, fmt.Errorf("failed to apply metric values: %w", applyErr)
		}
		affectedGoalIDs = goalIDs

	case "request_changes_l1", "request_changes_l2", "reject_l1", "reject_l2":
		batch.AssignedToID = nil
	}

	// Save batch
	if err := s.goalRepo.UpdateMetricImportBatchInTx(ctx, tx, batch); err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("failed to update batch: %w", err)
	}

	if err := tx.Commit().Error; err != nil {
		return nil, fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Post-commit: recalculate progress for affected goals
	for _, goalID := range affectedGoalIDs {
		s.RecalculateProgress(ctx, goalID)
	}

	// Post-commit notifications
	primaryGoal, _ := s.goalRepo.FindByID(ctx, batch.PrimaryGoalID)
	goalTitle := "a goal"
	if primaryGoal != nil {
		goalTitle = primaryGoal.Title
	}

	switch transition.Code {
	case "submit", "resubmit":
		if batch.AssignedToID != nil {
			reviewer, rErr := s.userRepo.FindByID(ctx, *batch.AssignedToID)
			if rErr == nil && reviewer != nil {
				s.notificationService.SendNotification(ctx, "notification", nil, "en",
					[]string{reviewer.Email}, nil, nil,
					"Metric Import Submitted for Review",
					fmt.Sprintf("Metric import batch '%s' has been submitted for your review (primary goal: %s)", batch.Title, goalTitle),
					nil, nil, &userID, nil)
			}
		}
	case "approve_l1":
		if batch.AssignedToID != nil {
			reviewer, rErr := s.userRepo.FindByID(ctx, *batch.AssignedToID)
			if rErr == nil && reviewer != nil {
				s.notificationService.SendNotification(ctx, "notification", nil, "en",
					[]string{reviewer.Email}, nil, nil,
					"Metric Import Pending L2 Review",
					fmt.Sprintf("Metric import batch '%s' for goal '%s' requires your L2 review", batch.Title, goalTitle),
					nil, nil, &userID, nil)
			}
		}
	case "approve_l1_final", "approve_l2":
		importer, iErr := s.userRepo.FindByID(ctx, batch.ImportedByID)
		if iErr == nil && importer != nil {
			s.notificationService.SendNotification(ctx, "notification", nil, "en",
				[]string{importer.Email}, nil, nil,
				"Metric Import Approved",
				fmt.Sprintf("Your metric import batch '%s' for goal '%s' has been approved. %d metric values have been applied.", batch.Title, goalTitle, batch.ItemCount),
				nil, nil, &userID, nil)
		}
	case "reject_l1", "reject_l2":
		importer, iErr := s.userRepo.FindByID(ctx, batch.ImportedByID)
		if iErr == nil && importer != nil {
			s.notificationService.SendNotification(ctx, "notification", nil, "en",
				[]string{importer.Email}, nil, nil,
				"Metric Import Rejected",
				fmt.Sprintf("Your metric import batch '%s' for goal '%s' has been rejected. Comment: %s", batch.Title, goalTitle, req.Comment),
				nil, nil, &userID, nil)
		}
	case "request_changes_l1", "request_changes_l2":
		importer, iErr := s.userRepo.FindByID(ctx, batch.ImportedByID)
		if iErr == nil && importer != nil {
			s.notificationService.SendNotification(ctx, "notification", nil, "en",
				[]string{importer.Email}, nil, nil,
				"Metric Import Returned for Changes",
				fmt.Sprintf("Your metric import batch '%s' for goal '%s' has been returned for changes. Comment: %s", batch.Title, goalTitle, req.Comment),
				nil, nil, &userID, nil)
		}
	}

	// Audit log
	s.actionLogService.LogAction(ctx, &LogActionParams{
		UserID:      userID,
		Action:      "transition",
		Module:      "goals",
		ResourceID:  batchID.String(),
		Description: fmt.Sprintf("Executed '%s' on metric import batch '%s'", transition.Name, batch.Title),
		Status:      "success",
	})

	// WebSocket broadcast
	if s.wsHub != nil {
		wsData := map[string]interface{}{
			"batch_id":        batchID,
			"primary_goal_id": batch.PrimaryGoalID,
			"status":          batch.Status,
			"transition":      transition.Name,
		}
		s.wsHub.BroadcastToGoal(batch.PrimaryGoalID, "metric_batch_transitioned", wsData, userID)
		s.wsHub.BroadcastGoalToAll("metric_batch_transitioned", wsData)
	}

	// Reload with relations
	batchWithRelations, reloadErr := s.goalRepo.FindMetricImportBatchByIDWithRelations(ctx, batchID)
	if reloadErr != nil {
		resp := batch.ToResponse()
		return &resp, nil
	}
	resp := batchWithRelations.ToResponse()
	return &resp, nil
}

// applyMetricBatchValues applies all metric values from a batch atomically
func (s *goalService) applyMetricBatchValues(ctx context.Context, tx *gorm.DB, batch *models.MetricImportBatch, userID uuid.UUID) ([]uuid.UUID, error) {
	items, err := s.goalRepo.ListMetricImportItemsByBatchID(ctx, batch.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to load batch items: %w", err)
	}

	goalSet := make(map[uuid.UUID]bool)

	for _, item := range items {
		// Fresh-read metric to handle drift
		metric, err := s.goalRepo.FindMetricByID(ctx, item.MetricID)
		if err != nil {
			return nil, fmt.Errorf("metric %s not found: %w", item.MetricID.String(), err)
		}

		// Create metric history entry
		historyEntry := &models.MetricHistory{
			MetricID:    metric.ID,
			OldValue:    metric.CurrentValue,
			NewValue:    item.NewValue,
			ChangedByID: userID,
			Comment:     fmt.Sprintf("Bulk import: %s", batch.Title),
		}
		if err := tx.Create(historyEntry).Error; err != nil {
			return nil, fmt.Errorf("failed to create metric history: %w", err)
		}

		// Update metric current value
		if err := tx.Model(metric).Update("current_value", item.NewValue).Error; err != nil {
			return nil, fmt.Errorf("failed to update metric value: %w", err)
		}

		goalSet[item.GoalID] = true
	}

	// Mark all items as applied
	if err := s.goalRepo.MarkMetricImportItemsApplied(ctx, tx, batch.ID); err != nil {
		return nil, fmt.Errorf("failed to mark items as applied: %w", err)
	}

	goalIDs := make([]uuid.UUID, 0, len(goalSet))
	for gid := range goalSet {
		goalIDs = append(goalIDs, gid)
	}

	return goalIDs, nil
}

// ──────────────────────────────────────────────────
// GetMetricBatchTransitionHistory
// ──────────────────────────────────────────────────

func (s *goalService) GetMetricBatchTransitionHistory(ctx context.Context, batchID uuid.UUID) ([]models.MetricImportBatchTransitionHistoryResponse, error) {
	histories, err := s.goalRepo.ListMetricBatchTransitionHistory(ctx, batchID)
	if err != nil {
		return nil, fmt.Errorf("failed to get transition history: %w", err)
	}

	responses := make([]models.MetricImportBatchTransitionHistoryResponse, len(histories))
	for i, h := range histories {
		responses[i] = h.ToResponse()
	}

	return responses, nil
}

// ──────────────────────────────────────────────────
// Goal Comments
// ──────────────────────────────────────────────────

func (s *goalService) AddComment(ctx context.Context, goalID uuid.UUID, content string, userID uuid.UUID) (*models.GoalCommentResponse, error) {
	canAccess, err := s.canAccessGoal(ctx, goalID, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to check goal access: %w", err)
	}
	if !canAccess {
		return nil, fmt.Errorf("access denied")
	}

	comment := &models.GoalComment{
		GoalID:   goalID,
		AuthorID: userID,
		Content:  content,
	}

	if err := s.goalRepo.CreateComment(ctx, comment); err != nil {
		return nil, fmt.Errorf("failed to create comment: %w", err)
	}

	// Reload with author preloaded
	created, err := s.goalRepo.FindCommentByID(ctx, comment.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to load comment: %w", err)
	}

	resp := models.ToGoalCommentResponse(created)
	return &resp, nil
}

func (s *goalService) ListComments(ctx context.Context, goalID uuid.UUID, page, limit int) ([]models.GoalCommentResponse, int64, error) {
	comments, total, err := s.goalRepo.ListComments(ctx, goalID, page, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to list comments: %w", err)
	}

	responses := make([]models.GoalCommentResponse, len(comments))
	for i, c := range comments {
		responses[i] = models.ToGoalCommentResponse(&c)
	}

	return responses, total, nil
}

func (s *goalService) DeleteComment(ctx context.Context, commentID uuid.UUID, userID uuid.UUID) error {
	comment, err := s.goalRepo.FindCommentByID(ctx, commentID)
	if err != nil {
		return fmt.Errorf("comment not found: %w", err)
	}

	// Only the author or a super admin can delete
	if comment.AuthorID != userID && !s.isSuperAdmin(ctx) {
		return fmt.Errorf("access denied: only the comment author can delete")
	}

	if err := s.goalRepo.DeleteComment(ctx, commentID); err != nil {
		return fmt.Errorf("failed to delete comment: %w", err)
	}

	return nil
}
