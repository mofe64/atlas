package mavsdk

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	directpb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/mavlink_direct"
	"github.com/sunnyside/atlas/atlas-agent/internal/navigation"
)

type localPositionNEDFields struct {
	TimeBootMS uint64  `json:"time_boot_ms"`
	X          float64 `json:"x"`
	Y          float64 `json:"y"`
	Z          float64 `json:"z"`
	VX         float64 `json:"vx"`
	VY         float64 `json:"vy"`
	VZ         float64 `json:"vz"`
}

type odometryFields struct {
	TimeUS        uint64    `json:"time_usec"`
	FrameID       uint32    `json:"frame_id"`
	ChildFrameID  uint32    `json:"child_frame_id"`
	X             float64   `json:"x"`
	Y             float64   `json:"y"`
	Z             float64   `json:"z"`
	Q             []float64 `json:"q"`
	VX            float64   `json:"vx"`
	VY            float64   `json:"vy"`
	VZ            float64   `json:"vz"`
	RollSpeed     float64   `json:"rollspeed"`
	PitchSpeed    float64   `json:"pitchspeed"`
	YawSpeed      float64   `json:"yawspeed"`
	ResetCounter  uint8     `json:"reset_counter"`
	EstimatorType uint8     `json:"estimator_type"`
	Quality       int8      `json:"quality"`
}

type estimatorStatusFields struct {
	TimeUS         uint64  `json:"time_usec"`
	Flags          uint32  `json:"flags"`
	VelocityRatio  float64 `json:"vel_ratio"`
	HorizPosRatio  float64 `json:"pos_horiz_ratio"`
	VertPosRatio   float64 `json:"pos_vert_ratio"`
	HeightAGLRatio float64 `json:"hagl_ratio"`
}

type opticalFlowFields struct {
	TimeUS            uint64  `json:"time_usec"`
	SensorID          uint8   `json:"sensor_id"`
	IntegrationTimeUS uint32  `json:"integration_time_us"`
	IntegratedX       float64 `json:"integrated_x"`
	IntegratedY       float64 `json:"integrated_y"`
	Quality           uint8   `json:"quality"`
	Distance          float64 `json:"distance"`
}

type distanceSensorFields struct {
	TimeBootMS    uint64 `json:"time_boot_ms"`
	MinimumCM     uint16 `json:"min_distance"`
	MaximumCM     uint16 `json:"max_distance"`
	CurrentCM     uint16 `json:"current_distance"`
	SensorID      uint8  `json:"id"`
	Orientation   uint8  `json:"orientation"`
	SignalQuality uint8  `json:"signal_quality"`
}

func (s *source) streamNavigation(ctx context.Context) {
	streams := []struct {
		name   string
		handle func(string, time.Time) error
	}{
		{"LOCAL_POSITION_NED", s.handleLocalPositionNED},
		{"ODOMETRY", s.handleOdometry},
		{"ESTIMATOR_STATUS", s.handleEstimatorStatus},
		{"OPTICAL_FLOW_RAD", s.handleOpticalFlow},
		{"DISTANCE_SENSOR", s.handleDistanceSensor},
	}
	for _, stream := range streams {
		go s.streamDirectMessage(ctx, stream.name, stream.handle)
	}
	go s.requestDirectNavigationRates(ctx)
	<-ctx.Done()
}

func (s *source) streamDirectMessage(ctx context.Context, name string, handle func(string, time.Time) error) {
	for ctx.Err() == nil {
		stream, err := s.direct.SubscribeMessage(ctx, &directpb.SubscribeMessageRequest{MessageName: name})
		if err != nil {
			s.retry(ctx, "MAVLink Direct "+name, err)
			continue
		}
		for ctx.Err() == nil {
			response, receiveErr := stream.Recv()
			if receiveErr != nil {
				break
			}
			message := response.GetMessage()
			if message == nil || message.GetMessageName() != name {
				continue
			}
			s.mu.Lock()
			if message.GetSystemId() != 0 {
				s.navigationTargetSystemID = message.GetSystemId()
			}
			if message.GetComponentId() != 0 {
				s.navigationTargetComponentID = message.GetComponentId()
			}
			s.mu.Unlock()
			if parseErr := handle(message.GetFieldsJson(), time.Now().UTC()); parseErr != nil {
				s.logger.Warn("invalid navigation MAVLink message", "message", name, "error", parseErr)
			}
		}
		sleepOrDone(ctx, streamRetryDelay)
	}
}

