package perception

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"
)

type fakeTrackerBackend struct {
	algorithm    TrackerAlgorithm
	associations []TrackAssociation
	trackErr     error
	resetErr     error
	resetCount   int
	frames       []TrackerFrame
}

func (backend *fakeTrackerBackend) Algorithm() TrackerAlgorithm { return backend.algorithm }

func (backend *fakeTrackerBackend) Track(frame TrackerFrame) ([]TrackAssociation, error) {
	backend.frames = append(backend.frames, frame)
	return append([]TrackAssociation(nil), backend.associations...), backend.trackErr
}

func (backend *fakeTrackerBackend) Reset() error {
	backend.resetCount++
	return backend.resetErr
}

func TestTrackingStageOwnsStableSessionScopedIDs(t *testing.T) {
	backend := &fakeTrackerBackend{
		algorithm:    TrackerAlgorithmByteTrackCMC,
		associations: []TrackAssociation{{DetectionIndex: 0, TrackKey: "backend-41"}},
	}
	stage := NewTrackingStage(backend, time.Second)
	stage.newSessionID = func() string { return "session-a" }

	first := stage.Process(trackerTestFrame("frame-1", time.Second, "provider-track-9"))
	second := stage.Process(trackerTestFrame("frame-2", 2*time.Second, "provider-track-77"))
	if first.Detections[0].TrackID != "atlas:session-a:1" || second.Detections[0].TrackID != first.Detections[0].TrackID {
		t.Fatalf("Atlas track IDs = %q and %q", first.Detections[0].TrackID, second.Detections[0].TrackID)
	}
	if first.Detections[0].UpstreamTrackID != "provider-track-9" || second.Detections[0].UpstreamTrackID != "provider-track-77" {
		t.Fatalf("upstream provenance was not preserved: %#v %#v", first.Detections[0], second.Detections[0])
	}
	if backend.frames[0].Detections[0].TrackID != "" || backend.frames[0].Detections[0].UpstreamTrackID != "provider-track-9" {
		t.Fatalf("backend received authoritative upstream ID: %#v", backend.frames[0].Detections[0])
	}

	backend.associations = []TrackAssociation{{DetectionIndex: 0, TrackKey: "backend-42"}}
	third := stage.Process(trackerTestFrame("frame-3", 3*time.Second, ""))
	if third.Detections[0].TrackID != "atlas:session-a:2" {
		t.Fatalf("new association ID = %q", third.Detections[0].TrackID)
	}
	health := stage.EnrichHealth(Health{}).Tracking
	if health.State != "ACTIVE" || health.Algorithm != TrackerAlgorithmByteTrackCMC || health.SessionID != "session-a" {
		t.Fatalf("tracking health = %#v", health)
	}
}

func TestTrackForFollowRequiresExactLiveSessionIdentity(t *testing.T) {
	backend := &fakeTrackerBackend{
		algorithm:    TrackerAlgorithmByteTrack,
		associations: []TrackAssociation{{DetectionIndex: 0, TrackKey: "backend-1"}},
	}
	stage := NewTrackingStage(backend, time.Second)
	stage.newSessionID = func() string { return "session-follow" }
	stage.Process(trackerTestFrame("frame-1", time.Second, ""))
	tracked := stage.Process(trackerTestFrame("frame-2", 2*time.Second, ""))
	trackID := tracked.Detections[0].TrackID
	observation, ok := stage.TrackForFollow("session-follow", trackID)
	if !ok || observation.LifecycleState != TrackLifecycleActive || observation.SourceID != "a8-main" || observation.TrackID != trackID {
		t.Fatalf("follow observation = %#v, available=%t", observation, ok)
	}
	if observation.SourcePTSNS != int64(2*time.Second) || observation.FrameTiming.PipelineIngressMonotonicNS != int64(12*time.Second) {
		t.Fatalf("follow observation did not retain its exact frame timing: %#v", observation)
	}
	if _, ok := stage.TrackForFollow("other-session", trackID); ok {
		t.Fatal("follow lookup accepted a mismatched tracker session")
	}
	stage.Reset(TrackingResetSourceChanged)
	if _, ok := stage.TrackForFollow("session-follow", trackID); ok {
		t.Fatal("follow lookup survived a tracker/source reset")
	}
}

