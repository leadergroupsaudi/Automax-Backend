package services

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/google/uuid"
	"gorm.io/datatypes"
)

// ── fakes ───────────────────────────────────────────────────────────────────
// Each embeds the interface and overrides only what the action executor calls,
// so a method it starts using surfaces as a nil-pointer panic. No database.

type eoAxSent struct {
	Channel  string
	Template *string
	Language string
	To       []string
	Subject  string
	Body     string
	Vars     map[string]string
}

type eoAxNotifier struct {
	mu   sync.Mutex
	sent []eoAxSent
	err  error
	// ctxErrs is ctx.Err() as each send saw it.
	ctxErrs []error
	// panicOn makes a send with this subject panic.
	panicOn string
}

func (n *eoAxNotifier) SendNotification(ctx context.Context, channel string, tpl *string, lang string, to, _, _ []string, subject, body string, vars map[string]string, _ []models.AttachmentData, _ *uuid.UUID, _ *uuid.UUID) (*SendNotificationResult, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.panicOn != "" && subject == n.panicOn {
		panic("notifier exploded")
	}
	n.ctxErrs = append(n.ctxErrs, ctx.Err())
	n.sent = append(n.sent, eoAxSent{channel, tpl, lang, to, subject, body, vars})
	return &SendNotificationResult{}, n.err
}

func (n *eoAxNotifier) subjects() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []string
	for _, s := range n.sent {
		out = append(out, s.Subject)
	}
	return out
}

type eoAxUsers struct {
	repository.UserRepository
	byID map[uuid.UUID]*models.User
}

func (u *eoAxUsers) FindByID(_ context.Context, id uuid.UUID) (*models.User, error) {
	if x, ok := u.byID[id]; ok {
		return x, nil
	}
	return nil, errors.New("user not found")
}

type eoAxDepts struct {
	repository.DepartmentRepository
	byID map[uuid.UUID]*models.Department
}

func (d *eoAxDepts) FindByID(_ context.Context, id uuid.UUID) (*models.Department, error) {
	if x, ok := d.byID[id]; ok {
		return x, nil
	}
	return nil, errors.New("department not found")
}

// eoAxRecSvc is stateful: Create adds a child the repo fake then reports, so a
// second run of the same transition sees the first run's child.
type eoAxRecSvc struct {
	EORecordService
	repo        *eoAxRepo
	created     []*models.EORecordCreateRequest
	updated     []*models.EORecordUpdateRequest
	updatedIDs  []uuid.UUID
	createErr   error
	updateErr   error
	updateActor []*models.User
}

func (s *eoAxRecSvc) Create(_ context.Context, req *models.EORecordCreateRequest, _ *models.User) (*models.EORecord, error) {
	s.created = append(s.created, req)
	if s.createErr != nil {
		return nil, s.createErr
	}
	child := models.EORecord{ID: uuid.New(), RecordTypeID: req.RecordTypeID, ParentRecordID: req.ParentRecordID}
	s.repo.children = append(s.repo.children, child)
	return &child, nil
}

func (s *eoAxRecSvc) Update(_ context.Context, id uuid.UUID, req *models.EORecordUpdateRequest, actor *models.User) (*models.EORecord, error) {
	s.updated = append(s.updated, req)
	s.updatedIDs = append(s.updatedIDs, id)
	s.updateActor = append(s.updateActor, actor)
	return &models.EORecord{ID: id}, s.updateErr
}

type eoAxRepo struct {
	repository.EORecordRepository
	children []models.EORecord
	history  []models.EORecordHistory
}

