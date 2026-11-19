package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime/multipart"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/automax/backend/internal/config"
	"github.com/automax/backend/internal/database"
	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/internal/storage"
	"github.com/automax/backend/pkg/constants"
	"github.com/redis/go-redis/v9"

	"github.com/automax/backend/pkg/utils"

	intutils "github.com/automax/backend/internal/utils"
	"github.com/automax/backend/pkg/i18n"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ErrUserAssigned is returned when a user cannot be deleted because they are still
// assigned to one or more incidents. Handlers map it to HTTP 409 with a localized message.
var ErrUserAssigned = errors.New("user is currently assigned")

type UserService interface {
	Register(ctx context.Context, req *models.UserRegisterRequest) (*models.AuthResponse, error)
	SSORegister(ctx context.Context, req *models.SSORegisterRequest) (*models.AuthResponse, error)
	Login(ctx context.Context, req *models.UserLoginRequest) (*models.AuthLoginResponse, error)
	SSOLogin(ctx context.Context, req *models.SSOLoginRequest) (*models.AuthLoginResponse, error)
	RefreshToken(ctx context.Context, refreshToken string, rememberMe ...bool) (*models.AuthResponse, error)
	Logout(ctx context.Context) error
	GetProfile(ctx context.Context, userID uuid.UUID) (*models.UserResponse, error)
	UpdateAdminProfile(ctx context.Context, userID uuid.UUID, req *models.UserUpdateRequest) (*models.UserResponse, error)
	ChangePassword(ctx context.Context, userID uuid.UUID, req *models.ChangePasswordRequest) error
	UploadAvatar(ctx context.Context, userID uuid.UUID, file multipart.File, header *multipart.FileHeader) (*models.UserResponse, error)
	UpdateAvatar(ctx context.Context, userID uuid.UUID, avatarURL string) error
	DeleteUser(ctx context.Context) error
	AdminDeleteUser(ctx context.Context, userID uuid.UUID) error
	ListUsers(ctx context.Context, page, limit int, search, phone, extension, callStatus string, roleIDs, departmentIDs, locationIDs, classificationIDs []uuid.UUID, withIncident, withIVR bool, strictDepartment ...bool) ([]models.UserResponse, int64, error)
	GetUserByID(ctx context.Context, userID uuid.UUID) (*models.UserResponse, error)
	GetUserByEmail(ctx context.Context, email string) (*models.User, error)
	GetUserByMobile(ctx context.Context, phone string) (*models.User, error)
	GetUserByUsername(ctx context.Context, username string) (*models.User, error)
	CheckExistingIdentifiers(ctx context.Context, emails, usernames, phones []string) (*models.ExistingUserIdentifiers, error)
	FindMatchingUsers(ctx context.Context, roleIDs []uuid.UUID, classificationID, locationID, departmentID, excludeUserID *uuid.UUID) ([]models.UserResponse, error)
	UpdateUserCallStatus(ctx context.Context, extension string, status string) (interface{}, error)
	FindByExtension(ctx context.Context, extension string) (*models.User, error)
	FindByExtOrPhone(ctx context.Context, phone string) (*models.User, error)
	FindByExtOrPhoneList(ctx context.Context, phones []string) ([]models.User, error)
	UpdateProfile(ctx context.Context, req *models.UserUpdateRequest) (*models.UserResponse, error)
	GenerateTokenViaUserID(ctx context.Context, mobile uuid.UUID) (*models.AuthResponse, error)
	ValidateMobileForLogin(ctx context.Context, phone string) (*models.UserResponse, error)
	SetLicenseService(ls LicenseService, lr repository.LicenseRepository)
	SetSettingsRepo(sr repository.SettingsRepository)
	AdminResetPassword(ctx context.Context, adminID, targetUserID uuid.UUID, newPassword string) error
	ForgotPassword(ctx context.Context, req *models.ForgotPasswordRequest) (string, error)
	VerifyOTPForReset(ctx context.Context, req *models.VerifyOTPRequest) (string, error)
	ResetPassword(ctx context.Context, resetToken, newPassword string) error
	ExistsByPhoneAndName(ctx context.Context, phone string, name ...string) (bool, error)
	FindByIDWithRelations(ctx context.Context, userID uuid.UUID) (*models.User, error)
}

type userService struct {
	userRepo         repository.UserRepository
	departmentRepo   repository.DepartmentRepository
	jwtManager       *utils.JWTManager
	sessionStore     *database.SessionStore
	storage          *storage.MinIOStorage
	config           *config.Config
	actionLogService ActionLogService
	otpService       *OTPService
	licenseService   LicenseService
	licenseRepo      repository.LicenseRepository
	db               *gorm.DB
	redis            *redis.Client
	settingsRepo     repository.SettingsRepository
}

// SetLicenseService injects the license service (post-construction to avoid circular deps).
func (s *userService) SetLicenseService(ls LicenseService, lr repository.LicenseRepository) {
	s.licenseService = ls
	s.licenseRepo = lr
}

// SetSettingsRepo injects the settings repository (post-construction to avoid touching the constructor everywhere it's called).
func (s *userService) SetSettingsRepo(sr repository.SettingsRepository) {
	s.settingsRepo = sr
}

func NewUserService(
	userRepo repository.UserRepository,
	departmentRepo repository.DepartmentRepository,
	jwtManager *utils.JWTManager,
	sessionStore *database.SessionStore,
	storage *storage.MinIOStorage,
	cfg *config.Config,
	actionLogService ActionLogService,
	otpService *OTPService,
	redis *redis.Client,
	db *gorm.DB,
) UserService {
	return &userService{
		userRepo:         userRepo,
		departmentRepo:   departmentRepo,
		jwtManager:       jwtManager,
		sessionStore:     sessionStore,
		storage:          storage,
		config:           cfg,
		actionLogService: actionLogService,
		otpService:       otpService,
		redis:            redis,
		db:               db,
	}
}

func (s *userService) Register(ctx context.Context, req *models.UserRegisterRequest) (*models.AuthResponse, error) {
	// Get the authenticated user (admin creating this user) from context
	actorID, _ := ctx.Value(constants.ContextKeys.UserID).(uuid.UUID)
	ipAddress, _ := ctx.Value(constants.ContextKeys.IP_ADDRESS).(string)
	userAgent, _ := ctx.Value(constants.ContextKeys.USER_AGENT).(string)

	// Normalize email to lowercase to avoid case-sensitivity issues
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))

	if req.Phone != "" {
		exists, err := s.userRepo.ExistsByPhone(ctx, req.Phone, uuid.Nil)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, errors.New(i18n.T(ctx, "phone_number_in_use"))
		}
	}

	exists, err := s.userRepo.ExistsByEmail(ctx, req.Email)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, errors.New(i18n.T(ctx, "email_already_exists"))
	}

	exists, err = s.userRepo.ExistsByUsername(ctx, req.Username)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, errors.New(i18n.T(ctx, "username_already_exists"))
	}

	hashedPassword, err := utils.HashPassword(req.Password)
	if err != nil {
		return nil, err
	}

	user := &models.User{
		Email:           req.Email,
		Username:        req.Username,
		Password:        hashedPassword,
		FirstName:       req.FirstName,
		MiddleName:      req.MiddleName,
		LastName:        req.LastName,
		Phone:           req.Phone,
		Extension:       req.Extension,
		DepartmentID:    req.DepartmentID,
		LocationID:      req.LocationID,
		IsActive:        true,
		BypassLoginTotp: req.BypassLoginTotp,
		EnableLoginTotp: req.EnableLoginTotp,
	}

	// Check license user limit before creating
	if s.licenseService != nil && s.licenseService.IsValid() && s.licenseRepo != nil {
		activeCount, countErr := s.licenseRepo.CountActiveUsers(ctx)
		if countErr == nil && activeCount >= int64(s.licenseService.GetMaxUsers()) {
			return nil, fmt.Errorf("%w: %d active users, license limit is %d", ErrUserLimitReached, activeCount, s.licenseService.GetMaxUsers())
		}
	}

	if err := s.userRepo.Create(ctx, user); err != nil {
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "23505") {
			if strings.Contains(err.Error(), "idx_users_email") {
				return nil, errors.New(i18n.T(ctx, "email_already_exists"))
			}
			if strings.Contains(err.Error(), "idx_users_username") {
				return nil, errors.New(i18n.T(ctx, "username_already_exists"))
			}
			if strings.Contains(err.Error(), "idx_users_phone") {
				return nil, errors.New(i18n.T(ctx, "phone_number_in_use"))
			}
		}
		return nil, err
	}

	// Department-scoped users cannot assign roles that grant department scope
	if len(req.RoleIDs) > 0 {
		actor, _ := s.userRepo.FindByIDWithRelations(ctx, actorID)
		if actor != nil && !actor.IsSuperAdmin && actor.HasPermission("users:view_department_only") {
			for _, roleID := range req.RoleIDs {
				var targetRole models.Role
				if err := s.db.Preload("Permissions").First(&targetRole, "id = ?", roleID).Error; err == nil {
					for _, perm := range targetRole.Permissions {
						if perm.Code == "users:view_department_only" {
							return nil, errors.New(i18n.T(ctx, "cannot_assign_department_manager_role"))
						}
					}
				}
			}
		}
		s.userRepo.AssignRoles(ctx, user.ID, req.RoleIDs)
	}

	// Assign departments if provided
	if len(req.DepartmentIDs) > 0 {
		s.userRepo.AssignDepartments(ctx, user.ID, req.DepartmentIDs)
	}

	// No manual role provided: inherit the roles associated with the user's department,
	// unless a different (manual) role is explicitly assigned in this request. Falls back to
	// the first many-to-many department when no primary DepartmentID is set, since the
	// create-user UI only ever populates department_ids, never the singular department_id.
	if len(req.RoleIDs) == 0 {
		inheritDeptID := user.DepartmentID
		if inheritDeptID == nil && len(req.DepartmentIDs) > 0 {
			inheritDeptID = &req.DepartmentIDs[0]
		}
		if inheritDeptID != nil {
			s.ApplyDepartmentDefaultRoles(ctx, user.ID, *inheritDeptID, actorID)
		}
	}

	// Assign manually selected locations/classifications from the frontend
	if len(req.LocationIDs) > 0 {
		s.userRepo.AssignLocations(ctx, user.ID, req.LocationIDs)
	}

	if len(req.ClassificationIDs) > 0 {
		s.userRepo.AssignClassifications(ctx, user.ID, req.ClassificationIDs)
	}

	// When enabled, append department-derived locations/classifications.
	// For creation all departments are "added" (oldDeptIDs is empty), so the sync
	// appends their locations/classifications — covers the fast-save race condition.
	if len(req.DepartmentIDs) > 0 && s.config.SyncDeptAttributesToUser {
		_ = s.syncDepartmentAttributesToUser(ctx, user.ID, nil, req.DepartmentIDs)
	}

	// Assign extension if provided
	if req.Extension != "" {
		actorID, _ := ctx.Value(constants.ContextKeys.UserID).(uuid.UUID)
		assignment := &models.ExtensionAssignment{
			Extension:  req.Extension,
			UserID:     user.ID,
			AssignedBy: actorID,
		}
		if err := s.db.WithContext(ctx).Create(assignment).Error; err != nil {
			return nil, fmt.Errorf("%s: %w", i18n.T(ctx, "failed_to_assign_extension"), err)
		}
		s.db.WithContext(ctx).Create(&models.ExtensionAssignmentHistory{
			Extension:  req.Extension,
			UserID:     &user.ID,
			AssignedBy: actorID,
			Action:     models.ExtensionActionAssign,
		})
	}

	// Get user's primary role for JWT (use "user" as default)
	role := "user"
	if user.IsSuperAdmin {
		role = "admin"
	}

	token, err := s.jwtManager.GenerateToken(user.ID, user.Email, role)
	if err != nil {
		return nil, err
	}

	if err := s.sessionStore.SetUserSession(ctx, user.ID.String(), map[string]interface{}{
		"user_id": user.ID,
		"email":   user.Email,
		"role":    role,
	}, s.jwtManager.GetTokenExpiration()); err != nil {
		return nil, err
	}

	// Reload user with relations
	user, _ = s.userRepo.FindByIDWithRelations(ctx, user.ID)

	// Log user creation with complete details
	go func() {
		_ = s.actionLogService.LogAction(context.Background(), &LogActionParams{
			UserID:      actorID,
			Action:      "create",
			Module:      "users",
			ResourceID:  user.ID.String(),
			Description: fmt.Sprintf("User %s (%s) created by admin", user.Username, user.Email),
			OldValue:    nil,
			NewValue:    req,
			IPAddress:   ipAddress,
			UserAgent:   userAgent,
			Status:      "success",
		})
	}()

	return &models.AuthResponse{
		User:  models.ToUserResponse(user),
		Token: token,
	}, nil
}

