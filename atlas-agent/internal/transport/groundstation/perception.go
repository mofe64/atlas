package groundstation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/config"
	"github.com/sunnyside/atlas/atlas-agent/internal/identity"
	"github.com/sunnyside/atlas/atlas-agent/internal/perception"
	pb "github.com/sunnyside/atlas/atlas-agent/internal/transport/groundstationpb"
)

func runPerception(ctx context.Context, logger *slog.Logger, client pb.GroundStationServiceClient, cfg config.Config, localIdentity identity.Identity, sessionID string, outputs perception.Outputs, demand *frameDemand) {
	backoff := minimumRetry
	for ctx.Err() == nil {
		err := streamPerception(ctx, client, cfg, localIdentity, sessionID, outputs, demand)
		if ctx.Err() != nil {
			return
		}
		logger.Warn("perception stream ended; reconnecting", "error", err, "retry_after", backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff *= 2
		if backoff > maximumRetry {
			backoff = maximumRetry
		}
	}
}

func streamPerception(ctx context.Context, client pb.GroundStationServiceClient, cfg config.Config, localIdentity identity.Identity, sessionID string, outputs perception.Outputs, demand *frameDemand) error {
	stream, err := client.OpenPerceptionStream(ctx)
	if err != nil {
		return fmt.Errorf("open perception stream: %w", err)
	}
	defer stream.CloseSend()
	defer demand.clearSubscriptions()
	streamID := identity.NewID()
	capabilities := perceptionCapabilities(cfg)
	if err := stream.Send(&pb.AgentPerception{
		SessionId: sessionID,
		DroneId:   localIdentity.DroneID,
		Payload: &pb.AgentPerception_Registration{Registration: &pb.PerceptionStreamRegistration{
			StreamId:         streamID,
			InstallationId:   localIdentity.InstallationID,
			ProtocolVersion:  perception.TransportProtocolVersion,
			Provider:         cfg.PerceptionProvider,
			Capabilities:     capabilities,
			ObservedAtUnixMs: time.Now().UTC().UnixMilli(),
		}},
	}); err != nil {
		return fmt.Errorf("register perception stream: %w", err)
	}
	response, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("receive perception registration response: %w", err)
	}
	accepted := response.GetStreamAccepted()
	if accepted == nil || accepted.GetStreamId() != streamID {
		return errors.New("ground station did not accept perception stream")
	}

	receiveErrors := make(chan error, 1)
	subscriptionUpdates := make(chan *pb.PerceptionFrameSubscription, 8)
	countingRuleUpdates := make(chan *pb.PerceptionCountingRuleSet, 4)
	go func() {
		for {
			message, err := stream.Recv()
			if err != nil {
				receiveErrors <- err
				return
			}
			if subscription := message.GetFrameSubscription(); subscription != nil {
				subscriptionUpdates <- subscription
			} else if rules := message.GetCountingRules(); rules != nil {
				countingRuleUpdates <- rules
			}
		}
	}()

	frames := outputs.Frames
	healthUpdates := outputs.Health
	trackUpdates := outputs.TrackUpdates
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-receiveErrors:
			if errors.Is(err, io.EOF) {
				return errors.New("ground station closed perception stream")
			}
			return err
		case subscription := <-subscriptionUpdates:
			if err := demand.applySubscription(subscription, time.Now()); err != nil {
				return fmt.Errorf("apply perception frame subscription: %w", err)
			}
			if outputs.Control != nil {
				claimID := "live_view:" + subscription.GetSubscriptionId()
				controlContext, cancel := context.WithTimeout(ctx, 15*time.Second)
				var controlErr error
				switch subscription.GetAction() {
				case pb.PerceptionFrameSubscriptionAction_PERCEPTION_FRAME_SUBSCRIPTION_ACTION_START_OR_RENEW:
					_, controlErr = outputs.Control.Acquire(controlContext, perception.Claim{
						ID:            claimID,
						Owner:         "live_view",
						LeaseDuration: time.Duration(subscription.GetLeaseDurationMs()) * time.Millisecond,
					})
				case pb.PerceptionFrameSubscriptionAction_PERCEPTION_FRAME_SUBSCRIPTION_ACTION_STOP:
					_, controlErr = outputs.Control.Release(controlContext, claimID)
				}
				cancel()
				if controlErr != nil {
					return fmt.Errorf("apply perception runtime activation claim: %w", controlErr)
				}
			}
		case ruleSet := <-countingRuleUpdates:
			if outputs.Counting == nil {
				return errors.New("ground station sent counting rules but tracking does not support them")
			}
			rules := make([]perception.CountingRule, 0, len(ruleSet.GetRules()))
			for _, rule := range ruleSet.GetRules() {
				points := make([]perception.NormalizedPoint, 0, len(rule.GetPoints()))
				for _, point := range rule.GetPoints() {
					points = append(points, perception.NormalizedPoint{X: point.GetX(), Y: point.GetY()})
				}
				rules = append(rules, perception.CountingRule{
					RuleID: rule.GetRuleId(), Label: rule.GetLabel(),
					RuleType: perception.CountingRuleType(rule.GetRuleType()), Revision: rule.GetRevision(),
					Points: points, ClassIDs: append([]int32(nil), rule.GetClassIds()...),
				})
			}
			if err := outputs.Counting.ReplaceCountingRules(ruleSet.GetSourceId(), rules); err != nil {
				return fmt.Errorf("apply perception counting rules: %w", err)
			}
		case frame, ok := <-frames:
			if !ok {
				frames = nil
				continue
			}
			if !demand.active(time.Now()) {
				continue
			}
			if err := stream.Send(perceptionFrameMessage(sessionID, localIdentity.DroneID, frame)); err != nil {
				return fmt.Errorf("send perception frame: %w", err)
			}
		case health, ok := <-healthUpdates:
			if !ok {
				healthUpdates = nil
				continue
			}
			if err := stream.Send(perceptionHealthMessage(sessionID, localIdentity.DroneID, health)); err != nil {
				return fmt.Errorf("send perception health: %w", err)
			}
		case batch, ok := <-trackUpdates:
			if !ok {
				trackUpdates = nil
				continue
			}
			if err := stream.Send(perceptionTrackUpdateMessage(sessionID, localIdentity.DroneID, batch)); err != nil {
				return fmt.Errorf("send perception track lifecycle: %w", err)
			}
		}
	}
}

