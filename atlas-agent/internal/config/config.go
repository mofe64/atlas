package config

import (
	"os"
	"time"
)

type Config struct {
	VehicleAgentID       string
	DroneID              string
	DroneName            string
	VehicleAgentVersion  string
	VehicleAgentGRPCAddr string
	MAVSDKGRPCAddr       string
	PX4SystemAddress     string
	HeartbeatInterval    time.Duration
	TelemetryInterval    time.Duration
	CommandTimeout       time.Duration
	ChannelRetryMin      time.Duration
	ChannelRetryMax      time.Duration
}

func Load() Config {
	return Config{
		VehicleAgentID:       envOrDefault("ATLAS_VEHICLE_AGENT_ID", "agent-001"),
		DroneID:              envOrDefault("ATLAS_DRONE_ID", "drone-001"),
		DroneName:            envOrDefault("ATLAS_DRONE_NAME", "Training Quad 1"),
		VehicleAgentVersion:  envOrDefault("ATLAS_VEHICLE_AGENT_VERSION", "0.1.0-dev"),
		VehicleAgentGRPCAddr: envOrDefault("ATLAS_VEHICLE_AGENT_GRPC_ADDR", "127.0.0.1:9090"),
		MAVSDKGRPCAddr:       envOrDefault("ATLAS_MAVSDK_GRPC_ADDR", "127.0.0.1:50051"),
		PX4SystemAddress:     envOrDefault("ATLAS_PX4_SYSTEM_ADDRESS", "udpin://0.0.0.0:14540"),
		HeartbeatInterval:    5 * time.Second,
		TelemetryInterval:    2 * time.Second,
		CommandTimeout:       15 * time.Second,
		ChannelRetryMin:      1 * time.Second,
		ChannelRetryMax:      30 * time.Second,
	}
}

func envOrDefault(key string, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}

	return value
}
