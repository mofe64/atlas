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
)

const maxRuntimeMessageBytes = 1 << 20

type runtimeEnvelope struct {
	ProtocolVersion string  `json:"protocolVersion"`
	Type            string  `json:"type"`
	Frame           *Frame  `json:"frame,omitempty"`
	Health          *Health `json:"health,omitempty"`
}

// StartRuntimeSource listens for a provider-specific process on a local Unix
// socket. The provider sends versioned NDJSON envelopes, but every payload is
// already translated into Atlas' accelerator-neutral model.
func StartRuntimeSource(ctx context.Context, logger *slog.Logger, socketPath string) (Outputs, error) {
	if logger == nil {
		logger = slog.Default()
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
	go runRuntimeSource(ctx, logger, socketPath, listener, frames, health)
	return Outputs{Frames: frames, Health: health}, nil
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

func runRuntimeSource(ctx context.Context, logger *slog.Logger, socketPath string, listener net.Listener, frames chan Frame, health chan Health) {
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
		if err := readRuntimeConnection(ctx, connection, frames, health); err != nil && ctx.Err() == nil {
			logger.Warn("perception runtime connection ended", "error", err)
		}
		connection.Close()
	}
}

func readRuntimeConnection(ctx context.Context, connection net.Conn, frames chan Frame, health chan Health) error {
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
		if envelope.ProtocolVersion != RuntimeProtocolVersion {
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
			publishLatest(frames, *envelope.Frame)
		case "health":
			if envelope.Health == nil {
				return errors.New("perception health envelope has no health")
			}
			if err := envelope.Health.Validate(); err != nil {
				return fmt.Errorf("invalid perception health: %w", err)
			}
			publishLatest(health, *envelope.Health)
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
