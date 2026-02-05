package repository

import (
	"context"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type NotificationTemplateRepository interface {
	Create(ctx context.Context, tpl *models.NotificationTemplate) error
	FindByID(ctx context.Context, id uuid.UUID) (*models.NotificationTemplate, error)
	FindByCode(ctx context.Context, code, channel, language string) (*models.NotificationTemplate, error)
	Update(ctx context.Context, tpl *models.NotificationTemplate) error
	Delete(ctx context.Context, id uuid.UUID) error
	List(ctx context.Context) ([]models.NotificationTemplate, error)
	// Create(ctx context.Context, tpl *models.NotificationTemplate) error
	FindByCodeChannelLanguage(
		ctx context.Context,
		code, channel, language string,
	) (*models.NotificationTemplate, error)
}

type notificationTemplateRepository struct {
	db *gorm.DB
}

func NewNotificationTemplateRepository(db *gorm.DB) NotificationTemplateRepository {
	return &notificationTemplateRepository{db: db}
}

// func (r *notificationTemplateRepository) Create(
// 	ctx context.Context,
// 	tpl *models.NotificationTemplate,
// ) error {
// 	return r.db.WithContext(ctx).Create(tpl).Error
// }

func (r *notificationTemplateRepository) FindByCodeChannelLanguage(
	ctx context.Context,
	code, channel, language string,
) (*models.NotificationTemplate, error) {

	var tpl models.NotificationTemplate
	err := r.db.WithContext(ctx).
		Where("code = ? AND channel = ? AND language = ? AND is_active = true",
			code, channel, language).
		First(&tpl).Error
	if err != nil {
		return nil, err
	}
	return &tpl, nil
}

func (r *notificationTemplateRepository) Create(
	ctx context.Context,
	tpl *models.NotificationTemplate,
) error {
	return r.db.WithContext(ctx).Create(tpl).Error
}

func (r *notificationTemplateRepository) FindByID(
	ctx context.Context,
	id uuid.UUID,
) (*models.NotificationTemplate, error) {
	var tpl models.NotificationTemplate
	if err := r.db.WithContext(ctx).
		First(&tpl, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &tpl, nil
}

func (r *notificationTemplateRepository) FindByCode(
	ctx context.Context,
	code, channel, language string,
) (*models.NotificationTemplate, error) {
	var tpl models.NotificationTemplate
	if err := r.db.WithContext(ctx).
		Where("code = ? AND channel = ? AND language = ? AND is_active = true",
			code, channel, language).
		First(&tpl).Error; err != nil {
		return nil, err
	}
	return &tpl, nil
}

func (r *notificationTemplateRepository) Update(
	ctx context.Context,
	tpl *models.NotificationTemplate,
) error {
	return r.db.WithContext(ctx).Save(tpl).Error
}

func (r *notificationTemplateRepository) Delete(
	ctx context.Context,
	id uuid.UUID,
) error {
	return r.db.WithContext(ctx).
		Delete(&models.NotificationTemplate{}, "id = ?", id).Error
}

func (r *notificationTemplateRepository) List(
	ctx context.Context,
) ([]models.NotificationTemplate, error) {
	var list []models.NotificationTemplate
	err := r.db.WithContext(ctx).
		Order("created_at DESC").
		Find(&list).Error
	return list, err
}
