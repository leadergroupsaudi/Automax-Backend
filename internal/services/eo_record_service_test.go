package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/automax/backend/internal/config"
	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/internal/services/eoformula"
	"github.com/automax/backend/internal/storage"
	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// ── fakes ───────────────────────────────────────────────────────────────────
// Each embeds the repository interface and overrides only what the pipeline
// calls, so a method the service starts using shows up as a nil-pointer panic
// in the test rather than as a silent zero value.

type eoSvcRecordRepo struct {
	repository.EORecordRepository
	stored   map[uuid.UUID]*models.EORecord
	children map[uuid.UUID][]models.EORecord
	entered  map[uuid.UUID]bool

	created      []models.EORecord
	updated      []models.EORecord
	createdLinks []models.EORecordLink
	history      []models.EORecordHistory
	// txTimers is the timer repository Transaction hands its closure; nil
	// unless a test wires SLA clocks.
	txTimers repository.EOTimerRepository
	// txTypes is the record-type repository Transaction hands its closure.
	// Update's derived step builds its dependency loader from it, as Execute
	// does, so the fixture wires it to the same type repository the service
	// holds.
	txTypes repository.EORecordTypeRepository
	// txRecords is the record repository Transaction hands its closure. Nil
	// means the fake itself; a test that must tell a transaction-bound read
	// from a pooled one sets a distinct object here.
	txRecords repository.EORecordRepository
	// lockCalls counts LockForUpdate. Update must take the row lock, not
	// merely open a transaction: a transaction alone does not serialise two
	// concurrent edits under READ COMMITTED.
	lockCalls int
	// onLock runs just after LockForUpdate finds the row and just before it
	// is handed back, standing in for another transaction's committed edit
	// landing between the service's pre-read and the lock.
	onLock func(stored *models.EORecord)
	// historyErr fails CreateHistory, to test what a failed create leaves.
	historyErr error
	// historyErrFrom limits historyErr to that call onwards (1-based; 0 means
	// every call), so a test can fail the SECOND history row a create writes
	// while letting the first through.
	historyErrFrom int
	historyCalls   int
	// inTx is true for as long as Transaction's closure is running, so a test
	// can tell whether something the pipeline did happened while a database
	// connection was held.
	inTx bool
}

func eoNewSvcRecordRepo() *eoSvcRecordRepo {
	return &eoSvcRecordRepo{stored: map[uuid.UUID]*models.EORecord{}}
}

func (f *eoSvcRecordRepo) Create(_ context.Context, rec *models.EORecord) error {
	f.created = append(f.created, *rec)
	f.stored[rec.ID] = rec
	return nil
}

func (f *eoSvcRecordRepo) Update(_ context.Context, rec *models.EORecord) error {
	f.updated = append(f.updated, *rec)
	f.stored[rec.ID] = rec
	return nil
}

func (f *eoSvcRecordRepo) GetByID(_ context.Context, id, recordTypeID uuid.UUID) (*models.EORecord, error) {
	rec, ok := f.stored[id]
	if !ok || rec.RecordTypeID != recordTypeID {
		return nil, nil
	}
	return rec, nil
}

func (f *eoSvcRecordRepo) GetAnyByID(_ context.Context, id uuid.UUID) (*models.EORecord, error) {
	return f.stored[id], nil
}

// LockForUpdate stands in for SELECT … FOR UPDATE. It answers like GetByID and
// counts the call: the fake cannot simulate a lock, but it CAN prove the
// pipeline asks for one.
func (f *eoSvcRecordRepo) LockForUpdate(_ context.Context, id, recordTypeID uuid.UUID) (*models.EORecord, error) {
	f.lockCalls++
	if f.onLock != nil {
		if rec, ok := f.stored[id]; ok {
			f.onLock(rec)
		} else {
			f.onLock(nil)
		}
	}
	rec, ok := f.stored[id]
	if !ok || rec.RecordTypeID != recordTypeID {
		return nil, nil
	}
	return rec, nil
}

