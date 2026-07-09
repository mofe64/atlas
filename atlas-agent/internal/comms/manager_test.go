package comms

import (
	"errors"
	"testing"
	"time"
)

func TestBackendChannelManagerRecordsReconnectsAndSends(t *testing.T) {
	manager := NewBackendChannelManager("127.0.0.1:8080")
	connectedAt := time.Date(2026, 7, 9, 10, 0, 0, 0, time.UTC)
	sendAt := connectedAt.Add(2 * time.Second)
	disconnectedAt := connectedAt.Add(5 * time.Second)

	manager.MarkConnecting(connectedAt.Add(-time.Second))
	manager.MarkConnected(connectedAt)
	manager.RecordSend(sendAt, true)
	manager.MarkDisconnected(disconnectedAt, errors.New("transport closed"))

	snapshot := manager.Snapshot()
	if snapshot.State != StateDisconnected {
		t.Fatalf("expected disconnected state, got %q", snapshot.State)
	}
	if snapshot.ReconnectCount != 1 {
		t.Fatalf("expected one reconnect, got %d", snapshot.ReconnectCount)
	}
	if snapshot.ConnectedAt != connectedAt {
		t.Fatalf("expected connected timestamp %s, got %s", connectedAt, snapshot.ConnectedAt)
	}
	if snapshot.LastSuccessfulSendAt != sendAt {
		t.Fatalf("expected last send timestamp %s, got %s", sendAt, snapshot.LastSuccessfulSendAt)
	}
	if snapshot.LastHeartbeatSentAt != sendAt {
		t.Fatalf("expected heartbeat timestamp %s, got %s", sendAt, snapshot.LastHeartbeatSentAt)
	}
	if snapshot.LastDisconnectedAt != disconnectedAt {
		t.Fatalf("expected disconnect timestamp %s, got %s", disconnectedAt, snapshot.LastDisconnectedAt)
	}
	if !snapshot.WeakLink {
		t.Fatal("expected disconnected channel to be marked weak")
	}
	if snapshot.WeakLinkReason != "transport closed" {
		t.Fatalf("expected transport error as weak-link reason, got %q", snapshot.WeakLinkReason)
	}
}

func TestBackendChannelManagerClearsErrorWhenConnected(t *testing.T) {
	manager := NewBackendChannelManager("127.0.0.1:8080")
	manager.MarkDisconnected(time.Now(), errors.New("dial failed"))
	manager.MarkConnected(time.Now())

	snapshot := manager.Snapshot()
	if snapshot.State != StateConnected {
		t.Fatalf("expected connected state, got %q", snapshot.State)
	}
	if snapshot.WeakLink {
		t.Fatal("expected connected channel to be healthy")
	}
	if snapshot.LastError != "" {
		t.Fatalf("expected last error to clear after connect, got %q", snapshot.LastError)
	}
}
