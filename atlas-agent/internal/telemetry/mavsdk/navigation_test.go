package mavsdk

import (
	"testing"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/navigation"
)

func TestMavlinkDirectNavigationFieldsAreNormalized(t *testing.T) {
	plane, err := navigation.NewPlane(navigation.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	s := &source{navigation: plane}
	base := time.Unix(1_800_000_000, 0)
	plane.SetConnected(true, base)
	plane.SetLocalPositionValid(true, base)
	inputs := []struct {
		handle func(string, time.Time) error
		raw    string
	}{
		{s.handleLocalPositionNED, `{"time_boot_ms":1000,"x":1,"y":2,"z":-3,"vx":0.1,"vy":0.2,"vz":-0.3}`},
		{s.handleOdometry, `{"time_usec":1000000,"frame_id":18,"child_frame_id":8,"x":1,"y":2,"z":-3,"q":[1,0,0,0],"vx":0.1,"vy":0.2,"vz":-0.3,"reset_counter":2,"quality":90}`},
		{s.handleEstimatorStatus, `{"time_usec":1000000,"flags":79,"vel_ratio":0.1,"pos_horiz_ratio":0.2,"pos_vert_ratio":0.3,"hagl_ratio":0.4}`},
		{s.handleOpticalFlow, `{"time_usec":1000000,"sensor_id":3,"integration_time_us":14285,"integrated_x":0.01,"integrated_y":-0.02,"quality":200,"distance":1.25}`},
		{s.handleDistanceSensor, `{"time_boot_ms":1000,"min_distance":8,"max_distance":3000,"current_distance":125,"id":4,"orientation":25,"signal_quality":80}`},
	}
	for _, input := range inputs {
		if err := input.handle(input.raw, base); err != nil {
			t.Fatal(err)
		}
	}
	state := plane.Latest(base.Add(10 * time.Millisecond))
	if state.Status != navigation.StatusReady || state.LocalPosition.Position.Z != -3 || state.Odometry.FrameID != 18 || state.OpticalFlow.SensorID != 3 || state.OpticalFlow.Quality != 200 || state.Range.CurrentM != 1.25 {
		t.Fatalf("normalized navigation state = %#v", state)
	}
}

func TestInvalidMavlinkDirectJSONIsRejected(t *testing.T) {
	plane, _ := navigation.NewPlane(navigation.DefaultConfig())
	s := &source{navigation: plane}
	if err := s.handleOpticalFlow(`{"time_usec":`, time.Now()); err == nil {
		t.Fatal("invalid JSON was accepted")
	}
}
