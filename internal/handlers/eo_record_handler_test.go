package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/internal/services"
	"github.com/automax/backend/pkg/constants"
	"github.com/automax/backend/pkg/validation"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// ── fakes ───────────────────────────────────────────────────────────────────
//
// Every fake EMBEDS the interface and overrides only the methods the test
// under it calls. A hand-written method set would quietly become more capable
// than the real repository, which is how a guard in this plan became dead code.

type eoFakeRecordService struct {
	services.EORecordService
	created     *models.EORecord
	createErr   error
	updated     *models.EORecord
	updateErr   error
	createCalls int
	updateCalls int
}

func (f *eoFakeRecordService) Create(_ context.Context, _ *models.EORecordCreateRequest, _ *models.User) (*models.EORecord, error) {
	f.createCalls++
	return f.created, f.createErr
}

func (f *eoFakeRecordService) Update(_ context.Context, _ uuid.UUID, _ *models.EORecordUpdateRequest, _ *models.User) (*models.EORecord, error) {
	f.updateCalls++
	return f.updated, f.updateErr
}

type eoFakeRecordRepo struct {
	repository.EORecordRepository
	listFilter *models.EORecordFilter
	listOut    []models.EORecord
	listTotal  int64
	listErr    error
}

func (f *eoFakeRecordRepo) List(_ context.Context, filter *models.EORecordFilter) ([]models.EORecord, int64, error) {
	copied := *filter
	f.listFilter = &copied
	return f.listOut, f.listTotal, f.listErr
}

type eoFakeTypeRepo struct {
	repository.EORecordTypeRepository
	byID        *models.EORecordType
	fieldDefs   []models.EOFieldDef
	all         []models.EORecordType
	linkTypes   []models.EOLinkType
	updateCalls int
}

func (f *eoFakeTypeRepo) GetByID(_ context.Context, _ uuid.UUID) (*models.EORecordType, error) {
	return f.byID, nil
}

func (f *eoFakeTypeRepo) FieldDefs(_ context.Context, _ []uuid.UUID) ([]models.EOFieldDef, error) {
	return f.fieldDefs, nil
}

func (f *eoFakeTypeRepo) List(_ context.Context) ([]models.EORecordType, error) {
	return f.all, nil
}

func (f *eoFakeTypeRepo) ListLinkTypes(_ context.Context) ([]models.EOLinkType, error) {
	return f.linkTypes, nil
}

func (f *eoFakeTypeRepo) Update(_ context.Context, _ *models.EORecordType) error {
	f.updateCalls++
	return nil
}

// eoTestActor installs an authenticated user, the way the auth middleware does.
func eoTestActor(user *models.User) fiber.Handler {
	return func(c *fiber.Ctx) error {
		c.Locals(constants.ContextKeys.User, user)
		return c.Next()
	}
}

// eoNoPerm stands in for authMiddleware.RequirePermission in a test app: the
// route SET is what is under test, not the permission check.
func eoNoPerm(_ ...string) fiber.Handler {
	return func(c *fiber.Ctx) error { return c.Next() }
}

func eoTestUser() *models.User {
	return &models.User{ID: uuid.New(), IsSuperAdmin: true}
}

// eoInitValidator installs the process-wide validator the list path runs the
// filter through. main.go calls it at startup; a test app does not.
func eoInitValidator(t *testing.T) {
	t.Helper()
	validation.InitValidatorRegistry()
}

// ── tests ───────────────────────────────────────────────────────────────────

