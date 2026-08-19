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

// bestEffortSetRates sets the MAVSDK telemetry rates in a best-effort manner.
// it continually calls setRatesOnce in a loop, and sleeps for the rate refresh interval.
// our wait is interruptible, so a context cancellation will stop the loop.
// the reason we continually set rates, is for resilience. after a mavsdk or px4 reconnection,
// previusolt requeted rates may no longer be active, so we ensure that we set them again
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

// setRatesOnce use a 5 sec context to set the subscription rates for the telemetry streams.
// we do a best effort approach, and ignore errors.
func (s *source) setRatesOnce(ctx context.Context) {
	rateCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// 	Without geolocation,Ordinary operator telemetry does not need extremely high-frequency data:
	// Position: 2 updates per second
	// Velocity: 2 updates per second
	// Battery: 2 updates per second
	// GPS: 2 updates per second
	// Health: 5 updates per second
	// This is enough for dashboards and status displays.
	positionRate, velocityRate := mavsdkRateHz, mavsdkRateHz
	if s.geolocation != nil {
		// geolocation needs more precise timing
		// Attitude: 30 updates per second
		// Position: 10 updates per second
		// Velocity: 20 updates per second
		// This is because the system may need to answer:
		// “Where was the aircraft, and which direction was it pointing, at the exact moment this camera frame was captured?”
		//If the aircraft is moving and rotating, one measurement every half-second is too coarse.
		// A 30 Hz attitude stream gives a new orientation roughly every 33 ms.
		positionRate, velocityRate = aircraftPositionRateHz, aircraftVelocityRateHz
		response, err := s.telemetry.SetRateAttitudeQuaternion(rateCtx, &telemetrypb.SetRateAttitudeQuaternionRequest{RateHz: aircraftAttitudeRateHz})
		if err != nil || response.GetTelemetryResult().GetResult() != telemetrypb.TelemetryResult_RESULT_SUCCESS {
			s.logger.Debug("high-rate aircraft attitude request was not accepted", "rate_hz", aircraftAttitudeRateHz, "error", err)
		}
		_, _ = s.telemetry.SetRateUnixEpochTime(rateCtx, &telemetrypb.SetRateUnixEpochTimeRequest{RateHz: autopilotTimeRateHz})
	}

	// set the rates for each of our telemetry streams.
	// we ignore responses and errors here, because we are following a best-effort approach.
	// except for the position and velocity streams, which we check for errors when we are using geolocation.
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

// Note: For all our streams, we use the following pattern:
// Outer loop: Keep trying to establish a working subscription.
// Inner loop: Keep reading messages from the current subscription.
// the outer loop owns the subscription's complete lifetime. it is essentially saying
// as long as the agent is running (ctx is not done), we keep trying to establish a working subscription.
// if the subscription cannot be opened, ot there is an error, we log the problem and retry after a delay.
// the inner loop reads messages from the subscription once it exists using response, err := stream.Recv()
// it extracts the relevant data from mavsdk, validates it, updates the shared atlas snapshot, and
// calls recv() again to get the next message.
// if there is an error, in the inner loop, we break out of the loop and go back to the outer loop which
// is responsible for retrying the subscription, the outer loop will execute sleepOrDone(...)
// which will use an interruptible timer to wait for the retry delay, and then continue the outer loop.

// streamConnectionState subscribes to the connection state stream from the MAVSDK core service.
// it doesn't update the snapshot using our establised update functiomn, but instead
// directly updates the connectionKnown and connected fields.
// it also handles concurrency by acquiring and releasing the mutex lock around the updates.
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

// streamPosition subscribes to the position stream from the MAVSDK telemetry service.
// it updates the snapshot using our establised update function, with a closure that updates the snapshot fields.
// the closure captures the position data from the mavsdk response, validates it, and updates the snapshot.
// the closure is passed to the update function, which is responsible for ensuring thtat
// the snapshot is updated in a thread-safe manner.
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
			// record when we received the position update
			received := geolocation.Now()
			s.update(func(snapshot *telemetry.Snapshot) {
				// “Finite” rejects IEEE NaN, positive infinity, and negative infinity.
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
				// PX4 sends "home is set" and the aircraft position on separate streams.
				// Position (coordinates) may arrive after the "home is set" message has arrived,
				// in that case, px4 has confirmed that home exists, but we don't yet have coordinates for the actual home position
				// so we call the setHomeFromCurrentPosition function to try to build the missing home position now.
				setHomeFromCurrentPosition(snapshot)

				latitude, longitude := position.GetLatitudeDeg(), position.GetLongitudeDeg()
				absolute, relative := float64(position.GetAbsoluteAltitudeM()), float64(position.GetRelativeAltitudeM())
				// For geolocation it applies a stricter all-or-nothing check.
				// Latitude, longitude, absolute altitude, and relative altitude must all be valid
				// before pose fields are updated and pose.positionValid is set.
				if latitude >= -90 && latitude <= 90 && longitude >= -180 && longitude <= 180 && finite(latitude) && finite(longitude) && finite(absolute) && finite(relative) {
					s.pose.latitudeDeg = latitude
					s.pose.longitudeDeg = longitude
					s.pose.absoluteAltitudeM = absolute
					s.pose.relativeAltitudeM = relative
					// set the companion computer's monotonic receive time for position updates
					// this will be used to calculate how old the position update is and how long it has been since the last update.
					s.pose.positionReceivedMonotonicNS = received.MonotonicNS
					s.pose.positionValid = true
				}
			})
		}
		sleepOrDone(ctx, streamRetryDelay)
	}
}

