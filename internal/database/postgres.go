package database

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/automax/backend/internal/config"
	"github.com/automax/backend/internal/database/migrations"
	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/pkg/constants"
	"github.com/automax/backend/pkg/utils"
	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var DB *gorm.DB

func Connect(cfg *config.DatabaseConfig) (*gorm.DB, error) {
	dsn := fmt.Sprintf(
		"host=%s user=%s password=%s dbname=%s port=%s sslmode=%s",
		cfg.Host, cfg.User, cfg.Password, cfg.DBName, cfg.Port, cfg.SSLMode,
	)

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	DB = db
	log.Println("Database connected successfully")
	return db, nil
}

func Migrate(db *gorm.DB, cfg *config.Config) error {
	log.Println("Running database migrations...")
	// Manually create the ENUM type for call_status if it doesn't exist
	createEnumQuery := `
        DO $$
        BEGIN
            IF NOT EXISTS (SELECT 1 FROM pg_type WHERE typname = 'user_call_status') THEN
                CREATE TYPE user_call_status AS ENUM ('offline', 'online', 'busy', 'in_call', 'available');
            END IF;
        END $$;`

	if err := db.Exec(createEnumQuery).Error; err != nil {
		return fmt.Errorf("failed to create enum type: %w", err)
	}

	// License table schema migration: the old single-PEM public_key column has
	// been replaced by a JWKS JSON blob. Dropping here is idempotent — AutoMigrate
	// will add the new `jwks` column on the next step. Any pre-existing license row
	// becomes non-verifiable; LoadFromDB gracefully falls back to cached state.
	if err := db.Exec(`ALTER TABLE IF EXISTS licenses DROP COLUMN IF EXISTS public_key`).Error; err != nil {
		log.Printf("Warning: failed to drop legacy licenses.public_key column: %v", err)
	}

	// Use session that disables FK constraints during migration to prevent auto-FK issues
	migrationDB := db.Session(&gorm.Session{})
	migrationDB.Config.DisableForeignKeyConstraintWhenMigrating = true

	err := migrationDB.AutoMigrate(
		&models.Permission{},
		&models.Role{},
		&models.Category{},
		&models.Classification{},
		&models.ClassificationType{},
		&models.ClassificationCriticality{},
		&models.Location{},
		&models.Department{},
		&models.User{},
		&models.ActionLog{},
		&models.ExtensionAssignment{},
		&models.ExtensionAssignmentHistory{},
		&models.CallLog{},
		&models.CallParticipant{},
		&models.CallLogAttachment{},
		&models.NotificationTemplate{},
		&models.NotificationLog{},
		&models.EscalationSLA{},
		// IncidentMerge MUST come before Incident (foreign key dependency)
		&models.IncidentMerge{},
		// Lookup models
		&models.LookupCategory{},
		&models.LookupValue{},
		// Workflow models
		&models.Workflow{},
		&models.WorkflowState{},
		&models.WorkflowTransition{},
		&models.TransitionRequirement{},
		&models.TransitionAction{},
		&models.TransitionFieldChange{},
		&models.CommentTemplate{},
		&models.FeedbackTemplate{},
		// Incident models
		&models.Incident{},
		&models.IncidentComment{},
		&models.IncidentAttachment{},
		&models.IncidentFeedback{},
		&models.IncidentPublicFeedback{},
		&models.SmsFeedbackPending{},
		&models.IncidentTransitionHistory{},
		&models.IncidentRevision{},
		&models.IncidentRejectionLog{},
		&models.IvrSmsLink{},
		&models.NasaqVerification{},
		// Report models
		&models.Report{},
		&models.ReportExecution{},
		&models.ReportTemplate{},
		// Application Links
		&models.ApplicationLink{},
		// Settings
		&models.Settings{},
		// Escalation Policies
		&models.EscalationPolicy{},
		&models.EscalationPolicyStep{},
		&models.EscalationPolicyStepTarget{},
		// Custom Escalation Groups
		&models.EscalationGroup{},
		&models.EscalationGroupTarget{},
		// Ready-to-Close expiry tracking
		&models.IncidentReadyToCloseEntry{},
		&models.DeviceToken{},
		&models.CallerSentiment{},
		// AI Quality Feedback
		&models.AIQualityFeedback{},
		// License Management
		&models.License{},
		// External Integration
		&models.IntegrationVariable{},
		&models.IntegrationScript{},
		&models.WorkflowStateTrigger{},
		&models.WorkflowTransitionTrigger{},
		&models.IntegrationExecutionLog{},
		&models.IncidentBridge{},
		&models.WebhookCallbackConfig{},
		// MOMRA CRM outbound integration
		&models.MOMRAStatusMapping{},
	)
	if err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	if cfg.GoalManagement.Enabled {
		if err := migrationDB.AutoMigrate(
			&models.Goal{},
			&models.GoalMetric{},
			&models.MetricHistory{},
			&models.GoalCollaborator{},
			&models.Evidence{},
			&models.EvidenceTransitionHistory{},
			&models.GoalCheckIn{},
			&models.GoalMetricValueChange{},
			&models.MetricTransitionHistory{},
			&models.MetricValueChangeTransitionHistory{},
			&models.GoalTemplate{},
			&models.MetricImportBatch{},
			&models.MetricImportItem{},
			&models.MetricImportBatchTransitionHistory{},
			&models.GoalComment{},
			&models.ReviewCycle{},
			&models.ReviewAssignment{},
			&models.GoalScore{},

			// KPI / Goal Management models
			&models.Pillar{},
			&models.Enabler{},
			&models.OperationalObjective{},
			&models.Process{},
			&models.Initiative{},
			&models.Domain{},
			&models.AwardCriterion{},
			&models.AwardSubCriterion{},
			&models.StrategicKPI{},
			&models.OperationalKPI{},
			&models.AwardKPI{},
			&models.KpiAnnualTarget{},
			&models.KpiPerformance{},
			&models.KpiBenchmark{},
			&models.KpiSegmentation{},
			&models.KpiWorkflowInstance{},
			&models.KpiWorkflowAction{},
			&models.KpiPerformanceBand{},
			&models.KpiCorrectiveAction{},
			&models.KpiDataSource{},
			&models.KpiSegmentationDimension{},
			&models.KpiPerformanceEvidence{},
			&models.KpiMetric{},
			&models.KpiEvidence{},
			&models.KpiCollaborator{},
			&models.KpiCheckIn{},
			&models.KpiComment{},
			&models.KpiEntry{},
			&models.KpiEntryEvidence{},
			&models.KpiCollaboratorAssignment{},
			&models.KpiOrganization{},
			&models.KpiSegmentationAxis{},
			&models.KpiAdministrativeUnit{},
		); err != nil {
			return fmt.Errorf("failed to run goal management migrations: %w", err)
		}

		// Seed a default kpi_entry approval workflow so KpiEntry submissions
		// have somewhere to go without requiring manual admin setup first.
		if err := migrations.SeedKpiEntryWorkflow(migrationDB); err != nil {
			log.Printf("Warning: kpi_entry workflow seed failed: %v", err)
		}

		// Recompute achievement_pct for existing KPI performance rows now that the
		// calculation respects each KPI's polarity instead of always using actual/target.
		if err := migrations.MigrateKpiAchievementBackfill(migrationDB); err != nil {
			log.Printf("Warning: KPI achievement backfill migration failed: %v", err)
		}

		// Backfill period_type/period_key on existing target/performance rows.
		if err := migrations.MigrateKpiPeriodBackfill(migrationDB); err != nil {
			log.Printf("Warning: KPI period backfill migration failed: %v", err)
		}

		// Replace the broken kpi_annual_targets unique index (missing
		// metric_id/target_year, not scoped past soft-deletes) with a correct one.
		if err := migrations.MigrateKpiTargetUniqueIndex(migrationDB); err != nil {
			log.Printf("Warning: KPI target unique index migration failed: %v", err)
		}

		// Scope strategic_kpis/operational_kpis/award_kpis' unique code index
		// to non-deleted rows — otherwise a soft-deleted KPI permanently
		// reserves its code and blocks ever recreating it.
		if err := migrations.MigrateKpiDictionaryCodeUniqueIndex(migrationDB); err != nil {
			log.Printf("Warning: KPI dictionary code unique index migration failed: %v", err)
		}

		// Multiple approved Entries per metric+period are now allowed
		// (aggregated, not treated as one final value) — drop the old
		// per-period uniqueness on kpi_entries and add Organization/Segment/
		// Version scope columns to entries and targets.
		if err := migrations.MigrateKpiEntryMultiOrgScope(migrationDB); err != nil {
			log.Printf("Warning: KPI entry multi-org-scope migration failed: %v", err)
		}

		// Strategic/Operational/Award KPIs now share one create form: Objective
		// is the one universal required taxonomy field, and Award Criteria/
		// Sub-Criteria becomes an optional supplementary tag on every type —
		// including Award KPIs, which previously required it.
		if err := migrations.MigrateKpiDictionaryUnifyTaxonomy(migrationDB); err != nil {
			log.Printf("Warning: KPI dictionary taxonomy unification migration failed: %v", err)
		}

		// Enforce unique domain names (excluding soft-deleted records).
		db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_domains_name_en ON domains(name_en) WHERE deleted_at IS NULL`)

		// Enforce unique data source names (excluding soft-deleted records).
		db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_kpi_data_sources_name_en ON kpi_data_sources(name_en) WHERE deleted_at IS NULL`)
	}

	// Drop problematic foreign key constraints on incidents table
	// Merge relationships are tracked via incident_merges table, not direct FK
	log.Println("Cleaning up auto-generated foreign key constraints...")
	db.Exec("ALTER TABLE incidents DROP CONSTRAINT IF EXISTS fk_incident_merges_master_incident")
	db.Exec("ALTER TABLE incidents DROP CONSTRAINT IF EXISTS incident_merges_master_incident_id_fkey")
	db.Exec("ALTER TABLE incidents DROP CONSTRAINT IF EXISTS fk_incidents_master_incident")

	// Drop FK constraints on performed_by_id so system-triggered actions (uuid.Nil) are allowed.
	// System actions (e.g. Ready-to-Close auto-revert) do not have a real user performer.
	db.Exec("ALTER TABLE incident_transition_histories DROP CONSTRAINT IF EXISTS fk_incident_transition_histories_performed_by")
	db.Exec("ALTER TABLE incident_transition_histories DROP CONSTRAINT IF EXISTS incident_transition_histories_performed_by_id_fkey")
	db.Exec("ALTER TABLE incident_revisions DROP CONSTRAINT IF EXISTS fk_incident_revisions_performed_by")
	db.Exec("ALTER TABLE incident_revisions DROP CONSTRAINT IF EXISTS incident_revisions_performed_by_id_fkey")
	db.Exec("ALTER TABLE evidence_transition_histories DROP CONSTRAINT IF EXISTS fk_evidence_transition_histories_performed_by")
	db.Exec("ALTER TABLE evidence_transition_histories DROP CONSTRAINT IF EXISTS evidence_transition_histories_performed_by_id_fkey")
	db.Exec("ALTER TABLE metric_import_batch_transition_histories DROP CONSTRAINT IF EXISTS fk_metric_import_batch_transition_histories_performed_by")
	db.Exec("ALTER TABLE metric_import_batch_transition_histories DROP CONSTRAINT IF EXISTS metric_import_batch_transition_histories_performed_by_id_fkey")
	db.Exec("ALTER TABLE metric_transition_histories DROP CONSTRAINT IF EXISTS fk_metric_transition_histories_performed_by")
	db.Exec("ALTER TABLE metric_transition_histories DROP CONSTRAINT IF EXISTS metric_transition_histories_performed_by_id_fkey")
	db.Exec("ALTER TABLE metric_value_change_transition_histories DROP CONSTRAINT IF EXISTS fk_metric_value_change_transition_histories_performed_by")
	db.Exec("ALTER TABLE metric_value_change_transition_histories DROP CONSTRAINT IF EXISTS metric_value_change_transition_histories_performed_by_id_fkey")
	db.Exec("ALTER TABLE kpi_workflow_actions DROP CONSTRAINT IF EXISTS fk_kpi_workflow_actions_performed_by")
	db.Exec("ALTER TABLE kpi_workflow_actions DROP CONSTRAINT IF EXISTS kpi_workflow_actions_performed_by_id_fkey")

	// Legacy strategic_goal_id columns predate the StrategicGoal→Goal model rename.
	// AutoMigrate never drops old columns, so on databases created before that
	// rename these can still exist with a NOT NULL constraint (nothing populates
	// them anymore), blocking every insert with a not-null violation. Drop them
	// wherever they're still present; no-op on schemas that never had them.
	for _, table := range []string{"operational_objectives", "processes", "initiatives", "strategic_kpis", "operational_kpis", "award_kpis"} {
		db.Exec(fmt.Sprintf(`ALTER TABLE IF EXISTS %s DROP COLUMN IF EXISTS strategic_goal_id`, table))
	}

	db.Exec("ALTER TABLE lookup_categories ADD COLUMN IF NOT EXISTS redirect_url VARCHAR(500)")

	// Notification template enhancements: add categorisation and bilingual-linking columns.
	db.Exec("ALTER TABLE notification_templates ADD COLUMN IF NOT EXISTS name VARCHAR(200)")
	db.Exec("ALTER TABLE notification_templates ADD COLUMN IF NOT EXISTS module_type VARCHAR(50)")
	db.Exec("ALTER TABLE notification_templates ADD COLUMN IF NOT EXISTS action_type VARCHAR(50)")
	db.Exec("ALTER TABLE notification_templates ADD COLUMN IF NOT EXISTS variables TEXT")
	db.Exec("ALTER TABLE notification_templates ADD COLUMN IF NOT EXISTS transition_id UUID REFERENCES workflow_transitions(id) ON DELETE SET NULL")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_notification_templates_module_type ON notification_templates(module_type)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_notification_templates_action_type ON notification_templates(action_type)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_notification_templates_transition_id ON notification_templates(transition_id)")

	// First Response Time is computed on read: the incident report query scans incident_revisions
	// per incident to find the earliest qualifying staff action. This composite index serves that
	// DISTINCT ON directly; without it the query degrades to a full scan as revisions accumulate.
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_incident_revisions_frt ON incident_revisions (incident_id, created_at, revision_number) INCLUDE (action_type, performed_by_id)`)

	// Bilingual redesign: add new columns, migrate existing data, drop old single-language columns.
	db.Exec("ALTER TABLE notification_templates ADD COLUMN IF NOT EXISTS subject_en TEXT")
	db.Exec("ALTER TABLE notification_templates ADD COLUMN IF NOT EXISTS body_en    TEXT")
	db.Exec("ALTER TABLE notification_templates ADD COLUMN IF NOT EXISTS subject_ar TEXT")
	db.Exec("ALTER TABLE notification_templates ADD COLUMN IF NOT EXISTS body_ar    TEXT")
	db.Exec("UPDATE notification_templates SET subject_en = subject, body_en = body WHERE language = 'en' AND body_en IS NULL")
	db.Exec(`UPDATE notification_templates t SET subject_ar = ar.subject, body_ar = ar.body FROM notification_templates ar WHERE ar.language = 'ar' AND ar.code = t.code AND ar.channel = t.channel AND t.language = 'en'`)
	db.Exec("DELETE FROM notification_templates WHERE language = 'ar'")
	db.Exec("ALTER TABLE notification_templates DROP COLUMN IF EXISTS language")
	db.Exec("ALTER TABLE notification_templates DROP COLUMN IF EXISTS subject")
	db.Exec("ALTER TABLE notification_templates DROP COLUMN IF EXISTS body")
	db.Exec("ALTER TABLE notification_templates DROP COLUMN IF EXISTS description")
	// Enforce uniqueness of (code, channel) for non-deleted templates so duplicate codes
	// are rejected at the DB level in addition to the service-level check.
	db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uq_notification_templates_code_channel ON notification_templates(code, channel) WHERE deleted_at IS NULL`)

	// Workflow code column was previously VARCHAR(50) which is too short for some existing workflows. Increase to 100 chars. Note: MySQL syntax is ALTER TABLE workflows MODIFY code VARCHAR(100) NOT NULL but Postgres is ALTER TABLE workflows ALTER COLUMN code TYPE VARCHAR(100). GORM's AutoMigrate doesn't handle this edge case, so we run raw SQL. Idempotent: safe to run repeatedly.
	db.Exec("ALTER TABLE workflows ALTER COLUMN code TYPE VARCHAR(100)")
	db.Exec("ALTER TABLE reports ALTER COLUMN timestamp_key TYPE VARCHAR(100) Default 'created_at'")

	// Migrate transition assignment roles (single → many-to-many)
	if err := migrations.MigrateTransitionAssignmentRoles(db); err != nil {
		log.Printf("Warning: transition assignment roles migration failed: %v", err)
	}

	// Migrate state assignment roles (creation-time user assignment join table)
	if err := migrations.MigrateStateAssignmentRoles(db); err != nil {
		log.Printf("Warning: state assignment roles migration failed: %v", err)
	}

	// Migrate closed incident edit tracking
	if err := migrations.MigrateClosedIncidentEditTracking(db); err != nil {
		log.Printf("Warning: closed incident edit tracking migration failed: %v", err)
	}

	// Sub-workflow columns: target_workflow_id, is_return_transition, workflow_stack.
	if err := migrations.MigrateWorkflowSubworkflowSchema(db); err != nil {
		log.Printf("Warning: workflow sub-workflow schema migration failed: %v", err)
	}

	// Migrate user mobile verified
	if err := migrations.MigrateUserMobileVerified(db); err != nil {
		log.Printf("Warning: user mobile verified migration failed: %v", err)
	}

	// Migrate AI quality columns
	if err := migrations.MigrateAIQuality(db); err != nil {
		log.Printf("Warning: AI quality migration failed: %v", err)
	}

	// Migrate classification single-type column → classification_types join table
	if err := migrations.MigrateClassificationTypes(db); err != nil {
		log.Printf("Warning: classification types migration failed: %v", err)
	}

	// Migrate user national_id column
	if err := migrations.MigrateUserNationalID(db); err != nil {
		log.Printf("Warning: user national_id migration failed: %v", err)
	}

	// Enforce phone uniqueness at the DB level — phone stays optional, but a
	// non-blank number may only belong to one live user
	if err := migrations.MigrateUserPhoneUnique(db); err != nil {
		log.Printf("Warning: user phone unique index migration failed: %v", err)
	}

	// Drop recording_url from call_logs — URLs are now generated from call_log_attachments
	if err := migrations.MigrateCallLogRecordingURL(db); err != nil {
		log.Printf("Warning: call_log recording_url migration failed: %v", err)
	}

	// Drop user_id from call_participants — participants are identified by phone_number only
	if err := migrations.MigrateCallParticipantPhone(db); err != nil {
		log.Printf("Warning: call_participant phone migration failed: %v", err)
	}

	// Backfill Arabic name/description/module on permissions. The Seed loop below
	// only inserts missing permissions, so existing rows would otherwise never get
	// translations. Idempotent and self-skipping once nothing is blank.
	if err := migrations.MigratePermissionArabic(db); err != nil {
		log.Printf("Warning: permission Arabic backfill failed: %v", err)
	}

	// call_logs.created_by must accept NULL: system/machine-ingested rows (e.g.
	// the Cintrix call-event webhook) have no acting user. Idempotent.
	db.Exec("ALTER TABLE call_logs ALTER COLUMN created_by DROP NOT NULL")

	// Decouple extension from users: backfill into extension_assignments, drop users.extension
	if err := migrations.MigrateExtensionDecouple(db); err != nil {
		log.Printf("Warning: extension decouple migration failed: %v", err)
	}

	// Composite index for the incident communication history query (filter by incident_id, sort by created_at)
	if err := migrations.MigrateNotificationLogIncidentIndex(db); err != nil {
		log.Printf("Warning: notification_log incident index migration failed: %v", err)
	}

	// Assign a unique Organization Code (ORG-######) to any department missing one.
	// EPM940 only — other clients supply department codes in the payload. Idempotent:
	// only touches rows with an empty/NULL code.
	if strings.EqualFold(strings.TrimSpace(cfg.ClientCode), constants.CLIENT_CODE.EPM940) {
		if err := migrations.MigrateDepartmentCodeBackfill(db); err != nil {
			log.Printf("Warning: department code backfill failed: %v", err)
		}
		if err := migrations.MigrateClassificationCodeBackfill(db); err != nil {
			log.Printf("Warning: classification code backfill failed: %v", err)
		}
		if err := migrations.MigrateRoleCodeBackfill(db); err != nil {
			log.Printf("Warning: role code backfill failed: %v", err)
		}
		if err := migrations.MigrateLocationCodeBackfill(db); err != nil {
			log.Printf("Warning: location code backfill failed: %v", err)
		}
	}

	// Seed existing free-text goal categories as root Category rows
	// and back-fill goals.category_id. Idempotent: safe to run repeatedly.
	if cfg.GoalManagement.Enabled {
		if err := migrateFreeTextGoalCategories(db); err != nil {
			log.Printf("Warning: goal category back-fill migration failed: %v", err)
		}
	}

	// Partial Close feature columns — idempotent, safe to run repeatedly
	db.Exec("ALTER TABLE workflow_states ADD COLUMN IF NOT EXISTS is_partial_close BOOLEAN NOT NULL DEFAULT false")
	db.Exec("ALTER TABLE incidents ADD COLUMN IF NOT EXISTS partial_close_expires_at TIMESTAMPTZ")
	db.Exec("ALTER TABLE incidents ADD COLUMN IF NOT EXISTS partial_close_duration VARCHAR(100) NOT NULL DEFAULT ''")
	db.Exec("ALTER TABLE incidents ADD COLUMN IF NOT EXISTS partial_close_notified BOOLEAN NOT NULL DEFAULT false")

	// AD/LDAP user flag — idempotent
	db.Exec("ALTER TABLE users ADD COLUMN IF NOT EXISTS is_ad_user BOOLEAN NOT NULL DEFAULT false")

	// NASAQ flag on classifications — only leaf classifications may have this set to
	// true; enforced in the classification handler/repository. Idempotent.
	db.Exec("ALTER TABLE classifications ADD COLUMN IF NOT EXISTS is_nasaq BOOLEAN NOT NULL DEFAULT false")

	// role_permissions is a GORM many2many join table with no primary key.
	// Without a replica identity PostgreSQL refuses DELETE operations on tables
	// that are part of a logical replication publication (SQLSTATE 55000).
	// FULL uses the entire row as identity — idempotent, safe to run repeatedly.
	db.Exec("ALTER TABLE role_permissions REPLICA IDENTITY FULL")

	// Serves the map markers endpoint, which filters to only geolocated
	// incidents; partial so rows without coordinates (the common case) don't
	// bloat the index.
	db.Exec(`CREATE INDEX IF NOT EXISTS idx_incidents_geo ON incidents (latitude, longitude) WHERE latitude IS NOT NULL AND longitude IS NOT NULL`)

	log.Println("Database migrations completed")
	return nil
}

