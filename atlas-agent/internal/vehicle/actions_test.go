package vehicle

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	actionpb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/action"
	gimbalpb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/gimbal"
	"google.golang.org/grpc"
)

type recordingActionServer struct {
	actionpb.UnimplementedActionServiceServer
	holdCalls atomic.Int32
}

type recordingGimbalServer struct {
	gimbalpb.UnimplementedGimbalServiceServer
	control       chan *gimbalpb.TakeControlRequest
	angles        chan *gimbalpb.SetAnglesRequest
	rates         chan *gimbalpb.SetAngularRatesRequest
	roi           chan *gimbalpb.SetRoiLocationRequest
	releases      chan *gimbalpb.ReleaseControlRequest
	rejectControl bool
	attitudePitch float32
	attitudeYaw   float32
}

func (s *recordingGimbalServer) GetAttitude(_ context.Context, request *gimbalpb.GetAttitudeRequest) (*gimbalpb.GetAttitudeResponse, error) {
	return &gimbalpb.GetAttitudeResponse{
		GimbalResult: &gimbalpb.GimbalResult{Result: gimbalpb.GimbalResult_RESULT_SUCCESS, ResultStr: "accepted"},
		Attitude: &gimbalpb.Attitude{
			GimbalId:          request.GetGimbalId(),
			EulerAngleForward: &gimbalpb.EulerAngle{PitchDeg: s.attitudePitch, YawDeg: s.attitudeYaw},
		},
	}, nil
}

func (s *recordingGimbalServer) TakeControl(_ context.Context, request *gimbalpb.TakeControlRequest) (*gimbalpb.TakeControlResponse, error) {
	s.control <- request
	if s.rejectControl {
		return &gimbalpb.TakeControlResponse{GimbalResult: &gimbalpb.GimbalResult{Result: gimbalpb.GimbalResult_RESULT_ERROR, ResultStr: "control denied"}}, nil
	}
	return &gimbalpb.TakeControlResponse{GimbalResult: &gimbalpb.GimbalResult{Result: gimbalpb.GimbalResult_RESULT_SUCCESS, ResultStr: "accepted"}}, nil
}

func (s *recordingGimbalServer) SetAngles(_ context.Context, request *gimbalpb.SetAnglesRequest) (*gimbalpb.SetAnglesResponse, error) {
	if s.angles != nil {
		s.angles <- request
	}
	return &gimbalpb.SetAnglesResponse{GimbalResult: &gimbalpb.GimbalResult{Result: gimbalpb.GimbalResult_RESULT_SUCCESS, ResultStr: "accepted"}}, nil
}

func (s *recordingGimbalServer) SetAngularRates(_ context.Context, request *gimbalpb.SetAngularRatesRequest) (*gimbalpb.SetAngularRatesResponse, error) {
	if s.rates != nil {
		s.rates <- request
	}
	return &gimbalpb.SetAngularRatesResponse{GimbalResult: &gimbalpb.GimbalResult{Result: gimbalpb.GimbalResult_RESULT_SUCCESS, ResultStr: "accepted"}}, nil
}

func (s *recordingGimbalServer) SetRoiLocation(_ context.Context, request *gimbalpb.SetRoiLocationRequest) (*gimbalpb.SetRoiLocationResponse, error) {
	if s.roi != nil {
		s.roi <- request
	}
	return &gimbalpb.SetRoiLocationResponse{GimbalResult: &gimbalpb.GimbalResult{Result: gimbalpb.GimbalResult_RESULT_SUCCESS, ResultStr: "accepted"}}, nil
}

func (s *recordingGimbalServer) ReleaseControl(_ context.Context, request *gimbalpb.ReleaseControlRequest) (*gimbalpb.ReleaseControlResponse, error) {
	if s.releases != nil {
		s.releases <- request
	}
	return &gimbalpb.ReleaseControlResponse{GimbalResult: &gimbalpb.GimbalResult{Result: gimbalpb.GimbalResult_RESULT_SUCCESS, ResultStr: "accepted"}}, nil
}

func (s *recordingActionServer) Hold(context.Context, *actionpb.HoldRequest) (*actionpb.HoldResponse, error) {
	s.holdCalls.Add(1)
	return &actionpb.HoldResponse{ActionResult: &actionpb.ActionResult{
		Result:    actionpb.ActionResult_RESULT_SUCCESS,
		ResultStr: "success",
	}}, nil
}

