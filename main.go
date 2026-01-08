package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"sync"

	"github.com/gorilla/websocket"
	sig "github.com/tomaslejdung/gopeep/pkg/signal"
)

// Note: TUI mode uses RunTUI() from tui.go

// DefaultSignalServer is the default remote signal server for P2P initialization
const DefaultSignalServer = "wss://gopeep.tineestudio.se"

// Config holds runtime configuration
type Config struct {
	ServeMode   bool
	Port        int
	ListWindows bool
	FPS         int
	Quality     string
	SignalURL   string
	LocalMode   bool // Force local-only mode (no remote signal server)
	Help        bool

	// TURN server configuration
	TURNServer string
	TURNUser   string
	TURNPass   string
	ForceRelay bool // Force TURN relay (no direct P2P)
}

func parseFlags() Config {
	config := Config{}

	flag.BoolVar(&config.ServeMode, "serve", false, "Run as signal server only")
	flag.BoolVar(&config.ServeMode, "s", false, "Run as signal server only (shorthand)")

	flag.IntVar(&config.Port, "port", 8080, "Signal server port")
	flag.IntVar(&config.Port, "p", 8080, "Signal server port (shorthand)")

	flag.BoolVar(&config.ListWindows, "list", false, "List available windows and exit")
	flag.BoolVar(&config.ListWindows, "l", false, "List available windows (shorthand)")

	flag.IntVar(&config.FPS, "fps", 30, "Target framerate")

	flag.StringVar(&config.Quality, "quality", "med", "Encoding quality (low|med|hi)")

	flag.StringVar(&config.SignalURL, "signal", "", "Custom signal server URL (overrides default)")
	flag.BoolVar(&config.LocalMode, "local", false, "Force local-only mode (skip remote signal server)")

	// TURN server flags
	flag.StringVar(&config.TURNServer, "turn", "", "TURN server URL (e.g., turn:turn.example.com:3478)")
	flag.StringVar(&config.TURNUser, "turn-user", "", "TURN server username")
	flag.StringVar(&config.TURNPass, "turn-pass", "", "TURN server password")
	flag.BoolVar(&config.ForceRelay, "force-relay", false, "Force TURN relay (disable direct P2P)")

	flag.BoolVar(&config.Help, "help", false, "Show help")
	flag.BoolVar(&config.Help, "h", false, "Show help (shorthand)")

	flag.Parse()

	return config
}