func (s *userService) SSORegister(ctx context.Context, req *models.SSORegisterRequest) (*models.AuthResponse, error) {
	ipAddress, _ := ctx.Value(constants.ContextKeys.IP_ADDRESS).(string)
	userAgent, _ := ctx.Value(constants.ContextKeys.USER_AGENT).(string)

	if req.Phone != "" {
		exists, err := s.userRepo.ExistsByPhone(ctx, req.Phone, uuid.Nil)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, errors.New(i18n.T(ctx, "phone_number_in_use"))
		}
	}

	exists, err := s.userRepo.ExistsByEmail(ctx, req.Email)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, errors.New(i18n.T(ctx, "email_already_exists"))
	}

	exists, err = s.userRepo.ExistsByUsername(ctx, req.Username)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, errors.New(i18n.T(ctx, "username_already_exists"))
	}

	exists, err = s.userRepo.ExistsByNationalID(ctx, req.NationalID)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, errors.New(i18n.T(ctx, "national_id_exists"))
	}

	randomPw, _ := uuid.NewRandom()
	hashedPassword, err := utils.HashPassword(randomPw.String())
	if err != nil {
		return nil, err
	}

	user := &models.User{
		Email:        req.Email,
		Username:     req.Username,
		Password:     hashedPassword,
		NationalID:   req.NationalID,
		FirstName:    req.FirstName,
		MiddleName:   req.MiddleName,
		LastName:     req.LastName,
		Phone:        req.Phone,
		Extension:    req.Extension,
		DepartmentID: req.DepartmentID,
		LocationID:   req.LocationID,
		IsActive:     true,
	}

	if s.licenseService != nil && s.licenseService.IsValid() && s.licenseRepo != nil {
		activeCount, countErr := s.licenseRepo.CountActiveUsers(ctx)
		if countErr == nil && activeCount >= int64(s.licenseService.GetMaxUsers()) {
			return nil, fmt.Errorf("%w: %d active users, license limit is %d", ErrUserLimitReached, activeCount, s.licenseService.GetMaxUsers())
		}
	}

	if err := s.userRepo.Create(ctx, user); err != nil {
		return nil, err
	}

	if len(req.RoleIDs) > 0 {
		s.userRepo.AssignRoles(ctx, user.ID, req.RoleIDs)
	}
	if len(req.DepartmentIDs) > 0 {
		s.userRepo.AssignDepartments(ctx, user.ID, req.DepartmentIDs)
	}
	if len(req.LocationIDs) > 0 {
		s.userRepo.AssignLocations(ctx, user.ID, req.LocationIDs)
	}
	if len(req.ClassificationIDs) > 0 {
		s.userRepo.AssignClassifications(ctx, user.ID, req.ClassificationIDs)
	}

	role := "user"
	if user.IsSuperAdmin {
		role = "admin"
	}

	token, err := s.jwtManager.GenerateToken(user.ID, user.Email, role)
	if err != nil {
		return nil, err
	}

	if err := s.sessionStore.SetUserSession(ctx, user.ID.String(), map[string]interface{}{
		"user_id": user.ID,
		"email":   user.Email,
		"role":    role,
	}, s.jwtManager.GetTokenExpiration()); err != nil {
		return nil, err
	}

	user, _ = s.userRepo.FindByIDWithRelations(ctx, user.ID)

	go func() {
		_ = s.actionLogService.LogAction(context.Background(), &LogActionParams{
			UserID:      uuid.Nil,
			Action:      "sso_register",
			Module:      "users",
			ResourceID:  user.ID.String(),
			Description: fmt.Sprintf("SSO user registered: %s (%s, national_id: %s)", user.Username, user.Email, user.NationalID),
			OldValue:    nil,
			NewValue:    req,
			IPAddress:   ipAddress,
			UserAgent:   userAgent,
			Status:      "success",
		})
	}()

	return &models.AuthResponse{
		User:  models.ToUserResponse(user),
		Token: token,
	}, nil
}

func (s *userService) SSOLogin(ctx context.Context, req *models.SSOLoginRequest) (*models.AuthLoginResponse, error) {
	user, err := s.userRepo.FindByNationalIDForLogin(ctx, req.NationalID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New(i18n.T(ctx, "invalid_credentials"))
		}
		return nil, err
	}

	if !user.IsActive {
		return nil, errors.New(i18n.T(ctx, "account_deactivated_service"))
	}

	role := "user"
	if user.IsSuperAdmin {
		role = "admin"
	} else if len(user.Roles) > 0 {
		role = user.Roles[0].Code
	}

	sessionID, err := s.sessionStore.SetUserSessionMultiDevices(ctx, user.ID.String(), map[string]interface{}{
		"user_id": user.ID,
		"email":   user.Email,
		"role":    role,
	}, s.jwtManager.GetTokenExpiration())
	if err != nil {
		return nil, err
	}

	tokenPair, err := s.jwtManager.GenerateTokenPair(user.ID, user.Email, role, sessionID, false)
	if err != nil {
		return nil, err
	}

	go func(ctx context.Context, userID uuid.UUID) {
		if err := s.userRepo.UpdateLastLogin(ctx, userID); err != nil {
			fmt.Println("failed to update last login:", err)
		}
	}(ctx, user.ID)

	return &models.AuthLoginResponse{
		User:         models.ToUserLoginResponse(user),
		Token:        tokenPair.AccessToken,
		RefreshToken: tokenPair.RefreshToken,
		ExpiresIn:    tokenPair.ExpiresIn,
	}, nil
}

