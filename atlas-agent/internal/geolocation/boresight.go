package geolocation

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

const BoresightGroundPlaneMethod = "BORESIGHT_GROUND_PLANE"

type BoresightAimPoint string

const (
	BoresightAimPointGroundContact BoresightAimPoint = "GROUND_CONTACT"
	BoresightAimPointTargetCenter  BoresightAimPoint = "TARGET_CENTER"
)

type BoresightGroundPlaneRequest struct {
	Timing                            VideoFrameTiming
	GimbalID                          int32
	AimPoint                          BoresightAimPoint
	AimPointNormalizedX               float64
	AimPointNormalizedY               float64
	GroundAltitudeAMSLM               float64
	GroundAltitudeUncertaintyM        float64
	GroundPlaneSource                 string
	AssumedAimPointHeightM            float64
	AssumedAimPointHeightUncertaintyM float64
}

type GeodeticPoint struct {
	LatitudeDeg  float64 `json:"latitudeDeg"`
	LongitudeDeg float64 `json:"longitudeDeg"`
	AltitudeM    float64 `json:"altitudeMeters"`
}

type BoresightUncertainty struct {
	AircraftHorizontalM   float64 `json:"aircraftHorizontalMeters"`
	OriginM               float64 `json:"originMeters"`
	TimingM               float64 `json:"timingMeters"`
	AngularM              float64 `json:"angularMeters"`
	AltitudeProjectionM   float64 `json:"altitudeProjectionMeters"`
	HorizontalRadiusM     float64 `json:"horizontalRadiusMeters"`
	StaticAngularBoundDeg float64 `json:"staticAngularBoundDeg"`
	TimingAngularDeg      float64 `json:"timingAngularDeg"`
	TotalAngularBoundDeg  float64 `json:"totalAngularBoundDeg"`
	GroundAltitudeM       float64 `json:"groundAltitudeMeters"`
	AimPointHeightM       float64 `json:"aimPointHeightMeters"`
}

type BoresightAlignmentEvidence struct {
	Status        string  `json:"status"`
	Reference     string  `json:"reference,omitempty"`
	ErrorBoundDeg float64 `json:"errorBoundDeg"`
}

// BoresightGroundPlaneEstimate is an explicitly approximate result. It assumes
// the centred image point lies on the camera/gimbal forward axis, the optical
// centre is colocated with the aircraft navigation position, and the target
// lies on the requested horizontal plane.
type BoresightGroundPlaneEstimate struct {
	Method                            string                     `json:"method"`
	FrameTime                         FrameTime                  `json:"frameTime"`
	Origin                            GeodeticPoint              `json:"origin"`
	Intersection                      GeodeticPoint              `json:"intersection"`
	GroundAltitudeAMSLM               float64                    `json:"groundAltitudeAmslMeters"`
	IntersectionAltitudeAMSLM         float64                    `json:"intersectionAltitudeAmslMeters"`
	GroundPlaneSource                 string                     `json:"groundPlaneSource"`
	AimPoint                          BoresightAimPoint          `json:"aimPoint"`
	AssumedAimPointHeightM            float64                    `json:"assumedAimPointHeightMeters"`
	AssumedAimPointHeightUncertaintyM float64                    `json:"assumedAimPointHeightUncertaintyMeters"`
	AimPointNormalizedX               float64                    `json:"aimPointNormalizedX"`
	AimPointNormalizedY               float64                    `json:"aimPointNormalizedY"`
	CenterErrorX                      float64                    `json:"centerErrorX"`
	CenterErrorY                      float64                    `json:"centerErrorY"`
	CenterTolerance                   float64                    `json:"centerTolerance"`
	GimbalBoresightFRD                Vector3                    `json:"gimbalBoresightFrd"`
	AircraftDirectionFRD              Vector3                    `json:"aircraftDirectionFrd"`
	WorldDirectionNED                 Vector3                    `json:"worldDirectionNed"`
	NorthOffsetM                      float64                    `json:"northOffsetMeters"`
	EastOffsetM                       float64                    `json:"eastOffsetMeters"`
	GroundRangeM                      float64                    `json:"groundRangeMeters"`
	SlantRangeM                       float64                    `json:"slantRangeMeters"`
	DepressionAngleDeg                float64                    `json:"depressionAngleDeg"`
	Uncertainty                       BoresightUncertainty       `json:"uncertainty"`
	BoresightAlignment                BoresightAlignmentEvidence `json:"boresightAlignment"`
	Assumptions                       []string                   `json:"assumptions"`
}

