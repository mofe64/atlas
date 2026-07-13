package onboardsetup

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

type CheckLevel string

const (
	CheckPass CheckLevel = "PASS"
	CheckWarn CheckLevel = "WARN"
	CheckFail CheckLevel = "FAIL"
)

type Check struct {
	Name    string
	Level   CheckLevel
	Message string
}

func Doctor(ctx context.Context, runner Runner, paths Paths, output io.Writer) ([]Check, error) {
	configuration := readEnvironmentFile(paths.ConfigFile)
	if len(configuration) == 0 {
		return nil, fmt.Errorf("Atlas is not configured; run sudo atlas-setup first")
	}
	checks := []Check{
		fileCheck("configuration", paths.ConfigFile, true),
		fileCheck("atlas-agent binary", paths.AgentBinary, true),
		fileCheck("mavsdk_server binary", paths.MAVSDKBinary, true),
		serviceCheck(ctx, runner, "atlas-mavsdk.service"),
		serviceCheck(ctx, runner, "atlas-agent.service"),
	}
	serialPath := configuration["ATLAS_FLIGHT_CONTROLLER_ENDPOINT"]
	checks = append(checks, fileCheck("TELEM2 serial device", serialPath, true))
	checks = append(checks, tcpCheck("MAVSDK gRPC", configuration["ATLAS_MAVSDK_GRPC_ADDR"], true))
	checks = append(checks, tcpCheck("Atlas Native", configuration["ATLAS_GROUND_STATION_ADDR"], false))

	camera := probeRTSP(ctx, runner, configuration["ATLAS_A8_RTSP_URL"])
	if camera.Reachable {
		checks = append(checks, Check{Name: "A8 RTSP", Level: CheckPass, Message: fmt.Sprintf("%s %sx%s", camera.Codec, camera.Width, camera.Height)})
	} else {
		checks = append(checks, Check{Name: "A8 RTSP", Level: CheckWarn, Message: fallback(camera.Error, "not reachable")})
	}

	if configuration["ATLAS_PERCEPTION_PROVIDER"] == "hailo" {
		hailo := discoverHailo(ctx, runner)
		if hailo.Ready() {
			checks = append(checks, Check{Name: "Hailo runtime", Level: CheckPass, Message: hailo.Accelerator})
		} else {
			checks = append(checks, Check{Name: "Hailo runtime", Level: CheckFail, Message: strings.Join(hailo.MissingComponents, ", ")})
		}
		checks = append(checks,
			fileCheck("Hailo HEF model", configuration["ATLAS_PERCEPTION_MODEL_PATH"], true),
			fileCheck("Hailo postprocess", configuration["ATLAS_PERCEPTION_POSTPROCESS_SO"], true),
			fileCheck("Hailo adapter", configuration["ATLAS_PERCEPTION_ADAPTER_PATH"], true),
		)
	}

	for _, check := range checks {
		_, _ = fmt.Fprintf(output, "[%s] %-24s %s\n", check.Level, check.Name, check.Message)
	}
	return checks, nil
}

func fileCheck(name, path string, required bool) Check {
	if path == "" {
		level := CheckWarn
		if required {
			level = CheckFail
		}
		return Check{Name: name, Level: level, Message: "not configured"}
	}
	info, err := os.Stat(path)
	if err == nil && !info.IsDir() {
		return Check{Name: name, Level: CheckPass, Message: path}
	}
	level := CheckWarn
	if required {
		level = CheckFail
	}
	return Check{Name: name, Level: level, Message: path + " is missing"}
}

func serviceCheck(ctx context.Context, runner Runner, service string) Check {
	result := runner.Run(ctx, "systemctl", "is-active", service)
	if result.Err == nil && result.Output == "active" {
		return Check{Name: service, Level: CheckPass, Message: "active"}
	}
	return Check{Name: service, Level: CheckFail, Message: fallback(result.Output, "inactive")}
}

func tcpCheck(name, address string, required bool) Check {
	if address == "" {
		level := CheckWarn
		if required {
			level = CheckFail
		}
		return Check{Name: name, Level: level, Message: "not configured"}
	}
	connection, err := net.DialTimeout("tcp", address, time.Second)
	if err == nil {
		_ = connection.Close()
		return Check{Name: name, Level: CheckPass, Message: address}
	}
	level := CheckWarn
	if required {
		level = CheckFail
	}
	return Check{Name: name, Level: level, Message: err.Error()}
}

func HasFailures(checks []Check) bool {
	for _, check := range checks {
		if check.Level == CheckFail {
			return true
		}
	}
	return false
}

func parseUint32(values map[string]string, key string, fallback uint32) uint32 {
	value, err := strconv.ParseUint(values[key], 10, 32)
	if err != nil {
		return fallback
	}
	return uint32(value)
}