func (s *userService) Login(ctx context.Context, req *models.UserLoginRequest) (*models.AuthLoginResponse, error) {
	ipAddress, _ := ctx.Value(constants.ContextKeys.IP_ADDRESS).(string)
	userAgent, _ := ctx.Value(constants.ContextKeys.USER_AGENT).(string)

	loginType := req.LoginType()

	var user *models.User
	var err error

	switch loginType {
	case "email_password":
		// Email/Password login
		user, err = s.userRepo.FindByEmailForLogin(ctx, req.Email)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				go func() {
					_ = s.actionLogService.LogAction(context.Background(), &LogActionParams{
						UserID:      uuid.Nil,
						Action:      "login_failed",
						Module:      "users",
						ResourceID:  "",
						Description: fmt.Sprintf("Failed login attempt for email: %s", req.Email),
						OldValue:    nil,
						NewValue:    nil,
						IPAddress:   ipAddress,
						UserAgent:   userAgent,
						Status:      "failed",
					})
				}()
				return nil, errors.New(i18n.T(ctx, "invalid_credentials"))
			}
			return nil, err
		}

		// Redirect AD users to LDAP login
		if user.IsADUser {
			return nil, errors.New(i18n.T(ctx, "use_ad_login"))
		}

		if !utils.CheckPassword(req.Password, user.Password) {
			go func() {
				_ = s.actionLogService.LogAction(context.Background(), &LogActionParams{
					UserID:      user.ID,
					Action:      "login_failed",
					Module:      "users",
					ResourceID:  user.ID.String(),
					Description: fmt.Sprintf("Failed login attempt for user: %s (%s)", user.Username, user.Email),
					OldValue:    nil,
					NewValue:    nil,
					IPAddress:   ipAddress,
					UserAgent:   userAgent,
					Status:      "failed",
				})
			}()
			return nil, errors.New(i18n.T(ctx, "invalid_credentials"))
		}

	case "mobile_otp":
		// Mobile/OTP login
		if s.otpService == nil {
			return nil, errors.New(i18n.T(ctx, "otp_service_unavailable"))
		}

		// First check if user exists with this phone number
		user, err = s.userRepo.FindByPhoneForLogin(ctx, req.Phone)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				go func() {
					_ = s.actionLogService.LogAction(context.Background(), &LogActionParams{
						UserID:      uuid.Nil,
						Action:      "login_failed",
						Module:      "users",
						ResourceID:  "",
						Description: fmt.Sprintf("No user found for phone: %s", req.Phone),
						OldValue:    nil,
						NewValue:    nil,
						IPAddress:   ipAddress,
						UserAgent:   userAgent,
						Status:      "failed",
					})
				}()
				return nil, errors.New(i18n.T(ctx, "no_user_for_mobile"))
			}
			return nil, err
		}

		// Check if mobile number is verified
		if !user.MobileVerified {
			go func() {
				_ = s.actionLogService.LogAction(context.Background(), &LogActionParams{
					UserID:      user.ID,
					Action:      "login_failed",
					Module:      "users",
					ResourceID:  user.ID.String(),
					Description: fmt.Sprintf("Login attempt with unverified mobile: %s (%s)", user.Username, user.Phone),
					OldValue:    nil,
					NewValue:    nil,
					IPAddress:   ipAddress,
					UserAgent:   userAgent,
					Status:      "failed",
				})
			}()
			return nil, errors.New(i18n.T(ctx, "mobile_not_verified"))
		}

		// Verify OTP
		_, err = s.otpService.VerifyOTP(ctx, req.Phone, req.SessionID, req.OTP)
		if err != nil {
			go func() {
				_ = s.actionLogService.LogAction(context.Background(), &LogActionParams{
					UserID:      uuid.Nil,
					Action:      "login_failed_otp",
					Module:      "users",
					ResourceID:  "",
					Description: fmt.Sprintf("Failed OTP login attempt for phone: %s - %v", req.Phone, err),
					OldValue:    nil,
					NewValue:    nil,
					IPAddress:   ipAddress,
					UserAgent:   userAgent,
					Status:      "failed",
				})
			}()
			return nil, errors.New(i18n.T(ctx, "invalid_or_expired_otp"))
		}

	default:
		return nil, errors.New(i18n.T(ctx, "invalid_login_credentials"))
	}

	// Check if user is active
	if !user.IsActive {
		go func() {
			_ = s.actionLogService.LogAction(context.Background(), &LogActionParams{
				UserID:      user.ID,
				Action:      "login_failed",
				Module:      "users",
				ResourceID:  user.ID.String(),
				Description: fmt.Sprintf("Login attempt failed for deactivated user: %s (%s)", user.Username, user.Email),
				OldValue:    nil,
				NewValue:    nil,
				IPAddress:   ipAddress,
				UserAgent:   userAgent,
				Status:      "failed",
			})
		}()
		return nil, errors.New(i18n.T(ctx, "account_deactivated_service"))
	}

	// Determine primary role for JWT
	role := "user"
	if user.IsSuperAdmin {
		role = "admin"
	} else if len(user.Roles) > 0 {
		role = user.Roles[0].Code
	}

	sessionID, err := s.sessionStore.SetUserSessionMultiDevices(ctx, user.ID.String(), map[string]interface{}{
		"user_id": user.ID,
		"email":   user.Email,
		"role":    role,
	}, s.jwtManager.GetTokenExpiration())
	log.Println("Login: User session created:", sessionID, "and User ID:", user.ID.String())
	if err != nil {
		return nil, err
	}

	tokenPair, err := s.jwtManager.GenerateTokenPair(user.ID, user.Email, role, sessionID, req.RememberMe)
	if err != nil {
		return nil, err
	}

	go func(ctx context.Context, userID uuid.UUID) {
		if err := s.userRepo.UpdateLastLogin(ctx, userID); err != nil {
			fmt.Println("failed to update last login:", err)
		}
	}(ctx, user.ID)
	// Log successful login
	go func() {
		if err := s.actionLogService.LogAction(context.Background(), &LogActionParams{
			UserID:      user.ID,
			Action:      "login",
			Module:      "users",
			ResourceID:  user.ID.String(),
			Description: fmt.Sprintf("Successful login for user: %s (%s)", user.Username, user.Email),
			OldValue:    nil,
			NewValue:    nil,
			IPAddress:   ipAddress,
			UserAgent:   userAgent,
			Status:      "success",
		}); err != nil {
			fmt.Printf("Error logging login action: %v\n", err)
		}
	}()

	// Reset login attempt counters on successful login
	// Reset by email
	if req.Email != "" {
		go func() {
			_ = database.ResetLoginAttempts(context.Background(), database.RedisClient, "user:"+req.Email)
		}()
	}
	// Reset by phone
	if req.Phone != "" {
		go func() {
			_ = database.ResetLoginAttempts(context.Background(), database.RedisClient, "user:"+req.Phone)
		}()
	}
	// Reset by IP
	go func() {
		_ = database.ResetLoginAttempts(context.Background(), database.RedisClient, "ip:"+ipAddress)
	}()

	resp := &models.AuthLoginResponse{
		User: models.ToUserLoginResponse(user),
	}

	totpEnabled := false
	if s.settingsRepo != nil {
		if settings, sErr := s.settingsRepo.GetOrCreate(ctx); sErr == nil && settings != nil {
			totpEnabled = settings.TotpEnabled
		} else if sErr != nil {
			log.Println("Login: failed to load settings for totp_enabled check:", sErr)
		}
	}

	if !totpEnabled || user.IsSuperAdmin || !IsTotpRequired(user) {
		resp.Token = tokenPair.AccessToken
		resp.RefreshToken = tokenPair.RefreshToken
		resp.ExpiresIn = tokenPair.ExpiresIn
	}

	return resp, nil
}

// IsTotpRequired decides whether a user must complete TOTP at login. This runs only
// when the global totp_enabled setting is on and the user isn't a super admin -
// both are checked separately by the caller. When the global setting is on, TOTP is
// required for everyone by default; a user is exempted if they, or any role
// assigned to them, is explicitly marked BypassLoginTotp - unless the user has an
// explicit per-user EnableLoginTotp override, which takes precedence over any
// role-level bypass.
func IsTotpRequired(user *models.User) bool {
	if user.BypassLoginTotp {
		return false
	}
	if user.EnableLoginTotp {
		return true
	}
	for _, role := range user.Roles {
		if role.BypassLoginTotp {
			return false
		}
	}
	return true
}

func (s *userService) ValidateMobileForLogin(ctx context.Context, phone string) (*models.UserResponse, error) {
	// Check if user exists with this phone number
	user, err := s.userRepo.FindByPhoneWithRelations(ctx, phone)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New(i18n.T(ctx, "no_user_for_mobile"))
		}
		return nil, err
	}

	// Check if mobile number is verified
	if !user.MobileVerified {
		return nil, errors.New(i18n.T(ctx, "mobile_not_verified"))
	}

	// Check if user is active
	if !user.IsActive {
		return nil, errors.New(i18n.T(ctx, "account_deactivated_service"))
	}

	// Mobile is verified and user is active - return user info
	resp := models.ToUserResponse(user)
	return &resp, nil
}

func (s *userService) RefreshToken(ctx context.Context, refreshToken string, rememberMe ...bool) (*models.AuthResponse, error) {
	// Validate the refresh token
	claims, err := s.jwtManager.ValidateRefreshToken(refreshToken)
	if err != nil {
		return nil, errors.New(i18n.T(ctx, "invalid_refresh_token"))
	}

	// Reject if password was changed after this refresh token was issued.
	// This closes the gap where Browser B's frontend would silently refresh
	// and get a new session even though all sessions were killed on password change.
	if passChangedAt, ok := s.sessionStore.GetPasswordChangedAt(ctx, claims.UserID.String()); ok {
		if claims.IssuedAt != nil && claims.IssuedAt.Unix() < passChangedAt {
			return nil, errors.New(i18n.T(ctx, "password_changed_relogin"))
		}
	}

	// Get user from database
	user, err := s.userRepo.FindByIDWithRelations(ctx, claims.UserID)
	if err != nil {
		return nil, errors.New(i18n.T(ctx, "user_not_found"))
	}

	if !user.IsActive {
		return nil, errors.New(i18n.T(ctx, "account_deactivated_service"))
	}

	// Determine primary role for JWT
	role := "user"
	if user.IsSuperAdmin {
		role = "admin"
	} else if len(user.Roles) > 0 {
		role = user.Roles[0].Code
	}

	// Create new session
	sessionID, err := s.sessionStore.SetUserSessionMultiDevices(ctx, user.ID.String(), map[string]interface{}{
		"user_id": user.ID,
		"email":   user.Email,
		"role":    role,
	}, s.jwtManager.GetTokenExpiration())
	if err != nil {
		return nil, err
	}

	// Default to false if not provided
	useRemember := len(rememberMe) > 0 && rememberMe[0]

	// Generate new token pair
	tokenPair, err := s.jwtManager.GenerateTokenPair(user.ID, user.Email, role, sessionID, useRemember)
	if err != nil {
		return nil, err
	}

	return &models.AuthResponse{
		User:         models.ToUserResponse(user),
		Token:        tokenPair.AccessToken,
		RefreshToken: tokenPair.RefreshToken,
		ExpiresIn:    tokenPair.ExpiresIn,
	}, nil
}

func (s *userService) Logout(ctx context.Context) error {
	// Get user ID from context to log the logout event
	userIDInterface := ctx.Value(constants.ContextKeys.UserID)
	if userIDInterface != nil {
		if userID, ok := userIDInterface.(uuid.UUID); ok {
			// Safely get IP address and user agent from context
			ipAddress := ""
			if ipVal := ctx.Value(constants.ContextKeys.IP_ADDRESS); ipVal != nil {
				if ipStr, ok := ipVal.(string); ok {
					ipAddress = ipStr
				}
			}

			userAgent := ""
			if uaVal := ctx.Value(constants.ContextKeys.USER_AGENT); uaVal != nil {
				if uaStr, ok := uaVal.(string); ok {
					userAgent = uaStr
				}
			}

			// Log logout event
			go func() {
				if err := s.actionLogService.LogAction(context.Background(), &LogActionParams{
					UserID:      userID,
					Action:      "logout",
					Module:      "users",
					ResourceID:  userID.String(),
					Description: "User logged out",
					OldValue:    nil,
					NewValue:    nil,
					IPAddress:   ipAddress,
					UserAgent:   userAgent,
					Status:      "success",
				}); err != nil {
					// Log the error for debugging
					fmt.Printf("Error logging logout action: %v\n", err)
				}
			}()
		}
	}

	token, _ := ctx.Value(constants.ContextKeys.Token).(string)
	if err := s.sessionStore.BlacklistToken(ctx, token, s.jwtManager.GetTokenExpiration()); err != nil {
		return err
	}

	// Also remove the specific session so ValidateSession immediately rejects any reuse
	if userID, ok := userIDInterface.(uuid.UUID); ok {
		sessionID, _ := ctx.Value(constants.ContextKeys.SessionID).(string)
		if sessionID != "" {
			_ = s.sessionStore.DeleteSession(ctx, userID.String(), sessionID)
		}
	}
	return nil
}

