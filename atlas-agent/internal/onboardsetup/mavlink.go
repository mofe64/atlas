package onboardsetup

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"
)

type MAVLinkHeartbeat struct {
	SystemID    uint8
	ComponentID uint8
	VehicleType uint8
	Autopilot   uint8
}

// ProbeMAVLinkHeartbeat configures a Linux serial device and listens for a
// checksum-valid MAVLink v1 or v2 HEARTBEAT. It does not transmit anything to
// the flight controller.
func ProbeMAVLinkHeartbeat(ctx context.Context, runner Runner, device string, baud uint32, timeout time.Duration) (MAVLinkHeartbeat, error) {
	if device == "" {
		return MAVLinkHeartbeat{}, errors.New("serial device is required")
	}
	result := runner.Run(ctx, "stty", "-F", device, fmt.Sprint(baud), "raw", "-echo", "-ixon", "-ixoff")
	if result.Err != nil {
		return MAVLinkHeartbeat{}, fmt.Errorf("configure serial device: %w%s", result.Err, outputSuffix(result.Output))
	}
	file, err := os.OpenFile(device, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return MAVLinkHeartbeat{}, fmt.Errorf("open serial device: %w", err)
	}
	defer file.Close()

	probeContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	buffer := make([]byte, 0, 4096)
	chunk := make([]byte, 1024)
	for {
		if heartbeat, ok := parseMAVLinkHeartbeat(buffer); ok {
			return heartbeat, nil
		}
		select {
		case <-probeContext.Done():
			return MAVLinkHeartbeat{}, fmt.Errorf("no checksum-valid MAVLink heartbeat observed within %s", timeout)
		default:
		}
		count, readErr := file.Read(chunk)
		if count > 0 {
			buffer = append(buffer, chunk[:count]...)
			if len(buffer) > 64*1024 {
				buffer = append([]byte(nil), buffer[len(buffer)-4096:]...)
			}
		}
		if readErr != nil && !errors.Is(readErr, syscall.EAGAIN) && !errors.Is(readErr, syscall.EWOULDBLOCK) && !errors.Is(readErr, io.EOF) {
			return MAVLinkHeartbeat{}, fmt.Errorf("read serial device: %w", readErr)
		}
		time.Sleep(15 * time.Millisecond)
	}
}

func parseMAVLinkHeartbeat(stream []byte) (MAVLinkHeartbeat, bool) {
	for offset := 0; offset < len(stream); offset++ {
		switch stream[offset] {
		case 0xfe:
			if heartbeat, ok := parseMAVLinkV1Heartbeat(stream[offset:]); ok {
				return heartbeat, true
			}
		case 0xfd:
			if heartbeat, ok := parseMAVLinkV2Heartbeat(stream[offset:]); ok {
				return heartbeat, true
			}
		}
	}
	return MAVLinkHeartbeat{}, false
}

func parseMAVLinkV1Heartbeat(frame []byte) (MAVLinkHeartbeat, bool) {
	if len(frame) < 8 {
		return MAVLinkHeartbeat{}, false
	}
	payloadLength := int(frame[1])
	frameLength := 6 + payloadLength + 2
	if len(frame) < frameLength || frame[5] != 0 || payloadLength < 9 {
		return MAVLinkHeartbeat{}, false
	}
	want := binary.LittleEndian.Uint16(frame[6+payloadLength : frameLength])
	if mavlinkCRC(frame[1:6+payloadLength], 50) != want {
		return MAVLinkHeartbeat{}, false
	}
	payload := frame[6 : 6+payloadLength]
	return MAVLinkHeartbeat{SystemID: frame[3], ComponentID: frame[4], VehicleType: payload[4], Autopilot: payload[5]}, true
}

func parseMAVLinkV2Heartbeat(frame []byte) (MAVLinkHeartbeat, bool) {
	if len(frame) < 12 {
		return MAVLinkHeartbeat{}, false
	}
	payloadLength := int(frame[1])
	signatureLength := 0
	if frame[2]&1 != 0 {
		signatureLength = 13
	}
	frameLength := 10 + payloadLength + 2 + signatureLength
	if len(frame) < frameLength || frame[7] != 0 || frame[8] != 0 || frame[9] != 0 || payloadLength < 9 {
		return MAVLinkHeartbeat{}, false
	}
	want := binary.LittleEndian.Uint16(frame[10+payloadLength : 12+payloadLength])
	if mavlinkCRC(frame[1:10+payloadLength], 50) != want {
		return MAVLinkHeartbeat{}, false
	}
	payload := frame[10 : 10+payloadLength]
	return MAVLinkHeartbeat{SystemID: frame[5], ComponentID: frame[6], VehicleType: payload[4], Autopilot: payload[5]}, true
}

func mavlinkCRC(data []byte, extra byte) uint16 {
	crc := uint16(0xffff)
	for _, value := range append(append([]byte(nil), data...), extra) {
		temporary := value ^ byte(crc&0xff)
		temporary ^= temporary << 4
		crc = (crc >> 8) ^ (uint16(temporary) << 8) ^ (uint16(temporary) << 3) ^ (uint16(temporary) >> 4)
	}
	return crc
}
