package repository

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type WorkflowRepository interface {
	// Workflow CRUD
	Create(ctx context.Context, workflow *models.Workflow) error
	FindByID(ctx context.Context, id uuid.UUID) (*models.Workflow, error)
	FindByIDWithRelations(ctx context.Context, id uuid.UUID) (*models.Workflow, error)
	FindByCode(ctx context.Context, code string) (*models.Workflow, error)
	List(ctx context.Context, activeOnly bool) ([]models.Workflow, error)
	ListByRecordType(ctx context.Context, recordType string, activeOnly bool) ([]models.Workflow, error)
	// ListByRecordTypeID returns the ACTIVE workflows bound to one EO record
	// type, matched on the record_type_id column (which models.Workflow
	// indexes) and never on the record_type STRING, whose 'all' and 'both'
	// wildcards would hand an EO type an incident workflow.
	//
	// It exists so services.ResolveWorkflowForType — called on every EO
	// create, every transition and every record-detail load — does not have to
	// List every active workflow in the database with seven preloads each and
	// then filter in Go for one id. Only the two relations the EO engine reads
	// off the workflow are preloaded: States (the initial and target states)
	// and Transitions (the candidate set). A transition's own roles are loaded
	// per transition by FindTransitionByIDWithRelations.
	ListByRecordTypeID(ctx context.Context, recordTypeID uuid.UUID) ([]models.Workflow, error)
	ListFiltered(ctx context.Context, filter *models.WorkflowFilter) ([]models.Workflow, int64, error)
	Update(ctx context.Context, workflow *models.Workflow) error
	Delete(ctx context.Context, id uuid.UUID) error
	HardDelete(ctx context.Context, id uuid.UUID) error         // Permanently delete with cascading
	ListDeleted(ctx context.Context) ([]models.Workflow, error) // List soft-deleted workflows
	Restore(ctx context.Context, id uuid.UUID) error            // Restore a soft-deleted workflow
	ExistsByCodeOrName(ctx context.Context, codeOrName []string) (bool, error)
	ExistsByName(ctx context.Context, name string) (bool, error)
	// Workflow-Classification assignments
	AssignClassifications(ctx context.Context, workflowID uuid.UUID, classificationIDs []uuid.UUID) error
	GetByClassificationID(ctx context.Context, classificationID uuid.UUID) (*models.Workflow, error)
	GetDefaultWorkflow(ctx context.Context) (*models.Workflow, error)

	// Workflow-Location assignments
	AssignLocations(ctx context.Context, workflowID uuid.UUID, locationIDs []uuid.UUID) error

	// Duplicate rule detection
	FindWorkflowsByRecordTypeAndClassifications(ctx context.Context, recordType string, classificationIDs []uuid.UUID, locationIDs []uuid.UUID, excludeWorkflowID *uuid.UUID) ([]models.Workflow, error)

	// Workflow ConvertToRequest role assignments
	AssignConvertToRequestRoles(ctx context.Context, workflowID uuid.UUID, roleIDs []uuid.UUID) error

	// Workflow MergeAllowedRoles assignments
	AssignMergeAllowedRoles(ctx context.Context, workflowID uuid.UUID, roleIDs []uuid.UUID) error
	GetMergeAllowedRoles(ctx context.Context, workflowID uuid.UUID) ([]models.Role, error)

	// WorkflowState CRUD
	CreateState(ctx context.Context, state *models.WorkflowState) error
	FindStateByID(ctx context.Context, id uuid.UUID) (*models.WorkflowState, error)
	FindStateByCode(ctx context.Context, workflowID uuid.UUID, code string) (*models.WorkflowState, error)
	ListStatesByWorkflowID(ctx context.Context, workflowID uuid.UUID) ([]models.WorkflowState, error)
	UpdateState(ctx context.Context, state *models.WorkflowState) error
	DeleteState(ctx context.Context, id uuid.UUID) error
	GetInitialState(ctx context.Context, workflowID uuid.UUID) (*models.WorkflowState, error)

	// WorkflowTransition CRUD
	CreateTransition(ctx context.Context, transition *models.WorkflowTransition) error
	FindTransitionByID(ctx context.Context, id uuid.UUID) (*models.WorkflowTransition, error)
	FindTransitionByIDWithRelations(ctx context.Context, id uuid.UUID) (*models.WorkflowTransition, error)
	ListTransitionsByWorkflowID(ctx context.Context, workflowID uuid.UUID) ([]models.WorkflowTransition, error)
	ListTransitionsFromState(ctx context.Context, stateID uuid.UUID) ([]models.WorkflowTransition, error)
	ListTransitionsToState(ctx context.Context, stateID uuid.UUID) ([]models.WorkflowTransition, error)
	UpdateTransition(ctx context.Context, transition *models.WorkflowTransition) error
	DeleteTransition(ctx context.Context, id uuid.UUID) error

	// Transition role assignments
	AssignTransitionRoles(ctx context.Context, transitionID uuid.UUID, roleIDs []uuid.UUID) error
	AssignTransitionAssignmentRoles(ctx context.Context, transitionID uuid.UUID, roleIDs []uuid.UUID) error

	// State viewable role assignments
	AssignStateViewableRoles(ctx context.Context, stateID uuid.UUID, roleIDs []uuid.UUID) error

	// State editable role assignments
	AssignStateEditableRoles(ctx context.Context, stateID uuid.UUID, roleIDs []uuid.UUID) error

	// State creation-time assignment roles
	AssignStateAssignmentRoles(ctx context.Context, stateID uuid.UUID, roleIDs []uuid.UUID) error

	// TransitionRequirement CRUD
	SetTransitionRequirements(ctx context.Context, transitionID uuid.UUID, requirements []models.TransitionRequirement) error
	GetTransitionRequirements(ctx context.Context, transitionID uuid.UUID) ([]models.TransitionRequirement, error)

	// TransitionAction CRUD
	SetTransitionActions(ctx context.Context, transitionID uuid.UUID, actions []models.TransitionAction) error
	GetTransitionActions(ctx context.Context, transitionID uuid.UUID) ([]models.TransitionAction, error)

	// TransitionFieldChange CRUD
	SetTransitionFieldChanges(ctx context.Context, transitionID uuid.UUID, fieldChanges []models.TransitionFieldChange) error
	GetTransitionFieldChanges(ctx context.Context, transitionID uuid.UUID) ([]models.TransitionFieldChange, error)
	GetTransitionsByFromState(ctx context.Context, workflowID, fromStateID uuid.UUID) ([]models.WorkflowTransition, error)
}

