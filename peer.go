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

// ICE servers for NAT traversal
var defaultICEServers = []webrtc.ICEServer{
	{URLs: []string{"stun:stun.l.google.com:19302"}},
	{URLs: []string{"stun:stun1.l.google.com:19302"}},
	{URLs: []string{"stun:stun2.l.google.com:19302"}},
}

// ViewerInfo holds information about a connected viewer
type ViewerInfo struct {
	PeerID         string
	State          string // connecting, connected, disconnected
	ConnectedAt    time.Time
	ConnectionType string // "direct", "relay", or "unknown"
}

// ICEConfig holds ICE server configuration
type ICEConfig struct {
	TURNServer string
	TURNUser   string
	TURNPass   string
	ForceRelay bool
}

// PeerManager manages WebRTC peer connections for sharing
type PeerManager struct {
	config       webrtc.Configuration
	iceConfig    ICEConfig
	codecType    CodecType
	videoTrack   *webrtc.TrackLocalStaticSample
	connections  map[string]*webrtc.PeerConnection
	viewerStates map[string]*ViewerInfo
	mu           sync.RWMutex
	onICE        func(candidate string)
	onConnected  func(peerID string)
	onDisconnect func(peerID string)
}

// NewPeerManager creates a new peer manager with default ICE servers and VP8 codec
func NewPeerManager() (*PeerManager, error) {
	return NewPeerManagerWithCodec(ICEConfig{}, CodecVP8)
}

// NewPeerManagerWithICE creates a peer manager with custom ICE configuration and VP8 codec
func NewPeerManagerWithICE(iceConfig ICEConfig) (*PeerManager, error) {
	return NewPeerManagerWithCodec(iceConfig, CodecVP8)
}

// NewPeerManagerWithCodec creates a peer manager with custom ICE configuration and codec
func NewPeerManagerWithCodec(iceConfig ICEConfig, codecType CodecType) (*PeerManager, error) {
	// Determine WebRTC MIME type based on codec
	var mimeType string
	switch codecType {
	case CodecVP9:
		mimeType = webrtc.MimeTypeVP9
	case CodecH264:
		mimeType = webrtc.MimeTypeH264
	case CodecVP8:
		fallthrough
	default:
		mimeType = webrtc.MimeTypeVP8
	}

	// Create video track
	videoTrack, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: mimeType},
		"video",
		"gopeep",
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create video track: %w", err)
	}

	// Build ICE servers list
	iceServers := make([]webrtc.ICEServer, 0)

	// Add default STUN servers (unless force relay)
	if !iceConfig.ForceRelay {
		iceServers = append(iceServers, defaultICEServers...)
	}

	// Add TURN server if configured
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

	// Configure ICE transport policy
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
		videoTrack:   videoTrack,
		connections:  make(map[string]*webrtc.PeerConnection),
		viewerStates: make(map[string]*ViewerInfo),
	}, nil
}

// GetCodecType returns the codec type being used
func (pm *PeerManager) GetCodecType() CodecType {
	return pm.codecType
}

// SetICECallback sets callback for ICE candidates
func (pm *PeerManager) SetICECallback(callback func(candidate string)) {
	pm.onICE = callback
}

// SetConnectionCallbacks sets callbacks for connection state changes
func (pm *PeerManager) SetConnectionCallbacks(onConnected, onDisconnect func(peerID string)) {
	pm.onConnected = onConnected
	pm.onDisconnect = onDisconnect
}

