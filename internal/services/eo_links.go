package services

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
)

// eoAnyType is the literal a link type's source or target list may carry to
// mean "a record of any type". It is how the universal Task's parent_record
// link hangs off every record type (spec §7.6).
const eoAnyType = "any"

// eoMaxAncestorDepth bounds the record ancestry walk. It has the same value as
// eoMaxInheritanceDepth (which bounds record-TYPE inheritance) but is a
// separate limit.
const eoMaxAncestorDepth = 32

// eoTypeAllowed reports whether typeID is permitted by a link type's jsonb
// type list. The list is either the string "any", an array of type-id strings
// (which may itself contain "any"), or empty. An empty or malformed list
// permits nothing: a link type with no declared endpoints is not usable.
func eoTypeAllowed(raw []byte, typeID uuid.UUID) (bool, error) {
	if len(raw) == 0 {
		return false, nil
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return single == eoAnyType || single == typeID.String(), nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err != nil {
		return false, fmt.Errorf("record type list is neither a string nor an array of strings: %w", err)
	}
	for _, s := range list {
		if s == eoAnyType || s == typeID.String() {
			return true, nil
		}
	}
	return false, nil
}

// ValidateLink checks that creating a link of type lt from one record to
// another is allowed: both endpoints' record types are permitted, the pair is
// not already linked by this type, and the cardinality holds. existing is the
// set of links the caller already has for from (links of other types, or of
// other records, are ignored).
//
// Cardinality is counted per source record: "one" means from may carry at most
// one outgoing link of this type (a Task has one parent_record); "many" is
// unbounded.
func ValidateLink(lt *models.EOLinkType, from, to *models.EORecord, existing []models.EORecordLink) error {
	if lt == nil || from == nil || to == nil {
		return fmt.Errorf("validate link: link type and both records are required")
	}
	if from.ID == to.ID {
		return fmt.Errorf("link %q: a record cannot be linked to itself", lt.Key)
	}

	ok, err := eoTypeAllowed(lt.SourceTypeIDs, from.RecordTypeID)
	if err != nil {
		return fmt.Errorf("link %q source types: %w", lt.Key, err)
	}
	if !ok {
		return fmt.Errorf("link %q does not allow a source record of type %s", lt.Key, from.RecordTypeID)
	}
	ok, err = eoTypeAllowed(lt.TargetTypeIDs, to.RecordTypeID)
	if err != nil {
		return fmt.Errorf("link %q target types: %w", lt.Key, err)
	}
	if !ok {
		return fmt.Errorf("link %q does not allow a target record of type %s", lt.Key, to.RecordTypeID)
	}

	outgoing := 0
	for _, l := range existing {
		if l.LinkTypeKey != lt.Key || l.FromRecordID != from.ID {
			continue
		}
		if l.ToRecordID == to.ID {
			return fmt.Errorf("link %q already exists between these records", lt.Key)
		}
		outgoing++
	}
	if lt.Cardinality == models.EOCardinalityOne && outgoing > 0 {
		return fmt.Errorf("link %q is one-to-one: record %s already has one", lt.Key, from.ID)
	}
	return nil
}

// DeriveDeadlineFromParent returns the due date a child inherits from its
// parent under lt: the parent's due date moved back by DeadlineOffsetDays
// (a negative offset moves it later). It returns nil when the link type does
// not derive deadlines, or when there is nothing to derive from: a parent with
// no due date yields no deadline rather than a guessed one, leaving the
// child's own due date untouched.
//
// The offset counts calendar days. A working-day count would need a business
// calendar (weekend days differ by jurisdiction, holidays by year) that a
// pure function does not have; the spec asks only for an offset.
func DeriveDeadlineFromParent(lt *models.EOLinkType, parent *models.EORecord) *time.Time {
	if lt == nil || parent == nil || !lt.DeriveDeadlineFromParent || parent.DueDate == nil {
		return nil
	}
	due := parent.DueDate.AddDate(0, 0, -lt.DeadlineOffsetDays)
	return &due
}

// BlockingChildren returns the children that stop their parent moving. The two
// block semantics of spec §7.6 are distinct: with an empty transitionKey it
// answers "may the parent close?" and only links whose type sets
// BlocksParentClosure count; with a transitionKey it answers "may the parent
// take this transition?" and only links whose type names exactly that key in
// BlocksParentTransitionKey count. A child that blocks one does not block the
// other.
//
// Link direction varies by link type (Task.parent_record points child to
// parent, spawned_task points parent to child), so a link's child end is found
// by membership in children, not by position. A link counts only when exactly
// one endpoint is in children: both outside means it is not about these
// children, both inside means it relates siblings, not parent to child. A child
// that is already closed no longer blocks. Each blocking child is returned
// once, in the order given.
func BlockingChildren(children []models.EORecord, links []models.EORecordLink, linkTypes map[string]models.EOLinkType, transitionKey string) []models.EORecord {
	blocks := func(lt models.EOLinkType) bool {
		if transitionKey == "" {
			return lt.BlocksParentClosure
		}
		return lt.BlocksParentTransitionKey == transitionKey
	}

	isChild := make(map[uuid.UUID]bool, len(children))
	for _, c := range children {
		isChild[c.ID] = true
	}
	blocking := make(map[uuid.UUID]bool)
	for _, l := range links {
		fromIn, toIn := isChild[l.FromRecordID], isChild[l.ToRecordID]
		if fromIn == toIn {
			continue
		}
		lt, ok := linkTypes[l.LinkTypeKey]
		if !ok || !blocks(lt) {
			continue
		}
		if fromIn {
			blocking[l.FromRecordID] = true
		} else {
			blocking[l.ToRecordID] = true
		}
	}

	var out []models.EORecord
	for _, c := range children {
		if blocking[c.ID] && c.ClosedAt == nil {
			out = append(out, c)
		}
	}
	return out
}

// WalkAncestors returns rec's ancestors nearest-first (parent, grandparent, …),
// not including rec. load fetches a record by id; a nil record is an error.
// The walk is untrusted: a cycle or a chain deeper than maxDepth is an error,
// never a loop, because it runs inside request handlers. It uses the same
// visited-set-plus-depth-bound shape as ResolveEffectiveFields; a non-positive
// maxDepth means eoMaxAncestorDepth.
func WalkAncestors(ctx context.Context, rec *models.EORecord, load func(uuid.UUID) (*models.EORecord, error), maxDepth int) ([]models.EORecord, error) {
	if rec == nil {
		return nil, fmt.Errorf("walk ancestors: nil record")
	}
	if load == nil {
		return nil, fmt.Errorf("walk ancestors: nil loader")
	}
	if maxDepth <= 0 {
		maxDepth = eoMaxAncestorDepth
	}

	var out []models.EORecord
	visited := map[uuid.UUID]bool{rec.ID: true}
	for cur := rec; cur.ParentRecordID != nil; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pid := *cur.ParentRecordID
		if visited[pid] {
			return nil, fmt.Errorf("walk ancestors: parent cycle at record %s", pid)
		}
		if len(out) >= maxDepth {
			return nil, fmt.Errorf("walk ancestors: hierarchy deeper than %d levels", maxDepth)
		}
		parent, err := load(pid)
		if err != nil {
			return nil, fmt.Errorf("walk ancestors: load %s: %w", pid, err)
		}
		if parent == nil {
			return nil, fmt.Errorf("walk ancestors: ancestor %s of record %s not found", pid, cur.ID)
		}
		visited[pid] = true
		out = append(out, *parent)
		cur = parent
	}
	return out, nil
}
