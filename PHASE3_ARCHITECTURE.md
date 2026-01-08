# Phase 3: Architecture Improvements

## Overview
Address architectural issues including state redundancy, potential race conditions, and error handling improvements.

**Risk Level:** Higher
**Requires careful testing**

---

## Tasks

### 1. Fix Potential Race Condition in BGRAFrame.Release()

#### 1.1 Use atomic swap for thread-safe release
- **Status:** [ ] Pending
- **File:** capture_multi_darwin.go, lines 864-870
- **Issue:** Two goroutines calling Release() simultaneously could race

**Current code:**
```go
func (f *BGRAFrame) Release() {
    if f.cData != nil && f.slot >= 0 {
        C.mc_release_frame_buffer(C.int(f.slot), (*C.uint8_t)(f.cData))
        f.cData = nil
        f.Data = nil
        f.slot = -1
    }
}
```

**Fixed code:**
```go
func (f *BGRAFrame) Release() {
    if f.slot < 0 {
        return
    }
    // Atomic swap to prevent double-release race
    ptr := atomic.SwapPointer((*unsafe.Pointer)(unsafe.Pointer(&f.cData)), nil)
    if ptr != nil {
        C.mc_release_frame_buffer(C.int(f.slot), (*C.uint8_t)(ptr))
        f.Data = nil
        f.slot = -1
    }
}
```

**Required import:** `sync/atomic`

---

### 2. Consolidate State Tracking (Optional - Higher Risk)

#### 2.1 Simplify PeerManager tracks/slots redundancy
- **Status:** [ ] Pending
- **Issue:** `tracks` map and `slots` array both store StreamTrackInfo
- **Option A:** Keep both but make `tracks` authoritative
- **Option B:** Remove `Info` from TrackSlot, only store TrackID

**Analysis needed:**
- Count usages of `slot.Info` vs `tracks[id]`
- Determine which is accessed more frequently
- Decide on single source of truth

---

### 3. Error Handling Improvements

#### 3.1 Add timeout-specific error codes in C capture code
- **Status:** [ ] Pending
- **File:** capture_multi_darwin.go C code
- **Issue:** Can't distinguish "window not found" from "timeout"

#### 3.2 Fix resource leak on capture error
- **Status:** [ ] Pending
- **File:** capture_multi_darwin.go, lines 249-252
- **Issue:** When addStreamOutput fails, stream/config/queue not cleaned up

#### 3.3 Add context to error log messages
- **Status:** [ ] Pending
- **Files:** multistream.go various locations
- **Issue:** Errors logged without trackID/peerID context

---

### 4. Consider Removing Old Single-Capture System (Optional)

#### 4.1 Audit capture_darwin.go usage
- **Status:** [ ] Pending
- **File:** capture_darwin.go
- **Check:** Which functions are still called from tui.go?

Known usages:
- `StopCapture()` called from tui.go:1801
- `IsCaptureActive()` called from tui.go:459

**If replaceable:** Could remove ~450 lines of duplicate C code

---

## Verification Steps

### For Race Condition Fix:
1. Build succeeds
2. Stress test: rapid window add/remove
3. Check for crashes or memory issues

### For State Consolidation:
1. Build succeeds
2. Test all peer connection scenarios
3. Test reconnection behavior
4. Test multi-viewer scenarios

### For Error Handling:
1. Build succeeds
2. Test error scenarios:
   - Window closed during capture
   - Invalid window ID
   - Permission denied

---

## Rollback Plan

Each change in this phase should be committed separately for easy rollback.
These are higher-risk changes that may require debugging.