// streamBattery subscribes to the battery stream from the MAVSDK telemetry service.
// we validate the battery information received
// Remaining percentage must be between 0 and 100.
// Voltage, consumed capacity, and remaining time must be non-negative.
// Current only needs to be finite because negative current can represent charging or regenerative flow.
// Temperature only needs to be finite because legitimate temperatures may be below zero.
// The battery is inserted into a map by ID since mavsdk can report multiple batteries.å
// The entire map is then converted to a sorted slice.
// The snapshot uses a slice because the complete battery collection is sent to Atlas GC as a list.
// Sorting matters because Go map iteration order is intentionally unstable.
// Deterministic ordering keeps serialization, tests, and UI behavior predictable.
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
				// The snapshot supports both:
				// snapshot.Batteries - which contains every battery
				// and:
				// snapshot.BatteryPercent - which contains one convenient overall battery percentage.
				// primaryBattery is used to determine which battery to use to provide the overall battery percentage
				// itfirst looks for a battery whose function is:
				// ALL: represents the aircraft’s combined battery state.
				// PROPULSION: powers the motors and flight system.
				// If neither exists, it uses the first battery in the sorted list—normally the lowest battery ID.
				// If no batteries have been received, it returns nil.
				if primary := primaryBattery(snapshot.Batteries); primary != nil {
					snapshot.BatteryPercent = primary.RemainingPercent
				}
			})
		}
		sleepOrDone(ctx, streamRetryDelay)
	}
}

// streamFlightMode subscribes to the flight mode stream from the MAVSDK telemetry service.
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

// streamArmed subscribes to the armed state stream from the MAVSDK telemetry service.
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

// streamInAir subscribes to the in-air state stream from the MAVSDK telemetry service.
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

// streamGPSInfo subscribes to the GPS info stream from the MAVSDK telemetry service.
// it extracts the GPS fix type and the number of satellites visible from the mavsdk response,
// gpsFixString removes both possible generated prefixes. For example:
// FIX_TYPE_FIX_3D → 3D
// FIX_TYPE_RTK_FIXED → RTK_FIXED
// A negative satellite count is converted to zero before conversion to uint32;
// this avoids a negative signed integer wrapping into a huge unsigned value.
// GPS fix state is separate from raw accuracy/uncertainty, which is handled by streamRawGPS.
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

// streamHeading subscribes to the heading stream from the MAVSDK telemetry service.
// it extracts the heading in degrees and accepts values from 0 through 360 inclusive.
// Invalid, infinite, out-of-range, or NaN values are ignored completely, including not advancing ObservedAt.
// Heading is operator telemetry only. High-rate geolocation orientation comes
// from the quaternion stream because a heading alone cannot represent roll and pitch.
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

// streamVelocity subscribes to the velocity stream from the MAVSDK telemetry service.
// we consume the velocity in the NED frame:
// North: positive northward.
// East: positive eastward.
// Down: positive downward.
// It stores the three components independently when valid.
// Ground speed is calculated from horizontal velocity: math.Hypot(north, east) This is equivalent to: sqrt(north² + east²)
// Climb rate is the negative of down velocity: ClimbRateMPS = -VelocityDownMPS
// Therefore:
// - Down = +2 means descending at 2 m/s, so climb rate is -2.
// -Down = -2 means rising at 2 m/s, so climb rate is +2.
// For geolocation, all three components must be finite before the vector becomes valid.
// The receive time is stored in monotonic nanoseconds so its age can later be calculated.
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

