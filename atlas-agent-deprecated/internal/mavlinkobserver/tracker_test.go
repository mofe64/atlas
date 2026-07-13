package mavlinkobserver

import (
	"context"
	"testing"
	"time"
)

func TestTrackerMatchesCommandAckByCommandTimeAndSource(t *testing.T) {
	tracker := NewTracker()
	observedAt := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)

	tracker.HandleObservation(context.Background(), Observation{
		Kind:        ObservationHeartbeat,
		ObservedAt:  observedAt.Add(-time.Second),
		SystemID:    1,
		ComponentID: 1,
		MessageID:   MessageIDHeartbeat,
		Heartbeat: &HeartbeatObservation{
			Autopilot: 12,
		},
	})
	tracker.HandleObservation(context.Background(), Observation{
		Kind:        ObservationCommandAck,
		ObservedAt:  observedAt,
		SystemID:    1,
		ComponentID: 1,
		MessageID:   MessageIDCommandAck,
		CommandAck: &CommandAckObservation{
			Command: 400,
			Result:  0,
		},
	})

	sourceSystemID, sourceComponentID, ok := tracker.PreferredCommandAckSource()
	if !ok {
		t.Fatal("expected preferred autopilot source")
	}

	ack, ok := tracker.WaitForCommandAck(context.Background(), CommandAckMatch{
		ActionID:           "act-1",
		ActionType:         "arm",
		Command:            400,
		EarliestObservedAt: observedAt.Add(-time.Millisecond),
		SourceSystemID:     sourceSystemID,
		SourceComponentID:  sourceComponentID,
		FinalOnly:          true,
	})
	if !ok {
		t.Fatal("expected matching command ack")
	}
	if !ack.Accepted() {
		t.Fatalf("expected accepted ack, got %s", ack.RawAckCode())
	}
	if ack.MatchStatus != "matched_command_time_active_action_source" {
		t.Fatalf("unexpected match status %q", ack.MatchStatus)
	}

	diagnostics := tracker.SnapshotDiagnostics()
	if diagnostics.PacketsSeen != 2 {
		t.Fatalf("expected two observed packets, got %d", diagnostics.PacketsSeen)
	}
	if diagnostics.LastCommandAckCommand != 400 {
		t.Fatalf("expected last ack command 400, got %d", diagnostics.LastCommandAckCommand)
	}
}

func TestTrackerIgnoresInProgressAckWhenFinalOnly(t *testing.T) {
	tracker := NewTracker()
	observedAt := time.Now().UTC()
	tracker.HandleObservation(context.Background(), Observation{
		Kind:        ObservationCommandAck,
		ObservedAt:  observedAt,
		SystemID:    1,
		ComponentID: 1,
		MessageID:   MessageIDCommandAck,
		CommandAck: &CommandAckObservation{
			Command: 22,
			Result:  5,
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if _, ok := tracker.WaitForCommandAck(ctx, CommandAckMatch{
		Command:            22,
		EarliestObservedAt: observedAt.Add(-time.Millisecond),
		FinalOnly:          true,
	}); ok {
		t.Fatal("expected in-progress ack to be ignored")
	}
}

func TestTrackerReturnsRejectedCommandAck(t *testing.T) {
	tracker := NewTracker()
	observedAt := time.Now().UTC()
	tracker.HandleObservation(context.Background(), Observation{
		Kind:        ObservationCommandAck,
		ObservedAt:  observedAt,
		SystemID:    1,
		ComponentID: 1,
		MessageID:   MessageIDCommandAck,
		CommandAck: &CommandAckObservation{
			Command: 21,
			Result:  2,
		},
	})

	ack, ok := tracker.WaitForCommandAck(context.Background(), CommandAckMatch{
		Command:            21,
		EarliestObservedAt: observedAt.Add(-time.Millisecond),
		FinalOnly:          true,
	})
	if !ok {
		t.Fatal("expected rejected ack")
	}
	if ack.Accepted() {
		t.Fatal("expected rejected ack to be non-accepted")
	}
	if ack.RawAckCode() != "MAV_RESULT_DENIED" {
		t.Fatalf("expected denied result label, got %q", ack.RawAckCode())
	}
}
