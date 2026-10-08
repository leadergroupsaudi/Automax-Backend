package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/automax/backend/internal/config"
	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/services"
	"github.com/automax/backend/pkg/constants"
	"github.com/automax/backend/pkg/i18n"
	"github.com/automax/backend/pkg/utils"
	"github.com/automax/backend/pkg/validation"
	"github.com/go-playground/validator/v10"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type WorkflowHandler struct {
	service          services.WorkflowService
	actionLogService services.ActionLogService
	validator        *validator.Validate
	cfg              *config.Config
}

func NewWorkflowHandler(service services.WorkflowService, actionLogService services.ActionLogService, cfg *config.Config) *WorkflowHandler {
	return &WorkflowHandler{
		service:          service,
		actionLogService: actionLogService,
		validator:        validator.New(),
		cfg:              cfg,
	}
}

// Workflow CRUD

func (h *WorkflowHandler) CreateWorkflow(c *fiber.Ctx) error {
	var req models.WorkflowCreateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}

	validationErrors := validation.ValidateStruct(c.UserContext(), &req)

	// EPM940 auto-generates the Workflow Code from Name (system-generated,
	// non-editable, never duplicated) — other clients (e.g. VD2) keep the
	// original behavior of requiring the client to supply a code. Code can't
	// be a static "required" struct tag since that rule is client-specific,
	// so fold it into the same validationErrors map (same key/format as
	// every other field) instead of a bespoke response shape.
	clientCode := strings.TrimSpace(h.cfg.ClientCode)
	isEPM940 := strings.EqualFold(clientCode, constants.CLIENT_CODE.EPM940)

	if !isEPM940 && req.Code == "" {
		if validationErrors == nil {
			validationErrors = map[string]string{}
		}
		validationErrors["Code"] = i18n.T(c.UserContext(), "workflow_code_required")
	}

	if len(validationErrors) != 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"errors":  validationErrors,
		})
	}

	if isEPM940 {
		if err := h.service.WorkflowExistsByName(c.UserContext(), req.Name); err != nil {
			return utils.ErrorResponse(c, fiber.StatusConflict, i18n.T(c.UserContext(), "workflow_name_exists"))
		}
	} else {
		if err := h.service.WorkflowExistsByCodeOrName(c.UserContext(), []string{req.Code, req.Name}); err != nil {
			return utils.ErrorResponse(c, fiber.StatusConflict, i18n.T(c.UserContext(), "workflow_code_name_exists"))
		}
	}

	// Get user ID from context
	userID := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)

	workflow, err := h.service.CreateWorkflow(c.UserContext(), &req, userID)
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	// Log the creation with workflow details
	ipAddress, _ := c.Locals(constants.ContextKeys.IP_ADDRESS).(string)
	userAgent, _ := c.Locals(constants.ContextKeys.USER_AGENT).(string)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		newValue := map[string]interface{}{
			"name":        workflow.Name,
			"code":        workflow.Code,
			"description": workflow.Description,
			"record_type": workflow.RecordType,
			"is_active":   workflow.IsActive,
			"is_default":  workflow.IsDefault,
		}

		_ = h.actionLogService.LogAction(ctx, &services.LogActionParams{
			UserID:      userID,
			Action:      "create",
			Module:      "workflows",
			ResourceID:  workflow.ID.String(),
			Description: fmt.Sprintf("Created new workflow '%s'", workflow.Name),
			NewValue:    newValue,
			IPAddress:   ipAddress,
			UserAgent:   userAgent,
			Status:      "success",
		})
	}()

	return utils.SuccessResponse(c, fiber.StatusCreated, i18n.T(c.UserContext(), "workflow_created"), workflow)
}

func (h *WorkflowHandler) GetWorkflow(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_id"))
	}

	workflow, err := h.service.GetWorkflow(c.UserContext(), id)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, i18n.T(c.UserContext(), "workflow_not_found"))
	}

	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "workflow_retrieved"), workflow)
}

// ListWorkflows powers the Workflow List page: search by name and filter by
// status, module/category (record_type), created_by, and created/modified date ranges.
func (h *WorkflowHandler) ListWorkflows(c *fiber.Ctx) error {
	filter := &models.WorkflowFilter{}
	if err := c.QueryParser(filter); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_query_parameters"))
	}

	if validationErrors := validation.ValidateStruct(c.UserContext(), filter); len(validationErrors) != 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"errors":  validationErrors,
		})
	}

	// Workflow Status filter: status=active|inactive. "active_only=true" is kept
	// for backward compatibility with existing callers of this endpoint.
	switch strings.ToLower(c.Query("status")) {
	case "active":
		active := true
		filter.IsActive = &active
	case "inactive":
		inactive := false
		filter.IsActive = &inactive
	default:
		if c.Query("active_only") == "true" {
			active := true
			filter.IsActive = &active
		}
	}

	// parseDate treats a date-only value (no time component) as the start of
	// that day in the server's local timezone (not UTC) — the DB stores
	// timestamptz, but the day boundary a user means by "2026-07-13" is a
	// local-calendar-day boundary, matching the convention in
	// action_log_handler.go. endOfDay pushes a "to" bound to
	// 23:59:59.999999999 local so the entire day is included instead of being
	// excluded by a midnight <= compare.
	parseDate := func(param string, endOfDay bool) (*time.Time, error) {
		v := c.Query(param)
		if v == "" {
			return nil, nil
		}
		if t, err := time.Parse("2006-01-02", v); err == nil {
			if endOfDay {
				local := time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 999999999, time.Local)
				return &local, nil
			}
			local := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.Local)
			return &local, nil
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return &t, nil
		}
		return nil, fmt.Errorf("%s must be YYYY-MM-DD or RFC3339", param)
	}

	var dateErr error
	if filter.CreatedFrom, dateErr = parseDate("created_from", false); dateErr != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, dateErr.Error())
	}
	if filter.CreatedTo, dateErr = parseDate("created_to", true); dateErr != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, dateErr.Error())
	}
	if filter.ModifiedFrom, dateErr = parseDate("modified_from", false); dateErr != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, dateErr.Error())
	}
	if filter.ModifiedTo, dateErr = parseDate("modified_to", true); dateErr != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, dateErr.Error())
	}

	// Pagination is opt-in: existing callers (dropdowns/pickers across the app)
	// hit this same endpoint expecting the full, unpaginated workflow list.
	// Only paginate when the caller explicitly asks for it, so that behavior
	// is unchanged for every other consumer.
	paginate := c.Query("page") != "" || c.Query("limit") != ""
	if paginate {
		if filter.Page < 1 {
			filter.Page = 1
		}
		if filter.Limit < 1 || filter.Limit > 100 {
			filter.Limit = 20
		}
	} else {
		filter.Page = 0
		filter.Limit = 0
	}

	workflows, total, err := h.service.ListWorkflowsFiltered(c.UserContext(), filter)
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	message := i18n.T(c.UserContext(), "workflows_retrieved")
	if total == 0 {
		message = i18n.T(c.UserContext(), "no_workflows_found")
	}

	if !paginate {
		return utils.SuccessResponse(c, fiber.StatusOK, message, workflows)
	}

	totalPages := 0
	if total > 0 {
		totalPages = (int(total) + filter.Limit - 1) / filter.Limit
	}

	return c.JSON(fiber.Map{
		"success":     true,
		"message":     message,
		"data":        workflows,
		"page":        filter.Page,
		"limit":       filter.Limit,
		"total_items": total,
		"total_pages": totalPages,
	})
}

