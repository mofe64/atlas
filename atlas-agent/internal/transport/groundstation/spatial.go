package groundstation

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/config"
	"github.com/sunnyside/atlas/atlas-agent/internal/identity"
	"github.com/sunnyside/atlas/atlas-agent/internal/spatial"
	pb "github.com/sunnyside/atlas/atlas-agent/internal/transport/groundstationpb"
)

type spatialDemand struct {
	mu      sync.Mutex
	expires map[string]time.Time
}

func newSpatialDemand() *spatialDemand { return &spatialDemand{expires: make(map[string]time.Time)} }

func (d *spatialDemand) apply(request *pb.SpatialCloudSubscription, now time.Time) error {
	if request.GetSubscriptionId() == "" || request.GetPurpose() != "indoor_view" {
		return errors.New("invalid spatial cloud subscription")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	switch request.GetAction() {
	case pb.SpatialCloudSubscriptionAction_SPATIAL_CLOUD_SUBSCRIPTION_ACTION_START_OR_RENEW:
		lease := time.Duration(request.GetLeaseDurationMs()) * time.Millisecond
		if lease < 3*time.Second || lease > 30*time.Second {
			return errors.New("spatial cloud lease must be between 3s and 30s")
		}
		d.expires[request.GetSubscriptionId()] = now.Add(lease)
	case pb.SpatialCloudSubscriptionAction_SPATIAL_CLOUD_SUBSCRIPTION_ACTION_STOP:
		delete(d.expires, request.GetSubscriptionId())
	default:
		return errors.New("unsupported spatial cloud subscription action")
	}
	return nil
}

func (d *spatialDemand) active(now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, expiry := range d.expires {
		if !expiry.After(now) {
			delete(d.expires, id)
		}
	}
	return len(d.expires) > 0
}

func (d *spatialDemand) clear() {
	d.mu.Lock()
	defer d.mu.Unlock()
	clear(d.expires)
}

func runSpatial(ctx context.Context, logger *slog.Logger, client pb.GroundStationServiceClient, cfg config.Config, localIdentity identity.Identity, sessionID string, outputs spatial.Outputs, demand *spatialDemand) {
	backoff := minimumRetry
	for ctx.Err() == nil {
		err := streamSpatial(ctx, client, cfg, localIdentity, sessionID, outputs, demand)
		if ctx.Err() != nil {
			return
		}
		logger.Warn("spatial stream ended; reconnecting", "error", err, "retry_after", backoff)
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

func streamSpatial(ctx context.Context, client pb.GroundStationServiceClient, cfg config.Config, localIdentity identity.Identity, sessionID string, outputs spatial.Outputs, demand *spatialDemand) error {
	stream, err := client.OpenSpatialStream(ctx)
	if err != nil {
		return fmt.Errorf("open spatial stream: %w", err)
	}
	defer stream.CloseSend()
	defer demand.clear()
	streamID := identity.NewID()
	if err := stream.Send(&pb.AgentSpatial{
		SessionId: sessionID, DroneId: localIdentity.DroneID,
		Payload: &pb.AgentSpatial_Registration{Registration: &pb.SpatialStreamRegistration{
			StreamId: streamID, InstallationId: localIdentity.InstallationID,
			ProtocolVersion: spatial.TransportProtocolVersion, SourceId: cfg.SpatialSourceID,
			MaximumPoints: spatial.MaximumPoints, ObservedAtUnixMs: time.Now().UTC().UnixMilli(),
		}},
	}); err != nil {
		return fmt.Errorf("register spatial stream: %w", err)
	}
	response, err := stream.Recv()
	if err != nil {
		return fmt.Errorf("receive spatial registration response: %w", err)
	}
	accepted := response.GetStreamAccepted()
	if accepted == nil || accepted.GetStreamId() != streamID {
		return errors.New("ground station did not accept spatial stream")
	}

	receiveErrors := make(chan error, 1)
	subscriptions := make(chan *pb.SpatialCloudSubscription, 4)
	go func() {
		for {
			message, receiveErr := stream.Recv()
			if receiveErr != nil {
				receiveErrors <- receiveErr
				return
			}
			if subscription := message.GetCloudSubscription(); subscription != nil {
				subscriptions <- subscription
			}
		}
	}()

	frames := outputs.Frames
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case receiveErr := <-receiveErrors:
			if errors.Is(receiveErr, io.EOF) {
				return errors.New("ground station closed spatial stream")
			}
			return receiveErr
		case subscription := <-subscriptions:
			if err := demand.apply(subscription, time.Now()); err != nil {
				return err
			}
		case frame, ok := <-frames:
			if !ok {
				frames = nil
				continue
			}
			if !demand.active(time.Now()) {
				continue
			}
			if err := stream.Send(spatialCloudMessage(sessionID, localIdentity.DroneID, frame)); err != nil {
				return fmt.Errorf("send complete spatial cloud: %w", err)
			}
		}
	}
}

func spatialCloudMessage(sessionID, droneID string, frame spatial.Frame) *pb.AgentSpatial {
	var pose *pb.SpatialPose
	if frame.Pose != nil {
		pose = &pb.SpatialPose{
			CaptureNs: frame.Pose.CaptureNS, FrameId: frame.Pose.FrameID,
			ChildFrameId: frame.Pose.ChildFrameID,
			X:            frame.Pose.Position[0], Y: frame.Pose.Position[1], Z: frame.Pose.Position[2],
			Qw: frame.Pose.OrientationWXYZ[0], Qx: frame.Pose.OrientationWXYZ[1],
			Qy: frame.Pose.OrientationWXYZ[2], Qz: frame.Pose.OrientationWXYZ[3],
		}
	}
	return &pb.AgentSpatial{
		SessionId: sessionID, DroneId: droneID,
		Payload: &pb.AgentSpatial_Cloud{Cloud: &pb.SpatialCloudSnapshot{
			SourceId: frame.SourceID, StreamEpoch: frame.StreamEpoch, Sequence: frame.Sequence,
			ObservedAtUnixMs: frame.ObservedAtUnixMS, CaptureNs: frame.CaptureNS,
			FrameId: frame.FrameID, VoxelSizeM: frame.VoxelSizeM,
			PointCount: frame.PointCount, XyzF32Le: frame.XYZF32LE, Pose: pose,
		}},
	}
}
