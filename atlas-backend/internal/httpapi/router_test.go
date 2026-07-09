package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sunnyside/atlas/atlas-backend/internal/database"
	"github.com/sunnyside/atlas/atlas-backend/internal/httpapi/dtos"
	"github.com/sunnyside/atlas/atlas-backend/internal/models"
	"github.com/sunnyside/atlas/atlas-backend/internal/repository"
	postgresrepo "github.com/sunnyside/atlas/atlas-backend/internal/repository/postgres"
	svc "github.com/sunnyside/atlas/atlas-backend/internal/services"
	"github.com/sunnyside/atlas/atlas-backend/internal/testutil"
)

func TestHealthz(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	NewRouter(Dependencies{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var body dtos.HealthResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if body.Service != serviceName {
		t.Fatalf("expected service %q, got %q", serviceName, body.Service)
	}

	if body.Status != "ok" {
		t.Fatalf("expected status ok, got %q", body.Status)
	}
}

func TestSendGimbalControlDispatchesTransientCommand(t *testing.T) {
	dispatcher := &recordingGimbalControlDispatcher{accepted: true}
	router := NewRouterWithGimbalControlDispatcher(Dependencies{}, nil, nil, dispatcher)
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/drones/drone-001/gimbal/control",
		bytes.NewReader([]byte(`{"pitchRateDegS":-25,"yawRateDegS":12.5}`)),
	)
	req.Header.Set("X-Atlas-Operator-ID", "operator-001")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status %d, got %d: %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}
	if !dispatcher.called {
		t.Fatal("expected gimbal control dispatcher to be called")
	}
	if dispatcher.command.DroneID != "drone-001" {
		t.Fatalf("expected drone-001, got %q", dispatcher.command.DroneID)
	}
	if dispatcher.command.PitchRateDegS != -25 {
		t.Fatalf("expected pitch rate -25, got %f", dispatcher.command.PitchRateDegS)
	}
	if dispatcher.command.YawRateDegS != 12.5 {
		t.Fatalf("expected yaw rate 12.5, got %f", dispatcher.command.YawRateDegS)
	}
	if dispatcher.command.RequestedBy != "operator-001" {
		t.Fatalf("expected operator-001 requester, got %q", dispatcher.command.RequestedBy)
	}
}

func TestSendGimbalControlRejectsOutOfRangeRateWithoutDispatch(t *testing.T) {
	dispatcher := &recordingGimbalControlDispatcher{accepted: true}
	router := NewRouterWithGimbalControlDispatcher(Dependencies{}, nil, nil, dispatcher)
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/drones/drone-001/gimbal/control",
		bytes.NewReader([]byte(`{"pitchRateDegS":91,"yawRateDegS":0}`)),
	)
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d: %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	if dispatcher.called {
		t.Fatal("expected invalid gimbal control request not to dispatch")
	}
}

func TestRegisterHeartbeatAndListDrones(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())

	registerBody := []byte(`{
		"vehicleAgentId": "agent-001",
		"droneId": "drone-001",
		"droneName": "Training Quad 1",
		"vehicleAgentVersion": "0.1.0"
	}`)
	registerReq := httptest.NewRequest(http.MethodPost, "/api/vehicle-agents/register", bytes.NewReader(registerBody))
	registerRec := httptest.NewRecorder()

	router.ServeHTTP(registerRec, registerReq)

	if registerRec.Code != http.StatusOK {
		t.Fatalf("expected register status %d, got %d: %s", http.StatusOK, registerRec.Code, registerRec.Body.String())
	}

	heartbeatBody := []byte(`{"vehicleAgentVersion": "0.1.0"}`)
	heartbeatReq := httptest.NewRequest(http.MethodPost, "/api/vehicle-agents/agent-001/heartbeat", bytes.NewReader(heartbeatBody))
	heartbeatRec := httptest.NewRecorder()

	router.ServeHTTP(heartbeatRec, heartbeatReq)

	if heartbeatRec.Code != http.StatusOK {
		t.Fatalf("expected heartbeat status %d, got %d: %s", http.StatusOK, heartbeatRec.Code, heartbeatRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/drones", nil)
	listRec := httptest.NewRecorder()

	router.ServeHTTP(listRec, listReq)

	if listRec.Code != http.StatusOK {
		t.Fatalf("expected list status %d, got %d: %s", http.StatusOK, listRec.Code, listRec.Body.String())
	}

	var drones []dtos.DroneResponse
	if err := json.NewDecoder(listRec.Body).Decode(&drones); err != nil {
		t.Fatalf("decode drones: %v", err)
	}

	if len(drones) != 1 {
		t.Fatalf("expected one drone, got %d", len(drones))
	}

	if drones[0].Status != "online" {
		t.Fatalf("expected online drone, got %q", drones[0].Status)
	}

	if drones[0].VehicleAgentID != "agent-001" {
		t.Fatalf("expected agent-001, got %q", drones[0].VehicleAgentID)
	}

	if drones[0].CommandChannel.State != string(models.CommandChannelDisconnected) {
		t.Fatalf("expected disconnected command channel, got %q", drones[0].CommandChannel.State)
	}
}

func TestPerceptionRoutesReturnEventsAndStatus(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())
	ctx := context.Background()
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)

	if _, err := repo.RegisterVehicleAgent(ctx, repository.RegisterVehicleAgentInput{
		VehicleAgentID:      "agent-001",
		DroneID:             "drone-001",
		DroneName:           "Training Quad 1",
		VehicleAgentVersion: "0.1.0",
	}, now); err != nil {
		t.Fatalf("register vehicle agent: %v", err)
	}
	if _, err := repo.RecordPerceptionEvent(ctx, "agent-001", models.PerceptionEvent{
		DroneID:            "drone-001",
		VideoSourceID:      "a8-main",
		ObservedAt:         now.Add(time.Second),
		FrameID:            "frame-000001",
		ModelName:          "yolov6n-hailo",
		ModelVersion:       "hef-mvp",
		InferenceLatencyMS: 19.25,
		Detections: []models.PerceptionDetection{
			{ClassName: "vehicle", Confidence: 0.87, BBox: [4]float64{0.2, 0.3, 0.25, 0.2}},
		},
	}, now.Add(2*time.Second)); err != nil {
		t.Fatalf("record perception event: %v", err)
	}
	if err := repo.RecordPerceptionHealth(ctx, "agent-001", models.PerceptionHealth{
		DroneID:          "drone-001",
		SourceID:         "a8-main",
		InputConnected:   true,
		OutputPublishing: true,
		ModelLoaded:      true,
		Accelerator:      "hailo",
		FPS:              24.5,
		LastFrameAt:      now.Add(3 * time.Second),
		LastDetectionAt:  now.Add(time.Second),
		ModelName:        "yolov6n-hailo",
		ModelVersion:     "hef-mvp",
	}, now.Add(3*time.Second)); err != nil {
		t.Fatalf("record perception health: %v", err)
	}

	eventsReq := httptest.NewRequest(http.MethodGet, "/api/drones/drone-001/perception/events?limit=5", nil)
	eventsRec := httptest.NewRecorder()
	router.ServeHTTP(eventsRec, eventsReq)
	if eventsRec.Code != http.StatusOK {
		t.Fatalf("expected perception events status %d, got %d: %s", http.StatusOK, eventsRec.Code, eventsRec.Body.String())
	}
	var events []dtos.PerceptionEventResponse
	if err := json.NewDecoder(eventsRec.Body).Decode(&events); err != nil {
		t.Fatalf("decode perception events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected one perception event, got %d", len(events))
	}
	if events[0].Detections[0].Class != "vehicle" {
		t.Fatalf("expected vehicle detection, got %#v", events[0].Detections)
	}

	statusReq := httptest.NewRequest(http.MethodGet, "/api/drones/drone-001/perception/status", nil)
	statusRec := httptest.NewRecorder()
	router.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("expected perception status %d, got %d: %s", http.StatusOK, statusRec.Code, statusRec.Body.String())
	}
	var status dtos.PerceptionStatusResponse
	if err := json.NewDecoder(statusRec.Body).Decode(&status); err != nil {
		t.Fatalf("decode perception status: %v", err)
	}
	if status.Accelerator != "hailo" {
		t.Fatalf("expected hailo accelerator, got %q", status.Accelerator)
	}
	if status.ActiveCounts["vehicle"] != 1 {
		t.Fatalf("expected one vehicle detection, got %#v", status.ActiveCounts)
	}
	if status.LatestEvent == nil || status.LatestEvent.FrameID != "frame-000001" {
		t.Fatalf("expected latest perception event, got %#v", status.LatestEvent)
	}
}

