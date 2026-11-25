package handlers

import (
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/automax/backend/internal/models"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// eoGateRecorder notes, for ONE request, which guards actually ran and in
// which order. The failure this is here to catch is a gate that is written,
// registered and tested in its own package while never being reached on the
// route that matters.
type eoGateRecorder struct {
	module []string
	action string
	order  []string
}

func (r *eoGateRecorder) perm(permissions ...string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		r.module = append(r.module, permissions...)
		r.order = append(r.order, "module")
		return c.Next()
	}
}

func (r *eoGateRecorder) typePerm(action string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		r.action = action
		r.order = append(r.order, "type")
		return c.Next()
	}
}

// TestEORecordRoutesCarryThePerTypeGate registers the REAL route set and
// drives every route in it.
//
// Spec §7.11's per-type permissions are a control only if they are applied
// where a record is reached. A middleware that exists and is correct, but is
// registered on four routes out of six, is the failure this programme keeps
// shipping — and no test inside internal/middleware can see it.
//
// The three list routes are deliberately NOT gated here: a register answers
// with the records the caller may see, not with 403, so they are filtered in
// the repository instead (applyEORecordTypePermissions). That is asserted as
// an EMPTY action, so adding a gate to a list route — which would hand the
// whole page to a 403 — fails this test too.
func TestEORecordRoutesCarryThePerTypeGate(t *testing.T) {
	eoInitValidator(t)
	id := uuid.New().String()

	cases := []struct {
		name       string
		method     string
		path       string
		wantModule []string
		wantAction string
	}{
		{"Create", "POST", "/eo-records", []string{"eo:create"}, models.EOTypePermissionCreate},
		{"List", "GET", "/eo-records", []string{"eo:view"}, ""},
		{"MyAssigned", "GET", "/eo-records/my-assigned", []string{"eo:view"}, ""},
		{"Search", "POST", "/eo-records/search", []string{"eo:view"}, ""},
		{"Get", "GET", "/eo-records/" + id, []string{"eo:view"}, models.EOTypePermissionView},
		{"Update", "PUT", "/eo-records/" + id, []string{"eo:update"}, models.EOTypePermissionUpdate},
		{"AvailableTransitions", "GET", "/eo-records/" + id + "/transitions", []string{"eo:view"}, models.EOTypePermissionView},
		{"Transition", "POST", "/eo-records/" + id + "/transition", []string{"eo:transition"}, models.EOTypePermissionUpdate},
		{"Children", "GET", "/eo-records/" + id + "/children", []string{"eo:view"}, models.EOTypePermissionView},
		{"History", "GET", "/eo-records/" + id + "/history", []string{"eo:view"}, models.EOTypePermissionView},
		{"CreateLink", "POST", "/eo-records/" + id + "/links", []string{"eo:update"}, models.EOTypePermissionUpdate},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &eoGateRecorder{}
			h := NewEORecordHandler(&eoFakeRecordService{}, nil, &eoFakeRecordRepo{listOut: []models.EORecord{}}, &eoFakeTypeRepo{})
			app := fiber.New()
			app.Use(eoTestActor(eoTestUser()))
			h.RegisterRoutes(app.Group("/eo-records"), rec.perm, rec.typePerm)

			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			if _, err := app.Test(req); err != nil {
				t.Fatalf("app.Test: %v", err)
			}

			if !reflect.DeepEqual(rec.module, tc.wantModule) {
				t.Errorf("module-level codes = %v, want %v: the per-type gate NARROWS the module code, it does not replace it", rec.module, tc.wantModule)
			}
			if rec.action != tc.wantAction {
				if tc.wantAction == "" {
					t.Errorf("%s %s ran the per-type gate for %q; a register must be FILTERED, not refused", tc.method, tc.path, rec.action)
				} else {
					t.Errorf("%s %s per-type action = %q, want %q", tc.method, tc.path, rec.action, tc.wantAction)
				}
			}
			if tc.wantAction != "" {
				// Order is load-bearing: the per-type gate reads the user the
				// module-level guard loads into Locals, and refuses when it
				// is not there. Registered first, it would refuse everyone.
				if want := []string{"module", "type"}; !reflect.DeepEqual(rec.order, want) {
					t.Errorf("guard order = %v, want %v", rec.order, want)
				}
			}
		})
	}
}

