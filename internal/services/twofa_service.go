package services

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"net/smtp"
	"strings"
	"time"

	"github.com/automax/backend/internal/config"
	"github.com/automax/backend/internal/database"
	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/pkg/utils"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// TwoFAService handles two-factor authentication operations
type TwoFAService struct {
	redisClient  *redis.Client
	userRepo     repository.UserRepository
	jwtManager   *utils.JWTManager
	sessionStore *database.SessionStore
	config       *config.Config
}

// NewTwoFAService creates a new 2FA service instance
func NewTwoFAService(
	redisClient *redis.Client,
	userRepo repository.UserRepository,
	jwtManager *utils.JWTManager,
	sessionStore *database.SessionStore,
	cfg *config.Config,
) *TwoFAService {
	return &TwoFAService{
		redisClient:  redisClient,
		userRepo:     userRepo,
		jwtManager:   jwtManager,
		sessionStore: sessionStore,
		config:       cfg,
	}
}

// TwoFASessionData stores the temporary 2FA session data in Redis
type TwoFASessionData struct {
	UserID        uuid.UUID `json:"user_id"`
	Email         string    `json:"email"`
	Phone         string    `json:"phone"`
	OTPHash       string    `json:"otp_hash"`
	Attempts      int       `json:"attempts"`
	MaxAttempts   int       `json:"max_attempts"`
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	OTPMethod     string    `json:"otp_method"` // "email" or "sms"
}

// GenerateSecureOTP generates a cryptographically secure 6-digit OTP
func GenerateSecureOTP() (string, error) {
	max := big.NewInt(1000000)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// InitiateTwoFA starts the 2FA process after successful credential validation
// Returns a session ID and sends OTP to the user
func (s *TwoFAService) InitiateTwoFA(ctx context.Context, user *models.User) (*models.TwoFAInitResponse, error) {
	// Check if 2FA is enabled globally
	if !s.config.TwoFA.Enabled {
		return nil, nil // 2FA not enabled, return nil
	}

	// Check if user has 2FA enabled
	if !user.TwoFactorEnabled {
		return nil, nil // User hasn't enabled 2FA
	}

	// Generate session ID and OTP
	sessionID := uuid.New().String()
	otp, err := GenerateSecureOTP()
	if err != nil {
		return nil, fmt.Errorf("failed to generate OTP: %w", err)
	}

	otpHash := HashOTP(otp)
	otpMethod := s.config.TwoFA.Method

	// Create session data
	sessionData := &TwoFASessionData{
		UserID:      user.ID,
		Email:       user.Email,
		Phone:       user.Phone,
		OTPHash:     otpHash,
		Attempts:    0,
		MaxAttempts: s.config.TwoFA.MaxAttempts,
		CreatedAt:   time.Now(),
		ExpiresAt:   time.Now().Add(time.Duration(s.config.TwoFA.OTPExpiryMinutes) * time.Minute),
		OTPMethod:   otpMethod,
	}

	// Store session in Redis with expiration
	expiration := time.Duration(s.config.TwoFA.OTPExpiryMinutes) * time.Minute
	redisKey := fmt.Sprintf("2fa_session:%s", sessionID)
	data, err := json.Marshal(sessionData)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal session data: %w", err)
	}

	if err := s.redisClient.Set(ctx, redisKey, data, expiration).Err(); err != nil {
		return nil, fmt.Errorf("failed to store 2FA session: %w", err)
	}

	// Send OTP via configured method
	if err := s.sendOTPToUser(ctx, user, otp, otpMethod); err != nil {
		// Clean up the session if sending fails
		s.redisClient.Del(ctx, redisKey)
		return nil, fmt.Errorf("failed to send OTP: %w", err)
	}

	// Return response (without the actual OTP)
	return &models.TwoFAInitResponse{
		TwoFARequired: true,
		SessionID:     sessionID,
		Message:       fmt.Sprintf("OTP sent to your %s", otpMethod),
		OTPMethod:     otpMethod,
	}, nil
}

// sendOTPToUser sends the OTP to the user via email or SMS
func (s *TwoFAService) sendOTPToUser(ctx context.Context, user *models.User, otp string, method string) error {
	switch method {
	case "email":
		return s.sendOTPViaEmail(ctx, user, otp)
	case "sms":
		return s.sendOTPViaSMS(ctx, user, otp)
	default:
		return fmt.Errorf("unsupported 2FA method: %s", method)
	}
}

