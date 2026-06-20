package telemetry

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/backend"
	corepb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/core"
	telemetrypb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/telemetry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	px4SourceName       = "px4"
	px4ReadMaxAge       = 5 * time.Second
	px4StreamRetryDelay = 2 * time.Second
	px4RateHz           = 2
)

type PX4Source struct {
	conn      *grpc.ClientConn
	core      corepb.CoreServiceClient
	telemetry telemetrypb.TelemetryServiceClient

	mu             sync.RWMutex
	snapshot       backend.TelemetryRequest
	updatedAt      time.Time
	havePosition   bool
	haveConnection bool
	connected      bool
	lastErr        error
}

func NewPX4Source(grpcAddr string) (*PX4Source, error) {
	if grpcAddr == "" {
		return nil, errors.New("mavsdk gRPC address is required")
	}

	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("create mavsdk gRPC client: %w", err)
	}

	source := &PX4Source{
		conn:      conn,
		core:      corepb.NewCoreServiceClient(conn),
		telemetry: telemetrypb.NewTelemetryServiceClient(conn),
		snapshot: backend.TelemetryRequest{
			FlightMode: "UNKNOWN",
			GPSFix:     "UNKNOWN",
			Source:     px4SourceName,
		},
	}

	source.start()

	return source, nil
}

func (s *PX4Source) Name() string {
	return px4SourceName
}

func (s *PX4Source) Read(now time.Time) (backend.TelemetryRequest, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.haveConnection && !s.connected {
		return backend.TelemetryRequest{}, errors.New("px4 telemetry not ready: mavsdk_server is not connected to PX4")
	}

	if !s.havePosition {
		return backend.TelemetryRequest{}, s.readinessError("px4 telemetry not ready: waiting for valid position")
	}

	if now.Sub(s.updatedAt) > px4ReadMaxAge {
		return backend.TelemetryRequest{}, s.readinessError("px4 telemetry stale: no recent mavsdk updates")
	}

	snapshot := s.snapshot
	snapshot.ObservedAt = s.updatedAt.UTC()
	snapshot.Source = px4SourceName

	return snapshot, nil
}

func (s *PX4Source) start() {
	s.bestEffortSetRates()

	go s.streamConnectionState()
	go s.streamPosition()
	go s.streamBattery()
	go s.streamFlightMode()
	go s.streamArmed()
	go s.streamInAir()
	go s.streamGpsInfo()
	go s.streamHeading()
	go s.streamAltitude()
	go s.streamHealth()
}

func (s *PX4Source) bestEffortSetRates() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, _ = s.telemetry.SetRatePosition(ctx, &telemetrypb.SetRatePositionRequest{RateHz: px4RateHz})
	_, _ = s.telemetry.SetRateBattery(ctx, &telemetrypb.SetRateBatteryRequest{RateHz: px4RateHz})
	_, _ = s.telemetry.SetRateGpsInfo(ctx, &telemetrypb.SetRateGpsInfoRequest{RateHz: px4RateHz})
	_, _ = s.telemetry.SetRateInAir(ctx, &telemetrypb.SetRateInAirRequest{RateHz: px4RateHz})
	_, _ = s.telemetry.SetRateAltitude(ctx, &telemetrypb.SetRateAltitudeRequest{RateHz: px4RateHz})
}

func (s *PX4Source) streamConnectionState() {
	for {
		stream, err := s.core.SubscribeConnectionState(context.Background(), &corepb.SubscribeConnectionStateRequest{})
		if err != nil {
			s.recordError(fmt.Errorf("subscribe connection state: %w", err))
			time.Sleep(px4StreamRetryDelay)
			continue
		}

		for {
			res, err := stream.Recv()
			if err != nil {
				s.recordError(fmt.Errorf("receive connection state: %w", err))
				break
			}

			state := res.GetConnectionState()
			s.mu.Lock()
			s.haveConnection = true
			s.connected = state.GetIsConnected()
			s.mu.Unlock()
		}

		time.Sleep(px4StreamRetryDelay)
	}
}

func (s *PX4Source) streamPosition() {
	for {
		stream, err := s.telemetry.SubscribePosition(context.Background(), &telemetrypb.SubscribePositionRequest{})
		if err != nil {
			s.recordError(fmt.Errorf("subscribe position: %w", err))
			time.Sleep(px4StreamRetryDelay)
			continue
		}

		for {
			res, err := stream.Recv()
			if err != nil {
				s.recordError(fmt.Errorf("receive position: %w", err))
				break
			}

			position := res.GetPosition()
			lat := position.GetLatitudeDeg()
			lon := position.GetLongitudeDeg()
			relativeAltitude := float64(position.GetRelativeAltitudeM())
			if !finite(lat) || !finite(lon) || !finite(relativeAltitude) {
				continue
			}

			s.mu.Lock()
			s.snapshot.Latitude = lat
			s.snapshot.Longitude = lon
			s.snapshot.RelativeAltitudeM = relativeAltitude
			s.snapshot.Source = px4SourceName
			s.updatedAt = time.Now().UTC()
			s.havePosition = true
			s.mu.Unlock()
		}

		time.Sleep(px4StreamRetryDelay)
	}
}