// migrateFreeTextGoalCategories seeds root Category rows from distinct
// non-empty `goals.category` values (where `category_id IS NULL`) and links
// those goals to the new Category rows. Idempotent.
func migrateFreeTextGoalCategories(db *gorm.DB) error {
	// Guard: only run if both columns exist on the goals table. On a fresh DB
	// the goals table may not exist yet during very first boot (it does after
	// AutoMigrate above, but we still double-check defensively).
	var hasTable bool
	if err := db.Raw(
		`SELECT EXISTS (SELECT FROM information_schema.tables WHERE table_name = 'goals')`,
	).Scan(&hasTable).Error; err != nil || !hasTable {
		return nil
	}

	type row struct {
		Category string
	}
	var rows []row
	if err := db.Raw(
		`SELECT DISTINCT category FROM goals
		 WHERE category IS NOT NULL AND category <> ''
		   AND category_id IS NULL
		   AND deleted_at IS NULL`,
	).Scan(&rows).Error; err != nil {
		return fmt.Errorf("scan distinct goal categories: %w", err)
	}

	if len(rows) == 0 {
		return nil
	}

	sortOrder := 0
	for _, r := range rows {
		name := r.Category
		code := slugifyCategoryCode(name)
		if code == "" {
			continue
		}

		// Idempotency: check if a category with this code already exists.
		var existing models.Category
		lookup := db.Where("code = ?", code).First(&existing)
		if lookup.Error != nil && lookup.Error != gorm.ErrRecordNotFound {
			log.Printf("category seed lookup failed for %q: %v", code, lookup.Error)
			continue
		}

		var catID uuid.UUID
		if lookup.Error == gorm.ErrRecordNotFound {
			newCat := models.Category{
				Name:      name,
				Code:      code,
				Level:     0,
				Path:      "/" + code,
				IsActive:  true,
				SortOrder: sortOrder,
			}
			if err := db.Create(&newCat).Error; err != nil {
				log.Printf("failed to create category %q from legacy goal value: %v", code, err)
				continue
			}
			catID = newCat.ID
			sortOrder++
		} else {
			catID = existing.ID
		}

		// Back-fill goals.category_id for matching free-text rows.
		if err := db.Exec(
			`UPDATE goals SET category_id = ?
			 WHERE category = ? AND category_id IS NULL AND deleted_at IS NULL`,
			catID, name,
		).Error; err != nil {
			log.Printf("failed to back-fill goals.category_id for %q: %v", name, err)
		}
	}

	return nil
}

