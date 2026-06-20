package config

import (
	"os"
	"time"
)

type Config struct {
	BackendURL        string
	AgentID           string
	DroneID           string
	DroneName         string
	AgentVersion      string
	MAVSDKGRPCAddr    string
	PX4SystemAddress  string
	HeartbeatInterval time.Duration
	TelemetryInterval time.Duration
	RegisterRetryMin  time.Duration
	RegisterRetryMax  time.Duration
}

func Load() Config {
	return Config{
		BackendURL:        envOrDefault("ATLAS_BACKEND_URL", "http://127.0.0.1:8080"),
		AgentID:           envOrDefault("ATLAS_AGENT_ID", "agent-001"),
		DroneID:           envOrDefault("ATLAS_DRONE_ID", "drone-001"),
		DroneName:         envOrDefault("ATLAS_DRONE_NAME", "Training Quad 1"),
		AgentVersion:      envOrDefault("ATLAS_AGENT_VERSION", "0.1.0-dev"),
		MAVSDKGRPCAddr:    envOrDefault("ATLAS_MAVSDK_GRPC_ADDR", "127.0.0.1:50051"),
		PX4SystemAddress:  envOrDefault("ATLAS_PX4_SYSTEM_ADDRESS", "udpin://0.0.0.0:14540"),
		HeartbeatInterval: 5 * time.Second,
		TelemetryInterval: 2 * time.Second,
		RegisterRetryMin:  1 * time.Second,
		RegisterRetryMax:  30 * time.Second,
	}
}

func envOrDefault(key string, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}

	return value
}
