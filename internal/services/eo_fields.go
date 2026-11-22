package services

import (
	"fmt"
	"sort"

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
