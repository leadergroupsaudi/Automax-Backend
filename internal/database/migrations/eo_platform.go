package migrations

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
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
//     THAT COMMAND DOES NOT EXIST IN PLAN 1: no per-type partition is ever
//     created and every record of every type lives in eo_records_default.
//     That is fully functional, but promotion is a ONE-WAY DOOR — creating a
//     partition makes Postgres take an ACCESS EXCLUSIVE lock on the DEFAULT
//     partition and full-scan it to prove no row would belong to the new one,
//     so promotion is a maintenance operation on a populated register even for
//     a type that has no records, and a type that HAS records needs its rows
//     moved on top of that. Plan 2 must create each type's partition BEFORE
//     its first record. See spec §7.7 and
//     models.EORecordType.PartitionPending.
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
		-- The record's Documenta folder; '' means it has none (see ALTER below).
		documenta_folder_id VARCHAR(100) NOT NULL DEFAULT '',
		created_at         TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
		updated_at         TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
		deleted_at         TIMESTAMP WITH TIME ZONE,
		CONSTRAINT pk_eo_records PRIMARY KEY (id, record_type_id),
		CONSTRAINT uq_eo_records_type_reference UNIQUE (record_type_id, reference)
	) PARTITION BY LIST (record_type_id);

	-- CREATE TABLE IF NOT EXISTS cannot add a column to a table that already
	-- exists, so the column above reaches an existing database only through
	-- this. Existing rows get '' (no folder), which is a legible state. The
	-- column must also be in eoRecordsColumns or the assertion refuses it.
	ALTER TABLE eo_records ADD COLUMN IF NOT EXISTS documenta_folder_id VARCHAR(100) NOT NULL DEFAULT '';

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
	if err := assertTable(db, "eo_records"); err != nil {
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

	-- One link of a given type between a given ordered pair, enforced by the
	-- DATABASE. services.ValidateLink's duplicate check is application-level
	-- and runs outside any transaction, so two concurrent POST /:id/links both
	-- read "not linked yet" and both insert. Uniqueness here is a property of
	-- the data, not of the handler that happens to write it.
	--
	-- A UNIQUE INDEX rather than a table constraint, because the CREATE TABLE
	-- above is IF NOT EXISTS and so cannot add a constraint to a table that
	-- already exists, while CREATE UNIQUE INDEX IF NOT EXISTS is idempotent on
	-- a new and an existing database alike. It adds no column, so
	-- eoRecordLinksColumns is unchanged. On a database that already holds
	-- duplicate links this statement FAILS — and with MigrateEOPlatform's
	-- error now returned by database.Migrate, the server will not start until
	-- they are removed; see docs/eo-platform-deploying.md.
	CREATE UNIQUE INDEX IF NOT EXISTS uq_eo_record_links_pair_type
		ON eo_record_links (from_record_id, to_record_id, link_type_key);
	`
	if err := db.Exec(linksSQL).Error; err != nil {
		return fmt.Errorf("eo platform: create eo_record_links: %w", err)
	}
	if err := assertTable(db, "eo_record_links"); err != nil {
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
	if err := assertTable(db, "eo_record_history"); err != nil {
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
	if err := assertTable(db, "eo_reference_sequences"); err != nil {
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
		attempts       INTEGER NOT NULL DEFAULT 0,
		last_error     TEXT NOT NULL DEFAULT '',
		created_at     TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
		CONSTRAINT pk_eo_timers PRIMARY KEY (id),
		CONSTRAINT chk_eo_timers_status CHECK (status IN ('pending', 'claimed', 'fired', 'cancelled', 'failed'))
	);

	CREATE INDEX IF NOT EXISTS idx_eo_timers_due ON eo_timers (fire_at) WHERE status IN ('pending', 'claimed');
	CREATE INDEX IF NOT EXISTS idx_eo_timers_record ON eo_timers (record_id);
	`
	if err := db.Exec(timersSQL).Error; err != nil {
		return fmt.Errorf("eo platform: create eo_timers: %w", err)
	}
	if err := assertTable(db, "eo_timers"); err != nil {
		return err
	}

	// EO's own breach log (spec §7.8). The live one, escalation_slas, is typed
	// on incident_id with a foreign key to incidents, and is read by the
	// incident product today; it is not touched.
	//
	// The unique index is the once-per-step guarantee. breached_at is the fire_at
	// of the timer that produced the event, so a timer delivered twice
	// (at-least-once) collides with itself, while a record that re-enters the
	// state and breaches again gets a new fire_at and so a new event.
	escalationSQL := `
	CREATE TABLE IF NOT EXISTS eo_escalation_events (
		id             UUID NOT NULL,
		record_id      UUID NOT NULL,
		record_type_id UUID NOT NULL,
		state_id       UUID NOT NULL,
		policy_id      UUID NOT NULL,
		step_index     INTEGER NOT NULL,
		breached_at    TIMESTAMP WITH TIME ZONE NOT NULL,
		notified_at    TIMESTAMP WITH TIME ZONE,
		created_at     TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
		CONSTRAINT pk_eo_escalation_events PRIMARY KEY (id)
	);

	CREATE UNIQUE INDEX IF NOT EXISTS uq_eo_escalation_events_step
		ON eo_escalation_events (record_id, state_id, policy_id, step_index, breached_at);
	CREATE INDEX IF NOT EXISTS idx_eo_escalation_events_record ON eo_escalation_events (record_id);
	`
	if err := db.Exec(escalationSQL).Error; err != nil {
		return fmt.Errorf("eo platform: create eo_escalation_events: %w", err)
	}
	if err := assertTable(db, "eo_escalation_events"); err != nil {
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
		{"source", "character varying", 100, true},
		{"owning_body_id", "uuid", 0, true},
		{"assigned_user_id", "uuid", 0, true},
		{"due_date", "timestamp with time zone", 0, true},
		{"closed_at", "timestamp with time zone", 0, true},
		{"created_by_id", "uuid", 0, false},
		{"change_reason", "text", 0, false},
		{"delay_reason", "text", 0, false},
		{"values", "jsonb", 0, false},
		{"documenta_folder_id", "character varying", 100, false},
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
		{"attempts", "integer", 0, false},
		{"last_error", "text", 0, false},
		{"created_at", "timestamp with time zone", 0, false},
	}

	eoEscalationEventsColumns = []eoColumn{
		{"id", "uuid", 0, false},
		{"record_id", "uuid", 0, false},
		{"record_type_id", "uuid", 0, false},
		{"state_id", "uuid", 0, false},
		{"policy_id", "uuid", 0, false},
		{"step_index", "integer", 0, false},
		{"breached_at", "timestamp with time zone", 0, false},
		{"notified_at", "timestamp with time zone", 0, true},
		{"created_at", "timestamp with time zone", 0, false},
	}
)

