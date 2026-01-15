package tui

import (
	"fmt"
	"log"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tomaslejdung/gopeep/internal/capture"
	sig "github.com/tomaslejdung/gopeep/internal/signal"
)

// handleWindowsUpdated processes window list updates
func (m Model) handleWindowsUpdated(msg windowsUpdatedMsg) (tea.Model, tea.Cmd) {
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
		return m, nil
	}

	m.sources = newSources

	// Reconcile selection: find the source matching our active capture by window ID
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
}

// handleRoomCodeReceived processes room code from server
func (m Model) handleRoomCodeReceived(msg roomCodeReceivedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		log.Printf("Failed to get room code from server: %v", msg.err)
		m.lastError = fmt.Sprintf("Server error: %v", msg.err)
		return m, nil
	}
	m.appCore.SetRoomCode(msg.roomCode, msg.roomSecret, "")
	log.Printf("Received room code from server: %s", m.appCore.GetRoomCode())

	// Initialize server synchronously
	if err := m.initMultiServer(); err != nil {
		log.Printf("Failed to initialize server: %v", err)
		m.lastError = err.Error()
	}
	return m, nil
}

// handleCaptureStarted processes successful capture start
func (m Model) handleCaptureStarted(msg captureStartedMsg) (tea.Model, tea.Cmd) {
	m.appCore.SetStarting(false)
	m.appCore.SetSharing(true)
	m.appCore.SetStreamer(msg.Streamer)
	m.appCore.SetPeerManager(msg.PeerManager)
	m.appCore.SetStartTime(time.Now())
	m.showStats = true
	m.syncOverlay()

	// Notify viewers that sharer has started (via DataChannel)
	if m.appCore.GetPeerManager() != nil && m.appCore.GetRoomCode() != "" {
		log.Printf("Broadcasting sharer-started to room %s", m.appCore.GetRoomCode())
		m.appCore.GetPeerManager().BroadcastControlMessage(sig.SignalMessage{Type: "sharer-started"})
	}

	// If in auto-share mode, start fast tick for rapid focus detection
	if m.appCore.IsAutoShareEnabled() {
		return m, tea.Batch(tickCmd(), fastTickCmd())
	}
	return m, tickCmd()
}

// handleCaptureError processes capture failure
func (m Model) handleCaptureError(msg captureErrorMsg) (tea.Model, tea.Cmd) {
	m.appCore.SetStarting(false)
	m.appCore.SetSharing(false)
	m.selectedSource = -1
	m.appCore.SetIsFullscreen(false)
	m.appCore.SetActiveWindowID(0)
	m.lastError = msg.err
	return m, refreshWindows
}

// handleTickMsg processes periodic tick
func (m Model) handleTickMsg() (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	cmds = append(cmds, tickCmd())
	cmds = append(cmds, refreshWindows)

	// Poll for topmost window among all visible windows
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
}

// handleFastTickMsg processes fast tick for auto-share mode
func (m Model) handleFastTickMsg() (tea.Model, tea.Cmd) {
	if m.appCore.IsAutoShareEnabled() && m.appCore.IsSharing() && m.appCore.GetStreamer() != nil {
		// Check if focus changed via OS notification
		if capture.CheckFocusChanged() {
			log.Printf("Auto-share: Focus change detected via OS notification")
		}

		// Extract window IDs from sources
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
				// Find window info from sources
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
		}

		// Sync overlay to update window count display
		m.syncOverlay()
		return m, fastTickCmd()
	}

	// If auto-share enabled but not sharing yet, keep ticking
	if m.appCore.IsAutoShareEnabled() {
		return m, fastTickCmd()
	}

	return m, nil
}

// handleReconnectMsg processes reconnection attempt
func (m Model) handleReconnectMsg(msg reconnectMsg) (tea.Model, tea.Cmd) {
	m.appCore.SetReconnecting(true)
	m.appCore.SetReconnectAttempt(msg.attempt)
	m.appCore.SetReconnectDelay(msg.delay)
	return m, m.attemptReconnect(msg.attempt, msg.delay)
}

// handleReconnectedMsg processes successful reconnection
func (m Model) handleReconnectedMsg(msg reconnectedMsg) (tea.Model, tea.Cmd) {
	m.appCore.SetReconnecting(false)
	m.appCore.SetReconnectAttempt(0)
	m.lastError = ""
	m.appCore.SetWSConn(msg.conn)

	// Reset disconnect flag
	if m.appCore.GetWSDisconnectedPtr() != nil {
		*m.appCore.GetWSDisconnectedPtr() = false
	}

	// Set up signaling via the new WebSocket
	disconnectFlag := m.appCore.GetWSDisconnectedPtr()
	setupRemoteSignaling(m.appCore.GetWSConn(), m.appCore.GetPeerManager(), func() {
		if disconnectFlag != nil {
			*disconnectFlag = true
		}
	})
	return m, nil
}

// handleReconnectFailedMsg processes reconnection failure
func (m Model) handleReconnectFailedMsg(msg reconnectFailedMsg) (tea.Model, tea.Cmd) {
	m.appCore.SetReconnecting(false)
	m.lastError = msg.err
	m.appCore.SetServerStarted(false)
	return m, nil
}

// handleOverlayToggle handles the overlay button click to toggle window selection
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
		// Window not in sources list - try to get its info directly
		windowInfo := capture.GetWindowInfoByID(windowID)
		if windowInfo == nil {
			log.Printf("Overlay: Window %d not found via CGWindowList, ignoring", windowID)
			return m, nil
		}

		// Add window to sources dynamically
		log.Printf("Overlay: Dynamically adding window %d (%s) to sources", windowID, windowInfo.DisplayName())
		m.sources = append(m.sources, SourceItem{Window: windowInfo})
	}

	return m.selection.ToggleWindow(&m, windowID)
}
