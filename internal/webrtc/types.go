package webrtc

import (
	"time"

	pwebrtc "github.com/pion/webrtc/v3"

	"github.com/tomaslejdung/gopeep/internal/encoding"
)

// CodecType is an alias for encoding.CodecType
type CodecType = encoding.CodecType

// Codec constants
const (
	CodecVP8  = encoding.CodecVP8
	CodecVP9  = encoding.CodecVP9
	CodecH264 = encoding.CodecH264
)

// DefaultICEServers are the default STUN servers for NAT traversal
var DefaultICEServers = []pwebrtc.ICEServer{
	{URLs: []string{"stun:stun.l.google.com:19302"}},
	{URLs: []string{"stun:stun1.l.google.com:19302"}},
	{URLs: []string{"stun:stun2.l.google.com:19302"}},
}

// ViewerInfo holds information about a connected viewer
type ViewerInfo struct {
	PeerID         string
	State          string // connecting, connected, disconnected
	ConnectedAt    time.Time
	ConnectionType string // "direct", "relay", or "unknown"
}

// ICEConfig holds ICE server configuration
type ICEConfig struct {
	TURNServer string
	TURNUser   string
	TURNPass   string
	ForceRelay bool
}

// StreamTrackInfo holds information about a single stream/track
type StreamTrackInfo struct {
	TrackID    string // e.g., "video0", "video1"
	WindowID   uint32
	WindowName string
	AppName    string
	Track      *pwebrtc.TrackLocalStaticSample
	IsFocused  bool
	Width      int
	Height     int
}

// StreamPipelineStats holds real-time statistics for a single stream
type StreamPipelineStats struct {
	TrackID   string  // Stream identifier
	AppName   string  // Application name (e.g., "VS Code")
	Width     int     // Current resolution width
	Height    int     // Current resolution height
	FPS       float64 // Current frames per second
	Bitrate   float64 // Current bitrate in kbps
	Frames    uint64  // Total frames encoded
	Bytes     uint64  // Total bytes sent
	IsFocused bool    // Whether this stream has focus
}

// PeerInfo holds peer connection and associated RTP senders
type PeerInfo struct {
	PC            *pwebrtc.PeerConnection
	Senders       map[string]*pwebrtc.RTPSender // trackID -> sender
	Renegotiating bool                          // Whether renegotiation is in progress
	ControlDC     *pwebrtc.DataChannel          // DataChannel for control messages
}

// TrackSlot represents a pre-allocated track slot for instant window sharing
// All 4 slots are created upfront and included in the initial SDP offer,
// eliminating the need for renegotiation when adding new windows.
type TrackSlot struct {
	TrackID string                          // "video0", "video1", etc.
	Track   *pwebrtc.TrackLocalStaticSample // The actual WebRTC track
	Active  bool                            // Whether this slot has an active stream
	Info    *StreamTrackInfo                // Window info when active, nil when inactive
}

// SlotInfo contains information about an active slot after recreation
type SlotInfo struct {
	TrackID    string
	WindowID   uint32
	WindowName string
	AppName    string
	Track      *pwebrtc.TrackLocalStaticSample
	IsFocused  bool
}

// GetMimeType returns the WebRTC MIME type for a codec
func GetMimeType(codecType CodecType) string {
	switch codecType {
	case CodecVP9:
		return pwebrtc.MimeTypeVP9
	case CodecH264:
		return pwebrtc.MimeTypeH264
	default:
		return pwebrtc.MimeTypeVP8
	}
}
