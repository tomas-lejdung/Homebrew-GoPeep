package signal

import (
	"encoding/json"
	"fmt"
	"sync"
)

// LocalSignalClient is used by the sharer when running embedded server
type LocalSignalClient struct {
	server   *Server
	roomCode string
	onMsg    func(SignalMessage)
	mu       sync.Mutex
}

// NewLocalSignalClient creates a client for local (in-process) signaling
func NewLocalSignalClient(server *Server, roomCode string) *LocalSignalClient {
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