func (f *eoSvcRecordRepo) Children(_ context.Context, parentID uuid.UUID, recordTypeID *uuid.UUID) ([]models.EORecord, error) {
	var out []models.EORecord
	for _, c := range f.children[parentID] {
		if recordTypeID == nil || c.RecordTypeID == *recordTypeID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *eoSvcRecordRepo) RecordsOfType(_ context.Context, recordTypeID uuid.UUID) ([]models.EORecord, error) {
	var out []models.EORecord
	for _, rec := range f.stored {
		if rec.RecordTypeID == recordTypeID {
			out = append(out, *rec)
		}
	}
	return out, nil
}

func (f *eoSvcRecordRepo) CreateLink(_ context.Context, link *models.EORecordLink) error {
	f.createdLinks = append(f.createdLinks, *link)
	return nil
}

// LinksFrom answers for the record being created, which has none yet; the
// pipeline asks so that the cardinality rule holds wherever it is reused.
func (f *eoSvcRecordRepo) LinksFrom(_ context.Context, _ uuid.UUID) ([]models.EORecordLink, error) {
	return nil, nil
}

func (f *eoSvcRecordRepo) CreateHistory(_ context.Context, entry *models.EORecordHistory) error {
	f.historyCalls++
	if f.historyErr != nil && f.historyCalls >= f.historyErrFrom {
		return f.historyErr
	}
	f.history = append(f.history, *entry)
	return nil
}

func (f *eoSvcRecordRepo) EnteredStateIDs(_ context.Context, _, _ uuid.UUID) (map[uuid.UUID]bool, error) {
	return f.entered, nil
}

func (f *eoSvcRecordRepo) Transaction(_ context.Context, fn func(repository.EORecordRepository, repository.EORecordTypeRepository, repository.EOTimerRepository) error) error {
	records := repository.EORecordRepository(f)
	if f.txRecords != nil {
		records = f.txRecords
	}
	f.inTx = true
	defer func() { f.inTx = false }()
	return fn(records, f.txTypes, f.txTimers)
}

type eoSvcTypeRepo struct {
	repository.EORecordTypeRepository
	types     map[uuid.UUID]*models.EORecordType
	defs      []models.EOFieldDef
	linkTypes map[string]*models.EOLinkType
}

func (f *eoSvcTypeRepo) add(t *models.EORecordType) *models.EORecordType {
	if f.types == nil {
		f.types = map[uuid.UUID]*models.EORecordType{}
	}
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	f.types[t.ID] = t
	return t
}

func (f *eoSvcTypeRepo) GetByID(_ context.Context, id uuid.UUID) (*models.EORecordType, error) {
	return f.types[id], nil
}

func (f *eoSvcTypeRepo) GetByKey(_ context.Context, key string) (*models.EORecordType, error) {
	for _, t := range f.types {
		if t.Key == key {
			return t, nil
		}
	}
	return nil, nil
}

func (f *eoSvcTypeRepo) Ancestors(_ context.Context, t *models.EORecordType) ([]models.EORecordType, error) {
	var out []models.EORecordType
	for cur := t; cur.ParentTypeID != nil; {
		parent := f.types[*cur.ParentTypeID]
		if parent == nil {
			return nil, errors.New("ancestor not found")
		}
		out = append(out, *parent)
		cur = parent
	}
	return out, nil
}

func (f *eoSvcTypeRepo) FieldDefs(_ context.Context, recordTypeIDs []uuid.UUID) ([]models.EOFieldDef, error) {
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

func (f *eoSvcTypeRepo) GetLinkTypeByKey(_ context.Context, key string) (*models.EOLinkType, error) {
	return f.linkTypes[key], nil
}

type eoSvcSequenceRepo struct {
	repository.EOSequenceRepository
	counters map[string]int
	scopes   []string
}

func (f *eoSvcSequenceRepo) NextInScope(_ context.Context, scope string) (int, error) {
	if f.counters == nil {
		f.counters = map[string]int{}
	}
	f.counters[scope]++
	f.scopes = append(f.scopes, scope)
	return f.counters[scope], nil
}

// ── fixtures ────────────────────────────────────────────────────────────────

// eoSvcFixture is one configured record type: its workflow, its fields and the
// repositories the service reads them through.
type eoSvcFixture struct {
	records   *eoSvcRecordRepo
	types     *eoSvcTypeRepo
	sequences *eoSvcSequenceRepo
	workflows *eoWorkflowFakeRepo
	svc       EORecordService

	recordType *models.EORecordType
	initial    models.WorkflowState
	actor      *models.User
}

func eoNewSvcFixture(t *testing.T, defs ...models.EOFieldDef) *eoSvcFixture {
	t.Helper()
	f := &eoSvcFixture{
		records:   eoNewSvcRecordRepo(),
		types:     &eoSvcTypeRepo{linkTypes: map[string]*models.EOLinkType{}},
		sequences: &eoSvcSequenceRepo{},
		actor:     &models.User{ID: uuid.New(), Username: "eo.officer"},
	}
	f.recordType = f.types.add(&models.EORecordType{
		Key: "eo_directive", Name: "Directive", IsActive: true,
		ReferencePrefix: "EO-DIR", ReferenceMode: models.EOReferenceModeOwn,
		ReferencePattern: "EO-DIR-YYYY-NNN",
	})
	f.initial = models.WorkflowState{ID: uuid.New(), Code: "draft", StateType: "initial"}
	wf := eoWfBound("eo-directive", &f.recordType.ID, "incident")
	wf.States = []models.WorkflowState{f.initial, {ID: uuid.New(), Code: "closed", StateType: "terminal"}}
	f.workflows = &eoWorkflowFakeRepo{workflows: []models.Workflow{wf}}

	defs = append(eoSvcInheritedFields(), defs...)
	for i := range defs {
		if defs[i].RecordTypeID == uuid.Nil {
			defs[i].RecordTypeID = f.recordType.ID
		}
	}
	f.types.defs = defs
	f.records.txTypes = f.types
	f.svc = NewEORecordService(f.records, f.types, f.sequences, f.workflows)
	return f
}

// eoSvcInheritedFields stands in for the inherited, column-backed definitions
// the base type carries (Task 20 seeds the full 17). Every key a request may
// submit needs one: a key no effective field defines is rejected.
func eoSvcInheritedFields() []models.EOFieldDef {
	out := []models.EOFieldDef{eoSvcField("subject_ar", models.EORequiredAlways)}
	for _, key := range []string{"subject_en", "priority", "sensitivity", "source", "change_reason"} {
		out = append(out, eoSvcField(key, models.EORequiredNever))
	}
	due := eoSvcField("due_date", models.EORequiredNever)
	due.Kind = models.EOKindDate
	return append(out, due)
}

// eoSvcField is a plain user-entered field definition.
func eoSvcField(key string, requiredMode string) models.EOFieldDef {
	return models.EOFieldDef{
		ID: uuid.New(), Key: key, Label: key, Kind: models.EOKindText,
		Provenance: models.EOProvenanceUser, RequiredMode: requiredMode,
	}
}

func eoSvcValues(t *testing.T, raw datatypes.JSON) map[string]any {
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

func eoSvcFieldErrors(t *testing.T, err error) map[string]string {
	t.Helper()
	var ve *EOValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("error = %v (%T), want an *EOValidationError", err, err)
	}
	return ve.Fields
}

// ── create ──────────────────────────────────────────────────────────────────

func TestCreateAssignsReferenceAndInitialState(t *testing.T) {
	f := eoNewSvcFixture(t)
	year := time.Now().Year()

	rec, err := f.svc.Create(context.Background(), &models.EORecordCreateRequest{
		RecordTypeID: f.recordType.ID,
		Values:       map[string]any{"subject_ar": "توجيه", "priority": "high"},
	}, f.actor)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	wantRef := "EO-DIR-" + strconv.Itoa(year) + "-001"
	if rec.Reference != wantRef {
		t.Errorf("reference = %q, want %q", rec.Reference, wantRef)
	}
	if rec.StateID == nil || *rec.StateID != f.initial.ID {
		t.Errorf("state_id = %v, want the workflow's initial state %s", rec.StateID, f.initial.ID)
	}
	if rec.CreatedByID != f.actor.ID {
		t.Errorf("created_by_id = %s, want the actor %s", rec.CreatedByID, f.actor.ID)
	}
	// The inherited fields are columns, not values keys.
	if rec.SubjectAr != "توجيه" || rec.Priority != "high" {
		t.Errorf("subject_ar = %q, priority = %q, want them written to their columns", rec.SubjectAr, rec.Priority)
	}
	if v := eoSvcValues(t, rec.Values); len(v) != 0 {
		t.Errorf("values jsonb = %v, want the column-backed fields to stay out of it", v)
	}

	if len(f.records.created) != 1 {
		t.Fatalf("records created = %d, want 1", len(f.records.created))
	}
	saved := f.records.created[0]
	if saved.Reference != wantRef || saved.StateID == nil {
		t.Errorf("persisted record = %+v, want the reference and state assigned before the insert", saved)
	}
	// The sequence is drawn from the pattern's own-sequence scope, keyed by year.
	wantScope, err := EOSequenceScope("EO-DIR-YYYY-NNN", "", year)
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	if len(f.sequences.scopes) != 1 || f.sequences.scopes[0] != wantScope {
		t.Errorf("sequence scopes = %v, want [%s]", f.sequences.scopes, wantScope)
	}
	// Entering the initial state is a transition like any other: without the
	// history row, "has this record ever been in state X" can never answer
	// yes for the state every record starts in.
	if len(f.records.history) != 1 {
		t.Fatalf("history rows = %d, want 1 for the creation", len(f.records.history))
	}
	h := f.records.history[0]
	if h.RecordID != rec.ID || h.RecordTypeID != rec.RecordTypeID {
		t.Errorf("history row points at %s/%s, want %s/%s", h.RecordID, h.RecordTypeID, rec.ID, rec.RecordTypeID)
	}
	if h.ToStateID == nil || *h.ToStateID != f.initial.ID || h.FromStateID != nil {
		t.Errorf("history transition = %v -> %v, want nil -> %s", h.FromStateID, h.ToStateID, f.initial.ID)
	}
	if h.PerformedByID == nil || *h.PerformedByID != f.actor.ID {
		t.Errorf("history performed_by = %v, want the actor %s", h.PerformedByID, f.actor.ID)
	}
}

func TestCreateRejectsMissingRequiredField(t *testing.T) {
	f := eoNewSvcFixture(t,
		eoSvcField("justification", models.EORequiredAlways),
		eoSvcField("note", models.EORequiredNever),
	)

	_, err := f.svc.Create(context.Background(), &models.EORecordCreateRequest{
		RecordTypeID: f.recordType.ID,
		Values:       map[string]any{"subject_ar": "توجيه", "note": "fyi"},
	}, f.actor)
	if err == nil {
		t.Fatalf("create with a missing required field succeeded, want a validation error")
	}
	fields := eoSvcFieldErrors(t, err)
	if _, ok := fields["justification"]; !ok {
		t.Errorf("error fields = %v, want the map keyed by the field key %q", fields, "justification")
	}
	if _, ok := fields["subject_ar"]; ok {
		t.Errorf("error fields = %v, want nothing for the field that was supplied", fields)
	}
	// Nothing is written and no number is burned on an invalid payload.
	if len(f.records.created) != 0 || len(f.records.history) != 0 {
		t.Errorf("records = %d, history = %d, want nothing persisted", len(f.records.created), len(f.records.history))
	}
	if len(f.sequences.scopes) != 0 {
		t.Errorf("sequence drawn %v, want none before the payload is known to be valid", f.sequences.scopes)
	}
}

func TestCreateRunsDerivedFieldsBeforePersisting(t *testing.T) {
	f := eoNewSvcFixture(t,
		models.EOFieldDef{
			ID: uuid.New(), Key: "weeks", Label: "Weeks", Kind: models.EOKindNumber,
			Provenance: models.EOProvenanceUser,
		},
		eoDerivedField("total_days", "weeks * 7", eoOwn("weeks")),
	)

	rec, err := f.svc.Create(context.Background(), &models.EORecordCreateRequest{
		RecordTypeID: f.recordType.ID,
		Values:       map[string]any{"subject_ar": "توجيه", "weeks": 3},
	}, f.actor)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := eoSvcValues(t, rec.Values)["total_days"]; got != 21.0 {
		t.Errorf("total_days = %v, want 21", got)
	}
	// The value the DB received is what matters: recomputing after the insert
	// would leave the stored row stale until the next edit.
	if len(f.records.created) != 1 {
		t.Fatalf("records created = %d, want 1", len(f.records.created))
	}
	if got := eoSvcValues(t, f.records.created[0].Values)["total_days"]; got != 21.0 {
		t.Errorf("persisted total_days = %v, want 21 — derived fields run before the insert", got)
	}
}

func TestCreateWritesADerivedFieldIntoItsColumn(t *testing.T) {
	f := eoNewSvcFixture(t,
		models.EOFieldDef{
			ID: uuid.New(), Key: "visit_date", Label: "Visit date", Kind: models.EOKindDate,
			Provenance: models.EOProvenanceUser,
		},
		eoDerivedField("due_date", "visit_date - days(2)", eoOwn("visit_date")),
	)

	rec, err := f.svc.Create(context.Background(), &models.EORecordCreateRequest{
		RecordTypeID: f.recordType.ID,
		Values:       map[string]any{"subject_ar": "زيارة", "visit_date": "2026-03-10"},
	}, f.actor)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// due_date is an inherited field, so it is a column: a derived value for
	// it has to land there, not in the jsonb bag the formula read from.
	want := time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC)
	if rec.DueDate == nil || !rec.DueDate.Equal(want) {
		t.Errorf("due_date = %v, want %v in the column", rec.DueDate, want)
	}
	if v := eoSvcValues(t, rec.Values)["due_date"]; v != nil {
		t.Errorf("values[due_date] = %v, want the derived value in the column only", v)
	}
}

// eoSvcParent configures a parent record and a link type tying children to it.
func eoSvcParent(t *testing.T, f *eoSvcFixture, lt *models.EOLinkType, due *time.Time, sensitivity string) *models.EORecord {
	t.Helper()
	parentType := f.types.add(&models.EORecordType{
		Key: "eo_programme", Name: "Programme", IsActive: true,
		ReferencePrefix: "EO-PRG", ReferencePattern: "EO-PRG-YYYY-NNN",
	})
	parent := &models.EORecord{
		ID: uuid.New(), RecordTypeID: parentType.ID, Reference: "EO-PRG-2026-007",
		SubjectAr: "برنامج", DueDate: due, Sensitivity: sensitivity,
	}
	f.records.stored[parent.ID] = parent
	lt.SourceTypeIDs = eoLinkTypeIDs(t, []string{f.recordType.ID.String()})
	lt.TargetTypeIDs = eoLinkTypeIDs(t, []string{parentType.ID.String()})
	f.types.linkTypes[lt.Key] = lt
	return parent
}

func TestCreateAppliesParentDerivedDeadline(t *testing.T) {
	f := eoNewSvcFixture(t)
	parentDue := time.Now().Add(30 * 24 * time.Hour).UTC()
	submitted := time.Now().Add(90 * 24 * time.Hour).UTC().Format(time.RFC3339)
	lt := &models.EOLinkType{
		ID: uuid.New(), Key: "covers_item", Label: "Covers",
		DeriveDeadlineFromParent: true, DeadlineOffsetDays: 7,
		Cardinality: models.EOCardinalityOne,
	}
	parent := eoSvcParent(t, f, lt, &parentDue, "")

	rec, err := f.svc.Create(context.Background(), &models.EORecordCreateRequest{
		RecordTypeID:      f.recordType.ID,
		ParentRecordID:    &parent.ID,
		ParentLinkTypeKey: "covers_item",
		Values:            map[string]any{"subject_ar": "بند", "due_date": submitted},
	}, f.actor)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	want := parentDue.AddDate(0, 0, -7)
	if rec.DueDate == nil || !rec.DueDate.Equal(want) {
		t.Errorf("due_date = %v, want the parent's %v less the 7 day offset (%v)", rec.DueDate, parentDue, want)
	}
	if rec.ParentRecordID == nil || *rec.ParentRecordID != parent.ID {
		t.Errorf("parent_record_id = %v, want %s", rec.ParentRecordID, parent.ID)
	}
	if len(f.records.createdLinks) != 1 {
		t.Fatalf("links created = %d, want 1", len(f.records.createdLinks))
	}
	link := f.records.createdLinks[0]
	if link.FromRecordID != rec.ID || link.ToRecordID != parent.ID || link.LinkTypeKey != "covers_item" {
		t.Errorf("link = %+v, want the child linked to the parent by covers_item", link)
	}
	if link.FromRecordTypeID != rec.RecordTypeID || link.ToRecordTypeID != parent.RecordTypeID {
		t.Errorf("link types = %s -> %s, want %s -> %s: a link row carries both endpoints' record types",
			link.FromRecordTypeID, link.ToRecordTypeID, rec.RecordTypeID, parent.RecordTypeID)
	}
}

func TestCreateKeepsADerivedDeadlineThatIsAlreadyPast(t *testing.T) {
	f := eoNewSvcFixture(t)
	parentDue := time.Now().Add(3 * 24 * time.Hour).UTC()
	lt := &models.EOLinkType{
		ID: uuid.New(), Key: "covers_item", Label: "Covers",
		DeriveDeadlineFromParent: true, DeadlineOffsetDays: 7,
	}
	parent := eoSvcParent(t, f, lt, &parentDue, "")

	rec, err := f.svc.Create(context.Background(), &models.EORecordCreateRequest{
		RecordTypeID:      f.recordType.ID,
		ParentRecordID:    &parent.ID,
		ParentLinkTypeKey: "covers_item",
		Values:            map[string]any{"subject_ar": "بند"},
	}, f.actor)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// A parent due in 3 days with a 7 day offset yields a deadline 4 days
	// ago. It is kept: the record really is late the moment it exists, and
	// clamping to today would invent a deadline the configuration does not
	// describe and hide an overdue child from the SLA sweep.
	want := parentDue.AddDate(0, 0, -7)
	if rec.DueDate == nil || !rec.DueDate.Equal(want) {
		t.Fatalf("due_date = %v, want %v — a past derived deadline is kept, not clamped", rec.DueDate, want)
	}
	if !rec.DueDate.Before(time.Now()) {
		t.Errorf("due_date = %v, want it in the past for this fixture", rec.DueDate)
	}
}

func TestCreateCascadesSensitivityFromParent(t *testing.T) {
	cases := []struct {
		name    string
		cascade bool
		want    string
	}{
		{"a confidential parent's child inherits its label", true, "confidential"},
		{"without the rule the child keeps what was submitted", false, "normal"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := eoNewSvcFixture(t)
			lt := &models.EOLinkType{
				ID: uuid.New(), Key: "covers_item", Label: "Covers",
				CascadeSensitivity: tc.cascade,
			}
			parent := eoSvcParent(t, f, lt, nil, "confidential")

			rec, err := f.svc.Create(context.Background(), &models.EORecordCreateRequest{
				RecordTypeID:      f.recordType.ID,
				ParentRecordID:    &parent.ID,
				ParentLinkTypeKey: "covers_item",
				Values:            map[string]any{"subject_ar": "بند", "sensitivity": "normal"},
			}, f.actor)
			if err != nil {
				t.Fatalf("create: %v", err)
			}
			if rec.Sensitivity != tc.want {
				t.Errorf("sensitivity = %q, want %q", rec.Sensitivity, tc.want)
			}
		})
	}
}

