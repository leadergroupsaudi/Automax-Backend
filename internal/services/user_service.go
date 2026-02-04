package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"time"

	"github.com/automax/backend/internal/config"
	"github.com/automax/backend/internal/database"
	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/internal/storage"
	"github.com/automax/backend/pkg/utils"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type UserService interface {
	Register(ctx context.Context, req *models.UserRegisterRequest) (*models.AuthResponse, error)
	Login(ctx context.Context, req *models.UserLoginRequest) (*models.AuthResponse, error)
	RefreshToken(ctx context.Context, refreshToken string) (*models.AuthResponse, error)
	Logout(ctx context.Context, token string) error
	GetProfile(ctx context.Context, userID uuid.UUID) (*models.UserResponse, error)
	UpdateProfile(ctx context.Context, userID uuid.UUID, req *models.UserUpdateRequest) (*models.UserResponse, error)
	ChangePassword(ctx context.Context, userID uuid.UUID, req *models.ChangePasswordRequest) error
	UploadAvatar(ctx context.Context, userID uuid.UUID, file multipart.File, header *multipart.FileHeader) (*models.UserResponse, error)
	UpdateAvatar(ctx context.Context, userID uuid.UUID, avatarURL string) error
	DeleteUser(ctx context.Context, userID uuid.UUID) error
	ListUsers(ctx context.Context, page, limit int) ([]models.UserResponse, int64, error)
	GetUserByID(ctx context.Context, userID uuid.UUID) (*models.UserResponse, error)
	GetUserByEmail(ctx context.Context, email string) (*models.User, error)
	GetUserByUsername(ctx context.Context, username string) (*models.User, error)
	FindMatchingUsers(ctx context.Context, roleID, classificationID, locationID, departmentID, excludeUserID *uuid.UUID) ([]models.UserResponse, error)
	UpdateUserCallStatus(ctx context.Context, extension string, status string) (interface{}, error)
	FindByExtension(ctx context.Context, extension string) (*models.User, error)
}

type userService struct {
	userRepo     repository.UserRepository
	jwtManager   *utils.JWTManager
	sessionStore *database.SessionStore
	storage      *storage.MinIOStorage
	config       *config.Config
}

func NewUserService(
	userRepo repository.UserRepository,
	jwtManager *utils.JWTManager,
	sessionStore *database.SessionStore,
	storage *storage.MinIOStorage,
	cfg *config.Config,
) UserService {
	return &userService{
		userRepo:     userRepo,
		jwtManager:   jwtManager,
		sessionStore: sessionStore,
		storage:      storage,
		config:       cfg,
	}
}

func (s *userService) Register(ctx context.Context, req *models.UserRegisterRequest) (*models.AuthResponse, error) {
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

	return &models.AuthResponse{
		User:  models.ToUserResponse(user),
		Token: token,
	}, nil
}

func (s *userService) Login(ctx context.Context, req *models.UserLoginRequest) (*models.AuthResponse, error) {
	user, err := s.userRepo.FindByEmailWithRelations(ctx, req.Email)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("invalid credentials")
		}
		return nil, err
	}

	if !utils.CheckPassword(req.Password, user.Password) {
		return nil, errors.New("invalid credentials")
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

	return &models.AuthResponse{
		User:         models.ToUserResponse(user),
		Token:        tokenPair.AccessToken,
		RefreshToken: tokenPair.RefreshToken,
		ExpiresIn:    tokenPair.ExpiresIn,
	}, nil
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

func (s *userService) Logout(ctx context.Context, token string) error {
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

func (s *userService) UpdateProfile(ctx context.Context, userID uuid.UUID, req *models.UserUpdateRequest) (*models.UserResponse, error) {
	user, err := s.userRepo.FindByIDWithRelations(ctx, userID)
	if err != nil {
		return nil, err
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

	if req.Extension != "" {
		user.Extension = req.Extension
	}

	if err := s.userRepo.Update(ctx, user); err != nil {
		return nil, err
	}

	// Update roles if provided
	if len(req.RoleIDs) > 0 {
		s.userRepo.AssignRoles(ctx, user.ID, req.RoleIDs)
	}

	// Update departments if provided
	if len(req.DepartmentIDs) > 0 {
		s.userRepo.AssignDepartments(ctx, user.ID, req.DepartmentIDs)
	}

	// Update locations if provided
	if len(req.LocationIDs) > 0 {
		s.userRepo.AssignLocations(ctx, user.ID, req.LocationIDs)
	}

	// Update classifications if provided
	if len(req.ClassificationIDs) > 0 {
		s.userRepo.AssignClassifications(ctx, user.ID, req.ClassificationIDs)
	}

	// Reload with relations
	user, _ = s.userRepo.FindByIDWithRelations(ctx, user.ID)

	response := models.ToUserResponse(user)
	return &response, nil
}

func (s *userService) ChangePassword(ctx context.Context, userID uuid.UUID, req *models.ChangePasswordRequest) error {
	user, err := s.userRepo.FindByID(ctx, userID)
	if err != nil {
		return err
	}

	if !utils.CheckPassword(req.OldPassword, user.Password) {
		return errors.New("current password is incorrect")
	}

	hashedPassword, err := utils.HashPassword(req.NewPassword)
	if err != nil {
		return err
	}

	user.Password = hashedPassword
	return s.userRepo.Update(ctx, user)
}

func (s *userService) UploadAvatar(ctx context.Context, userID uuid.UUID, file multipart.File, header *multipart.FileHeader) (*models.UserResponse, error) {
	user, err := s.userRepo.FindByIDWithRelations(ctx, userID)
	if err != nil {
		return nil, err
	}

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
	return &response, nil
}

func (s *userService) UpdateAvatar(ctx context.Context, userID uuid.UUID, avatarURL string) error {
	user, err := s.userRepo.FindByID(ctx, userID)
	if err != nil {
		return err
	}

	user.Avatar = s.storage.GetPublicURL(avatarURL, s.config.MinIO.Endpoint)
	return s.userRepo.Update(ctx, user)
}

func (s *userService) DeleteUser(ctx context.Context, userID uuid.UUID) error {
	user, err := s.userRepo.FindByID(ctx, userID)
	if err != nil {
		return err
	}

	if user.Avatar != "" {
		_ = s.storage.DeleteFile(ctx, user.Avatar)
	}

	_ = s.sessionStore.DeleteUserSession(ctx, userID.String())

	return s.userRepo.Delete(ctx, userID)
}

func (s *userService) ListUsers(ctx context.Context, page, limit int) ([]models.UserResponse, int64, error) {
	users, total, err := s.userRepo.List(ctx, page, limit)
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

func (s *userService) FindMatchingUsers(ctx context.Context, roleID, classificationID, locationID, departmentID, excludeUserID *uuid.UUID) ([]models.UserResponse, error) {
	users, err := s.userRepo.FindMatching(ctx, roleID, classificationID, locationID, departmentID, excludeUserID)
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
func (s *userService) GetUserByEmail(ctx context.Context, email string) (*models.User, error) {
	return s.userRepo.FindByEmail(ctx, email)
}

func (s *userService) GetUserByUsername(ctx context.Context, username string) (*models.User, error) {
	return s.userRepo.FindByUsername(ctx, username)
}
