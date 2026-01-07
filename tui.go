package main

import (
	"fmt"
	"image"
	"io"
	"log"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/gorilla/websocket"
)

// Column indices
const (
	columnSources = 0
	columnQuality = 1
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
	selectedSource int // -1 if not sharing

	// Quality
	qualityCursor   int
	selectedQuality int

	// Two-column navigation
	activeColumn int // 0 = sources, 1 = quality

	// Sharing state
	sharing      bool
	isFullscreen bool // true if sharing fullscreen
	roomCode     string
	shareURL     string
	viewerCount  int
	lastError    string
	startTime    time.Time // when sharing started

	// Stats display
	showStats bool
	stats     StreamStats

	// Components (persistent across source switches)
	server      *SignalServer
	peerManager *PeerManager
	streamer    *Streamer
	wsConn      *websocket.Conn // Remote signal server connection
	isRemote    bool            // Using remote signal server

	// Server started flag
	serverStarted bool

	// Terminal dimensions
	width  int
	height int
}

func initialModel(config Config) model {
	// Set default signal URL if not in local mode and not already set
	if config.SignalURL == "" && !config.LocalMode {
		config.SignalURL = DefaultSignalServer
	}

	return model{
		config:          config,
		sourceCursor:    0,
		selectedSource:  -1,
		qualityCursor:   DefaultQualityIndex(),
		selectedQuality: DefaultQualityIndex(),
		activeColumn:    columnSources,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		refreshWindows,
		tea.SetWindowTitle("GoPeep - Screen Sharing"),
	)
}

func refreshWindows() tea.Msg {
	windows, _ := ListWindows()
	return windowsUpdatedMsg{windows: windows}
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second*2, func(t time.Time) tea.Msg {
		return tickMsg(t)
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
		// If we're sharing fullscreen and got no windows, keep the existing list
		// (fullscreen capture may cause ListWindows to return empty)
		if m.sharing && m.isFullscreen && len(msg.windows) == 0 && len(m.sources) > 1 {
			return m, nil
		}

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

		// Only update if we got windows, or we're not sharing fullscreen
		if len(msg.windows) > 0 || !m.sharing || !m.isFullscreen {
			m.sources = newSources
		}

		// Keep cursor in bounds
		if m.sourceCursor >= len(m.sources) {
			m.sourceCursor = max(0, len(m.sources)-1)
		}

		return m, nil

	case viewerCountMsg:
		m.viewerCount = int(msg)
		return m, nil

	case tickMsg:
		// Periodic refresh
		var cmds []tea.Cmd
		cmds = append(cmds, tickCmd())

		// Refresh windows list
		cmds = append(cmds, refreshWindows)

		// Update viewer count and stats if sharing
		if m.sharing && m.server != nil {
			m.viewerCount = m.server.GetViewerCount(m.roomCode)
		}
		if m.sharing && m.streamer != nil {
			m.stats = m.streamer.GetStats()
		}

		return m, tea.Batch(cmds...)
	}

	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		m.cleanup()
		return m, tea.Quit

	case "tab", "right", "l":
		// Switch to next column
		m.activeColumn = (m.activeColumn + 1) % 2
		return m, nil

	case "shift+tab", "left", "h":
		// Switch to previous column
		m.activeColumn = (m.activeColumn + 1) % 2
		return m, nil

	case "up", "k":
		if m.activeColumn == columnSources {
			if m.sourceCursor > 0 {
				m.sourceCursor--
			}
		} else {
			if m.qualityCursor > 0 {
				m.qualityCursor--
			}
		}
		return m, nil

	case "down", "j":
		if m.activeColumn == columnSources {
			if m.sourceCursor < len(m.sources)-1 {
				m.sourceCursor++
			}
		} else {
			if m.qualityCursor < len(QualityPresets)-1 {
				m.qualityCursor++
			}
		}
		return m, nil

	case "enter", " ":
		if m.activeColumn == columnSources {
			// Select source and start sharing
			if len(m.sources) > 0 && m.sourceCursor < len(m.sources) {
				return m.startSharing(m.sourceCursor)
			}
		} else {
			// Select quality
			return m.applyQuality(m.qualityCursor)
		}
		return m, nil

	case "s":
		// Stop sharing (but keep server running)
		if m.sharing {
			m.stopCapture()
		}
		return m, nil

	case "r":
		// Refresh windows
		return m, refreshWindows

	// Quick quality selection with number keys
	case "1":
		return m.applyQuality(0)
	case "2":
		return m.applyQuality(1)
	case "3":
		return m.applyQuality(2)
	case "4":
		return m.applyQuality(3)
	case "5":
		return m.applyQuality(4)
	case "6":
		return m.applyQuality(5)
	case "7":
		return m.applyQuality(6)

	case "i":
		// Toggle stats display
		m.showStats = !m.showStats
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

	// If we're sharing and quality changed, restart the streamer
	if m.sharing && oldQuality != m.selectedQuality {
		return m.restartStreamer()
	}

	return m, nil
}

