package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/internal/services/eoformula"
	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// This is the EO workflow executor. It is a NEW implementation, not a refactor
// of incidentService.ExecuteTransition (spec §7.8): that one is ~1,000 lines
// typed on *models.Incident end to end, and extracting a shared core out of it
// is a refactor of live code with no regression suite behind it. What is
// reused here is the CONFIGURATION — Workflow, WorkflowState,
// WorkflowTransition, TransitionRequirement, TransitionFieldChange — and the
// semantics the incident executor gives it, which are deliberate and are
// reproduced on purpose:
//
//   - an empty AllowedRoles means NO restriction, not "nobody"
//     (incident_service.go:3432);
//   - a requirement whose IsMandatory is nil or false is skipped entirely
//     (incident_service.go:3469) — the column defaults to true, so nil and
//     false are both "not enforced" at the executor.
//
// What is new is spec §7.15's third actor rule: the user(s) named in a FIELD
// of this record or of a named ancestor.

// Sentinel errors, so a handler can map a refusal to the right status without
// matching on message text. Field-level problems come back as
// *EOValidationError instead, exactly as the create/update pipeline returns
// them.
var (
	// ErrEOTransitionNotAvailable: the transition does not run from the
	// record's current state, is not part of the record's workflow, or is
	// inactive.
	ErrEOTransitionNotAvailable = errors.New("transition is not available from this state")
	// ErrEOTransitionNotPermitted: this actor may not run this transition.
	ErrEOTransitionNotPermitted = errors.New("actor may not run this transition")
	// ErrEOTransitionBlocked: an open child record blocks it (spec §7.6).
	ErrEOTransitionBlocked = errors.New("transition is blocked by an open child record")
)

// Where a TransitionActorRule reads its field from.
const (
	EOActorFromSelf   = "self"
	EOActorFromParent = "parent"
	EOActorFromRoot   = "root"
)

// TransitionActorRule permits the user (or users) named in field FieldKey of
// this record, or of a named ancestor, to run the transition — spec §7.15's
// third actor rule, beside the reused AllowedRoles and RequireAssignee.
//
// The matrix needs it because a Committee's chair, secretary and members are
// fields on that record and a Referral's owner is a field on that referral:
// only the chair may approve THAT committee's agenda. A global role cannot
// express "that record's chair".
//
// From is self, parent (the immediate parent record) or root (the topmost
// ancestor). It is stored as a jsonb list on WorkflowTransition.EOActorRules.
type TransitionActorRule struct {
	FieldKey string `json:"field_key"`
	From     string `json:"from"`
}

// ParseTransitionActorRules decodes a transition's actor rules. An absent or
// empty list means the rule is not in play — like an empty AllowedRoles, it
// restricts nobody. An unreadable list, a blank field key or an unknown From
// is a configuration error and refuses the transition rather than opening it:
// a governance control that cannot be read must not silently become "anyone".
//
// From defaults to self when omitted, which is the common case in the matrix.
func ParseTransitionActorRules(raw datatypes.JSON) ([]TransitionActorRule, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var rules []TransitionActorRule
	if err := json.Unmarshal(raw, &rules); err != nil {
		return nil, fmt.Errorf("transition actor rules are not a list of {field_key, from}: %w", err)
	}
	for i := range rules {
		rules[i].FieldKey = strings.TrimSpace(rules[i].FieldKey)
		if rules[i].FieldKey == "" {
			return nil, fmt.Errorf("transition actor rule %d has no field_key", i)
		}
		if rules[i].From == "" {
			rules[i].From = EOActorFromSelf
		}
		switch rules[i].From {
		case EOActorFromSelf, EOActorFromParent, EOActorFromRoot:
		default:
			return nil, fmt.Errorf("transition actor rule %d: unknown from %q, want %s, %s or %s",
				i, rules[i].From, EOActorFromSelf, EOActorFromParent, EOActorFromRoot)
		}
	}
	return rules, nil
}

// EOTransitionActionDispatcher is the post-commit seam Task 15's action
// executor plugs into. It is called once, AFTER the transaction has committed,
// and its error never fails the transition — the state change is already
// durable and a failed notification must not undo it. A nil dispatcher means
// no actions are dispatched.
type EOTransitionActionDispatcher interface {
	ExecuteActions(ctx context.Context, rec *models.EORecord, transition *models.WorkflowTransition, performedBy *models.User) error
}

