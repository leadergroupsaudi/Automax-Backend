package handlers

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/pkg/utils"
	"github.com/automax/backend/pkg/validation"
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
	var req models.ClassificationCreateRequestWithCriticalities
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	if validationErrors := validation.ValidateStruct(c.UserContext(), &req); len(validationErrors) != 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"errors":  validationErrors,
		})
	}

	classTypes := models.ResolveClassificationTypes(req.Type, req.Types)

	classification := &models.Classification{
		Name:        req.Name,
		Description: req.Description,
		Types:       classTypes,
		ParentID:    req.ParentID,
		SortOrder:   req.SortOrder,
		IsActive:    true,
	}

	if err := h.repo.Create(c.UserContext(), classification); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	// Create criticalities if provided
	if len(req.Criticalities) > 0 {
		for _, critReq := range req.Criticalities {
			criticalityID, err := uuid.Parse(critReq.CriticalityID)
			if err != nil {
				// Rollback: delete the classification if criticality creation fails
				h.repo.Delete(c.UserContext(), classification.ID)
				return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid criticality ID: "+critReq.CriticalityID)
			}

			criticality := &models.ClassificationCriticality{
				ClassificationID:  classification.ID,
				CriticalityID:     criticalityID,
				MaxClosingHours:   critReq.MaxClosingHours,
				MaxClosingMinutes: critReq.MaxClosingMinutes,
				IsActive:          true,
			}

			if err := h.repo.CreateCriticality(c.UserContext(), criticality); err != nil {
				// Rollback: delete the classification if criticality creation fails
				h.repo.Delete(c.UserContext(), classification.ID)
				return utils.ErrorResponse(c, fiber.StatusInternalServerError, "Failed to create criticality: "+err.Error())
			}
		}
	}

	return utils.SuccessResponse(c, fiber.StatusCreated, "Classification created", models.ToClassificationResponse(classification))
}

func (h *ClassificationHandler) GetByID(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid ID")
	}

	classification, err := h.repo.FindByID(c.UserContext(), id)
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

	var req models.ClassificationCreateRequestWithCriticalities
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	classification, err := h.repo.FindByID(c.UserContext(), id)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "Classification not found")
	}

	if req.Name != "" {
		classification.Name = req.Name
	}
	if req.Description != "" {
		classification.Description = req.Description
	}
	if len(req.Types) > 0 {
		classification.Types = req.Types
	}
	classification.IsActive = true // Keep active on update
	if req.SortOrder >= 0 {
		classification.SortOrder = req.SortOrder
	}

	// Update classification
	if err := h.repo.Update(c.UserContext(), classification); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	// Update criticalities if provided
	if len(req.Criticalities) > 0 {
		// Get existing criticalities
		existingCriticalities, err := h.repo.GetCriticalitiesByClassificationID(c.UserContext(), id)
		if err != nil {
			return utils.ErrorResponse(c, fiber.StatusInternalServerError, "Failed to get existing criticalities")
		}

		// Build map of existing criticalities by criticality_id
		existingMap := make(map[uuid.UUID]*models.ClassificationCriticality)
		for i := range existingCriticalities {
			existingMap[existingCriticalities[i].CriticalityID] = &existingCriticalities[i]
		}

		// Process incoming criticalities
		for _, critReq := range req.Criticalities {
			criticalityID, err := uuid.Parse(critReq.CriticalityID)
			if err != nil {
				return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid criticality ID: "+critReq.CriticalityID)
			}

			if existing, ok := existingMap[criticalityID]; ok {
				// Update existing criticality
				existing.MaxClosingHours = critReq.MaxClosingHours
				existing.MaxClosingMinutes = critReq.MaxClosingMinutes
				if err := h.repo.UpdateCriticality(c.UserContext(), existing); err != nil {
					return utils.ErrorResponse(c, fiber.StatusInternalServerError, "Failed to update criticality: "+err.Error())
				}
				delete(existingMap, criticalityID)
			} else {
				// Create new criticality
				criticality := &models.ClassificationCriticality{
					ClassificationID:  id,
					CriticalityID:     criticalityID,
					MaxClosingHours:   critReq.MaxClosingHours,
					MaxClosingMinutes: critReq.MaxClosingMinutes,
					IsActive:          true,
				}
				if err := h.repo.CreateCriticality(c.UserContext(), criticality); err != nil {
					return utils.ErrorResponse(c, fiber.StatusInternalServerError, "Failed to create criticality: "+err.Error())
				}
			}
		}

		// Delete criticalities that are no longer in the request (optional - keep existing if not sent)
		// For now, we'll keep them - can be changed based on requirements
	}

	// Reload classification with criticalities
	updatedClassification, err := h.repo.FindByID(c.UserContext(), id)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, "Failed to reload classification")
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Classification updated", models.ToClassificationResponse(updatedClassification))
}

func (h *ClassificationHandler) Delete(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid ID")
	}

	if err := h.repo.Delete(c.UserContext(), id); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Classification deleted", nil)
}

