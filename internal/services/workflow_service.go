package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type WorkflowService interface {
	// Workflow CRUD
	CreateWorkflow(ctx context.Context, req *models.WorkflowCreateRequest, createdByID uuid.UUID) (*models.WorkflowResponse, error)
	GetWorkflow(ctx context.Context, id uuid.UUID) (*models.WorkflowResponse, error)
	ListWorkflows(ctx context.Context, activeOnly bool) ([]models.WorkflowResponse, error)
	ListWorkflowsByRecordType(ctx context.Context, recordType string, activeOnly bool) ([]models.WorkflowResponse, error)
	UpdateWorkflow(ctx context.Context, id uuid.UUID, req *models.WorkflowUpdateRequest) (*models.WorkflowResponse, error)
	DeleteWorkflow(ctx context.Context, id uuid.UUID) error
	PermanentDeleteWorkflow(ctx context.Context, id uuid.UUID) error
	RestoreWorkflow(ctx context.Context, id uuid.UUID) error
	ListDeletedWorkflows(ctx context.Context) ([]models.WorkflowResponse, error)
	DuplicateWorkflow(ctx context.Context, id uuid.UUID, createdByID uuid.UUID) (*models.WorkflowResponse, error)

	// Classification assignment
	AssignClassifications(ctx context.Context, workflowID uuid.UUID, classificationIDs []uuid.UUID) error
	GetWorkflowByClassification(ctx context.Context, classificationID uuid.UUID) (*models.WorkflowResponse, error)

	// State management
	CreateState(ctx context.Context, workflowID uuid.UUID, req *models.WorkflowStateCreateRequest) (*models.WorkflowStateResponse, error)
	GetState(ctx context.Context, stateID uuid.UUID) (*models.WorkflowStateResponse, error)
	ListStates(ctx context.Context, workflowID uuid.UUID) ([]models.WorkflowStateResponse, error)
	UpdateState(ctx context.Context, stateID uuid.UUID, req *models.WorkflowStateUpdateRequest) (*models.WorkflowStateResponse, error)
	DeleteState(ctx context.Context, stateID uuid.UUID) error

	// Check Workflow Existence by Code or Name
	WorkflowExistsByCodeOrName(ctx context.Context, codeOrName []string) error

	// Transition management
	CreateTransition(ctx context.Context, workflowID uuid.UUID, req *models.WorkflowTransitionCreateRequest) (*models.WorkflowTransitionResponse, error)
	GetTransition(ctx context.Context, transitionID uuid.UUID) (*models.WorkflowTransitionResponse, error)
	ListTransitions(ctx context.Context, workflowID uuid.UUID) ([]models.WorkflowTransitionResponse, error)
	UpdateTransition(ctx context.Context, transitionID uuid.UUID, req *models.WorkflowTransitionUpdateRequest) (*models.WorkflowTransitionResponse, error)
	DeleteTransition(ctx context.Context, transitionID uuid.UUID) error

	// Transition configuration
	SetTransitionRoles(ctx context.Context, transitionID uuid.UUID, roleIDs []uuid.UUID) error
	SetTransitionRequirements(ctx context.Context, transitionID uuid.UUID, requirements []models.TransitionRequirementRequest) error
	SetTransitionActions(ctx context.Context, transitionID uuid.UUID, actions []models.TransitionActionRequest) error
	SetTransitionFieldChanges(ctx context.Context, transitionID uuid.UUID, fieldChanges []models.TransitionFieldChangeRequest) error

	// Get transitions from a state (for incident transition UI)
	GetTransitionsFromState(ctx context.Context, stateID uuid.UUID) ([]models.WorkflowTransitionResponse, error)
	GetTransitionsToState(ctx context.Context, stateID uuid.UUID) ([]models.WorkflowTransitionResponse, error)
	GetInitialState(ctx context.Context, workflowID uuid.UUID) (*models.WorkflowStateResponse, error)

	// Get matching users for a workflow's initial state (for manual-select creation assignment)
	GetInitialStateMatchingUsers(ctx context.Context, workflowID uuid.UUID, classificationID, locationID, departmentID *uuid.UUID) ([]models.UserResponse, error)

	// Workflow matching - for mobile apps and other clients
	MatchWorkflow(ctx context.Context, req *models.WorkflowMatchRequest) (*models.WorkflowMatchResponse, error)

	// ResolveWorkflow picks the best workflow for an incident based on location, classification, and
	// record type, falling back to the default workflow when no specific match is found.
	ResolveWorkflow(ctx context.Context, locationID, classificationID *uuid.UUID, recordType string) (uuid.UUID, error)

	// Import/Export
	ExportWorkflow(ctx context.Context, id uuid.UUID) ([]byte, string, error)
	ImportWorkflow(ctx context.Context, data *models.WorkflowImportData, createdByID uuid.UUID) (*models.WorkflowResponse, []string, error)
}

type workflowService struct {
	repo      repository.WorkflowRepository
	roleRepo  repository.RoleRepository
	deptRepo  repository.DepartmentRepository
	classRepo repository.ClassificationRepository
	userRepo  repository.UserRepository
	db        *gorm.DB
}

func NewWorkflowService(repo repository.WorkflowRepository, roleRepo repository.RoleRepository, deptRepo repository.DepartmentRepository, classRepo repository.ClassificationRepository, userRepo repository.UserRepository, db *gorm.DB) WorkflowService {
	return &workflowService{
		repo:      repo,
		roleRepo:  roleRepo,
		deptRepo:  deptRepo,
		classRepo: classRepo,
		userRepo:  userRepo,
		db:        db,
	}
}

// checkForDuplicateRules checks if another workflow already has the same matching rules
// Returns (isConflict bool, conflictingWorkflowName string, conflictDetails string, error)
func (s *workflowService) checkForDuplicateRules(
	ctx context.Context,
	recordType string,
	sources []string,
	priorities []int,
	classificationIDs []uuid.UUID,
	locationIDs []uuid.UUID,
	isDefault bool,
	excludeWorkflowID *uuid.UUID,
) (bool, string, string, error) {
	// Get all workflows that could potentially conflict
	existingWorkflows, err := s.repo.FindWorkflowsByRecordTypeAndClassifications(
		ctx,
		recordType,
		classificationIDs,
		locationIDs,
		excludeWorkflowID,
	)
	if err != nil {
		return false, "", "", err
	}

	// Build sets for classification and location IDs for comparison
	newClassIDSet := make(map[uuid.UUID]bool)
	for _, id := range classificationIDs {
		newClassIDSet[id] = true
	}

	newLocIDSet := make(map[uuid.UUID]bool)
	for _, id := range locationIDs {
		newLocIDSet[id] = true
	}

	// Build sets for sources and priorities
	newSourceSet := make(map[string]bool)
	for _, s := range sources {
		newSourceSet[s] = true
	}

	newPrioritySet := make(map[int]bool)
	for _, p := range priorities {
		newPrioritySet[p] = true
	}

	for _, existingWf := range existingWorkflows {
		// Parse existing workflow's sources and priorities
		var existingSources []string
		if existingWf.Sources != "" {
			json.Unmarshal([]byte(existingWf.Sources), &existingSources)
		}

		var existingPriorities []int
		if existingWf.Priorities != "" {
			json.Unmarshal([]byte(existingWf.Priorities), &existingPriorities)
		}

		// Check if sources overlap (both empty OR have common elements)
		sourcesOverlap := (len(sources) == 0 && len(existingSources) == 0) ||
			(len(sources) == 0 || len(existingSources) == 0) || // One is generic (empty) - overlaps with everything
			hasOverlap(newSourceSet, existingSources)

		// Check if priorities overlap (both empty OR have common elements)
		prioritiesOverlap := (len(priorities) == 0 && len(existingPriorities) == 0) ||
			(len(priorities) == 0 || len(existingPriorities) == 0) || // One is generic (empty) - overlaps with everything
			hasOverlapInt(newPrioritySet, existingPriorities)

		// If sources or priorities don't overlap, no conflict
		if !sourcesOverlap || !prioritiesOverlap {
			continue
		}

		// Case 1: Both workflows are default with no classifications and no locations (generic fallback conflict)
		if isDefault && existingWf.IsDefault &&
			len(classificationIDs) == 0 && len(existingWf.Classifications) == 0 &&
			len(locationIDs) == 0 && len(existingWf.Locations) == 0 {
			var conflictParts []string
			if len(sources) > 0 {
				conflictParts = append(conflictParts, fmt.Sprintf("sources: %s", strings.Join(sources, ", ")))
			}
			if len(priorities) > 0 {
				priorityStrs := make([]string, len(priorities))
				for i, p := range priorities {
					priorityStrs[i] = fmt.Sprintf("%d", p)
				}
				conflictParts = append(conflictParts, fmt.Sprintf("priorities: %s", strings.Join(priorityStrs, ", ")))
			}
			conflictDetails := "default workflow"
			if len(conflictParts) > 0 {
				conflictDetails = fmt.Sprintf("default workflow with %s", strings.Join(conflictParts, " and "))
			} else {
				conflictDetails = "default workflow with no sources, priorities, classifications or locations"
			}
			return true, existingWf.Name, conflictDetails, nil
		}

		// Case 2: Exact classification AND location match
		classificationsMatch := len(classificationIDs) == len(existingWf.Classifications)
		locationsMatch := len(locationIDs) == len(existingWf.Locations)

		if classificationsMatch && locationsMatch {
			// Check classifications
			existingClassIDSet := make(map[uuid.UUID]bool)
			for _, c := range existingWf.Classifications {
				existingClassIDSet[c.ID] = true
			}

			allClassMatch := true
			for id := range newClassIDSet {
				if !existingClassIDSet[id] {
					allClassMatch = false
					break
				}
			}

			// Check locations
			existingLocIDSet := make(map[uuid.UUID]bool)
			for _, loc := range existingWf.Locations {
				existingLocIDSet[loc.ID] = true
			}

			allLocMatch := true
			for id := range newLocIDSet {
				if !existingLocIDSet[id] {
					allLocMatch = false
					break
				}
			}

			// Both must match for a conflict
			if allClassMatch && allLocMatch {
				// Build details about what conflicted
				var conflictParts []string

				if len(sources) > 0 {
					conflictParts = append(conflictParts, fmt.Sprintf("sources: %s", strings.Join(sources, ", ")))
				}

				if len(priorities) > 0 {
					priorityStrs := make([]string, len(priorities))
					for i, p := range priorities {
						priorityStrs[i] = fmt.Sprintf("%d", p)
					}
					conflictParts = append(conflictParts, fmt.Sprintf("priorities: %s", strings.Join(priorityStrs, ", ")))
				}

				if len(classificationIDs) > 0 {
					classNames := make([]string, 0, len(existingWf.Classifications))
					for _, c := range existingWf.Classifications {
						classNames = append(classNames, c.Name)
					}
					conflictParts = append(conflictParts, fmt.Sprintf("classifications: %s", strings.Join(classNames, ", ")))
				}

				if len(locationIDs) > 0 {
					locNames := make([]string, 0, len(existingWf.Locations))
					for _, loc := range existingWf.Locations {
						locNames = append(locNames, loc.Name)
					}
					conflictParts = append(conflictParts, fmt.Sprintf("locations: %s", strings.Join(locNames, ", ")))
				}

				conflictDetails := strings.Join(conflictParts, " and ")
				if conflictDetails == "" {
					conflictDetails = "same record type with no sources, priorities, classifications or locations"
				}

				return true, existingWf.Name, conflictDetails, nil
			}
		}
	}

	return false, "", "", nil
}

