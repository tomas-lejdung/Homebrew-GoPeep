# Migration: Unified Streamer Architecture

## Goal
Eliminate the single/multi stream code path split. Everything uses one unified `Streamer` implementation where single-window is just N=1.

## Current State
- `Streamer` + `PeerManager` for single-stream (peer.go) - **DEPRECATED**
- `MultiStreamer` + `MultiPeerManager` for multi-stream (multistream.go) - **NOW UNIFIED**
- TUI uses unified code path
- Per-stream stats working
- Quality changes work via SetBitrate()

## Target State
- Single `Streamer` implementation (renamed from MultiStreamer)
- Single `PeerManager` implementation (renamed from MultiPeerManager)
- No conditional branching based on stream count
- Per-stream stats display
- Runtime bitrate changes via encoder recreation

---

## Migration Steps

### Phase 1: Add Infrastructure - COMPLETED

- [x] **Step 1.1**: Add `SetBitrate(int) error` to VideoEncoder interface in codec.go
- [x] **Step 1.2**: Implement `SetBitrate()` in VPXEncoder (encoder.go) with recreation flag
- [x] **Step 1.3**: Implement `SetBitrate()` in VideoToolboxEncoder (encoder_videotoolbox.go) with recreation flag
- [x] **Step 1.4**: Add `StreamPipelineStats` struct to multistream.go
- [x] **Step 1.5**: Add stats tracking to StreamPipeline (frameCount, bytesSent, etc.)
- [x] **Step 1.6**: Add `GetStats() []StreamPipelineStats` method to MultiStreamer
- [x] **Step 1.7**: Add `SetBitrate(focusBitrate, bgBitrate int)` to MultiStreamer
- [x] **Step 1.8**: Update `StreamPipeline.updateBitrate()` to call encoder's SetBitrate

### Phase 2: Unify TUI - COMPLETED

- [x] **Step 2.1**: Remove `streamer *Streamer` field from model (now uses MultiStreamer)
- [x] **Step 2.2**: Remove `peerManager *PeerManager` field from model (now uses MultiPeerManager)
- [x] **Step 2.3**: Rename `multiStreamer` → `streamer` in model
- [x] **Step 2.4**: Rename `multiPeerManager` → `peerManager` in model
- [x] **Step 2.5**: Merge `initServer()` to use unified NewMultiPeerManager
- [x] **Step 2.6**: Update `startSharing()` to always use unified streamer path
- [x] **Step 2.7**: Replace `restartStreamer()` with `applyBitrateChange()` using SetBitrate()
- [x] **Step 2.8**: Update `applyQuality()` to call `applyBitrateChange()`
- [x] **Step 2.9**: Update tick handler to get stats from unified streamer
- [x] **Step 2.10**: Update `renderStats()` with new compact per-stream format
- [ ] **Step 2.11**: Remove redundant viewers panel from stats (optional cleanup)

**Note**: Fullscreen capture temporarily disabled - shows error message asking user to select windows instead.

### Phase 3: Rename Multi* to Standard Names - PENDING

- [ ] **Step 3.1**: Rename `MultiStreamer` → `Streamer` in multistream.go
- [ ] **Step 3.2**: Rename `MultiPeerManager` → `PeerManager` in multistream.go
- [ ] **Step 3.3**: Update all imports/references in tui.go
- [ ] **Step 3.4**: Update all imports/references in main.go
- [ ] **Step 3.5**: Remove old Streamer/PeerManager from peer.go (or deprecate)

### Phase 4: Cleanup - PENDING

- [ ] **Step 4.1**: Remove dead code paths
- [ ] **Step 4.2**: Update main.go non-TUI modes to use unified streamer
- [ ] **Step 4.3**: Add fullscreen support to MultiCapture (optional)
- [ ] **Step 4.4**: Final build and test
- [ ] **Step 4.5**: Delete this migration document

---

## Stats Panel Design

### Old Format (removed)
```
┌ Stats ─────────────────────────────┐
│ Uptime: 0:12  Resolution: --       │
│ FPS: --/0  Bitrate: --             │
│ Frames: 0  Data: 0 B               │
└────────────────────────────────────┘
```

### New Format (compact per-stream) - IMPLEMENTED
```
┌ Streams ──────────────────────────────────────────┐
│ Uptime: 0:12                                      │
│ 1: VS Code      1920x1080@30 | 2.1Mbps | 45.2MB * │
│ 2: Terminal     1280x720@30  | 0.8Mbps | 12.1MB   │
│ Total: 1.2K frames, 57.3MB                        │
└───────────────────────────────────────────────────┘
```

Where:
- `1:` = Stream number
- `VS Code` = App name (truncated to ~12 chars)
- `1920x1080@30` = Resolution @ FPS
- `2.1Mbps` = Current bitrate
- `45.2MB` = Total data sent
- `*` = Focused stream indicator

---

## Files Modified

| File | Changes | Status |
|------|---------|--------|
| codec.go | Add SetBitrate to interface | Done |
| encoder.go | Implement SetBitrate with recreation | Done |
| encoder_h264.go | Implement SetBitrate with recreation | Done |
| encoder_videotoolbox.go | Implement SetBitrate with recreation | Done |
| multistream.go | Add stats, SetBitrate | Done |
| tui.go | Unify code paths, new stats display | Done |
| main.go | Update to use unified streamer | Pending |
| peer.go | Deprecate/remove old implementations | Pending |

---

## Rollback Plan

Each phase can be rolled back independently via git. Keep commits granular:
1. One commit per step within a phase
2. Tag after each phase completion

---

## Testing Checklist

After Phase 2 (current):
- [x] `go build ./...` succeeds
- [ ] Single window streaming works (needs testing)
- [ ] Multi-window streaming works (needs testing)
- [ ] Quality changes apply to all streams via SetBitrate (needs testing)
- [ ] Stats display updates correctly (needs testing)
- [ ] Adding/removing windows dynamically works (needs testing)
- [ ] Viewer connection/disconnection handled properly (needs testing)

---

## Known Limitations

1. **Fullscreen capture disabled**: The unified MultiStreamer only supports window capture via MultiCapture. Fullscreen capture would require adding `StartDisplayCapture` to MultiCapture.

2. **Old code still present**: peer.go still contains the old Streamer/PeerManager implementations. These will be removed in Phase 4.
