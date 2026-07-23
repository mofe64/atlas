package navigation

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

const (
	estimatorAttitude      = uint32(1 << 0)
	estimatorVelocityHoriz = uint32(1 << 1)
	estimatorVelocityVert  = uint32(1 << 2)
	estimatorPosHorizRel   = uint32(1 << 3)
	estimatorPosVertAGL    = uint32(1 << 6)
	estimatorGPSGlitch     = uint32(1 << 10)
	estimatorAccelError    = uint32(1 << 11)
	requiredEstimatorFlags = estimatorAttitude | estimatorVelocityHoriz | estimatorVelocityVert | estimatorPosHorizRel | estimatorPosVertAGL
)

type Plane struct {
	mu                                sync.RWMutex
	config                            Config
	clock                             clockAligner
	sequence                          uint64
	connectionKnown                   bool
	connected                         bool
	localPositionKnown                bool
	localPositionValid                bool
	localPositionHealthObservedUnixNS int64
	localPosition                     *LocalPosition
	odometry                          *Odometry
	estimator                         *EstimatorStatus
	opticalFlow                       *OpticalFlow
	rangefinder                       *Range
	lastReset                         *EstimatorReset
	history                           []State
}

func NewPlane(config Config) (*Plane, error) {
	if config.LocalPositionStaleAfter <= 0 || config.LocalPositionHealthStaleAfter <= 0 || config.OdometryStaleAfter <= 0 || config.EstimatorStaleAfter <= 0 || config.OpticalFlowStaleAfter <= 0 || config.RangeStaleAfter <= 0 || config.ResetDegradedFor < 0 || config.HistoryDuration <= 0 {
		return nil, errors.New("navigation durations must be positive")
	}
	return &Plane{config: config}, nil
}

func (plane *Plane) SetConnected(connected bool, observedAt time.Time) {
	plane.mu.Lock()
	defer plane.mu.Unlock()
	plane.connectionKnown, plane.connected = true, connected
	plane.recordLocked(observedAt.UTC().UnixNano())
}

func (plane *Plane) SetLocalPositionValid(valid bool, observedAt time.Time) {
	plane.mu.Lock()
	defer plane.mu.Unlock()
	plane.localPositionKnown, plane.localPositionValid = true, valid
	plane.localPositionHealthObservedUnixNS = observedAt.UTC().UnixNano()
	plane.recordLocked(observedAt.UTC().UnixNano())
}

func (plane *Plane) ObserveLocalPosition(sourceUS uint64, receivedAt time.Time, position, velocity Vector3) {
	plane.mu.Lock()
	defer plane.mu.Unlock()
	value := &LocalPosition{Time: plane.clock.align(sourceUS, receivedAt), Position: position, Velocity: velocity}
	plane.localPosition = value
	plane.recordLocked(value.Time.AlignedUnixNS)
}

func (plane *Plane) ObserveOdometry(sourceUS uint64, receivedAt time.Time, value Odometry) {
	plane.mu.Lock()
	defer plane.mu.Unlock()
	value.Time = plane.clock.align(sourceUS, receivedAt)
	if plane.odometry != nil && value.ResetCounter != plane.odometry.ResetCounter {
		plane.lastReset = &EstimatorReset{PreviousCounter: plane.odometry.ResetCounter, CurrentCounter: value.ResetCounter, ObservedUnixNS: value.Time.AlignedUnixNS}
	}
	plane.odometry = &value
	plane.recordLocked(value.Time.AlignedUnixNS)
}

func (plane *Plane) ObserveEstimator(sourceUS uint64, receivedAt time.Time, value EstimatorStatus) {
	plane.mu.Lock()
	defer plane.mu.Unlock()
	value.Time = plane.clock.align(sourceUS, receivedAt)
	plane.estimator = &value
	plane.recordLocked(value.Time.AlignedUnixNS)
}

func (plane *Plane) ObserveOpticalFlow(sourceUS uint64, receivedAt time.Time, value OpticalFlow) {
	plane.mu.Lock()
	defer plane.mu.Unlock()
	value.Time = plane.clock.align(sourceUS, receivedAt)
	plane.opticalFlow = &value
	plane.recordLocked(value.Time.AlignedUnixNS)
}

