package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/backend"
	"github.com/sunnyside/atlas/atlas-agent/internal/config"
	"github.com/sunnyside/atlas/atlas-agent/internal/telemetry"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	cfg := config.Load()
	client := backend.NewClient(cfg.BackendURL)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	telemetrySource, err := telemetry.NewSource(
		telemetry.WithMAVSDKGRPCAddr(cfg.MAVSDKGRPCAddr),
	)
	if err != nil {
		logger.Error("telemetry source setup failed", "source", "px4", "error", err)
		os.Exit(1)
	}
	logger.Info(
		"telemetry source ready",
		"source", telemetrySource.Name(),
		"mavsdk_grpc_addr", cfg.MAVSDKGRPCAddr,
		"px4_system_address", cfg.PX4SystemAddress,
	)

	register, err := registerWithRetry(ctx, logger, client, cfg)
	if err != nil {
		logger.Error("agent stopped before registration completed", "error", err)
		os.Exit(1)
	}

	logger.Info(
		"agent registered",
		"agent_id", register.AgentID,
		"drone_id", register.DroneID,
		"status", register.Status,
		"heartbeat_interval_seconds", register.HeartbeatIntervalSeconds,
	)

	ticker := time.NewTicker(cfg.HeartbeatInterval)
	defer ticker.Stop()

	telemetryTicker := time.NewTicker(cfg.TelemetryInterval)
	defer telemetryTicker.Stop()

	if err := sendHeartbeat(ctx, logger, client, cfg); err != nil {
		if _, err := registerWithRetry(ctx, logger, client, cfg); err != nil {
			logger.Error("agent stopped before heartbeat recovery completed", "error", err)
			os.Exit(1)
		}
	}
	sendTelemetry(ctx, logger, client, cfg, telemetrySource)

	for {
		select {
		case <-ctx.Done():
			logger.Info("atlas agent stopped")
			return
		case <-ticker.C:
			if err := sendHeartbeat(ctx, logger, client, cfg); err != nil {
				logger.Warn("heartbeat failed; re-registering agent", "error", err)
				if _, err := registerWithRetry(ctx, logger, client, cfg); err != nil {
					logger.Error("agent stopped before heartbeat recovery completed", "error", err)
					os.Exit(1)
				}
			}
		case <-telemetryTicker.C:
			sendTelemetry(ctx, logger, client, cfg, telemetrySource)
		}
	}
}

func registerWithRetry(ctx context.Context, logger *slog.Logger, client *backend.Client, cfg config.Config) (backend.RegisterAgentResponse, error) {
	backoff := cfg.RegisterRetryMin

	for {
		res, err := client.RegisterAgent(ctx, backend.RegisterAgentRequest{
			AgentID:      cfg.AgentID,
			DroneID:      cfg.DroneID,
			DroneName:    cfg.DroneName,
			AgentVersion: cfg.AgentVersion,
		})
		if err == nil {
			return res, nil
		}

		logger.Warn(
			"agent registration failed; retrying",
			"error", err,
			"retry_after", backoff.String(),
		)

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return backend.RegisterAgentResponse{}, ctx.Err()
		case <-timer.C:
		}

		backoff = nextBackoff(backoff, cfg.RegisterRetryMax)
	}
}

func sendHeartbeat(ctx context.Context, logger *slog.Logger, client *backend.Client, cfg config.Config) error {
	res, err := client.SendHeartbeat(ctx, cfg.AgentID, backend.HeartbeatRequest{
		AgentVersion: cfg.AgentVersion,
	})
	if err != nil {
		return err
	}

	logger.Info(
		"heartbeat accepted",
		"agent_id", res.AgentID,
		"drone_id", res.DroneID,
		"status", res.Status,
		"last_heartbeat_at", res.LastHeartbeatAt,
	)

	return nil
}

func nextBackoff(current time.Duration, max time.Duration) time.Duration {
	next := current * 2
	if next > max {
		return max
	}

	return next
}

func sendTelemetry(ctx context.Context, logger *slog.Logger, client *backend.Client, cfg config.Config, source telemetry.Source) {
	snapshot, err := source.Read(time.Now().UTC())
	if err != nil {
		logger.Error("telemetry read failed", "source", source.Name(), "error", err)
		return
	}

	res, err := client.SendTelemetry(ctx, cfg.AgentID, snapshot)
	if err != nil {
		logger.Error("telemetry send failed", "error", err)
		return
	}

	logger.Info(
		"telemetry accepted",
		"agent_id", res.AgentID,
		"drone_id", res.DroneID,
		"telemetry_state", res.TelemetryState,
		"source", snapshot.Source,
	)
}
