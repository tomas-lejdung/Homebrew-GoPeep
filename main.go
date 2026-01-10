package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"runtime"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/tomaslejdung/gopeep/pkg/overlay"
	sig "github.com/tomaslejdung/gopeep/pkg/signal"
)

func init() {
	// Lock main goroutine to the main OS thread BEFORE main() runs.
	// This must happen in init() because Go's scheduler may move goroutines
	// between threads, and we need to ensure we stay on thread 0 (macOS main thread)
	// for AppKit/NSWindow operations to work correctly.
	runtime.LockOSThread()
}

// Note: TUI mode uses RunTUI() from tui.go

// DefaultSignalServer is the default remote signal server for P2P initialization
const DefaultSignalServer = "wss://gopeep.tineestudio.se"

// LocalSignalServer is the URL for local signal server
const LocalSignalServer = "ws://localhost:8080"

// Config holds runtime configuration
type Config struct {
	ServeMode   bool
	Port        int
	ListWindows bool
	FPS         int
	Quality     string
	SignalURL   string
	Help        bool

	// TURN server configuration
	TURNServer string
	TURNUser   string
	TURNPass   string
	ForceRelay bool // Force TURN relay (no direct P2P)
}

func parseFlags() Config {
	config := Config{}
	var localMode bool

	flag.BoolVar(&config.ServeMode, "serve", false, "Run as signal server only")
	flag.BoolVar(&config.ServeMode, "s", false, "Run as signal server only (shorthand)")

	flag.IntVar(&config.Port, "port", 8080, "Signal server port")
	flag.IntVar(&config.Port, "p", 8080, "Signal server port (shorthand)")

	flag.BoolVar(&config.ListWindows, "list", false, "List available windows and exit")
	flag.BoolVar(&config.ListWindows, "l", false, "List available windows (shorthand)")

	flag.IntVar(&config.FPS, "fps", 30, "Target framerate")

	flag.StringVar(&config.Quality, "quality", "med", "Encoding quality (low|med|hi)")

	flag.StringVar(&config.SignalURL, "signal", "", "Custom signal server URL (overrides default)")
	flag.BoolVar(&localMode, "local", false, "Use local signal server (ws://localhost:8080)")

	// TURN server flags
	flag.StringVar(&config.TURNServer, "turn", "", "TURN server URL (e.g., turn:turn.example.com:3478)")
	flag.StringVar(&config.TURNUser, "turn-user", "", "TURN server username")
	flag.StringVar(&config.TURNPass, "turn-pass", "", "TURN server password")
	flag.BoolVar(&config.ForceRelay, "force-relay", false, "Force TURN relay (disable direct P2P)")

	flag.BoolVar(&config.Help, "help", false, "Show help")
	flag.BoolVar(&config.Help, "h", false, "Show help (shorthand)")

	flag.Parse()

	// --local sets SignalURL to local server
	if localMode {
		config.SignalURL = LocalSignalServer
	}

	return config
}

func printHelp() {
	fmt.Println(`GoPeep - P2P Screen Sharing for Pair Programming

Usage: gopeep [options]

By default, GoPeep connects to the remote signal server at:
  ` + DefaultSignalServer + `

This allows P2P connections over the internet.

Options:
  --list, -l             List available windows and exit
  --local                Use local signal server (` + LocalSignalServer + `)
  --signal <url>         Custom signal server URL (overrides default)
  --serve, -s            Run as signal server only
  --port, -p <port>      Signal server port (default: 8080)
  --fps <rate>           Target framerate (default: 30)
  --quality <preset>     Encoding quality: low, medium, high, ultra, extreme, insane, max
  --help, -h             Show help

Network Options:
  --turn <url>           TURN server URL (e.g., turn:turn.example.com:3478)
  --turn-user <user>     TURN server username
  --turn-pass <pass>     TURN server password
  --force-relay          Force TURN relay (disable direct P2P connections)

Quality Presets:
  low      500 kbps   - Mobile/slow connections
  medium   1.5 Mbps   - Balanced (default)
  high     3 Mbps     - Good connections
  ultra    6 Mbps     - Fast connections
  extreme  10 Mbps    - Very fast connections
  insane   15 Mbps    - LAN/local network
  max      20 Mbps    - Maximum quality

Examples:
  gopeep                     # Uses remote signal server
  gopeep --serve             # Run local signal server
  gopeep --local             # Connect to local signal server
  gopeep --list              # List available windows

TUI Controls:
  Tab / ← →     Switch between Sources and Quality columns
  ↑/↓ or j/k    Navigate within column
  Enter/Space   Select source or apply quality
  Space         Toggle window selection (multi-window mode)
  1-7           Quick-select quality preset
  i             Toggle stats panel
  s             Stop sharing
  r             Refresh window list
  q             Quit`)
}