func perceptionFrameMessage(sessionID, droneID string, frame perception.Frame) *pb.AgentPerception {
	detections := make([]*pb.PerceptionDetection, 0, len(frame.Detections))
	for _, detection := range frame.Detections {
		detections = append(detections, &pb.PerceptionDetection{
			TrackId:        detection.TrackID,
			ClassId:        detection.ClassID,
			ClassLabel:     detection.ClassLabel,
			Confidence:     detection.Confidence,
			AttributesJson: string(detection.AttributesRaw),
			BoundingBox: &pb.NormalizedBoundingBox{
				X: detection.BoundingBox.X, Y: detection.BoundingBox.Y,
				Width: detection.BoundingBox.Width, Height: detection.BoundingBox.Height,
			},
		})
	}
	return &pb.AgentPerception{
		SessionId: sessionID,
		DroneId:   droneID,
		Payload: &pb.AgentPerception_Frame{Frame: &pb.PerceptionFrame{
			SourceId:           frame.SourceID,
			StreamEpoch:        frame.StreamEpoch,
			FrameId:            frame.FrameID,
			ObservedAtUnixMs:   frame.ObservedAt.UTC().UnixMilli(),
			SourcePtsNs:        frame.SourcePTSNS,
			ImageWidth:         frame.ImageWidth,
			ImageHeight:        frame.ImageHeight,
			Model:              perceptionModelMessage(frame.Model),
			InferenceLatencyMs: frame.InferenceLatencyMS,
			Detections:         detections,
		}},
	}
}

func perceptionHealthMessage(sessionID, droneID string, health perception.Health) *pb.AgentPerception {
	var model *pb.PerceptionModelIdentity
	if health.Model.Name != "" || health.Model.Version != "" {
		model = perceptionModelMessage(health.Model)
	}
	var tracking *pb.PerceptionTrackingHealth
	if health.Tracking != nil {
		tracking = &pb.PerceptionTrackingHealth{
			Algorithm:              string(health.Tracking.Algorithm),
			State:                  health.Tracking.State,
			SessionId:              health.Tracking.SessionID,
			LastResetReason:        string(health.Tracking.LastResetReason),
			ResetCount:             health.Tracking.ResetCount,
			LastError:              health.Tracking.LastError,
			CameraMotionState:      health.Tracking.CameraMotionState,
			CameraMotionMethod:     health.Tracking.CameraMotionMethod,
			CameraMotionConfidence: health.Tracking.CameraMotionConfidence,
			ReIdEnabled:            health.Tracking.ReIDEnabled,
		}
	}
	return &pb.AgentPerception{
		SessionId: sessionID,
		DroneId:   droneID,
		Payload: &pb.AgentPerception_Health{Health: &pb.PerceptionHealth{
			SourceId:              health.SourceID,
			Provider:              health.Provider,
			ActivationState:       health.ActivationState,
			Accelerator:           health.Accelerator,
			InputConnected:        health.InputConnected,
			InferenceReady:        health.InferenceReady,
			OutputPublishing:      health.OutputPublishing,
			InputFps:              health.InputFPS,
			InferenceFps:          health.InferenceFPS,
			DroppedFrames:         health.DroppedFrames,
			LastFrameAtUnixMs:     unixMilliseconds(health.LastFrameAt),
			LastDetectionAtUnixMs: unixMilliseconds(health.LastDetectionAt),
			LastError:             health.LastError,
			Model:                 model,
			ObservedAtUnixMs:      health.ObservedAt.UTC().UnixMilli(),
			Tracking:              tracking,
		}},
	}
}

