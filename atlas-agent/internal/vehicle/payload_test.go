package vehicle

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync/atomic"
	"testing"
	"time"

	camerapb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/camera"
	gimbalpb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/gimbal"
	"google.golang.org/grpc"
)

type recordingCameraServer struct {
	camerapb.UnimplementedCameraServiceServer
	subscriptions atomic.Int32
	zoomCalls     atomic.Int32
}

func (server *recordingCameraServer) SubscribeCameraList(_ *camerapb.SubscribeCameraListRequest, stream grpc.ServerStreamingServer[camerapb.CameraListResponse]) error {
	server.subscriptions.Add(1)
	return stream.Send(&camerapb.CameraListResponse{CameraList: &camerapb.CameraList{Cameras: []*camerapb.Information{{ComponentId: 100}}}})
}

func (server *recordingCameraServer) ZoomRange(context.Context, *camerapb.ZoomRangeRequest) (*camerapb.ZoomRangeResponse, error) {
	server.zoomCalls.Add(1)
	return &camerapb.ZoomRangeResponse{CameraResult: &camerapb.CameraResult{Result: camerapb.CameraResult_RESULT_SUCCESS}}, nil
}

func TestSIYICameraTransportNeverActivatesMAVSDKCamera(t *testing.T) {
	grpcListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen gRPC: %v", err)
	}
	grpcServer := grpc.NewServer()
	cameraRecorder := &recordingCameraServer{}
	camerapb.RegisterCameraServiceServer(grpcServer, cameraRecorder)
	go func() { _ = grpcServer.Serve(grpcListener) }()
	t.Cleanup(grpcServer.Stop)

	siyiConnection, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen SIYI UDP: %v", err)
	}
	t.Cleanup(func() { _ = siyiConnection.Close() })
	siyiRequests := make(chan byte, 2)
	go func() {
		for _, response := range [][]byte{
			siyiFrame(0, siyiMaximumZoomCommand, []byte{6, 0}),
			siyiFrame(1, siyiAbsoluteZoomCommand, []byte{1}),
		} {
			buffer := make([]byte, 256)
			length, client, readErr := siyiConnection.ReadFromUDP(buffer)
			if readErr != nil {
				return
			}
			command, _, decodeErr := decodeSIYIFrame(buffer[:length])
			if decodeErr != nil {
				return
			}
			siyiRequests <- command
			if _, writeErr := siyiConnection.WriteToUDP(response, client); writeErr != nil {
				return
			}
		}
	}()

	controller, err := NewPayloadController(grpcListener.Addr().String(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new payload controller: %v", err)
	}
	t.Cleanup(func() { _ = controller.Close() })
	controller.ConfigureCameraTransports(false, siyiConnection.LocalAddr().String())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ids, err := controller.DiscoverCameras(ctx)
	if err != nil {
		t.Fatalf("discover SIYI camera: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("MAVSDK camera ids = %v, want none", ids)
	}
	result, err := controller.setZoom(ctx, payloadCommand{CameraComponentID: 100, ZoomPercent: 50})
	if err != nil || result.Code != "SIYI_ZOOM_SET" {
		t.Fatalf("SIYI zoom result = %#v, error = %v", result, err)
	}
	if cameraRecorder.subscriptions.Load() != 0 || cameraRecorder.zoomCalls.Load() != 0 {
		t.Fatalf("MAVSDK camera calls = subscriptions %d zoom %d, want zero", cameraRecorder.subscriptions.Load(), cameraRecorder.zoomCalls.Load())
	}
	if first, second := <-siyiRequests, <-siyiRequests; first != siyiMaximumZoomCommand || second != siyiAbsoluteZoomCommand {
		t.Fatalf("SIYI commands = [%x %x]", first, second)
	}
	if !containsCapability(controller.Capabilities(), "camera:transport:siyi_udp") {
		t.Fatalf("capabilities = %v, want SIYI transport", controller.Capabilities())
	}
}

func TestMAVSDKCameraTransportDiscoversThroughCameraService(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen gRPC: %v", err)
	}
	server := grpc.NewServer()
	recorder := &recordingCameraServer{}
	camerapb.RegisterCameraServiceServer(server, recorder)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	controller, err := NewPayloadController(listener.Addr().String(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new payload controller: %v", err)
	}
	t.Cleanup(func() { _ = controller.Close() })
	controller.ConfigureCameraTransports(true, "")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ids, err := controller.DiscoverCameras(ctx)
	if err != nil {
		t.Fatalf("discover MAVSDK camera: %v", err)
	}
	if len(ids) != 1 || ids[0] != 100 || recorder.subscriptions.Load() != 1 {
		t.Fatalf("MAVSDK discovery ids = %v subscriptions = %d", ids, recorder.subscriptions.Load())
	}
	if !containsCapability(controller.Capabilities(), "camera:transport:mavsdk") {
		t.Fatalf("capabilities = %v, want MAVSDK transport", controller.Capabilities())
	}
}

func containsCapability(capabilities []string, wanted string) bool {
	for _, capability := range capabilities {
		if capability == wanted {
			return true
		}
	}
	return false
}

func TestPayloadManualOverrideRestoresCurrentWaypointIntent(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := grpc.NewServer()
	recorder := &recordingGimbalServer{
		control:  make(chan *gimbalpb.TakeControlRequest, 8),
		angles:   make(chan *gimbalpb.SetAnglesRequest, 8),
		rates:    make(chan *gimbalpb.SetAngularRatesRequest, 8),
		releases: make(chan *gimbalpb.ReleaseControlRequest, 8),
	}
	gimbalpb.RegisterGimbalServiceServer(server, recorder)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	controller, err := NewPayloadController(listener.Addr().String(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new payload controller: %v", err)
	}
	t.Cleanup(func() { _ = controller.Close() })
	controller.gimbalIDs = []int32{1}
	global := gimbalIntent{pitch: -90, yawMode: "FOLLOW_LANE_DIRECTION"}
	waypoint := gimbalIntent{pitch: -45, yawMode: "FOLLOW_LANE_DIRECTION"}
	controller.ConfigureMission("run-1", payloadMissionPlan{
		global: payloadIntent{gimbal: &global},
		waypoints: map[uint32]payloadIntent{
			1: {gimbal: &waypoint},
		},
	})
	if err := controller.ActivateMission(context.Background(), "run-1", "RUNNING"); err != nil {
		t.Fatalf("activate mission payload: %v", err)
	}
	if got := (<-recorder.angles).GetPitchDeg(); got != -90 {
		t.Fatalf("initial mission pitch = %v, want -90", got)
	}

	if _, err := controller.Execute(context.Background(), "payload_control_begin", `{"controlContext":{"kind":"mission_override","missionRunId":"run-1"},"controlSessionId":"manual-1","leaseDurationMs":7000,"gimbalId":1}`); err != nil {
		t.Fatalf("begin manual control: %v", err)
	}
	if _, err := controller.Execute(context.Background(), "gimbal_set_angles", `{"controlContext":{"kind":"mission_override","missionRunId":"run-1"},"controlSessionId":"manual-1","gimbalId":1,"pitchDegrees":-20,"yawDegrees":0,"yawFrame":"AIRCRAFT_RELATIVE"}`); err != nil {
		t.Fatalf("set manual angle: %v", err)
	}
	if got := (<-recorder.angles).GetPitchDeg(); got != -20 {
		t.Fatalf("manual pitch = %v, want -20", got)
	}

	controller.MissionProgress(context.Background(), "run-1", 1)
	select {
	case unexpected := <-recorder.angles:
		t.Fatalf("waypoint transition stole manual control: %#v", unexpected)
	default:
	}
	if _, err := controller.Execute(context.Background(), "payload_control_end", `{"controlContext":{"kind":"mission_override","missionRunId":"run-1"},"controlSessionId":"manual-1","gimbalId":1}`); err != nil {
		t.Fatalf("return to mission view: %v", err)
	}
	if got := (<-recorder.angles).GetPitchDeg(); got != -45 {
		t.Fatalf("restored pitch = %v, want current waypoint pitch -45", got)
	}
}

func TestInspectionControlDoesNotRequireMissionAndReleasesSafely(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := grpc.NewServer()
	recorder := &recordingGimbalServer{
		control:  make(chan *gimbalpb.TakeControlRequest, 4),
		rates:    make(chan *gimbalpb.SetAngularRatesRequest, 4),
		releases: make(chan *gimbalpb.ReleaseControlRequest, 4),
	}
	gimbalpb.RegisterGimbalServiceServer(server, recorder)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	controller, err := NewPayloadController(listener.Addr().String(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new payload controller: %v", err)
	}
	t.Cleanup(func() { _ = controller.Close() })
	controller.gimbalIDs = []int32{1}

	if _, err := controller.Execute(context.Background(), "payload_control_begin", `{"controlContext":{"kind":"inspection"},"controlSessionId":"inspection-1","leaseDurationMs":7000,"gimbalId":1}`); err != nil {
		t.Fatalf("begin inspection control: %v", err)
	}
	if got := (<-recorder.control).GetGimbalId(); got != 1 {
		t.Fatalf("controlled gimbal = %d, want 1", got)
	}
	if _, err := controller.Execute(context.Background(), "gimbal_set_rates", `{"controlContext":{"kind":"inspection"},"controlSessionId":"inspection-1","gimbalId":1,"pitchRateDegreesPerSecond":15,"yawRateDegreesPerSecond":0,"yawFrame":"AIRCRAFT_RELATIVE"}`); err != nil {
		t.Fatalf("set inspection rate: %v", err)
	}
	if got := (<-recorder.rates).GetPitchRateDegS(); got != 15 {
		t.Fatalf("inspection pitch rate = %v, want 15", got)
	}
	if _, err := controller.Execute(context.Background(), "payload_control_end", `{"controlContext":{"kind":"inspection"},"controlSessionId":"inspection-1","gimbalId":1}`); err != nil {
		t.Fatalf("end inspection control: %v", err)
	}
	if stopped := <-recorder.rates; stopped.GetPitchRateDegS() != 0 || stopped.GetYawRateDegS() != 0 {
		t.Fatalf("inspection stop rates = %#v", stopped)
	}
	if got := (<-recorder.releases).GetGimbalId(); got != 1 {
		t.Fatalf("released gimbal = %d, want 1", got)
	}

	if _, err := controller.Execute(context.Background(), "payload_control_begin", `{"controlContext":{"kind":"inspection"},"controlSessionId":"inspection-expiry","leaseDurationMs":7000,"gimbalId":1}`); err != nil {
		t.Fatalf("begin inspection control for expiry: %v", err)
	}
	<-recorder.control
	controller.mu.Lock()
	controller.manual.expirationTimer.Stop()
	controller.manual.expiresAt = time.Now().Add(-time.Second)
	controller.mu.Unlock()
	controller.expireManual("inspection-expiry")
	if stopped := <-recorder.rates; stopped.GetPitchRateDegS() != 0 || stopped.GetYawRateDegS() != 0 {
		t.Fatalf("expired inspection stop rates = %#v", stopped)
	}
	if got := (<-recorder.releases).GetGimbalId(); got != 1 {
		t.Fatalf("expired inspection released gimbal = %d, want 1", got)
	}
}

func TestMissionActivationRejectsActiveInspectionControl(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := grpc.NewServer()
	recorder := &recordingGimbalServer{control: make(chan *gimbalpb.TakeControlRequest, 2)}
	gimbalpb.RegisterGimbalServiceServer(server, recorder)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	controller, err := NewPayloadController(listener.Addr().String(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new payload controller: %v", err)
	}
	t.Cleanup(func() { _ = controller.Close() })
	controller.gimbalIDs = []int32{1}
	if _, err := controller.Execute(context.Background(), "payload_control_begin", `{"controlContext":{"kind":"inspection"},"controlSessionId":"inspection-1","leaseDurationMs":7000,"gimbalId":1}`); err != nil {
		t.Fatalf("begin inspection control: %v", err)
	}
	controller.ConfigureMission("run-1", missionPayloadPlan(nil))
	if err := controller.ActivateMission(context.Background(), "run-1", "RUNNING"); err == nil {
		t.Fatal("mission activation succeeded while inspection control was active")
	}
}

func TestMissionPayloadPlanAlwaysHasConcreteRestoreIntent(t *testing.T) {
	plan := missionPayloadPlan(nil)
	if plan.global.gimbal == nil || plan.global.gimbal.pitch != -35 || plan.global.gimbal.yawMode != "FOLLOW_DRONE_HEADING" {
		t.Fatalf("default gimbal intent = %#v", plan.global.gimbal)
	}
	if plan.global.zoom == nil || *plan.global.zoom != 0 {
		t.Fatalf("default zoom intent = %#v", plan.global.zoom)
	}
}
