package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// eoReturningStamps reads the DB-stamped created_at and updated_at back into
// the inserted record, so every EO timestamp comes from the one server clock.
var eoReturningStamps = clause.Returning{Columns: []clause.Column{{Name: "created_at"}, {Name: "updated_at"}}}

// EORecordRepository is the only place SQL touches eo_records (spec §7.7): the
// engine, capabilities, designer and seeds address records through it, so a
// later deployment can sub-partition or promote columns without touching them.
//
// eo_records has the composite primary key (id, record_type_id) — Postgres
// requires the partition key in every unique constraint — so every lookup
// takes the record type beside the id, no GORM association is declared on
// models.EORecord, and every query spells its Where clause out: First(&rec,
// id), Preload and the association machinery assume a single-column key.
//
// A getter returns (nil, nil) when nothing matches. Absence is an ordinary
// answer here (a parent that was deleted, a reference nobody issued); callers
// decide whether it is an error, and none of them has to know gorm's sentinel.
type EORecordRepository interface {
	Create(ctx context.Context, rec *models.EORecord) error
	GetByID(ctx context.Context, id, recordTypeID uuid.UUID) (*models.EORecord, error)
	// GetAnyByID finds a record by id alone, across every partition. It
	// exists for the parent walk: parent_record_id carries no record type, so
	// the type cannot be supplied. Use GetByID wherever the type is known.
	GetAnyByID(ctx context.Context, id uuid.UUID) (*models.EORecord, error)
	GetByReference(ctx context.Context, reference string) (*models.EORecord, error)
	// LockForUpdate re-reads a record inside the CALLER'S TRANSACTION and
	// holds a row lock on it (SELECT … FOR UPDATE) until that transaction
	// ends. It is only meaningful on a repository bound by Transaction.
	//
	// A transaction alone does not serialise two transitions: under
	// Postgres' default READ COMMITTED both would read the same state, both
	// would pass their from-state check, and both would commit with the
	// second write winning silently. That is worse than a lost update here,
	// because the transition engine dispatches its actions after the commit,
	// so a doubly-executed transition spawns two child records from one
	// logical action in a register whose references are an audit trail.
	LockForUpdate(ctx context.Context, id, recordTypeID uuid.UUID) (*models.EORecord, error)
	Update(ctx context.Context, rec *models.EORecord) error
	// UpdateValues merges keys into the values jsonb; keys it does not
	// mention keep their stored value.
	UpdateValues(ctx context.Context, id, recordTypeID uuid.UUID, values map[string]any) error
	// Children returns the DIRECT children of parentID, optionally of one
	// record type. Direct, not descendant: services.BlockingChildren
	// identifies a child by membership in the slice it is given and ignores
	// links whose endpoints are both inside it, so a transitive set would
	// silently stop blocking.
	Children(ctx context.Context, parentID uuid.UUID, recordTypeID *uuid.UUID) ([]models.EORecord, error)
	// List is the register query: filtered, row-level-visible, sorted and
	// paged. It returns the page and the total across all pages. filter is
	// not modified. An unknown filter.SortBy is an error.
	List(ctx context.Context, filter *models.EORecordFilter) ([]models.EORecord, int64, error)
	CreateLink(ctx context.Context, link *models.EORecordLink) error
	LinksFrom(ctx context.Context, recordID uuid.UUID) ([]models.EORecordLink, error)
	CreateHistory(ctx context.Context, entry *models.EORecordHistory) error
	// History is one record's audit trail, NEWEST FIRST, paged. It returns
	// the page and the total across all pages — the same contract List has —
	// and it clamps page and limit the same way, so a client that can page
	// the register can page this without learning a second convention.
	//
	// It applies NO row-level visibility of its own and takes no actor: a
	// trail belongs to its record, so the caller must first have established
	// that it may see THAT RECORD (handlers.eoLoadRecord, which asks List
	// under the caller's scope). Serving this without that check would
	// disclose every state change, actor and timestamp of a record §7.11
	// conceals — and the record's existence with it.
	History(ctx context.Context, recordID, recordTypeID uuid.UUID, page, limit int) ([]models.EORecordHistory, int64, error)
	// EnteredStateIDs is every state the record has ever entered, from
	// eo_record_history. It is not the current state: is_locked_after asks
	// whether a record has ever been in a state, and EO workflows have return
	// and exception paths, so the two answers differ.
	EnteredStateIDs(ctx context.Context, recordID, recordTypeID uuid.UUID) (map[uuid.UUID]bool, error)
	// Transaction runs fn against a record repository, a record-type
	// repository AND a timer repository, all bound to one database
	// transaction, committing when fn returns nil and rolling back otherwise.
	// A record, its parent link, its first history entry and the timers it
	// schedules are one write: a timer must not outlive a rolled-back
	// transition.
	//
	// Inside fn, every read and write must go through these three handles. A read on a
	// pooled repository takes a second connection while the transaction holds
	// its own plus a row lock, and database/sql waits for a free one forever
	// when the pool is capped.
	Transaction(ctx context.Context, fn func(EORecordRepository, EORecordTypeRepository, EOTimerRepository) error) error
	// RecordsOfType returns every live record of one record type, oldest
	// first. It is the ENGINE's type-scoped read — services.decisions_base
	// checks a draft against the standing records of another type — and it
	// is deliberately not List: List is the register query, paged and scoped
	// to the requesting user's §7.11 row-level visibility, and an engine
	// caller has no user, so it would return almost nothing while looking
	// like it worked. Confidentiality decisions over what this returns
	// belong to the caller, visibly.
	RecordsOfType(ctx context.Context, recordTypeID uuid.UUID) ([]models.EORecord, error)
}

