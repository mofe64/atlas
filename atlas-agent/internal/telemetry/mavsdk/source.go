// Package mavsdk adapts mavsdk_server's streaming gRPC API into Atlas telemetry snapshots.
// A snapshot is the latest known value of every field, not a set of fields measured simultaneously.
// ObservedAt is the time of the latest snapshot mutation.
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

	"github.com/sunnyside/atlas/atlas-agent/internal/geolocation"
	corepb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/core"
	telemetrypb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/telemetry"
	"github.com/sunnyside/atlas/atlas-agent/internal/telemetry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// notes:
// corepb: MAVSDK’s connection-state service.
// telemetrypb: MAVSDK’s telemetry subscriptions and rate-setting RPCs.
// both are generated from MAVSDK's protobuf definitions.

// Note: the rates we use are the rates at which we measure or request values from MAVSDK.
// The atlas agent uses the ATLAS_TELEMETRY_INTERNAL env var to control the rate at which we send the telem to the ground station.
// its default is 1 second
// so we collect all the values we need at the highest rate and then send the snapshot to the ground station at the slower rate.
const (
	streamRetryDelay       = 2 * time.Second  // time to wait before retrying a failed stream
	mavsdkRateHz           = 2.0              // rate at which we request telemetry from MAVSDK (twice per second)
	aircraftAttitudeRateHz = 30.0             // rate at which we request aircraft rotation from MAVSDK (30 times per second)
	aircraftPositionRateHz = 10.0             // rate at which we request position from MAVSDK (10 times per second)
	aircraftVelocityRateHz = 20.0             // rate at which we request velocity from MAVSDK (20 times per second)
	autopilotTimeRateHz    = 2.0              // rate at which we read the autopilot clock (twice per second), we do this so that we can align timestamps
	rateRefreshInterval    = 30 * time.Second // we refresh the our requested rates every 30 seconds, so that if mavsdk restarts or reconnect, we resync our rates requests
)

// aircraftPoseState is the temporary navigation state used to construct high-rate geolocation poses
// it holds the latest
// - lat and long
// - absolute and relative altitude
// - ned velocity
// - px4 position health flags
// - GPS uncertainty
// - Monotonic recieve times for position and velocity
// the Valid booleans are used to distinguish zero values, from values that have not yet been received.
// our recieve timestamps are based on the companion computer's monotonic clock.
// a monotonic clock advances steadily forward and is not affected by NTP or wall clocl correcttions, thats why its perfect for a
// age and interpolation.
type aircraftPoseState struct {
	positionReceivedMonotonicNS int64
	velocityReceivedMonotonicNS int64
	latitudeDeg                 float64
	longitudeDeg                float64
	absoluteAltitudeM           float64
	relativeAltitudeM           float64
	positionValid               bool
	velocityNEDMPS              geolocation.Vector3
	velocityValid               bool
	health                      telemetry.VehicleHealth
	healthValid                 bool
	gpsQuality                  telemetry.GPSQuality
	gpsQualityValid             bool
}

// Source is our internal runtime object holding our state
// its fields fall into four groups:
//
//   - mavsdk infra:
//     -- logger: structured diagnostic logging.
//     -- conn: shared gRPC connection.
//     -- core: generated Core service client.
//     -- telemetry: generated Telemetry service client.
//
//   - Operator snapshot state:
//     -- mu: guards all fields below it that are concurrently accessed.
//     -- snapshot: aggregate latest-known Atlas telemetry.
//     -- connectionKnown: whether Core has ever reported a connection state.
//     -- connected: most recently reported MAVSDK vehicle connection state.
//     -- batteries: battery state indexed by MAVSDK battery ID.
//
//   - Geolocation state:
//     -- pose: latest position, velocity, health, and quality used when attitude arrives.
//     -- geolocation: optional bounded temporal foundation.
//
//   - Methods with receiver (s *source) operate on this shared runtime object.
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
	pose            aircraftPoseState
	geolocation     *geolocation.Foundation
}

