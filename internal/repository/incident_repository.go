package repository

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/pkg/utils"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrVersionMismatch = errors.New("incident was modified by another user")

type IncidentRepository interface {
	// Incident CRUD
	Create(ctx context.Context, incident *models.Incident) error
	FindByID(ctx context.Context, id uuid.UUID) (*models.Incident, error)
	FindByIDWithRelations(ctx context.Context, id uuid.UUID) (*models.Incident, error)
	FindByIncidentNumber(ctx context.Context, number string) (*models.Incident, error)
	FindByIDs(ctx context.Context, ids []uuid.UUID) ([]models.Incident, error)
	List(ctx context.Context, filter *models.IncidentFilter) ([]models.Incident, int64, error)
	ListMapMarkers(ctx context.Context, filter *models.IncidentFilter, maxMarkers int) ([]models.IncidentMapMarker, int64, error)
	Update(ctx context.Context, incident *models.Incident) error
	UpdateFields(ctx context.Context, id uuid.UUID, updates map[string]interface{}) error
	UpdateFieldsWithVersion(ctx context.Context, id uuid.UUID, updates map[string]interface{}, expectedVersion int) error
	UpdateReporterNameByPhone(ctx context.Context, phone, reporterName string) error
	Delete(ctx context.Context, id uuid.UUID) error
	WithTx(tx *gorm.DB) IncidentRepository
	LockForUpdate(ctx context.Context, tx *gorm.DB, id uuid.UUID) (*models.Incident, error)
	FindUserOpenIncidentsForDuplicateCheck(ctx context.Context, reporterID uuid.UUID) ([]models.Incident, error)
	FindOpenIncidentsForDuplicateCheckByCaller(ctx context.Context, reporterPhone string) ([]models.Incident, error)
	FindByIDWithLast6DigitValidation(ctx context.Context, req *models.IncidentUpdateIVRRequest) (*models.Incident, error)
	// Incident number generation
	GenerateIncidentNumber(ctx context.Context) (string, error)
	GenerateRequestNumber(ctx context.Context) (string, error)
	GenerateComplaintNumber(ctx context.Context) (string, error)
	GenerateQueryNumber(ctx context.Context) (string, error)

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
	IsUserAssignee(ctx context.Context, incidentID, userID uuid.UUID) (bool, error)
	SetAssignees(ctx context.Context, incidentID uuid.UUID, userIDs []uuid.UUID) error
	ClearAssignees(ctx context.Context, incidentID uuid.UUID) error
	SetLookupValues(ctx context.Context, incidentID uuid.UUID, lookupValues []models.LookupValue) error
	AppendLookupValues(ctx context.Context, incidentID uuid.UUID, lookupValues []models.LookupValue) error

	// Stats
	GetStats(ctx context.Context, filter *models.IncidentFilter) (*models.IncidentStatsResponse, error)
	GetStatsV2(ctx context.Context, filter *models.IncidentFilter) (*models.IncidentStatsResponseV2, error)
	GetPriorityCounts(ctx context.Context, filter *models.IncidentFilter) (map[string]int64, error)
	GetSLABreachedIncidents(ctx context.Context) ([]models.Incident, error)
	UpdateSLABreached(ctx context.Context, incidentID uuid.UUID, breached bool) error
	MarkSLABreached(ctx context.Context) (int64, error)
	GetBreachedByFilter(ctx context.Context, locationID uuid.UUID, classificationID uuid.UUID) ([]models.Incident, error)
	GetIncidentsExceedingStateSLA(ctx context.Context) ([]models.Incident, error)
	GetNewlyBreachedIncidents(ctx context.Context) ([]models.Incident, error)

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
	ListAllFeedback(ctx context.Context) ([]models.IncidentFeedback, error)
	LinkFeedbackToTransition(ctx context.Context, feedbackID uuid.UUID, transitionHistoryID uuid.UUID) error
	HasWhatsAppFeedback(ctx context.Context, incidentID uuid.UUID) (bool, error)

	// Notifications
	CreateNotification(ctx context.Context, notification *models.NotificationLog) error

	// Auto-assign monitor
	FindUnassignedByStateCode(ctx context.Context, stateCode string) ([]models.Incident, error)

	// Complaint-specific
	IncrementEvaluationCount(ctx context.Context, id uuid.UUID) error
	GetComplaintSourceValidation(ctx context.Context, sourceID uuid.UUID, reporterPhone string) (*models.ComplaintSourceValidation, error)

	// Report section queries
	GetReportIncidentData(ctx context.Context, incidentID uuid.UUID) (*models.IncidentReportData, error)
	GetReportLookupValues(ctx context.Context, incidentID uuid.UUID) ([]models.IncidentReportLookupValue, error)
	GetReportTransitions(ctx context.Context, incidentID uuid.UUID) ([]models.IncidentReportTransition, error)
	GetReportAttachments(ctx context.Context, incidentID uuid.UUID) ([]models.IncidentReportAttachment, error)
	GetReportRevisions(ctx context.Context, incidentID uuid.UUID) ([]models.IncidentReportRevision, error)
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
		Preload("Workflow.Transitions.AllowedRoles").
		Preload("CurrentState").
		Preload("CurrentState.EditableRoles").
		Preload("CurrentState.ViewableRoles").
		Preload("CurrentState.AssignmentRoles").
		Preload("Assignee").
		Preload("Assignees").
		Preload("Department").
		Preload("ExternalEntity").
		Preload("Location").
		Preload("LookupValues.Category").
		Preload("Reporter").
		Preload("SourceIncident").
		Preload("ConvertedRequest").
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
		Preload("TransitionHistory.Transition").
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

func (r *incidentRepository) FindByIDs(ctx context.Context, ids []uuid.UUID) ([]models.Incident, error) {
	var incidents []models.Incident
	err := r.db.WithContext(ctx).
		Preload("CurrentState").
		Preload("Workflow").
		Preload("Classification").
		Preload("Assignee").
		Preload("Department").
		Preload("Location").
		Preload("Reporter").
		Where("id IN ?", ids).
		Find(&incidents).Error
	return incidents, err
}

// normalizeStateCodes trims and lower-cases incoming workflow-state codes, dropping blanks.
// Comparison is case-insensitive because workflow_states carries inconsistent casing - both 'cl'
// and 'Cl' exist across different workflows - so an exact match would silently return incomplete
// results for those states.
func normalizeStateCodes(in []string) []string {
	out := make([]string, 0, len(in))
	for _, c := range in {
		if c = strings.TrimSpace(c); c != "" {
			out = append(out, strings.ToLower(c))
		}
	}
	return out
}

// reporterPhoneVariants returns every shape a phone number may have been stored
// in, so a caller-supplied value matches regardless of the channel that captured
// it: bare, with a country code, with the national trunk zero, and the historic
// country-code-plus-trunk-zero form.
//
// Used for exact matching only. The ILIKE search filters below do not need it,
// because a substring pattern built from the bare number already matches the
// stored trunk and country-code forms.
func reporterPhoneVariants(supplied string) []string {
	return utils.MobileMatchVariants(supplied)
}

// stateCodeSubquery matches incidents whose current state carries one of the given codes. Written as
// an IN-subquery rather than a JOIN on purpose: List shares one *gorm.DB between Count and Find, and
// several stats queries already join workflow_states, so an added join would either duplicate theirs
// or change the count.
//
// Soft-deleted states are deliberately NOT excluded. Live incidents do sit in soft-deleted states
// (6 of them today, across 2 states, including a deleted 'new'), and an incident's current state is
// that state whether or not an admin has since removed it. Filtering them out would make those
// incidents unmatchable by any code, and would also make this filter behave differently from
// `current_state_id IN ?`, which matches by id regardless of state deletion.
const stateCodeSubquery = `current_state_id IN (
	SELECT id FROM workflow_states WHERE LOWER(code) IN ?)`

// applyIncidentFilters applies the full IncidentFilter WHERE-clause set to
// query. Shared by List and ListMapMarkers so the two stay in sync — a filter
// added to one automatically applies to the other.
func (r *incidentRepository) applyIncidentFilters(ctx context.Context, query *gorm.DB, filter *models.IncidentFilter) *gorm.DB {
	// Apply filters
	if len(filter.IncidentIDs) != 0 {
		query = query.Where("incidents.id IN ?", filter.IncidentIDs)
	}
	if len(filter.WorkflowID) != 0 {
		query = query.Where("incidents.workflow_id IN ?", filter.WorkflowID)
	}
	if len(filter.CurrentStateID) != 0 {
		query = query.Where("current_state_id IN ?", filter.CurrentStateID)
	}
	if codes := normalizeStateCodes(filter.CurrentStateCode); len(codes) != 0 {
		query = query.Where(stateCodeSubquery, codes)
	}
	if len(filter.ClassificationID) != 0 {
		query = query.Where("classification_id IN ?", filter.ClassificationID)
	}
	if filter.Priority != nil {
		query = query.Where(`incidents.id IN (
			SELECT ilv.incident_id FROM incident_lookup_values ilv
			INNER JOIN lookup_values lv ON lv.id = ilv.lookup_value_id
			INNER JOIN lookup_categories lc ON lc.id = lv.category_id
			WHERE lc.code = 'PRIORITY' AND lv.sort_order = ?
		)`, *filter.Priority)
	}
	if len(filter.AssigneeID) != 0 {
		if len(filter.AssigneeID) == 1 {
			// Check both primary assignee and multiple assignees
			query = query.Where("incidents.assignee_id = ? OR incidents.id IN (SELECT incident_id FROM incident_assignees WHERE user_id = ?)", filter.AssigneeID[0], filter.AssigneeID[0])
		} else {
			// Multiple assignees
			query = query.Where("assignee_id IN ? ", filter.AssigneeID)
		}
	}
	if len(filter.DepartmentID) != 0 {
		if filter.IncludeUnassignedDepartment {
			query = query.Where("department_id IN ? OR (department_id IS NULL AND current_state_id IN (SELECT id FROM workflow_states WHERE state_type = 'initial') AND (assignee_id = ? OR reporter_id = ? OR id IN (SELECT incident_id FROM incident_assignees WHERE user_id = ?)))",
				filter.DepartmentID, filter.UserID, filter.UserID, filter.UserID)
		} else {
			query = query.Where("department_id IN ?", filter.DepartmentID)
		}
	}
	// fix bleow filter for location and reporter to handle multiple values

	if len(filter.LocationID) != 0 {
		query = query.Where("location_id IN ?", filter.LocationID)
	}
	if len(filter.ReporterID) != 0 {
		query = query.Where("reporter_id IN ?", filter.ReporterID)
	}
	if filter.MyRecord != nil {
		if len(filter.UserRoleIDs) > 0 {
			// Expand to include incidents in states where the user's role has viewable access
			query = query.Where(
				`incidents.reporter_id = ? OR incidents.assignee_id = ?
				OR incidents.id IN (SELECT incident_id FROM incident_assignees WHERE user_id = ?)
				OR incidents.current_state_id IN (SELECT workflow_state_id FROM state_viewable_roles WHERE role_id IN ?)`,
				filter.MyRecord, filter.MyRecord, filter.MyRecord, filter.UserRoleIDs,
			)
		} else {
			query = query.Where("incidents.reporter_id = ? OR incidents.assignee_id = ? OR incidents.id IN (SELECT incident_id FROM incident_assignees WHERE user_id = ?)", filter.MyRecord, filter.MyRecord, filter.MyRecord)
		}
	}

	if filter.ReporterPhone != "" {
		phone := strings.TrimPrefix(strings.TrimSpace(filter.ReporterPhone), "+")
		phone = strings.TrimLeft(phone, "0")
		if phone != "" {
			phonePattern := "%" + phone + "%"
			query = query.Where("incidents.reporter_phone ILIKE ? OR incidents.reporter_id IN (SELECT id FROM users WHERE phone ILIKE ?)",
				phonePattern, phonePattern)
		}
	}
	// this to avoid EPM Citizen Portal security boundary
	if filter.ReporterPhoneSearch != "" {
		// A literal "+" in a query string is decoded as a space by
		// form-encoding rules (e.g. ?reporter_phone_search=+919120912012
		// arrives here as " 919120912012"), so trim before stripping the
		// "+" prefix or the leading space stays embedded in the pattern
		// and silently matches nothing.
		phone := strings.TrimPrefix(strings.TrimSpace(filter.ReporterPhoneSearch), "+")
		phone = strings.TrimLeft(phone, "0")
		if phone != "" {
			phonePattern := "%" + phone + "%"
			query = query.Where("incidents.reporter_phone ILIKE ? OR incidents.reporter_id IN (SELECT id FROM users WHERE phone ILIKE ?)",
				phonePattern, phonePattern)
		}
	}

	if filter.CallerIdentity != "" {
		query = query.Where("caller_identity = ?", filter.CallerIdentity)
	}
	if filter.SLABreached != nil {
		query = query.Where("sla_breached = ?", *filter.SLABreached)
	}
	if filter.RecordType != nil {
		query = query.Where(
			"incidents.record_type = ? AND incidents.workflow_id IN (SELECT id FROM workflows WHERE record_type = ? AND deleted_at IS NULL)",
			*filter.RecordType, *filter.RecordType)
	}

	if filter.Channel != nil && *filter.Channel != "" {
		query = query.Where("channel = ?", *filter.Channel)
	}
	if filter.Source != nil && *filter.Source != "" {
		query = query.Where("LOWER(source) = LOWER(?)", *filter.Source)
	}
	if filter.SourceIncidentID != nil && *filter.SourceIncidentID != "" {
		query = query.Where("source_incident_id = ?", *filter.SourceIncidentID)
	}
	if filter.ConvertedToRequest != nil {
		if *filter.ConvertedToRequest {
			query = query.Where("converted_request_id IS NOT NULL")
		} else {
			query = query.Where("converted_request_id IS NULL")
		}
	}
	if filter.StartDate != nil {
		query = query.Where("incidents.created_at >= ?", *filter.StartDate)
	}
	if filter.EndDate != nil {
		query = query.Where("incidents.created_at <= ?", *filter.EndDate)
	}
	if filter.Search != "" {
		searchPattern := "%" + filter.Search + "%"
		query = query.Where("incidents.incident_number ILIKE ? OR incidents.title ILIKE ? OR incidents.description ILIKE ?", searchPattern, searchPattern, searchPattern)
	}
	if filter.TaskID != "" {
		query = query.Where("NULLIF(custom_fields, '')::jsonb -> 'lookup:TASK ID' ->> 'value' ILIKE ?", "%"+filter.TaskID+"%")
	}
	// momra_ref searches the flat momra_incident_no key written by the EPM insert path.
	if filter.MomraRef != "" {
		query = query.Where("NULLIF(custom_fields, '')::jsonb ->> 'momra_incident_no' ILIKE ?", "%"+filter.MomraRef+"%")
	}
	// Flat custom_fields filters: cf=key:value (AND-ed)
	for _, cf := range filter.CustomFieldFilters {
		query = query.Where("NULLIF(custom_fields, '')::jsonb ->> ? ILIKE ?", cf.Key, "%"+cf.Value+"%")
	}

	// Transition filters — JOIN only when needed
	if filter.TransitionID != nil || filter.FromStateID != nil || filter.ToStateID != nil {
		// Build a subquery to get matching incident IDs
		subQuery := r.db.WithContext(ctx).
			Table("incident_transition_histories ith").
			Select("DISTINCT ith.incident_id").
			Joins("JOIN workflow_transitions wt ON wt.id = ith.transition_id")

		if filter.TransitionID != nil {
			subQuery = subQuery.Where("wt.id = ?", *filter.TransitionID)
		}
		if filter.FromStateID != nil {
			subQuery = subQuery.Where("wt.from_state_id = ?", *filter.FromStateID)
		}
		if filter.ToStateID != nil {
			subQuery = subQuery.Where("wt.to_state_id = ?", *filter.ToStateID)
		}

		// Use IN subquery on main query — no JOIN, no DISTINCT needed
		query = query.Where("incidents.id IN (?)", subQuery)
	}

	return query
}

func (r *incidentRepository) List(ctx context.Context, filter *models.IncidentFilter) ([]models.Incident, int64, error) {
	var incidents []models.Incident
	var total int64

	query := r.applyIncidentFilters(ctx, r.db.WithContext(ctx).Model(&models.Incident{}), filter)

	// Single unified count — works for all cases
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	log.Printf("[List] total=%d classification_id=%v location_id=%v my_record=%v record_type=%v user_role_ids=%v",
		total, filter.ClassificationID, filter.LocationID, filter.MyRecord, filter.RecordType, filter.UserRoleIDs)
	// Apply pagination
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.Limit < 1 || filter.Limit > 100 {
		filter.Limit = 20
	}
	offset := (filter.Page - 1) * filter.Limit

	// Defaults to created_at (unchanged existing behavior for callers that
	// don't pass sort_by). Restricted to this allow-list regardless of the
	// model-level validation, since it's interpolated directly into ORDER BY.
	sortColumn := "created_at"
	if filter.SortBy == "updated_at" {
		sortColumn = "updated_at"
	}

	err := query.
		Preload("Classification").
		Preload("TransitionHistory").
		Preload("TransitionHistory.PerformedBy").
		Preload("Reporter").
		Preload("Workflow").
		Preload("CurrentState").
		Preload("Assignee").
		Preload("Department").
		Preload("Location").
		Preload("LookupValues.Category").
		Preload("TransitionHistory.Transition").
		Order(sortColumn + " DESC").
		Offset(offset).
		Limit(filter.Limit).
		Find(&incidents).Error
	if err != nil {
		log.Print(err)
		return nil, 0, err
	}

	return incidents, total, nil
}

// ListMapMarkers returns minimal per-incident fields for map plotting instead
// of full Incident rows — no Preloads, just id/coords/state color/priority.
// This keeps the payload small enough to fetch every matching marker in one
// request instead of paginating. maxMarkers caps the result defensively; the
// caller is told via the returned total whether the cap was hit.
func (r *incidentRepository) ListMapMarkers(ctx context.Context, filter *models.IncidentFilter, maxMarkers int) ([]models.IncidentMapMarker, int64, error) {
	var total int64

	base := r.applyIncidentFilters(ctx, r.db.WithContext(ctx).Model(&models.Incident{}), filter)
	// Where("incidents.latitude IS NOT NULL AND incidents.longitude IS NOT NULL")

	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var markers []models.IncidentMapMarker
	err := base.
		Select(`incidents.id, incidents.latitude, incidents.longitude, incidents.current_state_id,
			workflow_states.color AS state_color`).
		Joins("JOIN workflow_states ON workflow_states.id = incidents.current_state_id").
		Limit(maxMarkers).
		Find(&markers).Error
	if err != nil {
		return nil, 0, err
	}

	return markers, total, nil
}

func (r *incidentRepository) FindByIDWithLast6DigitValidation(
	ctx context.Context,
	req *models.IncidentUpdateIVRRequest,
) (*models.Incident, error) {

	var incident models.Incident

	err := r.db.WithContext(ctx).
		Preload("CurrentState").
		Preload("Workflow").
		Joins("JOIN users ON users.id = incidents.reporter_id").
		Where("incidents.id = ?", req.IncidentID).
		Where("RIGHT(users.phone, 6) = ?", req.LastPhoneDigits).
		First(&incident).Error

	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	return &incident, nil
}
func (r *incidentRepository) Update(ctx context.Context, incident *models.Incident) error {
	return r.db.WithContext(ctx).Save(incident).Error
}

func (r *incidentRepository) UpdateFields(ctx context.Context, id uuid.UUID, updates map[string]interface{}) error {
	return r.db.WithContext(ctx).Model(&models.Incident{}).Where("id = ?", id).Updates(updates).Error
}

// UpdateReporterNameByPhone overwrites reporter_name on every incident whose reporter_phone
// matches the given phone, keeping a citizen's name consistent across their incident history
// once it's known to have changed. Matches every stored shape of the number (bare, with
// country code, with the national trunk zero, etc.) via reporterPhoneVariants, the same way
// List/ListMapMarkers match reporter_phone.
func (r *incidentRepository) UpdateReporterNameByPhone(ctx context.Context, phone, reporterName string) error {
	return r.db.WithContext(ctx).Model(&models.Incident{}).
		Where("reporter_phone IN ?", reporterPhoneVariants(phone)).
		Update("reporter_name", reporterName).Error
}

func (r *incidentRepository) Delete(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).Delete(&models.Incident{}, "id = ?", id).Error
}