// eoAssertedTables is EVERY table MigrateEOPlatform creates and then checks
// the shape of, paired with the shape it declares. Since C1 made this
// migration's error fail the boot, each of these six is a condition that stops
// the server, so the set has to be addressable from outside: assertTable
// (boot) and EOPreflightSQL (pre-deploy) both read it, and the published
// pre-deploy check therefore cannot claim fewer tables than the migration
// actually gates on. A hand-copied list in a document would be a third place
// to drift, which is the defect this whole file exists to prevent.
var eoAssertedTables = []struct {
	Name    string
	Columns []eoColumn
}{
	{"eo_records", eoRecordsColumns},
	{"eo_record_links", eoRecordLinksColumns},
	{"eo_record_history", eoRecordHistoryColumns},
	{"eo_reference_sequences", eoReferenceSequencesColumns},
	{"eo_timers", eoTimersColumns},
	{"eo_escalation_events", eoEscalationEventsColumns},
}

// EOAssertedTableNames is the six table names, for a caller that wants to
// name them (the deploy note, a test).
func EOAssertedTableNames() []string {
	out := make([]string, 0, len(eoAssertedTables))
	for _, t := range eoAssertedTables {
		out = append(out, t.Name)
	}
	return out
}

// assertTable checks one table against the shape eoAssertedTables declares for
// it. A table asserted here but absent from that slice is an error rather than
// a silently skipped check.
func assertTable(db *gorm.DB, table string) error {
	for _, t := range eoAssertedTables {
		if t.Name == table {
			return assertColumns(db, t.Name, t.Columns)
		}
	}
	return fmt.Errorf("eo platform: no expected column list is declared for %s; add it to eoAssertedTables", table)
}

