package services

// Commit plan for master-data imports: creates insert, updates modify in place
// (IDs stay valid), blank optional cells keep existing values, and record codes
// are never written.

import (
	"fmt"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (imp *masterDataImporter) commit(tx *gorm.DB) error {
	for _, r := range imp.recs {
		if r.Rejected {
			continue
		}
		switch r.action {
		case kpiImportActionCreate:
			if err := imp.createRec(tx, r); err != nil {
				return err
			}
		case kpiImportActionUpdate:
			if r.Existing == nil {
				return fmt.Errorf("%s %q has no existing record to update", imp.entityType, r.recordID())
			}
			if err := imp.updateRec(tx, r); err != nil {
				return err
			}
		}
	}
	return nil
}

func (imp *masterDataImporter) createRec(tx *gorm.DB, r *masterImportRec) error {
	v := &r.Payload
	create := tx.Omit(clause.Associations)
	wrap := func(label string, err error) error {
		if err != nil {
			return fmt.Errorf("creating %s %q: %w", label, r.recordID(), err)
		}
		return nil
	}

	switch r.EntityType {
	case KpiMasterPillar:
		item := &models.Pillar{NameEn: v.NameEn, NameAr: v.NameAr, OwnerID: v.OwnerID, IsActive: v.IsActive}
		if err := wrap("pillar", create.Create(item).Error); err != nil {
			return err
		}
		return imp.forceInactive(tx, &models.Pillar{}, item.ID, v.IsActive)
	case KpiMasterEnabler:
		item := &models.Enabler{NameEn: v.NameEn, NameAr: v.NameAr, OwnerID: v.OwnerID, IsActive: v.IsActive}
		if err := wrap("enabler", create.Create(item).Error); err != nil {
			return err
		}
		return imp.forceInactive(tx, &models.Enabler{}, item.ID, v.IsActive)
	case KpiMasterOperationalObjective:
		item := &models.OperationalObjective{
			NameEn: v.NameEn, NameAr: v.NameAr, GoalID: v.GoalID,
			PillarID: v.PillarID, EnablerID: v.EnablerID, IsActive: v.IsActive,
		}
		if err := wrap("operational objective", create.Create(item).Error); err != nil {
			return err
		}
		return imp.forceInactive(tx, &models.OperationalObjective{}, item.ID, v.IsActive)
	case KpiMasterProcess:
		item := &models.Process{
			NameEn: v.NameEn, NameAr: v.NameAr,
			OperationalObjectiveID: *v.OperationalObjectiveID,
			GoalID:                 v.GoalID,
			PillarID:               v.PillarID,
			EnablerID:              v.EnablerID,
			DepartmentID:           v.DepartmentID,
			Unit:                   r.Values["unit"],
			IsActive:               v.IsActive,
		}
		if err := wrap("process", create.Create(item).Error); err != nil {
			return err
		}
		return imp.forceInactive(tx, &models.Process{}, item.ID, v.IsActive)
	case KpiMasterInitiative:
		status := v.Status
		if status == "" {
			status = models.InitiativeStatusDraft
		}
		item := &models.Initiative{
			NameEn: v.NameEn, NameAr: v.NameAr, GoalID: v.GoalID,
			ObjectiveID: v.ObjectiveID, PillarID: v.PillarID,
			EnablerID: v.EnablerID, OwnerID: v.OwnerID, Status: status,
		}
		return wrap("initiative", create.Create(item).Error)
	case KpiMasterDomain:
		// Code is intentionally left empty so Postgres assigns it.
		item := &models.Domain{NameEn: v.NameEn, NameAr: v.NameAr, Type: v.Type, IsActive: v.IsActive}
		if err := wrap("domain", create.Create(item).Error); err != nil {
			return err
		}
		return imp.forceInactive(tx, &models.Domain{}, item.ID, v.IsActive)
	case KpiMasterAwardCriterion:
		item := &models.AwardCriterion{
			CriterionNo: v.CriterionNo, NameEn: v.NameEn, NameAr: v.NameAr, IsActive: v.IsActive,
		}
		if err := wrap("award criterion", create.Create(item).Error); err != nil {
			return err
		}
		return imp.forceInactive(tx, &models.AwardCriterion{}, item.ID, v.IsActive)
	case KpiMasterAwardSubCriterion:
		item := &models.AwardSubCriterion{
			AwardCriterionID: *v.AwardCriterionID, SubNo: v.SubNo,
			NameEn: v.NameEn, NameAr: v.NameAr, IsActive: v.IsActive,
		}
		if err := wrap("award sub-criterion", create.Create(item).Error); err != nil {
			return err
		}
		return imp.forceInactive(tx, &models.AwardSubCriterion{}, item.ID, v.IsActive)
	case KpiMasterAwardKpiMeasure:
		item := &models.AwardKpiMeasure{NameEn: v.NameEn, NameAr: v.NameAr, Version: 1}
		return wrap("award KPI measure", create.Create(item).Error)
	case KpiMasterDataSource:
		item := &models.KpiDataSource{NameEn: v.NameEn, NameAr: v.NameAr, IsActive: v.IsActive}
		if err := wrap("data source", create.Create(item).Error); err != nil {
			return err
		}
		return imp.forceInactive(tx, &models.KpiDataSource{}, item.ID, v.IsActive)
	case KpiMasterSegmentationDimension:
		item := &models.KpiSegmentationDimension{NameEn: v.NameEn, NameAr: v.NameAr, IsActive: v.IsActive}
		if err := wrap("segmentation dimension", create.Create(item).Error); err != nil {
			return err
		}
		return imp.forceInactive(tx, &models.KpiSegmentationDimension{}, item.ID, v.IsActive)
	case KpiMasterOrganization:
		item := &models.KpiOrganization{
			NameEn: v.NameEn, NameAr: v.NameAr, ContactInfo: v.ContactInfo, IsActive: v.IsActive,
		}
		if err := wrap("organization", create.Create(item).Error); err != nil {
			return err
		}
		return imp.forceInactive(tx, &models.KpiOrganization{}, item.ID, v.IsActive)
	}
	return fmt.Errorf("unsupported master-data entity type %q", r.EntityType)
}

// forceInactive writes false explicitly because GORM treats a false zero value
// as "use the default".
func (imp *masterDataImporter) forceInactive(tx *gorm.DB, model interface{}, id uuid.UUID, active bool) error {
	if active {
		return nil
	}
	if err := tx.Model(model).Where("id = ?", id).Update("is_active", false).Error; err != nil {
		return fmt.Errorf("setting is_active=false: %w", err)
	}
	return nil
}

func (imp *masterDataImporter) updateRec(tx *gorm.DB, r *masterImportRec) error {
	v := &r.Payload
	id := r.Existing.ID
	updates := map[string]interface{}{"name_en": v.NameEn}
	set := r.Set
	if set["name_ar"] {
		updates["name_ar"] = v.NameAr
	}
	if set["is_active"] {
		updates["is_active"] = v.IsActive
	}

	switch r.EntityType {
	case KpiMasterPillar:
		if set["owner"] {
			updates["owner_id"] = v.OwnerID
		}
		return imp.applyUpdates(tx, &models.Pillar{}, id, updates, r)
	case KpiMasterEnabler:
		if set["owner"] {
			updates["owner_id"] = v.OwnerID
		}
		return imp.applyUpdates(tx, &models.Enabler{}, id, updates, r)
	case KpiMasterOperationalObjective:
		// The goal is mandatory, so it is always present on a valid row.
		updates["goal_id"] = v.GoalID
		if set["pillar"] {
			updates["pillar_id"] = v.PillarID
		}
		if set["enabler"] {
			updates["enabler_id"] = v.EnablerID
		}
		return imp.applyUpdates(tx, &models.OperationalObjective{}, id, updates, r)
	case KpiMasterProcess:
		updates["operational_objective_id"] = v.OperationalObjectiveID
		updates["goal_id"] = v.GoalID
		if set["pillar"] {
			updates["pillar_id"] = v.PillarID
		}
		if set["enabler"] {
			updates["enabler_id"] = v.EnablerID
		}
		if set["department"] {
			updates["department_id"] = v.DepartmentID
		}
		if set["unit"] {
			updates["unit"] = r.Values["unit"]
		}
		return imp.applyUpdates(tx, &models.Process{}, id, updates, r)
	case KpiMasterInitiative:
		updates["goal_id"] = v.GoalID
		if set["objective"] {
			updates["objective_id"] = v.ObjectiveID
		}
		if set["pillar"] {
			updates["pillar_id"] = v.PillarID
		}
		if set["enabler"] {
			updates["enabler_id"] = v.EnablerID
		}
		if set["owner"] {
			updates["owner_id"] = v.OwnerID
		}
		if set["status"] {
			updates["status"] = v.Status
		}
		return imp.applyUpdates(tx, &models.Initiative{}, id, updates, r)
	case KpiMasterDomain:
		updates["type"] = v.Type
		return imp.applyUpdates(tx, &models.Domain{}, id, updates, r)
	case KpiMasterAwardCriterion:
		updates["criterion_no"] = v.CriterionNo
		return imp.applyUpdates(tx, &models.AwardCriterion{}, id, updates, r)
	case KpiMasterAwardSubCriterion:
		updates["award_criterion_id"] = v.AwardCriterionID
		updates["sub_no"] = v.SubNo
		return imp.applyUpdates(tx, &models.AwardSubCriterion{}, id, updates, r)
	case KpiMasterAwardKpiMeasure:
		// Optimistic-lock mirror: every import update bumps the version.
		updates["version"] = gorm.Expr("version + 1")
		return imp.applyUpdates(tx, &models.AwardKpiMeasure{}, id, updates, r)
	case KpiMasterDataSource:
		return imp.applyUpdates(tx, &models.KpiDataSource{}, id, updates, r)
	case KpiMasterSegmentationDimension:
		return imp.applyUpdates(tx, &models.KpiSegmentationDimension{}, id, updates, r)
	case KpiMasterOrganization:
		if set["contact_info"] {
			updates["contact_info"] = v.ContactInfo
		}
		return imp.applyUpdates(tx, &models.KpiOrganization{}, id, updates, r)
	}
	return fmt.Errorf("unsupported master-data entity type %q", r.EntityType)
}

func (imp *masterDataImporter) applyUpdates(tx *gorm.DB, model interface{}, id uuid.UUID, updates map[string]interface{}, r *masterImportRec) error {
	if err := tx.Model(model).Where("id = ?", id).Updates(updates).Error; err != nil {
		return fmt.Errorf("updating %s %q: %w", imp.entityType, r.recordID(), err)
	}
	return nil
}
