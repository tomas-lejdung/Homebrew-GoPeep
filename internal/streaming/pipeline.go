package streaming

import (
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v3/pkg/media"
	"github.com/tomaslejdung/gopeep/internal/capture"
	"github.com/tomaslejdung/gopeep/internal/encoding"
	"github.com/tomaslejdung/gopeep/internal/webrtc"
)

// capturedFrame holds a frame ready for encoding
type capturedFrame struct {
	frame         *capture.BGRAFrame
	frameDuration time.Duration
}

// encodedFrame holds encoded data ready for sending
type encodedFrame struct {
	data          []byte
	frameDuration time.Duration
}

// StreamPipeline manages capture-encode-stream for a single window
type StreamPipeline struct {
	trackInfo    *webrtc.StreamTrackInfo
	capture      *capture.CaptureInstance
	encoder      encoding.VideoEncoder
	running      bool
	stopping     bool
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

// Run is the main pipeline loop that captures, encodes, and sends frames
func (p *StreamPipeline) Run(
	pm *webrtc.PeerManager,
	mc *capture.MultiCapture,
	onSizeChange func(trackID string, width, height int),
) {
	p.mu.Lock()
	if p.stopping {
		p.mu.Unlock()
		return
	}
	if p.running {
		p.mu.Unlock()
		return
	}
	p.running = true
	p.lastStatsTime = time.Now()
	p.lastByteCount = 0
	p.wg.Add(1) // Track Run() itself; child goroutines use wg.Go()
	p.mu.Unlock()

	defer p.wg.Done() // For Run() completion

	p.mu.Lock()
	currentFPS := p.fps
	p.mu.Unlock()

	frameDuration := time.Second / time.Duration(currentFPS)

	// Create a done channel for coordinating goroutine shutdown
	done := make(chan struct{})

	// Start encoder goroutine (consumes captured frames, produces encoded frames)
	p.wg.Go(func() {
		p.encodeLoop(done)
	})

	// Start sender goroutine (consumes encoded frames, sends to WebRTC)
	p.wg.Go(func() {
		p.sendLoop(done)
	})

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
				go func(cap *capture.CaptureInstance, windowID uint32) {
					actualW, actualH, err := mc.GetWindowSize(cap)
					if err == nil && actualW > 0 && actualH > 0 {
						configW, configH, err := mc.GetConfigSize(cap)
						if err == nil && (configW != actualW || configH != actualH) {
							log.Printf(
								"Window resized: config %dx%d -> actual %dx%d, updating stream",
								configW,
								configH,
								actualW,
								actualH,
							)
							if err := mc.UpdateStreamSize(cap, actualW, actualH); err != nil {
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
					log.Printf(
						"encodeLoop: Encode failed for track %s (error #%d): %v",
						p.trackInfo.TrackID,
						errCount+1,
						err,
					)
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

// drainCapturedFrames releases any remaining frames in the capturedFrames buffer.
func (p *StreamPipeline) drainCapturedFrames() {
	for {
		select {
		case cf, ok := <-p.capturedFrames:
			if !ok {
				return
			}
			if cf.frame != nil {
				cf.frame.Release()
			}
		default:
			return
		}
	}
}

// sendLoop runs in a separate goroutine, sending encoded frames to WebRTC
func (p *StreamPipeline) sendLoop(done <-chan struct{}) {
	frameCount := 0
	lastSendTime := time.Now()

	for {
		select {
		case <-done:
			return
		case ef, ok := <-p.encodedFrames:
			if !ok {
				return
			}

			// Drain channel to get newest frame, dropping stale ones
			// This prevents accumulating latency when encoder falls behind
			newest := ef
			dropped := uint16(0)
		drainLoop:
			for {
				select {
				case newer, ok := <-p.encodedFrames:
					if !ok {
						break drainLoop
					}
					newest = newer
					dropped++
				default:
					break drainLoop
				}
			}

			if p.trackInfo.Track != nil {
				// Get current FPS for clamp bounds (may change at runtime via SetFPS)
				p.mu.Lock()
				currentFPS := p.fps
				p.mu.Unlock()
				targetDuration := time.Second / time.Duration(currentFPS)
				minDuration := targetDuration / 2
				maxDuration := targetDuration * 5

				// Use real elapsed time as Duration (pion uses this for RTP timestamps)
				// This keeps RTP timeline aligned with wall clock
				elapsed := time.Since(lastSendTime)

				// Clamp to reasonable bounds to avoid jitter issues
				sampleDuration := elapsed
				if sampleDuration < minDuration {
					sampleDuration = minDuration
				}
				if sampleDuration > maxDuration {
					sampleDuration = maxDuration
				}

				p.trackInfo.Track.WriteSample(media.Sample{
					Data:               newest.data,
					Duration:           sampleDuration,
					PrevDroppedPackets: dropped,
				})

				lastSendTime = time.Now()
				frameCount++

				// Log every 100 frames to confirm which track is receiving data
				if frameCount%100 == 1 {
					log.Printf(
						"sendLoop: Writing frame %d to track %s (windowID=%d, streamID=%s, duration=%v, dropped=%d)",
						frameCount,
						p.trackInfo.TrackID,
						p.trackInfo.WindowID,
						p.trackInfo.Track.StreamID(),
						sampleDuration,
						dropped,
					)
				}
			}
		}
	}
}

// GetStats returns current statistics for this pipeline
func (p *StreamPipeline) GetStats() webrtc.StreamPipelineStats {
	p.mu.Lock()
	fps := p.currentFPS
	bitrate := p.currentBitrate
	p.mu.Unlock()

	return webrtc.StreamPipelineStats{
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

// Stop stops the pipeline completely (encoder and capture)
// It waits for all goroutines to exit before returning.
func (p *StreamPipeline) Stop() {
	p.mu.Lock()
	alreadyStopping := p.stopping
	if !p.stopping {
		p.stopping = true
		close(p.stopChan)
	}
	p.running = false
	encoder := p.encoder
	p.mu.Unlock()

	if !alreadyStopping && encoder != nil {
		encoder.Stop()
	}

	p.wg.Wait()
	p.drainCapturedFrames()
}

// StopEncoderOnly stops the encoder and run loop but keeps capture alive for reuse.
// It waits for the run loop and goroutines to fully exit before returning.
func (p *StreamPipeline) StopEncoderOnly() {
	p.mu.Lock()
	alreadyStopping := p.stopping
	if !p.stopping {
		p.stopping = true
		close(p.stopChan)
	}
	p.running = false
	encoder := p.encoder
	p.mu.Unlock()

	if !alreadyStopping && encoder != nil {
		encoder.Stop()
	}

	// Wait for run loop to exit (outside mutex to avoid deadlock)
	p.wg.Wait()
	p.drainCapturedFrames()
	// Note: capture is NOT stopped - it will be reused
}

// updateBitrate updates the encoder bitrate based on focus state
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
func (p *StreamPipeline) SetFPS(
	newFPS int,
	mc *capture.MultiCapture,
	codecType encoding.CodecType,
) error {
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
	p.encoder, err = encoding.NewEncoder(codecType, encoding.EncoderConfig{
		FPS:     newFPS,
		Bitrate: p.bitrate,
	})
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

// GetTrackInfo returns the track info for this pipeline
func (p *StreamPipeline) GetTrackInfo() *webrtc.StreamTrackInfo {
	return p.trackInfo
}

// GetCapture returns the capture instance for this pipeline
func (p *StreamPipeline) GetCapture() *capture.CaptureInstance {
	return p.capture
}

// SetEncoder sets a new encoder for this pipeline
func (p *StreamPipeline) SetEncoder(encoder encoding.VideoEncoder) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.encoder = encoder
}

// SetTrackInfo sets the track info for this pipeline
func (p *StreamPipeline) SetTrackInfo(info *webrtc.StreamTrackInfo) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.trackInfo = info
}
