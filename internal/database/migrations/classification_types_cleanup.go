package migrations

import "gorm.io/gorm"

// MigrateClassificationTypesCleanup is Phase 2 of the dual-column migration.
// It drops the legacy type varchar column and renames types text[] → type.
//
// Prerequisites:
//   - MigrateClassificationTypesArray (Phase 1) must have run and been validated.
//   - Frontend must no longer read the old "type" string field from API responses.
//
// This migration is idempotent: IF NOT EXISTS / IF EXISTS guards prevent errors on re-runs.
func MigrateClassificationTypesCleanup(db *gorm.DB) error {
	sql := `
	-- Drop the legacy string column (was the shadow write target during Phase 1)
	ALTER TABLE classifications DROP COLUMN IF EXISTS type;

	-- Rename the array column to take the canonical name
	DO $$
	BEGIN
		IF EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = 'classifications' AND column_name = 'types'
		) THEN
			ALTER TABLE classifications RENAME COLUMN types TO type;
		END IF;
	END $$;
	`
	return db.Exec(sql).Error
}
