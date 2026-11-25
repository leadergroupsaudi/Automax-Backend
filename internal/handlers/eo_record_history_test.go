package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/automax/backend/internal/middleware"
	"github.com/automax/backend/internal/models"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// ── fake ────────────────────────────────────────────────────────────────────

// eoHistoryRepo is eoFakeRecordRepo with the audit-trail read added. It is a
// SEPARATE type rather than three more fields on the shared fake so that a
// test which does not opt in keeps the embedded interface's nil method — a
// handler that reached History from a route with no business reading it would
// panic rather than quietly succeed.
//
// It pages its own backing slice, so a handler that computed the offset itself
// (or ignored page entirely) shows up in the BODY, not only in the arguments
// it passed down.
type eoHistoryRepo struct {
	eoFakeRecordRepo
	rows []models.EORecordHistoryEntry
	err  error

	calls     int
	gotRecord uuid.UUID
	gotType   uuid.UUID
	gotPage   int
	gotLimit  int
}

func (f *eoHistoryRepo) History(_ context.Context, recordID, recordTypeID uuid.UUID, page, limit int) ([]models.EORecordHistoryEntry, int64, error) {
	f.calls++
	f.gotRecord, f.gotType, f.gotPage, f.gotLimit = recordID, recordTypeID, page, limit
	if f.err != nil {
		return nil, 0, f.err
	}
	total := int64(len(f.rows))
	if page < 1 || limit < 1 {
		// The fake refuses to invent a clamp of its own: a handler that let an
		// unclamped page through must fail here rather than be rescued.
		return nil, total, nil
	}
	start := (page - 1) * limit
	if start >= len(f.rows) {
		return nil, total, nil
	}
	end := start + limit
	if end > len(f.rows) {
		end = len(f.rows)
	}
	return f.rows[start:end], total, nil
}

// ── helpers ─────────────────────────────────────────────────────────────────

const eoHistoryTypeKey = "eo_incoming_correspondence"

// eoHistoryRecord is the record every test here reads the history of.
func eoHistoryRecord() *models.EORecord {
	return &models.EORecord{ID: uuid.New(), RecordTypeID: uuid.New(), Reference: "EO-INC-2026-001"}
}

// eoHistoryRows are n entries NEWEST FIRST, the order the route must preserve.
func eoHistoryRows(rec *models.EORecord, n int) []models.EORecordHistoryEntry {
	out := make([]models.EORecordHistoryEntry, 0, n)
	base := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		out = append(out, models.EORecordHistoryEntry{
			EORecordHistory: models.EORecordHistory{
				ID:           uuid.New(),
				RecordID:     rec.ID,
				RecordTypeID: rec.RecordTypeID,
				Comment:      "entry",
				CreatedAt:    base.Add(-time.Duration(i) * time.Minute),
			},
			PerformedByName: "Officer " + strconv.Itoa(i),
			FromStateName:   "Under Review",
			FromStateNameAr: "قيد المراجعة",
			ToStateName:     "Approved",
			ToStateNameAr:   "معتمد",
		})
	}
	return out
}

// eoHistoryUser builds a caller holding exactly the codes given, and nothing
// else. It is deliberately NOT a super admin: both gates short-circuit for
// one, which would make every assertion below vacuous.
func eoHistoryUser(codes ...string) *models.User {
	perms := make([]models.Permission, 0, len(codes))
	for _, code := range codes {
		perms = append(perms, models.Permission{Code: code, IsActive: true})
	}
	return &models.User{ID: uuid.New(), Email: "officer@eo.gov", Roles: []models.Role{{Permissions: perms}}}
}

// eoHistoryEnvelope is the paginated envelope, with the raw body beside it so
// a failure prints what the route actually said.
type eoHistoryEnvelope struct {
	Success    bool             `json:"success"`
	Data       []map[string]any `json:"data"`
	Page       int              `json:"page"`
	Limit      int              `json:"limit"`
	TotalItems int64            `json:"total_items"`
	TotalPages int              `json:"total_pages"`

	raw     string
	decoded map[string]json.RawMessage
}

func eoHistoryRead(t *testing.T, resp *http.Response) (string, int) {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(raw), resp.StatusCode
}

func eoHistoryBody(t *testing.T, raw string) eoHistoryEnvelope {
	t.Helper()
	var out eoHistoryEnvelope
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	out.raw = raw
	if err := json.Unmarshal([]byte(raw), &out.decoded); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return out
}

