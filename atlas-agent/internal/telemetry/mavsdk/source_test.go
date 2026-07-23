package mavsdk

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/geolocation"
	corepb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/core"
	directpb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/mavlink_direct"
	telemetrypb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/telemetry"
	"github.com/sunnyside/atlas/atlas-agent/internal/navigation"
	"github.com/sunnyside/atlas/atlas-agent/internal/telemetry"
	"google.golang.org/grpc"
)

type fakeTelemetryServer struct {
	telemetrypb.UnimplementedTelemetryServiceServer
}

type fakeCoreServer struct {
	corepb.UnimplementedCoreServiceServer
}

func (fakeCoreServer) SubscribeConnectionState(_ *corepb.SubscribeConnectionStateRequest, stream grpc.ServerStreamingServer[corepb.ConnectionStateResponse]) error {
	return stream.Send(&corepb.ConnectionStateResponse{ConnectionState: &corepb.ConnectionState{IsConnected: true}})
}

type fakeDirectServer struct {
	directpb.UnimplementedMavlinkDirectServiceServer
}

func (fakeDirectServer) SubscribeMessage(request *directpb.SubscribeMessageRequest, stream grpc.ServerStreamingServer[directpb.MessageResponse]) error {
	fields := map[string]string{
		"LOCAL_POSITION_NED": `{"time_boot_ms":1000,"x":1,"y":2,"z":-3,"vx":0.1,"vy":0.2,"vz":-0.3}`,
		"ODOMETRY":           `{"time_usec":1000000,"frame_id":18,"child_frame_id":8,"x":1,"y":2,"z":-3,"q":[1,0,0,0],"vx":0.1,"vy":0.2,"vz":-0.3,"reset_counter":2,"quality":90}`,
		"ESTIMATOR_STATUS":   `{"time_usec":1000000,"flags":79,"vel_ratio":0.1,"pos_horiz_ratio":0.2,"pos_vert_ratio":0.3,"hagl_ratio":0.4}`,
		"OPTICAL_FLOW_RAD":   `{"time_usec":1000000,"sensor_id":3,"integration_time_us":14285,"integrated_x":0.01,"integrated_y":-0.02,"quality":200,"distance":1.25}`,
		"DISTANCE_SENSOR":    `{"time_boot_ms":1000,"min_distance":8,"max_distance":3000,"current_distance":125,"id":4,"orientation":25,"signal_quality":80}`,
	}[request.GetMessageName()]
	return stream.Send(&directpb.MessageResponse{Message: &directpb.MavlinkMessage{
		MessageName: request.GetMessageName(), SystemId: 1, ComponentId: 1, FieldsJson: fields,
	}})
}

func (fakeDirectServer) SendMessage(context.Context, *directpb.SendMessageRequest) (*directpb.SendMessageResponse, error) {
	return &directpb.SendMessageResponse{MavlinkDirectResult: &directpb.MavlinkDirectResult{Result: directpb.MavlinkDirectResult_RESULT_SUCCESS}}, nil
}

func (fakeTelemetryServer) SubscribePosition(_ *telemetrypb.SubscribePositionRequest, stream grpc.ServerStreamingServer[telemetrypb.PositionResponse]) error {
	return stream.Send(&telemetrypb.PositionResponse{Position: &telemetrypb.Position{
		LatitudeDeg: 51.5074, LongitudeDeg: -0.1278, AbsoluteAltitudeM: 45.3, RelativeAltitudeM: 12.5,
	}})
}

func (fakeTelemetryServer) SubscribeBattery(_ *telemetrypb.SubscribeBatteryRequest, stream grpc.ServerStreamingServer[telemetrypb.BatteryResponse]) error {
	return stream.Send(&telemetrypb.BatteryResponse{Battery: &telemetrypb.Battery{
		Id: 0, BatteryFunction: telemetrypb.BatteryFunction_BATTERY_FUNCTION_ALL,
		RemainingPercent: 82.5, VoltageV: 22.8, CurrentBatteryA: 5.2,
		TemperatureDegc: 31, CapacityConsumedAh: 1.4, TimeRemainingS: 1200,
	}})
}

