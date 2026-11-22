package services

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
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
	base        *models.EORecord // the record as Update last returned it
}

func (s *eoAxRecSvc) Create(_ context.Context, req *models.EORecordCreateRequest, _ *models.User) (*models.EORecord, error) {
	s.created = append(s.created, req)
	if s.createErr != nil {
		return nil, s.createErr
	}
	child := models.EORecord{ID: uuid.New(), RecordTypeID: req.RecordTypeID, ParentRecordID: req.ParentRecordID}
	s.repo.children = append(s.repo.children, child)
	if req.ParentRecordID != nil {
		s.repo.links = append(s.repo.links, models.EORecordLink{FromRecordID: child.ID, ToRecordID: *req.ParentRecordID, LinkTypeKey: req.ParentLinkTypeKey})
	}
	return &child, nil
}

func (s *eoAxRecSvc) Update(_ context.Context, id uuid.UUID, req *models.EORecordUpdateRequest, actor *models.User) (*models.EORecord, error) {
	s.updated = append(s.updated, req)
	s.updatedIDs = append(s.updatedIDs, id)
	s.updateActor = append(s.updateActor, actor)
	if s.updateErr != nil {
		return nil, s.updateErr
	}
	// What the real service returns: the record with the write applied.
	out := *s.base
	vals := eoDecodeValues(out.Values)
	for k, v := range req.Values {
		if k == "priority" {
			out.Priority, _ = v.(string)
		} else {
			vals[k] = v
		}
	}
	out.Values, _ = json.Marshal(vals)
	s.base = &out
	return &out, nil
}

type eoAxRepo struct {
	repository.EORecordRepository
	children []models.EORecord
	links    []models.EORecordLink
	history  []models.EORecordHistory
}

func (r *eoAxRepo) LinksFrom(_ context.Context, id uuid.UUID) ([]models.EORecordLink, error) {
	var out []models.EORecordLink
	for _, l := range r.links {
		if l.FromRecordID == id {
			out = append(out, l)
		}
	}
	return out, nil
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
	byKey    map[string]*models.EORecordType
	fields   []models.EOFieldDef
	sourceID uuid.UUID
}