// CreateOffer creates an SDP offer for a new viewer
func (pm *PeerManager) CreateOffer(peerID string) (string, error) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Create peer connection
	pc, err := webrtc.NewPeerConnection(pm.config)
	if err != nil {
		return "", fmt.Errorf("failed to create peer connection: %w", err)
	}

	// Add video track
	_, err = pc.AddTrack(pm.videoTrack)
	if err != nil {
		pc.Close()
		return "", fmt.Errorf("failed to add video track: %w", err)
	}

	// Handle ICE candidates
	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil || pm.onICE == nil {
			return
		}
		candidateJSON, err := json.Marshal(candidate.ToJSON())
		if err != nil {
			log.Printf("Failed to marshal ICE candidate: %v", err)
			return
		}
		pm.onICE(string(candidateJSON))
	})

	// Track viewer state
	pm.viewerStates[peerID] = &ViewerInfo{
		PeerID:         peerID,
		State:          "connecting",
		ConnectionType: "unknown",
	}

	// Handle connection state
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("Peer %s connection state: %s", peerID, state.String())

		pm.mu.Lock()
		if info, ok := pm.viewerStates[peerID]; ok {
			info.State = state.String()
			if state == webrtc.PeerConnectionStateConnected {
				info.ConnectedAt = time.Now()
				// Detect connection type
				info.ConnectionType = pm.detectConnectionType(pc)
			}
		}
		pm.mu.Unlock()

		switch state {
		case webrtc.PeerConnectionStateConnected:
			if pm.onConnected != nil {
				pm.onConnected(peerID)
			}
		case webrtc.PeerConnectionStateDisconnected, webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			if pm.onDisconnect != nil {
				pm.onDisconnect(peerID)
			}
			pm.removePeer(peerID)
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

	// Wait for ICE gathering to complete
	gatherComplete := webrtc.GatheringCompletePromise(pc)
	<-gatherComplete

	// Store connection
	pm.connections[peerID] = pc

	// Return complete offer with ICE candidates
	return pc.LocalDescription().SDP, nil
}

// HandleAnswer processes an SDP answer from a viewer
func (pm *PeerManager) HandleAnswer(peerID string, sdp string) error {
	pm.mu.RLock()
	pc, exists := pm.connections[peerID]
	pm.mu.RUnlock()

	if !exists {
		return fmt.Errorf("peer not found: %s", peerID)
	}

	answer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  sdp,
	}

	err := pc.SetRemoteDescription(answer)
	if err != nil {
		return fmt.Errorf("failed to set remote description: %w", err)
	}

	return nil
}

// AddICECandidate adds an ICE candidate from a viewer
func (pm *PeerManager) AddICECandidate(peerID string, candidateJSON string) error {
	pm.mu.RLock()
	pc, exists := pm.connections[peerID]
	pm.mu.RUnlock()

	if !exists {
		return fmt.Errorf("peer not found: %s", peerID)
	}

	var candidate webrtc.ICECandidateInit
	if err := json.Unmarshal([]byte(candidateJSON), &candidate); err != nil {
		return fmt.Errorf("failed to parse ICE candidate: %w", err)
	}

	err := pc.AddICECandidate(candidate)
	if err != nil {
		return fmt.Errorf("failed to add ICE candidate: %w", err)
	}

	return nil
}

