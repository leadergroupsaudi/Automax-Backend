package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

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