func (r *eoAxRepo) Children(_ context.Context, parentID uuid.UUID, typeID *uuid.UUID) ([]models.EORecord, error) {
	var out []models.EORecord
	for _, c := range r.children {
		if c.ParentRecordID != nil && *c.ParentRecordID == parentID && (typeID == nil || c.RecordTypeID == *typeID) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (r *eoAxRepo) CreateHistory(_ context.Context, h *models.EORecordHistory) error {
	r.history = append(r.history, *h)
	return nil
}

type eoAxTypes struct {
	repository.EORecordTypeRepository
	byKey  map[string]*models.EORecordType
	fields []models.EOFieldDef
}

func (t *eoAxTypes) GetByKey(_ context.Context, key string) (*models.EORecordType, error) {
	return t.byKey[key], nil
}
func (t *eoAxTypes) Ancestors(context.Context, *models.EORecordType) ([]models.EORecordType, error) {
	return nil, nil
}
func (t *eoAxTypes) FieldDefs(context.Context, []uuid.UUID) ([]models.EOFieldDef, error) {
	return t.fields, nil
}

// ── fixture ─────────────────────────────────────────────────────────────────

type eoAxFixture struct {
	exec     *eoActionExecutor
	notifier *eoAxNotifier
	svc      *eoAxRecSvc
	repo     *eoAxRepo
	users    *eoAxUsers
	depts    *eoAxDepts
	rec      *models.EORecord
	actor    *models.User
	taskType *models.EORecordType
	manager  *models.User
	assignee *models.User
}

func eoAxNewFixture(t *testing.T) *eoAxFixture {
	t.Helper()
	assignee := &models.User{ID: uuid.New(), Email: "assignee@x.test", Phone: "0500000001", FirstName: "Asa", LastName: "Signee"}
	creator := &models.User{ID: uuid.New(), Email: "creator@x.test", Phone: "0500000002"}
	manager := &models.User{ID: uuid.New(), Email: "manager@x.test", Phone: "0500000003"}
	actor := &models.User{ID: uuid.New(), Email: "actor@x.test", FirstName: "Ann", LastName: "Actor"}
	dept := &models.Department{ID: uuid.New(), ManagerID: &manager.ID}
	typeID := uuid.New()
	taskType := &models.EORecordType{ID: uuid.New(), Key: "task", IsActive: true}

	rec := &models.EORecord{
		ID: uuid.New(), RecordTypeID: typeID, Reference: "EO-DIR-2026-001",
		SubjectAr: "Build the thing", AssignedUserID: &assignee.ID, CreatedByID: creator.ID,
		OwningBodyID: &dept.ID, Priority: "high",
		Values: datatypes.JSON(`{"budget":"1200"}`),
	}

	repo := &eoAxRepo{}
	svc := &eoAxRecSvc{repo: repo}
	notifier := &eoAxNotifier{}
	users := &eoAxUsers{byID: map[uuid.UUID]*models.User{assignee.ID: assignee, creator.ID: creator, manager.ID: manager}}
	depts := &eoAxDepts{byID: map[uuid.UUID]*models.Department{dept.ID: dept}}
	types := &eoAxTypes{
		byKey: map[string]*models.EORecordType{"task": taskType},
		fields: []models.EOFieldDef{
			{RecordTypeID: taskType.ID, Key: "subject_ar", Kind: "text"},
			{RecordTypeID: taskType.ID, Key: "priority", Kind: "text"},
			{RecordTypeID: taskType.ID, Key: "task_budget", Kind: "text"},
		},
	}
	return &eoAxFixture{
		exec:     NewEOActionExecutor(svc, repo, types, users, depts, notifier).(*eoActionExecutor),
		notifier: notifier, svc: svc, repo: repo, users: users, depts: depts,
		rec: rec, actor: actor, taskType: taskType, manager: manager, assignee: assignee,
	}
}

func eoAxAction(typ string, order int, cfg any) models.TransitionAction {
	b, _ := json.Marshal(cfg)
	return models.TransitionAction{ID: uuid.New(), ActionType: typ, Name: typ, Config: string(b), ExecutionOrder: order, IsActive: true}
}

func eoAxTransition(actions ...models.TransitionAction) *models.WorkflowTransition {
	return &models.WorkflowTransition{ID: uuid.New(), Code: "approve", Name: "Approve", Actions: actions}
}

func eoAxNotif(subject string, order int, recips ...string) models.TransitionAction {
	return eoAxAction("notification", order, map[string]any{"recipients": recips, "title": subject, "message": "m"})
}

// ── ExecuteActions ──────────────────────────────────────────────────────────

func TestExecuteActionsSortsByExecutionOrder(t *testing.T) {
	f := eoAxNewFixture(t)
	r := "assignee"
	off := eoAxNotif("off", 0, r)
	off.IsActive = false
	tr := eoAxTransition(
		eoAxNotif("third", 3, r), eoAxNotif("first", 1, r), off,
		eoAxNotif("second-a", 2, r), eoAxNotif("second-b", 2, r),
	)
	if err := f.exec.ExecuteActions(context.Background(), f.rec, tr, f.actor); err != nil {
		t.Fatalf("ExecuteActions: %v", err)
	}
	got := f.notifier.subjects()
	want := []string{"first", "second-a", "second-b", "third"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v (inactive skipped, ties keep configured order)", got, want)
	}
	// the caller's slice must not be reordered
	if tr.Actions[0].Name != "notification" || !strings.Contains(tr.Actions[0].Config, "third") {
		t.Fatalf("transition.Actions was reordered in place")
	}
}

func TestExecuteActionsNeverFailsTheTransition(t *testing.T) {
	f := eoAxNewFixture(t)
	f.notifier.panicOn = "boom"
	tr := eoAxTransition(
		eoAxAction("unknown_kind", 1, map[string]any{}),
		models.TransitionAction{ActionType: "notification", Name: "bad", Config: "{not json", ExecutionOrder: 2, IsActive: true},
		eoAxNotif("boom", 3, "assignee"),
		eoAxNotif("survivor", 4, "assignee"),
	)
	if err := f.exec.ExecuteActions(context.Background(), f.rec, tr, f.actor); err != nil {
		t.Fatalf("ExecuteActions returned %v; a failing action must never fail the transition", err)
	}
	if got := f.notifier.subjects(); !reflect.DeepEqual(got, []string{"survivor"}) {
		t.Fatalf("later actions must still run after failures and a panic; sent %v", got)
	}

	f2 := eoAxNewFixture(t)
	f2.notifier.err = errors.New("smtp down")
	if err := f2.exec.ExecuteActions(context.Background(), f2.rec, eoAxTransition(eoAxNotif("x", 1, "assignee")), f2.actor); err != nil {
		t.Fatalf("handler error leaked: %v", err)
	}
}

func TestExecuteActionsRunsAsyncActionsInTheBackground(t *testing.T) {
	f := eoAxNewFixture(t)
	a := eoAxNotif("bg", 1, "assignee")
	a.IsAsync = true
	if err := f.exec.ExecuteActions(context.Background(), f.rec, eoAxTransition(a), f.actor); err != nil {
		t.Fatal(err)
	}
	f.exec.wait()
	if got := f.notifier.subjects(); !reflect.DeepEqual(got, []string{"bg"}) {
		t.Fatalf("async action did not run: %v", got)
	}
}

func TestExecuteActionsAsyncActionOutlivesItsCaller(t *testing.T) {
	f := eoAxNewFixture(t)
	a := eoAxNotif("bg", 1, "assignee")
	a.IsAsync = true
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the request that dispatched it is already gone
	if err := f.exec.ExecuteActions(ctx, f.rec, eoAxTransition(a), f.actor); err != nil {
		t.Fatal(err)
	}
	f.exec.wait()
	if len(f.notifier.ctxErrs) != 1 || f.notifier.ctxErrs[0] != nil {
		t.Fatalf("async action ran on its caller's cancelled context: %v", f.notifier.ctxErrs)
	}
}

// ── placeholders: both mechanisms ───────────────────────────────────────────

func TestExecuteActionsEmailSuppliesBothPlaceholderMechanisms(t *testing.T) {
	f := eoAxNewFixture(t)
	inline := eoAxAction("email", 1, map[string]any{
		"recipients": []string{"assignee"}, "subject_template": "{{reference}} moved", "body_template": "{{subject_ar}} by {{performed_by_name}}, budget {{budget}}",
	})
	coded := eoAxAction("email", 2, map[string]any{"recipients": []string{"assignee"}, "template_code": "EO_MOVED"})
	if err := f.exec.ExecuteActions(context.Background(), f.rec, eoAxTransition(inline, coded), f.actor); err != nil {
		t.Fatal(err)
	}
	if len(f.notifier.sent) != 2 {
		t.Fatalf("sent %d, want 2", len(f.notifier.sent))
	}
	in := f.notifier.sent[0]
	if in.Subject != "EO-DIR-2026-001 moved" || in.Body != "Build the thing by Ann Actor, budget 1200" {
		t.Fatalf("inline template rendered literally or wrongly: %q / %q", in.Subject, in.Body)
	}
	if in.Template != nil {
		t.Fatalf("inline email must not carry a template code")
	}
	cd := f.notifier.sent[1]
	if cd.Template == nil || *cd.Template != "EO_MOVED" {
		t.Fatalf("template code not passed: %v", cd.Template)
	}
	for k, v := range map[string]string{"reference": "EO-DIR-2026-001", "subject_ar": "Build the thing", "budget": "1200", "transition_name": "Approve"} {
		if cd.Vars[k] != v {
			t.Errorf("template variable %q = %q, want %q (bare keys, for template codes)", k, cd.Vars[k], v)
		}
	}
	if !reflect.DeepEqual(cd.To, []string{"assignee@x.test"}) || cd.Channel != "email" {
		t.Errorf("recipient/channel = %v/%s", cd.To, cd.Channel)
	}
}

func TestExecuteActionsSmsGoesToPhones(t *testing.T) {
	f := eoAxNewFixture(t)
	a := eoAxAction("sms", 1, map[string]any{"recipients": []string{"assignee", "email:someone@x.test"}, "message_template": "{{reference}}"})
	if err := f.exec.ExecuteActions(context.Background(), f.rec, eoAxTransition(a), f.actor); err != nil {
		t.Fatal(err)
	}
	if len(f.notifier.sent) != 1 || f.notifier.sent[0].Channel != "sms" ||
		!reflect.DeepEqual(f.notifier.sent[0].To, []string{"0500000001"}) || f.notifier.sent[0].Body != "EO-DIR-2026-001" {
		t.Fatalf("sms = %+v", f.notifier.sent)
	}
}

func TestExecuteActionsNoRecipientsSendsNothing(t *testing.T) {
	f := eoAxNewFixture(t)
	if err := f.exec.ExecuteActions(context.Background(), f.rec, eoAxTransition(eoAxNotif("x", 1, "nobody-knows")), f.actor); err != nil {
		t.Fatal(err)
	}
	if len(f.notifier.sent) != 0 {
		t.Fatalf("sent to nobody: %+v", f.notifier.sent)
	}
}

// ── webhook ─────────────────────────────────────────────────────────────────

func TestExecuteActionsWebhookPostsEscapedBody(t *testing.T) {
	var gotBody, gotHeader, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody, gotHeader, gotMethod = string(b), r.Header.Get("X-Token"), r.Method
	}))
	defer srv.Close()

	f := eoAxNewFixture(t)
	f.rec.SubjectAr = `say "hi"`
	a := eoAxAction("webhook", 1, map[string]any{"url": srv.URL, "headers": map[string]string{"X-Token": "abc"}, "body": `{"ref":"{{reference}}","subject":"{{subject_ar}}"}`})
	if err := f.exec.ExecuteActions(context.Background(), f.rec, eoAxTransition(a), f.actor); err != nil {
		t.Fatal(err)
	}
	if gotMethod != "POST" || gotHeader != "abc" {
		t.Fatalf("method/header = %s/%s", gotMethod, gotHeader)
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(gotBody), &parsed); err != nil {
		t.Fatalf("webhook body is not valid JSON after substitution: %v: %s", err, gotBody)
	}
	if parsed["subject"] != `say "hi"` || parsed["ref"] != "EO-DIR-2026-001" {
		t.Fatalf("body = %v", parsed)
	}
}

