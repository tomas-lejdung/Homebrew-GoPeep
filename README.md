# GoPeep

P2P screen sharing for pair programming. Share your screen with anyone over the internet using WebRTC - video flows directly between peers, not through a server.

## Features

- **True P2P** - Video streams directly between you and viewers via WebRTC
- **No account needed** - Just share a URL
- **Window or fullscreen capture** - Choose what to share
- **Live quality switching** - Adjust bitrate on the fly (500kbps to 20Mbps)
- **Works over the internet** - Uses STUN for NAT traversal
- **TUI interface** - Beautiful terminal UI with Bubbletea

## Installation

### Homebrew (Recommended)

```bash
brew tap tomas-lejdung/gopeep https://github.com/tomas-lejdung/GoPeep
brew install gopeep
```

### Manual Installation

Requires Go 1.21+ and libvpx:

```bash
# Install dependencies
brew install libvpx

# Clone and build
git clone https://github.com/tomas-lejdung/GoPeep.git
cd GoPeep
make build-release
```

## Usage

```bash
# Launch TUI (default - uses remote signal server for internet sharing)
gopeep

# Share a specific window
gopeep --window "VS Code"

# Force local-only mode (same network only)
gopeep --local

# List available windows
gopeep --list
```

When you start sharing, you'll get a URL like:
```
https://gopeep.tineestudio.se/HAPPY-TIGER-42
```

Share this URL with anyone - they open it in a browser and see your screen!

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

Use number keys 1-7 in the TUI or `--quality <preset>` flag.

## TUI Controls

| Key | Action |
|-----|--------|
| Tab / ← → | Switch columns |
| ↑/↓ or j/k | Navigate |
| Enter/Space | Select |
| 1-7 | Quick quality select |
| i | Toggle stats |
| s | Stop sharing |
| r | Refresh windows |
| q | Quit |

## Self-Hosting the Signal Server

If you want to run your own signal server:

```bash
# Using Docker
docker run -d -p 8080:8080 tomaslejdung/gopeep-server

# Or build from source
go build -o gopeep-server ./cmd/server/
./gopeep-server --port 8080
```

Then point GoPeep to your server:

```bash
gopeep --signal wss://your-server.com
```

## Requirements

- macOS 12+ (uses ScreenCaptureKit)
- Screen Recording permission (prompted on first run)

## License

MIT
