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

// Window information structure
typedef struct {
    uint32_t window_id;
    char* owner_name;
    char* window_name;
    int32_t x, y;
    int32_t width, height;
    int on_screen;
} WindowInfo;

typedef struct {
    WindowInfo* windows;
    int count;
} WindowList;

typedef struct {
    uint8_t* data;
    int width;
    int height;
    int bytes_per_row;
} FrameData;

// Global state for screen capture
static SCStream* g_stream = nil;
static SCStreamConfiguration* g_config = nil;
static dispatch_queue_t g_queue = nil;
static FrameData g_latest_frame = {NULL, 0, 0, 0};
static dispatch_semaphore_t g_frame_semaphore = nil;
static int g_capture_active = 0;
static pthread_mutex_t g_frame_mutex = PTHREAD_MUTEX_INITIALIZER;
static int g_initialized = 0;

// Stream output delegate
@interface StreamOutputDelegate : NSObject <SCStreamOutput>
@end

@implementation StreamOutputDelegate

- (void)stream:(SCStream *)stream didOutputSampleBuffer:(CMSampleBufferRef)sampleBuffer ofType:(SCStreamOutputType)type {
    if (type != SCStreamOutputTypeScreen) return;

    CVImageBufferRef imageBuffer = CMSampleBufferGetImageBuffer(sampleBuffer);
    if (imageBuffer == NULL) return;

    CVPixelBufferLockBaseAddress(imageBuffer, kCVPixelBufferLock_ReadOnly);

    size_t width = CVPixelBufferGetWidth(imageBuffer);
    size_t height = CVPixelBufferGetHeight(imageBuffer);
    size_t bytesPerRow = CVPixelBufferGetBytesPerRow(imageBuffer);
    void* baseAddress = CVPixelBufferGetBaseAddress(imageBuffer);

    // Allocate new frame data
    size_t dataSize = height * bytesPerRow;
    uint8_t* newData = (uint8_t*)malloc(dataSize);
    if (newData != NULL) {
        memcpy(newData, baseAddress, dataSize);

        pthread_mutex_lock(&g_frame_mutex);

        // Swap old data
        uint8_t* oldData = g_latest_frame.data;
        g_latest_frame.data = newData;
        g_latest_frame.width = (int)width;
        g_latest_frame.height = (int)height;
        g_latest_frame.bytes_per_row = (int)bytesPerRow;

        pthread_mutex_unlock(&g_frame_mutex);

        if (oldData != NULL) {
            free(oldData);
        }

        if (g_frame_semaphore != nil) {
            dispatch_semaphore_signal(g_frame_semaphore);
        }
    }

    CVPixelBufferUnlockBaseAddress(imageBuffer, kCVPixelBufferLock_ReadOnly);
}

@end

static StreamOutputDelegate* g_output_delegate = nil;

// Initialize the NSApplication for screen capture
void init_for_capture() {
    if (g_initialized) return;
    g_initialized = 1;

    // Force NSApplication initialization
    @autoreleasepool {
        [NSApplication sharedApplication];
        // Process any pending events
        NSEvent *event;
        while ((event = [NSApp nextEventMatchingMask:NSEventMaskAny
                                            untilDate:nil
                                               inMode:NSDefaultRunLoopMode
                                              dequeue:YES])) {
            [NSApp sendEvent:event];
        }
    }
}

