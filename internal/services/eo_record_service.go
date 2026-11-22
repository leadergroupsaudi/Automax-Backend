package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/internal/services/eoformula"
	"github.com/automax/backend/internal/storage"
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
	if dst == nil {
		dst = map[string]string{}
	}
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

// ── the Documenta folder every record owns ──────────────────────────────────

// eoFolderNameMax bounds the subject part of a folder name. A subject column
// holds 500 characters and a folder name that long is neither browsable nor
// safely accepted by every storage backend behind Documenta.
const eoFolderNameMax = 120

// eoFolderFailureNote prefixes the history row written when a record is
// created without its folder. It is a fixed string so an operator (or a
// backfill) can find every affected record by it, beside the column query for
// an empty documenta_folder_id.
const eoFolderFailureNote = "documenta folder not created"

// eoDocumentaFolders creates the Documenta folder an EO record owns: one
// folder per record, under a per-type anchor folder at the workspace root.
//
// It calls CreateFolder and ListFiles directly and NEVER EnsureFolder, which
// is the wrong instrument here twice over:
//
//   - For the record's own folder there is nothing to ensure. The name
//     carries the record's reference, which is unique per type, and no second
//     record ever wants that folder. EnsureFolder would list the whole anchor
//     folder — a listing that pages nothing — on every single create, so the
//     cost of creating a record would grow with the number of records already
//     filed under its type.
//
//   - For the anchor, EnsureFolder SWALLOWS its ListFiles error
//     (`result, err := c.ListFiles(...)` then `if err == nil {`) and falls
//     through to create. Its first tier, the Redis lookup that would normally
//     hide that, does not exist for EO: cmd/server/main.go builds this client
//     with a nil Redis client on purpose, because the shipped client caches
//     its OAuth token and folder ids under keys that carry no client
//     identity. With no cache to short-circuit it, a flapping Documenta would
//     produce a NEW anchor on every failed listing and split the type's tree.
//     Here the listing error is returned instead: a failed listing says
//     nothing about what exists, so nothing is created on the strength of it.
//     (And had Redis been passed, EnsureFolder's two Set calls use TTL 0, so
//     a folder per record would leave one immortal cache entry per record.)
//
// The anchor is resolved once per record type per process and remembered, so
// the extra listing is paid once rather than per record.
type eoDocumentaFolders struct {
	client        storage.DocumentaClient
	workspaceName string

	mu sync.Mutex
	// anchors maps a record type id to its anchor folder id. Keyed by id, not
	// by name, so renaming a type does not strand the records already filed
	// under the anchor this process is using.
	anchors map[uuid.UUID]string
	// resolving holds the resolution in flight for a record type, if any.
	//
	// Without it the memo does not serialise anything: concurrent creates of
	// the same type all miss the cache, all list, all see no anchor and all
	// create one. That is not a rare interleaving — it is the FIRST BURST OF
	// CREATES AFTER A DEPLOY, when every listing is in flight at once, and it
	// yields one root-level anchor per concurrent create.
	//
	// Callers that arrive while a resolution is running wait for it and take
	// ITS result, rather than queueing to run their own. That distinction
	// matters when Documenta is down: a per-type lock held across the call
	// would make N concurrent creates wait N consecutive 30-second timeouts,
	// so the last one blocks for minutes. Sharing the outcome costs one
	// timeout for all of them. (This is singleflight for one key space,
	// written out to avoid promoting an indirect dependency to a direct one.)
	resolving map[uuid.UUID]*eoAnchorResolve
}

// eoAnchorResolve is one in-flight anchor resolution. done is closed when id
// and err are final; nothing writes them after that.
type eoAnchorResolve struct {
	done chan struct{}
	id   string
	err  error
}

