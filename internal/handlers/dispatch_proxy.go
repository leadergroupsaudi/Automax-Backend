package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/pkg/utils"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// DispatchProxy forwards Dispatch Management calls to Automax Registry.
// Registry keeps the unit, unit type, and fire-station rows. This handler
// does not forward the caller's Authorization header.
type DispatchProxy struct {
	base   string
	client *http.Client
}

func NewDispatchProxy(baseURL string) *DispatchProxy {
	return &DispatchProxy{
		base:   strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

func (h *DispatchProxy) Forward(c *fiber.Ctx) error {
	return h.forwardTo(c, c.Params("*"))
}

func (h *DispatchProxy) forwardTo(c *fiber.Ctx, tail string) error {
	if h == nil || h.base == "" {
		return utils.ErrorResponse(c, fiber.StatusServiceUnavailable, "dispatch service is not configured")
	}
	tail = strings.Trim(tail, "/")
	target := h.base + "/dispatch"
	if tail != "" {
		target += "/" + tail
	}
	if query := c.Request().URI().QueryString(); len(query) > 0 {
		target += "?" + string(query)
	}
	req, err := http.NewRequestWithContext(c.UserContext(), c.Method(), target, bytes.NewReader(c.Body()))
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadGateway, "dispatch service is unavailable")
	}
	if contentType := c.Get(fiber.HeaderContentType); contentType != "" {
		req.Header.Set(fiber.HeaderContentType, contentType)
	}
	req.Header.Set(fiber.HeaderAccept, fiber.MIMEApplicationJSON)
	resp, err := h.client.Do(req)
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadGateway, "dispatch service is unavailable")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return utils.ErrorResponse(c, fiber.StatusBadGateway, "dispatch service is unavailable")
	}
	if contentType := resp.Header.Get(fiber.HeaderContentType); contentType != "" {
		c.Set(fiber.HeaderContentType, contentType)
	} else {
		c.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	}
	return c.Status(resp.StatusCode).Send(body)
}

// FetchUnitIncidentIDs asks Registry for the incident ids tied to a unit
// (Registry calls the unit a resource).
func (h *DispatchProxy) FetchUnitIncidentIDs(ctx context.Context, unitID string) ([]string, error) {
	if h == nil || h.base == "" {
		return nil, fmt.Errorf("dispatch service is not configured")
	}
	payload, err := json.Marshal(map[string]string{"resource_id": unitID})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.base+"/dispatch/resources/incidents", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	req.Header.Set(fiber.HeaderAccept, fiber.MIMEApplicationJSON)
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dispatch returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		Data struct {
			IncidentIDs []string `json:"incident_ids"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out.Data.IncidentIDs, nil
}

// DriverPusher sends a push notification to one user (satisfied by *services.FCMService).
type DriverPusher interface {
	Push(ctx context.Context, req *models.PushRequest) error
}

// ForwardDrivers forwards POST /dispatch/resources/drivers to Registry and,
// when Registry answers 200, pushes an FCM notification to every id in
// data.driver_user_ids. The client always gets Registry's response; push
// failures are only logged.
func (h *DispatchProxy) ForwardDrivers(pusher DriverPusher, incidents repository.IncidentRepository) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req struct {
			ServiceID  string `json:"service_id"`
			IncidentID string `json:"incident_id"`
		}
		_ = json.Unmarshal(c.Body(), &req)

		if err := h.forwardTo(c, "resources/drivers"); err != nil {
			return err
		}
		if c.Response().StatusCode() != fiber.StatusOK || pusher == nil || incidents == nil {
			return nil
		}
		incidentID, err := uuid.Parse(req.IncidentID)
		if err != nil {
			return nil
		}
		var out struct {
			Data struct {
				DriverUserIDs []string `json:"driver_user_ids"`
			} `json:"data"`
		}
		if err := json.Unmarshal(c.Response().Body(), &out); err != nil || len(out.Data.DriverUserIDs) == 0 {
			return nil
		}

		driverIDs := out.Data.DriverUserIDs
		serviceID := req.ServiceID
		go notifyDrivers(pusher, incidents, incidentID, serviceID, driverIDs)
		return nil
	}
}

func notifyDrivers(pusher DriverPusher, incidents repository.IncidentRepository, incidentID uuid.UUID, serviceID string, driverIDs []string) {
	ctx := context.Background()
	incident, err := incidents.FindByID(ctx, incidentID)
	if err != nil || incident == nil {
		log.Printf("FCM-DISPATCH: incident %s lookup failed: %v", incidentID, err)
		return
	}
	recordType := strings.ToUpper(incident.RecordType)
	for _, raw := range driverIDs {
		userID, err := uuid.Parse(raw)
		if err != nil {
			log.Printf("FCM-DISPATCH: skipping invalid driver id %q", raw)
			continue
		}
		pushReq := &models.PushRequest{
			UserID: userID,
			Title:  fmt.Sprintf("Dispatch: %s", incident.IncidentNumber),
			Body:   fmt.Sprintf("You have been assigned to dispatch for \"%s\".", incident.Title),
			Data: map[string]string{
				"id":         incidentID.String(),
				"type":       recordType,
				"service_id": serviceID,
			},
		}
		if err := pusher.Push(ctx, pushReq); err != nil {
			log.Printf("FCM-DISPATCH: failed for driver %s: %v", userID, err)
		} else {
			log.Printf("FCM-DISPATCH: sent to driver %s", userID)
		}
	}
}

// SetResourceIncident attaches a unit (Registry resource) to an incident, or
// detaches it when incidentID is nil.
func (h *DispatchProxy) SetResourceIncident(ctx context.Context, resourceID string, incidentID *uuid.UUID) error {
	if h == nil || h.base == "" {
		return fmt.Errorf("dispatch service is not configured")
	}
	payload, err := json.Marshal(map[string]*uuid.UUID{"incident_id": incidentID})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, h.base+"/dispatch/resources/"+url.PathEscape(resourceID)+"/incident", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set(fiber.HeaderContentType, fiber.MIMEApplicationJSON)
	req.Header.Set(fiber.HeaderAccept, fiber.MIMEApplicationJSON)
	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("dispatch returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
