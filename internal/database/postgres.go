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
		Logger: logger.Default.LogMode(logger.Info),
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
	err := db.AutoMigrate(
		&models.Permission{},
		&models.Role{},
		&models.Classification{},
		&models.Location{},
		&models.Department{},
		&models.User{},
		&models.ActionLog{},
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
		&models.IncidentTransitionHistory{},
		&models.IncidentRevision{},
		// Report models
		&models.Report{},
		&models.ReportExecution{},
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

	log.Println("Database seeding completed")
	return nil
}

func Close(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}
