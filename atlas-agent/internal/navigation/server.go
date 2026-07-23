package navigation

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"time"
)

const maximumRequestBytes = 4096

type socketRequest struct {
	ProtocolVersion string `json:"protocolVersion"`
	Type            string `json:"type"`
	CaptureUnixNS   int64  `json:"captureUnixNs"`
	MaxSkewNS       int64  `json:"maxSkewNs"`
}

type socketResponse struct {
	ProtocolVersion string  `json:"protocolVersion"`
	Type            string  `json:"type"`
	State           *State  `json:"state,omitempty"`
	Sample          *Sample `json:"sample,omitempty"`
	Error           string  `json:"error,omitempty"`
}

func StartSocketServer(ctx context.Context, logger *slog.Logger, path string, plane *Plane) error {
	if plane == nil {
		return errors.New("navigation plane is required")
	}
	if !filepath.IsAbs(path) {
		return errors.New("navigation socket path must be absolute")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("refusing to replace non-socket path %s", path)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	if err := os.Chmod(path, 0o660); err != nil {
		_ = listener.Close()
		return err
	}
	go func() {
		<-ctx.Done()
		_ = listener.Close()
		_ = os.Remove(path)
	}()
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				if ctx.Err() == nil {
					logger.Warn("navigation socket accept failed", "error", acceptErr)
				}
				return
			}
			go serveConnection(connection, plane)
		}
	}()
	return nil
}

func serveConnection(connection net.Conn, plane *Plane) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
	reader := bufio.NewReader(io.LimitReader(connection, maximumRequestBytes+1))
	raw, err := reader.ReadBytes('\n')
	response := socketResponse{ProtocolVersion: ProtocolVersion, Type: "error"}
	if err != nil && !errors.Is(err, io.EOF) {
		response.Error = "read navigation request: " + err.Error()
	} else if len(raw) > maximumRequestBytes {
		response.Error = "navigation request exceeds 4096 bytes"
	} else {
		var request socketRequest
		if err := json.Unmarshal(raw, &request); err != nil {
			response.Error = "decode navigation request: " + err.Error()
		} else if request.ProtocolVersion != ProtocolVersion {
			response.Error = "unsupported navigation protocol"
		} else {
			switch request.Type {
			case "latest":
				state := plane.Latest(time.Now())
				response.Type, response.State = "state", &state
			case "sampleAt":
				sample, sampleErr := plane.SampleAt(request.CaptureUnixNS, request.MaxSkewNS)
				if sampleErr != nil {
					response.Error = sampleErr.Error()
				} else {
					response.Type, response.Sample = "sample", &sample
				}
			default:
				response.Error = "unsupported navigation request type"
			}
		}
	}
	encoded, marshalErr := json.Marshal(response)
	if marshalErr != nil {
		response = socketResponse{ProtocolVersion: ProtocolVersion, Type: "error", Error: "encode navigation response: " + marshalErr.Error()}
		encoded, _ = json.Marshal(response)
	}
	_, _ = connection.Write(append(encoded, '\n'))
}
