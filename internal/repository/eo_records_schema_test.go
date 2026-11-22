package repository

import (
	"strings"
	"testing"

	"github.com/automax/backend/internal/database/migrations"
	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// eoMigratedDB runs MigrateEOPlatform against a throwaway schema and fails the test
// if it errors.
func eoMigratedDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := eoTestDB(t)
	if err := migrations.MigrateEOPlatform(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestEORecordsIsPartitionedByRecordType(t *testing.T) {
	db := eoMigratedDB(t)

	var strategy string
	if err := db.Raw(`
		SELECT p.partstrat
		FROM pg_partitioned_table p
		JOIN pg_class c ON c.oid = p.partrelid
		WHERE c.oid = to_regclass('eo_records')`).Scan(&strategy).Error; err != nil {
		t.Fatalf("read partition strategy: %v", err)
	}
	if strategy != "l" {
		t.Errorf("partition strategy = %q, want %q (LIST)", strategy, "l")
	}

	var partKey string
	if err := db.Raw(`SELECT pg_get_partkeydef(to_regclass('eo_records'))`).Scan(&partKey).Error; err != nil {
		t.Fatalf("read partition key: %v", err)
	}
	if !strings.Contains(partKey, "record_type_id") {
		t.Errorf("partition key = %q, want it to be record_type_id", partKey)
	}
}

func TestEORecordsHasDefaultPartition(t *testing.T) {
	db := eoMigratedDB(t)

	// A record type created in the designer has no partition of its own, so
	// without a DEFAULT partition its first insert would fail.
	var defaults int64
	if err := db.Raw(`
		SELECT COUNT(*)
		FROM pg_class c
		JOIN pg_inherits i ON i.inhrelid = c.oid
		WHERE i.inhparent = to_regclass('eo_records')
		  AND pg_get_expr(c.relpartbound, c.oid) = 'DEFAULT'`).Scan(&defaults).Error; err != nil {
		t.Fatalf("count default partitions: %v", err)
	}
	if defaults != 1 {
		t.Fatalf("DEFAULT partitions = %d, want 1", defaults)
	}

	// And it must actually accept a record of a type nobody has partitioned.
	rec := eoNewTestRecord(uuid.New(), "EO-UNPARTITIONED-2026-1")
	if err := db.Create(rec).Error; err != nil {
		t.Errorf("insert into DEFAULT partition: %v", err)
	}
}

func TestEORecordsReferenceIsUniquePerPartition(t *testing.T) {
	db := eoMigratedDB(t)

	typeA, typeB := uuid.New(), uuid.New()
	const ref = "EO-DIR-2026-001"

	if err := db.Create(eoNewTestRecord(typeA, ref)).Error; err != nil {
		t.Fatalf("insert under type A: %v", err)
	}
	// Postgres cannot enforce a unique constraint across partitions, so the
	// same reference under a different record type is accepted. Global
	// uniqueness rests on models.ValidateReferencePrefix keeping type prefixes
	// from overlapping (spec §7.7), not on this constraint.
	if err := db.Create(eoNewTestRecord(typeB, ref)).Error; err != nil {
		t.Errorf("insert of the same reference under a second record type: %v, want it accepted", err)
	}
	// Within one record type the constraint does bite.
	if err := eoQuiet(db).Create(eoNewTestRecord(typeA, ref)).Error; err == nil {
		t.Errorf("duplicate reference within one record type was accepted, want a unique violation")
	}
}

func TestEORecordRoundTripsThroughGORM(t *testing.T) {
	db := eoMigratedDB(t)

	typeID := uuid.New()
	rec := eoNewTestRecord(typeID, "EO-DIR-2026-777")
	rec.Values = datatypes.JSON([]byte(`{"directive_origin":"weekly_meeting"}`))
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	if rec.ID == uuid.Nil {
		t.Fatalf("BeforeCreate did not assign an ID")
	}

	var got models.EORecord
	if err := db.Where("id = ? AND record_type_id = ?", rec.ID, typeID).First(&got).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got.Reference != rec.Reference || got.SubjectAr != rec.SubjectAr {
		t.Errorf("read back %+v, want reference %q subject_ar %q", got, rec.Reference, rec.SubjectAr)
	}
	if string(got.Values) != string(rec.Values) {
		t.Errorf("values = %s, want %s", got.Values, rec.Values)
	}

	// Soft delete must keep the row and hide it, as everywhere else in this codebase.
	if err := db.Where("id = ? AND record_type_id = ?", rec.ID, typeID).Delete(&models.EORecord{}).Error; err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	var live int64
	if err := db.Model(&models.EORecord{}).Where("id = ?", rec.ID).Count(&live).Error; err != nil {
		t.Fatalf("count live: %v", err)
	}
	if live != 0 {
		t.Errorf("live rows after soft delete = %d, want 0", live)
	}
	var total int64
	if err := db.Unscoped().Model(&models.EORecord{}).Where("id = ?", rec.ID).Count(&total).Error; err != nil {
		t.Fatalf("count unscoped: %v", err)
	}
	if total != 1 {
		t.Errorf("unscoped rows after soft delete = %d, want 1", total)
	}
}

func TestEORecordsHasItsQueryIndexes(t *testing.T) {
	db := eoMigratedDB(t)

	want := map[string]string{
		"idx_eo_records_values_gin":       "USING gin",
		"idx_eo_records_type_state":       "(record_type_id, state_id)",
		"idx_eo_records_reference":        "(reference)",
		"idx_eo_records_parent_record_id": "(parent_record_id)",
		"idx_eo_records_assigned_user_id": "(assigned_user_id)",
		"idx_eo_records_due_date":         "(due_date)",
	}
	defs := eoIndexDefs(t, db, "eo_records")
	for name, fragment := range want {
		def, ok := defs[name]
		if !ok {
			t.Errorf("index %s is missing; have %v", name, eoIndexNames(defs))
			continue
		}
		if !strings.Contains(def, fragment) {
			t.Errorf("index %s = %q, want it to contain %q", name, def, fragment)
		}
	}
}

func TestEORecordLinkRequiresBothEndpointsToExist(t *testing.T) {
	db := eoMigratedDB(t)

	from := eoNewTestRecord(uuid.New(), "EO-DIR-2026-010")
	to := eoNewTestRecord(uuid.New(), "EO-TSK-2026-010")
	if err := db.Create(from).Error; err != nil {
		t.Fatalf("create from: %v", err)
	}
	if err := db.Create(to).Error; err != nil {
		t.Fatalf("create to: %v", err)
	}

	link := &models.EORecordLink{
		FromRecordID:     from.ID,
		FromRecordTypeID: from.RecordTypeID,
		ToRecordID:       to.ID,
		ToRecordTypeID:   to.RecordTypeID,
		LinkTypeKey:      "spawned_task",
	}
	if err := db.Create(link).Error; err != nil {
		t.Fatalf("create link between two existing records: %v", err)
	}

	// The composite foreign key names the partition key, so a dangling
	// endpoint is rejected (spec §7.6).
	dangling := &models.EORecordLink{
		FromRecordID:     from.ID,
		FromRecordTypeID: from.RecordTypeID,
		ToRecordID:       uuid.New(),
		ToRecordTypeID:   uuid.New(),
		LinkTypeKey:      "spawned_task",
	}
	if err := eoQuiet(db).Create(dangling).Error; err == nil {
		t.Errorf("link to a non-existent record was accepted, want a foreign key violation")
	}
}

func TestEORecordHistoryIsTiedToARecord(t *testing.T) {
	db := eoMigratedDB(t)

	rec := eoNewTestRecord(uuid.New(), "EO-DIR-2026-020")
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("create record: %v", err)
	}
	entry := &models.EORecordHistory{
		RecordID:     rec.ID,
		RecordTypeID: rec.RecordTypeID,
		Comment:      "created",
	}
	if err := db.Create(entry).Error; err != nil {
		t.Fatalf("create history: %v", err)
	}
	orphan := &models.EORecordHistory{RecordID: uuid.New(), RecordTypeID: uuid.New()}
	if err := eoQuiet(db).Create(orphan).Error; err == nil {
		t.Errorf("history for a non-existent record was accepted, want a foreign key violation")
	}
}

func TestEORecordsNullabilityMatchesTheFieldMatrix(t *testing.T) {
	db := eoMigratedDB(t)

	type col struct {
		ColumnName string
		IsNullable string
	}
	var cols []col
	if err := db.Raw(`
		SELECT column_name, is_nullable
		FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'eo_records'`).Scan(&cols).Error; err != nil {
		t.Fatalf("read columns: %v", err)
	}
	nullable := make(map[string]bool, len(cols))
	for _, c := range cols {
		nullable[c.ColumnName] = c.IsNullable == "YES"
	}

	// The field matrix is the authority: subject_ar is Required Yes ("the
	// record's display name"), subject_en Required No ("secondary display
	// name"), and created_by is Auto on every record. created_by_id must be
	// NOT NULL because the Go field is a value type and could otherwise only
	// ever store the zero UUID.
	want := map[string]bool{
		"subject_ar":    false,
		"subject_en":    true,
		"created_by_id": false,
		"reference":     false,
		"due_date":      true,
		"closed_at":     true,
	}
	for name, wantNullable := range want {
		got, ok := nullable[name]
		if !ok {
			t.Errorf("column %s is missing", name)
			continue
		}
		if got != wantNullable {
			t.Errorf("column %s nullable = %v, want %v", name, got, wantNullable)
		}
	}
}

func TestEORecordHistoryBlocksHardDeleteOfItsRecord(t *testing.T) {
	db := eoMigratedDB(t)

	rec := eoNewTestRecord(uuid.New(), "EO-DIR-2026-030")
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("create record: %v", err)
	}
	if err := db.Create(&models.EORecordHistory{
		RecordID: rec.ID, RecordTypeID: rec.RecordTypeID, Comment: "created",
	}).Error; err != nil {
		t.Fatalf("create history: %v", err)
	}

	// ON DELETE RESTRICT: the audit trail outlives any attempt to hard-delete
	// the record, which is what "every CFF requires a traceable path" means.
	err := eoQuiet(db).Unscoped().
		Where("id = ? AND record_type_id = ?", rec.ID, rec.RecordTypeID).
		Delete(&models.EORecord{}).Error
	if err == nil {
		t.Errorf("hard delete of a record with history succeeded, want it blocked by ON DELETE RESTRICT")
	}

	// Soft delete, the normal path, is unaffected.
	if err := db.Where("id = ? AND record_type_id = ?", rec.ID, rec.RecordTypeID).
		Delete(&models.EORecord{}).Error; err != nil {
		t.Errorf("soft delete: %v", err)
	}
}

