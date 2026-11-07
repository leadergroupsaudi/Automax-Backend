package services

// KPI Master Data bulk import — validates and commits one entity type per
// workbook. Parent references accept a UUID or English/Arabic name, and every
// column is written or explicitly reported as ignored.

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/utils"
	"github.com/google/uuid"
	"github.com/xuri/excelize/v2"
	"gorm.io/gorm"
)

const (
	KpiMasterPillar                = "pillar"
	KpiMasterEnabler               = "enabler"
	KpiMasterOperationalObjective  = "operational-objective"
	KpiMasterProcess               = "process"
	KpiMasterInitiative            = "initiative"
	KpiMasterDomain                = "domain"
	KpiMasterAwardCriterion        = "award-criterion"
	KpiMasterAwardSubCriterion     = "award-sub-criterion"
	KpiMasterAwardKpiMeasure       = "award-kpi-measure"
	KpiMasterDataSource            = "data-source"
	KpiMasterSegmentationDimension = "segmentation-dimension"
	KpiMasterOrganization          = "organization"

	kpiMasterTemplateDataRows    = 4999
	kpiMasterWorksheetHeaderScan = 15
)

type masterEntitySpec struct {
	key   string
	sheet string
	cols  []kpiTemplateColumn
}

func masterColumn(key, en, ar string, width float64) kpiTemplateColumn {
	return kpiTemplateColumn{key: key, en: en, ar: ar, width: width}
}

func masterRequired(key, en, ar string, width float64) kpiTemplateColumn {
	c := masterColumn(key, en, ar, width)
	c.required = true
	return c
}

func masterChoiceColumn(key, en, ar string, width float64, values, valuesAr []string) kpiTemplateColumn {
	c := masterColumn(key, en, ar, width)
	c.values, c.valuesAr = values, valuesAr
	return c
}

