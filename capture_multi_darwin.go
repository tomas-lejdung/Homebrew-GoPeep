package main

/*
#cgo CFLAGS: -x objective-c -fmodules
#cgo LDFLAGS: -framework CoreGraphics -framework CoreFoundation -framework AppKit -framework ScreenCaptureKit -framework CoreMedia -framework CoreVideo

#include <CoreGraphics/CoreGraphics.h>
#include <CoreFoundation/CoreFoundation.h>
#include <AppKit/AppKit.h>
#include <ScreenCaptureKit/ScreenCaptureKit.h>
#include <CoreMedia/CoreMedia.h>
#include <CoreVideo/CoreVideo.h>
#include <stdlib.h>
#include <string.h>
#include <dispatch/dispatch.h>
#include <pthread.h>

// Maximum number of concurrent capture instances
#define MAX_CAPTURE_INSTANCES 4

typedef struct {
    uint8_t* data;
    int width;
    int height;
    int bytes_per_row;
} MCFrameData;

// Per-instance capture state
typedef struct {
    SCStream* stream;
    SCStreamConfiguration* config;
    dispatch_queue_t queue;
    MCFrameData latest_frame;
    dispatch_semaphore_t frame_semaphore;
    pthread_mutex_t frame_mutex;
    int active;
    uint32_t window_id;
    id output_delegate;  // Store delegate reference
} CaptureInstance;

// Global array of capture instances
static CaptureInstance g_instances[MAX_CAPTURE_INSTANCES];
static pthread_mutex_t g_instances_mutex = PTHREAD_MUTEX_INITIALIZER;
static int g_mc_initialized = 0;

// Stream output delegate for multi-capture
@interface MCStreamOutputDelegate : NSObject <SCStreamOutput>
@property (nonatomic, assign) int instanceIndex;
@end

@implementation MCStreamOutputDelegate

- (void)stream:(SCStream *)stream didOutputSampleBuffer:(CMSampleBufferRef)sampleBuffer ofType:(SCStreamOutputType)type {
    if (type != SCStreamOutputTypeScreen) return;
    if (self.instanceIndex < 0 || self.instanceIndex >= MAX_CAPTURE_INSTANCES) return;

    CaptureInstance* inst = &g_instances[self.instanceIndex];
    if (!inst->active) return;

    CVImageBufferRef imageBuffer = CMSampleBufferGetImageBuffer(sampleBuffer);
    if (imageBuffer == NULL) return;

    CVPixelBufferLockBaseAddress(imageBuffer, kCVPixelBufferLock_ReadOnly);

    size_t width = CVPixelBufferGetWidth(imageBuffer);
    size_t height = CVPixelBufferGetHeight(imageBuffer);
    size_t bytesPerRow = CVPixelBufferGetBytesPerRow(imageBuffer);
    void* baseAddress = CVPixelBufferGetBaseAddress(imageBuffer);

    size_t dataSize = height * bytesPerRow;
    uint8_t* newData = (uint8_t*)malloc(dataSize);
    if (newData != NULL) {
        memcpy(newData, baseAddress, dataSize);

        pthread_mutex_lock(&inst->frame_mutex);

        uint8_t* oldData = inst->latest_frame.data;
        inst->latest_frame.data = newData;
        inst->latest_frame.width = (int)width;
        inst->latest_frame.height = (int)height;
        inst->latest_frame.bytes_per_row = (int)bytesPerRow;

        pthread_mutex_unlock(&inst->frame_mutex);

        if (oldData != NULL) {
            free(oldData);
        }

        if (inst->frame_semaphore != nil) {
            dispatch_semaphore_signal(inst->frame_semaphore);
        }
    }

    CVPixelBufferUnlockBaseAddress(imageBuffer, kCVPixelBufferLock_ReadOnly);
}

@end

// Initialize multi-capture system
void mc_init() {
    if (g_mc_initialized) return;

    pthread_mutex_lock(&g_instances_mutex);
    for (int i = 0; i < MAX_CAPTURE_INSTANCES; i++) {
        memset(&g_instances[i], 0, sizeof(CaptureInstance));
        pthread_mutex_init(&g_instances[i].frame_mutex, NULL);
    }
    g_mc_initialized = 1;
    pthread_mutex_unlock(&g_instances_mutex);

    // Initialize NSApplication
    @autoreleasepool {
        [NSApplication sharedApplication];
    }
}

// Find a free capture slot, returns -1 if none available
int mc_find_free_slot() {
    pthread_mutex_lock(&g_instances_mutex);
    for (int i = 0; i < MAX_CAPTURE_INSTANCES; i++) {
        if (!g_instances[i].active) {
            pthread_mutex_unlock(&g_instances_mutex);
            return i;
        }
    }
    pthread_mutex_unlock(&g_instances_mutex);
    return -1;
}

// Start capturing a window, returns instance index or -1 on error
int mc_start_window_capture(uint32_t window_id, int target_width, int target_height, int fps) {
    mc_init();

    int slot = mc_find_free_slot();
    if (slot < 0) {
        return -1; // No free slots
    }

    CaptureInstance* inst = &g_instances[slot];

    __block SCContentFilter* filter = nil;
    __block int configWidth = target_width;
    __block int configHeight = target_height;
    dispatch_semaphore_t findSemaphore = dispatch_semaphore_create(0);

    [SCShareableContent getShareableContentWithCompletionHandler:^(SCShareableContent* content, NSError* error) {
        if (error == nil && content != nil) {
            for (SCWindow* window in content.windows) {
                if (window.windowID == window_id) {
                    filter = [[SCContentFilter alloc] initWithDesktopIndependentWindow:window];
                    if (configWidth <= 0) configWidth = (int)window.frame.size.width;
                    if (configHeight <= 0) configHeight = (int)window.frame.size.height;
                    break;
                }
            }
        }
        dispatch_semaphore_signal(findSemaphore);
    }];

    dispatch_semaphore_wait(findSemaphore, dispatch_time(DISPATCH_TIME_NOW, 5 * NSEC_PER_SEC));

    if (filter == nil) {
        return -2; // Window not found
    }

    @autoreleasepool {
        inst->config = [[SCStreamConfiguration alloc] init];
        inst->config.width = configWidth;
        inst->config.height = configHeight;
        inst->config.minimumFrameInterval = CMTimeMake(1, fps > 0 ? fps : 30);
        inst->config.pixelFormat = kCVPixelFormatType_32BGRA;
        inst->config.showsCursor = YES;

        inst->stream = [[SCStream alloc] initWithFilter:filter configuration:inst->config delegate:nil];

        char queueName[64];
        snprintf(queueName, sizeof(queueName), "com.gopeep.capture.%d", slot);
        inst->queue = dispatch_queue_create(queueName, DISPATCH_QUEUE_SERIAL);

        MCStreamOutputDelegate* delegate = [[MCStreamOutputDelegate alloc] init];
        delegate.instanceIndex = slot;
        inst->output_delegate = delegate;
        inst->frame_semaphore = dispatch_semaphore_create(0);
        inst->window_id = window_id;

        NSError* addError = nil;
        [inst->stream addStreamOutput:delegate type:SCStreamOutputTypeScreen sampleHandlerQueue:inst->queue error:&addError];
        if (addError != nil) {
            NSLog(@"Failed to add stream output for instance %d: %@", slot, addError);
            return -3;
        }

        __block int startResult = 0;
        dispatch_semaphore_t startSemaphore = dispatch_semaphore_create(0);

        [inst->stream startCaptureWithCompletionHandler:^(NSError* error) {
            if (error != nil) {
                NSLog(@"Failed to start capture for instance %d: %@", slot, error);
                startResult = -4;
            }
            dispatch_semaphore_signal(startSemaphore);
        }];

        dispatch_semaphore_wait(startSemaphore, dispatch_time(DISPATCH_TIME_NOW, 5 * NSEC_PER_SEC));

        if (startResult == 0) {
            inst->active = 1;
            return slot;
        }

        return startResult;
    }
}

// Start capturing the display (fullscreen), returns instance index or -1 on error
// Uses window_id = 0 to indicate this is a display capture
int mc_start_display_capture(int target_width, int target_height, int fps) {
    mc_init();

    int slot = mc_find_free_slot();
    if (slot < 0) {
        return -1; // No free slots
    }

    CaptureInstance* inst = &g_instances[slot];

    __block SCContentFilter* filter = nil;
    __block int configWidth = target_width;
    __block int configHeight = target_height;
    dispatch_semaphore_t findSemaphore = dispatch_semaphore_create(0);

    [SCShareableContent getShareableContentWithCompletionHandler:^(SCShareableContent* content, NSError* error) {
        if (error == nil && content != nil && content.displays.count > 0) {
            SCDisplay* display = content.displays[0];
            filter = [[SCContentFilter alloc] initWithDisplay:display excludingWindows:@[]];
            if (configWidth <= 0) configWidth = (int)display.width;
            if (configHeight <= 0) configHeight = (int)display.height;
        }
        dispatch_semaphore_signal(findSemaphore);
    }];

    dispatch_semaphore_wait(findSemaphore, dispatch_time(DISPATCH_TIME_NOW, 5 * NSEC_PER_SEC));

    if (filter == nil) {
        return -2; // Display not found
    }

    @autoreleasepool {
        inst->config = [[SCStreamConfiguration alloc] init];
        // Use display dimensions for display capture
        inst->config.width = configWidth;
        inst->config.height = configHeight;
        inst->config.minimumFrameInterval = CMTimeMake(1, fps > 0 ? fps : 30);
        inst->config.pixelFormat = kCVPixelFormatType_32BGRA;
        inst->config.showsCursor = YES;

        inst->stream = [[SCStream alloc] initWithFilter:filter configuration:inst->config delegate:nil];

        char queueName[64];
        snprintf(queueName, sizeof(queueName), "com.gopeep.capture.display.%d", slot);
        inst->queue = dispatch_queue_create(queueName, DISPATCH_QUEUE_SERIAL);

        MCStreamOutputDelegate* delegate = [[MCStreamOutputDelegate alloc] init];
        delegate.instanceIndex = slot;
        inst->output_delegate = delegate;
        inst->frame_semaphore = dispatch_semaphore_create(0);
        inst->window_id = 0; // 0 indicates display capture

        NSError* addError = nil;
        [inst->stream addStreamOutput:delegate type:SCStreamOutputTypeScreen sampleHandlerQueue:inst->queue error:&addError];
        if (addError != nil) {
            NSLog(@"Failed to add stream output for display capture instance %d: %@", slot, addError);
            return -3;
        }

        __block int startResult = 0;
        dispatch_semaphore_t startSemaphore = dispatch_semaphore_create(0);

        [inst->stream startCaptureWithCompletionHandler:^(NSError* error) {
            if (error != nil) {
                NSLog(@"Failed to start display capture for instance %d: %@", slot, error);
                startResult = -4;
            }
            dispatch_semaphore_signal(startSemaphore);
        }];

        dispatch_semaphore_wait(startSemaphore, dispatch_time(DISPATCH_TIME_NOW, 5 * NSEC_PER_SEC));

        if (startResult == 0) {
            inst->active = 1;
            return slot;
        }

        return startResult;
    }
}

// Stop a capture instance
void mc_stop_capture(int slot) {
    if (slot < 0 || slot >= MAX_CAPTURE_INSTANCES) return;

    CaptureInstance* inst = &g_instances[slot];
    if (!inst->active) return;

    @autoreleasepool {
        if (inst->stream != nil) {
            dispatch_semaphore_t semaphore = dispatch_semaphore_create(0);
            [inst->stream stopCaptureWithCompletionHandler:^(NSError* error) {
                dispatch_semaphore_signal(semaphore);
            }];
            dispatch_semaphore_wait(semaphore, dispatch_time(DISPATCH_TIME_NOW, 2 * NSEC_PER_SEC));
            inst->stream = nil;
        }

        inst->config = nil;
        inst->output_delegate = nil;
    }

    pthread_mutex_lock(&inst->frame_mutex);
    if (inst->latest_frame.data != NULL) {
        free(inst->latest_frame.data);
        inst->latest_frame.data = NULL;
    }
    pthread_mutex_unlock(&inst->frame_mutex);

    inst->active = 0;
    inst->window_id = 0;
}

// Stop all capture instances
void mc_stop_all() {
    for (int i = 0; i < MAX_CAPTURE_INSTANCES; i++) {
        mc_stop_capture(i);
    }
}

// Update stream configuration with new dimensions (called when window resizes)
// This is non-blocking - the update happens asynchronously
int mc_update_stream_size(int slot, int new_width, int new_height) {
    if (slot < 0 || slot >= MAX_CAPTURE_INSTANCES) return -1;

    CaptureInstance* inst = &g_instances[slot];
    if (!inst->active || inst->stream == nil || inst->config == nil) return -1;

    @autoreleasepool {
        // Create new configuration with updated dimensions
        SCStreamConfiguration* newConfig = [[SCStreamConfiguration alloc] init];
        newConfig.width = new_width;
        newConfig.height = new_height;
        newConfig.minimumFrameInterval = inst->config.minimumFrameInterval;
        newConfig.pixelFormat = inst->config.pixelFormat;
        newConfig.showsCursor = inst->config.showsCursor;

        // Store the new config immediately (frames will use it once update completes)
        inst->config = newConfig;

        // Update the stream configuration asynchronously (non-blocking)
        [inst->stream updateConfiguration:newConfig completionHandler:^(NSError* error) {
            if (error != nil) {
                NSLog(@"Failed to update stream configuration: %@", error);
            }
        }];

        return 0;
    }
}

// Get current window dimensions using fast CGWindowList API
// Returns width in out_width, height in out_height. Returns 0 on success, -1 on error.
int mc_get_window_size(int slot, int* out_width, int* out_height) {
    if (slot < 0 || slot >= MAX_CAPTURE_INSTANCES) return -1;

    CaptureInstance* inst = &g_instances[slot];
    if (!inst->active || inst->window_id == 0) return -1;

    // Use CGWindowListCopyWindowInfo - much faster than SCShareableContent
    CFArrayRef windowList = CGWindowListCopyWindowInfo(
        kCGWindowListOptionIncludingWindow,
        inst->window_id
    );

    if (windowList == NULL || CFArrayGetCount(windowList) == 0) {
        if (windowList) CFRelease(windowList);
        return -1;
    }

    CFDictionaryRef windowInfo = (CFDictionaryRef)CFArrayGetValueAtIndex(windowList, 0);
    CFDictionaryRef boundsDict = (CFDictionaryRef)CFDictionaryGetValue(windowInfo, kCGWindowBounds);

    if (boundsDict == NULL) {
        CFRelease(windowList);
        return -1;
    }

    CGRect bounds;
    if (!CGRectMakeWithDictionaryRepresentation(boundsDict, &bounds)) {
        CFRelease(windowList);
        return -1;
    }

    *out_width = (int)bounds.size.width;
    *out_height = (int)bounds.size.height;

    CFRelease(windowList);
    return 0;
}

// Get current stream config dimensions
int mc_get_config_size(int slot, int* out_width, int* out_height) {
    if (slot < 0 || slot >= MAX_CAPTURE_INSTANCES) return -1;

    CaptureInstance* inst = &g_instances[slot];
    if (!inst->active || inst->config == nil) return -1;

    *out_width = (int)inst->config.width;
    *out_height = (int)inst->config.height;
    return 0;
}

// Get latest frame from an instance
MCFrameData mc_get_latest_frame(int slot, int timeout_ms) {
    MCFrameData result = {NULL, 0, 0, 0};

    if (slot < 0 || slot >= MAX_CAPTURE_INSTANCES) return result;

    CaptureInstance* inst = &g_instances[slot];
    if (!inst->active || inst->frame_semaphore == nil) return result;

    // Drain excess signals
    while (dispatch_semaphore_wait(inst->frame_semaphore, DISPATCH_TIME_NOW) == 0) {}

    // Wait for new frame
    dispatch_time_t timeout = dispatch_time(DISPATCH_TIME_NOW, timeout_ms * NSEC_PER_MSEC);
    if (dispatch_semaphore_wait(inst->frame_semaphore, timeout) != 0) {
        // Timeout - return last known frame if available
        pthread_mutex_lock(&inst->frame_mutex);
        if (inst->latest_frame.data != NULL) {
            size_t dataSize = inst->latest_frame.height * inst->latest_frame.bytes_per_row;
            result.data = (uint8_t*)malloc(dataSize);
            if (result.data != NULL) {
                memcpy(result.data, inst->latest_frame.data, dataSize);
                result.width = inst->latest_frame.width;
                result.height = inst->latest_frame.height;
                result.bytes_per_row = inst->latest_frame.bytes_per_row;
            }
        }
        pthread_mutex_unlock(&inst->frame_mutex);
        return result;
    }

    pthread_mutex_lock(&inst->frame_mutex);
    if (inst->latest_frame.data != NULL) {
        size_t dataSize = inst->latest_frame.height * inst->latest_frame.bytes_per_row;
        result.data = (uint8_t*)malloc(dataSize);
        if (result.data != NULL) {
            memcpy(result.data, inst->latest_frame.data, dataSize);
            result.width = inst->latest_frame.width;
            result.height = inst->latest_frame.height;
            result.bytes_per_row = inst->latest_frame.bytes_per_row;
        }
    }
    pthread_mutex_unlock(&inst->frame_mutex);

    return result;
}

// Free frame data
void mc_free_frame(MCFrameData frame) {
    if (frame.data != NULL) {
        free(frame.data);
    }
}

// Check if instance is active
int mc_is_active(int slot) {
    if (slot < 0 || slot >= MAX_CAPTURE_INSTANCES) return 0;
    return g_instances[slot].active;
}

// Get window ID for an instance
uint32_t mc_get_window_id(int slot) {
    if (slot < 0 || slot >= MAX_CAPTURE_INSTANCES) return 0;
    return g_instances[slot].window_id;
}

// Get the topmost window from a list of window IDs (z-order based)
// Returns the window ID that appears first (topmost) in the screen's z-order
// window_ids: array of window IDs to check
// count: number of window IDs in the array
uint32_t mc_get_topmost_window(uint32_t* window_ids, int count) {
    @autoreleasepool {
        if (window_ids == NULL || count == 0) return 0;

        // Get all on-screen windows in z-order (front to back)
        // kCGWindowListOptionOnScreenOnly: only visible windows
        // kCGWindowListExcludeDesktopElements: skip desktop/wallpaper
        CFArrayRef windowList = CGWindowListCopyWindowInfo(
            kCGWindowListOptionOnScreenOnly | kCGWindowListExcludeDesktopElements,
            kCGNullWindowID
        );

        if (windowList == NULL) return 0;

        uint32_t topmostWindowID = 0;
        CFIndex listCount = CFArrayGetCount(windowList);

        // Iterate through windows in z-order (front to back)
        for (CFIndex i = 0; i < listCount && topmostWindowID == 0; i++) {
            CFDictionaryRef windowInfo = (CFDictionaryRef)CFArrayGetValueAtIndex(windowList, i);

            // Get window layer - we only care about normal windows (layer 0)
            CFNumberRef layerRef = (CFNumberRef)CFDictionaryGetValue(windowInfo, kCGWindowLayer);
            if (layerRef == NULL) continue;

            int layer;
            CFNumberGetValue(layerRef, kCFNumberIntType, &layer);
            if (layer != 0) continue; // Skip non-normal windows (menus, overlays, etc.)

            // Get the window ID (kCGWindowNumber)
            CFNumberRef windowIDRef = (CFNumberRef)CFDictionaryGetValue(windowInfo, kCGWindowNumber);
            if (windowIDRef == NULL) continue;

            uint32_t windowID;
            CFNumberGetValue(windowIDRef, kCFNumberIntType, &windowID);

            // Check if this window is one of our captured windows
            for (int j = 0; j < count; j++) {
                if (window_ids[j] == windowID) {
                    topmostWindowID = windowID;
                    break;
                }
            }
        }

        CFRelease(windowList);
        return topmostWindowID;
    }
}

// Legacy function - kept for compatibility but now just returns 0
// Use mc_get_topmost_window() instead
uint32_t mc_get_focused_window_id() {
    return 0;
}

// Get number of active instances
int mc_get_active_count() {
    int count = 0;
    for (int i = 0; i < MAX_CAPTURE_INSTANCES; i++) {
        if (g_instances[i].active) count++;
    }
    return count;
}
*/
import "C"

