package geolocation

import (
	"math"
	"testing"
	"time"
)

func testFoundation(t *testing.T) *Foundation {
	t.Helper()
	config := DefaultConfig()
	config.MaxInterpolationGap = 200 * time.Millisecond
	foundation, err := NewFoundation(config)
	if err != nil {
		t.Fatalf("NewFoundation() error = %v", err)
	}
	return foundation
}

func poseMeasurement(autopilotUS uint64, monotonicNS, unixNS int64, latitude float64) AircraftPoseMeasurement {
	return AircraftPoseMeasurement{
		AutopilotTimestampUS: autopilotUS,
		Received:             CompanionTime{MonotonicNS: monotonicNS, UnixNS: unixNS},
		LatitudeDeg:          latitude, LongitudeDeg: -0.1278, AltitudeAMSLM: 72, RelativeAltitudeM: 40,
		Attitude: Quaternion{W: 1}, VelocityNEDMPS: Vector3{X: 4, Y: 2, Z: -0.5},
		Quality: PoseQuality{
			GlobalPositionOK: true, LocalPositionOK: true, VelocityValid: true,
			PositionAge: 10 * time.Millisecond, VelocityAge: 5 * time.Millisecond,
			HorizontalUncertaintyM: 0.4, VerticalUncertaintyM: 0.8, VelocityUncertaintyMPS: 0.1,
		},
	}
}

func gimbalMeasurement(gimbalUS uint64, monotonicNS, unixNS int64, yaw float64) GimbalAttitudeMeasurement {
	return GimbalAttitudeMeasurement{
		GimbalID: 3, GimbalTimestampUS: gimbalUS,
		Received:        CompanionTime{MonotonicNS: monotonicNS, UnixNS: unixNS},
		EulerForwardDeg: Vector3{Y: -45, Z: yaw}, EulerNorthDeg: Vector3{Y: -45, Z: yaw},
		QuaternionForward: Quaternion{W: 1}, QuaternionNorth: Quaternion{W: 1},
		AngularVelocity: Vector3{Z: 0.1},
	}
}

func TestContextForFrameInterpolatesAircraftAndMeasuredGimbal(t *testing.T) {
	foundation := testFoundation(t)
	const (
		firstMonotonic = int64(10_000_000_000)
		firstUnix      = int64(1_700_000_000_000_000_000)
	)
	if err := foundation.RecordAircraftPose(poseMeasurement(1_000_000, firstMonotonic, firstUnix, 51)); err != nil {
		t.Fatal(err)
	}
	if err := foundation.RecordAircraftPose(poseMeasurement(1_100_000, firstMonotonic+100_000_000, firstUnix+100_000_000, 51.001)); err != nil {
		t.Fatal(err)
	}
	if err := foundation.RecordGimbalAttitude(gimbalMeasurement(2_000_000, firstMonotonic, firstUnix, 10)); err != nil {
		t.Fatal(err)
	}
	if err := foundation.RecordGimbalAttitude(gimbalMeasurement(2_100_000, firstMonotonic+100_000_000, firstUnix+100_000_000, 20)); err != nil {
		t.Fatal(err)
	}
	gimbalID := int32(3)
	context, err := foundation.ContextForFrame(VideoFrameTiming{
		SourceID: "a8-main", StreamEpoch: "epoch-1", SourcePTSNS: 5_000_000_000, SourcePTSPresent: true,
		PipelineIngressMonotonicNS: firstMonotonic + 50_000_000,
		PipelineIngressUnixNS:      firstUnix + 50_000_000,
	}, &gimbalID)
	if err != nil {
		t.Fatalf("ContextForFrame() error = %v", err)
	}
	if context.FrameTime.Quality != FrameTimePipelineIngressEstimate || context.FrameTime.CompanionMonotonicNS != firstMonotonic+50_000_000 {
		t.Fatalf("frame time = %#v", context.FrameTime)
	}
	if math.Abs(context.Aircraft.LatitudeDeg-51.0005) > 1e-9 || context.Aircraft.InterpolationSpan != 100*time.Millisecond {
		t.Fatalf("aircraft pose = %#v", context.Aircraft)
	}
	if context.Gimbal == nil || math.Abs(context.Gimbal.EulerForwardDeg.Z-15) > 1e-9 {
		t.Fatalf("gimbal attitude = %#v", context.Gimbal)
	}
}

func TestSourceReferenceTimestampTakesPriorityOverPipelineIngress(t *testing.T) {
	foundation := testFoundation(t)
	frameTime, err := foundation.ResolveFrameTime(VideoFrameTiming{
		SourceID: "a8-main", StreamEpoch: "epoch-1", SourcePTSNS: 1, SourcePTSPresent: true,
		PipelineIngressMonotonicNS: 10_000_000_000, PipelineIngressUnixNS: 1_700_000_000_000_000_000,
		SourceCaptureUnixNS: 1_699_999_999_950_000_000,
	})
	if err != nil {
		t.Fatalf("ResolveFrameTime() error = %v", err)
	}
	if frameTime.Quality != FrameTimeSourceReference || frameTime.CompanionMonotonicNS != 9_950_000_000 {
		t.Fatalf("frame time = %#v", frameTime)
	}
	if frameTime.Uncertainty < 50*time.Millisecond {
		t.Fatalf("source-reference uncertainty = %s", frameTime.Uncertainty)
	}
}