func masterSpecFor(entityType string) (masterEntitySpec, bool) {
	switch entityType {
	case KpiMasterPillar:
		return masterEntitySpec{key: entityType, sheet: "Pillars", cols: []kpiTemplateColumn{
			masterRequired("name_en", "Name (English)", "الاسم (بالإنجليزية)", 34),
			masterColumn("name_ar", "Name (Arabic)", "الاسم (بالعربية)", 34),
			masterColumn("owner", "Owner (Department)", "المالك (الإدارة)", 28),
			masterChoiceColumn("is_active", "Active", "نشط", 14, []string{"Yes", "No"}, []string{"نعم", "لا"}),
		}}, true
	case KpiMasterEnabler:
		return masterEntitySpec{key: entityType, sheet: "Enablers", cols: []kpiTemplateColumn{
			masterRequired("name_en", "Name (English)", "الاسم (بالإنجليزية)", 34),
			masterColumn("name_ar", "Name (Arabic)", "الاسم (بالعربية)", 34),
			masterColumn("owner", "Owner (Department)", "المالك (الإدارة)", 28),
			masterChoiceColumn("is_active", "Active", "نشط", 14, []string{"Yes", "No"}, []string{"نعم", "لا"}),
		}}, true
	case KpiMasterOperationalObjective:
		return masterEntitySpec{key: entityType, sheet: "Parent Objectives", cols: []kpiTemplateColumn{
			masterRequired("name_en", "Name (English)", "الاسم (بالإنجليزية)", 38),
			masterColumn("name_ar", "Name (Arabic)", "الاسم (بالعربية)", 38),
			masterRequired("goal", "Strategic Goal", "الهدف الاستراتيجي", 34),
			masterColumn("pillar", "Pillar", "الركيزة", 26),
			masterColumn("enabler", "Enabler", "الممكّن", 26),
			masterChoiceColumn("is_active", "Active", "نشط", 14, []string{"Yes", "No"}, []string{"نعم", "لا"}),
		}}, true
	case KpiMasterProcess:
		return masterEntitySpec{key: entityType, sheet: "Operational Objectives", cols: []kpiTemplateColumn{
			masterRequired("name_en", "Name (English)", "الاسم (بالإنجليزية)", 38),
			masterColumn("name_ar", "Name (Arabic)", "الاسم (بالعربية)", 38),
			masterRequired("operational_objective", "Parent Objective", "الهدف الرئيسي", 34),
			masterRequired("goal", "Strategic Goal", "الهدف الاستراتيجي", 34),
			masterColumn("pillar", "Pillar", "الركيزة", 26),
			masterColumn("enabler", "Enabler", "الممكّن", 26),
			masterColumn("department", "Department", "الإدارة", 28),
			masterColumn("unit", "Unit", "الوحدة", 24),
			masterChoiceColumn("is_active", "Active", "نشط", 14, []string{"Yes", "No"}, []string{"نعم", "لا"}),
		}}, true
	case KpiMasterInitiative:
		return masterEntitySpec{key: entityType, sheet: "Initiatives", cols: []kpiTemplateColumn{
			masterRequired("name_en", "Name (English)", "الاسم (بالإنجليزية)", 38),
			masterColumn("name_ar", "Name (Arabic)", "الاسم (بالعربية)", 38),
			masterRequired("goal", "Strategic Goal", "الهدف الاستراتيجي", 34),
			masterColumn("objective", "Parent Objective", "الهدف الرئيسي", 34),
			masterColumn("pillar", "Pillar", "الركيزة", 26),
			masterColumn("enabler", "Enabler", "الممكّن", 26),
			masterColumn("owner", "Owner (Department)", "المالك (الإدارة)", 28),
			masterChoiceColumn("status", "Status", "الحالة", 16,
				[]string{"draft", "active", "on_hold", "complete", "cancelled"},
				[]string{"مسودة", "نشط", "معلق", "مكتمل", "ملغى"}),
		}}, true
	case KpiMasterDomain:
		return masterEntitySpec{key: entityType, sheet: "Domains", cols: []kpiTemplateColumn{
			masterColumn("code", "Code", "الرمز", 16),
			masterRequired("name_en", "Name (English)", "الاسم (بالإنجليزية)", 38),
			masterColumn("name_ar", "Name (Arabic)", "الاسم (بالعربية)", 38),
			masterRequiredChoiceColumn("type", "Domain Type", "نوع النطاق", 22,
				[]string{"strategy", "award", "strategy_and_award"},
				[]string{"استراتيجي", "جائزة", "استراتيجي وجائزة"}),
			masterChoiceColumn("is_active", "Active", "نشط", 14, []string{"Yes", "No"}, []string{"نعم", "لا"}),
		}}, true
	case KpiMasterAwardCriterion:
		return masterEntitySpec{key: entityType, sheet: "Award Criteria", cols: []kpiTemplateColumn{
			masterRequired("criterion_no", "Criterion No.", "رقم المعيار", 16),
			masterRequired("name_en", "Name (English)", "الاسم (بالإنجليزية)", 38),
			masterColumn("name_ar", "Name (Arabic)", "الاسم (بالعربية)", 38),
			masterChoiceColumn("is_active", "Active", "نشط", 14, []string{"Yes", "No"}, []string{"نعم", "لا"}),
		}}, true
	case KpiMasterAwardSubCriterion:
		return masterEntitySpec{key: entityType, sheet: "Award Sub-criteria", cols: []kpiTemplateColumn{
			masterRequired("award_criterion", "Award Criterion", "معيار الجائزة", 30),
			masterRequired("sub_no", "Sub No.", "الرقم الفرعي", 14),
			masterRequired("name_en", "Name (English)", "الاسم (بالإنجليزية)", 38),
			masterColumn("name_ar", "Name (Arabic)", "الاسم (بالعربية)", 38),
			masterChoiceColumn("is_active", "Active", "نشط", 14, []string{"Yes", "No"}, []string{"نعم", "لا"}),
		}}, true
	case KpiMasterAwardKpiMeasure:
		return masterEntitySpec{key: entityType, sheet: "Award KPI Measures", cols: []kpiTemplateColumn{
			masterColumn("code", "Code", "الرمز", 16),
			masterRequired("name_en", "Name (English)", "الاسم (بالإنجليزية)", 38),
			masterColumn("name_ar", "Name (Arabic)", "الاسم (بالعربية)", 38),
		}}, true
	case KpiMasterDataSource:
		return masterEntitySpec{key: entityType, sheet: "Data Sources", cols: []kpiTemplateColumn{
			masterRequired("name_en", "Name (English)", "الاسم (بالإنجليزية)", 38),
			masterColumn("name_ar", "Name (Arabic)", "الاسم (بالعربية)", 38),
			masterChoiceColumn("is_active", "Active", "نشط", 14, []string{"Yes", "No"}, []string{"نعم", "لا"}),
		}}, true
	case KpiMasterSegmentationDimension:
		return masterEntitySpec{key: entityType, sheet: "Segmentation Dimensions", cols: []kpiTemplateColumn{
			masterRequired("name_en", "Name (English)", "الاسم (بالإنجليزية)", 38),
			masterColumn("name_ar", "Name (Arabic)", "الاسم (بالعربية)", 38),
			masterChoiceColumn("is_active", "Active", "نشط", 14, []string{"Yes", "No"}, []string{"نعم", "لا"}),
		}}, true
	case KpiMasterOrganization:
		return masterEntitySpec{key: entityType, sheet: "Organizations", cols: []kpiTemplateColumn{
			masterRequired("name_en", "Name (English)", "الاسم (بالإنجليزية)", 38),
			masterColumn("name_ar", "Name (Arabic)", "الاسم (بالعربية)", 38),
			masterColumn("contact_info", "Contact Info", "معلومات الاتصال", 30),
			masterChoiceColumn("is_active", "Active", "نشط", 14, []string{"Yes", "No"}, []string{"نعم", "لا"}),
		}}, true
	}
	return masterEntitySpec{}, false
}

func masterRequiredChoiceColumn(key, en, ar string, width float64, values, valuesAr []string) kpiTemplateColumn {
	c := masterChoiceColumn(key, en, ar, width, values, valuesAr)
	c.required = true
	return c
}

func IsValidKpiMasterDataEntityType(entityType string) bool {
	_, ok := masterSpecFor(entityType)
	return ok
}

func KpiMasterDataTemplateFileName(entityType, lang string) string {
	name := fmt.Sprintf("kpi-master-data-%s-template", kpiMasterDataFileSlug(entityType))
	if lang == KpiTemplateLangAR {
		return name + "-ar.xlsx"
	}
	return name + ".xlsx"
}

// kpiMasterDataFileSlug names files after the Master Data tabs:
// operational-objective = Parent Objectives, process = Operational Objectives.
func kpiMasterDataFileSlug(entityType string) string {
	switch entityType {
	case KpiMasterOperationalObjective:
		return "parent-objectives"
	case KpiMasterProcess:
		return "operational-objectives"
	default:
		return entityType
	}
}

func BuildKpiMasterDataImportTemplate(entityType, lang string) ([]byte, error) {
	spec, ok := masterSpecFor(entityType)
	if !ok {
		return nil, fmt.Errorf("unknown master-data entity type %q", entityType)
	}
	language := utils.LanguageEnglish
	if lang == KpiTemplateLangAR {
		language = utils.LanguageArabic
	}
	cfg := utils.WorkbookConfig{
		Language:     language,
		Title:        fmt.Sprintf("KPI Master Data Import Template — %s", spec.sheet),
		Subject:      "Bulk import of KPI master data",
		Creator:      "Automax",
		ActiveSheet:  spec.sheet,
		Instructions: buildKpiMasterDataInstructions(spec),
		Sheets: []utils.SheetConfig{{
			Name:         spec.sheet,
			Columns:      toExcelColumns(spec.cols),
			DataRows:     kpiMasterTemplateDataRows,
			FreezeHeader: true,
		}},
	}
	return utils.GenerateExcelTemplate(cfg)
}