// restartStreamer restarts the streamer with new quality settings
func (m model) restartStreamer() (tea.Model, tea.Cmd) {
	if !m.sharing || m.peerManager == nil {
		return m, nil
	}

	// Stop current streamer (but not capture)
	if m.streamer != nil {
		m.streamer.Stop()
		m.streamer = nil
	}

	// Create new streamer with updated bitrate
	bitrate := QualityPresets[m.selectedQuality].Bitrate
	m.streamer = NewStreamerWithBitrate(m.peerManager, m.config.FPS, bitrate)
	m.streamer.SetCaptureFunc(func() (*image.RGBA, error) {
		return GetLatestFrame(time.Second)
	})

	if err := m.streamer.Start(); err != nil {
		m.lastError = fmt.Sprintf("Failed to restart streamer: %v", err)
		return m, nil
	}

	return m, nil
}

// initServer initializes the server and room (only once)
func (m *model) initServer() error {
	if m.serverStarted {
		return nil
	}

	// Generate room code (only once)
	m.roomCode = GenerateRoomCode()

	// Create peer manager with ICE config (reused across source switches)
	iceConfig := ICEConfig{
		TURNServer: m.config.TURNServer,
		TURNUser:   m.config.TURNUser,
		TURNPass:   m.config.TURNPass,
		ForceRelay: m.config.ForceRelay,
	}
	var err error
	m.peerManager, err = NewPeerManagerWithICE(iceConfig)
	if err != nil {
		return fmt.Errorf("failed to create peer manager: %v", err)
	}

	// Try remote signal server first (unless local mode is forced)
	if !m.config.LocalMode && m.config.SignalURL != "" {
		if err := m.initRemoteSignaling(); err == nil {
			m.serverStarted = true
			return nil
		}
		// Fall through to local mode
	}

	// Local mode: start local signal server
	m.isRemote = false
	m.server = NewSignalServer()
	addr := fmt.Sprintf(":%d", m.config.Port)

	go func() {
		m.server.StartServer(addr)
	}()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	// Get local IP
	localIP := getLocalIP()
	m.shareURL = fmt.Sprintf("http://%s:%d/%s", localIP, m.config.Port, m.roomCode)

	// Set up signaling (connects server to peer manager)
	setupSignaling(m.server, m.peerManager, m.roomCode)

	m.serverStarted = true
	return nil
}

// initRemoteSignaling connects to the remote signal server
func (m *model) initRemoteSignaling() error {
	signalURL := m.config.SignalURL

	// Normalize URL scheme
	if strings.HasPrefix(signalURL, "http://") {
		signalURL = "ws://" + strings.TrimPrefix(signalURL, "http://")
	} else if strings.HasPrefix(signalURL, "https://") {
		signalURL = "wss://" + strings.TrimPrefix(signalURL, "https://")
	} else if !strings.HasPrefix(signalURL, "ws://") && !strings.HasPrefix(signalURL, "wss://") {
		signalURL = "wss://" + signalURL
	}

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

	// Join as sharer
	joinMsg := SignalMessage{Type: "join", Role: "sharer"}
	if err := conn.WriteJSON(joinMsg); err != nil {
		conn.Close()
		return fmt.Errorf("failed to send join message: %v", err)
	}

	// Wait for join confirmation
	var joinResp SignalMessage
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

	// Set up signaling via WebSocket
	setupRemoteSignaling(conn, m.peerManager)

	return nil
}

