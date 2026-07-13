// Package authservice owns authentication rules and use-case orchestration.
package authservice

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sunnyside/atlas/atlas-backend/internal/data/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repositories"
	"github.com/sunnyside/atlas/atlas-backend/internal/validation"
)

var (
	ErrInvalidCredentials  = errors.New("invalid email or password")
	ErrUnauthorized        = errors.New("session is invalid or expired")
	organizationSlugFormat = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
)

type ServiceConfig struct {
	IdleTimeout           time.Duration
	AbsoluteSessionExpiry time.Duration
	SessionRetention      time.Duration
	Now                   func() time.Time
}

type Service struct {
	txManager             repositories.TxManager
	idleTimeout           time.Duration
	absoluteSessionExpiry time.Duration
	sessionRetention      time.Duration
	now                   func() time.Time
	dummyPasswordHash     string
}

func NewService(txManager repositories.TxManager, cfg ServiceConfig) (*Service, error) {
	if txManager == nil {
		return nil, errors.New("authentication transaction manager is required")
	}
	if cfg.IdleTimeout <= 0 || cfg.AbsoluteSessionExpiry <= cfg.IdleTimeout || cfg.SessionRetention <= 0 {
		return nil, errors.New("session timeouts are invalid")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}

	// Missing-email login attempts still verify a real Argon2id hash. This makes
	// their timing closer to wrong-password attempts and reduces account discovery.
	dummyHash, err := hashPassword("this password is never accepted")
	if err != nil {
		return nil, err
	}

	return &Service{
		txManager:             txManager,
		idleTimeout:           cfg.IdleTimeout,
		absoluteSessionExpiry: cfg.AbsoluteSessionExpiry,
		sessionRetention:      cfg.SessionRetention,
		now:                   cfg.Now,
		dummyPasswordHash:     dummyHash,
	}, nil
}

func (s *Service) Register(ctx context.Context, input RegistrationInput) (Result, error) {
	input.OrganizationName = strings.TrimSpace(input.OrganizationName)
	input.OrganizationSlug = strings.ToLower(strings.TrimSpace(input.OrganizationSlug))
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Email = strings.TrimSpace(input.Email)
	normalizedEmail := normalizeEmail(input.Email)
	input.DeviceName = normalizeDeviceName(input.DeviceName)

	if err := validateRegistration(input, normalizedEmail); err != nil {
		return Result{}, err
	}
	passwordHash, err := hashPassword(input.Password)
	if err != nil {
		return Result{}, err
	}
	token, tokenHash, err := newSessionToken()
	if err != nil {
		return Result{}, err
	}

	now := s.now().UTC()
	expiresAt := now.Add(s.absoluteSessionExpiry)
	var organization models.Organization
	var user models.User
	err = s.txManager.WithinTransaction(ctx, func(repos repositories.Repositories) error {
		var transactionError error
		organization, user, transactionError = repos.Auth().CreateRegistration(
			ctx,
			models.NewOrganization{Name: input.OrganizationName, Slug: input.OrganizationSlug},
			models.NewUser{
				Email:           input.Email,
				NormalizedEmail: normalizedEmail,
				DisplayName:     input.DisplayName,
				PasswordHash:    passwordHash,
				Role:            models.RoleAdmin, // Registration can only create the first admin.
			},
			models.NewSession{TokenHash: tokenHash, DeviceName: input.DeviceName, Now: now, ExpiresAt: expiresAt},
		)
		return transactionError
	})
	if err != nil {
		return Result{}, err
	}

	return Result{
		SessionToken: token,
		ExpiresAt:    expiresAt,
		User:         user,
		Organization: organization,
	}, nil
}

func (s *Service) Login(ctx context.Context, input LoginInput) (Result, error) {
	normalizedEmail := normalizeEmail(input.Email)
	if normalizedEmail == "" || input.Password == "" {
		return Result{}, ErrInvalidCredentials
	}

	var record models.CredentialsRecord
	err := s.txManager.WithinTransaction(ctx, func(repos repositories.Repositories) error {
		var transactionError error
		record, transactionError = repos.Auth().FindCredentialsByEmail(ctx, normalizedEmail)
		return transactionError
	})
	if errors.Is(err, repositories.ErrNotFound) {
		_, _ = verifyPassword(input.Password, s.dummyPasswordHash)
		return Result{}, ErrInvalidCredentials
	}
	if err != nil {
		return Result{}, err
	}

	valid, err := verifyPassword(input.Password, record.PasswordHash)
	if err != nil {
		return Result{}, fmt.Errorf("verify stored password: %w", err)
	}
	if !valid || record.User.Status != "active" || record.Organization.Status != "active" {
		return Result{}, ErrInvalidCredentials
	}

	token, tokenHash, err := newSessionToken()
	if err != nil {
		return Result{}, err
	}
	now := s.now().UTC()
	expiresAt := now.Add(s.absoluteSessionExpiry)
	if err := s.txManager.WithinTransaction(ctx, func(repos repositories.Repositories) error {
		return repos.Auth().CreateSession(ctx, record.User.ID, models.NewSession{
			TokenHash:  tokenHash,
			DeviceName: normalizeDeviceName(input.DeviceName),
			Now:        now,
			ExpiresAt:  expiresAt,
		})
	}); err != nil {
		return Result{}, err
	}

	return Result{
		SessionToken: token,
		ExpiresAt:    expiresAt,
		User:         record.User,
		Organization: record.Organization,
	}, nil
}

