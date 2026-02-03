package repository

import (
	"context"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type UserRepository interface {
	Create(ctx context.Context, user *models.User) error
	FindByID(ctx context.Context, id uuid.UUID) (*models.User, error)
	FindByIDWithRelations(ctx context.Context, id uuid.UUID) (*models.User, error)
	FindByIDWithPermissions(ctx context.Context, id uuid.UUID) (*models.User, error)
	FindByEmail(ctx context.Context, email string) (*models.User, error)
	FindByEmailWithRelations(ctx context.Context, email string) (*models.User, error)
	FindByUsername(ctx context.Context, username string) (*models.User, error)
	Update(ctx context.Context, user *models.User) error
	Delete(ctx context.Context, id uuid.UUID) error
	List(ctx context.Context, page, limit int) ([]models.User, int64, error)
	ListByDepartment(ctx context.Context, departmentID uuid.UUID, page, limit int) ([]models.User, int64, error)
	ExistsByEmail(ctx context.Context, email string) (bool, error)
	ExistsByUsername(ctx context.Context, username string) (bool, error)
	AssignRoles(ctx context.Context, userID uuid.UUID, roleIDs []uuid.UUID) error
	AssignDepartments(ctx context.Context, userID uuid.UUID, departmentIDs []uuid.UUID) error
	AssignLocations(ctx context.Context, userID uuid.UUID, locationIDs []uuid.UUID) error
	AssignClassifications(ctx context.Context, userID uuid.UUID, classificationIDs []uuid.UUID) error
	GetUserRoles(ctx context.Context, userID uuid.UUID) ([]models.Role, error)
	GetUserPermissions(ctx context.Context, userID uuid.UUID) ([]string, error)
	FindMatching(ctx context.Context, roleID, classificationID, locationID, departmentID, excludeUserID *uuid.UUID) ([]models.User, error)

	FindByExtension(ctx context.Context, extension string) (*models.User, error)
}

type userRepository struct {
	db *gorm.DB
}

func NewUserRepository(db *gorm.DB) UserRepository {
	return &userRepository{db: db}
}

func (r *userRepository) Create(ctx context.Context, user *models.User) error {
	return r.db.WithContext(ctx).Create(user).Error
}

func (r *userRepository) FindByID(ctx context.Context, id uuid.UUID) (*models.User, error) {
	var user models.User
	err := r.db.WithContext(ctx).First(&user, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *userRepository) FindByIDWithRelations(ctx context.Context, id uuid.UUID) (*models.User, error) {
	var user models.User
	err := r.db.WithContext(ctx).
		Preload("Department").
		Preload("Location").
		Preload("Departments").
		Preload("Locations").
		Preload("Classifications").
		Preload("Roles").
		Preload("Roles.Permissions").
		First(&user, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// FindByIDWithPermissions loads only roles and permissions for permission checking
func (r *userRepository) FindByIDWithPermissions(ctx context.Context, id uuid.UUID) (*models.User, error) {
	var user models.User
	err := r.db.WithContext(ctx).
		Preload("Roles", "is_active = ?", true).
		Preload("Roles.Permissions", "is_active = ?", true).
		First(&user, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *userRepository) FindByEmail(ctx context.Context, email string) (*models.User, error) {
	var user models.User
	err := r.db.WithContext(ctx).First(&user, "email = ?", email).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *userRepository) FindByEmailWithRelations(ctx context.Context, email string) (*models.User, error) {
	var user models.User
	err := r.db.WithContext(ctx).
		Preload("Department").
		Preload("Location").
		Preload("Departments").
		Preload("Locations").
		Preload("Classifications").
		Preload("Roles").
		Preload("Roles.Permissions").
		First(&user, "email = ?", email).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *userRepository) FindByUsername(ctx context.Context, username string) (*models.User, error) {
	var user models.User
	err := r.db.WithContext(ctx).First(&user, "username = ?", username).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *userRepository) Update(ctx context.Context, user *models.User) error {
	return r.db.WithContext(ctx).Save(user).Error
}

func (r *userRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Delete(&models.User{}, "id = ?", id).Error
}

func (r *userRepository) List(ctx context.Context, page, limit int) ([]models.User, int64, error) {
	var users []models.User
	var total int64

	offset := (page - 1) * limit

	err := r.db.WithContext(ctx).Model(&models.User{}).Count(&total).Error
	if err != nil {
		return nil, 0, err
	}

	err = r.db.WithContext(ctx).
		Preload("Department").
		Preload("Location").
		Preload("Departments").
		Preload("Locations").
		Preload("Classifications").
		Preload("Roles").
		Offset(offset).
		Limit(limit).
		Order("created_at DESC").
		Find(&users).Error
	if err != nil {
		return nil, 0, err
	}

	return users, total, nil
}

func (r *userRepository) ListByDepartment(ctx context.Context, departmentID uuid.UUID, page, limit int) ([]models.User, int64, error) {
	var users []models.User
	var total int64

	offset := (page - 1) * limit

	err := r.db.WithContext(ctx).Model(&models.User{}).Where("department_id = ?", departmentID).Count(&total).Error
	if err != nil {
		return nil, 0, err
	}

	err = r.db.WithContext(ctx).
		Preload("Department").
		Preload("Location").
		Preload("Roles").
		Where("department_id = ?", departmentID).
		Offset(offset).
		Limit(limit).
		Order("created_at DESC").
		Find(&users).Error
	if err != nil {
		return nil, 0, err
	}

	return users, total, nil
}

func (r *userRepository) ExistsByEmail(ctx context.Context, email string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&models.User{}).Where("email = ?", email).Count(&count).Error
	return count > 0, err
}

func (r *userRepository) ExistsByUsername(ctx context.Context, username string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&models.User{}).Where("username = ?", username).Count(&count).Error
	return count > 0, err
}

func (r *userRepository) AssignRoles(ctx context.Context, userID uuid.UUID, roleIDs []uuid.UUID) error {
	var user models.User
	if err := r.db.WithContext(ctx).First(&user, "id = ?", userID).Error; err != nil {
		return err
	}

	var roles []models.Role
	if len(roleIDs) > 0 {
		if err := r.db.WithContext(ctx).Where("id IN ?", roleIDs).Find(&roles).Error; err != nil {
			return err
		}
	}

	return r.db.WithContext(ctx).Model(&user).Association("Roles").Replace(roles)
}

func (r *userRepository) AssignDepartments(ctx context.Context, userID uuid.UUID, departmentIDs []uuid.UUID) error {
	var user models.User
	if err := r.db.WithContext(ctx).First(&user, "id = ?", userID).Error; err != nil {
		return err
	}

	var departments []models.Department
	if len(departmentIDs) > 0 {
		if err := r.db.WithContext(ctx).Where("id IN ?", departmentIDs).Find(&departments).Error; err != nil {
			return err
		}
	}

	return r.db.WithContext(ctx).Model(&user).Association("Departments").Replace(departments)
}

func (r *userRepository) AssignLocations(ctx context.Context, userID uuid.UUID, locationIDs []uuid.UUID) error {
	var user models.User
	if err := r.db.WithContext(ctx).First(&user, "id = ?", userID).Error; err != nil {
		return err
	}

	var locations []models.Location
	if len(locationIDs) > 0 {
		if err := r.db.WithContext(ctx).Where("id IN ?", locationIDs).Find(&locations).Error; err != nil {
			return err
		}
	}

	return r.db.WithContext(ctx).Model(&user).Association("Locations").Replace(locations)
}

func (r *userRepository) AssignClassifications(ctx context.Context, userID uuid.UUID, classificationIDs []uuid.UUID) error {
	var user models.User
	if err := r.db.WithContext(ctx).First(&user, "id = ?", userID).Error; err != nil {
		return err
	}

	var classifications []models.Classification
	if len(classificationIDs) > 0 {
		if err := r.db.WithContext(ctx).Where("id IN ?", classificationIDs).Find(&classifications).Error; err != nil {
			return err
		}
	}

	return r.db.WithContext(ctx).Model(&user).Association("Classifications").Replace(classifications)
}

func (r *userRepository) GetUserRoles(ctx context.Context, userID uuid.UUID) ([]models.Role, error) {
	var user models.User
	if err := r.db.WithContext(ctx).Preload("Roles").Preload("Roles.Permissions").First(&user, "id = ?", userID).Error; err != nil {
		return nil, err
	}
	return user.Roles, nil
}

func (r *userRepository) GetUserPermissions(ctx context.Context, userID uuid.UUID) ([]string, error) {
	var user models.User
	if err := r.db.WithContext(ctx).Preload("Roles").Preload("Roles.Permissions").First(&user, "id = ?", userID).Error; err != nil {
		return nil, err
	}
	return user.GetPermissions(), nil
}

// FindMatching returns users that match the given criteria
func (r *userRepository) FindMatching(ctx context.Context, roleID, classificationID, locationID, departmentID, excludeUserID *uuid.UUID) ([]models.User, error) {
	var users []models.User

	query := r.db.WithContext(ctx).
		Preload("Department").
		Preload("Location").
		Preload("Departments").
		Preload("Locations").
		Preload("Classifications").
		Preload("Roles").
		Where("is_active = ?", true)

	// Exclude specific user if provided (e.g., current assignee)
	if excludeUserID != nil {
		query = query.Where("id != ?", excludeUserID)
	}

	// Filter by role if provided
	if roleID != nil {
		query = query.
			Joins("JOIN user_roles ur ON ur.user_id = users.id").
			Where("ur.role_id = ?", roleID)
	}

	// Filter by classification if provided (user must have the classification in their assigned classifications)
	if classificationID != nil {
		query = query.
			Joins("JOIN user_classifications uc ON uc.user_id = users.id").
			Where("uc.classification_id = ?", classificationID)
	}

	// Filter by location if provided (user must have the location in their assigned locations)
	if locationID != nil {
		query = query.
			Joins("JOIN user_locations ul ON ul.user_id = users.id").
			Where("ul.location_id = ?", locationID)
	}

	// Filter by department if provided (user must have the department in their assigned departments OR primary department)
	if departmentID != nil {
		query = query.
			Joins("LEFT JOIN user_departments ud ON ud.user_id = users.id").
			Where("users.department_id = ? OR ud.department_id = ?", departmentID, departmentID)
	}

	err := query.Distinct().Order("first_name, last_name").Find(&users).Error
	return users, err
}

func (r *userRepository) FindByExtension(ctx context.Context, extension string) (*models.User, error) {
	var user models.User
	err := r.db.WithContext(ctx).Where("extension = ?", extension).First(&user).Error
	return &user, err
}
