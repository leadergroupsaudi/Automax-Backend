package handlers

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/services"
	"github.com/automax/backend/internal/storage"
	"github.com/automax/backend/pkg/utils"
	"github.com/go-playground/validator/v10"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type UserHandler struct {
	userService services.UserService
	storage     *storage.MinIOStorage
	validator   *validator.Validate
}

func NewUserHandler(userService services.UserService, storage *storage.MinIOStorage) *UserHandler {
	return &UserHandler{
		userService: userService,
		storage:     storage,
		validator:   validator.New(),
	}
}

func (h *UserHandler) Register(c *fiber.Ctx) error {
	var req models.UserRegisterRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	if err := h.validator.Struct(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}

	response, err := h.userService.Register(c.Context(), &req)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusCreated, "User registered successfully", response)
}

func (h *UserHandler) Login(c *fiber.Ctx) error {
	var req models.UserLoginRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	if err := h.validator.Struct(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}

	response, err := h.userService.Login(c.Context(), &req)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusUnauthorized, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Login successful", response)
}

func (h *UserHandler) Logout(c *fiber.Ctx) error {
	token := c.Locals("token").(string)

	if err := h.userService.Logout(c.Context(), token); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, "Failed to logout")
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Logout successful", nil)
}

func (h *UserHandler) RefreshToken(c *fiber.Ctx) error {
	var req models.RefreshTokenRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	if err := h.validator.Struct(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}

	response, err := h.userService.RefreshToken(c.Context(), req.RefreshToken)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusUnauthorized, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Token refreshed successfully", response)
}

func (h *UserHandler) GetProfile(c *fiber.Ctx) error {
	userID := c.Locals("user_id").(uuid.UUID)

	response, err := h.userService.GetProfile(c.Context(), userID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "User not found")
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Profile retrieved successfully", response)
}

func (h *UserHandler) UpdateProfile(c *fiber.Ctx) error {
	userID := c.Locals("user_id").(uuid.UUID)

	var req models.UserUpdateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	if err := h.validator.Struct(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}

	response, err := h.userService.UpdateProfile(c.Context(), userID, &req)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Profile updated successfully", response)
}

func (h *UserHandler) ChangePassword(c *fiber.Ctx) error {
	userID := c.Locals("user_id").(uuid.UUID)

	var req models.ChangePasswordRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	if err := h.validator.Struct(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}

	if err := h.userService.ChangePassword(c.Context(), userID, &req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Password changed successfully", nil)
}

func (h *UserHandler) UploadAvatar(c *fiber.Ctx) error {
	userID := c.Locals("user_id").(uuid.UUID)

	file, err := c.FormFile("avatar")
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "No file uploaded")
	}

	if file.Size > 5*1024*1024 {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "File size exceeds 5MB limit")
	}

	contentType := file.Header.Get("Content-Type")
	if contentType != "image/jpeg" && contentType != "image/png" && contentType != "image/gif" && contentType != "image/webp" {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid file type. Only JPEG, PNG, GIF, and WebP are allowed")
	}

	src, err := file.Open()
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, "Failed to open file")
	}
	defer src.Close()

	response, err := h.userService.UploadAvatar(c.Context(), userID, src, file)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, "Failed to upload avatar")
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Avatar uploaded successfully", response)
}

func (h *UserHandler) DeleteAccount(c *fiber.Ctx) error {
	userID := c.Locals("user_id").(uuid.UUID)

	if err := h.userService.DeleteUser(c.Context(), userID); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, "Failed to delete account")
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Account deleted successfully", nil)
}

func (h *UserHandler) ListUsers(c *fiber.Ctx) error {
	page, _ := strconv.Atoi(c.Query("page", "1"))
	limit, _ := strconv.Atoi(c.Query("limit", "10"))

	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 10
	}

	users, total, err := h.userService.ListUsers(c.Context(), page, limit)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, "Failed to fetch users")
	}

	return utils.PaginatedSuccessResponse(c, users, page, limit, total)
}

func (h *UserHandler) GetUser(c *fiber.Ctx) error {
	userIDStr := c.Params("id")
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid user ID")
	}

	response, err := h.userService.GetUserByID(c.Context(), userID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "User not found")
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "User retrieved successfully", response)
}