// Workflow CRUD

func (s *workflowService) CreateWorkflow(ctx context.Context, req *models.WorkflowCreateRequest, createdByID uuid.UUID) (*models.WorkflowResponse, error) {
	// Convert RequiredFields array to JSON string
	requiredFieldsJSON := "[]"
	if len(req.RequiredFields) > 0 {
		jsonBytes, err := json.Marshal(req.RequiredFields)
		if err == nil {
			requiredFieldsJSON = string(jsonBytes)
		}
	}

	// Convert OptionalFields array to JSON string
	optionalFieldsJSON := "[]"
	if len(req.OptionalFields) > 0 {
		jsonBytes, err := json.Marshal(req.OptionalFields)
		if err == nil {
			optionalFieldsJSON = string(jsonBytes)
		}
	}

	recordType := "incident"
	if req.RecordType != "" {
		recordType = req.RecordType
	}

	// Parse classification IDs (if provided - usually configured later in designer)
	var classificationIDs []uuid.UUID
	for _, idStr := range req.ClassificationIDs {
		id, err := uuid.Parse(idStr)
		if err == nil {
			classificationIDs = append(classificationIDs, id)
		}
	}

	// Parse location IDs (if provided - usually configured later in designer)
	var locationIDs []uuid.UUID
	for _, idStr := range req.LocationIDs {
		id, err := uuid.Parse(idStr)
		if err == nil {
			locationIDs = append(locationIDs, id)
		}
	}

	// Marshal sources and priorities to JSON
	sourcesJSON := "[]"
	if len(req.Sources) > 0 {
		jsonBytes, err := json.Marshal(req.Sources)
		if err == nil {
			sourcesJSON = string(jsonBytes)
		}
	}

	prioritiesJSON := "[]"
	if len(req.Priorities) > 0 {
		jsonBytes, err := json.Marshal(req.Priorities)
		if err == nil {
			prioritiesJSON = string(jsonBytes)
		}
	}

	// Only check for duplicate rules if classifications, locations, sources, or priorities are provided
	// Typically, workflows are created empty and configured later in the designer
	if len(classificationIDs) > 0 || len(locationIDs) > 0 || len(req.Sources) > 0 || len(req.Priorities) > 0 {
		isConflict, conflictingName, conflictDetails, err := s.checkForDuplicateRules(
			ctx,
			recordType,
			req.Sources,
			req.Priorities,
			classificationIDs,
			locationIDs,
			false, // New workflows aren't default by default
			nil,   // No exclusion needed for new workflows
		)
		if err != nil {
			return nil, err
		}
		if isConflict {
			return nil, fmt.Errorf("workflow rules conflict: these rules (%s) are already in use by workflow '%s'", conflictDetails, conflictingName)
		}
	}

	workflow := &models.Workflow{
		Name:           req.Name,
		NameAr:         req.NameAr,
		Code:           req.Code,
		Description:    req.Description,
		DescriptionAr:  req.DescriptionAr,
		RecordType:     recordType,
		Sources:        sourcesJSON,
		Priorities:     prioritiesJSON,
		RequiredFields: requiredFieldsJSON,
		OptionalFields: optionalFieldsJSON,
		CreatedByID:    &createdByID,
		IsActive:       true,
		Version:        1,
	}

	if err := s.repo.Create(ctx, workflow); err != nil {
		// Check for unique constraint violations
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "unique constraint") {
			if strings.Contains(err.Error(), "idx_workflows_name") {
				return nil, fmt.Errorf("a workflow with this name already exists")
			}
			if strings.Contains(err.Error(), "idx_workflows_code") {
				return nil, fmt.Errorf("a workflow with this code already exists")
			}
		}
		return nil, err
	}

	// Assign classifications if provided (optional at creation time)
	if len(classificationIDs) > 0 {
		if err := s.repo.AssignClassifications(ctx, workflow.ID, classificationIDs); err != nil {
			// Log error but don't fail the workflow creation
		}
	}

	// Assign locations if provided (optional at creation time)
	if len(locationIDs) > 0 {
		if err := s.repo.AssignLocations(ctx, workflow.ID, locationIDs); err != nil {
			// Log error but don't fail the workflow creation
		}
	}

	// Fetch with relations (if FindByIDWithRelations fails due to missing table, use FindByID temporarily)
	created, err := s.repo.FindByIDWithRelations(ctx, workflow.ID)
	if err != nil {
		// Fallback: try without relations if migration hasn't run yet
		created, err = s.repo.FindByID(ctx, workflow.ID)
		if err != nil {
			return nil, err
		}
	}

	resp := models.ToWorkflowResponse(created)
	return &resp, nil
}

func (s *workflowService) GetWorkflow(ctx context.Context, id uuid.UUID) (*models.WorkflowResponse, error) {
	workflow, err := s.repo.FindByIDWithRelations(ctx, id)
	if err != nil {
		return nil, err
	}

	resp := models.ToWorkflowResponse(workflow)
	return &resp, nil
}

func (s *workflowService) ListWorkflows(ctx context.Context, activeOnly bool) ([]models.WorkflowResponse, error) {
	workflows, err := s.repo.List(ctx, activeOnly)
	if err != nil {
		return nil, err
	}

	responses := make([]models.WorkflowResponse, len(workflows))
	for i, w := range workflows {
		responses[i] = models.ToWorkflowResponse(&w)
	}

	return responses, nil
}

func (s *workflowService) ListWorkflowsByRecordType(ctx context.Context, recordType string, activeOnly bool) ([]models.WorkflowResponse, error) {
	workflows, err := s.repo.ListByRecordType(ctx, recordType, activeOnly)
	if err != nil {
		return nil, err
	}

	responses := make([]models.WorkflowResponse, len(workflows))
	for i, w := range workflows {
		responses[i] = models.ToWorkflowResponse(&w)
	}

	return responses, nil
}

func (s *workflowService) UpdateWorkflow(ctx context.Context, id uuid.UUID, req *models.WorkflowUpdateRequest) (*models.WorkflowResponse, error) {
	// Fetch existing workflow with relations
	workflow, err := s.repo.FindByIDWithRelations(ctx, id)
	if err != nil {
		return nil, err
	}

	// Determine the final state after updates
	finalRecordType := workflow.RecordType
	if req.RecordType != nil && *req.RecordType != "" {
		finalRecordType = *req.RecordType
	}

	finalIsDefault := workflow.IsDefault
	if req.IsDefault != nil {
		finalIsDefault = *req.IsDefault
	}

	// Parse sources from request or use existing
	var finalSources []string
	if req.Sources != nil {
		finalSources = req.Sources
	} else {
		// Use existing sources
		if workflow.Sources != "" {
			json.Unmarshal([]byte(workflow.Sources), &finalSources)
		}
	}

	// Parse priorities from request or use existing
	var finalPriorities []int
	if req.Priorities != nil {
		finalPriorities = req.Priorities
	} else {
		// Use existing priorities
		if workflow.Priorities != "" {
			json.Unmarshal([]byte(workflow.Priorities), &finalPriorities)
		}
	}

	// Parse classification IDs from request or use existing
	var finalClassificationIDs []uuid.UUID
	if req.ClassificationIDs != nil {
		for _, idStr := range req.ClassificationIDs {
			id, err := uuid.Parse(idStr)
			if err == nil {
				finalClassificationIDs = append(finalClassificationIDs, id)
			}
		}
	} else {
		// Use existing classifications
		for _, c := range workflow.Classifications {
			finalClassificationIDs = append(finalClassificationIDs, c.ID)
		}
	}

	// Parse location IDs from request or use existing
	var finalLocationIDs []uuid.UUID
	if req.LocationIDs != nil {
		for _, idStr := range req.LocationIDs {
			id, err := uuid.Parse(idStr)
			if err == nil {
				finalLocationIDs = append(finalLocationIDs, id)
			}
		}
	} else {
		// Use existing locations
		for _, loc := range workflow.Locations {
			finalLocationIDs = append(finalLocationIDs, loc.ID)
		}
	}

	// Check for duplicate rules with the final state, excluding this workflow
	isConflict, conflictingName, conflictDetails, err := s.checkForDuplicateRules(
		ctx,
		finalRecordType,
		finalSources,
		finalPriorities,
		finalClassificationIDs,
		finalLocationIDs,
		finalIsDefault,
		&id, // Exclude this workflow from conflict check
	)
	if err != nil {
		return nil, err
	}
	if isConflict {
		return nil, fmt.Errorf("workflow rules conflict: these rules (%s) are already in use by workflow '%s'", conflictDetails, conflictingName)
	}

	// Apply updates
	if req.Name != "" {
		workflow.Name = req.Name
	}
	if req.NameAr != "" {
		workflow.NameAr = req.NameAr
	}
	if req.Code != "" {
		workflow.Code = req.Code
	}
	if req.Description != "" {
		workflow.Description = req.Description
	}
	if req.DescriptionAr != "" {
		workflow.DescriptionAr = req.DescriptionAr
	}
	if req.IsActive != nil {
		workflow.IsActive = *req.IsActive
	}
	if req.IsDefault != nil {
		workflow.IsDefault = *req.IsDefault
	}
	if req.RecordType != nil {
		workflow.RecordType = *req.RecordType
	}
	// Update Sources if provided (nil means not updating, empty array means clear)
	if req.Sources != nil {
		jsonBytes, err := json.Marshal(req.Sources)
		if err == nil {
			workflow.Sources = string(jsonBytes)
		}
	}
	// Update Priorities if provided (nil means not updating, empty array means clear)
	if req.Priorities != nil {
		jsonBytes, err := json.Marshal(req.Priorities)
		if err == nil {
			workflow.Priorities = string(jsonBytes)
		}
	}
	if req.CanvasLayout != "" {
		workflow.CanvasLayout = req.CanvasLayout
	}
	// Update RequiredFields if provided (nil means not updating, empty array means clear)
	if req.RequiredFields != nil {
		jsonBytes, err := json.Marshal(req.RequiredFields)
		if err == nil {
			workflow.RequiredFields = string(jsonBytes)
		}
	}
	// Update OptionalFields if provided (nil means not updating, empty array means clear)
	if req.OptionalFields != nil {
		jsonBytes, err := json.Marshal(req.OptionalFields)
		if err == nil {
			workflow.OptionalFields = string(jsonBytes)
		}
	}

	if err := s.repo.Update(ctx, workflow); err != nil {
		return nil, err
	}

	// Update classifications if provided
	if req.ClassificationIDs != nil {
		if err := s.repo.AssignClassifications(ctx, workflow.ID, finalClassificationIDs); err != nil {
			return nil, err
		}
	}

	// Update locations if provided
	if req.LocationIDs != nil {
		if err := s.repo.AssignLocations(ctx, workflow.ID, finalLocationIDs); err != nil {
			return nil, err
		}
	}

	// Update convert-to-request roles if provided
	if req.ConvertToRequestRoleIDs != nil {
		roleIDs := make([]uuid.UUID, 0, len(req.ConvertToRequestRoleIDs))
		for _, idStr := range req.ConvertToRequestRoleIDs {
			id, err := uuid.Parse(idStr)
			if err != nil {
				continue
			}
			roleIDs = append(roleIDs, id)
		}
		if err := s.repo.AssignConvertToRequestRoles(ctx, workflow.ID, roleIDs); err != nil {
			return nil, err
		}
	}

	// Update merge-allowed roles if provided
	if req.MergeAllowedRoleIDs != nil {
		roleIDs := make([]uuid.UUID, 0, len(req.MergeAllowedRoleIDs))
		for _, idStr := range req.MergeAllowedRoleIDs {
			id, err := uuid.Parse(idStr)
			if err != nil {
				continue
			}
			roleIDs = append(roleIDs, id)
		}
		if err := s.repo.AssignMergeAllowedRoles(ctx, workflow.ID, roleIDs); err != nil {
			return nil, err
		}
	}

	updated, err := s.repo.FindByIDWithRelations(ctx, id)
	if err != nil {
		return nil, err
	}

	resp := models.ToWorkflowResponse(updated)
	return &resp, nil
}

