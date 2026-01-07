package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

// Note: TUI mode uses RunTUI() from tui.go

// DefaultSignalServer is the default remote signal server for P2P initialization
const DefaultSignalServer = "wss://gopeep.tineestudio.se"

// Config holds runtime configuration
type Config struct {
	ServeMode   bool
	Port        int
	WindowName  string
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

	flag.StringVar(&config.WindowName, "window", "", "Share window matching name (partial match)")
	flag.StringVar(&config.WindowName, "w", "", "Share window matching name (shorthand)")

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
  --window, -w <name>    Share window matching name (non-interactive mode)
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
  gopeep --window "VS Code"  # Share "VS Code" via remote server
  gopeep --local             # TUI mode, local network only
  gopeep --list              # List available windows
  gopeep --serve             # Run signal server only (for self-hosting)

TUI Controls:
  Tab / ← →     Switch between Sources and Quality columns
  ↑/↓ or j/k    Navigate within column
  Enter/Space   Select source or apply quality
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

	// If a window name is specified (non-TUI mode)
	if config.WindowName != "" {
		if config.LocalMode {
			runShareMode(config)
		} else {
			runShareModeWithFallback(config)
		}
		return
	}

	// TUI mode
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
	server := NewSignalServer()
	addr := fmt.Sprintf(":%d", port)

	fmt.Printf("Starting signal server on http://localhost%s\n", addr)
	fmt.Println("Press Ctrl+C to stop")

	if err := server.StartServer(addr); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

func runShareMode(config Config) {
	// Check screen recording permission
	if !HasScreenRecordingPermission() {
		fmt.Println("Screen Recording permission required.")
		fmt.Println("Please grant permission in:")
		fmt.Println("  System Preferences > Security & Privacy > Privacy > Screen Recording")
		fmt.Println()
		fmt.Println("After granting permission, restart gopeep.")
		return
	}

	// Get window to share
	var targetWindow *WindowInfo

	if config.WindowName != "" {
		// Find window by name
		matches, err := FindWindowByName(config.WindowName)
		if err != nil {
			log.Fatalf("Failed to find windows: %v", err)
		}
		if len(matches) == 0 {
			fmt.Printf("No windows found matching '%s'\n", config.WindowName)
			fmt.Println("Use --list to see available windows")
			return
		}
		if len(matches) > 1 {
			fmt.Printf("Multiple windows match '%s':\n", config.WindowName)
			for i, w := range matches {
				fmt.Printf("  [%d] %s\n", i+1, w.DisplayName())
			}
			fmt.Println("Please be more specific or use the picker")
			return
		}
		targetWindow = &matches[0]
	} else {
		// Interactive picker
		targetWindow = interactiveWindowPicker()
		if targetWindow == nil {
			return
		}
	}

	fmt.Printf("\nSharing: %s\n", targetWindow.DisplayName())

	// Generate room code
	roomCode := GenerateRoomCode()

	// Get local IP for URL
	localIP := getLocalIP()

	// Start signal server
	server := NewSignalServer()
	addr := fmt.Sprintf(":%d", config.Port)

	go func() {
		if err := server.StartServer(addr); err != nil {
			log.Printf("Server error: %v", err)
		}
	}()

	// Give server time to start
	time.Sleep(100 * time.Millisecond)

	fmt.Printf("Room: %s\n", roomCode)
	fmt.Printf("URL:  http://%s:%d/%s\n", localIP, config.Port, roomCode)
	fmt.Println()
	fmt.Println("Waiting for viewers...")

	// Create peer manager with ICE config
	iceConfig := ICEConfig{
		TURNServer: config.TURNServer,
		TURNUser:   config.TURNUser,
		TURNPass:   config.TURNPass,
		ForceRelay: config.ForceRelay,
	}
	peerManager, err := NewPeerManagerWithICE(iceConfig)
	if err != nil {
		log.Fatalf("Failed to create peer manager: %v", err)
	}
	defer peerManager.Close()

	// Track connected viewers
	var viewerCount int
	var viewerMu sync.Mutex

	peerManager.SetConnectionCallbacks(
		func(peerID string) {
			viewerMu.Lock()
			viewerCount++
			count := viewerCount
			viewerMu.Unlock()
			fmt.Printf("[Viewer connected: %d]\n", count)
		},
		func(peerID string) {
			viewerMu.Lock()
			viewerCount--
			count := viewerCount
			viewerMu.Unlock()
			fmt.Printf("[Viewer disconnected, remaining: %d]\n", count)
		},
	)

	// Start window capture
	err = StartWindowCapture(targetWindow.ID, 0, 0, config.FPS)
	if err != nil {
		log.Fatalf("Failed to start capture: %v", err)
	}
	defer StopCapture()

	// Create streamer with quality from config
	bitrate := ParseQualityFlag(config.Quality)
	streamer := NewStreamerWithBitrate(peerManager, config.FPS, bitrate)
	streamer.SetCaptureFunc(func() (*image.RGBA, error) {
		return GetLatestFrame(time.Second)
	})

	if err := streamer.Start(); err != nil {
		log.Fatalf("Failed to start streamer: %v", err)
	}
	defer streamer.Stop()

	// Set up signaling between server and peer manager
	setupSignaling(server, peerManager, roomCode)

	// Wait for interrupt
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	fmt.Println("\nStopping...")
}

func interactiveWindowPicker() *WindowInfo {
	windows, err := ListWindows()
	if err != nil {
		log.Printf("Failed to list windows: %v", err)
		return nil
	}

	if len(windows) == 0 {
		fmt.Println("No windows found. Make sure you have granted Screen Recording permission.")
		return nil
	}

	fmt.Println("Select window to share:")
	fmt.Println()

	for i, w := range windows {
		fmt.Printf("  [%d] %s\n", i+1, w.DisplayName())
	}

	fmt.Println()
	fmt.Print("> ")

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		log.Printf("Failed to read input: %v", err)
		return nil
	}

	input = strings.TrimSpace(input)
	choice, err := strconv.Atoi(input)
	if err != nil || choice < 1 || choice > len(windows) {
		fmt.Println("Invalid selection")
		return nil
	}

	return &windows[choice-1]
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

// runShareModeWithFallback tries remote signal server first, falls back to local
func runShareModeWithFallback(config Config) {
	// Check screen recording permission first
	if !HasScreenRecordingPermission() {
		fmt.Println("Screen Recording permission required.")
		fmt.Println("Please grant permission in:")
		fmt.Println("  System Preferences > Security & Privacy > Privacy > Screen Recording")
		fmt.Println()
		fmt.Println("After granting permission, restart gopeep.")
		return
	}

	// Try to connect to remote signal server
	signalURL := config.SignalURL
	if strings.HasPrefix(signalURL, "http://") {
		signalURL = "ws://" + strings.TrimPrefix(signalURL, "http://")
	} else if strings.HasPrefix(signalURL, "https://") {
		signalURL = "wss://" + strings.TrimPrefix(signalURL, "https://")
	} else if !strings.HasPrefix(signalURL, "ws://") && !strings.HasPrefix(signalURL, "wss://") {
		signalURL = "wss://" + signalURL
	}

	// Generate room code
	roomCode := GenerateRoomCode()
	wsURL := strings.TrimSuffix(signalURL, "/") + "/ws/" + roomCode

	fmt.Printf("Connecting to signal server %s...\n", config.SignalURL)

	// Try connecting with timeout
	dialer := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
	}
	conn, _, err := dialer.Dial(wsURL, nil)

	if err != nil {
		fmt.Printf("Remote signal server unavailable: %v\n", err)
		fmt.Println("Falling back to local mode...")
		fmt.Println()
		runShareMode(config)
		return
	}

	// Connected to remote - proceed with remote mode
	conn.Close() // Close test connection, runRemoteShareModeWithConn will reconnect

	// Run the remote share mode
	runRemoteShareMode(config)
}

