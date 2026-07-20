// Package geolocation owns the bounded temporal foundation used to turn a
// frame-space observation into a physically aligned aircraft/gimbal state and
// a bounded boresight/ground-plane estimate. It does not claim arbitrary-pixel
// projection or terrain-aware geolocation.
package geolocation

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

type FrameTimeQuality string

const (
	FrameTimeSourceReference         FrameTimeQuality = "SOURCE_REFERENCE"
	FrameTimePipelineIngressEstimate FrameTimeQuality = "PIPELINE_INGRESS_ESTIMATE"
)

type Config struct {
	MaxPoseSamples                  int
	MaxGimbalSamplesPerDevice       int
	MaxClockAnchors                 int
	MaxVideoClockDomains            int
	MaxInterpolationGap             time.Duration
	MaxPositionAge                  time.Duration
	MaxVelocityAge                  time.Duration
	PipelineIngressTimeUncertainty  time.Duration
	SourceReferenceTimeUncertainty  time.Duration
	MaxSourceReferenceAge           time.Duration
	MaxSourceReferenceFutureSkew    time.Duration
	BoresightCenterTolerance        float64
	BoresightAngularUncertaintyDeg  float64
	BoresightAlignmentReference     string
	BoresightOriginUncertaintyM     float64
	BoresightMinimumDepressionDeg   float64
	BoresightMaximumGroundRangeM    float64
	BoresightMaximumTimeUncertainty time.Duration
}

func DefaultConfig() Config {
	return Config{
		MaxPoseSamples:                  900,
		MaxGimbalSamplesPerDevice:       600,
		MaxClockAnchors:                 256,
		MaxVideoClockDomains:            16,
		MaxInterpolationGap:             250 * time.Millisecond,
		MaxPositionAge:                  750 * time.Millisecond,
		MaxVelocityAge:                  500 * time.Millisecond,
		PipelineIngressTimeUncertainty:  250 * time.Millisecond,
		SourceReferenceTimeUncertainty:  50 * time.Millisecond,
		MaxSourceReferenceAge:           10 * time.Second,
		MaxSourceReferenceFutureSkew:    250 * time.Millisecond,
		BoresightCenterTolerance:        0.04,
		BoresightAngularUncertaintyDeg:  10,
		BoresightOriginUncertaintyM:     1,
		BoresightMinimumDepressionDeg:   20,
		BoresightMaximumGroundRangeM:    3_000,
		BoresightMaximumTimeUncertainty: 500 * time.Millisecond,
	}
}

func (config Config) validate() error {
	if config.MaxPoseSamples < 2 || config.MaxGimbalSamplesPerDevice < 2 || config.MaxClockAnchors < 2 {
		return errors.New("geolocation buffers and clock anchor capacity must be at least two")
	}
	if config.MaxVideoClockDomains < 1 {
		return errors.New("at least one video clock domain is required")
	}
	if config.MaxInterpolationGap <= 0 || config.MaxPositionAge <= 0 || config.MaxVelocityAge <= 0 || config.PipelineIngressTimeUncertainty <= 0 ||
		config.SourceReferenceTimeUncertainty <= 0 || config.MaxSourceReferenceAge <= 0 || config.MaxSourceReferenceFutureSkew < 0 {
		return errors.New("geolocation timing limits must be positive and future skew cannot be negative")
	}
	if !finite(config.BoresightCenterTolerance) || config.BoresightCenterTolerance <= 0 || config.BoresightCenterTolerance > 0.25 {
		return errors.New("boresight centre tolerance must be between 0 and 0.25")
	}
	if !finite(config.BoresightAngularUncertaintyDeg) || config.BoresightAngularUncertaintyDeg <= 0 || config.BoresightAngularUncertaintyDeg >= 45 {
		return errors.New("boresight angular uncertainty must be between 0 and 45 degrees")
	}
	if len(strings.TrimSpace(config.BoresightAlignmentReference)) > 240 {
		return errors.New("boresight alignment reference cannot exceed 240 characters")
	}
	if !finite(config.BoresightOriginUncertaintyM) || config.BoresightOriginUncertaintyM < 0 {
		return errors.New("boresight origin uncertainty must be finite and non-negative")
	}
	if !finite(config.BoresightMinimumDepressionDeg) || config.BoresightMinimumDepressionDeg <= 0 || config.BoresightMinimumDepressionDeg >= 90 {
		return errors.New("boresight minimum depression must be between 0 and 90 degrees")
	}
	if !finite(config.BoresightMaximumGroundRangeM) || config.BoresightMaximumGroundRangeM <= 0 || config.BoresightMaximumTimeUncertainty <= 0 {
		return errors.New("boresight range and time limits must be positive")
	}
	return nil
}

