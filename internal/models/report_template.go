package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ReportTemplate represents a saved report template configuration
type ReportTemplate struct {
	ID          uuid.UUID      `gorm:"type:uuid;primary_key" json:"id"`
	Name        string         `gorm:"size:255;not null" json:"name"`
	Description string         `gorm:"type:text" json:"description"`
	Template    string         `gorm:"type:text" json:"template"` // JSON serialized TemplateConfig
	IsDefault   bool           `gorm:"default:false" json:"is_default"`
	IsPublic    bool           `gorm:"default:false" json:"is_public"`
	CreatedByID uuid.UUID      `gorm:"type:uuid;index" json:"created_by_id"`
	CreatedBy   *User          `gorm:"foreignKey:CreatedByID" json:"created_by,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   gorm.DeletedAt `gorm:"index" json:"-"`
}

func (r *ReportTemplate) BeforeCreate(tx *gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	return nil
}

// TemplateConfig is the main configuration structure for report templates
type TemplateConfig struct {
	PageSettings PageSettings     `json:"page_settings"`
	Header       *HeaderConfig    `json:"header,omitempty"`
	Footer       *FooterConfig    `json:"footer,omitempty"`
	Elements     []TemplateElement `json:"elements"`
	Styles       *GlobalStyles    `json:"styles,omitempty"`
}

// PageSettings defines the page layout
type PageSettings struct {
	Size        string  `json:"size"`        // A4, Letter, Legal, A3
	Orientation string  `json:"orientation"` // portrait, landscape
	MarginTop   float64 `json:"margin_top"`
	MarginRight float64 `json:"margin_right"`
	MarginBottom float64 `json:"margin_bottom"`
	MarginLeft  float64 `json:"margin_left"`
	Width       float64 `json:"width"`  // calculated or custom (mm)
	Height      float64 `json:"height"` // calculated or custom (mm)
}

// HeaderConfig defines the report header
type HeaderConfig struct {
	Enabled    bool              `json:"enabled"`
	Height     float64           `json:"height"` // mm
	Background string            `json:"background,omitempty"` // hex color
	Elements   []TemplateElement `json:"elements"`
	ShowOnAllPages bool          `json:"show_on_all_pages"`
}

// FooterConfig defines the report footer
type FooterConfig struct {
	Enabled        bool              `json:"enabled"`
	Height         float64           `json:"height"` // mm
	Background     string            `json:"background,omitempty"`
	Elements       []TemplateElement `json:"elements"`
	ShowPageNumber bool              `json:"show_page_number"`
	PageNumberFormat string          `json:"page_number_format"` // "Page {page} of {total}", "{page}/{total}"
	ShowOnAllPages bool              `json:"show_on_all_pages"`
}

// TemplateElement represents any element in the template
type TemplateElement struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"` // text, image, table, shape, line, spacer, dynamic_field, chart
	Position   ElementPosition `json:"position"`
	Size       ElementSize     `json:"size"`
	Style      ElementStyle    `json:"style"`
	Content    interface{}     `json:"content"` // Type-specific content
	Locked     bool            `json:"locked"`
	Visible    bool            `json:"visible"`
	ZIndex     int             `json:"z_index"`
}

// ElementPosition defines where an element is placed
type ElementPosition struct {
	X         float64 `json:"x"`         // mm from left
	Y         float64 `json:"y"`         // mm from top
	Anchor    string  `json:"anchor"`    // top-left, top-center, top-right, center, etc.
	Relative  bool    `json:"relative"`  // relative to parent or absolute
	ParentID  string  `json:"parent_id,omitempty"` // for nested elements
}

// ElementSize defines element dimensions
type ElementSize struct {
	Width     float64 `json:"width"`      // mm or percentage
	Height    float64 `json:"height"`     // mm or percentage
	MinWidth  float64 `json:"min_width"`
	MinHeight float64 `json:"min_height"`
	MaxWidth  float64 `json:"max_width"`
	MaxHeight float64 `json:"max_height"`
	Unit      string  `json:"unit"`       // mm, px, percent
	AutoHeight bool   `json:"auto_height"` // for tables/text
}

