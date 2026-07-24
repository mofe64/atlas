package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/config"
	"github.com/sunnyside/atlas/atlas-agent/internal/geolocation"
	"github.com/sunnyside/atlas/atlas-agent/internal/identity"
	"github.com/sunnyside/atlas/atlas-agent/internal/navigation"
	"github.com/sunnyside/atlas/atlas-agent/internal/perception"
	"github.com/sunnyside/atlas/atlas-agent/internal/spatial"
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
	geolocationConfig := geolocation.DefaultConfig()
	geolocationConfig.BoresightAngularUncertaintyDeg = cfg.GeolocationBoresightAngularUncertaintyDeg
	geolocationConfig.BoresightAlignmentReference = cfg.GeolocationBoresightAlignmentReference
	geolocationFoundation, err := geolocation.NewFoundation(geolocationConfig)
	if err != nil {
		logger.Error("configure geolocation temporal foundation", "error", err)
		os.Exit(1)
	}
	var perceptionOutputs perception.Outputs
	if cfg.PerceptionEnabled() {
		trackerStage := perception.NewDisabledTrackingStage()
		switch cfg.TrackerAlgorithm {
		case "byte_track", "byte_track_cmc":
			trackerConfig := perception.DefaultByteTrackConfig()
			trackerConfig.WorkerPath = cfg.ByteTrackWorkerPath
			trackerConfig.RequestTimeout = cfg.ByteTrackRequestTimeout
			trackerConfig.FrameRate = cfg.ByteTrackFrameRate
			trackerConfig.TrackThreshold = cfg.ByteTrackTrackThreshold
			trackerConfig.HighThreshold = cfg.ByteTrackHighThreshold
			trackerConfig.MatchThreshold = cfg.ByteTrackMatchThreshold
			trackerConfig.TrackBufferFrames = cfg.ByteTrackBufferFrames
			trackerConfig.CameraMotionEnabled = cfg.TrackerAlgorithm == "byte_track_cmc"
			trackerConfig.CameraMotionMinimumConfidence = cfg.TrackerCameraMotionMinConfidence
			trackerBackend, trackerErr := perception.NewByteTrackBackend(trackerConfig)
			if trackerErr != nil {
				logger.Error("configure FoundationVision ByteTrack tracker", "error", trackerErr)
				os.Exit(1)
			}
			defer trackerBackend.Close()
			lifecycleConfig := perception.TrackLifecycleConfig{
				ConfirmationObservations: uint64(cfg.TrackerConfirmationObservations),
				PredictionHorizon:        cfg.TrackerPredictionHorizon,
				LostAfter:                cfg.TrackerLostAfter,
				CloseAfter:               cfg.TrackerCloseAfter,
				SnapshotInterval:         cfg.TrackerLifecycleSnapshotInterval,
				MaxHistoryObservations:   cfg.TrackerHistoryObservations,
			}
			trackerStage, trackerErr = perception.NewTrackingStageWithLifecycle(trackerBackend, cfg.TrackerMaxTimestampGap, lifecycleConfig)
			if trackerErr != nil {
				logger.Error("configure persistent track lifecycle", "error", trackerErr)
				os.Exit(1)
			}
		}
		perceptionOutputs, err = perception.StartRuntimeSourceWithTrackerAndTiming(ctx, logger, cfg.PerceptionSocketPath, trackerStage, geolocationFoundation)
		if err != nil {
			logger.Error("start perception runtime source", "error", err)
			os.Exit(1)
		}
		logger.Info("perception runtime source ready", "provider", cfg.PerceptionProvider, "socket_path", cfg.PerceptionSocketPath, "tracker", cfg.TrackerAlgorithm, "camera_motion_compensation", cfg.TrackerAlgorithm == "byte_track_cmc", "reid", false)
		if cfg.PerceptionProvider == "hailo" && cfg.PerceptionAdapterMode == "process" {
			if err := perception.StartHailoRTAdapter(ctx, logger, cfg.PerceptionAdapterPath, cfg.PerceptionSocketPath); err != nil {
				logger.Error("start HailoRT perception adapter", "error", err)
				os.Exit(1)
			}
		} else if cfg.PerceptionProvider == "hailo" {
			logger.Info("HailoRT perception adapter is externally supervised", "mode", cfg.PerceptionAdapterMode)
		}
	}
	var spatialOutputs spatial.Outputs
	if cfg.SpatialEnabled {
		spatialOutputs = spatial.StartRuntimeSource(ctx, logger, cfg.SpatialCloudSocketPath)
		logger.Info("spatial cloud source ready", "socket_path", cfg.SpatialCloudSocketPath, "maximum_points", spatial.MaximumPoints, "snapshot_semantics", "complete_latest_only")
	}
	telemetryOutputs, err := mavsdktelemetry.StartWithGeolocation(ctx, logger, cfg.MAVSDKGRPCAddress, cfg.TelemetryInterval, geolocationFoundation)
	if err != nil {
		logger.Error("start MAVSDK telemetry", "error", err)
		os.Exit(1)
	}
	if err := navigation.StartSocketServer(ctx, logger, cfg.NavigationSocketPath, telemetryOutputs.Navigation); err != nil {
		logger.Error("start navigation-state data plane", "error", err)
		os.Exit(1)
	}
	logger.Info("navigation-state data plane ready", "socket_path", cfg.NavigationSocketPath, "movement_authority", false)
	payloadController, err := vehicle.NewPayloadController(cfg.MAVSDKGRPCAddress, logger)
	if err != nil {
		logger.Error("start MAVSDK payload controller", "error", err)
		os.Exit(1)
	}
	defer payloadController.Close()
	if err := payloadController.StartGimbalAttitudeRecording(ctx, geolocationFoundation); err != nil {
		logger.Error("start measured gimbal attitude recording", "error", err)
		os.Exit(1)
	}
	siyiCameraAddress := ""
	if cfg.CameraTransport.UsesSIYI() {
		siyiCameraAddress = cfg.SIYICameraAddress
	}
	payloadController.ConfigureCameraTransports(cfg.CameraTransport.UsesMAVSDK(), siyiCameraAddress)
	if perceptionOutputs.TrackFollower != nil && cfg.TrackerAlgorithm != "disabled" {
		if err := payloadController.ConfigureSelectedTrackGeolocation(perceptionOutputs.TrackFollower, geolocationFoundation); err != nil {
			logger.Error("configure selected-track geolocation", "error", err)
			os.Exit(1)
		}
		followConfig := vehicle.GimbalFollowConfig{
			UpdateInterval: cfg.GimbalFollowUpdateInterval, TrackFreshness: cfg.GimbalFollowTrackFreshness, HoldTimeout: cfg.GimbalFollowHoldTimeout,
			Deadband: cfg.GimbalFollowDeadband, PitchGain: cfg.GimbalFollowPitchGain, YawGain: cfg.GimbalFollowYawGain,
			MaxPitchRate: cfg.GimbalFollowMaxPitchRate, MaxYawRate: cfg.GimbalFollowMaxYawRate,
			MaxPitchAcceleration: cfg.GimbalFollowMaxPitchAcceleration, MaxYawAcceleration: cfg.GimbalFollowMaxYawAcceleration,
			MinPitch: cfg.GimbalFollowMinPitch, MaxPitch: cfg.GimbalFollowMaxPitch,
			MinYaw: cfg.GimbalFollowMinYaw, MaxYaw: cfg.GimbalFollowMaxYaw, LimitMargin: cfg.GimbalFollowLimitMargin,
		}
		if err := payloadController.ConfigureTrackFollowing(perceptionOutputs.TrackFollower, followConfig); err != nil {
			logger.Error("configure operator-selected gimbal following", "error", err)
			os.Exit(1)
		}
	}
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
	missionExecutor.SetPerceptionControl(perceptionOutputs.Control)
	aircraftFollowConfig := vehicle.DefaultAircraftFollowControllerConfig()
	aircraftFollowConfig.Enabled = cfg.AircraftFollowEnabled
	aircraftFollowConfig.ValidationReference = cfg.AircraftFollowValidationReference
	aircraftFollowController, err := vehicle.NewAircraftFollowController(
		cfg.MAVSDKGRPCAddress,
		logger,
		aircraftFollowConfig,
		telemetryOutputs.Latest,
	)
	if err != nil {
		logger.Error("start supervised aircraft follow controller", "error", err)
		os.Exit(1)
	}
	defer aircraftFollowController.Close()
	indoorExploreController, err := vehicle.NewIndoorExploreController(actionExecutor)
	if err != nil {
		logger.Error("start Indoor Explore contract controller", "error", err)
		os.Exit(1)
	}
	discoveryContext, cancelDiscovery := context.WithTimeout(ctx, 10*time.Second)
	gimbalIDs, discoveryErr := discoverGimbalsWithRetry(
		discoveryContext,
		time.Second,
		250*time.Millisecond,
		actionExecutor.DiscoverGimbals,
	)
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

	logger.Info("atlas agent started", "state_directory", cfg.StateDirectory, "installation_id", localIdentity.InstallationID, "drone_id", localIdentity.DroneID, "mavsdk_grpc_address", cfg.MAVSDKGRPCAddress, "camera_transport", cfg.CameraTransport, "geolocation_temporal_foundation", true)
	go groundstation.Run(ctx, logger, cfg, localIdentity, telemetryOutputs.Snapshots, telemetryOutputs.StatusTexts, perceptionOutputs, spatialOutputs, actionExecutor, missionExecutor, aircraftFollowController, indoorExploreController)
	<-ctx.Done()
	logger.Info("atlas agent stopped")
}
