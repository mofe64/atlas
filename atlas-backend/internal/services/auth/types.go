package authservice

import (
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/data/models"
)

type RegistrationInput struct {
	OrganizationName string
	OrganizationSlug string
	DisplayName      string
	Email            string
	Password         string
	DeviceName       string
}

type LoginInput struct {
	Email      string
	Password   string
	DeviceName string
}

type Result struct {
	SessionToken string
	ExpiresAt    time.Time
	User         models.User
	Organization models.Organization
}