func buildKpiMasterDataInstructions(spec masterEntitySpec) *utils.InstructionConfig {
	text := func(en, ar string) utils.LocalizedText { return utils.LocalizedText{EN: en, AR: ar} }
	item := func(labelEN, labelAR, bodyEN, bodyAR string) utils.InstructionItem {
		return utils.InstructionItem{Label: text(labelEN, labelAR), Text: text(bodyEN, bodyAR)}
	}
	required := make([]string, 0, len(spec.cols))
	for _, col := range spec.cols {
		if col.required {
			required = append(required, col.en)
		}
	}
	requiredText := strings.Join(required, ", ")
	return &utils.InstructionConfig{LabelWidth: 30, TextWidth: 110, Sections: []utils.InstructionSection{
		{Items: []utils.InstructionItem{
			item("Purpose", "الغرض",
				fmt.Sprintf("This workbook creates or updates the %s master-data records referenced by the KPI Dictionary.", spec.sheet),
				fmt.Sprintf("ينشئ هذا المصنف سجلات %s في البيانات الرئيسية التي يستخدمها دليل مؤشرات الأداء أو يحدّثها.", spec.sheet)),
		}, BlankAfter: true},
		{Title: text("How to use", "طريقة الاستخدام"), Items: []utils.InstructionItem{
			item("1. One row per record", "1. صف واحد لكل سجل",
				fmt.Sprintf("Fill the %s sheet starting at row 2. One record per row.", spec.sheet),
				fmt.Sprintf("املأ ورقة %s بدءاً من الصف 2. سجل واحد في كل صف.", spec.sheet)),
			item("2. Save and upload", "2. الحفظ والرفع",
				"Save as .xlsx, upload it, click Validate to review the report and preview, then Commit.",
				"احفظ الملف بصيغة .xlsx، وارفه، ثم اضغط تحقق لمراجعة التقرير والمعاينة، ثم تنفيذ."),
		}, BlankAfter: true},
		{Title: text("Rules", "القواعد"), Items: []utils.InstructionItem{
			item("Mandatory columns", "الأعمدة المطلوبة",
				"Columns marked with a red * are mandatory: "+requiredText+".",
				"الأعمدة المميزة بعلامة * الحمراء مطلوبة: "+requiredText+"."),
			item("References", "المراجع",
				"Goal, pillar, enabler, parent objective, department and award-criterion cells accept either the database ID or the record's English/Arabic name.",
				"خلايا الهدف والركيزة والممكّن والهدف الرئيسي والإدارة ومعيار الجائزة تقبل معرف قاعدة البيانات أو الاسم الإنجليزي/العربي للسجل."),
			item("Existing records", "السجلات الحالية",
				"With \"skip\", a record that already exists is left untouched. With \"update\", it is updated in place; blank optional cells keep their existing values.",
				"مع \"تخطي\" يُترك السجل الموجود دون تغيير. ومع \"تحديث\" يُحدّث في مكانه؛ وتحافظ الخلايا الاختيارية الفارغة على قيمها الحالية."),
			item("Codes", "الرموز",
				"Domain and Award KPI Measure codes are assigned automatically. A Code cell is used to match an existing record; it is ignored when a new record is created.",
				"رموز النطاقات ومقاييس الجوائز تُعيّن تلقائياً. تُستخدم خلية الرمز لمطابقة سجل موجود، وتُتجاهل عند إنشاء سجل جديد."),
			item("Validation report", "تقرير التحقق",
				"Every rejected row is listed with its Excel row number and reason. \"skip_invalid\" commits valid rows; \"abort\" commits nothing while any error remains.",
				"يُدرج كل صف مرفوض مع رقم صفه في Excel وسبب الرفض. \"تخطي غير الصالح\" ينفذ الصفوف الصالحة؛ و\"إلغاء\" لا ينفذ أي شيء ما دام هناك خطأ."),
		}},
	}}
}

// ─── Parsing ────────────────────────────────────────────────────────────────

type masterIssue struct {
	field   string
	message string
}

type masterPayload struct {
	NameEn                 string
	NameAr                 string
	IsActive               bool
	OwnerID                *uuid.UUID
	GoalID                 *uuid.UUID
	PillarID               *uuid.UUID
	EnablerID              *uuid.UUID
	DepartmentID           *uuid.UUID
	ObjectiveID            *uuid.UUID
	OperationalObjectiveID *uuid.UUID
	AwardCriterionID       *uuid.UUID
	SubNo                  string
	CriterionNo            int
	Type                   string
	Status                 string
	ContactInfo            string
	Code                   string
}

type masterExisting struct {
	ID      uuid.UUID
	NameEn  string
	NameAr  string
	Code    string
	Version int
}

type masterImportRec struct {
	Row        int
	EntityType string
	Values     map[string]string
	Set        map[string]bool
	Payload    masterPayload
	Existing   *masterExisting
	Issues     []masterIssue
	Rejected   bool
	action     string
}

func (r *masterImportRec) reject(field, message string) {
	r.Rejected = true
	r.Issues = append(r.Issues, masterIssue{field: field, message: message})
}

func (r *masterImportRec) reason() string {
	parts := make([]string, 0, len(r.Issues))
	for _, is := range r.Issues {
		parts = append(parts, is.message)
	}
	return strings.Join(parts, " ")
}

func (r *masterImportRec) recordID() string {
	switch r.EntityType {
	case KpiMasterAwardCriterion:
		return strconv.Itoa(r.Payload.CriterionNo)
	case KpiMasterAwardSubCriterion:
		return r.Payload.SubNo
	default:
		return r.Payload.NameEn
	}
}

