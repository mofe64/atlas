package mavlinkobserver

import (
	"encoding/binary"
	"testing"
)

func TestDecoderParsesMAVLinkV1Frame(t *testing.T) {
	payload := make([]byte, 9)
	binary.LittleEndian.PutUint32(payload[0:4], 42)
	payload[4] = 2
	payload[5] = 12
	payload[6] = 81
	payload[7] = 4
	payload[8] = 3

	frameBytes := mavlinkV1Frame(7, 1, 1, MessageIDHeartbeat, payload)
	var decoder Decoder
	frames, err := decoder.Push(frameBytes)
	if err != nil {
		t.Fatalf("push frame: %v", err)
	}

	if len(frames) != 1 {
		t.Fatalf("expected one frame, got %d", len(frames))
	}
	frame := frames[0]
	if frame.Version != 1 {
		t.Fatalf("expected MAVLink v1 frame, got v%d", frame.Version)
	}
	if frame.Sequence != 7 || frame.SystemID != 1 || frame.ComponentID != 1 {
		t.Fatalf("unexpected frame header: %#v", frame)
	}
	if frame.MessageID != MessageIDHeartbeat {
		t.Fatalf("expected heartbeat message id, got %d", frame.MessageID)
	}
	if len(frame.Payload) != len(payload) {
		t.Fatalf("expected payload length %d, got %d", len(payload), len(frame.Payload))
	}
}

func TestDecoderParsesFragmentedMAVLinkV2Frame(t *testing.T) {
	payload := make([]byte, 10)
	binary.LittleEndian.PutUint16(payload[0:2], 400)
	payload[2] = 0
	payload[3] = 100
	binary.LittleEndian.PutUint32(payload[4:8], 0)
	payload[8] = 1
	payload[9] = 1

	frameBytes := mavlinkV2Frame(3, 1, 1, MessageIDCommandAck, payload, false)
	var decoder Decoder
	frames, err := decoder.Push(frameBytes[:5])
	if err != nil {
		t.Fatalf("push first fragment: %v", err)
	}
	if len(frames) != 0 {
		t.Fatalf("expected no complete frames, got %d", len(frames))
	}
	if decoder.Buffered() != 5 {
		t.Fatalf("expected five buffered bytes, got %d", decoder.Buffered())
	}

	frames, err = decoder.Push(frameBytes[5:])
	if err != nil {
		t.Fatalf("push second fragment: %v", err)
	}
	if len(frames) != 1 {
		t.Fatalf("expected one frame, got %d", len(frames))
	}

	frame := frames[0]
	if frame.Version != 2 {
		t.Fatalf("expected MAVLink v2 frame, got v%d", frame.Version)
	}
	if frame.MessageID != MessageIDCommandAck {
		t.Fatalf("expected command ack message id, got %d", frame.MessageID)
	}
}

func TestDecoderKeepsSignedMAVLinkV2FrameAligned(t *testing.T) {
	first := mavlinkV2Frame(1, 1, 1, MessageIDMissionCurrent, []byte{5, 0}, true)
	second := mavlinkV2Frame(2, 1, 1, MessageIDMissionCurrent, []byte{6, 0}, false)

	var decoder Decoder
	frames, err := decoder.Push(append(first, second...))
	if err != nil {
		t.Fatalf("push frames: %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("expected two frames, got %d", len(frames))
	}
	if len(frames[0].Signature) != mavlinkSignatureLen {
		t.Fatalf("expected signed first frame, got signature length %d", len(frames[0].Signature))
	}
	if frames[1].Sequence != 2 {
		t.Fatalf("expected decoder to align second frame, got sequence %d", frames[1].Sequence)
	}
}

func mavlinkV1Frame(seq, sysid, compid uint8, messageID uint32, payload []byte) []byte {
	frame := []byte{mavlinkV1Magic, byte(len(payload)), seq, sysid, compid, byte(messageID)}
	frame = append(frame, payload...)
	frame = append(frame, 0, 0)
	return frame
}

func mavlinkV2Frame(seq, sysid, compid uint8, messageID uint32, payload []byte, signed bool) []byte {
	incompatFlags := byte(0)
	if signed {
		incompatFlags = mavlinkV2SignedPacket
	}

	frame := []byte{
		mavlinkV2Magic,
		byte(len(payload)),
		incompatFlags,
		0,
		seq,
		sysid,
		compid,
		byte(messageID),
		byte(messageID >> 8),
		byte(messageID >> 16),
	}
	frame = append(frame, payload...)
	frame = append(frame, 0, 0)
	if signed {
		frame = append(frame, make([]byte, mavlinkSignatureLen)...)
	}
	return frame
}
