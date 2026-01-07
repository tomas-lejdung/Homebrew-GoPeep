package main

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/pion/webrtc/v3"
	"github.com/pion/webrtc/v3/pkg/media"
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

// MultiPeerManager manages WebRTC connections with multiple video tracks
type MultiPeerManager struct {
	config        webrtc.Configuration
	iceConfig     ICEConfig
	codecType     CodecType
	tracks        map[string]*StreamTrackInfo // trackID -> track info
	connections   map[string]*webrtc.PeerConnection
	viewerStates  map[string]*ViewerInfo
	mu            sync.RWMutex
	onICE         func(peerID string, candidate string)
	onConnected   func(peerID string)
	onDisconnect  func(peerID string)
	onFocusChange func(trackID string) // Called when focus changes to a new track
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
		connections:  make(map[string]*webrtc.PeerConnection),
		viewerStates: make(map[string]*ViewerInfo),
	}, nil
}

// AddTrack creates a new video track for a window
func (mpm *MultiPeerManager) AddTrack(windowID uint32, windowName, appName string) (*StreamTrackInfo, error) {
	mpm.mu.Lock()
	defer mpm.mu.Unlock()

	// Generate track ID
	trackID := fmt.Sprintf("video%d", len(mpm.tracks))

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

// GetTracks returns all current tracks
func (mpm *MultiPeerManager) GetTracks() []*StreamTrackInfo {
	mpm.mu.RLock()
	defer mpm.mu.RUnlock()

	tracks := make([]*StreamTrackInfo, 0, len(mpm.tracks))
	for _, t := range mpm.tracks {
		tracks = append(tracks, t)
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

	// Add all video tracks
	for _, trackInfo := range mpm.tracks {
		_, err = pc.AddTrack(trackInfo.Track)
		if err != nil {
			pc.Close()
			return "", fmt.Errorf("failed to add video track %s: %w", trackInfo.TrackID, err)
		}
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

	// Store connection
	mpm.connections[peerID] = pc

	return pc.LocalDescription().SDP, nil
}

// HandleAnswer processes an SDP answer
func (mpm *MultiPeerManager) HandleAnswer(peerID string, sdp string) error {
	mpm.mu.RLock()
	pc, exists := mpm.connections[peerID]
	mpm.mu.RUnlock()

	if !exists {
		return fmt.Errorf("peer not found: %s", peerID)
	}

	answer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  sdp,
	}

	return pc.SetRemoteDescription(answer)
}

// AddICECandidate adds an ICE candidate
func (mpm *MultiPeerManager) AddICECandidate(peerID string, candidateJSON string) error {
	mpm.mu.RLock()
	pc, exists := mpm.connections[peerID]
	mpm.mu.RUnlock()

	if !exists {
		return fmt.Errorf("peer not found: %s", peerID)
	}

	var candidate webrtc.ICECandidateInit
	if err := json.Unmarshal([]byte(candidateJSON), &candidate); err != nil {
		return fmt.Errorf("failed to parse ICE candidate: %w", err)
	}

	return pc.AddICECandidate(candidate)
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

	if pc, exists := mpm.connections[peerID]; exists {
		pc.Close()
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

	for id, pc := range mpm.connections {
		pc.Close()
		delete(mpm.connections, id)
	}
}

// GetCodecType returns the codec type
func (mpm *MultiPeerManager) GetCodecType() CodecType {
	return mpm.codecType
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
	onStreamsChange func(streams []StreamInfo)
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
func (ms *MultiStreamer) SetOnStreamsChange(callback func(streams []StreamInfo)) {
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
func (ms *MultiStreamer) getStreamsInfo() []StreamInfo {
	streams := make([]StreamInfo, 0, len(ms.pipelines))
	for _, pipeline := range ms.pipelines {
		streams = append(streams, StreamInfo{
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
func (ms *MultiStreamer) GetStreamsInfo() []StreamInfo {
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