// EOPreflightSQL returns a READ-ONLY query that reports every column on which
// the live database and this migration disagree, for all six tables
// MigrateEOPlatform asserts. Zero rows means no assertion will fire at boot.
//
// It exists because assertColumns can only be run by starting the server, and
// since C1 starting the server is exactly what its failure prevents. An
// operator upgrading a box that ran an intermediate commit of this branch
// needs to find a column mismatch BEFORE the deployment, not diagnose it from
// a boot that refused. The expected values are rendered from eoAssertedTables,
// so this query cannot fall behind the assertion it previews.
//
// A table that does not exist yet is NOT reported: the migration creates it,
// and a fresh database would otherwise look like a wall of problems. Only a
// table that already exists and disagrees is a finding — which is precisely
// the case assertColumns catches, because CREATE TABLE IF NOT EXISTS cannot
// alter a table that is already there.
//
// The query is self-contained and takes no parameters, so it can be piped
// straight into psql (see docs/eo-platform-deploying.md).
func EOPreflightSQL() string {
	var rows []string
	for _, t := range eoAssertedTables {
		for i, c := range t.Columns {
			cast := ""
			if len(rows) == 0 && i == 0 {
				// The first VALUES row fixes the CTE's column types, so that
				// the FULL JOIN against information_schema compares like
				// with like.
				cast = "::text, ::text, ::text, ::int, ::boolean"
			}
			rows = append(rows, eoPreflightRow(t.Name, c, cast))
		}
	}
	// eoColumn.typeDesc, as SQL: the same "type(width) NOT NULL" rendering the
	// boot-time assertion puts in its error, so a preflight finding and the
	// startup failure it predicts read identically.
	desc := func(q string) string {
		return q + `.data_type || CASE WHEN ` + q + `.max_len > 0 THEN '(' || ` + q + `.max_len || ')' ELSE '' END` +
			` || CASE WHEN ` + q + `.nullable THEN '' ELSE ' NOT NULL' END`
	}
	return `-- EO platform pre-deploy check. Read-only. Generated from
-- migrations.EOPreflightSQL (internal/database/migrations/eo_platform.go),
-- which renders it from the very column lists MigrateEOPlatform asserts.
-- ZERO ROWS means no column assertion will fail at boot.
WITH expected(tbl, col, data_type, max_len, nullable) AS (
	VALUES
		` + strings.Join(rows, ",\n\t\t") + `
), live AS (
	SELECT table_name::text AS tbl,
	       column_name::text AS col,
	       data_type::text AS data_type,
	       COALESCE(character_maximum_length, 0)::int AS max_len,
	       (is_nullable = 'YES')::boolean AS nullable
	FROM information_schema.columns
	WHERE table_schema = current_schema()
	  AND table_name IN (SELECT DISTINCT tbl FROM expected)
), present AS (
	-- Only tables that ALREADY exist can drift; the migration creates the rest.
	SELECT DISTINCT tbl FROM live
)
SELECT COALESCE(e.tbl, l.tbl) AS table_name,
       COALESCE(e.col, l.col) AS column_name,
       CASE
         WHEN e.col IS NULL THEN 'exists in the database but is not declared in this migration'
         WHEN l.col IS NULL THEN 'is missing; expected ' || ` + desc("e") + `
         ELSE 'is ' || ` + desc("l") + ` || '; expected ' || ` + desc("e") + `
       END AS problem
FROM expected e
FULL JOIN live l ON e.tbl = l.tbl AND e.col = l.col
WHERE COALESCE(e.tbl, l.tbl) IN (SELECT tbl FROM present)
  AND (e.col IS NULL
       OR l.col IS NULL
       OR e.data_type IS DISTINCT FROM l.data_type
       OR e.max_len   IS DISTINCT FROM l.max_len
       OR e.nullable  IS DISTINCT FROM l.nullable)
ORDER BY table_name, column_name;
`
}

// eoPreflightRow renders one VALUES row, quoting the two identifiers the same
// way Postgres does. The column names here are this file's own literals, never
// anything a caller supplies.
func eoPreflightRow(table string, c eoColumn, cast string) string {
	casts := strings.Split(cast, ", ")
	for len(casts) < 5 {
		casts = append(casts, "")
	}
	return fmt.Sprintf("('%s'%s, '%s'%s, '%s'%s, %d%s, %t%s)",
		eoSQLQuote(table), casts[0],
		eoSQLQuote(c.Name), casts[1],
		eoSQLQuote(c.Type), casts[2],
		c.MaxLen, casts[3],
		c.Nullable, casts[4])
}

