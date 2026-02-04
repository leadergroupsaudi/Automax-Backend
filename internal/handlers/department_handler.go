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

type DepartmentHandler struct {
	repo      repository.DepartmentRepository
	validator *validator.Validate
}

func NewDepartmentHandler(repo repository.DepartmentRepository) *DepartmentHandler {
	return &DepartmentHandler{
		repo:      repo,
		validator: validator.New(),
	}
}

func (h *DepartmentHandler) Create(c *fiber.Ctx) error {
	var req models.DepartmentCreateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	if err := h.validator.Struct(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}

	department := &models.Department{
		Name:        req.Name,
		Code:        req.Code,
		Description: req.Description,
		ParentID:    req.ParentID,
		ManagerID:   req.ManagerID,
		SortOrder:   req.SortOrder,
		IsActive:    true,
	}

	if err := h.repo.Create(c.Context(), department); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	// Assign locations, classifications, and roles if provided
	if len(req.LocationIDs) > 0 {
		h.repo.AssignLocations(c.Context(), department.ID, req.LocationIDs)
	}
	if len(req.ClassificationIDs) > 0 {
		h.repo.AssignClassifications(c.Context(), department.ID, req.ClassificationIDs)
	}
	if len(req.RoleIDs) > 0 {
		h.repo.AssignRoles(c.Context(), department.ID, req.RoleIDs)
	}

	// Reload with associations
	department, _ = h.repo.FindByID(c.Context(), department.ID)

	return utils.SuccessResponse(c, fiber.StatusCreated, "Department created", models.ToDepartmentResponse(department))
}

func (h *DepartmentHandler) GetByID(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid ID")
	}

	department, err := h.repo.FindByID(c.Context(), id)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "Department not found")
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Department retrieved", models.ToDepartmentResponse(department))
}

func (h *DepartmentHandler) Update(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid ID")
	}

	var req models.DepartmentUpdateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	department, err := h.repo.FindByID(c.Context(), id)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "Department not found")
	}

	if req.Name != "" {
		department.Name = req.Name
	}
	if req.Code != "" {
		department.Code = req.Code
	}
	if req.Description != "" {
		department.Description = req.Description
	}
	if req.ManagerID != nil {
		department.ManagerID = req.ManagerID
	}
	if req.IsActive != nil {
		department.IsActive = *req.IsActive
	}
	if req.SortOrder != nil {
		department.SortOrder = *req.SortOrder
	}

	if err := h.repo.Update(c.Context(), department); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	// Update associations if provided
	if req.LocationIDs != nil {
		h.repo.AssignLocations(c.Context(), department.ID, req.LocationIDs)
	}
	if req.ClassificationIDs != nil {
		h.repo.AssignClassifications(c.Context(), department.ID, req.ClassificationIDs)
	}
	if req.RoleIDs != nil {
		h.repo.AssignRoles(c.Context(), department.ID, req.RoleIDs)
	}

	// Reload with associations
	department, _ = h.repo.FindByID(c.Context(), department.ID)

	return utils.SuccessResponse(c, fiber.StatusOK, "Department updated", models.ToDepartmentResponse(department))
}

func (h *DepartmentHandler) Delete(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid ID")
	}

	if err := h.repo.Delete(c.Context(), id); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Department deleted", nil)
}

