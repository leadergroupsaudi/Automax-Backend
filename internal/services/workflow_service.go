package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/google/uuid"
)

type WorkflowService interface {
	// Workflow CRUD
	CreateWorkflow(ctx context.Context, req *models.WorkflowCreateRequest, createdByID uuid.UUID) (*models.WorkflowResponse, error)
	GetWorkflow(ctx context.Context, id uuid.UUID) (*models.WorkflowResponse, error)
	ListWorkflows(ctx context.Context, activeOnly bool) ([]models.WorkflowResponse, error)
	ListWorkflowsByRecordType(ctx context.Context, recordType string, activeOnly bool) ([]models.WorkflowResponse, error)
	UpdateWorkflow(ctx context.Context, id uuid.UUID, req *models.WorkflowUpdateRequest) (*models.WorkflowResponse, error)
	DeleteWorkflow(ctx context.Context, id uuid.UUID) error
	DuplicateWorkflow(ctx context.Context, id uuid.UUID, createdByID uuid.UUID) (*models.WorkflowResponse, error)

	// Classification assignment
	AssignClassifications(ctx context.Context, workflowID uuid.UUID, classificationIDs []uuid.UUID) error
	GetWorkflowByClassification(ctx context.Context, classificationID uuid.UUID) (*models.WorkflowResponse, error)

	// State management
	CreateState(ctx context.Context, workflowID uuid.UUID, req *models.WorkflowStateCreateRequest) (*models.WorkflowStateResponse, error)
	ListStates(ctx context.Context, workflowID uuid.UUID) ([]models.WorkflowStateResponse, error)
	UpdateState(ctx context.Context, stateID uuid.UUID, req *models.WorkflowStateUpdateRequest) (*models.WorkflowStateResponse, error)
	DeleteState(ctx context.Context, stateID uuid.UUID) error

	// Transition management
	CreateTransition(ctx context.Context, workflowID uuid.UUID, req *models.WorkflowTransitionCreateRequest) (*models.WorkflowTransitionResponse, error)
	ListTransitions(ctx context.Context, workflowID uuid.UUID) ([]models.WorkflowTransitionResponse, error)
	UpdateTransition(ctx context.Context, transitionID uuid.UUID, req *models.WorkflowTransitionUpdateRequest) (*models.WorkflowTransitionResponse, error)
	DeleteTransition(ctx context.Context, transitionID uuid.UUID) error

	// Transition configuration
	SetTransitionRoles(ctx context.Context, transitionID uuid.UUID, roleIDs []uuid.UUID) error
	SetTransitionRequirements(ctx context.Context, transitionID uuid.UUID, requirements []models.TransitionRequirementRequest) error
	SetTransitionActions(ctx context.Context, transitionID uuid.UUID, actions []models.TransitionActionRequest) error

	// Get transitions from a state (for incident transition UI)
	GetTransitionsFromState(ctx context.Context, stateID uuid.UUID) ([]models.WorkflowTransitionResponse, error)
	GetInitialState(ctx context.Context, workflowID uuid.UUID) (*models.WorkflowStateResponse, error)

	// Workflow matching - for mobile apps and other clients
	MatchWorkflow(ctx context.Context, req *models.WorkflowMatchRequest) (*models.WorkflowMatchResponse, error)
}

type workflowService struct {
	repo repository.WorkflowRepository
}

