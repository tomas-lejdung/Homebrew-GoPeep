package main

/*
#cgo pkg-config: vpx
#include <vpx/vpx_encoder.h>
#include <vpx/vp8cx.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
    vpx_codec_ctx_t codec;
    vpx_image_t raw;
    int width;
    int height;
    int fps;
    int bitrate;
    int frame_count;
    int initialized;
    int is_vp9;
    int quality_mode;  // 0 = performance (CBR), 1 = quality (CQ)
    int cq_level;      // CQ level when in quality mode (0-63, lower = better)
} VPXEncoderContext;

VPXEncoderContext* create_vpx_encoder_with_mode(int width, int height, int fps, int bitrate, int use_vp9, int quality_mode, int cq_level) {
    VPXEncoderContext* ctx = (VPXEncoderContext*)calloc(1, sizeof(VPXEncoderContext));
    if (!ctx) return NULL;

    ctx->width = width;
    ctx->height = height;
    ctx->fps = fps;
    ctx->bitrate = bitrate;
    ctx->frame_count = 0;
    ctx->is_vp9 = use_vp9;
    ctx->quality_mode = quality_mode;
    ctx->cq_level = cq_level;

    // Select codec interface
    vpx_codec_iface_t* codec_iface = use_vp9 ? vpx_codec_vp9_cx() : vpx_codec_vp8_cx();

    // Get default config
    vpx_codec_enc_cfg_t cfg;
    if (vpx_codec_enc_config_default(codec_iface, &cfg, 0) != VPX_CODEC_OK) {
        free(ctx);
        return NULL;
    }

    // Configure encoder
    cfg.g_w = width;
    cfg.g_h = height;
    cfg.g_timebase.num = 1;
    cfg.g_timebase.den = fps;
    cfg.rc_target_bitrate = bitrate; // kbps - acts as ceiling in CQ mode
    cfg.g_error_resilient = VPX_ERROR_RESILIENT_DEFAULT;
    cfg.g_lag_in_frames = 0;         // Real-time mode
    cfg.kf_mode = VPX_KF_AUTO;
    cfg.kf_max_dist = fps * 2;       // Keyframe every 2 seconds

    // Rate control mode
    if (quality_mode) {
        cfg.rc_end_usage = VPX_CQ;   // Constrained Quality - prioritizes quality
    } else {
        cfg.rc_end_usage = VPX_CBR;  // Constant Bitrate - bandwidth efficient
    }

    if (use_vp9) {
        // VP9-specific settings for real-time
        cfg.g_threads = 4;           // Use multiple threads
    }

    // Initialize encoder
    if (vpx_codec_enc_init(&ctx->codec, codec_iface, &cfg, 0) != VPX_CODEC_OK) {
        free(ctx);
        return NULL;
    }

    // Set CQ level if in quality mode
    if (quality_mode) {
        vpx_codec_control(&ctx->codec, VP8E_SET_CQ_LEVEL, cq_level);
    }

    // Set real-time mode - different control for VP8 vs VP9
    if (use_vp9) {
        vpx_codec_control(&ctx->codec, VP8E_SET_CPUUSED, 8);  // 0-9, higher = faster
        vpx_codec_control(&ctx->codec, VP9E_SET_ROW_MT, 1);   // Row-based multi-threading
    } else {
        vpx_codec_control(&ctx->codec, VP8E_SET_CPUUSED, 8);  // Fastest
        vpx_codec_control(&ctx->codec, VP8E_SET_NOISE_SENSITIVITY, 0);
    }

    // Allocate image
    if (!vpx_img_alloc(&ctx->raw, VPX_IMG_FMT_I420, width, height, 16)) {
        vpx_codec_destroy(&ctx->codec);
        free(ctx);
        return NULL;
    }

    ctx->initialized = 1;
    return ctx;
}

// Legacy function - creates encoder in performance mode (CBR)
VPXEncoderContext* create_vpx_encoder(int width, int height, int fps, int bitrate, int use_vp9) {
    return create_vpx_encoder_with_mode(width, height, fps, bitrate, use_vp9, 0, 0);
}

// Legacy wrapper for VP8
VPXEncoderContext* create_encoder(int width, int height, int fps, int bitrate) {
    return create_vpx_encoder(width, height, fps, bitrate, 0);
}

void destroy_vpx_encoder(VPXEncoderContext* ctx) {
    if (!ctx) return;
    if (ctx->initialized) {
        vpx_img_free(&ctx->raw);
        vpx_codec_destroy(&ctx->codec);
    }
    free(ctx);
}

// Legacy wrapper
void destroy_encoder(VPXEncoderContext* ctx) {
    destroy_vpx_encoder(ctx);
}

// Encode a frame from BGRA data. Returns pointer to encoded data and sets size.
// The returned pointer is valid until next encode call.
const uint8_t* encode_vpx_frame(VPXEncoderContext* ctx, const uint8_t* bgra_data, int bgra_stride, int* out_size, int force_keyframe) {
    if (!ctx || !ctx->initialized || !bgra_data) {
        *out_size = 0;
        return NULL;
    }

    // Convert BGRA to I420 with SIMD-friendly batching and 2x2 UV averaging
    int width = ctx->width;
    int height = ctx->height;
    uint8_t* y_plane = ctx->raw.planes[VPX_PLANE_Y];
    uint8_t* u_plane = ctx->raw.planes[VPX_PLANE_U];
    uint8_t* v_plane = ctx->raw.planes[VPX_PLANE_V];
    int y_stride = ctx->raw.stride[VPX_PLANE_Y];
    int uv_stride = ctx->raw.stride[VPX_PLANE_U];

    // Process Y plane - full resolution with 4-pixel batching for SIMD
    for (int row = 0; row < height; row++) {
        const uint8_t* src_row = bgra_data + row * bgra_stride;
        uint8_t* y_row = y_plane + row * y_stride;

        // Process 4 pixels at a time for better vectorization
        int col = 0;
        for (; col + 3 < width; col += 4) {
            // Pixel 0
            int b0 = src_row[col * 4 + 0];
            int g0 = src_row[col * 4 + 1];
            int r0 = src_row[col * 4 + 2];
            int y0 = ((66 * r0 + 129 * g0 + 25 * b0 + 128) >> 8) + 16;

            // Pixel 1
            int b1 = src_row[(col + 1) * 4 + 0];
            int g1 = src_row[(col + 1) * 4 + 1];
            int r1 = src_row[(col + 1) * 4 + 2];
            int y1 = ((66 * r1 + 129 * g1 + 25 * b1 + 128) >> 8) + 16;

            // Pixel 2
            int b2 = src_row[(col + 2) * 4 + 0];
            int g2 = src_row[(col + 2) * 4 + 1];
            int r2 = src_row[(col + 2) * 4 + 2];
            int y2 = ((66 * r2 + 129 * g2 + 25 * b2 + 128) >> 8) + 16;

            // Pixel 3
            int b3 = src_row[(col + 3) * 4 + 0];
            int g3 = src_row[(col + 3) * 4 + 1];
            int r3 = src_row[(col + 3) * 4 + 2];
            int y3 = ((66 * r3 + 129 * g3 + 25 * b3 + 128) >> 8) + 16;

            // Clamp and store
            y_row[col] = (uint8_t)(y0 < 0 ? 0 : (y0 > 255 ? 255 : y0));
            y_row[col + 1] = (uint8_t)(y1 < 0 ? 0 : (y1 > 255 ? 255 : y1));
            y_row[col + 2] = (uint8_t)(y2 < 0 ? 0 : (y2 > 255 ? 255 : y2));
            y_row[col + 3] = (uint8_t)(y3 < 0 ? 0 : (y3 > 255 ? 255 : y3));
        }

        // Handle remaining pixels
        for (; col < width; col++) {
            int b = src_row[col * 4 + 0];
            int g = src_row[col * 4 + 1];
            int r = src_row[col * 4 + 2];
            int y = ((66 * r + 129 * g + 25 * b + 128) >> 8) + 16;
            y_row[col] = (uint8_t)(y < 0 ? 0 : (y > 255 ? 255 : y));
        }
    }

    // Process UV planes - half resolution with 2x2 block averaging for better quality
    for (int row = 0; row < height; row += 2) {
        const uint8_t* src_row0 = bgra_data + row * bgra_stride;
        const uint8_t* src_row1 = (row + 1 < height) ? bgra_data + (row + 1) * bgra_stride : src_row0;
        uint8_t* u_row = u_plane + (row / 2) * uv_stride;
        uint8_t* v_row = v_plane + (row / 2) * uv_stride;

        int col = 0;
        // Process 2 UV values at a time (4 source columns)
        for (; col + 3 < width; col += 4) {
            // First 2x2 block - average all 4 pixels
            int b00 = src_row0[col * 4 + 0];
            int g00 = src_row0[col * 4 + 1];
            int r00 = src_row0[col * 4 + 2];
            int b01 = src_row0[(col + 1) * 4 + 0];
            int g01 = src_row0[(col + 1) * 4 + 1];
            int r01 = src_row0[(col + 1) * 4 + 2];
            int b10 = src_row1[col * 4 + 0];
            int g10 = src_row1[col * 4 + 1];
            int r10 = src_row1[col * 4 + 2];
            int b11 = src_row1[(col + 1) * 4 + 0];
            int g11 = src_row1[(col + 1) * 4 + 1];
            int r11 = src_row1[(col + 1) * 4 + 2];

            // Average RGB values of 2x2 block
            int r_avg0 = (r00 + r01 + r10 + r11 + 2) >> 2;
            int g_avg0 = (g00 + g01 + g10 + g11 + 2) >> 2;
            int b_avg0 = (b00 + b01 + b10 + b11 + 2) >> 2;
            int u0 = ((-38 * r_avg0 - 74 * g_avg0 + 112 * b_avg0 + 128) >> 8) + 128;
            int v0 = ((112 * r_avg0 - 94 * g_avg0 - 18 * b_avg0 + 128) >> 8) + 128;

            // Second 2x2 block - average all 4 pixels
            int b02 = src_row0[(col + 2) * 4 + 0];
            int g02 = src_row0[(col + 2) * 4 + 1];
            int r02 = src_row0[(col + 2) * 4 + 2];
            int b03 = src_row0[(col + 3) * 4 + 0];
            int g03 = src_row0[(col + 3) * 4 + 1];
            int r03 = src_row0[(col + 3) * 4 + 2];
            int b12 = src_row1[(col + 2) * 4 + 0];
            int g12 = src_row1[(col + 2) * 4 + 1];
            int r12 = src_row1[(col + 2) * 4 + 2];
            int b13 = src_row1[(col + 3) * 4 + 0];
            int g13 = src_row1[(col + 3) * 4 + 1];
            int r13 = src_row1[(col + 3) * 4 + 2];

            // Average RGB values of 2x2 block
            int r_avg1 = (r02 + r03 + r12 + r13 + 2) >> 2;
            int g_avg1 = (g02 + g03 + g12 + g13 + 2) >> 2;
            int b_avg1 = (b02 + b03 + b12 + b13 + 2) >> 2;
            int u1 = ((-38 * r_avg1 - 74 * g_avg1 + 112 * b_avg1 + 128) >> 8) + 128;
            int v1 = ((112 * r_avg1 - 94 * g_avg1 - 18 * b_avg1 + 128) >> 8) + 128;

            // Clamp and store to separate U and V planes
            u_row[col / 2] = (uint8_t)(u0 < 0 ? 0 : (u0 > 255 ? 255 : u0));
            v_row[col / 2] = (uint8_t)(v0 < 0 ? 0 : (v0 > 255 ? 255 : v0));
            u_row[col / 2 + 1] = (uint8_t)(u1 < 0 ? 0 : (u1 > 255 ? 255 : u1));
            v_row[col / 2 + 1] = (uint8_t)(v1 < 0 ? 0 : (v1 > 255 ? 255 : v1));
        }

        // Handle remaining columns
        for (; col < width; col += 2) {
            // Average 2x2 block (or 2x1 if at edge)
            int b00 = src_row0[col * 4 + 0];
            int g00 = src_row0[col * 4 + 1];
            int r00 = src_row0[col * 4 + 2];
            int b01 = (col + 1 < width) ? src_row0[(col + 1) * 4 + 0] : b00;
            int g01 = (col + 1 < width) ? src_row0[(col + 1) * 4 + 1] : g00;
            int r01 = (col + 1 < width) ? src_row0[(col + 1) * 4 + 2] : r00;
            int b10 = src_row1[col * 4 + 0];
            int g10 = src_row1[col * 4 + 1];
            int r10 = src_row1[col * 4 + 2];
            int b11 = (col + 1 < width) ? src_row1[(col + 1) * 4 + 0] : b10;
            int g11 = (col + 1 < width) ? src_row1[(col + 1) * 4 + 1] : g10;
            int r11 = (col + 1 < width) ? src_row1[(col + 1) * 4 + 2] : r10;

            int r_avg = (r00 + r01 + r10 + r11 + 2) >> 2;
            int g_avg = (g00 + g01 + g10 + g11 + 2) >> 2;
            int b_avg = (b00 + b01 + b10 + b11 + 2) >> 2;
            int u = ((-38 * r_avg - 74 * g_avg + 112 * b_avg + 128) >> 8) + 128;
            int v = ((112 * r_avg - 94 * g_avg - 18 * b_avg + 128) >> 8) + 128;

            u_row[col / 2] = (uint8_t)(u < 0 ? 0 : (u > 255 ? 255 : u));
            v_row[col / 2] = (uint8_t)(v < 0 ? 0 : (v > 255 ? 255 : v));
        }
    }

    // Encode
    vpx_enc_frame_flags_t flags = force_keyframe ? VPX_EFLAG_FORCE_KF : 0;
    if (vpx_codec_encode(&ctx->codec, &ctx->raw, ctx->frame_count, 1, flags, VPX_DL_REALTIME) != VPX_CODEC_OK) {
        *out_size = 0;
        return NULL;
    }
    ctx->frame_count++;

    // Get encoded data
    const vpx_codec_cx_pkt_t* pkt;
    vpx_codec_iter_t iter = NULL;
    while ((pkt = vpx_codec_get_cx_data(&ctx->codec, &iter)) != NULL) {
        if (pkt->kind == VPX_CODEC_CX_FRAME_PKT) {
            *out_size = (int)pkt->data.frame.sz;
            return (const uint8_t*)pkt->data.frame.buf;
        }
    }

    *out_size = 0;
    return NULL;
}

// Legacy wrapper
const uint8_t* encode_frame(VPXEncoderContext* ctx, const uint8_t* rgba_data, int rgba_stride, int* out_size, int force_keyframe) {
    return encode_vpx_frame(ctx, rgba_data, rgba_stride, out_size, force_keyframe);
}

int encoder_width(VPXEncoderContext* ctx) {
    return ctx ? ctx->width : 0;
}

int encoder_height(VPXEncoderContext* ctx) {
    return ctx ? ctx->height : 0;
}

int encoder_is_vp9(VPXEncoderContext* ctx) {
    return ctx ? ctx->is_vp9 : 0;
}
*/
import "C"

