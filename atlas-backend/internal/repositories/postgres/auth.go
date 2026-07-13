package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/sunnyside/atlas/atlas-backend/internal/data/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/database"
	"github.com/sunnyside/atlas/atlas-backend/internal/repositories"
)

type AuthRepository struct {
	tx database.TxExecutor
}

// compile-time interface assertion.
var _ repositories.AuthRepository = (*AuthRepository)(nil)

// NewAuthRepository accepts only the TxExecutor created by TxManager.
// This repository never runs statements directly through the connection pool.
func NewAuthRepository(tx database.TxExecutor) *AuthRepository {
	return &AuthRepository{tx: tx}
}

func (r *AuthRepository) CreateRegistration(
	ctx context.Context,
	organizationInput models.NewOrganization,
	userInput models.NewUser,
	sessionInput models.NewSession,
) (models.Organization, models.User, error) {
	// All statements use the same transaction supplied by TxManager.
	var organization models.Organization
	err := r.tx.QueryRow(ctx, `
		INSERT INTO organizations (name, slug)
		VALUES ($1, $2)
		RETURNING id, name, slug, status
	`, organizationInput.Name, organizationInput.Slug).Scan(
		&organization.ID,
		&organization.Name,
		&organization.Slug,
		&organization.Status,
	)
	if err != nil {
		return models.Organization{}, models.User{}, mapDatabaseError("insert organization", err)
	}

	var user models.User
	err = r.tx.QueryRow(ctx, `
		INSERT INTO users (
			organization_id, email, normalized_email, display_name, password_hash, role
		)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, organization_id, email, display_name, role, status
	`,
		organization.ID,
		userInput.Email,
		userInput.NormalizedEmail,
		userInput.DisplayName,
		userInput.PasswordHash,
		userInput.Role,
	).Scan(
		&user.ID,
		&user.OrganizationID,
		&user.Email,
		&user.DisplayName,
		&user.Role,
		&user.Status,
	)
	if err != nil {
		return models.Organization{}, models.User{}, mapDatabaseError("insert first admin", err)
	}

	if _, err := r.tx.Exec(ctx, `
		INSERT INTO sessions (user_id, token_hash, device_name, created_at, last_seen_at, expires_at)
		VALUES ($1, $2, $3, $4, $4, $5)
	`, user.ID, sessionInput.TokenHash, sessionInput.DeviceName, sessionInput.Now, sessionInput.ExpiresAt); err != nil {
		return models.Organization{}, models.User{}, mapDatabaseError("insert initial session", err)
	}

	return organization, user, nil
}

func (r *AuthRepository) FindCredentialsByEmail(ctx context.Context, normalizedEmail string) (models.CredentialsRecord, error) {
	var record models.CredentialsRecord
	err := r.tx.QueryRow(ctx, `
		SELECT
			u.id, u.organization_id, u.email, u.display_name, u.role, u.status, u.password_hash,
			o.id, o.name, o.slug, o.status
		FROM users u
		JOIN organizations o ON o.id = u.organization_id
		WHERE u.normalized_email = $1
	`, normalizedEmail).Scan(
		&record.User.ID,
		&record.User.OrganizationID,
		&record.User.Email,
		&record.User.DisplayName,
		&record.User.Role,
		&record.User.Status,
		&record.PasswordHash,
		&record.Organization.ID,
		&record.Organization.Name,
		&record.Organization.Slug,
		&record.Organization.Status,
	)
	if err != nil {
		return models.CredentialsRecord{}, mapDatabaseError("find credentials", err)
	}
	return record, nil
}

func (r *AuthRepository) CreateSession(ctx context.Context, userID string, input models.NewSession) error {
	// Session creation and last-login tracking are one transaction-owned invariant.
	if _, err := r.tx.Exec(ctx, `
		INSERT INTO sessions (user_id, token_hash, device_name, created_at, last_seen_at, expires_at)
		VALUES ($1, $2, $3, $4, $4, $5)
	`, userID, input.TokenHash, input.DeviceName, input.Now, input.ExpiresAt); err != nil {
		return mapDatabaseError("insert session", err)
	}
	if _, err := r.tx.Exec(ctx, `
		UPDATE users SET last_login_at = $2, updated_at = $2 WHERE id = $1
	`, userID, input.Now); err != nil {
		return mapDatabaseError("update last login", err)
	}
	return nil
}

func (r *AuthRepository) FindSession(ctx context.Context, tokenHash []byte) (models.SessionRecord, error) {
	var record models.SessionRecord
	err := r.tx.QueryRow(ctx, `
		SELECT
			s.id, s.last_seen_at, s.expires_at, s.revoked_at,
			u.id, u.organization_id, u.email, u.display_name, u.role, u.status,
			o.id, o.name, o.slug, o.status
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		JOIN organizations o ON o.id = u.organization_id
		WHERE s.token_hash = $1
	`, tokenHash).Scan(
		&record.Principal.SessionID,
		&record.LastSeenAt,
		&record.ExpiresAt,
		&record.RevokedAt,
		&record.Principal.User.ID,
		&record.Principal.User.OrganizationID,
		&record.Principal.User.Email,
		&record.Principal.User.DisplayName,
		&record.Principal.User.Role,
		&record.Principal.User.Status,
		&record.Principal.Organization.ID,
		&record.Principal.Organization.Name,
		&record.Principal.Organization.Slug,
		&record.Principal.Organization.Status,
	)
	if err != nil {
		return models.SessionRecord{}, mapDatabaseError("find session", err)
	}
	return record, nil
}

func (r *AuthRepository) TouchSession(ctx context.Context, sessionID string, now time.Time) error {
	_, err := r.tx.Exec(ctx, `UPDATE sessions SET last_seen_at = $2 WHERE id = $1`, sessionID, now)
	if err != nil {
		return mapDatabaseError("touch session", err)
	}
	return nil
}

func (r *AuthRepository) RevokeSession(ctx context.Context, tokenHash []byte, now time.Time) error {
	_, err := r.tx.Exec(ctx, `
		UPDATE sessions
		SET revoked_at = COALESCE(revoked_at, $2)
		WHERE token_hash = $1
	`, tokenHash, now)
	if err != nil {
		return mapDatabaseError("revoke session", err)
	}
	return nil
}

func (r *AuthRepository) DeleteInactiveSessions(
	ctx context.Context,
	absoluteCutoff time.Time,
	idleCutoff time.Time,
	revokedCutoff time.Time,
) (int64, error) {
	result, err := r.tx.Exec(ctx, `
		DELETE FROM sessions
		WHERE expires_at < $1
		   OR last_seen_at < $2
		   OR (revoked_at IS NOT NULL AND revoked_at < $3)
	`, absoluteCutoff, idleCutoff, revokedCutoff)
	if err != nil {
		return 0, mapDatabaseError("delete inactive sessions", err)
	}
	return result.RowsAffected(), nil
}

func mapDatabaseError(operation string, err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return repositories.ErrNotFound
	}
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		switch postgresError.Code {
		case "23503", "23505":
			return repositories.ErrConflict
		}
	}
	return fmt.Errorf("%s: %w", operation, err)
}