type recordingGimbalControlDispatcher struct {
	accepted bool
	called   bool
	command  models.GimbalControlCommand
}

func (d *recordingGimbalControlDispatcher) DispatchGimbalControl(_ context.Context, command models.GimbalControlCommand) bool {
	d.called = true
	d.command = command
	return d.accepted
}

func TestListDronesIncludesCommandChannelState(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())

	registerTestVehicleAgent(t, router)

	connectedAt := time.Date(2026, 6, 20, 15, 30, 0, 0, time.UTC)
	if _, err := repo.RecordCommandChannelConnected(context.Background(), "agent-001", connectedAt); err != nil {
		t.Fatalf("record command channel connected: %v", err)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/drones", nil)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)

	var drones []dtos.DroneResponse
	if err := json.NewDecoder(listRec.Body).Decode(&drones); err != nil {
		t.Fatalf("decode drones: %v", err)
	}

	if len(drones) != 1 {
		t.Fatalf("expected one drone, got %d", len(drones))
	}

	if drones[0].CommandChannel.State != string(models.CommandChannelConnected) {
		t.Fatalf("expected connected command channel, got %q", drones[0].CommandChannel.State)
	}

	if drones[0].CommandChannel.ConnectedAt != connectedAt.Format(time.RFC3339) {
		t.Fatalf("expected connectedAt %q, got %q", connectedAt.Format(time.RFC3339), drones[0].CommandChannel.ConnectedAt)
	}
}

func TestListDronesIncludesCommunicationSummary(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())

	registerTestVehicleAgent(t, router)
	link := openTestDroneVehicleAgentConnection(t, repo)

	listReq := httptest.NewRequest(http.MethodGet, "/api/drones", nil)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)

	if listRec.Code != http.StatusOK {
		t.Fatalf("expected list status %d, got %d: %s", http.StatusOK, listRec.Code, listRec.Body.String())
	}

	var drones []dtos.DroneResponse
	if err := json.NewDecoder(listRec.Body).Decode(&drones); err != nil {
		t.Fatalf("decode drones: %v", err)
	}

	if len(drones) != 1 {
		t.Fatalf("expected one drone, got %d", len(drones))
	}

	if drones[0].Communication.ActiveCommandLinkID != link.ID {
		t.Fatalf("expected active command link %q, got %q", link.ID, drones[0].Communication.ActiveCommandLinkID)
	}
	if drones[0].Communication.ActiveTelemetryLinkID != link.ID {
		t.Fatalf("expected active telemetry link %q, got %q", link.ID, drones[0].Communication.ActiveTelemetryLinkID)
	}
	if drones[0].Communication.CommandLinkStatus != string(models.CommunicationLinkStatusConnected) {
		t.Fatalf("expected connected command link, got %q", drones[0].Communication.CommandLinkStatus)
	}
	if drones[0].Communication.ActiveLinkCount != 1 {
		t.Fatalf("expected one active link, got %d", drones[0].Communication.ActiveLinkCount)
	}
}

func TestListDroneCommunicationLinks(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())

	registerTestVehicleAgent(t, router)
	link := openTestDroneVehicleAgentConnection(t, repo)

	req := httptest.NewRequest(http.MethodGet, "/api/drones/drone-001/communication-links", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected list status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var links []dtos.CommunicationLinkResponse
	if err := json.NewDecoder(rec.Body).Decode(&links); err != nil {
		t.Fatalf("decode communication links: %v", err)
	}

	if len(links) != 1 {
		t.Fatalf("expected one communication link, got %d", len(links))
	}
	if links[0].ID != link.ID {
		t.Fatalf("expected link %q, got %q", link.ID, links[0].ID)
	}
	if links[0].LinkType != string(models.CommunicationLinkVehicleAgentGRPC) {
		t.Fatalf("expected vehicle-agent gRPC link, got %q", links[0].LinkType)
	}
	if !links[0].CommandEligible {
		t.Fatal("expected command eligible communication link")
	}
	if !stringSliceContains(links[0].Roles, string(models.CommunicationLinkRoleCommand)) {
		t.Fatalf("expected command role, got %#v", links[0].Roles)
	}
}

func TestListDroneTelemetryFeeds(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())

	registerTestVehicleAgent(t, router)
	link := openTestDroneVehicleAgentConnection(t, repo)
	sendTestTelemetry(t, router)

	req := httptest.NewRequest(http.MethodGet, "/api/drones/drone-001/telemetry-feeds", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected list status %d, got %d: %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var feeds []dtos.TelemetryFeedResponse
	if err := json.NewDecoder(rec.Body).Decode(&feeds); err != nil {
		t.Fatalf("decode telemetry feeds: %v", err)
	}

	if len(feeds) != 1 {
		t.Fatalf("expected one telemetry feed, got %d", len(feeds))
	}
	if feeds[0].SourceType != string(models.TelemetrySourceAgentDirect) {
		t.Fatalf("expected agent-direct feed, got %q", feeds[0].SourceType)
	}
	if feeds[0].SourceID != "agent-001" {
		t.Fatalf("expected source agent-001, got %q", feeds[0].SourceID)
	}
	if feeds[0].CommunicationLinkID != link.ID {
		t.Fatalf("expected communication link %q, got %q", link.ID, feeds[0].CommunicationLinkID)
	}
	if !feeds[0].FieldsAvailable.Position {
		t.Fatal("expected position field to be available")
	}
}

