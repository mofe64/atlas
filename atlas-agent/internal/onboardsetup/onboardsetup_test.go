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

func TestDiscoverLegacyUnitsFindsOnlyDeprecatedEtcUnits(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "etc", "systemd", "system", "atlas-agent.service")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	packaged := filepath.Join(root, "usr", "lib", "systemd", "system", "atlas-mavsdk.service")
	if err := os.MkdirAll(filepath.Dir(packaged), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(packaged, []byte("packaged"), 0o644); err != nil {
		t.Fatal(err)
	}

	units := discoverLegacyUnits(root)
	if len(units) != 1 || units[0] != legacy {
		t.Fatalf("legacy units = %#v, want only %s", units, legacy)
	}
}

func TestDiscoverHailoRequiresCompleteRuntime(t *testing.T) {
	runner := &fakeRunner{results: map[string]CommandResult{
		"lspci -Dnn":                     {Output: "0000:01:00.0 Co-processor [1e60:2864] Hailo Technologies"},
		"hailortcli fw-control identify": {Output: "Device Architecture: HAILO8L"},
		"gst-inspect-1.0 hailonet":       {},
		"gst-inspect-1.0 hailofilter":    {},
		"python3 -c import gi; gi.require_version('Gst', '1.0'); from gi.repository import Gst; import hailo": {},
		"apt-cache show hailo-all": {},
	}}

	status := discoverHailo(context.Background(), runner)
	if !status.Ready() || status.Accelerator != "hailo-8l" {
		t.Fatalf("status = %#v, want ready Hailo-8L", status)
	}

	runner.results["gst-inspect-1.0 hailofilter"] = CommandResult{Err: errors.New("missing")}
	status = discoverHailo(context.Background(), runner)
	if status.Ready() || !strings.Contains(strings.Join(status.MissingComponents, ","), "hailonet/hailofilter") {
		t.Fatalf("status = %#v, want incomplete GStreamer runtime", status)
	}
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
	config.PerceptionEnabled = true

	rendered, err := RenderEnvironment(config, paths)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		`ATLAS_DRONE_NAME="Survey \"One\""`,
		`ATLAS_FLIGHT_CONTROLLER_ENDPOINT="/dev/serial/by-id/usb-pixhawk"`,
		`ATLAS_MAVSDK_SYSTEM_ADDRESS="serial:///dev/serial/by-id/usb-pixhawk:921600"`,
		`ATLAS_PERCEPTION_PROVIDER="hailo"`,
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
	input := strings.NewReader("\n\n\n\n\n\n\n\n")
	output := &bytes.Buffer{}
	options := DefaultOptions()
	options.Paths = DefaultPaths(t.TempDir())
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

func TestPackagedSystemdUnitsUseDirectExecutables(t *testing.T) {
	for _, name := range []string{"atlas-agent.service", "atlas-mavsdk.service"} {
		path := filepath.Join("..", "..", "packaging", "systemd", name)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		unit := string(raw)
		if strings.Contains(unit, "bash -lc") || strings.Contains(unit, "atlas-mediamtx") || strings.Contains(unit, "atlas-video-agent") {
			t.Fatalf("%s retains a deprecated shell/media dependency:\n%s", name, unit)
		}
		if !strings.Contains(unit, "EnvironmentFile=/etc/atlas-agent/atlas-agent.env") {
			t.Fatalf("%s does not load the canonical Atlas configuration", name)
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
