package main

import (
	"github.com/tomaslejdung/gopeep/pkg/overlay"
)

// OverlayController implements overlay.Controller interface.
// It queries AppCore directly for state, avoiding state mirroring.
type OverlayController struct {
	appCore *AppCore
}

// NewOverlayController creates a new overlay controller that queries AppCore.
func NewOverlayController(appCore *AppCore) *OverlayController {
	return &OverlayController{
		appCore: appCore,
	}
}

// GetWindowState implements overlay.Controller.
func (c *OverlayController) GetWindowState(windowID uint32) overlay.WindowState {
	if !c.appCore.IsWindowSelected(windowID) {
		return overlay.StateNotSelected
	}

	if c.appCore.IsSharing() {
		return overlay.StateSharing
	}

	return overlay.StateSelected
}

// IsManualMode implements overlay.Controller.
func (c *OverlayController) IsManualMode() bool {
	return !c.appCore.IsAutoShareEnabled()
}

// GetFocusedWindow implements overlay.Controller.
// It uses the existing focus detection (GetFocusedWindow from capture_multi_darwin.go)
func (c *OverlayController) GetFocusedWindow() *overlay.FocusedWindowInfo {
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
	return c.appCore.GetSelectedCount()
}

// IsSharing implements overlay.Controller.
func (c *OverlayController) IsSharing() bool {
	return c.appCore.IsSharing()
}

// GetViewerCount implements overlay.Controller.
func (c *OverlayController) GetViewerCount() int {
	return c.appCore.GetViewerCount()
}

// IsFullscreenSelected implements overlay.Controller.
func (c *OverlayController) IsFullscreenSelected() bool {
	return c.appCore.IsFullscreenSelected()
}
