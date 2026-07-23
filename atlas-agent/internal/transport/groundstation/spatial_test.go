package groundstation

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/config"
	"github.com/sunnyside/atlas/atlas-agent/internal/identity"
	"github.com/sunnyside/atlas/atlas-agent/internal/spatial"
	pb "github.com/sunnyside/atlas/atlas-agent/internal/transport/groundstationpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type recordingSpatialGroundStation struct {
	pb.UnimplementedGroundStationServiceServer
	cloud chan *pb.SpatialCloudSnapshot
}

func (server *recordingSpatialGroundStation) OpenSpatialStream(stream pb.GroundStationService_OpenSpatialStreamServer) error {
	registration, err := stream.Recv()
	if err != nil {
		return err
	}
	if err := stream.Send(&pb.GroundStationSpatial{Payload: &pb.GroundStationSpatial_StreamAccepted{StreamAccepted: &pb.SpatialStreamAccepted{
		StreamId: registration.GetRegistration().GetStreamId(), AcceptedAtUnixMs: time.Now().UnixMilli(),
	}}}); err != nil {
		return err
	}
	if err := stream.Send(&pb.GroundStationSpatial{Payload: &pb.GroundStationSpatial_CloudSubscription{CloudSubscription: &pb.SpatialCloudSubscription{
		SubscriptionId: "indoor-1", Purpose: "indoor_view",
		Action:          pb.SpatialCloudSubscriptionAction_SPATIAL_CLOUD_SUBSCRIPTION_ACTION_START_OR_RENEW,
		LeaseDurationMs: 12_000,
	}}}); err != nil {
		return err
	}
	message, err := stream.Recv()
	if err != nil {
		return err
	}
	server.cloud <- message.GetCloud()
	return nil
}

func TestSpatialDemandUsesShortRenewableLeases(t *testing.T) {
	demand := newSpatialDemand()
	now := time.Unix(1, 0)
	request := &pb.SpatialCloudSubscription{SubscriptionId: "view-1", Purpose: "indoor_view", Action: pb.SpatialCloudSubscriptionAction_SPATIAL_CLOUD_SUBSCRIPTION_ACTION_START_OR_RENEW, LeaseDurationMs: 5_000}
	if err := demand.apply(request, now); err != nil {
		t.Fatal(err)
	}
	if !demand.active(now.Add(4 * time.Second)) {
		t.Fatal("lease ended early")
	}
	if demand.active(now.Add(6 * time.Second)) {
		t.Fatal("expired lease remained active")
	}
}

func TestSpatialCloudMessageKeepsTheFullPackedPayload(t *testing.T) {
	payload := make([]byte, 100_000*12)
	message := spatialCloudMessage("session", "drone", spatial.Frame{SourceID: "front-depth", StreamEpoch: "epoch", Sequence: 1, PointCount: 100_000, XYZF32LE: payload})
	if got := message.GetCloud(); got.GetPointCount() != 100_000 || len(got.GetXyzF32Le()) != len(payload) {
		t.Fatalf("cloud = %#v", got)
	}
}

func TestSpatialGRPCStreamDeliversOneCompleteMaximumCloudUnderAViewLease(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	recorder := &recordingSpatialGroundStation{cloud: make(chan *pb.SpatialCloudSnapshot, 1)}
	pb.RegisterGroundStationServiceServer(server, recorder)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	connection, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	frames := make(chan spatial.Frame, 1)
	frame := spatial.Frame{
		SourceID: "front-depth", StreamEpoch: "epoch-1", Sequence: 1,
		ObservedAtUnixMS: time.Now().UnixMilli(), CaptureNS: 1,
		FrameID: "vio_local", VoxelSizeM: 0.05, PointCount: spatial.MaximumPoints,
		XYZF32LE: make([]byte, spatial.MaximumPoints*12),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case frames <- frame:
			case <-ctx.Done():
				return
			}
			select {
			case <-ticker.C:
			case <-ctx.Done():
				return
			}
		}
	}()
	done := make(chan error, 1)
	go func() {
		done <- streamSpatial(ctx, pb.NewGroundStationServiceClient(connection), config.Config{SpatialSourceID: "front-depth"}, identity.Identity{InstallationID: "installation-1", DroneID: "drone-1"}, "session-1", spatial.Outputs{Frames: frames}, newSpatialDemand())
	}()

	select {
	case cloud := <-recorder.cloud:
		if cloud.GetPointCount() != spatial.MaximumPoints || len(cloud.GetXyzF32Le()) != 1_200_000 {
			t.Fatalf("delivered cloud = %d points, %d bytes", cloud.GetPointCount(), len(cloud.GetXyzF32Le()))
		}
	case <-ctx.Done():
		t.Fatal("complete cloud was not delivered before the deadline")
	}
}
