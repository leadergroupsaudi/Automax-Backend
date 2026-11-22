package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// ── I3: §7.14's guard rails reach the whole subtree ─────────────────────────

// eoAncestorFixture builds the shape the guard rail was blind to: an ABSTRACT
// base type with no records of its own, and a concrete descendant that holds
// them.
type eoAncestorFixture struct {
	base    models.EORecordType
	mid     models.EORecordType
	leaf    models.EORecordType
	types   *eoFakeTypeRepo
	records *eoFakeRecordRepo
}

func eoNewAncestorFixture(leafRecords int64) *eoAncestorFixture {
	base := models.EORecordType{
		ID: uuid.New(), Key: "eo_base", Name: "Base", IsAbstract: true,
	}
	mid := models.EORecordType{
		ID: uuid.New(), Key: "eo_correspondence", Name: "Correspondence", IsAbstract: true,
		ParentTypeID: &base.ID,
	}
	leaf := models.EORecordType{
		ID: uuid.New(), Key: "eo_directive", Name: "Directive",
		ParentTypeID: &mid.ID, ReferencePrefix: "EO-DIR", ReferencePattern: "EO-DIR-YYYY-NNN",
	}
	return &eoAncestorFixture{
		base: base, mid: mid, leaf: leaf,
		types: &eoFakeTypeRepo{
			byID: &base,
			all:  []models.EORecordType{base, mid, leaf},
			children: map[uuid.UUID][]models.EORecordType{
				base.ID: {mid},
				mid.ID:  {leaf},
			},
		},
		// The abstract ancestors hold NOTHING of their own. That is the whole
		// point: the old guard counted exactly this and always saw zero.
		records: &eoFakeRecordRepo{totalByType: map[uuid.UUID]int64{leaf.ID: leafRecords}},
	}
}

// TestEODestructiveEditOnAnAncestorWithPopulatedDescendants is the I3 field
// half. Remove a field from the ABSTRACT base type while a descendant holds
// records: §7.14 says no, and before the fix the base type's own record count
// was zero, so the guard could not fire.
//
// The single-line mutation that makes this fail: in eoTypeHasRecords, replace
// the DescendantIDs subtree with `ids := []string{typeID.String()}`.
func TestEODestructiveEditOnAnAncestorWithPopulatedDescendants(t *testing.T) {
	fieldID := uuid.New()
	cases := []struct {
		name        string
		leafRecords int64
		wantStatus  int
		wantDeletes int
	}{
		{"a descendant holds records: the removal is refused", 3, fiber.StatusBadRequest, 0},
		{"nothing anywhere in the subtree: the removal is allowed", 0, fiber.StatusOK, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := eoNewAncestorFixture(tc.leafRecords)
			f.types.fieldDefs = []models.EOFieldDef{{
				ID: fieldID, RecordTypeID: f.base.ID, Key: "subject_ar",
				Label: "Subject", Kind: models.EOKindText,
			}}
			h := NewEORecordTypeHandler(f.types, f.records)

			app := fiber.New()
			app.Use(eoTestActor(eoTestUser()))
			app.Delete("/eo-record-types/:id/fields/:field_id", h.DeleteField)

			resp, err := app.Test(httptest.NewRequest("DELETE",
				fmt.Sprintf("/eo-record-types/%s/fields/%s", f.base.ID, fieldID), nil))
			if err != nil {
				t.Fatalf("app.Test: %v", err)
			}
			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if f.types.deleteFieldCalls != tc.wantDeletes {
				t.Errorf("DeleteFieldDef calls = %d, want %d", f.types.deleteFieldCalls, tc.wantDeletes)
			}
			// The register must have been asked about the DESCENDANT, not
			// only about the type in the path.
			if f.records.listFilter == nil {
				t.Fatal("the register was never asked whether the subtree holds records")
			}
			asked := map[string]bool{}
			for _, id := range f.records.listFilter.RecordTypeID {
				asked[id] = true
			}
			if !asked[f.leaf.ID.String()] {
				t.Errorf("the record count asked about %v, which does not include the populated descendant %s",
					f.records.listFilter.RecordTypeID, f.leaf.ID)
			}
		})
	}
}

