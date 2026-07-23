package config

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLoadUsesExplicitAbsoluteStateDirectory(t *testing.T) {
	want := filepath.Join(t.TempDir(), "state")
	t.Setenv("ATLAS_AGENT_STATE_DIR", want)

	config, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if config.StateDirectory != want {
		t.Fatalf("StateDirectory = %q, want %q", config.StateDirectory, want)
	}
	if config.GroundStationAddress != "192.168.144.50:7443" {
		t.Fatalf("GroundStationAddress = %q", config.GroundStationAddress)
	}
	if config.SIYICameraAddress != "192.168.144.25:37260" {
		t.Fatalf("SIYICameraAddress = %q", config.SIYICameraAddress)
	}
	if config.CameraTransport != CameraTransportSIYIUDP || !config.CameraTransport.UsesSIYI() || config.CameraTransport.UsesMAVSDK() {
		t.Fatalf("CameraTransport = %q, want SIYI-only", config.CameraTransport)
	}
	if config.PerceptionProvider != "disabled" || config.PerceptionEnabled() {
		t.Fatalf("perception config = provider %q enabled %v", config.PerceptionProvider, config.PerceptionEnabled())
	}
	if config.PerceptionSocketPath != filepath.Join(want, "perception", "runtime.sock") {
		t.Fatalf("PerceptionSocketPath = %q", config.PerceptionSocketPath)
	}
	if config.NavigationSocketPath != filepath.Join(want, "navigation.sock") {
		t.Fatalf("NavigationSocketPath = %q", config.NavigationSocketPath)
	}
	if config.SpatialEnabled || config.SpatialCloudSocketPath != filepath.Join(want, "spatial-cloud.sock") || config.SpatialSourceID != "front-depth" {
		t.Fatalf("spatial defaults = %#v", config)
	}
	if config.PerceptionAdapterPath != "atlas-hailort-adapter" {
		t.Fatalf("PerceptionAdapterPath = %q", config.PerceptionAdapterPath)
	}
	if config.PerceptionAdapterMode != "process" {
		t.Fatalf("PerceptionAdapterMode = %q", config.PerceptionAdapterMode)
	}
	if config.TrackerAlgorithm != "byte_track" || config.TrackerMaxTimestampGap != 2*time.Second || config.TrackerCameraMotionMinConfidence != 0.25 {
		t.Fatalf("tracker defaults = %#v", config)
	}
	if config.TrackerConfirmationObservations != 2 || config.TrackerPredictionHorizon != 750*time.Millisecond || config.TrackerLostAfter != time.Second || config.TrackerCloseAfter != 3*time.Second || config.TrackerLifecycleSnapshotInterval != time.Second || config.TrackerHistoryObservations != 60 {
		t.Fatalf("track lifecycle defaults = %#v", config)
	}
	if config.ByteTrackWorkerPath != "atlas-bytetrack-worker" || config.ByteTrackRequestTimeout != 250*time.Millisecond || config.ByteTrackFrameRate != 30 || config.ByteTrackTrackThreshold != 0.50 || config.ByteTrackHighThreshold != 0.60 || config.ByteTrackMatchThreshold != 0.80 || config.ByteTrackBufferFrames != 30 {
		t.Fatalf("ByteTrack defaults = %#v", config)
	}
	if config.GeolocationBoresightAngularUncertaintyDeg != 10 || config.GeolocationBoresightAlignmentReference != "" {
		t.Fatalf("geolocation boresight defaults = %#v", config)
	}
	if config.GimbalFollowUpdateInterval != 100*time.Millisecond || config.GimbalFollowTrackFreshness != 350*time.Millisecond || config.GimbalFollowHoldTimeout != 2*time.Second || config.GimbalFollowDeadband != 0.025 || config.GimbalFollowMaxPitchRate != 20 || config.GimbalFollowMaxYawRate != 30 || config.GimbalFollowMinPitch != -90 || config.GimbalFollowMaxPitch != 30 || config.GimbalFollowMinYaw != -180 || config.GimbalFollowMaxYaw != 180 {
		t.Fatalf("gimbal follow defaults = %#v", config)
	}
	if config.AircraftFollowEnabled || config.AircraftFollowValidationReference != "" {
		t.Fatalf("uncommissioned aircraft follow must remain disabled = %#v", config)
	}
}

