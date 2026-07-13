// Package mavsdk adapts mavsdk_server's streaming gRPC API into Atlas telemetry.
package mavsdk

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	corepb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/core"
	telemetrypb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/telemetry"
	"github.com/sunnyside/atlas/atlas-agent/internal/telemetry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	streamRetryDelay = 2 * time.Second
	mavsdkRateHz     = 2.0
)

type source struct {
	logger    *slog.Logger
	conn      *grpc.ClientConn
	core      corepb.CoreServiceClient
	telemetry telemetrypb.TelemetryServiceClient

	mu              sync.RWMutex
	snapshot        telemetry.Snapshot
	connectionKnown bool
	connected       bool
	batteries       map[uint32]telemetry.Battery
}

type Outputs struct {
	Snapshots   <-chan telemetry.Snapshot
	StatusTexts <-chan telemetry.StatusTextEvent
}

// Start begins the MAVSDK subscriptions and returns a latest-only stream at the
// requested publish interval. A slow ground-station link never queues stale
// flight data.
func Start(ctx context.Context, logger *slog.Logger, address string, publishInterval time.Duration) (Outputs, error) {
	if address == "" {
		return Outputs{}, errors.New("MAVSDK gRPC address is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if publishInterval <= 0 {
		publishInterval = time.Second
	}
	conn, err := grpc.NewClient(address, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return Outputs{}, err
	}
	s := &source{
		logger:    logger,
		conn:      conn,
		core:      corepb.NewCoreServiceClient(conn),
		telemetry: telemetrypb.NewTelemetryServiceClient(conn),
		snapshot: telemetry.Snapshot{
			Source: "mavsdk",
		},
		batteries: make(map[uint32]telemetry.Battery),
	}

	go s.bestEffortSetRates(ctx)
	go s.streamConnectionState(ctx)
	go s.streamPosition(ctx)
	go s.streamBattery(ctx)
	go s.streamFlightMode(ctx)
	go s.streamArmed(ctx)
	go s.streamInAir(ctx)
	go s.streamGPSInfo(ctx)
	go s.streamHeading(ctx)
	go s.streamVelocity(ctx)
	go s.streamAltitude(ctx)
	go s.streamHealth(ctx)
	go s.streamLandedState(ctx)
	go s.streamRCStatus(ctx)
	go s.streamHome(ctx)
	go s.streamRawGPS(ctx)

	updates := make(chan telemetry.Snapshot, 1)
	statusTexts := make(chan telemetry.StatusTextEvent, 64)
	go s.publish(ctx, updates, publishInterval)
	go s.streamStatusText(ctx, statusTexts)
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()
	return Outputs{Snapshots: updates, StatusTexts: statusTexts}, nil
}

func (s *source) publish(ctx context.Context, updates chan telemetry.Snapshot, interval time.Duration) {
	defer close(updates)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var lastPublished time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snapshot, ready := s.current()
			if !ready || !snapshot.ObservedAt.After(lastPublished) {
				continue
			}
			lastPublished = snapshot.ObservedAt
			emitLatest(updates, snapshot)
		}
	}
}

func (s *source) current() (telemetry.Snapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ready := !s.snapshot.ObservedAt.IsZero() && (!s.connectionKnown || s.connected)
	return s.snapshot, ready
}

func (s *source) update(apply func(*telemetry.Snapshot)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	apply(&s.snapshot)
	s.snapshot.ObservedAt = time.Now().UTC()
}

func emitLatest(updates chan telemetry.Snapshot, snapshot telemetry.Snapshot) {
	select {
	case updates <- snapshot:
		return
	default:
	}
	select {
	case <-updates:
	default:
	}
	select {
	case updates <- snapshot:
	default:
	}
}

