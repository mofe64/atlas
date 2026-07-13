package vehicle

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	actionpb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/action"
	corepb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/core"
	missionpb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/mission"
	telemetrypb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/telemetry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	px4SourceName                  = "px4"
	px4StreamRetryDelay            = 2 * time.Second
	px4RateHz                      = 2
	missionStartTakeoffTimeout     = 45 * time.Second
	missionStartReadinessPollDelay = 250 * time.Millisecond
)

type MAVSDKGateway struct {
	conn      *grpc.ClientConn
	action    actionpb.ActionServiceClient
	core      corepb.CoreServiceClient
	mission   missionpb.MissionServiceClient
	telemetry telemetrypb.TelemetryServiceClient

	mu             sync.RWMutex
	snapshot       TelemetryEvent
	havePosition   bool
	haveConnection bool
	connected      bool
}

func NewMAVSDKGateway(grpcAddr string) (*MAVSDKGateway, error) {
	if grpcAddr == "" {
		return nil, errors.New("mavsdk gRPC address is required")
	}

	conn, err := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("create mavsdk gRPC client: %w", err)
	}

	return &MAVSDKGateway{
		conn:      conn,
		action:    actionpb.NewActionServiceClient(conn),
		core:      corepb.NewCoreServiceClient(conn),
		mission:   missionpb.NewMissionServiceClient(conn),
		telemetry: telemetrypb.NewTelemetryServiceClient(conn),
		snapshot: TelemetryEvent{
			FlightMode: "UNKNOWN",
			GPSFix:     "UNKNOWN",
			Source:     px4SourceName,
		},
	}, nil
}

func (g *MAVSDKGateway) Telemetry(ctx context.Context) (<-chan TelemetryEvent, error) {
	events := make(chan TelemetryEvent, 16)

	g.bestEffortSetRates(ctx)

	go g.streamConnectionState(ctx)
	go g.streamPosition(ctx, events)
	go g.streamBattery(ctx, events)
	go g.streamFlightMode(ctx, events)
	go g.streamArmed(ctx, events)
	go g.streamInAir(ctx, events)
	go g.streamGpsInfo(ctx, events)
	go g.streamHeading(ctx, events)
	go g.streamVelocity(ctx, events)
	go g.streamAltitude(ctx, events)
	go g.streamHealth(ctx, events)

	go func() {
		<-ctx.Done()
		_ = g.conn.Close()
	}()

	return events, nil
}

func (g *MAVSDKGateway) Arm(ctx context.Context) error {
	res, err := g.action.Arm(ctx, &actionpb.ArmRequest{})
	if err != nil {
		return fmt.Errorf("arm vehicle: %w", err)
	}

	return actionResultError("arm vehicle", res.GetActionResult())
}

func (g *MAVSDKGateway) Takeoff(ctx context.Context) error {
	res, err := g.action.Takeoff(ctx, &actionpb.TakeoffRequest{})
	if err != nil {
		return fmt.Errorf("takeoff vehicle: %w", err)
	}

	return actionResultError("takeoff vehicle", res.GetActionResult())
}

func (g *MAVSDKGateway) ReturnToLaunch(ctx context.Context) error {
	res, err := g.action.ReturnToLaunch(ctx, &actionpb.ReturnToLaunchRequest{})
	if err != nil {
		return fmt.Errorf("return vehicle to launch: %w", err)
	}

	return actionResultError("return vehicle to launch", res.GetActionResult())
}

func (g *MAVSDKGateway) Land(ctx context.Context) error {
	res, err := g.action.Land(ctx, &actionpb.LandRequest{})
	if err != nil {
		return fmt.Errorf("land vehicle: %w", err)
	}

	return actionResultError("land vehicle", res.GetActionResult())
}

