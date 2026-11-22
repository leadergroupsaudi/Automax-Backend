package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/internal/services/eoformula"
	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// ErrEORecordNotFound is returned for a record id (or a parent id) that names
// no live record.
var ErrEORecordNotFound = errors.New("eo record not found")

// EOValidationError carries field key -> message, which is the shape the API
// returns as {"success":false,"errors":{...}}. It is the only error a caller
// should show field by field; everything else is a configuration or storage
// failure and belongs in a 500.
type EOValidationError struct {
	Fields map[string]string
}

func (e *EOValidationError) Error() string {
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+": "+e.Fields[k])
	}
	return "eo record is not valid: " + strings.Join(parts, "; ")
}

// eoValidationError returns nil for an empty map, so callers can write
// `if err := eoValidationError(errs); err != nil`.
func eoValidationError(fields map[string]string) error {
	if len(fields) == 0 {
		return nil
	}
	return &EOValidationError{Fields: fields}
}

// ── values: columns and the jsonb bag are one field space ───────────────────
//
// The 17 fields every record type inherits are real eo_records columns; a
// type's own fields live in the values jsonb (spec §7.7). Everything above
// storage — validation, formulas, the API — addresses both by field KEY, so
// these two functions are the only place the split exists.

// eoRecordColumnWriters maps a field key to the column it writes. A key that
// is absent from here and from eoEngineOwnedKeys goes into the values jsonb.
var eoRecordColumnWriters = map[string]func(*models.EORecord, any) error{
	"subject_ar":       func(r *models.EORecord, v any) error { return eoSetString(&r.SubjectAr, v) },
	"subject_en":       func(r *models.EORecord, v any) error { return eoSetString(&r.SubjectEn, v) },
	"priority":         func(r *models.EORecord, v any) error { return eoSetString(&r.Priority, v) },
	"sensitivity":      func(r *models.EORecord, v any) error { return eoSetString(&r.Sensitivity, v) },
	"source":           func(r *models.EORecord, v any) error { return eoSetString(&r.Source, v) },
	"change_reason":    func(r *models.EORecord, v any) error { return eoSetString(&r.ChangeReason, v) },
	"delay_reason":     func(r *models.EORecord, v any) error { return eoSetString(&r.DelayReason, v) },
	"owning_body_id":   func(r *models.EORecord, v any) error { return eoSetUUID(&r.OwningBodyID, v) },
	"assigned_user_id": func(r *models.EORecord, v any) error { return eoSetUUID(&r.AssignedUserID, v) },
	"due_date":         func(r *models.EORecord, v any) error { return eoSetTime(&r.DueDate, v) },
}

// eoEngineOwnedKeys are column-backed keys the save pipeline writes itself: a
// reference comes from the numbering scheme, a state from a transition, a
// parent from the create request. The field definitions mark them auto or
// system so validation rejects a submission first; this map is the backstop
// for a type whose configuration forgot to.
var eoEngineOwnedKeys = map[string]bool{
	"reference":        true,
	"record_type_id":   true,
	"parent_record_id": true,
	"state_id":         true,
	"closed_at":        true,
	"created_by_id":    true,
	"created_at":       true,
	"updated_at":       true,
}

// eoFlattenStored returns a record's current values keyed by field key, across
// BOTH the real columns and the values jsonb. ValidateRecordValues takes this
// map: reading only rec.Values would make every inherited field look empty and
// so reject every partial update that does not resend the whole form.
//
// Columns are written last, so a stale jsonb key left behind by an earlier
// configuration cannot shadow the column that now owns it.
func eoFlattenStored(rec *models.EORecord) map[string]any {
	out := eoDecodeValues(rec.Values)
	out["reference"] = rec.Reference
	out["subject_ar"] = rec.SubjectAr
	out["subject_en"] = rec.SubjectEn
	out["parent_record_id"] = rec.ParentRecordID
	out["state_id"] = rec.StateID
	out["priority"] = rec.Priority
	out["sensitivity"] = rec.Sensitivity
	out["source"] = rec.Source
	out["owning_body_id"] = rec.OwningBodyID
	out["assigned_user_id"] = rec.AssignedUserID
	out["due_date"] = rec.DueDate
	out["closed_at"] = rec.ClosedAt
	out["created_by_id"] = rec.CreatedByID
	out["change_reason"] = rec.ChangeReason
	out["delay_reason"] = rec.DelayReason
	out["created_at"] = rec.CreatedAt
	out["updated_at"] = rec.UpdatedAt
	return out
}

