package services

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/google/uuid"
)

type ReportService interface {
	// Report CRUD
	CreateReport(ctx context.Context, req *models.ReportCreateRequest, userID uuid.UUID) (*models.ReportResponse, error)
	GetReport(ctx context.Context, id uuid.UUID) (*models.ReportResponse, error)
	ListReports(ctx context.Context, filter *models.ReportFilter) ([]models.ReportResponse, int64, error)
	UpdateReport(ctx context.Context, id uuid.UUID, req *models.ReportUpdateRequest, userID uuid.UUID) (*models.ReportResponse, error)
	DeleteReport(ctx context.Context, id uuid.UUID, userID uuid.UUID) error
	DuplicateReport(ctx context.Context, id uuid.UUID, userID uuid.UUID) (*models.ReportResponse, error)

	// Report Execution
	ExecuteReport(ctx context.Context, id uuid.UUID, req *models.ReportExecuteRequest, userID uuid.UUID) (*models.ReportResultResponse, error)
	PreviewReport(ctx context.Context, req *models.ReportCreateRequest) (*models.ReportResultResponse, error)
	GetExecutionHistory(ctx context.Context, reportID uuid.UUID, page, limit int) ([]models.ReportExecutionResponse, int64, error)

	// Metadata
	GetDataSources(ctx context.Context) []models.DataSourceInfo
}

type reportService struct {
	reportRepo repository.ReportRepository
}

func NewReportService(reportRepo repository.ReportRepository) ReportService {
	return &reportService{
		reportRepo: reportRepo,
	}
}

// Report CRUD

func (s *reportService) CreateReport(ctx context.Context, req *models.ReportCreateRequest, userID uuid.UUID) (*models.ReportResponse, error) {
	columnsJSON, _ := json.Marshal(req.Columns)
	filtersJSON, _ := json.Marshal(req.Filters)
	sortingJSON, _ := json.Marshal(req.Sorting)
	groupingJSON, _ := json.Marshal(req.Grouping)
	scheduleJSON, _ := json.Marshal(req.Schedule)

	isScheduled := req.Schedule != nil && req.Schedule.Enabled

	report := &models.Report{
		Name:         req.Name,
		Description:  req.Description,
		DataSource:   req.DataSource,
		Columns:      string(columnsJSON),
		Filters:      string(filtersJSON),
		Sorting:      string(sortingJSON),
		Grouping:     string(groupingJSON),
		OutputFormat: req.OutputFormat,
		IsPublic:     req.IsPublic,
		IsScheduled:  isScheduled,
		Schedule:     string(scheduleJSON),
		CreatedByID:  userID,
	}

	if report.OutputFormat == "" {
		report.OutputFormat = "table"
	}

	if err := s.reportRepo.Create(ctx, report); err != nil {
		return nil, err
	}

	return s.GetReport(ctx, report.ID)
}

func (s *reportService) GetReport(ctx context.Context, id uuid.UUID) (*models.ReportResponse, error) {
	report, err := s.reportRepo.FindByIDWithRelations(ctx, id)
	if err != nil {
		return nil, err
	}

	return toReportResponse(report), nil
}

func (s *reportService) ListReports(ctx context.Context, filter *models.ReportFilter) ([]models.ReportResponse, int64, error) {
	reports, total, err := s.reportRepo.List(ctx, filter)
	if err != nil {
		return nil, 0, err
	}

	responses := make([]models.ReportResponse, len(reports))
	for i, r := range reports {
		responses[i] = *toReportResponse(&r)
	}

	return responses, total, nil
}

