package handlers

import (
	"reflect"
	"testing"

	"github.com/automax/backend/internal/models"
)

func eoUserWithCodes(codes ...string) *models.User {
	perms := make([]models.Permission, len(codes))
	for i, c := range codes {
		perms[i] = models.Permission{Code: c, IsActive: true}
	}
	return &models.User{Roles: []models.Role{{Code: "r", IsActive: true, Permissions: perms}}}
}

// The flag is populated from the caller's own codes: set for a holder, NOT
// set for a caller lacking the code (a flag that is always true is the
// programme's signature defect), and a value already on the filter — as a
// form-encoded body could place there — is overwritten, not trusted.
// Mutation: set the flag unconditionally; drop the assignment (the preset
// true then survives).
func TestEOScopeFilterSetsViewAllBodiesOnlyForTheHolder(t *testing.T) {
	var f models.EORecordFilter
	eoScopeFilter(&f, eoUserWithCodes("eo:view", "eo:eo_referral:view", models.EOPermissionViewAllBodies))
	if !f.ViewAllBodies {
		t.Error("a holder of the oversight permission did not get the flag")
	}

	for name, u := range map[string]*models.User{
		"a per-type view code only": eoUserWithCodes("eo:view", "eo:eo_referral:view"),
		"no codes":                  eoUserWithCodes(),
		"a lookalike code":          eoUserWithCodes("eo:records:view_all_bodies_x", "eo:view_all_bodies"),
		"the code, inactive":        {Roles: []models.Role{{Code: "r", IsActive: true, Permissions: []models.Permission{{Code: models.EOPermissionViewAllBodies, IsActive: false}}}}},
	} {
		f := models.EORecordFilter{ViewAllBodies: true} // as if a client had set it
		eoScopeFilter(&f, u)
		if f.ViewAllBodies {
			t.Errorf("%s: the flag is set for a caller lacking the oversight permission", name)
		}
	}
}

// The flag carries query:"-" and json:"-", like the other server-filled fields.
func TestEORecordFilterViewAllBodiesIsNotClientSettable(t *testing.T) {
	field, ok := reflect.TypeOf(models.EORecordFilter{}).FieldByName("ViewAllBodies")
	if !ok {
		t.Fatal("EORecordFilter.ViewAllBodies is gone")
	}
	if field.Tag.Get("query") != "-" || field.Tag.Get("json") != "-" {
		t.Errorf("ViewAllBodies is client-bindable: tag %q", field.Tag)
	}
}
