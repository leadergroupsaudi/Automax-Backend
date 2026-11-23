package models

import "strings"

// Spec §7.11 names three visibility mechanisms: the sensitivity label,
// row-level visible_to, and PER-TYPE PERMISSIONS. This file is the third one's
// vocabulary — the code `eo:<type_key>:<action>` — and it is deliberately the
// ONLY place that spelling is written.
//
// Three surfaces consume it and they must agree exactly:
//
//   - internal/database/seed_eo_platform.go writes the rows an administrator
//     ticks;
//   - middleware.EOTypePermission answers 403 on the record routes;
//   - repository.applyEORecordTypePermissions filters the register's LIST.
//
// A second copy of the format string is how Plan 1 shipped a Critical: the
// Referral seed wrote row-level visibility under `visibility` while the engine
// read `visible_to`, so every Referral was unrestricted while the code LOOKED
// enforced. A gate that builds a code nobody holds refuses everyone; a filter
// that builds a code everybody holds filters nothing. Both failures are silent,
// and both are prevented by there being one function.
const (
	EOTypePermissionView   = "view"
	EOTypePermissionCreate = "create"
	EOTypePermissionUpdate = "update"
)

// eoTypePermissionPrefix and the separator are split out so the builder and
// the parser below cannot disagree about them.
const eoTypePermissionPrefix = "eo:"
const eoTypePermissionSep = ":"

// EOTypePermissionCode is the permission code that grants `action` on records
// of the record type `typeKey` — e.g. ("eo_directive", "view") is
// "eo:eo_directive:view".
func EOTypePermissionCode(typeKey, action string) string {
	return eoTypePermissionPrefix + typeKey + eoTypePermissionSep + action
}

// EOTypeKeysFromPermissionCodes is EOTypePermissionCode read backwards: given
// every permission code a caller holds, it returns the record-type KEYS they
// hold `action` on.
//
// The register's list filter needs the inverse rather than the builder,
// because it must restrict a query to a set of types without first knowing
// which types exist. Deriving the keys in Go and matching them against
// eo_record_types.key keeps the code format out of SQL entirely: a string
// literal 'eo:' || key || ':view' inside a WHERE clause would be a second
// spelling that no compiler, and no test of this function, could keep in step.
//
// A module-level code (eo:view, eo:types:manage) is not a per-type code and is
// skipped: neither has the shape eo:<key>:<action> for a key that is a single
// segment. A key containing a colon could not be told apart from a longer code
// and is rejected for that reason — eo_record_types.key is a snake_case
// identifier, so this excludes nothing real.
func EOTypeKeysFromPermissionCodes(codes []string, action string) []string {
	suffix := eoTypePermissionSep + action
	seen := make(map[string]bool, len(codes))
	out := make([]string, 0, len(codes))
	for _, code := range codes {
		if !strings.HasPrefix(code, eoTypePermissionPrefix) {
			continue
		}
		// The suffix is checked on the REMAINDER, not on the whole code. The
		// module-level code "eo:view" ends with ":view" as a whole string, so
		// checking the code itself would read it as a grant on a record type
		// named "view" — which every EO user holds, and which would make the
		// register's filter match a type key it was never told about.
		rest := strings.TrimPrefix(code, eoTypePermissionPrefix)
		if !strings.HasSuffix(rest, suffix) {
			continue
		}
		key := strings.TrimSuffix(rest, suffix)
		if key == "" || strings.Contains(key, eoTypePermissionSep) {
			continue
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
	}
	return out
}
