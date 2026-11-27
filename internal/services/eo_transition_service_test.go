package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
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
	// txTimers is the transaction-bound timer repository Transaction hands its
	// closure. Nil unless a test wires SLA clocks.
	txTimers repository.EOTimerRepository
	// recordsOfTypeTotal / recordsOfTypeInTx do the same for RecordsOfType,
	// the type-scoped read decisions_base checks a draft against: it loads a
	// whole record type, so taking it on a pooled connection inside the
	// transaction would be the largest of these reads, not the smallest.
	recordsOfTypeTotal int
	recordsOfTypeInTx  int
	// getAnyTotal / getAnyInTx do the same for GetAnyByID, which is how an
	// ancestor-scoped roll-up walks upwards — the other half of the same
	// deadlock class.
	getAnyTotal int
	getAnyInTx  int
	// linksTotal / linksInTx do the same for LinksFrom, which is how a
	// capability reads a record's typed links (Task B1's read seam).
	linksTotal int
	linksInTx  int
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

func (f *eoTxRecordRepo) RecordsOfType(_ context.Context, recordTypeID uuid.UUID) ([]models.EORecord, error) {
	f.calls = append(f.calls, "RecordsOfType")
	f.recordsOfTypeTotal++
	var out []models.EORecord
	for _, r := range f.stored {
		if r.RecordTypeID == recordTypeID {
			out = append(out, *r)
		}
	}
	return out, nil
}

