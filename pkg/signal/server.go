package signal

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Client represents a connected WebSocket client
type Client struct {
	conn   *websocket.Conn
	room   string
	role   string // "sharer" or "viewer"
	peerID string // assigned peer ID for this viewer
	send   chan []byte
	server *Server
}

// Room holds connected clients for a session
type Room struct {
	code       string
	password   string // optional password for room protection
	secret     string // secret token for sharer authentication
	sharer     *Client
	viewers    map[*Client]bool
	mu         sync.RWMutex
	reserved   bool      // true if reserved but sharer hasn't joined yet
	reservedAt time.Time // when the room was reserved
}

// Server manages WebSocket connections and room routing
type Server struct {
	rooms    map[string]*Room
	mu       sync.RWMutex
	upgrader websocket.Upgrader
	done     chan struct{} // signals shutdown to background goroutines
}

// NewServer creates a new signaling server
func NewServer() *Server {
	s := &Server{
		rooms: make(map[string]*Room),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  4096,
			WriteBufferSize: 4096,
			CheckOrigin: func(r *http.Request) bool {
				return true // Allow all origins for development
			},
		},
		done: make(chan struct{}),
	}

	// Start cleanup goroutine for expired room reservations
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.CleanupReservedRooms(5 * time.Minute)
			case <-s.done:
				return
			}
		}
	}()

	return s
}

// Close shuts down the server and stops background goroutines.
func (s *Server) Close() {
	close(s.done)
}

// getOrCreateRoom returns existing room or creates new one
func (s *Server) getOrCreateRoom(code string) *Room {
	s.mu.Lock()
	defer s.mu.Unlock()

	code = NormalizeRoomCode(code)
	if room, exists := s.rooms[code]; exists {
		return room
	}

	room := &Room{
		code:    code,
		viewers: make(map[*Client]bool),
	}
	s.rooms[code] = room
	return room
}

// removeClient removes a client from its room
func (s *Server) removeClient(client *Client) {
	s.mu.Lock()
	defer s.mu.Unlock()

	room, exists := s.rooms[client.room]
	if !exists {
		return
	}

	room.mu.Lock()
	defer room.mu.Unlock()

	if client.role == "sharer" && room.sharer == client {
		room.sharer = nil
		// Notify all viewers that sharer disconnected
		for viewer := range room.viewers {
			msg := SignalMessage{Type: "error", Error: "Sharer disconnected"}
			data, _ := json.Marshal(msg)
			select {
			case viewer.send <- data:
			default:
			}
		}
	} else {
		delete(room.viewers, client)
	}

	// Clean up empty rooms
	if room.sharer == nil && len(room.viewers) == 0 {
		delete(s.rooms, client.room)
	}
}

// generateUniqueRoomCode generates a room code that doesn't collide with active rooms.
// Caller must hold s.mu (read or write lock).
func (s *Server) generateUniqueRoomCode() string {
	const maxAttempts = 100
	for i := 0; i < maxAttempts; i++ {
		code := GenerateRoomCode()
		if _, exists := s.rooms[code]; !exists {
			return code
		}
	}

	// Fallback: add random suffix if all attempts failed (extremely unlikely with 10M combinations)
	return GenerateRoomCode() + "-" + string(rune('A'+rng.Intn(26)))
}

// ReserveRoom creates a reserved room that expires if not claimed.
// Uses a single write lock to atomically generate a unique code and create the room,
// avoiding TOCTOU race conditions.
// Returns the room code and a secret token for sharer authentication.
func (s *Server) ReserveRoom() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	code := s.generateUniqueRoomCode()
	secret := GenerateSecret()

	room := &Room{
		code:       code,
		secret:     secret,
		viewers:    make(map[*Client]bool),
		reserved:   true,
		reservedAt: time.Now(),
	}
	s.rooms[code] = room

	log.Printf("Reserved room: %s", code)
	return code, secret
}

// CleanupReservedRooms removes rooms that were reserved but never claimed
func (s *Server) CleanupReservedRooms(maxAge time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for code, room := range s.rooms {
		room.mu.RLock()
		shouldDelete := room.reserved && room.sharer == nil && now.Sub(room.reservedAt) > maxAge
		room.mu.RUnlock()
		if shouldDelete {
			log.Printf("Cleaning up expired reservation: %s", code)
			delete(s.rooms, code)
		}
	}
}

// HandleReserveRoom handles HTTP requests to reserve a new room code
func (s *Server) HandleReserveRoom(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	code, secret := s.ReserveRoom()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(map[string]string{
		"room":   code,
		"secret": secret,
	})
}