func (s *workflowService) WorkflowExistsByCodeOrName(ctx context.Context, codeOrName []string) error {
	exists, err := s.repo.ExistsByCodeOrName(ctx, codeOrName)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("workflow with code '%s' or name '%s' already exists", codeOrName[0], codeOrName[1])
	}
	return nil
}

func (s *workflowService) DeleteWorkflow(ctx context.Context, id uuid.UUID) error {
	// Soft delete - marks workflow as deleted but keeps in database
	return s.repo.Delete(ctx, id)
}

func (s *workflowService) PermanentDeleteWorkflow(ctx context.Context, id uuid.UUID) error {
	// Hard delete - permanently removes workflow and all related data from database
	return s.repo.HardDelete(ctx, id)
}

func (s *workflowService) RestoreWorkflow(ctx context.Context, id uuid.UUID) error {
	return s.repo.Restore(ctx, id)
}

func (s *workflowService) ListDeletedWorkflows(ctx context.Context) ([]models.WorkflowResponse, error) {
	workflows, err := s.repo.ListDeleted(ctx)
	if err != nil {
		return nil, err
	}

	responses := make([]models.WorkflowResponse, len(workflows))
	for i, w := range workflows {
		responses[i] = models.ToWorkflowResponse(&w)
	}
	return responses, nil
}

func (s *workflowService) DuplicateWorkflow(ctx context.Context, id uuid.UUID, createdByID uuid.UUID) (*models.WorkflowResponse, error) {
	original, err := s.repo.FindByIDWithRelations(ctx, id)
	if err != nil {
		return nil, err
	}

	// Create new workflow
	newWorkflow := &models.Workflow{
		Name:         fmt.Sprintf("%s (Copy)", original.Name),
		Code:         fmt.Sprintf("%s_copy_%s", original.Code, uuid.New().String()[:8]),
		Description:  original.Description,
		CanvasLayout: original.CanvasLayout,
		CreatedByID:  &createdByID,
		IsActive:     false, // Start as inactive
		Version:      1,
	}

	if err := s.repo.Create(ctx, newWorkflow); err != nil {
		return nil, err
	}

	// Map old state IDs to new state IDs
	stateIDMap := make(map[uuid.UUID]uuid.UUID)

	// Duplicate states
	for _, state := range original.States {
		newState := &models.WorkflowState{
			WorkflowID:    newWorkflow.ID,
			Name:          state.Name,
			NameAr:        state.NameAr,
			Code:          state.Code,
			Description:   state.Description,
			DescriptionAr: state.DescriptionAr,
			StateType:     state.StateType,
			Color:         state.Color,
			PositionX:     state.PositionX,
			PositionY:     state.PositionY,
			SLAHours:      state.SLAHours,
			SLAUnit:       state.SLAUnit,
			SortOrder:     state.SortOrder,
			IsActive:      state.IsActive,
		}
		if err := s.repo.CreateState(ctx, newState); err != nil {
			return nil, err
		}
		stateIDMap[state.ID] = newState.ID
	}

	// Duplicate transitions
	for _, trans := range original.Transitions {
		newFromStateID, ok := stateIDMap[trans.FromStateID]
		if !ok {
			continue
		}
		newToStateID, ok := stateIDMap[trans.ToStateID]
		if !ok {
			continue
		}

		newTrans := &models.WorkflowTransition{
			WorkflowID:    newWorkflow.ID,
			Name:          trans.Name,
			NameAr:        trans.NameAr,
			Code:          trans.Code,
			Description:   trans.Description,
			DescriptionAr: trans.DescriptionAr,
			FromStateID:   newFromStateID,
			ToStateID:     newToStateID,
			SortOrder:     trans.SortOrder,
			IsActive:      trans.IsActive,
		}
		if err := s.repo.CreateTransition(ctx, newTrans); err != nil {
			return nil, err
		}

		// Copy role assignments
		if len(trans.AllowedRoles) > 0 {
			roleIDs := make([]uuid.UUID, len(trans.AllowedRoles))
			for i, role := range trans.AllowedRoles {
				roleIDs[i] = role.ID
			}
			s.repo.AssignTransitionRoles(ctx, newTrans.ID, roleIDs)
		}

		// Copy requirements
		if len(trans.Requirements) > 0 {
			newReqs := make([]models.TransitionRequirement, len(trans.Requirements))
			for i, req := range trans.Requirements {
				newReqs[i] = models.TransitionRequirement{
					RequirementType: req.RequirementType,
					FieldName:       req.FieldName,
					FieldValue:      req.FieldValue,
					IsMandatory:     req.IsMandatory,
					ErrorMessage:    req.ErrorMessage,
				}
			}
			s.repo.SetTransitionRequirements(ctx, newTrans.ID, newReqs)
		}

		// Copy actions
		if len(trans.Actions) > 0 {
			newActions := make([]models.TransitionAction, len(trans.Actions))
			for i, action := range trans.Actions {
				newActions[i] = models.TransitionAction{
					ActionType:     action.ActionType,
					Name:           action.Name,
					Description:    action.Description,
					Config:         action.Config,
					ExecutionOrder: action.ExecutionOrder,
					IsAsync:        action.IsAsync,
					IsActive:       action.IsActive,
				}
			}
			s.repo.SetTransitionActions(ctx, newTrans.ID, newActions)
		}
	}

	duplicated, err := s.repo.FindByIDWithRelations(ctx, newWorkflow.ID)
	if err != nil {
		return nil, err
	}

	resp := models.ToWorkflowResponse(duplicated)
	return &resp, nil
}

// Classification assignment

func (s *workflowService) AssignClassifications(ctx context.Context, workflowID uuid.UUID, classificationIDs []uuid.UUID) error {
	return s.repo.AssignClassifications(ctx, workflowID, classificationIDs)
}

func (s *workflowService) GetWorkflowByClassification(ctx context.Context, classificationID uuid.UUID) (*models.WorkflowResponse, error) {
	workflow, err := s.repo.GetByClassificationID(ctx, classificationID)
	if err != nil {
		// Try to get default workflow
		workflow, err = s.repo.GetDefaultWorkflow(ctx)
		if err != nil {
			return nil, errors.New("no workflow found for classification and no default workflow configured")
		}
	}

	resp := models.ToWorkflowResponse(workflow)
	return &resp, nil
}

// State management