func (f *eoTxRecordRepo) LinksFrom(_ context.Context, recordID uuid.UUID) ([]models.EORecordLink, error) {
	f.linksTotal++
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

func (b *eoTxBoundRepo) RecordsOfType(ctx context.Context, recordTypeID uuid.UUID) ([]models.EORecord, error) {
	b.eoTxRecordRepo.recordsOfTypeInTx++
	return b.eoTxRecordRepo.RecordsOfType(ctx, recordTypeID)
}

func (b *eoTxBoundRepo) LinksFrom(ctx context.Context, recordID uuid.UUID) ([]models.EORecordLink, error) {
	b.eoTxRecordRepo.linksInTx++
	return b.eoTxRecordRepo.LinksFrom(ctx, recordID)
}

func (f *eoTxRecordRepo) Transaction(_ context.Context, fn func(repository.EORecordRepository, repository.EORecordTypeRepository, repository.EOTimerRepository) error) error {
	if err := fn(&eoTxBoundRepo{f}, f.txTypes, f.txTimers); err != nil {
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

// eoTxWorkflowPreloaded is f.wf with its Transitions preloaded the way the
// repository preloads them — Transitions but NOT Transitions.FieldChanges. A
// fake more generous than the repository hid a guard that never ran in
// production.
func (f *eoTxWorkflowRepo) eoTxWorkflowPreloaded() models.Workflow {
	wf := f.wf
	wf.Transitions = nil
	for _, tr := range f.transitions {
		cp := *tr
		cp.FieldChanges = nil
		wf.Transitions = append(wf.Transitions, cp)
	}
	sort.Slice(wf.Transitions, func(i, j int) bool { return wf.Transitions[i].Code < wf.Transitions[j].Code })
	return wf
}

func (f *eoTxWorkflowRepo) List(_ context.Context, _ bool) ([]models.Workflow, error) {
	return []models.Workflow{f.eoTxWorkflowPreloaded()}, nil
}

// ListByRecordTypeID is what ResolveWorkflowForType calls. It filters on the
// bound record type as the repository's WHERE clause does, so a workflow bound
// to another type is absent rather than merely skipped in Go.
func (f *eoTxWorkflowRepo) ListByRecordTypeID(_ context.Context, typeID uuid.UUID) ([]models.Workflow, error) {
	wf := f.eoTxWorkflowPreloaded()
	if wf.RecordTypeID == nil || *wf.RecordTypeID != typeID {
		return nil, nil
	}
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
		// Every type inherits `attachments` from the EO base type
		// (seed_eo_platform.go:303), and it is where a transition's
		// attachments are filed.
		eoTxField("attachments", models.EOKindMultiFile, models.EORequiredNever, f.recordType.ID),
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
		if _, err := f.run("raise", f.actor, models.EOTransitionInput{Attachments: []string{uuid.NewString()}}); err != nil {
			t.Fatalf("one attachment was supplied and still refused: %v", err)
		}
	})

	// eoAttachmentIDs is what makes this true, and nothing else tested it:
	// the requirement counts the ids that survive the blank filter, not the
	// entries of the list the request sent. Without it an empty string — what
	// a web form posts for a file input nobody filled — satisfies a mandatory
	// attachment, and the transition commits with its control unmet and
	// nothing filed.
	//
	// Mutations that make it fail: in eoCheckRequirements, count
	// len(input.Attachments) instead of len(ids) — the two single-blank cases
	// then transition with the control unmet; or, in eoAttachmentIDs, drop
	// the `strings.TrimSpace(id) == ""` skip — the last case then counts the
	// blank as a second attachment and trips the single-attachment limit.
	// (Under that second mutation the single-blank cases still refuse, but
	// for another reason entirely: the blank reaches the file-shape check as
	// a filed id. The last case is what pins the filter itself.)
	t.Run("a blank entry is not an attachment", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			in   []string
		}{
			{"one empty string", []string{""}},
			{"whitespace only", []string{"   "}},
			{"several blanks", []string{"", "  ", "\t"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := eoNewTxFixture(t)
				f.transition("raise").Requirements = []models.TransitionRequirement{
					{RequirementType: "attachment", IsMandatory: eoTxMandatory(true)},
				}
				if _, err := f.run("raise", f.actor, models.EOTransitionInput{Attachments: tc.in}); err == nil {
					t.Fatalf("%#v satisfied a mandatory attachment", tc.in)
				}
			})
		}

		// The other side of the same filter: a blank beside a real id is not
		// a second attachment, so it must not trip the single-attachment
		// limit either.
		t.Run("a blank beside an id is not a second attachment", func(t *testing.T) {
			f := eoNewTxFixture(t)
			f.transition("raise").Requirements = []models.TransitionRequirement{
				{RequirementType: "attachment", IsMandatory: eoTxMandatory(true)},
			}
			if _, err := f.run("raise", f.actor, models.EOTransitionInput{Attachments: []string{"", uuid.NewString()}}); err != nil {
				t.Fatalf("one id and one blank were refused as several attachments: %v", err)
			}
		})
	})

	t.Run("a single-attachment requirement refuses several", func(t *testing.T) {
		f := eoNewTxFixture(t)
		f.transition("raise").Requirements = []models.TransitionRequirement{
			{RequirementType: "attachment", IsMandatory: eoTxMandatory(true)},
		}
		two := []string{uuid.NewString(), uuid.NewString()}
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
				if _, err := f.run("raise", f.actor, models.EOTransitionInput{Comment: "x", Attachments: []string{uuid.NewString()}}); err == nil {
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

// ── capability timer bridge ─────────────────────────────────────────────────

// eoBridgeCap is a capability whose only live hook is Timers.
type eoBridgeCap struct {
	Capability
	key    string
	timers []models.EOTimer
	err    error
	calls  int
	// what Timers was handed
	seenState *uuid.UUID
	seenCfg   datatypes.JSON
}

func (c *eoBridgeCap) Key() string { return c.key }

// The hooks Execute calls around Timers. Everything else stays on the nil
// embedded interface, so a hook the bridge starts using panics loudly.
func (c *eoBridgeCap) ReservedKeys(datatypes.JSON) ([]string, error)           { return nil, nil }
func (c *eoBridgeCap) ValidateWorkflow(datatypes.JSON, *models.Workflow) error { return nil }
func (c *eoBridgeCap) Validate(context.Context, EOCapabilityReader, *models.EORecord, datatypes.JSON) (map[string]string, error) {
	return nil, nil
}
func (c *eoBridgeCap) Derive(context.Context, EOCapabilityReader, *models.EORecord, datatypes.JSON) (map[string]any, error) {
	return nil, nil
}
func (c *eoBridgeCap) OnTransition(context.Context, EOCapabilityReader, *models.EORecord, *models.WorkflowTransition, datatypes.JSON) error {
	return nil
}

func (c *eoBridgeCap) Timers(_ context.Context, _ EOCapabilityReader, rec *models.EORecord, cfg datatypes.JSON) ([]models.EOTimer, error) {
	c.calls++
	c.seenState, c.seenCfg = rec.StateID, cfg
	return c.timers, c.err
}

// eoBridgeTimers is the transaction-bound timer handle. ops keeps cancel and
// schedule in the order they happened.
type eoBridgeTimers struct {
	repository.EOTimerRepository
	ops         []string
	scheduled   []models.EOTimer
	cancelledID []uuid.UUID
}

func (f *eoBridgeTimers) CancelForRecordByCapability(_ context.Context, id uuid.UUID, key string) error {
	f.ops = append(f.ops, "cancel:"+key)
	f.cancelledID = append(f.cancelledID, id)
	return nil
}

func (f *eoBridgeTimers) Schedule(_ context.Context, t *models.EOTimer) error {
	f.ops = append(f.ops, "schedule:"+t.CapabilityKey)
	f.scheduled = append(f.scheduled, *t)
	return nil
}

const eoBridgeCfg = `{"after_hours":4}`

func eoBridgeFixture(t *testing.T, caps ...Capability) (*eoTxFixture, *eoBridgeTimers) {
	t.Helper()
	f := eoNewTxFixture(t)
	timers := &eoBridgeTimers{}
	f.records.txTimers = timers
	f.svc.(*eoTransitionService).caps = func(*models.EORecordType) ([]EOEnabledCapability, error) {
		out := make([]EOEnabledCapability, len(caps))
		for i, c := range caps {
			out[i] = EOEnabledCapability{Capability: c, Config: datatypes.JSON(eoBridgeCfg)}
		}
		return out, nil
	}
	return f, timers
}

func TestEoTransitionSchedulesCapabilityTimers(t *testing.T) {
	fireAt := time.Date(2026, 4, 1, 8, 0, 0, 0, time.UTC)
	c := &eoBridgeCap{key: "eo.bridge", timers: []models.EOTimer{{FireAt: fireAt, Payload: datatypes.JSON(`{"n":1}`)}}}
	f, timers := eoBridgeFixture(t, c)
	if _, err := f.run("raise", f.actor, models.EOTransitionInput{}); err != nil {
		t.Fatalf("raise: %v", err)
	}
	if len(timers.scheduled) != 1 {
		t.Fatalf("scheduled %d timers, want 1", len(timers.scheduled))
	}
	got := timers.scheduled[0]
	if got.RecordID != f.rec.ID {
		t.Errorf("RecordID = %v, want %v", got.RecordID, f.rec.ID)
	}
	if got.RecordTypeID != f.recordType.ID {
		t.Errorf("RecordTypeID = %v, want %v", got.RecordTypeID, f.recordType.ID)
	}
	if got.CapabilityKey != "eo.bridge" {
		t.Errorf("CapabilityKey = %q, want eo.bridge", got.CapabilityKey)
	}
	if c.seenState == nil || *c.seenState != f.escalated.ID {
		t.Errorf("Timers saw state %v, want the state entered (%v)", c.seenState, f.escalated.ID)
	}
	if string(c.seenCfg) != eoBridgeCfg {
		t.Errorf("Timers saw config %s, want %s", c.seenCfg, eoBridgeCfg)
	}
	if !got.FireAt.Equal(fireAt) || string(got.Payload) != `{"n":1}` {
		t.Errorf("FireAt/Payload not carried through: %v %s", got.FireAt, got.Payload)
	}
}

func TestEoTransitionRejectsForeignCapabilityKey(t *testing.T) {
	c := &eoBridgeCap{key: "eo.mine", timers: []models.EOTimer{{FireAt: time.Now(), CapabilityKey: "eo.other"}}}
	f, timers := eoBridgeFixture(t, c)
	if _, err := f.run("raise", f.actor, models.EOTransitionInput{}); err == nil {
		t.Fatal("a timer keyed to another capability was accepted")
	}
	if len(timers.scheduled) != 0 {
		t.Errorf("scheduled %d timers, want none", len(timers.scheduled))
	}
	if f.records.committed {
		t.Error("the transaction committed")
	}
}

func TestEoTransitionCancelsLeftStateCapabilityTimers(t *testing.T) {
	a := &eoBridgeCap{key: "eo.a", timers: []models.EOTimer{{FireAt: time.Now()}}}
	b := &eoBridgeCap{key: "eo.b"}
	f, timers := eoBridgeFixture(t, a, b)
	if _, err := f.run("raise", f.actor, models.EOTransitionInput{}); err != nil {
		t.Fatalf("raise: %v", err)
	}
	want := []string{"cancel:eo.a", "schedule:eo.a", "cancel:eo.b"}
	if len(timers.ops) != len(want) {
		t.Fatalf("ops = %v, want %v", timers.ops, want)
	}
	for i := range want {
		if timers.ops[i] != want[i] {
			t.Errorf("ops[%d] = %q, want %q (all: %v)", i, timers.ops[i], want[i], timers.ops)
		}
	}
	for _, id := range timers.cancelledID {
		if id != f.rec.ID {
			t.Errorf("cancelled for record %v, want %v", id, f.rec.ID)
		}
	}
}

func TestEoTransitionCapabilityTimersAreTxBound(t *testing.T) {
	c := &eoBridgeCap{key: "eo.bridge", timers: []models.EOTimer{{FireAt: time.Now()}}}
	f, timers := eoBridgeFixture(t, c)
	// The service holds no timer source of its own: a bypass would have
	// nowhere to go but a nil-pointer panic.
	if _, err := f.run("raise", f.actor, models.EOTransitionInput{}); err != nil {
		t.Fatalf("raise: %v", err)
	}
	if len(timers.scheduled) != 1 || len(timers.ops) != 2 {
		t.Errorf("tx handle saw ops %v, want one cancel and one schedule", timers.ops)
	}
}

func TestEoTransitionCapabilityTimerErrorAbortsTransition(t *testing.T) {
	boom := errors.New("timers broke")
	c := &eoBridgeCap{key: "eo.bridge", err: boom}
	f, timers := eoBridgeFixture(t, c)
	rec, err := f.run("raise", f.actor, models.EOTransitionInput{})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap %v", err, boom)
	}
	if rec != nil {
		t.Error("a record was returned from an aborted transition")
	}
	if f.records.committed {
		t.Error("the transaction committed, so the record would have moved")
	}
	if len(timers.scheduled) != 0 {
		t.Errorf("scheduled %d timers", len(timers.scheduled))
	}
}

func TestEoTransitionRejectsForeignRecordIdentity(t *testing.T) {
	cases := []struct {
		name  string
		timer models.EOTimer
	}{
		{"foreign record id", models.EOTimer{FireAt: time.Now(), RecordID: uuid.New()}},
		{"foreign record type id", models.EOTimer{FireAt: time.Now(), RecordTypeID: uuid.New()}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := &eoBridgeCap{key: "eo.mine", timers: []models.EOTimer{tc.timer}}
			f, timers := eoBridgeFixture(t, c)
			_, err := f.run("raise", f.actor, models.EOTransitionInput{})
			if err == nil || !strings.Contains(err.Error(), "eo.mine") {
				t.Fatalf("err = %v, want one naming the capability", err)
			}
			if len(timers.scheduled) != 0 {
				t.Errorf("scheduled %d timers, want none", len(timers.scheduled))
			}
		})
	}
}

func TestEoTransitionScheduleErrorNamesTheCapability(t *testing.T) {
	c := &eoBridgeCap{key: "eo.mine", timers: []models.EOTimer{{FireAt: time.Now()}}}
	f, timers := eoBridgeFixture(t, c)
	boom := errors.New("fire_at required")
	f.records.txTimers = &eoBridgeFailingTimers{eoBridgeTimers: timers, err: boom}
	_, err := f.run("raise", f.actor, models.EOTransitionInput{})
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "eo.mine") {
		t.Errorf("err = %v, want %v naming eo.mine", err, boom)
	}
}