// sendOTPViaEmail sends OTP via email using SMTP
func (s *TwoFAService) sendOTPViaEmail(ctx context.Context, user *models.User, otp string) error {
	// Check if SMTP is configured
	if s.config.SMTP.Host == "" || s.config.SMTP.User == "" || s.config.SMTP.Password == "" {
		return fmt.Errorf("SMTP not configured. Please configure SMTP settings to enable email OTP")
	}

	subject := "Your Two-Factor Authentication Code"
	
	// Create HTML email body
	body := fmt.Sprintf(`<!DOCTYPE html>
<html>
<head>
	<meta charset="UTF-8">
	<meta name="viewport" content="width=device-width, initial-scale=1.0">
	<style>
		body {
			font-family: Arial, sans-serif;
			line-height: 1.6;
			color: #333;
			max-width: 600px;
			margin: 0 auto;
			padding: 20px;
		}
		.container {
			background-color: #f9f9f9;
			border-radius: 10px;
			padding: 30px;
			margin: 20px 0;
		}
		.header {
			background-color: #007bff;
			color: white;
			padding: 20px;
			border-radius: 10px 10px 0 0;
			margin: -30px -30px 30px -30px;
		}
		.otp-code {
			font-size: 48px;
			font-weight: bold;
			color: #007bff;
			text-align: center;
			letter-spacing: 10px;
			padding: 20px;
			background-color: #ffffff;
			border: 2px dashed #007bff;
			border-radius: 5px;
			margin: 20px 0;
		}
		.footer {
			font-size: 12px;
			color: #666;
			margin-top: 30px;
			padding-top: 20px;
			border-top: 1px solid #ddd;
		}
		.warning {
			background-color: #fff3cd;
			border-left: 4px solid #ffc107;
			padding: 10px 15px;
			margin: 20px 0;
		}
	</style>
</head>
<body>
	<div class="container">
		<div class="header">
			<h1>🔐 Two-Factor Authentication</h1>
		</div>
		
		<p>Hello %s,</p>
		
		<p>Your verification code is:</p>
		
		<div class="otp-code">%s</div>
		
		<p>This code will expire in <strong>%d minutes</strong>.</p>
		
		<div class="warning">
			<strong>⚠️ Security Notice:</strong> If you didn't request this code, please ignore this email and ensure your account is secure.
		</div>
		
		<div class="footer">
			<p>This is an automated message from Automax. Please do not reply to this email.</p>
			<p>&copy; %d Automax. All rights reserved.</p>
		</div>
	</div>
</body>
</html>`, user.FirstName, otp, s.config.TwoFA.OTPExpiryMinutes, time.Now().Year())

	// Send email via SMTP
	return s.sendHTMLEmail(user.Email, subject, body)
}

// sendHTMLEmail sends an HTML email via SMTP
func (s *TwoFAService) sendHTMLEmail(to string, subject string, htmlBody string) error {
	smtpConfig := s.config.SMTP

	// SMTP server address
	addr := fmt.Sprintf("%s:%d", smtpConfig.Host, smtpConfig.Port)

	// Authentication
	auth := smtp.PlainAuth("", smtpConfig.User, smtpConfig.Password, smtpConfig.Host)

	// Create MIME message with HTML content
	var msg strings.Builder
	msg.WriteString(fmt.Sprintf("From: %s\r\n", smtpConfig.From))
	msg.WriteString(fmt.Sprintf("To: %s\r\n", to))
	msg.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n")
	msg.WriteString("Content-Transfer-Encoding: quoted-printable\r\n")
	msg.WriteString("\r\n")
	msg.WriteString(htmlBody)

	// Send email
	err := smtp.SendMail(addr, auth, smtpConfig.From, []string{to}, []byte(msg.String()))
	if err != nil {
		return fmt.Errorf("failed to send email: %w", err)
	}

	return nil
}

// sendOTPViaSMS sends OTP via SMS
// Note: SMS requires notification service setup. For now, email is the primary method.
func (s *TwoFAService) sendOTPViaSMS(ctx context.Context, user *models.User, otp string) error {
	return fmt.Errorf("SMS OTP not yet implemented. Please use email method or contact support")
}

