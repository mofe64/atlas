// Package navigation owns the read-only PX4/H-Flow navigation-state data
// plane. It deliberately contains no setpoint or movement-authority API.
package navigation

import "time"

const ProtocolVersion = "1"

type Status string

const (
	StatusReady       Status = "ready"
	StatusDegraded    Status = "degraded"
	StatusStale       Status = "stale"
	StatusUnavailable Status = "unavailable"
)

type Config struct {
	LocalPositionStaleAfter       time.Duration
	LocalPositionHealthStaleAfter time.Duration
	OdometryStaleAfter            time.Duration
	EstimatorStaleAfter           time.Duration
	OpticalFlowStaleAfter         time.Duration
	RangeStaleAfter               time.Duration
	ResetDegradedFor              time.Duration
	HistoryDuration               time.Duration
	MinimumFlowQuality            uint8
}

func DefaultConfig() Config {
	return Config{
		LocalPositionStaleAfter:       750 * time.Millisecond,
		LocalPositionHealthStaleAfter: 2500 * time.Millisecond,
		OdometryStaleAfter:            500 * time.Millisecond,
		EstimatorStaleAfter:           time.Second,
		OpticalFlowStaleAfter:         500 * time.Millisecond,
		RangeStaleAfter:               500 * time.Millisecond,
		ResetDegradedFor:              2 * time.Second,
		HistoryDuration:               15 * time.Second,
		MinimumFlowQuality:            1,
	}
}

type Vector3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

type Quaternion struct {
	W float64 `json:"w"`
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

type ObservationTime struct {
	PX4TimeUS        uint64 `json:"px4TimeUs"`
	AlignedUnixNS    int64  `json:"alignedUnixNs"`
	ReceivedUnixNS   int64  `json:"receivedUnixNs"`
	ClockEpoch       uint64 `json:"clockEpoch"`
	AlignmentErrorNS int64  `json:"alignmentErrorNs"`
}

type LocalPosition struct {
	Time     ObservationTime `json:"time"`
	Position Vector3         `json:"positionNedM"`
	Velocity Vector3         `json:"velocityNedMps"`
}

type Odometry struct {
	Time            ObservationTime `json:"time"`
	FrameID         uint32          `json:"frameId"`
	ChildFrameID    uint32          `json:"childFrameId"`
	Position        Vector3         `json:"positionM"`
	Attitude        Quaternion      `json:"attitude"`
	Velocity        Vector3         `json:"velocityMps"`
	AngularVelocity Vector3         `json:"angularVelocityRps"`
	ResetCounter    uint8           `json:"resetCounter"`
	EstimatorType   uint8           `json:"estimatorType"`
	Quality         int8            `json:"quality"`
}

type EstimatorStatus struct {
	Time                   ObservationTime `json:"time"`
	Flags                  uint32          `json:"flags"`
	VelocityTestRatio      float64         `json:"velocityTestRatio"`
	HorizontalPosTestRatio float64         `json:"horizontalPositionTestRatio"`
	VerticalPosTestRatio   float64         `json:"verticalPositionTestRatio"`
	HeightAGLTestRatio     float64         `json:"heightAglTestRatio"`
}

type OpticalFlow struct {
	Time              ObservationTime `json:"time"`
	SensorID          uint8           `json:"sensorId"`
	IntegrationTimeUS uint32          `json:"integrationTimeUs"`
	IntegratedXRad    float64         `json:"integratedXRad"`
	IntegratedYRad    float64         `json:"integratedYRad"`
	Quality           uint8           `json:"quality"`
	DistanceM         float64         `json:"distanceM"`
}

type Range struct {
	Time          ObservationTime `json:"time"`
	SensorID      uint8           `json:"sensorId"`
	Orientation   uint8           `json:"orientation"`
	MinimumM      float64         `json:"minimumM"`
	MaximumM      float64         `json:"maximumM"`
	CurrentM      float64         `json:"currentM"`
	SignalQuality uint8           `json:"signalQuality"`
}

type ComponentHealth struct {
	Status Status  `json:"status"`
	AgeMS  float64 `json:"ageMs,omitempty"`
	Reason string  `json:"reason,omitempty"`
}

type EstimatorReset struct {
	PreviousCounter uint8 `json:"previousCounter"`
	CurrentCounter  uint8 `json:"currentCounter"`
	ObservedUnixNS  int64 `json:"observedUnixNs"`
}

type State struct {
	ProtocolVersion                   string                     `json:"protocolVersion"`
	Sequence                          uint64                     `json:"sequence"`
	GeneratedAtUnixNS                 int64                      `json:"generatedAtUnixNs"`
	Status                            Status                     `json:"status"`
	Ready                             bool                       `json:"ready"`
	Reasons                           []string                   `json:"reasons,omitempty"`
	ConnectionObserved                bool                       `json:"connectionObserved"`
	Connected                         bool                       `json:"connected"`
	LocalPositionValid                bool                       `json:"localPositionValid"`
	LocalPositionHealthObservedUnixNS int64                      `json:"localPositionHealthObservedUnixNs,omitempty"`
	LocalPosition                     *LocalPosition             `json:"localPosition,omitempty"`
	Odometry                          *Odometry                  `json:"odometry,omitempty"`
	Estimator                         *EstimatorStatus           `json:"estimator,omitempty"`
	OpticalFlow                       *OpticalFlow               `json:"opticalFlow,omitempty"`
	Range                             *Range                     `json:"range,omitempty"`
	LastEstimatorReset                *EstimatorReset            `json:"lastEstimatorReset,omitempty"`
	Components                        map[string]ComponentHealth `json:"components"`
}

type Sample struct {
	State           State `json:"state"`
	CaptureUnixNS   int64 `json:"captureUnixNs"`
	SampleUnixNS    int64 `json:"sampleUnixNs"`
	SkewNS          int64 `json:"skewNs"`
	WithinTolerance bool  `json:"withinTolerance"`
}
