package handlers

import (
	"context"
	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/automax/backend/internal/repository"
	"github.com/gofiber/fiber/v2"
)

func TestDispatchProxyForwardsMethodPathQueryAndBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/dispatch/resource-types/abc" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.URL.Query().Get("page") != "2" || r.URL.Query().Get("search") != "truck" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		if r.Method != http.MethodPut {
			t.Errorf("method %s", r.Method)
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("authorization was forwarded")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"name":"Truck"}` {
			t.Errorf("body %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true,"data":{"name":"Truck"}}`))
	}))
	t.Cleanup(upstream.Close)

	app := fiber.New()
	proxy := NewDispatchProxy(upstream.URL)
	app.Put("/api/v1/dispatch/*", proxy.Forward)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/dispatch/resource-types/abc?page=2&search=truck", strings.NewReader(`{"name":"Truck"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(raw), `"name":"Truck"`) {
		t.Fatalf("body %s", raw)
	}
}

func TestDispatchProxyReportsMissingRegistry(t *testing.T) {
	app := fiber.New()
	app.Get("/api/v1/dispatch/*", NewDispatchProxy("").Forward)
	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/dispatch/resources", nil), -1)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

func TestFetchUnitIncidentIDs(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.URL.Path != "/dispatch/resources/incidents" || string(body) != `{"resource_id":"u1"}` {
			t.Errorf("unexpected request %s %s %s", r.Method, r.URL.Path, body)
		}
		if strings.Contains(string(body), "bad") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"incident_ids":["a","b"]}}`))
	}))
	t.Cleanup(upstream.Close)

	ids, err := NewDispatchProxy(upstream.URL).FetchUnitIncidentIDs(context.Background(), "u1")
	if err != nil || len(ids) != 2 || ids[0] != "a" {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
	if _, err := NewDispatchProxy("").FetchUnitIncidentIDs(context.Background(), "u1"); err == nil {
		t.Fatal("expected error for unconfigured base")
	}
}

type fakePusher struct {
	mu   sync.Mutex
	reqs []*models.PushRequest
}

func (f *fakePusher) Push(_ context.Context, r *models.PushRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r)
	return nil
}

func (f *fakePusher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

type fakeIncidentRepo struct{ repository.IncidentRepository }

func (fakeIncidentRepo) FindByID(_ context.Context, id uuid.UUID) (*models.Incident, error) {
	return &models.Incident{IncidentNumber: "INC-1", Title: "Fire", RecordType: "incident"}, nil
}

func runDrivers(t *testing.T, status int, upstreamBody string) (*fakePusher, string) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/dispatch/resources/drivers" {
			t.Errorf("path %s", r.URL.Path)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(upstreamBody))
	}))
	t.Cleanup(upstream.Close)
	pusher := &fakePusher{}
	app := fiber.New()
	app.Post("/api/v1/dispatch/resources/drivers", NewDispatchProxy(upstream.URL).ForwardDrivers(pusher, fakeIncidentRepo{}))
	body := `{"service_id":"` + uuid.NewString() + `","incident_id":"` + uuid.NewString() + `"}`
	resp, err := app.Test(httptest.NewRequest(http.MethodPost, "/api/v1/dispatch/resources/drivers", strings.NewReader(body)))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	return pusher, string(got)
}

func TestForwardDriversPushesToEachDriver(t *testing.T) {
	ids := `{"success":true,"data":{"driver_user_ids":["` + uuid.NewString() + `","` + uuid.NewString() + `","bad"]}}`
	pusher, got := runDrivers(t, 200, ids)
	if got != ids {
		t.Errorf("response altered: %s", got)
	}
	deadline := time.Now().Add(2 * time.Second)
	for pusher.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if pusher.count() != 2 {
		t.Fatalf("pushes = %d, want 2", pusher.count())
	}
}

func TestForwardDriversNoPushOnUpstreamError(t *testing.T) {
	pusher, _ := runDrivers(t, 400, `{"success":false}`)
	time.Sleep(100 * time.Millisecond)
	if pusher.count() != 0 {
		t.Fatalf("pushes = %d, want 0", pusher.count())
	}
}

func TestSetResourceIncident(t *testing.T) {
	var gotBody string
	status := http.StatusOK
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		if r.Method != http.MethodPatch || r.URL.Path != "/dispatch/resources/u1/incident" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(upstream.Close)
	p := NewDispatchProxy(upstream.URL)

	id := uuid.New()
	if err := p.SetResourceIncident(context.Background(), "u1", &id); err != nil || gotBody != `{"incident_id":"`+id.String()+`"}` {
		t.Fatalf("uuid body=%s err=%v", gotBody, err)
	}
	if err := p.SetResourceIncident(context.Background(), "u1", nil); err != nil || gotBody != `{"incident_id":null}` {
		t.Fatalf("null body=%s err=%v", gotBody, err)
	}
	status = http.StatusNotFound
	if err := p.SetResourceIncident(context.Background(), "u1", &id); err == nil {
		t.Fatal("expected error on 404")
	}
}