// VerifyTwoFAOTP verifies the OTP provided by the user
// Returns JWT tokens if verification is successful
func (s *TwoFAService) VerifyTwoFAOTP(ctx context.Context, sessionID string, otp string) (*models.TwoFAVerifyResponse, error) {
	// Get session from Redis
	redisKey := fmt.Sprintf("2fa_session:%s", sessionID)
	data, err := s.redisClient.Get(ctx, redisKey).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, fmt.Errorf("invalid or expired 2FA session")
		}
		return nil, fmt.Errorf("failed to retrieve 2FA session: %w", err)
	}

	var sessionData TwoFASessionData
	if err := json.Unmarshal(data, &sessionData); err != nil {
		return nil, fmt.Errorf("failed to parse 2FA session: %w", err)
	}

	// Check if session has expired
	if time.Now().After(sessionData.ExpiresAt) {
		s.redisClient.Del(ctx, redisKey)
		return nil, fmt.Errorf("2FA session has expired")
	}

	// Check attempt limit
	if sessionData.Attempts >= sessionData.MaxAttempts {
		s.redisClient.Del(ctx, redisKey)
		return nil, fmt.Errorf("maximum OTP attempts exceeded. Please request a new code")
	}

	// Verify OTP
	otpHash := HashOTP(otp)
	if otpHash != sessionData.OTPHash {
		// Increment attempts
		sessionData.Attempts++
		data, _ := json.Marshal(sessionData)
		s.redisClient.Set(ctx, redisKey, data, time.Duration(s.config.TwoFA.OTPExpiryMinutes)*time.Minute)

		remaining := sessionData.MaxAttempts - sessionData.Attempts
		return nil, fmt.Errorf("invalid OTP. %d attempts remaining", remaining)
	}

	// OTP is valid - fetch user and generate tokens
	user, err := s.userRepo.FindByIDWithRelations(ctx, sessionData.UserID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch user: %w", err)
	}

	// Determine role
	role := "user"
	if user.IsSuperAdmin {
		role = "admin"
	} else if len(user.Roles) > 0 {
		role = user.Roles[0].Code
	}

	// Generate JWT tokens
	tokenPair, err := s.jwtManager.GenerateTokenPair(user.ID, user.Email, role)
	if err != nil {
		return nil, fmt.Errorf("failed to generate tokens: %w", err)
	}

	// Store session in Redis
	if err := s.sessionStore.SetUserSession(ctx, user.ID.String(), map[string]interface{}{
		"user_id": user.ID,
		"email":   user.Email,
		"role":    role,
		"auth_source": "2fa",
	}, s.jwtManager.GetTokenExpiration()); err != nil {
		return nil, fmt.Errorf("failed to store session: %w", err)
	}

	// Delete the 2FA session
	s.redisClient.Del(ctx, redisKey)

	// Update last login timestamp
	go func() {
		_ = s.userRepo.UpdateLastLogin(context.Background(), user.ID)
	}()

	// Convert to response
	userResponse := models.ToUserResponse(user)

	return &models.TwoFAVerifyResponse{
		User:         userResponse,
		Token:        tokenPair.AccessToken,
		RefreshToken: tokenPair.RefreshToken,
		ExpiresIn:    tokenPair.ExpiresIn,
	}, nil
}

// ResendTwoFAOTP generates and sends a new OTP for an existing session
func (s *TwoFAService) ResendTwoFAOTP(ctx context.Context, sessionID string) error {
	// Get session from Redis
	redisKey := fmt.Sprintf("2fa_session:%s", sessionID)
	data, err := s.redisClient.Get(ctx, redisKey).Bytes()
	if err != nil {
		if err == redis.Nil {
			return fmt.Errorf("invalid or expired 2FA session")
		}
		return fmt.Errorf("failed to retrieve 2FA session: %w", err)
	}

	var sessionData TwoFASessionData
	if err := json.Unmarshal(data, &sessionData); err != nil {
		return fmt.Errorf("failed to parse 2FA session: %w", err)
	}

	// Check if session has expired
	if time.Now().After(sessionData.ExpiresAt) {
		s.redisClient.Del(ctx, redisKey)
		return fmt.Errorf("2FA session has expired")
	}

	// Generate new OTP
	otp, err := GenerateSecureOTP()
	if err != nil {
		return fmt.Errorf("failed to generate OTP: %w", err)
	}

	// Update session with new OTP hash
	sessionData.OTPHash = HashOTP(otp)
	sessionData.Attempts = 0 // Reset attempts on resend
	data, _ = json.Marshal(sessionData)
	if err := s.redisClient.Set(ctx, redisKey, data, time.Duration(s.config.TwoFA.OTPExpiryMinutes)*time.Minute).Err(); err != nil {
		return fmt.Errorf("failed to update 2FA session: %w", err)
	}

	// Fetch user to send OTP
	user, err := s.userRepo.FindByIDWithRelations(ctx, sessionData.UserID)
	if err != nil {
		return fmt.Errorf("failed to fetch user: %w", err)
	}

	// Send new OTP
	return s.sendOTPToUser(ctx, user, otp, sessionData.OTPMethod)
}
