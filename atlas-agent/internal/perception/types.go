// Package perception defines the accelerator-neutral boundary between an
// onboard inference runtime and Atlas Agent.
package perception

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

const (
	// AdapterProtocolVersion belongs to the local Agent-to-runtime Unix socket.
	// It evolves independently from the Agent-to-Native perception transport.
	AdapterProtocolVersion   = "3"
	TransportProtocolVersion = "1"

	// RuntimeProtocolVersion is retained as a source-compatible name for local
	// adapter tests and external providers built against the v1 package API.
	RuntimeProtocolVersion = AdapterProtocolVersion
)

type ModelIdentity struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	ArtifactHash string `json:"artifactHash,omitempty"`
}

type BoundingBox struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type NormalizedPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type CountingRuleType string

const (
	CountingRuleLine    CountingRuleType = "LINE"
	CountingRulePolygon CountingRuleType = "POLYGON"
)

// CountingRule is an operator-configured image-space boundary. Rules are
// replaced atomically by Native and evaluated only against confirmed Atlas
// tracks from the matching camera source.
type CountingRule struct {
	RuleID   string            `json:"ruleId"`
	Label    string            `json:"label"`
	RuleType CountingRuleType  `json:"ruleType"`
	Revision uint64            `json:"revision"`
	Points   []NormalizedPoint `json:"points"`
	ClassIDs []int32           `json:"classIds,omitempty"`
}

type TrackCountEventType string

const (
	TrackCountLineForward  TrackCountEventType = "LINE_FORWARD"
	TrackCountLineReverse  TrackCountEventType = "LINE_REVERSE"
	TrackCountPolygonEntry TrackCountEventType = "POLYGON_ENTRY"
	TrackCountPolygonExit  TrackCountEventType = "POLYGON_EXIT"
)

type TrackRuleCount struct {
	RuleID         string           `json:"ruleId"`
	RuleRevision   uint64           `json:"ruleRevision"`
	RuleType       CountingRuleType `json:"ruleType"`
	LineForward    uint64           `json:"lineForward"`
	LineReverse    uint64           `json:"lineReverse"`
	PolygonEntries uint64           `json:"polygonEntries"`
	PolygonExits   uint64           `json:"polygonExits"`
}

type TrackCountEvent struct {
	EventID        string              `json:"eventId"`
	RuleID         string              `json:"ruleId"`
	RuleRevision   uint64              `json:"ruleRevision"`
	TrackSessionID string              `json:"trackSessionId"`
	TrackID        string              `json:"trackId"`
	EventType      TrackCountEventType `json:"eventType"`
	ObservedAt     time.Time           `json:"observedAt"`
	Anchor         NormalizedPoint     `json:"anchor"`
}

type TrackCountingControl interface {
	ReplaceCountingRules(sourceID string, rules []CountingRule) error
}

// TrackLifecycleState describes Atlas' confidence in a temporary association.
// It is deliberately separate from the backend's internal ByteTrack state.
type TrackLifecycleState string

const (
	TrackLifecycleTentative           TrackLifecycleState = "TENTATIVE"
	TrackLifecycleActive              TrackLifecycleState = "ACTIVE"
	TrackLifecycleTemporarilyOccluded TrackLifecycleState = "TEMPORARILY_OCCLUDED"
	TrackLifecycleLost                TrackLifecycleState = "LOST"
	TrackLifecycleClosed              TrackLifecycleState = "CLOSED"
)

type TrackUpdateReason string

const (
	TrackUpdateCreated      TrackUpdateReason = "CREATED"
	TrackUpdateStateChanged TrackUpdateReason = "STATE_CHANGED"
	TrackUpdateReacquired   TrackUpdateReason = "REACQUIRED"
	TrackUpdatePeriodic     TrackUpdateReason = "PERIODIC"
	TrackUpdateClosed       TrackUpdateReason = "CLOSED"
)

