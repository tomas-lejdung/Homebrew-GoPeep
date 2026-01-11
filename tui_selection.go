package main

import (
	"log"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

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

// selectionTrackFocusTime updates the focus time for LRU tracking.
func selectionTrackFocusTime(m *model, windowID uint32) {
	if m.autoShareFocusTimes == nil {
		m.autoShareFocusTimes = make(map[uint32]time.Time)
	}
	m.autoShareFocusTimes[windowID] = time.Now()
}
