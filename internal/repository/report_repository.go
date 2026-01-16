package repository

import (
	"context"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type ReportRepository interface {
	// Report CRUD
	Create(ctx context.Context, report *models.Report) error
	FindByID(ctx context.Context, id uuid.UUID) (*models.Report, error)
	FindByIDWithRelations(ctx context.Context, id uuid.UUID) (*models.Report, error)
	List(ctx context.Context, filter *models.ReportFilter) ([]models.Report, int64, error)
	Update(ctx context.Context, report *models.Report) error
	Delete(ctx context.Context, id uuid.UUID) error

	// Report Execution
	CreateExecution(ctx context.Context, execution *models.ReportExecution) error
	FindExecutionByID(ctx context.Context, id uuid.UUID) (*models.ReportExecution, error)
	ListExecutions(ctx context.Context, reportID uuid.UUID, page, limit int) ([]models.ReportExecution, int64, error)
	UpdateExecution(ctx context.Context, execution *models.ReportExecution) error

	// Data queries for report execution
	ExecuteIncidentQuery(ctx context.Context, filters []models.ReportFilterConfig, sorting *models.ReportSortConfig, page, limit int) ([]map[string]interface{}, int64, error)
	ExecuteUserQuery(ctx context.Context, filters []models.ReportFilterConfig, sorting *models.ReportSortConfig, page, limit int) ([]map[string]interface{}, int64, error)
	ExecuteWorkflowQuery(ctx context.Context, filters []models.ReportFilterConfig, sorting *models.ReportSortConfig, page, limit int) ([]map[string]interface{}, int64, error)
	ExecuteDepartmentQuery(ctx context.Context, filters []models.ReportFilterConfig, sorting *models.ReportSortConfig, page, limit int) ([]map[string]interface{}, int64, error)
	ExecuteLocationQuery(ctx context.Context, filters []models.ReportFilterConfig, sorting *models.ReportSortConfig, page, limit int) ([]map[string]interface{}, int64, error)
	ExecuteClassificationQuery(ctx context.Context, filters []models.ReportFilterConfig, sorting *models.ReportSortConfig, page, limit int) ([]map[string]interface{}, int64, error)
}

type reportRepository struct {
	db *gorm.DB
}

func NewReportRepository(db *gorm.DB) ReportRepository {
	return &reportRepository{db: db}
}

// Report CRUD

func (r *reportRepository) Create(ctx context.Context, report *models.Report) error {
	return r.db.WithContext(ctx).Create(report).Error
}

func (r *reportRepository) FindByID(ctx context.Context, id uuid.UUID) (*models.Report, error) {
	var report models.Report
	err := r.db.WithContext(ctx).First(&report, "id = ?", id).Error
	return &report, err
}

func (r *reportRepository) FindByIDWithRelations(ctx context.Context, id uuid.UUID) (*models.Report, error) {
	var report models.Report
	err := r.db.WithContext(ctx).
		Preload("CreatedBy").
		First(&report, "id = ?", id).Error
	return &report, err
}

func (r *reportRepository) List(ctx context.Context, filter *models.ReportFilter) ([]models.Report, int64, error) {
	var reports []models.Report
	var total int64

	query := r.db.WithContext(ctx).Model(&models.Report{})

	if filter.DataSource != nil && *filter.DataSource != "" {
		query = query.Where("data_source = ?", *filter.DataSource)
	}
	if filter.CreatedByID != nil {
		query = query.Where("created_by_id = ?", *filter.CreatedByID)
	}
	if filter.IsPublic != nil {
		query = query.Where("is_public = ?", *filter.IsPublic)
	}
	if filter.Search != "" {
		search := "%" + filter.Search + "%"
		query = query.Where("name ILIKE ? OR description ILIKE ?", search, search)
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	offset := (filter.Page - 1) * filter.Limit
	err := query.
		Preload("CreatedBy").
		Order("created_at DESC").
		Offset(offset).
		Limit(filter.Limit).
		Find(&reports).Error

	return reports, total, err
}

func (r *reportRepository) Update(ctx context.Context, report *models.Report) error {
	return r.db.WithContext(ctx).Save(report).Error
}

func (r *reportRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Delete(&models.Report{}, "id = ?", id).Error
}

// Report Execution

func (r *reportRepository) CreateExecution(ctx context.Context, execution *models.ReportExecution) error {
	return r.db.WithContext(ctx).Create(execution).Error
}

func (r *reportRepository) FindExecutionByID(ctx context.Context, id uuid.UUID) (*models.ReportExecution, error) {
	var execution models.ReportExecution
	err := r.db.WithContext(ctx).
		Preload("ExecutedBy").
		First(&execution, "id = ?", id).Error
	return &execution, err
}

func (r *reportRepository) ListExecutions(ctx context.Context, reportID uuid.UUID, page, limit int) ([]models.ReportExecution, int64, error) {
	var executions []models.ReportExecution
	var total int64

	query := r.db.WithContext(ctx).Model(&models.ReportExecution{}).Where("report_id = ?", reportID)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	offset := (page - 1) * limit
	err := query.
		Preload("ExecutedBy").
		Order("created_at DESC").
		Offset(offset).
		Limit(limit).
		Find(&executions).Error

	return executions, total, err
}

func (r *reportRepository) UpdateExecution(ctx context.Context, execution *models.ReportExecution) error {
	return r.db.WithContext(ctx).Save(execution).Error
}

// Data query helpers

func (r *reportRepository) applyFilters(query *gorm.DB, filters []models.ReportFilterConfig) *gorm.DB {
	for _, f := range filters {
		switch f.Operator {
		case "equals":
			query = query.Where(f.Field+" = ?", f.Value)
		case "not_equals":
			query = query.Where(f.Field+" != ?", f.Value)
		case "contains":
			query = query.Where(f.Field+" ILIKE ?", "%"+f.Value.(string)+"%")
		case "starts_with":
			query = query.Where(f.Field+" ILIKE ?", f.Value.(string)+"%")
		case "ends_with":
			query = query.Where(f.Field+" ILIKE ?", "%"+f.Value.(string))
		case "gt":
			query = query.Where(f.Field+" > ?", f.Value)
		case "lt":
			query = query.Where(f.Field+" < ?", f.Value)
		case "gte":
			query = query.Where(f.Field+" >= ?", f.Value)
		case "lte":
			query = query.Where(f.Field+" <= ?", f.Value)
		case "in":
			query = query.Where(f.Field+" IN ?", f.Value)
		case "is_null":
			query = query.Where(f.Field + " IS NULL")
		case "is_not_null":
			query = query.Where(f.Field + " IS NOT NULL")
		case "between":
			if arr, ok := f.Value.([]interface{}); ok && len(arr) == 2 {
				query = query.Where(f.Field+" BETWEEN ? AND ?", arr[0], arr[1])
			}
		}
	}
	return query
}

func (r *reportRepository) applySorting(query *gorm.DB, sorting *models.ReportSortConfig) *gorm.DB {
	if sorting != nil && sorting.Field != "" {
		direction := "ASC"
		if sorting.Direction == "desc" {
			direction = "DESC"
		}
		query = query.Order(sorting.Field + " " + direction)
	}
	return query
}

// Data queries for report execution

func (r *reportRepository) ExecuteIncidentQuery(ctx context.Context, filters []models.ReportFilterConfig, sorting *models.ReportSortConfig, page, limit int) ([]map[string]interface{}, int64, error) {
	var total int64
	var results []map[string]interface{}

	query := r.db.WithContext(ctx).Model(&models.Incident{})
	query = r.applyFilters(query, filters)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	query = r.applySorting(query, sorting)
	if sorting == nil {
		query = query.Order("created_at DESC")
	}

	offset := (page - 1) * limit
	rows, err := query.
		Select("incidents.*, "+
			"reporters.email as reporter_email, reporters.first_name as reporter_first_name, reporters.last_name as reporter_last_name, "+
			"assignees.email as assignee_email, assignees.first_name as assignee_first_name, assignees.last_name as assignee_last_name, "+
			"workflow_states.name as current_state_name, "+
			"classifications.name as classification_name, "+
			"departments.name as department_name, "+
			"locations.name as location_name").
		Joins("LEFT JOIN users as reporters ON incidents.reporter_id = reporters.id").
		Joins("LEFT JOIN users as assignees ON incidents.assignee_id = assignees.id").
		Joins("LEFT JOIN workflow_states ON incidents.current_state_id = workflow_states.id").
		Joins("LEFT JOIN classifications ON incidents.classification_id = classifications.id").
		Joins("LEFT JOIN departments ON incidents.department_id = departments.id").
		Joins("LEFT JOIN locations ON incidents.location_id = locations.id").
		Offset(offset).
		Limit(limit).
		Rows()

	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	for rows.Next() {
		columns := make([]interface{}, len(cols))
		columnPointers := make([]interface{}, len(cols))
		for i := range columns {
			columnPointers[i] = &columns[i]
		}

		if err := rows.Scan(columnPointers...); err != nil {
			continue
		}

		row := make(map[string]interface{})
		for i, colName := range cols {
			val := columns[i]
			if b, ok := val.([]byte); ok {
				row[colName] = string(b)
			} else {
				row[colName] = val
			}
		}
		results = append(results, row)
	}

	return results, total, nil
}

func (r *reportRepository) ExecuteUserQuery(ctx context.Context, filters []models.ReportFilterConfig, sorting *models.ReportSortConfig, page, limit int) ([]map[string]interface{}, int64, error) {
	var total int64
	var results []map[string]interface{}

	query := r.db.WithContext(ctx).Model(&models.User{})
	query = r.applyFilters(query, filters)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	query = r.applySorting(query, sorting)
	if sorting == nil {
		query = query.Order("created_at DESC")
	}

	offset := (page - 1) * limit
	rows, err := query.
		Select("users.id, users.email, users.username, users.first_name, users.last_name, users.phone, users.avatar, users.is_active, users.is_super_admin, users.created_at, users.updated_at, "+
			"departments.name as department_name, locations.name as location_name").
		Joins("LEFT JOIN departments ON users.department_id = departments.id").
		Joins("LEFT JOIN locations ON users.location_id = locations.id").
		Offset(offset).
		Limit(limit).
		Rows()

	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	for rows.Next() {
		columns := make([]interface{}, len(cols))
		columnPointers := make([]interface{}, len(cols))
		for i := range columns {
			columnPointers[i] = &columns[i]
		}

		if err := rows.Scan(columnPointers...); err != nil {
			continue
		}

		row := make(map[string]interface{})
		for i, colName := range cols {
			val := columns[i]
			if b, ok := val.([]byte); ok {
				row[colName] = string(b)
			} else {
				row[colName] = val
			}
		}
		results = append(results, row)
	}

	return results, total, nil
}

func (r *reportRepository) ExecuteWorkflowQuery(ctx context.Context, filters []models.ReportFilterConfig, sorting *models.ReportSortConfig, page, limit int) ([]map[string]interface{}, int64, error) {
	var total int64
	var results []map[string]interface{}

	query := r.db.WithContext(ctx).Model(&models.Workflow{})
	query = r.applyFilters(query, filters)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	query = r.applySorting(query, sorting)
	if sorting == nil {
		query = query.Order("created_at DESC")
	}

	offset := (page - 1) * limit
	rows, err := query.
		Select("workflows.*").
		Offset(offset).
		Limit(limit).
		Rows()

	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	for rows.Next() {
		columns := make([]interface{}, len(cols))
		columnPointers := make([]interface{}, len(cols))
		for i := range columns {
			columnPointers[i] = &columns[i]
		}

		if err := rows.Scan(columnPointers...); err != nil {
			continue
		}

		row := make(map[string]interface{})
		for i, colName := range cols {
			val := columns[i]
			if b, ok := val.([]byte); ok {
				row[colName] = string(b)
			} else {
				row[colName] = val
			}
		}
		results = append(results, row)
	}

	return results, total, nil
}

func (r *reportRepository) ExecuteDepartmentQuery(ctx context.Context, filters []models.ReportFilterConfig, sorting *models.ReportSortConfig, page, limit int) ([]map[string]interface{}, int64, error) {
	var total int64
	var results []map[string]interface{}

	query := r.db.WithContext(ctx).Model(&models.Department{})
	query = r.applyFilters(query, filters)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	query = r.applySorting(query, sorting)
	if sorting == nil {
		query = query.Order("name ASC")
	}

	offset := (page - 1) * limit
	rows, err := query.
		Select("departments.*, parents.name as parent_name").
		Joins("LEFT JOIN departments as parents ON departments.parent_id = parents.id").
		Offset(offset).
		Limit(limit).
		Rows()

	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	for rows.Next() {
		columns := make([]interface{}, len(cols))
		columnPointers := make([]interface{}, len(cols))
		for i := range columns {
			columnPointers[i] = &columns[i]
		}

		if err := rows.Scan(columnPointers...); err != nil {
			continue
		}

		row := make(map[string]interface{})
		for i, colName := range cols {
			val := columns[i]
			if b, ok := val.([]byte); ok {
				row[colName] = string(b)
			} else {
				row[colName] = val
			}
		}
		results = append(results, row)
	}

	return results, total, nil
}

func (r *reportRepository) ExecuteLocationQuery(ctx context.Context, filters []models.ReportFilterConfig, sorting *models.ReportSortConfig, page, limit int) ([]map[string]interface{}, int64, error) {
	var total int64
	var results []map[string]interface{}

	query := r.db.WithContext(ctx).Model(&models.Location{})
	query = r.applyFilters(query, filters)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	query = r.applySorting(query, sorting)
	if sorting == nil {
		query = query.Order("name ASC")
	}

	offset := (page - 1) * limit
	rows, err := query.
		Select("locations.*, parents.name as parent_name").
		Joins("LEFT JOIN locations as parents ON locations.parent_id = parents.id").
		Offset(offset).
		Limit(limit).
		Rows()

	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	for rows.Next() {
		columns := make([]interface{}, len(cols))
		columnPointers := make([]interface{}, len(cols))
		for i := range columns {
			columnPointers[i] = &columns[i]
		}

		if err := rows.Scan(columnPointers...); err != nil {
			continue
		}

		row := make(map[string]interface{})
		for i, colName := range cols {
			val := columns[i]
			if b, ok := val.([]byte); ok {
				row[colName] = string(b)
			} else {
				row[colName] = val
			}
		}
		results = append(results, row)
	}

	return results, total, nil
}

func (r *reportRepository) ExecuteClassificationQuery(ctx context.Context, filters []models.ReportFilterConfig, sorting *models.ReportSortConfig, page, limit int) ([]map[string]interface{}, int64, error) {
	var total int64
	var results []map[string]interface{}

	query := r.db.WithContext(ctx).Model(&models.Classification{})
	query = r.applyFilters(query, filters)

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	query = r.applySorting(query, sorting)
	if sorting == nil {
		query = query.Order("name ASC")
	}

	offset := (page - 1) * limit
	rows, err := query.
		Select("classifications.*, parents.name as parent_name").
		Joins("LEFT JOIN classifications as parents ON classifications.parent_id = parents.id").
		Offset(offset).
		Limit(limit).
		Rows()

	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	for rows.Next() {
		columns := make([]interface{}, len(cols))
		columnPointers := make([]interface{}, len(cols))
		for i := range columns {
			columnPointers[i] = &columns[i]
		}

		if err := rows.Scan(columnPointers...); err != nil {
			continue
		}

		row := make(map[string]interface{})
		for i, colName := range cols {
			val := columns[i]
			if b, ok := val.([]byte); ok {
				row[colName] = string(b)
			} else {
				row[colName] = val
			}
		}
		results = append(results, row)
	}

	return results, total, nil
}