// eoHistoryApp registers the REAL route set with the REAL per-type gate and a
// module-level guard that grants exactly `granted`.
func eoHistoryApp(repo *eoHistoryRepo, types *eoFakeTypeRepo, user *models.User, granted ...string) *fiber.App {
	gate := middleware.NewEOTypePermission(repo, types)
	app := fiber.New()
	app.Use(eoTestActor(user))
	NewEORecordHandler(&eoFakeRecordService{}, nil, repo, types).
		RegisterRoutes(app.Group("/eo-records"), eoPermFor(granted...), gate.RequireEOTypePermission)
	return app
}

// ── tests ───────────────────────────────────────────────────────────────────

// TestGetEORecordHistoryRequiresViewCodes drives the REAL guards on the REAL
// route. The audit trail is the record read in full — every state change,
// every actor, every timestamp — so it asks for exactly what
// GET /eo-records/:id asks for: the module code eo:view AND the type's own
// eo:<type_key>:view.
//
// 403 and never 401: the frontend's axios interceptor logs the user out on a
// 401, so a permission refusal answering 401 would end the session.
func TestGetEORecordHistoryRequiresViewCodes(t *testing.T) {
	eoInitValidator(t)
	rec := eoHistoryRecord()
	typeView := models.EOTypePermissionCode(eoHistoryTypeKey, models.EOTypePermissionView)

	newApp := func(user *models.User, granted ...string) (*fiber.App, *eoHistoryRepo) {
		repo := &eoHistoryRepo{
			eoFakeRecordRepo: eoFakeRecordRepo{
				byID:       map[uuid.UUID]*models.EORecord{rec.ID: rec},
				visibleIDs: map[uuid.UUID]bool{rec.ID: true},
			},
			rows: eoHistoryRows(rec, 3),
		}
		types := &eoFakeTypeRepo{byID: &models.EORecordType{ID: rec.RecordTypeID, Key: eoHistoryTypeKey}}
		return eoHistoryApp(repo, types, user, granted...), repo
	}

	get := func(app *fiber.App) (string, int) {
		t.Helper()
		resp, err := app.Test(httptest.NewRequest("GET", "/eo-records/"+rec.ID.String()+"/history", nil))
		if err != nil {
			t.Fatalf("app.Test: %v", err)
		}
		return eoHistoryRead(t, resp)
	}

	t.Run("refused without the module code eo:view", func(t *testing.T) {
		// The near miss: the caller holds the TYPE's view code, so a route
		// leaning on the per-type gate alone would wave this through.
		user := eoHistoryUser(typeView)
		app, repo := newApp(user, "eo:update", "eo:create")
		body, status := get(app)
		if status != fiber.StatusForbidden {
			t.Fatalf("status = %d, want 403: %s", status, body)
		}
		if repo.calls != 0 {
			t.Errorf("the audit trail was read %d times for a refused caller", repo.calls)
		}
	})

	t.Run("refused without the type's view code", func(t *testing.T) {
		// The other near miss: eo:view plus VIEW on a DIFFERENT register.
		user := eoHistoryUser("eo:view", models.EOTypePermissionCode("eo_directive", models.EOTypePermissionView))
		app, repo := newApp(user, "eo:view")
		body, status := get(app)
		if status != fiber.StatusForbidden {
			t.Fatalf("status = %d, want 403: %s", status, body)
		}
		if repo.calls != 0 {
			t.Errorf("the audit trail was read %d times for a refused caller", repo.calls)
		}
	})

	t.Run("a refusal is never 401", func(t *testing.T) {
		for _, granted := range [][]string{{}, {"eo:view"}} {
			user := eoHistoryUser(typeView)
			app, _ := newApp(user, granted...)
			if _, status := get(app); status == fiber.StatusUnauthorized {
				t.Errorf("status = 401 for granted=%v: the client's axios interceptor logs the user out on a 401", granted)
			}
		}
	})

	t.Run("allowed with both", func(t *testing.T) {
		user := eoHistoryUser("eo:view", typeView)
		app, repo := newApp(user, "eo:view")
		body, status := get(app)
		if status != fiber.StatusOK {
			t.Fatalf("status = %d, want 200 for a caller holding both codes: %s", status, body)
		}
		if repo.calls != 1 {
			t.Errorf("the audit trail was read %d times, want 1", repo.calls)
		}
		if repo.gotRecord != rec.ID || repo.gotType != rec.RecordTypeID {
			t.Errorf("history read for (%s, %s), want (%s, %s)", repo.gotRecord, repo.gotType, rec.ID, rec.RecordTypeID)
		}
	})
}

