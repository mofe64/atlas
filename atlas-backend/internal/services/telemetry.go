package services

import (
	"context"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/domain"
	"github.com/sunnyside/atlas/atlas-backend/internal/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repository"
)

// TelemetryService records latest vehicle telemetry and settles workflows that telemetry can confirm.
type TelemetryService struct {
	txManager repository.TxManager
}

// NewTelemetryService builds the workflow service used by agent telemetry ingestion.
func NewTelemetryService(txManager repository.TxManager) *TelemetryService {
	return &TelemetryService{txManager: txManager}
}

// RecordTelemetry stores the latest telemetry snapshot and applies any vehicle action or mission state changes it proves.
func (s *TelemetryService) RecordTelemetry(ctx context.Context, snapshot models.TelemetrySnapshot, now time.Time) (models.TelemetrySnapshot, error) {
	var recorded models.TelemetrySnapshot
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		var err error
		recorded, err = s.recordTelemetry(ctx, repos, snapshot, now)
		return err
	})
	return recorded, err
}

// RecordLocalTelemetry records observe-only telemetry from a local ground-unit
// input such as an HM30/SiK MAVLink feed. It keeps feed/sample history current,
// but only promotes that source to telemetry_latest when the selected latest
// state is not fresh Agent telemetry.
func (s *TelemetryService) RecordLocalTelemetry(ctx context.Context, input repository.RecordLocalTelemetryInput, now time.Time) (models.TelemetrySnapshot, bool, error) {
	var recorded models.TelemetrySnapshot
	var promoted bool
	err := s.txManager.WithinTx(ctx, func(ctx context.Context, repos repository.Repositories) error {
		if !repos.Drones.DroneExists(ctx, input.DroneID) {
			return repository.ErrDroneNotFound
		}

		link, err := s.recordLocalCommunicationLink(ctx, repos, input, now)
		if err != nil {
			return err
		}

		snapshot := input.Snapshot
		snapshot.DroneID = input.DroneID
		snapshot.VehicleAgentID = ""
		if snapshot.Source == "" {
			snapshot.Source = input.Source
		}
		if snapshot.Source == "" {
			snapshot.Source = input.SourceID
		}

		feed, err := s.recordLocalTelemetryFeed(ctx, repos, input, snapshot, link.ID, now)
		if err != nil {
			return err
		}

		snapshot.ActiveTelemetryFeedID = feed.ID
		snapshot.SourceCommunicationLinkID = feed.CommunicationLinkID
		snapshot.ReceivedAt = now
		recorded = snapshot

		if err := s.insertTelemetrySample(ctx, repos, recorded); err != nil {
			return err
		}
		if err := repos.Drones.UpdateDroneLastSeen(ctx, input.DroneID, now); err != nil {
			return err
		}

		if !s.shouldPromoteLocalTelemetry(ctx, repos, input.DroneID, feed.ID, now) {
			return nil
		}

		recorded, err = repos.Telemetry.UpsertLatestTelemetry(ctx, recorded, now)
		if err != nil {
			return err
		}
		promoted = true
		return nil
	})
	return recorded, promoted, err
}

// recordTelemetry enriches agent telemetry with drone identity and runs telemetry-driven workflow updates in one transaction.
func (s *TelemetryService) recordTelemetry(ctx context.Context, repos repository.Repositories, snapshot models.TelemetrySnapshot, now time.Time) (models.TelemetrySnapshot, error) {
	agent, ok, err := repos.VehicleAgents.GetVehicleAgentByID(ctx, snapshot.VehicleAgentID)
	if err != nil {
		return models.TelemetrySnapshot{}, err
	}
	if !ok {
		return models.TelemetrySnapshot{}, repository.ErrVehicleAgentNotFound
	}

	snapshot.DroneID = agent.DroneID
	feed, err := s.recordAgentDirectTelemetryFeed(ctx, repos, agent, snapshot, now)
	if err != nil {
		return models.TelemetrySnapshot{}, err
	}
	snapshot.ActiveTelemetryFeedID = feed.ID
	snapshot.SourceCommunicationLinkID = feed.CommunicationLinkID

	recorded, err := repos.Telemetry.UpsertLatestTelemetry(ctx, snapshot, now)
	if err != nil {
		return models.TelemetrySnapshot{}, err
	}
	if err := s.insertTelemetrySample(ctx, repos, recorded); err != nil {
		return models.TelemetrySnapshot{}, err
	}
	if err := repos.Drones.UpdateDroneLastSeen(ctx, recorded.DroneID, now); err != nil {
		return models.TelemetrySnapshot{}, err
	}
	if err := s.confirmVehicleActionsFromTelemetry(ctx, repos, recorded, now); err != nil {
		return models.TelemetrySnapshot{}, err
	}
	if err := s.settleMissionExecutionsFromTelemetry(ctx, repos, recorded, now); err != nil {
		return models.TelemetrySnapshot{}, err
	}
	return recorded, nil
}

