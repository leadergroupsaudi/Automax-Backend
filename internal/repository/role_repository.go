package repository

import (
	"context"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type RoleRepository interface {
	Create(ctx context.Context, role *models.Role) error
	FindByID(ctx context.Context, id uuid.UUID) (*models.Role, error)
	FindByCode(ctx context.Context, code string) (*models.Role, error)
	Update(ctx context.Context, role *models.Role) error
	Delete(ctx context.Context, id uuid.UUID) error
	List(ctx context.Context) ([]models.Role, error)
	AssignPermissions(ctx context.Context, roleID uuid.UUID, permissionIDs []uuid.UUID) error
	GetPermissions(ctx context.Context, roleID uuid.UUID) ([]models.Permission, error)
}

type roleRepository struct {
	db *gorm.DB
}

func NewRoleRepository(db *gorm.DB) RoleRepository {
	return &roleRepository{db: db}
}

func (r *roleRepository) Create(ctx context.Context, role *models.Role) error {
	return r.db.WithContext(ctx).Create(role).Error
}

func (r *roleRepository) FindByID(ctx context.Context, id uuid.UUID) (*models.Role, error) {
	var role models.Role
	err := r.db.WithContext(ctx).Preload("Permissions").First(&role, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	return &role, nil
}

func (r *roleRepository) FindByCode(ctx context.Context, code string) (*models.Role, error) {
	var role models.Role
	err := r.db.WithContext(ctx).Preload("Permissions").First(&role, "code = ?", code).Error
	if err != nil {
		return nil, err
	}
	return &role, nil
}

func (r *roleRepository) Update(ctx context.Context, role *models.Role) error {
	return r.db.WithContext(ctx).Save(role).Error
}

func (r *roleRepository) Delete(ctx context.Context, id uuid.UUID) error {
	var role models.Role
	if err := r.db.WithContext(ctx).First(&role, "id = ?", id).Error; err != nil {
		return err
	}
	if role.IsSystem {
		return gorm.ErrRecordNotFound // Cannot delete system roles
	}
	return r.db.WithContext(ctx).Delete(&models.Role{}, "id = ?", id).Error
}

func (r *roleRepository) List(ctx context.Context) ([]models.Role, error) {
	var roles []models.Role
	err := r.db.WithContext(ctx).Preload("Permissions").Order("name").Find(&roles).Error
	return roles, err
}

func (r *roleRepository) AssignPermissions(ctx context.Context, roleID uuid.UUID, permissionIDs []uuid.UUID) error {
	var role models.Role
	if err := r.db.WithContext(ctx).First(&role, "id = ?", roleID).Error; err != nil {
		return err
	}

	var permissions []models.Permission
	if len(permissionIDs) > 0 {
		if err := r.db.WithContext(ctx).Where("id IN ?", permissionIDs).Find(&permissions).Error; err != nil {
			return err
		}
	}

	return r.db.WithContext(ctx).Model(&role).Association("Permissions").Replace(permissions)
}

func (r *roleRepository) GetPermissions(ctx context.Context, roleID uuid.UUID) ([]models.Permission, error) {
	var role models.Role
	if err := r.db.WithContext(ctx).Preload("Permissions").First(&role, "id = ?", roleID).Error; err != nil {
		return nil, err
	}
	return role.Permissions, nil
}

type PermissionRepository interface {
	Create(ctx context.Context, permission *models.Permission) error
	FindByID(ctx context.Context, id uuid.UUID) (*models.Permission, error)
	FindByCode(ctx context.Context, code string) (*models.Permission, error)
	Update(ctx context.Context, permission *models.Permission) error
	Delete(ctx context.Context, id uuid.UUID) error
	List(ctx context.Context) ([]models.Permission, error)
	ListByModule(ctx context.Context, module string) ([]models.Permission, error)
	GetModules(ctx context.Context) ([]string, error)
}

type permissionRepository struct {
	db *gorm.DB
}

func NewPermissionRepository(db *gorm.DB) PermissionRepository {
	return &permissionRepository{db: db}
}

func (r *permissionRepository) Create(ctx context.Context, permission *models.Permission) error {
	return r.db.WithContext(ctx).Create(permission).Error
}

func (r *permissionRepository) FindByID(ctx context.Context, id uuid.UUID) (*models.Permission, error) {
	var permission models.Permission
	err := r.db.WithContext(ctx).First(&permission, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	return &permission, nil
}

func (r *permissionRepository) FindByCode(ctx context.Context, code string) (*models.Permission, error) {
	var permission models.Permission
	err := r.db.WithContext(ctx).First(&permission, "code = ?", code).Error
	if err != nil {
		return nil, err
	}
	return &permission, nil
}

func (r *permissionRepository) Update(ctx context.Context, permission *models.Permission) error {
	return r.db.WithContext(ctx).Save(permission).Error
}

func (r *permissionRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Delete(&models.Permission{}, "id = ?", id).Error
}

func (r *permissionRepository) List(ctx context.Context) ([]models.Permission, error) {
	var permissions []models.Permission
	err := r.db.WithContext(ctx).Order("module, name").Find(&permissions).Error
	return permissions, err
}

func (r *permissionRepository) ListByModule(ctx context.Context, module string) ([]models.Permission, error) {
	var permissions []models.Permission
	err := r.db.WithContext(ctx).Where("module = ?", module).Order("name").Find(&permissions).Error
	return permissions, err
}

func (r *permissionRepository) GetModules(ctx context.Context) ([]string, error) {
	var modules []string
	err := r.db.WithContext(ctx).Model(&models.Permission{}).Distinct("module").Pluck("module", &modules).Error
	return modules, err
}
