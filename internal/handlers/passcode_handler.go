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

// PassCodeHandler serves passcode management (admin) and passcode login (public).
type PassCodeHandler struct {
	svc   *services.PassCodeService
	redis *redis.Client
}

func NewPassCodeHandler(svc *services.PassCodeService, redisClient *redis.Client) *PassCodeHandler {
	return &PassCodeHandler{svc: svc, redis: redisClient}
}

// Set handles PUT /users/:userID/passcode (adds, or replaces the existing one).
func (h *PassCodeHandler) Set(c *fiber.Ctx) error {
	ctx := c.UserContext()
	userID, err := uuid.Parse(c.Params("userID"))
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "invalid user id")
	}
	var req models.SetPassCodeRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(ctx, "invalid_request_body"))
	}
	if errs := validation.ValidateStruct(ctx, &req); len(errs) != 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"success": false, "errors": errs})
	}
	if err := h.svc.Set(ctx, userID, req.Passcode); err != nil {
		if errors.Is(err, services.ErrPassCodeUserNotFound) {
			return utils.ErrorResponse(c, fiber.StatusNotFound, "user not found")
		}
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, i18n.T(ctx, "internal_server_error"))
	}
	return utils.SuccessResponse(c, fiber.StatusOK, "passcode saved", nil)
}

// Remove handles DELETE /users/:userID/passcode.
func (h *PassCodeHandler) Remove(c *fiber.Ctx) error {
	ctx := c.UserContext()
	userID, err := uuid.Parse(c.Params("userID"))
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "invalid user id")
	}
	if err := h.svc.Remove(ctx, userID); err != nil {
		if errors.Is(err, services.ErrPassCodeNotFound) {
			return utils.ErrorResponse(c, fiber.StatusNotFound, "passcode not found")
		}
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, i18n.T(ctx, "internal_server_error"))
	}
	return utils.SuccessResponse(c, fiber.StatusOK, "passcode removed", nil)
}

// Login handles POST /auth/login/passcode.
func (h *PassCodeHandler) Login(c *fiber.Ctx) error {
	ctx := c.UserContext()
	var req models.PasscodeLoginRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(ctx, "invalid_request_body"))
	}
	if errs := validation.ValidateStruct(ctx, &req); len(errs) != 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"success": false, "errors": errs})
	}
	userID, err := uuid.Parse(req.UserID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "invalid user id")
	}

	// A 4-6 digit passcode is easy to brute-force, so limit failures per user and per IP.
	userKey := "passcode_attempts:user:" + strings.ToLower(userID.String())
	ipKey := "passcode_attempts:ip:" + c.IP()
	if h.blocked(c, userKey) || h.blocked(c, ipKey) {
		return utils.ErrorResponse(c, fiber.StatusTooManyRequests, "Too many login attempts. Please try again later.")
	}

	resp, err := h.svc.Login(ctx, userID, req.Passcode)
	if err != nil {
		if errors.Is(err, services.ErrInvalidPasscodeLogin) {
			h.recordFailure(c, userKey)
			h.recordFailure(c, ipKey)
			return utils.ErrorResponse(c, fiber.StatusUnauthorized, i18n.T(ctx, "invalid_credentials"))
		}
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, i18n.T(ctx, "internal_server_error"))
	}
	if h.redis != nil {
		h.redis.Del(ctx, userKey)
	}
	// The spec asks for the bare object, so respond without the success envelope.
	return c.Status(fiber.StatusOK).JSON(resp)
}

func (h *PassCodeHandler) blocked(c *fiber.Ctx, key string) bool {
	if h.redis == nil {
		return false
	}
	n, err := h.redis.Get(c.UserContext(), key).Int()
	return err == nil && n >= passcodeMaxAttempts
}

func (h *PassCodeHandler) recordFailure(c *fiber.Ctx, key string) {
	if h.redis == nil {
		return
	}
	ctx := c.UserContext()
	if n, err := h.redis.Incr(ctx, key).Result(); err == nil && n == 1 {
		h.redis.Expire(ctx, key, passcodeBlockWindow)
	}
}
