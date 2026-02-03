package handlers

import (
	"strconv"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/services"
	"github.com/automax/backend/pkg/utils"
	"github.com/go-playground/validator/v10"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type CallLogHandler struct {
	service   services.CallLogService
	validator *validator.Validate
}

func NewCallLogHandler(service services.CallLogService, validator *validator.Validate) *CallLogHandler {
	return &CallLogHandler{
		service:   service,
		validator: validator,
	}
}

// CreateCallLog handles POST /admin/call-logs
func (h *CallLogHandler) CreateCallLog(c *fiber.Ctx) error {
	var req models.CallLogCreateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	if err := h.validator.Struct(&req); err != nil {
		return utils.FormatValidationError(c, err)
	}

	// Get user from context (assuming middleware sets it)
	userID, ok := c.Locals("user_id").(uuid.UUID)
	if !ok {
		return utils.ErrorResponse(c, fiber.StatusUnauthorized, "User not authenticated")
	}

	callLog, err := h.service.CreateCallLog(c.Context(), &req, userID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"success": true,
		"data":    callLog,
	})
}

// GetCallLog handles GET /admin/call-logs/:id
func (h *CallLogHandler) GetCallLog(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid call log ID")
	}

	callLog, err := h.service.GetCallLog(c.Context(), id)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "Call log not found")
	}

	return c.JSON(fiber.Map{
		"success": true,
		"data":    callLog,
	})
}

// UpdateCallLog handles PUT /admin/call-logs/:id
func (h *CallLogHandler) UpdateCallLog(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid call log ID")
	}

	var req models.CallLogUpdateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	if err := h.validator.Struct(&req); err != nil {
		return utils.FormatValidationError(c, err)
	}

	callLog, err := h.service.UpdateCallLog(c.Context(), id, &req)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return c.JSON(fiber.Map{
		"success": true,
		"data":    callLog,
	})
}

// DeleteCallLog handles DELETE /admin/call-logs/:id
func (h *CallLogHandler) DeleteCallLog(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid call log ID")
	}

	if err := h.service.DeleteCallLog(c.Context(), id); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return c.JSON(fiber.Map{
		"success": true,
		"message": "Call log deleted successfully",
	})
}

// ListCallLogs handles GET /admin/call-logs
func (h *CallLogHandler) ListCallLogs(c *fiber.Ctx) error {
	filter := &models.CallLogFilter{
		Page:  1,
		Limit: 20,
	}

	// Parse query parameters
	if page := c.Query("page"); page != "" {
		if p, err := strconv.Atoi(page); err == nil {
			filter.Page = p
		}
	}
	if limit := c.Query("limit"); limit != "" {
		if l, err := strconv.Atoi(limit); err == nil {
			filter.Limit = l
		}
	}
	if createdBy := c.Query("created_by"); createdBy != "" {
		if id, err := uuid.Parse(createdBy); err == nil {
			filter.CreatedBy = &id
		}
	}
	if status := c.Query("status"); status != "" {
		filter.Status = status
	}
	if search := c.Query("search"); search != "" {
		filter.Search = search
	}
	if startDate := c.Query("start_date"); startDate != "" {
		if t, err := time.Parse("2006-01-02", startDate); err == nil {
			filter.StartDate = &t
		}
	}
	if endDate := c.Query("end_date"); endDate != "" {
		if t, err := time.Parse("2006-01-02", endDate); err == nil {
			// Set to end of day
			t = t.Add(24*time.Hour - time.Second)
			filter.EndDate = &t
		}
	}

	callLogs, total, err := h.service.ListCallLogs(c.Context(), filter)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	totalPages := (int(total) + filter.Limit - 1) / filter.Limit

	return c.JSON(fiber.Map{
		"success":     true,
		"data":        callLogs,
		"total_items": total,
		"total_pages": totalPages,
		"page":        filter.Page,
		"limit":       filter.Limit,
	})
}

// GetStats handles GET /admin/call-logs/stats
func (h *CallLogHandler) GetStats(c *fiber.Ctx) error {
	stats, err := h.service.GetStats(c.Context())
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return c.JSON(fiber.Map{
		"success": true,
		"data":    stats,
	})
}

// StartCall handles POST /api/v1/calls/start
func (h *CallLogHandler) StartCall(c *fiber.Ctx) error {
	var req struct {
		CallUUID     string      `json:"call_uuid" validate:"required"`
		Participants []uuid.UUID `json:"participants,omitempty"`
	}

	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	if err := h.validator.Struct(&req); err != nil {
		return utils.FormatValidationError(c, err)
	}

	userID, ok := c.Locals("user_id").(uuid.UUID)
	if !ok {
		return utils.ErrorResponse(c, fiber.StatusUnauthorized, "User not authenticated")
	}

	callLog, err := h.service.StartCall(c.Context(), req.CallUUID, userID, req.Participants)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"success": true,
		"data":    callLog,
	})
}

// EndCall handles POST /api/v1/calls/:call_uuid/end
func (h *CallLogHandler) EndCall(c *fiber.Ctx) error {
	callUUID := c.Params("call_uuid")
	if callUUID == "" {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Call UUID is required")
	}

	var req struct {
		EndAt *time.Time `json:"end_at,omitempty"`
	}

	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	callLog, err := h.service.EndCall(c.Context(), callUUID, req.EndAt)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return c.JSON(fiber.Map{
		"success": true,
		"data":    callLog,
	})
}

// JoinCall handles POST /api/v1/calls/:call_uuid/join
func (h *CallLogHandler) JoinCall(c *fiber.Ctx) error {
	callUUID := c.Params("call_uuid")
	if callUUID == "" {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Call UUID is required")
	}

	userID, ok := c.Locals("user_id").(uuid.UUID)
	if !ok {
		return utils.ErrorResponse(c, fiber.StatusUnauthorized, "User not authenticated")
	}

	if err := h.service.JoinCall(c.Context(), callUUID, userID); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return c.JSON(fiber.Map{
		"success": true,
		"message": "Successfully joined the call",
	})
}