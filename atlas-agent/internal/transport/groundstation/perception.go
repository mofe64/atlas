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
	if err := stream.Send(&pb.AgentPerception{
		SessionId: sessionID,
		DroneId:   localIdentity.DroneID,
		Payload: &pb.AgentPerception_Registration{Registration: &pb.PerceptionStreamRegistration{
			StreamId:         streamID,
			InstallationId:   localIdentity.InstallationID,
			ProtocolVersion:  perception.RuntimeProtocolVersion,
			Provider:         cfg.PerceptionProvider,
			Capabilities:     []string{"object_detection:v1", "health:v1", "frame_subscription:v1"},
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
	go func() {
		for {
			message, err := stream.Recv()
			if err != nil {
				receiveErrors <- err
				return
			}
			if subscription := message.GetFrameSubscription(); subscription != nil {
				subscriptionUpdates <- subscription
			}
		}
	}()

	frames := outputs.Frames
	healthUpdates := outputs.Health
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
	return &pb.AgentPerception{
		SessionId: sessionID,
		DroneId:   droneID,
		Payload: &pb.AgentPerception_Health{Health: &pb.PerceptionHealth{
			SourceId:              health.SourceID,
			Provider:              health.Provider,
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
		}},
	}
}

func perceptionModelMessage(model perception.ModelIdentity) *pb.PerceptionModelIdentity {
	return &pb.PerceptionModelIdentity{Name: model.Name, Version: model.Version, ArtifactHash: model.ArtifactHash}
}

func unixMilliseconds(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UTC().UnixMilli()
}
