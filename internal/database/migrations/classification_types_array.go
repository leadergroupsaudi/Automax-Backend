package migrations

import "gorm.io/gorm"

// MigrateClassificationTypesArray migrates classifications.type from varchar to text[].
//
// When the legacy varchar column is detected it:
//  1. Adds a temp text[] column (_type_new) and backfills from the old values
//  2. Drops the old varchar column (removes its default/constraints)
//  3. Renames _type_new to "type"
//
// This runs before AutoMigrate so GORM never sees a varchar "type" column
// and never attempts an in-place ALTER that PostgreSQL rejects.
//
// Fresh installs: table doesn't exist yet, block is skipped, AutoMigrate
// creates the table with type text[] directly from the model.
//
// Idempotent: all steps are guarded by IF EXISTS / IF NOT EXISTS checks.
//
// Mapping:
//
//	'both' → {incident,request}
//	'all'  → {incident,request,complaint,query,mobile,ivr}
//	any single value → {value}
func MigrateClassificationTypesArray(db *gorm.DB) error {
	sql := `
	DO $$
	BEGIN
		-- Only run if the classifications table exists AND type is still varchar.
		IF EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name  = 'classifications'
			  AND column_name = 'type'
			  AND data_type   = 'character varying'
		) THEN
			-- Step 1: add a temporary array column
			ALTER TABLE classifications ADD COLUMN IF NOT EXISTS _type_new text[];

			-- Step 2: backfill from the old varchar column
			UPDATE classifications SET _type_new = CASE
				WHEN type = 'both' THEN ARRAY['incident', 'request']
				WHEN type = 'all'  THEN ARRAY['incident', 'request', 'complaint', 'query', 'mobile', 'ivr']
				WHEN type IS NOT NULL AND type <> '' THEN ARRAY[type]
				ELSE ARRAY['incident', 'request']
			END
			WHERE _type_new IS NULL OR _type_new = '{}';

			-- Step 3: apply default + NOT NULL to the new column
			ALTER TABLE classifications ALTER COLUMN _type_new SET DEFAULT ARRAY['incident', 'request'];
			ALTER TABLE classifications ALTER COLUMN _type_new SET NOT NULL;

			-- Step 4: drop the old varchar column (removes its default too)
			ALTER TABLE classifications DROP COLUMN type;

			-- Step 5: rename temp column to the canonical name
			ALTER TABLE classifications RENAME COLUMN _type_new TO type;
		END IF;
	END $$;
	`
	return db.Exec(sql).Error
}
