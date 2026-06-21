package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	baseURL    string
	httpClient *http.Client
}

type RegisterAgentRequest struct {
	AgentID      string `json:"agentId"`
	DroneID      string `json:"droneId"`
	DroneName    string `json:"droneName"`
	AgentVersion string `json:"agentVersion"`
}

type RegisterAgentResponse struct {
	AgentID                  string `json:"agentId"`
	DroneID                  string `json:"droneId"`
	Status                   string `json:"status"`
	HeartbeatIntervalSeconds int    `json:"heartbeatIntervalSeconds"`
}

type HeartbeatRequest struct {
	AgentVersion string `json:"agentVersion"`
}

type HeartbeatResponse struct {
	AgentID                   string `json:"agentId"`
	DroneID                   string `json:"droneId"`
	Status                    string `json:"status"`
	LastHeartbeatAt           string `json:"lastHeartbeatAt"`
	NextHeartbeatAfterSeconds int    `json:"nextHeartbeatAfterSeconds"`
}

type TelemetryRequest struct {
	ObservedAt        time.Time `json:"observedAt"`
	BatteryPercent    float64   `json:"batteryPercent"`
	RelativeAltitudeM float64   `json:"relativeAltitudeM"`
	FlightMode        string    `json:"flightMode"`
	Armed             bool      `json:"armed"`
	InAir             bool      `json:"inAir"`
	Latitude          float64   `json:"latitude"`
	Longitude         float64   `json:"longitude"`
	HeadingDeg        float64   `json:"headingDeg"`
	GroundSpeedMPS    float64   `json:"groundSpeedMPS"`
	GPSFix            string    `json:"gpsFix"`
	SatellitesVisible int       `json:"satellitesVisible"`
	HomePositionSet   bool      `json:"homePositionSet"`
	Source            string    `json:"source"`
}

type TelemetryResponse struct {
	DroneID        string `json:"droneId"`
	AgentID        string `json:"agentId"`
	TelemetryState string `json:"telemetryState"`
	ReceivedAt     string `json:"receivedAt"`
}

type Command struct {
	ID             string `json:"id"`
	DroneID        string `json:"droneId"`
	AgentID        string `json:"agentId"`
	Type           string `json:"type"`
	State          string `json:"state"`
	RequestedBy    string `json:"requestedBy"`
	RequestedAt    string `json:"requestedAt"`
	UpdatedAt      string `json:"updatedAt"`
	PolicyReason   string `json:"policyReason,omitempty"`
	ResultMessage  string `json:"resultMessage,omitempty"`
	TelemetryState string `json:"telemetryState"`
	AgentStatus    string `json:"agentStatus"`
}

type CommandStatusRequest struct {
	State         string `json:"state"`
	ResultMessage string `json:"resultMessage,omitempty"`
}

const (
	CommandTypeArm            = "arm"
	CommandTypeTakeoff        = "takeoff"
	CommandTypeReturnToLaunch = "return_to_launch"
	CommandTypeLand           = "land"

	CommandStateAgentReceived   = "agent_received"
	CommandStateSentToVehicle   = "sent_to_vehicle"
	CommandStateVehicleAcked    = "vehicle_acked"
	CommandStateVehicleRejected = "vehicle_rejected"
	CommandStateFailed          = "failed"
)

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (c *Client) RegisterAgent(ctx context.Context, req RegisterAgentRequest) (RegisterAgentResponse, error) {
	var res RegisterAgentResponse
	if err := c.postJSON(ctx, "/api/agents/register", req, &res); err != nil {
		return RegisterAgentResponse{}, err
	}

	return res, nil
}

func (c *Client) SendHeartbeat(ctx context.Context, agentID string, req HeartbeatRequest) (HeartbeatResponse, error) {
	var res HeartbeatResponse
	path := fmt.Sprintf("/api/agents/%s/heartbeat", agentID)
	if err := c.postJSON(ctx, path, req, &res); err != nil {
		return HeartbeatResponse{}, err
	}

	return res, nil
}

func (c *Client) SendTelemetry(ctx context.Context, agentID string, req TelemetryRequest) (TelemetryResponse, error) {
	var res TelemetryResponse
	path := fmt.Sprintf("/api/agents/%s/telemetry", agentID)
	if err := c.postJSON(ctx, path, req, &res); err != nil {
		return TelemetryResponse{}, err
	}

	return res, nil
}

func (c *Client) FetchNextCommand(ctx context.Context, agentID string) (Command, bool, error) {
	var res Command
	path := fmt.Sprintf("/api/agents/%s/commands/next", url.PathEscape(agentID))
	ok, err := c.getJSON(ctx, path, &res)
	if err != nil {
		return Command{}, false, err
	}

	return res, ok, nil
}

func (c *Client) ReportCommandStatus(ctx context.Context, agentID string, commandID string, req CommandStatusRequest) (Command, error) {
	var res Command
	path := fmt.Sprintf("/api/agents/%s/commands/%s/status", url.PathEscape(agentID), url.PathEscape(commandID))
	if err := c.postJSON(ctx, path, req, &res); err != nil {
		return Command{}, err
	}

	return res, nil
}

func (c *Client) getJSON(ctx context.Context, path string, out any) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return false, fmt.Errorf("build request: %w", err)
	}

	res, err := c.httpClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("send request: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusNoContent {
		return false, nil
	}

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return false, fmt.Errorf("backend returned status %d", res.StatusCode)
	}

	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return false, fmt.Errorf("decode response: %w", err)
	}

	return true, nil
}

func (c *Client) postJSON(ctx context.Context, path string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("backend returned status %d", res.StatusCode)
	}

	if err := json.NewDecoder(res.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}

	return nil
}
