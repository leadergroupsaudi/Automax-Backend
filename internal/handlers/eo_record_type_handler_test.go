package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/automax/backend/internal/middleware"
	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/pkg/utils"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// eoPermFor stands in for authMiddleware.RequirePermission for a caller who
// holds exactly the codes in granted. It mirrors the real middleware
// (internal/middleware/auth.go): ANY of the required codes is enough, and a
// caller holding none is refused with 403 — never 401, which the frontend's
// axios interceptor treats as a logout.
//
// It deliberately has no super-admin short circuit: the real middleware's
// IsSuperAdmin bypass would make every one of these assertions vacuous.
func eoPermFor(granted ...string) EOPermissionMiddleware {
	held := make(map[string]bool, len(granted))
	for _, g := range granted {
		held[g] = true
	}
	return func(permissions ...string) fiber.Handler {
		return func(c *fiber.Ctx) error {
			for _, p := range permissions {
				if held[p] {
					return c.Next()
				}
			}
			return utils.ErrorResponse(c, fiber.StatusForbidden, "Insufficient permissions")
		}
	}
}

// eoTypeAuthzRepo adds the one read eoFakeTypeRepo does not override but the
// link-type mutations call before anything else. Without it those two routes
// would nil-panic on the reachable-with-manage control below, and the test
// would be measuring the fake rather than the guard.
type eoTypeAuthzRepo struct {
	eoFakeTypeRepo
	linkTypeByID *models.EOLinkType
	fieldDef     *models.EOFieldDef
}

func (f *eoTypeAuthzRepo) GetLinkTypeByID(_ context.Context, id uuid.UUID) (*models.EOLinkType, error) {
	if f.linkTypeByID != nil && f.linkTypeByID.ID == id {
		return f.linkTypeByID, nil
	}
	return nil, nil
}

// GetFieldDef and UpdateFieldDef are the reads and writes the two field
// MUTATIONS make before anything else. Without them those routes would
// nil-panic on the reachable-with-manage control below, and the test would be
// measuring the fake rather than the guard.
func (f *eoTypeAuthzRepo) GetFieldDef(_ context.Context, id uuid.UUID) (*models.EOFieldDef, error) {
	if f.fieldDef != nil && f.fieldDef.ID == id {
		return f.fieldDef, nil
	}
	return nil, nil
}

func (f *eoTypeAuthzRepo) UpdateFieldDef(_ context.Context, _ *models.EOFieldDef) error { return nil }

func (f *eoTypeAuthzRepo) CreateLinkType(_ context.Context, _ *models.EOLinkType) error { return nil }
func (f *eoTypeAuthzRepo) UpdateLinkType(_ context.Context, _ *models.EOLinkType) error { return nil }
func (f *eoTypeAuthzRepo) DeleteLinkType(_ context.Context, _ uuid.UUID) error          { return nil }
func (f *eoTypeAuthzRepo) Create(_ context.Context, _ *models.EORecordType) error       { return nil }

// eoTypeIDs are the ids the fake repository will answer for, so a request can
// name a type, a link type or a field definition that actually exists. The
// control half of the write test needs that: a 404 for an id nothing knows
// about is indistinguishable from a route nobody can reach.
type eoTypeIDs struct {
	typeID  uuid.UUID
	linkID  uuid.UUID
	fieldID uuid.UUID
}

// eoTypeTestApp registers the REAL designer route set behind a permission
// guard that grants exactly `granted`, and returns the app plus those ids.
func eoTypeTestApp(granted ...string) (*fiber.App, eoTypeIDs) {
	ids := eoTypeIDs{typeID: uuid.New(), linkID: uuid.New(), fieldID: uuid.New()}
	field := models.EOFieldDef{ID: ids.fieldID, RecordTypeID: ids.typeID, Key: "subject", Kind: models.EOKindText}
	types := &eoTypeAuthzRepo{
		eoFakeTypeRepo: eoFakeTypeRepo{
			byID:      &models.EORecordType{ID: ids.typeID, Key: "directive", Name: "Directive"},
			all:       []models.EORecordType{{ID: ids.typeID, Key: "directive", Name: "Directive"}},
			fieldDefs: []models.EOFieldDef{field},
		},
		linkTypeByID: &models.EOLinkType{ID: ids.linkID, Key: "covers_item", Label: "Covers"},
		fieldDef:     &field,
	}
	h := NewEORecordTypeHandler(types, &eoFakeRecordRepo{})

	app := fiber.New()
	app.Use(eoTestActor(&models.User{ID: uuid.New()}))
	h.RegisterRoutes(app.Group("/eo-record-types"), eoPermFor(granted...))
	return app, ids
}

// TestEOTypeReadRoutesAcceptEoView: the UI cannot render a register list, a
// type name or any form without the type catalogue — the forms are GENERATED
// from each type's field definitions. A pilot user holding eo:view and not
// eo:types:manage must therefore reach the four read routes — and must get a
// 200 from them, not merely something that is not a 403.
func TestEOTypeReadRoutesAcceptEoView(t *testing.T) {
	app, ids := eoTypeTestApp("eo:view")

	cases := []struct {
		name   string
		method string
		path   string
	}{
		{"List", "GET", "/eo-record-types"},
		{"Get", "GET", "/eo-record-types/" + ids.typeID.String()},
		{"ListLinkTypes", "GET", "/eo-record-types/link-types"},
		// The ancestor-walk route: a create form is built from this type's
		// field definitions AND every ancestor's, and List does not return
		// fields. The other direction — the three field MUTATIONS stay on
		// eo:types:manage — is pinned by the write table below.
		{"ListFields", "GET", "/eo-record-types/" + ids.typeID.String() + "/fields"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := app.Test(httptest.NewRequest(tc.method, tc.path, nil))
			if err != nil {
				t.Fatalf("app.Test: %v", err)
			}
			if resp.StatusCode == fiber.StatusForbidden {
				t.Fatalf("%s %s status = 403 for a caller holding eo:view: the read route still demands eo:types:manage", tc.method, tc.path)
			}
			if resp.StatusCode == fiber.StatusUnauthorized {
				t.Fatalf("%s %s status = 401; permission denial must answer 403 (the frontend logs out on 401)", tc.method, tc.path)
			}
			if resp.StatusCode != fiber.StatusOK {
				t.Fatalf("%s %s status = %d, want 200", tc.method, tc.path, resp.StatusCode)
			}
		})
	}
}