// eoApplyValues writes submitted values onto rec: a column-backed key to its
// column, everything else into the values jsonb, which is merged so keys the
// payload does not mention keep their stored value. It returns field key ->
// message for values that do not fit their column; the caller merges those
// into the validation errors.
func eoApplyValues(rec *models.EORecord, values map[string]any) map[string]string {
	errs := map[string]string{}
	bag := eoDecodeValues(rec.Values)
	for k, v := range values {
		if eoEngineOwnedKeys[k] {
			errs[k] = "is set by the engine and cannot be written"
			continue
		}
		if write, ok := eoRecordColumnWriters[k]; ok {
			if err := write(rec, v); err != nil {
				errs[k] = err.Error()
			}
			continue
		}
		bag[k] = v
	}
	raw, err := json.Marshal(bag)
	if err != nil {
		errs["values"] = "could not be encoded"
		return errs
	}
	rec.Values = datatypes.JSON(raw)
	return errs
}

// eoUnknownFieldErrors rejects submitted keys that no effective field defines.
//
// Only the request path calls it. Values the engine writes itself — derived
// fields, and later the capabilities — always have a definition or belong to
// the engine's own columns, and they are written server-side, so nothing is
// gained by letting a client post a key no configuration knows. What it
// prevents is a client pre-seeding a key a capability will later read as its
// own (an approval stamp, a quorum flag) before any definition exists to
// guard it, and unbounded growth of the GIN-indexed bag (spec §7.7: values
// holds the variable tail of a record, not its bulk).
func eoUnknownFieldErrors(fields []models.EOFieldDef, values map[string]any) map[string]string {
	known := make(map[string]bool, len(fields))
	for _, f := range fields {
		known[f.Key] = true
	}
	errs := map[string]string{}
	for k := range values {
		if !known[k] {
			errs[k] = "is not a field of this record type"
		}
	}
	return errs
}

// eoMergeFieldErrors folds src into dst without overwriting a message already
// there, so the first explanation of a key wins.
func eoMergeFieldErrors(dst, src map[string]string) map[string]string {
	for k, v := range src {
		if _, ok := dst[k]; !ok {
			dst[k] = v
		}
	}
	return dst
}

// eoDiffValues returns the before and after of every key whose value changed,
// compared by JSON encoding so a submitted int and a stored float64 are equal.
func eoDiffValues(before, after map[string]any) (old, updated map[string]any) {
	old, updated = map[string]any{}, map[string]any{}
	for k, v := range after {
		if !eoValuesEqual(before[k], v) {
			old[k], updated[k] = before[k], v
		}
	}
	for k, v := range before {
		if _, ok := after[k]; !ok {
			old[k], updated[k] = v, nil
		}
	}
	return old, updated
}

// eoClassifyDeriveError splits what RecomputeDerived reports into fields that
// could not be computed because the data is not there yet (an unresolved
// reference — a roll-up over children a new record does not have) and fields
// whose configuration is broken (a cycle, an unknown recompute_on scope, a
// loader failure, a type error in the formula). Only the second kind is
// actionable, and logging them alike makes a misconfigured formula
// indistinguishable from a correct one.
func eoClassifyDeriveError(err error) (pending, broken []error) {
	if err == nil {
		return nil, nil
	}
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		for _, e := range joined.Unwrap() {
			p, b := eoClassifyDeriveError(e)
			pending = append(pending, p...)
			broken = append(broken, b...)
		}
		return pending, broken
	}
	if errors.Is(err, eoformula.ErrUnresolvedReference) {
		return []error{err}, nil
	}
	return nil, []error{err}
}

