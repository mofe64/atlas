package onboardsetup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	agentconfig "github.com/sunnyside/atlas/atlas-agent/internal/config"
)

func Discover(ctx context.Context, runner Runner, options Options) (Discovery, error) {
	paths := options.Paths
	if paths.ConfigFile == "" {
		paths = DefaultPaths("/")
	}
	release, err := readOSRelease(rootPath(paths.Root, "/etc/os-release"))
	if err != nil {
		return Discovery{}, err
	}
	architecture := options.ArchitectureOverride
	if architecture == "" {
		architecture = "unknown"
	}
	existingConfig := readEnvironmentFile(paths.ConfigFile)
	cameraURL := existingConfig["ATLAS_A8_RTSP_URL"]
	if cameraURL == "" {
		cameraURL = DefaultA8RTSPURL
	}
	groundAddress := existingConfig["ATLAS_GROUND_STATION_ADDR"]
	if groundAddress == "" {
		groundAddress = DefaultGroundAddr
	}
	discovery := Discovery{
		OS:              release,
		Architecture:    architecture,
		BoardModel:      readBoardModel(rootPath(paths.Root, "/proc/device-tree/model")),
		Serial:          discoverSerial(paths.Root),
		Hailo:           discoverHailo(ctx, runner, paths),
		Camera:          probeRTSP(ctx, runner, cameraURL),
		GroundReachable: probeTCP(groundAddress, 800*time.Millisecond),
		ExistingConfig:  existingConfig,
	}
	return discovery, nil
}

func rootPath(root, path string) string {
	if root == "" || root == "/" {
		return path
	}
	return filepath.Join(root, strings.TrimPrefix(path, "/"))
}

func readOSRelease(path string) (OSRelease, error) {
	file, err := os.Open(path)
	if err != nil {
		return OSRelease{}, fmt.Errorf("read operating-system release: %w", err)
	}
	defer file.Close()
	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, value, found := strings.Cut(scanner.Text(), "=")
		if !found {
			continue
		}
		values[key] = strings.Trim(strings.TrimSpace(value), "\"")
	}
	if err := scanner.Err(); err != nil {
		return OSRelease{}, fmt.Errorf("read operating-system release: %w", err)
	}
	return OSRelease{ID: values["ID"], VersionID: values["VERSION_ID"], PrettyName: values["PRETTY_NAME"]}, nil
}

func readBoardModel(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(strings.TrimRight(string(raw), "\x00"))
}

func discoverSerial(root string) []SerialCandidate {
	patterns := []string{
		"/dev/serial/by-id/*",
		"/dev/serial/by-path/*",
		"/dev/ttyUSB*",
		"/dev/ttyACM*",
		"/dev/serial0",
	}
	seenResolved := map[string]bool{}
	var candidates []SerialCandidate
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(rootPath(root, pattern))
		sort.Strings(matches)
		for _, match := range matches {
			resolved, err := filepath.EvalSymlinks(match)
			if err != nil {
				resolved = match
			}
			if seenResolved[resolved] {
				continue
			}
			seenResolved[resolved] = true
			displayPath := match
			displayResolved := resolved
			if root != "" && root != "/" {
				displayPath = "/" + strings.TrimPrefix(strings.TrimPrefix(match, root), "/")
				displayResolved = "/" + strings.TrimPrefix(strings.TrimPrefix(resolved, root), "/")
			}
			candidates = append(candidates, SerialCandidate{
				Path:       displayPath,
				Resolved:   displayResolved,
				Persistent: strings.Contains(displayPath, "/dev/serial/by-"),
			})
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Persistent != candidates[j].Persistent {
			return candidates[i].Persistent
		}
		return candidates[i].Path < candidates[j].Path
	})
	return candidates
}

