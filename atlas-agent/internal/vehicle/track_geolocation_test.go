package vehicle

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/geolocation"
	"github.com/sunnyside/atlas/atlas-agent/internal/perception"
)

type fakeSelectedTrackSource struct {
	observation perception.TrackFollowObservation
	available   bool
}

func (source *fakeSelectedTrackSource) TrackForFollow(trackSessionID, trackID string) (perception.TrackFollowObservation, bool) {
	if !source.available || source.observation.TrackSessionID != trackSessionID || source.observation.TrackID != trackID {
		return perception.TrackFollowObservation{}, false
	}
	return source.observation, true
}

func TestSelectedTrackGeolocationUsesExactFrameAndReturnsEvidence(t *testing.T) {
	foundation := selectedTrackTestFoundation(t)
	source := &fakeSelectedTrackSource{available: true, observation: selectedTrackTestObservation()}
	controller := &PayloadController{gimbalIDs: []int32{1}}
	if err := controller.ConfigureSelectedTrackGeolocation(source, foundation); err != nil {
		t.Fatal(err)
	}

	result, err := controller.geolocateSelectedTrack(selectedTrackTestCommand())
	if err != nil {
		t.Fatalf("geolocateSelectedTrack() error = %v", err)
	}
	if result.Code != "TRACK_GEOLOCATION_ESTIMATED" || result.EvidenceJSON == "" {
		t.Fatalf("result = %#v", result)
	}
	var evidence selectedTrackGeolocationEvidence
	if err := json.Unmarshal([]byte(result.EvidenceJSON), &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.SchemaVersion != 1 || evidence.TrackSessionID != "session-1" || evidence.TrackID != "atlas:session-1:1" || evidence.Estimate == nil {
		t.Fatalf("evidence identity = %#v", evidence)
	}
	if evidence.Estimate.Intersection.LatitudeDeg <= evidence.Estimate.Origin.LatitudeDeg || evidence.Estimate.Uncertainty.HorizontalRadiusM <= 0 {
		t.Fatalf("estimate = %#v", evidence.Estimate)
	}
}

func TestSelectedTrackGeolocationPersistsExplicitEstimatorRejectionEvidence(t *testing.T) {
	foundation := selectedTrackTestFoundation(t)
	observation := selectedTrackTestObservation()
	observation.LatestConfirmedBox.X = 0.1
	source := &fakeSelectedTrackSource{available: true, observation: observation}
	controller := &PayloadController{gimbalIDs: []int32{1}}
	if err := controller.ConfigureSelectedTrackGeolocation(source, foundation); err != nil {
		t.Fatal(err)
	}

	result, err := controller.geolocateSelectedTrack(selectedTrackTestCommand())
	if err == nil || result.Code != "GEOLOCATION_ESTIMATE_REJECTED" || !strings.Contains(result.Message, "not centred") {
		t.Fatalf("rejection result = %#v, error = %v", result, err)
	}
	var evidence selectedTrackGeolocationEvidence
	if err := json.Unmarshal([]byte(result.EvidenceJSON), &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.Status != "REJECTED" || evidence.RejectionCode != result.Code || evidence.RejectionReason != result.Message {
		t.Fatalf("rejection evidence = %#v", evidence)
	}
}

func selectedTrackTestFoundation(t *testing.T) *geolocation.Foundation {
	t.Helper()
	foundation, err := geolocation.NewFoundation(geolocation.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	firstMonotonic := int64(10_000_000_000)
	firstUnix := time.Now().UTC().Add(-200 * time.Millisecond).UnixNano()
	for index := range 2 {
		offset := int64(index) * 100_000_000
		received := geolocation.CompanionTime{MonotonicNS: firstMonotonic + offset, UnixNS: firstUnix + offset}
		if err := foundation.RecordAircraftPose(geolocation.AircraftPoseMeasurement{
			AutopilotTimestampUS: uint64(1_000_000 + index*100_000), Received: received,
			LatitudeDeg: 51, LongitudeDeg: -0.1, AltitudeAMSLM: 72, RelativeAltitudeM: 40,
			Attitude: geolocation.Quaternion{W: 1}, VelocityNEDMPS: geolocation.Vector3{},
			Quality: geolocation.PoseQuality{
				GlobalPositionOK: true, LocalPositionOK: true, VelocityValid: true,
				PositionAge: 10 * time.Millisecond, VelocityAge: 10 * time.Millisecond,
				HorizontalUncertaintyM: 1, VerticalUncertaintyM: 1, VelocityUncertaintyMPS: 0.1,
			},
		}); err != nil {
			t.Fatal(err)
		}
		if err := foundation.RecordGimbalAttitude(geolocation.GimbalAttitudeMeasurement{
			GimbalID: 1, GimbalTimestampUS: uint64(2_000_000 + index*100_000), Received: received,
			EulerForwardDeg: geolocation.Vector3{Y: -45}, EulerNorthDeg: geolocation.Vector3{Y: -45},
			QuaternionForward: geolocation.Quaternion{W: 1}, QuaternionNorth: geolocation.Quaternion{W: 1},
		}); err != nil {
			t.Fatal(err)
		}
	}
	return foundation
}

func selectedTrackTestObservation() perception.TrackFollowObservation {
	now := time.Now().UTC()
	return perception.TrackFollowObservation{
		SourceID: "a8-main", StreamEpoch: "epoch-1", TrackSessionID: "session-1", TrackID: "atlas:session-1:1",
		LifecycleState: perception.TrackLifecycleActive, LastObservedAt: now,
		LatestConfirmedBox:        perception.BoundingBox{X: 0.45, Y: 0.4, Width: 0.1, Height: 0.2},
		LatestDetectionConfidence: 0.9, SourcePTSNS: 5_000_000_000,
		FrameTiming: perception.FrameTiming{
			SourcePTSPresent: true, PipelineIngressMonotonicNS: 10_050_000_000,
			PipelineIngressUnixNS: now.Add(-150 * time.Millisecond).UnixNano(),
		},
	}
}

func selectedTrackTestCommand() payloadCommand {
	return payloadCommand{
		SelectionID: "selection-1", SourceID: "a8-main", TrackSessionID: "session-1", TrackID: "atlas:session-1:1",
		GimbalID: 1, AimPoint: "TARGET_CENTER", GroundAltitudeAmslMeters: 32,
		GroundAltitudeUncertaintyMeters: 1, GroundAltitudeSource: "survey", GroundAltitudeSourceVersion: "r1",
		GroundAltitudeResolvedAtUnixMS: time.Now().UTC().UnixMilli(),
		AssumedAimPointHeightMeters:    0.9, AssumedAimPointHeightUncertaintyMeters: 0.5,
		RequestedBy: "operator",
	}
}