func (s *source) bestEffortSetRates(ctx context.Context) {
	rateCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, _ = s.telemetry.SetRatePosition(rateCtx, &telemetrypb.SetRatePositionRequest{RateHz: mavsdkRateHz})
	_, _ = s.telemetry.SetRateBattery(rateCtx, &telemetrypb.SetRateBatteryRequest{RateHz: mavsdkRateHz})
	_, _ = s.telemetry.SetRateGpsInfo(rateCtx, &telemetrypb.SetRateGpsInfoRequest{RateHz: mavsdkRateHz})
	_, _ = s.telemetry.SetRateInAir(rateCtx, &telemetrypb.SetRateInAirRequest{RateHz: mavsdkRateHz})
	_, _ = s.telemetry.SetRateAltitude(rateCtx, &telemetrypb.SetRateAltitudeRequest{RateHz: mavsdkRateHz})
	_, _ = s.telemetry.SetRateVelocityNed(rateCtx, &telemetrypb.SetRateVelocityNedRequest{RateHz: mavsdkRateHz})
	_, _ = s.telemetry.SetRateHealth(rateCtx, &telemetrypb.SetRateHealthRequest{RateHz: mavsdkRateHz})
	_, _ = s.telemetry.SetRateLandedState(rateCtx, &telemetrypb.SetRateLandedStateRequest{RateHz: mavsdkRateHz})
	_, _ = s.telemetry.SetRateRcStatus(rateCtx, &telemetrypb.SetRateRcStatusRequest{RateHz: mavsdkRateHz})
	_, _ = s.telemetry.SetRateHome(rateCtx, &telemetrypb.SetRateHomeRequest{RateHz: mavsdkRateHz})
	_, _ = s.telemetry.SetRateRawGps(rateCtx, &telemetrypb.SetRateRawGpsRequest{RateHz: mavsdkRateHz})
}

func (s *source) streamConnectionState(ctx context.Context) {
	for ctx.Err() == nil {
		stream, err := s.core.SubscribeConnectionState(ctx, &corepb.SubscribeConnectionStateRequest{})
		if err != nil {
			s.retry(ctx, "connection state", err)
			continue
		}
		for ctx.Err() == nil {
			response, err := stream.Recv()
			if err != nil {
				break
			}
			s.mu.Lock()
			s.connectionKnown = true
			s.connected = response.GetConnectionState().GetIsConnected()
			s.mu.Unlock()
		}
		sleepOrDone(ctx, streamRetryDelay)
	}
}

func (s *source) streamPosition(ctx context.Context) {
	for ctx.Err() == nil {
		stream, err := s.telemetry.SubscribePosition(ctx, &telemetrypb.SubscribePositionRequest{})
		if err != nil {
			s.retry(ctx, "position", err)
			continue
		}
		for ctx.Err() == nil {
			response, err := stream.Recv()
			if err != nil {
				break
			}
			position := response.GetPosition()
			s.update(func(snapshot *telemetry.Snapshot) {
				if latitude := position.GetLatitudeDeg(); finite(latitude) && latitude >= -90 && latitude <= 90 {
					snapshot.Latitude = pointer(latitude)
				}
				if longitude := position.GetLongitudeDeg(); finite(longitude) && longitude >= -180 && longitude <= 180 {
					snapshot.Longitude = pointer(longitude)
				}
				if altitude := float64(position.GetRelativeAltitudeM()); finite(altitude) {
					snapshot.RelativeAltitudeM = pointer(altitude)
				}
				if altitude := float64(position.GetAbsoluteAltitudeM()); finite(altitude) {
					snapshot.AbsoluteAltitudeM = pointer(altitude)
				}
				setHomeFromCurrentPosition(snapshot)
			})
		}
		sleepOrDone(ctx, streamRetryDelay)
	}
}

func (s *source) streamBattery(ctx context.Context) {
	for ctx.Err() == nil {
		stream, err := s.telemetry.SubscribeBattery(ctx, &telemetrypb.SubscribeBatteryRequest{})
		if err != nil {
			s.retry(ctx, "battery", err)
			continue
		}
		for ctx.Err() == nil {
			response, err := stream.Recv()
			if err != nil {
				break
			}
			battery := response.GetBattery()
			value := telemetry.Battery{
				ID:               battery.GetId(),
				Function:         batteryFunctionString(battery.GetBatteryFunction()),
				RemainingPercent: boundedPointer(float64(battery.GetRemainingPercent()), 0, 100),
				VoltageV:         nonNegativePointer(float64(battery.GetVoltageV())),
				CurrentA:         finitePointer(float64(battery.GetCurrentBatteryA())),
				TemperatureC:     finitePointer(float64(battery.GetTemperatureDegc())),
				ConsumedAH:       nonNegativePointer(float64(battery.GetCapacityConsumedAh())),
				TimeRemainingS:   nonNegativePointer(float64(battery.GetTimeRemainingS())),
			}
			s.update(func(snapshot *telemetry.Snapshot) {
				s.batteries[value.ID] = value
				snapshot.Batteries = sortedBatteries(s.batteries)
				if primary := primaryBattery(snapshot.Batteries); primary != nil {
					snapshot.BatteryPercent = primary.RemainingPercent
				}
			})
		}
		sleepOrDone(ctx, streamRetryDelay)
	}
}