func discoverHailo(ctx context.Context, runner Runner, paths Paths) HailoStatus {
	status := HailoStatus{RuntimeMode: AdapterModeProcess, Accelerator: "unknown", VersionsCompatible: true}
	if _, err := runner.LookPath("lspci"); err == nil {
		result := runner.Run(ctx, "lspci", "-Dnn")
		lower := strings.ToLower(result.Output)
		status.PCIVisible = result.Err == nil && (strings.Contains(lower, "hailo") || strings.Contains(lower, "[1e60:"))
	}
	status.DeviceNodeReady = commandSucceeds(ctx, runner, "test", "-c", rootPath(paths.Root, "/dev/hailo0"))

	containerConfig := readEnvironmentFile(paths.HailoContainerEnv)
	if containerConfig["ATLAS_HAILO_CONTAINER_IMAGE"] != "" {
		return discoverHailoContainer(ctx, runner, status, containerConfig)
	}

	if _, err := runner.LookPath("hailortcli"); err == nil {
		status.RuntimeInstalled = true
		result := runner.Run(ctx, "hailortcli", "fw-control", "identify")
		status.IdentifyOutput = result.Output
		status.DeviceReady = result.Err == nil
		status.Accelerator = parseHailoAccelerator(result.Output)
	}
	status.GStreamerReady = commandSucceeds(ctx, runner, "gst-inspect-1.0", "hailonet") && commandSucceeds(ctx, runner, "gst-inspect-1.0", "hailofilter")
	status.PythonReady = commandSucceeds(ctx, runner, "python3", "-c", "import gi; gi.require_version('Gst', '1.0'); from gi.repository import Gst; import hailo; import cv2; import numpy")
	if !status.PCIVisible {
		status.MissingComponents = append(status.MissingComponents, "Hailo PCIe device")
	}
	if !status.RuntimeInstalled {
		status.MissingComponents = append(status.MissingComponents, "hailortcli")
	} else if !status.DeviceReady {
		status.MissingComponents = append(status.MissingComponents, "working HailoRT device")
	}
	if !status.GStreamerReady {
		status.MissingComponents = append(status.MissingComponents, "hailonet/hailofilter")
	}
	if !status.PythonReady {
		status.MissingComponents = append(status.MissingComponents, "Hailo/OpenCV/NumPy Python bindings")
	}
	return status
}

func discoverHailoContainer(ctx context.Context, runner Runner, status HailoStatus, config map[string]string) HailoStatus {
	status.RuntimeMode = AdapterModeContainer
	status.ContainerImage = config["ATLAS_HAILO_CONTAINER_IMAGE"]
	status.ContainerName = fallback(config["ATLAS_HAILO_CONTAINER_NAME"], "atlas-hailo-adapter")
	status.ExpectedDriverVersion = config["ATLAS_HAILO_DRIVER_VERSION"]
	status.ExpectedDriverPackageVersion = config["ATLAS_HAILO_DRIVER_PACKAGE_VERSION"]
	status.ExpectedFirmwareVersion = config["ATLAS_HAILO_FIRMWARE_PACKAGE_VERSION"]
	status.ExpectedDeviceFirmwareVersion = config["ATLAS_HAILO_FIRMWARE_VERSION"]
	status.ExpectedUserspaceVersion = config["ATLAS_HAILORT_PACKAGE_VERSION"]
	status.ExpectedTAPPASVersion = config["ATLAS_HAILO_TAPPAS_PACKAGE_VERSION"]

	if result := runner.Run(ctx, "cat", "/sys/module/hailo_pci/version"); result.Err == nil {
		status.HostDriverVersion = strings.TrimSpace(result.Output)
	}
	if status.HostDriverVersion == "" {
		if result := runner.Run(ctx, "modinfo", "-F", "version", "hailo_pci"); result.Err == nil {
			status.HostDriverVersion = strings.TrimSpace(result.Output)
		}
	}
	if result := runner.Run(ctx, "dpkg-query", "-W", "-f=${Version}", "hailo-dkms"); result.Err == nil {
		status.HostDriverPackageVersion = strings.TrimSpace(result.Output)
	}
	if result := runner.Run(ctx, "dpkg-query", "-W", "-f=${Version}", "hailofw"); result.Err == nil {
		status.HostFirmwareVersion = strings.TrimSpace(result.Output)
	}

	if _, err := runner.LookPath("docker"); err == nil {
		image := runner.Run(ctx, "docker", "image", "inspect", status.ContainerImage)
		status.RuntimeInstalled = image.Err == nil
	}
	if status.RuntimeInstalled {
		result := runHailoContainerCheck(ctx, runner, status, "")
		values := parseKeyValueOutput(result.Output)
		status.ContainerReady = result.Err == nil
		status.DeviceReady = values["DEVICE_READY"] == "true"
		status.DeviceNodeReady = status.DeviceNodeReady && values["DEVICE_NODE_READY"] == "true"
		status.GStreamerReady = values["GSTREAMER_READY"] == "true"
		status.PythonReady = values["PYTHON_READY"] == "true"
		status.Accelerator = fallback(values["DEVICE_ARCHITECTURE"], "unknown")
		status.FirmwareVersion = values["FIRMWARE_VERSION"]
		status.UserspaceVersion = values["HAILORT_VERSION"]
		status.TAPPASVersion = values["TAPPAS_VERSION"]
		status.IdentifyOutput = result.Output
	}

	status.VersionsCompatible =
		versionMatches(status.HostDriverVersion, status.ExpectedDriverVersion) &&
			versionMatches(status.HostDriverPackageVersion, status.ExpectedDriverPackageVersion) &&
			versionMatches(status.HostFirmwareVersion, status.ExpectedFirmwareVersion) &&
			versionMatches(status.UserspaceVersion, status.ExpectedUserspaceVersion) &&
			versionMatches(status.TAPPASVersion, status.ExpectedTAPPASVersion) &&
			versionCoreMatches(status.FirmwareVersion, status.ExpectedDeviceFirmwareVersion)

	if !status.PCIVisible {
		status.MissingComponents = append(status.MissingComponents, "Hailo PCIe device")
	}
	if !status.DeviceNodeReady {
		status.MissingComponents = append(status.MissingComponents, "/dev/hailo0 access")
	}
	if !status.RuntimeInstalled {
		status.MissingComponents = append(status.MissingComponents, "pinned Hailo container image")
	} else if !status.DeviceReady {
		status.MissingComponents = append(status.MissingComponents, "container HailoRT device access")
	}
	if !status.GStreamerReady {
		status.MissingComponents = append(status.MissingComponents, "container hailonet/hailofilter")
	}
	if !status.PythonReady {
		status.MissingComponents = append(status.MissingComponents, "container Hailo Python bindings")
	}
	if !status.VersionsCompatible {
		status.MissingComponents = append(status.MissingComponents, "matching host/container Hailo versions")
	}
	return status
}