func TestTrackingStageResetsEveryRequiredContinuityBoundary(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Frame)
		want   TrackingResetReason
	}{
		{name: "source", mutate: func(frame *Frame) { frame.SourceID = "a8-secondary" }, want: TrackingResetSourceChanged},
		{name: "stream", mutate: func(frame *Frame) { frame.StreamEpoch = "epoch-2" }, want: TrackingResetStreamChanged},
		{name: "model", mutate: func(frame *Frame) { frame.Model.Version = "2" }, want: TrackingResetModelChanged},
		{name: "dimensions", mutate: func(frame *Frame) { frame.ImageWidth = 1280 }, want: TrackingResetDimensionsChanged},
		{name: "timestamp regression", mutate: func(frame *Frame) { frame.SourcePTSNS = int64(500 * time.Millisecond) }, want: TrackingResetTimestampRegressed},
		{name: "timestamp gap", mutate: func(frame *Frame) { frame.SourcePTSNS = int64(5 * time.Second) }, want: TrackingResetTimestampGap},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := &fakeTrackerBackend{algorithm: TrackerAlgorithmByteTrack, associations: []TrackAssociation{{DetectionIndex: 0, TrackKey: "track-1"}}}
			stage := NewTrackingStage(backend, 2*time.Second)
			session := 0
			stage.newSessionID = func() string {
				session++
				return "session-" + string(rune('0'+session))
			}
			first := stage.Process(trackerTestFrame("frame-1", time.Second, ""))
			nextFrame := trackerTestFrame("frame-2", 2*time.Second, "")
			test.mutate(&nextFrame)
			second := stage.Process(nextFrame)
			if first.Detections[0].TrackID == second.Detections[0].TrackID {
				t.Fatalf("track ID survived %s discontinuity", test.name)
			}
			health := stage.EnrichHealth(Health{}).Tracking
			if health.LastResetReason != test.want || health.ResetCount != 1 || backend.resetCount != 1 {
				t.Fatalf("tracking health = %#v backend resets=%d", health, backend.resetCount)
			}
		})
	}
}

func TestTrackingStageExplicitResetStartsNewSession(t *testing.T) {
	backend := &fakeTrackerBackend{algorithm: TrackerAlgorithmByteTrack, associations: []TrackAssociation{{DetectionIndex: 0, TrackKey: "track-1"}}}
	stage := NewTrackingStage(backend, time.Second)
	sessions := []string{"session-a", "session-b"}
	stage.newSessionID = func() string {
		value := sessions[0]
		sessions = sessions[1:]
		return value
	}
	first := stage.Process(trackerTestFrame("frame-1", time.Second, ""))
	stage.Reset(TrackingResetActivated)
	second := stage.Process(trackerTestFrame("frame-2", 2*time.Second, ""))
	if first.Detections[0].TrackID != "atlas:session-a:1" || second.Detections[0].TrackID != "atlas:session-b:1" {
		t.Fatalf("track IDs across activation = %q %q", first.Detections[0].TrackID, second.Detections[0].TrackID)
	}
	health := stage.EnrichHealth(Health{}).Tracking
	if health.Algorithm != TrackerAlgorithmByteTrack || health.LastResetReason != TrackingResetActivated {
		t.Fatalf("tracking health = %#v", health)
	}
}

func TestTrackingFailureDegradesToUntrackedDetectionsAndRecovers(t *testing.T) {
	backend := &fakeTrackerBackend{algorithm: TrackerAlgorithmByteTrack, associations: []TrackAssociation{{DetectionIndex: 0, TrackKey: "track-1"}}, trackErr: errors.New("algorithm unavailable")}
	stage := NewTrackingStage(backend, time.Second)
	failed := stage.Process(trackerTestFrame("frame-1", time.Second, "provider-1"))
	if failed.Detections[0].TrackID != "" || failed.Detections[0].UpstreamTrackID != "provider-1" {
		t.Fatalf("failed frame = %#v", failed.Detections[0])
	}
	health := stage.EnrichHealth(Health{}).Tracking
	if health.State != "DEGRADED" || health.LastResetReason != TrackingResetTrackerFailure || health.LastError == "" {
		t.Fatalf("degraded health = %#v", health)
	}

	backend.trackErr = nil
	recovered := stage.Process(trackerTestFrame("frame-2", 2*time.Second, ""))
	if recovered.Detections[0].TrackID == "" {
		t.Fatal("successful frame did not recover tracking")
	}
	if health = stage.EnrichHealth(Health{}).Tracking; health.State != "ACTIVE" || health.LastError != "" {
		t.Fatalf("recovered health = %#v", health)
	}
}

