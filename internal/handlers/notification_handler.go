package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/internal/services"
	"github.com/automax/backend/internal/storage"
	"github.com/automax/backend/pkg/constants"
	"github.com/automax/backend/pkg/i18n"
	"github.com/automax/backend/pkg/utils"
	"github.com/automax/backend/pkg/validation"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type NotificationHandler struct {
	service          *services.NotificationService
	storage          *storage.MinIOStorage
	userRepo         repository.UserRepository
	incidentRepo     repository.IncidentRepository
	actionLogService services.ActionLogService
}

func NewNotificationHandler(
	service *services.NotificationService,
	storage *storage.MinIOStorage,
	userRepo repository.UserRepository,
	incidentRepo repository.IncidentRepository,
	actionLogService services.ActionLogService,
) *NotificationHandler {
	return &NotificationHandler{
		service:          service,
		storage:          storage,
		userRepo:         userRepo,
		incidentRepo:     incidentRepo,
		actionLogService: actionLogService,
	}
}

// canViewIncident checks whether the user has visibility into the given incident,
// mirroring the classification/location scoping used in IncidentHandler.ListIncidents.
func (h *NotificationHandler) canViewIncident(ctx context.Context, userID uuid.UUID, incidentID uuid.UUID) (bool, error) {
	user, err := h.userRepo.FindByIDWithRelations(ctx, userID)
	if err != nil || user == nil {
		return false, err
	}
	if user.IsSuperAdmin {
		return true, nil
	}

	incident, err := h.incidentRepo.FindByID(ctx, incidentID)
	if err != nil || incident == nil {
		return false, err
	}

	classOK := incident.ClassificationID == nil
	for _, cls := range user.Classifications {
		if incident.ClassificationID != nil && cls.ID == *incident.ClassificationID {
			classOK = true
			break
		}
	}

	locOK := incident.LocationID == nil
	for _, loc := range user.Locations {
		if incident.LocationID != nil && loc.ID == *incident.LocationID {
			locOK = true
			break
		}
	}

	return classOK && locOK, nil
}

