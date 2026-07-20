package geolocation

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// CompanionTime carries both local clocks at one sampling instant. Unix time
// is useful for durable/operator timestamps; monotonic time is the only safe
// interpolation axis because wall time can be stepped by NTP.
type CompanionTime struct {
	MonotonicNS int64
	UnixNS      int64
}

// Now samples CLOCK_MONOTONIC and wall time close together. CLOCK_MONOTONIC is
// shared by Agent and the colocated Python inference process on Linux, unlike
// process-relative elapsed timers.
func Now() CompanionTime {
	var monotonic unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &monotonic); err != nil {
		panic(fmt.Sprintf("read companion monotonic clock: %v", err))
	}
	return CompanionTime{MonotonicNS: monotonic.Nano(), UnixNS: time.Now().UTC().UnixNano()}
}

type clockAnchor struct {
	remoteNS    int64
	companionNS int64
}

const clockProgressDiscontinuity = 2 * time.Second

// offsetCorrelator maps a same-rate remote clock into companion monotonic time.
// It intentionally uses the minimum observed receive offset: transport delay
// can make a sample late but cannot make it arrive before it was produced.
type offsetCorrelator struct {
	capacity      int
	epoch         uint64
	anchors       []clockAnchor
	lastRemoteNS  int64
	lastCompanion int64
}

func newOffsetCorrelator(capacity int) *offsetCorrelator {
	return &offsetCorrelator{capacity: capacity, epoch: 1}
}

func (correlator *offsetCorrelator) observe(remoteNS, companionNS int64) (reset bool) {
	progressMismatch := int64(0)
	if len(correlator.anchors) > 0 {
		progressMismatch = (remoteNS - correlator.lastRemoteNS) - (companionNS - correlator.lastCompanion)
	}
	if len(correlator.anchors) > 0 && (remoteNS < correlator.lastRemoteNS || companionNS < correlator.lastCompanion ||
		progressMismatch > int64(clockProgressDiscontinuity) || progressMismatch < -int64(clockProgressDiscontinuity)) {
		correlator.epoch++
		correlator.anchors = correlator.anchors[:0]
		reset = true
	}
	correlator.anchors = append(correlator.anchors, clockAnchor{remoteNS: remoteNS, companionNS: companionNS})
	if len(correlator.anchors) > correlator.capacity {
		copy(correlator.anchors, correlator.anchors[len(correlator.anchors)-correlator.capacity:])
		correlator.anchors = correlator.anchors[:correlator.capacity]
	}
	correlator.lastRemoteNS = remoteNS
	correlator.lastCompanion = companionNS
	return reset
}

func (correlator *offsetCorrelator) resolve(remoteNS int64) (int64, time.Duration, bool) {
	minimumOffset, maximumOffset, ok := correlator.offsetRange()
	if !ok {
		return 0, 0, false
	}
	return remoteNS + minimumOffset, durationFromPositiveNS(maximumOffset - minimumOffset), true
}

func (correlator *offsetCorrelator) remoteAt(companionNS int64) (int64, time.Duration, bool) {
	minimumOffset, maximumOffset, ok := correlator.offsetRange()
	if !ok {
		return 0, 0, false
	}
	return companionNS - minimumOffset, durationFromPositiveNS(maximumOffset - minimumOffset), true
}

func (correlator *offsetCorrelator) offsetRange() (int64, int64, bool) {
	if len(correlator.anchors) == 0 {
		return 0, 0, false
	}
	minimum := correlator.anchors[0].companionNS - correlator.anchors[0].remoteNS
	maximum := minimum
	for _, anchor := range correlator.anchors[1:] {
		offset := anchor.companionNS - anchor.remoteNS
		minimum = min(minimum, offset)
		maximum = max(maximum, offset)
	}
	return minimum, maximum, true
}

func durationFromPositiveNS(value int64) time.Duration {
	if value <= 0 {
		return 0
	}
	return time.Duration(value)
}

func (correlator *offsetCorrelator) health(domain string) ClockHealth {
	_, uncertainty, ready := correlator.resolve(correlator.lastRemoteNS)
	return ClockHealth{
		Domain: domain, Epoch: correlator.epoch, AnchorCount: len(correlator.anchors),
		Ready: ready, Uncertainty: uncertainty,
	}
}
