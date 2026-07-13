package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/config"
	"github.com/sunnyside/atlas/atlas-backend/internal/data/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repositories"
	authservice "github.com/sunnyside/atlas/atlas-backend/internal/services/auth"
)

type testPinger struct{ err error }

func (p testPinger) Ping(context.Context) error { return p.err }

type emptyAuthRepository struct{}

type testRepositories struct {
	auth repositories.AuthRepository
}

func (r testRepositories) Auth() repositories.AuthRepository {
	return r.auth
}

func (testRepositories) Drones() repositories.DroneRepository                             { return nil }
func (testRepositories) VehicleAgents() repositories.VehicleAgentRepository               { return nil }
func (testRepositories) VehicleAgentBindings() repositories.VehicleAgentBindingRepository { return nil }
func (testRepositories) CommunicationLinks() repositories.CommunicationLinkRepository     { return nil }
func (testRepositories) EnrollmentTokens() repositories.EnrollmentTokenRepository         { return nil }

type directTxManager struct {
	repositories repositories.Repositories
}

func (m directTxManager) WithinTransaction(
	ctx context.Context,
	work func(repositories.Repositories) error,
) error {
	return work(m.repositories)
}

func (emptyAuthRepository) CreateRegistration(context.Context, models.NewOrganization, models.NewUser, models.NewSession) (models.Organization, models.User, error) {
	return models.Organization{}, models.User{}, repositories.ErrNotFound
}
func (emptyAuthRepository) FindCredentialsByEmail(context.Context, string) (models.CredentialsRecord, error) {
	return models.CredentialsRecord{}, repositories.ErrNotFound
}
func (emptyAuthRepository) CreateSession(context.Context, string, models.NewSession) error {
	return nil
}
func (emptyAuthRepository) FindSession(context.Context, []byte) (models.SessionRecord, error) {
	return models.SessionRecord{}, repositories.ErrNotFound
}
func (emptyAuthRepository) TouchSession(context.Context, string, time.Time) error  { return nil }
func (emptyAuthRepository) RevokeSession(context.Context, []byte, time.Time) error { return nil }
func (emptyAuthRepository) DeleteInactiveSessions(context.Context, time.Time, time.Time, time.Time) (int64, error) {
	return 0, nil
}

func testRouter(t *testing.T) http.Handler {
	t.Helper()
	repository := emptyAuthRepository{}
	authService, err := authservice.NewService(directTxManager{
		repositories: testRepositories{auth: repository},
	}, authservice.ServiceConfig{
		IdleTimeout:           12 * time.Hour,
		AbsoluteSessionExpiry: 7 * 24 * time.Hour,
		SessionRetention:      30 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}

	router, err := NewRouter(config.Config{
		AllowedOrigins: []string{"tauri://localhost"},
	}, authService, testPinger{})
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

func TestReady(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()

	testRouter(t).ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
}

func TestMeRequiresAuthentication(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
	response := httptest.NewRecorder()

	testRouter(t).ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestRegistrationValidationReturnsStableReason(t *testing.T) {
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/auth/register",
		strings.NewReader(`{"organizationName":""}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	testRouter(t).ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
	var body apiError
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Code != "validation_failed" || body.Error.Reason != "required" || body.Error.Field != "organizationName" {
		t.Fatalf("error body = %#v", body.Error)
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