func (s *userService) GetProfile(ctx context.Context, userID uuid.UUID) (*models.UserResponse, error) {
	user, err := s.userRepo.FindByIDWithRelations(ctx, userID)
	if err != nil {
		return nil, err
	}

	response := models.ToUserResponse(user)
	return &response, nil
}

func (s *userService) GenerateTokenViaUserID(ctx context.Context, userID uuid.UUID) (*models.AuthResponse, error) {
	ipAddress, _ := ctx.Value(constants.ContextKeys.IP_ADDRESS).(string)
	userAgent, _ := ctx.Value(constants.ContextKeys.USER_AGENT).(string)
	log.Println("Attempting to find user by last 6 digits of phone number for IVR login")
	// user, err := s.userRepo.FindByLast6Digits(ctx, last6Digits)
	// if err != nil {
	// 	return nil, err
	// }

	user, err := s.userRepo.FindByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	log.Println("user fetched for last 6 digits login")
	// Continue with the rest of the login logic...
	sessionID, err := s.sessionStore.SetUserSessionMultiDevices(ctx, user.ID.String(), map[string]interface{}{
		"user_id": user.ID,
		"email":   user.Email,
		"role":    constants.USER_ROLE.CITIZEN,
	}, s.jwtManager.GetTokenExpiration())
	if err != nil {
		return nil, err
	}

	tokenPair, err := s.jwtManager.GenerateTokenPair(user.ID, user.Email, constants.USER_ROLE.CITIZEN, sessionID, false)
	if err != nil {
		return nil, err
	}

	// Log successful login
	go func() {
		if err := s.actionLogService.LogAction(context.Background(), &LogActionParams{
			UserID:      user.ID,
			Action:      "login",
			Module:      "users",
			ResourceID:  user.ID.String(),
			Description: fmt.Sprintf("Successful login for user: %s (%s)", user.Username, user.Email),
			OldValue:    nil,
			NewValue:    nil,
			IPAddress:   ipAddress,
			UserAgent:   userAgent,
			Status:      "success",
		}); err != nil {
			// Log the error for debugging
			fmt.Printf("Error logging login action: %v\n", err)
		}
	}()

	userResp := models.UserResponse{
		ID:           user.ID,
		Email:        user.Email,
		Username:     user.Username,
		FirstName:    user.FirstName,
		LastName:     user.LastName,
		Phone:        user.Phone,
		Extension:    user.Extension,
		DepartmentID: user.DepartmentID,
		LocationID:   user.LocationID,
		IsActive:     user.IsActive,
	}
	return &models.AuthResponse{
		User:         userResp,
		Token:        tokenPair.AccessToken,
		RefreshToken: tokenPair.RefreshToken,
		ExpiresIn:    tokenPair.ExpiresIn,
	}, nil

}
func (s *userService) UpdateAdminProfile(ctx context.Context, userID uuid.UUID, req *models.UserUpdateRequest) (*models.UserResponse, error) {
	// Get the authenticated user (who is performing this action) from context
	actorID, ok := ctx.Value(constants.ContextKeys.UserID).(uuid.UUID)
	if !ok {
		return nil, errors.New(i18n.T(ctx, "unauthorized_not_authenticated"))
	}
	ipAddress, _ := ctx.Value(constants.ContextKeys.IP_ADDRESS).(string)
	userAgent, _ := ctx.Value(constants.ContextKeys.USER_AGENT).(string)
	user, err := s.userRepo.FindByIDWithRelations(ctx, userID)
	if err != nil {
		return nil, err
	}

	// Store old values for audit logging
	// IMPORTANT: Create COPIES of values, not pointers to mutable fields
	oldIsActive := user.IsActive
	oldMobileVerified := user.MobileVerified
	oldExtension := user.Extension // Copy the extension value
	oldUser := &models.UserUpdateRequest{
		FirstName:         user.FirstName,
		MiddleName:        user.MiddleName,
		LastName:          user.LastName,
		Username:          user.Username,
		Phone:             user.Phone,
		Extension:         &oldExtension, // Pointer to the copy, not the original
		DepartmentID:      user.DepartmentID,
		LocationID:        user.LocationID,
		IsActive:          &oldIsActive,       // Pointer to the copy
		MobileVerified:    &oldMobileVerified, // Pointer to the copy
		DepartmentIDs:     make([]uuid.UUID, len(user.Departments)),
		LocationIDs:       make([]uuid.UUID, len(user.Locations)),
		ClassificationIDs: make([]uuid.UUID, len(user.Classifications)),
		RoleIDs:           make([]uuid.UUID, len(user.Roles)),
	}

	// Build old value with names for better audit trail
	oldUserForLogging := map[string]interface{}{
		"first_name":      user.FirstName,
		"middle_name":     user.MiddleName,
		"last_name":       user.LastName,
		"username":        user.Username,
		"phone":           user.Phone,
		"extension":       user.Extension,
		"mobile_verified": user.MobileVerified,
		"is_active":       oldIsActive,
	}

	// Add department names
	oldDeptNames := make([]string, len(user.Departments))
	for i, dept := range user.Departments {
		oldDeptNames[i] = dept.Name
	}
	oldUserForLogging["departments"] = oldDeptNames

	// Add location names
	oldLocNames := make([]string, len(user.Locations))
	for i, loc := range user.Locations {
		oldLocNames[i] = loc.Name
	}
	oldUserForLogging["locations"] = oldLocNames

	// Add classification names
	oldClassNames := make([]string, len(user.Classifications))
	for i, cls := range user.Classifications {
		oldClassNames[i] = cls.Name
	}
	oldUserForLogging["classifications"] = oldClassNames

	// Add role names
	oldRoleNames := make([]string, len(user.Roles))
	for i, role := range user.Roles {
		oldRoleNames[i] = role.Name
	}
	oldUserForLogging["roles"] = oldRoleNames

	// Track if departments changed to decide whether to sync
	departmentsChanged := false

	// Check if department_ids changed (for multi-department users)
	if req.DepartmentIDs != nil {
		oldDeptIDs := make(map[uuid.UUID]bool)
		for _, dept := range user.Departments {
			oldDeptIDs[dept.ID] = true
		}

		if len(oldDeptIDs) != len(req.DepartmentIDs) {
			departmentsChanged = true
		} else {
			for _, newDeptID := range req.DepartmentIDs {
				if !oldDeptIDs[newDeptID] {
					departmentsChanged = true
					break
				}
			}
		}
	}

	if req.Username != "" && req.Username != user.Username {
		exists, err := s.userRepo.ExistsByUsername(ctx, req.Username)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, errors.New(i18n.T(ctx, "username_already_exists"))
		}
		user.Username = req.Username
	}

	if req.FirstName != "" {
		user.FirstName = req.FirstName
	}
	if req.MiddleName != "" {
		user.MiddleName = req.MiddleName
	}
	if req.LastName != "" {
		user.LastName = req.LastName
	}
	if req.Phone != user.Phone {
		// Case 1: Remove phone number
		if req.Phone == "" {
			user.Phone = ""
		} else {
			// Case 2: Check unique (excluding current user)
			exists, err := s.userRepo.ExistsByPhone(ctx, req.Phone, user.ID)
			if err != nil {
				return nil, err
			}
			if exists {
				return nil, errors.New(i18n.T(ctx, "phone_number_in_use"))
			}

			user.Phone = req.Phone
		}
	}

	if req.DepartmentID != nil {
		user.DepartmentID = req.DepartmentID
	}
	if req.LocationID != nil {
		user.LocationID = req.LocationID
	}
	if req.IsActive != nil {
		user.IsActive = *req.IsActive
	}
	if req.DeptManagerDepartmentID != nil {
		user.DeptManagerDepartmentID = req.DeptManagerDepartmentID
	}
	if req.DeptManagerClassificationID != nil {
		user.DeptManagerClassificationID = req.DeptManagerClassificationID
	}
	if req.DeptManagerLocationID != nil {
		user.DeptManagerLocationID = req.DeptManagerLocationID
	}

	if req.Extension != nil {
		user.Extension = *req.Extension
	}
	if req.BypassLoginTotp != nil {
		user.BypassLoginTotp = *req.BypassLoginTotp
	}
	if req.EnableLoginTotp != nil {
		user.EnableLoginTotp = *req.EnableLoginTotp
	}

	if err := s.userRepo.Update(ctx, user); err != nil {
		return nil, err
	}

	// Department-scoped users cannot assign roles that grant department scope
	if req.RoleIDs != nil {
		actor, _ := s.userRepo.FindByIDWithRelations(ctx, actorID)
		if actor != nil && !actor.IsSuperAdmin && actor.HasPermission("users:view_department_only") {
			for _, roleID := range req.RoleIDs {
				var targetRole models.Role
				if err := s.db.Preload("Permissions").First(&targetRole, "id = ?", roleID).Error; err == nil {
					for _, perm := range targetRole.Permissions {
						if perm.Code == "users:view_department_only" {
							return nil, errors.New(i18n.T(ctx, "cannot_assign_department_manager_role"))
						}
					}
				}
			}
		}
	}

	// Always update associations (even if empty arrays) to allow removal
	// Track old values before updating
	oldRoleIDs := make([]uuid.UUID, len(oldUser.RoleIDs))
	copy(oldRoleIDs, oldUser.RoleIDs)
	s.userRepo.AssignRoles(ctx, user.ID, req.RoleIDs)

	// Track old values before updating
	oldDepartmentIDs := make([]uuid.UUID, len(oldUser.DepartmentIDs))
	copy(oldDepartmentIDs, oldUser.DepartmentIDs)
	s.userRepo.AssignDepartments(ctx, user.ID, req.DepartmentIDs)

	// If the user had no role before this update and none was explicitly requested,
	// inherit the roles associated with their department. A manually assigned role
	// (before or in this request) always takes priority and is never overridden.
	// Falls back to the first many-to-many department when no primary DepartmentID is
	// set, since the department_ids (M2M) field is what the frontend actually populates.
	if len(req.RoleIDs) == 0 && len(oldUser.RoleIDs) == 0 {
		inheritDeptID := user.DepartmentID
		if inheritDeptID == nil && len(req.DepartmentIDs) > 0 {
			inheritDeptID = &req.DepartmentIDs[0]
		}
		if inheritDeptID != nil {
			s.ApplyDepartmentDefaultRoles(ctx, user.ID, *inheritDeptID, actorID)
		}
	}

	// Track old values before updating (needed for audit logging below)
	oldLocationIDs := make([]uuid.UUID, len(oldUser.LocationIDs))
	copy(oldLocationIDs, oldUser.LocationIDs)
	oldClassificationIDs := make([]uuid.UUID, len(oldUser.ClassificationIDs))
	copy(oldClassificationIDs, oldUser.ClassificationIDs)

	// Always save frontend's selections (includes manual edits + dept-derived values).
	// The frontend now shows a loading state on locations/classifications while
	// department data is being fetched, so the race condition is prevented at source.
	s.userRepo.AssignLocations(ctx, user.ID, req.LocationIDs)
	s.userRepo.AssignClassifications(ctx, user.ID, req.ClassificationIDs)

	// Safety net: if departments changed and sync is enabled, append locations/
	// classifications from newly ADDED departments. This covers scope-filtered admins
	// who may not see all department locations via the frontend (limitIdsToScope).
	if departmentsChanged && s.config.SyncDeptAttributesToUser {
		_ = s.syncDepartmentAttributesToUser(ctx, user.ID, oldDepartmentIDs, req.DepartmentIDs)
	}

	// Reload with relations
	user, _ = s.userRepo.FindByIDWithRelations(ctx, user.ID)

	response := models.ToUserResponse(user)

	// Track if only is_active changed (no other fields)
	onlyStatusChanged := oldUser.IsActive != nil && req.IsActive != nil && *oldUser.IsActive != *req.IsActive

	// Log status change (Active/Inactive) separately for better audit trail
	if onlyStatusChanged {
		go func() {
			status := "activated"
			if !*req.IsActive {
				status = "deactivated"
			}
			_ = s.actionLogService.LogAction(context.Background(), &LogActionParams{
				UserID:      actorID,
				Action:      "status_change",
				Module:      "users",
				ResourceID:  user.ID.String(),
				Description: fmt.Sprintf("User %s %s by admin", user.Username, status),
				OldValue:    map[string]interface{}{"is_active": *oldUser.IsActive},
				NewValue:    map[string]interface{}{"is_active": *req.IsActive},
				IPAddress:   ipAddress,
				UserAgent:   userAgent,
				Status:      "success",
			})
		}()
	}

	// Build new value with names for better audit trail
	newUserForLogging := map[string]interface{}{
		"first_name":  req.FirstName,
		"middle_name": req.MiddleName,
		"last_name":   req.LastName,
		"username":    req.Username,
		"phone":       req.Phone,
	}

	if req.Extension != nil {
		newUserForLogging["extension"] = *req.Extension
	}
	if req.MobileVerified != nil {
		newUserForLogging["mobile_verified"] = *req.MobileVerified
	}
	if req.IsActive != nil {
		newUserForLogging["is_active"] = *req.IsActive
	}

	// Add department names from request
	if req.DepartmentIDs != nil {
		newDeptNames := make([]string, 0)
		for _, deptID := range req.DepartmentIDs {
			for _, dept := range user.Departments {
				if dept.ID == deptID {
					newDeptNames = append(newDeptNames, dept.Name)
				}
			}
		}
		newUserForLogging["departments"] = newDeptNames
	}

	// Add location names from request
	if req.LocationIDs != nil {
		newLocNames := make([]string, 0)
		for _, locID := range req.LocationIDs {
			for _, loc := range user.Locations {
				if loc.ID == locID {
					newLocNames = append(newLocNames, loc.Name)
				}
			}
		}
		newUserForLogging["locations"] = newLocNames
	}

	// Add classification names from request
	if req.ClassificationIDs != nil {
		newClassNames := make([]string, 0)
		for _, classID := range req.ClassificationIDs {
			for _, cls := range user.Classifications {
				if cls.ID == classID {
					newClassNames = append(newClassNames, cls.Name)
				}
			}
		}
		newUserForLogging["classifications"] = newClassNames
	}

	// Add role names from request
	if req.RoleIDs != nil {
		newRoleNames := make([]string, 0)
		for _, roleID := range req.RoleIDs {
			for _, role := range user.Roles {
				if role.ID == roleID {
					newRoleNames = append(newRoleNames, role.Name)
				}
			}
		}
		newUserForLogging["roles"] = newRoleNames
	}

	// Log general user update with complete old/new values for audit trail
	// This provides detailed field-level changes in the action log
	go func() {
		// Build a description of what changed
		changedFields := []string{}
		if req.FirstName != "" && req.FirstName != user.FirstName {
			changedFields = append(changedFields, "first name")
		}
		if req.LastName != "" && req.LastName != user.LastName {
			changedFields = append(changedFields, "last name")
		}
		if req.Username != "" && req.Username != user.Username {
			changedFields = append(changedFields, "username")
		}
		if req.Phone != "" && req.Phone != user.Phone {
			changedFields = append(changedFields, "phone")
		}
		if req.Extension != nil && *req.Extension != user.Extension {
			changedFields = append(changedFields, "extension")
		}
		if req.MobileVerified != nil && *req.MobileVerified != user.MobileVerified {
			if *req.MobileVerified {
				changedFields = append(changedFields, "mobile verified")
			} else {
				changedFields = append(changedFields, "mobile unverified")
			}
		}
		if req.IsActive != nil && *req.IsActive != user.IsActive {
			if *req.IsActive {
				changedFields = append(changedFields, "activated account")
			} else {
				changedFields = append(changedFields, "deactivated account")
			}
		}

		description := fmt.Sprintf("User profile updated for user: %s (%s)", user.Username, user.Email)
		if len(changedFields) > 0 {
			description = fmt.Sprintf("Updated %s for user: %s (%s)",
				strings.Join(changedFields, ", "), user.Username, user.Email)
		}

		_ = s.actionLogService.LogAction(context.Background(), &LogActionParams{
			UserID:      actorID,
			Action:      "update",
			Module:      "users",
			ResourceID:  user.ID.String(),
			Description: description,
			OldValue:    oldUserForLogging,
			NewValue:    newUserForLogging,
			IPAddress:   ipAddress,
			UserAgent:   userAgent,
			Status:      "success",
		})
	}()

	return &response, nil
}

