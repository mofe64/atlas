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
	// run discovery process to determine initial config, default paths, board model, serial device, hailo, spatial, etc.
	discovery, err := Discover(ctx, runner, options)
	if err != nil {
		return ApplyResult{}, err
	}
	// validate discovery results, we are just essentially checking that the platform is supported (arm64 ubuntu 24.04) since thats the only platform I have for now.
	// should allow for other platform when if i get jetson nano
	if err := validateDiscovery(discovery, options.AllowUnsupported); err != nil {
		return ApplyResult{}, err
	}
	manifest := readEnvironmentFile(options.Paths.ReleaseManifest)
	modelAccelerator := manifest["ATLAS_MODEL_ACCELERATOR"]
	options.PackagedModelAccelerator = modelAccelerator
	if version := manifest["ATLAS_RELEASE_VERSION"]; version != "" {
		discovery.ExistingConfig["ATLAS_AGENT_VERSION"] = version
	}
	// build the install plan
	plan, err := BuildInstallPlan(ctx, runner, discovery, options)
	if err != nil {
		return ApplyResult{}, err
	}
	// validate the model accelerator if perception is enabled
	if plan.Config.PerceptionEnabled {
		if err := validateModelAccelerator(modelAccelerator, discovery.Hailo); err != nil {
			return ApplyResult{}, err
		}
	}
	// set the agent version
	if version := manifest["ATLAS_RELEASE_VERSION"]; version != "" {
		plan.Config.AgentVersion = version
	}
	// apply the install plan
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

