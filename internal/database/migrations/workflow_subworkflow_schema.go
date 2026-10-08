package migrations

import "gorm.io/gorm"

// MigrateWorkflowSubworkflowSchema adds the Phase 1 sub-workflow columns.
// Idempotent. Existing rows keep their data and receive the documented defaults:
// target_workflow_id NULL, is_return_transition false, workflow_stack '[]'.
// AutoMigrate does not create the FK because foreign keys are disabled during migrate.
func MigrateWorkflowSubworkflowSchema(db *gorm.DB) error {
	migrationSQL := `
	ALTER TABLE workflow_transitions
		ADD COLUMN IF NOT EXISTS target_workflow_id UUID;

	ALTER TABLE workflow_transitions
		ADD COLUMN IF NOT EXISTS is_return_transition BOOLEAN NOT NULL DEFAULT false;

	UPDATE workflow_transitions
	SET is_return_transition = false
	WHERE is_return_transition IS NULL;

	ALTER TABLE incidents
		ADD COLUMN IF NOT EXISTS workflow_stack TEXT NOT NULL DEFAULT '[]';

	UPDATE incidents
	SET workflow_stack = '[]'
	WHERE workflow_stack IS NULL OR btrim(workflow_stack) = '';

	CREATE INDEX IF NOT EXISTS idx_workflow_transitions_target_workflow_id
		ON workflow_transitions (target_workflow_id);

	DO $$
	BEGIN
		IF NOT EXISTS (
			SELECT 1 FROM pg_constraint WHERE conname = 'fk_workflow_transitions_target_workflow'
		) THEN
			ALTER TABLE workflow_transitions
				ADD CONSTRAINT fk_workflow_transitions_target_workflow
				FOREIGN KEY (target_workflow_id) REFERENCES workflows(id);
		END IF;
	END $$;
	`
	return db.Exec(migrationSQL).Error
}