type eoBridgeFailingTimers struct {
	*eoBridgeTimers
	err error
}

func (f *eoBridgeFailingTimers) Schedule(context.Context, *models.EOTimer) error { return f.err }

// ── transition attachments are filed, not dropped ───────────────────────────

// eoTxAttachments decodes the record's attachments values key as a list of
// ids, so a test reads what was actually persisted rather than what was
// passed in.
func eoTxAttachments(t *testing.T, rec models.EORecord) []string {
	t.Helper()
	bag := map[string]any{}
	if len(rec.Values) > 0 {
		if err := json.Unmarshal(rec.Values, &bag); err != nil {
			t.Fatalf("decode values: %v", err)
		}
	}
	raw, ok := bag["attachments"]
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("attachments = %#v, want a list", raw)
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("attachment %#v is not an id", v)
		}
		out = append(out, s)
	}
	return out
}

func TestTransitionAttachmentsArePersisted(t *testing.T) {
	a, b := uuid.NewString(), uuid.NewString()

	t.Run("the ids reach the record", func(t *testing.T) {
		f := eoNewTxFixture(t)
		if _, err := f.run("raise", f.actor, models.EOTransitionInput{Attachments: []string{a, b}}); err != nil {
			t.Fatalf("transition refused: %v", err)
		}
		got := eoTxAttachments(t, f.records.updated[len(f.records.updated)-1])
		if strings.Join(got, ",") != a+","+b {
			t.Fatalf("attachments = %v, want [%s %s] filed in the record, not dropped", got, a, b)
		}
	})

	t.Run("the history says so", func(t *testing.T) {
		f := eoNewTxFixture(t)
		if _, err := f.run("raise", f.actor, models.EOTransitionInput{Attachments: []string{a}}); err != nil {
			t.Fatalf("transition refused: %v", err)
		}
		h := f.records.history[len(f.records.history)-1]
		if !strings.Contains(string(h.NewValues), a) {
			t.Fatalf("history new_values = %s, want the attachment id", h.NewValues)
		}
	})

	t.Run("a second transition adds to the file rather than replacing it", func(t *testing.T) {
		f := eoNewTxFixture(t)
		if _, err := f.run("raise", f.actor, models.EOTransitionInput{Attachments: []string{a}}); err != nil {
			t.Fatalf("first transition refused: %v", err)
		}
		// The record is now in escalated; move it back with the second id.
		f.addTransition(&models.WorkflowTransition{
			Code: "settle", Name: "Settle", FromStateID: f.escalated.ID, ToStateID: f.review.ID, IsActive: true,
		})
		f.rec = f.records.stored[f.rec.ID]
		if _, err := f.run("settle", f.actor, models.EOTransitionInput{Attachments: []string{a, b}}); err != nil {
			t.Fatalf("second transition refused: %v", err)
		}
		got := eoTxAttachments(t, f.records.updated[len(f.records.updated)-1])
		if strings.Join(got, ",") != a+","+b {
			t.Fatalf("attachments = %v, want [%s %s]: the first id kept and the second added once", got, a, b)
		}
	})

	t.Run("a value that is not a Documenta id is refused", func(t *testing.T) {
		f := eoNewTxFixture(t)
		_, err := f.run("raise", f.actor, models.EOTransitionInput{Attachments: []string{"not-a-documenta-id-at-all"}})
		if err == nil {
			t.Fatal("a transition filed an attachment that is not a Documenta file id")
		}
		var invalid *EOValidationError
		if !errors.As(err, &invalid) {
			t.Fatalf("err = %v (%T), want a validation error so the handler answers 400", err, err)
		}
		if len(f.records.updated) != 0 {
			t.Fatalf("the record was written anyway: %v", f.records.updated)
		}
	})

	t.Run("nowhere to file them is refused, not dropped", func(t *testing.T) {
		f := eoNewTxFixture(t)
		defs := f.types.defs[:0:0]
		for _, d := range f.types.defs {
			if d.Key != "attachments" {
				defs = append(defs, d)
			}
		}
		f.types.defs = defs
		if _, err := f.run("raise", f.actor, models.EOTransitionInput{Attachments: []string{a}}); err == nil {
			t.Fatal("a record type with no attachments field accepted attachments and dropped them")
		}
	})

	t.Run("a single file field takes one and refuses a second", func(t *testing.T) {
		f := eoNewTxFixture(t)
		for i := range f.types.defs {
			if f.types.defs[i].Key == "attachments" {
				f.types.defs[i].Kind = models.EOKindFile
			}
		}
		if _, err := f.run("raise", f.actor, models.EOTransitionInput{Attachments: []string{a}}); err != nil {
			t.Fatalf("one file was refused: %v", err)
		}
		got := f.records.updated[len(f.records.updated)-1]
		bag := map[string]any{}
		if err := json.Unmarshal(got.Values, &bag); err != nil {
			t.Fatalf("decode values: %v", err)
		}
		if bag["attachments"] != a {
			t.Fatalf("attachments = %#v, want the single id %s", bag["attachments"], a)
		}
		f.addTransition(&models.WorkflowTransition{
			Code: "settle", Name: "Settle", FromStateID: f.escalated.ID, ToStateID: f.review.ID, IsActive: true,
		})
		f.rec = f.records.stored[f.rec.ID]
		if _, err := f.run("settle", f.actor, models.EOTransitionInput{Attachments: []string{b}}); err == nil {
			t.Fatal("a second file overwrote the one a file field holds")
		}
	})

	t.Run("a value stored before the shape check is not deleted", func(t *testing.T) {
		f := eoNewTxFixture(t)
		f.setValues(t, map[string]any{"attachments": []any{"not-a-documenta-id-at-all", float64(12345)}})
		if _, err := f.run("raise", f.actor, models.EOTransitionInput{Attachments: []string{a}}); err != nil {
			t.Fatalf("transition refused: %v", err)
		}
		bag := map[string]any{}
		if err := json.Unmarshal(f.records.updated[len(f.records.updated)-1].Values, &bag); err != nil {
			t.Fatalf("decode values: %v", err)
		}
		list, ok := bag["attachments"].([]any)
		if !ok || len(list) != 3 || list[0] != "not-a-documenta-id-at-all" || list[2] != a {
			t.Fatalf("attachments = %#v, want the two stored values kept and %s appended", bag["attachments"], a)
		}
	})

	t.Run("no attachments writes nothing", func(t *testing.T) {
		f := eoNewTxFixture(t)
		if _, err := f.run("raise", f.actor, models.EOTransitionInput{}); err != nil {
			t.Fatalf("transition refused: %v", err)
		}
		if got := eoTxAttachments(t, f.records.updated[len(f.records.updated)-1]); got != nil {
			t.Fatalf("attachments = %v, want the key absent", got)
		}
	})
}

