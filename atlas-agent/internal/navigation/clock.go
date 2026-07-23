package navigation

import "time"

const unixTimestampThresholdUS = uint64(100_000_000_000_000)

type clockAligner struct {
	epoch        uint64
	lastSourceUS uint64
	bestOffsetNS int64
	haveOffset   bool
}

func (aligner *clockAligner) align(sourceUS uint64, received time.Time) ObservationTime {
	receivedNS := received.UTC().UnixNano()
	if sourceUS >= unixTimestampThresholdUS {
		return ObservationTime{PX4TimeUS: sourceUS, AlignedUnixNS: int64(sourceUS * 1000), ReceivedUnixNS: receivedNS}
	}
	if aligner.lastSourceUS > 0 && sourceUS+1_000_000 < aligner.lastSourceUS {
		aligner.epoch++
		aligner.haveOffset = false
	}
	aligner.lastSourceUS = sourceUS
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