// for non-admin user profile update
func (s *userService) UpdateProfile(ctx context.Context, req *models.UserUpdateRequest) (*models.UserResponse, error) {
	// For regular users, we only allow updating certain fields like FirstName, LastName, Phone, and Avatar
	userID := ctx.Value(constants.ContextKeys.UserID).(uuid.UUID)
	user, err := s.userRepo.FindByIDWithRelations(ctx, userID)
	if err != nil {
		return nil, err
	}

	// Create COPIES of values before they get modified
	oldExtension := user.Extension
	oldMobileVerified := user.MobileVerified

	oldUser := &models.UserUpdateRequest{
		FirstName:      user.FirstName,
		MiddleName:     user.MiddleName,
		LastName:       user.LastName,
		Phone:          user.Phone,
		Extension:      &oldExtension,      // Pointer to the copy
		MobileVerified: &oldMobileVerified, // Pointer to the copy
	}

	update := map[string]interface{}{}

	if req.FirstName != "" && req.FirstName != user.FirstName {
		user.FirstName = req.FirstName
		update["first_name"] = req.FirstName
	}
	if req.MiddleName != user.MiddleName {
		user.MiddleName = req.MiddleName
		update["middle_name"] = req.MiddleName
	}
	if req.LastName != user.LastName {
		user.LastName = req.LastName
		update["last_name"] = req.LastName
	}

	if req.Phone != user.Phone {
		if req.Phone != "" {
			exists, err := s.userRepo.ExistsByPhone(ctx, req.Phone, userID)
			if err != nil {
				return nil, err
			}
			if exists {
				return nil, errors.New(i18n.T(ctx, "phone_number_in_use"))
			}
		}
		user.Phone = req.Phone
		if req.Phone == "" {
			update["phone"] = nil // allow remove
		} else {
			update["phone"] = req.Phone
		}
	}

	if req.MobileVerified != nil && *req.MobileVerified != user.MobileVerified {
		user.MobileVerified = *req.MobileVerified
		update["mobile_verified"] = req.MobileVerified
	}

	// Extension is no longer stored on users — it is managed only via the extensions
	// API (extension_assignments table). Any req.Extension here is ignored.

	if req.Username != "" && req.Username != user.Username {
		exists, err := s.userRepo.ExistsByUsername(ctx, req.Username)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, errors.New(i18n.T(ctx, "username_already_exists"))
		}
		user.Username = req.Username
		update["username"] = req.Username
	}

	if len(update) == 0 {
		return &models.UserResponse{
			ID:         user.ID,
			Username:   user.Username,
			Email:      user.Email,
			FirstName:  user.FirstName,
			MiddleName: user.MiddleName,
			LastName:   user.LastName,
			Phone:      user.Phone,
			Avatar:     user.Avatar,
		}, nil
	}

	update["id"] = userID
	if err := s.userRepo.UpdateProfile(ctx, update); err != nil {
		return nil, err
	}

	response := models.ToUserResponse(user)

	// Log profile update
	ipAddress, _ := ctx.Value(constants.ContextKeys.IP_ADDRESS).(string)
	userAgent, _ := ctx.Value(constants.ContextKeys.USER_AGENT).(string)
	go func() {
		// Build a description of what changed
		changedFields := []string{}
		if req.FirstName != "" && req.FirstName != user.FirstName {
			changedFields = append(changedFields, "first name")
		}
		if req.LastName != "" && req.LastName != user.LastName {
			changedFields = append(changedFields, "last name")
		}
		if req.Phone != "" && req.Phone != user.Phone {
			changedFields = append(changedFields, "phone")
		}
		if req.Extension != nil && *req.Extension != user.Extension {
			changedFields = append(changedFields, "extension")
		}
		if req.MobileVerified != nil && *req.MobileVerified != user.MobileVerified {
			if *req.MobileVerified {
				changedFields = append(changedFields, "mobile verified")
			} else {
				changedFields = append(changedFields, "mobile unverified")
			}
		}
		if req.Username != "" && req.Username != user.Username {
			changedFields = append(changedFields, "username")
		}

		description := fmt.Sprintf("User profile updated for user: %s (%s)", user.Username, user.Email)
		if len(changedFields) > 0 {
			description = fmt.Sprintf("Updated %s for user: %s (%s)",
				strings.Join(changedFields, ", "), user.Username, user.Email)
		}

		_ = s.actionLogService.LogAction(context.Background(), &LogActionParams{
			UserID:      userID,
			Action:      "profile_update",
			Module:      "users",
			ResourceID:  user.ID.String(),
			Description: description,
			OldValue:    oldUser,
			NewValue:    req,
			IPAddress:   ipAddress,
			UserAgent:   userAgent,
			Status:      "success",
		})
	}()

	return &response, nil
}

