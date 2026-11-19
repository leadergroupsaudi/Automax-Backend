package services

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/pkg/i18n"
	"github.com/automax/backend/pkg/utils"
	"github.com/google/uuid"
)

// ErrInvalidPasscodeLogin is returned for every passcode-login failure (unknown
// driver, inactive account, wrong passcode) so callers cannot tell them apart.
var ErrInvalidPasscodeLogin = errors.New("invalid credentials")

// RegisterDriver creates an active user with the driver role. The passcode is
// bcrypt-hashed like a password. The password column is NOT NULL, so the driver
// gets a random password nobody knows; the passcode is their only login.
func (s *userService) RegisterDriver(ctx context.Context, req *models.DriverRegisterRequest) (*models.User, error) {
	email := strings.ToLower(strings.TrimSpace(req.Email))
	username := strings.TrimSpace(req.Username)

	exists, err := s.userRepo.ExistsByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, errors.New(i18n.T(ctx, "email_already_exists"))
	}
	exists, err = s.userRepo.ExistsByUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, errors.New(i18n.T(ctx, "username_already_exists"))
	}

	var driverRole models.Role
	if err := s.db.WithContext(ctx).Where("code = ? AND is_active = ?", "driver", true).First(&driverRole).Error; err != nil {
		return nil, fmt.Errorf("driver role is not configured: %w", err)
	}

	passcodeHash, err := utils.HashPassword(req.Passcode)
	if err != nil {
		return nil, err
	}
	passwordHash, err := utils.HashPassword(utils.GenerateRandomPassword(32))
	if err != nil {
		return nil, err
	}

	if s.licenseService != nil && s.licenseService.IsValid() && s.licenseRepo != nil {
		activeCount, countErr := s.licenseRepo.CountActiveUsers(ctx)
		if countErr == nil && activeCount >= int64(s.licenseService.GetMaxUsers()) {
			return nil, fmt.Errorf("%w: %d active users, license limit is %d", ErrUserLimitReached, activeCount, s.licenseService.GetMaxUsers())
		}
	}

	user := &models.User{
		Email:    email,
		Username: username,
		Password: passwordHash,
		Passcode: passcodeHash,
		IsActive: true,
	}
	if err := s.userRepo.Create(ctx, user); err != nil {
		if strings.Contains(err.Error(), "duplicate key") || strings.Contains(err.Error(), "23505") {
			if strings.Contains(err.Error(), "idx_users_email") {
				return nil, errors.New(i18n.T(ctx, "email_already_exists"))
			}
			if strings.Contains(err.Error(), "idx_users_username") {
				return nil, errors.New(i18n.T(ctx, "username_already_exists"))
			}
		}
		return nil, err
	}
	if err := s.userRepo.AssignRoles(ctx, user.ID, []uuid.UUID{driverRole.ID}); err != nil {
		return nil, err
	}
	return user, nil
}

// LoginWithPasscode verifies the passcode of the driver Registry resolved for a
// unit and returns a session-backed token.
func (s *userService) LoginWithPasscode(ctx context.Context, driverID uuid.UUID, passcode string) (*models.PasscodeLoginResponse, error) {
	user, err := s.userRepo.FindByIDWithRelations(ctx, driverID)
	if err != nil || user == nil {
		return nil, ErrInvalidPasscodeLogin
	}
	isDriver := false
	for _, r := range user.Roles {
		if r.Code == "driver" {
			isDriver = true
			break
		}
	}
	if !user.IsActive || !isDriver || user.Passcode == "" || !utils.CheckPassword(passcode, user.Passcode) {
		return nil, ErrInvalidPasscodeLogin
	}

	role := "driver"
	sessionID, err := s.sessionStore.SetUserSessionMultiDevices(ctx, user.ID.String(), map[string]interface{}{
		"user_id": user.ID,
		"email":   user.Email,
		"role":    role,
	}, s.jwtManager.GetTokenExpiration())
	if err != nil {
		return nil, err
	}
	tokenPair, err := s.jwtManager.GenerateTokenPair(user.ID, user.Email, role, sessionID)
	if err != nil {
		return nil, err
	}
	go func() { _ = s.userRepo.UpdateLastLogin(context.Background(), user.ID) }()

	return &models.PasscodeLoginResponse{
		Token:        tokenPair.AccessToken,
		UserID:       user.ID.String(),
		UserName:     user.Username,
		UserEmail:    user.Email,
		ProfileImage: user.Avatar,
		Mobile:       user.Phone,
	}, nil
}
