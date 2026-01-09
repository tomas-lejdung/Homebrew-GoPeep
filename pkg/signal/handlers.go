package signal

import (
	"encoding/json"
	"log"

	"github.com/gorilla/websocket"
)

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
		c.handleOffer(room, msg)
	case "answer", "renegotiate-answer":
		// Forward answer (initial or renegotiation) to sharer
		c.forwardToSharer(room, msg)
	case "ice":
		c.forwardICE(room, msg)
	case "streams-info", "focus-change", "stream-added", "stream-removed", "stream-activated", "stream-deactivated", "size-change":
		// Forward stream metadata from sharer to all viewers
		c.forwardToViewers(room, msg)
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

// handleJoin processes a join request
func (c *Client) handleJoin(room *Room, msg SignalMessage) {
	room.mu.Lock()
	defer room.mu.Unlock()

	c.role = msg.Role

	if msg.Role == "sharer" {
		if room.sharer != nil && room.sharer != c {
			// Close old sharer connection if exists (sharer reconnecting)
			oldSharer := room.sharer
			log.Printf("Sharer reconnecting to room %s, closing old connection", room.code)
			close(oldSharer.send)
		}
		room.sharer = c
		// Clear reservation since sharer has now joined
		room.reserved = false

		// Set room password if provided by sharer
		if msg.Password != "" {
			room.password = msg.Password
			log.Printf("Sharer joined room %s (password protected)", room.code)
		} else {
			room.password = "" // Clear any existing password
			log.Printf("Sharer joined room %s", room.code)
		}

		// Reset viewer peerIDs so they can receive new offers
		for viewer := range room.viewers {
			viewer.peerID = ""
		}

		// Send confirmation
		confirmMsg := SignalMessage{Type: "joined", Role: "sharer", Room: room.code}
		data, _ := json.Marshal(confirmMsg)
		c.send <- data

		// Notify existing viewers that sharer has reconnected
		// They will receive new offers shortly
		sharerReadyMsg := SignalMessage{Type: "sharer-ready"}
		sharerReadyData, _ := json.Marshal(sharerReadyMsg)
		for viewer := range room.viewers {
			select {
			case viewer.send <- sharerReadyData:
			default:
			}
		}

		// Also send viewer-joined for each existing viewer so sharer creates offers
		for range room.viewers {
			notifyMsg := SignalMessage{Type: "viewer-joined"}
			notifyData, _ := json.Marshal(notifyMsg)
			select {
			case c.send <- notifyData:
			default:
			}
		}

	} else if msg.Role == "viewer" {
		// Check password if room is protected
		if room.password != "" && msg.Password != room.password {
			errMsg := SignalMessage{Type: "password-required", Error: "Password required"}
			if msg.Password != "" {
				errMsg = SignalMessage{Type: "password-invalid", Error: "Invalid password"}
			}
			data, _ := json.Marshal(errMsg)
			c.send <- data
			return
		}

		// Clear peerID on rejoin so they can get a new offer
		// This handles the case where viewer rejoins after sharer stopped
		c.peerID = ""

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
