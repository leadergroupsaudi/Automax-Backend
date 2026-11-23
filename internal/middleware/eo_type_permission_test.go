package middleware

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/pkg/constants"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// ── fixtures ────────────────────────────────────────────────────────────────

type eoGateRecords struct {
	repository.EORecordRepository
	byID map[uuid.UUID]*models.EORecord
	err  error
}

func (f *eoGateRecords) GetAnyByID(_ context.Context, id uuid.UUID) (*models.EORecord, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byID[id], nil
}

type eoGateTypes struct {
	repository.EORecordTypeRepository
	byID map[uuid.UUID]*models.EORecordType
	err  error
}

func (f *eoGateTypes) GetByID(_ context.Context, id uuid.UUID) (*models.EORecordType, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byID[id], nil
}

// eoGateUser is a plain, non-super-admin caller holding exactly `codes`.
// The real middleware short-circuits on IsSuperAdmin, so a test driven by a
// super admin proves nothing about the gate (addendum §5).
func eoGateUser(codes ...string) *models.User {
	perms := make([]models.Permission, 0, len(codes))
	for _, c := range codes {
		perms = append(perms, models.Permission{Code: c, IsActive: true})
	}
	return &models.User{ID: uuid.New(), Roles: []models.Role{{IsActive: true, Permissions: perms}}}
}

// eoGateFixture builds the app the way the server does: the module-level
// permission middleware runs FIRST and leaves the loaded user in Locals, then
// the per-type gate, then the handler. It returns the app and the ids of a
// Directive and a Decision Record.
type eoGateFixture struct {
	app        *fiber.App
	directive  uuid.UUID // a record of type eo_directive
	decision   uuid.UUID // a record of type eo_decision_record
	directiveT uuid.UUID // the eo_directive type id
	decisionT  uuid.UUID // the eo_decision_record type id
	reached    bool      // did the handler run
}

func newEOGateFixture(t *testing.T, user *models.User, records *eoGateRecords, types *eoGateTypes) *eoGateFixture {
	t.Helper()
	f := &eoGateFixture{}
	f.directiveT, f.decisionT = uuid.New(), uuid.New()
	f.directive, f.decision = uuid.New(), uuid.New()

	if records == nil {
		records = &eoGateRecords{}
	}
	if records.byID == nil {
		records.byID = map[uuid.UUID]*models.EORecord{
			f.directive: {ID: f.directive, RecordTypeID: f.directiveT},
			f.decision:  {ID: f.decision, RecordTypeID: f.decisionT},
		}
	}
	if types == nil {
		types = &eoGateTypes{}
	}
	if types.byID == nil {
		types.byID = map[uuid.UUID]*models.EORecordType{
			f.directiveT: {ID: f.directiveT, Key: "eo_directive"},
			f.decisionT:  {ID: f.decisionT, Key: "eo_decision_record"},
		}
	}

	gate := NewEOTypePermission(records, types)
	app := fiber.New()
	// Stands in for RequirePermission: it leaves the loaded user in Locals,
	// which is the only thing the gate reads about who is calling.
	app.Use(func(c *fiber.Ctx) error {
		if user != nil {
			c.Locals(constants.ContextKeys.User, user)
		}
		return c.Next()
	})
	reached := func(c *fiber.Ctx) error {
		f.reached = true
		return c.SendStatus(fiber.StatusOK)
	}
	app.Get("/eo-records/:id", gate.RequireEOTypePermission(models.EOTypePermissionView), reached)
	app.Put("/eo-records/:id", gate.RequireEOTypePermission(models.EOTypePermissionUpdate), reached)
	app.Post("/eo-records", gate.RequireEOTypePermission(models.EOTypePermissionCreate), reached)
	f.app = app
	return f
}

func (f *eoGateFixture) do(t *testing.T, method, path, body string) int {
	t.Helper()
	f.reached = false
	var req = httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := f.app.Test(req)
	if err != nil {
		t.Fatalf("app.Test %s %s: %v", method, path, err)
	}
	return resp.StatusCode
}

