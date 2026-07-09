package mavlinkobserver

import "time"

// Observer combines frame extraction and message decoding. It is useful for
// tests and for future UDP/serial readers that will feed raw bytes into this
// package without learning MAVLink framing details.
type Observer struct {
	decoder Decoder
}

func (o *Observer) Push(data []byte, observedAt time.Time) ([]Observation, error) {
	frames, err := o.decoder.Push(data)
	if err != nil {
		return nil, err
	}

	observations := make([]Observation, 0, len(frames))
	for _, frame := range frames {
		observation, ok := DecodeFrame(frame, observedAt)
		if ok {
			observations = append(observations, observation)
		}
	}
	return observations, nil
}

func (o *Observer) Buffered() int {
	return o.decoder.Buffered()
}