// slugifyCategoryCode converts a free-text category name to a lowercase,
// hyphen-separated code suitable for the Category.Code field.
func slugifyCategoryCode(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	var b strings.Builder
	prevHyphen := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen && b.Len() > 0 {
				b.WriteRune('-')
				prevHyphen = true
			}
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if len(out) > 100 {
		out = out[:100]
	}
	return out
}

func Seed(db *gorm.DB, cfg *config.Config) error {
	log.Println("Seeding database...")

	// Seed default permissions
	permissions := []models.Permission{
		// User permissions
		{Name: "View Users", Code: "users:view", Module: "users", Action: "view", ActionAr: "عرض", Description: "View user list and details"},
		{Name: "Create Users", Code: "users:create", Module: "users", Action: "create", ActionAr: "إنشاء", Description: "Create new users"},
		{Name: "Update Users", Code: "users:update", Module: "users", Action: "update", ActionAr: "تحديث", Description: "Update user information"},
		{Name: "Delete Users", Code: "users:delete", Module: "users", Action: "delete", ActionAr: "حذف", Description: "Delete users"},
		{Name: "Reset User Password", Code: "users:reset_password", Module: "users", Action: "reset_password", ActionAr: "إعادة تعيين كلمة المرور", Description: "Reset user password"},

		// Role permissions
		{Name: "View Roles", Code: "roles:view", Module: "roles", Action: "view", ActionAr: "عرض", Description: "View roles list"},
		{Name: "Create Roles", Code: "roles:create", Module: "roles", Action: "create", ActionAr: "إنشاء", Description: "Create new roles"},
		{Name: "Update Roles", Code: "roles:update", Module: "roles", Action: "update", ActionAr: "تحديث", Description: "Update roles"},
		{Name: "Delete Roles", Code: "roles:delete", Module: "roles", Action: "delete", ActionAr: "حذف", Description: "Delete roles"},

		// Permission management
		{Name: "View Permissions", Code: "permissions:view", Module: "permissions", Action: "view", ActionAr: "عرض", Description: "View permissions"},
		{Name: "Manage Permissions", Code: "permissions:manage", Module: "permissions", Action: "manage", ActionAr: "إدارة", Description: "Manage permissions"},

		// Department permissions
		{Name: "View Departments", Code: "departments:view", Module: "departments", Action: "view", ActionAr: "عرض", Description: "View departments"},
		{Name: "Create Departments", Code: "departments:create", Module: "departments", Action: "create", ActionAr: "إنشاء", Description: "Create departments"},
		{Name: "Update Departments", Code: "departments:update", Module: "departments", Action: "update", ActionAr: "تحديث", Description: "Update departments"},
		{Name: "Delete Departments", Code: "departments:delete", Module: "departments", Action: "delete", ActionAr: "حذف", Description: "Delete departments"},

		// Extension assignment permissions (EPM940 telephony feature)
		{Name: "View Extensions", Code: "extensions:view", Module: "extensions", Action: "view", ActionAr: "عرض", Description: "View PBX extensions and assignment status"},
		{Name: "Assign Extensions", Code: "extensions:assign", Module: "extensions", Action: "assign", ActionAr: "تعيين", Description: "Assign/reassign PBX extensions to users"},
		{Name: "Release Extensions", Code: "extensions:release", Module: "extensions", Action: "release", ActionAr: "تحرير", Description: "Release PBX extensions from users"},
		{Name: "Create Extensions", Code: "extensions:create", Module: "extensions", Action: "create", ActionAr: "إنشاء", Description: "Create new PBX extensions on the switch"},

		// Location permissions
		{Name: "View Locations", Code: "locations:view", Module: "locations", Action: "view", ActionAr: "عرض", Description: "View locations"},
		{Name: "Create Locations", Code: "locations:create", Module: "locations", Action: "create", ActionAr: "إنشاء", Description: "Create locations"},
		{Name: "Update Locations", Code: "locations:update", Module: "locations", Action: "update", ActionAr: "تحديث", Description: "Update locations"},
		{Name: "Delete Locations", Code: "locations:delete", Module: "locations", Action: "delete", ActionAr: "حذف", Description: "Delete locations"},

		// Classification permissions
		{Name: "View Classifications", Code: "classifications:view", Module: "classifications", Action: "view", ActionAr: "عرض", Description: "View classifications"},
		{Name: "Create Classifications", Code: "classifications:create", Module: "classifications", Action: "create", ActionAr: "إنشاء", Description: "Create classifications"},
		{Name: "Update Classifications", Code: "classifications:update", Module: "classifications", Action: "update", ActionAr: "تحديث", Description: "Update classifications"},
		{Name: "Delete Classifications", Code: "classifications:delete", Module: "classifications", Action: "delete", ActionAr: "حذف", Description: "Delete classifications"},

		// Category permissions (hierarchical taxonomy used by goals)
		{Name: "View Categories", Code: "categories:view", Module: "categories", Action: "view", ActionAr: "عرض", Description: "View categories"},
		{Name: "Create Categories", Code: "categories:create", Module: "categories", Action: "create", ActionAr: "إنشاء", Description: "Create categories"},
		{Name: "Update Categories", Code: "categories:update", Module: "categories", Action: "update", ActionAr: "تحديث", Description: "Update categories"},
		{Name: "Delete Categories", Code: "categories:delete", Module: "categories", Action: "delete", ActionAr: "حذف", Description: "Delete categories"},

		// Settings permissions
		{Name: "View Settings", Code: "settings:view", Module: "settings", Action: "view", ActionAr: "عرض", Description: "View system settings"},
		{Name: "Update Settings", Code: "settings:update", Module: "settings", Action: "update", ActionAr: "تحديث", Description: "Update system settings"},

		// Workflow permissions
		{Name: "View Workflows", Code: "workflows:view", Module: "workflows", Action: "view", ActionAr: "عرض", Description: "View workflow templates"},
		{Name: "Create Workflows", Code: "workflows:create", Module: "workflows", Action: "create", ActionAr: "إنشاء", Description: "Create workflow templates"},
		{Name: "Update Workflows", Code: "workflows:update", Module: "workflows", Action: "update", ActionAr: "تحديث", Description: "Update workflow templates"},
		{Name: "Delete Workflows", Code: "workflows:delete", Module: "workflows", Action: "delete", ActionAr: "حذف", Description: "Delete workflow templates"},
		{Name: "Design Workflows", Code: "workflows:design", Module: "workflows", Action: "design", ActionAr: "تصميم", Description: "Access workflow designer"},

		// Incident permissions
		{Name: "View Incidents", Code: "incidents:view", Module: "incidents", Action: "view", ActionAr: "عرض", Description: "View incidents"},
		{Name: "Create Incidents", Code: "incidents:create", Module: "incidents", Action: "create", ActionAr: "إنشاء", Description: "Create new incidents"},
		{Name: "Update Incidents", Code: "incidents:update", Module: "incidents", Action: "update", ActionAr: "تحديث", Description: "Update incident fields"},
		{Name: "Delete Incidents", Code: "incidents:delete", Module: "incidents", Action: "delete", ActionAr: "حذف", Description: "Delete incidents"},
		{Name: "Transition Incidents", Code: "incidents:transition", Module: "incidents", Action: "transition", ActionAr: "انتقال الحالة", Description: "Execute state transitions"},
		{Name: "Assign Incidents", Code: "incidents:assign", Module: "incidents", Action: "assign", ActionAr: "تعيين", Description: "Assign/reassign incidents"},
		{Name: "Comment on Incidents", Code: "incidents:comment", Module: "incidents", Action: "comment", ActionAr: "تعليق", Description: "Add comments to incidents"},
		{Name: "View All Incidents", Code: "incidents:view_all", Module: "incidents", Action: "view_all", ActionAr: "عرض الكل", Description: "View all incidents regardless of assignment"},
		{Name: "Manage SLA", Code: "incidents:manage_sla", Module: "incidents", Action: "manage_sla", ActionAr: "إدارة اتفاقية مستوى الخدمة", Description: "Override SLA settings"},
		{Name: "Merge Incidents", Code: "incidents:merge", Module: "incidents", Action: "merge", ActionAr: "دمج", Description: "Merge multiple incidents into one"},
		{Name: "Edit Closed Incidents", Code: "incidents:edit-closed", Module: "incidents", Action: "edit_closed", ActionAr: "تعديل المغلق", Description: "Edit summary/description of closed incidents"},
		{Name: "Request Info on Incidents", Code: "incidents:request-info", Module: "incidents", Action: "request_info", ActionAr: "طلب معلومات", Description: "Request additional information from citizens"},
		{Name: "Verify Nasaq", Code: "incidents:verify-nasaq", Module: "incidents", Action: "verify_nasaq", ActionAr: "التحقق من نساق", Description: "Trigger manual Nasaq excavation permit verification for an incident"},
		{Name: "Share Incidents", Code: "incidents:share", Module: "incidents", Action: "share", ActionAr: "مشاركة", Description: "Share incident details with external parties"},
		{Name: "Filter Incidents by Reporter Phone", Code: "incidents:filter_reporter_phone", Module: "incidents", Action: "filter_reporter_phone", ActionAr: "تصفية حسب هاتف المبلغ", Description: "Filter incidents by reporter phone number"},
		{Name: "Upload Attachment Gallery", Code: "incidents:upload-attachment-gallery", Module: "incidents", Action: "upload_attachment_gallery", ActionAr: "رفع معرض المرفقات", Description: "Upload attachments to the incident attachment gallery"},

		// Request permissions
		{Name: "View Requests", Code: "requests:view", Module: "requests", Action: "view", ActionAr: "عرض", Description: "View requests"},
		{Name: "Create Requests", Code: "requests:create", Module: "requests", Action: "create", ActionAr: "إنشاء", Description: "Create new requests"},
		{Name: "Update Requests", Code: "requests:update", Module: "requests", Action: "update", ActionAr: "تحديث", Description: "Update request fields"},
		{Name: "Delete Requests", Code: "requests:delete", Module: "requests", Action: "delete", ActionAr: "حذف", Description: "Delete requests"},
		{Name: "Transition Requests", Code: "requests:transition", Module: "requests", Action: "transition", ActionAr: "انتقال الحالة", Description: "Execute request state transitions"},
		{Name: "Assign Requests", Code: "requests:assign", Module: "requests", Action: "assign", ActionAr: "تعيين", Description: "Assign/reassign requests"},
		{Name: "Comment on Requests", Code: "requests:comment", Module: "requests", Action: "comment", ActionAr: "تعليق", Description: "Add comments to requests"},
		{Name: "View All Requests", Code: "requests:view_all", Module: "requests", Action: "view_all", ActionAr: "عرض الكل", Description: "View all requests regardless of assignment"},

		// Complaint permissions
		{Name: "View Complaints", Code: "complaints:view", Module: "complaints", Action: "view", ActionAr: "عرض", Description: "View complaints"},
		{Name: "Create Complaints", Code: "complaints:create", Module: "complaints", Action: "create", ActionAr: "إنشاء", Description: "Create new complaints"},
		{Name: "Update Complaints", Code: "complaints:update", Module: "complaints", Action: "update", ActionAr: "تحديث", Description: "Update complaint fields"},
		{Name: "Delete Complaints", Code: "complaints:delete", Module: "complaints", Action: "delete", ActionAr: "حذف", Description: "Delete complaints"},
		{Name: "Transition Complaints", Code: "complaints:transition", Module: "complaints", Action: "transition", ActionAr: "انتقال الحالة", Description: "Execute complaint state transitions"},
		{Name: "Assign Complaints", Code: "complaints:assign", Module: "complaints", Action: "assign", ActionAr: "تعيين", Description: "Assign/reassign complaints"},
		{Name: "Comment on Complaints", Code: "complaints:comment", Module: "complaints", Action: "comment", ActionAr: "تعليق", Description: "Add comments to complaints"},
		{Name: "View All Complaints", Code: "complaints:view_all", Module: "complaints", Action: "view_all", ActionAr: "عرض الكل", Description: "View all complaints regardless of assignment"},

		// Query permissions
		{Name: "View Queries", Code: "queries:view", Module: "queries", Action: "view", ActionAr: "عرض", Description: "View queries"},
		{Name: "Create Queries", Code: "queries:create", Module: "queries", Action: "create", ActionAr: "إنشاء", Description: "Create new queries"},
		{Name: "Update Queries", Code: "queries:update", Module: "queries", Action: "update", ActionAr: "تحديث", Description: "Update query fields"},
		{Name: "Delete Queries", Code: "queries:delete", Module: "queries", Action: "delete", ActionAr: "حذف", Description: "Delete queries"},
		{Name: "Transition Queries", Code: "queries:transition", Module: "queries", Action: "transition", ActionAr: "انتقال الحالة", Description: "Execute query state transitions"},
		{Name: "Assign Queries", Code: "queries:assign", Module: "queries", Action: "assign", ActionAr: "تعيين", Description: "Assign/reassign queries"},
		{Name: "Comment on Queries", Code: "queries:comment", Module: "queries", Action: "comment", ActionAr: "تعليق", Description: "Add comments to queries"},
		{Name: "View All Queries", Code: "queries:view_all", Module: "queries", Action: "view_all", ActionAr: "عرض الكل", Description: "View all queries regardless of assignment"},

		// Report permissions
		{Name: "View Reports", Code: "reports:view", Module: "reports", Action: "view", ActionAr: "عرض", Description: "View reports"},
		{Name: "Create Reports", Code: "reports:create", Module: "reports", Action: "create", ActionAr: "إنشاء", Description: "Create new reports"},
		{Name: "Update Reports", Code: "reports:update", Module: "reports", Action: "update", ActionAr: "تحديث", Description: "Update reports"},
		{Name: "Delete Reports", Code: "reports:delete", Module: "reports", Action: "delete", ActionAr: "حذف", Description: "Delete reports"},

		// Action Log permissions
		{Name: "View Action Logs", Code: "action-logs:view", Module: "action-logs", Action: "view", ActionAr: "عرض", Description: "View action logs"},
		{Name: "Delete Action Logs", Code: "action-logs:delete", Module: "action-logs", Action: "delete", ActionAr: "حذف", Description: "Delete/cleanup action logs"},

		// Call Log permissions
		{Name: "View Call Logs", Code: "call-logs:view", Module: "call-logs", Action: "view", ActionAr: "عرض", Description: "View call logs"},
		{Name: "Create Call Logs", Code: "call-logs:create", Module: "call-logs", Action: "create", ActionAr: "إنشاء", Description: "Create call logs"},
		{Name: "Update Call Logs", Code: "call-logs:update", Module: "call-logs", Action: "update", ActionAr: "تحديث", Description: "Update call logs"},
		{Name: "Delete Call Logs", Code: "call-logs:delete", Module: "call-logs", Action: "delete", ActionAr: "حذف", Description: "Delete call logs"},
		{Name: "View All Call Logs", Code: "call-logs:view_all", Module: "call-logs", Action: "view_all", ActionAr: "عرض الكل", Description: "View call logs of other agents regardless of participation"},

		// Lookup permissions
		{Name: "View Lookups", Code: "lookups:view", Module: "lookups", Action: "view", ActionAr: "عرض", Description: "View lookup categories and values"},
		{Name: "Create Lookups", Code: "lookups:create", Module: "lookups", Action: "create", ActionAr: "إنشاء", Description: "Create lookup categories and values"},
		{Name: "Update Lookups", Code: "lookups:update", Module: "lookups", Action: "update", ActionAr: "تحديث", Description: "Update lookup categories and values"},
		{Name: "Delete Lookups", Code: "lookups:delete", Module: "lookups", Action: "delete", ActionAr: "حذف", Description: "Delete lookup categories and values"},

		// Application Links permissions
		{Name: "View Application Links", Code: "application-links:view", Module: "application-links", Action: "view", ActionAr: "عرض", Description: "View application links"},
		{Name: "Create Application Links", Code: "application-links:create", Module: "application-links", Action: "create", ActionAr: "إنشاء", Description: "Create application links"},
		{Name: "Update Application Links", Code: "application-links:update", Module: "application-links", Action: "update", ActionAr: "تحديث", Description: "Update application links"},
		{Name: "Delete Application Links", Code: "application-links:delete", Module: "application-links", Action: "delete", ActionAr: "حذف", Description: "Delete application links"},
		{Name: "Access Application Links on Dashboard", Code: "application-links:dashboard", Module: "application-links", Action: "dashboard", ActionAr: "لوحة التحكم", Description: "See and launch application links from the dashboard"},

		// Notification permissions
		{Name: "View Notifications", Code: "notifications:read", Module: "notifications", Action: "read", ActionAr: "قراءة", Description: "View notification logs"},
		{Name: "Send Notifications", Code: "notifications:send", Module: "notifications", Action: "send", ActionAr: "إرسال", Description: "Send email/SMS notifications"},
		{Name: "Create Draft Notifications", Code: "notifications:create", Module: "notifications", Action: "create", ActionAr: "إنشاء", Description: "Create draft notifications"},
		{Name: "Update Draft Notifications", Code: "notifications:update", Module: "notifications", Action: "update", ActionAr: "تحديث", Description: "Update draft notifications"},
		{Name: "Delete Notifications", Code: "notifications:delete", Module: "notifications", Action: "delete", ActionAr: "حذف", Description: "Delete notification logs"},

		// Communication Tracking Dashboard permissions (Call Center module)
		{Name: "View Communication Tracking Dashboard", Code: "communication-tracking:view", Module: "communication-tracking", Action: "view", ActionAr: "عرض", Description: "View the cross-channel communication tracking dashboard (SMS/Email/WhatsApp delivery status)"},
		{Name: "Update Communication Tracking", Code: "communication-tracking:update", Module: "communication-tracking", Action: "update", ActionAr: "تحديث", Description: "Manually resend a failed/undeliverable/expired SMS, Email, or WhatsApp notification"},

		// Template permissions
		{Name: "View Templates", Code: "templates:read", Module: "templates", Action: "read", ActionAr: "قراءة", Description: "View notification templates"},
		{Name: "Create Templates", Code: "templates:create", Module: "templates", Action: "create", ActionAr: "إنشاء", Description: "Create notification templates"},
		{Name: "Update Templates", Code: "templates:update", Module: "templates", Action: "update", ActionAr: "تحديث", Description: "Update notification templates"},
		{Name: "Delete Templates", Code: "templates:delete", Module: "templates", Action: "delete", ActionAr: "حذف", Description: "Delete notification templates"},

		// Escalation Group permissions
		{Name: "View Escalation Groups", Code: "escalation-groups:view", Module: "escalation-groups", Action: "view", ActionAr: "عرض", Description: "View escalation groups list and details"},
		{Name: "Create Escalation Group", Code: "escalation-groups:create", Module: "escalation-groups", Action: "create", ActionAr: "إنشاء", Description: "Create new escalation groups"},
		{Name: "Update Escalation Group", Code: "escalation-groups:update", Module: "escalation-groups", Action: "update", ActionAr: "تحديث", Description: "Update escalation group settings"},
		{Name: "Delete Escalation Group", Code: "escalation-groups:delete", Module: "escalation-groups", Action: "delete", ActionAr: "حذف", Description: "Delete escalation groups"},
		{Name: "Assign Users to Escalation Group", Code: "escalation-groups:assign_users", Module: "escalation-groups", Action: "assign_users", ActionAr: "تعيين المستخدمين", Description: "Add or remove users from escalation groups"},
		{Name: "Manage Escalation Rules", Code: "escalation-groups:manage_rules", Module: "escalation-groups", Action: "manage_rules", ActionAr: "إدارة القواعد", Description: "Configure escalation frequency, channel, and classification rules"},

		// Escalation Policy permissions
		{Name: "Create Escalation Policy", Code: "escalation-policies:create", Module: "escalation-policies", Action: "create", ActionAr: "إنشاء", Description: "Create new escalation policies"},

		// Caller Sentiment permissions
		{Name: "Create Caller Sentiment", Code: "caller-sentiment:create", Module: "caller-sentiment", Action: "create", ActionAr: "إنشاء", Description: "Record a sentiment entry after a call"},
		{Name: "View Caller Sentiments", Code: "caller-sentiment:view", Module: "caller-sentiment", Action: "view", ActionAr: "عرض", Description: "View all caller sentiment records and summaries"},

		// License permissions
		{Name: "View License", Code: "license:view", Module: "license", Action: "view", ActionAr: "عرض", Description: "View license status and info"},
		{Name: "Manage License", Code: "license:manage", Module: "license", Action: "manage", ActionAr: "إدارة", Description: "Activate, deactivate, and manage license keys"},

		// Dashboard permissions
		{Name: "Admin Dashboard", Code: "dashboard:admin", Module: "dashboard", Action: "admin", ActionAr: "مسؤول", Description: "Access admin section cards on dashboard"},
		{Name: "Incidents Dashboard", Code: "dashboard:incidents", Module: "dashboard", Action: "incidents", ActionAr: "البلاغات", Description: "Access incident cards on dashboard"},
		{Name: "Requests Dashboard", Code: "dashboard:requests", Module: "dashboard", Action: "requests", ActionAr: "الطلبات", Description: "Access request cards on dashboard"},
		{Name: "Complaints Dashboard", Code: "dashboard:complaints", Module: "dashboard", Action: "complaints", ActionAr: "الشكاوى", Description: "Access complaint cards on dashboard"},
		{Name: "Queries Dashboard", Code: "dashboard:queries", Module: "dashboard", Action: "queries", ActionAr: "الاستفسارات", Description: "Access query cards on dashboard"},
		{Name: "Workflows Dashboard", Code: "dashboard:workflows", Module: "dashboard", Action: "workflows", ActionAr: "سير العمل", Description: "Access workflow cards on dashboard"},
		{Name: "CCM Dashboard", Code: "dashboard:ccm", Module: "dashboard", Action: "ccm", ActionAr: "إدارة علاقات المواطنين", Description: "Access ccm cards on dashboard"},

		// Department-scoped permissions (configurable scoping)
		{Name: "View Only Department Incidents", Code: "incidents:view_department_only", Module: "incidents", Action: "view_department_only", ActionAr: "عرض القسم فقط", Description: "Restrict incident view to own department"},
		{Name: "View Only Department Users", Code: "users:view_department_only", Module: "users", Action: "view_department_only", ActionAr: "عرض القسم فقط", Description: "Restrict user view to own department"},
	}

	if cfg.GoalManagement.Enabled {
		permissions = append(permissions,
			models.Permission{Name: "View Goals", Code: "goals:view", Module: "goals", Action: "view", ActionAr: "عرض", Description: "View goals"},
			models.Permission{Name: "Create Goals", Code: "goals:create", Module: "goals", Action: "create", ActionAr: "إنشاء", Description: "Create new goals"},
			models.Permission{Name: "Update Goals", Code: "goals:update", Module: "goals", Action: "update", ActionAr: "تحديث", Description: "Update goals"},
			models.Permission{Name: "Delete Goals", Code: "goals:delete", Module: "goals", Action: "delete", ActionAr: "حذف", Description: "Delete goals"},
			models.Permission{Name: "Assign Goals", Code: "goals:assign", Module: "goals", Action: "assign", ActionAr: "تعيين", Description: "Assign goal collaborators"},
			models.Permission{Name: "Approve Goals", Code: "goals:approve", Module: "goals", Action: "approve", ActionAr: "موافقة", Description: "Approve/reject goal evidence"},
			models.Permission{Name: "Goals Dashboard", Code: "dashboard:goals", Module: "dashboard", Action: "goals", ActionAr: "الأهداف", Description: "Access goal cards on dashboard"},

			// KPI / Goal Management permissions
			models.Permission{Name: "Manage Goal Hierarchy", Code: "goals:manage", Module: "goals", Action: "manage", ActionAr: "إدارة", Description: "Create/update/delete goal hierarchy master data"},
			models.Permission{Name: "View KPI Dictionary", Code: "kpi:view", Module: "kpi", Action: "view", ActionAr: "عرض", Description: "View KPI definitions"},
			models.Permission{Name: "Create KPI Definitions", Code: "kpi:create", Module: "kpi", Action: "create", ActionAr: "إنشاء", Description: "Create new KPI definitions"},
			models.Permission{Name: "Update KPI Definitions", Code: "kpi:update", Module: "kpi", Action: "update", ActionAr: "تحديث", Description: "Edit KPI metadata, formula, targets"},
			models.Permission{Name: "Delete KPI Definitions", Code: "kpi:delete", Module: "kpi", Action: "delete", ActionAr: "حذف", Description: "Soft-delete KPI records"},
			models.Permission{Name: "Approve KPI Performance", Code: "kpi:approve", Module: "kpi", Action: "approve", ActionAr: "موافقة", Description: "Approve/reject KPI performance submissions from the approvals inbox"},
			models.Permission{Name: "Assign KPI Collaborators", Code: "kpi:assign", Module: "kpi", Action: "assign", ActionAr: "تعيين", Description: "Add/remove collaborators on a KPI definition"},
			models.Permission{Name: "View Performance Data", Code: "perf:view", Module: "perf", Action: "view", ActionAr: "عرض", Description: "View KPI performance data"},
			models.Permission{Name: "Submit Performance", Code: "perf:submit", Module: "perf", Action: "submit", ActionAr: "تقديم", Description: "Submit quarterly actuals for review"},
			models.Permission{Name: "Review Performance", Code: "perf:review", Module: "perf", Action: "review", ActionAr: "مراجعة", Description: "Start performance review process"},
			models.Permission{Name: "Approve Performance", Code: "perf:approve", Module: "perf", Action: "approve", ActionAr: "موافقة", Description: "Approve reviewed performance entries"},
			models.Permission{Name: "Reject Performance", Code: "perf:reject", Module: "perf", Action: "reject", ActionAr: "رفض", Description: "Reject and return for revision"},
			models.Permission{Name: "Publish Performance", Code: "perf:publish", Module: "perf", Action: "publish", ActionAr: "نشر", Description: "Publish approved performance to dashboards"},
			models.Permission{Name: "Request Performance Changes", Code: "perf:request_changes", Module: "perf", Action: "request_changes", ActionAr: "طلب تعديلات", Description: "Send a submitted performance entry back for changes"},
			models.Permission{Name: "Override Approval Lock", Code: "perf:override_lock", Module: "perf", Action: "override", ActionAr: "تجاوز", Description: "Edit or delete an already-approved performance entry"},
			models.Permission{Name: "View Targets", Code: "targets:view", Module: "targets", Action: "view", ActionAr: "عرض", Description: "View annual KPI targets"},
			models.Permission{Name: "Set Targets", Code: "targets:set", Module: "targets", Action: "set", ActionAr: "تحديد", Description: "Create/update annual targets"},
			models.Permission{Name: "Delete Targets", Code: "targets:delete", Module: "targets", Action: "delete", ActionAr: "حذف", Description: "Delete draft/returned/rejected annual targets"},
			models.Permission{Name: "Approve Targets", Code: "targets:approve", Module: "targets", Action: "approve", ActionAr: "موافقة", Description: "Approve submitted target submissions"},
			models.Permission{Name: "Reject Targets", Code: "targets:reject", Module: "targets", Action: "reject", ActionAr: "رفض", Description: "Reject submitted target submissions"},
			models.Permission{Name: "Manage Benchmarks", Code: "benchmark:manage", Module: "benchmark", Action: "manage", ActionAr: "إدارة", Description: "Create/update KPI benchmarks"},
			models.Permission{Name: "Manage Segment Data", Code: "segment:manage", Module: "segment", Action: "manage", ActionAr: "إدارة", Description: "Create/update KPI segmentation data"},
			models.Permission{Name: "Manage Corrective Actions", Code: "corrective_action:manage", Module: "corrective_action", Action: "manage", ActionAr: "إدارة", Description: "Create and close KPI corrective actions"},
		)
	}

	for _, perm := range permissions {
		var existing models.Permission
		result := db.Where("code = ?", perm.Code).First(&existing)
		if result.Error == gorm.ErrRecordNotFound {
			if err := db.Create(&perm).Error; err != nil {
				log.Printf("Failed to create permission %s: %v", perm.Code, err)
			}
		}
	}

	// Seed default roles
	var allPerms []models.Permission
	db.Find(&allPerms)

	// Admin role gets all permissions EXCEPT department-scoping restrictions.
	// "view_department_only" permissions are restrictive (they limit a user to their
	// own department) and should only be on department-scoped roles like Department
	// Manager and Supervisor — not on the Administrator role.
	var adminPerms []models.Permission
	for _, p := range allPerms {
		if !strings.HasSuffix(p.Code, "view_department_only") {
			adminPerms = append(adminPerms, p)
		}
	}

	var adminRole models.Role
	result := db.Where("code = ?", "admin").First(&adminRole)
	if result.Error == gorm.ErrRecordNotFound {
		adminRole = models.Role{
			Name:        "Administrator",
			Code:        "admin",
			Description: "Full system access",
			IsSystem:    true,
			IsActive:    true,
			Permissions: adminPerms,
		}
		db.Create(&adminRole)
	} else if db.Model(&adminRole).Association("Permissions").Count() == 0 {
		// Only seed permissions if role has none (fresh DB); skip if already configured
		db.Model(&adminRole).Association("Permissions").Append(adminPerms)
	}

	// Remove department-scoping permissions from admin role on existing databases.
	// These are restrictive and should never be on the Administrator role.
	var deptScopePerms []models.Permission
	db.Where("code LIKE ?", "%view_department_only").Find(&deptScopePerms)
	if len(deptScopePerms) > 0 {
		db.Model(&adminRole).Association("Permissions").Delete(deptScopePerms)
	}

	// User role with basic permissions
	var userRole models.Role
	result = db.Where("code = ?", "user").First(&userRole)
	if result.Error == gorm.ErrRecordNotFound {
		var viewPerms []models.Permission
		db.Where("action = ?", "view").Find(&viewPerms)
		userRole = models.Role{
			Name:        "User",
			Code:        "user",
			Description: "Basic user access",
			IsSystem:    true,
			IsActive:    true,
			Permissions: viewPerms,
		}
		db.Create(&userRole)
	} else if db.Model(&userRole).Association("Permissions").Count() == 0 {
		var viewPerms []models.Permission
		db.Where("action = ?", "view").Find(&viewPerms)
		db.Model(&userRole).Association("Permissions").Append(viewPerms)
	}

	// Manager role with broader permissions
	var managerRole models.Role
	result = db.Where("code = ?", "manager").First(&managerRole)
	if result.Error == gorm.ErrRecordNotFound {
		var managerPerms []models.Permission
		db.Where("action IN ?", []string{"view", "create", "update", "delete", "assign", "approve"}).Find(&managerPerms)
		// targets:reject uses action "reject" (mirroring perf:reject), which
		// isn't in the action list above — without this, splitting Reject out
		// of targets:approve into its own permission would silently take away
		// Manager's existing ability to reject a submitted target.
		var managerRejectPerm models.Permission
		if db.Where("code = ?", "targets:reject").First(&managerRejectPerm).Error == nil {
			managerPerms = append(managerPerms, managerRejectPerm)
		}
		managerRole = models.Role{
			Name:        "Manager",
			Code:        "manager",
			Description: "Department manager access",
			IsSystem:    true,
			IsActive:    true,
			Permissions: managerPerms,
		}
		db.Create(&managerRole)
	} else if db.Model(&managerRole).Association("Permissions").Count() == 0 {
		var managerPerms []models.Permission
		db.Where("action IN ?", []string{"view", "create", "update", "delete", "assign", "approve"}).Find(&managerPerms)
		var managerRejectPerm models.Permission
		if db.Where("code = ?", "targets:reject").First(&managerRejectPerm).Error == nil {
			managerPerms = append(managerPerms, managerRejectPerm)
		}
		db.Model(&managerRole).Association("Permissions").Replace(managerPerms)
	}

	// Department Manager role — manages users within assigned scope
	var deptManagerRole models.Role
	result = db.Where("code = ?", "department_manager").First(&deptManagerRole)
	if result.Error == gorm.ErrRecordNotFound {
		var deptManagerPerms []models.Permission
		db.Where("code IN ?", []string{
			"users:view", "users:create", "users:update",
			"incidents:view", "incidents:view_all", "incidents:transition", "incidents:assign", "incidents:comment",
			"requests:view", "requests:view_all", "requests:transition", "requests:assign", "requests:comment",
			"complaints:view", "complaints:view_all", "complaints:transition", "complaints:assign", "complaints:comment",
			"queries:view", "queries:view_all", "queries:transition", "queries:assign", "queries:comment",
			"incidents:view_department_only", "users:view_department_only",
		}).Find(&deptManagerPerms)
		deptManagerRole = models.Role{
			Name:                "Department Manager",
			Code:                "department_manager",
			Description:         "Department manager with restricted scope to assigned department, classification, and location",
			IsSystem:            true,
			IsActive:            true,
			IsDepartmentManager: true,
			Permissions:         deptManagerPerms,
		}
		db.Create(&deptManagerRole)
	} else {
		if db.Model(&deptManagerRole).Association("Permissions").Count() == 0 {
			var deptManagerPerms []models.Permission
			db.Where("code IN ?", []string{
				"users:view", "users:create", "users:update",
				"incidents:view", "incidents:view_all", "incidents:transition", "incidents:assign", "incidents:comment",
				"requests:view", "requests:view_all", "requests:transition", "requests:assign", "requests:comment",
				"complaints:view", "complaints:view_all", "complaints:transition", "complaints:assign", "complaints:comment",
				"queries:view", "queries:view_all", "queries:transition", "queries:assign", "queries:comment",
				"incidents:view_department_only", "users:view_department_only",
			}).Find(&deptManagerPerms)
			db.Model(&deptManagerRole).Association("Permissions").Append(deptManagerPerms)
		}
		if !deptManagerRole.IsDepartmentManager {
			db.Model(&deptManagerRole).Update("is_department_manager", true)
		}
	}

	// Supervisor role with view permissions for department scoping
	var supervisorRole models.Role
	result = db.Where("code = ?", "supervisor").First(&supervisorRole)
	if result.Error == gorm.ErrRecordNotFound {
		var supervisorPerms []models.Permission
		db.Where("code IN ?", []string{
			"incidents:view", "incidents:view_all", "incidents:transition", "incidents:assign", "incidents:comment",
			"requests:view", "requests:view_all", "requests:transition", "requests:assign", "requests:comment",
			"complaints:view", "complaints:view_all", "complaints:transition", "complaints:assign", "complaints:comment",
			"queries:view", "queries:view_all", "queries:transition", "queries:assign", "queries:comment",
			"users:view",
			"incidents:view_department_only", "users:view_department_only",
		}).Find(&supervisorPerms)
		supervisorRole = models.Role{
			Name:        "Supervisor",
			Code:        "supervisor",
			Description: "Department supervisor with scoped access to tickets and personnel",
			IsSystem:    true,
			IsActive:    true,
			Permissions: supervisorPerms,
		}
		db.Create(&supervisorRole)
	} else if db.Model(&supervisorRole).Association("Permissions").Count() == 0 {
		var supervisorPerms []models.Permission
		db.Where("code IN ?", []string{
			"incidents:view", "incidents:view_all", "incidents:transition", "incidents:assign", "incidents:comment",
			"requests:view", "requests:view_all", "requests:transition", "requests:assign", "requests:comment",
			"complaints:view", "complaints:view_all", "complaints:transition", "complaints:assign", "complaints:comment",
			"queries:view", "queries:view_all", "queries:transition", "queries:assign", "queries:comment",
			"users:view",
			"incidents:view_department_only", "users:view_department_only",
		}).Find(&supervisorPerms)
		db.Model(&supervisorRole).Association("Permissions").Append(supervisorPerms)
	}

	// KPI Owner and L1 Reviewer — global roles used by the KPI Workflow's
	// Draft→Reviewed→Approved→Active→Closed lifecycle transitions
	// (kpi_dictionary_workflow_seed.go's AllowedRoles). Granted kpi:view/
	// kpi:update so route-level permission checks admit them; the specific
	// "which transition" gating happens via each transition's AllowedRoles.
	kpiWorkflowRolePermCodes := []string{"kpi:view", "kpi:update"}

	var kpiOwnerRole models.Role
	result = db.Where("code = ?", "kpi_owner").First(&kpiOwnerRole)
	if result.Error == gorm.ErrRecordNotFound {
		var kpiOwnerPerms []models.Permission
		db.Where("code IN ?", kpiWorkflowRolePermCodes).Find(&kpiOwnerPerms)
		kpiOwnerRole = models.Role{
			Name:        "KPI Owner",
			Code:        "kpi_owner",
			Description: "Owns KPI records and drives their KPI Workflow lifecycle",
			IsSystem:    true,
			IsActive:    true,
			Permissions: kpiOwnerPerms,
		}
		db.Create(&kpiOwnerRole)
	} else if db.Model(&kpiOwnerRole).Association("Permissions").Count() == 0 {
		var kpiOwnerPerms []models.Permission
		db.Where("code IN ?", kpiWorkflowRolePermCodes).Find(&kpiOwnerPerms)
		db.Model(&kpiOwnerRole).Association("Permissions").Append(kpiOwnerPerms)
	}

	var l1ReviewerRole models.Role
	result = db.Where("code = ?", "l1_reviewer").First(&l1ReviewerRole)
	if result.Error == gorm.ErrRecordNotFound {
		var l1ReviewerPerms []models.Permission
		db.Where("code IN ?", kpiWorkflowRolePermCodes).Find(&l1ReviewerPerms)
		l1ReviewerRole = models.Role{
			Name:        "L1 Reviewer",
			Code:        "l1_reviewer",
			Description: "First-line reviewer for the KPI Workflow's Draft->Reviewed and Reviewed->Approved transitions",
			IsSystem:    true,
			IsActive:    true,
			Permissions: l1ReviewerPerms,
		}
		db.Create(&l1ReviewerRole)
	} else if db.Model(&l1ReviewerRole).Association("Permissions").Count() == 0 {
		var l1ReviewerPerms []models.Permission
		db.Where("code IN ?", kpiWorkflowRolePermCodes).Find(&l1ReviewerPerms)
		db.Model(&l1ReviewerRole).Association("Permissions").Append(l1ReviewerPerms)
	}

	// Grant extension-management permissions to the agent role (if it exists).
	// Admin already receives all permissions above; super admins bypass permission
	// checks entirely. We append (not replace) so any other agent permissions stay intact.
	var agentRole models.Role
	if err := db.Where("code = ?", "agent").Preload("Permissions").First(&agentRole).Error; err == nil {
		existing := make(map[uuid.UUID]bool, len(agentRole.Permissions))
		for _, p := range agentRole.Permissions {
			existing[p.ID] = true
		}
		var extPerms []models.Permission
		db.Where("module = ?", "extensions").Find(&extPerms)
		var toAdd []models.Permission
		for _, p := range extPerms {
			if !existing[p.ID] {
				toAdd = append(toAdd, p)
			}
		}
		if len(toAdd) > 0 {
			if err := db.Model(&agentRole).Association("Permissions").Append(toAdd); err != nil {
				log.Printf("Failed to grant extension permissions to agent role: %v", err)
			}
		}
	}

	// Create default super admin user
	var adminUser models.User
	result = db.Where("email = ?", "admin@automax.com").First(&adminUser)
	if result.Error == gorm.ErrRecordNotFound {
		hashedPassword, _ := utils.HashPassword("admin123")
		adminUser = models.User{
			Email:        "admin@automax.com",
			Username:     "admin",
			Password:     hashedPassword,
			FirstName:    "Super",
			LastName:     "Admin",
			IsActive:     true,
			IsSuperAdmin: true,
		}
		db.Create(&adminUser)
		db.Model(&adminUser).Association("Roles").Append(&adminRole)
	}

	// Create default departments for supervisor seeding
	var defaultDept models.Department
	if err := db.Where("name = ?", "General").First(&defaultDept).Error; err == gorm.ErrRecordNotFound {
		defaultDept = models.Department{
			Name:     "General",
			Code:     "GEN",
			IsActive: true,
		}
		db.Create(&defaultDept)
	}

	var supportDept models.Department
	if err := db.Where("name = ?", "Support").First(&supportDept).Error; err == gorm.ErrRecordNotFound {
		supportDept = models.Department{
			Name:     "Support",
			Code:     "SUP",
			IsActive: true,
		}
		db.Create(&supportDept)
	}

	// Create supervisor users with department assignments
	type supervisorSeed struct {
		Email        string
		Username     string
		FirstName    string
		LastName     string
		DepartmentID *uuid.UUID
	}
	supervisors := []supervisorSeed{
		{"supervisor1@automax.com", "supervisor1", "Ahmed", "Ali", &defaultDept.ID},
		{"supervisor2@automax.com", "supervisor2", "Sara", "Mohammed", &supportDept.ID},
	}
	for _, su := range supervisors {
		var existing models.User
		if db.Where("email = ?", su.Email).First(&existing).Error == gorm.ErrRecordNotFound {
			hashedPwd, _ := utils.HashPassword("supervisor123")
			user := models.User{
				Email:        su.Email,
				Username:     su.Username,
				Password:     hashedPwd,
				FirstName:    su.FirstName,
				LastName:     su.LastName,
				IsActive:     true,
				DepartmentID: su.DepartmentID,
			}
			db.Create(&user)
			db.Model(&user).Association("Roles").Append(&supervisorRole)
			db.Model(&models.Department{}).Where("id = ?", su.DepartmentID).Update("supervisor_id", user.ID)
		}
	}

	// Seed a default incident workflow for demo/test incidents
	seedDefaultIncidentWorkflow(db, defaultDept.ID, supportDept.ID, adminUser.ID)

	// Seed default lookup categories
	seedLookupCategories(db, cfg)

	if cfg.GoalManagement.Enabled {
		// Seed default evidence approval workflow
		seedEvidenceApprovalWorkflow(db)

		// Seed default metric & metric_value_change approval workflows
		seedMetricApprovalWorkflows(db)

		// Seed default KPI performance approval workflow
		seedKpiPerformanceWorkflow(db)

		// Seed default KPI Workflow (Draft->Reviewed->Approved->Active->Closed),
		// auto-assigned to every KPI dictionary row at creation time. Must run
		// after role seeding above (kpi_owner/l1_reviewer) since it attaches
		// those roles to states/transitions.
		seedKpiDictionaryWorkflow(db, cfg.KpiDictionaryWorkflowCode)

		// Seed default global RAG performance band (green >= 80, amber >= 60)
		seedDefaultPerformanceBand(db)

		// Seed starter data sources and segmentation dimensions
		seedKpiDataSources(db)
		seedKpiSegmentationDimensions(db)

		// Seed a small, idempotent demo dataset for Master Data, Goal
		// Management, and KPI Management so a fresh environment isn't empty
		seedGoalManagementDemoData(db, adminUser.ID)
		seedKpiEngagementDemoData(db, adminUser.ID)
	} else {
		unseedGoalManagement(db)
	}
	log.Println("Database seeding completed")
	return nil
}