// eoDecodeValues decodes the jsonb bag without coercing anything. It is
// deliberately not eoValuesOf (eo_derive.go), which turns date-shaped strings
// into time.Time for the formula env: a coerced value would no longer compare
// equal to the string a client echoes back, and the locked-field check
// compares submitted against stored.
func eoDecodeValues(raw datatypes.JSON) map[string]any {
	out := map[string]any{}
	if len(raw) == 0 {
		return out
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	return out
}

func eoSetString(dst *string, v any) error {
	switch x := v.(type) {
	case nil:
		*dst = ""
	case string:
		*dst = x
	default:
		return errors.New("must be text")
	}
	return nil
}

func eoSetUUID(dst **uuid.UUID, v any) error {
	switch x := v.(type) {
	case nil:
		*dst = nil
	case string:
		if strings.TrimSpace(x) == "" {
			*dst = nil
			return nil
		}
		id, err := uuid.Parse(x)
		if err != nil {
			return errors.New("must be an identifier")
		}
		*dst = &id
	default:
		return errors.New("must be an identifier")
	}
	return nil
}

func eoSetTime(dst **time.Time, v any) error {
	switch x := v.(type) {
	case nil:
		*dst = nil
	case time.Time:
		// Derived fields evaluate to a time.Time: a formula such as
		// "visit_date - days(2)" writing into a date column.
		*dst = &x
	case string:
		if strings.TrimSpace(x) == "" {
			*dst = nil
			return nil
		}
		for _, layout := range []string{time.RFC3339, "2006-01-02"} {
			if t, err := time.Parse(layout, x); err == nil {
				*dst = &t
				return nil
			}
		}
		return errors.New("must be a date (YYYY-MM-DD or RFC 3339)")
	default:
		return errors.New("must be a date (YYYY-MM-DD or RFC 3339)")
	}
	return nil
}

// ── dependency loading ──────────────────────────────────────────────────────

// eoRecordDeps adapts the id-based record repository to the key-based
// DependencyLoader that RecomputeDerived takes: a formula names a record type
// by key ("children_eo_stage_sum_weeks"), the repository knows only ids, so
// the key is resolved to an id here and the call delegated.
type eoRecordDeps struct {
	records repository.EORecordRepository
	types   repository.EORecordTypeRepository
}

func (d *eoRecordDeps) Children(ctx context.Context, parentID uuid.UUID, recordTypeKey string) ([]models.EORecord, error) {
	t, err := d.types.GetByKey(ctx, recordTypeKey)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, fmt.Errorf("no record type with key %q", recordTypeKey)
	}
	return d.records.Children(ctx, parentID, &t.ID)
}

// Ancestor returns the NEAREST ancestor of the named type, walking
// parent_record_id upwards. parent_record_id carries no record type, so the
// walk loads each ancestor by id alone; WalkAncestors bounds the walk and
// refuses a cycle rather than looping inside a request.
func (d *eoRecordDeps) Ancestor(ctx context.Context, rec *models.EORecord, direction string) (*models.EORecord, error) {
	t, err := d.types.GetByKey(ctx, direction)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, fmt.Errorf("no record type with key %q", direction)
	}
	ancestors, err := WalkAncestors(ctx, rec, func(id uuid.UUID) (*models.EORecord, error) {
		return d.records.GetAnyByID(ctx, id)
	}, 0)
	if err != nil {
		return nil, err
	}
	for i := range ancestors {
		if ancestors[i].RecordTypeID == t.ID {
			return &ancestors[i], nil
		}
	}
	return nil, nil
}

// ── the save pipeline ───────────────────────────────────────────────────────

