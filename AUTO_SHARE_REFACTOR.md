# Auto-Share Mode Refactor Plan

## Overview

Refactor auto-share mode to work exactly like normal mode, but with automatic window management based on OS focus instead of manual selection.

**Current Problem:** Auto-share uses a "swap capture" approach that is slow (1-2 seconds per swap) because it requires stopping one SCStream and starting another.

**Solution:** Reuse the existing multi-stream infrastructure (up to 4 simultaneous captures) and automatically add/remove windows based on focus. Switching between already-streaming windows is instant.

---

## Implementation Steps

### Step 1: Add Helper Methods to Streamer

**File:** `multistream.go`

Add these methods to check streaming state:

```go
func (ms *Streamer) IsWindowStreaming(windowID uint32) bool
func (ms *Streamer) GetActiveStreamCount() int
func (ms *Streamer) GetStreamingWindowIDs() []uint32
```

---

### Step 2: Update Model Fields

**File:** `tui.go`

**Remove:**
- `autoShareWindowID uint32`
- `autoShareWindowName string`

**Add:**
- `autoShareFocusTimes map[uint32]time.Time` - Track last focus time per window for LRU eviction

---

### Step 3: Add LRU Helper Function

**File:** `tui.go`

```go
func (m model) getLRUWindow() uint32
```

Returns the window ID that was focused longest ago (for eviction when pool is full).

---

### Step 4: Rewrite fastTickMsg Handler

**File:** `tui.go`

Replace the swap-based logic with add/remove logic:

**Old flow:**
1. Detect focus change
2. Call `SwapWindowCapture()` (slow - stops and starts capture)

**New flow:**
1. Detect focus change
2. Update `autoShareFocusTimes[windowID]`
3. If window already streaming → do nothing (instant, focus detection loop handles it)
4. If window not streaming:
   - If pool full (4 windows) → remove LRU window via `RemoveWindowDynamic()`
   - Add new window via `AddWindowDynamic()`
   - Update `selectedWindows` map

---

### Step 5: Update renderSourcesList

**File:** `tui.go`

Change auto-share mode to show the same UI as normal mode (with checkboxes showing all streaming windows), but with an "AUTO-SHARE" badge.

**Old:** Shows simplified single-window view
**New:** Shows full window list with checkboxes (same as normal mode)

---

### Step 6: Update toggleAutoShareMode

**File:** `tui.go`

**When enabling:**
- Initialize `autoShareFocusTimes`
- If already sharing, keep existing windows and just enable auto-management
- If not sharing, start with the focused window

**When disabling:**
- Keep windows streaming (don't stop)
- Clear `autoShareFocusTimes`
- User can now manually manage windows

---

### Step 7: Remove Unused Swap Code

**File:** `multistream.go`

Remove:
- `SwapWindowCapture()` function (~80 lines)

**File:** `capture_multi_darwin.go`

Remove C functions:
- `mc_swap_window_in_slot()` (~60 lines)
- `mc_swap_window_cached()` (~70 lines)
- `mc_start_content_cache()` (~50 lines)
- `mc_stop_content_cache()` (~15 lines)
- `mc_is_content_cache_ready()` (~5 lines)
- `mc_set_cached_content()` helper (~15 lines)
- Content cache globals (~10 lines)

Remove Go wrappers:
- `SwapWindowInSlot()`
- `SwapWindowCached()`
- `StartContentCache()`
- `StopContentCache()`
- `IsContentCacheReady()`

**File:** `tui.go`

Remove:
- Calls to `StartContentCache()` and `StopContentCache()`

---

### Step 8: Keep Focus Observer

The NSWorkspace focus observer (`StartFocusObserver`, `StopFocusObserver`, `CheckFocusChanged`) is still useful for faster focus detection. Keep it.

---

## Code to Review for Removal/Unification

After implementation, review these for potential cleanup:

### Potentially Unused After Refactor

1. **`mc_swap_window_in_slot`** - C function for swap (REMOVE)
2. **`mc_swap_window_cached`** - C function for cached swap (REMOVE)
3. **Content cache system** - All `mc_*_content_cache` functions (REMOVE)
4. **`SwapWindowCapture`** - Go function (REMOVE)
5. **`SwapWindowInSlot`** - Go wrapper (REMOVE)
6. **`SwapWindowCached`** - Go wrapper (REMOVE)

### Potentially Duplicated Logic

1. **Focus detection** - `focusDetectionLoop` in multistream.go vs `fastTickMsg` in tui.go
   - Both detect topmost window
   - Consider if they can share logic or if both are needed

2. **Window listing** - Multiple places call `ListWindows()` or iterate `m.sources`
   - Check if any can be consolidated

3. **LRU tracking** - New `autoShareFocusTimes` in tui.go
   - Could potentially move to Streamer if needed elsewhere

### Functions to Keep

1. **`AddWindowDynamic`** - Used by both normal and auto-share mode
2. **`RemoveWindowDynamic`** - Used by both normal and auto-share mode
3. **`StartFocusObserver`** / `StopFocusObserver`** - Useful for fast detection
4. **`CheckFocusChanged`** - Used by fastTickMsg
5. **`GetTopmostWindow`** - Used for z-order detection

---

## Expected Outcome

| Scenario | Before | After |
|----------|--------|-------|
| Switch to already-streaming window | 1-2 seconds (swap) | **INSTANT** |
| Switch to new window (pool not full) | 1-2 seconds (swap) | ~300-500ms (add) |
| Switch to new window (pool full) | 1-2 seconds (swap) | ~500-800ms (remove + add) |
| Host UI in auto-share | Simplified single view | Same as normal mode |
| Viewer experience | Single stream, swaps | Multiple streams (like normal) |
| Toggle auto-share off | Stops streaming | Keeps windows, manual mode |

---

## Files Modified

| File | Lines Added | Lines Removed | Net |
|------|-------------|---------------|-----|
| `multistream.go` | ~30 | ~80 | -50 |
| `tui.go` | ~60 | ~30 | +30 |
| `capture_multi_darwin.go` | 0 | ~225 | -225 |
| **Total** | ~90 | ~335 | **-245** |

---

## Implementation Order

1. Add helper methods (Step 1)
2. Update model fields (Step 2)
3. Add LRU helper (Step 3)
4. Rewrite fastTickMsg (Step 4)
5. Update UI rendering (Step 5)
6. Update toggle function (Step 6)
7. Remove unused code (Step 7)
8. Test thoroughly
9. Final cleanup review