func (foundation *Foundation) BoresightAlignmentStatus() string {
	if strings.TrimSpace(foundation.config.BoresightAlignmentReference) == "" {
		return "unverified"
	}
	return "verified"
}

func (foundation *Foundation) EstimateBoresightGroundPlane(request BoresightGroundPlaneRequest) (BoresightGroundPlaneEstimate, error) {
	if err := request.validate(); err != nil {
		return BoresightGroundPlaneEstimate{}, err
	}
	context, err := foundation.ContextForFrame(request.Timing, &request.GimbalID)
	if err != nil {
		return BoresightGroundPlaneEstimate{}, fmt.Errorf("resolve boresight temporal context: %w", err)
	}
	if context.Gimbal == nil {
		return BoresightGroundPlaneEstimate{}, errors.New("measured gimbal attitude is unavailable")
	}
	if context.FrameTime.Uncertainty > foundation.config.BoresightMaximumTimeUncertainty {
		return BoresightGroundPlaneEstimate{}, fmt.Errorf("frame-time uncertainty %s exceeds boresight limit %s", context.FrameTime.Uncertainty, foundation.config.BoresightMaximumTimeUncertainty)
	}
	if !context.Aircraft.Quality.GlobalPositionOK {
		return BoresightGroundPlaneEstimate{}, errors.New("aircraft global position is not healthy")
	}
	if !context.Aircraft.Quality.VelocityValid {
		return BoresightGroundPlaneEstimate{}, errors.New("aircraft velocity is unavailable for timing uncertainty")
	}

	centerErrorX := request.AimPointNormalizedX - 0.5
	centerErrorY := request.AimPointNormalizedY - 0.5
	if math.Abs(centerErrorX) > foundation.config.BoresightCenterTolerance || math.Abs(centerErrorY) > foundation.config.BoresightCenterTolerance {
		return BoresightGroundPlaneEstimate{}, fmt.Errorf("aim point is not centred within normalized tolerance %.3f", foundation.config.BoresightCenterTolerance)
	}

	worldDirection := directionFromNorthEuler(context.Gimbal.EulerNorthDeg)
	depressionAngleDeg := math.Asin(clamp(worldDirection.Z, -1, 1)) * 180 / math.Pi
	if depressionAngleDeg < foundation.config.BoresightMinimumDepressionDeg {
		return BoresightGroundPlaneEstimate{}, fmt.Errorf("gimbal depression %.2f degrees is below minimum %.2f degrees", depressionAngleDeg, foundation.config.BoresightMinimumDepressionDeg)
	}
	intersectionAltitude := request.GroundAltitudeAMSLM + request.AssumedAimPointHeightM
	verticalDrop := context.Aircraft.AltitudeAMSLM - intersectionAltitude
	if verticalDrop <= 0 {
		return BoresightGroundPlaneEstimate{}, errors.New("intersection plane must be below the aircraft")
	}
	slantRange := verticalDrop / worldDirection.Z
	northOffset := slantRange * worldDirection.X
	eastOffset := slantRange * worldDirection.Y
	groundRange := math.Hypot(northOffset, eastOffset)
	if !finite(slantRange) || slantRange <= 0 || !finite(groundRange) || groundRange > foundation.config.BoresightMaximumGroundRangeM {
		return BoresightGroundPlaneEstimate{}, fmt.Errorf("boresight ground range %.1f metres exceeds bounded limit %.1f metres", groundRange, foundation.config.BoresightMaximumGroundRangeM)
	}
	intersection, err := offsetGeodetic(context.Aircraft.LatitudeDeg, context.Aircraft.LongitudeDeg, intersectionAltitude, northOffset, eastOffset)
	if err != nil {
		return BoresightGroundPlaneEstimate{}, err
	}
	aircraftDirection := worldNEDToBodyFRD(worldDirection, context.Aircraft.RollDeg, context.Aircraft.PitchDeg, context.Aircraft.YawDeg)
	uncertainty, err := foundation.boresightUncertainty(context, request, worldDirection, verticalDrop, groundRange, depressionAngleDeg)
	if err != nil {
		return BoresightGroundPlaneEstimate{}, err
	}
	alignment := BoresightAlignmentEvidence{
		Status: "UNVERIFIED", ErrorBoundDeg: foundation.config.BoresightAngularUncertaintyDeg,
	}
	if reference := strings.TrimSpace(foundation.config.BoresightAlignmentReference); reference != "" {
		alignment.Status = "VERIFIED"
		alignment.Reference = reference
	}
	assumptions := []string{
		"centred aim point equals camera/gimbal boresight",
		"camera optical centre equals aircraft navigation position",
		"intersection surface is a horizontal plane",
	}
	if alignment.Status == "VERIFIED" {
		assumptions = append(assumptions, "camera/gimbal boresight alignment is bounded by the referenced physical verification")
	} else {
		assumptions = append(assumptions, "camera/gimbal boresight alignment has not been physically verified; the static angular bound is not field-accepted")
	}
	return BoresightGroundPlaneEstimate{
		Method: BoresightGroundPlaneMethod, FrameTime: context.FrameTime,
		Origin:       GeodeticPoint{LatitudeDeg: context.Aircraft.LatitudeDeg, LongitudeDeg: context.Aircraft.LongitudeDeg, AltitudeM: context.Aircraft.AltitudeAMSLM},
		Intersection: intersection, GroundAltitudeAMSLM: request.GroundAltitudeAMSLM,
		IntersectionAltitudeAMSLM: intersectionAltitude, GroundPlaneSource: strings.TrimSpace(request.GroundPlaneSource),
		AimPoint: request.AimPoint, AssumedAimPointHeightM: request.AssumedAimPointHeightM,
		AssumedAimPointHeightUncertaintyM: request.AssumedAimPointHeightUncertaintyM,
		AimPointNormalizedX:               request.AimPointNormalizedX, AimPointNormalizedY: request.AimPointNormalizedY,
		CenterErrorX: centerErrorX, CenterErrorY: centerErrorY, CenterTolerance: foundation.config.BoresightCenterTolerance,
		GimbalBoresightFRD: Vector3{X: 1}, AircraftDirectionFRD: aircraftDirection, WorldDirectionNED: worldDirection,
		NorthOffsetM: northOffset, EastOffsetM: eastOffset, GroundRangeM: groundRange, SlantRangeM: slantRange,
		DepressionAngleDeg: depressionAngleDeg, Uncertainty: uncertainty,
		BoresightAlignment: alignment, Assumptions: assumptions,
	}, nil
}

