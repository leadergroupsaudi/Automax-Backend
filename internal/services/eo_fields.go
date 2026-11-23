package services

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/services/eoformula"
	"github.com/automax/backend/internal/storage"
	"github.com/google/uuid"
)

// eoMaxInheritanceDepth bounds the ancestor walk. Real hierarchies are a few
// levels deep; the bound is a backstop that turns pathological input into an
// error, alongside the explicit revisit check.
const eoMaxInheritanceDepth = 32

// ResolveEffectiveFields returns the fields a record type effectively has: its
// own plus every ancestor's, ordered ancestors-first (root ancestor first),
// then by SortOrder, then by Key. Resolution happens at read time and nothing
// is copied (spec §7.2).
//
// If a type redefines an ancestor's field key, the type's definition replaces
// the ancestor's at the ancestor's position; the key appears once. The
// overriding definition's own SortOrder has no effect on ordering. An override
// narrows an inherited field (e.g. optional -> required, a relabel) rather than
// adding a new one, and a field must sit in the same position on every record
// type's form; do not "fix" this to position by the child's SortOrder.
//
// The ancestors slice is not trusted to be ordered or acyclic: the chain is
// built by following ParentTypeID through it. A cycle, a chain deeper than
// eoMaxInheritanceDepth, or a ParentTypeID naming a type absent from ancestors
// is an error. defs may contain fields of unrelated types; they are ignored.
func ResolveEffectiveFields(t *models.EORecordType, ancestors []models.EORecordType, defs []models.EOFieldDef) ([]models.EOFieldDef, error) {
	if t == nil {
		return nil, fmt.Errorf("resolve effective fields: nil record type")
	}

	byID := make(map[uuid.UUID]*models.EORecordType, len(ancestors))
	for i := range ancestors {
		byID[ancestors[i].ID] = &ancestors[i]
	}

	// Chain from the type itself up to the root, then reversed to root-first.
	chain := []*models.EORecordType{t}
	visited := map[uuid.UUID]bool{t.ID: true}
	for cur := t; cur.ParentTypeID != nil; {
		pid := *cur.ParentTypeID
		if visited[pid] {
			return nil, fmt.Errorf("resolve effective fields: inheritance cycle at record type %s", pid)
		}
		if len(chain) >= eoMaxInheritanceDepth {
			return nil, fmt.Errorf("resolve effective fields: inheritance deeper than %d levels", eoMaxInheritanceDepth)
		}
		parent, ok := byID[pid]
		if !ok {
			return nil, fmt.Errorf("resolve effective fields: ancestor %s of record type %s not provided", pid, cur.ID)
		}
		visited[pid] = true
		chain = append(chain, parent)
		cur = parent
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}

	perType := make(map[uuid.UUID][]models.EOFieldDef, len(chain))
	for _, d := range defs {
		if visited[d.RecordTypeID] {
			perType[d.RecordTypeID] = append(perType[d.RecordTypeID], d)
		}
	}

	var out []models.EOFieldDef
	index := make(map[string]int)
	for _, rt := range chain {
		own := perType[rt.ID]
		sort.SliceStable(own, func(a, b int) bool {
			if own[a].SortOrder != own[b].SortOrder {
				return own[a].SortOrder < own[b].SortOrder
			}
			return own[a].Key < own[b].Key
		})
		for _, d := range own {
			if i, seen := index[d.Key]; seen {
				out[i] = d
				continue
			}
			index[d.Key] = len(out)
			out = append(out, d)
		}
	}
	return out, nil
}

// FormulaEvaluator evaluates a boolean expression against an environment. The
// timeout is the implementation's concern, not part of the interface.
type FormulaEvaluator interface {
	EvalBool(expression string, env map[string]any) (bool, error)
}