func TestRecordTelemetryAndListDrones(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())

	registerBody := []byte(`{
		"vehicleAgentId": "agent-001",
		"droneId": "drone-001",
		"droneName": "Training Quad 1",
		"vehicleAgentVersion": "0.1.0"
	}`)
	registerReq := httptest.NewRequest(http.MethodPost, "/api/vehicle-agents/register", bytes.NewReader(registerBody))
	registerRec := httptest.NewRecorder()
	router.ServeHTTP(registerRec, registerReq)

	if registerRec.Code != http.StatusOK {
		t.Fatalf("expected register status %d, got %d: %s", http.StatusOK, registerRec.Code, registerRec.Body.String())
	}

	telemetryBody := []byte(`{
		"observedAt": "2026-06-20T15:30:00Z",
		"batteryPercent": 82,
		"relativeAltitudeM": 12.5,
		"flightMode": "HOLD",
		"armed": false,
		"inAir": false,
		"latitude": 51.5074,
		"longitude": -0.1278,
		"headingDeg": 91,
		"gpsFix": "3D",
		"satellitesVisible": 14,
		"homePositionSet": true,
		"source": "px4"
	}`)
	telemetryReq := httptest.NewRequest(http.MethodPost, "/api/vehicle-agents/agent-001/telemetry", bytes.NewReader(telemetryBody))
	telemetryRec := httptest.NewRecorder()
	router.ServeHTTP(telemetryRec, telemetryReq)

	if telemetryRec.Code != http.StatusOK {
		t.Fatalf("expected telemetry status %d, got %d: %s", http.StatusOK, telemetryRec.Code, telemetryRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/drones", nil)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)

	var drones []dtos.DroneResponse
	if err := json.NewDecoder(listRec.Body).Decode(&drones); err != nil {
		t.Fatalf("decode drones: %v", err)
	}

	if len(drones) != 1 {
		t.Fatalf("expected one drone, got %d", len(drones))
	}

	if drones[0].Telemetry == nil {
		t.Fatal("expected telemetry in drone response")
	}

	if drones[0].Telemetry.BatteryPercent != 82 {
		t.Fatalf("expected battery 82, got %f", drones[0].Telemetry.BatteryPercent)
	}
}

func TestListDronesIncludesRecentVehicleActions(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())

	registerTestVehicleAgent(t, router)
	sendTestHeartbeat(t, router)
	sendTestTelemetry(t, router)
	openTestDroneVehicleAgentConnection(t, repo)

	requestReq := httptest.NewRequest(http.MethodPost, "/api/drones/drone-001/actions/arm", nil)
	requestRec := httptest.NewRecorder()
	router.ServeHTTP(requestRec, requestReq)

	if requestRec.Code != http.StatusAccepted {
		t.Fatalf("expected request status %d, got %d: %s", http.StatusAccepted, requestRec.Code, requestRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/drones", nil)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)

	if listRec.Code != http.StatusOK {
		t.Fatalf("expected list status %d, got %d: %s", http.StatusOK, listRec.Code, listRec.Body.String())
	}

	var drones []dtos.DroneResponse
	if err := json.NewDecoder(listRec.Body).Decode(&drones); err != nil {
		t.Fatalf("decode drones: %v", err)
	}

	if len(drones) != 1 {
		t.Fatalf("expected one drone, got %d", len(drones))
	}

	if len(drones[0].VehicleActions) != 1 {
		t.Fatalf("expected one vehicle action in drone snapshot, got %d", len(drones[0].VehicleActions))
	}

	if drones[0].VehicleActions[0].Type != "arm" {
		t.Fatalf("expected arm vehicle action, got %q", drones[0].VehicleActions[0].Type)
	}
}

func TestDroneStreamSendsFleetSnapshotWithVehicleActions(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())

	registerTestVehicleAgent(t, router)
	sendTestHeartbeat(t, router)
	sendTestTelemetry(t, router)
	openTestDroneVehicleAgentConnection(t, repo)

	requestReq := httptest.NewRequest(http.MethodPost, "/api/drones/drone-001/actions/land", nil)
	requestRec := httptest.NewRecorder()
	router.ServeHTTP(requestRec, requestReq)

	if requestRec.Code != http.StatusAccepted {
		t.Fatalf("expected request status %d, got %d: %s", http.StatusAccepted, requestRec.Code, requestRec.Body.String())
	}

	server := httptest.NewServer(router)
	defer server.Close()

	streamURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/drones/stream"
	conn, _, err := websocket.DefaultDialer.Dial(streamURL, nil)
	if err != nil {
		t.Fatalf("dial drone stream: %v", err)
	}
	defer conn.Close()

	var drones []dtos.DroneResponse
	if err := conn.ReadJSON(&drones); err != nil {
		t.Fatalf("read drone stream snapshot: %v", err)
	}

	if len(drones) != 1 {
		t.Fatalf("expected one drone, got %d", len(drones))
	}

	if drones[0].ID != "drone-001" {
		t.Fatalf("expected drone-001, got %q", drones[0].ID)
	}

	if len(drones[0].VehicleActions) != 1 {
		t.Fatalf("expected one streamed vehicle action, got %d", len(drones[0].VehicleActions))
	}

	if drones[0].VehicleActions[0].Type != "land" {
		t.Fatalf("expected land vehicle action, got %q", drones[0].VehicleActions[0].Type)
	}
}

func TestRequestVehicleActionAuthorizedWithFreshTelemetry(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())

	registerTestVehicleAgent(t, router)
	sendTestHeartbeat(t, router)
	sendTestTelemetry(t, router)
	openTestDroneVehicleAgentConnection(t, repo)

	req := httptest.NewRequest(http.MethodPost, "/api/drones/drone-001/actions/arm", nil)
	req.Header.Set("X-Atlas-Operator-ID", "operator-001")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected vehicle action status %d, got %d: %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}

	var action dtos.VehicleActionResponse
	if err := json.NewDecoder(rec.Body).Decode(&action); err != nil {
		t.Fatalf("decode vehicle action: %v", err)
	}

	if action.Type != "arm" {
		t.Fatalf("expected arm vehicle action, got %q", action.Type)
	}

	if action.State != "authorized" {
		t.Fatalf("expected authorized vehicle action, got %q", action.State)
	}

	if action.RequestedBy != "operator-001" {
		t.Fatalf("expected operator-001, got %q", action.RequestedBy)
	}
}