// TestEOScopeFilterCarriesThePermissionCodes pins the other end of the
// register's half: the repository clause is fed from the filter, and a handler
// that never fills that field would leave the whole mechanism inert while
// every route-level assertion above still passed.
func TestEOScopeFilterCarriesThePermissionCodes(t *testing.T) {
	user := &models.User{ID: uuid.New(), Roles: []models.Role{{Permissions: []models.Permission{
		{Code: "eo:view", IsActive: true},
		{Code: models.EOTypePermissionCode("eo_directive", models.EOTypePermissionView), IsActive: true},
	}}}}

	var filter models.EORecordFilter
	eoScopeFilter(&filter, user)
	if want := []string{"eo:view", "eo:eo_directive:view"}; !reflect.DeepEqual(filter.PermissionCodes, want) {
		t.Fatalf("filter.PermissionCodes = %v, want %v", filter.PermissionCodes, want)
	}
	if keys := models.EOTypeKeysFromPermissionCodes(filter.PermissionCodes, models.EOTypePermissionView); !reflect.DeepEqual(keys, []string{"eo_directive"}) {
		t.Errorf("the register would restrict to %v, want [eo_directive]", keys)
	}

	// A caller's own codes never arrive from the request. Anything the client
	// sent in that field is overwritten, like the other server-filled fields.
	spoofed := models.EORecordFilter{PermissionCodes: []string{models.EOTypePermissionCode("eo_decision_record", models.EOTypePermissionView)}}
	eoScopeFilter(&spoofed, user)
	for _, code := range spoofed.PermissionCodes {
		if strings.Contains(code, "eo_decision_record") {
			t.Errorf("a caller-supplied permission code survived into the filter: %v", spoofed.PermissionCodes)
		}
	}
}

// TestEORecordListRoutesAreFilteredForANonAdmin: the three register routes are
// the ones with no 403 to give, so each must reach the repository carrying the
// caller's codes. A route that listed without them would be refused everything
// by the repository — or, had the clause been written to fail open, would
// serve everything.
func TestEORecordListRoutesAreFilteredForANonAdmin(t *testing.T) {
	eoInitValidator(t)
	code := models.EOTypePermissionCode("eo_directive", models.EOTypePermissionView)
	user := &models.User{ID: uuid.New(), Roles: []models.Role{{Permissions: []models.Permission{
		{Code: "eo:view", IsActive: true},
		{Code: code, IsActive: true},
	}}}}

	for _, tc := range []struct{ name, method, path string }{
		{"List", "GET", "/eo-records"},
		{"MyAssigned", "GET", "/eo-records/my-assigned"},
		{"Search", "POST", "/eo-records/search"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &eoFakeRecordRepo{listOut: []models.EORecord{}}
			h := NewEORecordHandler(&eoFakeRecordService{}, nil, repo, &eoFakeTypeRepo{})
			app := fiber.New()
			app.Use(eoTestActor(user))
			h.RegisterRoutes(app.Group("/eo-records"), eoNoPerm, eoNoTypePerm)

			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			if _, err := app.Test(req); err != nil {
				t.Fatalf("app.Test: %v", err)
			}
			if repo.listFilter == nil {
				t.Fatalf("%s %s never reached the register", tc.method, tc.path)
			}
			if repo.listFilter.IsAdmin {
				t.Fatal("a plain user must not be listed as an admin")
			}
			keys := models.EOTypeKeysFromPermissionCodes(repo.listFilter.PermissionCodes, models.EOTypePermissionView)
			if !reflect.DeepEqual(keys, []string{"eo_directive"}) {
				t.Errorf("the register was queried for types %v, want [eo_directive] — filter codes were %v", keys, repo.listFilter.PermissionCodes)
			}
		})
	}
}