import (
	"fmt"
	"sync"
	"time"
	"unsafe"
)

// MaxCaptureInstances is the maximum number of concurrent captures
const MaxCaptureInstances = 4

// CaptureInstance represents a single window capture session
type CaptureInstance struct {
	slot     int
	windowID uint32
	active   bool
}

// MultiCapture manages multiple concurrent window captures
type MultiCapture struct {
	instances []*CaptureInstance
	mu        sync.RWMutex
}

// NewMultiCapture creates a new multi-capture manager
func NewMultiCapture() *MultiCapture {
	return &MultiCapture{
		instances: make([]*CaptureInstance, 0, MaxCaptureInstances),
	}
}

// StartWindowCapture starts capturing a window, returns the capture instance
func (mc *MultiCapture) StartWindowCapture(windowID uint32, width, height, fps int) (*CaptureInstance, error) {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if len(mc.instances) >= MaxCaptureInstances {
		return nil, fmt.Errorf("maximum capture instances (%d) reached", MaxCaptureInstances)
	}

	// Check if already capturing this window
	for _, inst := range mc.instances {
		if inst.windowID == windowID {
			return nil, fmt.Errorf("window %d is already being captured", windowID)
		}
	}

	slot := C.mc_start_window_capture(C.uint32_t(windowID), C.int(width), C.int(height), C.int(fps))
	if slot < 0 {
		switch slot {
		case -1:
			return nil, fmt.Errorf("no free capture slots available")
		case -2:
			return nil, fmt.Errorf("window not found: %d", windowID)
		case -3:
			return nil, fmt.Errorf("failed to add stream output")
		case -4:
			return nil, fmt.Errorf("failed to start capture")
		default:
			return nil, fmt.Errorf("capture error: %d", slot)
		}
	}

	inst := &CaptureInstance{
		slot:     int(slot),
		windowID: windowID,
		active:   true,
	}
	mc.instances = append(mc.instances, inst)

	return inst, nil
}

