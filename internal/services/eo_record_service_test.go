package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/internal/services/eoformula"
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
	if f.historyErr != nil {
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