// TrackSnapshot is a bounded, revisioned lifecycle summary. High-frequency
// observation history remains onboard; Native receives state changes and
// periodic summaries rather than one durable database write per video frame.
type TrackSnapshot struct {
	TrackID                   string              `json:"trackId"`
	TrackSessionID            string              `json:"trackSessionId"`
	TrackerType               TrackerAlgorithm    `json:"trackerType"`
	LifecycleState            TrackLifecycleState `json:"lifecycleState"`
	Revision                  uint64              `json:"revision"`
	AgeFrames                 uint64              `json:"ageFrames"`
	ObservationCount          uint64              `json:"observationCount"`
	FirstObservedAt           time.Time           `json:"firstObservedAt"`
	LastObservedAt            time.Time           `json:"lastObservedAt"`
	LatestConfirmedBox        BoundingBox         `json:"latestConfirmedBox"`
	LatestDetectionConfidence float64             `json:"latestDetectionConfidence"`
	PredictedBox              *BoundingBox        `json:"predictedBox,omitempty"`
	PredictionConfidence      float64             `json:"predictionConfidence"`
	ClosedAt                  time.Time           `json:"closedAt,omitempty"`
	ClosureReason             string              `json:"closureReason,omitempty"`
	ClassID                   int32               `json:"classId"`
	ClassLabel                string              `json:"classLabel"`
	UpdateReason              TrackUpdateReason   `json:"updateReason"`
}

// TrackUpdateBatch is independent of high-rate frame subscriptions. This lets
// Native durably record lifecycle changes even when no video view is mounted.
type TrackUpdateBatch struct {
	SourceID         string            `json:"sourceId"`
	StreamEpoch      string            `json:"streamEpoch"`
	TrackSessionID   string            `json:"trackSessionId"`
	TrackerType      TrackerAlgorithm  `json:"trackerType"`
	ObservedAt       time.Time         `json:"observedAt"`
	SessionStarted   bool              `json:"sessionStarted,omitempty"`
	SessionEnded     bool              `json:"sessionEnded,omitempty"`
	SessionEndReason string            `json:"sessionEndReason,omitempty"`
	CurrentVisible   uint64            `json:"currentVisible"`
	UniqueConfirmed  uint64            `json:"uniqueConfirmed"`
	Tracks           []TrackSnapshot   `json:"tracks"`
	RuleCounts       []TrackRuleCount  `json:"ruleCounts,omitempty"`
	CountEvents      []TrackCountEvent `json:"countEvents,omitempty"`
}

// TrackFollowObservation is the latest in-memory state needed by the onboard
// image-space gimbal loop. It deliberately does not create another frame or
// lifecycle subscriber: the controller asks for one exact session-scoped
// track and never risks consuming transport-owned updates.
type TrackFollowObservation struct {
	SourceID                  string
	StreamEpoch               string
	TrackSessionID            string
	TrackID                   string
	LifecycleState            TrackLifecycleState
	LastObservedAt            time.Time
	LatestConfirmedBox        BoundingBox
	LatestDetectionConfidence float64
	SourcePTSNS               int64
	FrameTiming               FrameTiming
}

// TrackFollowSource exposes only exact (track session, track) lookups. A
// tracker reset therefore makes the old identity unresolvable instead of
// allowing a controller to attach to a recycled backend ID.
type TrackFollowSource interface {
	TrackForFollow(trackSessionID, trackID string) (TrackFollowObservation, bool)
}

type Detection struct {
	// TrackID is authoritative only after the Atlas-owned tracking stage.
	TrackID string `json:"trackId,omitempty"`
	// UpstreamTrackID preserves optional provider provenance but is never sent
	// to Native as an Atlas association.
	UpstreamTrackID string          `json:"upstreamTrackId,omitempty"`
	ClassID         int32           `json:"classId"`
	ClassLabel      string          `json:"classLabel"`
	Confidence      float64         `json:"confidence"`
	BoundingBox     BoundingBox     `json:"boundingBox"`
	AttributesRaw   json.RawMessage `json:"attributes,omitempty"`
}

type CameraMotionEstimate struct {
	Method     string    `json:"method"`
	Homography []float64 `json:"homography"`
	Confidence float64   `json:"confidence"`
}

// FrameTiming identifies the earliest frame instant currently observable by
// the provider. Pipeline ingress is captured before inference. A provider may
// additionally supply a sensor/RTCP reference timestamp in SourceCaptureUnixNS;
// the distinction is retained because ingress time includes transport/decode
// latency and must not be presented as exact camera exposure time.
type FrameTiming struct {
	SourcePTSPresent           bool  `json:"sourcePtsPresent"`
	PipelineIngressMonotonicNS int64 `json:"pipelineIngressMonotonicNs"`
	PipelineIngressUnixNS      int64 `json:"pipelineIngressUnixNs"`
	SourceCaptureUnixNS        int64 `json:"sourceCaptureUnixNs,omitempty"`
}

