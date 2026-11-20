package repository

import (
	"context"
	"errors"
	"log"
	"strings"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/pkg/constants"
	"github.com/automax/backend/pkg/utils"
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
	FindByEmailForLogin(ctx context.Context, email string) (*models.User, error)
	FindByUsername(ctx context.Context, username string) (*models.User, error)
	FindByLast6Digits(ctx context.Context, last6Digits string) (*models.User, error)
	Update(ctx context.Context, user *models.User) error
	UpdateLastLogin(ctx context.Context, id uuid.UUID) error
	Delete(ctx context.Context, id uuid.UUID) error
	List(ctx context.Context, page, limit int, search, phone, extension, callStatus string, roleIDs, departmentIDs, locationIDs, classificationIDs []uuid.UUID, withIncident, withIVR bool, strictDepartment ...bool) ([]models.User, int64, error)
	ListByDepartment(ctx context.Context, departmentID uuid.UUID, page, limit int) ([]models.User, int64, error)
	ExistsByEmail(ctx context.Context, email string) (bool, error)
	ExistsByUsername(ctx context.Context, username string) (bool, error)
	FindExistingIdentifiers(ctx context.Context, emails, usernames, phones []string) ([]models.UserIdentifier, error)
	AssignRoles(ctx context.Context, userID uuid.UUID, roleIDs []uuid.UUID) error
	AssignDepartments(ctx context.Context, userID uuid.UUID, departmentIDs []uuid.UUID) error
	AssignLocations(ctx context.Context, userID uuid.UUID, locationIDs []uuid.UUID) error
	AppendLocations(ctx context.Context, userID uuid.UUID, locationIDs []uuid.UUID) error
	AssignClassifications(ctx context.Context, userID uuid.UUID, classificationIDs []uuid.UUID) error
	AppendClassifications(ctx context.Context, userID uuid.UUID, classificationIDs []uuid.UUID) error
	GetUserRoles(ctx context.Context, userID uuid.UUID) ([]models.Role, error)
	GetUserPermissions(ctx context.Context, userID uuid.UUID) ([]string, error)
	FindMatching(ctx context.Context, roleIDs []uuid.UUID, classificationID, locationID, departmentID, excludeUserID *uuid.UUID) ([]models.User, error)
	FindMatchingOnline(ctx context.Context, roleIDs []uuid.UUID, classificationID, locationID, departmentID, excludeUserID *uuid.UUID) ([]models.User, error)

	FindByExtension(ctx context.Context, extension string) (*models.User, error)
	FindByExtOrPhone(ctx context.Context, phone string) (*models.User, error)
	FindByExtOrPhoneList(ctx context.Context, phones []string) ([]models.User, error)
	FindByMobile(ctx context.Context, phone string) (*models.User, error)
	FindByPhoneWithRelations(ctx context.Context, phone string) (*models.User, error)
	FindByPhoneForLogin(ctx context.Context, phone string) (*models.User, error)
	FindByIDs(ctx context.Context, ids []uuid.UUID) ([]models.User, error)
	// FindByIDsWithAssignments returns users by ID with Locations and Classifications preloaded,
	// for callers that need to filter/route by a user's own location+classification scope.
	FindByIDsWithAssignments(ctx context.Context, ids []uuid.UUID) ([]models.User, error)
	FindByRoleAndContext(ctx context.Context, roleIDs []uuid.UUID, classificationID, locationID, departmentID *uuid.UUID) ([]models.User, error)
	// FindByDepartmentAndRole returns active users belonging to departmentID AND holding roleID.
	// Either filter can be nil to skip it.
	FindByDepartmentAndRole(ctx context.Context, departmentID, roleID *uuid.UUID) ([]models.User, error)
	FindByDepartmentID(ctx context.Context, departmentID uuid.UUID) ([]models.User, error)
	UpdateProfile(ctx context.Context, user map[string]interface{}) error
	FindByPermissionCode(ctx context.Context, permissionCode string) ([]models.User, error)
	IsUserOnline(ctx context.Context, userID uuid.UUID) (bool, error)
	ExistsByPhone(ctx context.Context, phone string, excludeUserID uuid.UUID) (bool, error)
	ExistsByNationalID(ctx context.Context, nationalID string) (bool, error)
	FindByNationalIDForLogin(ctx context.Context, nationalID string) (*models.User, error)
	ExistsByPhoneAndName(ctx context.Context, phone string, name ...string) (bool, error)
	CountAssignedIncidents(ctx context.Context, userID uuid.UUID) (int64, error)
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
	err := r.db.WithContext(ctx).Preload("CurrentExtension").First(&user, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *userRepository) FindByIDWithRelations(ctx context.Context, id uuid.UUID) (*models.User, error) {
	var user models.User
	err := r.db.WithContext(ctx).
		Preload("CurrentExtension").
		Preload("Department").
		Preload("Location").
		Preload("Departments").
		Preload("Locations").
		Preload("Classifications").
		Preload("Roles").
		Preload("Roles.Permissions").
		Preload("DeptManagerDepartment").
		Preload("DeptManagerClassification").
		Preload("DeptManagerLocation").
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
		Preload("Departments").
		First(&user, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *userRepository) FindByEmail(ctx context.Context, email string) (*models.User, error) {
	var user models.User
	err := r.db.WithContext(ctx).Preload("CurrentExtension").First(&user, "email = ?", email).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *userRepository) FindByEmailWithRelations(ctx context.Context, email string) (*models.User, error) {
	var user models.User
	err := r.db.WithContext(ctx).
		Preload("CurrentExtension").
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

// FindByEmailForLogin loads only active roles and their permissions — fast path for login.
func (r *userRepository) FindByEmailForLogin(ctx context.Context, email string) (*models.User, error) {
	var user models.User
	err := r.db.WithContext(ctx).
		Preload("CurrentExtension").
		Preload("Roles", "is_active = ?", true).
		Preload("Roles.Permissions", "is_active = ?", true).
		First(&user, "email = ?", email).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// FindByPhoneForLogin loads only active roles and their permissions — fast path for OTP login.
func (r *userRepository) FindByPhoneForLogin(ctx context.Context, phone string) (*models.User, error) {
	var user models.User
	err := r.db.WithContext(ctx).
		Preload("CurrentExtension").
		Preload("Roles", "is_active = ?", true).
		Preload("Roles.Permissions", "is_active = ?", true).
		First(&user, "phone = ?", phone).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *userRepository) FindByUsername(ctx context.Context, username string) (*models.User, error) {
	var user models.User
	err := r.db.WithContext(ctx).Preload("CurrentExtension").First(&user, "username = ?", username).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// find by mobile

// FindByMobile matches phone against every stored shape of the number (bare, with country
// code, with the national trunk zero, etc. — see utils.MobileMatchVariants), the same way
// incident_repository's reporterPhoneVariants matches reporter_phone.
func (r *userRepository) FindByMobile(ctx context.Context, phone string) (*models.User, error) {
	var user models.User
	err := r.db.WithContext(ctx).First(&user, "phone IN ?", utils.MobileMatchVariants(phone)).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *userRepository) FindByPhoneWithRelations(ctx context.Context, phone string) (*models.User, error) {
	var user models.User
	err := r.db.WithContext(ctx).
		Preload("CurrentExtension").
		Preload("Department").
		Preload("Location").
		Preload("Departments").
		Preload("Locations").
		Preload("Classifications").
		Preload("Roles").
		Preload("Roles.Permissions").
		First(&user, "phone = ?", phone).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *userRepository) FindByLast6Digits(ctx context.Context, last6Digits string) (*models.User, error) {
	var user models.User
	log.Printf("Query start: %s", last6Digits)
	err := r.db.WithContext(ctx).Where("RIGHT(phone, 6) = ? AND is_active = ?", last6Digits, true).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *userRepository) Update(ctx context.Context, user *models.User) error {
	// Use Updates() with specific fields instead of Save() to avoid saving all loaded relations
	// This prevents the "extended protocol limited to 65535 parameters" error
	return r.db.WithContext(ctx).Model(&models.User{}).Where("id = ?", user.ID).Updates(map[string]interface{}{
		"username":                       user.Username,
		"first_name":                     user.FirstName,
		"middle_name":                    user.MiddleName,
		"last_name":                      user.LastName,
		"phone":                          user.Phone,
		"password":                       user.Password,
		"mobile_verified":                user.MobileVerified,
		"is_active":                      user.IsActive,
		"department_id":                  user.DepartmentID,
		"location_id":                    user.LocationID,
		"dept_manager_department_id":     user.DeptManagerDepartmentID,
		"dept_manager_classification_id": user.DeptManagerClassificationID,
		"dept_manager_location_id":       user.DeptManagerLocationID,
		"bypass_login_totp":              user.BypassLoginTotp,
		"enable_login_totp":              user.EnableLoginTotp,
	}).Error
}

func (r *userRepository) UpdateLastLogin(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Model(&models.User{}).Where("id = ?", id).Update("last_login_at", gorm.Expr("NOW()")).Error
}

// update only selected fields not whole user record fro profile updates (e.g., not touching password hash, roles, etc.)
func (r *userRepository) UpdateProfile(ctx context.Context, user map[string]interface{}) error {
	return r.db.WithContext(ctx).Model(&models.User{}).Where("id = ?", user["id"]).Updates(user).Error
}

func (r *userRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Delete(&models.User{}, "id = ?", id).Error
}

func (r *userRepository) List(ctx context.Context, page, limit int, search, phone, extension, callStatus string, roleIDs, departmentIDs, locationIDs, classificationIDs []uuid.UUID, withIncident, withIVR bool, strictDepartment ...bool) ([]models.User, int64, error) {
	var users []models.User
	var total int64

	offset := (page - 1) * limit
	hasFilters := len(roleIDs) > 0 || len(departmentIDs) > 0 || len(locationIDs) > 0 || len(classificationIDs) > 0
	isStrict := len(strictDepartment) > 0 && strictDepartment[0]

	// Build base query with search + join filters.
	// A deleted account is kept with is_active=false, so it stays out of the
	// admin user list, the driver role list, and the unit driver dropdown.
	base := r.db.WithContext(ctx).Model(&models.User{}).Where("users.is_active = ?", true)
	if !withIVR {
		// CreateIncident (incident_service.go) auto-registers a citizen user for
		// unauthenticated channels, generating email as "<sourceSlug>_<phone>_<epoch>@domain"
		// and username as "citizen_<phone>_<epoch>". Match those exact prefixes so these
		// auto-registered accounts stay hidden unless with_ivr=true is requested.
		base = base.Where("LOWER(users.email) NOT LIKE ?", strings.ToLower(constants.INCIDENT_SOURCE.IVR)+"\\_%")
		base = base.Where("LOWER(users.email) NOT LIKE ?", strings.ToLower(constants.INCIDENT_SOURCE.MOBILE)+"\\_%")
		base = base.Where("LOWER(users.email) NOT LIKE ?", strings.ToLower(constants.INCIDENT_SOURCE.WHATSAPP)+"\\_%")
		base = base.Where("LOWER(users.email) NOT LIKE ?", "%chatbot\\_%")
		base = base.Where("LOWER(users.email) NOT LIKE ?", "otp\\_%")
		base = base.Where("LOWER(users.username) NOT LIKE ?", strings.ToLower(constants.ROLES.CITIZEN)+"\\_%")
	}

	if strings.TrimSpace(search) != "" {
		search = strings.TrimSpace(search)
		like := "%" + strings.ToLower(search) + "%"
		phoneLike := strings.TrimLeft(strings.TrimPrefix(search, "+"), "0")
		if phoneLike != "" {
			base = base.Where(
				"LOWER(users.username) LIKE ? OR LOWER(users.email) LIKE ? OR LOWER(users.first_name) LIKE ? OR LOWER(users.last_name) LIKE ? OR users.phone LIKE ?",
				like, like, like, like, "%"+phoneLike+"%",
			)
		} else {
			base = base.Where(
				"LOWER(users.username) LIKE ? OR LOWER(users.email) LIKE ? OR LOWER(users.first_name) LIKE ? OR LOWER(users.last_name) LIKE ?",
				like, like, like, like,
			)
		}
	}

	if strings.TrimSpace(phone) != "" {
		phone = strings.TrimPrefix(strings.TrimSpace(phone), "+")
		phone = strings.TrimLeft(phone, "0")
		if phone != "" {
			base = base.Where("users.phone ILIKE ?", "%"+phone+"%")
		}
	}
	if extension != "" {
		base = base.Where("users.id IN (SELECT user_id FROM extension_assignments WHERE extension LIKE ?)", "%"+extension+"%")
	}
	if callStatus != "" {
		base = base.Where("users.call_status = ?", callStatus)
	}
	if withIncident {
		base = base.Where("users.id IN (SELECT reporter_id FROM incidents WHERE reporter_id IS NOT NULL)")
	}

	if len(roleIDs) > 0 {
		base = base.Joins("JOIN user_roles ur ON ur.user_id = users.id").
			Where("ur.role_id IN ?", roleIDs)
	}
	if len(departmentIDs) > 0 {
		if isStrict {
			base = base.Where("users.department_id IN ?", departmentIDs)
		} else {
			base = base.Joins("LEFT JOIN user_departments ud ON ud.user_id = users.id").
				Where("users.department_id IN ? OR ud.department_id IN ?", departmentIDs, departmentIDs)
		}
	}
	if len(locationIDs) > 0 {
		base = base.Joins("LEFT JOIN user_locations ul ON ul.user_id = users.id").
			Where("users.location_id IN ? OR ul.location_id IN ?", locationIDs, locationIDs)
	}
	if len(classificationIDs) > 0 {
		base = base.Joins("JOIN user_classifications uc ON uc.user_id = users.id").
			Where("uc.classification_id IN ?", classificationIDs)
	}

	if !hasFilters {
		// Simple path — no joins, no duplicates possible
		if err := base.Count(&total).Error; err != nil {
			return nil, 0, err
		}
		err := base.
			Preload("CurrentExtension").
			Preload("Department").
			Preload("Location").
			Preload("Departments").
			Preload("Locations").
			Preload("Classifications").
			Preload("Roles").
			Preload("DeptManagerDepartment").
			Preload("DeptManagerClassification").
			Preload("DeptManagerLocation").
			Offset(offset).Limit(limit).
			Order("users.created_at DESC").
			Find(&users).Error
		return users, total, err
	}

	// Filtered path — joins can produce duplicate rows; deduplicate via GROUP BY.
	// COUNT: wrap a GROUP BY subquery so COUNT(*) counts distinct matched users.
	idSubQuery := base.Select("users.id").Group("users.id")
	if err := r.db.WithContext(ctx).Table("(?) AS sub", idSubQuery).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// Fetch the page of distinct user IDs.
	// GROUP BY users.id deduplicates; no DISTINCT means ORDER BY created_at is allowed.
	var userIDs []uuid.UUID
	if err := base.
		Group("users.id").
		Order("users.created_at DESC").
		Offset(offset).Limit(limit).
		Pluck("users.id", &userIDs).Error; err != nil {
		return nil, 0, err
	}

	if len(userIDs) == 0 {
		return []models.User{}, total, nil
	}

	// Load full user records with all relations, preserving page order.
	if err := r.db.WithContext(ctx).
		Preload("CurrentExtension").
		Preload("Department").
		Preload("Location").
		Preload("Departments").
		Preload("Locations").
		Preload("Classifications").
		Preload("Roles").
		Preload("DeptManagerDepartment").
		Preload("DeptManagerClassification").
		Preload("DeptManagerLocation").
		Where("id IN ?", userIDs).
		Order("created_at DESC").
		Find(&users).Error; err != nil {
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

func (r *userRepository) FindByDepartmentID(ctx context.Context, departmentID uuid.UUID) ([]models.User, error) {
	var userIDs []uuid.UUID

	r.db.WithContext(ctx).Model(&models.User{}).
		Where("department_id = ?", departmentID).
		Pluck("id", &userIDs)

	var m2mIDs []uuid.UUID
	r.db.WithContext(ctx).Table("user_departments").
		Where("department_id = ?", departmentID).
		Pluck("user_id", &m2mIDs)

	idSet := make(map[uuid.UUID]bool)
	for _, id := range userIDs {
		idSet[id] = true
	}
	for _, id := range m2mIDs {
		idSet[id] = true
	}

	mergedIDs := make([]uuid.UUID, 0, len(idSet))
	for id := range idSet {
		mergedIDs = append(mergedIDs, id)
	}

	if len(mergedIDs) == 0 {
		return nil, nil
	}

	var users []models.User
	err := r.db.WithContext(ctx).
		Preload("Departments").
		Preload("Locations").
		Preload("Classifications").
		Preload("Roles").
		Where("id IN ?", mergedIDs).
		Find(&users).Error

	return users, err
}

func (r *userRepository) ExistsByEmail(ctx context.Context, email string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&models.User{}).Where("LOWER(email) = LOWER(?)", email).Count(&count).Error
	return count > 0, err
}

func (r *userRepository) ExistsByUsername(ctx context.Context, username string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&models.User{}).Where("username = ?", username).Count(&count).Error
	return count > 0, err
}

// FindExistingIdentifiers returns the email/username/phone of every user matching any of the supplied
// values. One query, three columns, no preloads - built for bulk-import duplicate pre-checks.
// Emails are compared case-insensitively, like ExistsByEmail, so pass lower-cased emails.
func (r *userRepository) FindExistingIdentifiers(ctx context.Context, emails, usernames, phones []string) ([]models.UserIdentifier, error) {
	if len(emails) == 0 && len(usernames) == 0 && len(phones) == 0 {
		return []models.UserIdentifier{}, nil
	}

	conditions := make([]string, 0, 3)
	args := make([]interface{}, 0, 3)
	if len(emails) > 0 {
		conditions = append(conditions, "LOWER(email) IN ?")
		args = append(args, emails)
	}
	if len(usernames) > 0 {
		conditions = append(conditions, "username IN ?")
		args = append(args, usernames)
	}
	if len(phones) > 0 {
		conditions = append(conditions, "phone IN ?")
		args = append(args, phones)
	}

	var identifiers []models.UserIdentifier
	err := r.db.WithContext(ctx).
		Model(&models.User{}).
		Select("email", "username", "phone").
		Where(strings.Join(conditions, " OR "), args...).
		Find(&identifiers).Error
	return identifiers, err
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

func (r *userRepository) AppendLocations(ctx context.Context, userID uuid.UUID, locationIDs []uuid.UUID) error {
	var user models.User
	if err := r.db.WithContext(ctx).First(&user, "id = ?", userID).Error; err != nil {
		return err
	}

	if len(locationIDs) == 0 {
		return nil
	}

	// Get existing locations
	var existingLocations []models.Location
	if err := r.db.WithContext(ctx).Model(&user).Association("Locations").Find(&existingLocations); err != nil {
		return err
	}

	// Get new locations to add
	var newLocations []models.Location
	if err := r.db.WithContext(ctx).Where("id IN ?", locationIDs).Find(&newLocations).Error; err != nil {
		return err
	}

	// Create a map of existing location IDs to avoid duplicates
	existingMap := make(map[uuid.UUID]bool)
	for _, loc := range existingLocations {
		existingMap[loc.ID] = true
	}

	// Only add locations that don't already exist
	var toAppend []models.Location
	for _, loc := range newLocations {
		if !existingMap[loc.ID] {
			toAppend = append(toAppend, loc)
		}
	}

	if len(toAppend) > 0 {
		return r.db.WithContext(ctx).Model(&user).Association("Locations").Append(toAppend)
	}

	return nil
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

func (r *userRepository) AppendClassifications(ctx context.Context, userID uuid.UUID, classificationIDs []uuid.UUID) error {
	var user models.User
	if err := r.db.WithContext(ctx).First(&user, "id = ?", userID).Error; err != nil {
		return err
	}

	if len(classificationIDs) == 0 {
		return nil
	}

	// Get existing classifications
	var existingClassifications []models.Classification
	if err := r.db.WithContext(ctx).Model(&user).Association("Classifications").Find(&existingClassifications); err != nil {
		return err
	}

	// Get new classifications to add
	var newClassifications []models.Classification
	if err := r.db.WithContext(ctx).Where("id IN ?", classificationIDs).Find(&newClassifications).Error; err != nil {
		return err
	}

	// Create a map of existing classification IDs to avoid duplicates
	existingMap := make(map[uuid.UUID]bool)
	for _, cls := range existingClassifications {
		existingMap[cls.ID] = true
	}

	// Only add classifications that don't already exist
	var toAppend []models.Classification
	for _, cls := range newClassifications {
		if !existingMap[cls.ID] {
			toAppend = append(toAppend, cls)
		}
	}

	if len(toAppend) > 0 {
		return r.db.WithContext(ctx).Model(&user).Association("Classifications").Append(toAppend)
	}

	return nil
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
func (r *userRepository) FindMatching(ctx context.Context, roleIDs []uuid.UUID, classificationID, locationID, departmentID, excludeUserID *uuid.UUID) ([]models.User, error) {
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

	// Filter by roles if provided (user must have at least one of the given roles)
	if len(roleIDs) > 0 {
		query = query.
			Joins("JOIN user_roles ur ON ur.user_id = users.id").
			Where("ur.role_id IN ?", roleIDs)
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

func (r *userRepository) FindMatchingOnline(ctx context.Context, roleIDs []uuid.UUID, classificationID, locationID, departmentID, excludeUserID *uuid.UUID) ([]models.User, error) {
	var users []models.User

	query := r.db.WithContext(ctx).
		Preload("Department").
		Preload("Location").
		Preload("Departments").
		Preload("Locations").
		Preload("Classifications").
		Preload("Roles").
		Where("is_active = ?", true).
		Where("call_status = ?", models.CallStatusOnline)

	if excludeUserID != nil {
		query = query.Where("id != ?", excludeUserID)
	}

	if len(roleIDs) > 0 {
		query = query.
			Joins("JOIN user_roles ur ON ur.user_id = users.id").
			Where("ur.role_id IN ?", roleIDs)
	}

	if classificationID != nil {
		query = query.
			Joins("JOIN user_classifications uc ON uc.user_id = users.id").
			Where("uc.classification_id = ?", classificationID)
	}

	if locationID != nil {
		query = query.
			Joins("JOIN user_locations ul ON ul.user_id = users.id").
			Where("ul.location_id = ?", locationID)
	}

	if departmentID != nil {
		query = query.
			Joins("LEFT JOIN user_departments ud ON ud.user_id = users.id").
			Where("users.department_id = ? OR ud.department_id = ?", departmentID, departmentID)
	}

	err := query.Distinct().Order("first_name, last_name").Find(&users).Error
	return users, err
}

func (r *userRepository) IsUserOnline(ctx context.Context, userID uuid.UUID) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&models.User{}).
		Where("id = ? AND is_active = ? AND call_status = ?", userID, true, models.CallStatusOnline).
		Count(&count).Error
	return count > 0, err
}

// FindByExtension resolves the user currently holding an extension via the
// extension_assignments table (extension is no longer a column on users).
func (r *userRepository) FindByExtension(ctx context.Context, extension string) (*models.User, error) {
	var user models.User
	err := r.db.WithContext(ctx).
		Preload("CurrentExtension").
		Where("users.id IN (SELECT user_id FROM extension_assignments WHERE extension = ?)", extension).
		First(&user).Error
	return &user, err
}

func (r *userRepository) FindByExtOrPhone(ctx context.Context, phone string) (*models.User, error) {
	var user models.User
	err := r.db.WithContext(ctx).
		Preload("CurrentExtension").
		Where("users.phone = ? OR users.id IN (SELECT user_id FROM extension_assignments WHERE extension = ?)", phone, phone).
		First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *userRepository) FindByExtOrPhoneList(ctx context.Context, phones []string) ([]models.User, error) {
	if len(phones) == 0 {
		return []models.User{}, nil
	}
	var users []models.User
	err := r.db.WithContext(ctx).
		Preload("CurrentExtension").
		Where("users.phone IN ? OR users.id IN (SELECT user_id FROM extension_assignments WHERE extension IN ?)", phones, phones).
		Find(&users).Error
	return users, err
}

func (r *userRepository) FindByIDs(ctx context.Context, ids []uuid.UUID) ([]models.User, error) {
	if len(ids) == 0 {
		return []models.User{}, nil
	}

	var users []models.User
	err := r.db.WithContext(ctx).Where("id IN ?", ids).Find(&users).Error
	return users, err
}

func (r *userRepository) FindByIDsWithAssignments(ctx context.Context, ids []uuid.UUID) ([]models.User, error) {
	if len(ids) == 0 {
		return []models.User{}, nil
	}

	var users []models.User
	err := r.db.WithContext(ctx).
		Preload("Locations").
		Preload("Classifications").
		Where("id IN ?", ids).
		Find(&users).Error
	return users, err
}

// FindByPermissionCode returns active users who hold at least one role that has the given permission code.
func (r *userRepository) FindByPermissionCode(ctx context.Context, permissionCode string) ([]models.User, error) {
	var userIDs []uuid.UUID
	err := r.db.WithContext(ctx).
		Model(&models.User{}).
		Joins("JOIN user_roles ON user_roles.user_id = users.id").
		Joins("JOIN role_permissions ON role_permissions.role_id = user_roles.role_id").
		Joins("JOIN permissions ON permissions.id = role_permissions.permission_id").
		Where("permissions.code = ? AND permissions.is_active = ? AND users.is_active = ?", permissionCode, true, true).
		Group("users.id").
		Pluck("users.id", &userIDs).Error
	if err != nil {
		return nil, err
	}
	if len(userIDs) == 0 {
		return []models.User{}, nil
	}
	var users []models.User
	err = r.db.WithContext(ctx).Where("id IN ?", userIDs).Find(&users).Error
	return users, err
}

// FindByDepartmentAndRole returns active users that belong to departmentID AND hold roleID.
// Either parameter can be nil to skip that filter.
func (r *userRepository) FindByDepartmentAndRole(ctx context.Context, departmentID, roleID *uuid.UUID) ([]models.User, error) {
	query := r.db.WithContext(ctx).
		Model(&models.User{}).
		Where("users.is_active = ?", true)

	if departmentID != nil {
		query = query.
			Joins("JOIN user_departments ON user_departments.user_id = users.id").
			Where("user_departments.department_id = ?", *departmentID)
	}
	if roleID != nil {
		query = query.
			Joins("JOIN user_roles ON user_roles.user_id = users.id").
			Where("user_roles.role_id = ?", *roleID)
	}

	var userIDs []uuid.UUID
	if err := query.Group("users.id").Pluck("users.id", &userIDs).Error; err != nil {
		return nil, err
	}
	if len(userIDs) == 0 {
		return []models.User{}, nil
	}
	var users []models.User
	err := r.db.WithContext(ctx).Where("id IN ?", userIDs).Find(&users).Error
	return users, err
}

// FindByRoleAndContext finds active users that hold at least one of the given roles and

func (r *userRepository) FindByRoleAndContext(ctx context.Context, roleIDs []uuid.UUID, classificationID, locationID, departmentID *uuid.UUID) ([]models.User, error) {
	if len(roleIDs) == 0 {
		return []models.User{}, nil
	}

	idQuery := r.db.WithContext(ctx).
		Model(&models.User{}).
		Joins("JOIN user_roles ON user_roles.user_id = users.id").
		Where("user_roles.role_id IN ?", roleIDs).
		Where("users.is_active = ?", true)

	if departmentID != nil {
		idQuery = idQuery.
			Joins("JOIN user_departments ON user_departments.user_id = users.id").
			Where("user_departments.department_id = ?", *departmentID)
	}
	if classificationID != nil {
		idQuery = idQuery.
			Joins("JOIN user_classifications ON user_classifications.user_id = users.id").
			Where("user_classifications.classification_id = ?", *classificationID)
	}
	if locationID != nil {
		idQuery = idQuery.
			Joins("JOIN user_locations ON user_locations.user_id = users.id").
			Where("user_locations.location_id = ?", *locationID)
	}

	// GROUP BY deduplicates users that match through multiple joined rows.
	var userIDs []uuid.UUID
	if err := idQuery.Group("users.id").Pluck("users.id", &userIDs).Error; err != nil {
		return nil, err
	}
	if len(userIDs) == 0 {
		return []models.User{}, nil
	}

	// fetch full user records (email, phone, name, etc.)
	var users []models.User
	err := r.db.WithContext(ctx).
		Where("id IN ?", userIDs).
		Find(&users).Error
	return users, err
}

func (r *userRepository) ExistsByPhone(ctx context.Context, phone string, excludeUserID uuid.UUID) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&models.User{}).
		Where("phone = ? AND id != ?", phone, excludeUserID).
		Count(&count).Error

	return count > 0, err
}

func (r *userRepository) ExistsByPhoneAndName(ctx context.Context, phone string, name ...string) (bool, error) {
	query := r.db.WithContext(ctx).Model(&models.User{})

	query = query.Where("phone = ?", phone)

	if len(name) > 0 && name[0] != "" {
		query = query.Where("username = ?", name[0])
	}

	// Use Select("1") + Limit(1) instead of Count — faster, stops at first match
	var user models.User
	err := query.Select("id").Limit(1).
		Where("is_active = ?", true).
		Where("deleted_at IS NULL").
		First(&user).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}

	return true, nil
}

func (r *userRepository) ExistsByNationalID(ctx context.Context, nationalID string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&models.User{}).Where("national_id = ?", nationalID).Count(&count).Error
	return count > 0, err
}

// CountAssignedIncidents returns how many live (non-deleted) incidents the user is
// currently assigned to — either as the single assignee (incidents.assignee_id) or via
// the many-to-many incident_assignees join. Used to block deletion of an assigned user.
func (r *userRepository) CountAssignedIncidents(ctx context.Context, userID uuid.UUID) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&models.Incident{}).
		Where("assignee_id = ? OR id IN (SELECT incident_id FROM incident_assignees WHERE user_id = ?)", userID, userID).
		Count(&count).Error
	return count, err
}

func (r *userRepository) FindByNationalIDForLogin(ctx context.Context, nationalID string) (*models.User, error) {
	var user models.User
	err := r.db.WithContext(ctx).
		Preload("Roles", "is_active = ?", true).
		Preload("Roles.Permissions", "is_active = ?", true).
		First(&user, "national_id = ?", nationalID).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}