type masterImportTable struct {
	sheet     string
	headerRow int
	cols      map[string]int
}

type masterRefIndex struct {
	byID  map[uuid.UUID]uuid.UUID
	byKey map[string]uuid.UUID
}

type masterObjectiveRef struct {
	GoalID    *uuid.UUID
	PillarID  *uuid.UUID
	EnablerID *uuid.UUID
}

type masterDataImporter struct {
	db   *gorm.DB
	ctx  context.Context
	opts KpiImportOptions
	res  *KpiImportResult

	entityType      string
	spec            masterEntitySpec
	structuralError bool
	sheet           string
	recs            []*masterImportRec

	existing   map[string]*masterExisting
	refs       map[string]*masterRefIndex
	objectives map[uuid.UUID]masterObjectiveRef
	accepted   map[string]int // identity key → Excel row already accepted
}

// ─── Entry points ───────────────────────────────────────────────────────────

func ValidateKpiMasterDataImport(ctx context.Context, db *gorm.DB, entityType string, data []byte, fileName string, userID uuid.UUID, opts KpiImportOptions) (*KpiImportResult, error) {
	imp, err := buildKpiMasterDataPlan(ctx, db, entityType, data, fileName, userID, opts)
	if err != nil {
		return nil, err
	}
	return imp.res, nil
}

func CommitKpiMasterDataImport(ctx context.Context, db *gorm.DB, entityType string, data []byte, fileName string, userID uuid.UUID, opts KpiImportOptions) (*KpiImportResult, error) {
	imp, err := buildKpiMasterDataPlan(ctx, db, entityType, data, fileName, userID, opts)
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

func buildKpiMasterDataPlan(ctx context.Context, db *gorm.DB, entityType string, data []byte, fileName string, userID uuid.UUID, opts KpiImportOptions) (*masterDataImporter, error) {
	spec, ok := masterSpecFor(entityType)
	if !ok {
		return nil, fmt.Errorf("unknown master-data entity type %q", entityType)
	}
	opts.normalize()
	imp := &masterDataImporter{
		db:         db,
		ctx:        ctx,
		entityType: entityType,
		spec:       spec,
		opts:       opts,
		res: &KpiImportResult{
			FileName: fileName,
			Options:  opts,
			Errors:   []KpiImportIssue{},
			Warnings: []KpiImportIssue{},
			Items:    []KpiImportItem{},
		},
	}
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

// ─── Parser ─────────────────────────────────────────────────────────────────

func masterColumnAliases(key string) []string {
	switch key {
	case "name_en":
		return []string{"name_en", "name (english)", "english name", "name", "الاسم (بالإنجليزية)", "الاسم الانجليزي"}
	case "name_ar":
		return []string{"name_ar", "name (arabic)", "arabic name", "الاسم (بالعربية)", "الاسم العربي"}
	case "is_active":
		return []string{"is_active", "active", "is active", "نشط"}
	case "owner":
		return []string{"owner", "owner_id", "owner name", "owner (department)", "المالك", "المالك (الإدارة)"}
	case "goal":
		return []string{"goal", "goal_id", "strategic_goal", "strategic goal", "الهدف الاستراتيجي"}
	case "pillar":
		return []string{"pillar", "pillar_id", "الركيزة"}
	case "enabler":
		return []string{"enabler", "enabler_id", "الممكّن"}
	case "operational_objective", "objective":
		return []string{"operational_objective", "operational_objective_id", "operational objective", "objective", "objective_id", "parent_objective", "parent_objective_id", "parent objective", "parent objective id", "الهدف التشغيلي", "الهدف الرئيسي"}
	case "department":
		return []string{"department", "department_id", "department name", "الإدارة"}
	case "unit":
		return []string{"unit", "الوحدة"}
	case "code":
		return []string{"code", "domain_code", "award_kpi_measure_code", "الرمز"}
	case "type":
		return []string{"type", "domain_type", "domain type", "نوع النطاق"}
	case "criterion_no":
		return []string{"criterion_no", "criterionno", "criterion no.", "criterion number", "no", "رقم المعيار"}
	case "award_criterion":
		return []string{"award_criterion", "award_criterion_id", "award criterion", "criterion", "معيار الجائزة"}
	case "sub_no":
		return []string{"sub_no", "subno", "sub no.", "sub number", "الرقم الفرعي"}
	case "contact_info":
		return []string{"contact_info", "contactinfo", "contact info", "معلومات الاتصال"}
	case "status":
		return []string{"status", "الحالة"}
	}
	return []string{key}
}

func masterColSpecs(spec masterEntitySpec) []colSpec {
	specs := make([]colSpec, 0, len(spec.cols))
	for _, c := range spec.cols {
		// Accept generated headers plus legacy aliases from older exports.
		aliases := append([]string{c.en, c.ar}, masterColumnAliases(c.key)...)
		specs = append(specs, colSpec{key: c.key, match: hdrAny(aliases...), required: c.required})
	}
	return specs
}

func (imp *masterDataImporter) parseWorkbook(data []byte) error {
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
	table := imp.readTable(sheet, rows, headerRow)
	if imp.structuralError {
		return nil
	}
	specs := masterColSpecs(imp.spec)
	for i := headerRow + 1; i < len(rows); i++ {
		if masterRowEmpty(rows[i]) {
			continue
		}
		values := map[string]string{}
		set := map[string]bool{}
		for _, spec := range specs {
			j, ok := table.cols[spec.key]
			if !ok {
				continue
			}
			v := strings.TrimSpace(cell(rows[i], j))
			values[spec.key] = v
			if v != "" {
				set[spec.key] = true
			}
		}
		imp.recs = append(imp.recs, &masterImportRec{
			Row:        i + 1,
			EntityType: imp.entityType,
			Values:     values,
			Set:        set,
		})
	}
	return nil
}

func (imp *masterDataImporter) locateSheet(xf *excelize.File) (string, [][]string, int) {
	specs := masterColSpecs(imp.spec)
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
		for i := 0; i < len(rows) && i < kpiMasterWorksheetHeaderScan; i++ {
			matched, required := 0, 0
			for j, h := range rows[i] {
				_ = j
				normalized := normHeader(h)
				if normalized == "" {
					continue
				}
				for _, spec := range specs {
					if spec.match(normalized) {
						matched++
						if spec.required {
							required++
						}
						break
					}
				}
			}
			if matched >= 2 || required >= 1 {
				found = append(found, candidate{sheet: name, rows: rows, headerRow: i})
				break
			}
		}
	}
	switch len(found) {
	case 0:
		imp.structural("", fmt.Sprintf("The workbook does not contain a %s sheet — keep the template's %q worksheet and its header row.", imp.spec.sheet, imp.spec.sheet))
		return "", nil, 0
	case 1:
		return found[0].sheet, found[0].rows, found[0].headerRow
	default:
		imp.structural(found[1].sheet, fmt.Sprintf("More than one worksheet looks like the %s sheet (%q and %q) — keep only one.", imp.spec.sheet, found[0].sheet, found[1].sheet))
		return "", nil, 0
	}
}

func (imp *masterDataImporter) readTable(sheet string, rows [][]string, headerRow int) *masterImportTable {
	table := &masterImportTable{sheet: sheet, headerRow: headerRow, cols: map[string]int{}}
	specs := masterColSpecs(imp.spec)
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
			imp.warn(sheet, headerRow+1, "", "", fmt.Sprintf("Column %q is not part of the %s template and will be ignored.", inputHeaderText(h), imp.spec.sheet))
		}
	}
	for _, spec := range specs {
		if _, ok := table.cols[spec.key]; !ok && spec.required {
			imp.structural(sheet, fmt.Sprintf("Mandatory column %q is missing from the header row.", masterColumnLabel(imp.spec, spec.key)))
		}
	}
	return table
}

