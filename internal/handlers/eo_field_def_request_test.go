package handlers

import (
	"reflect"
	"strings"
	"testing"
)

// The untranslated marker survives designer edits only because the designer's
// request struct cannot carry it: applyTo copies the request onto the STORED
// row, so an absent member leaves the column untouched. Adding the member
// would let any designer save clear it.
// Mutation: add `ArUntranslated bool `json:"ar_untranslated"“ to
// eoFieldDefRequest.
func TestFieldDefRequestCannotCarryTheUntranslatedMarker(t *testing.T) {
	rt := reflect.TypeOf(eoFieldDefRequest{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		tag := strings.Split(f.Tag.Get("json"), ",")[0]
		if tag == "ar_untranslated" || strings.EqualFold(f.Name, "ArUntranslated") {
			t.Errorf("eoFieldDefRequest has member %s (json %q); the marker must stay write-only", f.Name, tag)
		}
	}
}
