package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
)

//go:embed viewer.html
var viewerHTML embed.FS

// SignalMessage represents a WebSocket signaling message
type SignalMessage struct {
	Type      string `json:"type"`                // join, offer, answer, ice, error
	Room      string `json:"room,omitempty"`      // room code
	Role      string `json:"role,omitempty"`      // sharer or viewer
	SDP       string `json:"sdp,omitempty"`       // SDP offer/answer
	Candidate string `json:"candidate,omitempty"` // ICE candidate
	Error     string `json:"error,omitempty"`     // error message
	PeerID    string `json:"peerId,omitempty"`    // peer identifier for routing
}

// Client represents a connected WebSocket client
type Client struct {
	conn   *websocket.Conn
	room   string
	role   string // "sharer" or "viewer"
	peerID string // assigned peer ID for this viewer
	send   chan []byte
	server *SignalServer
}

// Room holds connected clients for a session
type Room struct {
	code    string
	sharer  *Client
	viewers map[*Client]bool
	mu      sync.RWMutex
}

// SignalServer manages WebSocket connections and room routing
type SignalServer struct {
	rooms    map[string]*Room
	mu       sync.RWMutex
	upgrader websocket.Upgrader
}

// NewSignalServer creates a new signaling server
func NewSignalServer() *SignalServer {
	return &SignalServer{
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
func (s *SignalServer) getOrCreateRoom(code string) *Room {
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
func (s *SignalServer) removeClient(client *Client) {
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
func (s *SignalServer) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
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
func (s *SignalServer) HandleViewer(w http.ResponseWriter, r *http.Request) {
	// Extract room code from path: /{room-code}
	path := strings.TrimPrefix(r.URL.Path, "/")

	if path == "" || path == "favicon.ico" {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	// Serve the embedded viewer.html
	content, err := viewerHTML.ReadFile("viewer.html")
	if err != nil {
		http.Error(w, "Viewer not found", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(content)
}

// readPump reads messages from the WebSocket
func (c *Client) readPump() {
	defer func() {
		c.server.removeClient(c)
		c.conn.Close()
	}()

	for {
		_, message, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket error: %v", err)
			}
			break
		}

		var msg SignalMessage
		if err := json.Unmarshal(message, &msg); err != nil {
			log.Printf("Invalid message format: %v", err)
			continue
		}

		c.handleMessage(msg)
	}
}

// writePump sends messages to the WebSocket
func (c *Client) writePump() {
	defer c.conn.Close()

	for message := range c.send {
		if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
			log.Printf("WebSocket write error: %v", err)
			return
		}
	}
}

// handleMessage processes incoming signaling messages
func (c *Client) handleMessage(msg SignalMessage) {
	room := c.server.getOrCreateRoom(c.room)

	switch msg.Type {
	case "join":
		c.handleJoin(room, msg)
	case "offer":
		c.forwardToViewers(room, msg)
	case "answer":
		c.forwardToSharer(room, msg)
	case "ice":
		c.forwardICE(room, msg)
	default:
		log.Printf("Unknown message type: %s", msg.Type)
	}
}

// handleJoin processes a join request
func (c *Client) handleJoin(room *Room, msg SignalMessage) {
	room.mu.Lock()
	defer room.mu.Unlock()

	c.role = msg.Role

	if msg.Role == "sharer" {
		if room.sharer != nil {
			// Room already has a sharer
			errMsg := SignalMessage{Type: "error", Error: "Room already has a sharer"}
			data, _ := json.Marshal(errMsg)
			c.send <- data
			return
		}
		room.sharer = c
		log.Printf("Sharer joined room %s", room.code)

		// Send confirmation
		confirmMsg := SignalMessage{Type: "joined", Role: "sharer", Room: room.code}
		data, _ := json.Marshal(confirmMsg)
		c.send <- data

	} else if msg.Role == "viewer" {
		room.viewers[c] = true
		log.Printf("Viewer joined room %s (total viewers: %d)", room.code, len(room.viewers))

		// Send confirmation
		confirmMsg := SignalMessage{Type: "joined", Role: "viewer", Room: room.code}
		data, _ := json.Marshal(confirmMsg)
		c.send <- data

		// Notify sharer about new viewer
		if room.sharer != nil {
			notifyMsg := SignalMessage{Type: "viewer-joined"}
			data, _ := json.Marshal(notifyMsg)
			select {
			case room.sharer.send <- data:
			default:
			}
		}
	}
}

// forwardToViewers sends message to all viewers in the room
func (c *Client) forwardToViewers(room *Room, msg SignalMessage) {
	room.mu.RLock()
	defer room.mu.RUnlock()

	data, _ := json.Marshal(msg)
	for viewer := range room.viewers {
		select {
		case viewer.send <- data:
		default:
			// Viewer buffer full, skip
		}
	}
}

// forwardToSharer sends message to the sharer
func (c *Client) forwardToSharer(room *Room, msg SignalMessage) {
	room.mu.RLock()
	defer room.mu.RUnlock()

	if room.sharer == nil {
		return
	}

	data, _ := json.Marshal(msg)
	select {
	case room.sharer.send <- data:
	default:
	}
}

// forwardICE forwards ICE candidates to appropriate peer(s)
func (c *Client) forwardICE(room *Room, msg SignalMessage) {
	if c.role == "sharer" {
		c.forwardToViewers(room, msg)
	} else {
		c.forwardToSharer(room, msg)
	}
}

// StartServer starts the signaling HTTP server
func (s *SignalServer) StartServer(addr string) error {
	mux := http.NewServeMux()

	// WebSocket endpoint for signaling
	mux.HandleFunc("/ws/", s.HandleWebSocket)

	// Serve viewer page for room URLs
	mux.HandleFunc("/", s.HandleViewer)

	log.Printf("Signal server starting on %s", addr)
	return http.ListenAndServe(addr, mux)
}

// GetViewerCount returns number of viewers in a room
func (s *SignalServer) GetViewerCount(roomCode string) int {
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
func (s *SignalServer) BroadcastToRoom(roomCode string, msg SignalMessage) {
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

// LocalSignalClient is used by the sharer when running embedded server
type LocalSignalClient struct {
	server   *SignalServer
	roomCode string
	onMsg    func(SignalMessage)
	mu       sync.Mutex
}

// NewLocalSignalClient creates a client for local (in-process) signaling
func NewLocalSignalClient(server *SignalServer, roomCode string) *LocalSignalClient {
	return &LocalSignalClient{
		server:   server,
		roomCode: roomCode,
	}
}

// SetMessageHandler sets the callback for incoming messages
func (c *LocalSignalClient) SetMessageHandler(handler func(SignalMessage)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onMsg = handler
}

// Send sends a message through the signaling channel
func (c *LocalSignalClient) Send(msg SignalMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	c.server.mu.RLock()
	room, exists := c.server.rooms[c.roomCode]
	c.server.mu.RUnlock()

	if !exists {
		return fmt.Errorf("room not found: %s", c.roomCode)
	}

	room.mu.RLock()
	defer room.mu.RUnlock()

	// Forward to all viewers
	for viewer := range room.viewers {
		select {
		case viewer.send <- data:
		default:
		}
	}

	return nil
}
