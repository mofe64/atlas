// Package onboardsetup implements discovery, configuration, installation, and
// diagnostics for the Atlas onboard runtime.
package onboardsetup

import (
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sunnyside/atlas/atlas-agent/internal/aircraftprofile"
	agentconfig "github.com/sunnyside/atlas/atlas-agent/internal/config"
)

const (
	SupportedOSID            = "ubuntu"
	SupportedOSVersion       = "24.04"
	DefaultBaudRate          = 921600
	DefaultGroundAddr        = "192.168.144.50:7443"
	DefaultA8RTSPURL         = "rtsp://192.168.144.25:8554/main.264"
	DefaultSIYIAddr          = "192.168.144.25:37260"
	DefaultMAVSDKAddr        = "127.0.0.1:50051"
	AdapterModeProcess       = "process"
	AdapterModeContainer     = "container"
	SpatialProviderDepthAI   = "depthai"
	SpatialProviderSynthetic = "synthetic"
)

type Paths struct {
	Root                  string
	ConfigFile            string
	StateDirectory        string
	RuntimeDirectory      string
	AgentBinary           string
	SetupBinary           string
	MAVSDKBinary          string
	HailoAdapter          string
	ByteTrackWorker       string
	HailoSetupBinary      string
	HailoContainerEnv     string
	HailoContainerService string
	AircraftProfilesDir   string
	AircraftProfileConfig string
	SpatialConfigFile     string
	SpatialRuntimeBinary  string
	SpatialCheck          string
	SpatialService        string
	ReleaseManifest       string
	AgentService          string
	MAVSDKService         string
	DefaultModel          string
	DefaultPostprocessSO  string
}

// DefaultPaths defines the canonical filesystem layout for the Atlas Agent
func DefaultPaths(root string) Paths {
	// helper function to place a path under the system root dir or another supplied dir
	rooted := func(path string) string {
		// if root is empty (caller did not supply a root) or "/" (the root directory), return the path unchanged
		if root == "" || root == "/" {
			return path
		}
		// otherwise, join the supplied root dir with the path
		// we strip leading "/" from the path to avoid double slashes
		// eg root =  "/tmp/atlas-test", path = "/etc/atlas-agent/atlas-agent.env" -> "/tmp/atlas-test/etc/atlas-agent/atlas-agent.env"
		return filepath.Join(root, strings.TrimPrefix(path, "/"))
	}

	// return the Paths struct with the paths populated using the rooted function
	return Paths{
		Root:                  root,
		ConfigFile:            rooted("/etc/atlas-agent/atlas-agent.env"),
		StateDirectory:        rooted("/var/lib/atlas-agent"),
		RuntimeDirectory:      rooted("/run/atlas-agent"),
		AgentBinary:           rooted("/usr/bin/atlas-agent"),
		SetupBinary:           rooted("/usr/bin/atlas-setup"),
		MAVSDKBinary:          rooted("/usr/libexec/atlas-agent/mavsdk_server"),
		HailoAdapter:          rooted("/usr/libexec/atlas-agent/atlas-hailort-adapter"),
		ByteTrackWorker:       rooted("/usr/libexec/atlas-agent/atlas-bytetrack-worker"),
		HailoSetupBinary:      rooted("/usr/sbin/atlas-hailo-setup"),
		HailoContainerEnv:     rooted("/etc/atlas-agent/hailo-container.env"),
		HailoContainerService: rooted("/usr/lib/systemd/system/atlas-hailo-adapter.service"),
		AircraftProfilesDir:   rooted("/usr/share/atlas-agent/aircraft-profiles"),
		AircraftProfileConfig: rooted("/etc/atlas-agent/aircraft-profile.json"),
		SpatialConfigFile:     rooted("/etc/atlas-agent/spatial.env"),
		SpatialRuntimeBinary:  rooted("/opt/atlas-spatial-runtime/bin/atlas-spatial-runtime"),
		SpatialCheck:          rooted("/opt/atlas-spatial-runtime/bin/atlas-spatial-check"),
		SpatialService:        rooted("/usr/lib/systemd/system/atlas-spatial-runtime.service"),
		ReleaseManifest:       rooted("/usr/share/atlas-agent/release.env"),
		AgentService:          rooted("/usr/lib/systemd/system/atlas-agent.service"),
		MAVSDKService:         rooted("/usr/lib/systemd/system/atlas-mavsdk.service"),
		DefaultModel:          rooted("/usr/share/atlas-agent/models/objects.hef"),
		DefaultPostprocessSO:  rooted("/usr/lib/aarch64-linux-gnu/hailo/tappas/post_processes/libyolo_hailortpp_post.so"),
	}
}