func TestAircraftFollowEnablementRequiresPhysicalEvidence(t *testing.T) {
	t.Setenv("ATLAS_AGENT_STATE_DIR", t.TempDir())
	t.Setenv("ATLAS_AIRCRAFT_FOLLOW_ENABLED", "true")
	if _, err := Load(); err == nil {
		t.Fatal("aircraft follow enabled without validation or boresight evidence")
	}
	t.Setenv("ATLAS_AIRCRAFT_FOLLOW_VALIDATION_REFERENCE", "sitl-hil-flight/accepted-1")
	if _, err := Load(); err == nil {
		t.Fatal("aircraft follow enabled without physical boresight evidence")
	}
	t.Setenv("ATLAS_GEOLOCATION_BORESIGHT_ALIGNMENT_REFERENCE", "commissioning/a8-gimbal-2026-07-20")
	config, err := Load()
	if err != nil {
		t.Fatalf("load commissioned follow config: %v", err)
	}
	if !config.AircraftFollowEnabled || config.AircraftFollowValidationReference != "sitl-hil-flight/accepted-1" {
		t.Fatalf("aircraft follow commissioning = %#v", config)
	}
}

func TestLoadReadsBoresightCommissioningEvidence(t *testing.T) {
	t.Setenv("ATLAS_AGENT_STATE_DIR", t.TempDir())
	t.Setenv("ATLAS_GEOLOCATION_BORESIGHT_ANGULAR_UNCERTAINTY_DEG", "2.5")
	t.Setenv("ATLAS_GEOLOCATION_BORESIGHT_ALIGNMENT_REFERENCE", "commissioning/a8-gimbal-2026-07-20")
	config, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if config.GeolocationBoresightAngularUncertaintyDeg != 2.5 || config.GeolocationBoresightAlignmentReference != "commissioning/a8-gimbal-2026-07-20" {
		t.Fatalf("geolocation boresight config = %#v", config)
	}

	t.Setenv("ATLAS_GEOLOCATION_BORESIGHT_ANGULAR_UNCERTAINTY_DEG", "45")
	if _, err := Load(); err == nil {
		t.Fatal("unsafe boresight angular bound was accepted")
	}
}

func TestLoadReadsAndValidatesGimbalFollowSafetyEnvelope(t *testing.T) {
	t.Setenv("ATLAS_AGENT_STATE_DIR", t.TempDir())
	t.Setenv("ATLAS_GIMBAL_FOLLOW_UPDATE_INTERVAL", "80ms")
	t.Setenv("ATLAS_GIMBAL_FOLLOW_TRACK_FRESHNESS", "400ms")
	t.Setenv("ATLAS_GIMBAL_FOLLOW_HOLD_TIMEOUT", "1500ms")
	t.Setenv("ATLAS_GIMBAL_FOLLOW_MAX_YAW_RATE", "25")
	t.Setenv("ATLAS_GIMBAL_FOLLOW_MAX_YAW_ACCELERATION", "70")
	t.Setenv("ATLAS_GIMBAL_FOLLOW_MIN_YAW", "-150")
	t.Setenv("ATLAS_GIMBAL_FOLLOW_MAX_YAW", "150")
	config, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if config.GimbalFollowUpdateInterval != 80*time.Millisecond || config.GimbalFollowTrackFreshness != 400*time.Millisecond || config.GimbalFollowHoldTimeout != 1500*time.Millisecond || config.GimbalFollowMaxYawRate != 25 || config.GimbalFollowMaxYawAcceleration != 70 || config.GimbalFollowMinYaw != -150 || config.GimbalFollowMaxYaw != 150 {
		t.Fatalf("gimbal follow config = %#v", config)
	}

	t.Setenv("ATLAS_GIMBAL_FOLLOW_MIN_YAW", "20")
	t.Setenv("ATLAS_GIMBAL_FOLLOW_MAX_YAW", "10")
	if _, err := Load(); err == nil {
		t.Fatal("inverted physical yaw limits were accepted")
	}
}

