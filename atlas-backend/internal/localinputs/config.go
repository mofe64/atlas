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
	VideoRTSPTransport       string
	VideoRTPBufferSize       int
	VideoWebRTCICENATIPs     []string
	VideoWebRTCUDPPortMin    uint16
	VideoWebRTCUDPPortMax    uint16
}

func LoadConfigFromEnv() Config {
	return Config{
		Enabled:                  envBool("ATLAS_LOCAL_INPUTS_ENABLED"),
		DroneID:                  envOrDefault("ATLAS_LOCAL_INPUT_DRONE_ID", os.Getenv("ATLAS_DRONE_ID")),
		SourceID:                 envOrDefault("ATLAS_LOCAL_INPUT_SOURCE_ID", "hm30-local"),
		TelemetryEndpoint:        strings.TrimSpace(os.Getenv("ATLAS_LOCAL_TELEMETRY_ENDPOINT")),
		TelemetryPublishInterval: envDuration("ATLAS_LOCAL_TELEMETRY_PUBLISH_INTERVAL", time.Second),
		VideoRTSPURL:             strings.TrimSpace(os.Getenv("ATLAS_LOCAL_VIDEO_RTSP_URL")),
		VideoRTSPTransport:       normalizeVideoRTSPTransport(os.Getenv("ATLAS_LOCAL_VIDEO_RTSP_TRANSPORT")),
		VideoRTPBufferSize:       envInt("ATLAS_LOCAL_VIDEO_RTP_BUFFER_SIZE", defaultVideoRTPBufferSize),
		VideoWebRTCICENATIPs:     envList("ATLAS_LOCAL_VIDEO_WEBRTC_ICE_NAT_IPS"),
		VideoWebRTCUDPPortMin:    envUint16("ATLAS_LOCAL_VIDEO_WEBRTC_UDP_PORT_MIN"),
		VideoWebRTCUDPPortMax:    envUint16("ATLAS_LOCAL_VIDEO_WEBRTC_UDP_PORT_MAX"),
	}
}

func normalizeVideoRTSPTransport(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "tcp":
		return "tcp"
	case "udp", "":
		return defaultVideoRTSPTransport
	default:
		return defaultVideoRTSPTransport
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

func envList(key string) []string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return nil
	}

	parts := strings.Split(value, ",")
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			values = append(values, trimmed)
		}
	}
	return values
}

func envUint16(key string) uint16 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseUint(value, 10, 16)
	if err != nil {
		return 0
	}
	return uint16(parsed)
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
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