func TestRequestVehicleActionRejectedWithoutCommunicationLink(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())

	registerTestVehicleAgent(t, router)
	sendTestHeartbeat(t, router)
	sendTestTelemetry(t, router)

	req := httptest.NewRequest(http.MethodPost, "/api/drones/drone-001/actions/arm", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected vehicle action status %d, got %d: %s", http.StatusConflict, rec.Code, rec.Body.String())
	}

	var action dtos.VehicleActionResponse
	if err := json.NewDecoder(rec.Body).Decode(&action); err != nil {
		t.Fatalf("decode vehicle action: %v", err)
	}

	if action.State != "rejected_by_policy" {
		t.Fatalf("expected rejected vehicle action, got %q", action.State)
	}
	if action.PolicyReason != "active drone vehicle agent connection is required" {
		t.Fatalf("expected communication policy reason, got %q", action.PolicyReason)
	}
}

func TestRequestVehicleActionRejectedWithoutFreshTelemetry(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())

	registerTestVehicleAgent(t, router)
	sendTestHeartbeat(t, router)
	openTestDroneVehicleAgentConnection(t, repo)

	req := httptest.NewRequest(http.MethodPost, "/api/drones/drone-001/actions/takeoff", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected vehicle action status %d, got %d: %s", http.StatusConflict, rec.Code, rec.Body.String())
	}

	var action dtos.VehicleActionResponse
	if err := json.NewDecoder(rec.Body).Decode(&action); err != nil {
		t.Fatalf("decode vehicle action: %v", err)
	}

	if action.State != "rejected_by_policy" {
		t.Fatalf("expected rejected vehicle action, got %q", action.State)
	}

	if action.PolicyReason != "telemetry must be fresh" {
		t.Fatalf("expected telemetry policy reason, got %q", action.PolicyReason)
	}
}

func TestListDroneVehicleActionsReturnsRecentVehicleActions(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())

	registerTestVehicleAgent(t, router)
	sendTestHeartbeat(t, router)
	sendTestTelemetry(t, router)
	openTestDroneVehicleAgentConnection(t, repo)

	requestReq := httptest.NewRequest(http.MethodPost, "/api/drones/drone-001/actions/arm", nil)
	requestRec := httptest.NewRecorder()
	router.ServeHTTP(requestRec, requestReq)

	if requestRec.Code != http.StatusAccepted {
		t.Fatalf("expected request status %d, got %d: %s", http.StatusAccepted, requestRec.Code, requestRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/drones/drone-001/actions?limit=5", nil)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)

	if listRec.Code != http.StatusOK {
		t.Fatalf("expected list status %d, got %d: %s", http.StatusOK, listRec.Code, listRec.Body.String())
	}

	var actions []dtos.VehicleActionResponse
	if err := json.NewDecoder(listRec.Body).Decode(&actions); err != nil {
		t.Fatalf("decode vehicle actions: %v", err)
	}

	if len(actions) != 1 {
		t.Fatalf("expected one vehicle action, got %d", len(actions))
	}

	if actions[0].Type != "arm" {
		t.Fatalf("expected arm vehicle action, got %q", actions[0].Type)
	}
}

func TestAgentVehicleActionPollingRouteIsNotRegistered(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())

	req := httptest.NewRequest(http.MethodGet, "/api/vehicle-agents/agent-001/actions/next", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected removed polling route to return %d, got %d: %s", http.StatusNotFound, rec.Code, rec.Body.String())
	}
}

func TestCreateMissionValidatedWithFreshTelemetry(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())

	registerTestVehicleAgent(t, router)
	sendTestHeartbeat(t, router)
	sendTestTelemetry(t, router)

	body := []byte(`{
		"name": "Training loop",
		"completionAction": "land",
		"waypoints": [
			{"latitude": 51.5074, "longitude": -0.1278, "relativeAltitudeM": 30, "speedMPS": 6.5, "loiterTimeS": 12},
			{"latitude": 51.5078, "longitude": -0.1282, "relativeAltitudeM": 35}
		]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/drones/drone-001/missions", bytes.NewReader(body))
	req.Header.Set("X-Atlas-Operator-ID", "operator-001")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected mission status %d, got %d: %s", http.StatusCreated, rec.Code, rec.Body.String())
	}

	var mission dtos.MissionResponse
	if err := json.NewDecoder(rec.Body).Decode(&mission); err != nil {
		t.Fatalf("decode mission: %v", err)
	}

	if mission.ValidationStatus != "validated" {
		t.Fatalf("expected validated mission, got %q", mission.ValidationStatus)
	}

	if mission.CurrentVersionID == "" {
		t.Fatal("expected current version id")
	}

	if mission.CreatedBy != "operator-001" {
		t.Fatalf("expected operator-001, got %q", mission.CreatedBy)
	}

	if mission.CompletionAction != "land" {
		t.Fatalf("expected land completion action, got %q", mission.CompletionAction)
	}

	if len(mission.Waypoints) != 2 {
		t.Fatalf("expected two waypoints, got %d", len(mission.Waypoints))
	}

	if mission.Waypoints[0].LoiterTimeS == nil || *mission.Waypoints[0].LoiterTimeS != 12 {
		t.Fatalf("expected waypoint loiter time 12, got %v", mission.Waypoints[0].LoiterTimeS)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/drones/drone-001/missions", nil)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)

	if listRec.Code != http.StatusOK {
		t.Fatalf("expected list status %d, got %d: %s", http.StatusOK, listRec.Code, listRec.Body.String())
	}

	var missions []dtos.MissionResponse
	if err := json.NewDecoder(listRec.Body).Decode(&missions); err != nil {
		t.Fatalf("decode missions: %v", err)
	}

	if len(missions) != 1 {
		t.Fatalf("expected one mission, got %d", len(missions))
	}

	if missions[0].CompletionAction != "land" {
		t.Fatalf("expected listed mission completion action land, got %q", missions[0].CompletionAction)
	}

	if missions[0].CurrentVersionID != mission.CurrentVersionID {
		t.Fatalf("expected listed current version %q, got %q", mission.CurrentVersionID, missions[0].CurrentVersionID)
	}
}

func TestCreateMissionRejectedWithValidationErrors(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())

	registerTestVehicleAgent(t, router)
	sendTestHeartbeat(t, router)

	body := []byte(`{
		"name": "",
		"waypoints": [
			{"latitude": 91, "longitude": -0.1278, "relativeAltitudeM": 0}
		]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/drones/drone-001/missions", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected mission status %d, got %d: %s", http.StatusConflict, rec.Code, rec.Body.String())
	}

	var mission dtos.MissionResponse
	if err := json.NewDecoder(rec.Body).Decode(&mission); err != nil {
		t.Fatalf("decode mission: %v", err)
	}

	if mission.ValidationStatus != "rejected" {
		t.Fatalf("expected rejected mission, got %q", mission.ValidationStatus)
	}

	if len(mission.ValidationErrors) == 0 {
		t.Fatal("expected validation errors")
	}

	if mission.ValidationErrors[0].Field == "" || mission.ValidationErrors[0].Message == "" {
		t.Fatalf("expected structured validation error, got %#v", mission.ValidationErrors[0])
	}
}

