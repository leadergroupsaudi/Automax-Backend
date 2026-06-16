package services

import (
	"context"
	"fmt"
	"log"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/internal/storage"
	"github.com/automax/backend/internal/utils"
	"github.com/google/uuid"
)

type NotificationService struct {
	templateRepo repository.NotificationTemplateRepository
	logRepo      repository.NotificationLogRepository
	userRepo     repository.UserRepository

	storage *storage.MinIOStorage
}

type SendNotificationResult struct {
	SentLog     *models.NotificationLog
	InboxLogIDs []uuid.UUID // IDs of inbox copies created for internal recipients
}

func NewNotificationService(
	templateRepo repository.NotificationTemplateRepository,
	logRepo repository.NotificationLogRepository, userRepo repository.UserRepository, storage *storage.MinIOStorage,
) *NotificationService {
	return &NotificationService{
		templateRepo: templateRepo,
		logRepo:      logRepo,
		userRepo:     userRepo,
		storage:      storage,
	}
}

func (s *NotificationService) SendNotification(ctx context.Context, channel string, templateCode *string, language string, to []string, cc []string, bcc []string, subject string, body string, variables map[string]string, attachments []models.AttachmentData, sentBy *uuid.UUID, sessionID *uuid.UUID) (*SendNotificationResult, error) {
	// REQUIRED: at least one recipient
	if len(to) == 0 && len(cc) == 0 && len(bcc) == 0 {
		return nil, fmt.Errorf("at least one recipient (to, cc, or bcc) is required")
	}

	// When a template code is provided the template itself supplies subject/body,
	// so the caller is allowed to pass empty strings for both.
	if (templateCode == nil || *templateCode == "") &&
		strings.TrimSpace(subject) == "" && strings.TrimSpace(body) == "" {
		return nil, fmt.Errorf("either subject or body must be provided")
	}

	//TEMPLATE LOGIC
	if templateCode != nil && *templateCode != "" {

		tpl, err := s.templateRepo.FindByCode(ctx, *templateCode, channel)
		if err != nil {
			// Template not found — fall through and use the provided subject/body as-is.
			log.Printf("[NotificationService] template '%s' not found for channel '%s', using fallback content: %v", *templateCode, channel, err)
		} else {
			// Use whichever body is set. When the requested language has content, prefer it;
			// otherwise use whichever variant is non-empty so the template always sends.
			var tplBody, tplSubject string
			if language == "ar" && tpl.BodyAR != "" {
				tplBody = tpl.BodyAR
				tplSubject = tpl.SubjectAR
			} else if language != "ar" && tpl.BodyEN != "" {
				tplBody = tpl.BodyEN
				tplSubject = tpl.SubjectEN
			} else if tpl.BodyEN != "" {
				tplBody = tpl.BodyEN
				tplSubject = tpl.SubjectEN
			} else if tpl.BodyAR != "" {
				tplBody = tpl.BodyAR
				tplSubject = tpl.SubjectAR
			} else {
				log.Printf("[NotificationService] Template '%s': both EN and AR bodies are empty — skipping send", *templateCode)
			}

			if len(variables) > 0 {
				log.Printf("[NotificationService] Rendering template '%s' (channel=%s) with %d variable(s)", *templateCode, channel, len(variables))
				if tplBody != "" {
					body, _ = RenderTemplate(tplBody, variables)
				}
				if tplSubject != "" {
					subject, _ = RenderTemplate(tplSubject, variables)
				}
			} else {
				log.Printf("[NotificationService] Rendering template '%s' (channel=%s) with NO variables — placeholders will not be substituted", *templateCode, channel)
				if tplBody != "" {
					body = tplBody
				}
				if tplSubject != "" {
					subject = tplSubject
				}
			}
		}

	} else {
		// No template → render provided body/subject if variables exist
		if len(variables) > 0 {
			if body != "" {
				body, _ = RenderTemplate(body, variables)
			}
			if subject != "" {
				subject, _ = RenderTemplate(subject, variables)
			}
		}
	}

	status := "sent"
	provider := channel
	fromPhone := ""
	var recipientStatuses models.RecipientArray
	var attachmentInfo models.AttachmentArray

	allRecipients := append(append([]string{}, to...), cc...)
	allRecipients = append(allRecipients, bcc...)

	for _, att := range attachments {

		// Upload to MinIO using bytes
		objectName, err := storage.Storage.UploadBytes(
			ctx,
			att.Data,
			att.Filename,
			att.ContentType,
			"sla_breach-notifications",
		)
		if err != nil {
			return nil, fmt.Errorf("failed to upload attachment: %w", err)
		}

		attachmentInfo = append(attachmentInfo, models.AttachmentInfo{
			ID:          att.AttachmentID.String(),
			Filename:    att.Filename,
			ContentType: att.ContentType,
			Size:        int64(len(att.Data)),
			URL:         "/api/v1/attachments/" + att.AttachmentID.String(),
			PreviewURL:  "/api/v1/attachments/" + att.AttachmentID.String() + "/preview",
			StoragePath: objectName,
		})
	}

	switch channel {
	case "email":
		_, err := utils.SendSMTPWithCCBCC(to, cc, bcc, subject, body, attachments)
		if err != nil {

			status = "failed"

			for _, r := range allRecipients {
				recipientStatuses = append(recipientStatuses, models.RecipientInfo{
					Email:   r,
					Channel: r,
					Type:    utils.GetRecipientType(r, to, cc, bcc),
					Status:  "failed",
					Error:   err.Error(),
				})
			}

		} else {

			// Success case
			for _, r := range allRecipients {
				recipientStatuses = append(recipientStatuses, models.RecipientInfo{
					Email:   r,
					Channel: r,
					Type:    utils.GetRecipientType(r, to, cc, bcc),
					Status:  "success",
				})
			}

		}

		provider = "smtp"
	case "sms":
		if sentBy != nil {
			if senderUser, err := s.userRepo.FindByID(ctx, *sentBy); err == nil && senderUser != nil {
				fromPhone = senderUser.Phone
			}
		}
		for _, phone := range to {
			err := utils.DispatchSMS(phone, body)
			if err != nil {
				status = "failed"
				recipientStatuses = append(recipientStatuses, models.RecipientInfo{
					Email:   phone,
					Channel: phone,
					Type:    "to",
					Status:  "failed",
					Error:   err.Error(),
				})
			} else {
				recipientStatuses = append(recipientStatuses, models.RecipientInfo{
					Email:   phone,
					Channel: phone,
					Type:    "to",
					Status:  "success",
				})
			}
		}
		if strings.EqualFold(os.Getenv("SMS_PROVIDER"), "msg91") {
			provider = "msg91"
		} else {
			provider = "twilio"
		}
	case "whatsapp":
		status = "sent"
		for _, phone := range to {
			err := utils.SendOTPWithMetaTemplate(phone, body)
			if err != nil {
				status = "failed"
				recipientStatuses = append(recipientStatuses, models.RecipientInfo{
					Channel:      phone,
					Type:         "to",
					Status:       "failed",
					Error:        err.Error(),
					ErrorMessage: err.Error(),
				})

			} else {
				recipientStatuses = append(recipientStatuses, models.RecipientInfo{
					Channel: phone,
					Type:    "to",
					Status:  "success",
				})
			}
		}

		provider = "meta"

	case "notification":
		// In-app only — no external delivery. Recipients must be internal user emails.
		status = "sent"
		provider = "in-app"
		for _, recipient := range to {
			recipientStatuses = append(recipientStatuses, models.RecipientInfo{
				Channel: recipient,
				Type:    "to",
				Status:  "success",
			})
		}

	default:
		return nil, fmt.Errorf("unsupported channel: %s", channel)
	}

	now := time.Now()
	log := &models.NotificationLog{
		ID:           uuid.New(),
		Channel:      channel,
		Direction:    models.DirectionOutbound,
		Category:     models.CategorySent,
		TemplateCode: deref(templateCode),
		Language:     language,
		Recipients:   recipientStatuses,
		CC:           cc,
		BCC:          bcc,
		From:         fromPhone,
		Subject:      subject,
		Body:         body,
		OTPSessionID: sessionID,
		OTPVerified:  false,
		Status:       status,
		Provider:     provider,
		Attachments:  attachmentInfo,
		SentBy:       sentBy,
		SentAt:       &now,
		CreatedAt:    now,
		UpdatedAt:    &now,
	}

	if err := s.logRepo.Create(ctx, log); err != nil {
		return nil, err
	}

	// Always create inbox logs for internal recipients, even if delivery failed. This ensures escalation and system notifications always appear in the recipient's inbox.
	var inboxLogIDs []uuid.UUID
	for _, recipient := range to {

		// Look up the system user by email (email channel) or phone (sms whatsapp channel)
		var user *models.User
		var userErr error
		if channel == "sms" || channel == "whatsapp" {
			user, userErr = s.userRepo.FindByMobile(ctx, recipient)
		} else {
			user, userErr = s.userRepo.FindByEmail(ctx, recipient)
		}
		if userErr != nil || user == nil {
			continue // Skip external/unknown recipients
		}

		inboxLog := &models.NotificationLog{
			ID:           uuid.New(),
			Channel:      channel,
			Direction:    models.DirectionInbound,
			Category:     models.CategoryInbox,
			TemplateCode: deref(templateCode),
			Language:     language,
			Recipients:   recipientStatuses,
			Subject:      subject,
			Body:         body,
			Status:       status,
			Provider:     provider,
			Attachments:  attachmentInfo,
			SentBy:       sentBy,
			ReceivedBy:   &user.ID, // this makes it appear in inbox
			CreatedAt:    now,
			UpdatedAt:    &now,
		}

		if err := s.logRepo.Create(ctx, inboxLog); err == nil {
			inboxLogIDs = append(inboxLogIDs, inboxLog.ID)
		}
	}

	if status == "failed" {
		return nil, fmt.Errorf("notification delivery failed")
	}

	return &SendNotificationResult{
		SentLog:     log,
		InboxLogIDs: inboxLogIDs,
	}, nil
}

