package config

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	Env                         string // env: APP_ENV ("development" | "staging" | "production"). Default: "production".
	Server                      ServerConfig
	Database                    DatabaseConfig
	Redis                       RedisConfig
	MinIO                       MinIOConfig
	JWT                         JWTConfig
	LDAP                        LDAPConfig
	ADBaseer                    ADBaseerConfig
	Nafath                      NafathConfig
	LoginRateLimit              LoginRateLimitConfig
	FrontendURL                 string // env: FRONTEND_URL — base URL of the frontend app, used in notification links
	SSOPrivateKey               string // env: SSO_RSA_PRIVATE_KEY (PEM, optional — auto-gen if empty)
	SSOIssuerURL                string // env: SSO_ISSUER_URL (e.g. https://automax.example.com — embedded in iss claim)
	SSOFrontendURL              string // env: SSO_FRONTEND_URL (e.g. https://automax.example.com — where /sso-complete lives)
	NafathAPIBaseURL            string // env: NAFATH_API_BASE_URL (e.g. https://nafath.amanathail.gov.sa)
	CountryCode                 string // env: COUNTRY_CODE (e.g. +966, +91)
	Escalation                  EscalationConfig
	ReadyToClose                ReadyToCloseConfig
	SmsFeedback                 SmsFeedbackConfig
	Documenta                   DocumentaConfig
	KpiDocumenta                DocumentaConfig
	// EODocumenta is the third, separate Documenta client: EO record folders.
	EODocumenta DocumentaConfig
	AIQuality                   AIQualityConfig
	Nasaq                       NasaqConfig
	AutoAssign                  AutoAssignConfig
	GoalManagement              GoalManagementConfig
	EOPlatform                  EOPlatformConfig
	License                     LicenseConfig
	Integration                 IntegrationConfig
	FinalCloseWhatsAppFeedback  FinalCloseWhatsAppFeedbackConfig
	Cintrix                     CintrixConfig
	PBX                         PBXConfig
	MOMRA                       MOMRAConfig
	SyncDeptAttributesToUser    bool   // env: SYNC_DEPT_ATTRIBUTES_TO_USER — when true, assigning a department to a user auto-appends the department's locations/classifications to that user. Default: false.
	MaxDescriptionLength        int    // env: MAX_DESCRIPTION_LENGTH — max description length allowed for incident creation for EPM940 clients. Default: 500.
	ClientCode                  string // env: CLIENT_CODE — client identifier, e.g. "EPM940".
	Report                      ReportConfig
	ImageValidation             ImageValidationConfig
	CitizenAttachmentSizeLimit  string // env: CITIZEN_ATTACHMENT_SIZE_LIMIT — max attachment size in bytes for citizen-submitted incidents (EPM940 ENV_CONFIGURATION lookup value). Default: 5242880 (5MB).
	InternalAttachmentSizeLimit string // env: INTERNAL_ATTACHMENT_SIZE_LIMIT — max attachment size in bytes for internally-created incidents (EPM940 ENV_CONFIGURATION lookup value). Default: 10485760 (10MB).
	ChatbotURL                  string // env: CHATBOT_URL — URL of the chatbot (EPM940 ENV_CONFIGURATION lookup value). Default: placeholder — override in .env.
	KpiDictionaryWorkflowCode   string // env: KPI_DICTIONARY_WORKFLOW_CODE — Workflow.Code the KPI Workflow (Draft->Reviewed->Approved->Active->Closed) is looked up by. Default: "kpi_dictionary_workflow" — must match the Code seeded in seed_kpi_dictionary_workflow.go, or nothing will resolve.
	Geo                         GeoConfig
}

// GeoConfig holds settings for geolocation-based queries (e.g. radius search
// on incidents).
type GeoConfig struct {
	// PostGISEnabled switches radius-based incident filtering from an inline
	// haversine WHERE clause to PostGIS's ST_DWithin. Only turn this on after
	// CREATE EXTENSION postgis has been applied via a migration — enabling it
	// without the extension present will make radius-filtered queries fail.
	// env: POSTGIS_ENABLED (default: false)
	PostGISEnabled bool
	// NearbyIncidentRadiusMeters is the default search radius (in meters) for
	// the "Nearby Incidents" feature when the caller doesn't supply its own
	// radius. env: NEARBY_INCIDENT_RADIUS_METERS (default: 500)
	NearbyIncidentRadiusMeters float64
	// RecurrenceIncidentRadiusMeters is the radius (in meters) within which an earlier
	// same-classification incident counts as a recurrence.
	// env: RECURRENCE_INCIDENT_RADIUS_METERS (default: 1000)
	RecurrenceIncidentRadiusMeters float64
}