func TestInvalidTrackerAssignmentsDegradeWithoutDroppingDetections(t *testing.T) {
	backend := &fakeTrackerBackend{algorithm: TrackerAlgorithmByteTrack, associations: []TrackAssociation{{DetectionIndex: 4, TrackKey: "track-1"}}}
	stage := NewTrackingStage(backend, time.Second)
	frame := stage.Process(trackerTestFrame("frame-1", time.Second, ""))
	if len(frame.Detections) != 1 || frame.Detections[0].TrackID != "" {
		t.Fatalf("degraded frame = %#v", frame)
	}
	if health := stage.EnrichHealth(Health{}).Tracking; health.State != "DEGRADED" || health.LastError == "" {
		t.Fatalf("tracking health = %#v", health)
	}
}

func TestDisabledTrackingStageRejectsUpstreamAuthority(t *testing.T) {
	stage := NewDisabledTrackingStage()
	frame := stage.Process(trackerTestFrame("frame-1", time.Second, "provider-track"))
	if frame.Detections[0].TrackID != "" || frame.Detections[0].UpstreamTrackID != "provider-track" {
		t.Fatalf("disabled tracking frame = %#v", frame.Detections[0])
	}
	health := stage.EnrichHealth(Health{}).Tracking
	if health.Algorithm != TrackerAlgorithmDisabled || health.State != "DISABLED" || health.SessionID != "" {
		t.Fatalf("disabled tracking health = %#v", health)
	}
}

func TestTrackingLifecycleTransitionsPredictionAndClosure(t *testing.T) {
	backend := &fakeTrackerBackend{
		algorithm:    TrackerAlgorithmByteTrack,
		associations: []TrackAssociation{{DetectionIndex: 0, TrackKey: "track-1"}},
	}
	lifecycle := DefaultTrackLifecycleConfig()
	lifecycle.PredictionHorizon = 500 * time.Millisecond
	lifecycle.LostAfter = time.Second
	lifecycle.CloseAfter = 2 * time.Second
	lifecycle.SnapshotInterval = 10 * time.Second
	stage, err := NewTrackingStageWithLifecycle(backend, 5*time.Second, lifecycle)
	if err != nil {
		t.Fatalf("configure lifecycle: %v", err)
	}
	stage.newSessionID = func() string { return "session-life" }
	updates := stage.SubscribeTrackUpdates()

	first := trackerTestFrame("frame-1", time.Second, "")
	stage.Process(first)
	created := receiveTrackUpdate(t, updates)
	if !created.SessionStarted || len(created.Tracks) != 1 || created.Tracks[0].LifecycleState != TrackLifecycleTentative || created.Tracks[0].UpdateReason != TrackUpdateCreated || created.Tracks[0].Revision != 1 {
		t.Fatalf("created lifecycle = %#v", created)
	}

	second := trackerTestFrame("frame-2", 1100*time.Millisecond, "")
	second.Detections[0].BoundingBox.X = 0.12
	stage.Process(second)
	active := receiveTrackUpdate(t, updates)
	if active.Tracks[0].LifecycleState != TrackLifecycleActive || active.Tracks[0].ObservationCount != 2 || active.Tracks[0].AgeFrames != 2 || active.Tracks[0].UpdateReason != TrackUpdateStateChanged {
		t.Fatalf("active lifecycle = %#v", active)
	}

	backend.associations = nil
	stage.Process(trackerTestFrame("frame-3", 1200*time.Millisecond, ""))
	occluded := receiveTrackUpdate(t, updates)
	if occluded.Tracks[0].LifecycleState != TrackLifecycleTemporarilyOccluded || occluded.Tracks[0].PredictedBox == nil || occluded.Tracks[0].PredictionConfidence <= 0 || occluded.Tracks[0].PredictionConfidence >= 0.9 {
		t.Fatalf("occluded lifecycle = %#v", occluded)
	}

	stage.Process(trackerTestFrame("frame-4", 2100*time.Millisecond, ""))
	lost := receiveTrackUpdate(t, updates)
	if lost.Tracks[0].LifecycleState != TrackLifecycleLost || lost.Tracks[0].PredictedBox != nil || lost.Tracks[0].PredictionConfidence != 0 {
		t.Fatalf("lost lifecycle = %#v", lost)
	}

	stage.Process(trackerTestFrame("frame-5", 3100*time.Millisecond, ""))
	closed := receiveTrackUpdate(t, updates)
	if closed.Tracks[0].LifecycleState != TrackLifecycleClosed || closed.Tracks[0].ClosureReason != "RETENTION_EXPIRED" || closed.Tracks[0].ClosedAt.IsZero() || closed.Tracks[0].UpdateReason != TrackUpdateClosed {
		t.Fatalf("closed lifecycle = %#v", closed)
	}
}