func (h *WorkflowHandler) UpdateWorkflow(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_id"))
	}

	var req models.WorkflowUpdateRequest
	if err := c.BodyParser(&req); err != nil {
		log.Println(err)
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}

	if validationErrors := validation.ValidateStruct(c.UserContext(), &req); len(validationErrors) != 0 {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}

	// Workflow Code is system-generated and immutable for EPM940. Discard any
	// client-supplied value up front so the duplicate-check below and the
	// service layer both see it as "not provided", regardless of what a
	// stale client or direct API call sends.
	if strings.EqualFold(strings.TrimSpace(h.cfg.ClientCode), constants.CLIENT_CODE.EPM940) {
		req.Code = ""
	}

	// Get old workflow for action logging
	oldWorkflow, err := h.service.GetWorkflow(c.UserContext(), id)
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	search := []string{}
	if req.Code != "" && req.Code != oldWorkflow.Code {
		search = append(search, req.Code)
	}
	if req.Name != "" && req.Name != oldWorkflow.Name {

		search = append(search, req.Name)
	}

	if len(search) > 0 {
		if err := h.service.WorkflowExistsByCodeOrName(c.UserContext(), search); err != nil {
			return utils.ErrorResponse(c, fiber.StatusConflict, i18n.T(c.UserContext(), "workflow_code_name_exists"))
		}
	}

	workflow, err := h.service.UpdateWorkflow(c.UserContext(), id, &req)
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	// Capture values before async logging
	userID := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)
	ipAddress, _ := c.Locals(constants.ContextKeys.IP_ADDRESS).(string)
	userAgent, _ := c.Locals(constants.ContextKeys.USER_AGENT).(string)

	// Build comprehensive old and new values
	oldValue := map[string]interface{}{
		"name":                     oldWorkflow.Name,
		"code":                     oldWorkflow.Code,
		"description":              oldWorkflow.Description,
		"record_type":              oldWorkflow.RecordType,
		"is_active":                oldWorkflow.IsActive,
		"is_default":               oldWorkflow.IsDefault,
		"sources":                  oldWorkflow.Sources,
		"priorities":               oldWorkflow.Priorities,
		"canvas_layout":            oldWorkflow.CanvasLayout,
		"required_fields":          oldWorkflow.RequiredFields,
		"classifications":          oldWorkflow.Classifications,
		"locations":                oldWorkflow.Locations,
		"convert_to_request_roles": oldWorkflow.ConvertToRequestRoles,
		"merge_allowed_roles":      oldWorkflow.MergeAllowedRoles,
	}
	newValue := map[string]interface{}{
		"name":                     workflow.Name,
		"code":                     workflow.Code,
		"description":              workflow.Description,
		"record_type":              workflow.RecordType,
		"is_active":                workflow.IsActive,
		"is_default":               workflow.IsDefault,
		"sources":                  workflow.Sources,
		"priorities":               workflow.Priorities,
		"canvas_layout":            workflow.CanvasLayout,
		"required_fields":          workflow.RequiredFields,
		"classifications":          workflow.Classifications,
		"locations":                workflow.Locations,
		"convert_to_request_roles": workflow.ConvertToRequestRoles,
		"merge_allowed_roles":      workflow.MergeAllowedRoles,
	}

	// Build detailed change description
	var changeDetails []string

	// Check each field for changes
	fieldLabels := map[string]string{
		"name": "Name", "code": "Code", "description": "Description",
		"record_type": "Record Type", "is_active": "Is Active", "is_default": "Is Default",
		"sources": "Sources", "priorities": "Priorities", "canvas_layout": "Canvas Layout",
		"required_fields": "Required Fields", "classifications": "Classifications",
		"locations": "Locations", "convert_to_request_roles": "Convert to Request Roles",
		"merge_allowed_roles": "Merge Allowed Roles",
	}

	for key, label := range fieldLabels {
		oldVal := oldValue[key]
		newVal := newValue[key]
		if fmt.Sprintf("%v", oldVal) != fmt.Sprintf("%v", newVal) {
			changeDetails = append(changeDetails, label)
		}
	}

	description := fmt.Sprintf("Updated workflow '%s'", workflow.Name)
	if len(changeDetails) > 0 {
		description += fmt.Sprintf(" - changed: %s", strings.Join(changeDetails, ", "))
	}

	// Log the update with old and new values
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = h.actionLogService.LogAction(ctx, &services.LogActionParams{
			UserID:      userID,
			Action:      "update",
			Module:      "workflows",
			ResourceID:  id.String(),
			Description: description,
			OldValue:    oldValue,
			NewValue:    newValue,
			IPAddress:   ipAddress,
			UserAgent:   userAgent,
			Status:      "success",
		})
	}()

	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "workflow_updated"), workflow)
}

