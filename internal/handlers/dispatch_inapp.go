package handlers

import (
	"context"
	"fmt"
	"strings"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/internal/services"
	"github.com/google/uuid"
)

// InAppNotifier creates an in-app (inbox) notification for one user.
type InAppNotifier interface {
	Notify(ctx context.Context, userID uuid.UUID, subject, body, subjectAr, bodyAr string, meta *models.NotificationMeta) error
}

type inAppNotifier struct {
	notifications *services.NotificationService
	users         repository.UserRepository
}

// NewInAppNotifier builds an InAppNotifier on the "notification" channel, which
// addresses internal users by email.
func NewInAppNotifier(notifications *services.NotificationService, users repository.UserRepository) InAppNotifier {
	return &inAppNotifier{notifications: notifications, users: users}
}

func (n *inAppNotifier) Notify(ctx context.Context, userID uuid.UUID, subject, body, subjectAr, bodyAr string, meta *models.NotificationMeta) error {
	user, err := n.users.FindByID(ctx, userID)
	if err != nil || user == nil {
		return fmt.Errorf("user lookup failed: %w", err)
	}
	email := strings.TrimSpace(user.Email)
	if email == "" {
		return fmt.Errorf("user %s has no email", userID)
	}
	result, err := n.notifications.SendNotification(ctx, "notification", nil, "en",
		[]string{email}, nil, nil, subject, body, nil, nil, nil, nil)
	if err != nil {
		return err
	}
	if len(result.InboxLogIDs) > 0 {
		if meta != nil {
			_ = n.notifications.SetMetaOnLogs(ctx, result.InboxLogIDs, meta)
		}
		if subjectAr != "" || bodyAr != "" {
			_ = n.notifications.SetArContentOnLogs(ctx, result.InboxLogIDs, subjectAr, bodyAr)
		}
	}
	return nil
}