func (s *Service) Authenticate(ctx context.Context, rawToken string) (models.Principal, error) {
	tokenHash, err := validateAndHashSessionToken(rawToken)
	if err != nil {
		return models.Principal{}, ErrUnauthorized
	}

	var principal models.Principal
	err = s.txManager.WithinTransaction(ctx, func(repos repositories.Repositories) error {
		repository := repos.Auth()
		record, transactionError := repository.FindSession(ctx, tokenHash)
		if errors.Is(transactionError, repositories.ErrNotFound) {
			return ErrUnauthorized
		}
		if transactionError != nil {
			return transactionError
		}

		now := s.now().UTC()
		if record.RevokedAt != nil || !now.Before(record.ExpiresAt) || now.Sub(record.LastSeenAt) >= s.idleTimeout {
			return ErrUnauthorized
		}
		if record.Principal.User.Status != "active" || record.Principal.Organization.Status != "active" {
			return ErrUnauthorized
		}

		// Touch at most once every five minutes. The read and optional write share
		// a transaction, so they operate on one consistent session record.
		if now.Sub(record.LastSeenAt) >= 5*time.Minute {
			if err := repository.TouchSession(ctx, record.Principal.SessionID, now); err != nil {
				return err
			}
		}
		principal = record.Principal
		return nil
	})
	if err != nil {
		return models.Principal{}, err
	}
	return principal, nil
}

func (s *Service) Logout(ctx context.Context, rawToken string) error {
	tokenHash, err := validateAndHashSessionToken(rawToken)
	if err != nil {
		return nil // Logout is intentionally idempotent for already-invalid sessions.
	}
	return s.txManager.WithinTransaction(ctx, func(repos repositories.Repositories) error {
		return repos.Auth().RevokeSession(ctx, tokenHash, s.now().UTC())
	})
}

// CleanupInactiveSessions removes only records that have been unusable for the
// configured retention period. Recent revoked/expired rows remain available for
// short-term incident investigation without growing the table forever.
func (s *Service) CleanupInactiveSessions(ctx context.Context) (int64, error) {
	now := s.now().UTC()
	var deleted int64
	err := s.txManager.WithinTransaction(ctx, func(repos repositories.Repositories) error {
		var transactionError error
		deleted, transactionError = repos.Auth().DeleteInactiveSessions(
			ctx,
			now.Add(-s.sessionRetention),
			now.Add(-s.idleTimeout-s.sessionRetention),
			now.Add(-s.sessionRetention),
		)
		return transactionError
	})
	return deleted, err
}

func normalizeEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizeDeviceName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "Atlas desktop"
	}
	if utf8.RuneCountInString(value) > 120 {
		return string([]rune(value)[:120])
	}
	return value
}

func validateRegistration(input RegistrationInput, normalizedEmail string) error {
	switch {
	case input.OrganizationName == "":
		return &validation.Error{Field: "organizationName", Code: validation.CodeRequired, Message: "is required"}
	case utf8.RuneCountInString(input.OrganizationName) > 200:
		return &validation.Error{Field: "organizationName", Code: validation.CodeTooLong, Message: "must contain no more than 200 characters"}
	case !organizationSlugFormat.MatchString(input.OrganizationSlug):
		return &validation.Error{Field: "organizationSlug", Code: validation.CodeInvalidFormat, Message: "must use lowercase letters, numbers, and single hyphens"}
	case input.DisplayName == "":
		return &validation.Error{Field: "displayName", Code: validation.CodeRequired, Message: "is required"}
	case utf8.RuneCountInString(input.DisplayName) > 120:
		return &validation.Error{Field: "displayName", Code: validation.CodeTooLong, Message: "must contain no more than 120 characters"}
	case normalizedEmail == "" || !strings.Contains(normalizedEmail, "@") || len(normalizedEmail) > 320:
		return &validation.Error{Field: "email", Code: validation.CodeInvalidFormat, Message: "must be a valid email address"}
	case utf8.RuneCountInString(input.Password) < 15:
		return &validation.Error{Field: "password", Code: validation.CodeTooShort, Message: "must contain at least 15 characters"}
	case utf8.RuneCountInString(input.Password) > 128 || len(input.Password) > 1024:
		return &validation.Error{Field: "password", Code: validation.CodeTooLong, Message: "must contain no more than 128 characters"}
	default:
		return nil
	}
}