func (s *workflowService) CreateState(ctx context.Context, workflowID uuid.UUID, req *models.WorkflowStateCreateRequest) (*models.WorkflowStateResponse, error) {
	// Serialise DurationOptions to JSON if provided
	var durationOptionsJSON string
	if len(req.DurationOptions) > 0 {
		if b, err := json.Marshal(req.DurationOptions); err == nil {
			durationOptionsJSON = string(b)
		}
	}

	state := &models.WorkflowState{
		WorkflowID:                   workflowID,
		Name:                         req.Name,
		NameAr:                       req.NameAr,
		Code:                         req.Code,
		Description:                  req.Description,
		DescriptionAr:                req.DescriptionAr,
		StateType:                    req.StateType,
		Color:                        req.Color,
		PositionX:                    req.PositionX,
		PositionY:                    req.PositionY,
		SLAHours:                     req.SLAHours,
		SLAUnit:                      req.SLAUnit,
		IsMergable:                   req.IsMergable,
		IsAIQA:                       req.IsAIQA,
		IsReadyToClose:               req.IsReadyToClose,
		IsPartialClose:               req.IsPartialClose,
		DurationOptions:              durationOptionsJSON,
		SortOrder:                    req.SortOrder,
		IsActive:                     true,
		AutoMatchUser:                req.AutoMatchUser,
		ManualSelectUser:             req.ManualSelectUser,
		NewIncidentEmailTemplateCode: req.NewIncidentEmailTemplateCode,
		NewIncidentSMSTemplateCode:   req.NewIncidentSMSTemplateCode,
	}
	if req.EscalationPolicyID != nil && *req.EscalationPolicyID != "" {
		if id, err := uuid.Parse(*req.EscalationPolicyID); err == nil {
			state.EscalationPolicyID = &id
		}
	}
	if req.AssignUserID != nil && *req.AssignUserID != "" {
		if id, err := uuid.Parse(*req.AssignUserID); err == nil {
			state.AssignUserID = &id
		}
	}

	if state.StateType == "" {
		state.StateType = "normal"
	}
	if state.Color == "" {
		state.Color = "#6366f1"
	}

	if err := s.repo.CreateState(ctx, state); err != nil {
		return nil, err
	}

	// Parse viewable roles
	viewableRoleIDs := make([]uuid.UUID, 0, len(req.ViewableRoleIDs))
	for _, idStr := range req.ViewableRoleIDs {
		id, err := uuid.Parse(idStr)
		if err != nil {
			continue
		}
		viewableRoleIDs = append(viewableRoleIDs, id)
	}

	// Parse editable roles
	editableRoleIDs := make([]uuid.UUID, 0, len(req.EditableRoleIDs))
	for _, idStr := range req.EditableRoleIDs {
		id, err := uuid.Parse(idStr)
		if err != nil {
			continue
		}
		editableRoleIDs = append(editableRoleIDs, id)
	}

	// A role with edit permission must also have view permission
	viewableSet := make(map[uuid.UUID]bool, len(viewableRoleIDs))
	for _, id := range viewableRoleIDs {
		viewableSet[id] = true
	}
	for _, id := range editableRoleIDs {
		if !viewableSet[id] {
			viewableRoleIDs = append(viewableRoleIDs, id)
			viewableSet[id] = true
		}
	}

	if len(viewableRoleIDs) > 0 {
		if err := s.repo.AssignStateViewableRoles(ctx, state.ID, viewableRoleIDs); err != nil {
			return nil, err
		}
	}

	if len(editableRoleIDs) > 0 {
		if err := s.repo.AssignStateEditableRoles(ctx, state.ID, editableRoleIDs); err != nil {
			return nil, err
		}
	}

	// Assign creation-time assignment roles if provided
	if len(req.AssignmentRoleIDs) > 0 {
		roleIDs := make([]uuid.UUID, 0, len(req.AssignmentRoleIDs))
		for _, idStr := range req.AssignmentRoleIDs {
			id, err := uuid.Parse(idStr)
			if err != nil {
				continue
			}
			roleIDs = append(roleIDs, id)
		}
		if err := s.repo.AssignStateAssignmentRoles(ctx, state.ID, roleIDs); err != nil {
			return nil, err
		}
	}

	// Fetch the state with relations
	created, err := s.repo.FindStateByID(ctx, state.ID)
	if err != nil {
		return nil, err
	}

	resp := models.ToWorkflowStateResponse(created)
	return &resp, nil
}

func (s *workflowService) ListStates(ctx context.Context, workflowID uuid.UUID) ([]models.WorkflowStateResponse, error) {
	states, err := s.repo.ListStatesByWorkflowID(ctx, workflowID)
	if err != nil {
		return nil, err
	}

	responses := make([]models.WorkflowStateResponse, len(states))
	for i, state := range states {
		responses[i] = models.ToWorkflowStateResponse(&state)
	}

	return responses, nil
}

func (s *workflowService) GetState(ctx context.Context, stateID uuid.UUID) (*models.WorkflowStateResponse, error) {
	state, err := s.repo.FindStateByID(ctx, stateID)
	if err != nil {
		return nil, err
	}

	resp := models.ToWorkflowStateResponse(state)
	return &resp, nil
}

func (s *workflowService) UpdateState(ctx context.Context, stateID uuid.UUID, req *models.WorkflowStateUpdateRequest) (*models.WorkflowStateResponse, error) {
	state, err := s.repo.FindStateByID(ctx, stateID)
	if err != nil {
		return nil, err
	}

	if req.Name != "" {
		state.Name = req.Name
	}
	if req.NameAr != "" {
		state.NameAr = req.NameAr
	}
	if req.Code != "" {
		state.Code = req.Code
	}
	if req.Description != "" {
		state.Description = req.Description
	}
	if req.DescriptionAr != "" {
		state.DescriptionAr = req.DescriptionAr
	}
	if req.StateType != "" {
		state.StateType = req.StateType
	}
	if req.Color != "" {
		state.Color = req.Color
	}
	if req.PositionX != nil {
		state.PositionX = *req.PositionX
	}
	if req.PositionY != nil {
		state.PositionY = *req.PositionY
	}
	if req.SLAHours != nil {
		state.SLAHours = req.SLAHours
	}
	if req.SLAUnit != "" {
		state.SLAUnit = req.SLAUnit
	}
	if req.EscalationPolicyID != nil {
		if *req.EscalationPolicyID == "" {
			state.EscalationPolicyID = nil
		} else {
			if id, err := uuid.Parse(*req.EscalationPolicyID); err == nil {
				state.EscalationPolicyID = &id
			}
		}
	}
	if req.IsMergable != nil {
		state.IsMergable = *req.IsMergable
	}
	if req.IsAIQA != nil {
		state.IsAIQA = *req.IsAIQA
	}
	if req.IsReadyToClose != nil {
		state.IsReadyToClose = *req.IsReadyToClose
	}
	if req.IsPartialClose != nil {
		state.IsPartialClose = *req.IsPartialClose
	}
	if req.DurationOptions != nil {
		if len(req.DurationOptions) > 0 {
			if b, err := json.Marshal(req.DurationOptions); err == nil {
				state.DurationOptions = string(b)
			}
		} else {
			state.DurationOptions = ""
		}
	}
	if req.SortOrder != nil {
		state.SortOrder = *req.SortOrder
	}
	if req.IsActive != nil {
		state.IsActive = *req.IsActive
	}
	if req.AutoMatchUser != nil {
		state.AutoMatchUser = *req.AutoMatchUser
	}
	if req.ManualSelectUser != nil {
		state.ManualSelectUser = *req.ManualSelectUser
	}
	if req.AssignUserID != nil {
		if *req.AssignUserID == "" {
			state.AssignUserID = nil
		} else {
			if id, err := uuid.Parse(*req.AssignUserID); err == nil {
				state.AssignUserID = &id
			}
		}
	}
	if req.NewIncidentEmailTemplateCode != nil {
		state.NewIncidentEmailTemplateCode = *req.NewIncidentEmailTemplateCode
	}
	if req.NewIncidentSMSTemplateCode != nil {
		state.NewIncidentSMSTemplateCode = *req.NewIncidentSMSTemplateCode
	}

	if err := s.repo.UpdateState(ctx, state); err != nil {
		return nil, err
	}

	// Determine viewable roles: use provided list, or fall back to the state's existing roles
	// so editable-only updates can still be validated/merged against the current viewable set.
	viewableProvided := req.ViewableRoleIDs != nil
	var viewableRoleIDs []uuid.UUID
	if viewableProvided {
		viewableRoleIDs = make([]uuid.UUID, 0, len(req.ViewableRoleIDs))
		for _, idStr := range req.ViewableRoleIDs {
			id, err := uuid.Parse(idStr)
			if err != nil {
				continue
			}
			viewableRoleIDs = append(viewableRoleIDs, id)
		}
	} else {
		viewableRoleIDs = make([]uuid.UUID, 0, len(state.ViewableRoles))
		for _, r := range state.ViewableRoles {
			viewableRoleIDs = append(viewableRoleIDs, r.ID)
		}
	}

	editableProvided := req.EditableRoleIDs != nil
	var editableRoleIDs []uuid.UUID
	if editableProvided {
		editableRoleIDs = make([]uuid.UUID, 0, len(req.EditableRoleIDs))
		for _, idStr := range req.EditableRoleIDs {
			id, err := uuid.Parse(idStr)
			if err != nil {
				continue
			}
			editableRoleIDs = append(editableRoleIDs, id)
		}
	}

	// A role with edit permission must also have view permission
	if editableProvided {
		viewableSet := make(map[uuid.UUID]bool, len(viewableRoleIDs))
		for _, id := range viewableRoleIDs {
			viewableSet[id] = true
		}
		for _, id := range editableRoleIDs {
			if !viewableSet[id] {
				viewableRoleIDs = append(viewableRoleIDs, id)
				viewableSet[id] = true
				viewableProvided = true // editable roles forced an addition; persist the updated viewable set
			}
		}
	}

	if viewableProvided {
		if err := s.repo.AssignStateViewableRoles(ctx, stateID, viewableRoleIDs); err != nil {
			return nil, err
		}
	}

	if editableProvided {
		if err := s.repo.AssignStateEditableRoles(ctx, stateID, editableRoleIDs); err != nil {
			return nil, err
		}
	}

	// Update creation-time assignment roles if provided (nil = no change, empty = clear all)
	if req.AssignmentRoleIDs != nil {
		roleIDs := make([]uuid.UUID, 0, len(req.AssignmentRoleIDs))
		for _, idStr := range req.AssignmentRoleIDs {
			id, err := uuid.Parse(idStr)
			if err != nil {
				continue
			}
			roleIDs = append(roleIDs, id)
		}
		if err := s.repo.AssignStateAssignmentRoles(ctx, stateID, roleIDs); err != nil {
			return nil, err
		}
	}

	// Fetch the state with relations
	updated, err := s.repo.FindStateByID(ctx, stateID)
	if err != nil {
		return nil, err
	}

	resp := models.ToWorkflowStateResponse(updated)
	return &resp, nil
}

func (s *workflowService) DeleteState(ctx context.Context, stateID uuid.UUID) error {
	return s.repo.DeleteState(ctx, stateID)
}

// Transition management