// ImageValidationConfig holds settings for the standalone image-quality
// validation endpoint (POST /api/v1/images/validate), used by Mobile App and
// Chatbot to reject mostly-black/mostly-white/no-detail photos before an
// incident is submitted.
type ImageValidationConfig struct {
	// MaxSizeBytes caps the accepted upload size. env: IMAGE_VALIDATION_MAX_SIZE_BYTES (default: 5MB)
	MaxSizeBytes int
	// AllowedMimeTypes is the whitelist of accepted content types. env: IMAGE_VALIDATION_ALLOWED_TYPES (comma-separated)
	AllowedMimeTypes []string
	// BlackMeanThreshold: mean grayscale luminance (0-255) at or below which an image is a mostly-black candidate. env: IMAGE_VALIDATION_BLACK_MEAN_THRESHOLD (default: 20)
	BlackMeanThreshold int
	// WhiteMeanThreshold: mean grayscale luminance (0-255) at or above which an image is a mostly-white candidate. env: IMAGE_VALIDATION_WHITE_MEAN_THRESHOLD (default: 235)
	WhiteMeanThreshold int
	// LowDetailStdDevThreshold: per-tile luminance standard deviation at or below which a tile is considered flat/featureless. env: IMAGE_VALIDATION_LOW_DETAIL_STDDEV_THRESHOLD (default: 12)
	LowDetailStdDevThreshold float64
	// BlurVarianceThreshold: per-tile variance-of-Laplacian sharpness score (measured on the image resized to 1600px on its longer side, see image_validation_service.go's analysisMaxDim) at or below which a tile is considered blurry. Calibrated against a synthetic sharp-vs-blurred test pattern at typical phone-camera resolution (4000x3000): mild/moderate blur (box-blur radius 5-20px) scored ~8-133, the sharp original scored ~4495 — re-check against your own real sample set if you see false positives/negatives. env: IMAGE_VALIDATION_BLUR_VARIANCE_THRESHOLD (default: 250)
	BlurVarianceThreshold float64
	// The image is divided into a TileGridSize x TileGridSize grid of regions; each tile is independently classified as black/white/low-detail/blurry using the thresholds above, and the image is rejected once the FRACTION of bad tiles reaches the matching *CoverageFraction below. This is what lets "half the frame is black" get caught even though the whole-image average would look fine.

	// TileGridSize: number of tiles per side (e.g. 8 => 64 tiles). env: IMAGE_VALIDATION_TILE_GRID_SIZE (default: 8)
	TileGridSize int
	// BlackCoverageFraction: fraction of tiles that must be black for the whole image to be rejected as mostly_black. env: IMAGE_VALIDATION_BLACK_COVERAGE_FRACTION (default: 0.5)
	BlackCoverageFraction float64
	// WhiteCoverageFraction: fraction of tiles that must be white for the whole image to be rejected as mostly_white. env: IMAGE_VALIDATION_WHITE_COVERAGE_FRACTION (default: 0.5)
	WhiteCoverageFraction float64
	// LowDetailCoverageFraction: fraction of tiles that must be flat/featureless for the whole image to be rejected as low_detail. env: IMAGE_VALIDATION_LOW_DETAIL_COVERAGE_FRACTION (default: 0.6)
	LowDetailCoverageFraction float64
	// BlurCoverageFraction: fraction of tiles that must be blurry for the whole image to be rejected as blurry. env: IMAGE_VALIDATION_BLUR_COVERAGE_FRACTION (default: 0.6)
	BlurCoverageFraction float64
}

// PBXConfig holds settings for the external PBX used by the extension-assignment
// feature. The extension pool is fetched from BaseURL with "?action=list".
type PBXConfig struct {
	// BaseURL is the PBX endpoint that lists/creates extensions, e.g.
	// https://zkff.automaxsw.com/create_user.php. The extension pool is read from
	// BaseURL + "?action=list"; a single extension from + "&username=<ext>".
	// env: PBX_BASE_URL
	BaseURL string
	// InsecureSkipVerify disables PBX TLS certificate validation. Dev/staging
	// escape hatch only; defaults to false. env: PBX_INSECURE_SKIP_VERIFY
	InsecureSkipVerify bool
}

// FinalCloseWhatsAppFeedbackConfig holds settings for deleting a still-active
// WhatsApp feedback session once the final-close feedback has already been
// submitted via another channel (e.g. the SMS fallback link), so the WhatsApp
// feedback link stops working for the reporter.
type FinalCloseWhatsAppFeedbackConfig struct {
	// SessionBaseURL is the full session endpoint of the WhatsApp feedback API,
	// including the "/session" path segment, e.g.
	// https://unicam.discretal.com/automax3-feedback/session. The mobile number
	// is appended directly to this value to form the DELETE URL.
	// env: FINAL_CLOSE_WHATSAPP_FEEDBACK_SESSION_BASE_URL
	SessionBaseURL string
}

type IntegrationConfig struct {
	SecretsKey string // env: INTEGRATION_SECRETS_KEY (64-char hex = 32 bytes for AES-256-GCM)
}

type LicenseConfig struct {
	EncryptionKey   string // env: LICENSE_ENCRYPTION_KEY (64-char hex = 32 bytes for AES-256)
	GracePeriodDays int    // env: LICENSE_GRACE_PERIOD_DAYS (default: 7)
	Enabled         bool   // env: LICENSE_ENABLED (default: true, set false for dev bypass)
	// DevSeedEnabled forces the dev-license auto-seeder to run even if APP_ENV != "development".
	// env: LICENSE_DEV_SEED (default: false). For CI pipelines that seed their own DB.
	DevSeedEnabled bool
	// DevSeedExpiryDays controls how long the auto-seeded dev license is valid.
	// env: LICENSE_DEV_EXPIRY_DAYS (default: 90). Kept short so developers exercise
	// the expiry and renewal flow rather than running against a never-expiring license.
	DevSeedExpiryDays int
}

// ReportConfig holds settings for the incident PDF/HTML report generator.
type ReportConfig struct {
	// LogoLeftURL is the left-side logo image URL embedded in the report header. env: LOGO_LEFT_URL
	LogoLeftURL string
	// LogoRightURL is the right-side logo image URL embedded in the report header. env: LOGO_RIGHT_URL
	LogoRightURL string
	// ChromeBin is the headless Chrome/Chromium binary used to render the report to PDF. env: CHROME_BIN (default: google-chrome)
	ChromeBin string
	// AppRegion controls the report timezone: "SA" -> Arabia Standard Time (UTC+3), anything else -> IST (UTC+5:30). env: APP_REGION
	AppRegion string
}