// eoTxAttachmentsDef returns the fixture's attachments field definition for a
// test to narrow, the way a record type's configuration narrows it.
func eoTxAttachmentsDef(f *eoTxFixture) *models.EOFieldDef {
	for i := range f.types.defs {
		if f.types.defs[i].Key == "attachments" {
			return &f.types.defs[i]
		}
	}
	panic("the fixture has no attachments field")
}

// The transition path files attachments into an ordinary field, so the rules
// that field carries apply to it. An engine write does not outrank the
// configuration: a guard that cannot fire in the case that matters is no
// guard, so contradictory configuration fails loudly at the point of use.
func TestTransitionAttachmentsObeyTheFieldRules(t *testing.T) {
	a, b := uuid.NewString(), uuid.NewString()

	t.Run("a locked field refuses a new attachment", func(t *testing.T) {
		f := eoNewTxFixture(t)
		eoTxAttachmentsDef(f).IsLockedAfterStateID = &f.review.ID
		f.records.entered = map[uuid.UUID]bool{f.review.ID: true}
		f.setValues(t, map[string]any{"attachments": []any{a}})

		_, err := f.run("raise", f.actor, models.EOTransitionInput{Attachments: []string{b}})
		if err == nil {
			t.Fatal("a transition filed an attachment into a field that is locked")
		}
		if !strings.Contains(err.Error(), "attachments") {
			t.Errorf("err = %v, want the conflicting field named", err)
		}
		if len(f.records.updated) != 0 {
			t.Fatalf("the record was written anyway: %v", f.records.updated)
		}
	})

	t.Run("a locked field accepts ids it already holds", func(t *testing.T) {
		// Nothing changes, so the lock has nothing to refuse — the same
		// allowance ValidateRecordValues makes for an unchanged echo.
		f := eoNewTxFixture(t)
		eoTxAttachmentsDef(f).IsLockedAfterStateID = &f.review.ID
		f.records.entered = map[uuid.UUID]bool{f.review.ID: true}
		f.setValues(t, map[string]any{"attachments": []any{a}})

		if _, err := f.run("raise", f.actor, models.EOTransitionInput{Attachments: []string{a}}); err != nil {
			t.Fatalf("an unchanged attachment tripped the lock: %v", err)
		}
		if got := eoTxAttachments(t, f.records.updated[len(f.records.updated)-1]); strings.Join(got, ",") != a {
			t.Fatalf("attachments = %v, want the stored id untouched", got)
		}
	})

	t.Run("a field the system fills refuses attachments", func(t *testing.T) {
		for _, prov := range []string{models.EOProvenanceDerived, models.EOProvenanceAuto} {
			t.Run(prov, func(t *testing.T) {
				f := eoNewTxFixture(t)
				eoTxAttachmentsDef(f).Provenance = prov
				_, err := f.run("raise", f.actor, models.EOTransitionInput{Attachments: []string{a}})
				if err == nil {
					t.Fatalf("a transition wrote attachments into a %q field", prov)
				}
				if !strings.Contains(err.Error(), "attachments") || !strings.Contains(err.Error(), prov) {
					t.Errorf("err = %v, want the field and its provenance named", err)
				}
				if len(f.records.updated) != 0 {
					t.Fatalf("the record was written anyway: %v", f.records.updated)
				}
			})
		}
	})
}