func (plane *Plane) ObserveRange(sourceUS uint64, receivedAt time.Time, value Range) {
	plane.mu.Lock()
	defer plane.mu.Unlock()
	value.Time = plane.clock.align(sourceUS, receivedAt)
	plane.rangefinder = &value
	plane.recordLocked(value.Time.AlignedUnixNS)
}

func (plane *Plane) Latest(now time.Time) State {
	plane.mu.RLock()
	defer plane.mu.RUnlock()
	return plane.snapshotLocked(now.UTC().UnixNano())
}

func (plane *Plane) SampleAt(captureUnixNS, maximumSkewNS int64) (Sample, error) {
	if captureUnixNS <= 0 || maximumSkewNS <= 0 {
		return Sample{}, errors.New("captureUnixNs and maxSkewNs must be positive")
	}
	plane.mu.RLock()
	defer plane.mu.RUnlock()
	if len(plane.history) == 0 {
		return Sample{}, errors.New("navigation history is unavailable")
	}
	index := sort.Search(len(plane.history), func(index int) bool {
		return plane.history[index].GeneratedAtUnixNS > captureUnixNS
	}) - 1
	if index < 0 {
		index = 0
	}
	state := plane.history[index]
	stripObservationsAfter(&state, captureUnixNS)
	state = evaluate(state, captureUnixNS, plane.config)
	sampleUnixNS, skew, withinTolerance := int64(0), int64(0), false
	if state.Odometry != nil {
		sampleUnixNS = state.Odometry.Time.AlignedUnixNS
		skew = captureUnixNS - sampleUnixNS
		if skew < 0 {
			skew = -skew
		}
		withinTolerance = skew <= maximumSkewNS
	}
	return Sample{State: state, CaptureUnixNS: captureUnixNS, SampleUnixNS: sampleUnixNS, SkewNS: skew, WithinTolerance: withinTolerance}, nil
}

func stripObservationsAfter(state *State, captureUnixNS int64) {
	if state.LocalPositionHealthObservedUnixNS > captureUnixNS {
		state.LocalPositionHealthObservedUnixNS = 0
	}
	if state.LocalPosition != nil && state.LocalPosition.Time.AlignedUnixNS > captureUnixNS {
		state.LocalPosition = nil
	}
	if state.Odometry != nil && state.Odometry.Time.AlignedUnixNS > captureUnixNS {
		state.Odometry = nil
	}
	if state.Estimator != nil && state.Estimator.Time.AlignedUnixNS > captureUnixNS {
		state.Estimator = nil
	}
	if state.OpticalFlow != nil && state.OpticalFlow.Time.AlignedUnixNS > captureUnixNS {
		state.OpticalFlow = nil
	}
	if state.Range != nil && state.Range.Time.AlignedUnixNS > captureUnixNS {
		state.Range = nil
	}
	if state.LastEstimatorReset != nil && state.LastEstimatorReset.ObservedUnixNS > captureUnixNS {
		state.LastEstimatorReset = nil
	}
}

func (plane *Plane) recordLocked(atUnixNS int64) {
	plane.sequence++
	state := plane.snapshotLocked(atUnixNS)
	plane.history = append(plane.history, state)
	sort.SliceStable(plane.history, func(left, right int) bool {
		return plane.history[left].GeneratedAtUnixNS < plane.history[right].GeneratedAtUnixNS
	})
	cutoff := atUnixNS - plane.config.HistoryDuration.Nanoseconds()
	first := sort.Search(len(plane.history), func(index int) bool { return plane.history[index].GeneratedAtUnixNS >= cutoff })
	if first > 0 {
		plane.history = append([]State(nil), plane.history[first:]...)
	}
}

