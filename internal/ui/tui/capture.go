package tui

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/gorilla/websocket"
	"github.com/tomaslejdung/gopeep/internal/capture"
	"github.com/tomaslejdung/gopeep/internal/config"
	"github.com/tomaslejdung/gopeep/internal/encoding"
	sig "github.com/tomaslejdung/gopeep/internal/signal"
	"github.com/tomaslejdung/gopeep/internal/streaming"
	"github.com/tomaslejdung/gopeep/internal/ui/settings"
	"github.com/tomaslejdung/gopeep/internal/webrtc"
)

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

// startSharing starts sharing based on index (legacy single-window entry point)
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
		m.appCore.SetFullscreenSelected(true)
		m.appCore.ClearSelection()
		m.appCore.SetIsFullscreen(true)
		m.appCore.SetActiveWindowID(0)
	} else if source.Window != nil {
		m.appCore.SetFullscreenSelected(false)
		m.appCore.ClearSelection()
		m.appCore.SelectWindow(source.Window.ID)
		m.appCore.SetIsFullscreen(false)
		m.appCore.SetActiveWindowID(source.Window.ID)
	}

	return m.startMultiWindowSharing()
}

// startMultiWindowSharing starts sharing selected windows or fullscreen display
func (m Model) startMultiWindowSharing() (tea.Model, tea.Cmd) {
	// Block streaming if no room code
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
	bgBitrate := focusBitrate / 3
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
	if m.appCore.GetStreamer() == nil || !m.appCore.IsSharing() {
		return m.startMultiWindowSharing()
	}

	// Get currently streaming windows (windowID=0 means display is streaming)
	currentWindows := m.appCore.GetStreamer().GetStreamingWindowIDs()
	hasDisplay := currentWindows[0]

	// Handle special case: nothing selected
	if !m.appCore.IsFullscreenSelected() && len(m.appCore.GetSelectedWindows()) == 0 {
		if hasDisplay {
			log.Printf("TUI: Removing display (no sources selected)")
			if err := m.appCore.GetStreamer().RemoveDisplayDynamic(); err != nil {
				log.Printf("TUI: Failed to remove display: %v", err)
			}
		}
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
		// Switching TO fullscreen
		for windowID := range currentWindows {
			if windowID != 0 {
				log.Printf("TUI: Removing window %d for fullscreen switch", windowID)
				if err := m.appCore.GetStreamer().RemoveWindowDynamic(windowID); err != nil {
					log.Printf("TUI: Failed to remove window %d: %v", windowID, err)
				}
			}
		}
		log.Printf("TUI: Adding display capture")
		if _, err := m.appCore.GetStreamer().AddDisplayDynamic(); err != nil {
			log.Printf("TUI: Failed to add display: %v", err)
			m.lastError = fmt.Sprintf("Failed to start fullscreen: %v", err)
		}
		return m, nil
	}

	if !m.appCore.IsFullscreenSelected() && hasDisplay {
		// Switching FROM fullscreen
		log.Printf("TUI: Removing display capture")
		if err := m.appCore.GetStreamer().RemoveDisplayDynamic(); err != nil {
			log.Printf("TUI: Failed to remove display: %v", err)
		}
	}

	// Find windows to add
	var windowsToAdd []capture.WindowInfo
	for windowID := range m.appCore.GetSelectedWindows() {
		if windowID != 0 && !currentWindows[windowID] {
			for _, source := range m.sources {
				if source.Window != nil && source.Window.ID == windowID {
					windowsToAdd = append(windowsToAdd, *source.Window)
					break
				}
			}
		}
	}

	// Find windows to remove
	var windowsToRemove []uint32
	for windowID := range currentWindows {
		if windowID != 0 && !m.appCore.GetSelectedWindows()[windowID] {
			windowsToRemove = append(windowsToRemove, windowID)
		}
	}

	// Remove windows first
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

	// Initialize pre-allocated track slots
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

		ms := streaming.NewStreamer(pm, fps, focusBitrate, bgBitrate, adaptiveBR, qualityMode)

		if fullscreen {
			_, err := ms.AddDisplay()
			if err != nil {
				ms.Stop()
				return captureErrorMsg{err: fmt.Sprintf("Failed to start fullscreen capture: %v", err)}
			}
		} else {
			for _, win := range windows {
				_, err := ms.AddWindow(win)
				if err != nil {
					ms.Stop()
					return captureErrorMsg{err: fmt.Sprintf("Failed to add window %s: %v", win.DisplayName(), err)}
				}
			}
		}

		if err := ms.Start(); err != nil {
			ms.Stop()
			return captureErrorMsg{err: fmt.Sprintf("Failed to start multi-streamer: %v", err)}
		}

		pm.RenegotiateAllPeers()

		return captureStartedMsg{
			Streamer:    ms,
			PeerManager: pm,
		}
	}
}

// stopCapture stops the current capture but keeps server running
func (m *Model) stopCapture(preserveState bool) {
	if m.appCore.GetStreamer() != nil {
		m.appCore.GetStreamer().Stop()
		m.appCore.SetStreamer(nil)
	}

	m.appCore.SetSharing(false)
	m.streamStats = nil
	m.syncOverlay()

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

	if m.appCore.GetPeerManager() != nil {
		m.appCore.GetPeerManager().Close()
		m.appCore.SetPeerManager(nil)
	}

	if m.appCore.GetWSConn() != nil {
		m.appCore.GetWSConn().Close()
		m.appCore.SetWSConn(nil)
	}

	m.appCore.SetServerStarted(false)
}

