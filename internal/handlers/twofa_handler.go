package handlers

import (
	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/services"
	"github.com/automax/backend/pkg/utils"
	"github.com/automax/backend/pkg/validation"
	"github.com/go-playground/validator/v10"
	"github.com/gofiber/fiber/v2"
)

type TwoFAHandler struct {
	twoFAService *services.TwoFAService
	validator    *validator.Validate
}

func NewTwoFAHandler(twoFAService *services.TwoFAService) *TwoFAHandler {
	return &TwoFAHandler{
		twoFAService: twoFAService,
		validator:    validator.New(),
	}
}

// VerifyOTP handles POST /api/v1/auth/verify-2fa-otp
// Verifies the 2FA OTP and returns JWT tokens
func (h *TwoFAHandler) VerifyOTP(c *fiber.Ctx) error {
	var req models.TwoFAVerifyRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	if validationErrors := validation.ValidateStruct(c.UserContext(), &req); len(validationErrors) != 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"errors":  validationErrors,
		})
	}

	response, err := h.twoFAService.VerifyTwoFAOTP(c.UserContext(), req.SessionID, req.OTP)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusUnauthorized, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "2FA verification successful", response)
}

// ResendOTP handles POST /api/v1/auth/resend-2fa-otp
// Resends a new OTP for an existing 2FA session
func (h *TwoFAHandler) ResendOTP(c *fiber.Ctx) error {
	var req struct {
		SessionID string `json:"session_id" validate:"required"`
	}

	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request body")
	}

	if validationErrors := validation.ValidateStruct(c.UserContext(), &req); len(validationErrors) != 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"errors":  validationErrors,
		})
	}

	if err := h.twoFAService.ResendTwoFAOTP(c.UserContext(), req.SessionID); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, err.Error())
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "New OTP sent successfully", nil)
}