// EOTransitionService runs the configured workflow of a record type.
type EOTransitionService interface {
	// AvailableTransitions lists the transitions out of the record's current
	// state that this actor is permitted to run. It answers "who may act",
	// not "would it succeed": requirements, field values and blocking
	// children are evaluated by Execute, against the input the actor supplies.
	AvailableTransitions(ctx context.Context, rec *models.EORecord, actor *models.User) ([]models.WorkflowTransition, error)
	Execute(ctx context.Context, rec *models.EORecord, transitionID uuid.UUID, actor *models.User, input models.EOTransitionInput) (*models.EORecord, error)
}

type eoTransitionService struct {
	records   repository.EORecordRepository
	types     repository.EORecordTypeRepository
	workflows repository.WorkflowRepository
	eval      FormulaEvaluator
	actions   EOTransitionActionDispatcher
}

// NewEOTransitionService builds the executor. actions may be nil until Task 15
// wires the EO action executor in.
func NewEOTransitionService(
	records repository.EORecordRepository,
	types repository.EORecordTypeRepository,
	workflows repository.WorkflowRepository,
	actions EOTransitionActionDispatcher,
) EOTransitionService {
	return &eoTransitionService{
		records:   records,
		types:     types,
		workflows: workflows,
		eval:      eoformula.New(0),
		actions:   actions,
	}
}

func (s *eoTransitionService) AvailableTransitions(ctx context.Context, rec *models.EORecord, actor *models.User) ([]models.WorkflowTransition, error) {
	if rec == nil {
		return nil, errors.New("available transitions: no record")
	}
	if actor == nil {
		return nil, errors.New("available transitions: no actor")
	}
	if rec.StateID == nil {
		return nil, fmt.Errorf("available transitions: record %s is in no state", rec.ID)
	}
	wf, err := ResolveWorkflowForType(ctx, rec.RecordTypeID, s.workflows)
	if err != nil {
		return nil, fmt.Errorf("available transitions for record %s: %w", rec.ID, err)
	}

	var out []models.WorkflowTransition
	for _, candidate := range wf.Transitions {
		if !candidate.IsActive || candidate.FromStateID != *rec.StateID {
			continue
		}
		// The workflow's preloaded transitions carry no AllowedRoles, so the
		// permission check needs the relations. A state has a handful of
		// transitions, so this is a handful of reads, not a scan.
		tr, err := s.workflows.FindTransitionByIDWithRelations(ctx, candidate.ID)
		if err != nil {
			return nil, fmt.Errorf("available transitions: load transition %s: %w", candidate.ID, err)
		}
		if tr == nil {
			return nil, fmt.Errorf("available transitions: transition %s of workflow %q no longer exists", candidate.ID, wf.Code)
		}
		switch err := s.checkActor(ctx, s.records, rec, tr, actor); {
		case err == nil:
			out = append(out, *tr)
		case errors.Is(err, ErrEOTransitionNotPermitted):
			// Not for this actor; not an error.
		default:
			return nil, err
		}
	}
	return out, nil
}