// Incident number generation - uses MAX to get the highest sequence number

func (r *incidentRepository) GenerateIncidentNumber(ctx context.Context) (string, error) {
	year := time.Now().Year()
	prefix := fmt.Sprintf("INC-%d-", year)
	var maxNumber *string
	err := r.db.WithContext(ctx).Unscoped().Model(&models.Incident{}).
		Select("MAX(incident_number)").
		Where("incident_number LIKE ? ", prefix+"%").
		Scan(&maxNumber).Error
	if err != nil {
		return "", err
	}
	nextSeq := 1
	if maxNumber != nil && *maxNumber != "" {
		// Extract sequence number from "INC-2026-000021"
		var seq int
		fmt.Sscanf(*maxNumber, "INC-%d-%d", &year, &seq)
		nextSeq = seq + 1
	}
	return fmt.Sprintf("INC-%d-%06d", year, nextSeq), nil
}

func (r *incidentRepository) GenerateRequestNumber(ctx context.Context) (string, error) {
	year := time.Now().Year()
	prefix := fmt.Sprintf("REQ-%d-", year)
	var maxNumber *string
	// Unscoped() to include soft-deleted rows in the MAX calculation
	err := r.db.WithContext(ctx).Unscoped().Model(&models.Incident{}).
		Select("MAX(incident_number)").Where("incident_number LIKE ?", prefix+"%").
		Scan(&maxNumber).Error
	if err != nil {
		return "", err
	}
	nextSeq := 1
	if maxNumber != nil && *maxNumber != "" {
		var seq int
		fmt.Sscanf(*maxNumber, "REQ-%d-%d", &year, &seq)
		nextSeq = seq + 1
	}
	return fmt.Sprintf("REQ-%d-%06d", year, nextSeq), nil
}

