package localinputs

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Enabled                  bool
	DroneID                  string
	SourceID                 string
	TelemetryEndpoint        string
	TelemetryPublishInterval time.Duration
	VideoRTSPURL             string
}

func LoadConfigFromEnv() Config {
	return Config{
		Enabled:                  envBool("ATLAS_LOCAL_INPUTS_ENABLED"),
		DroneID:                  envOrDefault("ATLAS_LOCAL_INPUT_DRONE_ID", os.Getenv("ATLAS_DRONE_ID")),
		SourceID:                 envOrDefault("ATLAS_LOCAL_INPUT_SOURCE_ID", "hm30-local"),
		TelemetryEndpoint:        strings.TrimSpace(os.Getenv("ATLAS_LOCAL_TELEMETRY_ENDPOINT")),
		TelemetryPublishInterval: envDuration("ATLAS_LOCAL_TELEMETRY_PUBLISH_INTERVAL", time.Second),
		VideoRTSPURL:             strings.TrimSpace(os.Getenv("ATLAS_LOCAL_VIDEO_RTSP_URL")),
	}
}

func envOrDefault(key string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func envBool(key string) bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	switch value {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
		return parsed
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return fallback
}