// Requiredness is judged over the record the transition will leave behind, so
// attachments supplied on the transition satisfy a field required at the
// target state. They arrive on input.Attachments, not in input.Values, and a
// refusal here would tell the actor they had not supplied what they just did.
func TestTransitionAttachmentsSatisfyRequiredness(t *testing.T) {
	a := uuid.NewString()

	build := func(t *testing.T) *eoTxFixture {
		f := eoNewTxFixture(t)
		d := eoTxAttachmentsDef(f)
		d.RequiredMode = models.EORequiredAtState
		d.RequiredAtStateIDs = datatypes.JSON(`["` + f.escalated.ID.String() + `"]`)
		return f
	}

	t.Run("supplied on the transition", func(t *testing.T) {
		f := build(t)
		if _, err := f.run("raise", f.actor, models.EOTransitionInput{Attachments: []string{a}}); err != nil {
			t.Fatalf("the attachments were supplied and the transition still refused them: %v", err)
		}
		if got := eoTxAttachments(t, f.records.updated[len(f.records.updated)-1]); strings.Join(got, ",") != a {
			t.Fatalf("attachments = %v, want %s filed", got, a)
		}
	})

	t.Run("not supplied at all is still refused", func(t *testing.T) {
		f := build(t)
		_, err := f.run("raise", f.actor, models.EOTransitionInput{})
		if err == nil {
			t.Fatal("the record entered a state that requires attachments with none")
		}
		var invalid *EOValidationError
		if !errors.As(err, &invalid) {
			t.Fatalf("err = %v, want a field validation error", err)
		}
		if _, named := invalid.Fields["attachments"]; !named {
			t.Fatalf("fields = %v, want attachments named", invalid.Fields)
		}
	})
}

