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

static int g_initialized = 0;

// Initialize the NSApplication for screen capture
void init_for_capture() {
    if (g_initialized) return;
    g_initialized = 1;

    @autoreleasepool {
        [NSApplication sharedApplication];
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

            result.windows = (WindowInfo*)calloc(windows.count, sizeof(WindowInfo));
            if (result.windows == NULL) {
                dispatch_semaphore_signal(semaphore);
                return;
            }

            int validCount = 0;
            for (SCWindow* window in windows) {
                if (window.title == nil || window.title.length == 0) continue;

                CGRect frame = window.frame;
                if (frame.size.width < 100 || frame.size.height < 100) continue;

                if (!window.isOnScreen) continue;

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

// Get window info by ID using CGWindowList (fast, works across Spaces)
// Returns 1 if found, 0 if not found
int get_window_by_id(uint32_t window_id, WindowInfo* out_info) {
    @autoreleasepool {
        // Query the specific window by ID
        CFArrayRef windowList = CGWindowListCopyWindowInfo(
            kCGWindowListOptionIncludingWindow,
            window_id
        );

        if (windowList == NULL || CFArrayGetCount(windowList) == 0) {
            if (windowList) CFRelease(windowList);
            return 0;
        }

        NSDictionary *info = (__bridge NSDictionary *)CFArrayGetValueAtIndex(windowList, 0);

        // Get window ID (verify it matches)
        NSNumber *windowIDNum = info[(NSString *)kCGWindowNumber];
        if (!windowIDNum || windowIDNum.unsignedIntValue != window_id) {
            CFRelease(windowList);
            return 0;
        }

        // Get owner name
        NSString *ownerName = info[(NSString *)kCGWindowOwnerName];
        if (!ownerName || ownerName.length == 0) {
            ownerName = @"Unknown";
        }

        // Get window name (title)
        NSString *windowName = info[(NSString *)kCGWindowName];
        if (!windowName) {
            windowName = @"";
        }

        // Get bounds
        NSDictionary *boundsDict = info[(NSString *)kCGWindowBounds];
        if (!boundsDict) {
            CFRelease(windowList);
            return 0;
        }

        CGRect bounds;
        if (!CGRectMakeWithDictionaryRepresentation((__bridge CFDictionaryRef)boundsDict, &bounds)) {
            CFRelease(windowList);
            return 0;
        }

        // Skip tiny windows
        if (bounds.size.width < 100 || bounds.size.height < 100) {
            CFRelease(windowList);
            return 0;
        }

        // Fill out the result
        out_info->window_id = window_id;
        out_info->owner_name = strdup([ownerName UTF8String]);
        out_info->window_name = strdup([windowName UTF8String]);
        out_info->x = (int32_t)bounds.origin.x;
        out_info->y = (int32_t)bounds.origin.y;
        out_info->width = (int32_t)bounds.size.width;
        out_info->height = (int32_t)bounds.size.height;
        out_info->on_screen = 1; // If we can query it, it's on screen

        CFRelease(windowList);
        return 1;
    }
}

// Free a single WindowInfo
void free_window_info(WindowInfo* info) {
    if (info != NULL) {
        free(info->owner_name);
        free(info->window_name);
    }
}
*/
import "C"

import (
	"fmt"
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

// GetWindowInfoByID returns window info for a specific window ID.
// Uses CGWindowList which works across Spaces, unlike ScreenCaptureKit.
// Returns nil if the window doesn't exist or is invalid.
func GetWindowInfoByID(windowID uint32) *WindowInfo {
	var cInfo C.WindowInfo
	if C.get_window_by_id(C.uint32_t(windowID), &cInfo) == 0 {
		return nil
	}
	defer C.free_window_info(&cInfo)

	return &WindowInfo{
		ID:         uint32(cInfo.window_id),
		OwnerName:  C.GoString(cInfo.owner_name),
		WindowName: C.GoString(cInfo.window_name),
		X:          int32(cInfo.x),
		Y:          int32(cInfo.y),
		Width:      int32(cInfo.width),
		Height:     int32(cInfo.height),
		OnScreen:   cInfo.on_screen != 0,
	}
}

// BGRAFrame holds raw BGRA frame data without conversion
type BGRAFrame struct {
	Data   []byte
	Width  int
	Height int
	Stride int
	// Zero-copy support fields
	cData unsafe.Pointer // Original C pointer (nil if Go-owned copy)
	slot  int            // Capture slot for release (-1 if Go-owned)
}

// HasScreenRecordingPermission checks if screen recording is allowed
func HasScreenRecordingPermission() bool {
	return C.has_screen_recording_permission() != 0
}
