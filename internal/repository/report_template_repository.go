package repository

import (
	"context"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ReportTemplateRepository interface {
	Create(ctx context.Context, template *models.ReportTemplate) error
	FindByID(ctx context.Context, id uuid.UUID) (*models.ReportTemplate, error)
	FindByIDWithRelations(ctx context.Context, id uuid.UUID) (*models.ReportTemplate, error)
	Update(ctx context.Context, template *models.ReportTemplate) error
	Delete(ctx context.Context, id uuid.UUID) error
	List(ctx context.Context, filter *models.ReportTemplateFilter) ([]models.ReportTemplate, int64, error)
	GetDefault(ctx context.Context) (*models.ReportTemplate, error)
	SetDefault(ctx context.Context, id uuid.UUID) error
}

type reportTemplateRepository struct {
	db *gorm.DB
}

func NewReportTemplateRepository(db *gorm.DB) ReportTemplateRepository {
	return &reportTemplateRepository{db: db}
}

func (r *reportTemplateRepository) Create(ctx context.Context, template *models.ReportTemplate) error {
	return r.db.WithContext(ctx).Create(template).Error
}

func (r *reportTemplateRepository) FindByID(ctx context.Context, id uuid.UUID) (*models.ReportTemplate, error) {
	var template models.ReportTemplate
	err := r.db.WithContext(ctx).First(&template, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	return &template, nil
}

func (r *reportTemplateRepository) FindByIDWithRelations(ctx context.Context, id uuid.UUID) (*models.ReportTemplate, error) {
	var template models.ReportTemplate
	err := r.db.WithContext(ctx).
		Preload("CreatedBy").
		First(&template, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	return &template, nil
}

func (r *reportTemplateRepository) Update(ctx context.Context, template *models.ReportTemplate) error {
	return r.db.WithContext(ctx).Save(template).Error
}

func (r *reportTemplateRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Delete(&models.ReportTemplate{}, "id = ?", id).Error
}

func (r *reportTemplateRepository) List(ctx context.Context, filter *models.ReportTemplateFilter) ([]models.ReportTemplate, int64, error) {
	var templates []models.ReportTemplate
	var total int64

	query := r.db.WithContext(ctx).Model(&models.ReportTemplate{})

	// Apply filters
	if filter.Search != "" {
		searchPattern := "%" + filter.Search + "%"
		query = query.Where("name ILIKE ? OR description ILIKE ?", searchPattern, searchPattern)
	}

	if filter.IsPublic != nil {
		query = query.Where("is_public = ?", *filter.IsPublic)
	}

	if filter.CreatedByID != nil {
		query = query.Where("created_by_id = ?", *filter.CreatedByID)
	}

	// Count total
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// Apply pagination
	page := filter.Page
	if page < 1 {
		page = 1
	}
	limit := filter.Limit
	if limit < 1 || limit > 100 {
		limit = 20
	}
	offset := (page - 1) * limit

	// Fetch with relations
	err := query.
		Preload("CreatedBy").
		Offset(offset).
		Limit(limit).
		Order("created_at DESC").
		Find(&templates).Error

	if err != nil {
		return nil, 0, err
	}

	return templates, total, nil
}

func (r *reportTemplateRepository) GetDefault(ctx context.Context) (*models.ReportTemplate, error) {
	var template models.ReportTemplate
	err := r.db.WithContext(ctx).
		Preload("CreatedBy").
		First(&template, "is_default = ?", true).Error
	if err != nil {
		return nil, err
	}
	return &template, nil
}

func (r *reportTemplateRepository) SetDefault(ctx context.Context, id uuid.UUID) error {
	// First, unset all defaults
	if err := r.db.WithContext(ctx).Model(&models.ReportTemplate{}).
		Where("is_default = ?", true).
		Update("is_default", false).Error; err != nil {
		return err
	}

	// Set the new default
	return r.db.WithContext(ctx).Model(&models.ReportTemplate{}).
		Where("id = ?", id).
		Update("is_default", true).Error
}