func (r *incidentRepository) GenerateComplaintNumber(ctx context.Context) (string, error) {
	year := time.Now().Year()
	prefix := fmt.Sprintf("COMP-%d-", year)
	var maxNumber *string
	err := r.db.WithContext(ctx).Unscoped().Model(&models.Incident{}).
		Select("MAX(incident_number)").
		Where("incident_number LIKE ?", prefix+"%").
		Scan(&maxNumber).Error
	if err != nil {
		return "", err
	}
	nextSeq := 1
	if maxNumber != nil && *maxNumber != "" {
		var seq int
		fmt.Sscanf(*maxNumber, "COMP-%d-%d", &year, &seq)
		nextSeq = seq + 1
	}
	return fmt.Sprintf("COMP-%d-%06d", year, nextSeq), nil
}

func (r *incidentRepository) GenerateQueryNumber(ctx context.Context) (string, error) {
	year := time.Now().Year()
	prefix := fmt.Sprintf("QRY-%d-", year)
	var maxNumber *string
	err := r.db.WithContext(ctx).Unscoped().Model(&models.Incident{}).
		Select("MAX(incident_number)").
		Where("incident_number LIKE ?", prefix+"%").
		Scan(&maxNumber).Error
	if err != nil {
		return "", err
	}
	nextSeq := 1
	if maxNumber != nil && *maxNumber != "" {
		var seq int
		fmt.Sscanf(*maxNumber, "QRY-%d-%d", &year, &seq)
		nextSeq = seq + 1
	}
	return fmt.Sprintf("QRY-%d-%06d", year, nextSeq), nil
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
		Preload("PerformedBy.Departments").
		Preload("Feedbacks").
		Preload("Feedbacks.CreatedBy").
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
		Preload("UploadedBy.Roles").
		Preload("UploadedBy.Departments").
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

func (r *incidentRepository) IsUserAssignee(ctx context.Context, incidentID, userID uuid.UUID) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&models.Incident{}).
		Where("id = ? AND (assignee_id = ? OR id IN (SELECT incident_id FROM incident_assignees WHERE user_id = ?))", incidentID, userID, userID).
		Count(&count).Error
	return count > 0, err
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

	// Deduplicate by category: last value per category wins.
	// Prevents storing multiple values from the same category (e.g. two Priorities).
	seen := make(map[uuid.UUID]int)
	var dedupValues []models.LookupValue
	for _, lv := range actualLookupValues {
		if idx, exists := seen[lv.CategoryID]; exists {
			dedupValues[idx] = lv
		} else {
			seen[lv.CategoryID] = len(dedupValues)
			dedupValues = append(dedupValues, lv)
		}
	}

	return r.db.WithContext(ctx).Model(&incident).Association("LookupValues").Replace(dedupValues)
}

// AppendLookupValues adds lookup values to an incident without removing existing ones.
// Unlike SetLookupValues, this allows multiple values per category (e.g. multiple priorities
// from different source incidents during convert-to-request).
func (r *incidentRepository) AppendLookupValues(ctx context.Context, incidentID uuid.UUID, lookupValues []models.LookupValue) error {
	var incident models.Incident
	if err := r.db.WithContext(ctx).First(&incident, "id = ?", incidentID).Error; err != nil {
		return err
	}

	// Fetch the actual LookupValue records from database
	lookupIDs := make([]uuid.UUID, len(lookupValues))
	for i, lv := range lookupValues {
		lookupIDs[i] = lv.ID
	}

	var actualLookupValues []models.LookupValue
	if err := r.db.WithContext(ctx).Where("id IN ?", lookupIDs).Find(&actualLookupValues).Error; err != nil {
		return err
	}

	return r.db.WithContext(ctx).Model(&incident).Association("LookupValues").Append(actualLookupValues)
}

// Stats

