package signal

// SignalMessage represents a WebSocket signaling message
type SignalMessage struct {
	Type      string `json:"type"`                // join, offer, answer, ice, error, streams-info, focus-change, stream-added, stream-removed, renegotiate-answer
	Room      string `json:"room,omitempty"`      // room code
	Role      string `json:"role,omitempty"`      // sharer or viewer
	SDP       string `json:"sdp,omitempty"`       // SDP offer/answer
	Candidate string `json:"candidate,omitempty"` // ICE candidate
	Error     string `json:"error,omitempty"`     // error message
	PeerID    string `json:"peerId,omitempty"`    // peer identifier for routing
	Password  string `json:"password,omitempty"`  // room password (for joining protected rooms)
	TrackID   string `json:"trackId,omitempty"`   // track identifier for multi-stream

	// Multi-stream fields
	Streams       []StreamInfo `json:"streams,omitempty"`       // list of available streams
	FocusedTrack  string       `json:"focusedTrack,omitempty"`  // currently focused stream track ID
	StreamAdded   *StreamInfo  `json:"streamAdded,omitempty"`   // info about newly added stream
	StreamRemoved string       `json:"streamRemoved,omitempty"` // trackID of removed stream
}

// StreamInfo describes a single video stream (window)
type StreamInfo struct {
	TrackID    string `json:"trackId"`    // WebRTC track ID (e.g., "video0", "video1")
	WindowName string `json:"windowName"` // Window title/name
	AppName    string `json:"appName"`    // Application name
	IsFocused  bool   `json:"isFocused"`  // Whether this window has OS focus
	Width      int    `json:"width"`      // Window width
	Height     int    `json:"height"`     // Window height
}
