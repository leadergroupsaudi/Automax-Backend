package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/google/uuid"
	"github.com/jung-kurt/gofpdf"
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
	QueryReport(ctx context.Context, req *models.ReportQueryRequest) (*models.ReportQueryResponse, error)
	ExportReport(ctx context.Context, req *models.ReportExportRequest) ([]byte, string, string, error)
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
	columnsJSON, _ := json.Marshal(req.Config.Columns)
	filtersJSON, _ := json.Marshal(req.Config.Filters)
	sortingJSON, _ := json.Marshal(req.Config.Sorting)

	report := &models.Report{
		Name:         req.Name,
		Description:  req.Description,
		DataSource:   req.DataSource,
		Columns:      string(columnsJSON),
		Filters:      string(filtersJSON),
		Sorting:      string(sortingJSON),
		OutputFormat: "table",
		IsPublic:     req.IsPublic,
		CreatedByID:  userID,
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
	if req.Config != nil {
		if req.Config.Columns != nil {
			columnsJSON, _ := json.Marshal(req.Config.Columns)
			report.Columns = string(columnsJSON)
		}
		if req.Config.Filters != nil {
			filtersJSON, _ := json.Marshal(req.Config.Filters)
			report.Filters = string(filtersJSON)
		}
		if req.Config.Sorting != nil {
			sortingJSON, _ := json.Marshal(req.Config.Sorting)
			report.Sorting = string(sortingJSON)
		}
	}
	if req.IsPublic != nil {
		report.IsPublic = *req.IsPublic
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

	// Get sorting from config
	var sorting *models.ReportSortConfig
	if len(req.Config.Sorting) > 0 {
		sorting = &req.Config.Sorting[0]
	}

	switch req.DataSource {
	case "incidents":
		data, total, err = s.reportRepo.ExecuteIncidentQuery(ctx, req.Config.Filters, sorting, page, limit)
	case "users":
		data, total, err = s.reportRepo.ExecuteUserQuery(ctx, req.Config.Filters, sorting, page, limit)
	case "workflows":
		data, total, err = s.reportRepo.ExecuteWorkflowQuery(ctx, req.Config.Filters, sorting, page, limit)
	case "departments":
		data, total, err = s.reportRepo.ExecuteDepartmentQuery(ctx, req.Config.Filters, sorting, page, limit)
	case "locations":
		data, total, err = s.reportRepo.ExecuteLocationQuery(ctx, req.Config.Filters, sorting, page, limit)
	case "classifications":
		data, total, err = s.reportRepo.ExecuteClassificationQuery(ctx, req.Config.Filters, sorting, page, limit)
	default:
		return nil, errors.New("unsupported data source")
	}

	if err != nil {
		return nil, err
	}

	return &models.ReportResultResponse{
		Columns: req.Config.Columns,
		Data:    data,
		Total:   total,
		Page:    page,
		Limit:   limit,
	}, nil
}

func (s *reportService) QueryReport(ctx context.Context, req *models.ReportQueryRequest) (*models.ReportQueryResponse, error) {
	var data []map[string]interface{}
	var total int64
	var err error

	// Convert sorting to the format expected by repository
	var sorting *models.ReportSortConfig
	if len(req.Sorting) > 0 {
		sorting = &req.Sorting[0]
	}

	switch req.DataSource {
	case "incidents":
		data, total, err = s.reportRepo.ExecuteIncidentQuery(ctx, req.Filters, sorting, req.Page, req.Limit)
	case "users":
		data, total, err = s.reportRepo.ExecuteUserQuery(ctx, req.Filters, sorting, req.Page, req.Limit)
	case "workflows":
		data, total, err = s.reportRepo.ExecuteWorkflowQuery(ctx, req.Filters, sorting, req.Page, req.Limit)
	case "departments":
		data, total, err = s.reportRepo.ExecuteDepartmentQuery(ctx, req.Filters, sorting, req.Page, req.Limit)
	case "locations":
		data, total, err = s.reportRepo.ExecuteLocationQuery(ctx, req.Filters, sorting, req.Page, req.Limit)
	case "classifications":
		data, total, err = s.reportRepo.ExecuteClassificationQuery(ctx, req.Filters, sorting, req.Page, req.Limit)
	default:
		return nil, errors.New("unsupported data source")
	}

	if err != nil {
		return nil, err
	}

	// Calculate total pages
	totalPages := int(total) / req.Limit
	if int(total)%req.Limit > 0 {
		totalPages++
	}

	return &models.ReportQueryResponse{
		Success:    true,
		Data:       data,
		Columns:    req.Columns,
		TotalItems: total,
		TotalPages: totalPages,
		Page:       req.Page,
		Limit:      req.Limit,
	}, nil
}

func (s *reportService) ExportReport(ctx context.Context, req *models.ReportExportRequest) ([]byte, string, string, error) {
	var data []map[string]interface{}
	var err error

	// Get sorting from request
	var sorting *models.ReportSortConfig
	if len(req.Sorting) > 0 {
		sorting = &req.Sorting[0]
	}

	// Fetch all data (no pagination limit for export)
	limit := 10000

	switch req.DataSource {
	case "incidents":
		data, _, err = s.reportRepo.ExecuteIncidentQuery(ctx, req.Filters, sorting, 1, limit)
	case "users":
		data, _, err = s.reportRepo.ExecuteUserQuery(ctx, req.Filters, sorting, 1, limit)
	case "workflows":
		data, _, err = s.reportRepo.ExecuteWorkflowQuery(ctx, req.Filters, sorting, 1, limit)
	case "departments":
		data, _, err = s.reportRepo.ExecuteDepartmentQuery(ctx, req.Filters, sorting, 1, limit)
	case "locations":
		data, _, err = s.reportRepo.ExecuteLocationQuery(ctx, req.Filters, sorting, 1, limit)
	case "classifications":
		data, _, err = s.reportRepo.ExecuteClassificationQuery(ctx, req.Filters, sorting, 1, limit)
	default:
		return nil, "", "", errors.New("unsupported data source")
	}

	if err != nil {
		return nil, "", "", err
	}

	title := "Report"
	if req.Options != nil && req.Options.Title != "" {
		title = req.Options.Title
	}

	timestamp := time.Now().Format("2006-01-02_150405")
	filename := title + "_" + timestamp

	if req.Format == "xlsx" {
		// Generate Excel file
		xlsxData, err := s.generateExcel(data, req.Columns, title, req.Options)
		if err != nil {
			return nil, "", "", err
		}
		return xlsxData, filename + ".xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", nil
	}

	// Generate PDF file
	pdfData, err := s.generatePDF(data, req.Columns, title, req.Options)
	if err != nil {
		return nil, "", "", err
	}
	return pdfData, filename + ".pdf", "application/pdf", nil
}

func (s *reportService) generateExcel(data []map[string]interface{}, columns []string, title string, options *models.ReportExportOptions) ([]byte, error) {
	// This is a simple CSV-like implementation
	// For a proper Excel file, you would use a library like excelize
	var buf bytes.Buffer

	// Write BOM for Excel UTF-8 compatibility
	buf.Write([]byte{0xEF, 0xBB, 0xBF})

	// Write header
	for i, col := range columns {
		if i > 0 {
			buf.WriteString("\t")
		}
		buf.WriteString(col)
	}
	buf.WriteString("\n")

	// Write data rows
	for _, row := range data {
		for i, col := range columns {
			if i > 0 {
				buf.WriteString("\t")
			}
			if val, ok := row[col]; ok && val != nil {
				buf.WriteString(formatExportValue(col, val))
			}
		}
		buf.WriteString("\n")
	}

	return buf.Bytes(), nil
}

func (s *reportService) generatePDF(data []map[string]interface{}, columns []string, title string, options *models.ReportExportOptions) ([]byte, error) {
	pdf := gofpdf.New("L", "mm", "A4", "") // Landscape for tables
	pdf.SetMargins(10, 15, 10)
	pdf.SetAutoPageBreak(true, 15)
	pdf.AddPage()

	// Title
	pdf.SetFont("Arial", "B", 16)
	pdf.CellFormat(0, 10, title, "", 1, "C", false, 0, "")
	pdf.Ln(5)

	// Timestamp if requested
	if options != nil && options.IncludeTimestamp {
		pdf.SetFont("Arial", "I", 10)
		pdf.SetTextColor(128, 128, 128)
		pdf.CellFormat(0, 6, fmt.Sprintf("Generated: %s", time.Now().Format("2006-01-02 15:04:05")), "", 1, "C", false, 0, "")
		pdf.SetTextColor(0, 0, 0)
		pdf.Ln(5)
	}

	// Calculate column widths based on number of columns
	pageWidth := 277.0 // A4 landscape minus margins
	colCount := len(columns)
	colWidth := pageWidth / float64(colCount)
	if colWidth > 50 {
		colWidth = 50 // Max column width
	}

	// Table header
	pdf.SetFont("Arial", "B", 9)
	pdf.SetFillColor(59, 130, 246) // Blue background
	pdf.SetTextColor(255, 255, 255)
	for _, col := range columns {
		// Truncate column name if too long
		displayCol := col
		if len(displayCol) > 15 {
			displayCol = displayCol[:12] + "..."
		}
		pdf.CellFormat(colWidth, 8, displayCol, "1", 0, "C", true, 0, "")
	}
	pdf.Ln(-1)

	// Table data
	pdf.SetFont("Arial", "", 8)
	pdf.SetTextColor(0, 0, 0)
	pdf.SetFillColor(240, 240, 240)
	fill := false

	for _, row := range data {
		// Check if we need a new page
		if pdf.GetY() > 180 {
			pdf.AddPage()
			// Reprint header on new page
			pdf.SetFont("Arial", "B", 9)
			pdf.SetFillColor(59, 130, 246)
			pdf.SetTextColor(255, 255, 255)
			for _, col := range columns {
				displayCol := col
				if len(displayCol) > 15 {
					displayCol = displayCol[:12] + "..."
				}
				pdf.CellFormat(colWidth, 8, displayCol, "1", 0, "C", true, 0, "")
			}
			pdf.Ln(-1)
			pdf.SetFont("Arial", "", 8)
			pdf.SetTextColor(0, 0, 0)
			pdf.SetFillColor(240, 240, 240)
		}

		for _, col := range columns {
			val := ""
			if v, ok := row[col]; ok && v != nil {
				val = formatExportValue(col, v)
			}
			// Truncate value if too long
			if len(val) > 20 {
				val = val[:17] + "..."
			}
			pdf.CellFormat(colWidth, 7, val, "1", 0, "L", fill, 0, "")
		}
		pdf.Ln(-1)
		fill = !fill
	}

	// Total records
	pdf.Ln(5)
	pdf.SetFont("Arial", "I", 10)
	pdf.CellFormat(0, 6, fmt.Sprintf("Total records: %d", len(data)), "", 1, "L", false, 0, "")

	// Output to buffer
	var buf bytes.Buffer
	err := pdf.Output(&buf)
	if err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func formatValue(v interface{}) string {
	switch val := v.(type) {
	case string:
		return val
	case float64:
		if val == float64(int64(val)) {
			return strconv.FormatInt(int64(val), 10)
		}
		return strconv.FormatFloat(val, 'f', 2, 64)
	case int:
		return strconv.Itoa(val)
	case int64:
		return strconv.FormatInt(val, 10)
	case bool:
		if val {
			return "Yes"
		}
		return "No"
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", val)
	}
}

// formatExportValue formats a value for export, handling enums like priority/severity
func formatExportValue(col string, v interface{}) string {
	if v == nil {
		return ""
	}

	// Handle priority enum
	if col == "priority" {
		switch val := v.(type) {
		case float64:
			return getPriorityLabel(int(val))
		case int:
			return getPriorityLabel(val)
		case int64:
			return getPriorityLabel(int(val))
		}
	}

	// Handle severity enum
	if col == "severity" {
		switch val := v.(type) {
		case float64:
			return getSeverityLabel(int(val))
		case int:
			return getSeverityLabel(val)
		case int64:
			return getSeverityLabel(int(val))
		}
	}

	// Handle boolean fields
	if col == "sla_breached" || col == "is_active" || col == "is_default" || col == "is_public" || col == "is_super_admin" {
		switch val := v.(type) {
		case bool:
			if val {
				return "Yes"
			}
			return "No"
		}
	}

	return formatValue(v)
}

func getPriorityLabel(priority int) string {
	switch priority {
	case 1:
		return "Critical"
	case 2:
		return "High"
	case 3:
		return "Medium"
	case 4:
		return "Low"
	case 5:
		return "Minimal"
	default:
		return strconv.Itoa(priority)
	}
}

func getSeverityLabel(severity int) string {
	switch severity {
	case 1:
		return "Critical"
	case 2:
		return "Major"
	case 3:
		return "Moderate"
	case 4:
		return "Minor"
	case 5:
		return "Trivial"
	default:
		return strconv.Itoa(severity)
	}
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
	var sorting []models.ReportSortConfig

	json.Unmarshal([]byte(r.Columns), &columns)
	json.Unmarshal([]byte(r.Filters), &filters)
	json.Unmarshal([]byte(r.Sorting), &sorting)

	// Build the config object matching frontend structure
	config := models.ReportTemplateConfig{
		Columns: columns,
		Filters: filters,
		Sorting: sorting,
	}

	resp := &models.ReportResponse{
		ID:          r.ID.String(),
		Name:        r.Name,
		Description: r.Description,
		DataSource:  r.DataSource,
		Config:      config,
		IsPublic:    r.IsPublic,
		IsSystem:    false, // Reports created by users are not system reports
		CanEdit:     true,  // Will be set based on permissions later
		CreatedAt:   r.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   r.UpdatedAt.Format(time.RFC3339),
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