func TestCreateNumbersAChildFromItsParentsReference(t *testing.T) {
	f := eoNewSvcFixture(t)
	f.recordType.ReferenceMode = models.EOReferenceModeParent
	f.recordType.ReferencePattern = "EO-PRG-YYYY-NNN-A#"
	lt := &models.EOLinkType{ID: uuid.New(), Key: "covers_item", Label: "Covers"}
	parent := eoSvcParent(t, f, lt, nil, "")

	rec, err := f.svc.Create(context.Background(), &models.EORecordCreateRequest{
		RecordTypeID:      f.recordType.ID,
		ParentRecordID:    &parent.ID,
		ParentLinkTypeKey: "covers_item",
		Values:            map[string]any{"subject_ar": "بند"},
	}, f.actor)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if want := parent.Reference + "-A1"; rec.Reference != want {
		t.Errorf("reference = %q, want %q", rec.Reference, want)
	}
	// Each parent counts its own children, so the scope carries the parent's
	// reference rather than the year.
	wantScope, err := EOSequenceScope("EO-PRG-YYYY-NNN-A#", parent.Reference, time.Now().Year())
	if err != nil {
		t.Fatalf("scope: %v", err)
	}
	if len(f.sequences.scopes) != 1 || f.sequences.scopes[0] != wantScope {
		t.Errorf("sequence scopes = %v, want [%s]", f.sequences.scopes, wantScope)
	}
}

func TestCreateRequiresALinkTypeForAParent(t *testing.T) {
	f := eoNewSvcFixture(t)
	parentDue := time.Now().Add(30 * 24 * time.Hour).UTC()
	lt := &models.EOLinkType{
		ID: uuid.New(), Key: "covers_item", Label: "Covers",
		DeriveDeadlineFromParent: true, DeadlineOffsetDays: 7, CascadeSensitivity: true,
	}
	parent := eoSvcParent(t, f, lt, &parentDue, "confidential")
	submitted := time.Now().Add(90 * 24 * time.Hour).UTC().Format(time.RFC3339)

	// Omitting the link type would otherwise buy the full parent hierarchy —
	// the record counts as a child for Children, BlockingChildren and
	// ancestor derivation — with none of the link rules: the client would set
	// its own deadline and its own sensitivity label.
	_, err := f.svc.Create(context.Background(), &models.EORecordCreateRequest{
		RecordTypeID:   f.recordType.ID,
		ParentRecordID: &parent.ID,
		Values:         map[string]any{"subject_ar": "بند", "due_date": submitted, "sensitivity": "normal"},
	}, f.actor)
	if err == nil {
		t.Fatalf("create under a parent with no link type succeeded, want it rejected")
	}
	if len(f.records.created) != 0 || len(f.sequences.scopes) != 0 {
		t.Errorf("records = %d, sequences = %v, want nothing written and no number burned",
			len(f.records.created), f.sequences.scopes)
	}
}

