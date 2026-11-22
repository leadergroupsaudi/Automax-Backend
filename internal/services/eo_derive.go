package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/services/eoformula"
	"github.com/google/uuid"
)

// DependencyLoader fetches the records a derived field may depend on. It is
// key-based (record type key), unlike the id-based repository; an adapter
// between the two lives with the caller.
type DependencyLoader interface {
	// Children returns the direct children of parentID whose type key is recordTypeKey.
	Children(ctx context.Context, parentID uuid.UUID, recordTypeKey string) ([]models.EORecord, error)
	// Ancestor returns the nearest ancestor of rec whose record type key is
	// direction, or nil (with no error) if there is none.
	Ancestor(ctx context.Context, rec *models.EORecord, direction string) (*models.EORecord, error)
}

// ErrEODeriveCycle is reported for derived fields that depend on each other in a loop.
var ErrEODeriveCycle = errors.New("derived fields form a dependency cycle")

// A derived field's recompute_on is a JSON array of dependency objects:
//
//	{"scope":"own",      "field":"visit_date"}
//	{"scope":"child",    "type":"execution_plan_stage", "field":"weeks", "aggregates":["sum"]}
//	{"scope":"ancestor", "type":"office_direction",     "field":"acknowledged_on"}
//
// "own" reads the record's own values (a derived field listed here is
// computed first). "child" aggregates a field over the record's children of
// one type; aggregates default to ["sum"] and may be sum, min, max, avg.
// "ancestor" reads a field of the nearest ancestor of the named type.
//
// Env keys the formula author writes (all lower-case):
//
//	own:      <field>                              e.g. visit_date
//	child:    children_<type>_<aggregate>_<field>  e.g. children_execution_plan_stage_sum_weeks
//	          children_<type>_count                number of children of that type (always set)
//	ancestor: ancestor_<type>_<field>              e.g. ancestor_office_direction_acknowledged_on
//
// Keys with no value are omitted from the env, never set to nil, so a formula
// over missing data yields eoformula.ErrUnresolvedReference. Child aggregates
// skip children lacking a numeric value; if none has one the aggregate key is
// omitted. String values shaped like YYYY-MM-DD or RFC 3339 become time.Time.
type eoDependency struct {
	Scope      string   `json:"scope"`
	Type       string   `json:"type"`
	Field      string   `json:"field"`
	Aggregates []string `json:"aggregates"`
}

const (
	eoScopeOwn      = "own"
	eoScopeChild    = "child"
	eoScopeAncestor = "ancestor"
)

// RecomputeDerived evaluates every derived field that has a formula and
// returns key -> value for those that resolved. Fields that fail (unresolved
// reference, loader error, bad config, cycle) are left out of the map and
// their errors are joined into the returned error, so callers can save the
// map and still surface the problems.
func RecomputeDerived(ctx context.Context, fields []models.EOFieldDef, rec *models.EORecord, deps DependencyLoader) (map[string]any, error) {
	result := map[string]any{}

	derived := map[string]*models.EOFieldDef{}
	var order []string
	for i := range fields {
		f := &fields[i]
		if f.Provenance == models.EOProvenanceDerived && strings.TrimSpace(f.Formula) != "" {
			derived[f.Key] = f
			order = append(order, f.Key)
		}
	}

	var errs []error
	parsed := map[string][]eoDependency{}
	var valid []string
	for _, key := range order {
		var d []eoDependency
		if raw := derived[key].RecomputeOn; len(raw) > 0 {
			if err := json.Unmarshal(raw, &d); err != nil {
				errs = append(errs, fmt.Errorf("derived field %q: invalid recompute_on: %w", key, err))
				continue
			}
		}
		if err := eoCheckDeps(d); err != nil {
			errs = append(errs, fmt.Errorf("derived field %q: %w", key, err))
			continue
		}
		parsed[key] = d
		valid = append(valid, key)
	}

	sorted, cyclic := eoSortDerived(valid, parsed, derived)
	for _, key := range cyclic {
		errs = append(errs, fmt.Errorf("derived field %q: %w", key, ErrEODeriveCycle))
	}

	own := eoOwnEnv(rec, derived)
	ev := eoformula.New(0)
	children := map[string][]models.EORecord{}
	ancestors := map[string]*models.EORecord{}

	for _, key := range sorted {
		env := map[string]any{}
		for k, v := range own {
			env[k] = v
		}
		for k, v := range result {
			env[k] = v
		}
		var depErr error
		for _, d := range parsed[key] {
			if deps == nil && d.Scope != eoScopeOwn {
				depErr = errors.New("no dependency loader configured for a child or ancestor dependency")
				break
			}
			switch d.Scope {
			case eoScopeChild:
				kids, ok := children[d.Type]
				if !ok {
					var err error
					kids, err = deps.Children(ctx, rec.ID, d.Type)
					if err != nil {
						depErr = fmt.Errorf("load %q children: %w", d.Type, err)
						break
					}
					children[d.Type] = kids
				}
				eoAddChildAggregates(env, kids, d)
			case eoScopeAncestor:
				anc, ok := ancestors[d.Type]
				if !ok {
					var err error
					anc, err = deps.Ancestor(ctx, rec, d.Type)
					if err != nil {
						depErr = fmt.Errorf("load %q ancestor: %w", d.Type, err)
						break
					}
					ancestors[d.Type] = anc
				}
				if anc != nil {
					if v, ok := eoValuesOf(anc)[d.Field]; ok {
						env["ancestor_"+d.Type+"_"+d.Field] = v
					}
				}
			}
			if depErr != nil {
				break
			}
		}
		if depErr != nil {
			errs = append(errs, fmt.Errorf("derived field %q: %w", key, depErr))
			continue
		}
		v, err := ev.Eval(derived[key].Formula, env)
		if err != nil {
			errs = append(errs, fmt.Errorf("derived field %q: %w", key, err))
			continue
		}
		result[key] = v
	}
	return result, errors.Join(errs...)
}

