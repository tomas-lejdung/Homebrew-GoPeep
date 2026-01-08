# Zero-Copy Capture Path Implementation

## Goal
Reduce frame copies from 3 to 1 per frame at 4K, saving ~1.9 GB/s of memory bandwidth.

## Current vs Target Architecture

### Before (3 copies per frame)
```
ScreenCaptureKit callback
    |
    v COPY 1 (32MB): CVPixelBuffer -> malloc'd buffer
inst->latest_frame.data
    |
    v COPY 2 (32MB): malloc + memcpy to return buffer
mc_get_latest_frame() returns
    |
    v COPY 3 (32MB): make([]byte) + copy() in Go
BGRAFrame.Data
```

### After (1 copy per frame)
```
ScreenCaptureKit callback
    |
    v COPY 1 (32MB): CVPixelBuffer -> triple buffer (only copy)
inst->frame_buffers[write_idx]
    |
    | (index swap - no copy)
    v
mc_get_latest_frame() returns pointer
    |
    | (unsafe.Slice wraps C memory - no copy)
    v
BGRAFrame.Data (backed by C memory)
    |
    | (explicit Release() call)
    v
mc_release_frame_buffer() marks buffer available
```

---

## Implementation Checklist

### Part 1: C Layer - Triple Buffer System

- [x] **1.1** Modify `CaptureInstance` struct - Add triple buffer fields
  - File: `capture_multi_darwin.go` (C section, lines 28-39)
  - Add: frame_buffers[3], buffer_capacities[3], frame_info[3], buffer_state[3]
  - Add: write_idx, ready_idx, buffer_mutex

- [x] **1.2** Modify `mc_init()` - Initialize new buffer fields
  - File: `capture_multi_darwin.go` (C section, lines 100-115)
  - Initialize buffer_mutex, ready_idx = -1, buffer states

- [x] **1.3** Rewrite `stream_output_callback` - Buffer rotation
  - File: `capture_multi_darwin.go` (C section, lines 53-95)
  - Write to rotating buffers instead of malloc per frame
  - Single memcpy from CVPixelBuffer to triple buffer

- [x] **1.4** Rewrite `mc_get_latest_frame()` - Zero-copy return
  - File: `capture_multi_darwin.go` (C section, lines 420-465)
  - Return pointer to buffer, mark as in_use
  - No malloc, no memcpy

- [x] **1.5** Add `mc_release_frame_buffer()` function
  - File: `capture_multi_darwin.go` (C section, after mc_free_frame)
  - Mark buffer as free when consumer is done

- [x] **1.6** Modify `mc_stop_capture()` - Cleanup triple buffers
  - File: `capture_multi_darwin.go` (C section, lines 310-328)
  - Free all three buffers, destroy buffer_mutex

### Part 2: Go Layer - Zero-Copy BGRAFrame

- [x] **2.1** Modify `BGRAFrame` struct - Add Release support
  - File: `capture_darwin.go` (lines 594-600)
  - Add: cData unsafe.Pointer, slot int
  - Add: Release() method

- [x] **2.2** Modify `GetLatestFrameBGRA()` - Zero-copy return
  - File: `capture_multi_darwin.go` (Go section, lines 748-775)
  - Use unsafe.Slice instead of make+copy
  - Set cData and slot for Release()

### Part 3: Pipeline Integration

- [x] **3.1** Update `encodeLoop()` - Release after encoding
  - File: `multistream.go` (lines 1868-1898)
  - Add cf.frame.Release() after encoding

- [x] **3.2** Update capture loop - Release on drop
  - File: `multistream.go` (around line 1858)
  - Release frame if channel send fails (buffer full)

### Part 4: Build & Test

- [x] **4.1** Build project
- [x] **4.2** Update PERFORMANCE_OPTIMIZATION.md

---

## Expected Impact

| Metric | Before | After | Improvement |
|--------|--------|-------|-------------|
| Copies per frame | 3 | 1 | -67% |
| Memory bandwidth (4K@30fps) | 2.8 GB/s | 0.9 GB/s | -1.9 GB/s |
| Allocations per frame (C) | 2 malloc | 0 malloc | -100% |
| Allocations per frame (Go) | 1 make | 0 make | -100% |

---

## Notes

- Triple buffer ensures callback always has a free buffer to write to
- Buffer states: 0=free, 1=being_written, 2=ready, 3=in_use
- Release() is safe to call multiple times (no-op if already released)
- After Release(), Data is nil - any access will panic clearly