// SendGridInboundWebhook handles incoming emails from SendGrid Inbound Parse
// POST /api/v1/webhooks/sendgrid/inbound
func (h *NotificationHandler) SendGridInboundWebhook(c *fiber.Ctx) error {
	// Parse multipart form data
	form, err := c.MultipartForm()
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Failed to parse multipart form")
	}

	// Extract email fields from SendGrid webhook
	from := c.FormValue("from")
	to := c.FormValue("to")
	subject := c.FormValue("subject")
	textBody := c.FormValue("text")
	htmlBody := c.FormValue("html")
	cc := c.FormValue("cc")

	// Parse headers for additional info (optional)
	headersJSON := c.FormValue("headers")

	if from == "" || to == "" {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "missing_required_from_to"))
	}

	// Parse recipients (TO field can be comma-separated)
	toRecipients := parseEmailAddresses(to)
	ccRecipients := parseEmailAddresses(cc)

	// Handle attachments
	var attachments []models.AttachmentData
	if files, ok := form.File["attachment"]; ok {
		for _, fileHeader := range files {
			file, err := fileHeader.Open()
			if err != nil {
				log.Printf("Failed to open attachment %s: %v", fileHeader.Filename, err)
				continue // Skip failed attachments
			}
			defer file.Close()

			data, err := io.ReadAll(file)
			if err != nil {
				log.Printf("Failed to read attachment %s: %v", fileHeader.Filename, err)
				continue
			}

			attachments = append(attachments, models.AttachmentData{
				Filename:    fileHeader.Filename,
				ContentType: fileHeader.Header.Get("Content-Type"),
				Data:        data,
			})
		}
	}

	// Try to find the recipient user in the system
	// You'll need to implement user lookup by email
	var receivedBy *uuid.UUID
	// TODO: Look up user by email address
	// receivedBy = findUserByEmail(toRecipients[0])

	// Create recipient info array
	var recipients models.RecipientArray
	for _, email := range toRecipients {
		recipients = append(recipients, models.RecipientInfo{
			Channel: email,
			Type:    "to",
			Status:  "received",
		})
	}

	// Save attachments to MinIO and create attachment info
	var attachmentInfo models.AttachmentArray
	for _, att := range attachments {
		// Save attachment to MinIO storage
		objectName := ""
		if h.storage != nil && len(att.Data) > 0 {
			// Create a unique folder for this notification's attachments
			folder := fmt.Sprintf("notifications/%s", uuid.New().String())

			// Upload to MinIO
			uploadedPath, err := h.storage.UploadBytes(
				c.UserContext(),
				att.Data,
				att.Filename,
				att.ContentType,
				folder,
			)
			if err != nil {
				log.Printf("Failed to upload attachment %s to MinIO: %v", att.Filename, err)

			} else {
				objectName = uploadedPath
			}
		}

		attachmentInfo = append(attachmentInfo, models.AttachmentInfo{
			ID:          uuid.New().String(),
			Filename:    att.Filename,
			ContentType: att.ContentType,
			Size:        int64(len(att.Data)),
			StoragePath: objectName, // Store MinIO object path
		})
	}

	// Create inbound notification log
	now := time.Now()
	inboundLog := &models.NotificationLog{
		ID:          uuid.New(),
		Channel:     "email",
		Direction:   models.DirectionInbound,
		Category:    models.CategoryInbox,
		From:        from,
		Recipients:  recipients,
		CC:          ccRecipients,
		Subject:     subject,
		Body:        textBody,
		BodyHTML:    htmlBody,
		Status:      "received",
		Provider:    "sendgrid",
		IsRead:      false,
		IsStarred:   false,
		Attachments: attachmentInfo,
		ReceivedBy:  receivedBy,
		SentAt:      &now,
		CreatedAt:   now,
		UpdatedAt:   &now,
	}

	// Save to database using the service
	if err := h.service.SaveInboundNotification(c.UserContext(), inboundLog); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, "Failed to save inbound email: "+err.Error())
	}

	// Log for debugging (optional)
	log.Printf("Received inbound email from %s to %s with subject: %s (Headers: %s)", from, to, subject, headersJSON)

	// Return 200 OK to SendGrid
	return c.JSON(fiber.Map{
		"success": true,
		"message": "Email received successfully",
		"id":      inboundLog.ID.String(),
	})
}

// parseEmailAddresses splits comma-separated email addresses
func parseEmailAddresses(emails string) []string {
	if emails == "" {
		return []string{}
	}

	result := []string{}
	parts := strings.Split(emails, ",")
	for _, part := range parts {
		email := strings.TrimSpace(part)
		if email != "" {
			result = append(result, email)
		}
	}
	return result
}

type SendNotificationRequest struct {
	Channel      string   `json:"channel"`
	TemplateCode *string  `json:"templateCode,omitempty"`
	Language     string   `json:"language"`
	To           []string `json:"to"`
	CC           []string `json:"cc,omitempty"`
	BCC          []string `json:"bcc,omitempty"`

	Subject   string            `json:"subject,omitempty"`
	Body      string            `json:"body,omitempty"`
	Variables map[string]string `json:"variables,omitempty"`
}

type SendNotificationResponse struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Provider string `json:"provider"`
}

