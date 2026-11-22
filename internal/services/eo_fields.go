package services

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/automax/backend/internal/models"
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

func eoIsEmpty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	case []any:
		return len(x) == 0
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
