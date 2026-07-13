// Package config loads local process configuration for the new Atlas Agent.
package config

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/buildinfo"
)

type Config struct {
	StateDirectory            string
	GroundStationAddress      string
	DroneName                 string
	AgentVersion              string
	ProtocolVersion           string
	HeartbeatInterval         time.Duration
	TelemetryInterval         time.Duration
	MAVSDKGRPCAddress         string
	SIYICameraAddress         string
	PerceptionProvider        string
	PerceptionSocketPath      string
	PerceptionAdapterPath     string
	FlightControllerUID       string
	FlightControllerSerial    string
	VehicleType               string
	FlightControllerTransport string
	FlightControllerEndpoint  string
	FlightControllerBaudRate  uint32
	MAVLinkSystemID           uint32
	MAVLinkComponentID        uint32
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

	return Config{
		StateDirectory:            filepath.Clean(stateDirectory),
		GroundStationAddress:      environmentOrDefault("ATLAS_GROUND_STATION_ADDR", "192.168.144.50:7443"),
		DroneName:                 environmentOrDefault("ATLAS_DRONE_NAME", "Atlas Drone"),
		AgentVersion:              environmentOrDefault("ATLAS_AGENT_VERSION", buildinfo.Version),
		ProtocolVersion:           "1",
		HeartbeatInterval:         5 * time.Second,
		TelemetryInterval:         telemetryInterval,
		MAVSDKGRPCAddress:         environmentOrDefault("ATLAS_MAVSDK_GRPC_ADDR", "127.0.0.1:50051"),
		SIYICameraAddress:         environmentOrDefault("ATLAS_SIYI_CAMERA_ADDR", "192.168.144.25:37260"),
		PerceptionProvider:        perceptionProvider,
		PerceptionSocketPath:      filepath.Clean(perceptionSocketPath),
		PerceptionAdapterPath:     environmentOrDefault("ATLAS_PERCEPTION_ADAPTER_PATH", "atlas-hailort-adapter"),
		FlightControllerUID:       strings.TrimSpace(os.Getenv("ATLAS_FLIGHT_CONTROLLER_UID")),
		FlightControllerSerial:    strings.TrimSpace(os.Getenv("ATLAS_FLIGHT_CONTROLLER_SERIAL")),
		VehicleType:               environmentOrDefault("ATLAS_VEHICLE_TYPE", "unknown"),
		FlightControllerTransport: environmentOrDefault("ATLAS_FLIGHT_CONTROLLER_TRANSPORT", "serial"),
		FlightControllerEndpoint:  environmentOrDefault("ATLAS_FLIGHT_CONTROLLER_ENDPOINT", "/dev/serial0"),
		FlightControllerBaudRate:  baudRate,
		MAVLinkSystemID:           systemID,
		MAVLinkComponentID:        componentID,
	}, nil
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

func validPerceptionProvider(value string) bool {
	switch value {
	case "disabled", "external", "hailo", "deepstream", "tensorrt", "onnx":
		return true
	default:
		return false
	}
}
