package mavlinkobserver

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/bluenviron/gomavlib/v4"
)

const (
	EndpointSchemeUDPServer = "udp-server"
	EndpointSchemeUDPClient = "udp-client"
	EndpointSchemeSerial    = "serial"
)

type EndpointSpec struct {
	Raw     string
	Scheme  string
	Address string
	Device  string
	Baud    int
}

func ParseEndpoint(raw string) (EndpointSpec, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return EndpointSpec{}, fmt.Errorf("mavlink observer endpoint is required")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return EndpointSpec{}, fmt.Errorf("parse mavlink observer endpoint: %w", err)
	}

	spec := EndpointSpec{
		Raw:    trimmed,
		Scheme: parsed.Scheme,
	}

	switch parsed.Scheme {
	case EndpointSchemeUDPServer, EndpointSchemeUDPClient:
		if parsed.Host == "" {
			return EndpointSpec{}, fmt.Errorf("%s endpoint requires host:port", parsed.Scheme)
		}
		if parsed.Path != "" {
			return EndpointSpec{}, fmt.Errorf("%s endpoint must not include a path", parsed.Scheme)
		}
		spec.Address = parsed.Host
		return spec, nil
	case EndpointSchemeSerial:
		if parsed.Path == "" || parsed.Path == "/" {
			return EndpointSpec{}, fmt.Errorf("serial endpoint requires a device path")
		}

		baudRaw := strings.TrimSpace(parsed.Query().Get("baud"))
		if baudRaw == "" {
			return EndpointSpec{}, fmt.Errorf("serial endpoint requires a baud query parameter")
		}

		baud, err := strconv.Atoi(baudRaw)
		if err != nil || baud <= 0 {
			return EndpointSpec{}, fmt.Errorf("serial endpoint baud must be a positive integer")
		}

		spec.Device = parsed.Path
		spec.Baud = baud
		return spec, nil
	default:
		return EndpointSpec{}, fmt.Errorf("unsupported mavlink observer endpoint scheme %q", parsed.Scheme)
	}
}

func BuildEndpoint(raw string) (gomavlib.Endpoint, EndpointSpec, error) {
	spec, err := ParseEndpoint(raw)
	if err != nil {
		return nil, EndpointSpec{}, err
	}

	switch spec.Scheme {
	case EndpointSchemeUDPServer:
		return &gomavlib.EndpointUDPServer{Address: spec.Address}, spec, nil
	case EndpointSchemeUDPClient:
		return &gomavlib.EndpointUDPClient{Address: spec.Address}, spec, nil
	case EndpointSchemeSerial:
		return &gomavlib.EndpointSerial{Device: spec.Device, Baud: spec.Baud}, spec, nil
	default:
		return nil, EndpointSpec{}, fmt.Errorf("unsupported mavlink observer endpoint scheme %q", spec.Scheme)
	}
}
