package services

// KPI Targets bulk import — validates and commits targets for existing KPIs.
// Periods follow the KPI/metric frequency; milestones derive them from dates.
// Only draft, returned or rejected existing targets can be updated.

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/utils"
	"github.com/google/uuid"
	"github.com/xuri/excelize/v2"
	"gorm.io/gorm"
)

const (
	KpiTargetsTemplateFileName = "kpi-targets-import-template.xlsx"

	kpiTargetsTemplateSheet       = "KPI Targets"
	kpiTargetsCategory            = "target"
	kpiTargetsTemplateDataRows    = 4999
	kpiTargetsWorksheetHeaderScan = 15
)

type kpiTargetIssue struct {
	field   string
	message string
}

type kpiTargetImportRec struct {
	Row        int
	KpiCode    string
	KpiType    string
	KpiID      uuid.UUID
	MetricID   *uuid.UUID
	MetricCode string
	MetricName string

	Target *models.KpiAnnualTarget
	Set    map[string]bool

	Existing *models.KpiAnnualTarget

	Issues    []kpiTargetIssue
	Rejected  bool
	RejectWhy string
	action    string
}

func (r *kpiTargetImportRec) reject(field, message string) {
	r.Rejected = true
	r.Issues = append(r.Issues, kpiTargetIssue{field: field, message: message})
}

func (r *kpiTargetImportRec) reason() string {
	parts := make([]string, 0, len(r.Issues))
	for _, is := range r.Issues {
		parts = append(parts, is.message)
	}
	return strings.Join(parts, " ")
}

func (r *kpiTargetImportRec) recordID() string {
	id := strings.TrimSpace(r.KpiCode)
	if r.Target != nil {
		if r.MetricName != "" {
			id += " / " + r.MetricName
		}
		id += fmt.Sprintf(" / %d / %s", r.Target.TargetYear, r.Target.PeriodCode)
	}
	return id
}

type kpiTargetImportTable struct {
	sheet     string
	headerRow int
	cols      map[string]int
}

type kpiTargetsImporter struct {
	db     *gorm.DB
	ctx    context.Context
	userID uuid.UUID
	opts   KpiImportOptions
	res    *KpiImportResult

	structuralError bool
	sheet           string
	recs            []*kpiTargetImportRec

	existingKpis   map[string]*existingKpi // type|CODE
	kpiByCode      map[string]string       // CODE -> type
	metricsByKpi   map[string][]*models.KpiMetric
	metricByCode   map[string]*models.KpiMetric // type|kpiID|lower(code)
	metricByName   map[string]*models.KpiMetric // type|kpiID|lower(name)
	existingTarget map[string]*models.KpiAnnualTarget
	allTargets     []*models.KpiAnnualTarget // version-1 rows, for Milestone overlap checks
}

// ─── Entry points ───────────────────────────────────────────────────────────

func ValidateKpiTargetsImport(ctx context.Context, db *gorm.DB, data []byte, fileName string, userID uuid.UUID, opts KpiImportOptions) (*KpiImportResult, error) {
	imp, err := buildKpiTargetsPlan(ctx, db, data, fileName, userID, opts)
	if err != nil {
		return nil, err
	}
	return imp.res, nil
}