// Send handles POST /api/v1/notifications/send with multipart/form-data support
func (h *NotificationHandler) Send(c *fiber.Ctx) error {
	// Get user ID from context
	var sentBy *uuid.UUID
	if userID, ok := c.Locals(constants.ContextKeys.UserID).(uuid.UUID); ok {
		sentBy = &userID
	}

	// Parse multipart form
	form, err := c.MultipartForm()
	if err != nil {
		// Try JSON fallback
		var req SendNotificationRequest
		if jsonErr := c.BodyParser(&req); jsonErr != nil {
			return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid request format")
		}

		// Send without attachments
		result, err := h.service.SendNotification(c.UserContext(), req.Channel, req.TemplateCode, req.Language, req.To, req.CC, req.BCC, req.Subject, req.Body, req.Variables, nil, sentBy, nil)
		if err != nil {
			return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
		}

		res := SendNotificationResponse{
			ID:       result.SentLog.ID.String(),
			Status:   result.SentLog.Status,
			Provider: result.SentLog.Provider,
		}

		return c.Status(fiber.StatusOK).JSON(fiber.Map{
			"success": true,
			"data":    res,
		})
	}

	// Extract form fields
	channel := c.FormValue("channel")
	templateCode := c.FormValue("templateCode")
	language := c.FormValue("language", "en")
	to := form.Value["to"]
	cc := form.Value["cc"]
	bcc := form.Value["bcc"]
	subject := c.FormValue("subject")
	body := c.FormValue("body")

	// Parse variables if provided (expects a JSON object string, e.g. {"key":"value"})
	variables := make(map[string]string)
	if varsStr := c.FormValue("variables"); varsStr != "" {
		if err := json.Unmarshal([]byte(varsStr), &variables); err != nil {
			log.Printf("[NotificationHandler] Failed to parse variables JSON from form: %v — variables will be empty", err)
		} else {
			log.Printf("[NotificationHandler] Parsed %d variable(s) from multipart form", len(variables))
		}
	}

	// Handle attachments
	var attachments []models.AttachmentData
	var attachmentURLs []models.AttachmentInfo // Track uploaded attachments with URLs
	if files, ok := form.File["attachments"]; ok {
		// Create a unique folder for this notification's attachments
		folder := fmt.Sprintf("notifications/%s", uuid.New().String())

		for _, fileHeader := range files {
			file, err := fileHeader.Open()
			if err != nil {
				return utils.ErrorResponse(c, fiber.StatusBadRequest, "Failed to read attachment: "+fileHeader.Filename)
			}
			defer file.Close()

			data, err := io.ReadAll(file)
			if err != nil {
				return utils.ErrorResponse(c, fiber.StatusBadRequest, "Failed to read attachment data: "+fileHeader.Filename)
			}

			contentType := fileHeader.Header.Get("Content-Type")
			if contentType == "" {
				contentType = "application/octet-stream"
			}

			// Upload to MinIO
			objectName := ""
			if h.storage != nil {
				uploadedPath, err := h.storage.UploadBytes(
					c.UserContext(),
					data,
					fileHeader.Filename,
					contentType,
					folder,
				)
				if err != nil {
					log.Printf("Failed to upload attachment %s to MinIO: %v", fileHeader.Filename, err)
				} else {
					objectName = uploadedPath
				}
			}

			attachments = append(attachments, models.AttachmentData{
				Filename:    fileHeader.Filename,
				ContentType: contentType,
				Data:        data,
			})

			attachmentURLs = append(attachmentURLs, models.AttachmentInfo{
				ID:          uuid.New().String(),
				Filename:    fileHeader.Filename,
				ContentType: contentType,
				Size:        int64(len(data)),
				StoragePath: objectName,
			})
		}
	}

	var templateCodePtr *string
	if templateCode != "" {
		templateCodePtr = &templateCode
	}

	// Send notification with attachments
	result, err := h.service.SendNotification(c.UserContext(), channel, templateCodePtr, language, to, cc, bcc, subject, body, variables, attachments, sentBy, nil)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	// Update attachment URLs if attachments were uploaded
	// Update both sent record and all inbox records
	if len(attachmentURLs) > 0 {
		// Update sent record
		if err := h.service.UpdateAttachmentURLs(c.UserContext(), result.SentLog.ID, attachmentURLs); err != nil {
			log.Printf("Warning: Failed to update attachment URLs for sent notification %s: %v", result.SentLog.ID, err)
		}

		// Update all inbox records
		for _, inboxID := range result.InboxLogIDs {
			if err := h.service.UpdateAttachmentURLs(c.UserContext(), inboxID, attachmentURLs); err != nil {
				log.Printf("Warning: Failed to update attachment URLs for inbox notification %s: %v", inboxID, err)
			}
		}
	}

	res := SendNotificationResponse{
		ID:       result.SentLog.ID.String(),
		Status:   result.SentLog.Status,
		Provider: result.SentLog.Provider,
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"data":    res,
	})
}

