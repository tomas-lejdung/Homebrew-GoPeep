# GoPeep

A single-binary P2P screen/window sharing tool for pair programming. Share windows or full screen from macOS, viewers connect via browser.

## Status

**Working MVP** - Core functionality complete and tested.

## Quick Start

```bash
# Build
CGO_ENABLED=1 go build -o gopeep .

# Launch TUI (recommended)
./gopeep

# Share specific window by name (non-interactive)
./gopeep --window "VS Code"

# List available windows
./gopeep --list

# Run signal server only
./gopeep --serve --port 8080
```

## Features

- **Two-column TUI** - Select sources (fullscreen or windows) and quality presets
- **Fullscreen capture** - Share entire primary display
- **Window capture** - Share specific application windows
- **7 quality presets** - From 500 kbps to 20 Mbps
- **Live quality switching** - Change bitrate while streaming
- **Room codes** - Easy-to-share ADJECTIVE-NOUN-NUMBER format
- **Browser viewer** - Zero installation for viewers
- **WebRTC P2P** - Direct peer-to-peer video streaming

## TUI Interface

```
GoPeep - P2P Screen Sharing

Room: CALM-RIVER-42  URL: http://192.168.1.5:8080/CALM-RIVER-42
Sharing: VS Code - main.go  Quality: High (3 Mbps)  Viewers: 1

╭─ Sources ──────────────────────────────╮ ╭─ Quality ──────────────────╮
│ > [F] Fullscreen (Primary Display)     │ │   Low (500 kbps)           │
│   [1] Terminal - ~/localdev/gopeep     │ │   Medium (1.5 Mbps)        │
│   [2] VS Code - main.go *              │ │ > High (3 Mbps)            │
│   [3] Safari - GitHub                  │ │   Ultra (6 Mbps)           │
│   [4] Slack - #general                 │ │   Extreme (10 Mbps)        │
╰────────────────────────────────────────╯ │   Insane (15 Mbps)         │
                                           │   Max (20 Mbps)            │
                                           ╰────────────────────────────╯

tab/←→ switch column • ↑/↓ navigate • enter select • 1-7 quality • s stop • r refresh • q quit
```

## TUI Controls

| Key | Action |
|-----|--------|
| `Tab` / `←` / `→` / `h` / `l` | Switch between columns |
| `↑` / `↓` / `j` / `k` | Navigate within column |
| `Enter` / `Space` | Select source or apply quality |
| `1-7` | Quick-select quality preset |
| `s` | Stop sharing |
| `r` | Refresh window list |
| `q` | Quit |

## Quality Presets

| # | Name | Bitrate | Use Case |
|---|------|---------|----------|
| 1 | Low | 500 kbps | Mobile/slow connections |
| 2 | Medium | 1.5 Mbps | Balanced (default) |
| 3 | High | 3 Mbps | Good connections |
| 4 | Ultra | 6 Mbps | Fast connections |
| 5 | Extreme | 10 Mbps | Very fast connections |
| 6 | Insane | 15 Mbps | LAN/local network |
| 7 | Max | 20 Mbps | Maximum quality |

## CLI Flags

```
Usage: gopeep [options]

Options:
  --window, -w <name>    Share window matching name (non-interactive mode)
  --list, -l             List available windows and exit
  --serve, -s            Run as signal server only
  --port, -p <port>      Signal server port (default: 8080)
  --signal <url>         Custom signal server URL
  --fps <rate>           Target framerate (default: 30)
  --quality <preset>     Encoding quality: low, medium, high, ultra, extreme, insane, max
  --help, -h             Show help
```

## Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│                         gopeep binary                           │
│  ┌──────────────┐  ┌──────────┐  ┌──────────┐  ┌─────────────┐ │
│  │   Capture    │  │ Encoder  │  │  WebRTC  │  │   Signal    │ │
│  │ScreenCapture │─►│  (VP8)   │─►│  Peer    │◄►│   Server    │ │
│  │    Kit       │  │  libvpx  │  │  Manager │  │ (WebSocket) │ │
│  └──────────────┘  └──────────┘  └──────────┘  └──────┬──────┘ │
│                                                        │        │
│        ┌─────────────┐              Embedded: viewer.html       │
│        │  Bubbletea  │                                 │        │
│        │     TUI     │                                 │        │
│        └─────────────┘                                 │        │
└────────────────────────────────────────────────────────┼────────┘
                                                         │
                     P2P Video Stream                    │ Signaling
                          (UDP)                          │
                            │                            │
                            ▼                            ▼
                     ┌─────────────────────────────────────┐
                     │           Browser Viewer            │
                     │  - Connects via room code in URL    │
                     │  - Receives WebRTC video stream     │
                     │  - Zero installation required       │
                     └─────────────────────────────────────┘
