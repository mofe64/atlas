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

func TestListDronesIncludesRecentCommands(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())

	registerTestVehicleAgent(t, router)
	sendTestHeartbeat(t, router)
	sendTestTelemetry(t, router)

	requestReq := httptest.NewRequest(http.MethodPost, "/api/drones/drone-001/commands/arm", nil)
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

	if len(drones[0].Commands) != 1 {
		t.Fatalf("expected one command in drone snapshot, got %d", len(drones[0].Commands))
	}

	if drones[0].Commands[0].Type != "arm" {
		t.Fatalf("expected arm command, got %q", drones[0].Commands[0].Type)
	}
}

func TestDroneStreamSendsFleetSnapshotWithCommands(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())

	registerTestVehicleAgent(t, router)
	sendTestHeartbeat(t, router)
	sendTestTelemetry(t, router)

	requestReq := httptest.NewRequest(http.MethodPost, "/api/drones/drone-001/commands/land", nil)
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

	if len(drones[0].Commands) != 1 {
		t.Fatalf("expected one streamed command, got %d", len(drones[0].Commands))
	}

	if drones[0].Commands[0].Type != "land" {
		t.Fatalf("expected land command, got %q", drones[0].Commands[0].Type)
	}
}

func TestRequestCommandAuthorizedWithFreshTelemetry(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())

	registerTestVehicleAgent(t, router)
	sendTestHeartbeat(t, router)
	sendTestTelemetry(t, router)

	req := httptest.NewRequest(http.MethodPost, "/api/drones/drone-001/commands/arm", nil)
	req.Header.Set("X-Atlas-Operator-ID", "operator-001")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected command status %d, got %d: %s", http.StatusAccepted, rec.Code, rec.Body.String())
	}

	var command dtos.CommandResponse
	if err := json.NewDecoder(rec.Body).Decode(&command); err != nil {
		t.Fatalf("decode command: %v", err)
	}

	if command.Type != "arm" {
		t.Fatalf("expected arm command, got %q", command.Type)
	}

	if command.State != "authorized" {
		t.Fatalf("expected authorized command, got %q", command.State)
	}

	if command.RequestedBy != "operator-001" {
		t.Fatalf("expected operator-001, got %q", command.RequestedBy)
	}
}

func TestRequestCommandRejectedWithoutFreshTelemetry(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())

	registerTestVehicleAgent(t, router)
	sendTestHeartbeat(t, router)

	req := httptest.NewRequest(http.MethodPost, "/api/drones/drone-001/commands/takeoff", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("expected command status %d, got %d: %s", http.StatusConflict, rec.Code, rec.Body.String())
	}

	var command dtos.CommandResponse
	if err := json.NewDecoder(rec.Body).Decode(&command); err != nil {
		t.Fatalf("decode command: %v", err)
	}

	if command.State != "rejected_by_policy" {
		t.Fatalf("expected rejected command, got %q", command.State)
	}

	if command.PolicyReason != "telemetry must be fresh" {
		t.Fatalf("expected telemetry policy reason, got %q", command.PolicyReason)
	}
}

func TestAgentFetchesNextAuthorizedCommand(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())

	registerTestVehicleAgent(t, router)
	sendTestHeartbeat(t, router)
	sendTestTelemetry(t, router)

	requestReq := httptest.NewRequest(http.MethodPost, "/api/drones/drone-001/commands/arm", nil)
	requestRec := httptest.NewRecorder()
	router.ServeHTTP(requestRec, requestReq)

	if requestRec.Code != http.StatusAccepted {
		t.Fatalf("expected request status %d, got %d: %s", http.StatusAccepted, requestRec.Code, requestRec.Body.String())
	}

	fetchReq := httptest.NewRequest(http.MethodGet, "/api/vehicle-agents/agent-001/commands/next", nil)
	fetchRec := httptest.NewRecorder()
	router.ServeHTTP(fetchRec, fetchReq)

	if fetchRec.Code != http.StatusOK {
		t.Fatalf("expected fetch status %d, got %d: %s", http.StatusOK, fetchRec.Code, fetchRec.Body.String())
	}

	var command dtos.CommandResponse
	if err := json.NewDecoder(fetchRec.Body).Decode(&command); err != nil {
		t.Fatalf("decode command: %v", err)
	}

	if command.Type != "arm" {
		t.Fatalf("expected arm command, got %q", command.Type)
	}

	if command.State != "sent_to_vehicle_agent" {
		t.Fatalf("expected sent_to_vehicle_agent, got %q", command.State)
	}
}