func (g *MAVSDKGateway) UploadMission(ctx context.Context, mission MissionPlan) error {
	plan, err := missionPlanToMAVSDK(mission)
	if err != nil {
		return err
	}

	rtlRes, err := g.mission.SetReturnToLaunchAfterMission(ctx, &missionpb.SetReturnToLaunchAfterMissionRequest{
		Enable: mission.CompletionAction == MissionCompletionActionReturnToLaunch || mission.CompletionAction == "",
	})
	if err != nil {
		return fmt.Errorf("configure mission completion action: %w", err)
	}
	if err := missionResultError("configure mission completion action", rtlRes.GetMissionResult()); err != nil {
		return err
	}

	uploadRes, err := g.mission.UploadMission(ctx, &missionpb.UploadMissionRequest{
		MissionPlan: plan,
	})
	if err != nil {
		return fmt.Errorf("upload mission: %w", err)
	}

	return missionResultError("upload mission", uploadRes.GetMissionResult())
}

func (g *MAVSDKGateway) PrepareMissionStart(ctx context.Context) error {
	snapshot := g.currentSnapshot()
	if !snapshot.Armed {
		if err := g.Arm(ctx); err != nil {
			return fmt.Errorf("prepare mission start: %w", err)
		}
	}

	snapshot = g.currentSnapshot()
	if snapshot.InAir {
		return nil
	}

	if err := g.Takeoff(ctx); err != nil {
		return fmt.Errorf("prepare mission start: %w", err)
	}

	takeoffCtx, cancel := context.WithTimeout(ctx, missionStartTakeoffTimeout)
	defer cancel()
	if err := g.waitUntilInAir(takeoffCtx); err != nil {
		return fmt.Errorf("prepare mission start: %w", err)
	}

	return nil
}

func (g *MAVSDKGateway) StartMission(ctx context.Context) error {
	res, err := g.mission.StartMission(ctx, &missionpb.StartMissionRequest{})
	if err != nil {
		return fmt.Errorf("start mission: %w", err)
	}

	return missionResultError("start mission", res.GetMissionResult())
}

func (g *MAVSDKGateway) currentSnapshot() TelemetryEvent {
	g.mu.RLock()
	defer g.mu.RUnlock()

	return g.snapshot
}

