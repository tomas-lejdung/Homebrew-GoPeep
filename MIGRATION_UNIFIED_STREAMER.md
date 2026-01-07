# Migration: Unified Streamer Architecture - COMPLETED

## Summary

The unified streamer migration is complete. All code now uses a single `Streamer` and `PeerManager` implementation from `multistream.go`. The legacy implementations have been removed.

## Final Architecture

```
multistream.go:
  - Streamer: Handles capture-encode-stream for one or more windows
  - PeerManager: Manages WebRTC connections with multi-track support
  - StreamPipeline: Per-window encoding pipeline with stats

peer.go:
  - defaultICEServers: STUN server list (shared)
  - ViewerInfo: Viewer connection info (shared)
  - ICEConfig: ICE/TURN configuration (shared)

tui.go:
  - Uses Streamer/PeerManager for all capture modes
  - Single-window = multi-window with N=1
```

## What Was Removed

### From peer.go (~665 lines removed):
- `LegacyPeerManager` - old single-track peer manager
- `LegacyStreamer` - old single-window streamer
- `StreamStats` - old stats struct (replaced by StreamPipelineStats)
- `ViewerPeer` - test/debug viewer peer
- All legacy constructor functions

### From main.go (~560 lines removed):
- `runShareMode()` - CLI mode for local sharing
- `runShareModeWithFallback()` - CLI mode with remote fallback
- `runRemoteShareMode()` - CLI mode for remote sharing
- `setupSignaling()` - legacy local signaling
- `setupRemoteSignaling()` - legacy remote signaling
- `setupRemoteSignalingWithCallback()` - legacy remote signaling
- `interactiveWindowPicker()` - CLI window picker
- `--window` / `-w` flag and related code

## Commits

1. `cff5854` - Phase 1 & 2: Add infrastructure and unify TUI
2. `1fba111` - Phase 3: Rename Multi* types to standard names
3. `6dea56a` - Phase 4: Cleanup signaling function names
4. `4b9a0d5` - Remove legacy CLI modes and old implementations

## Lines of Code

| File | Before | After | Removed |
|------|--------|-------|---------|
| peer.go | ~695 | ~30 | ~665 |
| main.go | ~1030 | ~470 | ~560 |
| **Total** | - | - | **~1225** |

## Features

- Single-window and multi-window streaming use the same code path
- Dynamic bitrate changes via `Streamer.SetBitrate()` (no restart needed)
- Per-stream statistics with `Streamer.GetStats()`
- Compact stats display showing each stream's resolution, FPS, bitrate, data sent
- All encoders (VP8, VP9, H264, VideoToolbox) support runtime bitrate changes

## Known Limitations

1. **Fullscreen capture disabled**: MultiCapture only supports window capture. Users are asked to select individual windows instead.

2. **TUI-only**: All screen sharing now goes through the TUI. The old CLI `--window` mode has been removed.

---

*This migration document can be deleted now that the migration is complete.*