func masterColumnLabel(spec masterEntitySpec, key string) string {
	for _, c := range spec.cols {
		if c.key == key {
			return c.en
		}
	}
	return key
}

func masterRowEmpty(row []string) bool {
	for _, v := range row {
		if strings.TrimSpace(v) != "" {
			return false
		}
	}
	return true
}

// ─── Existing data and references ───────────────────────────────────────────

func (imp *masterDataImporter) loadExisting() error {
	db := imp.db.WithContext(imp.ctx)
	imp.existing = map[string]*masterExisting{}
	imp.refs = map[string]*masterRefIndex{}
	imp.objectives = map[uuid.UUID]masterObjectiveRef{}
	imp.accepted = map[string]int{}

	newIndex := func() *masterRefIndex {
		return &masterRefIndex{byID: map[uuid.UUID]uuid.UUID{}, byKey: map[string]uuid.UUID{}}
	}
	addRef := func(kind string, id uuid.UUID, names ...string) {
		idx := imp.refs[kind]
		if idx == nil {
			idx = newIndex()
			imp.refs[kind] = idx
		}
		idx.byID[id] = id
		for _, name := range names {
			if key := masterRefKey(name); key != "" {
				idx.byKey[key] = id
			}
		}
	}

	var departments []models.Department
	if err := db.Select("id, name, name_ar").Find(&departments).Error; err != nil {
		return err
	}
	for _, d := range departments {
		addRef("department", d.ID, d.Name, d.NameAr)
	}

	var goals []models.Goal
	if err := db.Select("id, title").Find(&goals).Error; err != nil {
		return err
	}
	for _, g := range goals {
		addRef("goal", g.ID, g.Title)
	}

	var pillars []models.Pillar
	if err := db.Select("id, name_en, name_ar").Find(&pillars).Error; err != nil {
		return err
	}
	for _, p := range pillars {
		addRef("pillar", p.ID, p.NameEn, p.NameAr)
	}

	var enablers []models.Enabler
	if err := db.Select("id, name_en, name_ar").Find(&enablers).Error; err != nil {
		return err
	}
	for _, e := range enablers {
		addRef("enabler", e.ID, e.NameEn, e.NameAr)
	}

	var objectives []models.OperationalObjective
	if err := db.Select("id, name_en, name_ar, goal_id, pillar_id, enabler_id").Find(&objectives).Error; err != nil {
		return err
	}
	for _, o := range objectives {
		addRef("objective", o.ID, o.NameEn, o.NameAr)
		imp.objectives[o.ID] = masterObjectiveRef{GoalID: o.GoalID, PillarID: o.PillarID, EnablerID: o.EnablerID}
	}

	var criteria []models.AwardCriterion
	if err := db.Select("id, criterion_no, name_en, name_ar").Find(&criteria).Error; err != nil {
		return err
	}
	for _, c := range criteria {
		addRef("criterion", c.ID, c.NameEn, c.NameAr, strconv.Itoa(c.CriterionNo))
	}

	addExisting := func(me *masterExisting, keys ...string) {
		for _, key := range keys {
			if key != "" {
				imp.existing[key] = me
			}
		}
	}

	switch imp.entityType {
	case KpiMasterPillar:
		var items []models.Pillar
		if err := db.Select("id, name_en, name_ar, is_active").Find(&items).Error; err != nil {
			return err
		}
		for _, item := range items {
			addExisting(&masterExisting{ID: item.ID, NameEn: item.NameEn, NameAr: item.NameAr}, "pillar|"+masterRefKey(item.NameEn))
		}
	case KpiMasterEnabler:
		var items []models.Enabler
		if err := db.Select("id, name_en, name_ar, is_active").Find(&items).Error; err != nil {
			return err
		}
		for _, item := range items {
			addExisting(&masterExisting{ID: item.ID, NameEn: item.NameEn, NameAr: item.NameAr}, "enabler|"+masterRefKey(item.NameEn))
		}
	case KpiMasterOperationalObjective:
		var items []models.OperationalObjective
		if err := db.Select("id, name_en, name_ar, goal_id, is_active").Find(&items).Error; err != nil {
			return err
		}
		for _, item := range items {
			goal := ""
			if item.GoalID != nil {
				goal = item.GoalID.String()
			}
			addExisting(&masterExisting{ID: item.ID, NameEn: item.NameEn, NameAr: item.NameAr},
				"operational-objective|"+goal+"|"+masterRefKey(item.NameEn))
		}
	case KpiMasterProcess:
		var items []models.Process
		if err := db.Select("id, name_en, name_ar, operational_objective_id, is_active").Find(&items).Error; err != nil {
			return err
		}
		for _, item := range items {
			addExisting(&masterExisting{ID: item.ID, NameEn: item.NameEn, NameAr: item.NameAr},
				"process|"+item.OperationalObjectiveID.String()+"|"+masterRefKey(item.NameEn))
		}
	case KpiMasterInitiative:
		var items []models.Initiative
		if err := db.Select("id, name_en, name_ar, goal_id, status").Find(&items).Error; err != nil {
			return err
		}
		for _, item := range items {
			goal := ""
			if item.GoalID != nil {
				goal = item.GoalID.String()
			}
			addExisting(&masterExisting{ID: item.ID, NameEn: item.NameEn, NameAr: item.NameAr},
				"initiative|"+goal+"|"+masterRefKey(item.NameEn))
		}
	case KpiMasterDomain:
		var items []models.Domain
		if err := db.Select("id, code, name_en, name_ar, is_active").Find(&items).Error; err != nil {
			return err
		}
		for _, item := range items {
			me := &masterExisting{ID: item.ID, NameEn: item.NameEn, NameAr: item.NameAr, Code: item.Code}
			addExisting(me, "domain|name|"+masterRefKey(item.NameEn), "domain|code|"+masterRefKey(item.Code))
		}
	case KpiMasterAwardCriterion:
		var items []models.AwardCriterion
		if err := db.Select("id, criterion_no, name_en, name_ar, is_active").Find(&items).Error; err != nil {
			return err
		}
		for _, item := range items {
			addExisting(&masterExisting{ID: item.ID, NameEn: item.NameEn, NameAr: item.NameAr, Code: strconv.Itoa(item.CriterionNo)},
				"award-criterion|"+strconv.Itoa(item.CriterionNo))
		}
	case KpiMasterAwardSubCriterion:
		var items []models.AwardSubCriterion
		if err := db.Select("id, name_en, name_ar, award_criterion_id, sub_no, is_active").Find(&items).Error; err != nil {
			return err
		}
		for _, item := range items {
			addExisting(&masterExisting{ID: item.ID, NameEn: item.NameEn, NameAr: item.NameAr, Code: item.SubNo},
				"award-sub-criterion|"+item.AwardCriterionID.String()+"|"+masterRefKey(item.SubNo))
		}
	case KpiMasterAwardKpiMeasure:
		var items []models.AwardKpiMeasure
		if err := db.Select("id, code, name_en, name_ar, version").Find(&items).Error; err != nil {
			return err
		}
		for _, item := range items {
			addExisting(&masterExisting{ID: item.ID, NameEn: item.NameEn, NameAr: item.NameAr, Code: item.Code, Version: item.Version},
				"award-kpi-measure|name|"+masterRefKey(item.NameEn), "award-kpi-measure|code|"+masterRefKey(item.Code))
		}
	case KpiMasterDataSource:
		var items []models.KpiDataSource
		if err := db.Select("id, name_en, name_ar, is_active").Find(&items).Error; err != nil {
			return err
		}
		for _, item := range items {
			addExisting(&masterExisting{ID: item.ID, NameEn: item.NameEn, NameAr: item.NameAr},
				"data-source|"+masterRefKey(item.NameEn))
		}
	case KpiMasterSegmentationDimension:
		var items []models.KpiSegmentationDimension
		if err := db.Select("id, name_en, name_ar, is_active").Find(&items).Error; err != nil {
			return err
		}
		for _, item := range items {
			addExisting(&masterExisting{ID: item.ID, NameEn: item.NameEn, NameAr: item.NameAr},
				"segmentation-dimension|"+masterRefKey(item.NameEn))
		}
	case KpiMasterOrganization:
		var items []models.KpiOrganization
		if err := db.Select("id, name_en, name_ar, is_active").Find(&items).Error; err != nil {
			return err
		}
		for _, item := range items {
			addExisting(&masterExisting{ID: item.ID, NameEn: item.NameEn, NameAr: item.NameAr},
				"organization|"+masterRefKey(item.NameEn))
		}
	}
	return nil
}

