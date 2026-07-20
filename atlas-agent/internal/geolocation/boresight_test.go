package geolocation

import (
	"math"
	"strings"
	"testing"
	"time"
)

const (
	boresightFirstMonotonic = int64(10_000_000_000)
	boresightFirstUnix      = int64(1_700_000_000_000_000_000)
)

func boresightFoundation(t *testing.T, aircraftAttitude Quaternion, gimbalPitch, gimbalYaw float64) *Foundation {
	t.Helper()
	foundation := testFoundation(t)
	for index := range 2 {
		pose := poseMeasurement(
			uint64(1_000_000+index*100_000),
			boresightFirstMonotonic+int64(index)*100_000_000,
			boresightFirstUnix+int64(index)*100_000_000,
			51,
		)
		pose.AltitudeAMSLM = 72
		pose.RelativeAltitudeM = 40
		pose.Attitude = aircraftAttitude
		if err := foundation.RecordAircraftPose(pose); err != nil {
			t.Fatal(err)
		}
		gimbal := gimbalMeasurement(
			uint64(2_000_000+index*100_000),
			boresightFirstMonotonic+int64(index)*100_000_000,
			boresightFirstUnix+int64(index)*100_000_000,
			gimbalYaw,
		)
		gimbal.EulerForwardDeg.Y = gimbalPitch
		gimbal.EulerNorthDeg.Y = gimbalPitch
		if err := foundation.RecordGimbalAttitude(gimbal); err != nil {
			t.Fatal(err)
		}
	}
	return foundation
}

func boresightRequest() BoresightGroundPlaneRequest {
	return BoresightGroundPlaneRequest{
		Timing: VideoFrameTiming{
			SourceID: "a8-main", StreamEpoch: "epoch-1", SourcePTSNS: 5_000_000_000, SourcePTSPresent: true,
			PipelineIngressMonotonicNS: boresightFirstMonotonic + 50_000_000,
			PipelineIngressUnixNS:      boresightFirstUnix + 50_000_000,
		},
		GimbalID: 3, AimPoint: BoresightAimPointGroundContact,
		AimPointNormalizedX: 0.5, AimPointNormalizedY: 0.5,
		GroundAltitudeAMSLM: 32, GroundAltitudeUncertaintyM: 1,
		GroundPlaneSource: "OPERATOR_FLAT_GROUND",
	}
}

func TestEstimateBoresightGroundPlaneProjectsMeasuredNorthAttitude(t *testing.T) {
	foundation := boresightFoundation(t, Quaternion{W: 1}, -45, 0)
	estimate, err := foundation.EstimateBoresightGroundPlane(boresightRequest())
	if err != nil {
		t.Fatalf("EstimateBoresightGroundPlane() error = %v", err)
	}
	if estimate.Method != BoresightGroundPlaneMethod || estimate.FrameTime.Quality != FrameTimePipelineIngressEstimate {
		t.Fatalf("estimate provenance = %#v", estimate)
	}
	if math.Abs(estimate.WorldDirectionNED.X-math.Sqrt(0.5)) > 1e-12 || math.Abs(estimate.WorldDirectionNED.Y) > 1e-12 || math.Abs(estimate.WorldDirectionNED.Z-math.Sqrt(0.5)) > 1e-12 {
		t.Fatalf("world direction = %#v", estimate.WorldDirectionNED)
	}
	if math.Abs(estimate.NorthOffsetM-40) > 1e-9 || math.Abs(estimate.EastOffsetM) > 1e-9 || math.Abs(estimate.GroundRangeM-40) > 1e-9 || math.Abs(estimate.SlantRangeM-40*math.Sqrt(2)) > 1e-9 {
		t.Fatalf("intersection geometry = %#v", estimate)
	}
	if estimate.Intersection.LatitudeDeg <= estimate.Origin.LatitudeDeg || math.Abs(estimate.Intersection.LongitudeDeg-estimate.Origin.LongitudeDeg) > 1e-12 {
		t.Fatalf("intersection = %#v, origin = %#v", estimate.Intersection, estimate.Origin)
	}
	if estimate.Uncertainty.HorizontalRadiusM <= estimate.Uncertainty.AircraftHorizontalM || len(estimate.Assumptions) != 4 {
		t.Fatalf("uncertainty/assumptions = %#v / %#v", estimate.Uncertainty, estimate.Assumptions)
	}
	if estimate.BoresightAlignment.Status != "UNVERIFIED" || estimate.BoresightAlignment.ErrorBoundDeg != 10 || foundation.BoresightAlignmentStatus() != "unverified" {
		t.Fatalf("boresight alignment evidence = %#v", estimate.BoresightAlignment)
	}
}

func TestBoresightAlignmentStatusRequiresCommissioningReference(t *testing.T) {
	config := DefaultConfig()
	config.BoresightAlignmentReference = "commissioning/a8-gimbal-2026-07-20"
	foundation, err := NewFoundation(config)
	if err != nil {
		t.Fatal(err)
	}
	if foundation.BoresightAlignmentStatus() != "verified" {
		t.Fatalf("alignment status = %q", foundation.BoresightAlignmentStatus())
	}
}

