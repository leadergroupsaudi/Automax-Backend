package services

import (
	"fmt"

	"github.com/automax/backend/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// commit writes the validated plan; updates keep the metric ID so linked
// targets and entries stay valid.
func (imp *kpiMetricsImporter) commit(tx *gorm.DB) error {
	for _, r := range imp.recs {
		switch r.action {
		case kpiImportActionCreate:
			m := *r.Metric
			m.KpiID = r.KpiID
			m.KpiType = r.KpiType
			m.CreatedByID = imp.userID
			// New metrics start at their baseline, matching the metric form.
			m.CurrentValue = m.BaselineValue
			if err := tx.Omit(clause.Associations).Create(&m).Error; err != nil {
				return fmt.Errorf("creating metric %q for KPI %s: %w", m.Name, r.KpiCode, err)
			}
		case kpiImportActionUpdate:
			if r.ExistingID == nil {
				return fmt.Errorf("metric %q for KPI %s has no existing record to update", r.Metric.Name, r.KpiCode)
			}
			if err := tx.Model(&models.KpiMetric{}).Where("id = ?", *r.ExistingID).
				Updates(kpiMetricUpdateMap(r.Metric, r.Set)).Error; err != nil {
				return fmt.Errorf("updating metric %q for KPI %s: %w", r.Metric.Name, r.KpiCode, err)
			}
		}
	}
	return nil
}

// kpiMetricUpdateMap includes only fields present in the row; identity and
// runtime values are untouched.
func kpiMetricUpdateMap(m *models.KpiMetric, set map[string]bool) map[string]interface{} {
	updates := map[string]interface{}{"name": m.Name}
	if set["metric_code"] {
		updates["metric_code"] = m.MetricCode
	}
	if set["metric_description"] {
		updates["metric_description"] = m.MetricDescription
	}
	if set["metric_status"] {
		updates["metric_status"] = m.MetricStatus
	}
	if set["display_order"] {
		updates["display_order"] = m.DisplayOrder
	}
	if set["metric_type"] {
		updates["metric_type"] = m.MetricType
	}
	if set["unit"] {
		updates["unit"] = m.Unit
	}
	if set["custom_unit_label"] {
		updates["custom_unit_label"] = m.CustomUnitLabel
	}
	if set["baseline_value"] {
		updates["baseline_value"] = m.BaselineValue
	}
	if set["baseline_period"] {
		updates["baseline_period"] = m.BaselinePeriod
	}
	if set["weight"] {
		updates["weight"] = m.Weight
	}
	if set["formula"] {
		updates["formula"] = m.Formula
	}
	if set["calculation_type"] {
		updates["calculation_type"] = m.CalculationType
	}
	if set["direction"] {
		updates["direction"] = m.Direction
	}
	if set["decimal_precision"] {
		updates["decimal_precision"] = m.DecimalPrecision
	}
	if set["aggregation_method"] {
		updates["aggregation_method"] = m.AggregationMethod
	}
	if set["reporting_frequency"] {
		updates["reporting_frequency"] = m.ReportingFrequency
	}
	if set["numerator_label"] {
		updates["numerator_label"] = m.NumeratorLabel
	}
	if set["numerator_variable_code"] {
		updates["numerator_variable_code"] = m.NumeratorVariableCode
	}
	if set["denominator_label"] {
		updates["denominator_label"] = m.DenominatorLabel
	}
	if set["denominator_variable_code"] {
		updates["denominator_variable_code"] = m.DenominatorVariableCode
	}
	if set["direct_actual_label"] {
		updates["direct_actual_label"] = m.DirectActualLabel
	}
	if set["allow_manual_actual_override"] {
		updates["allow_manual_actual_override"] = m.AllowManualActualOverride
	}
	if set["advanced_formula_enabled"] {
		updates["advanced_formula_enabled"] = m.AdvancedFormulaEnabled
	}
	if set["formula_code"] {
		updates["formula_code"] = m.FormulaCode
	}
	if set["divide_by_zero_handling"] {
		updates["divide_by_zero_handling"] = m.DivideByZeroHandling
	}
	if set["rounding_rule"] {
		updates["rounding_rule"] = m.RoundingRule
	}
	if set["calculation_trace_required"] {
		updates["calculation_trace_required"] = m.CalculationTraceRequired
	}
	if set["metric_owner"] {
		updates["metric_owner_id"] = m.MetricOwnerID
	}
	if set["data_source"] {
		updates["data_source"] = m.DataSource
	}
	if set["evidence_required"] {
		updates["evidence_required"] = m.EvidenceRequired
	}
	if set["start_date"] {
		updates["start_date"] = m.StartDate
	}
	if set["due_date"] {
		updates["due_date"] = m.DueDate
	}
	return updates
}
