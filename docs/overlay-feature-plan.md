# Overlay Feature Implementation Plan

## Overview

Add a floating overlay button that appears on the focused window in manual mode, allowing users to add/remove windows from the sharing selection without using the TUI.

## Requirements

### Functional
- Floating button positioned at bottom-left corner of focused window (16px margin)
- Only visible in **manual mode** (hidden in auto mode)
- Shows current state of the focused window:
  - Not selected: "Share" button
  - Selected but not sharing: "Selected" indicator
  - Selected and sharing: "Sharing" indicator (with stop action)
- Click toggles window selection (same as pressing space in TUI)
- If max windows (4) already selected and user clicks on 5th window, automatically deselect the oldest selection (FIFO)
- Hover effect on button
- No exclusions (overlay appears on any window including gopeep terminal)

### Non-Functional
- **Completely separate** from existing code - own package
- Clean interface for communication with main app
- macOS only (with stub for other platforms)
- Minimal, modern design with semi-transparent background

## Architecture

### Package Structure

```
pkg/overlay/
├── overlay.go           # Public Go API and types
├── overlay_darwin.go    # macOS CGO implementation  
├── overlay_darwin.h     # Objective-C header declarations
├── overlay_darwin.m     # Objective-C implementation
└── overlay_stub.go      # No-op for non-macOS (build tag)
```

### Interface Design

```go
// pkg/overlay/overlay.go

package overlay

// WindowState represents the state of a window in the overlay
type WindowState int

const (
    StateNotSelected WindowState = iota
    StateSelected
    StateSharing
)

// Controller interface - implemented by the main app (tui.go)
// This is the ONLY coupling point with the rest of the codebase
type Controller interface {
    // ToggleWindowSelection toggles selection for a window
    // If maxed out (4 windows), it should deselect oldest first
    ToggleWindowSelection(windowID uint32)
    
    // GetWindowState returns the current state of a window
    GetWindowState(windowID uint32) WindowState
    
    // GetFocusedWindowID returns the currently focused window's ID
    // Returns 0 if no valid window is focused
    GetFocusedWindowID() uint32
}

// Overlay manages the floating button
type Overlay struct {
    controller Controller
    enabled    bool
    started    bool
}

// New creates a new Overlay with the given controller
func New(ctrl Controller) *Overlay

// Start initializes and shows the overlay system
func (o *Overlay) Start() error

// Stop cleans up and hides the overlay
func (o *Overlay) Stop()

// SetEnabled enables/disables the overlay (for manual/auto mode switching)
func (o *Overlay) SetEnabled(enabled bool)

// Refresh updates the overlay button state
// Call this after selection changes in the TUI
func (o *Overlay) Refresh()
```

### Data Flow

```
┌─────────────────────────────────────────────────────────────────┐
│                         Main App (TUI)                          │
│                                                                 │
│  ┌─────────────┐    implements    ┌─────────────────────────┐  │
│  │   model     │ ───────────────► │ overlay.Controller      │  │
│  │             │                  │                         │  │
│  │ - sharing   │                  │ - ToggleWindowSelection │  │
│  │ - selected  │                  │ - GetWindowState        │  │
│  │ - windows   │                  │ - GetFocusedWindowID    │  │
│  └─────────────┘                  └─────────────────────────┘  │
│         │                                    ▲                  │
│         │ state changes                      │                  │
│         ▼                                    │                  │
│  ┌─────────────┐                  ┌─────────────────────────┐  │
│  │  Refresh()  │ ────────────────►│    pkg/overlay          │  │
│  └─────────────┘                  │                         │  │
│                                   │  - NSPanel (floating)   │  │
│                                   │  - Focus tracking       │  │
│                                   │  - Button rendering     │  │
│                                   └─────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
```

### Communication Pattern

1. **App → Overlay**: 
   - `SetEnabled(true/false)` when switching manual/auto mode
   - `Refresh()` after any selection change

2. **Overlay → App** (via Controller):
   - `ToggleWindowSelection(windowID)` when button clicked
   - `GetWindowState(windowID)` to determine button appearance
   - `GetFocusedWindowID()` to know which window to track

3. **Internal Overlay**:
   - NSWorkspace notifications for focus changes
   - Timer-based position updates (window might move)
   - CGWindowListCopyWindowInfo for window geometry

## Objective-C Implementation Details

### NSPanel Configuration

```objc
NSPanel *panel = [[NSPanel alloc] initWithContentRect:frame
    styleMask:NSWindowStyleMaskBorderless | NSWindowStyleMaskNonactivatingPanel
    backing:NSBackingStoreBuffered
    defer:NO];

panel.level = NSFloatingWindowLevel;
panel.backgroundColor = [NSColor clearColor];
panel.opaque = NO;
panel.hasShadow = YES;
panel.ignoresMouseEvents = NO;
panel.collectionBehavior = NSWindowCollectionBehaviorCanJoinAllSpaces |
                           NSWindowCollectionBehaviorStationary |
                           NSWindowCollectionBehaviorIgnoresCycle;
```

### Focus Tracking