// createFor creates the record's own folder and returns its id. The context
// must already carry the acting user (storage.ContextWithOnBehalf).
func (f *eoDocumentaFolders) createFor(ctx context.Context, t *models.EORecordType, rec *models.EORecord) (string, error) {
	anchorID, err := f.anchorFor(ctx, t)
	if err != nil {
		return "", err
	}
	folderID, err := f.client.CreateFolder(ctx, f.workspaceName, anchorID, eoRecordFolderName(rec))
	if err != nil {
		return "", fmt.Errorf("create the documenta folder for %s: %w", rec.Reference, err)
	}
	// "" is the record's no-folder sentinel. Storing a blank id as though it
	// were a folder would make a folderless record look filed, so a response
	// that carries no uuid is a failure, not a folder.
	if strings.TrimSpace(folderID) == "" {
		return "", fmt.Errorf("documenta returned an empty folder id for %s", rec.Reference)
	}
	return folderID, nil
}

// anchorFor returns the folder every record of this type is filed under,
// creating it only when the workspace root has been listed successfully and
// shown not to contain it.
func (f *eoDocumentaFolders) anchorFor(ctx context.Context, t *models.EORecordType) (string, error) {
	f.mu.Lock()
	if cached, ok := f.anchors[t.ID]; ok {
		f.mu.Unlock()
		return cached, nil
	}
	if inflight, ok := f.resolving[t.ID]; ok {
		// Someone else is already resolving this type. Wait for their answer
		// instead of starting a second listing that would create a second
		// anchor. The context is honoured so a cancelled request does not
		// wait out another request's remote timeout.
		f.mu.Unlock()
		select {
		case <-inflight.done:
			return inflight.id, inflight.err
		case <-ctx.Done():
			return "", fmt.Errorf("wait for the anchor folder of %q: %w", t.Key, ctx.Err())
		}
	}
	inflight := &eoAnchorResolve{done: make(chan struct{})}
	f.resolving[t.ID] = inflight
	f.mu.Unlock()

	id, err := f.resolveAnchor(ctx, t)

	f.mu.Lock()
	delete(f.resolving, t.ID)
	if err == nil {
		// Memoised only on success: a failed resolution must not be cached,
		// or one outage would leave the type anchorless for the process's
		// lifetime.
		f.anchors[t.ID] = id
	}
	f.mu.Unlock()

	inflight.id, inflight.err = id, err
	close(inflight.done)
	return id, err
}

// resolveAnchor is the remote half of anchorFor, run by exactly one caller per
// record type at a time. It holds no lock: the calls are HTTP.
func (f *eoDocumentaFolders) resolveAnchor(ctx context.Context, t *models.EORecordType) (string, error) {
	name := eoAnchorFolderName(t)
	// The parent listing, with its error RETURNED. This is the one call
	// EnsureFolder ignores the failure of.
	listing, err := f.client.ListFiles(ctx, f.workspaceName, "")
	if err != nil {
		return "", fmt.Errorf("list the workspace root for the %q folder: %w", name, err)
	}
	anchorID := ""
	if listing != nil {
		// The listing pages nothing and carries no total, so its length is
		// the only overflow signal there is: an anchor that scrolls off the
		// one page Documenta returns would be silently re-created. Logged at
		// resolve time — once per type per process — so an operator can see
		// the root growing before that happens.
		log.Printf("[EO] documenta workspace root holds %d entries (resolving the %q folder)", len(listing.Files), name)
		for _, file := range listing.Files {
			if file.Type == "folder" && file.Name == name {
				anchorID = file.UUID
				break
			}
		}
	}
	if anchorID == "" {
		anchorID, err = f.client.CreateFolder(ctx, f.workspaceName, "", name)
		if err != nil {
			return "", fmt.Errorf("create the %q folder: %w", name, err)
		}
		if strings.TrimSpace(anchorID) == "" {
			return "", fmt.Errorf("documenta returned an empty folder id for %q", name)
		}
	}
	return anchorID, nil
}

