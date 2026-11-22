package repository

import (
	"strings"
	"testing"

	"github.com/automax/backend/internal/database/migrations"
)

// eoPreflightFinding is one row of migrations.EOPreflightSQL.
type eoPreflightFinding struct {
	TableName  string
	ColumnName string
	Problem    string
}

// TestEOPreflightSQLAgreesWithTheBootAssertion is the test the deploy note
// rests on. The published pre-deploy check must answer the SAME question
// assertColumns answers at boot — and must answer it for all six tables,
// because since C1 every one of them stops the server.
//
// Mutating any single expected column in eo_platform.go makes the preflight
// report that column AND makes MigrateEOPlatform refuse; the two are rendered
// from the same slices, which is the property under test. Dropping a table
// from eoAssertedTables makes the first subtest fail.
func TestEOPreflightSQLAgreesWithTheBootAssertion(t *testing.T) {
	t.Run("it covers every table the migration asserts", func(t *testing.T) {
		sql := migrations.EOPreflightSQL()
		want := migrations.EOAssertedTableNames()
		if len(want) != 6 {
			t.Fatalf("the migration asserts %d tables, the deploy note documents 6: %v", len(want), want)
		}
		for _, table := range want {
			if !strings.Contains(sql, "'"+table+"'") {
				t.Errorf("the preflight query does not mention %s, which assertColumns gates the boot on", table)
			}
		}
	})

	t.Run("a freshly migrated database reports nothing", func(t *testing.T) {
		db := eoMigratedDB(t)
		var findings []eoPreflightFinding
		if err := db.Raw(migrations.EOPreflightSQL()).Scan(&findings).Error; err != nil {
			t.Fatalf("run the preflight query: %v", err)
		}
		if len(findings) != 0 {
			t.Errorf("preflight reported %d problems on a database this migration just created: %+v", len(findings), findings)
		}
	})

	t.Run("it finds the drift the boot assertion would refuse", func(t *testing.T) {
		db := eoMigratedDB(t)
		// Exactly the shape of a box that ran an intermediate commit: the
		// table is already there, so CREATE TABLE IF NOT EXISTS is a no-op
		// and only assertColumns notices.
		if err := db.Exec(`ALTER TABLE eo_timers ALTER COLUMN capability_key TYPE VARCHAR(40)`).Error; err != nil {
			t.Fatalf("introduce the drift: %v", err)
		}
		if err := db.Exec(`ALTER TABLE eo_escalation_events ADD COLUMN stray_column INTEGER`).Error; err != nil {
			t.Fatalf("introduce the second drift: %v", err)
		}

		var findings []eoPreflightFinding
		if err := db.Raw(migrations.EOPreflightSQL()).Scan(&findings).Error; err != nil {
			t.Fatalf("run the preflight query: %v", err)
		}
		got := map[string]string{}
		for _, f := range findings {
			got[f.TableName+"."+f.ColumnName] = f.Problem
		}
		if _, ok := got["eo_timers.capability_key"]; !ok {
			t.Errorf("preflight missed the narrowed column; findings = %+v", findings)
		}
		if _, ok := got["eo_escalation_events.stray_column"]; !ok {
			t.Errorf("preflight missed the undeclared column; findings = %+v", findings)
		}
		if len(findings) != 2 {
			t.Errorf("preflight reported %d problems, want exactly the 2 introduced: %+v", len(findings), findings)
		}

		// And the boot assertion refuses the same database, which is what
		// makes the preflight a PREDICTION rather than a second opinion.
		if err := migrations.MigrateEOPlatform(db); err == nil {
			t.Error("MigrateEOPlatform accepted a drifted schema, want an error")
		}
	})

	t.Run("a database with no EO tables at all reports nothing", func(t *testing.T) {
		// A fresh deployment: the migration creates everything, so a wall of
		// "missing column" rows would be noise that teaches an operator to
		// ignore the check.
		db := eoTestDB(t)
		var findings []eoPreflightFinding
		if err := db.Raw(migrations.EOPreflightSQL()).Scan(&findings).Error; err != nil {
			t.Fatalf("run the preflight query: %v", err)
		}
		if len(findings) != 0 {
			t.Errorf("preflight reported %d problems on an empty schema: %+v", len(findings), findings)
		}
	})
}
