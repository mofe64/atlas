package onboardsetup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type ApplyResult struct {
	RebootRequired  bool
	PerceptionReady bool
	SpatialReady    bool
}

func Install(ctx context.Context, runner Runner, options Options) (ApplyResult, error) {
	if options.Input == nil {
		options.Input = os.Stdin
	}
	if options.Output == nil {
		options.Output = os.Stdout
	}
	if options.Paths.ConfigFile == "" {
		options.Paths = DefaultPaths("/")
	}
	discovery, err := Discover(ctx, runner, options)
	if err != nil {
		return ApplyResult{}, err
	}
	if err := validateDiscovery(discovery, options.AllowUnsupported); err != nil {
		return ApplyResult{}, err
	}
	manifest := readEnvironmentFile(options.Paths.ReleaseManifest)
	modelAccelerator := manifest["ATLAS_MODEL_ACCELERATOR"]
	options.PackagedModelAccelerator = modelAccelerator
	if version := manifest["ATLAS_RELEASE_VERSION"]; version != "" {
		discovery.ExistingConfig["ATLAS_AGENT_VERSION"] = version
	}
	plan, err := BuildInstallPlan(ctx, runner, discovery, options)
	if err != nil {
		return ApplyResult{}, err
	}
	if plan.Config.PerceptionEnabled {
		if err := validateModelAccelerator(modelAccelerator, discovery.Hailo); err != nil {
			return ApplyResult{}, err
		}
	}
	if version := manifest["ATLAS_RELEASE_VERSION"]; version != "" {
		plan.Config.AgentVersion = version
	}
	return ApplyInstallPlan(ctx, runner, options, plan)
}

func validateModelAccelerator(modelAccelerator string, hailo HailoStatus) error {
	if modelAccelerator == "" || !hailo.Ready() {
		return nil
	}
	if hailo.Accelerator == "unknown" {
		return fmt.Errorf("the Hailo runtime is ready but its accelerator type could not be identified; refusing to select the packaged %s HEF", modelAccelerator)
	}
	if modelAccelerator != hailo.Accelerator {
		return fmt.Errorf("packaged HEF targets %s but the detected accelerator is %s; install an Atlas package built with a compatible HEF", modelAccelerator, hailo.Accelerator)
	}
	return nil
}

func ApplyInstallPlan(ctx context.Context, commandRunner Runner, options Options, plan InstallPlan) (ApplyResult, error) {
	if !options.DryRun && !isRoot() && (options.Paths.Root == "" || options.Paths.Root == "/") {
		return ApplyResult{}, errors.New("atlas-setup install must run as root; use sudo atlas-setup")
	}
	output := options.Output
	if output == nil {
		output = io.Discard
	}
	runner := ApplyRunner{Runner: commandRunner, DryRun: options.DryRun, Output: output}
	if err := validateInstalledPayload(options.Paths, plan.Config, options.DryRun); err != nil {
		return ApplyResult{}, err
	}
	if err := ensureServiceAccount(ctx, commandRunner, runner); err != nil {
		return ApplyResult{}, err
	}
	if !options.DryRun {
		if err := verifySerialAccess(ctx, commandRunner, plan.Config.SerialDevice); err != nil {
			return ApplyResult{}, err
		}
		if plan.Config.PerceptionEnabled && plan.Config.PerceptionAdapterMode == AdapterModeProcess {
			if err := ensureHailoAccess(ctx, commandRunner, runner); err != nil {
				return ApplyResult{}, err
			}
		}
	}
	spatialReady, err := ensureSpatialRuntime(ctx, commandRunner, runner, options, &plan.Config)
	if err != nil {
		return ApplyResult{}, err
	}
	if err := writeConfiguration(ctx, runner, options, plan.Config); err != nil {
		return ApplyResult{}, err
	}
	if err := runner.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return ApplyResult{}, err
	}

	result := ApplyResult{PerceptionReady: !plan.Config.PerceptionEnabled, SpatialReady: spatialReady}
	if plan.Config.PerceptionEnabled && !options.DryRun {
		hailo := discoverHailo(ctx, commandRunner, options.Paths)
		postprocessReady := fileExists(plan.Config.PostprocessSO) || plan.Config.PerceptionAdapterMode == AdapterModeContainer
		result.PerceptionReady = hailo.Ready() && fileExists(plan.Config.ModelPath) && postprocessReady
		if !result.PerceptionReady {
			return result, errors.New("Hailo perception was selected but its runtime, model, or postprocess library is not ready")
		}
	}
	if plan.Config.PerceptionAdapterMode != AdapterModeContainer || !plan.Config.PerceptionEnabled {
		runner.RunOptional(ctx, "systemctl", "disable", "--now", "atlas-hailo-adapter.service")
	}
	if !plan.Config.SpatialEnabled {
		runner.RunOptional(ctx, "systemctl", "disable", "--now", "atlas-spatial-runtime.service")
	}
	if err := runner.Run(ctx, "systemctl", append([]string{"enable", "--now"}, configuredServices(plan.Config)...)...); err != nil {
		return ApplyResult{}, err
	}
	if options.DryRun {
		_, _ = fmt.Fprintln(output, "Atlas onboard dry-run complete; no files or services were changed.")
		return result, nil
	}
	_, _ = fmt.Fprintln(output, "Atlas onboard installation is active. Run 'sudo atlas-setup doctor' for the full health report.")
	return result, nil
}