// EORecordService is the create/update path for records of any configured
// type. Create runs: resolve effective fields -> validate -> allocate
// reference -> resolve initial state -> apply link rules -> recompute derived
// -> persist. Update runs the same pipeline without the steps that belong to
// a record's birth.
type EORecordService interface {
	Create(ctx context.Context, req *models.EORecordCreateRequest, actor *models.User) (*models.EORecord, error)
	Update(ctx context.Context, id uuid.UUID, req *models.EORecordUpdateRequest, actor *models.User) (*models.EORecord, error)
}

type eoRecordService struct {
	records   repository.EORecordRepository
	types     repository.EORecordTypeRepository
	sequences repository.EOSequenceRepository
	workflows repository.WorkflowRepository
	eval      FormulaEvaluator
	deps      DependencyLoader
}

func NewEORecordService(
	records repository.EORecordRepository,
	types repository.EORecordTypeRepository,
	sequences repository.EOSequenceRepository,
	workflows repository.WorkflowRepository,
) EORecordService {
	return &eoRecordService{
		records:   records,
		types:     types,
		sequences: sequences,
		workflows: workflows,
		eval:      eoformula.New(0),
		deps:      &eoRecordDeps{records: records, types: types},
	}
}

func (s *eoRecordService) Create(ctx context.Context, req *models.EORecordCreateRequest, actor *models.User) (*models.EORecord, error) {
	if req == nil {
		return nil, errors.New("create eo record: no request")
	}
	if actor == nil {
		return nil, errors.New("create eo record: no actor, and every record records its creator")
	}

	t, err := s.types.GetByID(ctx, req.RecordTypeID)
	if err != nil {
		return nil, fmt.Errorf("create eo record: %w", err)
	}
	if t == nil {
		return nil, fmt.Errorf("create eo record: no record type %s", req.RecordTypeID)
	}
	if t.IsAbstract {
		return nil, fmt.Errorf("create eo record: record type %q is abstract and holds no records of its own", t.Key)
	}
	if !t.IsActive {
		return nil, fmt.Errorf("create eo record: record type %q is not active", t.Key)
	}

	fields, err := s.effectiveFields(ctx, t)
	if err != nil {
		return nil, fmt.Errorf("create eo record: %w", err)
	}

	var parent *models.EORecord
	if req.ParentRecordID != nil {
		parent, err = s.records.GetAnyByID(ctx, *req.ParentRecordID)
		if err != nil {
			return nil, fmt.Errorf("create eo record: load parent: %w", err)
		}
		if parent == nil {
			return nil, fmt.Errorf("create eo record: parent %s: %w", *req.ParentRecordID, ErrEORecordNotFound)
		}
		// Checked here, before anything is written or a sequence number
		// burned: parent_record_id alone buys the whole hierarchy — the
		// record counts as a child for Children, BlockingChildren and
		// ancestor derivation — while the rules that come with being a child
		// live on the link type. Without this, omitting one JSON field lets a
		// client set its own deadline (which the SLA sweep and escalation run
		// on) and its own sensitivity label.
		if strings.TrimSpace(req.ParentLinkTypeKey) == "" {
			return nil, fmt.Errorf("create eo record: parent %s was given with no link type: the link type carries the rules that apply to a child (derived deadline, sensitivity cascade), so it cannot be omitted", parent.ID)
		}
	}

	// The initial state is resolved before validation because it is the state
	// the record is created into, and required_at_state asks about the state
	// being entered. Reading it changes nothing; it is assigned in its own
	// step below.
	wf, err := ResolveWorkflowForType(ctx, t.ID, s.workflows)
	if err != nil {
		return nil, fmt.Errorf("create eo record: %w", err)
	}
	initial, err := InitialState(wf)
	if err != nil {
		return nil, fmt.Errorf("create eo record: %w", err)
	}

	rec := &models.EORecord{ID: uuid.New(), RecordTypeID: t.ID, CreatedByID: actor.ID}
	if parent != nil {
		rec.ParentRecordID = &parent.ID
	}

	// 1. validate. A new record has no history, so no field is locked yet.
	fieldErrs, err := ValidateRecordValues(fields, eoFlattenStored(rec), req.Values, &initial.ID, nil, s.eval)
	if err != nil {
		return nil, fmt.Errorf("create eo record: %w", err)
	}
	fieldErrs = eoMergeFieldErrors(fieldErrs, eoUnknownFieldErrors(fields, req.Values))
	if err := eoValidationError(fieldErrs); err != nil {
		return nil, err
	}
	if err := eoValidationError(eoApplyValues(rec, req.Values)); err != nil {
		return nil, err
	}

	// 2. allocate the reference. The number is drawn only once the payload is
	// known to be good; a failure after this point leaves a gap, since the
	// counter never goes backwards.
	if err := s.allocateReference(ctx, rec, t, parent); err != nil {
		return nil, err
	}

	// 3. the initial state.
	rec.StateID = &initial.ID

	// 4. link rules: the typed link to the parent, its derived deadline and
	// its sensitivity cascade.
	link, err := s.applyLinkRules(ctx, rec, parent, req.ParentLinkTypeKey, actor)
	if err != nil {
		return nil, fmt.Errorf("create eo record: %w", err)
	}

	// 5. derived fields, over the values written above.
	s.applyDerived(ctx, fields, rec)

	// 6. persist. The record, its parent link and the history row that says
	// it entered its initial state are one write.
	err = s.records.Transaction(ctx, func(r repository.EORecordRepository) error {
		if err := r.Create(ctx, rec); err != nil {
			return err
		}
		if link != nil {
			if err := r.CreateLink(ctx, link); err != nil {
				return err
			}
		}
		return r.CreateHistory(ctx, &models.EORecordHistory{
			RecordID:      rec.ID,
			RecordTypeID:  rec.RecordTypeID,
			ToStateID:     &initial.ID,
			PerformedByID: &actor.ID,
			Comment:       "record created",
		})
	})
	if err != nil {
		return nil, fmt.Errorf("create eo record %q: %w", rec.Reference, err)
	}
	return rec, nil
}

