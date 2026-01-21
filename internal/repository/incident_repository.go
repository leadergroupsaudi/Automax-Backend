package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type IncidentRepository interface {
	// Incident CRUD
	Create(ctx context.Context, incident *models.Incident) error
	FindByID(ctx context.Context, id uuid.UUID) (*models.Incident, error)
	FindByIDWithRelations(ctx context.Context, id uuid.UUID) (*models.Incident, error)
	FindByIncidentNumber(ctx context.Context, number string) (*models.Incident, error)
	List(ctx context.Context, filter *models.IncidentFilter) ([]models.Incident, int64, error)
	Update(ctx context.Context, incident *models.Incident) error
	UpdateFields(ctx context.Context, id uuid.UUID, updates map[string]interface{}) error
	Delete(ctx context.Context, id uuid.UUID) error

	// Incident number generation
	GenerateIncidentNumber(ctx context.Context) (string, error)
	GenerateRequestNumber(ctx context.Context) (string, error)
	GenerateComplaintNumber(ctx context.Context) (string, error)

	// State transitions
	UpdateState(ctx context.Context, incidentID, newStateID uuid.UUID) error
	CreateTransitionHistory(ctx context.Context, history *models.IncidentTransitionHistory) error
	GetTransitionHistory(ctx context.Context, incidentID uuid.UUID) ([]models.IncidentTransitionHistory, error)

	// Comments
	CreateComment(ctx context.Context, comment *models.IncidentComment) error
	FindCommentByID(ctx context.Context, id uuid.UUID) (*models.IncidentComment, error)
	ListComments(ctx context.Context, incidentID uuid.UUID) ([]models.IncidentComment, error)
	UpdateComment(ctx context.Context, comment *models.IncidentComment) error
	DeleteComment(ctx context.Context, id uuid.UUID) error

	// Attachments
	CreateAttachment(ctx context.Context, attachment *models.IncidentAttachment) error
	FindAttachmentByID(ctx context.Context, id uuid.UUID) (*models.IncidentAttachment, error)
	ListAttachments(ctx context.Context, incidentID uuid.UUID) ([]models.IncidentAttachment, error)
	DeleteAttachment(ctx context.Context, id uuid.UUID) error
	LinkAttachmentsToTransition(ctx context.Context, attachmentIDs []uuid.UUID, transitionHistoryID uuid.UUID) error

	// Assignment
	AssignIncident(ctx context.Context, incidentID, assigneeID uuid.UUID) error
	SetAssignees(ctx context.Context, incidentID uuid.UUID, userIDs []uuid.UUID) error
	ClearAssignees(ctx context.Context, incidentID uuid.UUID) error
	SetLookupValues(ctx context.Context, incidentID uuid.UUID, lookupValues []models.LookupValue) error

	// Stats
	GetStats(ctx context.Context, filter *models.IncidentFilter) (*models.IncidentStatsResponse, error)
	GetSLABreachedIncidents(ctx context.Context) ([]models.Incident, error)
	UpdateSLABreached(ctx context.Context, incidentID uuid.UUID, breached bool) error
	MarkSLABreached(ctx context.Context) (int64, error)

	// User-specific queries
	GetAssignedToUser(ctx context.Context, userID uuid.UUID, recordType string, page, limit int) ([]models.Incident, int64, error)
	GetReportedByUser(ctx context.Context, userID uuid.UUID, recordType string, page, limit int) ([]models.Incident, int64, error)

	// Revisions
	CreateRevision(ctx context.Context, revision *models.IncidentRevision) error
	ListRevisions(ctx context.Context, filter *models.IncidentRevisionFilter) ([]models.IncidentRevision, int64, error)
	GetNextRevisionNumber(ctx context.Context, incidentID uuid.UUID) (int, error)

	// Feedback
	CreateFeedback(ctx context.Context, feedback *models.IncidentFeedback) error
	FindFeedbackByID(ctx context.Context, id uuid.UUID) (*models.IncidentFeedback, error)
	ListFeedback(ctx context.Context, incidentID uuid.UUID) ([]models.IncidentFeedback, error)
	LinkFeedbackToTransition(ctx context.Context, feedbackID uuid.UUID, transitionHistoryID uuid.UUID) error

	// Complaint-specific
	IncrementEvaluationCount(ctx context.Context, id uuid.UUID) error
}