func eoCheckDeps(deps []eoDependency) error {
	for _, d := range deps {
		if d.Field == "" && d.Scope != eoScopeChild {
			return fmt.Errorf("recompute_on %q entry needs a field", d.Scope)
		}
		switch d.Scope {
		case eoScopeOwn:
		case eoScopeChild, eoScopeAncestor:
			if d.Type == "" {
				return fmt.Errorf("recompute_on %q entry needs a type", d.Scope)
			}
			if d.Scope == eoScopeChild {
				for _, a := range d.aggs() {
					switch a {
					case "sum", "min", "max", "avg":
					default:
						return fmt.Errorf("unknown aggregate %q", a)
					}
				}
			}
		default:
			return fmt.Errorf("unknown recompute_on scope %q", d.Scope)
		}
	}
	return nil
}

func (d eoDependency) aggs() []string {
	if len(d.Aggregates) == 0 {
		return []string{"sum"}
	}
	return d.Aggregates
}

// eoSortDerived orders keys so a field follows the derived fields it lists as
// own dependencies (Kahn's algorithm, ties in input order). Keys that are in a
// cycle, or depend on one, are returned separately.
func eoSortDerived(keys []string, parsed map[string][]eoDependency, derived map[string]*models.EOFieldDef) (sorted, cyclic []string) {
	remaining := map[string]map[string]bool{}
	for _, k := range keys {
		need := map[string]bool{}
		for _, d := range parsed[k] {
			if d.Scope == eoScopeOwn {
				if _, ok := derived[d.Field]; ok {
					need[d.Field] = true
				}
			}
		}
		remaining[k] = need
	}
	done := map[string]bool{}
	for progress := true; progress; {
		progress = false
		for _, k := range keys {
			if done[k] {
				continue
			}
			ready := true
			for dep := range remaining[k] {
				if !done[dep] {
					ready = false
					break
				}
			}
			if ready {
				done[k] = true
				sorted = append(sorted, k)
				progress = true
			}
		}
	}
	for _, k := range keys {
		if !done[k] {
			cyclic = append(cyclic, k)
		}
	}
	return sorted, cyclic
}

// eoValuesOf is a record's values as a formula environment, keyed by field
// key and spanning BOTH the real eo_records columns and the values jsonb.
//
// It reads the flattened view rather than rec.Values alone because the 17
// inherited fields are columns: spec §7.5's Referral deadline is "counted
// back from the parent letter's external deadline", which reads an ancestor's
// due_date — a column. A jsonb-only environment resolves no such formula and,
// because an unresolved reference is reported rather than raised, the field
// would simply never be computed.
//
// Empty values are omitted rather than set to nil, so a formula over missing
// data yields eoformula.ErrUnresolvedReference. Date-shaped strings become
// time.Time; identifiers become their string form, which is comparable in a
// formula where a [16]byte array is not.
func eoValuesOf(rec *models.EORecord) map[string]any {
	out := map[string]any{}
	for k, v := range eoFlattenStored(rec) {
		if env := eoEnvValue(v); env != nil {
			out[k] = env
		}
	}
	return out
}

func eoEnvValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		if strings.TrimSpace(x) == "" {
			return nil
		}
		return eoCoerce(x)
	case time.Time:
		if x.IsZero() {
			return nil
		}
		return x
	case *time.Time:
		if x == nil {
			return nil
		}
		return *x
	case uuid.UUID:
		if x == uuid.Nil {
			return nil
		}
		return x.String()
	case *uuid.UUID:
		if x == nil {
			return nil
		}
		return x.String()
	case []any:
		if len(x) == 0 {
			return nil
		}
		return x
	}
	return v
}

// eoOwnEnv is the record's own values minus derived fields, whose stored
// values are stale by definition; they re-enter the env as they are computed.
func eoOwnEnv(rec *models.EORecord, derived map[string]*models.EOFieldDef) map[string]any {
	env := eoValuesOf(rec)
	for k := range derived {
		delete(env, k)
	}
	return env
}

func eoCoerce(v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return v
}

func eoAddChildAggregates(env map[string]any, kids []models.EORecord, d eoDependency) {
	env["children_"+d.Type+"_count"] = float64(len(kids))
	if d.Field == "" {
		return
	}
	var nums []float64
	for i := range kids {
		if f, ok := eoValuesOf(&kids[i])[d.Field].(float64); ok {
			nums = append(nums, f)
		}
	}
	if len(nums) == 0 {
		return
	}
	for _, a := range d.aggs() {
		var r float64
		switch a {
		case "sum", "avg":
			for _, n := range nums {
				r += n
			}
			if a == "avg" {
				r /= float64(len(nums))
			}
		case "min":
			r = math.Inf(1)
			for _, n := range nums {
				r = math.Min(r, n)
			}
		case "max":
			r = math.Inf(-1)
			for _, n := range nums {
				r = math.Max(r, n)
			}
		}
		env["children_"+d.Type+"_"+a+"_"+d.Field] = r
	}
}
