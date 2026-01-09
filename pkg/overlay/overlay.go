// Package overlay provides a floating overlay button for window selection.
// The overlay runs its own 60fps game loop, completely independent from the TUI.
// It communicates with the main application only through the Controller interface.
package overlay

import "sync/atomic"

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
	// EventToggleFullscreen - user clicked the fullscreen button
	EventToggleFullscreen
)

// FocusedWindowInfo contains information about the currently focused window
type FocusedWindowInfo struct {
	WindowID uint32
	X, Y     float64 // Screen position (origin top-left)
	Width    float64
	Height   float64
}

// Controller interface is implemented by the main application (TUI).
// The overlay queries this interface at 60fps to get current state.
// Implementation must be thread-safe.
type Controller interface {
	// GetWindowState returns the current state of a window.
	GetWindowState(windowID uint32) WindowState

	// IsManualMode returns true if in manual mode (overlay should be visible).
	// Returns false in auto mode (overlay should be hidden).
	IsManualMode() bool

	// GetFocusedWindow returns the currently focused window (excluding terminals).
	// Returns nil if no valid focused window or a terminal is focused.
	GetFocusedWindow() *FocusedWindowInfo

	// GetSelectedWindowCount returns the number of currently selected windows.
	GetSelectedWindowCount() int

	// IsSharing returns true if currently sharing windows.
	IsSharing() bool

	// GetViewerCount returns the number of connected viewers.
	GetViewerCount() int

	// IsFullscreenSelected returns true if fullscreen mode is selected.
	IsFullscreenSelected() bool
}

// Overlay manages the floating button that appears on focused windows.
// It runs an independent 60fps game loop for smooth animations.
// Safe for concurrent use.
type Overlay struct {
	controller Controller
	enabled    bool
	started    bool
	events     chan Event

	// Game loop state
	running atomic.Bool   // thread-safe flag for loop control
	ready   chan struct{} // signals overlay is created and ready
	stopped chan struct{} // signals game loop has exited
}

// New creates a new Overlay with the given controller.
// The overlay must be started with Start() to begin the game loop.
func New(ctrl Controller) *Overlay {
	return &Overlay{
		controller: ctrl,
		enabled:    true,
		events:     make(chan Event, 16),
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
		// Channel full, drop event
	}
}

// Start initializes the overlay and starts the 60fps game loop.
// On macOS, this creates the floating NSWindow.
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

// Stop stops the game loop and cleans up resources.
// This closes the Events() channel, causing any goroutine reading from it to exit.
func (o *Overlay) Stop() {
	if !o.started {
		return
	}
	o.platformStop()
	close(o.events) // Close channel to unblock any goroutine reading from Events()
	o.started = false
}

// SetEnabled enables or disables the overlay visibility.
// When disabled, the overlay is hidden but the game loop continues.
func (o *Overlay) SetEnabled(enabled bool) {
	o.enabled = enabled
	o.platformSetEnabled(enabled)
}

// IsEnabled returns whether the overlay is currently enabled.
func (o *Overlay) IsEnabled() bool {
	return o.enabled
}
