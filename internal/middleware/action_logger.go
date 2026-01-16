package middleware

import (
	"time"

	"github.com/automax/backend/internal/services"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type ActionLoggerConfig struct {
	Enabled      bool
	SkipPaths    []string
	SkipMethods  []string
	LogService   services.ActionLogService
}

func ActionLogger(config ActionLoggerConfig) fiber.Handler {
	skipPaths := make(map[string]bool)
	for _, path := range config.SkipPaths {
		skipPaths[path] = true
	}

	skipMethods := make(map[string]bool)
	for _, method := range config.SkipMethods {
		skipMethods[method] = true
	}

	return func(c *fiber.Ctx) error {
		// Skip if logging is disabled
		if !config.Enabled {
			return c.Next()
		}

		// Skip certain paths
		if skipPaths[c.Path()] {
			return c.Next()
		}

		// Skip certain methods (e.g., GET requests)
		if skipMethods[c.Method()] {
			return c.Next()
		}

		start := time.Now()

		// Process request
		err := c.Next()

		// Calculate duration
		duration := time.Since(start).Milliseconds()

		// Get user ID from context (set by auth middleware)
		userIDInterface := c.Locals("userID")
		if userIDInterface == nil {
			return err
		}

		userID, ok := userIDInterface.(uuid.UUID)
		if !ok {
			return err
		}

		// Determine action and module from request
		action := getActionFromMethod(c.Method())
		module := getModuleFromPath(c.Path())

		// Determine status
		status := "success"
		errorMsg := ""
		if err != nil {
			status = "failed"
			errorMsg = err.Error()
		} else if c.Response().StatusCode() >= 400 {
			status = "failed"
		}

		// Get resource ID from params if available
		resourceID := c.Params("id")

		// Log the action asynchronously
		go func() {
			logErr := config.LogService.LogAction(c.Context(), &services.LogActionParams{
				UserID:      userID,
				Action:      action,
				Module:      module,
				ResourceID:  resourceID,
				Description: generateDescription(action, module, resourceID),
				IPAddress:   c.IP(),
				UserAgent:   c.Get("User-Agent"),
				Status:      status,
				ErrorMsg:    errorMsg,
				Duration:    duration,
			})
			if logErr != nil {
				// Log error but don't fail the request
				// In production, you might want to use a proper logger
			}
		}()

		return err
	}
}

func getActionFromMethod(method string) string {
	switch method {
	case "POST":
		return "create"
	case "PUT", "PATCH":
		return "update"
	case "DELETE":
		return "delete"
	case "GET":
		return "view"
	default:
		return "other"
	}
}

func getModuleFromPath(path string) string {
	// Extract module from path like /api/admin/users -> users
	// or /api/v1/departments -> departments
	segments := splitPath(path)

	// Find the relevant segment (skip api, admin, v1, etc.)
	for i := len(segments) - 1; i >= 0; i-- {
		seg := segments[i]
		if seg != "" && seg != "api" && seg != "admin" && seg != "v1" && !isUUID(seg) {
			return seg
		}
	}

	return "unknown"
}

func splitPath(path string) []string {
	var segments []string
	current := ""
	for _, char := range path {
		if char == '/' {
			if current != "" {
				segments = append(segments, current)
				current = ""
			}
		} else {
			current += string(char)
		}
	}
	if current != "" {
		segments = append(segments, current)
	}
	return segments
}

func isUUID(s string) bool {
	_, err := uuid.Parse(s)
	return err == nil
}

func generateDescription(action, module, resourceID string) string {
	desc := action + " " + module
	if resourceID != "" {
		desc += " (ID: " + resourceID + ")"
	}
	return desc
}

// LogAction is a helper function to manually log actions
func LogAction(c *fiber.Ctx, logService services.ActionLogService, params *services.LogActionParams) {
	// Get user ID from context
	userIDInterface := c.Locals("userID")
	if userIDInterface == nil {
		return
	}

	userID, ok := userIDInterface.(uuid.UUID)
	if !ok {
		return
	}

	params.UserID = userID
	params.IPAddress = c.IP()
	params.UserAgent = c.Get("User-Agent")

	go func() {
		_ = logService.LogAction(c.Context(), params)
	}()
}