func (s *userService) ChangePassword(ctx context.Context, userID uuid.UUID, req *models.ChangePasswordRequest) error {
	ipAddress, _ := ctx.Value(constants.ContextKeys.IP_ADDRESS).(string)
	userAgent, _ := ctx.Value(constants.ContextKeys.USER_AGENT).(string)

	// Block AD/LDAP users from changing their password via the app
	user, userErr := s.userRepo.FindByID(ctx, userID)
	if userErr == nil && user.IsADUser {
		return errors.New(i18n.T(ctx, "cannot_change_ad_password"))
	}

	var sessionData map[string]interface{}
	if getErr := s.sessionStore.GetUserSession(ctx, userID.String(), &sessionData); getErr == nil {
		if authSource, ok := sessionData["auth_source"].(string); ok && authSource == "ldap" {
			return errors.New(i18n.T(ctx, "cannot_change_ldap_password"))
		}
	}

	user, err := s.userRepo.FindByID(ctx, userID)
	if err != nil {
		return err
	}

	if !utils.CheckPassword(req.OldPassword, user.Password) {
		// Log failed password change attempt
		go func() {
			_ = s.actionLogService.LogAction(context.Background(), &LogActionParams{
				UserID:      userID,
				Action:      "password_change_failed",
				Module:      "users",
				ResourceID:  user.ID.String(),
				Description: fmt.Sprintf("Failed password change attempt for user: %s (%s)", user.Username, user.Email),
				OldValue:    nil,
				NewValue:    nil,
				IPAddress:   ipAddress,
				UserAgent:   userAgent,
				Status:      "failed",
			})
		}()
		return errors.New(i18n.T(ctx, "current_password_incorrect"))
	}

	hashedPassword, err := utils.HashPassword(req.NewPassword)
	if err != nil {
		return err
	}

	user.Password = hashedPassword
	err = s.userRepo.Update(ctx, user)

	if err == nil {

		err = s.sessionStore.DeleteAllUserSessions(ctx, userID.String())
		if err != nil {
			return err
		}

		// Prevent old refresh tokens from creating new sessions after password change
		_ = s.sessionStore.SetPasswordChangedAt(ctx, userID.String(), s.jwtManager.GetRefreshExpiration())

		// Reset login attempts + unblock user
		_ = s.sessionStore.ResetBlockedUserState(ctx, user.Email, user.Phone)

		// Log successful password change
		go func() {
			_ = s.actionLogService.LogAction(context.Background(), &LogActionParams{
				UserID:      userID,
				Action:      "password_change",
				Module:      "users",
				ResourceID:  user.ID.String(),
				Description: fmt.Sprintf("Password changed for user: %s (%s)", user.Username, user.Email),
				OldValue:    map[string]interface{}{"has_password": true},
				NewValue:    map[string]interface{}{"has_password": true},
				IPAddress:   ipAddress,
				UserAgent:   userAgent,
				Status:      "success",
			})
			// Log logout action since password change forces logout
			_ = s.actionLogService.LogAction(context.Background(), &LogActionParams{
				UserID:      userID,
				Action:      "logout",
				Module:      "users",
				ResourceID:  userID.String(),
				Description: "User logged out after password change",
				OldValue:    nil,
				NewValue:    nil,
				IPAddress:   ipAddress,
				UserAgent:   userAgent,
				Status:      "success",
			})
		}()
	}

	return err
}

func (s *userService) AdminResetPassword(ctx context.Context, adminID, targetUserID uuid.UUID, newPassword string) error {
	// Get target user
	user, err := s.userRepo.FindByID(ctx, targetUserID)
	if err != nil {
		return err
	}
	// Block password reset for Super Admins
	if user.IsSuperAdmin && adminID != user.ID {
		return errors.New(i18n.T(ctx, "cannot_reset_super_admin_password"))
	}

	// Block password reset for AD users
	if user.IsADUser {
		return errors.New(i18n.T(ctx, "cannot_reset_ad_password"))
	}

	// Hash new password
	hashedPassword, err := utils.HashPassword(newPassword)
	if err != nil {
		return err
	}

	user.Password = hashedPassword

	if err := s.userRepo.Update(ctx, user); err != nil {
		return err
	}

	// Kill all sessions
	_ = s.sessionStore.DeleteAllUserSessions(ctx, user.ID.String())

	// Prevent old refresh tokens from creating new sessions after password reset
	_ = s.sessionStore.SetPasswordChangedAt(ctx, user.ID.String(), s.jwtManager.GetRefreshExpiration())

	//Reset login attempts + unblock
	_ = s.sessionStore.ResetBlockedUserState(ctx, user.Email, user.Phone)

	go func() {
		_ = s.actionLogService.LogAction(context.Background(), &LogActionParams{
			UserID:      adminID,
			Action:      "admin_password_reset",
			Module:      "users",
			ResourceID:  user.ID.String(),
			Description: fmt.Sprintf("Admin reset password for user: %s (%s)", user.Username, user.Email),
			Status:      "success",
		})
	}()

	return nil
}

func (s *userService) UploadAvatar(ctx context.Context, userID uuid.UUID, file multipart.File, header *multipart.FileHeader) (*models.UserResponse, error) {
	user, err := s.userRepo.FindByIDWithRelations(ctx, userID)
	if err != nil {
		return nil, err
	}

	oldAvatar := user.Avatar

	if user.Avatar != "" {
		_ = s.storage.DeleteFile(ctx, user.Avatar)
	}

	avatarPath, err := s.storage.UploadAvatar(ctx, file, header, userID.String())
	if err != nil {
		return nil, err
	}

	user.Avatar = s.storage.GetPublicURL(avatarPath, s.config.MinIO.Endpoint)
	if err := s.userRepo.Update(ctx, user); err != nil {
		return nil, err
	}

	response := models.ToUserResponse(user)

	// Log avatar upload
	uploadActorID := userID
	if id, ok := ctx.Value(constants.ContextKeys.ActorID).(uuid.UUID); ok {
		uploadActorID = id
	}
	uploadIP, _ := ctx.Value(constants.ContextKeys.IP_ADDRESS).(string)
	uploadUA, _ := ctx.Value(constants.ContextKeys.USER_AGENT).(string)
	go func() {
		_ = s.actionLogService.LogAction(context.Background(), &LogActionParams{
			UserID:      uploadActorID,
			Action:      "avatar_upload",
			Module:      "users",
			ResourceID:  userID.String(),
			Description: fmt.Sprintf("Avatar uploaded for user: %s (%s)", user.Username, user.Email),
			OldValue:    map[string]string{"avatar": oldAvatar},
			NewValue:    map[string]string{"avatar": user.Avatar},
			IPAddress:   uploadIP,
			UserAgent:   uploadUA,
			Status:      "success",
		})
	}()

	return &response, nil
}

func (s *userService) UpdateAvatar(ctx context.Context, userID uuid.UUID, avatarURL string) error {
	user, err := s.userRepo.FindByID(ctx, userID)
	if err != nil {
		return err
	}

	oldAvatar := user.Avatar
	newAvatar := s.storage.GetPublicURL(avatarURL, s.config.MinIO.Endpoint)
	user.Avatar = newAvatar

	err = s.userRepo.Update(ctx, user)

	updateActorID := userID
	if id, ok := ctx.Value(constants.ContextKeys.ActorID).(uuid.UUID); ok {
		updateActorID = id
	}
	updateIP, _ := ctx.Value(constants.ContextKeys.IP_ADDRESS).(string)
	updateUA, _ := ctx.Value(constants.ContextKeys.USER_AGENT).(string)
	if err == nil {
		// Log avatar update
		go func() {
			_ = s.actionLogService.LogAction(context.Background(), &LogActionParams{
				UserID:      updateActorID,
				Action:      "avatar_update",
				Module:      "users",
				ResourceID:  userID.String(),
				Description: fmt.Sprintf("Avatar updated for user: %s (%s)", user.Username, user.Email),
				OldValue:    map[string]string{"avatar": oldAvatar},
				NewValue:    map[string]string{"avatar": newAvatar},
				IPAddress:   updateIP,
				UserAgent:   updateUA,
				Status:      "success",
			})
		}()
	}

	return err
}

func (s *userService) ExistsByPhoneAndName(ctx context.Context, phone string, name ...string) (bool, error) {
	return s.userRepo.ExistsByPhoneAndName(ctx, phone, name...)
}

func (s *userService) FindByIDWithRelations(ctx context.Context, userID uuid.UUID) (*models.User, error) {
	return s.userRepo.FindByIDWithRelations(ctx, userID)
}

func (s *userService) DeleteUser(ctx context.Context) error {
	userID := ctx.Value(constants.ContextKeys.UserID).(uuid.UUID)
	ipAddress, _ := ctx.Value(constants.ContextKeys.IP_ADDRESS).(string)
	userAgent, _ := ctx.Value(constants.ContextKeys.USER_AGENT).(string)
	user, err := s.userRepo.FindByID(ctx, userID)
	if err != nil {
		// Log failed deletion attempt
		go func() {
			_ = s.actionLogService.LogAction(context.Background(), &LogActionParams{
				UserID:      userID,
				Action:      "delete_failed",
				Module:      "users",
				ResourceID:  userID.String(),
				Description: fmt.Sprintf("Failed user deletion attempt for user ID: %s", userID.String()),
				OldValue:    nil,
				NewValue:    nil,
				IPAddress:   ipAddress,
				UserAgent:   userAgent,
				Status:      "failed",
			})
		}()
		return err
	}

	// Block deletion while the user is still assigned to any live incident.
	if n, err := s.userRepo.CountAssignedIncidents(ctx, userID); err != nil {
		return err
	} else if n > 0 {
		return ErrUserAssigned
	}

	// Deactivate instead of soft-deleting: keep the row alive (IsActive=false).
	// Avatar file is preserved so a re-activated account keeps it.
	_ = s.sessionStore.DeleteAllUserSessions(ctx, userID.String())

	err = s.userRepo.UpdateProfile(ctx, map[string]interface{}{
		"id":        userID,
		"is_active": false,
	})

	if err == nil {
		// Log successful user deletion
		go func() {
			_ = s.actionLogService.LogAction(context.Background(), &LogActionParams{
				UserID:      userID,
				Action:      "delete",
				Module:      "users",
				ResourceID:  userID.String(),
				Description: fmt.Sprintf("User deleted: %s (%s)", user.Username, user.Email),
				OldValue:    user,
				NewValue:    nil,
				IPAddress:   ipAddress,
				UserAgent:   userAgent,
				Status:      "success",
			})
		}()
	} else {
		// Log failed deletion
		go func() {
			_ = s.actionLogService.LogAction(context.Background(), &LogActionParams{
				UserID:      userID,
				Action:      "delete_failed",
				Module:      "users",
				ResourceID:  userID.String(),
				Description: fmt.Sprintf("Failed to delete user: %s (%s)", user.Username, user.Email),
				OldValue:    nil,
				NewValue:    nil,
				IPAddress:   ipAddress,
				UserAgent:   userAgent,
				Status:      "failed",
			})
		}()
	}

	return err
}