type Frame struct {
	SourceID           string                `json:"sourceId"`
	StreamEpoch        string                `json:"streamEpoch"`
	FrameID            string                `json:"frameId"`
	ObservedAt         time.Time             `json:"observedAt"`
	SourcePTSNS        int64                 `json:"sourcePtsNs"`
	Timing             *FrameTiming          `json:"timing,omitempty"`
	ImageWidth         uint32                `json:"imageWidth"`
	ImageHeight        uint32                `json:"imageHeight"`
	Model              ModelIdentity         `json:"model"`
	CameraMotion       *CameraMotionEstimate `json:"cameraMotion,omitempty"`
	InferenceLatencyMS float64               `json:"inferenceLatencyMs"`
	Detections         []Detection           `json:"detections"`
}

type Health struct {
	SourceID                          string          `json:"sourceId"`
	Provider                          string          `json:"provider"`
	ActivationState                   string          `json:"activationState,omitempty"`
	Accelerator                       string          `json:"accelerator,omitempty"`
	InputConnected                    bool            `json:"inputConnected"`
	InferenceReady                    bool            `json:"inferenceReady"`
	OutputPublishing                  bool            `json:"outputPublishing"`
	InputFPS                          float64         `json:"inputFps"`
	InferenceFPS                      float64         `json:"inferenceFps"`
	DroppedFrames                     uint64          `json:"droppedFrames"`
	SourceReferenceTimestampSupported bool            `json:"sourceReferenceTimestampSupported,omitempty"`
	SourceReferenceFrames             uint64          `json:"sourceReferenceFrames,omitempty"`
	LastFrameAt                       time.Time       `json:"lastFrameAt,omitempty"`
	LastDetectionAt                   time.Time       `json:"lastDetectionAt,omitempty"`
	LastError                         string          `json:"lastError,omitempty"`
	Model                             ModelIdentity   `json:"model"`
	ObservedAt                        time.Time       `json:"observedAt"`
	Tracking                          *TrackingHealth `json:"tracking,omitempty"`
}

type TrackingHealth struct {
	Algorithm              TrackerAlgorithm    `json:"algorithm"`
	State                  string              `json:"state"`
	SessionID              string              `json:"sessionId,omitempty"`
	LastResetReason        TrackingResetReason `json:"lastResetReason,omitempty"`
	ResetCount             uint64              `json:"resetCount"`
	LastError              string              `json:"lastError,omitempty"`
	CameraMotionState      string              `json:"cameraMotionState"`
	CameraMotionMethod     string              `json:"cameraMotionMethod,omitempty"`
	CameraMotionConfidence float64             `json:"cameraMotionConfidence"`
	ReIDEnabled            bool                `json:"reIdEnabled"`
}

type Outputs struct {
	Frames        <-chan Frame
	Health        <-chan Health
	TrackUpdates  <-chan TrackUpdateBatch
	Control       Control
	Counting      TrackCountingControl
	TrackFollower TrackFollowSource
}

func (frame Frame) Validate() error {
	if strings.TrimSpace(frame.SourceID) == "" || strings.TrimSpace(frame.StreamEpoch) == "" || strings.TrimSpace(frame.FrameID) == "" {
		return errors.New("sourceId, streamEpoch, and frameId are required")
	}
	if frame.ObservedAt.IsZero() {
		return errors.New("observedAt is required")
	}
	if frame.ImageWidth == 0 || frame.ImageHeight == 0 {
		return errors.New("imageWidth and imageHeight must be positive")
	}
	if strings.TrimSpace(frame.Model.Name) == "" || strings.TrimSpace(frame.Model.Version) == "" {
		return errors.New("model name and version are required")
	}
	if !finiteNonNegative(frame.InferenceLatencyMS) {
		return errors.New("inferenceLatencyMs must be finite and non-negative")
	}
	if frame.Timing != nil {
		if err := frame.Timing.Validate(frame.SourcePTSNS); err != nil {
			return fmt.Errorf("timing: %w", err)
		}
	}
	if frame.CameraMotion != nil {
		if err := frame.CameraMotion.Validate(); err != nil {
			return fmt.Errorf("cameraMotion: %w", err)
		}
	}
	for index, detection := range frame.Detections {
		if err := detection.Validate(); err != nil {
			return fmt.Errorf("detection %d: %w", index, err)
		}
	}
	return nil
}

