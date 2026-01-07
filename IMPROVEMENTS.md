# GoPeep Improvement Plan

## Tasks Overview

| # | Task | Status | Effort | Files to Modify |
|---|------|--------|--------|-----------------|
| 1 | ~~Latency Display~~ | DONE | - | Already in viewer.html |
| 2 | BGRA→I420 Direct Conversion | TODO | Low | `capture_darwin.go`, `encoder.go`, `encoder_h264.go`, `encoder_videotoolbox.go`, `peer.go` |
| 3 | Password-Protected Rooms | TODO | Medium | `room.go`, `signal.go`, `cmd/server/main.go`, `tui.go`, `viewer.html` (x2) |
| 4 | Auto-Reconnect for Sharer | TODO | Low | `tui.go` |

---

## Task 1: Latency Display - DONE ✓

Already implemented in viewer.html with color coding:
- Green: < 100ms
- Yellow: 100-200ms  
- Red: > 200ms

---

## Task 2: BGRA→I420 Direct Conversion

**Goal:** Eliminate BGRA→RGBA→I420, do BGRA→I420 directly

**Changes:**
1. `capture_darwin.go` - Return BGRA directly instead of converting to RGBA
2. `encoder.go` - Modify C code to convert BGRA→I420 (swap R and B in conversion)
3. `encoder_h264.go` - Same BGRA→I420 change
4. `encoder_videotoolbox.go` - Same BGRA→I420 change
5. `peer.go` - Update to use BGRA data

---

## Task 3: Password-Protected Rooms

**Goal:** Press `P` to toggle password, generates readable password (word-NN format)

**Changes:**
1. `room.go` - Add `GeneratePassword()` function
2. `signal.go` - Add password field to Room, validate on viewer join
3. `cmd/server/main.go` - Same password logic for remote server
4. `tui.go` - Add `P` key toggle, show password in status bar
5. `viewer.html` (both) - Add password prompt dialog

**Password format:** `word-NN` (e.g., `tiger-42`)

---

## Task 4: Auto-Reconnect for Sharer

**Goal:** Auto-reconnect WebSocket if signal server connection drops

**Changes:**
1. `tui.go` - Add reconnection logic with exponential backoff
   - Detect WebSocket disconnect
   - Show "Reconnecting..." status
   - Retry with backoff (1s, 2s, 4s, 8s, max 30s)
   - Re-join as sharer on success

---

## Server Rebuild Required

After completing these tasks, rebuild:
- Main client: `go build .`
- Signal server: `cd cmd/server && go build .`
- Redeploy Docker image if using remote server
