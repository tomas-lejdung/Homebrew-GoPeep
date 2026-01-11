# GoPeep Architecture Refactoring Plan

**Author:** Claude (Opus 4.5)
**Date:** January 2026
**Updated:** After comparison with GPT plan - incorporated best practices

---

## 0. Goals and Constraints

### Goals
- **Single owner of application state**: One `App` struct owns selection, sharing state, and runtime services
- **Unidirectional flow**: UI → App.Dispatch(Action) → State update → UI renders
- **Separation of concerns**: UI knows nothing about WebRTC/capture/signaling
- **Slot-first track management**: Slots are the single source of truth (no `tracks` map)
- **Clean package structure**: Only `main.go` at repo root

### Constraints
- Don't change feature behavior
- Keep macOS main-thread requirements intact (overlay AppKit run loop)
- Incremental steps - repo must build after each phase
- Deactivation never removes slot tracks (only marks inactive)

### Explicit User Decisions
1. **Slots are the primary model** - `tracks map[...]` must be eliminated
2. **Support legacy/on-the-fly only as fallback** when slots not initialized
3. **Deactivate never deletes slots** - only stops sending, marks inactive
4. **Root `main.go` should be the only Go file at repo root**

---

## 1. Current Problems

### A. God Object TUI Model
The `model` struct in `tui.go` (lines 225-310) has **40+ fields** mixing:
- UI state (cursors, dimensions)
- Server state (wsConn, roomCode, reconnection)
- Selection state (selectedWindows, fullscreenSelected)
- Streaming state (streamer, peerManager)
- Overlay state (overlay, overlayController)

**Ownership Confusion:**
```
model
├── peerManager *PeerManager
└── streamer *Streamer
        └── peerManager *PeerManager  (SAME instance!)
```

### B. Mixed Concerns in Large Files
- `multistream.go` (~2800 LOC): WebRTC + negotiation + pipelines + streamer + stats
- `tui.go` (~2650 LOC): UI + selection rules + signaling + streamer creation + overlay bridging

### C. Tracks vs Slots Redundancy
`PeerManager` maintains dual storage that must be synced manually:
```go
tracks map[string]*StreamTrackInfo  // Legacy - REMOVE THIS
slots  [4]*TrackSlot                // Source of truth - KEEP ONLY THIS
```

### D. Overlay State Mirroring
`OverlayController.Sync(...)` copies TUI state - missing a sync causes drift.

---

## 2. Target Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                         main.go                              │
│  - Entry point                                               │
│  - Creates App, starts overlay run loop                      │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                    internal/app.App                          │
│  - Single owner of state                                     │
│  - Selection policy                                          │
│  - Start/stop sharing orchestration                          │
│  - Owns: rtc.Manager, stream.Manager, signalclient.Client    │
└─────────────────────────────────────────────────────────────┘
         │              │                │              │
         ▼              ▼                ▼              ▼
