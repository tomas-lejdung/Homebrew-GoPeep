package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tomaslejdung/gopeep/internal/config"
	sig "github.com/tomaslejdung/gopeep/internal/signal"
)

// handleKey processes keyboard input
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
		return m.handleKeyUp()

	case "down", "j":
		return m.handleKeyDown()

	case "enter":
		return m.handleKeyEnter()

	case " ":
		return m.handleKeySpace()

	case "s":
		// Stop sharing (but keep server running)
		return m.handleKeyStop()

	case "r":
		// Refresh windows
		return m, refreshWindows

	case "f":
		// F for fullscreen - toggles fullscreen selection
		if m.appCore.IsAutoShareEnabled() {
			return m, nil
		}
		return m.selection.ToggleFullscreen(&m)

	// Quick window selection with number keys (1-9)
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		if m.appCore.IsAutoShareEnabled() {
			return m, nil
		}
		num := int(msg.String()[0] - '0')
		return m.selectWindowByNumber(num)

	case "i":
		// Toggle stats display
		m.showStats = !m.showStats
		return m, nil

	case "c":
		// Copy URL to clipboard
		return m.handleKeyCopy()

	case "p":
		// Toggle password protection
		return m.handleKeyPassword()

	case "a":
		// Toggle adaptive bitrate
		return m.handleKeyAdaptive()

	case "A":
		// Shift+A - Toggle auto-share mode
		return m.toggleAutoShareMode()

	case "q":
		// Toggle quality mode (quality vs performance)
		return m.handleKeyQuality()
	}

	return m, nil
}

// handleKeyUp handles up/k key navigation
func (m Model) handleKeyUp() (tea.Model, tea.Cmd) {
	if m.activeColumn == columnSources {
		if m.sourceCursor > 0 {
			m.sourceCursor--
		}
	} else if m.activeColumn == columnQuality {
		if m.qualityCursor > 0 {
			m.qualityCursor--
		}
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
}

// handleKeyDown handles down/j key navigation
func (m Model) handleKeyDown() (tea.Model, tea.Cmd) {
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
}

// handleKeyEnter handles enter key for selection
func (m Model) handleKeyEnter() (tea.Model, tea.Cmd) {
	// In auto-share mode, ignore source selection via enter
	if m.activeColumn == columnSources && m.appCore.IsAutoShareEnabled() {
		return m, nil
	}
	if m.activeColumn == columnSources {
		// Start sharing based on selection (fullscreen or windows)
		if m.appCore.IsFullscreenSelected() {
			return m.startMultiWindowSharing()
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
}

// handleKeySpace handles space key for toggling selection
func (m Model) handleKeySpace() (tea.Model, tea.Cmd) {
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
}

// handleKeyStop handles 's' key to stop sharing
func (m Model) handleKeyStop() (tea.Model, tea.Cmd) {
	if m.appCore.IsSharing() {
		// Notify viewers that sharer has stopped so they reset and wait (via DataChannel)
		if m.appCore.GetPeerManager() != nil && m.appCore.GetRoomCode() != "" {
			m.appCore.GetPeerManager().BroadcastControlMessage(sig.SignalMessage{Type: "sharer-stopped"})
		}
		m.stopCapture(false)
		m.appCore.ClearSelection()
		if m.appCore.GetPeerManager() != nil {
			m.appCore.GetPeerManager().CloseAllConnections()
		}
	}
	return m, nil
}

// handleKeyCopy handles 'c' key to copy URL
func (m Model) handleKeyCopy() (tea.Model, tea.Cmd) {
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
}

// handleKeyPassword handles 'p' key to toggle password
func (m Model) handleKeyPassword() (tea.Model, tea.Cmd) {
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
}

// handleKeyAdaptive handles 'a' key to toggle adaptive bitrate
func (m Model) handleKeyAdaptive() (tea.Model, tea.Cmd) {
	m.appCore.SetAdaptiveBitrate(!m.appCore.IsAdaptiveBitrate())
	// Update if already streaming
	if m.appCore.GetStreamer() != nil {
		m.appCore.GetStreamer().SetAdaptiveBitrate(m.appCore.IsAdaptiveBitrate())
	}
	return m, nil
}

// handleKeyQuality handles 'q' key to toggle quality mode
func (m Model) handleKeyQuality() (tea.Model, tea.Cmd) {
	m.appCore.SetQualityMode(!m.appCore.IsQualityMode())
	// Update if already streaming
	if m.appCore.GetStreamer() != nil {
		m.appCore.GetStreamer().SetQualityMode(m.appCore.IsQualityMode())
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