// The env is built from stored AND submitted values, so a record that already
// holds a nil reaches the same rule on the transition path. This is the
// service's real evaluator, not a stub, so a regression here is a 500 on an
// ordinary transition.
func TestTransitionConditionalRuleOverANilStoredValue(t *testing.T) {
	f := eoNewTxFixture(t)
	f.types.defs = append(f.types.defs, models.EOFieldDef{
		ID: uuid.New(), RecordTypeID: f.recordType.ID, Key: "corrective_action", Label: "Corrective action",
		Kind: models.EOKindLongText, Provenance: models.EOProvenanceUser,
		RequiredMode: models.EORequiredConditional, RequiredRule: `he_reason matches "\\S"`,
	})
	// A BAG key, not a column: delay_reason is an eo_records column and
	// flattens to "" rather than nil, so it could not carry this case.
	f.setValues(t, map[string]any{"he_reason": nil})

	if _, err := f.run("raise", f.actor, models.EOTransitionInput{}); err != nil {
		t.Fatalf("a transition over a record holding a nil was refused: %v", err)
	}
}

// ── Task A3 (Plan 3): the pre-commit check and the executor must agree ─────

// eoCloseParentCheckTransition builds the one transition shape these two
// tests need: a single active close_parent action carrying `config`.
func eoCloseParentCheckTransition(config string) *models.WorkflowTransition {
	return &models.WorkflowTransition{
		Code: "issue",
		Actions: []models.TransitionAction{{
			Name:       "close the draft study",
			ActionType: eoActionTypeCloseParent,
			IsActive:   true,
			Config:     config,
		}},
	}
}

// TestCloseParentCheckMatchesTheExecutorOnWhitespace.
//
// eoCheckCloseParentHasAParent is the PRE-COMMIT refusal that exists so a
// close_parent action cannot fail after the child has committed, leaving the
// parent open with nothing left to close it. It read
// `strings.TrimSpace(cfg.LinkType)` and looked THAT up among the record's
// links, while eoActionExecutor.closeParent compares `l.LinkTypeKey ==
// cfg.LinkType` untrimmed. So a config with a stray space passed the check
// and then failed after the commit — precisely the outcome the check exists
// to prevent, and the one state in which it made things worse by vouching
// for the transition.
//
// BOTH DIRECTIONS, because a check that simply refused everything whitespaced
// would pass the first half while still disagreeing with the executor: the
// executor finds a link whose stored key carries the same whitespace, and so
// must the check.
func TestCloseParentCheckMatchesTheExecutorOnWhitespace(t *testing.T) {
	const key = "eo_decision_record_parent"
	rec := &models.EORecord{ID: uuid.New(), Reference: "EO-DEC-2026-001"}

	t.Run("a padded config against a clean link is refused", func(t *testing.T) {
		repo := &eoAxRepo{links: []models.EORecordLink{{FromRecordID: rec.ID, LinkTypeKey: key}}}
		err := eoCheckCloseParentHasAParent(context.Background(), repo,
			rec, eoCloseParentCheckTransition(`{"link_type":" `+key+` ","transition":"close"}`))
		if err == nil {
			t.Fatalf("the check passed a config whose link type is %q while the record carries %q; closeParent compares the two untrimmed, so the transition commits and the parent is left open", " "+key+" ", key)
		}
		if !errors.Is(err, ErrEOCloseParentHasNoParent) {
			t.Errorf("err = %v, want ErrEOCloseParentHasNoParent so a caller can tell it from a requirement refusal", err)
		}
	})

	t.Run("a padded config against the link the executor would find is allowed", func(t *testing.T) {
		padded := " " + key + " "
		repo := &eoAxRepo{links: []models.EORecordLink{{FromRecordID: rec.ID, LinkTypeKey: padded}}}
		if err := eoCheckCloseParentHasAParent(context.Background(), repo,
			rec, eoCloseParentCheckTransition(`{"link_type":"`+padded+`","transition":"close"}`)); err != nil {
			t.Errorf("the check refused a link closeParent would have found (%q == %q): err = %v", padded, padded, err)
		}
	})
}