```

## Project Structure

```
gopeep/
├── main.go                 # Entry point, CLI flags, mode switching
├── tui.go                  # Bubbletea TUI with two-column layout
├── capture_darwin.go       # macOS ScreenCaptureKit (CGO)
├── encoder.go              # VP8 encoding via libvpx (CGO)
├── peer.go                 # WebRTC peer management + Streamer
├── signal.go               # WebSocket signaling server
├── room.go                 # Room code generation (ADJECTIVE-NOUN-NN)
├── quality.go              # Quality preset definitions
├── viewer.html             # Embedded web viewer (go:embed)
├── go.mod
├── go.sum
├── Makefile
└── GOPEEP_SPEC.md
```

## Technical Implementation

### Window/Display Capture (macOS)

Uses **ScreenCaptureKit** (modern macOS API, not deprecated CGWindowListCreateImage):

```objc
// Display capture
SCContentFilter* filter = [[SCContentFilter alloc] initWithDisplay:display excludingWindows:@[]];

// Window capture  
SCContentFilter* filter = [[SCContentFilter alloc] initWithDesktopIndependentWindow:window];

// Stream configuration
SCStreamConfiguration* config = [[SCStreamConfiguration alloc] init];
config.width = width;
config.height = height;
config.minimumFrameInterval = CMTimeMake(1, fps);
config.pixelFormat = kCVPixelFormatType_32BGRA;
config.showsCursor = YES;
```

### Video Pipeline

```
ScreenCaptureKit → BGRA frames → RGBA conversion → VP8 encode → WebRTC track
     (30fps)                                         (libvpx)     (pion/webrtc)
```

### VP8 Encoding

Uses **libvpx** via CGO for real VP8 encoding:

```c
vpx_codec_enc_cfg_t cfg;
cfg.g_w = width;
cfg.g_h = height;
cfg.rc_target_bitrate = bitrate;  // Configurable 500-20000 kbps
cfg.rc_end_usage = VPX_CBR;       // Constant bitrate
cfg.g_lag_in_frames = 0;          // Real-time mode
```

### Signaling Protocol

JSON messages over WebSocket with peer ID tracking:

```json
// Join room
{"type": "join", "room": "BLUE-FROG-42", "role": "viewer"}

// SDP Exchange (with peer ID for multi-viewer support)
{"type": "offer", "sdp": "...", "peerId": "viewer-1"}
{"type": "answer", "sdp": "...", "peerId": "viewer-1"}

// ICE Candidates
{"type": "ice", "candidate": "...", "peerId": "viewer-1"}
```

### Room Codes

Word-based for easy verbal sharing:

```
ADJECTIVE-NOUN-NUMBER
Examples: BLUE-FROG-42, QUICK-TIGER-7, CALM-RIVER-99
```

## Dependencies

```go
require (
    github.com/pion/webrtc/v3      // WebRTC implementation
    github.com/gorilla/websocket   // WebSocket signaling
    github.com/charmbracelet/bubbletea  // TUI framework
    github.com/charmbracelet/lipgloss   // TUI styling
)
```

System dependencies:
- **libvpx** - `brew install libvpx`
- **macOS 12+** - For ScreenCaptureKit
- **Screen Recording permission** - Required for capture

## Build

```bash
# Development build
CGO_ENABLED=1 go build -o gopeep .