// ── field_update ────────────────────────────────────────────────────────────

func TestFieldUpdateWritesIntoTheValuesBag(t *testing.T) {
	f := eoAxNewFixture(t)
	a := eoAxAction("field_update", 1, map[string]any{"field": "budget", "value": "9000"})
	if err := f.exec.ExecuteActions(context.Background(), f.rec, eoAxTransition(a), f.actor); err != nil {
		t.Fatal(err)
	}
	if len(f.svc.updated) != 1 {
		t.Fatalf("Update calls = %d, want 1", len(f.svc.updated))
	}
	req := f.svc.updated[0]
	if f.svc.updatedIDs[0] != f.rec.ID || req.RecordTypeID != f.rec.RecordTypeID {
		t.Errorf("wrong record addressed: %v / %v", f.svc.updatedIDs[0], req.RecordTypeID)
	}
	if !reflect.DeepEqual(req.Values, map[string]any{"budget": "9000"}) {
		t.Errorf("values = %v", req.Values)
	}
	if f.svc.updateActor[0] != f.actor {
		t.Errorf("update must be attributed to the performing user")
	}
}

func TestFieldUpdateRefusesAnEmptyField(t *testing.T) {
	f := eoAxNewFixture(t)
	a := eoAxAction("field_update", 1, map[string]any{"value": "x"})
	if err := f.exec.ExecuteActions(context.Background(), f.rec, eoAxTransition(a), f.actor); err != nil {
		t.Fatal(err)
	}
	if len(f.svc.updated) != 0 {
		t.Fatalf("a field_update with no field must not write: %+v", f.svc.updated)
	}
}

