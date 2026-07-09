package comms

import (
	"sync"
	"time"
)

const (
	StateConnecting   = "connecting"
	StateConnected    = "connected"
	StateDisconnected = "disconnected"
)

type BackendChannelSnapshot struct {
	State                string
	ReconnectCount       uint64
	ConnectedAt          time.Time
	LastDisconnectedAt   time.Time
	LastSuccessfulSendAt time.Time
	LastHeartbeatSentAt  time.Time
	LastError            string
	BackendAddress       string
	WeakLink             bool
	WeakLinkReason       string
}

type BackendChannelManager struct {
	mu                   sync.RWMutex
	backendAddress       string
	state                string
	reconnectCount       uint64
	connectedAt          time.Time
	lastDisconnectedAt   time.Time
	lastSuccessfulSendAt time.Time
	lastHeartbeatSentAt  time.Time
	lastError            string
}

func NewBackendChannelManager(backendAddress string) *BackendChannelManager {
	return &BackendChannelManager{
		backendAddress: backendAddress,
		state:          StateDisconnected,
	}
}

func (m *BackendChannelManager) MarkConnecting(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.state = StateConnecting
}

func (m *BackendChannelManager) MarkConnected(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.state = StateConnected
	m.connectedAt = now.UTC()
	m.lastError = ""
}

func (m *BackendChannelManager) MarkDisconnected(now time.Time, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.state = StateDisconnected
	m.lastDisconnectedAt = now.UTC()
	m.reconnectCount++
	if err != nil {
		m.lastError = err.Error()
	}
}

func (m *BackendChannelManager) RecordSend(now time.Time, heartbeat bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	utc := now.UTC()
	m.lastSuccessfulSendAt = utc
	if heartbeat {
		m.lastHeartbeatSentAt = utc
	}
}

func (m *BackendChannelManager) RecordSendFailure(now time.Time, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.state = StateDisconnected
	m.lastDisconnectedAt = now.UTC()
	if err != nil {
		m.lastError = err.Error()
	}
}

func (m *BackendChannelManager) Snapshot() BackendChannelSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	state := m.state
	if state == "" {
		state = StateDisconnected
	}

	snapshot := BackendChannelSnapshot{
		State:                state,
		ReconnectCount:       m.reconnectCount,
		ConnectedAt:          m.connectedAt,
		LastDisconnectedAt:   m.lastDisconnectedAt,
		LastSuccessfulSendAt: m.lastSuccessfulSendAt,
		LastHeartbeatSentAt:  m.lastHeartbeatSentAt,
		LastError:            m.lastError,
		BackendAddress:       m.backendAddress,
	}

	if state != StateConnected {
		snapshot.WeakLink = true
		snapshot.WeakLinkReason = "backend channel is not connected"
	}
	if m.lastError != "" {
		snapshot.WeakLink = true
		snapshot.WeakLinkReason = m.lastError
	}

	return snapshot
}