func TestEORecordLinksAreRemovedWithTheirRecord(t *testing.T) {
	db := eoMigratedDB(t)

	from := eoNewTestRecord(uuid.New(), "EO-DIR-2026-040")
	to := eoNewTestRecord(uuid.New(), "EO-TSK-2026-040")
	if err := db.Create(from).Error; err != nil {
		t.Fatalf("create from: %v", err)
	}
	if err := db.Create(to).Error; err != nil {
		t.Fatalf("create to: %v", err)
	}
	if err := db.Create(&models.EORecordLink{
		FromRecordID: from.ID, FromRecordTypeID: from.RecordTypeID,
		ToRecordID: to.ID, ToRecordTypeID: to.RecordTypeID,
		LinkTypeKey: "spawned_task",
	}).Error; err != nil {
		t.Fatalf("create link: %v", err)
	}

	// A link to a record that no longer exists is meaningless, so links
	// CASCADE where history RESTRICTs.
	if err := db.Unscoped().
		Where("id = ? AND record_type_id = ?", to.ID, to.RecordTypeID).
		Delete(&models.EORecord{}).Error; err != nil {
		t.Fatalf("hard delete of a linked record: %v", err)
	}
	var links int64
	if err := db.Model(&models.EORecordLink{}).Count(&links).Error; err != nil {
		t.Fatalf("count links: %v", err)
	}
	if links != 0 {
		t.Errorf("links after deleting an endpoint = %d, want 0", links)
	}
}

