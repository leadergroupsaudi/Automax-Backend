package handlers

import (
	"encoding/json"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/pkg/utils"
	"github.com/go-playground/validator/v10"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type RoleHandler struct {
	roleRepo       repository.RoleRepository
	permissionRepo repository.PermissionRepository
	validator      *validator.Validate
}

func NewRoleHandler(roleRepo repository.RoleRepository, permissionRepo repository.PermissionRepository) *RoleHandler {
	return &RoleHandler{
		roleRepo:       roleRepo,
		permissionRepo: permissionRepo,
		validator:      validator.New(),
	}
}

// Role endpoints

func (h *RoleHandler) CreateRole(c *fiber.Ctx) error {
	var req models.RoleCreateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	if err := h.validator.Struct(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}

	role := &models.Role{
		Name:        req.Name,
		Code:        req.Code,
		Description: req.Description,
		IsActive:    true,
		IsSystem:    false,
	}

	if err := h.roleRepo.Create(c.Context(), role); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	// Assign permissions if provided
	if len(req.PermissionIDs) > 0 {
		h.roleRepo.AssignPermissions(c.Context(), role.ID, req.PermissionIDs)
	}

	// Reload with permissions
	role, _ = h.roleRepo.FindByID(c.Context(), role.ID)

	return utils.SuccessResponse(c, fiber.StatusCreated, "Role created", models.ToRoleResponse(role))
}

func (h *RoleHandler) GetRole(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid ID")
	}

	role, err := h.roleRepo.FindByID(c.Context(), id)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "Role not found")
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Role retrieved", models.ToRoleResponse(role))
}

func (h *RoleHandler) UpdateRole(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid ID")
	}

	var req models.RoleUpdateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	role, err := h.roleRepo.FindByID(c.Context(), id)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "Role not found")
	}

	if req.Name != "" {
		role.Name = req.Name
	}
	if req.Description != "" {
		role.Description = req.Description
	}
	if req.IsActive != nil {
		role.IsActive = *req.IsActive
	}

	if err := h.roleRepo.Update(c.Context(), role); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	// Update permissions if provided
	if req.PermissionIDs != nil {
		h.roleRepo.AssignPermissions(c.Context(), role.ID, req.PermissionIDs)
	}

	// Reload with permissions
	role, _ = h.roleRepo.FindByID(c.Context(), role.ID)

	return utils.SuccessResponse(c, fiber.StatusOK, "Role updated", models.ToRoleResponse(role))
}

func (h *RoleHandler) DeleteRole(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid ID")
	}

	role, err := h.roleRepo.FindByID(c.Context(), id)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "Role not found")
	}

	if role.IsSystem {
		return utils.ErrorResponse(c, fiber.StatusForbidden, "Cannot delete system role")
	}

	if err := h.roleRepo.Delete(c.Context(), id); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Role deleted", nil)
}

func (h *RoleHandler) ListRoles(c *fiber.Ctx) error {
	roles, err := h.roleRepo.List(c.Context())
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	responses := make([]models.RoleResponse, len(roles))
	for i, role := range roles {
		responses[i] = models.ToRoleResponse(&role)
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Roles retrieved", responses)
}

func (h *RoleHandler) AssignPermissions(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid ID")
	}

	var req struct {
		PermissionIDs []uuid.UUID `json:"permission_ids"`
	}
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	if err := h.roleRepo.AssignPermissions(c.Context(), id, req.PermissionIDs); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	role, _ := h.roleRepo.FindByID(c.Context(), id)
	return utils.SuccessResponse(c, fiber.StatusOK, "Permissions assigned", models.ToRoleResponse(role))
}

// Permission endpoints

func (h *RoleHandler) CreatePermission(c *fiber.Ctx) error {
	var req models.PermissionCreateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	if err := h.validator.Struct(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}

	permission := &models.Permission{
		Name:        req.Name,
		Code:        req.Code,
		Description: req.Description,
		Module:      req.Module,
		Action:      req.Action,
		IsActive:    true,
	}

	if err := h.permissionRepo.Create(c.Context(), permission); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusCreated, "Permission created", models.ToPermissionResponse(permission))
}

func (h *RoleHandler) GetPermission(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid ID")
	}

	permission, err := h.permissionRepo.FindByID(c.Context(), id)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "Permission not found")
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Permission retrieved", models.ToPermissionResponse(permission))
}

