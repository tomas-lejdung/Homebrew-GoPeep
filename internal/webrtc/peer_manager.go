package webrtc

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	pwebrtc "github.com/pion/webrtc/v3"
	sig "github.com/tomaslejdung/gopeep/internal/signal"
)

// PeerManager manages WebRTC connections with multiple video tracks
type PeerManager struct {
	config         pwebrtc.Configuration
	iceConfig      ICEConfig
	codecType      CodecType
	connections    map[string]*PeerInfo // peerID -> peer info with senders
	viewerStates   map[string]*ViewerInfo
	trackCounter   int // Monotonically increasing track counter (for legacy AddTrack only)
	mu             sync.RWMutex
	renegMu        sync.Mutex // Mutex to serialize renegotiations
	onICE          func(peerID string, candidate string)
	onConnected    func(peerID string)
	onDisconnect   func(peerID string)
	onFocusChange  func(trackID string)                            // Called when focus changes to a new track
	onSizeChange   func(trackID string, width, height int)         // Called when focused track dimensions change
	onCursorUpdate func(trackID string, x, y float64, inView bool) // Called with cursor position updates

	// Renegotiation callbacks
	onRenegotiate   func(peerID string, offer string)
	onStreamAdded   func(info sig.StreamInfo)
	onStreamRemoved func(trackID string)

	// Pre-allocated track slots - THE single source of truth for tracks
	slots      [4]*TrackSlot // Pre-allocated track slots (matches MaxCaptureInstances)
	slotsReady bool          // Whether slots have been initialized

	// Stream activation callbacks (no renegotiation needed)
	onStreamActivated   func(info sig.StreamInfo)
	onStreamDeactivated func(trackID string)

	// DataChannel callbacks
	onDataChannelOpen func(peerID string)
}

// NewPeerManager creates a new multi-track peer manager
func NewPeerManager(iceConfig ICEConfig, codecType CodecType) (*PeerManager, error) {
	// Build ICE servers list
	iceServers := make([]pwebrtc.ICEServer, 0)

	if !iceConfig.ForceRelay {
		iceServers = append(iceServers, DefaultICEServers...)
	}

	if iceConfig.TURNServer != "" {
		turnServer := pwebrtc.ICEServer{
			URLs: []string{iceConfig.TURNServer},
		}
		if iceConfig.TURNUser != "" {
			turnServer.Username = iceConfig.TURNUser
			turnServer.Credential = iceConfig.TURNPass
			turnServer.CredentialType = pwebrtc.ICECredentialTypePassword
		}
		iceServers = append(iceServers, turnServer)
	}

	iceTransportPolicy := pwebrtc.ICETransportPolicyAll
	if iceConfig.ForceRelay {
		iceTransportPolicy = pwebrtc.ICETransportPolicyRelay
	}

	return &PeerManager{
		config: pwebrtc.Configuration{
			ICEServers:         iceServers,
			ICETransportPolicy: iceTransportPolicy,
		},
		iceConfig:    iceConfig,
		codecType:    codecType,
		connections:  make(map[string]*PeerInfo),
		viewerStates: make(map[string]*ViewerInfo),
	}, nil
}

// getMimeType returns the MIME type for the current codec
func (mpm *PeerManager) getMimeType() string {
	return GetMimeType(mpm.codecType)
}

// SetICECallback sets callback for ICE candidates
func (mpm *PeerManager) SetICECallback(callback func(peerID string, candidate string)) {
	mpm.onICE = callback
}

// SetConnectionCallbacks sets callbacks for connection state changes
func (mpm *PeerManager) SetConnectionCallbacks(onConnected, onDisconnect func(peerID string)) {
	mpm.onConnected = onConnected
	mpm.onDisconnect = onDisconnect
}

// SetFocusChangeCallback sets callback for when focus changes between tracks
func (mpm *PeerManager) SetFocusChangeCallback(callback func(trackID string)) {
	mpm.onFocusChange = callback
}