// seedDefaultIncidentWorkflow creates a minimal incident workflow and demo tickets
// for the seeded departments so supervisors have data to view.
func seedDefaultIncidentWorkflow(db *gorm.DB, defaultDeptID, supportDeptID, adminUserID uuid.UUID) {
	var workflow models.Workflow
	if err := db.Where("code = ?", "incident_default").First(&workflow).Error; err == gorm.ErrRecordNotFound {
		workflow = models.Workflow{
			Name:       "Default Incident Workflow",
			Code:       "incident_default",
			RecordType: "incident",
			IsDefault:  true,
			IsActive:   true,
		}
		db.Create(&workflow)

		states := []models.WorkflowState{
			{WorkflowID: workflow.ID, Name: "New", Code: "new", StateType: "initial", Color: "#6366f1"},
			{WorkflowID: workflow.ID, Name: "In Progress", Code: "in_progress", StateType: "normal", Color: "#f59e0b"},
			{WorkflowID: workflow.ID, Name: "Resolved", Code: "resolved", StateType: "normal", Color: "#10b981"},
			{WorkflowID: workflow.ID, Name: "Closed", Code: "closed", StateType: "terminal", Color: "#6b7280"},
			{WorkflowID: workflow.ID, Name: "Rejected", Code: "rejected", StateType: "terminal", Color: "#ef4444"},
		}
		for i := range states {
			states[i].ID = uuid.New()
			db.Create(&states[i])
		}

		newState := states[0]
		inProgressState := states[1]
		resolvedState := states[2]
		closedState := states[3]

		now := time.Now()

		// Create demo incidents for each department
		demoIncidents := []models.Incident{
			{
				IncidentNumber: "INC-2026-0001",
				Title:          "Network outage in building A",
				Description:    "Users in building A cannot access the network",
				RecordType:     "incident",
				WorkflowID:     workflow.ID,
				CurrentStateID: newState.ID,
				DepartmentID:   &defaultDeptID,
				Source:         "phone",
				CreatedAt:      now,
				UpdatedAt:      now,
			},
			{
				IncidentNumber: "INC-2026-0002",
				Title:          "Email server slow response",
				Description:    "Email server taking more than 30 seconds to respond",
				RecordType:     "incident",
				WorkflowID:     workflow.ID,
				CurrentStateID: inProgressState.ID,
				DepartmentID:   &supportDeptID,
				Source:         "email",
				CreatedAt:      now,
				UpdatedAt:      now,
			},
			{
				IncidentNumber: "REQ-2026-0001",
				Title:          "New laptop request for onboarding",
				Description:    "New employee needs a laptop for onboarding",
				RecordType:     "request",
				WorkflowID:     workflow.ID,
				CurrentStateID: newState.ID,
				DepartmentID:   &defaultDeptID,
				Source:         "portal",
				CreatedAt:      now,
				UpdatedAt:      now,
			},
			{
				IncidentNumber: "REQ-2026-0002",
				Title:          "Software license renewal",
				Description:    "Renew Adobe Creative Cloud license for design team",
				RecordType:     "request",
				WorkflowID:     workflow.ID,
				CurrentStateID: resolvedState.ID,
				DepartmentID:   &supportDeptID,
				Source:         "email",
				CreatedAt:      now,
				UpdatedAt:      now,
			},
			{
				IncidentNumber: "CMP-2026-0001",
				Title:          "Rude behavior from support agent",
				Description:    "Customer complained about rude behavior from support agent",
				RecordType:     "complaint",
				WorkflowID:     workflow.ID,
				CurrentStateID: closedState.ID,
				DepartmentID:   &supportDeptID,
				Source:         "phone",
				CreatedAt:      now,
				UpdatedAt:      now,
			},
			{
				IncidentNumber: "QRY-2026-0001",
				Title:          "Inquiry about service hours",
				Description:    "Customer asking about weekend service hours",
				RecordType:     "query",
				WorkflowID:     workflow.ID,
				CurrentStateID: newState.ID,
				DepartmentID:   &defaultDeptID,
				Source:         "portal",
				CreatedAt:      now,
				UpdatedAt:      now,
			},
		}

		for _, inc := range demoIncidents {
			inc.ID = uuid.New()
			db.Create(&inc)
		}
	}
}