type eoRecordRepository struct {
	db *gorm.DB
}

func NewEORecordRepository(db *gorm.DB) EORecordRepository {
	return &eoRecordRepository{db: db}
}

// Create inserts the record. created_at and updated_at are left to the column
// defaults so they come from the database clock rather than the application
// host's; RETURNING hands the stored values back into rec.
func (r *eoRecordRepository) Create(ctx context.Context, rec *models.EORecord) error {
	if err := r.db.WithContext(ctx).
		Omit("CreatedAt", "UpdatedAt").
		Clauses(eoReturningStamps).
		Create(rec).Error; err != nil {
		return fmt.Errorf("create eo record %q: %w", rec.Reference, err)
	}
	return nil
}

func (r *eoRecordRepository) GetByID(ctx context.Context, id, recordTypeID uuid.UUID) (*models.EORecord, error) {
	return eoFirstRecord(r.db.WithContext(ctx).
		Where("eo_records.id = ? AND eo_records.record_type_id = ?", id, recordTypeID))
}

func (r *eoRecordRepository) GetAnyByID(ctx context.Context, id uuid.UUID) (*models.EORecord, error) {
	return eoFirstRecord(r.db.WithContext(ctx).Where("eo_records.id = ?", id))
}

func (r *eoRecordRepository) GetByReference(ctx context.Context, reference string) (*models.EORecord, error) {
	return eoFirstRecord(r.db.WithContext(ctx).Where("eo_records.reference = ?", reference))
}

// LockForUpdate takes the row lock. lock_timeout is SET LOCAL, so it lasts
// for the caller's transaction only and a contended row fails fast instead of
// pinning a request handler indefinitely — the same shape as the incident
// path's lock (incident_repository.go:2167), with the composite key
// (id, record_type_id) this table requires. A missing record is (nil, nil),
// as it is for every other getter here.
func (r *eoRecordRepository) LockForUpdate(ctx context.Context, id, recordTypeID uuid.UUID) (*models.EORecord, error) {
	if err := r.db.WithContext(ctx).Exec("SET LOCAL lock_timeout = '10s'").Error; err != nil {
		return nil, fmt.Errorf("lock eo record %s: set lock timeout: %w", id, err)
	}
	return eoFirstRecord(r.db.WithContext(ctx).
		Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("eo_records.id = ? AND eo_records.record_type_id = ?", id, recordTypeID))
}