// Execute runs one transition. The order is the governance order: load the
// transition, check the state, check who may act, check the requirements,
// check the blocking children, apply the field changes, then write the new
// state and the history row.
//
// Everything up to and including the write happens inside ONE transaction,
// which rolls back on every failure branch. The record is re-read inside it so
// the state the checks run against is the state that is written.
//
// Actions are dispatched after the commit, fire and forget (Task 15).
func (s *eoTransitionService) Execute(ctx context.Context, rec *models.EORecord, transitionID uuid.UUID, actor *models.User, input models.EOTransitionInput) (*models.EORecord, error) {
	if rec == nil {
		return nil, errors.New("execute transition: no record")
	}
	if actor == nil {
		return nil, errors.New("execute transition: no actor, and every transition records who ran it")
	}

	transition, err := s.workflows.FindTransitionByIDWithRelations(ctx, transitionID)
	if err != nil {
		return nil, fmt.Errorf("execute transition %s: %w", transitionID, err)
	}
	if transition == nil {
		return nil, fmt.Errorf("execute transition %s: no such transition", transitionID)
	}

	wf, err := ResolveWorkflowForType(ctx, rec.RecordTypeID, s.workflows)
	if err != nil {
		return nil, fmt.Errorf("execute transition on record %s: %w", rec.ID, err)
	}
	if transition.WorkflowID != wf.ID {
		return nil, fmt.Errorf("execute transition %q on record %s: %w: it belongs to workflow %s, not to %q",
			transition.Code, rec.ID, ErrEOTransitionNotAvailable, transition.WorkflowID, wf.Code)
	}
	if !transition.IsActive {
		return nil, fmt.Errorf("execute transition %q on record %s: %w: it is not active",
			transition.Code, rec.ID, ErrEOTransitionNotAvailable)
	}
	toState := eoStateByID(wf, transition.ToStateID)
	if toState == nil {
		return nil, fmt.Errorf("execute transition %q: target state %s is not a state of workflow %q",
			transition.Code, transition.ToStateID, wf.Code)
	}

	var updated *models.EORecord
	err = s.records.Transaction(ctx, func(r repository.EORecordRepository) error {
		// The row lock is the FIRST statement in the transaction: a
		// transaction alone does not serialise two transitions under READ
		// COMMITTED, and a doubly-executed transition would dispatch its
		// post-commit actions twice.
		fresh, err := r.LockForUpdate(ctx, rec.ID, rec.RecordTypeID)
		if err != nil {
			return err
		}
		if fresh == nil {
			return fmt.Errorf("record %s of type %s: %w", rec.ID, rec.RecordTypeID, ErrEORecordNotFound)
		}

		// 1. the state the transition runs from.
		if fresh.StateID == nil || *fresh.StateID != transition.FromStateID {
			return fmt.Errorf("transition %q on record %s: %w: it runs from state %s, the record is in %v",
				transition.Code, fresh.Reference, ErrEOTransitionNotAvailable, transition.FromStateID, fresh.StateID)
		}

		// 2. who may act: global roles, the assignee, and the record-scoped
		// rules of §7.15. The three are ANDed, as AllowedRoles and
		// RequireAssignee already are on the incident path: a transition that
		// names both a role and a chair means the chair, acting in that role.
		if err := s.checkActor(ctx, r, fresh, transition, actor); err != nil {
			return err
		}

		// 3. the requirements the transition carries.
		if err := eoCheckRequirements(transition, input); err != nil {
			return err
		}

		// 4. open children that block this move (spec §7.6).
		if err := s.checkBlockingChildren(ctx, r, fresh, transition, toState); err != nil {
			return err
		}

		// 5. the field changes the transition declares, validated against the
		// TARGET state so that "required at closure" is asked of the state
		// being entered.
		fields, err := s.effectiveFields(ctx, fresh.RecordTypeID)
		if err != nil {
			return err
		}
		before := eoFlattenStored(fresh)

		// Assignment resolution comes before the field changes, so an
		// assigned_user_id the transition DECLARES as a field change beats
		// the static assignment it carries — the order the incident path
		// uses, and the one that makes the actor's own choice win.
		if err := eoApplyTransitionAssignment(fresh, transition); err != nil {
			return err
		}

		// `before`, not the record as the assignment has just mutated it, is
		// the baseline the field rules compare against: the locked-field
		// check asks "is this value different from the stored one", and a
		// baseline that already holds the static assignment inverts it —
		// the value the transition just wrote would look unchanged and pass
		// the lock, while the record's genuine assignee would look like an
		// edit and be refused.
		if err := s.applyFieldChanges(ctx, r, fields, before, fresh, transition, input); err != nil {
			return err
		}

		// 6. the new state. A terminal state closes the record; leaving one
		// re-opens it, so closed_at is cleared rather than left stale.
		fresh.StateID = &transition.ToStateID
		if IsTerminal(toState) {
			now := time.Now()
			fresh.ClosedAt = &now
		} else {
			fresh.ClosedAt = nil
		}

		// 7. the derived fields, over the values and the state written
		// above. A transition field change is a declared dependency
		// changing (spec §7.5), so a roll-up must not go stale at the exact
		// moment the record moves. It runs AFTER the state and closed_at are
		// set, so a formula reading them sees the new ones, and BEFORE the
		// write, so the row the database receives is already current.
		//
		// The dependency loader is built on the TRANSACTION's repository, not
		// on the service's. A child or ancestor read taken on the pooled one
		// would acquire a second connection while this transaction holds its
		// own plus a FOR UPDATE row lock, and database/sql waits for a free
		// connection indefinitely unless the context carries a deadline —
		// lock_timeout does not bound that. It would also put those reads
		// outside the transaction. (The save pipeline recomputes BEFORE
		// opening its transaction, so it is no precedent for doing otherwise.)
		eoApplyDerived(ctx, fields, fresh, &eoRecordDeps{records: r, types: s.types})

		// 8. the record and its journal entry are one write.
		if err := r.Update(ctx, fresh); err != nil {
			return err
		}
		oldValues, newValues := eoDiffValues(before, eoFlattenStored(fresh))
		oldJSON, err := json.Marshal(oldValues)
		if err != nil {
			return fmt.Errorf("encode old values: %w", err)
		}
		newJSON, err := json.Marshal(newValues)
		if err != nil {
			return fmt.Errorf("encode new values: %w", err)
		}
		if err := r.CreateHistory(ctx, &models.EORecordHistory{
			RecordID:      fresh.ID,
			RecordTypeID:  fresh.RecordTypeID,
			TransitionID:  &transition.ID,
			FromStateID:   &transition.FromStateID,
			ToStateID:     &transition.ToStateID,
			PerformedByID: &actor.ID,
			Comment:       strings.TrimSpace(input.Comment),
			OldValues:     oldJSON,
			NewValues:     newJSON,
		}); err != nil {
			return err
		}
		updated = fresh
		return nil
	})
	if err != nil {
		return nil, err
	}

	// The commit has happened. Actions run outside it, and their failure is
	// logged rather than returned: the state change is durable and a
	// notification that did not go out must not undo it.
	if s.actions != nil {
		if err := s.actions.ExecuteActions(ctx, updated, transition, actor); err != nil {
			log.Printf("eo: record %s: actions of transition %q failed after commit: %v", updated.Reference, transition.Code, err)
		}
	}
	return updated, nil
}

