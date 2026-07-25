package aircraftprofile

import (
	"path/filepath"
	"testing"
)

func TestTrackedAriadneProfileIsValid(t *testing.T) {
	path, err := filepath.Abs(filepath.Join("..", "..", "..", "aircraft-profiles", "ariadne.json"))
	if err != nil {
		t.Fatal(err)
	}
	profile, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if profile.ProfileID != "ariadne" ||
		profile.Payloads.DepthCamera.DeviceID != "19443010F122147E00" ||
		profile.Payloads.DepthCamera.OffsetToBody.TranslationM.X != 0.155 {
		t.Fatalf("unexpected Ariadne profile: %#v", profile)
	}
}

func TestProfileRejectsMissingIdentityAndInvalidOffset(t *testing.T) {
	profile := validProfile()
	profile.Payloads.DepthCamera.DeviceID = ""
	if err := profile.Validate(); err == nil {
		t.Fatal("accepted profile without stable device identity")
	}
	profile = validProfile()
	profile.Payloads.DepthCamera.OffsetToBody.RotationWXYZ = Rotation{}
	if err := profile.Validate(); err == nil {
		t.Fatal("accepted non-normalized camera rotation")
	}
}

func validProfile() Profile {
	return Profile{
		ProfileID: "test-aircraft",
		Payloads: Payloads{
			DepthCamera: DepthCamera{
				DeviceID: "device-1",
				OffsetToBody: Offset{
					TranslationM: Translation{},
					RotationWXYZ: Rotation{W: 1},
				},
			},
		},
	}
}
