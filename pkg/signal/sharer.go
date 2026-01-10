package signal

// Sharer abstracts signaling transport for both local and remote modes.
// LocalSharer (embedded server) and RemoteSharer (WebSocket) both implement this interface.
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
