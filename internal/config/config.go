package config

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

// DefaultSignalServer is the default remote signal server for P2P initialization
const DefaultSignalServer = "wss://gopeep.tineestudio.se"

// LocalSignalServer is the URL for local signal server
const LocalSignalServer = "ws://localhost:8080"
