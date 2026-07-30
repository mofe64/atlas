package onboardsetup

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/aircraftprofile"
)

type InstallPlan struct {
	Config InstallConfig
}

type Prompter struct {
	reader *bufio.Reader
	output io.Writer
}

func NewPrompter(input io.Reader, output io.Writer) *Prompter {
	return &Prompter{reader: bufio.NewReader(input), output: output}
}

// converts discovery object (result of hardware, software and environment discovery) into a validated installation Config
func BuildInstallPlan(ctx context.Context, runner Runner, discovery Discovery, options Options) (InstallPlan, error) {
	// load aircraft profiles from the filesystem
	// aircraft profile describes the expected hardware configuration of an aircraft, including its depth camera identity and other payloads
	profiles, err := aircraftprofile.LoadDirectory(options.Paths.AircraftProfilesDir)
	if err != nil {
		return InstallPlan{}, err
	}
	// create initial config from our discovery object
	// layers the following sources -> built in defaults -> newly discovered hardware -> existign atlas config -> existing/discovered spatial config
	config := installConfigFromDiscovery(discovery, options.Paths)

	// determine the aircraft profile to use
	// first check if the user provided a profile ID on the command line
	requestedProfileID := strings.TrimSpace(options.AircraftProfileID)
	// if no profile ID was provided, check if one was stored in the existing spatial config
	if requestedProfileID == "" {
		requestedProfileID = strings.TrimSpace(discovery.ExistingSpatialConfig["ATLAS_AIRCRAFT_PROFILE_ID"])
	}

	// non interactive mode, does not prompt the user for input, we derive the required values, or return an error if they are not available
	if options.NonInteractive {
		if requestedProfileID == "" {
			return InstallPlan{}, fmt.Errorf("--aircraft-profile is required for a first non-interactive setup")
		}
		// find the requested profile in the loaded profiles, return an error if not found
		source, findErr := aircraftprofile.Find(profiles, requestedProfileID)
		if findErr != nil {
			return InstallPlan{}, findErr
		}
		// apply the profile to the config, we essentially modify the config to set specific values based on the profile
		// such as aircraft profile ID, spatial device ID, etc.
		// doing it this way allows us to enforce the invariant that the device ID for any connectecd payload muct match what the profile supplies
		// if we don't match, we should return an error, instead of having mismatching device IDs in the config
		if err := applyAircraftProfile(&config, source, discovery); err != nil {
			return InstallPlan{}, err
		}
		// ensure that we have a serial device candidate for our flight controller connection
		if err := ensureSerialCandidate(config, discovery); err != nil {
			return InstallPlan{}, err
		}
		// ensure that we have a Hailo runtime ready, if perception is enabled
		if config.PerceptionEnabled && !discovery.Hailo.Ready() {
			return InstallPlan{}, fmt.Errorf("Hailo perception is configured but the runtime is not ready: %s", strings.Join(discovery.Hailo.MissingComponents, ", "))
		}
		// ensure that the packaged Hailo model is compatible with the detected Hailo accelerator
		if config.PerceptionEnabled {
			if err := validateModelAccelerator(options.PackagedModelAccelerator, discovery.Hailo); err != nil {
				return InstallPlan{}, err
			}
		}
		return InstallPlan{Config: config}, config.Validate(options.Paths)
	}

	// interactive mode, prompts the user for input
	prompt := NewPrompter(options.Input, options.Output)
	// print the discovery info to stdout
	printDiscovery(options.Output, discovery)

	// If exactly one profile is installed, it becomes the default selection. The operator still goes through profile selection, but pressing Enter accepts it.
	if requestedProfileID == "" && len(profiles) == 1 {
		requestedProfileID = profiles[0].Profile.ProfileID
	}

	// select the aircraft profile to use
	selectedProfile, err := prompt.selectAircraftProfile(profiles, requestedProfileID)
	if err != nil {
		return InstallPlan{}, err
	}
	// apply the selected profile to the config
	if err := applyAircraftProfile(&config, selectedProfile, discovery); err != nil {
		return InstallPlan{}, err
	}
	// select the serial device to use
	selected, err := prompt.selectSerial(discovery.Serial, config.SerialDevice)
	if err != nil {
		return InstallPlan{}, err
	}
	config.SerialDevice = selected
	// prompt the user for the drone name
	config.DroneName, err = prompt.text("Drone name", config.DroneName)
	if err != nil {
		return InstallPlan{}, err
	}
	// prompt the user for the Atlas Native address
	config.GroundStationAddress, err = prompt.text("Atlas Native address", config.GroundStationAddress)
	if err != nil {
		return InstallPlan{}, err
	}
	// prompt the user for the TELEM2 baud rate (the baud rate we use to communicate with the flight controller)
	// our connection to the flight controller is on telem2 port
	baudText, err := prompt.text("TELEM2 baud rate", strconv.FormatUint(uint64(config.BaudRate), 10))
	if err != nil {
		return InstallPlan{}, err
	}
	baud, err := strconv.ParseUint(baudText, 10, 32)
	if err != nil || baud == 0 {
		return InstallPlan{}, fmt.Errorf("invalid baud rate %q", baudText)
	}
	config.BaudRate = uint32(baud)

	// check if the MAVSDK service is active, if it is, we can skip the heartbeat probe
	serviceStatus := runner.Run(ctx, "systemctl", "is-active", "atlas-mavsdk.service")
	if serviceStatus.Err == nil && strings.TrimSpace(serviceStatus.Output) == "active" {
		_, _ = fmt.Fprintln(options.Output, "\nMAVSDK is active; preserving the live serial owner and skipping the passive heartbeat probe.")
	} else {
		// if the MAVSDK service is not active, we need to probe the serial device for a MAVLink heartbeat
		// Atlas listens for up to four seconds for a MAVLink heartbeat.
		// If it fails, setup displays a warning and asks whether to continue:
		_, _ = fmt.Fprint(options.Output, "\nProbing the selected serial device for a MAVLink heartbeat...\n")
		heartbeat, probeErr := ProbeMAVLinkHeartbeat(ctx, runner, config.SerialDevice, config.BaudRate, 4*time.Second)
		if probeErr != nil {
			_, _ = fmt.Fprintf(options.Output, "  warning: %v\n", probeErr)
			proceed, confirmErr := prompt.confirm("Continue with this serial device", false)
			if confirmErr != nil {
				return InstallPlan{}, confirmErr
			}
			if !proceed {
				return InstallPlan{}, fmt.Errorf("installation cancelled because the flight-controller link was not verified")
			}
		} else {
			config.MAVLinkSystemID = uint32(heartbeat.SystemID)
			config.MAVLinkComponentID = uint32(heartbeat.ComponentID)
			_, _ = fmt.Fprintf(options.Output, "  detected MAVLink heartbeat: system %d, component %d\n", heartbeat.SystemID, heartbeat.ComponentID)
		}
	}
	// compares the accelerator targeted by the packaged HEF model against the detected accelerator.
	modelCompatibilityErr := validateModelAccelerator(options.PackagedModelAccelerator, discovery.Hailo)
	// if the Hailo runtime is ready and the model is not compatible, we
	// explain the incompatibility
	// disable perception
	// ask the user to continue with perception disabled
	// if the user agrees, continue with the installation
	// if the user does not agree, return an error
	if discovery.Hailo.Ready() && modelCompatibilityErr != nil {
		_, _ = fmt.Fprintf(options.Output, "\nHailo perception cannot be enabled: %v\n", modelCompatibilityErr)
		config.PerceptionEnabled = false
		proceed, confirmErr := prompt.confirm("Continue installing Atlas with perception disabled", false)
		if confirmErr != nil {
			return InstallPlan{}, confirmErr
		}
		if !proceed {
			return InstallPlan{}, fmt.Errorf("installation paused until a compatible Hailo model is packaged")
		}
		// if the Hailo runtime is ready and the model is compatible, we prompt the user to enable perception
	} else if discovery.Hailo.Ready() {
		config.PerceptionEnabled, err = prompt.confirm("Enable Hailo object detection", true)
		if err != nil {
			return InstallPlan{}, err
		}
		// if the Hailo runtime is not ready, we
		// print all the missing components
		// disable perception
		// ask user to continue with perception disabled
	} else {
		_, _ = fmt.Fprintf(options.Output, "\nHailo perception is not ready: %s\n", strings.Join(discovery.Hailo.MissingComponents, ", "))
		_, _ = fmt.Fprintln(options.Output, "On a clean Ubuntu 24.04 host, run sudo atlas-hailo-setup to install the pinned host driver and container runtime.")
		config.PerceptionEnabled = false
		proceed, confirmErr := prompt.confirm("Continue installing Atlas with perception disabled", false)
		if confirmErr != nil {
			return InstallPlan{}, confirmErr
		}
		if !proceed {
			return InstallPlan{}, fmt.Errorf("installation paused until the Hailo runtime is available")
		}
	}
	// if perception is enabled, we prompt the user for the Hailo HEF model
	if config.PerceptionEnabled {
		// prompt the user for the Hailo HEF model,
		// if our config already has a model path, we use that as the default, but allow the user to change it
		config.ModelPath, err = prompt.text("Hailo HEF model", config.ModelPath)
		if err != nil {
			return InstallPlan{}, err
		}
		// if we are using process adapater mode, user can also specify the postprocess library to use
		// for container mode, the container image already contains the postprocess library
		if config.PerceptionAdapterMode == AdapterModeProcess {
			config.PostprocessSO, err = prompt.text("Hailo postprocess library", config.PostprocessSO)
			if err != nil {
				return InstallPlan{}, err
			}
		}
	}

	// if a spatial camera is detected, we prompt the user to enable it
	if discovery.Spatial.DevicePresent {
		// if the spatial camera is connected over USB 2, we print a warning
		// USB 2 is slower than USB 3, and may limit the frame rate of the depth camera
		if discovery.Spatial.USBTransport == "usb2" {
			_, _ = fmt.Fprintf(options.Output, "\nSpatial camera %s is connected over USB 2; RGB-D frame rate may be limited.\n", fallback(discovery.Spatial.Model, discovery.Spatial.DeviceID))
			// if  usb transport is usb2-or-unbooted, this means that the spatial camera is not yet booted, so we print a warning
		} else if discovery.Spatial.USBTransport == "usb2-or-unbooted" {
			_, _ = fmt.Fprintln(options.Output, "\nSpatial camera USB 3 transport will be verified after its firmware starts.")
		}
		// prompt the user to enable the spatial camera
		config.SpatialEnabled, err = prompt.confirm("Enable the front spatial camera", config.SpatialEnabled)
		if err != nil {
			return InstallPlan{}, err
		}
		// this happens where sptial is configured, but is currently not visisble, eg when camera is unplugged
		//  in this case, we do not disable spatial camera, we ask to user if they want to keep it enabled, so that spatial service can retry connection later.
	} else if config.SpatialEnabled {
		_, _ = fmt.Fprintln(options.Output, "\nThe configured spatial camera is not currently visible. Its service can still be installed and will retry independently.")
		config.SpatialEnabled, err = prompt.confirm("Keep the front spatial camera enabled", true)
		if err != nil {
			return InstallPlan{}, err
		}
		// no spatial device detected, and no previous spatial config, if this case we leave the spatial service disabled
	} else {
		_, _ = fmt.Fprintln(options.Output, "\nSpatial camera: no supported USB device detected; leaving the optional runtime disabled.")
	}

	// construct the install plan
	plan := InstallPlan{Config: config}

	// verify perception files exist (the model we are using for object detection)
	if config.PerceptionEnabled && !fileExists(config.ModelPath) {
		return InstallPlan{}, fmt.Errorf("Hailo HEF model does not exist: %s", config.ModelPath)
	}
	// if process adapter mode is enabled, we also need to verify that the postprocess library exists
	if config.PerceptionEnabled && config.PerceptionAdapterMode == AdapterModeProcess && !fileExists(config.PostprocessSO) {
		return InstallPlan{}, fmt.Errorf("Hailo postprocess library does not exist: %s", config.PostprocessSO)
	}
	// validate the config, this ensures that the config is consistent and complete
	// it checks that all required values are present, and that the values are valid
	if err := config.Validate(options.Paths); err != nil {
		return InstallPlan{}, err
	}
	// print the install plan to stdout
	printPlan(options.Output, plan, options.Paths)
	// prompt the user to confirm the install plan
	confirmed, err := prompt.confirm("Apply this Atlas configuration", true)
	if err != nil {
		return InstallPlan{}, err
	}
	// if the user does not confirm, we return an error
	if !confirmed {
		return InstallPlan{}, fmt.Errorf("installation cancelled")
	}
	// if the user confirms, we return the install plan
	return plan, nil
}