// ValidateRecordValues checks submitted values against the effective fields
// (spec §7.4, §7.12). It returns field key -> message; an empty map means
// valid. The error return is for failures of the machinery itself (an
// evaluator error, a malformed field definition), never for invalid input.
//
// values is the submitted payload, possibly partial.
//
// stored is the record's current effective values keyed by field key, nil or
// empty on create. It spans BOTH the real eo_records columns (the inherited
// base fields such as subject_ar, priority, due_date) and the jsonb values
// bag; the caller flattens them. Do not read rec.Values here: that would make
// every inherited field look empty. This function stays ignorant of storage
// layout on purpose. For requiredness a field's effective value is the
// submitted one if its key is present, else the stored one. An explicit nil
// (or a blank string, or an empty list) is empty, exactly as an absent key
// with nothing stored is.
//
// A value supplied for a derived or auto field is an error whether or not it
// is nil; such fields are also exempt from requiredness since the system
// fills them. A locked field (IsLockedAfterStateID in enteredStateIDs) may be
// resubmitted unchanged, so a client can echo a whole form, but not altered.
// enteredStateIDs is every state the record has ever entered, from history.
// MESSAGES ARE ENGLISH-ONLY, against spec §5's bilingual requirement. The
// error returned here is passed to the client verbatim by the handler, so a
// refusal reaches an Arabic UI in English. Noted, not fixed, in the Plan 1 fix
// wave (2026-11-22): the fix is to return an i18n KEY plus its arguments
// instead of a sentence, and it belongs with Plan 2's forms, which is where
// the client-signed Arabic labels for each field and kind arrive. Writing
// Arabic wording the client has not approved now would only have to be
// revised then. See spec §5.
func ValidateRecordValues(
	fields []models.EOFieldDef,
	stored map[string]any,
	values map[string]any,
	targetStateID *uuid.UUID,
	enteredStateIDs map[uuid.UUID]bool,
	eval FormulaEvaluator,
) (map[string]string, error) {
	errs := map[string]string{}
	var env map[string]any // built lazily, only if a rule needs it

	for _, f := range fields {
		name := f.Label
		if name == "" {
			name = f.Key
		}
		submitted, has := values[f.Key]

		// Fail closed on vocabulary: a typo must not silently mean "never
		// required" or "user-writable". Empty means the column defaults.
		switch f.Provenance {
		case "", models.EOProvenanceUser, models.EOProvenanceAuto, models.EOProvenanceDerived, models.EOProvenanceSystem:
		default:
			return nil, fmt.Errorf("validate record values: field %q has unknown provenance %q", f.Key, f.Provenance)
		}
		switch f.RequiredMode {
		case "", models.EORequiredNever, models.EORequiredAlways, models.EORequiredAtState, models.EORequiredConditional:
		default:
			return nil, fmt.Errorf("validate record values: field %q has unknown required_mode %q", f.Key, f.RequiredMode)
		}

		if f.Provenance == models.EOProvenanceDerived || f.Provenance == models.EOProvenanceAuto {
			if has {
				errs[f.Key] = fmt.Sprintf("%s is set by the system and cannot be written", name)
			}
			continue
		}

		if has && f.IsLockedAfterStateID != nil && enteredStateIDs[*f.IsLockedAfterStateID] {
			if !eoValuesEqual(submitted, stored[f.Key]) {
				errs[f.Key] = fmt.Sprintf("%s can no longer be changed", name)
				continue
			}
		}

		// Per-kind shape, for the two kinds spec §7.12 pins to Documenta.
		//
		// Only the SUBMITTED value is checked, never the stored one: an edit
		// that leaves a file field alone must not be refused because of what
		// an earlier save — one made before this check existed — put there.
		// A client that ECHOES such a value back does get the error, which is
		// the honest answer: it is asking for that value to be written again.
		if has {
			if msg := eoFileValueError(f.Kind, name, submitted); msg != "" {
				errs[f.Key] = msg
				continue
			}
		}

		effective := stored[f.Key]
		if has {
			effective = submitted
		}
		if !eoIsEmpty(effective) {
			continue
		}

		required := false
		switch f.RequiredMode {
		case models.EORequiredAlways:
			required = true
		case models.EORequiredAtState:
			if targetStateID != nil {
				var ids []uuid.UUID
				if len(f.RequiredAtStateIDs) > 0 {
					if err := json.Unmarshal(f.RequiredAtStateIDs, &ids); err != nil {
						return nil, fmt.Errorf("validate record values: field %q required_at_state_ids: %w", f.Key, err)
					}
				}
				for _, id := range ids {
					if id == *targetStateID {
						required = true
						break
					}
				}
			}
		case models.EORequiredConditional:
			if strings.TrimSpace(f.RequiredRule) == "" {
				return nil, fmt.Errorf("validate record values: field %q is conditional but has no required_rule", f.Key)
			}
			if eval == nil {
				return nil, fmt.Errorf("validate record values: field %q needs a formula evaluator", f.Key)
			}
			if env == nil {
				env = make(map[string]any, len(stored)+len(values))
				for k, v := range stored {
					env[k] = v
				}
				for k, v := range values {
					env[k] = v
				}
			}
			ok, err := eval.EvalBool(f.RequiredRule, env)
			if err != nil {
				// An unresolved reference is not a failure of the machinery:
				// it means the rule asked about a field nobody supplied and
				// the record does not hold, so the condition it tests CANNOT
				// be met and the field is not required. Returning it made the
				// handler answer 500 to an ordinary empty create — measured
				// on three of the pilot's four conditional rules, whose
				// subject fields (direction_pattern, directive_origin,
				// recommendation) are values-bag keys absent from a blank
				// form. A genuinely broken rule — a syntax error, a function
				// formulas do not have, a result that is not a boolean —
				// still returns, because that is configuration to fix rather
				// than a value to supply.
				if eoRequiredRuleUnmet(err) {
					continue
				}
				return nil, fmt.Errorf("validate record values: field %q required_rule: %w", f.Key, err)
			}
			required = ok
		}
		if required {
			errs[f.Key] = fmt.Sprintf("%s is required", name)
		}
	}
	return errs, nil
}