func CommitKpiTargetsImport(ctx context.Context, db *gorm.DB, data []byte, fileName string, userID uuid.UUID, opts KpiImportOptions) (*KpiImportResult, error) {
	imp, err := buildKpiTargetsPlan(ctx, db, data, fileName, userID, opts)
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

func buildKpiTargetsPlan(ctx context.Context, db *gorm.DB, data []byte, fileName string, userID uuid.UUID, opts KpiImportOptions) (*kpiTargetsImporter, error) {
	imp := &kpiTargetsImporter{
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

func KpiTargetsTemplateFileNameFor(lang string) string {
	if lang == KpiTemplateLangAR {
		return "kpi-targets-import-template-ar.xlsx"
	}
	return KpiTargetsTemplateFileName
}

func BuildKpiTargetsImportTemplate(lang string) ([]byte, error) {
	language := utils.LanguageEnglish
	if lang == KpiTemplateLangAR {
		language = utils.LanguageArabic
	}
	cfg := utils.WorkbookConfig{
		Language:     language,
		Title:        "KPI Targets Import Template",
		Subject:      "Bulk import of KPI targets",
		Creator:      "Automax",
		ActiveSheet:  kpiTargetsTemplateSheet,
		Instructions: buildKpiTargetsInstructions(),
		Sheets: []utils.SheetConfig{{
			Name:         kpiTargetsTemplateSheet,
			Columns:      toExcelColumns(markKpiTargetTemplateRequired(kpiTargetTemplateColumns())),
			DataRows:     kpiTargetsTemplateDataRows,
			FreezeHeader: true,
		}},
	}
	return utils.GenerateExcelTemplate(cfg)
}

func kpiTargetColumns() []colSpec {
	return []colSpec{
		{"kpi_code", hdrAny("kpi code", "كود المؤشر"), true},
		{"kpi_type", hdrAny("kpi type", "نوع المؤشر"), true},
		{"metric_code", hdrAny("metric code", "كود المقياس"), false},
		{"metric_name", hdrAny("metric name", "اسم المقياس"), false},
		{"target_year", hdrPrefixAny("target year", "السنة المستهدفة"), false},
		{"period_code", hdrAny("period code", "رمز الفترة"), false},
		{"period_start", hdrPrefixAny("period start", "بداية الفترة"), false},
		{"period_end", hdrPrefixAny("period end", "نهاية الفترة"), false},
		{"target_value", hdrAny("target value", "قيمة المستهدف"), true},
		{"target_type", hdrAny("target type", "نوع المستهدف"), true},
		{"target_basis", hdrAny("target basis", "أساس المستهدف"), false},
		{"target_rationale", hdrAny("target rationale", "مبررات المستهدف"), false},
		{"threshold_mode", hdrAny("threshold mode", "نمط الحدود"), false},
		{"excellent_threshold", hdrPrefixAny("excellent threshold", "حد التميز"), false},
		{"achieved_threshold", hdrPrefixAny("achieved threshold", "حد التحقق"), false},
		{"warning_threshold", hdrPrefixAny("warning threshold", "حد التحذير"), false},
		{"target_range_min", hdrAny("target range min", "أدنى قيمة للنطاق"), false},
		{"target_range_max", hdrAny("target range max", "أعلى قيمة للنطاق"), false},
		{"target_status", hdrAny("target status", "حالة المستهدف"), false},
		{"effective_from", hdrPrefixAny("effective from", "ساري من"), false},
		{"effective_to", hdrPrefixAny("effective to", "ساري حتى"), false},
	}
}

func kpiTargetTemplateColumns() []kpiTemplateColumn {
	return []kpiTemplateColumn{
		{key: "kpi_code", ar: "كود المؤشر", en: "KPI Code", width: 16},
		{key: "kpi_type", ar: "نوع المؤشر", en: "KPI Type", width: 14,
			values: []string{"strategic", "operational", "award"}, valuesAr: []string{"استراتيجي", "تشغيلي", "جائزة"}},
		{key: "metric_code", ar: "كود المقياس", en: "Metric Code", width: 18},
		{key: "metric_name", ar: "اسم المقياس", en: "Metric Name", width: 30},
		{key: "target_year", ar: "السنة المستهدفة", en: "Target Year", width: 14},
		{key: "period_code", ar: "رمز الفترة", en: "Period Code", width: 16},
		{key: "period_start", ar: "بداية الفترة (YYYY-MM-DD)", en: "Period Start (YYYY-MM-DD)", width: 20},
		{key: "period_end", ar: "نهاية الفترة (YYYY-MM-DD)", en: "Period End (YYYY-MM-DD)", width: 20},
		{key: "target_value", ar: "قيمة المستهدف", en: "Target Value", width: 14},
		{key: "target_type", ar: "نوع المستهدف", en: "Target Type", width: 20,
			values: []string{"Period Target", "Annual Target", "Milestone / Ad Hoc"}, valuesAr: []string{"مستهدف فترة", "مستهدف سنوي", "مرحلة / حسب الحاجة"}},
		{key: "target_basis", ar: "أساس المستهدف", en: "Target Basis", width: 22,
			values:   []string{"Strategic Plan", "Previous Year", "Benchmark", "Regulatory Requirement", "Contract / SLA", "Management Decision", "Forecast", "Other"},
			valuesAr: []string{"الخطة الاستراتيجية", "العام السابق", "المعيار المرجعي", "متطلب تنظيمي", "عقد / اتفاقية مستوى خدمة", "قرار إداري", "توقع", "أخرى"}},
		{key: "target_rationale", ar: "مبررات المستهدف", en: "Target Rationale", width: 36},
		{key: "threshold_mode", ar: "نمط الحدود", en: "Threshold Mode", width: 24,
			values:   []string{"Use Global KPI Rules", "Percentage of Target", "Absolute Values", "Target Range", "No Thresholds"},
			valuesAr: []string{"استخدام قواعد المؤشر العامة", "نسبة من المستهدف", "قيم مطلقة", "نطاق مستهدف", "بدون حدود"}},
		{key: "excellent_threshold", ar: "حد التميز", en: "Excellent Threshold", width: 16},
		{key: "achieved_threshold", ar: "حد التحقق", en: "Achieved Threshold", width: 16},
		{key: "warning_threshold", ar: "حد التحذير", en: "Warning Threshold", width: 16},
		{key: "target_range_min", ar: "أدنى قيمة للنطاق", en: "Target Range Min", width: 16},
		{key: "target_range_max", ar: "أعلى قيمة للنطاق", en: "Target Range Max", width: 16},
		{key: "target_status", ar: "حالة المستهدف", en: "Target Status", width: 16,
			values: []string{"draft", "submitted"}, valuesAr: []string{"مسودة", "مقدم"}},
		{key: "effective_from", ar: "ساري من (YYYY-MM-DD)", en: "Effective From (YYYY-MM-DD)", width: 20},
		{key: "effective_to", ar: "ساري حتى (YYYY-MM-DD)", en: "Effective To (YYYY-MM-DD)", width: 20},
	}
}

func markKpiTargetTemplateRequired(cols []kpiTemplateColumn) []kpiTemplateColumn {
	required := make(map[string]bool, len(cols))
	for _, s := range kpiTargetColumns() {
		required[s.key] = s.required
	}
	for i := range cols {
		cols[i].required = required[cols[i].key]
	}
	return cols
}

func kpiTargetColumnLabel(key string) string {
	for _, c := range kpiTargetTemplateColumns() {
		if c.key == key {
			return c.en
		}
	}
	return key
}

func buildKpiTargetsInstructions() *utils.InstructionConfig {
	text := func(en, ar string) utils.LocalizedText { return utils.LocalizedText{EN: en, AR: ar} }
	item := func(labelEN, labelAR, bodyEN, bodyAR string) utils.InstructionItem {
		return utils.InstructionItem{Label: text(labelEN, labelAR), Text: text(bodyEN, bodyAR)}
	}
	return &utils.InstructionConfig{LabelWidth: 30, TextWidth: 110, Sections: []utils.InstructionSection{
		{Items: []utils.InstructionItem{item("Purpose", "الغرض", "This workbook creates or updates targets for existing KPIs, optionally attached to one of the KPI's metrics. The KPI itself must already exist in the KPI Dictionary.", "ينشئ هذا المصنف مستهدفات لمؤشرات موجودة أو يحدّثها، مع إمكانية ربطها بأحد مقاييس المؤشر. يجب أن يكون المؤشر نفسه موجوداً مسبقاً في دليل مؤشرات الأداء.")}, BlankAfter: true},
		{Title: text("How to use", "طريقة الاستخدام"), Items: []utils.InstructionItem{
			item("1. One row per target", "1. صف واحد لكل مستهدف", "Fill the KPI Targets sheet starting at row 2. One target per KPI, period and metric combination.", "املأ ورقة مستهدفات المؤشرات بدءاً من الصف 2. مستهدف واحد لكل مؤشر وفترة ومقياس."),
			item("2. Save and upload", "2. الحفظ والرفع", "Save as .xlsx, upload it, click Validate to review the report and preview, then Commit.", "احفظ الملف بصيغة .xlsx، وارفعه، ثم اضغط تحقق لمراجعة التقرير والمعاينة، ثم تنفيذ."),
		}, BlankAfter: true},
		{Title: text("Rules", "القواعد"), Items: []utils.InstructionItem{
			item("Mandatory columns", "الأعمدة المطلوبة", "Columns marked with a red * are mandatory: KPI Code, KPI Type, Target Value and Target Type. Target Year and Period Code are also required for every target that is not a Milestone / Ad Hoc target.", "الأعمدة المميزة بعلامة * الحمراء مطلوبة: كود المؤشر ونوع المؤشر وقيمة المستهدف ونوع المستهدف. كما تكون السنة المستهدفة ورمز الفترة مطلوبتين لكل مستهدف ليس من نوع مرحلة / حسب الحاجة."),
			item("KPI resolution", "مطابقة المؤشر", "KPI Code must match an existing strategic, operational or award KPI (case-insensitive). KPI Type must match that KPI's type.", "يجب أن يطابق كود المؤشر مؤشراً استراتيجياً أو تشغيلياً أو خاصاً بالجائزة موجوداً (دون حساسية لحالة الأحرف). ويجب أن يطابق نوع المؤشر نوع ذلك المؤشر."),
			item("Metric", "المقياس", "Metric Code and Metric Name are optional. If both are given they must identify the same metric under the KPI. Leave both blank for a KPI-level target.", "كود المقياس واسم المقياس اختياريان. وإذا ذُكرا معاً فيجب أن يشيرا إلى المقياس نفسه ضمن المؤشر. اتركهما فارغين لمستهدف على مستوى المؤشر."),
			item("Period", "الفترة", "Period Code must match the KPI's or metric's reporting frequency (monthly: jan..dec; quarterly: q1..q4; semi-annual: h1/h2; annual: annual or the year). For a Milestone / Ad Hoc target leave Period Code and Target Year blank and fill Period Start and Period End; the period code is derived from those dates.", "يجب أن يطابق رمز الفترة تكرار الإبلاغ للمؤشر أو المقياس (شهري: jan..dec؛ ربع سنوي: q1..q4؛ نصف سنوي: h1/h2؛ سنوي: annual أو السنة). ولمستهدف مرحلة / حسب الحاجة اترك رمز الفترة والسنة المستهدفة فارغين واملأ بداية الفترة ونهايتها؛ يُشتق رمز الفترة من هذين التاريخين."),
			item("Milestone dates", "تواريخ المرحلة", "Period Start and Period End are YYYY-MM-DD and only apply to Milestone / Ad Hoc targets.", "بداية الفترة ونهايتها بصيغة YYYY-MM-DD وتخصان مستهدفات مرحلة / حسب الحاجة فقط."),
			item("Status", "الحالة", "Target Status accepts draft or submitted (default draft). Approval, rejection and locking stay on the Targets page.", "تقبل حالة المستهدف مسودة أو مقدم (الافتراضي مسودة). ويبقى الاعتماد والرفض والقفل في صفحة المستهدفات."),
			item("Existing targets", "المستهدفات الحالية", "With \\\"skip\\\", an existing target with the same KPI, metric, year and period is left untouched. With \\\"update\\\", it is updated in place; only draft, returned or rejected targets can be updated — approved, submitted, locked and superseded targets are rejected.", "مع \\\"تخطي\\\" يُترك المستهدف الحالي ذو المؤشر والمقياس والسنة والفترة نفسها دون تغيير. ومع \\\"تحديث\\\" يُحدّث في مكانه؛ ولا يمكن تحديث إلا المستهدفات المسودة أو المعادة أو المرفوضة — أما المعتمدة والمقدمة والمقفلة والمستبدلة فتُرفض."),
			item("Formats", "التنسيقات", "Blank optional cells keep the existing value on update and use the form defaults on create (Target Basis: Management Decision; Threshold Mode: Use Global KPI Rules).", "تحافظ الخلايا الاختيارية الفارغة على القيمة الحالية عند التحديث وتستخدم القيم الافتراضية عند الإنشاء (أساس المستهدف: قرار إداري؛ نمط الحدود: استخدام قواعد المؤشر العامة)."),
			item("Every column is used", "كل الأعمدة مستخدمة", "Every template column is imported. Extra columns are reported as ignored; they never stop the import.", "يتم استيراد كل عمود في القالب. تُبلّغ الأعمدة الإضافية كمتجاهلة ولا توقف الاستيراد."),
			item("Validation report", "تقرير التحقق", "Every rejected row is listed with its Excel row number and reason. \\\"skip_invalid\\\" commits valid rows; \\\"abort\\\" commits nothing while any error remains.", "يُدرج كل صف مرفوض مع رقم صفه في Excel وسبب الرفض. \\\"تخطي غير الصالح\\\" ينفذ الصفوف الصالحة؛ و\\\"إلغاء\\\" لا ينفذ أي شيء ما دام هناك خطأ."),
		}, BlankAfter: true},
	}}
}

// ─── Parsing ────────────────────────────────────────────────────────────────

func (imp *kpiTargetsImporter) parseWorkbook(data []byte) error {
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

	table := imp.readTargetTable(sheet, rows, headerRow)
	if imp.structuralError {
		return nil
	}
	for i := headerRow + 1; i < len(rows); i++ {
		if targetRowEmpty(rows[i]) {
			continue
		}
		raw := func(key string) string {
			j, ok := table.cols[key]
			if !ok {
				return ""
			}
			return strings.TrimSpace(cell(rows[i], j))
		}
		imp.recs = append(imp.recs, imp.parseTargetRow(i+1, raw))
	}
	return nil
}

func (imp *kpiTargetsImporter) locateSheet(xf *excelize.File) (string, [][]string, int) {
	type candidate struct {
		sheet     string
		rows      [][]string
		headerRow int
	}
	var found []candidate
	for _, name := range xf.GetSheetList() {
		normalized := normHeader(name)
		if normalized == normHeader("Instructions") || normalized == "تعليمات" {
			continue
		}
		rows, err := xf.GetRows(name, excelize.Options{RawCellValue: true})
		if err != nil {
			imp.structural(name, fmt.Sprintf("The worksheet could not be read: %v", err))
			continue
		}
		for i := 0; i < len(rows) && i < kpiTargetsWorksheetHeaderScan; i++ {
			if isTargetHeaderRow(rows[i]) {
				found = append(found, candidate{sheet: name, rows: rows, headerRow: i})
				break
			}
		}
	}
	switch len(found) {
	case 0:
		imp.structural("", "The workbook does not contain a KPI Targets sheet — keep the template's \"KPI Targets\" worksheet and its header row.")
		return "", nil, 0
	case 1:
		return found[0].sheet, found[0].rows, found[0].headerRow
	default:
		imp.structural(found[1].sheet, fmt.Sprintf("More than one worksheet looks like the KPI Targets sheet (%q and %q) — keep only one.", found[0].sheet, found[1].sheet))
		return "", nil, 0
	}
}

func isTargetHeaderRow(row []string) bool {
	for _, h := range row {
		switch normHeader(h) {
		case "kpi code", "كود المؤشر", "target value", "قيمة المستهدف":
			return true
		}
	}
	return false
}

func (imp *kpiTargetsImporter) readTargetTable(sheet string, rows [][]string, headerRow int) *kpiTargetImportTable {
	table := &kpiTargetImportTable{sheet: sheet, headerRow: headerRow, cols: map[string]int{}}
	specs := kpiTargetColumns()
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
				matched[j] = true
				break
			}
		}
		if !matched[j] {
			imp.warn(sheet, headerRow+1, "", "", fmt.Sprintf("Column %q is not part of the KPI Targets template and will be ignored.", inputHeaderText(h)))
		}
	}
	for _, spec := range specs {
		if _, ok := table.cols[spec.key]; !ok && spec.required {
			imp.structural(sheet, fmt.Sprintf("Mandatory column %q is missing from the header row.", kpiTargetColumnLabel(spec.key)))
		}
	}
	return table
}

