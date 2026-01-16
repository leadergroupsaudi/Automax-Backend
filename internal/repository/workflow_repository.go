package repository

import (
	"context"

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
	Update(ctx context.Context, workflow *models.Workflow) error
	Delete(ctx context.Context, id uuid.UUID) error

	// Workflow-Classification assignments
	AssignClassifications(ctx context.Context, workflowID uuid.UUID, classificationIDs []uuid.UUID) error
	GetByClassificationID(ctx context.Context, classificationID uuid.UUID) (*models.Workflow, error)
	GetDefaultWorkflow(ctx context.Context) (*models.Workflow, error)

	// WorkflowState CRUD
	CreateState(ctx context.Context, state *models.WorkflowState) error
	FindStateByID(ctx context.Context, id uuid.UUID) (*models.WorkflowState, error)
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
	UpdateTransition(ctx context.Context, transition *models.WorkflowTransition) error
	DeleteTransition(ctx context.Context, id uuid.UUID) error

	// Transition role assignments
	AssignTransitionRoles(ctx context.Context, transitionID uuid.UUID, roleIDs []uuid.UUID) error

	// TransitionRequirement CRUD
	SetTransitionRequirements(ctx context.Context, transitionID uuid.UUID, requirements []models.TransitionRequirement) error
	GetTransitionRequirements(ctx context.Context, transitionID uuid.UUID) ([]models.TransitionRequirement, error)

	// TransitionAction CRUD
	SetTransitionActions(ctx context.Context, transitionID uuid.UUID, actions []models.TransitionAction) error
	GetTransitionActions(ctx context.Context, transitionID uuid.UUID) ([]models.TransitionAction, error)
}

type workflowRepository struct {
	db *gorm.DB
}

func NewWorkflowRepository(db *gorm.DB) WorkflowRepository {
	return &workflowRepository{db: db}
}

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

func (r *workflowRepository) List(ctx context.Context, activeOnly bool) ([]models.Workflow, error) {
	var workflows []models.Workflow
	query := r.db.WithContext(ctx).
		Preload("States").
		Preload("Transitions").
		Preload("Classifications").
		Preload("CreatedBy")

	if activeOnly {
		query = query.Where("is_active = ?", true)
	}

	err := query.Order("name").Find(&workflows).Error
	return workflows, err
}

func (r *workflowRepository) Update(ctx context.Context, workflow *models.Workflow) error {
	return r.db.WithContext(ctx).Save(workflow).Error
}

func (r *workflowRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Delete(&models.Workflow{}, "id = ?", id).Error
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
	return r.db.WithContext(ctx).Create(state).Error
}

func (r *workflowRepository) FindStateByID(ctx context.Context, id uuid.UUID) (*models.WorkflowState, error) {
	var state models.WorkflowState
	err := r.db.WithContext(ctx).First(&state, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	return &state, nil
}

func (r *workflowRepository) ListStatesByWorkflowID(ctx context.Context, workflowID uuid.UUID) ([]models.WorkflowState, error) {
	var states []models.WorkflowState
	err := r.db.WithContext(ctx).
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
		Where("workflow_id = ? AND state_type = ? AND is_active = ?", workflowID, "initial", true).
		First(&state).Error
	if err != nil {
		return nil, err
	}
	return &state, nil
}

// WorkflowTransition CRUD

func (r *workflowRepository) CreateTransition(ctx context.Context, transition *models.WorkflowTransition) error {
	return r.db.WithContext(ctx).Create(transition).Error
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
		Preload("AssignmentRole").
		Preload("Requirements").
		Preload("Actions", func(db *gorm.DB) *gorm.DB {
			return db.Order("execution_order")
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
		Preload("AssignmentRole").
		Preload("Requirements").
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
		Preload("AssignmentRole").
		Preload("Requirements").
		Preload("Actions", func(db *gorm.DB) *gorm.DB {
			return db.Order("execution_order")
		}).
		Where("from_state_id = ? AND is_active = ?", stateID, true).
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
