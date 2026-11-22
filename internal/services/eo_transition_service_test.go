package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// ── fakes ───────────────────────────────────────────────────────────────────
// Each embeds the repository interface and overrides only what the transition
// path calls, so a method the service starts using surfaces as a nil-pointer
// panic rather than as a silent zero value. No database.

type eoTxRecordRepo struct {
	repository.EORecordRepository
	stored   map[uuid.UUID]*models.EORecord
	children map[uuid.UUID][]models.EORecord
	links    map[uuid.UUID][]models.EORecordLink
	entered  map[uuid.UUID]bool

	updated   []models.EORecord
	history   []models.EORecordHistory
	committed bool
	// calls records the repository methods the executor used, in order, so a
	// test can pin that the row lock is taken before anything is read.
	calls []string
	// childrenTotal counts every Children read; childrenInTx counts the
	// subset that went through the transaction's own handle. A read taken
	// outside the transaction while it holds a connection and a row lock is
	// a pool-starvation deadlock waiting to happen, and the two counters are
	// what makes that visible.
	childrenTotal int
	// txTypes is the transaction-bound type repository Transaction hands its
	// closure: the same data as the service's, but a distinct object.
	txTypes      repository.EORecordTypeRepository
	childrenInTx int
	// getAnyTotal / getAnyInTx do the same for GetAnyByID, which is how an
	// ancestor-scoped roll-up walks upwards — the other half of the same
	// deadlock class.
	getAnyTotal int
	getAnyInTx  int
}

func (f *eoTxRecordRepo) LockForUpdate(_ context.Context, id, recordTypeID uuid.UUID) (*models.EORecord, error) {
	f.calls = append(f.calls, "LockForUpdate")
	rec, ok := f.stored[id]
	if !ok || rec.RecordTypeID != recordTypeID {
		return nil, nil
	}
	cp := *rec
	return &cp, nil
}

func (f *eoTxRecordRepo) GetByID(_ context.Context, id, recordTypeID uuid.UUID) (*models.EORecord, error) {
	f.calls = append(f.calls, "GetByID")
	rec, ok := f.stored[id]
	if !ok || rec.RecordTypeID != recordTypeID {
		return nil, nil
	}
	cp := *rec
	return &cp, nil
}

func (f *eoTxRecordRepo) GetAnyByID(_ context.Context, id uuid.UUID) (*models.EORecord, error) {
	f.getAnyTotal++
	rec, ok := f.stored[id]
	if !ok {
		return nil, nil
	}
	cp := *rec
	return &cp, nil
}

