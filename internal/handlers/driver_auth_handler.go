package handlers

import (
	"errors"
	"strings"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/services"
	"github.com/automax/backend/pkg/i18n"
	"github.com/automax/backend/pkg/utils"
	"github.com/automax/backend/pkg/validation"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	passcodeMaxAttempts = 5
	passcodeBlockWindow = 15 * time.Minute
)

// DriverAuthHandler serves the public dispatch driver endpoints
// (register, and login by unit id + passcode).
type DriverAuthHandler struct {
	userService services.UserService
	dispatch    *DispatchProxy
	redis       *redis.Client
}

func NewDriverAuthHandler(userService services.UserService, dispatch *DispatchProxy, redisClient *redis.Client) *DriverAuthHandler {
	return &DriverAuthHandler{userService: userService, dispatch: dispatch, redis: redisClient}
}

// Register handles POST /dispatch/auth/register.
func (h *DriverAuthHandler) Register(c *fiber.Ctx) error {
	ctx := c.UserContext()
	var req models.DriverRegisterRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(ctx, "invalid_request_body"))
	}
	if validationErrors := validation.ValidateStruct(ctx, &req); len(validationErrors) != 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"errors":  validationErrors,
		})
	}

	user, err := h.userService.RegisterDriver(ctx, &req)
	if err != nil {
		status := fiber.StatusBadRequest
		msg := err.Error()
		switch {
		case errors.Is(err, services.ErrUserLimitReached):
			status = fiber.StatusForbidden
		case msg == i18n.T(ctx, "email_already_exists") || msg == i18n.T(ctx, "username_already_exists"):
			status = fiber.StatusConflict
		case strings.Contains(msg, "driver role is not configured"):
			status = fiber.StatusInternalServerError
			msg = "driver registration is unavailable"
		}
		return utils.ErrorResponse(c, status, msg)
	}
	return utils.SuccessResponse(c, fiber.StatusCreated, i18n.T(ctx, "user_registered"), fiber.Map{
		"userId":    user.ID.String(),
		"userName":  user.Username,
		"userEmail": user.Email,
	})
}

// LoginWithPasscode handles POST /dispatch/auth/login/passcode.
// Flow: unit id -> Registry gives the driver id -> verify passcode against that user.
func (h *DriverAuthHandler) LoginWithPasscode(c *fiber.Ctx) error {
	ctx := c.UserContext()
	var req models.PasscodeLoginRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(ctx, "invalid_request_body"))
	}
	if validationErrors := validation.ValidateStruct(ctx, &req); len(validationErrors) != 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"errors":  validationErrors,
		})
	}

	// A 4-6 digit passcode is easy to brute-force, so limit attempts per unit and per IP.
	unitKey := "passcode_attempts:unit:" + strings.ToLower(strings.TrimSpace(req.UnitID))
	ipKey := "passcode_attempts:ip:" + c.IP()
	if h.blocked(c, unitKey) || h.blocked(c, ipKey) {
		return utils.ErrorResponse(c, fiber.StatusTooManyRequests, "Too many login attempts. Please try again later.")
	}

	fail := func() error {
		h.recordFailure(c, unitKey)
		h.recordFailure(c, ipKey)
		return utils.ErrorResponse(c, fiber.StatusUnauthorized, i18n.T(ctx, "invalid_credentials"))
	}

	driverIDStr, err := h.dispatch.FetchUnitDriverID(ctx, strings.TrimSpace(req.UnitID))
	if err != nil {
		if errors.Is(err, ErrUnitDriverNotFound) {
			return fail()
		}
		return utils.ErrorResponse(c, fiber.StatusBadGateway, "dispatch service is unavailable")
	}
	driverID, err := uuid.Parse(driverIDStr)
	if err != nil {
		return fail()
	}

	resp, err := h.userService.LoginWithPasscode(ctx, driverID, req.Passcode)
	if err != nil {
		if errors.Is(err, services.ErrInvalidPasscodeLogin) {
			return fail()
		}
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, i18n.T(ctx, "internal_server_error"))
	}

	if h.redis != nil {
		h.redis.Del(ctx, unitKey)
	}
	// The spec asks for the bare object, so respond without the success envelope.
	return c.Status(fiber.StatusOK).JSON(resp)
}

func (h *DriverAuthHandler) blocked(c *fiber.Ctx, key string) bool {
	if h.redis == nil {
		return false
	}
	n, err := h.redis.Get(c.UserContext(), key).Int()
	return err == nil && n >= passcodeMaxAttempts
}

func (h *DriverAuthHandler) recordFailure(c *fiber.Ctx, key string) {
	if h.redis == nil {
		return
	}
	ctx := c.UserContext()
	if n, err := h.redis.Incr(ctx, key).Result(); err == nil && n == 1 {
		h.redis.Expire(ctx, key, passcodeBlockWindow)
	}
}
