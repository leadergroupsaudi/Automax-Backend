package repository

import (
	"context"
	"fmt"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ClassificationRepository interface {
	Create(ctx context.Context, classification *models.Classification) error
	FindByID(ctx context.Context, id uuid.UUID) (*models.Classification, error)
	FindByNameAndParent(ctx context.Context, name string, parentID *uuid.UUID) (*models.Classification, error)
	Update(ctx context.Context, classification *models.Classification) error
	Delete(ctx context.Context, id uuid.UUID) error
	List(ctx context.Context) ([]models.Classification, error)
	ListByType(ctx context.Context, classType string) ([]models.Classification, error)
	GetTree(ctx context.Context) ([]models.Classification, error)
	GetTreeByType(ctx context.Context, classType string) ([]models.Classification, error)
	GetChildren(ctx context.Context, parentID uuid.UUID) ([]models.Classification, error)
	GetByParentID(ctx context.Context, parentID *uuid.UUID) ([]models.Classification, error)
}

type classificationRepository struct {
	db *gorm.DB
}

func NewClassificationRepository(db *gorm.DB) ClassificationRepository {
	return &classificationRepository{db: db}
}

func (r *classificationRepository) Create(ctx context.Context, classification *models.Classification) error {
	if classification.ParentID != nil {
		var parent models.Classification
		if err := r.db.WithContext(ctx).First(&parent, "id = ?", classification.ParentID).Error; err != nil {
			return fmt.Errorf("parent classification not found")
		}
		classification.Level = parent.Level + 1
		classification.Path = parent.Path + "/" + classification.ID.String()
	} else {
		classification.Level = 0
		classification.Path = classification.ID.String()
	}
	return r.db.WithContext(ctx).Create(classification).Error
}

func (r *classificationRepository) FindByID(ctx context.Context, id uuid.UUID) (*models.Classification, error) {
	var classification models.Classification
	err := r.db.WithContext(ctx).Preload("Children").First(&classification, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	return &classification, nil
}

func (r *classificationRepository) FindByNameAndParent(ctx context.Context, name string, parentID *uuid.UUID) (*models.Classification, error) {
	var classification models.Classification
	query := r.db.WithContext(ctx).Where("name = ?", name)
	if parentID == nil {
		query = query.Where("parent_id IS NULL")
	} else {
		query = query.Where("parent_id = ?", parentID)
	}
	err := query.First(&classification).Error
	if err != nil {
		return nil, err
	}
	return &classification, nil
}

func (r *classificationRepository) Update(ctx context.Context, classification *models.Classification) error {
	return r.db.WithContext(ctx).Save(classification).Error
}

func (r *classificationRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Delete(&models.Classification{}, "id = ?", id).Error
}

func (r *classificationRepository) List(ctx context.Context) ([]models.Classification, error) {
	var classifications []models.Classification
	err := r.db.WithContext(ctx).Order("sort_order, name").Find(&classifications).Error
	return classifications, err
}

func (r *classificationRepository) ListByType(ctx context.Context, classType string) ([]models.Classification, error) {
	var classifications []models.Classification
	query := r.db.WithContext(ctx)
	if classType != "" {
		// Support 'all' type which matches any type, 'both' matches incident/request
		query = query.Where("type = ? OR type = 'both' OR type = 'all'", classType)
	}
	err := query.Order("sort_order, name").Find(&classifications).Error
	return classifications, err
}

func (r *classificationRepository) GetTree(ctx context.Context) ([]models.Classification, error) {
	var roots []models.Classification
	err := r.db.WithContext(ctx).
		Where("parent_id IS NULL").
		Preload("Children", func(db *gorm.DB) *gorm.DB {
			return db.Order("sort_order, name")
		}).
		Preload("Children.Children", func(db *gorm.DB) *gorm.DB {
			return db.Order("sort_order, name")
		}).
		Preload("Children.Children.Children").
		Order("sort_order, name").
		Find(&roots).Error
	return roots, err
}

func (r *classificationRepository) GetTreeByType(ctx context.Context, classType string) ([]models.Classification, error) {
	var roots []models.Classification
	typeFilter := func(db *gorm.DB) *gorm.DB {
		return db.Where("type = ? OR type = 'both' OR type = 'all'", classType).Order("sort_order, name")
	}
	err := r.db.WithContext(ctx).
		Where("parent_id IS NULL").
		Where("type = ? OR type = 'both' OR type = 'all'", classType).
		Preload("Children", typeFilter).
		Preload("Children.Children", typeFilter).
		Preload("Children.Children.Children", typeFilter).
		Order("sort_order, name").
		Find(&roots).Error
	return roots, err
}

func (r *classificationRepository) GetChildren(ctx context.Context, parentID uuid.UUID) ([]models.Classification, error) {
	var children []models.Classification
	err := r.db.WithContext(ctx).
		Where("parent_id = ?", parentID).
		Order("sort_order, name").
		Find(&children).Error
	return children, err
}

func (r *classificationRepository) GetByParentID(ctx context.Context, parentID *uuid.UUID) ([]models.Classification, error) {
	var classifications []models.Classification
	query := r.db.WithContext(ctx)
	if parentID == nil {
		query = query.Where("parent_id IS NULL")
	} else {
		query = query.Where("parent_id = ?", parentID)
	}
	err := query.Order("sort_order, name").Find(&classifications).Error
	return classifications, err
}