func (h *WorkflowHandler) DeleteWorkflow(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_id"))
	}

	// Get workflow details for action logging
	oldWorkflow, err := h.service.GetWorkflow(c.UserContext(), id)
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	if err := h.service.DeleteWorkflow(c.UserContext(), id); err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	// Log the deletion
	userID := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)
	ipAddress, _ := c.Locals(constants.ContextKeys.IP_ADDRESS).(string)
	userAgent, _ := c.Locals(constants.ContextKeys.USER_AGENT).(string)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		oldValue := map[string]interface{}{
			"name":        oldWorkflow.Name,
			"code":        oldWorkflow.Code,
			"description": oldWorkflow.Description,
		}

		_ = h.actionLogService.LogAction(ctx, &services.LogActionParams{
			UserID:      userID,
			Action:      "delete",
			Module:      "workflows",
			ResourceID:  id.String(),
			Description: fmt.Sprintf("Deleted workflow '%s'", oldWorkflow.Name),
			OldValue:    oldValue,
			IPAddress:   ipAddress,
			UserAgent:   userAgent,
			Status:      "success",
		})
	}()

	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "workflow_deleted"), nil)
}

func (h *WorkflowHandler) ListDeletedWorkflows(c *fiber.Ctx) error {
	workflows, err := h.service.ListDeletedWorkflows(c.UserContext())
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "deleted_workflows_retrieved"), workflows)
}

func (h *WorkflowHandler) PermanentDeleteWorkflow(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_id"))
	}

	if err := h.service.PermanentDeleteWorkflow(c.UserContext(), id); err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "workflow_permanently_deleted"), nil)
}

func (h *WorkflowHandler) RestoreWorkflow(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_id"))
	}

	if err := h.service.RestoreWorkflow(c.UserContext(), id); err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "workflow_restored"), nil)
}

func (h *WorkflowHandler) DuplicateWorkflow(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_id"))
	}

	userID := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)

	workflow, err := h.service.DuplicateWorkflow(c.UserContext(), id, userID)
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	return utils.SuccessResponse(c, fiber.StatusCreated, i18n.T(c.UserContext(), "workflow_duplicated"), workflow)
}

// Classification assignment

func (h *WorkflowHandler) AssignClassifications(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_id"))
	}

	var req struct {
		ClassificationIDs []string `json:"classification_ids"`
	}
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}

	classIDs := make([]uuid.UUID, 0, len(req.ClassificationIDs))
	for _, idStr := range req.ClassificationIDs {
		classID, err := uuid.Parse(idStr)
		if err != nil {
			continue
		}
		classIDs = append(classIDs, classID)
	}

	if err := h.service.AssignClassifications(c.UserContext(), id, classIDs); err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	// Fetch updated workflow
	workflow, err := h.service.GetWorkflow(c.UserContext(), id)
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	// Log the classification assignment
	userID := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)
	ipAddress, _ := c.Locals(constants.ContextKeys.IP_ADDRESS).(string)
	userAgent, _ := c.Locals(constants.ContextKeys.USER_AGENT).(string)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Extract classification IDs from classifications
		var classIDs []string
		for _, c := range workflow.Classifications {
			classIDs = append(classIDs, c.ID.String())
		}

		newValue := map[string]interface{}{
			"classifications": classIDs,
		}

		_ = h.actionLogService.LogAction(ctx, &services.LogActionParams{
			UserID:      userID,
			Action:      "update",
			Module:      "workflows",
			ResourceID:  id.String(),
			Description: fmt.Sprintf("Updated classifications for workflow '%s'", workflow.Name),
			NewValue:    newValue,
			IPAddress:   ipAddress,
			UserAgent:   userAgent,
			Status:      "success",
		})
	}()

	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "classifications_assigned"), workflow)
}

func (h *WorkflowHandler) GetWorkflowByClassification(c *fiber.Ctx) error {
	idStr := c.Params("classification_id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_classification_id2"))
	}

	workflow, err := h.service.GetWorkflowByClassification(c.UserContext(), id)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, i18n.T(c.UserContext(), "workflow_not_found"))
	}

	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "workflow_retrieved"), workflow)
}

// State management

