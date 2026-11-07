package handlers

import (
	"context"
	"fmt"
	"io"
	"log"

	"github.com/automax/backend/internal/middleware"
	"github.com/automax/backend/internal/services"
	"github.com/automax/backend/pkg/constants"
	"github.com/automax/backend/pkg/utils"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const kpiImportMaxUploadSize = 20 << 20

// KpiImportHandler serves the KPI bulk-import endpoints for template download,
// validation and commit.
type KpiImportHandler struct {
	db           *gorm.DB
	actionLogSvc services.ActionLogService
	workflowSvc  *services.KpiWorkflowService
}

func NewKpiImportHandler(db *gorm.DB, actionLogSvc services.ActionLogService, workflowSvc *services.KpiWorkflowService) *KpiImportHandler {
	return &KpiImportHandler{db: db, actionLogSvc: actionLogSvc, workflowSvc: workflowSvc}
}

func (h *KpiImportHandler) readRequest(c *fiber.Ctx) ([]byte, string, services.KpiImportOptions, error) {
	opts := services.KpiImportOptions{
		ExistingMode: c.FormValue("existing_mode", services.KpiImportExistingSkip),
		ErrorMode:    c.FormValue("error_mode", services.KpiImportErrorsSkipInvalid),
	}
	file, err := c.FormFile("file")
	if err != nil {
		return nil, "", opts, fmt.Errorf("an Excel file is required")
	}
	if file.Size > kpiImportMaxUploadSize {
		return nil, "", opts, fmt.Errorf("the uploaded Excel file exceeds the %d MB limit", kpiImportMaxUploadSize>>20)
	}
	f, err := file.Open()
	if err != nil {
		return nil, "", opts, fmt.Errorf("failed to open the uploaded file")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, kpiImportMaxUploadSize+1))
	if err != nil {
		return nil, "", opts, fmt.Errorf("failed to read the uploaded file")
	}
	if len(data) > kpiImportMaxUploadSize {
		return nil, "", opts, fmt.Errorf("the uploaded Excel file exceeds the %d MB limit", kpiImportMaxUploadSize>>20)
	}
	return data, file.Filename, opts, nil
}

// Template returns the blank KPI Dictionary workbook accepted by Validate and
// Commit. The lang query value (en or ar) selects a single header language;
// the mandatory sheets and year columns are pre-filled.
func (h *KpiImportHandler) Template(c *fiber.Ctx) error {
	lang := c.Query("lang", services.KpiTemplateLangEN)
	if lang != services.KpiTemplateLangEN && lang != services.KpiTemplateLangAR {
		lang = services.KpiTemplateLangEN
	}
	data, err := services.BuildKpiDictionaryImportTemplate(lang)
	if err != nil {
		log.Printf("[kpi_import] Template: BuildKpiDictionaryImportTemplate failed: %v", err)
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, "Failed to build the KPI Dictionary import template")
	}
	c.Set(fiber.HeaderContentType, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Set(fiber.HeaderContentDisposition, fmt.Sprintf(`attachment; filename="%s"`, services.KpiDictionaryTemplateFileNameFor(lang)))
	c.Set(fiber.HeaderCacheControl, "no-store")
	return c.Send(data)
}

func (h *KpiImportHandler) Validate(c *fiber.Ctx) error {
	data, name, opts, err := h.readRequest(c)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}
	userID := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)
	res, err := services.ValidateKpiDictionaryImport(c.UserContext(), h.db, data, name, userID, opts)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}
	return utils.SuccessResponse(c, fiber.StatusOK, "", res)
}

func (h *KpiImportHandler) Commit(c *fiber.Ctx) error {
	data, name, opts, err := h.readRequest(c)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}
	// Overwriting existing KPIs needs update rights on top of create.
	if opts.ExistingMode == services.KpiImportExistingUpdate && !hasTargetPermission(c, "kpi:update") {
		return utils.ErrorResponse(c, fiber.StatusForbidden, "Updating existing KPIs requires the kpi:update permission")
	}
	userID := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)

	res, err := services.CommitKpiDictionaryImport(c.UserContext(), h.db, data, name, userID, opts,
		func(ctx context.Context, kpiType string, kpiID uuid.UUID) error {
			return h.workflowSvc.InitiateKpiDictionaryWorkflow(ctx, kpiType, kpiID, userID)
		})
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	status := "success"
	if !res.Committed {
		status = "failed"
	}
	middleware.LogAction(c, h.actionLogSvc, &services.LogActionParams{
		Action: "import",
		Module: "kpi",
		Description: fmt.Sprintf("KPI Dictionary import %q: %d created, %d updated, %d skipped, %d rejected, %d rolled back",
			name, res.Totals.Created, res.Totals.Updated, res.Totals.Skipped, res.Totals.Rejected, res.Totals.RolledBack),
		Status: status,
	})
	return utils.SuccessResponse(c, fiber.StatusOK, "", res)
}

