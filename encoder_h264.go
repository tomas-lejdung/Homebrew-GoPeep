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

    // Convert BGRA to I420 with SIMD-friendly batching and 2x2 UV averaging
    int width = ctx->width;
    int height = ctx->height;
    uint8_t* y_plane = ctx->pic_in.img.plane[0];
    uint8_t* u_plane = ctx->pic_in.img.plane[1];
    uint8_t* v_plane = ctx->pic_in.img.plane[2];
    int y_stride = ctx->pic_in.img.i_stride[0];
    int uv_stride = ctx->pic_in.img.i_stride[1];

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
		mode := "ABR"
		if e.qualityMode {
			mode = fmt.Sprintf("CRF(%.1f)", e.crfValue)
		}
		return fmt.Errorf("failed to create H.264 (x264) encoder: %dx%d @ %dfps, %dkbps, mode=%s",
			width, height, e.fps, e.bitrate, mode)
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
		// Reset frame count to force keyframe on next frame after dimension change
		e.frameCount = 0
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
		return nil, fmt.Errorf("H.264 (x264) encoding failed: frame=%d, dimensions=%dx%d, keyframe=%v",
			e.frameCount, e.width, e.height, forceKeyframe == 1)
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
