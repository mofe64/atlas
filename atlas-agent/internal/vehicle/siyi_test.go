package vehicle

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"
)

func TestSIYIFrameMatchesPublishedAbsoluteZoomExample(t *testing.T) {
	got := siyiFrame(1, siyiAbsoluteZoomCommand, []byte{4, 5})
	want := []byte{0x55, 0x66, 0x01, 0x02, 0x00, 0x01, 0x00, 0x0f, 0x04, 0x05, 0x60, 0xbb}
	if !bytes.Equal(got, want) {
		t.Fatalf("frame = % x, want % x", got, want)
	}
}

func TestDecodeSIYIFrameRejectsChecksumDamage(t *testing.T) {
	frame := siyiFrame(0, siyiMaximumZoomCommand, []byte{6, 0})
	command, payload, err := decodeSIYIFrame(frame)
	if err != nil || command != siyiMaximumZoomCommand || !bytes.Equal(payload, []byte{6, 0}) {
		t.Fatalf("decoded command=%x payload=%v error=%v", command, payload, err)
	}
	frame[8] ^= 0xff
	if _, _, err := decodeSIYIFrame(frame); err == nil {
		t.Fatal("expected checksum damage to be rejected")
	}
}

func TestSIYICameraDiscoversMaximumAndMapsZoomPercent(t *testing.T) {
	connection, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("listen UDP: %v", err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	requests := make(chan []byte, 2)
	serverErrors := make(chan error, 1)
	go func() {
		for _, response := range [][]byte{
			siyiFrame(0, siyiMaximumZoomCommand, []byte{6, 0}),
			siyiFrame(1, siyiAbsoluteZoomCommand, []byte{1}),
		} {
			buffer := make([]byte, 256)
			length, client, readErr := connection.ReadFromUDP(buffer)
			if readErr != nil {
				serverErrors <- readErr
				return
			}
			requests <- append([]byte(nil), buffer[:length]...)
			if _, writeErr := connection.WriteToUDP(response, client); writeErr != nil {
				serverErrors <- writeErr
				return
			}
		}
	}()

	camera := NewSIYICamera(connection.LocalAddr().String())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := camera.Discover(ctx); err != nil {
		t.Fatalf("discover camera: %v", err)
	}
	if err := camera.SetZoom(ctx, 50); err != nil {
		t.Fatalf("set zoom: %v", err)
	}

	maximumRequest := <-requests
	command, payload, err := decodeSIYIFrame(maximumRequest)
	if err != nil || command != siyiMaximumZoomCommand || len(payload) != 0 {
		t.Fatalf("max request command=%x payload=%v error=%v", command, payload, err)
	}
	zoomRequest := <-requests
	command, payload, err = decodeSIYIFrame(zoomRequest)
	if err != nil || command != siyiAbsoluteZoomCommand || !bytes.Equal(payload, []byte{3, 5}) {
		t.Fatalf("zoom request command=%x payload=%v error=%v", command, payload, err)
	}
	select {
	case serverErr := <-serverErrors:
		t.Fatalf("UDP server: %v", serverErr)
	default:
	}
}