func (s *TelemetryService) recordLocalCommunicationLink(ctx context.Context, repos repository.Repositories, input repository.RecordLocalTelemetryInput, now time.Time) (models.CommunicationLink, error) {
	transport := input.Transport
	if transport == "" {
		transport = "LOCAL"
	}
	endpointDescription := input.EndpointDescription
	if endpointDescription == "" {
		endpointDescription = input.SourceID
	}

	link, ok, err := repos.CommunicationLinks.GetOpenCommunicationLinkByLocalEndpoint(
		ctx,
		input.DroneID,
		models.CommunicationLinkGroundUnitDataLink,
		transport,
		endpointDescription,
	)
	if err != nil {
		return models.CommunicationLink{}, err
	}

	roles := input.Roles
	if len(roles) == 0 {
		roles = []models.CommunicationLinkRole{models.CommunicationLinkRoleTelemetry}
	}

	if ok {
		link.Roles = roles
		link.Status = models.CommunicationLinkStatusConnected
		link.CommandEligible = false
		link.LastSeenAt = now
		link.EndedAt = time.Time{}
		link.EndedReason = ""
		if err := repos.CommunicationLinks.UpdateCommunicationLink(ctx, link); err != nil {
			return models.CommunicationLink{}, err
		}
		return link, nil
	}

	linkID, err := repos.CommunicationLinks.GenerateCommunicationLinkID(ctx)
	if err != nil {
		return models.CommunicationLink{}, err
	}

	link = models.CommunicationLink{
		ID:                  linkID,
		DroneID:             input.DroneID,
		LinkType:            models.CommunicationLinkGroundUnitDataLink,
		Roles:               roles,
		Status:              models.CommunicationLinkStatusConnected,
		Transport:           transport,
		EndpointDescription: endpointDescription,
		CommandEligible:     false,
		LastSeenAt:          now,
		CreatedAt:           now,
	}
	if err := repos.CommunicationLinks.InsertCommunicationLink(ctx, link); err != nil {
		return models.CommunicationLink{}, err
	}
	return link, nil
}

func (s *TelemetryService) recordLocalTelemetryFeed(ctx context.Context, repos repository.Repositories, input repository.RecordLocalTelemetryInput, snapshot models.TelemetrySnapshot, communicationLinkID string, now time.Time) (models.TelemetryFeed, error) {
	feed, ok, err := repos.TelemetryFeeds.GetTelemetryFeedBySource(
		ctx,
		input.DroneID,
		models.TelemetrySourceLocalGround,
		input.SourceID,
		communicationLinkID,
	)
	if err != nil {
		return models.TelemetryFeed{}, err
	}

	fieldsAvailable := telemetryFieldsAvailable(snapshot)
	if ok {
		feed.Status = models.TelemetryFeedStatusActive
		feed.Freshness = models.TelemetryStateFresh
		feed.MessageRateHz = telemetryMessageRateHz(feed.LastTelemetryAt, now)
		feed.LastTelemetryAt = now
		feed.FieldsAvailable = fieldsAvailable
		feed.LastError = ""
		if err := repos.TelemetryFeeds.UpdateTelemetryFeed(ctx, feed); err != nil {
			return models.TelemetryFeed{}, err
		}
		return feed, nil
	}

	feedID, err := repos.TelemetryFeeds.GenerateTelemetryFeedID(ctx)
	if err != nil {
		return models.TelemetryFeed{}, err
	}

	feed = models.TelemetryFeed{
		ID:                  feedID,
		DroneID:             input.DroneID,
		SourceType:          models.TelemetrySourceLocalGround,
		SourceID:            input.SourceID,
		CommunicationLinkID: communicationLinkID,
		Status:              models.TelemetryFeedStatusActive,
		Priority:            200,
		Freshness:           models.TelemetryStateFresh,
		LastTelemetryAt:     now,
		FieldsAvailable:     fieldsAvailable,
		StartedAt:           now,
	}
	if err := repos.TelemetryFeeds.InsertTelemetryFeed(ctx, feed); err != nil {
		return models.TelemetryFeed{}, err
	}
	return feed, nil
}