// TestGetEORecordHistoryIsPagedNewestFirst pins the route's paging to the
// REGISTER'S OWN shape: the parameters are page and limit, the default page is
// eoDefaultPageLimit and the maximum eoMaxPageLimit, the answer is
// utils.PaginatedSuccessResponse's envelope (page, limit, total_items,
// total_pages), and a limit above the maximum is the caller's mistake rather
// than something clamped silently.
//
// A client that can page GET /eo-records must be able to page this without
// learning a second convention.
func TestGetEORecordHistoryIsPagedNewestFirst(t *testing.T) {
	eoInitValidator(t)
	rec := eoHistoryRecord()
	rows := eoHistoryRows(rec, 5)
	typeView := models.EOTypePermissionCode(eoHistoryTypeKey, models.EOTypePermissionView)

	newApp := func() (*fiber.App, *eoHistoryRepo) {
		repo := &eoHistoryRepo{
			eoFakeRecordRepo: eoFakeRecordRepo{
				byID:       map[uuid.UUID]*models.EORecord{rec.ID: rec},
				visibleIDs: map[uuid.UUID]bool{rec.ID: true},
			},
			rows: rows,
		}
		types := &eoFakeTypeRepo{byID: &models.EORecordType{ID: rec.RecordTypeID, Key: eoHistoryTypeKey}}
		return eoHistoryApp(repo, types, eoHistoryUser("eo:view", typeView), "eo:view"), repo
	}

	get := func(app *fiber.App, query string) (string, int) {
		t.Helper()
		resp, err := app.Test(httptest.NewRequest("GET", "/eo-records/"+rec.ID.String()+"/history"+query, nil))
		if err != nil {
			t.Fatalf("app.Test: %v", err)
		}
		return eoHistoryRead(t, resp)
	}

	t.Run("the default page is the register's", func(t *testing.T) {
		app, repo := newApp()
		body, status := get(app, "")
		if status != fiber.StatusOK {
			t.Fatalf("status = %d, want 200: %s", status, body)
		}
		out := eoHistoryBody(t, body)
		if out.Page != 1 || out.Limit != eoDefaultPageLimit {
			t.Errorf("page/limit = %d/%d, want 1/%d", out.Page, out.Limit, eoDefaultPageLimit)
		}
		if out.TotalItems != 5 || out.TotalPages != 1 {
			t.Errorf("total_items/total_pages = %d/%d, want 5/1", out.TotalItems, out.TotalPages)
		}
		if repo.gotPage != 1 || repo.gotLimit != eoDefaultPageLimit {
			t.Errorf("the repository was asked for page %d limit %d, want 1/%d", repo.gotPage, repo.gotLimit, eoDefaultPageLimit)
		}
		// NEWEST FIRST, in the order the repository handed them over.
		if len(out.Data) != 5 {
			t.Fatalf("data has %d entries, want 5: %s", len(out.Data), out.raw)
		}
		for i := range rows {
			if got, _ := out.Data[i]["id"].(string); got != rows[i].ID.String() {
				t.Fatalf("data[%d].id = %s, want %s — the route must not re-order the trail: %s", i, got, rows[i].ID, out.raw)
			}
		}
	})

	t.Run("page and limit name the same things the register's do", func(t *testing.T) {
		app, repo := newApp()
		body, status := get(app, "?page=2&limit=2")
		if status != fiber.StatusOK {
			t.Fatalf("status = %d, want 200: %s", status, body)
		}
		out := eoHistoryBody(t, body)
		if out.Page != 2 || out.Limit != 2 {
			t.Errorf("page/limit = %d/%d, want 2/2", out.Page, out.Limit)
		}
		if out.TotalItems != 5 || out.TotalPages != 3 {
			t.Errorf("total_items/total_pages = %d/%d, want 5/3", out.TotalItems, out.TotalPages)
		}
		if repo.gotPage != 2 || repo.gotLimit != 2 {
			t.Errorf("the repository was asked for page %d limit %d, want 2/2", repo.gotPage, repo.gotLimit)
		}
		if len(out.Data) != 2 {
			t.Fatalf("data has %d entries, want 2: %s", len(out.Data), out.raw)
		}
		for i, want := range rows[2:4] {
			if got, _ := out.Data[i]["id"].(string); got != want.ID.String() {
				t.Errorf("page 2 data[%d].id = %s, want %s: %s", i, got, want.ID, out.raw)
			}
		}
	})

	t.Run("page=0 and limit=0 are clamped, as the register clamps them", func(t *testing.T) {
		app, repo := newApp()
		body, status := get(app, "?page=0&limit=0")
		if status != fiber.StatusOK {
			t.Fatalf("status = %d, want 200: %s", status, body)
		}
		out := eoHistoryBody(t, body)
		if out.Page != 1 || out.Limit != eoDefaultPageLimit {
			t.Errorf("page/limit = %d/%d, want 1/%d", out.Page, out.Limit, eoDefaultPageLimit)
		}
		if repo.gotLimit < 1 {
			t.Errorf("the repository was asked for limit %d: a zero limit is a division by zero in the envelope", repo.gotLimit)
		}
	})

	// The BOUNDARY, not a number far above it: a declared maximum that drifts
	// from eoMaxPageLimit would still refuse ?limit=5000 while quietly
	// accepting everything between the two, which is the whole of the drift.
	t.Run("a limit above the maximum is refused, not clamped", func(t *testing.T) {
		for _, limit := range []string{strconv.Itoa(eoMaxPageLimit + 1), "5000"} {
			app, repo := newApp()
			body, status := get(app, "?limit="+limit)
			if status != fiber.StatusBadRequest {
				t.Errorf("limit=%s status = %d, want 400 — the declared maximum must be eoMaxPageLimit (%d), the register's own: %s",
					limit, status, eoMaxPageLimit, body)
			}
			if repo.calls != 0 {
				t.Errorf("limit=%s: the audit trail was read %d times for a rejected limit", limit, repo.calls)
			}
		}
	})

	t.Run("the maximum itself is accepted", func(t *testing.T) {
		app, repo := newApp()
		body, status := get(app, "?limit="+strconv.Itoa(eoMaxPageLimit))
		if status != fiber.StatusOK {
			t.Fatalf("limit=%d status = %d, want 200: the route's declared maximum must match the register's: %s",
				eoMaxPageLimit, status, body)
		}
		if repo.gotLimit != eoMaxPageLimit {
			t.Errorf("the repository was asked for limit %d, want %d", repo.gotLimit, eoMaxPageLimit)
		}
	})
}