// AIQualityConfig holds settings for the AI Quality Monitor.
type AIQualityConfig struct {
	// APIEndpoint is the URL of the external AI quality API. env: AI_QUALITY_API_ENDPOINT
	APIEndpoint string
	// APIKey is the bearer token used when calling the AI quality API. env: AI_QUALITY_API_KEY
	APIKey string
	// CheckIntervalMinutes controls how often the monitor polls (default 10). env: AI_QUALITY_CHECK_INTERVAL_MINUTES
	CheckIntervalMinutes int
	// AppProtocol is the protocol used to construct attachment fallback URLs (http/https). env: APP_PROTOCOL
	AppProtocol string
	// AppHost is the host:port of this server used to construct attachment fallback URLs. env: APP_HOST
	AppHost string
	// AppToken is an internal bearer token used when downloading attachments via the API fallback. env: APP_TOKEN
	AppToken string
}

// NasaqConfig points at the standalone Nasaq Integration Microservice (see
// Nasaq-Integration-Service repo) - this backend never calls Nasaq's own
// APIs directly, only this internal service.
type NasaqConfig struct {
	// BaseURL is the Nasaq Integration Microservice's base URL. env: NASAQ_SERVICE_BASE_URL
	BaseURL string
	// APIKey is sent as the X-Api-Key header on every call. env: NASAQ_SERVICE_API_KEY
	APIKey string
	// TimeoutSeconds bounds each call to the microservice (default 20). env: NASAQ_SERVICE_TIMEOUT_SECONDS
	TimeoutSeconds int
}

// AutoAssignConfig holds settings for the Auto-Assign Monitor.
type AutoAssignConfig struct {
	// StateCode is the workflow state code to scan for unassigned incidents. env: AUTO_ASSIGN_STATE_CODE
	// Leave empty to disable the monitor entirely.
	StateCode string
	// IntervalMinutes controls how often the monitor sweeps (default 5). env: AUTO_ASSIGN_INTERVAL_MINUTES
	IntervalMinutes int
}

type GoalManagementConfig struct {
	Enabled bool // env: GOAL_MANAGEMENT
}

// EOPlatformConfig gates the Executive Office record platform's SEED. The
// tables themselves are created on every boot by migrations.MigrateEOPlatform
// and the HTTP surface is gated by the eo_platform licence; this flag only
// decides whether database.Seed writes the base record type, the smoke types
// and their per-type permission codes.
type EOPlatformConfig struct {
	Enabled bool // env: EO_PLATFORM
	// TimerIntervalSeconds is how often the durable-timer worker polls for due
	// timers (default 30). It is deliberately short next to the five-minute
	// incident sweeps: a timer is only as punctual as the poll that finds it.
	// A zero or unreadable value falls back to the default.
	//
	// It lives here, beside Enabled, rather than being read with a bare
	// os.Getenv at its use site in main.go: the platform's two settings are
	// read the same way, and config.Load is the one place that knows which
	// environment variables this server reads. env: EO_TIMER_INTERVAL_SECONDS
	TimerIntervalSeconds int
	// TimerIntervalUnparsed is EO_TIMER_INTERVAL_SECONDS exactly as the
	// environment held it, and is non-empty ONLY when the variable was set and
	// could not be parsed as an integer. Empty means it was either unset or
	// valid.
	//
	// It exists because getEnvAsInt swallows strconv's error and returns the
	// default, which would make a TYPO completely silent: the fallback is a
	// perfectly ordinary 30, so the use site's "non-positive value" warning
	// never fires and nothing anywhere names the bad string. Carrying the raw
	// value out lets main.go warn and quote it, without config.Load — which
	// logs nothing at all today — becoming a logging package.
	TimerIntervalUnparsed string
}

type DocumentaConfig struct {
	BaseURL       string
	ClientID      string
	ClientSecret  string
	WorkspaceName string
	Enabled       bool
}

// CintrixConfig holds settings for the Cintrix CTI integration (contact-center
// softphone widget). env: CINTRIX_URL, CINTRIX_API_KEY_ID, CINTRIX_API_KEY_SECRET,
// CINTRIX_WEBHOOK_SECRET.
// CINTRIX_URL must be the Cintrix FRONTEND origin (its nginx serves
// cti-widget.js and proxies /api to the backend) — NOT the bare backend port;
// the widget script 404s there and the widget never renders.
// — leaving these unset disables the CTI routes.
// WebhookSecret authenticates inbound call-event webhooks from Cintrix
// (Authorization: Bearer + X-Cintrix-Signature HMAC); leaving it unset
// disables the webhook route (503).
type CintrixConfig struct {
	URL           string
	APIKeyID      string
	APIKeySecret  string
	WebhookSecret string
}

