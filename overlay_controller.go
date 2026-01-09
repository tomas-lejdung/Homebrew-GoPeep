package main

import (
	"sync"

	"github.com/tomaslejdung/gopeep/pkg/overlay"
)

// OverlayController implements overlay.Controller interface.
// It provides a thread-safe way to query the TUI model state from the overlay.
type OverlayController struct {
	mu sync.RWMutex

	// State mirrors from the TUI model (updated via Sync method)
	selectedWindows map[uint32]bool
	sharing         bool
	autoShareMode   bool
	viewerCount     int
}

// NewOverlayController creates a new overlay controller.
func NewOverlayController() *OverlayController {
	return &OverlayController{
		selectedWindows: make(map[uint32]bool),
	}
}

// Sync updates the controller state from the TUI model.
// Call this whenever the TUI state changes.
func (c *OverlayController) Sync(selectedWindows map[uint32]bool, sharing bool, autoShareMode bool, viewerCount int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Copy the map to avoid race conditions
	c.selectedWindows = make(map[uint32]bool)
	for k, v := range selectedWindows {
		c.selectedWindows[k] = v
	}
	c.sharing = sharing
	c.autoShareMode = autoShareMode
	c.viewerCount = viewerCount
}

// GetWindowState implements overlay.Controller.
func (c *OverlayController) GetWindowState(windowID uint32) overlay.WindowState {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if !c.selectedWindows[windowID] {
		return overlay.StateNotSelected
	}

	if c.sharing {
		return overlay.StateSharing
	}

	return overlay.StateSelected
}

// IsManualMode implements overlay.Controller.
func (c *OverlayController) IsManualMode() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	return !c.autoShareMode
}

// GetFocusedWindow implements overlay.Controller.
// It uses the same focus detection as the TUI (GetFocusedWindow from capture_multi_darwin.go)
func (c *OverlayController) GetFocusedWindow() *overlay.FocusedWindowInfo {
	// Call the existing focus detection (no lock needed, it's independent)
	info := GetFocusedWindow()
	if info == nil {
		return nil
	}

	return &overlay.FocusedWindowInfo{
		WindowID: info.WindowID,
		X:        info.Bounds.X,
		Y:        info.Bounds.Y,
		Width:    info.Bounds.Width,
		Height:   info.Bounds.Height,
	}
}

// GetSelectedWindowCount implements overlay.Controller.
func (c *OverlayController) GetSelectedWindowCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.selectedWindows)
}

// IsSharing implements overlay.Controller.
func (c *OverlayController) IsSharing() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sharing
}

// GetViewerCount implements overlay.Controller.
func (c *OverlayController) GetViewerCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.viewerCount
}