// ── the gate ────────────────────────────────────────────────────────────────

// TestRequireEOTypePermissionRefusesAForeignType is the control spec §7.11
// names and the branch has never had: a user granted Directives must not read
// a Decision Record.
func TestRequireEOTypePermissionRefusesAForeignType(t *testing.T) {
	user := eoGateUser("eo:view", "eo:update", models.EOTypePermissionCode("eo_directive", models.EOTypePermissionView))
	f := newEOGateFixture(t, user, nil, nil)

	if got := f.do(t, "GET", "/eo-records/"+f.decision.String(), ""); got != fiber.StatusForbidden {
		t.Errorf("GET a Decision Record as a Directive holder = %d, want 403", got)
	}
	if f.reached {
		t.Error("the handler ran: the gate let a foreign type through")
	}
	// 403, never 401 — the frontend's axios interceptor logs the user out on
	// a 401, so a permission refusal that returns one ends the session.
	if got := f.do(t, "GET", "/eo-records/"+f.decision.String(), ""); got == fiber.StatusUnauthorized {
		t.Error("the gate answered 401; a permission denial must be 403")
	}
}

// TestRequireEOTypePermissionAllowsTheGrantedType is the other half: the gate
// must not simply refuse everybody, which is how a mis-spelled code behaves.
func TestRequireEOTypePermissionAllowsTheGrantedType(t *testing.T) {
	user := eoGateUser(models.EOTypePermissionCode("eo_directive", models.EOTypePermissionView))
	f := newEOGateFixture(t, user, nil, nil)

	if got := f.do(t, "GET", "/eo-records/"+f.directive.String(), ""); got != fiber.StatusOK {
		t.Fatalf("GET a Directive as a Directive holder = %d, want 200", got)
	}
	if !f.reached {
		t.Error("the handler did not run for the granted type")
	}
}

// TestRequireEOTypePermissionIsPerAction: view does not grant update, and the
// gate on a write route asks for update.
func TestRequireEOTypePermissionIsPerAction(t *testing.T) {
	viewer := eoGateUser(models.EOTypePermissionCode("eo_directive", models.EOTypePermissionView))
	f := newEOGateFixture(t, viewer, nil, nil)
	if got := f.do(t, "PUT", "/eo-records/"+f.directive.String(), "{}"); got != fiber.StatusForbidden {
		t.Errorf("PUT as a view-only holder = %d, want 403", got)
	}

	writer := eoGateUser(models.EOTypePermissionCode("eo_directive", models.EOTypePermissionUpdate))
	g := newEOGateFixture(t, writer, nil, nil)
	if got := g.do(t, "PUT", "/eo-records/"+g.directive.String(), "{}"); got != fiber.StatusOK {
		t.Errorf("PUT as an update holder = %d, want 200", got)
	}
}

// TestRequireEOTypePermissionReadsTheCreateBody: POST /eo-records carries no
// record id, so the type comes from the body's record_type_id. Without this
// the create route is gated on nothing at all.
func TestRequireEOTypePermissionReadsTheCreateBody(t *testing.T) {
	user := eoGateUser(models.EOTypePermissionCode("eo_directive", models.EOTypePermissionCreate))
	f := newEOGateFixture(t, user, nil, nil)

	if got := f.do(t, "POST", "/eo-records", `{"record_type_id":"`+f.decision.String()+`"}`); got != fiber.StatusForbidden {
		// the id here is not even a type id; an unresolvable type is a refusal
		t.Errorf("POST naming an unknown type = %d, want 403", got)
	}
	if got := f.do(t, "POST", "/eo-records", `{"record_type_id":"`+f.decisionT.String()+`"}`); got != fiber.StatusForbidden {
		t.Errorf("POST naming a foreign type = %d, want 403", got)
	}
	if f.reached {
		t.Error("the handler ran for a type the caller may not create")
	}
	if got := f.do(t, "POST", "/eo-records", `{"record_type_id":"`+f.directiveT.String()+`"}`); got != fiber.StatusOK {
		t.Errorf("POST naming the granted type = %d, want 200", got)
	}
}

