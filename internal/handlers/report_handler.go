package handlers

import (
	"strconv"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/services"
	"github.com/automax/backend/pkg/utils"
	"github.com/go-playground/validator/v10"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type ReportHandler struct {
	service   services.ReportService
	validator *validator.Validate
}

func NewReportHandler(service services.ReportService) *ReportHandler {
	return &ReportHandler{
		service:   service,
		validator: validator.New(),
	}
}

// Report CRUD

func (h *ReportHandler) CreateReport(c *fiber.Ctx) error {
	var req models.ReportCreateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	if err := h.validator.Struct(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}

	userID := c.Locals("user_id").(uuid.UUID)

	report, err := h.service.CreateReport(c.Context(), &req, userID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusCreated, "Report created successfully", report)
}

func (h *ReportHandler) GetReport(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid report ID")
	}

	report, err := h.service.GetReport(c.Context(), id)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "Report not found")
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Report retrieved successfully", report)
}

func (h *ReportHandler) ListReports(c *fiber.Ctx) error {
	filter := &models.ReportFilter{}

	// Parse query parameters
	if page := c.Query("page"); page != "" {
		if p, err := strconv.Atoi(page); err == nil {
			filter.Page = p
		}
	}
	if filter.Page < 1 {
		filter.Page = 1
	}

	if limit := c.Query("limit"); limit != "" {
		if l, err := strconv.Atoi(limit); err == nil {
			filter.Limit = l
		}
	}
	if filter.Limit < 1 || filter.Limit > 100 {
		filter.Limit = 20
	}

	filter.Search = c.Query("search")

	if dataSource := c.Query("data_source"); dataSource != "" {
		filter.DataSource = &dataSource
	}

	if isPublic := c.Query("is_public"); isPublic != "" {
		pub := isPublic == "true"
		filter.IsPublic = &pub
	}

	// Get own reports or all if admin
	if mine := c.Query("mine"); mine == "true" {
		userID := c.Locals("user_id").(uuid.UUID)
		filter.CreatedByID = &userID
	}

	reports, total, err := h.service.ListReports(c.Context(), filter)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	totalPages := (int(total) + filter.Limit - 1) / filter.Limit

	return c.JSON(fiber.Map{
		"success":     true,
		"data":        reports,
		"page":        filter.Page,
		"limit":       filter.Limit,
		"total_items": total,
		"total_pages": totalPages,
	})
}

func (h *ReportHandler) UpdateReport(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid report ID")
	}

	var req models.ReportUpdateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	userID := c.Locals("user_id").(uuid.UUID)

	report, err := h.service.UpdateReport(c.Context(), id, &req, userID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Report updated successfully", report)
}

func (h *ReportHandler) DeleteReport(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid report ID")
	}

	userID := c.Locals("user_id").(uuid.UUID)

	if err := h.service.DeleteReport(c.Context(), id, userID); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Report deleted successfully", nil)
}

func (h *ReportHandler) DuplicateReport(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid report ID")
	}

	userID := c.Locals("user_id").(uuid.UUID)

	report, err := h.service.DuplicateReport(c.Context(), id, userID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusCreated, "Report duplicated successfully", report)
}

// Report Execution

func (h *ReportHandler) ExecuteReport(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid report ID")
	}

	var req models.ReportExecuteRequest
	if err := c.BodyParser(&req); err != nil {
		// Allow empty body for simple execution
		req = models.ReportExecuteRequest{}
	}

	userID := c.Locals("user_id").(uuid.UUID)

	result, err := h.service.ExecuteReport(c.Context(), id, &req, userID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Report executed successfully", result)
}

func (h *ReportHandler) PreviewReport(c *fiber.Ctx) error {
	var req models.ReportCreateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	if req.DataSource == "" {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "data_source is required")
	}

	result, err := h.service.PreviewReport(c.Context(), &req)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Preview generated successfully", result)
}

func (h *ReportHandler) GetExecutionHistory(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid report ID")
	}

	page, _ := strconv.Atoi(c.Query("page", "1"))
	limit, _ := strconv.Atoi(c.Query("limit", "20"))

	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}

	executions, total, err := h.service.GetExecutionHistory(c.Context(), id, page, limit)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	totalPages := (int(total) + limit - 1) / limit

	return c.JSON(fiber.Map{
		"success":     true,
		"data":        executions,
		"page":        page,
		"limit":       limit,
		"total_items": total,
		"total_pages": totalPages,
	})
}

// Metadata

func (h *ReportHandler) GetDataSources(c *fiber.Ctx) error {
	dataSources := h.service.GetDataSources(c.Context())
	return utils.SuccessResponse(c, fiber.StatusOK, "Data sources retrieved successfully", dataSources)
}