// EOCheckDocumentaBaseURL refuses an EO_DOCUMENTA_BASE_URL that cannot address
// Documenta. NewHTTPDocumentaClient does nothing but append "/api/v1" to it,
// so an empty value builds a client pointed at the relative path "/api/v1",
// and a scheme-less one ("mydocs.internal") builds "mydocs.internal/api/v1" —
// both of which parse fine and fail on every request instead of at boot. The
// other two Documenta blocks have a default URL that hides this; the EO block
// deliberately has none.
//
// It is a boot check, called from cmd/server/main.go, and it matters MORE
// because of the failure handling this task chose: a folder that cannot be
// created does not stop a record, so a misconfigured deployment would
// otherwise run for months filing nothing and saying so only in each record's
// history.
func EOCheckDocumentaBaseURL(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return errors.New("EO_DOCUMENTA_BASE_URL is empty")
	}
	u, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("EO_DOCUMENTA_BASE_URL %q is not a URL: %w", raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("EO_DOCUMENTA_BASE_URL %q has no http(s) scheme", raw)
	}
	if u.Host == "" {
		return fmt.Errorf("EO_DOCUMENTA_BASE_URL %q has no host", raw)
	}
	return nil
}

// eoAnchorFolderName is the per-type anchor's name: the type's display name,
// or its key when the name is blank, so the folder is never nameless.
func eoAnchorFolderName(t *models.EORecordType) string {
	if name := strings.TrimSpace(t.Name); name != "" {
		return eoClipFolderName(name)
	}
	return eoClipFolderName(strings.TrimSpace(t.Key))
}

// eoRecordFolderName names a record's folder "<reference> - <subject>". The
// reference comes first because it is what the register, the history and
// every notification call the record by, and it is unique per type.
func eoRecordFolderName(rec *models.EORecord) string {
	subject := strings.TrimSpace(rec.SubjectEn)
	if subject == "" {
		subject = strings.TrimSpace(rec.SubjectAr)
	}
	if subject == "" {
		return rec.Reference
	}
	return rec.Reference + " - " + eoClipFolderName(subject)
}

// eoClipFolderName clips on a RUNE boundary: subjects are Arabic, and cutting
// a multi-byte rune in half produces an invalid name rather than a short one.
func eoClipFolderName(s string) string {
	runes := []rune(s)
	if len(runes) <= eoFolderNameMax {
		return s
	}
	return strings.TrimSpace(string(runes[:eoFolderNameMax])) + "\u2026"
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
	// caps resolves a type's enabled capabilities; nil means the process-wide
	// registry. A test seam, like eval.
	caps eoCapabilityResolver
	// policies loads the initial state's escalation policy. Nil means SLA
	// clocks are not wired: creation schedules no timer.
	policies repository.EscalationPolicyRepository
	// folders gives every created record its Documenta folder. Nil means no
	// Documenta client is wired and records are created folderless — which is
	// a legible state, not a broken one: documenta_folder_id is NOT NULL
	// DEFAULT '' and every record made before this existed has it.
	folders *eoDocumentaFolders
}

// EORecordOption configures the record service at construction. It exists so
// that wiring a Documenta client is additive: the two constructors below keep
// the signatures everything already calls.
type EORecordOption func(*eoRecordService)

// EOWithDocumentaFolders gives every record this service creates a folder in
// Documenta (see eoDocumentaFolders). workspaceName is passed into every
// folder call because NewHTTPDocumentaClient IGNORES cfg.WorkspaceName — the
// per-call argument is the only way a configured workspace name can reach
// Documenta at all, and even then it is a fallback the OAuth token's
// workspace_uuid claim overrides (storage.resolveWorkspace).
//
// A nil client is the same as not passing the option: nothing is wired and
// records are created folderless.
func EOWithDocumentaFolders(client storage.DocumentaClient, workspaceName string) EORecordOption {
	return func(s *eoRecordService) {
		if client == nil {
			return
		}
		s.folders = &eoDocumentaFolders{
			client:        client,
			workspaceName: workspaceName,
			anchors:       map[uuid.UUID]string{},
			resolving:     map[uuid.UUID]*eoAnchorResolve{},
		}
	}
}

func NewEORecordService(
	records repository.EORecordRepository,
	types repository.EORecordTypeRepository,
	sequences repository.EOSequenceRepository,
	workflows repository.WorkflowRepository,
	opts ...EORecordOption,
) EORecordService {
	return NewEORecordServiceWithSLA(records, types, sequences, workflows, nil, opts...)
}