func TestListDroneCommandsReturnsRecentCommands(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())

	registerTestVehicleAgent(t, router)
	sendTestHeartbeat(t, router)
	sendTestTelemetry(t, router)

	requestReq := httptest.NewRequest(http.MethodPost, "/api/drones/drone-001/commands/arm", nil)
	requestRec := httptest.NewRecorder()
	router.ServeHTTP(requestRec, requestReq)

	if requestRec.Code != http.StatusAccepted {
		t.Fatalf("expected request status %d, got %d: %s", http.StatusAccepted, requestRec.Code, requestRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/drones/drone-001/commands?limit=5", nil)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)

	if listRec.Code != http.StatusOK {
		t.Fatalf("expected list status %d, got %d: %s", http.StatusOK, listRec.Code, listRec.Body.String())
	}

	var commands []dtos.CommandResponse
	if err := json.NewDecoder(listRec.Body).Decode(&commands); err != nil {
		t.Fatalf("decode commands: %v", err)
	}

	if len(commands) != 1 {
		t.Fatalf("expected one command, got %d", len(commands))
	}

	if commands[0].Type != "arm" {
		t.Fatalf("expected arm command, got %q", commands[0].Type)
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

func TestAgentFetchesNoContentWhenNoPendingCommand(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())

	registerTestVehicleAgent(t, router)

	req := httptest.NewRequest(http.MethodGet, "/api/vehicle-agents/agent-001/commands/next", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected status %d, got %d: %s", http.StatusNoContent, rec.Code, rec.Body.String())
	}
}

func TestAgentReportsCommandStatus(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())

	registerTestVehicleAgent(t, router)
	sendTestHeartbeat(t, router)
	sendTestTelemetry(t, router)

	requestReq := httptest.NewRequest(http.MethodPost, "/api/drones/drone-001/commands/land", nil)
	requestRec := httptest.NewRecorder()
	router.ServeHTTP(requestRec, requestReq)

	var requested dtos.CommandResponse
	if err := json.NewDecoder(requestRec.Body).Decode(&requested); err != nil {
		t.Fatalf("decode requested command: %v", err)
	}

	fetchReq := httptest.NewRequest(http.MethodGet, "/api/vehicle-agents/agent-001/commands/next", nil)
	fetchRec := httptest.NewRecorder()
	router.ServeHTTP(fetchRec, fetchReq)

	if fetchRec.Code != http.StatusOK {
		t.Fatalf("expected fetch status %d, got %d: %s", http.StatusOK, fetchRec.Code, fetchRec.Body.String())
	}

	body := []byte(`{"state":"vehicle_acked","resultMessage":"accepted by vehicle"}`)
	statusReq := httptest.NewRequest(http.MethodPost, "/api/vehicle-agents/agent-001/commands/"+requested.ID+"/status", bytes.NewReader(body))
	statusRec := httptest.NewRecorder()
	router.ServeHTTP(statusRec, statusReq)

	if statusRec.Code != http.StatusOK {
		t.Fatalf("expected status update %d, got %d: %s", http.StatusOK, statusRec.Code, statusRec.Body.String())
	}

	var updated dtos.CommandResponse
	if err := json.NewDecoder(statusRec.Body).Decode(&updated); err != nil {
		t.Fatalf("decode updated command: %v", err)
	}

	if updated.State != "vehicle_acked" {
		t.Fatalf("expected vehicle_acked, got %q", updated.State)
	}

	if updated.ResultMessage != "accepted by vehicle" {
		t.Fatalf("expected result message, got %q", updated.ResultMessage)
	}
}

func TestAgentCannotReportCommandResultBeforeFetching(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())

	registerTestVehicleAgent(t, router)
	sendTestHeartbeat(t, router)
	sendTestTelemetry(t, router)

	requestReq := httptest.NewRequest(http.MethodPost, "/api/drones/drone-001/commands/land", nil)
	requestRec := httptest.NewRecorder()
	router.ServeHTTP(requestRec, requestReq)

	var requested dtos.CommandResponse
	if err := json.NewDecoder(requestRec.Body).Decode(&requested); err != nil {
		t.Fatalf("decode requested command: %v", err)
	}

	body := []byte(`{"state":"vehicle_acked"}`)
	statusReq := httptest.NewRequest(http.MethodPost, "/api/vehicle-agents/agent-001/commands/"+requested.ID+"/status", bytes.NewReader(body))
	statusRec := httptest.NewRecorder()
	router.ServeHTTP(statusRec, statusReq)

	if statusRec.Code != http.StatusConflict {
		t.Fatalf("expected status update %d, got %d: %s", http.StatusConflict, statusRec.Code, statusRec.Body.String())
	}
}

func TestAgentCannotReportBackendOwnedCommandState(t *testing.T) {
	repo := newTestRepository(t)
	router := NewRouter(repo.dependencies())

	registerTestVehicleAgent(t, router)
	sendTestHeartbeat(t, router)
	sendTestTelemetry(t, router)

	requestReq := httptest.NewRequest(http.MethodPost, "/api/drones/drone-001/commands/land", nil)
	requestRec := httptest.NewRecorder()
	router.ServeHTTP(requestRec, requestReq)

	var requested dtos.CommandResponse
	if err := json.NewDecoder(requestRec.Body).Decode(&requested); err != nil {
		t.Fatalf("decode requested command: %v", err)
	}

	body := []byte(`{"state":"authorized"}`)
	statusReq := httptest.NewRequest(http.MethodPost, "/api/vehicle-agents/agent-001/commands/"+requested.ID+"/status", bytes.NewReader(body))
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
	*svc.TelemetryService
	*svc.CommandService
	*svc.MissionService
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
		VehicleAgentService: appServices.VehicleAgents,
		TelemetryService:    appServices.Telemetry,
		CommandService:      appServices.Commands,
		MissionService:      appServices.Missions,
		repos:               repos,
		deps: Dependencies{
			VehicleAgents: appServices.VehicleAgents,
			Telemetry:     appServices.Telemetry,
			Commands:      appServices.Commands,
			Missions:      appServices.Missions,
			Fleet:         appServices.Fleet,
		},
	}
}
