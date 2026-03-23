package migrations

import "gorm.io/gorm"

// MigrateClassificationTypesArray adds the types text[] column and backfills it from
// the legacy type string column. This is Phase 1 of the dual-column migration strategy.
//
// Mapping:
//   - 'both' → {incident,request}
//   - 'all'  → {incident,request,complaint,query,mobile,ivr}
//   - any single value → {value}
//
// The original type column is preserved as a shadow write target for rollback safety.
// Phase 2 (drop + rename) runs only after the validation window.
func MigrateClassificationTypesArray(db *gorm.DB) error {
	sql := `
	DO $$
	BEGIN
		-- Only run if the classifications table already exists AND still has the legacy
		-- varchar type column. Fresh installs skip this block entirely; AutoMigrate
		-- will create the table with type text[] directly from the model.
		IF EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name  = 'classifications'
			  AND column_name = 'type'
			  AND data_type   = 'character varying'
		) THEN
			-- Add types array column if it doesn't exist
			ALTER TABLE classifications ADD COLUMN IF NOT EXISTS types text[];

			-- Backfill from legacy string column (idempotent: skip rows already set)
			UPDATE classifications SET types = CASE
				WHEN type = 'both' THEN ARRAY['incident', 'request']
				WHEN type = 'all'  THEN ARRAY['incident', 'request', 'complaint', 'query', 'mobile', 'ivr']
				WHEN type IS NOT NULL AND type <> '' THEN ARRAY[type]
				ELSE ARRAY['incident', 'request']
			END
			WHERE types IS NULL OR types = '{}';

			-- Set default and NOT NULL now that every row has a value
			ALTER TABLE classifications ALTER COLUMN types SET DEFAULT ARRAY['incident', 'request'];
			ALTER TABLE classifications ALTER COLUMN types SET NOT NULL;
		END IF;
	END $$;
	`
	return db.Exec(sql).Error
}
