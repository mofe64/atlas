package vehicle

import (
	"testing"

	missionpb "github.com/sunnyside/atlas/atlas-agent/internal/mavsdkpb/mission"
)

func TestMissionProgressFromMAVSDKMarksFinishedAtTotal(t *testing.T) {
	progress := missionProgressFromMAVSDK(&missionpb.MissionProgress{
		Current: 6,
		Total:   6,
	})

	if progress.Current != 6 || progress.Total != 6 {
		t.Fatalf("expected progress 6/6, got %d/%d", progress.Current, progress.Total)
	}

	if !progress.Finished {
		t.Fatal("expected progress to be finished when current reaches total")
	}
}