func (request BoresightGroundPlaneRequest) validate() error {
	if request.AimPoint != BoresightAimPointGroundContact && request.AimPoint != BoresightAimPointTargetCenter {
		return errors.New("boresight aim point must be GROUND_CONTACT or TARGET_CENTER")
	}
	if !finite(request.AimPointNormalizedX) || !finite(request.AimPointNormalizedY) || request.AimPointNormalizedX < 0 || request.AimPointNormalizedX > 1 || request.AimPointNormalizedY < 0 || request.AimPointNormalizedY > 1 {
		return errors.New("boresight aim point must be within the normalized image")
	}
	if !finite(request.GroundAltitudeAMSLM) || !finite(request.GroundAltitudeUncertaintyM) || request.GroundAltitudeUncertaintyM < 0 {
		return errors.New("ground altitude and its uncertainty must be finite and uncertainty cannot be negative")
	}
	if strings.TrimSpace(request.GroundPlaneSource) == "" {
		return errors.New("ground-plane source is required")
	}
	if !finite(request.AssumedAimPointHeightM) || request.AssumedAimPointHeightM < 0 {
		return errors.New("assumed aim-point height must be finite and non-negative")
	}
	if !finite(request.AssumedAimPointHeightUncertaintyM) || request.AssumedAimPointHeightUncertaintyM < 0 {
		return errors.New("assumed aim-point-height uncertainty must be finite and non-negative")
	}
	if request.AimPoint == BoresightAimPointGroundContact && (request.AssumedAimPointHeightM != 0 || request.AssumedAimPointHeightUncertaintyM != 0) {
		return errors.New("GROUND_CONTACT aim point cannot have assumed aim-point height or height uncertainty")
	}
	if request.AimPoint == BoresightAimPointTargetCenter && (request.AssumedAimPointHeightM <= 0 || request.AssumedAimPointHeightUncertaintyM <= 0) {
		return errors.New("TARGET_CENTER aim point requires positive assumed aim-point height and uncertainty")
	}
	return nil
}