func (h *UserHandler) AdminCreateUser(c *fiber.Ctx) error {
	var req models.UserRegisterRequest

	contentType := string(c.Request().Header.ContentType())

	// Check if it's multipart form data (with file attachment)
	if strings.Contains(contentType, "multipart/form-data") {
		// Parse form fields manually for multipart
		req.Email = c.FormValue("email")
		req.Username = c.FormValue("username")
		req.Password = c.FormValue("password")
		req.FirstName = c.FormValue("first_name")
		req.LastName = c.FormValue("last_name")
		req.Phone = c.FormValue("phone")

		// Parse optional UUID fields
		if deptID := c.FormValue("department_id"); deptID != "" {
			if id, err := uuid.Parse(deptID); err == nil {
				req.DepartmentID = &id
			}
		}
		if locID := c.FormValue("location_id"); locID != "" {
			if id, err := uuid.Parse(locID); err == nil {
				req.LocationID = &id
			}
		}

		// Parse array fields (JSON format in form)
		if roleIDsStr := c.FormValue("role_ids"); roleIDsStr != "" {
			var roleIDStrings []string
			if err := json.Unmarshal([]byte(roleIDsStr), &roleIDStrings); err == nil {
				for _, idStr := range roleIDStrings {
					if id, err := uuid.Parse(idStr); err == nil {
						req.RoleIDs = append(req.RoleIDs, id)
					}
				}
			}
		}
		if deptIDsStr := c.FormValue("department_ids"); deptIDsStr != "" {
			var deptIDStrings []string
			if err := json.Unmarshal([]byte(deptIDsStr), &deptIDStrings); err == nil {
				for _, idStr := range deptIDStrings {
					if id, err := uuid.Parse(idStr); err == nil {
						req.DepartmentIDs = append(req.DepartmentIDs, id)
					}
				}
			}
		}
		if locIDsStr := c.FormValue("location_ids"); locIDsStr != "" {
			var locIDStrings []string
			if err := json.Unmarshal([]byte(locIDsStr), &locIDStrings); err == nil {
				for _, idStr := range locIDStrings {
					if id, err := uuid.Parse(idStr); err == nil {
						req.LocationIDs = append(req.LocationIDs, id)
					}
				}
			}
		}
		if classIDsStr := c.FormValue("classification_ids"); classIDsStr != "" {
			var classIDStrings []string
			if err := json.Unmarshal([]byte(classIDsStr), &classIDStrings); err == nil {
				for _, idStr := range classIDStrings {
					if id, err := uuid.Parse(idStr); err == nil {
						req.ClassificationIDs = append(req.ClassificationIDs, id)
					}
				}
			}
		}

	} else {
		// Regular JSON body
		if err := c.BodyParser(&req); err != nil {
			return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
		}
	}

	if err := h.validator.Struct(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}

	response, err := h.userService.Register(c.Context(), &req)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}

	// Handle avatar upload if provided (multipart form data)
	if strings.Contains(contentType, "multipart/form-data") {
		file, err := c.FormFile("avatar")
		if err == nil && file != nil {
			// Open the file
			src, err := file.Open()
			if err == nil {
				defer src.Close()

				// Upload to storage
				folder := fmt.Sprintf("avatars/%s", response.User.ID)
				filePath, err := h.storage.UploadFile(c.Context(), src, file, folder)
				if err == nil {
					// Update user's avatar URL
					avatarURL := filePath
					if updateErr := h.userService.UpdateAvatar(c.Context(), response.User.ID, avatarURL); updateErr == nil {
						response.User.Avatar = avatarURL
					}
				}
			}
		}
	}

	return utils.SuccessResponse(c, fiber.StatusCreated, "User created successfully", response)
}

func (h *UserHandler) AdminUpdateUser(c *fiber.Ctx) error {
	userIDStr := c.Params("id")
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid user ID")
	}

	var req models.UserUpdateRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	if err := h.validator.Struct(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}

	response, err := h.userService.UpdateProfile(c.Context(), userID, &req)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "User updated successfully", response)
}

