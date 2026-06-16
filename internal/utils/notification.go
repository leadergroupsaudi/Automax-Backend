package utils

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/smtp"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/twilio/twilio-go"
	openapi "github.com/twilio/twilio-go/rest/api/v2010"
)

// smtpPool holds one reusable authenticated SMTP connection so that every
// outgoing email does NOT trigger a new SMTP AUTH handshake. Gmail counts
// each AUTH as a "login attempt" and rate-limits after too many in a short
// window — a single persistent connection avoids that entirely.
var smtpPool = struct {
	mu     sync.Mutex
	client *smtp.Client
}{}

// getOrCreateSMTPClient returns the live pooled client, or dials+authenticates
// a fresh one if the connection is absent or has gone stale (NOOP check).
func getOrCreateSMTPClient() (*smtp.Client, error) {
	smtpPool.mu.Lock()
	defer smtpPool.mu.Unlock()

	if smtpPool.client != nil {
		if err := smtpPool.client.Noop(); err == nil {
			return smtpPool.client, nil
		}
		smtpPool.client.Close()
		smtpPool.client = nil
	}

	host := os.Getenv("SMTP_HOST")
	port := os.Getenv("SMTP_PORT")
	user := os.Getenv("SMTP_USER")
	pass := os.Getenv("SMTP_PASS")

	c, err := smtp.Dial(fmt.Sprintf("%s:%s", host, port))
	if err != nil {
		return nil, fmt.Errorf("smtp dial: %w", err)
	}
	if err = c.StartTLS(&tls.Config{ServerName: host}); err != nil {
		c.Close()
		return nil, fmt.Errorf("smtp starttls: %w", err)
	}
	if err = c.Auth(smtp.PlainAuth("", user, pass, host)); err != nil {
		c.Close()
		return nil, fmt.Errorf("smtp auth: %w", err)
	}

	smtpPool.client = c
	return c, nil
}

// sendViaPool delivers msg using the persistent client.
// On any mid-send error the connection is discarded so the next call reconnects.
func sendViaPool(fromEmail string, recipients []string, msg []byte) error {
	smtpPool.mu.Lock()
	defer smtpPool.mu.Unlock()

	c := smtpPool.client
	if c == nil {
		return fmt.Errorf("smtp client not initialised")
	}

	reset := func(err error) error {
		smtpPool.client.Close()
		smtpPool.client = nil
		return err
	}

	if err := c.Mail(fromEmail); err != nil {
		return reset(fmt.Errorf("smtp MAIL FROM: %w", err))
	}
	for _, r := range recipients {
		if err := c.Rcpt(r); err != nil {
			return reset(fmt.Errorf("smtp RCPT TO %s: %w", r, err))
		}
	}
	w, err := c.Data()
	if err != nil {
		return reset(fmt.Errorf("smtp DATA: %w", err))
	}
	if _, err = w.Write(msg); err != nil {
		return reset(fmt.Errorf("smtp write: %w", err))
	}
	if err = w.Close(); err != nil {
		return reset(fmt.Errorf("smtp close data: %w", err))
	}
	_ = c.Reset() // prepare connection for the next message
	return nil
}