// unseedGoalManagement hard-deletes all goal-related rows from the DB.
// Called on every startup when GOAL_MANAGEMENT=false. Safe to run repeatedly.
// Runs inside a transaction with session_replication_role=replica to bypass
// FK constraints (incidents→workflows, incident_transition_histories→transitions)
// and the replica-identity restriction on role_permissions.
func unseedGoalManagement(db *gorm.DB) {
	log.Println("GOAL_MANAGEMENT=false — purging goal data from DB...")

	tx := db.Begin()
	if tx.Error != nil {
		log.Printf("  [unseed] ERROR starting transaction: %v", tx.Error)
		return
	}

	// Disable FK triggers and replica-identity checks for this session.
	if err := tx.Exec("SET LOCAL session_replication_role = 'replica'").Error; err != nil {
		log.Printf("  [unseed] WARNING could not set session_replication_role: %v", err)
	}

	exec := func(label, sql string) {
		r := tx.Exec(sql)
		if r.Error != nil {
			log.Printf("  [unseed] ERROR %s: %v", label, r.Error)
		} else {
			log.Printf("  [unseed] %s: %d rows deleted", label, r.RowsAffected)
		}
	}

	// Step 1: delete goal data (children before parents)
	for _, table := range []string{
		"goal_scores",
		"review_assignments",
		"review_cycles",
		"metric_value_change_transition_histories",
		"goal_metric_value_changes",
		"metric_transition_histories",
		"metric_import_batch_transition_histories",
		"metric_import_items",
		"metric_import_batches",
		"metric_histories",
		"goal_metrics",
		"evidence_transition_histories",
		"evidences",
		"goal_collaborators",
		"goal_check_ins",
		"goal_comments",
		"goal_templates",
		"goals",
	} {
		exec(table, "DROP TABLE IF EXISTS "+table+" CASCADE")
	}

	// Step 2: delete goal approval workflows
	wfCodes := "('evidence_approval','metric_approval','metric_value_change_approval')"
	wfSub := "SELECT id FROM workflows WHERE code IN " + wfCodes
	stSub := "SELECT id FROM workflow_states WHERE workflow_id IN (" + wfSub + ")"
	trSub := "SELECT id FROM workflow_transitions WHERE workflow_id IN (" + wfSub + ")"

	exec("transition_requirements (goal wf)", "DELETE FROM transition_requirements WHERE transition_id IN ("+trSub+")")
	exec("transition_actions (goal wf)", "DELETE FROM transition_actions WHERE transition_id IN ("+trSub+")")
	exec("transition_field_changes (goal wf)", "DELETE FROM transition_field_changes WHERE transition_id IN ("+trSub+")")
	// workflow_state_triggers.workflow_state_id / workflow_transition_triggers.workflow_transition_id
	exec("workflow_state_triggers (goal wf)", "DELETE FROM workflow_state_triggers WHERE workflow_state_id IN ("+stSub+")")
	exec("workflow_transition_triggers (goal wf)", "DELETE FROM workflow_transition_triggers WHERE workflow_transition_id IN ("+trSub+")")
	exec("workflow_transitions (goal wf)", "DELETE FROM workflow_transitions WHERE workflow_id IN ("+wfSub+")")
	exec("workflow_states (goal wf)", "DELETE FROM workflow_states WHERE workflow_id IN ("+wfSub+")")
	exec("workflows (goal)", "DELETE FROM workflows WHERE code IN "+wfCodes)

	// Step 3: unlink goal permissions from roles, then delete them
	permSub := "SELECT id FROM permissions WHERE module = 'goals' OR code = 'dashboard:goals'"
	exec("role_permissions (goals)", "DELETE FROM role_permissions WHERE permission_id IN ("+permSub+")")
	exec("permissions (goals)", "DELETE FROM permissions WHERE module = 'goals' OR code = 'dashboard:goals'")

	// Step 3.5: delete KPI/goal management tables (children before parents)
	log.Println("  WARNING: About to drop 27 KPI/goal tables — this IRREVERSIBLY deletes all KPI configuration and user-entered performance/target data!")
	for _, table := range []string{
		"kpi_comments",
		"kpi_check_ins",
		"kpi_collaborators",
		"kpi_evidences",
		"kpi_metrics",
		"kpi_data_sources",
		"kpi_segmentation_dimensions",
		"kpi_corrective_actions",
		"kpi_workflow_actions",
		"kpi_workflow_instances",
		"kpi_performance_bands",
		"kpi_performance_evidences",
		"kpi_segmentations",
		"kpi_benchmarks",
		"kpi_performances",
		"kpi_annual_targets",
		"award_kpis",
		"operational_kpis",
		"strategic_kpis",
		"award_sub_criteria",
		"award_criteria",
		"domains",
		"initiatives",
		"processes",
		"operational_objectives",
		"enablers",
		"pillars",
	} {
		exec(table, "DROP TABLE IF EXISTS "+table+" CASCADE")
	}

	// Step 4: delete goal-specific roles and all their join-table associations
	roleSub := "SELECT id FROM roles WHERE code IN ('goal_manager','goal_collaborator')"
	for _, step := range []struct{ label, sql string }{
		{"user_roles (goal roles)", "DELETE FROM user_roles WHERE role_id IN (" + roleSub + ")"},
		{"role_permissions (goal roles)", "DELETE FROM role_permissions WHERE role_id IN (" + roleSub + ")"},
		{"department_roles (goal roles)", "DELETE FROM department_roles WHERE role_id IN (" + roleSub + ")"},
		{"state_viewable_roles (goal roles)", "DELETE FROM state_viewable_roles WHERE role_id IN (" + roleSub + ")"},
		{"state_editable_roles (goal roles)", "DELETE FROM state_editable_roles WHERE role_id IN (" + roleSub + ")"},
		{"state_assignment_roles (goal roles)", "DELETE FROM state_assignment_roles WHERE role_id IN (" + roleSub + ")"},
		{"transition_allowed_roles (goal roles)", "DELETE FROM transition_allowed_roles WHERE role_id IN (" + roleSub + ")"},
		{"transition_assignment_roles (goal roles)", "DELETE FROM transition_assignment_roles WHERE role_id IN (" + roleSub + ")"},
		{"workflow_convert_to_request_roles (goal roles)", "DELETE FROM workflow_convert_to_request_roles WHERE role_id IN (" + roleSub + ")"},
		{"workflow_merge_allowed_roles (goal roles)", "DELETE FROM workflow_merge_allowed_roles WHERE role_id IN (" + roleSub + ")"},
		{"escalation_policy_step_targets (goal roles)", "DELETE FROM escalation_policy_step_targets WHERE role_id IN (" + roleSub + ")"},
		{"escalation_group_targets (goal roles)", "DELETE FROM escalation_group_targets WHERE role_id IN (" + roleSub + ")"},
		{"roles (goal)", "DELETE FROM roles WHERE code IN ('goal_manager','goal_collaborator')"},
	} {
		exec(step.label, step.sql)
	}

	if err := tx.Commit().Error; err != nil {
		log.Printf("  [unseed] ERROR committing: %v", err)
		tx.Rollback()
		return
	}
	log.Println("  [unseed] done")
}

