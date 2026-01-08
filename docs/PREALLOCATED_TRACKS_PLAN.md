# Pre-allocated Tracks for Instant Window Sharing

## Problem Statement

Currently, when a user shares an additional window while a viewer is connected, there's a noticeable delay (1-3 seconds) caused by:

1. **Track creation**: Creating a new WebRTC track (~10ms)
2. **Adding track to peer**: `AddTrackToAllPeers()` loops through connections (~50ms)
3. **SDP renegotiation**: Creating new offer, waiting for ICE gathering, sending to viewer (~500ms-2s)
4. **Viewer processing**: Viewer receives offer, creates answer, sends back (~200-500ms)

This makes adding a second or third window feel sluggish.

## Solution: Pre-allocated Tracks

**Core idea**: Create all 4 WebRTC tracks upfront when the session starts. When a viewer connects, they receive all 4 tracks in the initial SDP negotiation. Adding a new window simply means "activating" an existing track by starting to send data on it - no renegotiation required.

### Architecture Overview

```
BEFORE (Current):
┌─────────────────────────────────────────────────────────────────┐
│ Share Window 1 → Create track0 → Viewer joins (SDP with 1 track)│
│ Share Window 2 → Create track1 → Add to PC → RENEGOTIATE (slow) │
│ Share Window 3 → Create track2 → Add to PC → RENEGOTIATE (slow) │
└─────────────────────────────────────────────────────────────────┘

AFTER (Pre-allocated):
┌─────────────────────────────────────────────────────────────────┐
│ Session starts → Create track0,1,2,3 (all pre-allocated)        │
│ Share Window 1 → Activate track0 → Start sending                │
│ Viewer joins   → SDP includes all 4 tracks                      │
│ Share Window 2 → Activate track1 → Start sending → INSTANT!     │
│ Share Window 3 → Activate track2 → Start sending → INSTANT!     │
└─────────────────────────────────────────────────────────────────┘
```

### Expected Performance Improvement

| Action | Current | After |
|--------|---------|-------|
| Share 2nd window (viewer connected) | ~1-3 seconds | ~50-100ms |
| Share 3rd/4th window | ~1-3 seconds | ~50-100ms |
| Initial viewer join | ~1-2 seconds | ~1-2 seconds (same) |

---

## Implementation Plan

### Phase 1: Track Slot Infrastructure

#### 1.1 Add Track Slot Concept to PeerManager

**File**: `multistream.go`

Add new types and fields to track "slots" vs "active tracks":

```go
// TrackSlot represents a pre-allocated track slot
type TrackSlot struct {
    TrackID  string                           // "video0", "video1", etc.
    Track    *webrtc.TrackLocalStaticSample   // The actual WebRTC track
    Active   bool                             // Whether this slot has an active stream
    Info     *StreamTrackInfo                 // Window info when active, nil when inactive
}

// Add to PeerManager struct:
type PeerManager struct {
    // ... existing fields ...
    
    slots       [MaxCaptureInstances]*TrackSlot  // Pre-allocated track slots (4)
    slotsReady  bool                              // Whether slots have been initialized
}
```

#### 1.2 Initialize Slots on Session Start

**File**: `multistream.go` - New method `InitializeTrackSlots()`

```go
// InitializeTrackSlots creates all 4 track slots upfront
func (mpm *PeerManager) InitializeTrackSlots() error {
    mpm.mu.Lock()
    defer mpm.mu.Unlock()
    
    if mpm.slotsReady {
        return nil // Already initialized
    }
    
    for i := 0; i < MaxCaptureInstances; i++ {
        trackID := fmt.Sprintf("video%d", i)
        
        // Create the WebRTC track
        track, err := webrtc.NewTrackLocalStaticSample(
            webrtc.RTPCodecCapability{MimeType: mpm.getMimeType()},
            trackID,
            "gopeep-stream",
        )
        if err != nil {
            return fmt.Errorf("failed to create track slot %d: %w", i, err)
        }
        
        mpm.slots[i] = &TrackSlot{
            TrackID: trackID,
            Track:   track,
            Active:  false,
            Info:    nil,
        }
    }
    
    mpm.slotsReady = true
    return nil
}
```

#### 1.3 Modify CreateOffer to Include All Slots

**File**: `multistream.go` - Modify `CreateOffer()`

Change from adding only active tracks to adding ALL pre-allocated slots:

```go
func (mpm *PeerManager) CreateOffer(peerID string) (string, error) {
    // ... existing setup code ...
    
    // Add ALL track slots (not just active ones)
    for i := 0; i < MaxCaptureInstances; i++ {
        slot := mpm.slots[i]
        if slot == nil {
            continue
        }
        
        sender, err := pc.AddTrack(slot.Track)
        if err != nil {
            pc.Close()
            return "", fmt.Errorf("failed to add track slot %d: %w", i, err)
        }
        peerInfo.Senders[slot.TrackID] = sender
    }
    
    // ... rest of method ...
}
```

---

### Phase 2: Modify Window Addition Flow

#### 2.1 New `ActivateSlot()` Method