func (s *userService) AdminDeleteUser(ctx context.Context, userID uuid.UUID) error {
	// Get the authenticated user (who is performing this action) from context
	actorID, ok := ctx.Value(constants.ContextKeys.UserID).(uuid.UUID)
	if !ok {
		return errors.New(i18n.T(ctx, "unauthorized_not_authenticated"))
	}
	ipAddress, _ := ctx.Value(constants.ContextKeys.IP_ADDRESS).(string)
	userAgent, _ := ctx.Value(constants.ContextKeys.USER_AGENT).(string)

	user, err := s.userRepo.FindByID(ctx, userID)
	if err != nil {
		return err
	}

	// Block deletion while the user is still assigned to any live incident.
	if n, err := s.userRepo.CountAssignedIncidents(ctx, userID); err != nil {
		return err
	} else if n > 0 {
		return ErrUserAssigned
	}

	// Store user info for logging before deactivation
	username := user.Username
	email := user.Email

	// Deactivate instead of soft-deleting: keep the row alive (IsActive=false).
	// Avatar file is preserved so a re-activated account keeps it.
	_ = s.sessionStore.DeleteAllUserSessions(ctx, userID.String())

	err = s.userRepo.UpdateProfile(ctx, map[string]interface{}{
		"id":        userID,
		"is_active": false,
	})

	if err == nil {
		// Log successful admin user deletion with complete details
		go func() {
			_ = s.actionLogService.LogAction(context.Background(), &LogActionParams{
				UserID:      actorID,
				Action:      "delete",
				Module:      "users",
				ResourceID:  userID.String(),
				Description: fmt.Sprintf("Admin deleted user: %s (%s)", username, email),
				OldValue:    map[string]interface{}{"username": username, "email": email, "id": userID.String()},
				NewValue:    nil,
				IPAddress:   ipAddress,
				UserAgent:   userAgent,
				Status:      "success",
			})
		}()
	}

	return err
}

func (s *userService) ListUsers(ctx context.Context, page, limit int, search, phone, extension, callStatus string, roleIDs, departmentIDs, locationIDs, classificationIDs []uuid.UUID, withIncident, withIVR bool, strictDepartment ...bool) ([]models.UserResponse, int64, error) {
	users, total, err := s.userRepo.List(ctx, page, limit, search, phone, extension, callStatus, roleIDs, departmentIDs, locationIDs, classificationIDs, withIncident, withIVR, strictDepartment...)
	if err != nil {
		return nil, 0, err
	}

	responses := make([]models.UserResponse, len(users))
	for i, user := range users {
		responses[i] = models.ToUserResponse(&user)
	}

	return responses, total, nil
}

func (s *userService) GetUserByID(ctx context.Context, userID uuid.UUID) (*models.UserResponse, error) {
	user, err := s.userRepo.FindByIDWithRelations(ctx, userID)
	if err != nil {
		return nil, err
	}

	response := models.ToUserResponse(user)
	return &response, nil
}

func (s *userService) FindMatchingUsers(ctx context.Context, roleIDs []uuid.UUID, classificationID, locationID, departmentID, excludeUserID *uuid.UUID) ([]models.UserResponse, error) {
	users, err := s.userRepo.FindMatching(ctx, roleIDs, classificationID, locationID, departmentID, excludeUserID)
	if err != nil {
		return nil, err
	}

	responses := make([]models.UserResponse, len(users))
	for i, user := range users {
		responses[i] = models.ToUserResponse(&user)
	}

	return responses, nil
}

func (s *userService) UpdateUserCallStatus(ctx context.Context, extension string, status string) (interface{}, error) {
	// The status endpoint exposes "available" as the ready-for-work state, but
	// round-robin (FindMatchingOnline) matches call_status = 'online'. Map the
	// client vocabulary onto the persisted enum so status drives assignment.
	if strings.EqualFold(status, "available") {
		status = string(models.CallStatusOnline) // "online"
	}

	//Setup cache key
	cacheKey := fmt.Sprintf("USER_CALL_STATUS:%s", extension)
	var cachedStatus map[string]interface{}

	err := s.sessionStore.Get(ctx, cacheKey, &cachedStatus)
	if err == nil {
		if st, ok := cachedStatus["call_status"].(string); ok && st == status {
			return cachedStatus, nil
		}
	}

	// Fetch User from DB
	user, err := s.userRepo.FindByExtension(ctx, extension)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", i18n.Tf(ctx, "user_extension_not_found", extension), err)
	}

	// Update DB if status is different. Use UpdateProfile (targeted map update)
	// because the generic Update method omits call_status from its field list,
	// so it would never persist the column that round-robin reads.
	if string(user.CallStatus) != status {
		if err := s.userRepo.UpdateProfile(ctx, map[string]interface{}{
			"id":          user.ID,
			"call_status": status,
		}); err != nil {
			return nil, err
		}
		user.CallStatus = models.CallStatus(status) // reflect new value in the response
	}

	// Prepare Response
	result := map[string]interface{}{
		"id":          user.ID,
		"extension":   user.Extension,
		"call_status": user.CallStatus,
		"updated_at":  user.UpdatedAt,
	}

	//  Update Cache
	err = s.sessionStore.Set(ctx, cacheKey, result, 15*time.Minute)
	if err != nil {
		fmt.Printf("Failed to update cache: %v\n", err)
	}

	return result, nil
}

// Helper to keep code clean
func (s *userService) updateStatusCache(ctx context.Context, key string, data interface{}) {
	if bytes, err := json.Marshal(data); err == nil {
		_ = s.sessionStore.Set(ctx, key, string(bytes), 15*time.Minute)
	}
}

func (s *userService) FindByExtension(ctx context.Context, extension string) (*models.User, error) {
	user, err := s.userRepo.FindByExtension(ctx, extension)
	if err != nil {
		return nil, err
	}
	return user, nil
}

func (s *userService) FindByExtOrPhone(ctx context.Context, phone string) (*models.User, error) {
	return s.userRepo.FindByExtOrPhone(ctx, phone)
}

func (s *userService) FindByExtOrPhoneList(ctx context.Context, phones []string) ([]models.User, error) {
	return s.userRepo.FindByExtOrPhoneList(ctx, phones)
}

func (s *userService) GetUserByEmail(ctx context.Context, email string) (*models.User, error) {
	return s.userRepo.FindByEmail(ctx, email)
}

func (s *userService) GetUserByMobile(ctx context.Context, phone string) (*models.User, error) {
	return s.userRepo.FindByMobile(ctx, phone)
}
func (s *userService) GetUserByUsername(ctx context.Context, username string) (*models.User, error) {
	return s.userRepo.FindByUsername(ctx, username)
}

// CheckExistingIdentifiers reports, in a single query, which of the supplied emails, usernames and
// phones already belong to a user. Intended for bulk paths that would otherwise probe one row at a time.
func (s *userService) CheckExistingIdentifiers(ctx context.Context, emails, usernames, phones []string) (*models.ExistingUserIdentifiers, error) {
	rows, err := s.userRepo.FindExistingIdentifiers(ctx, emails, usernames, phones)
	if err != nil {
		return nil, err
	}

	existing := models.NewExistingUserIdentifiers(len(rows))
	for _, row := range rows {
		existing.Add(row.Email, row.Username, row.Phone)
	}
	return existing, nil
}

// collectDeptAttributes fetches a department by ID and returns its location and classification IDs.
func (s *userService) collectDeptAttributes(ctx context.Context, deptID uuid.UUID) (locIDs, clsIDs []uuid.UUID) {
	dept, err := s.departmentRepo.FindByID(ctx, deptID)
	if err != nil {
		return nil, nil
	}
	for _, l := range dept.Locations {
		locIDs = append(locIDs, l.ID)
	}
	for _, c := range dept.Classifications {
		clsIDs = append(clsIDs, c.ID)
	}
	return
}

// syncDepartmentAttributesToUser appends locations and classifications from newly
// added departments to the user. This is a safety net that runs AFTER the frontend's
// selections have been saved — it ensures that department-derived items the frontend
// couldn't see (due to scope filtering / limitIdsToScope) are still granted.
//
// Removal of locations from removed departments is handled by the frontend: when a
// department is removed, the frontend regenerates the location/classification lists
// and sends the updated set via AssignLocations/AssignClassifications (Replace).
func (s *userService) syncDepartmentAttributesToUser(ctx context.Context, userID uuid.UUID, oldDeptIDs, newDeptIDs []uuid.UUID) error {
	oldSet := make(map[uuid.UUID]bool, len(oldDeptIDs))
	for _, id := range oldDeptIDs {
		oldSet[id] = true
	}

	// Find departments that were added (present in new but not in old)
	var addedDeptIDs []uuid.UUID
	for _, id := range newDeptIDs {
		if !oldSet[id] {
			addedDeptIDs = append(addedDeptIDs, id)
		}
	}

	if len(addedDeptIDs) == 0 {
		return nil
	}

	// Collect all locations/classifications from added departments and append
	addedLocs := make(map[uuid.UUID]bool)
	addedCls := make(map[uuid.UUID]bool)
	for _, deptID := range addedDeptIDs {
		locs, cls := s.collectDeptAttributes(ctx, deptID)
		for _, id := range locs {
			addedLocs[id] = true
		}
		for _, id := range cls {
			addedCls[id] = true
		}
	}
	if len(addedLocs) > 0 {
		ids := make([]uuid.UUID, 0, len(addedLocs))
		for id := range addedLocs {
			ids = append(ids, id)
		}
		if err := s.userRepo.AppendLocations(ctx, userID, ids); err != nil {
			fmt.Printf("Warning: failed to append locations from added departments: %v\n", err)
		}
	}
	if len(addedCls) > 0 {
		ids := make([]uuid.UUID, 0, len(addedCls))
		for id := range addedCls {
			ids = append(ids, id)
		}
		if err := s.userRepo.AppendClassifications(ctx, userID, ids); err != nil {
			fmt.Printf("Warning: failed to append classifications from added departments: %v\n", err)
		}
	}

	return nil
}