// ElementStyle defines visual styling
type ElementStyle struct {
	// Background
	BackgroundColor string `json:"background_color,omitempty"`
	BackgroundImage string `json:"background_image,omitempty"`

	// Border
	BorderWidth  float64 `json:"border_width"`
	BorderColor  string  `json:"border_color,omitempty"`
	BorderStyle  string  `json:"border_style,omitempty"` // solid, dashed, dotted
	BorderRadius float64 `json:"border_radius"`

	// Shadow
	Shadow       bool    `json:"shadow"`
	ShadowColor  string  `json:"shadow_color,omitempty"`
	ShadowBlur   float64 `json:"shadow_blur"`
	ShadowOffsetX float64 `json:"shadow_offset_x"`
	ShadowOffsetY float64 `json:"shadow_offset_y"`

	// Opacity
	Opacity float64 `json:"opacity"`

	// Padding
	PaddingTop    float64 `json:"padding_top"`
	PaddingRight  float64 `json:"padding_right"`
	PaddingBottom float64 `json:"padding_bottom"`
	PaddingLeft   float64 `json:"padding_left"`
}

// GlobalStyles defines document-wide styling
type GlobalStyles struct {
	PrimaryColor   string     `json:"primary_color"`
	SecondaryColor string     `json:"secondary_color"`
	AccentColor    string     `json:"accent_color"`
	TextColor      string     `json:"text_color"`
	DefaultFont    FontConfig `json:"default_font"`
}

// FontConfig defines font settings
type FontConfig struct {
	Family    string  `json:"family"`     // Arial, Helvetica, Times, etc.
	Size      float64 `json:"size"`       // pt
	Weight    string  `json:"weight"`     // normal, bold
	Style     string  `json:"style"`      // normal, italic
	Color     string  `json:"color"`
	LineHeight float64 `json:"line_height"`
}

// TextContent for text elements
type TextContent struct {
	Text        string     `json:"text"`
	Font        FontConfig `json:"font"`
	Alignment   string     `json:"alignment"`   // left, center, right, justify
	VAlignment  string     `json:"v_alignment"` // top, middle, bottom
	WordWrap    bool       `json:"word_wrap"`
	Truncate    bool       `json:"truncate"`
	MaxLines    int        `json:"max_lines"`
}

// ImageContent for image elements
type ImageContent struct {
	Source     string  `json:"source"`      // URL or base64
	SourceType string  `json:"source_type"` // url, base64, file
	Alt        string  `json:"alt"`
	Fit        string  `json:"fit"`         // contain, cover, fill, none
	Position   string  `json:"position"`    // center, top, bottom, left, right
}

// TableContent for table elements
type TableContent struct {
	DataSource    string         `json:"data_source"`    // incidents, users, etc.
	Columns       []TableColumn  `json:"columns"`
	Filters       []ReportFilterConfig `json:"filters"`
	Sorting       []ReportSortConfig   `json:"sorting"`
	ShowHeader    bool           `json:"show_header"`
	ShowRowNumbers bool          `json:"show_row_numbers"`
	AlternateRows bool           `json:"alternate_rows"`
	HeaderStyle   TableCellStyle `json:"header_style"`
	RowStyle      TableCellStyle `json:"row_style"`
	AltRowStyle   TableCellStyle `json:"alt_row_style"`
	MaxRows       int            `json:"max_rows"`
	Pagination    bool           `json:"pagination"`
	RowsPerPage   int            `json:"rows_per_page"`
}

// TableColumn defines a table column
type TableColumn struct {
	Field      string         `json:"field"`
	Label      string         `json:"label"`
	Width      float64        `json:"width"`      // mm or percentage
	WidthUnit  string         `json:"width_unit"` // mm, percent
	Alignment  string         `json:"alignment"`
	Format     string         `json:"format,omitempty"` // date, currency, number, etc.
	Style      TableCellStyle `json:"style"`
}

// TableCellStyle defines cell styling
type TableCellStyle struct {
	BackgroundColor string     `json:"background_color,omitempty"`
	TextColor       string     `json:"text_color,omitempty"`
	Font            FontConfig `json:"font"`
	BorderWidth     float64    `json:"border_width"`
	BorderColor     string     `json:"border_color,omitempty"`
	Padding         float64    `json:"padding"`
	VAlignment      string     `json:"v_alignment"` // top, middle, bottom
}

// ShapeContent for shape elements
type ShapeContent struct {
	ShapeType   string  `json:"shape_type"` // rectangle, circle, ellipse, triangle
	FillColor   string  `json:"fill_color,omitempty"`
	StrokeColor string  `json:"stroke_color,omitempty"`
	StrokeWidth float64 `json:"stroke_width"`
}