func (s *reportService) UpdateReport(ctx context.Context, id uuid.UUID, req *models.ReportUpdateRequest, userID uuid.UUID) (*models.ReportResponse, error) {
	report, err := s.reportRepo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}

	// Only creator can update (or implement permission check)
	if report.CreatedByID != userID {
		return nil, errors.New("you can only update your own reports")
	}

	if req.Name != "" {
		report.Name = req.Name
	}
	if req.Description != "" {
		report.Description = req.Description
	}
	if req.Columns != nil {
		columnsJSON, _ := json.Marshal(req.Columns)
		report.Columns = string(columnsJSON)
	}
	if req.Filters != nil {
		filtersJSON, _ := json.Marshal(req.Filters)
		report.Filters = string(filtersJSON)
	}
	if req.Sorting != nil {
		sortingJSON, _ := json.Marshal(req.Sorting)
		report.Sorting = string(sortingJSON)
	}
	if req.Grouping != nil {
		groupingJSON, _ := json.Marshal(req.Grouping)
		report.Grouping = string(groupingJSON)
	}
	if req.OutputFormat != "" {
		report.OutputFormat = req.OutputFormat
	}
	if req.IsPublic != nil {
		report.IsPublic = *req.IsPublic
	}
	if req.Schedule != nil {
		scheduleJSON, _ := json.Marshal(req.Schedule)
		report.Schedule = string(scheduleJSON)
		report.IsScheduled = req.Schedule.Enabled
	}

	if err := s.reportRepo.Update(ctx, report); err != nil {
		return nil, err
	}

	return s.GetReport(ctx, id)
}

func (s *reportService) DeleteReport(ctx context.Context, id uuid.UUID, userID uuid.UUID) error {
	report, err := s.reportRepo.FindByID(ctx, id)
	if err != nil {
		return err
	}

	// Only creator can delete
	if report.CreatedByID != userID {
		return errors.New("you can only delete your own reports")
	}

	return s.reportRepo.Delete(ctx, id)
}

func (s *reportService) DuplicateReport(ctx context.Context, id uuid.UUID, userID uuid.UUID) (*models.ReportResponse, error) {
	original, err := s.reportRepo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}

	duplicate := &models.Report{
		Name:         original.Name + " (Copy)",
		Description:  original.Description,
		DataSource:   original.DataSource,
		Columns:      original.Columns,
		Filters:      original.Filters,
		Sorting:      original.Sorting,
		Grouping:     original.Grouping,
		OutputFormat: original.OutputFormat,
		IsPublic:     false, // New report is private by default
		IsScheduled:  false, // Don't copy schedule
		Schedule:     "",
		CreatedByID:  userID,
	}

	if err := s.reportRepo.Create(ctx, duplicate); err != nil {
		return nil, err
	}

	return s.GetReport(ctx, duplicate.ID)
}

// Report Execution

func (s *reportService) ExecuteReport(ctx context.Context, id uuid.UUID, req *models.ReportExecuteRequest, userID uuid.UUID) (*models.ReportResultResponse, error) {
	report, err := s.reportRepo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}

	// Parse stored config
	var columns []models.ReportColumnConfig
	var filters []models.ReportFilterConfig
	var sorting *models.ReportSortConfig

	json.Unmarshal([]byte(report.Columns), &columns)
	json.Unmarshal([]byte(report.Filters), &filters)
	json.Unmarshal([]byte(report.Sorting), &sorting)

	// Override filters if provided
	if len(req.Filters) > 0 {
		filters = req.Filters
	}

	// Create execution record
	now := time.Now()
	execution := &models.ReportExecution{
		ReportID:     id,
		ExecutedByID: userID,
		Status:       "running",
		StartedAt:    &now,
	}
	s.reportRepo.CreateExecution(ctx, execution)

	// Execute query based on data source
	page := req.Page
	if page < 1 {
		page = 1
	}
	limit := req.Limit
	if limit < 1 || limit > 1000 {
		limit = 100
	}

	var data []map[string]interface{}
	var total int64
	var queryErr error

	switch report.DataSource {
	case "incidents":
		data, total, queryErr = s.reportRepo.ExecuteIncidentQuery(ctx, filters, sorting, page, limit)
	case "users":
		data, total, queryErr = s.reportRepo.ExecuteUserQuery(ctx, filters, sorting, page, limit)
	case "workflows":
		data, total, queryErr = s.reportRepo.ExecuteWorkflowQuery(ctx, filters, sorting, page, limit)
	case "departments":
		data, total, queryErr = s.reportRepo.ExecuteDepartmentQuery(ctx, filters, sorting, page, limit)
	case "locations":
		data, total, queryErr = s.reportRepo.ExecuteLocationQuery(ctx, filters, sorting, page, limit)
	case "classifications":
		data, total, queryErr = s.reportRepo.ExecuteClassificationQuery(ctx, filters, sorting, page, limit)
	default:
		queryErr = errors.New("unsupported data source")
	}

	// Update execution record
	completedAt := time.Now()
	execution.CompletedAt = &completedAt
	if queryErr != nil {
		execution.Status = "failed"
		execution.Error = queryErr.Error()
	} else {
		execution.Status = "completed"
		execution.ResultCount = int(total)
	}
	s.reportRepo.UpdateExecution(ctx, execution)

	if queryErr != nil {
		return nil, queryErr
	}

	return &models.ReportResultResponse{
		Columns: columns,
		Data:    data,
		Total:   total,
		Page:    page,
		Limit:   limit,
	}, nil
}

