package repositories

import (
	"context"
	"errors"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/data/models"
)

var (
	ErrNotFound = errors.New("record not found")
	ErrConflict = errors.New("record conflicts with existing data")
)

// AuthRepository is always transaction-bound. TxManager creates it after
// beginning a transaction, so its reads and writes commit or roll back together.
type AuthRepository interface {
	CreateRegistration(
		context.Context,
		models.NewOrganization,
		models.NewUser,
		models.NewSession,
	) (models.Organization, models.User, error)
	FindCredentialsByEmail(context.Context, string) (models.CredentialsRecord, error)
	CreateSession(context.Context, string, models.NewSession) error
	FindSession(context.Context, []byte) (models.SessionRecord, error)
	TouchSession(context.Context, string, time.Time) error
	RevokeSession(context.Context, []byte, time.Time) error
	DeleteInactiveSessions(context.Context, time.Time, time.Time, time.Time) (int64, error)
}
