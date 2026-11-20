package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/automax/backend/internal/models"
	"github.com/automax/backend/internal/repository"
	"github.com/automax/backend/internal/services"
	"github.com/automax/backend/pkg/constants"
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
	inApp  InAppNotifier // optional: also create in-app notifications for dispatched drivers
}

// SetInAppNotifier enables in-app notifications alongside FCM pushes.
func (h *DispatchProxy) SetInAppNotifier(n InAppNotifier) { h.inApp = n }

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

// ErrUnitDriverNotFound means Registry has no unit with that id, or the unit has no driver.
var ErrUnitDriverNotFound = errors.New("unit or driver not found")

// UnitInfo is the part of Registry's resource (unit) response that Automax uses.
type UnitInfo struct {
	ID           string `json:"id"`
	UniqueID     string `json:"unique_id"`
	PlateNumber  string `json:"plate_number"`
	DriverUserID string `json:"driver_user_id"`
}

// FetchUnit asks Registry for a unit (resource) and returns it. It returns
// ErrUnitDriverNotFound when the unit does not exist or has no driver.
func (h *DispatchProxy) FetchUnit(ctx context.Context, unitID string) (*UnitInfo, error) {
	if h == nil || h.base == "" {
		return nil, fmt.Errorf("dispatch service is not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.base+"/dispatch/resources/unique/"+url.PathEscape(unitID), nil)
	if err != nil {
		return nil, err
	}
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
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrUnitDriverNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dispatch returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		Data UnitInfo `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	if strings.TrimSpace(out.Data.DriverUserID) == "" {
		return nil, ErrUnitDriverNotFound
	}
	return &out.Data, nil
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
		performedBy, _ := c.Locals(constants.ContextKeys.UserID).(uuid.UUID)
		go func() {
			notifyDrivers(pusher, h.inApp, incidents, incidentID, serviceID, driverIDs)
			recordDispatchRevision(context.Background(), incidents, incidentID, performedBy, serviceID, driverIDs)
		}()
		return nil
	}
}

// recordDispatchRevision adds an incident revision noting that the dispatch
// notification was sent to a fire station service and its drivers.
func recordDispatchRevision(ctx context.Context, incidents repository.IncidentRepository, incidentID, performedBy uuid.UUID, serviceID string, driverIDs []string) {
	changes := []models.IncidentFieldChange{
		{
			FieldName:  "dispatch_service_id",
			FieldLabel: "Dispatch Service",
			NewValue:   &serviceID,
		},
	}
	drivers := strings.Join(driverIDs, ", ")
	changes = append(changes, models.IncidentFieldChange{
		FieldName:  "dispatch_driver_user_ids",
		FieldLabel: "Dispatched Drivers",
		NewValue:   &drivers,
	})
	changesJSON, err := json.Marshal(changes)
	if err != nil {
		log.Printf("REVISION-DISPATCH: marshal failed: %v", err)
		return
	}
	nextRev, err := incidents.GetNextRevisionNumber(ctx, incidentID)
	if err != nil {
		log.Printf("REVISION-DISPATCH: next revision number failed for incident %s: %v", incidentID, err)
		return
	}
	rev := &models.IncidentRevision{
		IncidentID:     incidentID,
		RevisionNumber: nextRev,
		ActionType:     models.RevisionActionFieldChange,
		ActionDescription: fmt.Sprintf("Dispatch notification sent to fire station service %s and drivers: %s",
			serviceID, drivers),
		Changes:       string(changesJSON),
		PerformedByID: performedBy,
	}
	if err := incidents.CreateRevision(ctx, rev); err != nil {
		log.Printf("REVISION-DISPATCH: create revision failed for incident %s: %v", incidentID, err)
	}
}

func notifyDrivers(pusher DriverPusher, inApp InAppNotifier, incidents repository.IncidentRepository, incidentID uuid.UUID, serviceID string, driverIDs []string) {
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
		if inApp != nil {
			subjectAr, bodyAr := services.DispatchAssignedTextsAr(incident.IncidentNumber, incident.Title)
			if err := inApp.Notify(ctx, userID, pushReq.Title, pushReq.Body, subjectAr, bodyAr,
				&models.NotificationMeta{ID: incidentID.String(), Type: recordType}); err != nil {
				log.Printf("INAPP-DISPATCH: failed for driver %s: %v", userID, err)
			} else {
				log.Printf("INAPP-DISPATCH: sent to driver %s", userID)
			}
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