// ─── KPI Metrics import ────────────────────────────────────────────────────

// MetricsTemplate returns the blank KPI Metrics workbook for the chosen language.
func (h *KpiImportHandler) MetricsTemplate(c *fiber.Ctx) error {
	lang := c.Query("lang", services.KpiTemplateLangEN)
	if lang != services.KpiTemplateLangEN && lang != services.KpiTemplateLangAR {
		lang = services.KpiTemplateLangEN
	}
	data, err := services.BuildKpiMetricsImportTemplate(lang)
	if err != nil {
		log.Printf("[kpi_import] MetricsTemplate: BuildKpiMetricsImportTemplate failed: %v", err)
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, "Failed to build the KPI Metrics import template")
	}
	c.Set(fiber.HeaderContentType, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Set(fiber.HeaderContentDisposition, fmt.Sprintf(`attachment; filename="%s"`, services.KpiMetricsTemplateFileNameFor(lang)))
	c.Set(fiber.HeaderCacheControl, "no-store")
	return c.Send(data)
}

func (h *KpiImportHandler) ValidateMetrics(c *fiber.Ctx) error {
	data, name, opts, err := h.readRequest(c)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}
	userID := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)
	res, err := services.ValidateKpiMetricsImport(c.UserContext(), h.db, data, name, userID, opts)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}
	return utils.SuccessResponse(c, fiber.StatusOK, "", res)
}

func (h *KpiImportHandler) CommitMetrics(c *fiber.Ctx) error {
	data, name, opts, err := h.readRequest(c)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}
	// Overwriting existing metrics needs update rights on top of create.
	if opts.ExistingMode == services.KpiImportExistingUpdate && !hasTargetPermission(c, "kpi:update") {
		return utils.ErrorResponse(c, fiber.StatusForbidden, "Updating existing metrics requires the kpi:update permission")
	}
	userID := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)

	res, err := services.CommitKpiMetricsImport(c.UserContext(), h.db, data, name, userID, opts)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	status := "success"
	if !res.Committed {
		status = "failed"
	}
	middleware.LogAction(c, h.actionLogSvc, &services.LogActionParams{
		Action: "import",
		Module: "kpi",
		Description: fmt.Sprintf("KPI Metrics import %q: %d created, %d updated, %d skipped, %d rejected, %d rolled back",
			name, res.Totals.Created, res.Totals.Updated, res.Totals.Skipped, res.Totals.Rejected, res.Totals.RolledBack),
		Status: status,
	})
	return utils.SuccessResponse(c, fiber.StatusOK, "", res)
}

// ─── KPI Targets import ────────────────────────────────────────────────────

// TargetsTemplate returns the blank KPI Targets workbook for the chosen language.
func (h *KpiImportHandler) TargetsTemplate(c *fiber.Ctx) error {
	lang := c.Query("lang", services.KpiTemplateLangEN)
	if lang != services.KpiTemplateLangEN && lang != services.KpiTemplateLangAR {
		lang = services.KpiTemplateLangEN
	}
	data, err := services.BuildKpiTargetsImportTemplate(lang)
	if err != nil {
		log.Printf("[kpi_import] TargetsTemplate: BuildKpiTargetsImportTemplate failed: %v", err)
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, "Failed to build the KPI Targets import template")
	}
	c.Set(fiber.HeaderContentType, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Set(fiber.HeaderContentDisposition, fmt.Sprintf(`attachment; filename="%s"`, services.KpiTargetsTemplateFileNameFor(lang)))
	c.Set(fiber.HeaderCacheControl, "no-store")
	return c.Send(data)
}

func (h *KpiImportHandler) ValidateTargets(c *fiber.Ctx) error {
	data, name, opts, err := h.readRequest(c)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}
	userID := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)
	res, err := services.ValidateKpiTargetsImport(c.UserContext(), h.db, data, name, userID, opts)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}
	return utils.SuccessResponse(c, fiber.StatusOK, "", res)
}