// StartDisplayCapture starts capturing the display (fullscreen), returns the capture instance
// Uses windowID = 0 to indicate this is a display capture
func (mc *MultiCapture) StartDisplayCapture(width, height, fps int) (*CaptureInstance, error) {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if len(mc.instances) >= MaxCaptureInstances {
		return nil, fmt.Errorf("maximum capture instances (%d) reached", MaxCaptureInstances)
	}

	// Check if already capturing display (windowID 0)
	for _, inst := range mc.instances {
		if inst.windowID == 0 {
			return nil, fmt.Errorf("display is already being captured")
		}
	}

	slot := C.mc_start_display_capture(C.int(width), C.int(height), C.int(fps))
	if slot < 0 {
		switch slot {
		case -1:
			return nil, fmt.Errorf("no free capture slots available")
		case -2:
			return nil, fmt.Errorf("display not found")
		case -3:
			return nil, fmt.Errorf("failed to add stream output")
		case -4:
			return nil, fmt.Errorf("failed to start capture")
		default:
			return nil, fmt.Errorf("capture error: %d", slot)
		}
	}

	inst := &CaptureInstance{
		slot:     int(slot),
		windowID: 0, // 0 indicates display capture
		active:   true,
	}
	mc.instances = append(mc.instances, inst)

	return inst, nil
}

