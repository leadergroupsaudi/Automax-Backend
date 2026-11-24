package repository

import (
	"context"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type LookupRepository interface {
	// Categories
	CreateCategory(ctx context.Context, category *models.LookupCategory) error
	FindCategoryByID(ctx context.Context, id uuid.UUID) (*models.LookupCategory, error)
	FindCategoryByCode(ctx context.Context, code string) (*models.LookupCategory, error)
	UpdateCategory(ctx context.Context, category *models.LookupCategory) error
	DeleteCategory(ctx context.Context, id uuid.UUID) error
	ListCategories(ctx context.Context) ([]models.LookupCategory, error)

	// Values
	CreateValue(ctx context.Context, value *models.LookupValue) error
	FindValueByID(ctx context.Context, id uuid.UUID) (*models.LookupValue, error)
	UpdateValue(ctx context.Context, value *models.LookupValue) error
	DeleteValue(ctx context.Context, id uuid.UUID) error
	ListValuesByCategory(ctx context.Context, categoryID uuid.UUID) ([]models.LookupValue, error)
	ListValuesByCategoryCode(ctx context.Context, code string) ([]models.LookupValue, error)
	GetDefaultValue(ctx context.Context, categoryCode string) (*models.LookupValue, error)
	ClearDefaultForCategory(ctx context.Context, categoryID uuid.UUID) error
}

type lookupRepository struct {
	db *gorm.DB
}

func NewLookupRepository(db *gorm.DB) LookupRepository {
	return &lookupRepository{db: db}
}

// Category methods

func (r *lookupRepository) CreateCategory(ctx context.Context, category *models.LookupCategory) error {
	return r.db.WithContext(ctx).Create(category).Error
}

func (r *lookupRepository) FindCategoryByID(ctx context.Context, id uuid.UUID) (*models.LookupCategory, error) {
	var category models.LookupCategory
	err := r.db.WithContext(ctx).
		Preload("Values", func(db *gorm.DB) *gorm.DB {
			return db.Order("sort_order ASC, name ASC")
		}).
		First(&category, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	return &category, nil
}

func (r *lookupRepository) FindCategoryByCode(ctx context.Context, code string) (*models.LookupCategory, error) {
	var category models.LookupCategory
	err := r.db.WithContext(ctx).
		Preload("Values", func(db *gorm.DB) *gorm.DB {
			return db.Where("is_active = ?", true).Order("sort_order ASC, name ASC")
		}).
		Where("code = ? AND is_active = ?", code, true).
		First(&category).Error
	if err != nil {
		return nil, err
	}
	return &category, nil
}

func (r *lookupRepository) UpdateCategory(ctx context.Context, category *models.LookupCategory) error {
	return r.db.WithContext(ctx).Save(category).Error
}

func (r *lookupRepository) DeleteCategory(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Delete all values in the category first
		if err := tx.Where("category_id = ?", id).Delete(&models.LookupValue{}).Error; err != nil {
			return err
		}
		// Delete the category
		return tx.Delete(&models.LookupCategory{}, "id = ?", id).Error
	})
}

func (r *lookupRepository) ListCategories(ctx context.Context) ([]models.LookupCategory, error) {
	var categories []models.LookupCategory
	err := r.db.WithContext(ctx).
		Preload("Values", func(db *gorm.DB) *gorm.DB {
			return db.Order("sort_order ASC, name ASC")
		}).
		Order("name ASC").
		Find(&categories).Error
	return categories, err
}

// Value methods

func (r *lookupRepository) CreateValue(ctx context.Context, value *models.LookupValue) error {
	return r.db.WithContext(ctx).Create(value).Error
}

func (r *lookupRepository) FindValueByID(ctx context.Context, id uuid.UUID) (*models.LookupValue, error) {
	var value models.LookupValue
	err := r.db.WithContext(ctx).
		Preload("Category").
		First(&value, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func (r *lookupRepository) UpdateValue(ctx context.Context, value *models.LookupValue) error {
	return r.db.WithContext(ctx).Save(value).Error
}

func (r *lookupRepository) DeleteValue(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Delete(&models.LookupValue{}, "id = ?", id).Error
}

func (r *lookupRepository) ListValuesByCategory(ctx context.Context, categoryID uuid.UUID) ([]models.LookupValue, error) {
	var values []models.LookupValue
	err := r.db.WithContext(ctx).
		Where("category_id = ?", categoryID).
		Order("sort_order ASC, name ASC").
		Find(&values).Error
	return values, err
}

func (r *lookupRepository) ListValuesByCategoryCode(ctx context.Context, code string) ([]models.LookupValue, error) {
	var values []models.LookupValue
	err := r.db.WithContext(ctx).
		Joins("JOIN lookup_categories ON lookup_categories.id = lookup_values.category_id").
		Where("UPPER(lookup_categories.code) = UPPER(?) AND lookup_categories.deleted_at IS NULL AND lookup_categories.is_active = ? AND lookup_values.is_active = ?", code, true, true).
		Order("lookup_values.sort_order ASC, lookup_values.name ASC").
		Find(&values).Error
	return values, err
}

func (r *lookupRepository) GetDefaultValue(ctx context.Context, categoryCode string) (*models.LookupValue, error) {
	var value models.LookupValue
	err := r.db.WithContext(ctx).
		Joins("JOIN lookup_categories ON lookup_categories.id = lookup_values.category_id").
		Where("lookup_categories.code = ? AND lookup_values.is_default = ? AND lookup_values.is_active = ?", categoryCode, true, true).
		First(&value).Error
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func (r *lookupRepository) ClearDefaultForCategory(ctx context.Context, categoryID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Model(&models.LookupValue{}).
		Where("category_id = ?", categoryID).
		Update("is_default", false).Error
}