func TestLoadReadsFoundationVisionByteTrackSettings(t *testing.T) {
	t.Setenv("ATLAS_AGENT_STATE_DIR", t.TempDir())
	t.Setenv("ATLAS_TRACKER_ALGORITHM", "byte_track")
	t.Setenv("ATLAS_BYTETRACK_WORKER_PATH", "/opt/atlas/atlas-bytetrack-worker")
	t.Setenv("ATLAS_BYTETRACK_REQUEST_TIMEOUT", "400ms")
	t.Setenv("ATLAS_BYTETRACK_FRAME_RATE", "25")
	t.Setenv("ATLAS_BYTETRACK_TRACK_THRESHOLD", "0.45")
	t.Setenv("ATLAS_BYTETRACK_HIGH_THRESHOLD", "0.55")
	t.Setenv("ATLAS_BYTETRACK_MATCH_THRESHOLD", "0.75")
	t.Setenv("ATLAS_BYTETRACK_BUFFER_FRAMES", "40")

	config, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if config.TrackerAlgorithm != "byte_track" || config.ByteTrackWorkerPath != "/opt/atlas/atlas-bytetrack-worker" || config.ByteTrackRequestTimeout != 400*time.Millisecond || config.ByteTrackFrameRate != 25 || config.ByteTrackTrackThreshold != 0.45 || config.ByteTrackHighThreshold != 0.55 || config.ByteTrackMatchThreshold != 0.75 || config.ByteTrackBufferFrames != 40 {
		t.Fatalf("ByteTrack config = %#v", config)
	}
}

func TestLoadReadsByteTrackCMCSettings(t *testing.T) {
	t.Setenv("ATLAS_AGENT_STATE_DIR", t.TempDir())
	t.Setenv("ATLAS_TRACKER_ALGORITHM", "byte_track_cmc")
	t.Setenv("ATLAS_TRACKER_MAX_TIMESTAMP_GAP", "3s")
	t.Setenv("ATLAS_TRACKER_CMC_MIN_CONFIDENCE", "0.4")
	t.Setenv("ATLAS_TRACKER_CONFIRMATION_OBSERVATIONS", "3")
	t.Setenv("ATLAS_TRACKER_PREDICTION_HORIZON", "500ms")
	t.Setenv("ATLAS_TRACKER_LOST_AFTER", "1500ms")
	t.Setenv("ATLAS_TRACKER_CLOSE_AFTER", "4s")
	t.Setenv("ATLAS_TRACKER_LIFECYCLE_SNAPSHOT_INTERVAL", "2s")
	t.Setenv("ATLAS_TRACKER_HISTORY_OBSERVATIONS", "90")
	config, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if config.TrackerAlgorithm != "byte_track_cmc" || config.TrackerMaxTimestampGap != 3*time.Second || config.TrackerCameraMotionMinConfidence != 0.4 || config.TrackerConfirmationObservations != 3 || config.TrackerPredictionHorizon != 500*time.Millisecond || config.TrackerLostAfter != 1500*time.Millisecond || config.TrackerCloseAfter != 4*time.Second || config.TrackerLifecycleSnapshotInterval != 2*time.Second || config.TrackerHistoryObservations != 90 {
		t.Fatalf("tracker config = %#v", config)
	}
}

func TestLoadRejectsInvertedTrackLifecycleThresholds(t *testing.T) {
	t.Setenv("ATLAS_AGENT_STATE_DIR", t.TempDir())
	t.Setenv("ATLAS_TRACKER_PREDICTION_HORIZON", "2s")
	t.Setenv("ATLAS_TRACKER_LOST_AFTER", "1s")
	if _, err := Load(); err == nil {
		t.Fatal("track prediction beyond lost threshold was accepted")
	}
}

func TestLoadRejectsUnsafeTrackerConfiguration(t *testing.T) {
	t.Setenv("ATLAS_AGENT_STATE_DIR", t.TempDir())
	t.Setenv("ATLAS_TRACKER_ALGORITHM", "deep_sort")
	if _, err := Load(); err == nil {
		t.Fatal("unsupported tracker algorithm was accepted")
	}
}