**File**: `multistream.go`

```go
// ActivateSlot activates a pre-allocated slot for a window
// Returns the slot index and track info
func (mpm *PeerManager) ActivateSlot(windowID uint32, windowName, appName string, width, height int) (*TrackSlot, error) {
    mpm.mu.Lock()
    defer mpm.mu.Unlock()
    
    // Find first inactive slot
    var slot *TrackSlot
    for i := 0; i < MaxCaptureInstances; i++ {
        if mpm.slots[i] != nil && !mpm.slots[i].Active {
            slot = mpm.slots[i]
            break
        }
    }
    
    if slot == nil {
        return nil, fmt.Errorf("no available track slots (max %d)", MaxCaptureInstances)
    }
    
    // Activate the slot
    slot.Active = true
    slot.Info = &StreamTrackInfo{
        TrackID:    slot.TrackID,
        WindowID:   windowID,
        WindowName: windowName,
        AppName:    appName,
        Track:      slot.Track,
        Width:      width,
        Height:     height,
    }
    
    // Also add to tracks map for backward compatibility
    mpm.tracks[slot.TrackID] = slot.Info
    
    return slot, nil
}
```

#### 2.2 New `DeactivateSlot()` Method

**File**: `multistream.go`

```go
// DeactivateSlot deactivates a slot (window unshared)
func (mpm *PeerManager) DeactivateSlot(trackID string) error {
    mpm.mu.Lock()
    defer mpm.mu.Unlock()
    
    for i := 0; i < MaxCaptureInstances; i++ {
        if mpm.slots[i] != nil && mpm.slots[i].TrackID == trackID {
            mpm.slots[i].Active = false
            mpm.slots[i].Info = nil
            delete(mpm.tracks, trackID)
            return nil
        }
    }
    
    return fmt.Errorf("slot not found: %s", trackID)
}
```

#### 2.3 Modify `AddWindowDynamic()` in Streamer

**File**: `multistream.go` - Modify `AddWindowDynamic()`

```go
func (ms *Streamer) AddWindowDynamic(window WindowInfo) (*StreamTrackInfo, error) {
    ms.mu.Lock()
    if len(ms.pipelines) >= MaxCaptureInstances {
        ms.mu.Unlock()
        return nil, fmt.Errorf("maximum windows (%d) reached", MaxCaptureInstances)
    }
    // ... existing duplicate check ...
    ms.mu.Unlock()
    
    // CHANGED: Activate a pre-allocated slot instead of creating new track
    slot, err := ms.peerManager.ActivateSlot(
        window.ID, 
        window.WindowName, 
        window.OwnerName,
        int(window.Width),
        int(window.Height),
    )
    if err != nil {
        return nil, err
    }
    
    trackInfo := slot.Info
    
    // Start capture (unchanged)
    capture, err := ms.multiCapture.StartWindowCapture(window.ID, 0, 0, ms.fps)
    if err != nil {
        ms.peerManager.DeactivateSlot(slot.TrackID)
        return nil, fmt.Errorf("failed to start capture: %w", err)
    }
    
    // Create encoder (unchanged)
    // ... existing encoder creation code ...
    
    // Create pipeline (unchanged)
    // ... existing pipeline creation code ...
    
    // REMOVED: AddTrackToAllPeers() - tracks already exist on all connections!
    // REMOVED: RenegotiateAllPeers() - no renegotiation needed!
    
    // Notify about stream activation (lightweight metadata update)
    ms.peerManager.NotifyStreamActivated(sig.StreamInfo{
        TrackID:    trackInfo.TrackID,
        WindowName: trackInfo.WindowName,
        AppName:    trackInfo.AppName,
        IsFocused:  trackInfo.IsFocused,
        Width:      trackInfo.Width,
        Height:     trackInfo.Height,
    })
    
    return trackInfo, nil
}
```

---

### Phase 3: Signaling Updates

#### 3.1 New "stream-activated" Message Type

**File**: `pkg/signal/types.go` (or equivalent)

```go
// Add new message type
const (
    // ... existing types ...
    TypeStreamActivated = "stream-activated"
    TypeStreamDeactivated = "stream-deactivated"
)
```

#### 3.2 Modify Signaling Handler

**File**: `main.go` - Add callback for stream activation

```go
// In setupPeerSignaling() or equivalent:
pm.SetStreamActivationCallback(func(info sig.StreamInfo) {
    msg := sig.Message{
        Type:       sig.TypeStreamActivated,
        StreamInfo: &info,
    }
    broadcastToViewers(msg)
})
```

#### 3.3 Update Viewer JavaScript

**File**: `pkg/signal/viewer.html`

```javascript
// Handle stream-activated message (no renegotiation, just metadata)
function handleStreamActivated(info) {
    console.log('Stream activated:', info.trackId);
    
    // Update streams info
    const match = info.trackId.match(/video(\d+)/);
    if (match) {
        const idx = parseInt(match[1], 10);
        
        // Update info for this stream
        if (streams[idx]) {
            streams[idx].info = info;
        }
        
        // Add to streamsInfo array if not already present
        const existing = streamsInfo.findIndex(s => s.trackId === info.trackId);
        if (existing >= 0) {
            streamsInfo[existing] = info;
        } else {
            streamsInfo.push(info);
        }
        
        // Update UI
        updateMultiStreamUI();
    }
}

// In WebSocket message handler:
case 'stream-activated':
    handleStreamActivated(msg.streamInfo);
    break;
```

