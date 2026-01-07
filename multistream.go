package main

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
	sig "github.com/tomaslejdung/gopeep/pkg/signal"
)

// StreamTrackInfo holds information about a single stream/track
type StreamTrackInfo struct {
	TrackID    string // e.g., "video0", "video1"
	WindowID   uint32
	WindowName string
	AppName    string
	Track      *webrtc.TrackLocalStaticSample
	IsFocused  bool
	Width      int
	Height     int
}

// PeerInfo holds peer connection and associated RTP senders
type PeerInfo struct {
	PC            *webrtc.PeerConnection
	Senders       map[string]*webrtc.RTPSender // trackID -> sender
	renegotiating bool                         // Whether renegotiation is in progress
}

// MultiPeerManager manages WebRTC connections with multiple video tracks
type MultiPeerManager struct {
	config        webrtc.Configuration
	iceConfig     ICEConfig
	codecType     CodecType
	tracks        map[string]*StreamTrackInfo // trackID -> track info
	connections   map[string]*PeerInfo        // peerID -> peer info with senders
	viewerStates  map[string]*ViewerInfo
	trackCounter  int // Monotonically increasing track counter
	mu            sync.RWMutex
	renegMu       sync.Mutex // Mutex to serialize renegotiations
	onICE         func(peerID string, candidate string)
	onConnected   func(peerID string)
	onDisconnect  func(peerID string)
	onFocusChange func(trackID string) // Called when focus changes to a new track

	// Renegotiation callbacks
	onRenegotiate   func(peerID string, offer string)
	onStreamAdded   func(info sig.StreamInfo)
	onStreamRemoved func(trackID string)
}

// NewMultiPeerManager creates a new multi-track peer manager
func NewMultiPeerManager(iceConfig ICEConfig, codecType CodecType) (*MultiPeerManager, error) {
	// Build ICE servers list
	iceServers := make([]webrtc.ICEServer, 0)

	if !iceConfig.ForceRelay {
		iceServers = append(iceServers, defaultICEServers...)
	}

	if iceConfig.TURNServer != "" {
		turnServer := webrtc.ICEServer{
			URLs: []string{iceConfig.TURNServer},
		}
		if iceConfig.TURNUser != "" {
			turnServer.Username = iceConfig.TURNUser
			turnServer.Credential = iceConfig.TURNPass
			turnServer.CredentialType = webrtc.ICECredentialTypePassword
		}
		iceServers = append(iceServers, turnServer)
	}

	iceTransportPolicy := webrtc.ICETransportPolicyAll
	if iceConfig.ForceRelay {
		iceTransportPolicy = webrtc.ICETransportPolicyRelay
	}

	return &MultiPeerManager{
		config: webrtc.Configuration{
			ICEServers:         iceServers,
			ICETransportPolicy: iceTransportPolicy,
		},
		iceConfig:    iceConfig,
		codecType:    codecType,
		tracks:       make(map[string]*StreamTrackInfo),
		connections:  make(map[string]*PeerInfo),
		viewerStates: make(map[string]*ViewerInfo),
	}, nil
}

// AddTrack creates a new video track for a window
func (mpm *MultiPeerManager) AddTrack(windowID uint32, windowName, appName string) (*StreamTrackInfo, error) {
	mpm.mu.Lock()
	defer mpm.mu.Unlock()

	// Generate track ID using monotonic counter (never reuses IDs)
	trackID := fmt.Sprintf("video%d", mpm.trackCounter)
	mpm.trackCounter++

	// Check if already have a track for this window
	for _, t := range mpm.tracks {
		if t.WindowID == windowID {
			return nil, fmt.Errorf("track already exists for window %d", windowID)
		}
	}

	// Determine MIME type
	var mimeType string
	switch mpm.codecType {
	case CodecVP9:
		mimeType = webrtc.MimeTypeVP9
	case CodecH264:
		mimeType = webrtc.MimeTypeH264
	default:
		mimeType = webrtc.MimeTypeVP8
	}

	// Create video track
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: mimeType},
		trackID,
		fmt.Sprintf("gopeep-%d", windowID),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create video track: %w", err)
	}

	info := &StreamTrackInfo{
		TrackID:    trackID,
		WindowID:   windowID,
		WindowName: windowName,
		AppName:    appName,
		Track:      track,
		IsFocused:  len(mpm.tracks) == 0, // First track is focused by default
	}

	mpm.tracks[trackID] = info
	return info, nil
}

