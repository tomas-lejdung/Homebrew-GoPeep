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

// Buffer states for triple buffering
#define BUFFER_FREE         0  // Available for writing
#define BUFFER_WRITING      1  // Currently being written by callback
#define BUFFER_READY        2  // Contains valid frame, ready for reading
#define BUFFER_IN_USE       3  // Consumer is using this buffer

// Per-instance capture state
typedef struct {
    SCStream* stream;
    SCStreamConfiguration* config;
    dispatch_queue_t queue;
    dispatch_semaphore_t frame_semaphore;
    pthread_mutex_t frame_mutex;
    int active;
    uint32_t window_id;
    id output_delegate;  // Store delegate reference

    // Triple buffer system for zero-copy frame handling
    uint8_t* frame_buffers[3];      // Pre-allocated frame data buffers
    size_t buffer_capacities[3];    // Current capacity of each buffer
    MCFrameData frame_info[3];      // Metadata (width, height, stride) for each
    int buffer_state[3];            // BUFFER_FREE, BUFFER_WRITING, BUFFER_READY, BUFFER_IN_USE
    int write_idx;                  // Next buffer for callback to write to
    int ready_idx;                  // Most recent complete frame (-1 if none)
    pthread_mutex_t buffer_mutex;   // Protects buffer state transitions
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

    pthread_mutex_lock(&inst->buffer_mutex);

    // Get current write buffer index
    int widx = inst->write_idx;

    // Ensure buffer has enough capacity (realloc only if needed)
    if (inst->buffer_capacities[widx] < dataSize) {
        // Free old buffer if exists
        if (inst->frame_buffers[widx] != NULL) {
            free(inst->frame_buffers[widx]);
        }
        inst->frame_buffers[widx] = (uint8_t*)malloc(dataSize);
        inst->buffer_capacities[widx] = (inst->frame_buffers[widx] != NULL) ? dataSize : 0;
    }

    if (inst->frame_buffers[widx] != NULL) {
        // Mark as being written
        inst->buffer_state[widx] = BUFFER_WRITING;

        // Unlock during memcpy (the expensive part)
        pthread_mutex_unlock(&inst->buffer_mutex);

        // Single memcpy - the only copy in the zero-copy path
        memcpy(inst->frame_buffers[widx], baseAddress, dataSize);

        pthread_mutex_lock(&inst->buffer_mutex);

        // Update metadata for this buffer
        inst->frame_info[widx].data = inst->frame_buffers[widx];
        inst->frame_info[widx].width = (int)width;
        inst->frame_info[widx].height = (int)height;
        inst->frame_info[widx].bytes_per_row = (int)bytesPerRow;

        // Mark previous ready buffer as free (if not in use by consumer)
        int prev_ready = inst->ready_idx;
        if (prev_ready >= 0 && inst->buffer_state[prev_ready] == BUFFER_READY) {
            inst->buffer_state[prev_ready] = BUFFER_FREE;
        }

        // This buffer is now ready for reading
        inst->buffer_state[widx] = BUFFER_READY;
        inst->ready_idx = widx;

        // Find next free buffer for writing
        for (int i = 1; i <= 3; i++) {
            int next = (widx + i) % 3;
            if (inst->buffer_state[next] == BUFFER_FREE) {
                inst->write_idx = next;
                break;
            }
        }

        pthread_mutex_unlock(&inst->buffer_mutex);

        // Signal that a new frame is available
        if (inst->frame_semaphore != nil) {
            dispatch_semaphore_signal(inst->frame_semaphore);
        }
    } else {
        pthread_mutex_unlock(&inst->buffer_mutex);
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
        pthread_mutex_init(&g_instances[i].buffer_mutex, NULL);
        g_instances[i].ready_idx = -1;  // No frame ready initially
        g_instances[i].write_idx = 0;   // Start writing to buffer 0
        for (int j = 0; j < 3; j++) {
            g_instances[i].buffer_state[j] = BUFFER_FREE;
            g_instances[i].frame_buffers[j] = NULL;
            g_instances[i].buffer_capacities[j] = 0;
        }
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
            // Cleanup allocated resources
            inst->stream = nil;
            inst->config = nil;
            inst->output_delegate = nil;
            inst->queue = nil;
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

        // Cleanup on start failure
        inst->stream = nil;
        inst->config = nil;
        inst->output_delegate = nil;
        inst->queue = nil;
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
            // Cleanup allocated resources
            inst->stream = nil;
            inst->config = nil;
            inst->output_delegate = nil;
            inst->queue = nil;
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

        // Cleanup on start failure
        inst->stream = nil;
        inst->config = nil;
        inst->output_delegate = nil;
        inst->queue = nil;
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

    // Clean up triple buffer system
    pthread_mutex_lock(&inst->buffer_mutex);
    for (int i = 0; i < 3; i++) {
        if (inst->frame_buffers[i] != NULL) {
            free(inst->frame_buffers[i]);
            inst->frame_buffers[i] = NULL;
        }
        inst->buffer_capacities[i] = 0;
        inst->buffer_state[i] = BUFFER_FREE;
        memset(&inst->frame_info[i], 0, sizeof(MCFrameData));
    }
    inst->ready_idx = -1;
    inst->write_idx = 0;
    pthread_mutex_unlock(&inst->buffer_mutex);

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

// Get latest frame from an instance (zero-copy version)
// Returns pointer to buffer - caller MUST call mc_release_frame_buffer when done!
MCFrameData mc_get_latest_frame(int slot, int timeout_ms) {
    MCFrameData result = {NULL, 0, 0, 0};

    if (slot < 0 || slot >= MAX_CAPTURE_INSTANCES) return result;

    CaptureInstance* inst = &g_instances[slot];
    if (!inst->active || inst->frame_semaphore == nil) return result;

    // Drain excess signals
    while (dispatch_semaphore_wait(inst->frame_semaphore, DISPATCH_TIME_NOW) == 0) {}

    // Wait for new frame
    dispatch_time_t timeout = dispatch_time(DISPATCH_TIME_NOW, timeout_ms * NSEC_PER_MSEC);
    long wait_result = dispatch_semaphore_wait(inst->frame_semaphore, timeout);

    pthread_mutex_lock(&inst->buffer_mutex);

    int ridx = inst->ready_idx;

    // Check if we have a ready frame
    if (ridx >= 0 && inst->buffer_state[ridx] == BUFFER_READY) {
        // Mark buffer as in_use (consumer owns it now)
        inst->buffer_state[ridx] = BUFFER_IN_USE;

        // Return pointer to buffer (NO COPY - zero-copy!)
        result = inst->frame_info[ridx];

        // Clear ready_idx - callback will set a new one
        inst->ready_idx = -1;
    } else if (wait_result != 0) {
        // Timeout - try to find any buffer that's in_use or ready
        // This handles the case where consumer calls repeatedly without new frames
        for (int i = 0; i < 3; i++) {
            if (inst->buffer_state[i] == BUFFER_IN_USE && inst->frame_info[i].data != NULL) {
                // Return the currently in-use buffer again (consumer still has it)
                result = inst->frame_info[i];
                break;
            }
        }
    }

    pthread_mutex_unlock(&inst->buffer_mutex);

    return result;
}

// Release a frame buffer back to the pool (zero-copy version)
// Must be called when consumer is done with frame from mc_get_latest_frame
void mc_release_frame_buffer(int slot, uint8_t* data) {
    if (slot < 0 || slot >= MAX_CAPTURE_INSTANCES || data == NULL) return;

    CaptureInstance* inst = &g_instances[slot];

    pthread_mutex_lock(&inst->buffer_mutex);

    // Find which buffer this pointer belongs to and release it
    for (int i = 0; i < 3; i++) {
        if (inst->frame_buffers[i] == data) {
            if (inst->buffer_state[i] == BUFFER_IN_USE) {
                inst->buffer_state[i] = BUFFER_FREE;
            }
            break;
        }
    }

    pthread_mutex_unlock(&inst->buffer_mutex);
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

// Get the actual frontmost window ID (the window receiving keyboard input)
// This uses NSWorkspace to get the frontmost application, then finds its topmost window
uint32_t mc_get_frontmost_window_id() {
    @autoreleasepool {
        // Get the frontmost application (the one receiving keyboard input)
        NSRunningApplication *frontApp = [[NSWorkspace sharedWorkspace] frontmostApplication];
        if (frontApp == nil) return 0;

        pid_t frontPID = frontApp.processIdentifier;

        // Get all on-screen windows in z-order
        CFArrayRef windowList = CGWindowListCopyWindowInfo(
            kCGWindowListOptionOnScreenOnly | kCGWindowListExcludeDesktopElements,
            kCGNullWindowID
        );

        if (windowList == NULL) return 0;

        uint32_t frontmostWindowID = 0;
        CFIndex listCount = CFArrayGetCount(windowList);

        // Find the first (topmost) window belonging to the frontmost application
        for (CFIndex i = 0; i < listCount && frontmostWindowID == 0; i++) {
            CFDictionaryRef windowInfo = (CFDictionaryRef)CFArrayGetValueAtIndex(windowList, i);

            // Get window layer - only care about normal windows (layer 0)
            CFNumberRef layerRef = (CFNumberRef)CFDictionaryGetValue(windowInfo, kCGWindowLayer);
            if (layerRef == NULL) continue;

            int layer;
            CFNumberGetValue(layerRef, kCFNumberIntType, &layer);
            if (layer != 0) continue;

            // Get owner PID
            CFNumberRef pidRef = (CFNumberRef)CFDictionaryGetValue(windowInfo, kCGWindowOwnerPID);
            if (pidRef == NULL) continue;

            pid_t windowPID;
            CFNumberGetValue(pidRef, kCFNumberIntType, &windowPID);

            // Check if this window belongs to the frontmost app
            if (windowPID == frontPID) {
                // Get window ID
                CFNumberRef windowIDRef = (CFNumberRef)CFDictionaryGetValue(windowInfo, kCGWindowNumber);
                if (windowIDRef != NULL) {
                    CFNumberGetValue(windowIDRef, kCFNumberIntType, &frontmostWindowID);
                }
            }
        }

        CFRelease(windowList);
        return frontmostWindowID;
    }
}

// Get number of active instances
int mc_get_active_count() {
    int count = 0;
    for (int i = 0; i < MAX_CAPTURE_INSTANCES; i++) {
        if (g_instances[i].active) count++;
    }
    return count;
}

// ============================================================================
// Focus Change Observer - Event-driven focus detection
// ============================================================================

// Global observer state
static id g_focus_observer = nil;
static dispatch_queue_t g_focus_queue = nil;

// Callback channel - Go will poll this
static volatile int g_focus_changed_flag = 0;

// Start observing application focus changes
// Uses NSWorkspace notifications for instant detection
void mc_start_focus_observer() {
    @autoreleasepool {
        if (g_focus_observer != nil) return; // Already observing

        // Create a serial queue for notifications
        g_focus_queue = dispatch_queue_create("com.gopeep.focus", DISPATCH_QUEUE_SERIAL);

        // Register for application activation notifications
        g_focus_observer = [[[NSWorkspace sharedWorkspace] notificationCenter]
            addObserverForName:NSWorkspaceDidActivateApplicationNotification
            object:nil
            queue:nil
            usingBlock:^(NSNotification* note) {
                // Set flag - Go will poll this
                g_focus_changed_flag = 1;
            }];

        NSLog(@"Focus observer started");
    }
}

// Stop observing focus changes
void mc_stop_focus_observer() {
    @autoreleasepool {
        if (g_focus_observer != nil) {
            [[[NSWorkspace sharedWorkspace] notificationCenter] removeObserver:g_focus_observer];
            g_focus_observer = nil;
            NSLog(@"Focus observer stopped");
        }
        if (g_focus_queue != nil) {
            g_focus_queue = nil;
        }
        g_focus_changed_flag = 0;
    }
}

// Check if focus changed since last check (and clear the flag)
// Returns 1 if focus changed, 0 otherwise
int mc_check_focus_changed() {
    if (g_focus_changed_flag) {
        g_focus_changed_flag = 0;
        return 1;
    }
    return 0;
}

*/
import "C"

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// Release returns the frame buffer to the capture pool.
// Must be called when done with zero-copy frames from GetLatestFrameBGRA.
// Safe to call multiple times or on Go-owned frames (no-op).
// Thread-safe: uses atomic swap to prevent double-release race conditions.
func (f *BGRAFrame) Release() {
	if f.slot < 0 {
		return
	}
	// Atomic swap to prevent double-release if called concurrently
	ptr := atomic.SwapPointer((*unsafe.Pointer)(unsafe.Pointer(&f.cData)), nil)
	if ptr != nil {
		C.mc_release_frame_buffer(C.int(f.slot), (*C.uint8_t)(ptr))
		f.Data = nil
		f.slot = -1
	}
}

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

// captureErrorToGoError converts C capture error codes to Go errors
func captureErrorToGoError(code C.int, windowID uint32) error {
	switch code {
	case -1:
		return fmt.Errorf("no free capture slots available")
	case -2:
		if windowID == 0 {
			return fmt.Errorf("display not found")
		}
		return fmt.Errorf("window not found: %d", windowID)
	case -3:
		return fmt.Errorf("failed to add stream output")
	case -4:
		return fmt.Errorf("failed to start capture")
	default:
		return fmt.Errorf("capture error: %d", code)
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
		return nil, captureErrorToGoError(slot, windowID)
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
		return nil, captureErrorToGoError(slot, 0)
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

// GetLatestFrameBGRA gets the latest frame from a capture instance (zero-copy).
// IMPORTANT: Caller MUST call frame.Release() when done with the frame!
// The frame data is backed by C memory and will be reused after Release().
func (mc *MultiCapture) GetLatestFrameBGRA(inst *CaptureInstance, timeout time.Duration) (*BGRAFrame, error) {
	if inst == nil || !inst.active {
		return nil, fmt.Errorf("capture instance not active")
	}

	frame := C.mc_get_latest_frame(C.int(inst.slot), C.int(timeout.Milliseconds()))
	if frame.data == nil {
		return nil, fmt.Errorf("no frame available")
	}

	width := int(frame.width)
	height := int(frame.height)
	bytesPerRow := int(frame.bytes_per_row)
	dataSize := height * bytesPerRow

	// Zero-copy: wrap C memory directly with unsafe.Slice (NO allocation, NO copy!)
	return &BGRAFrame{
		Data:   unsafe.Slice((*byte)(unsafe.Pointer(frame.data)), dataSize),
		Width:  width,
		Height: height,
		Stride: bytesPerRow,
		cData:  unsafe.Pointer(frame.data),
		slot:   inst.slot,
	}, nil
}

// GetFrontmostWindowID returns the window ID of the frontmost window (receiving keyboard input)
// This is the window that belongs to the frontmost application in macOS
func GetFrontmostWindowID() uint32 {
	return uint32(C.mc_get_frontmost_window_id())
}

// GetTopmostWindow returns which of the given window IDs is topmost in z-order
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

// ============================================================================
// Focus Observer - Event-driven focus detection
// ============================================================================

// StartFocusObserver starts the NSWorkspace focus change observer
// This provides instant notification when the user switches to a different application
func StartFocusObserver() {
	C.mc_start_focus_observer()
}

// StopFocusObserver stops the NSWorkspace focus change observer
func StopFocusObserver() {
	C.mc_stop_focus_observer()
}

// CheckFocusChanged checks if focus changed since last check (and clears the flag)
// Returns true if focus changed, false otherwise
// This is a non-blocking poll of the flag set by the NSWorkspace notification
func CheckFocusChanged() bool {
	return C.mc_check_focus_changed() != 0
}
