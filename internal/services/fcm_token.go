package services

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"strings"
	"time"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"google.golang.org/api/option"
)

type FCMService struct {
	deviceTokenRepo  *repository.DeviceTokenRepository
	notificationRepo repository.NotificationLogRepository
}

func NewFCMService(deviceTokenRepo *repository.DeviceTokenRepository, notificationRepo repository.NotificationLogRepository) *FCMService {
	return &FCMService{
		deviceTokenRepo:  deviceTokenRepo,
		notificationRepo: notificationRepo,
	}
}

func (s *FCMService) RegisterDeviceToken(userID, token, deviceType string) error {
	existing, err := s.deviceTokenRepo.GetByToken(token)
	if err != nil {
		return err
	}

	// If token already exists → update
	if existing != nil {
		existing.UserID = userID
		existing.DeviceType = deviceType

		return s.deviceTokenRepo.Update(existing)
	}

	// If token does not exist → create
	newToken := &models.DeviceToken{
		ID:          uuid.New(),
		UserID:      userID,
		DeviceToken: token,
		DeviceType:  deviceType,
	}

	return s.deviceTokenRepo.Create(newToken)
}

func (s *FCMService) GetUserDeviceTokens(userID uuid.UUID) ([]models.DeviceToken, error) {
	return s.deviceTokenRepo.GetByUserID(userID)
}

func (s *FCMService) RemoveDeviceToken(userID uuid.UUID, token string) error {
	return s.deviceTokenRepo.DeleteByUserAndToken(userID, token)
}

func (svc *FCMService) Push(ctx context.Context, req *models.PushRequest) error {
	now := time.Now()

	// Convert data to JSON for log body
	dataJSON, _ := json.Marshal(req.Data)
	bodyWithData := req.Body
	if len(dataJSON) > 0 {
		bodyWithData = fmt.Sprintf("%s | %s", req.Body, string(dataJSON))
	}

	tokens, err := svc.deviceTokenRepo.GetByUserID(req.UserID)
	if err != nil {
		return fmt.Errorf("failed to fetch tokens: %w", err)
	}

	if len(tokens) == 0 {
		return fmt.Errorf("no FCM tokens found for user %s", req.UserID)
	}

	var pushErr error

	for _, t := range tokens {
		if err := svc.SendPushNotification(ctx, t.DeviceToken, req.Title, req.Body, req.Data); err != nil {
			log.Printf("Push failed: %v", err)
			pushErr = err
		}
	}

	status := models.StatusSent
	if pushErr != nil {
		status = models.StatusFailed
	}

	logEntry := &models.NotificationLog{
		Channel:      "push-notification",
		Direction:    models.DirectionOutbound,
		Category:     models.CategorySent,
		Status:       status,
		Subject:      req.Title,
		Body:         bodyWithData,
		Provider:     "firebase",
		TemplateCode: req.UserType,
		SentAt:       &now,
		SentBy:       req.SentBy,
		ReceivedBy:   &req.UserID,
	}

	if pushErr != nil {
		logEntry.ErrorMessage = pushErr.Error()
	}

	if err := svc.notificationRepo.Create(ctx, logEntry); err != nil {
		log.Println("failed to save notification log:", err)
	}

	return pushErr
}

func (svc *FCMService) SendPushNotification(ctx context.Context, token string, title string, body string, data map[string]string) error {

	_ = godotenv.Load()

	credsPath := os.Getenv("FIREBASE_CREDENTIAL_PATH")

	if credsPath == "" {
		return fmt.Errorf("FIREBASE_CREDENTIAL_PATH not set in environment")
	}

	// Read credentials
	credsBytes, err := ioutil.ReadFile(credsPath)
	if err != nil {
		return fmt.Errorf("failed to read Firebase credentials: %w", err)
	}

	var creds struct {
		ProjectID string `json:"project_id"`
	}

	if err := json.Unmarshal(credsBytes, &creds); err != nil {
		return fmt.Errorf("failed to parse Firebase credentials: %w", err)
	}

	if creds.ProjectID == "" {
		return fmt.Errorf("project_id not found in credentials file")
	}

	// Firebase App
	opt := option.WithCredentialsFile(credsPath)

	app, err := firebase.NewApp(ctx, &firebase.Config{
		ProjectID: creds.ProjectID,
	}, opt)

	if err != nil {
		return fmt.Errorf("failed to initialize Firebase app: %w", err)
	}

	client, err := app.Messaging(ctx)
	if err != nil {
		return fmt.Errorf("failed to get FCM client: %w", err)
	}

	// Firebase Message
	message := &messaging.Message{
		Token: token,

		Notification: &messaging.Notification{
			Title: title,
			Body:  body,
		},

		Android: &messaging.AndroidConfig{
			Notification: &messaging.AndroidNotification{
				ChannelID: "default",
				Sound:     "default",
			},
		},

		Data: data,
	}

	//Firebase Payload
	//payload, _ := json.MarshalIndent(message, "", "  ")

	// Send
	resp, err := client.Send(ctx, message)
	if err != nil {

		if strings.Contains(err.Error(), "registration-token-not-registered") {
			return fmt.Errorf("device unreachable or token expired")
		}
		return fmt.Errorf("failed to send FCM message: %w", err)
	}

	log.Println("Push sent successfully:", "Time:", time.Now(), "Response:", resp)
	return nil
}