func TestEstimateBoresightGroundPlaneProducesAircraftBodyCoordinates(t *testing.T) {
	rootHalf := math.Sqrt(0.5)
	foundation := boresightFoundation(t, Quaternion{W: rootHalf, Z: rootHalf}, -45, 0)
	estimate, err := foundation.EstimateBoresightGroundPlane(boresightRequest())
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(estimate.AircraftDirectionFRD.X) > 1e-12 || estimate.AircraftDirectionFRD.Y >= 0 || math.Abs(estimate.AircraftDirectionFRD.Y+rootHalf) > 1e-12 || math.Abs(estimate.AircraftDirectionFRD.Z-rootHalf) > 1e-12 {
		t.Fatalf("aircraft direction = %#v, want north ray to be left/down of east-facing aircraft", estimate.AircraftDirectionFRD)
	}
}

func TestEstimateBoresightGroundPlaneSupportsElevatedTargetCentre(t *testing.T) {
	foundation := boresightFoundation(t, Quaternion{W: 1}, -45, 90)
	request := boresightRequest()
	request.AimPoint = BoresightAimPointTargetCenter
	request.AssumedAimPointHeightM = 2
	request.AssumedAimPointHeightUncertaintyM = 0.5
	estimate, err := foundation.EstimateBoresightGroundPlane(request)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(estimate.IntersectionAltitudeAMSLM-34) > 1e-12 || math.Abs(estimate.EastOffsetM-38) > 1e-9 || math.Abs(estimate.NorthOffsetM) > 1e-9 {
		t.Fatalf("elevated target intersection = %#v", estimate)
	}
}

func TestEstimateBoresightGroundPlaneRequiresTargetCentreHeightUncertainty(t *testing.T) {
	foundation := boresightFoundation(t, Quaternion{W: 1}, -45, 0)
	request := boresightRequest()
	request.AimPoint = BoresightAimPointTargetCenter
	if _, err := foundation.EstimateBoresightGroundPlane(request); err == nil || !strings.Contains(err.Error(), "requires positive") {
		t.Fatalf("target-centre height error = %v", err)
	}
}

func TestEstimateBoresightGroundPlaneRejectsOffCentreAndNearHorizon(t *testing.T) {
	foundation := boresightFoundation(t, Quaternion{W: 1}, -45, 0)
	request := boresightRequest()
	request.AimPointNormalizedX = 0.56
	if _, err := foundation.EstimateBoresightGroundPlane(request); err == nil || !strings.Contains(err.Error(), "not centred") {
		t.Fatalf("off-centre error = %v", err)
	}

	foundation = boresightFoundation(t, Quaternion{W: 1}, -5, 0)
	request = boresightRequest()
	if _, err := foundation.EstimateBoresightGroundPlane(request); err == nil || !strings.Contains(err.Error(), "below minimum") {
		t.Fatalf("near-horizon error = %v", err)
	}
}

func TestEstimateBoresightGroundPlaneRejectsUnboundedTiming(t *testing.T) {
	config := DefaultConfig()
	config.BoresightMaximumTimeUncertainty = 100_000_000
	foundation, err := NewFoundation(config)
	if err != nil {
		t.Fatal(err)
	}
	for index := range 2 {
		pose := poseMeasurement(uint64(1_000_000+index*100_000), boresightFirstMonotonic+int64(index)*100_000_000, boresightFirstUnix+int64(index)*100_000_000, 51)
		if err := foundation.RecordAircraftPose(pose); err != nil {
			t.Fatal(err)
		}
		gimbal := gimbalMeasurement(uint64(2_000_000+index*100_000), boresightFirstMonotonic+int64(index)*100_000_000, boresightFirstUnix+int64(index)*100_000_000, 0)
		if err := foundation.RecordGimbalAttitude(gimbal); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := foundation.EstimateBoresightGroundPlane(boresightRequest()); err == nil || !strings.Contains(err.Error(), "frame-time uncertainty") {
		t.Fatalf("time uncertainty error = %v", err)
	}
}

func TestBoresightUncertaintyRejectsConeAcrossHorizon(t *testing.T) {
	foundation := testFoundation(t)
	direction := directionFromNorthEuler(Vector3{Y: -4})
	context := TemporalContext{
		FrameTime: FrameTime{Uncertainty: 250 * time.Millisecond},
		Aircraft: AircraftPose{
			VelocityNEDMPS: Vector3{X: 1},
			Quality:        PoseQuality{HorizontalUncertaintyM: 1, VerticalUncertaintyM: 1},
		},
		Gimbal: &GimbalAttitude{AngularVelocity: Vector3{Z: 0.1}},
	}
	request := boresightRequest()
	if _, err := foundation.boresightUncertainty(context, request, direction, 40, 40/math.Tan(4*math.Pi/180), 4); err == nil || !strings.Contains(err.Error(), "horizon") {
		t.Fatalf("uncertainty cone error = %v", err)
	}
}