// ── spawn_record ────────────────────────────────────────────────────────────

func eoAxSpawn(order int, fieldMap map[string]any) models.TransitionAction {
	return eoAxAction("spawn_record", order, map[string]any{"target_type": "task", "link_type": "spawned_task", "field_map": fieldMap})
}

func TestSpawnRecordCreatesChildAndLink(t *testing.T) {
	f := eoAxNewFixture(t)
	a := eoAxSpawn(1, map[string]any{
		"subject_ar":  "{{subject_ar}}",
		"priority":    "{{priority}}",
		"task_budget": "Follow-up of {{reference}}",
	})
	if err := f.exec.ExecuteActions(context.Background(), f.rec, eoAxTransition(a), f.actor); err != nil {
		t.Fatal(err)
	}
	if len(f.svc.created) != 1 {
		t.Fatalf("Create calls = %d, want 1 (through the record service, not the repository)", len(f.svc.created))
	}
	req := f.svc.created[0]
	if req.RecordTypeID != f.taskType.ID {
		t.Errorf("target type = %v", req.RecordTypeID)
	}
	if req.ParentRecordID == nil || *req.ParentRecordID != f.rec.ID || req.ParentLinkTypeKey != "spawned_task" {
		t.Errorf("parent/link = %v / %q", req.ParentRecordID, req.ParentLinkTypeKey)
	}
	want := map[string]any{"subject_ar": "Build the thing", "priority": "high", "task_budget": "Follow-up of EO-DIR-2026-001"}
	if !reflect.DeepEqual(req.Values, want) {
		t.Errorf("values = %v, want %v", req.Values, want)
	}
}