// ── who may act ─────────────────────────────────────────────────────────────

// checkActor applies the three actor rules. It returns ErrEOTransitionNotPermitted
// when the actor simply may not act, and a plain error when the configuration
// cannot be read.
func (s *eoTransitionService) checkActor(ctx context.Context, r repository.EORecordRepository, rec *models.EORecord, transition *models.WorkflowTransition, actor *models.User) error {
	// An EMPTY AllowedRoles means no restriction, not "nobody"
	// (incident_service.go:3432).
	if len(transition.AllowedRoles) > 0 && !eoActorHasOneOf(actor, transition.AllowedRoles) {
		return fmt.Errorf("transition %q: %w: it is restricted to other roles", transition.Code, ErrEOTransitionNotPermitted)
	}

	if transition.RequireAssignee && (rec.AssignedUserID == nil || *rec.AssignedUserID != actor.ID) {
		return fmt.Errorf("transition %q: %w: only the assigned user may run it", transition.Code, ErrEOTransitionNotPermitted)
	}

	rules, err := ParseTransitionActorRules(transition.EOActorRules)
	if err != nil {
		return fmt.Errorf("transition %q: %w", transition.Code, err)
	}
	if len(rules) == 0 {
		return nil
	}
	ok, err := s.actorSatisfiesRules(ctx, r, rec, rules, actor)
	if err != nil {
		return fmt.Errorf("transition %q: %w", transition.Code, err)
	}
	if !ok {
		return fmt.Errorf("transition %q: %w: it is reserved to the user(s) named on the record", transition.Code, ErrEOTransitionNotPermitted)
	}
	return nil
}

func eoActorHasOneOf(actor *models.User, allowed []models.Role) bool {
	for _, role := range allowed {
		for _, held := range actor.Roles {
			if role.ID == held.ID {
				return true
			}
		}
	}
	return false
}

