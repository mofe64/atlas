// Package config loads local process configuration for the new Atlas Agent.
package config

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/buildinfo"
)

type CameraTransport string

const (
	CameraTransportSIYIUDP CameraTransport = "siyi_udp"
	CameraTransportMAVSDK  CameraTransport = "mavsdk"
	CameraTransportHybrid  CameraTransport = "hybrid"
)

func ParseCameraTransport(value string) (CameraTransport, error) {
	transport := CameraTransport(strings.ToLower(strings.TrimSpace(value)))
	if transport == "" {
		transport = CameraTransportSIYIUDP
	}
	if !transport.Valid() {
		return "", errors.New("ATLAS_CAMERA_TRANSPORT must be one of: siyi_udp, mavsdk, hybrid")
	}
	return transport, nil
}

func (transport CameraTransport) Valid() bool {
	return transport == CameraTransportSIYIUDP || transport == CameraTransportMAVSDK || transport == CameraTransportHybrid
}

func (transport CameraTransport) UsesSIYI() bool {
	return transport == CameraTransportSIYIUDP || transport == CameraTransportHybrid
}

func (transport CameraTransport) UsesMAVSDK() bool {
	return transport == CameraTransportMAVSDK || transport == CameraTransportHybrid
}

type Config struct {
	StateDirectory                            string
	GroundStationAddress                      string
	DroneName                                 string
	AgentVersion                              string
	ProtocolVersion                           string
	HeartbeatInterval                         time.Duration
	TelemetryInterval                         time.Duration
	MAVSDKGRPCAddress                         string
	NavigationSocketPath                      string
	SpatialEnabled                            bool
	SpatialCloudSocketPath                    string
	SpatialSourceID                           string
	CameraTransport                           CameraTransport
	SIYICameraAddress                         string
	PerceptionProvider                        string
	PerceptionSocketPath                      string
	PerceptionAdapterPath                     string
	PerceptionAdapterMode                     string
	TrackerAlgorithm                          string
	TrackerMaxTimestampGap                    time.Duration
	TrackerCameraMotionMinConfidence          float64
	TrackerConfirmationObservations           int
	TrackerPredictionHorizon                  time.Duration
	TrackerLostAfter                          time.Duration
	TrackerCloseAfter                         time.Duration
	TrackerLifecycleSnapshotInterval          time.Duration
	TrackerHistoryObservations                int
	ByteTrackWorkerPath                       string
	ByteTrackRequestTimeout                   time.Duration
	ByteTrackFrameRate                        int
	ByteTrackTrackThreshold                   float64
	ByteTrackHighThreshold                    float64
	ByteTrackMatchThreshold                   float64
	ByteTrackBufferFrames                     int
	GeolocationBoresightAngularUncertaintyDeg float64
	GeolocationBoresightAlignmentReference    string
	GimbalFollowUpdateInterval                time.Duration
	GimbalFollowTrackFreshness                time.Duration
	GimbalFollowHoldTimeout                   time.Duration
	GimbalFollowDeadband                      float64
	GimbalFollowPitchGain                     float64
	GimbalFollowYawGain                       float64
	GimbalFollowMaxPitchRate                  float64
	GimbalFollowMaxYawRate                    float64
	GimbalFollowMaxPitchAcceleration          float64
	GimbalFollowMaxYawAcceleration            float64
	GimbalFollowMinPitch                      float64
	GimbalFollowMaxPitch                      float64
	GimbalFollowMinYaw                        float64
	GimbalFollowMaxYaw                        float64
	GimbalFollowLimitMargin                   float64
	AircraftFollowEnabled                     bool
	AircraftFollowValidationReference         string
	FlightControllerUID                       string
	FlightControllerSerial                    string
	VehicleType                               string
	FlightControllerTransport                 string
	FlightControllerEndpoint                  string
	FlightControllerBaudRate                  uint32
	MAVLinkSystemID                           uint32
	MAVLinkComponentID                        uint32
}

