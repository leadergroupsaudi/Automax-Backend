package middleware

import (
	"context"
	"strings"

	"github.com/automax/backend/internal/database"
	"github.com/automax/backend/pkg/utils"
	"github.com/gofiber/fiber/v2"
)

type AuthMiddleware struct {
	jwtManager   *utils.JWTManager
	sessionStore *database.SessionStore
}

func NewAuthMiddleware(jwtManager *utils.JWTManager, sessionStore *database.SessionStore) *AuthMiddleware {
	return &AuthMiddleware{
		jwtManager:   jwtManager,
		sessionStore: sessionStore,
	}
}

func (m *AuthMiddleware) Authenticate() fiber.Handler {
	return func(c *fiber.Ctx) error {
		authHeader := c.Get("Authorization")
		if authHeader == "" {
			return utils.ErrorResponse(c, fiber.StatusUnauthorized, "Missing authorization header")
		}

		parts := strings.Split(authHeader, " ")
		if len(parts) != 2 || parts[0] != "Bearer" {
			return utils.ErrorResponse(c, fiber.StatusUnauthorized, "Invalid authorization header format")
		}

		token := parts[1]

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
