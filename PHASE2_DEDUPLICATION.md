# Phase 2: Code Deduplication

## Overview
Extract repeated patterns into helper functions to reduce code duplication and improve maintainability.

**Risk Level:** Medium
**Estimated Lines Saved:** ~200-300

---

## Tasks

### 1. tui.go - URL Normalization Helper

#### 1.1 Create `normalizeSignalURL()` helper
- **Status:** [ ] Pending
- **Duplicated in:**
  - `attemptReconnect()` (line ~1261)
  - `initRemoteSignaling()` (line ~1316)
  - `initMultiRemoteSignaling()` (line ~1677)

```go
// normalizeSignalURL converts HTTP URLs to WebSocket URLs
func normalizeSignalURL(url string) string {
    if strings.HasPrefix(url, "http://") {
        return "ws://" + strings.TrimPrefix(url, "http://")
    } else if strings.HasPrefix(url, "https://") {
        return "wss://" + strings.TrimPrefix(url, "https://")
    } else if !strings.HasPrefix(url, "ws://") && !strings.HasPrefix(url, "wss://") {
        return "wss://" + url
    }
    return url
}
```

---

### 2. tui.go - Merge Duplicate Server Init Functions

#### 2.1 Merge `initServer()` and `initMultiServer()`
- **Status:** [ ] Pending
- **Why:** These functions are nearly identical
- **Differences:** None significant - both use NewPeerManager
- **Action:** Remove `initMultiServer()`, use `initServer()` everywhere

#### 2.2 Merge `initRemoteSignaling()` and `initMultiRemoteSignaling()`
- **Status:** [ ] Pending
- **Why:** 100% duplicated code
- **Action:** Remove `initMultiRemoteSignaling()`, use `initRemoteSignaling()` everywhere

---

### 3. multistream.go - Pipeline Factory

#### 3.1 Create `newPipeline()` factory method
- **Status:** [ ] Pending
- **Duplicated in:**
  - `AddWindow()` (line ~1355)
  - `AddWindowDynamic()` (line ~2014)
  - `AddDisplay()` (line ~2232)
  - `AddDisplayDynamic()` (line ~2340)
  - `SetCodec()` slots path (line ~1794)
  - `SetCodec()` legacy path (line ~1863)

```go
func (ms *Streamer) newPipeline(trackInfo *StreamTrackInfo, capture *CaptureInstance, encoder VideoEncoder, bitrate int) *StreamPipeline {
    return &StreamPipeline{
        trackInfo:      trackInfo,
        capture:        capture,
        encoder:        encoder,
        fps:            ms.fps,
        bitrate:        bitrate,
        focusBitrate:   ms.focusBitrate,
        bgBitrate:      ms.bgBitrate,
        adaptiveBR:     ms.adaptiveBitrate,
        qualityMode:    ms.qualityMode,
        stopChan:       make(chan struct{}),
        fpsChanged:     make(chan int, 1),
        capturedFrames: make(chan capturedFrame, 2),
        encodedFrames:  make(chan encodedFrame, 2),
    }
}
```

---

### 4. multistream.go - Encoder Factory Helper

#### 4.1 Create `createAndConfigureEncoder()` helper
- **Status:** [ ] Pending
- **Duplicated in:** 6 locations (same as pipeline creation)

```go
func (ms *Streamer) createAndConfigureEncoder(bitrate int) (VideoEncoder, error) {
    factory := NewEncoderFactory()
    encoder, err := factory.CreateEncoder(ms.codecType, ms.fps, bitrate)
    if err != nil {
        return nil, err
    }
    if ms.qualityMode {
        encoder.SetQualityMode(true, bitrate)
    }
    return encoder, nil
}
```

---

### 5. multistream.go - Track Cleanup Helper

#### 5.1 Create `cleanupTrackOnError()` method
- **Status:** [ ] Pending
- **Duplicated in:**
  - `AddWindow()` (line ~1319)
  - `AddDisplay()` (line ~2198)
  - `AddDisplayDynamic()` (line ~2307)

```go
func (ms *Streamer) cleanupTrackOnError(trackID string) {
    if ms.peerManager.AreSlotsReady() {
        ms.peerManager.DeactivateSlot(trackID)
    } else {
        ms.peerManager.RemoveTrack(trackID)
    }
}
```

---

### 6. capture_multi_darwin.go - Error Mapping Helper

#### 6.1 Create `captureErrorToGoError()` helper
- **Status:** [ ] Pending
- **Duplicated in:**
  - `StartWindowCapture()` (line ~912)
  - `StartDisplayCapture()` (line ~955)

```go
func captureErrorToGoError(code int, windowID uint32) error {
    switch code {
    case -1:
        return fmt.Errorf("no free capture slots available")
    case -2:
        if windowID == 0 {
            return fmt.Errorf("display not found")
        }
        return fmt.Errorf("window not found: %d", windowID)
    case -3:
        return fmt.Errorf("failed to add stream output")
    case -4:
        return fmt.Errorf("failed to start capture")
    case -5:
        return fmt.Errorf("slot is still active")
    default:
        return fmt.Errorf("capture error: %d", code)
    }
}
```

---

## Verification Steps

After each helper is created:
1. Run `go build -o gopeep .` - must succeed
2. Test all capture modes work correctly
3. Test remote signaling reconnection works

---

## Rollback Plan

Each helper function change is isolated and can be reverted independently.
