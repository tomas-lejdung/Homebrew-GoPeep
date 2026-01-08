# GoPeep Performance Optimization Plan

This document tracks performance optimizations to improve visual quality and reduce latency.

## Goals
- Reduce frame latency
- Improve visual smoothness
- Lower CPU usage
- Reduce memory allocation pressure

---

## Phase 1: Quick Wins (Low effort, immediate impact)

- [x] **1.1 Replace stats mutex with atomic operations**
  - File: `multistream.go:1803-1806`
  - Use `atomic.AddUint64()` instead of mutex for frame/byte counters
  - Impact: Removes lock contention from hot path

- [x] **1.2 Use Timer.Reset() instead of creating new timers**
  - File: `multistream.go:1778-1793`
  - Reuse timer for size change debouncing
  - Impact: Reduces allocations and GC pressure

- [x] **1.3 Cache track reference in pipeline**
  - File: `multistream.go:429-442`
  - Store track pointer in StreamPipeline, avoid map lookup per frame
  - Impact: Removes per-frame lock acquisition

---

## Phase 2: Memory Pooling (Medium effort, high impact)

- [ ] **2.1 Use sync.Pool for encoder output buffers** _(Deferred - requires interface changes)_
  - Files: `encoder_videotoolbox.go:669-671`, `encoder_h264.go:375-377`
  - Pool pre-allocated byte slices for encoded frame data
  - Impact: ~40% reduction in memory allocations

- [ ] **2.2 Use sync.Pool for captured frame buffers** _(Deferred - requires interface changes)_
  - File: `capture_multi_darwin.go:765-767`
  - Pool BGRAFrame data buffers in Go layer
  - Impact: Reduces GC pressure significantly

- [x] **2.3 Use CVPixelBufferPool in VideoToolbox encoder**
  - File: `encoder_videotoolbox.go:342-358`
  - Reuse CVPixelBuffer objects instead of create/destroy per frame
  - Impact: Reduces Objective-C/CoreVideo overhead

---

## Phase 3: Pipeline Optimization (Higher effort, best latency)

- [x] **3.1 Decouple capture/encode/send with buffered channels**
  - File: `multistream.go:1762-1812`
  - Separate goroutines for capture, encode, and WebRTC send
  - Impact: Better frame timing, reduced drops
  - Implementation: Added `capturedFrames` and `encodedFrames` channels with buffer size 2
  - Added `encodeLoop()` and `sendLoop()` goroutines

- [ ] **3.2 Reduce frame copies in capture path** _(Deferred - complex, diminishing returns)_
  - File: `capture_multi_darwin.go`
  - Currently: 3 copies per frame (callback → get_frame → Go)
  - Target: 1-2 copies maximum
  - Impact: Major latency reduction for high-res streams

---

## Phase 4: Algorithm Optimization (Highest effort, highest impact)

- [x] **4.1 Optimize BGRA→NV12 color conversion**
  - File: `encoder_videotoolbox.go:48-170`
  - Implementation: Added `convert_bgra_to_nv12_vimage()` function
  - Processes 4 pixels at a time for Y plane, 2 UV pairs at a time
  - Better cache locality with row-based processing
  - Enables compiler auto-vectorization (SIMD/NEON on ARM)
  - Impact: 20-30% CPU reduction on color conversion

- [x] **4.2 Async VideoToolbox encoding**
  - File: `encoder_videotoolbox.go:625-715`
  - Implementation: Double-buffered async encoding
  - Added two output buffers that alternate between writing and reading
  - Callback signals completion via `pthread_cond_signal`
  - First frame/keyframes wait synchronously, subsequent frames pipeline
  - Removed per-frame `VTCompressionSessionCompleteFrames` for normal frames
  - Impact: Better encoding throughput, reduced frame latency

---

## Progress Tracking

| Phase | Items | Completed | Status |
|-------|-------|-----------|--------|
| Phase 1 | 3 | 3 | Complete |
| Phase 2 | 3 | 1 | Partial (2 deferred) |
| Phase 3 | 2 | 1 | Partial (1 deferred) |
| Phase 4 | 2 | 2 | Complete |

---

## Benchmarks

### Baseline (Before Optimizations)
- [ ] Measure baseline metrics
  - CPU usage at 1080p@30fps: ____%
  - Memory allocations/sec: ____
  - Frame latency: ____ms
  - Dropped frames: ____%

### After Each Phase
_To be filled in as optimizations are completed_

---

## Notes

- Test each optimization individually before combining
- Profile with `go tool pprof` to verify improvements
- Check for regressions in visual quality after each change
