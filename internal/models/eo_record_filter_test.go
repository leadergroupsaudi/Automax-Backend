package models

import (
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/valyala/fasthttp"
)

// The handler-filled fields decide row-level visibility (§7.11). Fiber's
// QueryParser falls back to the Go field name when a field has no query tag,
// so json:"-" alone would leave ?IsAdmin=true open. Nothing but this test
// notices a field losing its query:"-".
func TestEORecordFilterServerFieldsCannotBeSetFromTheQueryString(t *testing.T) {
	for _, param := range []string{
		"IsAdmin=true",
		"UserID=" + uuid.NewString(),
		"VisibleToDepartmentIDs=" + uuid.NewString(),
	} {
		t.Run(param[:strings.IndexByte(param, '=')], func(t *testing.T) {
			app := fiber.New()
			fctx := &fasthttp.RequestCtx{}
			fctx.Request.SetRequestURI("/?" + param + "&search=x&page=2")
			c := app.AcquireCtx(fctx)
			defer app.ReleaseCtx(c)

			var f EORecordFilter
			if err := c.QueryParser(&f); err != nil {
				t.Fatalf("QueryParser: %v", err)
			}
			// The parse really ran: client fields arrived.
			if f.Search != "x" || f.Page != 2 {
				t.Fatalf("client fields not parsed: %+v", f)
			}
			if f.IsAdmin || f.UserID != uuid.Nil || len(f.VisibleToDepartmentIDs) != 0 {
				t.Errorf("server-filled field set from the query string: %+v", f)
			}
		})
	}
}
