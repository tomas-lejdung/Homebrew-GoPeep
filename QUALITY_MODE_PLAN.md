# Quality vs Performance Mode Implementation

## Overview

Add a toggle that switches between two encoder rate control modes:
- **Performance Mode** (default): CBR - bandwidth efficient, uses only what's needed
- **Quality Mode**: CQ/Quality-based - maintains consistent visual quality, uses more bandwidth

Also add a new "Insane" 50 Mbps quality preset.

## Tasks

| # | Task | Status | Files |
|---|------|--------|-------|
| 1 | Add "Insane" 50 Mbps preset | DONE | `quality.go` |
| 2 | Remove `q` quit key, keep only ctrl+c | DONE | `tui.go` |
| 3 | Extend VideoEncoder interface with SetQualityMode | DONE | `codec.go` |
| 4 | Add CQ mode support to VP8/VP9 encoder | DONE | `encoder.go` |
| 5 | Add CRF mode support to x264 encoder | DONE | `encoder_h264.go` |
| 6 | Add quality mode to VideoToolbox encoder | DONE | `encoder_videotoolbox.go` |
| 7 | Pass quality mode through Streamer | DONE | `multistream.go` |
| 8 | Add `q` toggle in TUI for quality mode | DONE | `tui.go` |
| 9 | Build and test | DONE | - |

---

## Task 1: Add "Insane" 50 Mbps Preset

**File:** `quality.go`

Add new preset to `QualityPresets` slice:
```go
{Name: "Insane", Bitrate: 50000, Description: "50 Mbps"},
```

Update `ParseQualityFlag()` to handle "insane".

---

## Task 2: Remove `q` Quit Key

**File:** `tui.go`

- Remove `case "q":` that triggers quit
- Keep `ctrl+c` as the only quit method
- Update help text to remove "q quit"

---

## Task 3: Extend VideoEncoder Interface

**File:** `codec.go`

Add to `VideoEncoder` interface:
```go
// SetQualityMode enables quality-priority encoding (true) vs performance/bandwidth-efficient (false)
SetQualityMode(enabled bool) error
```

---

## Task 4: VP8/VP9 CQ Mode

**File:** `encoder.go`

### C Code Changes

Add to `VPXEncoderContext` struct:
```c
int quality_mode;  // 0 = performance (CBR), 1 = quality (CQ)
int cq_level;      // CQ level when in quality mode
```

Modify encoder creation to use quality mode:
```c
if (quality_mode) {
    cfg.rc_end_usage = VPX_CQ;
    // CQ level set after init via control
} else {
    cfg.rc_end_usage = VPX_CBR;
}
// ...
if (quality_mode) {
    vpx_codec_control(&ctx->codec, VP8E_SET_CQ_LEVEL, cq_level);
}
```

Add function to set quality mode:
```c
int set_vpx_quality_mode(VPXEncoderContext* ctx, int enabled, int cq_level);
```

### Go Code Changes

Add fields to `VPXEncoder`:
```go
qualityMode bool
cqLevel     int
```

Add method:
```go
func (e *VPXEncoder) SetQualityMode(enabled bool) error
```

### CQ Level Mapping (lower = better quality)

| Preset | Bitrate | CQ Level |
|--------|---------|----------|
| Low | 500 | 40 |
| Medium | 1500 | 32 |
| High | 3000 | 24 |
| Very High | 6000 | 18 |
| Ultra | 10000 | 12 |
| Extreme | 15000 | 8 |
| Max | 20000 | 4 |
| Insane | 50000 | 2 |

---

## Task 5: x264 CRF Mode

**File:** `encoder_h264.go`

### C Code Changes

Add to `H264EncoderContext` struct:
```c
int quality_mode;
float crf_value;
```

Modify rate control:
```c
if (quality_mode) {
    ctx->params.rc.i_rc_method = X264_RC_CRF;
    ctx->params.rc.f_rf_constant = crf_value;
    ctx->params.rc.i_vbv_max_bitrate = bitrate;  // Still cap max
    ctx->params.rc.i_vbv_buffer_size = bitrate;
} else {
    ctx->params.rc.i_rc_method = X264_RC_ABR;
    ctx->params.rc.i_bitrate = bitrate;
    // ...existing CBR config
}
```