func (f *eoTxRecordRepo) Children(_ context.Context, parentID uuid.UUID, recordTypeID *uuid.UUID) ([]models.EORecord, error) {
	f.calls = append(f.calls, "Children")
	f.childrenTotal++
	var out []models.EORecord
	for _, c := range f.children[parentID] {
		if recordTypeID == nil || c.RecordTypeID == *recordTypeID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *eoTxRecordRepo) LinksFrom(_ context.Context, recordID uuid.UUID) ([]models.EORecordLink, error) {
	return f.links[recordID], nil
}

func (f *eoTxRecordRepo) EnteredStateIDs(_ context.Context, _, _ uuid.UUID) (map[uuid.UUID]bool, error) {
	return f.entered, nil
}

func (f *eoTxRecordRepo) Update(_ context.Context, rec *models.EORecord) error {
	f.calls = append(f.calls, "Update")
	f.updated = append(f.updated, *rec)
	cp := *rec
	f.stored[rec.ID] = &cp
	return nil
}

func (f *eoTxRecordRepo) CreateHistory(_ context.Context, entry *models.EORecordHistory) error {
	f.history = append(f.history, *entry)
	return nil
}

// eoTxBoundRepo is what Transaction hands its closure: the same store, but a
// distinct object, so a read taken on the service's own non-transactional
// repository can be told apart from one taken through the transaction.
type eoTxBoundRepo struct {
	*eoTxRecordRepo
}

func (b *eoTxBoundRepo) Children(ctx context.Context, parentID uuid.UUID, recordTypeID *uuid.UUID) ([]models.EORecord, error) {
	b.eoTxRecordRepo.childrenInTx++
	return b.eoTxRecordRepo.Children(ctx, parentID, recordTypeID)
}

func (b *eoTxBoundRepo) GetAnyByID(ctx context.Context, id uuid.UUID) (*models.EORecord, error) {
	b.eoTxRecordRepo.getAnyInTx++
	return b.eoTxRecordRepo.GetAnyByID(ctx, id)
}

func (f *eoTxRecordRepo) Transaction(_ context.Context, fn func(repository.EORecordRepository, repository.EORecordTypeRepository, repository.EOTimerRepository) error) error {
	if err := fn(&eoTxBoundRepo{f}, f.txTypes, nil); err != nil {
		return err
	}
	f.committed = true
	return nil
}

// eoTxBoundTypes is the type repository as the transaction sees it.
type eoTxBoundTypes struct{ *eoTxTypeRepo }

type eoTxTypeRepo struct {
	repository.EORecordTypeRepository
	types     map[uuid.UUID]*models.EORecordType
	defs      []models.EOFieldDef
	linkTypes map[string]*models.EOLinkType
}

func (f *eoTxTypeRepo) GetByID(_ context.Context, id uuid.UUID) (*models.EORecordType, error) {
	return f.types[id], nil
}

func (f *eoTxTypeRepo) Ancestors(_ context.Context, t *models.EORecordType) ([]models.EORecordType, error) {
	var out []models.EORecordType
	for cur := t; cur.ParentTypeID != nil; {
		parent := f.types[*cur.ParentTypeID]
		if parent == nil {
			return nil, errors.New("ancestor type not found")
		}
		out = append(out, *parent)
		cur = parent
	}
	return out, nil
}

func (f *eoTxTypeRepo) FieldDefs(_ context.Context, recordTypeIDs []uuid.UUID) ([]models.EOFieldDef, error) {
	want := map[uuid.UUID]bool{}
	for _, id := range recordTypeIDs {
		want[id] = true
	}
	var out []models.EOFieldDef
	for _, d := range f.defs {
		if want[d.RecordTypeID] {
			out = append(out, d)
		}
	}
	return out, nil
}

func (f *eoTxTypeRepo) GetByKey(_ context.Context, key string) (*models.EORecordType, error) {
	for _, t := range f.types {
		if t.Key == key {
			return t, nil
		}
	}
	return nil, nil
}

func (f *eoTxTypeRepo) GetLinkTypeByKey(_ context.Context, key string) (*models.EOLinkType, error) {
	return f.linkTypes[key], nil
}

type eoTxWorkflowRepo struct {
	repository.WorkflowRepository
	wf          models.Workflow
	transitions map[uuid.UUID]*models.WorkflowTransition
}

func (f *eoTxWorkflowRepo) List(_ context.Context, _ bool) ([]models.Workflow, error) {
	wf := f.wf
	wf.Transitions = nil
	for _, tr := range f.transitions {
		// Mirrors workflowRepository.List, which preloads Transitions but not
		// Transitions.FieldChanges. A fake more generous than the repository
		// hid a guard that never ran in production.
		cp := *tr
		cp.FieldChanges = nil
		wf.Transitions = append(wf.Transitions, cp)
	}
	sort.Slice(wf.Transitions, func(i, j int) bool { return wf.Transitions[i].Code < wf.Transitions[j].Code })
	return []models.Workflow{wf}, nil
}

func (f *eoTxWorkflowRepo) FindTransitionByIDWithRelations(_ context.Context, id uuid.UUID) (*models.WorkflowTransition, error) {
	tr, ok := f.transitions[id]
	if !ok {
		return nil, fmt.Errorf("transition %s not found", id)
	}
	cp := *tr
	return &cp, nil
}

// eoTxActionSpy stands in for Task 15's executor. It records whether the
// transaction had already committed when it was called.
type eoTxActionSpy struct {
	records       *eoTxRecordRepo
	calls         int
	sawCommitted  bool
	sawState      *uuid.UUID
	sawTransition string
	err           error
}

func (s *eoTxActionSpy) ExecuteActions(_ context.Context, rec *models.EORecord, transition *models.WorkflowTransition, _ *models.User) error {
	s.calls++
	s.sawCommitted = s.records.committed
	s.sawState = rec.StateID
	s.sawTransition = transition.Code
	return s.err
}

// ── fixture ─────────────────────────────────────────────────────────────────

// eoTxFixture is one configured record type with a four-state workflow:
//
//	draft --submit--> review --raise--> escalated
//	                  review --close--> closed (terminal)
//	                  closed --reopen-> review
//
// The record starts in review, so both block semantics (close and raise) are
// reachable from one fixture.
type eoTxFixture struct {
	records   *eoTxRecordRepo
	types     *eoTxTypeRepo
	workflows *eoTxWorkflowRepo
	actions   *eoTxActionSpy
	svc       EOTransitionService

	recordType *models.EORecordType
	draft      models.WorkflowState
	review     models.WorkflowState
	escalated  models.WorkflowState
	closed     models.WorkflowState

	rec   *models.EORecord
	actor *models.User
	role  models.Role
}

func eoNewTxFixture(t *testing.T) *eoTxFixture {
	t.Helper()
	f := &eoTxFixture{
		records: &eoTxRecordRepo{
			stored:   map[uuid.UUID]*models.EORecord{},
			children: map[uuid.UUID][]models.EORecord{},
			links:    map[uuid.UUID][]models.EORecordLink{},
		},
		types: &eoTxTypeRepo{
			types:     map[uuid.UUID]*models.EORecordType{},
			linkTypes: map[string]*models.EOLinkType{},
		},
		role: models.Role{ID: uuid.New(), Code: "executive_office"},
	}
	f.records.txTypes = &eoTxBoundTypes{f.types}
	f.actions = &eoTxActionSpy{records: f.records}
	f.actor = &models.User{ID: uuid.New(), Username: "eo.officer", Roles: []models.Role{f.role}}

	f.recordType = &models.EORecordType{ID: uuid.New(), Key: "eo_draft_study", Name: "Draft Decision Study", IsActive: true}
	f.types.types[f.recordType.ID] = f.recordType

	f.draft = models.WorkflowState{ID: uuid.New(), Code: "draft", StateType: "initial"}
	f.review = models.WorkflowState{ID: uuid.New(), Code: "review", StateType: "normal"}
	f.escalated = models.WorkflowState{ID: uuid.New(), Code: "escalated", StateType: "normal"}
	f.closed = models.WorkflowState{ID: uuid.New(), Code: "closed", StateType: "terminal"}

	wfID := uuid.New()
	f.workflows = &eoTxWorkflowRepo{
		wf: models.Workflow{
			ID: wfID, Code: "eo-draft-study", RecordTypeID: &f.recordType.ID,
			States: []models.WorkflowState{f.draft, f.review, f.escalated, f.closed},
		},
		transitions: map[uuid.UUID]*models.WorkflowTransition{},
	}
	f.addTransition(&models.WorkflowTransition{
		Code: "submit", Name: "Submit", FromStateID: f.draft.ID, ToStateID: f.review.ID, IsActive: true,
		FieldChanges: []models.TransitionFieldChange{
			{FieldName: "he_reason", Label: "Reason"},
			{FieldName: "priority", Label: "Priority"},
		},
	})
	f.addTransition(&models.WorkflowTransition{
		Code: "raise", Name: "Raise to H.E.", FromStateID: f.review.ID, ToStateID: f.escalated.ID, IsActive: true,
	})
	f.addTransition(&models.WorkflowTransition{
		Code: "close", Name: "Close", FromStateID: f.review.ID, ToStateID: f.closed.ID, IsActive: true,
		FieldChanges: []models.TransitionFieldChange{{FieldName: "closure_note", Label: "Closure note"}},
	})
	f.addTransition(&models.WorkflowTransition{
		Code: "reopen", Name: "Reopen", FromStateID: f.closed.ID, ToStateID: f.review.ID, IsActive: true,
	})

	f.types.defs = []models.EOFieldDef{
		eoTxField("subject_ar", models.EOKindText, models.EORequiredAlways, f.recordType.ID),
		eoTxField("priority", models.EOKindText, models.EORequiredNever, f.recordType.ID),
		eoTxField("he_reason", models.EOKindLongText, models.EORequiredNever, f.recordType.ID),
		eoTxField("chair", models.EOKindUser, models.EORequiredNever, f.recordType.ID),
		eoTxField("members", models.EOKindMultiUser, models.EORequiredNever, f.recordType.ID),
		eoTxClosureNote(f.recordType.ID, f.closed.ID),
	}

	f.rec = &models.EORecord{
		ID: uuid.New(), RecordTypeID: f.recordType.ID, Reference: "EO-DRF-2026-001",
		SubjectAr: "دراسة", StateID: &f.review.ID, CreatedByID: f.actor.ID,
		Values: datatypes.JSON(`{}`),
	}
	f.records.stored[f.rec.ID] = f.rec

	f.svc = NewEOTransitionService(f.records, f.types, f.workflows, f.actions)
	return f
}

func (f *eoTxFixture) addTransition(tr *models.WorkflowTransition) {
	tr.ID = uuid.New()
	tr.WorkflowID = f.workflows.wf.ID
	f.workflows.transitions[tr.ID] = tr
}

// transition returns the live configuration of one transition, so a test can
// switch on exactly the control it is about.
func (f *eoTxFixture) transition(code string) *models.WorkflowTransition {
	for _, tr := range f.workflows.transitions {
		if tr.Code == code {
			return tr
		}
	}
	panic("no transition " + code)
}

func (f *eoTxFixture) run(code string, actor *models.User, input models.EOTransitionInput) (*models.EORecord, error) {
	return f.svc.Execute(context.Background(), f.rec, f.transition(code).ID, actor, input)
}

// setValues replaces the record's jsonb bag.
func (f *eoTxFixture) setValues(t *testing.T, values map[string]any) {
	t.Helper()
	raw, err := json.Marshal(values)
	if err != nil {
		t.Fatalf("encode values: %v", err)
	}
	f.rec.Values = raw
	f.records.stored[f.rec.ID] = f.rec
}

// addChild hangs an open child record off the record under test and links it
// with linkTypeKey. The link points child -> parent, the direction the
// universal Task uses; BlockingChildren identifies the child by membership,
// not by direction.
func (f *eoTxFixture) addChild(linkTypeKey string, closed bool) models.EORecord {
	child := models.EORecord{ID: uuid.New(), RecordTypeID: uuid.New(), Reference: "CHILD-" + linkTypeKey}
	if closed {
		now := eoTxNow()
		child.ClosedAt = &now
	}
	f.records.children[f.rec.ID] = append(f.records.children[f.rec.ID], child)
	f.records.links[child.ID] = append(f.records.links[child.ID], models.EORecordLink{
		ID: uuid.New(), FromRecordID: child.ID, ToRecordID: f.rec.ID, LinkTypeKey: linkTypeKey,
	})
	return child
}

func eoTxNow() time.Time { return time.Now() }

// eoTxField is a plain user-entered field definition.
func eoTxField(key, kind, requiredMode string, typeID uuid.UUID) models.EOFieldDef {
	return models.EOFieldDef{
		ID: uuid.New(), RecordTypeID: typeID, Key: key, Label: key, Kind: kind,
		Provenance: models.EOProvenanceUser, RequiredMode: requiredMode,
	}
}

// eoTxClosureNote is the field matrix's "Required: Yes at closure".
func eoTxClosureNote(typeID, closedStateID uuid.UUID) models.EOFieldDef {
	d := eoTxField("closure_note", models.EOKindLongText, models.EORequiredAtState, typeID)
	d.RequiredAtStateIDs = datatypes.JSON(`["` + closedStateID.String() + `"]`)
	return d
}

func eoTxActorRules(t *testing.T, rules ...TransitionActorRule) datatypes.JSON {
	t.Helper()
	raw, err := json.Marshal(rules)
	if err != nil {
		t.Fatalf("encode actor rules: %v", err)
	}
	return raw
}

func eoTxMandatory(v bool) *bool { return &v }

// ── state ───────────────────────────────────────────────────────────────────

func TestExecuteRejectsTransitionFromWrongState(t *testing.T) {
	t.Run("the record is not in the transition's from state", func(t *testing.T) {
		f := eoNewTxFixture(t)
		// The record is in review; submit runs draft -> review.
		if _, err := f.run("submit", f.actor, models.EOTransitionInput{}); err == nil {
			t.Fatal("submit from review succeeded, want a wrong-state rejection")
		} else if !errors.Is(err, ErrEOTransitionNotAvailable) {
			t.Fatalf("err = %v, want ErrEOTransitionNotAvailable", err)
		}
		if len(f.records.updated) != 0 || len(f.records.history) != 0 {
			t.Errorf("wrote %d updates and %d history rows, want none", len(f.records.updated), len(f.records.history))
		}
	})

	t.Run("a transition of another workflow", func(t *testing.T) {
		f := eoNewTxFixture(t)
		// Identical to the raise in every respect but its workflow, so
		// belonging to another workflow is the only thing that can refuse it.
		stray := &models.WorkflowTransition{
			ID: uuid.New(), WorkflowID: uuid.New(), Code: "stray",
			FromStateID: f.review.ID, ToStateID: f.escalated.ID, IsActive: true,
		}
		f.workflows.transitions[stray.ID] = stray
		_, err := f.svc.Execute(context.Background(), f.rec, stray.ID, f.actor, models.EOTransitionInput{})
		if err == nil {
			t.Fatal("a transition from another workflow succeeded, want a rejection")
		}
		if !errors.Is(err, ErrEOTransitionNotAvailable) {
			t.Fatalf("err = %v, want ErrEOTransitionNotAvailable", err)
		}
	})

	t.Run("an inactive transition", func(t *testing.T) {
		f := eoNewTxFixture(t)
		f.transition("close").IsActive = false
		// Everything else about this call is valid, so inactivity is the
		// only thing that can refuse it.
		_, err := f.run("close", f.actor, models.EOTransitionInput{Values: map[string]any{"closure_note": "done"}})
		if err == nil {
			t.Fatal("an inactive transition succeeded, want a rejection")
		}
		if !errors.Is(err, ErrEOTransitionNotAvailable) {
			t.Fatalf("err = %v, want ErrEOTransitionNotAvailable", err)
		}
	})
}

// ── actors: global roles ────────────────────────────────────────────────────

func TestExecuteEnforcesAllowedRoles(t *testing.T) {
	other := models.Role{ID: uuid.New(), Code: "protocol"}

	tests := []struct {
		name    string
		allowed []models.Role
		roles   []models.Role
		wantErr bool
	}{
		// incident_service.go:3432 — an empty AllowedRoles means no
		// restriction, not "nobody".
		{"empty allowed roles restrict nobody", nil, nil, false},
		{"empty allowed roles restrict nobody, even a role-less actor", nil, []models.Role{other}, false},
		{"a listed role may act", []models.Role{other}, []models.Role{other}, false},
		{"an unlisted role may not", []models.Role{other}, nil, true},
		{"one of several listed roles is enough", []models.Role{{ID: uuid.New()}, other}, []models.Role{other}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := eoNewTxFixture(t)
			f.transition("raise").AllowedRoles = tc.allowed
			actor := &models.User{ID: uuid.New(), Roles: tc.roles}

			_, err := f.run("raise", actor, models.EOTransitionInput{})
			if tc.wantErr {
				if err == nil {
					t.Fatal("transition succeeded, want a permission rejection")
				}
				if !errors.Is(err, ErrEOTransitionNotPermitted) {
					t.Fatalf("err = %v, want ErrEOTransitionNotPermitted", err)
				}
				if len(f.records.updated) != 0 {
					t.Errorf("record was written despite the rejection")
				}
				return
			}
			if err != nil {
				t.Fatalf("transition: %v", err)
			}
		})
	}
}

