package mavlinkobserver

import "encoding/binary"

const (
	mavlinkV1HeaderLen    = 6
	mavlinkV2HeaderLen    = 10
	mavlinkChecksumLen    = 2
	mavlinkSignatureLen   = 13
	mavlinkV2SignedPacket = 0x01
)

// Decoder incrementally extracts MAVLink v1/v2 frames from arbitrary byte
// chunks. It does not validate CRCs yet; the observer's first job is passive
// evidence extraction from trusted local links.
type Decoder struct {
	buffer []byte
}

func (d *Decoder) Push(data []byte) ([]Frame, error) {
	if len(data) > 0 {
		d.buffer = append(d.buffer, data...)
	}

	var frames []Frame
	for {
		d.discardUntilMagic()
		if len(d.buffer) == 0 {
			return frames, nil
		}

		frame, frameLen, ok := decodeBufferedFrame(d.buffer)
		if !ok {
			return frames, nil
		}

		frames = append(frames, frame)
		d.buffer = d.buffer[frameLen:]
	}
}

func (d *Decoder) Buffered() int {
	return len(d.buffer)
}

func (d *Decoder) discardUntilMagic() {
	for i, b := range d.buffer {
		if b == mavlinkV1Magic || b == mavlinkV2Magic {
			if i > 0 {
				d.buffer = d.buffer[i:]
			}
			return
		}
	}
	d.buffer = d.buffer[:0]
}

func decodeBufferedFrame(buf []byte) (Frame, int, bool) {
	switch buf[0] {
	case mavlinkV1Magic:
		return decodeBufferedV1Frame(buf)
	case mavlinkV2Magic:
		return decodeBufferedV2Frame(buf)
	default:
		return Frame{}, 0, false
	}
}

func decodeBufferedV1Frame(buf []byte) (Frame, int, bool) {
	if len(buf) < mavlinkV1HeaderLen {
		return Frame{}, 0, false
	}

	payloadLen := int(buf[1])
	frameLen := mavlinkV1HeaderLen + payloadLen + mavlinkChecksumLen
	if len(buf) < frameLen {
		return Frame{}, 0, false
	}

	payloadStart := mavlinkV1HeaderLen
	payloadEnd := payloadStart + payloadLen
	payload := append([]byte(nil), buf[payloadStart:payloadEnd]...)

	return Frame{
		Version:     1,
		Sequence:    buf[2],
		SystemID:    buf[3],
		ComponentID: buf[4],
		MessageID:   uint32(buf[5]),
		Payload:     payload,
		Checksum:    binary.LittleEndian.Uint16(buf[payloadEnd : payloadEnd+mavlinkChecksumLen]),
	}, frameLen, true
}

func decodeBufferedV2Frame(buf []byte) (Frame, int, bool) {
	if len(buf) < mavlinkV2HeaderLen {
		return Frame{}, 0, false
	}

	payloadLen := int(buf[1])
	signatureLen := 0
	if buf[2]&mavlinkV2SignedPacket != 0 {
		signatureLen = mavlinkSignatureLen
	}
	frameLen := mavlinkV2HeaderLen + payloadLen + mavlinkChecksumLen + signatureLen
	if len(buf) < frameLen {
		return Frame{}, 0, false
	}

	payloadStart := mavlinkV2HeaderLen
	payloadEnd := payloadStart + payloadLen
	payload := append([]byte(nil), buf[payloadStart:payloadEnd]...)
	checksumEnd := payloadEnd + mavlinkChecksumLen

	messageID := uint32(buf[7]) | uint32(buf[8])<<8 | uint32(buf[9])<<16
	frame := Frame{
		Version:       2,
		IncompatFlags: buf[2],
		CompatFlags:   buf[3],
		Sequence:      buf[4],
		SystemID:      buf[5],
		ComponentID:   buf[6],
		MessageID:     messageID,
		Payload:       payload,
		Checksum:      binary.LittleEndian.Uint16(buf[payloadEnd:checksumEnd]),
	}
	if signatureLen > 0 {
		frame.Signature = append([]byte(nil), buf[checksumEnd:checksumEnd+signatureLen]...)
	}

	return frame, frameLen, true
}