func (h *WorkflowHandler) CreateState(c *fiber.Ctx) error {
	workflowIDStr := c.Params("id")
	workflowID, err := uuid.Parse(workflowIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_workflow_id"))
	}

	var req models.WorkflowStateCreateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}

	// Normalize empty optional UUID strings to nil so "omitempty,uuid" validation passes
	if req.EscalationPolicyID != nil && *req.EscalationPolicyID == "" {
		req.EscalationPolicyID = nil
	}
	if req.AssignUserID != nil && *req.AssignUserID == "" {
		req.AssignUserID = nil
	}

	validationErrors := validation.ValidateStruct(c.UserContext(), &req)

	// EPM940 auto-generates the State Code (STE-######); other clients (e.g. VD2)
	// must supply it. The rule is client-specific, so fold it into validationErrors
	// like CreateWorkflow does rather than a static "required" struct tag.
	if !strings.EqualFold(strings.TrimSpace(h.cfg.ClientCode), constants.CLIENT_CODE.EPM940) && req.Code == "" {
		if validationErrors == nil {
			validationErrors = map[string]string{}
		}
		validationErrors["Code"] = i18n.T(c.UserContext(), "workflow_code_required")
	}

	if len(validationErrors) != 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"errors":  validationErrors,
		})
	}

	state, err := h.service.CreateState(c.UserContext(), workflowID, &req)
	if err != nil {
		var slaErr *services.SLAValidationError
		if errors.As(err, &slaErr) {
			return c.Status(fiber.StatusUnprocessableEntity).JSON(utils.Response{
				Success: false,
				Error:   slaErr.Error(),
				Data:    slaErr,
			})
		}
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	// Log the creation
	userID := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)
	ipAddress, _ := c.Locals(constants.ContextKeys.IP_ADDRESS).(string)
	userAgent, _ := c.Locals(constants.ContextKeys.USER_AGENT).(string)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Extract viewable and editable role IDs
		var viewableRoleIDs []string
		for _, r := range state.ViewableRoles {
			viewableRoleIDs = append(viewableRoleIDs, r.ID.String())
		}
		var editableRoleIDs []string
		for _, r := range state.EditableRoles {
			editableRoleIDs = append(editableRoleIDs, r.ID.String())
		}

		newValue := map[string]interface{}{
			"name":           state.Name,
			"code":           state.Code,
			"description":    state.Description,
			"state_type":     state.StateType,
			"color":          state.Color,
			"sla_hours":      state.SLAHours,
			"sla_unit":       state.SLAUnit,
			"sort_order":     state.SortOrder,
			"position_x":     state.PositionX,
			"position_y":     state.PositionY,
			"is_active":      state.IsActive,
			"viewable_roles": viewableRoleIDs,
			"editable_roles": editableRoleIDs,
		}

		_ = h.actionLogService.LogAction(ctx, &services.LogActionParams{
			UserID:      userID,
			Action:      "create",
			Module:      "workflow_states",
			ResourceID:  state.ID.String(),
			Description: fmt.Sprintf("Created workflow state '%s' in workflow", state.Name),
			NewValue:    newValue,
			IPAddress:   ipAddress,
			UserAgent:   userAgent,
			Status:      "success",
		})
	}()

	return utils.SuccessResponse(c, fiber.StatusCreated, i18n.T(c.UserContext(), "state_created"), state)
}

func (h *WorkflowHandler) ListStates(c *fiber.Ctx) error {
	workflowIDStr := c.Params("id")
	workflowID, err := uuid.Parse(workflowIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_workflow_id"))
	}

	states, err := h.service.ListStates(c.UserContext(), workflowID)
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "states_retrieved"), states)
}

func (h *WorkflowHandler) UpdateState(c *fiber.Ctx) error {
	stateIDStr := c.Params("state_id")
	stateID, err := uuid.Parse(stateIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_state_id2"))
	}

	var req models.WorkflowStateUpdateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}

	// State Code is system-generated and immutable for EPM940. Discard any
	// client-supplied value so the service never overwrites the generated code.
	if strings.EqualFold(strings.TrimSpace(h.cfg.ClientCode), constants.CLIENT_CODE.EPM940) {
		req.Code = ""
	}

	// Get old state for action logging
	oldState, err := h.service.GetState(c.UserContext(), stateID)
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	state, err := h.service.UpdateState(c.UserContext(), stateID, &req)
	if err != nil {
		var slaErr *services.SLAValidationError
		if errors.As(err, &slaErr) {
			return c.Status(fiber.StatusUnprocessableEntity).JSON(utils.Response{
				Success: false,
				Error:   slaErr.Error(),
				Data:    slaErr,
			})
		}
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	// Capture values before async logging
	userID := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)
	ipAddress, _ := c.Locals(constants.ContextKeys.IP_ADDRESS).(string)
	userAgent, _ := c.Locals(constants.ContextKeys.USER_AGENT).(string)

	// Log the update with old and new values
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Extract viewable and editable role IDs
		var oldViewableRoleIDs, newViewableRoleIDs []string
		for _, r := range oldState.ViewableRoles {
			oldViewableRoleIDs = append(oldViewableRoleIDs, r.ID.String())
		}
		for _, r := range state.ViewableRoles {
			newViewableRoleIDs = append(newViewableRoleIDs, r.ID.String())
		}
		var oldEditableRoleIDs, newEditableRoleIDs []string
		for _, r := range oldState.EditableRoles {
			oldEditableRoleIDs = append(oldEditableRoleIDs, r.ID.String())
		}
		for _, r := range state.EditableRoles {
			newEditableRoleIDs = append(newEditableRoleIDs, r.ID.String())
		}

		oldValue := map[string]interface{}{
			"name":           oldState.Name,
			"code":           oldState.Code,
			"description":    oldState.Description,
			"state_type":     oldState.StateType,
			"color":          oldState.Color,
			"sla_hours":      oldState.SLAHours,
			"sla_unit":       oldState.SLAUnit,
			"sort_order":     oldState.SortOrder,
			"is_active":      oldState.IsActive,
			"position_x":     oldState.PositionX,
			"position_y":     oldState.PositionY,
			"viewable_roles": oldViewableRoleIDs,
			"editable_roles": oldEditableRoleIDs,
		}
		newValue := map[string]interface{}{
			"name":           state.Name,
			"code":           state.Code,
			"description":    state.Description,
			"state_type":     state.StateType,
			"color":          state.Color,
			"sla_hours":      state.SLAHours,
			"sla_unit":       state.SLAUnit,
			"sort_order":     state.SortOrder,
			"is_active":      state.IsActive,
			"position_x":     state.PositionX,
			"position_y":     state.PositionY,
			"viewable_roles": newViewableRoleIDs,
			"editable_roles": newEditableRoleIDs,
		}

		// Build detailed change description
		var changeDetails []string
		fieldLabels := map[string]string{
			"name": "Name", "code": "Code", "description": "Description",
			"state_type": "State Type", "color": "Color", "sla_hours": "SLA Hours", "sla_unit": "SLA Unit",
			"sort_order": "Sort Order", "is_active": "Is Active",
			"position_x": "Position X", "position_y": "Position Y",
			"viewable_roles": "Viewable Roles", "editable_roles": "Editable Roles",
		}

		for key, label := range fieldLabels {
			if fmt.Sprintf("%v", oldValue[key]) != fmt.Sprintf("%v", newValue[key]) {
				changeDetails = append(changeDetails, label)
			}
		}

		desc := fmt.Sprintf("Updated workflow state '%s'", state.Name)
		if len(changeDetails) > 0 {
			desc += fmt.Sprintf(" - changed: %s", strings.Join(changeDetails, ", "))
		}

		_ = h.actionLogService.LogAction(ctx, &services.LogActionParams{
			UserID:      userID,
			Action:      "update",
			Module:      "workflow_states",
			ResourceID:  stateID.String(),
			Description: desc,
			OldValue:    oldValue,
			NewValue:    newValue,
			IPAddress:   ipAddress,
			UserAgent:   userAgent,
			Status:      "success",
		})
	}()

	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "state_updated"), state)
}

