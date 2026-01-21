package handlers

import (
	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/services"
	"github.com/automax/backend/pkg/utils"
	"github.com/go-playground/validator/v10"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type WorkflowHandler struct {
	service   services.WorkflowService
	validator *validator.Validate
}

func NewWorkflowHandler(service services.WorkflowService) *WorkflowHandler {
	return &WorkflowHandler{
		service:   service,
		validator: validator.New(),
	}
}

// Workflow CRUD

func (h *WorkflowHandler) CreateWorkflow(c *fiber.Ctx) error {
	var req models.WorkflowCreateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	if err := h.validator.Struct(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}

	// Get user ID from context
	userID := c.Locals("user_id").(uuid.UUID)

	workflow, err := h.service.CreateWorkflow(c.Context(), &req, userID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusCreated, "Workflow created", workflow)
}

func (h *WorkflowHandler) GetWorkflow(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid ID")
	}

	workflow, err := h.service.GetWorkflow(c.Context(), id)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "Workflow not found")
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Workflow retrieved", workflow)
}

func (h *WorkflowHandler) ListWorkflows(c *fiber.Ctx) error {
	activeOnly := c.Query("active_only") == "true"
	recordType := c.Query("record_type")

	var workflows []models.WorkflowResponse
	var err error

	if recordType != "" {
		workflows, err = h.service.ListWorkflowsByRecordType(c.Context(), recordType, activeOnly)
	} else {
		workflows, err = h.service.ListWorkflows(c.Context(), activeOnly)
	}
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Workflows retrieved", workflows)
}

func (h *WorkflowHandler) UpdateWorkflow(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid ID")
	}

	var req models.WorkflowUpdateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	workflow, err := h.service.UpdateWorkflow(c.Context(), id, &req)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Workflow updated", workflow)
}

func (h *WorkflowHandler) DeleteWorkflow(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid ID")
	}

	if err := h.service.DeleteWorkflow(c.Context(), id); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Workflow deleted", nil)
}

func (h *WorkflowHandler) DuplicateWorkflow(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid ID")
	}

	userID := c.Locals("user_id").(uuid.UUID)

	workflow, err := h.service.DuplicateWorkflow(c.Context(), id, userID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusCreated, "Workflow duplicated", workflow)
}

// Classification assignment

func (h *WorkflowHandler) AssignClassifications(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid ID")
	}

	var req struct {
		ClassificationIDs []string `json:"classification_ids"`
	}
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	classIDs := make([]uuid.UUID, 0, len(req.ClassificationIDs))
	for _, idStr := range req.ClassificationIDs {
		classID, err := uuid.Parse(idStr)
		if err != nil {
			continue
		}
		classIDs = append(classIDs, classID)
	}

	if err := h.service.AssignClassifications(c.Context(), id, classIDs); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	// Fetch updated workflow
	workflow, err := h.service.GetWorkflow(c.Context(), id)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Classifications assigned", workflow)
}

func (h *WorkflowHandler) GetWorkflowByClassification(c *fiber.Ctx) error {
	idStr := c.Params("classification_id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid classification ID")
	}

	workflow, err := h.service.GetWorkflowByClassification(c.Context(), id)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Workflow retrieved", workflow)
}

// State management

func (h *WorkflowHandler) CreateState(c *fiber.Ctx) error {
	workflowIDStr := c.Params("id")
	workflowID, err := uuid.Parse(workflowIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid workflow ID")
	}

	var req models.WorkflowStateCreateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	if err := h.validator.Struct(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}

	state, err := h.service.CreateState(c.Context(), workflowID, &req)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusCreated, "State created", state)
}

func (h *WorkflowHandler) ListStates(c *fiber.Ctx) error {
	workflowIDStr := c.Params("id")
	workflowID, err := uuid.Parse(workflowIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid workflow ID")
	}

	states, err := h.service.ListStates(c.Context(), workflowID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "States retrieved", states)
}

func (h *WorkflowHandler) UpdateState(c *fiber.Ctx) error {
	stateIDStr := c.Params("state_id")
	stateID, err := uuid.Parse(stateIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid state ID")
	}

	var req models.WorkflowStateUpdateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	state, err := h.service.UpdateState(c.Context(), stateID, &req)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "State updated", state)
}

func (h *WorkflowHandler) DeleteState(c *fiber.Ctx) error {
	stateIDStr := c.Params("state_id")
	stateID, err := uuid.Parse(stateIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid state ID")
	}

	if err := h.service.DeleteState(c.Context(), stateID); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "State deleted", nil)
}