import (
	"fmt"
	"image"
	"sync"
	"time"
	"unsafe"
)

// VPXEncoder encodes RGBA frames to VP8 or VP9
type VPXEncoder struct {
	ctx           *C.VPXEncoderContext
	width         int
	height        int
	fps           int
	bitrate       int
	frameCount    int
	mu            sync.Mutex
	initialized   bool
	isVP9         bool
	needsRecreate bool // Flag to recreate encoder on next frame (for bitrate/mode changes)
	qualityMode   bool // false = performance (CBR), true = quality (CQ)
	cqLevel       int  // CQ level for quality mode (0-63, lower = better)
}

// VP8Encoder is an alias for backwards compatibility
type VP8Encoder = VPXEncoder

// EncoderConfig holds encoder configuration
type EncoderConfig struct {
	Width   int
	Height  int
	FPS     int
	Bitrate int // in kbps
}

// DefaultEncoderConfig returns default encoder settings
func DefaultEncoderConfig() EncoderConfig {
	return EncoderConfig{
		Width:   1920,
		Height:  1080,
		FPS:     30,
		Bitrate: 2000, // 2 Mbps
	}
}

// NewVP8Encoder creates a new VP8 encoder
func NewVP8Encoder(config EncoderConfig) *VPXEncoder {
	return &VPXEncoder{
		width:   config.Width,
		height:  config.Height,
		fps:     config.FPS,
		bitrate: config.Bitrate,
		isVP9:   false,
	}
}

