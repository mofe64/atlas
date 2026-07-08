package models

import (
	"testing"
	"time"
)

func TestVehicleAgentStatusFromHeartbeat(t *testing.T) {
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)

	tests := []struct {
		name            string
		lastHeartbeatAt time.Time
		want            VehicleAgentStatus
	}{
		{
			name: "registered when heartbeat has never arrived",
			want: VehicleAgentStatusRegistered,
		},
		{
			name:            "online inside online window",
			lastHeartbeatAt: now.Add(-OnlineWindow),
			want:            VehicleAgentStatusOnline,
		},
		{
			name:            "stale after online window",
			lastHeartbeatAt: now.Add(-(OnlineWindow + time.Second)),
			want:            VehicleAgentStatusStale,
		},
		{
			name:            "offline after stale window",
			lastHeartbeatAt: now.Add(-(StaleWindow + time.Second)),
			want:            VehicleAgentStatusOffline,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := VehicleAgentStatusFromHeartbeat(tt.lastHeartbeatAt, now)
			if got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}
