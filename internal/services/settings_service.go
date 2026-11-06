package services

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
)

type SettingsService interface {
	GetSettings(ctx context.Context) (*models.SettingsResponse, error)
	UpdateSettings(ctx context.Context, req *models.SettingsUpdateRequest) (*models.SettingsResponse, error)
}

type settingsService struct {
	settingsRepo repository.SettingsRepository
}

func NewSettingsService(settingsRepo repository.SettingsRepository) SettingsService {
	return &settingsService{
		settingsRepo: settingsRepo,
	}
}

// GetSettings retrieves the application settings
func (s *settingsService) GetSettings(ctx context.Context) (*models.SettingsResponse, error) {
	settings, err := s.settingsRepo.GetOrCreate(ctx)
	if err != nil {
		return nil, err
	}

	return settings.ToSettingsResponse(), nil
}

// UpdateSettings updates the application settings
func (s *settingsService) UpdateSettings(ctx context.Context, req *models.SettingsUpdateRequest) (*models.SettingsResponse, error) {
	// Get existing settings
	settings, err := s.settingsRepo.GetOrCreate(ctx)
	if err != nil {
		return nil, err
	}

	// Update only provided fields
	if req.AppName != nil {
		settings.AppName = *req.AppName
	}
	if req.AppTagline != nil {
		settings.AppTagline = *req.AppTagline
	}
	if req.AppDescription != nil {
		settings.AppDescription = *req.AppDescription
	}
	if req.LogoURL != nil {
		settings.LogoURL = *req.LogoURL
	}
	if req.LogoSmallURL != nil {
		settings.LogoSmallURL = *req.LogoSmallURL
	}
	if req.FaviconURL != nil {
		settings.FaviconURL = *req.FaviconURL
	}
	if req.LogoAltText != nil {
		settings.LogoAltText = *req.LogoAltText
	}
	if req.PrimaryColor != nil {
		settings.PrimaryColor = *req.PrimaryColor
	}
	if req.SecondaryColor != nil {
		settings.SecondaryColor = *req.SecondaryColor
	}
	if req.AccentColor != nil {
		settings.AccentColor = *req.AccentColor
	}
	if req.CopyrightText != nil {
		settings.CopyrightText = *req.CopyrightText
	}
	if req.ContactEmail != nil {
		settings.ContactEmail = *req.ContactEmail
	}
	if req.ContactPhone != nil {
		settings.ContactPhone = *req.ContactPhone
	}
	if req.Feature1Title != nil {
		settings.Feature1Title = *req.Feature1Title
	}
	if req.Feature1Description != nil {
		settings.Feature1Description = *req.Feature1Description
	}
	if req.Feature2Title != nil {
		settings.Feature2Title = *req.Feature2Title
	}
	if req.Feature2Description != nil {
		settings.Feature2Description = *req.Feature2Description
	}
	if req.Feature3Title != nil {
		settings.Feature3Title = *req.Feature3Title
	}
	if req.Feature3Description != nil {
		settings.Feature3Description = *req.Feature3Description
	}
	if req.DateFormat != nil {
		settings.DateFormat = *req.DateFormat
	}
	if req.TimeFormat != nil {
		settings.TimeFormat = *req.TimeFormat
	}
	if req.DefaultLanguage != nil {
		settings.DefaultLanguage = *req.DefaultLanguage
	}
	if req.ShowEvaluateButton != nil {
		settings.ShowEvaluateButton = *req.ShowEvaluateButton
	}
	if req.AuthSetting != nil {
		if req.AuthSetting.TotpEnabled != nil {
			settings.TotpEnabled = *req.AuthSetting.TotpEnabled
		}
		if req.AuthSetting.ShowForgotPassword != nil {
			settings.ShowForgotPassword = *req.AuthSetting.ShowForgotPassword
		}
	}
	if req.FieldSettings != nil {
		fieldSettingsJSON, err := json.Marshal(req.FieldSettings)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal field settings: %w", err)
		}
		settings.FieldSettings = string(fieldSettingsJSON)
	}

	// Save updated settings
	if err := s.settingsRepo.Update(ctx, settings); err != nil {
		return nil, err
	}

	return settings.ToSettingsResponse(), nil
}
