package repository

import (
	"context"
	"fmt"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type DepartmentRepository interface {
	Create(ctx context.Context, department *models.Department) error
	FindByID(ctx context.Context, id uuid.UUID) (*models.Department, error)
	FindByCode(ctx context.Context, code string) (*models.Department, error)
	FindByNameAndParent(ctx context.Context, name string, parentID *uuid.UUID) (*models.Department, error)
	Update(ctx context.Context, department *models.Department) error
	Delete(ctx context.Context, id uuid.UUID) error
	List(ctx context.Context) ([]models.Department, error)
	GetTree(ctx context.Context) ([]models.Department, error)
	GetChildren(ctx context.Context, parentID uuid.UUID) ([]models.Department, error)
	GetByParentID(ctx context.Context, parentID *uuid.UUID) ([]models.Department, error)
	AssignLocations(ctx context.Context, departmentID uuid.UUID, locationIDs []uuid.UUID) error
	AssignClassifications(ctx context.Context, departmentID uuid.UUID, classificationIDs []uuid.UUID) error
	AssignRoles(ctx context.Context, departmentID uuid.UUID, roleIDs []uuid.UUID) error
	FindMatching(ctx context.Context, classificationID, locationID *uuid.UUID) ([]models.Department, error)
}

type departmentRepository struct {
	db *gorm.DB
}

func NewDepartmentRepository(db *gorm.DB) DepartmentRepository {
	return &departmentRepository{db: db}
}

func (r *departmentRepository) Create(ctx context.Context, department *models.Department) error {
	if department.ParentID != nil {
		var parent models.Department
		if err := r.db.WithContext(ctx).First(&parent, "id = ?", department.ParentID).Error; err != nil {
			return fmt.Errorf("parent department not found")
		}
		department.Level = parent.Level + 1
		department.Path = parent.Path + "/" + department.ID.String()
	} else {
		department.Level = 0
		department.Path = department.ID.String()
	}
	return r.db.WithContext(ctx).Create(department).Error
}

func (r *departmentRepository) FindByID(ctx context.Context, id uuid.UUID) (*models.Department, error) {
	var department models.Department
	err := r.db.WithContext(ctx).
		Preload("Children").
		Preload("Locations").
		Preload("Classifications").
		Preload("Roles").
		First(&department, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	return &department, nil
}

func (r *departmentRepository) FindByCode(ctx context.Context, code string) (*models.Department, error) {
	var department models.Department
	err := r.db.WithContext(ctx).First(&department, "code = ?", code).Error
	if err != nil {
		return nil, err
	}
	return &department, nil
}

func (r *departmentRepository) FindByNameAndParent(ctx context.Context, name string, parentID *uuid.UUID) (*models.Department, error) {
	var department models.Department
	query := r.db.WithContext(ctx).Where("name = ?", name)
	if parentID == nil {
		query = query.Where("parent_id IS NULL")
	} else {
		query = query.Where("parent_id = ?", parentID)
	}
	err := query.First(&department).Error
	if err != nil {
		return nil, err
	}
	return &department, nil
}

func (r *departmentRepository) Update(ctx context.Context, department *models.Department) error {
	return r.db.WithContext(ctx).Save(department).Error
}

func (r *departmentRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Delete(&models.Department{}, "id = ?", id).Error
}

func (r *departmentRepository) List(ctx context.Context) ([]models.Department, error) {
	var departments []models.Department
	err := r.db.WithContext(ctx).
		Preload("Locations").
		Preload("Classifications").
		Preload("Roles").
		Order("sort_order, name").
		Find(&departments).Error
	return departments, err
}

func (r *departmentRepository) GetTree(ctx context.Context) ([]models.Department, error) {
	var roots []models.Department
	err := r.db.WithContext(ctx).
		Where("parent_id IS NULL").
		Preload("Children", func(db *gorm.DB) *gorm.DB {
			return db.Order("sort_order, name")
		}).
		Preload("Children.Children", func(db *gorm.DB) *gorm.DB {
			return db.Order("sort_order, name")
		}).
		Preload("Children.Children.Children").
		Preload("Locations").
		Preload("Classifications").
		Preload("Roles").
		Order("sort_order, name").
		Find(&roots).Error
	return roots, err
}

func (r *departmentRepository) GetChildren(ctx context.Context, parentID uuid.UUID) ([]models.Department, error) {
	var children []models.Department
	err := r.db.WithContext(ctx).
		Where("parent_id = ?", parentID).
		Order("sort_order, name").
		Find(&children).Error
	return children, err
}

func (r *departmentRepository) GetByParentID(ctx context.Context, parentID *uuid.UUID) ([]models.Department, error) {
	var departments []models.Department
	query := r.db.WithContext(ctx)
	if parentID == nil {
		query = query.Where("parent_id IS NULL")
	} else {
		query = query.Where("parent_id = ?", parentID)
	}
	err := query.Order("sort_order, name").Find(&departments).Error
	return departments, err
}

func (r *departmentRepository) AssignLocations(ctx context.Context, departmentID uuid.UUID, locationIDs []uuid.UUID) error {
	var department models.Department
	if err := r.db.WithContext(ctx).First(&department, "id = ?", departmentID).Error; err != nil {
		return err
	}

	var locations []models.Location
	if len(locationIDs) > 0 {
		if err := r.db.WithContext(ctx).Where("id IN ?", locationIDs).Find(&locations).Error; err != nil {
			return err
		}
	}

	return r.db.WithContext(ctx).Model(&department).Association("Locations").Replace(locations)
}

func (r *departmentRepository) AssignClassifications(ctx context.Context, departmentID uuid.UUID, classificationIDs []uuid.UUID) error {
	var department models.Department
	if err := r.db.WithContext(ctx).First(&department, "id = ?", departmentID).Error; err != nil {
		return err
	}

	var classifications []models.Classification
	if len(classificationIDs) > 0 {
		if err := r.db.WithContext(ctx).Where("id IN ?", classificationIDs).Find(&classifications).Error; err != nil {
			return err
		}
	}

	return r.db.WithContext(ctx).Model(&department).Association("Classifications").Replace(classifications)
}

func (r *departmentRepository) AssignRoles(ctx context.Context, departmentID uuid.UUID, roleIDs []uuid.UUID) error {
	var department models.Department
	if err := r.db.WithContext(ctx).First(&department, "id = ?", departmentID).Error; err != nil {
		return err
	}

	var roles []models.Role
	if len(roleIDs) > 0 {
		if err := r.db.WithContext(ctx).Where("id IN ?", roleIDs).Find(&roles).Error; err != nil {
			return err
		}
	}

	return r.db.WithContext(ctx).Model(&department).Association("Roles").Replace(roles)
}

// FindMatching returns departments that match the given classification and/or location criteria
func (r *departmentRepository) FindMatching(ctx context.Context, classificationID, locationID *uuid.UUID) ([]models.Department, error) {
	var departments []models.Department

	query := r.db.WithContext(ctx).
		Preload("Locations").
		Preload("Classifications").
		Preload("Roles").
		Where("is_active = ?", true)

	// If both classification and location are provided, find departments that have both
	if classificationID != nil && locationID != nil {
		// Find departments that have both the classification AND the location
		query = query.
			Joins("JOIN department_classifications dc ON dc.department_id = departments.id").
			Joins("JOIN department_locations dl ON dl.department_id = departments.id").
			Where("dc.classification_id = ?", classificationID).
			Where("dl.location_id = ?", locationID).
			Distinct()
	} else if classificationID != nil {
		// Only classification provided
		query = query.
			Joins("JOIN department_classifications dc ON dc.department_id = departments.id").
			Where("dc.classification_id = ?", classificationID).
			Distinct()
	} else if locationID != nil {
		// Only location provided
		query = query.
			Joins("JOIN department_locations dl ON dl.department_id = departments.id").
			Where("dl.location_id = ?", locationID).
			Distinct()
	}

	err := query.Order("name").Find(&departments).Error
	return departments, err
}