func TestActionExecutorMapsGimbalYawFrameAndAcknowledgement(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := grpc.NewServer()
	recorder := &recordingGimbalServer{
		control: make(chan *gimbalpb.TakeControlRequest, 1),
		angles:  make(chan *gimbalpb.SetAnglesRequest, 1),
	}
	gimbalpb.RegisterGimbalServiceServer(server, recorder)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	executor, err := NewActionExecutor(listener.Addr().String())
	if err != nil {
		t.Fatalf("new executor: %v", err)
	}
	t.Cleanup(func() { _ = executor.Close() })
	configurePayloadForManualTest(executor, "run-1", 2)
	if _, err := executor.Execute(context.Background(), "payload-begin-1", "payload_control_begin", `{"controlContext":{"kind":"mission_override","missionRunId":"run-1"},"controlSessionId":"session-1","leaseDurationMs":7000,"gimbalId":2}`); err != nil {
		t.Fatalf("begin manual payload control: %v", err)
	}

	result, err := executor.Execute(context.Background(), "gimbal-command-1", "gimbal_set_angles", `{"controlContext":{"kind":"mission_override","missionRunId":"run-1"},"controlSessionId":"session-1","gimbalId":2,"pitchDegrees":-45,"yawDegrees":120,"yawFrame":"NORTH_LOCKED"}`)
	if err != nil {
		t.Fatalf("execute gimbal angles: %v", err)
	}
	if result.Code != "RESULT_SUCCESS" {
		t.Fatalf("result = %#v", result)
	}
	controlRequest := <-recorder.control
	if controlRequest.GetGimbalId() != 2 || controlRequest.GetControlMode() != gimbalpb.ControlMode_CONTROL_MODE_PRIMARY {
		t.Fatalf("control request = %#v", controlRequest)
	}
	request := <-recorder.angles
	if request.GetGimbalId() != 2 || request.GetPitchDeg() != -45 || request.GetYawDeg() != 120 || request.GetGimbalMode() != gimbalpb.GimbalMode_GIMBAL_MODE_YAW_LOCK {
		t.Fatalf("gimbal request = %#v", request)
	}
}

func TestActionExecutorReportsGimbalControlRejection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := grpc.NewServer()
	recorder := &recordingGimbalServer{
		control:       make(chan *gimbalpb.TakeControlRequest, 1),
		angles:        make(chan *gimbalpb.SetAnglesRequest, 1),
		rejectControl: true,
	}
	gimbalpb.RegisterGimbalServiceServer(server, recorder)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	executor, err := NewActionExecutor(listener.Addr().String())
	if err != nil {
		t.Fatalf("new executor: %v", err)
	}
	t.Cleanup(func() { _ = executor.Close() })
	configurePayloadForManualTest(executor, "run-1", 1)

	result, err := executor.Execute(context.Background(), "payload-begin-denied", "payload_control_begin", `{"controlContext":{"kind":"mission_override","missionRunId":"run-1"},"controlSessionId":"session-1","leaseDurationMs":7000,"gimbalId":1}`)
	if err == nil {
		t.Fatal("expected gimbal control rejection")
	}
	if result.Code != "RESULT_ERROR" || result.Message != "control denied" {
		t.Fatalf("result = %#v", result)
	}
	select {
	case request := <-recorder.angles:
		t.Fatalf("angle setpoint sent after rejected control: %#v", request)
	default:
	}
}

func configurePayloadForManualTest(executor *ActionExecutor, runID string, gimbalID int32) {
	executor.payload.mu.Lock()
	executor.payload.runID = runID
	executor.payload.runState = "RUNNING"
	executor.payload.gimbalIDs = []int32{gimbalID}
	executor.payload.plan = missionPayloadPlan(nil)
	executor.payload.mu.Unlock()
}

func TestActionExecutorDeduplicatesSuccessfulCommand(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := grpc.NewServer()
	recorder := &recordingActionServer{}
	actionpb.RegisterActionServiceServer(server, recorder)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	executor, err := NewActionExecutor(listener.Addr().String())
	if err != nil {
		t.Fatalf("new executor: %v", err)
	}
	t.Cleanup(func() { _ = executor.Close() })

	for range 2 {
		result, err := executor.Execute(context.Background(), "command-1", "hold", "{}")
		if err != nil {
			t.Fatalf("execute hold: %v", err)
		}
		if result.Code != "RESULT_SUCCESS" {
			t.Fatalf("result = %#v", result)
		}
	}
	if got := recorder.holdCalls.Load(); got != 1 {
		t.Fatalf("hold calls = %d, want 1", got)
	}
}