```objc
// Subscribe to focus changes
[[[NSWorkspace sharedWorkspace] notificationCenter]
    addObserverForName:NSWorkspaceDidActivateApplicationNotification
    object:nil
    queue:[NSOperationQueue mainQueue]
    usingBlock:^(NSNotification *note) {
        // Update overlay position and state
        updateOverlayPosition();
    }];
```

### Window Position Tracking

```objc
// Get focused window bounds
CFArrayRef windowList = CGWindowListCopyWindowInfo(
    kCGWindowListOptionOnScreenOnly | kCGWindowListExcludeDesktopElements,
    kCGNullWindowID
);

// Find the frontmost window and get its bounds
// Position overlay at bottom-left + 16px margin
```

### Button View

```objc
@interface GoPeepOverlayButton : NSView
@property (nonatomic) GoPeepWindowState state;
@property (nonatomic) BOOL isHovered;
@property (nonatomic, copy) void (^onClick)(void);
@end

// Drawing:
// - Rounded rect background with vibrancy/blur
// - State indicator (circle/square)
// - Text label
// - Hover effect (slightly brighter)
```

## Visual Design

### Button Appearance

```
Size: 100w x 32h points
Corner radius: 8px
Margin from window edge: 16px

Background: 
  - Normal: rgba(40, 40, 40, 0.75) with blur
  - Hover: rgba(60, 60, 60, 0.85)

States:
┌────────────────┐
│  ○  Share      │  Not selected - muted text, hollow circle
└────────────────┘

┌────────────────┐
│  ●  Selected   │  Selected - white text, filled circle (blue)
└────────────────┘

┌────────────────┐
│  ■  Sharing    │  Sharing - white text, filled square (red)
└────────────────┘

Font: System font, 13pt, medium weight
Icon size: 8x8 points
Spacing: 8px between icon and text
Padding: 12px horizontal, 8px vertical
```

### Color Palette

```
Background:        #282828 @ 75% opacity
Background hover:  #3C3C3C @ 85% opacity
Text muted:        #888888
Text active:       #FFFFFF
Accent blue:       #007AFF (selected indicator)
Accent red:        #FF3B30 (sharing indicator)
```

## Implementation Steps

### Phase 1: Package Setup
1. Create `pkg/overlay/` directory
2. Create `overlay.go` with interface and types
3. Create `overlay_stub.go` for non-macOS builds
4. Create empty `overlay_darwin.go` skeleton

### Phase 2: Objective-C Foundation
1. Create NSPanel setup code
2. Implement focus tracking with NSWorkspace
3. Implement window position tracking with CGWindowList
4. Test basic floating panel appears and follows focus

### Phase 3: Button View
1. Create custom NSView for the button
2. Implement drawing for all states
3. Add hover tracking (NSTrackingArea)
4. Add click handling

### Phase 4: Go-ObjC Bridge
1. Export Go callbacks for ObjC to call
2. Implement CGO functions for ObjC calls
3. Wire up button click → Go callback → Controller

### Phase 5: Integration
1. Implement Controller interface on TUI model
2. Add overlay initialization to main.go
3. Wire up SetEnabled for manual/auto mode
4. Wire up Refresh calls after selection changes

### Phase 6: Polish
1. Add smooth animations (fade in/out)
2. Fine-tune positioning and timing
3. Handle edge cases (window at screen edge, etc.)
4. Test with multiple monitors

## Testing Plan

### Manual Testing
1. Start gopeep in manual mode → overlay appears on focused window
2. Switch to auto mode → overlay disappears
3. Focus different windows → overlay follows
4. Click Share → window added to selection (verify in TUI)
5. Click Selected → window removed from selection
6. Share 4 windows, click 5th → oldest deselected, new one selected
7. Move window → overlay stays in correct position
8. Hover over button → hover effect visible

### Edge Cases
- Window at bottom-left of screen (overlay might clip)
- Very small windows
- Fullscreen windows
- Multiple monitors
- gopeep terminal window itself

## File Changes Summary

### New Files
- `pkg/overlay/overlay.go` - Public API
- `pkg/overlay/overlay_darwin.go` - macOS CGO implementation
- `pkg/overlay/overlay_darwin.h` - ObjC header
- `pkg/overlay/overlay_darwin.m` - ObjC implementation
- `pkg/overlay/overlay_stub.go` - Non-macOS stub

### Modified Files
- `main.go` - Initialize overlay
- `tui.go` - Implement Controller interface, wire up refresh calls

## Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| AppKit runloop conflicts with Go | Use dispatch_async to main queue for all UI |
| Memory leaks in ObjC | Use @autoreleasepool, careful CFRelease |
| Focus detection unreliable | Fallback timer-based polling (every 100ms) |
| Performance impact | Lazy updates, debounce position recalcs |
| Overlay steals focus | NSNonactivatingPanel prevents this |

## Future Enhancements (Out of Scope)
- Keyboard shortcut to toggle selection (global hotkey)
- Drag to reposition overlay
- Mini viewer count badge
- Multiple overlay positions (user preference)