// RemoveTrack removes a video track
func (mpm *MultiPeerManager) RemoveTrack(trackID string) {
	mpm.mu.Lock()
	defer mpm.mu.Unlock()
	delete(mpm.tracks, trackID)
}

// GetTracks returns all current tracks in sorted order by TrackID
func (mpm *MultiPeerManager) GetTracks() []*StreamTrackInfo {
	mpm.mu.RLock()
	defer mpm.mu.RUnlock()

	// Collect and sort track IDs to ensure consistent ordering
	trackIDs := make([]string, 0, len(mpm.tracks))
	for id := range mpm.tracks {
		trackIDs = append(trackIDs, id)
	}
	sort.Strings(trackIDs)

	tracks := make([]*StreamTrackInfo, 0, len(mpm.tracks))
	for _, id := range trackIDs {
		tracks = append(tracks, mpm.tracks[id])
	}
	return tracks
}

// SetFocusedTrack sets which track is focused
func (mpm *MultiPeerManager) SetFocusedTrack(trackID string) {
	mpm.mu.Lock()
	defer mpm.mu.Unlock()

	for id, t := range mpm.tracks {
		t.IsFocused = (id == trackID)
	}
}

// SetFocusedWindow sets focus based on window ID
func (mpm *MultiPeerManager) SetFocusedWindow(windowID uint32) string {
	mpm.mu.Lock()
	defer mpm.mu.Unlock()

	var focusedTrackID string
	for _, t := range mpm.tracks {
		if t.WindowID == windowID {
			t.IsFocused = true
			focusedTrackID = t.TrackID
		} else {
			t.IsFocused = false
		}
	}
	return focusedTrackID
}

// GetFocusedTrack returns the currently focused track
func (mpm *MultiPeerManager) GetFocusedTrack() *StreamTrackInfo {
	mpm.mu.RLock()
	defer mpm.mu.RUnlock()

	for _, t := range mpm.tracks {
		if t.IsFocused {
			return t
		}
	}
	return nil
}

// SetICECallback sets callback for ICE candidates
func (mpm *MultiPeerManager) SetICECallback(callback func(peerID string, candidate string)) {
	mpm.onICE = callback
}

// SetConnectionCallbacks sets callbacks for connection state changes
func (mpm *MultiPeerManager) SetConnectionCallbacks(onConnected, onDisconnect func(peerID string)) {
	mpm.onConnected = onConnected
	mpm.onDisconnect = onDisconnect
}

// SetFocusChangeCallback sets callback for when focus changes between tracks
func (mpm *MultiPeerManager) SetFocusChangeCallback(callback func(trackID string)) {
	mpm.onFocusChange = callback
}

// NotifyFocusChange notifies that focus has changed to a new track
func (mpm *MultiPeerManager) NotifyFocusChange(trackID string) {
	if mpm.onFocusChange != nil {
		log.Printf("NotifyFocusChange: callback exists, calling with trackID: %s", trackID)
		mpm.onFocusChange(trackID)
	} else {
		log.Printf("NotifyFocusChange: callback is NIL! trackID: %s", trackID)
	}
}

