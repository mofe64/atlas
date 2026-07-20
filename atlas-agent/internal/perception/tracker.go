package perception

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// TrackerAlgorithm identifies an association implementation without exposing
// provider-specific types to the rest of Agent.
type TrackerAlgorithm string

const (
	TrackerAlgorithmDisabled     TrackerAlgorithm = "DISABLED"
	TrackerAlgorithmByteTrack    TrackerAlgorithm = "BYTE_TRACK"
	TrackerAlgorithmByteTrackCMC TrackerAlgorithm = "BYTE_TRACK_CMC"
)

// TrackingResetReason is evidence explaining why Atlas ended one temporary
// association session and started another.
type TrackingResetReason string

const (
	TrackingResetActivated          TrackingResetReason = "PERCEPTION_ACTIVATED"
	TrackingResetDeactivated        TrackingResetReason = "PERCEPTION_DEACTIVATED"
	TrackingResetRuntimeReconnected TrackingResetReason = "RUNTIME_RECONNECTED"
	TrackingResetSourceChanged      TrackingResetReason = "SOURCE_CHANGED"
	TrackingResetStreamChanged      TrackingResetReason = "STREAM_EPOCH_CHANGED"
	TrackingResetModelChanged       TrackingResetReason = "MODEL_CHANGED"
	TrackingResetDimensionsChanged  TrackingResetReason = "FRAME_DIMENSIONS_CHANGED"
	TrackingResetTimestampRegressed TrackingResetReason = "TIMESTAMP_REGRESSED"
	TrackingResetTimestampGap       TrackingResetReason = "TIMESTAMP_GAP"
	TrackingResetTrackerFailure     TrackingResetReason = "TRACKER_FAILURE"
	defaultTrackerTimestampGap                          = 2 * time.Second
	trackUpdateBufferSize                               = 256
)

// TrackLifecycleConfig bounds in-memory history, extrapolation, and how often
// unchanged summaries cross the durable Native boundary.
type TrackLifecycleConfig struct {
	ConfirmationObservations uint64
	PredictionHorizon        time.Duration
	LostAfter                time.Duration
	CloseAfter               time.Duration
	SnapshotInterval         time.Duration
	MaxHistoryObservations   int
}

func DefaultTrackLifecycleConfig() TrackLifecycleConfig {
	return TrackLifecycleConfig{
		ConfirmationObservations: 2,
		PredictionHorizon:        750 * time.Millisecond,
		LostAfter:                time.Second,
		CloseAfter:               3 * time.Second,
		SnapshotInterval:         time.Second,
		MaxHistoryObservations:   60,
	}
}

func (config TrackLifecycleConfig) Validate() error {
	if config.ConfirmationObservations < 1 || config.ConfirmationObservations > 10 {
		return errors.New("track confirmation observations must be between 1 and 10")
	}
	if config.PredictionHorizon < 100*time.Millisecond || config.PredictionHorizon > 10*time.Second {
		return errors.New("track prediction horizon must be between 100ms and 10s")
	}
	if config.LostAfter < config.PredictionHorizon || config.LostAfter > 30*time.Second {
		return errors.New("track lost threshold must be at least the prediction horizon and at most 30s")
	}
	if config.CloseAfter <= config.LostAfter || config.CloseAfter > 2*time.Minute {
		return errors.New("track close threshold must be greater than the lost threshold and at most 2m")
	}
	if config.SnapshotInterval < 100*time.Millisecond || config.SnapshotInterval > 30*time.Second {
		return errors.New("track snapshot interval must be between 100ms and 30s")
	}
	if config.MaxHistoryObservations < 2 || config.MaxHistoryObservations > 600 {
		return errors.New("track history must retain between 2 and 600 observations")
	}
	return nil
}

// TrackerFrame is the normalized input shared by every Atlas-owned tracker.
// CameraMotion is optional. The byte_track_cmc backend applies estimates that
// meet its confidence threshold and otherwise degrades safely to identity.
type TrackerFrame struct {
	SourceID     string
	StreamEpoch  string
	FrameID      string
	ObservedAt   time.Time
	SourcePTSNS  int64
	ImageWidth   uint32
	ImageHeight  uint32
	Model        ModelIdentity
	CameraMotion *CameraMotionEstimate
	Detections   []Detection
}

// TrackAssociation connects one detection in the current frame to a
// backend-local track key. A backend must not reuse a key before Reset.
type TrackAssociation struct {
	DetectionIndex int
	TrackKey       string
}

// TrackerBackend is the algorithm boundary implemented by the ByteTrack modes.
// It deliberately returns backend-local keys; TrackingStage owns the
// operator-visible Atlas IDs and session lifecycle.
type TrackerBackend interface {
	Algorithm() TrackerAlgorithm
	Track(TrackerFrame) ([]TrackAssociation, error)
	Reset() error
}

type trackerFeatureReporter interface {
	CameraMotionCompensationEnabled() bool
	CameraMotionMinimumConfidence() float64
	ReIDEnabled() bool
}

// TrackingStage owns continuity checks, temporary Atlas IDs, and graceful
// degradation around an algorithm backend.
type TrackingStage struct {
	mu              sync.Mutex
	backend         TrackerBackend
	maxTimestampGap time.Duration
	lifecycle       TrackLifecycleConfig
	newSessionID    func() string
	updates         chan TrackUpdateBatch

	sessionID              string
	nextTrackID            uint64
	tracks                 map[string]*lifecycleTrack
	configuredCountRules   map[string]map[string]CountingRule
	activeCountSourceID    string
	countRules             map[string]CountingRule
	ruleCounts             map[string]*TrackRuleCount
	uniqueConfirmed        uint64
	initialized            bool
	lastFrame              trackingContinuity
	state                  string
	lastReset              TrackingResetReason
	resetCount             uint64
	lastError              string
	cameraMotionState      string
	cameraMotionMethod     string
	cameraMotionConfidence float64
	reIDEnabled            bool
}

