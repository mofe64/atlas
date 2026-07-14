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
	if config.PerceptionProvider != "disabled" || config.PerceptionEnabled() {
		t.Fatalf("perception config = provider %q enabled %v", config.PerceptionProvider, config.PerceptionEnabled())
	}
	if config.PerceptionSocketPath != filepath.Join(want, "perception", "runtime.sock") {
		t.Fatalf("PerceptionSocketPath = %q", config.PerceptionSocketPath)
	}
	if config.PerceptionAdapterPath != "atlas-hailort-adapter" {
		t.Fatalf("PerceptionAdapterPath = %q", config.PerceptionAdapterPath)
	}
	if config.PerceptionAdapterMode != "process" {
		t.Fatalf("PerceptionAdapterMode = %q", config.PerceptionAdapterMode)
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
	t.Setenv("ATLAS_SIYI_CAMERA_ADDR", "192.168.144.26:37260")

	config, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if config.GroundStationAddress != "192.168.144.50:7555" || config.FlightControllerBaudRate != 57600 || config.MAVLinkSystemID != 7 || config.MAVSDKGRPCAddress != "127.0.0.1:50052" || config.SIYICameraAddress != "192.168.144.26:37260" || config.TelemetryInterval != 750*time.Millisecond {
		t.Fatalf("config = %#v", config)
	}
}

func TestLoadRejectsTelemetryIntervalBelowOneHundredMilliseconds(t *testing.T) {
	t.Setenv("ATLAS_AGENT_STATE_DIR", t.TempDir())
	t.Setenv("ATLAS_TELEMETRY_INTERVAL", "50ms")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want telemetry interval validation error")
	}
}
