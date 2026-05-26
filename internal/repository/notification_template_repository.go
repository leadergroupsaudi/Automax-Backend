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
	// FindByCode looks up an active template by code and channel.
	FindByCode(ctx context.Context, code, channel string) (*models.NotificationTemplate, error)
	// FindByCodeChannelLanguage is kept for notification service compatibility; language param is ignored.
	FindByCodeChannelLanguage(ctx context.Context, code, channel, language string) (*models.NotificationTemplate, error)
	// ExistsByCodeAndChannel returns true if a non-deleted template with the given code+channel exists.
	// Pass excludeID to skip a specific record (useful during updates).
	ExistsByCodeAndChannel(ctx context.Context, code, channel string, excludeID *uuid.UUID) (bool, error)
	Update(ctx context.Context, tpl *models.NotificationTemplate) error
	Delete(ctx context.Context, id uuid.UUID) error
	List(ctx context.Context) ([]models.NotificationTemplate, error)
	ListWithFilters(ctx context.Context, filter models.NotificationTemplateFilter) ([]models.NotificationTemplate, int64, error)
	FindAllByCode(ctx context.Context, code, channel string) ([]models.NotificationTemplate, error)
	FindByTransitionID(ctx context.Context, transitionID uuid.UUID) ([]models.NotificationTemplate, error)
	// FindActiveByActionTypeAndChannel returns all active templates for a given action_type + channel.
	FindActiveByActionTypeAndChannel(ctx context.Context, actionType, channel string) ([]models.NotificationTemplate, error)
}

type notificationTemplateRepository struct {
	db *gorm.DB
}

func NewNotificationTemplateRepository(db *gorm.DB) NotificationTemplateRepository {
	return &notificationTemplateRepository{db: db}
}

func (r *notificationTemplateRepository) Create(ctx context.Context, tpl *models.NotificationTemplate) error {
	return r.db.WithContext(ctx).Create(tpl).Error
}

func (r *notificationTemplateRepository) FindByID(ctx context.Context, id uuid.UUID) (*models.NotificationTemplate, error) {
	var tpl models.NotificationTemplate
	if err := r.db.WithContext(ctx).First(&tpl, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &tpl, nil
}

func (r *notificationTemplateRepository) FindByCode(ctx context.Context, code, channel string) (*models.NotificationTemplate, error) {
	var tpl models.NotificationTemplate
	err := r.db.WithContext(ctx).
		Where("code = ? AND channel = ? AND is_active = true", code, channel).
		First(&tpl).Error
	if err != nil {
		return nil, err
	}
	return &tpl, nil
}

func (r *notificationTemplateRepository) FindByCodeChannelLanguage(ctx context.Context, code, channel, _ string) (*models.NotificationTemplate, error) {
	return r.FindByCode(ctx, code, channel)
}

func (r *notificationTemplateRepository) ExistsByCodeAndChannel(ctx context.Context, code, channel string, excludeID *uuid.UUID) (bool, error) {
	q := r.db.WithContext(ctx).Model(&models.NotificationTemplate{}).
		Where("code = ? AND channel = ?", code, channel)
	if excludeID != nil {
		q = q.Where("id != ?", *excludeID)
	}
	var count int64
	err := q.Count(&count).Error
	return count > 0, err
}

func (r *notificationTemplateRepository) Update(ctx context.Context, tpl *models.NotificationTemplate) error {
	return r.db.WithContext(ctx).Save(tpl).Error
}

func (r *notificationTemplateRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Delete(&models.NotificationTemplate{}, "id = ?", id).Error
}

func (r *notificationTemplateRepository) List(ctx context.Context) ([]models.NotificationTemplate, error) {
	var list []models.NotificationTemplate
	err := r.db.WithContext(ctx).Order("created_at DESC").Find(&list).Error
	return list, err
}

func (r *notificationTemplateRepository) ListWithFilters(ctx context.Context, filter models.NotificationTemplateFilter) ([]models.NotificationTemplate, int64, error) {
	q := r.db.WithContext(ctx).Model(&models.NotificationTemplate{})

	if filter.Channel != "" {
		q = q.Where("channel = ?", filter.Channel)
	}
	if filter.ModuleType != "" {
		q = q.Where("module_type = ?", filter.ModuleType)
	}
	if filter.ActionType != "" {
		q = q.Where("action_type = ?", filter.ActionType)
	}
	if filter.IsActive != nil {
		q = q.Where("is_active = ?", *filter.IsActive)
	}
	if filter.Code != "" {
		q = q.Where("code = ?", filter.Code)
	}
	if filter.Search != "" {
		like := "%" + filter.Search + "%"
		q = q.Where("name ILIKE ?", like)
	}
	if filter.TransitionID != nil {
		q = q.Where("transition_id = ?", *filter.TransitionID)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	page := filter.Page
	limit := filter.Limit
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	offset := (page - 1) * limit

	var list []models.NotificationTemplate
	err := q.Order("created_at DESC").Offset(offset).Limit(limit).Find(&list).Error
	return list, total, err
}

func (r *notificationTemplateRepository) FindAllByCode(ctx context.Context, code, channel string) ([]models.NotificationTemplate, error) {
	var list []models.NotificationTemplate
	q := r.db.WithContext(ctx).Where("code = ?", code)
	if channel != "" {
		q = q.Where("channel = ?", channel)
	}
	err := q.Order("code ASC").Find(&list).Error
	return list, err
}

func (r *notificationTemplateRepository) FindByTransitionID(ctx context.Context, transitionID uuid.UUID) ([]models.NotificationTemplate, error) {
	var list []models.NotificationTemplate
	err := r.db.WithContext(ctx).
		Where("transition_id = ?", transitionID).
		Order("code ASC").
		Find(&list).Error
	return list, err
}

func (r *notificationTemplateRepository) FindActiveByActionTypeAndChannel(ctx context.Context, actionType, channel string) ([]models.NotificationTemplate, error) {
	var list []models.NotificationTemplate
	err := r.db.WithContext(ctx).
		Where("action_type = ? AND channel = ? AND is_active = true", actionType, channel).
		Order("created_at ASC").
		Find(&list).Error
	return list, err
}
