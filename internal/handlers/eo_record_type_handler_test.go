package handlers

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/automax/backend/internal/models"
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
// eo:types:manage must therefore reach the three read routes.
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

// TestEOTypeWriteRoutesStillRequireManage asserts ALL SIX mutating routes.
// A single-route test would keep passing while a sibling silently widened —
// and "every route in this file shares one constant" is exactly how the read
// routes came to be locked behind eo:types:manage in the first place.
func TestEOTypeWriteRoutesStillRequireManage(t *testing.T) {
	viewer, viewerIDs := eoTypeTestApp("eo:view")
	manager, managerIDs := eoTypeTestApp("eo:types:manage")

	// path is built from the ids of whichever app is being called, so the
	// control request is the same ROUTE, not a request that 404s instead.
	cases := []struct {
		name   string
		method string
		path   func(eoTypeIDs) string
	}{
		{"Create", "POST", func(eoTypeIDs) string { return "/eo-record-types" }},
		{"Update", "PUT", func(i eoTypeIDs) string { return "/eo-record-types/" + i.typeID.String() }},
		{"Delete", "DELETE", func(i eoTypeIDs) string { return "/eo-record-types/" + i.typeID.String() }},
		{"CreateLinkType", "POST", func(eoTypeIDs) string { return "/eo-record-types/link-types" }},
		{"UpdateLinkType", "PUT", func(i eoTypeIDs) string { return "/eo-record-types/link-types/" + i.linkID.String() }},
		{"DeleteLinkType", "DELETE", func(i eoTypeIDs) string { return "/eo-record-types/link-types/" + i.linkID.String() }},
		// The field-definition routes. A1 left these four with NO permission
		// test at all, and its reviewer mutated GET /:id/fields from
		// eo:types:manage to eo:view with the whole suite still passing. The
		// three mutations are the dangerous half: an untested write route in
		// a permission file is how A1's original bug arose.
		//
		// The READ is here rather than with the eo:view routes because that
		// is where A1 left it, and deliberately: it is not a confidentiality
		// boundary (GET /:id already returns every type's full field
		// configuration through t.Fields), it is the designer's editing view
		// of the same data. This test pins where it IS, so moving it becomes
		// a decision somebody takes rather than a line that drifts.
		{"ListFields", "GET", func(i eoTypeIDs) string { return "/eo-record-types/" + i.typeID.String() + "/fields" }},
		{"CreateField", "POST", func(i eoTypeIDs) string { return "/eo-record-types/" + i.typeID.String() + "/fields" }},
		{"UpdateField", "PUT", func(i eoTypeIDs) string {
			return "/eo-record-types/" + i.typeID.String() + "/fields/" + i.fieldID.String()
		}},
		{"DeleteField", "DELETE", func(i eoTypeIDs) string {
			return "/eo-record-types/" + i.typeID.String() + "/fields/" + i.fieldID.String()
		}},
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
			if resp.StatusCode != fiber.StatusForbidden {
				t.Fatalf("%s %s status = %d for a caller holding only eo:view, want 403: this mutating route no longer requires eo:types:manage", tc.method, denied, resp.StatusCode)
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