// MOMRAConfig holds settings for the outbound MOMRA CRM integration (Automax acting as
// a client of MOMRA's CRM services — status sync, classification/EE master sync).
// This is the reverse direction from the existing inbound EPM routes in
// epm_incident_handler.go, which is MOMRA/EPM pushing incidents into Automax.
// env: MOMRA_BASE_URL, MOMRA_AUTH_SERVICE, MOMRA_AUTH_KEY, MOMRA_AMANA_ID.
// Leaving BaseURL unset disables outbound MOMRA sync/status calls.
type MOMRAConfig struct {
	// BaseURL is the MOMRA host (scheme + host + port), shared by the token API
	// (BaseURL + /API/Authentication/GetToken) and the CRM services
	// (BaseURL + /api/crm940/...), e.g. https://eservicestest.eamana.gov.sa:450
	BaseURL string
	// AuthService and AuthKey are the GetToken request's "Service" and "Key"
	// credentials issued by the client. Secrets — set via env, never commit.
	AuthService string
	AuthKey     string
	// AmanaID is UpdateStatusV2's "AmanaID" (this Amana's code in MOMRA, e.g. "003").
	AmanaID string
}

type EscalationConfig struct {
	DailyHour    int
	DailyMinute  int
	WeeklyHour   int
	WeeklyMinute int
}

// SmsFeedbackConfig controls the delayed SMS fallback sent when the WhatsApp chatbot
// has not received a response within the configured window after a final-close transition.
type SmsFeedbackConfig struct {
	// DelayMinutes is how long to wait before sending the SMS fallback. env: SMS_FEEDBACK_DELAY_MINUTES (default: 2880)
	DelayMinutes int
}

type ReadyToCloseConfig struct {

	// env: READY_TO_CLOSE_DURATION_OPTIONS default: "1 Day,2 Days,1 Week,2 Weeks,1 Month,3 Months"
	DefaultDurationOptions []string
	// PreExpiryNotificationHours controls how many hours before expiry the warning notification is sent. env: READY_TO_CLOSE_PRE_EXPIRY_HOURS (default: 24)
	PreExpiryNotificationHours int
	// RevertStateCode is the workflow state code incidents are moved back to when they expire in Ready to Close. env: READY_TO_CLOSE_REVERT_STATE_CODE (default: "under_resolution")
	RevertStateCode string
	// StateCode is the workflow state code that activates this feature. env: READY_TO_CLOSE_STATE_CODE (default: "ready_to_close")
	StateCode string
}

type ServerConfig struct {
	Port         string
	Host         string
	AllowOrigins string // env: ALLOW_ORIGINS (comma-separated)
}

type DatabaseConfig struct {
	Host     string
	Port     string
	User     string
	Password string
	DBName   string
	SSLMode  string
}

type RedisConfig struct {
	Host     string
	Port     string
	Password string
	DB       int
}

type MinIOConfig struct {
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	UseSSL          bool
	BucketName      string
}

type JWTConfig struct {
	Secret             string
	ExpireHour         float64
	RefreshExpireHour  float64
	RememberExpireHour float64
}

type LDAPConfig struct {
	Enabled            bool
	URL                string
	BaseDN             string
	BindDN             string
	BindPassword       string
	UserSearchBase     string
	UserSearchFilter   string
	GroupSearchBase    string
	GroupSearchFilter  string
	InsecureSkipVerify bool
}

// ADBaseerConfig holds settings for AD login through the client's AD-Baseer
// HTTPS APIs (GetToken -> ValidateCredentialsOTP -> IsValidOTP). When Enabled,
// POST /api/v1/ldap/login validates credentials through AD-Baseer instead of an
// LDAP bind and requires an OTP step; the LDAP admin endpoints are unaffected.
// env: AD_BASEER_*. Mode "mock" (BaseURL pointing at the ad-baseer-simulator)
// is refused when APP_ENV is production.
type ADBaseerConfig struct {
	Enabled bool
	// Mode is "real" (BaseURL must be https) or "mock" (BaseURL is the
	// ad-baseer-simulator; http allowed, refused in production).
	Mode    string
	BaseURL string
	// Service / Key are GetToken's credentials; ModuleID is
	// ValidateCredentialsOTP's ModuleID. Secrets — set via env, never commit.
	Service  string
	Key      string
	ModuleID string
	// TimeoutSeconds bounds each AD-Baseer HTTP call.
	TimeoutSeconds int
	// OTPTTLSeconds is how long a pending OTP challenge is kept by Automax.
	OTPTTLSeconds int
	// OTPMaxAttempts is how many OTP submissions one challenge allows.
	OTPMaxAttempts int
	// ActiveStatuses are the employeeInfo.status values allowed to log in.
	ActiveStatuses []string
	// InsecureSkipVerify disables TLS verification; refused in production.
	InsecureSkipVerify bool
}

func (c ADBaseerConfig) IsMock() bool {
	return strings.EqualFold(strings.TrimSpace(c.Mode), "mock")
}

// NafathConfig holds settings for National ID login through Nafath, called
// via the client's Amana gateway (GetAccessTokenByIdNumber ->
// NafathSendRequest -> NafathCheckRequest). When Enabled, POST
// /api/v1/auth/nafath/start and /status sign users in only after the person
// approves the request in the Nafath app, and the old National-ID-only
// POST /auth/sso/login is refused. env: NAFATH_* (NAFATH_API_BASE_URL is the
// unrelated old validation-app URL). Mode "mock" (BaseURL pointing at the
// nafath-simulator) is refused when APP_ENV is production.
type NafathConfig struct {
	Enabled bool
	// Mode is "real" (BaseURL must be https) or "mock" (BaseURL is the
	// nafath-simulator; http allowed, refused in production).
	Mode    string
	BaseURL string
	// Service / Key are GetAccessTokenByIdNumber's credentials (the same
	// fixed platform values as AD-Baseer's; defaults to AD_BASEER_SERVICE /
	// AD_BASEER_KEY). Secrets — set via env, never commit.
	Service string
	Key     string
	// AppID is NafathSendRequest's appID; CheckAppID is NafathCheckRequest's
	// (the client's document shows 13 and 17; defaults to AppID).
	AppID      string
	CheckAppID string
	// TimeoutSeconds bounds each gateway HTTP call.
	TimeoutSeconds int
	// PollSeconds is how often NafathCheckRequest runs while the request is
	// waiting (default 10 s). Automax adds no timer of its own: the login
	// lasts until Nafath reports the request COMPLETED, REJECTED or EXPIRED.
	PollSeconds int
	// LogAPI logs every gateway request and response (secrets and personal
	// data masked) as [NAFATH-API] lines, as evidence. Default on.
	LogAPI bool
	// InsecureSkipVerify disables TLS verification; refused in production.
	InsecureSkipVerify bool
}