// Transition management

func (h *WorkflowHandler) CreateTransition(c *fiber.Ctx) error {
	workflowIDStr := c.Params("id")
	workflowID, err := uuid.Parse(workflowIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid workflow ID")
	}

	var req models.WorkflowTransitionCreateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	if err := h.validator.Struct(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}

	transition, err := h.service.CreateTransition(c.Context(), workflowID, &req)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusCreated, "Transition created", transition)
}

func (h *WorkflowHandler) ListTransitions(c *fiber.Ctx) error {
	workflowIDStr := c.Params("id")
	workflowID, err := uuid.Parse(workflowIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid workflow ID")
	}

	transitions, err := h.service.ListTransitions(c.Context(), workflowID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Transitions retrieved", transitions)
}

func (h *WorkflowHandler) UpdateTransition(c *fiber.Ctx) error {
	transitionIDStr := c.Params("transition_id")
	transitionID, err := uuid.Parse(transitionIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid transition ID")
	}

	var req models.WorkflowTransitionUpdateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	transition, err := h.service.UpdateTransition(c.Context(), transitionID, &req)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Transition updated", transition)
}

func (h *WorkflowHandler) DeleteTransition(c *fiber.Ctx) error {
	transitionIDStr := c.Params("transition_id")
	transitionID, err := uuid.Parse(transitionIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid transition ID")
	}

	if err := h.service.DeleteTransition(c.Context(), transitionID); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Transition deleted", nil)
}

// Transition configuration

func (h *WorkflowHandler) SetTransitionRoles(c *fiber.Ctx) error {
	transitionIDStr := c.Params("id")
	transitionID, err := uuid.Parse(transitionIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid transition ID")
	}

	var req struct {
		RoleIDs []string `json:"role_ids"`
	}
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	roleIDs := make([]uuid.UUID, 0, len(req.RoleIDs))
	for _, idStr := range req.RoleIDs {
		roleID, err := uuid.Parse(idStr)
		if err != nil {
			continue
		}
		roleIDs = append(roleIDs, roleID)
	}

	if err := h.service.SetTransitionRoles(c.Context(), transitionID, roleIDs); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Transition roles updated", nil)
}

func (h *WorkflowHandler) SetTransitionRequirements(c *fiber.Ctx) error {
	transitionIDStr := c.Params("id")
	transitionID, err := uuid.Parse(transitionIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid transition ID")
	}

	var req struct {
		Requirements []models.TransitionRequirementRequest `json:"requirements"`
	}
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	if err := h.service.SetTransitionRequirements(c.Context(), transitionID, req.Requirements); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Transition requirements updated", nil)
}

func (h *WorkflowHandler) SetTransitionActions(c *fiber.Ctx) error {
	transitionIDStr := c.Params("id")
	transitionID, err := uuid.Parse(transitionIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid transition ID")
	}

	var req struct {
		Actions []models.TransitionActionRequest `json:"actions"`
	}
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	if err := h.service.SetTransitionActions(c.Context(), transitionID, req.Actions); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Transition actions updated", nil)
}

// Helper endpoints

func (h *WorkflowHandler) GetTransitionsFromState(c *fiber.Ctx) error {
	stateIDStr := c.Params("state_id")
	stateID, err := uuid.Parse(stateIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid state ID")
	}

	transitions, err := h.service.GetTransitionsFromState(c.Context(), stateID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Transitions retrieved", transitions)
}

func (h *WorkflowHandler) GetInitialState(c *fiber.Ctx) error {
	workflowIDStr := c.Params("id")
	workflowID, err := uuid.Parse(workflowIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid workflow ID")
	}

	state, err := h.service.GetInitialState(c.Context(), workflowID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "Initial state not found")
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Initial state retrieved", state)
}

// MatchWorkflow finds a workflow based on incident criteria and returns form configuration
// This endpoint is designed for mobile apps and other clients to get:
// 1. The matched workflow based on classification, location, source, etc.
// 2. The required fields for incident creation
// 3. All form fields with their labels and descriptions
func (h *WorkflowHandler) MatchWorkflow(c *fiber.Ctx) error {
	var req models.WorkflowMatchRequest
	if err := c.BodyParser(&req); err != nil {
		// If no body provided, use empty request (returns default workflow)
		req = models.WorkflowMatchRequest{}
	}

	result, err := h.service.MatchWorkflow(c.Context(), &req)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Workflow matched", result)
}