func (h *DepartmentHandler) List(c *fiber.Ctx) error {
	departments, err := h.repo.List(c.Context())
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	responses := make([]models.DepartmentResponse, len(departments))
	for i, dept := range departments {
		responses[i] = models.ToDepartmentResponse(&dept)
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Departments retrieved", responses)
}

func (h *DepartmentHandler) GetTree(c *fiber.Ctx) error {
	tree, err := h.repo.GetTree(c.Context())
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	responses := make([]models.DepartmentResponse, len(tree))
	for i, dept := range tree {
		responses[i] = models.ToDepartmentResponse(&dept)
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Department tree retrieved", responses)
}

func (h *DepartmentHandler) GetChildren(c *fiber.Ctx) error {
	parentIDStr := c.Query("parent_id")

	var children []models.Department
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

	responses := make([]models.DepartmentResponse, len(children))
	for i, dept := range children {
		responses[i] = models.ToDepartmentResponse(&dept)
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Children retrieved", responses)
}

// MatchDepartment finds departments that match the given classification and/or location
func (h *DepartmentHandler) MatchDepartment(c *fiber.Ctx) error {
	var req models.DepartmentMatchRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	var classificationID, locationID *uuid.UUID

	if req.ClassificationID != nil && *req.ClassificationID != "" {
		id, err := uuid.Parse(*req.ClassificationID)
		if err != nil {
			return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid classification_id")
		}
		classificationID = &id
	}

	if req.LocationID != nil && *req.LocationID != "" {
		id, err := uuid.Parse(*req.LocationID)
		if err != nil {
			return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid location_id")
		}
		locationID = &id
	}

	departments, err := h.repo.FindMatching(c.Context(), classificationID, locationID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	responses := make([]models.DepartmentResponse, len(departments))
	for i, dept := range departments {
		responses[i] = models.ToDepartmentResponse(&dept)
	}

	// Build match response
	matchResponse := models.DepartmentMatchResponse{
		Departments: responses,
		SingleMatch: len(departments) == 1,
	}

	if len(departments) == 1 {
		idStr := departments[0].ID.String()
		matchResponse.MatchedDepartmentID = &idStr
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Departments matched", matchResponse)
}

// Export exports all departments as JSON
func (h *DepartmentHandler) Export(c *fiber.Ctx) error {
	departments, err := h.repo.List(c.Context())
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	// Filter out invalid records (with corrupted paths or invalid UUIDs)
	validDepartments := make([]models.Department, 0)
	invalidUUID := "00000000-0000-0000-0000-000000000000"

	for _, dept := range departments {
		// Skip records with invalid paths or IDs
		if dept.ID.String() == invalidUUID ||
		   strings.Contains(dept.Path, invalidUUID) {
			continue
		}
		validDepartments = append(validDepartments, dept)
	}

	// Convert to export format
	exportData := make([]map[string]interface{}, len(validDepartments))
	for i, dept := range validDepartments {
		exportData[i] = map[string]interface{}{
			"id":          dept.ID,
			"name":        dept.Name,
			"code":        dept.Code,
			"description": dept.Description,
			"parent_id":   dept.ParentID,
			"level":       dept.Level,
			"path":        dept.Path,
			"manager_id":  dept.ManagerID,
			"is_active":   dept.IsActive,
			"sort_order":  dept.SortOrder,
		}
	}

	c.Set("Content-Type", "application/json")
	c.Set("Content-Disposition", "attachment; filename=departments_export.json")
	return c.JSON(exportData)
}

// Import imports departments from JSON
func (h *DepartmentHandler) Import(c *fiber.Ctx) error {
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
		ParentID    *uuid.UUID `json:"parent_id"`
		Level       int        `json:"level"`
		Path        string     `json:"path"`
		ManagerID   *uuid.UUID `json:"manager_id"`
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

	// Import all departments in level order
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

		// Check if department already exists with same name and parent
		existingDepartment, err := h.repo.FindByNameAndParent(c.Context(), data.Name, newParentID)
		if err == nil && existingDepartment != nil {
			// Department already exists, use existing ID
			skipped++
			errors = append(errors, data.Name+" (Level "+fmt.Sprintf("%d", data.Level)+") - Already exists, skipped")
			idMapping[data.ID] = existingDepartment.ID
			continue
		}

		// Create new department
		newID := uuid.New()
		department := &models.Department{
			ID:          newID,
			Name:        data.Name,
			Code:        data.Code,
			Description: data.Description,
			ParentID:    newParentID,
			ManagerID:   data.ManagerID,
			IsActive:    data.IsActive,
			SortOrder:   data.SortOrder,
		}

		if err := h.repo.Create(c.Context(), department); err != nil {
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