func (s *source) streamFlightMode(ctx context.Context) {
	for ctx.Err() == nil {
		stream, err := s.telemetry.SubscribeFlightMode(ctx, &telemetrypb.SubscribeFlightModeRequest{})
		if err != nil {
			s.retry(ctx, "flight mode", err)
			continue
		}
		for ctx.Err() == nil {
			response, err := stream.Recv()
			if err != nil {
				break
			}
			mode := flightModeString(response.GetFlightMode())
			s.update(func(snapshot *telemetry.Snapshot) { snapshot.FlightMode = pointer(mode) })
		}
		sleepOrDone(ctx, streamRetryDelay)
	}
}

func (s *source) streamArmed(ctx context.Context) {
	for ctx.Err() == nil {
		stream, err := s.telemetry.SubscribeArmed(ctx, &telemetrypb.SubscribeArmedRequest{})
		if err != nil {
			s.retry(ctx, "armed state", err)
			continue
		}
		for ctx.Err() == nil {
			response, err := stream.Recv()
			if err != nil {
				break
			}
			armed := response.GetIsArmed()
			s.update(func(snapshot *telemetry.Snapshot) { snapshot.Armed = pointer(armed) })
		}
		sleepOrDone(ctx, streamRetryDelay)
	}
}

func (s *source) streamInAir(ctx context.Context) {
	for ctx.Err() == nil {
		stream, err := s.telemetry.SubscribeInAir(ctx, &telemetrypb.SubscribeInAirRequest{})
		if err != nil {
			s.retry(ctx, "in-air state", err)
			continue
		}
		for ctx.Err() == nil {
			response, err := stream.Recv()
			if err != nil {
				break
			}
			inAir := response.GetIsInAir()
			s.update(func(snapshot *telemetry.Snapshot) { snapshot.InAir = pointer(inAir) })
		}
		sleepOrDone(ctx, streamRetryDelay)
	}
}

func (s *source) streamGPSInfo(ctx context.Context) {
	for ctx.Err() == nil {
		stream, err := s.telemetry.SubscribeGpsInfo(ctx, &telemetrypb.SubscribeGpsInfoRequest{})
		if err != nil {
			s.retry(ctx, "GPS info", err)
			continue
		}
		for ctx.Err() == nil {
			response, err := stream.Recv()
			if err != nil {
				break
			}
			gps := response.GetGpsInfo()
			fix := gpsFixString(gps.GetFixType())
			satellites := uint32(max(gps.GetNumSatellites(), 0))
			s.update(func(snapshot *telemetry.Snapshot) {
				snapshot.GPSFix = pointer(fix)
				snapshot.SatellitesVisible = pointer(satellites)
			})
		}
		sleepOrDone(ctx, streamRetryDelay)
	}
}

func (s *source) streamHeading(ctx context.Context) {
	for ctx.Err() == nil {
		stream, err := s.telemetry.SubscribeHeading(ctx, &telemetrypb.SubscribeHeadingRequest{})
		if err != nil {
			s.retry(ctx, "heading", err)
			continue
		}
		for ctx.Err() == nil {
			response, err := stream.Recv()
			if err != nil {
				break
			}
			heading := response.GetHeadingDeg().GetHeadingDeg()
			if finite(heading) && heading >= 0 && heading <= 360 {
				s.update(func(snapshot *telemetry.Snapshot) { snapshot.HeadingDeg = pointer(heading) })
			}
		}
		sleepOrDone(ctx, streamRetryDelay)
	}
}