// NewVP9Encoder creates a new VP9 encoder
func NewVP9Encoder(config EncoderConfig) *VPXEncoder {
	return &VPXEncoder{
		width:   config.Width,
		height:  config.Height,
		fps:     config.FPS,
		bitrate: config.Bitrate,
		isVP9:   true,
	}
}

// Start initializes the encoder with actual dimensions
func (e *VPXEncoder) Start() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.initialized {
		return nil
	}

	// Encoder will be initialized on first frame with actual dimensions
	e.initialized = true
	return nil
}

// initWithDimensions initializes the encoder with specific dimensions
func (e *VPXEncoder) initWithDimensions(width, height int) error {
	// Ensure even dimensions
	if width%2 != 0 {
		width--
	}
	if height%2 != 0 {
		height--
	}

	e.width = width
	e.height = height

	useVP9 := 0
	if e.isVP9 {
		useVP9 = 1
	}

	qualityMode := 0
	if e.qualityMode {
		qualityMode = 1
	}

	ctx := C.create_vpx_encoder_with_mode(
		C.int(width), C.int(height), C.int(e.fps), C.int(e.bitrate),
		C.int(useVP9), C.int(qualityMode), C.int(e.cqLevel),
	)
	if ctx == nil {
		codecName := "VP8"
		if e.isVP9 {
			codecName = "VP9"
		}
		mode := "CBR"
		if e.qualityMode {
			mode = fmt.Sprintf("CQ(level=%d)", e.cqLevel)
		}
		return fmt.Errorf("failed to create %s encoder: %dx%d @ %dfps, %dkbps, mode=%s",
			codecName, width, height, e.fps, e.bitrate, mode)
	}

	e.ctx = ctx
	return nil
}