func TestSpawnRecordKeepsTheNativeTypeOfAWholeValuePlaceholder(t *testing.T) {
	f := eoAxNewFixture(t)
	f.exec.types.(*eoAxTypes).fields = append(f.exec.types.(*eoAxTypes).fields, models.EOFieldDef{RecordTypeID: f.taskType.ID, Key: "due_date", Kind: "date"}, models.EOFieldDef{RecordTypeID: f.taskType.ID, Key: "owner", Kind: "user"})
	due := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	f.rec.DueDate = &due
	a := eoAxSpawn(1, map[string]any{"due_date": "{{due_date}}", "owner": "{{assigned_user_id}}", "task_budget": 42})
	if err := f.exec.ExecuteActions(context.Background(), f.rec, eoAxTransition(a), f.actor); err != nil {
		t.Fatal(err)
	}
	if len(f.svc.created) != 1 {
		t.Fatalf("not spawned; history=%v", f.repo.history)
	}
	v := f.svc.created[0].Values
	if v["due_date"] != "2026-12-01T00:00:00Z" || v["owner"] != f.assignee.ID.String() || v["task_budget"] != float64(42) {
		t.Errorf("values = %#v", v)
	}
}

func TestSpawnRecordKeepsNumbersAndSkipsUnsetSources(t *testing.T) {
	f := eoAxNewFixture(t)
	f.rec.Values = datatypes.JSON(`{"qty":5,"flag":true}`)
	ty := f.exec.types.(*eoAxTypes)
	ty.fields = append(ty.fields,
		models.EOFieldDef{RecordTypeID: f.taskType.ID, Key: "qty", Kind: "number"},
		models.EOFieldDef{RecordTypeID: f.taskType.ID, Key: "flag", Kind: "bool"},
		models.EOFieldDef{RecordTypeID: f.taskType.ID, Key: "never_set", Kind: "text"})
	a := eoAxSpawn(1, map[string]any{"qty": "{{qty}}", "flag": "{{flag}}", "never_set": "{{closed_at}}"})
	if err := f.exec.ExecuteActions(context.Background(), f.rec, eoAxTransition(a), f.actor); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"qty": float64(5), "flag": true}
	if len(f.svc.created) != 1 || !reflect.DeepEqual(f.svc.created[0].Values, want) {
		t.Fatalf("created = %+v, want values %v (numbers stay numbers; an unset source writes nothing)", f.svc.created, want)
	}
}