func TestExecuteEnforcesRequireAssignee(t *testing.T) {
	f := eoNewTxFixture(t)
	f.transition("raise").RequireAssignee = true

	if _, err := f.run("raise", f.actor, models.EOTransitionInput{}); err == nil {
		t.Fatal("a non-assignee ran an assignee-only transition")
	} else if !errors.Is(err, ErrEOTransitionNotPermitted) {
		t.Fatalf("err = %v, want ErrEOTransitionNotPermitted", err)
	}

	f.rec.AssignedUserID = &f.actor.ID
	f.records.stored[f.rec.ID] = f.rec
	if _, err := f.run("raise", f.actor, models.EOTransitionInput{}); err != nil {
		t.Fatalf("the assignee was refused: %v", err)
	}
}

// ── actors: record-scoped rules (spec §7.15) ────────────────────────────────

func TestExecuteEnforcesRecordScopedActorRule(t *testing.T) {
	chair := uuid.New()
	member := uuid.New()

	t.Run("only the user in the chair field may act", func(t *testing.T) {
		f := eoNewTxFixture(t)
		f.setValues(t, map[string]any{"chair": chair.String()})
		raise := f.transition("raise")
		raise.AllowedRoles = []models.Role{f.role}
		raise.EOActorRules = eoTxActorRules(t, TransitionActorRule{FieldKey: "chair", From: EOActorFromSelf})

		// Same global role as the chair, but not the chair of THIS record.
		impostor := &models.User{ID: uuid.New(), Roles: []models.Role{f.role}}
		if _, err := f.run("raise", impostor, models.EOTransitionInput{}); err == nil {
			t.Fatal("a user with the right role but not named on the record was allowed through")
		} else if !errors.Is(err, ErrEOTransitionNotPermitted) {
			t.Fatalf("err = %v, want ErrEOTransitionNotPermitted", err)
		}

		theChair := &models.User{ID: chair, Roles: []models.Role{f.role}}
		if _, err := f.run("raise", theChair, models.EOTransitionInput{}); err != nil {
			t.Fatalf("the chair was refused: %v", err)
		}
	})

	t.Run("a multi-user field names every one of them", func(t *testing.T) {
		f := eoNewTxFixture(t)
		f.setValues(t, map[string]any{"members": []any{uuid.New().String(), member.String()}})
		f.transition("raise").EOActorRules = eoTxActorRules(t, TransitionActorRule{FieldKey: "members", From: EOActorFromSelf})

		if _, err := f.run("raise", &models.User{ID: member}, models.EOTransitionInput{}); err != nil {
			t.Fatalf("a named member was refused: %v", err)
		}
		if _, err := f.run("raise", &models.User{ID: uuid.New()}, models.EOTransitionInput{}); err == nil {
			t.Fatal("a user absent from the member list was allowed through")
		}
	})

	t.Run("a column-backed key resolves too", func(t *testing.T) {
		f := eoNewTxFixture(t)
		f.rec.AssignedUserID = &chair
		f.records.stored[f.rec.ID] = f.rec
		f.transition("raise").EOActorRules = eoTxActorRules(t, TransitionActorRule{FieldKey: "assigned_user_id", From: EOActorFromSelf})

		if _, err := f.run("raise", &models.User{ID: chair}, models.EOTransitionInput{}); err != nil {
			t.Fatalf("the named user was refused: %v", err)
		}
		if _, err := f.run("raise", &models.User{ID: uuid.New()}, models.EOTransitionInput{}); err == nil {
			t.Fatal("a stranger was allowed through")
		}
	})

	t.Run("an empty field names nobody", func(t *testing.T) {
		f := eoNewTxFixture(t)
		f.transition("raise").EOActorRules = eoTxActorRules(t, TransitionActorRule{FieldKey: "chair", From: EOActorFromSelf})
		if _, err := f.run("raise", f.actor, models.EOTransitionInput{}); err == nil {
			t.Fatal("an unset chair field let everyone through; an actor rule must fail closed")
		}
	})

	t.Run("several rules are satisfied by any one of them", func(t *testing.T) {
		f := eoNewTxFixture(t)
		f.setValues(t, map[string]any{"chair": chair.String(), "members": []any{member.String()}})
		f.transition("raise").EOActorRules = eoTxActorRules(t,
			TransitionActorRule{FieldKey: "chair", From: EOActorFromSelf},
			TransitionActorRule{FieldKey: "members", From: EOActorFromSelf},
		)
		if _, err := f.run("raise", &models.User{ID: member}, models.EOTransitionInput{}); err != nil {
			t.Fatalf("the second rule was not consulted: %v", err)
		}
	})

	t.Run("an unknown from is a configuration error", func(t *testing.T) {
		f := eoNewTxFixture(t)
		f.transition("raise").EOActorRules = datatypes.JSON(`[{"field_key":"chair","from":"grandparent"}]`)
		if _, err := f.run("raise", f.actor, models.EOTransitionInput{}); err == nil {
			t.Fatal("an unknown from was accepted")
		}
	})
}