func (m model) startSharing(index int) (tea.Model, tea.Cmd) {
	if index >= len(m.sources) {
		return m, nil
	}

	// Stop any existing capture (but keep server)
	m.stopCapture()

	source := m.sources[index]
	m.selectedSource = index
	m.isFullscreen = source.IsFullscreen
	m.lastError = ""

	// Initialize server if not already running
	if err := m.initServer(); err != nil {
		m.lastError = err.Error()
		return m, nil
	}

	// Start capture
	var err error
	if source.IsFullscreen {
		err = StartDisplayCapture(0, 0, m.config.FPS)
	} else {
		err = StartWindowCapture(source.Window.ID, 0, 0, m.config.FPS)
	}

	if err != nil {
		m.lastError = fmt.Sprintf("Failed to start capture: %v", err)
		return m, nil
	}

	// Create new streamer with selected quality
	bitrate := QualityPresets[m.selectedQuality].Bitrate
	m.streamer = NewStreamerWithBitrate(m.peerManager, m.config.FPS, bitrate)
	m.streamer.SetCaptureFunc(func() (*image.RGBA, error) {
		return GetLatestFrame(time.Second)
	})

	if err := m.streamer.Start(); err != nil {
		m.lastError = fmt.Sprintf("Failed to start streamer: %v", err)
		StopCapture()
		return m, nil
	}

	m.sharing = true
	m.startTime = time.Now()

	return m, tickCmd()
}

// stopCapture stops the current capture but keeps server running
func (m *model) stopCapture() {
	if m.streamer != nil {
		m.streamer.Stop()
		m.streamer = nil
	}

	StopCapture()

	m.sharing = false
	m.selectedSource = -1
	m.isFullscreen = false
}