func (c NafathConfig) IsMock() bool {
	return strings.EqualFold(strings.TrimSpace(c.Mode), "mock")
}

type LoginRateLimitConfig struct {
	Enabled         bool
	MaxAttempts     int
	RateLimitWindow int // in minutes
	BlockDuration   int // in minutes
	BypassForAdmin  bool
}

func Load() *Config {
	eoTimerSeconds, eoTimerUnparsed := eoTimerIntervalFromEnv()
	return &Config{
		Env: getEnv("APP_ENV", "production"),
		Server: ServerConfig{
			Port:         getEnv("SERVER_PORT", "8080"),
			Host:         getEnv("SERVER_HOST", "0.0.0.0"),
			AllowOrigins: getEnv("ALLOW_ORIGINS", "http://localhost:3000,http://localhost:5173,http://localhost:5174"),
			// FrontendURL: getEnv("FRONTEND_URL", "http://localhost:5173"),
		},
		Database: DatabaseConfig{
			Host:     getEnv("DB_HOST", "localhost"),
			Port:     getEnv("DB_PORT", "5432"),
			User:     getEnv("DB_USER", "automax"),
			Password: getEnv("DB_PASSWORD", "automax123"),
			DBName:   getEnv("DB_NAME", "automax"),
			SSLMode:  getEnv("DB_SSLMODE", "disable"),
		},
		Redis: RedisConfig{
			Host:     getEnv("REDIS_HOST", "localhost"),
			Port:     getEnv("REDIS_PORT", "6379"),
			Password: getEnv("REDIS_PASSWORD", ""),
			DB:       getEnvAsInt("REDIS_DB", 0),
		},
		MinIO: MinIOConfig{
			Endpoint:        getEnv("MINIO_ENDPOINT", "localhost:9000"),
			AccessKeyID:     getEnv("MINIO_ACCESS_KEY", "minioadmin"),
			SecretAccessKey: getEnv("MINIO_SECRET_KEY", "minioadmin"),
			UseSSL:          getEnvAsBool("MINIO_USE_SSL", false),
			BucketName:      getEnv("MINIO_BUCKET", "automax"),
		},
		JWT: JWTConfig{
			Secret:             getEnv("JWT_SECRET", "your-super-secret-jwt-key-change-in-production"),
			ExpireHour:         getEnvAsFloat("JWT_EXPIRE_HOUR", 24),
			RefreshExpireHour:  getEnvAsFloat("JWT_REFRESH_EXPIRE_HOUR", 168),
			RememberExpireHour: getEnvAsFloat("JWT_REMEMBER_EXPIRE_HOUR", 720),
		},
		LDAP: LDAPConfig{
			Enabled:            getEnvAsBool("LDAP_ENABLED", false),
			URL:                getEnv("LDAP_URL", "ldap://localhost:389"),
			BaseDN:             getEnv("LDAP_BASE_DN", "dc=example,dc=com"),
			BindDN:             getEnv("LDAP_BIND_DN", ""),
			BindPassword:       getEnv("LDAP_BIND_PASSWORD", ""),
			UserSearchBase:     getEnv("LDAP_USER_SEARCH_BASE", "ou=users,dc=example,dc=com"),
			UserSearchFilter:   getEnv("LDAP_USER_SEARCH_FILTER", "(sAMAccountName={{username}})"),
			GroupSearchBase:    getEnv("LDAP_GROUP_SEARCH_BASE", "ou=groups,dc=example,dc=com"),
			GroupSearchFilter:  getEnv("LDAP_GROUP_SEARCH_FILTER", "(member={{userDN}})"),
			InsecureSkipVerify: getEnvAsBool("LDAP_INSECURE_SKIP_VERIFY", true),
		},
		ADBaseer: ADBaseerConfig{
			Enabled:            getEnvAsBool("AD_BASEER_ENABLED", false),
			Mode:               getEnv("AD_BASEER_MODE", "real"),
			BaseURL:            getEnv("AD_BASEER_BASE_URL", ""),
			Service:            getEnv("AD_BASEER_SERVICE", ""),
			Key:                getEnv("AD_BASEER_KEY", ""),
			ModuleID:           getEnv("AD_BASEER_MODULE_ID", ""),
			TimeoutSeconds:     getEnvAsInt("AD_BASEER_TIMEOUT_SECONDS", 15),
			OTPTTLSeconds:      getEnvAsInt("AD_BASEER_OTP_TTL_SECONDS", 300),
			OTPMaxAttempts:     getEnvAsInt("AD_BASEER_OTP_MAX_ATTEMPTS", 3),
			ActiveStatuses:     getEnvAsStringSlice("AD_BASEER_ACTIVE_STATUSES", []string{"1"}),
			InsecureSkipVerify: getEnvAsBool("AD_BASEER_INSECURE_SKIP_VERIFY", false),
		},
		Nafath: NafathConfig{
			Enabled:            getEnvAsBool("NAFATH_ENABLED", false),
			Mode:               getEnv("NAFATH_MODE", "real"),
			BaseURL:            getEnv("NAFATH_BASE_URL", ""),
			Service:            getEnv("NAFATH_SERVICE", getEnv("AD_BASEER_SERVICE", "")),
			Key:                getEnv("NAFATH_KEY", getEnv("AD_BASEER_KEY", "")),
			AppID:              getEnv("NAFATH_APP_ID", "13"),
			CheckAppID:         getEnv("NAFATH_CHECK_APP_ID", getEnv("NAFATH_APP_ID", "13")),
			TimeoutSeconds:     getEnvAsInt("NAFATH_TIMEOUT_SECONDS", 15),
			PollSeconds:        getEnvAsInt("NAFATH_POLL_SECONDS", 10),
			LogAPI:             getEnvAsBool("NAFATH_LOG_API", true),
			InsecureSkipVerify: getEnvAsBool("NAFATH_INSECURE_SKIP_VERIFY", false),
		},
		LoginRateLimit: LoginRateLimitConfig{
			Enabled:         getEnvAsBool("LOGIN_RATE_LIMIT_ENABLED", true),
			MaxAttempts:     getEnvAsInt("MAX_LOGIN_ATTEMPTS", 5),
			RateLimitWindow: getEnvAsInt("RATE_LIMIT_WINDOW", 5),
			BlockDuration:   getEnvAsInt("BLOCK_DURATION", 15),
			BypassForAdmin:  getEnvAsBool("BYPASS_RATE_LIMIT_FOR_ADMIN", true),
		},
		SSOPrivateKey:    getEnv("SSO_RSA_PRIVATE_KEY", ""),
		SSOIssuerURL:     getEnv("SSO_ISSUER_URL", ""),
		FrontendURL:      getEnv("FRONTEND_URL", ""),
		SSOFrontendURL:   getEnv("SSO_FRONTEND_URL", ""),
		NafathAPIBaseURL: getEnv("NAFATH_API_BASE_URL", ""),
		CountryCode:      getEnv("COUNTRY_CODE", "+966"),
		Escalation: EscalationConfig{
			DailyHour:    getEnvAsInt("ESCALATION_DAILY_HOUR", 18),
			DailyMinute:  getEnvAsInt("ESCALATION_DAILY_MINUTE", 0),
			WeeklyHour:   getEnvAsInt("ESCALATION_WEEKLY_HOUR", 9),
			WeeklyMinute: getEnvAsInt("ESCALATION_WEEKLY_MINUTE", 0),
		},
		Documenta: DocumentaConfig{
			BaseURL:       getEnv("DOCUMENTA_BASE_URL", "http://localhost:8090"),
			ClientID:      getEnv("DOCUMENTA_CLIENT_ID", ""),
			ClientSecret:  getEnv("DOCUMENTA_CLIENT_SECRET", ""),
			WorkspaceName: getEnv("DOCUMENTA_WORKSPACE_NAME", "automax"),
			Enabled:       getEnvAsBool("DOCUMENTA_ENABLED", false),
		},
		// KpiDocumenta is a SEPARATE OAuth client/config from Documenta above —
		// KPI evidence uploads intentionally use their own Documenta client
		// (own credentials, own workspace) rather than sharing Goal
		// Management's, per explicit product decision. Defaults to disabled
		// (stub client, see cmd/server/main.go) until real credentials exist.
		KpiDocumenta: DocumentaConfig{
			BaseURL:       getEnv("KPI_DOCUMENTA_BASE_URL", "https://mydocs.axionic.io"),
			ClientID:      getEnv("KPI_DOCUMENTA_CLIENT_ID", ""),
			ClientSecret:  getEnv("KPI_DOCUMENTA_CLIENT_SECRET", ""),
			WorkspaceName: getEnv("KPI_DOCUMENTA_WORKSPACE_NAME", "automax"),
			Enabled:       getEnvAsBool("KPI_DOCUMENTA_ENABLED", false),
		},
		// EODocumenta is the THIRD Documenta client, for the folder each EO
		// record owns. It is separate from both blocks above for the same
		// reason they are separate from each other: its own credentials and
		// its own workspace, so EO records never land in Goals' or KPI's.
		// Every default is empty/off on purpose — no inherited URL, no
		// inherited workspace — so an unconfigured deployment gets the stub
		// (see cmd/server/main.go) instead of writing into another module's
		// Documenta. Read by cmd/server/main.go.
		EODocumenta: DocumentaConfig{
			BaseURL:       getEnv("EO_DOCUMENTA_BASE_URL", ""),
			ClientID:      getEnv("EO_DOCUMENTA_CLIENT_ID", ""),
			ClientSecret:  getEnv("EO_DOCUMENTA_CLIENT_SECRET", ""),
			WorkspaceName: getEnv("EO_DOCUMENTA_WORKSPACE_NAME", ""),
			Enabled:       EODocumentaEnabled(),
		},
		ReadyToClose: ReadyToCloseConfig{
			DefaultDurationOptions:     getEnvAsStringSlice("READY_TO_CLOSE_DURATION_OPTIONS", []string{"1 Day", "2 Days", "1 Week", "2 Weeks", "1 Month", "3 Months"}),
			PreExpiryNotificationHours: getEnvAsInt("READY_TO_CLOSE_PRE_EXPIRY_HOURS", 24),
			RevertStateCode:            getEnv("READY_TO_CLOSE_REVERT_STATE_CODE", "under_resolution"),
			StateCode:                  getEnv("READY_TO_CLOSE_STATE_CODE", "ready_to_close"),
		},
		AIQuality: AIQualityConfig{
			APIEndpoint:          getEnv("AI_QUALITY_API_ENDPOINT", ""),
			APIKey:               getEnv("AI_QUALITY_API_KEY", ""),
			CheckIntervalMinutes: getEnvAsInt("AI_QUALITY_CHECK_INTERVAL_MINUTES", 10),
			AppProtocol:          getEnv("APP_PROTOCOL", "http"),
			AppHost:              getEnv("APP_HOST", "localhost:8080"),
			AppToken:             getEnv("APP_TOKEN", ""),
		},
		Nasaq: NasaqConfig{
			BaseURL:        getEnv("NASAQ_SERVICE_BASE_URL", ""),
			APIKey:         getEnv("NASAQ_SERVICE_API_KEY", ""),
			TimeoutSeconds: getEnvAsInt("NASAQ_SERVICE_TIMEOUT_SECONDS", 20),
		},
		FinalCloseWhatsAppFeedback: FinalCloseWhatsAppFeedbackConfig{
			SessionBaseURL: getEnv("FINAL_CLOSE_WHATSAPP_FEEDBACK_SESSION_BASE_URL", ""),
		},
		AutoAssign: AutoAssignConfig{
			StateCode:       getEnv("AUTO_ASSIGN_STATE_CODE", ""),
			IntervalMinutes: getEnvAsInt("AUTO_ASSIGN_INTERVAL_MINUTES", 5),
		},
		GoalManagement: GoalManagementConfig{
			Enabled: getEnvAsBool("GOAL_MANAGEMENT", false),
		},
		EOPlatform: EOPlatformConfig{
			Enabled:               getEnvAsBool("EO_PLATFORM", false),
			TimerIntervalSeconds:  eoTimerSeconds,
			TimerIntervalUnparsed: eoTimerUnparsed,
		},
		License: LicenseConfig{
			EncryptionKey:     getEnv("LICENSE_ENCRYPTION_KEY", ""),
			GracePeriodDays:   getEnvAsInt("LICENSE_GRACE_PERIOD_DAYS", 7),
			Enabled:           getEnvAsBool("LICENSE_ENABLED", true),
			DevSeedEnabled:    getEnvAsBool("LICENSE_DEV_SEED", false),
			DevSeedExpiryDays: getEnvAsInt("LICENSE_DEV_EXPIRY_DAYS", 90),
		},
		Integration: IntegrationConfig{
			SecretsKey: getEnv("INTEGRATION_SECRETS_KEY", ""),
		},
		SmsFeedback: SmsFeedbackConfig{
			DelayMinutes: getEnvAsInt("SMS_FEEDBACK_DELAY_MINUTES", 0),
		},
		Cintrix: CintrixConfig{
			URL:           getEnv("CINTRIX_URL", ""),
			APIKeyID:      getEnv("CINTRIX_API_KEY_ID", ""),
			APIKeySecret:  getEnv("CINTRIX_API_KEY_SECRET", ""),
			WebhookSecret: getEnv("CINTRIX_WEBHOOK_SECRET", ""),
		},
		PBX: PBXConfig{
			BaseURL:            getEnv("PBX_BASE_URL", "https://zkff.automaxsw.com/create_user.php"),
			InsecureSkipVerify: getEnvAsBool("PBX_INSECURE_SKIP_VERIFY", false),
		},
		MOMRA: MOMRAConfig{
			BaseURL:     getEnv("MOMRA_BASE_URL", ""),
			AuthService: getEnv("MOMRA_AUTH_SERVICE", ""),
			AuthKey:     getEnv("MOMRA_AUTH_KEY", ""),
			AmanaID:     getEnv("MOMRA_AMANA_ID", "003"),
		},
		SyncDeptAttributesToUser:    getEnvAsBool("SYNC_DEPT_ATTRIBUTES_TO_USER", false),
		MaxDescriptionLength:        getEnvAsInt("MAX_DESCRIPTION_LENGTH", 500),
		ClientCode:                  getEnv("CLIENT_CODE", ""),
		CitizenAttachmentSizeLimit:  getEnv("CITIZEN_ATTACHMENT_SIZE_LIMIT", "5242880"),
		InternalAttachmentSizeLimit: getEnv("INTERNAL_ATTACHMENT_SIZE_LIMIT", "10485760"),
		ChatbotURL:                  getEnv("CHATBOT_URL", "https://chatbot.automax.example.com"),
		KpiDictionaryWorkflowCode:   getEnv("KPI_DICTIONARY_WORKFLOW_CODE", "kpi_dictionary_workflow"),
		Geo: GeoConfig{
			PostGISEnabled:                 getEnvAsBool("POSTGIS_ENABLED", false),
			NearbyIncidentRadiusMeters:     getEnvAsFloat("NEARBY_INCIDENT_RADIUS_METERS", 1000),
			RecurrenceIncidentRadiusMeters: getEnvAsFloat("RECURRENCE_INCIDENT_RADIUS_METERS", 1000),
		},
		Report: ReportConfig{
			LogoLeftURL:  getEnv("LOGO_LEFT_URL", ""),
			LogoRightURL: getEnv("LOGO_RIGHT_URL", ""),
			ChromeBin:    getEnv("CHROME_BIN", "google-chrome"),
			AppRegion:    getEnv("APP_REGION", ""),
		},
		ImageValidation: ImageValidationConfig{
			MaxSizeBytes:              getEnvAsInt("IMAGE_VALIDATION_MAX_SIZE_BYTES", 5*1024*1024),
			AllowedMimeTypes:          getEnvAsStringSlice("IMAGE_VALIDATION_ALLOWED_TYPES", []string{"image/jpeg", "image/png", "image/gif", "image/webp"}),
			BlackMeanThreshold:        getEnvAsInt("IMAGE_VALIDATION_BLACK_MEAN_THRESHOLD", 20),
			WhiteMeanThreshold:        getEnvAsInt("IMAGE_VALIDATION_WHITE_MEAN_THRESHOLD", 235),
			LowDetailStdDevThreshold:  getEnvAsFloat("IMAGE_VALIDATION_LOW_DETAIL_STDDEV_THRESHOLD", 12),
			BlurVarianceThreshold:     getEnvAsFloat("IMAGE_VALIDATION_BLUR_VARIANCE_THRESHOLD", 250),
			TileGridSize:              getEnvAsInt("IMAGE_VALIDATION_TILE_GRID_SIZE", 8),
			BlackCoverageFraction:     getEnvAsFloat("IMAGE_VALIDATION_BLACK_COVERAGE_FRACTION", 0.5),
			WhiteCoverageFraction:     getEnvAsFloat("IMAGE_VALIDATION_WHITE_COVERAGE_FRACTION", 0.5),
			LowDetailCoverageFraction: getEnvAsFloat("IMAGE_VALIDATION_LOW_DETAIL_COVERAGE_FRACTION", 0.6),
			BlurCoverageFraction:      getEnvAsFloat("IMAGE_VALIDATION_BLUR_COVERAGE_FRACTION", 0.6),
		},
	}
}