func TestExecuteEnforcesActorRuleFromAncestor(t *testing.T) {
	// root --parent--> middle --parent--> the record under test
	rootOwner := uuid.New()
	parentOwner := uuid.New()

	build := func(t *testing.T) *eoTxFixture {
		t.Helper()
		f := eoNewTxFixture(t)
		root := &models.EORecord{
			ID: uuid.New(), RecordTypeID: uuid.New(), Reference: "ROOT",
			Values: datatypes.JSON(`{"owner":"` + rootOwner.String() + `"}`),
		}
		middle := &models.EORecord{
			ID: uuid.New(), RecordTypeID: uuid.New(), Reference: "MIDDLE", ParentRecordID: &root.ID,
			Values: datatypes.JSON(`{"owner":"` + parentOwner.String() + `"}`),
		}
		f.records.stored[root.ID] = root
		f.records.stored[middle.ID] = middle
		f.rec.ParentRecordID = &middle.ID
		f.records.stored[f.rec.ID] = f.rec
		return f
	}

	t.Run("from parent reads the immediate parent, not the root", func(t *testing.T) {
		f := build(t)
		f.transition("raise").EOActorRules = eoTxActorRules(t, TransitionActorRule{FieldKey: "owner", From: EOActorFromParent})

		if _, err := f.run("raise", &models.User{ID: parentOwner}, models.EOTransitionInput{}); err != nil {
			t.Fatalf("the parent's owner was refused: %v", err)
		}
		if _, err := f.run("raise", &models.User{ID: rootOwner}, models.EOTransitionInput{}); err == nil {
			t.Fatal("the root's owner satisfied a from:parent rule")
		}
	})

	t.Run("from root reads the topmost ancestor, not the parent", func(t *testing.T) {
		f := build(t)
		f.transition("raise").EOActorRules = eoTxActorRules(t, TransitionActorRule{FieldKey: "owner", From: EOActorFromRoot})

		if _, err := f.run("raise", &models.User{ID: rootOwner}, models.EOTransitionInput{}); err != nil {
			t.Fatalf("the root's owner was refused: %v", err)
		}
		if _, err := f.run("raise", &models.User{ID: parentOwner}, models.EOTransitionInput{}); err == nil {
			t.Fatal("the parent's owner satisfied a from:root rule")
		}
	})

	t.Run("a record with no ancestor satisfies no ancestor rule", func(t *testing.T) {
		f := eoNewTxFixture(t)
		f.transition("raise").EOActorRules = eoTxActorRules(t, TransitionActorRule{FieldKey: "owner", From: EOActorFromParent})
		if _, err := f.run("raise", f.actor, models.EOTransitionInput{}); err == nil {
			t.Fatal("a root record let everyone through a from:parent rule")
		}
	})
}

// ── requirements ────────────────────────────────────────────────────────────

func TestExecuteEnforcesRequirements(t *testing.T) {
	t.Run("a mandatory comment", func(t *testing.T) {
		f := eoNewTxFixture(t)
		f.transition("raise").Requirements = []models.TransitionRequirement{
			{RequirementType: "comment", IsMandatory: eoTxMandatory(true), ErrorMessage: "say why"},
		}
		_, err := f.run("raise", f.actor, models.EOTransitionInput{})
		if err == nil {
			t.Fatal("a transition requiring a comment ran without one")
		}
		if !strings.Contains(err.Error(), "say why") {
			t.Errorf("err = %v, want the configured error message", err)
		}
		if _, err := f.run("raise", f.actor, models.EOTransitionInput{Comment: "approved"}); err != nil {
			t.Fatalf("a comment was supplied and still refused: %v", err)
		}
	})

	t.Run("whitespace is not a comment", func(t *testing.T) {
		f := eoNewTxFixture(t)
		f.transition("raise").Requirements = []models.TransitionRequirement{
			{RequirementType: "comment", IsMandatory: eoTxMandatory(true)},
		}
		if _, err := f.run("raise", f.actor, models.EOTransitionInput{Comment: "   "}); err == nil {
			t.Fatal("blank space satisfied a mandatory comment")
		}
	})

	t.Run("a mandatory attachment", func(t *testing.T) {
		f := eoNewTxFixture(t)
		f.transition("raise").Requirements = []models.TransitionRequirement{
			{RequirementType: "attachment", IsMandatory: eoTxMandatory(true)},
		}
		if _, err := f.run("raise", f.actor, models.EOTransitionInput{}); err == nil {
			t.Fatal("a transition requiring an attachment ran without one")
		}
		if _, err := f.run("raise", f.actor, models.EOTransitionInput{Attachments: []uuid.UUID{uuid.New()}}); err != nil {
			t.Fatalf("one attachment was supplied and still refused: %v", err)
		}
	})

	t.Run("a single-attachment requirement refuses several", func(t *testing.T) {
		f := eoNewTxFixture(t)
		f.transition("raise").Requirements = []models.TransitionRequirement{
			{RequirementType: "attachment", IsMandatory: eoTxMandatory(true)},
		}
		two := []uuid.UUID{uuid.New(), uuid.New()}
		if _, err := f.run("raise", f.actor, models.EOTransitionInput{Attachments: two}); err == nil {
			t.Fatal("two attachments satisfied a single-attachment requirement")
		}
		f.transition("raise").Requirements[0].IsMultiple = eoTxMandatory(true)
		if _, err := f.run("raise", f.actor, models.EOTransitionInput{Attachments: two}); err != nil {
			t.Fatalf("is_multiple was set and several attachments still refused: %v", err)
		}
	})

	// incident_service.go:3469 — IsMandatory is a *bool with a DB default of
	// true, so nil and false are both "not enforced" at the executor.
	t.Run("is_mandatory nil or false is not enforced", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			flag *bool
		}{{"nil", nil}, {"false", eoTxMandatory(false)}} {
			t.Run(tc.name, func(t *testing.T) {
				f := eoNewTxFixture(t)
				f.transition("raise").Requirements = []models.TransitionRequirement{
					{RequirementType: "comment", IsMandatory: tc.flag},
					{RequirementType: "attachment", IsMandatory: tc.flag},
				}
				if _, err := f.run("raise", f.actor, models.EOTransitionInput{}); err != nil {
					t.Fatalf("a requirement that is not mandatory was enforced: %v", err)
				}
			})
		}
	})

	// A governance control the engine cannot evaluate must not pass silently.
	t.Run("a mandatory requirement type the engine does not evaluate is refused", func(t *testing.T) {
		for _, kind := range []string{"field_value", "feedback", "rating"} {
			t.Run(kind, func(t *testing.T) {
				f := eoNewTxFixture(t)
				f.transition("raise").Requirements = []models.TransitionRequirement{
					{RequirementType: kind, IsMandatory: eoTxMandatory(true)},
				}
				if _, err := f.run("raise", f.actor, models.EOTransitionInput{Comment: "x", Attachments: []uuid.UUID{uuid.New()}}); err == nil {
					t.Fatalf("a mandatory %q requirement passed although the EO engine never evaluates it", kind)
				}
			})
		}
	})

	t.Run("the same type that is not mandatory is skipped", func(t *testing.T) {
		f := eoNewTxFixture(t)
		f.transition("raise").Requirements = []models.TransitionRequirement{
			{RequirementType: "field_value", IsMandatory: eoTxMandatory(false)},
		}
		if _, err := f.run("raise", f.actor, models.EOTransitionInput{}); err != nil {
			t.Fatalf("an unevaluated requirement that is not mandatory blocked the transition: %v", err)
		}
	})
}

// ── required_at_state fields ────────────────────────────────────────────────

