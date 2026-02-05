package repository

import (
	"context"

	"github.com/automax/backend/internal/models"
	"gorm.io/gorm"
)

type NotificationLogRepository interface {
	Create(ctx context.Context, log *models.NotificationLog) error
}

type notificationLogRepository struct {
	db *gorm.DB
}

func NewNotificationLogRepository(db *gorm.DB) NotificationLogRepository {
	return &notificationLogRepository{db: db}
}

func (r *notificationLogRepository) Create(
	ctx context.Context,
	log *models.NotificationLog,
) error {
	return r.db.WithContext(ctx).Create(log).Error
}
