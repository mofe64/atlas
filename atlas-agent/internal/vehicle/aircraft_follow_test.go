package vehicle

import (
	"context"
	"io"
	"log/slog"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/telemetry"
)

type fakeAircraftFollowVehicle struct {
	mu        sync.Mutex
	offboard  bool
	holdCount int
	setpoints []followVelocitySetpoint
}

func (v *fakeAircraftFollowVehicle) SetVelocityNED(_ context.Context, setpoint followVelocitySetpoint) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.setpoints = append(v.setpoints, setpoint)
	return nil
}

func (v *fakeAircraftFollowVehicle) StartOffboard(context.Context) error {
	v.mu.Lock()
	v.offboard = true
	v.mu.Unlock()
	return nil
}

func (v *fakeAircraftFollowVehicle) OffboardActive(context.Context) (bool, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.offboard, nil
}

func (v *fakeAircraftFollowVehicle) StopOffboard(context.Context) error {
	v.mu.Lock()
	v.offboard = false
	v.mu.Unlock()
	return nil
}

func (v *fakeAircraftFollowVehicle) Hold(context.Context) error {
	v.mu.Lock()
	v.holdCount++
	v.mu.Unlock()
	return nil
}

func (*fakeAircraftFollowVehicle) Close() error { return nil }

func TestAircraftFollowControllerEntersHoldWhenTargetExpires(t *testing.T) {
	now := time.Now().UTC()
	snapshot := readyFollowTelemetry(now)
	vehicle := &fakeAircraftFollowVehicle{}
	config := DefaultAircraftFollowControllerConfig()
	config.Enabled = true
	config.ValidationReference = "sitl-hil-flight/accepted-1"
	config.UpdateInterval = 10 * time.Millisecond
	config.TargetFreshness = 45 * time.Millisecond
	config.TelemetryFreshness = time.Second
	controller, err := newAircraftFollowControllerWithVehicle(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		config,
		func() (telemetry.Snapshot, bool) {
			snapshot.ObservedAt = time.Now().UTC()
			return snapshot, true
		},
		vehicle,
	)
	if err != nil {
		t.Fatalf("create controller: %v", err)
	}
	operation := readyFollowOperation(now)
	operation.ValidationReference = config.ValidationReference
	operation.LeaseExpiresAt = now.Add(time.Second)
	controller.Apply(context.Background(), operation)

	wantStates := []string{"ACQUIRING", "FOLLOWING", "DEGRADED_HOLD"}
	for _, want := range wantStates {
		select {
		case update := <-controller.Updates():
			if update.State != want {
				t.Fatalf("state = %s, want %s (%s)", update.State, want, update.Message)
			}
			if want == "DEGRADED_HOLD" && update.ReasonCode != "GEOLOCATION_STALE" {
				t.Fatalf("hold reason = %s", update.ReasonCode)
			}
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s", want)
		}
	}
	vehicle.mu.Lock()
	defer vehicle.mu.Unlock()
	if vehicle.offboard || vehicle.holdCount == 0 {
		t.Fatalf("offboard=%v hold_count=%d", vehicle.offboard, vehicle.holdCount)
	}
}

func TestAircraftFollowSetpointRespectsSpeedAndAccelerationEnvelope(t *testing.T) {
	now := time.Now().UTC()
	snapshot := readyFollowTelemetry(now)
	vehicle := &fakeAircraftFollowVehicle{}
	config := DefaultAircraftFollowControllerConfig()
	config.Enabled = true
	config.ValidationReference = "simulation"
	controller, err := newAircraftFollowControllerWithVehicle(
		slog.Default(), config, func() (telemetry.Snapshot, bool) { return snapshot, true }, vehicle,
	)
	if err != nil {
		t.Fatalf("create controller: %v", err)
	}
	operation := readyFollowOperation(now)
	active := &activeAircraftFollow{
		id: operation.SessionID, envelope: operation.Envelope, target: operation.Target,
		observationRadialNorth: -1, observationRadialEast: 0,
		lastSetpoint: followVelocitySetpoint{}, lastSetpointAt: now.Add(-100 * time.Millisecond),
	}
	setpoint, err := controller.nextSetpoint(active, now)
	if err != nil {
		t.Fatalf("calculate setpoint: %v", err)
	}
	if speed := math.Hypot(setpoint.NorthMPS, setpoint.EastMPS); speed > 0.101 {
		t.Fatalf("first setpoint exceeds 1 m/s² acceleration over 100ms: %#v", setpoint)
	}
	if setpoint.DownMPS != 0 {
		t.Fatalf("altitude-hold down velocity = %f", setpoint.DownMPS)
	}
}