func main() {
	config := parseFlags()

	if config.Help {
		printHelp()
		return
	}

	// List windows mode
	if config.ListWindows {
		listWindowsAndExit()
		return
	}

	// Server-only mode
	if config.ServeMode {
		runSignalServer(config.Port)
		return
	}

	// Determine signal URL: use default if not specified
	if config.SignalURL == "" {
		config.SignalURL = DefaultSignalServer
	}

	// Check screen recording permission on main thread (required for AppKit APIs)
	if !HasScreenRecordingPermission() {
		fmt.Println("Screen Recording permission required.")
		fmt.Println("Please grant permission in:")
		fmt.Println("  System Preferences > Security & Privacy > Privacy > Screen Recording")
		fmt.Println()
		fmt.Println("After granting permission, restart gopeep.")
		return
	}

	// TUI mode - run TUI in background, main run loop on main thread
	// This is required because macOS AppKit needs the main thread to service
	// the main dispatch queue for overlay window operations.
	done := make(chan error, 1)
	go func() {
		done <- RunTUI(config)
	}()

	// Run the macOS main run loop on the main thread (this goroutine).
	// This services dispatch_async calls to the main queue.
	// It will be stopped when the TUI exits.
	go func() {
		err := <-done
		overlay.StopMainRunLoop()
		if err != nil {
			log.Fatalf("TUI error: %v", err)
		}
	}()

	overlay.RunMainRunLoop()
}

func listWindowsAndExit() {
	windows, err := ListWindows()
	if err != nil {
		log.Fatalf("Failed to list windows: %v", err)
	}

	if len(windows) == 0 {
		fmt.Println("No windows found. Make sure you have granted Screen Recording permission.")
		fmt.Println("Go to System Preferences > Security & Privacy > Privacy > Screen Recording")
		return
	}

	fmt.Println("Available windows:")
	fmt.Println()
	for i, w := range windows {
		fmt.Printf("  [%d] %s\n", i+1, w.DisplayName())
		fmt.Printf("      ID: %d, Size: %dx%d\n", w.ID, w.Width, w.Height)
	}
}