┌──────────────┐ ┌──────────────┐ ┌──────────────┐ ┌──────────────┐
│ internal/rtc │ │internal/stream│ │internal/     │ │internal/     │
│              │ │              │ │signalclient  │ │capture       │
│ PeerManager  │ │ Streamer     │ │              │ │              │
│ Slot mgmt    │ │ Pipeline     │ │ Reconnection │ │ MultiCapture │
│ Offer/Answer │ │ Encode/Send  │ │ Room reserve │ │ Focus/Cursor │
└──────────────┘ └──────────────┘ └──────────────┘ └──────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                         UI Layer                             │
│  ┌────────────────────┐    ┌────────────────────────────┐   │
│  │ internal/ui/tui    │    │ internal/ui/overlaybridge  │   │
│  │ - Bubbletea model  │    │ - overlay.Controller impl  │   │
│  │ - Cursor state     │    │ - Queries app.State()      │   │
│  │ - Renders state    │    │ - Dispatches actions       │   │
│  │ - Dispatches       │    └────────────────────────────┘   │
│  └────────────────────┘                                      │
└─────────────────────────────────────────────────────────────┘
```

### Key Design Principles
1. **Data is queried, actions are dispatched**
2. **App owns state, UIs render snapshots**
3. **No mirrored state - overlay queries App directly**

---

## 3. Package Layout

### Target: Only `main.go` at repo root

```
gopeep/
├── main.go                      # Entry point only (~50 lines)
├── go.mod
├── go.sum
│
├── internal/                    # Private implementation
│   ├── app/                     # Application state machine
│   │   ├── app.go              # App struct, State(), Dispatch()
│   │   ├── selection.go        # Selection policy, LRU eviction
│   │   ├── actions.go          # Action types
│   │   └── state.go            # State struct
│   │
│   ├── rtc/                     # WebRTC peer management
│   │   ├── manager.go          # PeerManager
│   │   ├── slots.go            # Slot management (source of truth)
│   │   ├── negotiation.go      # Offer/answer/ICE
│   │   └── types.go            # StreamTrackInfo, PeerInfo
│   │
│   ├── stream/                  # Streaming orchestration
│   │   ├── streamer.go         # Streamer
│   │   ├── pipeline.go         # Pipeline (capture→encode→send)
│   │   └── stats.go            # StreamPipelineStats
│   │
│   ├── capture/                 # Screen capture
│   │   ├── capture_darwin.go   # macOS implementation
│   │   ├── multi_darwin.go     # MultiCapture
│   │   ├── focus_darwin.go     # Focus/cursor detection
│   │   ├── types.go            # WindowInfo, BGRAFrame
│   │   └── capture_stub.go     # Non-macOS stub
│   │
│   ├── encode/                  # Video encoding
│   │   ├── encoder.go          # VideoEncoder interface
│   │   ├── vpx.go              # VP8/VP9
│   │   ├── h264.go             # Software H.264
│   │   ├── videotoolbox.go     # Hardware H.264
│   │   └── factory.go          # EncoderFactory
│   │
│   ├── signalclient/            # Signal server client
│   │   ├── client.go           # WebSocket client
│   │   ├── reconnect.go        # Reconnection policy
│   │   └── room.go             # Room reservation
│   │
│   └── ui/                      # UI layer
│       ├── tui/                 # Bubbletea TUI
│       │   ├── model.go        # Bubbletea model (UI state only)
│       │   ├── view.go         # Rendering
│       │   ├── keys.go         # Key handling
│       │   └── messages.go     # Bubbletea messages
│       │
│       └── overlaybridge/       # Overlay integration
│           └── controller.go   # overlay.Controller implementation
│
├── pkg/                         # Public packages (for cmd/server)
│   ├── signal/                  # Signaling server + message types
│   │   ├── server.go
│   │   ├── handlers.go
│   │   ├── message.go
│   │   └── viewer.html
│   │
│   ├── overlay/                 # Overlay package (already good)
│   │   ├── overlay.go
│   │   └── overlay_darwin.go
│   │
│   └── settings/                # Settings persistence
│       └── settings.go
│
└── cmd/
    └── server/                  # Standalone signal server
        └── main.go
```

---

## 4. Slot-First Track Management

### Current Problem
```go
type PeerManager struct {
    tracks map[string]*StreamTrackInfo  // REDUNDANT - DELETE
    slots  [4]*TrackSlot                // SOURCE OF TRUTH
}
```

### Target: Slots Only

```go
// internal/rtc/manager.go
type Manager struct {
    slots      [4]*Slot
    slotsReady bool
    // NO tracks map
}

// internal/rtc/slots.go
type Slot struct {
    TrackID  string                         // "video0".."video3" (stable)
    StreamID string                         // "gopeep-stream-0"..3 (stable)
    Track    *webrtc.TrackLocalStaticSample // WebRTC track (stable)
    Active   bool                           // Is window assigned?
    Info     *StreamInfo                    // Window info when active
}

