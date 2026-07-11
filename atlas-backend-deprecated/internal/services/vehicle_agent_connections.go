package services

import (
	"context"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repository"
)

// VehicleAgentConnectionService manages runtime backend-to-agent stream records.
//
// The service deliberately sits beside VehicleAgentService: VehicleAgentService
// owns the long-lived agent identity, while this service owns per-stream rows
// and the communication link that explains how that stream is currently
// reaching Atlas.
type VehicleAgentConnectionService struct {
	txManager repository.TxManager
}

func NewVehicleAgentConnectionService(txManager repository.TxManager) *VehicleAgentConnectionService {
	return &VehicleAgentConnectionService{txManager: txManager}
}

// OpenDroneVehicleAgentConnection records a newly accepted vehicle-agent gRPC
// stream and creates the network-path communication link for it.
//
// Roles describe which Atlas traffic planes this path is allowed to carry. The
// current protobuf stream handles telemetry, commands, and status messages; the
// same vehicle-agent network path can later support video and gimbal-control
// flows without changing the connection/link relationship.
func (s *VehicleAgentConnectionService) OpenDroneVehicleAgentConnection(ctx context.Context, input repository.OpenDroneVehicleAgentConnectionInput, now time.Time) (models.DroneVehicleAgentConnection, models.CommunicationLink, error) {
	var connection models.DroneVehicleAgentConnection
	var link models.CommunicationLink
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		connectionID, err := repos.DroneVehicleAgentConnections.GenerateDroneVehicleAgentConnectionID(ctx)
		if err != nil {
			return err
		}

		connection = models.DroneVehicleAgentConnection{
			ID:                  connectionID,
			VehicleAgentID:      input.VehicleAgentID,
			DroneID:             input.DroneID,
			ConnectionID:        connectionID,
			Transport:           "GRPC",
			RemoteAddress:       input.RemoteAddress,
			Status:              models.DroneVehicleAgentConnectionConnected,
			StartedAt:           now,
			LastHeartbeatAt:     now,
			VehicleAgentVersion: input.VehicleAgentVersion,
		}
		if err := repos.DroneVehicleAgentConnections.InsertDroneVehicleAgentConnection(ctx, connection); err != nil {
			return err
		}

		linkID, err := repos.CommunicationLinks.GenerateCommunicationLinkID(ctx)
		if err != nil {
			return err
		}

		linkType := input.LinkType
		if linkType == "" {
			linkType = models.CommunicationLinkVehicleAgentGRPC
		}
		link = models.CommunicationLink{
			ID:                            linkID,
			DroneID:                       input.DroneID,
			VehicleAgentID:                input.VehicleAgentID,
			DroneVehicleAgentConnectionID: connection.ID,
			LinkType:                      linkType,
			Roles: []models.CommunicationLinkRole{
				models.CommunicationLinkRoleTelemetry,
				models.CommunicationLinkRoleCommand,
				models.CommunicationLinkRoleVideo,
				models.CommunicationLinkRoleGimbalControl,
			},
			Status:              models.CommunicationLinkStatusConnected,
			Transport:           "GRPC",
			EndpointDescription: "vehicle-agent gRPC network path",
			CommandEligible:     true,
			LastSeenAt:          now,
			CreatedAt:           now,
		}
		if err := repos.CommunicationLinks.InsertCommunicationLink(ctx, link); err != nil {
			return err
		}

		_, err = repos.VehicleAgents.SetCommandChannelState(ctx, input.VehicleAgentID, models.CommandChannelConnected, now)
		return err
	})
	return connection, link, err
}

// RecordDroneVehicleAgentConnectionHeartbeat refreshes both the durable agent
// heartbeat and the per-connection/link heartbeat. One incoming heartbeat tells
// Atlas that the identity, active stream, and communication path are all still
// alive, so they are updated inside one transaction.
func (s *VehicleAgentConnectionService) RecordDroneVehicleAgentConnectionHeartbeat(ctx context.Context, connectionID string, input repository.VehicleAgentHeartbeatInput, now time.Time) (models.VehicleAgent, error) {
	var agent models.VehicleAgent
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		agent, err = repos.VehicleAgents.UpdateVehicleAgentHeartbeat(ctx, input, now)
		if err != nil {
			return err
		}
		if err := repos.Drones.UpdateDroneLastSeen(ctx, agent.DroneID, now); err != nil {
			return err
		}
		if _, err := repos.DroneVehicleAgentConnections.UpdateDroneVehicleAgentConnectionHeartbeat(ctx, connectionID, now); err != nil {
			return err
		}
		return repos.CommunicationLinks.TouchCommunicationLinksForDroneVehicleAgentConnection(ctx, connectionID, now)
	})
	return agent, err
}

// CloseDroneVehicleAgentConnection ends a stream record and marks its link lost.
//
// Reconnects can briefly leave an old stream shutting down while a newer stream
// is already active. MarkVehicleAgentDisconnected prevents that old cleanup from
// incorrectly marking the agent unavailable after the new connection has taken
// over command delivery.
func (s *VehicleAgentConnectionService) CloseDroneVehicleAgentConnection(ctx context.Context, connectionID string, input repository.CloseDroneVehicleAgentConnectionInput, now time.Time) (models.DroneVehicleAgentConnection, error) {
	var connection models.DroneVehicleAgentConnection
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		connection, err = repos.DroneVehicleAgentConnections.EndDroneVehicleAgentConnection(
			ctx,
			connectionID,
			models.DroneVehicleAgentConnectionDisconnected,
			input.EndedReason,
			now,
		)
		if err != nil {
			return err
		}

		if err := repos.CommunicationLinks.EndCommunicationLinksForDroneVehicleAgentConnection(
			ctx,
			connectionID,
			models.CommunicationLinkStatusLost,
			input.EndedReason,
			now,
		); err != nil {
			return err
		}

		if input.MarkVehicleAgentDisconnected {
			_, err = repos.VehicleAgents.SetCommandChannelState(ctx, connection.VehicleAgentID, models.CommandChannelDisconnected, now)
		}
		return err
	})
	return connection, err
}
