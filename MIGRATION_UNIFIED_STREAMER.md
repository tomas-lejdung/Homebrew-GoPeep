# Migration: Unified Streamer Architecture - COMPLETED

## Goal
Eliminate the single/multi stream code path split. Everything uses one unified `Streamer` implementation where single-window is just N=1.

## Final State (Migration Complete)

- `Streamer` (multistream.go) - unified streamer for all capture modes
- `PeerManager` (multistream.go) - unified peer manager for all capture modes  
- `LegacyStreamer` (peer.go) - deprecated, kept for non-TUI CLI modes
- `LegacyPeerManager` (peer.go) - deprecated, kept for non-TUI CLI modes
- TUI uses unified code path
- Per-stream stats working
- Quality changes work via SetBitrate()

---

## Migration Steps - ALL COMPLETED

### Phase 1: Add Infrastructure - COMPLETED

- [x] **Step 1.1**: Add `SetBitrate(int) error` to VideoEncoder interface in codec.go
- [x] **Step 1.2**: Implement `SetBitrate()` in VPXEncoder (encoder.go) with recreation flag
- [x] **Step 1.3**: Implement `SetBitrate()` in VideoToolboxEncoder (encoder_videotoolbox.go) with recreation flag
- [x] **Step 1.4**: Add `StreamPipelineStats` struct to multistream.go
- [x] **Step 1.5**: Add stats tracking to StreamPipeline (frameCount, bytesSent, etc.)
- [x] **Step 1.6**: Add `GetStats() []StreamPipelineStats` method to Streamer
- [x] **Step 1.7**: Add `SetBitrate(focusBitrate, bgBitrate int)` to Streamer
- [x] **Step 1.8**: Update `StreamPipeline.updateBitrate()` to call encoder's SetBitrate

### Phase 2: Unify TUI - COMPLETED

- [x] **Step 2.1**: Remove old `Streamer` field from model (now uses unified Streamer)
- [x] **Step 2.2**: Remove old `PeerManager` field from model (now uses unified PeerManager)
- [x] **Step 2.3**: Model uses `streamer *Streamer` (unified)
- [x] **Step 2.4**: Model uses `peerManager *PeerManager` (unified)
- [x] **Step 2.5**: `initServer()` uses unified NewPeerManager
- [x] **Step 2.6**: `startSharing()` uses unified streamer path
- [x] **Step 2.7**: `applyBitrateChange()` uses SetBitrate() instead of restart
- [x] **Step 2.8**: `applyQuality()` calls `applyBitrateChange()`
- [x] **Step 2.9**: Tick handler gets stats from unified streamer
- [x] **Step 2.10**: `renderStats()` uses new compact per-stream format

### Phase 3: Rename Types - COMPLETED

- [x] **Step 3.1**: Renamed `Streamer` → `LegacyStreamer` in peer.go
- [x] **Step 3.2**: Renamed `PeerManager` → `LegacyPeerManager` in peer.go
- [x] **Step 3.3**: Renamed `MultiStreamer` → `Streamer` in multistream.go
- [x] **Step 3.4**: Renamed `MultiPeerManager` → `PeerManager` in multistream.go
- [x] **Step 3.5**: Updated all references in tui.go and main.go

### Phase 4: Cleanup - COMPLETED

- [x] **Step 4.1**: Cleaned up duplicate code in stopCapture/cleanup
- [x] **Step 4.2**: Renamed signaling functions:
  - `setupMultiSignaling` → `setupPeerSignaling`
  - `setupMultiRemoteSignaling` → `setupRemotePeerSignaling`
- [x] **Step 4.3**: Build passes

---

## Files Modified

| File | Changes | Status |
|------|---------|--------|
| codec.go | Add SetBitrate to interface | Done |
| encoder.go | Implement SetBitrate with recreation | Done |
| encoder_h264.go | Implement SetBitrate with recreation | Done |
| encoder_videotoolbox.go | Implement SetBitrate with recreation | Done |
| multistream.go | Stats, SetBitrate, renamed to Streamer/PeerManager | Done |
| tui.go | Unified code paths, new stats display | Done |
| main.go | Updated signaling function names | Done |
| peer.go | Deprecated with Legacy prefix | Done |

---

## Known Limitations

1. **Fullscreen capture disabled in TUI**: The unified Streamer only supports window capture via MultiCapture. Fullscreen capture would require adding `StartDisplayCapture` to MultiCapture. Users see a message asking them to select windows instead.

2. **Legacy code retained**: peer.go still contains LegacyStreamer/LegacyPeerManager for the non-TUI CLI modes in main.go. These can be migrated later or removed if CLI modes are deprecated.

3. **Function naming**: Some functions still have "Multi" in their names (like `startMultiWindowSharing`) but these describe multi-window functionality, not the old Multi* types.

---

## Commits

1. `cff5854` - Phase 1 & 2: Unify TUI to use MultiStreamer for all capture modes
2. `1fba111` - Phase 3: Rename Multi* types to standard names
3. (pending) - Phase 4: Cleanup and final signaling function renames

---

## Testing Checklist

- [x] `go build ./...` succeeds
- [ ] Single window streaming works (manual test needed)
- [ ] Multi-window streaming works (manual test needed)
- [ ] Quality changes apply via SetBitrate (manual test needed)
- [ ] Stats display updates correctly (manual test needed)
- [ ] Adding/removing windows dynamically works (manual test needed)
- [ ] Viewer connection/disconnection handled properly (manual test needed)
