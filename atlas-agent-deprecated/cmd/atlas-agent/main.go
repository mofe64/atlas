package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/sunnyside/atlas/atlas-agent/internal/config"
	"github.com/sunnyside/atlas/atlas-agent/internal/mavlinkobserver"
	"github.com/sunnyside/atlas/atlas-agent/internal/telemetry"
	"github.com/sunnyside/atlas/atlas-agent/internal/transport/vehicleagentchannel"
	"github.com/sunnyside/atlas/atlas-agent/internal/vehicle"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	cfg := config.Load()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	observer, err := mavlinkobserver.NewRuntime(mavlinkobserver.Config{
		Endpoint: cfg.MAVLinkObserverEndpoint,
	})
	if err != nil {
		logger.Error("mavlink observer setup failed", "endpoint", cfg.MAVLinkObserverEndpoint, "error", err)
		os.Exit(1)
	}
	observerTracker := mavlinkobserver.NewTracker()
	observerLogger := logMAVLinkObservation(logger)
	go func() {
		if err := observer.Run(ctx, logger, func(ctx context.Context, observation mavlinkobserver.Observation) {
			observerTracker.HandleObservation(ctx, observation)
			observerLogger(ctx, observation)
		}, observerTracker); err != nil && ctx.Err() == nil {
			logger.Error("mavlink observer stopped", "endpoint", cfg.MAVLinkObserverEndpoint, "error", err)
			stop()
		}
	}()

	gateway, err := vehicle.NewMAVSDKGateway(cfg.MAVSDKGRPCAddr)
	if err != nil {
		logger.Error("vehicle gateway setup failed", "source", "px4", "error", err)
		os.Exit(1)
	}

	telemetrySource, err := telemetry.NewGatewaySource(ctx, "px4", gateway)
	if err != nil {
		logger.Error("telemetry source setup failed", "source", "px4", "error", err)
		os.Exit(1)
	}
	logger.Info(
		"telemetry source ready",
		"source", telemetrySource.Name(),
		"mavsdk_grpc_addr", cfg.MAVSDKGRPCAddr,
		"px4_system_address", cfg.PX4SystemAddress,
		"mavlink_observer_endpoint", cfg.MAVLinkObserverEndpoint,
	)

	go vehicleagentchannel.Run(ctx, logger, vehicleagentchannel.Config{
		Addr:                cfg.VehicleAgentGRPCAddr,
		VehicleAgentID:      cfg.VehicleAgentID,
		DroneID:             cfg.DroneID,
		DroneName:           cfg.DroneName,
		VehicleAgentVersion: cfg.VehicleAgentVersion,
		HeartbeatInterval:   cfg.HeartbeatInterval,
		TelemetryInterval:   cfg.TelemetryInterval,
		CommandTimeout:      cfg.CommandTimeout,
		RetryMin:            cfg.ChannelRetryMin,
		RetryMax:            cfg.ChannelRetryMax,
	}, gateway, telemetrySource, observerTracker, observer)

	<-ctx.Done()
	logger.Info("atlas vehicle agent stopped")
}

func logMAVLinkObservation(logger *slog.Logger) mavlinkobserver.ObservationHandler {
	if logger == nil {
		logger = slog.Default()
	}

	return func(_ context.Context, observation mavlinkobserver.Observation) {
		switch observation.Kind {
		case mavlinkobserver.ObservationCommandAck:
			ack := observation.CommandAck
			if ack == nil {
				return
			}
			logger.Info(
				"mavlink COMMAND_ACK observed",
				"system_id", observation.SystemID,
				"component_id", observation.ComponentID,
				"command", ack.Command,
				"result", ack.Result,
				"progress", optionalUint8(ack.Progress),
				"target_system", optionalUint8(ack.TargetSystem),
				"target_component", optionalUint8(ack.TargetComponent),
			)
		case mavlinkobserver.ObservationStatusText:
			statusText := observation.StatusText
			if statusText == nil || statusText.Text == "" {
				return
			}
			logger.Info(
				"mavlink STATUSTEXT observed",
				"system_id", observation.SystemID,
				"component_id", observation.ComponentID,
				"severity", statusText.Severity,
				"text", statusText.Text,
			)
		case mavlinkobserver.ObservationMissionCurrent:
			current := observation.MissionCurrent
			if current == nil {
				return
			}
			logger.Debug(
				"mavlink MISSION_CURRENT observed",
				"system_id", observation.SystemID,
				"component_id", observation.ComponentID,
				"sequence", current.Sequence,
			)
		default:
			logger.Debug(
				"mavlink observation decoded",
				"kind", observation.Kind,
				"system_id", observation.SystemID,
				"component_id", observation.ComponentID,
			)
		}
	}
}

func optionalUint8(value *uint8) any {
	if value == nil {
		return "unknown"
	}
	return *value
}