func (plane *Plane) snapshotLocked(nowUnixNS int64) State {
	state := State{
		ProtocolVersion: ProtocolVersion, Sequence: plane.sequence, GeneratedAtUnixNS: nowUnixNS,
		ConnectionObserved:                plane.connectionKnown,
		Connected:                         plane.connectionKnown && plane.connected,
		LocalPositionValid:                !plane.localPositionKnown || plane.localPositionValid,
		LocalPositionHealthObservedUnixNS: plane.localPositionHealthObservedUnixNS,
		LocalPosition:                     clone(plane.localPosition), Odometry: clone(plane.odometry), Estimator: clone(plane.estimator),
		OpticalFlow: clone(plane.opticalFlow), Range: clone(plane.rangefinder), LastEstimatorReset: clone(plane.lastReset),
	}
	return evaluate(state, nowUnixNS, plane.config)
}

func evaluate(state State, nowUnixNS int64, config Config) State {
	state.Components = make(map[string]ComponentHealth, 5)
	state.Reasons = nil
	state.Components["localPosition"] = localPositionHealth(nowUnixNS, state, config.LocalPositionStaleAfter, config.LocalPositionHealthStaleAfter)
	state.Components["odometry"] = timedHealth(nowUnixNS, timeOf(state.Odometry), config.OdometryStaleAfter, odometryReason(state.Odometry))
	state.Components["estimator"] = timedHealth(nowUnixNS, timeOf(state.Estimator), config.EstimatorStaleAfter, estimatorReason(state.Estimator))
	state.Components["opticalFlow"] = timedHealth(nowUnixNS, timeOf(state.OpticalFlow), config.OpticalFlowStaleAfter, flowReason(state.OpticalFlow, config.MinimumFlowQuality))
	state.Components["range"] = timedHealth(nowUnixNS, timeOf(state.Range), config.RangeStaleAfter, rangeReason(state.Range))
	state.Status = StatusReady
	if !state.ConnectionObserved || !state.Connected {
		state.Status = StatusUnavailable
		state.Reasons = append(state.Reasons, "PX4 connection not observed or unavailable")
	}
	for _, name := range []string{"localPosition", "odometry", "estimator", "opticalFlow", "range"} {
		health := state.Components[name]
		if health.Status == StatusUnavailable {
			state.Status = StatusUnavailable
		} else if health.Status == StatusStale && state.Status != StatusUnavailable {
			state.Status = StatusStale
		} else if health.Status == StatusDegraded && state.Status == StatusReady {
			state.Status = StatusDegraded
		}
		if health.Status != StatusReady {
			state.Reasons = append(state.Reasons, name+": "+health.Reason)
		}
	}
	if state.LastEstimatorReset != nil && nowUnixNS-state.LastEstimatorReset.ObservedUnixNS <= config.ResetDegradedFor.Nanoseconds() && state.Status == StatusReady {
		state.Status = StatusDegraded
		state.Reasons = append(state.Reasons, "estimator reset settling window active")
	}
	state.Ready = state.Status == StatusReady
	return state
}

func timedHealth(nowUnixNS int64, observed *ObservationTime, staleAfter time.Duration, degradedReason string) ComponentHealth {
	if observed == nil {
		return ComponentHealth{Status: StatusUnavailable, Reason: "not observed"}
	}
	ageNS := nowUnixNS - observed.AlignedUnixNS
	if ageNS < 0 {
		ageNS = -ageNS
	}
	ageMS := float64(ageNS) / float64(time.Millisecond)
	if ageNS > staleAfter.Nanoseconds() {
		return ComponentHealth{Status: StatusStale, AgeMS: ageMS, Reason: fmt.Sprintf("last sample is %.1f ms old", ageMS)}
	}
	if degradedReason != "" {
		return ComponentHealth{Status: StatusDegraded, AgeMS: ageMS, Reason: degradedReason}
	}
	return ComponentHealth{Status: StatusReady, AgeMS: ageMS}
}

