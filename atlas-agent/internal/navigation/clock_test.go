package navigation

import (
	"testing"
	"time"
)

func TestHostWallClockForwardStepStartsNewAlignmentEpoch(t *testing.T) {
	aligner := clockAligner{}
	base := time.Unix(1_800_000_000, 0)

	first := aligner.alignSample(8_000_000, base.UnixNano(), 0, false)
	steppedWallTime := base.Add(68*time.Second + 100*time.Millisecond)
	second := aligner.alignSample(
		8_100_000,
		steppedWallTime.UnixNano(),
		(100 * time.Millisecond).Nanoseconds(),
		true,
	)
	third := aligner.alignSample(
		8_200_000,
		steppedWallTime.Add(100*time.Millisecond).UnixNano(),
		(100 * time.Millisecond).Nanoseconds(),
		true,
	)

	if first.ClockEpoch != 0 || second.ClockEpoch != 1 || third.ClockEpoch != 1 {
		t.Fatalf("clock epochs = %d, %d, %d", first.ClockEpoch, second.ClockEpoch, third.ClockEpoch)
	}
	if second.AlignedUnixNS != steppedWallTime.UnixNano() || second.AlignmentErrorNS != 0 {
		t.Fatalf("forward-step realignment = %#v", second)
	}
	if third.AlignedUnixNS != steppedWallTime.Add(100*time.Millisecond).UnixNano() || third.AlignmentErrorNS != 0 {
		t.Fatalf("post-step alignment = %#v", third)
	}
}

func TestSubsecondHostWallClockStepStartsNewAlignmentEpoch(t *testing.T) {
	aligner := clockAligner{}
	base := time.Unix(1_800_000_000, 0)

	_ = aligner.alignSample(8_000_000, base.UnixNano(), 0, false)
	steppedWallTime := base.Add(600 * time.Millisecond)
	stepped := aligner.alignSample(
		8_100_000,
		steppedWallTime.UnixNano(),
		(100 * time.Millisecond).Nanoseconds(),
		true,
	)

	if stepped.ClockEpoch != 1 || stepped.AlignedUnixNS != steppedWallTime.UnixNano() {
		t.Fatalf("subsecond-step realignment = %#v", stepped)
	}
}

func TestReceiveDelayDoesNotStartNewAlignmentEpoch(t *testing.T) {
	aligner := clockAligner{}
	base := time.Unix(1_800_000_000, 0)

	first := aligner.alignSample(8_000_000, base.UnixNano(), 0, false)
	delayed := aligner.alignSample(
		8_100_000,
		base.Add(5*time.Second).UnixNano(),
		(5 * time.Second).Nanoseconds(),
		true,
	)

	if first.ClockEpoch != 0 || delayed.ClockEpoch != 0 {
		t.Fatalf("delivery delay changed epoch: first=%d delayed=%d", first.ClockEpoch, delayed.ClockEpoch)
	}
	if delayed.AlignedUnixNS != base.Add(100*time.Millisecond).UnixNano() {
		t.Fatalf("delayed sample was incorrectly made fresh: %#v", delayed)
	}
}