func (s *PX4Source) streamBattery() {
	for {
		stream, err := s.telemetry.SubscribeBattery(context.Background(), &telemetrypb.SubscribeBatteryRequest{})
		if err != nil {
			s.recordError(fmt.Errorf("subscribe battery: %w", err))
			time.Sleep(px4StreamRetryDelay)
			continue
		}

		for {
			res, err := stream.Recv()
			if err != nil {
				s.recordError(fmt.Errorf("receive battery: %w", err))
				break
			}

			remaining := float64(res.GetBattery().GetRemainingPercent())
			if !finite(remaining) {
				continue
			}

			s.mu.Lock()
			s.snapshot.BatteryPercent = clamp(remaining, 0, 100)
			s.updatedAt = time.Now().UTC()
			s.mu.Unlock()
		}

		time.Sleep(px4StreamRetryDelay)
	}
}

func (s *PX4Source) streamFlightMode() {
	for {
		stream, err := s.telemetry.SubscribeFlightMode(context.Background(), &telemetrypb.SubscribeFlightModeRequest{})
		if err != nil {
			s.recordError(fmt.Errorf("subscribe flight mode: %w", err))
			time.Sleep(px4StreamRetryDelay)
			continue
		}

		for {
			res, err := stream.Recv()
			if err != nil {
				s.recordError(fmt.Errorf("receive flight mode: %w", err))
				break
			}

			s.mu.Lock()
			s.snapshot.FlightMode = flightModeString(res.GetFlightMode())
			s.updatedAt = time.Now().UTC()
			s.mu.Unlock()
		}

		time.Sleep(px4StreamRetryDelay)
	}
}

func (s *PX4Source) streamArmed() {
	for {
		stream, err := s.telemetry.SubscribeArmed(context.Background(), &telemetrypb.SubscribeArmedRequest{})
		if err != nil {
			s.recordError(fmt.Errorf("subscribe armed: %w", err))
			time.Sleep(px4StreamRetryDelay)
			continue
		}

		for {
			res, err := stream.Recv()
			if err != nil {
				s.recordError(fmt.Errorf("receive armed: %w", err))
				break
			}

			s.mu.Lock()
			s.snapshot.Armed = res.GetIsArmed()
			s.updatedAt = time.Now().UTC()
			s.mu.Unlock()
		}

		time.Sleep(px4StreamRetryDelay)
	}
}

func (s *PX4Source) streamInAir() {
	for {
		stream, err := s.telemetry.SubscribeInAir(context.Background(), &telemetrypb.SubscribeInAirRequest{})
		if err != nil {
			s.recordError(fmt.Errorf("subscribe in-air: %w", err))
			time.Sleep(px4StreamRetryDelay)
			continue
		}

		for {
			res, err := stream.Recv()
			if err != nil {
				s.recordError(fmt.Errorf("receive in-air: %w", err))
				break
			}

			s.mu.Lock()
			s.snapshot.InAir = res.GetIsInAir()
			s.updatedAt = time.Now().UTC()
			s.mu.Unlock()
		}

		time.Sleep(px4StreamRetryDelay)
	}
}

func (s *PX4Source) streamGpsInfo() {
	for {
		stream, err := s.telemetry.SubscribeGpsInfo(context.Background(), &telemetrypb.SubscribeGpsInfoRequest{})
		if err != nil {
			s.recordError(fmt.Errorf("subscribe gps info: %w", err))
			time.Sleep(px4StreamRetryDelay)
			continue
		}

		for {
			res, err := stream.Recv()
			if err != nil {
				s.recordError(fmt.Errorf("receive gps info: %w", err))
				break
			}

			gps := res.GetGpsInfo()
			s.mu.Lock()
			s.snapshot.GPSFix = gpsFixString(gps.GetFixType())
			s.snapshot.SatellitesVisible = int(gps.GetNumSatellites())
			s.updatedAt = time.Now().UTC()
			s.mu.Unlock()
		}

		time.Sleep(px4StreamRetryDelay)
	}
}

func (s *PX4Source) streamHeading() {
	for {
		stream, err := s.telemetry.SubscribeHeading(context.Background(), &telemetrypb.SubscribeHeadingRequest{})
		if err != nil {
			s.recordError(fmt.Errorf("subscribe heading: %w", err))
			time.Sleep(px4StreamRetryDelay)
			continue
		}

		for {
			res, err := stream.Recv()
			if err != nil {
				s.recordError(fmt.Errorf("receive heading: %w", err))
				break
			}

			heading := res.GetHeadingDeg().GetHeadingDeg()
			if !finite(heading) {
				continue
			}

			s.mu.Lock()
			s.snapshot.HeadingDeg = heading
			s.updatedAt = time.Now().UTC()
			s.mu.Unlock()
		}

		time.Sleep(px4StreamRetryDelay)
	}
}

