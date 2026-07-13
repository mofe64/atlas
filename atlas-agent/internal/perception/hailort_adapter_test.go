package perception

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

func TestStartHailoRTAdapterRejectsMissingExecutable(t *testing.T) {
	err := StartHailoRTAdapter(
		context.Background(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		filepath.Join(t.TempDir(), "missing-adapter"),
		"/tmp/atlas-perception.sock",
	)
	if err == nil {
		t.Fatal("StartHailoRTAdapter() error = nil, want missing executable error")
	}
}