func deref(s *string) string {
	if s != nil {
		return *s
	}
	return ""
}

// GetNotificationStats returns per-user message counts merged across sent and received.
func (s *NotificationService) GetNotificationStats(ctx context.Context, channel string, sentBy *uuid.UUID, receivedBy *uuid.UUID) (*models.NotificationStatsData, error) {
	sentByRows, err := s.logRepo.GetStatsBySentBy(ctx, channel, sentBy, receivedBy)
	if err != nil {
		return nil, err
	}

	receivedByRows, err := s.logRepo.GetStatsByReceivedBy(ctx, channel, sentBy, receivedBy)
	if err != nil {
		return nil, err
	}

	// Merge sent and received counts per user.
	type merged struct {
		Total  int64
		Sent   int64
		Failed int64
		Inbox  int64
		Draft  int64
		Outbox int64
		Trash  int64
		Spam   int64
	}
	statsMap := make(map[uuid.UUID]*merged)

	for _, r := range sentByRows {
		m := statsMap[r.UserID]
		if m == nil {
			m = &merged{}
			statsMap[r.UserID] = m
		}
		m.Total += r.Total
		m.Sent += r.Sent
		m.Failed += r.Failed
		m.Draft += r.Draft
		m.Outbox += r.Outbox
		m.Trash += r.Trash
		m.Spam += r.Spam
	}

	for _, r := range receivedByRows {
		m := statsMap[r.UserID]
		if m == nil {
			m = &merged{}
			statsMap[r.UserID] = m
		}
		m.Total += r.Total
		m.Inbox += r.Inbox
		m.Trash += r.Trash
		m.Spam += r.Spam
	}

	// Bulk-fetch user details.
	userIDs := make([]uuid.UUID, 0, len(statsMap))
	for id := range statsMap {
		userIDs = append(userIDs, id)
	}
	userMap := make(map[uuid.UUID]*models.UserResponse)
	if len(userIDs) > 0 {
		if users, err := s.userRepo.FindByIDs(ctx, userIDs); err == nil {
			for i := range users {
				resp := models.ToUserResponse(&users[i])
				userMap[users[i].ID] = &resp
			}
		}
	}

	entries := make([]models.NotificationUserStatsEntry, 0, len(statsMap))
	for userID, m := range statsMap {
		entry := models.NotificationUserStatsEntry{
			UserID: userID.String(),
			Counts: models.NotificationUserCounts{
				Total:  m.Total,
				Sent:   m.Sent,
				Failed: m.Failed,
				Inbox:  m.Inbox,
				Draft:  m.Draft,
				Outbox: m.Outbox,
				Trash:  m.Trash,
				Spam:   m.Spam,
			},
		}
		if u := userMap[userID]; u != nil {
			entry.Name = u.FirstName + " " + u.LastName
			entry.Email = u.Email
		}
		entries = append(entries, entry)
	}

	return &models.NotificationStatsData{
		Channel: channel,
		Users:   entries,
	}, nil
}

