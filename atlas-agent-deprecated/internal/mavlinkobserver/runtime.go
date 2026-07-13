package mavlinkobserver

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/bluenviron/gomavlib/v4"
	"github.com/bluenviron/gomavlib/v4/pkg/dialects/common"
)

const (
	observerOutSystemID    byte = 250
	observerOutComponentID byte = 191
)

type Config struct {
	Endpoint string
}

type ObservationHandler func(context.Context, Observation)

type RuntimeStatusSink interface {
	MarkConnected(time.Time)
	MarkDisconnected(time.Time, string)
}

type Runtime struct {
	node *gomavlib.Node
	spec EndpointSpec
}

func NewRuntime(cfg Config) (*Runtime, error) {
	endpoint, spec, err := BuildEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}

	node := &gomavlib.Node{
		Endpoints: []gomavlib.Endpoint{endpoint},
		Dialect:   common.Dialect,

		OutVersion:     gomavlib.V2,
		OutSystemID:    observerOutSystemID,
		OutComponentID: observerOutComponentID,

		HeartbeatDisable:    true,
		StreamRequestEnable: false,
	}
	if err := node.Initialize(); err != nil {
		return nil, fmt.Errorf("initialize mavlink observer endpoint %q: %w", spec.Raw, err)
	}

	return &Runtime{node: node, spec: spec}, nil
}

func (r *Runtime) Run(ctx context.Context, logger *slog.Logger, handle ObservationHandler, statusSinks ...RuntimeStatusSink) error {
	if logger == nil {
		logger = slog.Default()
	}
	if handle == nil {
		handle = func(context.Context, Observation) {}
	}

	logger.Info(
		"mavlink observer started",
		"endpoint", r.spec.Raw,
		"scheme", r.spec.Scheme,
	)
	defer r.node.Close()

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-r.node.Events():
			if !ok {
				return nil
			}
			r.handleEvent(ctx, logger, handle, event, statusSinks...)
		}
	}
}

func (r *Runtime) handleEvent(ctx context.Context, logger *slog.Logger, handle ObservationHandler, event gomavlib.Event, statusSinks ...RuntimeStatusSink) {
	switch e := event.(type) {
	case *gomavlib.EventChannelOpen:
		for _, sink := range statusSinks {
			sink.MarkConnected(time.Now().UTC())
		}
		logger.Info("mavlink observer channel opened", "endpoint", r.spec.Raw)
	case *gomavlib.EventChannelClose:
		reason := ""
		if e.Error != nil {
			reason = e.Error.Error()
		}
		for _, sink := range statusSinks {
			sink.MarkDisconnected(time.Now().UTC(), reason)
		}
		logger.Warn("mavlink observer channel closed", "endpoint", r.spec.Raw, "error", e.Error)
	case *gomavlib.EventParseError:
		logger.Warn("mavlink observer parse error", "endpoint", r.spec.Raw, "error", e.Error)
	case *gomavlib.EventFrame:
		observation, ok := DecodeEventFrame(e, time.Now().UTC())
		if !ok {
			return
		}
		handle(ctx, observation)
	case *gomavlib.EventStreamRequested:
		logger.Debug("mavlink observer stream requested", "endpoint", r.spec.Raw, "system_id", e.SystemID, "component_id", e.ComponentID)
	}
}