func TestCreateRejectsValuesNoFieldDefines(t *testing.T) {
	f := eoNewSvcFixture(t, eoSvcField("note", models.EORequiredNever))

	// A client must not be able to pre-seed a key a capability will later
	// read as its own (an approval stamp, a quorum flag) before any
	// definition exists to guard it, nor to grow the GIN-indexed bag at will.
	_, err := f.svc.Create(context.Background(), &models.EORecordCreateRequest{
		RecordTypeID: f.recordType.ID,
		Values:       map[string]any{"subject_ar": "توجيه", "note": "fine", "approval_stamp": "forged"},
	}, f.actor)
	if err == nil {
		t.Fatalf("create with an undefined key succeeded, want it rejected")
	}
	fields := eoSvcFieldErrors(t, err)
	if _, ok := fields["approval_stamp"]; !ok {
		t.Errorf("error fields = %v, want approval_stamp", fields)
	}
	if _, ok := fields["note"]; ok {
		t.Errorf("error fields = %v, want nothing for the defined key", fields)
	}
	if len(f.records.created) != 0 {
		t.Errorf("records created = %d, want 0", len(f.records.created))
	}

	// The same key is rejected on the update path.
	rec := eoSvcExisting(f, nil)
	if _, err := f.svc.Update(context.Background(), rec.ID, &models.EORecordUpdateRequest{
		RecordTypeID: rec.RecordTypeID,
		Values:       map[string]any{"approval_stamp": "forged"},
	}, f.actor); err == nil {
		t.Errorf("update with an undefined key succeeded, want it rejected")
	}
}

func TestCreateRejectsUnusableRecordTypes(t *testing.T) {
	t.Run("abstract type", func(t *testing.T) {
		f := eoNewSvcFixture(t)
		f.recordType.IsAbstract = true
		_, err := f.svc.Create(context.Background(), &models.EORecordCreateRequest{
			RecordTypeID: f.recordType.ID,
		}, f.actor)
		if err == nil {
			t.Fatalf("create of an abstract type succeeded, want an error")
		}
		if len(f.records.created) != 0 {
			t.Errorf("records created = %d, want 0", len(f.records.created))
		}
	})
	t.Run("unknown type", func(t *testing.T) {
		f := eoNewSvcFixture(t)
		_, err := f.svc.Create(context.Background(), &models.EORecordCreateRequest{
			RecordTypeID: uuid.New(),
		}, f.actor)
		if err == nil {
			t.Fatalf("create of an unconfigured type succeeded, want an error")
		}
	})
	t.Run("no actor", func(t *testing.T) {
		f := eoNewSvcFixture(t)
		_, err := f.svc.Create(context.Background(), &models.EORecordCreateRequest{
			RecordTypeID: f.recordType.ID,
		}, nil)
		if err == nil {
			t.Fatalf("create without an actor succeeded, want an error: created_by is NOT NULL")
		}
	})
}

// ── update ──────────────────────────────────────────────────────────────────

// eoSvcExisting puts a saved record in the fixture's repository.
func eoSvcExisting(f *eoSvcFixture, apply func(*models.EORecord)) *models.EORecord {
	state := uuid.New()
	rec := &models.EORecord{
		ID: uuid.New(), RecordTypeID: f.recordType.ID, Reference: "EO-DIR-2026-001",
		SubjectAr: "توجيه", Priority: "high", StateID: &state,
		CreatedByID: f.actor.ID, Values: datatypes.JSON([]byte(`{"note":"original"}`)),
	}
	if apply != nil {
		apply(rec)
	}
	f.records.stored[rec.ID] = rec
	return rec
}

func TestUpdateValidatesAgainstColumnsAndJSONBTogether(t *testing.T) {
	t.Run("a stored column satisfies a required field a partial edit does not mention", func(t *testing.T) {
		f := eoNewSvcFixture(t,
			eoSvcField("note", models.EORequiredNever),
			eoSvcField("remark", models.EORequiredNever),
		)
		rec := eoSvcExisting(f, nil)

		got, err := f.svc.Update(context.Background(), rec.ID, &models.EORecordUpdateRequest{
			RecordTypeID: rec.RecordTypeID,
			Values:       map[string]any{"remark": "added"},
		}, f.actor)
		if err != nil {
			t.Fatalf("update: %v — subject_ar is a column, and reading only the values jsonb makes it look empty", err)
		}
		if got.SubjectAr != "توجيه" {
			t.Errorf("subject_ar = %q, want it untouched", got.SubjectAr)
		}
		values := eoSvcValues(t, got.Values)
		if values["note"] != "original" || values["remark"] != "added" {
			t.Errorf("values = %v, want the untouched key kept and the new one merged", values)
		}
		if len(f.records.updated) != 1 {
			t.Fatalf("updates persisted = %d, want 1", len(f.records.updated))
		}
	})

	t.Run("an empty stored column still fails the required check", func(t *testing.T) {
		f := eoNewSvcFixture(t,
			eoSvcField("remark", models.EORequiredNever),
		)
		rec := eoSvcExisting(f, func(r *models.EORecord) { r.SubjectAr = "" })

		_, err := f.svc.Update(context.Background(), rec.ID, &models.EORecordUpdateRequest{
			RecordTypeID: rec.RecordTypeID,
			Values:       map[string]any{"remark": "added"},
		}, f.actor)
		if err == nil {
			t.Fatalf("update of a record missing a required column succeeded, want a validation error")
		}
		if _, ok := eoSvcFieldErrors(t, err)["subject_ar"]; !ok {
			t.Errorf("error fields = %v, want subject_ar", eoSvcFieldErrors(t, err))
		}
		if len(f.records.updated) != 0 {
			t.Errorf("updates persisted = %d, want 0", len(f.records.updated))
		}
	})

	t.Run("a column value is written to its column, not into the values bag", func(t *testing.T) {
		f := eoNewSvcFixture(t)
		rec := eoSvcExisting(f, nil)
		due := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)

		got, err := f.svc.Update(context.Background(), rec.ID, &models.EORecordUpdateRequest{
			RecordTypeID: rec.RecordTypeID,
			Values: map[string]any{
				"subject_ar": "توجيه معدل",
				"due_date":   due.Format(time.RFC3339),
			},
		}, f.actor)
		if err != nil {
			t.Fatalf("update: %v", err)
		}
		if got.SubjectAr != "توجيه معدل" {
			t.Errorf("subject_ar = %q, want the edit", got.SubjectAr)
		}
		if got.DueDate == nil || !got.DueDate.Equal(due) {
			t.Errorf("due_date = %v, want %v parsed into the column", got.DueDate, due)
		}
		if v := eoSvcValues(t, got.Values); v["subject_ar"] != nil || v["due_date"] != nil {
			t.Errorf("values = %v, want the column-backed keys to stay out of the jsonb bag", v)
		}
	})

	t.Run("an unparseable column value is a field error, not a panic", func(t *testing.T) {
		f := eoNewSvcFixture(t)
		rec := eoSvcExisting(f, nil)

		_, err := f.svc.Update(context.Background(), rec.ID, &models.EORecordUpdateRequest{
			RecordTypeID: rec.RecordTypeID,
			Values:       map[string]any{"due_date": "next tuesday"},
		}, f.actor)
		if err == nil {
			t.Fatalf("update with an unparseable date succeeded, want a validation error")
		}
		if _, ok := eoSvcFieldErrors(t, err)["due_date"]; !ok {
			t.Errorf("error fields = %v, want due_date", eoSvcFieldErrors(t, err))
		}
	})
}

