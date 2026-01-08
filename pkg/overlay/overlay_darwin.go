//go:build darwin

package overlay

/*
#cgo CFLAGS: -x objective-c -fmodules -fobjc-arc
#cgo LDFLAGS: -framework Cocoa -framework CoreGraphics -framework ApplicationServices

#import <Cocoa/Cocoa.h>
#import <CoreGraphics/CoreGraphics.h>
#import <ApplicationServices/ApplicationServices.h>
#include <stdio.h>
#include <pthread.h>

// Forward declarations for Go callbacks
void goOverlayButtonClicked(uint32_t windowID);
int goGetWindowState(uint32_t windowID);
int goIsManualMode(void);

// Window state constants
#define STATE_NOT_SELECTED 0
#define STATE_SELECTED 1
#define STATE_SHARING 2

// Global state
static NSWindow *g_overlayWindow = nil;
static NSView *g_buttonView = nil;
static NSTextField *g_label = nil;
static NSView *g_indicator = nil;
static uint32_t g_currentWindowID = 0;
static BOOL g_overlayEnabled = YES;
static BOOL g_initialized = NO;
static BOOL g_isHovered = NO;
static volatile BOOL g_shouldStop = NO;

// Event tap for click detection
static CFMachPortRef g_eventTap = NULL;
static CFRunLoopSourceRef g_eventTapSource = NULL;
static CFRunLoopRef g_tapRunLoop = NULL;

// Button dimensions
static const CGFloat kButtonWidth = 110.0;
static const CGFloat kButtonHeight = 32.0;
static const CGFloat kCornerRadius = 8.0;
static const CGFloat kMargin = 16.0;
static const CGFloat kIndicatorSize = 8.0;

static NSColor* overlayBackgroundColor(BOOL hovered) {
    if (hovered) {
        return [NSColor colorWithRed:0.24 green:0.24 blue:0.24 alpha:0.95];
    }
    return [NSColor colorWithRed:0.16 green:0.16 blue:0.16 alpha:0.85];
}

static NSColor* overlayTextColorForState(int state) {
    if (state == STATE_NOT_SELECTED) {
        return [NSColor colorWithRed:0.53 green:0.53 blue:0.53 alpha:1.0];
    }
    return [NSColor whiteColor];
}

static NSColor* overlayIndicatorColorForState(int state) {
    switch (state) {
        case STATE_SHARING:
            return [NSColor colorWithRed:1.0 green:0.23 blue:0.19 alpha:1.0];
        case STATE_SELECTED:
            return [NSColor colorWithRed:0.0 green:0.48 blue:1.0 alpha:1.0];
        default:
            return [NSColor colorWithRed:0.53 green:0.53 blue:0.53 alpha:1.0];
    }
}

static NSString* overlayLabelTextForState(int state) {
    switch (state) {
        case STATE_SHARING:
            return @"Sharing";
        case STATE_SELECTED:
            return @"Selected";
        default:
            return @"Share";
    }
}

// Get the focused window from the frontmost app (excluding terminals)
static BOOL getFocusedWindowInfo(uint32_t *outWindowID, CGRect *outBounds) {
    NSRunningApplication *frontApp = [[NSWorkspace sharedWorkspace] frontmostApplication];
    if (!frontApp) return NO;

    // Skip if Terminal is frontmost (where gopeep runs)
    NSString *bundleID = frontApp.bundleIdentifier;
    if ([bundleID isEqualToString:@"com.apple.Terminal"] ||
        [bundleID isEqualToString:@"com.googlecode.iterm2"] ||
        [bundleID isEqualToString:@"io.alacritty"] ||
        [bundleID isEqualToString:@"com.mitchellh.ghostty"] ||
        [bundleID isEqualToString:@"dev.warp.Warp-Stable"] ||
        [bundleID isEqualToString:@"co.zeit.hyper"]) {
        return NO;
    }

    pid_t frontPID = frontApp.processIdentifier;

    CFArrayRef windowList = CGWindowListCopyWindowInfo(
        kCGWindowListOptionOnScreenOnly | kCGWindowListExcludeDesktopElements,
        kCGNullWindowID
    );
    if (!windowList) return NO;

    BOOL found = NO;
    CFIndex count = CFArrayGetCount(windowList);

    for (CFIndex i = 0; i < count; i++) {
        NSDictionary *info = (__bridge NSDictionary *)CFArrayGetValueAtIndex(windowList, i);

        NSNumber *pidNum = info[(NSString *)kCGWindowOwnerPID];
        if (pidNum.intValue != frontPID) continue;

        NSNumber *layerNum = info[(NSString *)kCGWindowLayer];
        if (layerNum.intValue != 0) continue;

        NSNumber *windowIDNum = info[(NSString *)kCGWindowNumber];
        if (!windowIDNum) continue;

        NSDictionary *boundsDict = info[(NSString *)kCGWindowBounds];
        if (!boundsDict) continue;

        CGRect bounds;
        if (!CGRectMakeWithDictionaryRepresentation((__bridge CFDictionaryRef)boundsDict, &bounds)) continue;

        if (bounds.size.width < 100 || bounds.size.height < 100) continue;

        *outWindowID = windowIDNum.unsignedIntValue;
        *outBounds = bounds;
        found = YES;
        break;
    }

    CFRelease(windowList);
    return found;
}

static void updateButtonAppearance(int state, BOOL hovered) {
    if (!g_buttonView) return;

    // Update background for hover state
    g_buttonView.layer.backgroundColor = overlayBackgroundColor(hovered).CGColor;

    if (g_label) {
        g_label.stringValue = overlayLabelTextForState(state);
        g_label.textColor = overlayTextColorForState(state);
    }

    if (g_indicator) {
        g_indicator.layer.backgroundColor = overlayIndicatorColorForState(state).CGColor;
        g_indicator.layer.cornerRadius = (state == STATE_SHARING) ? 2.0 : kIndicatorSize / 2.0;
    }
}

// Check if mouse is over the overlay
static BOOL isMouseOverOverlay(void) {
    if (!g_overlayWindow || !g_overlayWindow.isVisible) return NO;

    CGPoint mousePos = CGEventGetLocation(CGEventCreate(NULL));
    NSScreen *mainScreen = [NSScreen mainScreen];
    CGFloat screenHeight = mainScreen.frame.size.height;
    NSPoint cocoaPoint = NSMakePoint(mousePos.x, screenHeight - mousePos.y);

    return NSPointInRect(cocoaPoint, g_overlayWindow.frame);
}

// Main update function - called from timer
static void doOverlayUpdate(void) {
    if (!g_overlayWindow || !g_initialized) {
        return;
    }

    if (!g_overlayEnabled) {
        if (g_overlayWindow.isVisible) {
            [g_overlayWindow orderOut:nil];
        }
        return;
    }

    int manualMode = goIsManualMode();
    if (!manualMode) {
        if (g_overlayWindow.isVisible) {
            [g_overlayWindow orderOut:nil];
        }
        return;
    }

    uint32_t windowID = 0;
    CGRect windowBounds = CGRectZero;

    if (!getFocusedWindowInfo(&windowID, &windowBounds)) {
        if (g_overlayWindow.isVisible) {
            [g_overlayWindow orderOut:nil];
        }
        g_currentWindowID = 0;
        return;
    }

    g_currentWindowID = windowID;

    // Check hover state
    BOOL nowHovered = isMouseOverOverlay();

    int state = goGetWindowState(windowID);
    updateButtonAppearance(state, nowHovered);
    g_isHovered = nowHovered;

    // Position at bottom-left of focused window
    // Convert from CG coords (origin top-left) to Cocoa coords (origin bottom-left)
    NSScreen *mainScreen = [NSScreen mainScreen];
    CGFloat screenHeight = mainScreen.frame.size.height;
    CGFloat windowBottom = screenHeight - (windowBounds.origin.y + windowBounds.size.height);
    CGFloat overlayX = windowBounds.origin.x + kMargin;
    CGFloat overlayY = windowBottom + kMargin;

    NSRect overlayFrame = NSMakeRect(overlayX, overlayY, kButtonWidth, kButtonHeight);

    [g_overlayWindow setFrame:overlayFrame display:YES];

    if (!g_overlayWindow.isVisible) {
        [g_overlayWindow orderFrontRegardless];
    }
}



// Event tap callback for mouse clicks
static CGEventRef mouseEventCallback(CGEventTapProxy proxy, CGEventType type, CGEventRef event, void *refcon) {
    if (type == kCGEventLeftMouseDown) {
        if (!g_overlayWindow || !g_overlayWindow.isVisible || g_currentWindowID == 0) {
            return event;
        }

        CGPoint clickPoint = CGEventGetLocation(event);

        // Convert to Cocoa coordinates
        NSScreen *mainScreen = [NSScreen mainScreen];
        CGFloat screenHeight = mainScreen.frame.size.height;
        NSPoint cocoaPoint = NSMakePoint(clickPoint.x, screenHeight - clickPoint.y);

        NSRect frame = g_overlayWindow.frame;

        if (NSPointInRect(cocoaPoint, frame)) {
            goOverlayButtonClicked(g_currentWindowID);
            // Return NULL to consume the event (don't pass through to windows behind)
            return NULL;
        }
    } else if (type == kCGEventTapDisabledByTimeout || type == kCGEventTapDisabledByUserInput) {
        if (g_eventTap) {
            CGEventTapEnable(g_eventTap, true);
        }
    }

    return event;
}

// Thread function to run event tap (for click detection only)
static void* eventTapThread(void* arg) {
    @autoreleasepool {
        g_tapRunLoop = CFRunLoopGetCurrent();

        // Add event tap source
        if (g_eventTapSource) {
            CFRunLoopAddSource(g_tapRunLoop, g_eventTapSource, kCFRunLoopCommonModes);
        }

        // Run until stopped
        while (!g_shouldStop) {
            @autoreleasepool {
                CFRunLoopRunInMode(kCFRunLoopDefaultMode, 0.1, false);
            }
        }

        // Cleanup
        if (g_eventTapSource) {
            CFRunLoopRemoveSource(g_tapRunLoop, g_eventTapSource, kCFRunLoopCommonModes);
        }

        g_tapRunLoop = NULL;
    }
    return NULL;
}

static void createOverlay(void) {
    if (g_initialized) return;

    @autoreleasepool {
        // Ensure NSApplication is initialized
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];

        // Create the overlay window
        NSRect frame = NSMakeRect(100, 100, kButtonWidth, kButtonHeight);
        g_overlayWindow = [[NSWindow alloc] initWithContentRect:frame
                                                      styleMask:NSWindowStyleMaskBorderless
                                                        backing:NSBackingStoreBuffered
                                                          defer:NO];

        g_overlayWindow.level = NSFloatingWindowLevel;
        g_overlayWindow.backgroundColor = [NSColor clearColor];
        g_overlayWindow.opaque = NO;
        g_overlayWindow.hasShadow = YES;
        g_overlayWindow.ignoresMouseEvents = YES; // We use event tap for clicks
        g_overlayWindow.collectionBehavior = NSWindowCollectionBehaviorCanJoinAllSpaces |
                                             NSWindowCollectionBehaviorStationary |
                                             NSWindowCollectionBehaviorFullScreenAuxiliary |
                                             NSWindowCollectionBehaviorIgnoresCycle;
        g_overlayWindow.alphaValue = 1.0;

        // Create button view
        g_buttonView = [[NSView alloc] initWithFrame:NSMakeRect(0, 0, kButtonWidth, kButtonHeight)];
        g_buttonView.wantsLayer = YES;
        g_buttonView.layer.cornerRadius = kCornerRadius;
        g_buttonView.layer.backgroundColor = overlayBackgroundColor(NO).CGColor;
        g_overlayWindow.contentView = g_buttonView;

        // Create indicator
        CGFloat indicatorX = 12.0;
        CGFloat indicatorY = (kButtonHeight - kIndicatorSize) / 2.0;
        g_indicator = [[NSView alloc] initWithFrame:NSMakeRect(indicatorX, indicatorY, kIndicatorSize, kIndicatorSize)];
        g_indicator.wantsLayer = YES;
        g_indicator.layer.cornerRadius = kIndicatorSize / 2.0;
        g_indicator.layer.backgroundColor = overlayIndicatorColorForState(STATE_NOT_SELECTED).CGColor;
        [g_buttonView addSubview:g_indicator];

        // Create label
        CGFloat labelX = indicatorX + kIndicatorSize + 8.0;
        CGFloat labelWidth = kButtonWidth - labelX - 12.0;
        g_label = [[NSTextField alloc] initWithFrame:NSMakeRect(labelX, 6, labelWidth, 20)];
        g_label.stringValue = @"Share";
        g_label.font = [NSFont systemFontOfSize:13 weight:NSFontWeightMedium];
        g_label.textColor = overlayTextColorForState(STATE_NOT_SELECTED);
        g_label.backgroundColor = [NSColor clearColor];
        g_label.bordered = NO;
        g_label.editable = NO;
        g_label.selectable = NO;
        [g_buttonView addSubview:g_label];

        // Create event tap for click detection
        // Try with Default option first (can consume events), fall back to ListenOnly
        CGEventMask eventMask = CGEventMaskBit(kCGEventLeftMouseDown);
        g_eventTap = CGEventTapCreate(
            kCGSessionEventTap,
            kCGHeadInsertEventTap,
            kCGEventTapOptionDefault,  // Can modify/consume events
            eventMask,
            mouseEventCallback,
            NULL
        );

        // Fallback to listen-only if default fails (less permissions required)
        if (!g_eventTap) {
            g_eventTap = CGEventTapCreate(
                kCGSessionEventTap,
                kCGHeadInsertEventTap,
                kCGEventTapOptionListenOnly,
                eventMask,
                mouseEventCallback,
                NULL
            );
        }

        if (g_eventTap) {
            g_eventTapSource = CFMachPortCreateRunLoopSource(kCFAllocatorDefault, g_eventTap, 0);
            if (g_eventTapSource) {
                CGEventTapEnable(g_eventTap, true);

                // Start event tap thread with timer
                g_shouldStop = NO;
                pthread_t tapThread;
                pthread_create(&tapThread, NULL, eventTapThread, NULL);
                pthread_detach(tapThread);
            } else {
                CFRelease(g_eventTap);
                g_eventTap = NULL;
            }
        }

        g_initialized = YES;

        // Initial display
        [g_overlayWindow display];
        [[NSRunLoop currentRunLoop] runUntilDate:[NSDate dateWithTimeIntervalSinceNow:0.05]];
    }
}

static void destroyOverlay(void) {
    g_shouldStop = YES;

    // Give thread time to stop
    usleep(200000); // 200ms

    // Stop event tap thread
    if (g_tapRunLoop) {
        CFRunLoopStop(g_tapRunLoop);
        g_tapRunLoop = NULL;
    }

    // Clean up event tap
    if (g_eventTapSource) {
        CFRelease(g_eventTapSource);
        g_eventTapSource = NULL;
    }
    if (g_eventTap) {
        CFRelease(g_eventTap);
        g_eventTap = NULL;
    }

    // Clean up window
    if (g_overlayWindow) {
        [g_overlayWindow orderOut:nil];
        g_overlayWindow = nil;
    }
    g_buttonView = nil;
    g_label = nil;
    g_indicator = nil;
    g_initialized = NO;
}

static void setOverlayEnabled(BOOL enabled) {
    g_overlayEnabled = enabled;
}

// Called from Go to update overlay and pump run loop
static void updateAndPump(void) {
    @autoreleasepool {
        doOverlayUpdate();
        // Brief run loop pump to process UI updates
        [[NSRunLoop currentRunLoop] runUntilDate:[NSDate dateWithTimeIntervalSinceNow:0.001]];
    }
}

*/
import "C"
import (
	"runtime"
	"sync"
	"time"
)