func SendOTPWithMetaTemplate(phone string, otp string) error {
	metaURL := os.Getenv("OTP_TEMPLATE_URL")
	accessToken := os.Getenv("OTP_TEMPLATE_ACCESS_TOKEN")

	if metaURL == "" {
		return fmt.Errorf("whatsapp config error: OTP_TEMPLATE_URL not set")
	}

	if accessToken == "" {
		return fmt.Errorf("whatsapp config error: OTP_TEMPLATE_ACCESS_TOKEN not set")
	}

	// Template payload
	payload := map[string]interface{}{
		"messaging_product": "whatsapp",
		"recipient_type":    "individual",
		"to":                phone,
		"type":              "template",
		"template": map[string]interface{}{
			"name": "otp_code",
			"language": map[string]interface{}{
				"code": "en_US",
			},
			"components": []map[string]interface{}{
				{
					"type": "body",
					"parameters": []map[string]interface{}{
						{
							"type": "text",
							"text": otp,
						},
					},
				},
				{
					"type":     "button",
					"sub_type": "url",
					"index":    "0",
					"parameters": []map[string]interface{}{
						{
							"type": "text",
							"text": "123456",
						},
					},
				},
			},
		},
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("whatsapp payload marshal failed: %w", err)
	}

	req, err := http.NewRequest("POST", metaURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("whatsapp request creation failed: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("whatsapp network error: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	return fmt.Errorf(
		"whatsapp http error (%d): %s",
		resp.StatusCode,
		string(bodyBytes),
	)
}

func SendWhatsApp(phone string, message string) error {

	metaURL := os.Getenv("METAURL")
	accessToken := os.Getenv("META_ACCESS_TOKEN")

	// Keys validation
	if metaURL == "" {
		return fmt.Errorf("whatsapp config error: METAURL not set")
	}

	if accessToken == "" {
		return fmt.Errorf("whatsapp config error: META_ACCESS_TOKEN not set")
	}

	// Request payload
	payload := map[string]interface{}{
		"messaging_product": "whatsapp",
		"recipient_type":    "individual",
		"to":                phone,
		"type":              "text",
		"text": map[string]interface{}{
			"preview_url": false,
			"body":        message,
		},
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("whatsapp payload marshal failed: %w", err)
	}

	req, err := http.NewRequest("POST", metaURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("whatsapp request creation failed: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("whatsapp network error: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)

	// Success case
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	// parsing structured Meta error
	var metaErr models.MetaErrorResponse
	if err := json.Unmarshal(bodyBytes, &metaErr); err == nil && metaErr.Error.Code != 0 {

		switch metaErr.Error.Code {

		case 100:
			return fmt.Errorf("whatsapp invalid parameter (code 100): %s", metaErr.Error.Message)

		case 190:
			return fmt.Errorf("whatsapp access token expired (code 190)")

		case 200:
			return fmt.Errorf("whatsapp permission denied (code 200)")

		case 2500:
			return fmt.Errorf("whatsapp invalid endpoint (code 2500): %s", metaErr.Error.Message)

		default:
			return fmt.Errorf(
				"whatsapp api error (code %d): %s | trace: %s",
				metaErr.Error.Code,
				metaErr.Error.Message,
				metaErr.Error.FBTraceID,
			)
		}
	}

	//If response is not structured JSON
	return fmt.Errorf(
		"whatsapp http error (%d): %s",
		resp.StatusCode,
		string(bodyBytes),
	)
}

// SendSMTPWithCCBCC sends email with TO, CC, BCC, and attachments support.
// It reuses a persistent SMTP connection (authenticated once) to avoid Gmail's
// "too many login attempts" rate limit.
func SendSMTPWithCCBCC(to []string, cc []string, bcc []string, subject, body string, attachments []models.AttachmentData) (models.RecipientArray, error) {
	fromEmail := os.Getenv("SMTP_FROM")
	fromName := os.Getenv("SMTP_FROM_NAME")

	// fromHeader = display name + address for the email From: header.
	// fromEmail  = bare address for the SMTP envelope (smtp requires no display name).
	fromHeader := fromEmail
	if fromName != "" {
		fromHeader = fmt.Sprintf("%s <%s>", fromName, fromEmail)
	}

	// Ensure the pooled connection is alive before building the message.
	if _, err := getOrCreateSMTPClient(); err != nil {
		var recipientStatuses models.RecipientArray
		for _, r := range append(append([]string{}, to...), append(cc, bcc...)...) {
			recipientStatuses = append(recipientStatuses, models.RecipientInfo{
				Email: r, Channel: r,
				Type: GetRecipientType(r, to, cc, bcc), Status: "failed", Error: err.Error(),
			})
		}
		return recipientStatuses, err
	}

	// Build recipient list for SMTP (TO + CC + BCC)
	allRecipients := make([]string, 0, len(to)+len(cc)+len(bcc))
	allRecipients = append(allRecipients, to...)
	allRecipients = append(allRecipients, cc...)
	allRecipients = append(allRecipients, bcc...)

	// Build email message with MIME multipart for attachments
	var msg bytes.Buffer
	boundary := "==_boundary_=="

	// Headers
	msg.WriteString(fmt.Sprintf("From: %s\r\n", fromHeader))
	if len(to) > 0 {
		msg.WriteString(fmt.Sprintf("To: %s\r\n", strings.Join(to, ", ")))
	}
	if len(cc) > 0 {
		msg.WriteString(fmt.Sprintf("Cc: %s\r\n", strings.Join(cc, ", ")))
	}
	// BCC is not included in headers (that's the point of BCC)
	msg.WriteString(fmt.Sprintf("Subject: %s\r\n", subject))
	// Use two boundaries: outer for mixed (body + attachments),
	// inner for alternative (plain text fallback + HTML).
	altBoundary := "==_alt_boundary_=="

	msg.WriteString("MIME-Version: 1.0\r\n")
	msg.WriteString(fmt.Sprintf("Content-Type: multipart/mixed; boundary=\"%s\"\r\n", boundary))
	msg.WriteString("\r\n")

	// --- body part: multipart/alternative (plain fallback + HTML) ---
	msg.WriteString(fmt.Sprintf("--%s\r\n", boundary))
	msg.WriteString(fmt.Sprintf("Content-Type: multipart/alternative; boundary=\"%s\"\r\n", altBoundary))
	msg.WriteString("\r\n")

	// Plain-text fallback (strip HTML tags for clients that don't render HTML)
	plainBody := stripHTMLTags(body)
	msg.WriteString(fmt.Sprintf("--%s\r\n", altBoundary))
	msg.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
	msg.WriteString("\r\n")
	msg.WriteString(plainBody)
	msg.WriteString("\r\n")

	// HTML body
	msg.WriteString(fmt.Sprintf("--%s\r\n", altBoundary))
	msg.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n")
	msg.WriteString("\r\n")
	msg.WriteString(body)
	msg.WriteString("\r\n")

	msg.WriteString(fmt.Sprintf("--%s--\r\n", altBoundary))
	msg.WriteString("\r\n")

	// Attachment parts
	for _, att := range attachments {
		msg.WriteString(fmt.Sprintf("--%s\r\n", boundary))
		msg.WriteString(fmt.Sprintf("Content-Type: %s\r\n", att.ContentType))
		msg.WriteString("Content-Transfer-Encoding: base64\r\n")
		msg.WriteString(fmt.Sprintf("Content-Disposition: attachment; filename=\"%s\"\r\n", att.Filename))
		msg.WriteString("\r\n")

		// Encode attachment data to base64
		encoded := base64.StdEncoding.EncodeToString(att.Data)
		// Split into 76-character lines as per RFC 2045
		for i := 0; i < len(encoded); i += 76 {
			end := i + 76
			if end > len(encoded) {
				end = len(encoded)
			}
			msg.WriteString(encoded[i:end])
			msg.WriteString("\r\n")
		}
	}

	msg.WriteString(fmt.Sprintf("--%s--\r\n", boundary))

	// Send via persistent connection — no new AUTH handshake per email.
	err := sendViaPool(fromEmail, allRecipients, msg.Bytes())

	// Build recipient status array
	var recipientStatuses models.RecipientArray
	if err != nil {
		// All failed
		for _, email := range allRecipients {
			recipientType := GetRecipientType(email, to, cc, bcc)
			recipientStatuses = append(recipientStatuses, models.RecipientInfo{
				Channel: email,
				Type:    recipientType,
				Status:  "failed",
				Error:   err.Error(),
			})
		}
		return recipientStatuses, err
	}

	// All succeeded
	for _, email := range allRecipients {
		recipientType := GetRecipientType(email, to, cc, bcc)
		recipientStatuses = append(recipientStatuses, models.RecipientInfo{
			Channel: email,
			Type:    recipientType,
			Status:  "success",
		})
	}

	return recipientStatuses, nil
}

func SendSMS(to, message string) error {

	accountSID := os.Getenv("TWILIO_ACCOUNT_SID")
	authToken := os.Getenv("TWILIO_AUTH_TOKEN")
	from := os.Getenv("TWILIO_PHONE_NUMBER")

	if accountSID == "" || authToken == "" || from == "" {
		fmt.Println("[DEBUG-SMS] ERROR: Twilio env vars missing")
		return fmt.Errorf("twilio env vars missing: TWILIO_ACCOUNT_SID / TWILIO_AUTH_TOKEN / TWILIO_PHONE_NUMBER")
	}

	// Normalize whitespace: trim each line, drop blank lines, collapse to single newlines.
	// Templates often contain indentation and extra blank lines that inflate UCS-2 segment count.
	if strings.TrimSpace(message) == "" {
		fmt.Printf("[DEBUG-SMS] ERROR: empty message body for recipient %s — skipping send\n", to)
		return fmt.Errorf("sms body is empty")
	}
	fmt.Printf("[DEBUG-SMS] Sending to=%s from=%s body_len=%d body=%q\n", to, from, len(message), message)

	client := twilio.NewRestClientWithParams(twilio.ClientParams{
		Username: accountSID,
		Password: authToken,
	})

	params := &openapi.CreateMessageParams{}
	params.SetTo(to)
	params.SetFrom(from)
	params.SetBody(message)

	resp, err := client.Api.CreateMessage(params)
	if err != nil {
		fmt.Printf("[DEBUG-SMS] Twilio API error to=%s: %v\n", to, err)
		return fmt.Errorf("twilio send sms error: %w", err)
	}

	if resp != nil && resp.Sid != nil {
		status := ""
		if resp.Status != nil {
			status = *resp.Status
		}
		fmt.Printf("[DEBUG-SMS] Accepted by Twilio: SID=%s status=%s to=%s\n", *resp.Sid, status, to)
	} else {
		fmt.Println("[DEBUG-SMS] SMS sent successfully! (No SID in response)")
	}
	return nil
}

// SendSMSViaMSG91 sends an SMS using the MSG91 REST API.
// Required env vars: MSG91_AUTH_KEY, MSG91_SENDER_ID
// Optional env var:  MSG91_ROUTE (default "4" = transactional)
func SendSMSViaMSG91(to, message string) error {
	authKey := os.Getenv("MSG91_AUTH_KEY")
	senderID := os.Getenv("MSG91_SENDER_ID")
	route := os.Getenv("MSG91_ROUTE")

	if authKey == "" || senderID == "" {
		fmt.Println("[DEBUG-SMS] ERROR: MSG91 env vars missing")
		return fmt.Errorf("msg91 env vars missing: MSG91_AUTH_KEY / MSG91_SENDER_ID")
	}
	if route == "" {
		route = "4"
	}

	if strings.TrimSpace(message) == "" {
		fmt.Printf("[DEBUG-SMS] ERROR: empty message body for recipient %s — skipping send\n", to)
		return fmt.Errorf("sms body is empty")
	}

	// MSG91 expects number without leading '+' but with country code (e.g. 919876543210)
	normalizedTo := strings.TrimPrefix(to, "+")

	fmt.Printf("[DEBUG-SMS] MSG91 sending to=%s sender=%s body_len=%d\n", to, senderID, len(message))

	payload := map[string]interface{}{
		"sender": senderID,
		"route":  route,
		"sms": []map[string]interface{}{
			{
				"message": message,
				"to":      []string{normalizedTo},
			},
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("msg91: failed to marshal request: %w", err)
	}

	req, err := http.NewRequest("POST", "https://api.msg91.com/api/v2/sendsms", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("msg91: failed to create request: %w", err)
	}
	req.Header.Set("authkey", authKey)
	req.Header.Set("Content-Type", "application/json")

	httpClient := &http.Client{Timeout: 15 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		fmt.Printf("[DEBUG-SMS] MSG91 API error to=%s: %v\n", to, err)
		return fmt.Errorf("msg91 send sms error: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	fmt.Printf("[DEBUG-SMS] MSG91 response status=%d body=%s to=%s\n", resp.StatusCode, string(respBody), to)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("msg91 send sms failed: status=%d body=%s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	if jsonErr := json.Unmarshal(respBody, &result); jsonErr == nil {
		if result.Type == "error" {
			return fmt.Errorf("msg91 send sms error: %s", result.Message)
		}
		fmt.Printf("[DEBUG-SMS] MSG91 success: %s to=%s\n", result.Message, to)
	}
	return nil
}

// DispatchSMS sends an SMS via MSG91 or Twilio based on the SMS_PROVIDER env var.
// Set SMS_PROVIDER=msg91 to use MSG91; any other value (or unset) uses Twilio.
func DispatchSMS(to, message string) error {
	if strings.EqualFold(os.Getenv("SMS_PROVIDER"), "msg91") {
		return SendSMSViaMSG91(to, message)
	}
	return SendSMS(to, message)
}

// normalizeSMSBody trims each line, drops blank lines, and strips leading/trailing
// whitespace from the whole message. This reduces UCS-2 segment count when templates
// contain indentation or extra blank lines.
func normalizeSMSBody(s string) string {
	lines := strings.Split(s, "\n")
	out := lines[:0]
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// stripHTMLTags removes all HTML tags from s, returning plain text.
func stripHTMLTags(s string) string {
	var result strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			result.WriteRune(r)
		}
	}
	return result.String()
}

// GetRecipientType determines if an email is in TO, CC, or BCC list
func GetRecipientType(email string, to []string, cc []string, bcc []string) string {
	if contains(to, email) {
		return "to"
	}
	if contains(cc, email) {
		return "cc"
	}
	if contains(bcc, email) {
		return "bcc"
	}
	return "to" // default
}

// Helper function to check if string slice contains a value
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