// cleanup shuts down everything
func (m *model) cleanup() {
	m.stopCapture()

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
	}

	// Two-column layout
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
	if m.isRemote {
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
	b.WriteString("\n")

	// Currently sharing
	if m.sharing && m.selectedSource >= 0 && m.selectedSource < len(m.sources) {
		source := m.sources[m.selectedSource]
		b.WriteString(statusStyle.Render("Sharing: "))
		b.WriteString(selectedStyle.Render(truncate(source.DisplayName, 40)))
		b.WriteString("  ")

		// Quality
		b.WriteString(statusStyle.Render("Quality: "))
		b.WriteString(normalStyle.Render(QualityPresets[m.selectedQuality].Name))
		b.WriteString(dimStyle.Render(fmt.Sprintf(" (%s)", QualityPresets[m.selectedQuality].Description)))
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

	// Render quality column
	qualityContent := m.renderQualityList()

	// Create boxes with appropriate styles based on active column
	var sourcesBox, qualityBox string

	sourcesTitle := " Sources "
	qualityTitle := " Quality "

	if m.activeColumn == columnSources {
		sourcesBox = activeBoxStyle.Width(44).Render(
			boxTitleStyle.Render(sourcesTitle) + "\n" + sourcesContent,
		)
		qualityBox = inactiveBoxStyle.Width(26).Render(
			boxTitleDimStyle.Render(qualityTitle) + "\n" + qualityContent,
		)
	} else {
		sourcesBox = inactiveBoxStyle.Width(44).Render(
			boxTitleDimStyle.Render(sourcesTitle) + "\n" + sourcesContent,
		)
		qualityBox = activeBoxStyle.Width(26).Render(
			boxTitleStyle.Render(qualityTitle) + "\n" + qualityContent,
		)
	}

	// Join columns horizontally
	return lipgloss.JoinHorizontal(lipgloss.Top, sourcesBox, " ", qualityBox)
}

func (m model) renderSourcesList() string {
	var b strings.Builder

	if len(m.sources) == 0 {
		b.WriteString(dimStyle.Render("No sources available"))
		return b.String()
	}

	for i, source := range m.sources {
		cursor := "  "
		if m.activeColumn == columnSources && i == m.sourceCursor {
			cursor = "> "
		}

		// Format label
		var label string
		if source.IsFullscreen {
			label = "[F] " + source.DisplayName
		} else {
			label = fmt.Sprintf("[%d] %s", i, truncate(source.DisplayName, 34))
		}

		// Style based on selection state
		var line string
		isSharing := m.sharing && i == m.selectedSource

		if isSharing {
			line = selectedStyle.Render(cursor + label)
		} else if m.activeColumn == columnSources && i == m.sourceCursor {
			line = normalStyle.Render(cursor + label)
		} else {
			line = dimStyle.Render(cursor + label)
		}

		b.WriteString(line)
		if isSharing {
			b.WriteString(dimStyle.Render(" *"))
		}
		b.WriteString("\n")
	}

	return strings.TrimSuffix(b.String(), "\n")
}

func (m model) renderQualityList() string {
	var b strings.Builder

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

func (m model) renderStats() string {
	var b strings.Builder

	// Stats box style
	statsBoxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("8")).
		Padding(0, 1).
		Width(72)

	var content strings.Builder
	content.WriteString(boxTitleDimStyle.Render(" Stats "))
	content.WriteString("\n")

	// Uptime
	uptime := time.Since(m.startTime).Truncate(time.Second)
	content.WriteString(dimStyle.Render("Uptime: "))
	content.WriteString(normalStyle.Render(formatDuration(uptime)))
	content.WriteString("  ")

	// Resolution
	content.WriteString(dimStyle.Render("Resolution: "))
	if m.stats.Width > 0 {
		content.WriteString(normalStyle.Render(fmt.Sprintf("%dx%d", m.stats.Width, m.stats.Height)))
	} else {
		content.WriteString(dimStyle.Render("--"))
	}
	content.WriteString("  ")

	// FPS
	content.WriteString(dimStyle.Render("FPS: "))
	if m.stats.ActualFPS > 0 {
		content.WriteString(normalStyle.Render(fmt.Sprintf("%.1f/%d", m.stats.ActualFPS, m.stats.TargetFPS)))
	} else {
		content.WriteString(dimStyle.Render(fmt.Sprintf("--/%d", m.stats.TargetFPS)))
	}
	content.WriteString("  ")

	// Bitrate
	content.WriteString(dimStyle.Render("Bitrate: "))
	if m.stats.ActualBPS > 0 {
		actualMbps := float64(m.stats.ActualBPS) * 8 / 1_000_000
		content.WriteString(normalStyle.Render(fmt.Sprintf("%.1f Mbps", actualMbps)))
	} else {
		content.WriteString(dimStyle.Render("--"))
	}
	content.WriteString("\n")

	// Frames and bytes sent
	content.WriteString(dimStyle.Render("Frames: "))
	content.WriteString(normalStyle.Render(formatNumber(m.stats.FramesSent)))
	content.WriteString("  ")

	content.WriteString(dimStyle.Render("Data: "))
	content.WriteString(normalStyle.Render(formatBytes(m.stats.BytesSent)))
	content.WriteString("  ")

	// Viewer info with connection type
	if m.peerManager != nil {
		viewers := m.peerManager.GetViewerInfo()
		content.WriteString(dimStyle.Render("Viewers: "))
		if len(viewers) == 0 {
			content.WriteString(dimStyle.Render("none"))
		} else {
			var viewerStrs []string
			for _, v := range viewers {
				state := v.State
				if state == "connected" {
					connTime := time.Since(v.ConnectedAt).Truncate(time.Second)
					connType := ""
					if v.ConnectionType == "relay" {
						connType = " TURN"
					} else if v.ConnectionType == "direct" {
						connType = " P2P"
					}
					viewerStrs = append(viewerStrs, fmt.Sprintf("%s (%s%s)", v.PeerID, connTime, connType))
				} else {
					viewerStrs = append(viewerStrs, fmt.Sprintf("%s [%s]", v.PeerID, state))
				}
			}
			content.WriteString(normalStyle.Render(strings.Join(viewerStrs, ", ")))
		}
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
	var parts []string

	parts = append(parts, "tab/←→ switch column")
	parts = append(parts, "↑/↓ navigate")
	parts = append(parts, "enter select")
	parts = append(parts, "1-7 quality")

	if m.sharing {
		parts = append(parts, "i stats")
		parts = append(parts, "s stop")
	}

	parts = append(parts, "r refresh")
	parts = append(parts, "q quit")

	return helpStyle.Render(strings.Join(parts, " • "))
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

	// Disable logging in TUI mode (would corrupt the display)
	log.SetOutput(io.Discard)

	// Restore logging on exit
	defer log.SetOutput(os.Stderr)

	p := tea.NewProgram(
		initialModel(config),
		tea.WithAltScreen(),
	)

	_, err := p.Run()
	return err
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
