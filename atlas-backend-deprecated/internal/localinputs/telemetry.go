package localinputs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/sunnyside/atlas/atlas-backend/internal/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repository"
	"github.com/sunnyside/atlas/atlas-backend/internal/services"
)

type TelemetryService interface {
	RecordLocalTelemetry(context.Context, repository.RecordLocalTelemetryInput, time.Time) (models.TelemetrySnapshot, bool, error)
}

func RunTelemetry(ctx context.Context, logger *slog.Logger, cfg Config, telemetry TelemetryService) error {
	if logger == nil {
		logger = slog.Default()
	}
	if !cfg.Enabled || strings.TrimSpace(cfg.TelemetryEndpoint) == "" {
		return nil
	}
	if strings.TrimSpace(cfg.DroneID) == "" {
		return errors.New("ATLAS_LOCAL_INPUT_DRONE_ID or ATLAS_DRONE_ID is required when local telemetry is enabled")
	}
	if telemetry == nil {
		return errors.New("local telemetry service is required")
	}
	if cfg.TelemetryPublishInterval <= 0 {
		cfg.TelemetryPublishInterval = time.Second
	}

	network, addr, err := udpListenAddress(cfg.TelemetryEndpoint)
	if err != nil {
		return err
	}

	conn, err := net.ListenPacket(network, addr)
	if err != nil {
		return fmt.Errorf("listen for local telemetry %s: %w", cfg.TelemetryEndpoint, err)
	}
	defer conn.Close()

	logger.Info("local telemetry input listening", "endpoint", cfg.TelemetryEndpoint, "drone_id", cfg.DroneID, "source_id", cfg.SourceID)

	buffer := make([]byte, 4096)
	accumulator := &telemetryAccumulator{}
	var lastPublished time.Time
	for ctx.Err() == nil {
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		n, _, err := conn.ReadFrom(buffer)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read local telemetry packet: %w", err)
		}

		now := time.Now().UTC()
		for _, frame := range parseMAVLinkFrames(buffer[:n]) {
			accumulator.handleFrame(frame, now)
		}
		if now.Sub(lastPublished) < cfg.TelemetryPublishInterval {
			continue
		}

		snapshot, ok := accumulator.snapshot("local:" + cfg.SourceID)
		if !ok {
			continue
		}
		_, promoted, err := telemetry.RecordLocalTelemetry(ctx, repository.RecordLocalTelemetryInput{
			DroneID:             cfg.DroneID,
			SourceID:            cfg.SourceID,
			Source:              "local:" + cfg.SourceID,
			Transport:           "MAVLINK_UDP",
			EndpointDescription: cfg.TelemetryEndpoint,
			Roles:               []models.CommunicationLinkRole{models.CommunicationLinkRoleTelemetry},
			Snapshot:            snapshot,
		}, now)
		if err != nil {
			logger.Warn("local telemetry sample rejected", "endpoint", cfg.TelemetryEndpoint, "drone_id", cfg.DroneID, "error", err)
			continue
		}
		lastPublished = now
		logger.Debug("local telemetry sample accepted", "endpoint", cfg.TelemetryEndpoint, "drone_id", cfg.DroneID, "promoted", promoted)
	}
	return nil
}

func StartTelemetry(ctx context.Context, logger *slog.Logger, cfg Config, telemetry *services.TelemetryService) {
	if !cfg.Enabled || strings.TrimSpace(cfg.TelemetryEndpoint) == "" {
		return
	}

	go func() {
		if err := RunTelemetry(ctx, logger, cfg, telemetry); err != nil && ctx.Err() == nil {
			logger.Warn("local telemetry input stopped", "endpoint", cfg.TelemetryEndpoint, "error", err)
		}
	}()
}

func udpListenAddress(endpoint string) (string, string, error) {
	endpoint = strings.TrimSpace(endpoint)
	for _, prefix := range []string{"udp-server://", "udp://"} {
		if strings.HasPrefix(endpoint, prefix) {
			addr := strings.TrimPrefix(endpoint, prefix)
			if addr == "" {
				return "", "", errors.New("local telemetry UDP endpoint address is required")
			}
			return "udp", addr, nil
		}
	}
	return "", "", fmt.Errorf("unsupported local telemetry endpoint %q; use udp-server://host:port", endpoint)
}