func printHelp() {
	fmt.Println(`GoPeep - P2P Screen Sharing for Pair Programming

Usage: gopeep [options]

By default, GoPeep connects to the remote signal server at:
  ` + DefaultSignalServer + `

This allows P2P connections over the internet. If the remote server is
unreachable, it automatically falls back to local-only mode.

Options:
  --list, -l             List available windows and exit
  --local                Force local-only mode (skip remote signal server)
  --signal <url>         Custom signal server URL (overrides default)
  --serve, -s            Run as signal server only
  --port, -p <port>      Local server port (default: 8080)
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
  gopeep                     # TUI mode, uses remote signal server
  gopeep --local             # TUI mode, local network only
  gopeep --list              # List available windows
  gopeep --serve             # Run signal server only (for self-hosting)

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

	// Determine signal URL: custom > default > local fallback
	if config.SignalURL == "" && !config.LocalMode {
		// Use default signal server
		config.SignalURL = DefaultSignalServer
	}

	// TUI mode (the only interactive mode now)
	if err := RunTUI(config); err != nil {
		log.Fatalf("TUI error: %v", err)
	}
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

func getLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "localhost"
	}

	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}

	return "localhost"
}

// setupPeerSignaling connects the signal server to the peer manager
func setupPeerSignaling(server *sig.Server, pm *PeerManager, roomCode string, password string) {
	localSharer := server.RegisterLocalSharer(roomCode, password)

	var peerCounter int
	var peerMu sync.Mutex

	// Set up ICE callback
	pm.SetICECallback(func(peerID string, candidate string) {
		// Send ICE to specific viewer
		iceMsg := sig.SignalMessage{Type: "ice", Candidate: candidate, PeerID: peerID}
		localSharer.SendToViewer(peerID, iceMsg)
	})

	// Set up focus change callback to broadcast to all viewers
	pm.SetFocusChangeCallback(func(trackID string) {
		log.Printf("Focus change callback triggered for track: %s", trackID)
		focusMsg := sig.SignalMessage{Type: "focus-change", FocusedTrack: trackID}
		localSharer.SendToAllViewers(focusMsg)
	})

	// Set up size change callback to broadcast dimension changes to all viewers
	pm.SetSizeChangeCallback(func(trackID string, width, height int) {
		log.Printf("Size change callback triggered for track %s: %dx%d", trackID, width, height)
		sizeMsg := sig.SignalMessage{Type: "size-change", TrackID: trackID, Width: width, Height: height}
		localSharer.SendToAllViewers(sizeMsg)
	})

	// Set up renegotiation callback to send new offers during track add/remove
	pm.SetRenegotiateCallback(func(peerID string, offer string) {
		log.Printf("Renegotiation: sending offer to peer %s", peerID)
		offerMsg := sig.SignalMessage{Type: "offer", SDP: offer, PeerID: peerID}
		localSharer.SendToViewer(peerID, offerMsg)
	})

	// Set up stream change callbacks
	pm.SetStreamChangeCallbacks(
		func(info sig.StreamInfo) {
			// Broadcast stream-added to all viewers
			log.Printf("Broadcasting stream-added: %s", info.TrackID)
			msg := sig.SignalMessage{Type: "stream-added", StreamAdded: &info}
			localSharer.SendToAllViewers(msg)
		},
		func(trackID string) {
			// Broadcast stream-removed to all viewers
			log.Printf("Broadcasting stream-removed: %s", trackID)
			msg := sig.SignalMessage{Type: "stream-removed", StreamRemoved: trackID}
			localSharer.SendToAllViewers(msg)
		},
	)

	go func() {
		for data := range localSharer.Messages() {
			var msg sig.SignalMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}

			switch msg.Type {
			case "viewer-joined":
				found, assignPeerID := localSharer.GetUnassignedViewer()
				if !found {
					continue
				}

				peerMu.Lock()
				peerCounter++
				peerID := fmt.Sprintf("viewer-%d", peerCounter)
				peerMu.Unlock()

				assignPeerID(peerID)

				go func(pid string) {
					offer, err := pm.CreateOffer(pid)
					if err != nil {
						log.Printf("Failed to create offer: %v", err)
						return
					}

					offerMsg := sig.SignalMessage{Type: "offer", SDP: offer, PeerID: pid}
					localSharer.SendToViewer(pid, offerMsg)

					// Send streams-info after offer
					tracks := pm.GetTracks()
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
					streamsMsg := sig.SignalMessage{Type: "streams-info", Streams: streams}
					localSharer.SendToViewer(pid, streamsMsg)
				}(peerID)

			case "answer":
				peerID := msg.PeerID
				if peerID == "" {
					continue
				}
				if err := pm.HandleAnswer(peerID, msg.SDP); err != nil {
					log.Printf("Failed to handle answer for %s: %v", peerID, err)
				}

			case "ice":
				peerID := msg.PeerID
				if peerID == "" {
					continue
				}
				if err := pm.AddICECandidate(peerID, msg.Candidate); err != nil {
					log.Printf("Failed to add ICE candidate for %s: %v", peerID, err)
				}

			case "renegotiate-answer":
				peerID := msg.PeerID
				if peerID == "" {
					continue
				}
				if err := pm.HandleRenegotiateAnswer(peerID, msg.SDP); err != nil {
					log.Printf("Failed to handle renegotiate answer for %s: %v", peerID, err)
				}
			}
		}
	}()
}

// setupRemotePeerSignaling connects WebSocket to the peer manager with disconnect callback
func setupRemotePeerSignaling(conn *websocket.Conn, pm *PeerManager, onDisconnect func()) {
	var peerCounter int
	var peerMu sync.Mutex
	var connMu sync.Mutex // Protect WebSocket writes

	pm.SetICECallback(func(peerID string, candidate string) {
		iceMsg := sig.SignalMessage{Type: "ice", Candidate: candidate, PeerID: peerID}
		connMu.Lock()
		conn.WriteJSON(iceMsg)
		connMu.Unlock()
	})

	// Set up focus change callback to broadcast to all viewers
	pm.SetFocusChangeCallback(func(trackID string) {
		log.Printf("Remote focus change callback triggered for track: %s", trackID)
		focusMsg := sig.SignalMessage{Type: "focus-change", FocusedTrack: trackID}
		connMu.Lock()
		err := conn.WriteJSON(focusMsg)
		connMu.Unlock()
		if err != nil {
			log.Printf("Failed to send focus-change: %v", err)
		} else {
			log.Printf("Sent focus-change message to signal server")
		}
	})

	// Set up size change callback to broadcast dimension changes to all viewers
	pm.SetSizeChangeCallback(func(trackID string, width, height int) {
		log.Printf("Remote size change callback triggered for track %s: %dx%d", trackID, width, height)
		sizeMsg := sig.SignalMessage{Type: "size-change", TrackID: trackID, Width: width, Height: height}
		connMu.Lock()
		err := conn.WriteJSON(sizeMsg)
		connMu.Unlock()
		if err != nil {
			log.Printf("Failed to send size-change: %v", err)
		}
	})

	// Set up renegotiation callback for dynamic track add/remove
	pm.SetRenegotiateCallback(func(peerID string, offer string) {
		log.Printf("Remote renegotiation: sending offer for peer %s", peerID)
		offerMsg := sig.SignalMessage{Type: "offer", SDP: offer, PeerID: peerID}
		connMu.Lock()
		err := conn.WriteJSON(offerMsg)
		connMu.Unlock()
		if err != nil {
			log.Printf("Failed to send renegotiation offer: %v", err)
		}
	})

	// Set up stream change callbacks
	pm.SetStreamChangeCallbacks(
		func(info sig.StreamInfo) {
			log.Printf("Remote: broadcasting stream-added: %s", info.TrackID)
			msg := sig.SignalMessage{Type: "stream-added", StreamAdded: &info}
			connMu.Lock()
			conn.WriteJSON(msg)
			connMu.Unlock()
		},
		func(trackID string) {
			log.Printf("Remote: broadcasting stream-removed: %s", trackID)
			msg := sig.SignalMessage{Type: "stream-removed", StreamRemoved: trackID}
			connMu.Lock()
			conn.WriteJSON(msg)
			connMu.Unlock()
		},
	)

	go func() {
		for {
			var msg sig.SignalMessage
			if err := conn.ReadJSON(&msg); err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					log.Printf("Signal server disconnected: %v", err)
				}
				if onDisconnect != nil {
					onDisconnect()
				}
				return
			}

			switch msg.Type {
			case "viewer-joined":
				peerMu.Lock()
				peerCounter++
				peerID := fmt.Sprintf("viewer-%d", peerCounter)
				peerMu.Unlock()

				go func(pid string) {
					offer, err := pm.CreateOffer(pid)
					if err != nil {
						log.Printf("Failed to create offer: %v", err)
						return
					}

					offerMsg := sig.SignalMessage{Type: "offer", SDP: offer, PeerID: pid}
					connMu.Lock()
					conn.WriteJSON(offerMsg)
					connMu.Unlock()

					// Send streams-info after offer
					tracks := pm.GetTracks()
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
					streamsMsg := sig.SignalMessage{Type: "streams-info", Streams: streams}
					connMu.Lock()
					conn.WriteJSON(streamsMsg)
					connMu.Unlock()
				}(peerID)

			case "answer":
				peerID := msg.PeerID
				if peerID == "" {
					continue
				}
				if err := pm.HandleAnswer(peerID, msg.SDP); err != nil {
					log.Printf("Failed to handle answer for %s: %v", peerID, err)
				}

			case "ice":
				peerID := msg.PeerID
				if peerID == "" {
					continue
				}
				if err := pm.AddICECandidate(peerID, msg.Candidate); err != nil {
					log.Printf("Failed to add ICE candidate for %s: %v", peerID, err)
				}

			case "renegotiate-answer":
				peerID := msg.PeerID
				if peerID == "" {
					continue
				}
				if err := pm.HandleRenegotiateAnswer(peerID, msg.SDP); err != nil {
					log.Printf("Failed to handle renegotiate answer for %s: %v", peerID, err)
				}

			case "error":
				log.Printf("Signal server error: %s", msg.Error)
			}
		}
	}()
}