func seedLookupCategories(db *gorm.DB, cfg *config.Config) {
	// Priority category
	var priorityCategory models.LookupCategory
	result := db.Where("code = ?", "PRIORITY").First(&priorityCategory)
	if result.Error == gorm.ErrRecordNotFound {
		priorityCategory = models.LookupCategory{
			Code:        "PRIORITY",
			Name:        "Priority",
			NameAr:      "الأولوية",
			Description: "Incident priority levels",
			IsSystem:    true,
			IsActive:    true,
		}
		if err := db.Create(&priorityCategory).Error; err != nil {
			log.Printf("Failed to create priority category: %v", err)
		} else {
			// Create priority values
			priorityValues := []models.LookupValue{
				{CategoryID: priorityCategory.ID, Code: "CRITICAL", Name: "Critical", NameAr: "حرج", SortOrder: 1, Color: "#EF4444", IsDefault: false, IsActive: true},
				{CategoryID: priorityCategory.ID, Code: "HIGH", Name: "High", NameAr: "عالي", SortOrder: 2, Color: "#F97316", IsDefault: false, IsActive: true},
				{CategoryID: priorityCategory.ID, Code: "MEDIUM", Name: "Medium", NameAr: "متوسط", SortOrder: 3, Color: "#EAB308", IsDefault: true, IsActive: true},
				{CategoryID: priorityCategory.ID, Code: "LOW", Name: "Low", NameAr: "منخفض", SortOrder: 4, Color: "#3B82F6", IsDefault: false, IsActive: true},
				{CategoryID: priorityCategory.ID, Code: "VERY_LOW", Name: "Very Low", NameAr: "منخفض جداً", SortOrder: 5, Color: "#6B7280", IsDefault: false, IsActive: true},
			}
			for _, v := range priorityValues {
				if err := db.Create(&v).Error; err != nil {
					log.Printf("Failed to create priority value %s: %v", v.Code, err)
				}
			}
		}
	}

	// Environment configuration category — EPM940 only
	if strings.EqualFold(strings.TrimSpace(cfg.ClientCode), constants.CLIENT_CODE.EPM940) {
		var categoryTypeCategory models.LookupCategory
		result = db.Where("code = ?", "ENV_CONFIGURATION").First(&categoryTypeCategory)
		if result.Error == gorm.ErrRecordNotFound {
			categoryTypeCategory = models.LookupCategory{
				Code:        "ENV_CONFIGURATION",
				Name:        "ENV CONFIGURATION",
				NameAr:      "إعدادات البيئة",
				Description: "System environment configuration",
				IsSystem:    true,
				IsActive:    true,
			}
			if err := db.Create(&categoryTypeCategory).Error; err != nil {
				log.Printf("Failed to create category type category: %v", err)
			} else {
				// Create category type values
				categoryTypeValues := []models.LookupValue{
					{CategoryID: categoryTypeCategory.ID, Code: "CITIZEN_ATTACHMENT_SIZE_LIMIT", Name: cfg.CitizenAttachmentSizeLimit, SortOrder: 1, Description: "Maximum size of citizen attachment", Color: "#EF4444", IsDefault: false, IsActive: true},
					{CategoryID: categoryTypeCategory.ID, Code: "INTERNAL_ATTACHMENT_SIZE_LIMIT", Name: cfg.InternalAttachmentSizeLimit, SortOrder: 2, Description: "Maximum size of internal attachment", Color: "#F97316", IsDefault: false, IsActive: true},
					{CategoryID: categoryTypeCategory.ID, Code: "MAX_DESCRIPTION_LENGTH", Name: strconv.Itoa(cfg.MaxDescriptionLength), SortOrder: 3, Description: "Maximum description length", Color: "#EAB308", IsDefault: false, IsActive: true},
					{CategoryID: categoryTypeCategory.ID, Code: "CHATBOT_URL", Name: cfg.ChatbotURL, SortOrder: 4, Description: "URL of the chatbot", Color: "#3B82F6", IsDefault: false, IsActive: true},
				}
				for _, v := range categoryTypeValues {
					if err := db.Create(&v).Error; err != nil {
						log.Printf("Failed to create category type value %s: %v", v.Code, err)
					}
				}
			}
		}
	}
}

