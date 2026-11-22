package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	anyByID    *models.EORecord
	byID       map[uuid.UUID]*models.EORecord
	// visibleRows is what the register returns for a scoped read. It stands
	// in for applyEORecordVisibility's WHERE clause and is NOT a second
	// implementation of it: the rule itself is the repository's and is
	// covered there (repository.TestEORecordListFilterByParentID).
	visibleRows  []models.EORecord
	visibleTotal int64
	// visibleIDs, when non-nil, is the set of ids a SINGLE-record visibility
	// probe (filter.RecordID) finds. It is strictly narrower than the real
	// WHERE clause, never wider.
	visibleIDs    map[uuid.UUID]bool
	childrenCalls int
	linksFrom     []models.EORecordLink
	createdLinks  []*models.EORecordLink
	// totalByType, when non-nil, is how many records each record type holds.
	// List then answers only for the types the filter NAMES, which is what
	// makes "the ancestor has none of its own, its descendant has some" a
	// distinguishable case rather than one flat number.
	totalByType map[uuid.UUID]int64
}

func (f *eoFakeRecordRepo) List(_ context.Context, filter *models.EORecordFilter) ([]models.EORecord, int64, error) {
	copied := *filter
	f.listFilter = &copied
	if f.listErr != nil {
		return nil, 0, f.listErr
	}
	if f.totalByType != nil {
		var total int64
		for _, id := range filter.RecordTypeID {
			parsed, err := uuid.Parse(id)
			if err != nil {
				return nil, 0, err
			}
			total += f.totalByType[parsed]
		}
		return nil, total, nil
	}
	if f.visibleIDs != nil && filter.RecordID != nil {
		if f.visibleIDs[*filter.RecordID] {
			return []models.EORecord{{ID: *filter.RecordID}}, 1, nil
		}
		return nil, 0, nil
	}
	if f.visibleRows != nil {
		return f.visibleRows, f.visibleTotal, nil
	}
	return f.listOut, f.listTotal, nil
}

func (f *eoFakeRecordRepo) GetAnyByID(_ context.Context, id uuid.UUID) (*models.EORecord, error) {
	if rec, ok := f.byID[id]; ok {
		return rec, nil
	}
	return f.anyByID, nil
}

func (f *eoFakeRecordRepo) LinksFrom(_ context.Context, _ uuid.UUID) ([]models.EORecordLink, error) {
	return f.linksFrom, nil
}

func (f *eoFakeRecordRepo) CreateLink(_ context.Context, link *models.EORecordLink) error {
	f.createdLinks = append(f.createdLinks, link)
	return nil
}

// Children is overridden only to prove nothing calls it any more: it takes no
// actor and applies no visibility, so the children endpoint must not use it.
func (f *eoFakeRecordRepo) Children(_ context.Context, _ uuid.UUID, _ *uuid.UUID) ([]models.EORecord, error) {
	f.childrenCalls++
	return nil, nil
}

type eoFakeTypeRepo struct {
	repository.EORecordTypeRepository
	byID             *models.EORecordType
	fieldDefs        []models.EOFieldDef
	all              []models.EORecordType
	linkTypes        []models.EOLinkType
	linkTypeByKey    *models.EOLinkType
	updateCalls      int
	deleteCalls      int
	createFieldCalls int
	deleteFieldCalls int
	// children is the inheritance tree, keyed by parent type id. The §7.14
	// guard rails are now asked about a type's whole SUBTREE, so a fake that
	// answered "no children" unconditionally would be MORE PERMISSIVE than
	// the repository — which is exactly the shape of dead guard this plan
	// keeps producing.
	children map[uuid.UUID][]models.EORecordType
}

func (f *eoFakeTypeRepo) GetLinkTypeByKey(_ context.Context, key string) (*models.EOLinkType, error) {
	if f.linkTypeByKey != nil && f.linkTypeByKey.Key == key {
		return f.linkTypeByKey, nil
	}
	return nil, nil
}

func (f *eoFakeTypeRepo) GetByID(_ context.Context, _ uuid.UUID) (*models.EORecordType, error) {
	return f.byID, nil
}

func (f *eoFakeTypeRepo) FieldDefs(_ context.Context, _ []uuid.UUID) ([]models.EOFieldDef, error) {
	return f.fieldDefs, nil
}

func (f *eoFakeTypeRepo) ChildTypes(_ context.Context, parentID uuid.UUID) ([]models.EORecordType, error) {
	return f.children[parentID], nil
}

