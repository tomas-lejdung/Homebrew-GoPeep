# AGENTS.md - Coding Agent Instructions for GoPeep

## Project Overview

GoPeep is a P2P screen sharing application for macOS. It uses WebRTC for peer-to-peer video streaming, ScreenCaptureKit for window capture, and VideoToolbox/VP8/VP9 for encoding.

**Tech Stack:** Go 1.24+ with CGO, WebRTC (Pion), TUI (Bubbletea/Lipgloss), WebSocket (Gorilla)

## Build Commands

```bash
go build -o gopeep .                    # Build main app (requires macOS, libvpx)
go build -o gopeep-server ./cmd/server/ # Build signal server (cross-platform)
brew install libvpx                     # Install VP8/VP9 dependency
```

## Testing

```bash
go test ./...                           # Run all tests
go test -v ./...                        # Verbose output
go test -v -run TestFunctionName ./...  # Single test by name
go test -v ./pkg/signal/...             # Tests in specific package
go test -race ./...                     # Race detection
go test -coverprofile=coverage.out ./...# Coverage report
```

## Linting

```bash
go fmt ./...    # Format code (required before commits)
go vet ./...    # Static analysis
```

## Code Style Guidelines

### Import Organization

Group imports: 1) stdlib, 2) third-party, 3) local - separated by blank lines.

```go
import (
    "fmt"
    "sync"

    "github.com/pion/webrtc/v3"
    tea "github.com/charmbracelet/bubbletea"

    sig "github.com/tomaslejdung/gopeep/pkg/signal"
)
```

**Aliases:** `tea` for bubbletea, `sig` for signal package.

### Naming Conventions

| Element | Convention | Example |
|---------|------------|---------|
| Exported | PascalCase | `NewPeerManager`, `StreamInfo` |
| Unexported | camelCase | `parseFlags`, `roomCode` |
| Constants | PascalCase | `MaxCaptureInstances` |
| Acronyms | All caps | `SDP`, `ICE`, `WebRTC` |
| Receivers | Short (1-2 chars) | `(m model)`, `(pm *PeerManager)` |

### Error Handling

- Return errors, don't panic
- Wrap with context: `fmt.Errorf("context: %w", err)`
- Check immediately, use early returns

```go
func (pm *PeerManager) AddTrack(info StreamTrackInfo) error {
    if info.Track == nil {
        return fmt.Errorf("track cannot be nil")
    }
    pm.mu.Lock()
    defer pm.mu.Unlock()
    pm.tracks[info.TrackID] = &info
    return nil
}
```

### CGO Patterns

```go
/*
#cgo CFLAGS: -x objective-c -fmodules
#cgo LDFLAGS: -framework CoreGraphics -framework AppKit
*/
import "C"
```

- Use `@autoreleasepool` in Objective-C callbacks
- Release CF objects with `CFRelease()`
- Use `dispatch_semaphore` for async-to-sync bridging

### Concurrency

- `sync.Mutex` for locking, `sync.RWMutex` when reads dominate
- Name mutex fields `mu`, use `defer mu.Unlock()` immediately after `Lock()`
- Use `atomic` for simple flags/counters

### Comments

- Godoc-style for exported items: `// FunctionName does X.`
- Use `// TODO:` and `// FIXME:` for annotations

## Project Structure

```
gopeep/
├── main.go                 # Entry point, CLI parsing
├── tui.go                  # Bubbletea TUI
├── multistream.go          # WebRTC peer management
├── capture_darwin.go       # Window listing (CGO/ObjC)
├── capture_multi_darwin.go # Multi-window capture (CGO/ObjC)
├── encoder.go              # VP8/VP9 encoding (CGO/libvpx)
├── encoder_videotoolbox.go # Hardware H.264 encoding
├── codec.go                # Codec abstraction
├── quality.go              # Quality presets
├── pkg/signal/             # WebSocket signaling
│   ├── server.go           # Server implementation
│   ├── handlers.go         # Message handlers
│   ├── message.go          # Message types
│   └── viewer.html         # Embedded viewer
└── cmd/server/             # Standalone server binary
```

## Platform Notes

- **macOS-only** (ScreenCaptureKit, VideoToolbox)
- Requires macOS 12+
- Screen Recording permission required
- CGO required (no `CGO_ENABLED=0`)

## Common Patterns

### Bubbletea TUI

```go
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
    switch msg := msg.(type) {
    case tea.KeyMsg:
        // Handle keys
    }
    return m, nil
}
```

### WebRTC Callbacks

```go
pm.onICE = func(peerID, candidate string) { /* send ICE */ }
pm.onConnected = func(peerID string) { log.Printf("connected: %s", peerID) }
```

## Debugging

```bash
# Check gopeep-debug.log for runtime logs
# Common issues:
# - "no screen recording permission" -> System Preferences > Privacy
# - CGO errors -> xcode-select --install
# - libvpx errors -> brew install libvpx
```