func (s *workflowService) CreateTransition(ctx context.Context, workflowID uuid.UUID, req *models.WorkflowTransitionCreateRequest) (*models.WorkflowTransitionResponse, error) {
	fromStateID, err := uuid.Parse(req.FromStateID)
	if err != nil {
		return nil, errors.New("invalid from_state_id")
	}

	toStateID, err := uuid.Parse(req.ToStateID)
	if err != nil {
		return nil, errors.New("invalid to_state_id")
	}

	transition := &models.WorkflowTransition{
		WorkflowID:           workflowID,
		Name:                 req.Name,
		NameAr:               req.NameAr,
		Code:                 req.Code,
		Description:          req.Description,
		DescriptionAr:        req.DescriptionAr,
		FromStateID:          fromStateID,
		ToStateID:            toStateID,
		SortOrder:            req.SortOrder,
		IsActive:             true,
		IsRejection:          req.IsRejection,
		IsNotBelong:          req.IsNotBelong,
		IsMissingInfo:        req.IsMissingInfo,
		IsReopen:             req.IsReopen,
		IsFinalClose:         req.IsFinalClose,
		RequireAssignee:      req.RequireAssignee,
		AutoDetectDepartment: req.AutoDetectDepartment,
		DepartmentTypeFilter: req.DepartmentTypeFilter,
		AutoMatchUser:        req.AutoMatchUser,
		ManualSelectUser:     req.ManualSelectUser,
	}

	// Department Assignment
	if req.AssignDepartmentID != nil && *req.AssignDepartmentID != "" {
		deptID, err := uuid.Parse(*req.AssignDepartmentID)
		if err == nil {
			transition.AssignDepartmentID = &deptID
		}
	}

	// User Assignment
	if req.AssignUserID != nil && *req.AssignUserID != "" {
		userID, err := uuid.Parse(*req.AssignUserID)
		if err == nil {
			transition.AssignUserID = &userID
		}
	}

	if err := s.repo.CreateTransition(ctx, transition); err != nil {
		return nil, err
	}

	// Assign assignment roles if provided
	if len(req.AssignmentRoleIDs) > 0 {
		assignRoleIDs := make([]uuid.UUID, 0, len(req.AssignmentRoleIDs))
		for _, idStr := range req.AssignmentRoleIDs {
			id, err := uuid.Parse(idStr)
			if err != nil {
				continue
			}
			assignRoleIDs = append(assignRoleIDs, id)
		}
		if err := s.repo.AssignTransitionAssignmentRoles(ctx, transition.ID, assignRoleIDs); err != nil {
			return nil, err
		}
	}

	// Assign allowed roles if provided
	if len(req.RoleIDs) > 0 {
		roleIDs := make([]uuid.UUID, 0, len(req.RoleIDs))
		for _, idStr := range req.RoleIDs {
			id, err := uuid.Parse(idStr)
			if err != nil {
				continue
			}
			roleIDs = append(roleIDs, id)
		}
		if err := s.repo.AssignTransitionRoles(ctx, transition.ID, roleIDs); err != nil {
			return nil, err
		}
	}

	created, err := s.repo.FindTransitionByIDWithRelations(ctx, transition.ID)
	if err != nil {
		return nil, err
	}

	resp := models.ToWorkflowTransitionResponse(created)
	return &resp, nil
}

func (s *workflowService) ListTransitions(ctx context.Context, workflowID uuid.UUID) ([]models.WorkflowTransitionResponse, error) {
	transitions, err := s.repo.ListTransitionsByWorkflowID(ctx, workflowID)
	if err != nil {
		return nil, err
	}

	responses := make([]models.WorkflowTransitionResponse, len(transitions))
	for i, trans := range transitions {
		responses[i] = models.ToWorkflowTransitionResponse(&trans)
	}

	return responses, nil
}

func (s *workflowService) GetTransition(ctx context.Context, transitionID uuid.UUID) (*models.WorkflowTransitionResponse, error) {
	transition, err := s.repo.FindTransitionByIDWithRelations(ctx, transitionID)
	if err != nil {
		return nil, err
	}

	resp := models.ToWorkflowTransitionResponse(transition)
	return &resp, nil
}

func (s *workflowService) UpdateTransition(ctx context.Context, transitionID uuid.UUID, req *models.WorkflowTransitionUpdateRequest) (*models.WorkflowTransitionResponse, error) {
	transition, err := s.repo.FindTransitionByID(ctx, transitionID)
	if err != nil {
		return nil, err
	}

	if req.Name != "" {
		transition.Name = req.Name
	}
	if req.NameAr != "" {
		transition.NameAr = req.NameAr
	}
	if req.Code != "" {
		transition.Code = req.Code
	}
	if req.Description != "" {
		transition.Description = req.Description
	}
	if req.DescriptionAr != "" {
		transition.DescriptionAr = req.DescriptionAr
	}
	if req.FromStateID != "" {
		fromStateID, err := uuid.Parse(req.FromStateID)
		if err == nil {
			transition.FromStateID = fromStateID
		}
	}
	if req.ToStateID != "" {
		toStateID, err := uuid.Parse(req.ToStateID)
		if err == nil {
			transition.ToStateID = toStateID
		}
	}
	if req.SortOrder != nil {
		transition.SortOrder = *req.SortOrder
	}
	if req.IsActive != nil {
		transition.IsActive = *req.IsActive
	}
	if req.IsRejection != nil {
		transition.IsRejection = *req.IsRejection
	}

	if req.IsNotBelong != nil {
		transition.IsNotBelong = *req.IsNotBelong
	}
	if req.IsMissingInfo != nil {
		transition.IsMissingInfo = *req.IsMissingInfo
	}
	if req.IsReopen != nil {
		transition.IsReopen = *req.IsReopen
	}
	if req.IsFinalClose != nil {
		transition.IsFinalClose = *req.IsFinalClose
	}
	if req.RequireAssignee != nil {
		transition.RequireAssignee = *req.RequireAssignee
	}
	// Department Assignment
	if req.AutoDetectDepartment != nil {
		transition.AutoDetectDepartment = *req.AutoDetectDepartment
	}
	if req.DepartmentTypeFilter != nil {
		transition.DepartmentTypeFilter = *req.DepartmentTypeFilter
	}
	if req.AssignDepartmentID != nil {
		if *req.AssignDepartmentID == "" {
			transition.AssignDepartmentID = nil
		} else {
			deptID, err := uuid.Parse(*req.AssignDepartmentID)
			if err == nil {
				transition.AssignDepartmentID = &deptID
			}
		}
	}

	// User Assignment
	if req.AutoMatchUser != nil {
		transition.AutoMatchUser = *req.AutoMatchUser
	}
	if req.ManualSelectUser != nil {
		transition.ManualSelectUser = *req.ManualSelectUser
	}
	if req.AssignUserID != nil {
		if *req.AssignUserID == "" {
			transition.AssignUserID = nil
		} else {
			userID, err := uuid.Parse(*req.AssignUserID)
			if err == nil {
				transition.AssignUserID = &userID
			}
		}
	}
	if err := s.repo.UpdateTransition(ctx, transition); err != nil {
		return nil, err
	}

	// Update assignment roles if provided (nil slice = no change, empty slice = clear)
	if req.AssignmentRoleIDs != nil {
		assignRoleIDs := make([]uuid.UUID, 0, len(req.AssignmentRoleIDs))
		for _, idStr := range req.AssignmentRoleIDs {
			id, err := uuid.Parse(idStr)
			if err != nil {
				continue
			}
			assignRoleIDs = append(assignRoleIDs, id)
		}
		if err := s.repo.AssignTransitionAssignmentRoles(ctx, transitionID, assignRoleIDs); err != nil {
			return nil, err
		}
	}

	// Update allowed roles if provided
	if req.RoleIDs != nil {
		roleIDs := make([]uuid.UUID, 0, len(req.RoleIDs))
		for _, idStr := range req.RoleIDs {
			id, err := uuid.Parse(idStr)
			if err != nil {
				continue
			}
			roleIDs = append(roleIDs, id)
		}
		if err := s.repo.AssignTransitionRoles(ctx, transitionID, roleIDs); err != nil {
			return nil, err
		}
	}

	updated, err := s.repo.FindTransitionByIDWithRelations(ctx, transitionID)
	if err != nil {
		return nil, err
	}

	resp := models.ToWorkflowTransitionResponse(updated)
	return &resp, nil
}

func (s *workflowService) DeleteTransition(ctx context.Context, transitionID uuid.UUID) error {
	return s.repo.DeleteTransition(ctx, transitionID)
}

// Transition configuration

func (s *workflowService) SetTransitionRoles(ctx context.Context, transitionID uuid.UUID, roleIDs []uuid.UUID) error {
	return s.repo.AssignTransitionRoles(ctx, transitionID, roleIDs)
}

func (s *workflowService) SetTransitionRequirements(ctx context.Context, transitionID uuid.UUID, reqData []models.TransitionRequirementRequest) error {
	requirements := make([]models.TransitionRequirement, len(reqData))
	for i, req := range reqData {
		requirements[i] = models.TransitionRequirement{
			RequirementType: req.RequirementType,
			FieldName:       req.FieldName,
			FieldValue:      req.FieldValue,
			IsMandatory:     req.IsMandatory,
			ErrorMessage:    req.ErrorMessage,
		}
	}
	return s.repo.SetTransitionRequirements(ctx, transitionID, requirements)
}

func (s *workflowService) SetTransitionActions(ctx context.Context, transitionID uuid.UUID, actionData []models.TransitionActionRequest) error {
	actions := make([]models.TransitionAction, len(actionData))
	for i, action := range actionData {
		actions[i] = models.TransitionAction{
			ActionType:     action.ActionType,
			Name:           action.Name,
			Description:    action.Description,
			Config:         action.Config,
			ExecutionOrder: action.ExecutionOrder,
			IsAsync:        action.IsAsync,
			IsActive:       action.IsActive,
		}
	}
	return s.repo.SetTransitionActions(ctx, transitionID, actions)
}

func (s *workflowService) SetTransitionFieldChanges(ctx context.Context, transitionID uuid.UUID, data []models.TransitionFieldChangeRequest) error {
	fieldChanges := make([]models.TransitionFieldChange, len(data))
	for i, fc := range data {
		fieldChanges[i] = models.TransitionFieldChange{
			FieldName:            fc.FieldName,
			Label:                fc.Label,
			IsRequired:           fc.IsRequired,
			DepartmentTypeFilter: fc.DepartmentTypeFilter,
			SortOrder:            fc.SortOrder,
		}
	}
	return s.repo.SetTransitionFieldChanges(ctx, transitionID, fieldChanges)
}