func eoSQLQuote(s string) string { return strings.ReplaceAll(s, "'", "''") }

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

// ── per-type partitions ─────────────────────────────────────────────────────

// eoPartitionKeyPattern is what a record type key may look like for its key to
// name a partition. It is deliberately narrower than the column: the key is
// interpolated into DDL, where it cannot be a bound parameter, and anything
// outside this set is refused rather than quoted and hoped for. The upper
// bound leaves room for the "eo_records_" prefix inside Postgres's 63-byte
// identifier limit, which would otherwise TRUNCATE the name and silently
// collide two types' partitions.
var eoPartitionKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,50}$`)

// EORecordPartitionName is the partition a record type's rows live in. It is a
// function rather than a literal so the seed, the tests and any later tooling
// name the same table.
func EORecordPartitionName(typeKey string) string { return "eo_records_" + typeKey }

// CreateEORecordPartition gives one concrete record type its own LIST
// partition of eo_records, holding exactly the rows whose record_type_id is
// typeID.
//
// WHY AT TYPE CREATION AND NOWHERE ELSE. eo_records has a DEFAULT partition,
// and Postgres validates it when a new partition is created: it takes an
// ACCESS EXCLUSIVE lock on eo_records_default and scans it to prove no
// existing row would belong to the new partition. So this is free only while
// the type has no records — and it ERRORS outright once it has any, after
// which the type can be given a partition only by a data move under that lock
// (create detached, copy, delete, attach). Spec §7.7 and
// models.EORecordType.PartitionPending carry the full reasoning; the seed
// calls this inside the same transaction that creates the type, which is the
// one moment the cost is zero.
//
// Indexes are not created here and must not be: every index on eo_records is
// declared on the PARENT above, and Postgres cascades those to partitions
// present and future, so a partition created here is indexed on birth.
//
// It is NOT in eoAssertedTables and assertTable is deliberately not called on
// it. That slice is a static checksum of the tables this file's CREATE TABLE
// statements declare, and EOPreflightSQL renders the operator's pre-deploy
// query from the very same slice; a partition has no column list of its own
// (it carries the parent's by construction, and ALTERing the parent alters
// every partition with it), and its name is per-type, so there is nothing for
// a static list to check and a per-type entry could never be written down
// ahead of time. What a partition CAN get wrong is which type it holds, and
// that is what the bound check below asserts.
func CreateEORecordPartition(db *gorm.DB, typeKey string, typeID uuid.UUID) error {
	if !eoPartitionKeyPattern.MatchString(typeKey) {
		return fmt.Errorf("eo platform: record type key %q cannot name a partition of eo_records; expected a lower-case identifier of at most 51 characters", typeKey)
	}
	if typeID == uuid.Nil {
		return fmt.Errorf("eo platform: record type %q has no id, so a partition for it would hold nothing", typeKey)
	}
	name := EORecordPartitionName(typeKey)
	// Neither half can be a bound parameter: an identifier and a partition
	// bound are both parsed, not compared. typeKey is checked above and
	// uuid.UUID renders as hex and dashes only.
	if err := db.Exec(fmt.Sprintf(
		`CREATE TABLE IF NOT EXISTS %q PARTITION OF eo_records FOR VALUES IN ('%s')`,
		name, typeID.String())).Error; err != nil {
		return fmt.Errorf("eo platform: create partition %s for record type %q: %w", name, typeKey, err)
	}
	// IF NOT EXISTS matches on the NAME alone. A partition left behind by a
	// type that was deleted and recreated under the same key is still bound to
	// the dead id, so the statement above succeeds, every new record goes to
	// the default partition instead, and promotion stops being free the moment
	// the first one lands there — silently. Read the bound back.
	var bound string
	if err := db.Raw(
		`SELECT COALESCE(pg_get_expr(c.relpartbound, c.oid), '') FROM pg_class c WHERE c.oid = to_regclass(?)`,
		name).Scan(&bound).Error; err != nil {
		return fmt.Errorf("eo platform: inspect partition %s: %w", name, err)
	}
	want := fmt.Sprintf("FOR VALUES IN ('%s')", typeID.String())
	if bound != want {
		return fmt.Errorf(
			"eo platform: partition %s already exists bound to %q, expected %q; it belongs to an earlier record type with the same key and must be dropped or renamed by hand before %q can have one",
			name, bound, want, typeKey)
	}
	return nil
}
