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

// StreamPipelineStats holds real-time statistics for a single stream
type StreamPipelineStats struct {
	TrackID   string  // Stream identifier
	AppName   string  // Application name (e.g., "VS Code")
	Width     int     // Current resolution width
	Height    int     // Current resolution height
	FPS       float64 // Current frames per second
	Bitrate   float64 // Current bitrate in kbps
	Frames    uint64  // Total frames encoded
	Bytes     uint64  // Total bytes sent
	IsFocused bool    // Whether this stream has focus
}

// PeerInfo holds peer connection and associated RTP senders
type PeerInfo struct {
	PC            *webrtc.PeerConnection
	Senders       map[string]*webrtc.RTPSender // trackID -> sender
	renegotiating bool                         // Whether renegotiation is in progress
}

// PeerManager manages WebRTC connections with multiple video tracks
type PeerManager struct {
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

// NewPeerManager creates a new multi-track peer manager
func NewPeerManager(iceConfig ICEConfig, codecType CodecType) (*PeerManager, error) {
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

	return &PeerManager{
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
func (mpm *PeerManager) AddTrack(windowID uint32, windowName, appName string) (*StreamTrackInfo, error) {
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
func (mpm *PeerManager) RemoveTrack(trackID string) {
	mpm.mu.Lock()
	defer mpm.mu.Unlock()
	delete(mpm.tracks, trackID)
}

// GetTracks returns all current tracks in sorted order by TrackID
func (mpm *PeerManager) GetTracks() []*StreamTrackInfo {
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
func (mpm *PeerManager) SetFocusedTrack(trackID string) {
	mpm.mu.Lock()
	defer mpm.mu.Unlock()

	for id, t := range mpm.tracks {
		t.IsFocused = (id == trackID)
	}
}

// SetFocusedWindow sets focus based on window ID
func (mpm *PeerManager) SetFocusedWindow(windowID uint32) string {
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
func (mpm *PeerManager) GetFocusedTrack() *StreamTrackInfo {
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
		log.Printf("NotifyFocusChange: callback exists, calling with trackID: %s", trackID)
		mpm.onFocusChange(trackID)
	} else {
		log.Printf("NotifyFocusChange: callback is NIL! trackID: %s", trackID)
	}
}

// CreateOffer creates an SDP offer for a new viewer with all tracks
func (mpm *PeerManager) CreateOffer(peerID string) (string, error) {
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
func (mpm *PeerManager) HandleAnswer(peerID string, sdp string) error {
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
func (mpm *PeerManager) AddICECandidate(peerID string, candidateJSON string) error {
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
func (mpm *PeerManager) WriteVideoSample(trackID string, data []byte, duration time.Duration) error {
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
func (mpm *PeerManager) detectConnectionType(pc *webrtc.PeerConnection) string {
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

// GetCodecType returns the codec type
func (mpm *PeerManager) GetCodecType() CodecType {
	return mpm.codecType
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
func (mpm *PeerManager) HandleRenegotiateAnswer(peerID string, sdp string) error {
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
	qualityMode  bool // false = performance, true = quality
	mu           sync.Mutex

	// Stats tracking
	frameCount     uint64    // Total frames encoded
	byteCount      uint64    // Total bytes sent
	lastFrameTime  time.Time // For FPS calculation
	lastByteCount  uint64    // For bitrate calculation
	lastStatsTime  time.Time // When we last calculated rates
	currentFPS     float64   // Calculated FPS
	currentBitrate float64   // Calculated bitrate in kbps
}

// Streamer manages multiple stream pipelines
type Streamer struct {
	peerManager     *PeerManager
	multiCapture    *MultiCapture
	pipelines       map[string]*StreamPipeline // trackID -> pipeline
	codecType       CodecType
	fps             int
	focusBitrate    int
	bgBitrate       int
	adaptiveBitrate bool
	qualityMode     bool // false = performance, true = quality
	running         bool
	stopChan        chan struct{}
	focusCheckChan  chan struct{}
	mu              sync.RWMutex

	// Callbacks
	onFocusChange   func(trackID string)
	onStreamsChange func(streams []sig.StreamInfo)
}

// NewStreamer creates a new multi-streamer
func NewStreamer(peerManager *PeerManager, fps, focusBitrate, bgBitrate int, adaptiveBR bool, qualityMode bool) *Streamer {
	return &Streamer{
		peerManager:     peerManager,
		multiCapture:    NewMultiCapture(),
		pipelines:       make(map[string]*StreamPipeline),
		codecType:       peerManager.GetCodecType(),
		fps:             fps,
		focusBitrate:    focusBitrate,
		bgBitrate:       bgBitrate,
		adaptiveBitrate: adaptiveBR,
		qualityMode:     qualityMode,
		stopChan:        make(chan struct{}),
		focusCheckChan:  make(chan struct{}),
	}
}

// SetOnFocusChange sets the callback for focus changes
func (ms *Streamer) SetOnFocusChange(callback func(trackID string)) {
	ms.onFocusChange = callback
}

// SetOnStreamsChange sets the callback for streams info changes
func (ms *Streamer) SetOnStreamsChange(callback func(streams []sig.StreamInfo)) {
	ms.onStreamsChange = callback
}

// AddWindow adds a window to stream
func (ms *Streamer) AddWindow(window WindowInfo) (*StreamTrackInfo, error) {
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

	// Apply quality mode if enabled
	if ms.qualityMode {
		encoder.SetQualityMode(true, bitrate)
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
		qualityMode:  ms.qualityMode,
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
func (ms *Streamer) RemoveWindow(windowID uint32) {
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
func (ms *Streamer) Start() error {
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
func (ms *Streamer) Stop() {
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
func (ms *Streamer) focusDetectionLoop() {
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
							// Notify via Streamer callback
							if ms.onFocusChange != nil {
								go ms.onFocusChange(newTrackID)
							}
							// Also notify via PeerManager callback (for signaling)
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
func (ms *Streamer) updateBitrates() {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	for _, pipeline := range ms.pipelines {
		pipeline.updateBitrate()
	}
}

// getStreamsInfo returns StreamInfo for all streams
func (ms *Streamer) getStreamsInfo() []sig.StreamInfo {
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
func (ms *Streamer) GetStreamsInfo() []sig.StreamInfo {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return ms.getStreamsInfo()
}

// GetFocusedTrackID returns the currently focused track ID
func (ms *Streamer) GetFocusedTrackID() string {
	track := ms.peerManager.GetFocusedTrack()
	if track != nil {
		return track.TrackID
	}
	return ""
}

// SetAdaptiveBitrate enables/disables adaptive bitrate
func (ms *Streamer) SetAdaptiveBitrate(enabled bool) {
	ms.mu.Lock()
	ms.adaptiveBitrate = enabled
	ms.mu.Unlock()

	if enabled {
		ms.updateBitrates()
	}
}

// SetQualityMode enables/disables quality mode for all streams
// Quality mode (true): Uses CQ/CRF for consistent visual quality
// Performance mode (false): Uses CBR/ABR for bandwidth efficiency
func (ms *Streamer) SetQualityMode(enabled bool) {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	ms.qualityMode = enabled

	for _, pipeline := range ms.pipelines {
		pipeline.SetQualityMode(enabled)
	}

	mode := "performance"
	if enabled {
		mode = "quality"
	}
	log.Printf("Streamer quality mode: %s", mode)
}

// GetStats returns statistics for all active streams
func (ms *Streamer) GetStats() []StreamPipelineStats {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	stats := make([]StreamPipelineStats, 0, len(ms.pipelines))
	for _, pipeline := range ms.pipelines {
		stats = append(stats, pipeline.GetStats())
	}
	return stats
}

// SetBitrate updates the bitrate for all active streams
func (ms *Streamer) SetBitrate(focusBitrate, bgBitrate int) {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	ms.focusBitrate = focusBitrate
	ms.bgBitrate = bgBitrate

	for _, pipeline := range ms.pipelines {
		pipeline.SetBitrate(focusBitrate, bgBitrate)
	}

	log.Printf("Streamer bitrate updated: focus=%d kbps, bg=%d kbps", focusBitrate, bgBitrate)
}

// GetStreamingWindowIDs returns a map of currently streaming window IDs
func (ms *Streamer) GetStreamingWindowIDs() map[uint32]bool {
	ms.mu.RLock()
	defer ms.mu.RUnlock()

	result := make(map[uint32]bool)
	for _, pipeline := range ms.pipelines {
		result[pipeline.trackInfo.WindowID] = true
	}
	return result
}

// AddWindowDynamic adds a window without stopping other streams (for renegotiation)
func (ms *Streamer) AddWindowDynamic(window WindowInfo) (*StreamTrackInfo, error) {
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

	// Apply quality mode if enabled
	if ms.qualityMode {
		encoder.SetQualityMode(true, bitrate)
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
		qualityMode:  ms.qualityMode,
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
func (ms *Streamer) RemoveWindowDynamic(windowID uint32) error {
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

// AddDisplay adds display (fullscreen) capture to stream
// Uses windowID = 0 to identify display capture
func (ms *Streamer) AddDisplay() (*StreamTrackInfo, error) {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	if len(ms.pipelines) >= MaxCaptureInstances {
		return nil, fmt.Errorf("maximum streams (%d) reached", MaxCaptureInstances)
	}

	// Check if already capturing display
	for _, pipeline := range ms.pipelines {
		if pipeline.trackInfo.WindowID == 0 {
			return nil, fmt.Errorf("display is already being captured")
		}
	}

	// Create track in peer manager (windowID=0 for display)
	trackInfo, err := ms.peerManager.AddTrack(0, "Fullscreen", "Display")
	if err != nil {
		return nil, err
	}

	// Start display capture
	capture, err := ms.multiCapture.StartDisplayCapture(0, 0, ms.fps)
	if err != nil {
		ms.peerManager.RemoveTrack(trackInfo.TrackID)
		return nil, fmt.Errorf("failed to start display capture: %w", err)
	}

	// Use focus bitrate for display (it's the main content)
	bitrate := ms.focusBitrate

	// Create encoder
	factory := NewEncoderFactory()
	encoder, err := factory.CreateEncoder(ms.codecType, ms.fps, bitrate)
	if err != nil {
		ms.multiCapture.StopCapture(capture)
		ms.peerManager.RemoveTrack(trackInfo.TrackID)
		return nil, fmt.Errorf("failed to create encoder: %w", err)
	}

	// Apply quality mode if enabled
	if ms.qualityMode {
		encoder.SetQualityMode(true, bitrate)
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
		adaptiveBR:   false, // No adaptive bitrate for display
		qualityMode:  ms.qualityMode,
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

// AddDisplayDynamic adds display capture without stopping other streams (for renegotiation)
func (ms *Streamer) AddDisplayDynamic() (*StreamTrackInfo, error) {
	ms.mu.Lock()

	if len(ms.pipelines) >= MaxCaptureInstances {
		ms.mu.Unlock()
		return nil, fmt.Errorf("maximum streams (%d) reached", MaxCaptureInstances)
	}

	// Check if already streaming display
	for _, pipeline := range ms.pipelines {
		if pipeline.trackInfo.WindowID == 0 {
			ms.mu.Unlock()
			return nil, fmt.Errorf("display already streaming")
		}
	}
	ms.mu.Unlock()

	// Create track in peer manager
	trackInfo, err := ms.peerManager.AddTrack(0, "Fullscreen", "Display")
	if err != nil {
		return nil, err
	}

	// Start display capture
	capture, err := ms.multiCapture.StartDisplayCapture(0, 0, ms.fps)
	if err != nil {
		ms.peerManager.RemoveTrack(trackInfo.TrackID)
		return nil, fmt.Errorf("failed to start display capture: %w", err)
	}

	// Use focus bitrate for display
	bitrate := ms.focusBitrate

	// Create encoder
	factory := NewEncoderFactory()
	encoder, err := factory.CreateEncoder(ms.codecType, ms.fps, bitrate)
	if err != nil {
		ms.multiCapture.StopCapture(capture)
		ms.peerManager.RemoveTrack(trackInfo.TrackID)
		return nil, fmt.Errorf("failed to create encoder: %w", err)
	}

	// Apply quality mode if enabled
	if ms.qualityMode {
		encoder.SetQualityMode(true, bitrate)
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
		adaptiveBR:   false,
		qualityMode:  ms.qualityMode,
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
	log.Printf("AddDisplayDynamic: Adding track %s to all peers", trackInfo.TrackID)
	if err := ms.peerManager.AddTrackToAllPeers(trackInfo); err != nil {
		log.Printf("Warning: failed to add track to some peers: %v", err)
	}

	// Notify about new stream BEFORE renegotiation so viewer knows to expect it
	log.Printf("AddDisplayDynamic: Notifying about new stream %s", trackInfo.TrackID)
	ms.peerManager.NotifyStreamAdded(sig.StreamInfo{
		TrackID:    trackInfo.TrackID,
		WindowName: trackInfo.WindowName,
		AppName:    trackInfo.AppName,
		IsFocused:  true, // Display is always "focused"
		Width:      trackInfo.Width,
		Height:     trackInfo.Height,
	})

	// Trigger renegotiation with all viewers
	log.Printf("AddDisplayDynamic: Triggering renegotiation for track %s", trackInfo.TrackID)
	ms.peerManager.RenegotiateAllPeers()

	log.Printf("Added display dynamically: %s", trackInfo.TrackID)

	return trackInfo, nil
}

// RemoveDisplayDynamic removes display capture without stopping other streams
// This is just an alias for RemoveWindowDynamic(0) since display uses windowID=0
func (ms *Streamer) RemoveDisplayDynamic() error {
	return ms.RemoveWindowDynamic(0)
}

// Pipeline methods

func (p *StreamPipeline) run(pm *PeerManager, mc *MultiCapture) {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return
	}
	p.running = true
	p.lastStatsTime = time.Now()
	p.lastByteCount = 0
	p.mu.Unlock()

	frameDuration := time.Second / time.Duration(p.fps)
	ticker := time.NewTicker(frameDuration)
	defer ticker.Stop()

	// Stats update ticker (every second)
	statsTicker := time.NewTicker(time.Second)
	defer statsTicker.Stop()

	var framesSinceLastStats uint64

	for {
		select {
		case <-p.stopChan:
			return
		case <-statsTicker.C:
			// Update FPS and bitrate calculations
			p.mu.Lock()
			now := time.Now()
			elapsed := now.Sub(p.lastStatsTime).Seconds()
			if elapsed > 0 {
				p.currentFPS = float64(framesSinceLastStats) / elapsed
				bytesDiff := p.byteCount - p.lastByteCount
				p.currentBitrate = float64(bytesDiff) * 8 / elapsed / 1000 // kbps
			}
			p.lastStatsTime = now
			p.lastByteCount = p.byteCount
			framesSinceLastStats = 0
			p.mu.Unlock()

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

			p.mu.Lock()
			p.frameCount++
			p.byteCount += uint64(len(data))
			p.mu.Unlock()
			framesSinceLastStats++

			// Write to track
			if err := pm.WriteVideoSample(p.trackInfo.TrackID, data, frameDuration); err != nil {
				continue
			}
		}
	}
}

// GetStats returns current statistics for this pipeline
func (p *StreamPipeline) GetStats() StreamPipelineStats {
	p.mu.Lock()
	defer p.mu.Unlock()

	return StreamPipelineStats{
		TrackID:   p.trackInfo.TrackID,
		AppName:   p.trackInfo.AppName,
		Width:     p.trackInfo.Width,
		Height:    p.trackInfo.Height,
		FPS:       p.currentFPS,
		Bitrate:   p.currentBitrate,
		Frames:    p.frameCount,
		Bytes:     p.byteCount,
		IsFocused: p.trackInfo.IsFocused,
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
		// Apply new bitrate to encoder (will recreate on next frame)
		if p.encoder != nil {
			if err := p.encoder.SetBitrate(newBitrate); err != nil {
				log.Printf("Failed to set bitrate for track %s: %v", p.trackInfo.TrackID, err)
			} else {
				log.Printf("Track %s bitrate changed to %d kbps (focused: %v)",
					p.trackInfo.TrackID, newBitrate, p.trackInfo.IsFocused)
			}
		}
	}
}

// SetBitrate updates the bitrate for this pipeline
func (p *StreamPipeline) SetBitrate(focusBitrate, bgBitrate int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.focusBitrate = focusBitrate
	p.bgBitrate = bgBitrate

	// Determine which bitrate to apply based on focus state
	newBitrate := bgBitrate
	if p.trackInfo.IsFocused {
		newBitrate = focusBitrate
	}

	if newBitrate != p.bitrate {
		p.bitrate = newBitrate
		if p.encoder != nil {
			if err := p.encoder.SetBitrate(newBitrate); err != nil {
				log.Printf("Failed to set bitrate for track %s: %v", p.trackInfo.TrackID, err)
			} else {
				log.Printf("Track %s bitrate set to %d kbps", p.trackInfo.TrackID, newBitrate)
			}
		}
	}
}

// SetQualityMode updates the quality mode for this pipeline
func (p *StreamPipeline) SetQualityMode(enabled bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.qualityMode == enabled {
		return
	}

	p.qualityMode = enabled
	if p.encoder != nil {
		if err := p.encoder.SetQualityMode(enabled, p.bitrate); err != nil {
			log.Printf("Failed to set quality mode for track %s: %v", p.trackInfo.TrackID, err)
		} else {
			mode := "performance"
			if enabled {
				mode = "quality"
			}
			log.Printf("Track %s quality mode set to %s", p.trackInfo.TrackID, mode)
		}
	}
}
