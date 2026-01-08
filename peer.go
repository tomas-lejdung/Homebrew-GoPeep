package main

import (
	"time"

	"github.com/pion/webrtc/v3"
)

// ICE servers for NAT traversal
var defaultICEServers = []webrtc.ICEServer{
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