type StreamInfo struct {
    WindowID   uint32
    WindowName string
    AppName    string
    Width      int
    Height     int
    IsFocused  bool
}
```

### Slot API

```go
// Activation (no renegotiation when slots pre-initialized)
func (m *Manager) Activate(windowID uint32, name, app string, w, h int) (trackID string, err error)

// Deactivation (NEVER deletes slot, only marks inactive)
func (m *Manager) Deactivate(trackID string) error

// Query methods (iterate slots, no map lookups)
func (m *Manager) ActiveStreams() []StreamInfo
func (m *Manager) GetStreamInfo(trackID string) *StreamInfo
func (m *Manager) FocusedStream() *StreamInfo
func (m *Manager) SetFocusedWindow(windowID uint32) string
```

### Implementation

```go
func (m *Manager) ActiveStreams() []StreamInfo {
    m.mu.RLock()
    defer m.mu.RUnlock()

    result := make([]StreamInfo, 0, 4)
    for i := 0; i < 4; i++ {
        if m.slots[i] != nil && m.slots[i].Active && m.slots[i].Info != nil {
            result = append(result, *m.slots[i].Info)
        }
    }
    return result
}

func (m *Manager) GetStreamInfo(trackID string) *StreamInfo {
    m.mu.RLock()
    defer m.mu.RUnlock()

    for i := 0; i < 4; i++ {
        if m.slots[i] != nil && m.slots[i].TrackID == trackID {
            return m.slots[i].Info
        }
    }
    return nil
}

func (m *Manager) Deactivate(trackID string) error {
    m.mu.Lock()
    defer m.mu.Unlock()

    for i := 0; i < 4; i++ {
        if m.slots[i] != nil && m.slots[i].TrackID == trackID {
            // CRITICAL: Never delete slot, only mark inactive
            m.slots[i].Active = false
            m.slots[i].Info = nil
            return nil
        }
    }
    return fmt.Errorf("slot not found: %s", trackID)
}
```

---

## 5. App State Machine

### State Snapshot

```go
// internal/app/state.go
type State struct {
    // Selection
    SelectedWindows    map[uint32]bool
    FullscreenSelected bool
    AutoShareEnabled   bool

    // Sharing
    Sharing   bool
    Starting  bool
    RoomCode  string
    ShareURL  string

    // Viewers
    ViewerCount int

    // Streams
    Streams []StreamInfo

    // Config
    Codec           CodecType
    FPS             int
    Quality         int
    AdaptiveBitrate bool
    QualityMode     bool

    // Connection
    SignalState SignalState // Connected/Disconnected/Reconnecting
}
```

### Actions

```go
// internal/app/actions.go
type Action interface{ action() }

type ToggleWindow struct { WindowID uint32 }
type ToggleFullscreen struct {}
type ClearSelection struct {}
type StartSharing struct {}
type StopSharing struct {}
type SetCodec struct { Codec CodecType }
type SetFPS struct { FPS int }
type SetQuality struct { Quality int }
type ToggleAdaptiveBitrate struct {}
type ToggleQualityMode struct {}
type SignalConnected struct {}
type SignalDisconnected struct {}
type ViewerCountChanged struct { Count int }
```

### App Interface

```go
// internal/app/app.go
type App struct {
    mu sync.RWMutex

    // Owned services
    rtcManager     *rtc.Manager
    streamManager  *stream.Manager
    signalClient   *signalclient.Client

    // State
    state State

    // Callbacks
    onStateChange func()
}

func (a *App) State() State {
    a.mu.RLock()
    defer a.mu.RUnlock()
    return a.state // Return copy
}