func TestRequestMissionUploadCreatesExecution(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())
	mission := createValidatedMissionViaAPI(t, router)

	req := httptest.NewRequest(http.MethodPost, "/api/missions/"+mission.ID+"/upload", nil)
	req.Header.Set("X-Atlas-Operator-ID", "operator-001")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected upload status %d, got %d: %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}

	var execution dtos.MissionExecutionResponse
	if err := json.NewDecoder(rec.Body).Decode(&execution); err != nil {
		t.Fatalf("decode execution: %v", err)
	}

	if execution.MissionID != mission.ID {
		t.Fatalf("expected mission %q, got %q", mission.ID, execution.MissionID)
	}

	if execution.MissionVersionID != mission.CurrentVersionID {
		t.Fatalf("expected mission version %q, got %q", mission.CurrentVersionID, execution.MissionVersionID)
	}

	if execution.State != "upload_requested" {
		t.Fatalf("expected upload_requested, got %q", execution.State)
	}

	if execution.RequestedBy != "operator-001" {
		t.Fatalf("expected operator-001, got %q", execution.RequestedBy)
	}

	if execution.UploadRequestedBy != "operator-001" {
		t.Fatalf("expected upload requester operator-001, got %q", execution.UploadRequestedBy)
	}

	detailReq := httptest.NewRequest(http.MethodGet, "/api/missions/"+mission.ID, nil)
	detailRec := httptest.NewRecorder()
	router.ServeHTTP(detailRec, detailReq)

	if detailRec.Code != http.StatusOK {
		t.Fatalf("expected mission detail status %d, got %d: %s", http.StatusOK, detailRec.Code, detailRec.Body.String())
	}

	var detail dtos.MissionDetailResponse
	if err := json.NewDecoder(detailRec.Body).Decode(&detail); err != nil {
		t.Fatalf("decode mission detail: %v", err)
	}

	if detail.Mission.ID != mission.ID {
		t.Fatalf("expected mission %q, got %q", mission.ID, detail.Mission.ID)
	}

	if len(detail.Executions) != 1 || detail.Executions[0].ID != execution.ID {
		t.Fatalf("expected mission execution %q in detail, got %#v", execution.ID, detail.Executions)
	}

	if detail.Executions[0].MissionVersionID != mission.CurrentVersionID {
		t.Fatalf("expected detail execution mission version %q, got %q", mission.CurrentVersionID, detail.Executions[0].MissionVersionID)
	}

	executionsReq := httptest.NewRequest(http.MethodGet, "/api/missions/"+mission.ID+"/executions", nil)
	executionsRec := httptest.NewRecorder()
	router.ServeHTTP(executionsRec, executionsReq)

	if executionsRec.Code != http.StatusOK {
		t.Fatalf("expected mission executions status %d, got %d: %s", http.StatusOK, executionsRec.Code, executionsRec.Body.String())
	}

	var executions []dtos.MissionExecutionResponse
	if err := json.NewDecoder(executionsRec.Body).Decode(&executions); err != nil {
		t.Fatalf("decode mission executions: %v", err)
	}

	if len(executions) != 1 || executions[0].ID != execution.ID {
		t.Fatalf("expected mission execution %q, got %#v", execution.ID, executions)
	}

	if executions[0].MissionVersionID != mission.CurrentVersionID {
		t.Fatalf("expected listed execution mission version %q, got %q", mission.CurrentVersionID, executions[0].MissionVersionID)
	}
}

func TestRequestMissionUploadRejectsRejectedMission(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())

	registerTestVehicleAgent(t, router)
	sendTestHeartbeat(t, router)

	body := []byte(`{"name": "", "waypoints": []}`)
	createReq := httptest.NewRequest(http.MethodPost, "/api/drones/drone-001/missions", bytes.NewReader(body))
	createRec := httptest.NewRecorder()
	router.ServeHTTP(createRec, createReq)

	if createRec.Code != http.StatusConflict {
		t.Fatalf("expected create conflict, got %d: %s", createRec.Code, createRec.Body.String())
	}

	var mission dtos.MissionResponse
	if err := json.NewDecoder(createRec.Body).Decode(&mission); err != nil {
		t.Fatalf("decode mission: %v", err)
	}

	uploadReq := httptest.NewRequest(http.MethodPost, "/api/missions/"+mission.ID+"/upload", nil)
	uploadRec := httptest.NewRecorder()
	router.ServeHTTP(uploadRec, uploadReq)

	if uploadRec.Code != http.StatusConflict {
		t.Fatalf("expected upload conflict, got %d: %s", uploadRec.Code, uploadRec.Body.String())
	}
}

func TestRequestMissionStartRequiresUploadedExecution(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())
	mission := createValidatedMissionViaAPI(t, router)

	uploadReq := httptest.NewRequest(http.MethodPost, "/api/missions/"+mission.ID+"/upload", nil)
	uploadRec := httptest.NewRecorder()
	router.ServeHTTP(uploadRec, uploadReq)

	if uploadRec.Code != http.StatusAccepted {
		t.Fatalf("expected upload accepted, got %d: %s", uploadRec.Code, uploadRec.Body.String())
	}

	startReq := httptest.NewRequest(http.MethodPost, "/api/missions/"+mission.ID+"/start", nil)
	startRec := httptest.NewRecorder()
	router.ServeHTTP(startRec, startReq)

	if startRec.Code != http.StatusConflict {
		t.Fatalf("expected start conflict, got %d: %s", startRec.Code, startRec.Body.String())
	}
}