// TestCloseParentCheckRefusesAnEmptyTransition.
//
// closeParent errors on an empty `Transition` ("close_parent config names no
// transition"), and that error arrives AFTER the child has committed. The
// pre-commit check validated `link_type` and never `transition`, so one
// "parent left open after the commit" path survived a check whose own comment
// says a transition that cannot run its closing action must not pretend it
// did.
//
// TWO SHAPES, because closeParent treats them differently and so must this.
// It EMPTINESS-TESTS strings.TrimSpace(cfg.Transition) and then MATCHES the
// untrimmed value against the parent's transition codes — so an empty or
// all-whitespace transition is refused there by the first test, and a PADDED
// one (" close ") survives it and then matches no code at all. Both are
// refused here, the padded one on its shape: no stored transition code could
// legitimately carry the padding, so refusing it is exactly as strict as the
// executor and no stricter.
func TestCloseParentCheckRefusesAnEmptyTransition(t *testing.T) {
	const key = "eo_decision_record_parent"
	rec := &models.EORecord{ID: uuid.New(), Reference: "EO-DEC-2026-001"}

	for _, tc := range []struct{ name, config string }{
		{"no transition key at all", `{"link_type":"` + key + `"}`},
		{"an empty transition", `{"link_type":"` + key + `","transition":""}`},
		{"a transition of spaces", `{"link_type":"` + key + `","transition":"   "}`},
		// PADDED, not empty: closeParent compares cfg.Transition with the
		// parent's transition CODES untrimmed, so " close " matches none of
		// them and fails after the commit. Unlike a link type there is
		// nothing to look it up against here, and no stored code could
		// legitimately carry the padding, so the shape itself is the
		// refusal.
		{"a padded transition", `{"link_type":"` + key + `","transition":" close "}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The record DOES carry the link, so the only thing that can
			// refuse this is the transition check.
			repo := &eoAxRepo{links: []models.EORecordLink{{FromRecordID: rec.ID, LinkTypeKey: key}}}
			err := eoCheckCloseParentHasAParent(context.Background(), repo, rec,
				eoCloseParentCheckTransition(tc.config))
			if err == nil {
				t.Fatal("the check passed a close_parent action that names no transition; closeParent refuses it after the commit and the parent stays open")
			}
			if !strings.Contains(err.Error(), "transition") || !strings.Contains(err.Error(), "close the draft study") {
				t.Errorf("err = %v, and it names neither the transition nor the action, so an administrator cannot find the configuration to fix", err)
			}
		})
	}
}

// ── a transition says why it cannot run ─────────────────────────────────────

func eoTxByCode(t *testing.T, trs []models.WorkflowTransition, code string) models.WorkflowTransition {
	t.Helper()
	for _, tr := range trs {
		if tr.Code == code {
			return tr
		}
	}
	t.Fatalf("transition %q not offered (have %v)", code, eoTxCodes(trs))
	return models.WorkflowTransition{}
}

func TestAvailableTransitionsMarksBlockedOnes(t *testing.T) {
	newFixture := func(t *testing.T) *eoTxFixture {
		t.Helper()
		f := eoNewTxFixture(t)
		f.types.linkTypes["assignment_of"] = &models.EOLinkType{Key: "assignment_of", BlocksParentClosure: true}
		f.types.linkTypes["objection_to"] = &models.EOLinkType{Key: "objection_to", BlocksParentTransitionKey: "raise"}
		return f
	}

	t.Run("an open blocking child marks the move it blocks, with its reference", func(t *testing.T) {
		f := newFixture(t)
		child := f.addChild("assignment_of", false)
		got, err := f.svc.AvailableTransitions(context.Background(), f.rec, f.actor)
		if err != nil {
			t.Fatalf("available: %v", err)
		}
		closeTr := eoTxByCode(t, got, "close")
		if !closeTr.Blocked || len(closeTr.BlockedBy) != 1 || closeTr.BlockedBy[0].Reference != child.Reference {
			t.Errorf("close = blocked %v by %v, want blocked by %q", closeTr.Blocked, closeTr.BlockedBy, child.Reference)
		}
		if raise := eoTxByCode(t, got, "raise"); raise.Blocked {
			t.Error("raise is marked blocked by a blocks_parent_closure child")
		}
	})

	t.Run("the offer and the refusal come from one rule", func(t *testing.T) {
		f := newFixture(t)
		f.addChild("objection_to", false)
		got, err := f.svc.AvailableTransitions(context.Background(), f.rec, f.actor)
		if err != nil {
			t.Fatalf("available: %v", err)
		}
		for _, code := range []string{"raise", "close"} {
			offered := eoTxByCode(t, got, code)
			_, runErr := f.run(code, f.actor, models.EOTransitionInput{Values: map[string]any{"closure_note": "done"}})
			if offered.Blocked != errors.Is(runErr, ErrEOTransitionBlocked) {
				t.Errorf("%s: offered blocked=%v but Execute returned %v", code, offered.Blocked, runErr)
			}
		}
	})

	t.Run("a closed child, or none, blocks nothing", func(t *testing.T) {
		f := newFixture(t)
		got, err := f.svc.AvailableTransitions(context.Background(), f.rec, f.actor)
		if err != nil || len(got) == 0 {
			t.Fatalf("available = %v, err = %v; want transitions to inspect", eoTxCodes(got), err)
		}
		for _, tr := range got {
			if tr.Blocked || len(tr.BlockedBy) != 0 {
				t.Errorf("%s blocked with no children", tr.Code)
			}
		}
		f.addChild("assignment_of", true)
		got, err = f.svc.AvailableTransitions(context.Background(), f.rec, f.actor)
		if err != nil || len(got) == 0 {
			t.Fatalf("available = %v, err = %v; want transitions to inspect", eoTxCodes(got), err)
		}
		for _, tr := range got {
			if tr.Blocked {
				t.Errorf("%s blocked by a closed child", tr.Code)
			}
		}
	})

	t.Run("the refusal carries the blocking records", func(t *testing.T) {
		f := newFixture(t)
		child := f.addChild("assignment_of", false)
		_, err := f.run("close", f.actor, models.EOTransitionInput{Values: map[string]any{"closure_note": "done"}})
		var be *EOTransitionBlockedError
		if !errors.As(err, &be) || len(be.Blocked) != 1 || be.Blocked[0].ID != child.ID {
			t.Fatalf("err = %v, want *EOTransitionBlockedError naming the child", err)
		}
		if !errors.Is(err, ErrEOTransitionBlocked) {
			t.Error("typed error no longer matches ErrEOTransitionBlocked")
		}
	})
}

// TestAvailableTransitionsGathersTheBlockingInputsOnce pins the structure, not
// the answer: the rule's inputs are fetched once per request however many
// transitions are offered, and the decision still runs per transition.
func TestAvailableTransitionsGathersTheBlockingInputsOnce(t *testing.T) {
	f := eoNewTxFixture(t)
	f.types.linkTypes["assignment_of"] = &models.EOLinkType{Key: "assignment_of", BlocksParentClosure: true}
	const kids = 3
	for i := 0; i < kids; i++ {
		f.addChild("assignment_of", false)
	}
	f.records.childrenTotal, f.records.linksTotal = 0, 0

	got, err := f.svc.AvailableTransitions(context.Background(), f.rec, f.actor)
	if err != nil {
		t.Fatalf("available: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("offered %v, want at least two transitions so per-transition work would show", eoTxCodes(got))
	}
	if f.records.childrenTotal != 1 {
		t.Errorf("Children called %d times for %d transitions, want 1", f.records.childrenTotal, len(got))
	}
	if want := 1 + kids; f.records.linksTotal != want {
		t.Errorf("LinksFrom called %d times, want %d (record + each child, once)", f.records.linksTotal, want)
	}
	if !eoTxByCode(t, got, "close").Blocked {
		t.Error("close not blocked: the shared inputs were not decided over")
	}
}

// TestAvailableTransitionsSkipsATransitionWhoseTargetStateIsGone.
//
// WorkflowRepository.ListByRecordTypeID preloads States, and the preload
// excludes soft-deleted rows. So retiring a state that is still some
// transition's target leaves that transition pointing at a state the workflow
// no longer has — one misconfigured row, in configuration an administrator can
// change at runtime.
//
// The list must survive it. Failing the whole read would mean an officer in
// that state is offered NO move at all on a record in a statutory register —
// the list lost, not one entry — which is strictly worse than the behaviour
// before the to-state check was added. But the broken transition must not be
// OFFERED either: with no to-state the blocking rule cannot be decided, and
// offering it undecided hands the officer a move Execute then refuses.
//
// And it must not be SILENT: a move that vanishes with no explanation is its
// own defect. The skip is logged naming the transition, its workflow and the
// missing state, which is the only trace an operator gets, so it is asserted
// here rather than taken on trust.
func TestAvailableTransitionsSkipsATransitionWhoseTargetStateIsGone(t *testing.T) {
	f := eoNewTxFixture(t)
	retired := uuid.New() // a state id that is NOT among wf.States
	f.addTransition(&models.WorkflowTransition{
		Code: "archive", Name: "Archive", FromStateID: f.review.ID, ToStateID: retired, IsActive: true,
	})

	var logged strings.Builder
	restore := log.Writer()
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(restore) })

	got, err := f.svc.AvailableTransitions(context.Background(), f.rec, f.actor)
	if err != nil {
		t.Fatalf("available: %v — one misconfigured transition must not deny the officer every other move", err)
	}

	codes := eoTxCodes(got)
	// The healthy moves out of `review` are still served, whole.
	for _, code := range []string{"raise", "close"} {
		eoTxByCode(t, got, code)
	}
	for _, tr := range got {
		if tr.Code == "archive" {
			t.Errorf("the broken transition is offered (%v): without a to-state its blocking rule was never decided, and Execute would refuse it", codes)
		}
	}

	out := logged.String()
	for _, want := range []string{"archive", f.workflows.wf.Code, retired.String()} {
		if !strings.Contains(out, want) {
			t.Errorf("the skip was logged without %q — an operator has no other trace of it: %q", want, out)
		}
	}
}

// A requirement refusal is a validation error, so the handler answers 400
// with the reason, not "Internal server error" (the staging "Analyse the
// letter" 500, where the designer had added a mandatory attachment).
func TestEoRequirementRefusalsAreValidationErrors(t *testing.T) {
	for _, tc := range []struct {
		kind, key string
		in        models.EOTransitionInput
	}{
		{"comment", "comment", models.EOTransitionInput{}},
		{"attachment", eoAttachmentsFieldKey, models.EOTransitionInput{Comment: "x"}},
		{"feedback", "transition", models.EOTransitionInput{Comment: "x"}},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			tr := &models.WorkflowTransition{Name: "Analyse the letter", Code: "analyse",
				Requirements: []models.TransitionRequirement{{RequirementType: tc.kind, IsMandatory: eoTxMandatory(true)}}}
			err := eoCheckRequirements(tr, tc.in)
			var invalid *EOValidationError
			if !errors.As(err, &invalid) {
				t.Fatalf("refusal is %T (%v), want *EOValidationError", err, err)
			}
			if invalid.Fields[tc.key] == "" {
				t.Errorf("refusal names %v, want %q", invalid.Fields, tc.key)
			}
		})
	}
}
