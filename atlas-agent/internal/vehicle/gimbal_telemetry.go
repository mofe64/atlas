package vehicle

import (
	"context"
	"errors"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/geolocation"
	gimbalpb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/gimbal"
)

const gimbalTelemetryRetryDelay = 2 * time.Second

// StartGimbalAttitudeRecording subscribes to measured Gimbal v2 attitude. It
// is observational only: it never acquires payload control and therefore does
// not interfere with mission, manual, or follow-controller ownership.
func (p *PayloadController) StartGimbalAttitudeRecording(ctx context.Context, foundation *geolocation.Foundation) error {
	if foundation == nil {
		return errors.New("geolocation foundation is required")
	}
	go p.recordGimbalAttitudes(ctx, foundation)
	return nil
}

func (p *PayloadController) recordGimbalAttitudes(ctx context.Context, foundation *geolocation.Foundation) {
	for ctx.Err() == nil {
		stream, err := p.gimbal.SubscribeAttitude(ctx, &gimbalpb.SubscribeAttitudeRequest{})
		if err != nil {
			p.logger.Debug("MAVSDK gimbal attitude stream unavailable", "error", err)
			gimbalTelemetrySleep(ctx)
			continue
		}
		for ctx.Err() == nil {
			response, err := stream.Recv()
			if err != nil {
				break
			}
			if err := recordMeasuredGimbalAttitude(foundation, response.GetAttitude(), geolocation.Now()); err != nil {
				p.logger.Debug("discard invalid measured gimbal attitude", "error", err)
			}
		}
		gimbalTelemetrySleep(ctx)
	}
}

func recordMeasuredGimbalAttitude(foundation *geolocation.Foundation, attitude *gimbalpb.Attitude, received geolocation.CompanionTime) error {
	if attitude == nil {
		return errors.New("gimbal attitude is missing")
	}
	forwardEuler := attitude.GetEulerAngleForward()
	northEuler := attitude.GetEulerAngleNorth()
	forwardQuaternion := attitude.GetQuaternionForward()
	northQuaternion := attitude.GetQuaternionNorth()
	angularVelocity := attitude.GetAngularVelocity()
	if forwardEuler == nil || northEuler == nil || forwardQuaternion == nil || northQuaternion == nil || angularVelocity == nil {
		return errors.New("gimbal attitude is incomplete")
	}
	return foundation.RecordGimbalAttitude(geolocation.GimbalAttitudeMeasurement{
		GimbalID: attitude.GetGimbalId(), GimbalTimestampUS: attitude.GetTimestampUs(), Received: received,
		EulerForwardDeg: geolocation.Vector3{X: float64(forwardEuler.GetRollDeg()), Y: float64(forwardEuler.GetPitchDeg()), Z: float64(forwardEuler.GetYawDeg())},
		EulerNorthDeg:   geolocation.Vector3{X: float64(northEuler.GetRollDeg()), Y: float64(northEuler.GetPitchDeg()), Z: float64(northEuler.GetYawDeg())},
		QuaternionForward: geolocation.Quaternion{
			W: float64(forwardQuaternion.GetW()), X: float64(forwardQuaternion.GetX()),
			Y: float64(forwardQuaternion.GetY()), Z: float64(forwardQuaternion.GetZ()),
		},
		QuaternionNorth: geolocation.Quaternion{
			W: float64(northQuaternion.GetW()), X: float64(northQuaternion.GetX()),
			Y: float64(northQuaternion.GetY()), Z: float64(northQuaternion.GetZ()),
		},
		AngularVelocity: geolocation.Vector3{
			X: float64(angularVelocity.GetRollRadS()), Y: float64(angularVelocity.GetPitchRadS()), Z: float64(angularVelocity.GetYawRadS()),
		},
	})
}

func gimbalTelemetrySleep(ctx context.Context) {
	timer := time.NewTimer(gimbalTelemetryRetryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