func masterRefKey(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func (imp *masterDataImporter) validateRows() {
	for _, r := range imp.recs {
		imp.validateRec(r)
	}
}

func (imp *masterDataImporter) validateRec(r *masterImportRec) {
	v := &r.Payload
	reject := func(field, message string) { r.reject(field, message) }
	read := func(key string) string { return r.Values[key] }

	v.NameEn = strings.TrimSpace(read("name_en"))
	v.NameAr = strings.TrimSpace(read("name_ar"))
	if v.NameEn == "" {
		reject("Name (English)", "Name (English) is required.")
	} else if utf8.RuneCountInString(v.NameEn) > 255 {
		reject("Name (English)", "Name (English) must be at most 255 characters.")
	}
	if utf8.RuneCountInString(v.NameAr) > 255 {
		reject("Name (Arabic)", "Name (Arabic) must be at most 255 characters.")
	}
	v.IsActive = true
	if raw := read("is_active"); raw != "" {
		active, ok := parseMetricBool(raw, true)
		if !ok {
			reject("Active", fmt.Sprintf("Active %q must be Yes or No.", raw))
		} else {
			v.IsActive = active
		}
	}
	if r.EntityType == KpiMasterInitiative {
		v.Status = "draft"
		if raw := read("status"); raw != "" {
			if status, ok := normalizeMetricChoice(raw, masterInitiativeStatusChoices); ok {
				v.Status = status
			} else {
				reject("Status", fmt.Sprintf("Status %q is not valid. Allowed: draft, active, on_hold, complete, cancelled.", raw))
			}
		}
	}
	if r.EntityType == KpiMasterDomain {
		if raw := read("type"); raw != "" {
			if domainType, ok := normalizeMetricChoice(raw, masterDomainTypeChoices); ok {
				v.Type = domainType
			} else {
				reject("Domain Type", fmt.Sprintf("Domain Type %q is not valid. Allowed: strategy, award, strategy_and_award.", raw))
			}
		} else {
			reject("Domain Type", "Domain Type is required (strategy, award or strategy_and_award).")
		}
		v.Code = strings.TrimSpace(read("code"))
	}
	if r.EntityType == KpiMasterAwardKpiMeasure {
		v.Code = strings.TrimSpace(read("code"))
	}
	if r.EntityType == KpiMasterOrganization {
		v.ContactInfo = strings.TrimSpace(read("contact_info"))
		if utf8.RuneCountInString(v.ContactInfo) > 255 {
			reject("Contact Info", "Contact Info must be at most 255 characters.")
		}
	}
	if r.EntityType == KpiMasterAwardCriterion {
		raw := strings.TrimSpace(read("criterion_no"))
		if raw == "" {
			reject("Criterion No.", "Criterion No. is required.")
		} else {
			no, err := strconv.Atoi(raw)
			if err != nil || no <= 0 {
				reject("Criterion No.", fmt.Sprintf("Criterion No. %q must be a positive whole number.", raw))
			} else {
				v.CriterionNo = no
			}
		}
	}
	if r.EntityType == KpiMasterAwardSubCriterion {
		v.SubNo = strings.TrimSpace(read("sub_no"))
		if v.SubNo == "" {
			reject("Sub No.", "Sub No. is required.")
		} else if utf8.RuneCountInString(v.SubNo) > 20 {
			reject("Sub No.", "Sub No. must be at most 20 characters.")
		}
	}

	resolve := func(kind, raw, field string, required bool) *uuid.UUID {
		if raw = strings.TrimSpace(raw); raw != "" {
			if id, err := uuid.Parse(raw); err == nil {
				if idx := imp.refs[kind]; idx != nil {
					if _, ok := idx.byID[id]; ok {
						return &id
					}
				}
			}
			if idx := imp.refs[kind]; idx != nil {
				if id, ok := idx.byKey[masterRefKey(raw)]; ok {
					return &id
				}
			}
			reject(field, fmt.Sprintf("%s %q was not found. Use its ID or its English/Arabic name.", field, raw))
			return nil
		}
		if required {
			reject(field, fmt.Sprintf("%s is required.", field))
		}
		return nil
	}

	switch r.EntityType {
	case KpiMasterPillar, KpiMasterEnabler:
		v.OwnerID = resolve("department", read("owner"), "Owner", false)
	case KpiMasterOperationalObjective:
		v.GoalID = resolve("goal", read("goal"), "Strategic Goal", true)
		v.PillarID = resolve("pillar", read("pillar"), "Pillar", false)
		v.EnablerID = resolve("enabler", read("enabler"), "Enabler", false)
	case KpiMasterProcess:
		v.OperationalObjectiveID = resolve("objective", read("operational_objective"), "Parent Objective", true)
		v.GoalID = resolve("goal", read("goal"), "Strategic Goal", false)
		v.PillarID = resolve("pillar", read("pillar"), "Pillar", false)
		v.EnablerID = resolve("enabler", read("enabler"), "Enabler", false)
		v.DepartmentID = resolve("department", read("department"), "Department", false)
		if v.OperationalObjectiveID != nil {
			if ref, ok := imp.objectives[*v.OperationalObjectiveID]; ok {
				if v.GoalID == nil && read("goal") == "" {
					v.GoalID = ref.GoalID
				}
				if v.PillarID == nil && read("pillar") == "" {
					v.PillarID = ref.PillarID
				}
				if v.EnablerID == nil && read("enabler") == "" {
					v.EnablerID = ref.EnablerID
				}
			}
		}
		if v.GoalID == nil && read("goal") == "" {
			reject("Strategic Goal", "Strategic Goal is required.")
		}
	case KpiMasterInitiative:
		v.GoalID = resolve("goal", read("goal"), "Strategic Goal", true)
		v.ObjectiveID = resolve("objective", read("objective"), "Parent Objective", false)
		v.PillarID = resolve("pillar", read("pillar"), "Pillar", false)
		v.EnablerID = resolve("enabler", read("enabler"), "Enabler", false)
		v.OwnerID = resolve("department", read("owner"), "Owner", false)
	case KpiMasterAwardSubCriterion:
		v.AwardCriterionID = resolve("criterion", read("award_criterion"), "Award Criterion", true)
	}

	if r.Rejected {
		return
	}

	keys := imp.identityKeys(r)
	r.Existing = imp.lookupExisting(r)
	for _, key := range keys {
		if row, dup := imp.accepted[key]; dup {
			r.reject("Name (English)", fmt.Sprintf("Duplicate record in this file — row %d has the same identity.", row))
			return
		}
	}
	if r.Existing != nil {
		for _, key := range keys {
			imp.accepted[key] = r.Row
		}
		return
	}
	for _, key := range keys {
		imp.accepted[key] = r.Row
	}
}

func (imp *masterDataImporter) identityKeys(r *masterImportRec) []string {
	name := "name|" + masterRefKey(r.Payload.NameEn)
	switch r.EntityType {
	case KpiMasterDomain:
		keys := []string{"domain|" + name}
		if r.Payload.Code != "" {
			keys = append(keys, "domain|code|"+masterRefKey(r.Payload.Code))
		}
		return keys
	case KpiMasterAwardKpiMeasure:
		keys := []string{"award-kpi-measure|" + name}
		if r.Payload.Code != "" {
			keys = append(keys, "award-kpi-measure|code|"+masterRefKey(r.Payload.Code))
		}
		return keys
	case KpiMasterOperationalObjective:
		return []string{"operational-objective|" + uuidString(r.Payload.GoalID) + "|" + masterRefKey(r.Payload.NameEn)}
	case KpiMasterProcess:
		return []string{"process|" + uuidString(r.Payload.OperationalObjectiveID) + "|" + masterRefKey(r.Payload.NameEn)}
	case KpiMasterInitiative:
		return []string{"initiative|" + uuidString(r.Payload.GoalID) + "|" + masterRefKey(r.Payload.NameEn)}
	case KpiMasterAwardCriterion:
		return []string{"award-criterion|" + strconv.Itoa(r.Payload.CriterionNo)}
	case KpiMasterAwardSubCriterion:
		return []string{"award-sub-criterion|" + uuidString(r.Payload.AwardCriterionID) + "|" + masterRefKey(r.Payload.SubNo)}
	default:
		return []string{r.EntityType + "|" + masterRefKey(r.Payload.NameEn)}
	}
}

func uuidString(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return id.String()
}

func (imp *masterDataImporter) lookupExisting(r *masterImportRec) *masterExisting {
	for _, key := range imp.identityKeys(r) {
		if found := imp.existing[key]; found != nil {
			return found
		}
	}
	return nil
}

// ─── Finalize ───────────────────────────────────────────────────────────────

func (imp *masterDataImporter) finalize() {
	existingAction := kpiImportActionSkip
	if imp.opts.ExistingMode == KpiImportExistingUpdate {
		existingAction = kpiImportActionUpdate
	}
	for _, r := range imp.recs {
		if r.Rejected {
			for _, issue := range r.Issues {
				imp.errorf(imp.sheet, r.Row, r.recordID(), issue.field, issue.message)
			}
		}
		item := KpiImportItem{
			Category: imp.entityType,
			RecordID: r.recordID(),
			Name:     r.Payload.NameEn,
			Sheet:    imp.sheet,
			Row:      r.Row,
		}
		switch {
		case r.Rejected:
			r.action = kpiImportActionReject
			item.Action = kpiImportActionReject
			item.Reason = r.reason()
		case r.Existing != nil:
			r.action = existingAction
			item.Action = existingAction
			if existingAction == kpiImportActionSkip {
				item.Reason = "record already exists (existing_mode=skip)"
			} else {
				item.Reason = "existing record will be updated"
			}
			if (r.EntityType == KpiMasterDomain || r.EntityType == KpiMasterAwardKpiMeasure) && r.Payload.Code != "" {
				imp.warn(imp.sheet, r.Row, r.recordID(), "Code", "Code is used to match existing records; it is not changed by the update.")
			}
		default:
			r.action = kpiImportActionCreate
			item.Action = kpiImportActionCreate
			if (r.EntityType == KpiMasterDomain || r.EntityType == KpiMasterAwardKpiMeasure) && r.Payload.Code != "" {
				imp.warn(imp.sheet, r.Row, r.recordID(), "Code", "Code is assigned automatically; the Code cell is ignored when creating a record.")
			}
		}
		imp.res.Items = append(imp.res.Items, item)
	}
	imp.count()
	actionable := imp.res.Totals.Created + imp.res.Totals.Updated
	imp.res.CanCommit = !imp.structuralError && actionable > 0 &&
		!(imp.opts.ErrorMode == KpiImportErrorsAbort && len(imp.res.Errors) > 0)
}

func (imp *masterDataImporter) count() {
	imp.res.Categories = map[string]*KpiImportCounts{imp.entityType: {}}
	imp.res.Totals = KpiImportCounts{}
	c := imp.res.Categories[imp.entityType]
	for _, it := range imp.res.Items {
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

func (imp *masterDataImporter) structural(sheet, message string) {
	imp.structuralError = true
	if sheet == "" {
		sheet = "(workbook)"
	}
	imp.res.Errors = append(imp.res.Errors, KpiImportIssue{Severity: kpiImportSeverityError, Sheet: sheet, Message: message})
}

func (imp *masterDataImporter) errorf(sheet string, row int, recordID, field, message string) {
	imp.res.Errors = append(imp.res.Errors, KpiImportIssue{
		Severity: kpiImportSeverityError, Sheet: sheet, Row: row,
		RecordID: recordID, Field: field, Message: message,
	})
}

func (imp *masterDataImporter) warn(sheet string, row int, recordID, field, message string) {
	imp.res.Warnings = append(imp.res.Warnings, KpiImportIssue{
		Severity: kpiImportSeverityWarning, Sheet: sheet, Row: row,
		RecordID: recordID, Field: field, Message: message,
	})
}

func (imp *masterDataImporter) markRolledBack() {
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

var (
	masterDomainTypeChoices = []metricChoice{
		mc("strategy", "strategic", "استراتيجي", "استراتيجية"),
		mc("award", "جائزة"),
		mc("strategy_and_award", "strategy and award", "استراتيجي وجائزة"),
	}
	masterInitiativeStatusChoices = []metricChoice{
		mc("draft", "مسودة"),
		mc("active", "نشط"),
		mc("on_hold", "on hold", "معلق"),
		mc("complete", "completed", "مكتمل"),
		mc("cancelled", "canceled", "ملغى"),
	}
)