// List all windows using ScreenCaptureKit
WindowList list_windows() {
    init_for_capture();

    __block WindowList result = {NULL, 0};
    dispatch_semaphore_t semaphore = dispatch_semaphore_create(0);

    [SCShareableContent getShareableContentWithCompletionHandler:^(SCShareableContent* content, NSError* error) {
        @autoreleasepool {
            if (error != nil || content == nil || content.windows.count == 0) {
                dispatch_semaphore_signal(semaphore);
                return;
            }

            NSArray<SCWindow*>* windows = content.windows;

            // Allocate window info array
            result.windows = (WindowInfo*)calloc(windows.count, sizeof(WindowInfo));
            if (result.windows == NULL) {
                dispatch_semaphore_signal(semaphore);
                return;
            }

            int validCount = 0;
            for (SCWindow* window in windows) {
                // Skip windows without titles
                if (window.title == nil || window.title.length == 0) continue;

                // Skip small windows
                CGRect frame = window.frame;
                if (frame.size.width < 100 || frame.size.height < 100) continue;

                // Skip windows not on screen
                if (!window.isOnScreen) continue;

                // Must have an owning application with a name
                if (window.owningApplication == nil) continue;
                NSString* ownerName = window.owningApplication.applicationName;
                if (ownerName == nil || ownerName.length == 0) continue;

                // Skip system apps
                if ([ownerName isEqualToString:@"Wallpaper"]) continue;
                if ([ownerName isEqualToString:@"Dock"]) continue;
                if ([ownerName isEqualToString:@"Window Server"]) continue;
                if ([ownerName isEqualToString:@"Control Center"]) continue;

                NSString* windowTitle = window.title ?: @"";

                // Skip windows with system-like titles
                if ([windowTitle hasPrefix:@"Wallpaper"]) continue;
                if ([windowTitle hasSuffix:@"Backstop"]) continue;
                if ([windowTitle isEqualToString:@"Menubar"]) continue;
                if ([windowTitle containsString:@"underbelly"]) continue;

                WindowInfo* info = &result.windows[validCount];
                info->window_id = (uint32_t)window.windowID;

                info->owner_name = strdup([ownerName UTF8String]);
                info->window_name = strdup([windowTitle UTF8String]);
                info->x = (int32_t)frame.origin.x;
                info->y = (int32_t)frame.origin.y;
                info->width = (int32_t)frame.size.width;
                info->height = (int32_t)frame.size.height;
                info->on_screen = window.isOnScreen ? 1 : 0;

                validCount++;
            }

            result.count = validCount;
        }
        dispatch_semaphore_signal(semaphore);
    }];

    dispatch_semaphore_wait(semaphore, dispatch_time(DISPATCH_TIME_NOW, 5 * NSEC_PER_SEC));
    return result;
}

// Free window list
void free_window_list(WindowList list) {
    if (list.windows != NULL) {
        for (int i = 0; i < list.count; i++) {
            free(list.windows[i].owner_name);
            free(list.windows[i].window_name);
        }
        free(list.windows);
    }
}

