# GoPeep Improvement Plan

## Tasks Overview

| # | Task | Status | Effort | Files to Modify |
|---|------|--------|--------|-----------------|
| 1 | ~~Latency Display~~ | DONE | - | Already in viewer.html |
| 2 | ~~BGRA→I420 Direct Conversion~~ | DONE | Low | `capture_darwin.go`, `encoder.go`, `encoder_h264.go`, `encoder_videotoolbox.go`, `peer.go` |
| 3 | ~~Password-Protected Rooms~~ | DONE | Medium | `room.go`, `signal.go`, `cmd/server/main.go`, `tui.go`, `viewer.html` (x2) |
| 4 | ~~Auto-Reconnect for Sharer~~ | DONE | Low | `tui.go`, `main.go` |

---

## Task 1: Latency Display - DONE ✓

Already implemented in viewer.html with color coding:
- Green: < 100ms
- Yellow: 100-200ms  
- Red: > 200ms

---

## Task 2: BGRA→I420 Direct Conversion - DONE

**Goal:** Eliminate BGRA→RGBA→I420, do BGRA→I420 directly

**Changes completed:**
1. `capture_darwin.go` - Added `BGRAFrame` struct and `GetLatestFrameBGRA()` function
2. `encoder.go` - C code accepts BGRA directly, added `EncodeBGRAFrame()` method to VPXEncoder
3. `encoder_h264.go` - C code accepts BGRA directly, added `EncodeBGRAFrame()` method
4. `encoder_videotoolbox.go` - Added `EncodeBGRAFrame()` method
5. `codec.go` - Added `EncodeBGRAFrame()` to VideoEncoder interface
6. `peer.go` - Changed Streamer to use `GetLatestFrameBGRA()` and `EncodeBGRAFrame()`
7. `tui.go` - Updated `SetCaptureFunc` calls to use `GetLatestFrameBGRA()`
8. `main.go` - Updated `SetCaptureFunc` calls to use `GetLatestFrameBGRA()`

---

## Task 3: Password-Protected Rooms - DONE

**Goal:** Press `P` to toggle password, generates readable password (word-NN format)

**Changes completed:**
1. `room.go` - Added `GeneratePassword()` function with word-NN format
2. `signal.go` - Added `Password` field to SignalMessage and Room, validates on viewer join
3. `cmd/server/main.go` - Same password logic for remote server
4. `tui.go` - Added `P` key toggle (before sharing), shows password in status bar
5. `viewer.html` - Added password prompt dialog with form handling
6. `cmd/server/viewer.html` - Same password dialog

**Password format:** `word-NN` (e.g., `tiger-42`)

**Usage:**
- Press `P` before sharing to enable password protection
- Password is auto-generated and displayed in the status bar
- Viewers see a password prompt dialog when joining a protected room

---

## Task 4: Auto-Reconnect for Sharer - DONE

**Goal:** Auto-reconnect WebSocket if signal server connection drops

**Changes completed:**
1. `main.go` - Added `setupRemoteSignalingWithCallback()` for disconnect notification
2. `tui.go` - Added reconnection logic:
   - New message types: `reconnectMsg`, `reconnectedMsg`, `reconnectFailedMsg`
   - Added `wsDisconnected` pointer flag for goroutine communication
   - Added `attemptReconnect()` method with exponential backoff (1s, 2s, 4s... max 30s)
   - Status bar shows `[RECONNECTING n/10]` during reconnection
   - Auto-detects disconnect via callback from signaling goroutine
   - Re-joins as sharer on successful reconnection (preserving password)

---

## All Tasks Complete!

All improvement tasks have been completed. To use the new features:

### Build Commands
```bash
# Main client
cd /Users/tomaslejdung/localdev/gopeep
go build .

# Signal server
cd cmd/server && go build .
```

### New Features Summary

**Password Protection:**
- Press `P` before starting to share to enable password
- Auto-generates password like `tiger-42`
- Displayed in status bar next to URL
- Viewers see password prompt dialog

**Auto-Reconnect:**
- Automatically reconnects to remote signal server if connection drops
- Shows `[RECONNECTING n/10]` in status bar
- Uses exponential backoff: 1s, 2s, 4s, 8s... up to 30s
- Max 10 attempts before giving up

**Performance:**
- BGRA→I420 direct conversion (no unnecessary RGBA step)
- Reduced memory allocations in capture pipeline
