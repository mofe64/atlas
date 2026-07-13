package telemetry

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/vehicle"
)

const telemetryReadMaxAge = 5 * time.Second

type GatewaySource struct {
	name   string
	events <-chan vehicle.TelemetryEvent
	latest vehicle.TelemetryEvent
}

func NewGatewaySource(ctx context.Context, name string, gateway vehicle.Gateway) (*GatewaySource, error) {
	events, err := gateway.Telemetry(ctx)
	if err != nil {
		return nil, fmt.Errorf("start vehicle telemetry: %w", err)
	}

	return &GatewaySource{
		name:   name,
		events: events,
	}, nil
}

func (s *GatewaySource) Name() string {
	return s.name
}

func (s *GatewaySource) Read(now time.Time) (Snapshot, error) {
	for {
		select {
		case event, ok := <-s.events:
			if !ok {
				return Snapshot{}, errors.New("vehicle telemetry stream closed")
			}
			s.latest = event
		default:
			return s.latestRequest(now)
		}
	}
}

func (s *GatewaySource) latestRequest(now time.Time) (Snapshot, error) {
	if s.latest.ObservedAt.IsZero() {
		return Snapshot{}, errors.New("vehicle telemetry not ready")
	}

	if now.Sub(s.latest.ObservedAt) > telemetryReadMaxAge {
		return Snapshot{}, errors.New("vehicle telemetry stale")
	}

	return Snapshot{
		ObservedAt:        s.latest.ObservedAt.UTC(),
		BatteryPercent:    s.latest.BatteryPercent,
		RelativeAltitudeM: s.latest.RelativeAltitudeM,
		FlightMode:        s.latest.FlightMode,
		Armed:             s.latest.Armed,
		InAir:             s.latest.InAir,
		Latitude:          s.latest.Latitude,
		Longitude:         s.latest.Longitude,
		HeadingDeg:        s.latest.HeadingDeg,
		GroundSpeedMPS:    s.latest.GroundSpeedMPS,
		GPSFix:            s.latest.GPSFix,
		SatellitesVisible: s.latest.SatellitesVisible,
		HomePositionSet:   s.latest.HomePositionSet,
		Source:            s.latest.Source,
	}, nil
}