func TestTrackingLifecycleClosesUnconfirmedTrackWithoutOccludedIdentity(t *testing.T) {
	backend := &fakeTrackerBackend{
		algorithm:    TrackerAlgorithmByteTrack,
		associations: []TrackAssociation{{DetectionIndex: 0, TrackKey: "track-1"}},
	}
	stage := NewTrackingStage(backend, 5*time.Second)
	stage.newSessionID = func() string { return "session-unconfirmed" }
	updates := stage.SubscribeTrackUpdates()
	stage.Process(trackerTestFrame("frame-1", time.Second, ""))
	created := receiveTrackUpdate(t, updates)
	if created.Tracks[0].LifecycleState != TrackLifecycleTentative {
		t.Fatalf("created lifecycle = %#v", created)
	}

	backend.associations = nil
	stage.Process(trackerTestFrame("frame-2", 1100*time.Millisecond, ""))
	closed := receiveTrackUpdate(t, updates)
	if closed.Tracks[0].LifecycleState != TrackLifecycleClosed || closed.Tracks[0].ClosureReason != "UNCONFIRMED" {
		t.Fatalf("unconfirmed lifecycle = %#v", closed)
	}
	if closed.UniqueConfirmed != 0 || closed.CurrentVisible != 0 {
		t.Fatalf("unconfirmed track affected counts = %#v", closed)
	}
}

func TestTrackingLifecycleReacquiresSameTrackAndResetClosesSession(t *testing.T) {
	backend := &fakeTrackerBackend{algorithm: TrackerAlgorithmByteTrack, associations: []TrackAssociation{{DetectionIndex: 0, TrackKey: "track-1"}}}
	stage := NewTrackingStage(backend, 5*time.Second)
	stage.newSessionID = func() string { return "session-reacquire" }
	updates := stage.SubscribeTrackUpdates()
	first := stage.Process(trackerTestFrame("frame-1", time.Second, ""))
	_ = receiveTrackUpdate(t, updates)
	second := stage.Process(trackerTestFrame("frame-2", 1100*time.Millisecond, ""))
	_ = receiveTrackUpdate(t, updates)
	backend.associations = nil
	stage.Process(trackerTestFrame("frame-3", 1200*time.Millisecond, ""))
	_ = receiveTrackUpdate(t, updates)
	backend.associations = []TrackAssociation{{DetectionIndex: 0, TrackKey: "track-1"}}
	reacquiredFrame := stage.Process(trackerTestFrame("frame-4", 1300*time.Millisecond, ""))
	reacquired := receiveTrackUpdate(t, updates)
	if reacquiredFrame.Detections[0].TrackID != first.Detections[0].TrackID || first.Detections[0].TrackID != second.Detections[0].TrackID || reacquired.Tracks[0].UpdateReason != TrackUpdateReacquired || reacquired.Tracks[0].LifecycleState != TrackLifecycleActive {
		t.Fatalf("reacquired lifecycle = frame %#v update %#v", reacquiredFrame, reacquired)
	}

	stage.Reset(TrackingResetDeactivated)
	ended := receiveTrackUpdate(t, updates)
	if !ended.SessionEnded || ended.SessionEndReason != string(TrackingResetDeactivated) || len(ended.Tracks) != 1 || ended.Tracks[0].LifecycleState != TrackLifecycleClosed || ended.Tracks[0].ClosureReason != string(TrackingResetDeactivated) {
		t.Fatalf("reset closure = %#v", ended)
	}
}

