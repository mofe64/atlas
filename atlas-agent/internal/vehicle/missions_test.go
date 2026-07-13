package vehicle

import (
	"context"
	"io"
	"log/slog"
	"math"
	"os"
	"testing"
	"time"

	actionpb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/action"
	missionpb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/mission"
	telemetrypb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/telemetry"
	"google.golang.org/grpc"
)

type recordingActionClient struct {
	actionpb.ActionServiceClient
	order     *[]string
	armResult actionpb.ActionResult_Result
}

func (client *recordingActionClient) Arm(context.Context, *actionpb.ArmRequest, ...grpc.CallOption) (*actionpb.ArmResponse, error) {
	*client.order = append(*client.order, "arm")
	return &actionpb.ArmResponse{ActionResult: &actionpb.ActionResult{Result: client.armResult}}, nil
}

type recordingMissionClient struct {
	missionpb.MissionServiceClient
	order *[]string
}

func (client *recordingMissionClient) StartMission(context.Context, *missionpb.StartMissionRequest, ...grpc.CallOption) (*missionpb.StartMissionResponse, error) {
	*client.order = append(*client.order, "start")
	return &missionpb.StartMissionResponse{MissionResult: &missionpb.MissionResult{Result: missionpb.MissionResult_RESULT_SUCCESS}}, nil
}

func TestMissionStartArmsBeforeStartingMission(t *testing.T) {
	order := []string{}
	executor := &MissionExecutor{
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		action:        &recordingActionClient{order: &order, armResult: actionpb.ActionResult_RESULT_SUCCESS},
		mission:       &recordingMissionClient{order: &order},
		updates:       make(chan MissionUpdate, 8),
		uploadedRunID: "run-1",
		state:         "READY",
		watchCancel:   func() {},
	}

	err := executor.start(context.Background(), context.Background(), MissionOperation{
		OperationID: "operation-1",
		RunID:       "run-1",
		Type:        "start",
	}, false)
	if err != nil {
		t.Fatalf("start mission: %v", err)
	}
	if len(order) != 2 || order[0] != "arm" || order[1] != "start" {
		t.Fatalf("expected arm then start, got %v", order)
	}

	gotTypes := []string{}
	for len(executor.updates) > 0 {
		gotTypes = append(gotTypes, (<-executor.updates).Type)
	}
	wantTypes := []string{"arming", "armed", "started"}
	if len(gotTypes) != len(wantTypes) {
		t.Fatalf("expected lifecycle %v, got %v", wantTypes, gotTypes)
	}
	for index := range wantTypes {
		if gotTypes[index] != wantTypes[index] {
			t.Fatalf("expected lifecycle %v, got %v", wantTypes, gotTypes)
		}
	}
}

func TestMissionStartStopsWhenArmingIsRejected(t *testing.T) {
	order := []string{}
	executor := &MissionExecutor{
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		action:        &recordingActionClient{order: &order, armResult: actionpb.ActionResult_RESULT_COMMAND_DENIED},
		mission:       &recordingMissionClient{order: &order},
		updates:       make(chan MissionUpdate, 8),
		uploadedRunID: "run-1",
		state:         "READY",
		watchCancel:   func() {},
	}

	err := executor.start(context.Background(), context.Background(), MissionOperation{
		OperationID: "operation-1",
		RunID:       "run-1",
		Type:        "start",
	}, false)
	if err == nil {
		t.Fatal("expected rejected arm to stop mission start")
	}
	if len(order) != 1 || order[0] != "arm" {
		t.Fatalf("mission start must not be sent after rejected arm, got %v", order)
	}
}