// actorSatisfiesRules answers whether the actor is named by ANY of the rules.
// Several rules are an OR — "the chair or the secretary may approve" — because
// one user cannot hold two of the record's roles at once and a conjunction
// would make a second rule unsatisfiable.
//
// A rule whose field is empty, or whose ancestor does not exist, is NOT
// satisfied. Unlike an empty AllowedRoles, an empty field means nobody has
// been named for the job, so the transition waits rather than opening to
// everyone.
func (s *eoTransitionService) actorSatisfiesRules(ctx context.Context, r repository.EORecordRepository, rec *models.EORecord, rules []TransitionActorRule, actor *models.User) (bool, error) {
	var ancestors []models.EORecord
	ancestorsLoaded := false

	for _, rule := range rules {
		source := rec
		if rule.From != EOActorFromSelf {
			if !ancestorsLoaded {
				var err error
				ancestors, err = WalkAncestors(ctx, rec, func(id uuid.UUID) (*models.EORecord, error) {
					return r.GetAnyByID(ctx, id)
				}, 0)
				if err != nil {
					return false, err
				}
				ancestorsLoaded = true
			}
			if len(ancestors) == 0 {
				continue
			}
			switch rule.From {
			case EOActorFromParent:
				source = &ancestors[0]
			case EOActorFromRoot:
				source = &ancestors[len(ancestors)-1]
			default:
				return false, fmt.Errorf("actor rule: unknown from %q", rule.From)
			}
		}
		if eoFieldNamesUser(eoFlattenStored(source)[rule.FieldKey], actor.ID) {
			return true, nil
		}
	}
	return false, nil
}

// eoFieldNamesUser reports whether a stored field value names this user.
//
// It spans the shapes eoFlattenStored produces: a *uuid.UUID or uuid.UUID from
// a real column (assigned_user_id, created_by_id), a string from the values
// jsonb, and a []any for a multi_user field — a Committee's members are ONE
// field holding many users, and every one of them is named by it, so a
// multi-user field satisfies the rule for each of its members.
func eoFieldNamesUser(v any, id uuid.UUID) bool {
	switch x := v.(type) {
	case nil:
		return false
	case string:
		parsed, err := uuid.Parse(strings.TrimSpace(x))
		return err == nil && parsed == id
	case uuid.UUID:
		return x == id
	case *uuid.UUID:
		return x != nil && *x == id
	case []any:
		for _, e := range x {
			if eoFieldNamesUser(e, id) {
				return true
			}
		}
	}
	return false
}

// ── assignment ──────────────────────────────────────────────────────────────

// eoApplyTransitionAssignment resolves the assignment the transition carries
// (spec §7.8 lists assignment resolution as part of this path). The STATIC
// half is implemented: AssignUserID names the user and AssignDepartmentID the
// owning body. A transition carrying neither leaves the record's own alone;
// it is not a way to clear an assignee.
//
// ManualSelectUser needs no engine code — a transition that lets the actor
// choose declares assigned_user_id as a field change, so the choice goes
// through the field rules like any other value, and because field changes are
// applied after this, it wins.
//
// AutoDetectDepartment and AutoMatchUser are REFUSED rather than ignored.
//
// Refusing, not implementing, is the deliberate part. AutoDetectDepartment
// matches on an incident's classification and location, which EO records
// genuinely do not have. AutoMatchUser is the subtler case: its role-only
// fallback (incident_service.go:3753 matches on assignment roles alone) needs
// nothing an EO record lacks, so it COULD be implemented — but it wants a
// user repository this service does not have, and that wiring belongs with
// the seeding work, not here.
//
// What is not acceptable either way is silence. A transition seeded with
// AutoMatchUser that assigns nobody and says nothing is the same silent no-op
// as a mandatory requirement the engine never evaluates, so it fails the same
// way: loudly, naming the flag.
func eoApplyTransitionAssignment(rec *models.EORecord, transition *models.WorkflowTransition) error {
	if transition.AutoMatchUser {
		return fmt.Errorf("transition %q sets auto_match_user, which the EO engine does not resolve; name the user with assign_user_id, or declare assigned_user_id as a field change",
			transition.Code)
	}
	if transition.AutoDetectDepartment {
		return fmt.Errorf("transition %q sets auto_detect_department, which matches on an incident's classification and location and has no EO equivalent; name the body with assign_department_id",
			transition.Code)
	}
	if transition.AssignUserID != nil {
		id := *transition.AssignUserID
		rec.AssignedUserID = &id
	}
	if transition.AssignDepartmentID != nil {
		id := *transition.AssignDepartmentID
		rec.OwningBodyID = &id
	}
	return nil
}