func getEnv(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}

// EOTimerIntervalDefaultSeconds is the poll interval used when
// EO_TIMER_INTERVAL_SECONDS is unset, unreadable or not positive.
const EOTimerIntervalDefaultSeconds = 30

// eoTimerIntervalFromEnv reads EO_TIMER_INTERVAL_SECONDS and SAYS when it
// could not. getEnvAsInt, which every other numeric setting uses, discards
// strconv's error and returns the default — so `EO_TIMER_INTERVAL_SECONDS=3O`
// (letter O) would quietly become a perfectly ordinary 30 that no warning
// anywhere mentions. unparsed carries the offending string out so the caller
// can quote it; it is empty when the variable is unset or valid.
//
// It returns rather than logs because this package logs nothing: see
// EOPlatformConfig.TimerIntervalUnparsed.
func eoTimerIntervalFromEnv() (seconds int, unparsed string) {
	raw, exists := os.LookupEnv("EO_TIMER_INTERVAL_SECONDS")
	trimmed := strings.TrimSpace(raw)
	// Absent, and present-but-blank, are the same intent: a .env file
	// routinely carries `KEY=` for "not configured", and warning about it
	// would train the operator to ignore the warning that matters.
	if !exists || trimmed == "" {
		return EOTimerIntervalDefaultSeconds, ""
	}
	n, err := strconv.Atoi(trimmed)
	if err != nil {
		return EOTimerIntervalDefaultSeconds, raw
	}
	// A parsed but non-positive value is NOT reported here: it is readable,
	// and the use site already refuses it by name. Reporting it as unparsed
	// would make the two warnings say the wrong thing about each other.
	return n, ""
}