func TestTrackingLifecycleHistoryIsBounded(t *testing.T) {
	backend := &fakeTrackerBackend{algorithm: TrackerAlgorithmByteTrack, associations: []TrackAssociation{{DetectionIndex: 0, TrackKey: "track-1"}}}
	lifecycle := DefaultTrackLifecycleConfig()
	lifecycle.MaxHistoryObservations = 3
	stage, err := NewTrackingStageWithLifecycle(backend, 5*time.Second, lifecycle)
	if err != nil {
		t.Fatalf("configure lifecycle: %v", err)
	}
	stage.newSessionID = func() string { return "session-bounded" }
	for index := 0; index < 10; index++ {
		stage.Process(trackerTestFrame("frame", time.Second+time.Duration(index)*100*time.Millisecond, ""))
	}
	if got := len(stage.tracks["track-1"].history); got != 3 {
		t.Fatalf("history length = %d, want 3", got)
	}
}

func TestTrackingCountsAreConfirmedSessionScopedAndGeometryAware(t *testing.T) {
	backend := &fakeTrackerBackend{algorithm: TrackerAlgorithmByteTrack, associations: []TrackAssociation{{DetectionIndex: 0, TrackKey: "track-1"}}}
	stage := NewTrackingStage(backend, 5*time.Second)
	sessionIndex := 0
	stage.newSessionID = func() string {
		sessionIndex++
		return fmt.Sprintf("session-counts-%d", sessionIndex)
	}
	if err := stage.ReplaceCountingRules("a8-main", []CountingRule{
		{RuleID: "gate", Label: "Gate", RuleType: CountingRuleLine, Revision: 1, Points: []NormalizedPoint{{X: .5, Y: 0}, {X: .5, Y: 1}}},
		{RuleID: "yard", Label: "Yard", RuleType: CountingRulePolygon, Revision: 1, Points: []NormalizedPoint{{X: .5, Y: .1}, {X: .8, Y: .1}, {X: .8, Y: .9}, {X: .5, Y: .9}}},
	}); err != nil {
		t.Fatalf("configure counting rules: %v", err)
	}
	updates := stage.SubscribeTrackUpdates()

	first := trackerTestFrame("frame-1", time.Second, "")
	first.Detections[0].BoundingBox.X = .15
	stage.Process(first)
	created := receiveTrackUpdate(t, updates)
	if created.CurrentVisible != 0 || created.UniqueConfirmed != 0 || len(created.CountEvents) != 0 {
		t.Fatalf("tentative counts = %#v", created)
	}

	second := trackerTestFrame("frame-2", 1100*time.Millisecond, "")
	second.Detections[0].BoundingBox.X = .25
	stage.Process(second)
	active := receiveTrackUpdate(t, updates)
	if active.CurrentVisible != 1 || active.UniqueConfirmed != 1 || len(active.CountEvents) != 0 {
		t.Fatalf("active counts = %#v", active)
	}

	third := trackerTestFrame("frame-3", 1200*time.Millisecond, "")
	third.Detections[0].BoundingBox.X = .55
	stage.Process(third)
	crossed := receiveTrackUpdate(t, updates)
	if len(crossed.CountEvents) != 2 || crossed.CountEvents[0].TrackSessionID != "session-counts-1" {
		t.Fatalf("crossing events = %#v", crossed.CountEvents)
	}
	eventTypes := []TrackCountEventType{crossed.CountEvents[0].EventType, crossed.CountEvents[1].EventType}
	slices.Sort(eventTypes)
	if !slices.Equal(eventTypes, []TrackCountEventType{TrackCountLineReverse, TrackCountPolygonEntry}) {
		t.Fatalf("crossing event types = %#v", eventTypes)
	}

	fourth := trackerTestFrame("frame-4", 1300*time.Millisecond, "")
	fourth.Detections[0].BoundingBox.X = .85
	stage.Process(fourth)
	exited := receiveTrackUpdate(t, updates)
	if len(exited.CountEvents) != 1 || exited.CountEvents[0].EventType != TrackCountPolygonExit {
		t.Fatalf("polygon exit = %#v", exited.CountEvents)
	}

	backend.associations = nil
	stage.Process(trackerTestFrame("frame-5", 1400*time.Millisecond, ""))
	occluded := receiveTrackUpdate(t, updates)
	if occluded.CurrentVisible != 0 || occluded.UniqueConfirmed != 1 {
		t.Fatalf("occluded counts = %#v", occluded)
	}

	stage.Reset(TrackingResetDeactivated)
	closed := receiveTrackUpdate(t, updates)
	if !closed.SessionEnded || closed.TrackSessionID != "session-counts-1" {
		t.Fatalf("closed count session = %#v", closed)
	}
	backend.associations = []TrackAssociation{{DetectionIndex: 0, TrackKey: "track-1"}}
	for index, x := range []float64{.55, .56} {
		frame := trackerTestFrame(fmt.Sprintf("reset-frame-%d", index), time.Duration(2+index)*time.Second, "")
		frame.Detections[0].BoundingBox.X = x
		stage.Process(frame)
		resetUpdate := receiveTrackUpdate(t, updates)
		if resetUpdate.TrackSessionID != "session-counts-2" || len(resetUpdate.CountEvents) != 0 {
			t.Fatalf("new session counts leaked from prior session = %#v", resetUpdate)
		}
		if index == 1 && resetUpdate.UniqueConfirmed != 1 {
			t.Fatalf("new session unique count = %d, want 1", resetUpdate.UniqueConfirmed)
		}
	}
}