func runHailoContainerCheck(ctx context.Context, runner Runner, status HailoStatus, modelPath string) CommandResult {
	checkPath := "/usr/local/bin/atlas-hailo-container-check"
	arguments := []string{}
	if modelPath != "" {
		arguments = append(arguments, "--model", modelPath)
	}
	running := runner.Run(ctx, "docker", "inspect", "--format", "{{.State.Running}}", status.ContainerName)
	if running.Err == nil && strings.TrimSpace(running.Output) == "true" {
		return runner.Run(ctx, "docker", append([]string{"exec", status.ContainerName, checkPath}, arguments...)...)
	}
	dockerArguments := []string{
		"run", "--rm", "--network", "none", "--device", "/dev/hailo0:/dev/hailo0",
		"--env", "ATLAS_HAILO_ACCELERATOR=" + status.Accelerator, "--entrypoint", checkPath,
	}
	if modelPath != "" {
		dockerArguments = append(dockerArguments, "--volume", filepath.Dir(modelPath)+":"+filepath.Dir(modelPath)+":ro")
	}
	dockerArguments = append(dockerArguments, status.ContainerImage)
	dockerArguments = append(dockerArguments, arguments...)
	return runner.Run(ctx, "docker", dockerArguments...)
}

func parseKeyValueOutput(output string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(output, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if found && key != "" {
			values[key] = value
		}
	}
	return values
}

func versionMatches(actual, expected string) bool {
	return actual != "" && expected != "" && actual == expected
}

func versionCoreMatches(actual, expected string) bool {
	if actual == "" || expected == "" {
		return false
	}
	actual = strings.Fields(actual)[0]
	actual, _, _ = strings.Cut(actual, "-")
	expected = strings.Fields(expected)[0]
	expected, _, _ = strings.Cut(expected, "-")
	return actual == expected
}

func parseHailoAccelerator(output string) string {
	normalized := strings.NewReplacer("-", "", "_", "", " ", "").Replace(strings.ToLower(output))
	switch {
	case strings.Contains(normalized, "hailo8l"):
		return "hailo-8l"
	case strings.Contains(normalized, "hailo8"):
		return "hailo-8"
	default:
		return "unknown"
	}
}

func commandSucceeds(ctx context.Context, runner Runner, name string, args ...string) bool {
	if _, err := runner.LookPath(name); err != nil {
		return false
	}
	return runner.Run(ctx, name, args...).Err == nil
}

