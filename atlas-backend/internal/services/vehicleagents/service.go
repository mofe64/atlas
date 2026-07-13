// Package vehicleagents owns agent enrollment and agent-to-drone binding workflows.
package vehicleagents

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/data/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repositories"
	"github.com/sunnyside/atlas/atlas-backend/internal/validation"
)

var (
	ErrForbidden              = errors.New("vehicle-agent operation is forbidden")
	ErrEnrollmentTokenInvalid = errors.New("vehicle-agent enrollment token is invalid")
	ErrEnrollmentTokenExpired = errors.New("vehicle-agent enrollment token has expired")
	ErrEnrollmentTokenUsed    = errors.New("vehicle-agent enrollment token has already been used")
	ErrAgentAlreadyEnrolled   = errors.New("vehicle-agent installation is already enrolled")
	ErrDroneUnavailable       = errors.New("drone is unavailable for vehicle-agent binding")
	ErrAgentUnavailable       = errors.New("vehicle agent is unavailable for binding")
)

const enrollmentTokenPrefix = "atlas_enroll_"

type ServiceConfig struct {
	DefaultEnrollmentTTL time.Duration
	MaximumEnrollmentTTL time.Duration
	Now                  func() time.Time
}

type Service struct {
	txManager            repositories.TxManager
	defaultEnrollmentTTL time.Duration
	maximumEnrollmentTTL time.Duration
	now                  func() time.Time
}

type CreateEnrollmentTokenInput struct {
	ScopedDroneID string
	TTL           time.Duration
}

type EnrollmentTokenResult struct {
	Token             string
	EnrollmentTokenID string
	OrganizationID    string
	ScopedDroneID     string
	ExpiresAt         time.Time
}

type EnrollInput struct {
	Token             string
	RequestID         string
	InstallationID    string
	PublicKey         []byte
	AgentVersion      string
	ProtocolVersion   string
	DeviceProfile     models.DeviceProfile
	Capabilities      []string
	DroneName         string
	DroneSerialNumber string
	VehicleType       models.VehicleType
	Attachment        models.FlightControllerAttachment
}

type EnrollmentResult struct {
	Agent   models.VehicleAgent
	Drone   models.Drone
	Binding models.VehicleAgentBinding
}

type CreateBindingInput struct {
	VehicleAgentID string
	DroneID        string
	Attachment     models.FlightControllerAttachment
}

