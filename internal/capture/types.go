package capture

import (
	"sync/atomic"
	"unsafe"
)

// BGRAFrame holds raw BGRA frame data from screen capture
type BGRAFrame struct {
	Data   []byte
	Width  int
	Height int
	Stride int
	// Zero-copy support fields
	CData unsafe.Pointer // Original C pointer (nil if Go-owned copy)
	Slot  int            // Capture slot for release (-1 if Go-owned)
	// Release function (set by capture code for zero-copy frames)
	releaseFunc func()
}

// Release returns the frame buffer to the capture pool.
// Must be called when done with zero-copy frames from GetLatestFrameBGRA.
// Safe to call multiple times or on Go-owned frames (no-op).
// Thread-safe: uses atomic swap to prevent double-release race conditions.
func (f *BGRAFrame) Release() {
	if f.releaseFunc != nil {
		f.releaseFunc()
	}
}

// SetReleaseFunc sets the release callback function for zero-copy frames
func (f *BGRAFrame) SetReleaseFunc(fn func()) {
	f.releaseFunc = fn
}

// AtomicSwapCData atomically swaps the CData pointer, returning the old value.
// This is used for thread-safe release of zero-copy frames.
func (f *BGRAFrame) AtomicSwapCData() unsafe.Pointer {
	return atomic.SwapPointer((*unsafe.Pointer)(unsafe.Pointer(&f.CData)), nil)
}

// WindowInfo represents information about a window
type WindowInfo struct {
	ID         uint32
	OwnerName  string // Application name
	WindowName string // Window title
	X, Y       int32
	Width      int32
	Height     int32
	OnScreen   bool
}

// WindowBounds represents the position and size of a window on screen
type WindowBounds struct {
	X, Y          float64 // Position (screen coordinates, origin top-left)
	Width, Height float64 // Size
}

// FocusedWindowInfo contains information about the OS-focused window
type FocusedWindowInfo struct {
	WindowID uint32
	Bounds   WindowBounds
}

// CursorPosition holds cursor coordinates relative to a window
type CursorPosition struct {
	X            float64 // Cursor X in window coordinates (-1 if outside window)
	Y            float64 // Cursor Y in window coordinates (-1 if outside window)
	WindowWidth  float64 // Window content width
	WindowHeight float64 // Window content height
	InWindow     bool    // Whether cursor is inside the window
}

// MaxCaptureInstances is the maximum number of concurrent captures
const MaxCaptureInstances = 4

// CaptureInstance represents a single window capture session
type CaptureInstance struct {
	Slot     int
	WindowID uint32
	Active   bool
}