type trackObservation struct {
	observedAt time.Time
	box        BoundingBox
	confidence float64
}

type boxVelocity struct {
	x      float64
	y      float64
	width  float64
	height float64
}

type lifecycleTrack struct {
	backendKey        string
	snapshot          TrackSnapshot
	history           []trackObservation
	velocity          boxVelocity
	velocityValid     bool
	lastEmittedAt     time.Time
	confirmed         bool
	countStates       map[string]countRuleTrackState
	latestSourcePTSNS int64
	latestFrameTiming FrameTiming
}

type countRuleTrackState struct {
	initialized   bool
	lineSide      int
	polygonInside bool
}

type trackingContinuity struct {
	sourceID    string
	streamEpoch string
	observedAt  time.Time
	sourcePTSNS int64
	imageWidth  uint32
	imageHeight uint32
	model       ModelIdentity
}

func NewTrackingStage(backend TrackerBackend, maxTimestampGap time.Duration) *TrackingStage {
	stage, err := NewTrackingStageWithLifecycle(backend, maxTimestampGap, DefaultTrackLifecycleConfig())
	if err != nil {
		panic(err)
	}
	return stage
}

func NewTrackingStageWithLifecycle(backend TrackerBackend, maxTimestampGap time.Duration, lifecycle TrackLifecycleConfig) (*TrackingStage, error) {
	if maxTimestampGap <= 0 {
		maxTimestampGap = defaultTrackerTimestampGap
	}
	if err := lifecycle.Validate(); err != nil {
		return nil, err
	}
	state := "READY"
	cameraMotionState := "DISABLED"
	reIDEnabled := false
	if backend == nil || backend.Algorithm() == TrackerAlgorithmDisabled {
		state = "DISABLED"
	} else if features, ok := backend.(trackerFeatureReporter); ok {
		reIDEnabled = features.ReIDEnabled()
		if features.CameraMotionCompensationEnabled() {
			cameraMotionState = "WAITING"
		}
	}
	return &TrackingStage{
		backend:              backend,
		maxTimestampGap:      maxTimestampGap,
		lifecycle:            lifecycle,
		newSessionID:         newTrackingSessionID,
		tracks:               make(map[string]*lifecycleTrack),
		countRules:           make(map[string]CountingRule),
		configuredCountRules: make(map[string]map[string]CountingRule),
		ruleCounts:           make(map[string]*TrackRuleCount),
		state:                state,
		cameraMotionState:    cameraMotionState,
		reIDEnabled:          reIDEnabled,
	}, nil
}

func NewDisabledTrackingStage() *TrackingStage {
	return NewTrackingStage(nil, defaultTrackerTimestampGap)
}

// Process always removes provider-owned track IDs. If no Atlas backend is
// enabled, or if the backend fails, normalized detections continue downstream
// without an authoritative track ID.
func (stage *TrackingStage) Process(frame Frame) Frame {
	frame.Detections = cloneDetections(frame.Detections)
	for index := range frame.Detections {
		if frame.Detections[index].UpstreamTrackID == "" {
			frame.Detections[index].UpstreamTrackID = frame.Detections[index].TrackID
		}
		frame.Detections[index].TrackID = ""
	}

	stage.mu.Lock()
	if stage.disabledLocked() {
		stage.mu.Unlock()
		return frame
	}
	stage.observeCameraMotionLocked(frame.CameraMotion)
	if reason := stage.discontinuityLocked(frame); reason != "" {
		stage.resetLocked(reason, lifecycleClosureTime(frame.ObservedAt, stage.lastFrame.observedAt))
	}
	sessionStarted := stage.ensureSessionLocked(frame.SourceID)

	trackerDetections := cloneDetections(frame.Detections)
	trackerMotion := cloneCameraMotion(frame.CameraMotion)
	associations, err := stage.backend.Track(TrackerFrame{
		SourceID: frame.SourceID, StreamEpoch: frame.StreamEpoch, FrameID: frame.FrameID,
		ObservedAt: frame.ObservedAt, SourcePTSNS: frame.SourcePTSNS,
		ImageWidth: frame.ImageWidth, ImageHeight: frame.ImageHeight,
		Model: frame.Model, CameraMotion: trackerMotion,
		Detections: trackerDetections,
	})
	if err != nil {
		stage.failLocked(fmt.Errorf("track frame: %w", err), frame.ObservedAt)
		stage.mu.Unlock()
		return frame
	}
	if err := validateTrackAssociations(associations, len(frame.Detections)); err != nil {
		stage.failLocked(err, frame.ObservedAt)
		stage.mu.Unlock()
		return frame
	}
	updates, countEvents := stage.advanceLifecycleLocked(frame, associations)
	stage.lastFrame = continuityFromFrame(frame)
	stage.initialized = true
	stage.state = "ACTIVE"
	stage.lastError = ""
	if sessionStarted || len(updates) > 0 || len(countEvents) > 0 {
		stage.publishBatchLocked(TrackUpdateBatch{
			SourceID: frame.SourceID, StreamEpoch: frame.StreamEpoch,
			TrackSessionID: stage.sessionID, TrackerType: stage.backend.Algorithm(),
			ObservedAt: frame.ObservedAt, SessionStarted: sessionStarted,
			CurrentVisible: stage.currentVisibleLocked(), UniqueConfirmed: stage.uniqueConfirmed,
			Tracks: updates, RuleCounts: stage.ruleCountSnapshotsLocked(), CountEvents: countEvents,
		})
	}
	stage.mu.Unlock()
	return frame
}

