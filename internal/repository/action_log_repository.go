package repository

import (
	"context"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ActionLogRepository interface {
	Create(ctx context.Context, log *models.ActionLog) error
	FindByID(ctx context.Context, id uuid.UUID) (*models.ActionLog, error)
	List(ctx context.Context, filter *models.ActionLogFilter) ([]models.ActionLog, int64, error)
	GetStats(ctx context.Context) (*models.ActionLogStats, error)
	GetUserActions(ctx context.Context, userID uuid.UUID, page, limit int) ([]models.ActionLog, int64, error)
	DeleteOlderThan(ctx context.Context, date time.Time) (int64, error)
	GetDistinctModules(ctx context.Context) ([]string, error)
	GetDistinctActions(ctx context.Context) ([]string, error)
}

type actionLogRepository struct {
	db *gorm.DB
}

func NewActionLogRepository(db *gorm.DB) ActionLogRepository {
	return &actionLogRepository{db: db}
}

func (r *actionLogRepository) Create(ctx context.Context, log *models.ActionLog) error {
	return r.db.WithContext(ctx).Create(log).Error
}

func (r *actionLogRepository) FindByID(ctx context.Context, id uuid.UUID) (*models.ActionLog, error) {
	var log models.ActionLog
	err := r.db.WithContext(ctx).
		Preload("User").
		First(&log, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	return &log, nil
}

func (r *actionLogRepository) List(ctx context.Context, filter *models.ActionLogFilter) ([]models.ActionLog, int64, error) {
	var logs []models.ActionLog
	var total int64

	query := r.db.WithContext(ctx).Model(&models.ActionLog{})

	// Apply filters
	if filter.UserID != nil {
		query = query.Where("user_id = ?", *filter.UserID)
	}
	if filter.Action != "" {
		query = query.Where("action = ?", filter.Action)
	}
	if filter.Module != "" {
		query = query.Where("module = ?", filter.Module)
	}
	if filter.Status != "" {
		query = query.Where("status = ?", filter.Status)
	}
	if filter.ResourceID != "" {
		query = query.Where("resource_id = ?", filter.ResourceID)
	}
	if filter.StartDate != nil {
		query = query.Where("created_at >= ?", *filter.StartDate)
	}
	if filter.EndDate != nil {
		query = query.Where("created_at <= ?", *filter.EndDate)
	}
	if filter.Search != "" {
		searchPattern := "%" + filter.Search + "%"
		query = query.Where("description ILIKE ? OR ip_address ILIKE ?", searchPattern, searchPattern)
	}

	// Count total
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// Apply pagination
	offset := (filter.Page - 1) * filter.Limit
	err := query.
		Preload("User").
		Order("created_at DESC").
		Offset(offset).
		Limit(filter.Limit).
		Find(&logs).Error
	if err != nil {
		return nil, 0, err
	}

	return logs, total, nil
}

func (r *actionLogRepository) GetStats(ctx context.Context) (*models.ActionLogStats, error) {
	stats := &models.ActionLogStats{
		ActionsByModule: make(map[string]int64),
		ActionsByType:   make(map[string]int64),
	}

	// Total actions
	if err := r.db.WithContext(ctx).Model(&models.ActionLog{}).Count(&stats.TotalActions).Error; err != nil {
		return nil, err
	}

	// Today's actions
	today := time.Now().Truncate(24 * time.Hour)
	if err := r.db.WithContext(ctx).Model(&models.ActionLog{}).
		Where("created_at >= ?", today).
		Count(&stats.TodayActions).Error; err != nil {
		return nil, err
	}

	// Success rate
	var successCount int64
	if err := r.db.WithContext(ctx).Model(&models.ActionLog{}).
		Where("status = ?", "success").
		Count(&successCount).Error; err != nil {
		return nil, err
	}
	if stats.TotalActions > 0 {
		stats.SuccessRate = float64(successCount) / float64(stats.TotalActions) * 100
	}

	// Actions by module
	type moduleCount struct {
		Module string
		Count  int64
	}
	var moduleCounts []moduleCount
	if err := r.db.WithContext(ctx).Model(&models.ActionLog{}).
		Select("module, count(*) as count").
		Group("module").
		Scan(&moduleCounts).Error; err != nil {
		return nil, err
	}
	for _, mc := range moduleCounts {
		stats.ActionsByModule[mc.Module] = mc.Count
	}

	// Actions by type
	type actionCount struct {
		Action string
		Count  int64
	}
	var actionCounts []actionCount
	if err := r.db.WithContext(ctx).Model(&models.ActionLog{}).
		Select("action, count(*) as count").
		Group("action").
		Scan(&actionCounts).Error; err != nil {
		return nil, err
	}
	for _, ac := range actionCounts {
		stats.ActionsByType[ac.Action] = ac.Count
	}

	return stats, nil
}

func (r *actionLogRepository) GetUserActions(ctx context.Context, userID uuid.UUID, page, limit int) ([]models.ActionLog, int64, error) {
	var logs []models.ActionLog
	var total int64

	offset := (page - 1) * limit

	if err := r.db.WithContext(ctx).Model(&models.ActionLog{}).
		Where("user_id = ?", userID).
		Count(&total).Error; err != nil {
		return nil, 0, err
	}

	err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("created_at DESC").
		Offset(offset).
		Limit(limit).
		Find(&logs).Error
	if err != nil {
		return nil, 0, err
	}

	return logs, total, nil
}

func (r *actionLogRepository) DeleteOlderThan(ctx context.Context, date time.Time) (int64, error) {
	result := r.db.WithContext(ctx).
		Where("created_at < ?", date).
		Delete(&models.ActionLog{})
	return result.RowsAffected, result.Error
}

func (r *actionLogRepository) GetDistinctModules(ctx context.Context) ([]string, error) {
	var modules []string
	err := r.db.WithContext(ctx).Model(&models.ActionLog{}).
		Distinct("module").
		Pluck("module", &modules).Error
	return modules, err
}

func (r *actionLogRepository) GetDistinctActions(ctx context.Context) ([]string, error) {
	var actions []string
	err := r.db.WithContext(ctx).Model(&models.ActionLog{}).
		Distinct("action").
		Pluck("action", &actions).Error
	return actions, err
}
