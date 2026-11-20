package services

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/automax/backend/internal/models"
	"github.com/google/uuid"
)

func TestUserIsDriverUsesRoleCode(t *testing.T) {
	user := &models.User{Roles: []models.Role{{Code: "Driver", IsActive: true}}}
	if !userIsDriver(user) {
		t.Fatal("expected driver")
	}
	user.Roles[0].Code = "agent"
	if userIsDriver(user) {
		t.Fatal("agent is not a driver")
	}
	if driverDisplayName(&models.User{FirstName: " Ali ", LastName: "Hassan"}) != "Ali Hassan" {
		t.Fatal("name")
	}
	if driverDisplayName(&models.User{Username: "ali"}) != "ali" {
		t.Fatal("username fallback")
	}
}

func TestRegistryDriverSyncCreatesWhenMissing(t *testing.T) {
	var created bool
	var gotName, gotUnique string
	userID := uuid.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/registry/drivers/unique/"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/registry/drivers":
			created = true
			gotName, gotUnique = string(body), r.URL.Path
			if !strings.Contains(string(body), userID.String()) || !strings.Contains(string(body), "Ali Hassan") {
				t.Errorf("body %s", body)
			}
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	sync := NewRegistryDriverSync(srv.URL)
	if err := sync.Ensure(context.Background(), userID, "Ali Hassan"); err != nil {
		t.Fatal(err)
	}
	if !created || !strings.Contains(gotName, "Ali Hassan") || gotUnique != "/registry/drivers" {
		t.Fatalf("created=%v name=%s path=%s", created, gotName, gotUnique)
	}
}

func TestRegistryDriverSyncUpdatesExisting(t *testing.T) {
	userID := uuid.New()
	var method, path, body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		method, path, body = r.Method, r.URL.Path, string(raw)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := NewRegistryDriverSync(srv.URL).Ensure(context.Background(), userID, "Ali H"); err != nil {
		t.Fatal(err)
	}
	if method != http.MethodPut || path != "/registry/drivers/unique/"+userID.String() || !strings.Contains(body, "Ali H") {
		t.Fatalf("%s %s %s", method, path, body)
	}
}

func TestRegistryDriverSyncRemoveIgnoresMissingDriver(t *testing.T) {
	userID := uuid.New()
	var method, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		path = r.URL.Path
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	removed, err := NewRegistryDriverSync(srv.URL).Remove(context.Background(), userID)
	if err != nil || removed {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	if method != http.MethodDelete || path != "/registry/drivers/unique/"+userID.String() {
		t.Fatalf("%s %s", method, path)
	}
}
