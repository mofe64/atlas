package localinputs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

var (
	ErrLocalVideoDisabled     = errors.New("local video input is disabled")
	ErrLocalVideoInvalidOffer = errors.New("local video WebRTC offer is invalid")
	errLocalVideoNoRTPPackets = errors.New("RTSP source produced no RTP packets")
)

const (
	localVideoCodecH264            = "H264"
	defaultVideoRTSPTransport      = "udp"
	defaultVideoRTPBufferSize      = 256
	defaultVideoUDPReadBufferBytes = 1 << 20
	defaultVideoFirstPacketTimeout = 5 * time.Second
)

type VideoOffer struct {
	Type string
	SDP  string
}

type VideoAnswer struct {
	Type string
	SDP  string
}

type VideoStatus struct {
	Enabled     bool
	SourceID    string
	RTSPURL     string
	State       string
	WebRTCReady bool
	Codec       string
	ActivePeers int
	LastFrameAt time.Time
	LastError   string
	UpdatedAt   time.Time
}

type VideoService struct {
	mu               sync.RWMutex
	rtspURL          string
	sourceID         string
	status           VideoStatus
	sessions         map[string]context.CancelFunc
	rtspTransport    string
	rtpBufferSize    int
	webrtcICENATIPs  []string
	webrtcUDPPortMin uint16
	webrtcUDPPortMax uint16
}

func NewVideoService(cfg Config) *VideoService {
	rtspURL := strings.TrimSpace(cfg.VideoRTSPURL)
	sourceID := strings.TrimSpace(cfg.SourceID)
	rtpBufferSize := cfg.VideoRTPBufferSize
	if rtpBufferSize <= 0 {
		rtpBufferSize = defaultVideoRTPBufferSize
	}

	status := VideoStatus{
		Enabled:     cfg.Enabled && rtspURL != "",
		SourceID:    sourceID,
		RTSPURL:     rtspURL,
		State:       "disabled",
		WebRTCReady: false,
		UpdatedAt:   time.Now().UTC(),
	}
	if status.Enabled {
		status.State = "configured"
		status.WebRTCReady = true
	}
	return &VideoService{
		rtspURL:          rtspURL,
		sourceID:         sourceID,
		status:           status,
		sessions:         map[string]context.CancelFunc{},
		rtspTransport:    normalizeVideoRTSPTransport(cfg.VideoRTSPTransport),
		rtpBufferSize:    rtpBufferSize,
		webrtcICENATIPs:  append([]string(nil), cfg.VideoWebRTCICENATIPs...),
		webrtcUDPPortMin: cfg.VideoWebRTCUDPPortMin,
		webrtcUDPPortMax: cfg.VideoWebRTCUDPPortMax,
	}
}

func (s *VideoService) Status() VideoStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

func (s *VideoService) HandleOffer(ctx context.Context, offer VideoOffer) (VideoAnswer, error) {
	if err := s.validateOffer(offer); err != nil {
		s.setError(err)
		return VideoAnswer{}, err
	}
	if !s.isEnabled() {
		s.setError(ErrLocalVideoDisabled)
		return VideoAnswer{}, ErrLocalVideoDisabled
	}

	s.setState("starting", "")

	peerConnection, err := s.newPeerConnection()
	if err != nil {
		s.setError(fmt.Errorf("create WebRTC peer connection: %w", err))
		return VideoAnswer{}, err
	}

	closePeer := true
	defer func() {
		if closePeer {
			_ = peerConnection.Close()
		}
	}()

	videoTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{
		MimeType:     webrtc.MimeTypeH264,
		ClockRate:    90000,
		SDPFmtpLine:  "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
		RTCPFeedback: []webrtc.RTCPFeedback{{Type: "nack"}, {Type: "nack", Parameter: "pli"}},
	}, "local-video", "atlas-local")
	if err != nil {
		s.setError(fmt.Errorf("create H.264 WebRTC track: %w", err))
		return VideoAnswer{}, err
	}

	rtpSender, err := peerConnection.AddTrack(videoTrack)
	if err != nil {
		s.setError(fmt.Errorf("add WebRTC video track: %w", err))
		return VideoAnswer{}, err
	}
	go drainRTCP(rtpSender)

	if err := peerConnection.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  offer.SDP,
	}); err != nil {
		wrapped := fmt.Errorf("%w: %v", ErrLocalVideoInvalidOffer, err)
		s.setError(wrapped)
		return VideoAnswer{}, wrapped
	}

	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		s.setError(fmt.Errorf("create WebRTC answer: %w", err))
		return VideoAnswer{}, err
	}

	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)
	if err := peerConnection.SetLocalDescription(answer); err != nil {
		s.setError(fmt.Errorf("set WebRTC local description: %w", err))
		return VideoAnswer{}, err
	}

	select {
	case <-gatherComplete:
	case <-ctx.Done():
		err := fmt.Errorf("create WebRTC answer: %w", ctx.Err())
		s.setError(err)
		return VideoAnswer{}, err
	case <-time.After(5 * time.Second):
		err := errors.New("create WebRTC answer: ICE gathering timed out")
		s.setError(err)
		return VideoAnswer{}, err
	}

	localDescription := peerConnection.LocalDescription()
	if localDescription == nil {
		err := errors.New("create WebRTC answer: local description is missing")
		s.setError(err)
		return VideoAnswer{}, err
	}

	sessionID, sessionCtx, cancelSession := s.registerSession()
	peerConnection.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateConnected:
			s.setState("connected", "")
		case webrtc.PeerConnectionStateDisconnected,
			webrtc.PeerConnectionStateFailed,
			webrtc.PeerConnectionStateClosed:
			cancelSession()
		}
	})

	go func() {
		defer func() {
			cancelSession()
			_ = peerConnection.Close()
			s.unregisterSession(sessionID)
		}()

		if err := s.relayRTSPToTrack(sessionCtx, videoTrack); err != nil {
			s.setError(err)
		}
	}()

	closePeer = false
	return VideoAnswer{Type: localDescription.Type.String(), SDP: localDescription.SDP}, nil
}