// NotifyFocusChange notifies that focus has changed to a new track
func (mpm *PeerManager) NotifyFocusChange(trackID string) {
	if mpm.onFocusChange != nil {
		mpm.onFocusChange(trackID)
	}
}

// SetSizeChangeCallback sets callback for when focused track dimensions change
func (mpm *PeerManager) SetSizeChangeCallback(callback func(trackID string, width, height int)) {
	mpm.onSizeChange = callback
}

// NotifySizeChange notifies that the focused track dimensions have changed
func (mpm *PeerManager) NotifySizeChange(trackID string, width, height int) {
	if mpm.onSizeChange != nil {
		mpm.onSizeChange(trackID, width, height)
	}
}

// SetCursorCallback sets callback for cursor position updates
func (mpm *PeerManager) SetCursorCallback(callback func(trackID string, x, y float64, inView bool)) {
	mpm.onCursorUpdate = callback
}

// NotifyCursorUpdate notifies about cursor position changes
func (mpm *PeerManager) NotifyCursorUpdate(trackID string, x, y float64, inView bool) {
	if mpm.onCursorUpdate != nil {
		mpm.onCursorUpdate(trackID, x, y, inView)
	}
}

// CreateOffer creates an SDP offer for a new viewer with all tracks
func (mpm *PeerManager) CreateOffer(peerID string) (string, error) {
	mpm.mu.Lock()
	defer mpm.mu.Unlock()

	// Create peer connection
	pc, err := pwebrtc.NewPeerConnection(mpm.config)
	if err != nil {
		return "", fmt.Errorf("failed to create peer connection: %w", err)
	}

	// Create PeerInfo to track senders
	peerInfo := &PeerInfo{
		PC:      pc,
		Senders: make(map[string]*pwebrtc.RTPSender),
	}

	// Create ordered, reliable DataChannel for control messages
	ordered := true
	dcOptions := &pwebrtc.DataChannelInit{
		Ordered: &ordered,
	}
	dc, err := pc.CreateDataChannel("gopeep-control", dcOptions)
	if err != nil {
		pc.Close()
		return "", fmt.Errorf("failed to create data channel: %w", err)
	}
	peerInfo.ControlDC = dc

	// Set up DataChannel event handlers
	dc.OnOpen(func() {
		log.Printf("DataChannel opened for peer %s", peerID)
		if mpm.onDataChannelOpen != nil {
			mpm.onDataChannelOpen(peerID)
		}
	})
	dc.OnClose(func() {
		log.Printf("DataChannel closed for peer %s", peerID)
	})
	dc.OnError(func(err error) {
		log.Printf("DataChannel error for peer %s: %v", peerID, err)
	})

	// Add tracks to the peer connection
	// If slots are ready, add ALL pre-allocated slots (enables instant window sharing)
	// Otherwise, fall back to adding only active tracks (legacy mode)
	if mpm.slotsReady {
		// Add all 4 pre-allocated track slots
		// This eliminates the need for renegotiation when adding new windows
		log.Printf("CreateOffer: Using pre-allocated slots (4 tracks)")
		for i := 0; i < 4; i++ {
			slot := mpm.slots[i]
			if slot == nil {
				continue
			}
			sender, err := pc.AddTrack(slot.Track)
			if err != nil {
				pc.Close()
				return "", fmt.Errorf("failed to add track slot %d: %w", i, err)
			}
			peerInfo.Senders[slot.TrackID] = sender
		}
	} else {
		// Legacy mode: add only active tracks (requires renegotiation for new windows)
		// This path is rarely hit since slots are initialized before viewers connect
		log.Printf("CreateOffer: Using legacy mode (only active tracks from slots)")
		for i := 0; i < 4; i++ {
			slot := mpm.slots[i]
			if slot != nil && slot.Active && slot.Info != nil {
				sender, err := pc.AddTrack(slot.Track)
				if err != nil {
					pc.Close()
					return "", fmt.Errorf("failed to add video track %s: %w", slot.TrackID, err)
				}
				peerInfo.Senders[slot.TrackID] = sender
			}
		}
	}

	// Handle ICE candidates
	pc.OnICECandidate(func(candidate *pwebrtc.ICECandidate) {
		if candidate == nil || mpm.onICE == nil {
			return
		}
		candidateJSON := candidate.ToJSON()
		candidateStr := fmt.Sprintf(`{"candidate":"%s","sdpMid":"%s","sdpMLineIndex":%d}`,
			candidateJSON.Candidate, *candidateJSON.SDPMid, *candidateJSON.SDPMLineIndex)
		mpm.onICE(peerID, candidateStr)
	})

	// Track viewer state
	mpm.viewerStates[peerID] = &ViewerInfo{
		PeerID:         peerID,
		State:          "connecting",
		ConnectionType: "unknown",
	}

	// Handle connection state
	pc.OnConnectionStateChange(func(state pwebrtc.PeerConnectionState) {
		log.Printf("Peer %s connection state: %s", peerID, state.String())

		mpm.mu.Lock()
		if info, ok := mpm.viewerStates[peerID]; ok {
			info.State = state.String()
			if state == pwebrtc.PeerConnectionStateConnected {
				info.ConnectedAt = time.Now()
				info.ConnectionType = mpm.detectConnectionType(pc)
			}
		}
		mpm.mu.Unlock()

		switch state {
		case pwebrtc.PeerConnectionStateConnected:
			if mpm.onConnected != nil {
				mpm.onConnected(peerID)
			}
		case pwebrtc.PeerConnectionStateDisconnected, pwebrtc.PeerConnectionStateFailed, pwebrtc.PeerConnectionStateClosed:
			if mpm.onDisconnect != nil {
				mpm.onDisconnect(peerID)
			}
			mpm.removePeer(peerID)
		}
	})

	// Create offer
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		pc.Close()
		return "", fmt.Errorf("failed to create offer: %w", err)
	}

	// Set local description
	err = pc.SetLocalDescription(offer)
	if err != nil {
		pc.Close()
		return "", fmt.Errorf("failed to set local description: %w", err)
	}

	// Wait for ICE gathering
	gatherComplete := pwebrtc.GatheringCompletePromise(pc)
	<-gatherComplete

	// Store connection with sender info
	mpm.connections[peerID] = peerInfo

	return pc.LocalDescription().SDP, nil
}

