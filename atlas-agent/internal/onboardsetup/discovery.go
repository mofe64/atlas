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
		Hailo:           discoverHailo(ctx, runner),
		Camera:          probeRTSP(ctx, runner, cameraURL),
		GroundReachable: probeTCP(groundAddress, 800*time.Millisecond),
		ExistingConfig:  existingConfig,
		LegacyUnits:     discoverLegacyUnits(paths.Root),
	}
	return discovery, nil
}

func discoverLegacyUnits(root string) []string {
	names := []string{
		"atlas-agent.service",
		"atlas-mavsdk.service",
		"atlas-mavlink-router.service",
		"atlas-mediamtx.service",
		"atlas-video-agent.service",
	}
	units := make([]string, 0, len(names))
	for _, name := range names {
		path := rootPath(root, filepath.Join("/etc/systemd/system", name))
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
			units = append(units, path)
		}
	}
	return units
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

func discoverHailo(ctx context.Context, runner Runner) HailoStatus {
	status := HailoStatus{Accelerator: "unknown"}
	if _, err := runner.LookPath("lspci"); err == nil {
		result := runner.Run(ctx, "lspci", "-Dnn")
		lower := strings.ToLower(result.Output)
		status.PCIVisible = result.Err == nil && (strings.Contains(lower, "hailo") || strings.Contains(lower, "[1e60:"))
	}
	if _, err := runner.LookPath("hailortcli"); err == nil {
		status.RuntimeInstalled = true
		result := runner.Run(ctx, "hailortcli", "fw-control", "identify")
		status.IdentifyOutput = result.Output
		status.DeviceReady = result.Err == nil
		status.Accelerator = parseHailoAccelerator(result.Output)
	}
	status.GStreamerReady = commandSucceeds(ctx, runner, "gst-inspect-1.0", "hailonet") && commandSucceeds(ctx, runner, "gst-inspect-1.0", "hailofilter")
	status.PythonReady = commandSucceeds(ctx, runner, "python3", "-c", "import gi; gi.require_version('Gst', '1.0'); from gi.repository import Gst; import hailo")
	status.AptPackageReady = commandSucceeds(ctx, runner, "apt-cache", "show", "hailo-all")
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
		status.MissingComponents = append(status.MissingComponents, "Hailo Python bindings")
	}
	return status
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
	config.PerceptionEnabled = discovery.Hailo.Ready() && fileExists(paths.DefaultModel) && fileExists(paths.DefaultPostprocessSO)
	applyExistingConfig(&config, discovery.ExistingConfig)
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
	if value := values["ATLAS_SIYI_CAMERA_ADDR"]; value != "" {
		config.SIYICameraAddress = value
	}
	config.PerceptionEnabled = values["ATLAS_PERCEPTION_PROVIDER"] == "hailo" || (values["ATLAS_PERCEPTION_PROVIDER"] == "" && config.PerceptionEnabled)
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
