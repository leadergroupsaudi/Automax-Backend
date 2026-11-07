package services

// KPI Metrics bulk import — validates and commits KpiMetric rows under existing
// KPIs. Rejected rows are reported with their Excel row and reason.

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/utils"
	"github.com/google/uuid"
	"github.com/xuri/excelize/v2"
	"gorm.io/gorm"
)

const (
	KpiMetricsTemplateFileName = "kpi-metrics-import-template.xlsx"

	kpiMetricsTemplateSheet       = "KPI Metrics"
	kpiMetricsInstructionsSheet   = "Instructions"
	kpiMetricsCategory            = "metric"
	kpiMetricsTemplateDataRows    = 4999
	kpiMetricsWorksheetHeaderScan = 15
)

type kpiMetricIssue struct {
	field   string
	message string
}

type kpiMetricRec struct {
	Row      int
	KpiCode  string
	KpiType  string
	KpiID    uuid.UUID
	KpiName  string
	Metric   *models.KpiMetric
	OwnerRaw string
	Set      map[string]bool

	ExistingID             *uuid.UUID
	ExistingWeight         float64
	ExistingActive         bool
	ExistingBaseline       float64
	ExistingBaselinePeriod string

	Issues    []kpiMetricIssue
	Rejected  bool
	RejectWhy string
	action    string
}

func (r *kpiMetricRec) reject(field, message string) {
	r.Rejected = true
	r.Issues = append(r.Issues, kpiMetricIssue{field: field, message: message})
}

func (r *kpiMetricRec) reason() string {
	parts := make([]string, 0, len(r.Issues))
	for _, is := range r.Issues {
		parts = append(parts, is.message)
	}
	return strings.Join(parts, " ")
}

func (r *kpiMetricRec) recordID() string {
	if r.Metric != nil {
		if strings.TrimSpace(r.Metric.MetricCode) != "" {
			return strings.TrimSpace(r.Metric.MetricCode)
		}
		if strings.TrimSpace(r.Metric.Name) != "" {
			return strings.TrimSpace(r.Metric.Name)
		}
	}
	return r.KpiCode
}

type kpiMetricTable struct {
	sheet     string
	headerRow int
	cols      map[string]int
	labels    map[string]string
}

type kpiMetricsImporter struct {
	db     *gorm.DB
	ctx    context.Context
	userID uuid.UUID
	opts   KpiImportOptions
	res    *KpiImportResult

	structuralError bool
	sheet           string
	recs            []*kpiMetricRec

	existingKpis map[string]*existingKpi // type|CODE
	kpiByCode    map[string]string       // CODE -> type
	metricsByKpi map[string][]*models.KpiMetric
	metricByCode map[string]*models.KpiMetric // type|kpiID|lower(code)
	metricByName map[string]*models.KpiMetric // type|kpiID|lower(name)
	usersByLogin map[string]uuid.UUID
	usersByID    map[uuid.UUID]bool
	kpiLocked    map[string]bool // type|kpiID
}

// ─── Entry points ───────────────────────────────────────────────────────────

// ValidateKpiMetricsImport validates the workbook and returns a preview without writing.
func ValidateKpiMetricsImport(ctx context.Context, db *gorm.DB, data []byte, fileName string, userID uuid.UUID, opts KpiImportOptions) (*KpiImportResult, error) {
	imp, err := buildKpiMetricsPlan(ctx, db, data, fileName, userID, opts)
	if err != nil {
		return nil, err
	}
	return imp.res, nil
}

// CommitKpiMetricsImport re-validates and writes all valid metrics in one transaction.
func CommitKpiMetricsImport(ctx context.Context, db *gorm.DB, data []byte, fileName string, userID uuid.UUID, opts KpiImportOptions) (*KpiImportResult, error) {
	imp, err := buildKpiMetricsPlan(ctx, db, data, fileName, userID, opts)
	if err != nil {
		return nil, err
	}
	if !imp.res.CanCommit {
		return imp.res, nil
	}
	if err := db.WithContext(ctx).Transaction(imp.commit); err != nil {
		imp.res.CommitError = err.Error()
		imp.markRolledBack()
		return imp.res, nil
	}
	imp.res.Committed = true
	return imp.res, nil
}

func buildKpiMetricsPlan(ctx context.Context, db *gorm.DB, data []byte, fileName string, userID uuid.UUID, opts KpiImportOptions) (*kpiMetricsImporter, error) {
	imp := &kpiMetricsImporter{
		db:     db,
		ctx:    ctx,
		userID: userID,
		res: &KpiImportResult{
			FileName: fileName,
			Errors:   []KpiImportIssue{},
			Warnings: []KpiImportIssue{},
			Items:    []KpiImportItem{},
		},
	}
	imp.opts = opts
	imp.opts.normalize()
	imp.res.Options = imp.opts

	if err := imp.parseWorkbook(data); err != nil {
		return nil, err
	}
	if !imp.structuralError {
		if err := imp.loadExisting(); err != nil {
			return nil, err
		}
		imp.validateRows()
	}
	imp.finalize()
	return imp, nil
}

// ─── Template ───────────────────────────────────────────────────────────────

func KpiMetricsTemplateFileNameFor(lang string) string {
	if lang == KpiTemplateLangAR {
		return "kpi-metrics-import-template-ar.xlsx"
	}
	return KpiMetricsTemplateFileName
}

// BuildKpiMetricsImportTemplate returns the blank workbook for the given header language.
func BuildKpiMetricsImportTemplate(lang string) ([]byte, error) {
	language := utils.LanguageEnglish
	if lang == KpiTemplateLangAR {
		language = utils.LanguageArabic
	}
	cfg := utils.WorkbookConfig{
		Language:     language,
		Title:        "KPI Metrics Import Template",
		Subject:      "Bulk import of KPI metrics",
		Creator:      "Automax",
		ActiveSheet:  kpiMetricsTemplateSheet,
		Instructions: buildKpiMetricsInstructions(),
		Sheets: []utils.SheetConfig{{
			Name:         kpiMetricsTemplateSheet,
			Columns:      toExcelColumns(markKpiMetricTemplateRequired(kpiMetricTemplateColumns())),
			DataRows:     kpiMetricsTemplateDataRows,
			FreezeHeader: true,
		}},
	}
	return utils.GenerateExcelTemplate(cfg)
}