// ApplyDepartmentDefaultRoles assigns the roles associated with a department (department_roles)
// to a user, but only ever called from paths that have already confirmed the user has no
// manually assigned role. It never overrides an existing/explicit role assignment.
// A department-scoped actor (non-super-admin with "users:view_department_only") can never
// cause a role that grants department-manager scope to be inherited, mirroring the same
// restriction already enforced for explicit/manual role assignment.
func (s *userService) ApplyDepartmentDefaultRoles(ctx context.Context, userID uuid.UUID, departmentID uuid.UUID, actorID uuid.UUID) {
	dept, err := s.departmentRepo.FindByID(ctx, departmentID)
	if err != nil || len(dept.Roles) == 0 {
		return
	}

	actor, _ := s.userRepo.FindByIDWithRelations(ctx, actorID)
	restrictScope := actor != nil && !actor.IsSuperAdmin && actor.HasPermission("users:view_department_only")

	roleIDs := make([]uuid.UUID, 0, len(dept.Roles))
	for _, role := range dept.Roles {
		if restrictScope {
			var fullRole models.Role
			if err := s.db.Preload("Permissions").First(&fullRole, "id = ?", role.ID).Error; err == nil {
				grantsDeptScope := false
				for _, perm := range fullRole.Permissions {
					if perm.Code == "users:view_department_only" {
						grantsDeptScope = true
						break
					}
				}
				if grantsDeptScope {
					continue
				}
			}
		}
		roleIDs = append(roleIDs, role.ID)
	}

	if len(roleIDs) == 0 {
		return
	}

	if err := s.userRepo.AssignRoles(ctx, userID, roleIDs); err != nil {
		fmt.Printf("Warning: failed to inherit department default roles: %v\n", err)
	}
}

// Helper function to compare UUID slices
func equalUUIDSlices(a, b []uuid.UUID) bool {
	if len(a) != len(b) {
		return false
	}

	// Create maps to count occurrences
	countA := make(map[uuid.UUID]int)
	countB := make(map[uuid.UUID]int)

	for _, id := range a {
		countA[id]++
	}
	for _, id := range b {
		countB[id]++
	}

	// Compare counts
	if len(countA) != len(countB) {
		return false
	}

	for id, count := range countA {
		if countB[id] != count {
			return false
		}
	}

	return true
}

func (s *userService) ForgotPassword(ctx context.Context, req *models.ForgotPasswordRequest) (string, error) {

	channel := strings.ToLower(strings.TrimSpace(req.Channel))
	value := strings.TrimSpace(req.Value)

	var user *models.User
	var err error

	if channel == "email" {
		user, err = s.userRepo.FindByEmail(ctx, value)
	} else {
		user, err = s.userRepo.FindByMobile(ctx, value)
	}

	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return "", fmt.Errorf("%s", i18n.T(ctx, "failed_to_fetch_user"))
	}

	if user == nil {
		return "", fmt.Errorf("%s", i18n.T(ctx, "user_not_found"))
	}

	// Block AD users from resetting password
	if user.IsADUser {
		return "", errors.New(i18n.T(ctx, "cannot_reset_ad_password_it"))
	}

	//Generate OTP
	otp, err := s.otpService.GenerateOTP()
	if err != nil {
		return "", err
	}
	sessionID := uuid.New().String()
	//Send OTP
	if channel == "email" {
		err = s.SendEmailOTP(ctx, value, otp)
	} else {
		err = s.SendSMSOTP(ctx, value, otp)
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", i18n.T(ctx, "failed_to_send_otp"), err)
	}

	if err != nil {
		return "", err
	}
	//Store OTP in Redis
	key := fmt.Sprintf("otp:%s:%s:%s", channel, value, sessionID)
	data := map[string]interface{}{
		"value":     value,
		"otp":       HashOTP(otp),
		"channel":   channel,
		"attempts":  0,
		"createdAt": time.Now(),
	}

	jsonData, _ := json.Marshal(data)
	err = s.redis.Set(ctx, key, jsonData, 5*time.Minute).Err()
	if err != nil {
		return "", err
	}
	return sessionID, nil
}

func (s *userService) SendSMSOTP(ctx context.Context, phone, otp string) error {

	if !strings.HasPrefix(phone, "+") {
		return errors.New(i18n.T(ctx, "phone_country_code_required"))
	}
	message := fmt.Sprintf("Your OTP is %s. It is valid for 5 minutes.", otp)
	sid, err := intutils.SendSMS(ctx, phone, message)
	s.logOTPNotification("sms", "twilio", phone, message, sid, err)
	return err
}

// logOTPNotification persists an OTP send attempt (success or failure) to
// notification_logs so it shows up in the Notification Monitoring Dashboard.
// This does not alter the OTP send/verify flow in any way: it runs after the
// send already happened, and a failure here is only logged to stdout — it is
// never returned to the caller, so it cannot affect password reset/login.
func (s *userService) logOTPNotification(channel, provider, recipient, body, providerMessageID string, sendErr error) {
	if s.db == nil {
		return
	}

	status := "sent"
	recipientStatus := "success"
	errorMessage := ""
	failureCode := ""
	if sendErr != nil {
		status = "failed"
		recipientStatus = "failed"
		errorMessage = sendErr.Error()
		failureCode = ClassifyFailureCode(sendErr)
	}

	now := time.Now()
	notification := &models.NotificationLog{
		Channel:      channel,
		Direction:    models.DirectionOutbound,
		Category:     models.CategorySent,
		TemplateCode: "OTP",
		Language:     "en",
		Recipients: models.RecipientArray{{
			Email:             recipient,
			Channel:           recipient,
			Type:              "to",
			Status:            recipientStatus,
			Error:             errorMessage,
			ErrorMessage:      errorMessage,
			FailureCode:       failureCode,
			ProviderMessageID: providerMessageID,
		}},
		Subject:      "OTP",
		Body:         body,
		Status:       status,
		Provider:     provider,
		FailureCode:  failureCode,
		ErrorMessage: errorMessage,
		SentAt:       &now,
		CreatedAt:    now,
		UpdatedAt:    &now,
	}

	if err := s.db.Create(notification).Error; err != nil {
		log.Printf("[UserService] failed to persist OTP notification log: %v", err)
	}
}

func (s *userService) SendEmailOTP(ctx context.Context, toEmail, otp string) error {
	toEmail = strings.TrimSpace(toEmail)
	if toEmail == "" {
		return errors.New(i18n.T(ctx, "email_address_required"))
	}

	if !strings.Contains(toEmail, "@") {
		return errors.New(i18n.Tf(ctx, "invalid_email_format", toEmail))
	}
	subject := "Your OTP for Password Reset"
	// HTML body (avoid leading spaces/newlines issues)
	body := fmt.Sprintf(
		"<html><body>"+
			"<h2>OTP Verification</h2>"+
			"<p>Your OTP is:</p>"+
			"<h1>%s</h1>"+
			"<p>This OTP is valid for 5 minutes.</p>"+
			"</body></html>",
		otp,
	)

	to := []string{toEmail}

	_, err := intutils.SendSMTPWithCCBCC(ctx, to, nil, nil, subject, body, nil)
	s.logOTPNotification("email", "smtp", toEmail, body, "", err)
	// Logging
	if err != nil {
		log.Println("SMTP ERROR:", err)
		return err
	}

	log.Println("Email sent successfully")
	return nil
}

func (s *userService) VerifyOTPForReset(ctx context.Context, req *models.VerifyOTPRequest) (string, error) {

	channel := strings.ToLower(strings.TrimSpace(req.Channel))
	value := strings.TrimSpace(req.Value)
	var key string
	if channel == "email" {
		key = "otp:email:" + value + ":" + req.SessionID
	} else {
		key = "otp:sms:" + value + ":" + req.SessionID
	}
	val, err := s.redis.Get(ctx, key).Result()
	if err != nil {
		return "", fmt.Errorf("%s", i18n.T(ctx, "otp_expired_invalid_session"))
	}
	var data models.RedisOTPData
	if err := json.Unmarshal([]byte(val), &data); err != nil {
		return "", fmt.Errorf("%s", i18n.T(ctx, "invalid_stored_otp"))
	}
	maxVerifyStr := os.Getenv("VERIFY_OTP_MAX_ATTEMPT")
	maxVerify, _ := strconv.Atoi(maxVerifyStr)
	if maxVerify == 0 {
		maxVerify = 3
	}

	if data.Attempts >= maxVerify {
		s.redis.Del(ctx, key)
		return "", fmt.Errorf("%s", i18n.T(ctx, "max_verify_attempts_exceeded"))
	}

	if data.OTP != HashOTP(req.OTP) {
		data.Attempts++
		updated, _ := json.Marshal(data)
		ttl, _ := s.redis.TTL(ctx, key).Result()
		s.redis.Set(ctx, key, updated, ttl)

		return "", fmt.Errorf("%s", i18n.T(ctx, "invalid_otp"))
	}

	//enerate reset token
	resetToken := uuid.New().String()
	resetKey := "reset:" + resetToken

	s.redis.Set(ctx, resetKey, value, 10*time.Minute)
	s.redis.Del(ctx, key)
	return resetToken, nil
}

func (s *userService) ResetPassword(ctx context.Context, resetToken, newPassword string) error {

	val, err := s.redis.Get(ctx, "reset:"+resetToken).Result()
	if err != nil {
		return fmt.Errorf("%s", i18n.T(ctx, "invalid_or_expired_token"))
	}

	var user *models.User
	if strings.Contains(val, "@") {
		user, err = s.userRepo.FindByEmail(ctx, val)
		// log.Printf("User found by email: %s\n", val, user)
	} else {
		user, err = s.userRepo.FindByMobile(ctx, val)
		// log.Printf("User found by mobile: %s\n", val, user)
	}
	if err != nil || user == nil {
		return fmt.Errorf("%s", i18n.T(ctx, "user_not_found"))
	}

	//hash password
	hashed, err := utils.HashPassword(newPassword)
	if err != nil {
		return err
	}

	// update struct
	user.Password = hashed

	// call repo correctly
	if err := s.userRepo.Update(ctx, user); err != nil {
		return err
	}

	// delete reset token after use
	s.redis.Del(ctx, "reset:"+resetToken)

	return nil
}
