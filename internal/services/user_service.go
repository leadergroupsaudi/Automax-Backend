package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"mime/multipart"
	"time"

	"github.com/automax/backend/internal/config"
	"github.com/automax/backend/internal/database"
	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/internal/storage"
	"github.com/automax/backend/pkg/constants"
	"github.com/automax/backend/pkg/utils"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type UserService interface {
	Register(ctx context.Context, req *models.UserRegisterRequest) (*models.AuthResponse, error)
	Login(ctx context.Context, req *models.UserLoginRequest) (*models.AuthResponse, error)
	RefreshToken(ctx context.Context, refreshToken string) (*models.AuthResponse, error)
	Logout(ctx context.Context) error
	GetProfile(ctx context.Context, userID uuid.UUID) (*models.UserResponse, error)
	UpdateAdminProfile(ctx context.Context, userID uuid.UUID, req *models.UserUpdateRequest) (*models.UserResponse, error)
	ChangePassword(ctx context.Context, userID uuid.UUID, req *models.ChangePasswordRequest) error
	UploadAvatar(ctx context.Context, userID uuid.UUID, file multipart.File, header *multipart.FileHeader) (*models.UserResponse, error)
	UpdateAvatar(ctx context.Context, userID uuid.UUID, avatarURL string) error
	DeleteUser(ctx context.Context) error
	AdminDeleteUser(ctx context.Context, userID uuid.UUID) error
	ListUsers(ctx context.Context, page, limit int, search string, roleIDs, departmentIDs, locationIDs, classificationIDs []uuid.UUID) ([]models.UserResponse, int64, error)
	GetUserByID(ctx context.Context, userID uuid.UUID) (*models.UserResponse, error)
	GetUserByEmail(ctx context.Context, email string) (*models.User, error)
	GetUserByMobile(ctx context.Context, phone string) (*models.User, error)
	GetUserByUsername(ctx context.Context, username string) (*models.User, error)
	FindMatchingUsers(ctx context.Context, roleIDs []uuid.UUID, classificationID, locationID, departmentID, excludeUserID *uuid.UUID) ([]models.UserResponse, error)
	UpdateUserCallStatus(ctx context.Context, extension string, status string) (interface{}, error)
	FindByExtension(ctx context.Context, extension string) (*models.User, error)
	UpdateProfile(ctx context.Context, req *models.UserUpdateRequest) (*models.UserResponse, error)
	GenerateTokenViaUserID(ctx context.Context, mobile uuid.UUID) (*models.AuthResponse, error)
	ValidateMobileForLogin(ctx context.Context, phone string) (*models.UserResponse, error)
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
	}
}