// CreateOffer creates an SDP offer for a new viewer with all tracks
func (mpm *MultiPeerManager) CreateOffer(peerID string) (string, error) {
	mpm.mu.Lock()
	defer mpm.mu.Unlock()

	// Create peer connection
	pc, err := webrtc.NewPeerConnection(mpm.config)
	if err != nil {
		return "", fmt.Errorf("failed to create peer connection: %w", err)
	}

	// Create PeerInfo to track senders
	peerInfo := &PeerInfo{
		PC:      pc,
		Senders: make(map[string]*webrtc.RTPSender),
	}

	// Add all video tracks in sorted order (video0, video1, video2...)
	// This ensures the viewer receives tracks in the same order as their IDs
	trackIDs := make([]string, 0, len(mpm.tracks))
	for id := range mpm.tracks {
		trackIDs = append(trackIDs, id)
	}
	sort.Strings(trackIDs)

	for _, id := range trackIDs {
		trackInfo := mpm.tracks[id]
		sender, err := pc.AddTrack(trackInfo.Track)
		if err != nil {
			pc.Close()
			return "", fmt.Errorf("failed to add video track %s: %w", trackInfo.TrackID, err)
		}
		peerInfo.Senders[id] = sender
	}

	// Handle ICE candidates
	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
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
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("Peer %s connection state: %s", peerID, state.String())

		mpm.mu.Lock()
		if info, ok := mpm.viewerStates[peerID]; ok {
			info.State = state.String()
			if state == webrtc.PeerConnectionStateConnected {
				info.ConnectedAt = time.Now()
				info.ConnectionType = mpm.detectConnectionType(pc)
			}
		}
		mpm.mu.Unlock()

		switch state {
		case webrtc.PeerConnectionStateConnected:
			if mpm.onConnected != nil {
				mpm.onConnected(peerID)
			}
		case webrtc.PeerConnectionStateDisconnected, webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
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
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	<-gatherComplete

	// Store connection with sender info
	mpm.connections[peerID] = peerInfo

	return pc.LocalDescription().SDP, nil
}

// HandleAnswer processes an SDP answer
func (mpm *MultiPeerManager) HandleAnswer(peerID string, sdp string) error {
	mpm.mu.RLock()
	peerInfo, exists := mpm.connections[peerID]
	mpm.mu.RUnlock()

	if !exists {
		return fmt.Errorf("peer not found: %s", peerID)
	}

	answer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  sdp,
	}

	return peerInfo.PC.SetRemoteDescription(answer)
}

// AddICECandidate adds an ICE candidate
func (mpm *MultiPeerManager) AddICECandidate(peerID string, candidateJSON string) error {
	mpm.mu.RLock()
	peerInfo, exists := mpm.connections[peerID]
	mpm.mu.RUnlock()

	if !exists {
		return fmt.Errorf("peer not found: %s", peerID)
	}

	var candidate webrtc.ICECandidateInit
	if err := json.Unmarshal([]byte(candidateJSON), &candidate); err != nil {
		return fmt.Errorf("failed to parse ICE candidate: %w", err)
	}

	return peerInfo.PC.AddICECandidate(candidate)
}

// WriteVideoSample writes a video sample to a specific track
func (mpm *MultiPeerManager) WriteVideoSample(trackID string, data []byte, duration time.Duration) error {
	mpm.mu.RLock()
	trackInfo, exists := mpm.tracks[trackID]
	mpm.mu.RUnlock()

	if !exists {
		return fmt.Errorf("track not found: %s", trackID)
	}

	return trackInfo.Track.WriteSample(media.Sample{
		Data:     data,
		Duration: duration,
	})
}