// Stop stops the encoder
func (e *VPXEncoder) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.ctx != nil {
		C.destroy_vpx_encoder(e.ctx)
		e.ctx = nil
	}
	e.initialized = false
}

// EncodeFrame encodes an RGBA image to VP8/VP9
// Deprecated: Use EncodeBGRAFrame for better performance
func (e *VPXEncoder) EncodeFrame(img *image.RGBA) ([]byte, error) {
	// Convert RGBA to BGRA for the encoder
	bounds := img.Bounds()
	width := bounds.Dx()
	height := bounds.Dy()

	bgra := &BGRAFrame{
		Data:   make([]byte, len(img.Pix)),
		Width:  width,
		Height: height,
		Stride: img.Stride,
	}

	// Convert RGBA to BGRA
	for i := 0; i < len(img.Pix); i += 4 {
		bgra.Data[i+0] = img.Pix[i+2] // B
		bgra.Data[i+1] = img.Pix[i+1] // G
		bgra.Data[i+2] = img.Pix[i+0] // R
		bgra.Data[i+3] = img.Pix[i+3] // A
	}

	return e.EncodeBGRAFrame(bgra)
}

// EncodeBGRAFrame encodes raw BGRA data to VP8/VP9 (faster, no color conversion)
func (e *VPXEncoder) EncodeBGRAFrame(frame *BGRAFrame) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.initialized {
		return nil, fmt.Errorf("encoder not initialized")
	}

	width := frame.Width
	height := frame.Height

	// Ensure even dimensions
	if width%2 != 0 {
		width--
	}
	if height%2 != 0 {
		height--
	}

	// Initialize encoder on first frame with actual dimensions
	if e.ctx == nil {
		if err := e.initWithDimensions(width, height); err != nil {
			return nil, err
		}
	}

	// Check if dimensions changed or bitrate needs update
	if width != e.width || height != e.height || e.needsRecreate {
		// Reinitialize with new dimensions/bitrate
		if e.ctx != nil {
			C.destroy_vpx_encoder(e.ctx)
		}
		if err := e.initWithDimensions(width, height); err != nil {
			return nil, err
		}
		e.needsRecreate = false
	}

	// Force keyframe on first frame
	forceKeyframe := 0
	if e.frameCount == 0 {
		forceKeyframe = 1
	}

	var outSize C.int
	dataPtr := C.encode_vpx_frame(
		e.ctx,
		(*C.uint8_t)(unsafe.Pointer(&frame.Data[0])),
		C.int(frame.Stride),
		&outSize,
		C.int(forceKeyframe),
	)

	if dataPtr == nil || outSize == 0 {
		codecName := "VP8"
		if e.isVP9 {
			codecName = "VP9"
		}
		return nil, fmt.Errorf("%s encoding failed: frame=%d, dimensions=%dx%d, keyframe=%v",
			codecName, e.frameCount, e.width, e.height, forceKeyframe == 1)
	}

	e.frameCount++

	// Copy the data since it's only valid until next encode
	result := make([]byte, int(outSize))
	copy(result, unsafe.Slice((*byte)(unsafe.Pointer(dataPtr)), int(outSize)))

	return result, nil
}

