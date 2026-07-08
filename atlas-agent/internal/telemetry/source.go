package telemetry

import (
	"context"
	"strings"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/vehicle"
)

type Snapshot struct {
	ObservedAt        time.Time
	BatteryPercent    float64
	RelativeAltitudeM float64
	FlightMode        string
	Armed             bool
	InAir             bool
	Latitude          float64
	Longitude         float64
	HeadingDeg        float64
	GroundSpeedMPS    float64
	GPSFix            string
	SatellitesVisible int
	HomePositionSet   bool
	Source            string
}

type Source interface {
	Name() string
	Read(now time.Time) (Snapshot, error)
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