func ensureSpatialRuntime(ctx context.Context, commandRunner Runner, runner ApplyRunner, options Options, config *InstallConfig) (bool, error) {
	if !config.SpatialEnabled {
		return true, nil
	}
	imageReady := commandRunner.Run(ctx, "docker", "image", "inspect", config.SpatialContainerImage).Err == nil
	if !imageReady {
		if err := runner.Run(ctx, options.Paths.SpatialSetupBinary, "--image", config.SpatialContainerImage, "--build-local"); err != nil {
			return false, fmt.Errorf("prepare spatial runtime: %w", err)
		}
	}
	if config.SpatialProvider == SpatialProviderDepthAI {
		runner.RunOptional(ctx, "udevadm", "control", "--reload-rules")
		runner.RunOptional(ctx, "udevadm", "trigger", "--subsystem-match=usb", "--attr-match=idVendor=03e7")
	}
	if options.DryRun {
		return true, nil
	}
	if result := commandRunner.Run(ctx, "docker", "image", "inspect", config.SpatialContainerImage); result.Err != nil {
		return false, fmt.Errorf("spatial runtime image is not available after setup: %s", config.SpatialContainerImage)
	}
	resolved := commandRunner.Run(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", config.SpatialContainerImage)
	if resolved.Err != nil || !strings.HasPrefix(strings.TrimSpace(resolved.Output), "sha256:") {
		return false, fmt.Errorf("could not resolve immutable spatial runtime image id for %s", config.SpatialContainerImage)
	}
	config.SpatialContainerImage = strings.TrimSpace(resolved.Output)
	return true, nil
}

func validateInstalledPayload(paths Paths, config InstallConfig, dryRun bool) error {
	if dryRun {
		return nil
	}
	required := []string{paths.AgentBinary, paths.MAVSDKBinary, paths.AgentService, paths.MAVSDKService}
	if config.PerceptionEnabled && config.PerceptionAdapterMode == AdapterModeProcess {
		required = append(required, paths.HailoAdapter)
	} else if config.PerceptionEnabled && config.PerceptionAdapterMode == AdapterModeContainer {
		required = append(required, paths.HailoContainerService, paths.HailoContainerEnv)
	}
	if config.SpatialEnabled {
		required = append(required, paths.SpatialSetupBinary, paths.SpatialContainerRun, paths.SpatialCheck, paths.SpatialService, filepath.Join(paths.SpatialContext, "packaging", "Dockerfile"))
	}
	for _, path := range required {
		if !fileExists(path) {
			return fmt.Errorf("Atlas package payload is incomplete; missing %s", path)
		}
	}
	return nil
}

func configuredServices(config InstallConfig) []string {
	services := []string{"atlas-mavsdk.service", "atlas-agent.service"}
	if config.PerceptionEnabled && config.PerceptionAdapterMode == AdapterModeContainer {
		services = append(services, "atlas-hailo-adapter.service")
	}
	if config.SpatialEnabled {
		services = append(services, "atlas-spatial-runtime.service")
	}
	return services
}

func ensureServiceAccount(ctx context.Context, commandRunner Runner, runner ApplyRunner) error {
	if commandRunner.Run(ctx, "id", "-u", "atlas-agent").Err != nil {
		if err := runner.Run(ctx, "useradd", "--system", "--user-group", "--home-dir", "/var/lib/atlas-agent", "--shell", "/usr/sbin/nologin", "atlas-agent"); err != nil {
			return err
		}
	}
	return runner.Run(ctx, "usermod", "-a", "-G", "dialout", "atlas-agent")
}

func verifySerialAccess(ctx context.Context, runner Runner, device string) error {
	for _, mode := range []string{"-r", "-w"} {
		result := runner.Run(ctx, "runuser", "-u", "atlas-agent", "--", "test", mode, device)
		if result.Err != nil {
			return fmt.Errorf("atlas-agent service user cannot access TELEM2 device %s; verify its udev permissions and dialout group", device)
		}
	}
	return nil
}

func ensureHailoAccess(ctx context.Context, commandRunner Runner, runner ApplyRunner) error {
	devices, _ := filepath.Glob("/dev/hailo*")
	for _, device := range devices {
		groupResult := commandRunner.Run(ctx, "stat", "-c", "%G", device)
		group := strings.TrimSpace(groupResult.Output)
		if groupResult.Err == nil && group != "" && group != "root" {
			if err := runner.Run(ctx, "usermod", "-a", "-G", group, "atlas-agent"); err != nil {
				return err
			}
		}
	}
	result := commandRunner.Run(ctx, "runuser", "-u", "atlas-agent", "--", "hailortcli", "fw-control", "identify")
	if result.Err != nil {
		return fmt.Errorf("atlas-agent service user cannot access the Hailo device: %w%s", result.Err, outputSuffix(result.Output))
	}
	return nil
}

func writeConfiguration(ctx context.Context, runner ApplyRunner, options Options, config InstallConfig) error {
	content, err := RenderEnvironment(config, options.Paths)
	if err != nil {
		return err
	}
	spatialContent, err := RenderSpatialEnvironment(config, options.Paths)
	if err != nil {
		return err
	}
	if options.DryRun {
		_, _ = fmt.Fprintf(options.Output, "--- %s (0640 root:atlas-agent) ---\n%s", options.Paths.ConfigFile, content)
		_, _ = fmt.Fprintf(options.Output, "--- %s (0640 root:atlas-agent) ---\n%s", options.Paths.SpatialConfigFile, spatialContent)
		return nil
	}
	if err := installEnvironmentFile(ctx, runner, content, options.Paths.ConfigFile); err != nil {
		return err
	}
	if err := installEnvironmentFile(ctx, runner, spatialContent, options.Paths.SpatialConfigFile); err != nil {
		return err
	}
	if err := runner.Run(ctx, "install", "-d", "-m", "0750", "-o", "atlas-agent", "-g", "atlas-agent", options.Paths.StateDirectory, options.Paths.RuntimeDirectory, options.Paths.SpatialStateDirectory, filepath.Join(options.Paths.SpatialStateDirectory, "log")); err != nil {
		return err
	}
	return nil
}

func installEnvironmentFile(ctx context.Context, runner ApplyRunner, content, destination string) error {
	temporary, err := os.CreateTemp("", "atlas-agent-env-*")
	if err != nil {
		return fmt.Errorf("create temporary configuration: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.WriteString(content); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary configuration: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary configuration: %w", err)
	}
	return runner.Run(ctx, "install", "-D", "-m", "0640", "-o", "root", "-g", "atlas-agent", temporaryPath, destination)
}

func RenderEnvironment(config InstallConfig, paths Paths) (string, error) {
	provider := "disabled"
	if config.PerceptionEnabled {
		provider = "hailo"
	}
	values := [][2]string{
		{"ATLAS_AGENT_STATE_DIR", paths.StateDirectory},
		{"ATLAS_GROUND_STATION_ADDR", config.GroundStationAddress},
		{"ATLAS_DRONE_NAME", config.DroneName},
		{"ATLAS_FLIGHT_CONTROLLER_TRANSPORT", "serial"},
		{"ATLAS_FLIGHT_CONTROLLER_ENDPOINT", config.SerialDevice},
		{"ATLAS_FLIGHT_CONTROLLER_BAUD_RATE", strconv.FormatUint(uint64(config.BaudRate), 10)},
		{"ATLAS_MAVLINK_SYSTEM_ID", strconv.FormatUint(uint64(config.MAVLinkSystemID), 10)},
		{"ATLAS_MAVLINK_COMPONENT_ID", strconv.FormatUint(uint64(config.MAVLinkComponentID), 10)},
		{"ATLAS_MAVSDK_GRPC_ADDR", DefaultMAVSDKAddr},
		{"ATLAS_MAVSDK_GRPC_PORT", "50051"},
		{"ATLAS_MAVSDK_SYSTEM_ADDRESS", "serial://" + config.SerialDevice + ":" + strconv.FormatUint(uint64(config.BaudRate), 10)},
		{"ATLAS_CAMERA_TRANSPORT", string(config.CameraTransport)},
		{"ATLAS_SIYI_CAMERA_ADDR", config.SIYICameraAddress},
		{"ATLAS_PERCEPTION_PROVIDER", provider},
		{"ATLAS_PERCEPTION_ADAPTER_MODE", config.PerceptionAdapterMode},
		{"ATLAS_PERCEPTION_SOCKET_PATH", filepath.Join(paths.RuntimeDirectory, "perception.sock")},
		{"ATLAS_PERCEPTION_ADAPTER_PATH", paths.HailoAdapter},
		{"ATLAS_A8_RTSP_URL", config.A8RTSPURL},
		{"ATLAS_A8_RTP_CODEC", "auto"},
		{"ATLAS_A8_RTSP_TRANSPORT", "tcp"},
		{"ATLAS_A8_RTSP_LATENCY_MS", "75"},
		{"ATLAS_VIDEO_SOURCE_ID", "a8-main"},
		{"ATLAS_PERCEPTION_MODEL_PATH", config.ModelPath},
		{"ATLAS_PERCEPTION_POSTPROCESS_SO", config.PostprocessSO},
		{"ATLAS_PERCEPTION_POSTPROCESS_FUNCTION", config.PostprocessFunction},
		{"ATLAS_HAILO_ACCELERATOR", config.HailoAccelerator},
		{"ATLAS_PERCEPTION_WIDTH", "640"},
		{"ATLAS_PERCEPTION_HEIGHT", "640"},
		{"ATLAS_TRACKER_ALGORITHM", "byte_track"},
		{"ATLAS_TRACKER_MAX_TIMESTAMP_GAP", "2s"},
		{"ATLAS_TRACKER_CMC_MIN_CONFIDENCE", "0.25"},
		{"ATLAS_TRACKER_CMC_MAX_DIMENSION", "320"},
		{"ATLAS_TRACKER_CMC_MAX_FEATURES", "300"},
		{"ATLAS_BYTETRACK_WORKER_PATH", paths.ByteTrackWorker},
		{"ATLAS_BYTETRACK_REQUEST_TIMEOUT", "250ms"},
		{"ATLAS_BYTETRACK_FRAME_RATE", "30"},
		{"ATLAS_BYTETRACK_TRACK_THRESHOLD", "0.50"},
		{"ATLAS_BYTETRACK_HIGH_THRESHOLD", "0.60"},
		{"ATLAS_BYTETRACK_MATCH_THRESHOLD", "0.80"},
		{"ATLAS_BYTETRACK_BUFFER_FRAMES", "30"},
	}
	var builder strings.Builder
	builder.WriteString("# Generated by atlas-setup. Re-run atlas-setup to reconfigure.\n")
	for _, entry := range values {
		quoted, err := quoteEnvironmentValue(entry[1])
		if err != nil {
			return "", fmt.Errorf("%s: %w", entry[0], err)
		}
		fmt.Fprintf(&builder, "%s=%s\n", entry[0], quoted)
	}
	return builder.String(), nil
}

func RenderSpatialEnvironment(config InstallConfig, paths Paths) (string, error) {
	values := [][2]string{
		{"ATLAS_SPATIAL_ENABLED", strconv.FormatBool(config.SpatialEnabled)},
		{"ATLAS_SPATIAL_CONTRACT_VERSION", "1"},
		{"ATLAS_SPATIAL_PROVIDER", config.SpatialProvider},
		{"ATLAS_SPATIAL_SOURCE_ID", config.SpatialSourceID},
		{"ATLAS_SPATIAL_DEVICE_ID", config.SpatialDeviceID},
		{"ATLAS_SPATIAL_MODEL", config.SpatialModel},
		{"ATLAS_SPATIAL_USB_TRANSPORT", config.SpatialUSBTransport},
		{"ATLAS_SPATIAL_SOCKET_PATH", filepath.Join(paths.RuntimeDirectory, "spatial.sock")},
		{"ATLAS_SPATIAL_CONTAINER_IMAGE", config.SpatialContainerImage},
		{"ATLAS_SPATIAL_CONTAINER_NAME", "atlas-spatial-runtime"},
	}
	var builder strings.Builder
	builder.WriteString("# Generated by atlas-setup. Camera-vendor details stop at this boundary.\n")
	for _, entry := range values {
		quoted, err := quoteEnvironmentValue(entry[1])
		if err != nil {
			return "", fmt.Errorf("%s: %w", entry[0], err)
		}
		fmt.Fprintf(&builder, "%s=%s\n", entry[0], quoted)
	}
	return builder.String(), nil
}

func quoteEnvironmentValue(value string) (string, error) {
	if strings.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("configuration values cannot contain control characters")
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`, nil
}
