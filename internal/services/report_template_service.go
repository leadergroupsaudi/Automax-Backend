package services

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/google/uuid"
	"github.com/jung-kurt/gofpdf"
)

type ReportTemplateService interface {
	// Template CRUD
	CreateTemplate(ctx context.Context, req *models.ReportTemplateCreateRequest, userID uuid.UUID) (*models.ReportTemplateResponse, error)
	GetTemplate(ctx context.Context, id uuid.UUID) (*models.ReportTemplateResponse, error)
	ListTemplates(ctx context.Context, filter *models.ReportTemplateFilter) ([]models.ReportTemplateResponse, int64, error)
	UpdateTemplate(ctx context.Context, id uuid.UUID, req *models.ReportTemplateUpdateRequest, userID uuid.UUID) (*models.ReportTemplateResponse, error)
	DeleteTemplate(ctx context.Context, id uuid.UUID, userID uuid.UUID) error
	DuplicateTemplate(ctx context.Context, id uuid.UUID, userID uuid.UUID) (*models.ReportTemplateResponse, error)
	SetDefaultTemplate(ctx context.Context, id uuid.UUID) error
	GetDefaultTemplate(ctx context.Context) (*models.ReportTemplateResponse, error)

	// Report Generation
	GenerateReport(ctx context.Context, req *models.GenerateReportRequest, userID uuid.UUID) ([]byte, string, string, error)
	PreviewTemplate(ctx context.Context, template *models.TemplateConfig, dataSource string, limit int) ([]byte, error)
}

type reportTemplateService struct {
	templateRepo repository.ReportTemplateRepository
	reportRepo   repository.ReportRepository
}

func NewReportTemplateService(templateRepo repository.ReportTemplateRepository, reportRepo repository.ReportRepository) ReportTemplateService {
	return &reportTemplateService{
		templateRepo: templateRepo,
		reportRepo:   reportRepo,
	}
}

// Template CRUD

func (s *reportTemplateService) CreateTemplate(ctx context.Context, req *models.ReportTemplateCreateRequest, userID uuid.UUID) (*models.ReportTemplateResponse, error) {
	templateJSON, err := json.Marshal(req.Template)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize template: %w", err)
	}

	template := &models.ReportTemplate{
		Name:        req.Name,
		Description: req.Description,
		Template:    string(templateJSON),
		IsPublic:    req.IsPublic,
		CreatedByID: userID,
	}

	if err := s.templateRepo.Create(ctx, template); err != nil {
		return nil, err
	}

	return s.GetTemplate(ctx, template.ID)
}

func (s *reportTemplateService) GetTemplate(ctx context.Context, id uuid.UUID) (*models.ReportTemplateResponse, error) {
	template, err := s.templateRepo.FindByIDWithRelations(ctx, id)
	if err != nil {
		return nil, err
	}

	return toReportTemplateResponse(template), nil
}

func (s *reportTemplateService) ListTemplates(ctx context.Context, filter *models.ReportTemplateFilter) ([]models.ReportTemplateResponse, int64, error) {
	templates, total, err := s.templateRepo.List(ctx, filter)
	if err != nil {
		return nil, 0, err
	}

	responses := make([]models.ReportTemplateResponse, len(templates))
	for i, t := range templates {
		responses[i] = *toReportTemplateResponse(&t)
	}

	return responses, total, nil
}

func (s *reportTemplateService) UpdateTemplate(ctx context.Context, id uuid.UUID, req *models.ReportTemplateUpdateRequest, userID uuid.UUID) (*models.ReportTemplateResponse, error) {
	template, err := s.templateRepo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if template.CreatedByID != userID {
		return nil, errors.New("you can only update your own templates")
	}

	if req.Name != "" {
		template.Name = req.Name
	}
	if req.Description != "" {
		template.Description = req.Description
	}
	if req.Template != nil {
		templateJSON, err := json.Marshal(req.Template)
		if err != nil {
			return nil, fmt.Errorf("failed to serialize template: %w", err)
		}
		template.Template = string(templateJSON)
	}
	if req.IsPublic != nil {
		template.IsPublic = *req.IsPublic
	}
	if req.IsDefault != nil {
		template.IsDefault = *req.IsDefault
	}

	if err := s.templateRepo.Update(ctx, template); err != nil {
		return nil, err
	}

	return s.GetTemplate(ctx, id)
}

func (s *reportTemplateService) DeleteTemplate(ctx context.Context, id uuid.UUID, userID uuid.UUID) error {
	template, err := s.templateRepo.FindByID(ctx, id)
	if err != nil {
		return err
	}

	if template.CreatedByID != userID {
		return errors.New("you can only delete your own templates")
	}

	return s.templateRepo.Delete(ctx, id)
}

func (s *reportTemplateService) DuplicateTemplate(ctx context.Context, id uuid.UUID, userID uuid.UUID) (*models.ReportTemplateResponse, error) {
	original, err := s.templateRepo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}

	duplicate := &models.ReportTemplate{
		Name:        original.Name + " (Copy)",
		Description: original.Description,
		Template:    original.Template,
		IsPublic:    false,
		IsDefault:   false,
		CreatedByID: userID,
	}

	if err := s.templateRepo.Create(ctx, duplicate); err != nil {
		return nil, err
	}

	return s.GetTemplate(ctx, duplicate.ID)
}

func (s *reportTemplateService) SetDefaultTemplate(ctx context.Context, id uuid.UUID) error {
	return s.templateRepo.SetDefault(ctx, id)
}

func (s *reportTemplateService) GetDefaultTemplate(ctx context.Context) (*models.ReportTemplateResponse, error) {
	template, err := s.templateRepo.GetDefault(ctx)
	if err != nil {
		return nil, err
	}
	return toReportTemplateResponse(template), nil
}