func (r *incidentRepository) GetStats(ctx context.Context, filter *models.IncidentFilter) (*models.IncidentStatsResponse, error) {

	stats := &models.IncidentStatsResponse{
		ByState: make(map[string]int64),
	}

	baseQuery := r.db.WithContext(ctx).Model(&models.Incident{})

	// Apply filters if provided
	if filter != nil {

		if filter.RecordType != nil && *filter.RecordType != "" {
			baseQuery = baseQuery.Where("record_type = ?", *filter.RecordType)
		}

		if len(filter.WorkflowID) != 0 {
			baseQuery = baseQuery.Where("workflow_id IN ?", filter.WorkflowID)
		}

		if len(filter.DepartmentID) != 0 {
			if filter.IncludeUnassignedDepartment {
				baseQuery = baseQuery.Where("department_id IN ? OR (department_id IS NULL AND current_state_id IN (SELECT id FROM workflow_states WHERE state_type = 'initial') AND (assignee_id = ? OR reporter_id = ? OR id IN (SELECT incident_id FROM incident_assignees WHERE user_id = ?)))",
					filter.DepartmentID, filter.UserID, filter.UserID, filter.UserID)
			} else {
				baseQuery = baseQuery.Where("department_id IN ?", filter.DepartmentID)
			}
		}

		if len(filter.AssigneeID) != 0 {
			baseQuery = baseQuery.Where(
				"assignee_id IN ? OR id IN (SELECT incident_id FROM incident_assignees WHERE user_id IN ?)",
				filter.AssigneeID,
				filter.AssigneeID,
			)
		}

		// Created by Me filter
		if filter.FilterType == "created" {
			baseQuery = baseQuery.Where("reporter_id = ?", filter.UserID)
		}

		// Assigned to Me filter
		if filter.FilterType == "assigned" {
			baseQuery = baseQuery.Where(`
				assignee_id = ? OR id IN (
					SELECT incident_id FROM incident_assignees WHERE user_id = ?
				)
			`, filter.UserID, filter.UserID)
		}
	}

	// Total count
	if err := baseQuery.Count(&stats.Total).Error; err != nil {
		return nil, err
	}

	// SLA breached count
	slaQuery := r.db.WithContext(ctx).
		Model(&models.Incident{}).
		Where("sla_breached = ?", true)

	if filter != nil {

		if filter.RecordType != nil && *filter.RecordType != "" {
			slaQuery = slaQuery.Where("record_type = ?", *filter.RecordType)
		}

		// Created by Me filter
		if filter.FilterType == "created" {
			slaQuery = slaQuery.Where("reporter_id = ?", filter.UserID)
		}
		// Assigned to Me filter
		if filter.FilterType == "assigned" {
			slaQuery = slaQuery.Where(`
				assignee_id = ? OR id IN (
					SELECT incident_id FROM incident_assignees WHERE user_id = ?
				)
			`, filter.UserID, filter.UserID)
		}
	}

	if err := slaQuery.Count(&stats.SLABreached).Error; err != nil {
		return nil, err
	}

	// Counts by state
	type stateCount struct {
		StateID     uuid.UUID `gorm:"column:state_id"`
		StateName   string    `gorm:"column:state_name"`
		StateNameAr string    `gorm:"column:state_name_ar"`
		StateCode   string    `gorm:"column:state_code"`
		Count       int64     `gorm:"column:count"`
	}

	var stateCounts []stateCount

	stateQuery := r.db.WithContext(ctx).
		Model(&models.Incident{}).
		Select("workflow_states.id as state_id, workflow_states.name as state_name, workflow_states.code as state_code, count(*) as count").
		Joins("JOIN workflow_states ON workflow_states.id = incidents.current_state_id")

	if filter != nil {

		if filter.RecordType != nil && *filter.RecordType != "" {
			stateQuery = stateQuery.Where("incidents.record_type = ?", *filter.RecordType)
		}

		// Created by Me filter
		if filter.FilterType == "created" {
			stateQuery = stateQuery.Where("incidents.reporter_id = ?", filter.UserID)
		}
		// Assigned to Me filter
		if filter.FilterType == "assigned" {
			stateQuery = stateQuery.Where(`
				incidents.assignee_id = ? OR incidents.id IN (
					SELECT incident_id FROM incident_assignees WHERE user_id = ?
				)
			`, filter.UserID, filter.UserID)
		}

		// Filter by role visibility
		if len(filter.UserRoleIDs) > 0 {
			stateQuery = stateQuery.Where(`
				NOT EXISTS (
					SELECT 1 FROM state_viewable_roles
					WHERE workflow_state_id = workflow_states.id
				)
				OR EXISTS (
					SELECT 1 FROM state_viewable_roles
					WHERE workflow_state_id = workflow_states.id
					AND role_id IN ?
				)
				OR incidents.reporter_id = ? OR incidents.assignee_id = ?
				OR incidents.id IN (SELECT incident_id FROM incident_assignees WHERE user_id = ?)
			`, filter.UserRoleIDs, filter.UserID, filter.UserID, filter.UserID)
		}
	}

	if err := stateQuery.
		Group("workflow_states.id, workflow_states.name, workflow_states.name_ar").
		Scan(&stateCounts).Error; err != nil {
		return nil, err
	}

	stats.ByStateDetails = make([]models.StateStatDetail, 0, len(stateCounts))

	for _, sc := range stateCounts {

		stats.ByState[sc.StateName] = sc.Count

		stats.ByStateDetails = append(stats.ByStateDetails, models.StateStatDetail{
			ID:     sc.StateID,
			Name:   sc.StateName,
			NameAr: sc.StateNameAr,
			Count:  sc.Count,
		})

		if sc.StateCode == "resolved" {
			stats.Resolved += sc.Count
		}
	}

	// Count by state_type
	type stateTypeCount struct {
		StateType string `gorm:"column:state_type"`
		Count     int64  `gorm:"column:count"`
	}

	var stateTypeCounts []stateTypeCount

	stateTypeQuery := r.db.WithContext(ctx).
		Model(&models.Incident{}).
		Select("workflow_states.state_type as state_type, count(*) as count").
		Joins("JOIN workflow_states ON workflow_states.id = incidents.current_state_id")

	if filter != nil {

		if filter.RecordType != nil && *filter.RecordType != "" {
			stateTypeQuery = stateTypeQuery.Where("incidents.record_type = ?", *filter.RecordType)
		}

		// Created by Me filter
		if filter.FilterType == "created" {
			stateTypeQuery = stateTypeQuery.Where("incidents.reporter_id = ?", filter.UserID)
		}
		// Assigned to Me Filter
		if filter.FilterType == "assigned" {
			stateTypeQuery = stateTypeQuery.Where(`
				incidents.assignee_id = ? OR incidents.id IN (
					SELECT incident_id FROM incident_assignees WHERE user_id = ?
				)
			`, filter.UserID, filter.UserID)
		}
	}

	if err := stateTypeQuery.
		Group("workflow_states.state_type").
		Scan(&stateTypeCounts).Error; err != nil {
		return nil, err
	}

	for _, stc := range stateTypeCounts {
		switch stc.StateType {
		case "initial":
			stats.Open = stc.Count
		case "normal":
			stats.InProgress = stc.Count
		case "terminal":
			stats.Closed = stc.Count
		}
	}

	return stats, nil
}

//stats api v2 (After all fixes  will deprecated Old api)

