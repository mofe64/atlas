package main

import (
	"testing"
	"time"
)

func TestNextBackoffCapsAtMax(t *testing.T) {
	if got := nextBackoff(time.Second, 30*time.Second); got != 2*time.Second {
		t.Fatalf("expected 2s, got %s", got)
	}

	if got := nextBackoff(20*time.Second, 30*time.Second); got != 30*time.Second {
		t.Fatalf("expected cap at 30s, got %s", got)
	}
}