// Report Generation

func (s *reportTemplateService) GenerateReport(ctx context.Context, req *models.GenerateReportRequest, userID uuid.UUID) ([]byte, string, string, error) {
	// Debug logging
	fmt.Printf("[GenerateReport] Request received:\n")
	fmt.Printf("  - TemplateID: %s\n", req.TemplateID)
	fmt.Printf("  - DataSource: %s\n", req.DataSource)
	fmt.Printf("  - Format: %s\n", req.Format)
	fmt.Printf("  - Filters: %+v\n", req.Filters)
	fmt.Printf("  - Sorting: %+v\n", req.Sorting)
	if req.Overrides != nil {
		fmt.Printf("  - Overrides.Title: %s\n", req.Overrides.Title)
		fmt.Printf("  - Overrides.Columns: %d columns\n", len(req.Overrides.Columns))
		for i, col := range req.Overrides.Columns {
			fmt.Printf("    - Column[%d]: field=%s, label=%s\n", i, col.Field, col.Label)
		}
	}

	var template *models.ReportTemplate
	var err error

	// Handle "default" template or get by ID
	if req.TemplateID == "default" {
		// Try to get the default template
		template, err = s.templateRepo.GetDefault(ctx)
		if err != nil || template == nil {
			fmt.Printf("[GenerateReport] No default template found, creating basic template\n")
			// Create a basic template config on the fly
			basicTemplate := createBasicTemplate(req.DataSource, req.Overrides)
			templateJSON, _ := json.Marshal(basicTemplate)
			template = &models.ReportTemplate{
				Name:     "Generated Report",
				Template: string(templateJSON),
			}
		}
	} else {
		// Parse template ID
		templateID, parseErr := uuid.Parse(req.TemplateID)
		if parseErr != nil {
			return nil, "", "", fmt.Errorf("invalid template ID: %w", parseErr)
		}

		// Get template
		template, err = s.templateRepo.FindByID(ctx, templateID)
		if err != nil {
			return nil, "", "", fmt.Errorf("template not found: %w", err)
		}
	}

	// Parse template config
	var templateConfig models.TemplateConfig
	if err := json.Unmarshal([]byte(template.Template), &templateConfig); err != nil {
		return nil, "", "", fmt.Errorf("failed to parse template: %w", err)
	}

	// Apply overrides
	if req.Overrides != nil {
		fmt.Printf("[GenerateReport] Applying overrides...\n")
		applyTemplateOverrides(&templateConfig, req.Overrides)
	}

	// Debug: Print template elements
	fmt.Printf("[GenerateReport] Template has %d elements\n", len(templateConfig.Elements))
	for i, el := range templateConfig.Elements {
		fmt.Printf("  - Element[%d]: type=%s, id=%s\n", i, el.Type, el.ID)
		if el.Type == "table" {
			contentJSON, _ := json.Marshal(el.Content)
			var tc models.TableContent
			json.Unmarshal(contentJSON, &tc)
			fmt.Printf("    - Table has %d columns, data_source=%s\n", len(tc.Columns), tc.DataSource)
		}
	}

	// Fetch data based on data source
	var data []map[string]interface{}
	var sorting *models.ReportSortConfig
	if len(req.Sorting) > 0 {
		sorting = &req.Sorting[0]
	}

	limit := 10000 // Max records for export
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
		return nil, "", "", fmt.Errorf("failed to fetch data: %w", err)
	}

	fmt.Printf("[GenerateReport] Fetched %d records from %s\n", len(data), req.DataSource)
	if len(data) > 0 {
		fmt.Printf("[GenerateReport] First record keys: %v\n", getMapKeys(data[0]))
	}

	// Generate filename
	filename := req.FileName
	if filename == "" {
		filename = template.Name + "_" + time.Now().Format("2006-01-02_150405")
	}

	// Generate report based on format
	if req.Format == "xlsx" {
		xlsxData, err := s.generateExcelFromTemplate(&templateConfig, data)
		if err != nil {
			return nil, "", "", err
		}
		return xlsxData, filename + ".xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", nil
	}

	// Default to PDF
	pdfData, err := s.generatePDFFromTemplate(&templateConfig, data)
	if err != nil {
		return nil, "", "", err
	}
	return pdfData, filename + ".pdf", "application/pdf", nil
}

func (s *reportTemplateService) PreviewTemplate(ctx context.Context, template *models.TemplateConfig, dataSource string, limit int) ([]byte, error) {
	// Fetch sample data for preview
	var data []map[string]interface{}
	var err error

	if limit < 1 || limit > 50 {
		limit = 10
	}

	switch dataSource {
	case "incidents":
		data, _, err = s.reportRepo.ExecuteIncidentQuery(ctx, nil, nil, 1, limit)
	case "users":
		data, _, err = s.reportRepo.ExecuteUserQuery(ctx, nil, nil, 1, limit)
	case "workflows":
		data, _, err = s.reportRepo.ExecuteWorkflowQuery(ctx, nil, nil, 1, limit)
	case "departments":
		data, _, err = s.reportRepo.ExecuteDepartmentQuery(ctx, nil, nil, 1, limit)
	case "locations":
		data, _, err = s.reportRepo.ExecuteLocationQuery(ctx, nil, nil, 1, limit)
	case "classifications":
		data, _, err = s.reportRepo.ExecuteClassificationQuery(ctx, nil, nil, 1, limit)
	default:
		return nil, errors.New("unsupported data source")
	}

	if err != nil {
		return nil, err
	}

	return s.generatePDFFromTemplate(template, data)
}