func (fakeTelemetryServer) SubscribeFlightMode(_ *telemetrypb.SubscribeFlightModeRequest, stream grpc.ServerStreamingServer[telemetrypb.FlightModeResponse]) error {
	return stream.Send(&telemetrypb.FlightModeResponse{FlightMode: telemetrypb.FlightMode_FLIGHT_MODE_HOLD})
}

func (fakeTelemetryServer) SubscribeArmed(_ *telemetrypb.SubscribeArmedRequest, stream grpc.ServerStreamingServer[telemetrypb.ArmedResponse]) error {
	return stream.Send(&telemetrypb.ArmedResponse{IsArmed: true})
}

func (fakeTelemetryServer) SubscribeGpsInfo(_ *telemetrypb.SubscribeGpsInfoRequest, stream grpc.ServerStreamingServer[telemetrypb.GpsInfoResponse]) error {
	return stream.Send(&telemetrypb.GpsInfoResponse{GpsInfo: &telemetrypb.GpsInfo{
		FixType: telemetrypb.FixType_FIX_TYPE_FIX_3D, NumSatellites: 14,
	}})
}

func (fakeTelemetryServer) SubscribeVelocityNed(_ *telemetrypb.SubscribeVelocityNedRequest, stream grpc.ServerStreamingServer[telemetrypb.VelocityNedResponse]) error {
	return stream.Send(&telemetrypb.VelocityNedResponse{VelocityNed: &telemetrypb.VelocityNed{NorthMS: 4, EastMS: 2, DownMS: -0.5}})
}

func (fakeTelemetryServer) SubscribeAltitude(_ *telemetrypb.SubscribeAltitudeRequest, stream grpc.ServerStreamingServer[telemetrypb.AltitudeResponse]) error {
	return stream.Send(&telemetrypb.AltitudeResponse{Altitude: &telemetrypb.Altitude{AltitudeAmslM: 45.3, AltitudeTerrainM: 12.4, BottomClearanceM: 12.3}})
}

func (fakeTelemetryServer) SubscribeHealth(_ *telemetrypb.SubscribeHealthRequest, stream grpc.ServerStreamingServer[telemetrypb.HealthResponse]) error {
	return stream.Send(&telemetrypb.HealthResponse{Health: &telemetrypb.Health{
		IsGyrometerCalibrationOk: true, IsAccelerometerCalibrationOk: true,
		IsMagnetometerCalibrationOk: true, IsLocalPositionOk: true,
		IsGlobalPositionOk: true, IsHomePositionOk: true, IsArmable: true,
	}})
}

func (fakeTelemetryServer) SubscribeLandedState(_ *telemetrypb.SubscribeLandedStateRequest, stream grpc.ServerStreamingServer[telemetrypb.LandedStateResponse]) error {
	return stream.Send(&telemetrypb.LandedStateResponse{LandedState: telemetrypb.LandedState_LANDED_STATE_IN_AIR})
}

func (fakeTelemetryServer) SubscribeRcStatus(_ *telemetrypb.SubscribeRcStatusRequest, stream grpc.ServerStreamingServer[telemetrypb.RcStatusResponse]) error {
	return stream.Send(&telemetrypb.RcStatusResponse{RcStatus: &telemetrypb.RcStatus{WasAvailableOnce: true, IsAvailable: true, SignalStrengthPercent: 76}})
}

func (fakeTelemetryServer) SubscribeHome(_ *telemetrypb.SubscribeHomeRequest, stream grpc.ServerStreamingServer[telemetrypb.HomeResponse]) error {
	return stream.Send(&telemetrypb.HomeResponse{Home: &telemetrypb.Position{LatitudeDeg: 51.507, LongitudeDeg: -0.128, AbsoluteAltitudeM: 32.9}})
}