func NewWorkflowService(repo repository.WorkflowRepository) WorkflowService {
	return &workflowService{repo: repo}
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

	recordType := "incident"
	if req.RecordType != "" {
		recordType = req.RecordType
	}

	workflow := &models.Workflow{
		Name:           req.Name,
		Code:           req.Code,
		Description:    req.Description,
		RecordType:     recordType,
		RequiredFields: requiredFieldsJSON,
		CreatedByID:    &createdByID,
		IsActive:       true,
		Version:        1,
	}

	if err := s.repo.Create(ctx, workflow); err != nil {
		return nil, err
	}

	// Assign classifications if provided
	if len(req.ClassificationIDs) > 0 {
		classIDs := make([]uuid.UUID, len(req.ClassificationIDs))
		for i, idStr := range req.ClassificationIDs {
			id, err := uuid.Parse(idStr)
			if err != nil {
				continue
			}
			classIDs[i] = id
		}
		if err := s.repo.AssignClassifications(ctx, workflow.ID, classIDs); err != nil {
			// Log error but don't fail the workflow creation
		}
	}

	// Fetch with relations
	created, err := s.repo.FindByIDWithRelations(ctx, workflow.ID)
	if err != nil {
		return nil, err
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
	workflow, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if req.Name != "" {
		workflow.Name = req.Name
	}
	if req.Code != "" {
		workflow.Code = req.Code
	}
	if req.Description != "" {
		workflow.Description = req.Description
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

	if err := s.repo.Update(ctx, workflow); err != nil {
		return nil, err
	}

	// Update classifications if provided
	if req.ClassificationIDs != nil {
		classIDs := make([]uuid.UUID, 0, len(req.ClassificationIDs))
		for _, idStr := range req.ClassificationIDs {
			id, err := uuid.Parse(idStr)
			if err != nil {
				continue
			}
			classIDs = append(classIDs, id)
		}
		if err := s.repo.AssignClassifications(ctx, workflow.ID, classIDs); err != nil {
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

func (s *workflowService) DeleteWorkflow(ctx context.Context, id uuid.UUID) error {
	return s.repo.Delete(ctx, id)
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
			WorkflowID:  newWorkflow.ID,
			Name:        state.Name,
			Code:        state.Code,
			Description: state.Description,
			StateType:   state.StateType,
			Color:       state.Color,
			PositionX:   state.PositionX,
			PositionY:   state.PositionY,
			SLAHours:    state.SLAHours,
			SortOrder:   state.SortOrder,
			IsActive:    state.IsActive,
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
			WorkflowID:  newWorkflow.ID,
			Name:        trans.Name,
			Code:        trans.Code,
			Description: trans.Description,
			FromStateID: newFromStateID,
			ToStateID:   newToStateID,
			SortOrder:   trans.SortOrder,
			IsActive:    trans.IsActive,
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
	state := &models.WorkflowState{
		WorkflowID:  workflowID,
		Name:        req.Name,
		Code:        req.Code,
		Description: req.Description,
		StateType:   req.StateType,
		Color:       req.Color,
		PositionX:   req.PositionX,
		PositionY:   req.PositionY,
		SLAHours:    req.SLAHours,
		SortOrder:   req.SortOrder,
		IsActive:    true,
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

	// Assign viewable roles if provided
	if len(req.ViewableRoleIDs) > 0 {
		roleIDs := make([]uuid.UUID, 0, len(req.ViewableRoleIDs))
		for _, idStr := range req.ViewableRoleIDs {
			id, err := uuid.Parse(idStr)
			if err != nil {
				continue
			}
			roleIDs = append(roleIDs, id)
		}
		if err := s.repo.AssignStateViewableRoles(ctx, state.ID, roleIDs); err != nil {
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

func (s *workflowService) UpdateState(ctx context.Context, stateID uuid.UUID, req *models.WorkflowStateUpdateRequest) (*models.WorkflowStateResponse, error) {
	state, err := s.repo.FindStateByID(ctx, stateID)
	if err != nil {
		return nil, err
	}

	if req.Name != "" {
		state.Name = req.Name
	}
	if req.Code != "" {
		state.Code = req.Code
	}
	if req.Description != "" {
		state.Description = req.Description
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
	if req.SortOrder != nil {
		state.SortOrder = *req.SortOrder
	}
	if req.IsActive != nil {
		state.IsActive = *req.IsActive
	}

	if err := s.repo.UpdateState(ctx, state); err != nil {
		return nil, err
	}

	// Update viewable roles if provided
	if req.ViewableRoleIDs != nil {
		roleIDs := make([]uuid.UUID, 0, len(req.ViewableRoleIDs))
		for _, idStr := range req.ViewableRoleIDs {
			id, err := uuid.Parse(idStr)
			if err != nil {
				continue
			}
			roleIDs = append(roleIDs, id)
		}
		if err := s.repo.AssignStateViewableRoles(ctx, stateID, roleIDs); err != nil {
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
		Code:                 req.Code,
		Description:          req.Description,
		FromStateID:          fromStateID,
		ToStateID:            toStateID,
		SortOrder:            req.SortOrder,
		IsActive:             true,
		AutoDetectDepartment: req.AutoDetectDepartment,
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

	if req.AssignmentRoleID != nil && *req.AssignmentRoleID != "" {
		roleID, err := uuid.Parse(*req.AssignmentRoleID)
		if err == nil {
			transition.AssignmentRoleID = &roleID
		}
	}

	if err := s.repo.CreateTransition(ctx, transition); err != nil {
		return nil, err
	}

	// Assign roles if provided
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

func (s *workflowService) UpdateTransition(ctx context.Context, transitionID uuid.UUID, req *models.WorkflowTransitionUpdateRequest) (*models.WorkflowTransitionResponse, error) {
	transition, err := s.repo.FindTransitionByID(ctx, transitionID)
	if err != nil {
		return nil, err
	}

	if req.Name != "" {
		transition.Name = req.Name
	}
	if req.Code != "" {
		transition.Code = req.Code
	}
	if req.Description != "" {
		transition.Description = req.Description
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

	// Department Assignment
	if req.AutoDetectDepartment != nil {
		transition.AutoDetectDepartment = *req.AutoDetectDepartment
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
	if req.AssignmentRoleID != nil {
		if *req.AssignmentRoleID == "" {
			transition.AssignmentRoleID = nil
		} else {
			roleID, err := uuid.Parse(*req.AssignmentRoleID)
			if err == nil {
				transition.AssignmentRoleID = &roleID
			}
		}
	}

	if err := s.repo.UpdateTransition(ctx, transition); err != nil {
		return nil, err
	}

	// Update roles if provided
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

func (s *workflowService) GetInitialState(ctx context.Context, workflowID uuid.UUID) (*models.WorkflowStateResponse, error) {
	state, err := s.repo.GetInitialState(ctx, workflowID)
	if err != nil {
		return nil, err
	}

	resp := models.ToWorkflowStateResponse(state)
	return &resp, nil
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
		{Field: "severity", Label: "Severity", Description: "Impact level", IsRequired: false},
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

	// Parse classification ID if provided
	var classificationID uuid.UUID
	if req.ClassificationID != "" {
		classificationID, _ = uuid.Parse(req.ClassificationID)
	}

	// Find matching workflow
	var matchedWorkflow *models.Workflow
	var highestScore int

	for i := range workflows {
		w := &workflows[i]
		if !w.IsActive {
			continue
		}

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

		// Check if it's the default workflow
		if w.IsDefault {
			score += 1
		}

		// If this workflow has a higher score, use it
		if score > highestScore || (score == highestScore && matchedWorkflow == nil) {
			highestScore = score
			matchedWorkflow = w
		}
	}

	// If no workflow matched by criteria, use the default workflow
	if matchedWorkflow == nil {
		for i := range workflows {
			if workflows[i].IsDefault {
				matchedWorkflow = &workflows[i]
				break
			}
		}
	}

	// If still no workflow, use the first active one
	if matchedWorkflow == nil && len(workflows) > 0 {
		matchedWorkflow = &workflows[0]
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

	// Update form fields with required status
	formFields := make([]models.IncidentFormFieldConfig, len(allFormFields))
	for i, f := range allFormFields {
		formFields[i] = f
		for _, rf := range requiredFields {
			if rf == f.Field {
				formFields[i].IsRequired = true
				break
			}
		}
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
	response := &models.WorkflowMatchResponse{
		Matched:        true,
		WorkflowID:     &workflowIDStr,
		WorkflowName:   &fullWorkflow.Name,
		WorkflowCode:   &fullWorkflow.Code,
		RequiredFields: requiredFields,
		FormFields:     formFields,
		InitialStateID: initialStateID,
		InitialState:   initialStateName,
	}

	return response, nil
}