func (timing FrameTiming) Validate(sourcePTSNS int64) error {
	if timing.PipelineIngressMonotonicNS <= 0 || timing.PipelineIngressUnixNS <= 0 {
		return errors.New("pipeline ingress monotonic and unix timestamps are required")
	}
	if timing.SourcePTSPresent && sourcePTSNS < 0 {
		return errors.New("source PTS cannot be negative when present")
	}
	if timing.SourceCaptureUnixNS < 0 {
		return errors.New("source capture unix timestamp cannot be negative")
	}
	return nil
}

func (motion CameraMotionEstimate) Validate() error {
	if strings.TrimSpace(motion.Method) == "" {
		return errors.New("method is required")
	}
	if len(motion.Homography) != 9 {
		return errors.New("homography must contain nine values")
	}
	for _, value := range motion.Homography {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return errors.New("homography values must be finite")
		}
	}
	if !finiteUnit(motion.Confidence) {
		return errors.New("confidence must be between 0 and 1")
	}
	return nil
}

func (detection Detection) Validate() error {
	if strings.TrimSpace(detection.ClassLabel) == "" {
		return errors.New("classLabel is required")
	}
	if !finiteUnit(detection.Confidence) {
		return errors.New("confidence must be between 0 and 1")
	}
	box := detection.BoundingBox
	if !finiteUnit(box.X) || !finiteUnit(box.Y) || !finiteUnit(box.Width) || !finiteUnit(box.Height) {
		return errors.New("boundingBox values must be between 0 and 1")
	}
	const coordinateTolerance = 1e-9
	if box.X+box.Width > 1+coordinateTolerance || box.Y+box.Height > 1+coordinateTolerance {
		return errors.New("boundingBox must remain inside the normalized frame")
	}
	if len(detection.AttributesRaw) > 0 && !json.Valid(detection.AttributesRaw) {
		return errors.New("attributes must contain valid JSON")
	}
	return nil
}

func (health Health) Validate() error {
	if strings.TrimSpace(health.SourceID) == "" || strings.TrimSpace(health.Provider) == "" {
		return errors.New("sourceId and provider are required")
	}
	if health.ObservedAt.IsZero() {
		return errors.New("observedAt is required")
	}
	if !finiteNonNegative(health.InputFPS) || !finiteNonNegative(health.InferenceFPS) {
		return errors.New("inputFps and inferenceFps must be finite and non-negative")
	}
	if health.ActivationState != "" && health.ActivationState != "ACTIVE" && health.ActivationState != "INACTIVE" && health.ActivationState != "FAILED" {
		return errors.New("activationState must be ACTIVE, INACTIVE, or FAILED")
	}
	modelNamePresent := strings.TrimSpace(health.Model.Name) != ""
	modelVersionPresent := strings.TrimSpace(health.Model.Version) != ""
	if modelNamePresent != modelVersionPresent {
		return errors.New("model name and version must either both be present or both be absent")
	}
	if health.Tracking != nil {
		if err := health.Tracking.Validate(); err != nil {
			return fmt.Errorf("tracking: %w", err)
		}
	}
	return nil
}

func (health TrackingHealth) Validate() error {
	if health.Algorithm != TrackerAlgorithmDisabled && health.Algorithm != TrackerAlgorithmByteTrack && health.Algorithm != TrackerAlgorithmByteTrackCMC {
		return errors.New("algorithm must be DISABLED, BYTE_TRACK, or BYTE_TRACK_CMC")
	}
	if health.State != "DISABLED" && health.State != "READY" && health.State != "ACTIVE" && health.State != "DEGRADED" {
		return errors.New("state must be DISABLED, READY, ACTIVE, or DEGRADED")
	}
	if health.State == "ACTIVE" && strings.TrimSpace(health.SessionID) == "" {
		return errors.New("sessionId is required while active")
	}
	if health.CameraMotionState != "DISABLED" && health.CameraMotionState != "WAITING" && health.CameraMotionState != "ACTIVE" && health.CameraMotionState != "DEGRADED" {
		return errors.New("cameraMotionState must be DISABLED, WAITING, ACTIVE, or DEGRADED")
	}
	if !finiteUnit(health.CameraMotionConfidence) {
		return errors.New("cameraMotionConfidence must be between 0 and 1")
	}
	if health.CameraMotionState == "ACTIVE" && strings.TrimSpace(health.CameraMotionMethod) == "" {
		return errors.New("cameraMotionMethod is required while camera motion is active")
	}
	return nil
}

func finiteUnit(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

func finiteNonNegative(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0
}
