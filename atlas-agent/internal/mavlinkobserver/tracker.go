package mavlinkobserver

import (
	"context"
	"sort"
	"sync"
	"time"
)

const (
	defaultAckHistoryLimit = 256

	mavResultAccepted   uint8 = 0
	mavResultInProgress uint8 = 5
)

type CommandAckMatch struct {
	ActionID           string
	ActionType         string
	Command            uint16
	EarliestObservedAt time.Time
	LatestObservedAt   time.Time
	SourceSystemID     uint8
	SourceComponentID  uint8
	TargetSystemID     uint8
	TargetComponentID  uint8
	FinalOnly          bool
}

type CommandAckEvidence struct {
	ObservedAt        time.Time
	SourceSystemID    uint8
	SourceComponentID uint8
	Command           uint16
	Result            uint8
	Progress          *uint8
	ResultParam2      *int32
	TargetSystem      *uint8
	TargetComponent   *uint8
	ResultLabel       string
	MatchStatus       string
}

func (e CommandAckEvidence) Accepted() bool {
	return e.Result == mavResultAccepted
}

func (e CommandAckEvidence) RawAckCode() string {
	if e.ResultLabel != "" {
		return e.ResultLabel
	}
	return MAVResultLabel(e.Result)
}

type Diagnostics struct {
	Connected             bool
	PacketsSeen           uint64
	LastPacketAt          time.Time
	LastHeartbeatAt       time.Time
	LastCommandAckAt      time.Time
	LastCommandAckCommand uint16
	LastCommandAckResult  uint8
	Components            []ComponentDiagnostics
}

type ComponentDiagnostics struct {
	SystemID    uint8
	ComponentID uint8
	FirstSeenAt time.Time
	LastSeenAt  time.Time
	PacketCount uint64
}

type Tracker struct {
	mu sync.Mutex

	connected bool

	packetsSeen           uint64
	lastPacketAt          time.Time
	lastHeartbeatAt       time.Time
	lastCommandAckAt      time.Time
	lastCommandAckCommand uint16
	lastCommandAckResult  uint8

	components map[componentKey]*ComponentDiagnostics
	ackHistory []CommandAckEvidence
	waiters    map[int]ackWaiter
	nextWaiter int

	autopilotComponent componentKey
	hasAutopilot       bool
	historyLimit       int
}

type componentKey struct {
	systemID    uint8
	componentID uint8
}

type ackWaiter struct {
	match CommandAckMatch
	ch    chan CommandAckEvidence
}

func NewTracker() *Tracker {
	return &Tracker{
		components:   make(map[componentKey]*ComponentDiagnostics),
		waiters:      make(map[int]ackWaiter),
		historyLimit: defaultAckHistoryLimit,
	}
}

func (t *Tracker) MarkConnected(now time.Time) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.connected = true
}

func (t *Tracker) MarkDisconnected(now time.Time, _ string) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.connected = false
}

func (t *Tracker) HandleObservation(_ context.Context, observation Observation) {
	if observation.ObservedAt.IsZero() {
		observation.ObservedAt = time.Now().UTC()
	}
	observation.ObservedAt = observation.ObservedAt.UTC()

	t.mu.Lock()
	defer t.mu.Unlock()

	t.packetsSeen++
	t.lastPacketAt = observation.ObservedAt
	t.recordComponentLocked(observation)

	switch observation.Kind {
	case ObservationHeartbeat:
		t.lastHeartbeatAt = observation.ObservedAt
		if isAutopilotHeartbeat(observation) {
			t.autopilotComponent = componentKey{
				systemID:    observation.SystemID,
				componentID: observation.ComponentID,
			}
			t.hasAutopilot = true
		}
	case ObservationCommandAck:
		ack := evidenceFromObservation(observation)
		if ack.Command == 0 && ack.Result == 0 && observation.CommandAck == nil {
			return
		}
		t.lastCommandAckAt = ack.ObservedAt
		t.lastCommandAckCommand = ack.Command
		t.lastCommandAckResult = ack.Result
		t.appendAckLocked(ack)
		t.notifyAckWaitersLocked(ack)
	}
}

func (t *Tracker) WaitForCommandAck(ctx context.Context, match CommandAckMatch) (CommandAckEvidence, bool) {
	if match.Command == 0 {
		return CommandAckEvidence{}, false
	}
	if match.EarliestObservedAt.IsZero() {
		match.EarliestObservedAt = time.Now().UTC()
	}

	t.mu.Lock()
	if ack, ok := t.matchingAckLocked(match); ok {
		t.mu.Unlock()
		return ack, true
	}

	waiterID := t.nextWaiter
	t.nextWaiter++
	ch := make(chan CommandAckEvidence, 1)
	t.waiters[waiterID] = ackWaiter{match: match, ch: ch}
	t.mu.Unlock()

	select {
	case <-ctx.Done():
		t.mu.Lock()
		delete(t.waiters, waiterID)
		t.mu.Unlock()
		return CommandAckEvidence{}, false
	case ack := <-ch:
		return ack, true
	}
}

func (t *Tracker) PreferredCommandAckSource() (uint8, uint8, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.hasAutopilot {
		return 0, 0, false
	}
	return t.autopilotComponent.systemID, t.autopilotComponent.componentID, true
}