func TestUpdateLocksAFieldTheRecordHasEverEntered(t *testing.T) {
	locked := uuid.New()
	newField := func(lockAfter uuid.UUID) models.EOFieldDef {
		d := eoSvcField("budget", models.EORequiredNever)
		d.IsLockedAfterStateID = &lockAfter
		return d
	}
	f := eoNewSvcFixture(t, newField(locked))
	// The record is NOT in the locking state now — it returned to an earlier
	// one — but its history says it has been there. Asking rec.StateID would
	// answer no and let the edit through.
	rec := eoSvcExisting(f, func(r *models.EORecord) {
		r.Values = datatypes.JSON([]byte(`{"note":"original","budget":"1000"}`))
	})
	f.records.entered = map[uuid.UUID]bool{locked: true}

	_, err := f.svc.Update(context.Background(), rec.ID, &models.EORecordUpdateRequest{
		RecordTypeID: rec.RecordTypeID,
		Values:       map[string]any{"budget": "2000"},
	}, f.actor)
	if err == nil {
		t.Fatalf("edit of a locked field succeeded, want it rejected")
	}
	if _, ok := eoSvcFieldErrors(t, err)["budget"]; !ok {
		t.Errorf("error fields = %v, want budget", eoSvcFieldErrors(t, err))
	}

	// Re-submitting the stored value unchanged is allowed, so a client can
	// echo the whole form.
	if _, err := f.svc.Update(context.Background(), rec.ID, &models.EORecordUpdateRequest{
		RecordTypeID: rec.RecordTypeID,
		Values:       map[string]any{"budget": "1000"},
	}, f.actor); err != nil {
		t.Errorf("re-submitting the locked field unchanged: %v, want it accepted", err)
	}
}

func TestUpdateRecomputesDerivedAcrossChildrenAndAncestors(t *testing.T) {
	f := eoNewSvcFixture(t,
		eoSvcField("note", models.EORequiredNever),
		eoDerivedField("total_weeks", "children_eo_stage_sum_weeks", []map[string]any{
			{"scope": "child", "type": "eo_stage", "field": "weeks", "aggregates": []string{"sum"}},
		}),
		eoDerivedField("programme_subject", "ancestor_eo_programme_headline", []map[string]any{
			{"scope": "ancestor", "type": "eo_programme", "field": "headline"},
		}),
	)
	stageType := f.types.add(&models.EORecordType{Key: "eo_stage", Name: "Stage", IsActive: true})
	programmeType := f.types.add(&models.EORecordType{Key: "eo_programme", Name: "Programme", IsActive: true})

	programme := &models.EORecord{
		ID: uuid.New(), RecordTypeID: programmeType.ID, Reference: "EO-PRG-2026-007",
		Values: datatypes.JSON([]byte(`{"headline":"national programme"}`)),
	}
	f.records.stored[programme.ID] = programme
	rec := eoSvcExisting(f, func(r *models.EORecord) { r.ParentRecordID = &programme.ID })
	f.records.children = map[uuid.UUID][]models.EORecord{
		rec.ID: {
			{ID: uuid.New(), RecordTypeID: stageType.ID, Values: datatypes.JSON([]byte(`{"weeks":2}`))},
			{ID: uuid.New(), RecordTypeID: stageType.ID, Values: datatypes.JSON([]byte(`{"weeks":5}`))},
			// A child of another type must not be aggregated.
			{ID: uuid.New(), RecordTypeID: programmeType.ID, Values: datatypes.JSON([]byte(`{"weeks":100}`))},
		},
	}

	got, err := f.svc.Update(context.Background(), rec.ID, &models.EORecordUpdateRequest{
		RecordTypeID: rec.RecordTypeID,
		Values:       map[string]any{"note": "touched"},
	}, f.actor)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	values := eoSvcValues(t, got.Values)
	if values["total_weeks"] != 7.0 {
		t.Errorf("total_weeks = %v, want 7: the dependency loader maps the record type KEY to its id before asking the repository", values["total_weeks"])
	}
	if values["programme_subject"] != "national programme" {
		t.Errorf("programme_subject = %v, want the nearest ancestor of type eo_programme", values["programme_subject"])
	}
}

func TestUpdateDerivesFromInheritedColumns(t *testing.T) {
	f := eoNewSvcFixture(t,
		eoSvcField("note", models.EORequiredNever),
		eoDerivedField("reminder_date", "due_date - days(1)", eoOwn("due_date")),
		eoDerivedField("letter_deadline", "ancestor_eo_programme_due_date - days(3)", []map[string]any{
			{"scope": "ancestor", "type": "eo_programme", "field": "due_date"},
		}),
	)
	programmeType := f.types.add(&models.EORecordType{Key: "eo_programme", Name: "Programme", IsActive: true})
	programmeDue := time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)
	programme := &models.EORecord{
		ID: uuid.New(), RecordTypeID: programmeType.ID, Reference: "EO-PRG-2026-007",
		DueDate: &programmeDue,
	}
	f.records.stored[programme.ID] = programme

	ownDue := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	rec := eoSvcExisting(f, func(r *models.EORecord) {
		r.ParentRecordID = &programme.ID
		r.DueDate = &ownDue
	})

	got, err := f.svc.Update(context.Background(), rec.ID, &models.EORecordUpdateRequest{
		RecordTypeID: rec.RecordTypeID,
		Values:       map[string]any{"note": "touched"},
	}, f.actor)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	values := eoSvcValues(t, got.Values)
	// due_date is a column, not a values key. A formula that cannot see the
	// columns resolves neither of these and the save succeeds with both
	// fields simply never computed.
	if values["reminder_date"] != "2026-04-09T00:00:00Z" {
		t.Errorf("reminder_date = %v, want 2026-04-09T00:00:00Z derived from the record's own due_date column", values["reminder_date"])
	}
	// Spec §7.5: a Referral deadline is counted back from the parent letter's
	// external deadline — an ancestor's due_date, also a column.
	if values["letter_deadline"] != "2026-05-17T00:00:00Z" {
		t.Errorf("letter_deadline = %v, want 2026-05-17T00:00:00Z derived from the ancestor's due_date column", values["letter_deadline"])
	}
}

func TestEOClassifyDeriveErrorSeparatesMissingDataFromBrokenConfiguration(t *testing.T) {
	pendingErr := fmt.Errorf("derived field %q: %w", "total_weeks", eoformula.ErrUnresolvedReference)
	cycleErr := fmt.Errorf("derived field %q: %w", "a", ErrEODeriveCycle)
	configErr := errors.New(`derived field "b": unknown recompute_on scope "cousin"`)

	cases := []struct {
		name        string
		err         error
		wantPending int
		wantBroken  int
	}{
		{"nothing at all", nil, 0, 0},
		{"a roll-up with no data yet", pendingErr, 1, 0},
		{"a cycle is a configuration fault", cycleErr, 0, 1},
		{"an unknown scope is a configuration fault", configErr, 0, 1},
		{"a mixture is split", errors.Join(pendingErr, cycleErr, configErr), 1, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pending, broken := eoClassifyDeriveError(tc.err)
			if len(pending) != tc.wantPending || len(broken) != tc.wantBroken {
				t.Errorf("pending = %v, broken = %v, want %d pending and %d broken",
					pending, broken, tc.wantPending, tc.wantBroken)
			}
		})
	}
}

func TestUpdateJournalsTheFieldChange(t *testing.T) {
	f := eoNewSvcFixture(t, eoSvcField("note", models.EORequiredNever))
	rec := eoSvcExisting(f, nil)

	if _, err := f.svc.Update(context.Background(), rec.ID, &models.EORecordUpdateRequest{
		RecordTypeID: rec.RecordTypeID,
		Values: map[string]any{
			"note":          "edited",
			"priority":      "low",
			"change_reason": "the owning body asked for it",
		},
	}, f.actor); err != nil {
		t.Fatalf("update: %v", err)
	}

	if len(f.records.history) != 1 {
		t.Fatalf("history rows = %d, want 1: a field edit is not a transition, but it is still a change to a government record", len(f.records.history))
	}
	h := f.records.history[0]
	if h.RecordID != rec.ID || h.RecordTypeID != rec.RecordTypeID {
		t.Errorf("history row points at %s/%s, want %s/%s", h.RecordID, h.RecordTypeID, rec.ID, rec.RecordTypeID)
	}
	if h.TransitionID != nil || h.ToStateID != nil || h.FromStateID != nil {
		t.Errorf("history row = transition %v, %v -> %v, want all nil: no transition took place", h.TransitionID, h.FromStateID, h.ToStateID)
	}
	if h.PerformedByID == nil || *h.PerformedByID != f.actor.ID {
		t.Errorf("performed_by = %v, want the actor %s", h.PerformedByID, f.actor.ID)
	}
	if h.Comment != "the owning body asked for it" {
		t.Errorf("comment = %q, want the submitted change_reason", h.Comment)
	}
	before, after := eoSvcValues(t, h.OldValues), eoSvcValues(t, h.NewValues)
	if before["note"] != "original" || after["note"] != "edited" {
		t.Errorf("note journalled %v -> %v, want original -> edited", before["note"], after["note"])
	}
	if before["priority"] != "high" || after["priority"] != "low" {
		t.Errorf("priority journalled %v -> %v, want high -> low", before["priority"], after["priority"])
	}
	if _, ok := after["subject_ar"]; ok {
		t.Errorf("new values = %v, want only the keys that changed", after)
	}

	t.Run("an edit that changes nothing is not journalled", func(t *testing.T) {
		f := eoNewSvcFixture(t, eoSvcField("note", models.EORequiredNever))
		rec := eoSvcExisting(f, nil)
		if _, err := f.svc.Update(context.Background(), rec.ID, &models.EORecordUpdateRequest{
			RecordTypeID: rec.RecordTypeID,
			Values:       map[string]any{"note": "original"},
		}, f.actor); err != nil {
			t.Fatalf("update: %v", err)
		}
		if len(f.records.history) != 0 {
			t.Errorf("history rows = %d, want 0 for an edit that changed nothing", len(f.records.history))
		}
	})
}

