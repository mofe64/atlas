// Package spatial owns the bounded, read-only point-cloud data plane from the
// isolated ROS runtime. It never exposes movement authority.
package spatial

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"strings"
	"time"
)

const (
	TransportProtocolVersion = "1"
	MaximumPoints            = 100_000
	bytesPerPoint            = 12
	maximumHeaderBytes       = 16 * 1024
	maximumPayloadBytes      = MaximumPoints * bytesPerPoint
)

type Pose struct {
	CaptureNS       int64      `json:"captureNs"`
	FrameID         string     `json:"frameId"`
	ChildFrameID    string     `json:"childFrameId"`
	Position        [3]float64 `json:"position"`
	OrientationWXYZ [4]float64 `json:"orientationWxyz"`
}

type header struct {
	ProtocolVersion  string  `json:"protocolVersion"`
	SourceID         string  `json:"sourceId"`
	StreamEpoch      string  `json:"streamEpoch"`
	Sequence         uint64  `json:"sequence"`
	ObservedAtUnixMS int64   `json:"observedAtUnixMs"`
	CaptureNS        int64   `json:"captureNs"`
	FrameID          string  `json:"frameId"`
	VoxelSizeM       float32 `json:"voxelSizeM"`
	PointCount       uint32  `json:"pointCount"`
	XYZByteLength    uint32  `json:"xyzByteLength"`
	Pose             *Pose   `json:"pose"`
}

type Frame struct {
	SourceID         string
	StreamEpoch      string
	Sequence         uint64
	ObservedAtUnixMS int64
	CaptureNS        int64
	FrameID          string
	VoxelSizeM       float32
	PointCount       uint32
	XYZF32LE         []byte
	Pose             *Pose
}

type Outputs struct {
	Frames <-chan Frame
}

// StartRuntimeSource continuously reconnects to the local Unix socket. Its
// one-slot output replaces stale complete snapshots while a remote send is
// congested; no point subset or frame backlog is created.
func StartRuntimeSource(ctx context.Context, logger *slog.Logger, socketPath string) Outputs {
	frames := make(chan Frame, 1)
	go func() {
		defer close(frames)
		backoff := 250 * time.Millisecond
		for ctx.Err() == nil {
			connection, err := (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
			if err != nil {
				logger.Debug("spatial cloud socket unavailable", "socket_path", socketPath, "error", err)
			} else {
				err = readConnection(ctx, connection, frames)
				_ = connection.Close()
				if ctx.Err() == nil {
					logger.Warn("spatial cloud socket ended; reconnecting", "error", err)
				}
			}
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if backoff < 5*time.Second {
				backoff *= 2
			}
		}
	}()
	return Outputs{Frames: frames}
}

func readConnection(ctx context.Context, connection net.Conn, frames chan Frame) error {
	reader := bufio.NewReaderSize(connection, maximumPayloadBytes+maximumHeaderBytes+4)
	for {
		frame, err := readFrame(reader)
		if err != nil {
			return err
		}
		select {
		case frames <- frame:
		default:
			select {
			case <-frames:
			default:
			}
			select {
			case frames <- frame:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

func readFrame(reader io.Reader) (Frame, error) {
	var prefix [4]byte
	if _, err := io.ReadFull(reader, prefix[:]); err != nil {
		return Frame{}, err
	}
	headerLength := binary.BigEndian.Uint32(prefix[:])
	if headerLength == 0 || headerLength > maximumHeaderBytes {
		return Frame{}, errors.New("spatial frame header exceeds the bounded limit")
	}
	rawHeader := make([]byte, headerLength)
	if _, err := io.ReadFull(reader, rawHeader); err != nil {
		return Frame{}, fmt.Errorf("read spatial frame header: %w", err)
	}
	var metadata header
	decoder := json.NewDecoder(bytesReader(rawHeader))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return Frame{}, fmt.Errorf("decode spatial frame header: %w", err)
	}
	if err := validateHeader(metadata); err != nil {
		return Frame{}, err
	}
	payload := make([]byte, metadata.XYZByteLength)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return Frame{}, fmt.Errorf("read complete spatial cloud: %w", err)
	}
	for offset := 0; offset < len(payload); offset += 4 {
		if value := math.Float32frombits(binary.LittleEndian.Uint32(payload[offset:])); math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return Frame{}, errors.New("spatial cloud contains a non-finite coordinate")
		}
	}
	return Frame{
		SourceID: metadata.SourceID, StreamEpoch: metadata.StreamEpoch,
		Sequence: metadata.Sequence, ObservedAtUnixMS: metadata.ObservedAtUnixMS,
		CaptureNS: metadata.CaptureNS, FrameID: metadata.FrameID,
		VoxelSizeM: metadata.VoxelSizeM, PointCount: metadata.PointCount,
		XYZF32LE: payload, Pose: normalizedPoseOrNil(metadata.Pose),
	}, nil
}

// normalizedPoseOrNil preserves a complete map-frame cloud when VIO is
// degraded. Pose is optional transport metadata; an invalid pose must not tear
// down the spatial stream or be mistaken for an authoritative orientation.
func normalizedPoseOrNil(value *Pose) *Pose {
	if value == nil || value.CaptureNS <= 0 ||
		strings.TrimSpace(value.FrameID) == "" || strings.TrimSpace(value.ChildFrameID) == "" {
		return nil
	}
	values := append(value.Position[:], value.OrientationWXYZ[:]...)
	for _, coordinate := range values {
		if math.IsNaN(coordinate) || math.IsInf(coordinate, 0) {
			return nil
		}
	}
	norm := math.Sqrt(
		value.OrientationWXYZ[0]*value.OrientationWXYZ[0] +
			value.OrientationWXYZ[1]*value.OrientationWXYZ[1] +
			value.OrientationWXYZ[2]*value.OrientationWXYZ[2] +
			value.OrientationWXYZ[3]*value.OrientationWXYZ[3],
	)
	if norm < 0.9 || norm > 1.1 {
		return nil
	}
	result := *value
	for index := range result.OrientationWXYZ {
		result.OrientationWXYZ[index] /= norm
	}
	return &result
}

func validateHeader(value header) error {
	if value.ProtocolVersion != TransportProtocolVersion {
		return errors.New("unsupported spatial stream protocol")
	}
	if value.SourceID == "" || value.StreamEpoch == "" || value.FrameID == "" {
		return errors.New("spatial source, epoch, and frame are required")
	}
	if value.Sequence == 0 || value.CaptureNS <= 0 || value.ObservedAtUnixMS <= 0 {
		return errors.New("spatial timestamps and sequence must be positive")
	}
	if value.PointCount == 0 || value.PointCount > MaximumPoints {
		return fmt.Errorf("spatial point count must be between 1 and %d", MaximumPoints)
	}
	if value.XYZByteLength != value.PointCount*bytesPerPoint || value.XYZByteLength > maximumPayloadBytes {
		return errors.New("spatial XYZ length must be exactly 12 bytes per point")
	}
	if value.VoxelSizeM <= 0 || math.IsNaN(float64(value.VoxelSizeM)) || math.IsInf(float64(value.VoxelSizeM), 0) {
		return errors.New("spatial voxel size must be finite and positive")
	}
	return nil
}

type readOnlyBytes struct {
	value  []byte
	offset int
}

func bytesReader(value []byte) *readOnlyBytes { return &readOnlyBytes{value: value} }

func (reader *readOnlyBytes) Read(destination []byte) (int, error) {
	if reader.offset >= len(reader.value) {
		return 0, io.EOF
	}
	n := copy(destination, reader.value[reader.offset:])
	reader.offset += n
	return n, nil
}