// GetStats handles GET /api/v1/notifications/stats?user_id=<uuid>&channel=<channel>
func (h *NotificationHandler) GetStats(c *fiber.Ctx) error {
	v := strings.TrimSpace(c.Query("user_id"))
	if v == "" {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "user_id_required"))
	}
	userID, err := uuid.Parse(v)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_user_id_uuid"))
	}

	channel := strings.TrimSpace(c.Query("channel"))

	stats, err := h.service.GetNotificationStatsByUser(c.Context(), channel, userID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, i18n.T(c.UserContext(), "failed_to_fetch_notif_stats"))
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"data":    stats,
	})
}

// List handles GET /api/v1/notifications with search and filters
func (h *NotificationHandler) List(c *fiber.Ctx) error {
	userID, ok := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)
	if !ok {
		return utils.ErrorResponse(c, fiber.StatusUnauthorized, i18n.T(c.UserContext(), "user_not_authenticated"))
	}
	log.Printf("[Notifications List] User %s requesting notifications", userID.String())

	filter := &models.NotificationLogFilter{
		Page:   1,
		Limit:  20,
		UserID: &userID, // Automatically filter by logged-in user
	}

	// Parse query parameters
	if err := c.QueryParser(filter); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_query_parameters"))
	}

	if err := validation.ValidateStruct(c.UserContext(), filter); len(err) != 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"errors":  err,
		})
	}

	if filter.Page < 1 {
		filter.Page = 1
	}

	if filter.Limit < 1 || filter.Limit > 100 {
		filter.Limit = 20
	}

	if filter.IncidentID != nil {
		// Incident communication history request: this is not "my inbox" any more,
		// so drop the personal sent_by/received_by scoping and instead require that
		filter.UserID = nil
		allowed, err := h.canViewIncident(c.UserContext(), userID, *filter.IncidentID)
		if err != nil {
			return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
		}
		if !allowed {
			return utils.ErrorResponse(c, fiber.StatusForbidden, "Insufficient permissions")
		}
	}

	if endDate := c.Query("end_date"); endDate != "" {
		if t, err := time.Parse("2006-01-02", endDate); err == nil {
			// Set to end of day
			t = t.Add(24*time.Hour - time.Second)
			filter.EndDate = &t
		}
	}

	notifications, total, err := h.service.ListNotifications(c.UserContext(), filter)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	applyAcceptLanguage(c.Get("Accept-Language"), notifications)

	totalPages := (int(total) + filter.Limit - 1) / filter.Limit

	return c.JSON(fiber.Map{
		"success":     true,
		"data":        notifications,
		"total_items": total,
		"total_pages": totalPages,
		"page":        filter.Page,
		"limit":       filter.Limit,
	})
}

