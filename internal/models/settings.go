package models

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// GetSMSMaxLength reads SMS_MAX_LENGTH from the environment.
// Returns an error if the variable is missing or invalid — no hardcoded fallback.
func GetSMSMaxLength() (int, error) {
	v := os.Getenv("SMS_MAX_LENGTH")
	if v == "" {
		return 0, errors.New("SMS_MAX_LENGTH is not set in environment")
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, errors.New("SMS_MAX_LENGTH must be a positive integer")
	}
	return n, nil
}

// Settings represents application-wide configuration and branding
type Settings struct {
	ID uuid.UUID `gorm:"type:uuid;primary_key" json:"id"`

	// Application Branding
	AppName        string `gorm:"size:100;default:'Automax'" json:"app_name"`
	AppTagline     string `gorm:"size:200" json:"app_tagline"`
	AppDescription string `gorm:"size:500" json:"app_description"`

	// Logo Configuration
	LogoURL      string `gorm:"size:500" json:"logo_url"`       // Main logo URL
	LogoSmallURL string `gorm:"size:500" json:"logo_small_url"` // Small/compact logo
	FaviconURL   string `gorm:"size:500" json:"favicon_url"`    // Favicon URL
	LogoAltText  string `gorm:"size:100" json:"logo_alt_text"`  // Alt text for accessibility

	// Theme Colors
	PrimaryColor   string `gorm:"size:50;default:'#2563eb'" json:"primary_color"`   // Blue-600
	SecondaryColor string `gorm:"size:50;default:'#7c3aed'" json:"secondary_color"` // Violet-600
	AccentColor    string `gorm:"size:50;default:'#059669'" json:"accent_color"`    // Emerald-600

	// Footer Information
	CopyrightText string `gorm:"size:200" json:"copyright_text"`
	ContactEmail  string `gorm:"size:100" json:"contact_email"`
	ContactPhone  string `gorm:"size:50" json:"contact_phone"`

	// Feature Descriptions (for auth/landing pages)
	Feature1Title       string `gorm:"size:100" json:"feature1_title"`
	Feature1Description string `gorm:"size:200" json:"feature1_description"`
	Feature2Title       string `gorm:"size:100" json:"feature2_title"`
	Feature2Description string `gorm:"size:200" json:"feature2_description"`
	Feature3Title       string `gorm:"size:100" json:"feature3_title"`
	Feature3Description string `gorm:"size:200" json:"feature3_description"`

	// System Configuration
	DateFormat         string `gorm:"size:50;default:'YYYY-MM-DD'" json:"date_format"`
	TimeFormat         string `gorm:"size:50;default:'HH:mm:ss'" json:"time_format"`
	DefaultLanguage    string `gorm:"size:10;default:'en'" json:"default_language"`
	SupportedLanguages string `gorm:"size:500;default:'[\"en\",\"ar\"]'" json:"supported_languages"`

	// Feature Toggles
	ShowEvaluateButton bool `gorm:"default:false" json:"show_evaluate_button"` // Manual feedback trigger button on complaint/query detail pages

	// Auth Settings
	TotpEnabled        bool `gorm:"column:totp_enabled;default:false" json:"-"`         // Whether TOTP/OTP-based 2FA login is enforced; exposed nested as auth_setting.totp_enabled
	ShowForgotPassword bool `gorm:"column:show_forgot_password;default:true" json:"-"` // Whether to show the forgot-password link on login; exposed nested as auth_setting.show_forgot_password

	// Field Settings (per-entity, per-field configuration such as attachment limits),
	// stored as a JSON string and exposed nested as field_settings.*
	FieldSettings string `gorm:"column:field_settings;type:text;default:'{\"incident\":{\"attachment\":[]}}'" json:"-"`

	// Timestamps
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`
}

// BeforeCreate sets UUID if not provided
func (s *Settings) BeforeCreate(tx *gorm.DB) error {
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	return nil
}

// AuthSetting represents authentication-related feature toggles, nested under
// the "auth_setting" key in settings update/response payloads.
type AuthSetting struct {
	TotpEnabled        bool `json:"totp_enabled"`
	ShowForgotPassword bool `json:"show_forgot_password"`
}

// AuthSettingUpdateRequest represents a partial update to auth settings.
// Pointer fields distinguish omitted vs explicit false, so updating one
// toggle (e.g. totp_enabled) doesn't reset the other.
type AuthSettingUpdateRequest struct {
	TotpEnabled        *bool `json:"totp_enabled"`
	ShowForgotPassword *bool `json:"show_forgot_password"`
}

// AttachmentFieldSetting configures attachment limits for one user type on an
// entity (e.g. incident attachments allowed for citizens vs employees).
type AttachmentFieldSetting struct {
	UserType     string `json:"user_type" validate:"required,oneof=citizen emp"`
	AllowedCount int    `json:"allowed_count" validate:"required,min=1"`
	MaxSize      int    `json:"max_size" validate:"required,min=1"` // in MB
}

// IncidentFieldSettings holds field-level settings for the incident entity.
type IncidentFieldSettings struct {
	Attachment []AttachmentFieldSetting `json:"attachment" validate:"omitempty,dive"`
}

// FieldSettings represents per-entity, per-field configuration, nested under
// the "field_settings" key in settings update/response payloads.
type FieldSettings struct {
	Incident IncidentFieldSettings `json:"incident"`
}

// SettingsUpdateRequest represents the request to update settings
type SettingsUpdateRequest struct {
	// Application Branding
	AppName        *string `json:"app_name" validate:"omitempty,min=1,max=100"`
	AppTagline     *string `json:"app_tagline" validate:"omitempty,max=200"`
	AppDescription *string `json:"app_description" validate:"omitempty,max=500"`

	// Logo Configuration
	LogoURL      *string `json:"logo_url" validate:"omitempty,url,max=500"`
	LogoSmallURL *string `json:"logo_small_url" validate:"omitempty,url,max=500"`
	FaviconURL   *string `json:"favicon_url" validate:"omitempty,url,max=500"`
	LogoAltText  *string `json:"logo_alt_text" validate:"omitempty,max=100"`

	// Theme Colors
	PrimaryColor   *string `json:"primary_color" validate:"omitempty,hexcolor"`
	SecondaryColor *string `json:"secondary_color" validate:"omitempty,hexcolor"`
	AccentColor    *string `json:"accent_color" validate:"omitempty,hexcolor"`

	// Footer Information
	CopyrightText *string `json:"copyright_text" validate:"omitempty,max=200"`
	ContactEmail  *string `json:"contact_email" validate:"omitempty,email,max=100"`
	ContactPhone  *string `json:"contact_phone" validate:"omitempty,max=50"`

	// Feature Descriptions
	Feature1Title       *string `json:"feature1_title" validate:"omitempty,max=100"`
	Feature1Description *string `json:"feature1_description" validate:"omitempty,max=200"`
	Feature2Title       *string `json:"feature2_title" validate:"omitempty,max=100"`
	Feature2Description *string `json:"feature2_description" validate:"omitempty,max=200"`
	Feature3Title       *string `json:"feature3_title" validate:"omitempty,max=100"`
	Feature3Description *string `json:"feature3_description" validate:"omitempty,max=200"`

	// System Configuration
	DateFormat         *string `json:"date_format" validate:"omitempty,max=50"`
	TimeFormat         *string `json:"time_format" validate:"omitempty,max=50"`
	DefaultLanguage    *string `json:"default_language" validate:"omitempty,min=2,max=10"`
	SupportedLanguages *string `json:"supported_languages" validate:"omitempty,max=500"`

	// Feature Toggles
	ShowEvaluateButton *bool `json:"show_evaluate_button"`

	// Auth Settings
	AuthSetting *AuthSettingUpdateRequest `json:"auth_setting"`

	// Field Settings
	FieldSettings *FieldSettings `json:"field_settings" validate:"omitempty"`
}

// SettingsResponse represents the public settings response
type SettingsResponse struct {
	ID string `json:"id"`

	// Application Branding
	AppName        string `json:"app_name"`
	AppTagline     string `json:"app_tagline"`
	AppDescription string `json:"app_description"`

	// Logo Configuration
	LogoURL      string `json:"logo_url"`
	LogoSmallURL string `json:"logo_small_url"`
	FaviconURL   string `json:"favicon_url"`
	LogoAltText  string `json:"logo_alt_text"`

	// Theme Colors
	PrimaryColor   string `json:"primary_color"`
	SecondaryColor string `json:"secondary_color"`
	AccentColor    string `json:"accent_color"`

	// Footer Information
	CopyrightText string `json:"copyright_text"`
	ContactEmail  string `json:"contact_email"`
	ContactPhone  string `json:"contact_phone"`

	// Feature Descriptions
	Feature1Title       string `json:"feature1_title"`
	Feature1Description string `json:"feature1_description"`
	Feature2Title       string `json:"feature2_title"`
	Feature2Description string `json:"feature2_description"`
	Feature3Title       string `json:"feature3_title"`
	Feature3Description string `json:"feature3_description"`

	// System Configuration
	DateFormat         string `json:"date_format"`
	TimeFormat         string `json:"time_format"`
	DefaultLanguage    string `json:"default_language"`
	SupportedLanguages string `json:"supported_languages"`

	// Feature Toggles
	ShowEvaluateButton bool `json:"show_evaluate_button"`

	// Auth Settings
	AuthSetting AuthSetting `json:"auth_setting"`

	// Field Settings
	FieldSettings FieldSettings `json:"field_settings"`

	// SMS Configuration (env-driven, not stored in DB)
	SmsMaxLength int `json:"sms_max_length"`

	// Timestamps
	UpdatedAt string `json:"updated_at"`
}

// ToSettingsResponse converts Settings model to response DTO
func (s *Settings) ToSettingsResponse() *SettingsResponse {
	var fieldSettings FieldSettings
	if s.FieldSettings != "" {
		if err := json.Unmarshal([]byte(s.FieldSettings), &fieldSettings); err != nil {
			log.Printf("[Settings] ERROR: failed to unmarshal field_settings for settings id %s: %v | raw: %s", s.ID, err, s.FieldSettings)
		}
	}

	return &SettingsResponse{
		ID:                  s.ID.String(),
		AppName:             s.AppName,
		AppTagline:          s.AppTagline,
		AppDescription:      s.AppDescription,
		LogoURL:             s.LogoURL,
		LogoSmallURL:        s.LogoSmallURL,
		FaviconURL:          s.FaviconURL,
		LogoAltText:         s.LogoAltText,
		PrimaryColor:        s.PrimaryColor,
		SecondaryColor:      s.SecondaryColor,
		AccentColor:         s.AccentColor,
		CopyrightText:       s.CopyrightText,
		ContactEmail:        s.ContactEmail,
		ContactPhone:        s.ContactPhone,
		Feature1Title:       s.Feature1Title,
		Feature1Description: s.Feature1Description,
		Feature2Title:       s.Feature2Title,
		Feature2Description: s.Feature2Description,
		Feature3Title:       s.Feature3Title,
		Feature3Description: s.Feature3Description,
		DateFormat:          s.DateFormat,
		TimeFormat:          s.TimeFormat,
		DefaultLanguage:     s.DefaultLanguage,
		SupportedLanguages:  s.SupportedLanguages,
		ShowEvaluateButton:  s.ShowEvaluateButton,
		AuthSetting:         AuthSetting{TotpEnabled: s.TotpEnabled, ShowForgotPassword: s.ShowForgotPassword},
		FieldSettings:       fieldSettings,
		SmsMaxLength:        func() int { n, _ := GetSMSMaxLength(); return n }(),
		UpdatedAt:           s.UpdatedAt.Format(time.RFC3339),
	}
}