// Get transitions from a state (for incident transition UI)

func (s *workflowService) GetTransitionsFromState(ctx context.Context, stateID uuid.UUID) ([]models.WorkflowTransitionResponse, error) {
	transitions, err := s.repo.ListTransitionsFromState(ctx, stateID)
	if err != nil {
		return nil, err
	}

	responses := make([]models.WorkflowTransitionResponse, len(transitions))
	for i, trans := range transitions {
		responses[i] = models.ToWorkflowTransitionResponse(&trans)
	}

	return responses, nil
}

// Get transitions to a state (for incident transition UI)

func (s *workflowService) GetTransitionsToState(ctx context.Context, stateID uuid.UUID) ([]models.WorkflowTransitionResponse, error) {
	transitions, err := s.repo.ListTransitionsToState(ctx, stateID)
	if err != nil {
		return nil, err
	}

	responses := make([]models.WorkflowTransitionResponse, len(transitions))
	for i, trans := range transitions {
		responses[i] = models.ToWorkflowTransitionResponse(&trans)
	}

	return responses, nil
}
func (s *workflowService) GetInitialState(ctx context.Context, workflowID uuid.UUID) (*models.WorkflowStateResponse, error) {
	state, err := s.repo.GetInitialState(ctx, workflowID)
	if err != nil {
		return nil, err
	}

	resp := models.ToWorkflowStateResponse(state)
	return &resp, nil
}

func (s *workflowService) GetInitialStateMatchingUsers(ctx context.Context, workflowID uuid.UUID, classificationID, locationID, departmentID *uuid.UUID) ([]models.UserResponse, error) {
	state, err := s.repo.GetInitialState(ctx, workflowID)
	if err != nil {
		return nil, err
	}

	if !state.ManualSelectUser || len(state.AssignmentRoles) == 0 {
		return []models.UserResponse{}, nil
	}

	roleIDs := make([]uuid.UUID, len(state.AssignmentRoles))
	for i, r := range state.AssignmentRoles {
		roleIDs[i] = r.ID
	}

	users, err := s.userRepo.FindMatching(ctx, roleIDs, classificationID, locationID, departmentID, nil)
	if err != nil {
		return nil, err
	}

	result := make([]models.UserResponse, len(users))
	for i, u := range users {
		result[i] = models.ToUserResponse(&u)
	}
	return result, nil
}

// workflowMatchesRecordType checks if a workflow's RecordType is compatible with the requested record type.
func workflowMatchesRecordType(workflowRT, requestRT string) bool {
	if workflowRT == "all" {
		return true
	}
	if workflowRT == "both" {
		return requestRT == "incident" || requestRT == "request"
	}
	return workflowRT == requestRT
}

// MatchWorkflow finds a workflow based on incident criteria and returns form configuration
func (s *workflowService) MatchWorkflow(ctx context.Context, req *models.WorkflowMatchRequest) (*models.WorkflowMatchResponse, error) {
	// Get all active workflows with their classifications
	workflows, err := s.repo.List(ctx, true)
	if err != nil {
		return nil, err
	}

	// All available form fields with their labels and descriptions
	allFormFields := []models.IncidentFormFieldConfig{
		{Field: "title", Label: "Title", Description: "Brief description of the incident", IsRequired: true},
		{Field: "description", Label: "Description", Description: "Detailed incident description", IsRequired: false},
		{Field: "classification_id", Label: "Classification", Description: "Incident category/type", IsRequired: false},
		{Field: "priority", Label: "Priority", Description: "Urgency level", IsRequired: false},
		{Field: "source", Label: "Source", Description: "Where the incident originated", IsRequired: false},
		{Field: "assignee_id", Label: "Assignee", Description: "User assigned to handle", IsRequired: false},
		{Field: "department_id", Label: "Department", Description: "Responsible department", IsRequired: false},
		{Field: "location_id", Label: "Location", Description: "Physical location", IsRequired: false},
		{Field: "due_date", Label: "Due Date", Description: "Resolution deadline", IsRequired: false},
		{Field: "reporter_name", Label: "Reporter Name", Description: "Name of person reporting", IsRequired: false},
		{Field: "reporter_email", Label: "Reporter Email", Description: "Email of person reporting", IsRequired: false},
	}

	// Default response when no workflow matches
	defaultResponse := &models.WorkflowMatchResponse{
		Matched:        false,
		RequiredFields: []string{"title"},
		FormFields:     allFormFields,
	}

	if len(workflows) == 0 {
		return defaultResponse, nil
	}

	// Filter by RecordType first — only consider workflows that have RecordType configured
	var eligible []models.Workflow
	for _, w := range workflows {
		if !w.IsActive {
			continue
		}
		// Skip workflows with no RecordType set — they don't participate in matching
		if w.RecordType == "" {
			continue
		}
		// Check if workflow's RecordType is compatible with the requested record type
		if req.RecordType != "" && !workflowMatchesRecordType(w.RecordType, req.RecordType) {
			continue
		}
		eligible = append(eligible, w)
	}

	if len(eligible) == 0 {
		return defaultResponse, nil
	}

	// Parse classification ID if provided
	var classificationID uuid.UUID
	if req.ClassificationID != "" {
		classificationID, _ = uuid.Parse(req.ClassificationID)
	}

	// Parse location ID if provided
	var locationID uuid.UUID
	if req.LocationID != "" {
		locationID, _ = uuid.Parse(req.LocationID)
	}

	// Find matching workflow
	var matchedWorkflow *models.Workflow
	var highestScore int
	var matchedIsDefault bool

	for i := range eligible {
		w := &eligible[i]

		score := 0

		// Check classification match
		if classificationID != uuid.Nil && len(w.Classifications) > 0 {
			for _, c := range w.Classifications {
				if c.ID == classificationID {
					score += 10 // Classification is a strong match
					break
				}
			}
		}

		// Check location match
		if locationID != uuid.Nil && len(w.Locations) > 0 {
			for _, loc := range w.Locations {
				if loc.ID == locationID {
					score += 10 // Location is a strong match
					break
				}
			}
		}

		// Specificity bonus: exact record_type match beats broad ("all"/"both")
		if req.RecordType != "" && w.RecordType == req.RecordType {
			score += 5
		}

		// Default workflow bonus
		if w.IsDefault {
			score += 1
		}

		// Tie-breaking: higher score wins; on tie, prefer IsDefault over non-default
		if score > highestScore || (score == highestScore && !matchedIsDefault && w.IsDefault) || (score == highestScore && matchedWorkflow == nil) {
			highestScore = score
			matchedWorkflow = w
			matchedIsDefault = w.IsDefault
		}
	}

	// If no workflow matched by criteria, use the default workflow (from eligible set)
	if matchedWorkflow == nil {
		for i := range eligible {
			if eligible[i].IsDefault {
				matchedWorkflow = &eligible[i]
				break
			}
		}
	}

	// If still no workflow, use the first eligible one
	if matchedWorkflow == nil && len(eligible) > 0 {
		matchedWorkflow = &eligible[0]
	}

	if matchedWorkflow == nil {
		return defaultResponse, nil
	}

	// Get the full workflow with relations
	fullWorkflow, err := s.repo.FindByIDWithRelations(ctx, matchedWorkflow.ID)
	if err != nil {
		return defaultResponse, nil
	}

	// Parse required fields from workflow
	var requiredFields []string
	if fullWorkflow.RequiredFields != "" {
		json.Unmarshal([]byte(fullWorkflow.RequiredFields), &requiredFields)
	}
	// Title is always required
	requiredFields = append([]string{"title"}, requiredFields...)

	// Parse optional fields from workflow
	var optionalFields []string
	if fullWorkflow.OptionalFields != "" {
		json.Unmarshal([]byte(fullWorkflow.OptionalFields), &optionalFields)
	}

	// Build a set of fields to include in the form (required + optional)
	formFieldSet := make(map[string]bool)
	for _, f := range requiredFields {
		formFieldSet[f] = true
	}
	for _, f := range optionalFields {
		formFieldSet[f] = true
	}

	// Update form fields with required/optional status
	formFields := make([]models.IncidentFormFieldConfig, 0, len(allFormFields))
	for _, f := range allFormFields {
		if !formFieldSet[f.Field] {
			continue
		}
		fc := f
		for _, rf := range requiredFields {
			if rf == f.Field {
				fc.IsRequired = true
				break
			}
		}
		formFields = append(formFields, fc)
	}

	// Get initial state
	var initialStateID, initialStateName *string
	initialState, err := s.repo.GetInitialState(ctx, fullWorkflow.ID)
	if err == nil && initialState != nil {
		stateIDStr := initialState.ID.String()
		initialStateID = &stateIDStr
		initialStateName = &initialState.Name
	}

	// Build response
	workflowIDStr := fullWorkflow.ID.String()
	recordType := fullWorkflow.RecordType
	if optionalFields == nil {
		optionalFields = []string{}
	}
	response := &models.WorkflowMatchResponse{
		Matched:        true,
		WorkflowID:     &workflowIDStr,
		WorkflowName:   &fullWorkflow.Name,
		WorkflowCode:   &fullWorkflow.Code,
		RecordType:     &recordType,
		RequiredFields: requiredFields,
		OptionalFields: optionalFields,
		FormFields:     formFields,
		InitialStateID: initialStateID,
		InitialState:   initialStateName,
	}

	return response, nil
}

// ResolveWorkflow picks the best-matching workflow UUID for an epmportal incident.
// It delegates to MatchWorkflow for scoring, then falls back to GetDefaultWorkflow.
func (s *workflowService) ResolveWorkflow(ctx context.Context, locationID, classificationID *uuid.UUID, recordType string) (uuid.UUID, error) {
	req := &models.WorkflowMatchRequest{RecordType: recordType}
	if locationID != nil {
		req.LocationID = locationID.String()
	}
	if classificationID != nil {
		req.ClassificationID = classificationID.String()
	}

	result, err := s.MatchWorkflow(ctx, req)
	if err != nil {
		return uuid.Nil, err
	}

	if result.Matched && result.WorkflowID != nil {
		id, err := uuid.Parse(*result.WorkflowID)
		if err == nil {
			return id, nil
		}
	}

	// Fall back to the global default workflow
	defaultWf, err := s.repo.GetDefaultWorkflow(ctx)
	if err != nil {
		return uuid.Nil, errors.New("no workflow found and no default workflow configured")
	}
	return defaultWf.ID, nil
}

