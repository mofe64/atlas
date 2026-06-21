package domain

import "time"

type Drone struct {
	ID         string
	Name       string
	LastSeenAt time.Time
}

type Agent struct {
	ID                               string
	DroneID                          string
	Version                          string
	RegisteredAt                     time.Time
	LastHeartbeatAt                  time.Time
	CommandChannelState              CommandChannelState
	CommandChannelConnectedAt        time.Time
	CommandChannelLastDisconnectedAt time.Time
}

type AgentStatus string

const (
	AgentStatusRegistered AgentStatus = "registered"
	AgentStatusOnline     AgentStatus = "online"
	AgentStatusStale      AgentStatus = "stale"
	AgentStatusOffline    AgentStatus = "offline"
)

type CommandChannelState string

const (
	CommandChannelDisconnected CommandChannelState = "disconnected"
	CommandChannelConnected    CommandChannelState = "connected"
)

const (
	HeartbeatInterval = 5 * time.Second
	OnlineWindow      = 15 * time.Second
	StaleWindow       = 60 * time.Second
)

func StatusFromHeartbeat(lastHeartbeatAt time.Time, now time.Time) AgentStatus {
	if lastHeartbeatAt.IsZero() {
		return AgentStatusRegistered
	}

	age := now.Sub(lastHeartbeatAt)
	if age <= OnlineWindow {
		return AgentStatusOnline
	}

	if age <= StaleWindow {
		return AgentStatusStale
	}

	return AgentStatusOffline
}