// GetNotificationStatsByUser returns merged sent+received counts for a single user.
func (s *NotificationService) GetNotificationStatsByUser(ctx context.Context, channel string, userID uuid.UUID) (*models.NotificationStatsResponse, error) {
	row, err := s.logRepo.GetUserNotificationStats(ctx, channel, userID)
	if err != nil {
		return nil, err
	}

	counts := models.NotificationUserCounts{
		Total:  row.Total,
		Sent:   row.Sent,
		Failed: row.Failed,
		Inbox:  row.Inbox,
		Draft:  row.Draft,
		Outbox: row.Outbox,
		Trash:  row.Trash,
		Spam:   row.Spam,
	}

	return &models.NotificationStatsResponse{
		Channel: channel,
		Counts:  counts,
	}, nil
}

// ListNotifications retrieves notifications with filtering and search
func (s *NotificationService) ListNotifications(ctx context.Context, filter *models.NotificationLogFilter) ([]models.NotificationLogResponse, int64, error) {
	logs, total, err := s.logRepo.List(ctx, filter)
	if err != nil {
		return nil, 0, err
	}

	responses := make([]models.NotificationLogResponse, len(logs))
	for i, log := range logs {
		responses[i] = models.ToNotificationLogResponse(&log)
	}

	return responses, total, nil
}