func TestAircraftFollowRenewalCannotWidenOriginalEnvelope(t *testing.T) {
	now := time.Now().UTC()
	vehicle := &fakeAircraftFollowVehicle{}
	config := DefaultAircraftFollowControllerConfig()
	config.Enabled = true
	config.ValidationReference = "simulation"
	controller, err := newAircraftFollowControllerWithVehicle(
		slog.Default(), config, func() (telemetry.Snapshot, bool) { return readyFollowTelemetry(now), true }, vehicle,
	)
	if err != nil {
		t.Fatalf("create controller: %v", err)
	}
	operation := readyFollowOperation(now)
	operation.ValidationReference = config.ValidationReference
	active := &activeAircraftFollow{
		id: operation.SessionID, operationID: operation.OperationID, droneID: operation.DroneID,
		envelope: operation.Envelope, target: operation.Target, leaseExpiresAt: operation.LeaseExpiresAt,
		stop: make(chan followStopRequest, 1),
	}
	controller.active = active

	operation.Action = "renew"
	operation.Envelope.MaximumGeolocationUncertaintyM = 100
	operation.Target.HorizontalUncertaintyM = 30
	controller.renew(operation)

	select {
	case request := <-active.stop:
		if request.reasonCode != "TARGET_UPDATE_INVALID" {
			t.Fatalf("renewal reason = %s, want TARGET_UPDATE_INVALID", request.reasonCode)
		}
	case <-time.After(time.Second):
		t.Fatal("widened renewal was not rejected")
	}
}

func TestAircraftFollowControllerRemainsUnverifiedByDefault(t *testing.T) {
	config := DefaultAircraftFollowControllerConfig()
	controller, err := newAircraftFollowControllerWithVehicle(
		slog.Default(), config, func() (telemetry.Snapshot, bool) { return telemetry.Snapshot{}, false }, &fakeAircraftFollowVehicle{},
	)
	if err != nil {
		t.Fatalf("create controller: %v", err)
	}
	operation := readyFollowOperation(time.Now().UTC())
	controller.Apply(context.Background(), operation)
	select {
	case update := <-controller.Updates():
		if update.State != "DEGRADED_HOLD" || update.ReasonCode != "FOLLOW_CONTROL_UNVERIFIED" {
			t.Fatalf("unverified update = %#v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("unverified controller did not reject follow start")
	}
}

func readyFollowTelemetry(now time.Time) telemetry.Snapshot {
	battery, altitude, latitude, longitude, heading := 80.0, 30.0, 51.5, -0.14, 0.0
	armed, inAir := true, true
	return telemetry.Snapshot{
		ObservedAt: now, BatteryPercent: &battery, RelativeAltitudeM: &altitude,
		Latitude: &latitude, Longitude: &longitude, HeadingDeg: &heading,
		Armed: &armed, InAir: &inAir,
		Health: &telemetry.VehicleHealth{LocalPositionOK: true, GlobalPositionOK: true},
	}
}

func readyFollowOperation(now time.Time) AircraftFollowOperation {
	return AircraftFollowOperation{
		OperationID: "operation-1", SessionID: "follow-1", DroneID: "drone-1", Action: "start",
		Envelope: AircraftFollowEnvelope{
			StandoffM: 40, AltitudeRelativeM: 30, MinimumAltitudeRelativeM: 20,
			MaximumAltitudeRelativeM: 40, MaximumGroundSpeedMPS: 8,
			MaximumAccelerationMPS2: 1, MaximumDuration: time.Minute,
			BoundaryCenterLatitude: 51.5, BoundaryCenterLongitude: -0.14,
			BoundaryRadiusM: 500, MinimumBatteryPercent: 30, MinimumTrackConfidence: 0.7,
			MaximumGeolocationUncertaintyM: 20, MaximumVelocityUncertaintyMPS: 5,
		},
		Target: AircraftFollowTarget{
			GeolocationID: "geo-1", SelectionID: "selection-1", SourceID: "camera-1",
			TrackSessionID: "track-session-1", TrackID: "track-1", ObservedAt: now,
			Latitude: 51.5002, Longitude: -0.14, AltitudeAMSLM: 80,
			VelocityNorthMPS: 1, VelocityEastMPS: 0, HorizontalUncertaintyM: 5,
			VelocityUncertaintyMPS: 1, TrackConfidence: 0.9,
			LifecycleState: "ACTIVE", MotionStatus: "FILTERED",
		},
		LeaseExpiresAt: now.Add(time.Second),
	}
}