func targetRowEmpty(row []string) bool {
	for _, v := range row {
		if strings.TrimSpace(v) != "" {
			return false
		}
	}
	return true
}

func (imp *kpiTargetsImporter) parseTargetRow(row int, raw func(string) string) *kpiTargetImportRec {
	set := map[string]bool{}
	read := func(key string) string {
		v := raw(key)
		if v != "" {
			set[key] = true
		}
		return v
	}
	r := &kpiTargetImportRec{
		Row:        row,
		KpiCode:    read("kpi_code"),
		MetricCode: read("metric_code"),
		MetricName: read("metric_name"),
		Target: &models.KpiAnnualTarget{
			TargetType:     "Period Target",
			TargetBasis:    "Management Decision",
			ThresholdMode:  "Use Global KPI Rules",
			TargetStatus:   "draft",
			Version:        1,
			PeriodType:     "annual",
			TargetRangeMin: nil,
		},
		Set: set,
	}

	if r.KpiCode == "" {
		r.reject("KPI Code", "KPI Code is required.")
	}
	if typ := read("kpi_type"); typ == "" {
		r.reject("KPI Type", "KPI Type is required (strategic, operational or award).")
	} else if v, ok := normalizeMetricChoice(typ, kpiTargetKpiTypeChoices); !ok {
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
	if typ := read("target_type"); typ == "" {
		r.reject("Target Type", "Target Type is required (Period Target, Annual Target or Milestone / Ad Hoc).")
	} else if v, ok := normalizeMetricChoice(typ, kpiTargetTypeChoices); !ok {
		r.reject("Target Type", fmt.Sprintf("Target Type %q is not valid. Allowed: Period Target, Annual Target, Milestone / Ad Hoc.", typ))
	} else {
		r.Target.TargetType = v
	}
	applyChoice("Target Basis", "target_basis", "Management Decision", kpiTargetBasisChoices, "Strategic Plan, Previous Year, Benchmark, Regulatory Requirement, Contract / SLA, Management Decision, Forecast, Other", &r.Target.TargetBasis)
	applyChoice("Threshold Mode", "threshold_mode", "Use Global KPI Rules", kpiTargetThresholdChoices, "Use Global KPI Rules, Percentage of Target, Absolute Values, Target Range, No Thresholds", &r.Target.ThresholdMode)

	if status := read("target_status"); status != "" {
		if v, ok := normalizeMetricChoice(status, kpiTargetStatusChoices); !ok {
			r.reject("Target Status", fmt.Sprintf("Target Status %q is not valid. Import accepts draft or submitted only; approvals stay on the Targets page.", status))
		} else {
			r.Target.TargetStatus = v
		}
	}

	isMilestone := r.Target.TargetType == "Milestone / Ad Hoc"
	yearRaw := read("target_year")
	if yearRaw != "" {
		if v, ok := parseMetricInt(yearRaw); !ok {
			r.reject("Target Year", fmt.Sprintf("Target Year %q must be a whole number.", yearRaw))
		} else if v < 2020 || v > 2040 {
			r.reject("Target Year", fmt.Sprintf("Target Year %d must be between 2020 and 2040.", v))
		} else {
			r.Target.TargetYear = v
		}
	}

	periodRaw := read("period_code")
	if periodRaw != "" && isMilestone {
		r.reject("Period Code", "Period Code must be blank for a Milestone / Ad Hoc target — it is derived from Period Start and Period End.")
	} else if periodRaw != "" {
		periodRaw = strings.ToLower(strings.TrimSpace(periodRaw))
		r.Target.PeriodCode = periodRaw
		if IsCustomPeriodCode(periodRaw) {
			r.reject("Period Code", "Custom period codes are only allowed for Milestone / Ad Hoc targets; fill Period Start and Period End instead.")
		}
	}

	var start, end *time.Time
	if rawStart := read("period_start"); rawStart != "" {
		if v, ok := parseMetricDate(rawStart); !ok {
			r.reject("Period Start", fmt.Sprintf("Period Start %q must be a date in YYYY-MM-DD format.", rawStart))
		} else {
			start = v
		}
	}
	if rawEnd := read("period_end"); rawEnd != "" {
		if v, ok := parseMetricDate(rawEnd); !ok {
			r.reject("Period End", fmt.Sprintf("Period End %q must be a date in YYYY-MM-DD format.", rawEnd))
		} else {
			end = v
		}
	}
	if start != nil && end != nil && end.Before(*start) {
		r.reject("Period End", "Period End cannot be before Period Start.")
	}
	if isMilestone {
		if start == nil || end == nil {
			r.reject("Period Start", "Period Start and Period End are required for a Milestone / Ad Hoc target.")
		} else if !r.Rejected {
			r.Target.PeriodStart = start
			r.Target.PeriodEnd = end
			r.Target.PeriodCode = CustomPeriodCode(*start, *end)
			r.Target.TargetYear = start.Year()
		}
	} else {
		r.Target.PeriodStart = start
		r.Target.PeriodEnd = end
		if r.Target.PeriodCode == "" {
			r.reject("Period Code", "Period Code is required for Period and Annual targets.")
		}
		if r.Target.TargetYear == 0 {
			r.reject("Target Year", "Target Year is required for Period and Annual targets.")
		}
	}

	if rawValue := read("target_value"); rawValue == "" {
		r.reject("Target Value", "Target Value is required.")
	} else if v, ok := parseMetricFloat(rawValue); !ok {
		r.reject("Target Value", fmt.Sprintf("Target Value %q must be a number.", rawValue))
	} else {
		r.Target.TargetValue = v
	}

	r.Target.TargetRationale = read("target_rationale")

	for _, f := range []struct {
		field  string
		key    string
		target **float64
	}{
		{"Excellent Threshold", "excellent_threshold", &r.Target.ExcellentThreshold},
		{"Achieved Threshold", "achieved_threshold", &r.Target.AchievedThreshold},
		{"Warning Threshold", "warning_threshold", &r.Target.WarningThreshold},
		{"Target Range Min", "target_range_min", &r.Target.TargetRangeMin},
		{"Target Range Max", "target_range_max", &r.Target.TargetRangeMax},
	} {
		rawValue := read(f.key)
		if rawValue == "" {
			continue
		}
		if v, ok := parseMetricFloat(rawValue); !ok {
			r.reject(f.field, fmt.Sprintf("%s %q must be a number.", f.field, rawValue))
		} else {
			value := v
			*f.target = &value
		}
	}
	if r.Target.TargetRangeMin != nil && r.Target.TargetRangeMax != nil && *r.Target.TargetRangeMax < *r.Target.TargetRangeMin {
		r.reject("Target Range Max", "Target Range Max cannot be lower than Target Range Min.")
	}

	if rawValue := read("effective_from"); rawValue != "" {
		if v, ok := parseMetricDate(rawValue); !ok {
			r.reject("Effective From", fmt.Sprintf("Effective From %q must be a date in YYYY-MM-DD format.", rawValue))
		} else {
			r.Target.EffectiveFrom = v
		}
	}
	if rawValue := read("effective_to"); rawValue != "" {
		if v, ok := parseMetricDate(rawValue); !ok {
			r.reject("Effective To", fmt.Sprintf("Effective To %q must be a date in YYYY-MM-DD format.", rawValue))
		} else {
			r.Target.EffectiveTo = v
		}
	}
	if r.Target.EffectiveFrom != nil && r.Target.EffectiveTo != nil && r.Target.EffectiveTo.Before(*r.Target.EffectiveFrom) {
		r.reject("Effective To", "Effective To cannot be earlier than Effective From.")
	}

	if n := len([]rune(r.Target.TargetRationale)); n > 4000 {
		r.reject("Target Rationale", fmt.Sprintf("Target Rationale must be at most 4000 characters (got %d).", n))
	}
	return r
}

// ─── Existing data ──────────────────────────────────────────────────────────

func (imp *kpiTargetsImporter) loadExisting() error {
	db := imp.db.WithContext(imp.ctx)
	imp.existingKpis = map[string]*existingKpi{}
	imp.kpiByCode = map[string]string{}
	imp.metricsByKpi = map[string][]*models.KpiMetric{}
	imp.metricByCode = map[string]*models.KpiMetric{}
	imp.metricByName = map[string]*models.KpiMetric{}
	imp.existingTarget = map[string]*models.KpiAnnualTarget{}

	for _, t := range []struct {
		kpiType string
		model   interface{}
	}{
		{models.KPITypeStrategic, &models.StrategicKPI{}},
		{models.KPITypeOperational, &models.OperationalKPI{}},
		{models.KPITypeAward, &models.AwardKPI{}},
	} {
		var rows []struct {
			ID     uuid.UUID
			Code   string
			NameEn string
		}
		if err := db.Model(t.model).Select("id, code, name_en").Scan(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			code := strings.ToUpper(strings.TrimSpace(row.Code))
			key := t.kpiType + "|" + code
			imp.existingKpis[key] = &existingKpi{ID: row.ID, Type: t.kpiType, Code: row.Code, NameEn: row.NameEn}
			if _, ok := imp.kpiByCode[code]; !ok {
				imp.kpiByCode[code] = t.kpiType
			}
		}
	}

	var metrics []models.KpiMetric
	if err := db.Find(&metrics).Error; err != nil {
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

	var targets []models.KpiAnnualTarget
	if err := db.Where("version = ?", 1).Find(&targets).Error; err != nil {
		return err
	}
	for i := range targets {
		t := &targets[i]
		imp.allTargets = append(imp.allTargets, t)
		imp.existingTarget[kpiTargetIdentity(t.KpiType, t.KpiCode, t.TargetYear, t.PeriodCode, t.MetricID, t.OrganizationID)] = t
	}
	return nil
}

func kpiTargetIdentity(kpiType, kpiCode string, year int, periodCode string, metricID, orgID *uuid.UUID) string {
	metric := ""
	if metricID != nil {
		metric = metricID.String()
	}
	org := ""
	if orgID != nil {
		org = orgID.String()
	}
	return strings.ToLower(strings.TrimSpace(kpiType)) + "|" +
		strings.ToLower(strings.TrimSpace(kpiCode)) + "|" +
		strconv.Itoa(year) + "|" +
		strings.ToLower(strings.TrimSpace(periodCode)) + "|" +
		metric + "|" + org
}

// ─── Row validation ─────────────────────────────────────────────────────────

func (imp *kpiTargetsImporter) validateRows() {
	accepted := map[string]int{}
	var acceptedMilestones []*kpiTargetImportRec
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
		r.Target.KpiCode = ek.Code
		r.Target.KpiType = ek.Type

		group := r.KpiType + "|" + r.KpiID.String()
		metric, metricErr := imp.resolveMetric(r, group)
		if metricErr != "" {
			r.reject("Metric", metricErr)
			continue
		}
		if metric != nil {
			r.MetricID = &metric.ID
			r.MetricName = metric.Name
			r.Target.MetricID = &metric.ID
			r.Target.CalculationTypeSnapshot = metric.CalculationType
			r.Target.DirectionSnapshot = metric.Direction
			r.Target.UnitSnapshot = metric.Unit
			r.Target.DecimalPrecisionSnapshot = metric.DecimalPrecision
			r.Target.AggregationMethodSnapshot = metric.AggregationMethod
			r.Target.ReportingFrequencySnapshot = metric.ReportingFrequency
		}

		frequency := GetKPIReportingFrequency(imp.db.WithContext(imp.ctx), ek.Code, ek.Type)
		var periodErr error
		if r.Target.TargetType == "Milestone / Ad Hoc" {
			if metric != nil {
				periodErr = ValidateTargetPeriodForMetric(metric, frequency, r.Target.TargetYear, r.Target.PeriodCode)
			}
		} else if metric != nil {
			periodErr = ValidateTargetPeriodForMetric(metric, frequency, r.Target.TargetYear, r.Target.PeriodCode)
		} else {
			periodType := "annual"
			switch frequency {
			case models.KPIFrequencyMonthly:
				periodType = "month"
			case models.KPIFrequencyQuarterly:
				periodType = "quarter"
			case models.KPIFrequencySemiAnnual:
				periodType = "semi_annual"
			}
			periodErr = ValidatePeriod(frequency, periodType, r.Target.PeriodCode)
		}
		if periodErr != nil {
			r.reject("Period Code", periodErr.Error())
			continue
		}

		identity := kpiTargetIdentity(ek.Type, ek.Code, r.Target.TargetYear, r.Target.PeriodCode, r.MetricID, nil)
		if row, dup := accepted[identity]; dup {
			r.reject("Period Code", fmt.Sprintf("Duplicate target in this file — row %d targets the same KPI, metric and period.", row))
			continue
		}

		existing := imp.existingTarget[identity]
		if r.Target.TargetType == models.KpiTargetTypeMilestone {
			if other := imp.milestoneOverlap(r, existing, acceptedMilestones); other != nil {
				if other.Row > 0 {
					r.reject("Period Start", fmt.Sprintf("This date range overlaps row %d, which targets the same KPI and metric.", other.Row))
				} else {
					r.reject("Period Start", fmt.Sprintf("This date range overlaps an existing Milestone / Ad Hoc target (%s, status '%s').",
						FormatPeriodLabel(other.Target.PeriodCode, other.Target.TargetYear), other.Target.TargetStatus))
				}
				continue
			}
		}

		if existing != nil {
			r.Existing = existing
			if imp.opts.ExistingMode == KpiImportExistingUpdate {
				switch existing.TargetStatus {
				case "draft", "returned", "rejected":
					// Editable, matching UpdateTarget's rule.
				default:
					r.reject("Target Status", fmt.Sprintf("The existing target is %s and cannot be updated through import. Only draft, returned or rejected targets are editable.", existing.TargetStatus))
					continue
				}
				imp.preserveExisting(r, existing)
			}
		}
		if !r.Rejected {
			accepted[identity] = r.Row
			if r.Target.TargetType == models.KpiTargetTypeMilestone {
				acceptedMilestones = append(acceptedMilestones, r)
			}
		}
	}
}

// milestoneOverlap checks existing and in-file Milestone / Ad Hoc ranges for
// the same KPI and metric, mirroring the Targets API overlap rule.
func (imp *kpiTargetsImporter) milestoneOverlap(r *kpiTargetImportRec, matched *models.KpiAnnualTarget, accepted []*kpiTargetImportRec) *kpiTargetImportRec {
	if r.Target.PeriodStart == nil || r.Target.PeriodEnd == nil {
		return nil
	}
	for _, other := range imp.allTargets {
		if matched != nil && other.ID == matched.ID {
			continue
		}
		if other.OrganizationID != nil || !imp.sameTargetScope(r, other.KpiType, other.KpiCode, other.MetricID) {
			continue
		}
		if other.TargetStatus == "rejected" || other.TargetStatus == "superseded" {
			continue
		}
		if !IsCustomPeriodCode(other.PeriodCode) || other.PeriodStart == nil || other.PeriodEnd == nil {
			continue
		}
		if targetRangesOverlap(r.Target.PeriodStart, r.Target.PeriodEnd, other.PeriodStart, other.PeriodEnd) {
			return &kpiTargetImportRec{Target: other}
		}
	}
	for _, other := range accepted {
		if !imp.sameTargetScope(r, other.Target.KpiType, other.Target.KpiCode, other.MetricID) {
			continue
		}
		if targetRangesOverlap(r.Target.PeriodStart, r.Target.PeriodEnd, other.Target.PeriodStart, other.Target.PeriodEnd) {
			return other
		}
	}
	return nil
}

func (imp *kpiTargetsImporter) sameTargetScope(r *kpiTargetImportRec, kpiType, kpiCode string, metricID *uuid.UUID) bool {
	if !strings.EqualFold(strings.TrimSpace(kpiType), r.Target.KpiType) ||
		!strings.EqualFold(strings.TrimSpace(kpiCode), r.Target.KpiCode) {
		return false
	}
	if metricID == nil || r.MetricID == nil {
		return metricID == nil && r.MetricID == nil
	}
	return *metricID == *r.MetricID
}

func targetRangesOverlap(aStart, aEnd, bStart, bEnd *time.Time) bool {
	if aStart == nil || aEnd == nil || bStart == nil || bEnd == nil {
		return false
	}
	return !aEnd.Before(*bStart) && !aStart.After(*bEnd)
}

func (imp *kpiTargetsImporter) resolveMetric(r *kpiTargetImportRec, group string) (*models.KpiMetric, string) {
	codeRaw := r.MetricCode
	nameRaw := r.MetricName
	codeKey := strings.ToLower(strings.TrimSpace(codeRaw))
	nameKey := strings.ToLower(strings.TrimSpace(nameRaw))
	var byCode, byName *models.KpiMetric
	if codeKey != "" {
		byCode = imp.metricByCode[group+"|"+codeKey]
		if byCode == nil {
			return nil, fmt.Sprintf("Metric code %q was not found under KPI %s.", codeRaw, r.KpiCode)
		}
	}
	if nameKey != "" {
		byName = imp.metricByName[group+"|"+nameKey]
		if byName == nil {
			return nil, fmt.Sprintf("Metric name %q was not found under KPI %s.", nameRaw, r.KpiCode)
		}
	}
	if byCode != nil && byName != nil && byCode.ID != byName.ID {
		return nil, fmt.Sprintf("Metric code %q and metric name %q identify different metrics; fill only one or make them match.", codeRaw, nameRaw)
	}
	if byCode != nil {
		return byCode, ""
	}
	return byName, ""
}

// preserveExisting copies blank optional fields from the existing target.
func (imp *kpiTargetsImporter) preserveExisting(r *kpiTargetImportRec, existing *models.KpiAnnualTarget) {
	if !r.Set["target_basis"] {
		r.Target.TargetBasis = existing.TargetBasis
	}
	if !r.Set["target_rationale"] {
		r.Target.TargetRationale = existing.TargetRationale
	}
	if !r.Set["threshold_mode"] {
		r.Target.ThresholdMode = existing.ThresholdMode
	}
	if !r.Set["excellent_threshold"] {
		r.Target.ExcellentThreshold = existing.ExcellentThreshold
	}
	if !r.Set["achieved_threshold"] {
		r.Target.AchievedThreshold = existing.AchievedThreshold
	}
	if !r.Set["warning_threshold"] {
		r.Target.WarningThreshold = existing.WarningThreshold
	}
	if !r.Set["target_range_min"] {
		r.Target.TargetRangeMin = existing.TargetRangeMin
	}
	if !r.Set["target_range_max"] {
		r.Target.TargetRangeMax = existing.TargetRangeMax
	}
	if !r.Set["target_status"] {
		r.Target.TargetStatus = existing.TargetStatus
	}
	if !r.Set["effective_from"] {
		r.Target.EffectiveFrom = existing.EffectiveFrom
	}
	if !r.Set["effective_to"] {
		r.Target.EffectiveTo = existing.EffectiveTo
	}
	if r.Target.TargetType != "Milestone / Ad Hoc" {
		if !r.Set["period_start"] {
			r.Target.PeriodStart = existing.PeriodStart
		}
		if !r.Set["period_end"] {
			r.Target.PeriodEnd = existing.PeriodEnd
		}
	}
}

// ─── Result ─────────────────────────────────────────────────────────────────

func (imp *kpiTargetsImporter) finalize() {
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
		name := r.MetricName
		if name == "" {
			name = r.KpiCode
		}
		item := KpiImportItem{
			Category: kpiTargetsCategory,
			KpiType:  r.KpiType,
			RecordID: r.recordID(),
			Name:     name,
			Sheet:    imp.sheet,
			Row:      r.Row,
		}
		switch {
		case r.Rejected:
			r.action = kpiImportActionReject
			item.Action = kpiImportActionReject
			item.Reason = r.RejectWhy
		case r.Existing != nil:
			r.action = existingAction
			item.Action = existingAction
			if existingAction == kpiImportActionSkip {
				item.Reason = "target already exists (existing_mode=skip)"
			} else {
				item.Reason = "existing target will be updated"
			}
		default:
			r.action = kpiImportActionCreate
			item.Action = kpiImportActionCreate
		}
		items = append(items, item)
	}
	imp.res.Items = items
	imp.res.Categories = map[string]*KpiImportCounts{kpiTargetsCategory: {}}
	imp.res.Totals = KpiImportCounts{}
	imp.count()
	actionable := imp.res.Totals.Created + imp.res.Totals.Updated
	imp.res.CanCommit = !imp.structuralError && actionable > 0 &&
		!(imp.opts.ErrorMode == KpiImportErrorsAbort && len(imp.res.Errors) > 0)
}

