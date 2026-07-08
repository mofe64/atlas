package models

import (
	"testing"
	"time"
)

func TestTelemetryStateFromReceivedAt(t *testing.T) {
	now := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)

	tests := []struct {
		name       string
		receivedAt time.Time
		want       TelemetryState
	}{
		{name: "unknown without telemetry", want: TelemetryStateUnknown},
		{name: "fresh inside fresh window", receivedAt: now.Add(-TelemetryFreshWindow), want: TelemetryStateFresh},
		{name: "stale after fresh window", receivedAt: now.Add(-(TelemetryFreshWindow + time.Second)), want: TelemetryStateStale},
		{name: "lost after stale window", receivedAt: now.Add(-(TelemetryStaleWindow + time.Second)), want: TelemetryStateLost},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TelemetryStateFromReceivedAt(tt.receivedAt, now)
			if got != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, got)
			}
		})
	}
}