func TestSpawnRecordIsIdempotentWithinOneTransition(t *testing.T) {
	f := eoAxNewFixture(t)
	tr := eoAxTransition(eoAxSpawn(1, map[string]any{"subject_ar": "{{subject_ar}}"}))
	for i := 0; i < 3; i++ {
		if err := f.exec.ExecuteActions(context.Background(), f.rec, tr, f.actor); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.svc.created) != 1 {
		t.Fatalf("re-running the transition created %d children, want 1", len(f.svc.created))
	}
}

func TestSpawnRecordIdempotencyIsPerParentAndPerTargetType(t *testing.T) {
	f := eoAxNewFixture(t)
	// A child of the same type under ANOTHER parent, and a child of ANOTHER type
	// under this one, must not suppress the spawn.
	other := uuid.New()
	f.repo.children = []models.EORecord{
		{ID: uuid.New(), RecordTypeID: f.taskType.ID, ParentRecordID: &other},
		{ID: uuid.New(), RecordTypeID: uuid.New(), ParentRecordID: &f.rec.ID},
	}
	if err := f.exec.ExecuteActions(context.Background(), f.rec, eoAxTransition(eoAxSpawn(1, map[string]any{"subject_ar": "x"})), f.actor); err != nil {
		t.Fatal(err)
	}
	if len(f.svc.created) != 1 {
		t.Fatalf("created %d, want 1", len(f.svc.created))
	}
}

