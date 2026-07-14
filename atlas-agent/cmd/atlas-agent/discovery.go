package main

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type gimbalDiscoveryFunc func(context.Context) ([]int32, error)

// discoverGimbalsWithRetry bridges systemd process ordering and MAVSDK API
// readiness. atlas-mavsdk.service can be running before its gRPC listener is
// accepting calls, and the physical gimbal may register shortly afterward.
func discoverGimbalsWithRetry(
	ctx context.Context,
	attemptTimeout time.Duration,
	retryInterval time.Duration,
	discover gimbalDiscoveryFunc,
) ([]int32, error) {
	if attemptTimeout <= 0 {
		return nil, errors.New("gimbal discovery attempt timeout must be positive")
	}
	if retryInterval < 0 {
		return nil, errors.New("gimbal discovery retry interval cannot be negative")
	}

	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			if lastErr == nil {
				return nil, err
			}
			return nil, fmt.Errorf("gimbal discovery did not become ready: %w", lastErr)
		}

		attemptContext, cancelAttempt := context.WithTimeout(ctx, attemptTimeout)
		ids, err := discover(attemptContext)
		cancelAttempt()
		if err == nil && len(ids) > 0 {
			return ids, nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = errors.New("MAVSDK returned an empty gimbal list")
		}

		timer := time.NewTimer(retryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}
