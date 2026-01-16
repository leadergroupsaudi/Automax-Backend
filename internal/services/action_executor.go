package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/google/uuid"
)

// ActionExecutor handles the execution of transition actions
type ActionExecutor interface {
	ExecuteActions(ctx context.Context, incident *models.Incident, transition *models.WorkflowTransition, performedBy *models.User) error
	ExecuteAction(ctx context.Context, action *models.TransitionAction, incident *models.Incident, transition *models.WorkflowTransition, performedBy *models.User) error
}

type actionExecutor struct {
	incidentRepo repository.IncidentRepository
	userRepo     repository.UserRepository
	httpClient   *http.Client
}

// NewActionExecutor creates a new action executor
func NewActionExecutor(incidentRepo repository.IncidentRepository, userRepo repository.UserRepository) ActionExecutor {
	return &actionExecutor{
		incidentRepo: incidentRepo,
		userRepo:     userRepo,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// ExecuteActions executes all actions for a transition
func (e *actionExecutor) ExecuteActions(ctx context.Context, incident *models.Incident, transition *models.WorkflowTransition, performedBy *models.User) error {
	if transition.Actions == nil || len(transition.Actions) == 0 {
		return nil
	}

	// Sort actions by execution order
	actions := make([]models.TransitionAction, len(transition.Actions))
	copy(actions, transition.Actions)

	for i := 0; i < len(actions)-1; i++ {
		for j := i + 1; j < len(actions); j++ {
			if actions[i].ExecutionOrder > actions[j].ExecutionOrder {
				actions[i], actions[j] = actions[j], actions[i]
			}
		}
	}

	for _, action := range actions {
		if !action.IsActive {
			continue
		}

		if action.IsAsync {
			// Execute asynchronously
			go func(act models.TransitionAction) {
				if err := e.ExecuteAction(context.Background(), &act, incident, transition, performedBy); err != nil {
					log.Printf("Async action execution failed: %v", err)
				}
			}(action)
		} else {
			// Execute synchronously
			if err := e.ExecuteAction(ctx, &action, incident, transition, performedBy); err != nil {
				log.Printf("Action execution failed: %v", err)
				// Continue with other actions even if one fails
			}
		}
	}

	return nil
}

// ExecuteAction executes a single action
func (e *actionExecutor) ExecuteAction(ctx context.Context, action *models.TransitionAction, incident *models.Incident, transition *models.WorkflowTransition, performedBy *models.User) error {
	switch action.ActionType {
	case "notification":
		return e.executeNotification(ctx, action, incident, transition, performedBy)
	case "email":
		return e.executeEmail(ctx, action, incident, transition, performedBy)
	case "webhook":
		return e.executeWebhook(ctx, action, incident, transition, performedBy)
	case "field_update":
		return e.executeFieldUpdate(ctx, action, incident)
	default:
		return fmt.Errorf("unknown action type: %s", action.ActionType)
	}
}

// NotificationConfig represents the configuration for a notification action
type NotificationConfig struct {
	Recipients []string `json:"recipients"` // "assignee", "reporter", "role:admin", "user:uuid"
	Title      string   `json:"title"`
	Message    string   `json:"message"`
}

// executeNotification sends in-app notifications
func (e *actionExecutor) executeNotification(ctx context.Context, action *models.TransitionAction, incident *models.Incident, transition *models.WorkflowTransition, performedBy *models.User) error {
	var config NotificationConfig
	if err := json.Unmarshal([]byte(action.Config), &config); err != nil {
		return fmt.Errorf("invalid notification config: %w", err)
	}

	// Resolve recipients
	recipientIDs := e.resolveRecipients(ctx, config.Recipients, incident)

	// Replace placeholders in title and message
	title := e.replacePlaceholders(config.Title, incident, transition, performedBy)
	message := e.replacePlaceholders(config.Message, incident, transition, performedBy)

	// Log the notification (in a real system, this would create notification records)
	log.Printf("Notification: To=%v, Title=%s, Message=%s", recipientIDs, title, message)

	// TODO: Create actual notification records in database
	// For now, just log it
	return nil
}

// EmailConfig represents the configuration for an email action
type EmailConfig struct {
	Recipients []string `json:"recipients"` // Same as notification
	Subject    string   `json:"subject"`
	Body       string   `json:"body"`
	IsHTML     bool     `json:"is_html"`
}

// executeEmail sends email notifications
func (e *actionExecutor) executeEmail(ctx context.Context, action *models.TransitionAction, incident *models.Incident, transition *models.WorkflowTransition, performedBy *models.User) error {
	var config EmailConfig
	if err := json.Unmarshal([]byte(action.Config), &config); err != nil {
		return fmt.Errorf("invalid email config: %w", err)
	}

	// Resolve recipient emails
	recipientEmails := e.resolveRecipientEmails(ctx, config.Recipients, incident)

	// Replace placeholders
	subject := e.replacePlaceholders(config.Subject, incident, transition, performedBy)
	body := e.replacePlaceholders(config.Body, incident, transition, performedBy)

	// Log the email (in a real system, this would send actual emails)
	log.Printf("Email: To=%v, Subject=%s, Body=%s", recipientEmails, subject, body)

	// TODO: Integrate with email service (SMTP, SendGrid, etc.)
	// For now, just log it
	return nil
}

// WebhookConfig represents the configuration for a webhook action
type WebhookConfig struct {
	URL     string            `json:"url"`
	Method  string            `json:"method"` // GET, POST, PUT
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"` // JSON template
}

// executeWebhook calls an external webhook
func (e *actionExecutor) executeWebhook(ctx context.Context, action *models.TransitionAction, incident *models.Incident, transition *models.WorkflowTransition, performedBy *models.User) error {
	var config WebhookConfig
	if err := json.Unmarshal([]byte(action.Config), &config); err != nil {
		return fmt.Errorf("invalid webhook config: %w", err)
	}

	if config.Method == "" {
		config.Method = "POST"
	}

	// Replace placeholders in body
	body := e.replacePlaceholders(config.Body, incident, transition, performedBy)

	// Create request
	req, err := http.NewRequestWithContext(ctx, config.Method, config.URL, bytes.NewBufferString(body))
	if err != nil {
		return fmt.Errorf("failed to create webhook request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	for key, value := range config.Headers {
		req.Header.Set(key, value)
	}

	// Execute request
	resp, err := e.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("webhook request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook returned error status: %d", resp.StatusCode)
	}

	log.Printf("Webhook executed: URL=%s, Status=%d", config.URL, resp.StatusCode)
	return nil
}

// FieldUpdateConfig represents the configuration for a field update action
type FieldUpdateConfig struct {
	Field string      `json:"field"` // priority, severity, assignee_id, etc.
	Value interface{} `json:"value"`
}

// executeFieldUpdate updates incident fields
func (e *actionExecutor) executeFieldUpdate(ctx context.Context, action *models.TransitionAction, incident *models.Incident) error {
	var config FieldUpdateConfig
	if err := json.Unmarshal([]byte(action.Config), &config); err != nil {
		return fmt.Errorf("invalid field update config: %w", err)
	}

	// Update the field based on config
	updates := make(map[string]interface{})

	switch config.Field {
	case "priority":
		if v, ok := config.Value.(float64); ok {
			updates["priority"] = int(v)
		}
	case "severity":
		if v, ok := config.Value.(float64); ok {
			updates["severity"] = int(v)
		}
	case "assignee_id":
		if v, ok := config.Value.(string); ok {
			if v == "" || v == "null" {
				updates["assignee_id"] = nil
			} else {
				if uid, err := uuid.Parse(v); err == nil {
					updates["assignee_id"] = uid
				}
			}
		}
	case "department_id":
		if v, ok := config.Value.(string); ok {
			if v == "" || v == "null" {
				updates["department_id"] = nil
			} else {
				if uid, err := uuid.Parse(v); err == nil {
					updates["department_id"] = uid
				}
			}
		}
	default:
		return fmt.Errorf("unsupported field for update: %s", config.Field)
	}

	if len(updates) > 0 {
		if err := e.incidentRepo.UpdateFields(ctx, incident.ID, updates); err != nil {
			return fmt.Errorf("failed to update field: %w", err)
		}
		log.Printf("Field updated: Incident=%s, Field=%s, Value=%v", incident.IncidentNumber, config.Field, config.Value)
	}

	return nil
}

// resolveRecipients resolves recipient identifiers to user IDs
func (e *actionExecutor) resolveRecipients(ctx context.Context, recipients []string, incident *models.Incident) []uuid.UUID {
	var userIDs []uuid.UUID
	seen := make(map[uuid.UUID]bool)

	for _, recipient := range recipients {
		switch {
		case recipient == "assignee":
			if incident.AssigneeID != nil && !seen[*incident.AssigneeID] {
				userIDs = append(userIDs, *incident.AssigneeID)
				seen[*incident.AssigneeID] = true
			}
		case recipient == "reporter":
			if incident.ReporterID != nil && !seen[*incident.ReporterID] {
				userIDs = append(userIDs, *incident.ReporterID)
				seen[*incident.ReporterID] = true
			}
		case strings.HasPrefix(recipient, "user:"):
			if uid, err := uuid.Parse(strings.TrimPrefix(recipient, "user:")); err == nil && !seen[uid] {
				userIDs = append(userIDs, uid)
				seen[uid] = true
			}
		case strings.HasPrefix(recipient, "role:"):
			// TODO: Fetch users with specific role
			roleName := strings.TrimPrefix(recipient, "role:")
			log.Printf("Would notify users with role: %s", roleName)
		}
	}

	return userIDs
}

// resolveRecipientEmails resolves recipient identifiers to email addresses
func (e *actionExecutor) resolveRecipientEmails(ctx context.Context, recipients []string, incident *models.Incident) []string {
	var emails []string
	seen := make(map[string]bool)

	for _, recipient := range recipients {
		switch {
		case recipient == "assignee":
			if incident.Assignee != nil && incident.Assignee.Email != "" && !seen[incident.Assignee.Email] {
				emails = append(emails, incident.Assignee.Email)
				seen[incident.Assignee.Email] = true
			}
		case recipient == "reporter":
			if incident.Reporter != nil && incident.Reporter.Email != "" && !seen[incident.Reporter.Email] {
				emails = append(emails, incident.Reporter.Email)
				seen[incident.Reporter.Email] = true
			} else if incident.ReporterEmail != "" && !seen[incident.ReporterEmail] {
				emails = append(emails, incident.ReporterEmail)
				seen[incident.ReporterEmail] = true
			}
		case strings.HasPrefix(recipient, "email:"):
			email := strings.TrimPrefix(recipient, "email:")
			if !seen[email] {
				emails = append(emails, email)
				seen[email] = true
			}
		case strings.HasPrefix(recipient, "user:"):
			if uid, err := uuid.Parse(strings.TrimPrefix(recipient, "user:")); err == nil {
				user, err := e.userRepo.FindByID(ctx, uid)
				if err == nil && user.Email != "" && !seen[user.Email] {
					emails = append(emails, user.Email)
					seen[user.Email] = true
				}
			}
		}
	}

	return emails
}

// replacePlaceholders replaces template placeholders with actual values
func (e *actionExecutor) replacePlaceholders(template string, incident *models.Incident, transition *models.WorkflowTransition, performedBy *models.User) string {
	replacements := map[string]string{
		"{{incident_number}}": incident.IncidentNumber,
		"{{incident_title}}":  incident.Title,
		"{{incident_id}}":     incident.ID.String(),
		"{{priority}}":        fmt.Sprintf("%d", incident.Priority),
		"{{severity}}":        fmt.Sprintf("%d", incident.Severity),
	}

	if transition != nil {
		replacements["{{transition_name}}"] = transition.Name
		if transition.FromState != nil {
			replacements["{{from_state}}"] = transition.FromState.Name
		}
		if transition.ToState != nil {
			replacements["{{to_state}}"] = transition.ToState.Name
		}
	}

	if performedBy != nil {
		replacements["{{performed_by}}"] = performedBy.Username
		if performedBy.FirstName != "" {
			replacements["{{performed_by}}"] = performedBy.FirstName + " " + performedBy.LastName
		}
	}

	if incident.Assignee != nil {
		replacements["{{assignee}}"] = incident.Assignee.Username
		if incident.Assignee.FirstName != "" {
			replacements["{{assignee}}"] = incident.Assignee.FirstName + " " + incident.Assignee.LastName
		}
	} else {
		replacements["{{assignee}}"] = "Unassigned"
	}

	if incident.CurrentState != nil {
		replacements["{{current_state}}"] = incident.CurrentState.Name
	}

	result := template
	for placeholder, value := range replacements {
		result = strings.ReplaceAll(result, placeholder, value)
	}

	return result
}
