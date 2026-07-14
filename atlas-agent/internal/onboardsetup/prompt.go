package onboardsetup

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
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

func BuildInstallPlan(ctx context.Context, runner Runner, discovery Discovery, options Options) (InstallPlan, error) {
	config := installConfigFromDiscovery(discovery, options.Paths)
	plan := InstallPlan{Config: config}
	if options.NonInteractive {
		if err := ensureSerialCandidate(config, discovery); err != nil {
			return InstallPlan{}, err
		}
		if config.PerceptionEnabled && !discovery.Hailo.Ready() {
			return InstallPlan{}, fmt.Errorf("Hailo perception is configured but the runtime is not ready: %s", strings.Join(discovery.Hailo.MissingComponents, ", "))
		}
		if config.PerceptionEnabled {
			if err := validateModelAccelerator(options.PackagedModelAccelerator, discovery.Hailo); err != nil {
				return InstallPlan{}, err
			}
		}
		return plan, config.Validate(options.Paths)
	}

	prompt := NewPrompter(options.Input, options.Output)
	printDiscovery(options.Output, discovery)
	selected, err := prompt.selectSerial(discovery.Serial, config.SerialDevice)
	if err != nil {
		return InstallPlan{}, err
	}
	config.SerialDevice = selected
	config.DroneName, err = prompt.text("Drone name", config.DroneName)
	if err != nil {
		return InstallPlan{}, err
	}
	config.GroundStationAddress, err = prompt.text("Atlas Native address", config.GroundStationAddress)
	if err != nil {
		return InstallPlan{}, err
	}
	baudText, err := prompt.text("TELEM2 baud rate", strconv.FormatUint(uint64(config.BaudRate), 10))
	if err != nil {
		return InstallPlan{}, err
	}
	baud, err := strconv.ParseUint(baudText, 10, 32)
	if err != nil || baud == 0 {
		return InstallPlan{}, fmt.Errorf("invalid baud rate %q", baudText)
	}
	config.BaudRate = uint32(baud)

	serviceStatus := runner.Run(ctx, "systemctl", "is-active", "atlas-mavsdk.service")
	if serviceStatus.Err == nil && strings.TrimSpace(serviceStatus.Output) == "active" {
		_, _ = fmt.Fprintln(options.Output, "\nMAVSDK is active; preserving the live serial owner and skipping the passive heartbeat probe.")
	} else {
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

	modelCompatibilityErr := validateModelAccelerator(options.PackagedModelAccelerator, discovery.Hailo)
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
	} else if discovery.Hailo.Ready() {
		config.PerceptionEnabled, err = prompt.confirm("Enable Hailo object detection", true)
		if err != nil {
			return InstallPlan{}, err
		}
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
	if config.PerceptionEnabled {
		config.ModelPath, err = prompt.text("Hailo HEF model", config.ModelPath)
		if err != nil {
			return InstallPlan{}, err
		}
		if config.PerceptionAdapterMode == AdapterModeProcess {
			config.PostprocessSO, err = prompt.text("Hailo postprocess library", config.PostprocessSO)
			if err != nil {
				return InstallPlan{}, err
			}
		}
	}
	plan.Config = config
	if config.PerceptionEnabled && !fileExists(config.ModelPath) {
		return InstallPlan{}, fmt.Errorf("Hailo HEF model does not exist: %s", config.ModelPath)
	}
	if config.PerceptionEnabled && config.PerceptionAdapterMode == AdapterModeProcess && !fileExists(config.PostprocessSO) {
		return InstallPlan{}, fmt.Errorf("Hailo postprocess library does not exist: %s", config.PostprocessSO)
	}
	if err := config.Validate(options.Paths); err != nil {
		return InstallPlan{}, err
	}
	printPlan(options.Output, plan, options.Paths)
	confirmed, err := prompt.confirm("Apply this Atlas configuration", true)
	if err != nil {
		return InstallPlan{}, err
	}
	if !confirmed {
		return InstallPlan{}, fmt.Errorf("installation cancelled")
	}
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
	_, _ = fmt.Fprintf(output, "  Drone:       %s\n", plan.Config.DroneName)
	_, _ = fmt.Fprintf(output, "  Native:      %s\n", plan.Config.GroundStationAddress)
	_, _ = fmt.Fprintf(output, "  TELEM2:      %s at %d baud\n", plan.Config.SerialDevice, plan.Config.BaudRate)
	_, _ = fmt.Fprintf(output, "  Perception:  %s (%s)\n", provider, plan.Config.PerceptionAdapterMode)
	_, _ = fmt.Fprintf(output, "  Config:      %s\n", paths.ConfigFile)
	_, _ = fmt.Fprintf(output, "  Services:    %s\n", strings.Join(configuredServices(plan.Config), ", "))
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