// kpiMetricColumns is the shared header spec for parsing and template markers.
func kpiMetricColumns() []colSpec {
	return []colSpec{
		{"kpi_code", hdrAny("kpi code", "كود المؤشر"), true},
		{"kpi_type", hdrAny("kpi type", "نوع المؤشر"), true},
		{"metric_code", hdrAny("metric code", "كود المقياس"), false},
		{"metric_name", hdrAny("metric name", "اسم المقياس"), true},
		{"metric_description", hdrAny("metric description", "وصف المقياس"), false},
		{"metric_status", hdrAny("status", "metric status", "الحالة", "حالة المقياس"), false},
		{"display_order", hdrPrefixAny("display order", "ترتيب العرض"), false},
		{"metric_type", hdrAny("metric type", "نوع المقياس"), false},
		{"unit", hdrAny("unit", "الوحدة"), false},
		{"custom_unit_label", hdrAny("custom unit label", "تسمية الوحدة المخصصة"), false},
		{"baseline_value", hdrPrefixAny("baseline value", "قيمة خط الأساس"), false},
		{"baseline_period", hdrPrefixAny("baseline period", "فترة خط الأساس"), false},
		{"weight", hdrPrefixAny("weight", "الوزن"), false},
		{"calculation_type", hdrAny("calculation type", "نوع الحساب"), false},
		{"direction", hdrAny("direction", "الاتجاه"), false},
		{"decimal_precision", hdrPrefixAny("decimal precision", "الدقة العشرية"), false},
		{"aggregation_method", hdrAny("aggregation method", "طريقة التجميع"), false},
		{"reporting_frequency", hdrAny("reporting frequency", "تكرار الإبلاغ"), false},
		{"numerator_label", hdrAny("numerator label", "تسمية البسط"), false},
		{"numerator_variable_code", hdrAny("numerator variable code", "رمز متغير البسط"), false},
		{"denominator_label", hdrAny("denominator label", "تسمية المقام"), false},
		{"denominator_variable_code", hdrAny("denominator variable code", "رمز متغير المقام"), false},
		{"direct_actual_label", hdrAny("direct actual label", "تسمية القيمة الفعلية المباشرة"), false},
		{"allow_manual_actual_override", hdrPrefixAny("allow manual", "السماح بالتجاوز"), false},
		{"advanced_formula_enabled", hdrPrefixAny("advanced formula", "تفعيل المعادلة"), false},
		{"formula_code", hdrAny("formula code", "رمز المعادلة"), false},
		{"divide_by_zero_handling", hdrPrefixAny("divide by zero", "معالجة القسمة"), false},
		{"rounding_rule", hdrAny("rounding rule", "قاعدة التقريب"), false},
		{"calculation_trace_required", hdrPrefixAny("calculation trace", "مطلوب أثر الحساب"), false},
		{"metric_owner", hdrPrefixAny("metric owner", "مالك المقياس"), false},
		{"data_source", hdrAny("data source", "مصدر البيانات"), false},
		{"evidence_required", hdrAny("evidence required", "مطلوب دليل"), false},
		{"start_date", hdrPrefixAny("start date", "تاريخ البدء"), false},
		{"due_date", hdrPrefixAny("due date", "تاريخ الاستحقاق"), false},
		{"formula", hdrAny("formula", "المعادلة"), false},
	}
}

func kpiMetricTemplateColumns() []kpiTemplateColumn {
	return []kpiTemplateColumn{
		{key: "kpi_code", ar: "كود المؤشر", en: "KPI Code", width: 16},
		{key: "kpi_type", ar: "نوع المؤشر", en: "KPI Type", width: 14,
			values: []string{"strategic", "operational", "award"}, valuesAr: []string{"استراتيجي", "تشغيلي", "جائزة"}},
		{key: "metric_code", ar: "كود المقياس", en: "Metric Code", width: 18},
		{key: "metric_name", ar: "اسم المقياس", en: "Metric Name", width: 32},
		{key: "metric_description", ar: "وصف المقياس", en: "Metric Description", width: 36},
		{key: "metric_status", ar: "الحالة", en: "Status", width: 14,
			values: []string{"Draft", "Active", "Inactive", "Archived"}, valuesAr: []string{"مسودة", "نشط", "غير نشط", "مؤرشف"}},
		{key: "display_order", ar: "ترتيب العرض", en: "Display Order", width: 12},
		{key: "metric_type", ar: "نوع المقياس", en: "Metric Type", width: 14,
			values: []string{"Numeric", "Percentage", "Currency", "Text"}, valuesAr: []string{"رقمي", "نسبة مئوية", "عملة", "نص"}},
		{key: "unit", ar: "الوحدة", en: "Unit", width: 16,
			values: []string{"%", "Number", "Seconds", "Minutes", "Hours", "Days", "SAR", "Employees", "Requests", "Complaints", "Tasks", "Kilometers", "Square Meters", "Score", "Custom"}},
		{key: "custom_unit_label", ar: "تسمية الوحدة المخصصة", en: "Custom Unit Label", width: 20},
		{key: "baseline_value", ar: "قيمة خط الأساس", en: "Baseline Value", width: 14},
		{key: "baseline_period", ar: "فترة خط الأساس (YYYY-MM)", en: "Baseline Period (YYYY-MM)", width: 20},
		{key: "weight", ar: "الوزن (%)", en: "Weight (%)", width: 12},
		{key: "calculation_type", ar: "نوع الحساب", en: "Calculation Type", width: 22,
			values:   []string{"Direct Value", "Percentage - Ratio", "Ratio", "Average", "Sum", "Difference", "Weighted Average", "Formula"},
			valuesAr: []string{"قيمة مباشرة", "نسبة مئوية - نسبة", "نسبة", "متوسط", "مجموع", "فرق", "متوسط مرجح", "معادلة"}},
		{key: "direction", ar: "الاتجاه", en: "Direction", width: 18,
			values:   []string{"Higher is Better", "Lower is Better", "Target Range", "Exact Target", "Informational"},
			valuesAr: []string{"الأعلى أفضل", "الأقل أفضل", "نطاق مستهدف", "هدف محدد", "معلوماتي"}},
		{key: "decimal_precision", ar: "الدقة العشرية", en: "Decimal Precision", width: 12},
		{key: "aggregation_method", ar: "طريقة التجميع", en: "Aggregation Method", width: 24,
			values:   []string{"Sum", "Average", "Latest Approved Value", "Recalculate Ratio from Approved Inputs", "Minimum", "Maximum", "Weighted Average", "No Aggregation"},
			valuesAr: []string{"مجموع", "متوسط", "أحدث قيمة معتمدة", "إعادة حساب النسبة من المدخلات المعتمدة", "الحد الأدنى", "الحد الأعلى", "متوسط مرجح", "بدون تجميع"}},
		{key: "reporting_frequency", ar: "تكرار الإبلاغ", en: "Reporting Frequency", width: 20,
			values:   []string{"Monthly", "Quarterly", "Semiannual", "Annual", "Ad Hoc", "Inherit from KPI"},
			valuesAr: []string{"شهري", "ربع سنوي", "نصف سنوي", "سنوي", "حسب الحاجة", "وراثة من المؤشر"}},
		{key: "numerator_label", ar: "تسمية البسط", en: "Numerator Label", width: 22},
		{key: "numerator_variable_code", ar: "رمز متغير البسط", en: "Numerator Variable Code", width: 22},
		{key: "denominator_label", ar: "تسمية المقام", en: "Denominator Label", width: 22},
		{key: "denominator_variable_code", ar: "رمز متغير المقام", en: "Denominator Variable Code", width: 22},
		{key: "direct_actual_label", ar: "تسمية القيمة الفعلية المباشرة", en: "Direct Actual Label", width: 24},
		{key: "allow_manual_actual_override", ar: "السماح بالتجاوز اليدوي", en: "Allow Manual Override", width: 16,
			values: []string{"Yes", "No"}, valuesAr: []string{"نعم", "لا"}},
		{key: "advanced_formula_enabled", ar: "تفعيل المعادلة المتقدمة", en: "Advanced Formula Enabled", width: 18,
			values: []string{"Yes", "No"}, valuesAr: []string{"نعم", "لا"}},
		{key: "formula_code", ar: "رمز المعادلة", en: "Formula Code", width: 18},
		{key: "divide_by_zero_handling", ar: "معالجة القسمة على صفر", en: "Divide by Zero Handling", width: 24,
			values:   []string{"Block Submission", "Return Null", "Return Zero", "Require Exception Approval"},
			valuesAr: []string{"حظر الإرسال", "إرجاع فارغ", "إرجاع صفر", "يتطلب موافقة استثناء"}},
		{key: "rounding_rule", ar: "قاعدة التقريب", en: "Rounding Rule", width: 18,
			values:   []string{"Standard Round", "Round Up", "Round Down", "No Rounding"},
			valuesAr: []string{"تقريب قياسي", "تقريب لأعلى", "تقريب لأسفل", "بدون تقريب"}},
		{key: "calculation_trace_required", ar: "مطلوب أثر الحساب", en: "Calculation Trace Required", width: 18,
			values: []string{"Yes", "No"}, valuesAr: []string{"نعم", "لا"}},
		{key: "metric_owner", ar: "مالك المقياس (البريد الإلكتروني أو معرف المستخدم)", en: "Metric Owner (email or user ID)", width: 32},
		{key: "data_source", ar: "مصدر البيانات", en: "Data Source", width: 22},
		{key: "evidence_required", ar: "مطلوب دليل", en: "Evidence Required", width: 14,
			values: []string{"Yes", "No"}, valuesAr: []string{"نعم", "لا"}},
		{key: "start_date", ar: "تاريخ البدء (YYYY-MM-DD)", en: "Start Date (YYYY-MM-DD)", width: 18},
		{key: "due_date", ar: "تاريخ الاستحقاق (YYYY-MM-DD)", en: "Due Date (YYYY-MM-DD)", width: 18},
		{key: "formula", ar: "المعادلة", en: "Formula", width: 30},
	}
}