func (fakeTelemetryServer) SubscribeRawGps(_ *telemetrypb.SubscribeRawGpsRequest, stream grpc.ServerStreamingServer[telemetrypb.RawGpsResponse]) error {
	return stream.Send(&telemetrypb.RawGpsResponse{RawGps: &telemetrypb.RawGps{Hdop: 0.8, Vdop: 1.2, HorizontalUncertaintyM: 0.4, VerticalUncertaintyM: 0.7, VelocityUncertaintyMS: 0.1, CogDeg: 26.6}})
}

func (fakeTelemetryServer) SubscribeAttitudeQuaternion(_ *telemetrypb.SubscribeAttitudeQuaternionRequest, stream grpc.ServerStreamingServer[telemetrypb.AttitudeQuaternionResponse]) error {
	time.Sleep(50 * time.Millisecond)
	return stream.Send(&telemetrypb.AttitudeQuaternionResponse{AttitudeQuaternion: &telemetrypb.Quaternion{W: 1, TimestampUs: 1_000_000}})
}

func (fakeTelemetryServer) SubscribeUnixEpochTime(_ *telemetrypb.SubscribeUnixEpochTimeRequest, stream grpc.ServerStreamingServer[telemetrypb.UnixEpochTimeResponse]) error {
	return stream.Send(&telemetrypb.UnixEpochTimeResponse{TimeUs: 1_700_000_000_000_000})
}

func (fakeTelemetryServer) SubscribeStatusText(_ *telemetrypb.SubscribeStatusTextRequest, stream grpc.ServerStreamingServer[telemetrypb.StatusTextResponse]) error {
	return stream.Send(&telemetrypb.StatusTextResponse{StatusText: &telemetrypb.StatusText{Type: telemetrypb.StatusTextType_STATUS_TEXT_TYPE_WARNING, Text: "Battery temperature high"}})
}

func TestModeMappingsUseStableAtlasLabels(t *testing.T) {
	if got := flightModeString(telemetrypb.FlightMode_FLIGHT_MODE_RETURN_TO_LAUNCH); got != "RETURN_TO_LAUNCH" {
		t.Fatalf("flightModeString() = %q", got)
	}
	if got := gpsFixString(telemetrypb.FixType_FIX_TYPE_RTK_FIXED); got != "RTK_FIXED" {
		t.Fatalf("gpsFixString() = %q", got)
	}
}

func TestEmitLatestDropsSupersededSnapshot(t *testing.T) {
	updates := make(chan telemetry.Snapshot, 1)
	first := telemetry.Snapshot{ObservedAt: time.Unix(1, 0)}
	second := telemetry.Snapshot{ObservedAt: time.Unix(2, 0)}
	emitLatest(updates, first)
	emitLatest(updates, second)
	if got := <-updates; !got.ObservedAt.Equal(second.ObservedAt) {
		t.Fatalf("latest snapshot observed at %v", got.ObservedAt)
	}
}

func TestEmitStatusTextDropsOldestEventWhenBufferIsFull(t *testing.T) {
	events := make(chan telemetry.StatusTextEvent, 1)
	first := telemetry.StatusTextEvent{Text: "first"}
	second := telemetry.StatusTextEvent{Text: "second"}
	emitStatusText(events, first)
	emitStatusText(events, second)
	if got := <-events; got.Text != second.Text {
		t.Fatalf("latest status text = %#v", got)
	}
}

func TestSetHomeFromCurrentPositionPreservesFirstValidHome(t *testing.T) {
	latitude := 37.412173
	longitude := -121.998878
	homeSet := true
	snapshot := telemetry.Snapshot{Latitude: &latitude, Longitude: &longitude, HomePositionSet: &homeSet}
	setHomeFromCurrentPosition(&snapshot)
	if snapshot.HomePosition == nil || snapshot.HomePosition.Latitude == nil || *snapshot.HomePosition.Latitude != latitude || snapshot.HomePosition.Longitude == nil || *snapshot.HomePosition.Longitude != longitude {
		t.Fatalf("home position = %#v", snapshot.HomePosition)
	}
	newLatitude := latitude + 0.01
	snapshot.Latitude = &newLatitude
	setHomeFromCurrentPosition(&snapshot)
	if *snapshot.HomePosition.Latitude != latitude {
		t.Fatalf("home moved with aircraft: %#v", snapshot.HomePosition)
	}
}