func runSignalServer(port int) {
	server := sig.NewServer()
	addr := fmt.Sprintf(":%d", port)

	fmt.Printf("Starting signal server on http://localhost%s\n", addr)
	fmt.Println("Press Ctrl+C to stop")

	if err := server.StartServer(addr); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

// buildStreamsInfo converts tracks to StreamInfo slice
func buildStreamsInfo(tracks []*StreamTrackInfo) []sig.StreamInfo {
	streams := make([]sig.StreamInfo, len(tracks))
	for i, t := range tracks {
		streams[i] = sig.StreamInfo{
			TrackID:    t.TrackID,
			WindowName: t.WindowName,
			AppName:    t.AppName,
			IsFocused:  t.IsFocused,
			Width:      t.Width,
			Height:     t.Height,
		}
	}
	return streams
}

// sendOfferToViewer creates and sends an offer along with stream info to a viewer
func sendOfferToViewer(pm *PeerManager, sharer sig.Sharer, peerID string) {
	offer, err := pm.CreateOffer(peerID)
	if err != nil {
		log.Printf("Failed to create offer: %v", err)
		return
	}

	sharer.SendToViewer(peerID, sig.SignalMessage{Type: "offer", SDP: offer, PeerID: peerID})
	sharer.SendToViewer(peerID, sig.SignalMessage{Type: "streams-info", Streams: buildStreamsInfo(pm.GetTracks())})
}

// setupSignaling is the SINGLE entry point for all signaling logic.
// Works identically for local embedded server and remote WebSocket.
func setupSignaling(sharer sig.Sharer, pm *PeerManager) {
	var peerCounter int
	var peerMu sync.Mutex

	// === CALLBACK REGISTRATION (shared for all modes) ===

	pm.SetICECallback(func(peerID string, candidate string) {
		sharer.SendToViewer(peerID, sig.SignalMessage{Type: "ice", Candidate: candidate, PeerID: peerID})
	})

	pm.SetFocusChangeCallback(func(trackID string) {
		sharer.SendToAllViewers(sig.SignalMessage{Type: "focus-change", FocusedTrack: trackID})
	})

	pm.SetSizeChangeCallback(func(trackID string, width, height int) {
		sharer.SendToAllViewers(sig.SignalMessage{Type: "size-change", TrackID: trackID, Width: width, Height: height})
	})

	pm.SetCursorCallback(func(trackID string, x, y float64, inView bool) {
		sharer.SendToAllViewers(sig.SignalMessage{
			Type:         "cursor-position",
			TrackID:      trackID,
			CursorX:      x,
			CursorY:      y,
			CursorInView: inView,
		})
	})

	pm.SetRenegotiateCallback(func(peerID string, offer string) {
		log.Printf("Renegotiation: sending offer to peer %s", peerID)
		sharer.SendToViewer(peerID, sig.SignalMessage{Type: "offer", SDP: offer, PeerID: peerID})
		sharer.SendToViewer(peerID, sig.SignalMessage{Type: "streams-info", Streams: buildStreamsInfo(pm.GetTracks())})
		log.Printf("Renegotiation: sent streams-info with %d tracks to peer %s", len(pm.GetTracks()), peerID)
	})

	pm.SetStreamChangeCallbacks(
		func(info sig.StreamInfo) {
			log.Printf("Broadcasting stream-added: %s", info.TrackID)
			sharer.SendToAllViewers(sig.SignalMessage{Type: "stream-added", StreamAdded: &info})
		},
		func(trackID string) {
			log.Printf("Broadcasting stream-removed: %s", trackID)
			sharer.SendToAllViewers(sig.SignalMessage{Type: "stream-removed", StreamRemoved: trackID})
		},
	)

	pm.SetStreamActivationCallbacks(
		func(info sig.StreamInfo) {
			log.Printf("Broadcasting stream-activated: %s (fast path)", info.TrackID)
			sharer.SendToAllViewers(sig.SignalMessage{Type: "stream-activated", StreamActivated: &info})
		},
		func(trackID string) {
			log.Printf("Broadcasting stream-deactivated: %s (fast path)", trackID)
			sharer.SendToAllViewers(sig.SignalMessage{Type: "stream-deactivated", StreamDeactivated: trackID})
		},
	)

	// === MESSAGE HANDLING LOOP (shared for all modes) ===

	go func() {
		for data := range sharer.Messages() {
			var msg sig.SignalMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				log.Printf("Invalid message: %v", err)
				continue
			}

			switch msg.Type {
			case "viewer-joined":
				found, assignPeerID := sharer.GetUnassignedViewer()
				if !found {
					continue
				}

				peerMu.Lock()
				peerCounter++
				peerID := fmt.Sprintf("viewer-%d", peerCounter)
				peerMu.Unlock()

				assignPeerID(peerID)
				go sendOfferToViewer(pm, sharer, peerID)

			case "viewer-reoffer":
				peerID := msg.PeerID
				if peerID == "" {
					log.Printf("viewer-reoffer received without peerID")
					continue
				}

				found, assignPeerID := sharer.GetUnassignedViewer()
				if !found {
					log.Printf("viewer-reoffer: no unassigned viewer found for %s", peerID)
					continue
				}
				assignPeerID(peerID)

				log.Printf("Sending reoffer to existing viewer: %s", peerID)
				go sendOfferToViewer(pm, sharer, peerID)

			case "answer":
				if msg.PeerID == "" {
					continue
				}
				if err := pm.HandleAnswer(msg.PeerID, msg.SDP); err != nil {
					log.Printf("Failed to handle answer for %s: %v", msg.PeerID, err)
				}

			case "ice":
				if msg.PeerID == "" {
					continue
				}
				if err := pm.AddICECandidate(msg.PeerID, msg.Candidate); err != nil {
					log.Printf("Failed to add ICE candidate for %s: %v", msg.PeerID, err)
				}

			case "renegotiate-answer":
				if msg.PeerID == "" {
					continue
				}
				if err := pm.HandleRenegotiateAnswer(msg.PeerID, msg.SDP); err != nil {
					log.Printf("Failed to handle renegotiate answer for %s: %v", msg.PeerID, err)
				}

			case "error":
				log.Printf("Signal server error: %s", msg.Error)
			}
		}

		log.Printf("Signaling connection closed")
	}()
}

// setupRemoteSignaling sets up signaling for remote WebSocket mode
func setupRemoteSignaling(conn *websocket.Conn, pm *PeerManager, onDisconnect func()) sig.Sharer {
	sharer := sig.NewRemoteSharer(conn)
	sharer.SetDisconnectHandler(onDisconnect)
	setupSignaling(sharer, pm)
	return sharer
}