type workflowRepository struct {
	db *gorm.DB
}

func NewWorkflowRepository(db *gorm.DB) WorkflowRepository {
	return &workflowRepository{db: db}
}

// stateCodePrefix / transitionCodePrefix are the fixed prefixes for the
// auto-generated State (ste-000001) and Transition (trn-000001) codes,
// following the same convention as the department Organization Code (ORG-######).
const (
	stateCodePrefix      = "ste-"
	transitionCodePrefix = "trn-"
)

// State / Transition Code allocation. Each seq is an in-process counter, seeded
// once (loaded flag) from the current DB maximum and incremented under its mutex
// on every create. The counter is global per entity type (across all workflows).
// This assumes a single backend instance; there is no unique index on
// workflow_states.code / workflow_transitions.code, so the counter is the sole
// collision guard (same caveat as the department Organization Code).
var (
	stateCodeMu     sync.Mutex
	stateCodeSeq    int64
	stateCodeLoaded bool

	transitionCodeMu     sync.Mutex
	transitionCodeSeq    int64
	transitionCodeLoaded bool
)

// Workflow CRUD

func (r *workflowRepository) Create(ctx context.Context, workflow *models.Workflow) error {
	return r.db.WithContext(ctx).Create(workflow).Error
}

func (r *workflowRepository) FindByID(ctx context.Context, id uuid.UUID) (*models.Workflow, error) {
	var workflow models.Workflow
	err := r.db.WithContext(ctx).
		First(&workflow, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	return &workflow, nil
}

func (r *workflowRepository) FindByIDWithRelations(ctx context.Context, id uuid.UUID) (*models.Workflow, error) {
	var workflow models.Workflow
	err := r.db.WithContext(ctx).
		Preload("States", func(db *gorm.DB) *gorm.DB {
			return db.Order("sort_order, name")
		}).
		Preload("States.ViewableRoles").
		Preload("States.EditableRoles").
		Preload("Transitions", func(db *gorm.DB) *gorm.DB {
			return db.Order("sort_order, name")
		}).
		Preload("Transitions.FromState").
		Preload("Transitions.ToState").
		Preload("Transitions.AllowedRoles").
		Preload("Transitions.Requirements").
		Preload("Transitions.Actions", func(db *gorm.DB) *gorm.DB {
			return db.Order("execution_order")
		}).
		Preload("Classifications").
		Preload("Locations").
		Preload("ConvertToRequestRoles").
		Preload("MergeAllowedRoles").
		Preload("CreatedBy").
		First(&workflow, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	return &workflow, nil
}

func (r *workflowRepository) FindByCode(ctx context.Context, code string) (*models.Workflow, error) {
	var workflow models.Workflow
	err := r.db.WithContext(ctx).
		Where("code = ?", code).
		First(&workflow).Error
	if err != nil {
		return nil, err
	}
	return &workflow, nil
}

func (r *workflowRepository) ExistsByCodeOrName(ctx context.Context, codeOrName []string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&models.Workflow{}).
		Where("code IN (?) OR name IN (?)", codeOrName, codeOrName).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *workflowRepository) ExistsByName(ctx context.Context, name string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&models.Workflow{}).
		Where("name = ?", name).
		Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *workflowRepository) List(ctx context.Context, activeOnly bool) ([]models.Workflow, error) {
	var workflows []models.Workflow
	query := r.db.WithContext(ctx).
		Preload("States").
		Preload("Transitions").
		Preload("Classifications").
		Preload("Locations").
		Preload("CreatedBy").
		Preload("ConvertToRequestRoles").
		Preload("MergeAllowedRoles")

	if activeOnly {
		query = query.Where("is_active = ?", true)
	}

	err := query.Order("name").Find(&workflows).Error
	return workflows, err
}

