# Phase 1: Safe Cleanup of Unused Code

## Status: COMPLETED

**Risk Level:** Low
**Lines Removed:** ~380 lines

---

## Summary

Removed clearly unused functions and dead code left over from the old swap-based auto-share approach.

### Files Modified:
| File | Before | After | Removed |
|------|--------|-------|---------|
| capture_multi_darwin.go | 1275 | 1024 | 251 lines |
| capture_darwin.go | 689 | 559 | 130 lines |
| multistream.go | ~2815 | 2755 | ~60 lines |

---

## Completed Tasks

### 1. capture_multi_darwin.go

- [x] **1.1** Removed C function `mc_start_window_capture_in_slot()` (~90 lines)
- [x] **1.2** Removed Go wrapper `StartWindowCaptureInSlot()` (~45 lines)
- [x] **1.3** Removed C function `mc_free_frame()` (~7 lines)
- [x] **1.4** Removed C function `mc_get_focused_window_id()` (~4 lines)
- [x] **1.5** Removed unused Go helpers:
  - `GetActiveInstances()`
  - `GetActiveCount()`
  - `IsCapturing()`
  - `GetInstanceByWindowID()`
- [x] **1.6** Removed `GetFocusedWindowID()` Go wrapper

### 2. multistream.go

- [x] **2.1** Removed `UpdateTrackInfo()` (~24 lines)
- [x] **2.2** Removed `GetActiveSlotCount()` (~12 lines)
- [x] **2.3** Removed `SetFocusedTrack()` (~9 lines)
- [x] **2.4** Removed `ReplaceTrackOnAllPeers()` (~30 lines)

### 3. capture_darwin.go

- [x] **3.1** Removed `StartWindowCapture()` (~17 lines)
- [x] **3.2** Removed `StartDisplayCapture()` (~17 lines)
- [x] **3.3** Removed `GetLatestFrame()` (~11 lines) - deprecated
- [x] **3.4** Removed `frameDataToImage()` (~31 lines)
- [x] **3.5** Removed `frameDataToBGRA()` (~19 lines)
- [x] **3.6** Removed `GetLatestFrameBGRA()` (capture_darwin.go version) (~10 lines)
- [x] **3.7** Removed `FindWindowByName()` (~15 lines)
- [x] Removed unused imports (`image`, `time`)

---

## What Was Kept (Still Used)

### capture_darwin.go:
- `StopCapture()` - called from tui.go:1801 (legacy, harmless no-op)
- `IsCaptureActive()` - called from tui.go:459 (legacy, always returns false)
- `BGRAFrame` type - used throughout codebase
- `HasScreenRecordingPermission()` - called from tui.go:2468
- `ListWindows()` - core functionality
- All C code for single-capture (kept for now, could remove in Phase 3)

### capture_multi_darwin.go:
- All actively used multi-capture functions
- Focus observer functions
- `GetFrontmostWindowID()`, `GetTopmostWindow()` - used for focus detection

---

## Verification

- [x] Build succeeds: `go build -o gopeep .`
- [ ] Manual testing required by user

## Next Steps

After user testing confirms everything works, proceed to Phase 2 (Deduplication).