func (h *WorkflowHandler) DeleteState(c *fiber.Ctx) error {
	stateIDStr := c.Params("state_id")
	stateID, err := uuid.Parse(stateIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_state_id2"))
	}

	// Get state details for action logging
	oldState, err := h.service.GetState(c.UserContext(), stateID)
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	if err := h.service.DeleteState(c.UserContext(), stateID); err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	// Log the deletion
	userID := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)
	ipAddress, _ := c.Locals(constants.ContextKeys.IP_ADDRESS).(string)
	userAgent, _ := c.Locals(constants.ContextKeys.USER_AGENT).(string)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Extract viewable and editable role IDs
		var viewableRoleIDs []string
		for _, r := range oldState.ViewableRoles {
			viewableRoleIDs = append(viewableRoleIDs, r.ID.String())
		}
		var editableRoleIDsDel []string
		for _, r := range oldState.EditableRoles {
			editableRoleIDsDel = append(editableRoleIDsDel, r.ID.String())
		}

		oldValue := map[string]interface{}{
			"name":           oldState.Name,
			"code":           oldState.Code,
			"description":    oldState.Description,
			"state_type":     oldState.StateType,
			"color":          oldState.Color,
			"sla_hours":      oldState.SLAHours,
			"sla_unit":       oldState.SLAUnit,
			"sort_order":     oldState.SortOrder,
			"position_x":     oldState.PositionX,
			"position_y":     oldState.PositionY,
			"is_active":      oldState.IsActive,
			"viewable_roles": viewableRoleIDs,
			"editable_roles": editableRoleIDsDel,
		}

		_ = h.actionLogService.LogAction(ctx, &services.LogActionParams{
			UserID:      userID,
			Action:      "delete",
			Module:      "workflow_states",
			ResourceID:  stateID.String(),
			Description: fmt.Sprintf("Deleted workflow state '%s'", oldState.Name),
			OldValue:    oldValue,
			IPAddress:   ipAddress,
			UserAgent:   userAgent,
			Status:      "success",
		})
	}()

	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "state_deleted"), nil)
}

// Transition management

func transitionRequestError(err error) (string, bool) {
	switch {
	case errors.Is(err, services.ErrTransitionPushAndReturn),
		errors.Is(err, services.ErrInvalidTargetWorkflow),
		errors.Is(err, services.ErrTargetWorkflowNotFound):
		return err.Error(), true
	default:
		return "", false
	}
}

func (h *WorkflowHandler) CreateTransition(c *fiber.Ctx) error {
	workflowIDStr := c.Params("id")
	workflowID, err := uuid.Parse(workflowIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_workflow_id"))
	}

	var req models.WorkflowTransitionCreateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}

	validationErrors := validation.ValidateStruct(c.UserContext(), &req)

	// EPM940 auto-generates the Transition Code (TRN-######); other clients (e.g. VD2)
	// must supply it. The rule is client-specific, so fold it into validationErrors
	// like CreateWorkflow does rather than a static "required" struct tag.
	if !strings.EqualFold(strings.TrimSpace(h.cfg.ClientCode), constants.CLIENT_CODE.EPM940) && req.Code == "" {
		if validationErrors == nil {
			validationErrors = map[string]string{}
		}
		validationErrors["Code"] = i18n.T(c.UserContext(), "workflow_code_required")
	}

	if len(validationErrors) != 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"errors":  validationErrors,
		})
	}

	transition, err := h.service.CreateTransition(c.UserContext(), workflowID, &req)
	if err != nil {
		if msg, ok := transitionRequestError(err); ok {
			return utils.ErrorResponse(c, fiber.StatusBadRequest, msg)
		}
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	// Log the creation
	userID := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)
	ipAddress, _ := c.Locals(constants.ContextKeys.IP_ADDRESS).(string)
	userAgent, _ := c.Locals(constants.ContextKeys.USER_AGENT).(string)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Extract allowed role IDs
		var allowedRoleIDs []string
		for _, r := range transition.AllowedRoles {
			allowedRoleIDs = append(allowedRoleIDs, r.ID.String())
		}
		var assignmentRoleIDs []string
		for _, r := range transition.AssignmentRoles {
			assignmentRoleIDs = append(assignmentRoleIDs, r.ID.String())
		}

		newValue := map[string]interface{}{
			"name":                   transition.Name,
			"code":                   transition.Code,
			"description":            transition.Description,
			"from_state_id":          transition.FromStateID.String(),
			"to_state_id":            transition.ToStateID.String(),
			"sort_order":             transition.SortOrder,
			"is_active":              transition.IsActive,
			"allowed_roles":          allowedRoleIDs,
			"assign_department_id":   transition.AssignDepartmentID,
			"auto_detect_department": transition.AutoDetectDepartment,
			"assign_user_id":         transition.AssignUserID,
			"assignment_role_ids":    assignmentRoleIDs,
			"auto_match_user":        transition.AutoMatchUser,
			"manual_select_user":     transition.ManualSelectUser,
			"view_assigne_user_list": transition.ViewAssigneUserList,
		}

		_ = h.actionLogService.LogAction(ctx, &services.LogActionParams{
			UserID:      userID,
			Action:      "create",
			Module:      "workflow_transitions",
			ResourceID:  transition.ID.String(),
			Description: fmt.Sprintf("Created workflow transition '%s' (from state to state)", transition.Name),
			NewValue:    newValue,
			IPAddress:   ipAddress,
			UserAgent:   userAgent,
			Status:      "success",
		})
	}()

	return utils.SuccessResponse(c, fiber.StatusCreated, i18n.T(c.UserContext(), "transition_created"), transition)
}