func seedEvidenceApprovalWorkflow(db *gorm.DB) {
	// Check if workflow already exists
	var existing models.Workflow
	result := db.Where("code = ?", "evidence_approval").First(&existing)
	if result.Error == nil {
		return // Already seeded
	}

	log.Println("Seeding default evidence approval workflow...")

	workflow := models.Workflow{
		Name:        "Goal Evidence Approval",
		Code:        "evidence_approval",
		Description: "Default approval workflow for goal evidence. Supports L1 and optional L2 review.",
		RecordType:  "evidence",
		IsActive:    true,
		IsDefault:   true,
	}
	if err := db.Create(&workflow).Error; err != nil {
		log.Printf("Failed to create evidence approval workflow: %v", err)
		return
	}

	// Create states
	type stateSpec struct {
		Name      string
		Code      string
		StateType string
		Color     string
		SortOrder int
		PosX      int
		PosY      int
	}

	stateSpecs := []stateSpec{
		{"Draft", "draft", "initial", "#94a3b8", 1, 100, 200},
		{"Submitted", "submitted", "normal", "#3b82f6", 2, 300, 200},
		{"L1 Review", "l1_review", "normal", "#f59e0b", 3, 500, 200},
		{"L2 Review", "l2_review", "normal", "#f97316", 4, 700, 200},
		{"Approved", "approved", "terminal", "#22c55e", 5, 700, 400},
		{"Rejected", "rejected", "terminal", "#ef4444", 6, 500, 400},
		{"Changes Requested", "changes_requested", "normal", "#f97316", 7, 300, 400},
	}

	stateMap := make(map[string]models.WorkflowState)
	for _, spec := range stateSpecs {
		state := models.WorkflowState{
			WorkflowID: workflow.ID,
			Name:       spec.Name,
			Code:       spec.Code,
			StateType:  spec.StateType,
			Color:      spec.Color,
			SortOrder:  spec.SortOrder,
			PositionX:  spec.PosX,
			PositionY:  spec.PosY,
			IsActive:   true,
		}
		if err := db.Create(&state).Error; err != nil {
			log.Printf("Failed to create state %s: %v", spec.Code, err)
			return
		}
		stateMap[spec.Code] = state
	}

	// Create transitions
	type transSpec struct {
		Name        string
		Code        string
		From        string
		To          string
		IsRejection bool
		CommentReq  bool // whether comment is mandatory
		SortOrder   int
	}

	transSpecs := []transSpec{
		{"Submit for Review", "submit", "draft", "submitted", false, false, 1},
		{"Assign L1 Reviewer", "assign_l1", "submitted", "l1_review", false, false, 2},
		{"Approve (L1)", "approve_l1", "l1_review", "l2_review", false, false, 3},
		{"Approve (Final)", "approve_l1_final", "l1_review", "approved", false, false, 4},
		{"Request Changes", "request_changes_l1", "l1_review", "changes_requested", false, true, 5},
		{"Reject", "reject_l1", "l1_review", "rejected", true, true, 6},
		{"Approve (L2)", "approve_l2", "l2_review", "approved", false, false, 7},
		{"Request Changes", "request_changes_l2", "l2_review", "changes_requested", false, true, 8},
		{"Reject", "reject_l2", "l2_review", "rejected", true, true, 9},
		{"Resubmit", "resubmit", "changes_requested", "submitted", false, false, 10},
	}

	boolTrue := true
	for _, spec := range transSpecs {
		fromState := stateMap[spec.From]
		toState := stateMap[spec.To]

		transition := models.WorkflowTransition{
			WorkflowID:  workflow.ID,
			Name:        spec.Name,
			Code:        spec.Code,
			FromStateID: fromState.ID,
			ToStateID:   toState.ID,
			IsRejection: spec.IsRejection,
			IsActive:    true,
			SortOrder:   spec.SortOrder,
		}

		if err := db.Create(&transition).Error; err != nil {
			log.Printf("Failed to create transition %s: %v", spec.Code, err)
			return
		}

		// Add comment requirement for reject/request_changes transitions
		if spec.CommentReq {
			requirement := models.TransitionRequirement{
				TransitionID:    transition.ID,
				RequirementType: "comment",
				IsMandatory:     &boolTrue,
				ErrorMessage:    "Comment is required for this action",
			}
			if err := db.Create(&requirement).Error; err != nil {
				log.Printf("Failed to create requirement for %s: %v", spec.Code, err)
			}
		}
	}

	log.Println("Evidence approval workflow seeded successfully")
}

