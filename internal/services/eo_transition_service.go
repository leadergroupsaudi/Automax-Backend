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
	"gorm.io/gorm"
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
	caps      eoCapabilityResolver
	// policies loads the escalation policy of a state being entered. Nil means
	// SLA clocks are not wired: no timer is scheduled or cancelled.
	policies repository.EscalationPolicyRepository
}

// NewEOTransitionService builds the executor. actions may be nil until Task 15
// wires the EO action executor in.
func NewEOTransitionService(
	records repository.EORecordRepository,
	types repository.EORecordTypeRepository,
	workflows repository.WorkflowRepository,
	actions EOTransitionActionDispatcher,
) EOTransitionService {
	return NewEOTransitionServiceWithSLA(records, types, workflows, actions, nil)
}

// NewEOTransitionServiceWithSLA is NewEOTransitionService plus SLA clocks: a
// transition cancels the record's escalation timers (it has left the state they
// belong to) and starts those of the state it enters, in the transition's own
// transaction. policies loads the entered state's escalation policy.
func NewEOTransitionServiceWithSLA(
	records repository.EORecordRepository,
	types repository.EORecordTypeRepository,
	workflows repository.WorkflowRepository,
	actions EOTransitionActionDispatcher,
	policies repository.EscalationPolicyRepository,
) EOTransitionService {
	return &eoTransitionService{
		records:   records,
		types:     types,
		workflows: workflows,
		eval:      eoformula.New(0),
		actions:   actions,
		policies:  policies,
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

	// Capabilities are resolved BEFORE the transaction: that needs a type read,
	// and a pooled read taken while the transaction holds its own connection
	// and the row lock can wait forever for a free one. The hooks themselves
	// receive no repository at all.
	recType, err := s.types.GetByID(ctx, rec.RecordTypeID)
	if err != nil {
		return nil, fmt.Errorf("execute transition %q on record %s: %w", transition.Code, rec.ID, err)
	}
	if recType == nil {
		return nil, fmt.Errorf("execute transition %q on record %s: no record type %s", transition.Code, rec.ID, rec.RecordTypeID)
	}
	// The effective fields are read here for the same reason: field
	// definitions cannot change mid-transition, and a pooled read inside the
	// transaction waits forever for a connection if the pool is capped.
	fields, err := eoEffectiveFields(ctx, s.types, recType)
	if err != nil {
		return nil, fmt.Errorf("execute transition %q on record %s: %w", transition.Code, rec.ID, err)
	}
	caps, err := s.caps.resolve(recType)
	if err != nil {
		return nil, fmt.Errorf("execute transition %q on record %s: %w", transition.Code, rec.ID, err)
	}
	if err := eoCapabilitiesCheckConfig(caps, fields, wf, transition); err != nil {
		return nil, fmt.Errorf("execute transition %q on record %s: %w", transition.Code, rec.ID, err)
	}
	// The entered state's escalation policy is read here for the same reason:
	// the state is fixed by the transition, and a pooled read inside the
	// transaction waits forever for a connection if the pool is capped.
	slaState, err := eoStateForSLA(ctx, s.policies, toState)
	if err != nil {
		return nil, fmt.Errorf("execute transition %q on record %s: %w", transition.Code, rec.ID, err)
	}
	ctx = WithEOActor(ctx, actor)

	var updated *models.EORecord
	err = s.records.Transaction(ctx, func(r repository.EORecordRepository, rt repository.EORecordTypeRepository, timers repository.EOTimerRepository) error {
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
		if err := s.checkBlockingChildren(ctx, r, rt, fresh, transition, toState); err != nil {
			return err
		}

		// 5. a close_parent action on this transition must have a parent to
		// close. It runs AFTER the commit and nothing retries it, so this is
		// the last moment at which the outcome can still be prevented rather
		// than merely reported.
		if err := eoCheckCloseParentHasAParent(ctx, r, fresh, transition); err != nil {
			return err
		}

		// 6. the field changes the transition declares, validated against the
		// TARGET state so that "required at closure" is asked of the state
		// being entered.
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
		// Every state the record has ever entered, read ONCE: the locked-field
		// rule is asked twice from here — of the values the actor submitted,
		// and of the attachments the engine files — and the two must be
		// answered from the same history.
		entered, err := r.EnteredStateIDs(ctx, fresh.ID, fresh.RecordTypeID)
		if err != nil {
			return err
		}
		if err := s.applyFieldChanges(fields, before, fresh, transition, input, entered); err != nil {
			return err
		}

		// 6a. the attachments the actor supplied are FILED on the record.
		// After the field changes, so a transition that declares
		// `attachments` as a field change of its own has already written
		// what it writes and these are added to it; before the capabilities
		// and the derived step, so both see the record's whole file.
		if err := eoFileAttachments(fields, fresh, entered, transition.Code, input.Attachments); err != nil {
			return err
		}

		// 6b. the capabilities: their save-time checks over the record with the
		// field changes applied, then their transition hooks, which may write
		// engine-owned values (the approval stamp). Both run before the derived
		// step so a formula can read what they wrote, and an error from either
		// rolls the transition back.
		// The capability hooks' read path is built on the TRANSACTION's
		// handles, for the reason the dependency loader below is: a read
		// taken on the pooled ones would acquire a second connection while
		// this transaction holds its own plus a FOR UPDATE row lock, and
		// database/sql waits for a free one forever on a capped pool.
		capRead := eoNewCapabilityReader(r, rt)
		capErrs, err := eoCapabilitiesValidate(ctx, capRead, caps, fresh)
		if err != nil {
			return err
		}
		if err := eoValidationError(capErrs); err != nil {
			return err
		}
		for _, c := range caps {
			if err := c.OnTransition(ctx, capRead, fresh, transition, c.Config); err != nil {
				return err
			}
		}

		// 7. the new state. A terminal state closes the record; leaving one
		// re-opens it, so closed_at is cleared rather than left stale.
		fresh.StateID = &transition.ToStateID
		if IsTerminal(toState) {
			now := time.Now()
			fresh.ClosedAt = &now
		} else {
			fresh.ClosedAt = nil
		}

		// 8. the derived fields, over the values and the state written
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
		eoApplyDerived(ctx, fields, fresh, &eoRecordDeps{records: r, types: rt})
		if err := eoCapabilitiesDerive(ctx, capRead, caps, fresh); err != nil {
			return err
		}

		// 9. the record and its journal entry are one write.
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

		// 10. the SLA clock. The record has left its old state, so that state's
		// escalation timers stop; the state it entered starts its own. Both go
		// through the transaction's timer handle, so a transition that rolls
		// back leaves neither a cancelled clock nor a stray one behind.
		if s.policies != nil {
			if err := EOCancelStateSLA(ctx, fresh, timers); err != nil {
				return err
			}
			if slaState != nil {
				if err := ScheduleStateSLA(ctx, fresh, slaState, timers); err != nil {
					return err
				}
			}
		}

		// 11. capability timers. Each enabled capability re-declares its
		// timers for the state the record has just entered. The old ones are
		// cancelled FIRST: a capability may legitimately re-arm a timer for the
		// state it is entering, and cancelling after scheduling would destroy
		// the timer just created. Both go through the transaction's timer
		// handle (the closure's `timers`, never a field on the service), so a
		// rollback leaves neither a cancelled clock nor a stray one, and no
		// second pooled connection is taken while this transaction holds its
		// own plus a row lock.
		for _, c := range caps {
			key := c.Key()
			if err := timers.CancelForRecordByCapability(ctx, fresh.ID, key); err != nil {
				return err
			}
			if err := eoArmCapabilityTimers(ctx, capRead, c, fresh, timers); err != nil {
				return err
			}
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
			// The ids are counted here and FILED by eoFileAttachments after
			// the field changes are applied — this is the presence check, not
			// the end of the road. Blank entries are not attachments: a list
			// holding one empty string must not satisfy a mandatory
			// requirement, so the count is over the ids that survive
			// eoAttachmentIDs.
			ids := eoAttachmentIDs(input.Attachments)
			if len(ids) == 0 {
				return eoRequirementError(req, "an attachment is required for this transition")
			}
			if (req.IsMultiple == nil || !*req.IsMultiple) && len(ids) > 1 {
				return errors.New("only one attachment is allowed for this transition")
			}
		default:
			return fmt.Errorf("transition %q requires %q, which the EO engine does not evaluate; configure it as a field requirement instead",
				transition.Code, req.RequirementType)
		}
	}
	return nil
}

// ── close_parent needs a parent, and the time to say so is BEFORE the move ──

// ErrEOCloseParentHasNoParent is the refusal: the transition carries a
// close_parent action and the record carries no link of the kind that action
// names.
//
// THE OUTCOME IT REMOVES. close_parent runs AFTER the commit, and nothing
// retries it. Decision Record, Outgoing Reply and Task take their own
// reference sequences, so nothing anywhere requires them to have a parent
// (docs/superpowers/eo-plan1-carry-forward.md, "Nothing refuses a parentless
// Decision Record, Outgoing Reply or Task"). Compose the two and a clerk who
// issues a Decision Record they never linked gets: the issue succeeds, the
// record reaches its terminal state and looks finished, close_parent returns
// "carries no link", and the Draft Decision Study it was meant to close stays
// open FOR EVER. Nor can the executor's parent-side failure note stand in for
// the retry here, because there is no parent record to write it on — the
// engine does not know which study was meant. One log line is the whole trace.
//
// WHY REFUSE RATHER THAN REPORT. Everything else this transition does is
// recoverable by hand; the open parent is the one thing nobody is looking at.
// Refusing costs the clerk one correction with the remedy in the message —
// link the record to its parent and issue it again — and the pilot's matrix
// marks `parent_ticket` mandatory on every sub type, so a parentless child at
// its closing transition is a record that should not exist. It is also the
// only answer available before the commit: once the child is closed, every
// cheaper option is a note on a record that already looks finished.
//
// WHAT IT DOES NOT DO. It does not require a parent of every record, only of
// one about to run a transition that closes a parent, and it checks the link
// the ACTION names rather than parent_record_id. The create-path rule that
// would refuse a parentless child outright is still the carry-forward item it
// was; this closes the composition, not the gap underneath it.
var ErrEOCloseParentHasNoParent = errors.New("this transition closes the record's parent and the record has no parent to close")

// eoCheckCloseParentHasAParent reads the transition's own close_parent
// actions and refuses when the record carries no link of the kind one names.
//
// A config this cannot read is also a refusal. The action would fail after
// the commit for exactly the same reason, and a transition that cannot run
// its closing action must not pretend it did.
//
// IT ASKS EXACTLY WHAT closeParent ASKS, IN THE SAME SPELLINGS, because a
// check that is merely similar to the executor is worse than none: it vouches
// for a transition that then fails after the commit, which is the one outcome
// this exists to prevent. Two divergences were found and closed by Plan 3's
// Task A3:
//
//   - WHITESPACE. closeParent emptiness-tests strings.TrimSpace(cfg.LinkType)
//     but MATCHES on the untrimmed `l.LinkTypeKey == cfg.LinkType`. This used
//     to look the TRIMMED key up among the record's links, so a config with a
//     stray space passed here and found nothing there. The trim is kept for
//     the emptiness test, where closeParent also trims, and dropped from the
//     lookup, where closeParent does not.
//   - THE TRANSITION. closeParent also refuses an empty `Transition`
//     ("close_parent config names no transition"), and that refusal arrives
//     after the commit. It is asked here too, with the same TrimSpace.
//
// WHAT IT STILL CANNOT SEE, so nobody reads more into it than it says: the
// named transition may not be available out of the parent's current state,
// and a requirement at the state it enters may be unsatisfiable from what is
// already stored on the parent (closeParent passes no Values). Both are
// post-commit failures still, and the history notes written on BOTH records
// are what stands in for them.
func eoCheckCloseParentHasAParent(ctx context.Context, r repository.EORecordRepository, rec *models.EORecord, transition *models.WorkflowTransition) error {
	var wanted []string
	for i := range transition.Actions {
		a := transition.Actions[i]
		if !a.IsActive || a.ActionType != eoActionTypeCloseParent {
			continue
		}
		var cfg eoCloseParentConfig
		if err := json.Unmarshal([]byte(a.Config), &cfg); err != nil {
			return fmt.Errorf("transition %q carries a close_parent action %q whose configuration cannot be read (%v), so it cannot close anything",
				transition.Code, a.Name, err)
		}
		if strings.TrimSpace(cfg.LinkType) == "" {
			return fmt.Errorf("transition %q carries a close_parent action %q that names no link type, so it cannot say which parent to close",
				transition.Code, a.Name)
		}
		if strings.TrimSpace(cfg.Transition) == "" {
			return fmt.Errorf("transition %q carries a close_parent action %q that names no transition to run on the parent, so the parent would be left open after this one commits",
				transition.Code, a.Name)
		}
		// PADDING IS ALSO A REFUSAL, and here it can be exact. closeParent
		// matches the parent's available transitions on `available[i].Code
		// != cfg.Transition`, untrimmed, so " close " matches no workflow
		// code — unlike a link type key, no stored transition code could
		// legitimately carry the padding, and there is nothing to look it up
		// against before the commit. Refusing the SHAPE is therefore exactly
		// as strict as the executor and no stricter.
		if cfg.Transition != strings.TrimSpace(cfg.Transition) {
			return fmt.Errorf("transition %q carries a close_parent action %q whose transition %q is padded with whitespace; closeParent compares it with the parent's transition codes untrimmed, so it would match none of them after this one commits",
				transition.Code, a.Name, cfg.Transition)
		}
		// UNTRIMMED, which is the whole point: closeParent compares
		// l.LinkTypeKey with cfg.LinkType as written.
		wanted = append(wanted, cfg.LinkType)
	}
	if len(wanted) == 0 {
		return nil
	}

	links, err := r.LinksFrom(ctx, rec.ID)
	if err != nil {
		return fmt.Errorf("read the links of %s: %w", rec.Reference, err)
	}
	have := make(map[string]bool, len(links))
	for _, l := range links {
		have[l.LinkTypeKey] = true
	}
	for _, key := range wanted {
		if !have[key] {
			return fmt.Errorf("%w: %s carries no %q link. Link it to its parent and run this transition again; otherwise the parent would stay open with nothing left to close it",
				ErrEOCloseParentHasNoParent, rec.Reference, key)
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

// ── attachments ─────────────────────────────────────────────────────────────

// eoAttachmentsFieldKey is the values-bag key every record type inherits for
// its files: seed_eo_platform.go seeds it on the EO base type as a
// models.EOKindMultiFile field, and seed_eo_pilot.go narrows it to required on
// Incoming Correspondence. It is a bag key, not a column — it is absent from
// eoEngineOwnedKeys and from eoRecordColumnWriters — so eoApplyValues routes
// it into the jsonb exactly as it routes any other field.
const eoAttachmentsFieldKey = "attachments"

// eoAttachmentIDs drops the entries that are not ids at all: a blank or
// whitespace-only string is "no attachment", by the same rule eoIsEmpty
// applies to every other string value. Ids that are not blank are kept
// VERBATIM — an id is Documenta's string, not ours to trim or normalise, and
// one with spaces around it is refused by the shape check rather than quietly
// repaired into a different id.
func eoAttachmentIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			continue
		}
		out = append(out, id)
	}
	return out
}

// eoFileAttachments files the transition's attachments into the record's
// attachments field (spec §7.12: all file and multi-file fields store
// Documenta file IDs).
//
// Plan 1 left these ids checked for presence and DROPPED, which meant a
// transition could demand a letter, refuse to run without one, and keep
// nothing — the record passed its control and ended up with no file. They are
// MERGED, not replaced: the field is the record's dossier, so a second
// transition adds to it and an id already filed is not filed twice. Order is
// preserved, oldest first, because that is the order the files arrived in.
//
// The field's own rules apply to this write. Filing is an ENGINE write, and
// an engine write does not outrank the configuration: `attachments` is an
// ordinary field, and the two guards ValidateRecordValues applies to it on
// the create and update paths — provenance, and IsLockedAfterStateID — apply
// here too. A transition that demands an attachment into a field the client
// has locked, or has configured as one the system fills, is contradictory
// configuration, and it fails loudly at the point of use naming the conflict
// rather than resolving itself in the engine's favour. A guard that cannot
// fire in the case that matters is not a guard.
//
// Five refusals, none of them silent:
//
//   - a record type with no attachments field has nowhere to put them. That
//     is configuration, not input, so it returns a plain error (a 500 and a
//     log) rather than a field map a client could act on: no form the client
//     can fill would fix it.
//   - a field whose provenance is `derived` or `auto` is filled by the
//     system, and ValidateRecordValues refuses a submitted value for one
//     outright. Same answer here, for the same reason.
//   - a field LOCKED by a state the record has already entered refuses a
//     change. Ids the record already holds are not a change, so an actor
//     re-sending what is filed is let through, exactly as an unchanged echo
//     is on the update path.
//   - a `file` field holds ONE id, so a second one is refused rather than
//     overwriting the first.
//   - an id that is not shaped like a Documenta file id is refused as a
//     field error, the same shape check ValidateRecordValues applies to the
//     same field on the create and update paths.
//
// Both configuration refusals are plain errors rather than field errors: no
// value the actor could send would satisfy them.
func eoFileAttachments(fields []models.EOFieldDef, rec *models.EORecord, entered map[uuid.UUID]bool, transitionCode string, attachments []string) error {
	ids := eoAttachmentIDs(attachments)
	if len(ids) == 0 {
		return nil
	}

	var def *models.EOFieldDef
	for i := range fields {
		if fields[i].Key == eoAttachmentsFieldKey {
			def = &fields[i]
			break
		}
	}
	if def == nil {
		return fmt.Errorf("record %s: %d attachment(s) were supplied but record type %s has no %q field to file them in; add the field to the type rather than letting the ids be dropped",
			rec.Reference, len(ids), rec.RecordTypeID, eoAttachmentsFieldKey)
	}
	if def.Kind != models.EOKindFile && def.Kind != models.EOKindMultiFile {
		return fmt.Errorf("record %s: field %q is of kind %q, which does not hold Documenta file ids; attachments can only be filed in a %q or %q field",
			rec.Reference, eoAttachmentsFieldKey, def.Kind, models.EOKindFile, models.EOKindMultiFile)
	}
	if def.Provenance == models.EOProvenanceDerived || def.Provenance == models.EOProvenanceAuto {
		return fmt.Errorf("transition %q on record %s carries %d attachment(s), but field %q has provenance %q: the system fills that field and ValidateRecordValues refuses a submitted value for it, so filing these would write past a rule the same key obeys on every other path; configure the field as a user field, or the transition without attachments",
			transitionCode, rec.Reference, len(ids), eoAttachmentsFieldKey, def.Provenance)
	}

	name := def.Label
	if name == "" {
		name = def.Key
	}
	for _, id := range ids {
		if !eoIsDocumentaFileID(id) {
			return eoValidationError(map[string]string{eoAttachmentsFieldKey: name + " must be a Documenta file id"})
		}
	}

	// What the record already holds is kept VERBATIM, whatever it is. A
	// record created before the shape check may hold a value that is not an
	// id at all, and filing a new attachment is not the moment to delete it:
	// the merge adds, and nothing here removes.
	merged := eoStoredAttachmentValues(rec)
	held := len(merged)
	seen := make(map[string]bool, len(merged)+len(ids))
	for _, v := range merged {
		if s, ok := v.(string); ok {
			seen[s] = true
		}
	}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		merged = append(merged, id)
	}

	// Every id was already filed: there is nothing to write, so there is
	// nothing for the lock to refuse and no history entry to make.
	if len(merged) == held {
		return nil
	}
	if def.IsLockedAfterStateID != nil && entered[*def.IsLockedAfterStateID] {
		return fmt.Errorf("transition %q on record %s carries %d attachment(s), but field %q is locked after a state this record has already entered: the record's files can no longer be changed, so filing them would write past the lock; configure the transition without attachments, or the field without the lock",
			transitionCode, rec.Reference, len(ids), eoAttachmentsFieldKey)
	}

	var value any = merged
	if def.Kind == models.EOKindFile {
		if len(merged) > 1 {
			return eoValidationError(map[string]string{eoAttachmentsFieldKey: name + " holds one file, and this record already has one"})
		}
		value = merged[0]
	}
	return eoValidationError(eoApplyValues(rec, map[string]any{eoAttachmentsFieldKey: value}))
}

// eoStoredAttachmentValues reads what the record's attachments field already
// holds, as a list: a `multi_file` field's list, a `file` field's single id,
// or — for a record saved before the shape check existed — whatever else is
// there, kept as one entry rather than discarded. An empty or absent value is
// no entries.
func eoStoredAttachmentValues(rec *models.EORecord) []any {
	raw, ok := eoDecodeValues(rec.Values)[eoAttachmentsFieldKey]
	if !ok || eoIsEmpty(raw) {
		return nil
	}
	if list, ok := raw.([]any); ok {
		out := make([]any, 0, len(list))
		for _, item := range list {
			if eoIsEmpty(item) {
				continue
			}
			out = append(out, item)
		}
		return out
	}
	return []any{raw}
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
func (s *eoTransitionService) checkBlockingChildren(ctx context.Context, r repository.EORecordRepository, types repository.EORecordTypeRepository, rec *models.EORecord, transition *models.WorkflowTransition, toState *models.WorkflowState) error {
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
		lt, err := types.GetLinkTypeByKey(ctx, l.LinkTypeKey)
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
	fields []models.EOFieldDef,
	stored map[string]any,
	rec *models.EORecord,
	transition *models.WorkflowTransition,
	input models.EOTransitionInput,
	entered map[uuid.UUID]bool,
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

	baseline := eoValidationBaseline(stored, eoFlattenStored(rec), input.Values)

	// The attachments the actor supplied are filed immediately after this
	// (step 6a), so requiredness must be asked of the record the transition
	// WILL leave behind. They arrive on input.Attachments rather than in
	// input.Values, so without this a field required at the target state is
	// reported missing to an actor who has just supplied it.
	//
	// The baseline is the right channel and the only safe one: it answers
	// "the effective value of a key that was NOT submitted", which is read
	// in exactly the branch the locked-field comparison is not, so the
	// overlay cannot reach that comparison. When the key IS submitted the
	// value is already before the validator and nothing is overlaid.
	if ids := eoAttachmentIDs(input.Attachments); len(ids) > 0 {
		if _, submitted := input.Values[eoAttachmentsFieldKey]; !submitted {
			baseline[eoAttachmentsFieldKey] = ids
		}
	}

	fieldErrs, err := ValidateRecordValues(fields, baseline, input.Values, &transition.ToStateID, entered, s.eval)
	if err != nil {
		return err
	}
	if err := eoValidationError(fieldErrs); err != nil {
		return err
	}
	return eoValidationError(eoApplyValues(rec, input.Values))
}

// eoValidationBaseline builds the one `stored` map ValidateRecordValues takes,
// which it reads for two different questions that want two different answers
// once the engine has written something of its own (today: the transition's
// static assignment).
//
//   - The locked-field check compares a SUBMITTED value against stored[key]
//     to decide whether it is a change. That comparand must be the value the
//     record held BEFORE this transition touched anything, or the field the
//     engine just wrote looks unchanged and slips past its own lock.
//   - Requiredness reads stored[key] as the effective value of a key that was
//     NOT submitted. That one must be the value the record will actually hold,
//     or a transition whose whole job is to assign an assignee is told the
//     assignee is missing.
//
// The two never collide on one key, because each is read in the branch the
// other is not: the lock comparand only when the key is submitted, the
// effective value only when it is not (eo_fields.go:164-174). So the baseline
// is `before`, with `current` overlaid for exactly the keys the actor did not
// submit.
//
// before is not modified — the history diff still needs it as the
// pre-transition snapshot.
func eoValidationBaseline(before, current, submitted map[string]any) map[string]any {
	out := make(map[string]any, len(before))
	for k, v := range before {
		out[k] = v
	}
	for k, v := range current {
		if _, wasSubmitted := submitted[k]; !wasSubmitted {
			out[k] = v
		}
	}
	return out
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

// eoStateForSLA returns a copy of the state with its escalation policy loaded, or
// nil when entering it starts no SLA clock: SLAs are not wired, the state has no
// SLA or no policy, or it is terminal (a closed record does not escalate).
//
// A policy that has since been deleted is logged and skipped rather than
// failing the transition: refusing every move into the state over a
// configuration gap would stop the work the SLA exists to protect.
func eoStateForSLA(ctx context.Context, policies repository.EscalationPolicyRepository, state *models.WorkflowState) (*models.WorkflowState, error) {
	if policies == nil || IsTerminal(state) || state.SLADuration() <= 0 || state.EscalationPolicyID == nil {
		return nil, nil
	}
	policy, err := policies.FindByID(ctx, *state.EscalationPolicyID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		log.Printf("eo: state %q references escalation policy %s, which no longer exists; no SLA clock started", state.Code, *state.EscalationPolicyID)
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load escalation policy %s: %w", *state.EscalationPolicyID, err)
	}
	cp := *state
	cp.EscalationPolicy = policy
	return &cp, nil
}

// eoArmCapabilityTimers asks one capability for the timers of the state rec is
// in and schedules them on timers, which must be the transaction's own handle.
// It is shared by the transition and create paths so the stamping contract
// cannot drift between them.
//
// RecordID, RecordTypeID and CapabilityKey are stamped when the capability left
// them empty. One it set to somebody else's value is a capability bug and is
// refused, not silently rewritten: a timer is cancelled by its capability key,
// so one carrying another capability's key would be cancelled by that
// capability's state change while its owner never knew.
func eoArmCapabilityTimers(ctx context.Context, read EOCapabilityReader, c EOEnabledCapability, rec *models.EORecord, timers repository.EOTimerRepository) error {
	key := c.Key()
	capTimers, err := c.Timers(ctx, read, rec, c.Config)
	if err != nil {
		return fmt.Errorf("capability %q timers: %w", key, err)
	}
	for i := range capTimers {
		// A copy: Schedule mutates what it is given, and a capability may
		// return a cached slice.
		t := capTimers[i]
		if t.CapabilityKey != "" && t.CapabilityKey != key {
			return fmt.Errorf("capability %q returned a timer keyed %q", key, t.CapabilityKey)
		}
		if t.RecordID != uuid.Nil && t.RecordID != rec.ID {
			return fmt.Errorf("capability %q returned a timer for record %s, not %s", key, t.RecordID, rec.ID)
		}
		if t.RecordTypeID != uuid.Nil && t.RecordTypeID != rec.RecordTypeID {
			return fmt.Errorf("capability %q returned a timer for record type %s, not %s", key, t.RecordTypeID, rec.RecordTypeID)
		}
		t.RecordID = rec.ID
		t.RecordTypeID = rec.RecordTypeID
		t.CapabilityKey = key
		if err := timers.Schedule(ctx, &t); err != nil {
			return fmt.Errorf("capability %q schedule timer: %w", key, err)
		}
	}
	return nil
}