// Start capturing a window
int start_window_capture(uint32_t window_id, int target_width, int target_height, int fps) {
    if (g_capture_active) {
        return 0; // Already capturing
    }

    init_for_capture();

    __block SCContentFilter* filter = nil;
    __block int configWidth = target_width;
    __block int configHeight = target_height;
    dispatch_semaphore_t findSemaphore = dispatch_semaphore_create(0);

    [SCShareableContent getShareableContentWithCompletionHandler:^(SCShareableContent* content, NSError* error) {
        if (error == nil && content != nil) {
            for (SCWindow* window in content.windows) {
                if (window.windowID == window_id) {
                    // Create the filter inside the completion handler while window is valid
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
        return -1; // Window not found
    }

    @autoreleasepool {
        // Configure stream
        g_config = [[SCStreamConfiguration alloc] init];
        g_config.width = configWidth;
        g_config.height = configHeight;
        g_config.minimumFrameInterval = CMTimeMake(1, fps > 0 ? fps : 30);
        g_config.pixelFormat = kCVPixelFormatType_32BGRA;
        g_config.showsCursor = YES;

        // Create stream
        g_stream = [[SCStream alloc] initWithFilter:filter configuration:g_config delegate:nil];

        // Create queue and delegate
        g_queue = dispatch_queue_create("com.gopeep.capture", DISPATCH_QUEUE_SERIAL);
        g_output_delegate = [[StreamOutputDelegate alloc] init];
        g_frame_semaphore = dispatch_semaphore_create(0);

        NSError* addError = nil;
        [g_stream addStreamOutput:g_output_delegate type:SCStreamOutputTypeScreen sampleHandlerQueue:g_queue error:&addError];
        if (addError != nil) {
            NSLog(@"Failed to add stream output: %@", addError);
            return -2;
        }

        // Start capture
        __block int startResult = 0;
        dispatch_semaphore_t startSemaphore = dispatch_semaphore_create(0);

        [g_stream startCaptureWithCompletionHandler:^(NSError* error) {
            if (error != nil) {
                NSLog(@"Failed to start capture: %@", error);
                startResult = -3;
            }
            dispatch_semaphore_signal(startSemaphore);
        }];

        dispatch_semaphore_wait(startSemaphore, dispatch_time(DISPATCH_TIME_NOW, 5 * NSEC_PER_SEC));

        if (startResult == 0) {
            g_capture_active = 1;
        }

        return startResult;
    }
}

// Start capturing the display
int start_display_capture(int target_width, int target_height, int fps) {
    if (g_capture_active) {
        return 0;
    }

    init_for_capture();

    __block SCContentFilter* filter = nil;
    __block int configWidth = target_width;
    __block int configHeight = target_height;
    dispatch_semaphore_t semaphore = dispatch_semaphore_create(0);

    [SCShareableContent getShareableContentWithCompletionHandler:^(SCShareableContent* content, NSError* error) {
        if (error == nil && content != nil && content.displays.count > 0) {
            SCDisplay* display = content.displays[0];
            filter = [[SCContentFilter alloc] initWithDisplay:display excludingWindows:@[]];
            if (configWidth <= 0) configWidth = (int)display.width;
            if (configHeight <= 0) configHeight = (int)display.height;
        }
        dispatch_semaphore_signal(semaphore);
    }];

    dispatch_semaphore_wait(semaphore, dispatch_time(DISPATCH_TIME_NOW, 5 * NSEC_PER_SEC));

    if (filter == nil) {
        return -1;
    }

    @autoreleasepool {
        // Configure stream
        g_config = [[SCStreamConfiguration alloc] init];
        g_config.width = configWidth;
        g_config.height = configHeight;
        g_config.minimumFrameInterval = CMTimeMake(1, fps > 0 ? fps : 30);
        g_config.pixelFormat = kCVPixelFormatType_32BGRA;
        g_config.showsCursor = YES;

        // Create stream
        g_stream = [[SCStream alloc] initWithFilter:filter configuration:g_config delegate:nil];

        // Create queue and delegate
        g_queue = dispatch_queue_create("com.gopeep.capture", DISPATCH_QUEUE_SERIAL);
        g_output_delegate = [[StreamOutputDelegate alloc] init];
        g_frame_semaphore = dispatch_semaphore_create(0);

        NSError* error = nil;
        [g_stream addStreamOutput:g_output_delegate type:SCStreamOutputTypeScreen sampleHandlerQueue:g_queue error:&error];
        if (error != nil) {
            return -2;
        }

        __block int startResult = 0;
        dispatch_semaphore_t startSemaphore = dispatch_semaphore_create(0);

        [g_stream startCaptureWithCompletionHandler:^(NSError* error) {
            if (error != nil) {
                startResult = -3;
            }
            dispatch_semaphore_signal(startSemaphore);
        }];

        dispatch_semaphore_wait(startSemaphore, dispatch_time(DISPATCH_TIME_NOW, 5 * NSEC_PER_SEC));

        if (startResult == 0) {
            g_capture_active = 1;
        }

        return startResult;
    }
}

// Stop capturing
void stop_capture() {
    if (!g_capture_active) return;

    @autoreleasepool {
        if (g_stream != nil) {
            dispatch_semaphore_t semaphore = dispatch_semaphore_create(0);
            [g_stream stopCaptureWithCompletionHandler:^(NSError* error) {
                dispatch_semaphore_signal(semaphore);
            }];
            dispatch_semaphore_wait(semaphore, dispatch_time(DISPATCH_TIME_NOW, 2 * NSEC_PER_SEC));
            g_stream = nil;
        }

        g_config = nil;
        g_output_delegate = nil;
    }

    pthread_mutex_lock(&g_frame_mutex);
    if (g_latest_frame.data != NULL) {
        free(g_latest_frame.data);
        g_latest_frame.data = NULL;
    }
    pthread_mutex_unlock(&g_frame_mutex);

    g_capture_active = 0;
}

// Get latest frame (blocks until frame is available or timeout)
FrameData get_latest_frame(int timeout_ms) {
    FrameData result = {NULL, 0, 0, 0};

    if (!g_capture_active || g_frame_semaphore == nil) {
        return result;
    }

    // First try non-blocking to drain any excess signals
    while (dispatch_semaphore_wait(g_frame_semaphore, DISPATCH_TIME_NOW) == 0) {
        // Drain excess signals
    }

    // Wait for a new frame
    dispatch_time_t timeout = dispatch_time(DISPATCH_TIME_NOW, timeout_ms * NSEC_PER_MSEC);
    if (dispatch_semaphore_wait(g_frame_semaphore, timeout) != 0) {
        // Timeout - but check if we have a frame anyway
        pthread_mutex_lock(&g_frame_mutex);
        if (g_latest_frame.data != NULL) {
            // Return the last known frame
            size_t dataSize = g_latest_frame.height * g_latest_frame.bytes_per_row;
            result.data = (uint8_t*)malloc(dataSize);
            if (result.data != NULL) {
                memcpy(result.data, g_latest_frame.data, dataSize);
                result.width = g_latest_frame.width;
                result.height = g_latest_frame.height;
                result.bytes_per_row = g_latest_frame.bytes_per_row;
            }
        }
        pthread_mutex_unlock(&g_frame_mutex);
        return result;
    }

    pthread_mutex_lock(&g_frame_mutex);

    // Copy the latest frame
    if (g_latest_frame.data != NULL) {
        size_t dataSize = g_latest_frame.height * g_latest_frame.bytes_per_row;
        result.data = (uint8_t*)malloc(dataSize);
        if (result.data != NULL) {
            memcpy(result.data, g_latest_frame.data, dataSize);
            result.width = g_latest_frame.width;
            result.height = g_latest_frame.height;
            result.bytes_per_row = g_latest_frame.bytes_per_row;
        }
    }

    pthread_mutex_unlock(&g_frame_mutex);

    return result;
}

// Free frame data
void free_frame(FrameData frame) {
    if (frame.data != NULL) {
        free(frame.data);
    }
}

// Check if capture is active
int is_capture_active() {
    return g_capture_active;
}

// Check screen recording permission
int has_screen_recording_permission() {
    init_for_capture();

    if (@available(macOS 11.0, *)) {
        dispatch_semaphore_t semaphore = dispatch_semaphore_create(0);
        __block BOOL hasPermission = NO;

        [SCShareableContent getShareableContentWithCompletionHandler:^(SCShareableContent* content, NSError* error) {
            hasPermission = (error == nil && content != nil);
            dispatch_semaphore_signal(semaphore);
        }];

        dispatch_semaphore_wait(semaphore, dispatch_time(DISPATCH_TIME_NOW, 5 * NSEC_PER_SEC));
        return hasPermission ? 1 : 0;
    }
    return 0;
}
*/
import "C"

import (
	"fmt"
	"image"
	"strings"
	"time"
	"unsafe"
)

// WindowInfo represents information about a window
type WindowInfo struct {
	ID         uint32
	OwnerName  string // Application name
	WindowName string // Window title
	X, Y       int32
	Width      int32
	Height     int32
	OnScreen   bool
}

// DisplayName returns a formatted display name for the window
func (w WindowInfo) DisplayName() string {
	if w.WindowName != "" {
		return fmt.Sprintf("%s - %s", w.OwnerName, w.WindowName)
	}
	return w.OwnerName
}

// MatchesName checks if window matches a partial name (case-insensitive)
func (w WindowInfo) MatchesName(query string) bool {
	query = strings.ToLower(query)
	return strings.Contains(strings.ToLower(w.OwnerName), query) ||
		strings.Contains(strings.ToLower(w.WindowName), query)
}

// ListWindows returns a list of available windows
func ListWindows() ([]WindowInfo, error) {
	cList := C.list_windows()
	defer C.free_window_list(cList)

	if cList.count == 0 {
		return nil, nil
	}

	windows := make([]WindowInfo, cList.count)
	cWindows := unsafe.Slice(cList.windows, cList.count)

	for i := 0; i < int(cList.count); i++ {
		cWin := cWindows[i]
		windows[i] = WindowInfo{
			ID:         uint32(cWin.window_id),
			OwnerName:  C.GoString(cWin.owner_name),
			WindowName: C.GoString(cWin.window_name),
			X:          int32(cWin.x),
			Y:          int32(cWin.y),
			Width:      int32(cWin.width),
			Height:     int32(cWin.height),
			OnScreen:   cWin.on_screen != 0,
		}
	}

	return windows, nil
}

// StartWindowCapture starts capturing a specific window
func StartWindowCapture(windowID uint32, width, height, fps int) error {
	result := C.start_window_capture(C.uint32_t(windowID), C.int(width), C.int(height), C.int(fps))
	if result != 0 {
		switch result {
		case -1:
			return fmt.Errorf("window not found: %d", windowID)
		case -2:
			return fmt.Errorf("failed to add stream output")
		case -3:
			return fmt.Errorf("failed to start capture")
		default:
			return fmt.Errorf("capture error: %d", result)
		}
	}
	return nil
}

// StartDisplayCapture starts capturing the main display
func StartDisplayCapture(width, height, fps int) error {
	result := C.start_display_capture(C.int(width), C.int(height), C.int(fps))
	if result != 0 {
		switch result {
		case -1:
			return fmt.Errorf("display not found")
		case -2:
			return fmt.Errorf("failed to add stream output")
		case -3:
			return fmt.Errorf("failed to start capture")
		default:
			return fmt.Errorf("capture error: %d", result)
		}
	}
	return nil
}

// StopCapture stops the current capture
func StopCapture() {
	C.stop_capture()
}

// IsCaptureActive returns true if capture is running
func IsCaptureActive() bool {
	return C.is_capture_active() != 0
}

// GetLatestFrame gets the latest captured frame, blocks until available or timeout
func GetLatestFrame(timeout time.Duration) (*image.RGBA, error) {
	frame := C.get_latest_frame(C.int(timeout.Milliseconds()))
	if frame.data == nil {
		return nil, fmt.Errorf("no frame available")
	}
	defer C.free_frame(frame)

	return frameDataToImage(frame), nil
}

// BGRAFrame holds raw BGRA frame data without conversion
type BGRAFrame struct {
	Data   []byte
	Width  int
	Height int
	Stride int
}

// frameDataToImage converts C FrameData (BGRA) to Go image.RGBA
// Deprecated: Use frameDataToBGRA for better performance
func frameDataToImage(frame C.FrameData) *image.RGBA {
	width := int(frame.width)
	height := int(frame.height)
	bytesPerRow := int(frame.bytes_per_row)

	// Create RGBA image
	img := image.NewRGBA(image.Rect(0, 0, width, height))

	// Convert BGRA to RGBA
	srcData := unsafe.Slice(frame.data, height*bytesPerRow)

	for y := 0; y < height; y++ {
		srcRow := srcData[y*bytesPerRow : y*bytesPerRow+width*4]
		dstRow := img.Pix[y*img.Stride : y*img.Stride+width*4]

		for x := 0; x < width; x++ {
			srcIdx := x * 4
			dstIdx := x * 4

			// BGRA -> RGBA (cast C.uint8_t to Go uint8)
			dstRow[dstIdx+0] = uint8(srcRow[srcIdx+2]) // R
			dstRow[dstIdx+1] = uint8(srcRow[srcIdx+1]) // G
			dstRow[dstIdx+2] = uint8(srcRow[srcIdx+0]) // B
			dstRow[dstIdx+3] = uint8(srcRow[srcIdx+3]) // A
		}
	}

	return img
}

// frameDataToBGRA returns raw BGRA data without color conversion
func frameDataToBGRA(frame C.FrameData) *BGRAFrame {
	width := int(frame.width)
	height := int(frame.height)
	bytesPerRow := int(frame.bytes_per_row)
	dataSize := height * bytesPerRow

	// Copy the raw BGRA data (convert C.uint8_t slice to Go byte slice)
	srcData := unsafe.Slice((*byte)(unsafe.Pointer(frame.data)), dataSize)
	data := make([]byte, dataSize)
	copy(data, srcData)

	return &BGRAFrame{
		Data:   data,
		Width:  width,
		Height: height,
		Stride: bytesPerRow,
	}
}

// GetLatestFrameBGRA gets the latest captured frame as raw BGRA data
func GetLatestFrameBGRA(timeout time.Duration) (*BGRAFrame, error) {
	frame := C.get_latest_frame(C.int(timeout.Milliseconds()))
	if frame.data == nil {
		return nil, fmt.Errorf("no frame available")
	}
	defer C.free_frame(frame)

	return frameDataToBGRA(frame), nil
}

// HasScreenRecordingPermission checks if screen recording is allowed
func HasScreenRecordingPermission() bool {
	return C.has_screen_recording_permission() != 0
}

// FindWindowByName finds windows matching a name pattern
func FindWindowByName(pattern string) ([]WindowInfo, error) {
	windows, err := ListWindows()
	if err != nil {
		return nil, err
	}

	var matches []WindowInfo
	for _, w := range windows {
		if w.MatchesName(pattern) {
			matches = append(matches, w)
		}
	}

	return matches, nil
}
