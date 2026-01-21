package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/automax/backend/internal/config"
	"github.com/automax/backend/internal/database"
	"github.com/automax/backend/internal/handlers"
	"github.com/automax/backend/internal/middleware"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/internal/services"
	"github.com/automax/backend/internal/storage"
	"github.com/automax/backend/pkg/utils"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/go-playground/validator/v10"
)

func main() {
	cfg := config.Load()

	db, err := database.Connect(&cfg.Database)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer database.Close(db)

	if err := database.Migrate(db); err != nil {
		log.Fatalf("Failed to run migrations: %v", err)
	}

	// Seed default data
	if err := database.Seed(db); err != nil {
		log.Printf("Warning: Failed to seed database: %v", err)
	}

	redisClient, err := database.ConnectRedis(&cfg.Redis)
	if err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}
	defer database.CloseRedis(redisClient)

	minioStorage, err := storage.NewMinIOStorage(&cfg.MinIO)
	if err != nil {
		log.Fatalf("Failed to connect to MinIO: %v", err)
	}

	jwtManager := utils.NewJWTManager(cfg.JWT.Secret, cfg.JWT.ExpireHour)
	sessionStore := database.NewSessionStore(redisClient)

	// Initialize repositories
	userRepo := repository.NewUserRepository(db)
	classificationRepo := repository.NewClassificationRepository(db)
	locationRepo := repository.NewLocationRepository(db)
	departmentRepo := repository.NewDepartmentRepository(db)
	roleRepo := repository.NewRoleRepository(db)
	permissionRepo := repository.NewPermissionRepository(db)
	actionLogRepo := repository.NewActionLogRepository(db)
	workflowRepo := repository.NewWorkflowRepository(db)
	incidentRepo := repository.NewIncidentRepository(db)
	reportRepo := repository.NewReportRepository(db)
	lookupRepo := repository.NewLookupRepository(db)

	// Initialize services
	userService := services.NewUserService(userRepo, jwtManager, sessionStore, minioStorage, cfg)
	actionLogService := services.NewActionLogService(actionLogRepo)
	workflowService := services.NewWorkflowService(workflowRepo)
	incidentService := services.NewIncidentService(incidentRepo, workflowRepo, userRepo, minioStorage)
	reportService := services.NewReportService(reportRepo)

	// Initialize and start SLA Monitor (checks every 5 minutes)
	slaMonitor := services.NewSLAMonitor(incidentRepo, 5*time.Minute)
	ctx := context.Background()
	slaMonitor.Start(ctx)
	defer slaMonitor.Stop()

	// Initialize validator
	validate := validator.New()

	// Initialize handlers
	userHandler := handlers.NewUserHandler(userService, minioStorage)
	healthHandler := handlers.NewHealthHandler()
	classificationHandler := handlers.NewClassificationHandler(classificationRepo)
	locationHandler := handlers.NewLocationHandler(locationRepo)
	departmentHandler := handlers.NewDepartmentHandler(departmentRepo)
	roleHandler := handlers.NewRoleHandler(roleRepo, permissionRepo)
	actionLogHandler := handlers.NewActionLogHandler(actionLogService, validate)
	workflowHandler := handlers.NewWorkflowHandler(workflowService)
	incidentHandler := handlers.NewIncidentHandler(incidentService, userRepo, minioStorage)
	reportHandler := handlers.NewReportHandler(reportService)
	lookupHandler := handlers.NewLookupHandler(lookupRepo)

	// Initialize middleware
	authMiddleware := middleware.NewAuthMiddleware(jwtManager, sessionStore)

	app := fiber.New(fiber.Config{
		AppName:      "Automax Backend",
		ErrorHandler: customErrorHandler,
	})

	app.Use(recover.New())
	app.Use(logger.New(logger.Config{
		Format: "[${time}] ${status} - ${latency} ${method} ${path}\n",
	}))
	app.Use(cors.New(cors.Config{
		AllowOrigins:     "http://localhost:3000,http://localhost:5173",
		AllowMethods:     "GET,POST,PUT,DELETE,PATCH,OPTIONS",
		AllowHeaders:     "Origin,Content-Type,Accept,Authorization",
		AllowCredentials: true,
	}))

	api := app.Group("/api")
	v1 := api.Group("/v1")

	// Health routes
	v1.Get("/health", healthHandler.Health)
	v1.Get("/ready", healthHandler.Ready)

	// Auth routes
	auth := v1.Group("/auth")
	auth.Post("/register", userHandler.Register)
	auth.Post("/login", userHandler.Login)
	auth.Post("/refresh", userHandler.RefreshToken)
	auth.Post("/logout", authMiddleware.Authenticate(), userHandler.Logout)

	// User routes
	users := v1.Group("/users")
	users.Get("/me", authMiddleware.Authenticate(), userHandler.GetProfile)
	users.Put("/me", authMiddleware.Authenticate(), userHandler.UpdateProfile)
	users.Post("/me/avatar", authMiddleware.Authenticate(), userHandler.UploadAvatar)
	users.Put("/me/password", authMiddleware.Authenticate(), userHandler.ChangePassword)
	users.Delete("/me", authMiddleware.Authenticate(), userHandler.DeleteAccount)

	// Incident routes (authenticated users)
	incidents := v1.Group("/incidents", authMiddleware.Authenticate())
	incidents.Post("/", incidentHandler.CreateIncident)
	incidents.Get("/", incidentHandler.ListIncidents)
	incidents.Get("/stats", incidentHandler.GetStats)
	incidents.Get("/my-assigned", incidentHandler.GetMyAssigned)
	incidents.Get("/my-reported", incidentHandler.GetMyReported)
	incidents.Get("/sla-breached", incidentHandler.GetSLABreached)
	incidents.Get("/:id", incidentHandler.GetIncident)
	incidents.Put("/:id", incidentHandler.UpdateIncident)
	incidents.Delete("/:id", incidentHandler.DeleteIncident)
	incidents.Post("/:id/transition", incidentHandler.ExecuteTransition)
	incidents.Post("/:id/convert-to-request", incidentHandler.ConvertToRequest)
	incidents.Get("/:id/available-transitions", incidentHandler.GetAvailableTransitions)
	incidents.Get("/:id/history", incidentHandler.GetTransitionHistory)
	incidents.Post("/:id/comments", incidentHandler.AddComment)
	incidents.Get("/:id/comments", incidentHandler.ListComments)
	incidents.Put("/:id/comments/:comment_id", incidentHandler.UpdateComment)
	incidents.Delete("/:id/comments/:comment_id", incidentHandler.DeleteComment)
	incidents.Post("/:id/attachments", incidentHandler.UploadAttachment)
	incidents.Get("/:id/attachments", incidentHandler.ListAttachments)
	incidents.Delete("/:id/attachments/:attachment_id", incidentHandler.DeleteAttachment)
	incidents.Put("/:id/assign", incidentHandler.AssignIncident)
	incidents.Get("/:id/revisions", incidentHandler.ListRevisions)

	// Attachment download route
	attachments := v1.Group("/attachments", authMiddleware.Authenticate())
	attachments.Get("/:attachment_id", incidentHandler.DownloadAttachment)

	// Admin routes
	admin := v1.Group("/admin", authMiddleware.Authenticate())

	// User management
	admin.Get("/users", userHandler.ListUsers)
	admin.Post("/users", userHandler.AdminCreateUser)
	admin.Post("/users/match", userHandler.MatchUsers)
	admin.Get("/users/:id", userHandler.GetUser)
	admin.Put("/users/:id", userHandler.AdminUpdateUser)

	// Classification routes
	classifications := admin.Group("/classifications")
	classifications.Post("/", classificationHandler.Create)
	classifications.Get("/", classificationHandler.List)
	classifications.Get("/tree", classificationHandler.GetTree)
	classifications.Get("/children", classificationHandler.GetChildren)
	classifications.Get("/:id", classificationHandler.GetByID)
	classifications.Put("/:id", classificationHandler.Update)
	classifications.Delete("/:id", classificationHandler.Delete)

	// Location routes
	locations := admin.Group("/locations")
	locations.Post("/", locationHandler.Create)
	locations.Get("/", locationHandler.List)
	locations.Get("/tree", locationHandler.GetTree)
	locations.Get("/children", locationHandler.GetChildren)
	locations.Get("/by-type", locationHandler.GetByType)
	locations.Get("/:id", locationHandler.GetByID)
	locations.Put("/:id", locationHandler.Update)
	locations.Delete("/:id", locationHandler.Delete)

	// Department routes
	departments := admin.Group("/departments")
	departments.Post("/", departmentHandler.Create)
	departments.Get("/", departmentHandler.List)
	departments.Get("/tree", departmentHandler.GetTree)
	departments.Get("/children", departmentHandler.GetChildren)
	departments.Post("/match", departmentHandler.MatchDepartment)
	departments.Get("/:id", departmentHandler.GetByID)
	departments.Put("/:id", departmentHandler.Update)
	departments.Delete("/:id", departmentHandler.Delete)

	// Role routes
	roles := admin.Group("/roles")
	roles.Post("/", roleHandler.CreateRole)
	roles.Get("/", roleHandler.ListRoles)
	roles.Get("/:id", roleHandler.GetRole)
	roles.Put("/:id", roleHandler.UpdateRole)
	roles.Delete("/:id", roleHandler.DeleteRole)
	roles.Post("/:id/permissions", roleHandler.AssignPermissions)

	// Permission routes
	permissions := admin.Group("/permissions")
	permissions.Post("/", roleHandler.CreatePermission)
	permissions.Get("/", roleHandler.ListPermissions)
	permissions.Get("/modules", roleHandler.GetModules)
	permissions.Get("/:id", roleHandler.GetPermission)
	permissions.Put("/:id", roleHandler.UpdatePermission)
	permissions.Delete("/:id", roleHandler.DeletePermission)

	// Action Log routes
	actionLogs := admin.Group("/action-logs")
	actionLogs.Get("/", actionLogHandler.ListActionLogs)
	actionLogs.Get("/stats", actionLogHandler.GetStats)
	actionLogs.Get("/filter-options", actionLogHandler.GetFilterOptions)
	actionLogs.Get("/user/:id", actionLogHandler.GetUserActions)
	actionLogs.Get("/:id", actionLogHandler.GetActionLog)
	actionLogs.Delete("/cleanup", actionLogHandler.CleanupOldLogs)

	// Workflow routes
	workflows := admin.Group("/workflows")
	workflows.Post("/", workflowHandler.CreateWorkflow)
	workflows.Get("/", workflowHandler.ListWorkflows)
	workflows.Post("/match", workflowHandler.MatchWorkflow) // For mobile apps - matches workflow based on criteria
	workflows.Get("/by-classification/:classification_id", workflowHandler.GetWorkflowByClassification)
	workflows.Get("/:id", workflowHandler.GetWorkflow)
	workflows.Put("/:id", workflowHandler.UpdateWorkflow)
	workflows.Delete("/:id", workflowHandler.DeleteWorkflow)
	workflows.Post("/:id/duplicate", workflowHandler.DuplicateWorkflow)
	workflows.Post("/:id/classifications", workflowHandler.AssignClassifications)
	workflows.Get("/:id/initial-state", workflowHandler.GetInitialState)

	// Workflow state routes
	workflows.Post("/:id/states", workflowHandler.CreateState)
	workflows.Get("/:id/states", workflowHandler.ListStates)
	workflows.Put("/:id/states/:state_id", workflowHandler.UpdateState)
	workflows.Delete("/:id/states/:state_id", workflowHandler.DeleteState)
	workflows.Get("/states/:state_id/transitions", workflowHandler.GetTransitionsFromState)

	// Workflow transition routes
	workflows.Post("/:id/transitions", workflowHandler.CreateTransition)
	workflows.Get("/:id/transitions", workflowHandler.ListTransitions)
	workflows.Put("/:id/transitions/:transition_id", workflowHandler.UpdateTransition)
	workflows.Delete("/:id/transitions/:transition_id", workflowHandler.DeleteTransition)

	// Transition configuration routes
	transitions := admin.Group("/transitions")
	transitions.Put("/:id/roles", workflowHandler.SetTransitionRoles)
	transitions.Put("/:id/requirements", workflowHandler.SetTransitionRequirements)
	transitions.Put("/:id/actions", workflowHandler.SetTransitionActions)

	// Report routes
	reports := admin.Group("/reports")
	reports.Post("/", reportHandler.CreateReport)
	reports.Get("/", reportHandler.ListReports)
	reports.Get("/data-sources", reportHandler.GetDataSources)
	reports.Post("/preview", reportHandler.PreviewReport)
	reports.Get("/:id", reportHandler.GetReport)
	reports.Put("/:id", reportHandler.UpdateReport)
	reports.Delete("/:id", reportHandler.DeleteReport)
	reports.Post("/:id/duplicate", reportHandler.DuplicateReport)
	reports.Post("/:id/execute", reportHandler.ExecuteReport)
	reports.Get("/:id/executions", reportHandler.GetExecutionHistory)

	// Lookup routes (admin)
	lookups := admin.Group("/lookups")
	lookups.Post("/categories", lookupHandler.CreateCategory)
	lookups.Get("/categories", lookupHandler.ListCategories)
	lookups.Get("/categories/:id", lookupHandler.GetCategoryByID)
	lookups.Put("/categories/:id", lookupHandler.UpdateCategory)
	lookups.Delete("/categories/:id", lookupHandler.DeleteCategory)
	lookups.Post("/categories/:id/values", lookupHandler.CreateValue)
	lookups.Get("/categories/:id/values", lookupHandler.ListValuesByCategory)
	lookups.Get("/values/:id", lookupHandler.GetValueByID)
	lookups.Put("/values/:id", lookupHandler.UpdateValue)
	lookups.Delete("/values/:id", lookupHandler.DeleteValue)

	// Public lookup endpoint (by category code) - accessible to authenticated users
	v1.Get("/lookups/:code", authMiddleware.Authenticate(), lookupHandler.GetValuesByCategoryCode)

	go func() {
		addr := fmt.Sprintf("%s:%s", cfg.Server.Host, cfg.Server.Port)
		log.Printf("Server starting on %s", addr)
		if err := app.Listen(addr); err != nil {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")
	if err := app.Shutdown(); err != nil {
		log.Printf("Error during shutdown: %v", err)
	}
	log.Println("Server stopped")
}

func customErrorHandler(c *fiber.Ctx, err error) error {
	code := fiber.StatusInternalServerError
	message := "Internal Server Error"

	if e, ok := err.(*fiber.Error); ok {
		code = e.Code
		message = e.Message
	}

	return c.Status(code).JSON(fiber.Map{
		"success": false,
		"error":   message,
	})
}