func markKpiMetricTemplateRequired(cols []kpiTemplateColumn) []kpiTemplateColumn {
	required := make(map[string]bool, len(cols))
	for _, s := range kpiMetricColumns() {
		required[s.key] = s.required
	}
	for i := range cols {
		cols[i].required = required[cols[i].key]
	}
	return cols
}

func kpiMetricColumnLabel(key string) string {
	for _, c := range kpiMetricTemplateColumns() {
		if c.key == key {
			return c.en
		}
	}
	return key
}

func buildKpiMetricsInstructions() *utils.InstructionConfig {
	text := func(en, ar string) utils.LocalizedText { return utils.LocalizedText{EN: en, AR: ar} }
	item := func(labelEN, labelAR, bodyEN, bodyAR string) utils.InstructionItem {
		return utils.InstructionItem{Label: text(labelEN, labelAR), Text: text(bodyEN, bodyAR)}
	}
	return &utils.InstructionConfig{LabelWidth: 30, TextWidth: 110, Sections: []utils.InstructionSection{
		{Items: []utils.InstructionItem{item("Purpose", "الغرض", "This workbook creates or updates metrics (KpiMetric) under existing KPIs. The KPI itself must already exist in the KPI Dictionary.", "ينشئ هذا المصنف مقاييس (KpiMetric) أو يحدّثها تحت مؤشرات موجودة. يجب أن يكون المؤشر نفسه موجوداً مسبقاً في دليل مؤشرات الأداء.")}, BlankAfter: true},
		{Title: text("How to use", "طريقة الاستخدام"), Items: []utils.InstructionItem{
			item("1. One row per metric", "1. صف واحد لكل مقياس", "Fill the KPI Metrics sheet starting at row 2. One metric per row. Use the KPI Code and KPI Type of an existing KPI.", "املأ ورقة مقاييس المؤشرات بدءاً من الصف 2. مقياس واحد في كل صف. استخدم كود المؤشر ونوع مؤشر موجود."),
			item("2. Save and upload", "2. الحفظ والرفع", "Save as .xlsx, upload it, click Validate to review the report and preview, then Commit.", "احفظ الملف بصيغة .xlsx، وارفعه، ثم اضغط تحقق لمراجعة التقرير والمعاينة، ثم تنفيذ."),
		}, BlankAfter: true},
		{Title: text("Rules", "القواعد"), Items: []utils.InstructionItem{
			item("Mandatory columns", "الأعمدة المطلوبة", "Columns marked with a red * are mandatory: KPI Code, KPI Type and Metric Name. Keep the * in the header text.", "الأعمدة المميزة بعلامة * الحمراء مطلوبة: كود المؤشر ونوع المؤشر واسم المقياس. حافظ على علامة * في نص الرأس."),
			item("KPI resolution", "مطابقة المؤشر", "KPI Code must match an existing strategic, operational or award KPI (case-insensitive). KPI Type must match that KPI's type.", "يجب أن يطابق كود المؤشر مؤشراً استراتيجياً أو تشغيلياً أو خاصاً بالجائزة موجوداً (دون حساسية لحالة الأحرف). ويجب أن يطابق نوع المؤشر نوع ذلك المؤشر."),
			item("Duplicate metrics", "تكرار المقاييس", "Metric Code is optional but recommended. An existing metric is matched by code first, then by name (case-insensitive) under the same KPI. Metric names must be unique per KPI; a duplicate row in the file is rejected.", "كود المقياس اختياري لكنه موصى به. تتم مطابقة المقياس الموجود بالكود أولاً ثم بالاسم (دون حساسية لحالة الأحرف) ضمن المؤشر نفسه. يجب أن تكون أسماء المقاييس فريدة لكل مؤشر؛ ويُرفض أي صف مكرر في الملف."),
			item("Existing metrics", "المقاييس الحالية", "With \"skip\", existing metrics are left untouched. With \"update\", the row replaces the metric definition while keeping its ID, so targets and entries stay linked. Updating requires the kpi:update permission.", "مع \"تخطي\" تُترك المقاييس الحالية دون تغيير. ومع \"تحديث\" يستبدل الصف تعريف المقياس مع الحفاظ على معرفه، لتبقى المستهدفات والمدخلات مرتبطة. التحديث يتطلب صلاحية kpi:update."),
			item("Weight", "الوزن", "Blank weight defaults to 1. The Active metrics of one KPI must total at most 100%. A row that would exceed the limit is rejected.", "الوزن الفارغ يساوي 1. يجب ألا يتجاوز مجموع أوزان المقاييس النشطة للمؤشر الواحد 100%. يُرفض أي صف يتجاوز الحد."),
			item("Approval lock", "قفل الاعتماد", "After the KPI is approved, a metric's Weight and Baseline are locked; changing them through import is rejected and must go through the baseline revision flow.", "بعد اعتماد المؤشر يُقفل وزن المقياس وخط الأساس؛ ويُرفض تغييرهما عبر الاستيراد ويجب أن يتم عبر مسار مراجعة خط الأساس."),
			item("Formats", "التنسيقات", "Baseline Period is YYYY-MM. Start/Due Date are YYYY-MM-DD. Booleans accept Yes/No (نعم/لا). Cells left blank use the same defaults as the metric form.", "فترة خط الأساس بصيغة YYYY-MM. وتاريخ البدء/الاستحقاق بصيغة YYYY-MM-DD. تقبل القيم المنطقية نعم/لا. تستخدم الخلايا الفارغة نفس القيم الافتراضية لنموذج المقياس."),
			item("Every column is used", "كل الأعمدة مستخدمة", "Every template column is imported. Extra columns are reported as ignored; they never stop the import.", "يتم استيراد كل عمود في القالب. تُبلّغ الأعمدة الإضافية كمتجاهلة ولا توقف الاستيراد."),
			item("Validation report", "تقرير التحقق", "Every rejected row is listed with its Excel row number and reason. \"skip_invalid\" commits valid rows; \"abort\" commits nothing while any error remains.", "يُدرج كل صف مرفوض مع رقم صفه في Excel وسبب الرفض. \"تخطي غير الصالح\" ينفذ الصفوف الصالحة؛ و\"إلغاء\" لا ينفذ أي شيء ما دام هناك خطأ."),
		}, BlankAfter: true},
	}}
}

