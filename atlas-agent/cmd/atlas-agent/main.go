package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/config"
	"github.com/sunnyside/atlas/atlas-agent/internal/identity"
	"github.com/sunnyside/atlas/atlas-agent/internal/perception"
	mavsdktelemetry "github.com/sunnyside/atlas/atlas-agent/internal/telemetry/mavsdk"
	"github.com/sunnyside/atlas/atlas-agent/internal/transport/groundstation"
	"github.com/sunnyside/atlas/atlas-agent/internal/vehicle"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	localIdentity, err := identity.LoadOrCreate(cfg.StateDirectory)
	if err != nil {
		logger.Error("load agent identity", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var perceptionOutputs perception.Outputs
	if cfg.PerceptionEnabled() {
		perceptionOutputs, err = perception.StartRuntimeSource(ctx, logger, cfg.PerceptionSocketPath)
		if err != nil {
			logger.Error("start perception runtime source", "error", err)
			os.Exit(1)
		}
		logger.Info("perception runtime source ready", "provider", cfg.PerceptionProvider, "socket_path", cfg.PerceptionSocketPath)
		if cfg.PerceptionProvider == "hailo" {
			if err := perception.StartHailoRTAdapter(ctx, logger, cfg.PerceptionAdapterPath, cfg.PerceptionSocketPath); err != nil {
				logger.Error("start HailoRT perception adapter", "error", err)
				os.Exit(1)
			}
		}
	}
	telemetryOutputs, err := mavsdktelemetry.Start(ctx, logger, cfg.MAVSDKGRPCAddress, cfg.TelemetryInterval)
	if err != nil {
		logger.Error("start MAVSDK telemetry", "error", err)
		os.Exit(1)
	}
	payloadController, err := vehicle.NewPayloadController(cfg.MAVSDKGRPCAddress, logger)
	if err != nil {
		logger.Error("start MAVSDK payload controller", "error", err)
		os.Exit(1)
	}
	defer payloadController.Close()
	payloadController.ConfigureSIYICamera(cfg.SIYICameraAddress)
	actionExecutor, err := vehicle.NewActionExecutor(cfg.MAVSDKGRPCAddress, payloadController)
	if err != nil {
		logger.Error("start MAVSDK action executor", "error", err)
		os.Exit(1)
	}
	defer actionExecutor.Close()
	missionExecutor, err := vehicle.NewMissionExecutor(cfg.MAVSDKGRPCAddress, logger, payloadController)
	if err != nil {
		logger.Error("start MAVSDK mission executor", "error", err)
		os.Exit(1)
	}
	defer missionExecutor.Close()
	discoveryContext, cancelDiscovery := context.WithTimeout(ctx, 3*time.Second)
	gimbalIDs, discoveryErr := actionExecutor.DiscoverGimbals(discoveryContext)
	cancelDiscovery()
	if discoveryErr != nil {
		logger.Info("no MAVSDK gimbal discovered", "error", discoveryErr)
	} else {
		logger.Info("MAVSDK gimbal discovery complete", "gimbal_ids", gimbalIDs)
	}
	cameraDiscoveryContext, cancelCameraDiscovery := context.WithTimeout(ctx, 3*time.Second)
	cameraIDs, cameraDiscoveryErr := actionExecutor.DiscoverCameras(cameraDiscoveryContext)
	cancelCameraDiscovery()
	if cameraDiscoveryErr != nil {
		logger.Info("no zoom-capable camera discovered", "error", cameraDiscoveryErr)
	} else {
		logger.Info("camera discovery complete", "camera_component_ids", cameraIDs, "capabilities", actionExecutor.Capabilities())
	}

	logger.Info("atlas agent started", "state_directory", cfg.StateDirectory, "installation_id", localIdentity.InstallationID, "drone_id", localIdentity.DroneID, "mavsdk_grpc_address", cfg.MAVSDKGRPCAddress)
	go groundstation.Run(ctx, logger, cfg, localIdentity, telemetryOutputs.Snapshots, telemetryOutputs.StatusTexts, perceptionOutputs, actionExecutor, missionExecutor)
	<-ctx.Done()
	logger.Info("atlas agent stopped")
}