// ResendNotification handles POST /api/v1/admin/notification-monitoring/:id/resend
// — manually re-sends a failed/undeliverable/expired notification using its
// stored subject/body/recipients, and records the action in the audit log.
// This is always an explicit user action; there is no automatic retry.
func (h *NotificationHandler) ResendNotification(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_notification_id"))
	}

	var actorID uuid.UUID
	if userID, ok := c.Locals(constants.ContextKeys.UserID).(uuid.UUID); ok {
		actorID = userID
	}

	resendResult, err := h.service.ResendNotification(c.UserContext(), id, &actorID)

	logStatus := "success"
	description := fmt.Sprintf("Resent notification %s", idStr)
	if err != nil {
		logStatus = "failed"
		description = fmt.Sprintf("Failed to resend notification %s: %s", idStr, err.Error())
	}
	if h.actionLogService != nil {
		_ = h.actionLogService.LogAction(c.UserContext(), &services.LogActionParams{
			UserID:      actorID,
			Action:      "resend",
			Module:      "notifications",
			ResourceID:  idStr,
			Description: description,
			IPAddress:   c.IP(),
			UserAgent:   c.Get("User-Agent"),
			Status:      logStatus,
			ErrorMsg:    errString(err),
		})
	}

	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return c.Status(fiber.StatusOK).JSON(fiber.Map{
		"success": true,
		"data":    resendResult,
	})
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ListMonitoring handles GET /api/v1/admin/notification-monitoring — the
// admin-wide dashboard view (search/filter across every user's notifications,
// unlike List which is scoped to the requesting user's inbox).
func (h *NotificationHandler) ListMonitoring(c *fiber.Ctx) error {
	filter := &models.NotificationMonitoringFilter{
		Page:  1,
		Limit: 20,
	}

	if err := c.QueryParser(filter); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_query_parameters"))
	}

	if err := validation.ValidateStruct(c.UserContext(), filter); len(err) != 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"success": false,
			"errors":  err,
		})
	}

	if filter.Channel != "" {
		validChannels := map[string]bool{
			"email": true, "sms": true, "whatsapp": true, "notification": true, "push-notification": true,
		}
		for _, ch := range strings.Split(filter.Channel, ",") {
			ch = strings.TrimSpace(ch)
			if ch != "" && !validChannels[ch] {
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
					"success": false,
					"error":   fmt.Sprintf("invalid channel %q — must be one of email, sms, whatsapp, notification, push-notification", ch),
				})
			}
		}
	}

	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.Limit < 1 || filter.Limit > 100 {
		filter.Limit = 20
	}

	if startDate := c.Query("start_date"); startDate != "" {
		if t, err := time.Parse("2006-01-02", startDate); err == nil {
			filter.StartDate = &t
		}
	}
	if endDate := c.Query("end_date"); endDate != "" {
		if t, err := time.Parse("2006-01-02", endDate); err == nil {
			t = t.Add(24*time.Hour - time.Second)
			filter.EndDate = &t
		}
	}

	notifications, total, err := h.service.ListMonitoring(c.UserContext(), filter)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	totalPages := (int(total) + filter.Limit - 1) / filter.Limit

	return c.JSON(fiber.Map{
		"success":     true,
		"data":        notifications,
		"total_items": total,
		"total_pages": totalPages,
		"page":        filter.Page,
		"limit":       filter.Limit,
	})
}

// Get handles GET /api/v1/notifications/:id
func (h *NotificationHandler) Get(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_notification_id"))
	}

	notification, err := h.service.GetNotification(c.UserContext(), id)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, i18n.T(c.UserContext(), "notification_not_found"))
	}

	if strings.HasPrefix(strings.ToLower(c.Get("Accept-Language")), "ar") {
		if notification.SubjectAr != "" {
			notification.Subject = notification.SubjectAr
		}
		if notification.BodyAr != "" {
			notification.Body = notification.BodyAr
		}
	}

	return c.JSON(fiber.Map{
		"success": true,
		"data":    notification,
	})
}

// Delete handles DELETE /api/v1/notifications/:id (moves to trash)
func (h *NotificationHandler) Delete(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_notification_id"))
	}

	if err := h.service.DeleteNotification(c.UserContext(), id); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return c.JSON(fiber.Map{
		"success": true,
		"message": "Notification moved to trash",
	})
}

// PermanentDelete handles DELETE /api/v1/notifications/:id/permanent
func (h *NotificationHandler) PermanentDelete(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_notification_id"))
	}

	if err := h.service.PermanentDelete(c.UserContext(), id); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return c.JSON(fiber.Map{
		"success": true,
		"message": "Notification permanently deleted",
	})
}

// CreateDraft handles POST /api/v1/notifications/drafts
func (h *NotificationHandler) CreateDraft(c *fiber.Ctx) error {
	var req models.CreateDraftRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}

	// Get user ID from context
	userID, ok := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)
	if !ok {
		return utils.ErrorResponse(c, fiber.StatusUnauthorized, i18n.T(c.UserContext(), "user_not_authenticated"))
	}

	draft, err := h.service.CreateDraft(c.UserContext(), &req, userID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"success": true,
		"data":    draft,
	})
}

// UpdateDraft handles PUT /api/v1/notifications/drafts/:id
func (h *NotificationHandler) UpdateDraft(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid draft ID")
	}

	var req models.UpdateDraftRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}

	draft, err := h.service.UpdateDraft(c.UserContext(), id, &req)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return c.JSON(fiber.Map{
		"success": true,
		"data":    draft,
	})
}

