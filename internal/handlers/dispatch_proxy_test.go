package handlers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