func (t *eoAxTypes) GetByKey(_ context.Context, key string) (*models.EORecordType, error) {
	return t.byKey[key], nil
}
func (t *eoAxTypes) GetByID(_ context.Context, id uuid.UUID) (*models.EORecordType, error) {
	if id == t.sourceID {
		return &models.EORecordType{ID: id, Key: "source", IsActive: true}, nil
	}
	return &models.EORecordType{ID: id}, nil
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
	svc := &eoAxRecSvc{repo: repo, base: rec}
	notifier := &eoAxNotifier{}
	users := &eoAxUsers{byID: map[uuid.UUID]*models.User{assignee.ID: assignee, creator.ID: creator, manager.ID: manager}}
	depts := &eoAxDepts{byID: map[uuid.UUID]*models.Department{dept.ID: dept}}
	types := &eoAxTypes{
		sourceID: typeID,
		byKey:    map[string]*models.EORecordType{"task": taskType},
		fields: []models.EOFieldDef{
			// the source type defines "budget" (filled on rec) and "venue" (never filled)
			{RecordTypeID: typeID, Key: "budget", Kind: "text"},
			{RecordTypeID: typeID, Key: "venue", Kind: "text"},
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
	f.exec.httpClient = srv.Client() // the guarded client would refuse the loopback test server
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

func TestExecuteActionsWebhookRefusesInternalDestinations(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer srv.Close() // 127.0.0.1

	cases := map[string]string{
		"loopback":         srv.URL,
		"localhost name":   strings.Replace(srv.URL, "127.0.0.1", "localhost", 1),
		"private 10/8":     "http://10.0.0.5:8080/hook",
		"private 192.168":  "http://192.168.1.1/hook",
		"metadata":         "http://169.254.169.254/latest/meta-data/",
		"unspecified":      "http://0.0.0.0:9/hook",
		"ipv6 loopback":    "http://[::1]:9/hook",
		"ipv6 ula private": "http://[fd00::1]:9/hook",
	}
	for name, u := range cases {
		t.Run(name, func(t *testing.T) {
			f := eoAxNewFixture(t) // default, guarded client
			a := eoAxAction("webhook", 1, map[string]any{"url": u, "body": "{}"})
			err := f.exec.executeWebhook(context.Background(), &a, f.rec, eoAxTransition(a), f.actor)
			if err == nil || !strings.Contains(err.Error(), "not allowed") {
				t.Fatalf("err = %v, want a refusal", err)
			}
			// and through the dispatcher: logged, never failing the transition
			if err := f.exec.ExecuteActions(context.Background(), f.rec, eoAxTransition(a), f.actor); err != nil {
				t.Fatalf("ExecuteActions = %v", err)
			}
		})
	}
	if hits != 0 {
		t.Fatalf("the internal server was reached %d times", hits)
	}
}

func TestEOWebhookIPAllowed(t *testing.T) {
	for ip, want := range map[string]bool{
		"8.8.8.8": true, "93.184.216.34": true, "2606:4700::1111": true,
		"127.0.0.1": false, "10.1.2.3": false, "172.16.0.1": false, "172.31.255.255": false, "192.168.0.1": false,
		"169.254.169.254": false, "224.0.0.1": false, "0.0.0.0": false, "::1": false, "fe80::1": false, "fd12::1": false,
		"::ffff:10.0.0.1": false,
		"100.64.0.1":      false, "100.127.255.254": false, "100.128.0.1": true,
		"198.18.0.1": false, "198.19.255.255": false, "198.20.0.1": true,
		"192.0.0.1": false, "255.255.255.255": false,
		"::10.0.0.1": false, "::7f00:1": false, "::808:808": true, "::a00:1": false,
		"64:ff9b::a00:1": false, "64:ff9b::808:808": false,
	} {
		if got := eoWebhookIPAllowed(net.ParseIP(ip)); got != want {
			t.Errorf("%s allowed = %v, want %v", ip, got, want)
		}
	}
}

func TestExecuteActionsWebhookDoesNotFollowRedirects(t *testing.T) {
	var targetHits int
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { targetHits++ }))
	defer target.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redir.Close()

	f := eoAxNewFixture(t)
	// Same redirect policy as the real client, without the dial guard that
	// would otherwise refuse the loopback test servers first.
	f.exec.httpClient = &http.Client{CheckRedirect: eoWebhookNoRedirect}
	a := eoAxAction("webhook", 1, map[string]any{"url": redir.URL, "body": "{}"})
	err := f.exec.executeWebhook(context.Background(), &a, f.rec, eoAxTransition(a), f.actor)
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("err = %v, want a redirect refusal", err)
	}
	if targetHits != 0 {
		t.Fatalf("redirect was followed")
	}
	req, _ := http.NewRequest("GET", target.URL, nil)
	if got := eoNewWebhookClient().CheckRedirect(req, []*http.Request{req}); got != http.ErrUseLastResponse {
		t.Fatalf("the real client's redirect policy returned %v, want http.ErrUseLastResponse", got)
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

func TestFieldUpdateIsVisibleToLaterActionsInTheSameDispatch(t *testing.T) {
	f := eoAxNewFixture(t)
	fu := eoAxAction("field_update", 1, map[string]any{"field": "priority", "value": "critical"})
	sp := eoAxSpawn(2, map[string]any{"priority": "{{priority}}", "task_budget": "was {{priority}}"})
	tr := eoAxTransition(fu, sp)
	if err := f.exec.ExecuteActions(context.Background(), f.rec, tr, f.actor); err != nil {
		t.Fatal(err)
	}
	if len(f.svc.created) != 1 {
		t.Fatalf("created %d", len(f.svc.created))
	}
	v := f.svc.created[0].Values
	if v["priority"] != "critical" || v["task_budget"] != "was critical" {
		t.Fatalf("the spawn rendered from a stale record: %v", v)
	}
	if f.rec.Priority != "high" {
		t.Fatalf("the caller's record must not be mutated, priority = %q", f.rec.Priority)
	}
}

func TestSpawnRecordUnfilledBagFieldOfTheSourceTypeIsNotAnError(t *testing.T) {
	f := eoAxNewFixture(t)
	// "venue" is a custom field of the source type, absent from this record's
	// values bag. The same map must spawn whether or not it is filled.
	a := eoAxSpawn(1, map[string]any{"subject_ar": "{{reference}}", "priority": "{{venue}}", "task_budget": "at [{{venue}}] for {{budget}}"})
	if err := f.exec.ExecuteActions(context.Background(), f.rec, eoAxTransition(a), f.actor); err != nil {
		t.Fatal(err)
	}
	if len(f.svc.created) != 1 {
		t.Fatalf("an unfilled optional field must not stop the spawn; created %d, history %+v", len(f.svc.created), f.repo.history)
	}
	v := f.svc.created[0].Values
	if _, set := v["priority"]; set {
		t.Errorf("whole-value placeholder of an unfilled field must leave the target unset, got %v", v["priority"])
	}
	if v["task_budget"] != "at [] for 1200" {
		t.Errorf("mixed render = %q", v["task_budget"])
	}
	if len(f.repo.history) != 0 {
		t.Errorf("no failure should be recorded: %+v", f.repo.history)
	}
}

func TestEOActionVarsAlwaysCarryTheRelationKeys(t *testing.T) {
	vars := eoActionVars(&models.EORecord{ID: uuid.New()}, &models.WorkflowTransition{Code: "c"}, nil)
	for _, k := range []string{"transition_code", "transition_name", "from_state", "to_state", "performed_by_name", "performed_by_email"} {
		if _, ok := vars[k]; !ok {
			t.Errorf("vars lacks %q when the relation is not loaded", k)
		}
	}
	vars = eoActionVars(&models.EORecord{ID: uuid.New()}, nil, nil)
	if _, ok := vars["from_state"]; !ok {
		t.Errorf("vars lacks from_state with no transition")
	}
}

func TestSpawnRecordWholeValuePlaceholderResolvesVariablesToo(t *testing.T) {
	f := eoAxNewFixture(t)
	a := eoAxSpawn(1, map[string]any{"subject_ar": "{{transition_code}}", "task_budget": "{{performed_by_name}}"})
	if err := f.exec.ExecuteActions(context.Background(), f.rec, eoAxTransition(a), f.actor); err != nil {
		t.Fatal(err)
	}
	if len(f.svc.created) != 1 || f.svc.created[0].Values["subject_ar"] != "approve" || f.svc.created[0].Values["task_budget"] != "Ann Actor" {
		t.Fatalf("created = %+v", f.svc.created)
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
	c1 := models.EORecord{ID: uuid.New(), RecordTypeID: f.taskType.ID, ParentRecordID: &other}
	c2 := models.EORecord{ID: uuid.New(), RecordTypeID: uuid.New(), ParentRecordID: &f.rec.ID}
	f.repo.children = []models.EORecord{c1, c2}
	f.repo.links = []models.EORecordLink{
		{FromRecordID: c1.ID, ToRecordID: other, LinkTypeKey: "spawned_task"},
		{FromRecordID: c2.ID, ToRecordID: f.rec.ID, LinkTypeKey: "spawned_task"},
	}
	if err := f.exec.ExecuteActions(context.Background(), f.rec, eoAxTransition(eoAxSpawn(1, map[string]any{"subject_ar": "x"})), f.actor); err != nil {
		t.Fatal(err)
	}
	if len(f.svc.created) != 1 {
		t.Fatalf("created %d, want 1", len(f.svc.created))
	}
}

func TestSpawnRecordSkipKeyIncludesTheLinkType(t *testing.T) {
	f := eoAxNewFixture(t)
	a := eoAxAction("spawn_record", 1, map[string]any{"target_type": "task", "link_type": "link_a", "field_map": map[string]any{"subject_ar": "x"}})
	b := eoAxAction("spawn_record", 2, map[string]any{"target_type": "task", "link_type": "link_b", "field_map": map[string]any{"subject_ar": "x"}})
	c := eoAxAction("spawn_record", 3, map[string]any{"target_type": "task", "link_type": "link_b", "field_map": map[string]any{"subject_ar": "x"}})
	tr := eoAxTransition(a, b, c)
	for i := 0; i < 2; i++ {
		if err := f.exec.ExecuteActions(context.Background(), f.rec, tr, f.actor); err != nil {
			t.Fatal(err)
		}
	}
	var keys []string
	for _, r := range f.svc.created {
		keys = append(keys, r.ParentLinkTypeKey)
	}
	if !reflect.DeepEqual(keys, []string{"link_a", "link_b"}) {
		t.Fatalf("created via %v, want one child per link type, and no repeats on re-run", keys)
	}
}

func TestSpawnRecordLogsEverySkip(t *testing.T) {
	f := eoAxNewFixture(t)
	tr := eoAxTransition(eoAxSpawn(1, map[string]any{"subject_ar": "x"}))
	if err := f.exec.ExecuteActions(context.Background(), f.rec, tr, f.actor); err != nil {
		t.Fatal(err)
	}
	var buf strings.Builder
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	if err := f.exec.ExecuteActions(context.Background(), f.rec, tr, f.actor); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"SKIPPED", f.rec.Reference, `"task"`, `"spawned_task"`} {
		if !strings.Contains(out, want) {
			t.Errorf("skip log %q lacks %s", out, want)
		}
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
		{"typo in a whole-value placeholder", map[string]any{"target_type": "task", "link_type": "l", "field_map": map[string]any{"subject_ar": "{{refrence}}"}}, "refrence", nil},
		{"typo inside a mixed placeholder", map[string]any{"target_type": "task", "link_type": "l", "field_map": map[string]any{"subject_ar": "see {{refrence}}"}}, "refrence", nil},
		{"create rejects", map[string]any{"target_type": "task", "link_type": "l", "field_map": map[string]any{"subject_ar": "x"}}, "required thing missing", errors.New("required thing missing")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := eoAxNewFixture(t)
			f.svc.createErr = c.createE
			tr := eoAxTransition(eoAxAction("spawn_record", 1, c.cfg))
			var buf strings.Builder
			log.SetOutput(&buf)
			defer log.SetOutput(os.Stderr)
			if err := f.exec.ExecuteActions(context.Background(), f.rec, tr, f.actor); err != nil {
				t.Fatalf("must not fail the transition: %v", err)
			}
			link, _ := c.cfg["link_type"].(string)
			target, _ := c.cfg["target_type"].(string)
			for _, want := range []string{"SKIPPED", f.rec.Reference, `target_type="` + target + `"`, `link_type="` + link + `"`, c.want} {
				if !strings.Contains(buf.String(), want) {
					t.Errorf("misconfiguration log %q lacks %s", buf.String(), want)
				}
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