// SendDraft handles POST /api/v1/notifications/drafts/:id/send
func (h *NotificationHandler) SendDraft(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid draft ID")
	}

	result, err := h.service.SendDraft(c.UserContext(), id)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return c.JSON(fiber.Map{
		"success": true,
		"data":    result,
		"message": "Draft sent successfully",
	})
}

// MarkAsRead handles PATCH /api/v1/notifications/:id/read
func (h *NotificationHandler) MarkAsRead(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_notification_id"))
	}

	type ReadRequest struct {
		IsRead bool `json:"is_read"`
	}

	var req ReadRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}

	if err := h.service.MarkAsRead(c.UserContext(), id, req.IsRead); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return c.JSON(fiber.Map{
		"success": true,
		"message": "Notification updated",
	})
}

// ToggleStar handles PATCH /api/v1/notifications/:id/star
func (h *NotificationHandler) ToggleStar(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_notification_id"))
	}

	type StarRequest struct {
		IsStarred bool `json:"is_starred"`
	}

	var req StarRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}

	if err := h.service.ToggleStar(c.UserContext(), id, req.IsStarred); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return c.JSON(fiber.Map{
		"success": true,
		"message": "Notification updated",
	})
}

// MoveToCategory handles PATCH /api/v1/notifications/:id/move
func (h *NotificationHandler) MoveToCategory(c *fiber.Ctx) error {
	idStr := c.Params("id")
	id, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_notification_id"))
	}

	type MoveRequest struct {
		Category string `json:"category"`
	}

	var req MoveRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}

	if err := h.service.MoveToCategory(c.UserContext(), id, req.Category); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return c.JSON(fiber.Map{
		"success": true,
		"message": "Notification moved successfully",
	})
}

// BulkMoveToCategory handles POST /api/v1/notifications/bulk/move
func (h *NotificationHandler) BulkMoveToCategory(c *fiber.Ctx) error {
	type BulkMoveRequest struct {
		IDs      []string `json:"ids"`
		Category string   `json:"category"`
	}

	var req BulkMoveRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}

	// Parse UUIDs
	ids := make([]uuid.UUID, len(req.IDs))
	for i, idStr := range req.IDs {
		id, err := uuid.Parse(idStr)
		if err != nil {
			return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid notification ID: "+idStr)
		}
		ids[i] = id
	}

	if err := h.service.BulkMoveToCategory(c.UserContext(), ids, req.Category); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return c.JSON(fiber.Map{
		"success": true,
		"message": "Notifications moved successfully",
	})
}

// BulkDelete handles POST /api/v1/notifications/bulk/delete
func (h *NotificationHandler) BulkDelete(c *fiber.Ctx) error {
	type BulkDeleteRequest struct {
		IDs []string `json:"ids"`
	}

	var req BulkDeleteRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}

	// Parse UUIDs
	ids := make([]uuid.UUID, len(req.IDs))
	for i, idStr := range req.IDs {
		id, err := uuid.Parse(idStr)
		if err != nil {
			return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid notification ID: "+idStr)
		}
		ids[i] = id
	}

	if err := h.service.BulkDelete(c.UserContext(), ids); err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return c.JSON(fiber.Map{
		"success": true,
		"message": "Notifications deleted successfully",
	})
}

// Reply handles POST /api/v1/notifications/:id/reply
func (h *NotificationHandler) Reply(c *fiber.Ctx) error {
	// Get the original email ID
	idStr := c.Params("id")
	originalID, err := uuid.Parse(idStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_notification_id"))
	}

	// Get user ID from context
	var sentBy *uuid.UUID
	if userID, ok := c.Locals(constants.ContextKeys.UserID).(uuid.UUID); ok {
		sentBy = &userID
	}

	type ReplyRequest struct {
		To       []string `json:"to"`
		CC       []string `json:"cc,omitempty"`
		BCC      []string `json:"bcc,omitempty"`
		Subject  string   `json:"subject"`
		Body     string   `json:"body"`
		BodyHTML string   `json:"body_html,omitempty"`
	}

	var req ReplyRequest
	if err := c.BodyParser(&req); err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_request_body"))
	}

	// Send reply with threading
	reply, err := h.service.ReplyToNotification(c.UserContext(), originalID, req.To, req.CC, req.BCC, req.Subject, req.Body, req.BodyHTML, sentBy)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"success": true,
		"data":    reply,
		"message": "Reply sent successfully",
	})
}

