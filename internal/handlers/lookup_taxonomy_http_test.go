package handlers

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/gofiber/fiber/v2"

	"github.com/automax/backend/internal/repository"
)

// The path a record form actually calls: GET /lookups/:code, through the real
// handler and repository, against the category the real seed wrote. The
// category is stored lowercase; the handler upper-cases the code, so before the
// repository compared case-insensitively every spelling returned [].
// Mutation: restore `lookup_categories.code = ?` in ListValuesByCategoryCode.
func TestLookupByCodeServesTheSeededSubjectTaxonomy(t *testing.T) {
	db := eoSignoffTestDB(t)
	app := fiber.New()
	app.Get("/lookups/:code", NewLookupHandler(repository.NewLookupRepository(db)).GetValuesByCategoryCode)

	for _, spelling := range []string{"eo_subject_taxonomy", "EO_SUBJECT_TAXONOMY", "Eo_Subject_Taxonomy"} {
		resp, err := app.Test(httptest.NewRequest("GET", "/lookups/"+spelling, nil))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		var body struct {
			Data []struct {
				Code   string `json:"code"`
				Name   string `json:"name"`
				NameAr string `json:"name_ar"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("%s: %v: %s", spelling, err, raw)
		}
		if resp.StatusCode != 200 || len(body.Data) != 10 {
			t.Fatalf("GET /lookups/%s = %d with %d values, want 200 with 10: %s", spelling, resp.StatusCode, len(body.Data), raw)
		}
		var codes []string
		for _, v := range body.Data {
			if v.Name == "" || v.NameAr == "" {
				t.Errorf("%s: value %q lacks a name or Arabic name", spelling, v.Code)
			}
			codes = append(codes, v.Code)
		}
		sort.Strings(codes)
		if codes[0] != "building_permits" || codes[len(codes)-1] != "tariffs" {
			t.Errorf("%s: unexpected codes %v", spelling, codes)
		}
	}
}