func (h *ClassificationHandler) List(c *fiber.Ctx) error {
	var classifications []models.Classification
	var err error

	classType := c.Query("type")
	if classType != "" {
		classifications, err = h.repo.ListByType(c.UserContext(), classType)
	} else {
		classifications, err = h.repo.List(c.UserContext())
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
		tree, err = h.repo.GetTreeByType(c.UserContext(), classType)
	} else {
		tree, err = h.repo.GetTree(c.UserContext())
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
		children, err = h.repo.GetByParentID(c.UserContext(), nil)
	} else {
		parentID, parseErr := uuid.Parse(parentIDStr)
		if parseErr != nil {
			return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid parent ID")
		}
		children, err = h.repo.GetByParentID(c.UserContext(), &parentID)
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
	classifications, err := h.repo.List(c.UserContext())
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
			"types":       cls.Types,
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
		ID          uuid.UUID                  `json:"id"`
		Name        string                     `json:"name"`
		Description string                     `json:"description"`
		Type        string                     `json:"type"`  // legacy field — accepted for backward compat with old export files
		Types       models.ClassificationTypes `json:"types"` // preferred field
		ParentID    *uuid.UUID                 `json:"parent_id"`
		Level       int                        `json:"level"`
		Path        string                     `json:"path"`
		IsActive    bool                       `json:"is_active"`
		SortOrder   int                        `json:"sort_order"`
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

		// Create new classification (no duplicate check).
		// ResolveClassificationTypes handles backward compat: old export files may only have Type string.
		classTypes := models.ResolveClassificationTypes(data.Type, data.Types)
		newID := uuid.New()
		classification := &models.Classification{
			ID:          newID,
			Name:        data.Name,
			Description: data.Description,
			Types:       classTypes,
			ParentID:    newParentID,
			IsActive:    data.IsActive,
			SortOrder:   data.SortOrder,
		}

		if err := h.repo.Create(c.UserContext(), classification); err != nil {
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

// GetTreeWithStats returns classification tree with incident counts
func (h *ClassificationHandler) GetTreeWithStats(c *fiber.Ctx) error {
	recordType := c.Query("type", "")

	tree, err := h.repo.GetTreeWithStats(c.UserContext(), recordType)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Classification tree with stats retrieved", tree)
}

// GetCriticalities returns all criticalities for a classification
func (h *ClassificationHandler) GetCriticalities(c *fiber.Ctx) error {
	classificationIDStr := c.Params("classification_id")
	classificationID, err := uuid.Parse(classificationIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid classification ID")
	}

	criticalities, err := h.repo.GetCriticalitiesByClassificationID(c.UserContext(), classificationID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	responses := make([]models.ClassificationCriticalityResponse, len(criticalities))
	for i, crit := range criticalities {
		responses[i] = models.ToClassificationCriticalityResponse(&crit)
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Classification criticalities retrieved", responses)
}

// CreateCriticality creates a new criticality setting for a classification
func (h *ClassificationHandler) CreateCriticality(c *fiber.Ctx) error {
	classificationIDStr := c.Params("classification_id")
	classificationID, err := uuid.Parse(classificationIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid classification ID")
	}

	var req models.ClassificationCriticalityCreateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	if validationErrors := validation.ValidateStruct(c.UserContext(), &req); len(validationErrors) != 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"errors":  validationErrors,
		})
	}

	// Check if classification exists
	_, err = h.repo.FindByID(c.UserContext(), classificationID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "Classification not found")
	}

	criticalityID, err := uuid.Parse(req.CriticalityID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid criticality ID")
	}

	// Check if criticality already exists for this classification
	_, err = h.repo.GetCriticalityByClassificationAndCriticalityID(c.UserContext(), classificationID, criticalityID)
	if err == nil {
		return utils.ErrorResponse(c, fiber.StatusConflict, "Criticality already exists for this classification")
	}

	criticality := &models.ClassificationCriticality{
		ClassificationID:  classificationID,
		CriticalityID:     criticalityID,
		MaxClosingHours:   req.MaxClosingHours,
		MaxClosingMinutes: req.MaxClosingMinutes,
		IsActive:          true,
	}

	if err := h.repo.CreateCriticality(c.UserContext(), criticality); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, "Failed to create criticality: "+err.Error())
	}

	// Reload with criticality details
	created, err := h.repo.GetCriticalityByID(c.UserContext(), criticality.ID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, "Failed to retrieve created criticality")
	}

	return utils.SuccessResponse(c, fiber.StatusCreated, "Classification criticality created", models.ToClassificationCriticalityResponse(created))
}

// UpdateCriticality updates a criticality setting for a classification
func (h *ClassificationHandler) UpdateCriticality(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid ID")
	}

	var req models.ClassificationCriticalityUpdateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	// Validate request
	if validationErrors := validation.ValidateStruct(c.UserContext(), &req); len(validationErrors) != 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"errors":  validationErrors,
		})
	}

	criticality, err := h.repo.GetCriticalityByID(c.UserContext(), id)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "Classification criticality not found")
	}

	if req.MaxClosingHours != nil {
		criticality.MaxClosingHours = *req.MaxClosingHours
	}
	if req.MaxClosingMinutes != nil {
		criticality.MaxClosingMinutes = *req.MaxClosingMinutes
	}
	if req.IsActive != nil {
		criticality.IsActive = *req.IsActive
	}

	if err := h.repo.UpdateCriticality(c.UserContext(), criticality); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Classification criticality updated", models.ToClassificationCriticalityResponse(criticality))
}

// DeleteCriticality deletes a criticality setting for a classification
func (h *ClassificationHandler) DeleteCriticality(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid ID")
	}

	if err := h.repo.DeleteCriticality(c.UserContext(), id); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Classification criticality deleted", nil)
}

// GetCriticalityByID returns a single criticality setting by ID
func (h *ClassificationHandler) GetCriticalityByID(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid ID")
	}

	criticality, err := h.repo.GetCriticalityByID(c.UserContext(), id)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "Classification criticality not found")
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Classification criticality retrieved", models.ToClassificationCriticalityResponse(criticality))
}
