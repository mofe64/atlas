package mavlinkobserver

import (
	"context"
	"math"

	"github.com/bluenviron/gomavlib/v4/pkg/dialects/common"
)

type GimbalControlCommand struct {
	PitchRateDegS     float64
	YawRateDegS       float64
	TargetSystemID    uint8
	TargetComponentID uint8
	GimbalDeviceID    uint8
}

func (r *Runtime) SendGimbalControl(ctx context.Context, command GimbalControlCommand) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	targetSystemID := command.TargetSystemID
	if targetSystemID == 0 {
		targetSystemID = 1
	}

	return r.node.WriteMessageAll(&common.MessageGimbalManagerSetPitchyaw{
		TargetSystem:    targetSystemID,
		TargetComponent: command.TargetComponentID,
		Flags:           common.GIMBAL_MANAGER_FLAGS_YAW_IN_VEHICLE_FRAME,
		GimbalDeviceId:  command.GimbalDeviceID,
		Pitch:           float32(math.NaN()),
		Yaw:             float32(math.NaN()),
		PitchRate:       float32(command.PitchRateDegS),
		YawRate:         float32(command.YawRateDegS),
	})
}
