package tui

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tomaslejdung/gopeep/internal/app"
	"github.com/tomaslejdung/gopeep/internal/capture"
	"github.com/tomaslejdung/gopeep/internal/config"
	"github.com/tomaslejdung/gopeep/internal/encoding"
	"github.com/tomaslejdung/gopeep/internal/webrtc"
	"github.com/tomaslejdung/gopeep/internal/ui/overlay"
	"github.com/tomaslejdung/gopeep/internal/ui/settings"
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
	} else if m.appCore.IsSharing() {
		b.WriteString(statusStyle.Render("Streaming: "))
		if m.appCore.GetStreamer() != nil {
			count := m.appCore.GetStreamer().GetActiveStreamCount()
			if count == 1 {
				// Show source name for single stream
				tracks := m.appCore.GetStreamer().GetStreamingWindowIDs()
				if tracks[0] {
					b.WriteString(normalStyle.Render("Fullscreen"))
				} else {
					for _, source := range m.sources {
						if source.Window != nil && tracks[source.Window.ID] {
							b.WriteString(normalStyle.Render(source.DisplayName))
							break
						}
					}
				}
			} else {
				b.WriteString(normalStyle.Render(fmt.Sprintf("%d windows", count)))
			}
		}
		b.WriteString("  ")
		b.WriteString(statusStyle.Render("Viewers: "))
		b.WriteString(viewerStyle.Render(fmt.Sprintf("%d", m.appCore.GetViewerCount())))
		if m.appCore.GetStartTime().IsZero() == false {
			b.WriteString("  ")
			b.WriteString(dimStyle.Render(formatDuration(time.Since(m.appCore.GetStartTime()))))
		}
	}

	return b.String()
}

func (m Model) renderColumns() string {
	var b strings.Builder

	// Sources column
	sources := m.renderSourcesList()

	// Settings column (quality, fps, codec stacked)
	settingsCol := m.renderSettingsColumn()

	// Simple side-by-side layout
	srcLines := strings.Split(sources, "\n")
	settingsLines := strings.Split(settingsCol, "\n")

	maxLines := len(srcLines)
	if len(settingsLines) > maxLines {
		maxLines = len(settingsLines)
	}

	for i := 0; i < maxLines; i++ {
		srcLine := ""
		if i < len(srcLines) {
			srcLine = srcLines[i]
		}
		settingsLine := ""
		if i < len(settingsLines) {
			settingsLine = settingsLines[i]
		}

		// Pad source column to consistent width
		padded := srcLine
		for len(padded) < 50 {
			padded += " "
		}

		b.WriteString(padded)
		b.WriteString("  ")
		b.WriteString(settingsLine)
		b.WriteString("\n")
	}

	return b.String()
}

func (m Model) renderSettingsColumn() string {
	var b strings.Builder

	// Quality
	b.WriteString(m.renderQualityList())
	b.WriteString("\n")

	// FPS
	b.WriteString(m.renderFPSList())
	b.WriteString("\n")

	// Codec
	b.WriteString(m.renderCodecList())

	// Viewers if sharing
	if m.appCore.IsSharing() && m.appCore.GetPeerManager() != nil {
		b.WriteString("\n")
		b.WriteString(m.renderViewerList())
	}

	return b.String()
}

func (m Model) renderSourcesList() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("Sources"))
	b.WriteString("\n")

	for i, source := range m.sources {
		cursor := "  "
		if i == m.sourceCursor && m.activeColumn == columnSources {
			cursor = "> "
		}

		// Determine selection state
		isSelected := false
		if source.IsFullscreen {
			isSelected = m.appCore.IsFullscreenSelected()
		} else if source.Window != nil {
			isSelected = m.appCore.IsWindowSelected(source.Window.ID)
		}

		// Format display name with selection indicator
		name := source.DisplayName
		if !source.IsFullscreen {
			// Add window number
			windowNum := 0
			for j, s := range m.sources {
				if !s.IsFullscreen {
					windowNum++
					if j == i {
						name = fmt.Sprintf("[%d] %s", windowNum, truncate(source.DisplayName, 35))
						break
					}
				}
			}
		}

		style := normalStyle
		if isSelected {
			style = selectedStyle
			name = "✓ " + name
		}

		b.WriteString(cursor)
		b.WriteString(style.Render(name))
		b.WriteString("\n")
	}

	return b.String()
}

