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

// Note: for hailo readiness, we have two modes:
// 1. Process mode: which requires:
// -PCI device visible
// -AND HailoRT installed
// -AND device responds
// -AND GStreamer plugins work
// -AND Python bindings work

// 2. Container mode: which additionally requires:
// - /dev/hailo0 ready
// - pinned container image installed
// - host/container versions compatible

func Discover(ctx context.Context, runner Runner, options Options) (Discovery, error) {
	paths := options.Paths
	if paths.ConfigFile == "" {
		paths = DefaultPaths("/")
	}
	// read the operating system release information from the /etc/os-release file
	// this file contains the operating system name, version, and other information
	release, err := readOSRelease(rootPath(paths.Root, "/etc/os-release"))
	if err != nil {
		return Discovery{}, err
	}
	architecture := options.ArchitectureOverride
	if architecture == "" {
		architecture = "unknown"
	}
	existingConfig := readEnvironmentFile(paths.ConfigFile)
	existingSpatialConfig := readEnvironmentFile(paths.SpatialConfigFile)
	cameraURL := existingConfig["ATLAS_A8_RTSP_URL"]
	if cameraURL == "" {
		cameraURL = DefaultA8RTSPURL
	}
	groundAddress := existingConfig["ATLAS_GROUND_STATION_ADDR"]
	if groundAddress == "" {
		groundAddress = DefaultGroundAddr
	}
	discovery := Discovery{
		OS:                    release,
		Architecture:          architecture,
		BoardModel:            readBoardModel(rootPath(paths.Root, "/proc/device-tree/model")),
		Serial:                discoverSerial(paths.Root),
		Hailo:                 discoverHailo(ctx, runner, paths),
		Spatial:               discoverSpatial(ctx, runner, paths, existingSpatialConfig),
		Camera:                probeRTSP(ctx, runner, cameraURL),
		GroundReachable:       probeTCP(groundAddress, 800*time.Millisecond),
		ExistingConfig:        existingConfig,
		ExistingSpatialConfig: existingSpatialConfig,
	}
	return discovery, nil
}

func discoverSpatial(ctx context.Context, runner Runner, paths Paths, configuration map[string]string) SpatialStatus {
	status := SpatialStatus{
		Configured:       strings.EqualFold(configuration["ATLAS_SPATIAL_ENABLED"], "true"),
		Provider:         configuration["ATLAS_SPATIAL_PROVIDER"],
		DeviceID:         configuration["ATLAS_SPATIAL_DEVICE_ID"],
		Model:            configuration["ATLAS_SPATIAL_MODEL"],
		USBTransport:     fallback(configuration["ATLAS_SPATIAL_USB_TRANSPORT"], "unknown"),
		RuntimeInstalled: fileExists(paths.SpatialRuntimeBinary),
	}
	if fileExists(paths.SpatialCheck) {
		arguments := []string{"--discover", "--sysfs-root", rootPath(paths.Root, "/sys")}
		if status.DeviceID != "" {
			arguments = append(arguments, "--device-id", status.DeviceID)
		}
		result := runner.Run(ctx, paths.SpatialCheck, arguments...)
		values := parseKeyValueOutput(result.Output)
		status.DevicePresent = strings.EqualFold(values["DEVICE_PRESENT"], "true")
		if status.DevicePresent {
			status.Provider = fallback(values["PROVIDER"], status.Provider)
			status.DeviceID = fallback(values["DEVICE_ID"], status.DeviceID)
			status.Model = fallback(values["MODEL"], status.Model)
			status.USBTransport = fallback(values["USB_TRANSPORT"], status.USBTransport)
			status.USBSpeedMbps, _ = strconv.Atoi(values["USB_SPEED_MBPS"])
		}
	}
	return status
}

func rootPath(root, path string) string {
	if root == "" || root == "/" {
		return path
	}
	return filepath.Join(root, strings.TrimPrefix(path, "/"))
}