// eoRequiredRuleUnmet reports whether an EvalBool failure is ONLY an
// unresolved reference, and therefore a condition that cannot be met rather
// than a rule that is broken.
//
// It is shaped on eoClassifyDeriveError (eo_record_service.go), which splits
// the same error for derived fields into "waiting on data" and "broken
// configuration". The joined case is walked arm by arm on purpose: errors.Is
// alone reports true for a join that holds one unresolved reference beside a
// real failure, and that join is a broken rule.
func eoRequiredRuleUnmet(err error) bool {
	if err == nil {
		return false
	}
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		arms := joined.Unwrap()
		if len(arms) == 0 {
			return false
		}
		for _, e := range arms {
			if !eoRequiredRuleUnmet(e) {
				return false
			}
		}
		return true
	}
	return errors.Is(err, eoformula.ErrUnresolvedReference)
}

// ── file and multi_file carry Documenta ids (spec §7.12) ────────────────────

// eoFileValueError validates a submitted value against the two field kinds
// that hold Documenta file ids, and returns the message for the field error
// map, or "" when the value is acceptable or the kind is not a file kind.
//
// An EMPTY value is acceptable here whatever the kind: whether a field may be
// empty is the requiredness question, decided a few lines below this call,
// and answering it twice would report "is required" as a shape error.
//
// `file` holds ONE id and `multi_file` a LIST of them. Neither stands in for
// the other: a one-element list in a `file` field, or a bare id in a
// `multi_file` field, is refused rather than coerced, so the value a register
// reads back has the shape its field definition promises.
func eoFileValueError(kind, name string, v any) string {
	switch kind {
	case models.EOKindFile:
		if eoIsEmpty(v) {
			return ""
		}
		s, ok := v.(string)
		if !ok || !eoIsDocumentaFileID(s) {
			return fmt.Sprintf("%s must be a Documenta file id", name)
		}
	case models.EOKindMultiFile:
		if eoIsEmpty(v) {
			return ""
		}
		list, ok := eoValueList(v)
		if !ok {
			return fmt.Sprintf("%s must be a list of Documenta file ids", name)
		}
		for _, item := range list {
			s, ok := item.(string)
			if !ok || !eoIsDocumentaFileID(s) {
				return fmt.Sprintf("%s must be a list of Documenta file ids", name)
			}
		}
	}
	return ""
}

// eoValueList normalises the two ways a list reaches this function: []any, as
// a JSON body decodes it, and a typed slice, as a Go caller writes it.
func eoValueList(v any) ([]any, bool) {
	if list, ok := v.([]any); ok {
		return list, true
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array {
		return nil, false
	}
	out := make([]any, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		out[i] = rv.Index(i).Interface()
	}
	return out, true
}

// eoIsDocumentaFileID reports whether s has the shape of a Documenta file id.
//
// It is a SHAPE check and nothing more: it never asks Documenta whether the
// file exists. The pilot's default deployment runs with EO Documenta DISABLED
// (EO_DOCUMENTA_ENABLED=false), where no Documenta client exists at all, so a
// check that called out would accept a form on one deployment and refuse it on
// another — and would turn saving a record into an upstream round trip per
// attachment. Whether an id still resolves is a question for the moment the
// file is opened, not for the moment it is referenced.
//
// Two shapes are accepted:
//
//   - a canonical 36-character uuid, which is what the HTTP client reports as
//     DmsFile.UUID and what its UploadFile returns;
//   - storage.StubFileIDPrefix followed by one, which is what
//     StubDocumentaClient.UploadFile returns when Documenta is disabled.
//
// The second grants NO authority the first does not: the check accepts any
// well-formed uuid without asking whether a file exists behind it, so
// accepting the prefixed form widens nothing except the spelling. It is here
// so that a deployment running the stub is not refused by shape alone, and it
// is honest about its reach: it covers the id an UPLOAD returns, and no EO
// code path calls UploadFile yet — there is no EO upload route in the server,
// so today the prefix protects a flow that does not exist. The stub's
// browsing and search methods hand out fixed sample ids (stub-file-readme,
// stub-file-evidence, stub-file-search-1) that are not uuid-shaped and are
// NOT accepted; they name files nobody uploaded.
//
// The 36-character check is deliberate: uuid.Parse also accepts the
// unhyphenated, braced and urn: spellings, and none of those is the string
// Documenta gave out. Accepting them would mean the register stores an id in a
// spelling Documenta never used, and a later equality check against the
// upstream one would fail.
func eoIsDocumentaFileID(s string) bool {
	id := strings.TrimPrefix(s, storage.StubFileIDPrefix)
	if len(id) != 36 {
		return false
	}
	_, err := uuid.Parse(id)
	return err == nil
}

func eoIsEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	case []any:
		return len(x) == 0
	}
	// Typed nils: a NULL column flattened into stored arrives as a non-nil
	// interface holding a nil pointer, slice or map.
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr:
		return rv.IsNil()
	case reflect.Slice, reflect.Map:
		return rv.IsNil() || rv.Len() == 0
	}
	return false
}

// eoValuesEqual compares two values by their JSON encoding, so a submitted
// int and a stored float64 (as decoded from jsonb) compare equal.
func eoValuesEqual(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}