// eoFirstRecord reads at most one record. It uses Find rather than First
// because "no such record" is an ordinary answer here: First would return
// gorm.ErrRecordNotFound and log it as an error on every miss.
func eoFirstRecord(q *gorm.DB) (*models.EORecord, error) {
	var out []models.EORecord
	if err := q.Limit(1).Find(&out).Error; err != nil {
		return nil, fmt.Errorf("read eo record: %w", err)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return &out[0], nil
}

// eoUpdatableColumns are the columns Update writes. They are listed instead of
// handing GORM the struct because a struct update skips every zero value, so
// clearing a due date or blanking a priority would silently keep the old one.
// id, record_type_id, created_by_id and created_at are immutable; updated_at
// is set from the database clock.
func eoUpdatableColumns(rec *models.EORecord) map[string]any {
	return map[string]any{
		"reference":        rec.Reference,
		"subject_ar":       rec.SubjectAr,
		"subject_en":       rec.SubjectEn,
		"parent_record_id": rec.ParentRecordID,
		"state_id":         rec.StateID,
		"priority":         rec.Priority,
		"sensitivity":      rec.Sensitivity,
		"source":           rec.Source,
		"owning_body_id":   rec.OwningBodyID,
		"assigned_user_id": rec.AssignedUserID,
		"due_date":         rec.DueDate,
		"closed_at":        rec.ClosedAt,
		"change_reason":    rec.ChangeReason,
		"delay_reason":     rec.DelayReason,
		"values":           rec.Values,
		"updated_at":       gorm.Expr("NOW()"),
	}
}

func (r *eoRecordRepository) Update(ctx context.Context, rec *models.EORecord) error {
	res := r.db.WithContext(ctx).
		Model(&models.EORecord{}).
		Where("eo_records.id = ? AND eo_records.record_type_id = ?", rec.ID, rec.RecordTypeID).
		Updates(eoUpdatableColumns(rec))
	if res.Error != nil {
		return fmt.Errorf("update eo record %s: %w", rec.ID, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("update eo record %s: no live record of type %s", rec.ID, rec.RecordTypeID)
	}
	return nil
}

// UpdateValues merges values into the jsonb bag with the || operator, so keys
// it does not mention survive. "values" is a Postgres reserved word: GORM
// quotes it when it builds a statement from the column tag, but never inside a
// raw fragment, so it is quoted by hand here.
func (r *eoRecordRepository) UpdateValues(ctx context.Context, id, recordTypeID uuid.UUID, values map[string]any) error {
	patch, err := json.Marshal(values)
	if err != nil {
		return fmt.Errorf("update eo record %s values: encode: %w", id, err)
	}
	res := r.db.WithContext(ctx).Exec(
		`UPDATE eo_records
		 SET "values" = eo_records."values" || ?::jsonb, updated_at = NOW()
		 WHERE eo_records.id = ? AND eo_records.record_type_id = ? AND eo_records.deleted_at IS NULL`,
		string(patch), id, recordTypeID)
	if res.Error != nil {
		return fmt.Errorf("update eo record %s values: %w", id, res.Error)
	}
	if res.RowsAffected == 0 {
		return fmt.Errorf("update eo record %s values: no live record of type %s", id, recordTypeID)
	}
	return nil
}

func (r *eoRecordRepository) Children(ctx context.Context, parentID uuid.UUID, recordTypeID *uuid.UUID) ([]models.EORecord, error) {
	q := r.db.WithContext(ctx).
		Model(&models.EORecord{}).
		Where("eo_records.parent_record_id = ?", parentID)
	if recordTypeID != nil {
		q = q.Where("eo_records.record_type_id = ?", *recordTypeID)
	}
	var out []models.EORecord
	if err := q.Order("eo_records.created_at ASC, eo_records.id ASC").Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list children of eo record %s: %w", parentID, err)
	}
	return out, nil
}

func (r *eoRecordRepository) RecordsOfType(ctx context.Context, recordTypeID uuid.UUID) ([]models.EORecord, error) {
	var out []models.EORecord
	if err := r.db.WithContext(ctx).
		Model(&models.EORecord{}).
		Where("eo_records.record_type_id = ?", recordTypeID).
		Order("eo_records.created_at ASC, eo_records.id ASC").
		Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list eo records of type %s: %w", recordTypeID, err)
	}
	return out, nil
}

func (r *eoRecordRepository) CreateLink(ctx context.Context, link *models.EORecordLink) error {
	if err := r.db.WithContext(ctx).Create(link).Error; err != nil {
		return fmt.Errorf("create eo record link %q: %w", link.LinkTypeKey, err)
	}
	return nil
}

func (r *eoRecordRepository) LinksFrom(ctx context.Context, recordID uuid.UUID) ([]models.EORecordLink, error) {
	var out []models.EORecordLink
	if err := r.db.WithContext(ctx).
		Model(&models.EORecordLink{}).
		Where("eo_record_links.from_record_id = ?", recordID).
		Order("eo_record_links.created_at ASC, eo_record_links.id ASC").
		Find(&out).Error; err != nil {
		return nil, fmt.Errorf("list links from eo record %s: %w", recordID, err)
	}
	return out, nil
}

func (r *eoRecordRepository) CreateHistory(ctx context.Context, entry *models.EORecordHistory) error {
	if err := r.db.WithContext(ctx).Create(entry).Error; err != nil {
		return fmt.Errorf("create eo record history for %s: %w", entry.RecordID, err)
	}
	return nil
}