// ListByRecordTypeID is one indexed read. It is deliberately NOT built on
// ListByRecordType: that one matches the record_type string and treats 'all'
// and 'both' as wildcards, which is exactly the match EO must not make.
func (r *workflowRepository) ListByRecordTypeID(ctx context.Context, recordTypeID uuid.UUID) ([]models.Workflow, error) {
	var workflows []models.Workflow
	err := r.db.WithContext(ctx).
		Preload("States").
		Preload("Transitions").
		Where("record_type_id = ?", recordTypeID).
		Where("is_active = ?", true).
		Order("name").
		Find(&workflows).Error
	return workflows, err
}

func (r *workflowRepository) ListByRecordType(ctx context.Context, recordType string, activeOnly bool) ([]models.Workflow, error) {
	var workflows []models.Workflow
	query := r.db.WithContext(ctx).
		Preload("States").
		Preload("Transitions").
		Preload("Classifications").
		Preload("Locations").
		Preload("CreatedBy").
		Preload("ConvertToRequestRoles").
		Preload("MergeAllowedRoles")

	if recordType != "" {
		// Support 'all' type which matches any type, 'both' matches incident/request
		// Use parentheses to ensure correct SQL precedence with AND conditions
		query = query.Where("(record_type = ? OR record_type = 'both' OR record_type = 'all')", recordType)
	}

	if activeOnly {
		query = query.Where("is_active = ?", true)
	}

	err := query.Order("name").Find(&workflows).Error
	return workflows, err
}

func (r *workflowRepository) ListFiltered(ctx context.Context, filter *models.WorkflowFilter) ([]models.Workflow, int64, error) {
	var workflows []models.Workflow
	var total int64

	query := r.db.WithContext(ctx).Model(&models.Workflow{})

	if filter.Search != "" {
		searchPattern := "%" + filter.Search + "%"
		query = query.Where("name ILIKE ?", searchPattern)
	}

	if filter.IsActive != nil {
		query = query.Where("is_active = ?", *filter.IsActive)
	}

	if filter.RecordType != "" {
		// Support 'all' type which matches any type, 'both' matches incident/request
		query = query.Where("(record_type = ? OR record_type = 'both' OR record_type = 'all')", filter.RecordType)
	}

	if filter.CreatedByID != "" {
		query = query.Where("created_by_id = ?", filter.CreatedByID)
	}

	if filter.CreatedFrom != nil {
		query = query.Where("created_at >= ?", *filter.CreatedFrom)
	}
	if filter.CreatedTo != nil {
		query = query.Where("created_at <= ?", *filter.CreatedTo)
	}
	if filter.ModifiedFrom != nil {
		query = query.Where("updated_at >= ?", *filter.ModifiedFrom)
	}
	if filter.ModifiedTo != nil {
		query = query.Where("updated_at <= ?", *filter.ModifiedTo)
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	query = query.
		Preload("States").
		Preload("Transitions").
		Preload("Classifications").
		Preload("Locations").
		Preload("CreatedBy").
		Preload("ConvertToRequestRoles").
		Preload("MergeAllowedRoles").
		Order("name")

	// Limit <= 0 means the caller didn't ask for pagination (page/limit not
	// supplied) — return every matching row, as this endpoint always did
	// before search/filter support was added.
	if filter.Limit > 0 {
		if filter.Page < 1 {
			filter.Page = 1
		}
		offset := (filter.Page - 1) * filter.Limit
		query = query.Offset(offset).Limit(filter.Limit)
	}

	if err := query.Find(&workflows).Error; err != nil {
		return nil, 0, err
	}

	return workflows, total, nil
}

func (r *workflowRepository) Update(ctx context.Context, workflow *models.Workflow) error {
	return r.db.WithContext(ctx).Save(workflow).Error
}

func (r *workflowRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Delete(&models.Workflow{}, "id = ?", id).Error
}

// HardDelete permanently removes a workflow and all its related entities from the database
func (r *workflowRepository) HardDelete(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. Get all transitions for this workflow
		var transitions []models.WorkflowTransition
		if err := tx.Unscoped().Where("workflow_id = ?", id).Find(&transitions).Error; err != nil {
			return err
		}

		// 2. Delete transition requirements and actions for each transition
		for _, t := range transitions {
			// Delete transition requirements
			if err := tx.Unscoped().Where("transition_id = ?", t.ID).Delete(&models.TransitionRequirement{}).Error; err != nil {
				return err
			}
			// Delete transition actions
			if err := tx.Unscoped().Where("transition_id = ?", t.ID).Delete(&models.TransitionAction{}).Error; err != nil {
				return err
			}
			// Clear transition allowed roles (many2many)
			if err := tx.Exec("DELETE FROM transition_allowed_roles WHERE workflow_transition_id = ?", t.ID).Error; err != nil {
				return err
			}
		}

		// 3. Delete all transitions for this workflow
		if err := tx.Unscoped().Where("workflow_id = ?", id).Delete(&models.WorkflowTransition{}).Error; err != nil {
			return err
		}

		// 4. Get all states for this workflow
		var states []models.WorkflowState
		if err := tx.Unscoped().Where("workflow_id = ?", id).Find(&states).Error; err != nil {
			return err
		}

		// 5. Clear state viewable and editable roles (many2many) for each state
		for _, s := range states {
			if err := tx.Exec("DELETE FROM state_viewable_roles WHERE workflow_state_id = ?", s.ID).Error; err != nil {
				return err
			}
			if err := tx.Exec("DELETE FROM state_editable_roles WHERE workflow_state_id = ?", s.ID).Error; err != nil {
				return err
			}
		}

		// 6. Delete all states for this workflow
		if err := tx.Unscoped().Where("workflow_id = ?", id).Delete(&models.WorkflowState{}).Error; err != nil {
			return err
		}

		// 7. Clear workflow classifications (many2many)
		if err := tx.Exec("DELETE FROM workflow_classifications WHERE workflow_id = ?", id).Error; err != nil {
			return err
		}

		// 8. Clear workflow locations (many2many)
		if err := tx.Exec("DELETE FROM workflow_locations WHERE workflow_id = ?", id).Error; err != nil {
			return err
		}

		// 8b. Clear workflow convert-to-request roles (many2many)
		if err := tx.Exec("DELETE FROM workflow_convert_to_request_roles WHERE workflow_id = ?", id).Error; err != nil {
			return err
		}

		// 9. Finally, hard delete the workflow itself
		if err := tx.Unscoped().Delete(&models.Workflow{}, "id = ?", id).Error; err != nil {
			return err
		}

		return nil
	})
}

