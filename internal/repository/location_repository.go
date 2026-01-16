package repository

import (
	"context"
	"fmt"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type LocationRepository interface {
	Create(ctx context.Context, location *models.Location) error
	FindByID(ctx context.Context, id uuid.UUID) (*models.Location, error)
	Update(ctx context.Context, location *models.Location) error
	Delete(ctx context.Context, id uuid.UUID) error
	List(ctx context.Context) ([]models.Location, error)
	GetTree(ctx context.Context) ([]models.Location, error)
	GetChildren(ctx context.Context, parentID uuid.UUID) ([]models.Location, error)
	GetByParentID(ctx context.Context, parentID *uuid.UUID) ([]models.Location, error)
	GetByType(ctx context.Context, locationType string) ([]models.Location, error)
}

type locationRepository struct {
	db *gorm.DB
}

func NewLocationRepository(db *gorm.DB) LocationRepository {
	return &locationRepository{db: db}
}

func (r *locationRepository) Create(ctx context.Context, location *models.Location) error {
	if location.ParentID != nil {
		var parent models.Location
		if err := r.db.WithContext(ctx).First(&parent, "id = ?", location.ParentID).Error; err != nil {
			return fmt.Errorf("parent location not found")
		}
		location.Level = parent.Level + 1
		location.Path = parent.Path + "/" + location.ID.String()
	} else {
		location.Level = 0
		location.Path = location.ID.String()
	}
	return r.db.WithContext(ctx).Create(location).Error
}

func (r *locationRepository) FindByID(ctx context.Context, id uuid.UUID) (*models.Location, error) {
	var location models.Location
	err := r.db.WithContext(ctx).Preload("Children").First(&location, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	return &location, nil
}

func (r *locationRepository) Update(ctx context.Context, location *models.Location) error {
	return r.db.WithContext(ctx).Save(location).Error
}

func (r *locationRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Delete(&models.Location{}, "id = ?", id).Error
}

func (r *locationRepository) List(ctx context.Context) ([]models.Location, error) {
	var locations []models.Location
	err := r.db.WithContext(ctx).Order("sort_order, name").Find(&locations).Error
	return locations, err
}

func (r *locationRepository) GetTree(ctx context.Context) ([]models.Location, error) {
	var roots []models.Location
	err := r.db.WithContext(ctx).
		Where("parent_id IS NULL").
		Preload("Children", func(db *gorm.DB) *gorm.DB {
			return db.Order("sort_order, name")
		}).
		Preload("Children.Children", func(db *gorm.DB) *gorm.DB {
			return db.Order("sort_order, name")
		}).
		Preload("Children.Children.Children").
		Preload("Children.Children.Children.Children").
		Order("sort_order, name").
		Find(&roots).Error
	return roots, err
}

func (r *locationRepository) GetChildren(ctx context.Context, parentID uuid.UUID) ([]models.Location, error) {
	var children []models.Location
	err := r.db.WithContext(ctx).
		Where("parent_id = ?", parentID).
		Order("sort_order, name").
		Find(&children).Error
	return children, err
}

func (r *locationRepository) GetByParentID(ctx context.Context, parentID *uuid.UUID) ([]models.Location, error) {
	var locations []models.Location
	query := r.db.WithContext(ctx)
	if parentID == nil {
		query = query.Where("parent_id IS NULL")
	} else {
		query = query.Where("parent_id = ?", parentID)
	}
	err := query.Order("sort_order, name").Find(&locations).Error
	return locations, err
}

func (r *locationRepository) GetByType(ctx context.Context, locationType string) ([]models.Location, error) {
	var locations []models.Location
	err := r.db.WithContext(ctx).
		Where("type = ?", locationType).
		Order("sort_order, name").
		Find(&locations).Error
	return locations, err
}
