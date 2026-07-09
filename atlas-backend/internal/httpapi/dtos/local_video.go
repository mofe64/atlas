package dtos

type LocalVideoStatusResponse struct {
	Enabled     bool   `json:"enabled"`
	SourceID    string `json:"sourceId,omitempty"`
	RTSPURL     string `json:"rtspUrl,omitempty"`
	State       string `json:"state"`
	WebRTCReady bool   `json:"webrtcReady"`
	Codec       string `json:"codec,omitempty"`
	ActivePeers int    `json:"activePeers"`
	LastFrameAt string `json:"lastFrameAt,omitempty"`
	LastError   string `json:"lastError,omitempty"`
	UpdatedAt   string `json:"updatedAt,omitempty"`
}

type LocalVideoOfferRequest struct {
	Type string `json:"type"`
	SDP  string `json:"sdp"`
}

type LocalVideoAnswerResponse struct {
	Type string `json:"type"`
	SDP  string `json:"sdp"`
}