// ListDeleted returns all soft-deleted workflows
func (r *workflowRepository) ListDeleted(ctx context.Context) ([]models.Workflow, error) {
	var workflows []models.Workflow
	err := r.db.WithContext(ctx).
		Unscoped().
		Where("deleted_at IS NOT NULL").
		Preload("States", func(db *gorm.DB) *gorm.DB {
			return db.Unscoped().Order("sort_order ASC")
		}).
		Preload("Transitions", func(db *gorm.DB) *gorm.DB {
			return db.Unscoped()
		}).
		Preload("Classifications").
		Preload("CreatedBy").
		Order("deleted_at DESC").
		Find(&workflows).Error
	return workflows, err
}

// Restore restores a soft-deleted workflow
func (r *workflowRepository) Restore(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Restore the workflow
		if err := tx.Unscoped().Model(&models.Workflow{}).Where("id = ?", id).Update("deleted_at", nil).Error; err != nil {
			return err
		}

		// Restore all states for this workflow
		if err := tx.Unscoped().Model(&models.WorkflowState{}).Where("workflow_id = ?", id).Update("deleted_at", nil).Error; err != nil {
			return err
		}

		// Restore all transitions for this workflow
		if err := tx.Unscoped().Model(&models.WorkflowTransition{}).Where("workflow_id = ?", id).Update("deleted_at", nil).Error; err != nil {
			return err
		}

		return nil
	})
}

// Workflow-Classification assignments

func (r *workflowRepository) AssignClassifications(ctx context.Context, workflowID uuid.UUID, classificationIDs []uuid.UUID) error {
	var workflow models.Workflow
	if err := r.db.WithContext(ctx).First(&workflow, "id = ?", workflowID).Error; err != nil {
		return err
	}

	var classifications []models.Classification
	if len(classificationIDs) > 0 {
		if err := r.db.WithContext(ctx).Where("id IN ?", classificationIDs).Find(&classifications).Error; err != nil {
			return err
		}
	}

	return r.db.WithContext(ctx).Model(&workflow).Association("Classifications").Replace(classifications)
}

func (r *workflowRepository) GetByClassificationID(ctx context.Context, classificationID uuid.UUID) (*models.Workflow, error) {
	var workflow models.Workflow
	err := r.db.WithContext(ctx).
		Joins("JOIN workflow_classifications wc ON wc.workflow_id = workflows.id").
		Where("wc.classification_id = ? AND workflows.is_active = ?", classificationID, true).
		First(&workflow).Error
	if err != nil {
		return nil, err
	}
	return &workflow, nil
}

func (r *workflowRepository) GetDefaultWorkflow(ctx context.Context) (*models.Workflow, error) {
	var workflow models.Workflow
	err := r.db.WithContext(ctx).
		Where("is_default = ? AND is_active = ?", true, true).
		First(&workflow).Error
	if err != nil {
		return nil, err
	}
	return &workflow, nil
}