// GetThread handles GET /api/v1/notifications/threads/:thread_id
func (h *NotificationHandler) GetThread(c *fiber.Ctx) error {
	threadIDStr := c.Params("thread_id")
	threadID, err := uuid.Parse(threadIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, "Invalid thread ID")
	}

	// Get all emails in this thread
	filter := &models.NotificationLogFilter{
		ThreadID: &threadID,
		Page:     1,
		Limit:    100, // Get all messages in thread
	}

	notifications, total, err := h.service.ListNotifications(c.UserContext(), filter)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, err.Error())
	}

	applyAcceptLanguage(c.Get("Accept-Language"), notifications)

	return c.JSON(fiber.Map{
		"success":     true,
		"data":        notifications,
		"total_items": total,
		"thread_id":   threadID,
	})
}

// DownloadAttachment handles GET /api/v1/notifications/:id/attachments/:filename
// Downloads a specific attachment from a notification
func (h *NotificationHandler) DownloadAttachment(c *fiber.Ctx) error {
	// Get notification ID
	notificationIDStr := c.Params("id")
	notificationID, err := uuid.Parse(notificationIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_notification_id"))
	}

	// Get filename from URL
	filename := c.Params("filename")
	if filename == "" {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "filename_required"))
	}

	// Fetch the notification to verify it exists and get attachment info
	notification, err := h.service.GetNotification(c.UserContext(), notificationID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, i18n.T(c.UserContext(), "notification_not_found"))
	}

	// Find the attachment with matching filename
	var storagePath string
	var contentType string
	for _, att := range notification.Attachments {
		if att.Filename == filename {
			storagePath = att.StoragePath
			contentType = att.ContentType
			break
		}
	}

	if storagePath == "" {
		return utils.ErrorResponse(c, fiber.StatusNotFound, i18n.T(c.UserContext(), "attachment_not_found"))
	}

	// Download from MinIO
	if h.storage == nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, i18n.T(c.UserContext(), "storage_not_configured"))
	}

	fileReader, err := h.storage.GetFile(c.UserContext(), storagePath)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "Failed to retrieve attachment: "+err.Error())
	}
	defer fileReader.Close()

	// Read the file content
	fileData, err := io.ReadAll(fileReader)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, i18n.T(c.UserContext(), "failed_to_read_attachment"))
	}

	// Set appropriate headers
	c.Set("Content-Type", contentType)
	c.Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	c.Set("Content-Length", fmt.Sprintf("%d", len(fileData)))

	return c.Send(fileData)
}

// GetAttachmentURL handles GET /api/v1/notifications/:id/attachments/:filename/url
// Returns a presigned URL for downloading the attachment
func (h *NotificationHandler) GetAttachmentURL(c *fiber.Ctx) error {
	// Get notification ID
	notificationIDStr := c.Params("id")
	notificationID, err := uuid.Parse(notificationIDStr)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "invalid_notification_id"))
	}

	// Get filename from URL
	filename := c.Params("filename")
	if filename == "" {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "filename_required"))
	}

	// Fetch the notification to verify it exists and get attachment info
	notification, err := h.service.GetNotification(c.UserContext(), notificationID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, i18n.T(c.UserContext(), "notification_not_found"))
	}

	// Find the attachment with matching filename
	var storagePath string
	for _, att := range notification.Attachments {
		if att.Filename == filename {
			storagePath = att.StoragePath
			break
		}
	}

	if storagePath == "" {
		return utils.ErrorResponse(c, fiber.StatusNotFound, i18n.T(c.UserContext(), "attachment_not_found"))
	}

	// Generate presigned URL from MinIO
	if h.storage == nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, i18n.T(c.UserContext(), "storage_not_configured"))
	}

	presignedURL, err := h.storage.GetFileURL(c.UserContext(), storagePath)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, "Failed to generate download URL: "+err.Error())
	}

	return c.JSON(fiber.Map{
		"success": true,
		"data": fiber.Map{
			"url":      presignedURL,
			"filename": filename,
		},
	})
}