func NewService(txManager repositories.TxManager, cfg ServiceConfig) (*Service, error) {
	if txManager == nil {
		return nil, errors.New("vehicle-agent transaction manager is required")
	}
	if cfg.DefaultEnrollmentTTL == 0 {
		cfg.DefaultEnrollmentTTL = 24 * time.Hour
	}
	if cfg.MaximumEnrollmentTTL == 0 {
		cfg.MaximumEnrollmentTTL = 7 * 24 * time.Hour
	}
	if cfg.DefaultEnrollmentTTL <= 0 || cfg.MaximumEnrollmentTTL < cfg.DefaultEnrollmentTTL {
		return nil, errors.New("vehicle-agent enrollment token lifetimes are invalid")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{
		txManager:            txManager,
		defaultEnrollmentTTL: cfg.DefaultEnrollmentTTL,
		maximumEnrollmentTTL: cfg.MaximumEnrollmentTTL,
		now:                  cfg.Now,
	}, nil
}

func (s *Service) CreateEnrollmentToken(ctx context.Context, principal models.Principal, input CreateEnrollmentTokenInput) (EnrollmentTokenResult, error) {
	if !isActiveAdmin(principal) {
		return EnrollmentTokenResult{}, ErrForbidden
	}
	ttl := input.TTL
	if ttl == 0 {
		ttl = s.defaultEnrollmentTTL
	}
	if ttl <= 0 || ttl > s.maximumEnrollmentTTL {
		return EnrollmentTokenResult{}, &validation.Error{Field: "ttl", Code: validation.CodeInvalidFormat, Message: "enrollment token lifetime is outside the allowed range"}
	}
	rawToken, tokenHash, err := newEnrollmentToken()
	if err != nil {
		return EnrollmentTokenResult{}, err
	}
	now := s.now().UTC()
	var token models.EnrollmentToken
	err = s.txManager.WithinTransaction(ctx, func(repos repositories.Repositories) error {
		if input.ScopedDroneID != "" {
			drone, err := repos.Drones().FindByID(ctx, principal.Organization.ID, input.ScopedDroneID)
			if err != nil {
				return err
			}
			if drone.Status != models.DroneStatusActive {
				return ErrDroneUnavailable
			}
		}
		var err error
		token, err = repos.EnrollmentTokens().Create(ctx, models.NewEnrollmentToken{
			OrganizationID:  principal.Organization.ID,
			CreatedByUserID: principal.User.ID,
			TokenHash:       tokenHash,
			ScopedDroneID:   input.ScopedDroneID,
			Now:             now,
			ExpiresAt:       now.Add(ttl),
		})
		return err
	})
	if err != nil {
		return EnrollmentTokenResult{}, err
	}
	return EnrollmentTokenResult{
		Token:             rawToken,
		EnrollmentTokenID: token.ID,
		OrganizationID:    token.OrganizationID,
		ScopedDroneID:     token.ScopedDroneID,
		ExpiresAt:         token.ExpiresAt,
	}, nil
}

func (s *Service) Enroll(ctx context.Context, input EnrollInput) (EnrollmentResult, error) {
	if err := validateEnrollInput(&input); err != nil {
		return EnrollmentResult{}, err
	}
	tokenHash, err := enrollmentTokenHash(input.Token)
	if err != nil {
		return EnrollmentResult{}, ErrEnrollmentTokenInvalid
	}
	now := s.now().UTC()
	var result EnrollmentResult
	err = s.txManager.WithinTransaction(ctx, func(repos repositories.Repositories) error {
		token, err := repos.EnrollmentTokens().FindByHashForUpdate(ctx, tokenHash)
		if errors.Is(err, repositories.ErrNotFound) {
			return ErrEnrollmentTokenInvalid
		}
		if err != nil {
			return err
		}

		if token.UsedAt != nil {
			if token.EnrollmentRequestID != input.RequestID {
				return ErrEnrollmentTokenUsed
			}
			return replayEnrollment(ctx, repos, token, input, &result)
		}
		if !now.Before(token.ExpiresAt) {
			return ErrEnrollmentTokenExpired
		}

		if _, err := repos.VehicleAgents().FindByInstallationID(ctx, token.OrganizationID, input.InstallationID); err == nil {
			return ErrAgentAlreadyEnrolled
		} else if !errors.Is(err, repositories.ErrNotFound) {
			return err
		}

		drone, err := resolveEnrollmentDrone(ctx, repos, token, input, now)
		if err != nil {
			return err
		}
		agent, err := repos.VehicleAgents().Create(ctx, models.NewVehicleAgent{
			OrganizationID:      token.OrganizationID,
			InstallationID:      input.InstallationID,
			PublicKey:           append([]byte(nil), input.PublicKey...),
			AgentVersion:        input.AgentVersion,
			ProtocolVersion:     input.ProtocolVersion,
			DeviceProfile:       input.DeviceProfile,
			Capabilities:        append([]string(nil), input.Capabilities...),
			EnrollmentRequestID: input.RequestID,
			Now:                 now,
		})
		if err != nil {
			return err
		}
		bindingStatus := models.VehicleAgentBindingPending
		if input.Attachment.ObservedUID != "" {
			bindingStatus = models.VehicleAgentBindingActive
		}
		binding, err := repos.VehicleAgentBindings().Create(ctx, models.NewVehicleAgentBinding{
			OrganizationID: token.OrganizationID,
			VehicleAgentID: agent.ID,
			DroneID:        drone.ID,
			Status:         bindingStatus,
			Attachment:     input.Attachment,
			Now:            now,
		})
		if err != nil {
			return err
		}
		if err := repos.EnrollmentTokens().MarkUsed(ctx, token.ID, input.RequestID, agent.ID, binding.ID, now); err != nil {
			return err
		}
		result = EnrollmentResult{Agent: agent, Drone: drone, Binding: binding}
		return nil
	})
	return result, err
}

func (s *Service) CreateBinding(ctx context.Context, principal models.Principal, input CreateBindingInput) (models.VehicleAgentBinding, error) {
	if !isActiveAdmin(principal) {
		return models.VehicleAgentBinding{}, ErrForbidden
	}
	status := models.VehicleAgentBindingPending
	if strings.TrimSpace(input.Attachment.ObservedUID) != "" {
		status = models.VehicleAgentBindingActive
	}
	var binding models.VehicleAgentBinding
	err := s.txManager.WithinTransaction(ctx, func(repos repositories.Repositories) error {
		agent, err := repos.VehicleAgents().FindByID(ctx, principal.Organization.ID, input.VehicleAgentID)
		if err != nil {
			return err
		}
		if agent.Status != models.VehicleAgentStatusActive {
			return ErrAgentUnavailable
		}
		drone, err := repos.Drones().FindByID(ctx, principal.Organization.ID, input.DroneID)
		if err != nil {
			return err
		}
		if drone.Status != models.DroneStatusActive {
			return ErrDroneUnavailable
		}
		if input.Attachment.ObservedUID != "" {
			if _, err := repos.Drones().AssignIdentityIfUnset(ctx, principal.Organization.ID, drone.ID, models.DroneIdentity{
				FlightControllerUID: input.Attachment.ObservedUID,
				VehicleType:         drone.VehicleType,
				Now:                 s.now().UTC(),
			}); err != nil {
				return err
			}
		}
		binding, err = repos.VehicleAgentBindings().Create(ctx, models.NewVehicleAgentBinding{
			OrganizationID: principal.Organization.ID,
			VehicleAgentID: input.VehicleAgentID,
			DroneID:        input.DroneID,
			Status:         status,
			Attachment:     input.Attachment,
			Now:            s.now().UTC(),
		})
		return err
	})
	return binding, err
}

func (s *Service) EndBinding(ctx context.Context, principal models.Principal, bindingID, reason string) (models.VehicleAgentBinding, error) {
	if !isActiveAdmin(principal) {
		return models.VehicleAgentBinding{}, ErrForbidden
	}
	reason = strings.TrimSpace(reason)
	now := s.now().UTC()
	var binding models.VehicleAgentBinding
	err := s.txManager.WithinTransaction(ctx, func(repos repositories.Repositories) error {
		var err error
		binding, err = repos.VehicleAgentBindings().End(ctx, principal.Organization.ID, bindingID, reason, now)
		if err != nil {
			return err
		}
		return repos.CommunicationLinks().CloseOpenForBinding(ctx, principal.Organization.ID, bindingID, "binding ended: "+reason, now)
	})
	return binding, err
}

func resolveEnrollmentDrone(ctx context.Context, repos repositories.Repositories, token models.EnrollmentToken, input EnrollInput, now time.Time) (models.Drone, error) {
	if token.ScopedDroneID != "" {
		drone, err := repos.Drones().FindByID(ctx, token.OrganizationID, token.ScopedDroneID)
		if err != nil {
			return models.Drone{}, err
		}
		if drone.Status != models.DroneStatusActive {
			return models.Drone{}, ErrDroneUnavailable
		}
		if input.Attachment.ObservedUID == "" {
			return drone, nil
		}
		return repos.Drones().AssignIdentityIfUnset(ctx, token.OrganizationID, drone.ID, models.DroneIdentity{
			FlightControllerUID: input.Attachment.ObservedUID,
			SerialNumber:        input.DroneSerialNumber,
			VehicleType:         input.VehicleType,
			Now:                 now,
		})
	}
	if input.Attachment.ObservedUID == "" {
		return models.Drone{}, &validation.Error{Field: "attachment.observedUid", Code: validation.CodeRequired, Message: "flight-controller identity is required when the token is not scoped to a drone"}
	}
	drone, err := repos.Drones().FindByFlightControllerUID(ctx, token.OrganizationID, input.Attachment.ObservedUID)
	if err == nil {
		if drone.Status != models.DroneStatusActive {
			return models.Drone{}, ErrDroneUnavailable
		}
		return drone, nil
	}
	if !errors.Is(err, repositories.ErrNotFound) {
		return models.Drone{}, err
	}
	return repos.Drones().Create(ctx, models.NewDrone{
		OrganizationID:      token.OrganizationID,
		Name:                input.DroneName,
		FlightControllerUID: input.Attachment.ObservedUID,
		SerialNumber:        input.DroneSerialNumber,
		VehicleType:         input.VehicleType,
		Now:                 now,
	})
}

func replayEnrollment(ctx context.Context, repos repositories.Repositories, token models.EnrollmentToken, input EnrollInput, result *EnrollmentResult) error {
	agent, err := repos.VehicleAgents().FindByID(ctx, token.OrganizationID, token.EnrolledVehicleAgentID)
	if err != nil {
		return err
	}
	if agent.InstallationID != input.InstallationID || !bytes.Equal(agent.PublicKey, input.PublicKey) {
		return ErrEnrollmentTokenUsed
	}
	binding, err := repos.VehicleAgentBindings().FindByID(ctx, token.OrganizationID, token.EnrolledBindingID)
	if err != nil {
		return err
	}
	drone, err := repos.Drones().FindByID(ctx, token.OrganizationID, binding.DroneID)
	if err != nil {
		return err
	}
	*result = EnrollmentResult{Agent: agent, Drone: drone, Binding: binding}
	return nil
}

func validateEnrollInput(input *EnrollInput) error {
	input.Token = strings.TrimSpace(input.Token)
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.InstallationID = strings.TrimSpace(input.InstallationID)
	input.AgentVersion = strings.TrimSpace(input.AgentVersion)
	input.ProtocolVersion = strings.TrimSpace(input.ProtocolVersion)
	input.DroneName = strings.TrimSpace(input.DroneName)
	input.Attachment.ObservedUID = strings.TrimSpace(input.Attachment.ObservedUID)
	required := []struct {
		field string
		value string
	}{
		{field: "token", value: input.Token},
		{field: "requestId", value: input.RequestID},
		{field: "installationId", value: input.InstallationID},
		{field: "agentVersion", value: input.AgentVersion},
		{field: "protocolVersion", value: input.ProtocolVersion},
	}
	for _, item := range required {
		if item.value == "" {
			return &validation.Error{Field: item.field, Code: validation.CodeRequired, Message: item.field + " is required"}
		}
	}
	if len(input.PublicKey) != 32 {
		return &validation.Error{Field: "publicKey", Code: validation.CodeInvalidFormat, Message: "public key must be a 32-byte Ed25519 key"}
	}
	if input.VehicleType == "" {
		input.VehicleType = models.VehicleTypeUnknown
	}
	if !validVehicleType(input.VehicleType) {
		return &validation.Error{Field: "vehicleType", Code: validation.CodeInvalidFormat, Message: "vehicle type is not supported"}
	}
	if input.DroneName == "" {
		input.DroneName = input.DeviceProfile.DisplayName
	}
	if input.DroneName == "" {
		input.DroneName = "New Atlas drone"
	}
	if len(input.DroneName) > 120 {
		return &validation.Error{Field: "droneName", Code: validation.CodeTooLong, Message: "drone name must not exceed 120 characters"}
	}
	if input.Attachment.BaudRate < 0 || input.Attachment.SystemID < 0 || input.Attachment.SystemID > 255 ||
		input.Attachment.ComponentID < 0 || input.Attachment.ComponentID > 255 {
		return &validation.Error{Field: "attachment", Code: validation.CodeInvalidFormat, Message: "flight-controller attachment values are invalid"}
	}
	return nil
}

func newEnrollmentToken() (string, []byte, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", nil, err
	}
	raw := enrollmentTokenPrefix + base64.RawURLEncoding.EncodeToString(random)
	digest := sha256.Sum256([]byte(raw))
	return raw, digest[:], nil
}

func enrollmentTokenHash(raw string) ([]byte, error) {
	if !strings.HasPrefix(raw, enrollmentTokenPrefix) {
		return nil, ErrEnrollmentTokenInvalid
	}
	encoded := strings.TrimPrefix(raw, enrollmentTokenPrefix)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(decoded) != 32 {
		return nil, ErrEnrollmentTokenInvalid
	}
	digest := sha256.Sum256([]byte(raw))
	return digest[:], nil
}

func isActiveAdmin(principal models.Principal) bool {
	return principal.User.Role == models.RoleAdmin && principal.User.Status == "active" &&
		principal.Organization.Status == "active" && principal.User.OrganizationID == principal.Organization.ID
}

func validVehicleType(vehicleType models.VehicleType) bool {
	switch vehicleType {
	case models.VehicleTypeUnknown, models.VehicleTypeMulticopter,
		models.VehicleTypeVTOL:
		return true
	default:
		return false
	}
}