// GetNotification retrieves a single notification by ID
func (s *NotificationService) GetNotification(ctx context.Context, id uuid.UUID) (*models.NotificationLogResponse, error) {
	log, err := s.logRepo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}

	response := models.ToNotificationLogResponse(log)
	return &response, nil
}

// FindNotificationByAttachmentID finds a notification and attachment by attachment ID
func (s *NotificationService) FindNotificationByAttachmentID(ctx context.Context, attachmentID string) (*models.NotificationLog, *models.AttachmentInfo, error) {
	// Get all notifications that have attachments (this is a simple implementation)
	// For better performance, consider adding an index on attachments JSONB field
	notifications, err := s.logRepo.FindByAttachmentID(ctx, attachmentID)
	if err != nil {
		return nil, nil, fmt.Errorf("attachment not found: %v", err)
	}

	// Find the specific attachment within the notification
	for _, notification := range notifications {
		for _, att := range notification.Attachments {
			if att.ID == attachmentID {
				return notification, &att, nil
			}
		}
	}

	return nil, nil, fmt.Errorf("attachment not found")
}

// DeleteNotification soft deletes a notification (moves to trash)
func (s *NotificationService) DeleteNotification(ctx context.Context, id uuid.UUID) error {
	// Move to trash instead of hard delete
	return s.logRepo.MoveToCategory(ctx, id, models.CategoryTrash)
}

// PermanentDelete permanently deletes a notification
func (s *NotificationService) PermanentDelete(ctx context.Context, id uuid.UUID) error {
	return s.logRepo.Delete(ctx, id)
}