type incidentRepository struct {
	db *gorm.DB
}

func NewIncidentRepository(db *gorm.DB) IncidentRepository {
	return &incidentRepository{db: db}
}

// Incident CRUD

func (r *incidentRepository) Create(ctx context.Context, incident *models.Incident) error {
	return r.db.WithContext(ctx).Create(incident).Error
}

func (r *incidentRepository) FindByID(ctx context.Context, id uuid.UUID) (*models.Incident, error) {
	var incident models.Incident
	err := r.db.WithContext(ctx).
		Preload("CurrentState").
		Preload("Workflow").
		First(&incident, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	return &incident, nil
}

func (r *incidentRepository) FindByIDWithRelations(ctx context.Context, id uuid.UUID) (*models.Incident, error) {
	var incident models.Incident
	err := r.db.WithContext(ctx).Session(&gorm.Session{}).
		Preload("Classification").
		Preload("Workflow").
		Preload("Workflow.States", func(db *gorm.DB) *gorm.DB {
			return db.Order("sort_order ASC")
		}).
		Preload("Workflow.Transitions").
		Preload("CurrentState").
		Preload("Assignee").
		Preload("Assignees").
		Preload("Department").
		Preload("Location").
		Preload("LookupValues.Category").
		Preload("Reporter").
		Preload("SourceIncident").
		Preload("Comments", func(db *gorm.DB) *gorm.DB {
			return db.Order("created_at DESC")
		}).
		Preload("Comments.Author").
		Preload("Attachments", func(db *gorm.DB) *gorm.DB {
			return db.Order("created_at DESC")
		}).
		Preload("Attachments.UploadedBy").
		Preload("TransitionHistory", func(db *gorm.DB) *gorm.DB {
			return db.Order("transitioned_at DESC")
		}).
		Preload("TransitionHistory.FromState").
		Preload("TransitionHistory.ToState").
		Preload("TransitionHistory.PerformedBy").
		First(&incident, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	return &incident, nil
}

func (r *incidentRepository) FindByIncidentNumber(ctx context.Context, number string) (*models.Incident, error) {
	var incident models.Incident
	err := r.db.WithContext(ctx).
		Preload("CurrentState").
		Where("incident_number = ?", number).
		First(&incident).Error
	if err != nil {
		return nil, err
	}
	return &incident, nil
}

func (r *incidentRepository) List(ctx context.Context, filter *models.IncidentFilter) ([]models.Incident, int64, error) {
	var incidents []models.Incident
	var total int64

	query := r.db.WithContext(ctx).Model(&models.Incident{})

	// Apply filters
	if filter.WorkflowID != nil {
		query = query.Where("workflow_id = ?", *filter.WorkflowID)
	}
	if filter.CurrentStateID != nil {
		query = query.Where("current_state_id = ?", *filter.CurrentStateID)
	}
	if filter.ClassificationID != nil {
		query = query.Where("classification_id = ?", *filter.ClassificationID)
	}
	if filter.AssigneeID != nil {
		query = query.Where("assignee_id = ?", *filter.AssigneeID)
	}
	if filter.DepartmentID != nil {
		query = query.Where("department_id = ?", *filter.DepartmentID)
	}
	if filter.LocationID != nil {
		query = query.Where("location_id = ?", *filter.LocationID)
	}
	if filter.ReporterID != nil {
		query = query.Where("reporter_id = ?", *filter.ReporterID)
	}
	if filter.SLABreached != nil {
		query = query.Where("sla_breached = ?", *filter.SLABreached)
	}
	if filter.RecordType != nil {
		query = query.Where("record_type = ?", *filter.RecordType)
	}
	if filter.Channel != nil && *filter.Channel != "" {
		query = query.Where("channel = ?", *filter.Channel)
	}
	if filter.StartDate != nil {
		query = query.Where("created_at >= ?", *filter.StartDate)
	}
	if filter.EndDate != nil {
		query = query.Where("created_at <= ?", *filter.EndDate)
	}
	if filter.Search != "" {
		searchPattern := "%" + filter.Search + "%"
		query = query.Where("incident_number ILIKE ? OR title ILIKE ? OR description ILIKE ?", searchPattern, searchPattern, searchPattern)
	}

	// Count total
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// Apply pagination
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.Limit < 1 || filter.Limit > 100 {
		filter.Limit = 20
	}
	offset := (filter.Page - 1) * filter.Limit

	err := query.
		Preload("Classification").
		Preload("Workflow").
		Preload("CurrentState").
		Preload("Assignee").
		Preload("Department").
		Preload("Location").
		Preload("LookupValues.Category").
		Order("created_at DESC").
		Offset(offset).
		Limit(filter.Limit).
		Find(&incidents).Error
	if err != nil {
		return nil, 0, err
	}

	return incidents, total, nil
}

func (r *incidentRepository) Update(ctx context.Context, incident *models.Incident) error {
	return r.db.WithContext(ctx).Save(incident).Error
}

func (r *incidentRepository) UpdateFields(ctx context.Context, id uuid.UUID, updates map[string]interface{}) error {
	return r.db.WithContext(ctx).Model(&models.Incident{}).Where("id = ?", id).Updates(updates).Error
}

func (r *incidentRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Delete(&models.Incident{}, "id = ?", id).Error
}

// Incident number generation

func (r *incidentRepository) GenerateIncidentNumber(ctx context.Context) (string, error) {
	year := time.Now().Year()
	var count int64
	err := r.db.WithContext(ctx).Model(&models.Incident{}).
		Where("EXTRACT(YEAR FROM created_at) = ?", year).
		Where("record_type = 'incident' OR record_type = '' OR record_type IS NULL").
		Count(&count).Error
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("INC-%d-%06d", year, count+1), nil
}

func (r *incidentRepository) GenerateRequestNumber(ctx context.Context) (string, error) {
	year := time.Now().Year()
	var count int64
	err := r.db.WithContext(ctx).Model(&models.Incident{}).
		Where("EXTRACT(YEAR FROM created_at) = ?", year).
		Where("record_type = 'request'").
		Count(&count).Error
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("REQ-%d-%06d", year, count+1), nil
}

func (r *incidentRepository) GenerateComplaintNumber(ctx context.Context) (string, error) {
	year := time.Now().Year()
	var count int64
	err := r.db.WithContext(ctx).Model(&models.Incident{}).
		Where("EXTRACT(YEAR FROM created_at) = ?", year).
		Where("record_type = 'complaint'").
		Count(&count).Error
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("COMP-%d-%06d", year, count+1), nil
}

// State transitions

func (r *incidentRepository) UpdateState(ctx context.Context, incidentID, newStateID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Model(&models.Incident{}).
		Where("id = ?", incidentID).
		Update("current_state_id", newStateID).Error
}

func (r *incidentRepository) CreateTransitionHistory(ctx context.Context, history *models.IncidentTransitionHistory) error {
	return r.db.WithContext(ctx).Create(history).Error
}

func (r *incidentRepository) GetTransitionHistory(ctx context.Context, incidentID uuid.UUID) ([]models.IncidentTransitionHistory, error) {
	var history []models.IncidentTransitionHistory
	err := r.db.WithContext(ctx).
		Preload("Transition").
		Preload("FromState").
		Preload("ToState").
		Preload("PerformedBy").
		Where("incident_id = ?", incidentID).
		Order("transitioned_at DESC").
		Find(&history).Error
	return history, err
}

// Comments

func (r *incidentRepository) CreateComment(ctx context.Context, comment *models.IncidentComment) error {
	return r.db.WithContext(ctx).Create(comment).Error
}

func (r *incidentRepository) FindCommentByID(ctx context.Context, id uuid.UUID) (*models.IncidentComment, error) {
	var comment models.IncidentComment
	err := r.db.WithContext(ctx).
		Preload("Author").
		First(&comment, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	return &comment, nil
}

func (r *incidentRepository) ListComments(ctx context.Context, incidentID uuid.UUID) ([]models.IncidentComment, error) {
	var comments []models.IncidentComment
	err := r.db.WithContext(ctx).
		Preload("Author").
		Where("incident_id = ?", incidentID).
		Order("created_at DESC").
		Find(&comments).Error
	return comments, err
}

func (r *incidentRepository) UpdateComment(ctx context.Context, comment *models.IncidentComment) error {
	return r.db.WithContext(ctx).Save(comment).Error
}

func (r *incidentRepository) DeleteComment(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Delete(&models.IncidentComment{}, "id = ?", id).Error
}

// Attachments

func (r *incidentRepository) CreateAttachment(ctx context.Context, attachment *models.IncidentAttachment) error {
	return r.db.WithContext(ctx).Create(attachment).Error
}

func (r *incidentRepository) FindAttachmentByID(ctx context.Context, id uuid.UUID) (*models.IncidentAttachment, error) {
	var attachment models.IncidentAttachment
	err := r.db.WithContext(ctx).
		Preload("UploadedBy").
		First(&attachment, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	return &attachment, nil
}

func (r *incidentRepository) ListAttachments(ctx context.Context, incidentID uuid.UUID) ([]models.IncidentAttachment, error) {
	var attachments []models.IncidentAttachment
	err := r.db.WithContext(ctx).
		Preload("UploadedBy").
		Where("incident_id = ?", incidentID).
		Order("created_at DESC").
		Find(&attachments).Error
	return attachments, err
}

func (r *incidentRepository) DeleteAttachment(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Delete(&models.IncidentAttachment{}, "id = ?", id).Error
}

func (r *incidentRepository) LinkAttachmentsToTransition(ctx context.Context, attachmentIDs []uuid.UUID, transitionHistoryID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Model(&models.IncidentAttachment{}).
		Where("id IN ?", attachmentIDs).
		Update("transition_history_id", transitionHistoryID).Error
}

// Assignment

func (r *incidentRepository) AssignIncident(ctx context.Context, incidentID, assigneeID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Model(&models.Incident{}).
		Where("id = ?", incidentID).
		Update("assignee_id", assigneeID).Error
}

func (r *incidentRepository) SetAssignees(ctx context.Context, incidentID uuid.UUID, userIDs []uuid.UUID) error {
	// Get the incident
	var incident models.Incident
	if err := r.db.WithContext(ctx).First(&incident, "id = ?", incidentID).Error; err != nil {
		return err
	}

	// Get users to assign
	var users []models.User
	if len(userIDs) > 0 {
		if err := r.db.WithContext(ctx).Where("id IN ?", userIDs).Find(&users).Error; err != nil {
			return err
		}
	}

	// Replace the assignees (this clears existing and sets new ones)
	return r.db.WithContext(ctx).Model(&incident).Association("Assignees").Replace(users)
}

func (r *incidentRepository) ClearAssignees(ctx context.Context, incidentID uuid.UUID) error {
	var incident models.Incident
	if err := r.db.WithContext(ctx).First(&incident, "id = ?", incidentID).Error; err != nil {
		return err
	}
	return r.db.WithContext(ctx).Model(&incident).Association("Assignees").Clear()
}

func (r *incidentRepository) SetLookupValues(ctx context.Context, incidentID uuid.UUID, lookupValues []models.LookupValue) error {
	var incident models.Incident
	if err := r.db.WithContext(ctx).First(&incident, "id = ?", incidentID).Error; err != nil {
		return err
	}

	// First fetch the actual LookupValue records from database
	var actualLookupValues []models.LookupValue
	lookupIDs := make([]uuid.UUID, len(lookupValues))
	for i, lv := range lookupValues {
		lookupIDs[i] = lv.ID
	}

	if err := r.db.WithContext(ctx).Where("id IN ?", lookupIDs).Find(&actualLookupValues).Error; err != nil {
		return err
	}

	return r.db.WithContext(ctx).Model(&incident).Association("LookupValues").Replace(actualLookupValues)
}

// Stats

func (r *incidentRepository) GetStats(ctx context.Context, filter *models.IncidentFilter) (*models.IncidentStatsResponse, error) {

	stats := &models.IncidentStatsResponse{

		ByState: make(map[string]int64),

	}



	baseQuery := r.db.WithContext(ctx).Model(&models.Incident{})



	// Apply filters if provided

	if filter != nil {

		if filter.WorkflowID != nil {

			baseQuery = baseQuery.Where("workflow_id = ?", *filter.WorkflowID)

		}

		if filter.DepartmentID != nil {

			baseQuery = baseQuery.Where("department_id = ?", *filter.DepartmentID)

		}

		if filter.AssigneeID != nil {

			baseQuery = baseQuery.Where("assignee_id = ?", *filter.AssigneeID)

		}

	}



	// Total count

	if err := baseQuery.Count(&stats.Total).Error; err != nil {

		return nil, err

	}



	// SLA breached count

	if err := r.db.WithContext(ctx).Model(&models.Incident{}).Where("sla_breached = ?", true).Count(&stats.SLABreached).Error; err != nil {

		return nil, err

	}



	// Count by state (filtered by viewable roles if provided)

	type stateCount struct {

		StateID   uuid.UUID `gorm:"column:state_id"`

		StateName string    `gorm:"column:state_name"`

		Count     int64     `gorm:"column:count"`

	}

	var stateCounts []stateCount

	stateQuery := r.db.WithContext(ctx).Model(&models.Incident{}).

		Select("workflow_states.id as state_id, workflow_states.name as state_name, count(*) as count").

		Joins("JOIN workflow_states ON workflow_states.id = incidents.current_state_id")



	// Filter by user roles if provided (empty viewable_roles = visible to all)

	if filter != nil && len(filter.UserRoleIDs) > 0 {

		stateQuery = stateQuery.Where(`

			NOT EXISTS (SELECT 1 FROM state_viewable_roles WHERE workflow_state_id = workflow_states.id)

			OR EXISTS (SELECT 1 FROM state_viewable_roles

			           WHERE workflow_state_id = workflow_states.id AND role_id IN ?)

		`, filter.UserRoleIDs)

	}



	if err := stateQuery.Group("workflow_states.id, workflow_states.name").Scan(&stateCounts).Error; err != nil {

		return nil, err

	}

	stats.ByStateDetails = make([]models.StateStatDetail, 0, len(stateCounts))

	for _, sc := range stateCounts {

		stats.ByState[sc.StateName] = sc.Count

		stats.ByStateDetails = append(stats.ByStateDetails, models.StateStatDetail{

			ID:    sc.StateID,

			Name:  sc.StateName,

			Count: sc.Count,

		})

	}



	return stats, nil

}

func (r *incidentRepository) GetSLABreachedIncidents(ctx context.Context) ([]models.Incident, error) {
	var incidents []models.Incident
	err := r.db.WithContext(ctx).
		Preload("CurrentState").
		Preload("Assignee").
		Where("sla_breached = ? OR (sla_deadline IS NOT NULL AND sla_deadline < ? AND sla_breached = ?)", true, time.Now(), false).
		Find(&incidents).Error
	return incidents, err
}

func (r *incidentRepository) UpdateSLABreached(ctx context.Context, incidentID uuid.UUID, breached bool) error {
	return r.db.WithContext(ctx).
		Model(&models.Incident{}).
		Where("id = ?", incidentID).
		Update("sla_breached", breached).Error
}

func (r *incidentRepository) MarkSLABreached(ctx context.Context) (int64, error) {
	// Find and update all incidents that have passed their SLA deadline
	// but aren't marked as breached yet, and are not in a terminal state
	result := r.db.WithContext(ctx).
		Model(&models.Incident{}).
		Where("sla_deadline IS NOT NULL").
		Where("sla_deadline < ?", time.Now()).
		Where("sla_breached = ?", false).
		Where("current_state_id NOT IN (SELECT id FROM workflow_states WHERE state_type = 'terminal')").
		Update("sla_breached", true)

	return result.RowsAffected, result.Error
}

// User-specific queries

func (r *incidentRepository) GetAssignedToUser(ctx context.Context, userID uuid.UUID, recordType string, page, limit int) ([]models.Incident, int64, error) {
	var incidents []models.Incident
	var total int64

	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	offset := (page - 1) * limit

	baseQuery := r.db.WithContext(ctx).Model(&models.Incident{}).Where("assignee_id = ?", userID)

	// Filter by record_type if provided
	if recordType != "" {
		baseQuery = baseQuery.Where("record_type = ?", recordType)
	}

	if err := baseQuery.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	err := baseQuery.
		Preload("CurrentState").
		Preload("Workflow").
		Order("created_at DESC").
		Offset(offset).
		Limit(limit).
		Find(&incidents).Error
	if err != nil {
		return nil, 0, err
	}

	return incidents, total, nil
}

func (r *incidentRepository) GetReportedByUser(ctx context.Context, userID uuid.UUID, recordType string, page, limit int) ([]models.Incident, int64, error) {
	var incidents []models.Incident
	var total int64

	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	offset := (page - 1) * limit

	baseQuery := r.db.WithContext(ctx).Model(&models.Incident{}).Where("reporter_id = ?", userID)

	// Filter by record_type if provided
	if recordType != "" {
		baseQuery = baseQuery.Where("record_type = ?", recordType)
	}

	if err := baseQuery.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	err := baseQuery.
		Preload("CurrentState").
		Preload("Workflow").
		Order("created_at DESC").
		Offset(offset).
		Limit(limit).
		Find(&incidents).Error
	if err != nil {
		return nil, 0, err
	}

	return incidents, total, nil
}

// Revisions

func (r *incidentRepository) CreateRevision(ctx context.Context, revision *models.IncidentRevision) error {
	return r.db.WithContext(ctx).Create(revision).Error
}

func (r *incidentRepository) ListRevisions(ctx context.Context, filter *models.IncidentRevisionFilter) ([]models.IncidentRevision, int64, error) {
	var revisions []models.IncidentRevision
	var total int64

	page := filter.Page
	limit := filter.Limit
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > 100 {
		limit = 20
	}
	offset := (page - 1) * limit

	query := r.db.WithContext(ctx).Model(&models.IncidentRevision{}).Where("incident_id = ?", filter.IncidentID)

	if filter.ActionType != nil {
		query = query.Where("action_type = ?", *filter.ActionType)
	}
	if filter.PerformedByID != nil {
		query = query.Where("performed_by_id = ?", *filter.PerformedByID)
	}
	if filter.StartDate != nil {
		query = query.Where("created_at >= ?", *filter.StartDate)
	}
	if filter.EndDate != nil {
		query = query.Where("created_at <= ?", *filter.EndDate)
	}

	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	err := query.
		Preload("PerformedBy").
		Preload("PerformedBy.Roles").
		Order("revision_number ASC").
		Offset(offset).
		Limit(limit).
		Find(&revisions).Error
	if err != nil {
		return nil, 0, err
	}

	return revisions, total, nil
}

func (r *incidentRepository) GetNextRevisionNumber(ctx context.Context, incidentID uuid.UUID) (int, error) {
	var maxNum int
	err := r.db.WithContext(ctx).
		Model(&models.IncidentRevision{}).
		Select("COALESCE(MAX(revision_number), 0)").
		Where("incident_id = ?", incidentID).
		Scan(&maxNum).Error
	if err != nil {
		return 0, err
	}
	return maxNum + 1, nil
}

// Feedback

func (r *incidentRepository) CreateFeedback(ctx context.Context, feedback *models.IncidentFeedback) error {
	return r.db.WithContext(ctx).Create(feedback).Error
}

func (r *incidentRepository) FindFeedbackByID(ctx context.Context, id uuid.UUID) (*models.IncidentFeedback, error) {
	var feedback models.IncidentFeedback
	err := r.db.WithContext(ctx).
		Preload("CreatedBy").
		First(&feedback, "id = ?", id).Error
	if err != nil {
		return nil, err
	}
	return &feedback, nil
}

func (r *incidentRepository) ListFeedback(ctx context.Context, incidentID uuid.UUID) ([]models.IncidentFeedback, error) {
	var feedback []models.IncidentFeedback
	err := r.db.WithContext(ctx).
		Preload("CreatedBy").
		Where("incident_id = ?", incidentID).
		Order("created_at DESC").
		Find(&feedback).Error
	return feedback, err
}

func (r *incidentRepository) LinkFeedbackToTransition(ctx context.Context, feedbackID uuid.UUID, transitionHistoryID uuid.UUID) error {
	return r.db.WithContext(ctx).
		Model(&models.IncidentFeedback{}).
		Where("id = ?", feedbackID).
		Update("transition_history_id", transitionHistoryID).Error
}

// Complaint-specific

func (r *incidentRepository) IncrementEvaluationCount(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).
		Model(&models.Incident{}).
		Where("id = ?", id).
		Where("record_type = 'complaint'").
		Update("evaluation_count", gorm.Expr("evaluation_count + 1")).Error
}
