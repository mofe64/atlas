package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/sunnyside/atlas/atlas-agent/internal/config"
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
	}, gateway, telemetrySource)

	<-ctx.Done()
	logger.Info("atlas vehicle agent stopped")
}