// runRemoteShareMode connects to a remote signal server via WebSocket
func runRemoteShareMode(config Config) {
	// Check screen recording permission
	if !HasScreenRecordingPermission() {
		fmt.Println("Screen Recording permission required.")
		fmt.Println("Please grant permission in:")
		fmt.Println("  System Preferences > Security & Privacy > Privacy > Screen Recording")
		fmt.Println()
		fmt.Println("After granting permission, restart gopeep.")
		return
	}

	// Get window to share
	var targetWindow *WindowInfo

	if config.WindowName != "" {
		matches, err := FindWindowByName(config.WindowName)
		if err != nil {
			log.Fatalf("Failed to find windows: %v", err)
		}
		if len(matches) == 0 {
			fmt.Printf("No windows found matching '%s'\n", config.WindowName)
			fmt.Println("Use --list to see available windows")
			return
		}
		if len(matches) > 1 {
			fmt.Printf("Multiple windows match '%s':\n", config.WindowName)
			for i, w := range matches {
				fmt.Printf("  [%d] %s\n", i+1, w.DisplayName())
			}
			fmt.Println("Please be more specific")
			return
		}
		targetWindow = &matches[0]
	} else {
		targetWindow = interactiveWindowPicker()
		if targetWindow == nil {
			return
		}
	}

	fmt.Printf("\nSharing: %s\n", targetWindow.DisplayName())

	// Generate room code
	roomCode := GenerateRoomCode()

	// Parse signal URL and build WebSocket URL
	signalURL := config.SignalURL
	// Normalize URL scheme
	if strings.HasPrefix(signalURL, "http://") {
		signalURL = "ws://" + strings.TrimPrefix(signalURL, "http://")
	} else if strings.HasPrefix(signalURL, "https://") {
		signalURL = "wss://" + strings.TrimPrefix(signalURL, "https://")
	} else if !strings.HasPrefix(signalURL, "ws://") && !strings.HasPrefix(signalURL, "wss://") {
		signalURL = "wss://" + signalURL
	}

	// Build the viewer URL for sharing
	viewerURL := strings.Replace(signalURL, "wss://", "https://", 1)
	viewerURL = strings.Replace(viewerURL, "ws://", "http://", 1)
	viewerURL = strings.TrimSuffix(viewerURL, "/") + "/" + roomCode

	wsURL := strings.TrimSuffix(signalURL, "/") + "/ws/" + roomCode

	fmt.Printf("Room: %s\n", roomCode)
	fmt.Printf("URL:  %s\n", viewerURL)
	fmt.Println()
	fmt.Println("Connecting to signal server...")

	// Connect to remote signal server
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		log.Fatalf("Failed to connect to signal server: %v", err)
	}
	defer conn.Close()

	// Join as sharer
	joinMsg := SignalMessage{Type: "join", Role: "sharer"}
	if err := conn.WriteJSON(joinMsg); err != nil {
		log.Fatalf("Failed to send join message: %v", err)
	}

	// Wait for join confirmation
	var joinResp SignalMessage
	if err := conn.ReadJSON(&joinResp); err != nil {
		log.Fatalf("Failed to read join response: %v", err)
	}
	if joinResp.Type == "error" {
		log.Fatalf("Failed to join room: %s", joinResp.Error)
	}

	fmt.Println("Connected to signal server!")
	fmt.Println("Waiting for viewers...")

	// Create peer manager with ICE config
	iceConfig := ICEConfig{
		TURNServer: config.TURNServer,
		TURNUser:   config.TURNUser,
		TURNPass:   config.TURNPass,
		ForceRelay: config.ForceRelay,
	}
	peerManager, err := NewPeerManagerWithICE(iceConfig)
	if err != nil {
		log.Fatalf("Failed to create peer manager: %v", err)
	}
	defer peerManager.Close()

	// Track connected viewers
	var viewerCount int
	var viewerMu sync.Mutex

	peerManager.SetConnectionCallbacks(
		func(peerID string) {
			viewerMu.Lock()
			viewerCount++
			count := viewerCount
			viewerMu.Unlock()
			fmt.Printf("[Viewer connected: %d]\n", count)
		},
		func(peerID string) {
			viewerMu.Lock()
			viewerCount--
			count := viewerCount
			viewerMu.Unlock()
			fmt.Printf("[Viewer disconnected, remaining: %d]\n", count)
		},
	)

	// Start window capture
	err = StartWindowCapture(targetWindow.ID, 0, 0, config.FPS)
	if err != nil {
		log.Fatalf("Failed to start capture: %v", err)
	}
	defer StopCapture()

	// Create streamer with quality from config
	bitrate := ParseQualityFlag(config.Quality)
	streamer := NewStreamerWithBitrate(peerManager, config.FPS, bitrate)
	streamer.SetCaptureFunc(func() (*image.RGBA, error) {
		return GetLatestFrame(time.Second)
	})

	if err := streamer.Start(); err != nil {
		log.Fatalf("Failed to start streamer: %v", err)
	}
	defer streamer.Stop()

	// Set up signaling via WebSocket
	setupRemoteSignaling(conn, peerManager)

	// Wait for interrupt
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	<-sigChan

	fmt.Println("\nStopping...")
}

