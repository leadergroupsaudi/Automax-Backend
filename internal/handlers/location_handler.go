package handlers

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/pkg/utils"
	"github.com/go-playground/validator/v10"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type LocationHandler struct {
	repo      repository.LocationRepository
	validator *validator.Validate
}

func NewLocationHandler(repo repository.LocationRepository) *LocationHandler {
	return &LocationHandler{
		repo:      repo,
		validator: validator.New(),
	}
}

func (h *LocationHandler) Create(c *fiber.Ctx) error {
	var req models.LocationCreateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	if err := h.validator.Struct(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}

	location := &models.Location{
		Name:        req.Name,
		Code:        req.Code,
		Description: req.Description,
		Type:        req.Type,
		ParentID:    req.ParentID,
		Address:     req.Address,
		Latitude:    req.Latitude,
		Longitude:   req.Longitude,
		SortOrder:   req.SortOrder,
		IsActive:    true,
	}

	if err := h.repo.Create(c.Context(), location); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusCreated, "Location created", models.ToLocationResponse(location))
}

func (h *LocationHandler) GetByID(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid ID")
	}

	location, err := h.repo.FindByID(c.Context(), id)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "Location not found")
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Location retrieved", models.ToLocationResponse(location))
}

func (h *LocationHandler) Update(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid ID")
	}

	var req models.LocationUpdateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	location, err := h.repo.FindByID(c.Context(), id)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "Location not found")
	}

	if req.Name != "" {
		location.Name = req.Name
	}
	if req.Code != "" {
		location.Code = req.Code
	}
	if req.Description != "" {
		location.Description = req.Description
	}
	if req.Type != "" {
		location.Type = req.Type
	}
	if req.Address != "" {
		location.Address = req.Address
	}
	if req.Latitude != nil {
		location.Latitude = req.Latitude
	}
	if req.Longitude != nil {
		location.Longitude = req.Longitude
	}
	if req.IsActive != nil {
		location.IsActive = *req.IsActive
	}
	if req.SortOrder != nil {
		location.SortOrder = *req.SortOrder
	}

	if err := h.repo.Update(c.Context(), location); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Location updated", models.ToLocationResponse(location))
}

func (h *LocationHandler) Delete(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid ID")
	}

	if err := h.repo.Delete(c.Context(), id); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Location deleted", nil)
}

func (h *LocationHandler) List(c *fiber.Ctx) error {
	locations, err := h.repo.List(c.Context())
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	responses := make([]models.LocationResponse, len(locations))
	for i, loc := range locations {
		responses[i] = models.ToLocationResponse(&loc)
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Locations retrieved", responses)
}

func (h *LocationHandler) GetTree(c *fiber.Ctx) error {
	tree, err := h.repo.GetTree(c.Context())
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	responses := make([]models.LocationResponse, len(tree))
	for i, loc := range tree {
		responses[i] = models.ToLocationResponse(&loc)
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Location tree retrieved", responses)
}

func (h *LocationHandler) GetChildren(c *fiber.Ctx) error {
	parentIDStr := c.Query("parent_id")

	var children []models.Location
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

	responses := make([]models.LocationResponse, len(children))
	for i, loc := range children {
		responses[i] = models.ToLocationResponse(&loc)
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Children retrieved", responses)
}

func (h *LocationHandler) GetByType(c *fiber.Ctx) error {
	locationType := c.Query("type")
	if locationType == "" {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Type is required")
	}

	locations, err := h.repo.GetByType(c.Context(), locationType)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	responses := make([]models.LocationResponse, len(locations))
	for i, loc := range locations {
		responses[i] = models.ToLocationResponse(&loc)
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Locations retrieved", responses)
}

// Export exports all locations as JSON
func (h *LocationHandler) Export(c *fiber.Ctx) error {
	locations, err := h.repo.List(c.Context())
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	// Convert to export format - NO FILTERING to match classification behavior
	exportData := make([]map[string]interface{}, len(locations))
	for i, loc := range locations {
		exportData[i] = map[string]interface{}{
			"id":          loc.ID,
			"name":        loc.Name,
			"code":        loc.Code,
			"description": loc.Description,
			"type":        loc.Type,
			"parent_id":   loc.ParentID,
			"level":       loc.Level,
			"path":        loc.Path,
			"address":     loc.Address,
			"latitude":    loc.Latitude,
			"longitude":   loc.Longitude,
			"is_active":   loc.IsActive,
			"sort_order":  loc.SortOrder,
		}
	}

	c.Set("Content-Type", "application/json")
	c.Set("Content-Disposition", "attachment; filename=locations_export.json")
	return c.JSON(exportData)
}

// Import imports locations from JSON
func (h *LocationHandler) Import(c *fiber.Ctx) error {
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
		Code        string     `json:"code"`
		Description string     `json:"description"`
		Type        string     `json:"type"`
		ParentID    *uuid.UUID `json:"parent_id"`
		Level       int        `json:"level"`
		Path        string     `json:"path"`
		Address     string     `json:"address"`
		Latitude    *float64   `json:"latitude"`
		Longitude   *float64   `json:"longitude"`
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

	// Import all locations in level order
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

		// Create new location (no duplicate check)
		newID := uuid.New()
		location := &models.Location{
			ID:          newID,
			Name:        data.Name,
			Code:        data.Code,
			Description: data.Description,
			Type:        data.Type,
			ParentID:    newParentID,
			Address:     data.Address,
			Latitude:    data.Latitude,
			Longitude:   data.Longitude,
			IsActive:    data.IsActive,
			SortOrder:   data.SortOrder,
		}

		if err := h.repo.Create(c.Context(), location); err != nil {
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