func (h *RoleHandler) UpdatePermission(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid ID")
	}

	var req models.PermissionUpdateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	permission, err := h.permissionRepo.FindByID(c.Context(), id)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "Permission not found")
	}

	if req.Name != "" {
		permission.Name = req.Name
	}
	if req.Description != "" {
		permission.Description = req.Description
	}
	if req.IsActive != nil {
		permission.IsActive = *req.IsActive
	}

	if err := h.permissionRepo.Update(c.Context(), permission); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Permission updated", models.ToPermissionResponse(permission))
}

func (h *RoleHandler) DeletePermission(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid ID")
	}

	if err := h.permissionRepo.Delete(c.Context(), id); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Permission deleted", nil)
}

func (h *RoleHandler) ListPermissions(c *fiber.Ctx) error {
	module := c.Query("module")

	var permissions []models.Permission
	var err error

	if module != "" {
		permissions, err = h.permissionRepo.ListByModule(c.Context(), module)
	} else {
		permissions, err = h.permissionRepo.List(c.Context())
	}

	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	responses := make([]models.PermissionResponse, len(permissions))
	for i, perm := range permissions {
		responses[i] = models.ToPermissionResponse(&perm)
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Permissions retrieved", responses)
}

func (h *RoleHandler) GetModules(c *fiber.Ctx) error {
	modules, err := h.permissionRepo.GetModules(c.Context())
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Modules retrieved", modules)
}

// Export roles to JSON
func (h *RoleHandler) Export(c *fiber.Ctx) error {
	roles, err := h.roleRepo.List(c.Context())
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	type ExportRole struct {
		ID            uuid.UUID   `json:"id"`
		Name          string      `json:"name"`
		Code          string      `json:"code"`
		Description   string      `json:"description"`
		IsSystem      bool        `json:"is_system"`
		IsActive      bool        `json:"is_active"`
		PermissionIDs []uuid.UUID `json:"permission_ids"`
	}

	exportData := make([]ExportRole, len(roles))
	for i, role := range roles {
		permissionIDs := make([]uuid.UUID, len(role.Permissions))
		for j, perm := range role.Permissions {
			permissionIDs[j] = perm.ID
		}

		exportData[i] = ExportRole{
			ID:            role.ID,
			Name:          role.Name,
			Code:          role.Code,
			Description:   role.Description,
			IsSystem:      role.IsSystem,
			IsActive:      role.IsActive,
			PermissionIDs: permissionIDs,
		}
	}

	jsonData, err := json.MarshalIndent(exportData, "", "  ")
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, "Failed to create export file")
	}

	c.Set("Content-Type", "application/json")
	c.Set("Content-Disposition", "attachment; filename=roles_export.json")
	return c.Send(jsonData)
}

// Import roles from JSON
func (h *RoleHandler) Import(c *fiber.Ctx) error {
	file, err := c.FormFile("file")
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "No file provided")
	}

	fileContent, err := file.Open()
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, "Failed to open file")
	}
	defer fileContent.Close()

	type ImportRole struct {
		ID            uuid.UUID   `json:"id"`
		Name          string      `json:"name"`
		Code          string      `json:"code"`
		Description   string      `json:"description"`
		IsSystem      bool        `json:"is_system"`
		IsActive      bool        `json:"is_active"`
		PermissionIDs []uuid.UUID `json:"permission_ids"`
	}

	var importData []ImportRole
	if err := json.NewDecoder(fileContent).Decode(&importData); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid JSON format")
	}

	imported := 0
	skipped := 0
	var errors []string

	for _, data := range importData {
		// Check if role with same code already exists
		existingRole, err := h.roleRepo.FindByCode(c.Context(), data.Code)
		if err == nil && existingRole != nil {
			skipped++
			errors = append(errors, data.Name+" - Role with code "+data.Code+" already exists, skipped")
			continue
		}

		// Create new role
		role := &models.Role{
			Name:        data.Name,
			Code:        data.Code,
			Description: data.Description,
			IsActive:    data.IsActive,
			IsSystem:    false, // Always set imported roles as non-system
		}

		if err := h.roleRepo.Create(c.Context(), role); err != nil {
			errors = append(errors, data.Name+" - Failed to create: "+err.Error())
			continue
		}

		// Assign permissions if provided
		if len(data.PermissionIDs) > 0 {
			if err := h.roleRepo.AssignPermissions(c.Context(), role.ID, data.PermissionIDs); err != nil {
				errors = append(errors, data.Name+" - Role created but failed to assign permissions: "+err.Error())
			}
		}

		imported++
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Import completed", map[string]interface{}{
		"imported": imported,
		"skipped":  skipped,
		"errors":   errors,
	})
}