func printDiscovery(output io.Writer, discovery Discovery) {
	_, _ = fmt.Fprintln(output, "Atlas onboard setup")
	_, _ = fmt.Fprintf(output, "  OS:       %s\n", discovery.OS.PrettyName)
	_, _ = fmt.Fprintf(output, "  Board:    %s\n", discovery.BoardModel)
	_, _ = fmt.Fprintf(output, "  Arch:     %s\n", discovery.Architecture)
	if discovery.Camera.Reachable {
		_, _ = fmt.Fprintf(output, "  A8 RTSP:  reachable (%s %sx%s)\n", discovery.Camera.Codec, discovery.Camera.Width, discovery.Camera.Height)
	} else {
		_, _ = fmt.Fprintf(output, "  A8 RTSP:  not verified (%s)\n", fallback(discovery.Camera.Error, "connection failed"))
	}
	if discovery.Hailo.Ready() {
		_, _ = fmt.Fprintf(output, "  Hailo:    ready (%s, %s)\n", discovery.Hailo.Accelerator, discovery.Hailo.RuntimeMode)
	} else {
		_, _ = fmt.Fprintf(output, "  Hailo:    incomplete (%s)\n", strings.Join(discovery.Hailo.MissingComponents, ", "))
	}
	if discovery.Spatial.DevicePresent {
		_, _ = fmt.Fprintf(output, "  Spatial: ready to configure (%s, %s, %s)\n", fallback(discovery.Spatial.Model, "depth camera"), discovery.Spatial.Provider, discovery.Spatial.USBTransport)
	} else if discovery.Spatial.Configured {
		_, _ = fmt.Fprintf(output, "  Spatial: configured but device not visible (%s)\n", fallback(discovery.Spatial.Provider, "unknown provider"))
	} else {
		_, _ = fmt.Fprintln(output, "  Spatial: no supported USB camera detected")
	}
	if discovery.GroundReachable {
		_, _ = fmt.Fprintln(output, "  Native:   reachable")
	} else {
		_, _ = fmt.Fprintln(output, "  Native:   not currently reachable (non-fatal)")
	}
}