// WorkflowState CRUD

func (r *workflowRepository) CreateState(ctx context.Context, state *models.WorkflowState) error {
	// The State Code is system-generated for EPM940 (the service leaves it empty).
	// When empty, allocate the next unique STE-###### code from the in-process counter.
	if strings.TrimSpace(state.Code) == "" {
		code, err := r.nextStateCode(ctx)
		if err != nil {
			return err
		}
		state.Code = code
	}
	return r.db.WithContext(ctx).Create(state).Error
}

// nextStateCode returns the next unique State Code (e.g. STE-000001). It seeds an
// in-process counter once from the current DB maximum — Unscoped() so soft-deleted
// states keep their number reserved — then increments in memory under the mutex,
// so there is no per-create DB round-trip and no collision within this process.
func (r *workflowRepository) nextStateCode(ctx context.Context) (string, error) {
	stateCodeMu.Lock()
	defer stateCodeMu.Unlock()

	if !stateCodeLoaded {
		var maxCode *string
		if err := r.db.WithContext(ctx).Unscoped().Model(&models.WorkflowState{}).
			Select("MAX(code)").
			Where("code LIKE ?", stateCodePrefix+"%").
			Scan(&maxCode).Error; err != nil {
			return "", err
		}
		if maxCode != nil && *maxCode != "" {
			var n int64
			if _, err := fmt.Sscanf(*maxCode, stateCodePrefix+"%d", &n); err == nil {
				stateCodeSeq = n
			}
		}
		stateCodeLoaded = true
	}

	stateCodeSeq++
	return fmt.Sprintf("%s%06d", stateCodePrefix, stateCodeSeq), nil
}

