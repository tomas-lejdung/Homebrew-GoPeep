package webrtc

import (
	"fmt"
	"log"
	"sort"

	pwebrtc "github.com/pion/webrtc/v3"
	sig "github.com/tomaslejdung/gopeep/internal/signal"
)

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
		track, err := pwebrtc.NewTrackLocalStaticSample(
			pwebrtc.RTPCodecCapability{MimeType: mimeType},
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
		log.Printf(
			"InitializeTrackSlots: Created slot %d (trackID=%s, streamID=%s)",
			i,
			trackID,
			streamID,
		)
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

	// Recreate all 4 slots with new codec
	for i := 0; i < 4; i++ {
		trackID := fmt.Sprintf("video%d", i)
		streamID := fmt.Sprintf("gopeep-stream-%d", i)

		// Create new WebRTC track with new codec
		track, err := pwebrtc.NewTrackLocalStaticSample(
			pwebrtc.RTPCodecCapability{MimeType: mimeType},
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
		log.Printf(
			"RecreateSlots: Recreated slot %d (trackID=%s, streamID=%s)",
			i,
			trackID,
			streamID,
		)
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

		result = append(result, SlotInfo{
			TrackID:    slot.TrackID,
			WindowID:   active.windowID,
			WindowName: active.windowName,
			AppName:    active.appName,
			Track:      slot.Track,
			IsFocused:  active.isFocused,
		})
		log.Printf(
			"RecreateSlots: Restored active slot %d for window %d (%s)",
			active.slotIndex,
			active.windowID,
			active.windowName,
		)
	}

	log.Printf("RecreateSlots: Completed - %d active slots restored", len(result))
	return result, nil
}

// ActivateSlot activates a pre-allocated slot for a window.
// Returns the activated slot with its StreamTrackInfo populated.
// This is the fast path for adding windows - no renegotiation needed.
func (mpm *PeerManager) ActivateSlot(
	windowID uint32,
	windowName, appName string,
	width, height int,
) (*TrackSlot, error) {
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

	// Check if this is the first active slot (will be focused by default)
	activeCount := 0
	for i := 0; i < 4; i++ {
		if mpm.slots[i] != nil && mpm.slots[i].Active {
			activeCount++
		}
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
		IsFocused:  activeCount == 0, // First active slot is focused by default
	}

	log.Printf(
		"ActivateSlot: Activated %s for window %d (%s - %s)",
		slot.TrackID,
		windowID,
		appName,
		windowName,
	)
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
func (mpm *PeerManager) SetStreamActivationCallbacks(
	onActivated func(info sig.StreamInfo),
	onDeactivated func(trackID string),
) {
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
	for i := 0; i < 4; i++ {
		slot := mpm.slots[i]
		if slot != nil && slot.TrackID == trackID && slot.Active {
			return slot.Info
		}
	}
	return nil
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

// AddTrack creates a new video track for a window.
// This is a legacy function - use ActivateSlot for the fast path.
func (mpm *PeerManager) AddTrack(
	windowID uint32,
	windowName, appName string,
) (*StreamTrackInfo, error) {
	// Delegate to ActivateSlot which uses pre-allocated slots
	slot, err := mpm.ActivateSlot(windowID, windowName, appName, 0, 0)
	if err != nil {
		return nil, err
	}
	return slot.Info, nil
}

// AddTrackWithID creates a track with a specific ID.
// This is a legacy function - use ActivateSlot for the fast path.
func (mpm *PeerManager) AddTrackWithID(
	trackID string,
	windowID uint32,
	windowName, appName string,
) (*StreamTrackInfo, error) {
	// Since slots have fixed IDs, we can't create a specific ID
	// Just delegate to ActivateSlot
	slot, err := mpm.ActivateSlot(windowID, windowName, appName, 0, 0)
	if err != nil {
		return nil, err
	}
	return slot.Info, nil
}

// RemoveTrack removes a video track.
// This is a legacy function - use DeactivateSlot for the fast path.
func (mpm *PeerManager) RemoveTrack(trackID string) {
	mpm.DeactivateSlot(trackID)
}

// RemoveAllTracks removes all video tracks (used for FPS/settings restart)
func (mpm *PeerManager) RemoveAllTracks() {
	mpm.mu.Lock()
	defer mpm.mu.Unlock()
	mpm.trackCounter = 0 // Reset counter so track IDs start fresh on restart

	// Deactivate all slots so they can be reused
	for i := 0; i < 4; i++ {
		if mpm.slots[i] != nil {
			mpm.slots[i].Active = false
			mpm.slots[i].Info = nil
		}
	}
}

// GetTracks returns all active tracks in sorted order by TrackID.
// Tracks are now stored in slots - this iterates active slots.
func (mpm *PeerManager) GetTracks() []*StreamTrackInfo {
	mpm.mu.RLock()
	defer mpm.mu.RUnlock()

	// Collect track IDs from active slots
	trackIDs := make([]string, 0, 4)
	trackMap := make(map[string]*StreamTrackInfo)
	for i := 0; i < 4; i++ {
		slot := mpm.slots[i]
		if slot != nil && slot.Active && slot.Info != nil {
			trackIDs = append(trackIDs, slot.TrackID)
			trackMap[slot.TrackID] = slot.Info
		}
	}

	// Sort for consistent ordering
	sort.Strings(trackIDs)

	// Build result slice in sorted order
	tracks := make([]*StreamTrackInfo, 0, len(trackIDs))
	for _, id := range trackIDs {
		tracks = append(tracks, trackMap[id])
	}
	return tracks
}

// SetFocusedWindow sets focus based on window ID
func (mpm *PeerManager) SetFocusedWindow(windowID uint32) string {
	mpm.mu.Lock()
	defer mpm.mu.Unlock()

	var focusedTrackID string
	for i := 0; i < 4; i++ {
		slot := mpm.slots[i]
		if slot != nil && slot.Active && slot.Info != nil {
			if slot.Info.WindowID == windowID {
				slot.Info.IsFocused = true
				focusedTrackID = slot.TrackID
			} else {
				slot.Info.IsFocused = false
			}
		}
	}
	return focusedTrackID
}

// GetFocusedTrack returns the currently focused track
func (mpm *PeerManager) GetFocusedTrack() *StreamTrackInfo {
	mpm.mu.RLock()
	defer mpm.mu.RUnlock()

	for i := 0; i < 4; i++ {
		slot := mpm.slots[i]
		if slot != nil && slot.Active && slot.Info != nil && slot.Info.IsFocused {
			return slot.Info
		}
	}
	return nil
}