func (s *PX4Source) streamAltitude() {
	for {
		stream, err := s.telemetry.SubscribeAltitude(context.Background(), &telemetrypb.SubscribeAltitudeRequest{})
		if err != nil {
			s.recordError(fmt.Errorf("subscribe altitude: %w", err))
			time.Sleep(px4StreamRetryDelay)
			continue
		}

		for {
			res, err := stream.Recv()
			if err != nil {
				s.recordError(fmt.Errorf("receive altitude: %w", err))
				break
			}

			relativeAltitude := float64(res.GetAltitude().GetAltitudeRelativeM())
			if !finite(relativeAltitude) {
				continue
			}

			s.mu.Lock()
			s.snapshot.RelativeAltitudeM = relativeAltitude
			s.updatedAt = time.Now().UTC()
			s.mu.Unlock()
		}

		time.Sleep(px4StreamRetryDelay)
	}
}

func (s *PX4Source) streamHealth() {
	for {
		stream, err := s.telemetry.SubscribeHealth(context.Background(), &telemetrypb.SubscribeHealthRequest{})
		if err != nil {
			s.recordError(fmt.Errorf("subscribe health: %w", err))
			time.Sleep(px4StreamRetryDelay)
			continue
		}

		for {
			res, err := stream.Recv()
			if err != nil {
				s.recordError(fmt.Errorf("receive health: %w", err))
				break
			}

			s.mu.Lock()
			s.snapshot.HomePositionSet = res.GetHealth().GetIsHomePositionOk()
			s.updatedAt = time.Now().UTC()
			s.mu.Unlock()
		}

		time.Sleep(px4StreamRetryDelay)
	}
}

func (s *PX4Source) recordError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.lastErr = err
}

func (s *PX4Source) readinessError(message string) error {
	if s.lastErr == nil {
		return errors.New(message)
	}

	return fmt.Errorf("%s: %w", message, s.lastErr)
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func clamp(value float64, min float64, max float64) float64 {
	if value < min {
		return min
	}

	if value > max {
		return max
	}

	return value
}

func flightModeString(mode telemetrypb.FlightMode) string {
	switch mode {
	case telemetrypb.FlightMode_FLIGHT_MODE_READY:
		return "READY"
	case telemetrypb.FlightMode_FLIGHT_MODE_TAKEOFF:
		return "TAKEOFF"
	case telemetrypb.FlightMode_FLIGHT_MODE_HOLD:
		return "HOLD"
	case telemetrypb.FlightMode_FLIGHT_MODE_MISSION:
		return "MISSION"
	case telemetrypb.FlightMode_FLIGHT_MODE_RETURN_TO_LAUNCH:
		return "RETURN_TO_LAUNCH"
	case telemetrypb.FlightMode_FLIGHT_MODE_LAND:
		return "LAND"
	case telemetrypb.FlightMode_FLIGHT_MODE_OFFBOARD:
		return "OFFBOARD"
	case telemetrypb.FlightMode_FLIGHT_MODE_FOLLOW_ME:
		return "FOLLOW_ME"
	case telemetrypb.FlightMode_FLIGHT_MODE_MANUAL:
		return "MANUAL"
	case telemetrypb.FlightMode_FLIGHT_MODE_ALTCTL:
		return "ALTCTL"
	case telemetrypb.FlightMode_FLIGHT_MODE_POSCTL:
		return "POSCTL"
	case telemetrypb.FlightMode_FLIGHT_MODE_ACRO:
		return "ACRO"
	case telemetrypb.FlightMode_FLIGHT_MODE_STABILIZED:
		return "STABILIZED"
	case telemetrypb.FlightMode_FLIGHT_MODE_RATTITUDE:
		return "RATTITUDE"
	default:
		return "UNKNOWN"
	}
}

func gpsFixString(fix telemetrypb.FixType) string {
	switch fix {
	case telemetrypb.FixType_FIX_TYPE_NO_GPS:
		return "NO_GPS"
	case telemetrypb.FixType_FIX_TYPE_NO_FIX:
		return "NO_FIX"
	case telemetrypb.FixType_FIX_TYPE_FIX_2D:
		return "2D"
	case telemetrypb.FixType_FIX_TYPE_FIX_3D:
		return "3D"
	case telemetrypb.FixType_FIX_TYPE_FIX_DGPS:
		return "DGPS"
	case telemetrypb.FixType_FIX_TYPE_RTK_FLOAT:
		return "RTK_FLOAT"
	case telemetrypb.FixType_FIX_TYPE_RTK_FIXED:
		return "RTK_FIXED"
	default:
		return "UNKNOWN"
	}
}
