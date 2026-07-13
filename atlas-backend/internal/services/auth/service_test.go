package authservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/data/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repositories"
	"github.com/sunnyside/atlas/atlas-backend/internal/validation"
)

type fakeRepository struct {
	registrationOrganization models.NewOrganization
	registrationUser         models.NewUser
	registrationSession      models.NewSession
	credentials              models.CredentialsRecord
	credentialsErr           error
	session                  models.SessionRecord
	sessionErr               error
	createdSession           models.NewSession
	touchedSessionID         string
	revoked                  bool
}

type fakeRepositories struct {
	auth repositories.AuthRepository
}

func (r fakeRepositories) Auth() repositories.AuthRepository {
	return r.auth
}

func (fakeRepositories) Drones() repositories.DroneRepository                             { return nil }
func (fakeRepositories) VehicleAgents() repositories.VehicleAgentRepository               { return nil }
func (fakeRepositories) VehicleAgentBindings() repositories.VehicleAgentBindingRepository { return nil }
func (fakeRepositories) CommunicationLinks() repositories.CommunicationLinkRepository     { return nil }
func (fakeRepositories) EnrollmentTokens() repositories.EnrollmentTokenRepository         { return nil }

// fakeTxManager runs the callback directly for service tests. PostgreSQL
// integration tests cover real commit and rollback behavior.
type fakeTxManager struct {
	repositories repositories.Repositories
}

func (m fakeTxManager) WithinTransaction(
	ctx context.Context,
	work func(repositories.Repositories) error,
) error {
	return work(m.repositories)
}

func (r *fakeRepository) CreateRegistration(
	_ context.Context,
	organizationInput models.NewOrganization,
	userInput models.NewUser,
	sessionInput models.NewSession,
) (models.Organization, models.User, error) {
	r.registrationOrganization = organizationInput
	r.registrationUser = userInput
	r.registrationSession = sessionInput
	organization := models.Organization{
		ID: "org-1", Name: organizationInput.Name, Slug: organizationInput.Slug, Status: "active",
	}
	user := models.User{
		ID: "user-1", OrganizationID: organization.ID, Email: userInput.Email,
		DisplayName: userInput.DisplayName, Role: userInput.Role, Status: "active",
	}
	return organization, user, nil
}

func (r *fakeRepository) FindCredentialsByEmail(context.Context, string) (models.CredentialsRecord, error) {
	return r.credentials, r.credentialsErr
}

func (r *fakeRepository) CreateSession(_ context.Context, _ string, session models.NewSession) error {
	r.createdSession = session
	return nil
}

func (r *fakeRepository) FindSession(context.Context, []byte) (models.SessionRecord, error) {
	return r.session, r.sessionErr
}

func (r *fakeRepository) TouchSession(_ context.Context, sessionID string, _ time.Time) error {
	r.touchedSessionID = sessionID
	return nil
}

func (r *fakeRepository) RevokeSession(context.Context, []byte, time.Time) error {
	r.revoked = true
	return nil
}

func (r *fakeRepository) DeleteInactiveSessions(
	context.Context,
	time.Time,
	time.Time,
	time.Time,
) (int64, error) {
	return 0, nil
}

func newTestService(t *testing.T, repository repositories.AuthRepository, now time.Time) *Service {
	t.Helper()
	service, err := NewService(
		fakeTxManager{repositories: fakeRepositories{auth: repository}},
		ServiceConfig{
			IdleTimeout:           12 * time.Hour,
			AbsoluteSessionExpiry: 7 * 24 * time.Hour,
			SessionRetention:      30 * 24 * time.Hour,
			Now:                   func() time.Time { return now },
		},
	)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

func TestRegisterCreatesFirstAdminAndSession(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	repository := &fakeRepository{}
	service := newTestService(t, repository, now)

	result, err := service.Register(context.Background(), RegistrationInput{
		OrganizationName: " Sunnyside ",
		OrganizationSlug: "SUNNYSIDE",
		DisplayName:      " Atlas Admin ",
		Email:            " Admin@Example.com ",
		Password:         "a sufficiently long passphrase",
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if repository.registrationUser.Role != models.RoleAdmin {
		t.Fatalf("first role = %q, want admin", repository.registrationUser.Role)
	}
	if repository.registrationUser.NormalizedEmail != "admin@example.com" || repository.registrationOrganization.Slug != "sunnyside" {
		t.Fatalf("inputs were not normalized: %#v %#v", repository.registrationUser, repository.registrationOrganization)
	}
	if result.SessionToken == "" || len(repository.registrationSession.TokenHash) != 32 {
		t.Fatal("registration did not create a raw token and stored digest")
	}
	if !result.ExpiresAt.Equal(now.Add(7 * 24 * time.Hour)) {
		t.Fatalf("ExpiresAt = %s", result.ExpiresAt)
	}
}

func TestRegisterReturnsSharedValidationError(t *testing.T) {
	service := newTestService(t, &fakeRepository{}, time.Now())

	_, err := service.Register(context.Background(), RegistrationInput{
		OrganizationName: "Sunnyside",
		OrganizationSlug: "sunnyside",
		DisplayName:      "Atlas Admin",
		Email:            "admin@example.com",
		Password:         "short",
	})

	var validationError *validation.Error
	if !errors.As(err, &validationError) {
		t.Fatalf("Register() error = %v, want *validation.Error", err)
	}
	if validationError.Field != "password" || validationError.Code != validation.CodeTooShort {
		t.Fatalf("validation error = %#v", validationError)
	}
}

func TestLoginUsesGenericInvalidCredentialsError(t *testing.T) {
	repository := &fakeRepository{credentialsErr: repositories.ErrNotFound}
	service := newTestService(t, repository, time.Now())

	_, err := service.Login(context.Background(), LoginInput{
		Email: "missing@example.com", Password: "wrong",
	})
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login() error = %v, want ErrInvalidCredentials", err)
	}
}

func TestAuthenticateEnforcesIdleAndAbsoluteExpiry(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	rawToken, _, err := newSessionToken()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		lastSeenAt time.Time
		expiresAt  time.Time
		wantError  bool
	}{
		{name: "active", lastSeenAt: now.Add(-time.Hour), expiresAt: now.Add(time.Hour)},
		{name: "idle", lastSeenAt: now.Add(-12 * time.Hour), expiresAt: now.Add(time.Hour), wantError: true},
		{name: "absolute", lastSeenAt: now.Add(-time.Hour), expiresAt: now, wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repository := &fakeRepository{session: models.SessionRecord{
				Principal: models.Principal{
					SessionID:    "session-1",
					User:         models.User{ID: "user-1", Status: "active"},
					Organization: models.Organization{ID: "org-1", Status: "active"},
				},
				LastSeenAt: test.lastSeenAt,
				ExpiresAt:  test.expiresAt,
			}}
			service := newTestService(t, repository, now)
			_, err := service.Authenticate(context.Background(), rawToken)
			if test.wantError != errors.Is(err, ErrUnauthorized) {
				t.Fatalf("Authenticate() error = %v, wantUnauthorized=%v", err, test.wantError)
			}
		})
	}
}