// HandleAnswer processes an SDP answer
func (mpm *PeerManager) HandleAnswer(peerID string, sdp string) error {
	mpm.mu.RLock()
	peerInfo, exists := mpm.connections[peerID]
	mpm.mu.RUnlock()

	if !exists {
		return fmt.Errorf("peer not found: %s", peerID)
	}

	answer := pwebrtc.SessionDescription{
		Type: pwebrtc.SDPTypeAnswer,
		SDP:  sdp,
	}

	return peerInfo.PC.SetRemoteDescription(answer)
}

// AddICECandidate adds an ICE candidate
func (mpm *PeerManager) AddICECandidate(peerID string, candidateJSON string) error {
	mpm.mu.RLock()
	peerInfo, exists := mpm.connections[peerID]
	mpm.mu.RUnlock()

	if !exists {
		return fmt.Errorf("peer not found: %s", peerID)
	}

	var candidate pwebrtc.ICECandidateInit
	if err := json.Unmarshal([]byte(candidateJSON), &candidate); err != nil {
		return fmt.Errorf("failed to parse ICE candidate: %w", err)
	}

	return peerInfo.PC.AddICECandidate(candidate)
}

// detectConnectionType checks if connection is direct or relayed
func (mpm *PeerManager) detectConnectionType(pc *pwebrtc.PeerConnection) string {
	stats := pc.GetStats()

	for _, stat := range stats {
		if candidatePair, ok := stat.(pwebrtc.ICECandidatePairStats); ok {
			if candidatePair.State == pwebrtc.StatsICECandidatePairStateSucceeded {
				for _, s := range stats {
					if localCandidate, ok := s.(pwebrtc.ICECandidateStats); ok {
						if localCandidate.ID == candidatePair.LocalCandidateID {
							switch localCandidate.CandidateType {
							case pwebrtc.ICECandidateTypeRelay:
								return "relay"
							case pwebrtc.ICECandidateTypeHost:
								return "direct"
							case pwebrtc.ICECandidateTypeSrflx, pwebrtc.ICECandidateTypePrflx:
								return "direct"
							}
						}
					}
				}
			}
		}
	}
	return "unknown"
}