// ─── Parsing ────────────────────────────────────────────────────────────────

func (imp *kpiMetricsImporter) parseWorkbook(data []byte) error {
	xf, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("the file is not a valid .xlsx workbook: %w", err)
	}
	defer xf.Close()

	sheet, rows, headerRow := imp.locateSheet(xf)
	if sheet == "" {
		return nil
	}
	imp.sheet = sheet

	table := imp.readMetricTable(sheet, rows, headerRow)
	if imp.structuralError {
		return nil
	}
	for i := headerRow + 1; i < len(rows); i++ {
		if metricRowEmpty(rows[i]) {
			continue
		}
		raw := func(key string) string {
			j, ok := table.cols[key]
			if !ok {
				return ""
			}
			return strings.TrimSpace(cell(rows[i], j))
		}
		imp.recs = append(imp.recs, imp.parseMetricRow(i+1, raw))
	}
	return nil
}

func (imp *kpiMetricsImporter) locateSheet(xf *excelize.File) (string, [][]string, int) {
	type candidate struct {
		sheet     string
		rows      [][]string
		headerRow int
	}
	var found []candidate
	for _, name := range xf.GetSheetList() {
		normalized := normHeader(name)
		if normalized == normHeader(kpiMetricsInstructionsSheet) || normalized == "تعليمات" {
			continue
		}
		rows, err := xf.GetRows(name, excelize.Options{RawCellValue: true})
		if err != nil {
			imp.structural(name, fmt.Sprintf("The worksheet could not be read: %v", err))
			continue
		}
		for i := 0; i < len(rows) && i < kpiMetricsWorksheetHeaderScan; i++ {
			if isMetricHeaderRow(rows[i]) {
				found = append(found, candidate{sheet: name, rows: rows, headerRow: i})
				break
			}
		}
	}
	switch len(found) {
	case 0:
		imp.structural("", "The workbook does not contain a KPI Metrics sheet — keep the template's \"KPI Metrics\" worksheet and its header row.")
		return "", nil, 0
	case 1:
		return found[0].sheet, found[0].rows, found[0].headerRow
	default:
		imp.structural(found[1].sheet, fmt.Sprintf("More than one worksheet looks like the KPI Metrics sheet (%q and %q) — keep only one.", found[0].sheet, found[1].sheet))
		return "", nil, 0
	}
}

func isMetricHeaderRow(row []string) bool {
	hasCode, hasName := false, false
	for _, h := range row {
		switch normHeader(h) {
		case "kpi code", "كود المؤشر":
			hasCode = true
		case "metric name", "اسم المقياس":
			hasName = true
		}
	}
	return hasCode || hasName
}

func (imp *kpiMetricsImporter) readMetricTable(sheet string, rows [][]string, headerRow int) *kpiMetricTable {
	table := &kpiMetricTable{sheet: sheet, headerRow: headerRow, cols: map[string]int{}, labels: map[string]string{}}
	specs := kpiMetricColumns()
	header := rows[headerRow]
	matched := make([]bool, len(header))
	for j, h := range header {
		normalized := normHeader(h)
		if normalized == "" {
			continue
		}
		for _, spec := range specs {
			if _, taken := table.cols[spec.key]; taken {
				continue
			}
			if spec.match(normalized) {
				table.cols[spec.key] = j
				table.labels[spec.key] = inputHeaderText(h)
				matched[j] = true
				break
			}
		}
		if !matched[j] {
			imp.warn(sheet, headerRow+1, "", "", fmt.Sprintf("Column %q is not part of the KPI Metrics template and will be ignored.", inputHeaderText(h)))
		}
	}
	for _, spec := range specs {
		if _, ok := table.cols[spec.key]; !ok && spec.required {
			imp.structural(sheet, fmt.Sprintf("Mandatory column %q is missing from the header row.", kpiMetricColumnLabel(spec.key)))
		}
	}
	return table
}

func metricRowEmpty(row []string) bool {
	for _, v := range row {
		if strings.TrimSpace(v) != "" {
			return false
		}
	}
	return true
}