func TestTrackingCountsDoNotInferCrossingAcrossLongObservationGap(t *testing.T) {
	backend := &fakeTrackerBackend{algorithm: TrackerAlgorithmByteTrack, associations: []TrackAssociation{{DetectionIndex: 0, TrackKey: "track-1"}}}
	lifecycle := DefaultTrackLifecycleConfig()
	lifecycle.ConfirmationObservations = 1
	stage, err := NewTrackingStageWithLifecycle(backend, 5*time.Second, lifecycle)
	if err != nil {
		t.Fatalf("configure lifecycle: %v", err)
	}
	stage.newSessionID = func() string { return "session-gap" }
	if err := stage.ReplaceCountingRules("a8-main", []CountingRule{{
		RuleID: "gate", Label: "Gate", RuleType: CountingRuleLine, Revision: 1,
		Points: []NormalizedPoint{{X: .5, Y: 0}, {X: .5, Y: 1}},
	}}); err != nil {
		t.Fatalf("configure counting rule: %v", err)
	}
	updates := stage.SubscribeTrackUpdates()
	first := trackerTestFrame("frame-1", time.Second, "")
	first.Detections[0].BoundingBox.X = .1
	stage.Process(first)
	_ = receiveTrackUpdate(t, updates)
	second := trackerTestFrame("frame-2", 1900*time.Millisecond, "")
	second.Detections[0].BoundingBox.X = .8
	stage.Process(second)
	select {
	case update := <-updates:
		if len(update.CountEvents) != 0 {
			t.Fatalf("long-gap crossing was counted: %#v", update.CountEvents)
		}
	default:
	}
}

func receiveTrackUpdate(t *testing.T, updates <-chan TrackUpdateBatch) TrackUpdateBatch {
	t.Helper()
	select {
	case update := <-updates:
		return update
	case <-time.After(time.Second):
		t.Fatal("track lifecycle update was not published")
		return TrackUpdateBatch{}
	}
}

func trackerTestFrame(frameID string, pts time.Duration, upstreamTrackID string) Frame {
	return Frame{
		SourceID: "a8-main", StreamEpoch: "epoch-1", FrameID: frameID,
		ObservedAt: time.Unix(1, int64(pts)).UTC(), SourcePTSNS: int64(pts),
		Timing:     &FrameTiming{SourcePTSPresent: true, PipelineIngressMonotonicNS: int64(10*time.Second + pts), PipelineIngressUnixNS: time.Unix(1, int64(pts)).UnixNano()},
		ImageWidth: 1920, ImageHeight: 1080,
		Model: ModelIdentity{Name: "objects", Version: "1", ArtifactHash: "sha256:model"},
		Detections: []Detection{{
			TrackID: upstreamTrackID, ClassID: 0, ClassLabel: "person", Confidence: 0.9,
			BoundingBox: BoundingBox{X: 0.1, Y: 0.2, Width: 0.1, Height: 0.2},
		}},
	}
}
