package models

import (
	"reflect"
	"testing"
)

// TestEOTypePermissionCodeRoundTrip pins the builder and the parser to each
// other. The route gate builds a code and asks whether the caller holds it;
// the register's list filter takes the codes the caller holds and asks which
// type keys they name. If those two disagree by one character, one surface
// refuses everybody and the other filters nobody — and neither says so.
func TestEOTypePermissionCodeRoundTrip(t *testing.T) {
	keys := []string{"eo_directive", "eo_decision_record", "eo_incoming_correspondence"}
	for _, action := range []string{EOTypePermissionView, EOTypePermissionCreate, EOTypePermissionUpdate} {
		codes := make([]string, 0, len(keys))
		for _, k := range keys {
			codes = append(codes, EOTypePermissionCode(k, action))
		}
		if got := EOTypeKeysFromPermissionCodes(codes, action); !reflect.DeepEqual(got, keys) {
			t.Errorf("round trip for %q = %v, want %v", action, got, keys)
		}
	}
}

// TestEOTypePermissionCodeSpelling states the format literally, once. Every
// other assertion in this change is written against EOTypePermissionCode, so
// without this one a change to the format would move the gate, the filter and
// the seed together — away from the codes already granted in live databases.
func TestEOTypePermissionCodeSpelling(t *testing.T) {
	if got, want := EOTypePermissionCode("eo_directive", EOTypePermissionView), "eo:eo_directive:view"; got != want {
		t.Errorf("EOTypePermissionCode = %q, want %q (the spelling seeded by eoSeedTypePermissions)", got, want)
	}
	if got, want := EOTypePermissionCode("eo_directive", EOTypePermissionUpdate), "eo:eo_directive:update"; got != want {
		t.Errorf("EOTypePermissionCode = %q, want %q", got, want)
	}
}

// TestEOTypeKeysIgnoresModuleLevelCodes is the one that matters for the LIST
// filter. "eo:view" ends with ":view"; read carelessly it names a record type
// called "view" — and since every EO user holds eo:view, a filter built from
// that would be satisfied by a type key nobody granted.
func TestEOTypeKeysIgnoresModuleLevelCodes(t *testing.T) {
	held := []string{
		"eo:view", "eo:create", "eo:update", "eo:transition", "eo:types:manage",
		"queries:view", "users:view", "", "eo:", "eo::view",
		EOTypePermissionCode("eo_directive", EOTypePermissionView),
	}
	got := EOTypeKeysFromPermissionCodes(held, EOTypePermissionView)
	if want := []string{"eo_directive"}; !reflect.DeepEqual(got, want) {
		t.Errorf("keys = %v, want %v: only a per-type code names a type", got, want)
	}
}

// TestEOTypeKeysSeparatesActions: holding update must not be read as holding
// view. The register's filter asks for view only.
func TestEOTypeKeysSeparatesActions(t *testing.T) {
	held := []string{
		EOTypePermissionCode("eo_directive", EOTypePermissionUpdate),
		EOTypePermissionCode("eo_decision_record", EOTypePermissionView),
	}
	if got, want := EOTypeKeysFromPermissionCodes(held, EOTypePermissionView), []string{"eo_decision_record"}; !reflect.DeepEqual(got, want) {
		t.Errorf("view keys = %v, want %v", got, want)
	}
	if got, want := EOTypeKeysFromPermissionCodes(held, EOTypePermissionUpdate), []string{"eo_directive"}; !reflect.DeepEqual(got, want) {
		t.Errorf("update keys = %v, want %v", got, want)
	}
	if got := EOTypeKeysFromPermissionCodes(nil, EOTypePermissionView); len(got) != 0 {
		t.Errorf("keys for a caller holding nothing = %v, want none", got)
	}
}

// TestUserPermissionCodesMirrorsHasPermission: the filter is fed from
// PermissionCodes and the gate asks HasPermission. An inactive permission is
// not held by either.
func TestUserPermissionCodesMirrorsHasPermission(t *testing.T) {
	u := &User{Roles: []Role{
		{Permissions: []Permission{
			{Code: "eo:view", IsActive: true},
			{Code: "eo:eo_directive:view", IsActive: true},
			{Code: "eo:eo_decision_record:view", IsActive: false},
		}},
		{Permissions: []Permission{{Code: "eo:eo_directive:view", IsActive: true}}},
	}}
	got := u.PermissionCodes()
	if want := []string{"eo:view", "eo:eo_directive:view"}; !reflect.DeepEqual(got, want) {
		t.Errorf("PermissionCodes = %v, want %v (de-duplicated, inactive dropped)", got, want)
	}
	for _, code := range got {
		if !u.HasPermission(code) {
			t.Errorf("PermissionCodes returned %q but HasPermission says no", code)
		}
	}
	if u.HasPermission("eo:eo_decision_record:view") {
		t.Error("HasPermission accepted an inactive permission")
	}
	// A super admin holds everything, and holds it through HasPermission
	// rather than through this list: the list is what they ARE granted.
	if (&User{IsSuperAdmin: true}).HasPermission("eo:anything:view") != true {
		t.Error("super admin must hold every code")
	}
}