func TestUpdateRejectsAnUnknownRecord(t *testing.T) {
	f := eoNewSvcFixture(t)
	_, err := f.svc.Update(context.Background(), uuid.New(), &models.EORecordUpdateRequest{
		RecordTypeID: f.recordType.ID,
	}, f.actor)
	if !errors.Is(err, ErrEORecordNotFound) {
		t.Errorf("error = %v, want %v", err, ErrEORecordNotFound)
	}
}

// ── capability timers at creation ───────────────────────────────────────────

func eoCreateBridgeFixture(t *testing.T, c Capability) (*eoSvcFixture, *eoBridgeTimers) {
	t.Helper()
	f := eoNewSvcFixture(t)
	timers := &eoBridgeTimers{}
	f.records.txTimers = timers
	f.svc.(*eoRecordService).caps = func(*models.EORecordType) ([]EOEnabledCapability, error) {
		return []EOEnabledCapability{{Capability: c, Config: datatypes.JSON(eoBridgeCfg)}}, nil
	}
	return f, timers
}

func eoCreateBridgeRecord(f *eoSvcFixture) (*models.EORecord, error) {
	return f.svc.Create(context.Background(), &models.EORecordCreateRequest{
		RecordTypeID: f.recordType.ID, Values: map[string]any{"subject_ar": "x"},
	}, f.actor)
}

func TestEoCreateSchedulesCapabilityTimers(t *testing.T) {
	c := &eoBridgeCap{key: "eo.bridge", timers: []models.EOTimer{{FireAt: time.Now(), Payload: datatypes.JSON(`{}`)}}}
	f, timers := eoCreateBridgeFixture(t, c)
	rec, err := eoCreateBridgeRecord(f)
	if err != nil {
		t.Fatal(err)
	}
	if len(timers.scheduled) != 1 {
		t.Fatalf("scheduled %d timers, want 1", len(timers.scheduled))
	}
	got := timers.scheduled[0]
	if got.RecordID != rec.ID || got.RecordTypeID != rec.RecordTypeID || got.CapabilityKey != "eo.bridge" {
		t.Errorf("timer not stamped: %+v", got)
	}
	if c.seenState == nil || *c.seenState != f.initial.ID {
		t.Errorf("Timers saw state %v, want the initial state %v", c.seenState, f.initial.ID)
	}
	if string(c.seenCfg) != eoBridgeCfg {
		t.Errorf("Timers saw config %s, want %s", c.seenCfg, eoBridgeCfg)
	}
	for _, op := range timers.ops {
		if strings.HasPrefix(op, "cancel:") {
			t.Errorf("create issued %q; a new record has nothing to cancel", op)
		}
	}
}

func TestEoCreateRejectsForeignCapabilityKey(t *testing.T) {
	c := &eoBridgeCap{key: "eo.mine", timers: []models.EOTimer{{FireAt: time.Now(), CapabilityKey: "eo.other"}}}
	f, timers := eoCreateBridgeFixture(t, c)
	if _, err := eoCreateBridgeRecord(f); err == nil {
		t.Fatal("a timer keyed to another capability was accepted")
	}
	if len(timers.scheduled) != 0 {
		t.Errorf("scheduled %d timers, want none", len(timers.scheduled))
	}
}

// ── the Documenta folder every record owns (Task C2) ────────────────────────

// eoFolderCall is one folder call the create pipeline made, as the fake saw it.
type eoFolderCall struct {
	workspace string
	parent    string
	name      string
}

// eoFakeDocumenta is a DocumentaClient that records the folder calls the
// create pipeline makes. It embeds the interface and overrides ONLY
// CreateFolder and ListFiles: every other method is a nil interface call, so
// a pipeline that reached for EnsureFolder — the client whose swallowed
// ListFiles error makes a duplicate folder on every transient failure — would
// panic here rather than quietly pass.
//
// Its ids are stable ("folder-1", "folder-2", …) ON PURPOSE. The shipped stub
// (storage.NewStubDocumentaClient) returns a fresh random uuid per call, so a
// test asserting a particular id against the stub proves nothing; these ids
// are the fake's, and asserting them is an assertion about which call's id
// landed on the record.
type eoFakeDocumenta struct {
	storage.DocumentaClient

	// root is what ListFiles answers for the workspace root.
	root []storage.DmsFile
	// listErr fails ListFiles, standing in for a transient Documenta outage
	// in the middle of anchor resolution.
	listErr error
	// createErr fails CreateFolder from createErrFrom onwards (1-based; 0
	// means every call). The anchor is created first, so failing only the
	// second call is the only way to reach the record folder's own error.
	createErr     error
	createErrFrom int
	// emptyIDFrom makes CreateFolder succeed with a blank id from that call
	// onwards (1-based; 0 never), which is what Documenta returns when its
	// response envelope carries no uuid. "" is the no-folder sentinel on the
	// record, so it must never be stored as one — and the anchor and the
	// record folder each need their own guard, hence the call number.
	emptyIDFrom int
	// onCall runs on entry to every call, so a test can observe what was true
	// of the service at the moment it reached out.
	onCall func()

	lists   []eoFolderCall
	creates []eoFolderCall
	n       int
}

func (c *eoFakeDocumenta) ListFiles(_ context.Context, workspace, parent string) (*storage.DmsListResult, error) {
	if c.onCall != nil {
		c.onCall()
	}
	c.lists = append(c.lists, eoFolderCall{workspace: workspace, parent: parent})
	if c.listErr != nil {
		return nil, c.listErr
	}
	return &storage.DmsListResult{Files: c.root}, nil
}

func (c *eoFakeDocumenta) CreateFolder(_ context.Context, workspace, parent, name string) (string, error) {
	if c.onCall != nil {
		c.onCall()
	}
	c.creates = append(c.creates, eoFolderCall{workspace: workspace, parent: parent, name: name})
	if c.createErr != nil && len(c.creates) >= c.createErrFrom {
		return "", c.createErr
	}
	c.n++
	if c.emptyIDFrom > 0 && c.n >= c.emptyIDFrom {
		return "", nil
	}
	return "folder-" + strconv.Itoa(c.n), nil
}

// eoFoldersFixture is eoNewSvcFixture with the record service rebuilt around a
// Documenta client, and an actor who has an email to be acted on behalf of.
func eoFoldersFixture(t *testing.T, client storage.DocumentaClient) *eoSvcFixture {
	t.Helper()
	f := eoNewSvcFixture(t)
	f.actor.Email = "eo.officer@example.gov"
	f.svc = NewEORecordService(f.records, f.types, f.sequences, f.workflows,
		EOWithDocumentaFolders(client, "eo-workspace"))
	return f
}

// eoCreateForFolder creates one record through the fixture's service.
func eoCreateForFolder(t *testing.T, f *eoSvcFixture, subjectEn string) (*models.EORecord, error) {
	t.Helper()
	return f.svc.Create(context.Background(), &models.EORecordCreateRequest{
		RecordTypeID: f.recordType.ID,
		Values:       map[string]any{"subject_ar": "توجيه", "subject_en": subjectEn},
	}, f.actor)
}

// eoFolderNotes returns the history rows that report a missing folder.
func eoFolderNotes(history []models.EORecordHistory) []models.EORecordHistory {
	var out []models.EORecordHistory
	for _, h := range history {
		if strings.Contains(h.Comment, eoFolderFailureNote) {
			out = append(out, h)
		}
	}
	return out
}

