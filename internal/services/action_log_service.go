package services

import (
	"context"
	"encoding/json"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/google/uuid"
)

type ActionLogService interface {
	LogAction(ctx context.Context, params *LogActionParams) error
	GetActionLog(ctx context.Context, id uuid.UUID) (*models.ActionLogResponse, error)
	ListActionLogs(ctx context.Context, filter *models.ActionLogFilter) ([]models.ActionLogResponse, int64, error)
	GetStats(ctx context.Context) (*models.ActionLogStats, error)
	GetUserActions(ctx context.Context, userID uuid.UUID, page, limit int) ([]models.ActionLogResponse, int64, error)
	CleanupOldLogs(ctx context.Context, retentionDays int) (int64, error)
	GetFilterOptions(ctx context.Context) (*FilterOptions, error)
}

type LogActionParams struct {
	UserID      uuid.UUID
	Action      string
	Module      string
	ResourceID  string
	Description string
	OldValue    interface{}
	NewValue    interface{}
	IPAddress   string
	UserAgent   string
	Status      string
	ErrorMsg    string
	Duration    int64
}

type FilterOptions struct {
	Modules []string `json:"modules"`
	Actions []string `json:"actions"`
}

type actionLogService struct {
	repo repository.ActionLogRepository
}

func NewActionLogService(repo repository.ActionLogRepository) ActionLogService {
	return &actionLogService{repo: repo}
}

func (s *actionLogService) LogAction(ctx context.Context, params *LogActionParams) error {
	var oldValueJSON, newValueJSON string

	if params.OldValue != nil {
		data, err := json.Marshal(params.OldValue)
		if err == nil {
			oldValueJSON = string(data)
		}
	}

	if params.NewValue != nil {
		data, err := json.Marshal(params.NewValue)
		if err == nil {
			newValueJSON = string(data)
		}
	}

	if params.Status == "" {
		params.Status = "success"
	}

	log := &models.ActionLog{
		UserID:      params.UserID,
		Action:      params.Action,
		Module:      params.Module,
		ResourceID:  params.ResourceID,
		Description: params.Description,
		OldValue:    oldValueJSON,
		NewValue:    newValueJSON,
		IPAddress:   params.IPAddress,
		UserAgent:   params.UserAgent,
		Status:      params.Status,
		ErrorMsg:    params.ErrorMsg,
		Duration:    params.Duration,
		CreatedAt:   time.Now(),
	}

	return s.repo.Create(ctx, log)
}

func (s *actionLogService) GetActionLog(ctx context.Context, id uuid.UUID) (*models.ActionLogResponse, error) {
	log, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return models.ToActionLogResponse(log), nil
}

func (s *actionLogService) ListActionLogs(ctx context.Context, filter *models.ActionLogFilter) ([]models.ActionLogResponse, int64, error) {
	// Set defaults
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.Limit < 1 || filter.Limit > 100 {
		filter.Limit = 20
	}

	logs, total, err := s.repo.List(ctx, filter)
	if err != nil {
		return nil, 0, err
	}

	responses := make([]models.ActionLogResponse, len(logs))
	for i, log := range logs {
		responses[i] = *models.ToActionLogResponse(&log)
	}

	return responses, total, nil
}

func (s *actionLogService) GetStats(ctx context.Context) (*models.ActionLogStats, error) {
	return s.repo.GetStats(ctx)
}

func (s *actionLogService) GetUserActions(ctx context.Context, userID uuid.UUID, page, limit int) ([]models.ActionLogResponse, int64, error) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}

	logs, total, err := s.repo.GetUserActions(ctx, userID, page, limit)
	if err != nil {
		return nil, 0, err
	}

	responses := make([]models.ActionLogResponse, len(logs))
	for i, log := range logs {
		responses[i] = *models.ToActionLogResponse(&log)
	}

	return responses, total, nil
}

func (s *actionLogService) CleanupOldLogs(ctx context.Context, retentionDays int) (int64, error) {
	cutoffDate := time.Now().AddDate(0, 0, -retentionDays)
	return s.repo.DeleteOlderThan(ctx, cutoffDate)
}

func (s *actionLogService) GetFilterOptions(ctx context.Context) (*FilterOptions, error) {
	modules, err := s.repo.GetDistinctModules(ctx)
	if err != nil {
		return nil, err
	}

	actions, err := s.repo.GetDistinctActions(ctx)
	if err != nil {
		return nil, err
	}

	return &FilterOptions{
		Modules: modules,
		Actions: actions,
	}, nil
}