func (imp *kpiTargetsImporter) count() {
	if imp.res.Categories == nil {
		imp.res.Categories = map[string]*KpiImportCounts{}
	}
	if imp.res.Categories[kpiTargetsCategory] == nil {
		imp.res.Categories[kpiTargetsCategory] = &KpiImportCounts{}
	}
	imp.res.Totals = KpiImportCounts{}
	for _, it := range imp.res.Items {
		c := imp.res.Categories[kpiTargetsCategory]
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

func (imp *kpiTargetsImporter) structural(sheet, message string) {
	imp.structuralError = true
	if sheet == "" {
		sheet = "(workbook)"
	}
	imp.res.Errors = append(imp.res.Errors, KpiImportIssue{Severity: kpiImportSeverityError, Sheet: sheet, Message: message})
}

func (imp *kpiTargetsImporter) errorf(sheet string, row int, recordID, field, message string) {
	imp.res.Errors = append(imp.res.Errors, KpiImportIssue{
		Severity: kpiImportSeverityError,
		Sheet:    sheet,
		Row:      row,
		RecordID: recordID,
		Field:    field,
		Message:  message,
	})
}

func (imp *kpiTargetsImporter) warn(sheet string, row int, recordID, field, message string) {
	imp.res.Warnings = append(imp.res.Warnings, KpiImportIssue{
		Severity: kpiImportSeverityWarning,
		Sheet:    sheet,
		Row:      row,
		RecordID: recordID,
		Field:    field,
		Message:  message,
	})
}

func (imp *kpiTargetsImporter) markRolledBack() {
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

// ─── Choices ────────────────────────────────────────────────────────────────

var (
	kpiTargetKpiTypeChoices = []metricChoice{
		mc("strategic", "strategic kpi", "استراتيجي", "مؤشر استراتيجي"),
		mc("operational", "operational kpi", "تشغيلي", "مؤشر تشغيلي"),
		mc("award", "award kpi", "جائزة", "مؤشر جائزة"),
	}
	kpiTargetTypeChoices = []metricChoice{
		mc("Period Target", "period", "مستهدف فترة"),
		mc("Annual Target", "annual", "مستهدف سنوي"),
		mc("Milestone / Ad Hoc", "milestone", "milestone / ad hoc", "مرحلة / حسب الحاجة", "مرحلة"),
	}
	kpiTargetBasisChoices = []metricChoice{
		mc("Strategic Plan", "الخطة الاستراتيجية"),
		mc("Previous Year", "العام السابق"),
		mc("Benchmark", "المعيار المرجعي"),
		mc("Regulatory Requirement", "متطلب تنظيمي"),
		mc("Contract / SLA", "عقد / اتفاقية مستوى خدمة"),
		mc("Management Decision", "قرار إداري"),
		mc("Forecast", "توقع"),
		mc("Other", "أخرى"),
	}
	kpiTargetThresholdChoices = []metricChoice{
		mc("Use Global KPI Rules", "استخدام قواعد المؤشر العامة"),
		mc("Percentage of Target", "نسبة من المستهدف"),
		mc("Absolute Values", "قيم مطلقة"),
		mc("Target Range", "نطاق مستهدف"),
		mc("No Thresholds", "بدون حدود"),
	}
	kpiTargetStatusChoices = []metricChoice{
		mc("draft", "مسودة"),
		mc("submitted", "مقدم", "submitted for approval"),
	}
)