func (imp *kpiMetricsImporter) parseMetricRow(row int, raw func(string) string) *kpiMetricRec {
	set := map[string]bool{}
	read := func(key string) string {
		v := raw(key)
		if v != "" {
			set[key] = true
		}
		return v
	}
	r := &kpiMetricRec{
		Row:     row,
		KpiCode: read("kpi_code"),
		Metric: &models.KpiMetric{
			Name:                     read("metric_name"),
			MetricCode:               read("metric_code"),
			MetricDescription:        read("metric_description"),
			MetricStatus:             "Active",
			MetricType:               "Numeric",
			Unit:                     read("unit"),
			CustomUnitLabel:          read("custom_unit_label"),
			CalculationType:          "Direct Value",
			Direction:                "Higher is Better",
			DecimalPrecision:         2,
			AggregationMethod:        "Sum",
			DivideByZeroHandling:     "Block Submission",
			RoundingRule:             "Standard Round",
			CalculationTraceRequired: true,
			DataSource:               read("data_source"),
			Formula:                  read("formula"),
			Weight:                   1,
		},
		OwnerRaw: read("metric_owner"),
		Set:      set,
	}

	if r.KpiCode == "" {
		r.reject("KPI Code", "KPI Code is required.")
	}
	if r.Metric.Name == "" {
		r.reject("Metric Name", "Metric Name is required.")
	}
	if typ := read("kpi_type"); typ == "" {
		r.reject("KPI Type", "KPI Type is required (strategic, operational or award).")
	} else if v, ok := normalizeMetricChoice(typ, kpiTypeChoices); !ok {
		r.reject("KPI Type", fmt.Sprintf("KPI Type %q is not valid. Allowed: strategic, operational, award.", typ))
	} else {
		r.KpiType = v
	}

	applyChoice := func(field, rawKey, def string, choices []metricChoice, allowed string, target *string) {
		rawValue := read(rawKey)
		if rawValue == "" {
			*target = def
			return
		}
		v, ok := normalizeMetricChoice(rawValue, choices)
		if !ok {
			r.reject(field, fmt.Sprintf("%s %q is not valid. Allowed: %s.", field, rawValue, allowed))
			*target = def
			return
		}
		*target = v
	}
	applyChoice("Status", "metric_status", "Active", metricStatusChoices, "Draft, Active, Inactive, Archived", &r.Metric.MetricStatus)
	applyChoice("Metric Type", "metric_type", "Numeric", metricTypeChoices, "Numeric, Percentage, Currency, Text", &r.Metric.MetricType)
	applyChoice("Calculation Type", "calculation_type", "Direct Value", metricCalculationChoices, "Direct Value, Percentage - Ratio, Ratio, Average, Sum, Difference, Weighted Average, Formula", &r.Metric.CalculationType)
	applyChoice("Direction", "direction", "Higher is Better", metricDirectionChoices, "Higher is Better, Lower is Better, Target Range, Exact Target, Informational", &r.Metric.Direction)
	applyChoice("Aggregation Method", "aggregation_method", "Sum", metricAggregationChoices, "Sum, Average, Latest Approved Value, Recalculate Ratio from Approved Inputs, Minimum, Maximum, Weighted Average, No Aggregation", &r.Metric.AggregationMethod)
	applyChoice("Reporting Frequency", "reporting_frequency", "", metricFrequencyChoices, "Monthly, Quarterly, Semiannual, Annual, Ad Hoc, Inherit from KPI", &r.Metric.ReportingFrequency)
	applyChoice("Divide by Zero Handling", "divide_by_zero_handling", "Block Submission", metricDivideChoices, "Block Submission, Return Null, Return Zero, Require Exception Approval", &r.Metric.DivideByZeroHandling)
	applyChoice("Rounding Rule", "rounding_rule", "Standard Round", metricRoundingChoices, "Standard Round, Round Up, Round Down, No Rounding", &r.Metric.RoundingRule)

	applyBool := func(field, rawKey string, def bool, target *bool) {
		rawValue := read(rawKey)
		v, ok := parseMetricBool(rawValue, def)
		if !ok {
			r.reject(field, fmt.Sprintf("%s %q is not valid. Allowed: Yes or No.", field, rawValue))
			return
		}
		*target = v
	}
	applyBool("Allow Manual Override", "allow_manual_actual_override", false, &r.Metric.AllowManualActualOverride)
	applyBool("Advanced Formula Enabled", "advanced_formula_enabled", false, &r.Metric.AdvancedFormulaEnabled)
	applyBool("Calculation Trace Required", "calculation_trace_required", true, &r.Metric.CalculationTraceRequired)
	applyBool("Evidence Required", "evidence_required", false, &r.Metric.EvidenceRequired)

	if rawValue := read("display_order"); rawValue != "" {
		if v, ok := parseMetricInt(rawValue); !ok || v < 0 {
			r.reject("Display Order", fmt.Sprintf("Display Order %q must be a whole number of 0 or more.", rawValue))
		} else {
			r.Metric.DisplayOrder = v
		}
	}
	if rawValue := read("decimal_precision"); rawValue != "" {
		if v, ok := parseMetricInt(rawValue); !ok || v < 0 || v > 4 {
			r.reject("Decimal Precision", fmt.Sprintf("Decimal Precision %q must be a whole number between 0 and 4.", rawValue))
		} else {
			r.Metric.DecimalPrecision = v
		}
	}
	if rawValue := read("baseline_value"); rawValue != "" {
		if v, ok := parseMetricFloat(rawValue); !ok {
			r.reject("Baseline Value", fmt.Sprintf("Baseline Value %q must be a number.", rawValue))
		} else {
			r.Metric.BaselineValue = v
		}
	}
	if rawValue := read("weight"); rawValue != "" {
		if v, ok := parseMetricFloat(rawValue); !ok || v < 0 || v > 100 {
			r.reject("Weight", fmt.Sprintf("Weight %q must be a number between 0 and 100.", rawValue))
		} else {
			r.Metric.Weight = v
		}
	}
	if rawValue := read("baseline_period"); rawValue != "" {
		if !kpiMetricBaselinePeriodRe.MatchString(rawValue) {
			r.reject("Baseline Period", fmt.Sprintf("Baseline Period %q must be in YYYY-MM format.", rawValue))
		} else {
			r.Metric.BaselinePeriod = rawValue
		}
	}
	if rawValue := read("start_date"); rawValue != "" {
		if v, ok := parseMetricDate(rawValue); !ok {
			r.reject("Start Date", fmt.Sprintf("Start Date %q must be a date in YYYY-MM-DD format.", rawValue))
		} else {
			r.Metric.StartDate = v
		}
	}
	if rawValue := read("due_date"); rawValue != "" {
		if v, ok := parseMetricDate(rawValue); !ok {
			r.reject("Due Date", fmt.Sprintf("Due Date %q must be a date in YYYY-MM-DD format.", rawValue))
		} else {
			r.Metric.DueDate = v
		}
	}
	if r.Metric.StartDate != nil && r.Metric.DueDate != nil && r.Metric.DueDate.Before(*r.Metric.StartDate) {
		r.reject("Due Date", "Due Date cannot be earlier than Start Date.")
	}

	r.Metric.NumeratorLabel = read("numerator_label")
	r.Metric.NumeratorVariableCode = read("numerator_variable_code")
	r.Metric.DenominatorLabel = read("denominator_label")
	r.Metric.DenominatorVariableCode = read("denominator_variable_code")
	r.Metric.DirectActualLabel = read("direct_actual_label")
	r.Metric.FormulaCode = read("formula_code")

	checkMetricLen(r, "Metric Code", r.Metric.MetricCode, 50)
	checkMetricLen(r, "Metric Name", r.Metric.Name, 255)
	checkMetricLen(r, "Unit", r.Metric.Unit, 50)
	checkMetricLen(r, "Custom Unit Label", r.Metric.CustomUnitLabel, 255)
	checkMetricLen(r, "Numerator Label", r.Metric.NumeratorLabel, 255)
	checkMetricLen(r, "Numerator Variable Code", r.Metric.NumeratorVariableCode, 100)
	checkMetricLen(r, "Denominator Label", r.Metric.DenominatorLabel, 255)
	checkMetricLen(r, "Denominator Variable Code", r.Metric.DenominatorVariableCode, 100)
	checkMetricLen(r, "Direct Actual Label", r.Metric.DirectActualLabel, 255)
	checkMetricLen(r, "Formula Code", r.Metric.FormulaCode, 100)
	checkMetricLen(r, "Data Source", r.Metric.DataSource, 100)
	return r
}