# Release build (smaller binary)
CGO_ENABLED=1 go build -ldflags="-s -w" -o gopeep .
```

## Implementation Status

### Phase 1: Core MVP ✅

- [x] CLI with flag parsing
- [x] Full-screen capture via ScreenCaptureKit
- [x] VP8 encoding via libvpx
- [x] WebRTC peer setup with pion/webrtc
- [x] WebSocket signaling server
- [x] Embedded web viewer
- [x] Room code generation

### Phase 2: Window Selection ✅

- [x] macOS window enumeration via ScreenCaptureKit
- [x] Window-specific capture
- [x] Interactive TUI with Bubbletea
- [x] Partial window name matching (`--window` flag)

### Phase 3: Polish ✅

- [x] Two-column TUI layout (sources + quality)
- [x] Multiple viewer support (proper peer ID routing)
- [x] 7 quality presets (500 kbps to 20 Mbps)
- [x] Live quality switching while streaming
- [x] Graceful window switching (room code persists)
- [x] Connection status display
- [x] Viewer count tracking

### Phase 4: Distribution

- [x] Makefile with build targets
- [ ] Pre-built binaries (GitHub releases)
- [ ] Homebrew formula
- [ ] README with GIFs/screenshots

---

## Roadmap: Planned Features

### Phase 5: Better Web UI (Video Player) ✅

**Goal:** Transform the basic viewer into a proper video player experience.

- [x] **Fullscreen support**
  - Fullscreen button in player controls
  - `F` keyboard shortcut for fullscreen toggle
  - `Esc` to exit fullscreen
  - Double-click video to toggle fullscreen
  
- [x] **Picture-in-Picture**
  - PiP button for floating window
  - `P` keyboard shortcut
  
- [x] **Video scaling options**
  - Fit (maintain aspect ratio, fit in window)
  - Fill (cover entire window, may crop)
  - Native (1:1 pixel mapping)
  - `1`/`2`/`3` keyboard shortcuts
  
- [x] **Cleaner design**
  - Polished dark theme with glassmorphism
  - Fade-in/out controls on hover
  - Auto-hiding cursor
  - Loading spinner while connecting
  
- [x] **Keyboard shortcuts help**
  - `?` to show/hide shortcuts overlay

### Phase 6: Web UI Status & Stats ✅

**Goal:** Show connection quality and stream information to viewers.

- [x] **Connection status indicator**
  - Colored dot: green (connected), yellow (connecting), red (error)
  - Status text in top bar
  
- [x] **Stream information in top bar**
  - Resolution (e.g., 1920x1080)
  - Frame rate (e.g., 30 fps)
  - Bitrate (calculated from received bytes)
  
- [x] **Stats panel** (`I` key to toggle)
  - Connection type (P2P/NAT/Relay)
  - Resolution, Frame Rate, Bitrate
  - Latency (RTT)
  - Packets Lost (with percentage)
  - Jitter
  - Color-coded values (green/yellow/red)
  
- [x] **Auto-hiding UI**
  - Hides after 3 seconds of inactivity
  - Shows on mouse move or key press

### Phase 7: Enhanced TUI Status ✅

**Goal:** Provide detailed diagnostics and per-viewer information in the TUI.

- [x] **Real-time encoding stats**
  - Actual bitrate (vs target)
  - Current FPS (actual/target)
  - Capture resolution
  
- [x] **Data statistics**
  - Total frames sent
  - Total data transferred (KB/MB/GB)
  
- [x] **Per-viewer status**
  - List of connected viewers with peer IDs
  - Connection state per viewer
  - Connection duration
  - Connection type (P2P/TURN)
  
- [x] **Session info**
  - Uptime (duration of sharing)
  
- [x] **Stats panel toggle**
  - `i` key to show/hide detailed stats

### Phase 8: Over the Network (TURN Support) ✅

**Goal:** Enable sharing over the internet, not just LAN.

- [x] **TURN server configuration**
  - `--turn <url>` flag for custom TURN server
  - `--turn-user` and `--turn-pass` for credentials
  
- [x] **Default STUN servers**
  - Multiple public Google STUN servers bundled
  
- [x] **Connection type indicator**
  - Shows Direct (P2P), NAT (P2P), or Relay (TURN)
  - Displayed in both TUI stats and web UI stats panel
  
- [x] **ICE transport policy**
  - `--force-relay` flag to force TURN relay (disable P2P)
  - Useful for privacy or NAT traversal testing

### Phase 9: Multi-Window Sharing (Browser)

**Goal:** Share multiple windows, displayed in a grid/layout in the browser.

- [ ] **Multi-select in TUI**
  - `Space` to toggle window selection
  - `Enter` to start sharing selected windows
  - Visual indicator for selected windows
  
- [ ] **Multiple video tracks**
  - Each window = separate WebRTC video track
  - Independent encoding per window
  
- [ ] **Browser grid layout**
  - Auto-arrange windows in grid
  - Drag to resize/reposition
  - Click to focus/maximize single window
  - Save layout preference
  
- [ ] **Window labels**
  - Show window title on each stream
  - Optional: show which app
  
- [ ] **Performance considerations**
  - Cap total bitrate across all windows
  - Reduce quality/fps for non-focused windows
  - Pause minimized windows

### Phase 10: Native Viewer Client (CoScreen-like)

**Goal:** Desktop app for viewers that spawns real OS windows for each shared window.

- [ ] **Native macOS client**
  - Swift/SwiftUI app or Go with native bindings
  - Receives WebRTC stream(s)
  - Creates native window per shared window
  
- [ ] **Window management**
  - Spawn windows at original relative positions
  - Resize windows independently
  - Remember window positions
  
- [ ] **Window chrome options**
  - Minimal (borderless)
  - Standard (with title bar)
  - Show source window title
  
- [ ] **Connection handling**
  - Room code input dialog
  - Recent rooms list
  - Auto-reconnect
  
- [ ] **System integration**
  - Menu bar icon
  - Keyboard shortcut to connect
  - Notification on disconnect
  
- [ ] **Cross-platform** (future)
  - Windows client
  - Linux client

---

## Future Ideas (Beyond Roadmap)

- Linux/Windows sharer support
- Audio sharing
- Remote cursor overlay (show where sharer is pointing)
- Annotation/drawing tools
- Recording to file
- Bidirectional sharing (viewer can share back)
- Multiple display support
- Mobile viewer app
- mDNS LAN discovery (zero-config)
- End-to-end encryption
- Password-protected rooms

## Known Issues

- HTTP server doesn't have clean shutdown (minor)
- Window list may briefly flicker during fullscreen capture (mitigated)

## License

MIT