// attemptReconnect tries to reconnect to the remote signal server
func (m Model) attemptReconnect(attempt int, delay time.Duration) tea.Cmd {
	return func() tea.Msg {
		time.Sleep(delay)

		signalURL := normalizeSignalURL(m.appCore.GetConfig().SignalURL)
		wsURL := strings.TrimSuffix(signalURL, "/") + "/ws/" + m.appCore.GetRoomCode()

		dialer := websocket.Dialer{
			HandshakeTimeout: 5 * time.Second,
		}
		conn, _, err := dialer.Dial(wsURL, nil)
		if err != nil {
			nextDelay := delay * 2
			if nextDelay > 30*time.Second {
				nextDelay = 30 * time.Second
			}

			if attempt >= m.appCore.GetMaxReconnects() {
				return reconnectFailedMsg{err: "Failed to reconnect after multiple attempts"}
			}

			return reconnectMsg{attempt: attempt + 1, delay: nextDelay}
		}

		joinMsg := sig.SignalMessage{Type: "join", Role: "sharer", Password: m.appCore.GetPassword(), Secret: m.appCore.GetRoomSecret()}
		if err := conn.WriteJSON(joinMsg); err != nil {
			conn.Close()
			return reconnectMsg{attempt: attempt + 1, delay: delay * 2}
		}

		var joinResp sig.SignalMessage
		if err := conn.ReadJSON(&joinResp); err != nil {
			conn.Close()
			return reconnectMsg{attempt: attempt + 1, delay: delay * 2}
		}
		if joinResp.Type == "error" {
			conn.Close()
			return reconnectFailedMsg{err: joinResp.Error}
		}

		return reconnectedMsg{conn: conn}
	}
}

// toggleAutoShareMode toggles the auto-share mode on/off
func (m Model) toggleAutoShareMode() (tea.Model, tea.Cmd) {
	if m.appCore.IsAutoShareEnabled() {
		log.Printf("Auto-share: Disabling mode, switching to manual management")
		capture.StopFocusObserver()
		m.appCore.SetAutoShareEnabled(false)
		m.appCore.ClearAutoShareFocusTimes()
		m.syncOverlay()
		return m, nil
	}

	log.Printf("Auto-share: Enabling mode, starting focus observer")
	capture.StartFocusObserver()
	m.appCore.SetAutoShareEnabled(true)
	m.appCore.InitAutoShareFocusTimes()
	m.appCore.SetFullscreenSelected(false)
	m.syncOverlay()

	// If already sharing, keep existing windows
	if m.appCore.IsSharing() && m.appCore.GetStreamer() != nil {
		log.Printf("Auto-share: Already sharing, keeping existing %d windows", len(m.appCore.GetSelectedWindows()))
		for windowID := range m.appCore.GetSelectedWindows() {
			m.appCore.TrackFocusTime(windowID)
		}
		return m, fastTickCmd()
	}

	// Not sharing yet - start with focused window
	m.appCore.ClearSelection()

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

	var windowIDs []uint32
	for _, w := range windows {
		windowIDs = append(windowIDs, w.ID)
	}

	topmost := capture.GetTopmostWindow(windowIDs)

	if topmost == 0 {
		m.lastError = "No topmost window found"
		capture.StopFocusObserver()
		m.appCore.SetAutoShareEnabled(false)
		return m, nil
	}

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

	m.appCore.TrackFocusTime(topmost)
	return m.startAutoShareCapture(*targetWindow)
}

// startAutoShareCapture starts capture for a specific window in auto-share mode
func (m Model) startAutoShareCapture(window capture.WindowInfo) (tea.Model, tea.Cmd) {
	if m.appCore.IsStarting() || m.appCore.IsSharing() {
		return m, nil
	}

	m.stopCapture(false)
	if !m.appCore.IsServerStarted() {
		m.stopMultiCapture()
	}
	m.lastError = ""

	if err := m.initMultiServer(); err != nil {
		m.lastError = err.Error()
		capture.StopFocusObserver()
		m.appCore.SetAutoShareEnabled(false)
		return m, nil
	}

	m.appCore.SetStarting(true)
	m.appCore.ClearSelection()
	m.appCore.SelectWindow(window.ID)

	fps := m.getSelectedFPS()
	focusBitrate := config.QualityPresets[m.selectedQuality].Bitrate
	bgBitrate := focusBitrate / 3
	if bgBitrate < 500 {
		bgBitrate = 500
	}
	adaptiveBR := m.appCore.IsAdaptiveBitrate()
	qualityMode := m.appCore.IsQualityMode()
	codecType := m.getSelectedCodecType()

	captureCmd := startMultiCaptureAsync(m.appCore.GetPeerManager(), []capture.WindowInfo{window}, false, fps, focusBitrate, bgBitrate, adaptiveBR, qualityMode, codecType)
	return m, tea.Batch(captureCmd, fastTickCmd())
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

// setupSignaling is the SINGLE entry point for all signaling logic
func setupSignaling(sharer sig.Sharer, pm *webrtc.PeerManager) {
	var peerCounter int
	var peerMu sync.Mutex

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