func (s *eoRecordService) Update(ctx context.Context, id uuid.UUID, req *models.EORecordUpdateRequest, actor *models.User) (*models.EORecord, error) {
	if req == nil {
		return nil, errors.New("update eo record: no request")
	}
	if actor == nil {
		return nil, errors.New("update eo record: no actor")
	}

	rec, err := s.records.GetByID(ctx, id, req.RecordTypeID)
	if err != nil {
		return nil, fmt.Errorf("update eo record %s: %w", id, err)
	}
	if rec == nil {
		return nil, fmt.Errorf("update eo record %s of type %s: %w", id, req.RecordTypeID, ErrEORecordNotFound)
	}
	t, err := s.types.GetByID(ctx, rec.RecordTypeID)
	if err != nil {
		return nil, fmt.Errorf("update eo record %s: %w", id, err)
	}
	if t == nil {
		return nil, fmt.Errorf("update eo record %s: no record type %s", id, rec.RecordTypeID)
	}
	fields, err := s.effectiveFields(ctx, t)
	if err != nil {
		return nil, fmt.Errorf("update eo record %s: %w", id, err)
	}

	// is_locked_after asks whether the record has EVER entered a state, which
	// only the history answers: EO workflows have return and exception paths,
	// so the current state is a different question.
	entered, err := s.records.EnteredStateIDs(ctx, rec.ID, rec.RecordTypeID)
	if err != nil {
		return nil, fmt.Errorf("update eo record %s: %w", id, err)
	}

	// The before-snapshot of the journal, taken while rec still holds what
	// the database holds.
	before := eoFlattenStored(rec)

	fieldErrs, err := ValidateRecordValues(fields, before, req.Values, rec.StateID, entered, s.eval)
	if err != nil {
		return nil, fmt.Errorf("update eo record %s: %w", id, err)
	}
	fieldErrs = eoMergeFieldErrors(fieldErrs, eoUnknownFieldErrors(fields, req.Values))
	if err := eoValidationError(fieldErrs); err != nil {
		return nil, err
	}
	if err := eoValidationError(eoApplyValues(rec, req.Values)); err != nil {
		return nil, err
	}

	s.applyDerived(ctx, fields, rec)

	// A field edit is not a transition, so the history row carries no
	// transition and no state change — but it is still a change to a record
	// whose path has to be traceable, and change_reason is captured for
	// exactly this. The record and its journal entry are one write.
	oldValues, newValues := eoDiffValues(before, eoFlattenStored(rec))
	err = s.records.Transaction(ctx, func(r repository.EORecordRepository) error {
		if err := r.Update(ctx, rec); err != nil {
			return err
		}
		if len(newValues) == 0 {
			return nil
		}
		oldJSON, err := json.Marshal(oldValues)
		if err != nil {
			return fmt.Errorf("encode old values: %w", err)
		}
		newJSON, err := json.Marshal(newValues)
		if err != nil {
			return fmt.Errorf("encode new values: %w", err)
		}
		return r.CreateHistory(ctx, &models.EORecordHistory{
			RecordID:      rec.ID,
			RecordTypeID:  rec.RecordTypeID,
			PerformedByID: &actor.ID,
			Comment:       rec.ChangeReason,
			OldValues:     oldJSON,
			NewValues:     newJSON,
		})
	})
	if err != nil {
		return nil, fmt.Errorf("update eo record %s: %w", id, err)
	}
	return rec, nil
}