// TestGetEORecordHistoryServesDisplayNamesBesideTheIDs.
//
// The consumer cannot resolve `performed_by_id`, `from_state_id` or
// `to_state_id` itself: no route turns a user id into a name, so a history tab
// reading this payload alone would render "user 3f2a… moved 9b1c… → 7d4e…".
// The names therefore travel with the rows — and BESIDE the ids, never in
// place of them, because a consumer may legitimately key on an id and an audit
// payload must not lose one.
//
// The state names are bilingual, the convention WorkflowState and EORecordType
// already use (`name` + `name_ar`). The actor's is not: models.User has no
// Arabic name column.
func TestGetEORecordHistoryServesDisplayNamesBesideTheIDs(t *testing.T) {
	eoInitValidator(t)
	rec := eoHistoryRecord()
	rows := eoHistoryRows(rec, 2)
	actor, fromState, toState := uuid.New(), uuid.New(), uuid.New()
	for i := range rows {
		rows[i].PerformedByID = &actor
		rows[i].FromStateID = &fromState
		rows[i].ToStateID = &toState
	}
	repo := &eoHistoryRepo{
		eoFakeRecordRepo: eoFakeRecordRepo{
			byID:       map[uuid.UUID]*models.EORecord{rec.ID: rec},
			visibleIDs: map[uuid.UUID]bool{rec.ID: true},
		},
		rows: rows,
	}
	types := &eoFakeTypeRepo{byID: &models.EORecordType{ID: rec.RecordTypeID, Key: eoHistoryTypeKey}}
	app := eoHistoryApp(repo, types,
		eoHistoryUser("eo:view", models.EOTypePermissionCode(eoHistoryTypeKey, models.EOTypePermissionView)), "eo:view")

	resp, err := app.Test(httptest.NewRequest("GET", "/eo-records/"+rec.ID.String()+"/history", nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	body, status := eoHistoryRead(t, resp)
	if status != fiber.StatusOK {
		t.Fatalf("status = %d, want 200: %s", status, body)
	}
	out := eoHistoryBody(t, body)
	if len(out.Data) != 2 {
		t.Fatalf("data has %d entries, want 2: %s", len(out.Data), out.raw)
	}
	first := out.Data[0]

	for _, tc := range []struct{ key, want string }{
		{"performed_by_name", rows[0].PerformedByName},
		{"from_state_name", "Under Review"},
		{"from_state_name_ar", "قيد المراجعة"},
		{"to_state_name", "Approved"},
		{"to_state_name_ar", "معتمد"},
	} {
		got, _ := first[tc.key].(string)
		if got != tc.want {
			t.Errorf("data[0].%s = %q, want %q — a history tab has no other way to resolve it: %s",
				tc.key, got, tc.want, out.raw)
		}
	}

	// THE IDS SURVIVE. Replacing them with names would be a lossy change to an
	// audit payload.
	for _, tc := range []struct {
		key  string
		want uuid.UUID
	}{
		{"performed_by_id", actor},
		{"from_state_id", fromState},
		{"to_state_id", toState},
		{"record_id", rec.ID},
	} {
		got, _ := first[tc.key].(string)
		if got != tc.want.String() {
			t.Errorf("data[0].%s = %q, want %q — the names are added beside the ids, not instead of them",
				tc.key, got, tc.want)
		}
	}

	// No Arabic actor name is invented: models.User has no column for one, and
	// a key standing for data that does not exist is a third convention.
	if _, ok := first["performed_by_name_ar"]; ok {
		t.Errorf("performed_by_name_ar is present: models.User has no Arabic name column to fill it from: %s", out.raw)
	}
}

// TestGetEORecordHistoryOnARecordWithNone — Review Focus 3. A record whose
// trail is empty is a record, not a missing one: an empty LIST and a zero
// total, never a 404. The frontend's history tab renders "nothing yet"; a 404
// would render as an error, and would also contradict the 404 this route gives
// for a record the caller may not see.
func TestGetEORecordHistoryOnARecordWithNone(t *testing.T) {
	eoInitValidator(t)
	rec := eoHistoryRecord()
	repo := &eoHistoryRepo{
		eoFakeRecordRepo: eoFakeRecordRepo{
			byID:       map[uuid.UUID]*models.EORecord{rec.ID: rec},
			visibleIDs: map[uuid.UUID]bool{rec.ID: true},
		},
		rows: nil, // the trail is empty
	}
	types := &eoFakeTypeRepo{byID: &models.EORecordType{ID: rec.RecordTypeID, Key: eoHistoryTypeKey}}
	app := eoHistoryApp(repo, types,
		eoHistoryUser("eo:view", models.EOTypePermissionCode(eoHistoryTypeKey, models.EOTypePermissionView)), "eo:view")

	resp, err := app.Test(httptest.NewRequest("GET", "/eo-records/"+rec.ID.String()+"/history", nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	body, status := eoHistoryRead(t, resp)
	if status != fiber.StatusOK {
		t.Fatalf("status = %d, want 200 — an empty trail is not a missing record: %s", status, body)
	}
	out := eoHistoryBody(t, body)
	if !out.Success {
		t.Errorf("success = false, want true: %s", out.raw)
	}
	if out.TotalItems != 0 || out.TotalPages != 0 {
		t.Errorf("total_items/total_pages = %d/%d, want 0/0", out.TotalItems, out.TotalPages)
	}
	if len(out.Data) != 0 {
		t.Errorf("data = %v, want empty", out.Data)
	}
	// An EMPTY ARRAY, not null: the tab maps over it.
	if raw, ok := out.decoded["data"]; !ok || string(raw) != "[]" {
		t.Errorf("data encoded as %s, want []: a null is a crash in a client that maps over it", raw)
	}
}

// TestGetEORecordHistoryHonoursRowLevelVisibility is the disclosure this route
// would otherwise open.
//
// eo:view and the type's view code are TYPE-scoped: they say the caller may
// see records of this register, not that it may see THIS record. §7.11 narrows
// further — a record restricted with visible_to is shown only to an admin, its
// creator or a member of a matching body. A caller holding both codes but
// belonging to another body is refused the record itself and cannot find it in
// the register, so serving it that record's entire audit trail would disclose
// exactly what §7.11 conceals: every state change, every actor, every
// timestamp, and the fact that the record exists at all.
//
// The gate is the register's own rule, asked through eoLoadRecord — not a
// second copy of it.
func TestGetEORecordHistoryHonoursRowLevelVisibility(t *testing.T) {
	eoInitValidator(t)
	concealed := eoHistoryRecord()
	missing := uuid.New()
	typeView := models.EOTypePermissionCode(eoHistoryTypeKey, models.EOTypePermissionView)

	newApp := func(visible bool) (*fiber.App, *eoHistoryRepo) {
		seen := map[uuid.UUID]bool{}
		if visible {
			seen[concealed.ID] = true
		}
		repo := &eoHistoryRepo{
			eoFakeRecordRepo: eoFakeRecordRepo{
				byID:       map[uuid.UUID]*models.EORecord{concealed.ID: concealed},
				visibleIDs: seen,
			},
			rows: eoHistoryRows(concealed, 3),
		}
		types := &eoFakeTypeRepo{byID: &models.EORecordType{ID: concealed.RecordTypeID, Key: eoHistoryTypeKey}}
		return eoHistoryApp(repo, types, eoHistoryUser("eo:view", typeView), "eo:view"), repo
	}

	get := func(app *fiber.App, id string) (string, int) {
		t.Helper()
		resp, err := app.Test(httptest.NewRequest("GET", "/eo-records/"+id+"/history", nil))
		if err != nil {
			t.Fatalf("app.Test: %v", err)
		}
		return eoHistoryRead(t, resp)
	}

	t.Run("a record the caller may not see yields no history", func(t *testing.T) {
		app, repo := newApp(false)
		body, status := get(app, concealed.ID.String())
		if status == fiber.StatusOK {
			t.Fatalf("status = 200 for a record §7.11 conceals: the audit trail leaked: %s", body)
		}
		if repo.calls != 0 {
			t.Errorf("the audit trail was read %d times for a record the caller may not see", repo.calls)
		}
		for _, row := range repo.rows {
			if strings.Contains(body, row.ID.String()) {
				t.Errorf("a history row id reached a caller who may not see the record: %s", body)
			}
		}
	})

	// THE STATUS MUST NOT ITSELF DISCLOSE. A concealed record and one that was
	// never issued answer identically — status AND body — or the route becomes
	// an oracle for which ids exist. It is 404 for both, which is what
	// eoLoadRecord already answers for GET /eo-records/:id and what the file
	// routes answer: a 403 here would differ from the record's own read and
	// confirm the id to anyone who compared the two.
	t.Run("a concealed record is indistinguishable from one that does not exist", func(t *testing.T) {
		app, _ := newApp(false)
		concealedBody, concealedStatus := get(app, concealed.ID.String())
		missingBody, missingStatus := get(app, missing.String())
		if concealedStatus != missingStatus {
			t.Fatalf("concealed = %d, non-existent = %d: the status tells an attacker which ids exist",
				concealedStatus, missingStatus)
		}
		if concealedStatus != fiber.StatusNotFound {
			t.Errorf("status = %d, want 404 — the answer GET /eo-records/:id already gives for both", concealedStatus)
		}
		if concealedBody != missingBody {
			t.Errorf("concealed body %s differs from non-existent body %s: the body tells an attacker which ids exist",
				concealedBody, missingBody)
		}
	})

	// The other half, and a real failure if it breaks: a gate written so tight
	// that an officer cannot read the history of a record they CAN open.
	t.Run("a record the caller may see yields its history", func(t *testing.T) {
		app, repo := newApp(true)
		body, status := get(app, concealed.ID.String())
		if status != fiber.StatusOK {
			t.Fatalf("status = %d, want 200 for a record the caller may open: %s", status, body)
		}
		out := eoHistoryBody(t, body)
		if out.TotalItems != 3 || len(out.Data) != 3 {
			t.Errorf("total_items = %d, data = %d entries, want 3 and 3: %s", out.TotalItems, len(out.Data), out.raw)
		}
		if repo.calls != 1 {
			t.Errorf("the audit trail was read %d times, want 1", repo.calls)
		}
	})
}
