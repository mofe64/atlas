package vehicle

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"sync"
	"time"
)

const (
	siyiAbsoluteZoomCommand = byte(0x0f)
	siyiMaximumZoomCommand  = byte(0x16)
)

// SIYICamera implements the small, device-specific part of the A8 Mini that
// MAVSDK Camera may not expose: absolute digital zoom. The A8 has fixed-focus
// optics, so this adapter intentionally has no focus API.
type SIYICamera struct {
	address  string
	mu       sync.Mutex
	sequence uint16
	maxZoom  float32
}

func NewSIYICamera(address string) *SIYICamera {
	return &SIYICamera{address: address}
}

func (camera *SIYICamera) Discover(ctx context.Context) error {
	payload, err := camera.request(ctx, siyiMaximumZoomCommand, nil)
	if err != nil {
		return err
	}
	if len(payload) < 2 {
		return errors.New("SIYI max-zoom response was incomplete")
	}
	maximum := float32(payload[0]) + float32(payload[1])/10
	if maximum < 1 || maximum > 100 {
		return fmt.Errorf("SIYI reported invalid max zoom %.1fx", maximum)
	}
	if maximum == 1 {
		return errors.New("A8 Mini zoom is unavailable in the current camera resolution")
	}
	camera.mu.Lock()
	camera.maxZoom = maximum
	camera.mu.Unlock()
	return nil
}

func (camera *SIYICamera) SetZoom(ctx context.Context, percent float32) error {
	camera.mu.Lock()
	maximum := camera.maxZoom
	camera.mu.Unlock()
	if maximum < 1 {
		return errors.New("SIYI camera has not completed zoom discovery")
	}
	percent = max(0, min(100, percent))
	multiple := 1 + percent/100*(maximum-1)
	integer := byte(math.Floor(float64(multiple)))
	fraction := byte(math.Round(float64(multiple-float32(integer)) * 10))
	if fraction == 10 {
		integer++
		fraction = 0
	}
	payload, err := camera.request(ctx, siyiAbsoluteZoomCommand, []byte{integer, fraction})
	if err != nil {
		return err
	}
	if len(payload) > 0 && payload[0] != 1 {
		return errors.New("SIYI camera rejected absolute zoom")
	}
	return nil
}

func (camera *SIYICamera) request(ctx context.Context, command byte, payload []byte) ([]byte, error) {
	camera.mu.Lock()
	sequence := camera.sequence
	camera.sequence++
	camera.mu.Unlock()
	frame := siyiFrame(sequence, command, payload)
	dialer := net.Dialer{Timeout: 400 * time.Millisecond}
	connection, err := dialer.DialContext(ctx, "udp", camera.address)
	if err != nil {
		return nil, fmt.Errorf("connect SIYI camera %s: %w", camera.address, err)
	}
	defer connection.Close()
	deadline := time.Now().Add(650 * time.Millisecond)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return nil, err
	}
	if _, err := connection.Write(frame); err != nil {
		return nil, fmt.Errorf("send SIYI command 0x%02x: %w", command, err)
	}
	buffer := make([]byte, 256)
	for {
		length, readErr := connection.Read(buffer)
		if readErr != nil {
			return nil, fmt.Errorf("receive SIYI command 0x%02x acknowledgement: %w", command, readErr)
		}
		responseCommand, responsePayload, decodeErr := decodeSIYIFrame(buffer[:length])
		if decodeErr != nil {
			continue
		}
		if responseCommand == command {
			return responsePayload, nil
		}
	}
}

func siyiFrame(sequence uint16, command byte, payload []byte) []byte {
	frame := make([]byte, 10+len(payload))
	frame[0], frame[1], frame[2] = 0x55, 0x66, 0x01
	binary.LittleEndian.PutUint16(frame[3:5], uint16(len(payload)))
	binary.LittleEndian.PutUint16(frame[5:7], sequence)
	frame[7] = command
	copy(frame[8:], payload)
	binary.LittleEndian.PutUint16(frame[len(frame)-2:], siyiCRC16(frame[:len(frame)-2]))
	return frame
}

func decodeSIYIFrame(frame []byte) (byte, []byte, error) {
	if len(frame) < 10 || frame[0] != 0x55 || frame[1] != 0x66 {
		return 0, nil, errors.New("invalid SIYI frame header")
	}
	payloadLength := int(binary.LittleEndian.Uint16(frame[3:5]))
	expectedLength := 10 + payloadLength
	if len(frame) < expectedLength {
		return 0, nil, errors.New("incomplete SIYI frame")
	}
	wantCRC := binary.LittleEndian.Uint16(frame[expectedLength-2 : expectedLength])
	if gotCRC := siyiCRC16(frame[:expectedLength-2]); gotCRC != wantCRC {
		return 0, nil, errors.New("invalid SIYI frame checksum")
	}
	payload := append([]byte(nil), frame[8:8+payloadLength]...)
	return frame[7], payload, nil
}

// SIYI specifies CRC-16/CCITT with polynomial 0x1021 and an initial value of
// zero. The checksum is serialized low byte first in the SDK envelope.
func siyiCRC16(data []byte) uint16 {
	var crc uint16
	for _, value := range data {
		crc ^= uint16(value) << 8
		for range 8 {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}