// LineContent for line elements
type LineContent struct {
	StartX      float64 `json:"start_x"`
	StartY      float64 `json:"start_y"`
	EndX        float64 `json:"end_x"`
	EndY        float64 `json:"end_y"`
	StrokeColor string  `json:"stroke_color"`
	StrokeWidth float64 `json:"stroke_width"`
	StrokeStyle string  `json:"stroke_style"` // solid, dashed, dotted
}

// DynamicFieldContent for dynamic data fields
type DynamicFieldContent struct {
	Field       string     `json:"field"`       // date, page_number, total_pages, user_name, report_name, custom
	Format      string     `json:"format"`      // for dates: "2006-01-02", etc.
	Prefix      string     `json:"prefix"`
	Suffix      string     `json:"suffix"`
	Font        FontConfig `json:"font"`
	Alignment   string     `json:"alignment"`
	CustomValue string     `json:"custom_value,omitempty"` // for custom fields
}

// ChartContent for chart elements
type ChartContent struct {
	ChartType   string            `json:"chart_type"` // bar, line, pie, doughnut
	DataSource  string            `json:"data_source"`
	XField      string            `json:"x_field"`
	YField      string            `json:"y_field"`
	GroupField  string            `json:"group_field,omitempty"`
	Aggregation string            `json:"aggregation"` // count, sum, avg
	Colors      []string          `json:"colors"`
	ShowLegend  bool              `json:"show_legend"`
	ShowLabels  bool              `json:"show_labels"`
	Title       string            `json:"title,omitempty"`
	Filters     []ReportFilterConfig `json:"filters"`
}

// Request/Response types

type ReportTemplateCreateRequest struct {
	Name        string         `json:"name" validate:"required,max=255"`
	Description string         `json:"description"`
	Template    TemplateConfig `json:"template" validate:"required"`
	IsPublic    bool           `json:"is_public"`
}

type ReportTemplateUpdateRequest struct {
	Name        string          `json:"name" validate:"omitempty,max=255"`
	Description string          `json:"description"`
	Template    *TemplateConfig `json:"template"`
	IsPublic    *bool           `json:"is_public"`
	IsDefault   *bool           `json:"is_default"`
}

type ReportTemplateResponse struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Template    TemplateConfig      `json:"template"`
	IsDefault   bool                `json:"is_default"`
	IsPublic    bool                `json:"is_public"`
	CreatedBy   *UserBasicResponse  `json:"created_by,omitempty"`
	CreatedAt   string              `json:"created_at"`
	UpdatedAt   string              `json:"updated_at"`
}

type ReportTemplateFilter struct {
	Search      string     `json:"search"`
	IsPublic    *bool      `json:"is_public"`
	CreatedByID *uuid.UUID `json:"created_by_id"`
	Page        int        `json:"page"`
	Limit       int        `json:"limit"`
}

// GenerateReportRequest for generating a report from template
type GenerateReportRequest struct {
	TemplateID   string               `json:"template_id" validate:"required,uuid"`
	DataSource   string               `json:"data_source" validate:"required"`
	Filters      []ReportFilterConfig `json:"filters"`
	Sorting      []ReportSortConfig   `json:"sorting"`
	Format       string               `json:"format" validate:"required,oneof=pdf xlsx"`
	FileName     string               `json:"file_name"`
	// Override template values
	Overrides    *TemplateOverrides   `json:"overrides,omitempty"`
}

type TemplateOverrides struct {
	Title       string            `json:"title,omitempty"`
	Subtitle    string            `json:"subtitle,omitempty"`
	HeaderLogo  string            `json:"header_logo,omitempty"` // base64 or URL
	CustomTexts map[string]string `json:"custom_texts,omitempty"` // element_id -> text
	// Column overrides from old report config
	Columns     []ColumnOverride  `json:"columns,omitempty"`
}

// ColumnOverride represents a column configuration from old report templates
type ColumnOverride struct {
	Field     string `json:"field"`
	Label     string `json:"label"`
	Width     int    `json:"width,omitempty"`
	Alignment string `json:"alignment,omitempty"`
}

// GenerateReportResponse
type GenerateReportResponse struct {
	Success     bool   `json:"success"`
	FileName    string `json:"file_name"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	DownloadURL string `json:"download_url,omitempty"`
}