type OSRelease struct {
	ID         string
	VersionID  string
	PrettyName string
}

func (release OSRelease) Supported() bool {
	return release.ID == SupportedOSID && release.VersionID == SupportedOSVersion
}

type SerialCandidate struct {
	Path       string
	Resolved   string
	Persistent bool
}

func (candidate SerialCandidate) Label() string {
	if candidate.Resolved == "" || candidate.Resolved == candidate.Path {
		return candidate.Path
	}
	return fmt.Sprintf("%s -> %s", candidate.Path, candidate.Resolved)
}

type HailoStatus struct {
	RuntimeMode                   string
	PCIVisible                    bool
	DeviceNodeReady               bool
	RuntimeInstalled              bool
	DeviceReady                   bool
	GStreamerReady                bool
	PythonReady                   bool
	VersionsCompatible            bool
	ContainerReady                bool
	Accelerator                   string
	IdentifyOutput                string
	ContainerImage                string
	ContainerName                 string
	HostDriverPackageVersion      string
	HostDriverVersion             string
	HostFirmwareVersion           string
	FirmwareVersion               string
	UserspaceVersion              string
	TAPPASVersion                 string
	ExpectedDriverVersion         string
	ExpectedDriverPackageVersion  string
	ExpectedFirmwareVersion       string
	ExpectedDeviceFirmwareVersion string
	ExpectedUserspaceVersion      string
	ExpectedTAPPASVersion         string
	MissingComponents             []string
}

func (status HailoStatus) Ready() bool {
	if status.RuntimeMode == AdapterModeContainer {
		return status.PCIVisible && status.DeviceNodeReady && status.RuntimeInstalled && status.DeviceReady && status.GStreamerReady && status.PythonReady && status.VersionsCompatible
	}
	return status.PCIVisible && status.RuntimeInstalled && status.DeviceReady && status.GStreamerReady && status.PythonReady
}

type RTSPStatus struct {
	Reachable bool
	Codec     string
	Width     string
	Height    string
	Error     string
}

type SpatialStatus struct {
	Configured       bool
	DevicePresent    bool
	Provider         string
	DeviceID         string
	Model            string
	USBTransport     string
	USBSpeedMbps     int
	RuntimeInstalled bool
}

type Discovery struct {
	OS                    OSRelease
	Architecture          string
	BoardModel            string
	Serial                []SerialCandidate
	Hailo                 HailoStatus
	Camera                RTSPStatus
	Spatial               SpatialStatus
	GroundReachable       bool
	ExistingConfig        map[string]string
	ExistingSpatialConfig map[string]string
}

func (discovery Discovery) PlatformSupported() bool {
	return discovery.OS.Supported() && discovery.Architecture == "arm64" && strings.Contains(strings.ToLower(discovery.BoardModel), "raspberry pi 5")
}

type InstallConfig struct {
	AircraftProfileID         string
	AircraftProfileSourcePath string
	AircraftProfile           aircraftprofile.Profile
	DroneName                 string
	GroundStationAddress      string
	SerialDevice              string
	BaudRate                  uint32
	MAVLinkSystemID           uint32
	MAVLinkComponentID        uint32
	A8RTSPURL                 string
	CameraTransport           agentconfig.CameraTransport
	SIYICameraAddress         string
	PerceptionEnabled         bool
	PerceptionAdapterMode     string
	HailoAccelerator          string
	ModelPath                 string
	PostprocessSO             string
	PostprocessFunction       string
	SpatialEnabled            bool
	SpatialProvider           string
	SpatialDeviceID           string
	SpatialModel              string
	SpatialUSBTransport       string
	SpatialFrameID            string
	AgentVersion              string
}

