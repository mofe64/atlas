package mavlinkobserver

import (
	"testing"

	"github.com/bluenviron/gomavlib/v4"
)

func TestParseEndpointUDPServer(t *testing.T) {
	spec, err := ParseEndpoint("udp-server://0.0.0.0:14550")
	if err != nil {
		t.Fatalf("parse endpoint: %v", err)
	}

	if spec.Scheme != EndpointSchemeUDPServer {
		t.Fatalf("expected udp-server scheme, got %q", spec.Scheme)
	}
	if spec.Address != "0.0.0.0:14550" {
		t.Fatalf("expected address 0.0.0.0:14550, got %q", spec.Address)
	}
}

func TestParseEndpointUDPClient(t *testing.T) {
	spec, err := ParseEndpoint("udp-client://127.0.0.1:14550")
	if err != nil {
		t.Fatalf("parse endpoint: %v", err)
	}

	if spec.Scheme != EndpointSchemeUDPClient {
		t.Fatalf("expected udp-client scheme, got %q", spec.Scheme)
	}
	if spec.Address != "127.0.0.1:14550" {
		t.Fatalf("expected address 127.0.0.1:14550, got %q", spec.Address)
	}
}

func TestParseEndpointSerial(t *testing.T) {
	spec, err := ParseEndpoint("serial:///dev/ttyUSB0?baud=57600")
	if err != nil {
		t.Fatalf("parse endpoint: %v", err)
	}

	if spec.Scheme != EndpointSchemeSerial {
		t.Fatalf("expected serial scheme, got %q", spec.Scheme)
	}
	if spec.Device != "/dev/ttyUSB0" {
		t.Fatalf("expected device /dev/ttyUSB0, got %q", spec.Device)
	}
	if spec.Baud != 57600 {
		t.Fatalf("expected baud 57600, got %d", spec.Baud)
	}
}

func TestBuildEndpoint(t *testing.T) {
	endpoint, spec, err := BuildEndpoint("serial:///dev/ttyAMA0?baud=921600")
	if err != nil {
		t.Fatalf("build endpoint: %v", err)
	}
	if spec.Device != "/dev/ttyAMA0" {
		t.Fatalf("expected device /dev/ttyAMA0, got %q", spec.Device)
	}

	serialEndpoint, ok := endpoint.(*gomavlib.EndpointSerial)
	if !ok {
		t.Fatalf("expected serial endpoint, got %T", endpoint)
	}
	if serialEndpoint.Baud != 921600 {
		t.Fatalf("expected baud 921600, got %d", serialEndpoint.Baud)
	}
}

func TestParseEndpointRejectsInvalidValues(t *testing.T) {
	cases := []string{
		"",
		"udp-server://",
		"udp-server://0.0.0.0:14550/path",
		"serial://",
		"serial:///dev/ttyUSB0",
		"serial:///dev/ttyUSB0?baud=fast",
		"tcp://127.0.0.1:5760",
	}

	for _, tc := range cases {
		t.Run(tc, func(t *testing.T) {
			if _, err := ParseEndpoint(tc); err == nil {
				t.Fatalf("expected parse error for %q", tc)
			}
		})
	}
}
