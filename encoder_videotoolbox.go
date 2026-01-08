package main

/*
#cgo CFLAGS: -x objective-c -fmodules
#cgo LDFLAGS: -framework VideoToolbox -framework CoreMedia -framework CoreFoundation -framework CoreVideo

#include <VideoToolbox/VideoToolbox.h>
#include <CoreMedia/CoreMedia.h>
#include <CoreFoundation/CoreFoundation.h>
#include <CoreVideo/CoreVideo.h>
#include <stdlib.h>
#include <string.h>
#include <pthread.h>

typedef struct {
    VTCompressionSessionRef session;
    int width;
    int height;
    int fps;
    int bitrate;
    int frame_count;
    int initialized;
    int quality_mode;    // 0 = performance (bitrate), 1 = quality (quality-based)
    float quality_value; // Quality value when in quality mode (0.0-1.0, higher = better)

    // Output buffer
    uint8_t* output_buffer;
    int output_size;
    int output_capacity;
    pthread_mutex_t output_mutex;
    int has_output;

    // CVPixelBuffer pool for efficient buffer reuse
    CVPixelBufferPoolRef pixel_buffer_pool;
} VideoToolboxContext;

// Check if VideoToolbox H.264 encoding is available
int is_videotoolbox_available() {
    // VideoToolbox is available on macOS 10.8+ and iOS 8+
    // For H.264 hardware encoding, we need a compatible GPU
    return 1;  // Assume available on modern macOS
}

// Callback for compressed frame output
void vtb_output_callback(void* outputCallbackRefCon,
                         void* sourceFrameRefCon,
                         OSStatus status,
                         VTEncodeInfoFlags infoFlags,
                         CMSampleBufferRef sampleBuffer) {
    VideoToolboxContext* ctx = (VideoToolboxContext*)outputCallbackRefCon;
    if (status != noErr || !sampleBuffer) {
        return;
    }

    // Check if frame was dropped
    if (infoFlags & kVTEncodeInfo_FrameDropped) {
        return;
    }

    // Get the data buffer
    CMBlockBufferRef blockBuffer = CMSampleBufferGetDataBuffer(sampleBuffer);
    if (!blockBuffer) {
        return;
    }

    size_t totalLength = 0;
    size_t dataLength = 0;
    char* dataPointer = NULL;

    OSStatus blockStatus = CMBlockBufferGetDataPointer(blockBuffer, 0, &dataLength, &totalLength, &dataPointer);
    if (blockStatus != kCMBlockBufferNoErr || !dataPointer) {
        return;
    }

    // Check if this is a keyframe and get SPS/PPS
    int isKeyframe = 0;
    CFArrayRef attachments = CMSampleBufferGetSampleAttachmentsArray(sampleBuffer, false);
    if (attachments && CFArrayGetCount(attachments) > 0) {
        CFDictionaryRef attachment = CFArrayGetValueAtIndex(attachments, 0);
        CFBooleanRef notSync = CFDictionaryGetValue(attachment, kCMSampleAttachmentKey_NotSync);
        isKeyframe = (notSync == NULL || !CFBooleanGetValue(notSync));
    }

    pthread_mutex_lock(&ctx->output_mutex);

    // Calculate required size
    size_t requiredSize = totalLength;
    uint8_t* spsData = NULL;
    size_t spsSize = 0;
    uint8_t* ppsData = NULL;
    size_t ppsSize = 0;

    // For keyframes, we need to prepend SPS/PPS
    if (isKeyframe) {
        CMFormatDescriptionRef format = CMSampleBufferGetFormatDescription(sampleBuffer);
        if (format) {
            size_t paramCount = 0;
            CMVideoFormatDescriptionGetH264ParameterSetAtIndex(format, 0, NULL, NULL, &paramCount, NULL);

            for (size_t i = 0; i < paramCount; i++) {
                const uint8_t* paramData = NULL;
                size_t paramSize = 0;
                CMVideoFormatDescriptionGetH264ParameterSetAtIndex(format, i, &paramData, &paramSize, NULL, NULL);

                if (i == 0) {
                    spsData = (uint8_t*)paramData;
                    spsSize = paramSize;
                    requiredSize += 4 + spsSize;  // Start code + SPS
                } else if (i == 1) {
                    ppsData = (uint8_t*)paramData;
                    ppsSize = paramSize;
                    requiredSize += 4 + ppsSize;  // Start code + PPS
                }
            }
        }
    }

    // Ensure buffer capacity
    if (requiredSize > ctx->output_capacity) {
        ctx->output_capacity = requiredSize * 2;
        ctx->output_buffer = (uint8_t*)realloc(ctx->output_buffer, ctx->output_capacity);
    }

    uint8_t* writePtr = ctx->output_buffer;

    // Write SPS/PPS for keyframes
    if (isKeyframe && spsData && ppsData) {
        // SPS with Annex B start code
        writePtr[0] = 0x00;
        writePtr[1] = 0x00;
        writePtr[2] = 0x00;
        writePtr[3] = 0x01;
        writePtr += 4;
        memcpy(writePtr, spsData, spsSize);
        writePtr += spsSize;

        // PPS with Annex B start code
        writePtr[0] = 0x00;
        writePtr[1] = 0x00;
        writePtr[2] = 0x00;
        writePtr[3] = 0x01;
        writePtr += 4;
        memcpy(writePtr, ppsData, ppsSize);
        writePtr += ppsSize;
    }

    // Convert AVCC NAL units to Annex B format
    size_t offset = 0;
    while (offset < totalLength) {
        // Read NAL unit length (4 bytes, big-endian)
        uint32_t nalLength = 0;
        memcpy(&nalLength, dataPointer + offset, 4);
        nalLength = CFSwapInt32BigToHost(nalLength);
        offset += 4;

        if (nalLength == 0 || offset + nalLength > totalLength) {
            break;
        }

        // Write Annex B start code
        writePtr[0] = 0x00;
        writePtr[1] = 0x00;
        writePtr[2] = 0x00;
        writePtr[3] = 0x01;
        writePtr += 4;

        // Write NAL unit data
        memcpy(writePtr, dataPointer + offset, nalLength);
        writePtr += nalLength;
        offset += nalLength;
    }

    ctx->output_size = (int)(writePtr - ctx->output_buffer);
    ctx->has_output = 1;

    pthread_mutex_unlock(&ctx->output_mutex);
}

VideoToolboxContext* create_videotoolbox_encoder_with_mode(int width, int height, int fps, int bitrate, int quality_mode, float quality_value) {
    VideoToolboxContext* ctx = (VideoToolboxContext*)calloc(1, sizeof(VideoToolboxContext));
    if (!ctx) return NULL;

    ctx->width = width;
    ctx->height = height;
    ctx->fps = fps;
    ctx->bitrate = bitrate;
    ctx->frame_count = 0;
    ctx->quality_mode = quality_mode;
    ctx->quality_value = quality_value;
    ctx->output_capacity = width * height;  // Initial estimate
    ctx->output_buffer = (uint8_t*)malloc(ctx->output_capacity);
    pthread_mutex_init(&ctx->output_mutex, NULL);

    // Create compression session
    CFMutableDictionaryRef encoderSpec = CFDictionaryCreateMutable(
        kCFAllocatorDefault, 0,
        &kCFTypeDictionaryKeyCallBacks,
        &kCFTypeDictionaryValueCallBacks
    );

    // Prefer hardware encoding
    CFDictionarySetValue(encoderSpec,
        kVTVideoEncoderSpecification_EnableHardwareAcceleratedVideoEncoder,
        kCFBooleanTrue);

    // Source image attributes
    CFMutableDictionaryRef sourceAttrs = CFDictionaryCreateMutable(
        kCFAllocatorDefault, 0,
        &kCFTypeDictionaryKeyCallBacks,
        &kCFTypeDictionaryValueCallBacks
    );

    int pixelFormat = kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange;  // NV12
    CFNumberRef pixelFormatNum = CFNumberCreate(kCFAllocatorDefault, kCFNumberIntType, &pixelFormat);
    CFDictionarySetValue(sourceAttrs, kCVPixelBufferPixelFormatTypeKey, pixelFormatNum);
    CFRelease(pixelFormatNum);

    CFNumberRef widthNum = CFNumberCreate(kCFAllocatorDefault, kCFNumberIntType, &width);
    CFNumberRef heightNum = CFNumberCreate(kCFAllocatorDefault, kCFNumberIntType, &height);
    CFDictionarySetValue(sourceAttrs, kCVPixelBufferWidthKey, widthNum);
    CFDictionarySetValue(sourceAttrs, kCVPixelBufferHeightKey, heightNum);
    CFRelease(widthNum);
    CFRelease(heightNum);

    OSStatus status = VTCompressionSessionCreate(
        kCFAllocatorDefault,
        width, height,
        kCMVideoCodecType_H264,
        encoderSpec,
        sourceAttrs,
        kCFAllocatorDefault,
        vtb_output_callback,
        ctx,
        &ctx->session
    );

    CFRelease(encoderSpec);
    CFRelease(sourceAttrs);

    if (status != noErr || !ctx->session) {
        free(ctx->output_buffer);
        pthread_mutex_destroy(&ctx->output_mutex);
        free(ctx);
        return NULL;
    }

    // Configure session properties
    VTSessionSetProperty(ctx->session, kVTCompressionPropertyKey_RealTime, kCFBooleanTrue);
    VTSessionSetProperty(ctx->session, kVTCompressionPropertyKey_ProfileLevel, kVTProfileLevel_H264_Baseline_AutoLevel);
    VTSessionSetProperty(ctx->session, kVTCompressionPropertyKey_AllowFrameReordering, kCFBooleanFalse);

    if (quality_mode) {
        // Quality mode: Use quality-based encoding with bitrate as soft ceiling
        CFNumberRef qualityNum = CFNumberCreate(kCFAllocatorDefault, kCFNumberFloatType, &quality_value);
        VTSessionSetProperty(ctx->session, kVTCompressionPropertyKey_Quality, qualityNum);
        CFRelease(qualityNum);

        // Still set average bitrate as a soft target/ceiling
        int avgBitrate = bitrate * 1000;  // Convert kbps to bps
        CFNumberRef bitrateNum = CFNumberCreate(kCFAllocatorDefault, kCFNumberIntType, &avgBitrate);
        VTSessionSetProperty(ctx->session, kVTCompressionPropertyKey_AverageBitRate, bitrateNum);
        CFRelease(bitrateNum);

        // No strict data rate limits in quality mode - let encoder use what it needs
    } else {
        // Performance mode: Use strict bitrate control
        int avgBitrate = bitrate * 1000;  // Convert kbps to bps
        CFNumberRef bitrateNum = CFNumberCreate(kCFAllocatorDefault, kCFNumberIntType, &avgBitrate);
        VTSessionSetProperty(ctx->session, kVTCompressionPropertyKey_AverageBitRate, bitrateNum);
        CFRelease(bitrateNum);

        // Data rate limits for strict bandwidth control
        int bytesPerSecond = avgBitrate / 8;
        int limitDuration = 1;
        CFNumberRef bytesNum = CFNumberCreate(kCFAllocatorDefault, kCFNumberIntType, &bytesPerSecond);
        CFNumberRef durationNum = CFNumberCreate(kCFAllocatorDefault, kCFNumberIntType, &limitDuration);
        CFMutableArrayRef dataRateLimits = CFArrayCreateMutable(kCFAllocatorDefault, 2, &kCFTypeArrayCallBacks);
        CFArrayAppendValue(dataRateLimits, bytesNum);
        CFArrayAppendValue(dataRateLimits, durationNum);
        VTSessionSetProperty(ctx->session, kVTCompressionPropertyKey_DataRateLimits, dataRateLimits);
        CFRelease(bytesNum);
        CFRelease(durationNum);
        CFRelease(dataRateLimits);
    }

    // Keyframe interval
    int keyframeInterval = fps * 2;  // Every 2 seconds
    CFNumberRef keyframeNum = CFNumberCreate(kCFAllocatorDefault, kCFNumberIntType, &keyframeInterval);
    VTSessionSetProperty(ctx->session, kVTCompressionPropertyKey_MaxKeyFrameInterval, keyframeNum);
    CFRelease(keyframeNum);

    // Frame rate
    float expectedFPS = (float)fps;
    CFNumberRef fpsNum = CFNumberCreate(kCFAllocatorDefault, kCFNumberFloatType, &expectedFPS);
    VTSessionSetProperty(ctx->session, kVTCompressionPropertyKey_ExpectedFrameRate, fpsNum);
    CFRelease(fpsNum);

    // Prepare to encode
    status = VTCompressionSessionPrepareToEncodeFrames(ctx->session);
    if (status != noErr) {
        VTCompressionSessionInvalidate(ctx->session);
        CFRelease(ctx->session);
        free(ctx->output_buffer);
        pthread_mutex_destroy(&ctx->output_mutex);
        free(ctx);
        return NULL;
    }

    // Create CVPixelBufferPool for efficient buffer reuse
    CFMutableDictionaryRef poolAttrs = CFDictionaryCreateMutable(
        kCFAllocatorDefault, 0,
        &kCFTypeDictionaryKeyCallBacks,
        &kCFTypeDictionaryValueCallBacks
    );

    // Pool pixel buffer attributes
    CFMutableDictionaryRef pbAttrs = CFDictionaryCreateMutable(
        kCFAllocatorDefault, 0,
        &kCFTypeDictionaryKeyCallBacks,
        &kCFTypeDictionaryValueCallBacks
    );

    int poolPixelFormat = kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange;
    CFNumberRef poolPixelFormatNum = CFNumberCreate(kCFAllocatorDefault, kCFNumberIntType, &poolPixelFormat);
    CFDictionarySetValue(pbAttrs, kCVPixelBufferPixelFormatTypeKey, poolPixelFormatNum);
    CFRelease(poolPixelFormatNum);

    CFNumberRef poolWidthNum = CFNumberCreate(kCFAllocatorDefault, kCFNumberIntType, &width);
    CFNumberRef poolHeightNum = CFNumberCreate(kCFAllocatorDefault, kCFNumberIntType, &height);
    CFDictionarySetValue(pbAttrs, kCVPixelBufferWidthKey, poolWidthNum);
    CFDictionarySetValue(pbAttrs, kCVPixelBufferHeightKey, poolHeightNum);
    CFRelease(poolWidthNum);
    CFRelease(poolHeightNum);

    // Allow IOSurface backing for better memory efficiency
    CFDictionarySetValue(pbAttrs, kCVPixelBufferIOSurfacePropertiesKey,
        (__bridge CFDictionaryRef)@{});

    CVReturn poolStatus = CVPixelBufferPoolCreate(
        kCFAllocatorDefault,
        poolAttrs,
        pbAttrs,
        &ctx->pixel_buffer_pool
    );

    CFRelease(poolAttrs);
    CFRelease(pbAttrs);

    if (poolStatus != kCVReturnSuccess) {
        // Pool creation failed - encoder will fall back to per-frame allocation
        ctx->pixel_buffer_pool = NULL;
    }

    ctx->initialized = 1;
    return ctx;
}

// Legacy function - creates encoder in performance mode
VideoToolboxContext* create_videotoolbox_encoder(int width, int height, int fps, int bitrate) {
    return create_videotoolbox_encoder_with_mode(width, height, fps, bitrate, 0, 0.0);
}

void destroy_videotoolbox_encoder(VideoToolboxContext* ctx) {
    if (!ctx) return;
    if (ctx->initialized) {
        if (ctx->session) {
            VTCompressionSessionCompleteFrames(ctx->session, kCMTimeInvalid);
            VTCompressionSessionInvalidate(ctx->session);
            CFRelease(ctx->session);
        }
        if (ctx->pixel_buffer_pool) {
            CVPixelBufferPoolRelease(ctx->pixel_buffer_pool);
        }
        if (ctx->output_buffer) {
            free(ctx->output_buffer);
        }
        pthread_mutex_destroy(&ctx->output_mutex);
    }
    free(ctx);
}

// Encode a frame from BGRA data. Returns pointer to encoded data and sets size.
const uint8_t* encode_videotoolbox_frame(VideoToolboxContext* ctx, const uint8_t* bgra_data, int bgra_stride, int* out_size, int force_keyframe) {
    if (!ctx || !ctx->initialized || !bgra_data || !ctx->session) {
        *out_size = 0;
        return NULL;
    }

    int width = ctx->width;
    int height = ctx->height;

    // Get pixel buffer from pool (or create if pool unavailable)
    CVPixelBufferRef pixelBuffer = NULL;
    CVReturn cvStatus;

    if (ctx->pixel_buffer_pool) {
        // Use pool for efficient buffer reuse
        cvStatus = CVPixelBufferPoolCreatePixelBuffer(
            kCFAllocatorDefault,
            ctx->pixel_buffer_pool,
            &pixelBuffer
        );
    } else {
        // Fallback: create buffer directly
        NSDictionary* options = @{
            (id)kCVPixelBufferCGImageCompatibilityKey: @NO,
            (id)kCVPixelBufferCGBitmapContextCompatibilityKey: @NO
        };
        cvStatus = CVPixelBufferCreate(
            kCFAllocatorDefault,
            width, height,
            kCVPixelFormatType_420YpCbCr8BiPlanarVideoRange,
            (__bridge CFDictionaryRef)options,
            &pixelBuffer
        );
    }

    if (cvStatus != kCVReturnSuccess || !pixelBuffer) {
        *out_size = 0;
        return NULL;
    }

    // Lock the pixel buffer
    CVPixelBufferLockBaseAddress(pixelBuffer, 0);

    // Get plane addresses for NV12
    uint8_t* y_plane = (uint8_t*)CVPixelBufferGetBaseAddressOfPlane(pixelBuffer, 0);
    uint8_t* uv_plane = (uint8_t*)CVPixelBufferGetBaseAddressOfPlane(pixelBuffer, 1);
    size_t y_stride = CVPixelBufferGetBytesPerRowOfPlane(pixelBuffer, 0);
    size_t uv_stride = CVPixelBufferGetBytesPerRowOfPlane(pixelBuffer, 1);

    // Convert BGRA to NV12 directly (Y plane + interleaved UV plane)
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

            // NV12: UV interleaved, subsampled 2x2
            if (row % 2 == 0 && col % 2 == 0) {
                int uv_row = row / 2;
                int uv_col = col;
                uv_plane[uv_row * uv_stride + uv_col] = (uint8_t)u;
                uv_plane[uv_row * uv_stride + uv_col + 1] = (uint8_t)v;
            }
        }
    }

    CVPixelBufferUnlockBaseAddress(pixelBuffer, 0);

    // Create presentation timestamp
    CMTime pts = CMTimeMake(ctx->frame_count, ctx->fps);

    // Frame properties
    CFMutableDictionaryRef frameProps = NULL;
    if (force_keyframe) {
        frameProps = CFDictionaryCreateMutable(
            kCFAllocatorDefault, 1,
            &kCFTypeDictionaryKeyCallBacks,
            &kCFTypeDictionaryValueCallBacks
        );
        CFDictionarySetValue(frameProps, kVTEncodeFrameOptionKey_ForceKeyFrame, kCFBooleanTrue);
    }

    // Reset output flag
    pthread_mutex_lock(&ctx->output_mutex);
    ctx->has_output = 0;
    pthread_mutex_unlock(&ctx->output_mutex);

    // Encode frame
    OSStatus status = VTCompressionSessionEncodeFrame(
        ctx->session,
        pixelBuffer,
        pts,
        kCMTimeInvalid,
        frameProps,
        NULL,
        NULL
    );

    if (frameProps) {
        CFRelease(frameProps);
    }
    CVPixelBufferRelease(pixelBuffer);

    if (status != noErr) {
        *out_size = 0;
        return NULL;
    }

    // Force synchronous output
    VTCompressionSessionCompleteFrames(ctx->session, pts);

    ctx->frame_count++;

    // Return output
    pthread_mutex_lock(&ctx->output_mutex);
    if (ctx->has_output) {
        *out_size = ctx->output_size;
        pthread_mutex_unlock(&ctx->output_mutex);
        return ctx->output_buffer;
    }
    pthread_mutex_unlock(&ctx->output_mutex);

    *out_size = 0;
    return NULL;
}

// Check if hardware encoding is actually being used
int is_videotoolbox_hardware(VideoToolboxContext* ctx) {
    if (!ctx || !ctx->session) return 0;

    CFBooleanRef usingHardware = NULL;
    OSStatus status = VTSessionCopyProperty(
        ctx->session,
        kVTCompressionPropertyKey_UsingHardwareAcceleratedVideoEncoder,
        kCFAllocatorDefault,
        &usingHardware
    );

    if (status == noErr && usingHardware) {
        int result = CFBooleanGetValue(usingHardware);
        CFRelease(usingHardware);
        return result;
    }
    return 0;
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

// VideoToolboxEncoder encodes RGBA frames to H.264 using Apple VideoToolbox (hardware)
type VideoToolboxEncoder struct {
	ctx                 *C.VideoToolboxContext
	width               int
	height              int
	fps                 int
	bitrate             int
	frameCount          int
	mu                  sync.Mutex
	initialized         bool
	hardwareAccelerated bool
	needsRecreate       bool    // Flag to recreate encoder on next frame (for bitrate/mode changes)
	qualityMode         bool    // false = performance (bitrate), true = quality (quality-based)
	qualityValue        float32 // Quality value for quality mode (0.0-1.0, higher = better)
}

func init() {
	// Override the IsVideoToolboxAvailable function
	IsVideoToolboxAvailable = func() bool {
		return C.is_videotoolbox_available() != 0
	}
}

// NewVideoToolboxEncoder creates a new H.264 hardware encoder using VideoToolbox
func NewVideoToolboxEncoder(config EncoderConfig) *VideoToolboxEncoder {
	return &VideoToolboxEncoder{
		width:   config.Width,
		height:  config.Height,
		fps:     config.FPS,
		bitrate: config.Bitrate,
	}
}

// Start initializes the encoder
func (e *VideoToolboxEncoder) Start() error {
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
func (e *VideoToolboxEncoder) initWithDimensions(width, height int) error {
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

	ctx := C.create_videotoolbox_encoder_with_mode(
		C.int(width), C.int(height), C.int(e.fps), C.int(e.bitrate),
		C.int(qualityMode), C.float(e.qualityValue),
	)
	if ctx == nil {
		return fmt.Errorf("failed to create VideoToolbox encoder")
	}

	e.ctx = ctx
	e.hardwareAccelerated = C.is_videotoolbox_hardware(ctx) != 0
	return nil
}

// Stop stops the encoder
func (e *VideoToolboxEncoder) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.ctx != nil {
		C.destroy_videotoolbox_encoder(e.ctx)
		e.ctx = nil
	}
	e.initialized = false
}

// EncodeFrame encodes an RGBA image to H.264
// Deprecated: Use EncodeBGRAFrame for better performance
func (e *VideoToolboxEncoder) EncodeFrame(img *image.RGBA) ([]byte, error) {
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
func (e *VideoToolboxEncoder) EncodeBGRAFrame(frame *BGRAFrame) ([]byte, error) {
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
			C.destroy_videotoolbox_encoder(e.ctx)
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
	dataPtr := C.encode_videotoolbox_frame(
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
func (e *VideoToolboxEncoder) GetSampleDuration() time.Duration {
	return time.Second / time.Duration(e.fps)
}

// GetCodecType returns the codec type
func (e *VideoToolboxEncoder) GetCodecType() CodecType {
	return CodecH264
}

// IsHardwareAccelerated returns true if using hardware encoding
func (e *VideoToolboxEncoder) IsHardwareAccelerated() bool {
	return e.hardwareAccelerated
}

// SetBitrate changes the target bitrate (kbps)
// The encoder will be recreated on the next frame encode
func (e *VideoToolboxEncoder) SetBitrate(bitrate int) error {
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
// Quality mode (enabled=true): Uses quality-based encoding for consistent visual quality
// Performance mode (enabled=false): Uses bitrate-based encoding for bandwidth efficiency
// The encoder will be recreated on the next frame encode
func (e *VideoToolboxEncoder) SetQualityMode(enabled bool, bitrate int) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Get quality value from quality params
	_, _, vtQuality := QualityModeParams(bitrate)

	if e.qualityMode == enabled && e.qualityValue == vtQuality {
		return nil
	}

	e.qualityMode = enabled
	e.qualityValue = vtQuality
	e.needsRecreate = true
	return nil
}