func (r *incidentRepository) GetStatsV2(ctx context.Context, filter *models.IncidentFilter) (*models.IncidentStatsResponseV2, error) {

	stats := &models.IncidentStatsResponseV2{}

	isAdmin := filter != nil && filter.IsAdmin

	// only shows workflows matching the requested record_type), WorkflowID, DepartmentID,
	applySharedFilters := func(q *gorm.DB) *gorm.DB {
		if filter == nil {
			return q
		}
		if filter.RecordType != nil && *filter.RecordType != "" {
			//q = q.Where("incidents.record_type = ?", *filter.RecordType) // all Workflow counts
			q = q.Where(
				"incidents.record_type = ? AND incidents.workflow_id IN (SELECT id FROM workflows WHERE record_type = ? AND deleted_at IS NULL)",
				*filter.RecordType, *filter.RecordType)
		}
		if len(filter.WorkflowID) > 0 {
			q = q.Where("incidents.workflow_id IN ?", filter.WorkflowID)
		}
		if len(filter.DepartmentID) > 0 {
			if filter.IncludeUnassignedDepartment {
				q = q.Where("incidents.department_id IN ? OR (incidents.department_id IS NULL AND incidents.current_state_id IN (SELECT id FROM workflow_states WHERE state_type = 'initial') AND (incidents.assignee_id = ? OR incidents.reporter_id = ? OR incidents.id IN (SELECT incident_id FROM incident_assignees WHERE user_id = ?)))",
					filter.DepartmentID, filter.UserID, filter.UserID, filter.UserID)
			} else {
				q = q.Where("incidents.department_id IN ?", filter.DepartmentID)
			}
		}
		if len(filter.AssigneeID) > 0 {
			q = q.Where(`incidents.assignee_id IN ? OR incidents.id IN (SELECT incident_id FROM incident_assignees WHERE user_id IN ?)`,
				filter.AssigneeID, filter.AssigneeID)
		}
		if filter.MyRecord != nil {
			if len(filter.UserRoleIDs) > 0 {
				q = q.Where(
					`incidents.reporter_id = ? OR incidents.assignee_id = ?
					OR incidents.id IN (SELECT incident_id FROM incident_assignees WHERE user_id = ?)
					OR incidents.current_state_id IN (SELECT workflow_state_id FROM state_viewable_roles WHERE role_id IN ?)`,
					filter.MyRecord, filter.MyRecord, filter.MyRecord, filter.UserRoleIDs,
				)
			} else {
				q = q.Where("incidents.reporter_id = ? OR incidents.assignee_id = ? OR incidents.id IN (SELECT incident_id FROM incident_assignees WHERE user_id = ?)", filter.MyRecord, filter.MyRecord, filter.MyRecord)
			}
		}
		if len(filter.ClassificationID) > 0 {
			q = q.Where("incidents.classification_id IN ?", filter.ClassificationID)
		}
		if filter.MomraRef != "" {
			q = q.Where("NULLIF(custom_fields, '')::jsonb ->> 'momra_incident_no' ILIKE ?", "%"+filter.MomraRef+"%")
		}
		if len(filter.LocationID) > 0 {
			q = q.Where("incidents.location_id IN ?", filter.LocationID)
		}

		// Extended list-table filters, kept in sync with List()'s filtering so
		// stats reflect exactly what the incident list table shows.
		if len(filter.CurrentStateID) > 0 {
			q = q.Where("incidents.current_state_id IN ?", filter.CurrentStateID)
		}
		if codes := normalizeStateCodes(filter.CurrentStateCode); len(codes) != 0 {
			q = q.Where("incidents."+stateCodeSubquery, codes)
		}
		if filter.Priority != nil {
			q = q.Where(`incidents.id IN (
				SELECT ilv.incident_id FROM incident_lookup_values ilv
				INNER JOIN lookup_values lv ON lv.id = ilv.lookup_value_id
				INNER JOIN lookup_categories lc ON lc.id = lv.category_id
				WHERE lc.code = 'PRIORITY' AND lv.sort_order = ?
			)`, *filter.Priority)
		}
		if len(filter.ReporterID) > 0 {
			q = q.Where("incidents.reporter_id IN ?", filter.ReporterID)
		}
		if filter.ReporterPhone != "" {
			phone := strings.TrimPrefix(strings.TrimSpace(filter.ReporterPhone), "+")
			phone = strings.TrimLeft(phone, "0")
			if phone != "" {
				phonePattern := "%" + phone + "%"
				q = q.Where("incidents.reporter_phone ILIKE ? OR incidents.reporter_id IN (SELECT id FROM users WHERE phone ILIKE ?)",
					phonePattern, phonePattern)
			}
		}
		if filter.ReporterPhoneSearch != "" {
			// A literal "+" in a query string is decoded as a space by
			// form-encoding rules (e.g. ?reporter_phone_search=+919120912012
			// arrives here as " 919120912012"), so trim before stripping the
			// "+" prefix or the leading space stays embedded in the pattern
			// and silently matches nothing. Kept in sync with the single-ILIKE
			// pattern in applyIncidentFilters.
			phone := strings.TrimPrefix(strings.TrimSpace(filter.ReporterPhoneSearch), "+")
			phone = strings.TrimLeft(phone, "0")
			if phone != "" {
				phonePattern := "%" + phone + "%"
				q = q.Where("incidents.reporter_phone ILIKE ? OR incidents.reporter_id IN (SELECT id FROM users WHERE phone ILIKE ?)",
					phonePattern, phonePattern)
			}
		}
		if filter.SLABreached != nil {
			q = q.Where("incidents.sla_breached = ?", *filter.SLABreached)
		}
		if filter.Source != nil && *filter.Source != "" {
			q = q.Where("LOWER(incidents.source) = LOWER(?)", *filter.Source)
		}
		if filter.ConvertedToRequest != nil {
			if *filter.ConvertedToRequest {
				q = q.Where("incidents.converted_request_id IS NOT NULL")
			} else {
				q = q.Where("incidents.converted_request_id IS NULL")
			}
		}
		if filter.StartDate != nil {
			q = q.Where("incidents.created_at >= ?", *filter.StartDate)
		}
		if filter.EndDate != nil {
			q = q.Where("incidents.created_at <= ?", *filter.EndDate)
		}
		if filter.Search != "" {
			searchPattern := "%" + filter.Search + "%"
			q = q.Where("incidents.incident_number ILIKE ? OR incidents.title ILIKE ? OR incidents.description ILIKE ?", searchPattern, searchPattern, searchPattern)
		}
		if filter.TransitionID != nil || filter.FromStateID != nil || filter.ToStateID != nil {
			subQuery := r.db.WithContext(ctx).
				Table("incident_transition_histories ith").
				Select("DISTINCT ith.incident_id").
				Joins("JOIN workflow_transitions wt ON wt.id = ith.transition_id")

			if filter.TransitionID != nil {
				subQuery = subQuery.Where("wt.id = ?", *filter.TransitionID)
			}
			if filter.FromStateID != nil {
				subQuery = subQuery.Where("wt.from_state_id = ?", *filter.FromStateID)
			}
			if filter.ToStateID != nil {
				subQuery = subQuery.Where("wt.to_state_id = ?", *filter.ToStateID)
			}

			q = q.Where("incidents.id IN (?)", subQuery)
		}

		return q
	}

	// applyBaseFilters: shared filters + FilterType for ALL users
	applyBaseFilters := func(q *gorm.DB) *gorm.DB {
		q = applySharedFilters(q)
		if filter == nil {
			return q
		}
		if filter.FilterType == "created" {
			q = q.Where("incidents.reporter_id = ?", filter.UserID)
		}
		if filter.FilterType == "assigned" {
			q = q.Where(`incidents.assignee_id = ? OR incidents.id IN (SELECT incident_id FROM incident_assignees WHERE user_id = ?)`,
				filter.UserID, filter.UserID)
		}
		return q
	}

	// applyCommonFilters: used only for stateQuery (WorkflowStats/ByStateDetails).
	// Non-admin → applies FilterType + role-visibility (permission-based).
	applyCommonFilters := func(q *gorm.DB) *gorm.DB {
		q = applySharedFilters(q)
		if filter == nil {
			return q
		}
		if !isAdmin {
			if filter.FilterType == "created" {
				q = q.Where("incidents.reporter_id = ?", filter.UserID)
			}
			if filter.FilterType == "assigned" {
				q = q.Where(`incidents.assignee_id = ? OR incidents.id IN (SELECT incident_id FROM incident_assignees WHERE user_id = ?)`,
					filter.UserID, filter.UserID)
			}
			isPersonalFilter := filter.FilterType == "assigned" || filter.FilterType == "created"
			if len(filter.UserRoleIDs) > 0 && !isPersonalFilter {
				q = q.Where(`
					NOT EXISTS (SELECT 1 FROM state_viewable_roles WHERE workflow_state_id = incidents.current_state_id)
					OR EXISTS (SELECT 1 FROM state_viewable_roles WHERE workflow_state_id = incidents.current_state_id AND role_id IN ?)
					OR incidents.reporter_id = ? OR incidents.assignee_id = ?
					OR incidents.id IN (SELECT incident_id FROM incident_assignees WHERE user_id = ?)
				`, filter.UserRoleIDs, filter.UserID, filter.UserID, filter.UserID)
			}
		}
		return q
	}

	//  Total
	baseQuery := applyBaseFilters(
		r.db.WithContext(ctx).Model(&models.Incident{}).Debug().
			Where("incidents.workflow_id IN (SELECT id FROM workflows WHERE deleted_at IS NULL)"),
	)
	if err := baseQuery.Count(&stats.Total).Error; err != nil {
		return nil, err
	}
	log.Printf("[GetStatsV2] total=%d is_admin=%v classification_id=%v location_id=%v my_record=%v record_type=%v user_role_ids=%v",
		stats.Total, isAdmin, filter.ClassificationID, filter.LocationID, filter.MyRecord, filter.RecordType, filter.UserRoleIDs)

	// SLA Breached
	slaQuery := applyBaseFilters(
		r.db.WithContext(ctx).Model(&models.Incident{}).
			Where("sla_breached = ? AND incidents.workflow_id IN (SELECT id FROM workflows WHERE deleted_at IS NULL)", true),
	)
	if err := slaQuery.Count(&stats.SLABreached).Error; err != nil {
		return nil, err
	}

	// Partial Close (incidents currently in a partial_close state)
	pcQuery := applyBaseFilters(
		r.db.WithContext(ctx).Model(&models.Incident{}).
			Joins("JOIN workflow_states ON workflow_states.id = incidents.current_state_id").
			Where("workflow_states.is_partial_close = ? AND incidents.workflow_id IN (SELECT id FROM workflows WHERE deleted_at IS NULL)", true),
	)
	if err := pcQuery.Count(&stats.PartialClose).Error; err != nil {
		// Non-fatal: column may not exist yet on older schema — partial_close stays 0
		log.Printf("[GetStatsV2] partial_close count query failed (schema may need migration): %v", err)
	}

	// Per-state counts (workflow_stats + by_state)
	type stateCount struct {
		WorkflowID   uuid.UUID `gorm:"column:workflow_id"`
		WorkflowName string    `gorm:"column:workflow_name"`
		StateID      uuid.UUID `gorm:"column:state_id"`
		StateName    string    `gorm:"column:state_name"`
		StateNameAr  string    `gorm:"column:state_name_ar"`
		StateCode    string    `gorm:"column:state_code"`
		StateType    string    `gorm:"column:state_type"`
		Count        int64     `gorm:"column:count"`
	}
	var stateCounts []stateCount

	// LEFT JOIN approach: start from workflow_states so states with zero matching
	// incidents still appear in the result with count=0.
	incidentSubQ := applyCommonFilters(
		r.db.WithContext(ctx).Model(&models.Incident{}).
			Select("incidents.id, incidents.current_state_id").
			Where("incidents.workflow_id IN (SELECT id FROM workflows WHERE deleted_at IS NULL)"),
	)

	stateQuery := r.db.WithContext(ctx).
		Table("workflow_states ws").
		Select(`
			w.id    as workflow_id,
			w.name  as workflow_name,
			ws.id         as state_id,
			ws.name       as state_name,
			ws.name_ar    as state_name_ar,
			ws.code       as state_code,
			ws.state_type as state_type,
			COUNT(i.id)   as count
		`).
		Joins("JOIN workflows w ON w.id = ws.workflow_id AND w.deleted_at IS NULL").
		Joins("LEFT JOIN (?) AS i ON i.current_state_id = ws.id", incidentSubQ).
		Where("ws.deleted_at IS NULL")

	if filter != nil {
		if filter.RecordType != nil && *filter.RecordType != "" {
			stateQuery = stateQuery.Where("w.record_type = ?", *filter.RecordType)
		}
		if len(filter.WorkflowID) > 0 {
			stateQuery = stateQuery.Where("w.id IN ?", filter.WorkflowID)
		}
		if !isAdmin && len(filter.UserRoleIDs) > 0 {
			isPersonalFilter := filter.FilterType == "assigned" || filter.FilterType == "created"
			if !isPersonalFilter {
				stateQuery = stateQuery.Where(`
					NOT EXISTS (SELECT 1 FROM state_viewable_roles WHERE workflow_state_id = ws.id)
					OR EXISTS (SELECT 1 FROM state_viewable_roles WHERE workflow_state_id = ws.id AND role_id IN ?)
					OR EXISTS (SELECT 1 FROM incidents WHERE current_state_id = ws.id AND deleted_at IS NULL
						AND (reporter_id = ? OR assignee_id = ? OR id IN (SELECT incident_id FROM incident_assignees WHERE user_id = ?)))
				`, filter.UserRoleIDs, filter.UserID, filter.UserID, filter.UserID)
			}
		}
	}

	if err := stateQuery.
		Group(`w.id, w.name, ws.id, ws.name, ws.name_ar, ws.code, ws.state_type`).
		Order("w.name ASC, ws.state_type ASC").
		Scan(&stateCounts).Error; err != nil {
		return nil, err
	}

	type stateTypeCount struct {
		StateType string `gorm:"column:state_type"`
		StateCode string `gorm:"column:state_code"`
		Count     int64  `gorm:"column:count"`
	}
	var stateTypeCounts []stateTypeCount

	stateTypeQuery := applyBaseFilters(
		r.db.WithContext(ctx).Model(&models.Incident{}).
			Select("workflow_states.state_type as state_type, workflow_states.code as state_code, count(*) as count").
			//Joins("JOIN workflow_states ON workflow_states.id = incidents.current_state_id").
			Joins("JOIN workflow_states ON workflow_states.id = incidents.current_state_id AND workflow_states.deleted_at IS NULL").
			Where("incidents.workflow_id IN (SELECT id FROM workflows WHERE deleted_at IS NULL)"),
	)

	if err := stateTypeQuery.
		Group("workflow_states.state_type, workflow_states.code").
		Scan(&stateTypeCounts).Error; err != nil {
		return nil, err
	}

	for _, stc := range stateTypeCounts {
		switch stc.StateType {
		case "initial":
			stats.Open += stc.Count
		case "normal":
			stats.InProgress += stc.Count
		case "terminal":
			stats.Closed += stc.Count
		}
		if stc.StateCode == "resolved" {
			stats.Resolved += stc.Count
		}
	}

	//  Build workflow_stats

	filterType := ""
	if filter != nil {
		filterType = filter.FilterType
	}
	hasRecordType := filter != nil && filter.RecordType != nil && *filter.RecordType != ""
	hasTypeFilter := filterType == "created" || filterType == "assigned"

	if hasRecordType || hasTypeFilter {
		workflowMap := make(map[uuid.UUID]*models.WorkflowStats)
		for _, sc := range stateCounts {
			if _, exists := workflowMap[sc.WorkflowID]; !exists {
				workflowMap[sc.WorkflowID] = &models.WorkflowStats{
					WorkflowID:     sc.WorkflowID,
					WorkflowName:   sc.WorkflowName,
					ByState:        make(map[string]int64),
					ByStateDetails: []models.StateStatDetail{},
				}
			}
			wf := workflowMap[sc.WorkflowID]
			wf.ByState[sc.StateName] += sc.Count
			wf.ByStateDetails = append(wf.ByStateDetails, models.StateStatDetail{
				ID:     sc.StateID,
				Name:   sc.StateName,
				NameAr: sc.StateNameAr,
				Count:  sc.Count,
			})
		}
		for _, wf := range workflowMap {
			stats.WorkflowStats = append(stats.WorkflowStats, *wf)
		}
	}

	return stats, nil
}