type Quaternion struct {
	W float64 `json:"w"`
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

type Vector3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

type PoseQuality struct {
	GlobalPositionOK       bool
	LocalPositionOK        bool
	VelocityValid          bool
	PositionAge            time.Duration
	VelocityAge            time.Duration
	HorizontalUncertaintyM float64
	VerticalUncertaintyM   float64
	VelocityUncertaintyMPS float64
}

// AircraftPoseMeasurement combines the high-rate timestamped attitude with
// the latest estimator position/velocity. The field ages prevent a fresh
// quaternion from disguising stale navigation state.
type AircraftPoseMeasurement struct {
	AutopilotTimestampUS uint64
	Received             CompanionTime
	LatitudeDeg          float64
	LongitudeDeg         float64
	AltitudeAMSLM        float64
	RelativeAltitudeM    float64
	Attitude             Quaternion
	VelocityNEDMPS       Vector3
	Quality              PoseQuality
}

type AircraftPose struct {
	CompanionMonotonicNS int64
	ObservedAtUnixNS     int64
	AutopilotTimestampUS uint64
	AutopilotClockEpoch  uint64
	LatitudeDeg          float64
	LongitudeDeg         float64
	AltitudeAMSLM        float64
	RelativeAltitudeM    float64
	RollDeg              float64
	PitchDeg             float64
	YawDeg               float64
	Attitude             Quaternion
	VelocityNEDMPS       Vector3
	Quality              PoseQuality
	InterpolationSpan    time.Duration
}

type GimbalAttitudeMeasurement struct {
	GimbalID          int32
	GimbalTimestampUS uint64
	Received          CompanionTime
	EulerForwardDeg   Vector3
	EulerNorthDeg     Vector3
	QuaternionForward Quaternion
	QuaternionNorth   Quaternion
	AngularVelocity   Vector3
}

type GimbalAttitude struct {
	GimbalID             int32
	CompanionMonotonicNS int64
	ObservedAtUnixNS     int64
	GimbalTimestampUS    uint64
	GimbalClockEpoch     uint64
	EulerForwardDeg      Vector3
	EulerNorthDeg        Vector3
	QuaternionForward    Quaternion
	QuaternionNorth      Quaternion
	AngularVelocity      Vector3
	InterpolationSpan    time.Duration
}

type VideoFrameTiming struct {
	SourceID                   string
	StreamEpoch                string
	SourcePTSNS                int64
	SourcePTSPresent           bool
	PipelineIngressMonotonicNS int64
	PipelineIngressUnixNS      int64
	SourceCaptureUnixNS        int64
}

type FrameTime struct {
	SourceID             string           `json:"sourceId"`
	StreamEpoch          string           `json:"streamEpoch"`
	SourcePTSNS          int64            `json:"sourcePtsNs"`
	CompanionMonotonicNS int64            `json:"companionMonotonicNs"`
	ObservedAtUnixNS     int64            `json:"observedAtUnixNs"`
	Quality              FrameTimeQuality `json:"quality"`
	ClockEpoch           uint64           `json:"clockEpoch"`
	Uncertainty          time.Duration    `json:"uncertaintyNs"`
}

type TemporalContext struct {
	FrameTime FrameTime
	Aircraft  AircraftPose
	Gimbal    *GimbalAttitude
}

type ClockHealth struct {
	Domain      string
	Epoch       uint64
	AnchorCount int
	Ready       bool
	Uncertainty time.Duration
}

type Health struct {
	PoseSamples             int
	GimbalSamples           map[int32]int
	DroppedPoseMeasurements uint64
	Clocks                  []ClockHealth
}

type Foundation struct {
	mu sync.RWMutex

	config             Config
	wallClock          *offsetCorrelator
	autopilotClock     *offsetCorrelator
	autopilotUnixClock *offsetCorrelator
	videoClocks        map[string]*offsetCorrelator
	videoClockOrder    []string
	gimbalClocks       map[int32]*offsetCorrelator
	poses              []AircraftPose
	gimbals            map[int32][]GimbalAttitude
	droppedPoses       uint64
}

func NewFoundation(config Config) (*Foundation, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	return &Foundation{
		config:             config,
		wallClock:          newOffsetCorrelator(config.MaxClockAnchors),
		autopilotClock:     newOffsetCorrelator(config.MaxClockAnchors),
		autopilotUnixClock: newOffsetCorrelator(config.MaxClockAnchors),
		videoClocks:        make(map[string]*offsetCorrelator),
		gimbalClocks:       make(map[int32]*offsetCorrelator),
		gimbals:            make(map[int32][]GimbalAttitude),
	}, nil
}

func (foundation *Foundation) RecordAircraftPose(measurement AircraftPoseMeasurement) error {
	if err := validateCompanionTime(measurement.Received); err != nil {
		return err
	}
	if measurement.AutopilotTimestampUS == 0 {
		return errors.New("aircraft pose requires an autopilot timestamp")
	}
	if !validLatitudeLongitude(measurement.LatitudeDeg, measurement.LongitudeDeg) || !finite(measurement.AltitudeAMSLM) || !finite(measurement.RelativeAltitudeM) {
		return errors.New("aircraft pose position is invalid")
	}
	attitude, ok := normalizeQuaternion(measurement.Attitude)
	if !ok {
		return errors.New("aircraft pose quaternion is invalid")
	}
	if !finiteVector(measurement.VelocityNEDMPS) {
		return errors.New("aircraft pose velocity is invalid")
	}

	foundation.mu.Lock()
	defer foundation.mu.Unlock()
	foundation.wallClock.observe(measurement.Received.UnixNS, measurement.Received.MonotonicNS)
	remoteNS := microsecondsToNanoseconds(measurement.AutopilotTimestampUS)
	if foundation.autopilotClock.observe(remoteNS, measurement.Received.MonotonicNS) {
		foundation.poses = foundation.poses[:0]
	}
	resolved, _, _ := foundation.autopilotClock.resolve(remoteNS)
	observedUnix, _, _ := foundation.wallClock.remoteAt(resolved)
	roll, pitch, yaw := quaternionEulerDegrees(attitude)
	pose := AircraftPose{
		CompanionMonotonicNS: resolved, ObservedAtUnixNS: observedUnix,
		AutopilotTimestampUS: measurement.AutopilotTimestampUS, AutopilotClockEpoch: foundation.autopilotClock.epoch,
		LatitudeDeg: measurement.LatitudeDeg, LongitudeDeg: measurement.LongitudeDeg,
		AltitudeAMSLM: measurement.AltitudeAMSLM, RelativeAltitudeM: measurement.RelativeAltitudeM,
		RollDeg: roll, PitchDeg: pitch, YawDeg: yaw, Attitude: attitude,
		VelocityNEDMPS: measurement.VelocityNEDMPS, Quality: measurement.Quality,
	}
	foundation.poses = appendChronologicalPose(foundation.poses, pose)
	if len(foundation.poses) > foundation.config.MaxPoseSamples {
		overflow := len(foundation.poses) - foundation.config.MaxPoseSamples
		copy(foundation.poses, foundation.poses[overflow:])
		foundation.poses = foundation.poses[:foundation.config.MaxPoseSamples]
	}
	return nil
}

func (foundation *Foundation) DropAircraftPose() {
	foundation.mu.Lock()
	foundation.droppedPoses++
	foundation.mu.Unlock()
}

// ObserveAutopilotUnixTime anchors the autopilot's UTC stream separately from
// its boot-time attitude clock. Both resolve through companion monotonic time,
// which makes their relationship explicit without assuming either is local.
func (foundation *Foundation) ObserveAutopilotUnixTime(unixTimestampUS uint64, received CompanionTime) error {
	if unixTimestampUS == 0 {
		return errors.New("autopilot unix timestamp is required")
	}
	if err := validateCompanionTime(received); err != nil {
		return err
	}
	foundation.mu.Lock()
	defer foundation.mu.Unlock()
	foundation.wallClock.observe(received.UnixNS, received.MonotonicNS)
	foundation.autopilotUnixClock.observe(microsecondsToNanoseconds(unixTimestampUS), received.MonotonicNS)
	return nil
}

func (foundation *Foundation) RecordGimbalAttitude(measurement GimbalAttitudeMeasurement) error {
	if measurement.GimbalTimestampUS == 0 {
		return errors.New("gimbal attitude requires a gimbal timestamp")
	}
	if err := validateCompanionTime(measurement.Received); err != nil {
		return err
	}
	if !finiteVector(measurement.EulerForwardDeg) || !finiteVector(measurement.EulerNorthDeg) || !finiteVector(measurement.AngularVelocity) {
		return errors.New("gimbal attitude contains non-finite values")
	}
	forward, forwardOK := normalizeQuaternion(measurement.QuaternionForward)
	north, northOK := normalizeQuaternion(measurement.QuaternionNorth)
	if !forwardOK || !northOK {
		return errors.New("gimbal attitude quaternion is invalid")
	}

	foundation.mu.Lock()
	defer foundation.mu.Unlock()
	foundation.wallClock.observe(measurement.Received.UnixNS, measurement.Received.MonotonicNS)
	clock := foundation.gimbalClocks[measurement.GimbalID]
	if clock == nil {
		clock = newOffsetCorrelator(foundation.config.MaxClockAnchors)
		foundation.gimbalClocks[measurement.GimbalID] = clock
	}
	remoteNS := microsecondsToNanoseconds(measurement.GimbalTimestampUS)
	if clock.observe(remoteNS, measurement.Received.MonotonicNS) {
		foundation.gimbals[measurement.GimbalID] = nil
	}
	resolved, _, _ := clock.resolve(remoteNS)
	observedUnix, _, _ := foundation.wallClock.remoteAt(resolved)
	sample := GimbalAttitude{
		GimbalID: measurement.GimbalID, CompanionMonotonicNS: resolved, ObservedAtUnixNS: observedUnix,
		GimbalTimestampUS: measurement.GimbalTimestampUS, GimbalClockEpoch: clock.epoch,
		EulerForwardDeg: measurement.EulerForwardDeg, EulerNorthDeg: measurement.EulerNorthDeg,
		QuaternionForward: forward, QuaternionNorth: north, AngularVelocity: measurement.AngularVelocity,
	}
	values := appendChronologicalGimbal(foundation.gimbals[measurement.GimbalID], sample)
	if len(values) > foundation.config.MaxGimbalSamplesPerDevice {
		overflow := len(values) - foundation.config.MaxGimbalSamplesPerDevice
		copy(values, values[overflow:])
		values = values[:foundation.config.MaxGimbalSamplesPerDevice]
	}
	foundation.gimbals[measurement.GimbalID] = values
	return nil
}

// ObserveFrameTiming satisfies the perception runtime's narrow timing sink.
// It records only clock anchors; detection completion and UI receipt time are
// intentionally absent from this contract.
func (foundation *Foundation) ObserveFrameTiming(sourceID, streamEpoch string, sourcePTSNS int64, sourcePTSPresent bool, pipelineIngressMonotonicNS, pipelineIngressUnixNS, sourceCaptureUnixNS int64) {
	_, _ = foundation.ResolveFrameTime(VideoFrameTiming{
		SourceID: sourceID, StreamEpoch: streamEpoch, SourcePTSNS: sourcePTSNS, SourcePTSPresent: sourcePTSPresent,
		PipelineIngressMonotonicNS: pipelineIngressMonotonicNS, PipelineIngressUnixNS: pipelineIngressUnixNS,
		SourceCaptureUnixNS: sourceCaptureUnixNS,
	})
}

func (foundation *Foundation) ResolveFrameTime(timing VideoFrameTiming) (FrameTime, error) {
	if strings.TrimSpace(timing.SourceID) == "" || strings.TrimSpace(timing.StreamEpoch) == "" {
		return FrameTime{}, errors.New("video timing requires source and stream epoch")
	}
	if timing.PipelineIngressMonotonicNS <= 0 || timing.PipelineIngressUnixNS <= 0 {
		return FrameTime{}, errors.New("video timing requires pipeline-ingress companion clocks")
	}
	if timing.SourcePTSPresent && timing.SourcePTSNS < 0 {
		return FrameTime{}, errors.New("video source PTS cannot be negative")
	}
	if timing.SourceCaptureUnixNS < 0 {
		return FrameTime{}, errors.New("video source-capture Unix timestamp cannot be negative")
	}

	foundation.mu.Lock()
	defer foundation.mu.Unlock()
	foundation.wallClock.observe(timing.PipelineIngressUnixNS, timing.PipelineIngressMonotonicNS)
	key := videoClockKey(timing.SourceID, timing.StreamEpoch)
	clock := foundation.videoClocks[key]
	if clock == nil {
		clock = newOffsetCorrelator(foundation.config.MaxClockAnchors)
		foundation.videoClocks[key] = clock
		foundation.videoClockOrder = append(foundation.videoClockOrder, key)
		if len(foundation.videoClockOrder) > foundation.config.MaxVideoClockDomains {
			oldest := foundation.videoClockOrder[0]
			foundation.videoClockOrder = foundation.videoClockOrder[1:]
			delete(foundation.videoClocks, oldest)
		}
	}
	resolved := timing.PipelineIngressMonotonicNS
	uncertainty := foundation.config.PipelineIngressTimeUncertainty
	quality := FrameTimePipelineIngressEstimate
	clockEpoch := clock.epoch
	if timing.SourcePTSPresent {
		clock.observe(timing.SourcePTSNS, timing.PipelineIngressMonotonicNS)
		resolved, uncertainty, _ = clock.resolve(timing.SourcePTSNS)
		uncertainty = max(uncertainty, foundation.config.PipelineIngressTimeUncertainty)
		clockEpoch = clock.epoch
	}
	referenceAge := time.Duration(timing.PipelineIngressUnixNS - timing.SourceCaptureUnixNS)
	referencePlausible := timing.SourceCaptureUnixNS > 0 && referenceAge <= foundation.config.MaxSourceReferenceAge && referenceAge >= -foundation.config.MaxSourceReferenceFutureSkew
	if referencePlausible {
		var ok bool
		resolved, uncertainty, ok = foundation.wallClock.resolve(timing.SourceCaptureUnixNS)
		if !ok {
			return FrameTime{}, errors.New("companion wall-clock correlation is not ready")
		}
		uncertainty = max(uncertainty, foundation.config.SourceReferenceTimeUncertainty)
		quality = FrameTimeSourceReference
		clockEpoch = foundation.wallClock.epoch
	}
	observedUnix, wallUncertainty, ok := foundation.wallClock.remoteAt(resolved)
	if !ok {
		return FrameTime{}, errors.New("companion wall-clock correlation is not ready")
	}
	return FrameTime{
		SourceID: timing.SourceID, StreamEpoch: timing.StreamEpoch, SourcePTSNS: timing.SourcePTSNS,
		CompanionMonotonicNS: resolved, ObservedAtUnixNS: observedUnix, Quality: quality,
		ClockEpoch: clockEpoch, Uncertainty: max(uncertainty, wallUncertainty),
	}, nil
}

func (foundation *Foundation) ContextForFrame(timing VideoFrameTiming, gimbalID *int32) (TemporalContext, error) {
	frameTime, err := foundation.ResolveFrameTime(timing)
	if err != nil {
		return TemporalContext{}, err
	}
	pose, err := foundation.PoseAt(frameTime.CompanionMonotonicNS)
	if err != nil {
		return TemporalContext{}, fmt.Errorf("aircraft pose at frame time: %w", err)
	}
	context := TemporalContext{FrameTime: frameTime, Aircraft: pose}
	if gimbalID != nil {
		attitude, err := foundation.GimbalAt(*gimbalID, frameTime.CompanionMonotonicNS)
		if err != nil {
			return TemporalContext{}, fmt.Errorf("gimbal attitude at frame time: %w", err)
		}
		context.Gimbal = &attitude
	}
	return context, nil
}

func (foundation *Foundation) PoseAt(companionMonotonicNS int64) (AircraftPose, error) {
	foundation.mu.RLock()
	defer foundation.mu.RUnlock()
	before, after, exact, err := bracketPose(foundation.poses, companionMonotonicNS)
	if err != nil {
		return AircraftPose{}, err
	}
	if before.Quality.PositionAge > foundation.config.MaxPositionAge || after.Quality.PositionAge > foundation.config.MaxPositionAge {
		return AircraftPose{}, errors.New("aircraft position was stale at the requested time")
	}
	if exact {
		before.Quality = worstPoseQuality(before.Quality, before.Quality, foundation.config.MaxVelocityAge)
		return before, nil
	}
	span := time.Duration(after.CompanionMonotonicNS - before.CompanionMonotonicNS)
	if span > foundation.config.MaxInterpolationGap {
		return AircraftPose{}, fmt.Errorf("aircraft pose interpolation gap %s exceeds %s", span, foundation.config.MaxInterpolationGap)
	}
	ratio := float64(companionMonotonicNS-before.CompanionMonotonicNS) / float64(after.CompanionMonotonicNS-before.CompanionMonotonicNS)
	attitude, _ := normalizeQuaternion(interpolateQuaternion(before.Attitude, after.Attitude, ratio))
	roll, pitch, yaw := quaternionEulerDegrees(attitude)
	observedUnix := interpolateInt64(before.ObservedAtUnixNS, after.ObservedAtUnixNS, ratio)
	return AircraftPose{
		CompanionMonotonicNS: companionMonotonicNS, ObservedAtUnixNS: observedUnix,
		AutopilotTimestampUS: uint64(interpolateInt64(int64(before.AutopilotTimestampUS), int64(after.AutopilotTimestampUS), ratio)),
		AutopilotClockEpoch:  before.AutopilotClockEpoch,
		LatitudeDeg:          interpolate(before.LatitudeDeg, after.LatitudeDeg, ratio),
		LongitudeDeg:         interpolateLongitude(before.LongitudeDeg, after.LongitudeDeg, ratio),
		AltitudeAMSLM:        interpolate(before.AltitudeAMSLM, after.AltitudeAMSLM, ratio),
		RelativeAltitudeM:    interpolate(before.RelativeAltitudeM, after.RelativeAltitudeM, ratio),
		RollDeg:              roll, PitchDeg: pitch, YawDeg: yaw, Attitude: attitude,
		VelocityNEDMPS:    interpolateVector(before.VelocityNEDMPS, after.VelocityNEDMPS, ratio),
		Quality:           worstPoseQuality(before.Quality, after.Quality, foundation.config.MaxVelocityAge),
		InterpolationSpan: span,
	}, nil
}

func (foundation *Foundation) GimbalAt(gimbalID int32, companionMonotonicNS int64) (GimbalAttitude, error) {
	foundation.mu.RLock()
	defer foundation.mu.RUnlock()
	values := foundation.gimbals[gimbalID]
	before, after, exact, err := bracketGimbal(values, companionMonotonicNS)
	if err != nil {
		return GimbalAttitude{}, err
	}
	if exact {
		return before, nil
	}
	span := time.Duration(after.CompanionMonotonicNS - before.CompanionMonotonicNS)
	if span > foundation.config.MaxInterpolationGap {
		return GimbalAttitude{}, fmt.Errorf("gimbal interpolation gap %s exceeds %s", span, foundation.config.MaxInterpolationGap)
	}
	ratio := float64(companionMonotonicNS-before.CompanionMonotonicNS) / float64(after.CompanionMonotonicNS-before.CompanionMonotonicNS)
	forward, _ := normalizeQuaternion(interpolateQuaternion(before.QuaternionForward, after.QuaternionForward, ratio))
	north, _ := normalizeQuaternion(interpolateQuaternion(before.QuaternionNorth, after.QuaternionNorth, ratio))
	return GimbalAttitude{
		GimbalID: gimbalID, CompanionMonotonicNS: companionMonotonicNS,
		ObservedAtUnixNS:  interpolateInt64(before.ObservedAtUnixNS, after.ObservedAtUnixNS, ratio),
		GimbalTimestampUS: uint64(interpolateInt64(int64(before.GimbalTimestampUS), int64(after.GimbalTimestampUS), ratio)),
		GimbalClockEpoch:  before.GimbalClockEpoch,
		EulerForwardDeg:   interpolateAngleVector(before.EulerForwardDeg, after.EulerForwardDeg, ratio),
		EulerNorthDeg:     interpolateAngleVector(before.EulerNorthDeg, after.EulerNorthDeg, ratio),
		QuaternionForward: forward, QuaternionNorth: north,
		AngularVelocity:   interpolateVector(before.AngularVelocity, after.AngularVelocity, ratio),
		InterpolationSpan: span,
	}, nil
}

func (foundation *Foundation) Health() Health {
	foundation.mu.RLock()
	defer foundation.mu.RUnlock()
	health := Health{
		PoseSamples: len(foundation.poses), GimbalSamples: make(map[int32]int, len(foundation.gimbals)),
		DroppedPoseMeasurements: foundation.droppedPoses,
	}
	for id, values := range foundation.gimbals {
		health.GimbalSamples[id] = len(values)
	}
	health.Clocks = append(health.Clocks,
		foundation.wallClock.health("companion_unix"),
		foundation.autopilotClock.health("autopilot_boot"),
		foundation.autopilotUnixClock.health("autopilot_unix"),
	)
	for key, clock := range foundation.videoClocks {
		health.Clocks = append(health.Clocks, clock.health("video_pts:"+key))
	}
	for id, clock := range foundation.gimbalClocks {
		health.Clocks = append(health.Clocks, clock.health(fmt.Sprintf("gimbal:%d", id)))
	}
	sort.Slice(health.Clocks, func(i, j int) bool { return health.Clocks[i].Domain < health.Clocks[j].Domain })
	return health
}

func bracketPose(values []AircraftPose, target int64) (AircraftPose, AircraftPose, bool, error) {
	if len(values) == 0 {
		return AircraftPose{}, AircraftPose{}, false, errors.New("aircraft pose buffer is empty")
	}
	index := sort.Search(len(values), func(index int) bool { return values[index].CompanionMonotonicNS >= target })
	if index < len(values) && values[index].CompanionMonotonicNS == target {
		return values[index], values[index], true, nil
	}
	if index == 0 || index == len(values) {
		return AircraftPose{}, AircraftPose{}, false, errors.New("requested time is outside the aircraft pose buffer")
	}
	return values[index-1], values[index], false, nil
}

func bracketGimbal(values []GimbalAttitude, target int64) (GimbalAttitude, GimbalAttitude, bool, error) {
	if len(values) == 0 {
		return GimbalAttitude{}, GimbalAttitude{}, false, errors.New("gimbal attitude buffer is empty")
	}
	index := sort.Search(len(values), func(index int) bool { return values[index].CompanionMonotonicNS >= target })
	if index < len(values) && values[index].CompanionMonotonicNS == target {
		return values[index], values[index], true, nil
	}
	if index == 0 || index == len(values) {
		return GimbalAttitude{}, GimbalAttitude{}, false, errors.New("requested time is outside the gimbal attitude buffer")
	}
	return values[index-1], values[index], false, nil
}

func appendChronologicalPose(values []AircraftPose, value AircraftPose) []AircraftPose {
	if len(values) == 0 || values[len(values)-1].CompanionMonotonicNS < value.CompanionMonotonicNS {
		return append(values, value)
	}
	index := sort.Search(len(values), func(index int) bool { return values[index].CompanionMonotonicNS >= value.CompanionMonotonicNS })
	if index < len(values) && values[index].CompanionMonotonicNS == value.CompanionMonotonicNS {
		values[index] = value
		return values
	}
	values = append(values, AircraftPose{})
	copy(values[index+1:], values[index:])
	values[index] = value
	return values
}

func appendChronologicalGimbal(values []GimbalAttitude, value GimbalAttitude) []GimbalAttitude {
	if len(values) == 0 || values[len(values)-1].CompanionMonotonicNS < value.CompanionMonotonicNS {
		return append(values, value)
	}
	index := sort.Search(len(values), func(index int) bool { return values[index].CompanionMonotonicNS >= value.CompanionMonotonicNS })
	if index < len(values) && values[index].CompanionMonotonicNS == value.CompanionMonotonicNS {
		values[index] = value
		return values
	}
	values = append(values, GimbalAttitude{})
	copy(values[index+1:], values[index:])
	values[index] = value
	return values
}

func validateCompanionTime(value CompanionTime) error {
	if value.MonotonicNS <= 0 || value.UnixNS <= 0 {
		return errors.New("companion monotonic and unix timestamps are required")
	}
	return nil
}

func microsecondsToNanoseconds(value uint64) int64 {
	if value > math.MaxInt64/1_000 {
		return math.MaxInt64
	}
	return int64(value * 1_000)
}

func normalizeQuaternion(value Quaternion) (Quaternion, bool) {
	if !finite(value.W) || !finite(value.X) || !finite(value.Y) || !finite(value.Z) {
		return Quaternion{}, false
	}
	norm := math.Sqrt(value.W*value.W + value.X*value.X + value.Y*value.Y + value.Z*value.Z)
	if norm < 1e-9 {
		return Quaternion{}, false
	}
	return Quaternion{W: value.W / norm, X: value.X / norm, Y: value.Y / norm, Z: value.Z / norm}, true
}

func quaternionEulerDegrees(value Quaternion) (float64, float64, float64) {
	roll := math.Atan2(2*(value.W*value.X+value.Y*value.Z), 1-2*(value.X*value.X+value.Y*value.Y))
	pitchValue := 2 * (value.W*value.Y - value.Z*value.X)
	pitch := math.Copysign(math.Pi/2, pitchValue)
	if math.Abs(pitchValue) < 1 {
		pitch = math.Asin(pitchValue)
	}
	yaw := math.Atan2(2*(value.W*value.Z+value.X*value.Y), 1-2*(value.Y*value.Y+value.Z*value.Z))
	return roll * 180 / math.Pi, pitch * 180 / math.Pi, yaw * 180 / math.Pi
}

func interpolateQuaternion(first, second Quaternion, ratio float64) Quaternion {
	if first.W*second.W+first.X*second.X+first.Y*second.Y+first.Z*second.Z < 0 {
		second = Quaternion{W: -second.W, X: -second.X, Y: -second.Y, Z: -second.Z}
	}
	return Quaternion{
		W: interpolate(first.W, second.W, ratio), X: interpolate(first.X, second.X, ratio),
		Y: interpolate(first.Y, second.Y, ratio), Z: interpolate(first.Z, second.Z, ratio),
	}
}

func interpolateVector(first, second Vector3, ratio float64) Vector3 {
	return Vector3{X: interpolate(first.X, second.X, ratio), Y: interpolate(first.Y, second.Y, ratio), Z: interpolate(first.Z, second.Z, ratio)}
}

func interpolateAngleVector(first, second Vector3, ratio float64) Vector3 {
	return Vector3{X: interpolateAngle(first.X, second.X, ratio), Y: interpolateAngle(first.Y, second.Y, ratio), Z: interpolateAngle(first.Z, second.Z, ratio)}
}

func interpolateAngle(first, second, ratio float64) float64 {
	delta := math.Mod(second-first+540, 360) - 180
	return first + delta*ratio
}

func interpolateLongitude(first, second, ratio float64) float64 {
	value := interpolateAngle(first, second, ratio)
	if value > 180 {
		value -= 360
	}
	if value < -180 {
		value += 360
	}
	return value
}

func interpolate(first, second, ratio float64) float64 { return first + (second-first)*ratio }

func interpolateInt64(first, second int64, ratio float64) int64 {
	return first + int64(math.Round(float64(second-first)*ratio))
}

func worstPoseQuality(first, second PoseQuality, maxVelocityAge time.Duration) PoseQuality {
	quality := PoseQuality{
		GlobalPositionOK: first.GlobalPositionOK && second.GlobalPositionOK,
		LocalPositionOK:  first.LocalPositionOK && second.LocalPositionOK,
		VelocityValid:    first.VelocityValid && second.VelocityValid,
		PositionAge:      max(first.PositionAge, second.PositionAge), VelocityAge: max(first.VelocityAge, second.VelocityAge),
		HorizontalUncertaintyM: max(first.HorizontalUncertaintyM, second.HorizontalUncertaintyM),
		VerticalUncertaintyM:   max(first.VerticalUncertaintyM, second.VerticalUncertaintyM),
		VelocityUncertaintyMPS: max(first.VelocityUncertaintyMPS, second.VelocityUncertaintyMPS),
	}
	if quality.VelocityAge > maxVelocityAge {
		quality.VelocityValid = false
	}
	return quality
}

func validLatitudeLongitude(latitude, longitude float64) bool {
	return finite(latitude) && finite(longitude) && latitude >= -90 && latitude <= 90 && longitude >= -180 && longitude <= 180
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func finiteVector(value Vector3) bool { return finite(value.X) && finite(value.Y) && finite(value.Z) }

func videoClockKey(sourceID, streamEpoch string) string { return sourceID + "/" + streamEpoch }