// detectConnectionType checks if the connection is direct (P2P) or relayed (TURN)
func (pm *PeerManager) detectConnectionType(pc *webrtc.PeerConnection) string {
	stats := pc.GetStats()

	for _, stat := range stats {
		if candidatePair, ok := stat.(webrtc.ICECandidatePairStats); ok {
			if candidatePair.State == webrtc.StatsICECandidatePairStateSucceeded {
				// Check the local candidate type
				for _, s := range stats {
					if localCandidate, ok := s.(webrtc.ICECandidateStats); ok {
						if localCandidate.ID == candidatePair.LocalCandidateID {
							switch localCandidate.CandidateType {
							case webrtc.ICECandidateTypeRelay:
								return "relay"
							case webrtc.ICECandidateTypeHost:
								return "direct"
							case webrtc.ICECandidateTypeSrflx, webrtc.ICECandidateTypePrflx:
								return "direct" // Server reflexive is still P2P
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
func (pm *PeerManager) removePeer(peerID string) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	if pc, exists := pm.connections[peerID]; exists {
		pc.Close()
		delete(pm.connections, peerID)
	}
	delete(pm.viewerStates, peerID)
}

// GetViewerInfo returns information about all connected viewers
func (pm *PeerManager) GetViewerInfo() []ViewerInfo {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	viewers := make([]ViewerInfo, 0, len(pm.viewerStates))
	for _, info := range pm.viewerStates {
		viewers = append(viewers, *info)
	}
	return viewers
}

// WriteVideoSample writes a video sample to all connected peers
func (pm *PeerManager) WriteVideoSample(data []byte, duration time.Duration) error {
	return pm.videoTrack.WriteSample(media.Sample{
		Data:     data,
		Duration: duration,
	})
}

// GetConnectionCount returns number of active connections
func (pm *PeerManager) GetConnectionCount() int {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return len(pm.connections)
}

// Close closes all peer connections
func (pm *PeerManager) Close() {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	for id, pc := range pm.connections {
		pc.Close()
		delete(pm.connections, id)
	}
}

// StreamStats holds real-time streaming statistics
type StreamStats struct {
	// Encoding stats
	Width     int
	Height    int
	TargetFPS int
	ActualFPS float64
	Bitrate   int   // target bitrate in kbps
	ActualBPS int64 // actual bytes per second

	// Frame stats
	FramesSent int64
	BytesSent  int64

	// Timing
	StartTime  time.Time
	LastUpdate time.Time
}

// Streamer handles the capture-encode-stream pipeline
type Streamer struct {
	peerManager     *PeerManager
	encoder         VideoEncoder
	codecType       CodecType
	captureFuncBGRA func() (*BGRAFrame, error)
	fps             int
	bitrate         int
	running         bool
	stopChan        chan struct{}
	mu              sync.Mutex

	// Stats tracking
	stats          StreamStats
	statsMu        sync.RWMutex
	frameCount     int64
	byteCount      int64
	lastStatTime   time.Time
	lastFrameCount int64
	lastByteCount  int64
}

// NewStreamer creates a new streamer with default bitrate and codec from peer manager
func NewStreamer(pm *PeerManager, fps int) *Streamer {
	return NewStreamerWithBitrate(pm, fps, DefaultEncoderConfig().Bitrate)
}

// NewStreamerWithBitrate creates a new streamer with specified bitrate
func NewStreamerWithBitrate(pm *PeerManager, fps int, bitrate int) *Streamer {
	return NewStreamerWithCodec(pm, fps, bitrate, pm.GetCodecType())
}

// NewStreamerWithCodec creates a new streamer with specified codec
func NewStreamerWithCodec(pm *PeerManager, fps int, bitrate int, codecType CodecType) *Streamer {
	factory := NewEncoderFactory()
	encoder, err := factory.CreateEncoder(codecType, fps, bitrate)
	if err != nil {
		// Fallback to VP8 if encoder creation fails
		encoder, _ = factory.CreateEncoder(CodecVP8, fps, bitrate)
		codecType = CodecVP8
	}

	return &Streamer{
		peerManager: pm,
		encoder:     encoder,
		codecType:   codecType,
		fps:         fps,
		bitrate:     bitrate,
		stopChan:    make(chan struct{}),
	}
}

// GetCodecType returns the codec type being used
func (s *Streamer) GetCodecType() CodecType {
	return s.codecType
}

// IsHardwareAccelerated returns true if using hardware encoding
func (s *Streamer) IsHardwareAccelerated() bool {
	if s.encoder != nil {
		return s.encoder.IsHardwareAccelerated()
	}
	return false
}

// GetStats returns current streaming statistics
func (s *Streamer) GetStats() StreamStats {
	s.statsMu.RLock()
	defer s.statsMu.RUnlock()
	return s.stats
}

// SetCaptureFunc sets the function used to capture frames (BGRA format)
func (s *Streamer) SetCaptureFunc(fn func() (*BGRAFrame, error)) {
	s.captureFuncBGRA = fn
}

// Start starts the streaming loop
func (s *Streamer) Start() error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = true
	s.mu.Unlock()

	if err := s.encoder.Start(); err != nil {
		return err
	}

	// Initialize stats
	s.statsMu.Lock()
	s.stats = StreamStats{
		TargetFPS: s.fps,
		Bitrate:   s.bitrate,
		StartTime: time.Now(),
	}
	s.frameCount = 0
	s.byteCount = 0
	s.lastStatTime = time.Now()
	s.lastFrameCount = 0
	s.lastByteCount = 0
	s.statsMu.Unlock()

	go s.streamLoop()
	go s.statsLoop()
	return nil
}

// Stop stops the streaming loop
func (s *Streamer) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return
	}
	s.running = false
	s.mu.Unlock()

	close(s.stopChan)
	s.encoder.Stop()
}

// statsLoop updates statistics periodically
func (s *Streamer) statsLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopChan:
			return
		case <-ticker.C:
			s.updateStats()
		}
	}
}

// updateStats calculates current statistics
func (s *Streamer) updateStats() {
	s.statsMu.Lock()
	defer s.statsMu.Unlock()

	now := time.Now()
	elapsed := now.Sub(s.lastStatTime).Seconds()
	if elapsed > 0 {
		framesDelta := s.frameCount - s.lastFrameCount
		bytesDelta := s.byteCount - s.lastByteCount

		s.stats.ActualFPS = float64(framesDelta) / elapsed
		s.stats.ActualBPS = int64(float64(bytesDelta) / elapsed)
		s.stats.FramesSent = s.frameCount
		s.stats.BytesSent = s.byteCount
		s.stats.LastUpdate = now
	}

	s.lastStatTime = now
	s.lastFrameCount = s.frameCount
	s.lastByteCount = s.byteCount
}

// streamLoop is the main capture-encode-stream loop
func (s *Streamer) streamLoop() {
	frameDuration := time.Second / time.Duration(s.fps)
	ticker := time.NewTicker(frameDuration)
	defer ticker.Stop()

	var consecutiveErrors int
	const maxConsecutiveErrors = 30 // Only log after 1 second of errors at 30fps

	for {
		select {
		case <-s.stopChan:
			return
		case <-ticker.C:
			if s.captureFuncBGRA == nil {
				continue
			}

			// Capture frame (BGRA format - no conversion needed)
			frame, err := s.captureFuncBGRA()
			if err != nil {
				consecutiveErrors++
				if consecutiveErrors == maxConsecutiveErrors {
					log.Printf("Capture errors ongoing (suppressing further logs)")
				} else if consecutiveErrors < maxConsecutiveErrors {
					// Only log first few errors
				}
				continue
			}

			if consecutiveErrors >= maxConsecutiveErrors {
				log.Printf("Capture recovered after %d errors", consecutiveErrors)
			}
			consecutiveErrors = 0

			// Update resolution stats
			s.statsMu.Lock()
			s.stats.Width = frame.Width
			s.stats.Height = frame.Height
			s.statsMu.Unlock()

			// Encode frame (direct BGRA encoding - faster)
			data, err := s.encoder.EncodeBGRAFrame(frame)
			if err != nil {
				log.Printf("Encode error: %v", err)
				continue
			}

			// Track stats
			s.statsMu.Lock()
			s.frameCount++
			s.byteCount += int64(len(data))
			s.statsMu.Unlock()

			// Write to WebRTC track
			if err := s.peerManager.WriteVideoSample(data, frameDuration); err != nil {
				// This can happen if no peers are connected, which is fine
				continue
			}
		}
	}
}

// ViewerPeer represents a viewer's peer connection (for receiving)
type ViewerPeer struct {
	pc          *webrtc.PeerConnection
	onTrack     func(track *webrtc.TrackRemote)
	onICE       func(candidate string)
	onConnected func()
}

// NewViewerPeer creates a new viewer peer (for testing/debugging)
func NewViewerPeer() (*ViewerPeer, error) {
	config := webrtc.Configuration{
		ICEServers: defaultICEServers,
	}

	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		return nil, err
	}

	vp := &ViewerPeer{pc: pc}

	pc.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		if vp.onTrack != nil {
			vp.onTrack(track)
		}
	})

	pc.OnICECandidate(func(candidate *webrtc.ICECandidate) {
		if candidate == nil || vp.onICE == nil {
			return
		}
		candidateJSON, _ := json.Marshal(candidate.ToJSON())
		vp.onICE(string(candidateJSON))
	})

	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		if state == webrtc.PeerConnectionStateConnected && vp.onConnected != nil {
			vp.onConnected()
		}
	})

	return vp, nil
}

// HandleOffer processes an SDP offer and returns an answer
func (vp *ViewerPeer) HandleOffer(sdp string) (string, error) {
	offer := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  sdp,
	}

	if err := vp.pc.SetRemoteDescription(offer); err != nil {
		return "", err
	}

	answer, err := vp.pc.CreateAnswer(nil)
	if err != nil {
		return "", err
	}

	if err := vp.pc.SetLocalDescription(answer); err != nil {
		return "", err
	}

	// Wait for ICE gathering
	gatherComplete := webrtc.GatheringCompletePromise(vp.pc)
	<-gatherComplete

	return vp.pc.LocalDescription().SDP, nil
}

// AddICECandidate adds an ICE candidate
func (vp *ViewerPeer) AddICECandidate(candidateJSON string) error {
	var candidate webrtc.ICECandidateInit
	if err := json.Unmarshal([]byte(candidateJSON), &candidate); err != nil {
		return err
	}
	return vp.pc.AddICECandidate(candidate)
}

// Close closes the peer connection
func (vp *ViewerPeer) Close() {
	vp.pc.Close()
}
