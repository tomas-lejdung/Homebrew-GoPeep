package tui

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tomaslejdung/gopeep/internal/app"
	"github.com/tomaslejdung/gopeep/internal/capture"
	"github.com/tomaslejdung/gopeep/internal/config"
	"github.com/tomaslejdung/gopeep/internal/encoding"
	"github.com/tomaslejdung/gopeep/internal/ui/overlay"
	"github.com/tomaslejdung/gopeep/internal/ui/settings"
	"github.com/tomaslejdung/gopeep/internal/webrtc"
)

// Message types, SourceItem, and column constants are in types.go
// Styles are in styles.go
// SelectionManager is in selection.go
// Keyboard handling is in keys.go
// Message handlers are in handlers.go
// Capture/streaming lifecycle is in capture.go
// Utility functions are in utils.go

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

// findSourceIndex returns the index of the source matching the current capture state.
func (m *Model) findSourceIndex() int {
	if !m.appCore.IsSharing() && !m.appCore.IsStarting() {
		return -1
	}

	if m.appCore.IsFullscreen() {
		if len(m.sources) > 0 && m.sources[0].IsFullscreen {
			return 0
		}
		return -1
	}

	for i, source := range m.sources {
		if !source.IsFullscreen && source.Window != nil && source.Window.ID == m.appCore.GetActiveWindowID() {
			return i
		}
	}
	return -1
}

func initialModel(cfg config.Config, appCore *app.AppCore) Model {
	config.InitAvailableCodecs()

	savedSettings, err := settings.Load()
	if err != nil {
		log.Printf("Failed to load settings: %v", err)
		savedSettings = settings.DefaultSettings()
	}

	// Validate indices
	if savedSettings.Quality < 0 || savedSettings.Quality >= len(config.QualityPresets) {
		savedSettings.Quality = config.DefaultQualityIndex()
	}
	if savedSettings.FPS < 0 || savedSettings.FPS >= len(config.FPSPresets) {
		savedSettings.FPS = config.DefaultFPSIndex()
	}
	if savedSettings.Codec < 0 || savedSettings.Codec >= len(config.AvailableCodecs) {
		savedSettings.Codec = config.DefaultCodecIndex()
	}

	// CLI flags override saved settings
	fpsIndex := savedSettings.FPS
	if cfg.FPS != 30 {
		fpsIndex = config.FPSIndexForValue(cfg.FPS)
	}

	// Initialize AppCore settings
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
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg {
		return fastTickMsg(t)
	})
}

// Update dispatches messages to appropriate handlers
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKey(msg)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case windowsUpdatedMsg:
		return m.handleWindowsUpdated(msg)

	case viewerCountMsg:
		m.appCore.SetViewerCount(int(msg))
		return m, nil

	case roomCodeReceivedMsg:
		return m.handleRoomCodeReceived(msg)

	case captureStartedMsg:
		return m.handleCaptureStarted(msg)

	case captureErrorMsg:
		return m.handleCaptureError(msg)

	case osFocusChangedMsg:
		m.appCore.SetOSFocusedWindowID(msg.windowID)
		return m, nil

	case overlayToggleMsg:
		return m.handleOverlayToggle(msg.windowID)

	case overlayFullscreenToggleMsg:
		if m.appCore.IsAutoShareEnabled() {
			return m, nil
		}
		return m.selection.ToggleFullscreen(&m)

	case overlayClearAllMsg:
		return m.selection.ClearSelection(&m)

	case tickMsg:
		return m.handleTickMsg()

	case fastTickMsg:
		return m.handleFastTickMsg()

	case reconnectMsg:
		return m.handleReconnectMsg(msg)

	case reconnectedMsg:
		return m.handleReconnectedMsg(msg)

	case reconnectFailedMsg:
		return m.handleReconnectFailedMsg(msg)
	}

	return m, nil
}

// syncOverlay updates the overlay controller with current state
func (m *Model) syncOverlay() {
	if m.appCore != nil {
		m.appCore.SetSelectedWindows(m.appCore.GetSelectedWindows())
		m.appCore.SetFullscreenSelected(m.appCore.IsFullscreenSelected())
		m.appCore.SetSharing(m.appCore.IsSharing())
		m.appCore.SetAutoShareEnabled(m.appCore.IsAutoShareEnabled())
		m.appCore.SetViewerCount(m.appCore.GetViewerCount())
		m.appCore.SetStreamer(m.appCore.GetStreamer())
		m.appCore.SetPeerManager(m.appCore.GetPeerManager())
	}
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
	return 30
}

