package database

import (
	"fmt"
	"log"

	"github.com/automax/backend/internal/config"
	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/pkg/utils"
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

func Migrate(db *gorm.DB) error {
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

	err := db.AutoMigrate(
		&models.Permission{},
		&models.Role{},
		&models.Classification{},
		&models.Location{},
		&models.Department{},
		&models.User{},
		&models.ActionLog{},
		&models.CallLog{},
		// Lookup models
		&models.LookupCategory{},
		&models.LookupValue{},
		// Workflow models
		&models.Workflow{},
		&models.WorkflowState{},
		&models.WorkflowTransition{},
		&models.TransitionRequirement{},
		&models.TransitionAction{},
		// Incident models
		&models.Incident{},
		&models.IncidentComment{},
		&models.IncidentAttachment{},
		&models.IncidentFeedback{},
		&models.IncidentTransitionHistory{},
		&models.IncidentRevision{},
		// Report models
		&models.Report{},
		&models.ReportExecution{},
		&models.ReportTemplate{},
	)
	if err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}
	log.Println("Database migrations completed")
	return nil
}

func Seed(db *gorm.DB) error {
	log.Println("Seeding database...")

	// Seed default permissions
	permissions := []models.Permission{
		// User permissions
		{Name: "View Users", Code: "users:view", Module: "users", Action: "view", Description: "View user list and details"},
		{Name: "Create Users", Code: "users:create", Module: "users", Action: "create", Description: "Create new users"},
		{Name: "Update Users", Code: "users:update", Module: "users", Action: "update", Description: "Update user information"},
		{Name: "Delete Users", Code: "users:delete", Module: "users", Action: "delete", Description: "Delete users"},

		// Role permissions
		{Name: "View Roles", Code: "roles:view", Module: "roles", Action: "view", Description: "View roles list"},
		{Name: "Create Roles", Code: "roles:create", Module: "roles", Action: "create", Description: "Create new roles"},
		{Name: "Update Roles", Code: "roles:update", Module: "roles", Action: "update", Description: "Update roles"},
		{Name: "Delete Roles", Code: "roles:delete", Module: "roles", Action: "delete", Description: "Delete roles"},

		// Permission management
		{Name: "View Permissions", Code: "permissions:view", Module: "permissions", Action: "view", Description: "View permissions"},
		{Name: "Manage Permissions", Code: "permissions:manage", Module: "permissions", Action: "manage", Description: "Manage permissions"},

		// Department permissions
		{Name: "View Departments", Code: "departments:view", Module: "departments", Action: "view", Description: "View departments"},
		{Name: "Create Departments", Code: "departments:create", Module: "departments", Action: "create", Description: "Create departments"},
		{Name: "Update Departments", Code: "departments:update", Module: "departments", Action: "update", Description: "Update departments"},
		{Name: "Delete Departments", Code: "departments:delete", Module: "departments", Action: "delete", Description: "Delete departments"},

		// Location permissions
		{Name: "View Locations", Code: "locations:view", Module: "locations", Action: "view", Description: "View locations"},
		{Name: "Create Locations", Code: "locations:create", Module: "locations", Action: "create", Description: "Create locations"},
		{Name: "Update Locations", Code: "locations:update", Module: "locations", Action: "update", Description: "Update locations"},
		{Name: "Delete Locations", Code: "locations:delete", Module: "locations", Action: "delete", Description: "Delete locations"},

		// Classification permissions
		{Name: "View Classifications", Code: "classifications:view", Module: "classifications", Action: "view", Description: "View classifications"},
		{Name: "Create Classifications", Code: "classifications:create", Module: "classifications", Action: "create", Description: "Create classifications"},
		{Name: "Update Classifications", Code: "classifications:update", Module: "classifications", Action: "update", Description: "Update classifications"},
		{Name: "Delete Classifications", Code: "classifications:delete", Module: "classifications", Action: "delete", Description: "Delete classifications"},

		// Settings permissions
		{Name: "View Settings", Code: "settings:view", Module: "settings", Action: "view", Description: "View system settings"},
		{Name: "Update Settings", Code: "settings:update", Module: "settings", Action: "update", Description: "Update system settings"},

		// Workflow permissions
		{Name: "View Workflows", Code: "workflows:view", Module: "workflows", Action: "view", Description: "View workflow templates"},
		{Name: "Create Workflows", Code: "workflows:create", Module: "workflows", Action: "create", Description: "Create workflow templates"},
		{Name: "Update Workflows", Code: "workflows:update", Module: "workflows", Action: "update", Description: "Update workflow templates"},
		{Name: "Delete Workflows", Code: "workflows:delete", Module: "workflows", Action: "delete", Description: "Delete workflow templates"},
		{Name: "Design Workflows", Code: "workflows:design", Module: "workflows", Action: "design", Description: "Access workflow designer"},

		// Incident permissions
		{Name: "View Incidents", Code: "incidents:view", Module: "incidents", Action: "view", Description: "View incidents"},
		{Name: "Create Incidents", Code: "incidents:create", Module: "incidents", Action: "create", Description: "Create new incidents"},
		{Name: "Update Incidents", Code: "incidents:update", Module: "incidents", Action: "update", Description: "Update incident fields"},
		{Name: "Delete Incidents", Code: "incidents:delete", Module: "incidents", Action: "delete", Description: "Delete incidents"},
		{Name: "Transition Incidents", Code: "incidents:transition", Module: "incidents", Action: "transition", Description: "Execute state transitions"},
		{Name: "Assign Incidents", Code: "incidents:assign", Module: "incidents", Action: "assign", Description: "Assign/reassign incidents"},
		{Name: "Comment on Incidents", Code: "incidents:comment", Module: "incidents", Action: "comment", Description: "Add comments to incidents"},
		{Name: "View All Incidents", Code: "incidents:view_all", Module: "incidents", Action: "view_all", Description: "View all incidents regardless of assignment"},
		{Name: "Manage SLA", Code: "incidents:manage_sla", Module: "incidents", Action: "manage_sla", Description: "Override SLA settings"},

		// Request permissions
		{Name: "View Requests", Code: "requests:view", Module: "requests", Action: "view", Description: "View requests"},
		{Name: "Create Requests", Code: "requests:create", Module: "requests", Action: "create", Description: "Create new requests"},
		{Name: "Update Requests", Code: "requests:update", Module: "requests", Action: "update", Description: "Update request fields"},
		{Name: "Delete Requests", Code: "requests:delete", Module: "requests", Action: "delete", Description: "Delete requests"},
		{Name: "Transition Requests", Code: "requests:transition", Module: "requests", Action: "transition", Description: "Execute request state transitions"},
		{Name: "Assign Requests", Code: "requests:assign", Module: "requests", Action: "assign", Description: "Assign/reassign requests"},
		{Name: "Comment on Requests", Code: "requests:comment", Module: "requests", Action: "comment", Description: "Add comments to requests"},
		{Name: "View All Requests", Code: "requests:view_all", Module: "requests", Action: "view_all", Description: "View all requests regardless of assignment"},

		// Complaint permissions
		{Name: "View Complaints", Code: "complaints:view", Module: "complaints", Action: "view", Description: "View complaints"},
		{Name: "Create Complaints", Code: "complaints:create", Module: "complaints", Action: "create", Description: "Create new complaints"},
		{Name: "Update Complaints", Code: "complaints:update", Module: "complaints", Action: "update", Description: "Update complaint fields"},
		{Name: "Delete Complaints", Code: "complaints:delete", Module: "complaints", Action: "delete", Description: "Delete complaints"},
		{Name: "Transition Complaints", Code: "complaints:transition", Module: "complaints", Action: "transition", Description: "Execute complaint state transitions"},
		{Name: "Assign Complaints", Code: "complaints:assign", Module: "complaints", Action: "assign", Description: "Assign/reassign complaints"},
		{Name: "Comment on Complaints", Code: "complaints:comment", Module: "complaints", Action: "comment", Description: "Add comments to complaints"},
		{Name: "View All Complaints", Code: "complaints:view_all", Module: "complaints", Action: "view_all", Description: "View all complaints regardless of assignment"},

		// Query permissions
		{Name: "View Queries", Code: "queries:view", Module: "queries", Action: "view", Description: "View queries"},
		{Name: "Create Queries", Code: "queries:create", Module: "queries", Action: "create", Description: "Create new queries"},
		{Name: "Update Queries", Code: "queries:update", Module: "queries", Action: "update", Description: "Update query fields"},
		{Name: "Delete Queries", Code: "queries:delete", Module: "queries", Action: "delete", Description: "Delete queries"},
		{Name: "Transition Queries", Code: "queries:transition", Module: "queries", Action: "transition", Description: "Execute query state transitions"},
		{Name: "Assign Queries", Code: "queries:assign", Module: "queries", Action: "assign", Description: "Assign/reassign queries"},
		{Name: "Comment on Queries", Code: "queries:comment", Module: "queries", Action: "comment", Description: "Add comments to queries"},
		{Name: "View All Queries", Code: "queries:view_all", Module: "queries", Action: "view_all", Description: "View all queries regardless of assignment"},

		// Report permissions
		{Name: "View Reports", Code: "reports:view", Module: "reports", Action: "view", Description: "View reports"},
		{Name: "Create Reports", Code: "reports:create", Module: "reports", Action: "create", Description: "Create new reports"},
		{Name: "Update Reports", Code: "reports:update", Module: "reports", Action: "update", Description: "Update reports"},
		{Name: "Delete Reports", Code: "reports:delete", Module: "reports", Action: "delete", Description: "Delete reports"},

		// Action Log permissions
		{Name: "View Action Logs", Code: "action-logs:view", Module: "action-logs", Action: "view", Description: "View action logs"},
		{Name: "Delete Action Logs", Code: "action-logs:delete", Module: "action-logs", Action: "delete", Description: "Delete/cleanup action logs"},

		// Call Log permissions
		{Name: "View Call Logs", Code: "call-logs:view", Module: "call-logs", Action: "view", Description: "View call logs"},
		{Name: "Create Call Logs", Code: "call-logs:create", Module: "call-logs", Action: "create", Description: "Create call logs"},
		{Name: "Update Call Logs", Code: "call-logs:update", Module: "call-logs", Action: "update", Description: "Update call logs"},
		{Name: "Delete Call Logs", Code: "call-logs:delete", Module: "call-logs", Action: "delete", Description: "Delete call logs"},

		// Lookup permissions
		{Name: "View Lookups", Code: "lookups:view", Module: "lookups", Action: "view", Description: "View lookup categories and values"},
		{Name: "Create Lookups", Code: "lookups:create", Module: "lookups", Action: "create", Description: "Create lookup categories and values"},
		{Name: "Update Lookups", Code: "lookups:update", Module: "lookups", Action: "update", Description: "Update lookup categories and values"},
		{Name: "Delete Lookups", Code: "lookups:delete", Module: "lookups", Action: "delete", Description: "Delete lookup categories and values"},

		// Dashboard permissions
		{Name: "Admin Dashboard", Code: "dashboard:admin", Module: "dashboard", Action: "admin", Description: "Access admin section cards on dashboard"},
		{Name: "Incidents Dashboard", Code: "dashboard:incidents", Module: "dashboard", Action: "incidents", Description: "Access incident cards on dashboard"},
		{Name: "Requests Dashboard", Code: "dashboard:requests", Module: "dashboard", Action: "requests", Description: "Access request cards on dashboard"},
		{Name: "Complaints Dashboard", Code: "dashboard:complaints", Module: "dashboard", Action: "complaints", Description: "Access complaint cards on dashboard"},
		{Name: "Queries Dashboard", Code: "dashboard:queries", Module: "dashboard", Action: "queries", Description: "Access query cards on dashboard"},
		{Name: "Workflows Dashboard", Code: "dashboard:workflows", Module: "dashboard", Action: "workflows", Description: "Access workflow cards on dashboard"},
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

	// Admin role gets all permissions
	var adminRole models.Role
	result := db.Where("code = ?", "admin").First(&adminRole)
	if result.Error == gorm.ErrRecordNotFound {
		adminRole = models.Role{
			Name:        "Administrator",
			Code:        "admin",
			Description: "Full system access",
			IsSystem:    true,
			IsActive:    true,
			Permissions: allPerms,
		}
		db.Create(&adminRole)
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
	}

	// Manager role with broader permissions
	var managerRole models.Role
	result = db.Where("code = ?", "manager").First(&managerRole)
	if result.Error == gorm.ErrRecordNotFound {
		var managerPerms []models.Permission
		db.Where("action IN ?", []string{"view", "create", "update"}).Find(&managerPerms)
		managerRole = models.Role{
			Name:        "Manager",
			Code:        "manager",
			Description: "Department manager access",
			IsSystem:    true,
			IsActive:    true,
			Permissions: managerPerms,
		}
		db.Create(&managerRole)
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

	// Seed default lookup categories
	seedLookupCategories(db)

	log.Println("Database seeding completed")
	return nil
}

func seedLookupCategories(db *gorm.DB) {
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

	// Severity category
	var severityCategory models.LookupCategory
	result = db.Where("code = ?", "SEVERITY").First(&severityCategory)
	if result.Error == gorm.ErrRecordNotFound {
		severityCategory = models.LookupCategory{
			Code:        "SEVERITY",
			Name:        "Severity",
			NameAr:      "الخطورة",
			Description: "Incident severity levels",
			IsSystem:    true,
			IsActive:    true,
		}
		if err := db.Create(&severityCategory).Error; err != nil {
			log.Printf("Failed to create severity category: %v", err)
		} else {
			// Create severity values
			severityValues := []models.LookupValue{
				{CategoryID: severityCategory.ID, Code: "CRITICAL", Name: "Critical", NameAr: "حرج", SortOrder: 1, Color: "#EF4444", IsDefault: false, IsActive: true},
				{CategoryID: severityCategory.ID, Code: "MAJOR", Name: "Major", NameAr: "رئيسي", SortOrder: 2, Color: "#F97316", IsDefault: false, IsActive: true},
				{CategoryID: severityCategory.ID, Code: "MODERATE", Name: "Moderate", NameAr: "معتدل", SortOrder: 3, Color: "#EAB308", IsDefault: true, IsActive: true},
				{CategoryID: severityCategory.ID, Code: "MINOR", Name: "Minor", NameAr: "ثانوي", SortOrder: 4, Color: "#3B82F6", IsDefault: false, IsActive: true},
				{CategoryID: severityCategory.ID, Code: "COSMETIC", Name: "Cosmetic", NameAr: "تجميلي", SortOrder: 5, Color: "#6B7280", IsDefault: false, IsActive: true},
			}
			for _, v := range severityValues {
				if err := db.Create(&v).Error; err != nil {
					log.Printf("Failed to create severity value %s: %v", v.Code, err)
				}
			}
		}
	}
}

func Close(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