// seedMetricApprovalWorkflows seeds two parallel workflows used by the
// metric approval gate (item #21):
//   - record_type='metric' covers initial metric creation visibility.
//   - record_type='metric_value_change' covers each subsequent value update.
//
// Both have the same shape as the evidence workflow. Idempotent: skips if
// the workflow code already exists.
func seedMetricApprovalWorkflows(db *gorm.DB) {
	specs := []struct {
		Name        string
		Code        string
		Description string
		RecordType  string
	}{
		{
			Name:        "Goal Metric Approval",
			Code:        "metric_approval",
			Description: "Default approval workflow for goal metric definitions. Supports L1 and optional L2 review.",
			RecordType:  "metric",
		},
		{
			Name:        "Goal Metric Value Change Approval",
			Code:        "metric_value_change_approval",
			Description: "Default approval workflow for proposed metric value changes. Supports L1 and optional L2 review.",
			RecordType:  "metric_value_change",
		},
	}

	for _, s := range specs {
		var existing models.Workflow
		if err := db.Where("code = ?", s.Code).First(&existing).Error; err == nil {
			continue // already seeded
		}

		log.Printf("Seeding default %s workflow...", s.RecordType)

		workflow := models.Workflow{
			Name:        s.Name,
			Code:        s.Code,
			Description: s.Description,
			RecordType:  s.RecordType,
			IsActive:    true,
			IsDefault:   true,
		}
		if err := db.Create(&workflow).Error; err != nil {
			log.Printf("Failed to create %s workflow: %v", s.Code, err)
			continue
		}

		type stateSpec struct {
			Name      string
			Code      string
			StateType string
			Color     string
			SortOrder int
			PosX      int
			PosY      int
		}

		stateSpecs := []stateSpec{
			{"Draft", "draft", "initial", "#94a3b8", 1, 100, 200},
			{"Submitted", "submitted", "normal", "#3b82f6", 2, 300, 200},
			{"L1 Review", "l1_review", "normal", "#f59e0b", 3, 500, 200},
			{"L2 Review", "l2_review", "normal", "#f97316", 4, 700, 200},
			{"Approved", "approved", "terminal", "#22c55e", 5, 700, 400},
			{"Rejected", "rejected", "terminal", "#ef4444", 6, 500, 400},
			{"Changes Requested", "changes_requested", "normal", "#f97316", 7, 300, 400},
		}

		stateMap := make(map[string]models.WorkflowState)
		for _, sp := range stateSpecs {
			state := models.WorkflowState{
				WorkflowID: workflow.ID,
				Name:       sp.Name,
				Code:       sp.Code,
				StateType:  sp.StateType,
				Color:      sp.Color,
				SortOrder:  sp.SortOrder,
				PositionX:  sp.PosX,
				PositionY:  sp.PosY,
				IsActive:   true,
			}
			if err := db.Create(&state).Error; err != nil {
				log.Printf("Failed to create state %s for %s: %v", sp.Code, s.Code, err)
				return
			}
			stateMap[sp.Code] = state
		}

		type transSpec struct {
			Name        string
			Code        string
			From        string
			To          string
			IsRejection bool
			CommentReq  bool
			SortOrder   int
		}

		transSpecs := []transSpec{
			{"Submit for Review", "submit", "draft", "submitted", false, false, 1},
			{"Assign L1 Reviewer", "assign_l1", "submitted", "l1_review", false, false, 2},
			{"Approve (L1)", "approve_l1", "l1_review", "l2_review", false, false, 3},
			{"Approve (Final)", "approve_l1_final", "l1_review", "approved", false, false, 4},
			{"Request Changes", "request_changes_l1", "l1_review", "changes_requested", false, true, 5},
			{"Reject", "reject_l1", "l1_review", "rejected", true, true, 6},
			{"Approve (L2)", "approve_l2", "l2_review", "approved", false, false, 7},
			{"Request Changes", "request_changes_l2", "l2_review", "changes_requested", false, true, 8},
			{"Reject", "reject_l2", "l2_review", "rejected", true, true, 9},
			{"Resubmit", "resubmit", "changes_requested", "submitted", false, false, 10},
		}

		boolTrue := true
		for _, sp := range transSpecs {
			fromState := stateMap[sp.From]
			toState := stateMap[sp.To]

			transition := models.WorkflowTransition{
				WorkflowID:  workflow.ID,
				Name:        sp.Name,
				Code:        sp.Code,
				FromStateID: fromState.ID,
				ToStateID:   toState.ID,
				IsRejection: sp.IsRejection,
				IsActive:    true,
				SortOrder:   sp.SortOrder,
			}

			if err := db.Create(&transition).Error; err != nil {
				log.Printf("Failed to create transition %s for %s: %v", sp.Code, s.Code, err)
				return
			}

			if sp.CommentReq {
				requirement := models.TransitionRequirement{
					TransitionID:    transition.ID,
					RequirementType: "comment",
					IsMandatory:     &boolTrue,
					ErrorMessage:    "Comment is required for this action",
				}
				if err := db.Create(&requirement).Error; err != nil {
					log.Printf("Failed to create requirement for %s: %v", sp.Code, err)
				}
			}
		}

		log.Printf("%s workflow seeded successfully", s.Code)
	}
}

// seedDefaultPerformanceBand ensures exactly one global RAG default band row exists.
func seedDefaultPerformanceBand(db *gorm.DB) {
	var existing models.KpiPerformanceBand
	if err := db.Where("kpi_code IS NULL").First(&existing).Error; err == nil {
		return
	}
	band := models.KpiPerformanceBand{GreenMin: 80, AmberMin: 60}
	if err := db.Create(&band).Error; err != nil {
		log.Printf("Failed to seed default performance band: %v", err)
	}
}

// seedKpiDataSources seeds a starter list of governed data source names so
// the KPI dictionary forms' data source select isn't empty on a fresh install.
func seedKpiDataSources(db *gorm.DB) {
	names := []string{
		"Municipal Data Dashboard",
		"Infrastructure Asset System",
		"GIS Planning System",
		"Waste Operations System",
		"Inspection Platform",
		"Financial ERP",
		"Digital Services Platform",
		"Survey Platform",
		"Engagement Platform",
		"Quality of Life Dashboard",
		"Manual Entry",
		"System Integration",
	}
	for _, name := range names {
		var existing models.KpiDataSource
		if db.Where("name_en = ?", name).First(&existing).Error == gorm.ErrRecordNotFound {
			db.Create(&models.KpiDataSource{NameEn: name})
		}
	}
}

// seedKpiSegmentationDimensions seeds a starter list of governed segmentation
// dimension names so the segmentation form's dimension select isn't empty.
func seedKpiSegmentationDimensions(db *gorm.DB) {
	names := []string{
		"Municipality",
		"District",
		"City",
		"Road Segment",
		"Service Zone",
		"Contractor",
		"Department",
		"Customer Type",
		"Channel",
	}
	for _, name := range names {
		var existing models.KpiSegmentationDimension
		if db.Where("name_en = ?", name).First(&existing).Error == gorm.ErrRecordNotFound {
			db.Create(&models.KpiSegmentationDimension{NameEn: name})
		}
	}
}

func seedKpiPerformanceWorkflow(db *gorm.DB) {
	var wf models.Workflow
	exists := db.Where("code = ?", "kpi_performance_approval").First(&wf).Error == nil

	if !exists {
		log.Println("Seeding default KPI performance approval workflow...")

		wf = models.Workflow{
			Name:        "KPI Performance Approval",
			Code:        "kpi_performance_approval",
			Description: "Default approval workflow for KPI performance entries. Supports submit, review, approve, reject, and publish.",
			RecordType:  "kpi_performance",
			IsActive:    true,
			IsDefault:   true,
		}
		if err := db.Create(&wf).Error; err != nil {
			log.Printf("Failed to create KPI performance workflow: %v", err)
			return
		}

		type stateSpec struct {
			Name      string
			Code      string
			StateType string
			Color     string
			SortOrder int
			PosX      int
			PosY      int
		}

		stateSpecs := []stateSpec{
			{"Draft", "draft", "initial", "#94a3b8", 1, 100, 200},
			{"Submitted", "submitted", "normal", "#3b82f6", 2, 300, 200},
			{"Under Review", "under_review", "normal", "#f59e0b", 3, 500, 200},
			{"Approved", "approved", "terminal", "#22c55e", 4, 700, 200},
			{"Rejected", "rejected", "terminal", "#ef4444", 5, 700, 400},
			{"Published", "published", "terminal", "#8b5cf6", 6, 300, 400},
		}

		for _, sp := range stateSpecs {
			state := models.WorkflowState{
				WorkflowID: wf.ID,
				Name:       sp.Name,
				Code:       sp.Code,
				StateType:  sp.StateType,
				Color:      sp.Color,
				SortOrder:  sp.SortOrder,
				PositionX:  sp.PosX,
				PositionY:  sp.PosY,
				IsActive:   true,
			}
			if err := db.Create(&state).Error; err != nil {
				log.Printf("Failed to create state %s: %v", sp.Code, err)
				return
			}
		}
	}

	// Ensure transitions exist (even if the workflow was already seeded). Some
	// codes (approve/reject/request_changes) now have two rows apiece — one
	// from "submitted" and one from "under_review" — so dedup must key off
	// (code, from_state), not code alone.
	{
		stateMap := make(map[string]models.WorkflowState)
		var states []models.WorkflowState
		db.Where("workflow_id = ?", wf.ID).Find(&states)
		for _, s := range states {
			stateMap[s.Code] = s
		}

		type transSpec struct {
			Name        string
			Code        string
			From        string
			To          string
			IsRejection bool
			CommentReq  bool
			SortOrder   int
		}

		allTransSpecs := []transSpec{
			{"Submit for Review", "submit", "draft", "submitted", false, false, 1},
			{"Start Review", "review", "submitted", "under_review", false, false, 2},
			{"Approve", "approve", "under_review", "approved", false, false, 3},
			{"Reject", "reject", "under_review", "rejected", true, true, 4},
			{"Publish", "publish", "approved", "published", false, false, 5},
			{"Resubmit", "resubmit", "rejected", "submitted", false, false, 6},
			{"Request Changes", "request_changes", "under_review", "draft", false, true, 7},
			// Reviewers may act directly on a Submitted entry without first
			// clicking "Start Review" — these mirror the under_review→* transitions
			// above but from submitted, so approve/reject/request_changes work
			// immediately after submission as well as after an explicit review step.
			{"Approve", "approve", "submitted", "approved", false, false, 8},
			{"Reject", "reject", "submitted", "rejected", true, true, 9},
			{"Request Changes", "request_changes", "submitted", "draft", false, true, 10},
		}

		boolTrue := true
		for _, sp := range allTransSpecs {
			fromState, fromOk := stateMap[sp.From]
			toState, toOk := stateMap[sp.To]
			if !fromOk || !toOk {
				log.Printf("Skipping transition %s: missing state %s or %s", sp.Code, sp.From, sp.To)
				continue
			}

			var count int64
			db.Model(&models.WorkflowTransition{}).Where("workflow_id = ? AND code = ? AND from_state_id = ?", wf.ID, sp.Code, fromState.ID).Count(&count)
			if count > 0 {
				continue
			}

			transition := models.WorkflowTransition{
				WorkflowID:  wf.ID,
				Name:        sp.Name,
				Code:        sp.Code,
				FromStateID: fromState.ID,
				ToStateID:   toState.ID,
				IsRejection: sp.IsRejection,
				IsActive:    true,
				SortOrder:   sp.SortOrder,
			}

			if err := db.Create(&transition).Error; err != nil {
				log.Printf("Failed to create transition %s: %v", sp.Code, err)
				continue
			}

			if sp.CommentReq {
				errMsg := "Comment is required for this transition"
				if sp.IsRejection {
					errMsg = "Comment is required for rejection"
				} else if sp.Code == "request_changes" {
					errMsg = "Comment is required when requesting changes"
				}
				requirement := models.TransitionRequirement{
					TransitionID:    transition.ID,
					RequirementType: "comment",
					IsMandatory:     &boolTrue,
					ErrorMessage:    errMsg,
				}
				if err := db.Create(&requirement).Error; err != nil {
					log.Printf("Failed to create requirement for %s: %v", sp.Code, err)
				}
			}
		}
	}

	if !exists {
		log.Println("KPI performance approval workflow seeded successfully")
	}
}

func Close(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