// removePeer removes a peer connection
func (mpm *PeerManager) removePeer(peerID string) {
	mpm.mu.Lock()
	defer mpm.mu.Unlock()

	if peerInfo, exists := mpm.connections[peerID]; exists {
		peerInfo.PC.Close()
		delete(mpm.connections, peerID)
	}
	delete(mpm.viewerStates, peerID)
}

// GetViewerInfo returns information about connected viewers
func (mpm *PeerManager) GetViewerInfo() []ViewerInfo {
	mpm.mu.RLock()
	defer mpm.mu.RUnlock()

	viewers := make([]ViewerInfo, 0, len(mpm.viewerStates))
	for _, info := range mpm.viewerStates {
		viewers = append(viewers, *info)
	}
	return viewers
}

// GetConnectionCount returns number of active connections
func (mpm *PeerManager) GetConnectionCount() int {
	mpm.mu.RLock()
	defer mpm.mu.RUnlock()
	return len(mpm.connections)
}

// Close closes all peer connections
func (mpm *PeerManager) Close() {
	mpm.mu.Lock()
	defer mpm.mu.Unlock()

	for id, peerInfo := range mpm.connections {
		peerInfo.PC.Close()
		delete(mpm.connections, id)
	}
}

// CloseAllConnections closes all peer connections but keeps the PeerManager usable
// This forces viewers to reconnect with a fresh state
func (mpm *PeerManager) CloseAllConnections() {
	mpm.mu.Lock()
	defer mpm.mu.Unlock()

	for id, peerInfo := range mpm.connections {
		log.Printf("Closing peer connection: %s", id)
		peerInfo.PC.Close()
		delete(mpm.connections, id)
	}
}

// GetCodecType returns the codec type
func (mpm *PeerManager) GetCodecType() CodecType {
	return mpm.codecType
}

// SetCodecType updates the codec type for new tracks
func (mpm *PeerManager) SetCodecType(codecType CodecType) {
	mpm.mu.Lock()
	defer mpm.mu.Unlock()
	mpm.codecType = codecType
}

// SetRenegotiateCallback sets callback for when renegotiation offer is ready
func (mpm *PeerManager) SetRenegotiateCallback(callback func(peerID string, offer string)) {
	mpm.onRenegotiate = callback
}

// SetStreamChangeCallbacks sets callbacks for stream add/remove events
func (mpm *PeerManager) SetStreamChangeCallbacks(onAdded func(info sig.StreamInfo), onRemoved func(trackID string)) {
	mpm.onStreamAdded = onAdded
	mpm.onStreamRemoved = onRemoved
}

// AddTrackToAllPeers adds a track to all existing peer connections
func (mpm *PeerManager) AddTrackToAllPeers(trackInfo *StreamTrackInfo) error {
	mpm.mu.Lock()
	defer mpm.mu.Unlock()

	log.Printf("AddTrackToAllPeers: Adding track %s to %d peer connections", trackInfo.TrackID, len(mpm.connections))

	if len(mpm.connections) == 0 {
		log.Printf("AddTrackToAllPeers: No peer connections to add track to")
		return nil
	}

	for peerID, peerInfo := range mpm.connections {
		log.Printf("AddTrackToAllPeers: Adding track %s to peer %s (state: %s)",
			trackInfo.TrackID, peerID, peerInfo.PC.ConnectionState().String())
		sender, err := peerInfo.PC.AddTrack(trackInfo.Track)
		if err != nil {
			log.Printf("AddTrackToAllPeers: Failed to add track %s to peer %s: %v", trackInfo.TrackID, peerID, err)
			continue
		}
		peerInfo.Senders[trackInfo.TrackID] = sender
		log.Printf("AddTrackToAllPeers: Successfully added track %s to peer %s", trackInfo.TrackID, peerID)
	}
	return nil
}

