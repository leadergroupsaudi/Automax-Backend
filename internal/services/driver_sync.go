package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
)

// DriverSync stores a Driver-role user in the registry, which also sends the driver to Caguar.
type DriverSync interface {
	Ensure(ctx context.Context, userID uuid.UUID, name string) error
	Remove(ctx context.Context, userID uuid.UUID) (bool, error)
}

type registryDriverSync struct {
	base   string
	client *http.Client
}

func NewRegistryDriverSync(baseURL string) DriverSync {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil
	}
	return &registryDriverSync{
		base:   baseURL,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

// Ensure updates the registry driver for this user, or creates it when the registry has no row yet.
func (s *registryDriverSync) Ensure(ctx context.Context, userID uuid.UUID, name string) error {
	if s == nil || s.base == "" || userID == uuid.Nil {
		return fmt.Errorf("driver sync is not configured")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("driver name is required")
	}
	status, body, err := s.send(ctx, http.MethodPut, "/registry/drivers/unique/"+userID.String(), map[string]string{"name": name})
	if err != nil {
		return err
	}
	if status == http.StatusOK {
		return nil
	}
	if status != http.StatusNotFound {
		return fmt.Errorf("registry driver update status %d: %s", status, strings.TrimSpace(string(body)))
	}
	status, body, err = s.send(ctx, http.MethodPost, "/registry/drivers", map[string]string{
		"name":      name,
		"unique_id": userID.String(),
	})
	if err != nil {
		return err
	}
	if status != http.StatusCreated && status != http.StatusOK {
		return fmt.Errorf("registry driver create status %d: %s", status, strings.TrimSpace(string(body)))
	}
	return nil
}

// Remove deletes the registry driver for this user, which also deletes the Caguar driver.
// A user who was never stored as a driver is a success.
func (s *registryDriverSync) Remove(ctx context.Context, userID uuid.UUID) (bool, error) {
	if s == nil || s.base == "" || userID == uuid.Nil {
		return false, nil
	}
	status, body, err := s.send(ctx, http.MethodDelete, "/registry/drivers/unique/"+userID.String(), nil)
	if err != nil {
		return false, err
	}
	if status == http.StatusNotFound {
		return false, nil
	}
	if status == http.StatusOK || status == http.StatusNoContent {
		return true, nil
	}
	return false, fmt.Errorf("registry driver delete status %d: %s", status, strings.TrimSpace(string(body)))
}

func (s *registryDriverSync) send(ctx context.Context, method, path string, payload any) (int, []byte, error) {
	var raw []byte
	if payload != nil {
		var err error
		raw, err = json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, s.base+path, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, body, nil
}

func SetDriverSync(svc UserService, sync DriverSync) {
	if s, ok := svc.(*userService); ok {
		s.drivers = sync
	}
}

func (s *userService) syncDriver(ctx context.Context, user *models.User) {
	if s == nil || s.drivers == nil || user == nil || !userIsDriver(user) {
		return
	}
	name := driverDisplayName(user)
	if err := s.drivers.Ensure(ctx, user.ID, name); err != nil {
		log.Printf("backend: driver sync failed user_id=%s name=%s err=%v", user.ID, name, err)
		return
	}
	log.Printf("backend: driver synced user_id=%s name=%s", user.ID, name)
}

func (s *userService) removeDriver(ctx context.Context, userID uuid.UUID) {
	if s == nil || s.drivers == nil || userID == uuid.Nil {
		return
	}
	removed, err := s.drivers.Remove(ctx, userID)
	if err != nil {
		log.Printf("backend: driver delete failed user_id=%s err=%v", userID, err)
		return
	}
	if removed {
		log.Printf("backend: driver deleted user_id=%s", userID)
	}
}

func userIsDriver(user *models.User) bool {
	if user == nil {
		return false
	}
	for _, role := range user.Roles {
		if role.IsActive && strings.EqualFold(strings.TrimSpace(role.Code), "driver") {
			return true
		}
	}
	return false
}

func driverDisplayName(user *models.User) string {
	if user == nil {
		return ""
	}
	parts := make([]string, 0, 3)
	for _, part := range []string{user.FirstName, user.MiddleName, user.LastName} {
		if text := strings.TrimSpace(part); text != "" {
			parts = append(parts, text)
		}
	}
	name := strings.TrimSpace(strings.Join(parts, " "))
	if name == "" {
		name = strings.TrimSpace(user.Username)
	}
	return name
}