func perceptionTrackUpdateMessage(sessionID, droneID string, batch perception.TrackUpdateBatch) *pb.AgentPerception {
	tracks := make([]*pb.PerceptionTrackSnapshot, 0, len(batch.Tracks))
	for _, track := range batch.Tracks {
		var predictedBox *pb.NormalizedBoundingBox
		if track.PredictedBox != nil {
			predictedBox = perceptionBoundingBoxMessage(*track.PredictedBox)
		}
		tracks = append(tracks, &pb.PerceptionTrackSnapshot{
			TrackId: track.TrackID, TrackSessionId: track.TrackSessionID,
			TrackerType: string(track.TrackerType), LifecycleState: string(track.LifecycleState),
			Revision: track.Revision, AgeFrames: track.AgeFrames,
			ObservationCount:          track.ObservationCount,
			FirstObservedAtUnixMs:     track.FirstObservedAt.UTC().UnixMilli(),
			LastObservedAtUnixMs:      track.LastObservedAt.UTC().UnixMilli(),
			LatestConfirmedBox:        perceptionBoundingBoxMessage(track.LatestConfirmedBox),
			LatestDetectionConfidence: track.LatestDetectionConfidence,
			PredictedBox:              predictedBox, PredictionConfidence: track.PredictionConfidence,
			ClosedAtUnixMs: unixMilliseconds(track.ClosedAt), ClosureReason: track.ClosureReason,
			ClassId: track.ClassID, ClassLabel: track.ClassLabel, UpdateReason: string(track.UpdateReason),
		})
	}
	ruleCounts := make([]*pb.PerceptionTrackRuleCount, 0, len(batch.RuleCounts))
	for _, count := range batch.RuleCounts {
		ruleCounts = append(ruleCounts, &pb.PerceptionTrackRuleCount{
			RuleId: count.RuleID, RuleRevision: count.RuleRevision, RuleType: string(count.RuleType),
			LineForward: count.LineForward, LineReverse: count.LineReverse,
			PolygonEntries: count.PolygonEntries, PolygonExits: count.PolygonExits,
		})
	}
	countEvents := make([]*pb.PerceptionTrackCountEvent, 0, len(batch.CountEvents))
	for _, event := range batch.CountEvents {
		countEvents = append(countEvents, &pb.PerceptionTrackCountEvent{
			EventId: event.EventID, RuleId: event.RuleID, RuleRevision: event.RuleRevision,
			TrackSessionId: event.TrackSessionID, TrackId: event.TrackID,
			EventType: string(event.EventType), ObservedAtUnixMs: event.ObservedAt.UTC().UnixMilli(),
			Anchor: &pb.NormalizedPoint{X: event.Anchor.X, Y: event.Anchor.Y},
		})
	}
	return &pb.AgentPerception{
		SessionId: sessionID,
		DroneId:   droneID,
		Payload: &pb.AgentPerception_TrackUpdates{TrackUpdates: &pb.PerceptionTrackUpdateBatch{
			SourceId: batch.SourceID, StreamEpoch: batch.StreamEpoch,
			TrackSessionId: batch.TrackSessionID, TrackerType: string(batch.TrackerType),
			ObservedAtUnixMs: batch.ObservedAt.UTC().UnixMilli(),
			SessionStarted:   batch.SessionStarted, SessionEnded: batch.SessionEnded,
			SessionEndReason: batch.SessionEndReason, Tracks: tracks,
			CurrentVisible: batch.CurrentVisible, UniqueConfirmed: batch.UniqueConfirmed,
			RuleCounts: ruleCounts, CountEvents: countEvents,
		}},
	}
}

func perceptionBoundingBoxMessage(boxValue perception.BoundingBox) *pb.NormalizedBoundingBox {
	return &pb.NormalizedBoundingBox{X: boxValue.X, Y: boxValue.Y, Width: boxValue.Width, Height: boxValue.Height}
}

func perceptionModelMessage(model perception.ModelIdentity) *pb.PerceptionModelIdentity {
	return &pb.PerceptionModelIdentity{Name: model.Name, Version: model.Version, ArtifactHash: model.ArtifactHash}
}

func perceptionCapabilities(cfg config.Config) []string {
	capabilities := []string{"object_detection:v1", "health:v1", "frame_subscription:v1", "activation_claims:v1", "tracking_stage:v1", "track_lifecycle:v1", "track_counting:v1"}
	if cfg.TrackerAlgorithm == "byte_track" {
		capabilities = append(capabilities, "tracker:byte_track:foundationvision:v1", "camera_motion:none", "reid:disabled")
	} else if cfg.TrackerAlgorithm == "byte_track_cmc" {
		capabilities = append(capabilities, "tracker:byte_track_cmc:atlas:v1", "camera_motion:sparse_optical_flow:v1", "reid:disabled")
	}
	return capabilities
}

func unixMilliseconds(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UTC().UnixMilli()
}