func printPlan(output io.Writer, plan InstallPlan, paths Paths) {
	provider := "disabled"
	if plan.Config.PerceptionEnabled {
		provider = "hailo"
	}
	_, _ = fmt.Fprintln(output, "\nInstallation plan")
	_, _ = fmt.Fprintf(output, "  Profile:     %s\n", plan.Config.AircraftProfileID)
	_, _ = fmt.Fprintf(output, "  Drone:       %s\n", plan.Config.DroneName)
	_, _ = fmt.Fprintf(output, "  Native:      %s\n", plan.Config.GroundStationAddress)
	_, _ = fmt.Fprintf(output, "  TELEM2:      %s at %d baud\n", plan.Config.SerialDevice, plan.Config.BaudRate)
	_, _ = fmt.Fprintf(output, "  Perception:  %s (%s)\n", provider, plan.Config.PerceptionAdapterMode)
	spatial := "disabled"
	if plan.Config.SpatialEnabled {
		spatial = plan.Config.SpatialProvider
	}
	_, _ = fmt.Fprintf(output, "  Spatial:     %s\n", spatial)
	_, _ = fmt.Fprintf(output, "  Config:      %s\n", paths.ConfigFile)
	_, _ = fmt.Fprintf(output, "  Services:    %s\n", strings.Join(configuredServices(plan.Config), ", "))
}