// CreateDraft creates a draft email
func (s *NotificationService) CreateDraft(ctx context.Context, req *models.CreateDraftRequest, createdBy uuid.UUID) (*models.NotificationLogResponse, error) {
	// Build recipients array for draft
	var recipients models.RecipientArray
	for _, email := range req.To {
		recipients = append(recipients, models.RecipientInfo{
			Channel: email,
			Type:    "to",
			Status:  "draft",
		})
	}

	// Convert attachments
	var attachmentInfo models.AttachmentArray
	for _, att := range req.Attachments {
		attachmentInfo = append(attachmentInfo, models.AttachmentInfo{
			Filename:    att.Filename,
			ContentType: att.ContentType,
			Size:        int64(len(att.Data)),
		})
	}

	now := time.Now()
	draft := &models.NotificationLog{
		ID:          uuid.New(),
		Channel:     req.Channel,
		Direction:   models.DirectionOutbound,
		Category:    models.CategoryDraft,
		Recipients:  recipients,
		CC:          req.CC,
		BCC:         req.BCC,
		Subject:     req.Subject,
		Body:        req.Body,
		BodyHTML:    req.BodyHTML,
		Status:      models.StatusDraft,
		Attachments: attachmentInfo,
		SentBy:      &createdBy,
		ScheduledAt: req.ScheduledAt,
		CreatedAt:   now,
		UpdatedAt:   &now,
	}

	if err := s.logRepo.Create(ctx, draft); err != nil {
		return nil, err
	}

	response := models.ToNotificationLogResponse(draft)
	return &response, nil
}

// UpdateDraft updates an existing draft
func (s *NotificationService) UpdateDraft(ctx context.Context, id uuid.UUID, req *models.UpdateDraftRequest) (*models.NotificationLogResponse, error) {
	draft, err := s.logRepo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if draft.Category != models.CategoryDraft {
		return nil, fmt.Errorf("can only update drafts")
	}

	// Update fields if provided
	if len(req.To) > 0 {
		var recipients models.RecipientArray
		for _, email := range req.To {
			recipients = append(recipients, models.RecipientInfo{
				Channel: email,
				Type:    "to",
				Status:  "draft",
			})
		}
		draft.Recipients = recipients
	}

	if len(req.CC) > 0 {
		draft.CC = req.CC
	}
	if len(req.BCC) > 0 {
		draft.BCC = req.BCC
	}
	if req.Subject != "" {
		draft.Subject = req.Subject
	}
	if req.Body != "" {
		draft.Body = req.Body
	}
	if req.BodyHTML != "" {
		draft.BodyHTML = req.BodyHTML
	}
	if req.ScheduledAt != nil {
		draft.ScheduledAt = req.ScheduledAt
	}

	// Update attachments if provided
	if len(req.Attachments) > 0 {
		var attachmentInfo models.AttachmentArray
		for _, att := range req.Attachments {
			attachmentInfo = append(attachmentInfo, models.AttachmentInfo{
				Filename:    att.Filename,
				ContentType: att.ContentType,
				Size:        int64(len(att.Data)),
			})
		}
		draft.Attachments = attachmentInfo
	}

	now := time.Now()
	draft.UpdatedAt = &now

	if err := s.logRepo.Update(ctx, draft); err != nil {
		return nil, err
	}

	response := models.ToNotificationLogResponse(draft)
	return &response, nil
}

