package postgres

import (
	"context"
	cryptorand "crypto/rand"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sunnyside/atlas/atlas-backend/internal/data/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/database"
	"github.com/sunnyside/atlas/atlas-backend/internal/repositories"
)

func TestAuthRepositoryLifecycle(t *testing.T) {
	databaseURL := os.Getenv("ATLAS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set ATLAS_TEST_DATABASE_URL to run PostgreSQL integration tests")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect to test PostgreSQL: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping test PostgreSQL: %v", err)
	}

	txManager := database.NewPostgresTxManager(pool, NewRepositories)
	createRegistration := func(
		organizationInput models.NewOrganization,
		userInput models.NewUser,
		sessionInput models.NewSession,
	) (models.Organization, models.User, error) {
		var organization models.Organization
		var user models.User
		err := txManager.WithinTransaction(ctx, func(repos repositories.Repositories) error {
			var transactionError error
			organization, user, transactionError = repos.Auth().CreateRegistration(
				ctx, organizationInput, userInput, sessionInput,
			)
			return transactionError
		})
		return organization, user, err
	}

	suffix := time.Now().UnixNano()
	email := fmt.Sprintf("integration-%d@example.com", suffix)
	slug := fmt.Sprintf("integration-%d", suffix)
	now := time.Now().UTC().Truncate(time.Microsecond)
	tokenHash := make([]byte, 32)
	if _, err := cryptorand.Read(tokenHash); err != nil {
		t.Fatalf("generate unique test token hash: %v", err)
	}

	organization, user, err := createRegistration(
		models.NewOrganization{Name: "Integration Organization", Slug: slug},
		models.NewUser{
			Email: email, NormalizedEmail: email, DisplayName: "Integration Admin",
			PasswordHash: "test-only-hash", Role: models.RoleAdmin,
		},
		models.NewSession{TokenHash: tokenHash, DeviceName: "integration test", Now: now, ExpiresAt: now.Add(time.Hour)},
	)
	if err != nil {
		t.Fatalf("CreateRegistration() error = %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM sessions WHERE user_id = $1", user.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", user.ID)
		_, _ = pool.Exec(ctx, "DELETE FROM organizations WHERE id = $1", organization.ID)
	})

	var credentials models.CredentialsRecord
	err = txManager.WithinTransaction(ctx, func(repos repositories.Repositories) error {
		var transactionError error
		credentials, transactionError = repos.Auth().FindCredentialsByEmail(ctx, email)
		return transactionError
	})
	if err != nil {
		t.Fatalf("FindCredentialsByEmail() error = %v", err)
	}
	if credentials.User.Role != models.RoleAdmin || credentials.Organization.ID != organization.ID {
		t.Fatalf("credentials do not match registration: %#v", credentials)
	}

	var session models.SessionRecord
	err = txManager.WithinTransaction(ctx, func(repos repositories.Repositories) error {
		var transactionError error
		session, transactionError = repos.Auth().FindSession(ctx, tokenHash)
		return transactionError
	})
	if err != nil {
		t.Fatalf("FindSession() error = %v", err)
	}
	if session.Principal.User.ID != user.ID || session.Principal.Organization.ID != organization.ID {
		t.Fatalf("session principal does not match registration: %#v", session)
	}

	var revoked models.SessionRecord
	err = txManager.WithinTransaction(ctx, func(repos repositories.Repositories) error {
		repository := repos.Auth()
		if err := repository.RevokeSession(ctx, tokenHash, now.Add(time.Minute)); err != nil {
			return err
		}
		var transactionError error
		revoked, transactionError = repository.FindSession(ctx, tokenHash)
		return transactionError
	})
	if err != nil || revoked.RevokedAt == nil {
		t.Fatalf("revoked session = %#v, error = %v", revoked, err)
	}

	// A globally unique normalized email keeps email/password login unambiguous.
	conflictingSlug := slug + "-other"
	_, _, err = createRegistration(
		models.NewOrganization{Name: "Conflicting Organization", Slug: conflictingSlug},
		models.NewUser{
			Email: email, NormalizedEmail: email, DisplayName: "Other Admin",
			PasswordHash: "test-only-hash", Role: models.RoleAdmin,
		},
		models.NewSession{TokenHash: make([]byte, 32), DeviceName: "integration test", Now: now, ExpiresAt: now.Add(time.Hour)},
	)
	if !errors.Is(err, repositories.ErrConflict) {
		t.Fatalf("duplicate email error = %v, want repositories.ErrConflict", err)
	}

	// The organization insert happened before the conflicting user insert. A
	// count of zero proves TxManager rolled the earlier write back.
	var leakedOrganizations int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM organizations WHERE slug = $1`, conflictingSlug).Scan(&leakedOrganizations); err != nil {
		t.Fatalf("count rolled-back organization: %v", err)
	}
	if leakedOrganizations != 0 {
		t.Fatalf("rolled-back organization count = %d, want 0", leakedOrganizations)
	}
}
