# GoPeep

P2P screen sharing for pair programming. Share your screen with anyone over the internet using WebRTC - video flows directly between peers, not through a server.

## Features

- **True P2P** - Video streams directly between you and viewers via WebRTC
- **No account needed** - Just share a room code or URL
- **Multi-window capture** - Share multiple windows simultaneously
- **Live quality switching** - Adjust bitrate on the fly (500kbps to 20Mbps)
- **Works over the internet** - Uses STUN for NAT traversal
- **TUI interface** - Terminal UI with Bubbletea

## Installation

### Homebrew (Recommended)

```bash
brew tap tomas-lejdung/gopeep
brew install gopeep
```

### Manual Installation

Requires Go 1.24+ and libvpx:

```bash
# Install dependencies
brew install libvpx

# Clone and build
git clone https://github.com/tomaslejdung/gopeep.git
cd gopeep
go build -o gopeep .
```

## Usage

```bash
# Launch TUI (connects to remote signal server)
gopeep

# List available windows
gopeep --list
```

When you start sharing, you'll get a room code like `HAPPY-TIGER-42`. Share this with viewers - they can join at:
```
https://gopeep.tineestudio.se/HAPPY-TIGER-42
```

### Local Development

For local development, run the signal server separately:

```bash
# Terminal 1: Start local signal server
gopeep --serve

# Terminal 2: Connect to local server
gopeep --local
```

## How It Works

```
┌─────────────────────────────────────────────────────────────────────────┐
│                                                                         │
│   Your Mac                 Signal Server              Friend's Browser  │
│   (sharing)               (tiny JSON only)              (viewing)       │
│       │                         │                           │           │
│       │◄──── WebSocket ────────►│◄──── WebSocket ──────────►│           │
│       │   (signaling only)      │     (signaling only)      │           │
│       │                                                     │           │
│       │◄═══════════════ Direct P2P Video ══════════════════►│           │
│                        (via STUN/WebRTC)                                │
│                                                                         │
└─────────────────────────────────────────────────────────────────────────┘
```

The signal server only handles the initial handshake (~few KB of JSON). All video flows directly between peers.

## Quality Presets

| Preset  | Bitrate  | Use Case              |
|---------|----------|-----------------------|
| low     | 500 kbps | Mobile/slow connections |
| medium  | 1.5 Mbps | Balanced (default)    |
| high    | 3 Mbps   | Good connections      |
| ultra   | 6 Mbps   | Fast connections      |
| extreme | 10 Mbps  | Very fast connections |
| insane  | 15 Mbps  | LAN/local network     |
| max     | 20 Mbps  | Maximum quality       |

Navigate to the Quality column in the TUI and press Enter to change.

## TUI Controls

| Key | Action |
|-----|--------|
| Tab / ← → | Switch columns (Sources, Quality, FPS, Codec) |
| ↑/↓ or j/k | Navigate |
| Enter/Space | Select/toggle |
| 1-7 | Quick window select |
| f | Toggle fullscreen capture |
| s | Stop sharing |
| r | Refresh window list |
| c | Copy share URL to clipboard |
| p | Toggle password protection |
| a | Toggle adaptive bitrate |
| A | Toggle auto-share mode |
| i | Toggle stats display |
| q | Toggle quality mode |
| Esc | Quit |

## Command Line Options

```
--list, -l           List available windows and exit
--serve, -s          Run as signal server only
--local              Use local signal server (ws://localhost:8080)
--signal <url>       Custom signal server URL
--port, -p <port>    Signal server port (default: 8080)
--fps <rate>         Target framerate (default: 30)
--quality <preset>   Encoding quality preset
--help, -h           Show help

Network Options:
--turn <url>         TURN server URL
--turn-user <user>   TURN server username
--turn-pass <pass>   TURN server password
--force-relay        Force TURN relay (disable direct P2P)
```

## Self-Hosting the Signal Server

Run your own signal server:

```bash
# Option 1: Use gopeep directly
gopeep --serve --port 8080

# Option 2: Build standalone server
go build -o gopeep-server ./cmd/server/
./gopeep-server --port 8080
```

Then point clients to your server:

```bash
gopeep --signal wss://your-server.com
```

## Requirements

- macOS 12+ (uses ScreenCaptureKit)
- Screen Recording permission (prompted on first run)

## License

MIT