func (prompt *Prompter) selectAircraftProfile(sources []aircraftprofile.Source, defaultID string) (aircraftprofile.Source, error) {
	_, _ = fmt.Fprintln(prompt.output, "\nInstalled aircraft profiles:")
	defaultIndex := 0
	for index, source := range sources {
		marker := " "
		if source.Profile.ProfileID == defaultID {
			defaultIndex, marker = index, "*"
		}
		_, _ = fmt.Fprintf(
			prompt.output,
			"  %d. %s %s\n",
			index+1,
			marker,
			source.Profile.ProfileID,
		)
	}
	for {
		value, err := prompt.text("Select the aircraft profile", strconv.Itoa(defaultIndex+1))
		if err != nil {
			return aircraftprofile.Source{}, err
		}
		selection, parseErr := strconv.Atoi(value)
		if parseErr == nil && selection >= 1 && selection <= len(sources) {
			return sources[selection-1], nil
		}
		if source, findErr := aircraftprofile.Find(sources, value); findErr == nil {
			return source, nil
		}
		_, _ = fmt.Fprintln(prompt.output, "Choose one of the listed profile numbers or ids.")
	}
}

func applyAircraftProfile(config *InstallConfig, source aircraftprofile.Source, discovery Discovery) error {
	if discovery.Spatial.DevicePresent &&
		discovery.Spatial.Provider == SpatialProviderDepthAI &&
		discovery.Spatial.DeviceID != "" &&
		discovery.Spatial.DeviceID != source.Profile.Payloads.DepthCamera.DeviceID {
		return fmt.Errorf(
			"connected Spatial device %s does not match aircraft profile %s device %s",
			discovery.Spatial.DeviceID,
			source.Profile.ProfileID,
			source.Profile.Payloads.DepthCamera.DeviceID,
		)
	}
	config.AircraftProfileID = source.Profile.ProfileID
	config.AircraftProfileSourcePath = source.Path
	config.AircraftProfile = source.Profile
	if config.SpatialProvider == SpatialProviderDepthAI || config.SpatialProvider == "" {
		config.SpatialDeviceID = source.Profile.Payloads.DepthCamera.DeviceID
	}
	return nil
}

