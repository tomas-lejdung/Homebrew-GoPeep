package signal

import (
	"log"
	"sync"

	"github.com/gorilla/websocket"
)

// RemoteSharer implements Sharer for remote WebSocket connection to signal server
type RemoteSharer struct {
	conn         *websocket.Conn
	connMu       sync.Mutex
	msgChan      chan []byte
	done         chan struct{}
	onDisconnect func()
	closed       bool
	closeMu      sync.Mutex
}

// NewRemoteSharer creates a sharer for remote server mode
func NewRemoteSharer(conn *websocket.Conn) *RemoteSharer {
	rs := &RemoteSharer{
		conn:    conn,
		msgChan: make(chan []byte, 100),
		done:    make(chan struct{}),
	}
	go rs.readLoop()
	return rs
}

func (rs *RemoteSharer) readLoop() {
	defer func() {
		close(rs.msgChan)
		rs.closeMu.Lock()
		if rs.onDisconnect != nil && !rs.closed {
			rs.onDisconnect()
		}
		rs.closeMu.Unlock()
	}()

	for {
		_, data, err := rs.conn.ReadMessage()
		if err != nil {
			log.Printf("WebSocket read error: %v", err)
			return
		}
		select {
		case rs.msgChan <- data:
		case <-rs.done:
			return
		}
	}
}

// SendToViewer sends a message to a specific viewer
func (rs *RemoteSharer) SendToViewer(peerID string, msg SignalMessage) {
	rs.closeMu.Lock()
	closed := rs.closed
	rs.closeMu.Unlock()
	if closed {
		return
	}

	rs.connMu.Lock()
	defer rs.connMu.Unlock()
	if err := rs.conn.WriteJSON(msg); err != nil {
		log.Printf("Send to %s failed: %v", peerID, err)
	}
}

// SendToAllViewers broadcasts to all connected viewers
// In remote mode, the server handles broadcast distribution
func (rs *RemoteSharer) SendToAllViewers(msg SignalMessage) {
	rs.closeMu.Lock()
	closed := rs.closed
	rs.closeMu.Unlock()
	if closed {
		return
	}

	rs.connMu.Lock()
	defer rs.connMu.Unlock()
	if err := rs.conn.WriteJSON(msg); err != nil {
		log.Printf("Broadcast failed: %v", err)
	}
}

// Messages returns channel of incoming raw messages
func (rs *RemoteSharer) Messages() <-chan []byte {
	return rs.msgChan
}

// GetUnassignedViewer returns a viewer waiting for peerID assignment
// In remote mode, the server manages viewer assignment, so we always return true
func (rs *RemoteSharer) GetUnassignedViewer() (bool, func(string)) {
	// Remote server manages viewer assignment
	// When we get viewer-joined, viewer is already "assigned" server-side
	return true, func(peerID string) {
		// No-op for remote - server handles assignment
	}
}

// SetDisconnectHandler sets callback for when connection is lost
func (rs *RemoteSharer) SetDisconnectHandler(handler func()) {
	rs.closeMu.Lock()
	rs.onDisconnect = handler
	rs.closeMu.Unlock()
}

// Close shuts down the sharer
func (rs *RemoteSharer) Close() {
	rs.closeMu.Lock()
	defer rs.closeMu.Unlock()
	if !rs.closed {
		rs.closed = true
		close(rs.done)
		rs.conn.Close()
	}
}