func (s *source) streamVelocity(ctx context.Context) {
	for ctx.Err() == nil {
		stream, err := s.telemetry.SubscribeVelocityNed(ctx, &telemetrypb.SubscribeVelocityNedRequest{})
		if err != nil {
			s.retry(ctx, "velocity", err)
			continue
		}
		for ctx.Err() == nil {
			response, err := stream.Recv()
			if err != nil {
				break
			}
			velocity := response.GetVelocityNed()
			north, east, down := float64(velocity.GetNorthMS()), float64(velocity.GetEastMS()), float64(velocity.GetDownMS())
			s.update(func(snapshot *telemetry.Snapshot) {
				if finite(north) && finite(east) {
					snapshot.GroundSpeedMPS = pointer(math.Hypot(north, east))
				}
				if finite(north) {
					snapshot.VelocityNorthMPS = pointer(north)
				}
				if finite(east) {
					snapshot.VelocityEastMPS = pointer(east)
				}
				if finite(down) {
					snapshot.VelocityDownMPS = pointer(down)
					snapshot.ClimbRateMPS = pointer(-down)
				}
			})
		}
		sleepOrDone(ctx, streamRetryDelay)
	}
}

func (s *source) streamAltitude(ctx context.Context) {
	for ctx.Err() == nil {
		stream, err := s.telemetry.SubscribeAltitude(ctx, &telemetrypb.SubscribeAltitudeRequest{})
		if err != nil {
			s.retry(ctx, "altitude", err)
			continue
		}
		for ctx.Err() == nil {
			response, err := stream.Recv()
			if err != nil {
				break
			}
			altitude := float64(response.GetAltitude().GetAltitudeRelativeM())
			altitudeData := response.GetAltitude()
			s.update(func(snapshot *telemetry.Snapshot) {
				if finite(altitude) {
					snapshot.RelativeAltitudeM = pointer(altitude)
				}
				if value := float64(altitudeData.GetAltitudeAmslM()); finite(value) {
					snapshot.AbsoluteAltitudeM = pointer(value)
				}
				if value := float64(altitudeData.GetAltitudeTerrainM()); finite(value) {
					snapshot.TerrainAltitudeM = pointer(value)
				}
				if value := float64(altitudeData.GetBottomClearanceM()); finite(value) {
					snapshot.BottomClearanceM = pointer(value)
				}
			})
		}
		sleepOrDone(ctx, streamRetryDelay)
	}
}

func (s *source) streamHealth(ctx context.Context) {
	for ctx.Err() == nil {
		stream, err := s.telemetry.SubscribeHealth(ctx, &telemetrypb.SubscribeHealthRequest{})
		if err != nil {
			s.retry(ctx, "health", err)
			continue
		}
		for ctx.Err() == nil {
			response, err := stream.Recv()
			if err != nil {
				break
			}
			health := response.GetHealth()
			value := telemetry.VehicleHealth{
				GyrometerCalibrationOK:     health.GetIsGyrometerCalibrationOk(),
				AccelerometerCalibrationOK: health.GetIsAccelerometerCalibrationOk(),
				MagnetometerCalibrationOK:  health.GetIsMagnetometerCalibrationOk(),
				LocalPositionOK:            health.GetIsLocalPositionOk(),
				GlobalPositionOK:           health.GetIsGlobalPositionOk(),
				HomePositionOK:             health.GetIsHomePositionOk(),
				Armable:                    health.GetIsArmable(),
			}
			s.update(func(snapshot *telemetry.Snapshot) {
				snapshot.HomePositionSet = pointer(value.HomePositionOK)
				snapshot.Health = pointer(value)
				setHomeFromCurrentPosition(snapshot)
			})
		}
		sleepOrDone(ctx, streamRetryDelay)
	}
}