func checkMetricLen(r *kpiMetricRec, field, value string, max int) {
	if value == "" {
		return
	}
	if n := utf8.RuneCountInString(value); n > max {
		r.reject(field, fmt.Sprintf("%s must be at most %d characters (got %d).", field, max, n))
	}
}

// ─── Existing data ──────────────────────────────────────────────────────────

func (imp *kpiMetricsImporter) loadExisting() error {
	db := imp.db.WithContext(imp.ctx)
	imp.existingKpis = map[string]*existingKpi{}
	imp.kpiByCode = map[string]string{}
	imp.metricsByKpi = map[string][]*models.KpiMetric{}
	imp.metricByCode = map[string]*models.KpiMetric{}
	imp.metricByName = map[string]*models.KpiMetric{}
	imp.usersByLogin = map[string]uuid.UUID{}
	imp.usersByID = map[uuid.UUID]bool{}
	imp.kpiLocked = map[string]bool{}

	for _, t := range []struct {
		kpiType string
		model   interface{}
	}{
		{models.KPITypeStrategic, &models.StrategicKPI{}},
		{models.KPITypeOperational, &models.OperationalKPI{}},
		{models.KPITypeAward, &models.AwardKPI{}},
	} {
		var rows []struct {
			ID               uuid.UUID
			Code             string
			NameEn           string
			ActivationStatus string
		}
		if err := db.Model(t.model).Select("id, code, name_en, activation_status").Scan(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			code := strings.ToUpper(strings.TrimSpace(row.Code))
			key := t.kpiType + "|" + code
			imp.existingKpis[key] = &existingKpi{ID: row.ID, Type: t.kpiType, Code: row.Code, NameEn: row.NameEn}
			if _, ok := imp.kpiByCode[code]; !ok {
				imp.kpiByCode[code] = t.kpiType
			}
			status := strings.ToLower(strings.TrimSpace(row.ActivationStatus))
			imp.kpiLocked[t.kpiType+"|"+row.ID.String()] = status == "approved" || status == "active" || status == "closed"
		}
	}

	var metrics []models.KpiMetric
	if err := db.Order("display_order ASC, created_at ASC").Find(&metrics).Error; err != nil {
		return err
	}
	for i := range metrics {
		m := &metrics[i]
		group := strings.ToLower(m.KpiType) + "|" + m.KpiID.String()
		imp.metricsByKpi[group] = append(imp.metricsByKpi[group], m)
		if code := strings.ToLower(strings.TrimSpace(m.MetricCode)); code != "" {
			imp.metricByCode[group+"|"+code] = m
		}
		if name := strings.ToLower(strings.TrimSpace(m.Name)); name != "" {
			imp.metricByName[group+"|"+name] = m
		}
	}

	var users []struct {
		ID       uuid.UUID
		Email    string
		Username string
	}
	if err := db.Model(&models.User{}).Select("id, email, username").Scan(&users).Error; err != nil {
		return err
	}
	for _, u := range users {
		imp.usersByID[u.ID] = true
		for _, login := range []string{u.Email, u.Username} {
			login = strings.ToLower(strings.TrimSpace(login))
			if login != "" {
				imp.usersByLogin[login] = u.ID
			}
		}
	}
	return nil
}

// ─── Row validation ─────────────────────────────────────────────────────────

func (imp *kpiMetricsImporter) validateRows() {
	type seenKeys struct {
		codes    map[string]int
		names    map[string]int
		existing map[uuid.UUID]int
	}
	seen := map[string]*seenKeys{}
	for _, r := range imp.recs {
		if r.Rejected {
			continue
		}
		code := strings.ToUpper(strings.TrimSpace(r.KpiCode))
		kpiKey := r.KpiType + "|" + code
		ek := imp.existingKpis[kpiKey]
		if ek == nil {
			if other, ok := imp.kpiByCode[code]; ok {
				r.reject("KPI Code", fmt.Sprintf("KPI %s exists as %s, not %s — check the KPI Type column.", r.KpiCode, other, r.KpiType))
			} else {
				r.reject("KPI Code", fmt.Sprintf("KPI %s (%s) was not found. Import the KPI Dictionary first or correct the KPI Code and KPI Type.", r.KpiCode, r.KpiType))
			}
			continue
		}
		r.KpiID = ek.ID
		r.KpiName = ek.NameEn

		if r.OwnerRaw != "" {
			if id, ok := imp.resolveUser(r.OwnerRaw); ok {
				r.Metric.MetricOwnerID = &id
			} else {
				r.reject("Metric Owner", fmt.Sprintf("Metric Owner %q was not found. Use a user email, username or user ID.", r.OwnerRaw))
			}
		}

		group := r.KpiType + "|" + r.KpiID.String()
		s := seen[group]
		if s == nil {
			s = &seenKeys{codes: map[string]int{}, names: map[string]int{}, existing: map[uuid.UUID]int{}}
			seen[group] = s
		}
		nameKey := strings.ToLower(strings.TrimSpace(r.Metric.Name))
		codeKey := strings.ToLower(strings.TrimSpace(r.Metric.MetricCode))
		if codeKey != "" {
			if row, dup := s.codes[codeKey]; dup {
				r.reject("Metric Code", fmt.Sprintf("Duplicate metric in this file — the same Metric Code is used on row %d.", row))
			}
		}
		if row, dup := s.names[nameKey]; dup {
			r.reject("Metric Name", fmt.Sprintf("Duplicate metric in this file — the same Metric Name is used on row %d.", row))
		}
		if r.Rejected {
			continue
		}
		if codeKey != "" {
			s.codes[codeKey] = r.Row
		}
		s.names[nameKey] = r.Row

		existing := imp.matchExistingMetric(group, codeKey, nameKey)
		if existing != nil {
			if row, dup := s.existing[existing.ID]; dup {
				r.reject("Metric Code", fmt.Sprintf("Duplicate metric in this file — row %d already matches the same existing metric.", row))
				continue
			}
			s.existing[existing.ID] = r.Row
			id := existing.ID
			r.ExistingID = &id
			r.ExistingWeight = existing.Weight
			r.ExistingActive = strings.EqualFold(existing.MetricStatus, "Active")
			r.ExistingBaseline = existing.BaselineValue
			r.ExistingBaselinePeriod = existing.BaselinePeriod
			if other := imp.metricByName[group+"|"+nameKey]; other != nil && other.ID != existing.ID {
				r.reject("Metric Name", fmt.Sprintf("Metric name %q already exists under this KPI.", r.Metric.Name))
			}
			if imp.opts.ExistingMode == KpiImportExistingUpdate {
				// A blank optional cell keeps the existing value on update.
				if !r.Set["weight"] {
					r.Metric.Weight = existing.Weight
				}
				if !r.Set["baseline_value"] {
					r.Metric.BaselineValue = existing.BaselineValue
				}
				if !r.Set["baseline_period"] {
					r.Metric.BaselinePeriod = existing.BaselinePeriod
				}
				if !r.Set["metric_status"] {
					r.Metric.MetricStatus = existing.MetricStatus
				}
				if imp.kpiLocked[group] {
					if r.Set["weight"] && math.Abs(existing.Weight-r.Metric.Weight) >= 0.005 {
						r.reject("Weight", "The KPI is approved; Weight is locked and can only be changed before approval.")
					}
					if (r.Set["baseline_value"] || r.Set["baseline_period"]) &&
						(math.Abs(existing.BaselineValue-r.Metric.BaselineValue) >= 0.00005 || existing.BaselinePeriod != r.Metric.BaselinePeriod) {
						r.reject("Baseline Value", "The KPI is approved; Baseline is locked and can only be changed through an approved baseline revision.")
					}
				}
			}
		}
	}
	imp.applyWeightLimit()
}