---

### Phase 4: Handle Initial Streams Info

#### 4.1 Include Active Slots in streams-info

**File**: `main.go` or `multistream.go`

When sending initial `streams-info` to a new viewer, only include ACTIVE slots:

```go
func (mpm *PeerManager) GetActiveStreamsInfo() []sig.StreamInfo {
    mpm.mu.RLock()
    defer mpm.mu.RUnlock()
    
    result := make([]sig.StreamInfo, 0)
    for i := 0; i < MaxCaptureInstances; i++ {
        slot := mpm.slots[i]
        if slot != nil && slot.Active && slot.Info != nil {
            result = append(result, sig.StreamInfo{
                TrackID:    slot.Info.TrackID,
                WindowName: slot.Info.WindowName,
                AppName:    slot.Info.AppName,
                IsFocused:  slot.Info.IsFocused,
                Width:      slot.Info.Width,
                Height:     slot.Info.Height,
            })
        }
    }
    return result
}
```

---

### Phase 5: Cleanup and Edge Cases

#### 5.1 Handle Viewer Connection Before Any Window is Shared

If a viewer connects before any windows are shared:
- They still receive all 4 track slots in the SDP
- `streams-info` will be empty
- Viewer shows "Waiting for sharer..." state
- When first window is shared, viewer receives `stream-activated` message

#### 5.2 Handle Window Removal

When a window is unshared:
1. Stop the pipeline
2. Deactivate the slot (slot.Active = false)
3. Send `stream-deactivated` message to viewers
4. Viewers hide the thumbnail, switch main video if needed
5. **No renegotiation** - track remains in SDP, just stops sending data

#### 5.3 Backward Compatibility

During transition, support both:
- Old viewers that expect renegotiation
- New viewers that handle stream-activated

Use a capability check or version flag in the initial handshake.

---

## Files to Modify

| File | Changes |
|------|---------|
| `multistream.go` | Add TrackSlot struct, InitializeTrackSlots(), ActivateSlot(), DeactivateSlot(), modify CreateOffer(), GetActiveStreamsInfo() |
| `main.go` | Initialize slots on server start, add stream activation callbacks |
| `tui.go` | Call InitializeTrackSlots() when starting session |
| `pkg/signal/types.go` | Add stream-activated/deactivated message types |
| `pkg/signal/viewer.html` | Handle stream-activated messages, update UI without renegotiation |

---

## Testing Plan

1. **Single window, viewer joins**: Should work as before
2. **Add second window while viewer connected**: Should be near-instant (< 200ms)
3. **Remove window while viewer connected**: Should hide thumbnail immediately
4. **Viewer joins with 2 windows already shared**: Should see both immediately
5. **Max windows (4)**: Should still enforce limit correctly
6. **Codec change**: Needs special handling (may require slot re-creation)

---

## Future Enhancements (Phase 2)

### Encoder Pool

Pre-warm 2-3 encoders to eliminate encoder initialization latency:

```go
type EncoderPool struct {
    available chan Encoder
    codec     CodecType
    fps       int
}

func (p *EncoderPool) Get() Encoder {
    select {
    case enc := <-p.available:
        return enc
    default:
        // Create new if pool empty
        return createEncoder(p.codec, p.fps)
    }
}

func (p *EncoderPool) Return(enc Encoder) {
    enc.Reset() // Clear state
    select {
    case p.available <- enc:
    default:
        enc.Close() // Pool full, discard
    }
}
```

This would reduce encoder creation from ~50-100ms to ~1ms.

---

## Questions / Decisions Needed

1. **Slot count**: Hard-code 4 or make configurable?
   - Recommendation: Hard-code 4 (matches MaxCaptureInstances)

2. **What to send on inactive tracks?**: Nothing, or a placeholder frame?
   - Recommendation: Nothing (no bandwidth cost)

3. **Backward compatibility**: Support old viewers during transition?
   - Recommendation: Yes, detect viewer version and fall back to renegotiation if needed

---

## Estimated Implementation Effort

| Phase | Effort | Description |
|-------|--------|-------------|
| Phase 1 | ~2-3 hours | Track slot infrastructure |
| Phase 2 | ~2-3 hours | Modify window addition flow |
| Phase 3 | ~1-2 hours | Signaling updates |
| Phase 4 | ~1 hour | Initial streams info |
| Phase 5 | ~1-2 hours | Edge cases and testing |
| **Total** | **~7-11 hours** | |

---

## Rollback Plan

If issues arise, can easily roll back by:
1. Disabling slot pre-allocation
2. Reverting to creating tracks on-demand
3. Re-enabling renegotiation in AddWindowDynamic

The changes are mostly additive and the old code paths can be preserved behind a feature flag.