func localPositionHealth(nowUnixNS int64, state State, sampleStaleAfter, healthStaleAfter time.Duration) ComponentHealth {
	health := timedHealth(nowUnixNS, timeOf(state.LocalPosition), sampleStaleAfter, "")
	if health.Status == StatusUnavailable || health.Status == StatusStale {
		return health
	}
	if state.LocalPositionHealthObservedUnixNS == 0 {
		return ComponentHealth{Status: StatusUnavailable, AgeMS: health.AgeMS, Reason: "PX4 local-position health not observed"}
	}
	healthAgeNS := nowUnixNS - state.LocalPositionHealthObservedUnixNS
	if healthAgeNS < 0 {
		healthAgeNS = -healthAgeNS
	}
	if healthAgeNS > healthStaleAfter.Nanoseconds() {
		return ComponentHealth{Status: StatusStale, AgeMS: float64(healthAgeNS) / float64(time.Millisecond), Reason: "PX4 local-position health is stale"}
	}
	if !state.LocalPositionValid {
		return ComponentHealth{Status: StatusDegraded, AgeMS: health.AgeMS, Reason: "PX4 local-position health is false"}
	}
	return health
}

func odometryReason(value *Odometry) string {
	if value == nil {
		return ""
	}
	if value.Quality < 0 {
		return "PX4 reports failed odometry quality"
	}
	for _, field := range []float64{
		value.Position.X, value.Position.Y, value.Position.Z,
		value.Velocity.X, value.Velocity.Y, value.Velocity.Z,
		value.Attitude.W, value.Attitude.X, value.Attitude.Y, value.Attitude.Z,
	} {
		if !finite(field) {
			return "odometry contains a non-finite value"
		}
	}
	normSquared := value.Attitude.W*value.Attitude.W + value.Attitude.X*value.Attitude.X + value.Attitude.Y*value.Attitude.Y + value.Attitude.Z*value.Attitude.Z
	if normSquared < 0.8 || normSquared > 1.2 {
		return "odometry attitude quaternion is not normalized"
	}
	return ""
}

func estimatorReason(value *EstimatorStatus) string {
	if value == nil {
		return ""
	}
	missing := requiredEstimatorFlags &^ value.Flags
	if missing != 0 {
		return fmt.Sprintf("required estimator flags missing: 0x%x", missing)
	}
	if value.Flags&(estimatorGPSGlitch|estimatorAccelError) != 0 {
		return "estimator reports GPS glitch or acceleration error"
	}
	for _, ratio := range []float64{value.VelocityTestRatio, value.HorizontalPosTestRatio, value.VerticalPosTestRatio, value.HeightAGLTestRatio} {
		if math.IsNaN(ratio) || math.IsInf(ratio, 0) || ratio < 0 || ratio > 1 {
			return "estimator innovation test ratio exceeds 1"
		}
	}
	return ""
}

func flowReason(value *OpticalFlow, minimum uint8) string {
	if value == nil {
		return ""
	}
	if value.IntegrationTimeUS == 0 || !finite(value.IntegratedXRad) || !finite(value.IntegratedYRad) {
		return "optical-flow integration is invalid"
	}
	if value.Quality < minimum {
		return fmt.Sprintf("optical-flow quality %d is below %d", value.Quality, minimum)
	}
	return ""
}

func rangeReason(value *Range) string {
	if value == nil {
		return ""
	}
	if !finite(value.MinimumM) || !finite(value.MaximumM) || value.MinimumM < 0 || value.MaximumM <= value.MinimumM {
		return "range sensor bounds are invalid"
	}
	if !finite(value.CurrentM) || value.CurrentM < value.MinimumM || value.CurrentM > value.MaximumM {
		return "range is invalid or outside the sensor bounds"
	}
	return ""
}

func finite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func timeOf[T interface {
	LocalPosition | Odometry | EstimatorStatus | OpticalFlow | Range
}](value *T) *ObservationTime {
	if value == nil {
		return nil
	}
	switch typed := any(value).(type) {
	case *LocalPosition:
		return &typed.Time
	case *Odometry:
		return &typed.Time
	case *EstimatorStatus:
		return &typed.Time
	case *OpticalFlow:
		return &typed.Time
	case *Range:
		return &typed.Time
	default:
		return nil
	}
}

func clone[T any](value *T) *T {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