func TestLoadRejectsInvertedByteTrackThresholds(t *testing.T) {
	t.Setenv("ATLAS_AGENT_STATE_DIR", t.TempDir())
	t.Setenv("ATLAS_TRACKER_ALGORITHM", "byte_track")
	t.Setenv("ATLAS_BYTETRACK_TRACK_THRESHOLD", "0.7")
	t.Setenv("ATLAS_BYTETRACK_HIGH_THRESHOLD", "0.6")
	if _, err := Load(); err == nil {
		t.Fatal("inverted ByteTrack thresholds were accepted")
	}
}

func TestLoadReadsHardwareNeutralPerceptionSettings(t *testing.T) {
	stateDirectory := t.TempDir()
	socketPath := filepath.Join(t.TempDir(), "atlas-perception.sock")
	t.Setenv("ATLAS_AGENT_STATE_DIR", stateDirectory)
	t.Setenv("ATLAS_PERCEPTION_PROVIDER", "deepstream")
	t.Setenv("ATLAS_PERCEPTION_SOCKET_PATH", socketPath)
	t.Setenv("ATLAS_PERCEPTION_ADAPTER_PATH", "/opt/atlas/atlas-hailort-adapter")
	t.Setenv("ATLAS_PERCEPTION_ADAPTER_MODE", "container")

	config, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !config.PerceptionEnabled() || config.PerceptionProvider != "deepstream" || config.PerceptionSocketPath != socketPath || config.PerceptionAdapterPath != "/opt/atlas/atlas-hailort-adapter" || config.PerceptionAdapterMode != "container" {
		t.Fatalf("perception config = %#v", config)
	}
}

func TestLoadRejectsUnknownPerceptionAdapterMode(t *testing.T) {
	t.Setenv("ATLAS_AGENT_STATE_DIR", t.TempDir())
	t.Setenv("ATLAS_PERCEPTION_ADAPTER_MODE", "magic")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want adapter mode validation error")
	}
}

func TestLoadRejectsUnknownPerceptionProvider(t *testing.T) {
	t.Setenv("ATLAS_AGENT_STATE_DIR", t.TempDir())
	t.Setenv("ATLAS_PERCEPTION_PROVIDER", "cuda-magic")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want provider validation error")
	}
}

func TestLoadRejectsRelativeStateDirectory(t *testing.T) {
	t.Setenv("ATLAS_AGENT_STATE_DIR", "relative/state")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want relative-path validation error")
	}
}

func TestLoadReadsGroundStationAndFlightControllerSettings(t *testing.T) {
	t.Setenv("ATLAS_AGENT_STATE_DIR", t.TempDir())
	t.Setenv("ATLAS_GROUND_STATION_ADDR", "192.168.144.50:7555")
	t.Setenv("ATLAS_FLIGHT_CONTROLLER_BAUD_RATE", "57600")
	t.Setenv("ATLAS_MAVLINK_SYSTEM_ID", "7")
	t.Setenv("ATLAS_MAVSDK_GRPC_ADDR", "127.0.0.1:50052")
	t.Setenv("ATLAS_TELEMETRY_INTERVAL", "750ms")
	t.Setenv("ATLAS_CAMERA_TRANSPORT", "HYBRID")
	t.Setenv("ATLAS_SIYI_CAMERA_ADDR", "192.168.144.26:37260")

	config, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if config.GroundStationAddress != "192.168.144.50:7555" || config.FlightControllerBaudRate != 57600 || config.MAVLinkSystemID != 7 || config.MAVSDKGRPCAddress != "127.0.0.1:50052" || config.CameraTransport != CameraTransportHybrid || !config.CameraTransport.UsesSIYI() || !config.CameraTransport.UsesMAVSDK() || config.SIYICameraAddress != "192.168.144.26:37260" || config.TelemetryInterval != 750*time.Millisecond {
		t.Fatalf("config = %#v", config)
	}
}

func TestLoadRejectsUnknownCameraTransport(t *testing.T) {
	t.Setenv("ATLAS_AGENT_STATE_DIR", t.TempDir())
	t.Setenv("ATLAS_CAMERA_TRANSPORT", "automatic")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want camera transport validation error")
	}
}

func TestLoadRejectsTelemetryIntervalBelowOneHundredMilliseconds(t *testing.T) {
	t.Setenv("ATLAS_AGENT_STATE_DIR", t.TempDir())
	t.Setenv("ATLAS_TELEMETRY_INTERVAL", "50ms")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want telemetry interval validation error")
	}
}