// GetSampleDuration returns the duration of one frame
func (e *VPXEncoder) GetSampleDuration() time.Duration {
	return time.Second / time.Duration(e.fps)
}

// GetCodecType returns the codec type
func (e *VPXEncoder) GetCodecType() CodecType {
	if e.isVP9 {
		return CodecVP9
	}
	return CodecVP8
}

// IsHardwareAccelerated returns false for VPX (software encoding)
func (e *VPXEncoder) IsHardwareAccelerated() bool {
	return false
}

// SetBitrate changes the target bitrate (kbps)
// The encoder will be recreated on the next frame encode
func (e *VPXEncoder) SetBitrate(bitrate int) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.bitrate == bitrate {
		return nil
	}

	e.bitrate = bitrate
	e.needsRecreate = true
	return nil
}

// SetQualityMode switches between quality and performance modes
// Quality mode (enabled=true): Uses CQ rate control for consistent visual quality
// Performance mode (enabled=false): Uses CBR rate control for bandwidth efficiency
// The encoder will be recreated on the next frame encode
func (e *VPXEncoder) SetQualityMode(enabled bool, bitrate int) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Get CQ level from quality params
	cqLevel, _, _ := QualityModeParams(bitrate)

	if e.qualityMode == enabled && e.cqLevel == cqLevel {
		return nil
	}

	e.qualityMode = enabled
	e.cqLevel = cqLevel
	e.needsRecreate = true
	return nil
}
