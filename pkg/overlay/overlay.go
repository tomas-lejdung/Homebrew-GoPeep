// Package overlay provides a floating overlay button for window selection.
// This package is designed to be completely independent from the main TUI,
// communicating only through the Controller interface.
package overlay

// WindowState represents the selection/sharing state of a window
type WindowState int

const (
	// StateNotSelected - window is not in the selection
	StateNotSelected WindowState = iota
	// StateSelected - window is selected but not currently sharing
	StateSelected
	// StateSharing - window is selected and actively sharing
	StateSharing
)

// Event represents an event from the overlay to the main application.
type Event struct {
	// Type of event
	Type EventType
	// WindowID is the window that was acted upon
	WindowID uint32
}

// EventType represents the type of overlay event.
type EventType int

const (
	// EventToggleSelection - user clicked the overlay button to toggle selection
	EventToggleSelection EventType = iota
)

// Controller interface is implemented by the main application (TUI).
// This is the ONLY coupling point between the overlay and the rest of the codebase.
type Controller interface {
	// GetWindowState returns the current state of a window.
	GetWindowState(windowID uint32) WindowState

	// IsManualMode returns true if in manual mode (overlay should be visible).
	// Returns false in auto mode (overlay should be hidden).
	IsManualMode() bool
}

// Overlay manages the floating button that appears on focused windows.
// It is safe for concurrent use.
type Overlay struct {
	controller Controller
	enabled    bool
	started    bool
	events     chan Event
}

// New creates a new Overlay with the given controller.
// The overlay starts disabled and must be started with Start().
func New(ctrl Controller) *Overlay {
	return &Overlay{
		controller: ctrl,
		enabled:    true,
		events:     make(chan Event, 16), // Buffered channel for events
	}
}

// Events returns a channel that receives overlay events.
// The main application should read from this channel and handle events.
func (o *Overlay) Events() <-chan Event {
	return o.events
}

// sendEvent sends an event to the events channel (non-blocking).
func (o *Overlay) sendEvent(evt Event) {
	select {
	case o.events <- evt:
	default:
		// Channel full, drop event (shouldn't happen with buffered channel)
	}
}

// Start initializes the overlay system and begins tracking window focus.
// On macOS, this creates the floating NSPanel.
// Returns an error if initialization fails.
func (o *Overlay) Start() error {
	if o.started {
		return nil
	}
	err := o.platformStart()
	if err != nil {
		return err
	}
	o.started = true
	return nil
}

// Stop cleans up the overlay and releases resources.
func (o *Overlay) Stop() {
	if !o.started {
		return
	}
	o.platformStop()
	o.started = false
}

// SetEnabled enables or disables the overlay visibility.
// When disabled, the overlay is hidden but tracking continues.
// Use this to hide the overlay in auto mode.
func (o *Overlay) SetEnabled(enabled bool) {
	o.enabled = enabled
	o.platformSetEnabled(enabled)
}

// Refresh updates the overlay button state.
// Call this after selection changes in the TUI to update the button appearance.
// Deprecated: Use RefreshWithFocus instead for proper positioning.
func (o *Overlay) Refresh() {
	if !o.started {
		return
	}
	o.platformRefresh(0, 0, 0, 0, 0)
}

// FocusedWindow contains information about the currently focused window
type FocusedWindow struct {
	WindowID uint32
	X, Y     float64 // Screen position (origin top-left)
	Width    float64
	Height   float64
}

// RefreshWithFocus updates the overlay button state and position.
// Pass the focused window information from the TUI for accurate positioning.
// If focus is nil, the overlay will be hidden.
func (o *Overlay) RefreshWithFocus(focus *FocusedWindow) {
	if !o.started {
		return
	}
	if focus == nil {
		o.platformRefresh(0, 0, 0, 0, 0)
	} else {
		o.platformRefresh(focus.WindowID, focus.X, focus.Y, focus.Width, focus.Height)
	}
}

// IsEnabled returns whether the overlay is currently enabled.
func (o *Overlay) IsEnabled() bool {
	return o.enabled
}