// PDF Generation from Template

func (s *reportTemplateService) generatePDFFromTemplate(template *models.TemplateConfig, data []map[string]interface{}) ([]byte, error) {
	// Determine page settings
	pageSize := template.PageSettings.Size
	if pageSize == "" {
		pageSize = "A4"
	}
	orientation := "P"
	if template.PageSettings.Orientation == "landscape" {
		orientation = "L"
	}

	pdf := gofpdf.New(orientation, "mm", pageSize, "")

	// Set margins
	marginLeft := template.PageSettings.MarginLeft
	if marginLeft == 0 {
		marginLeft = 15
	}
	marginTop := template.PageSettings.MarginTop
	if marginTop == 0 {
		marginTop = 15
	}
	marginRight := template.PageSettings.MarginRight
	if marginRight == 0 {
		marginRight = 15
	}
	marginBottom := template.PageSettings.MarginBottom
	if marginBottom == 0 {
		marginBottom = 15
	}

	pdf.SetMargins(marginLeft, marginTop, marginRight)
	pdf.SetAutoPageBreak(true, marginBottom)

	// Get page dimensions
	pageWidth, pageHeight := pdf.GetPageSize()

	// Add first page
	pdf.AddPage()

	// Render header on first page
	if template.Header != nil && template.Header.Enabled {
		s.renderHeader(pdf, template.Header, pageWidth, marginLeft, marginRight)
	}

	// Render body elements
	for _, element := range template.Elements {
		if !element.Visible {
			continue
		}
		s.renderElement(pdf, &element, data, pageWidth, pageHeight, marginLeft, marginTop)
	}

	// Render footer on all pages
	if template.Footer != nil && template.Footer.Enabled {
		s.renderFooter(pdf, template.Footer, pageWidth, pageHeight, marginLeft, marginRight, marginBottom)
	}

	// Output to buffer
	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func (s *reportTemplateService) renderHeader(pdf *gofpdf.Fpdf, header *models.HeaderConfig, pageWidth, marginLeft, marginRight float64) {
	if header.Background != "" {
		r, g, b := hexToRGB(header.Background)
		pdf.SetFillColor(r, g, b)
		pdf.Rect(0, 0, pageWidth, header.Height, "F")
	}

	for _, element := range header.Elements {
		if !element.Visible {
			continue
		}
		s.renderHeaderElement(pdf, &element, pageWidth, marginLeft, marginRight, header.Height)
	}

	// Move cursor below header
	pdf.SetY(header.Height + 5)
}

func (s *reportTemplateService) renderHeaderElement(pdf *gofpdf.Fpdf, element *models.TemplateElement, pageWidth, marginLeft, marginRight, headerHeight float64) {
	x := element.Position.X + marginLeft
	y := element.Position.Y

	switch element.Type {
	case "text":
		s.renderTextElement(pdf, element, x, y)
	case "image":
		s.renderImageElement(pdf, element, x, y)
	case "dynamic_field":
		s.renderDynamicField(pdf, element, x, y)
	}
}

func (s *reportTemplateService) renderElement(pdf *gofpdf.Fpdf, element *models.TemplateElement, data []map[string]interface{}, pageWidth, pageHeight, marginLeft, marginTop float64) {
	x := element.Position.X + marginLeft
	y := element.Position.Y + marginTop

	// If position is relative, use current Y
	if element.Position.Relative {
		y = pdf.GetY() + element.Position.Y
	}

	switch element.Type {
	case "text":
		s.renderTextElement(pdf, element, x, y)
	case "image":
		s.renderImageElement(pdf, element, x, y)
	case "table":
		s.renderTableElement(pdf, element, data, x, y, pageWidth, marginLeft)
	case "line":
		s.renderLineElement(pdf, element, x, y)
	case "shape":
		s.renderShapeElement(pdf, element, x, y)
	case "spacer":
		pdf.SetY(y + element.Size.Height)
	case "dynamic_field":
		s.renderDynamicField(pdf, element, x, y)
	}
}

func (s *reportTemplateService) renderTextElement(pdf *gofpdf.Fpdf, element *models.TemplateElement, x, y float64) {
	contentJSON, _ := json.Marshal(element.Content)
	var content models.TextContent
	json.Unmarshal(contentJSON, &content)

	// Set font
	fontStyle := ""
	if content.Font.Weight == "bold" {
		fontStyle += "B"
	}
	if content.Font.Style == "italic" {
		fontStyle += "I"
	}

	fontFamily := content.Font.Family
	if fontFamily == "" {
		fontFamily = "Arial"
	}
	fontSize := content.Font.Size
	if fontSize == 0 {
		fontSize = 12
	}

	pdf.SetFont(fontFamily, fontStyle, fontSize)

	// Set color
	if content.Font.Color != "" {
		r, g, b := hexToRGB(content.Font.Color)
		pdf.SetTextColor(r, g, b)
	} else {
		pdf.SetTextColor(0, 0, 0)
	}

	// Apply background style
	if element.Style.BackgroundColor != "" {
		r, g, b := hexToRGB(element.Style.BackgroundColor)
		pdf.SetFillColor(r, g, b)
	}

	pdf.SetXY(x, y)

	// Determine alignment
	align := "L"
	switch content.Alignment {
	case "center":
		align = "C"
	case "right":
		align = "R"
	case "justify":
		align = "J"
	}

	width := element.Size.Width
	if width == 0 {
		width = 0 // Auto width
	}

	if content.WordWrap && width > 0 {
		pdf.MultiCell(width, content.Font.LineHeight, content.Text, "", align, element.Style.BackgroundColor != "")
	} else {
		pdf.CellFormat(width, fontSize*0.35, content.Text, "", 0, align, element.Style.BackgroundColor != "", 0, "")
	}
}

func (s *reportTemplateService) renderImageElement(pdf *gofpdf.Fpdf, element *models.TemplateElement, x, y float64) {
	contentJSON, _ := json.Marshal(element.Content)
	var content models.ImageContent
	json.Unmarshal(contentJSON, &content)

	if content.Source == "" {
		return
	}

	var imageReader io.Reader
	var imageType string

	if content.SourceType == "base64" {
		// Decode base64 image
		// Extract image type from data URI if present
		source := content.Source
		if strings.HasPrefix(source, "data:image/") {
			parts := strings.SplitN(source, ",", 2)
			if len(parts) == 2 {
				// Extract type from "data:image/png;base64"
				typePart := strings.TrimPrefix(parts[0], "data:image/")
				imageType = strings.Split(typePart, ";")[0]
				source = parts[1]
			}
		}

		decoded, err := base64.StdEncoding.DecodeString(source)
		if err != nil {
			return
		}
		imageReader = bytes.NewReader(decoded)
	} else {
		// Fetch from URL
		resp, err := http.Get(content.Source)
		if err != nil {
			return
		}
		defer resp.Body.Close()

		imageData, err := io.ReadAll(resp.Body)
		if err != nil {
			return
		}
		imageReader = bytes.NewReader(imageData)

		// Determine type from URL or content-type
		contentType := resp.Header.Get("Content-Type")
		if strings.Contains(contentType, "png") {
			imageType = "PNG"
		} else if strings.Contains(contentType, "jpeg") || strings.Contains(contentType, "jpg") {
			imageType = "JPEG"
		} else if strings.Contains(contentType, "gif") {
			imageType = "GIF"
		}
	}

	if imageType == "" {
		imageType = "PNG" // Default
	}

	// Register and place image
	imageName := element.ID
	pdf.RegisterImageOptionsReader(imageName, gofpdf.ImageOptions{ImageType: imageType}, imageReader)

	width := element.Size.Width
	height := element.Size.Height

	pdf.ImageOptions(imageName, x, y, width, height, false, gofpdf.ImageOptions{}, 0, "")
}

func (s *reportTemplateService) renderTableElement(pdf *gofpdf.Fpdf, element *models.TemplateElement, data []map[string]interface{}, x, y, pageWidth, marginLeft float64) {
	contentJSON, _ := json.Marshal(element.Content)
	var content models.TableContent
	json.Unmarshal(contentJSON, &content)

	fmt.Printf("[renderTableElement] Rendering table with %d columns and %d data rows\n", len(content.Columns), len(data))
	for i, col := range content.Columns {
		fmt.Printf("  - Column[%d]: field=%s, label=%s\n", i, col.Field, col.Label)
	}
	if len(data) > 0 {
		fmt.Printf("  - First row keys: %v\n", getMapKeys(data[0]))
	}

	if len(content.Columns) == 0 {
		fmt.Printf("[renderTableElement] WARNING: No columns defined!\n")
		return
	}

	pdf.SetXY(x, y)

	// Calculate available width
	availableWidth := pageWidth - marginLeft*2
	if element.Size.Width > 0 {
		availableWidth = element.Size.Width
	}

	// Calculate column widths
	totalDefinedWidth := 0.0
	autoWidthCols := 0
	for _, col := range content.Columns {
		if col.Width > 0 {
			if col.WidthUnit == "percent" {
				totalDefinedWidth += availableWidth * col.Width / 100
			} else {
				totalDefinedWidth += col.Width
			}
		} else {
			autoWidthCols++
		}
	}

	autoWidth := 0.0
	if autoWidthCols > 0 {
		autoWidth = (availableWidth - totalDefinedWidth) / float64(autoWidthCols)
	}

	colWidths := make([]float64, len(content.Columns))
	for i, col := range content.Columns {
		if col.Width > 0 {
			if col.WidthUnit == "percent" {
				colWidths[i] = availableWidth * col.Width / 100
			} else {
				colWidths[i] = col.Width
			}
		} else {
			colWidths[i] = autoWidth
		}
	}

	rowHeight := 8.0

	// Render header
	if content.ShowHeader {
		// Apply header style
		if content.HeaderStyle.BackgroundColor != "" {
			r, g, b := hexToRGB(content.HeaderStyle.BackgroundColor)
			pdf.SetFillColor(r, g, b)
		} else {
			pdf.SetFillColor(59, 130, 246) // Default blue
		}

		if content.HeaderStyle.TextColor != "" {
			r, g, b := hexToRGB(content.HeaderStyle.TextColor)
			pdf.SetTextColor(r, g, b)
		} else {
			pdf.SetTextColor(255, 255, 255) // White text
		}

		fontSize := content.HeaderStyle.Font.Size
		if fontSize == 0 {
			fontSize = 10
		}
		pdf.SetFont("Arial", "B", fontSize)

		for i, col := range content.Columns {
			align := "C"
			if col.Alignment != "" {
				align = strings.ToUpper(col.Alignment[:1])
			}
			label := col.Label
			if len(label) > 20 {
				label = label[:17] + "..."
			}
			pdf.CellFormat(colWidths[i], rowHeight, label, "1", 0, align, true, 0, "")
		}
		pdf.Ln(-1)
	}

	// Render data rows
	if content.RowStyle.TextColor != "" {
		r, g, b := hexToRGB(content.RowStyle.TextColor)
		pdf.SetTextColor(r, g, b)
	} else {
		pdf.SetTextColor(0, 0, 0)
	}

	fontSize := content.RowStyle.Font.Size
	if fontSize == 0 {
		fontSize = 9
	}
	pdf.SetFont("Arial", "", fontSize)

	fill := false
	rowBgColor := [3]int{255, 255, 255}
	altRowBgColor := [3]int{245, 245, 245}

	if content.RowStyle.BackgroundColor != "" {
		rowBgColor[0], rowBgColor[1], rowBgColor[2] = hexToRGB(content.RowStyle.BackgroundColor)
	}
	if content.AltRowStyle.BackgroundColor != "" {
		altRowBgColor[0], altRowBgColor[1], altRowBgColor[2] = hexToRGB(content.AltRowStyle.BackgroundColor)
	}

	maxRows := len(data)
	if content.MaxRows > 0 && content.MaxRows < maxRows {
		maxRows = content.MaxRows
	}

	for rowIdx := 0; rowIdx < maxRows; rowIdx++ {
		row := data[rowIdx]

		// Check for page break
		if pdf.GetY() > 260 { // Near bottom of page
			pdf.AddPage()
			pdf.SetY(15)

			// Re-render header on new page
			if content.ShowHeader {
				if content.HeaderStyle.BackgroundColor != "" {
					r, g, b := hexToRGB(content.HeaderStyle.BackgroundColor)
					pdf.SetFillColor(r, g, b)
				} else {
					pdf.SetFillColor(59, 130, 246)
				}
				if content.HeaderStyle.TextColor != "" {
					r, g, b := hexToRGB(content.HeaderStyle.TextColor)
					pdf.SetTextColor(r, g, b)
				} else {
					pdf.SetTextColor(255, 255, 255)
				}
				headerFontSize := content.HeaderStyle.Font.Size
				if headerFontSize == 0 {
					headerFontSize = 10
				}
				pdf.SetFont("Arial", "B", headerFontSize)

				for i, col := range content.Columns {
					align := "C"
					if col.Alignment != "" {
						align = strings.ToUpper(col.Alignment[:1])
					}
					label := col.Label
					if len(label) > 20 {
						label = label[:17] + "..."
					}
					pdf.CellFormat(colWidths[i], rowHeight, label, "1", 0, align, true, 0, "")
				}
				pdf.Ln(-1)

				if content.RowStyle.TextColor != "" {
					r, g, b := hexToRGB(content.RowStyle.TextColor)
					pdf.SetTextColor(r, g, b)
				} else {
					pdf.SetTextColor(0, 0, 0)
				}
				pdf.SetFont("Arial", "", fontSize)
			}
		}

		// Set row background
		if content.AlternateRows && fill {
			pdf.SetFillColor(altRowBgColor[0], altRowBgColor[1], altRowBgColor[2])
		} else {
			pdf.SetFillColor(rowBgColor[0], rowBgColor[1], rowBgColor[2])
		}

		for i, col := range content.Columns {
			val := ""
			// Use getNestedValue to handle nested fields like "current_state.name"
			v := getNestedValue(row, col.Field)
			if v != nil {
				val = formatCellValue(col.Field, col.Format, v)
			}

			// Truncate if too long
			maxLen := int(colWidths[i] / 2)
			if maxLen < 10 {
				maxLen = 10
			}
			if len(val) > maxLen {
				val = val[:maxLen-3] + "..."
			}

			align := "L"
			if col.Alignment != "" {
				align = strings.ToUpper(col.Alignment[:1])
			}

			pdf.CellFormat(colWidths[i], rowHeight, val, "1", 0, align, content.AlternateRows, 0, "")
		}
		pdf.Ln(-1)

		if content.AlternateRows {
			fill = !fill
		}
	}

	// Total count
	pdf.Ln(3)
	pdf.SetFont("Arial", "I", 9)
	pdf.SetTextColor(128, 128, 128)
	pdf.CellFormat(0, 6, fmt.Sprintf("Total: %d records", len(data)), "", 1, "L", false, 0, "")
	pdf.SetTextColor(0, 0, 0)
}

func (s *reportTemplateService) renderLineElement(pdf *gofpdf.Fpdf, element *models.TemplateElement, x, y float64) {
	contentJSON, _ := json.Marshal(element.Content)
	var content models.LineContent
	json.Unmarshal(contentJSON, &content)

	if content.StrokeColor != "" {
		r, g, b := hexToRGB(content.StrokeColor)
		pdf.SetDrawColor(r, g, b)
	}

	if content.StrokeWidth > 0 {
		pdf.SetLineWidth(content.StrokeWidth)
	}

	startX := x + content.StartX
	startY := y + content.StartY
	endX := x + content.EndX
	endY := y + content.EndY

	pdf.Line(startX, startY, endX, endY)
}

func (s *reportTemplateService) renderShapeElement(pdf *gofpdf.Fpdf, element *models.TemplateElement, x, y float64) {
	contentJSON, _ := json.Marshal(element.Content)
	var content models.ShapeContent
	json.Unmarshal(contentJSON, &content)

	drawStyle := ""
	if content.FillColor != "" {
		r, g, b := hexToRGB(content.FillColor)
		pdf.SetFillColor(r, g, b)
		drawStyle = "F"
	}
	if content.StrokeColor != "" {
		r, g, b := hexToRGB(content.StrokeColor)
		pdf.SetDrawColor(r, g, b)
		if drawStyle == "F" {
			drawStyle = "FD"
		} else {
			drawStyle = "D"
		}
	}
	if content.StrokeWidth > 0 {
		pdf.SetLineWidth(content.StrokeWidth)
	}

	switch content.ShapeType {
	case "rectangle":
		pdf.Rect(x, y, element.Size.Width, element.Size.Height, drawStyle)
	case "circle":
		radius := element.Size.Width / 2
		pdf.Circle(x+radius, y+radius, radius, drawStyle)
	case "ellipse":
		pdf.Ellipse(x+element.Size.Width/2, y+element.Size.Height/2, element.Size.Width/2, element.Size.Height/2, 0, drawStyle)
	}
}

func (s *reportTemplateService) renderDynamicField(pdf *gofpdf.Fpdf, element *models.TemplateElement, x, y float64) {
	contentJSON, _ := json.Marshal(element.Content)
	var content models.DynamicFieldContent
	json.Unmarshal(contentJSON, &content)

	// Set font
	fontStyle := ""
	if content.Font.Weight == "bold" {
		fontStyle += "B"
	}
	if content.Font.Style == "italic" {
		fontStyle += "I"
	}

	fontFamily := content.Font.Family
	if fontFamily == "" {
		fontFamily = "Arial"
	}
	fontSize := content.Font.Size
	if fontSize == 0 {
		fontSize = 10
	}

	pdf.SetFont(fontFamily, fontStyle, fontSize)

	if content.Font.Color != "" {
		r, g, b := hexToRGB(content.Font.Color)
		pdf.SetTextColor(r, g, b)
	}

	var value string
	switch content.Field {
	case "date":
		format := content.Format
		if format == "" {
			format = "2006-01-02"
		}
		value = time.Now().Format(format)
	case "datetime":
		format := content.Format
		if format == "" {
			format = "2006-01-02 15:04:05"
		}
		value = time.Now().Format(format)
	case "page_number":
		value = fmt.Sprintf("%d", pdf.PageNo())
	case "custom":
		value = content.CustomValue
	default:
		value = content.Field
	}

	text := content.Prefix + value + content.Suffix

	pdf.SetXY(x, y)

	align := "L"
	switch content.Alignment {
	case "center":
		align = "C"
	case "right":
		align = "R"
	}

	pdf.CellFormat(element.Size.Width, fontSize*0.35, text, "", 0, align, false, 0, "")
}

func (s *reportTemplateService) renderFooter(pdf *gofpdf.Fpdf, footer *models.FooterConfig, pageWidth, pageHeight, marginLeft, marginRight, marginBottom float64) {
	// Footer is rendered at bottom of page
	footerY := pageHeight - marginBottom - footer.Height

	if footer.Background != "" {
		r, g, b := hexToRGB(footer.Background)
		pdf.SetFillColor(r, g, b)
		pdf.Rect(0, footerY, pageWidth, footer.Height, "F")
	}

	for _, element := range footer.Elements {
		if !element.Visible {
			continue
		}

		x := element.Position.X + marginLeft
		y := footerY + element.Position.Y

		switch element.Type {
		case "text":
			s.renderTextElement(pdf, &element, x, y)
		case "dynamic_field":
			s.renderDynamicField(pdf, &element, x, y)
		}
	}

	// Page numbers
	if footer.ShowPageNumber {
		pdf.SetFont("Arial", "", 9)
		pdf.SetTextColor(128, 128, 128)
		pageNum := fmt.Sprintf("Page %d", pdf.PageNo())
		if footer.PageNumberFormat != "" {
			pageNum = strings.ReplaceAll(footer.PageNumberFormat, "{page}", strconv.Itoa(pdf.PageNo()))
		}
		pdf.SetXY(pageWidth/2-20, footerY+footer.Height/2-3)
		pdf.CellFormat(40, 6, pageNum, "", 0, "C", false, 0, "")
	}
}

func (s *reportTemplateService) generateExcelFromTemplate(template *models.TemplateConfig, data []map[string]interface{}) ([]byte, error) {
	// Find table element in template
	var tableContent *models.TableContent
	for _, element := range template.Elements {
		if element.Type == "table" {
			contentJSON, _ := json.Marshal(element.Content)
			var content models.TableContent
			json.Unmarshal(contentJSON, &content)
			tableContent = &content
			break
		}
	}

	if tableContent == nil || len(tableContent.Columns) == 0 {
		return nil, errors.New("no table element found in template")
	}

	var buf bytes.Buffer

	// BOM for Excel UTF-8
	buf.Write([]byte{0xEF, 0xBB, 0xBF})

	// Header row
	for i, col := range tableContent.Columns {
		if i > 0 {
			buf.WriteString("\t")
		}
		buf.WriteString(col.Label)
	}
	buf.WriteString("\n")

	// Data rows
	for _, row := range data {
		for i, col := range tableContent.Columns {
			if i > 0 {
				buf.WriteString("\t")
			}
			if val, ok := row[col.Field]; ok && val != nil {
				buf.WriteString(formatCellValue(col.Field, col.Format, val))
			}
		}
		buf.WriteString("\n")
	}

	return buf.Bytes(), nil
}

// Helper functions

func applyTemplateOverrides(template *models.TemplateConfig, overrides *models.TemplateOverrides) {
	if template.Header == nil {
		return
	}

	for i, element := range template.Header.Elements {
		if element.Type == "text" {
			contentJSON, _ := json.Marshal(element.Content)
			var content models.TextContent
			json.Unmarshal(contentJSON, &content)

			// Check if this element should be overridden
			if overrides.Title != "" && strings.Contains(strings.ToLower(element.ID), "title") {
				content.Text = overrides.Title
				template.Header.Elements[i].Content = content
			}
			if overrides.Subtitle != "" && strings.Contains(strings.ToLower(element.ID), "subtitle") {
				content.Text = overrides.Subtitle
				template.Header.Elements[i].Content = content
			}
		}
		if element.Type == "image" && overrides.HeaderLogo != "" {
			contentJSON, _ := json.Marshal(element.Content)
			var content models.ImageContent
			json.Unmarshal(contentJSON, &content)
			content.Source = overrides.HeaderLogo
			content.SourceType = "base64"
			template.Header.Elements[i].Content = content
		}
	}

	// Apply custom text overrides
	if overrides.CustomTexts != nil {
		for i, element := range template.Elements {
			if text, ok := overrides.CustomTexts[element.ID]; ok && element.Type == "text" {
				contentJSON, _ := json.Marshal(element.Content)
				var content models.TextContent
				json.Unmarshal(contentJSON, &content)
				content.Text = text
				template.Elements[i].Content = content
			}
		}
	}

	// Apply column overrides to table elements
	if len(overrides.Columns) > 0 {
		fmt.Printf("[applyTemplateOverrides] Applying %d column overrides to table\n", len(overrides.Columns))
		tableFound := false
		for i, element := range template.Elements {
			fmt.Printf("[applyTemplateOverrides] Checking element[%d]: type=%s\n", i, element.Type)
			if element.Type == "table" {
				tableFound = true
				contentJSON, _ := json.Marshal(element.Content)
				var content models.TableContent
				json.Unmarshal(contentJSON, &content)

				fmt.Printf("[applyTemplateOverrides] Found table element, original columns: %d\n", len(content.Columns))

				// Replace table columns with override columns
				newColumns := make([]models.TableColumn, len(overrides.Columns))
				for j, col := range overrides.Columns {
					alignment := col.Alignment
					if alignment == "" {
						alignment = "left"
					}
					newColumns[j] = models.TableColumn{
						Field:     col.Field,
						Label:     col.Label,
						Width:     float64(col.Width),
						WidthUnit: "percent",
						Alignment: alignment,
					}
				}
				content.Columns = newColumns
				template.Elements[i].Content = content
				fmt.Printf("[applyTemplateOverrides] Updated table with %d columns\n", len(content.Columns))
				break // Only apply to first table
			}
		}
		if !tableFound {
			fmt.Printf("[applyTemplateOverrides] No table element found, creating one with %d columns\n", len(overrides.Columns))
			// Create a new table element with the override columns
			newColumns := make([]models.TableColumn, len(overrides.Columns))
			for j, col := range overrides.Columns {
				alignment := col.Alignment
				if alignment == "" {
					alignment = "left"
				}
				newColumns[j] = models.TableColumn{
					Field:     col.Field,
					Label:     col.Label,
					Width:     float64(col.Width),
					WidthUnit: "percent",
					Alignment: alignment,
				}
			}

			tableElement := models.TemplateElement{
				ID:   "auto_generated_table",
				Type: "table",
				Position: models.ElementPosition{
					X:        0,
					Y:        10,
					Relative: true,
				},
				Size: models.ElementSize{
					Width: 180,
				},
				Visible: true,
				Content: models.TableContent{
					Columns:       newColumns,
					ShowHeader:    true,
					AlternateRows: true,
					HeaderStyle: models.TableCellStyle{
						BackgroundColor: "#2563eb",
						TextColor:       "#ffffff",
						Font: models.FontConfig{
							Size:   10,
							Weight: "bold",
						},
						Padding: 4,
					},
					RowStyle: models.TableCellStyle{
						BackgroundColor: "#ffffff",
						TextColor:       "#000000",
						Font: models.FontConfig{
							Size: 9,
						},
						Padding: 3,
					},
					AltRowStyle: models.TableCellStyle{
						BackgroundColor: "#f3f4f6",
						TextColor:       "#000000",
						Font: models.FontConfig{
							Size: 9,
						},
						Padding: 3,
					},
				},
			}
			template.Elements = append(template.Elements, tableElement)
			fmt.Printf("[applyTemplateOverrides] Added new table element with %d columns\n", len(newColumns))
		}
	}
}

// getMapKeys returns all keys from a map (for debugging)
func getMapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// getNestedValue retrieves a value from a map, handling both flat keys with dots and nested objects
func getNestedValue(data map[string]interface{}, field string) interface{} {
	// First, try direct key lookup (for flat keys like "current_state.name")
	if val, ok := data[field]; ok {
		return val
	}

	// If not found, try nested lookup (for actual nested objects)
	parts := strings.Split(field, ".")
	if len(parts) == 1 {
		return nil // Already tried direct lookup
	}

	var current interface{} = data
	for _, part := range parts {
		if current == nil {
			return nil
		}

		if m, ok := current.(map[string]interface{}); ok {
			current = m[part]
		} else {
			return nil
		}
	}

	return current
}

func hexToRGB(hex string) (int, int, int) {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) == 3 {
		hex = string(hex[0]) + string(hex[0]) + string(hex[1]) + string(hex[1]) + string(hex[2]) + string(hex[2])
	}
	if len(hex) != 6 {
		return 0, 0, 0
	}

	r, _ := strconv.ParseInt(hex[0:2], 16, 64)
	g, _ := strconv.ParseInt(hex[2:4], 16, 64)
	b, _ := strconv.ParseInt(hex[4:6], 16, 64)

	return int(r), int(g), int(b)
}