func (s *TelemetryService) shouldPromoteLocalTelemetry(ctx context.Context, repos repository.Repositories, droneID string, localFeedID string, now time.Time) bool {
	current, ok := repos.Telemetry.GetTelemetryForDrone(ctx, droneID)
	if !ok {
		return true
	}
	if current.ActiveTelemetryFeedID == localFeedID {
		return true
	}
	if current.VehicleAgentID == "" {
		return true
	}
	return models.TelemetryStateFromReceivedAt(current.ReceivedAt, now) != models.TelemetryStateFresh
}

func (s *TelemetryService) recordAgentDirectTelemetryFeed(ctx context.Context, repos repository.Repositories, agent models.VehicleAgent, snapshot models.TelemetrySnapshot, now time.Time) (models.TelemetryFeed, error) {
	communicationLinkID, err := activeTelemetryCommunicationLinkID(ctx, repos, agent.ID)
	if err != nil {
		return models.TelemetryFeed{}, err
	}

	feed, ok, err := repos.TelemetryFeeds.GetTelemetryFeedBySource(
		ctx,
		agent.DroneID,
		models.TelemetrySourceAgentDirect,
		agent.ID,
		communicationLinkID,
	)
	if err != nil {
		return models.TelemetryFeed{}, err
	}

	fieldsAvailable := telemetryFieldsAvailable(snapshot)
	if ok {
		feed.Status = models.TelemetryFeedStatusActive
		feed.Freshness = models.TelemetryStateFresh
		feed.MessageRateHz = telemetryMessageRateHz(feed.LastTelemetryAt, now)
		feed.LastTelemetryAt = now
		feed.FieldsAvailable = fieldsAvailable
		feed.LastError = ""
		if err := repos.TelemetryFeeds.UpdateTelemetryFeed(ctx, feed); err != nil {
			return models.TelemetryFeed{}, err
		}
		return feed, nil
	}

	feedID, err := repos.TelemetryFeeds.GenerateTelemetryFeedID(ctx)
	if err != nil {
		return models.TelemetryFeed{}, err
	}

	feed = models.TelemetryFeed{
		ID:                  feedID,
		DroneID:             agent.DroneID,
		SourceType:          models.TelemetrySourceAgentDirect,
		SourceID:            agent.ID,
		CommunicationLinkID: communicationLinkID,
		Status:              models.TelemetryFeedStatusActive,
		Priority:            100,
		Freshness:           models.TelemetryStateFresh,
		LastTelemetryAt:     now,
		FieldsAvailable:     fieldsAvailable,
		StartedAt:           now,
	}
	if err := repos.TelemetryFeeds.InsertTelemetryFeed(ctx, feed); err != nil {
		return models.TelemetryFeed{}, err
	}
	return feed, nil
}

func (s *TelemetryService) insertTelemetrySample(ctx context.Context, repos repository.Repositories, snapshot models.TelemetrySnapshot) error {
	sampleID, err := repos.TelemetrySamples.GenerateTelemetrySampleID(ctx)
	if err != nil {
		return err
	}

	return repos.TelemetrySamples.InsertTelemetrySample(ctx, models.TelemetrySample{
		ID:              sampleID,
		DroneID:         snapshot.DroneID,
		TelemetryFeedID: snapshot.ActiveTelemetryFeedID,
		Timestamp:       snapshot.ReceivedAt,
		Snapshot:        snapshot,
	})
}

