package repository

import (
	"context"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type CallLogRepository interface {
	Create(ctx context.Context, callLog *models.CallLog) error
	FindByID(ctx context.Context, id uuid.UUID) (*models.CallLog, error)
	FindByCallUUID(ctx context.Context, callUUID string) (*models.CallLog, error)
	Update(ctx context.Context, callLog *models.CallLog) error
	Delete(ctx context.Context, id uuid.UUID) error
	List(ctx context.Context, filter *models.CallLogFilter) ([]models.CallLog, int64, error)
	GetStats(ctx context.Context) (*models.CallLogStats, error)
}

type callLogRepository struct {
	db *gorm.DB
}

func NewCallLogRepository(db *gorm.DB) CallLogRepository {
	return &callLogRepository{db: db}
}

func (r *callLogRepository) Create(ctx context.Context, callLog *models.CallLog) error {
	return r.db.WithContext(ctx).Create(callLog).Error
}

func (r *callLogRepository) FindByID(ctx context.Context, id uuid.UUID) (*models.CallLog, error) {
	var callLog models.CallLog
	err := r.db.WithContext(ctx).
		Preload("Creator").
		First(&callLog, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	return &callLog, nil
}

func (r *callLogRepository) FindByCallUUID(ctx context.Context, callUUID string) (*models.CallLog, error) {
	var callLog models.CallLog
	err := r.db.WithContext(ctx).
		Preload("Creator").
		First(&callLog, "call_uuid = ?", callUUID).Error
	if err != nil {
		return nil, err
	}
	return &callLog, nil
}

func (r *callLogRepository) Update(ctx context.Context, callLog *models.CallLog) error {
	return r.db.WithContext(ctx).Save(callLog).Error
}

func (r *callLogRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Delete(&models.CallLog{}, "id = ?", id).Error
}

func (r *callLogRepository) List(ctx context.Context, filter *models.CallLogFilter) ([]models.CallLog, int64, error) {
	var callLogs []models.CallLog
	var total int64

	query := r.db.WithContext(ctx).Model(&models.CallLog{})

	// Apply filters
	if filter.CreatedBy != nil {
		query = query.Where("created_by = ?", *filter.CreatedBy)
	}
	if filter.Status != "" {
		query = query.Where("status = ?", filter.Status)
	}
	if filter.StartDate != nil {
		query = query.Where("created_at >= ?", *filter.StartDate)
	}
	if filter.EndDate != nil {
		query = query.Where("created_at <= ?", *filter.EndDate)
	}
	if filter.Search != "" {
		searchPattern := "%" + filter.Search + "%"
		query = query.Where("call_uuid ILIKE ? OR recording_url ILIKE ?", searchPattern, searchPattern)
	}

	// Count total
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// Apply pagination
	offset := (filter.Page - 1) * filter.Limit
	err := query.
		Preload("Creator").
		Order("created_at DESC").
		Offset(offset).
		Limit(filter.Limit).
		Find(&callLogs).Error
	if err != nil {
		return nil, 0, err
	}

	return callLogs, total, nil
}

func (r *callLogRepository) GetStats(ctx context.Context) (*models.CallLogStats, error) {
	stats := &models.CallLogStats{}

	// Total calls
	r.db.WithContext(ctx).Model(&models.CallLog{}).Count(&stats.TotalCalls)

	// Calls by status
	var statusStats []struct {
		Status string
		Count  int64
	}
	r.db.WithContext(ctx).Model(&models.CallLog{}).
		Select("status, count(*) as count").
		Group("status").
		Find(&statusStats)

	stats.CallsByStatus = make(map[string]int64)
	for _, stat := range statusStats {
		stats.CallsByStatus[stat.Status] = stat.Count
	}

	// Recent calls (last 30 days)
	thirtyDaysAgo := time.Now().AddDate(0, 0, -30)
	r.db.WithContext(ctx).Model(&models.CallLog{}).
		Where("created_at >= ?", thirtyDaysAgo).
		Count(&stats.RecentCalls)

	return stats, nil
}