func (h *WorkflowHandler) ListTransitions(c *fiber.Ctx) error {
	workflowIDStr := c.Params("id")
	workflowID, err := uuid.Parse(workflowIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_workflow_id"))
	}

	transitions, err := h.service.ListTransitions(c.UserContext(), workflowID)
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "transitions_retrieved"), transitions)
}

func (h *WorkflowHandler) UpdateTransition(c *fiber.Ctx) error {
	transitionIDStr := c.Params("transition_id")
	transitionID, err := uuid.Parse(transitionIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_transition_id"))
	}

	var req models.WorkflowTransitionUpdateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}

	// Transition Code is system-generated and immutable for EPM940. Discard any
	// client-supplied value so the service never overwrites the generated code.
	if strings.EqualFold(strings.TrimSpace(h.cfg.ClientCode), constants.CLIENT_CODE.EPM940) {
		req.Code = ""
	}

	// Get old transition for action logging
	oldTransition, err := h.service.GetTransition(c.UserContext(), transitionID)
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	transition, err := h.service.UpdateTransition(c.UserContext(), transitionID, &req)
	if err != nil {
		if msg, ok := transitionRequestError(err); ok {
			return utils.ErrorResponse(c, fiber.StatusBadRequest, msg)
		}
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	// Capture values before async logging
	userID := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)
	ipAddress, _ := c.Locals(constants.ContextKeys.IP_ADDRESS).(string)
	userAgent, _ := c.Locals(constants.ContextKeys.USER_AGENT).(string)

	// Log the update with old and new values
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Extract allowed role IDs
		var oldAllowedRoleIDs, newAllowedRoleIDs []string
		for _, r := range oldTransition.AllowedRoles {
			oldAllowedRoleIDs = append(oldAllowedRoleIDs, r.ID.String())
		}
		for _, r := range transition.AllowedRoles {
			newAllowedRoleIDs = append(newAllowedRoleIDs, r.ID.String())
		}
		var oldAssignmentRoleIDs, newAssignmentRoleIDs []string
		for _, r := range oldTransition.AssignmentRoles {
			oldAssignmentRoleIDs = append(oldAssignmentRoleIDs, r.ID.String())
		}
		for _, r := range transition.AssignmentRoles {
			newAssignmentRoleIDs = append(newAssignmentRoleIDs, r.ID.String())
		}

		oldValue := map[string]interface{}{
			"name":                   oldTransition.Name,
			"code":                   oldTransition.Code,
			"description":            oldTransition.Description,
			"from_state_id":          oldTransition.FromStateID.String(),
			"to_state_id":            oldTransition.ToStateID.String(),
			"sort_order":             oldTransition.SortOrder,
			"is_active":              oldTransition.IsActive,
			"allowed_roles":          oldAllowedRoleIDs,
			"assign_department_id":   oldTransition.AssignDepartmentID,
			"auto_detect_department": oldTransition.AutoDetectDepartment,
			"assign_user_id":         oldTransition.AssignUserID,
			"assignment_role_ids":    oldAssignmentRoleIDs,
			"auto_match_user":        oldTransition.AutoMatchUser,
			"manual_select_user":     oldTransition.ManualSelectUser,
			"view_assigne_user_list": oldTransition.ViewAssigneUserList,
		}
		newValue := map[string]interface{}{
			"name":                   transition.Name,
			"code":                   transition.Code,
			"description":            transition.Description,
			"from_state_id":          transition.FromStateID.String(),
			"to_state_id":            transition.ToStateID.String(),
			"sort_order":             transition.SortOrder,
			"is_active":              transition.IsActive,
			"allowed_roles":          newAllowedRoleIDs,
			"assign_department_id":   transition.AssignDepartmentID,
			"auto_detect_department": transition.AutoDetectDepartment,
			"assign_user_id":         transition.AssignUserID,
			"assignment_role_ids":    newAssignmentRoleIDs,
			"auto_match_user":        transition.AutoMatchUser,
			"manual_select_user":     transition.ManualSelectUser,
			"view_assigne_user_list": transition.ViewAssigneUserList,
		}

		// Build detailed change description
		var changeDetails []string
		fieldLabels := map[string]string{
			"name": "Name", "code": "Code", "description": "Description",
			"from_state_id": "From State", "to_state_id": "To State",
			"sort_order": "Sort Order", "is_active": "Is Active", "allowed_roles": "Roles",
			"assign_department_id": "Assign Department", "auto_detect_department": "Auto Detect Dept",
			"assign_user_id": "Assign User", "assignment_role_ids": "Assignment Roles",
			"auto_match_user": "Auto Match User", "manual_select_user": "Manual Select User",
			"view_assigne_user_list": "View Assignee User List",
		}

		for key, label := range fieldLabels {
			if fmt.Sprintf("%v", oldValue[key]) != fmt.Sprintf("%v", newValue[key]) {
				changeDetails = append(changeDetails, label)
			}
		}

		desc := fmt.Sprintf("Updated workflow transition '%s'", transition.Name)
		if len(changeDetails) > 0 {
			desc += fmt.Sprintf(" - changed: %s", strings.Join(changeDetails, ", "))
		}

		_ = h.actionLogService.LogAction(ctx, &services.LogActionParams{
			UserID:      userID,
			Action:      "update",
			Module:      "workflow_transitions",
			ResourceID:  transitionID.String(),
			Description: desc,
			OldValue:    oldValue,
			NewValue:    newValue,
			IPAddress:   ipAddress,
			UserAgent:   userAgent,
			Status:      "success",
		})
	}()

	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "transition_updated"), transition)
}