func (s *VideoService) validateOffer(offer VideoOffer) error {
	if strings.ToLower(strings.TrimSpace(offer.Type)) != "offer" {
		return fmt.Errorf("%w: expected offer type", ErrLocalVideoInvalidOffer)
	}
	if strings.TrimSpace(offer.SDP) == "" {
		return fmt.Errorf("%w: missing SDP", ErrLocalVideoInvalidOffer)
	}
	return nil
}

func (s *VideoService) isEnabled() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status.Enabled
}

func (s *VideoService) newPeerConnection() (*webrtc.PeerConnection, error) {
	var settingEngine webrtc.SettingEngine

	if s.webrtcUDPPortMin != 0 || s.webrtcUDPPortMax != 0 {
		if err := settingEngine.SetEphemeralUDPPortRange(s.webrtcUDPPortMin, s.webrtcUDPPortMax); err != nil {
			return nil, fmt.Errorf("configure WebRTC UDP port range: %w", err)
		}
	}

	if len(s.webrtcICENATIPs) > 0 {
		settingEngine.SetNAT1To1IPs(s.webrtcICENATIPs, webrtc.ICECandidateTypeHost)
	}

	return webrtc.NewAPI(webrtc.WithSettingEngine(settingEngine)).NewPeerConnection(webrtc.Configuration{})
}

func (s *VideoService) relayRTSPToTrack(ctx context.Context, track *webrtc.TrackLocalStaticRTP) error {
	if s.rtspTransport == "udp" {
		if err := s.relayRTSPToTrackWithTransport(ctx, track, "udp"); err != nil {
			if errors.Is(err, errLocalVideoNoRTPPackets) && ctx.Err() == nil {
				s.setState("starting", "no UDP RTP packets received; retrying RTSP over TCP")
				return s.relayRTSPToTrackWithTransport(ctx, track, "tcp")
			}
			return err
		}
		return nil
	}

	return s.relayRTSPToTrackWithTransport(ctx, track, s.rtspTransport)
}

