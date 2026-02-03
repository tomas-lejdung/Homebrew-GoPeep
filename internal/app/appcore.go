package app

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/tomaslejdung/gopeep/internal/config"
	sig "github.com/tomaslejdung/gopeep/internal/signal"
	"github.com/tomaslejdung/gopeep/internal/streaming"
	"github.com/tomaslejdung/gopeep/internal/webrtc"
)

// AppCore owns the shared application state that both TUI and Overlay need.
// It provides thread-safe access to streaming, signaling, and selection state.
type AppCore struct {
	mu     sync.RWMutex
	Config config.Config

	// Selection state
	selectedWindows     map[uint32]bool
	fullscreenSelected  bool
	autoShareEnabled    bool
	autoShareFocusTimes map[uint32]time.Time
	osFocusedWindowID   uint32

	// Streaming state
	peerManager     *webrtc.PeerManager
	streamer        *streaming.Streamer
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
func NewAppCore(cfg config.Config) *AppCore {
	return &AppCore{
		Config:              cfg,
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
func (c *AppCore) GetStreamer() *streaming.Streamer {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.streamer
}

// GetPeerManager returns the peer manager.
func (c *AppCore) GetPeerManager() *webrtc.PeerManager {
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
func (c *AppCore) SetStreamer(s *streaming.Streamer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.streamer = s
}

// SetPeerManager sets the peer manager.
func (c *AppCore) SetPeerManager(pm *webrtc.PeerManager) {
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

// SetShareURL sets just the share URL.
func (c *AppCore) SetShareURL(url string) {
	c.mu.Lock()
	defer c.mu.Unlock()
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

// GetAutoShareFocusTimes returns the autoShareFocusTimes map (for iteration).
func (c *AppCore) GetAutoShareFocusTimes() map[uint32]time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.autoShareFocusTimes
}

// InitAutoShareFocusTimes ensures the map is initialized.
func (c *AppCore) InitAutoShareFocusTimes() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.autoShareFocusTimes == nil {
		c.autoShareFocusTimes = make(map[uint32]time.Time)
	}
}

// ClearAutoShareFocusTimes clears the focus times map.
func (c *AppCore) ClearAutoShareFocusTimes() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.autoShareFocusTimes = nil
}

// ClearFocusTime removes the focus time for a window.
func (c *AppCore) ClearFocusTime(windowID uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.autoShareFocusTimes, windowID)
}

// --- Config Access ---

// GetConfig returns the config.
func (c *AppCore) GetConfig() config.Config {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Config
}

// --- Additional Streaming Getters ---

// IsFullscreen returns true if sharing fullscreen.
func (c *AppCore) IsFullscreen() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.isFullscreen
}

// GetActiveWindowID returns the active window ID.
func (c *AppCore) GetActiveWindowID() uint32 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.activeWindowID
}

// IsAdaptiveBitrate returns true if adaptive bitrate is enabled.
func (c *AppCore) IsAdaptiveBitrate() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.adaptiveBitrate
}

// IsQualityMode returns true if quality mode (vs performance mode).
func (c *AppCore) IsQualityMode() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.qualityMode
}

// GetStartTime returns the sharing start time.
func (c *AppCore) GetStartTime() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.startTime
}

// --- Additional Signaling Getters ---

// GetRoomSecret returns the room secret.
func (c *AppCore) GetRoomSecret() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.roomSecret
}

// GetWSConn returns the WebSocket connection.
func (c *AppCore) GetWSConn() *websocket.Conn {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.wsConn
}

// GetSharer returns the sharer interface.
func (c *AppCore) GetSharer() sig.Sharer {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sharer
}

// IsServerStarted returns true if server has started.
func (c *AppCore) IsServerStarted() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.serverStarted
}

// --- Reconnection Getters ---

// IsReconnecting returns true if currently reconnecting.
func (c *AppCore) IsReconnecting() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.reconnecting
}

// GetReconnectAttempt returns the current reconnect attempt number.
func (c *AppCore) GetReconnectAttempt() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.reconnectAttempt
}

// GetReconnectDelay returns the reconnect delay duration.
func (c *AppCore) GetReconnectDelay() time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.reconnectDelay
}

// GetMaxReconnects returns the max reconnection attempts.
func (c *AppCore) GetMaxReconnects() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.maxReconnects
}

// IsWSDisconnected returns true if WebSocket is disconnected.
func (c *AppCore) IsWSDisconnected() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.wsDisconnected == nil {
		return false
	}
	return *c.wsDisconnected
}

// GetWSDisconnectedPtr returns the pointer to wsDisconnected flag.
func (c *AppCore) GetWSDisconnectedPtr() *bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.wsDisconnected
}

// --- Focus Getters ---

// GetOSFocusedWindowID returns the OS-focused window ID.
func (c *AppCore) GetOSFocusedWindowID() uint32 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.osFocusedWindowID
}

// --- Password Getters ---

// IsPasswordEnabled returns true if password protection is enabled.
func (c *AppCore) IsPasswordEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.passwordEnabled
}

// GetPassword returns the password.
func (c *AppCore) GetPassword() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.password
}

// --- Additional Streaming Setters ---

// SetIsFullscreen sets the fullscreen state.
func (c *AppCore) SetIsFullscreen(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.isFullscreen = v
}

// SetActiveWindowID sets the active window ID.
func (c *AppCore) SetActiveWindowID(id uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.activeWindowID = id
}

// SetAdaptiveBitrate sets adaptive bitrate mode.
func (c *AppCore) SetAdaptiveBitrate(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.adaptiveBitrate = v
}

// SetQualityMode sets quality mode.
func (c *AppCore) SetQualityMode(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.qualityMode = v
}

// --- Additional Signaling Setters ---

// SetWSConn sets the WebSocket connection.
func (c *AppCore) SetWSConn(conn *websocket.Conn) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wsConn = conn
}

// SetSharer sets the sharer interface.
func (c *AppCore) SetSharer(s sig.Sharer) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sharer = s
}

// SetServerStarted sets the server started state.
func (c *AppCore) SetServerStarted(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.serverStarted = v
}

// --- Reconnection Setters ---

// SetReconnecting sets the reconnecting state.
func (c *AppCore) SetReconnecting(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reconnecting = v
}

// SetReconnectAttempt sets the reconnect attempt number.
func (c *AppCore) SetReconnectAttempt(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reconnectAttempt = n
}

// SetReconnectDelay sets the reconnect delay.
func (c *AppCore) SetReconnectDelay(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reconnectDelay = d
}

// SetMaxReconnects sets the max reconnection attempts.
func (c *AppCore) SetMaxReconnects(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.maxReconnects = n
}

// SetWSDisconnected sets the wsDisconnected pointer.
func (c *AppCore) SetWSDisconnected(v *bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wsDisconnected = v
}

// --- Focus Setters ---

// SetOSFocusedWindowID sets the OS-focused window ID.
func (c *AppCore) SetOSFocusedWindowID(id uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.osFocusedWindowID = id
}

// --- Password Setters ---

// SetPasswordEnabled sets password protection state.
func (c *AppCore) SetPasswordEnabled(v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.passwordEnabled = v
}

// SetPassword sets the password.
func (c *AppCore) SetPassword(p string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.password = p
}