func (s *userService) Register(ctx context.Context, req *models.UserRegisterRequest) (*models.AuthResponse, error) {
	// Get the authenticated user (admin creating this user) from context
	actorID, _ := ctx.Value(constants.ContextKeys.UserID).(uuid.UUID)
	ipAddress, _ := ctx.Value(constants.ContextKeys.IP_ADDRESS).(string)
	userAgent, _ := ctx.Value(constants.ContextKeys.USER_AGENT).(string)

	exists, err := s.userRepo.ExistsByEmail(ctx, req.Email)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, errors.New("email already exists")
	}

	exists, err = s.userRepo.ExistsByUsername(ctx, req.Username)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, errors.New("username already exists")
	}

	hashedPassword, err := utils.HashPassword(req.Password)
	if err != nil {
		return nil, err
	}

	user := &models.User{
		Email:        req.Email,
		Username:     req.Username,
		Password:     hashedPassword,
		FirstName:    req.FirstName,
		LastName:     req.LastName,
		Phone:        req.Phone,
		Extension:    req.Extension,
		DepartmentID: req.DepartmentID,
		LocationID:   req.LocationID,
		IsActive:     true,
	}

	if err := s.userRepo.Create(ctx, user); err != nil {
		return nil, err
	}

	// Assign roles if provided
	if len(req.RoleIDs) > 0 {
		s.userRepo.AssignRoles(ctx, user.ID, req.RoleIDs)
	}

	// Assign departments if provided
	if len(req.DepartmentIDs) > 0 {
		s.userRepo.AssignDepartments(ctx, user.ID, req.DepartmentIDs)
	}

	// Assign locations if provided
	if len(req.LocationIDs) > 0 {
		s.userRepo.AssignLocations(ctx, user.ID, req.LocationIDs)
	}

	// Assign classifications if provided
	if len(req.ClassificationIDs) > 0 {
		s.userRepo.AssignClassifications(ctx, user.ID, req.ClassificationIDs)
	}

	// Sync classifications and locations from departments
	if req.DepartmentID != nil || len(req.DepartmentIDs) > 0 {
		_ = s.syncDepartmentAttributesToUser(ctx, user.ID)
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

func (s *userService) Login(ctx context.Context, req *models.UserLoginRequest) (*models.AuthResponse, error) {
	ipAddress, _ := ctx.Value(constants.ContextKeys.IP_ADDRESS).(string)
	userAgent, _ := ctx.Value(constants.ContextKeys.USER_AGENT).(string)

	loginType := req.LoginType()

	var user *models.User
	var err error

	switch loginType {
	case "email_password":
		// Email/Password login
		user, err = s.userRepo.FindByEmailWithRelations(ctx, req.Email)
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
				return nil, errors.New("invalid credentials")
			}
			return nil, err
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
			return nil, errors.New("invalid credentials")
		}

	case "mobile_otp":
		// Mobile/OTP login
		if s.otpService == nil {
			return nil, errors.New("OTP service not available")
		}

		// First check if user exists with this phone number
		user, err = s.userRepo.FindByPhoneWithRelations(ctx, req.Phone)
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
				return nil, errors.New("no user found for this mobile number")
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
			return nil, errors.New("mobile number is not verified. Please contact administrator to verify your mobile number")
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
			return nil, errors.New("invalid or expired OTP")
		}

	default:
		return nil, errors.New("invalid login credentials")
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
		return nil, errors.New("account is deactivated")
	}

	// Determine primary role for JWT
	role := "user"
	if user.IsSuperAdmin {
		role = "admin"
	} else if len(user.Roles) > 0 {
		role = user.Roles[0].Code
	}

	tokenPair, err := s.jwtManager.GenerateTokenPair(user.ID, user.Email, role)
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

	// Update last login timestamp
	go func() {
		_ = s.userRepo.UpdateLastLogin(context.Background(), user.ID)
	}()

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

	return &models.AuthResponse{
		User:         models.ToUserResponse(user),
		Token:        tokenPair.AccessToken,
		RefreshToken: tokenPair.RefreshToken,
		ExpiresIn:    tokenPair.ExpiresIn,
	}, nil
}

func (s *userService) ValidateMobileForLogin(ctx context.Context, phone string) (*models.UserResponse, error) {
	// Check if user exists with this phone number
	user, err := s.userRepo.FindByPhoneWithRelations(ctx, phone)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("no user found for this mobile number")
		}
		return nil, err
	}

	// Check if mobile number is verified
	if !user.MobileVerified {
		return nil, errors.New("mobile number is not verified. Please contact administrator to verify your mobile number")
	}

	// Check if user is active
	if !user.IsActive {
		return nil, errors.New("account is deactivated")
	}

	// Mobile is verified and user is active - return user info
	resp := models.ToUserResponse(user)
	return &resp, nil
}

// GetUserFor2FA retrieves user information for 2FA processing
func (s *userService) GetUserFor2FA(ctx context.Context, email string) (*models.User, error) {
	return s.userRepo.FindByEmailWithRelations(ctx, email)
}