type Outputs struct {
	// the channel types are receive-only. Callers can read them but cannot send into or close them.
	Snapshots   <-chan telemetry.Snapshot         // periodic operator facing telemetry sent to the atlas ground station capacity is 1, old snapshots are dropped
	StatusTexts <-chan telemetry.StatusTextEvent  // discrete px4 messages such as warnings and errors capacity is 64, old messages are dropped
	Latest      func() (telemetry.Snapshot, bool) // used for synchronous access to the latest telem snapshot for onboard safety logic
	//Latest is used by the aircraft-follow controller. That controller independently checks freshness, arm/in-air state, position health, battery, altitude, and geofence boundaries in aircraft_follow.go
}

// Start begins the MAVSDK telem subscriptions without geolocation data.
// the high rate geolocation pipelien is disabled, since we pass in nil for the geolocation foundation.
func Start(ctx context.Context, logger *slog.Logger, address string, publishInterval time.Duration) (Outputs, error) {
	return StartWithGeolocation(ctx, logger, address, publishInterval, nil)
}

// StartWithGeolocation creates our grpc client connection to mavsdk_server
// it also creats the macsdk core and telemetry service clients.
// it initialises our source runtime object and starts the MAVSDK telem subscriptions.
// it also creates and starts the snapshot publisher and starys event stream
// Snapshot publisher: periodically reads the latest combined aircraft telemetry—position, battery, flight mode, etc.
// and sends it through Outputs.Snapshots. It only keeps the newest pending snapshot.
// Status-event stream: continuously receives individual PX4 messages—warnings, errors, mission notices—and sends them through Outputs.StatusTexts.
// clients can then read the telem snapshot and status events from these channels.
// when the geolocation foundation is not nil, it also starts the high rate geolocation pipeline.
func StartWithGeolocation(ctx context.Context, logger *slog.Logger, address string, publishInterval time.Duration, foundation *geolocation.Foundation) (Outputs, error) {
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
		batteries:   make(map[uint32]telemetry.Battery),
		geolocation: foundation,
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
	if foundation != nil {
		// high rate geolocation pipeline
		// if the geolocation foundation is not nill, we start
		// timestamoed attitude quaternions and
		// autopilot unix time streams.
		go s.streamAttitudeQuaternion(ctx)
		go s.streamUnixEpochTime(ctx)
	}

	updates := make(chan telemetry.Snapshot, 1)
	statusTexts := make(chan telemetry.StatusTextEvent, 64)
	go s.publish(ctx, updates, publishInterval)
	go s.streamStatusText(ctx, statusTexts)
	go func() {
		// when the context is done, we close the grpc connection.
		<-ctx.Done()
		_ = conn.Close()
	}()
	return Outputs{Snapshots: updates, StatusTexts: statusTexts, Latest: s.current}, nil
}

