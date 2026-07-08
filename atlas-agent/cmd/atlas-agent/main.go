package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	agentchannel "github.com/sunnyside/atlas/atlas-agent/internal/agentchannel"
	"github.com/sunnyside/atlas/atlas-agent/internal/backend"
	"github.com/sunnyside/atlas/atlas-agent/internal/config"
	"github.com/sunnyside/atlas/atlas-agent/internal/telemetry"
	"github.com/sunnyside/atlas/atlas-agent/internal/vehicle"
)

var errUnsupportedCommand = errors.New("unsupported command type")

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	cfg := config.Load()
	client := backend.NewClient(cfg.BackendURL)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

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
	)

	go agentchannel.Run(ctx, logger, agentchannel.Config{
		Addr:                cfg.VehicleAgentGRPCAddr,
		VehicleAgentID:      cfg.VehicleAgentID,
		DroneID:             cfg.DroneID,
		DroneName:           cfg.DroneName,
		VehicleAgentVersion: cfg.VehicleAgentVersion,
		HeartbeatInterval:   cfg.HeartbeatInterval,
		TelemetryInterval:   cfg.TelemetryInterval,
		CommandTimeout:      cfg.CommandTimeout,
		RetryMin:            cfg.RegisterRetryMin,
		RetryMax:            cfg.RegisterRetryMax,
	}, gateway, telemetrySource)

	register, err := registerWithRetry(ctx, logger, client, cfg)
	if err != nil {
		logger.Error("vehicle agent stopped before registration completed", "error", err)
		os.Exit(1)
	}

	logger.Info(
		"vehicle agent registered",
		"vehicle_agent_id", register.VehicleAgentID,
		"drone_id", register.DroneID,
		"status", register.Status,
		"heartbeat_interval_seconds", register.HeartbeatIntervalSeconds,
	)

	commandTicker := time.NewTicker(cfg.CommandPollInterval)
	defer commandTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("atlas vehicle agent stopped")
			return
		case <-commandTicker.C:
			processNextCommand(ctx, logger, client, cfg, gateway)
		}
	}
}

func registerWithRetry(ctx context.Context, logger *slog.Logger, client *backend.Client, cfg config.Config) (backend.RegisterVehicleAgentResponse, error) {
	backoff := cfg.RegisterRetryMin

	for {
		res, err := client.RegisterVehicleAgent(ctx, backend.RegisterVehicleAgentRequest{
			VehicleAgentID:      cfg.VehicleAgentID,
			DroneID:             cfg.DroneID,
			DroneName:           cfg.DroneName,
			VehicleAgentVersion: cfg.VehicleAgentVersion,
		})
		// TODO: Handle structured backend registration failures when the API exposes them.
		// For now, every registration error is treated as retryable.
		if err == nil {
			return res, nil
		}

		logger.Warn(
			"vehicle-agent registration failed; retrying",
			"error", err,
			"retry_after", backoff.String(),
		)

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return backend.RegisterVehicleAgentResponse{}, ctx.Err()
		case <-timer.C:
		}

		backoff = nextBackoff(backoff, cfg.RegisterRetryMax)
	}
}

func nextBackoff(current time.Duration, max time.Duration) time.Duration {
	next := current * 2
	if next > max {
		return max
	}

	return next
}

func processNextCommand(ctx context.Context, logger *slog.Logger, client *backend.Client, cfg config.Config, gateway vehicle.Gateway) {
	command, ok, err := client.FetchNextCommand(ctx, cfg.VehicleAgentID)
	if err != nil {
		logger.Warn("command poll failed", "error", err)
		return
	}

	if !ok {
		return
	}

	logger.Info(
		"command received",
		"command_id", command.ID,
		"type", command.Type,
		"drone_id", command.DroneID,
		"requested_by", command.RequestedBy,
	)

	reportCommandStatus(ctx, logger, client, cfg.VehicleAgentID, command.ID, backend.CommandStateVehicleAgentReceived, "")
	reportCommandStatus(ctx, logger, client, cfg.VehicleAgentID, command.ID, backend.CommandStateSentToVehicle, "")

	commandCtx, cancel := context.WithTimeout(ctx, cfg.CommandTimeout)
	defer cancel()

	if err := executeVehicleCommand(commandCtx, gateway, command); err != nil {
		state := backend.CommandStateVehicleRejected
		if errors.Is(err, errUnsupportedCommand) {
			state = backend.CommandStateFailed
		}

		reportCommandStatus(ctx, logger, client, cfg.VehicleAgentID, command.ID, state, err.Error())
		logger.Error("command execution failed", "command_id", command.ID, "type", command.Type, "error", err)
		return
	}

	reportCommandStatus(ctx, logger, client, cfg.VehicleAgentID, command.ID, backend.CommandStateVehicleAcked, "accepted by vehicle")
	logger.Info("command acknowledged by vehicle", "command_id", command.ID, "type", command.Type)
}

func executeVehicleCommand(ctx context.Context, gateway vehicle.Gateway, command backend.Command) error {
	switch command.Type {
	case backend.CommandTypeArm:
		return gateway.Arm(ctx)
	case backend.CommandTypeTakeoff:
		return gateway.Takeoff(ctx)
	case backend.CommandTypeReturnToLaunch:
		return gateway.ReturnToLaunch(ctx)
	case backend.CommandTypeLand:
		return gateway.Land(ctx)
	default:
		return fmt.Errorf("%w: %s", errUnsupportedCommand, command.Type)
	}
}

func reportCommandStatus(ctx context.Context, logger *slog.Logger, client *backend.Client, agentID string, commandID string, state string, resultMessage string) {
	command, err := client.ReportCommandStatus(ctx, agentID, commandID, backend.CommandStatusRequest{
		State:         state,
		ResultMessage: resultMessage,
	})
	if err != nil {
		logger.Warn("command status report failed", "command_id", commandID, "state", state, "error", err)
		return
	}

	logger.Info("command status reported", "command_id", command.ID, "state", command.State)
}
