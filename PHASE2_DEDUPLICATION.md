# Phase 2: Code Deduplication

## Status: COMPLETED

**Risk Level:** Medium
**Lines Saved:** ~200 lines

---

## Summary

Extracted repeated patterns into helper functions and removed duplicate code.

### Changes Made:

| File | Change | Lines Saved |
|------|--------|-------------|
| tui.go | Created `normalizeSignalURL()` helper | ~15 lines |
| tui.go | Removed dead `initServer()` function | ~60 lines |
| tui.go | Removed duplicate `initMultiRemoteSignaling()` | ~50 lines |
| multistream.go | Created `newPipeline()` factory | ~60 lines |
| multistream.go | Created `createAndConfigureEncoder()` helper | ~30 lines |
| capture_multi_darwin.go | Created `captureErrorToGoError()` helper | ~20 lines |

---

## Completed Tasks

### 1. tui.go - URL Normalization
- [x] Created `normalizeSignalURL()` helper function
- [x] Replaced 3 duplicate URL normalization blocks

### 2. tui.go - Server Initialization Consolidation
- [x] Removed dead `initServer()` function (was never called)
- [x] `initMultiServer()` now uses `initRemoteSignaling()`
- [x] Removed duplicate `initMultiRemoteSignaling()` function

### 3. multistream.go - Pipeline Factory
- [x] Created `newPipeline()` factory method on Streamer
- [x] Replaced 4 pipeline creation blocks in:
  - `AddWindow()`
  - `AddWindowDynamic()`
  - `AddDisplay()`
  - `AddDisplayDynamic()`

### 4. multistream.go - Encoder Factory
- [x] Created `createAndConfigureEncoder()` helper method
- [x] Replaced encoder creation + quality mode setup in same 4 locations

### 5. capture_multi_darwin.go - Error Mapping
- [x] Created `captureErrorToGoError()` helper function
- [x] Replaced error switch statements in:
  - `StartWindowCapture()`
  - `StartDisplayCapture()`

### 6. Skipped Tasks
- [-] `cleanupTrackOnError()` - Skipped because the pattern uses closures that capture state at a specific point; extracting would change semantics

---

## New Helper Functions

### tui.go
```go
// normalizeSignalURL converts HTTP URLs to WebSocket URLs
func normalizeSignalURL(url string) string
```

### multistream.go
```go
// newPipeline creates a new StreamPipeline with standard configuration
func (ms *Streamer) newPipeline(trackInfo *StreamTrackInfo, capture *CaptureInstance, encoder VideoEncoder, bitrate int) *StreamPipeline

// createAndConfigureEncoder creates an encoder with current codec and quality settings
func (ms *Streamer) createAndConfigureEncoder(bitrate int) (VideoEncoder, error)
```

### capture_multi_darwin.go
```go
// captureErrorToGoError converts C capture error codes to Go errors
func captureErrorToGoError(code C.int, windowID uint32) error
```

---

## Verification

- [x] Build succeeds: `go build -o gopeep .`
- [ ] Manual testing required by user

## Next Steps

After user testing confirms everything works, Phase 3 (Architecture Improvements) is optional and higher risk.
