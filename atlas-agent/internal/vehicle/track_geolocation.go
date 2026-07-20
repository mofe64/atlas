package vehicle

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/geolocation"
	"github.com/sunnyside/atlas/atlas-agent/internal/perception"
)

const selectedTrackGeolocationFreshness = time.Second

type selectedTrackGeolocator struct {
	source     perception.TrackFollowSource
	foundation *geolocation.Foundation
}

type selectedTrackGeolocationEvidence struct {
	SchemaVersion     int                                       `json:"schemaVersion"`
	Status            string                                    `json:"status"`
	SelectionID       string                                    `json:"selectionId"`
	SourceID          string                                    `json:"sourceId"`
	TrackSessionID    string                                    `json:"trackSessionId"`
	TrackID           string                                    `json:"trackId"`
	EstimatedAtUnixMS int64                                     `json:"estimatedAtUnixMs,omitempty"`
	Estimate          *geolocation.BoresightGroundPlaneEstimate `json:"estimate,omitempty"`
	RejectionCode     string                                    `json:"rejectionCode,omitempty"`
	RejectionReason   string                                    `json:"rejectionReason,omitempty"`
}

// ConfigureSelectedTrackGeolocation connects the non-actuating estimator to
// the same exact-identity source used by gimbal following. Native still owns
// operator selection and supplies the reviewed ground-plane provenance.
func (p *PayloadController) ConfigureSelectedTrackGeolocation(source perception.TrackFollowSource, foundation *geolocation.Foundation) error {
	if source == nil || foundation == nil {
		return errors.New("selected-track geolocation requires a tracking source and temporal foundation")
	}
	p.commandMu.Lock()
	defer p.commandMu.Unlock()
	p.mu.Lock()
	p.trackGeolocator = &selectedTrackGeolocator{source: source, foundation: foundation}
	p.mu.Unlock()
	return nil
}

func (p *PayloadController) geolocateSelectedTrack(input payloadCommand) (CommandResult, error) {
	p.mu.Lock()
	geolocator := p.trackGeolocator
	gimbalID := input.GimbalID
	if gimbalID == 0 {
		gimbalID = p.primaryGimbalIDLocked()
	}
	p.mu.Unlock()
	if geolocator == nil {
		return rejectedGeolocation(input, "GEOLOCATION_UNAVAILABLE", "selected-track geolocation is not configured")
	}
	if err := validateSelectedTrackGeolocationInput(input); err != nil {
		return rejectedGeolocation(input, "GEOLOCATION_INVALID_REQUEST", err.Error())
	}
	if gimbalID <= 0 {
		return rejectedGeolocation(input, "GIMBAL_UNAVAILABLE", "no discovered gimbal is available for selected-track geolocation")
	}

	observation, ok := geolocator.source.TrackForFollow(input.TrackSessionID, input.TrackID)
	if !ok {
		return rejectedGeolocation(input, "GEOLOCATION_TRACK_NOT_AVAILABLE", "the exact session-scoped track is no longer available")
	}
	if observation.SourceID != input.SourceID {
		return rejectedGeolocation(input, "GEOLOCATION_SOURCE_CHANGED", "the selected track no longer belongs to the requested camera source")
	}
	if observation.LifecycleState != perception.TrackLifecycleActive {
		return rejectedGeolocation(input, "GEOLOCATION_TRACK_NOT_ACTIVE", fmt.Sprintf("selected-track geolocation requires an ACTIVE track, got %s", observation.LifecycleState))
	}
	if trackObservationStale(observation.LastObservedAt, time.Now(), selectedTrackGeolocationFreshness) {
		return rejectedGeolocation(input, "GEOLOCATION_TRACK_STALE", "the selected track observation is stale")
	}
	if observation.FrameTiming.PipelineIngressMonotonicNS <= 0 || observation.FrameTiming.PipelineIngressUnixNS <= 0 {
		return rejectedGeolocation(input, "GEOLOCATION_FRAME_TIMING_UNAVAILABLE", "the selected track does not have a usable pre-inference frame timing anchor")
	}

	aimPoint := geolocation.BoresightAimPoint(input.AimPoint)
	aimX := observation.LatestConfirmedBox.X + observation.LatestConfirmedBox.Width/2
	aimY := observation.LatestConfirmedBox.Y + observation.LatestConfirmedBox.Height/2
	if aimPoint == geolocation.BoresightAimPointGroundContact {
		aimY = observation.LatestConfirmedBox.Y + observation.LatestConfirmedBox.Height
	}
	estimate, err := geolocator.foundation.EstimateBoresightGroundPlane(geolocation.BoresightGroundPlaneRequest{
		Timing: geolocation.VideoFrameTiming{
			SourceID: input.SourceID, StreamEpoch: observation.StreamEpoch,
			SourcePTSNS:                observation.SourcePTSNS,
			SourcePTSPresent:           observation.FrameTiming.SourcePTSPresent,
			PipelineIngressMonotonicNS: observation.FrameTiming.PipelineIngressMonotonicNS,
			PipelineIngressUnixNS:      observation.FrameTiming.PipelineIngressUnixNS,
			SourceCaptureUnixNS:        observation.FrameTiming.SourceCaptureUnixNS,
		},
		GimbalID: gimbalID, AimPoint: aimPoint,
		AimPointNormalizedX: aimX, AimPointNormalizedY: aimY,
		GroundAltitudeAMSLM:               input.GroundAltitudeAmslMeters,
		GroundAltitudeUncertaintyM:        input.GroundAltitudeUncertaintyMeters,
		GroundPlaneSource:                 input.GroundAltitudeSource + "@" + input.GroundAltitudeSourceVersion,
		AssumedAimPointHeightM:            input.AssumedAimPointHeightMeters,
		AssumedAimPointHeightUncertaintyM: input.AssumedAimPointHeightUncertaintyMeters,
	})
	if err != nil {
		return rejectedGeolocation(input, "GEOLOCATION_ESTIMATE_REJECTED", err.Error())
	}

	evidence := selectedTrackGeolocationEvidence{
		SchemaVersion: 1, Status: "SUCCEEDED", SelectionID: input.SelectionID,
		SourceID: input.SourceID, TrackSessionID: input.TrackSessionID, TrackID: input.TrackID,
		EstimatedAtUnixMS: time.Now().UTC().UnixMilli(), Estimate: &estimate,
	}
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		return rejectedGeolocation(input, "GEOLOCATION_EVIDENCE_ENCODING_FAILED", err.Error())
	}
	return CommandResult{
		Code: "TRACK_GEOLOCATION_ESTIMATED", Message: "Selected-track boresight coordinate estimated",
		EvidenceJSON: string(evidenceJSON),
	}, nil
}

