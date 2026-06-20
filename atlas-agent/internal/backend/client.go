package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
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
