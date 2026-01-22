package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Report represents a saved report configuration
type Report struct {
	ID           uuid.UUID      `gorm:"type:uuid;primary_key" json:"id"`
	Name         string         `gorm:"size:255;not null" json:"name"`
	Description  string         `gorm:"type:text" json:"description"`
	DataSource   string         `gorm:"size:50;not null;index" json:"data_source"` // incidents, users, workflows, etc.
	Columns      string         `gorm:"type:text" json:"columns"`                  // JSON array of column configs
	Filters      string         `gorm:"type:text" json:"filters"`                  // JSON array of filter configs
	Sorting      string         `gorm:"type:text" json:"sorting"`                  // JSON object for sorting config
	Grouping     string         `gorm:"type:text" json:"grouping"`                 // JSON object for grouping config
	OutputFormat string         `gorm:"size:20;default:'table'" json:"output_format"`
	IsPublic     bool           `gorm:"default:false" json:"is_public"`
	IsScheduled  bool           `gorm:"default:false" json:"is_scheduled"`
	Schedule     string         `gorm:"type:text" json:"schedule"` // JSON object for schedule config
	CreatedByID  uuid.UUID      `gorm:"type:uuid;index" json:"created_by_id"`
	CreatedBy    *User          `gorm:"foreignKey:CreatedByID" json:"created_by,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	DeletedAt    gorm.DeletedAt `gorm:"index" json:"-"`
}

func (r *Report) BeforeCreate(tx *gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	return nil
}

// ReportExecution stores history of report executions
type ReportExecution struct {
	ID           uuid.UUID  `gorm:"type:uuid;primary_key" json:"id"`
	ReportID     uuid.UUID  `gorm:"type:uuid;index;not null" json:"report_id"`
	Report       *Report    `gorm:"foreignKey:ReportID" json:"report,omitempty"`
	ExecutedByID uuid.UUID  `gorm:"type:uuid;index" json:"executed_by_id"`
	ExecutedBy   *User      `gorm:"foreignKey:ExecutedByID" json:"executed_by,omitempty"`
	Status       string     `gorm:"size:20;not null;default:'pending'" json:"status"` // pending, running, completed, failed
	ResultCount  int        `gorm:"default:0" json:"result_count"`
	FilePath     string     `gorm:"size:500" json:"file_path"` // Path to exported file if any
	Error        string     `gorm:"type:text" json:"error"`
	StartedAt    *time.Time `json:"started_at"`
	CompletedAt  *time.Time `json:"completed_at"`
	CreatedAt    time.Time  `json:"created_at"`
}

func (e *ReportExecution) BeforeCreate(tx *gorm.DB) error {
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	return nil
}

// Request/Response types

type ReportColumnConfig struct {
	Field   string `json:"field"`
	Label   string `json:"label"`
	Visible bool   `json:"visible"`
	Width   int    `json:"width,omitempty"`
}

type ReportFilterConfig struct {
	Field    string      `json:"field"`
	Operator string      `json:"operator"` // equals, not_equals, contains, gt, lt, gte, lte, in, between, is_null, is_not_null
	Value    interface{} `json:"value"`
}

type ReportSortConfig struct {
	Field     string `json:"field"`
	Direction string `json:"direction"` // asc, desc
}

type ReportGroupConfig struct {
	Field       string `json:"field"`
	Aggregation string `json:"aggregation,omitempty"` // count, sum, avg, min, max
}

type ReportScheduleConfig struct {
	Enabled   bool     `json:"enabled"`
	Frequency string   `json:"frequency"` // daily, weekly, monthly
	Time      string   `json:"time"`      // HH:MM format
	DayOfWeek *int     `json:"day_of_week,omitempty"`
	DayOfMonth *int    `json:"day_of_month,omitempty"`
	Recipients []string `json:"recipients"` // Email addresses
}

// ReportCreateRequestConfig for nested config in create request
type ReportCreateRequestConfig struct {
	Columns []ReportColumnConfig `json:"columns" validate:"required,min=1"`
	Filters []ReportFilterConfig `json:"filters"`
	Sorting []ReportSortConfig   `json:"sorting"`
	Options *ReportConfigOptions `json:"options,omitempty"`
}

type ReportCreateRequest struct {
	Name        string                    `json:"name" validate:"required,max=255"`
	Description string                    `json:"description"`
	DataSource  string                    `json:"data_source" validate:"required,oneof=incidents action_logs users workflows departments locations classifications"`
	Config      ReportCreateRequestConfig `json:"config" validate:"required"`
	IsPublic    bool                      `json:"is_public"`
}

type ReportUpdateRequest struct {
	Name        string                     `json:"name" validate:"omitempty,max=255"`
	Description string                     `json:"description"`
	Config      *ReportCreateRequestConfig `json:"config"`
	IsPublic    *bool                      `json:"is_public"`
}

type ReportExecuteRequest struct {
	Filters      []ReportFilterConfig `json:"filters"`      // Override filters
	ExportFormat string               `json:"export_format"` // csv, xlsx, pdf, or empty for preview
	Limit        int                  `json:"limit"`
	Page         int                  `json:"page"`
}

// ReportExportRequest is used for exporting reports
type ReportExportRequest struct {
	DataSource string               `json:"data_source" validate:"required"`
	Columns    []string             `json:"columns" validate:"required,min=1"`
	Filters    []ReportFilterConfig `json:"filters"`
	Sorting    []ReportSortConfig   `json:"sorting"`
	Format     string               `json:"format" validate:"required,oneof=xlsx pdf"`
	Options    *ReportExportOptions `json:"options"`
}

type ReportExportOptions struct {
	Title            string `json:"title"`
	IncludeFilters   bool   `json:"includeFilters"`
	IncludeTimestamp bool   `json:"includeTimestamp"`
}

// ReportQueryRequest is used for ad-hoc report queries without saving
type ReportQueryRequest struct {
	DataSource string               `json:"data_source" validate:"required"`
	Columns    []string             `json:"columns" validate:"required,min=1"`
	Filters    []ReportFilterConfig `json:"filters"`
	Sorting    []ReportSortConfig   `json:"sorting"`
	Page       int                  `json:"page"`
	Limit      int                  `json:"limit"`
	Options    *ReportQueryOptions  `json:"options"`
}

type ReportQueryOptions struct {
	IncludeSubDepartments bool `json:"includeSubDepartments"`
	IncludeSubLocations   bool `json:"includeSubLocations"`
}

type ReportFilter struct {
	DataSource  *string `json:"data_source"`
	CreatedByID *uuid.UUID `json:"created_by_id"`
	IsPublic    *bool   `json:"is_public"`
	Search      string  `json:"search"`
	Page        int     `json:"page"`
	Limit       int     `json:"limit"`
}

// Response types

// ReportTemplateConfig is the nested config structure matching frontend expectations
type ReportTemplateConfig struct {
	Columns []ReportColumnConfig `json:"columns"`
	Filters []ReportFilterConfig `json:"filters"`
	Sorting []ReportSortConfig   `json:"sorting"`
	Options *ReportConfigOptions `json:"options,omitempty"`
}

type ReportConfigOptions struct {
	IncludeSubDepartments bool `json:"includeSubDepartments,omitempty"`
	IncludeSubLocations   bool `json:"includeSubLocations,omitempty"`
	Limit                 int  `json:"limit,omitempty"`
}

type ReportResponse struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	Description string                `json:"description"`
	DataSource  string                `json:"data_source"`
	Config      ReportTemplateConfig  `json:"config"`
	IsPublic    bool                  `json:"is_public"`
	IsSystem    bool                  `json:"is_system"`
	CreatedBy   *UserBasicResponse    `json:"created_by,omitempty"`
	SharedWith  []SharedUserResponse  `json:"shared_with,omitempty"`
	CanEdit     bool                  `json:"can_edit"`
	CreatedAt   string                `json:"created_at"`
	UpdatedAt   string                `json:"updated_at"`
}

type SharedUserResponse struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	CanEdit  bool   `json:"can_edit"`
}

type ReportExecutionResponse struct {
	ID          string             `json:"id"`
	ReportID    string             `json:"report_id"`
	ExecutedBy  *UserBasicResponse `json:"executed_by,omitempty"`
	Status      string             `json:"status"`
	ResultCount int                `json:"result_count"`
	FilePath    string             `json:"file_path,omitempty"`
	Error       string             `json:"error,omitempty"`
	StartedAt   string             `json:"started_at,omitempty"`
	CompletedAt string             `json:"completed_at,omitempty"`
	CreatedAt   string             `json:"created_at"`
}

type ReportResultResponse struct {
	Columns []ReportColumnConfig `json:"columns"`
	Data    []map[string]interface{} `json:"data"`
	Total   int64                `json:"total"`
	Page    int                  `json:"page"`
	Limit   int                  `json:"limit"`
}

// ReportQueryResponse is the response for ad-hoc report queries
type ReportQueryResponse struct {
	Success    bool                     `json:"success"`
	Data       []map[string]interface{} `json:"data"`
	Columns    []string                 `json:"columns"`
	TotalItems int64                    `json:"total_items"`
	TotalPages int                      `json:"total_pages"`
	Page       int                      `json:"page"`
	Limit      int                      `json:"limit"`
}

type UserBasicResponse struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Username  string `json:"username"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Avatar    string `json:"avatar,omitempty"`
}

// DataSource metadata for frontend
type DataSourceField struct {
	Field    string `json:"field"`
	Label    string `json:"label"`
	Type     string `json:"type"` // string, number, date, boolean, uuid
	Filterable bool `json:"filterable"`
	Sortable   bool `json:"sortable"`
}

type DataSourceInfo struct {
	Name   string            `json:"name"`
	Label  string            `json:"label"`
	Fields []DataSourceField `json:"fields"`
}