// streamAltitude subscribes to the altitude stream from the MAVSDK telemetry service.
// MAVSDK’s altitude message exposes several reference frames:
// Relative altitude.
// AMSL altitude: altitude above mean sea level.
// Terrain altitude.
// Bottom clearance.
// The code maps AMSL into Snapshot.AbsoluteAltitudeM.
// Position messages also provide relative and absolute altitude, so streamPosition and streamAltitude
// can both update those fields. Whichever valid message arrives last wins.
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

// streamHealth subscribes to the health stream from the MAVSDK telemetry service.
// This creates telemetry.VehicleHealth from PX4 estimator and calibration state
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
				// Health may arrive after position. Trying here as well handles either order.
				setHomeFromCurrentPosition(snapshot)
			})
		}
		sleepOrDone(ctx, streamRetryDelay)
	}
}

// streamLandedState subscribes to the landed state stream from the MAVSDK telemetry service.
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

// streamRCStatus subscribes to the RC status stream from the MAVSDK telemetry service.
// Builds telemetry.RCStatus:
// Available: RC is available now.
// WasAvailableOnce: an RC connection existed previously.
// SignalStrengthPercent: validated to 0–100.
// This separates “never detected” from “was detected but has now disappeared.”
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

// streamHome subscribes to the home position stream from the MAVSDK telemetry service.
// This consumes MAVSDK’s explicit home-position stream.
// Latitude and longitude are mandatory and range-checked. If either is invalid, the entire message is ignored.
// Altitude fields are optional in Atlas terms: invalid values become nil.
// A valid explicit home position replaces a fallback home position that may have been inferred earlier during position updates.
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

// setHomeFromCurrentPosition is a helper function that sets the home position to the  aircraft's current position
// if the home position is not set yet.
func setHomeFromCurrentPosition(snapshot *telemetry.Snapshot) {
	// Do nothing until PX4 says that it has set a home position.
	// Also do nothing if we already have a valid home, or do not know our current position.
	if snapshot.HomePositionSet == nil || !*snapshot.HomePositionSet || validHomePosition(snapshot.HomePosition) || snapshot.Latitude == nil || snapshot.Longitude == nil {
		return
	}

	// The dedicated MAVSDK home stream has not provided valid home coordinates yet,
	// so we don't have coordinates for the actual home position.
	// So we use the aircraft's current position as a temporary home position.
	// We only do this once, so the home position will not move with the aircraft.
	// Once a valid home exists, later aircraft-position updates will not move it.
	// The dedicated home stream can still replace this fallback with the actual home.
	snapshot.HomePosition = &telemetry.HomePosition{
		Latitude:          pointer(*snapshot.Latitude),
		Longitude:         pointer(*snapshot.Longitude),
		AbsoluteAltitudeM: snapshot.AbsoluteAltitudeM,
		RelativeAltitudeM: pointer(0.0),
	}
}

// validHomePosition is a helper function that checks if the home position is valid.
// Checks that:
// The home object exists.
// Latitude and longitude pointers exist.
// Both values are finite.
// Both are in legal coordinate ranges.
// Altitude is not required for a home position to count as valid.
func validHomePosition(home *telemetry.HomePosition) bool {
	return home != nil && home.Latitude != nil && home.Longitude != nil &&
		finite(*home.Latitude) && finite(*home.Longitude) &&
		*home.Latitude >= -90 && *home.Latitude <= 90 &&
		*home.Longitude >= -180 && *home.Longitude <= 180
}

// streamRawGPS subscribes to the raw GPS stream from the MAVSDK telemetry service.
// This handles GPS quality rather than fix classification.
// It records:
// HDOP: horizontal dilution of precision.
// VDOP: vertical dilution of precision.
// Horizontal uncertainty.
// Vertical uncertainty.
// Velocity uncertainty.
// Course over ground.
// Uncertainties and dilution values must be non-negative. Course must be between 0 and 360 degrees.
// The resulting value is stored in two places:
// Snapshot.GPSQuality for operator telemetry.
// pose.gpsQuality for geolocation uncertainty propagation.
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
