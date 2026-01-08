# Phase 3: Architecture Improvements

## Status: COMPLETED

**Risk Level:** Higher
**Changes:** Thread safety fix, legacy code removal, resource leak fixes

---

## Completed Tasks

### 1. Fix BGRAFrame.Release() Race Condition
- **File:** `capture_multi_darwin.go`
- **Issue:** Two goroutines calling Release() simultaneously could cause double-free
- **Fix:** Used `atomic.SwapPointer` for thread-safe release
- **Added:** `sync/atomic` import

```go
// Before (race condition):
func (f *BGRAFrame) Release() {
    if f.cData != nil && f.slot >= 0 {
        C.mc_release_frame_buffer(...)
        f.cData = nil
    }
}

// After (thread-safe):
func (f *BGRAFrame) Release() {
    if f.slot < 0 {
        return
    }
    ptr := atomic.SwapPointer((*unsafe.Pointer)(unsafe.Pointer(&f.cData)), nil)
    if ptr != nil {
        C.mc_release_frame_buffer(...)
    }
}
```

### 2. Remove Legacy Single-Capture Usage
- **File:** `tui.go`
- **Removed:** `IsCaptureActive()` check (was always false with multistream)
- **Removed:** `StopCapture()` call (redundant after `m.streamer.Stop()`)

### 3. Remove Legacy Functions
- **File:** `capture_darwin.go`
- **Removed:** `StopCapture()` - was never used with multistream
- **Removed:** `IsCaptureActive()` - was never used with multistream

### 4. Fix Resource Leaks on Capture Error
- **File:** `capture_multi_darwin.go` (C code)
- **Fixed:** `mc_start_window_capture()` - cleanup on addStreamOutput failure
- **Fixed:** `mc_start_window_capture()` - cleanup on startCapture failure
- **Fixed:** `mc_start_display_capture()` - cleanup on addStreamOutput failure
- **Fixed:** `mc_start_display_capture()` - cleanup on startCapture failure

Resources now properly cleaned up:
- `inst->stream = nil`
- `inst->config = nil`
- `inst->output_delegate = nil`
- `inst->queue = nil`

---

## Skipped Tasks

### Timeout-Specific Error Codes
- **Status:** Skipped (low priority)
- **Reason:** Would require more invasive C code changes
- **Current behavior:** Timeout and "not found" both return -2
- **Future improvement:** Could add -6 for timeout errors

---

## Verification

- [x] Build succeeds: `go build -o gopeep .`
- [ ] Manual testing required by user

---

## Summary of All Phases

| Phase | Focus | Lines Changed | Risk |
|-------|-------|---------------|------|
| Phase 1 | Dead code removal | ~380 lines removed | Low |
| Phase 2 | Code deduplication | ~200 lines consolidated | Medium |
| Phase 3 | Architecture fixes | Thread safety, leak fixes | Higher |

**Total improvement:** ~600+ lines of code improved/removed