func (r *workflowRepository) FindStateByID(ctx context.Context, id uuid.UUID) (*models.WorkflowState, error) {
	var state models.WorkflowState
	err := r.db.WithContext(ctx).
		Preload("ViewableRoles").
		Preload("EditableRoles").
		Preload("AssignmentRoles").
		Preload("AssignUser").
		First(&state, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	return &state, nil
}

func (r *workflowRepository) FindStateByCode(ctx context.Context, workflowID uuid.UUID, code string) (*models.WorkflowState, error) {
	var state models.WorkflowState
	err := r.db.WithContext(ctx).
		Preload("ViewableRoles").
		Preload("EditableRoles").
		Preload("AssignmentRoles").
		Preload("AssignUser").
		Where("workflow_id = ? AND code = ?", workflowID, code).
		First(&state).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &state, nil
}

func (r *workflowRepository) ListStatesByWorkflowID(ctx context.Context, workflowID uuid.UUID) ([]models.WorkflowState, error) {
	var states []models.WorkflowState
	err := r.db.WithContext(ctx).
		Preload("ViewableRoles").
		Preload("EditableRoles").
		Preload("AssignmentRoles").
		Preload("AssignUser").
		Where("workflow_id = ?", workflowID).
		Order("sort_order, name").
		Find(&states).Error
	return states, err
}

func (r *workflowRepository) UpdateState(ctx context.Context, state *models.WorkflowState) error {
	return r.db.WithContext(ctx).Save(state).Error
}

func (r *workflowRepository) DeleteState(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Delete(&models.WorkflowState{}, "id = ?", id).Error
}

func (r *workflowRepository) GetInitialState(ctx context.Context, workflowID uuid.UUID) (*models.WorkflowState, error) {
	var state models.WorkflowState
	err := r.db.WithContext(ctx).
		Preload("AssignmentRoles").
		Preload("AssignUser").
		Where("workflow_id = ? AND state_type = ? AND is_active = ?", workflowID, "initial", true).
		First(&state).Error
	if err != nil {
		return nil, err
	}
	return &state, nil
}

// WorkflowTransition CRUD

func (r *workflowRepository) CreateTransition(ctx context.Context, transition *models.WorkflowTransition) error {
	// The Transition Code is system-generated for EPM940 (the service leaves it empty).
	// When empty, allocate the next unique TRN-###### code from the in-process counter.
	if strings.TrimSpace(transition.Code) == "" {
		code, err := r.nextTransitionCode(ctx)
		if err != nil {
			return err
		}
		transition.Code = code
	}
	return r.db.WithContext(ctx).Create(transition).Error
}

// nextTransitionCode returns the next unique Transition Code (e.g. TRN-000001).
// It seeds an in-process counter once from the current DB maximum — Unscoped() so
// soft-deleted transitions keep their number reserved — then increments in memory
// under the mutex, so there is no per-create DB round-trip and no collision within
// this process.
func (r *workflowRepository) nextTransitionCode(ctx context.Context) (string, error) {
	transitionCodeMu.Lock()
	defer transitionCodeMu.Unlock()

	if !transitionCodeLoaded {
		var maxCode *string
		if err := r.db.WithContext(ctx).Unscoped().Model(&models.WorkflowTransition{}).
			Select("MAX(code)").
			Where("code LIKE ?", transitionCodePrefix+"%").
			Scan(&maxCode).Error; err != nil {
			return "", err
		}
		if maxCode != nil && *maxCode != "" {
			var n int64
			if _, err := fmt.Sscanf(*maxCode, transitionCodePrefix+"%d", &n); err == nil {
				transitionCodeSeq = n
			}
		}
		transitionCodeLoaded = true
	}

	transitionCodeSeq++
	return fmt.Sprintf("%s%06d", transitionCodePrefix, transitionCodeSeq), nil
}

func (r *workflowRepository) FindTransitionByID(ctx context.Context, id uuid.UUID) (*models.WorkflowTransition, error) {
	var transition models.WorkflowTransition
	err := r.db.WithContext(ctx).First(&transition, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	return &transition, nil
}

func (r *workflowRepository) FindTransitionByIDWithRelations(ctx context.Context, id uuid.UUID) (*models.WorkflowTransition, error) {
	var transition models.WorkflowTransition
	err := r.db.WithContext(ctx).
		Preload("FromState").
		Preload("ToState").
		Preload("AllowedRoles").
		Preload("AssignDepartment").
		Preload("AssignUser").
		Preload("AssignmentRoles").
		Preload("Requirements").
		Preload("Actions", func(db *gorm.DB) *gorm.DB {
			return db.Order("execution_order")
		}).
		Preload("FieldChanges", func(db *gorm.DB) *gorm.DB {
			return db.Order("sort_order")
		}).
		First(&transition, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	return &transition, nil
}

func (r *workflowRepository) ListTransitionsByWorkflowID(ctx context.Context, workflowID uuid.UUID) ([]models.WorkflowTransition, error) {
	var transitions []models.WorkflowTransition
	err := r.db.WithContext(ctx).
		Preload("FromState").
		Preload("ToState").
		Preload("AllowedRoles").
		Preload("AssignDepartment").
		Preload("AssignUser").
		Preload("AssignmentRoles").
		Preload("Requirements").
		Preload("Actions", func(db *gorm.DB) *gorm.DB {
			return db.Order("execution_order")
		}).
		Preload("FieldChanges", func(db *gorm.DB) *gorm.DB {
			return db.Order("sort_order")
		}).
		Where("workflow_id = ?", workflowID).
		Order("sort_order, name").
		Find(&transitions).Error
	return transitions, err
}

func (r *workflowRepository) ListTransitionsFromState(ctx context.Context, stateID uuid.UUID) ([]models.WorkflowTransition, error) {
	var transitions []models.WorkflowTransition
	err := r.db.WithContext(ctx).
		Preload("FromState").
		Preload("ToState").
		Preload("AllowedRoles").
		Preload("AssignDepartment").
		Preload("AssignUser").
		Preload("AssignmentRoles").
		Preload("Requirements").
		Preload("Actions", func(db *gorm.DB) *gorm.DB {
			return db.Order("execution_order")
		}).
		Preload("FieldChanges", func(db *gorm.DB) *gorm.DB {
			return db.Order("sort_order")
		}).
		Where("from_state_id = ? AND is_active = ?", stateID, true).
		Order("sort_order, name").
		Find(&transitions).Error
	return transitions, err
}

func (r *workflowRepository) ListTransitionsToState(ctx context.Context, stateID uuid.UUID) ([]models.WorkflowTransition, error) {
	var transitions []models.WorkflowTransition
	err := r.db.WithContext(ctx).
		Preload("FromState").
		Preload("ToState").
		Preload("AllowedRoles").
		Preload("AssignDepartment").
		Preload("AssignUser").
		Preload("AssignmentRoles").
		Preload("Requirements").
		Preload("Actions", func(db *gorm.DB) *gorm.DB {
			return db.Order("execution_order")
		}).
		Preload("FieldChanges", func(db *gorm.DB) *gorm.DB {
			return db.Order("sort_order")
		}).
		Where("to_state_id = ? AND is_active = ?", stateID, true).
		Order("sort_order, name").
		Find(&transitions).Error
	return transitions, err
}

func (r *workflowRepository) UpdateTransition(ctx context.Context, transition *models.WorkflowTransition) error {
	return r.db.WithContext(ctx).Save(transition).Error
}

func (r *workflowRepository) DeleteTransition(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Delete(&models.WorkflowTransition{}, "id = ?", id).Error
}

// Transition role assignments

func (r *workflowRepository) AssignTransitionRoles(ctx context.Context, transitionID uuid.UUID, roleIDs []uuid.UUID) error {
	var transition models.WorkflowTransition
	if err := r.db.WithContext(ctx).First(&transition, "id = ?", transitionID).Error; err != nil {
		return err
	}

	var roles []models.Role
	if len(roleIDs) > 0 {
		if err := r.db.WithContext(ctx).Where("id IN ?", roleIDs).Find(&roles).Error; err != nil {
			return err
		}
	}

	return r.db.WithContext(ctx).Model(&transition).Association("AllowedRoles").Replace(roles)
}

func (r *workflowRepository) AssignTransitionAssignmentRoles(ctx context.Context, transitionID uuid.UUID, roleIDs []uuid.UUID) error {
	var transition models.WorkflowTransition
	if err := r.db.WithContext(ctx).First(&transition, "id = ?", transitionID).Error; err != nil {
		return err
	}

	var roles []models.Role
	if len(roleIDs) > 0 {
		if err := r.db.WithContext(ctx).Where("id IN ?", roleIDs).Find(&roles).Error; err != nil {
			return err
		}
	}

	return r.db.WithContext(ctx).Model(&transition).Association("AssignmentRoles").Replace(roles)
}

func (r *workflowRepository) AssignStateViewableRoles(ctx context.Context, stateID uuid.UUID, roleIDs []uuid.UUID) error {
	var state models.WorkflowState
	if err := r.db.WithContext(ctx).First(&state, "id = ?", stateID).Error; err != nil {
		return err
	}

	var roles []models.Role
	if len(roleIDs) > 0 {
		if err := r.db.WithContext(ctx).Where("id IN ?", roleIDs).Find(&roles).Error; err != nil {
			return err
		}
	}

	return r.db.WithContext(ctx).Model(&state).Association("ViewableRoles").Replace(roles)
}

func (r *workflowRepository) AssignStateEditableRoles(ctx context.Context, stateID uuid.UUID, roleIDs []uuid.UUID) error {
	var state models.WorkflowState
	if err := r.db.WithContext(ctx).First(&state, "id = ?", stateID).Error; err != nil {
		return err
	}

	var roles []models.Role
	if len(roleIDs) > 0 {
		if err := r.db.WithContext(ctx).Where("id IN ?", roleIDs).Find(&roles).Error; err != nil {
			return err
		}
	}

	return r.db.WithContext(ctx).Model(&state).Association("EditableRoles").Replace(roles)
}

func (r *workflowRepository) AssignStateAssignmentRoles(ctx context.Context, stateID uuid.UUID, roleIDs []uuid.UUID) error {
	var state models.WorkflowState
	if err := r.db.WithContext(ctx).First(&state, "id = ?", stateID).Error; err != nil {
		return err
	}

	var roles []models.Role
	if len(roleIDs) > 0 {
		if err := r.db.WithContext(ctx).Where("id IN ?", roleIDs).Find(&roles).Error; err != nil {
			return err
		}
	}

	return r.db.WithContext(ctx).Model(&state).Association("AssignmentRoles").Replace(roles)
}

// TransitionRequirement CRUD

func (r *workflowRepository) SetTransitionRequirements(ctx context.Context, transitionID uuid.UUID, requirements []models.TransitionRequirement) error {
	// Delete existing requirements
	if err := r.db.WithContext(ctx).Where("transition_id = ?", transitionID).Delete(&models.TransitionRequirement{}).Error; err != nil {
		return err
	}

	// Create new requirements
	for i := range requirements {
		requirements[i].TransitionID = transitionID
		if err := r.db.WithContext(ctx).Create(&requirements[i]).Error; err != nil {
			return err
		}
	}

	return nil
}

func (r *workflowRepository) GetTransitionRequirements(ctx context.Context, transitionID uuid.UUID) ([]models.TransitionRequirement, error) {
	var requirements []models.TransitionRequirement
	err := r.db.WithContext(ctx).
		Where("transition_id = ?", transitionID).
		Find(&requirements).Error
	return requirements, err
}

// TransitionAction CRUD

func (r *workflowRepository) SetTransitionActions(ctx context.Context, transitionID uuid.UUID, actions []models.TransitionAction) error {
	// Delete existing actions
	if err := r.db.WithContext(ctx).Where("transition_id = ?", transitionID).Delete(&models.TransitionAction{}).Error; err != nil {
		return err
	}

	// Create new actions
	for i := range actions {
		actions[i].TransitionID = transitionID
		if err := r.db.WithContext(ctx).Create(&actions[i]).Error; err != nil {
			return err
		}
	}

	return nil
}

func (r *workflowRepository) GetTransitionActions(ctx context.Context, transitionID uuid.UUID) ([]models.TransitionAction, error) {
	var actions []models.TransitionAction
	err := r.db.WithContext(ctx).
		Where("transition_id = ?", transitionID).
		Order("execution_order").
		Find(&actions).Error
	return actions, err
}

// TransitionFieldChange CRUD

func (r *workflowRepository) SetTransitionFieldChanges(ctx context.Context, transitionID uuid.UUID, fieldChanges []models.TransitionFieldChange) error {
	if err := r.db.WithContext(ctx).Where("transition_id = ?", transitionID).Delete(&models.TransitionFieldChange{}).Error; err != nil {
		return err
	}
	for i := range fieldChanges {
		fieldChanges[i].TransitionID = transitionID
		if err := r.db.WithContext(ctx).Create(&fieldChanges[i]).Error; err != nil {
			return err
		}
	}
	return nil
}

func (r *workflowRepository) GetTransitionFieldChanges(ctx context.Context, transitionID uuid.UUID) ([]models.TransitionFieldChange, error) {
	var fieldChanges []models.TransitionFieldChange
	err := r.db.WithContext(ctx).
		Where("transition_id = ?", transitionID).
		Order("sort_order").
		Find(&fieldChanges).Error
	return fieldChanges, err
}

// AssignConvertToRequestRoles assigns roles that are allowed to convert incidents to requests for this workflow
func (r *workflowRepository) AssignConvertToRequestRoles(ctx context.Context, workflowID uuid.UUID, roleIDs []uuid.UUID) error {
	var workflow models.Workflow
	if err := r.db.WithContext(ctx).First(&workflow, "id = ?", workflowID).Error; err != nil {
		return err
	}

	var roles []models.Role
	if len(roleIDs) > 0 {
		if err := r.db.WithContext(ctx).Where("id IN ?", roleIDs).Find(&roles).Error; err != nil {
			return err
		}
	}

	return r.db.WithContext(ctx).Model(&workflow).Association("ConvertToRequestRoles").Replace(roles)
}

// AssignMergeAllowedRoles assigns roles that are allowed to merge incidents for this workflow
func (r *workflowRepository) AssignMergeAllowedRoles(ctx context.Context, workflowID uuid.UUID, roleIDs []uuid.UUID) error {
	var workflow models.Workflow
	if err := r.db.WithContext(ctx).First(&workflow, "id = ?", workflowID).Error; err != nil {
		return err
	}

	var roles []models.Role
	if len(roleIDs) > 0 {
		if err := r.db.WithContext(ctx).Where("id IN ?", roleIDs).Find(&roles).Error; err != nil {
			return err
		}
	}

	return r.db.WithContext(ctx).Model(&workflow).Association("MergeAllowedRoles").Replace(roles)
}

// GetMergeAllowedRoles returns roles that are allowed to merge incidents for this workflow
func (r *workflowRepository) GetMergeAllowedRoles(ctx context.Context, workflowID uuid.UUID) ([]models.Role, error) {
	var roles []models.Role
	err := r.db.WithContext(ctx).
		Table("roles").
		Joins("JOIN workflow_merge_allowed_roles ON roles.id = workflow_merge_allowed_roles.role_id").
		Where("workflow_merge_allowed_roles.workflow_id = ?", workflowID).
		Find(&roles).Error
	return roles, err
}

// AssignLocations assigns locations to a workflow
func (r *workflowRepository) AssignLocations(ctx context.Context, workflowID uuid.UUID, locationIDs []uuid.UUID) error {
	var workflow models.Workflow
	if err := r.db.WithContext(ctx).First(&workflow, "id = ?", workflowID).Error; err != nil {
		return err
	}

	var locations []models.Location
	if len(locationIDs) > 0 {
		if err := r.db.WithContext(ctx).Where("id IN ?", locationIDs).Find(&locations).Error; err != nil {
			return err
		}
	}

	return r.db.WithContext(ctx).Model(&workflow).Association("Locations").Replace(locations)
}

// FindWorkflowsByRecordTypeAndClassifications finds workflows matching record type for duplicate rule detection
func (r *workflowRepository) FindWorkflowsByRecordTypeAndClassifications(
	ctx context.Context,
	recordType string,
	classificationIDs []uuid.UUID,
	locationIDs []uuid.UUID,
	excludeWorkflowID *uuid.UUID,
) ([]models.Workflow, error) {
	var workflows []models.Workflow

	query := r.db.WithContext(ctx).
		Preload("Classifications").
		Preload("Locations").
		Where("is_active = ?", true)

	// Match record type including 'both' and 'all'
	if recordType == "incident" || recordType == "request" {
		query = query.Where("record_type IN ?", []string{recordType, "both", "all"})
	} else if recordType == "complaint" || recordType == "query" {
		query = query.Where("record_type IN ?", []string{recordType, "all"})
	} else {
		query = query.Where("record_type = ?", recordType)
	}

	// Exclude current workflow if updating
	if excludeWorkflowID != nil {
		query = query.Where("id != ?", *excludeWorkflowID)
	}

	if err := query.Find(&workflows).Error; err != nil {
		return nil, err
	}

	return workflows, nil
}

// GetTransitionsByFromState gets all transitions from a specific state
func (r *workflowRepository) GetTransitionsByFromState(ctx context.Context, workflowID, fromStateID uuid.UUID) ([]models.WorkflowTransition, error) {
	var transitions []models.WorkflowTransition
	err := r.db.WithContext(ctx).
		Preload("AllowedRoles").
		Preload("FromState").
		Preload("ToState").
		Where("workflow_id = ? AND from_state_id = ? AND is_active = ?", workflowID, fromStateID, true).
		Order("sort_order").
		Find(&transitions).Error
	return transitions, err
}