func (s *source) streamLandedState(ctx context.Context) {
	for ctx.Err() == nil {
		stream, err := s.telemetry.SubscribeLandedState(ctx, &telemetrypb.SubscribeLandedStateRequest{})
		if err != nil {
			s.retry(ctx, "landed state", err)
			continue
		}
		for ctx.Err() == nil {
			response, err := stream.Recv()
			if err != nil {
				break
			}
			state := enumLabel(response.GetLandedState().String(), "LANDED_STATE_")
			s.update(func(snapshot *telemetry.Snapshot) { snapshot.LandedState = pointer(state) })
		}
		sleepOrDone(ctx, streamRetryDelay)
	}
}

func (s *source) streamRCStatus(ctx context.Context) {
	for ctx.Err() == nil {
		stream, err := s.telemetry.SubscribeRcStatus(ctx, &telemetrypb.SubscribeRcStatusRequest{})
		if err != nil {
			s.retry(ctx, "RC status", err)
			continue
		}
		for ctx.Err() == nil {
			response, err := stream.Recv()
			if err != nil {
				break
			}
			rc := response.GetRcStatus()
			value := telemetry.RCStatus{
				Available:             rc.GetIsAvailable(),
				WasAvailableOnce:      rc.GetWasAvailableOnce(),
				SignalStrengthPercent: boundedPointer(float64(rc.GetSignalStrengthPercent()), 0, 100),
			}
			s.update(func(snapshot *telemetry.Snapshot) { snapshot.RCStatus = pointer(value) })
		}
		sleepOrDone(ctx, streamRetryDelay)
	}
}

func (s *source) streamHome(ctx context.Context) {
	for ctx.Err() == nil {
		stream, err := s.telemetry.SubscribeHome(ctx, &telemetrypb.SubscribeHomeRequest{})
		if err != nil {
			s.retry(ctx, "home position", err)
			continue
		}
		for ctx.Err() == nil {
			response, err := stream.Recv()
			if err != nil {
				break
			}
			home := response.GetHome()
			latitude := boundedPointer(home.GetLatitudeDeg(), -90, 90)
			longitude := boundedPointer(home.GetLongitudeDeg(), -180, 180)
			if latitude == nil || longitude == nil {
				continue
			}
			value := telemetry.HomePosition{
				Latitude:          latitude,
				Longitude:         longitude,
				AbsoluteAltitudeM: finitePointer(float64(home.GetAbsoluteAltitudeM())),
				RelativeAltitudeM: finitePointer(float64(home.GetRelativeAltitudeM())),
			}
			s.update(func(snapshot *telemetry.Snapshot) { snapshot.HomePosition = pointer(value) })
		}
		sleepOrDone(ctx, streamRetryDelay)
	}
}

func setHomeFromCurrentPosition(snapshot *telemetry.Snapshot) {
	if snapshot.HomePositionSet == nil || !*snapshot.HomePositionSet || validHomePosition(snapshot.HomePosition) || snapshot.Latitude == nil || snapshot.Longitude == nil {
		return
	}
	snapshot.HomePosition = &telemetry.HomePosition{
		Latitude:          pointer(*snapshot.Latitude),
		Longitude:         pointer(*snapshot.Longitude),
		AbsoluteAltitudeM: snapshot.AbsoluteAltitudeM,
		RelativeAltitudeM: pointer(0.0),
	}
}

func validHomePosition(home *telemetry.HomePosition) bool {
	return home != nil && home.Latitude != nil && home.Longitude != nil &&
		finite(*home.Latitude) && finite(*home.Longitude) &&
		*home.Latitude >= -90 && *home.Latitude <= 90 &&
		*home.Longitude >= -180 && *home.Longitude <= 180
}

func (s *source) streamRawGPS(ctx context.Context) {
	for ctx.Err() == nil {
		stream, err := s.telemetry.SubscribeRawGps(ctx, &telemetrypb.SubscribeRawGpsRequest{})
		if err != nil {
			s.retry(ctx, "raw GPS", err)
			continue
		}
		for ctx.Err() == nil {
			response, err := stream.Recv()
			if err != nil {
				break
			}
			raw := response.GetRawGps()
			value := telemetry.GPSQuality{
				HDOP:                    nonNegativePointer(float64(raw.GetHdop())),
				VDOP:                    nonNegativePointer(float64(raw.GetVdop())),
				HorizontalUncertaintyM:  nonNegativePointer(float64(raw.GetHorizontalUncertaintyM())),
				VerticalUncertaintyM:    nonNegativePointer(float64(raw.GetVerticalUncertaintyM())),
				VelocityUncertaintyMPS:  nonNegativePointer(float64(raw.GetVelocityUncertaintyMS())),
				CourseOverGroundDegrees: boundedPointer(float64(raw.GetCogDeg()), 0, 360),
			}
			s.update(func(snapshot *telemetry.Snapshot) { snapshot.GPSQuality = pointer(value) })
		}
		sleepOrDone(ctx, streamRetryDelay)
	}
}