// SubscribeTrackUpdates attaches the single lifecycle consumer owned by the
// Agent-to-Native transport. Replays and unit tests that only need association
// output do not allocate or retain lifecycle messages.
func (stage *TrackingStage) SubscribeTrackUpdates() <-chan TrackUpdateBatch {
	stage.mu.Lock()
	defer stage.mu.Unlock()
	if stage.updates == nil {
		stage.updates = make(chan TrackUpdateBatch, trackUpdateBufferSize)
	}
	return stage.updates
}

// TrackForFollow returns a copy of the latest state for one exact Atlas track.
// False is intentional on every session reset, closure eviction, or identity
// mismatch; callers must stop rather than search for a replacement ID.
func (stage *TrackingStage) TrackForFollow(trackSessionID, trackID string) (TrackFollowObservation, bool) {
	stage.mu.Lock()
	defer stage.mu.Unlock()
	if stage.disabledLocked() || stage.sessionID == "" || stage.sessionID != trackSessionID {
		return TrackFollowObservation{}, false
	}
	for _, track := range stage.tracks {
		if track.snapshot.TrackID != trackID {
			continue
		}
		return TrackFollowObservation{
			SourceID:                  stage.lastFrame.sourceID,
			StreamEpoch:               stage.lastFrame.streamEpoch,
			TrackSessionID:            stage.sessionID,
			TrackID:                   track.snapshot.TrackID,
			LifecycleState:            track.snapshot.LifecycleState,
			LastObservedAt:            track.snapshot.LastObservedAt,
			LatestConfirmedBox:        track.snapshot.LatestConfirmedBox,
			LatestDetectionConfidence: track.snapshot.LatestDetectionConfidence,
			SourcePTSNS:               track.latestSourcePTSNS,
			FrameTiming:               track.latestFrameTiming,
		}, true
	}
	return TrackFollowObservation{}, false
}

// ReplaceCountingRules atomically installs Native-owned rules. Geometry
// changes reset only the affected per-track side/inside state and session
// counter; they never replay old observations as new crossings.
func (stage *TrackingStage) ReplaceCountingRules(sourceID string, rules []CountingRule) error {
	validated, err := validateCountingRules(sourceID, rules)
	if err != nil {
		return err
	}
	stage.mu.Lock()
	defer stage.mu.Unlock()
	sourceID = strings.TrimSpace(sourceID)
	stage.configuredCountRules[sourceID] = validated
	if stage.sessionID == "" || stage.activeCountSourceID != sourceID {
		return nil
	}
	previous := stage.countRules
	stage.countRules = validated
	for ruleID, rule := range validated {
		old, unchanged := previous[ruleID]
		if unchanged && old.Revision == rule.Revision {
			continue
		}
		stage.ruleCounts[ruleID] = &TrackRuleCount{
			RuleID: ruleID, RuleRevision: rule.Revision, RuleType: rule.RuleType,
		}
		for _, track := range stage.tracks {
			delete(track.countStates, ruleID)
			if track.confirmed && ruleMatchesClass(rule, track.snapshot.ClassID) {
				track.countStates[ruleID] = initialCountRuleState(rule, boxAnchor(track.snapshot.LatestConfirmedBox))
			}
		}
	}
	for ruleID := range stage.ruleCounts {
		if _, exists := validated[ruleID]; !exists {
			delete(stage.ruleCounts, ruleID)
			for _, track := range stage.tracks {
				delete(track.countStates, ruleID)
			}
		}
	}
	return nil
}

func (stage *TrackingStage) advanceLifecycleLocked(frame Frame, associations []TrackAssociation) ([]TrackSnapshot, []TrackCountEvent) {
	for _, track := range stage.tracks {
		track.snapshot.AgeFrames++
	}
	associated := make(map[string]struct{}, len(associations))
	updates := make([]TrackSnapshot, 0, len(associations))
	countEvents := make([]TrackCountEvent, 0)
	for _, association := range associations {
		detection := frame.Detections[association.DetectionIndex]
		track, exists := stage.tracks[association.TrackKey]
		if !exists {
			stage.nextTrackID++
			track = &lifecycleTrack{
				backendKey: association.TrackKey,
				snapshot: TrackSnapshot{
					TrackID:        "atlas:" + stage.sessionID + ":" + strconv.FormatUint(stage.nextTrackID, 10),
					TrackSessionID: stage.sessionID, TrackerType: stage.backend.Algorithm(),
					LifecycleState: TrackLifecycleTentative, AgeFrames: 1,
					FirstObservedAt: frame.ObservedAt,
					ClassID:         detection.ClassID, ClassLabel: detection.ClassLabel,
				},
				history:     make([]trackObservation, 0, stage.lifecycle.MaxHistoryObservations),
				countStates: make(map[string]countRuleTrackState),
			}
			if stage.lifecycle.ConfirmationObservations == 1 {
				track.snapshot.LifecycleState = TrackLifecycleActive
			}
			stage.tracks[association.TrackKey] = track
		}
		associated[association.TrackKey] = struct{}{}
		frame.Detections[association.DetectionIndex].TrackID = track.snapshot.TrackID
		reason, events := stage.observeTrackLocked(track, detection, frame)
		countEvents = append(countEvents, events...)
		if reason != "" {
			updates = append(updates, stage.snapshotForEmissionLocked(track, reason, frame.ObservedAt))
		}
	}

	for key, track := range stage.tracks {
		if _, ok := associated[key]; ok {
			continue
		}
		if update, closeTrack := stage.missTrackLocked(track, frame.ObservedAt); update != nil {
			updates = append(updates, *update)
			if closeTrack {
				delete(stage.tracks, key)
			}
		}
	}
	return updates, countEvents
}

