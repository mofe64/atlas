package navigation

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

const (
	estimatorAttitude      = uint32(1 << 0)
	estimatorVelocityHoriz = uint32(1 << 1)
	estimatorVelocityVert  = uint32(1 << 2)
	estimatorPosHorizRel   = uint32(1 << 3)
	estimatorGPSGlitch     = uint32(1 << 10)
	estimatorAccelError    = uint32(1 << 11)
	requiredEstimatorFlags = estimatorAttitude | estimatorVelocityHoriz | estimatorVelocityVert | estimatorPosHorizRel
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
}

func NewPlane(config Config) (*Plane, error) {
	if config.LocalPositionStaleAfter <= 0 || config.LocalPositionHealthStaleAfter <= 0 || config.OdometryStaleAfter <= 0 || config.EstimatorStaleAfter <= 0 || config.OpticalFlowStaleAfter <= 0 || config.RangeStaleAfter <= 0 || config.ResetDegradedFor < 0 {
		return nil, errors.New("navigation durations must be positive")
	}
	return &Plane{config: config}, nil
}

func (plane *Plane) SetConnected(connected bool) {
	plane.mu.Lock()
	defer plane.mu.Unlock()
	plane.connectionKnown, plane.connected = true, connected
	plane.sequence++
}

func (plane *Plane) SetLocalPositionValid(valid bool, observedAt time.Time) {
	plane.mu.Lock()
	defer plane.mu.Unlock()
	plane.localPositionKnown, plane.localPositionValid = true, valid
	plane.localPositionHealthObservedUnixNS = observedAt.UTC().UnixNano()
	plane.sequence++
}

func (plane *Plane) ObserveLocalPosition(sourceUS uint64, receivedAt time.Time, position, velocity Vector3) {
	plane.mu.Lock()
	defer plane.mu.Unlock()
	value := &LocalPosition{Time: plane.clock.align(sourceUS, receivedAt), Position: position, Velocity: velocity}
	plane.localPosition = value
	plane.sequence++
}

func (plane *Plane) ObserveOdometry(sourceUS uint64, receivedAt time.Time, value Odometry) {
	plane.mu.Lock()
	defer plane.mu.Unlock()
	value.Time = plane.clock.align(sourceUS, receivedAt)
	if plane.odometry != nil && value.ResetCounter != plane.odometry.ResetCounter {
		plane.lastReset = &EstimatorReset{PreviousCounter: plane.odometry.ResetCounter, CurrentCounter: value.ResetCounter, ObservedUnixNS: value.Time.AlignedUnixNS}
	}
	plane.odometry = &value
	plane.sequence++
}

func (plane *Plane) ObserveEstimator(sourceUS uint64, receivedAt time.Time, value EstimatorStatus) {
	plane.mu.Lock()
	defer plane.mu.Unlock()
	value.Time = plane.clock.align(sourceUS, receivedAt)
	plane.estimator = &value
	plane.sequence++
}

func (plane *Plane) ObserveOpticalFlow(sourceUS uint64, receivedAt time.Time, value OpticalFlow) {
	plane.mu.Lock()
	defer plane.mu.Unlock()
	value.Time = plane.clock.align(sourceUS, receivedAt)
	plane.opticalFlow = &value
	plane.sequence++
}

func (plane *Plane) ObserveRange(sourceUS uint64, receivedAt time.Time, value Range) {
	plane.mu.Lock()
	defer plane.mu.Unlock()
	value.Time = plane.clock.align(sourceUS, receivedAt)
	plane.rangefinder = &value
	plane.sequence++
}

func (plane *Plane) Latest(now time.Time) State {
	plane.mu.RLock()
	defer plane.mu.RUnlock()
	return plane.snapshotLocked(now.UTC().UnixNano())
}

func (plane *Plane) snapshotLocked(nowUnixNS int64) State {
	state := State{
		Sequence: plane.sequence, GeneratedAtUnixNS: nowUnixNS,
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
	state.HFlowReasons = nil
	state.Components["localPosition"] = localPositionHealth(nowUnixNS, state, config.LocalPositionStaleAfter, config.LocalPositionHealthStaleAfter)
	state.Components["odometry"] = timedHealth(nowUnixNS, timeOf(state.Odometry), config.OdometryStaleAfter, odometryReason(state.Odometry))
	state.Components["estimator"] = timedHealth(nowUnixNS, timeOf(state.Estimator), config.EstimatorStaleAfter, estimatorReason(state.Estimator))
	state.Components["opticalFlow"] = timedHealth(nowUnixNS, timeOf(state.OpticalFlow), config.OpticalFlowStaleAfter, flowReason(state.OpticalFlow, config.MinimumFlowQuality))
	state.Components["range"] = timedHealth(nowUnixNS, timeOf(state.Range), config.RangeStaleAfter, rangeReason(state.Range))

	// The top-level status is generic PX4 navigation readiness. H-Flow is
	// reported separately because consumers that do not use H-Flow must not be
	// disabled by a missing optical-flow sensor or rangefinder.
	state.Status = StatusReady
	if !state.ConnectionObserved || !state.Connected {
		state.Status = StatusUnavailable
		state.Reasons = append(state.Reasons, "PX4 connection not observed or unavailable")
	}
	for _, name := range []string{"localPosition", "odometry", "estimator"} {
		health := state.Components[name]
		state.Status = combineStatus(state.Status, health.Status)
		if health.Status != StatusReady {
			state.Reasons = append(state.Reasons, name+": "+health.Reason)
		}
	}
	if state.LastEstimatorReset != nil && nowUnixNS-state.LastEstimatorReset.ObservedUnixNS <= config.ResetDegradedFor.Nanoseconds() && state.Status == StatusReady {
		state.Status = StatusDegraded
		state.Reasons = append(state.Reasons, "estimator reset settling window active")
	}
	state.Ready = state.Status == StatusReady

	state.HFlowStatus = StatusReady
	if !state.ConnectionObserved || !state.Connected {
		state.HFlowStatus = StatusUnavailable
		state.HFlowReasons = append(state.HFlowReasons, "PX4 connection not observed or unavailable")
	}
	for _, name := range []string{"opticalFlow", "range"} {
		health := state.Components[name]
		state.HFlowStatus = combineStatus(state.HFlowStatus, health.Status)
		if health.Status != StatusReady {
			state.HFlowReasons = append(state.HFlowReasons, name+": "+health.Reason)
		}
	}
	state.HFlowReady = state.HFlowStatus == StatusReady
	return state
}

func combineStatus(current, component Status) Status {
	if current == StatusUnavailable || component == StatusUnavailable {
		return StatusUnavailable
	}
	if current == StatusStale || component == StatusStale {
		return StatusStale
	}
	if current == StatusDegraded || component == StatusDegraded {
		return StatusDegraded
	}
	return StatusReady
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
	for _, ratio := range []float64{value.VelocityTestRatio, value.HorizontalPosTestRatio, value.VerticalPosTestRatio} {
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
