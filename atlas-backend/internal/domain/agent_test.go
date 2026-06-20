package domain

import (
	"testing"
	"time"
)

func TestStatusFromHeartbeat(t *testing.T) {
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)

	tests := []struct {
		name            string
		lastHeartbeatAt time.Time
		want            AgentStatus
	}{
		{
			name: "registered when heartbeat has never arrived",
			want: AgentStatusRegistered,
		},
		{
			name:            "online inside online window",
			lastHeartbeatAt: now.Add(-OnlineWindow),
			want:            AgentStatusOnline,
		},
		{
			name:            "stale after online window",
			lastHeartbeatAt: now.Add(-(OnlineWindow + time.Second)),
			want:            AgentStatusStale,
		},
		{
			name:            "offline after stale window",
			lastHeartbeatAt: now.Add(-(StaleWindow + time.Second)),
			want:            AgentStatusOffline,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StatusFromHeartbeat(tt.lastHeartbeatAt, now)
			if got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}
