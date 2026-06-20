package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sunnyside/atlas/atlas-backend/internal/registry"
)

func TestHealthz(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	NewRouter().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var body healthResponse
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
	reg := registry.NewMemoryRegistry()
	router := NewRouterWithRegistry(reg)

	registerBody := []byte(`{
		"agentId": "agent-001",
		"droneId": "drone-001",
		"droneName": "Training Quad 1",
		"agentVersion": "0.1.0"
	}`)
	registerReq := httptest.NewRequest(http.MethodPost, "/api/agents/register", bytes.NewReader(registerBody))
	registerRec := httptest.NewRecorder()

	router.ServeHTTP(registerRec, registerReq)

	if registerRec.Code != http.StatusOK {
		t.Fatalf("expected register status %d, got %d: %s", http.StatusOK, registerRec.Code, registerRec.Body.String())
	}

	heartbeatBody := []byte(`{"agentVersion": "0.1.0"}`)
	heartbeatReq := httptest.NewRequest(http.MethodPost, "/api/agents/agent-001/heartbeat", bytes.NewReader(heartbeatBody))
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

	var drones []droneResponse
	if err := json.NewDecoder(listRec.Body).Decode(&drones); err != nil {
		t.Fatalf("decode drones: %v", err)
	}

	if len(drones) != 1 {
		t.Fatalf("expected one drone, got %d", len(drones))
	}

	if drones[0].Status != "online" {
		t.Fatalf("expected online drone, got %q", drones[0].Status)
	}

	if drones[0].AgentID != "agent-001" {
		t.Fatalf("expected agent-001, got %q", drones[0].AgentID)
	}
}

func TestRecordTelemetryAndListDrones(t *testing.T) {
	reg := registry.NewMemoryRegistry()
	router := NewRouterWithRegistry(reg)

	registerBody := []byte(`{
		"agentId": "agent-001",
		"droneId": "drone-001",
		"droneName": "Training Quad 1",
		"agentVersion": "0.1.0"
	}`)
	registerReq := httptest.NewRequest(http.MethodPost, "/api/agents/register", bytes.NewReader(registerBody))
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
	telemetryReq := httptest.NewRequest(http.MethodPost, "/api/agents/agent-001/telemetry", bytes.NewReader(telemetryBody))
	telemetryRec := httptest.NewRecorder()
	router.ServeHTTP(telemetryRec, telemetryReq)

	if telemetryRec.Code != http.StatusOK {
		t.Fatalf("expected telemetry status %d, got %d: %s", http.StatusOK, telemetryRec.Code, telemetryRec.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/drones", nil)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)

	var drones []droneResponse
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