func TestExecuteEnforcesRequiredAtStateFields(t *testing.T) {
	t.Run("the field is required at the TARGET state", func(t *testing.T) {
		f := eoNewTxFixture(t)
		_, err := f.run("close", f.actor, models.EOTransitionInput{})
		if err == nil {
			t.Fatal("closure succeeded with the closure note empty")
		}
		fields := eoTxFieldErrors(t, err)
		if fields["closure_note"] == "" {
			t.Fatalf("field errors = %v, want closure_note required", fields)
		}
		if len(f.records.updated) != 0 {
			t.Error("the record was written despite the validation failure")
		}
	})

	t.Run("supplying it at the transition satisfies it", func(t *testing.T) {
		f := eoNewTxFixture(t)
		rec, err := f.run("close", f.actor, models.EOTransitionInput{Values: map[string]any{"closure_note": "done"}})
		if err != nil {
			t.Fatalf("close: %v", err)
		}
		if got := eoTxValues(t, rec.Values)["closure_note"]; got != "done" {
			t.Errorf("closure_note = %v, want done", got)
		}
	})

	t.Run("a value already stored satisfies it", func(t *testing.T) {
		f := eoNewTxFixture(t)
		f.setValues(t, map[string]any{"closure_note": "recorded earlier"})
		if _, err := f.run("close", f.actor, models.EOTransitionInput{}); err != nil {
			t.Fatalf("close: %v", err)
		}
	})

	t.Run("it is not required by a transition to another state", func(t *testing.T) {
		f := eoNewTxFixture(t)
		if _, err := f.run("raise", f.actor, models.EOTransitionInput{}); err != nil {
			t.Fatalf("raise: %v", err)
		}
	})

	// enteredStateIDs comes from eo_record_history, not from rec.StateID: a
	// locked field asks whether the record has EVER been in a state.
	t.Run("a locked field is locked by history, not by the current state", func(t *testing.T) {
		f := eoNewTxFixture(t)
		for i := range f.types.defs {
			if f.types.defs[i].Key == "he_reason" {
				f.types.defs[i].IsLockedAfterStateID = &f.draft.ID
			}
		}
		f.setValues(t, map[string]any{"he_reason": "first"})
		// The record is in review now and has long left draft; only the
		// history says it was ever there.
		f.records.entered = map[uuid.UUID]bool{f.draft.ID: true, f.review.ID: true}
		f.transition("close").FieldChanges = append(f.transition("close").FieldChanges,
			models.TransitionFieldChange{FieldName: "he_reason"})

		_, err := f.run("close", f.actor, models.EOTransitionInput{
			Values: map[string]any{"closure_note": "done", "he_reason": "second"},
		})
		if err == nil {
			t.Fatal("a field locked after draft was rewritten")
		}
		if fields := eoTxFieldErrors(t, err); fields["he_reason"] == "" {
			t.Fatalf("field errors = %v, want he_reason locked", fields)
		}
	})
}

// ── blocking children (spec §7.6) ───────────────────────────────────────────

func TestExecuteBlockedByOpenChild(t *testing.T) {
	// EO-DIR: an open Assignment blocks the parent's CLOSURE.
	// EO-DRF: an open Objection blocks the RAISE to H.E., not closure.
	// The two semantics are not interchangeable, so both directions are pinned.
	newFixture := func(t *testing.T) *eoTxFixture {
		t.Helper()
		f := eoNewTxFixture(t)
		f.types.linkTypes["assignment_of"] = &models.EOLinkType{Key: "assignment_of", BlocksParentClosure: true}
		f.types.linkTypes["objection_to"] = &models.EOLinkType{Key: "objection_to", BlocksParentTransitionKey: "raise"}
		return f
	}

	t.Run("an open assignment blocks closure", func(t *testing.T) {
		f := newFixture(t)
		f.addChild("assignment_of", false)
		_, err := f.run("close", f.actor, models.EOTransitionInput{Values: map[string]any{"closure_note": "done"}})
		if err == nil {
			t.Fatal("closure succeeded with an open assignment")
		}
		if !errors.Is(err, ErrEOTransitionBlocked) {
			t.Fatalf("err = %v, want ErrEOTransitionBlocked", err)
		}
		if len(f.records.updated) != 0 {
			t.Error("the record was written despite the block")
		}
	})

	t.Run("an open assignment does NOT block the raise", func(t *testing.T) {
		f := newFixture(t)
		f.addChild("assignment_of", false)
		if _, err := f.run("raise", f.actor, models.EOTransitionInput{}); err != nil {
			t.Fatalf("blocks_parent_closure blocked a named transition: %v", err)
		}
	})

	t.Run("an open objection blocks the raise it names", func(t *testing.T) {
		f := newFixture(t)
		f.addChild("objection_to", false)
		_, err := f.run("raise", f.actor, models.EOTransitionInput{})
		if err == nil {
			t.Fatal("the raise succeeded with an open objection")
		}
		if !errors.Is(err, ErrEOTransitionBlocked) {
			t.Fatalf("err = %v, want ErrEOTransitionBlocked", err)
		}
	})

	t.Run("an open objection does NOT block closure", func(t *testing.T) {
		f := newFixture(t)
		f.addChild("objection_to", false)
		if _, err := f.run("close", f.actor, models.EOTransitionInput{Values: map[string]any{"closure_note": "done"}}); err != nil {
			t.Fatalf("blocks_parent_transition_key blocked closure: %v", err)
		}
	})

	t.Run("a closed child blocks nothing", func(t *testing.T) {
		f := newFixture(t)
		f.addChild("assignment_of", true)
		f.addChild("objection_to", true)
		if _, err := f.run("close", f.actor, models.EOTransitionInput{Values: map[string]any{"closure_note": "done"}}); err != nil {
			t.Fatalf("a closed child blocked closure: %v", err)
		}
	})

	t.Run("a link type that blocks neither blocks nothing", func(t *testing.T) {
		f := newFixture(t)
		f.types.linkTypes["note_on"] = &models.EOLinkType{Key: "note_on"}
		f.addChild("note_on", false)
		if _, err := f.run("close", f.actor, models.EOTransitionInput{Values: map[string]any{"closure_note": "done"}}); err != nil {
			t.Fatalf("an inert link type blocked closure: %v", err)
		}
	})
}

// ── field changes ───────────────────────────────────────────────────────────

func TestExecuteAppliesFieldChanges(t *testing.T) {
	t.Run("declared values reach their column and the values bag", func(t *testing.T) {
		f := eoNewTxFixture(t)
		f.rec.StateID = &f.draft.ID
		f.records.stored[f.rec.ID] = f.rec

		rec, err := f.run("submit", f.actor, models.EOTransitionInput{
			Values: map[string]any{"he_reason": "as directed", "priority": "high"},
		})
		if err != nil {
			t.Fatalf("submit: %v", err)
		}
		if rec.Priority != "high" {
			t.Errorf("priority = %q, want it written to its column", rec.Priority)
		}
		if got := eoTxValues(t, rec.Values)["he_reason"]; got != "as directed" {
			t.Errorf("he_reason = %v, want it in the values bag", got)
		}
		if len(f.records.updated) != 1 {
			t.Fatalf("records updated = %d, want 1", len(f.records.updated))
		}
		if f.records.updated[0].Priority != "high" {
			t.Error("the persisted record does not carry the field change")
		}
	})

	t.Run("a value the transition does not declare is refused", func(t *testing.T) {
		f := eoNewTxFixture(t)
		f.rec.StateID = &f.draft.ID
		f.records.stored[f.rec.ID] = f.rec

		_, err := f.run("submit", f.actor, models.EOTransitionInput{Values: map[string]any{"chair": uuid.New().String()}})
		if err == nil {
			t.Fatal("a field the transition does not declare was written")
		}
		if fields := eoTxFieldErrors(t, err); fields["chair"] == "" {
			t.Fatalf("field errors = %v, want chair refused", fields)
		}
	})

	t.Run("a required field change must be supplied", func(t *testing.T) {
		f := eoNewTxFixture(t)
		f.rec.StateID = &f.draft.ID
		f.records.stored[f.rec.ID] = f.rec
		f.transition("submit").FieldChanges[0].IsRequired = true

		if _, err := f.run("submit", f.actor, models.EOTransitionInput{}); err == nil {
			t.Fatal("a required field change was omitted and the transition ran")
		} else if fields := eoTxFieldErrors(t, err); fields["he_reason"] == "" {
			t.Fatalf("field errors = %v, want he_reason required", fields)
		}
		if _, err := f.run("submit", f.actor, models.EOTransitionInput{Values: map[string]any{"he_reason": "  "}}); err == nil {
			t.Fatal("blank space satisfied a required field change")
		}
	})
}

// ── history ─────────────────────────────────────────────────────────────────

