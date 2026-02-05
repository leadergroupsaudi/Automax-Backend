package services

import (
	"bytes"
	"context"
	"fmt"
	"net/smtp"
	"os"
	"text/template"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/google/uuid"
)

type NotificationService struct {
	templateRepo repository.NotificationTemplateRepository
	logRepo      repository.NotificationLogRepository
}

func NewNotificationService(
	templateRepo repository.NotificationTemplateRepository,
	logRepo repository.NotificationLogRepository,
) *NotificationService {
	return &NotificationService{
		templateRepo: templateRepo,
		logRepo:      logRepo,
	}
}

func (s *NotificationService) SendNotification(
	ctx context.Context,
	channel, templateCode, language, recipient string,
	variables map[string]string,
) (*models.NotificationLog, error) {

	tpl, err := s.templateRepo.FindByCodeChannelLanguage(
		ctx, templateCode, channel, language,
	)
	if err != nil {
		return nil, err
	}

	body, err := RenderTemplate(tpl.Body, variables)
	if err != nil {
		return nil, err
	}

	subject := tpl.Subject
	if subject != "" {
		subject, _ = RenderTemplate(subject, variables)
	}

	status := "sent"
	provider := "smtp"
	if os.Getenv("ENV") == "local" {
		status = "mock-sent"
		provider = "mock"
	} else if channel == "email" {
		err := sendSMTP(recipient, subject, body)
		if err != nil {
			return nil, err
		}
	}

	log := &models.NotificationLog{
		ID:           uuid.New(),
		Channel:      channel,
		TemplateCode: templateCode,
		Language:     language,
		Recipient:    recipient,
		Subject:      subject,
		Body:         body,
		Status:       status,
		Provider:     provider,
	}

	if err := s.logRepo.Create(ctx, log); err != nil {
		return nil, err
	}

	// ✅ Return the log so handler can send it back in the API response
	return log, nil
}

// func (s *NotificationService) Send(
// 	ctx context.Context,
// 	channel, templateCode, language, recipient string,
// 	variables map[string]string,
// ) error {

// 	tpl, err := s.templateRepo.FindByCodeChannelLanguage(
// 		ctx, templateCode, channel, language,
// 	)
// 	if err != nil {
// 		return err
// 	}

// 	body, err := RenderTemplate(tpl.Body, variables)
// 	if err != nil {
// 		return err
// 	}

// 	subject := tpl.Subject
// 	if subject != "" {
// 		subject, _ = RenderTemplate(subject, variables)
// 	}

// 	status := "sent"
// 	provider := "smtp"

// 	if os.Getenv("ENV") == "local" {
// 		fmt.Println("local,", os.Getenv("ENV"), status, provider)
// 		status = "mock-sent"
// 		provider = "mock"
// 	} else if channel == "email" {
// 		// send real email via SMTP
// 		fmt.Println("channel", channel)
// 		err := sendSMTP(recipient, subject, body)
// 		if err != nil {
// 			return err
// 		}
// 	}

// 	// Save log
// 	log := &models.NotificationLog{
// 		ID:           uuid.New(),
// 		Channel:      channel,
// 		TemplateCode: templateCode,
// 		Language:     language,
// 		Recipient:    recipient,
// 		Subject:      subject,
// 		Body:         body,
// 		Status:       status,
// 		Provider:     provider,
// 	}

// 	return s.logRepo.Create(ctx, log)
// }

// func (s *NotificationService) Send(
// 	ctx context.Context,
// 	channel, templateCode, language, recipient string,
// 	variables map[string]string,
// ) error {

// 	tpl, err := s.templateRepo.FindByCodeChannelLanguage(
// 		ctx, templateCode, channel, language,
// 	)
// 	//fmt.Println("TPL.tpl,", tpl)
// 	if err != nil {
// 		return err
// 	}

// 	body, err := RenderTemplate(tpl.Body, variables)
// 	fmt.Println("TPL.body,", body, err)
// 	if err != nil {
// 		return err
// 	}

// 	subject := tpl.Subject
// 	if subject != "" {
// 		subject, _ = RenderTemplate(subject, variables)
// 	}

// 	status := "sent"
// 	provider := "smtp"
// 	fmt.Println("local,11", os.Getenv("ENV"), status, provider)
// 	if os.Getenv("ENV") == "local" {
// 		fmt.Println("local,", os.Getenv("ENV"), status, provider)
// 		status = "mock-sent"
// 		provider = "mock"
// 	}

// 	log := &models.NotificationLog{
// 		ID:           uuid.New(),
// 		Channel:      channel,
// 		TemplateCode: templateCode,
// 		Language:     language,
// 		Recipient:    recipient,
// 		Subject:      subject,
// 		Body:         body,
// 		Status:       status,
// 		Provider:     provider,
// 	}

// 	return s.logRepo.Create(ctx, log)
// }

// func RenderTemplate(tpl string, vars map[string]string) (string, error) {
// 	t, err := template.New("tpl").Parse(tpl)
// 	if err != nil {
// 		return "", err
// 	}
// 	var buf bytes.Buffer
// 	err = t.Execute(&buf, vars)
// 	return buf.String(), err
// }

func RenderTemplate(tpl string, vars map[string]string) (string, error) {
	t, err := template.New("tpl").Option("missingkey=zero").Parse(tpl)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	err = t.Execute(&buf, vars)
	return buf.String(), err
}

func sendSMTP(to, subject, body string) error {
	host := os.Getenv("SMTP_HOST")
	port := os.Getenv("SMTP_PORT")
	user := os.Getenv("SMTP_USER")
	pass := os.Getenv("SMTP_PASS")
	from := os.Getenv("SMTP_FROM")

	addr := fmt.Sprintf("%s:%s", host, port)
	auth := smtp.PlainAuth("", user, pass, host)

	msg := []byte(fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=\"UTF-8\"\r\n\r\n%s",
		from, to, subject, body))

	return smtp.SendMail(addr, auth, from, []string{to}, msg)
}