func TestCreateGivesTheRecordADocumentaFolder(t *testing.T) {
	t.Run("the folder id is stored on the record and on the row that is inserted", func(t *testing.T) {
		client := &eoFakeDocumenta{}
		f := eoFoldersFixture(t, client)

		rec, err := eoCreateForFolder(t, f, "Directive one")
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		// Two creates: the per-type anchor first (nothing in the root), then
		// the record's own folder under it. The record gets the SECOND id.
		if rec.DocumentaFolderID != "folder-2" {
			t.Errorf("rec.DocumentaFolderID = %q, want the record folder's id %q", rec.DocumentaFolderID, "folder-2")
		}
		if len(f.records.created) != 1 {
			t.Fatalf("records created = %d, want 1", len(f.records.created))
		}
		// The INSERTED row, not just the returned struct: the folder id has to
		// be on the record before it is written, or the column stays '' until
		// something updates it and a crash in between loses the folder.
		if saved := f.records.created[0]; saved.DocumentaFolderID != "folder-2" {
			t.Errorf("inserted documenta_folder_id = %q, want %q", saved.DocumentaFolderID, "folder-2")
		}
		if notes := eoFolderNotes(f.records.history); len(notes) != 0 {
			t.Errorf("history carries %d folder-failure notes, want none on a successful create: %+v", len(notes), notes)
		}
	})

	t.Run("the folder is created under a per-type anchor, in the configured workspace", func(t *testing.T) {
		client := &eoFakeDocumenta{}
		f := eoFoldersFixture(t, client)

		rec, err := eoCreateForFolder(t, f, "Directive one")
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if len(client.creates) != 2 {
			t.Fatalf("CreateFolder calls = %+v, want 2 (the anchor, then the record)", client.creates)
		}
		anchor, folder := client.creates[0], client.creates[1]
		if anchor.parent != "" || anchor.name != f.recordType.Name {
			t.Errorf("anchor call = %+v, want it at the workspace root named %q", anchor, f.recordType.Name)
		}
		if folder.parent != "folder-1" {
			t.Errorf("record folder parent = %q, want the anchor %q", folder.parent, "folder-1")
		}
		wantName := rec.Reference + " - Directive one"
		if folder.name != wantName {
			t.Errorf("record folder name = %q, want %q", folder.name, wantName)
		}
		// EO_DOCUMENTA_WORKSPACE_NAME reaches Documenta only as this per-call
		// argument; the client constructor ignores cfg.WorkspaceName.
		for _, c := range append(append([]eoFolderCall{}, client.creates...), client.lists...) {
			if c.workspace != "eo-workspace" {
				t.Errorf("call %+v used workspace %q, want the configured %q", c, c.workspace, "eo-workspace")
			}
		}
	})

	t.Run("the folder call is made outside the create transaction", func(t *testing.T) {
		client := &eoFakeDocumenta{}
		f := eoFoldersFixture(t, client)
		var sawTx []bool
		client.onCall = func() { sawTx = append(sawTx, f.records.inTx) }

		if _, err := eoCreateForFolder(t, f, "Directive one"); err != nil {
			t.Fatalf("create: %v", err)
		}
		if len(sawTx) == 0 {
			t.Fatalf("the pipeline made no Documenta call")
		}
		for i, in := range sawTx {
			if in {
				t.Errorf("Documenta call %d ran inside the create transaction; a remote call must not hold a pooled connection", i)
			}
		}
	})

	t.Run("no Documenta client wired leaves the record folderless and says nothing", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			svc  func(f *eoSvcFixture) EORecordService
		}{
			{
				name: "the option is not passed at all",
				svc: func(f *eoSvcFixture) EORecordService {
					return NewEORecordService(f.records, f.types, f.sequences, f.workflows)
				},
			},
			{
				// main.go always passes a client, but a nil one must not turn
				// every create into a nil-pointer panic.
				name: "the option is passed a nil client",
				svc: func(f *eoSvcFixture) EORecordService {
					return NewEORecordService(f.records, f.types, f.sequences, f.workflows,
						EOWithDocumentaFolders(nil, "eo-workspace"))
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := eoNewSvcFixture(t)
				f.svc = tc.svc(f)
				rec, err := eoCreateForFolder(t, f, "Directive one")
				if err != nil {
					t.Fatalf("create: %v", err)
				}
				if rec.DocumentaFolderID != "" {
					t.Errorf("documenta_folder_id = %q, want empty when no client is wired", rec.DocumentaFolderID)
				}
				if notes := eoFolderNotes(f.records.history); len(notes) != 0 {
					t.Errorf("history carries %d folder notes, want none when folders are not configured", len(notes))
				}
			})
		}
	})
}

// TestEOAFolderNoteThatCannotBeWrittenFailsTheCreate pins the other half of
// the Review Focus 4 decision: the record survives a Documenta outage only
// BECAUSE the reason is recorded with it. If that row cannot be written, the
// create fails and the real repository's transaction rolls the record back,
// rather than committing a folderless record that explains nothing.
func TestEOAFolderNoteThatCannotBeWrittenFailsTheCreate(t *testing.T) {
	client := &eoFakeDocumenta{listErr: errors.New("documenta: connection refused")}
	f := eoFoldersFixture(t, client)
	// The creation row goes through; the folder note, the second row, does not.
	f.records.historyErr = errors.New("write eo record history: deadlock detected")
	f.records.historyErrFrom = 2

	if _, err := eoCreateForFolder(t, f, "Directive one"); err == nil {
		t.Fatal("create succeeded although the folder note could not be written")
	} else if !strings.Contains(err.Error(), "deadlock detected") {
		t.Errorf("create error = %v, want it to carry the history failure", err)
	}
}

func TestFolderCreationFailureDoesNotLoseTheRecord(t *testing.T) {
	// The decision this task had to make (Review Focus 4): a Documenta outage
	// during a create does NOT block the record. The record is written with an
	// empty documenta_folder_id — which C1 made a legible value rather than a
	// null — and is backfillable. What makes that honest rather than silent is
	// the history row: it is written in the SAME transaction as the record, on
	// the record it happened to, so it cannot be lost and cannot be filed
	// against the wrong record the way A5's close_parent note was.
	cases := []struct {
		name    string
		client  *eoFakeDocumenta
		wantErr string
		// creates is how many CreateFolder calls the failure should leave.
		creates int
	}{
		{
			name:    "the anchor folder cannot be created",
			client:  &eoFakeDocumenta{createErr: errors.New("documenta: request failed (503)"), createErrFrom: 1},
			wantErr: "503",
			creates: 1,
		},
		{
			// The anchor is made first, so only failing the SECOND call
			// reaches the record folder's own failure.
			name:    "the record folder cannot be created",
			client:  &eoFakeDocumenta{createErr: errors.New("documenta: request failed (507)"), createErrFrom: 2},
			wantErr: "507",
			creates: 2,
		},
		{
			name:    "the workspace cannot be listed, so the anchor cannot be resolved",
			client:  &eoFakeDocumenta{listErr: errors.New("documenta: dial tcp: connection refused")},
			wantErr: "connection refused",
			creates: 0, // nothing is created on a listing failure — see the retry test
		},
		{
			name:    "documenta answers with a blank id for the anchor",
			client:  &eoFakeDocumenta{emptyIDFrom: 1},
			wantErr: `empty folder id for "Directive"`,
			creates: 1,
		},
		{
			name:    "documenta answers with a blank id for the record folder",
			client:  &eoFakeDocumenta{emptyIDFrom: 2},
			wantErr: "empty folder id for EO-DIR-",
			creates: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := eoFoldersFixture(t, tc.client)

			rec, err := eoCreateForFolder(t, f, "Directive one")
			if err != nil {
				t.Fatalf("create failed on a Documenta failure: %v; the decision is that the record survives", err)
			}
			if rec.DocumentaFolderID != "" {
				t.Errorf("documenta_folder_id = %q, want empty: there is no folder", rec.DocumentaFolderID)
			}
			if len(f.records.created) != 1 {
				t.Fatalf("records created = %d, want the record to be written anyway", len(f.records.created))
			}
			if saved := f.records.created[0]; saved.Reference == "" || saved.DocumentaFolderID != "" {
				t.Errorf("inserted row = {reference:%q folder:%q}, want a referenced record with no folder", saved.Reference, saved.DocumentaFolderID)
			}
			if len(tc.client.creates) != tc.creates {
				t.Errorf("CreateFolder calls = %d (%+v), want %d", len(tc.client.creates), tc.client.creates, tc.creates)
			}

			notes := eoFolderNotes(f.records.history)
			if len(notes) != 1 {
				t.Fatalf("folder-failure history rows = %d, want exactly 1: %+v", len(notes), f.records.history)
			}
			note := notes[0]
			// On the record it happened to — the A5 lesson.
			if note.RecordID != rec.ID || note.RecordTypeID != rec.RecordTypeID {
				t.Errorf("note is on %s/%s, want the record it happened to, %s/%s", note.RecordID, note.RecordTypeID, rec.ID, rec.RecordTypeID)
			}
			// Operators find the affected record by its reference, so the note
			// has to name it, and it has to carry the reason.
			if !strings.Contains(note.Comment, rec.Reference) {
				t.Errorf("note %q does not name the record %q", note.Comment, rec.Reference)
			}
			if !strings.Contains(note.Comment, tc.wantErr) {
				t.Errorf("note %q does not carry the failure %q", note.Comment, tc.wantErr)
			}
			if !note.IsSystemAction {
				t.Errorf("note is not marked a system action; nobody performed it")
			}
			// It must not read as a state entry, or "has this record been in
			// state X" starts counting a folder failure as a transition.
			if note.ToStateID != nil || note.FromStateID != nil {
				t.Errorf("note carries states %v -> %v, want none", note.FromStateID, note.ToStateID)
			}
			// And the creation row is still there, unchanged.
			if len(f.records.history) != 2 {
				t.Errorf("history rows = %d, want the creation row and the note", len(f.records.history))
			}
		})
	}
}