func formatCellValue(field, format string, v interface{}) string {
	if v == nil {
		return ""
	}

	// Handle specific field formats
	switch field {
	case "priority":
		if num, ok := toInt(v); ok {
			return getPriorityLabel(num)
		}
	case "severity":
		if num, ok := toInt(v); ok {
			return getSeverityLabel(num)
		}
	}

	// Handle format specifiers
	switch format {
	case "date":
		if t, ok := v.(time.Time); ok {
			return t.Format("2006-01-02")
		}
		if s, ok := v.(string); ok {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				return t.Format("2006-01-02")
			}
		}
	case "datetime":
		if t, ok := v.(time.Time); ok {
			return t.Format("2006-01-02 15:04")
		}
		if s, ok := v.(string); ok {
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				return t.Format("2006-01-02 15:04")
			}
		}
	case "boolean":
		if b, ok := v.(bool); ok {
			if b {
				return "Yes"
			}
			return "No"
		}
	}

	// Default formatting
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
	default:
		return fmt.Sprintf("%v", val)
	}
}

func toInt(v interface{}) (int, bool) {
	switch val := v.(type) {
	case int:
		return val, true
	case int64:
		return int(val), true
	case float64:
		return int(val), true
	}
	return 0, false
}

func toReportTemplateResponse(t *models.ReportTemplate) *models.ReportTemplateResponse {
	var templateConfig models.TemplateConfig
	json.Unmarshal([]byte(t.Template), &templateConfig)

	resp := &models.ReportTemplateResponse{
		ID:          t.ID.String(),
		Name:        t.Name,
		Description: t.Description,
		Template:    templateConfig,
		IsDefault:   t.IsDefault,
		IsPublic:    t.IsPublic,
		CreatedAt:   t.CreatedAt.Format(time.RFC3339),
		UpdatedAt:   t.UpdatedAt.Format(time.RFC3339),
	}

	if t.CreatedBy != nil {
		resp.CreatedBy = &models.UserBasicResponse{
			ID:        t.CreatedBy.ID.String(),
			Email:     t.CreatedBy.Email,
			Username:  t.CreatedBy.Username,
			FirstName: t.CreatedBy.FirstName,
			LastName:  t.CreatedBy.LastName,
			Avatar:    t.CreatedBy.Avatar,
		}
	}

	return resp
}