func TestStartAggregatesMAVSDKStreams(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	server := grpc.NewServer()
	corepb.RegisterCoreServiceServer(server, fakeCoreServer{})
	directpb.RegisterMavlinkDirectServiceServer(server, fakeDirectServer{})
	telemetrypb.RegisterTelemetryServiceServer(server, fakeTelemetryServer{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	foundation, err := geolocation.NewFoundation(geolocation.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := StartWithGeolocation(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), listener.Addr().String(), 10*time.Millisecond, foundation)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	for {
		select {
		case snapshot := <-outputs.Snapshots:
			if snapshot.BatteryPercent != nil && snapshot.FlightMode != nil && snapshot.Armed != nil && snapshot.GPSFix != nil && snapshot.Latitude != nil && snapshot.Health != nil && snapshot.RCStatus != nil && snapshot.GPSQuality != nil && snapshot.LandedState != nil && validHomePosition(snapshot.HomePosition) {
				if *snapshot.BatteryPercent != 82.5 || *snapshot.FlightMode != "HOLD" || !*snapshot.Armed || *snapshot.GPSFix != "3D" || *snapshot.Latitude != 51.5074 || !snapshot.Health.Armable || *snapshot.LandedState != "IN_AIR" || len(snapshot.Batteries) != 1 {
					t.Fatalf("snapshot = %#v", snapshot)
				}
				goto statusText
			}
		case <-ctx.Done():
			t.Fatal("complete MAVSDK snapshot was not published")
		}
	}

statusText:
	select {
	case event := <-outputs.StatusTexts:
		if event.Severity != "WARNING" || event.Text != "Battery temperature high" {
			t.Fatalf("status text = %#v", event)
		}
	case <-ctx.Done():
		t.Fatal("MAVSDK status text was not published")
	}
	for foundation.Health().PoseSamples == 0 {
		select {
		case <-time.After(10 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("high-rate timestamped aircraft pose was not buffered")
		}
	}
	for outputs.Navigation.Latest(time.Now()).Status != navigation.StatusReady {
		select {
		case <-time.After(10 * time.Millisecond):
		case <-ctx.Done():
			t.Fatalf("navigation plane did not become ready: %#v", outputs.Navigation.Latest(time.Now()))
		}
	}
}

func TestRecordAircraftPoseFusesTimestampedAttitudeWithAgedNavigationState(t *testing.T) {
	foundation, err := geolocation.NewFoundation(geolocation.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	s := &source{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), geolocation: foundation,
		pose: aircraftPoseState{
			positionReceivedMonotonicNS: 9_980_000_000, velocityReceivedMonotonicNS: 9_990_000_000,
			latitudeDeg: 51.5074, longitudeDeg: -0.1278, absoluteAltitudeM: 72, relativeAltitudeM: 40,
			positionValid: true, velocityNEDMPS: geolocation.Vector3{X: 4, Y: 2, Z: -0.5}, velocityValid: true,
			health: telemetry.VehicleHealth{GlobalPositionOK: true, LocalPositionOK: true}, healthValid: true,
		},
	}
	s.recordAircraftPose(&telemetrypb.Quaternion{W: 1, TimestampUs: 1_000_000}, geolocation.CompanionTime{
		MonotonicNS: 10_000_000_000, UnixNS: 1_700_000_000_000_000_000,
	})
	pose, err := foundation.PoseAt(10_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if pose.LatitudeDeg != 51.5074 || pose.Quality.PositionAge != 20*time.Millisecond || pose.Quality.VelocityAge != 10*time.Millisecond || !pose.Quality.GlobalPositionOK {
		t.Fatalf("pose = %#v", pose)
	}
}