// TestEOTypeUpdateOnAnAncestorWithPopulatedDescendants is the same blindness
// on the four identity edits §7.14 added on 2026-11-22. Re-keying an abstract
// ancestor breaks reference-prefix uniqueness for references its descendants
// have already issued.
func TestEOTypeUpdateOnAnAncestorWithPopulatedDescendants(t *testing.T) {
	f := eoNewAncestorFixture(1)
	h := NewEORecordTypeHandler(f.types, f.records)

	app := fiber.New()
	app.Use(eoTestActor(eoTestUser()))
	app.Put("/eo-record-types/:id", h.Update)

	body, _ := json.Marshal(map[string]any{
		"key": "eo_base_renamed", "name": "Base", "is_abstract": true,
	})
	req := httptest.NewRequest("PUT", "/eo-record-types/"+f.base.ID.String(), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test: %v", err)
	}
	if resp.StatusCode != fiber.StatusBadRequest {
		t.Errorf("status = %d, want 400: re-keying an ancestor whose descendants hold records is destructive", resp.StatusCode)
	}
	if f.types.updateCalls != 0 {
		t.Errorf("repository Update calls = %d, want 0", f.types.updateCalls)
	}
}

// TestDeleteEORecordTypeRefusesATypeWithChildren covers the second half of I3.
// A soft-deleted parent is invisible to GetByID, so Ancestors answers "parent
// … not found" and every create, update and transition of every descendant
// fails until the row is un-deleted in SQL.
//
// The single-line mutation that makes this fail: delete the `if
// len(children) > 0` refusal from Delete.
func TestDeleteEORecordTypeRefusesATypeWithChildren(t *testing.T) {
	t.Run("a type with children is refused even with no records anywhere", func(t *testing.T) {
		f := eoNewAncestorFixture(0)
		h := NewEORecordTypeHandler(f.types, f.records)

		app := fiber.New()
		app.Use(eoTestActor(eoTestUser()))
		app.Delete("/eo-record-types/:id", h.Delete)

		resp, err := app.Test(httptest.NewRequest("DELETE", "/eo-record-types/"+f.base.ID.String(), nil))
		if err != nil {
			t.Fatalf("app.Test: %v", err)
		}
		if resp.StatusCode != fiber.StatusConflict {
			t.Errorf("status = %d, want 409", resp.StatusCode)
		}
		if f.types.deleteCalls != 0 {
			t.Errorf("repository Delete calls = %d, want 0", f.types.deleteCalls)
		}
		var out struct {
			Error string `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		// The message names the child, so the operator knows what to move.
		if want := f.mid.Key; !bytes.Contains([]byte(out.Error), []byte(want)) {
			t.Errorf("error = %q, want it to name the child type %q", out.Error, want)
		}
	})

	t.Run("a childless, record-free leaf is deletable", func(t *testing.T) {
		f := eoNewAncestorFixture(0)
		f.types.byID = &f.leaf
		h := NewEORecordTypeHandler(f.types, f.records)

		app := fiber.New()
		app.Use(eoTestActor(eoTestUser()))
		app.Delete("/eo-record-types/:id", h.Delete)

		resp, err := app.Test(httptest.NewRequest("DELETE", "/eo-record-types/"+f.leaf.ID.String(), nil))
		if err != nil {
			t.Fatalf("app.Test: %v", err)
		}
		if resp.StatusCode != fiber.StatusOK {
			t.Errorf("status = %d, want 200", resp.StatusCode)
		}
		if f.types.deleteCalls != 1 {
			t.Errorf("repository Delete calls = %d, want 1", f.types.deleteCalls)
		}
	})

	t.Run("a childless leaf that holds records is still refused", func(t *testing.T) {
		f := eoNewAncestorFixture(2)
		f.types.byID = &f.leaf
		h := NewEORecordTypeHandler(f.types, f.records)

		app := fiber.New()
		app.Use(eoTestActor(eoTestUser()))
		app.Delete("/eo-record-types/:id", h.Delete)

		resp, err := app.Test(httptest.NewRequest("DELETE", "/eo-record-types/"+f.leaf.ID.String(), nil))
		if err != nil {
			t.Fatalf("app.Test: %v", err)
		}
		if resp.StatusCode != fiber.StatusConflict {
			t.Errorf("status = %d, want 409", resp.StatusCode)
		}
		if f.types.deleteCalls != 0 {
			t.Errorf("repository Delete calls = %d, want 0", f.types.deleteCalls)
		}
	})
}

// ── I2a: the field kind is validated ────────────────────────────────────────

// TestCreateEOFieldDefValidatesTheKind. The single-line mutation that makes it
// fail: drop the `case !models.IsEOFieldKind(f.Kind)` arm from
// eoValidateFieldShape.
func TestCreateEOFieldDefValidatesTheKind(t *testing.T) {
	typeID := uuid.New()
	stored := models.EORecordType{ID: typeID, Key: "eo_directive", Name: "Directive"}

	post := func(t *testing.T, kind string) (int, *eoFakeTypeRepo) {
		t.Helper()
		types := &eoFakeTypeRepo{byID: &stored, all: []models.EORecordType{stored}}
		h := NewEORecordTypeHandler(types, &eoFakeRecordRepo{})

		app := fiber.New()
		app.Use(eoTestActor(eoTestUser()))
		app.Post("/eo-record-types/:id/fields", h.CreateField)

		body, _ := json.Marshal(map[string]any{"key": "k", "label": "L", "kind": kind})
		req := httptest.NewRequest("POST", "/eo-record-types/"+typeID.String()+"/fields", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("app.Test: %v", err)
		}
		return resp.StatusCode, types
	}

	t.Run("an unknown kind is refused with the field-error map", func(t *testing.T) {
		types := &eoFakeTypeRepo{byID: &stored, all: []models.EORecordType{stored}}
		h := NewEORecordTypeHandler(types, &eoFakeRecordRepo{})

		app := fiber.New()
		app.Use(eoTestActor(eoTestUser()))
		app.Post("/eo-record-types/:id/fields", h.CreateField)

		body, _ := json.Marshal(map[string]any{"key": "k", "label": "L", "kind": "drop_down"})
		req := httptest.NewRequest("POST", "/eo-record-types/"+typeID.String()+"/fields", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
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
			t.Error("success = true, want false")
		}
		if out.Errors["kind"] == "" {
			t.Errorf("errors = %v, want a message against the kind key", out.Errors)
		}
		if types.createFieldCalls != 0 {
			t.Errorf("CreateFieldDef calls = %d, want 0", types.createFieldCalls)
		}
	})

	t.Run("every one of the configured kinds is accepted", func(t *testing.T) {
		if len(models.EOFieldKinds) != 27 {
			t.Fatalf("models.EOFieldKinds has %d entries, want the 27 kinds of spec §7.3", len(models.EOFieldKinds))
		}
		for _, kind := range models.EOFieldKinds {
			status, types := post(t, kind)
			if status != fiber.StatusCreated {
				t.Errorf("kind %q: status = %d, want 201", kind, status)
			}
			if types.createFieldCalls != 1 {
				t.Errorf("kind %q: CreateFieldDef calls = %d, want 1", kind, types.createFieldCalls)
			}
		}
	})

	t.Run("an empty kind is still refused", func(t *testing.T) {
		if status, _ := post(t, ""); status != fiber.StatusBadRequest {
			t.Errorf("status = %d, want 400", status)
		}
	})
}

// ── I6: a parent_record_id the caller may not see is a 404 ──────────────────

// TestCreateEORecordChecksParentVisibility. The single-line mutation that
// makes it fail: delete the `if req.ParentRecordID != nil { … eoLoadRecord …}`
// block from Create.
func TestCreateEORecordChecksParentVisibility(t *testing.T) {
	user := &models.User{ID: uuid.New()} // NOT a super admin: visibility applies
	parent := &models.EORecord{ID: uuid.New(), RecordTypeID: uuid.New(), Reference: "EO-DIR-2026-001"}

	run := func(t *testing.T, visible bool) (int, *eoFakeRecordService) {
		t.Helper()
		visibleIDs := map[uuid.UUID]bool{}
		if visible {
			visibleIDs[parent.ID] = true
		}
		repo := &eoFakeRecordRepo{
			byID:       map[uuid.UUID]*models.EORecord{parent.ID: parent},
			visibleIDs: visibleIDs,
		}
		svc := &eoFakeRecordService{created: &models.EORecord{ID: uuid.New(), Reference: "EO-DIR-2026-002"}}
		h := NewEORecordHandler(svc, nil, repo, &eoFakeTypeRepo{})

		app := fiber.New()
		app.Use(eoTestActor(user))
		app.Post("/eo-records", h.Create)

		body, _ := json.Marshal(models.EORecordCreateRequest{
			RecordTypeID:      uuid.New(),
			ParentRecordID:    &parent.ID,
			ParentLinkTypeKey: "spawned_task",
		})
		req := httptest.NewRequest("POST", "/eo-records", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("app.Test: %v", err)
		}
		return resp.StatusCode, svc
	}

	t.Run("a parent the caller cannot see is 404 and the service is never reached", func(t *testing.T) {
		status, svc := run(t, false)
		if status != fiber.StatusNotFound {
			t.Errorf("status = %d, want 404: invisible and absent must be indistinguishable", status)
		}
		if svc.createCalls != 0 {
			t.Errorf("service Create calls = %d, want 0: the record must not be numbered under a parent the caller may not read", svc.createCalls)
		}
	})

	t.Run("a visible parent still creates", func(t *testing.T) {
		status, svc := run(t, true)
		if status != fiber.StatusCreated {
			t.Errorf("status = %d, want 201", status)
		}
		if svc.createCalls != 1 {
			t.Errorf("service Create calls = %d, want 1", svc.createCalls)
		}
	})

	t.Run("no parent at all is unaffected", func(t *testing.T) {
		repo := &eoFakeRecordRepo{visibleIDs: map[uuid.UUID]bool{}}
		svc := &eoFakeRecordService{created: &models.EORecord{ID: uuid.New()}}
		h := NewEORecordHandler(svc, nil, repo, &eoFakeTypeRepo{})

		app := fiber.New()
		app.Use(eoTestActor(user))
		app.Post("/eo-records", h.Create)

		body, _ := json.Marshal(models.EORecordCreateRequest{RecordTypeID: uuid.New()})
		req := httptest.NewRequest("POST", "/eo-records", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("app.Test: %v", err)
		}
		if resp.StatusCode != fiber.StatusCreated {
			t.Errorf("status = %d, want 201", resp.StatusCode)
		}
		if svc.createCalls != 1 {
			t.Errorf("service Create calls = %d, want 1", svc.createCalls)
		}
	})
}

// ── I1: the jsonb and date-range filters are reachable ──────────────────────

// TestSearchEORecordsReachesTheBodyOnlyFilters. The single-line mutation that
// makes it fail: remove `r.Post("/search", …)` from RegisterRoutes.
func TestSearchEORecordsReachesTheBodyOnlyFilters(t *testing.T) {
	eoInitValidator(t)
	user := eoTestUser()

	search := func(t *testing.T, body map[string]any, actor *models.User) (int, *eoFakeRecordRepo) {
		t.Helper()
		repo := &eoFakeRecordRepo{listOut: []models.EORecord{}}
		h := NewEORecordHandler(&eoFakeRecordService{}, nil, repo, &eoFakeTypeRepo{})

		app := fiber.New()
		app.Use(eoTestActor(actor))
		// Registered through RegisterRoutes, not hand-mounted: the route's
		// EXISTENCE and its position before /:id are part of what is covered.
		h.RegisterRoutes(app.Group("/eo-records"), eoNoPerm)

		raw, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", "/eo-records/search", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("app.Test: %v", err)
		}
		return resp.StatusCode, repo
	}

	t.Run("a jsonb values filter reaches the repository", func(t *testing.T) {
		status, repo := search(t, map[string]any{
			"values": map[string]any{"committee": "audit", "cycle": float64(3)},
		}, user)
		if status != fiber.StatusOK {
			t.Fatalf("status = %d, want 200: the /:id route matched first, or the route is absent", status)
		}
		if repo.listFilter == nil {
			t.Fatal("the register was never queried")
		}
		if got := repo.listFilter.Values["committee"]; got != "audit" {
			t.Errorf("filter.Values[committee] = %v, want \"audit\": the body-only jsonb filter did not reach the repository", got)
		}
		if got := repo.listFilter.Values["cycle"]; got != float64(3) {
			t.Errorf("filter.Values[cycle] = %v, want 3", got)
		}
	})

	t.Run("a date range reaches the repository", func(t *testing.T) {
		from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		to := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
		status, repo := search(t, map[string]any{
			"created_from": from.Format(time.RFC3339),
			"created_to":   to.Format(time.RFC3339),
			"due_from":     from.Format(time.RFC3339),
			"due_to":       to.Format(time.RFC3339),
		}, user)
		if status != fiber.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		f := repo.listFilter
		if f == nil {
			t.Fatal("the register was never queried")
		}
		for name, got := range map[string]*time.Time{
			"created_from": f.CreatedFrom, "created_to": f.CreatedTo,
			"due_from": f.DueFrom, "due_to": f.DueTo,
		} {
			if got == nil {
				t.Errorf("filter.%s is nil: the body-only date filter did not reach the repository", name)
			}
		}
		if f.CreatedFrom != nil && !f.CreatedFrom.Equal(from) {
			t.Errorf("filter.CreatedFrom = %v, want %v", f.CreatedFrom, from)
		}
	})

	t.Run("the scope fields cannot be set from the body", func(t *testing.T) {
		// A body that tries every server-filled field by its Go name AND by
		// the json name it would have if the json:"-" tag were ever dropped.
		forged := uuid.New()
		status, repo := search(t, map[string]any{
			"IsAdmin": true, "is_admin": true,
			"UserID": forged.String(), "user_id": forged.String(),
			"VisibleToDepartmentIDs":    []string{forged.String()},
			"visible_to_department_ids": []string{forged.String()},
			"RecordID":                  forged.String(), "record_id": forged.String(),
		}, user)
		if status != fiber.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		f := repo.listFilter
		if f == nil {
			t.Fatal("the register was never queried")
		}
		if f.IsAdmin != user.IsSuperAdmin {
			t.Errorf("filter.IsAdmin = %v, want %v (the session's answer, not the body's)", f.IsAdmin, user.IsSuperAdmin)
		}
		if f.UserID != user.ID {
			t.Errorf("filter.UserID = %v, want the authenticated user %v", f.UserID, user.ID)
		}
		if len(f.VisibleToDepartmentIDs) != 0 {
			t.Errorf("filter.VisibleToDepartmentIDs = %v, want the session's (empty) set", f.VisibleToDepartmentIDs)
		}
		if f.RecordID != nil {
			t.Errorf("filter.RecordID = %v, want nil: it is the single-record probe's, not a caller's", f.RecordID)
		}
	})

	t.Run("search clamps the limit like every other list route", func(t *testing.T) {
		status, repo := search(t, map[string]any{"limit": 0}, user)
		if status != fiber.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		if repo.listFilter.Limit < 1 {
			t.Errorf("filter.Limit = %d, want >= 1", repo.listFilter.Limit)
		}
	})
}

// eoTypeRepoAssertion keeps the compiler honest: the fake must still satisfy
// the interface it embeds after the two new methods were added to it.
var _ = func() bool {
	var f eoFakeTypeRepo
	_, _ = f.ChildTypes(context.Background(), uuid.Nil)
	return true
}
