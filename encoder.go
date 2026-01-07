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
    int frame_count;
    int initialized;
    int is_vp9;
} VPXEncoderContext;

VPXEncoderContext* create_vpx_encoder(int width, int height, int fps, int bitrate, int use_vp9) {
    VPXEncoderContext* ctx = (VPXEncoderContext*)calloc(1, sizeof(VPXEncoderContext));
    if (!ctx) return NULL;

    ctx->width = width;
    ctx->height = height;
    ctx->fps = fps;
    ctx->frame_count = 0;
    ctx->is_vp9 = use_vp9;

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
    cfg.rc_target_bitrate = bitrate; // kbps
    cfg.g_error_resilient = VPX_ERROR_RESILIENT_DEFAULT;
    cfg.g_lag_in_frames = 0;         // Real-time mode
    cfg.rc_end_usage = VPX_CBR;      // Constant bitrate
    cfg.kf_mode = VPX_KF_AUTO;
    cfg.kf_max_dist = fps * 2;       // Keyframe every 2 seconds

    if (use_vp9) {
        // VP9-specific settings for real-time
        cfg.g_threads = 4;           // Use multiple threads
    }

    // Initialize encoder
    if (vpx_codec_enc_init(&ctx->codec, codec_iface, &cfg, 0) != VPX_CODEC_OK) {
        free(ctx);
        return NULL;
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

    // Convert BGRA to I420 directly (no intermediate RGBA conversion)
    int width = ctx->width;
    int height = ctx->height;
    uint8_t* y_plane = ctx->raw.planes[VPX_PLANE_Y];
    uint8_t* u_plane = ctx->raw.planes[VPX_PLANE_U];
    uint8_t* v_plane = ctx->raw.planes[VPX_PLANE_V];
    int y_stride = ctx->raw.stride[VPX_PLANE_Y];
    int uv_stride = ctx->raw.stride[VPX_PLANE_U];

    for (int row = 0; row < height; row++) {
        for (int col = 0; col < width; col++) {
            int bgra_idx = row * bgra_stride + col * 4;
            // BGRA format: B=0, G=1, R=2, A=3
            int b = bgra_data[bgra_idx];
            int g = bgra_data[bgra_idx + 1];
            int r = bgra_data[bgra_idx + 2];

            // RGB to YUV (BT.601)
            int y = ((66 * r + 129 * g + 25 * b + 128) >> 8) + 16;
            int u = ((-38 * r - 74 * g + 112 * b + 128) >> 8) + 128;
            int v = ((112 * r - 94 * g - 18 * b + 128) >> 8) + 128;

            // Clamp
            if (y < 0) y = 0; else if (y > 255) y = 255;
            if (u < 0) u = 0; else if (u > 255) u = 255;
            if (v < 0) v = 0; else if (v > 255) v = 255;

            y_plane[row * y_stride + col] = (uint8_t)y;

            // Subsample UV
            if (row % 2 == 0 && col % 2 == 0) {
                int uv_row = row / 2;
                int uv_col = col / 2;
                u_plane[uv_row * uv_stride + uv_col] = (uint8_t)u;
                v_plane[uv_row * uv_stride + uv_col] = (uint8_t)v;
            }
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
	needsRecreate bool // Flag to recreate encoder on next frame (for bitrate changes)
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

	ctx := C.create_vpx_encoder(C.int(width), C.int(height), C.int(e.fps), C.int(e.bitrate), C.int(useVP9))
	if ctx == nil {
		codecName := "VP8"
		if e.isVP9 {
			codecName = "VP9"
		}
		return fmt.Errorf("failed to create %s encoder", codecName)
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
		return nil, fmt.Errorf("encoding failed")
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