// StopCapture stops a capture instance
func (mc *MultiCapture) StopCapture(inst *CaptureInstance) {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	if inst == nil || !inst.active {
		return
	}

	C.mc_stop_capture(C.int(inst.slot))
	inst.active = false

	// Remove from instances list
	for i, instance := range mc.instances {
		if instance == inst {
			mc.instances = append(mc.instances[:i], mc.instances[i+1:]...)
			break
		}
	}
}

// StopAll stops all capture instances
func (mc *MultiCapture) StopAll() {
	mc.mu.Lock()
	defer mc.mu.Unlock()

	C.mc_stop_all()

	for _, inst := range mc.instances {
		inst.active = false
	}
	mc.instances = mc.instances[:0]
}

// UpdateStreamSize updates the capture stream configuration with new dimensions
// This should be called when the captured window is resized
func (mc *MultiCapture) UpdateStreamSize(inst *CaptureInstance, newWidth, newHeight int) error {
	if inst == nil || !inst.active {
		return fmt.Errorf("capture instance not active")
	}

	result := C.mc_update_stream_size(C.int(inst.slot), C.int(newWidth), C.int(newHeight))
	if result != 0 {
		return fmt.Errorf("failed to update stream size: %d", result)
	}
	return nil
}

// GetWindowSize gets the current actual window dimensions from macOS
func (mc *MultiCapture) GetWindowSize(inst *CaptureInstance) (width, height int, err error) {
	if inst == nil || !inst.active {
		return 0, 0, fmt.Errorf("capture instance not active")
	}

	var w, h C.int
	result := C.mc_get_window_size(C.int(inst.slot), &w, &h)
	if result != 0 {
		return 0, 0, fmt.Errorf("failed to get window size")
	}
	return int(w), int(h), nil
}