var (
	globalOverlay *Overlay
	globalMu      sync.RWMutex
	started       bool
	lastClickTime time.Time
)

//export goOverlayButtonClicked
func goOverlayButtonClicked(windowID C.uint32_t) {
	// Debounce clicks
	if time.Since(lastClickTime) < 300*time.Millisecond {
		return
	}
	lastClickTime = time.Now()

	globalMu.RLock()
	o := globalOverlay
	globalMu.RUnlock()

	if o != nil {
		o.sendEvent(Event{
			Type:     EventToggleSelection,
			WindowID: uint32(windowID),
		})
	}
}

//export goGetWindowState
func goGetWindowState(windowID C.uint32_t) C.int {
	globalMu.RLock()
	o := globalOverlay
	globalMu.RUnlock()

	if o == nil || o.controller == nil {
		return C.int(StateNotSelected)
	}

	state := o.controller.GetWindowState(uint32(windowID))
	return C.int(state)
}

//export goIsManualMode
func goIsManualMode() C.int {
	globalMu.RLock()
	o := globalOverlay
	globalMu.RUnlock()

	if o == nil || o.controller == nil {
		return 0
	}

	if o.controller.IsManualMode() {
		return 1
	}
	return 0
}

// platformStart initializes the macOS overlay.
func (o *Overlay) platformStart() error {
	globalMu.Lock()
	globalOverlay = o
	globalMu.Unlock()

	// Create overlay on main thread
	runtime.LockOSThread()
	C.createOverlay()
	started = true

	return nil
}

// platformStop cleans up the macOS overlay.
func (o *Overlay) platformStop() {
	if started {
		C.destroyOverlay()
		started = false
	}

	globalMu.Lock()
	globalOverlay = nil
	globalMu.Unlock()
}

// platformSetEnabled enables/disables the overlay visibility.
func (o *Overlay) platformSetEnabled(enabled bool) {
	if started {
		C.setOverlayEnabled(C.BOOL(enabled))
	}
}

// platformRefresh updates the overlay position and pumps the run loop.
func (o *Overlay) platformRefresh(windowID uint32, x, y, width, height float64) {
	if started {
		C.updateAndPump()
	}
}