func (prompt *Prompter) text(label, defaultValue string) (string, error) {
	_, _ = fmt.Fprintf(prompt.output, "%s [%s]: ", label, defaultValue)
	line, err := prompt.reader.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	value := strings.TrimSpace(line)
	if value == "" {
		return defaultValue, nil
	}
	return value, nil
}

func (prompt *Prompter) confirm(label string, defaultYes bool) (bool, error) {
	suffix := "[Y/n]"
	if !defaultYes {
		suffix = "[y/N]"
	}
	for {
		_, _ = fmt.Fprintf(prompt.output, "%s %s: ", label, suffix)
		line, err := prompt.reader.ReadString('\n')
		if err != nil && line == "" {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "":
			return defaultYes, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			_, _ = fmt.Fprintln(prompt.output, "Please answer yes or no.")
		}
	}
}

func (prompt *Prompter) selectSerial(candidates []SerialCandidate, defaultPath string) (string, error) {
	if len(candidates) == 0 {
		return prompt.text("TELEM2 serial device", defaultPath)
	}
	_, _ = fmt.Fprintln(prompt.output, "\nDetected serial devices:")
	defaultIndex := 0
	for index, candidate := range candidates {
		marker := " "
		if candidate.Path == defaultPath {
			defaultIndex, marker = index, "*"
		}
		_, _ = fmt.Fprintf(prompt.output, "  %d. %s %s\n", index+1, marker, candidate.Label())
	}
	for {
		value, err := prompt.text("Select the TELEM2 adapter", strconv.Itoa(defaultIndex+1))
		if err != nil {
			return "", err
		}
		selection, parseErr := strconv.Atoi(value)
		if parseErr == nil && selection >= 1 && selection <= len(candidates) {
			return candidates[selection-1].Path, nil
		}
		_, _ = fmt.Fprintln(prompt.output, "Choose one of the listed device numbers.")
	}
}

func fallback(value, fallbackValue string) string {
	if strings.TrimSpace(value) == "" {
		return fallbackValue
	}
	return value
}
