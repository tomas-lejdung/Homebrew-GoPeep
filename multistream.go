package main

import (
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"sync"
	"sync/atomic"
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

// TrackSlot represents a pre-allocated track slot for instant window sharing
// All 4 slots are created upfront and included in the initial SDP offer,
// eliminating the need for renegotiation when adding new windows.
type TrackSlot struct {
	TrackID string                         // "video0", "video1", etc.
	Track   *webrtc.TrackLocalStaticSample // The actual WebRTC track
	Active  bool                           // Whether this slot has an active stream
	Info    *StreamTrackInfo               // Window info when active, nil when inactive
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
	onFocusChange  func(trackID string)                         // Called when focus changes to a new track
	onSizeChange   func(trackID string, width, height int)      // Called when focused track dimensions change
	onCursorUpdate func(trackID string, x, y float64, inView bool) // Called with cursor position updates

	// Renegotiation callbacks
	onRenegotiate   func(peerID string, offer string)
	onStreamAdded   func(info sig.StreamInfo)
	onStreamRemoved func(trackID string)

	// Pre-allocated track slots for instant window sharing
	slots      [4]*TrackSlot // Pre-allocated track slots (matches MaxCaptureInstances)
	slotsReady bool          // Whether slots have been initialized

	// Stream activation callbacks (no renegotiation needed)
	onStreamActivated   func(info sig.StreamInfo)
	onStreamDeactivated func(trackID string)
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

// getMimeType returns the MIME type for the current codec
func (mpm *PeerManager) getMimeType() string {
	switch mpm.codecType {
	case CodecVP9:
		return webrtc.MimeTypeVP9
	case CodecH264:
		return webrtc.MimeTypeH264
	default:
		return webrtc.MimeTypeVP8
	}
}

// InitializeTrackSlots creates all 4 track slots upfront for instant window sharing.
// This must be called before any viewers connect to ensure all tracks are in the initial SDP.
func (mpm *PeerManager) InitializeTrackSlots() error {
	mpm.mu.Lock()
	defer mpm.mu.Unlock()

	if mpm.slotsReady {
		return nil // Already initialized
	}

	mimeType := mpm.getMimeType()
	log.Printf("InitializeTrackSlots: Creating 4 pre-allocated track slots with codec %s", mimeType)

	for i := 0; i < 4; i++ {
		trackID := fmt.Sprintf("video%d", i)
		streamID := fmt.Sprintf("gopeep-stream-%d", i) // Unique stream ID per slot

		// Create the WebRTC track
		track, err := webrtc.NewTrackLocalStaticSample(
			webrtc.RTPCodecCapability{MimeType: mimeType},
			trackID,
			streamID,
		)
		if err != nil {
			return fmt.Errorf("failed to create track slot %d: %w", i, err)
		}

		mpm.slots[i] = &TrackSlot{
			TrackID: trackID,
			Track:   track,
			Active:  false,
			Info:    nil,
		}
		log.Printf("InitializeTrackSlots: Created slot %d (trackID=%s, streamID=%s)", i, trackID, streamID)
	}

	// Set trackCounter to 4 so legacy AddTrack won't conflict with slot IDs
	mpm.trackCounter = 4

	mpm.slotsReady = true
	log.Printf("InitializeTrackSlots: All 4 track slots ready")
	return nil
}

// AreSlotsReady returns whether track slots have been initialized
func (mpm *PeerManager) AreSlotsReady() bool {
	mpm.mu.RLock()
	defer mpm.mu.RUnlock()
	return mpm.slotsReady
}

// RecreateSlots recreates all track slots with a new codec.
// This is called during codec change to update the pre-allocated slots.
// Active slots are preserved (their window info is kept) but tracks are recreated.
// Returns info about which slots were active so pipelines can be reconnected.
func (mpm *PeerManager) RecreateSlots(newCodec CodecType) ([]SlotInfo, error) {
	mpm.mu.Lock()
	defer mpm.mu.Unlock()

	if !mpm.slotsReady {
		return nil, fmt.Errorf("track slots not initialized")
	}

	// Collect info about currently active slots
	type activeSlotInfo struct {
		slotIndex  int
		windowID   uint32
		windowName string
		appName    string
		width      int
		height     int
		isFocused  bool
	}
	activeSlots := make([]activeSlotInfo, 0)
	for i := 0; i < 4; i++ {
		if mpm.slots[i] != nil && mpm.slots[i].Active && mpm.slots[i].Info != nil {
			activeSlots = append(activeSlots, activeSlotInfo{
				slotIndex:  i,
				windowID:   mpm.slots[i].Info.WindowID,
				windowName: mpm.slots[i].Info.WindowName,
				appName:    mpm.slots[i].Info.AppName,
				width:      mpm.slots[i].Info.Width,
				height:     mpm.slots[i].Info.Height,
				isFocused:  mpm.slots[i].Info.IsFocused,
			})
		}
	}

	// Update codec type
	mpm.codecType = newCodec
	mimeType := mpm.getMimeType()
	log.Printf("RecreateSlots: Recreating 4 track slots with new codec %s", mimeType)

	// Clear tracks map (will be repopulated for active slots)
	mpm.tracks = make(map[string]*StreamTrackInfo)

	// Recreate all 4 slots with new codec
	for i := 0; i < 4; i++ {
		trackID := fmt.Sprintf("video%d", i)
		streamID := fmt.Sprintf("gopeep-stream-%d", i)

		// Create new WebRTC track with new codec
		track, err := webrtc.NewTrackLocalStaticSample(
			webrtc.RTPCodecCapability{MimeType: mimeType},
			trackID,
			streamID,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to recreate track slot %d: %w", i, err)
		}

		mpm.slots[i] = &TrackSlot{
			TrackID: trackID,
			Track:   track,
			Active:  false,
			Info:    nil,
		}
		log.Printf("RecreateSlots: Recreated slot %d (trackID=%s, streamID=%s)", i, trackID, streamID)
	}

	// Restore active slots with their window info
	result := make([]SlotInfo, 0, len(activeSlots))
	for _, active := range activeSlots {
		slot := mpm.slots[active.slotIndex]
		slot.Active = true
		slot.Info = &StreamTrackInfo{
			TrackID:    slot.TrackID,
			WindowID:   active.windowID,
			WindowName: active.windowName,
			AppName:    active.appName,
			Track:      slot.Track,
			Width:      active.width,
			Height:     active.height,
			IsFocused:  active.isFocused,
		}
		mpm.tracks[slot.TrackID] = slot.Info

		result = append(result, SlotInfo{
			TrackID:    slot.TrackID,
			WindowID:   active.windowID,
			WindowName: active.windowName,
			AppName:    active.appName,
			Track:      slot.Track,
			IsFocused:  active.isFocused,
		})
		log.Printf("RecreateSlots: Restored active slot %d for window %d (%s)", active.slotIndex, active.windowID, active.windowName)
	}

	log.Printf("RecreateSlots: Completed - %d active slots restored", len(result))
	return result, nil
}

// SlotInfo contains information about an active slot after recreation
type SlotInfo struct {
	TrackID    string
	WindowID   uint32
	WindowName string
	AppName    string
	Track      *webrtc.TrackLocalStaticSample
	IsFocused  bool
}

// ActivateSlot activates a pre-allocated slot for a window.
// Returns the activated slot with its StreamTrackInfo populated.
// This is the fast path for adding windows - no renegotiation needed.
func (mpm *PeerManager) ActivateSlot(windowID uint32, windowName, appName string, width, height int) (*TrackSlot, error) {
	mpm.mu.Lock()
	defer mpm.mu.Unlock()

	if !mpm.slotsReady {
		return nil, fmt.Errorf("track slots not initialized - call InitializeTrackSlots first")
	}

	// Check if this window is already active in a slot
	for i := 0; i < 4; i++ {
		slot := mpm.slots[i]
		if slot != nil && slot.Active && slot.Info != nil && slot.Info.WindowID == windowID {
			return nil, fmt.Errorf("window %d already has an active slot", windowID)
		}
	}

	// Find first inactive slot
	var slot *TrackSlot
	for i := 0; i < 4; i++ {
		if mpm.slots[i] != nil && !mpm.slots[i].Active {
			slot = mpm.slots[i]
			break
		}
	}

	if slot == nil {
		return nil, fmt.Errorf("no available track slots (max 4 windows)")
	}

	// Activate the slot
	slot.Active = true
	slot.Info = &StreamTrackInfo{
		TrackID:    slot.TrackID,
		WindowID:   windowID,
		WindowName: windowName,
		AppName:    appName,
		Track:      slot.Track,
		Width:      width,
		Height:     height,
		IsFocused:  len(mpm.tracks) == 0, // First active slot is focused by default
	}

	// Also add to tracks map for backward compatibility with existing code
	mpm.tracks[slot.TrackID] = slot.Info

	log.Printf("ActivateSlot: Activated %s for window %d (%s - %s)", slot.TrackID, windowID, appName, windowName)
	return slot, nil
}

// DeactivateSlot deactivates a slot when a window is unshared.
// The slot remains in the SDP but stops sending data.
func (mpm *PeerManager) DeactivateSlot(trackID string) error {
	mpm.mu.Lock()
	defer mpm.mu.Unlock()

	for i := 0; i < 4; i++ {
		slot := mpm.slots[i]
		if slot != nil && slot.TrackID == trackID {
			if !slot.Active {
				return fmt.Errorf("slot %s is not active", trackID)
			}

			slot.Active = false
			slot.Info = nil
			delete(mpm.tracks, trackID)

			log.Printf("DeactivateSlot: Deactivated %s", trackID)
			return nil
		}
	}

	return fmt.Errorf("slot not found: %s", trackID)
}

// GetActiveStreamsInfo returns StreamInfo for all active slots
// Used for sending initial streams-info to new viewers
func (mpm *PeerManager) GetActiveStreamsInfo() []sig.StreamInfo {
	mpm.mu.RLock()
	defer mpm.mu.RUnlock()

	result := make([]sig.StreamInfo, 0, 4)
	for i := 0; i < 4; i++ {
		slot := mpm.slots[i]
		if slot != nil && slot.Active && slot.Info != nil {
			result = append(result, sig.StreamInfo{
				TrackID:    slot.Info.TrackID,
				WindowName: slot.Info.WindowName,
				AppName:    slot.Info.AppName,
				IsFocused:  slot.Info.IsFocused,
				Width:      slot.Info.Width,
				Height:     slot.Info.Height,
			})
		}
	}
	return result
}

// SetStreamActivationCallbacks sets callbacks for stream activation/deactivation events
func (mpm *PeerManager) SetStreamActivationCallbacks(onActivated func(info sig.StreamInfo), onDeactivated func(trackID string)) {
	mpm.onStreamActivated = onActivated
	mpm.onStreamDeactivated = onDeactivated
}

// NotifyStreamActivated notifies that a stream was activated (no renegotiation)
func (mpm *PeerManager) NotifyStreamActivated(info sig.StreamInfo) {
	if mpm.onStreamActivated != nil {
		mpm.onStreamActivated(info)
	}
}

// NotifyStreamDeactivated notifies that a stream was deactivated
func (mpm *PeerManager) NotifyStreamDeactivated(trackID string) {
	if mpm.onStreamDeactivated != nil {
		mpm.onStreamDeactivated(trackID)
	}
}

// GetTrackInfo returns the StreamTrackInfo for a given track ID
func (mpm *PeerManager) GetTrackInfo(trackID string) *StreamTrackInfo {
	mpm.mu.RLock()
	defer mpm.mu.RUnlock()
	return mpm.tracks[trackID]
}

// GetSlot returns the TrackSlot at the given index (0-3)
func (mpm *PeerManager) GetSlot(index int) *TrackSlot {
	mpm.mu.RLock()
	defer mpm.mu.RUnlock()
	if index < 0 || index >= 4 || !mpm.slotsReady {
		return nil
	}
	return mpm.slots[index]
}

// AddTrack creates a new video track for a window
func (mpm *PeerManager) AddTrack(windowID uint32, windowName, appName string) (*StreamTrackInfo, error) {
	mpm.mu.Lock()
	// Generate track ID using monotonic counter (never reuses IDs)
	trackID := fmt.Sprintf("video%d", mpm.trackCounter)
	mpm.trackCounter++
	mpm.mu.Unlock()
	return mpm.addTrackWithID(trackID, windowID, windowName, appName)
}

// AddTrackWithID creates a track with a specific ID (used for codec changes to preserve track IDs)
func (mpm *PeerManager) AddTrackWithID(trackID string, windowID uint32, windowName, appName string) (*StreamTrackInfo, error) {
	return mpm.addTrackWithID(trackID, windowID, windowName, appName)
}

// addTrackWithID is the internal implementation for adding tracks
func (mpm *PeerManager) addTrackWithID(trackID string, windowID uint32, windowName, appName string) (*StreamTrackInfo, error) {
	mpm.mu.Lock()
	defer mpm.mu.Unlock()

	// Check if already have a track for this window
	for _, t := range mpm.tracks {
		if t.WindowID == windowID {
			return nil, fmt.Errorf("track already exists for window %d", windowID)
		}
	}

	// Check if track ID already exists
	if _, exists := mpm.tracks[trackID]; exists {
		return nil, fmt.Errorf("track ID %s already exists", trackID)
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

// RemoveAllTracks removes all video tracks (used for FPS/settings restart)
func (mpm *PeerManager) RemoveAllTracks() {
	mpm.mu.Lock()
	defer mpm.mu.Unlock()
	mpm.tracks = make(map[string]*StreamTrackInfo)
	mpm.trackCounter = 0 // Reset counter so track IDs start fresh on restart

	// Also deactivate all slots so they can be reused
	for i := 0; i < 4; i++ {
		if mpm.slots[i] != nil {
			mpm.slots[i].Active = false
			mpm.slots[i].Info = nil
		}
	}
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
	pc, err := webrtc.NewPeerConnection(mpm.config)
	if err != nil {
		return "", fmt.Errorf("failed to create peer connection: %w", err)
	}

	// Create PeerInfo to track senders
	peerInfo := &PeerInfo{
		PC:      pc,
		Senders: make(map[string]*webrtc.RTPSender),
	}

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
		log.Printf("CreateOffer: Using legacy mode (only active tracks)")
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
// capturedFrame holds a frame ready for encoding
type capturedFrame struct {
	frame         *BGRAFrame
	frameDuration time.Duration
}

// encodedFrame holds encoded data ready for sending
type encodedFrame struct {
	data          []byte
	frameDuration time.Duration
}

type StreamPipeline struct {
	trackInfo    *StreamTrackInfo
	capture      *CaptureInstance
	encoder      VideoEncoder
	running      bool
	stopChan     chan struct{}
	fpsChanged   chan int // Signal to update FPS in run loop
	fps          int
	bitrate      int
	focusBitrate int // bitrate when focused
	bgBitrate    int // bitrate when not focused (background)
	adaptiveBR   bool
	qualityMode  bool // false = performance, true = quality
	mu           sync.Mutex
	wg           sync.WaitGroup // For waiting on run loop to exit

	// Pipeline channels for decoupled capture/encode/send
	capturedFrames chan capturedFrame // Buffer between capture and encode
	encodedFrames  chan encodedFrame  // Buffer between encode and send

	// Stats tracking
	frameCount     uint64    // Total frames encoded
	byteCount      uint64    // Total bytes sent
	encodeErrors   uint64    // Consecutive encode errors (for logging)
	lastFrameTime  time.Time // For FPS calculation
	lastByteCount  uint64    // For bitrate calculation
	lastStatsTime  time.Time // When we last calculated rates
	currentFPS     float64   // Calculated FPS
	currentBitrate float64   // Calculated bitrate in kbps

	// Size change tracking (for debounced notifications)
	lastWidth          int
	lastHeight         int
	sizeChangeTimer    *time.Timer
	sizeChangePending  bool
	sizeChangeMu       sync.Mutex
	pendingSizeTrackID string
	pendingSizeWidth   int
	pendingSizeHeight  int
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
	onSizeChange    func(trackID string, width, height int)
	onCursorUpdate  func(trackID string, x, y float64, inView bool)

	// Cursor tracking state
	lastCursorX float64
	lastCursorY float64
	cursorMu    sync.Mutex
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

// newPipeline creates a new StreamPipeline with the standard configuration
func (ms *Streamer) newPipeline(trackInfo *StreamTrackInfo, capture *CaptureInstance, encoder VideoEncoder, bitrate int) *StreamPipeline {
	return &StreamPipeline{
		trackInfo:      trackInfo,
		capture:        capture,
		encoder:        encoder,
		fps:            ms.fps,
		bitrate:        bitrate,
		focusBitrate:   ms.focusBitrate,
		bgBitrate:      ms.bgBitrate,
		adaptiveBR:     ms.adaptiveBitrate,
		qualityMode:    ms.qualityMode,
		stopChan:       make(chan struct{}),
		fpsChanged:     make(chan int, 1),
		capturedFrames: make(chan capturedFrame, 4),
		encodedFrames:  make(chan encodedFrame, 4),
	}
}

// createAndConfigureEncoder creates an encoder with the current codec and quality settings
func (ms *Streamer) createAndConfigureEncoder(bitrate int) (VideoEncoder, error) {
	factory := NewEncoderFactory()
	encoder, err := factory.CreateEncoder(ms.codecType, ms.fps, bitrate)
	if err != nil {
		return nil, err
	}
	if ms.qualityMode {
		encoder.SetQualityMode(true, bitrate)
	}
	return encoder, nil
}

// SetOnFocusChange sets the callback for focus changes
func (ms *Streamer) SetOnFocusChange(callback func(trackID string)) {
	ms.onFocusChange = callback
}

// SetOnStreamsChange sets the callback for streams info changes
func (ms *Streamer) SetOnStreamsChange(callback func(streams []sig.StreamInfo)) {
	ms.onStreamsChange = callback
}

// SetOnSizeChange sets the callback for when focused track dimensions change
func (ms *Streamer) SetOnSizeChange(callback func(trackID string, width, height int)) {
	ms.onSizeChange = callback
}

// SetOnCursorUpdate sets the callback for cursor position updates
func (ms *Streamer) SetOnCursorUpdate(callback func(trackID string, x, y float64, inView bool)) {
	ms.onCursorUpdate = callback
}

// AddWindow adds a window to stream
func (ms *Streamer) AddWindow(window WindowInfo) (*StreamTrackInfo, error) {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	if len(ms.pipelines) >= MaxCaptureInstances {
		return nil, fmt.Errorf("maximum windows (%d) reached", MaxCaptureInstances)
	}

	var trackInfo *StreamTrackInfo

	// Use pre-allocated slots if available (fast path)
	if ms.peerManager.AreSlotsReady() {
		slot, err := ms.peerManager.ActivateSlot(
			window.ID,
			window.WindowName,
			window.OwnerName,
			int(window.Width),
			int(window.Height),
		)
		if err != nil {
			return nil, err
		}
		trackInfo = slot.Info
	} else {
		// Legacy path: create new track
		var err error
		trackInfo, err = ms.peerManager.AddTrack(window.ID, window.WindowName, window.OwnerName)
		if err != nil {
			return nil, err
		}
		trackInfo.Width = int(window.Width)
		trackInfo.Height = int(window.Height)
	}

	// Helper to clean up track on error
	useFastPath := ms.peerManager.AreSlotsReady()
	cleanupTrack := func() {
		if useFastPath {
			ms.peerManager.DeactivateSlot(trackInfo.TrackID)
		} else {
			ms.peerManager.RemoveTrack(trackInfo.TrackID)
		}
	}

	// Start capture
	capture, err := ms.multiCapture.StartWindowCapture(window.ID, 0, 0, ms.fps)
	if err != nil {
		cleanupTrack()
		return nil, fmt.Errorf("failed to start capture: %w", err)
	}

	// Determine initial bitrate
	bitrate := ms.focusBitrate
	if ms.adaptiveBitrate && !trackInfo.IsFocused {
		bitrate = ms.bgBitrate
	}

	// Create encoder
	encoder, err := ms.createAndConfigureEncoder(bitrate)
	if err != nil {
		ms.multiCapture.StopCapture(capture)
		cleanupTrack()
		return nil, fmt.Errorf("failed to create encoder: %w", err)
	}

	// Create pipeline
	pipeline := ms.newPipeline(trackInfo, capture, encoder, bitrate)

	ms.pipelines[trackInfo.TrackID] = pipeline

	// Start pipeline if already running
	if ms.running {
		go pipeline.run(ms.peerManager, ms.multiCapture, ms.onSizeChange)
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
		go pipeline.run(ms.peerManager, ms.multiCapture, ms.onSizeChange)
	}

	// Start focus detection loop
	go ms.focusDetectionLoop()

	// Start cursor tracking loop
	go ms.cursorTrackingLoop()

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

	// Notify viewers about each removed stream before clearing tracks
	// This ensures viewers clear their state properly
	if ms.peerManager != nil {
		for trackID := range ms.pipelines {
			ms.peerManager.NotifyStreamRemoved(trackID)
		}
		ms.peerManager.RemoveAllTracks()
	}
}

// focusDetectionLoop periodically checks for focus changes using z-order
func (ms *Streamer) focusDetectionLoop() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	var lastTopmostWindow uint32
	var fullscreenFocusSet bool // Track if we've set focus for fullscreen

	ms.mu.RLock()
	ms.mu.RUnlock()

	for {
		select {
		case <-ms.stopChan:
			return
		case <-ticker.C:
			// Collect all captured window IDs and check for fullscreen
			ms.mu.RLock()
			windowIDs := make([]uint32, 0, len(ms.pipelines))
			var hasFullscreen bool
			var fullscreenTrackID string
			for trackID, pipeline := range ms.pipelines {
				windowIDs = append(windowIDs, pipeline.trackInfo.WindowID)
				if pipeline.trackInfo.WindowID == 0 {
					hasFullscreen = true
					fullscreenTrackID = trackID
				}
			}
			pipelineCount := len(ms.pipelines)
			ms.mu.RUnlock()

			if len(windowIDs) == 0 {
				continue
			}

			// Special handling for fullscreen capture (windowID=0)
			// Fullscreen doesn't appear in z-order, so we handle it separately
			if hasFullscreen && pipelineCount == 1 {
				// Single fullscreen capture - always focused
				if !fullscreenFocusSet {
					ms.peerManager.SetFocusedWindow(0)
					ms.peerManager.NotifyFocusChange(fullscreenTrackID)
					if ms.onFocusChange != nil {
						go ms.onFocusChange(fullscreenTrackID)
					}
					fullscreenFocusSet = true
				}
				continue // Skip z-order check for single fullscreen
			}

			// Reset fullscreen focus flag if we're no longer in single-fullscreen mode
			if !hasFullscreen || pipelineCount > 1 {
				fullscreenFocusSet = false
			}

			// Find which captured window is topmost in z-order
			topmostWindow := GetTopmostWindow(windowIDs)

			if topmostWindow != lastTopmostWindow && topmostWindow != 0 {
				lastTopmostWindow = topmostWindow

				// Find the track for this window and update focus
				ms.mu.RLock()
				for trackID, pipeline := range ms.pipelines {
					if pipeline.trackInfo.WindowID == topmostWindow {
						newTrackID := ms.peerManager.SetFocusedWindow(topmostWindow)
						_ = trackID // unused after removing log

						if newTrackID != "" {
							// Notify via Streamer callback
							if ms.onFocusChange != nil {
								go ms.onFocusChange(newTrackID)
							}
							// Also notify via PeerManager callback (for signaling)
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

// cursorTrackingLoop sends cursor position updates at ~5fps to minimize message volume
func (ms *Streamer) cursorTrackingLoop() {
	ticker := time.NewTicker(200 * time.Millisecond) // ~5fps
	defer ticker.Stop()

	const threshold = 1.0 // Only send if cursor moved >1% of window

	for {
		select {
		case <-ms.stopChan:
			return
		case <-ticker.C:
			// Get focused track
			focusedTrack := ms.peerManager.GetFocusedTrack()
			if focusedTrack == nil {
				continue
			}

			// Get cursor position relative to focused window
			cursor := GetCursorPosition(focusedTrack.WindowID)

			// Convert to percentage coordinates
			var pctX, pctY float64
			if cursor.InWindow && cursor.WindowWidth > 0 && cursor.WindowHeight > 0 {
				pctX = (cursor.X / cursor.WindowWidth) * 100
				pctY = (cursor.Y / cursor.WindowHeight) * 100
			} else {
				pctX = -1
				pctY = -1
			}

			// Throttle: only send if moved significantly or cursor entered/left window
			ms.cursorMu.Lock()
			wasInWindow := ms.lastCursorX >= 0 && ms.lastCursorY >= 0
			dx := pctX - ms.lastCursorX
			dy := pctY - ms.lastCursorY
			if dx < 0 {
				dx = -dx
			}
			if dy < 0 {
				dy = -dy
			}
			shouldSend := (dx > threshold || dy > threshold) ||
				(cursor.InWindow != wasInWindow)

			if shouldSend {
				ms.lastCursorX = pctX
				ms.lastCursorY = pctY
				ms.cursorMu.Unlock()

				// Notify via PeerManager callback (for signaling)
				ms.peerManager.NotifyCursorUpdate(focusedTrack.TrackID, pctX, pctY, cursor.InWindow)
			} else {
				ms.cursorMu.Unlock()
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

// SetFPS updates the FPS for all active streams (requires capture restart)
func (ms *Streamer) SetFPS(newFPS int) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	if ms.fps == newFPS {
		return nil
	}

	ms.fps = newFPS

	var lastErr error
	for _, pipeline := range ms.pipelines {
		if err := pipeline.SetFPS(newFPS, ms.multiCapture, ms.codecType); err != nil {
			log.Printf("Failed to set FPS for pipeline: %v", err)
			lastErr = err
		}
	}

	if lastErr != nil {
		return lastErr
	}

	log.Printf("Streamer FPS updated to %d", newFPS)
	return nil
}

// SetCodec changes the codec dynamically without disconnecting viewers
func (ms *Streamer) SetCodec(newCodec CodecType) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()

	if ms.codecType == newCodec {
		return nil
	}

	log.Printf("SetCodec: Changing codec from %v to %v", ms.codecType, newCodec)

	// Check if we're using pre-allocated slots
	useSlotsPath := ms.peerManager.AreSlotsReady()

	// 1. Collect pipeline info before stopping (including captures to reuse)
	type pipelineInfo struct {
		trackID    string
		windowID   uint32
		windowName string
		appName    string
		wasFocused bool
		capture    *CaptureInstance
	}

	pipelineInfos := make([]pipelineInfo, 0, len(ms.pipelines))
	for _, pipeline := range ms.pipelines {
		pipelineInfos = append(pipelineInfos, pipelineInfo{
			trackID:    pipeline.trackInfo.TrackID,
			windowID:   pipeline.trackInfo.WindowID,
			windowName: pipeline.trackInfo.WindowName,
			appName:    pipeline.trackInfo.AppName,
			wasFocused: pipeline.trackInfo.IsFocused,
			capture:    pipeline.capture,
		})
	}

	// 2. Stop all pipeline run loops and encoders (but NOT captures)
	for _, pipeline := range ms.pipelines {
		pipeline.stopEncoderOnly()
	}

	// 3. Clear pipelines map
	ms.pipelines = make(map[string]*StreamPipeline)

	// 4. Update codec type on streamer
	ms.codecType = newCodec

	// 5. Recreate tracks/slots with new codec
	factory := NewEncoderFactory()

	if useSlotsPath {
		// SLOTS PATH: Recreate pre-allocated slots with new codec
		log.Printf("SetCodec: Using slots path - recreating slots with new codec")

		// First, remove all existing tracks from peer connections
		// This is necessary because codec change requires new transceivers
		for i := 0; i < 4; i++ {
			trackID := fmt.Sprintf("video%d", i)
			if err := ms.peerManager.RemoveTrackFromAllPeers(trackID); err != nil {
				log.Printf("SetCodec: Failed to remove track %s: %v", trackID, err)
			}
		}

		// Recreate slots with new codec
		slotInfos, err := ms.peerManager.RecreateSlots(newCodec)
		if err != nil {
			return fmt.Errorf("failed to recreate slots: %w", err)
		}

		// Create a map of trackID -> capture for quick lookup
		captureByTrackID := make(map[string]*CaptureInstance)
		for _, info := range pipelineInfos {
			captureByTrackID[info.trackID] = info.capture
		}

		// Add all slots to peer connections (including inactive ones for pre-allocation)
		for i := 0; i < 4; i++ {
			slot := ms.peerManager.GetSlot(i)
			if slot != nil {
				trackInfo := &StreamTrackInfo{
					TrackID: slot.TrackID,
					Track:   slot.Track,
				}
				if slot.Info != nil {
					trackInfo.WindowID = slot.Info.WindowID
					trackInfo.WindowName = slot.Info.WindowName
					trackInfo.AppName = slot.Info.AppName
					trackInfo.Width = slot.Info.Width
					trackInfo.Height = slot.Info.Height
					trackInfo.IsFocused = slot.Info.IsFocused
				}
				if err := ms.peerManager.AddTrackToAllPeers(trackInfo); err != nil {
					log.Printf("SetCodec: Failed to add slot %s to peers: %v", slot.TrackID, err)
				}
			}
		}

		// Recreate pipelines using the new slot tracks
		for _, slotInfo := range slotInfos {
			// Find the capture for this track
			capture, ok := captureByTrackID[slotInfo.TrackID]
			if !ok {
				log.Printf("SetCodec: No capture found for track %s, skipping", slotInfo.TrackID)
				continue
			}

			// Determine bitrate based on focus
			bitrate := ms.bgBitrate
			if slotInfo.IsFocused {
				bitrate = ms.focusBitrate
			}

			// Create new encoder with new codec
			encoder, err := factory.CreateEncoder(newCodec, ms.fps, bitrate)
			if err != nil {
				log.Printf("SetCodec: Failed to create encoder for %s: %v", slotInfo.TrackID, err)
				continue
			}

			// Apply quality mode if enabled
			if ms.qualityMode {
				encoder.SetQualityMode(true, bitrate)
			}

			if err := encoder.Start(); err != nil {
				log.Printf("SetCodec: Failed to start encoder for %s: %v", slotInfo.TrackID, err)
				continue
			}

			// Get the track info from the slot (it has the new Track pointer)
			trackInfo := ms.peerManager.GetTrackInfo(slotInfo.TrackID)
			if trackInfo == nil {
				log.Printf("SetCodec: Could not get track info for %s", slotInfo.TrackID)
				continue
			}

			// Create new pipeline with new encoder and new track reference
			pipeline := &StreamPipeline{
				trackInfo:      trackInfo,
				capture:        capture,
				encoder:        encoder,
				stopChan:       make(chan struct{}),
				fpsChanged:     make(chan int, 1),
				capturedFrames: make(chan capturedFrame, 4),
				encodedFrames:  make(chan encodedFrame, 4),
				fps:            ms.fps,
				bitrate:        bitrate,
				focusBitrate:   ms.focusBitrate,
				bgBitrate:      ms.bgBitrate,
				adaptiveBR:     ms.adaptiveBitrate,
				qualityMode:    ms.qualityMode,
				running:        false,
			}

			ms.pipelines[trackInfo.TrackID] = pipeline
			log.Printf("SetCodec: Created pipeline for slot %s (window %d)", slotInfo.TrackID, slotInfo.WindowID)
		}

	} else {
		// LEGACY PATH: Remove tracks and add new ones
		log.Printf("SetCodec: Using legacy path - creating new tracks")

		// First remove old tracks
		for _, info := range pipelineInfos {
			if err := ms.peerManager.RemoveTrackFromAllPeers(info.trackID); err != nil {
				log.Printf("SetCodec: Failed to remove track %s: %v", info.trackID, err)
			}
		}

		ms.peerManager.SetCodecType(newCodec)

		for _, info := range pipelineInfos {
			// Create new track with SAME track ID but new codec
			trackInfo, err := ms.peerManager.AddTrackWithID(info.trackID, info.windowID, info.windowName, info.appName)
			if err != nil {
				log.Printf("SetCodec: Failed to create track for window %d: %v", info.windowID, err)
				continue
			}
			trackInfo.IsFocused = info.wasFocused

			// Determine bitrate based on focus
			bitrate := ms.bgBitrate
			if info.wasFocused {
				bitrate = ms.focusBitrate
			}

			// Create new encoder with new codec
			encoder, err := factory.CreateEncoder(newCodec, ms.fps, bitrate)
			if err != nil {
				log.Printf("SetCodec: Failed to create encoder: %v", err)
				ms.peerManager.RemoveTrack(trackInfo.TrackID)
				continue
			}

			// Apply quality mode if enabled
			if ms.qualityMode {
				encoder.SetQualityMode(true, bitrate)
			}

			if err := encoder.Start(); err != nil {
				log.Printf("SetCodec: Failed to start encoder: %v", err)
				ms.peerManager.RemoveTrack(trackInfo.TrackID)
				continue
			}

			// Create new pipeline (reusing capture)
			pipeline := &StreamPipeline{
				trackInfo:      trackInfo,
				capture:        info.capture,
				encoder:        encoder,
				stopChan:       make(chan struct{}),
				fpsChanged:     make(chan int, 1),
				capturedFrames: make(chan capturedFrame, 4),
				encodedFrames:  make(chan encodedFrame, 4),
				fps:            ms.fps,
				bitrate:        bitrate,
				focusBitrate:   ms.focusBitrate,
				bgBitrate:      ms.bgBitrate,
				adaptiveBR:     ms.adaptiveBitrate,
				qualityMode:    ms.qualityMode,
				running:        false,
			}

			ms.pipelines[trackInfo.TrackID] = pipeline

			// Add track to all peers
			if err := ms.peerManager.AddTrackToAllPeers(trackInfo); err != nil {
				log.Printf("SetCodec: Failed to add track to peers: %v", err)
			}
		}
	}

	// 7. Trigger renegotiation with all peers
	log.Printf("SetCodec: Triggering renegotiation for %d pipelines", len(ms.pipelines))
	ms.peerManager.RenegotiateAllPeers()

	// 8. Start all new pipeline run loops
	for _, pipeline := range ms.pipelines {
		go pipeline.run(ms.peerManager, ms.multiCapture, ms.onSizeChange)
	}

	// 9. Notify streams change
	if ms.onStreamsChange != nil {
		streams := make([]sig.StreamInfo, 0, len(ms.pipelines))
		for _, pipeline := range ms.pipelines {
			streams = append(streams, sig.StreamInfo{
				TrackID:    pipeline.trackInfo.TrackID,
				WindowName: pipeline.trackInfo.WindowName,
				AppName:    pipeline.trackInfo.AppName,
				IsFocused:  pipeline.trackInfo.IsFocused,
			})
		}
		ms.onStreamsChange(streams)
	}

	log.Printf("SetCodec: Successfully changed codec to %v with %d pipelines", newCodec, len(ms.pipelines))
	return nil
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

// AddWindowDynamic adds a window without stopping other streams.
// If pre-allocated slots are ready, this is instant (no renegotiation).
// Otherwise, falls back to legacy mode with renegotiation.
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

	// Determine whether to use pre-allocated slots (fast path) or legacy mode
	useFastPath := ms.peerManager.AreSlotsReady()

	var trackInfo *StreamTrackInfo

	if useFastPath {
		// FAST PATH: Activate a pre-allocated slot (no renegotiation needed!)
		log.Printf("AddWindowDynamic: Using fast path (pre-allocated slots)")
		slot, err := ms.peerManager.ActivateSlot(
			window.ID,
			window.WindowName,
			window.OwnerName,
			int(window.Width),
			int(window.Height),
		)
		if err != nil {
			return nil, err
		}
		trackInfo = slot.Info
	} else {
		// LEGACY PATH: Create new track (requires renegotiation)
		log.Printf("AddWindowDynamic: Using legacy path (new track creation)")
		var err error
		trackInfo, err = ms.peerManager.AddTrack(window.ID, window.WindowName, window.OwnerName)
		if err != nil {
			return nil, err
		}
		trackInfo.Width = int(window.Width)
		trackInfo.Height = int(window.Height)
	}

	// Start capture for this window
	capture, err := ms.multiCapture.StartWindowCapture(window.ID, 0, 0, ms.fps)
	if err != nil {
		if useFastPath {
			ms.peerManager.DeactivateSlot(trackInfo.TrackID)
		} else {
			ms.peerManager.RemoveTrack(trackInfo.TrackID)
		}
		return nil, fmt.Errorf("failed to start capture: %w", err)
	}

	// Determine initial bitrate (new windows are not focused by default)
	bitrate := ms.bgBitrate
	if ms.adaptiveBitrate && trackInfo.IsFocused {
		bitrate = ms.focusBitrate
	}

	// Create encoder
	encoder, err := ms.createAndConfigureEncoder(bitrate)
	if err != nil {
		ms.multiCapture.StopCapture(capture)
		if useFastPath {
			ms.peerManager.DeactivateSlot(trackInfo.TrackID)
		} else {
			ms.peerManager.RemoveTrack(trackInfo.TrackID)
		}
		return nil, fmt.Errorf("failed to create encoder: %w", err)
	}

	// Create pipeline
	pipeline := ms.newPipeline(trackInfo, capture, encoder, bitrate)

	ms.mu.Lock()
	ms.pipelines[trackInfo.TrackID] = pipeline
	isRunning := ms.running
	ms.mu.Unlock()

	// Start pipeline if streamer is already running
	if isRunning {
		if err := encoder.Start(); err != nil {
			ms.multiCapture.StopCapture(capture)
			if useFastPath {
				ms.peerManager.DeactivateSlot(trackInfo.TrackID)
			} else {
				ms.peerManager.RemoveTrack(trackInfo.TrackID)
			}
			ms.mu.Lock()
			delete(ms.pipelines, trackInfo.TrackID)
			ms.mu.Unlock()
			return nil, fmt.Errorf("failed to start encoder: %w", err)
		}
		go pipeline.run(ms.peerManager, ms.multiCapture, ms.onSizeChange)
	}

	// Notify viewers about the stream
	streamInfo := sig.StreamInfo{
		TrackID:    trackInfo.TrackID,
		WindowName: trackInfo.WindowName,
		AppName:    trackInfo.AppName,
		IsFocused:  trackInfo.IsFocused,
		Width:      trackInfo.Width,
		Height:     trackInfo.Height,
	}

	if useFastPath {
		// FAST PATH: Just notify about activation (no renegotiation!)
		log.Printf("AddWindowDynamic: Notifying stream activated %s (NO renegotiation)", trackInfo.TrackID)
		ms.peerManager.NotifyStreamActivated(streamInfo)
	} else {
		// LEGACY PATH: Add track to peers and trigger renegotiation
		log.Printf("AddWindowDynamic: Adding track %s to all peers", trackInfo.TrackID)
		if err := ms.peerManager.AddTrackToAllPeers(trackInfo); err != nil {
			log.Printf("Warning: failed to add track to some peers: %v", err)
		}

		log.Printf("AddWindowDynamic: Notifying about new stream %s", trackInfo.TrackID)
		ms.peerManager.NotifyStreamAdded(streamInfo)

		log.Printf("AddWindowDynamic: Triggering renegotiation for track %s", trackInfo.TrackID)
		ms.peerManager.RenegotiateAllPeers()
	}

	log.Printf("Added window dynamically: %s (windowID=%d, fastPath=%v)", trackInfo.TrackID, window.ID, useFastPath)

	return trackInfo, nil
}

// IsWindowStreaming checks if a window is already being captured
func (ms *Streamer) IsWindowStreaming(windowID uint32) bool {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	for _, pipeline := range ms.pipelines {
		if pipeline.trackInfo.WindowID == windowID {
			return true
		}
	}
	return false
}

// GetActiveStreamCount returns number of active streams
func (ms *Streamer) GetActiveStreamCount() int {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	return len(ms.pipelines)
}

// RemoveWindowDynamic removes a window without stopping other streams.
// If pre-allocated slots are in use, this is instant (no renegotiation).
// Otherwise, falls back to legacy mode with renegotiation.
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

	// Determine whether slots are in use
	useFastPath := ms.peerManager.AreSlotsReady()

	log.Printf("Removing window dynamically: %s (windowID=%d, fastPath=%v)", trackIDToRemove, windowID, useFastPath)

	// Stop the pipeline
	pipelineToStop.stop()

	// Stop capture for this window
	ms.multiCapture.StopCapture(pipelineToStop.capture)

	if useFastPath {
		// FAST PATH: Deactivate the slot (no renegotiation!)
		// The track remains in the SDP but stops sending data
		if err := ms.peerManager.DeactivateSlot(trackIDToRemove); err != nil {
			log.Printf("Warning: failed to deactivate slot: %v", err)
		}

		// Notify about deactivated stream
		log.Printf("RemoveWindowDynamic: Notifying stream deactivated %s (NO renegotiation)", trackIDToRemove)
		ms.peerManager.NotifyStreamDeactivated(trackIDToRemove)
	} else {
		// LEGACY PATH: Remove track and renegotiate
		if err := ms.peerManager.RemoveTrackFromAllPeers(trackIDToRemove); err != nil {
			log.Printf("Warning: failed to remove track from some peers: %v", err)
		}

		ms.peerManager.RemoveTrack(trackIDToRemove)
		ms.peerManager.RenegotiateAllPeers()
		ms.peerManager.NotifyStreamRemoved(trackIDToRemove)
	}

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

	var trackInfo *StreamTrackInfo

	// Use pre-allocated slots if available (fast path)
	if ms.peerManager.AreSlotsReady() {
		slot, err := ms.peerManager.ActivateSlot(0, "Fullscreen", "Display", 0, 0)
		if err != nil {
			return nil, err
		}
		trackInfo = slot.Info
	} else {
		// Legacy path: create new track
		var err error
		trackInfo, err = ms.peerManager.AddTrack(0, "Fullscreen", "Display")
		if err != nil {
			return nil, err
		}
	}

	// Helper to clean up track on error
	useFastPath := ms.peerManager.AreSlotsReady()
	cleanupTrack := func() {
		if useFastPath {
			ms.peerManager.DeactivateSlot(trackInfo.TrackID)
		} else {
			ms.peerManager.RemoveTrack(trackInfo.TrackID)
		}
	}

	// Start display capture
	capture, err := ms.multiCapture.StartDisplayCapture(0, 0, ms.fps)
	if err != nil {
		cleanupTrack()
		return nil, fmt.Errorf("failed to start display capture: %w", err)
	}

	// Use focus bitrate for display (it's the main content)
	bitrate := ms.focusBitrate

	// Create encoder
	encoder, err := ms.createAndConfigureEncoder(bitrate)
	if err != nil {
		ms.multiCapture.StopCapture(capture)
		cleanupTrack()
		return nil, fmt.Errorf("failed to create encoder: %w", err)
	}

	// Create pipeline (no adaptive bitrate for display)
	pipeline := ms.newPipeline(trackInfo, capture, encoder, bitrate)
	pipeline.adaptiveBR = false

	ms.pipelines[trackInfo.TrackID] = pipeline

	// Start pipeline if already running
	if ms.running {
		go pipeline.run(ms.peerManager, ms.multiCapture, ms.onSizeChange)
	}

	// Notify about streams change
	if ms.onStreamsChange != nil {
		ms.onStreamsChange(ms.getStreamsInfo())
	}

	return trackInfo, nil
}

// AddDisplayDynamic adds display capture without stopping other streams.
// If pre-allocated slots are ready, this is instant (no renegotiation).
// Otherwise, falls back to legacy mode with renegotiation.
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

	// Determine whether to use pre-allocated slots (fast path) or legacy mode
	useFastPath := ms.peerManager.AreSlotsReady()

	var trackInfo *StreamTrackInfo

	if useFastPath {
		// FAST PATH: Activate a pre-allocated slot (no renegotiation needed!)
		log.Printf("AddDisplayDynamic: Using fast path (pre-allocated slots)")
		slot, err := ms.peerManager.ActivateSlot(0, "Fullscreen", "Display", 0, 0)
		if err != nil {
			return nil, err
		}
		trackInfo = slot.Info
	} else {
		// LEGACY PATH: Create new track (requires renegotiation)
		log.Printf("AddDisplayDynamic: Using legacy path (new track creation)")
		var err error
		trackInfo, err = ms.peerManager.AddTrack(0, "Fullscreen", "Display")
		if err != nil {
			return nil, err
		}
	}

	// Helper to clean up track on error
	cleanupTrack := func() {
		if useFastPath {
			ms.peerManager.DeactivateSlot(trackInfo.TrackID)
		} else {
			ms.peerManager.RemoveTrack(trackInfo.TrackID)
		}
	}

	// Start display capture
	capture, err := ms.multiCapture.StartDisplayCapture(0, 0, ms.fps)
	if err != nil {
		cleanupTrack()
		return nil, fmt.Errorf("failed to start display capture: %w", err)
	}

	// Use focus bitrate for display
	bitrate := ms.focusBitrate

	// Create encoder
	encoder, err := ms.createAndConfigureEncoder(bitrate)
	if err != nil {
		ms.multiCapture.StopCapture(capture)
		cleanupTrack()
		return nil, fmt.Errorf("failed to create encoder: %w", err)
	}

	// Create pipeline (no adaptive bitrate for display)
	pipeline := ms.newPipeline(trackInfo, capture, encoder, bitrate)
	pipeline.adaptiveBR = false

	ms.mu.Lock()
	ms.pipelines[trackInfo.TrackID] = pipeline
	isRunning := ms.running
	ms.mu.Unlock()

	// Start pipeline if streamer is already running
	if isRunning {
		if err := encoder.Start(); err != nil {
			ms.multiCapture.StopCapture(capture)
			cleanupTrack()
			ms.mu.Lock()
			delete(ms.pipelines, trackInfo.TrackID)
			ms.mu.Unlock()
			return nil, fmt.Errorf("failed to start encoder: %w", err)
		}
		go pipeline.run(ms.peerManager, ms.multiCapture, ms.onSizeChange)
	}

	// Notify viewers about the stream
	streamInfo := sig.StreamInfo{
		TrackID:    trackInfo.TrackID,
		WindowName: trackInfo.WindowName,
		AppName:    trackInfo.AppName,
		IsFocused:  true, // Display is always "focused"
		Width:      trackInfo.Width,
		Height:     trackInfo.Height,
	}

	if useFastPath {
		// FAST PATH: Just notify about activation (no renegotiation!)
		log.Printf("AddDisplayDynamic: Notifying stream activated %s (NO renegotiation)", trackInfo.TrackID)
		ms.peerManager.NotifyStreamActivated(streamInfo)
	} else {
		// LEGACY PATH: Add track to peers and trigger renegotiation
		log.Printf("AddDisplayDynamic: Adding track %s to all peers", trackInfo.TrackID)
		if err := ms.peerManager.AddTrackToAllPeers(trackInfo); err != nil {
			log.Printf("Warning: failed to add track to some peers: %v", err)
		}

		log.Printf("AddDisplayDynamic: Notifying about new stream %s", trackInfo.TrackID)
		ms.peerManager.NotifyStreamAdded(streamInfo)

		log.Printf("AddDisplayDynamic: Triggering renegotiation for track %s", trackInfo.TrackID)
		ms.peerManager.RenegotiateAllPeers()
	}

	log.Printf("Added display dynamically: %s (fastPath=%v)", trackInfo.TrackID, useFastPath)

	return trackInfo, nil
}

// RemoveDisplayDynamic removes display capture without stopping other streams
// This is just an alias for RemoveWindowDynamic(0) since display uses windowID=0
func (ms *Streamer) RemoveDisplayDynamic() error {
	return ms.RemoveWindowDynamic(0)
}

// Pipeline methods

func (p *StreamPipeline) run(pm *PeerManager, mc *MultiCapture, onSizeChange func(trackID string, width, height int)) {
	p.wg.Add(1)
	defer p.wg.Done()

	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return
	}
	p.running = true
	p.lastStatsTime = time.Now()
	p.lastByteCount = 0
	p.mu.Unlock()

	p.mu.Lock()
	currentFPS := p.fps
	p.mu.Unlock()

	frameDuration := time.Second / time.Duration(currentFPS)

	// Create a done channel for coordinating goroutine shutdown
	done := make(chan struct{})

	// Start encoder goroutine (consumes captured frames, produces encoded frames)
	go p.encodeLoop(done)

	// Start sender goroutine (consumes encoded frames, sends to WebRTC)
	go p.sendLoop(done)

	// Main capture loop
	ticker := time.NewTicker(frameDuration)
	defer ticker.Stop()

	// Stats update ticker (every second)
	statsTicker := time.NewTicker(time.Second)
	defer statsTicker.Stop()

	var framesSinceLastStats uint64

	defer func() {
		// Signal goroutines to stop and close channels
		close(done)
		close(p.capturedFrames)
		// encodedFrames will be closed by encodeLoop when it exits
	}()

	for {
		select {
		case <-p.stopChan:
			return

		case newFPS := <-p.fpsChanged:
			// FPS changed - update ticker
			ticker.Stop()
			currentFPS = newFPS
			frameDuration = time.Second / time.Duration(currentFPS)
			ticker = time.NewTicker(frameDuration)
			log.Printf("Pipeline ticker updated to %d FPS", currentFPS)

		case <-statsTicker.C:
			// Update FPS and bitrate calculations
			now := time.Now()
			currentByteCount := atomic.LoadUint64(&p.byteCount)
			p.mu.Lock()
			elapsed := now.Sub(p.lastStatsTime).Seconds()
			if elapsed > 0 {
				p.currentFPS = float64(framesSinceLastStats) / elapsed
				bytesDiff := currentByteCount - p.lastByteCount
				p.currentBitrate = float64(bytesDiff) * 8 / elapsed / 1000 // kbps
			}
			p.lastStatsTime = now
			p.lastByteCount = currentByteCount
			framesSinceLastStats = 0
			p.mu.Unlock()

			// Check if window has been resized and update stream configuration
			if p.capture != nil && p.trackInfo.WindowID != 0 {
				go func(capture *CaptureInstance, windowID uint32) {
					actualW, actualH, err := mc.GetWindowSize(capture)
					if err == nil && actualW > 0 && actualH > 0 {
						configW, configH, err := mc.GetConfigSize(capture)
						if err == nil && (configW != actualW || configH != actualH) {
							log.Printf("Window resized: config %dx%d -> actual %dx%d, updating stream", configW, configH, actualW, actualH)
							if err := mc.UpdateStreamSize(capture, actualW, actualH); err != nil {
								log.Printf("Failed to update stream size: %v", err)
							}
						}
					}
				}(p.capture, p.trackInfo.WindowID)
			}

		case <-ticker.C:
			frame, err := mc.GetLatestFrameBGRA(p.capture, 100*time.Millisecond)
			if err != nil {
				continue
			}

			// Check for dimension changes and notify (debounced, focused track only)
			if frame.Width != p.lastWidth || frame.Height != p.lastHeight {
				p.lastWidth = frame.Width
				p.lastHeight = frame.Height
				p.trackInfo.Width = frame.Width
				p.trackInfo.Height = frame.Height

				// Debounced size change notification (only for focused track)
				if p.trackInfo.IsFocused {
					p.sizeChangeMu.Lock()
					p.pendingSizeTrackID = p.trackInfo.TrackID
					p.pendingSizeWidth = frame.Width
					p.pendingSizeHeight = frame.Height
					p.sizeChangePending = true

					if p.sizeChangeTimer == nil {
						p.sizeChangeTimer = time.AfterFunc(250*time.Millisecond, func() {
							p.sizeChangeMu.Lock()
							if p.sizeChangePending {
								trackID := p.pendingSizeTrackID
								width := p.pendingSizeWidth
								height := p.pendingSizeHeight
								p.sizeChangePending = false
								p.sizeChangeMu.Unlock()
								pm.NotifySizeChange(trackID, width, height)
								if onSizeChange != nil {
									onSizeChange(trackID, width, height)
								}
							} else {
								p.sizeChangeMu.Unlock()
							}
						})
					} else {
						p.sizeChangeTimer.Reset(250 * time.Millisecond)
					}
					p.sizeChangeMu.Unlock()
				}
			}

			// Send frame to encode goroutine (non-blocking with small buffer)
			select {
			case p.capturedFrames <- capturedFrame{frame: frame, frameDuration: frameDuration}:
				framesSinceLastStats++
			default:
				// Buffer full - drop frame to maintain timing
				// This prevents capture from blocking if encoding is slow
				// Release the frame back to capture pool since encode loop won't see it
				frame.Release()
			}
		}
	}
}

// encodeLoop runs in a separate goroutine, encoding frames as they arrive
func (p *StreamPipeline) encodeLoop(done <-chan struct{}) {
	defer close(p.encodedFrames)

	for {
		select {
		case <-done:
			return
		case cf, ok := <-p.capturedFrames:
			if !ok {
				return
			}

			// Encode the frame
			data, err := p.encoder.EncodeBGRAFrame(cf.frame)

			// Release frame buffer back to capture pool (zero-copy)
			// Must be done after encoding, whether it succeeded or not
			cf.frame.Release()

			if err != nil {
				// Log encode failures (first 5 after any recreation)
				errCount := atomic.LoadUint64(&p.encodeErrors)
				atomic.AddUint64(&p.encodeErrors, 1)
				if errCount < 5 {
					log.Printf("encodeLoop: Encode failed for track %s (error #%d): %v", p.trackInfo.TrackID, errCount+1, err)
				}
				continue
			}

			// Reset error counter on successful encode
			atomic.StoreUint64(&p.encodeErrors, 0)

			atomic.AddUint64(&p.frameCount, 1)
			atomic.AddUint64(&p.byteCount, uint64(len(data)))

			// Send to sender goroutine (non-blocking)
			select {
			case p.encodedFrames <- encodedFrame{data: data, frameDuration: cf.frameDuration}:
			default:
				// Buffer full - drop encoded frame
			}
		}
	}
}

// sendLoop runs in a separate goroutine, sending encoded frames to WebRTC
func (p *StreamPipeline) sendLoop(done <-chan struct{}) {
	frameCount := 0
	for {
		select {
		case <-done:
			return
		case ef, ok := <-p.encodedFrames:
			if !ok {
				return
			}

			// Write directly to track
			if p.trackInfo.Track != nil {
				p.trackInfo.Track.WriteSample(media.Sample{
					Data:     ef.data,
					Duration: ef.frameDuration,
				})
				frameCount++
				// Log every 100 frames to confirm which track is receiving data
				if frameCount%100 == 1 {
					log.Printf("sendLoop: Writing frame %d to track %s (windowID=%d, streamID=%s)",
						frameCount, p.trackInfo.TrackID, p.trackInfo.WindowID, p.trackInfo.Track.StreamID())
				}
			}
		}
	}
}

// GetStats returns current statistics for this pipeline
func (p *StreamPipeline) GetStats() StreamPipelineStats {
	p.mu.Lock()
	fps := p.currentFPS
	bitrate := p.currentBitrate
	p.mu.Unlock()

	return StreamPipelineStats{
		TrackID:   p.trackInfo.TrackID,
		AppName:   p.trackInfo.AppName,
		Width:     p.trackInfo.Width,
		Height:    p.trackInfo.Height,
		FPS:       fps,
		Bitrate:   bitrate,
		Frames:    atomic.LoadUint64(&p.frameCount),
		Bytes:     atomic.LoadUint64(&p.byteCount),
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

// stopEncoderOnly stops the encoder and run loop but keeps capture alive for reuse
// It waits for the run loop to fully exit before returning
func (p *StreamPipeline) stopEncoderOnly() {
	p.mu.Lock()
	if !p.running {
		p.mu.Unlock()
		return
	}

	p.running = false
	close(p.stopChan)
	if p.encoder != nil {
		p.encoder.Stop()
	}
	p.mu.Unlock()

	// Wait for run loop to exit (outside mutex to avoid deadlock)
	p.wg.Wait()
	// Note: capture is NOT stopped - it will be reused
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

// SetFPS updates the FPS for this pipeline (requires capture restart)
func (p *StreamPipeline) SetFPS(newFPS int, mc *MultiCapture, codecType CodecType) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.fps == newFPS {
		return nil
	}

	oldFPS := p.fps
	p.fps = newFPS

	// Stop current capture
	if p.capture != nil {
		mc.StopCapture(p.capture)
	}

	// Stop current encoder
	if p.encoder != nil {
		p.encoder.Stop()
	}

	// Restart capture with new FPS
	var err error
	if p.trackInfo.WindowID == 0 {
		// Display capture
		p.capture, err = mc.StartDisplayCapture(0, 0, newFPS)
	} else {
		// Window capture
		p.capture, err = mc.StartWindowCapture(p.trackInfo.WindowID, 0, 0, newFPS)
	}
	if err != nil {
		p.fps = oldFPS // Restore on error
		return fmt.Errorf("failed to restart capture with new FPS: %w", err)
	}

	// Create new encoder with new FPS
	factory := NewEncoderFactory()
	p.encoder, err = factory.CreateEncoder(codecType, newFPS, p.bitrate)
	if err != nil {
		p.fps = oldFPS
		return fmt.Errorf("failed to create encoder with new FPS: %w", err)
	}

	// Apply quality mode if enabled
	if p.qualityMode {
		p.encoder.SetQualityMode(true, p.bitrate)
	}

	if err := p.encoder.Start(); err != nil {
		p.fps = oldFPS
		return fmt.Errorf("failed to start encoder: %w", err)
	}

	log.Printf("Track %s FPS changed from %d to %d", p.trackInfo.TrackID, oldFPS, newFPS)

	// Signal the run loop to update its ticker
	select {
	case p.fpsChanged <- newFPS:
	default:
		// Channel full, run loop will pick up new FPS from p.fps
	}

	return nil
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