// ── requirements ────────────────────────────────────────────────────────────

// eoCheckRequirements enforces the transition's requirements.
//
// A requirement whose IsMandatory is nil or false is skipped entirely,
// matching incident_service.go:3469: the column defaults to true, so nil is
// "the configuration never said" and false is "do not enforce".
//
// The EO engine evaluates "comment" and "attachment". It does NOT evaluate
// "feedback" (an incident's citizen satisfaction survey — EO records have no
// such audience), "field_value" (the field matrix expresses per-state
// requiredness on the field definition, which ValidateRecordValues already
// enforces against the target state) or "rating". Where the incident executor
// lets an unhandled type fall through its switch and pass, this one REFUSES a
// mandatory requirement it cannot evaluate: a governance control the engine
// does not understand must not silently become no control at all.
func eoCheckRequirements(transition *models.WorkflowTransition, input models.EOTransitionInput) error {
	for _, req := range transition.Requirements {
		if req.IsMandatory == nil || !*req.IsMandatory {
			continue
		}
		switch req.RequirementType {
		case "comment":
			if strings.TrimSpace(input.Comment) == "" {
				return eoRequirementError(req, "a comment is required for this transition")
			}
		case "attachment":
			// NOTE: attachments are checked for PRESENCE and then dropped.
			// Nothing here stores them and no eo_record_attachments table
			// exists: spec §7.12 puts all file handling in Documenta, and
			// the attachments field itself lives in the values bag, so
			// persisting them belongs with the Documenta integration (a
			// later plan). Until then input.Attachments satisfies a
			// requirement and goes no further — it is not an upload path.
			if len(input.Attachments) == 0 {
				return eoRequirementError(req, "an attachment is required for this transition")
			}
			if (req.IsMultiple == nil || !*req.IsMultiple) && len(input.Attachments) > 1 {
				return errors.New("only one attachment is allowed for this transition")
			}
		default:
			return fmt.Errorf("transition %q requires %q, which the EO engine does not evaluate; configure it as a field requirement instead",
				transition.Code, req.RequirementType)
		}
	}
	return nil
}

// eoRequirementError prefers the message the configuration carries, so a
// bilingual or process-specific wording reaches the actor unchanged.
func eoRequirementError(req models.TransitionRequirement, fallback string) error {
	if msg := strings.TrimSpace(req.ErrorMessage); msg != "" {
		return errors.New(msg)
	}
	return errors.New(fallback)
}

// ── blocking children ───────────────────────────────────────────────────────

// checkBlockingChildren applies both of spec §7.6's block semantics, which are
// NOT interchangeable:
//
//   - the named-transition question (blocks_parent_transition_key): EO-DRF's
//     open Objection blocks the raise to H.E., and nothing else;
//   - the closure question (blocks_parent_closure): EO-DIR's open Assignment
//     blocks the parent CLOSING, whichever transition closes it.
//
// So a move into a terminal state is asked BOTH questions: its own named one,
// and the closure one. A record with an open blocking child cannot reach a
// terminal state, which is the whole point of the control.
//
// Only DIRECT children are passed to BlockingChildren: it identifies the child
// end of a link by membership in the slice and ignores links whose endpoints
// are both inside it, so a transitive descendant set would silently stop
// blocking.
func (s *eoTransitionService) checkBlockingChildren(ctx context.Context, r repository.EORecordRepository, rec *models.EORecord, transition *models.WorkflowTransition, toState *models.WorkflowState) error {
	children, err := r.Children(ctx, rec.ID, nil)
	if err != nil {
		return err
	}
	if len(children) == 0 {
		return nil
	}

	// A link's direction varies by link type (Task.parent_record points child
	// to parent, spawned_task points parent to child), so both ends are read.
	links, err := r.LinksFrom(ctx, rec.ID)
	if err != nil {
		return err
	}
	seen := make(map[uuid.UUID]bool, len(links))
	for _, l := range links {
		seen[l.ID] = true
	}
	for _, c := range children {
		fromChild, err := r.LinksFrom(ctx, c.ID)
		if err != nil {
			return err
		}
		for _, l := range fromChild {
			if !seen[l.ID] {
				seen[l.ID] = true
				links = append(links, l)
			}
		}
	}

	linkTypes := make(map[string]models.EOLinkType, len(links))
	for _, l := range links {
		if _, ok := linkTypes[l.LinkTypeKey]; ok {
			continue
		}
		lt, err := s.types.GetLinkTypeByKey(ctx, l.LinkTypeKey)
		if err != nil {
			return err
		}
		if lt != nil {
			linkTypes[l.LinkTypeKey] = *lt
		}
	}

	// An empty transition key asks the CLOSURE question, so the named check
	// is only run for a transition that has a code to be named by.
	if transition.Code != "" {
		if blocked := BlockingChildren(children, links, linkTypes, transition.Code); len(blocked) > 0 {
			return eoBlockedError(transition.Code, blocked)
		}
	}
	if IsTerminal(toState) {
		if blocked := BlockingChildren(children, links, linkTypes, ""); len(blocked) > 0 {
			return eoBlockedError(transition.Code, blocked)
		}
	}
	return nil
}