// HandleWebSocket handles WebSocket connections for signaling
func (s *Server) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	// Extract room code from URL path: /ws/{room-code}
	path := strings.TrimPrefix(r.URL.Path, "/ws/")
	roomCode := NormalizeRoomCode(path)

	if roomCode == "" || !ValidateRoomCode(roomCode) {
		http.Error(w, "Invalid room code", http.StatusBadRequest)
		return
	}

	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade failed: %v", err)
		return
	}

	client := &Client{
		conn:   conn,
		room:   roomCode,
		send:   make(chan []byte, 256),
		server: s,
	}

	go client.writePump()
	go client.readPump()
}

// HandleViewer serves the viewer HTML page
func (s *Server) HandleViewer(w http.ResponseWriter, r *http.Request) {
	// Extract room code from path: /{room-code}
	path := strings.TrimPrefix(r.URL.Path, "/")

	if path == "" || path == "favicon.ico" {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	// Serve the embedded viewer.html
	content, err := ViewerHTML.ReadFile("viewer.html")
	if err != nil {
		http.Error(w, "Viewer not found", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(content)
}

// StartServer starts the signaling HTTP server
func (s *Server) StartServer(addr string) error {
	mux := http.NewServeMux()

	// API endpoint to reserve a room code
	mux.HandleFunc("/api/reserve", s.HandleReserveRoom)

	// WebSocket endpoint for signaling
	mux.HandleFunc("/ws/", s.HandleWebSocket)

	// Serve viewer page for room URLs
	mux.HandleFunc("/", s.HandleViewer)

	log.Printf("Signal server starting on %s", addr)
	return http.ListenAndServe(addr, mux)
}

// BroadcastToViewers sends a message to all viewers in a room (not the sharer)
func (s *Server) BroadcastToViewers(roomCode string, msg SignalMessage) {
	s.mu.RLock()
	room, exists := s.rooms[NormalizeRoomCode(roomCode)]
	s.mu.RUnlock()

	if !exists {
		return
	}

	room.mu.RLock()
	defer room.mu.RUnlock()

	data, _ := json.Marshal(msg)

	for viewer := range room.viewers {
		select {
		case viewer.send <- data:
		default:
		}
	}
}

// UpdateRoomPassword updates the password for an existing room
func (s *Server) UpdateRoomPassword(roomCode string, password string) {
	s.mu.RLock()
	room, exists := s.rooms[NormalizeRoomCode(roomCode)]
	s.mu.RUnlock()

	if !exists {
		return
	}

	room.mu.Lock()
	room.password = password
	if password != "" {
		log.Printf("Room %s password updated", roomCode)
	} else {
		log.Printf("Room %s password removed", roomCode)
	}
	room.mu.Unlock()
}

// LocalSharer provides an interface for local (in-process) sharing
// It registers as the sharer for a room and provides methods to interact with viewers
type LocalSharer struct {
	server   *Server
	room     *Room
	client   *Client
	roomCode string
}

// RegisterLocalSharer creates a local sharer for a room
// This is used when the signal server runs embedded in the sharer process
func (s *Server) RegisterLocalSharer(roomCode string, password string) *LocalSharer {
	room := s.getOrCreateRoom(roomCode)

	sharerClient := &Client{
		room:   roomCode,
		role:   "sharer",
		send:   make(chan []byte, 256),
		server: s,
	}

	room.mu.Lock()
	room.sharer = sharerClient
	room.password = password
	room.mu.Unlock()

	return &LocalSharer{
		server:   s,
		room:     room,
		client:   sharerClient,
		roomCode: roomCode,
	}
}

// Messages returns a channel that receives messages from viewers
func (ls *LocalSharer) Messages() <-chan []byte {
	return ls.client.send
}

// SendToViewer sends a message to a specific viewer by peerID
func (ls *LocalSharer) SendToViewer(peerID string, msg SignalMessage) {
	data, _ := json.Marshal(msg)

	ls.room.mu.RLock()
	defer ls.room.mu.RUnlock()

	for viewer := range ls.room.viewers {
		if viewer.peerID == peerID {
			select {
			case viewer.send <- data:
			default:
			}
			return
		}
	}
}

// SendToAllViewers broadcasts a message to all viewers
func (ls *LocalSharer) SendToAllViewers(msg SignalMessage) {
	data, _ := json.Marshal(msg)

	ls.room.mu.RLock()
	defer ls.room.mu.RUnlock()

	for viewer := range ls.room.viewers {
		select {
		case viewer.send <- data:
		default:
		}
	}
}

// GetUnassignedViewer returns the first viewer without a peerID
// Used when handling viewer-joined to find the new viewer
func (ls *LocalSharer) GetUnassignedViewer() (found bool, assignPeerID func(string)) {
	ls.room.mu.Lock()
	defer ls.room.mu.Unlock()

	for viewer := range ls.room.viewers {
		if viewer.peerID == "" {
			return true, func(peerID string) {
				viewer.peerID = peerID
			}
		}
	}
	return false, nil
}
