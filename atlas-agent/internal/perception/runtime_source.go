package perception

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
)

const maxRuntimeMessageBytes = 1 << 20

type runtimeEnvelope struct {
	ProtocolVersion  string                   `json:"protocolVersion"`
	Type             string                   `json:"type"`
	Frame            *Frame                   `json:"frame,omitempty"`
	Health           *Health                  `json:"health,omitempty"`
	ActivationResult *runtimeActivationResult `json:"activationResult,omitempty"`
}

// FrameTimingObserver is intentionally narrow so the runtime boundary can
// feed the geolocation clock foundation without depending on camera
// calibration or world-space projection concerns.
type FrameTimingObserver interface {
	ObserveFrameTiming(sourceID, streamEpoch string, sourcePTSNS int64, sourcePTSPresent bool, pipelineIngressMonotonicNS, pipelineIngressUnixNS, sourceCaptureUnixNS int64)
}

// StartRuntimeSource listens for a provider-specific process on a local Unix
// socket. The provider sends versioned NDJSON envelopes, but every payload is
// already translated into Atlas' accelerator-neutral model.
func StartRuntimeSource(ctx context.Context, logger *slog.Logger, socketPath string) (Outputs, error) {
	return StartRuntimeSourceWithTracker(ctx, logger, socketPath, NewDisabledTrackingStage())
}

// StartRuntimeSourceWithTracker is the injectable runtime boundary used by
// algorithm integration and deterministic tests. A nil stage is replaced by a
// disabled stage so provider-owned IDs can never bypass Atlas ownership.
func StartRuntimeSourceWithTracker(ctx context.Context, logger *slog.Logger, socketPath string, tracker *TrackingStage) (Outputs, error) {
	return StartRuntimeSourceWithTrackerAndTiming(ctx, logger, socketPath, tracker, nil)
}

// StartRuntimeSourceWithTrackerAndTiming wires the provider's pre-inference
// frame anchor into the onboard temporal foundation.
func StartRuntimeSourceWithTrackerAndTiming(ctx context.Context, logger *slog.Logger, socketPath string, tracker *TrackingStage, timingObserver FrameTimingObserver) (Outputs, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if tracker == nil {
		tracker = NewDisabledTrackingStage()
	}
	if !filepath.IsAbs(socketPath) {
		return Outputs{}, errors.New("perception runtime socket path must be absolute")
	}
	if err := prepareSocketPath(socketPath); err != nil {
		return Outputs{}, err
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return Outputs{}, fmt.Errorf("listen for perception runtime: %w", err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		listener.Close()
		return Outputs{}, fmt.Errorf("protect perception runtime socket: %w", err)
	}

	frames := make(chan Frame, 1)
	health := make(chan Health, 1)
	trackUpdates := tracker.SubscribeTrackUpdates()
	controller := newRuntimeController(ctx)
	controller.setTrackingStage(tracker)
	go runRuntimeSource(ctx, logger, socketPath, listener, frames, health, controller, tracker, timingObserver)
	return Outputs{Frames: frames, Health: health, TrackUpdates: trackUpdates, Control: controller, Counting: tracker, TrackFollower: tracker}, nil
}

func prepareSocketPath(socketPath string) error {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		return fmt.Errorf("create perception runtime directory: %w", err)
	}
	info, err := os.Lstat(socketPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect perception runtime socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return errors.New("perception runtime socket path exists and is not a socket")
	}
	if err := os.Remove(socketPath); err != nil {
		return fmt.Errorf("remove stale perception runtime socket: %w", err)
	}
	return nil
}

func runRuntimeSource(ctx context.Context, logger *slog.Logger, socketPath string, listener net.Listener, frames chan Frame, health chan Health, controller *runtimeController, tracker *TrackingStage, timingObserver FrameTimingObserver) {
	defer close(frames)
	defer close(health)
	defer listener.Close()
	defer os.Remove(socketPath)

	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	for ctx.Err() == nil {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() == nil {
				logger.Warn("accept perception runtime connection", "error", err)
			}
			return
		}
		logger.Info("perception runtime connected", "socket_path", socketPath)
		controller.setConnection(connection)
		if err := readRuntimeConnection(ctx, connection, frames, health, controller, tracker, timingObserver); err != nil && ctx.Err() == nil {
			logger.Warn("perception runtime connection ended", "error", err)
		}
		controller.clearConnection(connection)
		connection.Close()
	}
}

func readRuntimeConnection(ctx context.Context, connection net.Conn, frames chan Frame, health chan Health, controller *runtimeController, tracker *TrackingStage, timingObserver FrameTimingObserver) error {
	scanner := bufio.NewScanner(connection)
	scanner.Buffer(make([]byte, 64*1024), maxRuntimeMessageBytes)
	for scanner.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var envelope runtimeEnvelope
		if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil {
			return fmt.Errorf("decode perception runtime envelope: %w", err)
		}
		if envelope.ProtocolVersion != AdapterProtocolVersion {
			return fmt.Errorf("unsupported perception runtime protocol %q", envelope.ProtocolVersion)
		}
		switch envelope.Type {
		case "frame":
			if envelope.Frame == nil {
				return errors.New("perception frame envelope has no frame")
			}
			if err := envelope.Frame.Validate(); err != nil {
				return fmt.Errorf("invalid perception frame: %w", err)
			}
			if envelope.Frame.Timing == nil {
				return errors.New("perception protocol v3 frame has no pre-inference timing anchor")
			}
			if timingObserver != nil {
				timing := envelope.Frame.Timing
				timingObserver.ObserveFrameTiming(
					envelope.Frame.SourceID, envelope.Frame.StreamEpoch, envelope.Frame.SourcePTSNS,
					timing.SourcePTSPresent, timing.PipelineIngressMonotonicNS,
					timing.PipelineIngressUnixNS, timing.SourceCaptureUnixNS,
				)
			}
			frame := controller.filterFrame(*envelope.Frame)
			frame = tracker.Process(frame)
			controller.observeFrame(frame)
			publishLatest(frames, frame)
		case "health":
			if envelope.Health == nil {
				return errors.New("perception health envelope has no health")
			}
			trackedHealth := tracker.EnrichHealth(*envelope.Health)
			if err := trackedHealth.Validate(); err != nil {
				return fmt.Errorf("invalid perception health: %w", err)
			}
			publishLatest(health, trackedHealth)
		case "activation_result":
			if envelope.ActivationResult == nil {
				return errors.New("perception activation result envelope has no result")
			}
			if strings.TrimSpace(envelope.ActivationResult.RequestID) == "" ||
				(envelope.ActivationResult.State != "ACTIVE" && envelope.ActivationResult.State != "INACTIVE" && envelope.ActivationResult.State != "FAILED") ||
				envelope.ActivationResult.ObservedAt.IsZero() {
				return errors.New("perception activation result is invalid")
			}
			controller.handleActivationResult(*envelope.ActivationResult)
		default:
			return fmt.Errorf("unknown perception runtime envelope type %q", envelope.Type)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read perception runtime envelope: %w", err)
	}
	return nil
}

func publishLatest[T any](channel chan T, value T) {
	select {
	case channel <- value:
		return
	default:
	}
	select {
	case <-channel:
	default:
	}
	select {
	case channel <- value:
	default:
	}
}
