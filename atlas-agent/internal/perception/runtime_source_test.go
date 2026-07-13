package perception

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeSourceTranslatesVersionedMessagesAndCleansUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// Darwin limits Unix-domain socket paths to roughly 104 bytes, while
	// testing.T.TempDir can exceed that before the file name is appended.
	tempDirectory, err := os.MkdirTemp("/tmp", "atlas-perception-")
	if err != nil {
		t.Fatalf("create short runtime directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(tempDirectory) })
	socketPath := filepath.Join(tempDirectory, "runtime.sock")
	outputs, err := StartRuntimeSource(
		ctx,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		socketPath,
	)
	if err != nil {
		t.Fatalf("StartRuntimeSource() error = %v", err)
	}
	connection, err := net.Dial("unix", socketPath)
	if err != nil {
		cancel()
		t.Fatalf("dial runtime socket: %v", err)
	}
	now := time.Now().UTC()
	encoder := json.NewEncoder(connection)
	if err := encoder.Encode(runtimeEnvelope{
		ProtocolVersion: RuntimeProtocolVersion,
		Type:            "frame",
		Frame: &Frame{
			SourceID: "a8-main", StreamEpoch: "epoch-1", FrameID: "frame-1",
			ObservedAt: now, ImageWidth: 1920, ImageHeight: 1080,
			Model: ModelIdentity{Name: "atlas-objects", Version: "1"},
			Detections: []Detection{{
				ClassID: 0, ClassLabel: "person", Confidence: 0.9,
				BoundingBox: BoundingBox{X: 0.1, Y: 0.2, Width: 0.3, Height: 0.4},
			}},
		},
	}); err != nil {
		t.Fatalf("write frame: %v", err)
	}
	if err := encoder.Encode(runtimeEnvelope{
		ProtocolVersion: RuntimeProtocolVersion,
		Type:            "health",
		Health: &Health{
			SourceID: "a8-main", Provider: "deepstream", Accelerator: "jetson-orin",
			InputConnected: true, InferenceReady: true, OutputPublishing: true,
			InputFPS: 30, InferenceFPS: 20, ObservedAt: now,
		},
	}); err != nil {
		t.Fatalf("write health: %v", err)
	}

	select {
	case frame := <-outputs.Frames:
		if frame.FrameID != "frame-1" || frame.Detections[0].ClassLabel != "person" {
			t.Fatalf("frame = %#v", frame)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime frame was not published")
	}
	select {
	case health := <-outputs.Health:
		if health.Provider != "deepstream" || health.Accelerator != "jetson-orin" {
			t.Fatalf("health = %#v", health)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime health was not published")
	}

	connection.Close()
	cancel()
	for range 50 {
		if _, err := os.Lstat(socketPath); os.IsNotExist(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("runtime socket %q was not removed", socketPath)
}
