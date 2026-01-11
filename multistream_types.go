package main

import (
	"github.com/pion/webrtc/v3"
)

// StreamTrackInfo holds information about a single stream/track
type StreamTrackInfo struct {
	TrackID    string // e.g., "video0", "video1"
	WindowID   uint32
	WindowName string
	AppName    string
	Track      *webrtc.TrackLocalStaticSample
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
	PC            *webrtc.PeerConnection
	Senders       map[string]*webrtc.RTPSender // trackID -> sender
	renegotiating bool                         // Whether renegotiation is in progress
}

// TrackSlot represents a pre-allocated track slot for instant window sharing
// All 4 slots are created upfront and included in the initial SDP offer,
// eliminating the need for renegotiation when adding new windows.
type TrackSlot struct {
	TrackID string                         // "video0", "video1", etc.
	Track   *webrtc.TrackLocalStaticSample // The actual WebRTC track
	Active  bool                           // Whether this slot has an active stream
	Info    *StreamTrackInfo               // Window info when active, nil when inactive
}

// SlotInfo contains information about an active slot after recreation
type SlotInfo struct {
	TrackID    string
	WindowID   uint32
	WindowName string
	AppName    string
	Track      *webrtc.TrackLocalStaticSample
	IsFocused  bool
}
