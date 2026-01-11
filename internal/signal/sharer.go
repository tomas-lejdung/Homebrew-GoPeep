package signal

// Sharer abstracts the signaling transport for WebRTC negotiation.
// RemoteSharer implements this interface for WebSocket-based signaling.
type Sharer interface {
	// SendToViewer sends a message to a specific peer
	SendToViewer(peerID string, msg SignalMessage)

	// SendToAllViewers broadcasts to all connected viewers
	SendToAllViewers(msg SignalMessage)

	// Messages returns channel of incoming raw messages
	Messages() <-chan []byte

	// GetUnassignedViewer returns a viewer waiting for peerID assignment
	// Returns (false, nil) if no viewer is waiting
	GetUnassignedViewer() (found bool, assignPeerID func(string))

	// SetDisconnectHandler sets callback for when connection is lost
	SetDisconnectHandler(handler func())

	// Close shuts down the sharer
	Close()
}
