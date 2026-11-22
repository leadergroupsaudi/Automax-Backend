// Command eo-preflight checks, BEFORE a deployment, whether the EO platform
// migration will refuse to run on a given database.
//
// Usage:
//
//	go run ./cmd/eo-preflight                      # print the check as SQL
//	go run ./cmd/eo-preflight | psql "$DSN"        # ... and run it with psql
//	go run ./cmd/eo-preflight -dsn "$DSN"          # run it directly and report
//
// Exit status with -dsn: 0 when the database is clear, 1 when the deployment
// would fail. Without -dsn it only prints and always exits 0.
//
// Why this exists: migrations.MigrateEOPlatform asserts the shape of six
// tables, and its error now stops the server (internal/database/postgres.go).
// That is deliberate — the assertions are what keep production and the
// migration file from drifting unnoticed — but it means the only way to
// discover a mismatch used to be a boot that refused. On a box that ran an
// intermediate commit of this branch, where a column changed mid-branch and
// the table already exists, CREATE TABLE IF NOT EXISTS is a no-op and only
// the assertion notices. This runs the same comparison without starting
// anything.
//
// Everything it issues is READ-ONLY: one SELECT over information_schema.
// It writes nothing and locks nothing, so it is safe against production.
//
// See docs/eo-platform-deploying.md.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/automax/backend/internal/database/migrations"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// finding is one row of migrations.EOPreflightSQL.
type finding struct {
	TableName  string
	ColumnName string
	Problem    string
}

func main() {
	dsn := flag.String("dsn", "", "Postgres DSN to run the check against. Omitted: print the SQL and exit.")
	flag.Parse()

	sql := migrations.EOPreflightSQL()
	if strings.TrimSpace(*dsn) == "" {
		fmt.Println(sql)
		return
	}

	db, err := gorm.Open(postgres.Open(*dsn), &gorm.Config{
		// A failed check is this program's output, not a line of GORM error
		// log noise above it.
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "eo-preflight: connect: %v\n", err)
		os.Exit(2)
	}
	sqlDB, err := db.DB()
	if err != nil {
		fmt.Fprintf(os.Stderr, "eo-preflight: %v\n", err)
		os.Exit(2)
	}
	defer sqlDB.Close()

	var findings []finding
	if err := db.Raw(sql).Scan(&findings).Error; err != nil {
		fmt.Fprintf(os.Stderr, "eo-preflight: run the check: %v\n", err)
		os.Exit(2)
	}

	tables := migrations.EOAssertedTableNames()
	if len(findings) == 0 {
		fmt.Printf("eo-preflight: OK — %s match this build; no column assertion will fail at boot.\n",
			strings.Join(tables, ", "))
		fmt.Println("eo-preflight: this covers the column assertions ONLY. See docs/eo-platform-deploying.md for the")
		fmt.Println("              partitioning and duplicate-link checks, which are separate queries.")
		return
	}

	fmt.Fprintf(os.Stderr, "eo-preflight: %d column mismatch(es). MigrateEOPlatform will FAIL and the server will not start:\n", len(findings))
	for _, f := range findings {
		fmt.Fprintf(os.Stderr, "  %s.%s %s\n", f.TableName, f.ColumnName, f.Problem)
	}
	fmt.Fprintln(os.Stderr, "Each needs an ALTER TABLE migration, or an update to the expected column list in")
	fmt.Fprintln(os.Stderr, "internal/database/migrations/eo_platform.go — whichever of the two is the mistake.")
	os.Exit(1)
}
