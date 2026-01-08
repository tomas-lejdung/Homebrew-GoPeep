package signal

// SignalMessage represents a WebSocket signaling message
type SignalMessage struct {
	Type      string `json:"type"`                // join, offer, answer, ice, error, streams-info, focus-change, stream-added, stream-removed, stream-activated, stream-deactivated, renegotiate-answer, size-change
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
	StreamAdded   *StreamInfo  `json:"streamAdded,omitempty"`   // info about newly added stream (legacy, requires renegotiation)
	StreamRemoved string       `json:"streamRemoved,omitempty"` // trackID of removed stream (legacy, requires renegotiation)

	// Pre-allocated slots fields (no renegotiation needed)
	StreamActivated   *StreamInfo `json:"streamActivated,omitempty"`   // info about activated stream (fast path, no renegotiation)
	StreamDeactivated string      `json:"streamDeactivated,omitempty"` // trackID of deactivated stream (fast path, no renegotiation)

	// Size change fields (for size-change message type)
	Width  int `json:"width,omitempty"`  // new width after resize
	Height int `json:"height,omitempty"` // new height after resize
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