func Load() (Config, error) {
	stateDirectory := strings.TrimSpace(os.Getenv("ATLAS_AGENT_STATE_DIR"))
	if stateDirectory == "" {
		userConfigDirectory, err := os.UserConfigDir()
		if err != nil {
			return Config{}, err
		}
		stateDirectory = filepath.Join(userConfigDirectory, "atlas-agent")
	}
	if !filepath.IsAbs(stateDirectory) {
		return Config{}, errors.New("ATLAS_AGENT_STATE_DIR must be an absolute path")
	}
	baudRate, err := uint32Environment("ATLAS_FLIGHT_CONTROLLER_BAUD_RATE", 921600)
	if err != nil {
		return Config{}, err
	}
	systemID, err := uint32Environment("ATLAS_MAVLINK_SYSTEM_ID", 1)
	if err != nil || systemID > 255 {
		return Config{}, errors.New("ATLAS_MAVLINK_SYSTEM_ID must be between 0 and 255")
	}
	componentID, err := uint32Environment("ATLAS_MAVLINK_COMPONENT_ID", 1)
	if err != nil || componentID > 255 {
		return Config{}, errors.New("ATLAS_MAVLINK_COMPONENT_ID must be between 0 and 255")
	}
	telemetryInterval, err := durationEnvironment("ATLAS_TELEMETRY_INTERVAL", time.Second)
	if err != nil || telemetryInterval < 100*time.Millisecond {
		return Config{}, errors.New("ATLAS_TELEMETRY_INTERVAL must be a duration of at least 100ms")
	}
	perceptionProvider := strings.ToLower(environmentOrDefault("ATLAS_PERCEPTION_PROVIDER", "disabled"))
	if !validPerceptionProvider(perceptionProvider) {
		return Config{}, errors.New("ATLAS_PERCEPTION_PROVIDER must be one of: disabled, external, hailo, deepstream, tensorrt, onnx")
	}
	perceptionSocketPath := environmentOrDefault(
		"ATLAS_PERCEPTION_SOCKET_PATH",
		filepath.Join(stateDirectory, "perception", "runtime.sock"),
	)
	if !filepath.IsAbs(perceptionSocketPath) {
		return Config{}, errors.New("ATLAS_PERCEPTION_SOCKET_PATH must be an absolute path")
	}
	navigationSocketPath := environmentOrDefault(
		"ATLAS_NAVIGATION_SOCKET_PATH",
		filepath.Join(stateDirectory, "navigation.sock"),
	)
	if !filepath.IsAbs(navigationSocketPath) {
		return Config{}, errors.New("ATLAS_NAVIGATION_SOCKET_PATH must be an absolute path")
	}
	spatialEnabled, err := booleanEnvironment("ATLAS_SPATIAL_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	spatialCloudSocketPath := environmentOrDefault(
		"ATLAS_SPATIAL_CLOUD_SOCKET_PATH",
		filepath.Join(stateDirectory, "spatial-cloud.sock"),
	)
	if !filepath.IsAbs(spatialCloudSocketPath) {
		return Config{}, errors.New("ATLAS_SPATIAL_CLOUD_SOCKET_PATH must be an absolute path")
	}
	perceptionAdapterMode := strings.ToLower(environmentOrDefault("ATLAS_PERCEPTION_ADAPTER_MODE", "process"))
	if perceptionAdapterMode != "process" && perceptionAdapterMode != "container" {
		return Config{}, errors.New("ATLAS_PERCEPTION_ADAPTER_MODE must be one of: process, container")
	}
	trackerAlgorithm := strings.ToLower(environmentOrDefault("ATLAS_TRACKER_ALGORITHM", "byte_track"))
	if trackerAlgorithm != "disabled" && trackerAlgorithm != "byte_track" && trackerAlgorithm != "byte_track_cmc" {
		return Config{}, errors.New("ATLAS_TRACKER_ALGORITHM must be one of: disabled, byte_track, byte_track_cmc")
	}
	trackerMaxTimestampGap, err := durationEnvironment("ATLAS_TRACKER_MAX_TIMESTAMP_GAP", 2*time.Second)
	if err != nil || trackerMaxTimestampGap < 100*time.Millisecond || trackerMaxTimestampGap > 30*time.Second {
		return Config{}, errors.New("ATLAS_TRACKER_MAX_TIMESTAMP_GAP must be between 100ms and 30s")
	}
	trackerCameraMotionMinConfidence, err := boundedFloatEnvironment("ATLAS_TRACKER_CMC_MIN_CONFIDENCE", 0.25, 0, 1)
	if err != nil {
		return Config{}, err
	}
	trackerConfirmationObservations, err := boundedIntegerEnvironment("ATLAS_TRACKER_CONFIRMATION_OBSERVATIONS", 2, 1, 10)
	if err != nil {
		return Config{}, err
	}
	trackerPredictionHorizon, err := durationEnvironment("ATLAS_TRACKER_PREDICTION_HORIZON", 750*time.Millisecond)
	if err != nil || trackerPredictionHorizon < 100*time.Millisecond || trackerPredictionHorizon > 10*time.Second {
		return Config{}, errors.New("ATLAS_TRACKER_PREDICTION_HORIZON must be between 100ms and 10s")
	}
	trackerLostAfter, err := durationEnvironment("ATLAS_TRACKER_LOST_AFTER", time.Second)
	if err != nil || trackerLostAfter < trackerPredictionHorizon || trackerLostAfter > 30*time.Second {
		return Config{}, errors.New("ATLAS_TRACKER_LOST_AFTER must be at least the prediction horizon and at most 30s")
	}
	trackerCloseAfter, err := durationEnvironment("ATLAS_TRACKER_CLOSE_AFTER", 3*time.Second)
	if err != nil || trackerCloseAfter <= trackerLostAfter || trackerCloseAfter > 2*time.Minute {
		return Config{}, errors.New("ATLAS_TRACKER_CLOSE_AFTER must be greater than the lost threshold and at most 2m")
	}
	trackerLifecycleSnapshotInterval, err := durationEnvironment("ATLAS_TRACKER_LIFECYCLE_SNAPSHOT_INTERVAL", time.Second)
	if err != nil || trackerLifecycleSnapshotInterval < 100*time.Millisecond || trackerLifecycleSnapshotInterval > 30*time.Second {
		return Config{}, errors.New("ATLAS_TRACKER_LIFECYCLE_SNAPSHOT_INTERVAL must be between 100ms and 30s")
	}
	trackerHistoryObservations, err := boundedIntegerEnvironment("ATLAS_TRACKER_HISTORY_OBSERVATIONS", 60, 2, 600)
	if err != nil {
		return Config{}, err
	}
	byteTrackRequestTimeout, err := durationEnvironment("ATLAS_BYTETRACK_REQUEST_TIMEOUT", 250*time.Millisecond)
	if err != nil || byteTrackRequestTimeout < 10*time.Millisecond || byteTrackRequestTimeout > 5*time.Second {
		return Config{}, errors.New("ATLAS_BYTETRACK_REQUEST_TIMEOUT must be between 10ms and 5s")
	}
	byteTrackFrameRate, err := boundedIntegerEnvironment("ATLAS_BYTETRACK_FRAME_RATE", 30, 1, 240)
	if err != nil {
		return Config{}, err
	}
	geolocationBoresightAngularUncertaintyDeg, err := boundedFloatEnvironment("ATLAS_GEOLOCATION_BORESIGHT_ANGULAR_UNCERTAINTY_DEG", 10, 0.1, 44.9)
	if err != nil {
		return Config{}, err
	}
	geolocationBoresightAlignmentReference := strings.TrimSpace(os.Getenv("ATLAS_GEOLOCATION_BORESIGHT_ALIGNMENT_REFERENCE"))
	if len(geolocationBoresightAlignmentReference) > 240 {
		return Config{}, errors.New("ATLAS_GEOLOCATION_BORESIGHT_ALIGNMENT_REFERENCE cannot exceed 240 characters")
	}
	byteTrackTrackThreshold, err := boundedFloatEnvironment("ATLAS_BYTETRACK_TRACK_THRESHOLD", 0.50, 0, 1)
	if err != nil {
		return Config{}, err
	}
	byteTrackHighThreshold, err := boundedFloatEnvironment("ATLAS_BYTETRACK_HIGH_THRESHOLD", 0.60, 0, 1)
	if err != nil {
		return Config{}, err
	}
	byteTrackMatchThreshold, err := boundedFloatEnvironment("ATLAS_BYTETRACK_MATCH_THRESHOLD", 0.80, 0, 1)
	if err != nil {
		return Config{}, err
	}
	byteTrackBufferFrames, err := boundedIntegerEnvironment("ATLAS_BYTETRACK_BUFFER_FRAMES", 30, 1, 300)
	if err != nil {
		return Config{}, err
	}
	if byteTrackTrackThreshold >= byteTrackHighThreshold {
		return Config{}, errors.New("ByteTrack thresholds require TRACK < HIGH")
	}
	gimbalFollowUpdateInterval, err := durationEnvironment("ATLAS_GIMBAL_FOLLOW_UPDATE_INTERVAL", 100*time.Millisecond)
	if err != nil || gimbalFollowUpdateInterval < 50*time.Millisecond || gimbalFollowUpdateInterval > 500*time.Millisecond {
		return Config{}, errors.New("ATLAS_GIMBAL_FOLLOW_UPDATE_INTERVAL must be between 50ms and 500ms")
	}
	gimbalFollowTrackFreshness, err := durationEnvironment("ATLAS_GIMBAL_FOLLOW_TRACK_FRESHNESS", 350*time.Millisecond)
	if err != nil || gimbalFollowTrackFreshness < gimbalFollowUpdateInterval || gimbalFollowTrackFreshness > 2*time.Second {
		return Config{}, errors.New("ATLAS_GIMBAL_FOLLOW_TRACK_FRESHNESS must be at least the update interval and at most 2s")
	}
	gimbalFollowHoldTimeout, err := durationEnvironment("ATLAS_GIMBAL_FOLLOW_HOLD_TIMEOUT", 2*time.Second)
	if err != nil || gimbalFollowHoldTimeout < gimbalFollowTrackFreshness || gimbalFollowHoldTimeout > 10*time.Second {
		return Config{}, errors.New("ATLAS_GIMBAL_FOLLOW_HOLD_TIMEOUT must be at least track freshness and at most 10s")
	}
	gimbalFollowDeadband, err := boundedFloatEnvironment("ATLAS_GIMBAL_FOLLOW_DEADBAND", 0.025, 0, 0.25)
	if err != nil {
		return Config{}, err
	}
	gimbalFollowPitchGain, err := boundedFloatEnvironment("ATLAS_GIMBAL_FOLLOW_PITCH_GAIN", 60, 1, 360)
	if err != nil {
		return Config{}, err
	}
	gimbalFollowYawGain, err := boundedFloatEnvironment("ATLAS_GIMBAL_FOLLOW_YAW_GAIN", 80, 1, 360)
	if err != nil {
		return Config{}, err
	}
	gimbalFollowMaxPitchRate, err := boundedFloatEnvironment("ATLAS_GIMBAL_FOLLOW_MAX_PITCH_RATE", 20, 1, 90)
	if err != nil {
		return Config{}, err
	}
	gimbalFollowMaxYawRate, err := boundedFloatEnvironment("ATLAS_GIMBAL_FOLLOW_MAX_YAW_RATE", 30, 1, 90)
	if err != nil {
		return Config{}, err
	}
	gimbalFollowMaxPitchAcceleration, err := boundedFloatEnvironment("ATLAS_GIMBAL_FOLLOW_MAX_PITCH_ACCELERATION", 60, 1, 360)
	if err != nil {
		return Config{}, err
	}
	gimbalFollowMaxYawAcceleration, err := boundedFloatEnvironment("ATLAS_GIMBAL_FOLLOW_MAX_YAW_ACCELERATION", 90, 1, 360)
	if err != nil {
		return Config{}, err
	}
	gimbalFollowMinPitch, err := boundedFloatEnvironment("ATLAS_GIMBAL_FOLLOW_MIN_PITCH", -90, -180, 180)
	if err != nil {
		return Config{}, err
	}
	gimbalFollowMaxPitch, err := boundedFloatEnvironment("ATLAS_GIMBAL_FOLLOW_MAX_PITCH", 30, -180, 180)
	if err != nil || gimbalFollowMinPitch >= gimbalFollowMaxPitch {
		return Config{}, errors.New("ATLAS_GIMBAL_FOLLOW_MIN_PITCH must be less than ATLAS_GIMBAL_FOLLOW_MAX_PITCH")
	}
	gimbalFollowMinYaw, err := boundedFloatEnvironment("ATLAS_GIMBAL_FOLLOW_MIN_YAW", -180, -360, 360)
	if err != nil {
		return Config{}, err
	}
	gimbalFollowMaxYaw, err := boundedFloatEnvironment("ATLAS_GIMBAL_FOLLOW_MAX_YAW", 180, -360, 360)
	if err != nil || gimbalFollowMinYaw >= gimbalFollowMaxYaw {
		return Config{}, errors.New("ATLAS_GIMBAL_FOLLOW_MIN_YAW must be less than ATLAS_GIMBAL_FOLLOW_MAX_YAW")
	}
	gimbalFollowLimitMargin, err := boundedFloatEnvironment("ATLAS_GIMBAL_FOLLOW_LIMIT_MARGIN", 2, 0, 15)
	if err != nil || gimbalFollowLimitMargin*2 >= gimbalFollowMaxPitch-gimbalFollowMinPitch || gimbalFollowLimitMargin*2 >= gimbalFollowMaxYaw-gimbalFollowMinYaw {
		return Config{}, errors.New("ATLAS_GIMBAL_FOLLOW_LIMIT_MARGIN must fit inside both configured angle ranges")
	}
	cameraTransport, err := ParseCameraTransport(os.Getenv("ATLAS_CAMERA_TRANSPORT"))
	if err != nil {
		return Config{}, err
	}
	aircraftFollowEnabled, err := booleanEnvironment("ATLAS_AIRCRAFT_FOLLOW_ENABLED", false)
	if err != nil {
		return Config{}, err
	}
	aircraftFollowValidationReference := strings.TrimSpace(os.Getenv("ATLAS_AIRCRAFT_FOLLOW_VALIDATION_REFERENCE"))
	if len(aircraftFollowValidationReference) > 240 {
		return Config{}, errors.New("ATLAS_AIRCRAFT_FOLLOW_VALIDATION_REFERENCE cannot exceed 240 characters")
	}
	if aircraftFollowEnabled && aircraftFollowValidationReference == "" {
		return Config{}, errors.New("ATLAS_AIRCRAFT_FOLLOW_ENABLED requires ATLAS_AIRCRAFT_FOLLOW_VALIDATION_REFERENCE")
	}
	if aircraftFollowEnabled && geolocationBoresightAlignmentReference == "" {
		return Config{}, errors.New("ATLAS_AIRCRAFT_FOLLOW_ENABLED requires a physical ATLAS_GEOLOCATION_BORESIGHT_ALIGNMENT_REFERENCE")
	}
	return Config{
		StateDirectory:                            filepath.Clean(stateDirectory),
		GroundStationAddress:                      environmentOrDefault("ATLAS_GROUND_STATION_ADDR", "192.168.144.50:7443"),
		DroneName:                                 environmentOrDefault("ATLAS_DRONE_NAME", "Atlas Drone"),
		AgentVersion:                              environmentOrDefault("ATLAS_AGENT_VERSION", buildinfo.Version),
		ProtocolVersion:                           "1",
		HeartbeatInterval:                         5 * time.Second,
		TelemetryInterval:                         telemetryInterval,
		MAVSDKGRPCAddress:                         environmentOrDefault("ATLAS_MAVSDK_GRPC_ADDR", "127.0.0.1:50051"),
		NavigationSocketPath:                      filepath.Clean(navigationSocketPath),
		SpatialEnabled:                            spatialEnabled,
		SpatialCloudSocketPath:                    filepath.Clean(spatialCloudSocketPath),
		SpatialSourceID:                           environmentOrDefault("ATLAS_SPATIAL_SOURCE_ID", "front-depth"),
		CameraTransport:                           cameraTransport,
		SIYICameraAddress:                         environmentOrDefault("ATLAS_SIYI_CAMERA_ADDR", "192.168.144.25:37260"),
		PerceptionProvider:                        perceptionProvider,
		PerceptionSocketPath:                      filepath.Clean(perceptionSocketPath),
		PerceptionAdapterPath:                     environmentOrDefault("ATLAS_PERCEPTION_ADAPTER_PATH", "atlas-hailort-adapter"),
		PerceptionAdapterMode:                     perceptionAdapterMode,
		TrackerAlgorithm:                          trackerAlgorithm,
		TrackerMaxTimestampGap:                    trackerMaxTimestampGap,
		TrackerCameraMotionMinConfidence:          trackerCameraMotionMinConfidence,
		TrackerConfirmationObservations:           trackerConfirmationObservations,
		TrackerPredictionHorizon:                  trackerPredictionHorizon,
		TrackerLostAfter:                          trackerLostAfter,
		TrackerCloseAfter:                         trackerCloseAfter,
		TrackerLifecycleSnapshotInterval:          trackerLifecycleSnapshotInterval,
		TrackerHistoryObservations:                trackerHistoryObservations,
		ByteTrackWorkerPath:                       environmentOrDefault("ATLAS_BYTETRACK_WORKER_PATH", "atlas-bytetrack-worker"),
		ByteTrackRequestTimeout:                   byteTrackRequestTimeout,
		ByteTrackFrameRate:                        byteTrackFrameRate,
		ByteTrackTrackThreshold:                   byteTrackTrackThreshold,
		ByteTrackHighThreshold:                    byteTrackHighThreshold,
		ByteTrackMatchThreshold:                   byteTrackMatchThreshold,
		ByteTrackBufferFrames:                     byteTrackBufferFrames,
		GeolocationBoresightAngularUncertaintyDeg: geolocationBoresightAngularUncertaintyDeg,
		GeolocationBoresightAlignmentReference:    geolocationBoresightAlignmentReference,
		GimbalFollowUpdateInterval:                gimbalFollowUpdateInterval,
		GimbalFollowTrackFreshness:                gimbalFollowTrackFreshness,
		GimbalFollowHoldTimeout:                   gimbalFollowHoldTimeout,
		GimbalFollowDeadband:                      gimbalFollowDeadband,
		GimbalFollowPitchGain:                     gimbalFollowPitchGain,
		GimbalFollowYawGain:                       gimbalFollowYawGain,
		GimbalFollowMaxPitchRate:                  gimbalFollowMaxPitchRate,
		GimbalFollowMaxYawRate:                    gimbalFollowMaxYawRate,
		GimbalFollowMaxPitchAcceleration:          gimbalFollowMaxPitchAcceleration,
		GimbalFollowMaxYawAcceleration:            gimbalFollowMaxYawAcceleration,
		GimbalFollowMinPitch:                      gimbalFollowMinPitch,
		GimbalFollowMaxPitch:                      gimbalFollowMaxPitch,
		GimbalFollowMinYaw:                        gimbalFollowMinYaw,
		GimbalFollowMaxYaw:                        gimbalFollowMaxYaw,
		GimbalFollowLimitMargin:                   gimbalFollowLimitMargin,
		AircraftFollowEnabled:                     aircraftFollowEnabled,
		AircraftFollowValidationReference:         aircraftFollowValidationReference,
		FlightControllerUID:                       strings.TrimSpace(os.Getenv("ATLAS_FLIGHT_CONTROLLER_UID")),
		FlightControllerSerial:                    strings.TrimSpace(os.Getenv("ATLAS_FLIGHT_CONTROLLER_SERIAL")),
		VehicleType:                               environmentOrDefault("ATLAS_VEHICLE_TYPE", "unknown"),
		FlightControllerTransport:                 environmentOrDefault("ATLAS_FLIGHT_CONTROLLER_TRANSPORT", "serial"),
		FlightControllerEndpoint:                  environmentOrDefault("ATLAS_FLIGHT_CONTROLLER_ENDPOINT", "/dev/serial0"),
		FlightControllerBaudRate:                  baudRate,
		MAVLinkSystemID:                           systemID,
		MAVLinkComponentID:                        componentID,
	}, nil
}

func booleanEnvironment(name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, errors.New(name + " must be true or false")
	}
	return parsed, nil
}

func (config Config) PerceptionEnabled() bool {
	return config.PerceptionProvider != "" && config.PerceptionProvider != "disabled"
}

func durationEnvironment(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, errors.New(name + " must be a valid duration")
	}
	return parsed, nil
}

func environmentOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func uint32Environment(name string, fallback uint32) (uint32, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0, errors.New(name + " must be an unsigned integer")
	}
	return uint32(parsed), nil
}

func boundedIntegerEnvironment(name string, fallback, minimum, maximum int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s must be an integer between %d and %d", name, minimum, maximum)
	}
	return parsed, nil
}

func boundedFloatEnvironment(name string, fallback, minimum, maximum float64) (float64, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < minimum || parsed > maximum {
		return 0, fmt.Errorf("%s must be between %.2f and %.2f", name, minimum, maximum)
	}
	return parsed, nil
}

func validPerceptionProvider(value string) bool {
	switch value {
	case "disabled", "external", "hailo", "deepstream", "tensorrt", "onnx":
		return true
	default:
		return false
	}
}
