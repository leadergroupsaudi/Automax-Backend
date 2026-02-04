package handlers

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

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

// Export exports all classifications as JSON
func (h *ClassificationHandler) Export(c *fiber.Ctx) error {
	classifications, err := h.repo.List(c.Context())
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	// Filter out invalid records (with corrupted paths or invalid UUIDs)
	validClassifications := make([]models.Classification, 0)
	invalidUUID := "00000000-0000-0000-0000-000000000000"

	for _, cls := range classifications {
		// Skip records with invalid paths or IDs
		if cls.ID.String() == invalidUUID ||
		   strings.Contains(cls.Path, invalidUUID) {
			continue
		}
		validClassifications = append(validClassifications, cls)
	}

	// Convert to export format
	exportData := make([]map[string]interface{}, len(validClassifications))
	for i, cls := range validClassifications {
		exportData[i] = map[string]interface{}{
			"id":          cls.ID,
			"name":        cls.Name,
			"description": cls.Description,
			"type":        cls.Type,
			"parent_id":   cls.ParentID,
			"level":       cls.Level,
			"path":        cls.Path,
			"is_active":   cls.IsActive,
			"sort_order":  cls.SortOrder,
		}
	}

	c.Set("Content-Type", "application/json")
	c.Set("Content-Disposition", "attachment; filename=classifications_export.json")
	return c.JSON(exportData)
}

// Import imports classifications from JSON
func (h *ClassificationHandler) Import(c *fiber.Ctx) error {
	file, err := c.FormFile("file")
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "No file uploaded")
	}

	// Open and read file
	fileContent, err := file.Open()
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, "Failed to read file")
	}
	defer fileContent.Close()

	// Read file content
	var importData []struct {
		ID          uuid.UUID  `json:"id"`
		Name        string     `json:"name"`
		Description string     `json:"description"`
		Type        string     `json:"type"`
		ParentID    *uuid.UUID `json:"parent_id"`
		Level       int        `json:"level"`
		Path        string     `json:"path"`
		IsActive    bool       `json:"is_active"`
		SortOrder   int        `json:"sort_order"`
	}

	// Parse JSON from file
	decoder := json.NewDecoder(fileContent)
	if err := decoder.Decode(&importData); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid JSON format: "+err.Error())
	}

	// Sort by level to ensure parents are imported before children
	sort.Slice(importData, func(i, j int) bool {
		return importData[i].Level < importData[j].Level
	})

	// Create a map from old IDs to new IDs for maintaining parent-child relationships
	idMapping := make(map[uuid.UUID]uuid.UUID)
	imported := 0
	skipped := 0
	errors := []string{}

	// Import all classifications in level order
	for _, data := range importData {
		var newParentID *uuid.UUID

		// If has parent, get the new parent ID from mapping
		if data.ParentID != nil {
			mappedParentID, exists := idMapping[*data.ParentID]
			if exists {
				newParentID = &mappedParentID
			} else {
				// Parent not found in import data, import as root node
				newParentID = nil
			}
		}

		// Create new classification (no duplicate check)
		newID := uuid.New()
		classification := &models.Classification{
			ID:          newID,
			Name:        data.Name,
			Description: data.Description,
			Type:        data.Type,
			ParentID:    newParentID,
			IsActive:    data.IsActive,
			SortOrder:   data.SortOrder,
		}

		if err := h.repo.Create(c.Context(), classification); err != nil {
			skipped++
			errors = append(errors, data.Name+" (Level "+fmt.Sprintf("%d", data.Level)+") - "+err.Error())
		} else {
			imported++
			idMapping[data.ID] = newID
		}
	}

	result := map[string]interface{}{
		"imported": imported,
		"skipped":  skipped,
		"errors":   errors,
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Import completed", result)
}
