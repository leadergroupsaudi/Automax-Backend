package handlers

import (
	"strings"

	"github.com/automax/backend/internal/config"
	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/services"
	"github.com/automax/backend/internal/storage"

	"github.com/automax/backend/pkg/i18n"
	"github.com/gofiber/fiber/v2"
)

type SettingsHandler struct {
	settingsService services.SettingsService
	storage         *storage.MinIOStorage
	minioEndpoint   string
}

func NewSettingsHandler(settingsService services.SettingsService, fileStorage *storage.MinIOStorage, cfg *config.Config) *SettingsHandler {
	return &SettingsHandler{
		settingsService: settingsService,
		storage:         fileStorage,
		minioEndpoint:   cfg.MinIO.Endpoint,
	}
}

// UploadBrandingImage uploads an image to the shared MinIO bucket and returns
// its permanent public URL. The caller persists that URL in settings.
func (h *SettingsHandler) UploadBrandingImage(c *fiber.Ctx) error {
	file, err := c.FormFile("image")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"success": false, "error": i18n.T(c.UserContext(), "no_file_uploaded")})
	}
	if file.Size <= 0 || file.Size > 5*1024*1024 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"success": false, "error": "Image must be no larger than 5 MB"})
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(file.Header.Get("Content-Type"), ";")[0]))
	allowed := map[string]bool{
		"image/jpeg": true, "image/png": true, "image/gif": true, "image/webp": true,
		"image/svg+xml": true, "image/x-icon": true, "image/vnd.microsoft.icon": true,
	}
	if !allowed[contentType] {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"success": false, "error": "Unsupported image type"})
	}
	src, err := file.Open()
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"success": false, "error": "Failed to open image"})
	}
	defer src.Close()
	key, err := h.storage.UploadFile(c.UserContext(), src, file, "branding")
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{"success": false, "error": "Failed to upload image"})
	}
	return c.JSON(fiber.Map{"success": true, "data": fiber.Map{"url": h.storage.GetPublicURL(key, h.minioEndpoint)}})
}

// GetSettings retrieves the application settings (public endpoint)
// @Summary Get application settings
// @Description Get application-wide settings and branding configuration
// @Tags Settings
// @Accept json
// @Produce json
// @Success 200 {object} models.SettingsResponse
// @Failure 500 {object} fiber.Map
// @Router /api/v1/settings [get]
func (h *SettingsHandler) GetSettings(c *fiber.Ctx) error {
	settings, err := h.settingsService.GetSettings(c.UserContext())
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to retrieve settings",
		})
	}

	return c.JSON(fiber.Map{
		"success": true,
		"data":    settings,
	})
}

// UpdateSettings updates the application settings (admin only)
// @Summary Update application settings
// @Description Update application-wide settings and branding (requires settings:update permission)
// @Tags Settings
// @Accept json
// @Produce json
// @Param request body models.SettingsUpdateRequest true "Settings update data"
// @Success 200 {object} models.SettingsResponse
// @Failure 400 {object} fiber.Map
// @Failure 403 {object} fiber.Map
// @Failure 500 {object} fiber.Map
// @Router /api/v1/admin/settings [put]
func (h *SettingsHandler) UpdateSettings(c *fiber.Ctx) error {
	var req models.SettingsUpdateRequest

	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"error": i18n.T(c.UserContext(), "invalid_request_body"),
		})
	}

	settings, err := h.settingsService.UpdateSettings(c.UserContext(), &req)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"error": "Failed to update settings",
		})
	}

	return c.JSON(fiber.Map{
		"success": true,
		"message": "Settings updated successfully",
		"data":    settings,
	})
}