func (t *Tracker) SnapshotDiagnostics() Diagnostics {
	t.mu.Lock()
	defer t.mu.Unlock()

	components := make([]ComponentDiagnostics, 0, len(t.components))
	for _, component := range t.components {
		components = append(components, *component)
	}
	sort.Slice(components, func(i, j int) bool {
		if components[i].SystemID == components[j].SystemID {
			return components[i].ComponentID < components[j].ComponentID
		}
		return components[i].SystemID < components[j].SystemID
	})

	return Diagnostics{
		Connected:             t.connected,
		PacketsSeen:           t.packetsSeen,
		LastPacketAt:          t.lastPacketAt,
		LastHeartbeatAt:       t.lastHeartbeatAt,
		LastCommandAckAt:      t.lastCommandAckAt,
		LastCommandAckCommand: t.lastCommandAckCommand,
		LastCommandAckResult:  t.lastCommandAckResult,
		Components:            components,
	}
}

func (t *Tracker) recordComponentLocked(observation Observation) {
	key := componentKey{systemID: observation.SystemID, componentID: observation.ComponentID}
	component := t.components[key]
	if component == nil {
		component = &ComponentDiagnostics{
			SystemID:    observation.SystemID,
			ComponentID: observation.ComponentID,
			FirstSeenAt: observation.ObservedAt,
		}
		t.components[key] = component
	}
	component.LastSeenAt = observation.ObservedAt
	component.PacketCount++
}

func (t *Tracker) appendAckLocked(ack CommandAckEvidence) {
	t.ackHistory = append(t.ackHistory, ack)
	if limit := t.historyLimit; limit > 0 && len(t.ackHistory) > limit {
		t.ackHistory = append([]CommandAckEvidence(nil), t.ackHistory[len(t.ackHistory)-limit:]...)
	}
}

func (t *Tracker) notifyAckWaitersLocked(ack CommandAckEvidence) {
	for id, waiter := range t.waiters {
		if !commandAckMatches(waiter.match, ack) {
			continue
		}
		delete(t.waiters, id)
		waiter.ch <- withMatchStatus(ack, waiter.match)
	}
}

func (t *Tracker) matchingAckLocked(match CommandAckMatch) (CommandAckEvidence, bool) {
	for i := len(t.ackHistory) - 1; i >= 0; i-- {
		ack := t.ackHistory[i]
		if commandAckMatches(match, ack) {
			return withMatchStatus(ack, match), true
		}
	}
	return CommandAckEvidence{}, false
}

func commandAckMatches(match CommandAckMatch, ack CommandAckEvidence) bool {
	if match.Command != 0 && ack.Command != match.Command {
		return false
	}
	if !match.EarliestObservedAt.IsZero() && ack.ObservedAt.Before(match.EarliestObservedAt) {
		return false
	}
	if !match.LatestObservedAt.IsZero() && ack.ObservedAt.After(match.LatestObservedAt) {
		return false
	}
	if match.SourceSystemID != 0 && ack.SourceSystemID != match.SourceSystemID {
		return false
	}
	if match.SourceComponentID != 0 && ack.SourceComponentID != match.SourceComponentID {
		return false
	}
	if match.TargetSystemID != 0 && (ack.TargetSystem == nil || *ack.TargetSystem != match.TargetSystemID) {
		return false
	}
	if match.TargetComponentID != 0 && (ack.TargetComponent == nil || *ack.TargetComponent != match.TargetComponentID) {
		return false
	}
	if match.FinalOnly && ack.Result == mavResultInProgress {
		return false
	}
	return true
}

func withMatchStatus(ack CommandAckEvidence, match CommandAckMatch) CommandAckEvidence {
	ack.MatchStatus = "matched_command_time_active_action"
	if match.SourceSystemID != 0 || match.SourceComponentID != 0 {
		ack.MatchStatus += "_source"
	}
	if match.TargetSystemID != 0 || match.TargetComponentID != 0 || ack.TargetSystem != nil || ack.TargetComponent != nil {
		ack.MatchStatus += "_target"
	}
	return ack
}

func evidenceFromObservation(observation Observation) CommandAckEvidence {
	ack := observation.CommandAck
	if ack == nil {
		return CommandAckEvidence{}
	}
	return CommandAckEvidence{
		ObservedAt:        observation.ObservedAt,
		SourceSystemID:    observation.SystemID,
		SourceComponentID: observation.ComponentID,
		Command:           ack.Command,
		Result:            ack.Result,
		Progress:          ack.Progress,
		ResultParam2:      ack.ResultParam2,
		TargetSystem:      ack.TargetSystem,
		TargetComponent:   ack.TargetComponent,
		ResultLabel:       MAVResultLabel(ack.Result),
	}
}

func isAutopilotHeartbeat(observation Observation) bool {
	if observation.Heartbeat == nil {
		return false
	}
	// MAV_AUTOPILOT_INVALID is 8. GCS and camera components often heartbeat too;
	// this keeps the preferred ACK source pointed at the flight controller.
	return observation.Heartbeat.Autopilot != 0 && observation.Heartbeat.Autopilot != 8
}

func MAVResultLabel(result uint8) string {
	switch result {
	case 0:
		return "MAV_RESULT_ACCEPTED"
	case 1:
		return "MAV_RESULT_TEMPORARILY_REJECTED"
	case 2:
		return "MAV_RESULT_DENIED"
	case 3:
		return "MAV_RESULT_UNSUPPORTED"
	case 4:
		return "MAV_RESULT_FAILED"
	case 5:
		return "MAV_RESULT_IN_PROGRESS"
	case 6:
		return "MAV_RESULT_CANCELLED"
	case 7:
		return "MAV_RESULT_COMMAND_LONG_ONLY"
	case 8:
		return "MAV_RESULT_COMMAND_INT_ONLY"
	case 9:
		return "MAV_RESULT_COMMAND_UNSUPPORTED_MAV_FRAME"
	default:
		return "MAV_RESULT_UNKNOWN"
	}
}
