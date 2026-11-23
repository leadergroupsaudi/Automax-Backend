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
}

func (f *eoTypeAuthzRepo) GetLinkTypeByID(_ context.Context, id uuid.UUID) (*models.EOLinkType, error) {
	if f.linkTypeByID != nil && f.linkTypeByID.ID == id {
		return f.linkTypeByID, nil
	}
	return nil, nil
}

func (f *eoTypeAuthzRepo) CreateLinkType(_ context.Context, _ *models.EOLinkType) error { return nil }
func (f *eoTypeAuthzRepo) UpdateLinkType(_ context.Context, _ *models.EOLinkType) error { return nil }
func (f *eoTypeAuthzRepo) DeleteLinkType(_ context.Context, _ uuid.UUID) error          { return nil }
func (f *eoTypeAuthzRepo) Create(_ context.Context, _ *models.EORecordType) error       { return nil }

// eoTypeTestApp registers the REAL designer route set behind a permission
// guard that grants exactly `granted`, and returns the app plus the type id
// and link-type id the fake repository will answer for.
func eoTypeTestApp(granted ...string) (*fiber.App, uuid.UUID, uuid.UUID) {
	typeID := uuid.New()
	linkTypeID := uuid.New()
	types := &eoTypeAuthzRepo{
		eoFakeTypeRepo: eoFakeTypeRepo{
			byID: &models.EORecordType{ID: typeID, Key: "directive", Name: "Directive"},
			all:  []models.EORecordType{{ID: typeID, Key: "directive", Name: "Directive"}},
		},
		linkTypeByID: &models.EOLinkType{ID: linkTypeID, Key: "covers_item", Label: "Covers"},
	}
	h := NewEORecordTypeHandler(types, &eoFakeRecordRepo{})

	app := fiber.New()
	app.Use(eoTestActor(&models.User{ID: uuid.New()}))
	h.RegisterRoutes(app.Group("/eo-record-types"), eoPermFor(granted...))
	return app, typeID, linkTypeID
}

// TestEOTypeReadRoutesAcceptEoView: the UI cannot render a register list, a
// type name or any form without the type catalogue — the forms are GENERATED
// from each type's field definitions. A pilot user holding eo:view and not
// eo:types:manage must therefore reach the three read routes.
func TestEOTypeReadRoutesAcceptEoView(t *testing.T) {
	app, typeID, _ := eoTypeTestApp("eo:view")

	cases := []struct {
		name   string
		method string
		path   string
	}{
		{"List", "GET", "/eo-record-types"},
		{"Get", "GET", "/eo-record-types/" + typeID.String()},
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
	viewer, viewerTypeID, viewerLinkID := eoTypeTestApp("eo:view")
	manager, managerTypeID, managerLinkID := eoTypeTestApp("eo:types:manage")

	// path is built from the ids of whichever app is being called, so the
	// control request is the same ROUTE, not a request that 404s instead.
	cases := []struct {
		name   string
		method string
		path   func(typeID, linkID uuid.UUID) string
	}{
		{"Create", "POST", func(uuid.UUID, uuid.UUID) string { return "/eo-record-types" }},
		{"Update", "PUT", func(t, _ uuid.UUID) string { return "/eo-record-types/" + t.String() }},
		{"Delete", "DELETE", func(t, _ uuid.UUID) string { return "/eo-record-types/" + t.String() }},
		{"CreateLinkType", "POST", func(uuid.UUID, uuid.UUID) string { return "/eo-record-types/link-types" }},
		{"UpdateLinkType", "PUT", func(_, l uuid.UUID) string { return "/eo-record-types/link-types/" + l.String() }},
		{"DeleteLinkType", "DELETE", func(_, l uuid.UUID) string { return "/eo-record-types/link-types/" + l.String() }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			denied := tc.path(viewerTypeID, viewerLinkID)
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
			allowed := tc.path(managerTypeID, managerLinkID)
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