// getLRUWindow returns the least recently focused window ID for eviction
func (m Model) getLRUWindow(excludeWindowID uint32) uint32 {
	var lruWindowID uint32
	var lruTime time.Time
	first := true

	for windowID := range m.appCore.GetSelectedWindows() {
		if windowID == excludeWindowID {
			continue
		}

		focusTime, exists := m.appCore.GetAutoShareFocusTimes()[windowID]
		if !exists {
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

// applyQuality changes the quality setting
func (m Model) applyQuality(index int) (tea.Model, tea.Cmd) {
	if index < 0 || index >= len(config.QualityPresets) {
		return m, nil
	}

	oldQuality := m.selectedQuality
	m.selectedQuality = index
	m.qualityCursor = index

	if m.appCore.IsSharing() && oldQuality != m.selectedQuality {
		return m.applyBitrateChange()
	}

	return m, nil
}

// applyCodec changes the codec setting dynamically
func (m Model) applyCodec(index int) (tea.Model, tea.Cmd) {
	if index < 0 || index >= len(config.AvailableCodecs) {
		return m, nil
	}

	oldCodec := m.selectedCodec
	m.selectedCodec = index
	m.codecCursor = index

	if m.appCore.IsSharing() && m.appCore.GetStreamer() != nil && oldCodec != m.selectedCodec {
		codecType := m.getSelectedCodecType()
		if err := m.appCore.GetStreamer().SetCodec(codecType); err != nil {
			m.lastError = fmt.Sprintf("Codec change failed: %v", err)
		}
	}

	return m, nil
}

// applyFPS changes the FPS setting dynamically
func (m Model) applyFPS(index int) (tea.Model, tea.Cmd) {
	if index < 0 || index >= len(config.FPSPresets) {
		return m, nil
	}

	oldFPS := m.selectedFPS
	m.selectedFPS = index
	m.fpsCursor = index

	if m.appCore.IsSharing() && m.appCore.GetStreamer() != nil && oldFPS != m.selectedFPS {
		fps := m.getSelectedFPS()
		if err := m.appCore.GetStreamer().SetFPS(fps); err != nil {
			m.lastError = fmt.Sprintf("FPS change failed: %v", err)
		}
	}

	return m, nil
}

// applyBitrateChange applies a new bitrate to the running streamer
func (m Model) applyBitrateChange() (tea.Model, tea.Cmd) {
	if !m.appCore.IsSharing() || m.appCore.GetStreamer() == nil {
		return m, nil
	}

	bitrate := config.QualityPresets[m.selectedQuality].Bitrate
	m.appCore.GetStreamer().SetBitrate(bitrate, bitrate/2)

	return m, nil
}

// View renders the TUI
func (m Model) View() string {
	var b strings.Builder

	// Title
	b.WriteString(titleStyle.Render("GoPeep"))
	b.WriteString(dimStyle.Render(" - P2P Screen Sharing"))
	b.WriteString("\n\n")

	// Status bar
	if m.appCore.IsServerStarted() {
		b.WriteString(m.renderSharingStatus())
		b.WriteString("\n")
	} else if m.appCore.GetRoomCode() != "" {
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

	// Column layout
	b.WriteString(m.renderColumns())

	// Stats panel
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

	if m.appCore.IsReconnecting() {
		b.WriteString(errorStyle.Render(fmt.Sprintf("[RECONNECTING %d/%d]", m.appCore.GetReconnectAttempt(), m.appCore.GetMaxReconnects())))
		b.WriteString("  ")
	}

	b.WriteString(statusStyle.Render("Room: "))
	b.WriteString(normalStyle.Render(m.appCore.GetRoomCode()))
	b.WriteString("  ")

	b.WriteString(statusStyle.Render("URL: "))
	b.WriteString(urlStyle.Render(m.appCore.GetShareURL()))
	if m.copyMessage != "" {
		b.WriteString("  ")
		b.WriteString(selectedStyle.Render(m.copyMessage))
	}
	if m.appCore.IsPasswordEnabled() && m.appCore.GetPassword() != "" {
		b.WriteString("  ")
		b.WriteString(statusStyle.Render("Pass: "))
		b.WriteString(selectedStyle.Render(m.appCore.GetPassword()))
	}
	b.WriteString("\n")

	if m.appCore.IsStarting() && len(m.appCore.GetSelectedWindows()) > 0 {
		b.WriteString(statusStyle.Render("Starting: "))
		b.WriteString(normalStyle.Render(fmt.Sprintf("%d windows", len(m.appCore.GetSelectedWindows()))))
		b.WriteString("  ")
		b.WriteString(dimStyle.Render("please wait..."))
	} else if m.appCore.IsStarting() && m.selectedSource >= 0 && m.selectedSource < len(m.sources) {
		b.WriteString(statusStyle.Render("Starting: "))
		b.WriteString(normalStyle.Render(m.sources[m.selectedSource].DisplayName))
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
		if m.appCore.GetSelectedCount() > 0 {
			modeText := fmt.Sprintf("AUTO-SHARE: %d/%d windows", m.appCore.GetSelectedCount(), capture.MaxCaptureInstances)
			b.WriteString(selectedStyle.Render(modeText))
		} else {
			b.WriteString(selectedStyle.Render("AUTO-SHARE MODE"))
		}
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("Windows auto-managed (Shift+A to exit)"))
		b.WriteString("\n")
	} else if m.appCore.GetSelectedCount() > 0 {
		// Normal mode with selections
		modeText := fmt.Sprintf("Selected: %d/%d windows", m.appCore.GetSelectedCount(), capture.MaxCaptureInstances)
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
			if source.Window != nil && m.appCore.IsWindowSelected(source.Window.ID) {
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

		// Format: name + description
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
		var totalFrames, totalBytes uint64

		for i, stat := range m.streamStats {
			totalFrames += stat.Frames
			totalBytes += stat.Bytes

			appName := stat.AppName
			if appName == "" {
				appName = "Display"
			}
			appName = truncate(appName, 12)

			resStr := fmt.Sprintf("%dx%d", stat.Width, stat.Height)
			bitrateStr := fmt.Sprintf("%.1fMbps", stat.Bitrate/1000)
			dataStr := formatBytes(stat.Bytes)

			focusMarker := ""
			if stat.IsFocused {
				focusMarker = " *"
			}

			line := fmt.Sprintf("%d: %-12s %s | %s | %s%s",
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
			formatNumber(int(totalFrames)), formatBytes(totalBytes))))
	}

	b.WriteString(statsBoxStyle.Render(content.String()))
	return b.String()
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

	// Stats toggle (only when sharing)
	if m.appCore.IsSharing() {
		toggles = append(toggles, m.renderToggle("i", "stats", m.showStats))
	}

	// Auto-share mode toggle
	toggles = append(toggles, m.renderToggle("A", "auto", m.appCore.IsAutoShareEnabled()))

	if len(toggles) > 0 {
		b.WriteString("\n")
		b.WriteString(strings.Join(toggles, "   "))
	}

	return b.String()
}

func (m Model) renderToggle(key, label string, active bool) string {
	if active {
		return toggleActiveStyle.Render("◉ "+key) + " " + toggleActiveStyle.Render(label)
	}
	return toggleInactiveStyle.Render("○ "+key) + " " + toggleInactiveStyle.Render(label)
}

// RunTUI starts the TUI application
func RunTUI(cfg config.Config) error {
	// Write logs to file only when debug mode is enabled
	if cfg.Debug {
		logFile, err := os.Create("gopeep-debug.log")
		if err != nil {
			log.SetOutput(io.Discard)
		} else {
			log.SetOutput(logFile)
			log.Printf("=== GoPeep started at %s ===", time.Now().Format(time.RFC3339))
			defer logFile.Close()
		}
		defer log.SetOutput(os.Stderr)
	} else {
		log.SetOutput(io.Discard)
	}

	// Create AppCore - the shared state owner
	appCore := app.NewAppCore(cfg)

	// Create overlay controller and overlay
	overlayCtrl := app.NewOverlayController(appCore)
	overlayInstance := overlay.New(overlayCtrl)

	// Create the initial model
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

	overlayInstance.Stop()

	return runErr
}