### CRF Mapping (lower = better quality)

| Preset | Bitrate | CRF |
|--------|---------|-----|
| Low | 500 | 28 |
| Medium | 1500 | 24 |
| High | 3000 | 21 |
| Very High | 6000 | 18 |
| Ultra | 10000 | 16 |
| Extreme | 15000 | 14 |
| Max | 20000 | 12 |
| Insane | 50000 | 10 |

---

## Task 6: VideoToolbox Quality Mode

**File:** `encoder_videotoolbox.go`

### C Code Changes

Add to `VideoToolboxContext` struct:
```c
int quality_mode;
float quality_value;  // 0.0-1.0
```

Modify session properties:
```c
if (quality_mode) {
    // Use quality-based encoding
    CFNumberRef qualityNum = CFNumberCreate(kCFAllocatorDefault, kCFNumberFloatType, &quality_value);
    VTSessionSetProperty(ctx->session, kVTCompressionPropertyKey_Quality, qualityNum);
    CFRelease(qualityNum);
    // Still set bitrate as soft ceiling
    VTSessionSetProperty(ctx->session, kVTCompressionPropertyKey_AverageBitRate, bitrateNum);
    // Remove strict DataRateLimits
} else {
    // Existing CBR behavior with DataRateLimits
}
```

### Quality Value Mapping (higher = better)

| Preset | Bitrate | VT Quality |
|--------|---------|------------|
| Low | 500 | 0.50 |
| Medium | 1500 | 0.65 |
| High | 3000 | 0.75 |
| Very High | 6000 | 0.85 |
| Ultra | 10000 | 0.90 |
| Extreme | 15000 | 0.95 |
| Max | 20000 | 0.98 |
| Insane | 50000 | 1.00 |

---

## Task 7: Streamer Quality Mode

**File:** `multistream.go`

Add to `Streamer` struct:
```go
qualityMode bool
```

Add method:
```go
func (ms *Streamer) SetQualityMode(enabled bool) {
    ms.mu.Lock()
    defer ms.mu.Unlock()
    ms.qualityMode = enabled
    for _, pipeline := range ms.pipelines {
        pipeline.SetQualityMode(enabled)
    }
}
```

Add to `StreamPipeline`:
```go
func (p *StreamPipeline) SetQualityMode(enabled bool) {
    if p.encoder != nil {
        p.encoder.SetQualityMode(enabled)
    }
}
```

Pass quality mode when creating new pipelines.

---

## Task 8: TUI Toggle

**File:** `tui.go`

Add to `model` struct:
```go
qualityMode bool  // false = performance, true = quality
```

Add key handler in `handleKeyboardInput`:
```go
case "q":
    m.qualityMode = !m.qualityMode
    if m.streamer != nil {
        m.streamer.SetQualityMode(m.qualityMode)
    }
    return m, nil
```

Update help text in `renderHelp`:
```go
if m.qualityMode {
    parts = append(parts, "q quality ON")
} else {
    parts = append(parts, "q performance")
}
```

Pass `qualityMode` to streamer on creation.

---

## Task 9: Build and Test

```bash
go build .
./gopeep
```

Test:
1. Start sharing with default (performance) mode
2. Press `q` to toggle to quality mode
3. Verify bitrate increases for static content
4. Toggle back to performance mode
5. Test with different quality presets including Insane

---

## Quality Mapping Helper

Add to `quality.go`:
```go
// QualityModeParams returns encoder parameters for quality mode
func (q QualityPreset) QualityModeParams() (cqLevel int, crf float32, vtQuality float32) {
    // Map bitrate to quality parameters
    switch q.Bitrate {
    case 500:
        return 40, 28, 0.50
    case 1500:
        return 32, 24, 0.65
    case 3000:
        return 24, 21, 0.75
    case 6000:
        return 18, 18, 0.85
    case 10000:
        return 12, 16, 0.90
    case 15000:
        return 8, 14, 0.95
    case 20000:
        return 4, 12, 0.98
    case 50000:
        return 2, 10, 1.00
    default:
        return 18, 18, 0.85 // Default to Very High
    }
}
```