// RemoveTrackFromAllPeers removes a track from all existing peer connections
func (mpm *PeerManager) RemoveTrackFromAllPeers(trackID string) error {
	mpm.mu.Lock()
	defer mpm.mu.Unlock()

	for peerID, peerInfo := range mpm.connections {
		if sender, ok := peerInfo.Senders[trackID]; ok {
			if err := peerInfo.PC.RemoveTrack(sender); err != nil {
				log.Printf("Failed to remove track %s from peer %s: %v", trackID, peerID, err)
				continue
			}
			delete(peerInfo.Senders, trackID)
			log.Printf("Removed track %s from peer %s", trackID, peerID)
		}
	}
	return nil
}

// RenegotiateAllPeers triggers renegotiation with all connected peers
// Runs in a goroutine to not block the caller
func (mpm *PeerManager) RenegotiateAllPeers() {
	go func() {
		// Serialize renegotiations to prevent race conditions
		mpm.renegMu.Lock()
		defer mpm.renegMu.Unlock()

		mpm.mu.RLock()
		peerIDs := make([]string, 0, len(mpm.connections))
		for id := range mpm.connections {
			peerIDs = append(peerIDs, id)
		}
		mpm.mu.RUnlock()

		log.Printf("Renegotiating with %d peers", len(peerIDs))

		// Process each peer sequentially to avoid race conditions
		for _, peerID := range peerIDs {
			mpm.renegotiatePeer(peerID)
		}
	}()
}

// renegotiatePeer creates a new offer for a specific peer
func (mpm *PeerManager) renegotiatePeer(peerID string) {
	mpm.mu.Lock()
	peerInfo, exists := mpm.connections[peerID]
	if !exists {
		mpm.mu.Unlock()
		log.Printf("Renegotiation: peer %s not found", peerID)
		return
	}

	// Check if already renegotiating
	if peerInfo.Renegotiating {
		mpm.mu.Unlock()
		log.Printf("Renegotiation: peer %s already renegotiating, skipping", peerID)
		return
	}
	peerInfo.Renegotiating = true
	mpm.mu.Unlock()

	// Ensure we clear the flag when done
	defer func() {
		mpm.mu.Lock()
		if pi, ok := mpm.connections[peerID]; ok {
			pi.Renegotiating = false
		}
		mpm.mu.Unlock()
	}()

	// Check signaling state - can only create offer in stable state
	signalingState := peerInfo.PC.SignalingState()
	log.Printf("Renegotiation: peer %s signaling state: %s", peerID, signalingState.String())

	if signalingState != pwebrtc.SignalingStateStable {
		log.Printf("Renegotiation: peer %s not in stable state, waiting...", peerID)
		// Wait for state to become stable (with timeout)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
			signalingState = peerInfo.PC.SignalingState()
			if signalingState == pwebrtc.SignalingStateStable {
				break
			}
		}
		if signalingState != pwebrtc.SignalingStateStable {
			log.Printf("Renegotiation: peer %s still not stable after wait, skipping (state: %s)", peerID, signalingState.String())
			return
		}
	}

	// Check connection state
	connState := peerInfo.PC.ConnectionState()
	log.Printf("Renegotiation: peer %s connection state: %s", peerID, connState.String())

	if connState != pwebrtc.PeerConnectionStateConnected {
		log.Printf("Renegotiation: peer %s not connected, skipping", peerID)
		return
	}

	// Create new offer
	offer, err := peerInfo.PC.CreateOffer(nil)
	if err != nil {
		log.Printf("Renegotiation: failed to create offer for %s: %v", peerID, err)
		return
	}

	if err := peerInfo.PC.SetLocalDescription(offer); err != nil {
		log.Printf("Renegotiation: failed to set local description for %s: %v", peerID, err)
		return
	}

	// Wait for ICE gathering to complete with timeout
	gatherComplete := pwebrtc.GatheringCompletePromise(peerInfo.PC)
	select {
	case <-gatherComplete:
		log.Printf("Renegotiation: ICE gathering complete for %s", peerID)
	case <-time.After(10 * time.Second):
		log.Printf("Renegotiation: ICE gathering timeout for %s", peerID)
		return
	}

	log.Printf("Renegotiation: sending offer to peer %s (SDP length: %d)", peerID, len(peerInfo.PC.LocalDescription().SDP))

	if mpm.onRenegotiate != nil {
		mpm.onRenegotiate(peerID, peerInfo.PC.LocalDescription().SDP)
	}
}

