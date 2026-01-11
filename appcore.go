package main

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
	sig "github.com/tomaslejdung/gopeep/pkg/signal"
)

// AppCore owns the shared application state that both TUI and Overlay need.
// It provides thread-safe access to streaming, signaling, and selection state.
type AppCore struct {
	mu     sync.RWMutex
	config Config

	// Selection state
	selectedWindows     map[uint32]bool
	fullscreenSelected  bool
	autoShareEnabled    bool
	autoShareFocusTimes map[uint32]time.Time
	osFocusedWindowID   uint32

	// Streaming state
	peerManager     *PeerManager
	streamer        *Streamer
	sharing         bool
	starting        bool
	isFullscreen    bool
	activeWindowID  uint32
	adaptiveBitrate bool
	qualityMode     bool

	// Signaling state
	roomCode         string
	roomSecret       string
	shareURL         string
	viewerCount      int
	startTime        time.Time
	wsConn           *websocket.Conn
	sharer           sig.Sharer
	reconnecting     bool
	reconnectAttempt int
	reconnectDelay   time.Duration
	maxReconnects    int
	wsDisconnected   *bool
	serverStarted    bool

	// Password protection
	passwordEnabled bool
	password        string
}

// NewAppCore creates a new AppCore with the given config.
func NewAppCore(config Config) *AppCore {
	return &AppCore{
		config:              config,
		selectedWindows:     make(map[uint32]bool),
		autoShareFocusTimes: make(map[uint32]time.Time),
		maxReconnects:       5,
		reconnectDelay:      time.Second,
	}
}

// --- Selection Getters ---

// GetSelectedWindows returns a copy of the selected windows map.
func (c *AppCore) GetSelectedWindows() map[uint32]bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make(map[uint32]bool)
	for k, v := range c.selectedWindows {
		result[k] = v
	}
	return result
}

// IsWindowSelected returns true if the given window is selected.
func (c *AppCore) IsWindowSelected(windowID uint32) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.selectedWindows[windowID]
}

// GetSelectedCount returns the number of selected windows.
func (c *AppCore) GetSelectedCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.selectedWindows)
}

// IsFullscreenSelected returns true if fullscreen is selected.
func (c *AppCore) IsFullscreenSelected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.fullscreenSelected
}

// IsAutoShareEnabled returns true if auto-share mode is enabled.
func (c *AppCore) IsAutoShareEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.autoShareEnabled
}

// HasSelection returns true if anything is selected.
func (c *AppCore) HasSelection() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.fullscreenSelected || len(c.selectedWindows) > 0
}

// --- Streaming Getters ---

// IsSharing returns true if currently sharing.
func (c *AppCore) IsSharing() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sharing
}

// IsStarting returns true if capture is starting.
func (c *AppCore) IsStarting() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.starting
}

// GetViewerCount returns the current viewer count.
func (c *AppCore) GetViewerCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.viewerCount
}

// GetRoomCode returns the room code.
func (c *AppCore) GetRoomCode() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.roomCode
}

// GetShareURL returns the share URL.
func (c *AppCore) GetShareURL() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.shareURL
}

// GetStreamer returns the streamer (for stats access).
func (c *AppCore) GetStreamer() *Streamer {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.streamer
}

// GetPeerManager returns the peer manager.
func (c *AppCore) GetPeerManager() *PeerManager {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.peerManager
}

// --- Selection Setters (called by SelectionManager) ---

// SetSelectedWindows updates the selected windows map.
func (c *AppCore) SetSelectedWindows(windows map[uint32]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.selectedWindows = make(map[uint32]bool)
	for k, v := range windows {
		c.selectedWindows[k] = v
	}
}

// SelectWindow adds a window to selection.
func (c *AppCore) SelectWindow(windowID uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.selectedWindows[windowID] = true
}

// DeselectWindow removes a window from selection.
func (c *AppCore) DeselectWindow(windowID uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.selectedWindows, windowID)
}

// SetFullscreenSelected sets the fullscreen selection state.
func (c *AppCore) SetFullscreenSelected(selected bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fullscreenSelected = selected
	if selected {
		// Clear window selection when enabling fullscreen
		c.selectedWindows = make(map[uint32]bool)
	}
}

// SetAutoShareEnabled sets the auto-share mode.
func (c *AppCore) SetAutoShareEnabled(enabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.autoShareEnabled = enabled
}

// ClearSelection clears all selections.
func (c *AppCore) ClearSelection() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fullscreenSelected = false
	c.selectedWindows = make(map[uint32]bool)
}

// --- Streaming Setters ---

// SetSharing sets the sharing state.
func (c *AppCore) SetSharing(sharing bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sharing = sharing
}

// SetStarting sets the starting state.
func (c *AppCore) SetStarting(starting bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.starting = starting
}

// SetStreamer sets the streamer.
func (c *AppCore) SetStreamer(s *Streamer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.streamer = s
}

// SetPeerManager sets the peer manager.
func (c *AppCore) SetPeerManager(pm *PeerManager) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.peerManager = pm
}

// SetViewerCount sets the viewer count.
func (c *AppCore) SetViewerCount(count int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.viewerCount = count
}

// SetRoomCode sets the room code and share URL.
func (c *AppCore) SetRoomCode(code, secret, url string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.roomCode = code
	c.roomSecret = secret
	c.shareURL = url
}

// SetStartTime sets the sharing start time.
func (c *AppCore) SetStartTime(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.startTime = t
}

// --- Focus Time Tracking ---

// TrackFocusTime updates the focus time for a window (for LRU eviction).
func (c *AppCore) TrackFocusTime(windowID uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.autoShareFocusTimes == nil {
		c.autoShareFocusTimes = make(map[uint32]time.Time)
	}
	c.autoShareFocusTimes[windowID] = time.Now()
}

// GetFocusTime returns the last focus time for a window.
func (c *AppCore) GetFocusTime(windowID uint32) time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.autoShareFocusTimes[windowID]
}

// ClearFocusTime removes the focus time for a window.
func (c *AppCore) ClearFocusTime(windowID uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.autoShareFocusTimes, windowID)
}

// --- Config Access ---

// GetConfig returns the config.
func (c *AppCore) GetConfig() Config {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.config
}