// setupRemoteSignaling connects the WebSocket to the peer manager
func setupRemoteSignaling(conn *websocket.Conn, pm *PeerManager) {
	// Counter for peer IDs
	var peerCounter int
	var peerMu sync.Mutex

	// Track viewers by peerID for ICE candidate routing
	viewerPeerIDs := make(map[string]bool)
	var viewersMu sync.Mutex

	// Read messages from signal server
	go func() {
		for {
			var msg SignalMessage
			if err := conn.ReadJSON(&msg); err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					log.Printf("Signal server disconnected: %v", err)
				}
				return
			}

			switch msg.Type {
			case "viewer-joined":
				// New viewer connected
				peerMu.Lock()
				peerCounter++
				peerID := fmt.Sprintf("viewer-%d", peerCounter)
				peerMu.Unlock()

				viewersMu.Lock()
				viewerPeerIDs[peerID] = true
				viewersMu.Unlock()

				go func(pid string) {
					offer, err := pm.CreateOffer(pid)
					if err != nil {
						log.Printf("Failed to create offer: %v", err)
						return
					}

					// Send offer to signal server (will be forwarded to viewer)
					offerMsg := SignalMessage{Type: "offer", SDP: offer, PeerID: pid}
					if err := conn.WriteJSON(offerMsg); err != nil {
						log.Printf("Failed to send offer: %v", err)
					}
				}(peerID)

			case "answer":
				peerID := msg.PeerID
				if peerID == "" {
					log.Printf("Answer received without peerID, ignoring")
					continue
				}

				if err := pm.HandleAnswer(peerID, msg.SDP); err != nil {
					log.Printf("Failed to handle answer for %s: %v", peerID, err)
				}

			case "ice":
				peerID := msg.PeerID
				if peerID == "" {
					log.Printf("ICE candidate received without peerID, ignoring")
					continue
				}

				if err := pm.AddICECandidate(peerID, msg.Candidate); err != nil {
					log.Printf("Failed to add ICE candidate for %s: %v", peerID, err)
				}

			case "error":
				log.Printf("Signal server error: %s", msg.Error)
			}
		}
	}()
}

