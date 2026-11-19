package services

import (
	"context"
	"errors"

	"github.com/automax/backend/internal/database"
	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/pkg/utils"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	// ErrInvalidPasscodeLogin is returned for every passcode-login failure so callers
	// cannot tell an unknown user from a wrong passcode or an inactive account.
	ErrInvalidPasscodeLogin = errors.New("invalid credentials")
	ErrPassCodeNotFound     = errors.New("passcode not found")
	ErrPassCodeUserNotFound = errors.New("user not found")
)

// PassCodeService manages user_pass_code rows and passcode login.
type PassCodeService struct {
	db           *gorm.DB
	userRepo     repository.UserRepository
	jwtManager   *utils.JWTManager
	sessionStore *database.SessionStore
}

func NewPassCodeService(db *gorm.DB, userRepo repository.UserRepository, jwtManager *utils.JWTManager, sessionStore *database.SessionStore) *PassCodeService {
	return &PassCodeService{db: db, userRepo: userRepo, jwtManager: jwtManager, sessionStore: sessionStore}
}

// Set creates the user's passcode, or replaces it when one already exists.
func (s *PassCodeService) Set(ctx context.Context, userID uuid.UUID, passcode string) error {
	if _, err := s.userRepo.FindByID(ctx, userID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrPassCodeUserNotFound
		}
		return err
	}
	hash, err := utils.HashPassword(passcode)
	if err != nil {
		return err
	}
	row := &models.UserPassCode{ID: uuid.New(), UserID: userID, PassCodeHash: hash}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "user_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"pass_code_hash", "updated_at"}),
	}).Create(row).Error
}

// Remove deletes the user's passcode.
func (s *PassCodeService) Remove(ctx context.Context, userID uuid.UUID) error {
	res := s.db.WithContext(ctx).Where("user_id = ?", userID).Delete(&models.UserPassCode{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrPassCodeNotFound
	}
	return nil
}

// Login verifies the user's passcode and returns a session-backed token.
func (s *PassCodeService) Login(ctx context.Context, userID uuid.UUID, passcode string) (*models.PasscodeLoginResponse, error) {
	var row models.UserPassCode
	if err := s.db.WithContext(ctx).Where("user_id = ?", userID).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInvalidPasscodeLogin
		}
		return nil, err
	}
	if !utils.CheckPassword(passcode, row.PassCodeHash) {
		return nil, ErrInvalidPasscodeLogin
	}
	user, err := s.userRepo.FindByIDWithRelations(ctx, userID)
	if err != nil || user == nil || !user.IsActive {
		return nil, ErrInvalidPasscodeLogin
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