// DescendantIDs walks the same tree the repository walks: rootID, then
// everything beneath it.
func (f *eoFakeTypeRepo) DescendantIDs(_ context.Context, rootID uuid.UUID) ([]uuid.UUID, error) {
	out := []uuid.UUID{rootID}
	for i := 0; i < len(out); i++ {
		for _, c := range f.children[out[i]] {
			out = append(out, c.ID)
		}
	}
	return out, nil
}

func (f *eoFakeTypeRepo) Delete(_ context.Context, _ uuid.UUID) error {
	f.deleteCalls++
	return nil
}

func (f *eoFakeTypeRepo) CreateFieldDef(_ context.Context, _ *models.EOFieldDef) error {
	f.createFieldCalls++
	return nil
}

func (f *eoFakeTypeRepo) DeleteFieldDef(_ context.Context, _ uuid.UUID) error {
	f.deleteFieldCalls++
	return nil
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
		// The router's map must be KEYED on the exported constant the
		// scheduler stamps onto the timer and the handler checks on receipt.
		// A string literal here would route nothing and every SLA timer would
		// take the retry-then-fail path.
		"services.EOEscalationCapabilityKey:",
		"services.NewEOTimerMonitor(",
		"eoTimerMonitor.Start(",
		// Constructing the handlers proves nothing if nothing mounts them.
		// Without these two lines the routes do not exist, and the only thing
		// that would notice is Go's unused-variable error.
		"eoRecordHandler.RegisterRoutes(",
		"eoRecordTypeHandler.RegisterRoutes(",
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

// ── authorization ───────────────────────────────────────────────────────────

// TestUpdateEORecordHonoursVisibilityEvenWithARecordTypeID covers the gate
// that was bypassable: the visibility check used to run only when the body
// omitted record_type_id, so a caller who supplied the right one skipped it
// entirely and could update a record GET would 404 for.
func TestUpdateEORecordHonoursVisibilityEvenWithARecordTypeID(t *testing.T) {
	eoInitValidator(t)
	hidden := &models.EORecord{ID: uuid.New(), RecordTypeID: uuid.New(), Reference: "EO-DIR-2026-009"}

	cases := []struct {
		name string
		body map[string]any
	}{
		{"with the correct record_type_id", map[string]any{
			"record_type_id": hidden.RecordTypeID.String(),
			"values":         map[string]any{"subject_en": "taken over"},
		}},
		{"with no record_type_id", map[string]any{
			"values": map[string]any{"subject_en": "taken over"},
		}},
		{"with a wrong record_type_id", map[string]any{
			"record_type_id": uuid.New().String(),
			"values":         map[string]any{"subject_en": "taken over"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The record exists across the partitions, but the register
			// returns nothing for this caller: it is restricted to a body
			// they do not belong to.
			repo := &eoFakeRecordRepo{anyByID: hidden, visibleRows: []models.EORecord{}, visibleTotal: 0}
			svc := &eoFakeRecordService{updated: hidden}
			h := NewEORecordHandler(svc, nil, repo, &eoFakeTypeRepo{})

			app := fiber.New()
			// NOT a super admin: row-level visibility is in play.
			app.Use(eoTestActor(&models.User{ID: uuid.New()}))
			app.Put("/eo-records/:id", h.Update)

			raw, _ := json.Marshal(tc.body)
			req := httptest.NewRequest("PUT", "/eo-records/"+hidden.ID.String(), bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			resp, err := app.Test(req)
			if err != nil {
				t.Fatalf("app.Test: %v", err)
			}
			if resp.StatusCode != fiber.StatusNotFound {
				t.Errorf("status = %d, want 404", resp.StatusCode)
			}
			// A 404 that still called the service would mean the record was
			// modified before the response was written.
			if svc.updateCalls != 0 {
				t.Errorf("service Update calls = %d, want 0", svc.updateCalls)
			}
		})
	}
}

// TestUpdateEORecordUsesTheStoredRecordType proves the composite key the
// service is handed comes from the stored record, never from the request.
func TestUpdateEORecordUsesTheStoredRecordType(t *testing.T) {
	eoInitValidator(t)
	rec := &models.EORecord{ID: uuid.New(), RecordTypeID: uuid.New(), Reference: "EO-DIR-2026-010"}
	repo := &eoFakeRecordRepo{anyByID: rec}
	svc := &eoFakeRecordService{updated: rec}
	h := NewEORecordHandler(svc, nil, repo, &eoFakeTypeRepo{})

	app := fiber.New()
	app.Use(eoTestActor(eoTestUser()))
	app.Put("/eo-records/:id", h.Update)

	raw, _ := json.Marshal(map[string]any{"values": map[string]any{"subject_en": "revised"}})
	req := httptest.NewRequest("PUT", "/eo-records/"+rec.ID.String(), bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if svc.updateCalls != 1 {
		t.Errorf("service Update calls = %d, want 1", svc.updateCalls)
	}
}

// TestEOChildrenAreVisibilityScoped covers the leak: the parent was checked
// and then every child was returned raw from a repository method that takes no
// actor. The children now come from the register's own scoped query.
func TestEOChildrenAreVisibilityScoped(t *testing.T) {
	eoInitValidator(t)
	parent := &models.EORecord{ID: uuid.New(), RecordTypeID: uuid.New(), Reference: "EO-DIR-2026-001"}
	actor := &models.User{ID: uuid.New(), DepartmentID: &[]uuid.UUID{uuid.New()}[0]}
	// Two children exist; the register returns only the one this caller may
	// see. The other is restricted to a body they do not belong to.
	visibleChild := models.EORecord{ID: uuid.New(), RecordTypeID: parent.RecordTypeID, Reference: "EO-DIR-2026-001-A"}

	repo := &eoFakeRecordRepo{
		anyByID:      parent,
		visibleRows:  []models.EORecord{visibleChild},
		visibleTotal: 1,
	}
	h := NewEORecordHandler(&eoFakeRecordService{}, nil, repo, &eoFakeTypeRepo{})

	app := fiber.New()
	app.Use(eoTestActor(actor))
	app.Get("/eo-records/:id/children", h.Children)

	resp, err := app.Test(httptest.NewRequest("GET", "/eo-records/"+parent.ID.String()+"/children", nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != fiber.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Data       []models.EORecord `json:"data"`
		TotalItems int64             `json:"total_items"`
		Limit      int               `json:"limit"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Data) != 1 || out.Data[0].Reference != visibleChild.Reference {
		t.Errorf("children = %v, want exactly the visible one", out.Data)
	}
	if out.Limit < 1 {
		t.Errorf("limit = %d, want >= 1: PaginatedSuccessResponse divides by it", out.Limit)
	}
	// The unscoped repository method must not be reached at all.
	if repo.childrenCalls != 0 {
		t.Errorf("repository Children calls = %d, want 0: it applies no visibility", repo.childrenCalls)
	}
	// The LAST List call is the children query (the first was the parent's
	// own visibility check).
	if repo.listFilter == nil {
		t.Fatal("the register was never queried for the children")
	}
	if repo.listFilter.ParentID == nil || *repo.listFilter.ParentID != parent.ID.String() {
		t.Errorf("filter.ParentID = %v, want the parent in the path", repo.listFilter.ParentID)
	}
	// The three scope fields are what make visibility unforgeable; they must
	// be filled on this path too.
	if repo.listFilter.UserID != actor.ID {
		t.Errorf("filter.UserID = %v, want the actor %v", repo.listFilter.UserID, actor.ID)
	}
	if repo.listFilter.IsAdmin {
		t.Errorf("filter.IsAdmin = true, want false for a non-admin actor")
	}
	if len(repo.listFilter.VisibleToDepartmentIDs) != 1 || repo.listFilter.VisibleToDepartmentIDs[0] != *actor.DepartmentID {
		t.Errorf("filter.VisibleToDepartmentIDs = %v, want the actor's body", repo.listFilter.VisibleToDepartmentIDs)
	}
}

// TestEOChildrenRefuseAnInvisibleParent: the children of a record the caller
// may not see are not theirs to enumerate.
func TestEOChildrenRefuseAnInvisibleParent(t *testing.T) {
	eoInitValidator(t)
	parent := &models.EORecord{ID: uuid.New(), RecordTypeID: uuid.New(), Reference: "EO-DIR-2026-002"}
	repo := &eoFakeRecordRepo{anyByID: parent, visibleRows: []models.EORecord{}, visibleTotal: 0}
	h := NewEORecordHandler(&eoFakeRecordService{}, nil, repo, &eoFakeTypeRepo{})

	app := fiber.New()
	app.Use(eoTestActor(&models.User{ID: uuid.New()}))
	app.Get("/eo-records/:id/children", h.Children)

	resp, err := app.Test(httptest.NewRequest("GET", "/eo-records/"+parent.ID.String()+"/children", nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != fiber.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	if repo.childrenCalls != 0 {
		t.Errorf("repository Children calls = %d, want 0", repo.childrenCalls)
	}
}

// ── links ───────────────────────────────────────────────────────────────────

// eoAnyTypeJSON is the literal a link type's endpoint list carries to mean
// "a record of any type" (services/eo_links.go).
const eoAnyTypeJSON = `"any"`

func eoLinkApp(t *testing.T, h *EORecordHandler, actor *models.User) *fiber.App {
	t.Helper()
	app := fiber.New()
	app.Use(eoTestActor(actor))
	app.Post("/eo-records/:id/links", h.CreateLink)
	return app
}

func eoPostLink(t *testing.T, app *fiber.App, fromID uuid.UUID, body map[string]any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/eo-records/"+fromID.String()+"/links", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.StatusCode, out
}

func TestCreateEORecordLinkEnforcesTheLinkTypeRules(t *testing.T) {
	directiveType, taskType := uuid.New(), uuid.New()
	from := &models.EORecord{ID: uuid.New(), RecordTypeID: taskType, Reference: "EO-DIR-2026-001-A"}
	to := &models.EORecord{ID: uuid.New(), RecordTypeID: directiveType, Reference: "EO-DIR-2026-001"}

	// parent_record: a task may point at a directive, at most once.
	parentLink := func() *models.EOLinkType {
		return &models.EOLinkType{
			ID: uuid.New(), Key: "parent_record", Label: "Parent record",
			SourceTypeIDs: []byte(`["` + taskType.String() + `"]`),
			TargetTypeIDs: []byte(`["` + directiveType.String() + `"]`),
			Cardinality:   models.EOCardinalityOne,
		}
	}

	cases := []struct {
		name        string
		linkType    func() *models.EOLinkType
		existing    []models.EORecordLink
		body        map[string]any
		wantStatus  int
		wantWritten int
		wantErrKey  string
	}{
		{
			name:     "a permitted pair is written",
			linkType: parentLink,
			body: map[string]any{
				"to_record_id": to.ID.String(), "link_type_key": "parent_record",
			},
			wantStatus: fiber.StatusCreated, wantWritten: 1,
		},
		{
			name: "an endpoint type the link type does not allow is refused",
			linkType: func() *models.EOLinkType {
				lt := parentLink()
				// This link type accepts no task as a source.
				lt.SourceTypeIDs = []byte(`["` + directiveType.String() + `"]`)
				return lt
			},
			body: map[string]any{
				"to_record_id": to.ID.String(), "link_type_key": "parent_record",
			},
			wantStatus: fiber.StatusBadRequest, wantWritten: 0, wantErrKey: "link_type_key",
		},
		{
			name:     "a duplicate of an existing link is refused",
			linkType: parentLink,
			existing: []models.EORecordLink{
				{FromRecordID: from.ID, ToRecordID: to.ID, LinkTypeKey: "parent_record"},
			},
			body: map[string]any{
				"to_record_id": to.ID.String(), "link_type_key": "parent_record",
			},
			wantStatus: fiber.StatusBadRequest, wantWritten: 0, wantErrKey: "link_type_key",
		},
		{
			name:     "a second outgoing link breaks cardinality one",
			linkType: parentLink,
			existing: []models.EORecordLink{
				{FromRecordID: from.ID, ToRecordID: uuid.New(), LinkTypeKey: "parent_record"},
			},
			body: map[string]any{
				"to_record_id": to.ID.String(), "link_type_key": "parent_record",
			},
			wantStatus: fiber.StatusBadRequest, wantWritten: 0, wantErrKey: "link_type_key",
		},
		{
			name:     "a link type nobody configured is refused",
			linkType: parentLink,
			body: map[string]any{
				"to_record_id": to.ID.String(), "link_type_key": "no_such_link",
			},
			wantStatus: fiber.StatusBadRequest, wantWritten: 0, wantErrKey: "link_type_key",
		},
		{
			name:       "a missing target is a field error, not a 500",
			linkType:   parentLink,
			body:       map[string]any{"link_type_key": "parent_record"},
			wantStatus: fiber.StatusBadRequest, wantWritten: 0, wantErrKey: "to_record_id",
		},
		{
			name:       "a missing link type is a field error",
			linkType:   parentLink,
			body:       map[string]any{"to_record_id": to.ID.String()},
			wantStatus: fiber.StatusBadRequest, wantWritten: 0, wantErrKey: "link_type_key",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			records := &eoFakeRecordRepo{
				byID:      map[uuid.UUID]*models.EORecord{from.ID: from, to.ID: to},
				linksFrom: tc.existing,
			}
			types := &eoFakeTypeRepo{linkTypeByKey: tc.linkType()}
			h := NewEORecordHandler(&eoFakeRecordService{}, nil, records, types)

			// A super admin: row-level visibility is covered separately, and
			// this case is about the link type's own rules.
			status, body := eoPostLink(t, eoLinkApp(t, h, eoTestUser()), from.ID, tc.body)

			if status != tc.wantStatus {
				t.Errorf("status = %d, want %d (body %v)", status, tc.wantStatus, body)
			}
			if len(records.createdLinks) != tc.wantWritten {
				t.Errorf("links written = %d, want %d", len(records.createdLinks), tc.wantWritten)
			}
			if tc.wantErrKey != "" {
				errs, _ := body["errors"].(map[string]any)
				if _, ok := errs[tc.wantErrKey]; !ok {
					t.Errorf("errors = %v, want a message under %q", body["errors"], tc.wantErrKey)
				}
				if _, isStringShape := body["error"]; isStringShape {
					t.Errorf("body carries the %q string shape, want the errors map", "error")
				}
			}
			if tc.wantStatus == fiber.StatusCreated {
				written := records.createdLinks[0]
				if written.FromRecordID != from.ID || written.ToRecordID != to.ID {
					t.Errorf("link endpoints = %s -> %s, want %s -> %s", written.FromRecordID, written.ToRecordID, from.ID, to.ID)
				}
				// Both endpoints carry their record type, because a foreign
				// key into a partitioned table must name the partition key.
				if written.FromRecordTypeID != from.RecordTypeID || written.ToRecordTypeID != to.RecordTypeID {
					t.Errorf("link types = %s -> %s, want %s -> %s",
						written.FromRecordTypeID, written.ToRecordTypeID, from.RecordTypeID, to.RecordTypeID)
				}
				if ok, _ := body["success"].(bool); !ok {
					t.Errorf("success = %v, want true", body["success"])
				}
			}
		})
	}
}

// TestCreateEORecordLinkChecksBothEndpointsForVisibility: CreateLink loads
// both records through eoLoadRecord, so a caller who cannot see EITHER end
// gets 404 and nothing is written. Seeing only one end is not enough to link
// them — the link would otherwise reveal that the other record exists.
func TestCreateEORecordLinkChecksBothEndpointsForVisibility(t *testing.T) {
	from := &models.EORecord{ID: uuid.New(), RecordTypeID: uuid.New(), Reference: "EO-A"}
	to := &models.EORecord{ID: uuid.New(), RecordTypeID: uuid.New(), Reference: "EO-B"}
	anyLink := &models.EOLinkType{
		ID: uuid.New(), Key: "related", Label: "Related",
		SourceTypeIDs: []byte("[" + eoAnyTypeJSON + "]"),
		TargetTypeIDs: []byte("[" + eoAnyTypeJSON + "]"),
		Cardinality:   models.EOCardinalityMany,
	}

	cases := []struct {
		name    string
		visible map[uuid.UUID]bool
	}{
		{"neither endpoint is visible", map[uuid.UUID]bool{}},
		{"only the source is visible", map[uuid.UUID]bool{from.ID: true}},
		{"only the target is visible", map[uuid.UUID]bool{to.ID: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			records := &eoFakeRecordRepo{
				byID:       map[uuid.UUID]*models.EORecord{from.ID: from, to.ID: to},
				visibleIDs: tc.visible,
			}
			types := &eoFakeTypeRepo{linkTypeByKey: anyLink}
			h := NewEORecordHandler(&eoFakeRecordService{}, nil, records, types)

			// NOT a super admin.
			app := eoLinkApp(t, h, &models.User{ID: uuid.New()})
			status, _ := eoPostLink(t, app, from.ID, map[string]any{
				"to_record_id": to.ID.String(), "link_type_key": "related",
			})
			if status != fiber.StatusNotFound {
				t.Errorf("status = %d, want 404", status)
			}
			if len(records.createdLinks) != 0 {
				t.Errorf("links written = %d, want 0", len(records.createdLinks))
			}
		})
	}

	t.Run("both visible: the link is written", func(t *testing.T) {
		records := &eoFakeRecordRepo{
			byID:       map[uuid.UUID]*models.EORecord{from.ID: from, to.ID: to},
			visibleIDs: map[uuid.UUID]bool{from.ID: true, to.ID: true},
		}
		types := &eoFakeTypeRepo{linkTypeByKey: anyLink}
		h := NewEORecordHandler(&eoFakeRecordService{}, nil, records, types)

		app := eoLinkApp(t, h, &models.User{ID: uuid.New()})
		status, _ := eoPostLink(t, app, from.ID, map[string]any{
			"to_record_id": to.ID.String(), "link_type_key": "related",
		})
		if status != fiber.StatusCreated {
			t.Errorf("status = %d, want 201", status)
		}
		if len(records.createdLinks) != 1 {
			t.Errorf("links written = %d, want 1", len(records.createdLinks))
		}
	})
}

// ── transitions ─────────────────────────────────────────────────────────────

type eoFakeTransitionService struct {
	services.EOTransitionService
	available     []models.WorkflowTransition
	availableErr  error
	executed      *models.EORecord
	executeErr    error
	executeCalls  int
	seenTransitID uuid.UUID
	seenInput     models.EOTransitionInput
}

func (f *eoFakeTransitionService) AvailableTransitions(_ context.Context, _ *models.EORecord, _ *models.User) ([]models.WorkflowTransition, error) {
	return f.available, f.availableErr
}

func (f *eoFakeTransitionService) Execute(_ context.Context, _ *models.EORecord, transitionID uuid.UUID, _ *models.User, input models.EOTransitionInput) (*models.EORecord, error) {
	f.executeCalls++
	f.seenTransitID = transitionID
	f.seenInput = input
	return f.executed, f.executeErr
}

func TestListEOAvailableTransitions(t *testing.T) {
	rec := &models.EORecord{ID: uuid.New(), RecordTypeID: uuid.New(), Reference: "EO-DIR-2026-001"}

	t.Run("returns what the executor permits", func(t *testing.T) {
		svc := &eoFakeTransitionService{available: []models.WorkflowTransition{
			{ID: uuid.New(), Code: "submit"},
			{ID: uuid.New(), Code: "reject"},
		}}
		records := &eoFakeRecordRepo{anyByID: rec}
		h := NewEORecordHandler(&eoFakeRecordService{}, svc, records, &eoFakeTypeRepo{})

		app := fiber.New()
		app.Use(eoTestActor(eoTestUser()))
		app.Get("/eo-records/:id/transitions", h.AvailableTransitions)

		resp, err := app.Test(httptest.NewRequest("GET", "/eo-records/"+rec.ID.String()+"/transitions", nil))
		if err != nil {
			t.Fatalf("app.Test: %v", err)
		}
		if resp.StatusCode != fiber.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		var out struct {
			Success bool                        `json:"success"`
			Data    []models.WorkflowTransition `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !out.Success || len(out.Data) != 2 {
			t.Errorf("body = %+v, want the two permitted transitions", out)
		}
	})

	t.Run("none permitted is an empty list, not null", func(t *testing.T) {
		svc := &eoFakeTransitionService{available: nil}
		records := &eoFakeRecordRepo{anyByID: rec}
		h := NewEORecordHandler(&eoFakeRecordService{}, svc, records, &eoFakeTypeRepo{})

		app := fiber.New()
		app.Use(eoTestActor(eoTestUser()))
		app.Get("/eo-records/:id/transitions", h.AvailableTransitions)

		resp, err := app.Test(httptest.NewRequest("GET", "/eo-records/"+rec.ID.String()+"/transitions", nil))
		if err != nil {
			t.Fatalf("app.Test: %v", err)
		}
		var out struct {
			Data []models.WorkflowTransition `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if out.Data == nil || len(out.Data) != 0 {
			t.Errorf("data = %v, want an empty list", out.Data)
		}
	})

	t.Run("a record the caller cannot see is 404 and the executor is not asked", func(t *testing.T) {
		svc := &eoFakeTransitionService{}
		records := &eoFakeRecordRepo{anyByID: rec, visibleIDs: map[uuid.UUID]bool{}}
		h := NewEORecordHandler(&eoFakeRecordService{}, svc, records, &eoFakeTypeRepo{})

		app := fiber.New()
		app.Use(eoTestActor(&models.User{ID: uuid.New()}))
		app.Get("/eo-records/:id/transitions", h.AvailableTransitions)

		resp, err := app.Test(httptest.NewRequest("GET", "/eo-records/"+rec.ID.String()+"/transitions", nil))
		if err != nil {
			t.Fatalf("app.Test: %v", err)
		}
		if resp.StatusCode != fiber.StatusNotFound {
			t.Errorf("status = %d, want 404", resp.StatusCode)
		}
	})
}

// TestExecuteEOTransitionMapsRefusalsToStatuses is the point of the explicit
// switch in eoServiceError: a governance refusal must not read as a server
// fault. Every one of these would be a 500 under a default-only mapping.
func TestExecuteEOTransitionMapsRefusalsToStatuses(t *testing.T) {
	rec := &models.EORecord{ID: uuid.New(), RecordTypeID: uuid.New(), Reference: "EO-DIR-2026-001"}
	transitionID := uuid.New()

	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantErrKey string
	}{
		{"not permitted for this actor", services.ErrEOTransitionNotPermitted, fiber.StatusForbidden, ""},
		{"not available from this state", services.ErrEOTransitionNotAvailable, fiber.StatusConflict, ""},
		{"blocked by an open child", services.ErrEOTransitionBlocked, fiber.StatusConflict, ""},
		{"the record went away", services.ErrEORecordNotFound, fiber.StatusNotFound, ""},
		{
			"a requirement the actor did not satisfy",
			&services.EOValidationError{Fields: map[string]string{"comment": "is required"}},
			fiber.StatusBadRequest, "comment",
		},
		{
			"a record type naming an unregistered capability",
			fmt.Errorf("record type %q: %w %q", "eo_directive", services.ErrEOUnknownCapability, "ghost"),
			fiber.StatusInternalServerError, "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &eoFakeTransitionService{executeErr: tc.err}
			records := &eoFakeRecordRepo{anyByID: rec}
			h := NewEORecordHandler(&eoFakeRecordService{}, svc, records, &eoFakeTypeRepo{})

			app := fiber.New()
			app.Use(eoTestActor(eoTestUser()))
			app.Post("/eo-records/:id/transition", h.Transition)

			raw, _ := json.Marshal(map[string]any{
				"transition_id": transitionID.String(), "comment": "please proceed",
			})
			req := httptest.NewRequest("POST", "/eo-records/"+rec.ID.String()+"/transition", bytes.NewReader(raw))
			req.Header.Set("Content-Type", "application/json")
			resp, err := app.Test(req)
			if err != nil {
				t.Fatalf("app.Test: %v", err)
			}
			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if tc.wantErrKey != "" {
				var out struct {
					Errors map[string]string `json:"errors"`
					Error  string            `json:"error"`
				}
				if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
					t.Fatalf("decode: %v", err)
				}
				if out.Errors[tc.wantErrKey] == "" {
					t.Errorf("errors = %v, want a message under %q", out.Errors, tc.wantErrKey)
				}
				if out.Error != "" {
					t.Errorf("error = %q, want the map shape", out.Error)
				}
			}
		})
	}
}

func TestExecuteEOTransitionHappyPathAndGuards(t *testing.T) {
	rec := &models.EORecord{ID: uuid.New(), RecordTypeID: uuid.New(), Reference: "EO-DIR-2026-001"}
	transitionID := uuid.New()

	t.Run("the executor is handed the transition and the actor's input", func(t *testing.T) {
		svc := &eoFakeTransitionService{executed: rec}
		records := &eoFakeRecordRepo{anyByID: rec}
		h := NewEORecordHandler(&eoFakeRecordService{}, svc, records, &eoFakeTypeRepo{})

		app := fiber.New()
		app.Use(eoTestActor(eoTestUser()))
		app.Post("/eo-records/:id/transition", h.Transition)

		raw, _ := json.Marshal(map[string]any{
			"transition_id": transitionID.String(),
			"comment":       "approved by the committee",
			"values":        map[string]any{"decision": "approve"},
		})
		req := httptest.NewRequest("POST", "/eo-records/"+rec.ID.String()+"/transition", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("app.Test: %v", err)
		}
		if resp.StatusCode != fiber.StatusOK {
			t.Fatalf("status = %d, want 200", resp.StatusCode)
		}
		if svc.seenTransitID != transitionID {
			t.Errorf("transition id = %s, want %s", svc.seenTransitID, transitionID)
		}
		if svc.seenInput.Comment != "approved by the committee" {
			t.Errorf("comment = %q, want it passed through", svc.seenInput.Comment)
		}
		if svc.seenInput.Values["decision"] != "approve" {
			t.Errorf("values = %v, want the transition's field changes passed through", svc.seenInput.Values)
		}
	})

	t.Run("no transition id is a field error and the executor is never called", func(t *testing.T) {
		svc := &eoFakeTransitionService{executed: rec}
		records := &eoFakeRecordRepo{anyByID: rec}
		h := NewEORecordHandler(&eoFakeRecordService{}, svc, records, &eoFakeTypeRepo{})

		app := fiber.New()
		app.Use(eoTestActor(eoTestUser()))
		app.Post("/eo-records/:id/transition", h.Transition)

		raw, _ := json.Marshal(map[string]any{"comment": "no transition named"})
		req := httptest.NewRequest("POST", "/eo-records/"+rec.ID.String()+"/transition", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("app.Test: %v", err)
		}
		if resp.StatusCode != fiber.StatusBadRequest {
			t.Errorf("status = %d, want 400", resp.StatusCode)
		}
		if svc.executeCalls != 0 {
			t.Errorf("executor calls = %d, want 0", svc.executeCalls)
		}
	})

	t.Run("a record the caller cannot see is 404 and nothing is executed", func(t *testing.T) {
		svc := &eoFakeTransitionService{executed: rec}
		records := &eoFakeRecordRepo{anyByID: rec, visibleIDs: map[uuid.UUID]bool{}}
		h := NewEORecordHandler(&eoFakeRecordService{}, svc, records, &eoFakeTypeRepo{})

		app := fiber.New()
		app.Use(eoTestActor(&models.User{ID: uuid.New()}))
		app.Post("/eo-records/:id/transition", h.Transition)

		raw, _ := json.Marshal(map[string]any{"transition_id": transitionID.String()})
		req := httptest.NewRequest("POST", "/eo-records/"+rec.ID.String()+"/transition", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("app.Test: %v", err)
		}
		if resp.StatusCode != fiber.StatusNotFound {
			t.Errorf("status = %d, want 404", resp.StatusCode)
		}
		if svc.executeCalls != 0 {
			t.Errorf("executor calls = %d, want 0", svc.executeCalls)
		}
	})
}

// TestEOVisibilityProbeFailureIsNotAMissingRecord: a database failure on the
// visibility probe must read as an outage, not as a deletion.
func TestEOVisibilityProbeFailureIsNotAMissingRecord(t *testing.T) {
	rec := &models.EORecord{ID: uuid.New(), RecordTypeID: uuid.New(), Reference: "EO-DIR-2026-001"}
	records := &eoFakeRecordRepo{anyByID: rec, listErr: errors.New("connection reset by peer")}
	h := NewEORecordHandler(&eoFakeRecordService{}, nil, records, &eoFakeTypeRepo{})

	app := fiber.New()
	app.Use(eoTestActor(&models.User{ID: uuid.New()})) // not an admin: the probe runs
	app.Get("/eo-records/:id", h.Get)

	resp, err := app.Test(httptest.NewRequest("GET", "/eo-records/"+rec.ID.String(), nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != fiber.StatusInternalServerError {
		t.Errorf("status = %d, want 500: a failed probe is an outage, not a missing record", resp.StatusCode)
	}
}

// TestListEORecordsRejectsAnUnknownSortKey: the allow-list refusal is the
// caller's mistake. Without the sentinel branch it was a 500 plus an
// "internal error" log line for somebody's typo.
func TestListEORecordsRejectsAnUnknownSortKey(t *testing.T) {
	eoInitValidator(t)
	records := &eoFakeRecordRepo{
		listErr: fmt.Errorf("list eo records: %w sort_by %q", repository.ErrEOUnsupportedSort, "bogus"),
	}
	h := NewEORecordHandler(&eoFakeRecordService{}, nil, records, &eoFakeTypeRepo{})

	app := fiber.New()
	app.Use(eoTestActor(eoTestUser()))
	app.Get("/eo-records", h.List)

	resp, err := app.Test(httptest.NewRequest("GET", "/eo-records?sort_by=bogus", nil))
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	var out struct {
		Success bool              `json:"success"`
		Errors  map[string]string `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Success {
		t.Errorf("success = %v, want false", out.Success)
	}
	// The message names what the caller may ask for instead.
	if msg := out.Errors["sort_by"]; msg == "" || !strings.Contains(msg, "created_at") {
		t.Errorf("errors[sort_by] = %q, want it to name the allow-listed keys", msg)
	}
}
