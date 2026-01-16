package handlers

import (
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
