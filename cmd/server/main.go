package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/automax/backend/internal/config"
	"github.com/automax/backend/internal/database"
	"github.com/automax/backend/internal/handlers"
	"github.com/automax/backend/internal/licensing"
	"github.com/automax/backend/internal/licensing/devseed"
	"github.com/automax/backend/internal/middleware"
	"github.com/automax/backend/internal/natsclient"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/internal/services"
	"github.com/automax/backend/internal/storage"
	"github.com/automax/backend/pkg/constants"
	"github.com/automax/backend/pkg/utils"
	"github.com/automax/backend/pkg/validation"
	"github.com/go-playground/validator/v10"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/joho/godotenv"
)

func main() {
	godotenv.Load() // must be before config.Load() so .env values are in the environment
	cfg := config.Load()
	handlers.SetClientCode(cfg.ClientCode)

	log.Printf("GOAL_MANAGEMENT enabled: %v", cfg.GoalManagement.Enabled)
	validation.InitValidatorRegistry()
	db, err := database.Connect(&cfg.Database)
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}
	defer database.Close(db)

	if err := database.Migrate(db, cfg); err != nil {
		log.Fatalf("Failed to run migrations: %v", err)
	}

	// Seed default data
	if err := database.Seed(db, cfg); err != nil {
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

	natsClient := natsclient.Connect(cfg.NATS.URL)
	defer natsClient.Close()

	jwtManager := utils.NewJWTManager(cfg.JWT.Secret, cfg.JWT.ExpireHour, cfg.JWT.RefreshExpireHour, cfg.JWT.RememberExpireHour)
	ssoJWTManager := utils.NewSSOJWTManager(cfg.SSOPrivateKey, cfg.SSOIssuerURL)
	sessionStore := database.NewSessionStore(redisClient)

	// Initialize repositories
	userRepo := repository.NewUserRepository(db)
	categoryRepo := repository.NewCategoryRepository(db)
	classificationRepo := repository.NewClassificationRepository(db)
	locationRepo := repository.NewLocationRepository(db)
	departmentRepo := repository.NewDepartmentRepository(db)
	roleRepo := repository.NewRoleRepository(db)
	permissionRepo := repository.NewPermissionRepository(db)
	actionLogRepo := repository.NewActionLogRepository(db)
	workflowRepo := repository.NewWorkflowRepository(db)
	incidentRepo := repository.NewIncidentRepository(db)
	incidentMergeRepo := repository.NewIncidentMergeRepository(db)
	reportRepo := repository.NewReportRepository(db)
	reportTemplateRepo := repository.NewReportTemplateRepository(db)
	rejectionLogRepo := repository.NewRejectionLogRepository(db)
	nasaqVerificationRepo := repository.NewNasaqVerificationRepository(db)
	lookupRepo := repository.NewLookupRepository(db)
	callLogRepo := repository.NewCallLogRepository(db)
	applicationLinkRepo := repository.NewApplicationLinkRepository(db)
	settingsRepo := repository.NewSettingsRepository(db)
	escalationRepo := repository.NewEscalationRepository(db)
	escalationGroupRepo := repository.NewEscalationGroupRepository(db)
	escalationPolicyRepo := repository.NewEscalationPolicyRepository(db)
	callerSentimentRepo := repository.NewCallerSentimentRepo(db)
	goalRepo := repository.NewGoalRepository(db)
	aiQualityFeedbackRepo := repository.NewAIQualityFeedbackRepository(db)
	feedbackTemplateRepo := repository.NewFeedbackTemplateRepository(db)
	publicFeedbackRepo := repository.NewIncidentPublicFeedbackRepository(db)
	ivrSmsLinkRepo := repository.NewIvrSmsLinkRepository(db)
	commentTemplateRepo := repository.NewCommentTemplateRepository(db)

	// Initialize WebSocket hub and start it
	wsHub := services.NewWSHub()
	go wsHub.Run()
	log.Println("WebSocket hub started")
	notificationTemplateRepo := repository.NewNotificationTemplateRepository(db)
	notificationLogRepo := repository.NewNotificationLogRepository(db)

	// Initialize services
	actionLogService := services.NewActionLogService(actionLogRepo)
	notificationService := services.NewNotificationService(notificationTemplateRepo, notificationLogRepo, userRepo, minioStorage, cfg)
	userService := services.NewUserService(userRepo, departmentRepo, jwtManager, sessionStore, minioStorage, cfg, actionLogService, nil, redisClient, db)
	extensionAssignmentRepo := repository.NewExtensionAssignmentRepository(db)
	extensionService := services.NewExtensionService(db, extensionAssignmentRepo, userRepo, roleRepo, actionLogService, notificationService, wsHub, sessionStore, cfg)
	otpService := services.NewOTPService(redisClient, notificationService, notificationLogRepo, userRepo, userService, sessionStore, jwtManager, roleRepo)
	//otpService := services.NewOTPService(redisClient, notificationService, notificationLogRepo, userRepo, userService, roleRepo)

	// Initialize LDAP service
	ldapService, err := services.NewLDAPService(cfg)
	if err != nil {
		log.Fatalf("Failed to initialize LDAP service: %v", err)
	}
	defer ldapService.Close()

	callLogService := services.NewCallLogService(callLogRepo, userRepo, minioStorage)
	workflowService := services.NewWorkflowService(workflowRepo, roleRepo, departmentRepo, classificationRepo, userRepo, db, cfg)
	incidentService := services.NewIncidentService(incidentRepo, incidentMergeRepo, workflowRepo, workflowService, userRepo, departmentRepo, classificationRepo, locationRepo, rejectionLogRepo, roleRepo, minioStorage, db, wsHub)
	incidentMergeService := services.NewIncidentMergeService(incidentMergeRepo, incidentRepo, workflowRepo, roleRepo, locationRepo, classificationRepo, db, wsHub)
	reportService := services.NewReportService(reportRepo, rejectionLogRepo, locationRepo, classificationRepo, workflowRepo)
	reportTemplateService := services.NewReportTemplateService(reportTemplateRepo, reportRepo)
	applicationLinkService := services.NewApplicationLinkService(applicationLinkRepo)
	settingsService := services.NewSettingsService(settingsRepo)
	presenceService := services.NewPresenceService(redisClient)
	escalationPolicyService := services.NewEscalationPolicyService(escalationPolicyRepo, userRepo)
	escalationService := services.NewEscalationService(escalationRepo, escalationPolicyRepo, incidentRepo, workflowRepo, userRepo, notificationService)
	escalationService.SetPolicyService(escalationPolicyService)
	escalationService.SetFrontendURL(cfg.FrontendURL)
	escalationGroupService := services.NewEscalationGroupService(escalationGroupRepo, incidentRepo, userRepo, reportRepo, notificationService, cfg.Escalation)
	escalationGroupService.SetFrontendURL(cfg.FrontendURL)
	escalationGroupService.SetPolicyService(escalationPolicyService)
	fcmService := services.NewFCMService(repository.NewDeviceTokenRepository(db), notificationLogRepo)
	callerSentimentService := services.NewCallerSentimentService(callerSentimentRepo)
	feedbackTemplateService := services.NewFeedbackTemplateService(feedbackTemplateRepo)
	finalCloseWhatsAppFeedbackSessionService := services.NewFinalCloseWhatsAppFeedbackSessionService(cfg.FinalCloseWhatsAppFeedback.SessionBaseURL)
	publicFeedbackService := services.NewIncidentPublicFeedbackService(publicFeedbackRepo, incidentRepo, notificationService, incidentService, workflowRepo, classificationRepo, finalCloseWhatsAppFeedbackSessionService)
	commentTemplateService := services.NewCommentTemplateService(commentTemplateRepo)

	// Goal management services
	var documentaClient storage.DocumentaClient
	if cfg.Documenta.Enabled {
		documentaClient = storage.NewHTTPDocumentaClient(cfg.Documenta, redisClient)
		log.Println("Documenta HTTP client enabled")
	} else {
		documentaClient = storage.NewStubDocumentaClient()
		log.Println("Documenta stub client (DOCUMENTA_ENABLED=false)")
	}
	goalService := services.NewGoalService(goalRepo, workflowRepo, userRepo, departmentRepo, documentaClient, notificationService, actionLogService, cfg, wsHub)
	documentService := services.NewDocumentService(documentaClient, cfg.Documenta)
	documentAuthzService := services.NewDocumentAuthzService(db, documentaClient, cfg.Documenta)

	// KPI evidence uploads use a SEPARATE Documenta client/config from Goal
	// Management's (own OAuth credentials, own workspace) — an explicit
	// product decision, not shared with documentaClient above.
	var kpiDocumentaClient storage.DocumentaClient
	if cfg.KpiDocumenta.Enabled {
		kpiDocumentaClient = storage.NewHTTPDocumentaClient(cfg.KpiDocumenta, redisClient)
		log.Println("KPI Documenta HTTP client enabled")
	} else {
		kpiDocumentaClient = storage.NewStubDocumentaClient()
		log.Println("KPI Documenta stub client (KPI_DOCUMENTA_ENABLED=false)")
	}

	// Ready-to-Close service
	readyToCloseRepo := repository.NewReadyToCloseRepository(db)
	readyToCloseService := services.NewReadyToCloseService(readyToCloseRepo, incidentRepo, workflowRepo, notificationService, cfg.ReadyToClose, db)

	// Wire ReadyToCloseService into IncidentService (post-construction to avoid circular deps)
	incidentService.SetReadyToCloseService(readyToCloseService)
	incidentService.SetNotificationService(notificationService)
	incidentService.SetUserService(userService)
	incidentService.SetFCMService(fcmService)
	incidentService.SetIvrSmsLinkRepo(ivrSmsLinkRepo)
	incidentService.SetConfig(cfg)
	incidentService.SetNATSClient(natsClient)
	smsFeedbackPendingRepo := repository.NewSmsFeedbackPendingRepository(db)
	incidentService.SetActionExecutor(services.NewActionExecutor(incidentRepo, userRepo, notificationService, smsFeedbackPendingRepo))
	incidentService.SetPublicFeedbackRepo(publicFeedbackRepo)
	incidentService.SetSmsFeedbackPendingRepo(smsFeedbackPendingRepo, cfg.SmsFeedback.DelayMinutes)

	// External Integration
	integrationRepo := repository.NewIntegrationRepository(db)
	integrationService := services.NewIntegrationService(integrationRepo, cfg.Integration.SecretsKey)
	integrationExecutor := services.NewIntegrationExecutor(integrationRepo, integrationService)
	incidentService.SetIntegrationExecutor(integrationExecutor)

	// Initialize and start SLA Monitor.
	// Interval is controlled by SLA_MONITOR_INTERVAL_MINUTES (default 5).
	slaIntervalMinutes := 5
	if v := os.Getenv("SLA_MONITOR_INTERVAL_MINUTES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			slaIntervalMinutes = n
		} else {
			log.Printf("Warning: invalid SLA_MONITOR_INTERVAL_MINUTES=%q, using default %d min", v, slaIntervalMinutes)
		}
	}
	log.Printf("SLA Monitor interval: %d minutes", slaIntervalMinutes)
	smsFeedbackService := services.NewSmsFeedbackService(smsFeedbackPendingRepo, incidentRepo, notificationService, finalCloseWhatsAppFeedbackSessionService)
	slaMonitor := services.NewSLAMonitor(incidentRepo, escalationService, escalationGroupService, readyToCloseService, smsFeedbackService, time.Duration(slaIntervalMinutes)*time.Minute)
	ctx := context.Background()
	slaMonitor.Start(ctx)
	defer slaMonitor.Stop()

	// Initialize and start AI Quality Monitor
	aiQualityMonitor := services.NewAIQualityMonitor(aiQualityFeedbackRepo, incidentRepo, minioStorage, cfg.AIQuality)
	aiQualityMonitor.Start(ctx)
	defer aiQualityMonitor.Stop()

	clientCode := strings.TrimSpace(cfg.ClientCode)
	if strings.EqualFold(clientCode, constants.CLIENT_CODE.EPM940) {
		// Initialize and start Auto-Assign Monitor
		autoAssignMonitor := services.NewAutoAssignMonitor(incidentService, cfg.AutoAssign)
		autoAssignMonitor.Start(ctx)
		defer autoAssignMonitor.Stop()
	}
	// Initialize validator
	validate := validator.New()

	// Initialize handlers
	userHandler := handlers.NewUserHandler(userService, minioStorage, redisClient, cfg, wsHub)
	healthHandler := handlers.NewHealthHandler()

	// Initialize LDAP handler
	ldapHandler := handlers.NewLDAPHandler(ldapService, userService, userRepo, jwtManager, sessionStore, cfg)

	categoryService := services.NewCategoryService(categoryRepo)
	categoryHandler := handlers.NewCategoryHandler(categoryService)
	classificationHandler := handlers.NewClassificationHandler(classificationRepo, cfg)
	locationHandler := handlers.NewLocationHandler(locationRepo, cfg)
	departmentHandler := handlers.NewDepartmentHandler(departmentRepo, userRepo, incidentRepo, cfg)
	extensionHandler := handlers.NewExtensionHandler(extensionService, cfg)
	roleHandler := handlers.NewRoleHandler(roleRepo, permissionRepo, userRepo, wsHub, cfg)
	actionLogHandler := handlers.NewActionLogHandler(actionLogService, validate)
	callLogHandler := handlers.NewCallLogHandler(callLogService, validate, userService, minioStorage)
	workflowHandler := handlers.NewWorkflowHandler(workflowService, actionLogService, cfg)
	incidentHandler := handlers.NewIncidentHandler(incidentService, userService, userRepo, incidentRepo, workflowRepo, locationRepo, classificationRepo, minioStorage, presenceService, cfg)
	incidentHandler.SetReadyToCloseService(readyToCloseService)
	incidentHandler.SetIvrSmsLinkRepo(ivrSmsLinkRepo)
	incidentHandler.SetPublicFeedbackRepo(publicFeedbackRepo)
	incidentHandler.SetLookupRepo(lookupRepo)
	incidentMergeHandler := handlers.NewIncidentMergeHandler(incidentMergeService, userRepo)
	websocketHandler := handlers.NewWebSocketHandler(wsHub)
	reportHandler := handlers.NewReportHandler(reportService, userRepo)
	reportTemplateHandler := handlers.NewReportTemplateHandler(reportTemplateService)
	lookupHandler := handlers.NewLookupHandler(lookupRepo)
	applicationLinkHandler := handlers.NewApplicationLinkHandler(applicationLinkService, minioStorage)
	settingsHandler := handlers.NewSettingsHandler(settingsService)
	jwksHandler := handlers.NewJWKSHandler(ssoJWTManager)
	ssoHandler := handlers.NewSSOHandler(ssoJWTManager, jwtManager, sessionStore, userRepo, applicationLinkRepo, userService, otpService, cfg.SSOFrontendURL, cfg.NafathAPIBaseURL)
	notificationTemplateService := services.NewNotificationTemplateService(notificationTemplateRepo, db)
	notificationHandler := handlers.NewNotificationHandler(notificationService, minioStorage, userRepo, incidentRepo, actionLogService)
	templateHandler := handlers.NewNotificationTemplateHandler(notificationTemplateService)
	attachmentHandler := handlers.NewAttachmentHandler(incidentService, notificationService, minioStorage)
	imageValidationHandler := handlers.NewImageValidationHandler(services.NewImageValidationService(), cfg.ImageValidation)
	otpHandler := handlers.NewOTPHandler(otpService, userService)
	escalationHandler := handlers.NewEscalationHandler(escalationService)
	escalationGroupHandler := handlers.NewEscalationGroupHandler(escalationGroupService)
	escalationPolicyHandler := handlers.NewEscalationPolicyHandler(escalationPolicyService, userRepo, departmentRepo)
	feedbackTemplateHandler := handlers.NewFeedbackTemplateHandler(feedbackTemplateService)
	commentTemplateHandler := handlers.NewCommentTemplateHandler(commentTemplateService)
	rejectionLogHandler := handlers.NewRejectionLogHandler(rejectionLogRepo)
	nasaqClient := services.NewNasaqClient(cfg.Nasaq)
	nasaqVerificationHandler := handlers.NewNasaqVerificationHandler(incidentRepo, nasaqVerificationRepo, nasaqClient)
	incidentFeedbackHandler := handlers.NewIncidentFeedbackHandler(incidentRepo, publicFeedbackRepo)
	publicFeedbackHandler := handlers.NewIncidentPublicFeedbackHandler(publicFeedbackService, actionLogService)
	aiQualityFeedbackHandler := handlers.NewAIQualityFeedbackHandler(aiQualityFeedbackRepo)
	fcmHandler := handlers.NewFCMHandler(fcmService)
	ctiHandler := handlers.NewCTIHandler(cfg.Cintrix.URL, cfg.Cintrix.APIKeyID, cfg.Cintrix.APIKeySecret, callLogRepo, userRepo, extensionAssignmentRepo, db)
	integrationUserHandler := handlers.NewIntegrationUserHandler(userService, roleRepo, departmentRepo, locationRepo, classificationRepo)
	sentimentHandler := handlers.NewCallerSentimentHandler(callerSentimentService)
	goalHandler := handlers.NewGoalHandler(goalService, actionLogService)
	goalAnalyticsService := services.NewGoalAnalyticsService(db)
	goalAnalyticsHandler := handlers.NewGoalAnalyticsHandler(goalAnalyticsService)
	reviewRepo := repository.NewReviewRepository(db)
	reviewService := services.NewReviewService(reviewRepo, goalRepo)
	reviewHandler := handlers.NewReviewHandler(reviewService)
	documentHandler := handlers.NewDocumentHandler(documentService, documentAuthzService, cfg.Documenta.WorkspaceName)

	// Goal Templates
	goalTemplateRepo := repository.NewGoalTemplateRepository(db)
	goalTemplateService := services.NewGoalTemplateService(goalTemplateRepo)
	goalTemplateHandler := handlers.NewGoalTemplateHandler(goalTemplateService)

	// KPI Master Data handler
	kpiMasterDataHandler := handlers.NewKpiMasterDataHandler(db)

	// KPI Workflow service
	kpiWorkflowService := services.NewKpiWorkflowService(db, workflowRepo, cfg.KpiDictionaryWorkflowCode)

	// KPI Dictionary handler
	kpiDictionaryHandler := handlers.NewKpiDictionaryHandler(db, actionLogService, kpiWorkflowService)

	// KPI Performance handler
	kpiPerformanceHandler := handlers.NewKpiPerformanceHandler(db, kpiWorkflowService, actionLogService)

	// KPI Engagement handler (metrics, evidence, collaborators, check-ins, comments, activity)
	kpiEngagementHandler := handlers.NewKpiEngagementHandler(db, actionLogService, minioStorage, kpiDocumentaClient, cfg.KpiDocumenta)

	// KPI Evidence Folder Configuration handler (resolve/browse/create Documenta folders by taxonomy)
	kpiDocumentaHandler := handlers.NewKpiDocumentaHandler(kpiDocumentaClient, cfg.KpiDocumenta)

	// KPI Dashboard handler
	kpiDashboardHandler := handlers.NewKpiDashboardHandler(db)

	// KPI Performance Band handler
	kpiPerformanceBandHandler := handlers.NewKpiPerformanceBandHandler(db)

	// KPI Corrective Action handler
	kpiCorrectiveActionHandler := handlers.NewKpiCorrectiveActionHandler(db)

	// KPI Entry handler
	kpiEntryHandler := handlers.NewKpiEntryHandler(db, actionLogService, kpiWorkflowService)

	// KPI Collaborator Assignment handler
	kpiCollaboratorHandler := handlers.NewKpiCollaboratorHandler(db, actionLogService)

	// KPI Composed views handler (Card / per-KPI Dashboard / annual rollup)
	kpiComposedHandler := handlers.NewKpiComposedHandler(db)

	// Integration handler
	integrationHandler := handlers.NewIntegrationHandler(integrationService, integrationExecutor, integrationRepo, incidentRepo)
	webhookHandler := handlers.NewWebhookHandler(integrationService, incidentService)
	cintrixWebhookHandler := handlers.NewCintrixWebhookHandler(cfg.Cintrix.WebhookSecret, callLogRepo, userRepo)

	// MOMRA CRM outbound integration (Automax -> MOMRA: classification/EE master sync,
	// status sync). Reverse direction from the existing inbound EPM routes below.
	// Leaving MOMRA_BASE_URL unset still wires the routes but every call will fail
	// against an empty base URL — acceptable for now since this is behind admin-only,
	// manually-triggered endpoints, not something that runs unprompted.
	momraClient := services.NewMOMRAClient(cfg.MOMRA)
	momraSyncService := services.NewMOMRASyncService(momraClient, classificationRepo, departmentRepo, cfg.MOMRA)
	momraSyncHandler := handlers.NewMOMRASyncHandler(momraSyncService)

	// MOMRA status sync (Story A/B/C: WorkflowState -> CaseStatusID mapping, outbound
	// call on status change reusing the integration execution-log infra, retry/backoff).
	momraStatusMappingRepo := repository.NewMOMRAStatusMappingRepository(db)
	momraStatusSyncService, err := services.NewMOMRAStatusSyncService(ctx, momraClient, momraStatusMappingRepo, integrationRepo)
	if err != nil {
		log.Fatalf("failed to initialize MOMRA status sync service: %v", err)
	}
	incidentService.SetMOMRAStatusSyncService(momraStatusSyncService)
	momraStatusMappingHandler := handlers.NewMOMRAStatusMappingHandler(momraStatusMappingRepo, momraStatusSyncService, integrationRepo)

	// License management
	licenseRepo := repository.NewLicenseRepository(db)
	licenseService := services.NewLicenseService(licenseRepo, cfg.License)
	if err := licenseService.LoadFromDB(ctx); err != nil {
		log.Printf("Warning: Failed to load license from DB: %v", err)
	}
	// Dev-mode auto-seeder: installs a full-scope license when APP_ENV=development
	// and no license is active. No-op in production.
	if err := devseed.SeedDevLicenseIfNeeded(ctx, cfg, licenseService); err != nil {
		log.Printf("Warning: dev license seeder failed: %v", err)
	}
	licenseHandler := handlers.NewLicenseHandler(licenseService)

	// Wire license service into user service for user limit enforcement
	userService.SetLicenseService(licenseService, licenseRepo)

	// Wire settings repo into user service to gate login token issuance on totp_enabled
	userService.SetSettingsRepo(settingsRepo)

	// Initialize GIS handler
	gisService := services.NewGISService()
	gisHandler := handlers.NewGISHandler(gisService)

	// Initialize EPM handler
	epmHandler := handlers.NewEPMHandler(userRepo, jwtManager, sessionStore)
	epmIncidentHandler := handlers.NewEPMIncidentHandler(userRepo, locationRepo, classificationRepo, incidentRepo, workflowRepo, lookupRepo, departmentRepo, momraStatusMappingRepo, jwtManager, sessionStore, minioStorage, db, wsHub)

	// Initialize middleware
	authMiddleware := middleware.NewAuthMiddleware(jwtManager, sessionStore, userRepo)
	licenseMiddleware := middleware.NewLicenseMiddleware(licenseService)
	loginRateLimitMiddleware := middleware.LoginRateLimitMiddleware(cfg, redisClient, userService)

	app := fiber.New(fiber.Config{
		AppName:        "Automax Backend",
		ErrorHandler:   customErrorHandler,
		BodyLimit:      100 * 1024 * 1024, // 100MB max body size
		ReadBufferSize: 64 * 1024,
	})

	app.Use(recover.New())
	app.Use(logger.New(logger.Config{
		Format: "[${time}] ${status} - ${latency} ${method} ${path}\n",
	}))
	// When AllowOrigins is "*", credentials cannot be sent (browser spec).
	// In that case we allow all origins without the credentials flag.
	corsAllowCredentials := cfg.Server.AllowOrigins != "*"
	app.Use(cors.New(cors.Config{
		AllowOrigins:     cfg.Server.AllowOrigins,
		AllowMethods:     "GET,POST,PUT,DELETE,PATCH,OPTIONS",
		AllowHeaders:     "Origin,Content-Type,Accept,Authorization",
		AllowCredentials: corsAllowCredentials,
	}))

	app.Use(middleware.RequestContext())
	app.Use(middleware.ValidationMiddleware())

	// Public SSO routes (no auth required — browser redirect landing points)
	app.Get("/.well-known/jwks.json", jwksHandler.GetJWKS)
	app.Get("/sso/callback", ssoHandler.Callback)

	// EPM external API routes
	app.Post("/Momra/API/EPM/Login", epmHandler.Login)
	app.Post("/Momra/API/EPM/InsertIncidents", epmIncidentHandler.InsertIncidents)
	app.Get("/Momra/API/EPM/GetMomraIncidentStatusDetails", epmIncidentHandler.GetMomraIncidentStatusDetails)
	app.Post("/Momra/API/EPM/UpdateIncident", epmIncidentHandler.UpdateIncident)
	app.Post("/Momra/API/EPM/ReopenIncident", epmIncidentHandler.ReopenIncident)

	// EPM v2 routes - newer MOMRA contract (PATCH, meta/json request envelope, response
	// envelope). Left alongside the v1 routes above.
	app.Patch("/Momra/API/EPM/UpdateIncidentV2", epmIncidentHandler.UpdateIncidentV2)
	app.Patch("/Momra/API/EPM/ReOpenIncidentV2", epmIncidentHandler.ReopenIncidentV2)
	// STUB: MOMRA->Automax external-entity assignment notification. Mechanism/contract
	// not yet confirmed by MOMRA (OD-N2) — see AssignExternalEntity's doc comment. Kept
	// alongside UpdateIncidentV2, which now also persists EE fields on every call.
	app.Patch("/Momra/API/EPM/AssignExternalEntity", epmIncidentHandler.AssignExternalEntity)

	api := app.Group("/api")
	v1 := api.Group("/v1")

	// Health routes
	v1.Get("/health", healthHandler.Health)
	v1.Get("/ready", healthHandler.Ready)

	// WebSocket route for real-time updates
	// Query params: incident_id, user_id, token (for auth)
	v1.Get("/ws", websocketHandler.WebSocketUpgrader())

	// Broadcast WebSocket route for incident list viewer count updates
	// Query params: token (for auth)
	v1.Get("/ws/broadcast", websocketHandler.BroadcastWebSocketUpgrader())

	if cfg.GoalManagement.Enabled {
		v1.Get("/ws/goal", websocketHandler.GoalWebSocketUpgrader())
		v1.Get("/ws/goal/broadcast", websocketHandler.GoalBroadcastWebSocketUpgrader())
	}

	// WebSocket connection statistics endpoint
	v1.Get("/ws/stats", authMiddleware.RequirePermission("incidents:view"), websocketHandler.GetConnectionStats)

	// Webhook routes (no authentication required - external services)
	webhooks := v1.Group("/webhooks")
	webhooks.Post("/sendgrid/inbound", notificationHandler.SendGridInboundWebhook)
	webhooks.Post("/automax-callback", webhookHandler.HandleAutomaxCallback)
	webhooks.Post("/cintrix/call-event", cintrixWebhookHandler.HandleCallEvent)

	ivr := v1.Group("/ivr/incident")
	// Public: validates signed URL + last 6 digits, returns incident + session token
	ivr.Get("/sms-link/:id", incidentHandler.FindByIDWithLast6DigitValidation)
	// Public: validates signed URL from normal closure SMS, returns incident info for feedback page
	ivr.Get("/feedback/:id", incidentHandler.FindByIDForFeedback)
	// Protected: require valid IVR session token issued by the GET route above
	// ivr.Put("/sms-link/update/:id", authMiddleware.ValidateIvrSmsToken(), incidentHandler.UpdateIncidentViaIvrSms)
	// ivr.Post("/sms-link/attachment/:id", authMiddleware.ValidateIvrSmsToken(), incidentHandler.UploadAttachmentIvrSms)

	// Auth routes
	auth := v1.Group("/auth")
	auth.Post("/register", userHandler.Register)
	auth.Post("/sso/register", ssoHandler.SSORegister)
	auth.Post("/login", loginRateLimitMiddleware, userHandler.Login)
	auth.Post("/sso/login", ssoHandler.SSOLogin)
	auth.Get("/sso/validate", ssoHandler.SSOValidate)
	auth.Post("/refresh", userHandler.RefreshToken)
	auth.Post("/logout", authMiddleware.Authenticate(), userHandler.Logout)
	auth.Post("/unblock-user", authMiddleware.Authenticate(), authMiddleware.RequirePermission("users:unblock"), userHandler.UnblockUser)
	auth.Post("/forgot-password", userHandler.ForgotPassword)
	auth.Post("/verify-reset-otp", userHandler.VerifyOTPForReset)
	auth.Post("/reset-password", userHandler.ResetPassword)

	// LDAP routes (public - for LDAP authentication)
	ldap := v1.Group("/ldap")
	ldap.Post("/login", ldapHandler.Login)
	ldap.Post("/test", authMiddleware.Authenticate(), authMiddleware.RequirePermission("admin:ldap"), ldapHandler.TestConnection)
	ldap.Post("/search", authMiddleware.Authenticate(), authMiddleware.RequirePermission("users:view"), ldapHandler.SearchUser)
	ldap.Post("/sync", authMiddleware.Authenticate(), authMiddleware.RequirePermission("users:update"), ldapHandler.SyncUser)
	ldap.Post("/users", authMiddleware.Authenticate(), authMiddleware.RequirePermission("users:view"), ldapHandler.ListADUsers)
	ldap.Post("/register", authMiddleware.Authenticate(), authMiddleware.RequirePermission("users:create"), ldapHandler.RegisterADUser)
	ldap.Get("/status", authMiddleware.Authenticate(), authMiddleware.RequirePermission("admin:ldap"), ldapHandler.GetLDAPStatus)

	// SSO routes
	sso := v1.Group("/sso", authMiddleware.Authenticate(), licenseMiddleware.RequireLicensedFeature(string(licensing.FeatureSSO)))
	sso.Post("/launch", ssoHandler.Launch)

	// User routes
	users := v1.Group("/users")
	users.Get("/me", authMiddleware.Authenticate(), userHandler.GetProfile)
	users.Put("/me", authMiddleware.Authenticate(), userHandler.UpdateProfile)
	users.Post("/me/avatar", authMiddleware.Authenticate(), userHandler.UploadAvatar)
	users.Put("/me/password", authMiddleware.Authenticate(), userHandler.ChangePassword)
	users.Delete("/me", authMiddleware.Authenticate(), userHandler.DeleteAccount)
	users.Put("/:userExtID/status", userHandler.UpdateUserCallStatus)
	users.Put("/:userID/password", authMiddleware.Authenticate(), authMiddleware.RequirePermission("users:reset_password"), userHandler.AdminResetPassword)

	// Incident routes (authenticated users)
	incidents := v1.Group("/incidents", authMiddleware.Authenticate(), licenseMiddleware.RequireLicensedFeature(string(licensing.FeatureIncidents)))
	incidents.Post("/", authMiddleware.RequirePermission("incidents:create"), incidentHandler.CreateIncident)
	incidents.Get("/", authMiddleware.RequirePermission("incidents:view"), incidentHandler.ListIncidents)
	incidents.Post("/search", authMiddleware.RequirePermission("incidents:view"), incidentHandler.SearchIncidents)
	incidents.Post("/search/markers", authMiddleware.RequirePermission("incidents:view"), incidentHandler.SearchIncidentMarkers)
	incidents.Get("/stats", authMiddleware.RequirePermission("incidents:view"), incidentHandler.GetStats)
	incidents.Get("/stats/v2", authMiddleware.RequirePermission("incidents:view"), incidentHandler.GetStatsV2)
	incidents.Get("/priority-counts", authMiddleware.RequirePermission("incidents:view"), incidentHandler.GetPriorityCounts)
	incidents.Get("/my-assigned", authMiddleware.RequirePermission("incidents:view"), incidentHandler.GetMyAssigned)
	incidents.Get("/my-reported", authMiddleware.RequirePermission("incidents:view"), incidentHandler.GetMyReported)
	incidents.Get("/sla-breached", authMiddleware.RequirePermission("incidents:view"), incidentHandler.GetSLABreached)
	incidents.Get("/ready-to-close/duration-options", authMiddleware.RequirePermission("incidents:view"), incidentHandler.GetReadyToCloseDurationOptions)
	incidents.Post("/bulk/convert-to-request", authMiddleware.RequirePermission("incidents:update"), incidentHandler.BulkConvertToRequest)
	incidents.Get("/:id", authMiddleware.RequirePermission("incidents:view"), incidentHandler.GetIncident)
	incidents.Get("/:id/report", authMiddleware.RequirePermission("reports:view"), incidentHandler.GenerateReport)
	incidents.Put("/:id", authMiddleware.RequirePermission("incidents:update"), incidentHandler.UpdateIncident)
	incidents.Delete("/:id", authMiddleware.RequirePermission("incidents:delete", "requests:delete"), incidentHandler.DeleteIncident)
	incidents.Post("/:id/transition", authMiddleware.RequirePermission("incidents:transition"), incidentHandler.ExecuteTransition)
	incidents.Put("/:id/state", authMiddleware.RequirePermission("incidents:transition"), incidentHandler.ForceState)
	incidents.Post("/:id/convert-to-request", authMiddleware.RequirePermission("incidents:update"), incidentHandler.ConvertToRequest)
	incidents.Get("/:id/can-convert", authMiddleware.RequirePermission("incidents:view"), incidentHandler.CanConvertToRequest)
	incidents.Get("/:id/available-transitions", authMiddleware.RequirePermission("incidents:view"), incidentHandler.GetAvailableTransitions)
	incidents.Get("/:id/history", authMiddleware.RequirePermission("incidents:view"), incidentHandler.GetTransitionHistory)
	incidents.Post("/:id/comments", authMiddleware.RequirePermission("incidents:comment"), incidentHandler.AddComment)
	incidents.Get("/:id/comments", authMiddleware.RequirePermission("incidents:view"), incidentHandler.ListComments)
	incidents.Get("/:id/feedbacks", authMiddleware.RequirePermission("incidents:view"), incidentHandler.ListFeedbacks)
	incidents.Put("/:id/comments/:comment_id", authMiddleware.RequirePermission("incidents:comment"), incidentHandler.UpdateComment)
	incidents.Delete("/:id/comments/:comment_id", authMiddleware.RequirePermission("incidents:comment"), incidentHandler.DeleteComment)
	incidents.Post("/:id/attachments", authMiddleware.RequirePermission("incidents:update"), incidentHandler.UploadAttachment)
	incidents.Get("/:id/attachments", authMiddleware.RequirePermission("incidents:view"), incidentHandler.ListAttachments)
	incidents.Delete("/:id/attachments/:attachment_id", authMiddleware.RequirePermission("incidents:update"), incidentHandler.DeleteAttachment)
	incidents.Put("/:id/assign", authMiddleware.RequirePermission("incidents:assign"), incidentHandler.AssignIncident)
	incidents.Get("/:id/revisions", authMiddleware.RequirePermission("incidents:view"), incidentHandler.ListRevisions)
	incidents.Get("/:id/rejection-logs", authMiddleware.RequirePermission("incidents:view"), rejectionLogHandler.GetByIncident)
	incidents.Get("/:id/ai-quality", authMiddleware.RequirePermission("incidents:view"), aiQualityFeedbackHandler.GetByIncident)
	incidents.Post("/:id/request-info", authMiddleware.RequirePermission("incidents:request-info"), incidentHandler.RequestCitizenInfo)
	incidents.Post("/:id/nasaq/verify", authMiddleware.RequirePermission("incidents:verify-nasaq"), nasaqVerificationHandler.Verify)
	incidents.Get("/:id/nasaq/verify", authMiddleware.RequirePermission("incidents:view"), nasaqVerificationHandler.GetLatest)

	aiQuality := v1.Group("/ai-quality", authMiddleware.Authenticate(), licenseMiddleware.RequireLicensedFeature(string(licensing.FeatureAIQuality)))
	aiQuality.Get("/", authMiddleware.RequirePermission("incidents:view"), aiQualityFeedbackHandler.GetAll)

	feedback := v1.Group("/feedback", authMiddleware.Authenticate())
	feedback.Get("/", authMiddleware.RequirePermission("incidents:view"), incidentFeedbackHandler.ListAllFeedback)
	feedback.Post("/:id", authMiddleware.RequirePermission("incidents:update"), incidentFeedbackHandler.CreateFeedback)
	feedback.Get("/:id", authMiddleware.RequirePermission("incidents:view"), incidentFeedbackHandler.ListFeedback)

	publicFeedback := v1.Group("/public-feedback")
	publicFeedback.Post("/:incidentID", authMiddleware.Authenticate(), authMiddleware.RequirePermission("incidents:update"), publicFeedbackHandler.Create)
	publicFeedback.Get("/", authMiddleware.Authenticate(), authMiddleware.RequirePermission("incidents:view"), publicFeedbackHandler.ListAll)
	publicFeedback.Get("/:incidentID", authMiddleware.Authenticate(), authMiddleware.RequirePermission("incidents:view"), publicFeedbackHandler.ListByIncident)
	publicFeedback.Get("/:incidentID/init", publicFeedbackHandler.Init)
	publicFeedback.Put("/:incidentID/submit", publicFeedbackHandler.Submit)

	// Closed incident editing (requires special permission)
	incidents.Patch("/:id/closed-summary", authMiddleware.RequirePermission("incidents:edit-closed"), incidentHandler.UpdateClosedIncidentSummary)

	// Presence tracking routes
	incidents.Post("/:id/presence", authMiddleware.RequirePermission("incidents:view"), incidentHandler.MarkPresence)
	incidents.Get("/:id/presence", authMiddleware.RequirePermission("incidents:view"), incidentHandler.GetPresence)
	incidents.Delete("/:id/presence", authMiddleware.RequirePermission("incidents:view"), incidentHandler.RemovePresence)

	// Incident Merge routes
	incidents.Post("/merge/validate", authMiddleware.RequirePermission("incidents:merge"), incidentMergeHandler.ValidateMerge)
	incidents.Post("/merge", authMiddleware.RequirePermission("incidents:merge"), incidentMergeHandler.MergeIncidents)
	incidents.Post("/merge/unmerge", authMiddleware.RequirePermission("incidents:merge"), incidentMergeHandler.UnmergeIncident)
	incidents.Post("/merge/bulk-unmerge", authMiddleware.RequirePermission("incidents:merge"), incidentMergeHandler.BulkUnmergeIncidents)
	incidents.Get("/merge/:id/merged", authMiddleware.RequirePermission("incidents:view"), incidentMergeHandler.GetMergedIncidents)
	incidents.Get("/merge/can-merge", authMiddleware.RequirePermission("incidents:view"), incidentMergeHandler.CanMerge)

	// Attachment download route (unified for incidents and notifications)
	attachments := v1.Group("/attachments", authMiddleware.Authenticate())
	attachments.Get("/:attachment_id", attachmentHandler.DownloadAttachment)
	attachments.Get("/:attachment_id/preview", attachmentHandler.PreviewAttachment)

	// Standalone image-quality validation, called by Mobile App / Chatbot
	// before incident submission to reject mostly-black/mostly-white/no-detail photos.
	images := v1.Group("/images", authMiddleware.Authenticate())
	images.Post("/validate", imageValidationHandler.ValidateImage)

	// Complaint routes (authenticated users)
	complaints := v1.Group("/complaints", authMiddleware.Authenticate(), licenseMiddleware.RequireLicensedFeature(string(licensing.FeatureComplaints)))
	complaints.Post("/", authMiddleware.RequirePermission("complaints:create"), incidentHandler.CreateComplaint)
	complaints.Get("/", authMiddleware.RequirePermission("complaints:view"), incidentHandler.ListComplaints)
	complaints.Get("/:id", authMiddleware.RequirePermission("complaints:view"), incidentHandler.GetComplaint)
	complaints.Get("/:id/report", authMiddleware.RequirePermission("reports:view"), incidentHandler.GenerateReport)
	complaints.Put("/:id", authMiddleware.RequirePermission("complaints:update"), incidentHandler.UpdateIncident)
	complaints.Post("/:id/transition", authMiddleware.RequirePermission("complaints:transition"), incidentHandler.ExecuteTransition)
	complaints.Get("/:id/available-transitions", authMiddleware.RequirePermission("complaints:view"), incidentHandler.GetAvailableTransitions)
	complaints.Get("/:id/history", authMiddleware.RequirePermission("complaints:view"), incidentHandler.GetTransitionHistory)
	complaints.Post("/:id/comments", authMiddleware.RequirePermission("complaints:comment"), incidentHandler.AddComment)
	complaints.Get("/:id/comments", authMiddleware.RequirePermission("complaints:view"), incidentHandler.ListComments)
	complaints.Get("/:id/feedbacks", authMiddleware.RequirePermission("complaints:view"), incidentHandler.ListFeedbacks)
	complaints.Put("/:id/comments/:comment_id", authMiddleware.RequirePermission("complaints:comment"), incidentHandler.UpdateComment)
	complaints.Delete("/:id/comments/:comment_id", authMiddleware.RequirePermission("complaints:comment"), incidentHandler.DeleteComment)
	complaints.Post("/:id/attachments", authMiddleware.RequirePermission("complaints:update"), incidentHandler.UploadAttachment)
	complaints.Get("/:id/attachments", authMiddleware.RequirePermission("complaints:view"), incidentHandler.ListAttachments)
	complaints.Delete("/:id/attachments/:attachment_id", authMiddleware.RequirePermission("complaints:update"), incidentHandler.DeleteAttachment)
	complaints.Post("/:id/evaluate", authMiddleware.RequirePermission("complaints:update"), incidentHandler.IncrementEvaluation)
	complaints.Get("/:id/revisions", authMiddleware.RequirePermission("complaints:view"), incidentHandler.ListRevisions)

	// Query routes (authenticated users)
	queries := v1.Group("/queries", authMiddleware.Authenticate(), licenseMiddleware.RequireLicensedFeature(string(licensing.FeatureQueries)))
	queries.Post("/", authMiddleware.RequirePermission("queries:create"), incidentHandler.CreateQuery)
	queries.Get("/", authMiddleware.RequirePermission("queries:view"), incidentHandler.ListQueries)
	queries.Get("/:id", authMiddleware.RequirePermission("queries:view"), incidentHandler.GetQuery)
	queries.Get("/:id/report", authMiddleware.RequirePermission("reports:view"), incidentHandler.GenerateReport)
	queries.Put("/:id", authMiddleware.RequirePermission("queries:update"), incidentHandler.UpdateIncident)
	queries.Post("/:id/transition", authMiddleware.RequirePermission("queries:transition"), incidentHandler.ExecuteTransition)
	queries.Get("/:id/available-transitions", authMiddleware.RequirePermission("queries:view"), incidentHandler.GetAvailableTransitions)
	queries.Get("/:id/history", authMiddleware.RequirePermission("queries:view"), incidentHandler.GetTransitionHistory)
	queries.Post("/:id/comments", authMiddleware.RequirePermission("queries:comment"), incidentHandler.AddComment)
	queries.Get("/:id/comments", authMiddleware.RequirePermission("queries:view"), incidentHandler.ListComments)
	queries.Put("/:id/comments/:comment_id", authMiddleware.RequirePermission("queries:comment"), incidentHandler.UpdateComment)
	queries.Delete("/:id/comments/:comment_id", authMiddleware.RequirePermission("queries:comment"), incidentHandler.DeleteComment)
	queries.Post("/:id/attachments", authMiddleware.RequirePermission("queries:update"), incidentHandler.UploadAttachment)
	queries.Get("/:id/attachments", authMiddleware.RequirePermission("queries:view"), incidentHandler.ListAttachments)
	queries.Delete("/:id/attachments/:attachment_id", authMiddleware.RequirePermission("queries:update"), incidentHandler.DeleteAttachment)
	queries.Get("/:id/revisions", authMiddleware.RequirePermission("queries:view"), incidentHandler.ListRevisions)

	// Admin routes
	admin := v1.Group("/admin", authMiddleware.Authenticate(), licenseMiddleware.LicenseGate())

	// User management (action logging handled by service layer for detailed old/new values)
	usersGroup := admin.Group("/users")
	usersGroup.Get("/", authMiddleware.RequirePermission("users:view"), userHandler.ListUsers)
	usersGroup.Post("/", authMiddleware.RequirePermission("users:create"), userHandler.AdminCreateUser)
	usersGroup.Post("/match", authMiddleware.RequirePermission("users:view"), userHandler.MatchUsers)
	usersGroup.Get("/manager-scope", authMiddleware.RequirePermission("users:view"), userHandler.GetManagerScope)
	usersGroup.Get("/export", authMiddleware.RequirePermission("users:view"), userHandler.Export)
	usersGroup.Post("/import", authMiddleware.RequirePermission("users:create"), userHandler.Import)
	usersGroup.Get("/:id", authMiddleware.RequirePermission("users:view"), userHandler.GetUser)
	usersGroup.Put("/:id", authMiddleware.RequirePermission("users:update"), userHandler.AdminUpdateUser)
	usersGroup.Delete("/:id", authMiddleware.RequirePermission("users:delete"), userHandler.AdminDeleteUser)

	// Integration user provisioning (external teams create users with minimal payload)
	integrationUsers := v1.Group("/integration/users", authMiddleware.Authenticate(), authMiddleware.RequirePermission("users:create"))
	integrationUsers.Post("/", integrationUserHandler.CreateUser)

	// Category routes (foundational admin taxonomy used by goals — no license gate)
	categories := admin.Group("/categories", middleware.ActionLogger(middleware.ActionLoggerConfig{
		Enabled:     true,
		LogService:  actionLogService,
		SkipMethods: []string{"GET"},
	}))
	categories.Post("/", authMiddleware.RequirePermission("categories:create"), categoryHandler.Create)
	categories.Get("/", authMiddleware.RequirePermission("categories:view"), categoryHandler.List)
	categories.Get("/tree", authMiddleware.RequirePermission("categories:view"), categoryHandler.GetTree)
	categories.Get("/:id", authMiddleware.RequirePermission("categories:view"), categoryHandler.GetByID)
	categories.Put("/:id", authMiddleware.RequirePermission("categories:update"), categoryHandler.Update)
	categories.Delete("/:id", authMiddleware.RequirePermission("categories:delete"), categoryHandler.Delete)

	// Classification routes (with action logging)
	classifications := admin.Group("/classifications", middleware.ActionLogger(middleware.ActionLoggerConfig{
		Enabled:     true,
		LogService:  actionLogService,
		SkipMethods: []string{"GET"},
	}))
	classifications.Post("/", authMiddleware.RequirePermission("classifications:create"), classificationHandler.Create)
	classifications.Get("/", authMiddleware.RequirePermission("classifications:view"), classificationHandler.List)
	classifications.Get("/tree", authMiddleware.RequirePermission("classifications:view", "users:view_department_only"), classificationHandler.GetTree)
	classifications.Get("/tree/stats", authMiddleware.RequirePermission("classifications:view"), classificationHandler.GetTreeWithStats)
	classifications.Get("/children", authMiddleware.RequirePermission("classifications:view"), classificationHandler.GetChildren)
	classifications.Get("/export", authMiddleware.RequirePermission("classifications:view"), classificationHandler.Export)
	classifications.Post("/import", authMiddleware.RequirePermission("classifications:create"), classificationHandler.Import)
	classifications.Get("/:id", authMiddleware.RequirePermission("classifications:view"), classificationHandler.GetByID)
	classifications.Put("/:id", authMiddleware.RequirePermission("classifications:update"), classificationHandler.Update)
	classifications.Delete("/:id", authMiddleware.RequirePermission("classifications:delete"), classificationHandler.Delete)
	// Classification Criticality routes
	classifications.Get("/:classification_id/criticalities", authMiddleware.RequirePermission("classifications:view"), classificationHandler.GetCriticalities)
	classifications.Post("/:classification_id/criticalities", authMiddleware.RequirePermission("classifications:create"), classificationHandler.CreateCriticality)
	classifications.Get("/criticalities/:id", authMiddleware.RequirePermission("classifications:view"), classificationHandler.GetCriticalityByID)
	classifications.Put("/criticalities/:id", authMiddleware.RequirePermission("classifications:update"), classificationHandler.UpdateCriticality)
	classifications.Delete("/criticalities/:id", authMiddleware.RequirePermission("classifications:delete"), classificationHandler.DeleteCriticality)

	// Location routes (with action logging)
	locations := admin.Group("/locations", middleware.ActionLogger(middleware.ActionLoggerConfig{
		Enabled:     true,
		LogService:  actionLogService,
		SkipMethods: []string{"GET"},
	}))
	locations.Post("/", authMiddleware.RequirePermission("locations:create"), locationHandler.Create)
	locations.Get("/", authMiddleware.RequirePermission("locations:view"), locationHandler.List)
	locations.Get("/tree", authMiddleware.RequirePermission("locations:view", "users:view_department_only"), locationHandler.GetTree)
	locations.Get("/tree/stats", authMiddleware.RequirePermission("locations:view"), locationHandler.GetTreeWithStats)
	locations.Get("/children", authMiddleware.RequirePermission("locations:view"), locationHandler.GetChildren)
	locations.Get("/by-type", authMiddleware.RequirePermission("locations:view"), locationHandler.GetByType)
	locations.Get("/export", authMiddleware.RequirePermission("locations:view"), locationHandler.Export)
	locations.Post("/import", authMiddleware.RequirePermission("locations:create"), locationHandler.Import)
	locations.Get("/:id", authMiddleware.RequirePermission("locations:view"), locationHandler.GetByID)
	locations.Put("/:id", authMiddleware.RequirePermission("locations:update"), locationHandler.Update)
	locations.Delete("/:id", authMiddleware.RequirePermission("locations:delete"), locationHandler.Delete)

	// Department routes (with action logging)
	departments := admin.Group("/departments", middleware.ActionLogger(middleware.ActionLoggerConfig{
		Enabled:     true,
		LogService:  actionLogService,
		SkipMethods: []string{"GET"},
	}))
	departments.Post("/", authMiddleware.RequirePermission("departments:create"), departmentHandler.Create)
	departments.Get("/", authMiddleware.RequirePermission("departments:view"), departmentHandler.List)
	departments.Get("/tree", authMiddleware.RequirePermission("departments:view", "users:view_department_only"), departmentHandler.GetTree)
	departments.Get("/children", authMiddleware.RequirePermission("departments:view"), departmentHandler.GetChildren)
	departments.Post("/match", authMiddleware.RequirePermission("departments:view"), departmentHandler.MatchDepartment)
	departments.Get("/export", authMiddleware.RequirePermission("departments:view"), departmentHandler.Export)
	departments.Post("/import", authMiddleware.RequirePermission("departments:create"), departmentHandler.Import)
	departments.Get("/:id", authMiddleware.RequirePermission("departments:view"), departmentHandler.GetByID)
	departments.Put("/:id", authMiddleware.RequirePermission("departments:update"), departmentHandler.Update)
	departments.Delete("/:id", authMiddleware.RequirePermission("departments:delete"), departmentHandler.Delete)

	// Role routes (with action logging)
	roles := admin.Group("/roles", middleware.ActionLogger(middleware.ActionLoggerConfig{
		Enabled:     true,
		LogService:  actionLogService,
		SkipMethods: []string{"GET"},
	}))
	roles.Post("/", authMiddleware.RequirePermission("roles:create"), roleHandler.CreateRole)
	roles.Get("/", authMiddleware.RequirePermission("roles:view", "users:view_department_only"), roleHandler.ListRoles)
	roles.Get("/export", authMiddleware.RequirePermission("roles:view"), roleHandler.Export)
	roles.Post("/import", authMiddleware.RequirePermission("roles:create"), roleHandler.Import)
	roles.Get("/:id", authMiddleware.RequirePermission("roles:view"), roleHandler.GetRole)
	roles.Put("/:id", authMiddleware.RequirePermission("roles:update"), roleHandler.UpdateRole)
	roles.Delete("/:id", authMiddleware.RequirePermission("roles:delete"), roleHandler.DeleteRole)
	roles.Post("/:id/permissions", authMiddleware.RequirePermission("roles:update"), roleHandler.AssignPermissions)

	// Permission routes (with action logging)
	permissions := admin.Group("/permissions", middleware.ActionLogger(middleware.ActionLoggerConfig{
		Enabled:     true,
		LogService:  actionLogService,
		SkipMethods: []string{"GET"},
	}))
	permissions.Post("/", authMiddleware.RequirePermission("permissions:create"), roleHandler.CreatePermission)
	permissions.Get("/", authMiddleware.RequirePermission("permissions:view"), roleHandler.ListPermissions)
	permissions.Get("/modules", authMiddleware.RequirePermission("permissions:view"), roleHandler.GetModules)
	permissions.Get("/:id", authMiddleware.RequirePermission("permissions:view"), roleHandler.GetPermission)
	permissions.Put("/:id", authMiddleware.RequirePermission("permissions:update"), roleHandler.UpdatePermission)
	permissions.Delete("/:id", authMiddleware.RequirePermission("permissions:delete"), roleHandler.DeletePermission)

	// Action Log routes
	actionLogs := admin.Group("/action-logs")
	actionLogs.Get("/", authMiddleware.RequirePermission("action-logs:view"), actionLogHandler.ListActionLogs)
	actionLogs.Get("/stats", authMiddleware.RequirePermission("action-logs:view"), actionLogHandler.GetStats)
	actionLogs.Get("/filter-options", authMiddleware.RequirePermission("action-logs:view"), actionLogHandler.GetFilterOptions)
	actionLogs.Get("/user/:id", authMiddleware.RequirePermission("action-logs:view"), actionLogHandler.GetUserActions)
	actionLogs.Get("/:id", authMiddleware.RequirePermission("action-logs:view"), actionLogHandler.GetActionLog)
	actionLogs.Get("/export", authMiddleware.RequirePermission("action-logs:export"), actionLogHandler.ExportActionLogs)
	actionLogs.Delete("/cleanup", authMiddleware.RequirePermission("action-logs:delete"), actionLogHandler.CleanupOldLogs)

	// Call Log routes
	callLogs := admin.Group("/call-logs", licenseMiddleware.RequireLicensedFeature(string(licensing.FeatureCallCentre)))
	callLogs.Post("/", authMiddleware.RequirePermission("call-logs:create"), callLogHandler.CreateCallLog)
	callLogs.Get("/", authMiddleware.RequirePermission("call-logs:view"), callLogHandler.ListCallLogs)
	callLogs.Get("/stats", authMiddleware.RequirePermission("call-logs:view"), callLogHandler.GetStats)
	callLogs.Get("/:id", authMiddleware.RequirePermission("call-logs:view"), callLogHandler.GetCallLog)
	callLogs.Put("/:id", authMiddleware.RequirePermission("call-logs:update"), callLogHandler.UpdateCallLog)
	callLogs.Delete("/:id", authMiddleware.RequirePermission("call-logs:delete"), callLogHandler.DeleteCallLog)

	// Workflow routes (with action logging)
	workflows := admin.Group("/workflows", licenseMiddleware.RequireLicensedFeature(string(licensing.FeatureWorkflows)), middleware.ActionLogger(middleware.ActionLoggerConfig{
		Enabled:     true,
		LogService:  actionLogService,
		SkipMethods: []string{"GET"}, // Skip read-only operations from logging
	}))
	workflows.Post("/", authMiddleware.RequirePermission("workflows:create"), workflowHandler.CreateWorkflow)
	workflows.Get("/", authMiddleware.RequirePermission("workflows:view"), workflowHandler.ListWorkflows)
	workflows.Get("/deleted", authMiddleware.RequirePermission("workflows:view"), workflowHandler.ListDeletedWorkflows) // List soft-deleted workflows
	workflows.Post("/match", authMiddleware.RequirePermission("workflows:view"), workflowHandler.MatchWorkflow)         // For mobile apps - matches workflow based on criteria
	workflows.Get("/by-classification/:classification_id", authMiddleware.RequirePermission("workflows:view"), workflowHandler.GetWorkflowByClassification)
	workflows.Get("/:id", authMiddleware.RequirePermission("workflows:view"), workflowHandler.GetWorkflow)
	workflows.Put("/:id", authMiddleware.RequirePermission("workflows:update"), workflowHandler.UpdateWorkflow)
	workflows.Delete("/:id", authMiddleware.RequirePermission("workflows:delete"), workflowHandler.DeleteWorkflow)
	workflows.Delete("/:id/permanent", authMiddleware.RequirePermission("workflows:delete"), workflowHandler.PermanentDeleteWorkflow) // Hard delete
	workflows.Post("/:id/restore", authMiddleware.RequirePermission("workflows:update"), workflowHandler.RestoreWorkflow)             // Restore soft-deleted workflow
	workflows.Post("/:id/duplicate", authMiddleware.RequirePermission("workflows:create"), workflowHandler.DuplicateWorkflow)
	workflows.Post("/:id/classifications", authMiddleware.RequirePermission("workflows:update"), workflowHandler.AssignClassifications)
	workflows.Get("/:id/initial-state", authMiddleware.RequirePermission("workflows:view"), workflowHandler.GetInitialState)
	workflows.Get("/:id/initial-state/matching-users", authMiddleware.RequirePermission("workflows:view"), workflowHandler.GetInitialStateMatchingUsers)
	workflows.Get("/:id/export", authMiddleware.RequirePermission("workflows:view"), workflowHandler.ExportWorkflow)
	workflows.Post("/import", authMiddleware.RequirePermission("workflows:create"), workflowHandler.ImportWorkflow)

	// Workflow state routes (with action logging)
	workflows.Post("/:id/states", authMiddleware.RequirePermission("workflows:update"), workflowHandler.CreateState)
	workflows.Get("/:id/states", authMiddleware.RequirePermission("workflows:view"), workflowHandler.ListStates)
	workflows.Put("/:id/states/:state_id", authMiddleware.RequirePermission("workflows:update"), workflowHandler.UpdateState)
	workflows.Delete("/:id/states/:state_id", authMiddleware.RequirePermission("workflows:update"), workflowHandler.DeleteState)
	workflows.Get("/states/:state_id/transitions", authMiddleware.RequirePermission("workflows:view"), workflowHandler.GetTransitionsToState)

	// Workflow transition routes (with action logging)
	workflows.Post("/:id/transitions", authMiddleware.RequirePermission("workflows:update"), workflowHandler.CreateTransition)
	workflows.Get("/:id/transitions", authMiddleware.RequirePermission("workflows:view"), workflowHandler.ListTransitions)
	workflows.Put("/:id/transitions/:transition_id", authMiddleware.RequirePermission("workflows:update"), workflowHandler.UpdateTransition)
	workflows.Delete("/:id/transitions/:transition_id", authMiddleware.RequirePermission("workflows:update"), workflowHandler.DeleteTransition)

	// Transition configuration routes (with action logging)
	transitions := admin.Group("/transitions", middleware.ActionLogger(middleware.ActionLoggerConfig{
		Enabled:     true,
		LogService:  actionLogService,
		SkipMethods: []string{"GET"}, // Skip read-only operations from logging
	}))
	transitions.Put("/:id/roles", authMiddleware.RequirePermission("workflows:update"), workflowHandler.SetTransitionRoles)
	transitions.Put("/:id/requirements", authMiddleware.RequirePermission("workflows:update"), workflowHandler.SetTransitionRequirements)
	transitions.Put("/:id/actions", authMiddleware.RequirePermission("workflows:update"), workflowHandler.SetTransitionActions)
	transitions.Put("/:id/field-changes", authMiddleware.RequirePermission("workflows:update"), workflowHandler.SetTransitionFieldChanges)

	// Comment Template routes (with action logging)
	commentTemplates := admin.Group("/comment-templates", middleware.ActionLogger(middleware.ActionLoggerConfig{
		Enabled:     true,
		LogService:  actionLogService,
		SkipMethods: []string{"GET"},
	}))
	commentTemplates.Post("/", authMiddleware.RequirePermission("workflows:update"), commentTemplateHandler.Create)
	commentTemplates.Get("/", authMiddleware.RequirePermission("workflows:view"), commentTemplateHandler.List)
	commentTemplates.Get("/workflow-transition/:workflowTransitionId", authMiddleware.RequirePermission("workflows:view"), commentTemplateHandler.GetByWorkflowTransitionID)
	commentTemplates.Get("/:id", authMiddleware.RequirePermission("workflows:view"), commentTemplateHandler.GetByID)
	commentTemplates.Put("/:id", authMiddleware.RequirePermission("workflows:update"), commentTemplateHandler.Update)
	commentTemplates.Delete("/:id", authMiddleware.RequirePermission("workflows:update"), commentTemplateHandler.Delete)

	feedbackTemplates := admin.Group("/feedback-templates", middleware.ActionLogger(middleware.ActionLoggerConfig{
		Enabled:     true,
		LogService:  actionLogService,
		SkipMethods: []string{"GET"},
	}))
	feedbackTemplates.Post("/", authMiddleware.RequirePermission("workflows:update"), feedbackTemplateHandler.Create)
	feedbackTemplates.Get("/", authMiddleware.RequirePermission("workflows:view"), feedbackTemplateHandler.List)
	feedbackTemplates.Get("/workflow-transition/:workflowTransitionId", authMiddleware.RequirePermission("workflows:view"), feedbackTemplateHandler.GetByWorkflowTransitionID)
	feedbackTemplates.Get("/:id", authMiddleware.RequirePermission("workflows:view"), feedbackTemplateHandler.GetByID)
	feedbackTemplates.Put("/:id", authMiddleware.RequirePermission("workflows:update"), feedbackTemplateHandler.Update)
	feedbackTemplates.Delete("/:id", authMiddleware.RequirePermission("workflows:update"), feedbackTemplateHandler.Delete)

	// Notification Template routes (admin managed: email/SMS templates with bilingual support)
	notifTemplates := admin.Group("/notification-templates", middleware.ActionLogger(middleware.ActionLoggerConfig{
		Enabled:     true,
		LogService:  actionLogService,
		SkipMethods: []string{"GET"},
	}))
	notifTemplates.Post("/", authMiddleware.RequirePermission("templates:create"), templateHandler.Create)
	notifTemplates.Get("/by-code/:code", authMiddleware.RequirePermission("templates:read"), templateHandler.GetByCode)
	notifTemplates.Get("/by-transition/:transitionId", authMiddleware.RequirePermission("templates:read"), templateHandler.GetByTransition)
	notifTemplates.Get("/available-variables", authMiddleware.RequirePermission("templates:read"), templateHandler.GetAvailableVariables)
	notifTemplates.Get("/", authMiddleware.RequirePermission("templates:read"), templateHandler.List)
	notifTemplates.Get("/:id", authMiddleware.RequirePermission("templates:read"), templateHandler.GetByID)
	notifTemplates.Put("/:id", authMiddleware.RequirePermission("templates:update"), templateHandler.Update)
	notifTemplates.Patch("/:id/toggle", authMiddleware.RequirePermission("templates:update"), templateHandler.Toggle)
	notifTemplates.Delete("/:id", authMiddleware.RequirePermission("templates:delete"), templateHandler.Delete)

	// External Integration routes
	integrationVars := admin.Group("/integration-variables")
	integrationVars.Post("/", authMiddleware.RequirePermission("admin:integration"), integrationHandler.CreateVariable)
	integrationVars.Get("/", authMiddleware.RequirePermission("admin:integration"), integrationHandler.ListVariables)
	integrationVars.Delete("/:id", authMiddleware.RequirePermission("admin:integration"), integrationHandler.DeleteVariable)

	integrationScripts := admin.Group("/integration-scripts")
	integrationScripts.Post("/", authMiddleware.RequirePermission("admin:integration"), integrationHandler.CreateScript)
	integrationScripts.Get("/", authMiddleware.RequirePermission("admin:integration"), integrationHandler.ListScripts)
	integrationScripts.Get("/:id", authMiddleware.RequirePermission("admin:integration"), integrationHandler.GetScript)
	integrationScripts.Put("/:id", authMiddleware.RequirePermission("admin:integration"), integrationHandler.UpdateScript)
	integrationScripts.Delete("/:id", authMiddleware.RequirePermission("admin:integration"), integrationHandler.DeleteScript)
	integrationScripts.Post("/:id/test", authMiddleware.RequirePermission("admin:integration"), integrationHandler.TestScript)
	integrationScripts.Get("/:id/logs", authMiddleware.RequirePermission("admin:integration"), integrationHandler.ListLogsByScript)

	// MOMRA CRM outbound sync — manual admin trigger for classification / external
	// entity / EE-classification master data (docs/MOMRA_Outbound_Integration_Spec_v1.0.md).
	momraSync := admin.Group("/momra")
	momraSync.Post("/sync/classifications", authMiddleware.RequirePermission("admin:integration"), momraSyncHandler.SyncClassifications)
	momraSync.Post("/sync/external-entities", authMiddleware.RequirePermission("admin:integration"), momraSyncHandler.SyncExternalEntities)
	momraSync.Post("/sync/external-entity-classifications", authMiddleware.RequirePermission("admin:integration"), momraSyncHandler.SyncExternalEntityClassifications)
	momraSync.Post("/sync/all", authMiddleware.RequirePermission("admin:integration"), momraSyncHandler.SyncAll)

	momraStatusMappings := admin.Group("/momra/status-mappings")
	momraStatusMappings.Post("/", authMiddleware.RequirePermission("admin:integration"), momraStatusMappingHandler.Create)
	momraStatusMappings.Get("/", authMiddleware.RequirePermission("admin:integration"), momraStatusMappingHandler.ListByWorkflow)
	momraStatusMappings.Put("/:id", authMiddleware.RequirePermission("admin:integration"), momraStatusMappingHandler.Update)
	momraStatusMappings.Delete("/:id", authMiddleware.RequirePermission("admin:integration"), momraStatusMappingHandler.Delete)
	momraSync.Get("/status-sync/logs", authMiddleware.RequirePermission("admin:integration"), momraStatusMappingHandler.ListStatusSyncLogs)
	momraSync.Post("/status-sync/logs/:logId/retry", authMiddleware.RequirePermission("admin:integration"), momraStatusMappingHandler.RetryFailedSync)

	stateIntegrations := admin.Group("/workflow-states")
	stateIntegrations.Post("/:stateId/triggers", authMiddleware.RequirePermission("admin:integration"), integrationHandler.CreateStateTrigger)
	stateIntegrations.Get("/:stateId/triggers", authMiddleware.RequirePermission("admin:integration"), integrationHandler.ListStateTriggers)
	stateIntegrations.Put("/:stateId/triggers/:id", authMiddleware.RequirePermission("admin:integration"), integrationHandler.UpdateStateTrigger)
	stateIntegrations.Delete("/:stateId/triggers/:id", authMiddleware.RequirePermission("admin:integration"), integrationHandler.DeleteStateTrigger)

	transitionIntegrations := admin.Group("/workflow-transitions")
	transitionIntegrations.Post("/:transitionId/triggers", authMiddleware.RequirePermission("admin:integration"), integrationHandler.CreateTransitionTrigger)
	transitionIntegrations.Get("/:transitionId/triggers", authMiddleware.RequirePermission("admin:integration"), integrationHandler.ListTransitionTriggers)
	transitionIntegrations.Put("/:transitionId/triggers/:id", authMiddleware.RequirePermission("admin:integration"), integrationHandler.UpdateTransitionTrigger)
	transitionIntegrations.Delete("/:transitionId/triggers/:id", authMiddleware.RequirePermission("admin:integration"), integrationHandler.DeleteTransitionTrigger)

	// Per-incident integration log + bridge viewer (accessible to incident handlers)
	v1.Get("/incidents/:incidentId/integration-logs", authMiddleware.Authenticate(), authMiddleware.RequirePermission("incidents:view"), integrationHandler.ListLogsByIncident)
	v1.Get("/incidents/:incidentId/bridges", authMiddleware.Authenticate(), authMiddleware.RequirePermission("incidents:view"), integrationHandler.ListBridgesByIncident)
	v1.Post("/incidents/:incidentId/bridges", authMiddleware.Authenticate(), authMiddleware.RequirePermission("incidents:update"), integrationHandler.CreateBridgeForIncident)

	// Webhook callback config management
	webhookConfigs := admin.Group("/webhook-configs")
	webhookConfigs.Post("/", authMiddleware.RequirePermission("admin:integration"), integrationHandler.CreateWebhookConfig)
	webhookConfigs.Get("/", authMiddleware.RequirePermission("admin:integration"), integrationHandler.ListWebhookConfigs)
	webhookConfigs.Put("/:id", authMiddleware.RequirePermission("admin:integration"), integrationHandler.UpdateWebhookConfig)
	webhookConfigs.Delete("/:id", authMiddleware.RequirePermission("admin:integration"), integrationHandler.DeleteWebhookConfig)

	// Rejection Log routes (admin-level reporting)
	rejectionLogs := admin.Group("/rejection-logs")
	rejectionLogs.Get("/", authMiddleware.RequirePermission("reports:view"), rejectionLogHandler.List)

	// Report routes
	reports := admin.Group("/reports", licenseMiddleware.RequireLicensedFeature(string(licensing.FeatureReports)))
	reports.Post("/", authMiddleware.RequirePermission("reports:create"), reportHandler.CreateReport)
	reports.Get("/", authMiddleware.RequirePermission("reports:view"), reportHandler.ListReports)
	reports.Get("/data-sources", authMiddleware.RequirePermission("reports:view"), reportHandler.GetDataSources)
	reports.Post("/preview", authMiddleware.RequirePermission("reports:view"), reportHandler.PreviewReport)
	reports.Post("/query", authMiddleware.RequirePermission("reports:view"), reportHandler.QueryReport)
	reports.Post("/export", authMiddleware.RequirePermission("reports:view"), reportHandler.ExportReport)
	reports.Get("/:id", authMiddleware.RequirePermission("reports:view"), reportHandler.GetReport)
	reports.Put("/:id", authMiddleware.RequirePermission("reports:update"), reportHandler.UpdateReport)
	reports.Delete("/:id", authMiddleware.RequirePermission("reports:delete"), reportHandler.DeleteReport)
	reports.Post("/:id/duplicate", authMiddleware.RequirePermission("reports:create"), reportHandler.DuplicateReport)
	reports.Post("/:id/execute", authMiddleware.RequirePermission("reports:view"), reportHandler.ExecuteReport)
	reports.Get("/:id/executions", authMiddleware.RequirePermission("reports:view"), reportHandler.GetExecutionHistory)

	// Report Template routes
	reportTemplates := admin.Group("/report-templates", licenseMiddleware.RequireLicensedFeature(string(licensing.FeatureReports)))
	reportTemplates.Post("/", authMiddleware.RequirePermission("reports:create"), reportTemplateHandler.CreateTemplate)
	reportTemplates.Get("/", authMiddleware.RequirePermission("reports:view"), reportTemplateHandler.ListTemplates)
	reportTemplates.Get("/default", authMiddleware.RequirePermission("reports:view"), reportTemplateHandler.GetDefaultTemplate)
	reportTemplates.Post("/preview", authMiddleware.RequirePermission("reports:view"), reportTemplateHandler.PreviewTemplate)
	reportTemplates.Post("/generate", authMiddleware.RequirePermission("reports:view"), reportTemplateHandler.GenerateReport)
	reportTemplates.Get("/:id", authMiddleware.RequirePermission("reports:view"), reportTemplateHandler.GetTemplate)
	reportTemplates.Put("/:id", authMiddleware.RequirePermission("reports:update"), reportTemplateHandler.UpdateTemplate)
	reportTemplates.Delete("/:id", authMiddleware.RequirePermission("reports:delete"), reportTemplateHandler.DeleteTemplate)
	reportTemplates.Post("/:id/duplicate", authMiddleware.RequirePermission("reports:create"), reportTemplateHandler.DuplicateTemplate)
	reportTemplates.Post("/:id/set-default", authMiddleware.RequirePermission("reports:update"), reportTemplateHandler.SetDefaultTemplate)

	// Lookup routes (admin)
	lookups := admin.Group("/lookups")
	lookups.Post("/categories", authMiddleware.RequirePermission("lookups:create"), lookupHandler.CreateCategory)
	lookups.Get("/categories", authMiddleware.RequirePermission("lookups:view"), lookupHandler.ListCategories)
	lookups.Get("/categories/:id", authMiddleware.RequirePermission("lookups:view"), lookupHandler.GetCategoryByID)
	lookups.Put("/categories/:id", authMiddleware.RequirePermission("lookups:update"), lookupHandler.UpdateCategory)
	lookups.Delete("/categories/:id", authMiddleware.RequirePermission("lookups:delete"), lookupHandler.DeleteCategory)
	lookups.Post("/categories/:id/values", authMiddleware.RequirePermission("lookups:create"), lookupHandler.CreateValue)
	lookups.Get("/categories/:id/values", authMiddleware.RequirePermission("lookups:view"), lookupHandler.ListValuesByCategory)
	lookups.Get("/values/:id", authMiddleware.RequirePermission("lookups:view"), lookupHandler.GetValueByID)
	lookups.Put("/values/:id", authMiddleware.RequirePermission("lookups:update"), lookupHandler.UpdateValue)
	lookups.Delete("/values/:id", authMiddleware.RequirePermission("lookups:delete"), lookupHandler.DeleteValue)

	// Public lookup endpoint (by category code) - accessible to authenticated users
	v1.Get("/lookups/:code", authMiddleware.Authenticate(), lookupHandler.GetValuesByCategoryCode)

	// Application Links routes (admin)
	appLinks := admin.Group("/application-links")
	appLinks.Post("/", authMiddleware.RequirePermission("application-links:create"), applicationLinkHandler.CreateLink)
	appLinks.Get("/", authMiddleware.RequirePermission("application-links:view"), applicationLinkHandler.ListLinks)
	appLinks.Get("/:id", authMiddleware.RequirePermission("application-links:view"), applicationLinkHandler.GetLink)
	appLinks.Put("/:id", authMiddleware.RequirePermission("application-links:update"), applicationLinkHandler.UpdateLink)
	appLinks.Delete("/:id", authMiddleware.RequirePermission("application-links:delete"), applicationLinkHandler.DeleteLink)
	appLinks.Post("/:id/upload-image", authMiddleware.RequirePermission("application-links:update"), applicationLinkHandler.UploadImage)
	appLinks.Delete("/:id/image", authMiddleware.RequirePermission("application-links:update"), applicationLinkHandler.RemoveImage)

	// Dashboard application links - requires explicit permission
	v1.Get("/application-links", authMiddleware.Authenticate(), authMiddleware.RequirePermission("application-links:dashboard"), applicationLinkHandler.ListActiveLinks)

	// Settings routes
	// Public settings endpoint (accessible to everyone for branding)
	v1.Get("/settings", settingsHandler.GetSettings)
	// Admin settings endpoint (requires settings:update permission)
	admin.Put("/settings", authMiddleware.RequirePermission("settings:update"), settingsHandler.UpdateSettings)

	// License management routes
	licenseGroup := admin.Group("/license")
	licenseGroup.Post("/activate", authMiddleware.RequirePermission("license:manage"), licenseHandler.Activate)
	licenseGroup.Get("/status", authMiddleware.RequirePermission("license:view"), licenseHandler.GetStatus)
	licenseGroup.Delete("/", authMiddleware.RequirePermission("license:manage"), licenseHandler.Deactivate)
	// Public license info (all authenticated users)
	v1.Get("/license/info", authMiddleware.Authenticate(), licenseHandler.GetPublicInfo)
	// Public feature catalog — no auth; describes product shape, not tenant state.
	v1.Get("/license/catalog", licenseHandler.GetCatalog)

	// Call routes (authenticated users)
	calls := v1.Group("/calls", authMiddleware.Authenticate())
	calls.Post("/start", callLogHandler.StartCall)
	calls.Post("/:call_uuid/end", callLogHandler.EndCall)
	calls.Post("/:call_uuid/join", callLogHandler.JoinCall)
	calls.Post("/:call_uuid/attachments", callLogHandler.UploadAttachment)
	calls.Get("/:attachment_id/preview", callLogHandler.PreviewAttachment)

	// Call logs routes
	callLogsPublic := v1.Group("/call-logs", authMiddleware.Authenticate())
	callLogsPublic.Get("/sip-info", callLogHandler.GetSipInfo)
	callLogsPublic.Get("/extension/:extension", callLogHandler.GetCallLogsByExtension)

	// Cintrix CTI routes (contact-center softphone widget)
	cti := v1.Group("/cti", authMiddleware.Authenticate())
	cti.Get("/widget-token", ctiHandler.GetWidgetToken)
	cti.Get("/recording", ctiHandler.GetRecording)

	// ---- TEMPLATE ROUTES (legacy path, no feature-license gate) ----
	templates := v1.Group("/templates", authMiddleware.Authenticate())
	templates.Post("/", authMiddleware.RequirePermission("templates:create"), templateHandler.Create)
	templates.Get("/", authMiddleware.RequirePermission("templates:read"), templateHandler.List)
	templates.Get("/:id", authMiddleware.RequirePermission("templates:read"), templateHandler.GetByID)
	templates.Put("/:id", authMiddleware.RequirePermission("templates:update"), templateHandler.Update)
	templates.Delete("/:id", authMiddleware.RequirePermission("templates:delete"), templateHandler.Delete)

	// ---- EXTENSION ASSIGNMENT ROUTES (EPM940 telephony feature) ----
	// Only registered for the EPM940 deployment; other clients get 404.
	if strings.EqualFold(clientCode, constants.CLIENT_CODE.EPM940) {
		extensions := v1.Group("/extensions", authMiddleware.Authenticate())
		extensions.Get("/", authMiddleware.RequirePermission("extensions:view"), extensionHandler.List)
		extensions.Get("/mine", authMiddleware.RequirePermission("extensions:view"), extensionHandler.Mine)
		extensions.Get("/:extension/history", authMiddleware.RequirePermission("extensions:view"), extensionHandler.History)
		extensions.Post("/assign", authMiddleware.RequirePermission("extensions:assign"), extensionHandler.Assign)
		extensions.Post("/create", authMiddleware.RequirePermission("extensions:create"), extensionHandler.Create)
		extensions.Delete("/:extension", authMiddleware.RequirePermission("extensions:release"), extensionHandler.Release)
	}

	// ---- NOTIFICATION ROUTES ----
	notifications := v1.Group("/notifications", authMiddleware.Authenticate(), licenseMiddleware.RequireLicensedFeature(string(licensing.FeatureCommunication)))

	// Stats
	notifications.Get("/stats", authMiddleware.RequirePermission("notifications:read"), notificationHandler.GetStats)

	// Send notifications
	notifications.Post("/send", authMiddleware.RequirePermission("notifications:send"), notificationHandler.Send)

	// List and view notifications (inbox-like functionality)
	notifications.Get("/", authMiddleware.RequirePermission("notifications:read"), notificationHandler.List)
	notifications.Get("/:id", authMiddleware.RequirePermission("notifications:read"), notificationHandler.Get)

	// Thread operations
	notifications.Post("/:id/reply", authMiddleware.RequirePermission("notifications:send"), notificationHandler.Reply)
	notifications.Get("/threads/:thread_id", authMiddleware.RequirePermission("notifications:read"), notificationHandler.GetThread)

	// Draft management
	notifications.Post("/drafts", authMiddleware.RequirePermission("notifications:create"), notificationHandler.CreateDraft)
	notifications.Put("/drafts/:id", authMiddleware.RequirePermission("notifications:update"), notificationHandler.UpdateDraft)
	notifications.Post("/drafts/:id/send", authMiddleware.RequirePermission("notifications:send"), notificationHandler.SendDraft)

	// Email actions (mark as read, star, move to folder)
	notifications.Patch("/:id/read", authMiddleware.RequirePermission("notifications:read"), notificationHandler.MarkAsRead)
	notifications.Patch("/:id/star", authMiddleware.RequirePermission("notifications:read"), notificationHandler.ToggleStar)
	notifications.Patch("/:id/move", authMiddleware.RequirePermission("notifications:read"), notificationHandler.MoveToCategory)

	// Bulk actions
	notifications.Post("/bulk/move", authMiddleware.RequirePermission("notifications:read"), notificationHandler.BulkMoveToCategory)
	notifications.Post("/bulk/delete", authMiddleware.RequirePermission("notifications:delete"), notificationHandler.BulkDelete)

	// Attachment operations
	notifications.Get("/:id/attachments/:filename", authMiddleware.RequirePermission("notifications:read"), notificationHandler.DownloadAttachment)
	notifications.Get("/:id/attachments/:filename/url", authMiddleware.RequirePermission("notifications:read"), notificationHandler.GetAttachmentURL)

	// Delete (move to trash)
	notifications.Delete("/:id", authMiddleware.RequirePermission("notifications:delete"), notificationHandler.Delete)
	notifications.Delete("/:id/permanent", authMiddleware.RequirePermission("notifications:delete"), notificationHandler.PermanentDelete)

	// OTP Routes
	// Public OTP routes for mobile login (no auth required)
	otpPublic := v1.Group("/otp")
	otpPublic.Post("/send", otpHandler.SendOTP)
	otpPublic.Post("/verify", otpHandler.VerifyOTP)

	// Authenticated OTP routes (for admin users to send OTP to others)
	otp := v1.Group("/otp", authMiddleware.Authenticate())
	otp.Post("/send", authMiddleware.RequirePermission("otp:send"), otpHandler.SendOTP)
	otp.Post("/verify", authMiddleware.RequirePermission("otp:verify"), otpHandler.VerifyOTP)

	escalation := v1.Group("/escalation", authMiddleware.Authenticate())

	// GET /api/v1/escalation— list all SLA breach notification records
	escalation.Get("/", escalationHandler.List)
	escalation.Get("/incident/:incident_id", escalationHandler.ListByIncident)

	// Communication Tracking Dashboard (admin, Call Center module) — cross-user delivery tracking, search & filter
	notificationMonitoring := admin.Group("/notification-monitoring", authMiddleware.Authenticate(), licenseMiddleware.RequireLicensedFeature(string(licensing.FeatureCommunication)))
	notificationMonitoring.Get("/", authMiddleware.RequirePermission("communication-tracking:view"), notificationHandler.ListMonitoring)
	notificationMonitoring.Get("/:id", authMiddleware.RequirePermission("communication-tracking:view"), notificationHandler.Get)
	notificationMonitoring.Post("/:id/resend", authMiddleware.RequirePermission("communication-tracking:update"), notificationHandler.ResendNotification)

	// Custom Escalation Groups (admin)
	escalationGroups := admin.Group("/escalation-groups", authMiddleware.Authenticate(), authMiddleware.RequirePermission("escalation-groups:manage_rules"), licenseMiddleware.RequireLicensedFeature(string(licensing.FeatureEscalation)))
	escalationGroups.Post("/", escalationGroupHandler.Create)
	escalationGroups.Get("/", escalationGroupHandler.List)
	escalationGroups.Get("/:id", escalationGroupHandler.GetByID)
	escalationGroups.Put("/:id", escalationGroupHandler.Update)
	escalationGroups.Delete("/:id", escalationGroupHandler.Delete)

	// Escalation Policies (admin)
	escalationPolicies := admin.Group("/escalation-policies", authMiddleware.Authenticate(), authMiddleware.RequirePermission("escalation-groups:manage_rules"), licenseMiddleware.RequireLicensedFeature(string(licensing.FeatureEscalation)))
	escalationPolicies.Post("/resolve-users", escalationPolicyHandler.ResolveTargetUsers)
	escalationPolicies.Get("/", escalationPolicyHandler.List)
	escalationPolicies.Post("/", escalationPolicyHandler.Create)
	escalationPolicies.Get("/:id", escalationPolicyHandler.GetByID)
	escalationPolicies.Put("/:id", escalationPolicyHandler.Update)
	escalationPolicies.Delete("/:id", escalationPolicyHandler.Delete)
	escalationPolicies.Put("/:id/targets/:target_id/excluded-users", escalationPolicyHandler.UpdateTargetExcludedUsers)

	//FCM
	fcm := v1.Group("/fcm", authMiddleware.Authenticate())
	fcm.Post("/register", fcmHandler.RegisterToken)
	fcm.Post("/push", fcmHandler.PushNotification)
	fcm.Get("/device-tokens", fcmHandler.GetUserDeviceTokens)
	fcm.Delete("/remove-device", fcmHandler.RemoveDevice)

	//Caller sentiments
	callerSentiments := v1.Group("/caller-sentiments", authMiddleware.Authenticate(), licenseMiddleware.RequireLicensedFeature(string(licensing.FeatureCallCentre)))
	callerSentiments.Post("/", authMiddleware.RequirePermission("caller-sentiment:create"), sentimentHandler.Create)
	callerSentiments.Get("/", authMiddleware.RequirePermission("caller-sentiment:view"), sentimentHandler.GetAllCallerSentiments)
	callerSentiments.Get("/:caller_id", authMiddleware.RequirePermission("caller-sentiment:view"), sentimentHandler.GetCallerSentiments)
	callerSentiments.Get("/:caller_id/:callee_id", sentimentHandler.GetCallerSentimentsByCallerAndCallee)

	if cfg.GoalManagement.Enabled {
		// ---- GOAL TEMPLATE ROUTES ----
		goalTemplates := v1.Group("/goal-templates", authMiddleware.Authenticate(), licenseMiddleware.RequireLicensedFeature(string(licensing.FeatureGoals)))
		goalTemplates.Post("/", authMiddleware.RequirePermission("goals:create"), goalTemplateHandler.Create)
		goalTemplates.Get("/", authMiddleware.RequirePermission("goals:view"), goalTemplateHandler.List)
		goalTemplates.Get("/active", authMiddleware.RequirePermission("goals:view"), goalTemplateHandler.ListActive)
		goalTemplates.Get("/:id", authMiddleware.RequirePermission("goals:view"), goalTemplateHandler.GetByID)
		goalTemplates.Put("/:id", authMiddleware.RequirePermission("goals:update"), goalTemplateHandler.Update)
		goalTemplates.Delete("/:id", authMiddleware.RequirePermission("goals:delete"), goalTemplateHandler.Delete)

		// ---- GOAL MANAGEMENT ROUTES ----
		goals := v1.Group("/goals", authMiddleware.Authenticate(), licenseMiddleware.RequireLicensedFeature(string(licensing.FeatureGoals)))
		goals.Get("/export", authMiddleware.RequirePermission("goals:view"), goalHandler.ExportGoals)
		goals.Post("/import", authMiddleware.RequirePermission("goals:create"), goalHandler.ImportGoals)
		goals.Get("/metrics/export-template", authMiddleware.RequirePermission("goals:view"), goalHandler.ExportMetricsTemplate)
		goals.Post("/metrics/import", authMiddleware.RequirePermission("goals:update"), goalHandler.ImportMetrics)
		goals.Get("/metric-batches", authMiddleware.RequirePermission("goals:view"), goalHandler.ListMetricImportBatches)
		goals.Get("/metric-batches/:id", authMiddleware.RequirePermission("goals:view"), goalHandler.GetMetricImportBatch)
		goals.Delete("/metric-batches/:id", authMiddleware.RequirePermission("goals:update"), goalHandler.DeleteMetricImportBatch)
		goals.Get("/metric-batches/:id/available-transitions", authMiddleware.RequirePermission("goals:view"), goalHandler.GetAvailableMetricBatchTransitions)
		goals.Post("/metric-batches/:id/transition", authMiddleware.RequirePermission("goals:update"), goalHandler.ExecuteMetricBatchTransition)
		goals.Get("/metric-batches/:id/transition-history", authMiddleware.RequirePermission("goals:view"), goalHandler.GetMetricBatchTransitionHistory)
		goals.Post("/bulk", authMiddleware.RequirePermission("goals:update"), goalHandler.BulkAction)
		// Analytics routes (must be before /:id catch-all)
		goals.Get("/analytics/stats", authMiddleware.RequirePermission("goals:view"), goalAnalyticsHandler.GetGoalStats)
		goals.Get("/analytics/distributions", authMiddleware.RequirePermission("goals:view"), goalAnalyticsHandler.GetDistributions)
		goals.Get("/analytics/progress", authMiddleware.RequirePermission("goals:view"), goalAnalyticsHandler.GetProgressSummary)
		goals.Get("/analytics/at-risk", authMiddleware.RequirePermission("goals:view"), goalAnalyticsHandler.GetAtRiskGoals)
		goals.Get("/analytics/trends", authMiddleware.RequirePermission("goals:view"), goalAnalyticsHandler.GetTrendData)
		goals.Get("/analytics/okr-tree", authMiddleware.RequirePermission("goals:view"), goalAnalyticsHandler.GetOKRTree)
		goals.Post("/", authMiddleware.RequirePermission("goals:create"), goalHandler.CreateGoal)
		goals.Get("/", authMiddleware.RequirePermission("goals:view"), goalHandler.ListGoals)
		goals.Get("/:id/activity", authMiddleware.RequirePermission("goals:view"), goalHandler.GetGoalActivity)
		goals.Post("/:id/comments", authMiddleware.RequirePermission("goals:update"), goalHandler.AddGoalComment)
		goals.Get("/:id/comments", authMiddleware.RequirePermission("goals:view"), goalHandler.ListGoalComments)
		goals.Delete("/comments/:cid", authMiddleware.RequirePermission("goals:update"), goalHandler.DeleteGoalComment)
		goals.Get("/:id", authMiddleware.RequirePermission("goals:view"), goalHandler.GetGoal)
		goals.Put("/:id", authMiddleware.RequirePermission("goals:update"), goalHandler.UpdateGoal)
		goals.Delete("/:id", authMiddleware.RequirePermission("goals:delete"), goalHandler.DeleteGoal)
		goals.Post("/:id/transition", authMiddleware.RequirePermission("goals:update"), goalHandler.TransitionStatus)
		goals.Post("/:id/clone", authMiddleware.RequirePermission("goals:create"), goalHandler.CloneGoal)
		goals.Get("/:id/children", authMiddleware.RequirePermission("goals:view"), goalHandler.ListChildGoals)
		goals.Get("/:id/tree", authMiddleware.RequirePermission("goals:view"), goalHandler.GetGoalTree)
		goals.Post("/:id/check-ins", authMiddleware.RequirePermission("goals:update"), goalHandler.CreateCheckIn)
		goals.Get("/:id/check-ins", authMiddleware.RequirePermission("goals:view"), goalHandler.ListCheckIns)
		goals.Delete("/check-ins/:id", authMiddleware.RequirePermission("goals:update"), goalHandler.DeleteCheckIn)
		goals.Post("/:id/collaborators", authMiddleware.RequirePermission("goals:assign"), goalHandler.AddCollaborator)
		goals.Delete("/:id/collaborators/:user_id", authMiddleware.RequirePermission("goals:assign"), goalHandler.RemoveCollaborator)
		goals.Post("/:gid/metrics", authMiddleware.RequirePermission("goals:update"), goalHandler.CreateMetric)
		goals.Put("/metrics/:id", authMiddleware.RequirePermission("goals:update"), goalHandler.UpdateMetric)
		goals.Delete("/metrics/:id", authMiddleware.RequirePermission("goals:update"), goalHandler.DeleteMetric)
		goals.Put("/metrics/:id/value", authMiddleware.RequirePermission("goals:update"), goalHandler.UpdateMetricValue)
		goals.Get("/metrics/:id/history", authMiddleware.RequirePermission("goals:view"), goalHandler.GetMetricHistory)
		goals.Post("/:gid/evidences", authMiddleware.RequirePermission("goals:update"), goalHandler.UploadEvidence)
		goals.Get("/:gid/evidences", authMiddleware.RequirePermission("goals:view"), goalHandler.ListEvidences)
		goals.Get("/evidences/:id", authMiddleware.RequirePermission("goals:view"), goalHandler.GetEvidence)
		goals.Get("/evidences/:id/preview", authMiddleware.RequirePermission("goals:view"), goalHandler.GetEvidencePreview)
		goals.Get("/evidences/:id/download", authMiddleware.RequirePermission("goals:view"), goalHandler.DownloadEvidence)
		goals.Delete("/evidences/:id", authMiddleware.RequirePermission("goals:update"), goalHandler.DeleteEvidence)
		goals.Put("/evidences/:id/file", authMiddleware.RequirePermission("goals:update"), goalHandler.ReplaceEvidenceFile)
		goals.Get("/evidences/:id/available-transitions", authMiddleware.RequirePermission("goals:view"), goalHandler.GetAvailableEvidenceTransitions)
		goals.Post("/evidences/:id/transition", authMiddleware.RequirePermission("goals:update"), goalHandler.ExecuteEvidenceTransition)
		goals.Get("/evidences/:id/transition-history", authMiddleware.RequirePermission("goals:view"), goalHandler.GetEvidenceTransitionHistory)

		// ---- METRIC APPROVAL WORKFLOW ROUTES ----
		goalMetrics := v1.Group("/goal-metrics", authMiddleware.Authenticate(), licenseMiddleware.RequireLicensedFeature(string(licensing.FeatureGoals)))
		goalMetrics.Get("/:id/available-transitions", authMiddleware.RequirePermission("goals:view"), goalHandler.GetAvailableMetricTransitions)
		goalMetrics.Post("/:id/transition", authMiddleware.RequirePermission("goals:approve"), goalHandler.TransitionMetric)

		goalMetricValueChanges := v1.Group("/goal-metric-value-changes", authMiddleware.Authenticate(), licenseMiddleware.RequireLicensedFeature(string(licensing.FeatureGoals)))
		goalMetricValueChanges.Get("/:id/available-transitions", authMiddleware.RequirePermission("goals:view"), goalHandler.GetAvailableMetricValueChangeTransitions)
		goalMetricValueChanges.Post("/:id/transition", authMiddleware.RequirePermission("goals:approve"), goalHandler.TransitionMetricValueChange)

		// Per-metric value-change history (any caller with access to the parent goal)
		goals.Get("/:id/metrics/:metric_id/value-changes", authMiddleware.RequirePermission("goals:view"), goalHandler.ListMetricValueChanges)

		// ---- APPROVAL ROUTES ----
		approvals := v1.Group("/approvals", authMiddleware.Authenticate(), licenseMiddleware.RequireLicensedFeature(string(licensing.FeatureGoals)))
		approvals.Get("/pending", authMiddleware.RequirePermission("goals:approve"), goalHandler.ListPendingApprovals)
		approvals.Get("/completed", authMiddleware.RequirePermission("goals:approve"), goalHandler.ListCompletedApprovals)
		approvals.Get("/pending-metrics", authMiddleware.RequirePermission("goals:approve"), goalHandler.ListPendingMetricApprovals)
		approvals.Get("/pending-metric-value-changes", authMiddleware.RequirePermission("goals:approve"), goalHandler.ListPendingMetricValueChangeApprovals)

		// ---- PERFORMANCE REVIEW ROUTES ----
		reviews := v1.Group("/reviews", authMiddleware.Authenticate(), licenseMiddleware.RequireLicensedFeature(string(licensing.FeatureGoals)))
		reviews.Post("/cycles", authMiddleware.RequirePermission("goals:update"), reviewHandler.CreateCycle)
		reviews.Get("/cycles", authMiddleware.RequirePermission("goals:view"), reviewHandler.ListCycles)
		reviews.Get("/cycles/:id", authMiddleware.RequirePermission("goals:view"), reviewHandler.GetCycle)
		reviews.Put("/cycles/:id", authMiddleware.RequirePermission("goals:update"), reviewHandler.UpdateCycle)
		reviews.Delete("/cycles/:id", authMiddleware.RequirePermission("goals:delete"), reviewHandler.DeleteCycle)
		reviews.Post("/cycles/:id/activate", authMiddleware.RequirePermission("goals:update"), reviewHandler.ActivateCycle)
		reviews.Post("/cycles/:id/complete", authMiddleware.RequirePermission("goals:update"), reviewHandler.CompleteCycle)
		reviews.Post("/cycles/:id/assignments", authMiddleware.RequirePermission("goals:assign"), reviewHandler.AssignReviewees)
		reviews.Get("/cycles/:id/assignments", authMiddleware.RequirePermission("goals:view"), reviewHandler.ListCycleAssignments)
		reviews.Delete("/assignments/:id", authMiddleware.RequirePermission("goals:assign"), reviewHandler.RemoveAssignment)
		reviews.Get("/assignments/:id", authMiddleware.RequirePermission("goals:view"), reviewHandler.GetAssignment)
		reviews.Post("/assignments/:id/score", authMiddleware.RequirePermission("goals:update"), reviewHandler.ScoreGoals)
		reviews.Post("/assignments/:id/submit", authMiddleware.RequirePermission("goals:update"), reviewHandler.SubmitReview)
		reviews.Get("/my-reviews", authMiddleware.RequirePermission("goals:view"), reviewHandler.ListMyReviews)
		reviews.Get("/my-review-tasks", authMiddleware.RequirePermission("goals:view"), reviewHandler.ListMyReviewTasks)

		// ---- DOCUMENT MANAGEMENT ROUTES ----
		docs := v1.Group("/documents", authMiddleware.Authenticate(), licenseMiddleware.RequireLicensedFeature(string(licensing.FeatureDocuments)))
		docs.Get("/files", authMiddleware.RequirePermission("goals:view"), documentHandler.ListFiles)
		docs.Post("/search", authMiddleware.RequirePermission("goals:view"), documentHandler.SearchFiles)
		docs.Get("/files/:id/info", authMiddleware.RequirePermission("goals:view"), documentHandler.GetFileInfo)
		docs.Get("/files/:id/breadcrumb", authMiddleware.RequirePermission("goals:view"), documentHandler.GetFileBreadcrumb)
		docs.Get("/files/:id/preview", authMiddleware.RequirePermission("goals:view"), documentHandler.GetPreviewURL)
		docs.Get("/files/:id/download", authMiddleware.RequirePermission("goals:view"), documentHandler.DownloadFile)
		docs.Get("/files/:id/comments", authMiddleware.RequirePermission("goals:view"), documentHandler.GetComments)
		docs.Post("/files/:id/comments", authMiddleware.RequirePermission("goals:update"), documentHandler.AddComment)
		docs.Get("/files/:id/tags", authMiddleware.RequirePermission("goals:view"), documentHandler.GetTags)
		docs.Put("/files/:id/tags", authMiddleware.RequirePermission("goals:update"), documentHandler.SetTags)
		docs.Get("/files/:id/versions", authMiddleware.RequirePermission("goals:view"), documentHandler.ListVersions)
		docs.Post("/files/:id/versions", authMiddleware.RequirePermission("goals:update"), documentHandler.UploadVersion)
		docs.Get("/versions/:vid/download", authMiddleware.RequirePermission("goals:view"), documentHandler.DownloadVersion)
		docs.Post("/files/:id/versions/rollback", authMiddleware.RequirePermission("goals:update"), documentHandler.RollbackVersion)
	} // end cfg.GoalManagement.Enabled

	// ---- KPI MASTER DATA ROUTES ----
	kpi := v1.Group("/kpi", authMiddleware.Authenticate())

	// Dashboard
	kpi.Get("/dashboard", authMiddleware.RequirePermission("kpi:view"), kpiDashboardHandler.GetDashboard)

	// Performance bands (RAG thresholds)
	kpi.Get("/performance-bands", authMiddleware.RequirePermission("kpi:view"), kpiPerformanceBandHandler.ListBands)
	kpi.Get("/performance-bands/effective", authMiddleware.RequirePermission("kpi:view"), kpiPerformanceBandHandler.GetEffectiveBand)
	kpi.Post("/performance-bands", authMiddleware.RequirePermission("goals:manage"), kpiPerformanceBandHandler.UpsertBand)
	kpi.Delete("/performance-bands/:id", authMiddleware.RequirePermission("goals:manage"), kpiPerformanceBandHandler.DeleteBand)

	kpi.Get("/pillars", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.ListPillars)
	kpi.Post("/pillars", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.CreatePillar)
	kpi.Put("/pillars/:id", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.UpdatePillar)
	kpi.Delete("/pillars/:id", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.DeletePillar)

	kpi.Get("/enablers", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.ListEnablers)
	kpi.Post("/enablers", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.CreateEnabler)
	kpi.Put("/enablers/:id", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.UpdateEnabler)
	kpi.Delete("/enablers/:id", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.DeleteEnabler)

	kpi.Get("/operational-objectives", authMiddleware.RequirePermission("goals:manage", "kpi:view"), kpiMasterDataHandler.ListOperationalObjectives)
	kpi.Post("/operational-objectives", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.CreateOperationalObjective)
	kpi.Put("/operational-objectives/:id", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.UpdateOperationalObjective)
	kpi.Delete("/operational-objectives/:id", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.DeleteOperationalObjective)
	kpi.Get("/operational-objectives/:id/kpis", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.ListKpisForOperationalObjective)
	kpi.Get("/operational-objectives/:id/collaborators", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.ListCollaboratorsForOperationalObjective)
	kpi.Get("/operational-objectives/:id/evidence", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.ListEvidenceForOperationalObjective)

	kpi.Get("/processes", authMiddleware.RequirePermission("goals:manage", "kpi:view"), kpiMasterDataHandler.ListProcesses)
	kpi.Post("/processes", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.CreateProcess)
	kpi.Put("/processes/:id", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.UpdateProcess)
	kpi.Delete("/processes/:id", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.DeleteProcess)
	kpi.Get("/processes/:id/kpis", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.ListKpisForProcess)
	kpi.Get("/processes/:id/collaborators", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.ListCollaboratorsForProcess)
	kpi.Get("/processes/:id/evidence", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.ListEvidenceForProcess)

	kpi.Get("/initiatives", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.ListInitiatives)
	kpi.Post("/initiatives", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.CreateInitiative)
	kpi.Put("/initiatives/:id", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.UpdateInitiative)
	kpi.Delete("/initiatives/:id", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.DeleteInitiative)

	kpi.Get("/domains", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.ListDomains)
	kpi.Post("/domains", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.CreateDomain)
	kpi.Put("/domains/:id", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.UpdateDomain)
	kpi.Delete("/domains/:id", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.DeleteDomain)

	kpi.Get("/award-criteria", authMiddleware.RequirePermission("goals:manage", "kpi:view"), kpiMasterDataHandler.ListAwardCriteria)
	kpi.Post("/award-criteria", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.CreateAwardCriterion)
	kpi.Put("/award-criteria/:id", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.UpdateAwardCriterion)
	kpi.Delete("/award-criteria/:id", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.DeleteAwardCriterion)
	kpi.Get("/award-criteria/:id/kpis", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.ListKpisForAwardCriterion)
	kpi.Get("/award-criteria/:id/collaborators", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.ListCollaboratorsForAwardCriterion)
	kpi.Get("/award-criteria/:id/evidence", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.ListEvidenceForAwardCriterion)

	kpi.Get("/award-criteria/:criterionId/sub-criteria", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.ListAwardSubCriteria)
	kpi.Post("/award-criteria/:criterionId/sub-criteria", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.CreateAwardSubCriterion)
	kpi.Get("/award-sub-criteria", authMiddleware.RequirePermission("goals:manage", "kpi:view"), kpiMasterDataHandler.ListAwardSubCriteria)
	kpi.Put("/award-sub-criteria/:id", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.UpdateAwardSubCriterion)
	kpi.Delete("/award-sub-criteria/:id", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.DeleteAwardSubCriterion)
	kpi.Get("/award-sub-criteria/:id/kpis", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.ListKpisForAwardSubCriterion)
	kpi.Get("/award-sub-criteria/:id/collaborators", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.ListCollaboratorsForAwardSubCriterion)
	kpi.Get("/award-sub-criteria/:id/evidence", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.ListEvidenceForAwardSubCriterion)

	kpi.Get("/data-sources", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.ListDataSources)
	kpi.Post("/data-sources", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.CreateDataSource)
	kpi.Put("/data-sources/:id", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.UpdateDataSource)
	kpi.Delete("/data-sources/:id", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.DeleteDataSource)

	kpi.Get("/segmentation-dimensions", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.ListSegmentationDimensions)
	kpi.Post("/segmentation-dimensions", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.CreateSegmentationDimension)
	kpi.Put("/segmentation-dimensions/:id", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.UpdateSegmentationDimension)
	kpi.Delete("/segmentation-dimensions/:id", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.DeleteSegmentationDimension)

	kpi.Get("/organizations", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.ListOrganizations)
	kpi.Post("/organizations", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.CreateOrganization)
	kpi.Put("/organizations/:id", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.UpdateOrganization)
	kpi.Delete("/organizations/:id", authMiddleware.RequirePermission("goals:manage"), kpiMasterDataHandler.DeleteOrganization)

	kpi.Get("/:type/:id/segmentation-axes", authMiddleware.RequirePermission("kpi:view"), kpiMasterDataHandler.ListSegmentationAxes)
	kpi.Post("/:type/:id/segmentation-axes", authMiddleware.RequirePermission("kpi:update"), kpiMasterDataHandler.AddSegmentationAxis)
	kpi.Delete("/segmentation-axes/:id", authMiddleware.RequirePermission("kpi:update"), kpiMasterDataHandler.DeleteSegmentationAxis)

	kpi.Get("/:type/:id/administrative-units", authMiddleware.RequirePermission("kpi:view"), kpiMasterDataHandler.ListAdministrativeUnits)
	kpi.Post("/:type/:id/administrative-units", authMiddleware.RequirePermission("kpi:update"), kpiMasterDataHandler.AddAdministrativeUnit)
	kpi.Delete("/administrative-units/:id", authMiddleware.RequirePermission("kpi:update"), kpiMasterDataHandler.DeleteAdministrativeUnit)

	// ---- KPI EVIDENCE FOLDER CONFIGURATION ROUTES ----
	// Taxonomy-parameterized (not KPI-id-parameterized) so they work from the
	// create form before a KPI is ever saved.
	kpi.Get("/documenta/anchor", authMiddleware.RequirePermission("kpi:create", "kpi:update"), kpiDocumentaHandler.ResolveAnchor)
	kpi.Get("/documenta/folders", authMiddleware.RequirePermission("kpi:create", "kpi:update"), kpiDocumentaHandler.ListFolders)
	kpi.Post("/documenta/folders", authMiddleware.RequirePermission("kpi:create", "kpi:update"), kpiDocumentaHandler.CreateFolder)
	kpi.Get("/documenta/folders/:id", authMiddleware.RequirePermission("kpi:view"), kpiDocumentaHandler.GetFolderInfo)

	// ---- KPI DICTIONARY ROUTES ----
	kpi.Get("/strategic", authMiddleware.RequirePermission("kpi:view"), kpiDictionaryHandler.ListStrategic)
	kpi.Get("/strategic/:id", authMiddleware.RequirePermission("kpi:view"), kpiDictionaryHandler.GetStrategic)
	kpi.Post("/strategic", authMiddleware.RequirePermission("kpi:create"), kpiDictionaryHandler.CreateStrategic)
	kpi.Put("/strategic/:id", authMiddleware.RequirePermission("kpi:update"), kpiDictionaryHandler.UpdateStrategic)
	kpi.Delete("/strategic/:id", authMiddleware.RequirePermission("kpi:delete"), kpiDictionaryHandler.DeleteStrategic)

	kpi.Get("/operational", authMiddleware.RequirePermission("kpi:view"), kpiDictionaryHandler.ListOperational)
	kpi.Get("/operational/:id", authMiddleware.RequirePermission("kpi:view"), kpiDictionaryHandler.GetOperational)
	kpi.Post("/operational", authMiddleware.RequirePermission("kpi:create"), kpiDictionaryHandler.CreateOperational)
	kpi.Put("/operational/:id", authMiddleware.RequirePermission("kpi:update"), kpiDictionaryHandler.UpdateOperational)
	kpi.Delete("/operational/:id", authMiddleware.RequirePermission("kpi:delete"), kpiDictionaryHandler.DeleteOperational)

	kpi.Get("/award", authMiddleware.RequirePermission("kpi:view"), kpiDictionaryHandler.ListAward)
	kpi.Get("/award/:id", authMiddleware.RequirePermission("kpi:view"), kpiDictionaryHandler.GetAward)
	kpi.Post("/award", authMiddleware.RequirePermission("kpi:create"), kpiDictionaryHandler.CreateAward)
	kpi.Put("/award/:id", authMiddleware.RequirePermission("kpi:update"), kpiDictionaryHandler.UpdateAward)
	kpi.Delete("/award/:id", authMiddleware.RequirePermission("kpi:delete"), kpiDictionaryHandler.DeleteAward)

	// ---- KPI PERFORMANCE ROUTES (must be before /:type/:id/transition to avoid route conflict) ----
	kpi.Get("/targets", authMiddleware.RequirePermission("targets:view"), kpiPerformanceHandler.ListTargets)
	kpi.Post("/targets", authMiddleware.RequirePermission("targets:set"), kpiPerformanceHandler.SetTarget)
	kpi.Put("/targets/:id", authMiddleware.RequirePermission("targets:set"), kpiPerformanceHandler.UpdateTarget)
	// RequirePermission is OR-based (any of these grants route access) — the
	// finer-grained approve-vs-reject split happens inside TransitionTarget
	// itself, since the action (approve/reject/return) isn't known until the
	// request body is parsed.
	kpi.Post("/targets/:id/transition", authMiddleware.RequirePermission("targets:approve", "targets:reject"), kpiPerformanceHandler.TransitionTarget)
	kpi.Delete("/targets/:id", authMiddleware.RequirePermission("targets:delete"), kpiPerformanceHandler.DeleteTarget)

	kpi.Get("/performance", authMiddleware.RequirePermission("perf:view"), kpiPerformanceHandler.ListPerformance)
	kpi.Get("/performance/:id", authMiddleware.RequirePermission("perf:view"), kpiPerformanceHandler.GetPerformance)
	kpi.Post("/performance", authMiddleware.RequirePermission("perf:submit"), kpiPerformanceHandler.SubmitPerformance)
	kpi.Put("/performance/:id", authMiddleware.RequirePermission("perf:submit"), kpiPerformanceHandler.UpdatePerformance)
	kpi.Delete("/performance/:id", authMiddleware.RequirePermission("perf:submit"), kpiPerformanceHandler.DeletePerformance)
	kpi.Post("/performance/:id/transition", authMiddleware.RequirePermission("perf:review"), kpiPerformanceHandler.TransitionPerformance)
	kpi.Get("/performance/:id/transitions", authMiddleware.RequirePermission("perf:view"), kpiPerformanceHandler.GetAvailableTransitions)
	kpi.Get("/performance/:id/history", authMiddleware.RequirePermission("perf:view"), kpiPerformanceHandler.GetPerformanceHistory)
	kpi.Get("/performance/:id/evidence", authMiddleware.RequirePermission("perf:view"), kpiPerformanceHandler.ListPerformanceEvidence)
	kpi.Post("/performance/:id/evidence", authMiddleware.RequirePermission("perf:submit"), kpiPerformanceHandler.CreatePerformanceEvidence)
	kpi.Delete("/performance/:id/evidence/:evidenceId", authMiddleware.RequirePermission("perf:submit"), kpiPerformanceHandler.DeletePerformanceEvidence)

	// These entries/* routes must be registered BEFORE the /:type/:id/transition
	// wildcard below — Fiber matches routes in registration order, and "entries"
	// would otherwise satisfy the :type param and swallow these requests.
	kpi.Post("/entries/:entryId/transition", authMiddleware.RequirePermission("perf:review"), kpiEntryHandler.TransitionEntry)
	kpi.Get("/entries/:entryId/transitions", authMiddleware.RequirePermission("kpi:view"), kpiEntryHandler.GetAvailableEntryTransitions)
	kpi.Get("/entries/:entryId/history", authMiddleware.RequirePermission("kpi:view"), kpiEntryHandler.GetEntryHistory)

	kpi.Get("/:type/:id/transitions", authMiddleware.RequirePermission("kpi:view"), kpiDictionaryHandler.GetAvailableTransitions)
	kpi.Post("/:type/:id/transition", authMiddleware.RequirePermission("kpi:update"), kpiDictionaryHandler.TransitionKpi)

	kpi.Get("/:type/:id/card", authMiddleware.RequirePermission("kpi:view"), kpiComposedHandler.GetKpiCard)
	kpi.Get("/:type/:id/dashboard", authMiddleware.RequirePermission("kpi:view"), kpiComposedHandler.GetKpiDashboard)
	kpi.Get("/:type/:id/annual-rollup", authMiddleware.RequirePermission("kpi:view"), kpiComposedHandler.GetKpiAnnualRollup)
	kpi.Get("/:type/:id/metrics/:metricId/period-rollup", authMiddleware.RequirePermission("kpi:view"), kpiComposedHandler.GetMetricPeriodRollup)
	kpi.Get("/:type/:id/metrics/:metricId/display-rollup", authMiddleware.RequirePermission("kpi:view"), kpiComposedHandler.GetMetricDisplayRollup)
	kpi.Get("/:type/:id/metrics/:metricId/period-series", authMiddleware.RequirePermission("kpi:view"), kpiComposedHandler.GetMetricPeriodSeries)
	kpi.Get("/:type/:id/composite-score", authMiddleware.RequirePermission("kpi:view"), kpiComposedHandler.GetCompositeScore)
	kpi.Get("/:type/:id/composite-score-latest", authMiddleware.RequirePermission("kpi:view"), kpiComposedHandler.GetCompositeScoreLatest)

	// KPI engagement features — metrics, evidence, collaborators, check-ins, comments, activity
	kpi.Get("/metrics-by-code/:code", authMiddleware.RequirePermission("kpi:view"), kpiEngagementHandler.ListMetricsByCode)

	// KPI Entries
	kpi.Get("/entries", authMiddleware.RequirePermission("kpi:view"), kpiEntryHandler.ListAllEntries)
	kpi.Get("/:type/:id/entries", authMiddleware.RequirePermission("kpi:view"), kpiEntryHandler.ListEntries)
	kpi.Post("/:type/:id/entries", authMiddleware.RequirePermission("kpi:update"), kpiEntryHandler.CreateEntry)
	kpi.Put("/:type/:id/entries/:entryId", authMiddleware.RequirePermission("kpi:update"), kpiEntryHandler.UpdateEntry)
	kpi.Delete("/:type/:id/entries/:entryId", authMiddleware.RequirePermission("kpi:update"), kpiEntryHandler.DeleteEntry)
	kpi.Get("/entries/:entryId/evidence", authMiddleware.RequirePermission("kpi:view"), kpiEntryHandler.ListEntryEvidence)

	kpi.Get("/:type/:id/metrics", authMiddleware.RequirePermission("kpi:view"), kpiEngagementHandler.ListMetrics)
	kpi.Post("/:type/:id/metrics", authMiddleware.RequirePermission("kpi:update"), kpiEngagementHandler.CreateMetric)
	kpi.Post("/:type/:id/attachment", authMiddleware.RequirePermission("kpi:update"), kpiEngagementHandler.UploadAttachment)
	kpi.Put("/metrics/:id", authMiddleware.RequirePermission("kpi:update"), kpiEngagementHandler.UpdateMetric)
	kpi.Put("/metrics/:id/value", authMiddleware.RequirePermission("kpi:update"), kpiEngagementHandler.UpdateMetricValue)
	kpi.Delete("/metrics/:id", authMiddleware.RequirePermission("kpi:update"), kpiEngagementHandler.DeleteMetric)

	kpi.Get("/:type/:id/evidence", authMiddleware.RequirePermission("kpi:view"), kpiEngagementHandler.ListEvidence)
	kpi.Post("/:type/:id/evidence", authMiddleware.RequirePermission("kpi:update"), kpiEngagementHandler.CreateEvidence)
	kpi.Delete("/evidence/:id", authMiddleware.RequirePermission("kpi:update"), kpiEngagementHandler.DeleteEvidence)
	kpi.Get("/evidence/:id/download", authMiddleware.RequirePermission("kpi:view"), kpiEngagementHandler.DownloadEvidence)

	kpi.Get("/:type/:id/collaborators", authMiddleware.RequirePermission("kpi:view"), kpiEngagementHandler.ListCollaborators)
	kpi.Post("/:type/:id/collaborators", authMiddleware.RequirePermission("kpi:assign"), kpiEngagementHandler.AddCollaborator)
	kpi.Delete("/:type/:id/collaborators/:user_id", authMiddleware.RequirePermission("kpi:assign"), kpiEngagementHandler.RemoveCollaborator)

	kpi.Get("/:type/:id/collaborator-assignments", authMiddleware.RequirePermission("kpi:view"), kpiCollaboratorHandler.ListAssignments)
	kpi.Get("/collaborator-assignments/:assignmentId", authMiddleware.RequirePermission("kpi:view"), kpiCollaboratorHandler.GetAssignment)
	kpi.Post("/:type/:id/collaborator-assignments", authMiddleware.RequirePermission("kpi:assign"), kpiCollaboratorHandler.CreateAssignment)
	kpi.Put("/collaborator-assignments/:assignmentId", authMiddleware.RequirePermission("kpi:assign"), kpiCollaboratorHandler.UpdateAssignment)
	kpi.Delete("/collaborator-assignments/:assignmentId", authMiddleware.RequirePermission("kpi:assign"), kpiCollaboratorHandler.DeleteAssignment)
	kpi.Get("/collaborator-permission-matrix", authMiddleware.RequirePermission("kpi:view"), kpiCollaboratorHandler.GetPermissionMatrix)

	kpi.Get("/:type/:id/check-ins", authMiddleware.RequirePermission("kpi:view"), kpiEngagementHandler.ListCheckIns)
	kpi.Post("/:type/:id/check-ins", authMiddleware.RequirePermission("kpi:update"), kpiEngagementHandler.CreateCheckIn)
	kpi.Delete("/check-ins/:id", authMiddleware.RequirePermission("kpi:update"), kpiEngagementHandler.DeleteCheckIn)

	kpi.Get("/:type/:id/comments", authMiddleware.RequirePermission("kpi:view"), kpiEngagementHandler.ListComments)
	kpi.Post("/:type/:id/comments", authMiddleware.RequirePermission("kpi:update"), kpiEngagementHandler.AddComment)
	kpi.Delete("/comments/:id", authMiddleware.RequirePermission("kpi:update"), kpiEngagementHandler.DeleteComment)

	kpi.Get("/:type/:id/activity", authMiddleware.RequirePermission("kpi:view"), kpiEngagementHandler.GetActivity)

	kpi.Get("/benchmarks", authMiddleware.RequirePermission("benchmark:manage"), kpiPerformanceHandler.ListBenchmarks)
	kpi.Post("/benchmarks", authMiddleware.RequirePermission("benchmark:manage"), kpiPerformanceHandler.CreateBenchmark)
	kpi.Delete("/benchmarks/:id", authMiddleware.RequirePermission("benchmark:manage"), kpiPerformanceHandler.DeleteBenchmark)
	kpi.Get("/benchmarks/summary", authMiddleware.RequirePermission("benchmark:manage"), kpiPerformanceHandler.ListBenchmarkSummary)

	kpi.Get("/segmentation", authMiddleware.RequirePermission("segment:manage"), kpiPerformanceHandler.ListSegmentation)
	kpi.Post("/segmentation", authMiddleware.RequirePermission("segment:manage"), kpiPerformanceHandler.CreateSegmentation)
	kpi.Delete("/segmentation/:id", authMiddleware.RequirePermission("segment:manage"), kpiPerformanceHandler.DeleteSegmentation)
	kpi.Get("/segmentation/summary", authMiddleware.RequirePermission("segment:manage"), kpiPerformanceHandler.ListSegmentationSummary)

	// ---- KPI CORRECTIVE ACTION ROUTES ----
	kpi.Get("/corrective-actions", authMiddleware.RequirePermission("perf:view"), kpiCorrectiveActionHandler.ListByPerformance)
	kpi.Post("/corrective-actions", authMiddleware.RequirePermission("corrective_action:manage"), kpiCorrectiveActionHandler.Create)
	kpi.Put("/corrective-actions/:id/status", authMiddleware.RequirePermission("corrective_action:manage"), kpiCorrectiveActionHandler.UpdateStatus)

	// ---- KPI DASHBOARD TRENDS & CARDS ----
	kpi.Get("/dashboard/trends", authMiddleware.RequirePermission("kpi:view"), kpiDashboardHandler.GetDashboardTrends)
	kpi.Get("/dashboard/cards", authMiddleware.RequirePermission("kpi:view"), kpiDashboardHandler.GetKpiCardDefinitions)

	// ---- GIS ROUTES ----
	gis := v1.Group("/gis", authMiddleware.Authenticate())
	gis.Post("/identify", gisHandler.Identify)

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
