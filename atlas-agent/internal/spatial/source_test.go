package spatial

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math"
	"testing"
)

func framed(t *testing.T, metadata header, payload []byte) []byte {
	t.Helper()
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	result := make([]byte, 4+len(raw)+len(payload))
	binary.BigEndian.PutUint32(result, uint32(len(raw)))
	copy(result[4:], raw)
	copy(result[4+len(raw):], payload)
	return result
}

func validHeader() header {
	return header{ProtocolVersion: "1", SourceID: "front-depth", StreamEpoch: "epoch-1", Sequence: 7, ObservedAtUnixMS: 1, CaptureNS: 2, FrameID: "vio_local", VoxelSizeM: 0.05, PointCount: 1, XYZByteLength: 12}
}

func TestReadFramePreservesTheCompletePackedCloud(t *testing.T) {
	payload := make([]byte, 12)
	binary.LittleEndian.PutUint32(payload, math.Float32bits(1.25))
	frame, err := readFrame(bytes.NewReader(framed(t, validHeader(), payload)))
	if err != nil {
		t.Fatal(err)
	}
	if frame.PointCount != 1 || !bytes.Equal(frame.XYZF32LE, payload) {
		t.Fatalf("frame = %#v", frame)
	}
}

func TestReadFrameNormalizesValidOptionalPose(t *testing.T) {
	metadata := validHeader()
	metadata.Pose = &Pose{
		CaptureNS: 2, FrameID: "vio_local", ChildFrameID: "oak_mount",
		OrientationWXYZ: [4]float64{1.01, 0, 0, 0},
	}
	frame, err := readFrame(bytes.NewReader(framed(t, metadata, make([]byte, 12))))
	if err != nil {
		t.Fatal(err)
	}
	if frame.Pose == nil || frame.Pose.OrientationWXYZ != [4]float64{1, 0, 0, 0} {
		t.Fatalf("normalized pose = %#v", frame.Pose)
	}
}

func TestReadFrameOmitsInvalidOptionalPoseWithoutDroppingCloud(t *testing.T) {
	metadata := validHeader()
	metadata.Pose = &Pose{
		CaptureNS: 2, FrameID: "vio_local", ChildFrameID: "oak_mount",
		OrientationWXYZ: [4]float64{},
	}
	frame, err := readFrame(bytes.NewReader(framed(t, metadata, make([]byte, 12))))
	if err != nil {
		t.Fatal(err)
	}
	if frame.Pose != nil || frame.PointCount != 1 || len(frame.XYZF32LE) != 12 {
		t.Fatalf("frame with omitted pose = %#v", frame)
	}
}

func TestReadFrameOmitsOptionalPoseWithBlankFrameIdentity(t *testing.T) {
	metadata := validHeader()
	metadata.Pose = &Pose{
		CaptureNS: 2, FrameID: " ", ChildFrameID: "oak_mount",
		OrientationWXYZ: [4]float64{1, 0, 0, 0},
	}
	frame, err := readFrame(bytes.NewReader(framed(t, metadata, make([]byte, 12))))
	if err != nil {
		t.Fatal(err)
	}
	if frame.Pose != nil {
		t.Fatalf("pose with blank frame identity was preserved: %#v", frame.Pose)
	}
}

func TestReadFrameRejectsMismatchedAndNonFiniteClouds(t *testing.T) {
	metadata := validHeader()
	metadata.XYZByteLength = 8
	if _, err := readFrame(bytes.NewReader(framed(t, metadata, make([]byte, 8)))); err == nil {
		t.Fatal("mismatched payload accepted")
	}

	metadata = validHeader()
	payload := make([]byte, 12)
	binary.LittleEndian.PutUint32(payload, math.Float32bits(float32(math.Inf(1))))
	if _, err := readFrame(bytes.NewReader(framed(t, metadata, payload))); err == nil {
		t.Fatal("non-finite payload accepted")
	}
}

func TestReadFrameAcceptsOneCompleteMaximumCloud(t *testing.T) {
	metadata := validHeader()
	metadata.PointCount = MaximumPoints
	metadata.XYZByteLength = MaximumPoints * bytesPerPoint
	payload := make([]byte, maximumPayloadBytes)
	frame, err := readFrame(bytes.NewReader(framed(t, metadata, payload)))
	if err != nil {
		t.Fatal(err)
	}
	if frame.PointCount != MaximumPoints || len(frame.XYZF32LE) != 1_200_000 {
		t.Fatalf("maximum cloud = %d points, %d bytes", frame.PointCount, len(frame.XYZF32LE))
	}
}
