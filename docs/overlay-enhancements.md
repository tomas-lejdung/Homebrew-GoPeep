# Overlay Enhancement Plan

This document outlines planned enhancements for the GoPeep floating overlay button. Each enhancement should be implemented and evaluated independently before moving to the next.

## Current State

The overlay is a floating button that appears on focused windows (excluding terminals) in manual mode. It shows:
- Status indicator (gray=not selected, blue=selected, red=sharing)
- Label text ("Share", "Selected", "Sharing")
- Arrow button to toggle between left/right corners

Location: `pkg/overlay/overlay_darwin.go`

---

## Enhancement 1: Smooth Corner Animation

### Description
When clicking the arrow to move the overlay between corners, animate the transition with a smooth slide instead of an instant jump.

### Technical Approach
- Use `NSAnimationContext` or `CABasicAnimation` to animate the `setFrame:` call
- Duration: ~200-300ms
- Easing: ease-in-out curve
- The animation should be interruptible (if user clicks arrow again mid-animation)

### Implementation Notes
```objc
[NSAnimationContext runAnimationGroup:^(NSAnimationContext *context) {
    context.duration = 0.25;
    context.timingFunction = [CAMediaTimingFunction functionWithName:kCAMediaTimingFunctionEaseInOut];
    [[g_overlayWindow animator] setFrame:newFrame display:YES];
} completionHandler:nil];
```

### Acceptance Criteria
- [x] Overlay slides smoothly between corners
- [x] Animation takes ~250ms
- [x] No visual glitches during animation
- [x] Clicking arrow during animation works correctly (g_isAnimating guard prevents re-entry)

---

## Enhancement 2: Viewer Count Badge

### Description
When actively sharing, show a small badge on the overlay indicating how many viewers are connected.

### Technical Approach
- Add a new `NSTextField` or `NSView` as a badge in the top-right corner of the overlay
- Badge should be circular with the count number
- Only visible when `state == STATE_SHARING` and `viewerCount > 0`
- Badge color: red or accent color to stand out

### Controller Changes
Add a new method to the overlay controller interface:
```go
type Controller interface {
    GetWindowState(windowID uint32) WindowState
    IsManualMode() bool
    GetViewerCount() int  // NEW
}
```

