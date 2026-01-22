package middleware

import (
	"context"
	"strings"

	"github.com/automax/backend/internal/database"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/pkg/utils"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type AuthMiddleware struct {
	jwtManager   *utils.JWTManager
	sessionStore *database.SessionStore
	userRepo     repository.UserRepository
}

func NewAuthMiddleware(jwtManager *utils.JWTManager, sessionStore *database.SessionStore, userRepo repository.UserRepository) *AuthMiddleware {
	return &AuthMiddleware{
		jwtManager:   jwtManager,
		sessionStore: sessionStore,
		userRepo:     userRepo,
	}
}

func (m *AuthMiddleware) Authenticate() fiber.Handler {
	return func(c *fiber.Ctx) error {
		var token string

		// First check Authorization header
		authHeader := c.Get("Authorization")
		if authHeader != "" {
			parts := strings.Split(authHeader, " ")
			if len(parts) == 2 && parts[0] == "Bearer" {
				token = parts[1]
			}
		}

		// If no token in header, check query parameter (for file downloads/images)
		if token == "" {
			token = c.Query("token")
		}

		if token == "" {
			return utils.ErrorResponse(c, fiber.StatusUnauthorized, "Missing authorization token")
		}

		isBlacklisted, err := m.sessionStore.IsTokenBlacklisted(context.Background(), token)
		if err != nil {
			return utils.ErrorResponse(c, fiber.StatusInternalServerError, "Failed to validate token")
		}
		if isBlacklisted {
			return utils.ErrorResponse(c, fiber.StatusUnauthorized, "Token has been revoked")
		}

		claims, err := m.jwtManager.ValidateToken(token)
		if err != nil {
			return utils.ErrorResponse(c, fiber.StatusUnauthorized, "Invalid or expired token")
		}

		c.Locals("user_id", claims.UserID)
		c.Locals("email", claims.Email)
		c.Locals("role", claims.Role)
		c.Locals("token", token)

		return c.Next()
	}
}

func (m *AuthMiddleware) RequireRole(roles ...string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		userRole := c.Locals("role").(string)

		for _, role := range roles {
			if userRole == role {
				return c.Next()
			}
		}

		return utils.ErrorResponse(c, fiber.StatusForbidden, "Insufficient permissions")
	}
}

// RequirePermission checks if the authenticated user has any of the specified permissions
func (m *AuthMiddleware) RequirePermission(permissions ...string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		userID, ok := c.Locals("user_id").(uuid.UUID)
		if !ok {
			return utils.ErrorResponse(c, fiber.StatusUnauthorized, "User not authenticated")
		}

		user, err := m.userRepo.FindByIDWithPermissions(c.Context(), userID)
		if err != nil {
			return utils.ErrorResponse(c, fiber.StatusForbidden, "User not found")
		}

		// Super admin has all permissions
		if user.IsSuperAdmin {
			c.Locals("user", user)
			return c.Next()
		}

		// Check if user has any of the required permissions
		for _, perm := range permissions {
			if user.HasPermission(perm) {
				c.Locals("user", user)
				return c.Next()
			}
		}

		return utils.ErrorResponse(c, fiber.StatusForbidden, "Insufficient permissions")
	}
}

func (m *AuthMiddleware) OptionalAuth() fiber.Handler {
	return func(c *fiber.Ctx) error {
		authHeader := c.Get("Authorization")
		if authHeader == "" {
			return c.Next()
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			return c.Next()
		}

		token := parts[1]

		isBlacklisted, _ := m.sessionStore.IsTokenBlacklisted(context.Background(), token)
		if isBlacklisted {
			return c.Next()
		}

		claims, err := m.jwtManager.ValidateToken(token)
		if err != nil {
			return c.Next()
		}

		c.Locals("user_id", claims.UserID)
		c.Locals("email", claims.Email)
		c.Locals("role", claims.Role)
		c.Locals("token", token)

		return c.Next()
	}
}
