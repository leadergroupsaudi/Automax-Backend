package models

import (
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/valyala/fasthttp"
)

// IncidentFilter's server-filled fields must not be bindable from the query
// string. Fiber's QueryParser falls back to the Go field name when a field has
// no query alias, and json:"-" stops request-body binding only — so without
// query:"-" a caller could send ?IsAdmin=true and switch off role scoping in
// the stats query. Nothing but this test catches a regression: the compiler
// cannot, and the field is only read deep in the repository.
func TestIncidentFilterServerFieldsCannotBeSetFromTheQueryString(t *testing.T) {
	someID := uuid.New().String()

	cases := []struct {
		name  string
		query string
		check func(t *testing.T, f *IncidentFilter)
	}{
		{"IsAdmin", "IsAdmin=true", func(t *testing.T, f *IncidentFilter) {
			if f.IsAdmin {
				t.Error("IsAdmin was set from the query string; role scoping would be bypassed")
			}
		}},
		{"UserID", "UserID=" + someID, func(t *testing.T, f *IncidentFilter) {
			if f.UserID != uuid.Nil {
				t.Errorf("UserID was set from the query string: %v", f.UserID)
			}
		}},
		{"IncludeUnassignedDepartment", "IncludeUnassignedDepartment=true", func(t *testing.T, f *IncidentFilter) {
			if f.IncludeUnassignedDepartment {
				t.Error("IncludeUnassignedDepartment was set from the query string")
			}
		}},
		{"ExactRecordType", "ExactRecordType=incident", func(t *testing.T, f *IncidentFilter) {
			if f.ExactRecordType != nil {
				t.Errorf("ExactRecordType was set from the query string: %v", *f.ExactRecordType)
			}
		}},
	}

	app := fiber.New()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := app.AcquireCtx(&fasthttp.RequestCtx{})
			defer app.ReleaseCtx(ctx)
			// A client-settable field rides along, so a parse that silently does
			// nothing cannot pass this test vacuously.
			ctx.Request().URI().SetQueryString(tc.query + "&search=roadworks&page=2")

			f := &IncidentFilter{}
			if err := ctx.QueryParser(f); err != nil {
				t.Fatalf("QueryParser: %v", err)
			}
			if f.Search != "roadworks" || f.Page != 2 {
				t.Fatalf("client-settable fields did not parse: search=%q page=%d", f.Search, f.Page)
			}
			tc.check(t, f)
		})
	}
}