func TestRequestMissionStartAcceptsGroundedVehicleForLaunchWorkflow(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())
	mission := createValidatedMissionViaAPI(t, router)

	uploadReq := httptest.NewRequest(http.MethodPost, "/api/missions/"+mission.ID+"/upload", nil)
	uploadRec := httptest.NewRecorder()
	router.ServeHTTP(uploadRec, uploadReq)

	if uploadRec.Code != http.StatusAccepted {
		t.Fatalf("expected upload accepted, got %d: %s", uploadRec.Code, uploadRec.Body.String())
	}

	var uploaded dtos.MissionExecutionResponse
	if err := json.NewDecoder(uploadRec.Body).Decode(&uploaded); err != nil {
		t.Fatalf("decode upload execution: %v", err)
	}

	if _, err := repo.RecordMissionExecutionUploaded(context.Background(), uploaded.ID, "uploaded to vehicle", time.Now().UTC()); err != nil {
		t.Fatalf("record uploaded execution: %v", err)
	}

	startReq := httptest.NewRequest(http.MethodPost, "/api/missions/"+mission.ID+"/start", nil)
	startRec := httptest.NewRecorder()
	router.ServeHTTP(startRec, startReq)

	if startRec.Code != http.StatusAccepted {
		t.Fatalf("expected start accepted, got %d: %s", startRec.Code, startRec.Body.String())
	}

	var body dtos.MissionExecutionResponse
	if err := json.NewDecoder(startRec.Body).Decode(&body); err != nil {
		t.Fatalf("decode start execution: %v", err)
	}

	if body.State != string(models.MissionExecutionStateStartRequested) {
		t.Fatalf("expected start_requested, got %q", body.State)
	}
}

func TestRequestMissionStartMovesUploadedExecutionToStartRequested(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())
	mission := createValidatedMissionViaAPI(t, router)

	uploadReq := httptest.NewRequest(http.MethodPost, "/api/missions/"+mission.ID+"/upload", nil)
	uploadRec := httptest.NewRecorder()
	router.ServeHTTP(uploadRec, uploadReq)

	if uploadRec.Code != http.StatusAccepted {
		t.Fatalf("expected upload accepted, got %d: %s", uploadRec.Code, uploadRec.Body.String())
	}

	var uploaded dtos.MissionExecutionResponse
	if err := json.NewDecoder(uploadRec.Body).Decode(&uploaded); err != nil {
		t.Fatalf("decode upload execution: %v", err)
	}

	if _, err := repo.RecordMissionExecutionUploaded(context.Background(), uploaded.ID, "uploaded to vehicle", time.Now().UTC()); err != nil {
		t.Fatalf("record uploaded execution: %v", err)
	}
	recordAirborneTelemetry(t, repo)

	startReq := httptest.NewRequest(http.MethodPost, "/api/missions/"+mission.ID+"/start", nil)
	startReq.Header.Set("X-Atlas-Operator-ID", "start-operator")
	startRec := httptest.NewRecorder()
	router.ServeHTTP(startRec, startReq)

	if startRec.Code != http.StatusAccepted {
		t.Fatalf("expected start accepted, got %d: %s", startRec.Code, startRec.Body.String())
	}

	var started dtos.MissionExecutionResponse
	if err := json.NewDecoder(startRec.Body).Decode(&started); err != nil {
		t.Fatalf("decode started execution: %v", err)
	}

	if started.ID != uploaded.ID {
		t.Fatalf("expected execution %q, got %q", uploaded.ID, started.ID)
	}

	if started.State != "start_requested" {
		t.Fatalf("expected start_requested, got %q", started.State)
	}

	if started.RequestedBy != "start-operator" {
		t.Fatalf("expected start-operator, got %q", started.RequestedBy)
	}

	if started.UploadRequestedBy == "" {
		t.Fatal("expected upload requester to be preserved")
	}

	if started.StartRequestedBy != "start-operator" {
		t.Fatalf("expected start requester start-operator, got %q", started.StartRequestedBy)
	}

	dronesReq := httptest.NewRequest(http.MethodGet, "/api/drones", nil)
	dronesRec := httptest.NewRecorder()
	router.ServeHTTP(dronesRec, dronesReq)

	if dronesRec.Code != http.StatusOK {
		t.Fatalf("expected drones status %d, got %d: %s", http.StatusOK, dronesRec.Code, dronesRec.Body.String())
	}

	var drones []dtos.DroneResponse
	if err := json.NewDecoder(dronesRec.Body).Decode(&drones); err != nil {
		t.Fatalf("decode drones: %v", err)
	}

	if len(drones) != 1 || drones[0].MissionExecution == nil {
		t.Fatalf("expected drone mission execution in snapshot, got %#v", drones)
	}

	if drones[0].MissionExecution.ID != started.ID {
		t.Fatalf("expected mission execution %q, got %q", started.ID, drones[0].MissionExecution.ID)
	}
}

func TestRequestMissionAbortMovesActiveExecutionToRTLRequested(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())
	mission := createValidatedMissionViaAPI(t, router)

	uploadReq := httptest.NewRequest(http.MethodPost, "/api/missions/"+mission.ID+"/upload", nil)
	uploadRec := httptest.NewRecorder()
	router.ServeHTTP(uploadRec, uploadReq)

	if uploadRec.Code != http.StatusAccepted {
		t.Fatalf("expected upload accepted, got %d: %s", uploadRec.Code, uploadRec.Body.String())
	}

	var uploaded dtos.MissionExecutionResponse
	if err := json.NewDecoder(uploadRec.Body).Decode(&uploaded); err != nil {
		t.Fatalf("decode upload execution: %v", err)
	}

	if _, err := repo.RecordMissionExecutionUploaded(context.Background(), uploaded.ID, "uploaded to vehicle", time.Now().UTC()); err != nil {
		t.Fatalf("record uploaded execution: %v", err)
	}
	recordAirborneTelemetry(t, repo)

	startReq := httptest.NewRequest(http.MethodPost, "/api/missions/"+mission.ID+"/start", nil)
	startRec := httptest.NewRecorder()
	router.ServeHTTP(startRec, startReq)

	if startRec.Code != http.StatusAccepted {
		t.Fatalf("expected start accepted, got %d: %s", startRec.Code, startRec.Body.String())
	}

	if _, err := repo.UpdateMissionExecutionStatus(context.Background(), repository.UpdateMissionExecutionStatusInput{
		VehicleAgentID: "agent-001",
		ExecutionID:    uploaded.ID,
		State:          models.MissionExecutionStateActive,
		ResultMessage:  "mission started",
	}, time.Now().UTC()); err != nil {
		t.Fatalf("mark mission active: %v", err)
	}

	abortReq := httptest.NewRequest(http.MethodPost, "/api/missions/"+mission.ID+"/abort", nil)
	abortReq.Header.Set("X-Atlas-Operator-ID", "safety-operator")
	abortRec := httptest.NewRecorder()
	router.ServeHTTP(abortRec, abortReq)

	if abortRec.Code != http.StatusAccepted {
		t.Fatalf("expected abort accepted, got %d: %s", abortRec.Code, abortRec.Body.String())
	}

	var aborted dtos.MissionExecutionResponse
	if err := json.NewDecoder(abortRec.Body).Decode(&aborted); err != nil {
		t.Fatalf("decode abort execution: %v", err)
	}

	if aborted.ID != uploaded.ID {
		t.Fatalf("expected abort on execution %q, got %q", uploaded.ID, aborted.ID)
	}

	if aborted.State != string(models.MissionExecutionStateRTLRequested) {
		t.Fatalf("expected rtl_requested, got %q", aborted.State)
	}

	if aborted.RequestedBy != "safety-operator" {
		t.Fatalf("expected safety-operator, got %q", aborted.RequestedBy)
	}
}