func (s *reportService) PreviewReport(ctx context.Context, req *models.ReportCreateRequest) (*models.ReportResultResponse, error) {
	page := 1
	limit := 50 // Preview limit

	var data []map[string]interface{}
	var total int64
	var err error

	switch req.DataSource {
	case "incidents":
		data, total, err = s.reportRepo.ExecuteIncidentQuery(ctx, req.Filters, req.Sorting, page, limit)
	case "users":
		data, total, err = s.reportRepo.ExecuteUserQuery(ctx, req.Filters, req.Sorting, page, limit)
	case "workflows":
		data, total, err = s.reportRepo.ExecuteWorkflowQuery(ctx, req.Filters, req.Sorting, page, limit)
	case "departments":
		data, total, err = s.reportRepo.ExecuteDepartmentQuery(ctx, req.Filters, req.Sorting, page, limit)
	case "locations":
		data, total, err = s.reportRepo.ExecuteLocationQuery(ctx, req.Filters, req.Sorting, page, limit)
	case "classifications":
		data, total, err = s.reportRepo.ExecuteClassificationQuery(ctx, req.Filters, req.Sorting, page, limit)
	default:
		return nil, errors.New("unsupported data source")
	}

	if err != nil {
		return nil, err
	}

	return &models.ReportResultResponse{
		Columns: req.Columns,
		Data:    data,
		Total:   total,
		Page:    page,
		Limit:   limit,
	}, nil
}

func (s *reportService) GetExecutionHistory(ctx context.Context, reportID uuid.UUID, page, limit int) ([]models.ReportExecutionResponse, int64, error) {
	executions, total, err := s.reportRepo.ListExecutions(ctx, reportID, page, limit)
	if err != nil {
		return nil, 0, err
	}

	responses := make([]models.ReportExecutionResponse, len(executions))
	for i, e := range executions {
		responses[i] = toExecutionResponse(&e)
	}

	return responses, total, nil
}

// Metadata