// HandleRenegotiateAnswer processes an SDP answer from renegotiation
func (mpm *PeerManager) HandleRenegotiateAnswer(peerID string, sdp string) error {
	mpm.mu.RLock()
	peerInfo, exists := mpm.connections[peerID]
	mpm.mu.RUnlock()

	if !exists {
		return fmt.Errorf("peer not found: %s", peerID)
	}

	answer := pwebrtc.SessionDescription{
		Type: pwebrtc.SDPTypeAnswer,
		SDP:  sdp,
	}

	log.Printf("Renegotiation: received answer from peer %s, setting remote description", peerID)
	err := peerInfo.PC.SetRemoteDescription(answer)
	if err != nil {
		log.Printf("Renegotiation: failed to set remote description for %s: %v", peerID, err)
		return err
	}

	log.Printf("Renegotiation: complete for peer %s, signaling state: %s, connection state: %s",
		peerID, peerInfo.PC.SignalingState().String(), peerInfo.PC.ConnectionState().String())
	return nil
}

// NotifyStreamAdded notifies that a stream was added
func (mpm *PeerManager) NotifyStreamAdded(info sig.StreamInfo) {
	if mpm.onStreamAdded != nil {
		mpm.onStreamAdded(info)
	}
}

// NotifyStreamRemoved notifies that a stream was removed
func (mpm *PeerManager) NotifyStreamRemoved(trackID string) {
	if mpm.onStreamRemoved != nil {
		mpm.onStreamRemoved(trackID)
	}
}

// SetDataChannelOpenCallback sets callback for when DataChannel opens
func (mpm *PeerManager) SetDataChannelOpenCallback(callback func(peerID string)) {
	mpm.onDataChannelOpen = callback
}

// SendControlMessage sends a JSON message via DataChannel to a specific peer
// Returns true if sent successfully, false if DataChannel is not open
func (mpm *PeerManager) SendControlMessage(peerID string, msg interface{}) bool {
	mpm.mu.RLock()
	peerInfo, exists := mpm.connections[peerID]
	mpm.mu.RUnlock()

	if !exists || peerInfo.ControlDC == nil ||
		peerInfo.ControlDC.ReadyState() != pwebrtc.DataChannelStateOpen {
		return false
	}

	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Failed to marshal control message: %v", err)
		return false
	}

	if err := peerInfo.ControlDC.SendText(string(data)); err != nil {
		log.Printf("Failed to send control message to %s: %v", peerID, err)
		return false
	}

	return true
}

// BroadcastControlMessage sends a JSON message to all peers via DataChannel
// Returns list of peerIDs where DataChannel was not open (need WebSocket fallback)
func (mpm *PeerManager) BroadcastControlMessage(msg interface{}) []string {
	mpm.mu.RLock()
	defer mpm.mu.RUnlock()

	data, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Failed to marshal control message: %v", err)
		return nil
	}

	var needWebSocket []string
	dataStr := string(data)
	for peerID, info := range mpm.connections {
		if info.ControlDC == nil ||
			info.ControlDC.ReadyState() != pwebrtc.DataChannelStateOpen {
			needWebSocket = append(needWebSocket, peerID)
			continue
		}
		if err := info.ControlDC.SendText(dataStr); err != nil {
			log.Printf("Failed to send control message to %s via DC: %v", peerID, err)
			needWebSocket = append(needWebSocket, peerID)
		}
	}
	return needWebSocket
}