// eoHistoryOrder is the audit trail's TOTAL order.
//
// created_at DESC alone is not one. A create writes more than one row in a
// single transaction (eo_record_service.go:938/953/1163) and a transition
// writes another (eo_transition_service.go:446), so rows share a timestamp
// routinely — and Postgres may return tied rows in any order, and need not
// return the same order twice. Under LIMIT/OFFSET paging that is not a
// cosmetic wobble: a tied row can land on two pages while another lands on
// none, so a client paging an audit trail silently loses entries. The id
// breaks the tie, exactly as eoOrderClause does for the register.
const eoHistoryOrder = "eo_record_history.created_at DESC, eo_record_history.id ASC"

func (r *eoRecordRepository) History(ctx context.Context, recordID, recordTypeID uuid.UUID, page, limit int) ([]models.EORecordHistory, int64, error) {
	page, limit = eoClampPaging(page, limit)
	// Both the count and the page are built from the same predicate, so they
	// cannot drift. The composite key is spelled out because the record id
	// alone is not unique across partitions.
	scope := func() *gorm.DB {
		return r.db.WithContext(ctx).
			Model(&models.EORecordHistory{}).
			Where("eo_record_history.record_id = ? AND eo_record_history.record_type_id = ?", recordID, recordTypeID)
	}

	var total int64
	if err := scope().Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count history of eo record %s: %w", recordID, err)
	}

	var out []models.EORecordHistory
	if err := scope().
		Order(eoHistoryOrder).
		Offset((page - 1) * limit).
		Limit(limit).
		Find(&out).Error; err != nil {
		return nil, 0, fmt.Errorf("read history of eo record %s: %w", recordID, err)
	}
	return out, total, nil
}

func (r *eoRecordRepository) EnteredStateIDs(ctx context.Context, recordID, recordTypeID uuid.UUID) (map[uuid.UUID]bool, error) {
	var ids []uuid.UUID
	if err := r.db.WithContext(ctx).
		Model(&models.EORecordHistory{}).
		Where("eo_record_history.record_id = ? AND eo_record_history.record_type_id = ? AND eo_record_history.to_state_id IS NOT NULL",
			recordID, recordTypeID).
		Distinct().
		Pluck("eo_record_history.to_state_id", &ids).Error; err != nil {
		return nil, fmt.Errorf("read entered states of eo record %s: %w", recordID, err)
	}
	out := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

func (r *eoRecordRepository) Transaction(ctx context.Context, fn func(EORecordRepository, EORecordTypeRepository, EOTimerRepository) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(&eoRecordRepository{db: tx}, &eoRecordTypeRepository{db: tx}, &eoTimerRepository{db: tx})
	})
}

// eoSortColumns maps the sort_by keys a client may send to the qualified
// column each orders by. Only the mapped value reaches SQL, never the key.
var eoSortColumns = map[string]string{
	"created_at": "eo_records.created_at",
	"updated_at": "eo_records.updated_at",
	"due_date":   "eo_records.due_date",
	"reference":  "eo_records.reference",
}

const (
	eoDefaultLimit = 20
	eoMaxLimit     = 100
)

// eoClampPaging resolves page and limit to the EO register's own defaults and
// maximum. It is the one copy: List and History both page, and a second clamp
// written beside one of them is a second convention a client would have to
// learn. A limit of zero never leaves this function — it would be an
// unbounded read, and a division by zero in the paginated envelope.
func eoClampPaging(page, limit int) (int, int) {
	if page < 1 {
		page = 1
	}
	if limit < 1 || limit > eoMaxLimit {
		limit = eoDefaultLimit
	}
	return page, limit
}

// eoVisibleToKey is the values key a record carries when it is restricted to
// one body ("the referred body only", §7.11).
const eoVisibleToKey = "visible_to"

func (r *eoRecordRepository) List(ctx context.Context, filter *models.EORecordFilter) ([]models.EORecord, int64, error) {
	if filter == nil {
		filter = &models.EORecordFilter{}
	}
	order, err := eoOrderClause(filter)
	if err != nil {
		return nil, 0, err
	}
	// Page and limit are resolved into locals; the caller's struct is left alone.
	page, limit := eoClampPaging(filter.Page, filter.Limit)

	var total int64
	countQ, err := applyEORecordFilters(r.db.WithContext(ctx).Model(&models.EORecord{}), filter)
	if err != nil {
		return nil, 0, err
	}
	if err := countQ.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("count eo records: %w", err)
	}

	listQ, err := applyEORecordFilters(r.db.WithContext(ctx).Model(&models.EORecord{}), filter)
	if err != nil {
		return nil, 0, err
	}
	var out []models.EORecord
	if err := listQ.Order(order).Offset((page - 1) * limit).Limit(limit).Find(&out).Error; err != nil {
		return nil, 0, fmt.Errorf("list eo records: %w", err)
	}
	return out, total, nil
}

