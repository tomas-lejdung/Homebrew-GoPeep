package main

import (
	"fmt"
	"log"
	"sync"
	"time"

	sig "github.com/tomaslejdung/gopeep/pkg/signal"
)

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
		capturedFrames: make(chan capturedFrame, 2),
		encodedFrames:  make(chan encodedFrame, 2),
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
				capturedFrames: make(chan capturedFrame, 2),
				encodedFrames:  make(chan encodedFrame, 2),
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
				capturedFrames: make(chan capturedFrame, 2),
				encodedFrames:  make(chan encodedFrame, 2),
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
		IsFocused:  trackInfo.IsFocused,
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