// SendDraft sends a draft email
func (s *NotificationService) SendDraft(ctx context.Context, id uuid.UUID) (*models.NotificationLogResponse, error) {
	draft, err := s.logRepo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}

	if draft.Category != models.CategoryDraft {
		return nil, fmt.Errorf("can only send drafts")
	}

	// Extract TO recipients from Recipients array
	var to []string
	for _, r := range draft.Recipients {
		if r.Type == "to" {
			to = append(to, r.Channel)
		}
	}

	// Convert AttachmentInfo to AttachmentData for sending
	var attachments []models.AttachmentData
	for _, att := range draft.Attachments {
		// Note: We don't have the actual file data stored, so we can't send attachments from drafts
		// This is a limitation - in a real system, you'd store attachment files separately
		attachments = append(attachments, models.AttachmentData{
			Filename:    att.Filename,
			ContentType: att.ContentType,
			Data:        []byte{}, // Empty data - need to store files separately
		})
	}

	// Send the email using SendNotification
	result, err := s.SendNotification(
		ctx,
		draft.Channel,
		nil,
		draft.Language,
		to,
		draft.CC,
		draft.BCC,
		draft.Subject,
		draft.Body,
		nil,
		attachments,
		draft.SentBy,
		nil,
	)
	if err != nil {
		return nil, err
	}

	// Delete the draft after successful send
	_ = s.logRepo.Delete(ctx, id)

	// Convert NotificationLog to NotificationLogResponse
	response := models.ToNotificationLogResponse(result.SentLog)
	return &response, nil
}

// MarkAsRead marks an email as read/unread
func (s *NotificationService) MarkAsRead(ctx context.Context, id uuid.UUID, isRead bool) error {
	return s.logRepo.MarkAsRead(ctx, id, isRead)
}

// ToggleStar toggles star/unstar on an email
func (s *NotificationService) ToggleStar(ctx context.Context, id uuid.UUID, isStarred bool) error {
	return s.logRepo.ToggleStar(ctx, id, isStarred)
}

// MoveToCategory moves email to a different category/folder
func (s *NotificationService) MoveToCategory(ctx context.Context, id uuid.UUID, category string) error {
	// Validate category
	validCategories := []string{
		models.CategoryInbox,
		models.CategorySent,
		models.CategoryDraft,
		models.CategoryOutbox,
		models.CategoryTrash,
		models.CategorySpam,
	}

	valid := false
	for _, c := range validCategories {
		if category == c {
			valid = true
			break
		}
	}

	if !valid {
		return fmt.Errorf("invalid category: %s", category)
	}

	return s.logRepo.MoveToCategory(ctx, id, category)
}

// BulkMoveToCategory moves multiple emails to a category
func (s *NotificationService) BulkMoveToCategory(ctx context.Context, ids []uuid.UUID, category string) error {
	return s.logRepo.BulkMoveToCategory(ctx, ids, category)
}

// BulkDelete deletes multiple emails
func (s *NotificationService) BulkDelete(ctx context.Context, ids []uuid.UUID) error {
	return s.logRepo.BulkDelete(ctx, ids)
}

// SaveInboundNotification saves an inbound notification (received email/SMS)
func (s *NotificationService) SaveInboundNotification(ctx context.Context, log *models.NotificationLog) error {
	return s.logRepo.Create(ctx, log)
}

// ReplyToNotification sends a reply to an existing email and maintains threading
func (s *NotificationService) ReplyToNotification(ctx context.Context, originalID uuid.UUID, to []string, cc []string, bcc []string, subject string, body string, bodyHTML string, sentBy *uuid.UUID) (*models.NotificationLogResponse, error) {
	// Get the original email
	original, err := s.logRepo.FindByID(ctx, originalID)
	if err != nil {
		return nil, fmt.Errorf("original email not found: %w", err)
	}

	// Determine thread ID
	threadID := original.ThreadID
	if threadID == nil {
		// If original email has no thread, use its ID as the thread ID
		threadID = &original.ID
	}

	// Auto-prefix subject with "Re:" if not already present
	if subject != "" && !strings.HasPrefix(subject, "Re:") {
		subject = "Re: " + subject
	} else if subject == "" && original.Subject != "" {
		subject = "Re: " + original.Subject
	}

	// Send the reply
	result, err := s.SendNotification(
		ctx,
		original.Channel,
		nil,
		original.Language,
		to,
		cc,
		bcc,
		subject,
		body,
		nil,
		nil,
		sentBy,
		nil,
	)
	if err != nil {
		return nil, err
	}

	// Update the sent email with threading info
	result.SentLog.ThreadID = threadID
	result.SentLog.InReplyTo = &originalID
	if err := s.logRepo.Update(ctx, result.SentLog); err != nil {
		return nil, err
	}

	// Also update original email's thread_id if it didn't have one
	if original.ThreadID == nil {
		original.ThreadID = threadID
		_ = s.logRepo.Update(ctx, original)
	}

	response := models.ToNotificationLogResponse(result.SentLog)
	return &response, nil
}

