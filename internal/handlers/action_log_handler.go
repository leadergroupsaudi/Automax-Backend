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

type ActionLogHandler struct {
	service   services.ActionLogService
	validator *validator.Validate
}

func NewActionLogHandler(service services.ActionLogService, validator *validator.Validate) *ActionLogHandler {
	return &ActionLogHandler{
		service:   service,
		validator: validator,
	}
}

// ListActionLogs handles GET /admin/action-logs
func (h *ActionLogHandler) ListActionLogs(c *fiber.Ctx) error {
	filter := &models.ActionLogFilter{
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
	if userID := c.Query("user_id"); userID != "" {
		if id, err := uuid.Parse(userID); err == nil {
			filter.UserID = &id
		}
	}
	if action := c.Query("action"); action != "" {
		filter.Action = action
	}
	if module := c.Query("module"); module != "" {
		filter.Module = module
	}
	if status := c.Query("status"); status != "" {
		filter.Status = status
	}
	if resourceID := c.Query("resource_id"); resourceID != "" {
		filter.ResourceID = resourceID
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

	logs, total, err := h.service.ListActionLogs(c.Context(), filter)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	totalPages := (int(total) + filter.Limit - 1) / filter.Limit

	return c.JSON(fiber.Map{
		"success":     true,
		"data":        logs,
		"total_items": total,
		"total_pages": totalPages,
		"page":        filter.Page,
		"limit":       filter.Limit,
	})
}

// GetActionLog handles GET /admin/action-logs/:id
func (h *ActionLogHandler) GetActionLog(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid action log ID")
	}

	log, err := h.service.GetActionLog(c.Context(), id)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "Action log not found")
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Action log retrieved successfully", log)
}

// GetStats handles GET /admin/action-logs/stats
func (h *ActionLogHandler) GetStats(c *fiber.Ctx) error {
	stats, err := h.service.GetStats(c.Context())
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Stats retrieved successfully", stats)
}

// GetFilterOptions handles GET /admin/action-logs/filter-options
func (h *ActionLogHandler) GetFilterOptions(c *fiber.Ctx) error {
	options, err := h.service.GetFilterOptions(c.Context())
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Filter options retrieved successfully", options)
}

// GetUserActions handles GET /admin/action-logs/user/:id
func (h *ActionLogHandler) GetUserActions(c *fiber.Ctx) error {
	userIDStr := c.Params("id")
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid user ID")
	}

	page, _ := strconv.Atoi(c.Query("page", "1"))
	limit, _ := strconv.Atoi(c.Query("limit", "20"))

	logs, total, err := h.service.GetUserActions(c.Context(), userID, page, limit)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	totalPages := (int(total) + limit - 1) / limit

	return c.JSON(fiber.Map{
		"success":     true,
		"data":        logs,
		"total_items": total,
		"total_pages": totalPages,
		"page":        page,
		"limit":       limit,
	})
}

// CleanupOldLogs handles DELETE /admin/action-logs/cleanup
func (h *ActionLogHandler) CleanupOldLogs(c *fiber.Ctx) error {
	retentionDays, _ := strconv.Atoi(c.Query("retention_days", "90"))
	if retentionDays < 7 {
		retentionDays = 7 // Minimum 7 days retention
	}

	deleted, err := h.service.CleanupOldLogs(c.Context(), retentionDays)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Old logs cleaned up", fiber.Map{
		"deleted_count":  deleted,
		"retention_days": retentionDays,
	})
}
