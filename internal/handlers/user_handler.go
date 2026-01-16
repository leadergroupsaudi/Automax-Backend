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