// effectiveFields resolves a type's own fields plus every ancestor's. Nothing
// is copied at configuration time, so this runs on every save (spec §7.2).
func (s *eoRecordService) effectiveFields(ctx context.Context, t *models.EORecordType) ([]models.EOFieldDef, error) {
	return eoEffectiveFields(ctx, s.types, t)
}

// eoEffectiveFields is the shared resolution the save pipeline and the
// transition executor both need: a type's own field definitions plus every
// ancestor's, resolved at read time (spec §7.2).
func eoEffectiveFields(ctx context.Context, types repository.EORecordTypeRepository, t *models.EORecordType) ([]models.EOFieldDef, error) {
	ancestors, err := types.Ancestors(ctx, t)
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(ancestors)+1)
	ids = append(ids, t.ID)
	for _, a := range ancestors {
		ids = append(ids, a.ID)
	}
	defs, err := types.FieldDefs(ctx, ids)
	if err != nil {
		return nil, err
	}
	return ResolveEffectiveFields(t, ancestors, defs)
}

func (s *eoRecordService) allocateReference(ctx context.Context, rec *models.EORecord, t *models.EORecordType, parent *models.EORecord) error {
	pattern := strings.TrimSpace(t.ReferencePattern)
	if pattern == "" {
		return fmt.Errorf("create eo record: record type %q has no reference pattern", t.Key)
	}
	parentRef := ""
	if parent != nil {
		parentRef = parent.Reference
	}
	year := time.Now().Year()
	scope, err := EOSequenceScope(pattern, parentRef, year)
	if err != nil {
		return fmt.Errorf("create eo record of type %q: %w", t.Key, err)
	}
	seq, err := s.sequences.NextInScope(ctx, scope)
	if err != nil {
		return fmt.Errorf("create eo record of type %q: %w", t.Key, err)
	}
	ref, err := RenderReference(pattern, parentRef, year, seq)
	if err != nil {
		return fmt.Errorf("create eo record of type %q: %w", t.Key, err)
	}
	rec.Reference = ref
	return nil
}

