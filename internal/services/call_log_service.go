package services

import (
	"context"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/google/uuid"
)

type CallLogService interface {
	CreateCallLog(ctx context.Context, req *models.CallLogCreateRequest, createdBy uuid.UUID) (*models.CallLogResponse, error)
	GetCallLog(ctx context.Context, id uuid.UUID) (*models.CallLogResponse, error)
	UpdateCallLog(ctx context.Context, id uuid.UUID, req *models.CallLogUpdateRequest) (*models.CallLogResponse, error)
	DeleteCallLog(ctx context.Context, id uuid.UUID) error
	ListCallLogs(ctx context.Context, filter *models.CallLogFilter) ([]models.CallLogResponse, int64, error)
	GetStats(ctx context.Context) (*models.CallLogStats, error)
	StartCall(ctx context.Context, callUUID string, createdBy uuid.UUID, participants []uuid.UUID) (*models.CallLogResponse, error)
	EndCall(ctx context.Context, callUUID string, endAt *time.Time) (*models.CallLogResponse, error)
	JoinCall(ctx context.Context, callUUID string, userID uuid.UUID) error
}

type callLogService struct {
	repo repository.CallLogRepository
}

func NewCallLogService(repo repository.CallLogRepository) CallLogService {
	return &callLogService{repo: repo}
}

func (s *callLogService) CreateCallLog(ctx context.Context, req *models.CallLogCreateRequest, createdBy uuid.UUID) (*models.CallLogResponse, error) {
	meta := req.Meta
	if meta == "" {
		meta = "{}"
	}
	
	callLog := &models.CallLog{
		CallUuid:     req.CallUuid,
		CreatedBy:    createdBy,
		StartAt:      req.StartAt,
		EndAt:        req.EndAt,
		Status:       req.Status,
		Participants: req.Participants,
		InvitedUsers: req.InvitedUsers,
		RecordingUrl: req.RecordingUrl,
		Meta:         meta,
		CreatedAt:    time.Now(),
	}

	if err := s.repo.Create(ctx, callLog); err != nil {
		return nil, err
	}

	return s.getCallLogResponse(ctx, callLog.ID)
}

func (s *callLogService) GetCallLog(ctx context.Context, id uuid.UUID) (*models.CallLogResponse, error) {
	return s.getCallLogResponse(ctx, id)
}

func (s *callLogService) UpdateCallLog(ctx context.Context, id uuid.UUID, req *models.CallLogUpdateRequest) (*models.CallLogResponse, error) {
	callLog, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if req.StartAt != nil {
		callLog.StartAt = req.StartAt
	}
	if req.EndAt != nil {
		callLog.EndAt = req.EndAt
	}
	if req.Status != "" {
		callLog.Status = req.Status
	}
	if req.Participants != nil {
		callLog.Participants = req.Participants
	}
	if req.JoinedUsers != nil {
		callLog.JoinedUsers = req.JoinedUsers
	}
	if req.InvitedUsers != nil {
		callLog.InvitedUsers = req.InvitedUsers
	}
	if req.RecordingUrl != "" {
		callLog.RecordingUrl = req.RecordingUrl
	}
	if req.Meta != "" {
		if req.Meta == "" {
			callLog.Meta = "{}"
		} else {
			callLog.Meta = req.Meta
		}
	}

	callLog.UpdatedAt = &time.Time{}
	*callLog.UpdatedAt = time.Now()

	if err := s.repo.Update(ctx, callLog); err != nil {
		return nil, err
	}

	return s.getCallLogResponse(ctx, id)
}

func (s *callLogService) DeleteCallLog(ctx context.Context, id uuid.UUID) error {
	return s.repo.Delete(ctx, id)
}

func (s *callLogService) ListCallLogs(ctx context.Context, filter *models.CallLogFilter) ([]models.CallLogResponse, int64, error) {
	// Set defaults
	if filter.Page <= 0 {
		filter.Page = 1
	}
	if filter.Limit <= 0 || filter.Limit > 100 {
		filter.Limit = 10
	}

	callLogs, total, err := s.repo.List(ctx, filter)
	if err != nil {
		return nil, 0, err
	}

	responses := make([]models.CallLogResponse, len(callLogs))
	for i, callLog := range callLogs {
		responses[i] = models.ToCallLogResponse(&callLog, nil) // TODO: Pass user repo for participants
	}

	return responses, total, nil
}

func (s *callLogService) GetStats(ctx context.Context) (*models.CallLogStats, error) {
	return s.repo.GetStats(ctx)
}

func (s *callLogService) StartCall(ctx context.Context, callUUID string, createdBy uuid.UUID, participants []uuid.UUID) (*models.CallLogResponse, error) {
	now := time.Now()
	req := &models.CallLogCreateRequest{
		CallUuid:     callUUID,
		StartAt:      &now,
		Status:       "ongoing",
		Participants: participants,
		InvitedUsers: participants,
	}

	return s.CreateCallLog(ctx, req, createdBy)
}

func (s *callLogService) EndCall(ctx context.Context, callUUID string, endAt *time.Time) (*models.CallLogResponse, error) {
	callLog, err := s.repo.FindByCallUUID(ctx, callUUID)
	if err != nil {
		return nil, err
	}

	if endAt == nil {
		now := time.Now()
		endAt = &now
	}

	updateReq := &models.CallLogUpdateRequest{
		EndAt:  endAt,
		Status: "completed",
	}

	return s.UpdateCallLog(ctx, callLog.ID, updateReq)
}

func (s *callLogService) JoinCall(ctx context.Context, callUUID string, userID uuid.UUID) error {
	callLog, err := s.repo.FindByCallUUID(ctx, callUUID)
	if err != nil {
		return err
	}

	// Add user to joined users if not already present
	found := false
	for _, id := range callLog.JoinedUsers {
		if id == userID {
			found = true
			break
		}
	}
	if !found {
		callLog.JoinedUsers = append(callLog.JoinedUsers, userID)
		callLog.UpdatedAt = &time.Time{}
		*callLog.UpdatedAt = time.Now()
		return s.repo.Update(ctx, callLog)
	}

	return nil
}

func (s *callLogService) getCallLogResponse(ctx context.Context, id uuid.UUID) (*models.CallLogResponse, error) {
	callLog, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}

	response := models.ToCallLogResponse(callLog, nil) // TODO: Pass user repo for participants
	return &response, nil
}