// TestMigrateEOPlatformDetectsShapeDrift covers the structural hole in
// CREATE TABLE IF NOT EXISTS: on a database where the table already exists,
// editing the column list in eo_platform.go is a silent no-op, and every test
// starts from an empty schema so nothing would notice. The migration asserts
// the live shape after creating it; these cases prove the assertion fires.
func TestMigrateEOPlatformDetectsShapeDrift(t *testing.T) {
	cases := []struct {
		name    string
		drift   string
		wantSub []string
	}{
		{
			name:    "narrowed column",
			drift:   `ALTER TABLE eo_records ALTER COLUMN reference TYPE VARCHAR(80)`,
			wantSub: []string{"reference is character varying(80)", "character varying(100) NOT NULL"},
		},
		{
			name:    "dropped column",
			drift:   `ALTER TABLE eo_records DROP COLUMN delay_reason`,
			wantSub: []string{"delay_reason is missing"},
		},
		{
			name:    "undeclared column",
			drift:   `ALTER TABLE eo_records ADD COLUMN legacy_note TEXT`,
			wantSub: []string{"legacy_note exists but is not declared"},
		},
		{
			name:    "relaxed nullability",
			drift:   `ALTER TABLE eo_records ALTER COLUMN created_by_id DROP NOT NULL`,
			wantSub: []string{"created_by_id is uuid,", "uuid NOT NULL"},
		},
		{
			name:    "drift in a sibling table",
			drift:   `ALTER TABLE eo_record_history DROP COLUMN is_system_action`,
			wantSub: []string{"eo_record_history", "is_system_action is missing"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := eoMigratedDB(t)
			if err := db.Exec(tc.drift).Error; err != nil {
				t.Fatalf("apply drift: %v", err)
			}
			err := migrations.MigrateEOPlatform(db)
			if err == nil {
				t.Fatalf("migration accepted a drifted table, want an error")
			}
			t.Logf("%s: %v", tc.name, err)
			for _, sub := range tc.wantSub {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error = %q, want it to contain %q", err, sub)
				}
			}
		})
	}
}