func TestCreateEORecordReturns201WithEnvelope(t *testing.T) {
	rec := &models.EORecord{ID: uuid.New(), RecordTypeID: uuid.New(), Reference: "EO-DIR-2026-001"}
	svc := &eoFakeRecordService{created: rec}
	h := NewEORecordHandler(svc, nil, &eoFakeRecordRepo{}, &eoFakeTypeRepo{})

	app := fiber.New()
	app.Use(eoTestActor(eoTestUser()))
	app.Post("/eo-records", h.Create)

	body, _ := json.Marshal(models.EORecordCreateRequest{RecordTypeID: rec.RecordTypeID})
	req := httptest.NewRequest("POST", "/eo-records", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != fiber.StatusCreated {
		t.Errorf("status = %d, want %d", resp.StatusCode, fiber.StatusCreated)
	}
	var out struct {
		Success bool `json:"success"`
		Data    struct {
			Reference string `json:"reference"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Success {
		t.Errorf("success = %v, want true", out.Success)
	}
	if out.Data.Reference != rec.Reference {
		t.Errorf("data.reference = %q, want %q", out.Data.Reference, rec.Reference)
	}
}

func TestCreateEORecordValidationErrorShape(t *testing.T) {
	svc := &eoFakeRecordService{createErr: &services.EOValidationError{
		Fields: map[string]string{"subject_ar": "is required"},
	}}
	h := NewEORecordHandler(svc, nil, &eoFakeRecordRepo{}, &eoFakeTypeRepo{})

	app := fiber.New()
	app.Use(eoTestActor(eoTestUser()))
	app.Post("/eo-records", h.Create)

	body, _ := json.Marshal(models.EORecordCreateRequest{RecordTypeID: uuid.New()})
	req := httptest.NewRequest("POST", "/eo-records", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, fiber.StatusBadRequest)
	}
	var out struct {
		Success bool              `json:"success"`
		Error   string            `json:"error"`
		Errors  map[string]string `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Success {
		t.Errorf("success = %v, want false", out.Success)
	}
	if out.Error != "" {
		t.Errorf("error = %q, want the map shape and no error string", out.Error)
	}
	if got := out.Errors["subject_ar"]; got != "is required" {
		t.Errorf("errors[subject_ar] = %q, want %q", got, "is required")
	}
}

func TestListEORecordsClampsLimit(t *testing.T) {
	cases := []struct {
		name      string
		query     string
		wantLimit int
		wantPage  int
	}{
		{"limit=0 is clamped to the default", "?limit=0", 20, 1},
		{"no limit at all", "", 20, 1},
		{"page=0 is clamped to 1", "?page=0", 20, 1},

		{"a valid limit is kept", "?limit=5&page=3", 5, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eoInitValidator(t)
			repo := &eoFakeRecordRepo{listOut: []models.EORecord{}, listTotal: 0}
			h := NewEORecordHandler(&eoFakeRecordService{}, nil, repo, &eoFakeTypeRepo{})

			app := fiber.New()
			app.Use(eoTestActor(eoTestUser()))
			app.Get("/eo-records", h.List)

			resp, err := app.Test(httptest.NewRequest("GET", "/eo-records"+tc.query, nil))
			if err != nil {
				t.Fatalf("app.Test: %v", err)
			}
			if resp.StatusCode != fiber.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}
			var out struct {
				Limit      int   `json:"limit"`
				Page       int   `json:"page"`
				TotalPages int   `json:"total_pages"`
				TotalItems int64 `json:"total_items"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if out.Limit != tc.wantLimit {
				t.Errorf("limit = %d, want %d", out.Limit, tc.wantLimit)
			}
			if out.Page != tc.wantPage {
				t.Errorf("page = %d, want %d", out.Page, tc.wantPage)
			}
			if repo.listFilter == nil {
				t.Fatal("repository List was never called")
			}
			if repo.listFilter.Limit < 1 {
				t.Errorf("filter.Limit = %d, want >= 1", repo.listFilter.Limit)
			}
		})
	}

	// Above the declared maximum is the caller's mistake, not something to
	// clamp silently: EORecordFilter declares max=100 and the field-error map
	// says which key was wrong. The register is never queried.
	t.Run("limit above the maximum is rejected before the register is read", func(t *testing.T) {
		eoInitValidator(t)
		repo := &eoFakeRecordRepo{}
		h := NewEORecordHandler(&eoFakeRecordService{}, nil, repo, &eoFakeTypeRepo{})

		app := fiber.New()
		app.Use(eoTestActor(eoTestUser()))
		app.Get("/eo-records", h.List)

		resp, err := app.Test(httptest.NewRequest("GET", "/eo-records?limit=5000", nil))
		if err != nil {
			t.Fatalf("app.Test: %v", err)
		}
		if resp.StatusCode != fiber.StatusBadRequest {
			t.Errorf("status = %d, want 400", resp.StatusCode)
		}
		if repo.listFilter != nil {
			t.Errorf("repository List was called with %+v, want no call", repo.listFilter)
		}
	})
}

func TestEORecordRoutesOrderStaticBeforeParam(t *testing.T) {
	eoInitValidator(t)
	repo := &eoFakeRecordRepo{listOut: []models.EORecord{}, listTotal: 0}
	h := NewEORecordHandler(&eoFakeRecordService{}, nil, repo, &eoFakeTypeRepo{})

	app := fiber.New()
	app.Use(eoTestActor(eoTestUser()))
	// The REAL route set, in the order main.go registers it.
	h.RegisterRoutes(app.Group("/eo-records"), eoNoPerm)

	resp, err := app.Test(httptest.NewRequest("GET", "/eo-records/my-assigned", nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	// /:id would reject "my-assigned" as an id with 400; the literal route
	// answers with the paginated envelope.
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("GET /eo-records/my-assigned status = %d, want 200: the /:id route matched first", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := out["total_items"]; !ok {
		t.Errorf("body = %v, want the paginated envelope from the my-assigned handler", out)
	}
	if repo.listFilter == nil {
		t.Fatal("the my-assigned handler never listed: the /:id route matched first")
	}
	if len(repo.listFilter.AssignedUser) != 1 {
		t.Errorf("filter.AssignedUser = %v, want the actor's id", repo.listFilter.AssignedUser)
	}
}

// The designer has the same hazard one level down: /eo-record-types/link-types
// is a literal that /:id would swallow, and the by-id handler would answer 400
// for an unparseable id rather than listing the link types.
func TestEORecordTypeRoutesOrderStaticBeforeParam(t *testing.T) {
	types := &eoFakeTypeRepo{}
	h := NewEORecordTypeHandler(types, &eoFakeRecordRepo{})

	app := fiber.New()
	app.Use(eoTestActor(eoTestUser()))
	h.RegisterRoutes(app.Group("/eo-record-types"), eoNoPerm)

	resp, err := app.Test(httptest.NewRequest("GET", "/eo-record-types/link-types", nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("GET /eo-record-types/link-types status = %d, want 200: the /:id route matched first", resp.StatusCode)
	}
	var out struct {
		Success bool                `json:"success"`
		Data    []models.EOLinkType `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Success {
		t.Errorf("success = %v, want true", out.Success)
	}
}

func TestUpdateEORecordTypeRejectsDestructiveEdit(t *testing.T) {
	typeID := uuid.New()
	before := &models.EORecordType{
		ID:               typeID,
		Key:              "eo_directive",
		Name:             "Directive",
		ReferencePrefix:  "EO-DIR",
		ReferencePattern: "EO-DIR-YYYY-NNN",
	}
	cases := []struct {
		name        string
		body        map[string]any
		hasRecords  int64
		wantStatus  int
		wantUpdates int
	}{
		{
			name: "changing the reference prefix while records exist",
			body: map[string]any{
				"key": "eo_directive", "name": "Directive",
				"reference_prefix": "EO-NEW", "reference_pattern": "EO-DIR-YYYY-NNN",
			},
			hasRecords: 1, wantStatus: fiber.StatusBadRequest, wantUpdates: 0,
		},
		{
			name: "changing the reference pattern while records exist",
			body: map[string]any{
				"key": "eo_directive", "name": "Directive",
				"reference_prefix": "EO-DIR", "reference_pattern": "EO-DIR-YYYY-NNNN",
			},
			hasRecords: 1, wantStatus: fiber.StatusBadRequest, wantUpdates: 0,
		},
		{
			name: "a non-destructive rename is accepted",
			body: map[string]any{
				"key": "eo_directive", "name": "Directive (revised)",
				"reference_prefix": "EO-DIR", "reference_pattern": "EO-DIR-YYYY-NNN",
			},
			hasRecords: 1, wantStatus: fiber.StatusOK, wantUpdates: 1,
		},
		{
			name: "the same prefix change with no records is allowed",
			body: map[string]any{
				"key": "eo_directive", "name": "Directive",
				"reference_prefix": "EO-NEW", "reference_pattern": "EO-DIR-YYYY-NNN",
			},
			hasRecords: 0, wantStatus: fiber.StatusOK, wantUpdates: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			copied := *before
			types := &eoFakeTypeRepo{byID: &copied, all: []models.EORecordType{*before}}
			records := &eoFakeRecordRepo{listTotal: tc.hasRecords}
			h := NewEORecordTypeHandler(types, records)

			app := fiber.New()
			app.Use(eoTestActor(eoTestUser()))
			app.Put("/eo-record-types/:id", h.Update)

			raw, _ := json.Marshal(tc.body)
			req := httptest.NewRequest("PUT", "/eo-record-types/"+typeID.String(), bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			resp, err := app.Test(req)
			if err != nil {
				t.Fatalf("app.Test: %v", err)
			}
			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if types.updateCalls != tc.wantUpdates {
				t.Errorf("repository Update calls = %d, want %d", types.updateCalls, tc.wantUpdates)
			}
		})
	}
}

// TestEOWiringUsesSLAConstructors reads cmd/server/main.go as TEXT. It is
// deliberately crude: no unit test in this package notices when main.go is
// rewired to a constructor that compiles, runs and silently does nothing —
// NewEORecordService and NewEOTransitionService both delegate with a nil
// policy repository, so the SLA clocks never start and nothing fails.
func TestEOWiringUsesSLAConstructors(t *testing.T) {
	const mainPath = "../../cmd/server/main.go"
	raw, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("read %s: %v", mainPath, err)
	}
	src := string(raw)

	for _, want := range []string{
		"services.RegisterEOCapabilities()",
		"repository.NewEOEscalationEventRepository(",
		"services.NewEORecordServiceWithSLA(",
		"services.NewEOActionExecutor(",
		"services.NewEOTransitionServiceWithSLA(",
		"services.NewEOEscalationHandler(",
		"services.NewEOTimerRouter(",
		"services.NewEOTimerMonitor(",
		"eoTimerMonitor.Start(",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("cmd/server/main.go does not contain %q: the EO engine is constructed but unreachable", want)
		}
	}

	for _, banned := range []string{
		"services.NewEORecordService(",
		"services.NewEOTransitionService(",
	} {
		if strings.Contains(src, banned) {
			t.Errorf("cmd/server/main.go contains %q: that constructor passes a nil policy repository, so SLA clocks never start", banned)
		}
	}
}
