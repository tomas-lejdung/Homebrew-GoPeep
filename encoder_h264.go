package main

/*
#cgo pkg-config: x264
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <x264.h>

typedef struct {
    x264_t* encoder;
    x264_param_t params;
    x264_picture_t pic_in;
    x264_picture_t pic_out;
    int width;
    int height;
    int fps;
    int bitrate;
    int frame_count;
    int initialized;
    int quality_mode;  // 0 = performance (ABR), 1 = quality (CRF)
    float crf_value;   // CRF value when in quality mode (0-51, lower = better)
} H264EncoderContext;

H264EncoderContext* create_h264_encoder_with_mode(int width, int height, int fps, int bitrate, int quality_mode, float crf_value) {
    H264EncoderContext* ctx = (H264EncoderContext*)calloc(1, sizeof(H264EncoderContext));
    if (!ctx) return NULL;

    ctx->width = width;
    ctx->height = height;
    ctx->fps = fps;
    ctx->bitrate = bitrate;
    ctx->frame_count = 0;
    ctx->quality_mode = quality_mode;
    ctx->crf_value = crf_value;

    // Use ultrafast preset for real-time encoding
    if (x264_param_default_preset(&ctx->params, "ultrafast", "zerolatency") < 0) {
        free(ctx);
        return NULL;
    }

    // Configure encoder
    ctx->params.i_csp = X264_CSP_I420;
    ctx->params.i_width = width;
    ctx->params.i_height = height;
    ctx->params.i_fps_num = fps;
    ctx->params.i_fps_den = 1;
    ctx->params.i_threads = 0;  // Auto-detect
    ctx->params.b_vfr_input = 0;
    ctx->params.b_repeat_headers = 1;  // Include SPS/PPS with each keyframe
    ctx->params.b_annexb = 1;          // Annex B format (NAL start codes)

    // Rate control - choose based on mode
    if (quality_mode) {
        // Quality mode: CRF with VBV constraints
        ctx->params.rc.i_rc_method = X264_RC_CRF;
        ctx->params.rc.f_rf_constant = crf_value;
        ctx->params.rc.i_vbv_max_bitrate = bitrate;  // Cap max bitrate
        ctx->params.rc.i_vbv_buffer_size = bitrate;
    } else {
        // Performance mode: ABR (bandwidth efficient)
        ctx->params.rc.i_rc_method = X264_RC_ABR;
        ctx->params.rc.i_bitrate = bitrate;
        ctx->params.rc.i_vbv_buffer_size = bitrate;
        ctx->params.rc.i_vbv_max_bitrate = bitrate;
    }

    // Keyframe interval
    ctx->params.i_keyint_max = fps * 2;  // Keyframe every 2 seconds
    ctx->params.i_keyint_min = fps;

    // Apply profile (baseline for maximum compatibility)
    if (x264_param_apply_profile(&ctx->params, "baseline") < 0) {
        free(ctx);
        return NULL;
    }

    // Open encoder
    ctx->encoder = x264_encoder_open(&ctx->params);
    if (!ctx->encoder) {
        free(ctx);
        return NULL;
    }

    // Allocate picture
    if (x264_picture_alloc(&ctx->pic_in, X264_CSP_I420, width, height) < 0) {
        x264_encoder_close(ctx->encoder);
        free(ctx);
        return NULL;
    }

    ctx->initialized = 1;
    return ctx;
}

// Legacy function - creates encoder in performance mode (ABR)
H264EncoderContext* create_h264_encoder(int width, int height, int fps, int bitrate) {
    return create_h264_encoder_with_mode(width, height, fps, bitrate, 0, 0);
}

void destroy_h264_encoder(H264EncoderContext* ctx) {
    if (!ctx) return;
    if (ctx->initialized) {
        x264_picture_clean(&ctx->pic_in);
        if (ctx->encoder) {
            x264_encoder_close(ctx->encoder);
        }
    }
    free(ctx);
}

// Encode a frame from BGRA data. Returns pointer to encoded data and sets size.
const uint8_t* encode_h264_frame(H264EncoderContext* ctx, const uint8_t* bgra_data, int bgra_stride, int* out_size, int force_keyframe) {
    if (!ctx || !ctx->initialized || !bgra_data || !ctx->encoder) {
        *out_size = 0;
        return NULL;
    }

    // Convert BGRA to I420 directly
    int width = ctx->width;
    int height = ctx->height;
    uint8_t* y_plane = ctx->pic_in.img.plane[0];
    uint8_t* u_plane = ctx->pic_in.img.plane[1];
    uint8_t* v_plane = ctx->pic_in.img.plane[2];
    int y_stride = ctx->pic_in.img.i_stride[0];
    int uv_stride = ctx->pic_in.img.i_stride[1];

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

    // Set frame properties
    ctx->pic_in.i_pts = ctx->frame_count;
    if (force_keyframe) {
        ctx->pic_in.i_type = X264_TYPE_IDR;
    } else {
        ctx->pic_in.i_type = X264_TYPE_AUTO;
    }

    // Encode
    x264_nal_t* nals;
    int num_nals;
    int frame_size = x264_encoder_encode(ctx->encoder, &nals, &num_nals, &ctx->pic_in, &ctx->pic_out);

    if (frame_size < 0) {
        *out_size = 0;
        return NULL;
    }

    ctx->frame_count++;

    if (frame_size == 0 || num_nals == 0) {
        *out_size = 0;
        return NULL;
    }

    *out_size = frame_size;
    return nals[0].p_payload;
}

int h264_encoder_width(H264EncoderContext* ctx) {
    return ctx ? ctx->width : 0;
}

int h264_encoder_height(H264EncoderContext* ctx) {
    return ctx ? ctx->height : 0;
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

// H264Encoder encodes RGBA frames to H.264 using x264 (software)
type H264Encoder struct {
	ctx           *C.H264EncoderContext
	width         int
	height        int
	fps           int
	bitrate       int
	frameCount    int
	mu            sync.Mutex
	initialized   bool
	needsRecreate bool    // Flag to recreate encoder on next frame (for bitrate/mode changes)
	qualityMode   bool    // false = performance (ABR), true = quality (CRF)
	crfValue      float32 // CRF value for quality mode (0-51, lower = better)
}

// NewH264Encoder creates a new H.264 software encoder using x264
func NewH264Encoder(config EncoderConfig) *H264Encoder {
	return &H264Encoder{
		width:   config.Width,
		height:  config.Height,
		fps:     config.FPS,
		bitrate: config.Bitrate,
	}
}

// Start initializes the encoder
func (e *H264Encoder) Start() error {
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
func (e *H264Encoder) initWithDimensions(width, height int) error {
	// Ensure even dimensions
	if width%2 != 0 {
		width--
	}
	if height%2 != 0 {
		height--
	}

	e.width = width
	e.height = height

	qualityMode := 0
	if e.qualityMode {
		qualityMode = 1
	}

	ctx := C.create_h264_encoder_with_mode(
		C.int(width), C.int(height), C.int(e.fps), C.int(e.bitrate),
		C.int(qualityMode), C.float(e.crfValue),
	)
	if ctx == nil {
		return fmt.Errorf("failed to create H.264 encoder")
	}

	e.ctx = ctx
	return nil
}

// Stop stops the encoder
func (e *H264Encoder) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.ctx != nil {
		C.destroy_h264_encoder(e.ctx)
		e.ctx = nil
	}
	e.initialized = false
}

// EncodeFrame encodes an RGBA image to H.264
// Deprecated: Use EncodeBGRAFrame for better performance
func (e *H264Encoder) EncodeFrame(img *image.RGBA) ([]byte, error) {
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

// EncodeBGRAFrame encodes raw BGRA data to H.264 (faster, no color conversion)
func (e *H264Encoder) EncodeBGRAFrame(frame *BGRAFrame) ([]byte, error) {
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
			C.destroy_h264_encoder(e.ctx)
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
	dataPtr := C.encode_h264_frame(
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
func (e *H264Encoder) GetSampleDuration() time.Duration {
	return time.Second / time.Duration(e.fps)
}

// GetCodecType returns the codec type
func (e *H264Encoder) GetCodecType() CodecType {
	return CodecH264
}

// IsHardwareAccelerated returns false for x264 (software encoding)
func (e *H264Encoder) IsHardwareAccelerated() bool {
	return false
}

// SetBitrate changes the target bitrate (kbps)
// The encoder will be recreated on the next frame encode
func (e *H264Encoder) SetBitrate(bitrate int) error {
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
// Quality mode (enabled=true): Uses CRF rate control for consistent visual quality
// Performance mode (enabled=false): Uses ABR rate control for bandwidth efficiency
// The encoder will be recreated on the next frame encode
func (e *H264Encoder) SetQualityMode(enabled bool, bitrate int) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Get CRF value from quality params
	_, crf, _ := QualityModeParams(bitrate)

	if e.qualityMode == enabled && e.crfValue == crf {
		return nil
	}

	e.qualityMode = enabled
	e.crfValue = crf
	e.needsRecreate = true
	return nil
}
