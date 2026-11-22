package migrations

import (
	"fmt"

	"gorm.io/gorm"
)

// MigrateEOPlatform creates the EO platform record storage: the LIST-partitioned
// eo_records table with its DEFAULT partition and indexes, eo_record_links and
// eo_record_history. It is idempotent and safe to run on every boot.
//
// None of this can be expressed by AutoMigrate, which is why models.EORecord is
// absent from the AutoMigrate list in postgres.go:
//   - eo_records is PARTITION BY LIST (record_type_id). Postgres requires the
//     partition key in the primary key and in every unique constraint, so the
//     primary key is composite, (id, record_type_id), and reference is unique
//     per partition only — UNIQUE (record_type_id, reference). Global
//     uniqueness rests on type reference prefixes not overlapping, which
//     models.ValidateReferencePrefix enforces at type creation (spec §7.7).
//   - a DEFAULT partition catches every record type that has no partition of
//     its own, so a type created in the designer works immediately. A tooling
//     command emits the migration that gives it its own partition later.
//   - eo_record_links and eo_record_history carry both the record id and the
//     record type id of each endpoint, because a foreign key referencing a
//     partitioned table must name the partition key (spec §7.6). AutoMigrate
//     writes no foreign keys at all (DisableForeignKeyConstraintWhenMigrating),
//     so these are written by hand.
//
// CREATE INDEX CONCURRENTLY is not used: Postgres rejects it on a partitioned
// table, and it cannot run inside a transaction, which the house migration
// style requires (spec §7.7).
//
// Foreign keys to users, departments and workflow_states are deliberately not
// created, matching the rest of this codebase, which disables them during
// migration. The only hand-written foreign keys are the ones inside the EO
// record family, where the composite shape is the design point being enforced.
//
// eo_timers (Task 17) and eo_escalation_events (Task 18) are not created here.
func MigrateEOPlatform(db *gorm.DB) error {
	// A plain eo_records table cannot be upgraded in place: catch it loudly
	// rather than letting CREATE TABLE IF NOT EXISTS silently accept it and
	// every assumption below be wrong.
	var relkind string
	if err := db.Raw(
		`SELECT c.relkind FROM pg_class c WHERE c.oid = to_regclass('eo_records')`,
	).Scan(&relkind).Error; err != nil {
		return fmt.Errorf("eo platform: inspect eo_records: %w", err)
	}
	if relkind != "" && relkind != "p" {
		return fmt.Errorf(
			"eo platform: eo_records exists but is not partitioned (relkind %q); it was most likely created by AutoMigrate — models.EORecord must stay out of the AutoMigrate list, and the existing table has to be dropped or migrated by hand",
			relkind)
	}

	recordsSQL := `
	-- The record table itself: one table for every configured type, LIST
	-- partitioned on the type. The 17 inherited fields are real columns
	-- because every register screen filters, joins and sorts on them; the
	-- type-specific tail lives in "values" (spec §7.7). "values" is a
	-- reserved word, hence the quoting.
	CREATE TABLE IF NOT EXISTS eo_records (
		id                 UUID NOT NULL,
		record_type_id     UUID NOT NULL,
		reference          VARCHAR(100) NOT NULL,
		subject_ar         VARCHAR(500) NOT NULL DEFAULT '',
		subject_en         VARCHAR(500) NOT NULL DEFAULT '',
		parent_record_id   UUID,
		state_id           UUID,
		priority           VARCHAR(50) NOT NULL DEFAULT '',
		sensitivity        VARCHAR(50) NOT NULL DEFAULT '',
		source             VARCHAR(100) NOT NULL DEFAULT '',
		owning_body_id     UUID,
		assigned_user_id   UUID,
		due_date           TIMESTAMP WITH TIME ZONE,
		closed_at          TIMESTAMP WITH TIME ZONE,
		created_by_id      UUID,
		change_reason      TEXT NOT NULL DEFAULT '',
		delay_reason       TEXT NOT NULL DEFAULT '',
		"values"           JSONB NOT NULL DEFAULT '{}'::jsonb,
		created_at         TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
		updated_at         TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
		deleted_at         TIMESTAMP WITH TIME ZONE,
		CONSTRAINT pk_eo_records PRIMARY KEY (id, record_type_id),
		CONSTRAINT uq_eo_records_type_reference UNIQUE (record_type_id, reference)
	) PARTITION BY LIST (record_type_id);

	-- Every record type without a partition of its own lands here and is
	-- fully functional; without it an unpartitioned type could not insert.
	CREATE TABLE IF NOT EXISTS eo_records_default PARTITION OF eo_records DEFAULT;

	-- Indexes declared on the parent cascade to every partition, present and
	-- future, so a new type's partition is indexed the moment it is created.
	CREATE INDEX IF NOT EXISTS idx_eo_records_values_gin ON eo_records USING GIN ("values");
	CREATE INDEX IF NOT EXISTS idx_eo_records_type_state ON eo_records (record_type_id, state_id);
	-- Lookup by reference alone: the UNIQUE constraint's index leads with
	-- record_type_id, so it cannot serve "find the record someone quoted".
	CREATE INDEX IF NOT EXISTS idx_eo_records_reference ON eo_records (reference);
	CREATE INDEX IF NOT EXISTS idx_eo_records_parent_record_id ON eo_records (parent_record_id);
	CREATE INDEX IF NOT EXISTS idx_eo_records_assigned_user_id ON eo_records (assigned_user_id);
	CREATE INDEX IF NOT EXISTS idx_eo_records_due_date ON eo_records (due_date);
	CREATE INDEX IF NOT EXISTS idx_eo_records_deleted_at ON eo_records (deleted_at);
	`
	if err := db.Exec(recordsSQL).Error; err != nil {
		return fmt.Errorf("eo platform: create eo_records: %w", err)
	}

	linksSQL := `
	-- Typed relationships between records (spec §7.6). Both endpoints store
	-- the record type because the foreign key must name the partition key;
	-- the link resolver reads the type from the row instead of looking it up.
	CREATE TABLE IF NOT EXISTS eo_record_links (
		id                  UUID NOT NULL,
		from_record_id      UUID NOT NULL,
		from_record_type_id UUID NOT NULL,
		to_record_id        UUID NOT NULL,
		to_record_type_id   UUID NOT NULL,
		link_type_key       VARCHAR(100) NOT NULL,
		created_by_id       UUID,
		created_at          TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
		CONSTRAINT pk_eo_record_links PRIMARY KEY (id),
		CONSTRAINT fk_eo_record_links_from FOREIGN KEY (from_record_id, from_record_type_id)
			REFERENCES eo_records (id, record_type_id) ON DELETE CASCADE,
		CONSTRAINT fk_eo_record_links_to FOREIGN KEY (to_record_id, to_record_type_id)
			REFERENCES eo_records (id, record_type_id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_eo_record_links_from ON eo_record_links (from_record_id);
	CREATE INDEX IF NOT EXISTS idx_eo_record_links_to ON eo_record_links (to_record_id);
	CREATE INDEX IF NOT EXISTS idx_eo_record_links_type_key ON eo_record_links (link_type_key);
	`
	if err := db.Exec(linksSQL).Error; err != nil {
		return fmt.Errorf("eo platform: create eo_record_links: %w", err)
	}

	historySQL := `
	-- The per-record audit trail: transitions and the field changes that came
	-- with them.
	CREATE TABLE IF NOT EXISTS eo_record_history (
		id               UUID NOT NULL,
		record_id        UUID NOT NULL,
		record_type_id   UUID NOT NULL,
		transition_id    UUID,
		from_state_id    UUID,
		to_state_id      UUID,
		performed_by_id  UUID,
		comment          TEXT NOT NULL DEFAULT '',
		old_values       JSONB NOT NULL DEFAULT '{}'::jsonb,
		new_values       JSONB NOT NULL DEFAULT '{}'::jsonb,
		is_system_action BOOLEAN NOT NULL DEFAULT FALSE,
		created_at       TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
		CONSTRAINT pk_eo_record_history PRIMARY KEY (id),
		CONSTRAINT fk_eo_record_history_record FOREIGN KEY (record_id, record_type_id)
			REFERENCES eo_records (id, record_type_id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_eo_record_history_record ON eo_record_history (record_id, created_at);
	`
	if err := db.Exec(historySQL).Error; err != nil {
		return fmt.Errorf("eo platform: create eo_record_history: %w", err)
	}

	return nil
}