func TestSITLMissionUploadArmsAndStarts(t *testing.T) {
	address := os.Getenv("ATLAS_TEST_SITL_MAVSDK_ADDR")
	if address == "" {
		t.Skip("set ATLAS_TEST_SITL_MAVSDK_ADDR to run against PX4 SITL")
	}
	executor, err := NewMissionExecutor(address, slog.Default())
	if err != nil {
		t.Fatalf("connect mission executor: %v", err)
	}
	defer executor.Close()

	operation := MissionOperation{
		OperationID: "sitl-upload",
		RunID:       "sitl-auto-arm-run",
		Type:        "upload",
		MissionPlanJSON: `{
			"generatedWaypoints":[
				{"sequence":0,"latitude":37.41908,"longitude":-121.99320,"altitudeMeters":20,"speedMps":4},
				{"sequence":1,"latitude":37.41918,"longitude":-121.99305,"altitudeMeters":20,"speedMps":4}
			],
			"actions":[{"sequence":0,"actionType":"RETURN_TO_LAUNCH","params":{}}]
		}`,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := executor.upload(ctx, operation); err != nil {
		t.Fatalf("upload SITL mission: %v", err)
	}

	operation.OperationID = "sitl-start"
	operation.Type = "start"
	if err := executor.start(ctx, ctx, operation, false); err != nil {
		t.Fatalf("arm and start SITL mission: %v", err)
	}

	telemetry := telemetrypb.NewTelemetryServiceClient(executor.connection)
	armedStream, err := telemetry.SubscribeArmed(ctx, &telemetrypb.SubscribeArmedRequest{})
	if err != nil {
		t.Fatalf("subscribe SITL armed state: %v", err)
	}
	armed, err := armedStream.Recv()
	if err != nil || !armed.GetIsArmed() {
		t.Fatalf("expected Atlas to arm SITL aircraft, armed=%v error=%v", armed.GetIsArmed(), err)
	}
	inAirStream, err := telemetry.SubscribeInAir(ctx, &telemetrypb.SubscribeInAirRequest{})
	if err != nil {
		t.Fatalf("subscribe SITL in-air state: %v", err)
	}
	for {
		inAir, receiveErr := inAirStream.Recv()
		if receiveErr != nil {
			t.Fatalf("wait for SITL takeoff: %v", receiveErr)
		}
		if inAir.GetIsInAir() {
			break
		}
	}

	operation.OperationID = "sitl-rtl"
	operation.Type = "return_to_launch"
	if err := executor.returnToLaunch(ctx, operation); err != nil {
		t.Fatalf("return SITL aircraft to launch: %v", err)
	}
}

func TestTranslateMissionPlanMapsFlightAndGimbalIntent(t *testing.T) {
	translated, err := translateMissionPlan(`{
        "generatedWaypoints": [
          {"sequence":0,"latitude":51,"longitude":-0.1,"altitudeMeters":25,"speedMps":4},
          {"sequence":1,"latitude":51.0005,"longitude":-0.1005,"altitudeMeters":25,"holdSeconds":10}
        ],
        "actions": [
          {"sequence":0,"actionType":"SET_GIMBAL_ORIENTATION","params":{"pitchDegrees":-35,"yawMode":"FOLLOW_DRONE_HEADING"}},
          {"sequence":1,"actionType":"START_RECORDING","params":{}},
          {"sequence":2,"actionType":"NAVIGATE_TO","params":{"waypointSequence":0}},
          {"sequence":3,"actionType":"NAVIGATE_TO","params":{"waypointSequence":1}},
          {"sequence":4,"actionType":"SET_GIMBAL_ORIENTATION","params":{"waypointSequence":1,"pitchDegrees":-45,"yawMode":"LOOK_AT_POINT","target":{"latitude":51.001,"longitude":-0.101}}},
          {"sequence":5,"actionType":"STOP_RECORDING","params":{}},
          {"sequence":6,"actionType":"RETURN_TO_LAUNCH","params":{}}
        ]
      }`)
	if err != nil {
		t.Fatalf("translate mission: %v", err)
	}
	if !translated.returnToLaunch {
		t.Fatal("expected RTL-after-mission option")
	}
	items := translated.plan.GetMissionItems()
	if len(items) != 2 {
		t.Fatalf("got %d mission items", len(items))
	}
	if !math.IsNaN(float64(items[0].GetGimbalPitchDeg())) || !math.IsNaN(float64(items[0].GetGimbalYawDeg())) {
		t.Fatalf("PX4 mission item must not compete with Agent payload ownership: pitch=%v yaw=%v", items[0].GetGimbalPitchDeg(), items[0].GetGimbalYawDeg())
	}
	if translated.payload.global.gimbal == nil || translated.payload.global.gimbal.pitch != -35 {
		t.Fatalf("global payload intent was not retained: %#v", translated.payload.global.gimbal)
	}
	if translated.payload.waypoints[1].gimbal == nil || translated.payload.waypoints[1].gimbal.pitch != -45 || math.IsNaN(float64(items[1].GetYawDeg())) {
		t.Fatalf("waypoint payload intent was not mapped: %#v aircraft_yaw=%v", translated.payload.waypoints[1].gimbal, items[1].GetYawDeg())
	}
	if items[0].GetCameraAction() != missionpb.MissionItem_CAMERA_ACTION_START_VIDEO || items[1].GetCameraAction() != missionpb.MissionItem_CAMERA_ACTION_STOP_VIDEO {
		t.Fatalf("video actions were not mapped: %v %v", items[0].GetCameraAction(), items[1].GetCameraAction())
	}
	if items[0].GetIsFlyThrough() != true || items[1].GetIsFlyThrough() != false || items[1].GetLoiterTimeS() != 10 {
		t.Fatal("hold/fly-through behavior was not mapped")
	}
}

func TestTranslateMissionPlanReportsUnexecutedPerception(t *testing.T) {
	translated, err := translateMissionPlan(`{
        "generatedWaypoints":[{"sequence":0,"latitude":51,"longitude":-0.1,"altitudeMeters":25}],
        "actions":[{"sequence":0,"actionType":"START_PERCEPTION","params":{"detectionClasses":["person"]}}]
      }`)
	if err != nil {
		t.Fatalf("translate mission: %v", err)
	}
	if len(translated.warnings) != 1 {
		t.Fatalf("expected one translation warning, got %v", translated.warnings)
	}
}

func TestTranslateMissionPlanRejectsEmptyPlan(t *testing.T) {
	if _, err := translateMissionPlan(`{"generatedWaypoints":[],"actions":[]}`); err == nil {
		t.Fatal("expected empty plan to fail")
	}
}
