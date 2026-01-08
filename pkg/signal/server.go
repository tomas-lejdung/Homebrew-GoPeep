package signal

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"

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
	code     string
	password string // optional password for room protection
	sharer   *Client
	viewers  map[*Client]bool
	mu       sync.RWMutex
}

// Server manages WebSocket connections and room routing
type Server struct {
	rooms    map[string]*Room
	mu       sync.RWMutex
	upgrader websocket.Upgrader
}

// NewServer creates a new signaling server
func NewServer() *Server {
	return &Server{
		rooms: make(map[string]*Room),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin: func(r *http.Request) bool {
				return true // Allow all origins for development
			},
		},
	}
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

	// WebSocket endpoint for signaling
	mux.HandleFunc("/ws/", s.HandleWebSocket)

	// Serve viewer page for room URLs
	mux.HandleFunc("/", s.HandleViewer)

	log.Printf("Signal server starting on %s", addr)
	return http.ListenAndServe(addr, mux)
}

// GetViewerCount returns number of viewers in a room
func (s *Server) GetViewerCount(roomCode string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	room, exists := s.rooms[NormalizeRoomCode(roomCode)]
	if !exists {
		return 0
	}

	room.mu.RLock()
	defer room.mu.RUnlock()

	return len(room.viewers)
}

// BroadcastToRoom sends a message to all clients in a room
func (s *Server) BroadcastToRoom(roomCode string, msg SignalMessage) {
	s.mu.RLock()
	room, exists := s.rooms[NormalizeRoomCode(roomCode)]
	s.mu.RUnlock()

	if !exists {
		return
	}

	room.mu.RLock()
	defer room.mu.RUnlock()

	data, _ := json.Marshal(msg)

	if room.sharer != nil {
		select {
		case room.sharer.send <- data:
		default:
		}
	}

	for viewer := range room.viewers {
		select {
		case viewer.send <- data:
		default:
		}
	}
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

// GetViewerByPeerID returns the viewer client by peerID (for sending messages)
func (ls *LocalSharer) GetViewerByPeerID(peerID string) (send func(SignalMessage), found bool) {
	ls.room.mu.RLock()
	defer ls.room.mu.RUnlock()

	for viewer := range ls.room.viewers {
		if viewer.peerID == peerID {
			return func(msg SignalMessage) {
				data, _ := json.Marshal(msg)
				select {
				case viewer.send <- data:
				default:
				}
			}, true
		}
	}
	return nil, false
}