// NewEORecordServiceWithSLA is NewEORecordService plus SLA clocks: a record is
// created with the escalation timers of its initial state already scheduled, in
// the create transaction. An untouched record is the one that must escalate, so
// the clock cannot wait for the first transition.
func NewEORecordServiceWithSLA(
	records repository.EORecordRepository,
	types repository.EORecordTypeRepository,
	sequences repository.EOSequenceRepository,
	workflows repository.WorkflowRepository,
	policies repository.EscalationPolicyRepository,
	opts ...EORecordOption,
) EORecordService {
	s := &eoRecordService{
		policies:  policies,
		records:   records,
		types:     types,
		sequences: sequences,
		workflows: workflows,
		eval:      eoformula.New(0),
		deps:      &eoRecordDeps{records: records, types: types},
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
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
	caps, err := s.caps.resolve(t)
	if err != nil {
		return nil, fmt.Errorf("create eo record: %w", err)
	}
	if err := eoCapabilitiesCheckConfig(caps, fields, nil, nil); err != nil {
		return nil, fmt.Errorf("create eo record: %w", err)
	}
	ctx = WithEOActor(ctx, actor)

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
	// The capabilities' save-time checks, over the record as it will be saved.
	// The capabilities' read path. This is the one place it is built on the
	// POOLED handles, and it is safe because no transaction is open yet: the
	// create pipeline validates and derives BEFORE it opens one, exactly as
	// it resolves the capability list and the effective fields before it. The
	// timer arming at the end of the transaction gets a transaction-bound
	// reader of its own. The name says which is which: preTxCapRead is still
	// in scope inside the closure below, and using it there is the deadlock.
	preTxCapRead := eoNewCapabilityReader(s.records, s.types)
	capErrs, err := eoCapabilitiesValidate(ctx, preTxCapRead, caps, rec)
	if err != nil {
		return nil, fmt.Errorf("create eo record: %w", err)
	}
	if err := eoValidationError(capErrs); err != nil {
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
	if err := eoCapabilitiesDerive(ctx, preTxCapRead, caps, rec); err != nil {
		return nil, fmt.Errorf("create eo record: %w", err)
	}

	// 6. the record's Documenta folder, BEFORE the transaction is opened and
	// never inside it. The call is HTTP to another system behind a 30-second
	// client timeout; making it inside the transaction would hold a pooled
	// database connection — and an open Postgres transaction — for the length
	// of a remote round trip, on every create.
	//
	// A failure here does NOT lose the record (Review Focus 4). The record is
	// written with an empty documenta_folder_id, which C1 made a legible
	// value rather than a null, and a history row on THIS record says why, in
	// the same transaction as the record itself. The alternative — failing the
	// create — would make an outage of the document store an outage of the
	// Executive Office, and would strand the reference this record has
	// already burned.
	folderErr := s.createRecordFolder(ctx, t, rec, actor)

	// 7. persist. The record, its parent link and the history row that says
	// it entered its initial state are one write.
	// The initial state's policy is read before the transaction: a pooled read
	// inside it would take a second connection.
	slaState, err := eoStateForSLA(ctx, s.policies, initial)
	if err != nil {
		return nil, fmt.Errorf("create eo record %q: %w", rec.Reference, err)
	}
	err = s.records.Transaction(ctx, func(r repository.EORecordRepository, rt repository.EORecordTypeRepository, timers repository.EOTimerRepository) error {
		// Inside the transaction the capabilities read through ITS handles,
		// never the pooled ones captured above: this closure holds a
		// connection, and a second pooled read would wait for a free one
		// forever on a capped pool.
		txCapRead := eoNewCapabilityReader(r, rt)
		if err := r.Create(ctx, rec); err != nil {
			return err
		}
		if link != nil {
			if err := r.CreateLink(ctx, link); err != nil {
				return err
			}
		}
		if err := r.CreateHistory(ctx, &models.EORecordHistory{
			RecordID:      rec.ID,
			RecordTypeID:  rec.RecordTypeID,
			ToStateID:     &initial.ID,
			PerformedByID: &actor.ID,
			Comment:       "record created",
		}); err != nil {
			return err
		}
		// The folder failure is recorded ON THE RECORD IT HAPPENED TO, in the
		// same write as the record: the row cannot be lost the way a
		// post-commit log line can, and it cannot be filed against another
		// record the way A5's close_parent note was. documenta_folder_id
		// stays '' beside it, so the record is findable both ways.
		if folderErr != nil {
			if err := r.CreateHistory(ctx, &models.EORecordHistory{
				RecordID:       rec.ID,
				RecordTypeID:   rec.RecordTypeID,
				PerformedByID:  &actor.ID,
				IsSystemAction: true,
				Comment:        eoFolderFailureComment(rec, folderErr),
			}); err != nil {
				return err
			}
		}
		// The SLA clock starts with the record, on the transaction's timer
		// handle, so a failed create leaves no timer behind.
		if slaState != nil {
			if err := ScheduleStateSLA(ctx, rec, slaState, timers); err != nil {
				return err
			}
		}
		// Capability timers for the initial state, last, on the same handle.
		// No cancel: a new record has no earlier timers.
		for _, c := range caps {
			if err := eoArmCapabilityTimers(ctx, txCapRead, c, rec, timers); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("create eo record %q: %w", rec.Reference, err)
	}
	return rec, nil
}

// createRecordFolder fills in rec.DocumentaFolderID, or returns the reason it
// could not. It returns nil when no Documenta client is wired, which is the
// default deployment (EO_DOCUMENTA_ENABLED=false): a record with no folder is
// then the expected state and nothing is reported.
func (s *eoRecordService) createRecordFolder(ctx context.Context, t *models.EORecordType, rec *models.EORecord, actor *models.User) error {
	if s.folders == nil {
		return nil
	}
	// The acting user travels with the call. Without this every EO folder is
	// owned by system@automax.local and the Documenta audit trail cannot say
	// who filed what.
	folderID, err := s.folders.createFor(storage.ContextWithOnBehalf(ctx, actor.Email), t, rec)
	if err != nil {
		log.Printf("[EO] record %s (%s) created without a documenta folder: %v", rec.Reference, rec.ID, err)
		return err
	}
	rec.DocumentaFolderID = folderID
	return nil
}

// eoFolderFailureComment is the history row an operator reads to find out why
// a record has no folder, and which record it is.
func eoFolderFailureComment(rec *models.EORecord, err error) string {
	return fmt.Sprintf("%s for %s: %v. The record was created without one (documenta_folder_id is empty) and can be backfilled.",
		eoFolderFailureNote, rec.Reference, err)
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
	caps, err := s.caps.resolve(t)
	if err != nil {
		return nil, fmt.Errorf("update eo record %s: %w", id, err)
	}
	if err := eoCapabilitiesCheckConfig(caps, fields, nil, nil); err != nil {
		return nil, fmt.Errorf("update eo record %s: %w", id, err)
	}
	ctx = WithEOActor(ctx, actor)

	// Everything from here runs INSIDE one transaction that opens with a row
	// lock, the same shape EOTransitionService.Execute uses and for the same
	// reason. Without it, Update read the record on a pooled connection,
	// validated and merged the edit against that snapshot, then wrote EVERY
	// column back (eoUpdatableColumns lists them, so that clearing a due date
	// is not mistaken for leaving it alone). Two concurrent updates therefore
	// both read the same row, each applied its own keys to its own stale copy,
	// and the second write silently reverted the first one's edits — including
	// edits to fields it never mentioned. A transaction alone does not prevent
	// it: under READ COMMITTED both would still read the pre-image.
	//
	// Taking the lock rather than switching to the UpdateValues jsonb merge,
	// because the merge is not a substitute here. Most of what this pipeline
	// writes — subject_ar, priority, sensitivity, due_date, assigned_user_id,
	// change_reason — is a real COLUMN, not a key in the values bag, and
	// UpdateValues touches only the bag. It would leave the column edits
	// racing exactly as before while making the code look fixed. The lock also
	// gives the validation a current baseline: is_locked_after and the
	// locked-field check compare the submitted value against the STORED one,
	// and against a stale snapshot they judge the wrong thing.
	//
	// The reads that do not change under a concurrent edit — the record type,
	// the effective fields, the capability set — stay outside, because a
	// pooled read taken while this transaction holds its connection and a row
	// lock waits for a free connection indefinitely if the pool is capped.
	var updated *models.EORecord
	err = s.records.Transaction(ctx, func(r repository.EORecordRepository, rt repository.EORecordTypeRepository, _ repository.EOTimerRepository) error {
		fresh, err := r.LockForUpdate(ctx, id, rec.RecordTypeID)
		if err != nil {
			return err
		}
		if fresh == nil {
			return fmt.Errorf("record %s of type %s: %w", id, rec.RecordTypeID, ErrEORecordNotFound)
		}

		// is_locked_after asks whether the record has EVER entered a state,
		// which only the history answers: EO workflows have return and
		// exception paths, so the current state is a different question.
		entered, err := r.EnteredStateIDs(ctx, fresh.ID, fresh.RecordTypeID)
		if err != nil {
			return err
		}

		// The before-snapshot of the journal, taken from the LOCKED row, so it
		// is what the database holds and no one else can change it until this
		// transaction ends.
		before := eoFlattenStored(fresh)

		fieldErrs, err := ValidateRecordValues(fields, before, req.Values, fresh.StateID, entered, s.eval)
		if err != nil {
			return err
		}
		fieldErrs = eoMergeFieldErrors(fieldErrs, eoUnknownFieldErrors(fields, req.Values))
		if err := eoValidationError(fieldErrs); err != nil {
			return err
		}
		if err := eoValidationError(eoApplyValues(fresh, req.Values)); err != nil {
			return err
		}
		// The capability hooks' read path, built on the TRANSACTION's handles
		// for the same reason the dependency loader below is: a pooled read
		// taken here would take a second connection while this transaction
		// holds its own plus the row lock.
		capRead := eoNewCapabilityReader(r, rt)
		capErrs, err := eoCapabilitiesValidate(ctx, capRead, caps, fresh)
		if err != nil {
			return err
		}
		if err := eoValidationError(capErrs); err != nil {
			return err
		}

		// The dependency loader is built on the TRANSACTION's repositories, as
		// it is in Execute: a child or ancestor read on the pooled ones would
		// take a second connection while this transaction holds its own plus
		// the row lock.
		eoApplyDerived(ctx, fields, fresh, &eoRecordDeps{records: r, types: rt})
		if err := eoCapabilitiesDerive(ctx, capRead, caps, fresh); err != nil {
			return err
		}

		// A field edit is not a transition, so the history row carries no
		// transition and no state change — but it is still a change to a
		// record whose path has to be traceable, and change_reason is captured
		// for exactly this. The record and its journal entry are one write.
		if err := r.Update(ctx, fresh); err != nil {
			return err
		}
		updated = fresh
		oldValues, newValues := eoDiffValues(before, eoFlattenStored(fresh))
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
			RecordID:      fresh.ID,
			RecordTypeID:  fresh.RecordTypeID,
			PerformedByID: &actor.ID,
			Comment:       fresh.ChangeReason,
			OldValues:     oldJSON,
			NewValues:     newJSON,
		})
	})
	if err != nil {
		// A refusal keeps its own identity. The pipeline's validation errors
		// and ErrEORecordNotFound now come back THROUGH the transaction, and
		// wrapping them in "update eo record …" prose here would not break
		// errors.As/errors.Is but would change the message every caller and
		// test reads for a failure that is not about the update at all.
		var invalid *EOValidationError
		if errors.As(err, &invalid) || errors.Is(err, ErrEORecordNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("update eo record %s: %w", id, err)
	}
	return updated, nil
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