func getEnvAsInt(key string, defaultValue int) int {
	if value, exists := os.LookupEnv(key); exists {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

// EODocumentaEnabled reports whether the Executive Office's Documenta client
// is configured, and is the ONE place EO_DOCUMENTA_ENABLED's name and its
// default are written. Config.EODocumenta.Enabled is this value; it is
// exported because the EO pilot seed's boot-time obligation report must say
// which of the two deployments it is looking at
// (database.eoLogFileFieldsAwaitingDocumenta), and a second reading of the
// variable elsewhere could drift from this one.
//
// It reads the environment on every call, deliberately: nothing is cached at
// init, so a boot report says what the running process was given and a test
// may set the variable with t.Setenv.
func EODocumentaEnabled() bool { return getEnvAsBool("EO_DOCUMENTA_ENABLED", false) }

func getEnvAsBool(key string, defaultValue bool) bool {
	if value, exists := os.LookupEnv(key); exists {
		if boolValue, err := strconv.ParseBool(value); err == nil {
			return boolValue
		}
	}
	return defaultValue
}

func getEnvAsStringSlice(key string, defaultValue []string) []string {
	if value, exists := os.LookupEnv(key); exists && value != "" {
		parts := strings.Split(value, ",")
		result := make([]string, 0, len(parts))
		for _, p := range parts {
			if trimmed := strings.TrimSpace(p); trimmed != "" {
				result = append(result, trimmed)
			}
		}
		if len(result) > 0 {
			return result
		}
	}
	return defaultValue
}

func getEnvAsFloat(key string, defaultValue float64) float64 {
	if value, exists := os.LookupEnv(key); exists {
		if floatValue, err := strconv.ParseFloat(value, 64); err == nil {
			return floatValue
		}
	}
	return defaultValue
}