func TestNoDuplicateFolderOnRetry(t *testing.T) {
	t.Run("a second record reuses the anchor instead of making another", func(t *testing.T) {
		client := &eoFakeDocumenta{}
		f := eoFoldersFixture(t, client)

		first, err := eoCreateForFolder(t, f, "Directive one")
		if err != nil {
			t.Fatalf("create 1: %v", err)
		}
		second, err := eoCreateForFolder(t, f, "Directive two")
		if err != nil {
			t.Fatalf("create 2: %v", err)
		}
		if len(client.creates) != 3 {
			t.Fatalf("CreateFolder calls = %+v, want 3 (one anchor, two records)", client.creates)
		}
		anchors := 0
		for _, c := range client.creates {
			if c.parent == "" {
				anchors++
			}
		}
		if anchors != 1 {
			t.Errorf("root-level folders created = %d, want exactly 1 anchor", anchors)
		}
		if first.DocumentaFolderID == second.DocumentaFolderID {
			t.Errorf("both records got folder %q; each record owns its own", first.DocumentaFolderID)
		}
		// The anchor is resolved once per type and remembered, so the second
		// create does not list the workspace root again.
		if len(client.lists) != 1 {
			t.Errorf("ListFiles calls = %d, want 1: the anchor is resolved once per type", len(client.lists))
		}
	})

	t.Run("a restarted process adopts the anchor that is already there", func(t *testing.T) {
		// The decoy FILE is listed first, and shares the anchor's name: an
		// anchor lookup that matched on name alone would adopt it and file
		// every record of this type under a file id.
		client := &eoFakeDocumenta{root: []storage.DmsFile{
			{UUID: "a-file", Name: "Directive", Type: "file"},
			{UUID: "existing-anchor", Name: "Directive", Type: "folder"},
		}}
		f := eoFoldersFixture(t, client)

		rec, err := eoCreateForFolder(t, f, "Directive one")
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if len(client.creates) != 1 {
			t.Fatalf("CreateFolder calls = %+v, want 1: the anchor already exists", client.creates)
		}
		if client.creates[0].parent != "existing-anchor" {
			t.Errorf("record folder parent = %q, want the anchor already in the workspace", client.creates[0].parent)
		}
		if rec.DocumentaFolderID != "folder-1" {
			t.Errorf("documenta_folder_id = %q, want the id of the one folder that was created", rec.DocumentaFolderID)
		}
	})

	t.Run("a transient listing failure creates nothing at all", func(t *testing.T) {
		// This is the trap EnsureFolder walks into: it swallows its ListFiles
		// error and falls through to create, and the EO client has no Redis
		// tier to short-circuit it, so a flapping Documenta would leave one
		// orphan anchor per failed create and split the tree. Here the error
		// is returned, and the NEXT create — once Documenta answers again —
		// adopts the anchor that was there all along.
		client := &eoFakeDocumenta{listErr: errors.New("documenta: connection refused")}
		f := eoFoldersFixture(t, client)

		if _, err := eoCreateForFolder(t, f, "Directive one"); err != nil {
			t.Fatalf("create: %v", err)
		}
		if len(client.creates) != 0 {
			t.Fatalf("CreateFolder calls = %+v, want none: a failed listing says nothing about what exists", client.creates)
		}

		client.listErr = nil
		client.root = []storage.DmsFile{{UUID: "existing-anchor", Name: "Directive", Type: "folder"}}
		rec, err := eoCreateForFolder(t, f, "Directive two")
		if err != nil {
			t.Fatalf("create after recovery: %v", err)
		}
		if len(client.creates) != 1 || client.creates[0].parent != "existing-anchor" {
			t.Errorf("after recovery CreateFolder calls = %+v, want one record folder under the existing anchor", client.creates)
		}
		if rec.DocumentaFolderID == "" {
			t.Errorf("the recovered create got no folder")
		}
	})
}

func TestEOFolderNamesAreLegibleAndBounded(t *testing.T) {
	// 120 is written out rather than taken from eoFolderNameMax: a test that
	// derives its expectation from the constant moves with it and would hold
	// any new limit, however wrong, in place.
	long := strings.Repeat("ت", 400)
	cases := []struct {
		name string
		rec  models.EORecord
		want string
	}{
		{
			name: "reference and english subject",
			rec:  models.EORecord{Reference: "EO-DIR-2026-001", SubjectAr: "توجيه", SubjectEn: "Safety directive"},
			want: "EO-DIR-2026-001 - Safety directive",
		},
		{
			name: "arabic subject when there is no english one",
			rec:  models.EORecord{Reference: "EO-DIR-2026-002", SubjectAr: "توجيه"},
			want: "EO-DIR-2026-002 - توجيه",
		},
		{
			name: "the reference alone when there is no subject",
			rec:  models.EORecord{Reference: "EO-DIR-2026-003"},
			want: "EO-DIR-2026-003",
		},
		{
			name: "a long subject is clipped on a rune boundary",
			rec:  models.EORecord{Reference: "EO-DIR-2026-004", SubjectEn: long},
			want: "EO-DIR-2026-004 - " + strings.Repeat("ت", 120) + "…",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := eoRecordFolderName(&tc.rec); got != tc.want {
				t.Errorf("eoRecordFolderName() = %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("the anchor falls back to the type key when the name is blank", func(t *testing.T) {
		if got := eoAnchorFolderName(&models.EORecordType{Key: "eo_directive", Name: "  "}); got != "eo_directive" {
			t.Errorf("eoAnchorFolderName() = %q, want the key", got)
		}
		if got := eoAnchorFolderName(&models.EORecordType{Key: "eo_directive", Name: "Directive"}); got != "Directive" {
			t.Errorf("eoAnchorFolderName() = %q, want the name", got)
		}
	})
}

// TestEOFolderCallCarriesTheActingUserAndWorkspace drives the REAL HTTP client
// against an httptest server, because the two things it checks are only
// observable on the wire: X-On-Behalf-Of is set from a context value whose key
// is unexported, and the workspace reaches Documenta inside the request body.
// Without the first, every EO folder is owned by system@automax.local and the
// Documenta audit trail cannot say who filed what.
func TestEOFolderCallCarriesTheActingUserAndWorkspace(t *testing.T) {
	type seen struct {
		onBehalf  string
		workspace string
	}
	var got []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/oauth/token"):
			// Not a JWT, so the client resolves no workspace_uuid claim and
			// falls back to the per-call workspace name — which is the only
			// way EO_DOCUMENTA_WORKSPACE_NAME can reach Documenta at all.
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"access_token":"opaque-token","expires_in":3600}`)
		case strings.HasSuffix(r.URL.Path, "/files/folder"):
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			got = append(got, seen{onBehalf: r.Header.Get("X-On-Behalf-Of"), workspace: body["workspaceUuid"]})
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"success":true,"data":{"uuid":"real-folder"}}`)
		case strings.HasSuffix(r.URL.Path, "/files"):
			got = append(got, seen{onBehalf: r.Header.Get("X-On-Behalf-Of"), workspace: r.URL.Query().Get("workspace")})
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"success":true,"data":{"files":[]}}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := storage.NewHTTPDocumentaClient(config.DocumentaConfig{
		BaseURL: srv.URL, ClientID: "eo", ClientSecret: "secret", WorkspaceName: "ignored-by-the-constructor",
	}, nil)

	f := eoNewSvcFixture(t)
	f.actor.Email = "eo.officer@example.gov"
	f.svc = NewEORecordService(f.records, f.types, f.sequences, f.workflows,
		EOWithDocumentaFolders(client, "eo-workspace"))

	rec, err := eoCreateForFolder(t, f, "Directive one")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if rec.DocumentaFolderID != "real-folder" {
		t.Errorf("documenta_folder_id = %q, want the id the server returned", rec.DocumentaFolderID)
	}
	if len(got) == 0 {
		t.Fatalf("the server saw no folder requests")
	}
	for i, g := range got {
		if g.onBehalf != f.actor.Email {
			t.Errorf("request %d X-On-Behalf-Of = %q, want the acting user %q", i, g.onBehalf, f.actor.Email)
		}
		if g.workspace != "eo-workspace" {
			t.Errorf("request %d workspace = %q, want the per-call workspace name %q", i, g.workspace, "eo-workspace")
		}
	}
}