### Visual Design
- Small circular badge (16x16 or 18x18)
- Position: top-right corner of overlay, slightly overlapping
- Background: red (#FF3B30) or blue (#007AFF)
- Text: white, bold, small font (10-11pt)
- Show "9+" if more than 9 viewers

### Acceptance Criteria
- [ ] Badge appears when sharing with 1+ viewers
- [ ] Badge hidden when not sharing or 0 viewers
- [ ] Count updates in real-time as viewers join/leave
- [ ] Badge doesn't obscure main button functionality

**Note:** This enhancement was removed - deemed not necessary.

---

## Enhancement 3: State Change Effect

### Description
Add a subtle visual effect when the overlay state changes (e.g., when starting to share, selecting a window, etc.) to provide clear feedback.

### Technical Approach
Options (pick one):
1. **Pulse/scale animation**: Briefly scale up to 105% then back to 100%
2. **Flash/highlight**: Brief white overlay that fades out
3. **Border glow**: Colored border that pulses once
4. **Ripple effect**: Material-design style ripple from click point

Recommended: Pulse animation - simple and effective.

### Implementation Notes
```objc
static void pulseOverlay(void) {
    CAKeyframeAnimation *pulse = [CAKeyframeAnimation animationWithKeyPath:@"transform.scale"];
    pulse.values = @[@1.0, @1.08, @1.0];
    pulse.duration = 0.2;
    pulse.timingFunctions = @[
        [CAMediaTimingFunction functionWithName:kCAMediaTimingFunctionEaseOut],
        [CAMediaTimingFunction functionWithName:kCAMediaTimingFunctionEaseIn]
    ];
    [g_buttonView.layer addAnimation:pulse forKey:@"pulse"];
}
```

### Trigger Points
- When `goOverlayButtonClicked` is called (user clicked to toggle)
- When state changes from Selected to Sharing
- When state changes from Sharing to Selected

### Acceptance Criteria
- [x] Visual feedback occurs on state change
- [x] Effect is subtle, not distracting
- [x] Effect completes in <300ms (250ms)
- [x] Works correctly during rapid state changes

---

## Enhancement 4: Quick Share (Auto-Start on First Selection)

### Description
When no windows are selected and not sharing, clicking a window in the overlay should both select it AND automatically start sharing. This provides a one-click workflow.

### Current Behavior
1. Click overlay → window gets selected
2. User must go to TUI and press Enter to start sharing

### New Behavior
1. Click overlay when no windows selected → window gets selected AND sharing starts
2. Click overlay when already sharing → toggle window selection (existing behavior)

### Technical Approach
Add a new event type:
```go
const (
    EventToggleSelection EventType = iota
    EventQuickShare  // NEW: Select and start sharing
)
```

Or modify the existing event to include context:
```go
type Event struct {
    Type     EventType
    WindowID uint32
    IsFirstSelection bool  // NEW: true if this is first window being selected
}
```

### Controller Changes
Add method to check if any windows are selected:
```go
type Controller interface {
    // ... existing methods
    HasSelectedWindows() bool  // NEW
    IsSharing() bool           // NEW
}
```

### TUI Changes
In `handleOverlayToggle`:
- If `!m.sharing && len(m.selectedWindows) == 0`:
  - Select the window
  - Automatically trigger share start (same as pressing Enter)

### Acceptance Criteria
- [x] First window click starts sharing immediately
- [x] Subsequent clicks just toggle selection (existing behavior)
- [x] Works correctly if sharing is stopped and restarted
- [x] TUI state stays in sync

---

## Enhancement 5: Multi-Window Indicator

### Description
Show how many windows are currently selected/sharing, so users can see the state without looking at the TUI.

### Visual Design
Options:
1. **Text indicator**: Show "2/4" (2 selected, max 4) next to the status
2. **Dot indicators**: Show 1-4 small dots, filled for selected windows
3. **Badge**: Small count badge similar to viewer count

Recommended: Dot indicators - visual and compact.

### Implementation
Add 4 small dots (6x6) between the status indicator and the label:
```
[●] [○○●●] Selected  →
 ^    ^       ^       ^
 |    |       |       └── Arrow
 |    |       └── Label
 |    └── Window dots (4 slots, filled = selected)
 └── Status indicator
```

### Controller Changes
```go
type Controller interface {
    // ... existing methods
    GetSelectedWindowCount() int  // NEW
    GetMaxWindows() int           // NEW (returns 4)
}
```

### Acceptance Criteria
- [ ] Shows correct count of selected windows
- [ ] Updates when windows are added/removed
- [ ] Clear visual distinction between selected and empty slots
- [ ] Doesn't make overlay too wide

---

## Enhancement 6: Show Other Shared Windows

### Description
When sharing multiple windows, provide a way to see which other windows are being shared even when they're not in focus.

### Approach Options

#### Option A: Expandable List
- Add a small expand/collapse button
- When expanded, show a vertical list of all shared windows
- Each item shows: window icon/name + status
- Click to bring that window to focus (if possible)

#### Option B: Tooltip/Popover
- Hover over a "more" indicator to show a tooltip
- Tooltip lists all shared window names
- Less intrusive, read-only

#### Option C: Dot Row with Tooltips
- Show a row of colored dots for each shared window
- Hover over a dot to see window name
- Current/focused window has a ring around its dot

#### Option D: Carousel Arrows
- Show `< Window 1 of 3 >` 
- Click arrows to cycle through info about each shared window
- Shows which window is focused vs just shared

### Recommended: Option C (Dot Row)
- Compact and doesn't require expanding the overlay much
- Visual at-a-glance indication
- Tooltip provides details on hover

### Technical Approach
1. Add a row of small dots below the main button content
2. Each dot = one shared window (max 4)
3. Dot colors:
   - Gray outline = slot available
   - Blue filled = selected but not focused
   - Blue with white ring = currently focused
   - Red = sharing (all selected windows when sharing)
4. Hover over dot shows window name in tooltip

### Controller Changes
```go
type Controller interface {
    // ... existing methods
    GetSharedWindows() []SharedWindowInfo  // NEW
}

type SharedWindowInfo struct {
    WindowID   uint32
    WindowName string
    IsFocused  bool
}
```

### Acceptance Criteria
- [ ] All shared windows represented visually
- [ ] Clear indication of which window is focused
- [ ] Hover shows window name
- [ ] Updates when windows added/removed from sharing

---

## Implementation Order

Implement these enhancements in the following order, testing each thoroughly before proceeding:

1. **Enhancement 1: Smooth Corner Animation** - Polish, low risk
2. **Enhancement 3: State Change Effect** - Polish, low risk
3. **Enhancement 5: Multi-Window Indicator** - Useful feedback, medium complexity
4. **Enhancement 2: Viewer Count Badge** - Useful feedback, requires controller changes
5. **Enhancement 4: Quick Share** - Behavior change, requires careful testing
6. **Enhancement 6: Show Other Shared Windows** - Most complex, builds on #5

---

## Testing Notes

For each enhancement:
1. Test with 0, 1, 2, 3, and 4 windows selected
2. Test while sharing and while not sharing
3. Test rapid interactions (clicking quickly)
4. Test with overlay in both left and right positions
5. Verify TUI stays in sync with overlay state
6. Check for memory leaks or zombie processes on exit
