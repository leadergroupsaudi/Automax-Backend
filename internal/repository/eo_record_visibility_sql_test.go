package repository

import (
	"strings"
	"testing"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// These tests need NO database. GORM builds the statement without running it
// (ToSQL runs the callback in a DryRun session), so what applyEORecordFilters
// turns each caller shape into is asserted on the generated SQL. They are the
// only executable check of the visibility predicate on a machine without
// AUTOMAX_TEST_DB_DSN. What they do NOT prove: that Postgres evaluates the
// predicate as intended against real rows — the DSN-gated list tests cover
// that and are skipped here.
func eoDryRunDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN: "host=127.0.0.1 port=1 user=none dbname=none sslmode=disable",
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("open dry-run db: %v", err)
	}
	return db
}

// eoSQLFor is the SQL a register list becomes for the filter.
func eoSQLFor(t *testing.T, f models.EORecordFilter) string {
	t.Helper()
	db := eoDryRunDB(t)
	sql := db.ToSQL(func(tx *gorm.DB) *gorm.DB {
		q, err := applyEORecordFilters(tx.Model(&models.EORecord{}), &f)
		if err != nil {
			t.Fatalf("applyEORecordFilters: %v", err)
		}
		return q.Find(&[]models.EORecord{})
	})
	if sql == "" {
		t.Fatal("no SQL was generated")
	}
	return sql
}

const eoVisibleToExpr = `eo_records."values"->>'visible_to'`

func TestApplyEORecordVisibility_OversightSeesOtherBodiesRecords(t *testing.T) {
	sql := eoSQLFor(t, models.EORecordFilter{
		UserID:          uuid.New(),
		PermissionCodes: []string{"eo:eo_referral:view", models.EOPermissionViewAllBodies},
		ViewAllBodies:   true,
		// no bodies: the oversight caller belongs to none
	})
	if strings.Contains(sql, eoVisibleToExpr) {
		t.Errorf("an oversight caller is still restricted by visible_to:\n%s", sql)
	}
	// Still only the type gate and the soft-delete guard: nothing about bodies.
	if strings.Contains(sql, "created_by_id") {
		t.Errorf("the oversight clause carries a body/creator arm:\n%s", sql)
	}
}

func TestApplyEORecordVisibility_WithoutOversightIsUnchanged(t *testing.T) {
	user, body := uuid.New(), uuid.New()
	sql := eoSQLFor(t, models.EORecordFilter{
		UserID:                 user,
		VisibleToDepartmentIDs: []uuid.UUID{body},
		PermissionCodes:        []string{"eo:eo_referral:view"},
	})
	// Unrestricted records, its own creations, its own bodies — exactly these.
	want := `(` + eoVisibleToExpr + ` IS NULL OR eo_records.created_by_id = '` + user.String() +
		`' OR ` + eoVisibleToExpr + ` IN ('` + body.String() + `'))`
	if !strings.Contains(sql, want) {
		t.Errorf("the restricted caller's clause changed.\nwant fragment: %s\ngot: %s", want, sql)
	}

	// No user, no bodies: unrestricted records only.
	sql = eoSQLFor(t, models.EORecordFilter{PermissionCodes: []string{"eo:eo_referral:view"}})
	want = `(` + eoVisibleToExpr + ` IS NULL)`
	if !strings.Contains(sql, want) {
		t.Errorf("a caller with no bodies and no user is not limited to unrestricted records.\nwant fragment: %s\ngot: %s", want, sql)
	}
}

// An admin's and a creator's views are untouched by the flag: the admin still
// bypasses outright, and a creator without the flag still gets the
// created_by_id arm.
func TestApplyEORecordVisibility_AdminAndCreatorUnchanged(t *testing.T) {
	sql := eoSQLFor(t, models.EORecordFilter{IsAdmin: true})
	if strings.Contains(sql, eoVisibleToExpr) || strings.Contains(sql, "eo_record_types") {
		t.Errorf("admin is restricted:\n%s", sql)
	}
	user := uuid.New()
	sql = eoSQLFor(t, models.EORecordFilter{UserID: user, PermissionCodes: []string{"eo:eo_referral:view"}})
	if !strings.Contains(sql, "eo_records.created_by_id = '"+user.String()+"'") {
		t.Errorf("the creator arm is gone:\n%s", sql)
	}
}

// THE COMPOSITION. The flag lifts the body restriction and nothing else: a
// caller holding the oversight permission and no per-type view code for any
// register is still limited to an EMPTY type set, and one holding a code for
// one register is limited to that register.
func TestApplyEORecordVisibility_OversightDoesNotGrantARegister(t *testing.T) {
	sql := eoSQLFor(t, models.EORecordFilter{
		UserID:          uuid.New(),
		PermissionCodes: []string{models.EOPermissionViewAllBodies},
		ViewAllBodies:   true,
	})
	// An empty type set renders as key IN (NULL): matches no type, fails closed.
	if !strings.Contains(sql, `eo_records.record_type_id IN (SELECT id FROM "eo_record_types" WHERE key IN (NULL))`) {
		t.Fatalf("an oversight caller with no view code is not held to an empty register:\n%s", sql)
	}

	sql = eoSQLFor(t, models.EORecordFilter{
		PermissionCodes: []string{models.EOPermissionViewAllBodies, "eo:eo_directive:view"},
		ViewAllBodies:   true,
	})
	if !strings.Contains(sql, "key IN ('eo_directive')") {
		t.Errorf("the type set is not exactly the held view codes:\n%s", sql)
	}
	if strings.Contains(sql, "eo_referral") {
		t.Errorf("a register without a view code is reachable:\n%s", sql)
	}
}

// The oversight code must never read as a type key.
func TestEOPermissionViewAllBodiesIsNotAPerTypeCode(t *testing.T) {
	for _, a := range []string{"view", "create", "update"} {
		if k := models.EOTypeKeysFromPermissionCodes([]string{models.EOPermissionViewAllBodies}, a); len(k) != 0 {
			t.Errorf("%s read as a per-type %s grant on %v", models.EOPermissionViewAllBodies, a, k)
		}
	}
}
