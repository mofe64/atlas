package onboardsetup

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner struct {
	results map[string]CommandResult
	missing map[string]bool
	calls   []string
}

func (runner *fakeRunner) Run(_ context.Context, name string, args ...string) CommandResult {
	key := name + " " + strings.Join(args, " ")
	runner.calls = append(runner.calls, key)
	if result, ok := runner.results[key]; ok {
		return result
	}
	if result, ok := runner.results[name]; ok {
		return result
	}
	return CommandResult{}
}

func (runner *fakeRunner) LookPath(name string) (string, error) {
	if runner.missing[name] {
		return "", errors.New("not found")
	}
	return "/usr/bin/" + name, nil
}

func TestDiscoverSerialPrefersPersistentPath(t *testing.T) {
	root := t.TempDir()
	device := filepath.Join(root, "dev", "ttyUSB0")
	if err := os.MkdirAll(filepath.Join(root, "dev", "serial", "by-id"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(device, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../ttyUSB0", filepath.Join(root, "dev", "serial", "by-id", "usb-pixhawk")); err != nil {
		t.Fatal(err)
	}

	candidates := discoverSerial(root)
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v, want one deduplicated device", candidates)
	}
	if candidates[0].Path != "/dev/serial/by-id/usb-pixhawk" || !candidates[0].Persistent {
		t.Fatalf("candidate = %#v, want persistent by-id path", candidates[0])
	}
}

func TestDiscoverHailoRequiresCompleteRuntime(t *testing.T) {
	runner := &fakeRunner{results: map[string]CommandResult{
		"lspci -Dnn":                     {Output: "0000:01:00.0 Co-processor [1e60:2864] Hailo Technologies"},
		"hailortcli fw-control identify": {Output: "Device Architecture: HAILO8L"},
		"gst-inspect-1.0 hailonet":       {},
		"gst-inspect-1.0 hailofilter":    {},
		"python3 -c import gi; gi.require_version('Gst', '1.0'); from gi.repository import Gst; import hailo; import cv2; import numpy": {},
	}}

	status := discoverHailo(context.Background(), runner, DefaultPaths("/"))
	if !status.Ready() || status.Accelerator != "hailo-8l" {
		t.Fatalf("status = %#v, want ready Hailo-8L", status)
	}

	runner.results["gst-inspect-1.0 hailofilter"] = CommandResult{Err: errors.New("missing")}
	status = discoverHailo(context.Background(), runner, DefaultPaths("/"))
	if status.Ready() || !strings.Contains(strings.Join(status.MissingComponents, ","), "hailonet/hailofilter") {
		t.Fatalf("status = %#v, want incomplete GStreamer runtime", status)
	}
}

func TestDiscoverContainerHailoRequiresPinnedMatchingStack(t *testing.T) {
	paths := DefaultPaths(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(paths.HailoContainerEnv), 0o755); err != nil {
		t.Fatal(err)
	}
	containerEnvironment := strings.Join([]string{
		`ATLAS_HAILO_CONTAINER_IMAGE="sha256:image"`,
		`ATLAS_HAILO_CONTAINER_NAME="atlas-hailo-adapter"`,
		`ATLAS_HAILO_DRIVER_VERSION="4.20.0"`,
		`ATLAS_HAILO_DRIVER_PACKAGE_VERSION="4.20.0-1"`,
		`ATLAS_HAILO_FIRMWARE_VERSION="4.20.0"`,
		`ATLAS_HAILO_FIRMWARE_PACKAGE_VERSION="4.20.0-1"`,
		`ATLAS_HAILORT_PACKAGE_VERSION="4.20.0-1"`,
		`ATLAS_HAILO_TAPPAS_PACKAGE_VERSION="3.31.0+1-1"`,
	}, "\n")
	if err := os.WriteFile(paths.HailoContainerEnv, []byte(containerEnvironment), 0o644); err != nil {
		t.Fatal(err)
	}
	checkOutput := strings.Join([]string{
		"HAILORT_VERSION=4.20.0-1",
		"TAPPAS_VERSION=3.31.0+1-1",
		"GSTREAMER_READY=true",
		"PYTHON_READY=true",
		"DEVICE_NODE_READY=true",
		"DEVICE_READY=true",
		"DEVICE_ARCHITECTURE=hailo-8l",
		"FIRMWARE_VERSION=4.20.0",
	}, "\n")
	runner := &fakeRunner{results: map[string]CommandResult{
		"lspci -Dnn": {Output: "0000:01:00.0 Co-processor [1e60:2864] Hailo Technologies"},
		"test -c " + rootPath(paths.Root, "/dev/hailo0"):                             {},
		"modinfo -F version hailo_pci":                                               {Output: "4.20.0"},
		"dpkg-query -W -f=${Version} hailo-dkms":                                     {Output: "4.20.0-1"},
		"dpkg-query -W -f=${Version} hailofw":                                        {Output: "4.20.0-1"},
		"docker image inspect sha256:image":                                          {},
		"docker inspect --format {{.State.Running}} atlas-hailo-adapter":             {Output: "true"},
		"docker exec atlas-hailo-adapter /usr/local/bin/atlas-hailo-container-check": {Output: checkOutput},
	}}

	status := discoverHailo(context.Background(), runner, paths)
	if !status.Ready() || status.RuntimeMode != AdapterModeContainer || status.Accelerator != "hailo-8l" {
		t.Fatalf("status = %#v, want ready pinned container runtime", status)
	}

	runner.results["docker exec atlas-hailo-adapter /usr/local/bin/atlas-hailo-container-check"] = CommandResult{Output: strings.Replace(checkOutput, "HAILORT_VERSION=4.20.0-1", "HAILORT_VERSION=4.19.0-3", 1)}
	status = discoverHailo(context.Background(), runner, paths)
	if status.Ready() || status.VersionsCompatible || !strings.Contains(strings.Join(status.MissingComponents, ","), "matching host/container Hailo versions") {
		t.Fatalf("status = %#v, want userspace version mismatch", status)
	}
}

func TestContainerPerceptionEnablesContainerService(t *testing.T) {
	config := DefaultInstallConfig(DefaultPaths("/"))
	config.PerceptionEnabled = true
	config.PerceptionAdapterMode = AdapterModeContainer
	services := configuredServices(config)
	if !slicesEqual(services, []string{"atlas-mavsdk.service", "atlas-agent.service", "atlas-hailo-adapter.service"}) {
		t.Fatalf("services = %#v", services)
	}
}

func TestConfiguredServicesIncludesSpatialRuntimeWhenEnabled(t *testing.T) {
	config := DefaultInstallConfig(DefaultPaths("/"))
	config.SpatialEnabled = true
	config.SpatialProvider = SpatialProviderDepthAI
	services := configuredServices(config)
	if !slicesEqual(services, []string{"atlas-mavsdk.service", "atlas-agent.service", "atlas-spatial-runtime.service"}) {
		t.Fatalf("services = %#v", services)
	}
}

func TestDiscoverSpatialParsesDeviceAndHealthContract(t *testing.T) {
	paths := DefaultPaths(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(paths.SpatialCheck), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.SpatialCheck, []byte("check"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.SpatialRuntimeBinary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.SpatialRuntimeBinary, []byte("runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	configuration := map[string]string{
		"ATLAS_SPATIAL_ENABLED":     "true",
		"ATLAS_SPATIAL_SOURCE_ID":   "front-depth",
		"ATLAS_SPATIAL_SOCKET_PATH": filepath.Join(paths.RuntimeDirectory, "spatial.sock"),
	}
	discoverCall := paths.SpatialCheck + " --discover --sysfs-root " + rootPath(paths.Root, "/sys")
	probeCall := paths.SpatialCheck + " --socket " + configuration["ATLAS_SPATIAL_SOCKET_PATH"]
	runner := &fakeRunner{results: map[string]CommandResult{
		discoverCall: {Output: "DEVICE_PRESENT=true\nPROVIDER=depthai\nDEVICE_ID=oak-123\nMODEL=OAK-D Lite\nUSB_TRANSPORT=usb3\nUSB_SPEED_MBPS=5000"},
		"systemctl is-active atlas-spatial-runtime.service": {Output: "active"},
		probeCall: {Output: "READY=true\nSTATUS=ready\nDEPTH_FPS=15.0\nDEPTH_FRAME_ID=camera_optical\nCALIBRATION_VALID=true\nCALIBRATION_FRAME_ID=camera_optical"},
	}}

	status := discoverSpatial(context.Background(), runner, paths, configuration)
	if !status.DevicePresent || !status.RuntimeInstalled || !status.ServiceRunning || !status.Ready {
		t.Fatalf("status = %#v, want device and runtime ready", status)
	}
	if status.Provider != SpatialProviderDepthAI || status.DeviceID != "oak-123" || status.USBTransport != "usb3" || !status.CalibrationValid || status.CalibrationFrame != "camera_optical" {
		t.Fatalf("status = %#v, want parsed vendor boundary metadata", status)
	}
}

func TestSpatialDiscoveryUsesIndependentNativeInstallation(t *testing.T) {
	paths := DefaultPaths(t.TempDir())
	existing := map[string]string{
		"ATLAS_SPATIAL_ENABLED":  "true",
		"ATLAS_SPATIAL_PROVIDER": SpatialProviderDepthAI,
	}
	status := discoverSpatial(context.Background(), &fakeRunner{results: map[string]CommandResult{}}, paths, existing)
	if status.RuntimeInstalled {
		t.Fatal("runtime should not be installed before its independent binary exists")
	}
	if err := os.MkdirAll(filepath.Dir(paths.SpatialRuntimeBinary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.SpatialRuntimeBinary, []byte("runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	status = discoverSpatial(context.Background(), &fakeRunner{results: map[string]CommandResult{}}, paths, existing)
	if !status.RuntimeInstalled {
		t.Fatal("runtime should be installed when the native binary exists")
	}
}

func TestDoctorSpatialRequiresReadyCalibratedDepthContract(t *testing.T) {
	paths := DefaultPaths(t.TempDir())
	for _, path := range []string{paths.SpatialConfigFile, paths.SpatialRuntimeBinary, paths.SpatialCheck} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("test"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	configuration := map[string]string{
		"ATLAS_SPATIAL_PROVIDER":    SpatialProviderDepthAI,
		"ATLAS_SPATIAL_DEVICE_ID":   "device-123",
		"ATLAS_SPATIAL_SOCKET_PATH": filepath.Join(paths.RuntimeDirectory, "spatial.sock"),
	}
	discoverCall := paths.SpatialCheck + " --discover --sysfs-root " + rootPath(paths.Root, "/sys") + " --device-id device-123"
	probeCall := paths.SpatialCheck + " --socket " + configuration["ATLAS_SPATIAL_SOCKET_PATH"]
	runner := &fakeRunner{results: map[string]CommandResult{
		"systemctl is-active atlas-spatial-runtime.service": {Output: "active"},
		discoverCall: {Output: "DEVICE_PRESENT=true\nDEVICE_ID=device-123\nMODEL=OAK-D Lite\nUSB_TRANSPORT=usb3"},
		probeCall:    {Output: "READY=true\nSTATUS=ready\nDEPTH_FPS=15\nDEPTH_ENCODING=16UC1\nDEPTH_UNIT=millimetre\nDEPTH_FRAME_ID=camera_optical\nCALIBRATION_VALID=true\nCALIBRATION_FRAME_ID=camera_optical"},
	}}

	checks := doctorSpatial(context.Background(), runner, paths, configuration)
	if HasFailures(checks) {
		t.Fatalf("checks = %#v, want all spatial checks ready", checks)
	}
	runner.results[probeCall] = CommandResult{Output: "READY=false\nSTATUS=degraded\nLAST_ERROR=depth stale", Err: errors.New("not ready")}
	if checks = doctorSpatial(context.Background(), runner, paths, configuration); !HasFailures(checks) {
		t.Fatalf("checks = %#v, want degraded contract failure", checks)
	}
	if check := namedCheck(checks, "spatial depth contract"); !strings.Contains(check.Message, "error=depth stale") {
		t.Fatalf("depth check = %#v, want detailed provider failure", check)
	}

	runner.results[discoverCall] = CommandResult{Output: "READY=false\nSTATUS=error\nLAST_ERROR=cannot read USB sysfs", Err: errors.New("discovery failed")}
	checks = doctorSpatial(context.Background(), runner, paths, configuration)
	if check := namedCheck(checks, "spatial USB camera"); !strings.Contains(check.Message, "error=cannot read USB sysfs") {
		t.Fatalf("USB check = %#v, want detailed discovery failure", check)
	}
}

func TestDefaultSpatialDiagnosticsArePrivate(t *testing.T) {
	paths := DefaultPaths("/")
	if paths.SpatialCheck != "/opt/atlas-spatial-runtime/bin/atlas-spatial-check" {
		t.Fatalf("SpatialCheck = %q, want private packaged diagnostic", paths.SpatialCheck)
	}
}

func namedCheck(checks []Check, name string) Check {
	for _, check := range checks {
		if check.Name == name {
			return check
		}
	}
	return Check{}
}

func TestAircraftProfileCheckReportsSelectedCamera(t *testing.T) {
	paths := DefaultPaths(t.TempDir())
	installTestAircraftProfile(t, paths)
	source := filepath.Join(paths.AircraftProfilesDir, "ariadne.json")
	if check := aircraftProfileCheck(source, "ariadne"); check.Level != CheckPass || !strings.Contains(check.Message, "19443010F122147E00") {
		t.Fatalf("profile check = %#v", check)
	}
	if check := aircraftProfileCheck(source, "another-aircraft"); check.Level != CheckFail {
		t.Fatalf("mismatched profile check = %#v", check)
	}
}

func TestDoctorContainerHailoValidatesHEFAccelerator(t *testing.T) {
	model := filepath.Join(t.TempDir(), "objects.hef")
	if err := os.WriteFile(model, []byte("hef"), 0o644); err != nil {
		t.Fatal(err)
	}
	status := HailoStatus{
		RuntimeMode:                   AdapterModeContainer,
		DeviceNodeReady:               true,
		RuntimeInstalled:              true,
		GStreamerReady:                true,
		PythonReady:                   true,
		Accelerator:                   "hailo-8l",
		ContainerImage:                "sha256:image",
		ContainerName:                 "atlas-hailo-adapter",
		HostDriverPackageVersion:      "4.20.0-1",
		HostDriverVersion:             "4.20.0",
		HostFirmwareVersion:           "4.20.0-1",
		FirmwareVersion:               "4.20.0",
		UserspaceVersion:              "4.20.0-1",
		TAPPASVersion:                 "3.31.0+1-1",
		ExpectedDriverVersion:         "4.20.0",
		ExpectedDriverPackageVersion:  "4.20.0-1",
		ExpectedFirmwareVersion:       "4.20.0-1",
		ExpectedDeviceFirmwareVersion: "4.20.0",
		ExpectedUserspaceVersion:      "4.20.0-1",
		ExpectedTAPPASVersion:         "3.31.0+1-1",
	}
	configuration := map[string]string{
		"ATLAS_PERCEPTION_MODEL_PATH": model,
		"ATLAS_HAILO_ACCELERATOR":     "hailo-8l",
	}
	modelCheck := "MODEL_READY=true\nMODEL_ARCHITECTURE=hailo-8l\nMODEL_COMPATIBLE=true"
	runner := &fakeRunner{results: map[string]CommandResult{
		"systemctl is-active atlas-hailo-adapter.service":                                             {Output: "active"},
		"docker inspect --format {{.State.Running}} atlas-hailo-adapter":                              {Output: "true"},
		"docker exec atlas-hailo-adapter /usr/local/bin/atlas-hailo-container-check --model " + model: {Output: modelCheck},
	}}
	checks := doctorHailoContainer(context.Background(), runner, configuration, status)
	if HasFailures(checks) {
		t.Fatalf("checks = %#v, want all pass", checks)
	}

	configuration["ATLAS_HAILO_ACCELERATOR"] = "hailo-8"
	checks = doctorHailoContainer(context.Background(), runner, configuration, status)
	if !HasFailures(checks) {
		t.Fatalf("checks = %#v, want HEF accelerator mismatch", checks)
	}

	configuration["ATLAS_HAILO_ACCELERATOR"] = "hailo-8l"
	runner.results["docker exec atlas-hailo-adapter /usr/local/bin/atlas-hailo-container-check --model "+model] = CommandResult{
		Output: "MODEL_READY=true\nMODEL_ARCHITECTURE=hailo-8l\nMODEL_COMPATIBLE=false",
		Err:    errors.New("exit status 1"),
	}
	checks = doctorHailoContainer(context.Background(), runner, configuration, status)
	levels := map[string]CheckLevel{}
	for _, check := range checks {
		levels[check.Name] = check.Level
	}
	if levels["Hailo HEF parse"] != CheckPass || levels["Hailo HEF accelerator"] != CheckFail {
		t.Fatalf("checks = %#v, want independent parse pass and accelerator failure", checks)
	}
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestPackagedModelMustMatchDetectedAccelerator(t *testing.T) {
	ready := HailoStatus{PCIVisible: true, RuntimeInstalled: true, DeviceReady: true, GStreamerReady: true, PythonReady: true}
	ready.Accelerator = "hailo-8"
	if err := validateModelAccelerator("hailo-8l", ready); err == nil {
		t.Fatal("accepted Hailo-8L HEF for a Hailo-8 accelerator")
	}
	ready.Accelerator = "unknown"
	if err := validateModelAccelerator("hailo-8l", ready); err == nil {
		t.Fatal("accepted HEF when the accelerator type was unknown")
	}
	ready.Accelerator = "hailo-8l"
	if err := validateModelAccelerator("hailo-8l", ready); err != nil {
		t.Fatalf("rejected matching accelerator: %v", err)
	}
}

func TestModelMismatchStillAllowsCoreInstallWhenPerceptionDisabled(t *testing.T) {
	paths := DefaultPaths(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(paths.DefaultModel), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.DefaultModel, []byte("hef"), 0o644); err != nil {
		t.Fatal(err)
	}
	discovery := Discovery{
		Serial:         []SerialCandidate{{Path: "/dev/ttyUSB0"}},
		ExistingConfig: map[string]string{"ATLAS_PERCEPTION_PROVIDER": "disabled"},
		Hailo: HailoStatus{
			PCIVisible: true, RuntimeInstalled: true, DeviceReady: true, GStreamerReady: true, PythonReady: true, Accelerator: "hailo-8",
		},
	}
	options := DefaultOptions()
	options.Paths = paths
	options.NonInteractive = true
	options.PackagedModelAccelerator = "hailo-8l"
	options.AircraftProfileID = installTestAircraftProfile(t, paths)
	plan, err := BuildInstallPlan(context.Background(), &fakeRunner{results: map[string]CommandResult{}}, discovery, options)
	if err != nil {
		t.Fatalf("core install was blocked by disabled perception: %v", err)
	}
	if plan.Config.PerceptionEnabled {
		t.Fatal("perception unexpectedly enabled with a mismatched HEF")
	}
}

func TestRenderEnvironmentUsesOneSerialSelectionEverywhere(t *testing.T) {
	paths := DefaultPaths("/")
	config := DefaultInstallConfig(paths)
	config.DroneName = `Survey "One"`
	config.SerialDevice = "/dev/serial/by-id/usb-pixhawk"
	config.AgentVersion = "0.1.27-test"
	config.PerceptionEnabled = true

	rendered, err := RenderEnvironment(config, paths)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`ATLAS_AGENT_VERSION="0.1.27-test"`,
		`ATLAS_DRONE_NAME="Survey \"One\""`,
		`ATLAS_FLIGHT_CONTROLLER_ENDPOINT="/dev/serial/by-id/usb-pixhawk"`,
		`ATLAS_MAVSDK_SYSTEM_ADDRESS="serial:///dev/serial/by-id/usb-pixhawk:921600"`,
		`ATLAS_CAMERA_TRANSPORT="siyi_udp"`,
		`ATLAS_PERCEPTION_PROVIDER="hailo"`,
		`ATLAS_TRACKER_ALGORITHM="byte_track"`,
		`ATLAS_TRACKER_CMC_MAX_DIMENSION="320"`,
		`ATLAS_BYTETRACK_WORKER_PATH="/usr/libexec/atlas-agent/atlas-bytetrack-worker"`,
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered config missing %q:\n%s", expected, rendered)
		}
	}
}

func TestRenderEnvironmentRejectsMultilineValues(t *testing.T) {
	config := DefaultInstallConfig(DefaultPaths("/"))
	config.SerialDevice = "/dev/ttyUSB0"
	config.DroneName = "unsafe\nvalue"
	if _, err := RenderEnvironment(config, DefaultPaths("/")); err == nil {
		t.Fatal("RenderEnvironment accepted a multiline value")
	}
}

func TestRenderSpatialEnvironmentUsesVendorNeutralContract(t *testing.T) {
	paths := DefaultPaths("/")
	config := DefaultInstallConfig(paths)
	config.SpatialEnabled = true
	config.SpatialProvider = SpatialProviderDepthAI
	config.SpatialDeviceID = "device-123"
	config.SpatialModel = "OAK-D Lite"
	config.SpatialUSBTransport = "usb3"

	rendered, err := RenderSpatialEnvironment(config, paths)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`ATLAS_SPATIAL_CONTRACT_VERSION="2"`,
		`ATLAS_SPATIAL_PROVIDER="depthai"`,
		`ATLAS_SPATIAL_SOURCE_ID="front-depth"`,
		`ATLAS_SPATIAL_DEVICE_ID="device-123"`,
		`ATLAS_SPATIAL_SOCKET_PATH="/run/atlas-agent/spatial.sock"`,
		`ATLAS_SPATIAL_FRAME_ID="oak_rgb_camera_optical_frame"`,
		`ATLAS_SPATIAL_WIDTH="640"`,
		`ATLAS_SPATIAL_HEIGHT="400"`,
		`ATLAS_SPATIAL_FPS="20"`,
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered spatial config missing %q:\n%s", expected, rendered)
		}
	}
}

func TestEnsureSpatialRuntimeOnlyRefreshesDepthAIUdevRules(t *testing.T) {
	paths := DefaultPaths("/")
	config := DefaultInstallConfig(paths)
	config.SpatialEnabled = true
	config.SpatialProvider = SpatialProviderDepthAI
	runner := &fakeRunner{results: map[string]CommandResult{}}
	output := &bytes.Buffer{}
	ready, err := ensureSpatialRuntime(context.Background(), ApplyRunner{Runner: runner, DryRun: true, Output: output}, Options{DryRun: true, Paths: paths}, &config)
	if err != nil || !ready {
		t.Fatalf("ready=%v err=%v", ready, err)
	}
	rendered := output.String()
	for _, expected := range []string{"udevadm control --reload-rules || true"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("dry-run missing %q:\n%s", expected, rendered)
		}
	}
	if strings.Contains(rendered, "docker") {
		t.Fatalf("native Spatial setup unexpectedly invokes Docker:\n%s", rendered)
	}
}

func TestEnsureSpatialRuntimeChecksNativeBinary(t *testing.T) {
	paths := DefaultPaths(t.TempDir())
	config := DefaultInstallConfig(paths)
	config.SpatialEnabled = true
	config.SpatialProvider = SpatialProviderSynthetic
	if err := os.MkdirAll(filepath.Dir(paths.SpatialRuntimeBinary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.SpatialRuntimeBinary, []byte("runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{results: map[string]CommandResult{}}
	ready, err := ensureSpatialRuntime(context.Background(), ApplyRunner{Runner: runner, Output: &bytes.Buffer{}}, Options{Paths: paths}, &config)
	if err != nil || !ready {
		t.Fatalf("ready=%v err=%v", ready, err)
	}
}

func TestEnvironmentFileRoundTripsQuotedValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atlas-agent.env")
	if err := os.WriteFile(path, []byte("ATLAS_DRONE_NAME=\"Survey \\\"One\\\" \\\\ North\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	values := readEnvironmentFile(path)
	if values["ATLAS_DRONE_NAME"] != `Survey "One" \ North` {
		t.Fatalf("decoded value = %q", values["ATLAS_DRONE_NAME"])
	}
}

func TestRTSPCredentialRedaction(t *testing.T) {
	redacted := redactURLCredentials("rtsp://operator:secret@192.168.1.2/live")
	if strings.Contains(redacted, "secret") || !strings.Contains(redacted, "redacted") {
		t.Fatalf("redacted URL = %q", redacted)
	}
}

func TestParseChecksumValidMAVLinkHeartbeats(t *testing.T) {
	payload := []byte{0, 0, 0, 0, 2, 12, 0, 4, 3}
	v1 := mavlinkV1Heartbeat(7, 1, payload)
	heartbeat, ok := parseMAVLinkHeartbeat(append([]byte{1, 2, 3}, v1...))
	if !ok || heartbeat.SystemID != 7 || heartbeat.ComponentID != 1 || heartbeat.VehicleType != 2 || heartbeat.Autopilot != 12 {
		t.Fatalf("v1 heartbeat = %#v, %v", heartbeat, ok)
	}

	v2 := mavlinkV2Heartbeat(9, 42, payload)
	heartbeat, ok = parseMAVLinkHeartbeat(v2)
	if !ok || heartbeat.SystemID != 9 || heartbeat.ComponentID != 42 {
		t.Fatalf("v2 heartbeat = %#v, %v", heartbeat, ok)
	}
	v2[len(v2)-1] ^= 0xff
	if _, ok := parseMAVLinkHeartbeat(v2); ok {
		t.Fatal("accepted heartbeat with a corrupt checksum")
	}
}

func TestInteractivePlanUsesDetectedHeartbeat(t *testing.T) {
	device := filepath.Join(t.TempDir(), "telem2")
	payload := []byte{0, 0, 0, 0, 2, 12, 0, 4, 3}
	if err := os.WriteFile(device, mavlinkV1Heartbeat(17, 1, payload), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{results: map[string]CommandResult{}}
	discovery := Discovery{
		OS:           OSRelease{ID: "ubuntu", VersionID: "24.04", PrettyName: "Ubuntu 24.04"},
		Architecture: "arm64",
		BoardModel:   "Raspberry Pi 5 Model B",
		Serial:       []SerialCandidate{{Path: device, Persistent: true}},
		Hailo: HailoStatus{
			PCIVisible: true, RuntimeInstalled: true, DeviceReady: true, GStreamerReady: true, PythonReady: true, Accelerator: "hailo-8l",
		},
	}
	input := strings.NewReader("\n\n\n\n\n\n\n\n\n")
	output := &bytes.Buffer{}
	options := DefaultOptions()
	options.Paths = DefaultPaths(t.TempDir())
	installTestAircraftProfile(t, options.Paths)
	for _, path := range []string{options.Paths.DefaultModel, options.Paths.DefaultPostprocessSO} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	options.Input, options.Output = input, output

	plan, err := BuildInstallPlan(context.Background(), runner, discovery, options)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Config.MAVLinkSystemID != 17 || plan.Config.MAVLinkComponentID != 1 {
		t.Fatalf("MAVLink identity = %d/%d, want 17/1", plan.Config.MAVLinkSystemID, plan.Config.MAVLinkComponentID)
	}
	if !plan.Config.PerceptionEnabled {
		t.Fatal("expected ready Hailo runtime to be enabled by default")
	}
}

func installTestAircraftProfile(t *testing.T, paths Paths) string {
	t.Helper()
	source := filepath.Join("..", "..", "..", "aircraft-profiles", "ariadne.json")
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(paths.AircraftProfilesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(paths.AircraftProfilesDir, "ariadne.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return "ariadne"
}

func TestPackagedSystemdUnitsUseDirectExecutables(t *testing.T) {
	for _, name := range []string{"atlas-agent.service", "atlas-mavsdk.service"} {
		path := filepath.Join("..", "..", "packaging", "systemd", name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		unit := string(raw)
		if strings.Contains(unit, "bash -lc") {
			t.Fatalf("%s uses an indirect shell command:\n%s", name, unit)
		}
		if !strings.Contains(unit, "EnvironmentFile=/etc/atlas-agent/atlas-agent.env") {
			t.Fatalf("%s does not load the canonical Atlas configuration", name)
		}
	}
}

func TestHailoContainerServiceUsesLeastPrivilegeLauncher(t *testing.T) {
	unitPath := filepath.Join("..", "..", "packaging", "systemd", "atlas-hailo-adapter.service")
	raw, err := os.ReadFile(unitPath)
	if err != nil {
		t.Fatal(err)
	}
	unit := string(raw)
	for _, expected := range []string{
		"Requires=docker.service atlas-agent.service",
		"EnvironmentFile=/etc/atlas-agent/hailo-container.env",
		"ExecStart=/usr/libexec/atlas-agent/atlas-hailo-container-run",
		"ExecStop=/usr/bin/docker stop --timeout 10 ${ATLAS_HAILO_CONTAINER_NAME}",
		"PartOf=atlas-agent.service",
	} {
		if !strings.Contains(unit, expected) {
			t.Fatalf("Hailo unit missing %q:\n%s", expected, unit)
		}
	}
	for _, invalid := range []string{
		"dev-hailo0.device",
		"ConditionPathExists=/dev/hailo0",
	} {
		if strings.Contains(unit, invalid) {
			t.Fatalf("Hailo unit relies on unavailable device-unit behavior %q:\n%s", invalid, unit)
		}
	}

	launcherPath := filepath.Join("..", "..", "packaging", "hailo", "atlas-hailo-container-run")
	raw, err = os.ReadFile(launcherPath)
	if err != nil {
		t.Fatal(err)
	}
	launcher := string(raw)
	for _, expected := range []string{
		"[ ! -c /dev/hailo0 ]",
		"Hailo device /dev/hailo0 is not available; retrying through systemd",
		"adapter_path=\"/usr/libexec/atlas-agent/atlas-hailort-adapter\"",
		"Atlas Hailo adapter ${adapter_path} is not executable; retrying through systemd",
		"--network host",
		"--workdir /tmp",
		"--device /dev/hailo0:/dev/hailo0",
		"--user \"${agent_uid}:${agent_gid}\"",
		"--group-add \"${device_gid}\"",
		"--cap-drop ALL",
		"--volume /run/atlas-agent:/run/atlas-agent:rw",
		"--volume /usr/share/atlas-agent/models:/usr/share/atlas-agent/models:ro",
		"--volume \"${adapter_path}:/usr/local/bin/atlas-hailort-adapter:ro\"",
	} {
		if !strings.Contains(launcher, expected) {
			t.Fatalf("Hailo launcher missing %q:\n%s", expected, launcher)
		}
	}
}

func mavlinkV1Heartbeat(systemID, componentID byte, payload []byte) []byte {
	frame := []byte{0xfe, byte(len(payload)), 1, systemID, componentID, 0}
	frame = append(frame, payload...)
	checksum := mavlinkCRC(frame[1:], 50)
	return binary.LittleEndian.AppendUint16(frame, checksum)
}

func mavlinkV2Heartbeat(systemID, componentID byte, payload []byte) []byte {
	frame := []byte{0xfd, byte(len(payload)), 0, 0, 1, systemID, componentID, 0, 0, 0}
	frame = append(frame, payload...)
	checksum := mavlinkCRC(frame[1:], 50)
	return binary.LittleEndian.AppendUint16(frame, checksum)
}