// TestEOTypeWriteRoutesStillRequireManage asserts the mutating routes AND
// the one read route that lives among them, in BOTH directions: refused to a
// caller holding only eo:view, reachable for the designer.
//
// A single-route test would keep passing while a sibling silently widened —
// and "every route in this file shares one constant" is exactly how the read
// routes came to be locked behind eo:types:manage in the first place. The
// opposite drift is just as silent, which is why GET /:id/fields is in this
// table with wantViewer403 false rather than left untested: a line that
// re-narrows it fails here.
func TestEOTypeWriteRoutesStillRequireManage(t *testing.T) {
	viewer, viewerIDs := eoTypeTestApp("eo:view")
	// The designer app holds BOTH codes, because the role that uses these
	// routes does: eo_admin is seeded with eo:view and eo:types:manage
	// (database.Seed), and admin holds everything. A manage-only app would
	// report GET /:id/fields as unreachable for a caller that cannot exist.
	// The control half is unweakened by this: what proves a mutation demands
	// eo:types:manage is the viewer half's 403, not this one.
	manager, managerIDs := eoTypeTestApp("eo:types:manage", "eo:view")

	// path is built from the ids of whichever app is being called, so the
	// control request is the same ROUTE, not a request that 404s instead.
	cases := []struct {
		name   string
		method string
		path   func(eoTypeIDs) string
		// wantViewer403 is the DIRECTION: true for a designer privilege, and
		// false for a route an eo:view caller must reach.
		wantViewer403 bool
	}{
		{"Create", "POST", func(eoTypeIDs) string { return "/eo-record-types" }, true},
		{"Update", "PUT", func(i eoTypeIDs) string { return "/eo-record-types/" + i.typeID.String() }, true},
		{"Delete", "DELETE", func(i eoTypeIDs) string { return "/eo-record-types/" + i.typeID.String() }, true},
		{"CreateLinkType", "POST", func(eoTypeIDs) string { return "/eo-record-types/link-types" }, true},
		{"UpdateLinkType", "PUT", func(i eoTypeIDs) string { return "/eo-record-types/link-types/" + i.linkID.String() }, true},
		{"DeleteLinkType", "DELETE", func(i eoTypeIDs) string { return "/eo-record-types/link-types/" + i.linkID.String() }, true},
		// The field-definition routes. A1 left these four with NO permission
		// test at all, and its reviewer mutated GET /:id/fields from
		// eo:types:manage to eo:view with the whole suite still passing.
		//
		// The READ is eo:view as of the Plan 3 fix wave. It is not a
		// confidentiality boundary — GET /:id already returns every type's
		// full field configuration through t.Fields to the same caller — and
		// gating it on eo:types:manage left the pilot's own operator role
		// (executive_office: eo:view, no eo:types:manage) unable to render a
		// create form for a type or any of its ancestors. The three
		// mutations are the designer privilege and stay where they were.
		{"ListFields", "GET", func(i eoTypeIDs) string { return "/eo-record-types/" + i.typeID.String() + "/fields" }, false},
		{"CreateField", "POST", func(i eoTypeIDs) string { return "/eo-record-types/" + i.typeID.String() + "/fields" }, true},
		{"UpdateField", "PUT", func(i eoTypeIDs) string {
			return "/eo-record-types/" + i.typeID.String() + "/fields/" + i.fieldID.String()
		}, true},
		{"DeleteField", "DELETE", func(i eoTypeIDs) string {
			return "/eo-record-types/" + i.typeID.String() + "/fields/" + i.fieldID.String()
		}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			denied := tc.path(viewerIDs)
			req := httptest.NewRequest(tc.method, denied, strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			resp, err := viewer.Test(req)
			if err != nil {
				t.Fatalf("app.Test: %v", err)
			}
			switch {
			case tc.wantViewer403 && resp.StatusCode != fiber.StatusForbidden:
				t.Fatalf("%s %s status = %d for a caller holding only eo:view, want 403: this mutating route no longer requires eo:types:manage", tc.method, denied, resp.StatusCode)
			case !tc.wantViewer403 && resp.StatusCode == fiber.StatusForbidden:
				t.Fatalf("%s %s status = 403 for a caller holding eo:view: the EO operator role holds eo:view and NOT eo:types:manage, so re-narrowing this route makes every create form unrenderable for the pilot's own staff", tc.method, denied)
			case !tc.wantViewer403 && resp.StatusCode == fiber.StatusUnauthorized:
				t.Fatalf("%s %s status = 401; permission denial must answer 403 (the frontend logs out on 401)", tc.method, denied)
			}

			// Control: the same route must EXIST and be reachable with
			// eo:types:manage, otherwise the 403 above could be a 404 in
			// disguise or a route that nobody can call at all.
			allowed := tc.path(managerIDs)
			ctlReq := httptest.NewRequest(tc.method, allowed, strings.NewReader("{}"))
			ctlReq.Header.Set("Content-Type", "application/json")
			ctlResp, err := manager.Test(ctlReq)
			if err != nil {
				t.Fatalf("app.Test (manage): %v", err)
			}
			if ctlResp.StatusCode == fiber.StatusForbidden || ctlResp.StatusCode == fiber.StatusNotFound {
				t.Fatalf("%s %s status = %d for a caller holding eo:types:manage, want the route to be reachable", tc.method, allowed, ctlResp.StatusCode)
			}
		})
	}
}

