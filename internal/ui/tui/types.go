package tui

import (
	"time"

	"github.com/gorilla/websocket"
	"github.com/tomaslejdung/gopeep/internal/capture"
	"github.com/tomaslejdung/gopeep/internal/streaming"
	"github.com/tomaslejdung/gopeep/internal/webrtc"
)

// reconnectMsg indicates the WebSocket needs reconnection
type reconnectMsg struct {
	attempt int
	delay   time.Duration
}

// reconnectedMsg indicates WebSocket reconnection succeeded
type reconnectedMsg struct {
	conn *websocket.Conn
}

// reconnectFailedMsg indicates WebSocket reconnection failed
type reconnectFailedMsg struct {
	err string
}

// osFocusChangedMsg indicates OS window focus changed
type osFocusChangedMsg struct {
	windowID uint32
}

// roomCodeReceivedMsg indicates room code was received from server
type roomCodeReceivedMsg struct {
	roomCode   string
	roomSecret string
	err        error
}

// windowsUpdatedMsg contains updated window list
type windowsUpdatedMsg struct {
	windows []capture.WindowInfo
}

// viewerCountMsg contains the current viewer count
type viewerCountMsg int

// tickMsg is a periodic tick for UI updates
type tickMsg time.Time

// captureStartedMsg indicates capture started successfully (unified for single/multi)
type captureStartedMsg struct {
	Streamer    *streaming.Streamer
	PeerManager *webrtc.PeerManager
}

// captureErrorMsg indicates capture failed to start
type captureErrorMsg struct {
	err string
}

// overlayToggleMsg indicates the overlay button was clicked
type overlayToggleMsg struct {
	windowID uint32
}

// overlayFullscreenToggleMsg indicates the fullscreen button was clicked
type overlayFullscreenToggleMsg struct{}

// overlayClearAllMsg indicates the clear all button was clicked
type overlayClearAllMsg struct{}

// fastTickMsg is a faster tick for UI animations
type fastTickMsg time.Time

// SourceItem represents a selectable source (fullscreen or window)
type SourceItem struct {
	IsFullscreen bool
	Window       *capture.WindowInfo // nil for fullscreen
	DisplayName  string
}

// Column indices
const (
	columnSources = 0
	columnQuality = 1
	columnFPS     = 2
	columnCodec   = 3
)