func probeRTSP(ctx context.Context, runner Runner, url string) RTSPStatus {
	if _, err := runner.LookPath("ffprobe"); err != nil {
		return RTSPStatus{Error: "ffprobe is not installed"}
	}
	probeContext, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result := runner.Run(probeContext, "ffprobe", "-rtsp_transport", "tcp", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=codec_name,width,height", "-of", "csv=p=0", url)
	if result.Err != nil {
		return RTSPStatus{Error: strings.ReplaceAll(result.Output, url, redactURLCredentials(url))}
	}
	fields := strings.Split(strings.TrimSpace(result.Output), ",")
	status := RTSPStatus{Reachable: true}
	if len(fields) > 0 {
		status.Codec = fields[0]
	}
	if len(fields) > 2 {
		status.Width, status.Height = fields[1], fields[2]
	}
	return status
}

func redactURLCredentials(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil {
		return raw
	}
	if _, hasPassword := parsed.User.Password(); hasPassword {
		parsed.User = url.UserPassword(parsed.User.Username(), "redacted")
	}
	return parsed.String()
}

func probeTCP(address string, timeout time.Duration) bool {
	connection, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}

func readEnvironmentFile(path string) map[string]string {
	values := map[string]string{}
	file, err := os.Open(path)
	if err != nil {
		return values
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		values[strings.TrimSpace(key)] = parseEnvironmentValue(value)
	}
	return values
}

func parseEnvironmentValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		if unquoted, err := strconv.Unquote(value); err == nil {
			return unquoted
		}
	}
	return strings.Trim(value, "\"")
}

func installConfigFromDiscovery(discovery Discovery, paths Paths) InstallConfig {
	config := DefaultInstallConfig(paths)
	if len(discovery.Serial) > 0 {
		config.SerialDevice = discovery.Serial[0].Path
	}
	if discovery.Hailo.Accelerator != "unknown" {
		config.HailoAccelerator = discovery.Hailo.Accelerator
	}
	if discovery.Hailo.RuntimeMode == AdapterModeContainer {
		config.PerceptionAdapterMode = AdapterModeContainer
	}
	postprocessReady := fileExists(paths.DefaultPostprocessSO) || config.PerceptionAdapterMode == AdapterModeContainer
	config.PerceptionEnabled = discovery.Hailo.Ready() && fileExists(paths.DefaultModel) && postprocessReady
	applyExistingConfig(&config, discovery.ExistingConfig)
	if discovery.Hailo.RuntimeMode == AdapterModeContainer {
		config.PerceptionAdapterMode = AdapterModeContainer
	}
	return config
}

func applyExistingConfig(config *InstallConfig, values map[string]string) {
	if value := values["ATLAS_DRONE_NAME"]; value != "" {
		config.DroneName = value
	}
	if value := values["ATLAS_GROUND_STATION_ADDR"]; value != "" {
		config.GroundStationAddress = value
	}
	if value := values["ATLAS_FLIGHT_CONTROLLER_ENDPOINT"]; value != "" {
		config.SerialDevice = value
	}
	if value, err := strconv.ParseUint(values["ATLAS_FLIGHT_CONTROLLER_BAUD_RATE"], 10, 32); err == nil && value > 0 {
		config.BaudRate = uint32(value)
	}
	if value := values["ATLAS_A8_RTSP_URL"]; value != "" {
		config.A8RTSPURL = value
	}
	if value := values["ATLAS_CAMERA_TRANSPORT"]; value != "" {
		config.CameraTransport = agentconfig.CameraTransport(strings.ToLower(strings.TrimSpace(value)))
	}
	if value := values["ATLAS_SIYI_CAMERA_ADDR"]; value != "" {
		config.SIYICameraAddress = value
	}
	config.PerceptionEnabled = values["ATLAS_PERCEPTION_PROVIDER"] == "hailo" || (values["ATLAS_PERCEPTION_PROVIDER"] == "" && config.PerceptionEnabled)
	if value := values["ATLAS_PERCEPTION_ADAPTER_MODE"]; value != "" {
		config.PerceptionAdapterMode = value
	}
	if value := values["ATLAS_HAILO_ACCELERATOR"]; value != "" {
		config.HailoAccelerator = value
	}
	if value := values["ATLAS_PERCEPTION_MODEL_PATH"]; value != "" {
		config.ModelPath = value
	}
	if value := values["ATLAS_PERCEPTION_POSTPROCESS_SO"]; value != "" {
		config.PostprocessSO = value
	}
	if value := values["ATLAS_PERCEPTION_POSTPROCESS_FUNCTION"]; value != "" {
		config.PostprocessFunction = value
	}
	if value := values["ATLAS_AGENT_VERSION"]; value != "" {
		config.AgentVersion = value
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func validateDiscovery(discovery Discovery, allowUnsupported bool) error {
	if discovery.PlatformSupported() || allowUnsupported {
		return nil
	}
	return fmt.Errorf("unsupported onboard platform: need Ubuntu 24.04 arm64 on Raspberry Pi 5, found %s, %s, %s", discovery.OS.PrettyName, discovery.Architecture, discovery.BoardModel)
}

func ensureSerialCandidate(config InstallConfig, discovery Discovery) error {
	if config.SerialDevice == "" {
		return errors.New("no flight-controller serial device was detected")
	}
	return nil
}
