// Package aircraftprofile loads the small physical payload description selected
// during onboard setup.
package aircraftprofile

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var identifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

type Profile struct {
	ProfileID string   `json:"profileId"`
	Payloads  Payloads `json:"payloads"`
}

type Payloads struct {
	DepthCamera DepthCamera `json:"depthCamera"`
}

type DepthCamera struct {
	DeviceID     string `json:"deviceId"`
	OffsetToBody Offset `json:"offsetToBody"`
}

type Offset struct {
	TranslationM Translation `json:"translationM"`
	RotationWXYZ Rotation    `json:"rotationWXYZ"`
}

type Translation struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

type Rotation struct {
	W float64 `json:"w"`
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

type Source struct {
	Path    string
	Profile Profile
}

func Load(path string) (Profile, error) {
	if !filepath.IsAbs(path) {
		return Profile{}, fmt.Errorf("aircraft profile path must be absolute: %s", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Profile{}, fmt.Errorf("read aircraft profile %s: %w", path, err)
	}
	var profile Profile
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&profile); err != nil {
		return Profile{}, fmt.Errorf("decode aircraft profile %s: %w", path, err)
	}
	if err := profile.Validate(); err != nil {
		return Profile{}, fmt.Errorf("validate aircraft profile %s: %w", path, err)
	}
	return profile, nil
}

func LoadDirectory(directory string) ([]Source, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read aircraft profile directory %s: %w", directory, err)
	}
	sources := make([]Source, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		profile, err := Load(path)
		if err != nil {
			return nil, err
		}
		sources = append(sources, Source{Path: path, Profile: profile})
	}
	sort.Slice(sources, func(left, right int) bool {
		return sources[left].Profile.ProfileID < sources[right].Profile.ProfileID
	})
	if len(sources) == 0 {
		return nil, fmt.Errorf("no aircraft profiles found in %s", directory)
	}
	return sources, nil
}

func Find(sources []Source, profileID string) (Source, error) {
	for _, source := range sources {
		if source.Profile.ProfileID == profileID {
			return source, nil
		}
	}
	return Source{}, fmt.Errorf("aircraft profile %q is not installed", profileID)
}

func (profile Profile) Validate() error {
	if !identifierPattern.MatchString(profile.ProfileID) {
		return errors.New("profileId must be a lowercase identifier")
	}
	camera := profile.Payloads.DepthCamera
	if strings.TrimSpace(camera.DeviceID) == "" {
		return errors.New("payloads.depthCamera.deviceId is required")
	}
	translation := camera.OffsetToBody.TranslationM
	if !finite(translation.X) || !finite(translation.Y) || !finite(translation.Z) {
		return errors.New("depth camera translation must contain finite numbers")
	}
	rotation := camera.OffsetToBody.RotationWXYZ
	if !finite(rotation.W) || !finite(rotation.X) || !finite(rotation.Y) || !finite(rotation.Z) {
		return errors.New("depth camera rotation must contain finite numbers")
	}
	norm := math.Sqrt(
		rotation.W*rotation.W +
			rotation.X*rotation.X +
			rotation.Y*rotation.Y +
			rotation.Z*rotation.Z,
	)
	if math.Abs(norm-1) > 1e-3 {
		return errors.New("depth camera rotation quaternion must be normalized")
	}
	return nil
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