// TestEOTypePermissionSuperAdminBypasses, as everywhere else in this codebase.
func TestEOTypePermissionSuperAdminBypasses(t *testing.T) {
	admin := &models.User{ID: uuid.New(), IsSuperAdmin: true}
	f := newEOGateFixture(t, admin, nil, nil)

	if got := f.do(t, "GET", "/eo-records/"+f.decision.String(), ""); got != fiber.StatusOK {
		t.Errorf("GET as super admin = %d, want 200", got)
	}
	if got := f.do(t, "POST", "/eo-records", `{"record_type_id":"`+f.decisionT.String()+`"}`); got != fiber.StatusOK {
		t.Errorf("POST as super admin = %d, want 200", got)
	}
}

// TestEOTypePermissionFailsClosed covers the three ways the gate can be asked
// a question it cannot answer. Each must refuse, and none may answer 401.
func TestEOTypePermissionFailsClosed(t *testing.T) {
	t.Run("no authenticated user in Locals", func(t *testing.T) {
		f := newEOGateFixture(t, nil, nil, nil)
		if got := f.do(t, "GET", "/eo-records/"+f.directive.String(), ""); got != fiber.StatusForbidden {
			t.Errorf("status = %d, want 403: the gate must not run open when it cannot see who is calling", got)
		}
		if f.reached {
			t.Error("the handler ran with no user")
		}
	})

	t.Run("the record's type is not in the catalogue", func(t *testing.T) {
		user := eoGateUser(models.EOTypePermissionCode("eo_directive", models.EOTypePermissionView))
		f := newEOGateFixture(t, user, nil, &eoGateTypes{byID: map[uuid.UUID]*models.EORecordType{}})
		if got := f.do(t, "GET", "/eo-records/"+f.directive.String(), ""); got != fiber.StatusForbidden {
			t.Errorf("status = %d, want 403 for a type that cannot be resolved", got)
		}
	})

	t.Run("the register is down", func(t *testing.T) {
		user := eoGateUser(models.EOTypePermissionCode("eo_directive", models.EOTypePermissionView))
		f := newEOGateFixture(t, user, &eoGateRecords{err: errors.New("boom")}, nil)
		got := f.do(t, "GET", "/eo-records/"+f.directive.String(), "")
		if got != fiber.StatusInternalServerError {
			t.Errorf("status = %d, want 500: a failed lookup is an outage, not an answer", got)
		}
		if f.reached {
			t.Error("the handler ran after the lookup failed")
		}
	})
}

// TestEOTypePermissionPassesThroughWhatTheHandlerAnswers: the gate must not
// turn a missing record or an unparseable id into its own answer, or the
// endpoint's 404/400 contract changes shape.
func TestEOTypePermissionPassesThroughWhatTheHandlerAnswers(t *testing.T) {
	user := eoGateUser(models.EOTypePermissionCode("eo_directive", models.EOTypePermissionView))
	f := newEOGateFixture(t, user, nil, nil)

	if got := f.do(t, "GET", "/eo-records/"+uuid.New().String(), ""); got != fiber.StatusNotFound {
		t.Errorf("GET an unknown id = %d, want 404", got)
	}
	if f.reached {
		t.Error("the handler ran for a record that does not exist")
	}
	// An unparseable id is the HANDLER's 400 (invalid_id); the gate has no
	// type to check and must not mask it.
	if got := f.do(t, "GET", "/eo-records/not-a-uuid", ""); got != fiber.StatusOK {
		t.Errorf("GET an unparseable id reached the handler with %d; the gate must leave the 400 to it", got)
	}
	// A create body with no record_type_id is likewise the handler's 400.
	if got := f.do(t, "POST", "/eo-records", `{}`); got != fiber.StatusOK {
		t.Errorf("POST with no record_type_id = %d; the gate must leave the field error to the handler", got)
	}
}