// readOSRelease reads the operating system release information from the /etc/os-release file
// this file contains the operating system name, version, and other information in key=value pairs
func readOSRelease(path string) (OSRelease, error) {
	file, err := os.Open(path)
	if err != nil {
		return OSRelease{}, fmt.Errorf("read operating-system release: %w", err)
	}
	defer file.Close()
	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		// split the line into key and value using the = character since os-release file is a key=value pair file
		key, value, found := strings.Cut(scanner.Text(), "=")
		if !found {
			continue
		}
		// trim any whitespace and quotes from the value and store it in the values map
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

// discoverSerial finds all possible serial devices for the PX4 flight controller
// it used file system based detection to find the serial devices
func discoverSerial(root string) []SerialCandidate {
	// possible serail device locations
	// these cover common linux serial device names
	patterns := []string{
		"/dev/serial/by-id/*",
		"/dev/serial/by-path/*",
		"/dev/ttyUSB*",
		"/dev/ttyACM*",
		"/dev/serial0",
	}
	seenResolved := map[string]bool{}
	var candidates []SerialCandidate
	// look for device paths matching the patterns
	// -> resolve symbolic links
	// -> remove duplicates
	// -> prefer persistent devices (by-id or by-path) over non-persistent devices (ttyUSB, ttyACM, serial0)
	for _, pattern := range patterns {
		matches, _ := filepath.Glob(rootPath(root, pattern))
		sort.Strings(matches)
		for _, match := range matches {
			// resolve symbolic links to get the actual device path
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

// discoverHailo determines whether the complete Hailo perception stack is installed and ready to use
func discoverHailo(ctx context.Context, runner Runner, paths Paths) HailoStatus {
	// intial status
	status := HailoStatus{RuntimeMode: AdapterModeProcess, Accelerator: "unknown", VersionsCompatible: true}
	// checks PCI visibility using lspci command
	if _, err := runner.LookPath("lspci"); err == nil {
		//lspci is a command line tool to list all PCI devices
		// -Dnn is the format to display device information in a human-readable format
		result := runner.Run(ctx, "lspci", "-Dnn")
		lower := strings.ToLower(result.Output)
		// examine output for hailo text or hailo PCI vendor ID, if found, set PCIVisible to true
		status.PCIVisible = result.Err == nil && (strings.Contains(lower, "hailo") || strings.Contains(lower, "[1e60:"))
	}
	// checks if the hailo0 device node is ready using the test command
	// test is a command line tool to test file attributes
	// -c checks if the file exists and is a character device
	// A character device is how userspace software communicates with the kernel driver
	// so the following answers the question Has the driver exposed the Hailo device to userspace?
	status.DeviceNodeReady = commandSucceeds(ctx, runner, "test", "-c", rootPath(paths.Root, "/dev/hailo0"))

	// read the Hailo container environment file to check if the Hailo container is configured
	containerConfig := readEnvironmentFile(paths.HailoContainerEnv)
	if containerConfig["ATLAS_HAILO_CONTAINER_IMAGE"] != "" {
		// if hailo container is configured, use the discoverHailoContainer function to check the status
		// and resturn the container status
		return discoverHailoContainer(ctx, runner, status, containerConfig)
	}

	// if we are not using the hailo container, we are in process mode
	// note this is the legacy path, when we first set this up we ran hailo libraries and tools directly on the host
	// this required a lot of additonal work and complexity since hailo rt is more optimised for raspberry pi os, and not ubuntu
	// we still kept this path for backwards compatibility, but newer versions of the agent do not use this path

	// checks if the hailortcli command is available using the lookpath command
	// hailortcli is a command line tool to interact with the HailoRT device
	// fw-control is a subcommand to identify the HailoRT device
	// identify is a subcommand to identify the HailoRT device
	// if the hailortcli command is available, set the RuntimeInstalled to true
	// and run the hailortcli fw-control identify command to get the identify output
	// the identify output contains the device information, including the device name, device type, and device status
	// if the command succeeds, set the DeviceReady to true
	// and parse the accelerator from the identify output using the parseHailoAccelerator function
	// the accelerator is the type of the HailoRT device, such as hailo-8l or hailo-8
	// if the command fails, set the DeviceReady to false
	// and set the Accelerator to unknown
	// if the command succeeds, set the GStreamerReady to true if the gst-inspect-1.0 hailonet and gst-inspect-1.0 hailofilter commands are available
	// and set the PythonReady to true if the python3 command is available and the python bindings are installed
	// if the PCIVisible is false, add the Hailo PCIe device to the MissingComponents list
	// if the RuntimeInstalled is false, add the hailortcli to the MissingComponents list
	// if the DeviceReady is false, add the working HailoRT device to the MissingComponents list
	// if the GStreamerReady is false, add the hailonet/hailofilter to the MissingComponents list
	// if the PythonReady is false, add the Hailo/OpenCV/NumPy Python bindings to the MissingComponents list
	// return the status
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

// discoverHailoContainer checks the status of the Hailo container
// hailo in container mode keeps hailo userspace deps inside a pinned Docker image, while kernel driver remains on host
// answers the question: Is the host driver, configured Docker image, container userspace, Hailo device, and their versions a compatible working stack?
func discoverHailoContainer(ctx context.Context, runner Runner, status HailoStatus, config map[string]string) HailoStatus {
	// set the runtime mode to container mode
	// and load the expected, pinned versions from our config
	status.RuntimeMode = AdapterModeContainer
	status.ContainerImage = config["ATLAS_HAILO_CONTAINER_IMAGE"] // exact docker image to use
	status.ContainerName = fallback(config["ATLAS_HAILO_CONTAINER_NAME"], "atlas-hailo-adapter")
	status.ExpectedDriverVersion = config["ATLAS_HAILO_DRIVER_VERSION"]                // expected version of the loaded hailo_pci kernel module
	status.ExpectedDriverPackageVersion = config["ATLAS_HAILO_DRIVER_PACKAGE_VERSION"] // expected version of the hailo-dkms package
	status.ExpectedFirmwareVersion = config["ATLAS_HAILO_FIRMWARE_PACKAGE_VERSION"]    // expected version of the hailo-fw package
	status.ExpectedDeviceFirmwareVersion = config["ATLAS_HAILO_FIRMWARE_VERSION"]      // expcted firmware version of the Hailo device
	status.ExpectedUserspaceVersion = config["ATLAS_HAILORT_PACKAGE_VERSION"]          // expected HailoRT userspace version inside the container.
	status.ExpectedTAPPASVersion = config["ATLAS_HAILO_TAPPAS_PACKAGE_VERSION"]        // expected TAPPAS version of the Hailo device, Tappas provided the hailo GStreamer components used by
	// the perception pipeline

	// read the loaded host driver version
	// linux exposes info about loaded kernel modules under /sys/module
	if result := runner.Run(ctx, "cat", "/sys/module/hailo_pci/version"); result.Err == nil {
		status.HostDriverVersion = strings.TrimSpace(result.Output)
	}
	if status.HostDriverVersion == "" { // if the host driver version is not found, use modinfo to get the version
		// modinfo is a cli tool to read metadata from installled kernel modules
		if result := runner.Run(ctx, "modinfo", "-F", "version", "hailo_pci"); result.Err == nil {
			status.HostDriverVersion = strings.TrimSpace(result.Output)
		}
	}
	// Note /sys/module/hailo_pci/version → version of the currently loaded module
	// modinfo hailo_pci → version of the installed module available on disk
	// The loaded-module value is preferred because it describes what the running kernel is actually using.

	// read the installed driver package version
	if result := runner.Run(ctx, "dpkg-query", "-W", "-f=${Version}", "hailo-dkms"); result.Err == nil {
		status.HostDriverPackageVersion = strings.TrimSpace(result.Output)
	}
	// read the installed firmware package version
	if result := runner.Run(ctx, "dpkg-query", "-W", "-f=${Version}", "hailofw"); result.Err == nil {
		status.HostFirmwareVersion = strings.TrimSpace(result.Output)
	}

	// check if docker is installed and running
	if _, err := runner.LookPath("docker"); err == nil {
		image := runner.Run(ctx, "docker", "image", "inspect", status.ContainerImage)
		status.RuntimeInstalled = image.Err == nil
	}
	if status.RuntimeInstalled {
		// run hailo container health check
		result := runHailoContainerCheck(ctx, runner, status, "")
		values := parseKeyValueOutput(result.Output)
		status.ContainerReady = result.Err == nil             // mark container as ready if health check result does not contain errors
		status.DeviceReady = values["DEVICE_READY"] == "true" // mark device ready if health check conforms that hailort inside container can access the accelerator
		// we mark device node ready if host can see the /dev/hailo0 device and the container can also see the /dev/hailo0 device
		status.DeviceNodeReady = status.DeviceNodeReady && values["DEVICE_NODE_READY"] == "true"
		status.GStreamerReady = values["GSTREAMER_READY"] == "true" // mark gstreamer ready if health check conforms that hailonet and hailofilter plugins are installed and working
		status.PythonReady = values["PYTHON_READY"] == "true"       // mark python ready if health check conforms that the Hailo/OpenCV/NumPy Python bindings are installed and working
		status.Accelerator = fallback(values["DEVICE_ARCHITECTURE"], "unknown")
		status.FirmwareVersion = values["FIRMWARE_VERSION"]
		status.UserspaceVersion = values["HAILORT_VERSION"]
		status.TAPPASVersion = values["TAPPAS_VERSION"]
		status.IdentifyOutput = result.Output
	}

	// confirm that driver, hailo-dkms, hailo-fw, container hailort, container tappas, and actual device firmware versions are compatible with the expected versions
	status.VersionsCompatible =
		versionMatches(status.HostDriverVersion, status.ExpectedDriverVersion) &&
			versionMatches(status.HostDriverPackageVersion, status.ExpectedDriverPackageVersion) &&
			versionMatches(status.HostFirmwareVersion, status.ExpectedFirmwareVersion) &&
			versionMatches(status.UserspaceVersion, status.ExpectedUserspaceVersion) &&
			versionMatches(status.TAPPASVersion, status.ExpectedTAPPASVersion) &&
			versionCoreMatches(status.FirmwareVersion, status.ExpectedDeviceFirmwareVersion)

	// build missing components list
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

// runHailoContainerCheck runs the Hailo container health check script
// it checks if the configured container is running and if it is, it runs the container check script
// if the container is not running, it runs the container check script in a new container
// the container check script is a simple bash script that checks if the Hailo device is accessible and if the GStreamer plugins are installed and working
// it also checks if the Python bindings are installed and working
// it returns the command result
func runHailoContainerCheck(ctx context.Context, runner Runner, status HailoStatus, modelPath string) CommandResult {
	// path to the container check script, which should be inside the container image
	checkPath := "/usr/local/bin/atlas-hailo-container-check"
	arguments := []string{}
	if modelPath != "" {
		arguments = append(arguments, "--model", modelPath)
	}
	// check if configured container is running
	running := runner.Run(ctx, "docker", "inspect", "--format", "{{.State.Running}}", status.ContainerName)
	if running.Err == nil && strings.TrimSpace(running.Output) == "true" {
		return runner.Run(ctx, "docker", append([]string{"exec", status.ContainerName, checkPath}, arguments...)...)
	}
	// if container is running, first prepare args  for the container check script
	dockerArguments := []string{
		"run", "--rm", "--network", "none", "--device", "/dev/hailo0:/dev/hailo0",
		"--env", "ATLAS_HAILO_ACCELERATOR=" + status.Accelerator, "--entrypoint", checkPath,
	}

	// if model path is provided, add the model path to the docker arguments, so that the container check script can access the model file
	if modelPath != "" {
		dockerArguments = append(dockerArguments, "--volume", filepath.Dir(modelPath)+":"+filepath.Dir(modelPath)+":ro")
	}
	dockerArguments = append(dockerArguments, status.ContainerImage)
	dockerArguments = append(dockerArguments, arguments...)

	// run the container check script
	// final command will be something like
	// 	docker run \
	//   --rm \
	//   --network none \
	//   --device /dev/hailo0:/dev/hailo0 \
	//   --env ATLAS_HAILO_ACCELERATOR=hailo-8l \
	//   --entrypoint /usr/local/bin/atlas-hailo-container-check \
	//   sha256:configured-image \
	//   --model /usr/share/atlas-agent/models/objects.hef
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

// installConfigFromDiscovery converts our discovery object into a validated installation Config
// chooses the first discovered serial device;
// uses the detected Hailo accelerator;
// switches to container mode when container Hailo was discovered;
// initially enables perception if Hailo and its files are ready;
// restores values from an existing installation;
// carries forward the existing or newly discovered Spatial state.
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
	applySpatialDiscovery(&config, discovery)
	if discovery.Hailo.RuntimeMode == AdapterModeContainer {
		config.PerceptionAdapterMode = AdapterModeContainer
	}
	return config
}

func applySpatialDiscovery(config *InstallConfig, discovery Discovery) {
	status := discovery.Spatial
	config.SpatialProvider = status.Provider
	config.SpatialDeviceID = status.DeviceID
	config.SpatialModel = status.Model
	config.SpatialUSBTransport = fallback(status.USBTransport, "unknown")
	if value, exists := discovery.ExistingSpatialConfig["ATLAS_SPATIAL_ENABLED"]; exists {
		config.SpatialEnabled = strings.EqualFold(value, "true")
	} else {
		config.SpatialEnabled = status.DevicePresent
	}
	if !status.DevicePresent {
		if value := discovery.ExistingSpatialConfig["ATLAS_SPATIAL_PROVIDER"]; value != "" {
			config.SpatialProvider = value
		}
		if value := discovery.ExistingSpatialConfig["ATLAS_SPATIAL_DEVICE_ID"]; value != "" {
			config.SpatialDeviceID = value
		}
		if value := discovery.ExistingSpatialConfig["ATLAS_SPATIAL_MODEL"]; value != "" {
			config.SpatialModel = value
		}
		if value := discovery.ExistingSpatialConfig["ATLAS_SPATIAL_USB_TRANSPORT"]; value != "" {
			config.SpatialUSBTransport = value
		}
	}
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
