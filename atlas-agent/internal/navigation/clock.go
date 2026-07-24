package navigation

import "time"

const (
	unixTimestampThresholdUS = uint64(100_000_000_000_000)
	// This compares wall and monotonic clocks sampled on the same host, so
	// transport latency is not part of the difference. Keep the tolerance
	// below the shortest 500 ms navigation freshness deadline while leaving
	// ample room for clock-read jitter and normal NTP slew.
	clockStepTolerance = 250 * time.Millisecond
)

type clockAligner struct {
	epoch              uint64
	lastSourceUS       uint64
	lastReceived       time.Time
	lastReceivedUnixNS int64
	bestOffsetNS       int64
	haveOffset         bool
}

func (aligner *clockAligner) align(sourceUS uint64, received time.Time) ObservationTime {
	elapsedNS, haveElapsed := int64(0), !aligner.lastReceived.IsZero()
	if haveElapsed {
		// time.Sub uses Go's monotonic clock when both values came from
		// time.Now. Unlike wall time, that clock is not stepped by NTP.
		elapsedNS = received.Sub(aligner.lastReceived).Nanoseconds()
	}
	result := aligner.alignSample(sourceUS, received.UnixNano(), elapsedNS, haveElapsed)
	aligner.lastReceived = received
	return result
}

func (aligner *clockAligner) alignSample(sourceUS uint64, receivedNS, elapsedNS int64, haveElapsed bool) ObservationTime {
	if sourceUS >= unixTimestampThresholdUS {
		return ObservationTime{PX4TimeUS: sourceUS, AlignedUnixNS: int64(sourceUS * 1000), ReceivedUnixNS: receivedNS}
	}

	sourceReset := aligner.lastSourceUS > 0 && sourceUS+1_000_000 < aligner.lastSourceUS
	wallStep := false
	if haveElapsed && aligner.lastReceivedUnixNS != 0 {
		wallElapsedNS := receivedNS - aligner.lastReceivedUnixNS
		progressDifferenceNS := wallElapsedNS - elapsedNS
		wallStep = progressDifferenceNS > clockStepTolerance.Nanoseconds() ||
			progressDifferenceNS < -clockStepTolerance.Nanoseconds()
	}
	if sourceReset || wallStep {
		aligner.epoch++
		aligner.haveOffset = false
	}
	aligner.lastSourceUS = sourceUS
	aligner.lastReceivedUnixNS = receivedNS
	sourceNS := int64(sourceUS * 1000)
	offsetNS := receivedNS - sourceNS
	if !aligner.haveOffset || offsetNS < aligner.bestOffsetNS {
		aligner.bestOffsetNS = offsetNS
		aligner.haveOffset = true
	}
	return ObservationTime{
		PX4TimeUS: sourceUS, AlignedUnixNS: sourceNS + aligner.bestOffsetNS,
		ReceivedUnixNS: receivedNS, ClockEpoch: aligner.epoch,
		AlignmentErrorNS: offsetNS - aligner.bestOffsetNS,
	}
}