func (g *MAVSDKGateway) waitUntilInAir(ctx context.Context) error {
	ticker := time.NewTicker(missionStartReadinessPollDelay)
	defer ticker.Stop()

	for {
		if g.currentSnapshot().InAir {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (g *MAVSDKGateway) MissionProgress(ctx context.Context) (<-chan MissionProgressEvent, error) {
	stream, err := g.mission.SubscribeMissionProgress(ctx, &missionpb.SubscribeMissionProgressRequest{})
	if err != nil {
		return nil, fmt.Errorf("subscribe mission progress: %w", err)
	}

	events := make(chan MissionProgressEvent, 4)
	go func() {
		defer close(events)

		for ctx.Err() == nil {
			res, err := stream.Recv()
			if err != nil {
				return
			}

			event := missionProgressFromMAVSDK(res.GetMissionProgress())
			select {
			case <-ctx.Done():
				return
			case events <- event:
			}
		}
	}()

	return events, nil
}

func (g *MAVSDKGateway) bestEffortSetRates(ctx context.Context) {
	rateCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	_, _ = g.telemetry.SetRatePosition(rateCtx, &telemetrypb.SetRatePositionRequest{RateHz: px4RateHz})
	_, _ = g.telemetry.SetRateBattery(rateCtx, &telemetrypb.SetRateBatteryRequest{RateHz: px4RateHz})
	_, _ = g.telemetry.SetRateGpsInfo(rateCtx, &telemetrypb.SetRateGpsInfoRequest{RateHz: px4RateHz})
	_, _ = g.telemetry.SetRateInAir(rateCtx, &telemetrypb.SetRateInAirRequest{RateHz: px4RateHz})
	_, _ = g.telemetry.SetRateAltitude(rateCtx, &telemetrypb.SetRateAltitudeRequest{RateHz: px4RateHz})
	_, _ = g.telemetry.SetRateVelocityNed(rateCtx, &telemetrypb.SetRateVelocityNedRequest{RateHz: px4RateHz})
}

func (g *MAVSDKGateway) streamConnectionState(ctx context.Context) {
	for ctx.Err() == nil {
		stream, err := g.core.SubscribeConnectionState(ctx, &corepb.SubscribeConnectionStateRequest{})
		if err != nil {
			sleepOrDone(ctx, px4StreamRetryDelay)
			continue
		}

		for ctx.Err() == nil {
			res, err := stream.Recv()
			if err != nil {
				break
			}

			state := res.GetConnectionState()
			g.mu.Lock()
			g.haveConnection = true
			g.connected = state.GetIsConnected()
			g.mu.Unlock()
		}

		sleepOrDone(ctx, px4StreamRetryDelay)
	}
}

func (g *MAVSDKGateway) streamPosition(ctx context.Context, events chan TelemetryEvent) {
	for ctx.Err() == nil {
		stream, err := g.telemetry.SubscribePosition(ctx, &telemetrypb.SubscribePositionRequest{})
		if err != nil {
			sleepOrDone(ctx, px4StreamRetryDelay)
			continue
		}

		for ctx.Err() == nil {
			res, err := stream.Recv()
			if err != nil {
				break
			}

			position := res.GetPosition()
			lat := position.GetLatitudeDeg()
			lon := position.GetLongitudeDeg()
			relativeAltitude := float64(position.GetRelativeAltitudeM())
			if !finite(lat) || !finite(lon) || !finite(relativeAltitude) {
				continue
			}

			g.update(events, func(snapshot *TelemetryEvent) {
				snapshot.Latitude = lat
				snapshot.Longitude = lon
				snapshot.RelativeAltitudeM = relativeAltitude
				g.havePosition = true
			})
		}

		sleepOrDone(ctx, px4StreamRetryDelay)
	}
}

func (g *MAVSDKGateway) streamBattery(ctx context.Context, events chan TelemetryEvent) {
	for ctx.Err() == nil {
		stream, err := g.telemetry.SubscribeBattery(ctx, &telemetrypb.SubscribeBatteryRequest{})
		if err != nil {
			sleepOrDone(ctx, px4StreamRetryDelay)
			continue
		}

		for ctx.Err() == nil {
			res, err := stream.Recv()
			if err != nil {
				break
			}

			remaining := float64(res.GetBattery().GetRemainingPercent())
			if !finite(remaining) {
				continue
			}

			g.update(events, func(snapshot *TelemetryEvent) {
				snapshot.BatteryPercent = clamp(remaining, 0, 100)
			})
		}

		sleepOrDone(ctx, px4StreamRetryDelay)
	}
}

func (g *MAVSDKGateway) streamFlightMode(ctx context.Context, events chan TelemetryEvent) {
	for ctx.Err() == nil {
		stream, err := g.telemetry.SubscribeFlightMode(ctx, &telemetrypb.SubscribeFlightModeRequest{})
		if err != nil {
			sleepOrDone(ctx, px4StreamRetryDelay)
			continue
		}

		for ctx.Err() == nil {
			res, err := stream.Recv()
			if err != nil {
				break
			}

			g.update(events, func(snapshot *TelemetryEvent) {
				snapshot.FlightMode = flightModeString(res.GetFlightMode())
			})
		}

		sleepOrDone(ctx, px4StreamRetryDelay)
	}
}

func (g *MAVSDKGateway) streamArmed(ctx context.Context, events chan TelemetryEvent) {
	for ctx.Err() == nil {
		stream, err := g.telemetry.SubscribeArmed(ctx, &telemetrypb.SubscribeArmedRequest{})
		if err != nil {
			sleepOrDone(ctx, px4StreamRetryDelay)
			continue
		}

		for ctx.Err() == nil {
			res, err := stream.Recv()
			if err != nil {
				break
			}

			g.update(events, func(snapshot *TelemetryEvent) {
				snapshot.Armed = res.GetIsArmed()
			})
		}

		sleepOrDone(ctx, px4StreamRetryDelay)
	}
}

func (g *MAVSDKGateway) streamInAir(ctx context.Context, events chan TelemetryEvent) {
	for ctx.Err() == nil {
		stream, err := g.telemetry.SubscribeInAir(ctx, &telemetrypb.SubscribeInAirRequest{})
		if err != nil {
			sleepOrDone(ctx, px4StreamRetryDelay)
			continue
		}

		for ctx.Err() == nil {
			res, err := stream.Recv()
			if err != nil {
				break
			}

			g.update(events, func(snapshot *TelemetryEvent) {
				snapshot.InAir = res.GetIsInAir()
			})
		}

		sleepOrDone(ctx, px4StreamRetryDelay)
	}
}

func (g *MAVSDKGateway) streamGpsInfo(ctx context.Context, events chan TelemetryEvent) {
	for ctx.Err() == nil {
		stream, err := g.telemetry.SubscribeGpsInfo(ctx, &telemetrypb.SubscribeGpsInfoRequest{})
		if err != nil {
			sleepOrDone(ctx, px4StreamRetryDelay)
			continue
		}

		for ctx.Err() == nil {
			res, err := stream.Recv()
			if err != nil {
				break
			}

			gps := res.GetGpsInfo()
			g.update(events, func(snapshot *TelemetryEvent) {
				snapshot.GPSFix = gpsFixString(gps.GetFixType())
				snapshot.SatellitesVisible = int(gps.GetNumSatellites())
			})
		}

		sleepOrDone(ctx, px4StreamRetryDelay)
	}
}

func (g *MAVSDKGateway) streamHeading(ctx context.Context, events chan TelemetryEvent) {
	for ctx.Err() == nil {
		stream, err := g.telemetry.SubscribeHeading(ctx, &telemetrypb.SubscribeHeadingRequest{})
		if err != nil {
			sleepOrDone(ctx, px4StreamRetryDelay)
			continue
		}

		for ctx.Err() == nil {
			res, err := stream.Recv()
			if err != nil {
				break
			}

			heading := res.GetHeadingDeg().GetHeadingDeg()
			if !finite(heading) {
				continue
			}

			g.update(events, func(snapshot *TelemetryEvent) {
				snapshot.HeadingDeg = heading
			})
		}

		sleepOrDone(ctx, px4StreamRetryDelay)
	}
}

func (g *MAVSDKGateway) streamVelocity(ctx context.Context, events chan TelemetryEvent) {
	for ctx.Err() == nil {
		stream, err := g.telemetry.SubscribeVelocityNed(ctx, &telemetrypb.SubscribeVelocityNedRequest{})
		if err != nil {
			sleepOrDone(ctx, px4StreamRetryDelay)
			continue
		}

		for ctx.Err() == nil {
			res, err := stream.Recv()
			if err != nil {
				break
			}

			velocity := res.GetVelocityNed()
			north := float64(velocity.GetNorthMS())
			east := float64(velocity.GetEastMS())
			if !finite(north) || !finite(east) {
				continue
			}

			g.update(events, func(snapshot *TelemetryEvent) {
				snapshot.GroundSpeedMPS = math.Hypot(north, east)
			})
		}

		sleepOrDone(ctx, px4StreamRetryDelay)
	}
}

func (g *MAVSDKGateway) streamAltitude(ctx context.Context, events chan TelemetryEvent) {
	for ctx.Err() == nil {
		stream, err := g.telemetry.SubscribeAltitude(ctx, &telemetrypb.SubscribeAltitudeRequest{})
		if err != nil {
			sleepOrDone(ctx, px4StreamRetryDelay)
			continue
		}

		for ctx.Err() == nil {
			res, err := stream.Recv()
			if err != nil {
				break
			}

			relativeAltitude := float64(res.GetAltitude().GetAltitudeRelativeM())
			if !finite(relativeAltitude) {
				continue
			}

			g.update(events, func(snapshot *TelemetryEvent) {
				snapshot.RelativeAltitudeM = relativeAltitude
			})
		}

		sleepOrDone(ctx, px4StreamRetryDelay)
	}
}

func (g *MAVSDKGateway) streamHealth(ctx context.Context, events chan TelemetryEvent) {
	for ctx.Err() == nil {
		stream, err := g.telemetry.SubscribeHealth(ctx, &telemetrypb.SubscribeHealthRequest{})
		if err != nil {
			sleepOrDone(ctx, px4StreamRetryDelay)
			continue
		}

		for ctx.Err() == nil {
			res, err := stream.Recv()
			if err != nil {
				break
			}

			g.update(events, func(snapshot *TelemetryEvent) {
				snapshot.HomePositionSet = res.GetHealth().GetIsHomePositionOk()
			})
		}

		sleepOrDone(ctx, px4StreamRetryDelay)
	}
}

func (g *MAVSDKGateway) update(events chan TelemetryEvent, apply func(*TelemetryEvent)) {
	g.mu.Lock()
	apply(&g.snapshot)
	g.snapshot.ObservedAt = time.Now().UTC()
	g.snapshot.Source = px4SourceName
	snapshot := g.snapshot
	ready := g.havePosition && (!g.haveConnection || g.connected)
	g.mu.Unlock()

	if !ready {
		return
	}

	emitTelemetry(events, snapshot)
}

func emitTelemetry(events chan TelemetryEvent, snapshot TelemetryEvent) {
	select {
	case events <- snapshot:
		return
	default:
	}

	select {
	case <-events:
	default:
	}

	select {
	case events <- snapshot:
	default:
	}
}

func sleepOrDone(ctx context.Context, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func actionResultError(operation string, result *actionpb.ActionResult) error {
	if result.GetResult() == actionpb.ActionResult_RESULT_SUCCESS {
		return nil
	}

	if result.GetResultStr() != "" {
		return fmt.Errorf("%s: %s", operation, result.GetResultStr())
	}

	return fmt.Errorf("%s: %s", operation, result.GetResult().String())
}

func missionPlanToMAVSDK(mission MissionPlan) (*missionpb.MissionPlan, error) {
	if len(mission.Waypoints) == 0 {
		return nil, errors.New("mission must include at least one waypoint")
	}

	items := make([]*missionpb.MissionItem, 0, len(mission.Waypoints))
	for _, waypoint := range mission.Waypoints {
		if !finite(waypoint.Latitude) || waypoint.Latitude < -90 || waypoint.Latitude > 90 {
			return nil, fmt.Errorf("waypoint %d latitude must be between -90 and 90", waypoint.Sequence)
		}

		if !finite(waypoint.Longitude) || waypoint.Longitude < -180 || waypoint.Longitude > 180 {
			return nil, fmt.Errorf("waypoint %d longitude must be between -180 and 180", waypoint.Sequence)
		}

		if !finite(waypoint.RelativeAltitudeM) {
			return nil, fmt.Errorf("waypoint %d relative altitude must be finite", waypoint.Sequence)
		}

		item := &missionpb.MissionItem{
			LatitudeDeg:       waypoint.Latitude,
			LongitudeDeg:      waypoint.Longitude,
			RelativeAltitudeM: float32(waypoint.RelativeAltitudeM),
			IsFlyThrough:      false,
			CameraAction:      missionpb.MissionItem_CAMERA_ACTION_NONE,
			VehicleAction:     missionpb.MissionItem_VEHICLE_ACTION_NONE,
			LoiterTimeS:       float32(math.NaN()),
		}

		if waypoint.SpeedMPS != nil {
			if !finite(*waypoint.SpeedMPS) || *waypoint.SpeedMPS <= 0 {
				return nil, fmt.Errorf("waypoint %d speed must be greater than 0 when provided", waypoint.Sequence)
			}
			item.SpeedMS = float32(*waypoint.SpeedMPS)
		} else {
			item.SpeedMS = float32(math.NaN())
		}

		if waypoint.LoiterTimeS != nil {
			if !finite(*waypoint.LoiterTimeS) || *waypoint.LoiterTimeS < 0 {
				return nil, fmt.Errorf("waypoint %d loiter time cannot be negative when provided", waypoint.Sequence)
			}
			item.LoiterTimeS = float32(*waypoint.LoiterTimeS)
		}

		items = append(items, item)
	}

	switch mission.CompletionAction {
	case "", MissionCompletionActionReturnToLaunch, MissionCompletionActionHold:
	case MissionCompletionActionLand:
		items[len(items)-1].VehicleAction = missionpb.MissionItem_VEHICLE_ACTION_LAND
	default:
		return nil, fmt.Errorf("unsupported mission completion action: %s", mission.CompletionAction)
	}

	return &missionpb.MissionPlan{MissionItems: items}, nil
}

func missionProgressFromMAVSDK(progress *missionpb.MissionProgress) MissionProgressEvent {
	current := int(progress.GetCurrent())
	total := int(progress.GetTotal())
	return MissionProgressEvent{
		Current:  current,
		Total:    total,
		Finished: total > 0 && current >= total,
	}
}

func missionResultError(operation string, result *missionpb.MissionResult) error {
	if result.GetResult() == missionpb.MissionResult_RESULT_SUCCESS {
		return nil
	}

	if result.GetResultStr() != "" {
		return fmt.Errorf("%s: %s", operation, result.GetResultStr())
	}

	return fmt.Errorf("%s: %s", operation, result.GetResult().String())
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func clamp(value float64, min float64, max float64) float64 {
	if value < min {
		return min
	}

	if value > max {
		return max
	}

	return value
}

func flightModeString(mode telemetrypb.FlightMode) string {
	switch mode {
	case telemetrypb.FlightMode_FLIGHT_MODE_READY:
		return "READY"
	case telemetrypb.FlightMode_FLIGHT_MODE_TAKEOFF:
		return "TAKEOFF"
	case telemetrypb.FlightMode_FLIGHT_MODE_HOLD:
		return "HOLD"
	case telemetrypb.FlightMode_FLIGHT_MODE_MISSION:
		return "MISSION"
	case telemetrypb.FlightMode_FLIGHT_MODE_RETURN_TO_LAUNCH:
		return "RETURN_TO_LAUNCH"
	case telemetrypb.FlightMode_FLIGHT_MODE_LAND:
		return "LAND"
	case telemetrypb.FlightMode_FLIGHT_MODE_OFFBOARD:
		return "OFFBOARD"
	case telemetrypb.FlightMode_FLIGHT_MODE_FOLLOW_ME:
		return "FOLLOW_ME"
	case telemetrypb.FlightMode_FLIGHT_MODE_MANUAL:
		return "MANUAL"
	case telemetrypb.FlightMode_FLIGHT_MODE_ALTCTL:
		return "ALTCTL"
	case telemetrypb.FlightMode_FLIGHT_MODE_POSCTL:
		return "POSCTL"
	case telemetrypb.FlightMode_FLIGHT_MODE_ACRO:
		return "ACRO"
	case telemetrypb.FlightMode_FLIGHT_MODE_STABILIZED:
		return "STABILIZED"
	case telemetrypb.FlightMode_FLIGHT_MODE_RATTITUDE:
		return "RATTITUDE"
	default:
		return "UNKNOWN"
	}
}

func gpsFixString(fix telemetrypb.FixType) string {
	switch fix {
	case telemetrypb.FixType_FIX_TYPE_NO_GPS:
		return "NO_GPS"
	case telemetrypb.FixType_FIX_TYPE_NO_FIX:
		return "NO_FIX"
	case telemetrypb.FixType_FIX_TYPE_FIX_2D:
		return "2D"
	case telemetrypb.FixType_FIX_TYPE_FIX_3D:
		return "3D"
	case telemetrypb.FixType_FIX_TYPE_FIX_DGPS:
		return "DGPS"
	case telemetrypb.FixType_FIX_TYPE_RTK_FLOAT:
		return "RTK_FLOAT"
	case telemetrypb.FixType_FIX_TYPE_RTK_FIXED:
		return "RTK_FIXED"
	default:
		return "UNKNOWN"
	}
}