// ErrEOUnsupportedSort marks a sort key or direction the allow-list does not
// contain. It is a SENTINEL because it is the one List failure that is the
// caller's mistake rather than the server's: it is raised before any query
// runs, and the HTTP layer answers 400 for it instead of logging an internal
// error for somebody's typo. Matching on the message text would be the only
// alternative, and the allow-list itself must not be duplicated outside this
// file — a second copy is a second thing to keep in step with eoSortColumns.
var ErrEOUnsupportedSort = errors.New("unsupported sort")

// eoOrderClause builds ORDER BY from the allow-list. eo_records.id breaks ties
// so pages never repeat or skip a row.
func eoOrderClause(filter *models.EORecordFilter) (string, error) {
	key := filter.SortBy
	if key == "" {
		key = "created_at"
	}
	col, ok := eoSortColumns[key]
	if !ok {
		return "", fmt.Errorf("list eo records: %w sort_by %q", ErrEOUnsupportedSort, filter.SortBy)
	}
	dir := "DESC"
	switch strings.ToLower(filter.SortDir) {
	case "", "desc":
	case "asc":
		dir = "ASC"
	default:
		return "", fmt.Errorf("list eo records: %w sort_dir %q", ErrEOUnsupportedSort, filter.SortDir)
	}
	return col + " " + dir + ", eo_records.id ASC", nil
}

