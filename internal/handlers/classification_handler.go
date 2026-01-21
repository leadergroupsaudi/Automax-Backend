package handlers

import (
	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/pkg/utils"
	"github.com/go-playground/validator/v10"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type ClassificationHandler struct {
	repo      repository.ClassificationRepository
	validator *validator.Validate
}

func NewClassificationHandler(repo repository.ClassificationRepository) *ClassificationHandler {
	return &ClassificationHandler{
		repo:      repo,
		validator: validator.New(),
	}
}

func (h *ClassificationHandler) Create(c *fiber.Ctx) error {
	var req models.ClassificationCreateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	if err := h.validator.Struct(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}

	classType := "both"
	if req.Type != "" {
		classType = req.Type
	}

	classification := &models.Classification{
		Name:        req.Name,
		Description: req.Description,
		Type:        classType,
		ParentID:    req.ParentID,
		SortOrder:   req.SortOrder,
		IsActive:    true,
	}

	if err := h.repo.Create(c.Context(), classification); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusCreated, "Classification created", models.ToClassificationResponse(classification))
}

func (h *ClassificationHandler) GetByID(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid ID")
	}

	classification, err := h.repo.FindByID(c.Context(), id)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "Classification not found")
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Classification retrieved", models.ToClassificationResponse(classification))
}

func (h *ClassificationHandler) Update(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid ID")
	}

	var req models.ClassificationUpdateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	classification, err := h.repo.FindByID(c.Context(), id)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "Classification not found")
	}

	if req.Name != "" {
		classification.Name = req.Name
	}
	if req.Description != "" {
		classification.Description = req.Description
	}
	if req.Type != nil {
		classification.Type = *req.Type
	}
	if req.IsActive != nil {
		classification.IsActive = *req.IsActive
	}
	if req.SortOrder != nil {
		classification.SortOrder = *req.SortOrder
	}

	if err := h.repo.Update(c.Context(), classification); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Classification updated", models.ToClassificationResponse(classification))
}

func (h *ClassificationHandler) Delete(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid ID")
	}

	if err := h.repo.Delete(c.Context(), id); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Classification deleted", nil)
}

func (h *ClassificationHandler) List(c *fiber.Ctx) error {
	var classifications []models.Classification
	var err error

	classType := c.Query("type")
	if classType != "" {
		classifications, err = h.repo.ListByType(c.Context(), classType)
	} else {
		classifications, err = h.repo.List(c.Context())
	}
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	responses := make([]models.ClassificationResponse, len(classifications))
	for i, cls := range classifications {
		responses[i] = models.ToClassificationResponse(&cls)
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Classifications retrieved", responses)
}

func (h *ClassificationHandler) GetTree(c *fiber.Ctx) error {
	var tree []models.Classification
	var err error

	classType := c.Query("type")
	if classType != "" {
		tree, err = h.repo.GetTreeByType(c.Context(), classType)
	} else {
		tree, err = h.repo.GetTree(c.Context())
	}
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	responses := make([]models.ClassificationResponse, len(tree))
	for i, cls := range tree {
		responses[i] = models.ToClassificationResponse(&cls)
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Classification tree retrieved", responses)
}

func (h *ClassificationHandler) GetChildren(c *fiber.Ctx) error {
	parentIDStr := c.Query("parent_id")

	var children []models.Classification
	var err error

	if parentIDStr == "" {
		children, err = h.repo.GetByParentID(c.Context(), nil)
	} else {
		parentID, parseErr := uuid.Parse(parentIDStr)
		if parseErr != nil {
			return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid parent ID")
		}
		children, err = h.repo.GetByParentID(c.Context(), &parentID)
	}

	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	responses := make([]models.ClassificationResponse, len(children))
	for i, cls := range children {
		responses[i] = models.ToClassificationResponse(&cls)
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Children retrieved", responses)
}
