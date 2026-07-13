// Package communicationlinks owns live agent-to-Atlas session state.
package communicationlinks

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/data/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repositories"
	"github.com/sunnyside/atlas/atlas-backend/internal/validation"
)

var ErrBindingUnavailable = errors.New("vehicle-agent binding cannot open a communication link")

type Service struct {
	txManager repositories.TxManager
	now       func() time.Time
}

type OpenInput struct {
	OrganizationID        string
	VehicleAgentBindingID string
	SessionInstanceID     string
	Transport             string
	Roles                 []string
	RemoteAddress         string
	CommandEligible       bool
}

func NewService(txManager repositories.TxManager, now func() time.Time) (*Service, error) {
	if txManager == nil {
		return nil, errors.New("communication-link transaction manager is required")
	}
	if now == nil {
		now = time.Now
	}
	return &Service{txManager: txManager, now: now}, nil
}

// Open is called only after a future transport authenticates the agent. The
// transport must derive OrganizationID from that identity, never from payload.
func (s *Service) Open(ctx context.Context, input OpenInput) (models.CommunicationLink, error) {
	input.OrganizationID = strings.TrimSpace(input.OrganizationID)
	input.VehicleAgentBindingID = strings.TrimSpace(input.VehicleAgentBindingID)
	input.SessionInstanceID = strings.TrimSpace(input.SessionInstanceID)
	input.Transport = strings.ToLower(strings.TrimSpace(input.Transport))
	required := []struct {
		field string
		value string
	}{
		{field: "organizationId", value: input.OrganizationID},
		{field: "vehicleAgentBindingId", value: input.VehicleAgentBindingID},
		{field: "sessionInstanceId", value: input.SessionInstanceID},
		{field: "transport", value: input.Transport},
	}
	for _, item := range required {
		if item.value == "" {
			return models.CommunicationLink{}, &validation.Error{Field: item.field, Code: validation.CodeRequired, Message: item.field + " is required"}
		}
	}

	var link models.CommunicationLink
	err := s.txManager.WithinTransaction(ctx, func(repos repositories.Repositories) error {
		binding, err := repos.VehicleAgentBindings().FindByID(ctx, input.OrganizationID, input.VehicleAgentBindingID)
		if err != nil {
			return err
		}
		if binding.Status != models.VehicleAgentBindingActive && binding.Status != models.VehicleAgentBindingPending {
			return ErrBindingUnavailable
		}
		commandEligible := input.CommandEligible && binding.Status == models.VehicleAgentBindingActive

		existing, err := repos.CommunicationLinks().FindBySessionInstanceID(ctx, input.OrganizationID, input.VehicleAgentBindingID, input.SessionInstanceID)
		if err == nil {
			link = existing
			return nil
		}
		if !errors.Is(err, repositories.ErrNotFound) {
			return err
		}
		link, err = repos.CommunicationLinks().Create(ctx, models.NewCommunicationLink{
			OrganizationID:        input.OrganizationID,
			VehicleAgentBindingID: input.VehicleAgentBindingID,
			SessionInstanceID:     input.SessionInstanceID,
			Transport:             input.Transport,
			Roles:                 append([]string(nil), input.Roles...),
			RemoteAddress:         input.RemoteAddress,
			CommandEligible:       commandEligible,
			Now:                   s.now().UTC(),
		})
		return err
	})
	return link, err
}

func (s *Service) RecordHeartbeat(ctx context.Context, organizationID, linkID string, health models.CommunicationLinkHealth) (models.CommunicationLink, error) {
	if err := validateHealth(health); err != nil {
		return models.CommunicationLink{}, err
	}
	var link models.CommunicationLink
	err := s.txManager.WithinTransaction(ctx, func(repos repositories.Repositories) error {
		var err error
		link, err = repos.CommunicationLinks().RecordHeartbeat(ctx, organizationID, linkID, health, s.now().UTC())
		return err
	})
	return link, err
}

func (s *Service) Close(ctx context.Context, organizationID, linkID, reason string) (models.CommunicationLink, error) {
	var link models.CommunicationLink
	err := s.txManager.WithinTransaction(ctx, func(repos repositories.Repositories) error {
		var err error
		link, err = repos.CommunicationLinks().Close(ctx, organizationID, linkID, strings.TrimSpace(reason), s.now().UTC())
		return err
	})
	return link, err
}

func validateHealth(health models.CommunicationLinkHealth) error {
	if health.LatencyMS != nil && *health.LatencyMS < 0 {
		return &validation.Error{Field: "latencyMs", Code: validation.CodeInvalidFormat, Message: "latency cannot be negative"}
	}
	if health.PacketLossEstimate != nil && (*health.PacketLossEstimate < 0 || *health.PacketLossEstimate > 1) {
		return &validation.Error{Field: "packetLossEstimate", Code: validation.CodeInvalidFormat, Message: "packet loss must be between 0 and 1"}
	}
	if health.RXBytesPerSecond != nil && *health.RXBytesPerSecond < 0 {
		return &validation.Error{Field: "rxBytesPerSecond", Code: validation.CodeInvalidFormat, Message: "receive rate cannot be negative"}
	}
	if health.TXBytesPerSecond != nil && *health.TXBytesPerSecond < 0 {
		return &validation.Error{Field: "txBytesPerSecond", Code: validation.CodeInvalidFormat, Message: "transmit rate cannot be negative"}
	}
	return nil
}