func directionFromNorthEuler(euler Vector3) Vector3 {
	pitch := euler.Y * math.Pi / 180
	yaw := euler.Z * math.Pi / 180
	cosPitch := math.Cos(pitch)
	return Vector3{X: cosPitch * math.Cos(yaw), Y: cosPitch * math.Sin(yaw), Z: -math.Sin(pitch)}
}

func worldNEDToBodyFRD(world Vector3, rollDeg, pitchDeg, yawDeg float64) Vector3 {
	roll, pitch, yaw := rollDeg*math.Pi/180, pitchDeg*math.Pi/180, yawDeg*math.Pi/180
	cr, sr := math.Cos(roll), math.Sin(roll)
	cp, sp := math.Cos(pitch), math.Sin(pitch)
	cy, sy := math.Cos(yaw), math.Sin(yaw)
	// Transpose of body-FRD to world-NED Rz(yaw)*Ry(pitch)*Rx(roll).
	return Vector3{
		X: cy*cp*world.X + sy*cp*world.Y - sp*world.Z,
		Y: (cy*sp*sr-sy*cr)*world.X + (sy*sp*sr+cy*cr)*world.Y + cp*sr*world.Z,
		Z: (cy*sp*cr+sy*sr)*world.X + (sy*sp*cr-cy*sr)*world.Y + cp*cr*world.Z,
	}
}