func TestExecuteWritesHistory(t *testing.T) {
	f := eoNewTxFixture(t)
	f.rec.StateID = &f.draft.ID
	f.records.stored[f.rec.ID] = f.rec
	submit := f.transition("submit")

	rec, err := f.run("submit", f.actor, models.EOTransitionInput{
		Comment: "  sent for review  ",
		Values:  map[string]any{"he_reason": "as directed"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if rec.StateID == nil || *rec.StateID != f.review.ID {
		t.Fatalf("state = %v, want review %s", rec.StateID, f.review.ID)
	}

	if len(f.records.history) != 1 {
		t.Fatalf("history rows = %d, want 1", len(f.records.history))
	}
	h := f.records.history[0]
	if h.RecordID != f.rec.ID || h.RecordTypeID != f.rec.RecordTypeID {
		t.Errorf("history points at %s/%s, want %s/%s", h.RecordID, h.RecordTypeID, f.rec.ID, f.rec.RecordTypeID)
	}
	if h.TransitionID == nil || *h.TransitionID != submit.ID {
		t.Errorf("transition_id = %v, want %s", h.TransitionID, submit.ID)
	}
	if h.FromStateID == nil || *h.FromStateID != f.draft.ID {
		t.Errorf("from_state_id = %v, want draft %s", h.FromStateID, f.draft.ID)
	}
	if h.ToStateID == nil || *h.ToStateID != f.review.ID {
		t.Errorf("to_state_id = %v, want review %s", h.ToStateID, f.review.ID)
	}
	if h.PerformedByID == nil || *h.PerformedByID != f.actor.ID {
		t.Errorf("performed_by_id = %v, want the actor %s", h.PerformedByID, f.actor.ID)
	}
	if h.Comment != "sent for review" {
		t.Errorf("comment = %q, want it trimmed", h.Comment)
	}
	if h.IsSystemAction {
		t.Error("is_system_action = true, want false for an actor-driven transition")
	}
	newValues := eoTxValues(t, h.NewValues)
	if newValues["he_reason"] != "as directed" {
		t.Errorf("new_values = %v, want the field change journalled", newValues)
	}
	if _, ok := eoTxValues(t, h.OldValues)["he_reason"]; !ok {
		t.Errorf("old_values = %v, want the previous he_reason journalled", eoTxValues(t, h.OldValues))
	}
}

func TestExecuteDispatchesActionsAfterCommit(t *testing.T) {
	t.Run("dispatched once, after the commit, with the new state", func(t *testing.T) {
		f := eoNewTxFixture(t)
		if _, err := f.run("raise", f.actor, models.EOTransitionInput{}); err != nil {
			t.Fatalf("raise: %v", err)
		}
		if f.actions.calls != 1 {
			t.Fatalf("action dispatches = %d, want 1", f.actions.calls)
		}
		if !f.actions.sawCommitted {
			t.Error("actions were dispatched before the transaction committed")
		}
		if f.actions.sawState == nil || *f.actions.sawState != f.escalated.ID {
			t.Errorf("actions saw state %v, want the new state %s", f.actions.sawState, f.escalated.ID)
		}
		if f.actions.sawTransition != "raise" {
			t.Errorf("actions saw transition %q, want raise", f.actions.sawTransition)
		}
	})

	t.Run("a failing dispatch does not fail the transition", func(t *testing.T) {
		f := eoNewTxFixture(t)
		f.actions.err = errors.New("notification service is down")
		if _, err := f.run("raise", f.actor, models.EOTransitionInput{}); err != nil {
			t.Fatalf("a failed action dispatch failed the committed transition: %v", err)
		}
	})

	t.Run("nothing is dispatched when the transition is refused", func(t *testing.T) {
		f := eoNewTxFixture(t)
		f.transition("raise").Requirements = []models.TransitionRequirement{
			{RequirementType: "comment", IsMandatory: eoTxMandatory(true)},
		}
		if _, err := f.run("raise", f.actor, models.EOTransitionInput{}); err == nil {
			t.Fatal("the transition was expected to fail")
		}
		if f.actions.calls != 0 {
			t.Errorf("action dispatches = %d, want 0 after a refusal", f.actions.calls)
		}
	})
}

// ── concurrency ─────────────────────────────────────────────────────────────

func TestExecuteLocksTheRecordBeforeReadingIt(t *testing.T) {
	// A transaction alone does not serialise two transitions: under READ
	// COMMITTED both would read the same state, both would pass their
	// from-state check and both would commit. The lock has to be the first
	// statement in the transaction, before any check reads anything.
	f := eoNewTxFixture(t)
	if _, err := f.run("raise", f.actor, models.EOTransitionInput{}); err != nil {
		t.Fatalf("raise: %v", err)
	}
	if len(f.records.calls) == 0 || f.records.calls[0] != "LockForUpdate" {
		t.Fatalf("repository calls = %v, want LockForUpdate first", f.records.calls)
	}
	for _, c := range f.records.calls {
		if c == "GetByID" {
			t.Errorf("the executor read the record with an unlocked GetByID: %v", f.records.calls)
		}
	}
	if n := strings.Count(strings.Join(f.records.calls, " "), "LockForUpdate"); n != 1 {
		t.Errorf("LockForUpdate calls = %d, want 1", n)
	}
}

func TestExecuteFailsWhenTheRecordIsGone(t *testing.T) {
	f := eoNewTxFixture(t)
	delete(f.records.stored, f.rec.ID)
	if _, err := f.run("raise", f.actor, models.EOTransitionInput{}); !errors.Is(err, ErrEORecordNotFound) {
		t.Fatalf("err = %v, want ErrEORecordNotFound", err)
	}
}

// ── assignment resolution (spec §7.8) ───────────────────────────────────────

func TestExecuteAppliesTransitionAssignment(t *testing.T) {
	assignee := uuid.New()
	body := uuid.New()

	t.Run("the transition's user and body are written", func(t *testing.T) {
		f := eoNewTxFixture(t)
		raise := f.transition("raise")
		raise.AssignUserID = &assignee
		raise.AssignDepartmentID = &body

		rec, err := f.run("raise", f.actor, models.EOTransitionInput{})
		if err != nil {
			t.Fatalf("raise: %v", err)
		}
		if rec.AssignedUserID == nil || *rec.AssignedUserID != assignee {
			t.Errorf("assigned_user_id = %v, want %s", rec.AssignedUserID, assignee)
		}
		if rec.OwningBodyID == nil || *rec.OwningBodyID != body {
			t.Errorf("owning_body_id = %v, want %s", rec.OwningBodyID, body)
		}
		if len(f.records.updated) != 1 || f.records.updated[0].AssignedUserID == nil ||
			*f.records.updated[0].AssignedUserID != assignee {
			t.Error("the assignment was not persisted")
		}
		// The move of an assignee is a change to the record, so it belongs in
		// the journal beside the state change.
		if len(f.records.history) != 1 {
			t.Fatalf("history rows = %d, want 1", len(f.records.history))
		}
		if _, ok := eoTxValues(t, f.records.history[0].NewValues)["assigned_user_id"]; !ok {
			t.Errorf("new_values = %v, want the assignment journalled", eoTxValues(t, f.records.history[0].NewValues))
		}
	})

	t.Run("a transition carrying no assignment leaves the assignee alone", func(t *testing.T) {
		f := eoNewTxFixture(t)
		f.rec.AssignedUserID = &assignee
		f.records.stored[f.rec.ID] = f.rec

		rec, err := f.run("raise", f.actor, models.EOTransitionInput{})
		if err != nil {
			t.Fatalf("raise: %v", err)
		}
		if rec.AssignedUserID == nil || *rec.AssignedUserID != assignee {
			t.Errorf("assigned_user_id = %v, want the existing assignee %s kept", rec.AssignedUserID, assignee)
		}
	})

	t.Run("a declared field change overrides the static assignment", func(t *testing.T) {
		// The incident path applies the transition's assignment first and the
		// actor's field changes after, so the actor's choice wins. This is
		// also how manual selection is expressed in EO: declare
		// assigned_user_id as a field change and the choice goes through the
		// field rules like any other value.
		f := eoNewTxFixture(t)
		chosen := uuid.New()
		raise := f.transition("raise")
		raise.AssignUserID = &assignee
		raise.FieldChanges = []models.TransitionFieldChange{{FieldName: "assigned_user_id"}}
		f.types.defs = append(f.types.defs,
			eoTxField("assigned_user_id", models.EOKindUser, models.EORequiredNever, f.recordType.ID))

		rec, err := f.run("raise", f.actor, models.EOTransitionInput{
			Values: map[string]any{"assigned_user_id": chosen.String()},
		})
		if err != nil {
			t.Fatalf("raise: %v", err)
		}
		if rec.AssignedUserID == nil || *rec.AssignedUserID != chosen {
			t.Errorf("assigned_user_id = %v, want the actor's choice %s", rec.AssignedUserID, chosen)
		}
	})
}

// ── derived fields (spec §7.5) ──────────────────────────────────────────────

func TestExecuteRecomputesDerivedFields(t *testing.T) {
	// A transition field change is a declared dependency changing, so the
	// derived field must be recomputed before the row is written: otherwise a
	// roll-up goes stale at the exact moment the record moves.
	f := eoNewTxFixture(t)
	f.rec.StateID = &f.draft.ID
	f.records.stored[f.rec.ID] = f.rec

	weeks := eoTxField("weeks", models.EOKindNumber, models.EORequiredNever, f.recordType.ID)
	total := eoDerivedField("total_days", "weeks * 7", eoOwn("weeks"))
	total.ID, total.RecordTypeID, total.Kind = uuid.New(), f.recordType.ID, models.EOKindNumber
	f.types.defs = append(f.types.defs, weeks, total)
	f.transition("submit").FieldChanges = append(f.transition("submit").FieldChanges,
		models.TransitionFieldChange{FieldName: "weeks"})

	rec, err := f.run("submit", f.actor, models.EOTransitionInput{Values: map[string]any{"weeks": 3}})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got := eoTxValues(t, rec.Values)["total_days"]; got != 21.0 {
		t.Errorf("total_days = %v, want 21 recomputed from the value the transition wrote", got)
	}
	// What the database received is what matters: recomputing after the write
	// would leave the stored row stale until the next edit.
	if len(f.records.updated) != 1 {
		t.Fatalf("records updated = %d, want 1", len(f.records.updated))
	}
	if got := eoTxValues(t, f.records.updated[0].Values)["total_days"]; got != 21.0 {
		t.Errorf("persisted total_days = %v, want 21 — derived fields run before the write", got)
	}
}

func TestExecuteLockedFieldIsComparedAgainstThePreTransitionValue(t *testing.T) {
	// The static assignment writes assigned_user_id before the field changes
	// are validated. If the locked-field check then compares a submitted
	// value against the record AS MUTATED, the lock inverts: the value the
	// transition just wrote looks unchanged and is accepted, while the
	// record's genuine assignee looks like an edit and is refused. The
	// comparand has to be the pre-transition snapshot.
	original := uuid.New()
	static := uuid.New()

	build := func(t *testing.T) *eoTxFixture {
		t.Helper()
		f := eoNewTxFixture(t)
		f.rec.AssignedUserID = &original
		f.records.stored[f.rec.ID] = f.rec
		f.records.entered = map[uuid.UUID]bool{f.draft.ID: true, f.review.ID: true}

		locked := eoTxField("assigned_user_id", models.EOKindUser, models.EORequiredNever, f.recordType.ID)
		locked.IsLockedAfterStateID = &f.draft.ID
		f.types.defs = append(f.types.defs, locked)

		raise := f.transition("raise")
		raise.AssignUserID = &static
		raise.FieldChanges = []models.TransitionFieldChange{{FieldName: "assigned_user_id"}}
		return f
	}

	t.Run("the static value is still an edit and the lock refuses it", func(t *testing.T) {
		f := build(t)
		_, err := f.run("raise", f.actor, models.EOTransitionInput{
			Values: map[string]any{"assigned_user_id": static.String()},
		})
		if err == nil {
			t.Fatal("a locked field was changed, because the baseline had already been overwritten")
		}
		if fields := eoTxFieldErrors(t, err); fields["assigned_user_id"] == "" {
			t.Fatalf("field errors = %v, want assigned_user_id locked", fields)
		}
	})

	t.Run("re-submitting the record's own assignee is a no-op and is allowed", func(t *testing.T) {
		f := build(t)
		if _, err := f.run("raise", f.actor, models.EOTransitionInput{
			Values: map[string]any{"assigned_user_id": original.String()},
		}); err != nil {
			t.Fatalf("echoing the stored assignee was refused as a locked-field change: %v", err)
		}
	})

	t.Run("a genuine third value is still refused", func(t *testing.T) {
		f := build(t)
		if _, err := f.run("raise", f.actor, models.EOTransitionInput{
			Values: map[string]any{"assigned_user_id": uuid.New().String()},
		}); err == nil {
			t.Fatal("a locked assignee was reassigned to a new user")
		}
	})
}

func TestExecuteStaticAssignmentSatisfiesRequiredness(t *testing.T) {
	// The lock comparand must be the PRE-transition value, but the
	// requiredness check asks a different question of the same map: what will
	// this field hold once the transition is done. Hiding the static
	// assignment from BOTH is how "assigned_user_id is required" gets raised
	// against a transition whose entire job is to assign it.
	assignee := uuid.New()

	build := func(t *testing.T) *eoTxFixture {
		t.Helper()
		f := eoNewTxFixture(t)
		required := eoTxField("assigned_user_id", models.EOKindUser, models.EORequiredAtState, f.recordType.ID)
		required.RequiredAtStateIDs = datatypes.JSON(`["` + f.escalated.ID.String() + `"]`)
		f.types.defs = append(f.types.defs, required)
		return f
	}

	t.Run("the transition assigns it, so it is not missing", func(t *testing.T) {
		f := build(t)
		// Unassigned record, and the transition declares no field change:
		// the static assignment is the only thing that fills the field.
		f.transition("raise").AssignUserID = &assignee

		rec, err := f.run("raise", f.actor, models.EOTransitionInput{})
		if err != nil {
			t.Fatalf("a transition carrying the assignment was told the assignee is missing: %v", err)
		}
		if rec.AssignedUserID == nil || *rec.AssignedUserID != assignee {
			t.Errorf("assigned_user_id = %v, want %s", rec.AssignedUserID, assignee)
		}
	})

	t.Run("without the assignment it really is missing", func(t *testing.T) {
		f := build(t)
		_, err := f.run("raise", f.actor, models.EOTransitionInput{})
		if err == nil {
			t.Fatal("an unassigned record reached a state that requires an assignee")
		}
		if fields := eoTxFieldErrors(t, err); fields["assigned_user_id"] == "" {
			t.Fatalf("field errors = %v, want assigned_user_id required", fields)
		}
	})

	t.Run("a required-always field the transition assigns", func(t *testing.T) {
		f := eoNewTxFixture(t)
		body := uuid.New()
		f.types.defs = append(f.types.defs,
			eoTxField("owning_body_id", models.EOKindOrgUnit, models.EORequiredAlways, f.recordType.ID))
		f.transition("raise").AssignDepartmentID = &body

		rec, err := f.run("raise", f.actor, models.EOTransitionInput{})
		if err != nil {
			t.Fatalf("a transition carrying the owning body was told it is missing: %v", err)
		}
		if rec.OwningBodyID == nil || *rec.OwningBodyID != body {
			t.Errorf("owning_body_id = %v, want %s", rec.OwningBodyID, body)
		}
	})
}

func TestExecuteRecomputesDerivedThroughTheTransaction(t *testing.T) {
	// A roll-up's child read must go through the transaction's own handle.
	// Taken on the service's pooled repository it would acquire a SECOND
	// connection while this transaction holds one plus a FOR UPDATE row
	// lock, and database/sql waits for a free connection indefinitely unless
	// the context carries a deadline — lock_timeout does not bound it.
	f := eoNewTxFixture(t)
	f.rec.StateID = &f.draft.ID
	f.records.stored[f.rec.ID] = f.rec

	stage := &models.EORecordType{ID: uuid.New(), Key: "eo_stage", Name: "Stage", IsActive: true}
	f.types.types[stage.ID] = stage
	f.records.children[f.rec.ID] = []models.EORecord{
		{ID: uuid.New(), RecordTypeID: stage.ID, Values: datatypes.JSON(`{"weeks":2}`)},
		{ID: uuid.New(), RecordTypeID: stage.ID, Values: datatypes.JSON(`{"weeks":3}`)},
	}

	total := eoDerivedField("total_weeks", "children_eo_stage_sum_weeks",
		[]map[string]any{{"scope": "child", "type": "eo_stage", "field": "weeks", "aggregates": []string{"sum"}}})
	total.ID, total.RecordTypeID, total.Kind = uuid.New(), f.recordType.ID, models.EOKindNumber

	// An ancestor-scoped roll-up walks upwards through GetAnyByID, which is
	// the other half of the same deadlock class.
	programme := &models.EORecordType{ID: uuid.New(), Key: "eo_programme", Name: "Programme", IsActive: true}
	f.types.types[programme.ID] = programme
	parent := &models.EORecord{
		ID: uuid.New(), RecordTypeID: programme.ID, Reference: "PROG-1",
		Values: datatypes.JSON(`{"code":"P-7"}`),
	}
	f.records.stored[parent.ID] = parent
	f.rec.ParentRecordID = &parent.ID
	f.records.stored[f.rec.ID] = f.rec

	inherited := eoDerivedField("programme_code", "ancestor_eo_programme_code",
		[]map[string]any{{"scope": "ancestor", "type": "eo_programme", "field": "code"}})
	inherited.ID, inherited.RecordTypeID, inherited.Kind = uuid.New(), f.recordType.ID, models.EOKindText

	f.types.defs = append(f.types.defs, total, inherited)

	rec, err := f.run("submit", f.actor, models.EOTransitionInput{})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	values := eoTxValues(t, rec.Values)
	if values["total_weeks"] != 5.0 {
		t.Fatalf("total_weeks = %v, want 5 rolled up from the children", values["total_weeks"])
	}
	if values["programme_code"] != "P-7" {
		t.Fatalf("programme_code = %v, want P-7 read from the ancestor", values["programme_code"])
	}
	if f.records.childrenTotal == 0 || f.records.getAnyTotal == 0 {
		t.Fatalf("child reads = %d, ancestor reads = %d; the roll-ups did not run",
			f.records.childrenTotal, f.records.getAnyTotal)
	}
	if f.records.childrenInTx != f.records.childrenTotal {
		t.Errorf("child reads: %d of %d went through the transaction — the rest took a second pooled connection while this one holds a row lock",
			f.records.childrenInTx, f.records.childrenTotal)
	}
	if f.records.getAnyInTx != f.records.getAnyTotal {
		t.Errorf("ancestor reads: %d of %d went through the transaction — the rest took a second pooled connection while this one holds a row lock",
			f.records.getAnyInTx, f.records.getAnyTotal)
	}
}

func TestExecuteRefusesUnsupportedAssignmentConfiguration(t *testing.T) {
	// A flag the engine cannot honour must not pass quietly: a transition
	// seeded with auto_match_user assigning nobody and saying nothing is the
	// same silent no-op as an unevaluated requirement type.
	for _, tc := range []struct {
		name string
		set  func(*models.WorkflowTransition)
	}{
		{"auto_match_user", func(tr *models.WorkflowTransition) { tr.AutoMatchUser = true }},
		{"auto_detect_department", func(tr *models.WorkflowTransition) { tr.AutoDetectDepartment = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := eoNewTxFixture(t)
			tc.set(f.transition("raise"))
			_, err := f.run("raise", f.actor, models.EOTransitionInput{})
			if err == nil {
				t.Fatalf("a transition configured with %s ran and assigned nobody", tc.name)
			}
			if !strings.Contains(err.Error(), tc.name) {
				t.Errorf("err = %v, want it to name %s", err, tc.name)
			}
			if len(f.records.updated) != 0 {
				t.Error("the record was written despite the refusal")
			}
		})
	}

	t.Run("manual_select_user is supported and does not refuse", func(t *testing.T) {
		// It needs no engine code: the transition declares assigned_user_id
		// as a field change and the actor's choice goes through the field
		// rules like any other value.
		f := eoNewTxFixture(t)
		chosen := uuid.New()
		raise := f.transition("raise")
		raise.ManualSelectUser = true
		raise.FieldChanges = []models.TransitionFieldChange{{FieldName: "assigned_user_id"}}
		f.types.defs = append(f.types.defs,
			eoTxField("assigned_user_id", models.EOKindUser, models.EORequiredNever, f.recordType.ID))

		rec, err := f.run("raise", f.actor, models.EOTransitionInput{
			Values: map[string]any{"assigned_user_id": chosen.String()},
		})
		if err != nil {
			t.Fatalf("manual selection was refused: %v", err)
		}
		if rec.AssignedUserID == nil || *rec.AssignedUserID != chosen {
			t.Errorf("assigned_user_id = %v, want the actor's choice %s", rec.AssignedUserID, chosen)
		}
	})
}

// ── terminal states ─────────────────────────────────────────────────────────

func TestExecuteSetsClosedAtOnTerminalState(t *testing.T) {
	t.Run("a terminal target stamps closed_at", func(t *testing.T) {
		f := eoNewTxFixture(t)
		rec, err := f.run("close", f.actor, models.EOTransitionInput{Values: map[string]any{"closure_note": "done"}})
		if err != nil {
			t.Fatalf("close: %v", err)
		}
		if rec.ClosedAt == nil {
			t.Fatal("closed_at is nil after moving to a terminal state")
		}
		if len(f.records.updated) != 1 || f.records.updated[0].ClosedAt == nil {
			t.Error("closed_at was not persisted")
		}
	})

	t.Run("a non-terminal target leaves closed_at nil", func(t *testing.T) {
		f := eoNewTxFixture(t)
		rec, err := f.run("raise", f.actor, models.EOTransitionInput{})
		if err != nil {
			t.Fatalf("raise: %v", err)
		}
		if rec.ClosedAt != nil {
			t.Errorf("closed_at = %v, want nil for a normal state", rec.ClosedAt)
		}
	})

	t.Run("leaving a terminal state clears closed_at", func(t *testing.T) {
		f := eoNewTxFixture(t)
		closedAt := eoTxNow()
		f.rec.StateID = &f.closed.ID
		f.rec.ClosedAt = &closedAt
		f.records.stored[f.rec.ID] = f.rec

		rec, err := f.run("reopen", f.actor, models.EOTransitionInput{})
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		if rec.ClosedAt != nil {
			t.Errorf("closed_at = %v, want nil after a reopen", rec.ClosedAt)
		}
	})
}

// ── available transitions ───────────────────────────────────────────────────

func TestExecuteAvailableTransitionsFilterByStateAndActor(t *testing.T) {
	t.Run("only the transitions out of the current state", func(t *testing.T) {
		f := eoNewTxFixture(t)
		got, err := f.svc.AvailableTransitions(context.Background(), f.rec, f.actor)
		if err != nil {
			t.Fatalf("available: %v", err)
		}
		if codes := eoTxCodes(got); strings.Join(codes, ",") != "close,raise" {
			t.Errorf("available = %v, want [close raise]", codes)
		}
	})

	t.Run("a transition the actor may not run is not offered", func(t *testing.T) {
		f := eoNewTxFixture(t)
		f.transition("close").AllowedRoles = []models.Role{{ID: uuid.New()}}
		f.transition("raise").EOActorRules = eoTxActorRules(t, TransitionActorRule{FieldKey: "chair", From: EOActorFromSelf})

		got, err := f.svc.AvailableTransitions(context.Background(), f.rec, f.actor)
		if err != nil {
			t.Fatalf("available: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("available = %v, want none", eoTxCodes(got))
		}
	})

	t.Run("an inactive transition is not offered", func(t *testing.T) {
		f := eoNewTxFixture(t)
		f.transition("close").IsActive = false
		got, err := f.svc.AvailableTransitions(context.Background(), f.rec, f.actor)
		if err != nil {
			t.Fatalf("available: %v", err)
		}
		if codes := eoTxCodes(got); strings.Join(codes, ",") != "raise" {
			t.Errorf("available = %v, want [raise]", codes)
		}
	})
}

// ── helpers ─────────────────────────────────────────────────────────────────

func eoTxCodes(trs []models.WorkflowTransition) []string {
	out := make([]string, 0, len(trs))
	for _, tr := range trs {
		out = append(out, tr.Code)
	}
	sort.Strings(out)
	return out
}

func eoTxValues(t *testing.T, raw datatypes.JSON) map[string]any {
	t.Helper()
	out := map[string]any{}
	if len(raw) == 0 {
		return out
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode values %s: %v", raw, err)
	}
	return out
}

func eoTxFieldErrors(t *testing.T, err error) map[string]string {
	t.Helper()
	var ve *EOValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v (%T), want an *EOValidationError", err, err)
	}
	return ve.Fields
}