// GetConfigSize gets the current stream configuration dimensions
func (mc *MultiCapture) GetConfigSize(inst *CaptureInstance) (width, height int, err error) {
	if inst == nil || !inst.active {
		return 0, 0, fmt.Errorf("capture instance not active")
	}

	var w, h C.int
	result := C.mc_get_config_size(C.int(inst.slot), &w, &h)
	if result != 0 {
		return 0, 0, fmt.Errorf("failed to get config size")
	}
	return int(w), int(h), nil
}

// GetLatestFrameBGRA gets the latest frame from a capture instance
func (mc *MultiCapture) GetLatestFrameBGRA(inst *CaptureInstance, timeout time.Duration) (*BGRAFrame, error) {
	if inst == nil || !inst.active {
		return nil, fmt.Errorf("capture instance not active")
	}

	frame := C.mc_get_latest_frame(C.int(inst.slot), C.int(timeout.Milliseconds()))
	if frame.data == nil {
		return nil, fmt.Errorf("no frame available")
	}
	defer C.mc_free_frame(frame)

	width := int(frame.width)
	height := int(frame.height)
	bytesPerRow := int(frame.bytes_per_row)
	dataSize := height * bytesPerRow

	srcData := unsafe.Slice((*byte)(unsafe.Pointer(frame.data)), dataSize)
	data := make([]byte, dataSize)
	copy(data, srcData)

	return &BGRAFrame{
		Data:   data,
		Width:  width,
		Height: height,
		Stride: bytesPerRow,
	}, nil
}