// Takes the config produced by BuildInstallPlan and applies it to the operating system
// Does the following:
// 1. check for permissions to modify os
// 2. verify that packages supplied all required files
// 3. prepare the service user
// 4. verify that the service user can access hardware
// 5. write the configuration files
// 6. reload and activate systemd services
// 7. reports final readiness status
func ApplyInstallPlan(ctx context.Context, commandRunner Runner, options Options, plan InstallPlan) (ApplyResult, error) {
	// if this isn't a dry run, and we are not running as root, and the root path is not set, we return an error
	// for actual installation, we need root, tor create system user, change group membership
	// write under /etc, /var and /run and to realod and control systemd services
	if !options.DryRun && !isRoot() && (options.Paths.Root == "" || options.Paths.Root == "/") { // isRoot() checks if effective user id is 0 (root)
		// a custom Paths.Root bypasses this root check, but out cli currently enforces that custom root is only used for dry runs
		// we should preserve this invariant for any other callers
		return ApplyResult{}, errors.New("atlas-setup install must run as root; use sudo atlas-setup")
	}

	// if output destination is not set, use io.Discard to avoid logging to the console and avoid a panic
	output := options.Output
	if output == nil {
		output = io.Discard
	}

	// we are wrapping the command runner with our own custom ApplyRunner that adds dry run functionality and output destination
	runner := ApplyRunner{Runner: commandRunner, DryRun: options.DryRun, Output: output}

	// validate the provided install payload
	// this confirms that the relevant packages have already been installed, and we are good to go
	if err := validateInstalledPayload(options.Paths, plan.Config, options.DryRun); err != nil {
		return ApplyResult{}, err
	}

	// prepare service account, we run atlas under `atlas-agent` user, so this method will create the user if it doesn't exist
	// and add it to the dialout group to access the serial device
	if err := ensureServiceAccount(ctx, commandRunner, runner); err != nil {
		return ApplyResult{}, err
	}
	// if we aren't in dry run mode,
	if !options.DryRun {
		// verify that the service user can access the serial device (should be on telem2 for the pixhawk device)
		if err := verifySerialAccess(ctx, commandRunner, plan.Config.SerialDevice); err != nil {
			return ApplyResult{}, err
		}
		// if we are running perception in process mode, we need to verify that the service user can access the Hailo device
		// we don't do this for container mode, since this is already handled bu the docker service and the container health checks we did when we built the install plan
		if plan.Config.PerceptionEnabled && plan.Config.PerceptionAdapterMode == AdapterModeProcess {
			if err := ensureHailoAccess(ctx, commandRunner, runner); err != nil {
				return ApplyResult{}, err
			}
		}
	}
	// prepare spatial runtime
	// relaods device rules for DepthAI devices, and checks if the spatial runtime binary is present
	spatialReady, err := ensureSpatialRuntime(ctx, runner, options, &plan.Config)
	if err != nil {
		return ApplyResult{}, err
	}

	// wriet the configuration files
	// writes the followung
	// /etc/atlas-agent/atlas-agent.env
	// /etc/atlas-agent/spatial.env
	// /etc/atlas-agent/aircraft-profile.json
	// it also creates the state directory and runtime directory
	if err := writeConfiguration(ctx, runner, options, plan.Config); err != nil {
		return ApplyResult{}, err
	}

	// reload systemd daemon to pick up new configuration and services
	if err := runner.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return ApplyResult{}, err
	}
	// set out readiness status, if perception is disabled we mark it as ready
	result := ApplyResult{PerceptionReady: !plan.Config.PerceptionEnabled, SpatialReady: spatialReady}

	// if perception is enabled and we are not in dry run mode, we need to verify that the Hailo device is ready, the model is present, and the postprocess library is present, before we activate the service
	if plan.Config.PerceptionEnabled && !options.DryRun {
		hailo := discoverHailo(ctx, commandRunner, options.Paths)                                                              // rerun hailo discovery to get the latest status, since things might have changed since we built the install plan
		postprocessReady := fileExists(plan.Config.PostprocessSO) || plan.Config.PerceptionAdapterMode == AdapterModeContainer // postprocess library is optional for container mode, but required for process mode
		result.PerceptionReady = hailo.Ready() && fileExists(plan.Config.ModelPath) && postprocessReady                        // determine final perception readiness status
		if !result.PerceptionReady {
			// throw error if any perception readiness condition is not met
			return result, errors.New("Hailo perception was selected but its runtime, model, or postprocess library is not ready")
		}
	}

	// disable old hailo adapter service if we are not running in container mode or perception is disabled
	// In process mode, the Atlas Agent manages the host Hailo adapter process itself using the configured adapter binary,
	// so if we are running in process mode, we need to disable the container service, Otherwise, both adapters could compete for /dev/hailo0 or the same perception socket
	if plan.Config.PerceptionAdapterMode != AdapterModeContainer || !plan.Config.PerceptionEnabled {
		runner.RunOptional(ctx, "systemctl", "disable", "--now", "atlas-hailo-adapter.service")
	}
	// disable spatial runtime service if spatial is disabled
	if !plan.Config.SpatialEnabled {
		runner.RunOptional(ctx, "systemctl", "disable", "--now", "atlas-spatial-runtime.service")
	}

	// now enable and start the services that are configured to run
	// note configured service will always include atlas-agent.service and atlas-mavsdk.service
	// it contiditionally adds atlas-hailo-adapter.service when container perception is enabled, in procecc mode, there is no indepenetent hailo systemd service, atlas uses
	// the configured adapter binary directly
	// and atlas-spatial-runtime.service when spatial is enabled
	if err := runner.Run(ctx, "systemctl", append([]string{"enable", "--now"}, configuredServices(plan.Config)...)...); err != nil {
		return ApplyResult{}, err
	}
	// restart spatail with the new environment
	if plan.Config.SpatialEnabled {
		// A package upgrade may have restarted Spatial with its previous
		// environment. Restart once so this setup run's device selection and
		// frame contract are applied immediately.
		if err := runner.Run(ctx, "systemctl", "restart", "atlas-spatial-runtime.service"); err != nil {
			return ApplyResult{}, err
		}
	}
	if options.DryRun {
		_, _ = fmt.Fprintln(output, "Atlas onboard dry-run complete; no files or services were changed.")
		return result, nil
	}
	_, _ = fmt.Fprintln(output, "Atlas onboard installation is active. Run 'sudo atlas-setup doctor' for the full health report.")
	return result, nil
}

// ensure that the spatial runtime is ready
func ensureSpatialRuntime(ctx context.Context, runner ApplyRunner, options Options, config *InstallConfig) (bool, error) {
	// if the spatial config says spatial is disabled, we retrun true here
	if !config.SpatialEnabled {
		return true, nil
	}

	// if the spatial provider is DepthAI (oak-d lite), we need to reload the udev rules and trigger a USB device event
	// this retriggers usb devices with vendor id 03e7 (DepthAI)
	// the objective here is to apply newly installed permissions without requiring the camera to be unplugged and plugged back in
	// we use runOptional so that a udev reload failure does not immediately abort our setup process. since later runtime and healht checks
	// can provide a stronger readiness signal
	if config.SpatialProvider == SpatialProviderDepthAI {
		runner.RunOptional(ctx, "udevadm", "control", "--reload-rules")
		runner.RunOptional(ctx, "udevadm", "trigger", "--subsystem-match=usb", "--attr-match=idVendor=03e7")
	}
	// if we are in dry run mode, we return true here
	if options.DryRun {
		return true, nil
	}
	// check if the spatial runtime binary exists
	return fileExists(options.Paths.SpatialRuntimeBinary), nil
}