// publish converts continuously changing snapshot state, into periodic operator facing telemetry snapshots.
// it creates a ticker at the requested publication interval.
// waits for the ticker to fire, and then calls current to get the latest snapshot.
// skips publication if the snapshot is not ready or if it is older than the last published snapshot.
// it uses emitLatest to send the snapshot to the updates channel.
// closes the output channel on shutdown.
func (s *source) publish(ctx context.Context, updates chan telemetry.Snapshot, interval time.Duration) {
	defer close(updates)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var lastPublished time.Time
	// loop until the context is done.
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

// current returns the latest snapshot and a bool indicating if it is ready to be published.
// it is ready if:
// - At least one update has set ObservedAt.
// - and either:
// - MAVSDK connection state has not been reported yet,
// - It has been reported and the vehicle is connected.
// it acuires a read lock on the mutex to prevent concurrent updates.
// and returns a copy of the snapshot, plus the ready flag.
// Allowing publication before connectionKnown prevents startup from being blocked if telemetry arrives before the Core connection-state stream.
// Once a disconnection is explicitly reported, current returns ready=false. The snapshot remains stored; it is not erased.
// The copy we return is shallow copy, and the pointer fields and batery slics refer to the same underlying data.
func (s *source) current() (telemetry.Snapshot, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ready := !s.snapshot.ObservedAt.IsZero() && (!s.connectionKnown || s.connected)
	return s.snapshot, ready
}

// update is a helper function that updates the snapshot and sets the observed at time.
// every telem goroutine needs to update the same snapshot obejct.
// update centalises the locking procedure for them to update the snapshot.
// 1. acquire the exclusive mutex lock
// 2. invoke our mutation function (apply)
// 3. set observedAt to the current UTC wall time
// 4. release the mutex lock
// this prevents paritally written state and data racess.
// the closure approach lets each stream update one or several realated field atomically.
// the closure here is the anon function that is passed to update,
// so each stream creats an anon function describing the changes it wants and then passes it to update.
// update then carries out our locking procedure and invokes the closure.
// its called a closure because the anon function captures the variables
// from its surrounding scope (the stream defining it)
// and the closure applies the captured values to the snapshot.
func (s *source) update(apply func(*telemetry.Snapshot)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	apply(&s.snapshot)
	s.snapshot.ObservedAt = time.Now().UTC()
}

func emitLatest(updates chan telemetry.Snapshot, snapshot telemetry.Snapshot) {
	// our updates channel has a buffer of 1, so we can only have one pending snapshot.
	select {
	// we first check to see if we can send the snapshot to the channel.
	// if we can, we send it and return.
	// we do this check in a select statement to avoid blocking via the default branch.
	case updates <- snapshot:
		return
	// if the channel is full, we execute our default branch.
	// this prevents us from blocking until the the channel has room for our snapshot.
	// if we didn't have the default branch, we would block here until the channel has room for our snapshot.
	default:
	}

	// if we get here, the channel is full, so we need to drop the oldest snapshot.
	// we do this be recieving from the channel and discarding the value.
	// we do this in a select statment becuase outside the select, we could block if the channel is empty.
	// so we do it in a select with a default branch to avoid blocking.
	// especially during concurrent operations.
	// The default handles concurrency safely. Between the first and second select,
	// the ground-station goroutine might consume the old snapshot itself.
	// In that case the channel is already empty,
	// so this block simply continues instead of waiting for a value
	select {
	case <-updates:
	default:
	}

	// finally we insert the latest snspshot into the channel.
	// The send is non-blocking again. In the current architecture there is normally only one snapshot publisher,
	//  so this send should succeed after the old value has been removed.
	// The default is defensive: it guarantees that this function never blocks even if
	// channel usage changes or another producer fills it concurrently in the future.
	select {
	case updates <- snapshot:
	default:
	}
}

func (s *source) bestEffortSetRates(ctx context.Context) {
	for ctx.Err() == nil {
		s.setRatesOnce(ctx)
		timer := time.NewTimer(rateRefreshInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *source) setRatesOnce(ctx context.Context) {
	rateCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	positionRate, velocityRate := mavsdkRateHz, mavsdkRateHz
	if s.geolocation != nil {
		positionRate, velocityRate = aircraftPositionRateHz, aircraftVelocityRateHz
		response, err := s.telemetry.SetRateAttitudeQuaternion(rateCtx, &telemetrypb.SetRateAttitudeQuaternionRequest{RateHz: aircraftAttitudeRateHz})
		if err != nil || response.GetTelemetryResult().GetResult() != telemetrypb.TelemetryResult_RESULT_SUCCESS {
			s.logger.Debug("high-rate aircraft attitude request was not accepted", "rate_hz", aircraftAttitudeRateHz, "error", err)
		}
		_, _ = s.telemetry.SetRateUnixEpochTime(rateCtx, &telemetrypb.SetRateUnixEpochTimeRequest{RateHz: autopilotTimeRateHz})
	}
	positionResponse, positionErr := s.telemetry.SetRatePosition(rateCtx, &telemetrypb.SetRatePositionRequest{RateHz: positionRate})
	_, _ = s.telemetry.SetRateBattery(rateCtx, &telemetrypb.SetRateBatteryRequest{RateHz: mavsdkRateHz})
	_, _ = s.telemetry.SetRateGpsInfo(rateCtx, &telemetrypb.SetRateGpsInfoRequest{RateHz: mavsdkRateHz})
	_, _ = s.telemetry.SetRateInAir(rateCtx, &telemetrypb.SetRateInAirRequest{RateHz: mavsdkRateHz})
	_, _ = s.telemetry.SetRateAltitude(rateCtx, &telemetrypb.SetRateAltitudeRequest{RateHz: mavsdkRateHz})
	velocityResponse, velocityErr := s.telemetry.SetRateVelocityNed(rateCtx, &telemetrypb.SetRateVelocityNedRequest{RateHz: velocityRate})
	_, _ = s.telemetry.SetRateHealth(rateCtx, &telemetrypb.SetRateHealthRequest{RateHz: 5})
	_, _ = s.telemetry.SetRateLandedState(rateCtx, &telemetrypb.SetRateLandedStateRequest{RateHz: mavsdkRateHz})
	_, _ = s.telemetry.SetRateRcStatus(rateCtx, &telemetrypb.SetRateRcStatusRequest{RateHz: mavsdkRateHz})
	_, _ = s.telemetry.SetRateHome(rateCtx, &telemetrypb.SetRateHomeRequest{RateHz: mavsdkRateHz})
	_, _ = s.telemetry.SetRateRawGps(rateCtx, &telemetrypb.SetRateRawGpsRequest{RateHz: mavsdkRateHz})
	if s.geolocation != nil {
		if positionErr != nil || positionResponse.GetTelemetryResult().GetResult() != telemetrypb.TelemetryResult_RESULT_SUCCESS {
			s.logger.Debug("high-rate aircraft position request was not accepted", "rate_hz", positionRate, "error", positionErr)
		}
		if velocityErr != nil || velocityResponse.GetTelemetryResult().GetResult() != telemetrypb.TelemetryResult_RESULT_SUCCESS {
			s.logger.Debug("high-rate aircraft velocity request was not accepted", "rate_hz", velocityRate, "error", velocityErr)
		}
	}
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
			received := geolocation.Now()
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
				latitude, longitude := position.GetLatitudeDeg(), position.GetLongitudeDeg()
				absolute, relative := float64(position.GetAbsoluteAltitudeM()), float64(position.GetRelativeAltitudeM())
				if latitude >= -90 && latitude <= 90 && longitude >= -180 && longitude <= 180 && finite(latitude) && finite(longitude) && finite(absolute) && finite(relative) {
					s.pose.latitudeDeg = latitude
					s.pose.longitudeDeg = longitude
					s.pose.absoluteAltitudeM = absolute
					s.pose.relativeAltitudeM = relative
					s.pose.positionReceivedMonotonicNS = received.MonotonicNS
					s.pose.positionValid = true
				}
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
			received := geolocation.Now()
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
				if finite(north) && finite(east) && finite(down) {
					s.pose.velocityNEDMPS = geolocation.Vector3{X: north, Y: east, Z: down}
					s.pose.velocityReceivedMonotonicNS = received.MonotonicNS
					s.pose.velocityValid = true
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
				s.pose.health = value
				s.pose.healthValid = true
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
			s.update(func(snapshot *telemetry.Snapshot) {
				snapshot.GPSQuality = pointer(value)
				s.pose.gpsQuality = value
				s.pose.gpsQualityValid = true
			})
		}
		sleepOrDone(ctx, streamRetryDelay)
	}
}

func (s *source) streamAttitudeQuaternion(ctx context.Context) {
	for ctx.Err() == nil {
		stream, err := s.telemetry.SubscribeAttitudeQuaternion(ctx, &telemetrypb.SubscribeAttitudeQuaternionRequest{})
		if err != nil {
			s.retry(ctx, "attitude quaternion", err)
			continue
		}
		for ctx.Err() == nil {
			response, err := stream.Recv()
			if err != nil {
				break
			}
			attitude := response.GetAttitudeQuaternion()
			received := geolocation.Now()
			s.recordAircraftPose(attitude, received)
		}
		sleepOrDone(ctx, streamRetryDelay)
	}
}

func (s *source) recordAircraftPose(attitude *telemetrypb.Quaternion, received geolocation.CompanionTime) {
	if attitude == nil || attitude.GetTimestampUs() == 0 || s.geolocation == nil {
		if s.geolocation != nil {
			s.geolocation.DropAircraftPose()
		}
		return
	}
	s.mu.RLock()
	state := s.pose
	s.mu.RUnlock()
	if !state.positionValid {
		s.geolocation.DropAircraftPose()
		return
	}
	quality := geolocation.PoseQuality{
		GlobalPositionOK: state.healthValid && state.health.GlobalPositionOK,
		LocalPositionOK:  state.healthValid && state.health.LocalPositionOK,
		VelocityValid:    state.velocityValid,
		PositionAge:      durationBetweenMonotonic(received.MonotonicNS, state.positionReceivedMonotonicNS),
		VelocityAge:      durationBetweenMonotonic(received.MonotonicNS, state.velocityReceivedMonotonicNS),
	}
	if state.gpsQualityValid {
		quality.HorizontalUncertaintyM = pointerValue(state.gpsQuality.HorizontalUncertaintyM)
		quality.VerticalUncertaintyM = pointerValue(state.gpsQuality.VerticalUncertaintyM)
		quality.VelocityUncertaintyMPS = pointerValue(state.gpsQuality.VelocityUncertaintyMPS)
	}
	err := s.geolocation.RecordAircraftPose(geolocation.AircraftPoseMeasurement{
		AutopilotTimestampUS: attitude.GetTimestampUs(), Received: received,
		LatitudeDeg: state.latitudeDeg, LongitudeDeg: state.longitudeDeg,
		AltitudeAMSLM: state.absoluteAltitudeM, RelativeAltitudeM: state.relativeAltitudeM,
		Attitude: geolocation.Quaternion{
			W: float64(attitude.GetW()), X: float64(attitude.GetX()),
			Y: float64(attitude.GetY()), Z: float64(attitude.GetZ()),
		},
		VelocityNEDMPS: state.velocityNEDMPS, Quality: quality,
	})
	if err != nil {
		s.geolocation.DropAircraftPose()
		s.logger.Debug("discard invalid geolocation aircraft pose", "error", err)
	}
}

func (s *source) streamUnixEpochTime(ctx context.Context) {
	for ctx.Err() == nil {
		stream, err := s.telemetry.SubscribeUnixEpochTime(ctx, &telemetrypb.SubscribeUnixEpochTimeRequest{})
		if err != nil {
			s.retry(ctx, "unix epoch time", err)
			continue
		}
		for ctx.Err() == nil {
			response, err := stream.Recv()
			if err != nil {
				break
			}
			if err := s.geolocation.ObserveAutopilotUnixTime(response.GetTimeUs(), geolocation.Now()); err != nil {
				s.logger.Debug("discard invalid autopilot unix time", "error", err)
			}
		}
		sleepOrDone(ctx, streamRetryDelay)
	}
}

func durationBetweenMonotonic(current, previous int64) time.Duration {
	if current <= 0 || previous <= 0 || current < previous {
		return time.Duration(math.MaxInt64)
	}
	return time.Duration(current - previous)
}

func pointerValue(value *float64) float64 {
	if value == nil || !finite(*value) || *value < 0 {
		return 0
	}
	return *value
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
