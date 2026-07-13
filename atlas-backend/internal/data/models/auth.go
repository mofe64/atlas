package models

import "time"

type Role string

const (
	RoleAdmin    Role = "admin"
	RoleOperator Role = "operator"
)

type Organization struct {
	ID     string
	Name   string
	Slug   string
	Status string
}

type User struct {
	ID             string
	OrganizationID string
	Email          string
	DisplayName    string
	Role           Role
	Status         string
}

// Principal is the server-derived identity attached to an authenticated request.
// Handlers must use this organization ID rather than trusting tenant IDs from clients.
type Principal struct {
	SessionID    string
	User         User
	Organization Organization
}

type SessionRecord struct {
	Principal  Principal
	LastSeenAt time.Time
	ExpiresAt  time.Time
	RevokedAt  *time.Time
}

type CredentialsRecord struct {
	User         User
	Organization Organization
	PasswordHash string
}

type NewOrganization struct {
	Name string
	Slug string
}

type NewUser struct {
	Email           string
	NormalizedEmail string
	DisplayName     string
	PasswordHash    string
	Role            Role
}

type NewSession struct {
	TokenHash  []byte
	DeviceName string
	Now        time.Time
	ExpiresAt  time.Time
}