func validateSelectedTrackGeolocationInput(input payloadCommand) error {
	for name, value := range map[string]string{
		"selectionId": input.SelectionID, "sourceId": input.SourceID,
		"trackSessionId": input.TrackSessionID, "trackId": input.TrackID,
		"groundAltitudeSource":        input.GroundAltitudeSource,
		"groundAltitudeSourceVersion": input.GroundAltitudeSourceVersion,
		"requestedBy":                 input.RequestedBy,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if input.AimPoint != string(geolocation.BoresightAimPointGroundContact) && input.AimPoint != string(geolocation.BoresightAimPointTargetCenter) {
		return errors.New("aimPoint must be GROUND_CONTACT or TARGET_CENTER")
	}
	if !finiteGeolocationNumber(input.GroundAltitudeAmslMeters) || input.GroundAltitudeAmslMeters < -500 || input.GroundAltitudeAmslMeters > 9_000 {
		return errors.New("groundAltitudeAmslMeters must be between -500 and 9000")
	}
	if !finiteGeolocationNumber(input.GroundAltitudeUncertaintyMeters) || input.GroundAltitudeUncertaintyMeters < 0 || input.GroundAltitudeUncertaintyMeters > 100 {
		return errors.New("groundAltitudeUncertaintyMeters must be between 0 and 100")
	}
	if input.GroundAltitudeResolvedAtUnixMS <= 0 || input.GroundAltitudeResolvedAtUnixMS > time.Now().UTC().Add(5*time.Minute).UnixMilli() {
		return errors.New("groundAltitudeResolvedAtUnixMs must be a valid non-future timestamp")
	}
	if input.AimPoint == string(geolocation.BoresightAimPointGroundContact) {
		if input.AssumedAimPointHeightMeters != 0 || input.AssumedAimPointHeightUncertaintyMeters != 0 {
			return errors.New("GROUND_CONTACT cannot include an assumed aim-point height")
		}
	} else if !finiteGeolocationNumber(input.AssumedAimPointHeightMeters) || input.AssumedAimPointHeightMeters <= 0 || input.AssumedAimPointHeightMeters > 100 ||
		!finiteGeolocationNumber(input.AssumedAimPointHeightUncertaintyMeters) || input.AssumedAimPointHeightUncertaintyMeters <= 0 || input.AssumedAimPointHeightUncertaintyMeters > 100 {
		return errors.New("TARGET_CENTER requires aim-point height and uncertainty between 0 and 100 metres")
	}
	return nil
}

func rejectedGeolocation(input payloadCommand, code, reason string) (CommandResult, error) {
	evidence, _ := json.Marshal(selectedTrackGeolocationEvidence{
		SchemaVersion: 1, Status: "REJECTED", SelectionID: input.SelectionID,
		SourceID: input.SourceID, TrackSessionID: input.TrackSessionID, TrackID: input.TrackID,
		RejectionCode: code, RejectionReason: reason,
	})
	return CommandResult{Code: code, Message: reason, EvidenceJSON: string(evidence)}, errors.New(reason)
}

func finiteGeolocationNumber(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
