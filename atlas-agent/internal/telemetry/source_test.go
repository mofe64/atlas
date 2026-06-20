package telemetry

import (
	"testing"
)

func TestNewSourceBuildsPX4Source(t *testing.T) {
	source, err := NewSource()
	if err != nil {
		t.Fatalf("new source: %v", err)
	}

	if source.Name() != "px4" {
		t.Fatalf("expected px4 source, got %q", source.Name())
	}
}

func TestNewSourceRejectsEmptyMAVSDKAddress(t *testing.T) {
	_, err := NewSource(WithMAVSDKGRPCAddr(""))
	if err == nil {
		t.Fatal("expected empty mavsdk address error")
	}
}
