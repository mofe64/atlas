package localinputs

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

func TestHandleOfferAcceptsBrowserStyleVideoOffer(t *testing.T) {
	offerSDP := newBrowserStyleVideoOffer(t)
	service := NewVideoService(Config{
		Enabled:      true,
		SourceID:     "hm30-local",
		VideoRTSPURL: "rtsp://127.0.0.1:1/atlas",
	})
	t.Cleanup(func() {
		service.mu.RLock()
		cancels := make([]context.CancelFunc, 0, len(service.sessions))
		for _, cancel := range service.sessions {
			cancels = append(cancels, cancel)
		}
		service.mu.RUnlock()

		for _, cancel := range cancels {
			cancel()
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	answer, err := service.HandleOffer(ctx, VideoOffer{
		Type: "offer",
		SDP:  offerSDP,
	})
	if err != nil {
		t.Fatalf("handle browser-style video offer: %v", err)
	}
	if answer.Type != "answer" {
		t.Fatalf("expected answer type, got %q", answer.Type)
	}
	if strings.TrimSpace(answer.SDP) == "" {
		t.Fatal("expected non-empty answer SDP")
	}
}

func newBrowserStyleVideoOffer(t *testing.T) string {
	t.Helper()

	peerConnection, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("create offer peer connection: %v", err)
	}
	t.Cleanup(func() {
		if err := peerConnection.Close(); err != nil {
			t.Fatalf("close offer peer connection: %v", err)
		}
	})

	if _, err := peerConnection.AddTransceiverFromKind(
		webrtc.RTPCodecTypeVideo,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly},
	); err != nil {
		t.Fatalf("add recv-only video transceiver: %v", err)
	}

	offer, err := peerConnection.CreateOffer(nil)
	if err != nil {
		t.Fatalf("create video offer: %v", err)
	}

	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)
	if err := peerConnection.SetLocalDescription(offer); err != nil {
		t.Fatalf("set offer local description: %v", err)
	}
	<-gatherComplete

	localDescription := peerConnection.LocalDescription()
	if localDescription == nil || strings.TrimSpace(localDescription.SDP) == "" {
		t.Fatal("expected non-empty local offer SDP")
	}
	return localDescription.SDP
}