func (h *WorkflowHandler) DeleteTransition(c *fiber.Ctx) error {
	transitionIDStr := c.Params("transition_id")
	transitionID, err := uuid.Parse(transitionIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_transition_id"))
	}

	// Get transition details for action logging
	oldTransition, err := h.service.GetTransition(c.UserContext(), transitionID)
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	if err := h.service.DeleteTransition(c.UserContext(), transitionID); err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	// Log the deletion
	userID := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)
	ipAddress, _ := c.Locals(constants.ContextKeys.IP_ADDRESS).(string)
	userAgent, _ := c.Locals(constants.ContextKeys.USER_AGENT).(string)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Extract allowed role IDs
		var allowedRoleIDs []string
		for _, r := range oldTransition.AllowedRoles {
			allowedRoleIDs = append(allowedRoleIDs, r.ID.String())
		}
		var assignmentRoleIDs []string
		for _, r := range oldTransition.AssignmentRoles {
			assignmentRoleIDs = append(assignmentRoleIDs, r.ID.String())
		}

		oldValue := map[string]interface{}{
			"name":                   oldTransition.Name,
			"code":                   oldTransition.Code,
			"description":            oldTransition.Description,
			"from_state_id":          oldTransition.FromStateID.String(),
			"to_state_id":            oldTransition.ToStateID.String(),
			"sort_order":             oldTransition.SortOrder,
			"is_active":              oldTransition.IsActive,
			"allowed_roles":          allowedRoleIDs,
			"assign_department_id":   oldTransition.AssignDepartmentID,
			"auto_detect_department": oldTransition.AutoDetectDepartment,
			"assign_user_id":         oldTransition.AssignUserID,
			"assignment_role_ids":    assignmentRoleIDs,
			"auto_match_user":        oldTransition.AutoMatchUser,
			"manual_select_user":     oldTransition.ManualSelectUser,
			"view_assigne_user_list": oldTransition.ViewAssigneUserList,
		}

		_ = h.actionLogService.LogAction(ctx, &services.LogActionParams{
			UserID:      userID,
			Action:      "delete",
			Module:      "workflow_transitions",
			ResourceID:  transitionID.String(),
			Description: fmt.Sprintf("Deleted workflow transition '%s'", oldTransition.Name),
			OldValue:    oldValue,
			IPAddress:   ipAddress,
			UserAgent:   userAgent,
			Status:      "success",
		})
	}()

	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "transition_deleted"), nil)
}

// Transition configuration

func (h *WorkflowHandler) SetTransitionRoles(c *fiber.Ctx) error {
	transitionIDStr := c.Params("id")
	transitionID, err := uuid.Parse(transitionIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_transition_id"))
	}

	var req struct {
		RoleIDs []string `json:"role_ids"`
	}
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}

	roleIDs := make([]uuid.UUID, 0, len(req.RoleIDs))
	for _, idStr := range req.RoleIDs {
		roleID, err := uuid.Parse(idStr)
		if err != nil {
			continue
		}
		roleIDs = append(roleIDs, roleID)
	}

	if err := h.service.SetTransitionRoles(c.UserContext(), transitionID, roleIDs); err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	// Log the role assignment
	userID := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)
	ipAddress, _ := c.Locals(constants.ContextKeys.IP_ADDRESS).(string)
	userAgent, _ := c.Locals(constants.ContextKeys.USER_AGENT).(string)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = h.actionLogService.LogAction(ctx, &services.LogActionParams{
			UserID:      userID,
			Action:      "update",
			Module:      "workflow_transitions",
			ResourceID:  transitionID.String(),
			Description: fmt.Sprintf("Updated roles for transition (ID: %s)", transitionID.String()),
			NewValue:    map[string]interface{}{"role_ids": req.RoleIDs},
			IPAddress:   ipAddress,
			UserAgent:   userAgent,
			Status:      "success",
		})
	}()

	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "transition_roles_updated"), nil)
}

func (h *WorkflowHandler) SetTransitionRequirements(c *fiber.Ctx) error {
	transitionIDStr := c.Params("id")
	transitionID, err := uuid.Parse(transitionIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_transition_id"))
	}

	var req struct {
		Requirements []models.TransitionRequirementRequest `json:"requirements"`
	}
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}

	if err := h.service.SetTransitionRequirements(c.UserContext(), transitionID, req.Requirements); err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	// Log the requirements update
	userID := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)
	ipAddress, _ := c.Locals(constants.ContextKeys.IP_ADDRESS).(string)
	userAgent, _ := c.Locals(constants.ContextKeys.USER_AGENT).(string)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = h.actionLogService.LogAction(ctx, &services.LogActionParams{
			UserID:      userID,
			Action:      "update",
			Module:      "workflow_transitions",
			ResourceID:  transitionID.String(),
			Description: fmt.Sprintf("Updated requirements for transition (ID: %s)", transitionID.String()),
			NewValue:    map[string]interface{}{"requirements": req.Requirements},
			IPAddress:   ipAddress,
			UserAgent:   userAgent,
			Status:      "success",
		})
	}()

	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "transition_requirements_updated"), nil)
}

func (h *WorkflowHandler) SetTransitionActions(c *fiber.Ctx) error {
	transitionIDStr := c.Params("id")
	transitionID, err := uuid.Parse(transitionIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_transition_id"))
	}

	var req struct {
		Actions []models.TransitionActionRequest `json:"actions"`
	}
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}

	if err := h.service.SetTransitionActions(c.UserContext(), transitionID, req.Actions); err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	// Log the actions update
	userID := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)
	ipAddress, _ := c.Locals(constants.ContextKeys.IP_ADDRESS).(string)
	userAgent, _ := c.Locals(constants.ContextKeys.USER_AGENT).(string)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = h.actionLogService.LogAction(ctx, &services.LogActionParams{
			UserID:      userID,
			Action:      "update",
			Module:      "workflow_transitions",
			ResourceID:  transitionID.String(),
			Description: fmt.Sprintf("Updated actions for transition (ID: %s)", transitionID.String()),
			NewValue:    map[string]interface{}{"actions": req.Actions},
			IPAddress:   ipAddress,
			UserAgent:   userAgent,
			Status:      "success",
		})
	}()

	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "transition_actions_updated"), nil)
}