// EOSortKeys is the allow-listed sort_by values, for an API that wants to tell
// a caller what it may ask for. It is derived from eoSortColumns, never
// written out a second time.
func EOSortKeys() []string {
	out := make([]string, 0, len(eoSortColumns))
	for k := range eoSortColumns {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// applyEORecordFilters is the one place a register's WHERE clause is built, so
// List and any later count or summary query cannot drift. Row-level visibility
// is applied here, unconditionally: no filter field can switch it off.
func applyEORecordFilters(q *gorm.DB, f *models.EORecordFilter) (*gorm.DB, error) {
	q = applyEORecordVisibility(q, f)
	q = applyEORecordTypePermissions(q, f)

	if f.RecordID != nil {
		q = q.Where("eo_records.id = ?", *f.RecordID)
	}
	if len(f.RecordTypeID) > 0 {
		q = q.Where("eo_records.record_type_id IN ?", f.RecordTypeID)
	}
	if len(f.StateID) > 0 {
		q = q.Where("eo_records.state_id IN ?", f.StateID)
	}
	if len(f.OwningBodyID) > 0 {
		q = q.Where("eo_records.owning_body_id IN ?", f.OwningBodyID)
	}
	if len(f.AssignedUser) > 0 {
		q = q.Where("eo_records.assigned_user_id IN ?", f.AssignedUser)
	}
	if f.ParentID != nil {
		q = q.Where("eo_records.parent_record_id = ?", *f.ParentID)
	}
	if len(f.Priority) > 0 {
		q = q.Where("eo_records.priority IN ?", f.Priority)
	}
	if len(f.Sensitivity) > 0 {
		q = q.Where("eo_records.sensitivity IN ?", f.Sensitivity)
	}
	if len(f.Source) > 0 {
		q = q.Where("eo_records.source IN ?", f.Source)
	}
	if f.Search != "" {
		like := "%" + eoLikeEscaper.Replace(f.Search) + "%"
		q = q.Where("(eo_records.reference ILIKE ? OR eo_records.subject_ar ILIKE ? OR eo_records.subject_en ILIKE ?)", like, like, like)
	}
	if f.CreatedFrom != nil {
		q = q.Where("eo_records.created_at >= ?", *f.CreatedFrom)
	}
	if f.CreatedTo != nil {
		q = q.Where("eo_records.created_at <= ?", *f.CreatedTo)
	}
	if f.DueFrom != nil {
		q = q.Where("eo_records.due_date >= ?", *f.DueFrom)
	}
	if f.DueTo != nil {
		q = q.Where("eo_records.due_date <= ?", *f.DueTo)
	}
	if len(f.Values) > 0 {
		want, err := json.Marshal(f.Values)
		if err != nil {
			return nil, fmt.Errorf("list eo records: encode values filter: %w", err)
		}
		// "values" is a reserved word that GORM does not quote in a raw
		// fragment, so it is quoted by hand.
		q = q.Where(`eo_records."values" @> ?::jsonb`, string(want))
	}
	return q, nil
}

// applyEORecordVisibility enforces §7.11 row-level visibility. A record with a
// visible_to value is shown only to an admin, to its creator (the office that
// issued a referral must see it to consolidate), or to a caller whose bodies
// include that body; a record with none is unrestricted. A caller with no
// bodies and no UserID therefore sees unrestricted records only. The
// sensitivity label is deliberately not consulted: there is no clearance policy.
//
// A caller carrying ViewAllBodies (the eo:records:view_all_bodies permission,
// the oversight role's grant) sees every record in the registers it may
// otherwise see: this function lifts only the BODY restriction. It is not a
// read-only lift: every caller of this function, including the paths that
// locate a record for update, transition or delete, is lifted too.
// applyEORecordTypePermissions is a separate clause, ANDed by
// applyEORecordFilters, and still limits the caller to the types it holds a
// view code on — so the flag composes with the per-type gate and widens no
// register.
func applyEORecordVisibility(q *gorm.DB, f *models.EORecordFilter) *gorm.DB {
	if f.IsAdmin {
		return q
	}
	if f.ViewAllBodies {
		return q
	}
	const restrictedTo = `eo_records."values"->>'` + eoVisibleToKey + `'`
	clauses := []string{restrictedTo + " IS NULL"}
	args := []any{}
	if f.UserID != uuid.Nil {
		clauses = append(clauses, "eo_records.created_by_id = ?")
		args = append(args, f.UserID)
	}
	if len(f.VisibleToDepartmentIDs) > 0 {
		bodies := make([]string, len(f.VisibleToDepartmentIDs))
		for i, id := range f.VisibleToDepartmentIDs {
			bodies[i] = id.String()
		}
		clauses = append(clauses, restrictedTo+" IN ?")
		args = append(args, bodies)
	}
	return q.Where("("+strings.Join(clauses, " OR ")+")", args...)
}

// applyEORecordTypePermissions enforces §7.11's PER-TYPE permissions over the
// register. It is the second half of the control middleware.EOTypePermission
// starts: that gate answers 403 for one record reached by its id, and a route
// check cannot narrow a result set — without this clause a caller who does not
// hold eo:eo_decision_record:view is refused one Decision Record and shown
// every Decision Record by GET /eo-records.
//
// IT LIVES HERE, beside applyEORecordVisibility and for that function's own
// reason: a new endpoint that queries the register cannot bypass it. It
// composes with visibility rather than replacing it — the two are ANDed, so a
// caller who passes the type gate can still be refused by visible_to.
//
// IT FAILS CLOSED. The keys come from the caller's own permission codes; a
// caller holding none matches no type and sees an EMPTY register. A filter
// that treated "no codes supplied" as "no restriction" would be switched off
// by every caller who forgot to supply them, which is exactly the inert guard
// this mechanism has been for two plans.
//
// THE CODE FORMAT NEVER REACHES SQL. The keys are derived in Go by
// models.EOTypeKeysFromPermissionCodes, the inverse of the function the seed
// writes the codes with; a literal 'eo:' || key || ':view' in this WHERE
// clause would be a second spelling that nothing could keep in step with the
// seed — the shape of the Plan 1 Critical where visibility was seeded under
// `visibility` and read as `visible_to`.
//
// The subquery names the TABLE rather than the model, deliberately: GORM would
// add deleted_at IS NULL for the model, and soft-deleting a record type would
// then drop its records out of the restricted set and unhide them for
// everybody. Deleting a type is a configuration change, not a declassification.
func applyEORecordTypePermissions(q *gorm.DB, f *models.EORecordFilter) *gorm.DB {
	if f.IsAdmin {
		return q
	}
	keys := models.EOTypeKeysFromPermissionCodes(f.PermissionCodes, models.EOTypePermissionView)
	allowed := q.Session(&gorm.Session{NewDB: true}).
		Table("eo_record_types").
		Select("id").
		Where("key IN ?", keys)
	return q.Where("eo_records.record_type_id IN (?)", allowed)
}

// eoLikeEscaper makes a search term match literally inside ILIKE.
var eoLikeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