// UpdateAttachmentURLs updates the attachment URLs in an existing notification
func (s *NotificationService) UpdateAttachmentURLs(ctx context.Context, id uuid.UUID, attachments models.AttachmentArray) error {
	log, err := s.logRepo.FindByID(ctx, id)
	if err != nil {
		return err
	}

	log.Attachments = attachments
	now := time.Now()
	log.UpdatedAt = &now

	return s.logRepo.Update(ctx, log)
}

func (s *NotificationService) SetMetaOnLogs(ctx context.Context, ids []uuid.UUID, meta *models.NotificationMeta) error {
	return s.logRepo.SetMeta(ctx, ids, meta)
}

// placeholderRe matches {{key}} and {{.key}} patterns (key may contain letters, digits, underscores).
var placeholderRe = regexp.MustCompile(`\{\{\.?([A-Za-z0-9_]+)\}\}`)

// RenderTemplate substitutes variables into a template string.
// SendByActionType sends all active templates matching actionType+channel to the given recipients.
// Each active template is sent as a separate notification.
// Returns an error only when NO active templates are found, so callers can fall back to hardcoded content.
func (s *NotificationService) SendByActionType(
	ctx context.Context,
	actionType, channel, language string,
	to []string,
	variables map[string]string,
	sentBy *uuid.UUID,
) error {
	templates, err := s.templateRepo.FindActiveByActionTypeAndChannel(ctx, actionType, channel)
	if err != nil || len(templates) == 0 {
		return fmt.Errorf("no active %s templates found for channel %s", actionType, channel)
	}
	log.Printf("[SendByActionType] action=%s channel=%s lang=%s recipients=%v templates_found=%d", actionType, channel, language, to, len(templates))
	var lastErr error
	for _, tpl := range templates {
		code := tpl.Code
		log.Printf("[SendByActionType] sending template code=%s body_en_len=%d body_ar_len=%d", code, len(tpl.BodyEN), len(tpl.BodyAR))
		_, sendErr := s.SendNotification(
			ctx, channel, &code, language,
			to, nil, nil,
			"", "", variables, nil, sentBy, nil,
		)
		if sendErr != nil {
			log.Printf("[SendByActionType] template %s send failed: %v", code, sendErr)
			lastErr = sendErr
		} else {
			log.Printf("[SendByActionType] template %s sent OK", code)
		}
	}
	return lastErr
}

// Matching is case-insensitive: {{Incident_Number}} matches vars["incident_number"].
// Supports both {{variable_name}} and {{.variable_name}} syntax.
// Unknown variables are left unchanged in the output (never errors).
// Logs which variables were substituted and which placeholders remain unmapped.
func RenderTemplate(tpl string, vars map[string]string) (string, error) {
	// Build a lowercase-keyed copy for case-insensitive lookups.
	lower := make(map[string]string, len(vars))
	for k, v := range vars {
		lower[strings.ToLower(k)] = v
	}

	var substituted []string
	result := placeholderRe.ReplaceAllStringFunc(tpl, func(match string) string {
		// Extract inner key, strip optional leading dot.
		sub := placeholderRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		key := strings.ToLower(strings.TrimSpace(sub[1]))
		if v, ok := lower[key]; ok {
			substituted = append(substituted, sub[1]) // log original casing
			return v
		}
		return match // leave unrecognised placeholders as-is
	})

	// Detect any remaining unmapped placeholders.
	unmapped := placeholderRe.FindAllString(result, -1)
	if len(unmapped) > 0 {
		log.Printf("[RenderTemplate] WARNING: %d unmapped placeholder(s) remain: %v | substituted: %v",
			len(unmapped), unmapped, substituted)
	} else {
		log.Printf("[RenderTemplate] OK: substituted variables: %v", substituted)
	}

	return result, nil
}