// MatchUsers finds users that match the given criteria (role, classification, location, department)
func (h *UserHandler) MatchUsers(c *fiber.Ctx) error {
	var req models.UserMatchRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	var roleID, classificationID, locationID, departmentID, excludeUserID *uuid.UUID

	if req.RoleID != nil && *req.RoleID != "" {
		id, err := uuid.Parse(*req.RoleID)
		if err != nil {
			return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid role_id")
		}
		roleID = &id
	}

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

	if req.DepartmentID != nil && *req.DepartmentID != "" {
		id, err := uuid.Parse(*req.DepartmentID)
		if err != nil {
			return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid department_id")
		}
		departmentID = &id
	}

	if req.ExcludeUserID != nil && *req.ExcludeUserID != "" {
		id, err := uuid.Parse(*req.ExcludeUserID)
		if err != nil {
			return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid exclude_user_id")
		}
		excludeUserID = &id
	}

	users, err := h.userService.FindMatchingUsers(c.Context(), roleID, classificationID, locationID, departmentID, excludeUserID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	// Build match response
	matchResponse := models.UserMatchResponse{
		Users:       users,
		SingleMatch: len(users) == 1,
	}

	if len(users) == 1 {
		idStr := users[0].ID.String()
		matchResponse.MatchedUserID = &idStr
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Users matched", matchResponse)
}

// Export exports all users as JSON
func (h *UserHandler) Export(c *fiber.Ctx) error {
	// Get all users without pagination
	users, _, err := h.userService.ListUsers(c.Context(), 1, 10000)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	// Convert to export format
	exportData := make([]map[string]interface{}, len(users))
	for i, user := range users {
		// Extract role IDs
		roleIDs := make([]string, len(user.Roles))
		for j, role := range user.Roles {
			roleIDs[j] = role.ID.String()
		}

		// Extract department IDs
		departmentIDs := make([]string, len(user.Departments))
		for j, dept := range user.Departments {
			departmentIDs[j] = dept.ID.String()
		}

		// Extract location IDs
		locationIDs := make([]string, len(user.Locations))
		for j, loc := range user.Locations {
			locationIDs[j] = loc.ID.String()
		}

		// Extract classification IDs
		classificationIDs := make([]string, len(user.Classifications))
		for j, cls := range user.Classifications {
			classificationIDs[j] = cls.ID.String()
		}

		exportData[i] = map[string]interface{}{
			"id":                  user.ID,
			"username":            user.Username,
			"email":               user.Email,
			"first_name":          user.FirstName,
			"last_name":           user.LastName,
			"phone":               user.Phone,
			"department_id":       user.DepartmentID,
			"location_id":         user.LocationID,
			"role_ids":            roleIDs,
			"department_ids":      departmentIDs,
			"location_ids":        locationIDs,
			"classification_ids":  classificationIDs,
			"is_active":           user.IsActive,
			"is_super_admin":      user.IsSuperAdmin,
		}
	}

	c.Set("Content-Type", "application/json")
	c.Set("Content-Disposition", "attachment; filename=users_export.json")
	return c.JSON(exportData)
}

// Import imports users from JSON
func (h *UserHandler) Import(c *fiber.Ctx) error {
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
		ID                uuid.UUID   `json:"id"`
		Username          string      `json:"username"`
		Email             string      `json:"email"`
		FirstName         string      `json:"first_name"`
		LastName          string      `json:"last_name"`
		Phone             string      `json:"phone"`
		DepartmentID      *uuid.UUID  `json:"department_id"`
		LocationID        *uuid.UUID  `json:"location_id"`
		RoleIDs           []string    `json:"role_ids"`
		DepartmentIDs     []string    `json:"department_ids"`
		LocationIDs       []string    `json:"location_ids"`
		ClassificationIDs []string    `json:"classification_ids"`
		IsActive          bool        `json:"is_active"`
		IsSuperAdmin      bool        `json:"is_super_admin"`
	}

	// Parse JSON from file
	decoder := json.NewDecoder(fileContent)
	if err := decoder.Decode(&importData); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid JSON format: "+err.Error())
	}

	imported := 0
	skipped := 0
	errors := []string{}

	// Import all users
	for _, data := range importData {
		// Check if user already exists by email or username
		existingUser, _ := h.userService.GetUserByEmail(c.Context(), data.Email)
		if existingUser != nil {
			skipped++
			errors = append(errors, data.Email+" - User already exists with this email, skipped")
			continue
		}

		existingUser, _ = h.userService.GetUserByUsername(c.Context(), data.Username)
		if existingUser != nil {
			skipped++
			errors = append(errors, data.Username+" - User already exists with this username, skipped")
			continue
		}

		// Parse role IDs
		roleIDs := make([]uuid.UUID, 0)
		for _, roleIDStr := range data.RoleIDs {
			if roleID, err := uuid.Parse(roleIDStr); err == nil {
				roleIDs = append(roleIDs, roleID)
			}
		}

		// Parse department IDs
		departmentIDs := make([]uuid.UUID, 0)
		for _, deptIDStr := range data.DepartmentIDs {
			if deptID, err := uuid.Parse(deptIDStr); err == nil {
				departmentIDs = append(departmentIDs, deptID)
			}
		}

		// Parse location IDs
		locationIDs := make([]uuid.UUID, 0)
		for _, locIDStr := range data.LocationIDs {
			if locID, err := uuid.Parse(locIDStr); err == nil {
				locationIDs = append(locationIDs, locID)
			}
		}

		// Parse classification IDs
		classificationIDs := make([]uuid.UUID, 0)
		for _, clsIDStr := range data.ClassificationIDs {
			if clsID, err := uuid.Parse(clsIDStr); err == nil {
				classificationIDs = append(classificationIDs, clsID)
			}
		}

		// Create user registration request
		req := &models.UserRegisterRequest{
			Username:          data.Username,
			Email:             data.Email,
			Password:          "ChangeMe123!",  // Default password, user should change
			FirstName:         data.FirstName,
			LastName:          data.LastName,
			Phone:             data.Phone,
			DepartmentID:      data.DepartmentID,
			LocationID:        data.LocationID,
			RoleIDs:           roleIDs,
			DepartmentIDs:     departmentIDs,
			LocationIDs:       locationIDs,
			ClassificationIDs: classificationIDs,
		}

		// Register user
		_, err := h.userService.Register(c.Context(), req)
		if err != nil {
			skipped++
			errors = append(errors, data.Email+" - "+err.Error())
		} else {
			imported++
		}
	}

	result := map[string]interface{}{
		"imported": imported,
		"skipped":  skipped,
		"errors":   errors,
		"note":     "Imported users have default password: ChangeMe123! - Please ask users to change it",
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Import completed", result)
}