func (a *App) Dispatch(action Action) error {
    a.mu.Lock()
    defer a.mu.Unlock()

    switch act := action.(type) {
    case ToggleWindow:
        return a.toggleWindow(act.WindowID)
    case StartSharing:
        return a.startSharing()
    // ... etc
    }
    return nil
}
```

---

## 6. Migration Phases

### Phase 0: Baseline (Day 0)
- Ensure `go test ./...` passes
- Ensure `go build` works
- Document manual smoke test steps

### Phase 1: Create `internal/app` Shell (~Day 1)
- Create `internal/app/` with App, State, Action types
- App initially wraps existing behavior through adapters
- TUI still works, just imports app

**Files:**
- NEW: `internal/app/app.go`
- NEW: `internal/app/state.go`
- NEW: `internal/app/actions.go`
- MODIFY: `tui.go` - import app, create App in RunTUI

### Phase 2: Move Selection Logic (~Day 2)
- Move SelectionManager + LRU eviction into `internal/app/selection.go`
- Replace TUI direct state mutations with `app.Dispatch()`
- Keep behavior identical (including auto-share)

**Files:**
- NEW: `internal/app/selection.go`
- MODIFY: `tui.go` - remove selectedWindows, call Dispatch

### Phase 3: Slot-First RTC Manager (~Day 3-4)
- Move PeerManager into `internal/rtc/`
- Remove `tracks` map - slots are only source of truth
- Implement `Activate()` / `Deactivate()` with slot-only storage
- Deactivate NEVER deletes slots

**Files:**
- NEW: `internal/rtc/manager.go`
- NEW: `internal/rtc/slots.go`
- NEW: `internal/rtc/negotiation.go`
- NEW: `internal/rtc/types.go`
- DELETE (content moved): `multistream.go` PeerManager section

### Phase 4: Signaling Client (~Day 5)
- Move room reservation + connect + reconnection into `internal/signalclient/`
- Replace `wsDisconnected *bool` hack with proper state machine
- App owns signaling client

**Files:**
- NEW: `internal/signalclient/client.go`
- NEW: `internal/signalclient/reconnect.go`
- NEW: `internal/signalclient/room.go`
- MODIFY: `main.go` - remove setupSignaling, use signalclient
- MODIFY: `internal/app/app.go` - own signalclient

### Phase 5: Streaming Package (~Day 6-7)
- Move Streamer + Pipeline into `internal/stream/`
- Remove Streamer's direct PeerManager ownership
- Pass `rtc.TrackWriter` interface instead

**Files:**
- NEW: `internal/stream/streamer.go`
- NEW: `internal/stream/pipeline.go`
- NEW: `internal/stream/stats.go`
- DELETE (content moved): remaining `multistream.go`

### Phase 6: Capture Package (~Day 8)
- Move capture_darwin.go, capture_multi_darwin.go into `internal/capture/`
- Keep build tags
- Make focus/cursor functions callable by both overlay and app

**Files:**
- NEW: `internal/capture/capture_darwin.go`
- NEW: `internal/capture/multi_darwin.go`
- NEW: `internal/capture/focus_darwin.go`
- NEW: `internal/capture/types.go`
- NEW: `internal/capture/capture_stub.go`
- DELETE: root capture files

### Phase 7: Rebuild TUI (~Day 9)
- Create `internal/ui/tui/` with clean Bubbletea model
- Model only contains: cursor state, render logic, key handling
- Calls `app.Dispatch()` for all actions
- Renders from `app.State()`

**Files:**
- NEW: `internal/ui/tui/model.go`
- NEW: `internal/ui/tui/view.go`
- NEW: `internal/ui/tui/keys.go`
- DELETE: `tui.go`

### Phase 8: Overlay Bridge (~Day 10)
- Create `internal/ui/overlaybridge/` with Controller implementation
- Controller queries `app.State()` directly (no Sync() mirroring)
- Events dispatch to `app.Dispatch()`

**Files:**
- NEW: `internal/ui/overlaybridge/controller.go`
- DELETE: `overlay_controller.go`

### Phase 9: Root Cleanup (~Day 11)
- Move remaining root files to internal packages
- Move encoders to `internal/encode/`
- Keep only `main.go` at root

**Files:**
- MOVE: `encoder*.go` → `internal/encode/`
- MOVE: `codec.go` → `internal/encode/`
- MOVE: `quality.go`, `fps.go` → `internal/app/` or `internal/encode/`
- MOVE: `peer.go` → `internal/rtc/`
- DELETE: All root `.go` files except `main.go`

### Phase 10: Tests + Polish (~Day 12)
- Add unit tests where now possible:
  - `internal/app/selection_test.go` - LRU eviction, capacity limits
  - `internal/rtc/slots_test.go` - activate/deactivate invariants
- Ensure `go test ./...` and `go vet ./...` pass
- gofmt, import grouping

---

## 7. Definitions of Done

- [ ] `tui.go` no longer exists - replaced by `internal/ui/tui/`
- [ ] TUI model doesn't own streaming/signaling services
- [ ] Overlay doesn't mirror state via `Sync(...)` - queries App directly
- [ ] Track management is slot-first - no `tracks` map
- [ ] Deactivation never deletes slot tracks
- [ ] Repo root contains only `main.go` (no other `.go` files)
- [ ] `go test ./...` passes
- [ ] `go vet ./...` passes
- [ ] Manual smoke test passes

---

## 8. Risk Mitigations

### Risk: Breaking macOS main thread invariants
**Mitigation:** Keep current `main.go` structure (overlay run loop on main thread, TUI in goroutine) until Phase 9. Only then refactor main.go.

### Risk: Subtle SDP/renegotiation behavior changes
**Mitigation:**
- Keep compatibility mode during transition
- Add verbose logging around offer creation
- Test with viewer after each phase

### Risk: Deadlocks or races with new App state
**Mitigation:**
- `State()` returns copy, not reference
- Use channels for eventing where possible
- Run with `-race` flag during development

### Risk: CGO files don't work in internal packages
**Mitigation:**
- Test CGO build early (Phase 6)
- Keep build tags intact
- May need to keep some CGO at root if issues arise

---

## 9. Open Questions

1. **MaxCaptureInstances location**: Should this constant live in `internal/capture` or `internal/app`? (Currently duplicated)

2. **Eager vs lazy slot creation**: Should all 4 slots be created at startup, or lazily? (Prefer eager for consistency)

3. **Fullscreen representation**: Currently `windowID=0` means fullscreen. Should this be a separate field in Slot?

4. **Encoder package CGO**: VideoToolbox requires specific frameworks - will it work in `internal/encode/`?

---

## 10. Manual Smoke Test Checklist

After each phase:
- [ ] `go build -o gopeep .` succeeds
- [ ] Start app, room code appears
- [ ] Select 1 window via TUI → sharing starts
- [ ] Viewer sees stream
- [ ] Add 2nd window via overlay → streams without renegotiation
- [ ] Toggle fullscreen mode → windows cleared
- [ ] Stop sharing → streams stop
- [ ] Start again → works
- [ ] Change codec → streams recover
- [ ] Kill signal server → reconnection works
- [ ] Check `gopeep-debug.log` for errors

---

## Appendix: Comparison with GPT Plan

| Aspect | Claude Plan | GPT Plan | Resolution |
|--------|-------------|----------|------------|
| Package structure | `pkg/*` | `internal/*` | **GPT wins** - adopted `internal/` |
| Phases | 4 phases | 10 phases | **GPT wins** - adopted 10 phases |
| Action/Dispatch | Methods only | Explicit Actions | **Hybrid** - adopted Action types |
| Code examples | Detailed | Abstract | **Claude wins** - kept examples |
| File tables | Yes | No | **Claude wins** - kept tables |
| Open questions | No | Yes | **GPT wins** - added section |
| Definitions of done | No | Yes | **GPT wins** - added section |
| Risk mitigations | Brief | Detailed | **GPT wins** - expanded |
| Signal separation | In AppCore | `signalclient` pkg | **GPT wins** - adopted |
| UI structure | `pkg/app/tui` | `internal/ui/tui` | **GPT wins** - adopted |