func (s *userService) RefreshToken(ctx context.Context, refreshToken string) (*models.AuthResponse, error) {
	// Validate the refresh token
	claims, err := s.jwtManager.ValidateRefreshToken(refreshToken)
	if err != nil {
		return nil, errors.New("invalid or expired refresh token")
	}

	// Get user from database
	user, err := s.userRepo.FindByIDWithRelations(ctx, claims.UserID)
	if err != nil {
		return nil, errors.New("user not found")
	}

	if !user.IsActive {
		return nil, errors.New("account is deactivated")
	}

	// Determine primary role for JWT
	role := "user"
	if user.IsSuperAdmin {
		role = "admin"
	} else if len(user.Roles) > 0 {
		role = user.Roles[0].Code
	}

	// Generate new token pair
	tokenPair, err := s.jwtManager.GenerateTokenPair(user.ID, user.Email, role)
	if err != nil {
		return nil, err
	}

	// Update session
	if err := s.sessionStore.SetUserSession(ctx, user.ID.String(), map[string]interface{}{
		"user_id": user.ID,
		"email":   user.Email,
		"role":    role,
	}, s.jwtManager.GetTokenExpiration()); err != nil {
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
					Description: fmt.Sprintf("User logged out"),
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
	return s.sessionStore.BlacklistToken(ctx, token, s.jwtManager.GetTokenExpiration())
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
	tokenPair, err := s.jwtManager.GenerateTokenPair(user.ID, user.Email, constants.USER_ROLE.CITIZEN)
	if err != nil {
		return nil, err
	}

	if err := s.sessionStore.SetUserSession(ctx, user.ID.String(), map[string]interface{}{
		"user_id": user.ID,
		"email":   user.Email,
		"role":    constants.USER_ROLE.CITIZEN,
	}, s.jwtManager.GetTokenExpiration()); err != nil {
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
		return nil, errors.New("unauthorized: user not authenticated")
	}
	ipAddress, _ := ctx.Value(constants.ContextKeys.IP_ADDRESS).(string)
	userAgent, _ := ctx.Value(constants.ContextKeys.USER_AGENT).(string)
	user, err := s.userRepo.FindByIDWithRelations(ctx, userID)
	if err != nil {
		return nil, err
	}

	// Store old values for audit logging
	// IMPORTANT: Capture boolean values BEFORE they're modified
	oldIsActive := user.IsActive
	oldUser := &models.UserUpdateRequest{
		FirstName:         user.FirstName,
		LastName:          user.LastName,
		Username:          user.Username,
		Phone:             user.Phone,
		Extension:         &user.Extension,
		DepartmentID:      user.DepartmentID,
		LocationID:        user.LocationID,
		IsActive:          &oldIsActive, // Capture value, not pointer reference
		DepartmentIDs:     make([]uuid.UUID, len(user.Departments)),
		LocationIDs:       make([]uuid.UUID, len(user.Locations)),
		ClassificationIDs: make([]uuid.UUID, len(user.Classifications)),
		RoleIDs:           make([]uuid.UUID, len(user.Roles)),
	}

	for i, dept := range user.Departments {
		oldUser.DepartmentIDs[i] = dept.ID
	}
	for i, loc := range user.Locations {
		oldUser.LocationIDs[i] = loc.ID
	}
	for i, cls := range user.Classifications {
		oldUser.ClassificationIDs[i] = cls.ID
	}
	for i, role := range user.Roles {
		oldUser.RoleIDs[i] = role.ID
	}

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
			return nil, errors.New("username already exists")
		}
		user.Username = req.Username
	}

	if req.FirstName != "" {
		user.FirstName = req.FirstName
	}
	if req.LastName != "" {
		user.LastName = req.LastName
	}
	if req.Phone != "" {
		user.Phone = req.Phone
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

	if req.Extension != nil {
		user.Extension = *req.Extension
	}

	if err := s.userRepo.Update(ctx, user); err != nil {
		return nil, err
	}

	// Always update associations (even if empty arrays) to allow removal
	// Log role assignments
	oldRoleIDs := make([]uuid.UUID, len(oldUser.RoleIDs))
	copy(oldRoleIDs, oldUser.RoleIDs)
	s.userRepo.AssignRoles(ctx, user.ID, req.RoleIDs)

	// Log role assignment changes
	if !equalUUIDSlices(oldRoleIDs, req.RoleIDs) {
		go func() {
			_ = s.actionLogService.LogAction(context.Background(), &LogActionParams{
				UserID:      actorID,
				Action:      "role_assignment",
				Module:      "users",
				ResourceID:  user.ID.String(),
				Description: fmt.Sprintf("Roles assigned to user: %s (%s)", user.Username, user.Email),
				OldValue:    map[string]interface{}{"role_ids": oldRoleIDs},
				NewValue:    map[string]interface{}{"role_ids": req.RoleIDs},
				IPAddress:   ipAddress,
				UserAgent:   userAgent,
				Status:      "success",
			})
		}()
	}

	// Log department assignments
	oldDepartmentIDs := make([]uuid.UUID, len(oldUser.DepartmentIDs))
	copy(oldDepartmentIDs, oldUser.DepartmentIDs)
	s.userRepo.AssignDepartments(ctx, user.ID, req.DepartmentIDs)

	// Log department assignment changes
	if !equalUUIDSlices(oldDepartmentIDs, req.DepartmentIDs) {
		go func() {
			_ = s.actionLogService.LogAction(context.Background(), &LogActionParams{
				UserID:      actorID,
				Action:      "department_assignment",
				Module:      "users",
				ResourceID:  user.ID.String(),
				Description: fmt.Sprintf("Departments assigned to user: %s (%s)", user.Username, user.Email),
				OldValue:    map[string]interface{}{"department_ids": oldDepartmentIDs},
				NewValue:    map[string]interface{}{"department_ids": req.DepartmentIDs},
				IPAddress:   ipAddress,
				UserAgent:   userAgent,
				Status:      "success",
			})
		}()
	}

	// Only update locations and classifications if departments did NOT change
	// If departments changed, we'll sync from departments instead
	if !departmentsChanged {
		// Log location assignments
		oldLocationIDs := make([]uuid.UUID, len(oldUser.LocationIDs))
		copy(oldLocationIDs, oldUser.LocationIDs)
		s.userRepo.AssignLocations(ctx, user.ID, req.LocationIDs)

		// Log location assignment changes
		if !equalUUIDSlices(oldLocationIDs, req.LocationIDs) {
			go func() {
				_ = s.actionLogService.LogAction(context.Background(), &LogActionParams{
					UserID:      actorID,
					Action:      "location_assignment",
					Module:      "users",
					ResourceID:  user.ID.String(),
					Description: fmt.Sprintf("Locations assigned to user: %s (%s)", user.Username, user.Email),
					OldValue:    map[string]interface{}{"location_ids": oldLocationIDs},
					NewValue:    map[string]interface{}{"location_ids": req.LocationIDs},
					IPAddress:   ipAddress,
					UserAgent:   userAgent,
					Status:      "success",
				})
			}()
		}

		// Log classification assignments
		oldClassificationIDs := make([]uuid.UUID, len(oldUser.ClassificationIDs))
		copy(oldClassificationIDs, oldUser.ClassificationIDs)
		s.userRepo.AssignClassifications(ctx, user.ID, req.ClassificationIDs)

		// Log classification assignment changes
		if !equalUUIDSlices(oldClassificationIDs, req.ClassificationIDs) {
			go func() {
				_ = s.actionLogService.LogAction(context.Background(), &LogActionParams{
					UserID:      actorID,
					Action:      "classification_assignment",
					Module:      "users",
					ResourceID:  user.ID.String(),
					Description: fmt.Sprintf("Classifications assigned to user: %s (%s)", user.Username, user.Email),
					OldValue:    map[string]interface{}{"classification_ids": oldClassificationIDs},
					NewValue:    map[string]interface{}{"classification_ids": req.ClassificationIDs},
					IPAddress:   ipAddress,
					UserAgent:   userAgent,
					Status:      "success",
				})
			}()
		}
	}

	// Sync classifications and locations from departments ONLY if departments changed
	if departmentsChanged {
		_ = s.syncDepartmentAttributesToUser(ctx, user.ID)
	}

	// Reload with relations
	user, _ = s.userRepo.FindByIDWithRelations(ctx, user.ID)

	response := models.ToUserResponse(user)

	// Track if only is_active changed (no other fields)
	onlyStatusChanged := oldUser.IsActive != nil && req.IsActive != nil && *oldUser.IsActive != *req.IsActive

	// Log status change (Active/Inactive) separately for better audit trail
	// This is kept because the middleware logs the general "update" action, but we want
	// a specific "status_change" log when ONLY the active status is changed
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

	// Log general user update with complete old/new values for audit trail
	// This provides detailed field-level changes in the action log
	go func() {
		_ = s.actionLogService.LogAction(context.Background(), &LogActionParams{
			UserID:      actorID,
			Action:      "update",
			Module:      "users",
			ResourceID:  user.ID.String(),
			Description: fmt.Sprintf("User profile updated for user: %s (%s)", user.Username, user.Email),
			OldValue:    oldUser,
			NewValue:    req,
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

	oldUser := &models.UserUpdateRequest{
		FirstName: user.FirstName,
		LastName:  user.LastName,
		Phone:     user.Phone,
	}

	update := map[string]interface{}{}

	if req.FirstName != "" && req.FirstName != user.FirstName {
		user.FirstName = req.FirstName
		update["first_name"] = req.FirstName
	}
	if req.LastName != user.LastName {
		user.LastName = req.LastName
		update["last_name"] = req.LastName
	}
	if req.Phone != user.Phone {
		user.Phone = req.Phone
		update["phone"] = req.Phone
	}

	if req.MobileVerified != nil && *req.MobileVerified != user.MobileVerified {
		user.MobileVerified = *req.MobileVerified
		update["mobile_verified"] = req.MobileVerified
	}

	if req.Extension != nil && *req.Extension != user.Extension {
		user.Extension = *req.Extension
		update["extension"] = req.Extension
	}

	if req.Username != "" && req.Username != user.Username {
		exists, err := s.userRepo.ExistsByUsername(ctx, req.Username)
		if err != nil {
			return nil, err
		}
		if exists {
			return nil, errors.New("username already exists")
		}
		user.Username = req.Username
		update["username"] = req.Username
	}

	if len(update) == 0 {
		return &models.UserResponse{
			ID:        user.ID,
			Username:  user.Username,
			Email:     user.Email,
			FirstName: user.FirstName,
			LastName:  user.LastName,
			Phone:     user.Phone,
			Avatar:    user.Avatar,
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
		_ = s.actionLogService.LogAction(context.Background(), &LogActionParams{
			UserID:      userID,
			Action:      "profile_update",
			Module:      "users",
			ResourceID:  user.ID.String(),
			Description: fmt.Sprintf("User profile updated for user: %s (%s)", user.Username, user.Email),
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

	// Block LDAP-authenticated users from changing their password via the app
	var sessionData map[string]interface{}
	if getErr := s.sessionStore.GetUserSession(ctx, userID.String(), &sessionData); getErr == nil {
		if authSource, ok := sessionData["auth_source"].(string); ok && authSource == "ldap" {
			return errors.New("password cannot be changed for LDAP-authenticated accounts")
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
		return errors.New("current password is incorrect")
	}

	hashedPassword, err := utils.HashPassword(req.NewPassword)
	if err != nil {
		return err
	}

	user.Password = hashedPassword
	err = s.userRepo.Update(ctx, user)

	if err == nil {
		// Invalidate the current session and token so the user must re-authenticate
		_ = s.sessionStore.DeleteUserSession(ctx, userID.String())
		if token, ok := ctx.Value(constants.ContextKeys.Token).(string); ok && token != "" {
			_ = s.sessionStore.BlacklistToken(ctx, token, s.jwtManager.GetTokenExpiration())
		}

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
				Description: fmt.Sprintf("User logged out after password change"),
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

	if user.Avatar != "" {
		_ = s.storage.DeleteFile(ctx, user.Avatar)
	}

	_ = s.sessionStore.DeleteUserSession(ctx, userID.String())

	err = s.userRepo.Delete(ctx, userID)

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
		return errors.New("unauthorized: user not authenticated")
	}
	ipAddress, _ := ctx.Value(constants.ContextKeys.IP_ADDRESS).(string)
	userAgent, _ := ctx.Value(constants.ContextKeys.USER_AGENT).(string)

	user, err := s.userRepo.FindByID(ctx, userID)
	if err != nil {
		return err
	}

	// Store user info for logging before deletion
	username := user.Username
	email := user.Email
	avatar := user.Avatar

	if avatar != "" {
		_ = s.storage.DeleteFile(ctx, avatar)
	}

	_ = s.sessionStore.DeleteUserSession(ctx, userID.String())

	err = s.userRepo.Delete(ctx, userID)

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

func (s *userService) ListUsers(ctx context.Context, page, limit int, search string, roleIDs, departmentIDs, locationIDs, classificationIDs []uuid.UUID) ([]models.UserResponse, int64, error) {
	users, total, err := s.userRepo.List(ctx, page, limit, search, roleIDs, departmentIDs, locationIDs, classificationIDs)
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
		return nil, fmt.Errorf("user with extension %s not found: %w", extension, err)
	}

	// Update DB if status is different
	if string(user.CallStatus) != status {
		user.CallStatus = models.CallStatus(status)
		if err := s.userRepo.Update(ctx, user); err != nil {
			return nil, err
		}
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

func (s *userService) GetUserByEmail(ctx context.Context, email string) (*models.User, error) {
	return s.userRepo.FindByEmail(ctx, email)
}

func (s *userService) GetUserByMobile(ctx context.Context, phone string) (*models.User, error) {
	return s.userRepo.FindByMobile(ctx, phone)
}
func (s *userService) GetUserByUsername(ctx context.Context, username string) (*models.User, error) {
	return s.userRepo.FindByUsername(ctx, username)
}

// syncDepartmentAttributesToUser automatically inherits classifications and locations from user's departments
func (s *userService) syncDepartmentAttributesToUser(ctx context.Context, userID uuid.UUID) error {
	// Get user with all department relationships
	user, err := s.userRepo.FindByIDWithRelations(ctx, userID)
	if err != nil {
		return err
	}

	// Collect all unique classification and location IDs from all departments
	classificationIDMap := make(map[uuid.UUID]bool)
	locationIDMap := make(map[uuid.UUID]bool)

	// Collect from primary department if set
	if user.DepartmentID != nil {
		dept, err := s.departmentRepo.FindByID(ctx, *user.DepartmentID)
		if err == nil {
			for _, classification := range dept.Classifications {
				classificationIDMap[classification.ID] = true
			}
			for _, location := range dept.Locations {
				locationIDMap[location.ID] = true
			}
		}
	}

	// Collect from many-to-many departments
	for _, dept := range user.Departments {
		// Load department with relations
		fullDept, err := s.departmentRepo.FindByID(ctx, dept.ID)
		if err != nil {
			continue
		}
		for _, classification := range fullDept.Classifications {
			classificationIDMap[classification.ID] = true
		}
		for _, location := range fullDept.Locations {
			locationIDMap[location.ID] = true
		}
	}

	// Convert maps to slices
	var classificationIDs []uuid.UUID
	for id := range classificationIDMap {
		classificationIDs = append(classificationIDs, id)
	}

	var locationIDs []uuid.UUID
	for id := range locationIDMap {
		locationIDs = append(locationIDs, id)
	}

	// Assign to user
	if len(classificationIDs) > 0 {
		if err := s.userRepo.AssignClassifications(ctx, userID, classificationIDs); err != nil {
			fmt.Printf("Warning: failed to sync classifications from departments: %v\n", err)
		}
	}

	if len(locationIDs) > 0 {
		if err := s.userRepo.AssignLocations(ctx, userID, locationIDs); err != nil {
			fmt.Printf("Warning: failed to sync locations from departments: %v\n", err)
		}
	}

	return nil
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
