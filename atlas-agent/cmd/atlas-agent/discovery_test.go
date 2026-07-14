package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDiscoverGimbalsWithRetryWaitsForMAVSDKAndNonEmptyList(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	attempts := 0
	ids, err := discoverGimbalsWithRetry(ctx, 50*time.Millisecond, time.Millisecond, func(context.Context) ([]int32, error) {
		attempts++
		switch attempts {
		case 1:
			return nil, errors.New("MAVSDK gRPC connection refused")
		case 2:
			return nil, nil
		default:
			return []int32{7}, nil
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 3 || len(ids) != 1 || ids[0] != 7 {
		t.Fatalf("attempts = %d, ids = %v; want 3 attempts and gimbal 7", attempts, ids)
	}
}

func TestDiscoverGimbalsWithRetryReportsLastFailureAtDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := discoverGimbalsWithRetry(ctx, 5*time.Millisecond, time.Millisecond, func(context.Context) ([]int32, error) {
		return nil, errors.New("connection refused")
	})
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("error = %v, want final discovery failure", err)
	}
}