func (h *KpiImportHandler) CommitTargets(c *fiber.Ctx) error {
	data, name, opts, err := h.readRequest(c)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}
	// Overwriting existing targets needs set rights, which the route already
	// requires; the explicit check keeps the error clear for direct API use.
	if opts.ExistingMode == services.KpiImportExistingUpdate && !hasTargetPermission(c, "targets:set") {
		return utils.ErrorResponse(c, fiber.StatusForbidden, "Updating existing targets requires the targets:set permission")
	}
	userID := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)

	res, err := services.CommitKpiTargetsImport(c.UserContext(), h.db, data, name, userID, opts)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	status := "success"
	if !res.Committed {
		status = "failed"
	}
	middleware.LogAction(c, h.actionLogSvc, &services.LogActionParams{
		Action: "import",
		Module: "kpi",
		Description: fmt.Sprintf("KPI Targets import %q: %d created, %d updated, %d skipped, %d rejected, %d rolled back",
			name, res.Totals.Created, res.Totals.Updated, res.Totals.Skipped, res.Totals.Rejected, res.Totals.RolledBack),
		Status: status,
	})
	return utils.SuccessResponse(c, fiber.StatusOK, "", res)
}

// ─── KPI Master Data import ────────────────────────────────────────────────

// MasterDataTemplate returns the blank workbook for one master-data entity type.
func (h *KpiImportHandler) MasterDataTemplate(c *fiber.Ctx) error {
	entityType := c.Query("type")
	if !services.IsValidKpiMasterDataEntityType(entityType) {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, fmt.Sprintf("Unknown master-data type %q", entityType))
	}
	lang := c.Query("lang", services.KpiTemplateLangEN)
	if lang != services.KpiTemplateLangEN && lang != services.KpiTemplateLangAR {
		lang = services.KpiTemplateLangEN
	}
	data, err := services.BuildKpiMasterDataImportTemplate(entityType, lang)
	if err != nil {
		log.Printf("[kpi_import] MasterDataTemplate: BuildKpiMasterDataImportTemplate(%q) failed: %v", entityType, err)
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, "Failed to build the master-data import template")
	}
	c.Set(fiber.HeaderContentType, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Set(fiber.HeaderContentDisposition, fmt.Sprintf(`attachment; filename="%s"`, services.KpiMasterDataTemplateFileName(entityType, lang)))
	c.Set(fiber.HeaderCacheControl, "no-store")
	return c.Send(data)
}

func (h *KpiImportHandler) masterDataEntityType(c *fiber.Ctx) (string, error) {
	entityType := c.Query("type")
	if entityType == "" {
		entityType = c.FormValue("type")
	}
	if !services.IsValidKpiMasterDataEntityType(entityType) {
		return "", fmt.Errorf("unknown master-data type %q", entityType)
	}
	return entityType, nil
}

func (h *KpiImportHandler) ValidateMasterData(c *fiber.Ctx) error {
	entityType, err := h.masterDataEntityType(c)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}
	data, name, opts, err := h.readRequest(c)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}
	userID := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)
	res, err := services.ValidateKpiMasterDataImport(c.UserContext(), h.db, entityType, data, name, userID, opts)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}
	return utils.SuccessResponse(c, fiber.StatusOK, "", res)
}

func (h *KpiImportHandler) CommitMasterData(c *fiber.Ctx) error {
	entityType, err := h.masterDataEntityType(c)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}
	data, name, opts, err := h.readRequest(c)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}
	userID := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)

	res, err := services.CommitKpiMasterDataImport(c.UserContext(), h.db, entityType, data, name, userID, opts)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	status := "success"
	if !res.Committed {
		status = "failed"
	}
	middleware.LogAction(c, h.actionLogSvc, &services.LogActionParams{
		Action: "import",
		Module: "kpi",
		Description: fmt.Sprintf("KPI Master Data (%s) import %q: %d created, %d updated, %d skipped, %d rejected, %d rolled back",
			entityType, name, res.Totals.Created, res.Totals.Updated, res.Totals.Skipped, res.Totals.Rejected, res.Totals.RolledBack),
		Status: status,
	})
	return utils.SuccessResponse(c, fiber.StatusOK, "", res)
}