func (imp *kpiMetricsImporter) matchExistingMetric(group, codeKey, nameKey string) *models.KpiMetric {
	if codeKey != "" {
		if m := imp.metricByCode[group+"|"+codeKey]; m != nil {
			return m
		}
	}
	return imp.metricByName[group+"|"+nameKey]
}

func (imp *kpiMetricsImporter) resolveUser(raw string) (uuid.UUID, bool) {
	raw = strings.TrimSpace(raw)
	if id, err := uuid.Parse(raw); err == nil {
		if imp.usersByID[id] {
			return id, true
		}
		return uuid.Nil, false
	}
	id, ok := imp.usersByLogin[strings.ToLower(raw)]
	return id, ok
}

// applyWeightLimit enforces Active weights ≤ 100 per KPI, applying updates before creates.
func (imp *kpiMetricsImporter) applyWeightLimit() {
	used := map[string]float64{}
	for group, metrics := range imp.metricsByKpi {
		for _, m := range metrics {
			if strings.EqualFold(m.MetricStatus, "Active") {
				used[group] += m.Weight
			}
		}
	}
	updateMode := imp.opts.ExistingMode == KpiImportExistingUpdate

	type pendingUpdate struct {
		rec   *kpiMetricRec
		delta float64
	}
	var updates []pendingUpdate
	if updateMode {
		for _, r := range imp.recs {
			if r.Rejected || r.ExistingID == nil {
				continue
			}
			delta := 0.0
			if strings.EqualFold(r.Metric.MetricStatus, "Active") {
				delta += r.Metric.Weight
			}
			if r.ExistingActive {
				delta -= r.ExistingWeight
			}
			updates = append(updates, pendingUpdate{rec: r, delta: delta})
		}
		// Apply weight-freeing updates first so one row freeing weight makes
		// room for a later row in the same file regardless of sheet order.
		sort.SliceStable(updates, func(i, j int) bool { return updates[i].delta < updates[j].delta })
	}
	for _, u := range updates {
		r := u.rec
		group := r.KpiType + "|" + r.KpiID.String()
		if u.delta > 0 && used[group]+u.delta > 100.005 {
			r.reject("Weight", fmt.Sprintf("Active metric weights for this KPI would exceed 100%% (%.2f already allocated, %.2f remaining).", used[group], math.Max(0, 100-used[group])))
			continue
		}
		used[group] += u.delta
	}
	for _, r := range imp.recs {
		if r.Rejected || r.ExistingID != nil {
			continue
		}
		if !strings.EqualFold(r.Metric.MetricStatus, "Active") {
			continue
		}
		group := r.KpiType + "|" + r.KpiID.String()
		if used[group]+r.Metric.Weight > 100.005 {
			r.reject("Weight", fmt.Sprintf("Active metric weights for this KPI would exceed 100%% (%.2f already allocated, %.2f remaining).", used[group], math.Max(0, 100-used[group])))
			continue
		}
		used[group] += r.Metric.Weight
	}
}

// ─── Result ─────────────────────────────────────────────────────────────────

func (imp *kpiMetricsImporter) finalize() {
	for _, r := range imp.recs {
		if !r.Rejected {
			continue
		}
		r.RejectWhy = r.reason()
		for _, issue := range r.Issues {
			imp.errorf(imp.sheet, r.Row, r.recordID(), issue.field, issue.message)
		}
	}

	existingAction := kpiImportActionSkip
	if imp.opts.ExistingMode == KpiImportExistingUpdate {
		existingAction = kpiImportActionUpdate
	}
	items := make([]KpiImportItem, 0, len(imp.recs))
	for _, r := range imp.recs {
		item := KpiImportItem{
			Category: kpiMetricsCategory,
			KpiType:  r.KpiType,
			RecordID: r.recordID(),
			Name:     r.Metric.Name,
			Sheet:    imp.sheet,
			Row:      r.Row,
		}
		switch {
		case r.Rejected:
			r.action = kpiImportActionReject
			item.Action = kpiImportActionReject
			item.Reason = r.RejectWhy
		case r.ExistingID != nil:
			r.action = existingAction
			item.Action = existingAction
			if existingAction == kpiImportActionSkip {
				item.Reason = "metric already exists (existing_mode=skip)"
			} else {
				item.Reason = "existing metric will be updated"
			}
		default:
			r.action = kpiImportActionCreate
			item.Action = kpiImportActionCreate
		}
		items = append(items, item)
	}
	imp.res.Items = items
	imp.res.Categories = map[string]*KpiImportCounts{kpiMetricsCategory: {}}
	imp.res.Totals = KpiImportCounts{}
	imp.count()
	actionable := imp.res.Totals.Created + imp.res.Totals.Updated
	imp.res.CanCommit = !imp.structuralError && actionable > 0 &&
		!(imp.opts.ErrorMode == KpiImportErrorsAbort && len(imp.res.Errors) > 0)
}

func (imp *kpiMetricsImporter) count() {
	if imp.res.Categories == nil {
		imp.res.Categories = map[string]*KpiImportCounts{}
	}
	if imp.res.Categories[kpiMetricsCategory] == nil {
		imp.res.Categories[kpiMetricsCategory] = &KpiImportCounts{}
	}
	imp.res.Totals = KpiImportCounts{}
	for _, it := range imp.res.Items {
		c := imp.res.Categories[kpiMetricsCategory]
		switch it.Action {
		case kpiImportActionCreate:
			c.Created++
			imp.res.Totals.Created++
		case kpiImportActionUpdate:
			c.Updated++
			imp.res.Totals.Updated++
		case kpiImportActionSkip:
			c.Skipped++
			imp.res.Totals.Skipped++
		case kpiImportActionReject:
			c.Rejected++
			imp.res.Totals.Rejected++
		case "rolled_back":
			c.RolledBack++
			imp.res.Totals.RolledBack++
		}
	}
}