func (s *reportService) GetDataSources(ctx context.Context) []models.DataSourceInfo {
	return []models.DataSourceInfo{
		{
			Name:  "incidents",
			Label: "Incidents",
			Fields: []models.DataSourceField{
				{Field: "incident_number", Label: "Incident Number", Type: "string", Filterable: true, Sortable: true},
				{Field: "title", Label: "Title", Type: "string", Filterable: true, Sortable: true},
				{Field: "description", Label: "Description", Type: "string", Filterable: true, Sortable: false},
				{Field: "priority", Label: "Priority", Type: "number", Filterable: true, Sortable: true},
				{Field: "severity", Label: "Severity", Type: "number", Filterable: true, Sortable: true},
				{Field: "current_state_name", Label: "Status", Type: "string", Filterable: true, Sortable: true},
				{Field: "classification_name", Label: "Classification", Type: "string", Filterable: true, Sortable: true},
				{Field: "department_name", Label: "Department", Type: "string", Filterable: true, Sortable: true},
				{Field: "location_name", Label: "Location", Type: "string", Filterable: true, Sortable: true},
				{Field: "reporter_email", Label: "Reporter Email", Type: "string", Filterable: true, Sortable: true},
				{Field: "assignee_email", Label: "Assignee Email", Type: "string", Filterable: true, Sortable: true},
				{Field: "sla_breached", Label: "SLA Breached", Type: "boolean", Filterable: true, Sortable: true},
				{Field: "created_at", Label: "Created At", Type: "date", Filterable: true, Sortable: true},
				{Field: "updated_at", Label: "Updated At", Type: "date", Filterable: true, Sortable: true},
				{Field: "resolved_at", Label: "Resolved At", Type: "date", Filterable: true, Sortable: true},
				{Field: "closed_at", Label: "Closed At", Type: "date", Filterable: true, Sortable: true},
			},
		},
		{
			Name:  "users",
			Label: "Users",
			Fields: []models.DataSourceField{
				{Field: "email", Label: "Email", Type: "string", Filterable: true, Sortable: true},
				{Field: "username", Label: "Username", Type: "string", Filterable: true, Sortable: true},
				{Field: "first_name", Label: "First Name", Type: "string", Filterable: true, Sortable: true},
				{Field: "last_name", Label: "Last Name", Type: "string", Filterable: true, Sortable: true},
				{Field: "phone", Label: "Phone", Type: "string", Filterable: true, Sortable: false},
				{Field: "is_active", Label: "Active", Type: "boolean", Filterable: true, Sortable: true},
				{Field: "is_super_admin", Label: "Super Admin", Type: "boolean", Filterable: true, Sortable: true},
				{Field: "department_name", Label: "Department", Type: "string", Filterable: true, Sortable: true},
				{Field: "location_name", Label: "Location", Type: "string", Filterable: true, Sortable: true},
				{Field: "created_at", Label: "Created At", Type: "date", Filterable: true, Sortable: true},
			},
		},
		{
			Name:  "workflows",
			Label: "Workflows",
			Fields: []models.DataSourceField{
				{Field: "name", Label: "Name", Type: "string", Filterable: true, Sortable: true},
				{Field: "code", Label: "Code", Type: "string", Filterable: true, Sortable: true},
				{Field: "description", Label: "Description", Type: "string", Filterable: true, Sortable: false},
				{Field: "is_active", Label: "Active", Type: "boolean", Filterable: true, Sortable: true},
				{Field: "is_default", Label: "Default", Type: "boolean", Filterable: true, Sortable: true},
				{Field: "created_at", Label: "Created At", Type: "date", Filterable: true, Sortable: true},
			},
		},
		{
			Name:  "departments",
			Label: "Departments",
			Fields: []models.DataSourceField{
				{Field: "name", Label: "Name", Type: "string", Filterable: true, Sortable: true},
				{Field: "code", Label: "Code", Type: "string", Filterable: true, Sortable: true},
				{Field: "description", Label: "Description", Type: "string", Filterable: true, Sortable: false},
				{Field: "parent_name", Label: "Parent Department", Type: "string", Filterable: true, Sortable: true},
				{Field: "is_active", Label: "Active", Type: "boolean", Filterable: true, Sortable: true},
				{Field: "created_at", Label: "Created At", Type: "date", Filterable: true, Sortable: true},
			},
		},
		{
			Name:  "locations",
			Label: "Locations",
			Fields: []models.DataSourceField{
				{Field: "name", Label: "Name", Type: "string", Filterable: true, Sortable: true},
				{Field: "code", Label: "Code", Type: "string", Filterable: true, Sortable: true},
				{Field: "location_type", Label: "Type", Type: "string", Filterable: true, Sortable: true},
				{Field: "address", Label: "Address", Type: "string", Filterable: true, Sortable: false},
				{Field: "parent_name", Label: "Parent Location", Type: "string", Filterable: true, Sortable: true},
				{Field: "is_active", Label: "Active", Type: "boolean", Filterable: true, Sortable: true},
				{Field: "created_at", Label: "Created At", Type: "date", Filterable: true, Sortable: true},
			},
		},
		{
			Name:  "classifications",
			Label: "Classifications",
			Fields: []models.DataSourceField{
				{Field: "name", Label: "Name", Type: "string", Filterable: true, Sortable: true},
				{Field: "code", Label: "Code", Type: "string", Filterable: true, Sortable: true},
				{Field: "description", Label: "Description", Type: "string", Filterable: true, Sortable: false},
				{Field: "parent_name", Label: "Parent Classification", Type: "string", Filterable: true, Sortable: true},
				{Field: "is_active", Label: "Active", Type: "boolean", Filterable: true, Sortable: true},
				{Field: "created_at", Label: "Created At", Type: "date", Filterable: true, Sortable: true},
			},
		},
	}
}