func TestImplausibleSourceReferenceFallsBackToPipelineIngress(t *testing.T) {
	foundation := testFoundation(t)
	frameTime, err := foundation.ResolveFrameTime(VideoFrameTiming{
		SourceID: "a8-main", StreamEpoch: "epoch-1", SourcePTSNS: 1, SourcePTSPresent: true,
		PipelineIngressMonotonicNS: 10_000_000_000, PipelineIngressUnixNS: 1_700_000_000_000_000_000,
		SourceCaptureUnixNS: 1_699_999_900_000_000_000,
	})
	if err != nil {
		t.Fatalf("ResolveFrameTime() error = %v", err)
	}
	if frameTime.Quality != FrameTimePipelineIngressEstimate || frameTime.CompanionMonotonicNS != 10_000_000_000 {
		t.Fatalf("implausible source-reference frame time = %#v", frameTime)
	}
}

func TestClockRollbackStartsNewEpochAndClearsPoseContinuity(t *testing.T) {
	foundation := testFoundation(t)
	if err := foundation.RecordAircraftPose(poseMeasurement(2_000_000, 10_000_000_000, 1_700_000_000_000_000_000, 51)); err != nil {
		t.Fatal(err)
	}
	if err := foundation.RecordAircraftPose(poseMeasurement(1_000_000, 10_100_000_000, 1_700_000_000_100_000_000, 52)); err != nil {
		t.Fatal(err)
	}
	health := foundation.Health()
	if health.PoseSamples != 1 {
		t.Fatalf("pose samples = %d, want 1 after clock rollback", health.PoseSamples)
	}
	pose, err := foundation.PoseAt(10_100_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if pose.AutopilotClockEpoch != 2 || pose.LatitudeDeg != 52 {
		t.Fatalf("pose = %#v", pose)
	}
}

func TestPoseBufferIsBoundedAndRejectsStalePosition(t *testing.T) {
	config := DefaultConfig()
	config.MaxPoseSamples = 2
	foundation, err := NewFoundation(config)
	if err != nil {
		t.Fatal(err)
	}
	for index := range 3 {
		measurement := poseMeasurement(uint64(1_000_000+index*100_000), int64(10_000_000_000+index*100_000_000), int64(1_700_000_000_000_000_000+index*100_000_000), 51+float64(index))
		if err := foundation.RecordAircraftPose(measurement); err != nil {
			t.Fatal(err)
		}
	}
	if health := foundation.Health(); health.PoseSamples != 2 {
		t.Fatalf("pose samples = %d, want bounded 2", health.PoseSamples)
	}
	stale := poseMeasurement(1_400_000, 10_400_000_000, 1_700_000_000_400_000_000, 54)
	stale.Quality.PositionAge = 2 * time.Second
	if err := foundation.RecordAircraftPose(stale); err != nil {
		t.Fatal(err)
	}
	if _, err := foundation.PoseAt(10_400_000_000); err == nil {
		t.Fatal("PoseAt() accepted stale estimator position")
	}
}

func TestVideoClockDomainsAreBoundedAcrossStreamResets(t *testing.T) {
	config := DefaultConfig()
	config.MaxVideoClockDomains = 2
	foundation, err := NewFoundation(config)
	if err != nil {
		t.Fatal(err)
	}
	for index := range 3 {
		_, err := foundation.ResolveFrameTime(VideoFrameTiming{
			SourceID: "a8-main", StreamEpoch: string(rune('a' + index)), SourcePTSPresent: true,
			SourcePTSNS: int64(index), PipelineIngressMonotonicNS: int64(1_000 + index),
			PipelineIngressUnixNS: int64(10_000 + index),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	foundation.mu.RLock()
	defer foundation.mu.RUnlock()
	if len(foundation.videoClocks) != 2 {
		t.Fatalf("video clock domains = %d, want bounded 2", len(foundation.videoClocks))
	}
}

func TestVideoPTSForwardDiscontinuityStartsNewClockEpoch(t *testing.T) {
	foundation := testFoundation(t)
	first, err := foundation.ResolveFrameTime(VideoFrameTiming{
		SourceID: "a8-main", StreamEpoch: "epoch-1", SourcePTSPresent: true,
		SourcePTSNS: 1_000_000_000, PipelineIngressMonotonicNS: 10_000_000_000,
		PipelineIngressUnixNS: 1_700_000_000_000_000_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := foundation.ResolveFrameTime(VideoFrameTiming{
		SourceID: "a8-main", StreamEpoch: "epoch-1", SourcePTSPresent: true,
		SourcePTSNS: 20_000_000_000, PipelineIngressMonotonicNS: 10_100_000_000,
		PipelineIngressUnixNS: 1_700_000_000_100_000_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ClockEpoch != 1 || second.ClockEpoch != 2 || second.CompanionMonotonicNS != 10_100_000_000 {
		t.Fatalf("clock epochs first=%#v second=%#v", first, second)
	}
}
