package handlers

import (
	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/pkg/constants"
	"github.com/automax/backend/pkg/utils"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// MobileNotificationHandler handles mobile-specific notification endpoints:
// push notification history and acknowledgement.
type MobileNotificationHandler struct {
	notificationRepo repository.NotificationLogRepository
}

func NewMobileNotificationHandler(notificationRepo repository.NotificationLogRepository) *MobileNotificationHandler {
	return &MobileNotificationHandler{notificationRepo: notificationRepo}
}

// GetMyNotifications returns the most recent 50 push notifications received by the current user.
// GET /api/v1/mobile/notifications
func (h *MobileNotificationHandler) GetMyNotifications(c *fiber.Ctx) error {
	userIDVal := c.Locals(constants.ContextKeys.UserID)
	if userIDVal == nil {
		return utils.ErrorResponse(c, fiber.StatusUnauthorized, "Unauthorized")
	}
	userID := userIDVal.(uuid.UUID)

	channel := "push-notification"
	filter := &models.NotificationLogFilter{
		ReceivedBy: &userID,
		Channel:    channel,
		Limit:      50,
		Page:       1,
	}

	logs, _, err := h.notificationRepo.List(c.Context(), filter)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, "Failed to fetch notifications")
	}

	responses := make([]models.NotificationLogResponse, len(logs))
	for i, l := range logs {
		responses[i] = models.ToNotificationLogResponse(&l)
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Notifications retrieved", fiber.Map{
		"notifications": responses,
		"count":         len(responses),
	})
}

// AcknowledgeNotification marks a push notification as read/acknowledged.
// POST /api/v1/mobile/notifications/:id/acknowledge
func (h *MobileNotificationHandler) AcknowledgeNotification(c *fiber.Ctx) error {
	userIDVal := c.Locals(constants.ContextKeys.UserID)
	if userIDVal == nil {
		return utils.ErrorResponse(c, fiber.StatusUnauthorized, "Unauthorized")
	}
	userID := userIDVal.(uuid.UUID)

	notifID, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid notification ID")
	}

	// Verify the notification belongs to this user before acknowledging
	notif, err := h.notificationRepo.FindByID(c.Context(), notifID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "Notification not found")
	}
	if notif.ReceivedBy == nil || *notif.ReceivedBy != userID {
		return utils.ErrorResponse(c, fiber.StatusForbidden, "Access denied")
	}

	if err := h.notificationRepo.MarkAsRead(c.Context(), notifID, true); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, "Failed to acknowledge notification")
	}

	return utils.SuccessResponse(c, fiber.StatusOK, "Notification acknowledged", nil)
}
