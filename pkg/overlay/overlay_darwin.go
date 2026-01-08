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
static NSTextField *g_arrowLabel = nil;
static uint32_t g_currentWindowID = 0;
static BOOL g_overlayEnabled = YES;
static BOOL g_initialized = NO;
static BOOL g_isHovered = NO;
static BOOL g_isArrowHovered = NO;
static BOOL g_positionedRight = NO;  // false = left corner, true = right corner
static volatile BOOL g_shouldStop = NO;

// Event tap for click and mouse move detection
static CFMachPortRef g_eventTap = NULL;
static CFRunLoopSourceRef g_eventTapSource = NULL;
static CFRunLoopRef g_tapRunLoop = NULL;

// Cached window bounds for positioning
static CGRect g_lastWindowBounds = {0};

// Button dimensions
static const CGFloat kButtonWidth = 130.0;  // Slightly wider for arrow
static const CGFloat kButtonHeight = 32.0;
static const CGFloat kCornerRadius = 8.0;
static const CGFloat kMargin = 16.0;
static const CGFloat kIndicatorSize = 8.0;
static const CGFloat kArrowWidth = 20.0;

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

static void updateButtonAppearance(int state, BOOL hovered, BOOL arrowHovered) {
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

    if (g_arrowLabel) {
        // Update arrow direction based on position
        g_arrowLabel.stringValue = g_positionedRight ? @"←" : @"→";
        // Highlight arrow when hovered
        if (arrowHovered) {
            g_arrowLabel.textColor = [NSColor whiteColor];
        } else {
            g_arrowLabel.textColor = [NSColor colorWithRed:0.5 green:0.5 blue:0.5 alpha:1.0];
        }
    }
}

// Check if a point is over the overlay
static BOOL isPointOverOverlay(CGPoint cgPoint) {
    if (!g_overlayWindow || !g_overlayWindow.isVisible) return NO;

    NSScreen *mainScreen = [NSScreen mainScreen];
    CGFloat screenHeight = mainScreen.frame.size.height;
    NSPoint cocoaPoint = NSMakePoint(cgPoint.x, screenHeight - cgPoint.y);

    return NSPointInRect(cocoaPoint, g_overlayWindow.frame);
}

// Check if a point is over the arrow area
static BOOL isPointOverArrow(CGPoint cgPoint) {
    if (!g_overlayWindow || !g_overlayWindow.isVisible) return NO;

    NSScreen *mainScreen = [NSScreen mainScreen];
    CGFloat screenHeight = mainScreen.frame.size.height;
    NSPoint cocoaPoint = NSMakePoint(cgPoint.x, screenHeight - cgPoint.y);

    NSRect frame = g_overlayWindow.frame;
    NSRect arrowRect = NSMakeRect(frame.origin.x + frame.size.width - kArrowWidth - 4,
                                   frame.origin.y,
                                   kArrowWidth + 4,
                                   frame.size.height);

    return NSPointInRect(cocoaPoint, arrowRect);
}

// Main update function - called from Go during tick
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
    g_lastWindowBounds = windowBounds;

    // Check hover state
    CGPoint mousePos = CGEventGetLocation(CGEventCreate(NULL));
    BOOL nowHovered = isPointOverOverlay(mousePos);
    BOOL arrowHovered = isPointOverArrow(mousePos);

    int state = goGetWindowState(windowID);
    updateButtonAppearance(state, nowHovered, arrowHovered);
    g_isHovered = nowHovered;
    g_isArrowHovered = arrowHovered;

    // Position at bottom corner of focused window
    // Convert from CG coords (origin top-left) to Cocoa coords (origin bottom-left)
    NSScreen *mainScreen = [NSScreen mainScreen];
    CGFloat screenHeight = mainScreen.frame.size.height;
    CGFloat windowBottom = screenHeight - (windowBounds.origin.y + windowBounds.size.height);

    CGFloat overlayX;
    if (g_positionedRight) {
        // Right corner
        overlayX = windowBounds.origin.x + windowBounds.size.width - kButtonWidth - kMargin;
    } else {
        // Left corner
        overlayX = windowBounds.origin.x + kMargin;
    }
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

        if (isPointOverOverlay(clickPoint)) {
            if (isPointOverArrow(clickPoint)) {
                // Toggle position
                g_positionedRight = !g_positionedRight;
            } else {
                // Toggle selection
                goOverlayButtonClicked(g_currentWindowID);
            }
            // Consume the click
            return NULL;
        }
    } else if (type == kCGEventTapDisabledByTimeout || type == kCGEventTapDisabledByUserInput) {
        if (g_eventTap) {
            CGEventTapEnable(g_eventTap, true);
        }
    }

    return event;
}