// applyLinkRules writes the typed link to the parent and applies the rules the
// link type carries (spec §7.6): the derived deadline and the sensitivity
// cascade. It returns the link row to write, or nil when the hierarchy is
// recorded by parent_record_id alone.
func (s *eoRecordService) applyLinkRules(ctx context.Context, rec, parent *models.EORecord, linkTypeKey string, actor *models.User) (*models.EORecordLink, error) {
	if parent == nil {
		return nil, nil
	}
	if strings.TrimSpace(linkTypeKey) == "" {
		return nil, fmt.Errorf("parent %s was given with no link type", parent.ID)
	}
	lt, err := s.types.GetLinkTypeByKey(ctx, linkTypeKey)
	if err != nil {
		return nil, err
	}
	if lt == nil {
		return nil, fmt.Errorf("no link type %q", linkTypeKey)
	}
	existing, err := s.records.LinksFrom(ctx, rec.ID)
	if err != nil {
		return nil, err
	}
	if err := ValidateLink(lt, rec, parent, existing); err != nil {
		return nil, err
	}

	if due := DeriveDeadlineFromParent(lt, parent); due != nil {
		// A deadline counted back from the parent's own is the rule, so it
		// replaces whatever was submitted. It is NOT clamped when it lands in
		// the past: the record really is late the moment it exists, and
		// moving it to today would invent a deadline the configuration does
		// not describe and hide an overdue child from the SLA sweep. It is
		// logged, because a deadline before the record was created usually
		// means the offset is wrong rather than the work.
		rec.DueDate = due
		if due.Before(time.Now()) {
			log.Printf("eo: record %s of type %s inherits a deadline that has already passed (%s): parent %s is due %s and link type %q counts back %d days",
				rec.Reference, rec.RecordTypeID, due.Format(time.RFC3339), parent.Reference,
				parent.DueDate.Format(time.RFC3339), lt.Key, lt.DeadlineOffsetDays)
		}
	}
	// A confidential parent's children inherit its label (spec §7.6). The
	// cascade wins over whatever was submitted: it is a governance control,
	// not a default.
	if lt.CascadeSensitivity && parent.Sensitivity != "" {
		rec.Sensitivity = parent.Sensitivity
	}

	return &models.EORecordLink{
		ID:               uuid.New(),
		FromRecordID:     rec.ID,
		FromRecordTypeID: rec.RecordTypeID,
		ToRecordID:       parent.ID,
		ToRecordTypeID:   parent.RecordTypeID,
		LinkTypeKey:      lt.Key,
		CreatedByID:      &actor.ID,
	}, nil
}

// applyDerived recomputes the derived fields and writes what resolved onto the
// record, before it is persisted. A field that cannot resolve yet — a roll-up
// over children a new record does not have — must not block the save:
// RecomputeDerived returns what it could compute and reports the rest, which
// is logged.
func (s *eoRecordService) applyDerived(ctx context.Context, fields []models.EOFieldDef, rec *models.EORecord) {
	eoApplyDerived(ctx, fields, rec, s.deps)
}

// eoApplyDerived is the shared recompute the save pipeline and the transition
// executor both run (spec §7.5: recompute whenever a declared dependency
// changes, and a transition's field change is exactly such a change).
func eoApplyDerived(ctx context.Context, fields []models.EOFieldDef, rec *models.EORecord, deps DependencyLoader) {
	derived, err := RecomputeDerived(ctx, fields, rec, deps)
	if pending, broken := eoClassifyDeriveError(err); len(pending) > 0 || len(broken) > 0 {
		if len(pending) > 0 {
			log.Printf("eo: record %s: derived field(s) waiting on data: %v", rec.ID, errors.Join(pending...))
		}
		if len(broken) > 0 {
			log.Printf("eo: record %s: derived field configuration is broken: %v", rec.ID, errors.Join(broken...))
		}
	}
	if len(derived) == 0 {
		return
	}
	if errs := eoApplyValues(rec, derived); len(errs) > 0 {
		log.Printf("eo: record %s derived fields could not be stored: %v", rec.ID, errs)
	}
}