// setupSignaling connects the signal server to the peer manager
func setupSignaling(server *SignalServer, pm *PeerManager, roomCode string) {
	// Create a room for the sharer
	room := server.getOrCreateRoom(roomCode)

	// Create a virtual "sharer" client
	sharerClient := &Client{
		room:   roomCode,
		role:   "sharer",
		send:   make(chan []byte, 256),
		server: server,
	}

	room.mu.Lock()
	room.sharer = sharerClient
	room.mu.Unlock()

	// Counter for peer IDs
	var peerCounter int
	var peerMu sync.Mutex

	// Track which viewer client is waiting for an offer response
	var pendingViewers []*Client
	var pendingMu sync.Mutex

	// Process messages from viewers (via the sharer client's channel)
	go func() {
		for data := range sharerClient.send {
			var msg SignalMessage
			if err := json.Unmarshal(data, &msg); err != nil {
				continue
			}

			switch msg.Type {
			case "viewer-joined":
				// New viewer connected, find the newest viewer without a peerID
				room.mu.RLock()
				var newViewer *Client
				for viewer := range room.viewers {
					if viewer.peerID == "" {
						newViewer = viewer
						break
					}
				}
				room.mu.RUnlock()

				if newViewer == nil {
					log.Printf("viewer-joined but no unassigned viewer found")
					continue
				}

				// Assign peer ID
				peerMu.Lock()
				peerCounter++
				peerID := fmt.Sprintf("viewer-%d", peerCounter)
				peerMu.Unlock()

				newViewer.peerID = peerID

				// Track pending viewer
				pendingMu.Lock()
				pendingViewers = append(pendingViewers, newViewer)
				pendingMu.Unlock()

				go func(viewer *Client, pid string) {
					offer, err := pm.CreateOffer(pid)
					if err != nil {
						log.Printf("Failed to create offer: %v", err)
						return
					}

					// Send offer only to THIS viewer with their peerID
					offerMsg := SignalMessage{Type: "offer", SDP: offer, PeerID: pid}
					data, _ := json.Marshal(offerMsg)

					select {
					case viewer.send <- data:
					default:
						log.Printf("Failed to send offer to viewer %s", pid)
					}
				}(newViewer, peerID)

			case "answer":
				// Viewer sent answer - use the peerID they echo back
				peerID := msg.PeerID
				if peerID == "" {
					log.Printf("Answer received without peerID, ignoring")
					continue
				}

				if err := pm.HandleAnswer(peerID, msg.SDP); err != nil {
					log.Printf("Failed to handle answer for %s: %v", peerID, err)
				}

			case "ice":
				// ICE candidate from viewer - use the peerID they echo back
				peerID := msg.PeerID
				if peerID == "" {
					log.Printf("ICE candidate received without peerID, ignoring")
					continue
				}

				if err := pm.AddICECandidate(peerID, msg.Candidate); err != nil {
					log.Printf("Failed to add ICE candidate for %s: %v", peerID, err)
				}
			}
		}
	}()
}
