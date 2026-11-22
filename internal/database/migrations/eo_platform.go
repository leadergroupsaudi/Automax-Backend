package migrations

import (
	"fmt"
	"sort"
	"strings"

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
// Every statement is CREATE TABLE/INDEX IF NOT EXISTS, which cannot alter a
// table that already exists — so each create block is followed by an assertion
// that the live table really has the shape declared here (assertColumns, and
// assertPartitionKey for eo_records). Without it, editing a column list in this
// file would be a silent no-op on every database that already has the table,
// and no test would notice, because every test starts from an empty schema.
// Changing a column therefore means an ALTER TABLE migration plus an update to
// the expected column list below.
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
// eo_escalation_events (Task 18) is not created here.
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
	switch relkind {
	case "":
		// Nothing there yet: the statements below create it.
	case "p":
		// It exists and is partitioned. Check it is partitioned the way this
		// file declares before any DDL runs against it.
		if err := assertPartitionKey(db, "eo_records", eoRecordsPartitionKey); err != nil {
			return err
		}
	default:
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
		-- subject_en is nullable: the field matrix marks it Required No,
		-- a secondary display name. subject_ar is the primary one.
		subject_en         VARCHAR(500),
		parent_record_id   UUID,
		state_id           UUID,
		priority           VARCHAR(50) NOT NULL DEFAULT '',
		sensitivity        VARCHAR(50) NOT NULL DEFAULT '',
		source             VARCHAR(100) NOT NULL DEFAULT '',
		owning_body_id     UUID,
		assigned_user_id   UUID,
		due_date           TIMESTAMP WITH TIME ZONE,
		closed_at          TIMESTAMP WITH TIME ZONE,
		-- Every record has a creator: the matrix marks created_by/created_at
		-- Auto, written on every record. The Go field is a value type, so a
		-- nullable column could only ever hold the zero UUID.
		created_by_id      UUID NOT NULL,
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
	if err := assertColumns(db, "eo_records", eoRecordsColumns); err != nil {
		return err
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
	if err := assertColumns(db, "eo_record_links", eoRecordLinksColumns); err != nil {
		return err
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
		-- RESTRICT, not CASCADE: the audit trail must outlive any attempt to
		-- hard-delete a record, and this makes such a delete impossible by
		-- construction. Records are soft-deleted in normal operation.
		CONSTRAINT fk_eo_record_history_record FOREIGN KEY (record_id, record_type_id)
			REFERENCES eo_records (id, record_type_id) ON DELETE RESTRICT
	);

	CREATE INDEX IF NOT EXISTS idx_eo_record_history_record ON eo_record_history (record_id, created_at);
	`
	if err := db.Exec(historySQL).Error; err != nil {
		return fmt.Errorf("eo platform: create eo_record_history: %w", err)
	}
	if err := assertColumns(db, "eo_record_history", eoRecordHistoryColumns); err != nil {
		return err
	}

	// One row per numbering scope (see services.EOSequenceScope). The counter
	// only ever moves forward and is never derived from eo_records, so
	// soft-deleting a record can never free its number for reuse.
	sequencesSQL := `
	CREATE TABLE IF NOT EXISTS eo_reference_sequences (
		scope VARCHAR(500) NOT NULL,
		seq   BIGINT NOT NULL DEFAULT 0,
		CONSTRAINT pk_eo_reference_sequences PRIMARY KEY (scope)
	);
	`
	if err := db.Exec(sequencesSQL).Error; err != nil {
		return fmt.Errorf("eo platform: create eo_reference_sequences: %w", err)
	}
	if err := assertColumns(db, "eo_reference_sequences", eoReferenceSequencesColumns); err != nil {
		return err
	}

	// Durable timers (spec §7.10). No foreign key to eo_records: records are
	// soft-deleted, so a cascade would never fire, and the worker treats a
	// timer whose record is gone as cancelled (see services.EOTimerMonitor).
	// The partial index is what ClaimDue scans; fired and cancelled rows, which
	// only accumulate, are kept out of it.
	timersSQL := `
	CREATE TABLE IF NOT EXISTS eo_timers (
		id             UUID NOT NULL,
		record_id      UUID NOT NULL,
		record_type_id UUID NOT NULL,
		fire_at        TIMESTAMP WITH TIME ZONE NOT NULL,
		capability_key VARCHAR(100) NOT NULL,
		payload        JSONB NOT NULL DEFAULT '{}'::jsonb,
		status         VARCHAR(20) NOT NULL DEFAULT 'pending',
		claimed_at     TIMESTAMP WITH TIME ZONE,
		created_at     TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
		CONSTRAINT pk_eo_timers PRIMARY KEY (id),
		CONSTRAINT chk_eo_timers_status CHECK (status IN ('pending', 'claimed', 'fired', 'cancelled'))
	);

	CREATE INDEX IF NOT EXISTS idx_eo_timers_due ON eo_timers (fire_at) WHERE status IN ('pending', 'claimed');
	CREATE INDEX IF NOT EXISTS idx_eo_timers_record ON eo_timers (record_id);
	`
	if err := db.Exec(timersSQL).Error; err != nil {
		return fmt.Errorf("eo platform: create eo_timers: %w", err)
	}
	if err := assertColumns(db, "eo_timers", eoTimersColumns); err != nil {
		return err
	}

	return nil
}

// eoColumn is one expected column: its name, its information_schema data type,
// its character_maximum_length (0 where the type has none) and whether it is
// nullable.
type eoColumn struct {
	Name     string
	Type     string
	MaxLen   int
	Nullable bool
}

// typeDesc renders the column's type, width and nullability — not its name,
// which the caller already has.
func (c eoColumn) typeDesc() string {
	t := c.Type
	if c.MaxLen > 0 {
		t = fmt.Sprintf("%s(%d)", t, c.MaxLen)
	}
	if !c.Nullable {
		t += " NOT NULL"
	}
	return t
}

// The expected shape of each table, duplicated from the CREATE TABLE
// statements above on purpose: it is a checksum, not documentation.
//
// Every statement above is CREATE TABLE IF NOT EXISTS, so on a database where
// the table already exists, editing a column list up there changes nothing and
// every test — each of which starts from an empty schema — still passes. The
// lists below turn that silent no-op into a startup error. Changing a column
// therefore means writing an ALTER TABLE migration AND updating the list here;
// if the two ever disagree, this refuses to run rather than letting production
// and the file drift apart unnoticed.
// eoRecordsPartitionKey is what pg_get_partkeydef must report for eo_records.
const eoRecordsPartitionKey = "LIST (record_type_id)"

var (
	eoRecordsColumns = []eoColumn{
		{"id", "uuid", 0, false},
		{"record_type_id", "uuid", 0, false},
		{"reference", "character varying", 100, false},
		{"subject_ar", "character varying", 500, false},
		{"subject_en", "character varying", 500, true},
		{"parent_record_id", "uuid", 0, true},
		{"state_id", "uuid", 0, true},
		{"priority", "character varying", 50, false},
		{"sensitivity", "character varying", 50, false},
		{"source", "character varying", 100, false},
		{"owning_body_id", "uuid", 0, true},
		{"assigned_user_id", "uuid", 0, true},
		{"due_date", "timestamp with time zone", 0, true},
		{"closed_at", "timestamp with time zone", 0, true},
		{"created_by_id", "uuid", 0, false},
		{"change_reason", "text", 0, false},
		{"delay_reason", "text", 0, false},
		{"values", "jsonb", 0, false},
		{"created_at", "timestamp with time zone", 0, false},
		{"updated_at", "timestamp with time zone", 0, false},
		{"deleted_at", "timestamp with time zone", 0, true},
	}

	eoRecordLinksColumns = []eoColumn{
		{"id", "uuid", 0, false},
		{"from_record_id", "uuid", 0, false},
		{"from_record_type_id", "uuid", 0, false},
		{"to_record_id", "uuid", 0, false},
		{"to_record_type_id", "uuid", 0, false},
		{"link_type_key", "character varying", 100, false},
		{"created_by_id", "uuid", 0, true},
		{"created_at", "timestamp with time zone", 0, false},
	}

	eoRecordHistoryColumns = []eoColumn{
		{"id", "uuid", 0, false},
		{"record_id", "uuid", 0, false},
		{"record_type_id", "uuid", 0, false},
		{"transition_id", "uuid", 0, true},
		{"from_state_id", "uuid", 0, true},
		{"to_state_id", "uuid", 0, true},
		{"performed_by_id", "uuid", 0, true},
		{"comment", "text", 0, false},
		{"old_values", "jsonb", 0, false},
		{"new_values", "jsonb", 0, false},
		{"is_system_action", "boolean", 0, false},
		{"created_at", "timestamp with time zone", 0, false},
	}

	eoReferenceSequencesColumns = []eoColumn{
		{"scope", "character varying", 500, false},
		{"seq", "bigint", 0, false},
	}

	eoTimersColumns = []eoColumn{
		{"id", "uuid", 0, false},
		{"record_id", "uuid", 0, false},
		{"record_type_id", "uuid", 0, false},
		{"fire_at", "timestamp with time zone", 0, false},
		{"capability_key", "character varying", 100, false},
		{"payload", "jsonb", 0, false},
		{"status", "character varying", 20, false},
		{"claimed_at", "timestamp with time zone", 0, true},
		{"created_at", "timestamp with time zone", 0, false},
	}
)

// assertColumns fails if the live table does not have exactly the expected
// columns, with the expected types, widths and nullability.
func assertColumns(db *gorm.DB, table string, expected []eoColumn) error {
	type liveColumn struct {
		ColumnName string
		DataType   string
		MaxLen     int
		IsNullable string
	}
	var live []liveColumn
	if err := db.Raw(`
		SELECT column_name, data_type, COALESCE(character_maximum_length, 0) AS max_len, is_nullable
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = ?`, table).Scan(&live).Error; err != nil {
		return fmt.Errorf("eo platform: inspect %s columns: %w", table, err)
	}

	byName := make(map[string]eoColumn, len(live))
	for _, c := range live {
		byName[c.ColumnName] = eoColumn{c.ColumnName, c.DataType, c.MaxLen, c.IsNullable == "YES"}
	}

	var problems []string
	for _, want := range expected {
		got, ok := byName[want.Name]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s is missing, expected %s", want.Name, want.typeDesc()))
			continue
		}
		delete(byName, want.Name)
		if got != want {
			problems = append(problems, fmt.Sprintf("%s is %s, expected %s", want.Name, got.typeDesc(), want.typeDesc()))
		}
	}
	for name := range byName {
		problems = append(problems, fmt.Sprintf("%s exists but is not declared in this migration", name))
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf(
		"eo platform: %s does not match the shape this migration declares (%s); CREATE TABLE IF NOT EXISTS cannot alter an existing table, so a column change needs an ALTER TABLE migration and an update to the expected column list in eo_platform.go",
		table, strings.Join(problems, "; "))
}

// assertPartitionKey fails if the live table is not partitioned exactly as
// declared. The relkind guard above only proves it is partitioned somehow.
func assertPartitionKey(db *gorm.DB, table, expected string) error {
	var live string
	if err := db.Raw(`SELECT pg_get_partkeydef(to_regclass(?))`, table).Scan(&live).Error; err != nil {
		return fmt.Errorf("eo platform: inspect %s partition key: %w", table, err)
	}
	if live != expected {
		return fmt.Errorf("eo platform: %s is partitioned by %q, expected %q; repartitioning is a table rewrite and must be done deliberately", table, live, expected)
	}
	return nil
}
