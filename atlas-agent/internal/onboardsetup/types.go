// Package onboardsetup implements discovery, configuration, installation, and
// diagnostics for the Atlas onboard runtime.
package onboardsetup

import (
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	SupportedOSID        = "ubuntu"
	SupportedOSVersion   = "24.04"
	DefaultBaudRate      = 921600
	DefaultGroundAddr    = "192.168.144.50:7443"
	DefaultA8RTSPURL     = "rtsp://192.168.144.25:8554/main.264"
	DefaultSIYIAddr      = "192.168.144.25:37260"
	DefaultMAVSDKAddr    = "127.0.0.1:50051"
	AdapterModeProcess   = "process"
	AdapterModeContainer = "container"
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
	HailoSetupBinary      string
	HailoContainerEnv     string
	HailoContainerService string
	ReleaseManifest       string
	AgentService          string
	MAVSDKService         string
	DefaultModel          string
	DefaultPostprocessSO  string
}

func DefaultPaths(root string) Paths {
	rooted := func(path string) string {
		if root == "" || root == "/" {
			return path
		}
		return filepath.Join(root, strings.TrimPrefix(path, "/"))
	}
	return Paths{
		Root:                  root,
		ConfigFile:            rooted("/etc/atlas-agent/atlas-agent.env"),
		StateDirectory:        rooted("/var/lib/atlas-agent"),
		RuntimeDirectory:      rooted("/run/atlas-agent"),
		AgentBinary:           rooted("/usr/bin/atlas-agent"),
		SetupBinary:           rooted("/usr/bin/atlas-setup"),
		MAVSDKBinary:          rooted("/usr/libexec/atlas-agent/mavsdk_server"),
		HailoAdapter:          rooted("/usr/libexec/atlas-agent/atlas-hailort-adapter"),
		HailoSetupBinary:      rooted("/usr/sbin/atlas-hailo-setup"),
		HailoContainerEnv:     rooted("/etc/atlas-agent/hailo-container.env"),
		HailoContainerService: rooted("/usr/lib/systemd/system/atlas-hailo-adapter.service"),
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

type Discovery struct {
	OS              OSRelease
	Architecture    string
	BoardModel      string
	Serial          []SerialCandidate
	Hailo           HailoStatus
	Camera          RTSPStatus
	GroundReachable bool
	ExistingConfig  map[string]string
}

func (discovery Discovery) PlatformSupported() bool {
	return discovery.OS.Supported() && discovery.Architecture == "arm64" && strings.Contains(strings.ToLower(discovery.BoardModel), "raspberry pi 5")
}

type InstallConfig struct {
	DroneName             string
	GroundStationAddress  string
	SerialDevice          string
	BaudRate              uint32
	MAVLinkSystemID       uint32
	MAVLinkComponentID    uint32
	A8RTSPURL             string
	SIYICameraAddress     string
	PerceptionEnabled     bool
	PerceptionAdapterMode string
	HailoAccelerator      string
	ModelPath             string
	PostprocessSO         string
	PostprocessFunction   string
	AgentVersion          string
}

func DefaultInstallConfig(paths Paths) InstallConfig {
	return InstallConfig{
		DroneName:             "Atlas Drone",
		GroundStationAddress:  DefaultGroundAddr,
		BaudRate:              DefaultBaudRate,
		MAVLinkSystemID:       1,
		MAVLinkComponentID:    1,
		A8RTSPURL:             DefaultA8RTSPURL,
		SIYICameraAddress:     DefaultSIYIAddr,
		PerceptionAdapterMode: AdapterModeProcess,
		HailoAccelerator:      "hailo-8l",
		ModelPath:             paths.DefaultModel,
		PostprocessSO:         paths.DefaultPostprocessSO,
		PostprocessFunction:   "filter",
		AgentVersion:          "unknown",
	}
}

func (config InstallConfig) Validate(paths Paths) error {
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
}

func DefaultOptions() Options {
	return Options{Paths: DefaultPaths("/"), ArchitectureOverride: runtime.GOARCH}
}