func (stage *TrackingStage) observeTrackLocked(track *lifecycleTrack, detection Detection, frame Frame) (TrackUpdateReason, []TrackCountEvent) {
	observedAt := frame.ObservedAt
	if len(track.history) > 0 {
		previous := track.history[len(track.history)-1]
		seconds := observedAt.Sub(previous.observedAt).Seconds()
		if seconds > 0 {
			track.velocity = boxVelocity{
				x:      boundedVelocity((detection.BoundingBox.X - previous.box.X) / seconds),
				y:      boundedVelocity((detection.BoundingBox.Y - previous.box.Y) / seconds),
				width:  boundedVelocity((detection.BoundingBox.Width - previous.box.Width) / seconds),
				height: boundedVelocity((detection.BoundingBox.Height - previous.box.Height) / seconds),
			}
			track.velocityValid = true
		}
	}
	track.history = append(track.history, trackObservation{observedAt: observedAt, box: detection.BoundingBox, confidence: detection.Confidence})
	if overflow := len(track.history) - stage.lifecycle.MaxHistoryObservations; overflow > 0 {
		copy(track.history, track.history[overflow:])
		track.history = track.history[:len(track.history)-overflow]
	}
	track.snapshot.ObservationCount++
	track.snapshot.LastObservedAt = observedAt
	track.snapshot.LatestConfirmedBox = detection.BoundingBox
	track.snapshot.LatestDetectionConfidence = detection.Confidence
	track.snapshot.PredictedBox = nil
	track.snapshot.PredictionConfidence = 0
	track.latestSourcePTSNS = frame.SourcePTSNS
	if frame.Timing != nil {
		track.latestFrameTiming = *frame.Timing
	}

	reason := TrackUpdateReason("")
	if track.snapshot.Revision == 0 {
		reason = TrackUpdateCreated
	}
	previousState := track.snapshot.LifecycleState
	if reason == "" {
		switch previousState {
		case TrackLifecycleTentative:
			if track.snapshot.ObservationCount >= stage.lifecycle.ConfirmationObservations {
				track.snapshot.LifecycleState = TrackLifecycleActive
				reason = TrackUpdateStateChanged
			}
		case TrackLifecycleTemporarilyOccluded, TrackLifecycleLost:
			if track.snapshot.ObservationCount >= stage.lifecycle.ConfirmationObservations {
				track.snapshot.LifecycleState = TrackLifecycleActive
				reason = TrackUpdateReacquired
			} else {
				track.snapshot.LifecycleState = TrackLifecycleTentative
				reason = TrackUpdateStateChanged
			}
		}
	}
	if track.snapshot.LifecycleState == TrackLifecycleActive && !track.confirmed {
		track.confirmed = true
		stage.uniqueConfirmed++
	}
	events := stage.evaluateCountingRulesLocked(track, frame.SourceID, observedAt)
	if reason == "" && observedAt.Sub(track.lastEmittedAt) >= stage.lifecycle.SnapshotInterval {
		reason = TrackUpdatePeriodic
	}
	return reason, events
}