// confirmVehicleActionsFromTelemetry closes vehicle actions that were acknowledged by the vehicle and later proven by observed state.
func (s *TelemetryService) confirmVehicleActionsFromTelemetry(ctx context.Context, repos repository.Repositories, snapshot models.TelemetrySnapshot, now time.Time) error {
	actions, err := repos.VehicleActions.ListVehicleActionsForUpdate(ctx, repository.VehicleActionFilter{
		DroneID: snapshot.DroneID,
		States:  []models.VehicleActionState{models.VehicleActionStateVehicleAcked},
		Order:   repository.VehicleActionOrderRequestedAsc,
	})
	if err != nil {
		return err
	}
	for _, action := range actions {
		if !domain.TelemetryConfirmsVehicleAction(action, snapshot) {
			continue
		}
		action = domain.MarkVehicleActionTelemetryConfirmed(action, now)
		if err := repos.VehicleActions.UpdateVehicleAction(ctx, action); err != nil {
			return err
		}
		if err := repos.VehicleActions.InsertVehicleActionEvent(ctx, action, string(action.State), "backend", action.ResultMessage, now); err != nil {
			return err
		}
	}
	return nil
}

func activeTelemetryCommunicationLinkID(ctx context.Context, repos repository.Repositories, agentID string) (string, error) {
	connection, ok, err := repos.DroneVehicleAgentConnections.LatestActiveDroneVehicleAgentConnectionForAgent(ctx, agentID)
	if err != nil || !ok {
		return "", err
	}

	link, ok, err := repos.CommunicationLinks.GetCommunicationLinkForDroneVehicleAgentConnection(ctx, connection.ID)
	if err != nil || !ok {
		return "", err
	}
	if !communicationLinkCanCarryTelemetry(link) {
		return "", nil
	}

	return link.ID, nil
}

func communicationLinkCanCarryTelemetry(link models.CommunicationLink) bool {
	if !link.EndedAt.IsZero() || link.Status == models.CommunicationLinkStatusLost || link.Status == models.CommunicationLinkStatusDisabled {
		return false
	}

	for _, role := range link.Roles {
		if role == models.CommunicationLinkRoleTelemetry {
			return true
		}
	}
	return false
}

func telemetryMessageRateHz(previous time.Time, now time.Time) float64 {
	if previous.IsZero() || !now.After(previous) {
		return 0
	}

	return 1 / now.Sub(previous).Seconds()
}

func telemetryFieldsAvailable(snapshot models.TelemetrySnapshot) models.TelemetryFieldsAvailable {
	return models.TelemetryFieldsAvailable{
		Position:        snapshot.Latitude != 0 || snapshot.Longitude != 0,
		Altitude:        snapshot.RelativeAltitudeM != 0 || snapshot.AltitudeMSL != 0,
		Heading:         snapshot.HeadingDeg != 0,
		Attitude:        snapshot.RollDeg != 0 || snapshot.PitchDeg != 0,
		Velocity:        snapshot.GroundSpeedMPS != 0 || snapshot.VelocityNorthMPS != 0 || snapshot.VelocityEastMPS != 0 || snapshot.VelocityDownMPS != 0,
		Battery:         true,
		Armed:           true,
		FlightMode:      snapshot.FlightMode != "",
		GPSHealth:       snapshot.GPSFix != "" || snapshot.SatellitesVisible > 0,
		HomePosition:    snapshot.HomePositionSet,
		MissionProgress: snapshot.MissionCurrentItem != 0 || snapshot.MissionTotalItems != 0,
		SystemHealth:    snapshot.SystemHealth != (models.SystemHealth{}),
	}
}

// settleMissionExecutionsFromTelemetry marks RTL abort requests complete once telemetry shows the aircraft is no longer airborne.
func (s *TelemetryService) settleMissionExecutionsFromTelemetry(ctx context.Context, repos repository.Repositories, snapshot models.TelemetrySnapshot, now time.Time) error {
	if snapshot.InAir {
		return nil
	}

	executions, err := repos.MissionExecutions.ListMissionExecutionsForUpdate(ctx, repository.MissionExecutionFilter{
		DroneID: snapshot.DroneID,
		States:  []models.MissionExecutionState{models.MissionExecutionStateRTLRequested},
		Order:   repository.MissionExecutionOrderUpdatedAsc,
	})
	if err != nil {
		return err
	}
	for _, execution := range executions {
		execution = domain.MarkMissionExecutionAbortedByTelemetry(execution, now)
		if err := repos.MissionExecutions.UpdateMissionExecution(ctx, execution); err != nil {
			return err
		}
		if err := repos.MissionExecutions.InsertMissionExecutionEvent(ctx, execution, "aborted", "backend", execution.ResultMessage, now); err != nil {
			return err
		}
	}
	return nil
}