func eoBlockedError(code string, blocked []models.EORecord) error {
	refs := make([]string, 0, len(blocked))
	for _, b := range blocked {
		refs = append(refs, b.Reference)
	}
	return fmt.Errorf("transition %q: %w: %s", code, ErrEOTransitionBlocked, strings.Join(refs, ", "))
}

// ── field changes ───────────────────────────────────────────────────────────

// applyFieldChanges writes the values the actor supplied for the fields this
// transition declares.
//
// Only declared keys may be written: a transition modal is the form its
// configuration describes, and accepting any key would make the transition a
// second edit path around the field rules — in particular around a field that
// is locked after a state the record has already entered.
//
// Validation runs against the TARGET state, because required_at_state asks
// about the state being entered, and against the states the record has EVER
// entered, read from eo_record_history: is_locked_after asks a question only
// the history can answer, since EO workflows have return and exception paths.
// stored is the record's values as they were BEFORE this transition touched
// anything. It is passed in rather than re-flattened from rec, because by the
// time this runs the static assignment has already been applied and rec is no
// longer the baseline the field rules must compare against.
func (s *eoTransitionService) applyFieldChanges(
	ctx context.Context,
	r repository.EORecordRepository,
	fields []models.EOFieldDef,
	stored map[string]any,
	rec *models.EORecord,
	transition *models.WorkflowTransition,
	input models.EOTransitionInput,
) error {
	declared := make(map[string]models.TransitionFieldChange, len(transition.FieldChanges))
	for _, fc := range transition.FieldChanges {
		declared[fc.FieldName] = fc
	}

	errs := map[string]string{}
	for key := range input.Values {
		if _, ok := declared[key]; !ok {
			errs[key] = "is not a field this transition may change"
		}
	}
	for name, fc := range declared {
		if !fc.IsRequired {
			continue
		}
		if v, ok := input.Values[name]; !ok || eoIsEmpty(v) {
			label := fc.Label
			if label == "" {
				label = name
			}
			errs[name] = label + " is required for this transition"
		}
	}
	if err := eoValidationError(errs); err != nil {
		return err
	}

	entered, err := r.EnteredStateIDs(ctx, rec.ID, rec.RecordTypeID)
	if err != nil {
		return err
	}
	fieldErrs, err := ValidateRecordValues(fields, stored, input.Values, &transition.ToStateID, entered, s.eval)
	if err != nil {
		return err
	}
	if err := eoValidationError(fieldErrs); err != nil {
		return err
	}
	return eoValidationError(eoApplyValues(rec, input.Values))
}

// effectiveFields resolves a record type's own fields plus every ancestor's.
func (s *eoTransitionService) effectiveFields(ctx context.Context, typeID uuid.UUID) ([]models.EOFieldDef, error) {
	t, err := s.types.GetByID(ctx, typeID)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, fmt.Errorf("no record type %s", typeID)
	}
	return eoEffectiveFields(ctx, s.types, t)
}

// eoStateByID finds a state in the workflow that owns it. Reading the state
// from the workflow rather than from transition.ToState also proves the
// transition's target belongs to this record's workflow.
func eoStateByID(wf *models.Workflow, id uuid.UUID) *models.WorkflowState {
	for i := range wf.States {
		if wf.States[i].ID == id {
			return &wf.States[i]
		}
	}
	return nil
}
