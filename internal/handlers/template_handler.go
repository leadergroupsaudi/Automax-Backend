package handlers

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
)

type NotificationTemplateHandler struct {
	repo repository.NotificationTemplateRepository
}

func NewNotificationTemplateHandler(
	repo repository.NotificationTemplateRepository,
) *NotificationTemplateHandler {
	return &NotificationTemplateHandler{
		repo: repo,
	}
}

// POST /api/v1/templates
// func (h *NotificationTemplateHandler) Create(c *fiber.Ctx) error {
// 	var req map[string]interface{}
// 	if err := c.BodyParser(&req); err != nil {
// 		return fiber.ErrBadRequest
// 	}

// 	if err := h.repo.Create(c.Context(), req); err != nil {
// 		return err
// 	}

// 	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
// 		"success": true,
// 	})
// }

func (h *NotificationTemplateHandler) Create(c *fiber.Ctx) error {
	var tpl models.NotificationTemplate

	if err := c.BodyParser(&tpl); err != nil {
		return fiber.ErrBadRequest
	}

	// Optional: enforce defaults
	if tpl.ID == uuid.Nil {
		tpl.ID = uuid.New()
	}
	tpl.IsActive = true

	if err := h.repo.Create(c.Context(), &tpl); err != nil {
		return err
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"success": true,
		"data":    tpl,
	})
}

// GET /api/v1/templates
func (h *NotificationTemplateHandler) List(c *fiber.Ctx) error {
	templates, err := h.repo.List(c.Context())
	if err != nil {
		return err
	}

	return c.JSON(fiber.Map{
		"success": true,
		"data":    templates,
	})
}

// GET /api/v1/templates/:id
func (h *NotificationTemplateHandler) GetByID(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.ErrBadRequest
	}

	template, err := h.repo.FindByID(c.Context(), id)
	if err != nil {
		return err
	}

	return c.JSON(fiber.Map{
		"success": true,
		"data":    template,
	})
}

// PUT /api/v1/templates/:id

func (h *NotificationTemplateHandler) Update(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.ErrBadRequest
	}

	// fetch existing template
	tpl, err := h.repo.FindByID(c.Context(), id)
	if err != nil {
		return err
	}

	// parse update into the SAME struct
	if err := c.BodyParser(tpl); err != nil {
		return fiber.ErrBadRequest
	}

	if err := h.repo.Update(c.Context(), tpl); err != nil {
		return err
	}

	return c.JSON(fiber.Map{
		"success": true,
		"data":    tpl,
	})
}

// func (h *NotificationTemplateHandler) Update(c *fiber.Ctx) error {
// 	id, err := uuid.Parse(c.Params("id"))
// 	if err != nil {
// 		return fiber.ErrBadRequest
// 	}

// 	var req map[string]interface{}
// 	if err := c.BodyParser(&req); err != nil {
// 		return fiber.ErrBadRequest
// 	}

// 	if err := h.repo.Update(c.Context(), id, req); err != nil {
// 		return err
// 	}

// 	return c.JSON(fiber.Map{
// 		"success": true,
// 	})
// }

// DELETE /api/v1/templates/:id
func (h *NotificationTemplateHandler) Delete(c *fiber.Ctx) error {
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return fiber.ErrBadRequest
	}

	if err := h.repo.Delete(c.Context(), id); err != nil {
		return err
	}

	return c.JSON(fiber.Map{
		"success": true,
	})
}

// func (h *TemplateHandler) Create(c *fiber.Ctx) error {
// 	var tpl models.NotificationTemplate
// 	if err := c.BodyParser(&tpl); err != nil {
// 		return c.Status(400).JSON(fiber.Map{"error": err.Error()})
// 	}

// 	if err := h.DB.Create(&tpl).Error; err != nil {
// 		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
// 	}

// 	return c.JSON(tpl)
// }
