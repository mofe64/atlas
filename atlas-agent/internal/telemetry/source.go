package telemetry

import (
	"strings"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/backend"
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

func NewSource(options ...SourceOption) (Source, error) {
	cfg := sourceConfig{
		mavsdkGRPCAddr: "127.0.0.1:50051",
	}
	for _, option := range options {
		option(&cfg)
	}

	return NewPX4Source(cfg.mavsdkGRPCAddr)
}
