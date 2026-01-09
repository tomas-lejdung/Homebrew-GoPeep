//go:build darwin

package overlay

/*
#cgo CFLAGS: -x objective-c -fmodules -fobjc-arc
#cgo LDFLAGS: -framework Cocoa -framework CoreGraphics -framework ApplicationServices -framework QuartzCore

#import <Cocoa/Cocoa.h>
#import <CoreGraphics/CoreGraphics.h>
#import <ApplicationServices/ApplicationServices.h>
#import <QuartzCore/QuartzCore.h>
#include <stdio.h>
#include <pthread.h>
#include <stdatomic.h>

// Forward declarations for Go callbacks
void goOverlayButtonClicked(uint32_t windowID);
int goGetWindowState(uint32_t windowID);
int goIsManualMode(void);
int goGetFocusedWindow(uint32_t *outWindowID, double *outX, double *outY, double *outW, double *outH);
int goGetSelectedWindowCount(void);
int goIsSharing(void);

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

// Animation state (updated every frame by game loop)
// g_isAnimating is atomic to synchronize between event tap thread and game loop thread
static _Atomic BOOL g_isAnimating = NO;
static CGFloat g_animStartX = 0;
static CGFloat g_animEndX = 0;
static CFAbsoluteTime g_animStartTime = 0;
static const CFTimeInterval kAnimDuration = 0.25;  // 250ms animation

// State change tracking for pulse effect
static int g_lastState = -1;  // -1 = uninitialized

// Event tap for click detection
static CFMachPortRef g_eventTap = NULL;
static CFRunLoopSourceRef g_eventTapSource = NULL;
static CFRunLoopRef g_tapRunLoop = NULL;

// Cached window bounds for positioning
static CGRect g_lastWindowBounds = {0};

// Corner status indicator (shows when sharing, regardless of focus)
static NSWindow *g_statusWindow = nil;
static NSView *g_statusView = nil;
static NSView *g_statusDots[4] = {nil, nil, nil, nil};
static int g_lastSelectedCount = -1;  // For animation on count change

// Button dimensions
static const CGFloat kButtonWidth = 130.0;
static const CGFloat kButtonHeight = 32.0;
static const CGFloat kCornerRadius = 8.0;
static const CGFloat kMargin = 16.0;
static const CGFloat kIndicatorSize = 8.0;
static const CGFloat kArrowWidth = 20.0;

// Status indicator dimensions
static const CGFloat kStatusDotSize = 10.0;
static const CGFloat kStatusDotSpacing = 4.0;
static const CGFloat kStatusPadding = 8.0;
static const CGFloat kStatusCornerMargin = 20.0;

static NSColor* overlayBackgroundColor(BOOL hovered) {
    if (hovered) {
        // Brighter background on hover for more visible feedback
        return [NSColor colorWithRed:0.32 green:0.32 blue:0.34 alpha:0.98];
    }
    return [NSColor colorWithRed:0.16 green:0.16 blue:0.16 alpha:0.85];
}

static NSColor* overlayTextColorForState(int state, BOOL hovered) {
    if (hovered) {
        // White text on hover for all states
        return [NSColor whiteColor];
    }
    if (state == STATE_NOT_SELECTED) {
        return [NSColor colorWithRed:0.53 green:0.53 blue:0.53 alpha:1.0];
    }
    return [NSColor whiteColor];
}

static NSColor* overlayIndicatorColorForState(int state, BOOL hovered) {
    NSColor *baseColor;
    switch (state) {
        case STATE_SHARING:
            baseColor = [NSColor colorWithRed:1.0 green:0.23 blue:0.19 alpha:1.0];
            break;
        case STATE_SELECTED:
            baseColor = [NSColor colorWithRed:0.0 green:0.48 blue:1.0 alpha:1.0];
            break;
        default:
            baseColor = [NSColor colorWithRed:0.53 green:0.53 blue:0.53 alpha:1.0];
            break;
    }

    if (hovered && state == STATE_NOT_SELECTED) {
        // Brighten the gray indicator on hover
        return [NSColor colorWithRed:0.7 green:0.7 blue:0.7 alpha:1.0];
    }
    return baseColor;
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

// Get the focused window via Go callback (uses same detection as TUI)
static BOOL getFocusedWindowInfo(uint32_t *outWindowID, CGRect *outBounds) {
    double x, y, w, h;
    if (!goGetFocusedWindow(outWindowID, &x, &y, &w, &h)) {
        return NO;
    }
    *outBounds = CGRectMake(x, y, w, h);
    return YES;
}

static void updateButtonAppearance(int state, BOOL hovered, BOOL arrowHovered) {
    if (!g_buttonView) return;

    g_buttonView.layer.backgroundColor = overlayBackgroundColor(hovered).CGColor;

    if (g_label) {
        g_label.stringValue = overlayLabelTextForState(state);
        g_label.textColor = overlayTextColorForState(state, hovered);
    }

    if (g_indicator) {
        g_indicator.layer.backgroundColor = overlayIndicatorColorForState(state, hovered).CGColor;
        g_indicator.layer.cornerRadius = (state == STATE_SHARING) ? 2.0 : kIndicatorSize / 2.0;
    }

    if (g_arrowLabel) {
        g_arrowLabel.stringValue = g_positionedRight ? @"←" : @"→";
        if (arrowHovered || hovered) {
            // Arrow brightens when hovering anywhere on the button
            g_arrowLabel.textColor = [NSColor colorWithRed:0.7 green:0.7 blue:0.7 alpha:1.0];
        } else {
            g_arrowLabel.textColor = [NSColor colorWithRed:0.5 green:0.5 blue:0.5 alpha:1.0];
        }
        // Extra bright when directly hovering the arrow
        if (arrowHovered) {
            g_arrowLabel.textColor = [NSColor whiteColor];
        }
    }
}

// Ease-in-out curve for smooth animation
static CGFloat easeInOutQuad(CGFloat t) {
    return t < 0.5 ? 2.0 * t * t : 1.0 - pow(-2.0 * t + 2.0, 2.0) / 2.0;
}

// Pulse animation for state changes - gives visual feedback
// Pulses the indicator dot to draw attention to the state change
static void pulseOverlay(void) {
    if (!g_indicator || !g_indicator.layer) return;

    // Ensure anchor point is centered so scale grows from center
    CGRect bounds = g_indicator.layer.bounds;
    g_indicator.layer.anchorPoint = CGPointMake(0.5, 0.5);
    g_indicator.layer.position = CGPointMake(
        g_indicator.frame.origin.x + bounds.size.width / 2.0,
        g_indicator.frame.origin.y + bounds.size.height / 2.0
    );

    // Scale the indicator dot - it's small (8x8) so clipping is minimal
    CAKeyframeAnimation *pulse = [CAKeyframeAnimation animationWithKeyPath:@"transform.scale"];
    pulse.values = @[@1.0, @1.6, @1.0];
    pulse.keyTimes = @[@0.0, @0.35, @1.0];
    pulse.duration = 0.35;
    pulse.timingFunctions = @[
        [CAMediaTimingFunction functionWithName:kCAMediaTimingFunctionEaseOut],
        [CAMediaTimingFunction functionWithName:kCAMediaTimingFunctionEaseIn]
    ];
    [g_indicator.layer addAnimation:pulse forKey:@"pulse"];
}

// Update status indicator dots based on selected count
static void updateStatusIndicator(int selectedCount, BOOL isSharing) {
    if (!g_statusWindow) return;

    // Red color for sharing dots
    NSColor *filledColor = [NSColor colorWithRed:1.0 green:0.23 blue:0.19 alpha:1.0];
    // Gray outline for empty slots
    NSColor *emptyBorderColor = [NSColor colorWithRed:0.4 green:0.4 blue:0.4 alpha:1.0];

    for (int i = 0; i < 4; i++) {
        if (!g_statusDots[i]) continue;

        if (i < selectedCount) {
            // Filled dot (sharing)
            g_statusDots[i].layer.backgroundColor = filledColor.CGColor;
            g_statusDots[i].layer.borderWidth = 0;
        } else {
            // Empty slot (outline only)
            g_statusDots[i].layer.backgroundColor = [NSColor clearColor].CGColor;
            g_statusDots[i].layer.borderColor = emptyBorderColor.CGColor;
            g_statusDots[i].layer.borderWidth = 1.5;
        }
    }

    // Pulse animation when count changes
    if (g_lastSelectedCount != -1 && selectedCount != g_lastSelectedCount && selectedCount > 0) {
        // Animate the dot that just changed (either new or removed)
        int changedDot = (selectedCount > g_lastSelectedCount) ? (selectedCount - 1) : g_lastSelectedCount - 1;
        if (changedDot >= 0 && changedDot < 4 && g_statusDots[changedDot]) {
            CAKeyframeAnimation *pulse = [CAKeyframeAnimation animationWithKeyPath:@"transform.scale"];
            pulse.values = @[@1.0, @1.3, @1.0];
            pulse.duration = 0.2;
            pulse.timingFunctions = @[
                [CAMediaTimingFunction functionWithName:kCAMediaTimingFunctionEaseOut],
                [CAMediaTimingFunction functionWithName:kCAMediaTimingFunctionEaseIn]
            ];
            [g_statusDots[changedDot].layer addAnimation:pulse forKey:@"pulse"];
        }
    }
    g_lastSelectedCount = selectedCount;
}

// Start animation to opposite corner (called from click handler)
static void startCornerAnimation(BOOL toRight) {
    if (!g_overlayWindow || !g_initialized) return;

    // Get current X position
    g_animStartX = g_overlayWindow.frame.origin.x;

    // Calculate target X position
    if (toRight) {
        g_animEndX = g_lastWindowBounds.origin.x + g_lastWindowBounds.size.width - kButtonWidth - kMargin;
    } else {
        g_animEndX = g_lastWindowBounds.origin.x + kMargin;
    }

    // Update position flag and arrow appearance immediately
    g_positionedRight = toRight;
    if (g_arrowLabel) {
        g_arrowLabel.stringValue = toRight ? @"←" : @"→";
    }

    // Start animation (game loop will handle the rest)
    g_animStartTime = CFAbsoluteTimeGetCurrent();
    g_isAnimating = YES;
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

// Update status indicator visibility and content
static void updateStatusWindow(void) {
    if (!g_statusWindow) return;

    int isSharing = goIsSharing();
    int selectedCount = goGetSelectedWindowCount();

    if (isSharing && selectedCount > 0) {
        updateStatusIndicator(selectedCount, YES);
        if (!g_statusWindow.isVisible) {
            [g_statusWindow orderFrontRegardless];
        }
    } else {
        if (g_statusWindow.isVisible) {
            [g_statusWindow orderOut:nil];
        }
        g_lastSelectedCount = -1;  // Reset for next time
    }
}

// Main frame update - called by game loop at 60fps
// Note: NSWindow was created on main thread, but most operations can be
// called from other threads if done carefully. The game loop thread is
// locked with runtime.LockOSThread() for consistency.
static void doFrame(void) {
    if (!g_overlayWindow || !g_initialized) {
        return;
    }

    // Always update status indicator (shows when sharing, independent of focus)
    updateStatusWindow();

    // Early exit if disabled
    if (!g_overlayEnabled) {
        if (g_overlayWindow.isVisible) {
            [g_overlayWindow orderOut:nil];
        }
        return;
    }

    // Early exit if not in manual mode
    int manualMode = goIsManualMode();
    if (!manualMode) {
        if (g_overlayWindow.isVisible) {
            [g_overlayWindow orderOut:nil];
        }
        return;
    }

    // Get focused window via Go callback (uses same detection as TUI)
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

    // Check hover state (must release the CGEventRef to avoid memory leak)
    CGEventRef mouseEvent = CGEventCreate(NULL);
    CGPoint mousePos = CGPointZero;
    if (mouseEvent) {
        mousePos = CGEventGetLocation(mouseEvent);
        CFRelease(mouseEvent);
    }
    BOOL nowHovered = isPointOverOverlay(mousePos);
    BOOL arrowHovered = isPointOverArrow(mousePos);

    // Update appearance
    int state = goGetWindowState(windowID);

    // Pulse animation on state change (but not on first frame)
    if (g_lastState != -1 && state != g_lastState) {
        pulseOverlay();
    }
    g_lastState = state;

    updateButtonAppearance(state, nowHovered, arrowHovered);
    g_isHovered = nowHovered;
    g_isArrowHovered = arrowHovered;

    // Calculate position
    NSScreen *mainScreen = [NSScreen mainScreen];
    CGFloat screenHeight = mainScreen.frame.size.height;
    CGFloat windowBottom = screenHeight - (windowBounds.origin.y + windowBounds.size.height);
    CGFloat overlayY = windowBottom + kMargin;

    CGFloat overlayX;

    // Handle animation
    if (g_isAnimating) {
        CFAbsoluteTime elapsed = CFAbsoluteTimeGetCurrent() - g_animStartTime;
        CGFloat progress = elapsed / kAnimDuration;

        if (progress >= 1.0) {
            progress = 1.0;
            g_isAnimating = NO;
        }

        CGFloat easedProgress = easeInOutQuad(progress);
        overlayX = g_animStartX + (g_animEndX - g_animStartX) * easedProgress;
    } else {
        // Normal positioning
        if (g_positionedRight) {
            overlayX = windowBounds.origin.x + windowBounds.size.width - kButtonWidth - kMargin;
        } else {
            overlayX = windowBounds.origin.x + kMargin;
        }
    }

    NSRect overlayFrame = NSMakeRect(overlayX, overlayY, kButtonWidth, kButtonHeight);
    [g_overlayWindow setFrame:overlayFrame display:YES];

    if (!g_overlayWindow.isVisible) {
        [g_overlayWindow orderFrontRegardless];
    }

    // Pump run loop briefly to process events
    [[NSRunLoop currentRunLoop] runUntilDate:[NSDate dateWithTimeIntervalSinceNow:0.001]];
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
                // Start animation to opposite corner
                startCornerAnimation(!g_positionedRight);
            } else {
                // Toggle selection
                goOverlayButtonClicked(g_currentWindowID);
            }
            return NULL; // Consume the click
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
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];

        // Check if we're on the main thread - NSWindow requires main thread
        if (![NSThread isMainThread]) {
            NSLog(@"Warning: createOverlay called from non-main thread, overlay will be disabled");
            return;
        }

        @try {
            NSRect frame = NSMakeRect(100, 100, kButtonWidth, kButtonHeight);
            g_overlayWindow = [[NSWindow alloc] initWithContentRect:frame
                                                          styleMask:NSWindowStyleMaskBorderless
                                                            backing:NSBackingStoreBuffered
                                                              defer:NO];
        } @catch (NSException *exception) {
            NSLog(@"Failed to create overlay window: %@", exception);
            g_overlayWindow = nil;
            return;
        }

        g_overlayWindow.level = NSFloatingWindowLevel;
        g_overlayWindow.backgroundColor = [NSColor clearColor];
        g_overlayWindow.opaque = NO;
        g_overlayWindow.hasShadow = YES;
        g_overlayWindow.ignoresMouseEvents = NO;
        g_overlayWindow.collectionBehavior = NSWindowCollectionBehaviorCanJoinAllSpaces |
                                             NSWindowCollectionBehaviorStationary |
                                             NSWindowCollectionBehaviorFullScreenAuxiliary |
                                             NSWindowCollectionBehaviorIgnoresCycle;
        g_overlayWindow.alphaValue = 1.0;

        g_buttonView = [[NSView alloc] initWithFrame:NSMakeRect(0, 0, kButtonWidth, kButtonHeight)];
        g_buttonView.wantsLayer = YES;
        g_buttonView.layer.cornerRadius = kCornerRadius;
        g_buttonView.layer.backgroundColor = overlayBackgroundColor(NO).CGColor;
        g_overlayWindow.contentView = g_buttonView;

        CGFloat indicatorX = 12.0;
        CGFloat indicatorY = (kButtonHeight - kIndicatorSize) / 2.0;
        g_indicator = [[NSView alloc] initWithFrame:NSMakeRect(indicatorX, indicatorY, kIndicatorSize, kIndicatorSize)];
        g_indicator.wantsLayer = YES;
        g_indicator.layer.cornerRadius = kIndicatorSize / 2.0;
        g_indicator.layer.backgroundColor = overlayIndicatorColorForState(STATE_NOT_SELECTED, NO).CGColor;
        [g_buttonView addSubview:g_indicator];

        CGFloat labelX = indicatorX + kIndicatorSize + 8.0;
        CGFloat labelWidth = kButtonWidth - labelX - kArrowWidth - 8.0;
        CGFloat labelHeight = 18.0;
        CGFloat labelY = (kButtonHeight - labelHeight) / 2.0;
        g_label = [[NSTextField alloc] initWithFrame:NSMakeRect(labelX, labelY, labelWidth, labelHeight)];
        g_label.stringValue = @"Share";
        g_label.font = [NSFont systemFontOfSize:13 weight:NSFontWeightMedium];
        g_label.textColor = overlayTextColorForState(STATE_NOT_SELECTED, NO);
        g_label.backgroundColor = [NSColor clearColor];
        g_label.bordered = NO;
        g_label.editable = NO;
        g_label.selectable = NO;
        [g_buttonView addSubview:g_label];

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

        // Create status indicator window (top-right corner, shows when sharing)
        CGFloat statusWidth = kStatusPadding * 2 + kStatusDotSize * 4 + kStatusDotSpacing * 3;
        CGFloat statusHeight = kStatusPadding * 2 + kStatusDotSize;
        NSScreen *mainScreen = [NSScreen mainScreen];
        CGFloat statusX = mainScreen.frame.size.width - statusWidth - kStatusCornerMargin;
        CGFloat statusY = mainScreen.frame.size.height - statusHeight - kStatusCornerMargin - 25; // Below menu bar

        NSRect statusFrame = NSMakeRect(statusX, statusY, statusWidth, statusHeight);
        g_statusWindow = [[NSWindow alloc] initWithContentRect:statusFrame
                                                      styleMask:NSWindowStyleMaskBorderless
                                                        backing:NSBackingStoreBuffered
                                                          defer:NO];

        g_statusWindow.level = NSFloatingWindowLevel;
        g_statusWindow.backgroundColor = [NSColor clearColor];
        g_statusWindow.opaque = NO;
        g_statusWindow.hasShadow = YES;
        g_statusWindow.ignoresMouseEvents = YES;  // Non-interactive
        g_statusWindow.collectionBehavior = NSWindowCollectionBehaviorCanJoinAllSpaces |
                                            NSWindowCollectionBehaviorStationary |
                                            NSWindowCollectionBehaviorFullScreenAuxiliary |
                                            NSWindowCollectionBehaviorIgnoresCycle;
        g_statusWindow.alphaValue = 1.0;

        g_statusView = [[NSView alloc] initWithFrame:NSMakeRect(0, 0, statusWidth, statusHeight)];
        g_statusView.wantsLayer = YES;
        g_statusView.layer.cornerRadius = statusHeight / 2.0;  // Pill shape
        g_statusView.layer.backgroundColor = [NSColor colorWithRed:0.0 green:0.0 blue:0.0 alpha:0.7].CGColor;
        g_statusWindow.contentView = g_statusView;

        // Create 4 dots
        for (int i = 0; i < 4; i++) {
            CGFloat dotX = kStatusPadding + i * (kStatusDotSize + kStatusDotSpacing);
            CGFloat dotY = kStatusPadding;
            g_statusDots[i] = [[NSView alloc] initWithFrame:NSMakeRect(dotX, dotY, kStatusDotSize, kStatusDotSize)];
            g_statusDots[i].wantsLayer = YES;
            g_statusDots[i].layer.cornerRadius = kStatusDotSize / 2.0;
            // Initial state: empty outline
            g_statusDots[i].layer.backgroundColor = [NSColor clearColor].CGColor;
            g_statusDots[i].layer.borderColor = [NSColor colorWithRed:0.4 green:0.4 blue:0.4 alpha:1.0].CGColor;
            g_statusDots[i].layer.borderWidth = 1.5;
            [g_statusView addSubview:g_statusDots[i]];
        }

        CGEventMask eventMask = CGEventMaskBit(kCGEventLeftMouseDown);
        g_eventTap = CGEventTapCreate(
            kCGSessionEventTap,
            kCGHeadInsertEventTap,
            kCGEventTapOptionDefault,
            eventMask,
            mouseEventCallback,
            NULL
        );

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
        [g_overlayWindow display];
    }
}

static void destroyOverlay(void) {
    g_shouldStop = YES;
    g_isAnimating = NO;

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

    // Clean up status indicator
    if (g_statusWindow) {
        [g_statusWindow orderOut:nil];
        g_statusWindow = nil;
    }
    g_statusView = nil;
    for (int i = 0; i < 4; i++) {
        g_statusDots[i] = nil;
    }
    g_lastSelectedCount = -1;

    g_initialized = NO;
}

static void setOverlayEnabled(BOOL enabled) {
    g_overlayEnabled = enabled;
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
	lastClickTime time.Time
)

//export goOverlayButtonClicked
func goOverlayButtonClicked(windowID C.uint32_t) {
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

//export goGetFocusedWindow
func goGetFocusedWindow(outWindowID *C.uint32_t, outX, outY, outW, outH *C.double) C.int {
	globalMu.RLock()
	o := globalOverlay
	globalMu.RUnlock()

	if o == nil || o.controller == nil {
		return 0
	}

	info := o.controller.GetFocusedWindow()
	if info == nil {
		return 0
	}

	*outWindowID = C.uint32_t(info.WindowID)
	*outX = C.double(info.X)
	*outY = C.double(info.Y)
	*outW = C.double(info.Width)
	*outH = C.double(info.Height)
	return 1
}

//export goGetSelectedWindowCount
func goGetSelectedWindowCount() C.int {
	globalMu.RLock()
	o := globalOverlay
	globalMu.RUnlock()

	if o == nil || o.controller == nil {
		return 0
	}

	return C.int(o.controller.GetSelectedWindowCount())
}

//export goIsSharing
func goIsSharing() C.int {
	globalMu.RLock()
	o := globalOverlay
	globalMu.RUnlock()

	if o == nil || o.controller == nil {
		return 0
	}

	if o.controller.IsSharing() {
		return 1
	}
	return 0
}

// runLoop is the 60fps game loop for the overlay.
// It runs in its own goroutine, locked to an OS thread for Cocoa compatibility.
// The overlay is created on this thread - if it happens to be the main thread,
// NSWindow will work. Otherwise, the overlay gracefully degrades.
func (o *Overlay) runLoop() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Create overlay on this thread
	C.createOverlay()

	// Signal that we're ready
	o.ready <- struct{}{}

	const targetFrameTime = time.Second / 60 // ~16.67ms

	for o.running.Load() {
		frameStart := time.Now()

		// Update overlay (C call)
		C.doFrame()

		// Sleep remaining time to maintain 60fps
		elapsed := time.Since(frameStart)
		if elapsed < targetFrameTime {
			time.Sleep(targetFrameTime - elapsed)
		}
	}

	// Destroy overlay on the same thread it was created
	C.destroyOverlay()

	o.stopped <- struct{}{}
}

// platformStart initializes the macOS overlay and starts the game loop.
func (o *Overlay) platformStart() error {
	globalMu.Lock()
	globalOverlay = o
	globalMu.Unlock()

	// Start the game loop in a separate goroutine
	o.running.Store(true)
	o.ready = make(chan struct{})
	o.stopped = make(chan struct{})
	go o.runLoop()

	// Wait for overlay to be created (or fail gracefully)
	<-o.ready

	return nil
}

// platformStop cleans up the macOS overlay and stops the game loop.
func (o *Overlay) platformStop() {
	if !o.running.Load() {
		return
	}

	// Signal loop to stop
	o.running.Store(false)

	// Wait for loop to exit (it will destroy the overlay)
	<-o.stopped

	globalMu.Lock()
	globalOverlay = nil
	globalMu.Unlock()
}

// platformSetEnabled enables/disables the overlay visibility.
func (o *Overlay) platformSetEnabled(enabled bool) {
	C.setOverlayEnabled(C.BOOL(enabled))
}