func validateInstalledPayload(paths Paths, config InstallConfig, dryRun bool) error {
	// no validation for dry runs
	if dryRun {
		return nil
	}
	// the min required payload for installation
	// should be
	// 	/usr/bin/atlas-agent
	// /usr/libexec/atlas-agent/mavsdk_server
	// /usr/lib/systemd/system/atlas-agent.service
	// /usr/lib/systemd/system/atlas-mavsdk.service
	required := []string{paths.AgentBinary, paths.MAVSDKBinary, paths.AgentService, paths.MAVSDKService}

	// if perception is enabled and adapter mode is process, we need to add the hailo adapter binary to the required paths
	if config.PerceptionEnabled && config.PerceptionAdapterMode == AdapterModeProcess {
		required = append(required, paths.HailoAdapter)
		// if container mode, we need to add the hailo container service and environment file to the required paths
	} else if config.PerceptionEnabled && config.PerceptionAdapterMode == AdapterModeContainer {
		required = append(required, paths.HailoContainerService, paths.HailoContainerEnv)
	}

	// for each required path, we check if it exists
	for _, path := range required {
		if !fileExists(path) {
			return fmt.Errorf("Atlas package payload is incomplete; missing %s", path)
		}
	}
	// if spatial is enabled, we need to add the spatial runtime binary, check, and service to the required paths
	if config.SpatialEnabled {
		for _, path := range []string{paths.SpatialRuntimeBinary, paths.SpatialCheck, paths.SpatialService} {
			if !fileExists(path) {
				return fmt.Errorf("Atlas Spatial Runtime is not installed; install atlas-spatial-runtime on the Pi before enabling depth acquisition (missing %s)", path)
			}
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
	// check if the service account exists
	if commandRunner.Run(ctx, "id", "-u", "atlas-agent").Err != nil {
		// if it doesn't create it
		// --system: create a system/service account rather than a normal login user.
		// --user-group: create a matching atlas-agent group.
		// --home-dir /var/lib/atlas-agent: set the service state directory as the account’s home.
		// --shell /usr/sbin/nologin: prevent interactive login.
		// atlas-agent: account name.
		if err := runner.Run(ctx, "useradd", "--system", "--user-group", "--home-dir", "/var/lib/atlas-agent", "--shell", "/usr/sbin/nologin", "atlas-agent"); err != nil {
			return err
		}
	}
	// add the service account to the dialout group, allows the service account to access the serial device
	// -G dialout: add dialout as a supplementary group.
	// -a: append it without removing existing supplementary groups.
	// Without -a, usermod -G could replace all the user’s other supplementary groups.
	return runner.Run(ctx, "usermod", "-a", "-G", "dialout", "atlas-agent")
}

func verifySerialAccess(ctx context.Context, runner Runner, device string) error {
	// we check two modes of access read and write
	// we need to check both modes, since mavsdk needs ro readt telem and heartbeat messages
	// and also needs write for commands and protocol messages
	for _, mode := range []string{"-r", "-w"} {
		result := runner.Run(ctx, "runuser", "-u", "atlas-agent", "--", "test", mode, device)
		if result.Err != nil {
			return fmt.Errorf("atlas-agent service user cannot access TELEM2 device %s; verify its udev permissions and dialout group", device)
		}
	}
	return nil
}

func ensureHailoAccess(ctx context.Context, commandRunner Runner, runner ApplyRunner) error {
	// find all hailo devices
	devices, _ := filepath.Glob("/dev/hailo*")
	// for each device, check its owning group
	for _, device := range devices {
		groupResult := commandRunner.Run(ctx, "stat", "-c", "%G", device)
		group := strings.TrimSpace(groupResult.Output)
		// if the device is not owned by root, add the atlas-agent user to the group
		if groupResult.Err == nil && group != "" && group != "root" {
			if err := runner.Run(ctx, "usermod", "-a", "-G", group, "atlas-agent"); err != nil {
				return err
			}
		}
	}
	// perform a hailortcli command to confirm that hailo is setup and ready, and that the service user can access it
	result := commandRunner.Run(ctx, "runuser", "-u", "atlas-agent", "--", "hailortcli", "fw-control", "identify")
	if result.Err != nil {
		return fmt.Errorf("atlas-agent service user cannot access the Hailo device: %w%s", result.Err, outputSuffix(result.Output))
	}
	return nil
}

func writeConfiguration(ctx context.Context, runner ApplyRunner, options Options, config InstallConfig) error {
	// generate string rep for the agent environment values
	content, err := RenderEnvironment(config, options.Paths)
	if err != nil {
		return err
	}
	// generate string rep for the spatial environment values
	spatialContent, err := RenderSpatialEnvironment(config, options.Paths)
	if err != nil {
		return err
	}
	// for dry run, we don't modify filesystem, so we just print the configuration files alongside, the aircraft profile,
	// intended ownership and permissions to the output
	if options.DryRun {
		_, _ = fmt.Fprintf(
			options.Output,
			"--- %s -> %s (0640 root:atlas-agent) ---\n",
			config.AircraftProfileSourcePath,
			options.Paths.AircraftProfileConfig,
		)
		_, _ = fmt.Fprintf(options.Output, "--- %s (0640 root:atlas-agent) ---\n%s", options.Paths.ConfigFile, content)
		_, _ = fmt.Fprintf(options.Output, "--- %s (0640 root:atlas-agent) ---\n%s", options.Paths.SpatialConfigFile, spatialContent)
		return nil
	}

	// install the aircraft profile
	// 	-D: create missing parent directories.
	// -m 0640: owner can read/write, group can read, everyone else has no access.
	// -o root: root owns the file.
	// -g atlas-agent: Atlas services can read it.
	// This produces a stable active-profile path. The source profile may remain among several packaged profiles, while the runtime always reads one selected copy.
	if err := runner.Run(
		ctx,
		"install",
		"-D",
		"-m",
		"0640",
		"-o",
		"root",
		"-g",
		"atlas-agent",
		config.AircraftProfileSourcePath,
		options.Paths.AircraftProfileConfig,
	); err != nil {
		return err
	}
	// install the agent environment file
	// the helper function
	// 1. creates a temp file
	// 2. writes the complete contet to the temp file
	// 3. closes the temp file
	// 4. invokes install to copy it to the destination with correct ownership and permissions;
	// 5. removes the temp file
	// 	The destination receives:
	// mode:  0640
	// owner: root
	// group: atlas-agent
	// Closing the temporary file before invoking install ensures all buffered content has been handed to the operating system.
	if err := installEnvironmentFile(ctx, runner, content, options.Paths.ConfigFile); err != nil {
		return err
	}
	// install the spatial environment file
	if err := installEnvironmentFile(ctx, runner, spatialContent, options.Paths.SpatialConfigFile); err != nil {
		return err
	}
	if err := runner.Run(ctx, "install", "-d", "-m", "0750", "-o", "atlas-agent", "-g", "atlas-agent", options.Paths.StateDirectory, options.Paths.RuntimeDirectory); err != nil {
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
		{"ATLAS_AGENT_VERSION", config.AgentVersion},
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
		{"ATLAS_AIRCRAFT_PROFILE_ID", config.AircraftProfileID},
		{"ATLAS_AIRCRAFT_PROFILE_PATH", paths.AircraftProfileConfig},
		{"ATLAS_SPATIAL_PROVIDER", config.SpatialProvider},
		{"ATLAS_SPATIAL_DEVICE_ID", config.SpatialDeviceID},
		{"ATLAS_SPATIAL_MODEL", config.SpatialModel},
		{"ATLAS_SPATIAL_USB_TRANSPORT", config.SpatialUSBTransport},
		{"ATLAS_SPATIAL_SOCKET_PATH", filepath.Join(paths.RuntimeDirectory, "spatial.sock")},
		{"ATLAS_SPATIAL_FRAME_ID", config.SpatialFrameID},
		{"ATLAS_SPATIAL_WIDTH", "640"},
		{"ATLAS_SPATIAL_HEIGHT", "400"},
		{"ATLAS_SPATIAL_FPS", "20"},
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