func DefaultInstallConfig(paths Paths) InstallConfig {
	return InstallConfig{
		DroneName:             "Atlas Drone",
		GroundStationAddress:  DefaultGroundAddr,
		BaudRate:              DefaultBaudRate,
		MAVLinkSystemID:       1,
		MAVLinkComponentID:    1,
		A8RTSPURL:             DefaultA8RTSPURL,
		CameraTransport:       agentconfig.CameraTransportSIYIUDP,
		SIYICameraAddress:     DefaultSIYIAddr,
		PerceptionAdapterMode: AdapterModeProcess,
		HailoAccelerator:      "hailo-8l",
		ModelPath:             paths.DefaultModel,
		PostprocessSO:         paths.DefaultPostprocessSO,
		PostprocessFunction:   "filter",
		SpatialUSBTransport:   "unknown",
		SpatialFrameID:        "oak_rgb_camera_optical_frame",
		AgentVersion:          "unknown",
	}
}

func (config InstallConfig) Validate(paths Paths) error {
	if strings.TrimSpace(config.AircraftProfileID) == "" {
		return fmt.Errorf("aircraft profile selection is required")
	}
	if !filepath.IsAbs(config.AircraftProfileSourcePath) {
		return fmt.Errorf("aircraft profile source path must be absolute")
	}
	if config.AircraftProfile.ProfileID != config.AircraftProfileID {
		return fmt.Errorf("selected aircraft profile id does not match the loaded profile")
	}
	if err := config.AircraftProfile.Validate(); err != nil {
		return fmt.Errorf("selected aircraft profile is invalid: %w", err)
	}
	if strings.TrimSpace(config.DroneName) == "" {
		return fmt.Errorf("drone name is required")
	}
	if strings.TrimSpace(config.GroundStationAddress) == "" {
		return fmt.Errorf("ground station address is required")
	}
	if !filepath.IsAbs(config.SerialDevice) {
		return fmt.Errorf("flight-controller serial device must be an absolute path")
	}
	if config.BaudRate == 0 {
		return fmt.Errorf("flight-controller baud rate must be positive")
	}
	if config.MAVLinkSystemID > 255 || config.MAVLinkComponentID > 255 {
		return fmt.Errorf("MAVLink system and component ids must be between 0 and 255")
	}
	if !config.CameraTransport.Valid() {
		return fmt.Errorf("camera transport must be one of: siyi_udp, mavsdk, hybrid")
	}
	if config.CameraTransport.UsesSIYI() && strings.TrimSpace(config.SIYICameraAddress) == "" {
		return fmt.Errorf("SIYI camera address is required for %s camera transport", config.CameraTransport)
	}
	if config.PerceptionEnabled {
		if config.PerceptionAdapterMode != AdapterModeProcess && config.PerceptionAdapterMode != AdapterModeContainer {
			return fmt.Errorf("perception adapter mode must be process or container")
		}
		if !filepath.IsAbs(config.ModelPath) || !filepath.IsAbs(config.PostprocessSO) {
			return fmt.Errorf("Hailo model and postprocess paths must be absolute")
		}
		if !filepath.IsAbs(paths.HailoAdapter) {
			return fmt.Errorf("Hailo adapter path must be absolute")
		}
	}
	if config.SpatialEnabled {
		if config.SpatialProvider != SpatialProviderDepthAI && config.SpatialProvider != SpatialProviderSynthetic {
			return fmt.Errorf("spatial provider must be depthai or synthetic")
		}
		if config.SpatialProvider == SpatialProviderDepthAI &&
			config.SpatialDeviceID != config.AircraftProfile.Payloads.DepthCamera.DeviceID {
			return fmt.Errorf("spatial device must match the depth camera in aircraft profile %q", config.AircraftProfileID)
		}
	}
	return nil
}

type Options struct {
	DryRun                   bool
	NonInteractive           bool
	AllowUnsupported         bool
	Paths                    Paths
	Input                    io.Reader
	Output                   io.Writer
	ArchitectureOverride     string
	PackagedModelAccelerator string
	AircraftProfileID        string
}

func DefaultOptions() Options {
	return Options{Paths: DefaultPaths("/"), ArchitectureOverride: runtime.GOARCH}
}