// eoTypeWriteFailRepo fails every type write with a fixed error, so the
// designer's mapping of that error can be driven from the route.
type eoTypeWriteFailRepo struct {
	eoFakeTypeRepo
	err error
}

func (f *eoTypeWriteFailRepo) Create(_ context.Context, _ *models.EORecordType) error { return f.err }
func (f *eoTypeWriteFailRepo) Update(_ context.Context, _ *models.EORecordType) error { return f.err }

// TestEOTypeWriteErrorsAreActionable: both failures the per-type permission
// write can produce used to arrive as a bare 500 with the cause in a log the
// operator cannot read — "Internal server error" and a dead designer screen.
// One of them is the operator's own to resolve and names a bad KEY; the other
// names a role they must restore. Neither is readable unless the response says
// so.
func TestEOTypeWriteErrorsAreActionable(t *testing.T) {
	eoInitValidator(t)
	body := `{"key":"eo_objection","name":"Supplier Objection","reference_prefix":"EO-OBJ","reference_pattern":"EO-OBJ-YYYY-NNN"}`

	t.Run("a key whose permission codes already exist is a field error naming them", func(t *testing.T) {
		repo := &eoTypeWriteFailRepo{err: fmt.Errorf("create eo record type %q: %w: %s", "eo_objection",
			repository.ErrEOTypePermissionCodesExist, "eo:eo_objection:view, eo:eo_objection:create")}
		h := NewEORecordTypeHandler(repo, &eoFakeRecordRepo{})
		app := fiber.New()
		app.Use(eoTestActor(&models.User{ID: uuid.New()}))
		h.RegisterRoutes(app.Group("/eo-record-types"), eoPermFor("eo:types:manage"))

		req := httptest.NewRequest("POST", "/eo-record-types", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("app.Test: %v", err)
		}
		if resp.StatusCode != fiber.StatusBadRequest {
			t.Fatalf("status = %d, want 400: this is the operator's to fix, not a server fault", resp.StatusCode)
		}
		var out struct {
			Errors map[string]string `json:"errors"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		msg, ok := out.Errors["key"]
		if !ok {
			t.Fatalf("errors = %v, want the refusal against the key field", out.Errors)
		}
		if !strings.Contains(msg, "eo:eo_objection:view") {
			t.Errorf("message %q does not name the codes in the way, so the operator cannot act on it", msg)
		}
	})

	t.Run("a missing role names the roles to restore", func(t *testing.T) {
		repo := &eoTypeWriteFailRepo{err: fmt.Errorf("%w: %q is required", repository.ErrEOTypePermissionRoleMissing, "executive_office")}
		h := NewEORecordTypeHandler(repo, &eoFakeRecordRepo{})
		app := fiber.New()
		app.Use(eoTestActor(&models.User{ID: uuid.New()}))
		h.RegisterRoutes(app.Group("/eo-record-types"), eoPermFor("eo:types:manage"))

		req := httptest.NewRequest("POST", "/eo-record-types", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("app.Test: %v", err)
		}
		if resp.StatusCode != fiber.StatusInternalServerError {
			t.Fatalf("status = %d, want 500: a deployment missing a seeded role is not a bad request", resp.StatusCode)
		}
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		msg, _ := out["error"].(string)
		if msg == "" {
			msg, _ = out["message"].(string)
		}
		if !strings.Contains(msg, "executive_office") {
			t.Errorf("response %v does not name executive_office; the operator is left with a dead screen", out)
		}
	})
}

// ── §7.14's confirmed destructive edit ──────────────────────────────────────

// eoConfirmTypeRepo is the designer repository with the writes a confirmed
// destructive edit needs, and with a transaction that behaves like one: work
// done inside it reaches the repository only if the function returns nil.
//
// That is what makes "the audit row is written in the edit's transaction" a
// testable claim rather than an assertion. A fake that appended every write
// straight to the committed slices could not tell an audit row written WITH
// the edit from one written beside it, which is the exact failure Plan 3's
// Task C1 established matters here.
type eoConfirmTypeRepo struct {
	eoFakeTypeRepo
	fieldDef  *models.EOFieldDef
	writeErr  error
	committed eoConfirmWrites
	// lastTx is the transaction the handler opened, kept after it closes so
	// a test can see what was done inside the SAME one.
	lastTx *eoConfirmTypeTx
	// auditOutsideTx counts audit rows written on the repository the handler
	// holds rather than on a transaction-bound one. It is zero in every test
	// here, and a non-zero value is the "separate handle" defect itself.
	auditOutsideTx int
}

// eoConfirmWrites is what a repository (or one transaction's buffer) has done.
type eoConfirmWrites struct {
	fieldUpdates []models.EOFieldDef
	fieldDeletes []uuid.UUID
	typeUpdates  []models.EORecordType
	audit        []models.ActionLog
}

func (w *eoConfirmWrites) merge(other eoConfirmWrites) {
	w.fieldUpdates = append(w.fieldUpdates, other.fieldUpdates...)
	w.fieldDeletes = append(w.fieldDeletes, other.fieldDeletes...)
	w.typeUpdates = append(w.typeUpdates, other.typeUpdates...)
	w.audit = append(w.audit, other.audit...)
}

func (f *eoConfirmTypeRepo) GetFieldDef(_ context.Context, id uuid.UUID) (*models.EOFieldDef, error) {
	if f.fieldDef != nil && f.fieldDef.ID == id {
		return f.fieldDef, nil
	}
	return nil, nil
}

func (f *eoConfirmTypeRepo) UpdateFieldDef(_ context.Context, def *models.EOFieldDef) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.committed.fieldUpdates = append(f.committed.fieldUpdates, *def)
	return nil
}

func (f *eoConfirmTypeRepo) DeleteFieldDef(_ context.Context, id uuid.UUID) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.committed.fieldDeletes = append(f.committed.fieldDeletes, id)
	return nil
}

func (f *eoConfirmTypeRepo) Update(_ context.Context, t *models.EORecordType) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.committed.typeUpdates = append(f.committed.typeUpdates, *t)
	return nil
}

func (f *eoConfirmTypeRepo) WriteActionLog(_ context.Context, entry *models.ActionLog) error {
	f.auditOutsideTx++
	f.committed.audit = append(f.committed.audit, *entry)
	return nil
}

func (f *eoConfirmTypeRepo) Transaction(_ context.Context, fn func(repository.EORecordTypeRepository) error) error {
	tx := &eoConfirmTypeTx{parent: f}
	f.lastTx = tx
	if err := fn(tx); err != nil {
		return err // nothing in tx.pending reaches the repository
	}
	f.committed.merge(tx.pending)
	return nil
}

// eoConfirmTypeTx is the transaction-bound repository. It embeds the interface
// so that a write this task did not intend to make inside a transaction
// nil-panics loudly rather than being silently accepted.
type eoConfirmTypeTx struct {
	repository.EORecordTypeRepository
	parent  *eoConfirmTypeRepo
	pending eoConfirmWrites
}

func (tx *eoConfirmTypeTx) UpdateFieldDef(_ context.Context, def *models.EOFieldDef) error {
	if tx.parent.writeErr != nil {
		return tx.parent.writeErr
	}
	tx.pending.fieldUpdates = append(tx.pending.fieldUpdates, *def)
	return nil
}

func (tx *eoConfirmTypeTx) DeleteFieldDef(_ context.Context, id uuid.UUID) error {
	if tx.parent.writeErr != nil {
		return tx.parent.writeErr
	}
	tx.pending.fieldDeletes = append(tx.pending.fieldDeletes, id)
	return nil
}

func (tx *eoConfirmTypeTx) CreateFieldDef(_ context.Context, def *models.EOFieldDef) error {
	if tx.parent.writeErr != nil {
		return tx.parent.writeErr
	}
	tx.pending.fieldUpdates = append(tx.pending.fieldUpdates, *def)
	return nil
}

func (tx *eoConfirmTypeTx) Update(_ context.Context, t *models.EORecordType) error {
	if tx.parent.writeErr != nil {
		return tx.parent.writeErr
	}
	tx.pending.typeUpdates = append(tx.pending.typeUpdates, *t)
	return nil
}

func (tx *eoConfirmTypeTx) WriteActionLog(_ context.Context, entry *models.ActionLog) error {
	tx.pending.audit = append(tx.pending.audit, *entry)
	return nil
}

// eoConfirmFixture is the shape the guard rail is hardest on: an ABSTRACT base
// type holding a field, no records of its own, and a concrete descendant that
// holds three. Every edit to the base is therefore destructive, and the
// confirmation has to name the base — not the descendant whose records are the
// reason the question is being asked.
type eoConfirmFixture struct {
	base    models.EORecordType
	leaf    models.EORecordType
	fieldID uuid.UUID
	types   *eoConfirmTypeRepo
	records *eoFakeRecordRepo
	app     *fiber.App
	actor   *models.User
}

func eoNewConfirmFixture(t *testing.T) *eoConfirmFixture {
	t.Helper()
	base := models.EORecordType{ID: uuid.New(), Key: "eo_base", Name: "Base", IsAbstract: true,
		ReferencePrefix: "EO", ReferencePattern: "EO-YYYY-NNN"}
	leaf := models.EORecordType{ID: uuid.New(), Key: "eo_directive", Name: "Directive",
		ParentTypeID: &base.ID, ReferencePrefix: "EO-DIR", ReferencePattern: "EO-DIR-YYYY-NNN"}
	field := models.EOFieldDef{
		ID: uuid.New(), RecordTypeID: base.ID, Key: "attachments", Label: "Attachments",
		Kind: models.EOKindFile, RequiredMode: models.EORequiredNever,
	}
	types := &eoConfirmTypeRepo{
		eoFakeTypeRepo: eoFakeTypeRepo{
			byID:      &base,
			all:       []models.EORecordType{base, leaf},
			fieldDefs: []models.EOFieldDef{field},
			children:  map[uuid.UUID][]models.EORecordType{base.ID: {leaf}},
		},
		fieldDef: &field,
	}
	// The abstract base holds nothing of its own; its descendant holds three.
	records := &eoFakeRecordRepo{totalByType: map[uuid.UUID]int64{leaf.ID: 3}}

	f := &eoConfirmFixture{base: base, leaf: leaf, fieldID: field.ID, types: types, records: records,
		actor: eoTestUser()}
	h := NewEORecordTypeHandler(types, records)
	f.app = fiber.New()
	// THE REAL RequestContext MIDDLEWARE, which main.go installs app-wide
	// (cmd/server/main.go) and which is what puts the caller's IP and
	// User-Agent where the audit row reads them from. A fixture without it
	// would hand the handler two empty strings, and the truncation test below
	// would pass against a handler that did not truncate.
	f.app.Use(middleware.RequestContext())
	f.app.Use(eoTestActor(f.actor))
	h.RegisterRoutes(f.app.Group("/eo-record-types"), eoNoPerm2)
	return f
}

// eoNoPerm2 is eoNoPerm in the EOPermissionMiddleware shape RegisterRoutes
// takes: these tests are about the guard rail, not about who may reach it.
func eoNoPerm2(_ ...string) fiber.Handler {
	return func(c *fiber.Ctx) error { return c.Next() }
}

// eoConfirmReason is a stated backfill rule of the kind §7.14 asks for: what
// will be done about the records the change leaves behind.
const eoConfirmReason = "Documenta is connected as of this morning; the registry clerk attaches the letter to each of the 3 open directives before its next transition, and the four closed ones are exempt by the client's note of 2026-11-20."

// eoRequireFieldBody is the PUT body that makes the relaxed file field
// demanded again at a state — the edit blocker 1 of the Documenta list has no
// supported route for.
func eoRequireFieldBody(confirm map[string]any) []byte {
	body := map[string]any{
		"key": "attachments", "label": "Attachments", "kind": models.EOKindFile,
		"required_mode": models.EORequiredAtState,
	}
	if confirm != nil {
		body["confirm_destructive"] = confirm
	}
	out, _ := json.Marshal(body)
	return out
}

func eoConfirmBlock(typeKey, fieldKey, change string) map[string]any {
	return map[string]any{"type_key": typeKey, "field_key": fieldKey, "change": change, "reason": eoConfirmReason}
}

func (f *eoConfirmFixture) do(t *testing.T, method, path string, body []byte) *http.Response {
	t.Helper()
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := f.app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	return resp
}

// eoAuditDetail is what the audit row's new_value carries.
type eoAuditDetail struct {
	TypeKey  string `json:"type_key"`
	FieldKey string `json:"field_key"`
	Change   string `json:"change"`
	Reason   string `json:"reason"`
	Refusal  string `json:"refusal"`
}

// TestAConfirmedFieldEditAppliesAndAuditsInOneTransaction is blocker 1 of the
// three gating EO_DOCUMENTA_ENABLED=true, driven end to end through the route:
// a file field relaxed to `never` is made `at_state` again on a type whose
// descendant holds records, which before this task had no supported route at
// all.
func TestAConfirmedFieldEditAppliesAndAuditsInOneTransaction(t *testing.T) {
	f := eoNewConfirmFixture(t)

	resp := f.do(t, "PUT", fmt.Sprintf("/eo-record-types/%s/fields/%s", f.base.ID, f.fieldID),
		eoRequireFieldBody(eoConfirmBlock("eo_base", "attachments", models.EODestructiveFieldRequired)))
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want 200: a confirmation naming this type, this field and this change did not authorise the edit", resp.StatusCode)
	}
	if len(f.types.committed.fieldUpdates) != 1 {
		t.Fatalf("field updates = %d, want 1", len(f.types.committed.fieldUpdates))
	}
	if got := f.types.committed.fieldUpdates[0].RequiredMode; got != models.EORequiredAtState {
		t.Errorf("stored required_mode = %q, want %q", got, models.EORequiredAtState)
	}
	if len(f.types.committed.audit) != 1 {
		t.Fatalf("audit rows = %d, want exactly 1", len(f.types.committed.audit))
	}
	if f.types.auditOutsideTx != 0 {
		t.Errorf("%d audit row(s) written on the repository's own handle: a confirmed edit's audit row must be written on the transaction that carries the edit", f.types.auditOutsideTx)
	}
	// Both writes happened inside ONE transaction, which is the property
	// Plan 3's Task C1 established: an entry written through a second handle
	// commits for an edit that rolled back, or is lost for one that did not.
	if f.types.lastTx == nil {
		t.Fatal("no transaction was opened: the edit and its audit row cannot have been atomic")
	}
	if len(f.types.lastTx.pending.fieldUpdates) != 1 || len(f.types.lastTx.pending.audit) != 1 {
		t.Errorf("inside the transaction: %d field update(s) and %d audit row(s), want 1 and 1",
			len(f.types.lastTx.pending.fieldUpdates), len(f.types.lastTx.pending.audit))
	}

	entry := f.types.committed.audit[0]
	if entry.UserID != f.actor.ID {
		t.Errorf("audit user = %s, want the confirming actor %s", entry.UserID, f.actor.ID)
	}
	if entry.ResourceID != f.base.ID.String() {
		t.Errorf("audit resource = %q, want the edited type %s", entry.ResourceID, f.base.ID)
	}
	if entry.Module == "" || entry.Action == "" {
		t.Errorf("audit module/action = %q/%q, want both set so the row is findable in the action-log screen", entry.Module, entry.Action)
	}
	if utf8.RuneCountInString(entry.Description) > 500 {
		t.Errorf("audit description is %d characters; action_logs.description is varchar(500) and the INSERT would fail inside the edit's transaction", utf8.RuneCountInString(entry.Description))
	}
	var detail eoAuditDetail
	if err := json.Unmarshal([]byte(entry.NewValue), &detail); err != nil {
		t.Fatalf("audit new_value is not the confirmation detail: %v (%q)", err, entry.NewValue)
	}
	if detail.TypeKey != "eo_base" || detail.FieldKey != "attachments" || detail.Change != models.EODestructiveFieldRequired {
		t.Errorf("audit detail = %+v, want it to name the type, the field and the change", detail)
	}
	if detail.Reason != eoConfirmReason {
		t.Errorf("audit reason = %q, want the operator's stated rule verbatim", detail.Reason)
	}
	// The refusal the confirmation overrode is the only durable statement of
	// WHAT was destroyed. Without it the row says an edit was confirmed and
	// not what the edit did to the records.
	if !strings.Contains(detail.Refusal, "attachments") || !strings.Contains(detail.Refusal, "destructive") {
		t.Errorf("audit refusal = %q, want the §7.14 refusal this confirmation overrode", detail.Refusal)
	}
}

// TestAConfirmedEditThatCannotBeWrittenLeavesNoAuditRow: the audit row says a
// destructive edit was applied. If the edit is rolled back the row must go
// with it, or the register's history records a change that never happened.
func TestAConfirmedEditThatCannotBeWrittenLeavesNoAuditRow(t *testing.T) {
	f := eoNewConfirmFixture(t)
	f.types.writeErr = fmt.Errorf("deadlock detected")

	resp := f.do(t, "PUT", fmt.Sprintf("/eo-record-types/%s/fields/%s", f.base.ID, f.fieldID),
		eoRequireFieldBody(eoConfirmBlock("eo_base", "attachments", models.EODestructiveFieldRequired)))
	if resp.StatusCode != fiber.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
	if len(f.types.committed.audit) != 0 {
		t.Errorf("audit rows = %d after a failed edit, want 0: the row survived a write that did not",
			len(f.types.committed.audit))
	}
	if len(f.types.committed.fieldUpdates) != 0 {
		t.Errorf("field updates = %d after a failed write, want 0", len(f.types.committed.fieldUpdates))
	}
}

// TestAnUnconfirmedDestructiveFieldEditIsStillRefused: the first half of
// §7.14 is unchanged. Nothing about an edit that carries no confirmation
// moved, including the refusal the operator reads.
func TestAnUnconfirmedDestructiveFieldEditIsStillRefused(t *testing.T) {
	f := eoNewConfirmFixture(t)

	resp := f.do(t, "PUT", fmt.Sprintf("/eo-record-types/%s/fields/%s", f.base.ID, f.fieldID),
		eoRequireFieldBody(nil))
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	body := eoReadBody(t, resp)
	if !strings.Contains(body, "attachments") || !strings.Contains(body, "destructive") {
		t.Errorf("refusal body = %q, want the §7.14 message naming the field", body)
	}
	if len(f.types.committed.fieldUpdates) != 0 || len(f.types.committed.audit) != 0 {
		t.Errorf("a refused edit wrote %d field update(s) and %d audit row(s), want none",
			len(f.types.committed.fieldUpdates), len(f.types.committed.audit))
	}
}

// TestAConfirmationNamingTheDescendantDoesNotAuthoriseTheAncestorEdit is the
// hazard the handler creates and the model closes: hasRecords is computed over
// the type AND EVERY TYPE BENEATH IT, so this edit to the ABSTRACT base is
// destructive only because the descendant holds records. A confirmation
// obtained while looking at that descendant names the descendant — and must
// not carry a change to the ancestor, whose other children nobody reviewed.
func TestAConfirmationNamingTheDescendantDoesNotAuthoriseTheAncestorEdit(t *testing.T) {
	f := eoNewConfirmFixture(t)

	resp := f.do(t, "PUT", fmt.Sprintf("/eo-record-types/%s/fields/%s", f.base.ID, f.fieldID),
		eoRequireFieldBody(eoConfirmBlock("eo_directive", "attachments", models.EODestructiveFieldRequired)))
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("status = %d, want 400: a confirmation naming %q authorised an edit to %q", resp.StatusCode, "eo_directive", "eo_base")
	}
	body := eoReadBody(t, resp)
	if !strings.Contains(body, "eo_directive") || !strings.Contains(body, "eo_base") {
		t.Errorf("refusal body = %q, want it to name the confirmed type and the edited one", body)
	}
	if len(f.types.committed.fieldUpdates) != 0 || len(f.types.committed.audit) != 0 {
		t.Errorf("a refused edit wrote %d field update(s) and %d audit row(s), want none",
			len(f.types.committed.fieldUpdates), len(f.types.committed.audit))
	}
}

// TestAnEditWithNothingToConfirmWritesNoAuditRow: the audit row records an
// OVERRIDE, not a request. An ordinary edit writes none — and neither does one
// that carries a well-formed confirmation for a change it does not make, which
// is what a client would send if it attached the operator's last confirmation
// to every subsequent save.
func TestAnEditWithNothingToConfirmWritesNoAuditRow(t *testing.T) {
	relabel := func(confirm map[string]any) []byte {
		body := map[string]any{
			"key": "attachments", "label": "Attachments (scanned)", "kind": models.EOKindFile,
			"required_mode": models.EORequiredNever,
		}
		if confirm != nil {
			body["confirm_destructive"] = confirm
		}
		out, _ := json.Marshal(body)
		return out
	}
	cases := map[string]map[string]any{
		"no confirmation":       nil,
		"an unconsumed one":     eoConfirmBlock("eo_base", "attachments", models.EODestructiveFieldRequired),
		"one for another field": eoConfirmBlock("eo_base", "subject", models.EODestructiveFieldRemoved),
	}
	for name, confirm := range cases {
		t.Run(name, func(t *testing.T) {
			f := eoNewConfirmFixture(t)
			resp := f.do(t, "PUT", fmt.Sprintf("/eo-record-types/%s/fields/%s", f.base.ID, f.fieldID), relabel(confirm))
			if resp.StatusCode != fiber.StatusOK {
				t.Fatalf("status = %d, want 200: a harmless relabel was refused", resp.StatusCode)
			}
			if len(f.types.committed.audit) != 0 {
				t.Errorf("audit rows = %d, want 0: an edit that overrode nothing was recorded as a confirmed destructive edit", len(f.types.committed.audit))
			}
			if len(f.types.committed.fieldUpdates) != 1 {
				t.Errorf("field updates = %d, want 1", len(f.types.committed.fieldUpdates))
			}
		})
	}
}

// TestAConfirmedFieldRemovalIsAcceptedOnDelete: removing a field is the case
// §7.14 exists for, and DELETE carries no attribute set — so the confirmation
// arrives in an optional body, which this proves is read. The same request
// with no body is still refused.
func TestAConfirmedFieldRemovalIsAcceptedOnDelete(t *testing.T) {
	path := func(f *eoConfirmFixture) string {
		return fmt.Sprintf("/eo-record-types/%s/fields/%s", f.base.ID, f.fieldID)
	}

	f := eoNewConfirmFixture(t)
	if resp := f.do(t, "DELETE", path(f), nil); resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("status = %d for an unconfirmed removal, want 400", resp.StatusCode)
	}

	f = eoNewConfirmFixture(t)
	body, _ := json.Marshal(map[string]any{
		"confirm_destructive": eoConfirmBlock("eo_base", "attachments", models.EODestructiveFieldRemoved),
	})
	resp := f.do(t, "DELETE", path(f), body)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d for a confirmed removal, want 200 (body = %q)", resp.StatusCode, eoReadBody(t, resp))
	}
	if len(f.types.committed.fieldDeletes) != 1 {
		t.Errorf("field deletes = %d, want 1", len(f.types.committed.fieldDeletes))
	}
	if len(f.types.committed.audit) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(f.types.committed.audit))
	}
	var detail eoAuditDetail
	if err := json.Unmarshal([]byte(f.types.committed.audit[0].NewValue), &detail); err != nil {
		t.Fatalf("audit new_value: %v", err)
	}
	if detail.Change != models.EODestructiveFieldRemoved || detail.FieldKey != "attachments" {
		t.Errorf("audit detail = %+v, want the removal of attachments", detail)
	}
}

// TestAConfirmedTypeEditAuditsTheTypeLevelChange: the six changes that are not
// about a field take the same route, and their audit row names no field.
func TestAConfirmedTypeEditAuditsTheTypeLevelChange(t *testing.T) {
	f := eoNewConfirmFixture(t)
	body, _ := json.Marshal(map[string]any{
		"key": "eo_base", "name": "Base", "is_abstract": true,
		"reference_prefix": "EO2", "reference_pattern": "EO-YYYY-NNN",
		"confirm_destructive": map[string]any{
			"type_key": "eo_base", "change": models.EODestructiveReferencePrefix, "reason": eoConfirmReason,
		},
	})
	resp := f.do(t, "PUT", "/eo-record-types/"+f.base.ID.String(), body)
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want 200 (body = %q)", resp.StatusCode, eoReadBody(t, resp))
	}
	if len(f.types.committed.typeUpdates) != 1 || len(f.types.committed.audit) != 1 {
		t.Fatalf("type updates = %d and audit rows = %d, want 1 and 1",
			len(f.types.committed.typeUpdates), len(f.types.committed.audit))
	}
	var detail eoAuditDetail
	if err := json.Unmarshal([]byte(f.types.committed.audit[0].NewValue), &detail); err != nil {
		t.Fatalf("audit new_value: %v", err)
	}
	if detail.Change != models.EODestructiveReferencePrefix || detail.FieldKey != "" {
		t.Errorf("audit detail = %+v, want the prefix change and no field", detail)
	}
}

// eoReadBody reads a response body for an assertion about what the operator is
// told.
func eoReadBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(out)
}

// TestAConfirmationOnAnAddedFieldConsumesNothing pins the claim CreateField's
// comment makes: adding a definition is additive, so there is nothing for a
// confirmation to authorise and no audit row is owed — even on a type whose
// descendant holds records, which is the state where every other edit to it is
// refused. If a later rule makes some addition destructive, this fails and the
// audit arm that route keeps becomes reachable.
func TestAConfirmationOnAnAddedFieldConsumesNothing(t *testing.T) {
	f := eoNewConfirmFixture(t)
	body, _ := json.Marshal(map[string]any{
		"key": "decision_note", "label": "Decision note", "kind": models.EOKindText,
		"required_mode":       models.EORequiredAlways,
		"confirm_destructive": eoConfirmBlock("eo_base", "decision_note", models.EODestructiveFieldRequired),
	})
	resp := f.do(t, "POST", fmt.Sprintf("/eo-record-types/%s/fields", f.base.ID), body)
	if resp.StatusCode != fiber.StatusCreated {
		t.Fatalf("status = %d, want 201 (body = %q)", resp.StatusCode, eoReadBody(t, resp))
	}
	if f.types.createFieldCalls != 1 {
		t.Errorf("CreateFieldDef calls = %d, want 1", f.types.createFieldCalls)
	}
	if len(f.types.committed.audit) != 0 {
		t.Errorf("audit rows = %d, want 0: adding a field overrode nothing", len(f.types.committed.audit))
	}
}

// TestAConfirmedEditIsNotLostToAnOverlongUserAgent: the audit row carries two
// client-controlled columns, and it is written INSIDE the edit's transaction —
// so a value too long for its column does not merely truncate the row, it
// fails the INSERT (SQLSTATE 22001) and rolls back the destructive edit the
// operator just confirmed, with an opaque 500.
func TestAConfirmedEditIsNotLostToAnOverlongUserAgent(t *testing.T) {
	f := eoNewConfirmFixture(t)

	req := httptest.NewRequest("PUT", fmt.Sprintf("/eo-record-types/%s/fields/%s", f.base.ID, f.fieldID),
		bytes.NewReader(eoRequireFieldBody(eoConfirmBlock("eo_base", "attachments", models.EODestructiveFieldRequired))))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", strings.Repeat("Mozilla/5.0 ", 50)) // 600 characters
	resp, err := f.app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(f.types.committed.audit) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(f.types.committed.audit))
	}
	entry := f.types.committed.audit[0]
	// action_logs.user_agent is varchar(500) and .ip_address is varchar(45).
	if n := utf8.RuneCountInString(entry.UserAgent); n > 500 {
		t.Errorf("audit user_agent is %d characters; the column holds 500 and the INSERT shares the edit's transaction", n)
	}
	if n := utf8.RuneCountInString(entry.IPAddress); n > 45 {
		t.Errorf("audit ip_address is %d characters; the column holds 45", n)
	}
}

// TestAMalformedDeleteBodyIsRefusedRatherThanIgnored: the optional body is the
// only way to confirm a removal, so reading an unparseable one as "no
// confirmation" would turn a typo into either the wrong refusal or — here,
// where the subtree holds no records and the removal needs no confirmation —
// a silent 200 that deletes the field the operator was still deciding about.
func TestAMalformedDeleteBodyIsRefusedRatherThanIgnored(t *testing.T) {
	f := eoNewConfirmFixture(t)
	// Nothing anywhere in the subtree: without the body check this request is
	// an ordinary allowed removal and answers 200.
	f.records.totalByType = map[uuid.UUID]int64{}

	resp := f.do(t, "DELETE", fmt.Sprintf("/eo-record-types/%s/fields/%s", f.base.ID, f.fieldID),
		[]byte(`{"confirm_destructive": {"type_key": "eo_base",`))
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("status = %d for an unparseable DELETE body, want 400", resp.StatusCode)
	}
	if len(f.types.committed.fieldDeletes) != 0 {
		t.Errorf("the field was deleted under a request body nothing could read")
	}

	// The control: the same request with no body at all is the ordinary
	// removal, so the 400 above is the body check and not the guard rail.
	f = eoNewConfirmFixture(t)
	f.records.totalByType = map[uuid.UUID]int64{}
	if resp := f.do(t, "DELETE", fmt.Sprintf("/eo-record-types/%s/fields/%s", f.base.ID, f.fieldID), nil); resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d for an unconfirmed removal on an empty subtree, want 200", resp.StatusCode)
	}
}

// linkEndpointApp registers the real designer routes over a fake catalogue of
// one concrete type, one abstract type and one stored link type.
func linkEndpointApp(t *testing.T) (app *fiber.App, concrete, abstract, linkID uuid.UUID) {
	t.Helper()
	eoInitValidator(t)
	concrete, abstract, linkID = uuid.New(), uuid.New(), uuid.New()
	all := []models.EORecordType{
		{ID: concrete, Key: "directive", Name: "Directive"},
		{ID: abstract, Key: "base", Name: "Base", IsAbstract: true},
	}
	repo := &eoTypeAuthzRepo{
		eoFakeTypeRepo: eoFakeTypeRepo{byID: &all[0], all: all},
		linkTypeByID:   &models.EOLinkType{ID: linkID, Key: "covers", Label: "Covers", SourceTypeIDs: []byte(`["any"]`), TargetTypeIDs: []byte(`["any"]`)},
	}
	h := NewEORecordTypeHandler(repo, &eoFakeRecordRepo{})
	app = fiber.New()
	app.Use(eoTestActor(&models.User{ID: uuid.New()}))
	h.RegisterRoutes(app.Group("/eo-record-types"), eoPermFor("eo:types:manage"))
	return app, concrete, abstract, linkID
}

func linkEndpointCall(t *testing.T, app *fiber.App, method, path, body string) (int, map[string]string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	var out struct {
		Errors map[string]string `json:"errors"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out.Errors
}

func TestLinkTypeEndpointsAreValidatedAtConfigurationTime(t *testing.T) {
	app, concrete, abstract, linkID := linkEndpointApp(t)
	missing := uuid.New()
	mk := func(src, tgt string) string {
		return `{"key":"k","label":"L","cardinality":"many","source_type_ids":` + src + `,"target_type_ids":` + tgt + `}`
	}
	ok := `["` + concrete.String() + `"]`
	cases := []struct {
		name, src, tgt string
		wantField      string // "" means accepted
	}{
		{"empty source list", `[]`, ok, "source_type_ids"},
		{"empty target list", ok, `[]`, "target_type_ids"},
		{"null target", ok, `null`, "target_type_ids"},
		{"unknown id", `["` + missing.String() + `"]`, ok, "source_type_ids"},
		{"not a uuid", ok, `["nonsense"]`, "target_type_ids"},
		{"abstract target", ok, `["` + abstract.String() + `"]`, "target_type_ids"},
		{"abstract hidden in a mixed list", `["any","` + abstract.String() + `"]`, ok, "source_type_ids"},
		{"wrong shape", ok, `{"a":1}`, "target_type_ids"},
		{"array any", `["any"]`, ok, ""},
		{"bare string any", ok, `"any"`, ""},
		{"valid pairing", ok, ok, ""},
	}
	for _, tc := range cases {
		for _, route := range []struct{ method, path string }{
			{"POST", "/eo-record-types/link-types"},
			{"PUT", "/eo-record-types/link-types/" + linkID.String()},
		} {
			t.Run(tc.name+" "+route.method, func(t *testing.T) {
				status, errs := linkEndpointCall(t, app, route.method, route.path, mk(tc.src, tc.tgt))
				if tc.wantField == "" {
					if status >= 300 {
						t.Fatalf("status = %d errors = %v, want accepted", status, errs)
					}
					return
				}
				if status != fiber.StatusBadRequest && status != fiber.StatusUnprocessableEntity {
					t.Fatalf("status = %d, want a field-error refusal", status)
				}
				if errs[tc.wantField] == "" || len(errs) != 1 {
					t.Fatalf("errors = %v, want exactly %q", errs, tc.wantField)
				}
			})
		}
	}
}

// An omitted endpoint list on create is empty, and refused; on update it keeps
// the stored value, and is accepted.
func TestLinkTypeOmittedEndpointsCreateRefusedUpdateKept(t *testing.T) {
	app, _, _, linkID := linkEndpointApp(t)
	body := `{"key":"k","label":"L","cardinality":"many"}`
	status, errs := linkEndpointCall(t, app, "POST", "/eo-record-types/link-types", body)
	if status < 400 || errs["source_type_ids"] == "" || errs["target_type_ids"] == "" {
		t.Fatalf("create with no endpoints: status %d errors %v, want both refused", status, errs)
	}
	status, errs = linkEndpointCall(t, app, "PUT", "/eo-record-types/link-types/"+linkID.String(), body)
	if status >= 300 {
		t.Fatalf("update keeping stored endpoints: status %d errors %v", status, errs)
	}
}