func (stage *TrackingStage) missTrackLocked(track *lifecycleTrack, observedAt time.Time) (*TrackSnapshot, bool) {
	elapsed := observedAt.Sub(track.snapshot.LastObservedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	switch track.snapshot.LifecycleState {
	case TrackLifecycleTentative:
		// Confirmation requires consecutive observations. An association that
		// disappears first is never exposed as an occluded/lost confirmed track.
		track.snapshot.LifecycleState = TrackLifecycleClosed
		track.snapshot.ClosedAt = observedAt
		track.snapshot.ClosureReason = "UNCONFIRMED"
		track.snapshot.PredictedBox = nil
		track.snapshot.PredictionConfidence = 0
		update := stage.snapshotForEmissionLocked(track, TrackUpdateClosed, observedAt)
		return &update, true
	case TrackLifecycleActive:
		track.snapshot.LifecycleState = TrackLifecycleTemporarilyOccluded
		stage.updatePredictionLocked(track, elapsed)
		update := stage.snapshotForEmissionLocked(track, TrackUpdateStateChanged, observedAt)
		return &update, false
	case TrackLifecycleTemporarilyOccluded:
		stage.updatePredictionLocked(track, elapsed)
		if elapsed >= stage.lifecycle.LostAfter {
			track.snapshot.LifecycleState = TrackLifecycleLost
			track.snapshot.PredictedBox = nil
			track.snapshot.PredictionConfidence = 0
			update := stage.snapshotForEmissionLocked(track, TrackUpdateStateChanged, observedAt)
			return &update, false
		}
	case TrackLifecycleLost:
		if elapsed >= stage.lifecycle.CloseAfter {
			track.snapshot.LifecycleState = TrackLifecycleClosed
			track.snapshot.ClosedAt = observedAt
			track.snapshot.ClosureReason = "RETENTION_EXPIRED"
			track.snapshot.PredictedBox = nil
			track.snapshot.PredictionConfidence = 0
			update := stage.snapshotForEmissionLocked(track, TrackUpdateClosed, observedAt)
			return &update, true
		}
	}
	if observedAt.Sub(track.lastEmittedAt) >= stage.lifecycle.SnapshotInterval {
		update := stage.snapshotForEmissionLocked(track, TrackUpdatePeriodic, observedAt)
		return &update, false
	}
	return nil, false
}

func (stage *TrackingStage) updatePredictionLocked(track *lifecycleTrack, elapsed time.Duration) {
	if elapsed > stage.lifecycle.PredictionHorizon {
		track.snapshot.PredictedBox = nil
		track.snapshot.PredictionConfidence = 0
		return
	}
	seconds := elapsed.Seconds()
	box := track.snapshot.LatestConfirmedBox
	if track.velocityValid {
		box.X += track.velocity.x * seconds
		box.Y += track.velocity.y * seconds
		box.Width += track.velocity.width * seconds
		box.Height += track.velocity.height * seconds
	}
	box = boundedPredictedBox(box, track.snapshot.LatestConfirmedBox)
	confidence := track.snapshot.LatestDetectionConfidence * (1 - float64(elapsed)/float64(stage.lifecycle.PredictionHorizon))
	if confidence < 0 {
		confidence = 0
	}
	track.snapshot.PredictedBox = &box
	track.snapshot.PredictionConfidence = confidence
}

func (stage *TrackingStage) snapshotForEmissionLocked(track *lifecycleTrack, reason TrackUpdateReason, observedAt time.Time) TrackSnapshot {
	track.snapshot.Revision++
	track.snapshot.UpdateReason = reason
	track.lastEmittedAt = observedAt
	snapshot := track.snapshot
	if track.snapshot.PredictedBox != nil {
		predicted := *track.snapshot.PredictedBox
		snapshot.PredictedBox = &predicted
	}
	return snapshot
}

func (stage *TrackingStage) publishBatchLocked(batch TrackUpdateBatch) {
	if stage.updates != nil {
		stage.updates <- batch
	}
}

func (stage *TrackingStage) evaluateCountingRulesLocked(track *lifecycleTrack, sourceID string, observedAt time.Time) []TrackCountEvent {
	if !track.confirmed || stage.activeCountSourceID != sourceID {
		return nil
	}
	current := boxAnchor(track.snapshot.LatestConfirmedBox)
	var previous NormalizedPoint
	continuous := false
	if len(track.history) >= 2 {
		prior := track.history[len(track.history)-2]
		previous = boxAnchor(prior.box)
		gap := observedAt.Sub(prior.observedAt)
		continuous = gap >= 0 && gap <= stage.lifecycle.PredictionHorizon
	}
	events := make([]TrackCountEvent, 0)
	for ruleID, rule := range stage.countRules {
		if !ruleMatchesClass(rule, track.snapshot.ClassID) {
			continue
		}
		state, exists := track.countStates[ruleID]
		if !exists {
			anchor := current
			if continuous {
				anchor = previous
			}
			state = initialCountRuleState(rule, anchor)
		}
		if !continuous {
			track.countStates[ruleID] = initialCountRuleState(rule, current)
			continue
		}
		var eventType TrackCountEventType
		switch rule.RuleType {
		case CountingRuleLine:
			currentSide := lineSide(rule.Points[0], rule.Points[1], current)
			if currentSide != 0 && state.lineSide != 0 && currentSide != state.lineSide &&
				segmentsIntersect(previous, current, rule.Points[0], rule.Points[1]) {
				if state.lineSide < currentSide {
					eventType = TrackCountLineForward
				} else {
					eventType = TrackCountLineReverse
				}
			}
			if currentSide != 0 {
				state.lineSide = currentSide
			}
		case CountingRulePolygon:
			inside := pointInPolygon(current, rule.Points)
			if state.initialized && inside != state.polygonInside {
				if inside {
					eventType = TrackCountPolygonEntry
				} else {
					eventType = TrackCountPolygonExit
				}
			}
			state.polygonInside = inside
		}
		state.initialized = true
		track.countStates[ruleID] = state
		if eventType == "" {
			continue
		}
		count := stage.ruleCounts[ruleID]
		if count == nil || count.RuleRevision != rule.Revision {
			count = &TrackRuleCount{RuleID: ruleID, RuleRevision: rule.Revision, RuleType: rule.RuleType}
			stage.ruleCounts[ruleID] = count
		}
		switch eventType {
		case TrackCountLineForward:
			count.LineForward++
		case TrackCountLineReverse:
			count.LineReverse++
		case TrackCountPolygonEntry:
			count.PolygonEntries++
		case TrackCountPolygonExit:
			count.PolygonExits++
		}
		events = append(events, TrackCountEvent{
			EventID: fmt.Sprintf("%s:%s:%d:%s:%d", stage.sessionID, ruleID, rule.Revision, track.snapshot.TrackID, track.snapshot.ObservationCount),
			RuleID:  ruleID, RuleRevision: rule.Revision,
			TrackSessionID: stage.sessionID, TrackID: track.snapshot.TrackID,
			EventType: eventType, ObservedAt: observedAt, Anchor: current,
		})
	}
	return events
}

func (stage *TrackingStage) currentVisibleLocked() uint64 {
	var visible uint64
	for _, track := range stage.tracks {
		if track.confirmed && track.snapshot.LifecycleState == TrackLifecycleActive {
			visible++
		}
	}
	return visible
}

func (stage *TrackingStage) ruleCountSnapshotsLocked() []TrackRuleCount {
	counts := make([]TrackRuleCount, 0, len(stage.ruleCounts))
	for _, count := range stage.ruleCounts {
		counts = append(counts, *count)
	}
	sort.Slice(counts, func(left, right int) bool { return counts[left].RuleID < counts[right].RuleID })
	return counts
}

func cloneDetections(detections []Detection) []Detection {
	cloned := append([]Detection(nil), detections...)
	for index := range cloned {
		cloned[index].AttributesRaw = append([]byte(nil), cloned[index].AttributesRaw...)
	}
	return cloned
}

func cloneCameraMotion(motion *CameraMotionEstimate) *CameraMotionEstimate {
	if motion == nil {
		return nil
	}
	cloned := *motion
	cloned.Homography = append([]float64(nil), motion.Homography...)
	return &cloned
}

func (stage *TrackingStage) Reset(reason TrackingResetReason) {
	stage.mu.Lock()
	defer stage.mu.Unlock()
	if stage.disabledLocked() {
		return
	}
	stage.resetLocked(reason, time.Now().UTC())
}

func (stage *TrackingStage) EnrichHealth(health Health) Health {
	stage.mu.Lock()
	defer stage.mu.Unlock()
	algorithm := TrackerAlgorithmDisabled
	if stage.backend != nil {
		algorithm = stage.backend.Algorithm()
	}
	tracking := &TrackingHealth{
		Algorithm:              algorithm,
		State:                  stage.state,
		SessionID:              stage.sessionID,
		LastResetReason:        stage.lastReset,
		ResetCount:             stage.resetCount,
		LastError:              stage.lastError,
		CameraMotionState:      stage.cameraMotionState,
		CameraMotionMethod:     stage.cameraMotionMethod,
		CameraMotionConfidence: stage.cameraMotionConfidence,
		ReIDEnabled:            stage.reIDEnabled,
	}
	health.Tracking = tracking
	return health
}

func (stage *TrackingStage) disabledLocked() bool {
	return stage.backend == nil || stage.backend.Algorithm() == TrackerAlgorithmDisabled
}

func (stage *TrackingStage) ensureSessionLocked(sourceID string) bool {
	if stage.sessionID == "" {
		stage.sessionID = stage.newSessionID()
		stage.nextTrackID = 0
		stage.tracks = make(map[string]*lifecycleTrack)
		stage.uniqueConfirmed = 0
		stage.activeCountSourceID = sourceID
		stage.countRules = cloneCountingRuleMap(stage.configuredCountRules[sourceID])
		stage.resetRuleCountsLocked()
		return true
	}
	return false
}

func (stage *TrackingStage) resetLocked(reason TrackingResetReason, closedAt time.Time) {
	if stage.sessionID != "" {
		updates := make([]TrackSnapshot, 0, len(stage.tracks))
		for _, track := range stage.tracks {
			track.snapshot.LifecycleState = TrackLifecycleClosed
			track.snapshot.ClosedAt = lifecycleClosureTime(closedAt, track.snapshot.LastObservedAt)
			track.snapshot.ClosureReason = string(reason)
			track.snapshot.PredictedBox = nil
			track.snapshot.PredictionConfidence = 0
			updates = append(updates, stage.snapshotForEmissionLocked(track, TrackUpdateClosed, track.snapshot.ClosedAt))
		}
		batchObservedAt := lifecycleClosureTime(closedAt, stage.lastFrame.observedAt)
		stage.publishBatchLocked(TrackUpdateBatch{
			SourceID: stage.lastFrame.sourceID, StreamEpoch: stage.lastFrame.streamEpoch,
			TrackSessionID: stage.sessionID, TrackerType: stage.backend.Algorithm(),
			ObservedAt: batchObservedAt, SessionEnded: true, SessionEndReason: string(reason),
			CurrentVisible: 0, UniqueConfirmed: stage.uniqueConfirmed,
			Tracks: updates, RuleCounts: stage.ruleCountSnapshotsLocked(),
		})
	}
	stage.sessionID = ""
	stage.nextTrackID = 0
	stage.tracks = make(map[string]*lifecycleTrack)
	stage.uniqueConfirmed = 0
	stage.activeCountSourceID = ""
	stage.countRules = make(map[string]CountingRule)
	stage.ruleCounts = make(map[string]*TrackRuleCount)
	stage.initialized = false
	stage.lastFrame = trackingContinuity{}
	stage.lastReset = reason
	stage.resetCount++
	stage.state = "READY"
	stage.lastError = ""
	stage.cameraMotionMethod = ""
	stage.cameraMotionConfidence = 0
	if features, ok := stage.backend.(trackerFeatureReporter); ok && features.CameraMotionCompensationEnabled() {
		stage.cameraMotionState = "WAITING"
	} else {
		stage.cameraMotionState = "DISABLED"
	}
	if err := stage.backend.Reset(); err != nil {
		stage.state = "DEGRADED"
		stage.lastError = "reset tracker: " + err.Error()
	}
}

func (stage *TrackingStage) observeCameraMotionLocked(motion *CameraMotionEstimate) {
	features, ok := stage.backend.(trackerFeatureReporter)
	if !ok || !features.CameraMotionCompensationEnabled() {
		stage.cameraMotionState = "DISABLED"
		stage.cameraMotionMethod = ""
		stage.cameraMotionConfidence = 0
		return
	}
	if motion == nil {
		stage.cameraMotionState = "DEGRADED"
		stage.cameraMotionMethod = ""
		stage.cameraMotionConfidence = 0
		return
	}
	stage.cameraMotionMethod = motion.Method
	stage.cameraMotionConfidence = motion.Confidence
	if motion.Confidence < features.CameraMotionMinimumConfidence() {
		stage.cameraMotionState = "DEGRADED"
		return
	}
	stage.cameraMotionState = "ACTIVE"
}

func (stage *TrackingStage) failLocked(err error, observedAt time.Time) {
	stage.resetLocked(TrackingResetTrackerFailure, observedAt)
	stage.state = "DEGRADED"
	if stage.lastError == "" {
		stage.lastError = err.Error()
	} else {
		stage.lastError = errors.Join(err, errors.New(stage.lastError)).Error()
	}
}

func (stage *TrackingStage) discontinuityLocked(frame Frame) TrackingResetReason {
	if !stage.initialized {
		return ""
	}
	previous := stage.lastFrame
	switch {
	case frame.SourceID != previous.sourceID:
		return TrackingResetSourceChanged
	case frame.StreamEpoch != previous.streamEpoch:
		return TrackingResetStreamChanged
	case frame.Model != previous.model:
		return TrackingResetModelChanged
	case frame.ImageWidth != previous.imageWidth || frame.ImageHeight != previous.imageHeight:
		return TrackingResetDimensionsChanged
	}
	if frame.SourcePTSNS > 0 && previous.sourcePTSNS > 0 {
		delta := frame.SourcePTSNS - previous.sourcePTSNS
		if delta < 0 {
			return TrackingResetTimestampRegressed
		}
		if delta > stage.maxTimestampGap.Nanoseconds() {
			return TrackingResetTimestampGap
		}
		return ""
	}
	delta := frame.ObservedAt.Sub(previous.observedAt)
	if delta < 0 {
		return TrackingResetTimestampRegressed
	}
	if delta > stage.maxTimestampGap {
		return TrackingResetTimestampGap
	}
	return ""
}

func continuityFromFrame(frame Frame) trackingContinuity {
	return trackingContinuity{
		sourceID: frame.SourceID, streamEpoch: frame.StreamEpoch,
		observedAt: frame.ObservedAt, sourcePTSNS: frame.SourcePTSNS,
		imageWidth: frame.ImageWidth, imageHeight: frame.ImageHeight, model: frame.Model,
	}
}

func validateTrackAssociations(associations []TrackAssociation, detectionCount int) error {
	detectionIndexes := make(map[int]struct{}, len(associations))
	trackKeys := make(map[string]struct{}, len(associations))
	for _, association := range associations {
		key := strings.TrimSpace(association.TrackKey)
		if association.DetectionIndex < 0 || association.DetectionIndex >= detectionCount {
			return fmt.Errorf("tracker returned detection index %d outside frame", association.DetectionIndex)
		}
		if key == "" {
			return errors.New("tracker returned an empty track key")
		}
		if _, duplicate := detectionIndexes[association.DetectionIndex]; duplicate {
			return fmt.Errorf("tracker assigned detection index %d more than once", association.DetectionIndex)
		}
		if _, duplicate := trackKeys[key]; duplicate {
			return fmt.Errorf("tracker assigned track key %q more than once in one frame", key)
		}
		detectionIndexes[association.DetectionIndex] = struct{}{}
		trackKeys[key] = struct{}{}
	}
	return nil
}

func boundedVelocity(value float64) float64 {
	const maximumNormalizedUnitsPerSecond = 2.0
	if value < -maximumNormalizedUnitsPerSecond {
		return -maximumNormalizedUnitsPerSecond
	}
	if value > maximumNormalizedUnitsPerSecond {
		return maximumNormalizedUnitsPerSecond
	}
	return value
}

func boundedPredictedBox(box, fallback BoundingBox) BoundingBox {
	if box.Width <= 0 || box.Width > 1 {
		box.Width = fallback.Width
	}
	if box.Height <= 0 || box.Height > 1 {
		box.Height = fallback.Height
	}
	box.Width = clampUnit(box.Width)
	box.Height = clampUnit(box.Height)
	box.X = clamp(box.X, 0, 1-box.Width)
	box.Y = clamp(box.Y, 0, 1-box.Height)
	return box
}

func clampUnit(value float64) float64 {
	return clamp(value, 0, 1)
}

func clamp(value, minimum, maximum float64) float64 {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func lifecycleClosureTime(candidate, lastObserved time.Time) time.Time {
	if candidate.IsZero() || (!lastObserved.IsZero() && candidate.Before(lastObserved)) {
		return lastObserved
	}
	return candidate
}

func (stage *TrackingStage) resetRuleCountsLocked() {
	stage.ruleCounts = make(map[string]*TrackRuleCount, len(stage.countRules))
	for ruleID, rule := range stage.countRules {
		stage.ruleCounts[ruleID] = &TrackRuleCount{
			RuleID: ruleID, RuleRevision: rule.Revision, RuleType: rule.RuleType,
		}
	}
}

func validateCountingRules(sourceID string, rules []CountingRule) (map[string]CountingRule, error) {
	if strings.TrimSpace(sourceID) == "" {
		return nil, errors.New("counting rule source is required")
	}
	if len(rules) > 64 {
		return nil, errors.New("at most 64 counting rules may be active")
	}
	validated := make(map[string]CountingRule, len(rules))
	for _, rule := range rules {
		rule.RuleID = strings.TrimSpace(rule.RuleID)
		rule.Label = strings.TrimSpace(rule.Label)
		if rule.RuleID == "" || rule.Label == "" || rule.Revision == 0 {
			return nil, errors.New("counting rule id, label, and revision are required")
		}
		if _, duplicate := validated[rule.RuleID]; duplicate {
			return nil, fmt.Errorf("counting rule %q is duplicated", rule.RuleID)
		}
		switch rule.RuleType {
		case CountingRuleLine:
			if len(rule.Points) != 2 || pointsEqual(rule.Points[0], rule.Points[1]) {
				return nil, fmt.Errorf("line counting rule %q requires two distinct points", rule.RuleID)
			}
		case CountingRulePolygon:
			if len(rule.Points) < 3 || len(rule.Points) > 32 || math.Abs(polygonArea(rule.Points)) < 1e-6 {
				return nil, fmt.Errorf("polygon counting rule %q requires 3-32 non-collinear points", rule.RuleID)
			}
		default:
			return nil, fmt.Errorf("counting rule %q has unsupported type %q", rule.RuleID, rule.RuleType)
		}
		for _, point := range rule.Points {
			if math.IsNaN(point.X) || math.IsInf(point.X, 0) || point.X < 0 || point.X > 1 ||
				math.IsNaN(point.Y) || math.IsInf(point.Y, 0) || point.Y < 0 || point.Y > 1 {
				return nil, fmt.Errorf("counting rule %q leaves the normalized frame", rule.RuleID)
			}
		}
		rule.Points = append([]NormalizedPoint(nil), rule.Points...)
		rule.ClassIDs = append([]int32(nil), rule.ClassIDs...)
		slices.Sort(rule.ClassIDs)
		rule.ClassIDs = slices.Compact(rule.ClassIDs)
		validated[rule.RuleID] = rule
	}
	return validated, nil
}

func cloneCountingRuleMap(rules map[string]CountingRule) map[string]CountingRule {
	cloned := make(map[string]CountingRule, len(rules))
	for ruleID, rule := range rules {
		rule.Points = append([]NormalizedPoint(nil), rule.Points...)
		rule.ClassIDs = append([]int32(nil), rule.ClassIDs...)
		cloned[ruleID] = rule
	}
	return cloned
}

func ruleMatchesClass(rule CountingRule, classID int32) bool {
	return len(rule.ClassIDs) == 0 || slices.Contains(rule.ClassIDs, classID)
}

func boxAnchor(box BoundingBox) NormalizedPoint {
	return NormalizedPoint{X: clampUnit(box.X + box.Width/2), Y: clampUnit(box.Y + box.Height/2)}
}

func initialCountRuleState(rule CountingRule, anchor NormalizedPoint) countRuleTrackState {
	state := countRuleTrackState{initialized: true}
	if rule.RuleType == CountingRuleLine {
		state.lineSide = lineSide(rule.Points[0], rule.Points[1], anchor)
	} else {
		state.polygonInside = pointInPolygon(anchor, rule.Points)
	}
	return state
}

func lineSide(start, end, point NormalizedPoint) int {
	const epsilon = 0.002
	cross := (end.X-start.X)*(point.Y-start.Y) - (end.Y-start.Y)*(point.X-start.X)
	if math.Abs(cross) <= epsilon {
		return 0
	}
	if cross > 0 {
		return 1
	}
	return -1
}

func segmentsIntersect(a, b, c, d NormalizedPoint) bool {
	orientation := func(p, q, r NormalizedPoint) float64 {
		return (q.X-p.X)*(r.Y-p.Y) - (q.Y-p.Y)*(r.X-p.X)
	}
	const epsilon = 1e-9
	o1, o2 := orientation(a, b, c), orientation(a, b, d)
	o3, o4 := orientation(c, d, a), orientation(c, d, b)
	return ((o1 > epsilon && o2 < -epsilon) || (o1 < -epsilon && o2 > epsilon) || math.Abs(o1) <= epsilon || math.Abs(o2) <= epsilon) &&
		((o3 > epsilon && o4 < -epsilon) || (o3 < -epsilon && o4 > epsilon) || math.Abs(o3) <= epsilon || math.Abs(o4) <= epsilon)
}

func pointInPolygon(point NormalizedPoint, polygon []NormalizedPoint) bool {
	inside := false
	for current, previous := 0, len(polygon)-1; current < len(polygon); previous, current = current, current+1 {
		left, right := polygon[current], polygon[previous]
		intersects := (left.Y > point.Y) != (right.Y > point.Y) &&
			point.X < (right.X-left.X)*(point.Y-left.Y)/(right.Y-left.Y)+left.X
		if intersects {
			inside = !inside
		}
	}
	return inside
}

func polygonArea(points []NormalizedPoint) float64 {
	area := 0.0
	for index, point := range points {
		next := points[(index+1)%len(points)]
		area += point.X*next.Y - next.X*point.Y
	}
	return area / 2
}

func pointsEqual(left, right NormalizedPoint) bool {
	return math.Abs(left.X-right.X) < 1e-9 && math.Abs(left.Y-right.Y) < 1e-9
}

var fallbackTrackingSessionID atomic.Uint64

func newTrackingSessionID() string {
	bytes := make([]byte, 8)
	if _, err := rand.Read(bytes); err == nil {
		return hex.EncodeToString(bytes)
	}
	return strconv.FormatInt(time.Now().UTC().UnixNano(), 36) + "-" + strconv.FormatUint(fallbackTrackingSessionID.Add(1), 36)
}
