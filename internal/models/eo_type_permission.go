package models

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
)

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

// EOPermissionViewAllBodies is the module-level permission that lifts §7.11's
// ROW-LEVEL body restriction (a record's visible_to) for the caller. It is
// the oversight role's grant (decided with the client, 2026-11-25).
//
// IT COMPOSES; IT DOES NOT SUBSTITUTE. It is not a per-type code and grants
// no register: the caller must still hold eo:<type_key>:view for every
// register it reads, and this only removes the body restriction inside those.
// Its shape (eo:records:view_all_bodies) is deliberately not eo:<key>:view, so
// EOTypeKeysFromPermissionCodes cannot read it as a grant on a type.
const EOPermissionViewAllBodies = "eo:records:view_all_bodies"

// EOCanViewAllBodies reports whether the caller's permission codes include
// EOPermissionViewAllBodies. It is the one place the handler asks, so the flag
// on the filter is derived from the code and never set independently.
func EOCanViewAllBodies(codes []string) bool {
	for _, c := range codes {
		if c == EOPermissionViewAllBodies {
			return true
		}
	}
	return false
}

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

// ── the rows themselves ─────────────────────────────────────────────────────
//
// TWO WRITERS, ONE CONSTRUCTOR. The boot seed
// (database.eoSeedTypePermissions) writes these rows for every type it seeds,
// and the type designer writes them for a type an administrator creates at
// runtime (repository.eoRecordTypeRepository.Create). Until Plan 3's A2 fix
// round the designer wrote NOTHING, and with the per-type gate enforcing, a
// type created through the designer was invisible to every non-super-admin —
// admin and executive_office included — until the next server restart. Spec
// §7.13's headline is "a new process is a configuration change rather than a
// release"; a configuration change that needs a restart to take effect is a
// release.
//
// So the two writers share this constructor and the role list below, rather
// than the designer growing a second copy that is right on the day it is
// written. What a designer-created type gets is, by construction, what a
// seeded type gets.

// EOTypePermissionAction is one of spec §7.11's three actions together with
// the words the administrator reads in the permissions UI.
type EOTypePermissionAction struct{ Action, Verb, VerbAr string }

// EOTypePermissionActions is §7.11's three actions, in the order the codes are
// written.
var EOTypePermissionActions = []EOTypePermissionAction{
	{EOTypePermissionView, "View", "عرض"},
	{EOTypePermissionCreate, "Create", "إنشاء"},
	{EOTypePermissionUpdate, "Update", "تحديث"},
}

// EOTypePermissionRoleCodes are the roles a new record type's codes are
// granted to. It is ONE list because the seed and the designer must grant the
// same set: a designer that invented its own would make a runtime-created type
// reachable by a different population than a seeded one, which is a
// configuration difference nobody could see in the UI.
//
// Which roles belong here is a question for whoever changes the seed, not for
// a caller: this is read, never parameterised.
var EOTypePermissionRoleCodes = []string{"admin", "executive_office"}

// EOTypePermissionEnforcedEN and EOTypePermissionEnforcedAR are appended to
// every per-type permission's description, in both languages.
//
// WHY THE WORDING IS PART OF THE ROW. The permissions UI shows the
// administrator this Description and nothing else. Through Plan 1 and Plan 2
// the sentence here said the opposite — "NOT YET ENFORCED… granting it
// records an intention and applies no control" — because nothing consulted
// these codes. Task A2 wired the control and the sentence had to go in the
// same change: a box that says it enforces nothing while enforcing something
// is the same defect pointing the other way.
//
// It names BOTH surfaces. An administrator revoking a view code needs to know
// the register narrows, not only that one request is refused.
//
// THE ARABIC WAS WRITTEN BY AN IMPLEMENTER, NOT SUPPLIED BY THE CLIENT, and it
// awaits the spec §9 sign-off round like every other Arabic string in the EO
// seed. docs/superpowers/eo-plan1-carry-forward.md names it so the sign-off
// round can find it.
const (
	EOTypePermissionEnforcedEN = "ENFORCED: a caller who does not hold this code is refused records of this type, and the register lists only the types whose view code they hold."
	EOTypePermissionEnforcedAR = "مُطبَّق: يُرفض وصول من لا يملك هذا الرمز إلى سجلات هذا النوع، ولا يعرض السجل إلا الأنواع التي يملك رمز عرضها."
)

// EOTypePermissionsFor builds the three permission rows a concrete record type
// needs, in the order EOTypePermissionActions lists them.
//
// Permission.Name is uniquely indexed, so it is built from the type KEY —
// which eo_record_types.key already makes unique — and not from the display
// Name, which two types may share. The display name goes in the description,
// where a collision is harmless. The Code is derived from the same key, so the
// uniqueness of the two agrees.
//
// IsActive is deliberately left zero: the column defaults to true, and the
// seed has always relied on that. Setting it here would make a designer-written
// row differ from a seeded one in a column HasPermission reads.
func EOTypePermissionsFor(t EORecordType) []Permission {
	out := make([]Permission, 0, len(EOTypePermissionActions))
	for _, a := range EOTypePermissionActions {
		out = append(out, Permission{
			ID:            uuid.New(),
			Name:          fmt.Sprintf("EO %s %s", a.Verb, t.Key),
			Code:          EOTypePermissionCode(t.Key, a.Action),
			Module:        "eo",
			Action:        fmt.Sprintf("%s:%s", t.Key, a.Action),
			ActionAr:      a.VerbAr,
			Description:   fmt.Sprintf("%s executive-office records of type %s (%s). %s", a.Verb, t.Name, t.Key, EOTypePermissionEnforcedEN),
			DescriptionAr: fmt.Sprintf("%s سجلات المكتب التنفيذي من نوع %s. %s", a.VerbAr, t.NameAr, EOTypePermissionEnforcedAR),
		})
	}
	return out
}
