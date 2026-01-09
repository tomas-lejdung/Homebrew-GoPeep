package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/gorilla/websocket"
	"github.com/tomaslejdung/gopeep/pkg/overlay"
	"github.com/tomaslejdung/gopeep/pkg/settings"
	sig "github.com/tomaslejdung/gopeep/pkg/signal"
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
	roomCode string
	err      error
}

// copyToClipboard copies text to the macOS clipboard using pbcopy
func copyToClipboard(text string) error {
	cmd := exec.Command("pbcopy")
	pipe, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	if _, err := pipe.Write([]byte(text)); err != nil {
		return err
	}
	if err := pipe.Close(); err != nil {
		return err
	}
	return cmd.Wait()
}

// normalizeSignalURL converts HTTP URLs to WebSocket URLs
func normalizeSignalURL(url string) string {
	if strings.HasPrefix(url, "http://") {
		return "ws://" + strings.TrimPrefix(url, "http://")
	} else if strings.HasPrefix(url, "https://") {
		return "wss://" + strings.TrimPrefix(url, "https://")
	} else if !strings.HasPrefix(url, "ws://") && !strings.HasPrefix(url, "wss://") {
		return "wss://" + url
	}
	return url
}

// requestRoomCodeFromServer requests a unique room code from the signal server
func requestRoomCodeFromServer(signalURL string) tea.Cmd {
	return func() tea.Msg {
		// Convert WebSocket URL to HTTP URL for the API call
		apiURL := signalURL
		apiURL = strings.Replace(apiURL, "wss://", "https://", 1)
		apiURL = strings.Replace(apiURL, "ws://", "http://", 1)
		apiURL = strings.TrimSuffix(apiURL, "/") + "/api/reserve"

		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Post(apiURL, "application/json", nil)
		if err != nil {
			return roomCodeReceivedMsg{err: fmt.Errorf("failed to request room code: %w", err)}
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return roomCodeReceivedMsg{err: fmt.Errorf("server returned status %d", resp.StatusCode)}
		}

		var result struct {
			Room string `json:"room"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return roomCodeReceivedMsg{err: fmt.Errorf("failed to decode response: %w", err)}
		}

		return roomCodeReceivedMsg{roomCode: result.Room}
	}
}

// Column indices
const (
	columnSources = 0
	columnQuality = 1
	columnFPS     = 2
	columnCodec   = 3
)

// Styles
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("12"))

	selectedStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("10"))

	normalStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("7"))

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("8"))

	statusStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("14"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("9"))

	urlStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("13"))

	viewerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("11"))

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("8"))

	// Keybind styles
	keyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("14")) // Cyan for keys

	keySepStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("8")) // Dim separator

	toggleActiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("10")) // Green for active toggles

	toggleInactiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("8")) // Dim for inactive toggles

	// Box styles for columns
	activeBoxStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("12")).
			Padding(0, 1)

	inactiveBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("8")).
				Padding(0, 1)

	boxTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("12"))

	boxTitleDimStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color("8"))
)

// Messages
type windowsUpdatedMsg struct {
	windows []WindowInfo
}

type viewerCountMsg int

type tickMsg time.Time

// captureStartedMsg indicates capture started successfully (unified for single/multi)
type captureStartedMsg struct {
	streamer    *Streamer
	peerManager *PeerManager
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

// SourceItem represents a selectable source (fullscreen or window)
type SourceItem struct {
	IsFullscreen bool
	Window       *WindowInfo // nil for fullscreen
	DisplayName  string
}

// Model
type model struct {
	// Config
	config Config

	// Sources (fullscreen + windows)
	sources        []SourceItem
	sourceCursor   int
	selectedSource int // -1 if not sharing (single-window mode)

	// Multi-window mode (always used now - single window is just len(selectedWindows)==1)
	selectedWindows    map[uint32]bool // window IDs selected for streaming
	fullscreenSelected bool            // true if fullscreen is selected (mutually exclusive with selectedWindows)
	adaptiveBitrate    bool            // reduce bitrate for non-focused windows
	qualityMode        bool            // false = performance, true = quality (uses CQ/CRF)
	streamer           *Streamer       // unified streamer (handles 1 or more windows)
	peerManager        *PeerManager    // unified peer manager

	// Quality
	qualityCursor   int
	selectedQuality int

	// FPS
	fpsCursor   int
	selectedFPS int

	// Codec
	codecCursor   int
	selectedCodec int

	// Navigation: 0 = sources, 1 = quality, 2 = fps, 3 = codec
	activeColumn int

	// Sharing state
	sharing        bool
	starting       bool   // true while capture is starting (async)
	isFullscreen   bool   // true if sharing fullscreen
	activeWindowID uint32 // window ID being shared (for restarts)
	roomCode       string
	shareURL       string
	viewerCount    int
	lastError      string
	startTime      time.Time // when sharing started
	copyMessage    string    // temporary "Copied!" message
	copyMsgTime    time.Time // when copy message was shown

	// Stats display
	showStats   bool
	streamStats []StreamPipelineStats // Per-stream stats from unified streamer

	// OS focus tracking
	osFocusedWindowID uint32 // Currently OS-focused window ID

	// Auto-share mode (automatically shares the topmost window)
	autoShareEnabled    bool                 // true when in auto-share mode
	autoShareFocusTimes map[uint32]time.Time // track last focus time per window for LRU eviction

	// Password protection
	passwordEnabled bool
	password        string

	// Reconnection state (for remote signal server)
	reconnecting     bool
	reconnectAttempt int
	reconnectDelay   time.Duration
	maxReconnects    int
	wsDisconnected   *bool // Pointer so goroutine can set it

	// Components (persistent across source switches)
	server   *sig.Server
	wsConn   *websocket.Conn // Remote signal server connection
	isRemote bool            // Using remote signal server

	// Server started flag
	serverStarted bool

	// Terminal dimensions
	width  int
	height int

	// Overlay components
	overlay           *overlay.Overlay
	overlayController *OverlayController

	// Selection manager (centralizes all selection logic)
	selection *SelectionManager
}

// SelectionManager handles all selection state changes centrally.
// TUI and overlay should use these methods instead of manipulating state directly.
// This is a stateless helper - methods receive model as parameter for bubbletea compatibility.
type SelectionManager struct{}

// --- Mutation Methods ---

// ToggleFullscreen toggles fullscreen selection (F key / overlay button).
// When enabling fullscreen, clears all window selections.
func (SelectionManager) ToggleFullscreen(m *model) (tea.Model, tea.Cmd) {
	if len(m.sources) == 0 || !m.sources[0].IsFullscreen {
		return *m, nil
	}

	m.fullscreenSelected = !m.fullscreenSelected

	if m.fullscreenSelected {
		// Enabling fullscreen clears windows
		m.selectedWindows = make(map[uint32]bool)
	}

	m.sourceCursor = 0
	m.syncOverlay()

	return selectionPostChange(m)
}

// ToggleWindow toggles a window's selection (Space key on window / overlay click).
// Selecting a window always clears fullscreen mode.
// Handles capacity limits with LRU eviction.
func (SelectionManager) ToggleWindow(m *model, windowID uint32) (tea.Model, tea.Cmd) {
	// Selecting/toggling a window always clears fullscreen
	m.fullscreenSelected = false

	if m.selectedWindows[windowID] {
		// Deselect
		delete(m.selectedWindows, windowID)
		delete(m.autoShareFocusTimes, windowID)
	} else {
		// Select - enforce capacity with LRU eviction
		if len(m.selectedWindows) >= MaxCaptureInstances {
			lruID := m.getLRUWindow(windowID)
			if lruID != 0 {
				delete(m.selectedWindows, lruID)
				delete(m.autoShareFocusTimes, lruID)
				log.Printf("SelectionManager: Evicted LRU window %d to make room", lruID)
			}
		}
		m.selectedWindows[windowID] = true
		selectionTrackFocusTime(m, windowID)
	}

	m.syncOverlay()

	return selectionPostChange(m)
}

// SelectWindow ensures a window is selected (doesn't toggle, for explicit selection).
// Clears fullscreen mode and handles capacity limits.
func (SelectionManager) SelectWindow(m *model, windowID uint32) (tea.Model, tea.Cmd) {
	// Clear fullscreen when selecting a window
	m.fullscreenSelected = false

	if !m.selectedWindows[windowID] {
		// Not already selected - add it
		if len(m.selectedWindows) >= MaxCaptureInstances {
			lruID := m.getLRUWindow(windowID)
			if lruID != 0 {
				delete(m.selectedWindows, lruID)
				delete(m.autoShareFocusTimes, lruID)
				log.Printf("SelectionManager: Evicted LRU window %d to make room", lruID)
			}
		}
		m.selectedWindows[windowID] = true
		selectionTrackFocusTime(m, windowID)
	}

	m.syncOverlay()

	return selectionPostChange(m)
}

// DeselectWindow removes a window from selection.
func (SelectionManager) DeselectWindow(m *model, windowID uint32) (tea.Model, tea.Cmd) {
	if m.selectedWindows[windowID] {
		delete(m.selectedWindows, windowID)
		delete(m.autoShareFocusTimes, windowID)
		m.syncOverlay()
		return selectionPostChange(m)
	}

	return *m, nil
}

// ClearSelection clears all selections (windows and fullscreen).
func (SelectionManager) ClearSelection(m *model) (tea.Model, tea.Cmd) {
	m.fullscreenSelected = false
	m.selectedWindows = make(map[uint32]bool)
	m.syncOverlay()

	return selectionPostChange(m)
}

// --- Getter Methods ---

// IsFullscreenSelected returns true if fullscreen mode is selected.
func (SelectionManager) IsFullscreenSelected(m *model) bool {
	return m.fullscreenSelected
}

// IsWindowSelected returns true if the given window is selected.
func (SelectionManager) IsWindowSelected(m *model, windowID uint32) bool {
	return m.selectedWindows[windowID]
}

// GetSelectedWindows returns a slice of selected window IDs.
func (SelectionManager) GetSelectedWindows(m *model) []uint32 {
	result := make([]uint32, 0, len(m.selectedWindows))
	for id := range m.selectedWindows {
		result = append(result, id)
	}
	return result
}

// GetSelectedCount returns number of selected windows (0 if fullscreen).
func (SelectionManager) GetSelectedCount(m *model) int {
	return len(m.selectedWindows)
}

// HasSelection returns true if anything is selected (fullscreen or windows).
func (SelectionManager) HasSelection(m *model) bool {
	return m.fullscreenSelected || len(m.selectedWindows) > 0
}

// IsSharing returns true if currently streaming.
func (SelectionManager) IsSharing(m *model) bool {
	return m.sharing
}

// CanAddWindow returns true if another window can be added (capacity check).
func (SelectionManager) CanAddWindow(m *model) bool {
	return len(m.selectedWindows) < MaxCaptureInstances
}

// --- Internal Helper Functions ---

// selectionPostChange handles stream updates after selection changes.
// If sharing: updates stream dynamically.
// If not sharing but has selection: starts sharing (Quick Share).
func selectionPostChange(m *model) (tea.Model, tea.Cmd) {
	if m.sharing && m.streamer != nil {
		// Already sharing - dynamically update
		return m.updateMultiStreamSelection()
	}

	// Not sharing - check if we should Quick Share
	if m.fullscreenSelected || len(m.selectedWindows) > 0 {
		log.Printf("Quick Share: Starting with %d windows, fullscreen=%v",
			len(m.selectedWindows), m.fullscreenSelected)
		return m.startMultiWindowSharing()
	}

	return *m, nil
}

// selectionTrackFocusTime records the focus time for LRU eviction.
func selectionTrackFocusTime(m *model, windowID uint32) {
	if m.autoShareFocusTimes == nil {
		m.autoShareFocusTimes = make(map[uint32]time.Time)
	}
	m.autoShareFocusTimes[windowID] = time.Now()
}

// findSourceIndex returns the index of the source matching the current capture state.
// Returns -1 if not found (window closed or not in list).
func (m *model) findSourceIndex() int {
	if !m.sharing && !m.starting {
		return -1
	}

	if m.isFullscreen {
		// Fullscreen is always index 0
		if len(m.sources) > 0 && m.sources[0].IsFullscreen {
			return 0
		}
		return -1
	}

	// Find window by ID
	for i, source := range m.sources {
		if !source.IsFullscreen && source.Window != nil && source.Window.ID == m.activeWindowID {
			return i
		}
	}
	return -1
}

func initialModel(config Config) model {
	// Set default signal URL if not in local mode and not already set
	if config.SignalURL == "" && !config.LocalMode {
		config.SignalURL = DefaultSignalServer
	}

	// Initialize available codecs
	InitAvailableCodecs()

	// Load saved settings
	savedSettings, err := settings.Load()
	if err != nil {
		log.Printf("Failed to load settings: %v", err)
		savedSettings = settings.DefaultSettings()
	}

	// Validate indices after InitAvailableCodecs()
	if savedSettings.Quality < 0 || savedSettings.Quality >= len(QualityPresets) {
		savedSettings.Quality = DefaultQualityIndex()
	}
	if savedSettings.FPS < 0 || savedSettings.FPS >= len(FPSPresets) {
		savedSettings.FPS = DefaultFPSIndex()
	}
	if savedSettings.Codec < 0 || savedSettings.Codec >= len(AvailableCodecs) {
		savedSettings.Codec = DefaultCodecIndex()
	}

	// CLI flags override saved settings (30 is the default FPS flag value)
	fpsIndex := savedSettings.FPS
	if config.FPS != 30 {
		fpsIndex = FPSIndexForValue(config.FPS)
	}

	return model{
		config:          config,
		sourceCursor:    0,
		selectedSource:  -1,
		selectedWindows: make(map[uint32]bool),
		qualityCursor:   savedSettings.Quality,
		selectedQuality: savedSettings.Quality,
		fpsCursor:       fpsIndex,
		selectedFPS:     fpsIndex,
		codecCursor:     savedSettings.Codec,
		selectedCodec:   savedSettings.Codec,
		adaptiveBitrate: savedSettings.AdaptiveBitrate,
		qualityMode:     savedSettings.QualityMode,
		activeColumn:    columnSources,
		maxReconnects:   10, // Max reconnection attempts
		selection:       &SelectionManager{},
	}
}

func (m model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		refreshWindows,
		tea.SetWindowTitle("GoPeep - Screen Sharing"),
	}

	// Generate room code on startup
	if !m.config.LocalMode && m.config.SignalURL != "" {
		// Remote mode: request from server
		signalURL := normalizeSignalURL(m.config.SignalURL)
		cmds = append(cmds, requestRoomCodeFromServer(signalURL))
	} else {
		// Local mode: generate immediately
		cmds = append(cmds, func() tea.Msg {
			code := sig.GenerateRoomCode()
			return roomCodeReceivedMsg{roomCode: code}
		})
	}

	return tea.Batch(cmds...)
}

func refreshWindows() tea.Msg {
	windows, _ := ListWindows()
	return windowsUpdatedMsg{windows: windows}
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// fastTickMsg is used for rapid focus checking in auto-share mode
type fastTickMsg time.Time

func fastTickCmd() tea.Cmd {
	// Slow backup tick (500ms) - most focus detection happens via NSWorkspace notifications
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg {
		return fastTickMsg(t)
	})
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case windowsUpdatedMsg:
		// Build sources list: fullscreen first, then windows
		newSources := []SourceItem{
			{IsFullscreen: true, DisplayName: "Fullscreen (Primary Display)"},
		}
		for i := range msg.windows {
			w := msg.windows[i]
			newSources = append(newSources, SourceItem{
				IsFullscreen: false,
				Window:       &w,
				DisplayName:  w.DisplayName(),
			})
		}

		// Sync overlay state on window updates
		m.syncOverlay()

		// If we're actively streaming and got an empty window list, keep existing sources
		// (ScreenCaptureKit can sometimes return empty transiently)
		if (m.sharing || m.starting) && len(msg.windows) == 0 && len(m.sources) > 1 {
			// Keep existing sources and selection - don't change anything
			return m, nil
		}

		m.sources = newSources

		// Reconcile selection: find the source matching our active capture by window ID
		// Only do this if we're actively sharing/starting AND the current selectedSource
		// doesn't already point to the correct window
		if m.sharing || m.starting {
			// Check if current selectedSource is still valid
			currentValid := false
			if m.selectedSource >= 0 && m.selectedSource < len(m.sources) {
				source := m.sources[m.selectedSource]
				if m.isFullscreen && source.IsFullscreen {
					currentValid = true
				} else if !m.isFullscreen && !source.IsFullscreen && source.Window != nil && source.Window.ID == m.activeWindowID {
					currentValid = true
				}
			}

			// Only reconcile if current selection is invalid
			if !currentValid {
				m.selectedSource = m.findSourceIndex()
			}
		}

		// Keep cursor in bounds
		if m.sourceCursor >= len(m.sources) {
			m.sourceCursor = max(0, len(m.sources)-1)
		}

		return m, nil

	case viewerCountMsg:
		m.viewerCount = int(msg)
		return m, nil

	case roomCodeReceivedMsg:
		if msg.err != nil {
			if m.config.LocalMode {
				// Local mode: generate locally
				m.roomCode = sig.GenerateRoomCode()
				log.Printf("Generated local room code: %s", m.roomCode)
			} else {
				// Remote mode: server MUST provide the room code - show error
				log.Printf("Failed to get room code from server: %v", msg.err)
				m.lastError = fmt.Sprintf("Server error: %v (is the server updated?)", msg.err)
				return m, nil
			}
		} else {
			m.roomCode = msg.roomCode
			log.Printf("Received room code from server: %s", m.roomCode)
		}

		// Initialize server synchronously (not in a Cmd - Bubbletea model changes don't persist in goroutines)
		if err := m.initMultiServer(); err != nil {
			log.Printf("Failed to initialize server: %v", err)
			m.lastError = err.Error()
		}
		return m, nil

	case captureStartedMsg:
		// Capture started successfully (unified for single/multi)
		m.starting = false
		m.sharing = true
		m.streamer = msg.streamer
		m.peerManager = msg.peerManager
		m.startTime = time.Now()
		m.showStats = true // Show stats by default when sharing starts
		m.syncOverlay()    // Update overlay state (now sharing)
		// Notify viewers that sharer has started (so they can rejoin)
		if m.server != nil && m.roomCode != "" {
			log.Printf("Broadcasting sharer-started to room %s", m.roomCode)
			m.server.BroadcastToViewers(m.roomCode, sig.SignalMessage{Type: "sharer-started"})
		} else {
			log.Printf("Cannot broadcast sharer-started: server=%v roomCode=%s", m.server != nil, m.roomCode)
		}
		// If in auto-share mode, start fast tick for rapid focus detection
		if m.autoShareEnabled {
			return m, tea.Batch(tickCmd(), fastTickCmd())
		}
		return m, tickCmd()

	case captureErrorMsg:
		// Capture failed - reset state fully
		m.starting = false
		m.sharing = false
		m.selectedSource = -1
		m.isFullscreen = false
		m.activeWindowID = 0
		m.lastError = msg.err
		return m, refreshWindows

	case osFocusChangedMsg:
		// OS focus changed - update the tracked window ID
		m.osFocusedWindowID = msg.windowID
		return m, nil

	case overlayToggleMsg:
		// Overlay button was clicked - toggle window selection
		return m.handleOverlayToggle(msg.windowID)

	case overlayFullscreenToggleMsg:
		// Fullscreen button was clicked - toggle fullscreen mode
		if m.autoShareEnabled {
			return m, nil
		}
		return m.selection.ToggleFullscreen(&m)

	case overlayClearAllMsg:
		// Clear all button was clicked - stop sharing and clear selection
		return m.selection.ClearSelection(&m)

	case tickMsg:
		// Periodic refresh (1 second)
		var cmds []tea.Cmd
		cmds = append(cmds, tickCmd())

		// Refresh windows list
		cmds = append(cmds, refreshWindows)

		// Poll for topmost window among all visible windows (z-order based)
		// Collect all window IDs from sources
		var allWindowIDs []uint32
		for _, source := range m.sources {
			if !source.IsFullscreen && source.Window != nil {
				allWindowIDs = append(allWindowIDs, source.Window.ID)
			}
		}
		if len(allWindowIDs) > 0 {
			topmostWindow := GetTopmostWindow(allWindowIDs)
			if topmostWindow != m.osFocusedWindowID {
				m.osFocusedWindowID = topmostWindow
			}
		}

		// Update overlay state
		m.syncOverlay()

		// Update viewer count and stats if sharing
		if m.sharing && m.peerManager != nil {
			m.viewerCount = m.peerManager.GetConnectionCount()
		}
		if m.sharing && m.streamer != nil {
			m.streamStats = m.streamer.GetStats()
		}

		// Clear copy message after 2 seconds
		if m.copyMessage != "" && time.Since(m.copyMsgTime) > 2*time.Second {
			m.copyMessage = ""
		}

		// Check if our window was closed (if streaming a window)
		if m.sharing && !m.isFullscreen && m.activeWindowID != 0 {
			// If window is no longer in the sources list, stop capture
			if m.selectedSource == -1 {
				m.stopCapture(false)
				m.lastError = "Window was closed"
			}
		}

		// Check for WebSocket disconnection and trigger reconnection
		if m.isRemote && m.serverStarted && m.wsDisconnected != nil && *m.wsDisconnected && !m.reconnecting {
			*m.wsDisconnected = false
			m.reconnecting = true
			m.reconnectAttempt = 1
			m.reconnectDelay = time.Second
			cmds = append(cmds, m.attemptReconnect(1, time.Second))
		}

		return m, tea.Batch(cmds...)

	case fastTickMsg:
		// Auto-share mode: automatically add/remove windows based on OS focus
		// Uses same multi-stream infrastructure as normal mode
		if m.autoShareEnabled && m.sharing && m.streamer != nil {
			// Check if focus changed via OS notification (instant detection)
			if CheckFocusChanged() {
				log.Printf("Auto-share: Focus change detected via OS notification")
			}

			// Extract window IDs from m.sources (already in memory - cheap)
			var windowIDs []uint32
			for _, source := range m.sources {
				if !source.IsFullscreen && source.Window != nil {
					windowIDs = append(windowIDs, source.Window.ID)
				}
			}

			// Find topmost window by z-order
			topmost := GetTopmostWindow(windowIDs)

			if topmost != 0 {
				// Update focus time for LRU tracking
				if m.autoShareFocusTimes == nil {
					m.autoShareFocusTimes = make(map[uint32]time.Time)
				}
				m.autoShareFocusTimes[topmost] = time.Now()

				// Check if this window is already streaming
				if !m.streamer.IsWindowStreaming(topmost) {
					// Find window info from m.sources
					var topmostWindow *WindowInfo
					for _, source := range m.sources {
						if !source.IsFullscreen && source.Window != nil && source.Window.ID == topmost {
							topmostWindow = source.Window
							break
						}
					}

					if topmostWindow != nil {
						windowName := topmostWindow.WindowName
						if windowName == "" {
							windowName = topmostWindow.OwnerName
						}

						// Check if pool is full (4 windows)
						if m.streamer.GetActiveStreamCount() >= MaxCaptureInstances {
							// Remove LRU window to make room
							lruWindowID := m.getLRUWindow(topmost)
							if lruWindowID != 0 {
								log.Printf("Auto-share: Pool full, removing LRU window %d", lruWindowID)
								if err := m.streamer.RemoveWindowDynamic(lruWindowID); err != nil {
									log.Printf("Auto-share: Failed to remove LRU window: %v", err)
								} else {
									delete(m.selectedWindows, lruWindowID)
									delete(m.autoShareFocusTimes, lruWindowID)
								}
							}
						}

						// Add new window
						log.Printf("Auto-share: Adding window %d (%s)", topmost, windowName)
						if _, err := m.streamer.AddWindowDynamic(*topmostWindow); err != nil {
							log.Printf("Auto-share: Failed to add window: %v", err)
						} else {
							m.selectedWindows[topmost] = true
							log.Printf("Auto-share: Successfully added %s", windowName)
						}
					}
				}
				// If window is already streaming, focus detection loop handles the focus change
			}

			// Sync overlay to update window count display
			m.syncOverlay()

			// Continue ticking while in auto-share mode
			return m, fastTickCmd()
		}

		// If auto-share enabled but not sharing yet, keep ticking
		if m.autoShareEnabled {
			return m, fastTickCmd()
		}

		// If no longer in auto-share mode, don't continue fast tick
		return m, nil

	case reconnectMsg:
		// WebSocket disconnected, attempt reconnection
		m.reconnecting = true
		m.reconnectAttempt = msg.attempt
		m.reconnectDelay = msg.delay
		return m, m.attemptReconnect(msg.attempt, msg.delay)

	case reconnectedMsg:
		// Reconnection successful - store the new connection and set up signaling
		m.reconnecting = false
		m.reconnectAttempt = 0
		m.lastError = ""
		m.wsConn = msg.conn
		// Reset disconnect flag
		if m.wsDisconnected != nil {
			*m.wsDisconnected = false
		}
		// Set up signaling via the new WebSocket with disconnect callback
		disconnectFlag := m.wsDisconnected
		setupRemotePeerSignaling(m.wsConn, m.peerManager, func() {
			if disconnectFlag != nil {
				*disconnectFlag = true
			}
		})
		return m, nil

	case reconnectFailedMsg:
		// Reconnection failed
		m.reconnecting = false
		m.lastError = msg.err
		m.serverStarted = false
		return m, nil
	}

	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		m.cleanup()
		return m, tea.Quit

	case "tab", "right", "l":
		// Switch to next column (sources <-> right panel)
		if m.activeColumn == columnSources {
			m.activeColumn = columnQuality
		} else {
			m.activeColumn = columnSources
		}
		return m, nil

	case "shift+tab", "left", "h":
		// Switch to previous column
		if m.activeColumn == columnSources {
			m.activeColumn = columnQuality
		} else {
			m.activeColumn = columnSources
		}
		return m, nil

	case "up", "k":
		if m.activeColumn == columnSources {
			if m.sourceCursor > 0 {
				m.sourceCursor--
			}
		} else if m.activeColumn == columnQuality {
			if m.qualityCursor > 0 {
				m.qualityCursor--
			}
			// At top of quality, can't go higher
		} else if m.activeColumn == columnFPS {
			if m.fpsCursor > 0 {
				m.fpsCursor--
			} else {
				// Move from FPS to quality section
				m.activeColumn = columnQuality
				m.qualityCursor = len(QualityPresets) - 1
			}
		} else if m.activeColumn == columnCodec {
			if m.codecCursor > 0 {
				m.codecCursor--
			} else {
				// Move from codec to FPS section
				m.activeColumn = columnFPS
				m.fpsCursor = len(FPSPresets) - 1
			}
		}
		return m, nil

	case "down", "j":
		if m.activeColumn == columnSources {
			if m.sourceCursor < len(m.sources)-1 {
				m.sourceCursor++
			}
		} else if m.activeColumn == columnQuality {
			if m.qualityCursor < len(QualityPresets)-1 {
				m.qualityCursor++
			} else {
				// At bottom of quality, move to FPS section
				m.activeColumn = columnFPS
				m.fpsCursor = 0
			}
		} else if m.activeColumn == columnFPS {
			if m.fpsCursor < len(FPSPresets)-1 {
				m.fpsCursor++
			} else {
				// At bottom of FPS, move to codec section
				m.activeColumn = columnCodec
				m.codecCursor = 0
			}
		} else if m.activeColumn == columnCodec {
			if m.codecCursor < len(AvailableCodecs)-1 {
				m.codecCursor++
			}
		}
		return m, nil

	case "enter":
		// In auto-share mode, ignore source selection via enter
		if m.activeColumn == columnSources && m.autoShareEnabled {
			return m, nil
		}
		if m.activeColumn == columnSources {
			// Start sharing based on selection (fullscreen or windows)
			if m.fullscreenSelected {
				return m.startMultiWindowSharing() // Will handle fullscreen via streamer
			}
			if len(m.selectedWindows) > 0 {
				return m.startMultiWindowSharing()
			}
			// If nothing selected, select current item and start
			if m.sourceCursor < len(m.sources) {
				source := m.sources[m.sourceCursor]
				if source.IsFullscreen {
					m.fullscreenSelected = true
					return m.startMultiWindowSharing()
				} else if source.Window != nil {
					m.selectedWindows[source.Window.ID] = true
					return m.startMultiWindowSharing()
				}
			}
		} else if m.activeColumn == columnQuality {
			return m.applyQuality(m.qualityCursor)
		} else if m.activeColumn == columnFPS {
			return m.applyFPS(m.fpsCursor)
		} else if m.activeColumn == columnCodec {
			return m.applyCodec(m.codecCursor)
		}
		return m, nil

	case " ":
		// In auto-share mode, ignore source selection via space
		if m.activeColumn == columnSources && m.autoShareEnabled {
			return m, nil
		}
		if m.activeColumn == columnSources {
			// Toggle source selection (fullscreen or windows, mutually exclusive)
			if m.sourceCursor < len(m.sources) {
				source := m.sources[m.sourceCursor]
				if source.IsFullscreen {
					return m.selection.ToggleFullscreen(&m)
				} else if source.Window != nil {
					return m.selection.ToggleWindow(&m, source.Window.ID)
				}
			}
			return m, nil
		} else if m.activeColumn == columnQuality {
			return m.applyQuality(m.qualityCursor)
		} else if m.activeColumn == columnFPS {
			return m.applyFPS(m.fpsCursor)
		} else if m.activeColumn == columnCodec {
			return m.applyCodec(m.codecCursor)
		}
		return m, nil

	case "s":
		// Stop sharing (but keep server running)
		// Clear selections so user must reselect to start again
		// Close peer connections so viewers reconnect with fresh state
		if m.sharing {
			// Notify viewers that sharer has stopped so they reset and wait
			if m.server != nil && m.roomCode != "" {
				m.server.BroadcastToViewers(m.roomCode, sig.SignalMessage{Type: "sharer-stopped"})
			}
			m.stopCapture(false)
			m.selectedWindows = make(map[uint32]bool)
			m.fullscreenSelected = false
			if m.peerManager != nil {
				m.peerManager.CloseAllConnections()
			}
		}
		return m, nil

	case "r":
		// Refresh windows
		return m, refreshWindows

	// F for fullscreen - toggles fullscreen selection (mutually exclusive with windows)
	case "f":
		// Disabled in auto-share mode
		if m.autoShareEnabled {
			return m, nil
		}
		return m.selection.ToggleFullscreen(&m)

	// Quick window selection with number keys (1-9 selects windows, skipping fullscreen)
	// Disabled in auto-share mode
	case "1":
		if m.autoShareEnabled {
			return m, nil
		}
		return m.selectWindowByNumber(1)
	case "2":
		if m.autoShareEnabled {
			return m, nil
		}
		return m.selectWindowByNumber(2)
	case "3":
		if m.autoShareEnabled {
			return m, nil
		}
		return m.selectWindowByNumber(3)
	case "4":
		if m.autoShareEnabled {
			return m, nil
		}
		return m.selectWindowByNumber(4)
	case "5":
		if m.autoShareEnabled {
			return m, nil
		}
		return m.selectWindowByNumber(5)
	case "6":
		if m.autoShareEnabled {
			return m, nil
		}
		return m.selectWindowByNumber(6)
	case "7":
		if m.autoShareEnabled {
			return m, nil
		}
		return m.selectWindowByNumber(7)
	case "8":
		if m.autoShareEnabled {
			return m, nil
		}
		return m.selectWindowByNumber(8)
	case "9":
		if m.autoShareEnabled {
			return m, nil
		}
		return m.selectWindowByNumber(9)

	case "i":
		// Toggle stats display
		m.showStats = !m.showStats
		return m, nil

	case "c":
		// Copy URL to clipboard
		if m.shareURL != "" {
			if err := copyToClipboard(m.shareURL); err == nil {
				m.copyMessage = "Copied!"
				m.copyMsgTime = time.Now()
			} else {
				m.copyMessage = "Copy failed"
				m.copyMsgTime = time.Now()
			}
		}
		return m, nil

	case "p":
		// Toggle password protection
		m.passwordEnabled = !m.passwordEnabled
		if m.passwordEnabled {
			m.password = sig.GeneratePassword()
		} else {
			m.password = ""
		}
		// If server is already started, update the room password
		if m.serverStarted && m.server != nil {
			m.server.UpdateRoomPassword(m.roomCode, m.password)
		}
		return m, nil

	case "a":
		// Toggle adaptive bitrate
		m.adaptiveBitrate = !m.adaptiveBitrate
		// Update if already streaming
		if m.streamer != nil {
			m.streamer.SetAdaptiveBitrate(m.adaptiveBitrate)
		}
		return m, nil

	case "A": // Shift+A - Toggle auto-share mode
		return m.toggleAutoShareMode()

	case "q":
		// Toggle quality mode (quality vs performance)
		m.qualityMode = !m.qualityMode
		// Update if already streaming
		if m.streamer != nil {
			m.streamer.SetQualityMode(m.qualityMode)
		}
		return m, nil
	}

	return m, nil
}

// applyQuality changes the quality setting
func (m model) applyQuality(index int) (tea.Model, tea.Cmd) {
	if index < 0 || index >= len(QualityPresets) {
		return m, nil
	}

	oldQuality := m.selectedQuality
	m.selectedQuality = index
	m.qualityCursor = index

	// If we're sharing and quality changed, apply new bitrate dynamically
	if m.sharing && oldQuality != m.selectedQuality {
		return m.applyBitrateChange()
	}

	return m, nil
}

// applyCodec changes the codec setting dynamically without full restart
func (m model) applyCodec(index int) (tea.Model, tea.Cmd) {
	if index < 0 || index >= len(AvailableCodecs) {
		return m, nil
	}

	oldCodec := m.selectedCodec
	m.selectedCodec = index
	m.codecCursor = index

	// If we're sharing and codec changed, update dynamically
	if m.sharing && m.streamer != nil && oldCodec != m.selectedCodec {
		codecType := m.getSelectedCodecType()
		if err := m.streamer.SetCodec(codecType); err != nil {
			m.lastError = fmt.Sprintf("Codec change failed: %v", err)
		}
	}

	return m, nil
}

// selectWindowByNumber toggles window selection by its display number (1-9)
// Windows are numbered starting from 1, excluding fullscreen
func (m model) selectWindowByNumber(num int) (tea.Model, tea.Cmd) {
	// Find the nth non-fullscreen source
	windowCount := 0
	for i, source := range m.sources {
		if !source.IsFullscreen && source.Window != nil {
			windowCount++
			if windowCount == num {
				m.sourceCursor = i
				return m.selection.ToggleWindow(&m, source.Window.ID)
			}
		}
	}
	return m, nil
}

// handleOverlayToggle handles the overlay button click to toggle window selection.
// When not sharing, this acts as "Quick Share" - selecting the window and starting
// sharing immediately (like pressing Enter in the TUI).
// When already sharing, this toggles the window selection.
func (m model) handleOverlayToggle(windowID uint32) (tea.Model, tea.Cmd) {
	// Check if window exists in sources
	found := false
	for _, source := range m.sources {
		if !source.IsFullscreen && source.Window != nil && source.Window.ID == windowID {
			found = true
			break
		}
	}

	if !found {
		// Window not in sources list - try to get its info directly via CGWindowList
		// This handles the case where gopeep was started in a different Space
		windowInfo := GetWindowInfoByID(windowID)
		if windowInfo == nil {
			// Window doesn't exist or is invalid
			log.Printf("Overlay: Window %d not found via CGWindowList, ignoring", windowID)
			return m, nil
		}

		// Add window to sources dynamically so it shows in the TUI
		log.Printf("Overlay: Dynamically adding window %d (%s) to sources", windowID, windowInfo.DisplayName())
		m.sources = append(m.sources, SourceItem{Window: windowInfo})
	}

	return m.selection.ToggleWindow(&m, windowID)
}

// syncOverlay updates the overlay controller with current state.
// The overlay handles its own focus detection via a background thread.
func (m *model) syncOverlay() {
	if m.overlayController != nil {
		m.overlayController.Sync(m.selectedWindows, m.sharing, m.autoShareEnabled, m.viewerCount, m.fullscreenSelected)
	}
	// Note: The overlay now runs its own update loop via background thread,
	// so we don't need to call Refresh() here. The overlay queries state
	// through the controller callbacks (goGetWindowState, goIsManualMode).
}

// getSelectedCodecType returns the currently selected codec type
func (m model) getSelectedCodecType() CodecType {
	if m.selectedCodec >= 0 && m.selectedCodec < len(AvailableCodecs) {
		return AvailableCodecs[m.selectedCodec].Type
	}
	return CodecVP8
}

// getSelectedFPS returns the currently selected FPS value
func (m model) getSelectedFPS() int {
	if m.selectedFPS >= 0 && m.selectedFPS < len(FPSPresets) {
		return FPSPresets[m.selectedFPS].Value
	}
	return 30 // default
}

// getLRUWindow returns the least recently focused window ID for eviction
// excludeWindowID is the window that should not be evicted (typically the new focused window)
func (m model) getLRUWindow(excludeWindowID uint32) uint32 {
	var lruWindowID uint32
	var lruTime time.Time
	first := true

	for windowID := range m.selectedWindows {
		if windowID == excludeWindowID {
			continue // Don't evict the window we're about to focus
		}

		focusTime, exists := m.autoShareFocusTimes[windowID]
		if !exists {
			// Window with no focus time = oldest, evict immediately
			return windowID
		}

		if first || focusTime.Before(lruTime) {
			lruWindowID = windowID
			lruTime = focusTime
			first = false
		}
	}
	return lruWindowID
}

// applyFPS changes the FPS setting dynamically without full restart
func (m model) applyFPS(index int) (tea.Model, tea.Cmd) {
	if index < 0 || index >= len(FPSPresets) {
		return m, nil
	}

	oldFPS := m.selectedFPS
	m.selectedFPS = index
	m.fpsCursor = index

	// If we're sharing and FPS changed, update dynamically
	if m.sharing && m.streamer != nil && oldFPS != m.selectedFPS {
		fps := m.getSelectedFPS()
		if err := m.streamer.SetFPS(fps); err != nil {
			m.lastError = fmt.Sprintf("FPS change failed: %v", err)
		}
	}

	return m, nil
}

// applyBitrateChange applies a new bitrate to the running streamer without restart
func (m model) applyBitrateChange() (tea.Model, tea.Cmd) {
	if !m.sharing || m.streamer == nil {
		return m, nil
	}

	// Use SetBitrate to change bitrate dynamically (no restart needed)
	bitrate := QualityPresets[m.selectedQuality].Bitrate
	m.streamer.SetBitrate(bitrate, bitrate/2)

	return m, nil
}

// toggleAutoShareMode toggles the auto-share mode on/off
// When enabled, the app automatically shares whichever window has OS focus
// Works exactly like normal mode but with automatic window management
func (m model) toggleAutoShareMode() (tea.Model, tea.Cmd) {
	if m.autoShareEnabled {
		// Disable auto-share mode - keep windows streaming (switch to manual mode)
		log.Printf("Auto-share: Disabling mode, switching to manual management")
		StopFocusObserver()
		m.autoShareEnabled = false
		m.autoShareFocusTimes = nil
		// DON'T stop streaming - windows stay selected for manual management
		m.syncOverlay() // Show overlay in manual mode
		return m, nil
	}

	// Enable auto-share mode
	log.Printf("Auto-share: Enabling mode, starting focus observer")
	StartFocusObserver()
	m.autoShareEnabled = true
	m.autoShareFocusTimes = make(map[uint32]time.Time)
	m.fullscreenSelected = false // Disable fullscreen in auto mode
	m.syncOverlay()              // Hide overlay in auto mode

	// If already sharing, keep existing windows and initialize LRU times
	if m.sharing && m.streamer != nil {
		log.Printf("Auto-share: Already sharing, keeping existing %d windows", len(m.selectedWindows))
		// Initialize focus times for existing windows
		now := time.Now()
		for windowID := range m.selectedWindows {
			m.autoShareFocusTimes[windowID] = now
		}
		return m, fastTickCmd()
	}

	// Not sharing yet - start with focused window
	m.selectedWindows = make(map[uint32]bool)

	// Get all shareable windows and find topmost by z-order
	windows, err := ListWindows()
	if err != nil {
		m.lastError = fmt.Sprintf("Failed to list windows: %v", err)
		StopFocusObserver()
		m.autoShareEnabled = false
		return m, nil
	}

	if len(windows) == 0 {
		m.lastError = "No shareable windows found"
		StopFocusObserver()
		m.autoShareEnabled = false
		return m, nil
	}

	// Extract window IDs for z-order check
	var windowIDs []uint32
	for _, w := range windows {
		windowIDs = append(windowIDs, w.ID)
	}

	// Find topmost window by z-order
	topmost := GetTopmostWindow(windowIDs)

	if topmost == 0 {
		m.lastError = "No topmost window found"
		StopFocusObserver()
		m.autoShareEnabled = false
		return m, nil
	}

	// Find window info for the topmost window
	var targetWindow *WindowInfo
	for i := range windows {
		if windows[i].ID == topmost {
			targetWindow = &windows[i]
			break
		}
	}

	if targetWindow == nil {
		m.lastError = "Topmost window not in list"
		StopFocusObserver()
		m.autoShareEnabled = false
		return m, nil
	}

	// Initialize focus time for first window
	m.autoShareFocusTimes[topmost] = time.Now()

	// Start sharing this window directly (bypass m.sources lookup)
	return m.startAutoShareCapture(*targetWindow)
}

// startAutoShareCapture starts capture for a specific window in auto-share mode
// This bypasses the normal m.sources lookup to ensure the window is captured
func (m model) startAutoShareCapture(window WindowInfo) (tea.Model, tea.Cmd) {
	if m.starting || m.sharing {
		return m, nil
	}

	m.stopCapture(false)
	if !m.serverStarted {
		m.stopMultiCapture()
	}
	m.lastError = ""

	// Initialize server
	if err := m.initMultiServer(); err != nil {
		m.lastError = err.Error()
		StopFocusObserver()
		m.autoShareEnabled = false
		return m, nil
	}

	m.starting = true
	m.selectedWindows = make(map[uint32]bool)
	m.selectedWindows[window.ID] = true

	// Capture config
	fps := m.getSelectedFPS()
	focusBitrate := QualityPresets[m.selectedQuality].Bitrate
	bgBitrate := focusBitrate / 3
	if bgBitrate < 500 {
		bgBitrate = 500
	}
	adaptiveBR := m.adaptiveBitrate
	qualityMode := m.qualityMode
	codecType := m.getSelectedCodecType()

	// Start capture with just this one window, and start fast tick for focus detection
	captureCmd := startMultiCaptureAsync(m.peerManager, []WindowInfo{window}, false, fps, focusBitrate, bgBitrate, adaptiveBR, qualityMode, codecType)
	return m, tea.Batch(captureCmd, fastTickCmd())
}

// attemptReconnect tries to reconnect to the remote signal server
func (m model) attemptReconnect(attempt int, delay time.Duration) tea.Cmd {
	return func() tea.Msg {
		// Wait for the delay
		time.Sleep(delay)

		// Try to reconnect
		signalURL := normalizeSignalURL(m.config.SignalURL)

		// Build WebSocket URL
		wsURL := strings.TrimSuffix(signalURL, "/") + "/ws/" + m.roomCode

		// Try connecting with timeout
		dialer := websocket.Dialer{
			HandshakeTimeout: 5 * time.Second,
		}
		conn, _, err := dialer.Dial(wsURL, nil)
		if err != nil {
			// Calculate next delay with exponential backoff
			nextDelay := delay * 2
			if nextDelay > 30*time.Second {
				nextDelay = 30 * time.Second
			}

			if attempt >= m.maxReconnects {
				return reconnectFailedMsg{err: "Failed to reconnect after multiple attempts"}
			}

			return reconnectMsg{attempt: attempt + 1, delay: nextDelay}
		}

		// Join as sharer (with optional password)
		joinMsg := sig.SignalMessage{Type: "join", Role: "sharer", Password: m.password}
		if err := conn.WriteJSON(joinMsg); err != nil {
			conn.Close()
			return reconnectMsg{attempt: attempt + 1, delay: delay * 2}
		}

		// Wait for join confirmation
		var joinResp sig.SignalMessage
		if err := conn.ReadJSON(&joinResp); err != nil {
			conn.Close()
			return reconnectMsg{attempt: attempt + 1, delay: delay * 2}
		}
		if joinResp.Type == "error" {
			conn.Close()
			return reconnectFailedMsg{err: joinResp.Error}
		}

		// Success - return the new connection
		return reconnectedMsg{conn: conn}
	}
}

// initRemoteSignaling connects to the remote signal server
func (m *model) initRemoteSignaling() error {
	signalURL := normalizeSignalURL(m.config.SignalURL)

	// Build WebSocket URL
	wsURL := strings.TrimSuffix(signalURL, "/") + "/ws/" + m.roomCode

	// Build viewer URL
	viewerURL := strings.Replace(signalURL, "wss://", "https://", 1)
	viewerURL = strings.Replace(viewerURL, "ws://", "http://", 1)
	m.shareURL = strings.TrimSuffix(viewerURL, "/") + "/" + m.roomCode

	// Try connecting with timeout
	dialer := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
	}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		return fmt.Errorf("failed to connect to signal server: %v", err)
	}

	// Join as sharer (with optional password)
	joinMsg := sig.SignalMessage{Type: "join", Role: "sharer", Password: m.password}
	if err := conn.WriteJSON(joinMsg); err != nil {
		conn.Close()
		return fmt.Errorf("failed to send join message: %v", err)
	}

	// Wait for join confirmation
	var joinResp sig.SignalMessage
	if err := conn.ReadJSON(&joinResp); err != nil {
		conn.Close()
		return fmt.Errorf("failed to read join response: %v", err)
	}
	if joinResp.Type == "error" {
		conn.Close()
		return fmt.Errorf("failed to join room: %s", joinResp.Error)
	}

	m.wsConn = conn
	m.isRemote = true

	// Initialize disconnect flag if needed
	if m.wsDisconnected == nil {
		m.wsDisconnected = new(bool)
	}
	*m.wsDisconnected = false

	// Set up signaling via WebSocket with disconnect callback
	disconnectFlag := m.wsDisconnected
	setupRemotePeerSignaling(conn, m.peerManager, func() {
		*disconnectFlag = true
	})

	return nil
}

func (m model) startSharing(index int) (tea.Model, tea.Cmd) {
	if m.starting || m.sharing {
		return m, nil
	}

	if index < 0 || index >= len(m.sources) {
		return m, nil
	}

	source := m.sources[index]
	m.selectedSource = index
	m.lastError = ""

	// Set up selection state for unified path
	if source.IsFullscreen {
		// Fullscreen selected - clear window selection
		m.fullscreenSelected = true
		m.selectedWindows = make(map[uint32]bool)
		m.isFullscreen = true
		m.activeWindowID = 0
	} else if source.Window != nil {
		// Single window selected - add to selection
		m.fullscreenSelected = false
		m.selectedWindows = make(map[uint32]bool)
		m.selectedWindows[source.Window.ID] = true
		m.isFullscreen = false
		m.activeWindowID = source.Window.ID
	}

	// Use unified multi-window path
	return m.startMultiWindowSharing()
}

// startMultiWindowSharing starts sharing selected windows or fullscreen display
func (m model) startMultiWindowSharing() (tea.Model, tea.Cmd) {
	// Block streaming if no room code (server connection failed)
	if m.roomCode == "" {
		m.lastError = "Cannot start: no room code (server connection failed)"
		return m, nil
	}

	if !m.fullscreenSelected && len(m.selectedWindows) == 0 {
		m.lastError = "No windows or fullscreen selected. Use SPACE to select."
		return m, nil
	}

	if m.starting || m.sharing {
		return m, nil
	}

	m.stopCapture(false)
	// Only do full cleanup if server isn't already running
	// If server is running, keep peerManager alive to reuse the connection
	if !m.serverStarted {
		m.stopMultiCapture()
	}
	m.lastError = ""

	// Initialize server for multi-window mode
	if err := m.initMultiServer(); err != nil {
		m.lastError = err.Error()
		return m, nil
	}

	m.starting = true

	// Collect selected windows info (empty if fullscreen selected)
	var selectedWindowInfos []WindowInfo
	if !m.fullscreenSelected {
		for _, source := range m.sources {
			if !source.IsFullscreen && source.Window != nil {
				if m.selectedWindows[source.Window.ID] {
					selectedWindowInfos = append(selectedWindowInfos, *source.Window)
				}
			}
		}
	}

	// Capture config values for async command
	fps := m.getSelectedFPS()
	focusBitrate := QualityPresets[m.selectedQuality].Bitrate
	bgBitrate := focusBitrate / 3 // Background windows get 1/3 bitrate
	if bgBitrate < 500 {
		bgBitrate = 500
	}
	adaptiveBR := m.adaptiveBitrate
	qualityMode := m.qualityMode
	codecType := m.getSelectedCodecType()
	multiPeerManager := m.peerManager
	fullscreen := m.fullscreenSelected

	return m, startMultiCaptureAsync(multiPeerManager, selectedWindowInfos, fullscreen, fps, focusBitrate, bgBitrate, adaptiveBR, qualityMode, codecType)
}

// updateMultiStreamSelection dynamically adds/removes windows/display without full restart
func (m model) updateMultiStreamSelection() (tea.Model, tea.Cmd) {
	// If not currently streaming, fall back to starting fresh
	if m.streamer == nil || !m.sharing {
		return m.startMultiWindowSharing()
	}

	// Get currently streaming windows (windowID=0 means display is streaming)
	currentWindows := m.streamer.GetStreamingWindowIDs()
	hasDisplay := currentWindows[0] // windowID 0 = display capture

	// Handle special case: nothing selected - just remove all streams, keep connection alive
	if !m.fullscreenSelected && len(m.selectedWindows) == 0 {
		// Remove display if active
		if hasDisplay {
			log.Printf("TUI: Removing display (no sources selected)")
			if err := m.streamer.RemoveDisplayDynamic(); err != nil {
				log.Printf("TUI: Failed to remove display: %v", err)
			}
		}
		// Remove all windows
		for windowID := range currentWindows {
			if windowID != 0 {
				log.Printf("TUI: Removing window %d (no sources selected)", windowID)
				if err := m.streamer.RemoveWindowDynamic(windowID); err != nil {
					log.Printf("TUI: Failed to remove window %d: %v", windowID, err)
				}
			}
		}
		return m, nil
	}

	// Handle fullscreen transitions
	if m.fullscreenSelected && !hasDisplay {
		// Switching TO fullscreen: remove all windows first, then add display
		for windowID := range currentWindows {
			if windowID != 0 { // Skip display (shouldn't be there anyway)
				log.Printf("TUI: Removing window %d for fullscreen switch", windowID)
				if err := m.streamer.RemoveWindowDynamic(windowID); err != nil {
					log.Printf("TUI: Failed to remove window %d: %v", windowID, err)
				}
			}
		}
		// Add display
		log.Printf("TUI: Adding display capture")
		if _, err := m.streamer.AddDisplayDynamic(); err != nil {
			log.Printf("TUI: Failed to add display: %v", err)
			m.lastError = fmt.Sprintf("Failed to start fullscreen: %v", err)
		}
		return m, nil
	}

	if !m.fullscreenSelected && hasDisplay {
		// Switching FROM fullscreen: remove display
		log.Printf("TUI: Removing display capture")
		if err := m.streamer.RemoveDisplayDynamic(); err != nil {
			log.Printf("TUI: Failed to remove display: %v", err)
		}
		// Continue to add any selected windows below
	}

	// Find windows to add (skip windowID 0 which is display)
	var windowsToAdd []WindowInfo
	for windowID := range m.selectedWindows {
		if windowID != 0 && !currentWindows[windowID] {
			// Find the WindowInfo for this ID from sources
			for _, source := range m.sources {
				if source.Window != nil && source.Window.ID == windowID {
					windowsToAdd = append(windowsToAdd, *source.Window)
					break
				}
			}
		}
	}

	// Find windows to remove (skip windowID 0 which is handled above)
	var windowsToRemove []uint32
	for windowID := range currentWindows {
		if windowID != 0 && !m.selectedWindows[windowID] {
			windowsToRemove = append(windowsToRemove, windowID)
		}
	}

	// Remove windows first (to free up space for new ones)
	for _, windowID := range windowsToRemove {
		log.Printf("TUI: Removing window dynamically: %d", windowID)
		if err := m.streamer.RemoveWindowDynamic(windowID); err != nil {
			log.Printf("TUI: Failed to remove window %d: %v", windowID, err)
		}
	}

	// Add new windows
	for _, window := range windowsToAdd {
		log.Printf("TUI: Adding window dynamically: %d (%s)", window.ID, window.WindowName)
		if _, err := m.streamer.AddWindowDynamic(window); err != nil {
			log.Printf("TUI: Failed to add window %d: %v", window.ID, err)
		}
	}

	return m, nil
}

// initMultiServer initializes the server for multi-window mode
func (m *model) initMultiServer() error {
	if m.serverStarted && m.peerManager != nil {
		return nil
	}

	// Room code must be set before initializing server
	if m.roomCode == "" {
		return fmt.Errorf("no room code set")
	}

	// Create multi peer manager
	iceConfig := ICEConfig{
		TURNServer: m.config.TURNServer,
		TURNUser:   m.config.TURNUser,
		TURNPass:   m.config.TURNPass,
		ForceRelay: m.config.ForceRelay,
	}
	codecType := m.getSelectedCodecType()

	var err error
	m.peerManager, err = NewPeerManager(iceConfig, codecType)
	if err != nil {
		return fmt.Errorf("failed to create multi peer manager: %v", err)
	}

	// Initialize pre-allocated track slots for instant window sharing
	if err := m.peerManager.InitializeTrackSlots(); err != nil {
		return fmt.Errorf("failed to initialize track slots: %v", err)
	}

	// Try remote signal server first
	if !m.config.LocalMode && m.config.SignalURL != "" {
		if err := m.initRemoteSignaling(); err == nil {
			m.serverStarted = true
			return nil
		}
	}

	// Local mode
	m.isRemote = false
	m.server = sig.NewServer()
	addr := fmt.Sprintf(":%d", m.config.Port)

	go func() {
		m.server.StartServer(addr)
	}()

	time.Sleep(100 * time.Millisecond)

	localIP := getLocalIP()
	m.shareURL = fmt.Sprintf("http://%s:%d/%s", localIP, m.config.Port, m.roomCode)

	// Set up signaling for multi-window
	setupPeerSignaling(m.server, m.peerManager, m.roomCode, m.password)

	m.serverStarted = true
	return nil
}

// stopMultiCapture stops multi-window capture
func (m *model) stopMultiCapture() {
	if m.streamer != nil {
		m.streamer.Stop()
		m.streamer = nil
	}
	if m.peerManager != nil {
		m.peerManager.Close()
		m.peerManager = nil
	}
}

// startMultiCaptureAsync starts multi-window or display capture asynchronously
func startMultiCaptureAsync(pm *PeerManager, windows []WindowInfo, fullscreen bool, fps, focusBitrate, bgBitrate int, adaptiveBR bool, qualityMode bool, codecType CodecType) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(100 * time.Millisecond)

		// Create multi streamer
		ms := NewStreamer(pm, fps, focusBitrate, bgBitrate, adaptiveBR, qualityMode)

		if fullscreen {
			// Add display capture
			_, err := ms.AddDisplay()
			if err != nil {
				ms.Stop()
				return captureErrorMsg{err: fmt.Sprintf("Failed to start fullscreen capture: %v", err)}
			}
		} else {
			// Add each window
			for _, win := range windows {
				_, err := ms.AddWindow(win)
				if err != nil {
					ms.Stop()
					return captureErrorMsg{err: fmt.Sprintf("Failed to add window %s: %v", win.DisplayName(), err)}
				}
			}
		}

		// Set up focus change callback - this will be called when OS focus changes
		// The callback needs access to the websocket or server to broadcast
		// For now, the focus info is tracked in the tracks and sent with streams-info

		// Start streaming
		if err := ms.Start(); err != nil {
			ms.Stop()
			return captureErrorMsg{err: fmt.Sprintf("Failed to start multi-streamer: %v", err)}
		}

		// Trigger renegotiation with any existing viewers
		// This is needed when restarting after stop ('s' key) to update viewers with new tracks
		pm.RenegotiateAllPeers()

		return captureStartedMsg{
			streamer:    ms,
			peerManager: pm,
		}
	}
}

// stopCapture stops the current capture but keeps server running.
// If preserveState is true, keeps isFullscreen and activeWindowID for restart scenarios.
func (m *model) stopCapture(preserveState bool) {
	// Stop unified streamer
	if m.streamer != nil {
		m.streamer.Stop()
		m.streamer = nil
	}

	m.sharing = false
	m.streamStats = nil
	m.syncOverlay() // Update overlay state (no longer sharing)

	if !preserveState {
		m.selectedSource = -1
		m.isFullscreen = false
		m.activeWindowID = 0
	}
}

// cleanup shuts down everything
func (m *model) cleanup() {
	// Save settings before cleanup
	currentSettings := settings.UserSettings{
		Quality:         m.selectedQuality,
		FPS:             m.selectedFPS,
		Codec:           m.selectedCodec,
		AdaptiveBitrate: m.adaptiveBitrate,
		QualityMode:     m.qualityMode,
	}
	if err := settings.Save(currentSettings); err != nil {
		log.Printf("Failed to save settings: %v", err)
	}

	m.stopCapture(false)

	// Close unified peer manager
	if m.peerManager != nil {
		m.peerManager.Close()
		m.peerManager = nil
	}

	if m.wsConn != nil {
		m.wsConn.Close()
		m.wsConn = nil
	}

	// Note: HTTP server doesn't have clean shutdown in current implementation
	m.serverStarted = false
}

func (m model) View() string {
	var b strings.Builder

	// Title
	b.WriteString(titleStyle.Render("GoPeep"))
	b.WriteString(dimStyle.Render(" - P2P Screen Sharing"))
	b.WriteString("\n\n")

	// Status bar (if server is running)
	if m.serverStarted {
		b.WriteString(m.renderSharingStatus())
		b.WriteString("\n")
	} else if m.roomCode != "" {
		// Show room code even before streaming starts
		if m.config.LocalMode {
			b.WriteString(dimStyle.Render("[LOCAL]"))
		} else {
			b.WriteString(selectedStyle.Render("[INTERNET]"))
		}
		b.WriteString("  ")
		b.WriteString(statusStyle.Render("Room: "))
		b.WriteString(normalStyle.Render(m.roomCode))
		b.WriteString("  ")
		if m.serverStarted {
			b.WriteString(dimStyle.Render("(ready, select source to start)"))
		} else {
			b.WriteString(dimStyle.Render("(connecting...)"))
		}
		b.WriteString("\n\n")
	}

	// Column layout (Sources, Settings, and Viewers when sharing)
	b.WriteString(m.renderColumns())

	// Stats panel (if enabled and sharing)
	if m.showStats && m.sharing {
		b.WriteString("\n")
		b.WriteString(m.renderStats())
	}

	// Error message
	if m.lastError != "" {
		b.WriteString("\n")
		b.WriteString(errorStyle.Render("Error: " + m.lastError))
		b.WriteString("\n")
	}

	// Help
	b.WriteString("\n")
	b.WriteString(m.renderHelp())

	return b.String()
}

func (m model) renderSharingStatus() string {
	var b strings.Builder

	// Mode indicator
	if m.reconnecting {
		b.WriteString(errorStyle.Render(fmt.Sprintf("[RECONNECTING %d/%d]", m.reconnectAttempt, m.maxReconnects)))
	} else if m.isRemote {
		b.WriteString(selectedStyle.Render("[INTERNET]"))
	} else {
		b.WriteString(dimStyle.Render("[LOCAL]"))
	}
	b.WriteString("  ")

	// Room code and URL (always show once server started)
	b.WriteString(statusStyle.Render("Room: "))
	b.WriteString(normalStyle.Render(m.roomCode))
	b.WriteString("  ")

	b.WriteString(statusStyle.Render("URL: "))
	b.WriteString(urlStyle.Render(m.shareURL))
	// Show copy message if present
	if m.copyMessage != "" {
		b.WriteString("  ")
		b.WriteString(selectedStyle.Render(m.copyMessage))
	}
	// Show password if enabled
	if m.passwordEnabled && m.password != "" {
		b.WriteString("  ")
		b.WriteString(statusStyle.Render("Pass: "))
		b.WriteString(selectedStyle.Render(m.password))
	}
	b.WriteString("\n")

	// Show status based on state
	if m.starting && len(m.selectedWindows) > 0 {
		// Starting multi-window capture
		b.WriteString(statusStyle.Render("Starting: "))
		b.WriteString(normalStyle.Render(fmt.Sprintf("%d windows", len(m.selectedWindows))))
		b.WriteString("  ")
		b.WriteString(dimStyle.Render("please wait..."))
	} else if m.starting && m.selectedSource >= 0 && m.selectedSource < len(m.sources) {
		// Starting single-window capture (async)
		source := m.sources[m.selectedSource]
		b.WriteString(statusStyle.Render("Starting: "))
		b.WriteString(normalStyle.Render(truncate(source.DisplayName, 30)))
		b.WriteString("  ")
		b.WriteString(dimStyle.Render("please wait..."))
	} else if m.sharing && m.streamer != nil {
		// Multi-window sharing
		streams := m.streamer.GetStreamsInfo()
		b.WriteString(statusStyle.Render("Sharing: "))
		b.WriteString(selectedStyle.Render(fmt.Sprintf("%d windows", len(streams))))
		if m.adaptiveBitrate {
			b.WriteString(dimStyle.Render(" [adaptive]"))
		}
		b.WriteString("  ")

		// Quality
		b.WriteString(statusStyle.Render("Quality: "))
		b.WriteString(normalStyle.Render(QualityPresets[m.selectedQuality].Name))
		b.WriteString("  ")

		// Viewer count
		b.WriteString(statusStyle.Render("Viewers: "))
		if m.viewerCount == 0 {
			b.WriteString(dimStyle.Render("waiting..."))
		} else {
			b.WriteString(viewerStyle.Render(fmt.Sprintf("%d", m.viewerCount)))
		}
	} else if m.sharing && m.selectedSource >= 0 && m.selectedSource < len(m.sources) {
		// Currently sharing single window
		source := m.sources[m.selectedSource]
		b.WriteString(statusStyle.Render("Sharing: "))
		b.WriteString(selectedStyle.Render(truncate(source.DisplayName, 30)))
		b.WriteString("  ")

		// Quality
		b.WriteString(statusStyle.Render("Quality: "))
		b.WriteString(normalStyle.Render(QualityPresets[m.selectedQuality].Name))
		b.WriteString("  ")

		// Codec with hardware indicator
		b.WriteString(statusStyle.Render("Codec: "))
		if m.selectedCodec >= 0 && m.selectedCodec < len(AvailableCodecs) {
			codec := AvailableCodecs[m.selectedCodec]
			if codec.IsHardware {
				b.WriteString(selectedStyle.Render(codec.Name + " [HW]"))
			} else {
				b.WriteString(normalStyle.Render(codec.Name))
			}
		}
		b.WriteString("  ")

		// Viewer count
		b.WriteString(statusStyle.Render("Viewers: "))
		if m.viewerCount == 0 {
			b.WriteString(dimStyle.Render("waiting..."))
		} else {
			b.WriteString(viewerStyle.Render(fmt.Sprintf("%d", m.viewerCount)))
		}
	} else {
		b.WriteString(dimStyle.Render("Select a source to start sharing"))
	}
	b.WriteString("\n")

	return b.String()
}

func (m model) renderColumns() string {
	// Render sources column
	sourcesContent := m.renderSourcesList()

	// Render quality, FPS and codec as a combined right panel
	qualityContent := m.renderQualityList()
	fpsContent := m.renderFPSList()
	codecContent := m.renderCodecList()

	// Create boxes with appropriate styles based on active column
	var sourcesBox string
	rightPanelContent := qualityContent + "\n\n" + fpsContent + "\n\n" + codecContent

	sourcesTitle := " Sources "
	rightTitle := " Settings "
	viewersTitle := " Viewers "

	isRightPanelActive := m.activeColumn == columnQuality || m.activeColumn == columnFPS || m.activeColumn == columnCodec

	if m.activeColumn == columnSources {
		sourcesBox = activeBoxStyle.Width(44).Render(
			boxTitleStyle.Render(sourcesTitle) + "\n" + sourcesContent,
		)
	} else {
		sourcesBox = inactiveBoxStyle.Width(44).Render(
			boxTitleDimStyle.Render(sourcesTitle) + "\n" + sourcesContent,
		)
	}

	var rightBox string
	if isRightPanelActive {
		rightBox = activeBoxStyle.Width(28).Render(
			boxTitleStyle.Render(rightTitle) + "\n" + rightPanelContent,
		)
	} else {
		rightBox = inactiveBoxStyle.Width(28).Render(
			boxTitleDimStyle.Render(rightTitle) + "\n" + rightPanelContent,
		)
	}

	// Add viewers column when sharing
	if m.sharing {
		viewersContent := m.renderViewerList()
		viewerBoxStyle := inactiveBoxStyle.Copy().
			BorderForeground(lipgloss.Color("11"))
		viewersBox := viewerBoxStyle.Width(22).Render(
			viewerStyle.Render(viewersTitle) + "\n" + viewersContent,
		)
		return lipgloss.JoinHorizontal(lipgloss.Top, sourcesBox, " ", rightBox, " ", viewersBox)
	}

	// Join columns horizontally
	return lipgloss.JoinHorizontal(lipgloss.Top, sourcesBox, " ", rightBox)
}

func (m model) renderSourcesList() string {
	var b strings.Builder

	// Show header based on mode
	if m.autoShareEnabled {
		// Auto-share mode: show badge and auto-managed window count
		if len(m.selectedWindows) > 0 {
			modeText := fmt.Sprintf("AUTO-SHARE: %d/%d windows", len(m.selectedWindows), MaxCaptureInstances)
			b.WriteString(selectedStyle.Render(modeText))
		} else {
			b.WriteString(selectedStyle.Render("AUTO-SHARE MODE"))
		}
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("Windows auto-managed (Shift+A to exit)"))
		b.WriteString("\n")
	} else if len(m.selectedWindows) > 0 {
		// Normal mode with selections
		modeText := fmt.Sprintf("Selected: %d/%d windows", len(m.selectedWindows), MaxCaptureInstances)
		b.WriteString(selectedStyle.Render(modeText))
		b.WriteString("\n")
	} else {
		b.WriteString(dimStyle.Render("Use SPACE to select windows (up to 4)"))
		b.WriteString("\n")
	}

	if len(m.sources) == 0 {
		b.WriteString(dimStyle.Render("No sources available"))
		return b.String()
	}

	windowNum := 0 // Counter for window numbers (1-9)
	for i, source := range m.sources {
		cursor := "  "
		if m.activeColumn == columnSources && i == m.sourceCursor {
			cursor = "> "
		}

		// Format label with appropriate shortcut key
		var label string
		var isSelected bool

		if source.IsFullscreen {
			// Fullscreen option with checkbox
			checkbox := "[ ]"
			if m.fullscreenSelected {
				checkbox = "[x]"
				isSelected = true
			}
			label = fmt.Sprintf("%s [F] %s", checkbox, source.DisplayName)
		} else {
			// Window with checkbox
			windowNum++
			checkbox := "[ ]"
			if source.Window != nil && m.selectedWindows[source.Window.ID] {
				checkbox = "[x]"
				isSelected = true
			}
			// Check if this window has OS focus
			hasFocus := source.Window != nil && source.Window.ID == m.osFocusedWindowID
			focusIndicator := ""
			if hasFocus {
				focusIndicator = " *" // Asterisk indicates OS focus
			}
			if windowNum <= 9 {
				label = fmt.Sprintf("%s [%d] %s%s", checkbox, windowNum, truncate(source.DisplayName, 26), focusIndicator)
			} else {
				label = fmt.Sprintf("%s [ ] %s%s", checkbox, truncate(source.DisplayName, 26), focusIndicator)
			}
		}

		// Style based on selection state
		var line string
		isSharing := m.sharing && i == m.selectedSource
		isStarting := m.starting && i == m.selectedSource

		if isSelected {
			line = selectedStyle.Render(cursor + label)
		} else if isSharing {
			line = selectedStyle.Render(cursor + label)
		} else if isStarting {
			line = normalStyle.Render(cursor + label)
		} else if m.activeColumn == columnSources && i == m.sourceCursor {
			line = normalStyle.Render(cursor + label)
		} else {
			line = dimStyle.Render(cursor + label)
		}

		b.WriteString(line)
		if isSharing {
			b.WriteString(dimStyle.Render(" *"))
		} else if isStarting {
			b.WriteString(dimStyle.Render(" ..."))
		}
		b.WriteString("\n")
	}

	return strings.TrimSuffix(b.String(), "\n")
}

func (m model) renderQualityList() string {
	var b strings.Builder

	b.WriteString(dimStyle.Render("--- Quality ---"))
	b.WriteString("\n")

	for i, preset := range QualityPresets {
		cursor := "  "
		if m.activeColumn == columnQuality && i == m.qualityCursor {
			cursor = "> "
		}

		// Format: name + bitrate
		label := fmt.Sprintf("%s (%s)", preset.Name, preset.Description)

		// Style based on selection state
		var line string
		isSelected := i == m.selectedQuality

		if isSelected {
			line = selectedStyle.Render(cursor + label)
		} else if m.activeColumn == columnQuality && i == m.qualityCursor {
			line = normalStyle.Render(cursor + label)
		} else {
			line = dimStyle.Render(cursor + label)
		}

		b.WriteString(line)
		b.WriteString("\n")
	}

	return strings.TrimSuffix(b.String(), "\n")
}

func (m model) renderFPSList() string {
	var b strings.Builder

	b.WriteString(dimStyle.Render("--- FPS ---"))
	b.WriteString("\n")

	for i, preset := range FPSPresets {
		cursor := "  "
		if m.activeColumn == columnFPS && i == m.fpsCursor {
			cursor = "> "
		}

		// Format: value + description
		label := fmt.Sprintf("%s (%s)", preset.Name, preset.Description)

		// Style based on selection state
		var line string
		isSelected := i == m.selectedFPS

		if isSelected {
			line = selectedStyle.Render(cursor + label)
		} else if m.activeColumn == columnFPS && i == m.fpsCursor {
			line = normalStyle.Render(cursor + label)
		} else {
			line = dimStyle.Render(cursor + label)
		}

		b.WriteString(line)
		b.WriteString("\n")
	}

	return strings.TrimSuffix(b.String(), "\n")
}

func (m model) renderCodecList() string {
	var b strings.Builder

	b.WriteString(dimStyle.Render("--- Codec ---"))
	b.WriteString("\n")

	for i, codec := range AvailableCodecs {
		cursor := "  "
		if m.activeColumn == columnCodec && i == m.codecCursor {
			cursor = "> "
		}

		// Format: name + description + hardware indicator
		hwIndicator := ""
		if codec.IsHardware {
			hwIndicator = " [HW]"
		}
		label := fmt.Sprintf("%s (%s)%s", codec.Name, codec.Description, hwIndicator)

		// Style based on selection state
		var line string
		isSelected := i == m.selectedCodec

		if isSelected {
			line = selectedStyle.Render(cursor + label)
		} else if m.activeColumn == columnCodec && i == m.codecCursor {
			line = normalStyle.Render(cursor + label)
		} else {
			line = dimStyle.Render(cursor + label)
		}

		b.WriteString(line)
		b.WriteString("\n")
	}

	return strings.TrimSuffix(b.String(), "\n")
}

func (m model) renderViewerList() string {
	var content strings.Builder

	// Get viewer info from peer manager
	var viewers []ViewerInfo
	if m.peerManager != nil {
		viewers = m.peerManager.GetViewerInfo()
	}

	// Count display
	countStr := fmt.Sprintf("(%d)", len(viewers))
	content.WriteString(dimStyle.Render(countStr))
	content.WriteString("\n")

	if len(viewers) == 0 {
		content.WriteString(dimStyle.Render("Waiting..."))
	} else {
		// Render each viewer on its own line
		for _, v := range viewers {
			var line string
			switch v.State {
			case "connected":
				connTime := time.Since(v.ConnectedAt).Truncate(time.Second)
				connType := ""
				if v.ConnectionType == "relay" {
					connType = " TURN"
				} else if v.ConnectionType == "direct" {
					connType = " P2P"
				}
				line = fmt.Sprintf("%s%s %s", v.PeerID, connType, formatDuration(connTime))
				content.WriteString(viewerStyle.Render(line))
			case "connecting":
				line = fmt.Sprintf("%s ...", v.PeerID)
				content.WriteString(dimStyle.Render(line))
			default:
				line = fmt.Sprintf("%s [%s]", v.PeerID, v.State)
				content.WriteString(dimStyle.Render(line))
			}
			content.WriteString("\n")
		}
	}

	return strings.TrimSuffix(content.String(), "\n")
}

func (m model) renderStats() string {
	var b strings.Builder

	// Stats box style
	statsBoxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("8")).
		Padding(0, 1).
		Width(74)

	var content strings.Builder
	content.WriteString(boxTitleDimStyle.Render(" Streams "))
	content.WriteString("\n")

	// Uptime
	uptime := time.Since(m.startTime).Truncate(time.Second)
	content.WriteString(dimStyle.Render("Uptime: "))
	content.WriteString(normalStyle.Render(formatDuration(uptime)))
	content.WriteString("\n")

	// Per-stream stats in compact format
	if len(m.streamStats) == 0 {
		content.WriteString(dimStyle.Render("No active streams"))
	} else {
		// Calculate totals
		var totalFrames uint64
		var totalBytes uint64
		for _, stat := range m.streamStats {
			totalFrames += stat.Frames
			totalBytes += stat.Bytes
		}

		// Show each stream
		for i, stat := range m.streamStats {
			// Stream number and app name (truncated)
			appName := stat.AppName
			if len(appName) > 12 {
				appName = appName[:12]
			}
			if appName == "" {
				appName = stat.TrackID
			}

			// Format: "1: AppName    1920x1080@30 | 2.1Mbps | 45.2MB *"
			focusMarker := " "
			if stat.IsFocused {
				focusMarker = "*"
			}

			resStr := fmt.Sprintf("%dx%d@%.0f", stat.Width, stat.Height, stat.FPS)
			bitrateStr := fmt.Sprintf("%.1fMbps", stat.Bitrate/1000)
			dataStr := formatBytes(int64(stat.Bytes))

			line := fmt.Sprintf("%d: %-12s %s | %s | %s %s",
				i+1, appName, resStr, bitrateStr, dataStr, focusMarker)

			if stat.IsFocused {
				content.WriteString(selectedStyle.Render(line))
			} else {
				content.WriteString(normalStyle.Render(line))
			}
			content.WriteString("\n")
		}

		// Totals line
		content.WriteString(dimStyle.Render(fmt.Sprintf("Total: %s frames, %s",
			formatNumber(int64(totalFrames)), formatBytes(int64(totalBytes)))))
	}

	b.WriteString(statsBoxStyle.Render(content.String()))
	return b.String()
}

func formatDuration(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

func formatNumber(n int64) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

func formatBytes(b int64) string {
	if b >= 1_000_000_000 {
		return fmt.Sprintf("%.2f GB", float64(b)/1_000_000_000)
	}
	if b >= 1_000_000 {
		return fmt.Sprintf("%.1f MB", float64(b)/1_000_000)
	}
	if b >= 1_000 {
		return fmt.Sprintf("%.1f KB", float64(b)/1_000)
	}
	return fmt.Sprintf("%d B", b)
}

func (m model) renderHelp() string {
	var b strings.Builder
	sep := keySepStyle.Render(" │ ")

	// Line 1: Regular keybinds (actions)
	var actions []string

	actions = append(actions, keyStyle.Render("tab")+helpStyle.Render(" columns"))
	actions = append(actions, keyStyle.Render("↑↓")+helpStyle.Render(" select"))
	actions = append(actions, keyStyle.Render("space")+helpStyle.Render(" toggle"))
	actions = append(actions, keyStyle.Render("enter")+helpStyle.Render(" start"))
	actions = append(actions, keyStyle.Render("f")+helpStyle.Render(" fullscreen"))

	if m.serverStarted {
		actions = append(actions, keyStyle.Render("c")+helpStyle.Render(" copy"))
	}

	if m.sharing {
		actions = append(actions, keyStyle.Render("s")+helpStyle.Render(" stop"))
	}

	actions = append(actions, keyStyle.Render("r")+helpStyle.Render(" refresh"))
	actions = append(actions, keyStyle.Render("^c")+helpStyle.Render(" quit"))

	b.WriteString(strings.Join(actions, sep))

	// Line 2: Toggles with state indicators
	var toggles []string

	// Adaptive bitrate toggle (only before sharing)
	if !m.sharing && !m.starting {
		toggles = append(toggles, m.renderToggle("a", "adaptive", m.adaptiveBitrate))
	}

	// Quality mode toggle - shows current mode (quality ON = quality mode, OFF = performance mode)
	if m.qualityMode {
		toggles = append(toggles, m.renderToggle("q", "quality", true))
	} else {
		toggles = append(toggles, m.renderToggle("q", "performance", false))
	}

	// Password toggle
	toggles = append(toggles, m.renderToggle("p", "password", m.passwordEnabled))

	// Stats toggle (only while sharing)
	if m.sharing {
		toggles = append(toggles, m.renderToggle("i", "stats", m.showStats))
	}

	// Auto-share mode toggle
	toggles = append(toggles, m.renderToggle("A", "auto", m.autoShareEnabled))

	if len(toggles) > 0 {
		b.WriteString("\n\n")
		b.WriteString(strings.Join(toggles, "   "))
	}

	return b.String()
}

// renderToggle renders a toggle keybind with active/inactive indicator
func (m model) renderToggle(key, label string, active bool) string {
	if active {
		return toggleActiveStyle.Render("◉ "+key) + " " + toggleActiveStyle.Render(label)
	}
	return toggleInactiveStyle.Render("○ "+key) + " " + toggleInactiveStyle.Render(label)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// RunTUI starts the TUI application
func RunTUI(config Config) error {
	// Check screen recording permission first
	if !HasScreenRecordingPermission() {
		fmt.Println("Screen Recording permission required.")
		fmt.Println("Please grant permission in:")
		fmt.Println("  System Preferences > Security & Privacy > Privacy > Screen Recording")
		fmt.Println()
		fmt.Println("After granting permission, restart gopeep.")
		return nil
	}

	// Write logs to file instead of corrupting TUI display
	logFile, err := os.Create("gopeep-debug.log")
	if err != nil {
		// Fall back to discarding if we can't create log file
		log.SetOutput(io.Discard)
	} else {
		log.SetOutput(logFile)
		log.Printf("=== GoPeep started at %s ===", time.Now().Format(time.RFC3339))
		defer logFile.Close()
	}

	// Restore logging on exit
	defer log.SetOutput(os.Stderr)

	// Create overlay controller and overlay
	overlayCtrl := NewOverlayController()
	overlayInstance := overlay.New(overlayCtrl)

	// Create the initial model with overlay
	m := initialModel(config)
	m.overlay = overlayInstance
	m.overlayController = overlayCtrl

	p := tea.NewProgram(
		m,
		tea.WithAltScreen(),
	)

	// Start overlay and listen for events
	if err := overlayInstance.Start(); err != nil {
		log.Printf("Failed to start overlay: %v", err)
	} else {
		// Goroutine to forward overlay events to the TUI
		go func() {
			for evt := range overlayInstance.Events() {
				switch evt.Type {
				case overlay.EventToggleSelection:
					p.Send(overlayToggleMsg{windowID: evt.WindowID})
				case overlay.EventToggleFullscreen:
					p.Send(overlayFullscreenToggleMsg{})
				case overlay.EventClearAll:
					p.Send(overlayClearAllMsg{})
				}
			}
		}()
	}

	_, runErr := p.Run()

	// Cleanup overlay
	overlayInstance.Stop()

	return runErr
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