func (s *source) requestDirectNavigationRates(ctx context.Context) {
	for {
		delay := rateRefreshInterval
		if !s.requestDirectNavigationRatesOnce(ctx) {
			delay = time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *source) requestDirectNavigationRatesOnce(ctx context.Context) bool {
	s.mu.RLock()
	systemID, componentID := s.navigationTargetSystemID, s.navigationTargetComponentID
	s.mu.RUnlock()
	if systemID == 0 || componentID == 0 {
		return false
	}
	// MAV_CMD_SET_MESSAGE_INTERVAL changes telemetry publication only. It does
	// not arm, change mode, publish setpoints, or grant movement authority.
	for _, request := range []struct {
		messageID uint32
		rateHz    int64
	}{{230, 10}, {106, 20}} { // ESTIMATOR_STATUS, OPTICAL_FLOW_RAD
		fields := fmt.Sprintf(`{"target_system":%d,"target_component":%d,"command":511,"confirmation":0,"param1":%d,"param2":%d,"param3":0,"param4":0,"param5":0,"param6":0,"param7":0}`,
			systemID, componentID, request.messageID, 1_000_000/request.rateHz)
		requestCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		response, err := s.direct.SendMessage(requestCtx, &directpb.SendMessageRequest{Message: &directpb.MavlinkMessage{
			MessageName: "COMMAND_LONG", TargetSystemId: systemID, TargetComponentId: componentID, FieldsJson: fields,
		}})
		cancel()
		if err != nil || response.GetMavlinkDirectResult().GetResult() != directpb.MavlinkDirectResult_RESULT_SUCCESS {
			s.logger.Debug("navigation MAVLink rate request was not accepted", "message_id", request.messageID, "error", err)
		}
	}
	return true
}

func (s *source) handleLocalPositionNED(raw string, received time.Time) error {
	var fields localPositionNEDFields
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return err
	}
	s.navigation.ObserveLocalPosition(fields.TimeBootMS*1000, received,
		navigation.Vector3{X: fields.X, Y: fields.Y, Z: fields.Z},
		navigation.Vector3{X: fields.VX, Y: fields.VY, Z: fields.VZ})
	return nil
}

func (s *source) handleOdometry(raw string, received time.Time) error {
	var fields odometryFields
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return err
	}
	attitude := navigation.Quaternion{W: 1}
	if len(fields.Q) == 4 {
		attitude = navigation.Quaternion{W: fields.Q[0], X: fields.Q[1], Y: fields.Q[2], Z: fields.Q[3]}
	}
	s.navigation.ObserveOdometry(fields.TimeUS, received, navigation.Odometry{
		FrameID: fields.FrameID, ChildFrameID: fields.ChildFrameID,
		Position: navigation.Vector3{X: fields.X, Y: fields.Y, Z: fields.Z}, Attitude: attitude,
		Velocity:        navigation.Vector3{X: fields.VX, Y: fields.VY, Z: fields.VZ},
		AngularVelocity: navigation.Vector3{X: fields.RollSpeed, Y: fields.PitchSpeed, Z: fields.YawSpeed},
		ResetCounter:    fields.ResetCounter, EstimatorType: fields.EstimatorType, Quality: fields.Quality,
	})
	return nil
}

func (s *source) handleEstimatorStatus(raw string, received time.Time) error {
	var fields estimatorStatusFields
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return err
	}
	s.navigation.ObserveEstimator(fields.TimeUS, received, navigation.EstimatorStatus{
		Flags: fields.Flags, VelocityTestRatio: fields.VelocityRatio,
		HorizontalPosTestRatio: fields.HorizPosRatio, VerticalPosTestRatio: fields.VertPosRatio,
		HeightAGLTestRatio: fields.HeightAGLRatio,
	})
	return nil
}

func (s *source) handleOpticalFlow(raw string, received time.Time) error {
	var fields opticalFlowFields
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return err
	}
	s.navigation.ObserveOpticalFlow(fields.TimeUS, received, navigation.OpticalFlow{
		SensorID: fields.SensorID, IntegrationTimeUS: fields.IntegrationTimeUS, IntegratedXRad: fields.IntegratedX,
		IntegratedYRad: fields.IntegratedY, Quality: fields.Quality, DistanceM: fields.Distance,
	})
	return nil
}

func (s *source) handleDistanceSensor(raw string, received time.Time) error {
	var fields distanceSensorFields
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return err
	}
	current := float64(fields.CurrentCM) / 100
	if fields.CurrentCM == math.MaxUint16 {
		current = -1
	}
	s.navigation.ObserveRange(fields.TimeBootMS*1000, received, navigation.Range{
		SensorID: fields.SensorID, Orientation: fields.Orientation,
		MinimumM: float64(fields.MinimumCM) / 100, MaximumM: float64(fields.MaximumCM) / 100,
		CurrentM: current, SignalQuality: fields.SignalQuality,
	})
	return nil
}
