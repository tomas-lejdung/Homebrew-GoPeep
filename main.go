package main

import (
	"flag"
	"fmt"
	"log"
	"runtime"

	"github.com/tomaslejdung/gopeep/internal/capture"
	"github.com/tomaslejdung/gopeep/internal/config"
	sig "github.com/tomaslejdung/gopeep/internal/signal"
	"github.com/tomaslejdung/gopeep/internal/ui/overlay"
	"github.com/tomaslejdung/gopeep/internal/ui/tui"
)

func init() {
	// Lock main goroutine to the main OS thread BEFORE main() runs.
	// This must happen in init() because Go's scheduler may move goroutines
	// between threads, and we need to ensure we stay on thread 0 (macOS main thread)
	// for AppKit/NSWindow operations to work correctly.
	runtime.LockOSThread()
}

// Note: TUI mode uses tui.RunTUI() from internal/tui

// DefaultSignalServer is the default remote signal server for P2P initialization
const DefaultSignalServer = config.DefaultSignalServer

// LocalSignalServer is the URL for local signal server
const LocalSignalServer = config.LocalSignalServer

// Config is aliased from the config package
type Config = config.Config

func parseFlags() Config {
	cfg := Config{}
	var localMode bool

	flag.BoolVar(&cfg.ServeMode, "serve", false, "Run as signal server only")
	flag.BoolVar(&cfg.ServeMode, "s", false, "Run as signal server only (shorthand)")

	flag.IntVar(&cfg.Port, "port", 8080, "Signal server port")
	flag.IntVar(&cfg.Port, "p", 8080, "Signal server port (shorthand)")

	flag.BoolVar(&cfg.ListWindows, "list", false, "List available windows and exit")
	flag.BoolVar(&cfg.ListWindows, "l", false, "List available windows (shorthand)")

	flag.IntVar(&cfg.FPS, "fps", 30, "Target framerate")

	flag.StringVar(&cfg.Quality, "quality", "med", "Encoding quality (low|med|hi)")

	flag.StringVar(&cfg.SignalURL, "signal", "", "Custom signal server URL (overrides default)")
	flag.BoolVar(&localMode, "local", false, "Use local signal server (ws://localhost:8080)")

	// TURN server flags
	flag.StringVar(&cfg.TURNServer, "turn", "", "TURN server URL (e.g., turn:turn.example.com:3478)")
	flag.StringVar(&cfg.TURNUser, "turn-user", "", "TURN server username")
	flag.StringVar(&cfg.TURNPass, "turn-pass", "", "TURN server password")
	flag.BoolVar(&cfg.ForceRelay, "force-relay", false, "Force TURN relay (disable direct P2P)")

	flag.BoolVar(&cfg.Help, "help", false, "Show help")
	flag.BoolVar(&cfg.Help, "h", false, "Show help (shorthand)")

	flag.BoolVar(&cfg.Debug, "debug", false, "Enable debug logging to file")

	flag.Parse()

	// --local sets SignalURL to local server
	if localMode {
		cfg.SignalURL = LocalSignalServer
	}

	return cfg
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
	cfg := parseFlags()

	if cfg.Help {
		printHelp()
		return
	}

	// List windows mode
	if cfg.ListWindows {
		listWindowsAndExit()
		return
	}

	// Server-only mode
	if cfg.ServeMode {
		runSignalServer(cfg.Port)
		return
	}

	// Determine signal URL: use default if not specified
	if cfg.SignalURL == "" {
		cfg.SignalURL = DefaultSignalServer
	}

	// Check screen recording permission on main thread (required for AppKit APIs)
	if !capture.HasScreenRecordingPermission() {
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
		done <- tui.RunTUI(cfg)
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
	windows, err := capture.ListWindows()
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

// Note: Signaling functions (setupSignaling, setupRemoteSignaling) are now in internal/tui