// Thread function to run event tap
static void* eventTapThread(void* arg) {
    @autoreleasepool {
        g_tapRunLoop = CFRunLoopGetCurrent();

        if (g_eventTapSource) {
            CFRunLoopAddSource(g_tapRunLoop, g_eventTapSource, kCFRunLoopCommonModes);
        }

        while (!g_shouldStop) {
            @autoreleasepool {
                CFRunLoopRunInMode(kCFRunLoopDefaultMode, 0.1, false);
            }
        }

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
        g_overlayWindow.ignoresMouseEvents = NO; // Window captures mouse to prevent passthrough
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

        // Create label - vertically centered
        CGFloat labelX = indicatorX + kIndicatorSize + 8.0;
        CGFloat labelWidth = kButtonWidth - labelX - kArrowWidth - 8.0;
        CGFloat labelHeight = 18.0;
        CGFloat labelY = (kButtonHeight - labelHeight) / 2.0;
        g_label = [[NSTextField alloc] initWithFrame:NSMakeRect(labelX, labelY, labelWidth, labelHeight)];
        g_label.stringValue = @"Share";
        g_label.font = [NSFont systemFontOfSize:13 weight:NSFontWeightMedium];
        g_label.textColor = overlayTextColorForState(STATE_NOT_SELECTED);
        g_label.backgroundColor = [NSColor clearColor];
        g_label.bordered = NO;
        g_label.editable = NO;
        g_label.selectable = NO;
        [g_buttonView addSubview:g_label];

        // Create arrow label for repositioning - vertically centered
        CGFloat arrowX = kButtonWidth - kArrowWidth - 6.0;
        CGFloat arrowHeight = 18.0;
        CGFloat arrowY = (kButtonHeight - arrowHeight) / 2.0;
        g_arrowLabel = [[NSTextField alloc] initWithFrame:NSMakeRect(arrowX, arrowY, kArrowWidth, arrowHeight)];
        g_arrowLabel.stringValue = @"→";
        g_arrowLabel.font = [NSFont systemFontOfSize:14 weight:NSFontWeightMedium];
        g_arrowLabel.textColor = [NSColor colorWithRed:0.5 green:0.5 blue:0.5 alpha:1.0];
        g_arrowLabel.backgroundColor = [NSColor clearColor];
        g_arrowLabel.bordered = NO;
        g_arrowLabel.editable = NO;
        g_arrowLabel.selectable = NO;
        g_arrowLabel.alignment = NSTextAlignmentCenter;
        [g_buttonView addSubview:g_arrowLabel];

        // Create event tap for click detection only
        // Mouse hover is blocked by the window itself (ignoresMouseEvents = NO)
        CGEventMask eventMask = CGEventMaskBit(kCGEventLeftMouseDown);
        g_eventTap = CGEventTapCreate(
            kCGSessionEventTap,
            kCGHeadInsertEventTap,
            kCGEventTapOptionDefault,  // Can modify/consume events
            eventMask,
            mouseEventCallback,
            NULL
        );

        // Fallback to listen-only if default fails
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
    usleep(200000);

    if (g_tapRunLoop) {
        CFRunLoopStop(g_tapRunLoop);
        g_tapRunLoop = NULL;
    }

    if (g_eventTapSource) {
        CFRelease(g_eventTapSource);
        g_eventTapSource = NULL;
    }
    if (g_eventTap) {
        CFRelease(g_eventTap);
        g_eventTap = NULL;
    }

    if (g_overlayWindow) {
        [g_overlayWindow orderOut:nil];
        g_overlayWindow = nil;
    }
    g_buttonView = nil;
    g_label = nil;
    g_indicator = nil;
    g_arrowLabel = nil;
    g_initialized = NO;
}

static void setOverlayEnabled(BOOL enabled) {
    g_overlayEnabled = enabled;
}

// Called from Go to update overlay and pump run loop
static void updateAndPump(void) {
    @autoreleasepool {
        doOverlayUpdate();
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