// DownloadNotificationAttachmentByID handles GET /api/v1/attachments/:attachment_id
// Downloads a notification attachment by its ID
func (h *NotificationHandler) DownloadNotificationAttachmentByID(c *fiber.Ctx) error {
	attachmentID := c.Params("attachment_id")
	if attachmentID == "" {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "attachment_id_required"))
	}

	// Find the notification containing this attachment
	_, attachment, err := h.service.FindNotificationByAttachmentID(c.UserContext(), attachmentID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, i18n.T(c.UserContext(), "attachment_not_found"))
	}

	if attachment.StoragePath == "" {
		return utils.ErrorResponse(c, fiber.StatusNotFound, i18n.T(c.UserContext(), "attachment_storage_missing"))
	}

	// Download from MinIO
	if h.storage == nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, i18n.T(c.UserContext(), "storage_not_configured"))
	}

	fileReader, err := h.storage.GetFile(c.UserContext(), attachment.StoragePath)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "Failed to retrieve attachment: "+err.Error())
	}
	defer fileReader.Close()

	// Read the file content
	fileData, err := io.ReadAll(fileReader)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, i18n.T(c.UserContext(), "failed_to_read_attachment"))
	}

	// Set appropriate headers for download
	c.Set("Content-Type", attachment.ContentType)
	c.Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", attachment.Filename))
	c.Set("Content-Length", fmt.Sprintf("%d", len(fileData)))

	return c.Send(fileData)
}

// PreviewNotificationAttachmentByID handles GET /api/v1/attachments/:attachment_id/preview
// Previews a notification attachment by its ID (inline display)
func (h *NotificationHandler) PreviewNotificationAttachmentByID(c *fiber.Ctx) error {
	attachmentID := c.Params("attachment_id")
	if attachmentID == "" {
		return utils.ErrorResponse(c, fiber.StatusBadRequest, i18n.T(c.UserContext(), "attachment_id_required"))
	}

	// Find the notification containing this attachment
	_, attachment, err := h.service.FindNotificationByAttachmentID(c.UserContext(), attachmentID)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, i18n.T(c.UserContext(), "attachment_not_found"))
	}

	if attachment.StoragePath == "" {
		return utils.ErrorResponse(c, fiber.StatusNotFound, i18n.T(c.UserContext(), "attachment_storage_missing"))
	}

	// Download from MinIO
	if h.storage == nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, i18n.T(c.UserContext(), "storage_not_configured"))
	}

	fileReader, err := h.storage.GetFile(c.UserContext(), attachment.StoragePath)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusNotFound, "Failed to retrieve attachment: "+err.Error())
	}
	defer fileReader.Close()

	// Read the file content
	fileData, err := io.ReadAll(fileReader)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusInternalServerError, i18n.T(c.UserContext(), "failed_to_read_attachment"))
	}

	// Set appropriate headers for inline preview
	c.Set("Content-Type", attachment.ContentType)
	c.Set("Content-Disposition", fmt.Sprintf("inline; filename=\"%s\"", attachment.Filename))
	c.Set("Content-Length", fmt.Sprintf("%d", len(fileData)))

	return c.Send(fileData)
}

// applyAcceptLanguage replaces subject/body with Arabic versions in-place when
// Accept-Language is "ar" and Arabic content is available.
func applyAcceptLanguage(acceptLang string, notifications []models.NotificationLogResponse) {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(acceptLang)), "ar") {
		return
	}
	for i := range notifications {
		if notifications[i].SubjectAr != "" {
			notifications[i].Subject = notifications[i].SubjectAr
		}
		if notifications[i].BodyAr != "" {
			notifications[i].Body = notifications[i].BodyAr
		}
	}
}