func (s *VideoService) relayRTSPToTrackWithTransport(ctx context.Context, track *webrtc.TrackLocalStaticRTP, transport string) error {
	u, err := base.ParseURL(s.rtspURL)
	if err != nil {
		return fmt.Errorf("parse RTSP URL: %w", err)
	}

	protocol := gortsplib.ProtocolUDP
	if transport == "tcp" {
		protocol = gortsplib.ProtocolTCP
	}
	client := &gortsplib.Client{
		Scheme:                u.Scheme,
		Host:                  u.Host,
		Protocol:              &protocol,
		ReadTimeout:           10 * time.Second,
		WriteTimeout:          10 * time.Second,
		InitialUDPReadTimeout: 3 * time.Second,
		UDPReadBufferSize:     defaultVideoUDPReadBufferBytes,
		UserAgent:             "atlas-backend",
	}

	if err := client.Start(); err != nil {
		return fmt.Errorf("connect to RTSP source: %w", err)
	}
	defer client.Close()

	closed := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			client.Close()
		case <-closed:
		}
	}()
	defer close(closed)

	desc, _, err := client.Describe(u)
	if err != nil {
		return fmt.Errorf("describe RTSP source: %w", err)
	}

	var h264Format *format.H264
	media := desc.FindFormat(&h264Format)
	if media == nil {
		return errors.New("RTSP source does not expose an H.264 video stream")
	}

	if _, err := client.Setup(desc.BaseURL, media, 0, 0); err != nil {
		return fmt.Errorf("setup RTSP H.264 media: %w", err)
	}

	rtpPackets := make(chan *rtp.Packet, s.rtpBufferSize)
	writerDone := make(chan error, 1)
	firstPacket := make(chan struct{})
	var firstPacketOnce sync.Once
	go func() {
		for pkt := range rtpPackets {
			if err := track.WriteRTP(pkt); err != nil {
				if errors.Is(err, io.ErrClosedPipe) {
					writerDone <- nil
				} else {
					writerDone <- fmt.Errorf("write RTP packet to WebRTC track: %w", err)
				}
				return
			}
			s.recordFrame(localVideoCodecH264)
		}
		writerDone <- nil
	}()

	client.OnPacketRTP(media, h264Format, func(pkt *rtp.Packet) {
		if ctx.Err() != nil {
			return
		}

		firstPacketOnce.Do(func() {
			close(firstPacket)
		})

		packet := cloneRTPPacket(pkt)
		select {
		case rtpPackets <- packet:
		default:
			select {
			case <-rtpPackets:
			default:
			}
			select {
			case rtpPackets <- packet:
			default:
			}
		}
	})

	if _, err := client.Play(nil); err != nil {
		return fmt.Errorf("start RTSP playback: %w", err)
	}

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- client.Wait()
	}()

	var relayErr error
	writerFinished := false
	waitFinished := false
	select {
	case <-firstPacket:
		select {
		case err := <-writerDone:
			writerFinished = true
			if err != nil {
				relayErr = err
			}
		case err := <-waitDone:
			waitFinished = true
			if err != nil && ctx.Err() == nil && !errors.Is(err, io.ErrClosedPipe) {
				relayErr = fmt.Errorf("RTSP playback stopped: %w", err)
			}
		case <-ctx.Done():
		}
	case <-time.After(defaultVideoFirstPacketTimeout):
		relayErr = fmt.Errorf("%w using %s", errLocalVideoNoRTPPackets, transport)
	case err := <-writerDone:
		writerFinished = true
		if err != nil {
			relayErr = err
		}
	case err := <-waitDone:
		waitFinished = true
		if ctx.Err() == nil {
			if err != nil && !errors.Is(err, io.ErrClosedPipe) {
				relayErr = fmt.Errorf("%w using %s: RTSP playback stopped before first packet: %v", errLocalVideoNoRTPPackets, transport, err)
			} else {
				relayErr = fmt.Errorf("%w using %s: RTSP playback stopped before first packet", errLocalVideoNoRTPPackets, transport)
			}
		}
	case <-ctx.Done():
	}

	client.Close()
	if !waitFinished {
		<-waitDone
	}

	close(rtpPackets)
	if !writerFinished {
		if err := <-writerDone; err != nil && relayErr == nil {
			relayErr = err
		}
	}

	return relayErr
}

func cloneRTPPacket(pkt *rtp.Packet) *rtp.Packet {
	if len(pkt.Header.Extensions) > 0 {
		raw, err := pkt.Marshal()
		if err == nil {
			var cloned rtp.Packet
			if err := cloned.Unmarshal(raw); err == nil {
				return &cloned
			}
		}
	}

	cloned := *pkt
	if pkt.Payload != nil {
		cloned.Payload = append([]byte(nil), pkt.Payload...)
	}
	if pkt.Raw != nil {
		cloned.Raw = append([]byte(nil), pkt.Raw...)
	}
	cloned.Header.CSRC = append([]uint32(nil), pkt.Header.CSRC...)
	cloned.Header.Extensions = append([]rtp.Extension(nil), pkt.Header.Extensions...)
	return &cloned
}

func (s *VideoService) registerSession() (string, context.Context, context.CancelFunc) {
	sessionCtx, cancel := context.WithCancel(context.Background())
	sessionID := fmt.Sprintf("local-video-%d", time.Now().UTC().UnixNano())

	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sessionID] = cancel
	s.status.ActivePeers = len(s.sessions)
	s.status.UpdatedAt = time.Now().UTC()
	return sessionID, sessionCtx, cancel
}

func (s *VideoService) unregisterSession(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.sessions, sessionID)
	s.status.ActivePeers = len(s.sessions)
	if s.status.ActivePeers == 0 && s.status.Enabled && s.status.State != "failed" {
		s.status.State = "configured"
	}
	s.status.UpdatedAt = time.Now().UTC()
}

func (s *VideoService) setState(state string, lastError string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.State = state
	s.status.LastError = lastError
	s.status.UpdatedAt = time.Now().UTC()
}

func (s *VideoService) setError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if errors.Is(err, ErrLocalVideoDisabled) {
		s.status.State = "disabled"
		s.status.WebRTCReady = false
	} else {
		s.status.State = "failed"
	}
	s.status.LastError = err.Error()
	s.status.UpdatedAt = time.Now().UTC()
}

func (s *VideoService) recordFrame(codec string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status.Codec = codec
	s.status.State = "streaming"
	s.status.LastFrameAt = time.Now().UTC()
	s.status.LastError = ""
	s.status.UpdatedAt = s.status.LastFrameAt
}

func drainRTCP(sender *webrtc.RTPSender) {
	buf := make([]byte, 1500)
	for {
		if _, _, err := sender.Read(buf); err != nil {
			return
		}
	}
}