// ExportWorkflow exports a workflow as JSON with all related data
func (s *workflowService) ExportWorkflow(ctx context.Context, id uuid.UUID) ([]byte, string, error) {
	// Load workflow with all relations
	workflow, err := s.repo.FindByIDWithRelations(ctx, id)
	if err != nil {
		return nil, "", err
	}

	// Parse required fields
	var requiredFields []string
	if workflow.RequiredFields != "" {
		json.Unmarshal([]byte(workflow.RequiredFields), &requiredFields)
	}
	if requiredFields == nil {
		requiredFields = []string{}
	}

	// Build state code to ID mapping for transitions
	stateCodeMap := make(map[uuid.UUID]string)
	exportStates := make([]models.WorkflowStateExport, len(workflow.States))
	for i, state := range workflow.States {
		stateCodeMap[state.ID] = state.Code

		// Convert viewable roles to code/name pairs
		viewableRoles := make([]models.CodeNamePair, len(state.ViewableRoles))
		for j, role := range state.ViewableRoles {
			viewableRoles[j] = models.CodeNamePair{
				Code: role.Code,
				Name: role.Name,
			}
		}

		// Convert editable roles to code/name pairs
		editableRoles := make([]models.CodeNamePair, len(state.EditableRoles))
		for j, role := range state.EditableRoles {
			editableRoles[j] = models.CodeNamePair{
				Code: role.Code,
				Name: role.Name,
			}
		}

		exportStates[i] = models.WorkflowStateExport{
			Name:          state.Name,
			Code:          state.Code,
			Description:   state.Description,
			StateType:     state.StateType,
			Color:         state.Color,
			PositionX:     state.PositionX,
			PositionY:     state.PositionY,
			SLAHours:      state.SLAHours,
			SLAUnit:       state.SLAUnit,
			SortOrder:     state.SortOrder,
			ViewableRoles: viewableRoles,
			EditableRoles: editableRoles,
		}
	}

	// Build transitions with codes
	exportTransitions := make([]models.WorkflowTransitionExport, len(workflow.Transitions))
	for i, trans := range workflow.Transitions {
		// Convert allowed roles to code/name pairs
		allowedRoles := make([]models.CodeNamePair, len(trans.AllowedRoles))
		for j, role := range trans.AllowedRoles {
			allowedRoles[j] = models.CodeNamePair{
				Code: role.Code,
				Name: role.Name,
			}
		}

		// Convert department to code/name pair
		var assignDepartment *models.CodeNamePair
		if trans.AssignDepartment != nil {
			assignDepartment = &models.CodeNamePair{
				Code: trans.AssignDepartment.Code,
				Name: trans.AssignDepartment.Name,
			}
		}

		// Convert assign user to code/name pair (use email as code)
		var assignUser *models.CodeNamePair
		if trans.AssignUser != nil {
			fullName := trans.AssignUser.FirstName
			if trans.AssignUser.LastName != "" {
				if fullName != "" {
					fullName += " "
				}
				fullName += trans.AssignUser.LastName
			}
			if fullName == "" {
				fullName = trans.AssignUser.Username
			}
			assignUser = &models.CodeNamePair{
				Code: trans.AssignUser.Email,
				Name: fullName,
			}
		}

		// Convert assignment roles to code/name pairs
		var assignmentRoles []models.CodeNamePair
		for _, r := range trans.AssignmentRoles {
			assignmentRoles = append(assignmentRoles, models.CodeNamePair{
				Code: r.Code,
				Name: r.Name,
			})
		}

		// Convert requirements
		requirements := make([]models.TransitionRequirementExport, len(trans.Requirements))
		for j, req := range trans.Requirements {
			requirements[j] = models.TransitionRequirementExport{
				RequirementType: req.RequirementType,
				FieldName:       req.FieldName,
				FieldValue:      req.FieldValue,
				IsMandatory:     req.IsMandatory,
				ErrorMessage:    req.ErrorMessage,
			}
		}

		// Convert actions
		actions := make([]models.TransitionActionExport, len(trans.Actions))
		for j, action := range trans.Actions {
			actions[j] = models.TransitionActionExport{
				ActionType:     action.ActionType,
				Name:           action.Name,
				Description:    action.Description,
				Config:         action.Config,
				ExecutionOrder: action.ExecutionOrder,
				IsAsync:        action.IsAsync,
				IsActive:       action.IsActive,
			}
		}

		exportTransitions[i] = models.WorkflowTransitionExport{
			Name:                 trans.Name,
			Code:                 trans.Code,
			Description:          trans.Description,
			FromStateCode:        stateCodeMap[trans.FromStateID],
			ToStateCode:          stateCodeMap[trans.ToStateID],
			AllowedRoles:         allowedRoles,
			AssignDepartment:     assignDepartment,
			AutoDetectDepartment: trans.AutoDetectDepartment,
			AssignUser:           assignUser,
			AssignmentRoles:      assignmentRoles,
			AutoMatchUser:        trans.AutoMatchUser,
			ManualSelectUser:     trans.ManualSelectUser,
			Requirements:         requirements,
			Actions:              actions,
			SortOrder:            trans.SortOrder,
		}
	}

	// Convert classifications to code/name pairs (use name as code since no code field)
	classifications := make([]models.CodeNamePair, len(workflow.Classifications))
	for i, class := range workflow.Classifications {
		classifications[i] = models.CodeNamePair{
			Code: class.Name,
			Name: class.Name,
		}
	}

	// Convert convert-to-request roles
	convertRoles := make([]models.CodeNamePair, len(workflow.ConvertToRequestRoles))
	for i, role := range workflow.ConvertToRequestRoles {
		convertRoles[i] = models.CodeNamePair{
			Code: role.Code,
			Name: role.Name,
		}
	}

	// Parse sources and priorities
	var sources []string
	if workflow.Sources != "" {
		json.Unmarshal([]byte(workflow.Sources), &sources)
	}

	var priorities []int
	if workflow.Priorities != "" {
		json.Unmarshal([]byte(workflow.Priorities), &priorities)
	}

	// Build export structure
	exportData := models.WorkflowExportData{
		ExportVersion: "1.0",
		ExportedAt:    time.Now().Format(time.RFC3339),
		Workflow: models.WorkflowExportContent{
			Name:                  workflow.Name,
			Code:                  workflow.Code,
			Description:           workflow.Description,
			RecordType:            workflow.RecordType,
			Sources:               sources,
			Priorities:            priorities,
			RequiredFields:        requiredFields,
			States:                exportStates,
			Transitions:           exportTransitions,
			Classifications:       classifications,
			ConvertToRequestRoles: convertRoles,
		},
	}

	// Marshal to pretty-printed JSON
	jsonBytes, err := json.MarshalIndent(exportData, "", "  ")
	if err != nil {
		return nil, "", err
	}

	// Generate filename
	timestamp := time.Now().Format("20060102_150405")
	filename := fmt.Sprintf("workflow_%s_%s.json", workflow.Code, timestamp)

	return jsonBytes, filename, nil
}