func TestSpawnRecordMisconfiguredMapSurfaces(t *testing.T) {
	cases := []struct {
		name    string
		cfg     map[string]any
		want    string
		createE error
	}{
		{"undefined target key", map[string]any{"target_type": "task", "link_type": "l", "field_map": map[string]any{"subject_ar": "x", "no_such_field": "y"}}, "no_such_field", nil},
		{"unknown target type", map[string]any{"target_type": "ghost", "link_type": "l", "field_map": map[string]any{}}, "ghost", nil},
		{"no link type", map[string]any{"target_type": "task", "field_map": map[string]any{}}, "link_type", nil},
		{"no target type", map[string]any{"link_type": "l", "field_map": map[string]any{}}, "target_type", nil},
		{"create rejects", map[string]any{"target_type": "task", "link_type": "l", "field_map": map[string]any{"subject_ar": "x"}}, "required thing missing", errors.New("required thing missing")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := eoAxNewFixture(t)
			f.svc.createErr = c.createE
			tr := eoAxTransition(eoAxAction("spawn_record", 1, c.cfg))
			if err := f.exec.ExecuteActions(context.Background(), f.rec, tr, f.actor); err != nil {
				t.Fatalf("must not fail the transition: %v", err)
			}
			if c.createE == nil && len(f.svc.created) != 0 {
				t.Fatalf("a misconfigured spawn must not reach Create")
			}
			if len(f.repo.history) != 1 {
				t.Fatalf("a failed spawn must leave a trail on the parent; history = %d", len(f.repo.history))
			}
			h := f.repo.history[0]
			if h.RecordID != f.rec.ID || h.RecordTypeID != f.rec.RecordTypeID || h.TransitionID == nil || *h.TransitionID != tr.ID ||
				!strings.Contains(h.Comment, c.want) || !strings.Contains(h.Comment, "spawn_record") {
				t.Errorf("history = %+v, want comment naming spawn_record and %q", h, c.want)
			}
		})
	}
}

// ── recipients ──────────────────────────────────────────────────────────────

func TestEORecipientResolution(t *testing.T) {
	f := eoAxNewFixture(t)
	creator := f.rec.CreatedByID
	cases := []struct {
		name   string
		tokens []string
		emails []string
		phones []string
	}{
		{"assignee", []string{"assignee"}, []string{"assignee@x.test"}, []string{"0500000001"}},
		{"creator", []string{"creator"}, []string{"creator@x.test"}, []string{"0500000002"}},
		{"owning body manager", []string{"owning_body_manager"}, []string{"manager@x.test"}, []string{"0500000003"}},
		{"user uuid", []string{"user:" + f.manager.ID.String()}, []string{"manager@x.test"}, []string{"0500000003"}},
		{"raw email", []string{"email:ext@y.test"}, []string{"ext@y.test"}, nil},
		{"unknown token", []string{"the_pope"}, nil, nil},
		{"user with bad uuid", []string{"user:not-a-uuid"}, nil, nil},
		{"user that does not exist", []string{"user:" + uuid.New().String()}, nil, nil},
		{"empty email", []string{"email:  "}, nil, nil},
		{"dedupes across tokens, case-insensitively", []string{"assignee", "email:ASSIGNEE@x.test", "assignee"}, []string{"assignee@x.test"}, []string{"0500000001"}},
		{"mixed good and bad", []string{"nope", "creator", "email:a@b.test"}, []string{"creator@x.test", "a@b.test"}, []string{"0500000002"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := f.exec.resolveRecipients(context.Background(), c.tokens, f.rec)
			if !reflect.DeepEqual(got.Emails, c.emails) || !reflect.DeepEqual(got.Phones, c.phones) {
				t.Fatalf("got emails=%v phones=%v, want emails=%v phones=%v", got.Emails, got.Phones, c.emails, c.phones)
			}
		})
	}
	_ = creator

	t.Run("record with no assignee or owning body resolves to nothing", func(t *testing.T) {
		bare := &models.EORecord{ID: uuid.New()}
		got := f.exec.resolveRecipients(context.Background(), []string{"assignee", "owning_body_manager"}, bare)
		if len(got.Emails) != 0 || len(got.Phones) != 0 {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("owning body without a manager", func(t *testing.T) {
		d := &models.Department{ID: uuid.New()}
		f.depts.byID[d.ID] = d
		rec := &models.EORecord{ID: uuid.New(), OwningBodyID: &d.ID}
		if got := f.exec.resolveRecipients(context.Background(), []string{"owning_body_manager"}, rec); len(got.Emails) != 0 {
			t.Fatalf("got %+v", got)
		}
	})
}

// The executor must be usable as task 14's post-commit dispatcher.
var _ EOTransitionActionDispatcher = EOActionExecutor(nil)
