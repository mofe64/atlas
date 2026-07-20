package vehicle

import (
	"math"
	"testing"

	"github.com/sunnyside/atlas/atlas-agent/internal/geolocation"
	gimbalpb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/gimbal"
)

func TestRecordMeasuredGimbalAttitudePreservesTimestampAndFrames(t *testing.T) {
	foundation, err := geolocation.NewFoundation(geolocation.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	received := geolocation.CompanionTime{MonotonicNS: 10_000_000_000, UnixNS: 1_700_000_000_000_000_000}
	attitude := &gimbalpb.Attitude{
		GimbalId: 2, TimestampUs: 1_000_000,
		EulerAngleForward: &gimbalpb.EulerAngle{RollDeg: 1, PitchDeg: -42, YawDeg: 12},
		EulerAngleNorth:   &gimbalpb.EulerAngle{RollDeg: 1, PitchDeg: -42, YawDeg: 102},
		QuaternionForward: &gimbalpb.Quaternion{W: 1}, QuaternionNorth: &gimbalpb.Quaternion{W: 1},
		AngularVelocity: &gimbalpb.AngularVelocityBody{PitchRadS: 0.2, YawRadS: 0.3},
	}
	if err := recordMeasuredGimbalAttitude(foundation, attitude, received); err != nil {
		t.Fatalf("recordMeasuredGimbalAttitude() error = %v", err)
	}
	sample, err := foundation.GimbalAt(2, received.MonotonicNS)
	if err != nil {
		t.Fatal(err)
	}
	if sample.GimbalTimestampUS != 1_000_000 || sample.EulerForwardDeg.Y != -42 || sample.EulerNorthDeg.Z != 102 || math.Abs(sample.AngularVelocity.Z-0.3) > 1e-6 {
		t.Fatalf("sample = %#v", sample)
	}
}

func TestRecordMeasuredGimbalAttitudeRejectsCommandOnlyShape(t *testing.T) {
	foundation, err := geolocation.NewFoundation(geolocation.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	err = recordMeasuredGimbalAttitude(foundation, &gimbalpb.Attitude{
		GimbalId: 2, TimestampUs: 1, EulerAngleForward: &gimbalpb.EulerAngle{PitchDeg: -20},
	}, geolocation.CompanionTime{MonotonicNS: 1, UnixNS: 1})
	if err == nil {
		t.Fatal("incomplete measured attitude was accepted")
	}
}