// ImportWorkflow imports a workflow from JSON data
func (s *workflowService) ImportWorkflow(ctx context.Context, data *models.WorkflowImportData, createdByID uuid.UUID) (*models.WorkflowResponse, []string, error) {
	warnings := []string{}

	// Validate export version
	if data.ExportVersion != "1.0" {
		return nil, nil, fmt.Errorf("unsupported export version: %s", data.ExportVersion)
	}

	// Validate required fields
	if data.Workflow.Name == "" || data.Workflow.Code == "" {
		return nil, nil, errors.New("workflow name and code are required")
	}
	if len(data.Workflow.States) == 0 {
		return nil, nil, errors.New("workflow must have at least one state")
	}

	codeOrName := []string{data.Workflow.Code, data.Workflow.Name}
	exist, err := s.repo.ExistsByCodeOrName(ctx, codeOrName)
	if err != nil {

		return nil, nil, err
	}

	if exist {
		// Append timestamp to make it unique
		timestamp := time.Now().Format("20060102_150405")
		data.Workflow.Code = fmt.Sprintf("%s_imported_%s", data.Workflow.Code, timestamp)
		data.Workflow.Name = fmt.Sprintf("%s (Imported %s)", data.Workflow.Name, timestamp)
		warnings = append(warnings, fmt.Sprintf("Workflow code was modified to '%s' to avoid duplicate", data.Workflow.Code))
	}

	// Validate at least one initial state exists
	hasInitialState := false
	for _, state := range data.Workflow.States {
		if state.StateType == "initial" {
			hasInitialState = true
			break
		}
	}
	if !hasInitialState {
		return nil, nil, errors.New("workflow must have at least one initial state")
	}

	// Check for duplicate workflow code
	workflowCode := data.Workflow.Code
	existing, _ := s.repo.FindByCode(ctx, workflowCode)
	if existing != nil {
		// Append timestamp to make it unique
		timestamp := time.Now().Format("20060102_150405")
		workflowCode = fmt.Sprintf("%s_imported_%s", workflowCode, timestamp)
		warnings = append(warnings, fmt.Sprintf("Workflow code was modified to '%s' to avoid duplicate", workflowCode))
	}

	// Start transaction
	tx := s.db.Begin()
	defer func() {
		if r := recover(); r != nil {
			tx.Rollback()
		}
	}()

	if tx.Error != nil {
		return nil, nil, tx.Error
	}

	// Resolve classification codes to IDs
	classificationIDs := []uuid.UUID{}
	for _, class := range data.Workflow.Classifications {
		var classification models.Classification
		err := tx.Where("name = ?", class.Code).First(&classification).Error
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				warnings = append(warnings, fmt.Sprintf("Classification '%s' not found and will be skipped", class.Name))
				continue
			}
			tx.Rollback()
			return nil, nil, err
		}
		classificationIDs = append(classificationIDs, classification.ID)
	}

	// Resolve convert-to-request role codes to IDs
	convertRoleIDs := []uuid.UUID{}
	for _, role := range data.Workflow.ConvertToRequestRoles {
		foundRole, err := s.roleRepo.FindByCode(ctx, role.Code)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("Role '%s' not found for convert-to-request permission", role.Name))
			continue
		}
		convertRoleIDs = append(convertRoleIDs, foundRole.ID)
	}

	// Create workflow
	requiredFieldsJSON, _ := json.Marshal(data.Workflow.RequiredFields)
	sourcesJSON, _ := json.Marshal(data.Workflow.Sources)
	prioritiesJSON, _ := json.Marshal(data.Workflow.Priorities)

	workflow := &models.Workflow{
		ID:             uuid.New(),
		Name:           data.Workflow.Name,
		Code:           workflowCode,
		Description:    data.Workflow.Description,
		RecordType:     data.Workflow.RecordType,
		Sources:        string(sourcesJSON),
		Priorities:     string(prioritiesJSON),
		RequiredFields: string(requiredFieldsJSON),
		CreatedByID:    &createdByID,
		IsActive:       false, // Start as inactive
		Version:        1,
	}

	if err := tx.Create(workflow).Error; err != nil {
		tx.Rollback()
		return nil, nil, err
	}

	// Assign classifications
	if len(classificationIDs) > 0 {
		if err := tx.Exec("INSERT INTO workflow_classifications (workflow_id, classification_id) VALUES " +
			buildBulkInsertValues(workflow.ID, classificationIDs)).Error; err != nil {
			tx.Rollback()
			return nil, nil, err
		}
	}

	// Assign convert-to-request roles
	if len(convertRoleIDs) > 0 {
		if err := tx.Exec("INSERT INTO workflow_convert_to_request_roles (workflow_id, role_id) VALUES " +
			buildBulkInsertValues(workflow.ID, convertRoleIDs)).Error; err != nil {
			tx.Rollback()
			return nil, nil, err
		}
	}

	// Create states and build code to ID mapping
	stateCodeToID := make(map[string]uuid.UUID)
	for _, stateData := range data.Workflow.States {
		state := &models.WorkflowState{
			ID:          uuid.New(),
			WorkflowID:  workflow.ID,
			Name:        stateData.Name,
			Code:        stateData.Code,
			Description: stateData.Description,
			StateType:   stateData.StateType,
			Color:       stateData.Color,
			PositionX:   stateData.PositionX,
			PositionY:   stateData.PositionY,
			SLAHours:    stateData.SLAHours,
			SLAUnit:     stateData.SLAUnit,
			SortOrder:   stateData.SortOrder,
			IsActive:    true,
		}

		if err := tx.Create(state).Error; err != nil {
			tx.Rollback()
			return nil, nil, err
		}

		stateCodeToID[stateData.Code] = state.ID

		// Resolve viewable roles
		viewableRoleIDs := []uuid.UUID{}
		for _, roleRef := range stateData.ViewableRoles {
			role, err := s.roleRepo.FindByCode(ctx, roleRef.Code)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("Role '%s' not found for state '%s'", roleRef.Name, stateData.Name))
				continue
			}
			viewableRoleIDs = append(viewableRoleIDs, role.ID)
		}

		// Resolve editable roles
		editableRoleIDs := []uuid.UUID{}
		for _, roleRef := range stateData.EditableRoles {
			role, err := s.roleRepo.FindByCode(ctx, roleRef.Code)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("Role '%s' not found for editable_roles in state '%s'", roleRef.Name, stateData.Name))
				continue
			}
			editableRoleIDs = append(editableRoleIDs, role.ID)
		}

		// A role with edit permission must also have view permission
		viewableSet := make(map[uuid.UUID]bool, len(viewableRoleIDs))
		for _, id := range viewableRoleIDs {
			viewableSet[id] = true
		}
		for _, id := range editableRoleIDs {
			if !viewableSet[id] {
				viewableRoleIDs = append(viewableRoleIDs, id)
				viewableSet[id] = true
			}
		}

		if len(viewableRoleIDs) > 0 {
			if err := tx.Exec("INSERT INTO state_viewable_roles (workflow_state_id, role_id) VALUES " +
				buildBulkInsertValues(state.ID, viewableRoleIDs)).Error; err != nil {
				tx.Rollback()
				return nil, nil, err
			}
		}

		if len(editableRoleIDs) > 0 {
			if err := tx.Exec("INSERT INTO state_editable_roles (workflow_state_id, role_id) VALUES " +
				buildBulkInsertValues(state.ID, editableRoleIDs)).Error; err != nil {
				tx.Rollback()
				return nil, nil, err
			}
		}
	}

	// Create transitions
	for _, transData := range data.Workflow.Transitions {
		fromStateID, ok := stateCodeToID[transData.FromStateCode]
		if !ok {
			tx.Rollback()
			return nil, nil, fmt.Errorf("invalid from_state_code: %s", transData.FromStateCode)
		}

		toStateID, ok := stateCodeToID[transData.ToStateCode]
		if !ok {
			tx.Rollback()
			return nil, nil, fmt.Errorf("invalid to_state_code: %s", transData.ToStateCode)
		}

		transition := &models.WorkflowTransition{
			ID:                   uuid.New(),
			WorkflowID:           workflow.ID,
			Name:                 transData.Name,
			Code:                 transData.Code,
			Description:          transData.Description,
			FromStateID:          fromStateID,
			ToStateID:            toStateID,
			AutoDetectDepartment: transData.AutoDetectDepartment,
			AutoMatchUser:        transData.AutoMatchUser,
			ManualSelectUser:     transData.ManualSelectUser,
			SortOrder:            transData.SortOrder,
			IsActive:             true,
		}

		// Resolve department
		if transData.AssignDepartment != nil {
			dept, err := s.deptRepo.FindByCode(ctx, transData.AssignDepartment.Code)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("Department '%s' not found for transition '%s'", transData.AssignDepartment.Name, transData.Name))
			} else {
				transition.AssignDepartmentID = &dept.ID
			}
		}

		// Resolve user (skip if not found, as users are environment-specific)
		if transData.AssignUser != nil {
			var user models.User
			err := tx.Where("email = ?", transData.AssignUser.Code).First(&user).Error
			if err == nil {
				transition.AssignUserID = &user.ID
			} else {
				warnings = append(warnings, fmt.Sprintf("User '%s' not found for transition '%s'", transData.AssignUser.Name, transData.Name))
			}
		}

		if err := tx.Create(transition).Error; err != nil {
			tx.Rollback()
			return nil, nil, err
		}

		// Resolve and assign assignment roles
		if len(transData.AssignmentRoles) > 0 {
			assignRoleIDs := []uuid.UUID{}
			for _, roleRef := range transData.AssignmentRoles {
				role, err := s.roleRepo.FindByCode(ctx, roleRef.Code)
				if err != nil {
					warnings = append(warnings, fmt.Sprintf("Assignment role '%s' not found for transition '%s'", roleRef.Name, transData.Name))
					continue
				}
				assignRoleIDs = append(assignRoleIDs, role.ID)
			}
			if len(assignRoleIDs) > 0 {
				if err := tx.Exec("INSERT INTO transition_assignment_roles (workflow_transition_id, role_id) VALUES " +
					buildBulkInsertValues(transition.ID, assignRoleIDs)).Error; err != nil {
					tx.Rollback()
					return nil, nil, err
				}
			}
		}

		// Assign allowed roles
		if len(transData.AllowedRoles) > 0 {
			roleIDs := []uuid.UUID{}
			for _, roleRef := range transData.AllowedRoles {
				role, err := s.roleRepo.FindByCode(ctx, roleRef.Code)
				if err != nil {
					warnings = append(warnings, fmt.Sprintf("Role '%s' not found for transition '%s'", roleRef.Name, transData.Name))
					continue
				}
				roleIDs = append(roleIDs, role.ID)
			}

			if len(roleIDs) > 0 {
				if err := tx.Exec("INSERT INTO transition_allowed_roles (workflow_transition_id, role_id) VALUES " +
					buildBulkInsertValues(transition.ID, roleIDs)).Error; err != nil {
					tx.Rollback()
					return nil, nil, err
				}
			}
		}

		// Create requirements
		for _, reqData := range transData.Requirements {
			requirement := &models.TransitionRequirement{
				ID:              uuid.New(),
				TransitionID:    transition.ID,
				RequirementType: reqData.RequirementType,
				FieldName:       reqData.FieldName,
				FieldValue:      reqData.FieldValue,
				IsMandatory:     reqData.IsMandatory,
				ErrorMessage:    reqData.ErrorMessage,
			}

			if err := tx.Create(requirement).Error; err != nil {
				tx.Rollback()
				return nil, nil, err
			}
		}

		// Create actions
		for _, actionData := range transData.Actions {
			action := &models.TransitionAction{
				ID:             uuid.New(),
				TransitionID:   transition.ID,
				ActionType:     actionData.ActionType,
				Name:           actionData.Name,
				Description:    actionData.Description,
				Config:         actionData.Config,
				ExecutionOrder: actionData.ExecutionOrder,
				IsAsync:        actionData.IsAsync,
				IsActive:       actionData.IsActive,
			}

			if err := tx.Create(action).Error; err != nil {
				tx.Rollback()
				return nil, nil, err
			}
		}
	}

	// Commit transaction
	if err := tx.Commit().Error; err != nil {
		return nil, nil, err
	}

	// Fetch the created workflow with all relations
	createdWorkflow, err := s.repo.FindByIDWithRelations(ctx, workflow.ID)
	if err != nil {
		return nil, nil, err
	}

	resp := models.ToWorkflowResponse(createdWorkflow)
	return &resp, warnings, nil
}

// Helper function to build bulk insert SQL values
func buildBulkInsertValues(workflowID uuid.UUID, ids []uuid.UUID) string {
	values := ""
	for i, id := range ids {
		if i > 0 {
			values += ", "
		}
		values += fmt.Sprintf("('%s', '%s')", workflowID.String(), id.String())
	}
	return values
}

// Helper function to check if a set of strings overlaps with an array
func hasOverlap(set map[string]bool, arr []string) bool {
	for _, item := range arr {
		if set[item] {
			return true
		}
	}
	return false
}

// Helper function to check if a set of ints overlaps with an array
func hasOverlapInt(set map[int]bool, arr []int) bool {
	for _, item := range arr {
		if set[item] {
			return true
		}
	}
	return false
}