func (imp *kpiMetricsImporter) structural(sheet, message string) {
	imp.structuralError = true
	if sheet == "" {
		sheet = "(workbook)"
	}
	imp.res.Errors = append(imp.res.Errors, KpiImportIssue{Severity: kpiImportSeverityError, Sheet: sheet, Message: message})
}

func (imp *kpiMetricsImporter) errorf(sheet string, row int, recordID, field, message string) {
	imp.res.Errors = append(imp.res.Errors, KpiImportIssue{
		Severity: kpiImportSeverityError,
		Sheet:    sheet,
		Row:      row,
		RecordID: recordID,
		Field:    field,
		Message:  message,
	})
}

func (imp *kpiMetricsImporter) warn(sheet string, row int, recordID, field, message string) {
	imp.res.Warnings = append(imp.res.Warnings, KpiImportIssue{
		Severity: kpiImportSeverityWarning,
		Sheet:    sheet,
		Row:      row,
		RecordID: recordID,
		Field:    field,
		Message:  message,
	})
}

func (imp *kpiMetricsImporter) markRolledBack() {
	for i := range imp.res.Items {
		it := &imp.res.Items[i]
		if it.Action == kpiImportActionCreate || it.Action == kpiImportActionUpdate {
			it.Action = "rolled_back"
			it.Reason = "transaction rolled back"
		}
	}
	imp.count()
	imp.res.CanCommit = false
}

// ─── Value helpers ──────────────────────────────────────────────────────────

type metricChoice struct {
	value   string
	aliases []string
}

func mc(value string, aliases ...string) metricChoice {
	return metricChoice{value: value, aliases: aliases}
}

var (
	kpiTypeChoices = []metricChoice{
		mc("strategic", "strategic kpi", "استراتيجي", "مؤشر استراتيجي"),
		mc("operational", "operational kpi", "تشغيلي", "مؤشر تشغيلي"),
		mc("award", "award kpi", "جائزة", "مؤشر جائزة"),
	}
	metricStatusChoices = []metricChoice{
		mc("Draft", "مسودة"),
		mc("Active", "نشط", "فعال"),
		mc("Inactive", "غير نشط"),
		mc("Archived", "مؤرشف"),
	}
	metricTypeChoices = []metricChoice{
		mc("Numeric", "رقمي", "عدد"),
		mc("Percentage", "نسبة مئوية", "نسبة"),
		mc("Currency", "عملة"),
		mc("Text", "نص"),
	}
	metricCalculationChoices = []metricChoice{
		mc("Direct Value", "قيمة مباشرة"),
		mc("Percentage - Ratio", "نسبة مئوية - نسبة"),
		mc("Ratio", "نسبة"),
		mc("Average", "متوسط"),
		mc("Sum", "مجموع"),
		mc("Difference", "فرق"),
		mc("Weighted Average", "متوسط مرجح"),
		mc("Formula", "معادلة"),
	}
	metricDirectionChoices = []metricChoice{
		mc("Higher is Better", "الأعلى أفضل", "أعلى أفضل"),
		mc("Lower is Better", "الأقل أفضل", "أقل أفضل"),
		mc("Target Range", "نطاق مستهدف"),
		mc("Exact Target", "هدف محدد"),
		mc("Informational", "معلوماتي"),
	}
	metricAggregationChoices = []metricChoice{
		mc("Sum", "مجموع"),
		mc("Average", "متوسط"),
		mc("Latest Approved Value", "أحدث قيمة معتمدة"),
		mc("Recalculate Ratio from Approved Inputs", "إعادة حساب النسبة من المدخلات المعتمدة"),
		mc("Minimum", "الحد الأدنى"),
		mc("Maximum", "الحد الأعلى"),
		mc("Weighted Average", "متوسط مرجح"),
		mc("No Aggregation", "بدون تجميع"),
	}
	metricFrequencyChoices = []metricChoice{
		mc("Monthly", "شهري"),
		mc("Quarterly", "ربع سنوي"),
		mc("Semiannual", "نصف سنوي", "semi-annual", "semi annual"),
		mc("Annual", "سنوي"),
		mc("Ad Hoc", "حسب الحاجة"),
		mc("", "inherit from kpi", "وراثة من المؤشر", "موروث من المؤشر"),
	}
	metricDivideChoices = []metricChoice{
		mc("Block Submission", "حظر الإرسال"),
		mc("Return Null", "إرجاع فارغ"),
		mc("Return Zero", "إرجاع صفر"),
		mc("Require Exception Approval", "يتطلب موافقة استثناء"),
	}
	metricRoundingChoices = []metricChoice{
		mc("Standard Round", "تقريب قياسي"),
		mc("Round Up", "تقريب لأعلى"),
		mc("Round Down", "تقريب لأسفل"),
		mc("No Rounding", "بدون تقريب"),
	}

	kpiMetricBaselinePeriodRe = regexp.MustCompile(`^\d{4}-(0[1-9]|1[0-2])$`)
)

var arabicChoiceReplacer = strings.NewReplacer(
	"أ", "ا", "إ", "ا", "آ", "ا",
	"ى", "ي", "ة", "ه",
	"َ", "", "ً", "", "ُ", "", "ٌ", "", "ِ", "", "ٍ", "", "ْ", "", "ّ", "", "ـ", "",
)

func normalizeMetricChoice(raw string, choices []metricChoice) (string, bool) {
	s := normalizeChoiceText(raw)
	if s == "" {
		return "", false
	}
	for _, c := range choices {
		if s == normalizeChoiceText(c.value) {
			return c.value, true
		}
		for _, alias := range c.aliases {
			if s == normalizeChoiceText(alias) {
				return c.value, true
			}
		}
	}
	return "", false
}

func normalizeChoiceText(s string) string {
	s = strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(s)), " "))
	return arabicChoiceReplacer.Replace(s)
}

func parseMetricBool(raw string, def bool) (bool, bool) {
	switch normalizeChoiceText(raw) {
	case "":
		return def, true
	case "yes", "y", "true", "1", "نعم":
		return true, true
	case "no", "n", "false", "0", "لا":
		return false, true
	}
	return def, false
}

func parseMetricInt(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, true
	}
	v, err := strconv.Atoi(raw)
	return v, err == nil
}

func parseMetricFloat(raw string) (float64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, true
	}
	raw = strings.ReplaceAll(raw, ",", "")
	raw = strings.TrimSpace(strings.TrimSuffix(raw, "%"))
	v, err := strconv.ParseFloat(raw, 64)
	return v, err == nil
}

func parseMetricDate(raw string) (*time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, true
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return &t, true
	}
	if serial, err := strconv.ParseFloat(raw, 64); err == nil && serial > 20000 && serial < 80000 {
		if t, err := excelize.ExcelDateToTime(serial, false); err == nil {
			return &t, true
		}
	}
	return nil, false
}