func (h *WorkflowHandler) SetTransitionFieldChanges(c *fiber.Ctx) error {
	transitionIDStr := c.Params("id")
	transitionID, err := uuid.Parse(transitionIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_transition_id"))
	}

	var req struct {
		FieldChanges []models.TransitionFieldChangeRequest `json:"field_changes"`
	}
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}

	if err := h.service.SetTransitionFieldChanges(c.UserContext(), transitionID, req.FieldChanges); err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	// Log the field changes update
	userID := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)
	ipAddress, _ := c.Locals(constants.ContextKeys.IP_ADDRESS).(string)
	userAgent, _ := c.Locals(constants.ContextKeys.USER_AGENT).(string)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_ = h.actionLogService.LogAction(ctx, &services.LogActionParams{
			UserID:      userID,
			Action:      "update",
			Module:      "workflow_transitions",
			ResourceID:  transitionID.String(),
			Description: fmt.Sprintf("Updated field changes for transition (ID: %s)", transitionID.String()),
			NewValue:    map[string]interface{}{"field_changes": req.FieldChanges},
			IPAddress:   ipAddress,
			UserAgent:   userAgent,
			Status:      "success",
		})
	}()

	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "transition_field_changes_updated"), nil)
}

// Helper endpoints

func (h *WorkflowHandler) GetTransitionsToState(c *fiber.Ctx) error {
	stateIDStr := c.Params("state_id")
	stateID, err := uuid.Parse(stateIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_state_id2"))
	}

	transitions, err := h.service.GetTransitionsToState(c.UserContext(), stateID)
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "transitions_retrieved"), transitions)
}

func (h *WorkflowHandler) GetInitialState(c *fiber.Ctx) error {
	workflowIDStr := c.Params("id")
	workflowID, err := uuid.Parse(workflowIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_workflow_id"))
	}

	state, err := h.service.GetInitialState(c.UserContext(), workflowID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, i18n.T(c.UserContext(), "initial_state_not_found"))
	}

	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "initial_state_retrieved"), state)
}

func (h *WorkflowHandler) GetInitialStateMatchingUsers(c *fiber.Ctx) error {
	workflowIDStr := c.Params("id")
	workflowID, err := uuid.Parse(workflowIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_workflow_id"))
	}

	var classificationID, locationID, departmentID *uuid.UUID
	if v := c.Query("classification_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			classificationID = &id
		}
	}
	if v := c.Query("location_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			locationID = &id
		}
	}
	if v := c.Query("department_id"); v != "" {
		if id, err := uuid.Parse(v); err == nil {
			departmentID = &id
		}
	}

	users, err := h.service.GetInitialStateMatchingUsers(c.UserContext(), workflowID, classificationID, locationID, departmentID)
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "matching_users_retrieved"), users)
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

	result, err := h.service.MatchWorkflow(c.UserContext(), &req)
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	return utils.SuccessResponse(c, fiber.StatusOK, i18n.T(c.UserContext(), "workflow_matched"), result)
}

// ExportWorkflow exports a workflow as a JSON file
func (h *WorkflowHandler) ExportWorkflow(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_id"))
	}

	jsonBytes, filename, err := h.service.ExportWorkflow(c.UserContext(), id)
	if err != nil {
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	// Set headers for file download
	c.Set("Content-Type", "application/json")
	c.Set("Content-Disposition", "attachment; filename=\""+filename+"\"")

	return c.Send(jsonBytes)
}

// ImportWorkflow imports a workflow from a JSON file
func (h *WorkflowHandler) ImportWorkflow(c *fiber.Ctx) error {
	// Get file from multipart form
	file, err := c.FormFile("file")
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "no_file_uploaded"))
	}

	log.Println("err check")
	// Validate file size (10MB limit)
	const maxFileSize = 10 * 1024 * 1024 // 10MB
	if file.Size > maxFileSize {
		return utils.ErrorResponse(c, fiber.StatusRequestEntityTooLarge, i18n.T(c.UserContext(), "file_size_10mb"))
	}

	// Open and read file
	fileContent, err := file.Open()
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, i18n.T(c.UserContext(), "failed_to_read_file"))
	}
	defer fileContent.Close()
	log.Println("err check")

	// Read file content
	buf, err := io.ReadAll(fileContent)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, i18n.T(c.UserContext(), "failed_to_read_file_content"))
	}

	// Parse JSON
	var importData models.WorkflowImportData
	if err := json.Unmarshal(buf, &importData); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_json_format"))
	}
	log.Println("Parsed import data:", importData)

	if validationErrors := validation.ValidateStruct(c.UserContext(), &importData); len(validationErrors) != 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"errors":  validationErrors,
		})
	}

	log.Println("Validation passed for import data")
	// Get user ID from context
	userID := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)

	// Import workflow
	workflow, warnings, err := h.service.ImportWorkflow(c.UserContext(), &importData, userID)
	if err != nil {
		log.Println(err)
		return utils.InternalErrorResponse(c, err, i18n.T(c.UserContext(), "internal_server_error"))
	}

	// Build response
	response := models.WorkflowImportResponse{
		Workflow: *workflow,
		Warnings: warnings,
	}

	return utils.SuccessResponse(c, fiber.StatusCreated, i18n.T(c.UserContext(), "workflow_imported"), response)
}

// getChangedFields returns a comma-separated list of field names that changed
func getChangedFields(changes map[string]interface{}) string {
	var fields []string
	for key := range changes {
		fields = append(fields, strings.ReplaceAll(key, "_", " "))
	}
	if len(fields) == 0 {
		return "none"
	}
	return strings.Join(fields, ", ")
}