func TestListMissionExecutionEvents(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())
	mission := createValidatedMissionViaAPI(t, router)

	uploadReq := httptest.NewRequest(http.MethodPost, "/api/missions/"+mission.ID+"/upload", nil)
	uploadRec := httptest.NewRecorder()
	router.ServeHTTP(uploadRec, uploadReq)

	if uploadRec.Code != http.StatusAccepted {
		t.Fatalf("expected upload accepted, got %d: %s", uploadRec.Code, uploadRec.Body.String())
	}

	eventsReq := httptest.NewRequest(http.MethodGet, "/api/missions/"+mission.ID+"/events", nil)
	eventsRec := httptest.NewRecorder()
	router.ServeHTTP(eventsRec, eventsReq)

	if eventsRec.Code != http.StatusOK {
		t.Fatalf("expected events status %d, got %d: %s", http.StatusOK, eventsRec.Code, eventsRec.Body.String())
	}

	var events []dtos.MissionExecutionEventResponse
	if err := json.NewDecoder(eventsRec.Body).Decode(&events); err != nil {
		t.Fatalf("decode events: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("expected one mission event, got %#v", events)
	}

	if events[0].Type != "upload_requested" || events[0].Source != "backend" {
		t.Fatalf("expected backend upload event, got %#v", events[0])
	}
}

func TestAgentReportsVehicleActionStatus(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())

	registerTestVehicleAgent(t, router)
	sendTestHeartbeat(t, router)
	sendTestTelemetry(t, router)
	openTestDroneVehicleAgentConnection(t, repo)

	requestReq := httptest.NewRequest(http.MethodPost, "/api/drones/drone-001/actions/land", nil)
	requestRec := httptest.NewRecorder()
	router.ServeHTTP(requestRec, requestReq)

	var requested dtos.VehicleActionResponse
	if err := json.NewDecoder(requestRec.Body).Decode(&requested); err != nil {
		t.Fatalf("decode requested vehicle action: %v", err)
	}

	if _, err := repo.dependencies().VehicleActions.ClaimVehicleActionForVehicleAgent(context.Background(), "agent-001", requested.ID, time.Now().UTC()); err != nil {
		t.Fatalf("claim vehicle action for status report setup: %v", err)
	}

	body, err := json.Marshal(dtos.VehicleActionStatusRequest{
		State:            "vehicle_acked",
		ResultMessage:    "accepted by vehicle",
		AckCorrelationID: requested.AckCorrelationID,
	})
	if err != nil {
		t.Fatalf("encode status request: %v", err)
	}
	statusReq := httptest.NewRequest(http.MethodPost, "/api/vehicle-agents/agent-001/actions/"+requested.ID+"/status", bytes.NewReader(body))
	statusRec := httptest.NewRecorder()
	router.ServeHTTP(statusRec, statusReq)

	if statusRec.Code != http.StatusOK {
		t.Fatalf("expected status update %d, got %d: %s", http.StatusOK, statusRec.Code, statusRec.Body.String())
	}

	var updated dtos.VehicleActionResponse
	if err := json.NewDecoder(statusRec.Body).Decode(&updated); err != nil {
		t.Fatalf("decode updated vehicle action: %v", err)
	}

	if updated.State != "vehicle_acked" {
		t.Fatalf("expected vehicle_acked, got %q", updated.State)
	}

	if updated.ResultMessage != "accepted by vehicle" {
		t.Fatalf("expected result message, got %q", updated.ResultMessage)
	}
}

func TestAgentCannotReportVehicleActionResultBeforeFetching(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())

	registerTestVehicleAgent(t, router)
	sendTestHeartbeat(t, router)
	sendTestTelemetry(t, router)
	openTestDroneVehicleAgentConnection(t, repo)

	requestReq := httptest.NewRequest(http.MethodPost, "/api/drones/drone-001/actions/land", nil)
	requestRec := httptest.NewRecorder()
	router.ServeHTTP(requestRec, requestReq)

	var requested dtos.VehicleActionResponse
	if err := json.NewDecoder(requestRec.Body).Decode(&requested); err != nil {
		t.Fatalf("decode requested vehicle action: %v", err)
	}

	body := []byte(`{"state":"vehicle_acked"}`)
	statusReq := httptest.NewRequest(http.MethodPost, "/api/vehicle-agents/agent-001/actions/"+requested.ID+"/status", bytes.NewReader(body))
	statusRec := httptest.NewRecorder()
	router.ServeHTTP(statusRec, statusReq)

	if statusRec.Code != http.StatusConflict {
		t.Fatalf("expected status update %d, got %d: %s", http.StatusConflict, statusRec.Code, statusRec.Body.String())
	}
}

func TestAgentCannotReportBackendOwnedVehicleActionState(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())

	registerTestVehicleAgent(t, router)
	sendTestHeartbeat(t, router)
	sendTestTelemetry(t, router)
	openTestDroneVehicleAgentConnection(t, repo)

	requestReq := httptest.NewRequest(http.MethodPost, "/api/drones/drone-001/actions/land", nil)
	requestRec := httptest.NewRecorder()
	router.ServeHTTP(requestRec, requestReq)

	var requested dtos.VehicleActionResponse
	if err := json.NewDecoder(requestRec.Body).Decode(&requested); err != nil {
		t.Fatalf("decode requested vehicle action: %v", err)
	}

	body := []byte(`{"state":"authorized"}`)
	statusReq := httptest.NewRequest(http.MethodPost, "/api/vehicle-agents/agent-001/actions/"+requested.ID+"/status", bytes.NewReader(body))
	statusRec := httptest.NewRecorder()
	router.ServeHTTP(statusRec, statusReq)

	if statusRec.Code != http.StatusBadRequest {
		t.Fatalf("expected status update %d, got %d: %s", http.StatusBadRequest, statusRec.Code, statusRec.Body.String())
	}
}