// createBasicTemplate creates a basic template configuration when no template is selected
func createBasicTemplate(dataSource string, overrides *models.TemplateOverrides) *models.TemplateConfig {
	title := "Report"
	if overrides != nil && overrides.Title != "" {
		title = overrides.Title
	}

	// Build columns from overrides or use defaults
	var columns []models.TableColumn
	if overrides != nil && len(overrides.Columns) > 0 {
		columns = make([]models.TableColumn, len(overrides.Columns))
		for i, col := range overrides.Columns {
			alignment := col.Alignment
			if alignment == "" {
				alignment = "left"
			}
			columns[i] = models.TableColumn{
				Field:     col.Field,
				Label:     col.Label,
				Width:     float64(col.Width),
				WidthUnit: "percent",
				Alignment: alignment,
			}
		}
	} else {
		// Default columns based on data source
		switch dataSource {
		case "incidents":
			columns = []models.TableColumn{
				{Field: "reference_number", Label: "Reference", Alignment: "left"},
				{Field: "title", Label: "Title", Alignment: "left"},
				{Field: "status", Label: "Status", Alignment: "center"},
				{Field: "priority", Label: "Priority", Alignment: "center"},
				{Field: "created_at", Label: "Created", Alignment: "left"},
			}
		case "users":
			columns = []models.TableColumn{
				{Field: "username", Label: "Username", Alignment: "left"},
				{Field: "email", Label: "Email", Alignment: "left"},
				{Field: "first_name", Label: "First Name", Alignment: "left"},
				{Field: "last_name", Label: "Last Name", Alignment: "left"},
			}
		default:
			columns = []models.TableColumn{
				{Field: "id", Label: "ID", Alignment: "left"},
				{Field: "name", Label: "Name", Alignment: "left"},
			}
		}
	}

	return &models.TemplateConfig{
		PageSettings: models.PageSettings{
			Size:         "A4",
			Orientation:  "portrait",
			MarginTop:    15,
			MarginBottom: 15,
			MarginLeft:   15,
			MarginRight:  15,
		},
		Header: &models.HeaderConfig{
			Enabled:        true,
			Height:         25,
			ShowOnAllPages: true,
			Elements: []models.TemplateElement{
				{
					ID:   "header_title",
					Type: "text",
					Position: models.ElementPosition{
						X: 10,
						Y: 10,
					},
					Size: models.ElementSize{
						Width:  180,
						Height: 15,
					},
					Content: models.TextContent{
						Text: title,
						Font: models.FontConfig{
							Family: "Arial",
							Size:   16,
							Weight: "bold",
							Color:  "#000000",
						},
						Alignment: "center",
					},
				},
			},
		},
		Footer: &models.FooterConfig{
			Enabled:          true,
			Height:           15,
			ShowPageNumber:   true,
			PageNumberFormat: "Page {page} of {total}",
			ShowOnAllPages:   true,
		},
		Elements: []models.TemplateElement{
			{
				ID:   "main_table",
				Type: "table",
				Position: models.ElementPosition{
					X:        0,
					Y:        0,
					Relative: true,
				},
				Size: models.ElementSize{
					Width: 180,
				},
				Content: models.TableContent{
					DataSource:    dataSource,
					Columns:       columns,
					ShowHeader:    true,
					AlternateRows: true,
					HeaderStyle: models.TableCellStyle{
						BackgroundColor: "#2563eb",
						TextColor:       "#ffffff",
						Font: models.FontConfig{
							Size:   10,
							Weight: "bold",
						},
						Padding: 4,
					},
					RowStyle: models.TableCellStyle{
						BackgroundColor: "#ffffff",
						TextColor:       "#000000",
						Font: models.FontConfig{
							Size: 9,
						},
						Padding: 3,
					},
					AltRowStyle: models.TableCellStyle{
						BackgroundColor: "#f3f4f6",
						TextColor:       "#000000",
						Font: models.FontConfig{
							Size: 9,
						},
						Padding: 3,
					},
				},
			},
		},
	}
}
