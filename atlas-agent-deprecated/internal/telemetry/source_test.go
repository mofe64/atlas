package telemetry

import (
	"context"
	"testing"
)

func TestNewSourceBuildsPX4Source(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	source, err := NewSource(ctx)
	if err != nil {
		t.Fatalf("new source: %v", err)
	}

	if source.Name() != "px4" {
		t.Fatalf("expected px4 source, got %q", source.Name())
	}
}

func TestNewSourceRejectsEmptyMAVSDKAddress(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := NewSource(ctx, WithMAVSDKGRPCAddr(""))
	if err == nil {
		t.Fatal("expected empty mavsdk address error")
	}
}
