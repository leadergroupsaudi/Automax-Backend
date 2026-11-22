package repository

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// eoTestDB returns a *gorm.DB bound to a freshly created, uniquely named
// Postgres schema that is dropped when the test ends. It skips the test unless
// AUTOMAX_TEST_DB_DSN is set.
//
// It deliberately opens the DSN directly: database.Connect would assign the
// database.DB package global and database.Migrate would build the whole
// application schema, neither of which a test may do to a shared database.
//
// This helper is self-contained so it can be copied verbatim into another
// package's tests.
func eoTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("AUTOMAX_TEST_DB_DSN")
	if dsn == "" {
		t.Skip("set AUTOMAX_TEST_DB_DSN to run DB tests")
	}

	// Pin the pool to one connection so the session-level search_path set
	// below applies to every statement the test issues.
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("get sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("random schema suffix: %v", err)
	}
	schema := "eo_test_" + hex.EncodeToString(suffix)

	t.Cleanup(func() {
		if err := db.Exec(`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`).Error; err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
		sqlDB.Close()
	})

	if err := db.Exec(`CREATE SCHEMA "` + schema + `"`).Error; err != nil {
		t.Fatalf("create schema %s: %v", schema, err)
	}
	if err := db.Exec(`SET search_path TO "` + schema + `"`).Error; err != nil {
		t.Fatalf("set search_path: %v", err)
	}
	return db
}

func TestEoTestDBIsolatesSchema(t *testing.T) {
	t.Run("creates probe", func(t *testing.T) {
		db := eoTestDB(t)
		if err := db.Exec(`CREATE TABLE probe (id int)`).Error; err != nil {
			t.Fatalf("create probe: %v", err)
		}
		if !db.Migrator().HasTable("probe") {
			t.Errorf("HasTable(probe) = false, want true in the schema that created it")
		}
	})
	t.Run("cannot see probe", func(t *testing.T) {
		db := eoTestDB(t)
		if db.Migrator().HasTable("probe") {
			t.Errorf("HasTable(probe) = true, want false in a fresh schema")
		}
	})
}
