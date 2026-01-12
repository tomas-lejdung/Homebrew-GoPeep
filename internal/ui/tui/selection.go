package tui

import (
	"log"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tomaslejdung/gopeep/internal/capture"
)

// SelectionManager handles all selection state changes centrally.
// TUI and overlay should use these methods instead of manipulating state directly.
// This is a stateless helper - methods receive model as parameter for bubbletea compatibility.
// All state is stored in AppCore, not Model.
type SelectionManager struct{}

// --- Mutation Methods ---

// ToggleFullscreen toggles fullscreen selection (F key / overlay button).
// When enabling fullscreen, clears all window selections.
func (SelectionManager) ToggleFullscreen(m *Model) (tea.Model, tea.Cmd) {
	if len(m.sources) == 0 || !m.sources[0].IsFullscreen {
		return *m, nil
	}

	// Toggle via AppCore
	isSelected := m.appCore.IsFullscreenSelected()
	m.appCore.SetFullscreenSelected(!isSelected)

	m.sourceCursor = 0
	m.syncOverlay()

	return selectionPostChange(m)
}

// ToggleWindow toggles a window's selection (Space key on window / overlay click).
// Selecting a window always clears fullscreen mode.
// Handles capacity limits with LRU eviction.
func (SelectionManager) ToggleWindow(m *Model, windowID uint32) (tea.Model, tea.Cmd) {
	// Selecting/toggling a window always clears fullscreen
	m.appCore.SetFullscreenSelected(false)

	if m.appCore.IsWindowSelected(windowID) {
		// Deselect
		m.appCore.DeselectWindow(windowID)
		m.appCore.ClearFocusTime(windowID)
	} else {
		// Select - enforce capacity with LRU eviction
		if m.appCore.GetSelectedCount() >= capture.MaxCaptureInstances {
			lruID := m.getLRUWindow(windowID)
			if lruID != 0 {
				m.appCore.DeselectWindow(lruID)
				m.appCore.ClearFocusTime(lruID)
				log.Printf("SelectionManager: Evicted LRU window %d to make room", lruID)
			}
		}
		m.appCore.SelectWindow(windowID)
		m.appCore.TrackFocusTime(windowID)
	}

	m.syncOverlay()

	return selectionPostChange(m)
}

// SelectWindow ensures a window is selected (doesn't toggle, for explicit selection).
// Clears fullscreen mode and handles capacity limits.
func (SelectionManager) SelectWindow(m *Model, windowID uint32) (tea.Model, tea.Cmd) {
	// Clear fullscreen when selecting a window
	m.appCore.SetFullscreenSelected(false)

	if !m.appCore.IsWindowSelected(windowID) {
		// Not already selected - add it
		if m.appCore.GetSelectedCount() >= capture.MaxCaptureInstances {
			lruID := m.getLRUWindow(windowID)
			if lruID != 0 {
				m.appCore.DeselectWindow(lruID)
				m.appCore.ClearFocusTime(lruID)
				log.Printf("SelectionManager: Evicted LRU window %d to make room", lruID)
			}
		}
		m.appCore.SelectWindow(windowID)
		m.appCore.TrackFocusTime(windowID)
	}

	m.syncOverlay()

	return selectionPostChange(m)
}

// DeselectWindow removes a window from selection.
func (SelectionManager) DeselectWindow(m *Model, windowID uint32) (tea.Model, tea.Cmd) {
	if m.appCore.IsWindowSelected(windowID) {
		m.appCore.DeselectWindow(windowID)
		m.appCore.ClearFocusTime(windowID)
		m.syncOverlay()
		return selectionPostChange(m)
	}

	return *m, nil
}

// ClearSelection clears all selections (windows and fullscreen).
func (SelectionManager) ClearSelection(m *Model) (tea.Model, tea.Cmd) {
	m.appCore.ClearSelection()
	m.syncOverlay()

	return selectionPostChange(m)
}

// --- Getter Methods (delegate to AppCore) ---

// IsFullscreenSelected returns true if fullscreen mode is selected.
func (SelectionManager) IsFullscreenSelected(m *Model) bool {
	return m.appCore.IsFullscreenSelected()
}

// IsWindowSelected returns true if the given window is selected.
func (SelectionManager) IsWindowSelected(m *Model, windowID uint32) bool {
	return m.appCore.IsWindowSelected(windowID)
}

// GetSelectedWindows returns a slice of selected window IDs.
func (SelectionManager) GetSelectedWindows(m *Model) []uint32 {
	windows := m.appCore.GetSelectedWindows()
	result := make([]uint32, 0, len(windows))
	for id := range windows {
		result = append(result, id)
	}
	return result
}

// GetSelectedCount returns number of selected windows (0 if fullscreen).
func (SelectionManager) GetSelectedCount(m *Model) int {
	return m.appCore.GetSelectedCount()
}

// HasSelection returns true if anything is selected (fullscreen or windows).
func (SelectionManager) HasSelection(m *Model) bool {
	return m.appCore.HasSelection()
}

// IsSharing returns true if currently streaming.
func (SelectionManager) IsSharing(m *Model) bool {
	return m.appCore.IsSharing()
}

// CanAddWindow returns true if another window can be added (capacity check).
func (SelectionManager) CanAddWindow(m *Model) bool {
	return m.appCore.GetSelectedCount() < capture.MaxCaptureInstances
}

// --- Internal Helper Functions ---

// selectionPostChange handles stream updates after selection changes.
// If sharing: updates stream dynamically.
// If not sharing but has selection: starts sharing (Quick Share).
func selectionPostChange(m *Model) (tea.Model, tea.Cmd) {
	if m.appCore.IsSharing() && m.appCore.GetStreamer() != nil {
		// Already sharing - dynamically update
		return m.updateMultiStreamSelection()
	}

	// Not sharing - check if we should Quick Share
	if m.appCore.HasSelection() {
		log.Printf("Quick Share: Starting with %d windows, fullscreen=%v",
			m.appCore.GetSelectedCount(), m.appCore.IsFullscreenSelected())
		return m.startMultiWindowSharing()
	}

	return *m, nil
}