func createValidatedMissionViaAPI(t *testing.T, router http.Handler) dtos.MissionResponse {
	t.Helper()

	registerTestVehicleAgent(t, router)
	sendTestHeartbeat(t, router)
	sendTestTelemetry(t, router)

	body := []byte(`{
		"name": "Training loop",
		"waypoints": [
			{"latitude": 51.5074, "longitude": -0.1278, "relativeAltitudeM": 30}
		]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/api/drones/drone-001/missions", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected mission status %d, got %d: %s", http.StatusCreated, rec.Code, rec.Body.String())
	}

	var mission dtos.MissionResponse
	if err := json.NewDecoder(rec.Body).Decode(&mission); err != nil {
		t.Fatalf("decode mission: %v", err)
	}

	if mission.ValidationStatus != "validated" {
		t.Fatalf("expected validated mission, got %q", mission.ValidationStatus)
	}

	if mission.CurrentVersionID == "" {
		t.Fatal("expected current version id")
	}

	return mission
}

func registerTestVehicleAgent(t *testing.T, router http.Handler) {
	t.Helper()

	registerBody := []byte(`{
		"vehicleAgentId": "agent-001",
		"droneId": "drone-001",
		"droneName": "Training Quad 1",
		"vehicleAgentVersion": "0.1.0"
	}`)
	registerReq := httptest.NewRequest(http.MethodPost, "/api/vehicle-agents/register", bytes.NewReader(registerBody))
	registerRec := httptest.NewRecorder()
	router.ServeHTTP(registerRec, registerReq)

	if registerRec.Code != http.StatusOK {
		t.Fatalf("expected register status %d, got %d: %s", http.StatusOK, registerRec.Code, registerRec.Body.String())
	}
}

func sendTestHeartbeat(t *testing.T, router http.Handler) {
	t.Helper()

	heartbeatBody := []byte(`{"vehicleAgentVersion": "0.1.0"}`)
	heartbeatReq := httptest.NewRequest(http.MethodPost, "/api/vehicle-agents/agent-001/heartbeat", bytes.NewReader(heartbeatBody))
	heartbeatRec := httptest.NewRecorder()
	router.ServeHTTP(heartbeatRec, heartbeatReq)

	if heartbeatRec.Code != http.StatusOK {
		t.Fatalf("expected heartbeat status %d, got %d: %s", http.StatusOK, heartbeatRec.Code, heartbeatRec.Body.String())
	}
}

func sendTestTelemetry(t *testing.T, router http.Handler) {
	t.Helper()

	telemetryBody := []byte(`{
		"observedAt": "2026-06-20T15:30:00Z",
		"batteryPercent": 82,
		"relativeAltitudeM": 12.5,
		"flightMode": "HOLD",
		"armed": false,
		"inAir": false,
		"latitude": 51.5074,
		"longitude": -0.1278,
		"headingDeg": 91,
		"gpsFix": "3D",
		"satellitesVisible": 14,
		"homePositionSet": true,
		"source": "px4"
	}`)
	telemetryReq := httptest.NewRequest(http.MethodPost, "/api/vehicle-agents/agent-001/telemetry", bytes.NewReader(telemetryBody))
	telemetryRec := httptest.NewRecorder()
	router.ServeHTTP(telemetryRec, telemetryReq)

	if telemetryRec.Code != http.StatusOK {
		t.Fatalf("expected telemetry status %d, got %d: %s", http.StatusOK, telemetryRec.Code, telemetryRec.Body.String())
	}
}

func openTestDroneVehicleAgentConnection(t *testing.T, repo *testRepository) models.CommunicationLink {
	t.Helper()

	_, link, err := repo.VehicleAgentConnectionService.OpenDroneVehicleAgentConnection(context.Background(), repository.OpenDroneVehicleAgentConnectionInput{
		VehicleAgentID:      "agent-001",
		DroneID:             "drone-001",
		VehicleAgentVersion: "0.1.0",
		RemoteAddress:       "127.0.0.1:50051",
	}, time.Now().UTC())
	if err != nil {
		t.Fatalf("open test drone vehicle agent connection: %v", err)
	}

	return link
}

func stringSliceContains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

type telemetryWorkflow interface {
	RecordTelemetry(context.Context, models.TelemetrySnapshot, time.Time) (models.TelemetrySnapshot, error)
}

func recordAirborneTelemetry(t *testing.T, repo telemetryWorkflow) {
	t.Helper()

	now := time.Now().UTC()
	if _, err := repo.RecordTelemetry(context.Background(), models.TelemetrySnapshot{
		VehicleAgentID:    "agent-001",
		ObservedAt:        now,
		BatteryPercent:    82,
		RelativeAltitudeM: 12.5,
		FlightMode:        "TAKEOFF",
		Armed:             true,
		InAir:             true,
		Latitude:          51.5074,
		Longitude:         -0.1278,
		HeadingDeg:        91,
		GPSFix:            "3D",
		SatellitesVisible: 14,
		HomePositionSet:   true,
		Source:            "px4",
	}, now); err != nil {
		t.Fatalf("record airborne telemetry: %v", err)
	}
}

type testRepository struct {
	*svc.VehicleAgentService
	*svc.VehicleAgentConnectionService
	*svc.TelemetryService
	*svc.VehicleActionService
	*svc.MissionService
	*svc.PerceptionService
	repos repository.Repositories
	deps  Dependencies
}

func (r *testRepository) dependencies() Dependencies {
	return r.deps
}

func (r *testRepository) ListDrones(ctx context.Context, now time.Time) []repository.DroneSnapshot {
	return r.repos.Drones.ListDrones(ctx, now)
}

func (r *testRepository) GetTelemetryForDrone(ctx context.Context, droneID string) (models.TelemetrySnapshot, bool) {
	return r.repos.Telemetry.GetTelemetryForDrone(ctx, droneID)
}

func newTestRepository(t *testing.T) *testRepository {
	t.Helper()

	db, err := database.OpenPostgres(context.Background(), testutil.DatabaseURL(t))
	if err != nil {
		t.Fatalf("open postgres test db: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Fatalf("close postgres test db: %v", err)
		}
	})

	txManager := postgresrepo.NewTxManager(db)
	repos := txManager.Repositories()
	appServices := svc.New(svc.Dependencies{
		TxManager:    txManager,
		Repositories: repos,
	})

	return &testRepository{
		VehicleAgentService:           appServices.VehicleAgents,
		VehicleAgentConnectionService: appServices.VehicleAgentConnections,
		TelemetryService:              appServices.Telemetry,
		VehicleActionService:          appServices.VehicleActions,
		MissionService:                appServices.Missions,
		PerceptionService:             appServices.Perception,
		repos:                         repos,
		deps: Dependencies{
			VehicleAgents:  appServices.VehicleAgents,
			Telemetry:      appServices.Telemetry,
			VehicleActions: appServices.VehicleActions,
			Missions:       appServices.Missions,
			Fleet:          appServices.Fleet,
			Perception:     appServices.Perception,
		},
	}
}