func (foundation *Foundation) boresightUncertainty(context TemporalContext, request BoresightGroundPlaneRequest, direction Vector3, verticalDrop, groundRange, depressionAngleDeg float64) (BoresightUncertainty, error) {
	staticAngularRad := foundation.config.BoresightAngularUncertaintyDeg * math.Pi / 180
	timingAngularRad := 0.0
	if context.Gimbal != nil {
		angularRate := math.Sqrt(
			context.Gimbal.AngularVelocity.X*context.Gimbal.AngularVelocity.X +
				context.Gimbal.AngularVelocity.Y*context.Gimbal.AngularVelocity.Y +
				context.Gimbal.AngularVelocity.Z*context.Gimbal.AngularVelocity.Z,
		)
		timingAngularRad = angularRate * context.FrameTime.Uncertainty.Seconds()
	}
	totalAngularRad := staticAngularRad + timingAngularRad
	depressionRad := depressionAngleDeg * math.Pi / 180
	if totalAngularRad >= depressionRad {
		return BoresightUncertainty{}, errors.New("boresight uncertainty cone reaches or crosses the horizon")
	}
	shallowerGroundRange := verticalDrop / math.Tan(depressionRad-totalAngularRad)
	if !finite(shallowerGroundRange) || shallowerGroundRange > foundation.config.BoresightMaximumGroundRangeM {
		return BoresightUncertainty{}, errors.New("boresight uncertainty exceeds the bounded ground-range envelope")
	}
	radialAngularM := max(0, shallowerGroundRange-groundRange)
	crossTrackAngularM := groundRange * math.Tan(totalAngularRad)
	angularM := radialAngularM + crossTrackAngularM
	horizontalSpeed := math.Hypot(context.Aircraft.VelocityNEDMPS.X, context.Aircraft.VelocityNEDMPS.Y)
	verticalSpeedProjection := math.Abs(context.Aircraft.VelocityNEDMPS.Z) * math.Hypot(direction.X, direction.Y) / direction.Z
	timingM := (horizontalSpeed + verticalSpeedProjection) * context.FrameTime.Uncertainty.Seconds()
	verticalUncertainty := context.Aircraft.Quality.VerticalUncertaintyM + request.GroundAltitudeUncertaintyM + request.AssumedAimPointHeightUncertaintyM
	horizontalComponent := math.Hypot(direction.X, direction.Y)
	altitudeProjectionM := verticalUncertainty * horizontalComponent / direction.Z
	radius := context.Aircraft.Quality.HorizontalUncertaintyM + foundation.config.BoresightOriginUncertaintyM + timingM + angularM + altitudeProjectionM
	return BoresightUncertainty{
		AircraftHorizontalM: context.Aircraft.Quality.HorizontalUncertaintyM,
		OriginM:             foundation.config.BoresightOriginUncertaintyM,
		TimingM:             timingM, AngularM: angularM, AltitudeProjectionM: altitudeProjectionM,
		HorizontalRadiusM:     radius,
		StaticAngularBoundDeg: foundation.config.BoresightAngularUncertaintyDeg,
		TimingAngularDeg:      timingAngularRad * 180 / math.Pi,
		TotalAngularBoundDeg:  totalAngularRad * 180 / math.Pi,
		GroundAltitudeM:       request.GroundAltitudeUncertaintyM,
		AimPointHeightM:       request.AssumedAimPointHeightUncertaintyM,
	}, nil
}

func offsetGeodetic(latitudeDeg, longitudeDeg, altitudeM, northM, eastM float64) (GeodeticPoint, error) {
	if !validLatitudeLongitude(latitudeDeg, longitudeDeg) || !finite(altitudeM) || !finite(northM) || !finite(eastM) {
		return GeodeticPoint{}, errors.New("geodetic offset input is invalid")
	}
	const (
		wgs84SemiMajorM          = 6_378_137.0
		wgs84EccentricitySquared = 6.69437999014e-3
	)
	latitude := latitudeDeg * math.Pi / 180
	sinLatitude := math.Sin(latitude)
	denominator := math.Sqrt(1 - wgs84EccentricitySquared*sinLatitude*sinLatitude)
	primeVerticalRadius := wgs84SemiMajorM / denominator
	meridianRadius := wgs84SemiMajorM * (1 - wgs84EccentricitySquared) / (denominator * denominator * denominator)
	cosLatitude := math.Cos(latitude)
	if math.Abs(cosLatitude) < 1e-6 {
		return GeodeticPoint{}, errors.New("boresight local offset is unsupported near the poles")
	}
	latitude += northM / (meridianRadius + altitudeM)
	longitude := longitudeDeg*math.Pi/180 + eastM/((primeVerticalRadius+altitudeM)*cosLatitude)
	result := GeodeticPoint{LatitudeDeg: latitude * 180 / math.Pi, LongitudeDeg: longitude * 180 / math.Pi, AltitudeM: altitudeM}
	if !validLatitudeLongitude(result.LatitudeDeg, result.LongitudeDeg) {
		return GeodeticPoint{}, errors.New("boresight intersection lies outside valid geodetic coordinates")
	}
	return result, nil
}

func clamp(value, minimum, maximum float64) float64 {
	return math.Max(minimum, math.Min(maximum, value))
}
