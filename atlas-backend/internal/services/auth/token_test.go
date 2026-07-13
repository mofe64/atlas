package authservice

import (
	"bytes"
	"testing"
)

func TestSessionTokenRoundTrip(t *testing.T) {
	raw, storedHash, err := newSessionToken()
	if err != nil {
		t.Fatalf("newSessionToken() error = %v", err)
	}
	parsedHash, err := validateAndHashSessionToken(raw)
	if err != nil {
		t.Fatalf("validateAndHashSessionToken() error = %v", err)
	}
	if !bytes.Equal(storedHash, parsedHash) || len(storedHash) != 32 {
		t.Fatalf("hashes differ or have wrong length: %d", len(storedHash))
	}
	if bytes.Contains(storedHash, []byte(raw)) {
		t.Fatal("stored digest contains raw bearer token")
	}
}

func TestSessionTokenRejectsMalformedInput(t *testing.T) {
	for _, token := range []string{"", "wrong_prefix_value", "atlas_session_short"} {
		if _, err := validateAndHashSessionToken(token); err == nil {
			t.Fatalf("validateAndHashSessionToken(%q) error = nil", token)
		}
	}
}
