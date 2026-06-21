package telemetry

import (
	"context"
	"strings"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/backend"
	"github.com/sunnyside/atlas/atlas-agent/internal/vehicle"
)

type Source interface {
	Name() string
	Read(now time.Time) (backend.TelemetryRequest, error)
}

type sourceConfig struct {
	mavsdkGRPCAddr string
}

type SourceOption func(*sourceConfig)

func WithMAVSDKGRPCAddr(addr string) SourceOption {
	return func(cfg *sourceConfig) {
		cfg.mavsdkGRPCAddr = strings.TrimSpace(addr)
	}
}

func NewSource(ctx context.Context, options ...SourceOption) (Source, error) {
	cfg := sourceConfig{
		mavsdkGRPCAddr: "127.0.0.1:50051",
	}
	for _, option := range options {
		option(&cfg)
	}

	gateway, err := vehicle.NewMAVSDKGateway(cfg.mavsdkGRPCAddr)
	if err != nil {
		return nil, err
	}

	return NewGatewaySource(ctx, "px4", gateway)
}
