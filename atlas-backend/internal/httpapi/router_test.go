package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/config"
)

func testRouter(t *testing.T) http.Handler {
	t.Helper()

	router, err := NewRouter(config.Config{
		ShutdownTimeout: time.Second,
		AllowedOrigins:  []string{"tauri://localhost"},
	})
	if err != nil {
		t.Fatalf("NewRouter() error = %v", err)
	}
	return router
}

func TestHealth(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	response := httptest.NewRecorder()

	testRouter(t).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}

	var body map[string]string
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["status"] != "ok" || body["service"] != "atlas-backend" {
		t.Fatalf("body = %#v", body)
	}
}

func TestCORSAllowsConfiguredDesktopOrigin(t *testing.T) {
	request := httptest.NewRequest(http.MethodOptions, "/healthz", nil)
	request.Header.Set("Origin", "tauri://localhost")
	response := httptest.NewRecorder()

	testRouter(t).ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "tauri://localhost" {
		t.Fatalf("Access-Control-Allow-Origin = %q", got)
	}
}

func TestCORSDoesNotAuthorizeUnknownOrigin(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.Header.Set("Origin", "https://untrusted.example")
	response := httptest.NewRecorder()

	testRouter(t).ServeHTTP(response, request)

	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want empty", got)
	}
}