// detectConnectionType checks if connection is direct or relayed
func (mpm *MultiPeerManager) detectConnectionType(pc *webrtc.PeerConnection) string {
	stats := pc.GetStats()

	for _, stat := range stats {
		if candidatePair, ok := stat.(webrtc.ICECandidatePairStats); ok {
			if candidatePair.State == webrtc.StatsICECandidatePairStateSucceeded {
				for _, s := range stats {
					if localCandidate, ok := s.(webrtc.ICECandidateStats); ok {
						if localCandidate.ID == candidatePair.LocalCandidateID {
							switch localCandidate.CandidateType {
							case webrtc.ICECandidateTypeRelay:
								return "relay"
							case webrtc.ICECandidateTypeHost:
								return "direct"
							case webrtc.ICECandidateTypeSrflx, webrtc.ICECandidateTypePrflx:
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
func (mpm *MultiPeerManager) removePeer(peerID string) {
	mpm.mu.Lock()
	defer mpm.mu.Unlock()

	if peerInfo, exists := mpm.connections[peerID]; exists {
		peerInfo.PC.Close()
		delete(mpm.connections, peerID)
	}
	delete(mpm.viewerStates, peerID)
}

// GetViewerInfo returns information about connected viewers
func (mpm *MultiPeerManager) GetViewerInfo() []ViewerInfo {
	mpm.mu.RLock()
	defer mpm.mu.RUnlock()

	viewers := make([]ViewerInfo, 0, len(mpm.viewerStates))
	for _, info := range mpm.viewerStates {
		viewers = append(viewers, *info)
	}
	return viewers
}

// GetConnectionCount returns number of active connections
func (mpm *MultiPeerManager) GetConnectionCount() int {
	mpm.mu.RLock()
	defer mpm.mu.RUnlock()
	return len(mpm.connections)
}

// Close closes all peer connections
func (mpm *MultiPeerManager) Close() {
	mpm.mu.Lock()
	defer mpm.mu.Unlock()

	for id, peerInfo := range mpm.connections {
		peerInfo.PC.Close()
		delete(mpm.connections, id)
	}
}

// GetCodecType returns the codec type
func (mpm *MultiPeerManager) GetCodecType() CodecType {
	return mpm.codecType
}

// SetRenegotiateCallback sets callback for when renegotiation offer is ready
func (mpm *MultiPeerManager) SetRenegotiateCallback(callback func(peerID string, offer string)) {
	mpm.onRenegotiate = callback
}

// SetStreamChangeCallbacks sets callbacks for stream add/remove events
func (mpm *MultiPeerManager) SetStreamChangeCallbacks(onAdded func(info sig.StreamInfo), onRemoved func(trackID string)) {
	mpm.onStreamAdded = onAdded
	mpm.onStreamRemoved = onRemoved
}

// AddTrackToAllPeers adds a track to all existing peer connections
func (mpm *MultiPeerManager) AddTrackToAllPeers(trackInfo *StreamTrackInfo) error {
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
func (mpm *MultiPeerManager) RemoveTrackFromAllPeers(trackID string) error {
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
func (mpm *MultiPeerManager) RenegotiateAllPeers() {
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
func (mpm *MultiPeerManager) renegotiatePeer(peerID string) {
	mpm.mu.Lock()
	peerInfo, exists := mpm.connections[peerID]
	if !exists {
		mpm.mu.Unlock()
		log.Printf("Renegotiation: peer %s not found", peerID)
		return
	}

	// Check if already renegotiating
	if peerInfo.renegotiating {
		mpm.mu.Unlock()
		log.Printf("Renegotiation: peer %s already renegotiating, skipping", peerID)
		return
	}
	peerInfo.renegotiating = true
	mpm.mu.Unlock()

	// Ensure we clear the flag when done
	defer func() {
		mpm.mu.Lock()
		if pi, ok := mpm.connections[peerID]; ok {
			pi.renegotiating = false
		}
		mpm.mu.Unlock()
	}()

	// Check signaling state - can only create offer in stable state
	signalingState := peerInfo.PC.SignalingState()
	log.Printf("Renegotiation: peer %s signaling state: %s", peerID, signalingState.String())

	if signalingState != webrtc.SignalingStateStable {
		log.Printf("Renegotiation: peer %s not in stable state, waiting...", peerID)
		// Wait for state to become stable (with timeout)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
			signalingState = peerInfo.PC.SignalingState()
			if signalingState == webrtc.SignalingStateStable {
				break
			}
		}
		if signalingState != webrtc.SignalingStateStable {
			log.Printf("Renegotiation: peer %s still not stable after wait, skipping (state: %s)", peerID, signalingState.String())
			return
		}
	}

	// Check connection state
	connState := peerInfo.PC.ConnectionState()
	log.Printf("Renegotiation: peer %s connection state: %s", peerID, connState.String())

	if connState != webrtc.PeerConnectionStateConnected {
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
	gatherComplete := webrtc.GatheringCompletePromise(peerInfo.PC)
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
func (mpm *MultiPeerManager) HandleRenegotiateAnswer(peerID string, sdp string) error {
	mpm.mu.RLock()
	peerInfo, exists := mpm.connections[peerID]
	mpm.mu.RUnlock()

	if !exists {
		return fmt.Errorf("peer not found: %s", peerID)
	}

	answer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
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
func (mpm *MultiPeerManager) NotifyStreamAdded(info sig.StreamInfo) {
	if mpm.onStreamAdded != nil {
		mpm.onStreamAdded(info)
	}
}

// NotifyStreamRemoved notifies that a stream was removed
func (mpm *MultiPeerManager) NotifyStreamRemoved(trackID string) {
	if mpm.onStreamRemoved != nil {
		mpm.onStreamRemoved(trackID)
	}
}

// StreamPipeline manages capture-encode-stream for a single window
type StreamPipeline struct {
	trackInfo    *StreamTrackInfo
	capture      *CaptureInstance
	encoder      VideoEncoder
	running      bool
	stopChan     chan struct{}
	fps          int
	bitrate      int
	focusBitrate int // bitrate when focused
	bgBitrate    int // bitrate when not focused (background)
	adaptiveBR   bool
	mu           sync.Mutex

	// Stats
	frameCount int64
	byteCount  int64
}

// MultiStreamer manages multiple stream pipelines
type MultiStreamer struct {
	peerManager     *MultiPeerManager
	multiCapture    *MultiCapture
	pipelines       map[string]*StreamPipeline // trackID -> pipeline
	codecType       CodecType
	fps             int
	focusBitrate    int
	bgBitrate       int
	adaptiveBitrate bool
	running         bool
	stopChan        chan struct{}
	focusCheckChan  chan struct{}
	mu              sync.RWMutex

	// Callbacks
	onFocusChange   func(trackID string)
	onStreamsChange func(streams []sig.StreamInfo)
}

// NewMultiStreamer creates a new multi-streamer
func NewMultiStreamer(peerManager *MultiPeerManager, fps, focusBitrate, bgBitrate int, adaptiveBR bool) *MultiStreamer {
	return &MultiStreamer{
		peerManager:     peerManager,
		multiCapture:    NewMultiCapture(),
		pipelines:       make(map[string]*StreamPipeline),
		codecType:       peerManager.GetCodecType(),
		fps:             fps,
		focusBitrate:    focusBitrate,
		bgBitrate:       bgBitrate,
		adaptiveBitrate: adaptiveBR,
		stopChan:        make(chan struct{}),
		focusCheckChan:  make(chan struct{}),
	}
}

// SetOnFocusChange sets the callback for focus changes
func (ms *MultiStreamer) SetOnFocusChange(callback func(trackID string)) {
	ms.onFocusChange = callback
}

// SetOnStreamsChange sets the callback for streams info changes
func (ms *MultiStreamer) SetOnStreamsChange(callback func(streams []sig.StreamInfo)) {
	ms.onStreamsChange = callback
}

// AddWindow adds a window to stream
func (ms *MultiStreamer) AddWindow(window WindowInfo) (*StreamTrackInfo, error) {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	if len(ms.pipelines) >= MaxCaptureInstances {
		return nil, fmt.Errorf("maximum windows (%d) reached", MaxCaptureInstances)
	}

	// Create track in peer manager
	trackInfo, err := ms.peerManager.AddTrack(window.ID, window.WindowName, window.OwnerName)
	if err != nil {
		return nil, err
	}

	trackInfo.Width = int(window.Width)
	trackInfo.Height = int(window.Height)

	// Start capture
	capture, err := ms.multiCapture.StartWindowCapture(window.ID, 0, 0, ms.fps)
	if err != nil {
		ms.peerManager.RemoveTrack(trackInfo.TrackID)
		return nil, fmt.Errorf("failed to start capture: %w", err)
	}

	// Determine initial bitrate
	bitrate := ms.focusBitrate
	if ms.adaptiveBitrate && !trackInfo.IsFocused {
		bitrate = ms.bgBitrate
	}

	// Create encoder
	factory := NewEncoderFactory()
	encoder, err := factory.CreateEncoder(ms.codecType, ms.fps, bitrate)
	if err != nil {
		ms.multiCapture.StopCapture(capture)
		ms.peerManager.RemoveTrack(trackInfo.TrackID)
		return nil, fmt.Errorf("failed to create encoder: %w", err)
	}

	// Create pipeline
	pipeline := &StreamPipeline{
		trackInfo:    trackInfo,
		capture:      capture,
		encoder:      encoder,
		fps:          ms.fps,
		bitrate:      bitrate,
		focusBitrate: ms.focusBitrate,
		bgBitrate:    ms.bgBitrate,
		adaptiveBR:   ms.adaptiveBitrate,
		stopChan:     make(chan struct{}),
	}

	ms.pipelines[trackInfo.TrackID] = pipeline

	// Start pipeline if already running
	if ms.running {
		go pipeline.run(ms.peerManager, ms.multiCapture)
	}

	// Notify about streams change
	if ms.onStreamsChange != nil {
		ms.onStreamsChange(ms.getStreamsInfo())
	}

	return trackInfo, nil
}

// RemoveWindow removes a window from streaming
func (ms *MultiStreamer) RemoveWindow(windowID uint32) {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	for trackID, pipeline := range ms.pipelines {
		if pipeline.trackInfo.WindowID == windowID {
			pipeline.stop()
			ms.multiCapture.StopCapture(pipeline.capture)
			ms.peerManager.RemoveTrack(trackID)
			delete(ms.pipelines, trackID)
			break
		}
	}

	// Notify about streams change
	if ms.onStreamsChange != nil {
		ms.onStreamsChange(ms.getStreamsInfo())
	}
}

// Start starts all pipelines
func (ms *MultiStreamer) Start() error {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	if ms.running {
		return nil
	}

	ms.running = true

	// Start all pipelines
	for _, pipeline := range ms.pipelines {
		if err := pipeline.encoder.Start(); err != nil {
			return err
		}
		go pipeline.run(ms.peerManager, ms.multiCapture)
	}

	// Start focus detection loop
	go ms.focusDetectionLoop()

	return nil
}

// Stop stops all pipelines
func (ms *MultiStreamer) Stop() {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	if !ms.running {
		return
	}

	ms.running = false
	close(ms.stopChan)

	for _, pipeline := range ms.pipelines {
		pipeline.stop()
	}

	ms.multiCapture.StopAll()
}

// focusDetectionLoop periodically checks for focus changes using z-order
func (ms *MultiStreamer) focusDetectionLoop() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var lastTopmostWindow uint32

	// Log captured windows at start
	ms.mu.RLock()
	log.Printf("Focus detection loop started with %d pipelines:", len(ms.pipelines))
	for trackID, pipeline := range ms.pipelines {
		log.Printf("  - Track %s: windowID=%d, name=%s", trackID, pipeline.trackInfo.WindowID, pipeline.trackInfo.WindowName)
	}
	ms.mu.RUnlock()

	for {
		select {
		case <-ms.stopChan:
			log.Printf("Focus detection loop stopped")
			return
		case <-ticker.C:
			// Collect all captured window IDs
			ms.mu.RLock()
			windowIDs := make([]uint32, 0, len(ms.pipelines))
			for _, pipeline := range ms.pipelines {
				windowIDs = append(windowIDs, pipeline.trackInfo.WindowID)
			}
			ms.mu.RUnlock()

			if len(windowIDs) == 0 {
				continue
			}

			// Find which captured window is topmost in z-order
			topmostWindow := GetTopmostWindow(windowIDs)

			if topmostWindow != lastTopmostWindow && topmostWindow != 0 {
				log.Printf("Topmost captured window changed to: %d (was %d)", topmostWindow, lastTopmostWindow)
				lastTopmostWindow = topmostWindow

				// Find the track for this window and update focus
				ms.mu.RLock()
				for trackID, pipeline := range ms.pipelines {
					if pipeline.trackInfo.WindowID == topmostWindow {
						newTrackID := ms.peerManager.SetFocusedWindow(topmostWindow)
						log.Printf("Focus set to track=%s, windowID=%d, name=%s", trackID, topmostWindow, pipeline.trackInfo.WindowName)

						if newTrackID != "" {
							// Notify via MultiStreamer callback
							if ms.onFocusChange != nil {
								go ms.onFocusChange(newTrackID)
							}
							// Also notify via MultiPeerManager callback (for signaling)
							log.Printf("Calling NotifyFocusChange for track: %s", newTrackID)
							ms.peerManager.NotifyFocusChange(newTrackID)
						}

						// Update bitrates if adaptive
						if ms.adaptiveBitrate {
							ms.updateBitrates()
						}
						break
					}
				}
				ms.mu.RUnlock()
			}
		}
	}
}

// updateBitrates updates encoder bitrates based on focus
func (ms *MultiStreamer) updateBitrates() {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	for _, pipeline := range ms.pipelines {
		pipeline.updateBitrate()
	}
}

// getStreamsInfo returns StreamInfo for all streams
func (ms *MultiStreamer) getStreamsInfo() []sig.StreamInfo {
	streams := make([]sig.StreamInfo, 0, len(ms.pipelines))
	for _, pipeline := range ms.pipelines {
		streams = append(streams, sig.StreamInfo{
			TrackID:    pipeline.trackInfo.TrackID,
			WindowName: pipeline.trackInfo.WindowName,
			AppName:    pipeline.trackInfo.AppName,
			IsFocused:  pipeline.trackInfo.IsFocused,
			Width:      pipeline.trackInfo.Width,
			Height:     pipeline.trackInfo.Height,
		})
	}
	return streams
}

// GetStreamsInfo returns current streams info (thread-safe)
func (ms *MultiStreamer) GetStreamsInfo() []sig.StreamInfo {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.getStreamsInfo()
}

// GetFocusedTrackID returns the currently focused track ID
func (ms *MultiStreamer) GetFocusedTrackID() string {
	track := ms.peerManager.GetFocusedTrack()
	if track != nil {
		return track.TrackID
	}
	return ""
}

// SetAdaptiveBitrate enables/disables adaptive bitrate
func (ms *MultiStreamer) SetAdaptiveBitrate(enabled bool) {
	ms.mu.Lock()
	ms.adaptiveBitrate = enabled
	ms.mu.Unlock()

	if enabled {
		ms.updateBitrates()
	}
}

// GetStreamingWindowIDs returns a map of currently streaming window IDs
func (ms *MultiStreamer) GetStreamingWindowIDs() map[uint32]bool {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	result := make(map[uint32]bool)
	for _, pipeline := range ms.pipelines {
		result[pipeline.trackInfo.WindowID] = true
	}
	return result
}

// AddWindowDynamic adds a window without stopping other streams (for renegotiation)
func (ms *MultiStreamer) AddWindowDynamic(window WindowInfo) (*StreamTrackInfo, error) {
	ms.mu.Lock()

	if len(ms.pipelines) >= MaxCaptureInstances {
		ms.mu.Unlock()
		return nil, fmt.Errorf("maximum windows (%d) reached", MaxCaptureInstances)
	}

	// Check if already streaming this window
	for _, pipeline := range ms.pipelines {
		if pipeline.trackInfo.WindowID == window.ID {
			ms.mu.Unlock()
			return nil, fmt.Errorf("window %d already streaming", window.ID)
		}
	}
	ms.mu.Unlock()

	// Create track in peer manager
	trackInfo, err := ms.peerManager.AddTrack(window.ID, window.WindowName, window.OwnerName)
	if err != nil {
		return nil, err
	}

	trackInfo.Width = int(window.Width)
	trackInfo.Height = int(window.Height)

	// Start capture for this window
	capture, err := ms.multiCapture.StartWindowCapture(window.ID, 0, 0, ms.fps)
	if err != nil {
		ms.peerManager.RemoveTrack(trackInfo.TrackID)
		return nil, fmt.Errorf("failed to start capture: %w", err)
	}

	// Determine initial bitrate (new windows are not focused by default)
	bitrate := ms.bgBitrate
	if ms.adaptiveBitrate && trackInfo.IsFocused {
		bitrate = ms.focusBitrate
	}

	// Create encoder
	factory := NewEncoderFactory()
	encoder, err := factory.CreateEncoder(ms.codecType, ms.fps, bitrate)
	if err != nil {
		ms.multiCapture.StopCapture(capture)
		ms.peerManager.RemoveTrack(trackInfo.TrackID)
		return nil, fmt.Errorf("failed to create encoder: %w", err)
	}

	// Create pipeline
	pipeline := &StreamPipeline{
		trackInfo:    trackInfo,
		capture:      capture,
		encoder:      encoder,
		fps:          ms.fps,
		bitrate:      bitrate,
		focusBitrate: ms.focusBitrate,
		bgBitrate:    ms.bgBitrate,
		adaptiveBR:   ms.adaptiveBitrate,
		stopChan:     make(chan struct{}),
	}

	ms.mu.Lock()
	ms.pipelines[trackInfo.TrackID] = pipeline
	isRunning := ms.running
	ms.mu.Unlock()

	// Start pipeline if streamer is already running
	if isRunning {
		if err := encoder.Start(); err != nil {
			ms.multiCapture.StopCapture(capture)
			ms.peerManager.RemoveTrack(trackInfo.TrackID)
			ms.mu.Lock()
			delete(ms.pipelines, trackInfo.TrackID)
			ms.mu.Unlock()
			return nil, fmt.Errorf("failed to start encoder: %w", err)
		}
		go pipeline.run(ms.peerManager, ms.multiCapture)
	}

	// Add track to all existing peer connections
	log.Printf("AddWindowDynamic: Adding track %s to all peers", trackInfo.TrackID)
	if err := ms.peerManager.AddTrackToAllPeers(trackInfo); err != nil {
		log.Printf("Warning: failed to add track to some peers: %v", err)
	}

	// Notify about new stream BEFORE renegotiation so viewer knows to expect it
	log.Printf("AddWindowDynamic: Notifying about new stream %s", trackInfo.TrackID)
	ms.peerManager.NotifyStreamAdded(sig.StreamInfo{
		TrackID:    trackInfo.TrackID,
		WindowName: trackInfo.WindowName,
		AppName:    trackInfo.AppName,
		IsFocused:  trackInfo.IsFocused,
		Width:      trackInfo.Width,
		Height:     trackInfo.Height,
	})

	// Trigger renegotiation with all viewers
	log.Printf("AddWindowDynamic: Triggering renegotiation for track %s", trackInfo.TrackID)
	ms.peerManager.RenegotiateAllPeers()

	log.Printf("Added window dynamically: %s (windowID=%d)", trackInfo.TrackID, window.ID)

	return trackInfo, nil
}

// RemoveWindowDynamic removes a window without stopping other streams (for renegotiation)
func (ms *MultiStreamer) RemoveWindowDynamic(windowID uint32) error {
	ms.mu.Lock()

	var trackIDToRemove string
	var pipelineToStop *StreamPipeline

	for trackID, pipeline := range ms.pipelines {
		if pipeline.trackInfo.WindowID == windowID {
			trackIDToRemove = trackID
			pipelineToStop = pipeline
			delete(ms.pipelines, trackID)
			break
		}
	}
	ms.mu.Unlock()

	if pipelineToStop == nil {
		return fmt.Errorf("window %d not found in active streams", windowID)
	}

	log.Printf("Removing window dynamically: %s (windowID=%d)", trackIDToRemove, windowID)

	// Stop the pipeline
	pipelineToStop.stop()

	// Stop capture for this window
	ms.multiCapture.StopCapture(pipelineToStop.capture)

	// Remove track from all peer connections
	if err := ms.peerManager.RemoveTrackFromAllPeers(trackIDToRemove); err != nil {
		log.Printf("Warning: failed to remove track from some peers: %v", err)
	}

	// Remove track from peer manager
	ms.peerManager.RemoveTrack(trackIDToRemove)

	// Trigger renegotiation with all viewers
	ms.peerManager.RenegotiateAllPeers()

	// Notify about removed stream (for signaling to broadcast stream-removed)
	ms.peerManager.NotifyStreamRemoved(trackIDToRemove)

	return nil
}

// Pipeline methods

func (p *StreamPipeline) run(pm *MultiPeerManager, mc *MultiCapture) {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return
	}
	p.running = true
	p.mu.Unlock()

	frameDuration := time.Second / time.Duration(p.fps)
	ticker := time.NewTicker(frameDuration)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopChan:
			return
		case <-ticker.C:
			frame, err := mc.GetLatestFrameBGRA(p.capture, 100*time.Millisecond)
			if err != nil {
				continue
			}

			// Update dimensions
			p.trackInfo.Width = frame.Width
			p.trackInfo.Height = frame.Height

			// Encode
			data, err := p.encoder.EncodeBGRAFrame(frame)
			if err != nil {
				continue
			}

			p.frameCount++
			p.byteCount += int64(len(data))

			// Write to track
			if err := pm.WriteVideoSample(p.trackInfo.TrackID, data, frameDuration); err != nil {
				continue
			}
		}
	}
}

func (p *StreamPipeline) stop() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.running {
		return
	}

	p.running = false
	close(p.stopChan)
	p.encoder.Stop()
}

func (p *StreamPipeline) updateBitrate() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.adaptiveBR {
		return
	}

	newBitrate := p.bgBitrate
	if p.trackInfo.IsFocused {
		newBitrate = p.focusBitrate
	}

	if newBitrate != p.bitrate {
		p.bitrate = newBitrate
		// Note: Changing bitrate on-the-fly requires encoder support
		// For now, the bitrate change will take effect on next encoder restart
		log.Printf("Track %s bitrate changed to %d kbps (focused: %v)",
			p.trackInfo.TrackID, newBitrate, p.trackInfo.IsFocused)
	}
}