func TestMigrateEOPlatformRejectsAWrongPartitionKey(t *testing.T) {
	db := eoTestDB(t)

	// Partitioned, so the relkind guard is satisfied — but on the wrong key.
	// Repartitioning is a table rewrite, so this has to be refused rather than
	// quietly accepted.
	if err := db.Exec(`
		CREATE TABLE eo_records (id uuid NOT NULL, record_type_id uuid NOT NULL)
		PARTITION BY HASH (id)`).Error; err != nil {
		t.Fatalf("create decoy table: %v", err)
	}
	err := migrations.MigrateEOPlatform(db)
	if err == nil {
		t.Fatalf("migration accepted a table partitioned on the wrong key, want an error")
	}
	if !strings.Contains(err.Error(), "HASH (id)") || !strings.Contains(err.Error(), "LIST (record_type_id)") {
		t.Errorf("error = %q, want it to name both the live and the expected partition key", err)
	}
}

func TestMigrateEOPlatformIsIdempotent(t *testing.T) {
	db := eoTestDB(t)

	if err := migrations.MigrateEOPlatform(db); err != nil {
		t.Fatalf("first run: %v", err)
	}
	before := eoSchemaSnapshot(t, db)
	if !strings.Contains(before, "uq_eo_records_type_reference") {
		t.Fatalf("snapshot did not capture the schema, so the comparison below would be vacuous:\n%s", before)
	}

	if err := migrations.MigrateEOPlatform(db); err != nil {
		t.Fatalf("second run: %v", err)
	}
	after := eoSchemaSnapshot(t, db)

	// The second run must change nothing: same relations (same OIDs, so
	// nothing was dropped and recreated), same indexes, same constraints.
	if before != after {
		t.Errorf("schema changed on the second run:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	var partitions int64
	if err := db.Raw(`
		SELECT COUNT(*) FROM pg_inherits WHERE inhparent = to_regclass('eo_records')`).Scan(&partitions).Error; err != nil {
		t.Fatalf("count partitions: %v", err)
	}
	if partitions != 1 {
		t.Errorf("partitions after two runs = %d, want 1 (the DEFAULT partition)", partitions)
	}
}

// eoSchemaSnapshot renders every EO relation, index and constraint in the
// current schema as one comparable string.
func eoSchemaSnapshot(t *testing.T, db *gorm.DB) string {
	t.Helper()
	var lines []string
	if err := db.Raw(`
		SELECT c.oid::text || ' ' || c.relkind::text || ' ' || c.relname AS line
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND c.relname LIKE 'eo\_%'
		UNION ALL
		SELECT 'index ' || indexname || ' ' || indexdef
		FROM pg_indexes WHERE schemaname = current_schema() AND tablename LIKE 'eo\_%'
		UNION ALL
		SELECT 'constraint ' || conname || ' ' || pg_get_constraintdef(oid)
		FROM pg_constraint
		WHERE connamespace = (SELECT oid FROM pg_namespace WHERE nspname = current_schema())
		ORDER BY 1`).Scan(&lines).Error; err != nil {
		t.Fatalf("snapshot schema: %v", err)
	}
	return strings.Join(lines, "\n")
}

func TestMigrateEOPlatformRejectsANonPartitionedTable(t *testing.T) {
	db := eoTestDB(t)

	// What AutoMigrate would leave behind if models.EORecord were ever added
	// to its list: a plain table under the same name.
	if err := db.Exec(`CREATE TABLE eo_records (id uuid NOT NULL, record_type_id uuid NOT NULL)`).Error; err != nil {
		t.Fatalf("create decoy table: %v", err)
	}
	err := migrations.MigrateEOPlatform(db)
	if err == nil {
		t.Fatalf("migration succeeded against a plain eo_records table, want a descriptive error")
	}
	if !strings.Contains(err.Error(), "partition") {
		t.Errorf("error = %q, want it to mention partitioning", err)
	}
}

// TestEOMetamodelUniqueIndexesArePartial is a carry-forward check on Task 3.
// Those three unique indexes are declared as GORM tags of the form
// `uniqueIndex:<name>,where:deleted_at IS NULL`. Nothing had verified that
// GORM's `where:` option actually emits a partial index; a plain unique index
// would mean a soft-deleted key could never be reused.
func TestEOMetamodelUniqueIndexesArePartial(t *testing.T) {
	db := eoTestDB(t)
	// Same session configuration Migrate() uses (postgres.go:65).
	// CreateTable, not AutoMigrate: AutoMigrate reorders in every associated
	// model, which would drag users, departments and the whole workflow graph
	// into the throwaway schema. Index creation is the same code path either
	// way — the Postgres driver sets CreateIndexAfterCreateTable, so both
	// AutoMigrate and CreateTable emit indexes through its Migrator.CreateIndex.
	migrationDB := db.Session(&gorm.Session{})
	migrationDB.Config.DisableForeignKeyConstraintWhenMigrating = true
	if err := migrationDB.Migrator().CreateTable(&models.EORecordType{}, &models.EOFieldDef{}, &models.EOLinkType{}); err != nil {
		t.Fatalf("create metamodel tables: %v", err)
	}

	cases := []struct{ table, index string }{
		{"eo_record_types", "idx_eo_record_types_key"},
		{"eo_link_types", "idx_eo_link_types_key"},
		{"eo_field_defs", "idx_eo_field_defs_type_key"},
	}
	for _, c := range cases {
		defs := eoIndexDefs(t, db, c.table)
		def, ok := defs[c.index]
		if !ok {
			t.Errorf("%s: index %s is missing; have %v", c.table, c.index, eoIndexNames(defs))
			continue
		}
		t.Logf("%s.%s = %s", c.table, c.index, def)
		if !strings.Contains(def, "UNIQUE INDEX") {
			t.Errorf("%s: index %s is not unique: %s", c.table, c.index, def)
		}
		if !strings.Contains(def, "WHERE (deleted_at IS NULL)") {
			t.Errorf("%s: index %s is not partial, so a soft-deleted key can never be reused: %s", c.table, c.index, def)
		}
	}
}

// eoQuiet suppresses GORM's error log for a statement that is expected to fail,
// so a passing run prints nothing.
func eoQuiet(db *gorm.DB) *gorm.DB {
	return db.Session(&gorm.Session{Logger: logger.Discard})
}

func eoNewTestRecord(recordTypeID uuid.UUID, reference string) *models.EORecord {
	return &models.EORecord{
		RecordTypeID: recordTypeID,
		Reference:    reference,
		SubjectAr:    "موضوع",
		SubjectEn:    "subject",
		CreatedByID:  uuid.New(),
	}
}

// eoIndexDefs returns indexdef by index name for one table in the current schema.
func eoIndexDefs(t *testing.T, db *gorm.DB, table string) map[string]string {
	t.Helper()
	type row struct {
		IndexName string
		IndexDef  string
	}
	var rows []row
	if err := db.Raw(`
		SELECT indexname AS index_name, indexdef AS index_def
		FROM pg_indexes
		WHERE schemaname = current_schema() AND tablename = ?`, table).Scan(&rows).Error; err != nil {
		t.Fatalf("read pg_indexes for %s: %v", table, err)
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.IndexName] = r.IndexDef
	}
	return out
}

func eoIndexNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