func (s *source) streamStatusText(ctx context.Context, events chan telemetry.StatusTextEvent) {
	defer close(events)
	for ctx.Err() == nil {
		stream, err := s.telemetry.SubscribeStatusText(ctx, &telemetrypb.SubscribeStatusTextRequest{})
		if err != nil {
			s.retry(ctx, "status text", err)
			continue
		}
		for ctx.Err() == nil {
			response, err := stream.Recv()
			if err != nil {
				break
			}
			status := response.GetStatusText()
			if strings.TrimSpace(status.GetText()) == "" {
				continue
			}
			event := telemetry.StatusTextEvent{
				ObservedAt: time.Now().UTC(),
				Source:     "mavsdk",
				Severity:   enumLabel(status.GetType().String(), "STATUS_TEXT_TYPE_"),
				Text:       status.GetText(),
			}
			emitStatusText(events, event)
		}
		sleepOrDone(ctx, streamRetryDelay)
	}
}

func emitStatusText(events chan telemetry.StatusTextEvent, event telemetry.StatusTextEvent) {
	select {
	case events <- event:
		return
	default:
	}
	select {
	case <-events:
	default:
	}
	select {
	case events <- event:
	default:
	}
}

func (s *source) retry(ctx context.Context, stream string, err error) {
	s.logger.Debug("MAVSDK telemetry stream unavailable", "stream", stream, "error", err)
	sleepOrDone(ctx, streamRetryDelay)
}

func sleepOrDone(ctx context.Context, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func clamp(value, minimum, maximum float64) float64 {
	return min(max(value, minimum), maximum)
}

func finitePointer(value float64) *float64 {
	if !finite(value) {
		return nil
	}
	return pointer(value)
}

func nonNegativePointer(value float64) *float64 {
	if !finite(value) || value < 0 {
		return nil
	}
	return pointer(value)
}

func boundedPointer(value, minimum, maximum float64) *float64 {
	if !finite(value) || value < minimum || value > maximum {
		return nil
	}
	return pointer(value)
}

func sortedBatteries(values map[uint32]telemetry.Battery) []telemetry.Battery {
	batteries := make([]telemetry.Battery, 0, len(values))
	for _, battery := range values {
		batteries = append(batteries, battery)
	}
	sort.Slice(batteries, func(i, j int) bool { return batteries[i].ID < batteries[j].ID })
	return batteries
}

func primaryBattery(batteries []telemetry.Battery) *telemetry.Battery {
	for i := range batteries {
		if batteries[i].Function == "ALL" || batteries[i].Function == "PROPULSION" {
			return &batteries[i]
		}
	}
	if len(batteries) == 0 {
		return nil
	}
	return &batteries[0]
}

func pointer[T any](value T) *T { return &value }

func enumLabel(value, prefix string) string {
	label := strings.TrimPrefix(value, prefix)
	if label == value {
		return "UNKNOWN"
	}
	return label
}

func batteryFunctionString(function telemetrypb.BatteryFunction) string {
	return enumLabel(function.String(), "BATTERY_FUNCTION_")
}

func flightModeString(mode telemetrypb.FlightMode) string {
	name := strings.TrimPrefix(mode.String(), "FLIGHT_MODE_")
	if name != mode.String() {
		return name
	}
	return "UNKNOWN"
}

func gpsFixString(fix telemetrypb.FixType) string {
	name := strings.TrimPrefix(fix.String(), "FIX_TYPE_")
	name = strings.TrimPrefix(name, "FIX_")
	if name != fix.String() {
		return name
	}
	return "UNKNOWN"
}
