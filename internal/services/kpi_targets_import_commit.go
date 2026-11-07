package services

import (
	"fmt"

	"github.com/automax/backend/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// commit writes the validated plan; updates keep the target ID so linked
// entries stay valid.
func (imp *kpiTargetsImporter) commit(tx *gorm.DB) error {
	for _, r := range imp.recs {
		switch r.action {
		case kpiImportActionCreate:
			t := *r.Target
			t.KpiCode = r.KpiCode
			if t.KpiCode == "" {
				t.KpiCode = r.Target.KpiCode
			}
			t.KpiType = r.KpiType
			if t.KpiType == "" {
				t.KpiType = r.Target.KpiType
			}
			t.Version = 1
			// Legacy columns mirror target_year/period_code, matching
			// KpiAnnualTargetRequest.ToModel.
			t.Year = t.TargetYear
			t.PeriodType = "annual"
			t.PeriodKey = t.PeriodCode
			if t.TargetStatus == "" {
				t.TargetStatus = "draft"
			}
			if t.TargetType == "" {
				t.TargetType = "Period Target"
			}
			if t.TargetBasis == "" {
				t.TargetBasis = "Management Decision"
			}
			if t.ThresholdMode == "" {
				t.ThresholdMode = "Use Global KPI Rules"
			}
			if err := tx.Omit(clause.Associations).Create(&t).Error; err != nil {
				return fmt.Errorf("creating target for KPI %s period %s/%d: %w", t.KpiCode, t.PeriodCode, t.TargetYear, err)
			}
		case kpiImportActionUpdate:
			if r.Existing == nil {
				return fmt.Errorf("target for KPI %s period %s/%d has no existing record to update", r.KpiCode, r.Target.PeriodCode, r.Target.TargetYear)
			}
			if err := tx.Model(&models.KpiAnnualTarget{}).Where("id = ?", r.Existing.ID).
				Updates(kpiTargetUpdateMap(r.Target, r.Set)).Error; err != nil {
				return fmt.Errorf("updating target for KPI %s period %s/%d: %w", r.KpiCode, r.Target.PeriodCode, r.Target.TargetYear, err)
			}
		}
	}
	return nil
}

// kpiTargetUpdateMap includes only fields present in the row; identity,
// approval and runtime fields are untouched.
func kpiTargetUpdateMap(t *models.KpiAnnualTarget, set map[string]bool) map[string]interface{} {
	updates := map[string]interface{}{
		"target_value": t.TargetValue,
		"target_type":  t.TargetType,
		// Legacy mirrors stay in sync with target_year/period_code.
		"year":        t.TargetYear,
		"period_type": "annual",
		"period_key":  t.PeriodCode,
	}
	if set["target_basis"] {
		updates["target_basis"] = t.TargetBasis
	}
	if set["target_rationale"] {
		updates["target_rationale"] = t.TargetRationale
	}
	if set["threshold_mode"] {
		updates["threshold_mode"] = t.ThresholdMode
	}
	if set["excellent_threshold"] {
		updates["excellent_threshold"] = t.ExcellentThreshold
	}
	if set["achieved_threshold"] {
		updates["achieved_threshold"] = t.AchievedThreshold
	}
	if set["warning_threshold"] {
		updates["warning_threshold"] = t.WarningThreshold
	}
	if set["target_range_min"] {
		updates["target_range_min"] = t.TargetRangeMin
	}
	if set["target_range_max"] {
		updates["target_range_max"] = t.TargetRangeMax
	}
	if set["target_status"] {
		updates["target_status"] = t.TargetStatus
	}
	if set["effective_from"] {
		updates["effective_from"] = t.EffectiveFrom
	}
	if set["effective_to"] {
		updates["effective_to"] = t.EffectiveTo
	}
	if t.TargetType == models.KpiTargetTypeMilestone || set["period_start"] {
		updates["period_start"] = t.PeriodStart
	}
	if t.TargetType == models.KpiTargetTypeMilestone || set["period_end"] {
		updates["period_end"] = t.PeriodEnd
	}
	if set["metric_code"] || set["metric_name"] {
		updates["metric_id"] = t.MetricID
		updates["calculation_type_snapshot"] = t.CalculationTypeSnapshot
		updates["direction_snapshot"] = t.DirectionSnapshot
		updates["unit_snapshot"] = t.UnitSnapshot
		updates["decimal_precision_snapshot"] = t.DecimalPrecisionSnapshot
		updates["aggregation_method_snapshot"] = t.AggregationMethodSnapshot
		updates["reporting_frequency_snapshot"] = t.ReportingFrequencySnapshot
	}
	return updates
}
