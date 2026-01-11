package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/gorilla/websocket"
	"github.com/tomaslejdung/gopeep/internal/app"
	"github.com/tomaslejdung/gopeep/internal/capture"
	"github.com/tomaslejdung/gopeep/internal/config"
	"github.com/tomaslejdung/gopeep/internal/encoding"
	"github.com/tomaslejdung/gopeep/internal/streaming"
	"github.com/tomaslejdung/gopeep/internal/webrtc"
	sig "github.com/tomaslejdung/gopeep/internal/signal"
	"github.com/tomaslejdung/gopeep/internal/ui/overlay"
	"github.com/tomaslejdung/gopeep/internal/ui/settings"
)

// Message types, SourceItem, and column constants are in types.go
// Styles are in styles.go
// SelectionManager is in selection.go

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
			Room   string `json:"room"`
			Secret string `json:"secret"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return roomCodeReceivedMsg{err: fmt.Errorf("failed to decode response: %w", err)}
		}

		return roomCodeReceivedMsg{roomCode: result.Room, roomSecret: result.Secret}
	}
}

// Model is the TUI model.
// All application state is in AppCore - this struct only holds UI-specific state.
type Model struct {
	// AppCore holds shared state that both TUI and Overlay need
	appCore *app.AppCore

	// Sources (fullscreen + windows) - for rendering
	sources        []SourceItem
	sourceCursor   int
	selectedSource int // -1 if not sharing (single-window mode)

	// Quality selection
	qualityCursor   int
	selectedQuality int

	// FPS selection
	fpsCursor   int
	selectedFPS int

	// Codec selection
	codecCursor   int
	selectedCodec int

	// Navigation: 0 = sources, 1 = quality, 2 = fps, 3 = codec
	activeColumn int

	// Display state
	lastError   string
	copyMessage string    // temporary "Copied!" message
	copyMsgTime time.Time // when copy message was shown

	// Stats display
	showStats   bool
	streamStats []webrtc.StreamPipelineStats // Per-stream stats from unified streamer

	// Terminal dimensions
	width  int
	height int

	// Overlay components
	overlay           *overlay.Overlay
	overlayController *app.OverlayController

	// Selection manager (centralizes all selection logic)
	selection *SelectionManager
}

// SelectionManager is in tui_selection.go

// findSourceIndex returns the index of the source matching the current capture state.
// Returns -1 if not found (window closed or not in list).
func (m *Model) findSourceIndex() int {
	if !m.appCore.IsSharing() && !m.appCore.IsStarting() {
		return -1
	}

	if m.appCore.IsFullscreen() {
		// Fullscreen is always index 0
		if len(m.sources) > 0 && m.sources[0].IsFullscreen {
			return 0
		}
		return -1
	}

	// Find window by ID
	for i, source := range m.sources {
		if !source.IsFullscreen && source.Window != nil && source.Window.ID == m.appCore.GetActiveWindowID() {
			return i
		}
	}
	return -1
}

func initialModel(cfg config.Config, appCore *app.AppCore) Model {
	// Initialize available codecs
	config.InitAvailableCodecs()

	// Load saved settings
	savedSettings, err := settings.Load()
	if err != nil {
		log.Printf("Failed to load settings: %v", err)
		savedSettings = settings.DefaultSettings()
	}

	// Validate indices after InitAvailableCodecs()
	if savedSettings.Quality < 0 || savedSettings.Quality >= len(config.QualityPresets) {
		savedSettings.Quality = config.DefaultQualityIndex()
	}
	if savedSettings.FPS < 0 || savedSettings.FPS >= len(config.FPSPresets) {
		savedSettings.FPS = config.DefaultFPSIndex()
	}
	if savedSettings.Codec < 0 || savedSettings.Codec >= len(config.AvailableCodecs) {
		savedSettings.Codec = config.DefaultCodecIndex()
	}

	// CLI flags override saved settings (30 is the default FPS flag value)
	fpsIndex := savedSettings.FPS
	if cfg.FPS != 30 {
		fpsIndex = config.FPSIndexForValue(cfg.FPS)
	}

	// Initialize AppCore settings from saved settings
	appCore.SetAdaptiveBitrate(savedSettings.AdaptiveBitrate)
	appCore.SetQualityMode(savedSettings.QualityMode)
	appCore.SetMaxReconnects(10)

	return Model{
		appCore:         appCore,
		sourceCursor:    0,
		selectedSource:  -1,
		qualityCursor:   savedSettings.Quality,
		selectedQuality: savedSettings.Quality,
		fpsCursor:       fpsIndex,
		selectedFPS:     fpsIndex,
		codecCursor:     savedSettings.Codec,
		selectedCodec:   savedSettings.Codec,
		activeColumn:    columnSources,
		selection:       &SelectionManager{},
	}
}

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		refreshWindows,
		tea.SetWindowTitle("GoPeep - Screen Sharing"),
	}

	// Request room code from signal server
	signalURL := normalizeSignalURL(m.appCore.GetConfig().SignalURL)
	cmds = append(cmds, requestRoomCodeFromServer(signalURL))

	return tea.Batch(cmds...)
}

func refreshWindows() tea.Msg {
	windows, _ := capture.ListWindows()
	return windowsUpdatedMsg{windows: windows}
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func fastTickCmd() tea.Cmd {
	// Slow backup tick (500ms) - most focus detection happens via NSWorkspace notifications
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg {
		return fastTickMsg(t)
	})
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		if (m.appCore.IsSharing() || m.appCore.IsStarting()) && len(msg.windows) == 0 && len(m.sources) > 1 {
			// Keep existing sources and selection - don't change anything
			return m, nil
		}

		m.sources = newSources

		// Reconcile selection: find the source matching our active capture by window ID
		// Only do this if we're actively sharing/starting AND the current selectedSource
		// doesn't already point to the correct window
		if m.appCore.IsSharing() || m.appCore.IsStarting() {
			// Check if current selectedSource is still valid
			currentValid := false
			if m.selectedSource >= 0 && m.selectedSource < len(m.sources) {
				source := m.sources[m.selectedSource]
				if m.appCore.IsFullscreen() && source.IsFullscreen {
					currentValid = true
				} else if !m.appCore.IsFullscreen() && !source.IsFullscreen && source.Window != nil && source.Window.ID == m.appCore.GetActiveWindowID() {
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
		m.appCore.SetViewerCount(int(msg))
		return m, nil

	case roomCodeReceivedMsg:
		if msg.err != nil {
			// Server must provide room code - show error
			log.Printf("Failed to get room code from server: %v", msg.err)
			m.lastError = fmt.Sprintf("Server error: %v", msg.err)
			return m, nil
		}
		m.appCore.SetRoomCode(msg.roomCode, msg.roomSecret, "")
		log.Printf("Received room code from server: %s", m.appCore.GetRoomCode())

		// Initialize server synchronously (not in a Cmd - Bubbletea model changes don't persist in goroutines)
		if err := m.initMultiServer(); err != nil {
			log.Printf("Failed to initialize server: %v", err)
			m.lastError = err.Error()
		}
		return m, nil

	case captureStartedMsg:
		// Capture started successfully (unified for single/multi)
		m.appCore.SetStarting(false)
		m.appCore.SetSharing(true)
		m.appCore.SetStreamer(msg.Streamer)
		m.appCore.SetPeerManager(msg.PeerManager)
		m.appCore.SetStartTime(time.Now())
		m.showStats = true // Show stats by default when sharing starts
		m.syncOverlay()    // Update overlay state (now sharing)
		// Notify viewers that sharer has started (so they can rejoin)
		if m.appCore.GetSharer() != nil && m.appCore.GetRoomCode() != "" {
			log.Printf("Broadcasting sharer-started to room %s", m.appCore.GetRoomCode())
			m.appCore.GetSharer().SendToAllViewers(sig.SignalMessage{Type: "sharer-started"})
		}
		// If in auto-share mode, start fast tick for rapid focus detection
		if m.appCore.IsAutoShareEnabled() {
			return m, tea.Batch(tickCmd(), fastTickCmd())
		}
		return m, tickCmd()

	case captureErrorMsg:
		// Capture failed - reset state fully
		m.appCore.SetStarting(false)
		m.appCore.SetSharing(false)
		m.selectedSource = -1
		m.appCore.SetIsFullscreen(false)
		m.appCore.SetActiveWindowID(0)
		m.lastError = msg.err
		return m, refreshWindows

	case osFocusChangedMsg:
		// OS focus changed - update the tracked window ID
		m.appCore.SetOSFocusedWindowID(msg.windowID)
		return m, nil

	case overlayToggleMsg:
		// Overlay button was clicked - toggle window selection
		return m.handleOverlayToggle(msg.windowID)

	case overlayFullscreenToggleMsg:
		// Fullscreen button was clicked - toggle fullscreen mode
		if m.appCore.IsAutoShareEnabled() {
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
			topmostWindow := capture.GetTopmostWindow(allWindowIDs)
			if topmostWindow != m.appCore.GetOSFocusedWindowID() {
				m.appCore.SetOSFocusedWindowID(topmostWindow)
			}
		}

		// Update overlay state
		m.syncOverlay()

		// Update viewer count and stats if sharing
		if m.appCore.IsSharing() && m.appCore.GetPeerManager() != nil {
			m.appCore.SetViewerCount(m.appCore.GetPeerManager().GetConnectionCount())
		}
		if m.appCore.IsSharing() && m.appCore.GetStreamer() != nil {
			m.streamStats = m.appCore.GetStreamer().GetStats()
		}

		// Clear copy message after 2 seconds
		if m.copyMessage != "" && time.Since(m.copyMsgTime) > 2*time.Second {
			m.copyMessage = ""
		}

		// Check if our window was closed (if streaming a window)
		if m.appCore.IsSharing() && !m.appCore.IsFullscreen() && m.appCore.GetActiveWindowID() != 0 {
			// If window is no longer in the sources list, stop capture
			if m.selectedSource == -1 {
				m.stopCapture(false)
				m.lastError = "Window was closed"
			}
		}

		// Check for WebSocket disconnection and trigger reconnection
		if m.appCore.IsServerStarted() && m.appCore.GetWSDisconnectedPtr() != nil && *m.appCore.GetWSDisconnectedPtr() && !m.appCore.IsReconnecting() {
			*m.appCore.GetWSDisconnectedPtr() = false
			m.appCore.SetReconnecting(true)
			m.appCore.SetReconnectAttempt(1)
			m.appCore.SetReconnectDelay(time.Second)
			cmds = append(cmds, m.attemptReconnect(1, time.Second))
		}

		return m, tea.Batch(cmds...)

	case fastTickMsg:
		// Auto-share mode: automatically add/remove windows based on OS focus
		// Uses same multi-stream infrastructure as normal mode
		if m.appCore.IsAutoShareEnabled() && m.appCore.IsSharing() && m.appCore.GetStreamer() != nil {
			// Check if focus changed via OS notification (instant detection)
			if capture.CheckFocusChanged() {
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
			topmost := capture.GetTopmostWindow(windowIDs)

			if topmost != 0 {
				// Update focus time for LRU tracking
				m.appCore.InitAutoShareFocusTimes()
				m.appCore.TrackFocusTime(topmost)

				// Check if this window is already streaming
				if !m.appCore.GetStreamer().IsWindowStreaming(topmost) {
					// Find window info from m.sources
					var topmostWindow *capture.WindowInfo
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
						if m.appCore.GetStreamer().GetActiveStreamCount() >= capture.MaxCaptureInstances {
							// Remove LRU window to make room
							lruWindowID := m.getLRUWindow(topmost)
							if lruWindowID != 0 {
								log.Printf("Auto-share: Pool full, removing LRU window %d", lruWindowID)
								if err := m.appCore.GetStreamer().RemoveWindowDynamic(lruWindowID); err != nil {
									log.Printf("Auto-share: Failed to remove LRU window: %v", err)
								} else {
									delete(m.appCore.GetSelectedWindows(), lruWindowID)
									delete(m.appCore.GetAutoShareFocusTimes(), lruWindowID)
								}
							}
						}

						// Add new window
						log.Printf("Auto-share: Adding window %d (%s)", topmost, windowName)
						if _, err := m.appCore.GetStreamer().AddWindowDynamic(*topmostWindow); err != nil {
							log.Printf("Auto-share: Failed to add window: %v", err)
						} else {
							m.appCore.GetSelectedWindows()[topmost] = true
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
		if m.appCore.IsAutoShareEnabled() {
			return m, fastTickCmd()
		}

		// If no longer in auto-share mode, don't continue fast tick
		return m, nil

	case reconnectMsg:
		// WebSocket disconnected, attempt reconnection
		m.appCore.SetReconnecting(true)
		m.appCore.SetReconnectAttempt(msg.attempt)
		m.appCore.SetReconnectDelay(msg.delay)
		return m, m.attemptReconnect(msg.attempt, msg.delay)

	case reconnectedMsg:
		// Reconnection successful - store the new connection and set up signaling
		m.appCore.SetReconnecting(false)
		m.appCore.SetReconnectAttempt(0)
		m.lastError = ""
		m.appCore.SetWSConn(msg.conn)
		// Reset disconnect flag
		if m.appCore.GetWSDisconnectedPtr() != nil {
			*m.appCore.GetWSDisconnectedPtr() = false
		}
		// Set up signaling via the new WebSocket with disconnect callback
		disconnectFlag := m.appCore.GetWSDisconnectedPtr()
		setupRemoteSignaling(m.appCore.GetWSConn(), m.appCore.GetPeerManager(), func() {
			if disconnectFlag != nil {
				*disconnectFlag = true
			}
		})
		return m, nil

	case reconnectFailedMsg:
		// Reconnection failed
		m.appCore.SetReconnecting(false)
		m.lastError = msg.err
		m.appCore.SetServerStarted(false)
		return m, nil
	}

	return m, nil
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
				m.qualityCursor = len(config.QualityPresets) - 1
			}
		} else if m.activeColumn == columnCodec {
			if m.codecCursor > 0 {
				m.codecCursor--
			} else {
				// Move from codec to FPS section
				m.activeColumn = columnFPS
				m.fpsCursor = len(config.FPSPresets) - 1
			}
		}
		return m, nil

	case "down", "j":
		if m.activeColumn == columnSources {
			if m.sourceCursor < len(m.sources)-1 {
				m.sourceCursor++
			}
		} else if m.activeColumn == columnQuality {
			if m.qualityCursor < len(config.QualityPresets)-1 {
				m.qualityCursor++
			} else {
				// At bottom of quality, move to FPS section
				m.activeColumn = columnFPS
				m.fpsCursor = 0
			}
		} else if m.activeColumn == columnFPS {
			if m.fpsCursor < len(config.FPSPresets)-1 {
				m.fpsCursor++
			} else {
				// At bottom of FPS, move to codec section
				m.activeColumn = columnCodec
				m.codecCursor = 0
			}
		} else if m.activeColumn == columnCodec {
			if m.codecCursor < len(config.AvailableCodecs)-1 {
				m.codecCursor++
			}
		}
		return m, nil

	case "enter":
		// In auto-share mode, ignore source selection via enter
		if m.activeColumn == columnSources && m.appCore.IsAutoShareEnabled() {
			return m, nil
		}
		if m.activeColumn == columnSources {
			// Start sharing based on selection (fullscreen or windows)
			if m.appCore.IsFullscreenSelected() {
				return m.startMultiWindowSharing() // Will handle fullscreen via streamer
			}
			if len(m.appCore.GetSelectedWindows()) > 0 {
				return m.startMultiWindowSharing()
			}
			// If nothing selected, select current item and start
			if m.sourceCursor < len(m.sources) {
				source := m.sources[m.sourceCursor]
				if source.IsFullscreen {
					m.appCore.SetFullscreenSelected(true)
					return m.startMultiWindowSharing()
				} else if source.Window != nil {
					m.appCore.SelectWindow(source.Window.ID)
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
		if m.activeColumn == columnSources && m.appCore.IsAutoShareEnabled() {
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
		if m.appCore.IsSharing() {
			// Notify viewers that sharer has stopped so they reset and wait
			if m.appCore.GetSharer() != nil && m.appCore.GetRoomCode() != "" {
				m.appCore.GetSharer().SendToAllViewers(sig.SignalMessage{Type: "sharer-stopped"})
			}
			m.stopCapture(false)
			m.appCore.ClearSelection()
			if m.appCore.GetPeerManager() != nil {
				m.appCore.GetPeerManager().CloseAllConnections()
			}
		}
		return m, nil

	case "r":
		// Refresh windows
		return m, refreshWindows

	// F for fullscreen - toggles fullscreen selection (mutually exclusive with windows)
	case "f":
		// Disabled in auto-share mode
		if m.appCore.IsAutoShareEnabled() {
			return m, nil
		}
		return m.selection.ToggleFullscreen(&m)

	// Quick window selection with number keys (1-9 selects windows, skipping fullscreen)
	// Disabled in auto-share mode
	case "1":
		if m.appCore.IsAutoShareEnabled() {
			return m, nil
		}
		return m.selectWindowByNumber(1)
	case "2":
		if m.appCore.IsAutoShareEnabled() {
			return m, nil
		}
		return m.selectWindowByNumber(2)
	case "3":
		if m.appCore.IsAutoShareEnabled() {
			return m, nil
		}
		return m.selectWindowByNumber(3)
	case "4":
		if m.appCore.IsAutoShareEnabled() {
			return m, nil
		}
		return m.selectWindowByNumber(4)
	case "5":
		if m.appCore.IsAutoShareEnabled() {
			return m, nil
		}
		return m.selectWindowByNumber(5)
	case "6":
		if m.appCore.IsAutoShareEnabled() {
			return m, nil
		}
		return m.selectWindowByNumber(6)
	case "7":
		if m.appCore.IsAutoShareEnabled() {
			return m, nil
		}
		return m.selectWindowByNumber(7)
	case "8":
		if m.appCore.IsAutoShareEnabled() {
			return m, nil
		}
		return m.selectWindowByNumber(8)
	case "9":
		if m.appCore.IsAutoShareEnabled() {
			return m, nil
		}
		return m.selectWindowByNumber(9)

	case "i":
		// Toggle stats display
		m.showStats = !m.showStats
		return m, nil

	case "c":
		// Copy URL to clipboard
		if m.appCore.GetShareURL() != "" {
			if err := copyToClipboard(m.appCore.GetShareURL()); err == nil {
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
		m.appCore.SetPasswordEnabled(!m.appCore.IsPasswordEnabled())
		if m.appCore.IsPasswordEnabled() {
			m.appCore.SetPassword(sig.GeneratePassword())
		} else {
			m.appCore.SetPassword("")
		}
		// If server is already started, update the room password
		if m.appCore.IsServerStarted() && m.appCore.GetSharer() != nil {
			pwMsg := sig.SignalMessage{Type: "password-update", Password: m.appCore.GetPassword(), Secret: m.appCore.GetRoomSecret()}
			m.appCore.GetSharer().SendToAllViewers(pwMsg)
		}
		return m, nil

	case "a":
		// Toggle adaptive bitrate
		m.appCore.SetAdaptiveBitrate(!m.appCore.IsAdaptiveBitrate())
		// Update if already streaming
		if m.appCore.GetStreamer() != nil {
			m.appCore.GetStreamer().SetAdaptiveBitrate(m.appCore.IsAdaptiveBitrate())
		}
		return m, nil

	case "A": // Shift+A - Toggle auto-share mode
		return m.toggleAutoShareMode()

	case "q":
		// Toggle quality mode (quality vs performance)
		m.appCore.SetQualityMode(!m.appCore.IsQualityMode())
		// Update if already streaming
		if m.appCore.GetStreamer() != nil {
			m.appCore.GetStreamer().SetQualityMode(m.appCore.IsQualityMode())
		}
		return m, nil
	}

	return m, nil
}

// applyQuality changes the quality setting
func (m Model) applyQuality(index int) (tea.Model, tea.Cmd) {
	if index < 0 || index >= len(config.QualityPresets) {
		return m, nil
	}

	oldQuality := m.selectedQuality
	m.selectedQuality = index
	m.qualityCursor = index

	// If we're sharing and quality changed, apply new bitrate dynamically
	if m.appCore.IsSharing() && oldQuality != m.selectedQuality {
		return m.applyBitrateChange()
	}

	return m, nil
}

// applyCodec changes the codec setting dynamically without full restart
func (m Model) applyCodec(index int) (tea.Model, tea.Cmd) {
	if index < 0 || index >= len(config.AvailableCodecs) {
		return m, nil
	}

	oldCodec := m.selectedCodec
	m.selectedCodec = index
	m.codecCursor = index

	// If we're sharing and codec changed, update dynamically
	if m.appCore.IsSharing() && m.appCore.GetStreamer() != nil && oldCodec != m.selectedCodec {
		codecType := m.getSelectedCodecType()
		if err := m.appCore.GetStreamer().SetCodec(codecType); err != nil {
			m.lastError = fmt.Sprintf("Codec change failed: %v", err)
		}
	}

	return m, nil
}

// selectWindowByNumber toggles window selection by its display number (1-9)
// Windows are numbered starting from 1, excluding fullscreen
func (m Model) selectWindowByNumber(num int) (tea.Model, tea.Cmd) {
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
func (m Model) handleOverlayToggle(windowID uint32) (tea.Model, tea.Cmd) {
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
		windowInfo := capture.GetWindowInfoByID(windowID)
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
func (m *Model) syncOverlay() {
	// Sync model state to AppCore - the overlay queries AppCore directly
	if m.appCore != nil {
		m.appCore.SetSelectedWindows(m.appCore.GetSelectedWindows())
		m.appCore.SetFullscreenSelected(m.appCore.IsFullscreenSelected())
		m.appCore.SetSharing(m.appCore.IsSharing())
		m.appCore.SetAutoShareEnabled(m.appCore.IsAutoShareEnabled())
		m.appCore.SetViewerCount(m.appCore.GetViewerCount())
		m.appCore.SetStreamer(m.appCore.GetStreamer())
		m.appCore.SetPeerManager(m.appCore.GetPeerManager())
	}
	// Note: The overlay now runs its own update loop via background thread,
	// and queries state through AppCore (via OverlayController).
}

// getSelectedCodecType returns the currently selected codec type
func (m Model) getSelectedCodecType() encoding.CodecType {
	if m.selectedCodec >= 0 && m.selectedCodec < len(config.AvailableCodecs) {
		return config.AvailableCodecs[m.selectedCodec].Type
	}
	return encoding.CodecVP8
}

// getSelectedFPS returns the currently selected FPS value
func (m Model) getSelectedFPS() int {
	if m.selectedFPS >= 0 && m.selectedFPS < len(config.FPSPresets) {
		return config.FPSPresets[m.selectedFPS].Value
	}
	return 30 // default
}

// getLRUWindow returns the least recently focused window ID for eviction
// excludeWindowID is the window that should not be evicted (typically the new focused window)
func (m Model) getLRUWindow(excludeWindowID uint32) uint32 {
	var lruWindowID uint32
	var lruTime time.Time
	first := true

	for windowID := range m.appCore.GetSelectedWindows() {
		if windowID == excludeWindowID {
			continue // Don't evict the window we're about to focus
		}

		focusTime, exists := m.appCore.GetAutoShareFocusTimes()[windowID]
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
func (m Model) applyFPS(index int) (tea.Model, tea.Cmd) {
	if index < 0 || index >= len(config.FPSPresets) {
		return m, nil
	}

	oldFPS := m.selectedFPS
	m.selectedFPS = index
	m.fpsCursor = index

	// If we're sharing and FPS changed, update dynamically
	if m.appCore.IsSharing() && m.appCore.GetStreamer() != nil && oldFPS != m.selectedFPS {
		fps := m.getSelectedFPS()
		if err := m.appCore.GetStreamer().SetFPS(fps); err != nil {
			m.lastError = fmt.Sprintf("FPS change failed: %v", err)
		}
	}

	return m, nil
}

// applyBitrateChange applies a new bitrate to the running streamer without restart
func (m Model) applyBitrateChange() (tea.Model, tea.Cmd) {
	if !m.appCore.IsSharing() || m.appCore.GetStreamer() == nil {
		return m, nil
	}

	// Use SetBitrate to change bitrate dynamically (no restart needed)
	bitrate := config.QualityPresets[m.selectedQuality].Bitrate
	m.appCore.GetStreamer().SetBitrate(bitrate, bitrate/2)

	return m, nil
}

// toggleAutoShareMode toggles the auto-share mode on/off
// When enabled, the app automatically shares whichever window has OS focus
// Works exactly like normal mode but with automatic window management
func (m Model) toggleAutoShareMode() (tea.Model, tea.Cmd) {
	if m.appCore.IsAutoShareEnabled() {
		// Disable auto-share mode - keep windows streaming (switch to manual mode)
		log.Printf("Auto-share: Disabling mode, switching to manual management")
		capture.StopFocusObserver()
		m.appCore.SetAutoShareEnabled(false)
		m.appCore.ClearAutoShareFocusTimes()
		// DON'T stop streaming - windows stay selected for manual management
		m.syncOverlay() // Show overlay in manual mode
		return m, nil
	}

	// Enable auto-share mode
	log.Printf("Auto-share: Enabling mode, starting focus observer")
	capture.StartFocusObserver()
	m.appCore.SetAutoShareEnabled(true)
	m.appCore.InitAutoShareFocusTimes()
	m.appCore.SetFullscreenSelected(false) // Disable fullscreen in auto mode
	m.syncOverlay()                         // Hide overlay in auto mode

	// If already sharing, keep existing windows and initialize LRU times
	if m.appCore.IsSharing() && m.appCore.GetStreamer() != nil {
		log.Printf("Auto-share: Already sharing, keeping existing %d windows", len(m.appCore.GetSelectedWindows()))
		// Initialize focus times for existing windows
		for windowID := range m.appCore.GetSelectedWindows() {
			m.appCore.TrackFocusTime(windowID)
		}
		return m, fastTickCmd()
	}

	// Not sharing yet - start with focused window
	m.appCore.ClearSelection()

	// Get all shareable windows and find topmost by z-order
	windows, err := capture.ListWindows()
	if err != nil {
		m.lastError = fmt.Sprintf("Failed to list windows: %v", err)
		capture.StopFocusObserver()
		m.appCore.SetAutoShareEnabled(false)
		return m, nil
	}

	if len(windows) == 0 {
		m.lastError = "No shareable windows found"
		capture.StopFocusObserver()
		m.appCore.SetAutoShareEnabled(false)
		return m, nil
	}

	// Extract window IDs for z-order check
	var windowIDs []uint32
	for _, w := range windows {
		windowIDs = append(windowIDs, w.ID)
	}

	// Find topmost window by z-order
	topmost := capture.GetTopmostWindow(windowIDs)

	if topmost == 0 {
		m.lastError = "No topmost window found"
		capture.StopFocusObserver()
		m.appCore.SetAutoShareEnabled(false)
		return m, nil
	}

	// Find window info for the topmost window
	var targetWindow *capture.WindowInfo
	for i := range windows {
		if windows[i].ID == topmost {
			targetWindow = &windows[i]
			break
		}
	}

	if targetWindow == nil {
		m.lastError = "Topmost window not in list"
		capture.StopFocusObserver()
		m.appCore.SetAutoShareEnabled(false)
		return m, nil
	}

	// Initialize focus time for first window
	m.appCore.TrackFocusTime(topmost)

	// Start sharing this window directly (bypass m.sources lookup)
	return m.startAutoShareCapture(*targetWindow)
}

// startAutoShareCapture starts capture for a specific window in auto-share mode
// This bypasses the normal m.sources lookup to ensure the window is captured
func (m Model) startAutoShareCapture(window capture.WindowInfo) (tea.Model, tea.Cmd) {
	if m.appCore.IsStarting() || m.appCore.IsSharing() {
		return m, nil
	}

	m.stopCapture(false)
	if !m.appCore.IsServerStarted() {
		m.stopMultiCapture()
	}
	m.lastError = ""

	// Initialize server
	if err := m.initMultiServer(); err != nil {
		m.lastError = err.Error()
		capture.StopFocusObserver()
		m.appCore.SetAutoShareEnabled(false)
		return m, nil
	}

	m.appCore.SetStarting(true)
	m.appCore.ClearSelection()
	m.appCore.SelectWindow(window.ID)

	// Capture config
	fps := m.getSelectedFPS()
	focusBitrate := config.QualityPresets[m.selectedQuality].Bitrate
	bgBitrate := focusBitrate / 3
	if bgBitrate < 500 {
		bgBitrate = 500
	}
	adaptiveBR := m.appCore.IsAdaptiveBitrate()
	qualityMode := m.appCore.IsQualityMode()
	codecType := m.getSelectedCodecType()

	// Start capture with just this one window, and start fast tick for focus detection
	captureCmd := startMultiCaptureAsync(m.appCore.GetPeerManager(), []capture.WindowInfo{window}, false, fps, focusBitrate, bgBitrate, adaptiveBR, qualityMode, codecType)
	return m, tea.Batch(captureCmd, fastTickCmd())
}

// attemptReconnect tries to reconnect to the remote signal server
func (m Model) attemptReconnect(attempt int, delay time.Duration) tea.Cmd {
	return func() tea.Msg {
		// Wait for the delay
		time.Sleep(delay)

		// Try to reconnect
		signalURL := normalizeSignalURL(m.appCore.GetConfig().SignalURL)

		// Build WebSocket URL
		wsURL := strings.TrimSuffix(signalURL, "/") + "/ws/" + m.appCore.GetRoomCode()

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

			if attempt >= m.appCore.GetMaxReconnects() {
				return reconnectFailedMsg{err: "Failed to reconnect after multiple attempts"}
			}

			return reconnectMsg{attempt: attempt + 1, delay: nextDelay}
		}

		// Join as sharer (with optional password and secret for authentication)
		joinMsg := sig.SignalMessage{Type: "join", Role: "sharer", Password: m.appCore.GetPassword(), Secret: m.appCore.GetRoomSecret()}
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
func (m *Model) initRemoteSignaling() error {
	signalURL := normalizeSignalURL(m.appCore.GetConfig().SignalURL)

	// Build WebSocket URL
	wsURL := strings.TrimSuffix(signalURL, "/") + "/ws/" + m.appCore.GetRoomCode()

	// Build viewer URL
	viewerURL := strings.Replace(signalURL, "wss://", "https://", 1)
	viewerURL = strings.Replace(viewerURL, "ws://", "http://", 1)
	m.appCore.SetShareURL(strings.TrimSuffix(viewerURL, "/") + "/" + m.appCore.GetRoomCode())

	// Try connecting with timeout
	dialer := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
	}
	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		return fmt.Errorf("failed to connect to signal server: %v", err)
	}

	// Join as sharer (with optional password and secret for authentication)
	joinMsg := sig.SignalMessage{Type: "join", Role: "sharer", Password: m.appCore.GetPassword(), Secret: m.appCore.GetRoomSecret()}
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

	m.appCore.SetWSConn(conn)

	// Initialize disconnect flag if needed
	if m.appCore.GetWSDisconnectedPtr() == nil {
		disconnected := false
		m.appCore.SetWSDisconnected(&disconnected)
	}
	*m.appCore.GetWSDisconnectedPtr() = false

	// Set up signaling via WebSocket with disconnect callback
	disconnectFlag := m.appCore.GetWSDisconnectedPtr()
	m.appCore.SetSharer(setupRemoteSignaling(conn, m.appCore.GetPeerManager(), func() {
		*disconnectFlag = true
	}))

	return nil
}

func (m Model) startSharing(index int) (tea.Model, tea.Cmd) {
	if m.appCore.IsStarting() || m.appCore.IsSharing() {
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
		m.appCore.SetFullscreenSelected(true)
		m.appCore.ClearSelection()
		m.appCore.SetIsFullscreen(true)
		m.appCore.SetActiveWindowID(0)
	} else if source.Window != nil {
		// Single window selected - add to selection
		m.appCore.SetFullscreenSelected(false)
		m.appCore.ClearSelection()
		m.appCore.SelectWindow(source.Window.ID)
		m.appCore.SetIsFullscreen(false)
		m.appCore.SetActiveWindowID(source.Window.ID)
	}

	// Use unified multi-window path
	return m.startMultiWindowSharing()
}

// startMultiWindowSharing starts sharing selected windows or fullscreen display
func (m Model) startMultiWindowSharing() (tea.Model, tea.Cmd) {
	// Block streaming if no room code (server connection failed)
	if m.appCore.GetRoomCode() == "" {
		m.lastError = "Cannot start: no room code (server connection failed)"
		return m, nil
	}

	if !m.appCore.IsFullscreenSelected() && len(m.appCore.GetSelectedWindows()) == 0 {
		m.lastError = "No windows or fullscreen selected. Use SPACE to select."
		return m, nil
	}

	if m.appCore.IsStarting() || m.appCore.IsSharing() {
		return m, nil
	}

	m.stopCapture(false)
	// Only do full cleanup if server isn't already running
	// If server is running, keep peerManager alive to reuse the connection
	if !m.appCore.IsServerStarted() {
		m.stopMultiCapture()
	}
	m.lastError = ""

	// Initialize server for multi-window mode
	if err := m.initMultiServer(); err != nil {
		m.lastError = err.Error()
		return m, nil
	}

	m.appCore.SetStarting(true)

	// Collect selected windows info (empty if fullscreen selected)
	var selectedWindowInfos []capture.WindowInfo
	if !m.appCore.IsFullscreenSelected() {
		for _, source := range m.sources {
			if !source.IsFullscreen && source.Window != nil {
				if m.appCore.GetSelectedWindows()[source.Window.ID] {
					selectedWindowInfos = append(selectedWindowInfos, *source.Window)
				}
			}
		}
	}

	// Capture config values for async command
	fps := m.getSelectedFPS()
	focusBitrate := config.QualityPresets[m.selectedQuality].Bitrate
	bgBitrate := focusBitrate / 3 // Background windows get 1/3 bitrate
	if bgBitrate < 500 {
		bgBitrate = 500
	}
	adaptiveBR := m.appCore.IsAdaptiveBitrate()
	qualityMode := m.appCore.IsQualityMode()
	codecType := m.getSelectedCodecType()
	multiPeerManager := m.appCore.GetPeerManager()
	fullscreen := m.appCore.IsFullscreenSelected()

	return m, startMultiCaptureAsync(multiPeerManager, selectedWindowInfos, fullscreen, fps, focusBitrate, bgBitrate, adaptiveBR, qualityMode, codecType)
}

// updateMultiStreamSelection dynamically adds/removes windows/display without full restart
func (m Model) updateMultiStreamSelection() (tea.Model, tea.Cmd) {
	// If not currently streaming, fall back to starting fresh
	if m.appCore.GetStreamer() == nil || !m.appCore.IsSharing() {
		return m.startMultiWindowSharing()
	}

	// Get currently streaming windows (windowID=0 means display is streaming)
	currentWindows := m.appCore.GetStreamer().GetStreamingWindowIDs()
	hasDisplay := currentWindows[0] // windowID 0 = display capture

	// Handle special case: nothing selected - just remove all streams, keep connection alive
	if !m.appCore.IsFullscreenSelected() && len(m.appCore.GetSelectedWindows()) == 0 {
		// Remove display if active
		if hasDisplay {
			log.Printf("TUI: Removing display (no sources selected)")
			if err := m.appCore.GetStreamer().RemoveDisplayDynamic(); err != nil {
				log.Printf("TUI: Failed to remove display: %v", err)
			}
		}
		// Remove all windows
		for windowID := range currentWindows {
			if windowID != 0 {
				log.Printf("TUI: Removing window %d (no sources selected)", windowID)
				if err := m.appCore.GetStreamer().RemoveWindowDynamic(windowID); err != nil {
					log.Printf("TUI: Failed to remove window %d: %v", windowID, err)
				}
			}
		}
		return m, nil
	}

	// Handle fullscreen transitions
	if m.appCore.IsFullscreenSelected() && !hasDisplay {
		// Switching TO fullscreen: remove all windows first, then add display
		for windowID := range currentWindows {
			if windowID != 0 { // Skip display (shouldn't be there anyway)
				log.Printf("TUI: Removing window %d for fullscreen switch", windowID)
				if err := m.appCore.GetStreamer().RemoveWindowDynamic(windowID); err != nil {
					log.Printf("TUI: Failed to remove window %d: %v", windowID, err)
				}
			}
		}
		// Add display
		log.Printf("TUI: Adding display capture")
		if _, err := m.appCore.GetStreamer().AddDisplayDynamic(); err != nil {
			log.Printf("TUI: Failed to add display: %v", err)
			m.lastError = fmt.Sprintf("Failed to start fullscreen: %v", err)
		}
		return m, nil
	}

	if !m.appCore.IsFullscreenSelected() && hasDisplay {
		// Switching FROM fullscreen: remove display
		log.Printf("TUI: Removing display capture")
		if err := m.appCore.GetStreamer().RemoveDisplayDynamic(); err != nil {
			log.Printf("TUI: Failed to remove display: %v", err)
		}
		// Continue to add any selected windows below
	}

	// Find windows to add (skip windowID 0 which is display)
	var windowsToAdd []capture.WindowInfo
	for windowID := range m.appCore.GetSelectedWindows() {
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
		if windowID != 0 && !m.appCore.GetSelectedWindows()[windowID] {
			windowsToRemove = append(windowsToRemove, windowID)
		}
	}

	// Remove windows first (to free up space for new ones)
	for _, windowID := range windowsToRemove {
		log.Printf("TUI: Removing window dynamically: %d", windowID)
		if err := m.appCore.GetStreamer().RemoveWindowDynamic(windowID); err != nil {
			log.Printf("TUI: Failed to remove window %d: %v", windowID, err)
		}
	}

	// Add new windows
	for _, window := range windowsToAdd {
		log.Printf("TUI: Adding window dynamically: %d (%s)", window.ID, window.WindowName)
		if _, err := m.appCore.GetStreamer().AddWindowDynamic(window); err != nil {
			log.Printf("TUI: Failed to add window %d: %v", window.ID, err)
		}
	}

	return m, nil
}

// initMultiServer initializes the server for multi-window mode
func (m *Model) initMultiServer() error {
	if m.appCore.IsServerStarted() && m.appCore.GetPeerManager() != nil {
		return nil
	}

	// Room code must be set before initializing server
	if m.appCore.GetRoomCode() == "" {
		return fmt.Errorf("no room code set")
	}

	// Create multi peer manager
	iceConfig := webrtc.ICEConfig{
		TURNServer: m.appCore.GetConfig().TURNServer,
		TURNUser:   m.appCore.GetConfig().TURNUser,
		TURNPass:   m.appCore.GetConfig().TURNPass,
		ForceRelay: m.appCore.GetConfig().ForceRelay,
	}
	codecType := m.getSelectedCodecType()

	pm, err := webrtc.NewPeerManager(iceConfig, codecType)
	if err != nil {
		return fmt.Errorf("failed to create multi peer manager: %v", err)
	}
	m.appCore.SetPeerManager(pm)

	// Initialize pre-allocated track slots for instant window sharing
	if err := m.appCore.GetPeerManager().InitializeTrackSlots(); err != nil {
		return fmt.Errorf("failed to initialize track slots: %v", err)
	}

	// Connect to signal server
	if err := m.initRemoteSignaling(); err != nil {
		return fmt.Errorf("failed to connect to signal server: %v", err)
	}

	m.appCore.SetServerStarted(true)
	return nil
}

// stopMultiCapture stops multi-window capture
func (m *Model) stopMultiCapture() {
	if m.appCore.GetStreamer() != nil {
		m.appCore.GetStreamer().Stop()
		m.appCore.SetStreamer(nil)
	}
	if m.appCore.GetPeerManager() != nil {
		m.appCore.GetPeerManager().Close()
		m.appCore.SetPeerManager(nil)
	}
}

// startMultiCaptureAsync starts multi-window or display capture asynchronously
func startMultiCaptureAsync(pm *webrtc.PeerManager, windows []capture.WindowInfo, fullscreen bool, fps, focusBitrate, bgBitrate int, adaptiveBR bool, qualityMode bool, codecType encoding.CodecType) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(100 * time.Millisecond)

		// Create multi streamer
		ms := streaming.NewStreamer(pm, fps, focusBitrate, bgBitrate, adaptiveBR, qualityMode)

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
			Streamer:    ms,
			PeerManager: pm,
		}
	}
}

// stopCapture stops the current capture but keeps server running.
// If preserveState is true, keeps isFullscreen and activeWindowID for restart scenarios.
func (m *Model) stopCapture(preserveState bool) {
	// Stop unified streamer
	if m.appCore.GetStreamer() != nil {
		m.appCore.GetStreamer().Stop()
		m.appCore.SetStreamer(nil)
	}

	m.appCore.SetSharing(false)
	m.streamStats = nil
	m.syncOverlay() // Update overlay state (no longer sharing)

	if !preserveState {
		m.selectedSource = -1
		m.appCore.SetIsFullscreen(false)
		m.appCore.SetActiveWindowID(0)
	}
}

// cleanup shuts down everything
func (m *Model) cleanup() {
	// Save settings before cleanup
	currentSettings := settings.UserSettings{
		Quality:         m.selectedQuality,
		FPS:             m.selectedFPS,
		Codec:           m.selectedCodec,
		AdaptiveBitrate: m.appCore.IsAdaptiveBitrate(),
		QualityMode:     m.appCore.IsQualityMode(),
	}
	if err := settings.Save(currentSettings); err != nil {
		log.Printf("Failed to save settings: %v", err)
	}

	m.stopCapture(false)

	// Close unified peer manager
	if m.appCore.GetPeerManager() != nil {
		m.appCore.GetPeerManager().Close()
		m.appCore.SetPeerManager(nil)
	}

	if m.appCore.GetWSConn() != nil {
		m.appCore.GetWSConn().Close()
		m.appCore.SetWSConn(nil)
	}

	// Note: HTTP server doesn't have clean shutdown in current implementation
	m.appCore.SetServerStarted(false)
}

func (m Model) View() string {
	var b strings.Builder

	// Title
	b.WriteString(titleStyle.Render("GoPeep"))
	b.WriteString(dimStyle.Render(" - P2P Screen Sharing"))
	b.WriteString("\n\n")

	// Status bar (if server is running)
	if m.appCore.IsServerStarted() {
		b.WriteString(m.renderSharingStatus())
		b.WriteString("\n")
	} else if m.appCore.GetRoomCode() != "" {
		// Show room code even before streaming starts
		b.WriteString(statusStyle.Render("Room: "))
		b.WriteString(normalStyle.Render(m.appCore.GetRoomCode()))
		b.WriteString("  ")
		if m.appCore.IsServerStarted() {
			b.WriteString(dimStyle.Render("(ready, select source to start)"))
		} else {
			b.WriteString(dimStyle.Render("(connecting...)"))
		}
		b.WriteString("\n\n")
	}

	// Column layout (Sources, Settings, and Viewers when sharing)
	b.WriteString(m.renderColumns())

	// Stats panel (if enabled and sharing)
	if m.showStats && m.appCore.IsSharing() {
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

func (m Model) renderSharingStatus() string {
	var b strings.Builder

	// Mode indicator (only show when reconnecting)
	if m.appCore.IsReconnecting() {
		b.WriteString(errorStyle.Render(fmt.Sprintf("[RECONNECTING %d/%d]", m.appCore.GetReconnectAttempt(), m.appCore.GetMaxReconnects())))
		b.WriteString("  ")
	}

	// Room code and URL (always show once server started)
	b.WriteString(statusStyle.Render("Room: "))
	b.WriteString(normalStyle.Render(m.appCore.GetRoomCode()))
	b.WriteString("  ")

	b.WriteString(statusStyle.Render("URL: "))
	b.WriteString(urlStyle.Render(m.appCore.GetShareURL()))
	// Show copy message if present
	if m.copyMessage != "" {
		b.WriteString("  ")
		b.WriteString(selectedStyle.Render(m.copyMessage))
	}
	// Show password if enabled
	if m.appCore.IsPasswordEnabled() && m.appCore.GetPassword() != "" {
		b.WriteString("  ")
		b.WriteString(statusStyle.Render("Pass: "))
		b.WriteString(selectedStyle.Render(m.appCore.GetPassword()))
	}
	b.WriteString("\n")

	// Show status based on state
	if m.appCore.IsStarting() && len(m.appCore.GetSelectedWindows()) > 0 {
		// Starting multi-window capture
		b.WriteString(statusStyle.Render("Starting: "))
		b.WriteString(normalStyle.Render(fmt.Sprintf("%d windows", len(m.appCore.GetSelectedWindows()))))
		b.WriteString("  ")
		b.WriteString(dimStyle.Render("please wait..."))
	} else if m.appCore.IsStarting() && m.selectedSource >= 0 && m.selectedSource < len(m.sources) {
		// Starting single-window capture (async)
		source := m.sources[m.selectedSource]
		b.WriteString(statusStyle.Render("Starting: "))
		b.WriteString(normalStyle.Render(truncate(source.DisplayName, 30)))
		b.WriteString("  ")
		b.WriteString(dimStyle.Render("please wait..."))
	} else if m.appCore.IsSharing() && m.appCore.GetStreamer() != nil {
		// Multi-window sharing
		streams := m.appCore.GetStreamer().GetStreamsInfo()
		b.WriteString(statusStyle.Render("Sharing: "))
		b.WriteString(selectedStyle.Render(fmt.Sprintf("%d windows", len(streams))))
		if m.appCore.IsAdaptiveBitrate() {
			b.WriteString(dimStyle.Render(" [adaptive]"))
		}
		b.WriteString("  ")

		// Quality
		b.WriteString(statusStyle.Render("Quality: "))
		b.WriteString(normalStyle.Render(config.QualityPresets[m.selectedQuality].Name))
		b.WriteString("  ")

		// Viewer count
		b.WriteString(statusStyle.Render("Viewers: "))
		if m.appCore.GetViewerCount() == 0 {
			b.WriteString(dimStyle.Render("waiting..."))
		} else {
			b.WriteString(viewerStyle.Render(fmt.Sprintf("%d", m.appCore.GetViewerCount())))
		}
	} else if m.appCore.IsSharing() && m.selectedSource >= 0 && m.selectedSource < len(m.sources) {
		// Currently sharing single window
		source := m.sources[m.selectedSource]
		b.WriteString(statusStyle.Render("Sharing: "))
		b.WriteString(selectedStyle.Render(truncate(source.DisplayName, 30)))
		b.WriteString("  ")

		// Quality
		b.WriteString(statusStyle.Render("Quality: "))
		b.WriteString(normalStyle.Render(config.QualityPresets[m.selectedQuality].Name))
		b.WriteString("  ")

		// Codec with hardware indicator
		b.WriteString(statusStyle.Render("Codec: "))
		if m.selectedCodec >= 0 && m.selectedCodec < len(config.AvailableCodecs) {
			codec := config.AvailableCodecs[m.selectedCodec]
			if codec.IsHardware {
				b.WriteString(selectedStyle.Render(codec.Name + " [HW]"))
			} else {
				b.WriteString(normalStyle.Render(codec.Name))
			}
		}
		b.WriteString("  ")

		// Viewer count
		b.WriteString(statusStyle.Render("Viewers: "))
		if m.appCore.GetViewerCount() == 0 {
			b.WriteString(dimStyle.Render("waiting..."))
		} else {
			b.WriteString(viewerStyle.Render(fmt.Sprintf("%d", m.appCore.GetViewerCount())))
		}
	} else {
		b.WriteString(dimStyle.Render("Select a source to start sharing"))
	}
	b.WriteString("\n")

	return b.String()
}

func (m Model) renderColumns() string {
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
	if m.appCore.IsSharing() {
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

func (m Model) renderSourcesList() string {
	var b strings.Builder

	// Show header based on mode
	if m.appCore.IsAutoShareEnabled() {
		// Auto-share mode: show badge and auto-managed window count
		if len(m.appCore.GetSelectedWindows()) > 0 {
			modeText := fmt.Sprintf("AUTO-SHARE: %d/%d windows", len(m.appCore.GetSelectedWindows()), capture.MaxCaptureInstances)
			b.WriteString(selectedStyle.Render(modeText))
		} else {
			b.WriteString(selectedStyle.Render("AUTO-SHARE MODE"))
		}
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("Windows auto-managed (Shift+A to exit)"))
		b.WriteString("\n")
	} else if len(m.appCore.GetSelectedWindows()) > 0 {
		// Normal mode with selections
		modeText := fmt.Sprintf("Selected: %d/%d windows", len(m.appCore.GetSelectedWindows()), capture.MaxCaptureInstances)
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
			if m.appCore.IsFullscreenSelected() {
				checkbox = "[x]"
				isSelected = true
			}
			label = fmt.Sprintf("%s [F] %s", checkbox, source.DisplayName)
		} else {
			// Window with checkbox
			windowNum++
			checkbox := "[ ]"
			if source.Window != nil && m.appCore.GetSelectedWindows()[source.Window.ID] {
				checkbox = "[x]"
				isSelected = true
			}
			// Check if this window has OS focus
			hasFocus := source.Window != nil && source.Window.ID == m.appCore.GetOSFocusedWindowID()
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
		isSharing := m.appCore.IsSharing() && i == m.selectedSource
		isStarting := m.appCore.IsStarting() && i == m.selectedSource

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

func (m Model) renderQualityList() string {
	var b strings.Builder

	b.WriteString(dimStyle.Render("--- Quality ---"))
	b.WriteString("\n")

	for i, preset := range config.QualityPresets {
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

func (m Model) renderFPSList() string {
	var b strings.Builder

	b.WriteString(dimStyle.Render("--- FPS ---"))
	b.WriteString("\n")

	for i, preset := range config.FPSPresets {
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

func (m Model) renderCodecList() string {
	var b strings.Builder

	b.WriteString(dimStyle.Render("--- Codec ---"))
	b.WriteString("\n")

	for i, codec := range config.AvailableCodecs {
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

func (m Model) renderViewerList() string {
	var content strings.Builder

	// Get viewer info from peer manager
	var viewers []webrtc.ViewerInfo
	if m.appCore.GetPeerManager() != nil {
		viewers = m.appCore.GetPeerManager().GetViewerInfo()
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

func (m Model) renderStats() string {
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
	uptime := time.Since(m.appCore.GetStartTime()).Truncate(time.Second)
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

func (m Model) renderHelp() string {
	var b strings.Builder
	sep := keySepStyle.Render(" │ ")

	// Line 1: Regular keybinds (actions)
	var actions []string

	actions = append(actions, keyStyle.Render("tab")+helpStyle.Render(" columns"))
	actions = append(actions, keyStyle.Render("↑↓")+helpStyle.Render(" select"))
	actions = append(actions, keyStyle.Render("space")+helpStyle.Render(" toggle"))
	actions = append(actions, keyStyle.Render("enter")+helpStyle.Render(" start"))
	actions = append(actions, keyStyle.Render("f")+helpStyle.Render(" fullscreen"))

	if m.appCore.IsServerStarted() {
		actions = append(actions, keyStyle.Render("c")+helpStyle.Render(" copy"))
	}

	if m.appCore.IsSharing() {
		actions = append(actions, keyStyle.Render("s")+helpStyle.Render(" stop"))
	}

	actions = append(actions, keyStyle.Render("r")+helpStyle.Render(" refresh"))
	actions = append(actions, keyStyle.Render("^c")+helpStyle.Render(" quit"))

	b.WriteString(strings.Join(actions, sep))

	// Line 2: Toggles with state indicators
	var toggles []string

	// Adaptive bitrate toggle (only before sharing)
	if !m.appCore.IsSharing() && !m.appCore.IsStarting() {
		toggles = append(toggles, m.renderToggle("a", "adaptive", m.appCore.IsAdaptiveBitrate()))
	}

	// Quality mode toggle - shows current mode (quality ON = quality mode, OFF = performance mode)
	if m.appCore.IsQualityMode() {
		toggles = append(toggles, m.renderToggle("q", "quality", true))
	} else {
		toggles = append(toggles, m.renderToggle("q", "performance", false))
	}

	// Password toggle
	toggles = append(toggles, m.renderToggle("p", "password", m.appCore.IsPasswordEnabled()))

	// Stats toggle (only while sharing)
	if m.appCore.IsSharing() {
		toggles = append(toggles, m.renderToggle("i", "stats", m.showStats))
	}

	// Auto-share mode toggle
	toggles = append(toggles, m.renderToggle("A", "auto", m.appCore.IsAutoShareEnabled()))

	if len(toggles) > 0 {
		b.WriteString("\n\n")
		b.WriteString(strings.Join(toggles, "   "))
	}

	return b.String()
}

// renderToggle renders a toggle keybind with active/inactive indicator
func (m Model) renderToggle(key, label string, active bool) string {
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
func RunTUI(cfg config.Config) error {
	// Note: Screen recording permission is checked in main() on the main thread

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

	// Create AppCore - the shared state owner
	appCore := app.NewAppCore(cfg)

	// Create overlay controller (queries AppCore directly) and overlay
	overlayCtrl := app.NewOverlayController(appCore)
	overlayInstance := overlay.New(overlayCtrl)

	// Create the initial model with AppCore and overlay
	m := initialModel(cfg, appCore)
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

// buildStreamsInfo converts tracks to StreamInfo slice
func buildStreamsInfo(tracks []*webrtc.StreamTrackInfo) []sig.StreamInfo {
	streams := make([]sig.StreamInfo, len(tracks))
	for i, t := range tracks {
		streams[i] = sig.StreamInfo{
			TrackID:    t.TrackID,
			WindowName: t.WindowName,
			AppName:    t.AppName,
			IsFocused:  t.IsFocused,
			Width:      t.Width,
			Height:     t.Height,
		}
	}
	return streams
}

// sendOfferToViewer creates and sends an offer along with stream info to a viewer
func sendOfferToViewer(pm *webrtc.PeerManager, sharer sig.Sharer, peerID string) {
	offer, err := pm.CreateOffer(peerID)
	if err != nil {
		log.Printf("Failed to create offer: %v", err)
		return
	}

	sharer.SendToViewer(peerID, sig.SignalMessage{Type: "offer", SDP: offer, PeerID: peerID})
	sharer.SendToViewer(peerID, sig.SignalMessage{Type: "streams-info", Streams: buildStreamsInfo(pm.GetTracks())})
}

// setupSignaling is the SINGLE entry point for all signaling logic.
// Works identically for local embedded server and remote WebSocket.
func setupSignaling(sharer sig.Sharer, pm *webrtc.PeerManager) {
	var peerCounter int
	var peerMu sync.Mutex

	// === CALLBACK REGISTRATION (shared for all modes) ===

	pm.SetICECallback(func(peerID string, candidate string) {
		sharer.SendToViewer(peerID, sig.SignalMessage{Type: "ice", Candidate: candidate, PeerID: peerID})
	})

	pm.SetFocusChangeCallback(func(trackID string) {
		sharer.SendToAllViewers(sig.SignalMessage{Type: "focus-change", FocusedTrack: trackID})
	})

	pm.SetSizeChangeCallback(func(trackID string, width, height int) {
		sharer.SendToAllViewers(sig.SignalMessage{Type: "size-change", TrackID: trackID, Width: width, Height: height})
	})

	pm.SetCursorCallback(func(trackID string, x, y float64, inView bool) {
		sharer.SendToAllViewers(sig.SignalMessage{
			Type:         "cursor-position",
			TrackID:      trackID,
			CursorX:      x,
			CursorY:      y,
			CursorInView: inView,
		})
	})

	pm.SetRenegotiateCallback(func(peerID string, offer string) {
		log.Printf("Renegotiation: sending offer to peer %s", peerID)
		sharer.SendToViewer(peerID, sig.SignalMessage{Type: "offer", SDP: offer, PeerID: peerID})
		sharer.SendToViewer(peerID, sig.SignalMessage{Type: "streams-info", Streams: buildStreamsInfo(pm.GetTracks())})
		log.Printf("Renegotiation: sent streams-info with %d tracks to peer %s", len(pm.GetTracks()), peerID)
	})

	pm.SetStreamChangeCallbacks(
		func(info sig.StreamInfo) {
			log.Printf("Broadcasting stream-added: %s", info.TrackID)
			sharer.SendToAllViewers(sig.SignalMessage{Type: "stream-added", StreamAdded: &info})
		},
		func(trackID string) {
			log.Printf("Broadcasting stream-removed: %s", trackID)
			sharer.SendToAllViewers(sig.SignalMessage{Type: "stream-removed", StreamRemoved: trackID})
		},
	)

	pm.SetStreamActivationCallbacks(
		func(info sig.StreamInfo) {
			log.Printf("Broadcasting stream-activated: %s (fast path)", info.TrackID)
			sharer.SendToAllViewers(sig.SignalMessage{Type: "stream-activated", StreamActivated: &info})
		},
		func(trackID string) {
			log.Printf("Broadcasting stream-deactivated: %s (fast path)", trackID)
			sharer.SendToAllViewers(sig.SignalMessage{Type: "stream-deactivated", StreamDeactivated: trackID})
		},
	)

	// === MESSAGE HANDLING LOOP (shared for all modes) ===

	go func() {
		for data := range sharer.Messages() {
			var msg sig.SignalMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				log.Printf("Invalid message: %v", err)
				continue
			}

			switch msg.Type {
			case "viewer-joined":
				found, assignPeerID := sharer.GetUnassignedViewer()
				if !found {
					continue
				}

				peerMu.Lock()
				peerCounter++
				peerID := fmt.Sprintf("viewer-%d", peerCounter)
				peerMu.Unlock()

				assignPeerID(peerID)
				go sendOfferToViewer(pm, sharer, peerID)

			case "viewer-reoffer":
				peerID := msg.PeerID
				if peerID == "" {
					log.Printf("viewer-reoffer received without peerID")
					continue
				}

				found, assignPeerID := sharer.GetUnassignedViewer()
				if !found {
					log.Printf("viewer-reoffer: no unassigned viewer found for %s", peerID)
					continue
				}
				assignPeerID(peerID)

				log.Printf("Sending reoffer to existing viewer: %s", peerID)
				go sendOfferToViewer(pm, sharer, peerID)

			case "answer":
				if msg.PeerID == "" {
					continue
				}
				if err := pm.HandleAnswer(msg.PeerID, msg.SDP); err != nil {
					log.Printf("Failed to handle answer for %s: %v", msg.PeerID, err)
				}

			case "ice":
				if msg.PeerID == "" {
					continue
				}
				if err := pm.AddICECandidate(msg.PeerID, msg.Candidate); err != nil {
					log.Printf("Failed to add ICE candidate for %s: %v", msg.PeerID, err)
				}

			case "renegotiate-answer":
				if msg.PeerID == "" {
					continue
				}
				if err := pm.HandleRenegotiateAnswer(msg.PeerID, msg.SDP); err != nil {
					log.Printf("Failed to handle renegotiate answer for %s: %v", msg.PeerID, err)
				}

			case "error":
				log.Printf("Signal server error: %s", msg.Error)
			}
		}

		log.Printf("Signaling connection closed")
	}()
}

// setupRemoteSignaling sets up signaling for remote WebSocket mode
func setupRemoteSignaling(conn *websocket.Conn, pm *webrtc.PeerManager, onDisconnect func()) sig.Sharer {
	sharer := sig.NewRemoteSharer(conn)
	sharer.SetDisconnectHandler(onDisconnect)
	setupSignaling(sharer, pm)
	return sharer
}
