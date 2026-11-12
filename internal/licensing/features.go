// Package licensing is the single source of truth for licensable features in Automax.
//
// License feature codes are used in three places:
//  1. Backend middleware — RequireLicensedFeature gates route groups
//  2. Frontend — hides nav items and UI for unlicensed features
//  3. License issuance — dev seeder and CI helpers pass these codes into JWT claims
//
// Adding a new feature means adding one entry to Catalog here, then gating the route
// in cmd/server/main.go using the typed constant. Do NOT introduce string literals.
package licensing

// FeatureCode is a typed constant for license features. Use these everywhere instead
// of string literals so renames fail at compile time.
type FeatureCode string

const (
	FeatureIncidents     FeatureCode = "incidents"
	FeatureDispatch      FeatureCode = "dispatch"
	FeatureComplaints    FeatureCode = "complaints"
	FeatureQueries       FeatureCode = "queries"
	FeatureGoals         FeatureCode = "goals"
	FeatureWorkflows     FeatureCode = "workflows"
	FeatureReports       FeatureCode = "reports"
	FeatureDocuments     FeatureCode = "documents"
	FeatureEscalation    FeatureCode = "escalation"
	FeatureAIQuality     FeatureCode = "ai_quality"
	FeatureSSO           FeatureCode = "sso"
	FeatureCommunication FeatureCode = "communication"
	FeatureCallCentre    FeatureCode = "call_centre"
)

// Feature is the canonical descriptor for a licensable feature.
type Feature struct {
	Code              FeatureCode   `json:"code"`
	Name              string        `json:"name"`
	Description       string        `json:"description"`
	PermissionModules []string      `json:"permission_modules"`     // permission-module codes unlocked by this feature
	Dependencies      []FeatureCode `json:"dependencies,omitempty"` // other features that must be licensed too
	TierMinimum       string        `json:"tier_minimum,omitempty"` // e.g. "standard", "professional", "enterprise"
}

// Catalog is the canonical list of features. Keep sorted by typical license tier / importance
// so the UI renders a sensible default order.
var Catalog = []Feature{
	{
		Code:              FeatureIncidents,
		Name:              "Incidents",
		Description:       "Incident tracking with SLAs, workflows, comments, bulk operations, and record-type variants (including requests).",
		PermissionModules: []string{"incidents", "requests"},
		TierMinimum:       "standard",
	},
	{
		Code:              FeatureDispatch,
		Name:              "Dispatch",
		Description:       "Units, unit types, and fire stations used to dispatch resources to incidents.",
		PermissionModules: []string{"dispatch"},
		TierMinimum:       "standard",
	},
	{
		Code:              FeatureComplaints,
		Name:              "Complaints",
		Description:       "Complaint intake, workflow, assignment and SLA tracking.",
		PermissionModules: []string{"complaints"},
		TierMinimum:       "standard",
	},
	{
		Code:              FeatureQueries,
		Name:              "Queries",
		Description:       "Customer query tracking and response workflow.",
		PermissionModules: []string{"queries"},
		TierMinimum:       "standard",
	},
	{
		Code:              FeatureGoals,
		Name:              "Goals & OKR",
		Description:       "Goal management, metrics, evidence, performance reviews, OKR alignment, templates, and analytics.",
		PermissionModules: []string{"goals"},
		TierMinimum:       "professional",
	},
	{
		Code:        FeatureDocuments,
		Name:        "Document Management",
		Description: "File storage, versioning, comments, and tagging via Documenta DMS integration.",
		// documents routes currently reuse goals:* permissions (see cmd/server/main.go).
		// A follow-up should introduce dedicated documents:view/update permissions and
		// list them here. Until then this is intentionally empty.
		PermissionModules: []string{},
		Dependencies:      []FeatureCode{FeatureGoals},
		TierMinimum:       "professional",
	},
	{
		Code:              FeatureWorkflows,
		Name:              "Workflow Designer",
		Description:       "Visual workflow designer with custom states, transitions, and approval rules.",
		PermissionModules: []string{"workflows"},
		TierMinimum:       "professional",
	},
	{
		Code:              FeatureReports,
		Name:              "Reports",
		Description:       "Custom report builder with templates, scheduling, and export.",
		PermissionModules: []string{"reports"},
		TierMinimum:       "professional",
	},
	{
		Code:              FeatureEscalation,
		Name:              "Escalation Engine",
		Description:       "Rule-based escalation policies with configurable groups and notifications.",
		PermissionModules: []string{"escalation-groups"},
		TierMinimum:       "enterprise",
	},
	{
		Code:              FeatureAIQuality,
		Name:              "AI Quality Audit",
		Description:       "AI-powered quality scoring and feedback for incidents and interactions.",
		PermissionModules: []string{},
		Dependencies:      []FeatureCode{FeatureIncidents},
		TierMinimum:       "enterprise",
	},
	{
		Code:              FeatureSSO,
		Name:              "SSO / LDAP",
		Description:       "Single sign-on via SAML/OIDC and LDAP user sync.",
		PermissionModules: []string{},
		TierMinimum:       "enterprise",
	},
	{
		Code:              FeatureCommunication,
		Name:              "Communication Center",
		Description:       "Templated notifications over email, SMS, and in-app channels.",
		PermissionModules: []string{"templates", "notifications"},
		TierMinimum:       "standard",
	},
	{
		Code:              FeatureCallCentre,
		Name:              "Call Centre",
		Description:       "SIP call handling, call logs, caller sentiment analysis.",
		PermissionModules: []string{"call-logs", "caller-sentiment"},
		TierMinimum:       "enterprise",
	},
}

// AllCodes returns every feature code — used by the dev seeder to issue full-scope licenses.
func AllCodes() []string {
	codes := make([]string, len(Catalog))
	for i, f := range Catalog {
		codes[i] = string(f.Code)
	}
	return codes
}

// ModulesForFeature returns the permission modules gated by a feature. Empty slice if
// the feature exists but gates no permission modules (e.g., sso, ai_quality).
func ModulesForFeature(code FeatureCode) []string {
	for _, f := range Catalog {
		if f.Code == code {
			return f.PermissionModules
		}
	}
	return nil
}

// FeatureForModule returns the feature code that gates a permission module, or nil if
// the module is foundational (e.g., users, roles, departments) and always available.
func FeatureForModule(module string) *FeatureCode {
	for _, f := range Catalog {
		for _, m := range f.PermissionModules {
			if m == module {
				code := f.Code
				return &code
			}
		}
	}
	return nil
}

// Find returns the full Feature descriptor by code, or nil if not found.
func Find(code FeatureCode) *Feature {
	for i := range Catalog {
		if Catalog[i].Code == code {
			return &Catalog[i]
		}
	}
	return nil
}