// Helper functions

func toReportResponse(r *models.Report) *models.ReportResponse {
	var columns []models.ReportColumnConfig
	var filters []models.ReportFilterConfig
	var sorting *models.ReportSortConfig
	var grouping *models.ReportGroupConfig
	var schedule *models.ReportScheduleConfig

	json.Unmarshal([]byte(r.Columns), &columns)
	json.Unmarshal([]byte(r.Filters), &filters)
	json.Unmarshal([]byte(r.Sorting), &sorting)
	json.Unmarshal([]byte(r.Grouping), &grouping)
	json.Unmarshal([]byte(r.Schedule), &schedule)

	resp := &models.ReportResponse{
		ID:           r.ID.String(),
		Name:         r.Name,
		Description:  r.Description,
		DataSource:   r.DataSource,
		Columns:      columns,
		Filters:      filters,
		Sorting:      sorting,
		Grouping:     grouping,
		OutputFormat: r.OutputFormat,
		IsPublic:     r.IsPublic,
		IsScheduled:  r.IsScheduled,
		Schedule:     schedule,
		CreatedAt:    r.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    r.UpdatedAt.Format(time.RFC3339),
	}

	if r.CreatedBy != nil {
		resp.CreatedBy = &models.UserBasicResponse{
			ID:        r.CreatedBy.ID.String(),
			Email:     r.CreatedBy.Email,
			Username:  r.CreatedBy.Username,
			FirstName: r.CreatedBy.FirstName,
			LastName:  r.CreatedBy.LastName,
			Avatar:    r.CreatedBy.Avatar,
		}
	}

	return resp
}

func toExecutionResponse(e *models.ReportExecution) models.ReportExecutionResponse {
	resp := models.ReportExecutionResponse{
		ID:          e.ID.String(),
		ReportID:    e.ReportID.String(),
		Status:      e.Status,
		ResultCount: e.ResultCount,
		FilePath:    e.FilePath,
		Error:       e.Error,
		CreatedAt:   e.CreatedAt.Format(time.RFC3339),
	}

	if e.StartedAt != nil {
		resp.StartedAt = e.StartedAt.Format(time.RFC3339)
	}
	if e.CompletedAt != nil {
		resp.CompletedAt = e.CompletedAt.Format(time.RFC3339)
	}

	if e.ExecutedBy != nil {
		resp.ExecutedBy = &models.UserBasicResponse{
			ID:        e.ExecutedBy.ID.String(),
			Email:     e.ExecutedBy.Email,
			Username:  e.ExecutedBy.Username,
			FirstName: e.ExecutedBy.FirstName,
			LastName:  e.ExecutedBy.LastName,
			Avatar:    e.ExecutedBy.Avatar,
		}
	}

	return resp
}
