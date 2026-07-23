package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/sunnyside/atlas/atlas-agent/internal/navigation"
)

func main() {
	socketPath := flag.String("socket", "/run/atlas-agent/navigation.sock", "Atlas navigation Unix socket")
	sampleAt := flag.Int64("sample-at-unix-ns", 0, "capture Unix nanoseconds; zero requests latest state")
	maxSkew := flag.Duration("max-skew", 100*time.Millisecond, "maximum capture-time sample skew")
	requireReady := flag.Bool("require-ready", false, "exit non-zero unless the returned state is ready")
	flag.Parse()

	request := map[string]any{"protocolVersion": navigation.ProtocolVersion, "type": "latest"}
	if *sampleAt > 0 {
		request["type"] = "sampleAt"
		request["captureUnixNs"] = *sampleAt
		request["maxSkewNs"] = maxSkew.Nanoseconds()
	}
	connection, err := net.DialTimeout("unix", *socketPath, time.Second)
	if err != nil {
		fail(err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		fail(err)
	}
	var response map[string]any
	if err := json.NewDecoder(connection).Decode(&response); err != nil {
		fail(err)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(response); err != nil {
		fail(err)
	}
	if message, _ := response["error"].(string); message != "" {
		os.Exit(1)
	}
	if *requireReady && !responseReady(response) {
		os.Exit(2)
	}
}

func responseReady(response map[string]any) bool {
	state, _ := response["state"].(map[string]any)
	if sample, ok := response["sample"].(map[string]any); ok {
		state, _ = sample["state"].(map[string]any)
	}
	ready, _ := state["ready"].(bool)
	return ready
}

func fail(err error) {
	_, _ = fmt.Fprintln(os.Stderr, "atlas-navigation-probe:", err)
	os.Exit(1)
}
