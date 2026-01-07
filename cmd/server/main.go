package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

//go:embed viewer.html
var viewerHTML embed.FS

// SignalMessage represents a WebSocket signaling message
type SignalMessage struct {
	Type      string `json:"type"`
	Room      string `json:"room,omitempty"`
	Role      string `json:"role,omitempty"`
	SDP       string `json:"sdp,omitempty"`
	Candidate string `json:"candidate,omitempty"`
	Error     string `json:"error,omitempty"`
	PeerID    string `json:"peerId,omitempty"`
}

// Client represents a connected WebSocket client
type Client struct {
	conn   *websocket.Conn
	room   string
	role   string
	peerID string
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

func NewSignalServer() *SignalServer {
	return &SignalServer{
		rooms: make(map[string]*Room),
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
	}
}

func (s *SignalServer) getOrCreateRoom(code string) *Room {
	s.mu.Lock()
	defer s.mu.Unlock()

	code = normalizeRoomCode(code)
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

	if room.sharer == nil && len(room.viewers) == 0 {
		delete(s.rooms, client.room)
	}
}

func (s *SignalServer) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/ws/")
	roomCode := normalizeRoomCode(path)

	if roomCode == "" || !validateRoomCode(roomCode) {
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

func (s *SignalServer) HandleViewer(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")

	if path == "" || path == "favicon.ico" {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	content, err := viewerHTML.ReadFile("viewer.html")
	if err != nil {
		http.Error(w, "Viewer not found", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(content)
}

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

func (c *Client) writePump() {
	defer c.conn.Close()

	for message := range c.send {
		if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
			log.Printf("WebSocket write error: %v", err)
			return
		}
	}
}

func (c *Client) handleMessage(msg SignalMessage) {
	room := c.server.getOrCreateRoom(c.room)

	switch msg.Type {
	case "join":
		c.handleJoin(room, msg)
	case "offer":
		c.handleOffer(room, msg)
	case "answer":
		c.forwardToSharer(room, msg)
	case "ice":
		c.forwardICE(room, msg)
	default:
		log.Printf("Unknown message type: %s", msg.Type)
	}
}

// handleOffer assigns peerID to the first unassigned viewer and forwards the offer
func (c *Client) handleOffer(room *Room, msg SignalMessage) {
	room.mu.Lock()

	// Find the first viewer without a peerID and assign this one
	if msg.PeerID != "" {
		for viewer := range room.viewers {
			if viewer.peerID == "" {
				viewer.peerID = msg.PeerID
				log.Printf("Assigned peerID %s to viewer", msg.PeerID)
				break
			}
		}
	}

	room.mu.Unlock()

	// Now forward to the specific viewer (using forwardToViewers which checks peerID)
	c.forwardToViewers(room, msg)
}

func (c *Client) handleJoin(room *Room, msg SignalMessage) {
	room.mu.Lock()
	defer room.mu.Unlock()

	c.role = msg.Role

	if msg.Role == "sharer" {
		if room.sharer != nil {
			errMsg := SignalMessage{Type: "error", Error: "Room already has a sharer"}
			data, _ := json.Marshal(errMsg)
			c.send <- data
			return
		}
		room.sharer = c
		log.Printf("Sharer joined room %s", room.code)

		confirmMsg := SignalMessage{Type: "joined", Role: "sharer", Room: room.code}
		data, _ := json.Marshal(confirmMsg)
		c.send <- data

	} else if msg.Role == "viewer" {
		room.viewers[c] = true
		log.Printf("Viewer joined room %s (total viewers: %d)", room.code, len(room.viewers))

		confirmMsg := SignalMessage{Type: "joined", Role: "viewer", Room: room.code}
		data, _ := json.Marshal(confirmMsg)
		c.send <- data

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

// forwardToViewers sends message to viewers in the room
// If msg.PeerID is set, only sends to that specific viewer
func (c *Client) forwardToViewers(room *Room, msg SignalMessage) {
	room.mu.RLock()
	defer room.mu.RUnlock()

	data, _ := json.Marshal(msg)

	// If a specific peerID is specified, only send to that viewer
	if msg.PeerID != "" {
		for viewer := range room.viewers {
			if viewer.peerID == msg.PeerID {
				select {
				case viewer.send <- data:
				default:
				}
				return
			}
		}
		return
	}

	// Otherwise broadcast to all viewers
	for viewer := range room.viewers {
		select {
		case viewer.send <- data:
		default:
		}
	}
}

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
// Uses msg.PeerID to route to specific viewer when from sharer
func (c *Client) forwardICE(room *Room, msg SignalMessage) {
	if c.role == "sharer" {
		// From sharer to viewer - use peerID to route to specific viewer
		c.forwardToViewers(room, msg)
	} else {
		// From viewer to sharer - include this viewer's peerID
		msg.PeerID = c.peerID
		c.forwardToSharer(room, msg)
	}
}

// Room code helpers
var adjectives = []string{
	"QUICK", "LAZY", "HAPPY", "CALM", "BRAVE",
	"BRIGHT", "COOL", "DARK", "EAGER", "FAIR",
	"GENTLE", "GRAND", "GREAT", "GREEN", "BLUE",
	"RED", "GOLD", "SILVER", "WARM", "WILD",
}

var nouns = []string{
	"FROG", "TIGER", "RIVER", "CLOUD", "STONE",
	"LEAF", "BIRD", "FISH", "WOLF", "BEAR",
	"HAWK", "DEER", "LION", "EAGLE", "WHALE",
	"TREE", "LAKE", "MOON", "STAR", "WAVE",
}

var rng = rand.New(rand.NewSource(time.Now().UnixNano()))

func generateRoomCode() string {
	adj := adjectives[rng.Intn(len(adjectives))]
	noun := nouns[rng.Intn(len(nouns))]
	num := rng.Intn(100)
	return fmt.Sprintf("%s-%s-%02d", adj, noun, num)
}

func normalizeRoomCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

func validateRoomCode(code string) bool {
	parts := strings.Split(code, "-")
	if len(parts) != 3 {
		return false
	}
	return len(parts[0]) > 0 && len(parts[1]) > 0 && len(parts[2]) > 0
}

func main() {
	port := flag.Int("port", 8080, "Server port")
	flag.Parse()

	// Check for PORT env var (for cloud deployments)
	if envPort := os.Getenv("PORT"); envPort != "" {
		fmt.Sscanf(envPort, "%d", port)
	}

	server := NewSignalServer()

	mux := http.NewServeMux()
	mux.HandleFunc("/ws/", server.HandleWebSocket)
	mux.HandleFunc("/", server.HandleViewer)

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("GoPeep Signal Server starting on %s", addr)
	log.Printf("Share URL format: https://your-domain/%s", generateRoomCode())

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
