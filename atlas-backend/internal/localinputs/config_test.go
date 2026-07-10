package localinputs

import "testing"

func TestNormalizeVideoRTSPTransportDefaultsToUDP(t *testing.T) {
	if got := normalizeVideoRTSPTransport(""); got != "udp" {
		t.Fatalf("expected empty transport to default to udp, got %q", got)
	}
	if got := normalizeVideoRTSPTransport("invalid"); got != "udp" {
		t.Fatalf("expected invalid transport to default to udp, got %q", got)
	}
}

func TestNormalizeVideoRTSPTransportAcceptsTCPOverride(t *testing.T) {
	if got := normalizeVideoRTSPTransport("tcp"); got != "tcp" {
		t.Fatalf("expected tcp override, got %q", got)
	}
}