func (r *incidentRepository) GetPriorityCounts(ctx context.Context, filter *models.IncidentFilter) (map[string]int64, error) {
	// Query to count incidents grouped by priority
	type priorityCount struct {
		PriorityName string `gorm:"column:priority_name"`
		SortOrder    int    `gorm:"column:sort_order"`
		Count        int64  `gorm:"column:count"`
	}

	query := r.db.WithContext(ctx).Model(&models.Incident{}).
		Select("lookup_values.name as priority_name, lookup_values.sort_order as sort_order, count(*) as count").
		Joins("INNER JOIN incident_lookup_values ON incident_lookup_values.incident_id = incidents.id").
		Joins("INNER JOIN lookup_values ON lookup_values.id = incident_lookup_values.lookup_value_id").
		Joins("INNER JOIN lookup_categories ON lookup_categories.id = lookup_values.category_id").
		Where("lookup_categories.code = ?", "PRIORITY")

	// Apply filters if provided
	if filter != nil {
		if filter.RecordType != nil && *filter.RecordType != "" {
			query = query.Where("incidents.record_type = ?", *filter.RecordType)
		}

		if len(filter.WorkflowID) != 0 {
			query = query.Where("incidents.workflow_id IN ?", filter.WorkflowID)
		}

		if len(filter.DepartmentID) != 0 {
			if filter.IncludeUnassignedDepartment {
				query = query.Where("incidents.department_id IN ? OR (incidents.department_id IS NULL AND incidents.current_state_id IN (SELECT id FROM workflow_states WHERE state_type = 'initial') AND (incidents.assignee_id = ? OR incidents.reporter_id = ? OR incidents.id IN (SELECT incident_id FROM incident_assignees WHERE user_id = ?)))",
					filter.DepartmentID, filter.UserID, filter.UserID, filter.UserID)
			} else {
				query = query.Where("incidents.department_id IN ?", filter.DepartmentID)
			}
		}

		if len(filter.AssigneeID) != 0 {
			if len(filter.AssigneeID) == 1 {
				// Check both primary assignee and multiple assignees
				query = query.Where("incidents.assignee_id = ? OR incidents.id IN (SELECT incident_id FROM incident_assignees WHERE user_id = ?)", filter.AssigneeID[0], filter.AssigneeID[0])
			} else {
				// Multiple assignees
				query = query.Where("incidents.assignee_id IN ?", filter.AssigneeID)
			}
		}

		// Filter by state visibility based on user roles
		if filter.UserRoleIDs != nil && len(filter.UserRoleIDs) > 0 {
			query = query.Joins("JOIN workflow_states ON workflow_states.id = incidents.current_state_id").
				Where(`
					NOT EXISTS (SELECT 1 FROM state_viewable_roles WHERE workflow_state_id = workflow_states.id)
					OR EXISTS (SELECT 1 FROM state_viewable_roles
					           WHERE workflow_state_id = workflow_states.id AND role_id IN ?)
					OR incidents.reporter_id = ? OR incidents.assignee_id = ?
					OR incidents.id IN (SELECT incident_id FROM incident_assignees WHERE user_id = ?)
				`, filter.UserRoleIDs, filter.UserID, filter.UserID, filter.UserID)
		}
	}

	var counts []priorityCount
	if err := query.Group("lookup_values.id, lookup_values.name, lookup_values.sort_order").
		Order("lookup_values.sort_order ASC").
		Scan(&counts).Error; err != nil {
		return nil, err
	}

	// Build result map with priority names as keys
	result := make(map[string]int64)
	for _, pc := range counts {
		result[pc.PriorityName] = pc.Count
	}

	return result, nil
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

// bulkEscalationEligibleStateCode returns the workflow state code whose resolved ID incidents must currently sit in to be eligible for bulk escalation. Configurable via BULK_ESCALATION_ELIGIBLE_STATE_CODE (default "under_resolution").
func BulkEscalationEligibleStateCode() string {
	if code := os.Getenv("BULK_ESCALATION_ELIGIBLE_STATE_CODE"); code != "" {
		return code
	}
	return "under_resolution"
}

// GetBreachedByFilter returns SLA-breached incidents eligible for bulk escalation. Only incidents whose current_state_id matches the resolved ID of the eligible state (see bulkEscalationEligibleStateCode, default "Under Resolution")
func (r *incidentRepository) GetBreachedByFilter(ctx context.Context, locationID uuid.UUID, classificationID uuid.UUID) ([]models.Incident, error) {

	var incidents []models.Incident

	query := r.db.WithContext(ctx).
		Where("sla_breached = ?", true).
		Where("record_type = ?", "incident").
		Where("current_state_id IN (SELECT id FROM workflow_states WHERE code = ?)", BulkEscalationEligibleStateCode())

	// Only filter by location if a non-zero UUID is provided
	if locationID != (uuid.UUID{}) {
		query = query.Where("location_id = ?", locationID)
	}

	// Only filter by classification if a non-zero UUID is provided
	if classificationID != (uuid.UUID{}) {
		query = query.Where("classification_id = ?", classificationID)
	}

	err := query.Preload("Location").Preload("Classification").Preload("CurrentState").Preload("Assignee").Preload("Department").Find(&incidents).Error
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

	const maxLimit = 100
	page = max(page, 1)
	if limit < 1 || limit > maxLimit {
		limit = 20
	}
	offset := (page - 1) * limit

	baseQuery := r.db.WithContext(ctx).
		Model(&models.Incident{}).
		Where("assignee_id = ? OR id IN (SELECT incident_id FROM incident_assignees WHERE user_id = ?)", userID, userID)

	if recordType != "" {
		baseQuery = baseQuery.Where("record_type = ?", recordType)
	}

	// Clone for count (critical fix)
	countQuery := baseQuery.Session(&gorm.Session{})
	if err := countQuery.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	// Main query with preloads
	if err := baseQuery.
		Preload("Classification").
		Preload("TransitionHistory").
		Preload("TransitionHistory.PerformedBy").
		Preload("Workflow").
		Preload("CurrentState").
		Preload("Assignee").
		Preload("Department").
		Preload("Location").
		Preload("LookupValues.Category").
		Preload("TransitionHistory.Transition").
		Order("created_at DESC").
		Offset(offset).
		Limit(limit).
		Find(&incidents).Error; err != nil {
		return nil, 0, err
	}

	return incidents, total, nil
}

func (r *incidentRepository) GetAssignedToUser1(ctx context.Context, userID uuid.UUID, recordType string, page, limit int) ([]models.Incident, int64, error) {
	var incidents []models.Incident
	var total int64

	page = max(page, 1)
	if limit < 1 || limit > 100 {
		limit = 20
	}
	offset := (page - 1) * limit

	// Check both primary assignee (assignee_id) AND multiple assignees (incident_assignees table)
	baseQuery := r.db.WithContext(ctx).Model(&models.Incident{}).
		Where("assignee_id = ? OR id IN (SELECT incident_id FROM incident_assignees WHERE user_id = ?)", userID, userID)

	// Filter by record_type if provided
	if recordType != "" {
		baseQuery = baseQuery.Where("record_type = ?", recordType)
	}

	if err := baseQuery.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := baseQuery.
		Preload("Classification").
		Preload("TransitionHistory").
		Preload("TransitionHistory.PerformedBy").
		Preload("Workflow").
		Preload("CurrentState").
		Preload("Assignee").
		Preload("Department").
		Preload("Location").
		Preload("LookupValues.Category").
		Preload("TransitionHistory.Transition").
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

	page = max(page, 1)
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
		Preload("Classification").
		Preload("TransitionHistory").
		Preload("TransitionHistory.PerformedBy").
		Preload("CurrentState").
		Preload("Workflow").
		Preload("Assignee").
		Preload("Department").
		Preload("Location").
		Preload("LookupValues.Category").
		Preload("TransitionHistory.Transition").
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
	page = max(page, 1)
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
		Preload("PerformedBy.Departments").
		Preload("TransitionHistory").
		Preload("TransitionHistory.Transition").
		Preload("TransitionHistory.FromState").
		Preload("TransitionHistory.ToState").
		Order("revision_number DESC").
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

func (r *incidentRepository) CreateNotification(ctx context.Context, notification *models.NotificationLog) error {
	return r.db.WithContext(ctx).Create(notification).Error
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

func (r *incidentRepository) ListAllFeedback(ctx context.Context) ([]models.IncidentFeedback, error) {
	var feedback []models.IncidentFeedback
	err := r.db.WithContext(ctx).
		Preload("CreatedBy").
		Preload("Incident").
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

// HasWhatsAppFeedback returns true if a WhatsApp chatbot feedback row already exists
// for this incident (transition_history_id IS NULL = chatbot submission, not a transition).
func (r *incidentRepository) HasWhatsAppFeedback(ctx context.Context, incidentID uuid.UUID) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&models.IncidentFeedback{}).
		Where("incident_id = ? AND transition_history_id IS NULL", incidentID).
		Count(&count).Error
	return count > 0, err
}

// Complaint-specific

func (r *incidentRepository) IncrementEvaluationCount(ctx context.Context, id uuid.UUID) error {
	return r.db.WithContext(ctx).
		Model(&models.Incident{}).
		Where("id = ?", id).
		Where("record_type = 'complaint'").
		Update("evaluation_count", gorm.Expr("evaluation_count + 1")).Error
}

// GetComplaintSourceValidation reads, in a single query, everything CreateComplaint needs to
// validate a candidate source incident: its type, current state code, age, reporter phone and
// the classification/location to inherit, plus two derived booleans.
//
// reporterPhone is the phone on the incoming request, and it drives both booleans: the complaint
// is only allowed when it matches the source incident's own reporter phone, and it is the key for
// detecting a complaint already filed against the same source incident. Keying both on the same
// value is deliberate — checking the match against the logged-in user's phone instead would let a
// caller vary reporter_phone freely and file unlimited complaints on one incident.
//
// Only an unclosed prior complaint blocks: once the earlier one reaches the 'closed' state the
// reporter may complain again about the same incident. A complaint whose state does not resolve
// counts as open, so a broken state reference fails safe by blocking rather than letting a
// duplicate through.
//
// Phone comparison matches on every shape the number may be stored in (see
// utils.MobileMatchVariants): the "+" and any known country code prefix are stripped from
// both sides before comparing, along with the trunk zero, so "+966501234567", "966501234567"
// and "0501234567" are all treated as the same number regardless of which side supplied which
// format. Variants of a real number are never blank, so a blank stored phone can never match.
//
// phone_matches also accepts the mobile number of the user who created the incident, joined live
// from users.phone via incidents.created_by_id: some channels (e.g. a citizen filing via the
// mobile app) record the creator as the reporter without necessarily duplicating that number into
// reporter_phone, so the creator's mobile is an equally valid proof that this caller is the same
// person who filed the source incident. Joining live rather than reading a snapshot column means
// this stays correct even if the user's phone number changes after the incident was created. A
// blank/NULL users.phone never matches for the same reason a blank reporter_phone never matches -
// it simply isn't a value produced by MobileMatchVariants.
func (r *incidentRepository) GetComplaintSourceValidation(ctx context.Context, sourceID uuid.UUID, reporterPhone string) (*models.ComplaintSourceValidation, error) {
	variants := utils.MobileMatchVariants(reporterPhone)

	const query = `
		SELECT i.record_type,
		       ws.code AS state_code,
		       i.created_at,
		       COALESCE(i.reporter_phone, '') AS reporter_phone,
		       i.classification_id,
		       i.location_id,
		       (i.reporter_phone IN (?) OR u.phone IN (?)) AS phone_matches,
		       EXISTS (
		           SELECT 1
		           FROM incidents c
		           LEFT JOIN workflow_states cws ON cws.id = c.current_state_id AND cws.deleted_at IS NULL
		           WHERE c.source_incident_id = i.id
		             AND c.record_type = 'complaint'
		             AND c.deleted_at IS NULL
		             AND COALESCE(cws.code, '') <> 'closed'
		             AND c.reporter_phone IN (?)
		       ) AS open_complaint_exists
		FROM incidents i
		LEFT JOIN workflow_states ws ON ws.id = i.current_state_id AND ws.deleted_at IS NULL
		LEFT JOIN users u ON u.id = i.reporter_id
		WHERE i.id = ?
		  AND i.deleted_at IS NULL`

	var result models.ComplaintSourceValidation
	err := r.db.WithContext(ctx).Raw(query, variants, variants, variants, sourceID).Scan(&result).Error
	if err != nil {
		return nil, err
	}
	// Scan leaves the struct zeroed when no row matched; record_type is NOT NULL in the
	// schema, so an empty value means the incident does not exist.
	if result.RecordType == "" {
		return nil, gorm.ErrRecordNotFound
	}
	return &result, nil
}

func (r *incidentRepository) FindUnassignedByStateCode(ctx context.Context, stateCode string) ([]models.Incident, error) {
	var incidents []models.Incident
	err := r.db.WithContext(ctx).
		Joins("JOIN workflow_states ws ON ws.id = incidents.current_state_id").
		Where("ws.code = ?", stateCode).
		Where("incidents.id NOT IN (SELECT incident_id FROM incident_assignees) AND incidents.record_type = ?", "incident").
		Preload("CurrentState").
		Preload("CurrentState.AssignmentRoles").
		Preload("CurrentState.AssignUser").
		Preload("Classification").
		Preload("Location").
		Preload("Department").
		Find(&incidents).Error
	return incidents, err
}

// Concurrency Control

// UpdateFieldsWithVersion performs optimistic lock field update
func (r *incidentRepository) UpdateFieldsWithVersion(ctx context.Context, id uuid.UUID, updates map[string]interface{}, expectedVersion int) error {
	updates["version"] = expectedVersion + 1
	updates["updated_at"] = time.Now()

	result := r.db.WithContext(ctx).
		Model(&models.Incident{}).
		Where("id = ? AND version = ?", id, expectedVersion).
		Updates(updates)

	if result.Error != nil {
		return result.Error
	}

	if result.RowsAffected == 0 {
		return ErrVersionMismatch
	}

	return nil
}

// WithTx returns repository instance using the provided transaction
func (r *incidentRepository) WithTx(tx *gorm.DB) IncidentRepository {
	return &incidentRepository{db: tx}
}

// LockForUpdate acquires a pessimistic lock (SELECT FOR UPDATE)
func (r *incidentRepository) LockForUpdate(ctx context.Context, tx *gorm.DB, id uuid.UUID) (*models.Incident, error) {
	var incident models.Incident

	// Set lock timeout to 10 seconds to prevent indefinite waiting
	if err := tx.Exec("SET LOCAL lock_timeout = '10s'").Error; err != nil {
		return nil, err
	}

	err := tx.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Preload("Classification").
		Preload("Workflow").
		Preload("CurrentState").
		Where("id = ?", id).
		First(&incident).Error

	if err != nil {
		return nil, err
	}

	return &incident, nil
}

func (r *incidentRepository) FindUserOpenIncidentsForDuplicateCheck(ctx context.Context, reporterID uuid.UUID) ([]models.Incident, error) {

	var incidents []models.Incident

	err := r.db.WithContext(ctx).
		Where("reporter_id = ?", reporterID).
		Where("closed_at IS NULL").
		Where("latitude IS NOT NULL").
		Where("longitude IS NOT NULL").
		Find(&incidents).Error

	return incidents, err
}

func (r *incidentRepository) FindOpenIncidentsForDuplicateCheckByCaller(ctx context.Context, reporterPhone string) ([]models.Incident, error) {
	if reporterPhone == "" {
		return []models.Incident{}, nil
	}

	query := r.db.WithContext(ctx).
		Where("closed_at IS NULL").
		Where("latitude IS NOT NULL").
		Where("longitude IS NOT NULL")

	query = query.Where("reporter_phone IN ?", reporterPhoneVariants(reporterPhone))

	var incidents []models.Incident
	err := query.Find(&incidents).Error
	return incidents, err
}

// GetIncidentsExceedingStateSLA returns incidents where the time spent in the current state

func (r *incidentRepository) GetIncidentsExceedingStateSLA(ctx context.Context) ([]models.Incident, error) {
	var incidents []models.Incident

	err := r.db.WithContext(ctx).
		Select("incidents.*").
		Joins("JOIN workflow_states ws ON incidents.current_state_id = ws.id").
		Where("incidents.record_type = 'incident'").
		Where("ws.sla_hours IS NOT NULL AND ws.sla_hours > 0").
		Where("ws.state_type != 'terminal'").
		Where("ws.deleted_at IS NULL").
		Where(`
			EXTRACT(EPOCH FROM (NOW() - COALESCE(
				(SELECT MAX(ith.transitioned_at)
				 FROM incident_transition_histories ith
				 WHERE ith.incident_id = incidents.id
				   AND ith.to_state_id = incidents.current_state_id),
				incidents.created_at
			))) >
			CASE COALESCE(ws.sla_unit, 'hours')
				WHEN 'minutes' THEN ws.sla_hours * 60
				WHEN 'days'    THEN ws.sla_hours * 86400
				WHEN 'months'  THEN ws.sla_hours * 2592000
				ELSE ws.sla_hours * 3600
			END
		`).
		Preload("CurrentState").
		Preload("Workflow").
		Preload("Location").
		Preload("Classification").
		Preload("Assignee").
		Preload("Reporter").
		Preload("Department").
		Preload("LookupValues.Category").
		Preload("TransitionHistory", func(db *gorm.DB) *gorm.DB {
			return db.Order("transitioned_at DESC").Limit(1)
		}).
		Order("incidents.created_at DESC").
		Find(&incidents).Error

	return incidents, err
}

// GetNewlyBreachedIncidents returns incidents with sla_breached = true that have no

func (r *incidentRepository) GetNewlyBreachedIncidents(ctx context.Context) ([]models.Incident, error) {
	var incidents []models.Incident
	err := r.db.WithContext(ctx).
		Where("sla_breached = ?", true).
		Where(`current_state_id NOT IN (
			SELECT id FROM workflow_states WHERE state_type = 'terminal'
		)`).
		Where(`NOT EXISTS (
			SELECT 1 FROM escalation_slas es
			WHERE es.incident_id = incidents.id
			  AND es.state_id IS NULL
		)`).
		Preload("Assignee").
		Preload("Reporter").
		Preload("CurrentState").
		Preload("Location").
		Preload("Classification").
		Preload("Workflow").
		Preload("LookupValues.Category").
		Find(&incidents).Error
	return incidents, err
}

func (r *incidentRepository) GetReportTransitions(ctx context.Context, incidentID uuid.UUID) ([]models.IncidentReportTransition, error) {
	var results []models.IncidentReportTransition
	sql := `
SELECT
    ith.id, ith.transition_id, ith.performed_by_id,
    ith.comment, ith.old_values, ith.new_values, ith.transitioned_at,
    COALESCE(u.first_name, '')      AS performed_by_first_name,
    COALESCE(u.last_name, '')       AS performed_by_last_name,
    COALESCE(wt.name, '')           AS transition_name,
    COALESCE(wt.name_ar, '')        AS transition_name_ar,
    COALESCE(wt.code, '')           AS transition_code,
    ith.from_state_id,
    COALESCE(fs.name, '')           AS from_state_name,
    COALESCE(fs.name_ar, '')        AS from_state_name_ar,
    COALESCE(fs.code, '')           AS from_state_code,
    COALESCE(fs.color, '')          AS from_state_color,
    COALESCE(fs.state_type, '')     AS from_state_type,
    ith.to_state_id,
    COALESCE(ts.name, '')           AS to_state_name,
    COALESCE(ts.name_ar, '')        AS to_state_name_ar,
    COALESCE(ts.code, '')           AS to_state_code,
    COALESCE(ts.color, '')          AS to_state_color,
    COALESCE(ts.state_type, '')     AS to_state_type,
    COALESCE(ifb.comment, '')       AS feedback_comment
FROM incident_transition_histories ith
LEFT JOIN workflow_transitions wt  ON wt.id  = ith.transition_id
LEFT JOIN workflow_states fs       ON fs.id  = ith.from_state_id
LEFT JOIN workflow_states ts       ON ts.id  = ith.to_state_id
LEFT JOIN users u                  ON u.id   = ith.performed_by_id
LEFT JOIN incident_feedbacks ifb   ON ifb.transition_history_id = ith.id
WHERE ith.incident_id = ?
ORDER BY ith.transitioned_at ASC`
	err := r.db.WithContext(ctx).Raw(sql, incidentID).Scan(&results).Error
	return results, err
}

func (r *incidentRepository) GetReportAttachments(ctx context.Context, incidentID uuid.UUID) ([]models.IncidentReportAttachment, error) {
	var results []models.IncidentReportAttachment
	sql := `
SELECT
    ia.id, ia.incident_id, ia.transition_history_id,
    ia.file_name, ia.file_size, ia.mime_type, ia.file_path,
    ia.uploaded_by_id, ia.created_at, ia.deleted_at,
    COALESCE(u.first_name, '')  AS uploaded_by_first_name,
    COALESCE(u.last_name, '')   AS uploaded_by_last_name,
    COALESCE((
        SELECT STRING_AGG(DISTINCT r.name, ', ' ORDER BY r.name)
        FROM user_roles ur
        JOIN roles r ON r.id = ur.role_id AND r.deleted_at IS NULL AND r.is_active = true
        WHERE ur.user_id = ia.uploaded_by_id
    ), '')                       AS uploaded_by_role,
    wt.name                     AS transition_name,
    wt.name_ar                  AS transition_name_ar,
    fs.name                     AS from_state_name,
    fs.name_ar                  AS from_state_name_ar,
    ts.name                     AS to_state_name,
    ts.name_ar                  AS to_state_name_ar
FROM incident_attachments ia
LEFT JOIN users u                            ON u.id  = ia.uploaded_by_id
LEFT JOIN incident_transition_histories ith  ON ith.id = ia.transition_history_id
LEFT JOIN workflow_transitions wt            ON wt.id  = ith.transition_id
LEFT JOIN workflow_states fs                 ON fs.id  = ith.from_state_id
LEFT JOIN workflow_states ts                 ON ts.id  = ith.to_state_id
WHERE ia.incident_id = ?
ORDER BY ia.created_at ASC`
	err := r.db.WithContext(ctx).Raw(sql, incidentID).Scan(&results).Error
	return results, err
}

func (r *incidentRepository) GetReportRevisions(ctx context.Context, incidentID uuid.UUID) ([]models.IncidentReportRevision, error) {
	var results []models.IncidentReportRevision
	sql := `
SELECT
    ir.id, ir.incident_id, ir.transition_history_id,
    ir.comment_id, ir.attachment_id,
    ir.revision_number, ir.action_type, ir.action_description,
    ir.changes, ir.performed_by_id,
    COALESCE(ir.performed_by_roles, '') AS performed_by_roles,
    COALESCE(ir.performed_by_phone, '') AS performed_by_phone,
    ir.created_at,
    COALESCE(u.first_name, '') AS performed_by_first_name,
    COALESCE(u.last_name, '')  AS performed_by_last_name
FROM incident_revisions ir
LEFT JOIN users u ON u.id = ir.performed_by_id
WHERE ir.incident_id = ?
ORDER BY ir.created_at ASC`
	err := r.db.WithContext(ctx).Raw(sql, incidentID).Scan(&results).Error
	return results, err
}

func (r *incidentRepository) GetReportIncidentData(ctx context.Context, incidentID uuid.UUID) (*models.IncidentReportData, error) {
	var result models.IncidentReportData
	sql := `
SELECT
    i.id, i.incident_number, i.title, i.description,
    i.channel, i.source, i.record_type,
    i.created_at, i.updated_at,
    i.sla_breached, i.sla_deadline,
    i.due_date, i.resolved_at, i.closed_at,
    i.classification_id, i.location_id,
    COALESCE(i.created_by_mobile, '') AS created_by_mobile,
    COALESCE(i.created_by_name, '')   AS created_by_name,
    i.latitude, i.longitude,
    COALESCE(i.address, '')     AS address,
    COALESCE(i.city, '')        AS city,
    COALESCE(i.state, '')       AS state,
    COALESCE(i.country, '')     AS country,
    COALESCE(i.postal_code, '') AS postal_code,
    COALESCE(i.custom_fields, '') AS custom_fields,
    COALESCE(ws.name, '')       AS status_name,
    COALESCE(ws.name_ar, '')    AS status_name_ar,
    COALESCE(cl.name, '')       AS classification_name,
    COALESCE(cl.name_ar, '')    AS classification_name_ar,
    COALESCE(loc.name, '')      AS location_name,
    COALESCE(loc.name_ar, '')   AS location_name_ar,
    COALESCE(creator.username, '') AS creator_username,
    COALESCE(creator.first_name, '') AS creator_first_name,
    COALESCE(creator.last_name, '')  AS creator_last_name,
	CONCAT_WS(' ', creator.first_name, creator.last_name) AS creator_full_name,
    COALESCE(creator.email, '')  AS creator_email,
    COALESCE(creator.phone, '')  AS creator_phone,
    reporter_phone AS caller_phone,
    reporter_name AS caller_name,
    COALESCE(i.reporter_email, '') AS reporter_email,
    COALESCE(asn.first_name, '') AS assignee_first_name,
    COALESCE(asn.last_name, '')  AS assignee_last_name,
    COALESCE(dep.name, '')       AS department_name,
    COALESCE((
        SELECT string_agg(u2.first_name || ' ' || u2.last_name, ', ')
        FROM incident_assignees ia2
        JOIN users u2 ON u2.id = ia2.user_id
        WHERE ia2.incident_id = i.id
    ), '') AS assignees_name
FROM incidents i
LEFT JOIN workflow_states ws ON ws.id  = i.current_state_id
LEFT JOIN classifications cl  ON cl.id  = i.classification_id
LEFT JOIN locations loc        ON loc.id = i.location_id
LEFT JOIN users creator            ON creator.id = i.reporter_id
LEFT JOIN users asn            ON asn.id = i.assignee_id
LEFT JOIN departments dep      ON dep.id = i.department_id
WHERE i.id = ?`
	err := r.db.WithContext(ctx).Raw(sql, incidentID).Scan(&result).Error
	return &result, err
}

func (r *incidentRepository) GetReportLookupValues(ctx context.Context, incidentID uuid.UUID) ([]models.IncidentReportLookupValue, error) {
	var results []models.IncidentReportLookupValue
	sql := `
SELECT
    COALESCE(lc.id, '00000000-0000-0000-0000-000000000000'::uuid) AS category_id,
    COALESCE(lc.name, lv.code)  AS category_name,
    COALESCE(lc.name_ar, '')    AS category_name_ar,
    lv.code,
    COALESCE(lv.name, '')       AS name,
    COALESCE(lv.name_ar, '')    AS name_ar
FROM incident_lookup_values ilv
JOIN  lookup_values lv     ON lv.id  = ilv.lookup_value_id
LEFT JOIN lookup_categories lc ON lc.id = lv.category_id
WHERE ilv.incident_id = ?
ORDER BY lc.name, lv.sort_order`
	err := r.db.WithContext(ctx).Raw(sql, incidentID).Scan(&results).Error
	return results, err
}