func (m Model) renderQualityList() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("Quality"))
	b.WriteString("\n")

	for i, preset := range config.QualityPresets {
		cursor := "  "
		if i == m.qualityCursor && m.activeColumn == columnQuality {
			cursor = "> "
		}

		style := normalStyle
		if i == m.selectedQuality {
			style = selectedStyle
		}

		b.WriteString(cursor)
		b.WriteString(style.Render(fmt.Sprintf("%s (%d kbps)", preset.Name, preset.Bitrate)))
		b.WriteString("\n")
	}

	return b.String()
}

func (m Model) renderFPSList() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("FPS"))
	b.WriteString("\n")

	for i, preset := range config.FPSPresets {
		cursor := "  "
		if i == m.fpsCursor && m.activeColumn == columnFPS {
			cursor = "> "
		}

		style := normalStyle
		if i == m.selectedFPS {
			style = selectedStyle
		}

		b.WriteString(cursor)
		b.WriteString(style.Render(fmt.Sprintf("%d fps", preset.Value)))
		b.WriteString("\n")
	}

	return b.String()
}

func (m Model) renderCodecList() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("Codec"))
	b.WriteString("\n")

	for i, codec := range config.AvailableCodecs {
		cursor := "  "
		if i == m.codecCursor && m.activeColumn == columnCodec {
			cursor = "> "
		}

		style := normalStyle
		if i == m.selectedCodec {
			style = selectedStyle
		}

		b.WriteString(cursor)
		b.WriteString(style.Render(codec.Name))
		b.WriteString("\n")
	}

	return b.String()
}

func (m Model) renderViewerList() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("Viewers"))
	b.WriteString("\n")

	count := m.appCore.GetViewerCount()
	if count == 0 {
		b.WriteString(dimStyle.Render("  No viewers"))
	} else {
		b.WriteString(viewerStyle.Render(fmt.Sprintf("  %d connected", count)))
	}
	b.WriteString("\n")

	return b.String()
}

func (m Model) renderStats() string {
	var b strings.Builder
	b.WriteString(headerStyle.Render("Stats"))
	b.WriteString("\n")

	if len(m.streamStats) == 0 {
		b.WriteString(dimStyle.Render("  No active streams"))
		return b.String()
	}

	for _, stat := range m.streamStats {
		name := stat.AppName
		if name == "" {
			name = "Display"
		}
		b.WriteString(fmt.Sprintf("  %s: %.0f fps, %.0f kbps\n",
			truncate(name, 20),
			stat.FPS,
			stat.Bitrate))
	}

	return b.String()
}

func (m Model) renderHelp() string {
	var b strings.Builder

	// Key hints
	hints := []string{
		"↑/↓ navigate",
		"SPACE select",
		"ENTER start",
		"s stop",
		"r refresh",
		"c copy URL",
		"Ctrl+C quit",
	}
	b.WriteString(dimStyle.Render(strings.Join(hints, "  ")))

	// Toggle indicators
	var toggles []string

	// Adaptive bitrate toggle
	if !m.appCore.IsSharing() && !m.appCore.IsStarting() {
		toggles = append(toggles, m.renderToggle("a", "adaptive", m.appCore.IsAdaptiveBitrate()))
	}

	// Quality mode toggle
	if m.appCore.IsQualityMode() {
		toggles = append(toggles, m.renderToggle("q", "quality", true))
	} else {
		toggles = append(toggles, m.renderToggle("q", "performance", false))
	}

	// Password toggle
	toggles = append(toggles, m.renderToggle("p", "password", m.appCore.IsPasswordEnabled()))

	// Stats toggle
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

func (m Model) renderToggle(key, label string, active bool) string {
	if active {
		return toggleActiveStyle.Render("◉ "+key) + " " + toggleActiveStyle.Render(label)
	}
	return toggleInactiveStyle.Render("○ "+key) + " " + toggleInactiveStyle.Render(label)
}

// RunTUI starts the TUI application
func RunTUI(cfg config.Config) error {
	// Write logs to file instead of corrupting TUI display
	logFile, err := os.Create("gopeep-debug.log")
	if err != nil {
		log.SetOutput(io.Discard)
	} else {
		log.SetOutput(logFile)
		log.Printf("=== GoPeep started at %s ===", time.Now().Format(time.RFC3339))
		defer logFile.Close()
	}

	defer log.SetOutput(os.Stderr)

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