// GetActiveInstances returns all active capture instances
func (mc *MultiCapture) GetActiveInstances() []*CaptureInstance {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	result := make([]*CaptureInstance, len(mc.instances))
	copy(result, mc.instances)
	return result
}

// GetActiveCount returns the number of active captures
func (mc *MultiCapture) GetActiveCount() int {
	mc.mu.RLock()
	defer mc.mu.RUnlock()
	return len(mc.instances)
}

// GetFocusedWindowID returns the currently focused window ID (legacy - returns 0)
// Use GetTopmostWindow instead
func GetFocusedWindowID() uint32 {
	return uint32(C.mc_get_focused_window_id())
}

// GetTopmostWindow returns which of the given window IDs is topmost in z-order
// This is more reliable than GetFocusedWindowID as it checks actual z-order
func GetTopmostWindow(windowIDs []uint32) uint32 {
	if len(windowIDs) == 0 {
		return 0
	}

	// Convert Go slice to C array
	cArray := make([]C.uint32_t, len(windowIDs))
	for i, id := range windowIDs {
		cArray[i] = C.uint32_t(id)
	}

	return uint32(C.mc_get_topmost_window(&cArray[0], C.int(len(windowIDs))))
}

// IsCapturing checks if a specific window is being captured
func (mc *MultiCapture) IsCapturing(windowID uint32) bool {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	for _, inst := range mc.instances {
		if inst.windowID == windowID && inst.active {
			return true
		}
	}
	return false
}

// GetInstanceByWindowID returns the capture instance for a window
func (mc *MultiCapture) GetInstanceByWindowID(windowID uint32) *CaptureInstance {
	mc.mu.RLock()
	defer mc.mu.RUnlock()

	for _, inst := range mc.instances {
		if inst.windowID == windowID && inst.active {
			return inst
		}
	}
	return nil
}
