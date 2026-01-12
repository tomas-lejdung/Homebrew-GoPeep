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
void goFullscreenButtonClicked(void);
void goClearButtonClicked(void);
int goGetWindowState(uint32_t windowID);
int goIsManualMode(void);
int goGetFocusedWindow(uint32_t *outWindowID, double *outX, double *outY, double *outW, double *outH);
int goGetSelectedWindowCount(void);
int goIsSharing(void);
int goGetViewerCount(void);
int goIsFullscreenSelected(void);

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
static _Atomic BOOL g_overlayEnabled = YES;
static _Atomic BOOL g_initialized = NO;
static BOOL g_isHovered = NO;
static BOOL g_isArrowHovered = NO;
static BOOL g_positionedRight = NO;  // false = left corner, true = right corner
static _Atomic BOOL g_shouldStop = NO;

// Frame counter for periodic health checks
static int g_frameCounter = 0;

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
static NSTextField *g_viewerLabel = nil;
static int g_lastSelectedCount = -1;  // For animation on count change

// Fullscreen button (in status indicator)
static NSView *g_fullscreenButtonView = nil;
static NSTextField *g_fullscreenLabel = nil;
static BOOL g_fullscreenButtonHovered = NO;
static BOOL g_lastFullscreenState = NO;  // For state change tracking

// Clear all button (in status indicator, only visible when sharing)
static NSView *g_clearButtonView = nil;
static NSTextField *g_clearButtonLabel = nil;
static BOOL g_clearButtonHovered = NO;

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

// Fullscreen button dimensions
static const CGFloat kFullscreenButtonHeight = 26.0;
static const CGFloat kFullscreenButtonSpacing = 6.0;  // Gap between status row and button

// Clear button dimensions
static const CGFloat kClearButtonHeight = 26.0;

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

    // Red color for sharing dots, blue for selected but not sharing
    NSColor *sharingColor = [NSColor colorWithRed:1.0 green:0.23 blue:0.19 alpha:1.0];
    NSColor *selectedColor = [NSColor colorWithRed:0.0 green:0.48 blue:1.0 alpha:1.0];  // Blue
    NSColor *filledColor = isSharing ? sharingColor : selectedColor;
    // Gray outline for empty slots
    NSColor *emptyBorderColor = [NSColor colorWithRed:0.4 green:0.4 blue:0.4 alpha:1.0];

    for (int i = 0; i < 4; i++) {
        if (!g_statusDots[i]) continue;

        if (i < selectedCount) {
            // Filled dot (selected or sharing)
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
    if (!g_overlayWindow || !atomic_load(&g_initialized)) return;

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

// Check if a point is over the fullscreen button in the status window
static BOOL isPointOverFullscreenButton(CGPoint cgPoint) {
    if (!g_statusWindow || !g_statusWindow.isVisible || !g_fullscreenButtonView) return NO;

    NSScreen *mainScreen = [NSScreen mainScreen];
    CGFloat screenHeight = mainScreen.frame.size.height;
    NSPoint cocoaPoint = NSMakePoint(cgPoint.x, screenHeight - cgPoint.y);

    // Get the fullscreen button's frame in screen coordinates
    NSRect statusFrame = g_statusWindow.frame;
    NSRect buttonFrame = g_fullscreenButtonView.frame;
    NSRect buttonScreenRect = NSMakeRect(
        statusFrame.origin.x + buttonFrame.origin.x,
        statusFrame.origin.y + buttonFrame.origin.y,
        buttonFrame.size.width,
        buttonFrame.size.height
    );

    return NSPointInRect(cocoaPoint, buttonScreenRect);
}

// Update fullscreen button appearance based on state and hover
static void updateFullscreenButton(BOOL isFullscreen, BOOL isHovered) {
    if (!g_fullscreenButtonView || !g_fullscreenLabel) return;

    // Update background color - clear hover effect with lighter background
    if (isHovered) {
        g_fullscreenButtonView.layer.backgroundColor = [NSColor colorWithRed:0.35 green:0.35 blue:0.38 alpha:1.0].CGColor;
    } else {
        g_fullscreenButtonView.layer.backgroundColor = [NSColor clearColor].CGColor;  // Transparent, container shows through
    }

    // Update text and color based on fullscreen state
    if (isFullscreen) {
        g_fullscreenLabel.stringValue = @"Fullscreen ✓";
        g_fullscreenLabel.textColor = [NSColor colorWithRed:1.0 green:0.23 blue:0.19 alpha:1.0];  // Red when active
    } else {
        g_fullscreenLabel.stringValue = @"Fullscreen";
        if (isHovered) {
            g_fullscreenLabel.textColor = [NSColor whiteColor];
        } else {
            g_fullscreenLabel.textColor = [NSColor colorWithRed:0.7 green:0.7 blue:0.7 alpha:1.0];
        }
    }
}

// Check if point is over clear button
static BOOL isPointOverClearButton(CGPoint cgPoint) {
    if (!g_statusWindow || !g_statusWindow.isVisible || !g_clearButtonView || g_clearButtonView.isHidden) return NO;

    NSScreen *mainScreen = [NSScreen mainScreen];
    CGFloat screenHeight = mainScreen.frame.size.height;
    NSPoint cocoaPoint = NSMakePoint(cgPoint.x, screenHeight - cgPoint.y);

    // Get the clear button's frame in screen coordinates
    NSRect statusFrame = g_statusWindow.frame;
    NSRect buttonFrame = g_clearButtonView.frame;
    NSRect buttonScreenRect = NSMakeRect(
        statusFrame.origin.x + buttonFrame.origin.x,
        statusFrame.origin.y + buttonFrame.origin.y,
        buttonFrame.size.width,
        buttonFrame.size.height
    );

    return NSPointInRect(cocoaPoint, buttonScreenRect);
}

// Update clear button appearance based on hover state
static void updateClearButton(BOOL isHovered) {
    if (!g_clearButtonView || !g_clearButtonLabel) return;

    if (isHovered) {
        g_clearButtonView.layer.backgroundColor = [NSColor colorWithRed:0.35 green:0.35 blue:0.38 alpha:1.0].CGColor;
        g_clearButtonLabel.textColor = [NSColor whiteColor];
    } else {
        g_clearButtonView.layer.backgroundColor = [NSColor clearColor].CGColor;
        g_clearButtonLabel.textColor = [NSColor colorWithRed:0.7 green:0.7 blue:0.7 alpha:1.0];
    }
}

// Track if clear button is currently visible (for resize detection)
static BOOL g_clearButtonVisible = NO;

// Update status indicator visibility and content
static void updateStatusWindow(void) {
    if (!g_statusWindow) return;

    int isManualMode = goIsManualMode();
    int isSharing = goIsSharing();
    int selectedCount = goGetSelectedWindowCount();
    int isFullscreen = goIsFullscreenSelected();

    // Show status window in manual mode OR when sharing (including auto mode)
    if (isManualMode || isSharing) {
        BOOL isAutoMode = !isManualMode;
        // Update dots indicator based on current state
        if (isSharing) {
            if (isFullscreen) {
                // Show fullscreen indicator in first dot (filled), rest empty
                updateStatusIndicator(1, YES);
            } else {
                updateStatusIndicator(selectedCount, YES);
            }
        } else {
            // Not sharing yet - show empty dots or selected count
            if (isFullscreen) {
                updateStatusIndicator(1, NO);  // Show as selected but not sharing
            } else {
                updateStatusIndicator(selectedCount, selectedCount > 0 ? NO : NO);
            }
        }

        // Update viewer count label
        if (g_viewerLabel) {
            int viewerCount = goGetViewerCount();
            g_viewerLabel.stringValue = [NSString stringWithFormat:@"%d", viewerCount];
        }

        // Update fullscreen button appearance
        updateFullscreenButton(isFullscreen, g_fullscreenButtonHovered);

        // Dynamically resize window when clear/auto button visibility changes
        // In AUTO mode: show "AUTO" indicator when sharing
        // In MANUAL mode: show "Clear All" when sharing with selections
        BOOL shouldShowClear = isSharing && (isAutoMode || (selectedCount > 0 || isFullscreen));

        // Update the button label based on mode
        if (g_clearButtonLabel) {
            if (isAutoMode) {
                g_clearButtonLabel.stringValue = @"AUTO";
            } else {
                g_clearButtonLabel.stringValue = @"Clear All";
            }
        }
        if (g_clearButtonView && g_clearButtonVisible != shouldShowClear) {
            g_clearButtonVisible = shouldShowClear;
            g_clearButtonView.hidden = !shouldShowClear;

            // Calculate dimensions
            CGFloat dotsWidth = kStatusDotSize * 4 + kStatusDotSpacing * 3;
            CGFloat viewerLabelWidth = 24.0;
            CGFloat statusWidth = kStatusPadding + dotsWidth + 8.0 + viewerLabelWidth + kStatusPadding;
            CGFloat statusRowHeight = kStatusPadding * 2 + kStatusDotSize;
            CGFloat dividerHeight = 1.0;

            // Height depends on whether clear button is visible
            CGFloat statusHeight;
            CGFloat clearButtonY = 0;
            CGFloat fullscreenY;
            CGFloat dividerY;
            CGFloat dotsRowY;

            if (shouldShowClear) {
                // Full height with clear button
                statusHeight = statusRowHeight + dividerHeight + kFullscreenButtonHeight + kClearButtonHeight;
                fullscreenY = kClearButtonHeight;
                dividerY = kClearButtonHeight + kFullscreenButtonHeight;
                dotsRowY = kClearButtonHeight + kFullscreenButtonHeight + dividerHeight;
            } else {
                // Shorter height without clear button
                statusHeight = statusRowHeight + dividerHeight + kFullscreenButtonHeight;
                fullscreenY = 0;
                dividerY = kFullscreenButtonHeight;
                dotsRowY = kFullscreenButtonHeight + dividerHeight;
            }

            // Resize window (keep top-right position fixed)
            NSScreen *mainScreen = [NSScreen mainScreen];
            CGFloat statusX = mainScreen.frame.size.width - statusWidth - kStatusCornerMargin;
            CGFloat statusY = mainScreen.frame.size.height - statusHeight - kStatusCornerMargin - 25;
            NSRect newFrame = NSMakeRect(statusX, statusY, statusWidth, statusHeight);
            [g_statusWindow setFrame:newFrame display:YES animate:YES];

            // Resize container view
            g_statusView.frame = NSMakeRect(0, 0, statusWidth, statusHeight);

            // Reposition fullscreen button
            NSRect fsFrame = g_fullscreenButtonView.frame;
            fsFrame.origin.y = fullscreenY;
            g_fullscreenButtonView.frame = fsFrame;

            // Reposition dots
            for (int i = 0; i < 4; i++) {
                if (g_statusDots[i]) {
                    NSRect dotFrame = g_statusDots[i].frame;
                    dotFrame.origin.y = dotsRowY + kStatusPadding;
                    g_statusDots[i].frame = dotFrame;
                }
            }

            // Reposition viewer label
            if (g_viewerLabel) {
                NSRect viewerFrame = g_viewerLabel.frame;
                viewerFrame.origin.y = dotsRowY + (statusRowHeight - 14.0) / 2.0;
                g_viewerLabel.frame = viewerFrame;
            }

            // Find and reposition divider (it's a subview of statusView)
            for (NSView *subview in g_statusView.subviews) {
                if (subview != g_fullscreenButtonView && subview != g_clearButtonView &&
                    subview != g_viewerLabel && ![subview isKindOfClass:[NSTextField class]]) {
                    // Check if it's the divider (small height)
                    if (subview.frame.size.height <= 2.0) {
                        NSRect divFrame = subview.frame;
                        divFrame.origin.y = dividerY;
                        subview.frame = divFrame;
                        break;
                    }
                }
            }
        }

        // Update clear button appearance if visible
        if (g_clearButtonView && !g_clearButtonView.isHidden) {
            updateClearButton(g_clearButtonHovered);
        }

        if (!g_statusWindow.isVisible) {
            [g_statusWindow orderFrontRegardless];
        }
    } else {
        // Not in manual mode AND not sharing - hide status window
        if (g_statusWindow.isVisible) {
            [g_statusWindow orderOut:nil];
        }
        g_lastSelectedCount = -1;  // Reset for next time
        g_lastFullscreenState = NO;
        g_clearButtonHovered = NO;
        g_clearButtonVisible = NO;
    }
}

// Main frame update - called by game loop at 60fps
// All NSWindow operations are dispatched to the main thread.
static void doFrameOnMainThread(void);

static void doFrame(void) {
    if (!g_overlayWindow || !atomic_load(&g_initialized)) {
        return;
    }

    // Dispatch UI updates to main thread
    if ([NSThread isMainThread]) {
        doFrameOnMainThread();
    } else {
        dispatch_async(dispatch_get_main_queue(), ^{
            doFrameOnMainThread();
        });
    }
}

static void doFrameOnMainThread(void) {
    if (!g_overlayWindow || !atomic_load(&g_initialized)) {
        return;
    }

    // Periodic event tap health check (every ~2 seconds at 60fps)
    g_frameCounter++;
    if (g_frameCounter % 120 == 0 && g_eventTap) {
        if (!CGEventTapIsEnabled(g_eventTap)) {
            CGEventTapEnable(g_eventTap, true);
        }
    }

    // Always update status indicator (shows when sharing, independent of focus)
    updateStatusWindow();

    // Early exit if disabled
    if (!atomic_load(&g_overlayEnabled)) {
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

    // Get screen info (needed for coordinate conversion and positioning)
    NSScreen *mainScreen = [NSScreen mainScreen];
    CGFloat screenHeight = mainScreen.frame.size.height;

    // Check hover state using NSEvent mouseLocation (more efficient than CGEventCreate)
    NSPoint mouseLocation = [NSEvent mouseLocation];
    CGPoint mousePos = CGPointMake(mouseLocation.x, screenHeight - mouseLocation.y);
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

// Event tap callback for mouse clicks and movement
static CGEventRef mouseEventCallback(CGEventTapProxy proxy, CGEventType type, CGEventRef event, void *refcon) {
    if (type == kCGEventLeftMouseDown) {
        CGPoint clickPoint = CGEventGetLocation(event);

        // Check for fullscreen button click first (status window)
        if (isPointOverFullscreenButton(clickPoint)) {
            goFullscreenButtonClicked();
            return NULL; // Consume the click
        }

        // Check for clear all button click (status window)
        // Only handle click in manual mode - in auto mode it's just an indicator
        if (isPointOverClearButton(clickPoint)) {
            if (goIsManualMode()) {
                goClearButtonClicked();
                return NULL; // Consume the click
            }
            // In AUTO mode, don't consume - let it pass through
        }

        // Check for overlay button click (window share button)
        if (g_overlayWindow && g_overlayWindow.isVisible && g_currentWindowID != 0) {
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
        }
    } else if (type == kCGEventMouseMoved) {
        // Track hover state for fullscreen button
        CGPoint mousePoint = CGEventGetLocation(event);
        BOOL wasHovered = g_fullscreenButtonHovered;
        g_fullscreenButtonHovered = isPointOverFullscreenButton(mousePoint);

        // Update button appearance if hover state changed
        if (wasHovered != g_fullscreenButtonHovered) {
            int isFullscreen = goIsFullscreenSelected();
            updateFullscreenButton(isFullscreen, g_fullscreenButtonHovered);
        }

        // Track hover state for clear button (only in manual mode - it's clickable)
        BOOL wasClearHovered = g_clearButtonHovered;
        // Only show hover effect in manual mode where button is clickable
        g_clearButtonHovered = goIsManualMode() && isPointOverClearButton(mousePoint);

        // Update clear button appearance if hover state changed
        if (wasClearHovered != g_clearButtonHovered) {
            updateClearButton(g_clearButtonHovered);
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

        while (!atomic_load(&g_shouldStop)) {
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

static void createOverlayOnMainThread(void);

static void createOverlay(void) {
    if (atomic_load(&g_initialized)) return;

    // NSWindow operations MUST happen on the main thread.
    // In the current architecture, RunTUI runs in a background goroutine while
    // the main goroutine runs the main run loop. So this will typically be called
    // from a non-main thread, and we dispatch to the main queue which is serviced
    // by the main run loop.
    if ([NSThread isMainThread]) {
        createOverlayOnMainThread();
    } else {
        dispatch_async(dispatch_get_main_queue(), ^{
            createOverlayOnMainThread();
        });
    }
}

static void createOverlayOnMainThread(void) {
    if (atomic_load(&g_initialized)) return;

    @autoreleasepool {
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];

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

        // Create status indicator window (top-right corner, shows status and fullscreen toggle)
        // Width: padding + 4 dots + spacing + divider space + viewer label + padding
        CGFloat dotsWidth = kStatusDotSize * 4 + kStatusDotSpacing * 3;
        CGFloat viewerLabelWidth = 24.0;  // Space for "99" (just number)
        CGFloat statusWidth = kStatusPadding + dotsWidth + 8.0 + viewerLabelWidth + kStatusPadding;
        CGFloat statusRowHeight = kStatusPadding * 2 + kStatusDotSize;
        CGFloat dividerHeight = 1.0;
        // Initial height: status row + divider + fullscreen button row (no clear button yet)
        CGFloat statusHeight = statusRowHeight + dividerHeight + kFullscreenButtonHeight;
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
        g_statusWindow.ignoresMouseEvents = NO;  // Interactive (for fullscreen button)
        g_statusWindow.collectionBehavior = NSWindowCollectionBehaviorCanJoinAllSpaces |
                                            NSWindowCollectionBehaviorStationary |
                                            NSWindowCollectionBehaviorFullScreenAuxiliary |
                                            NSWindowCollectionBehaviorIgnoresCycle;
        g_statusWindow.alphaValue = 1.0;

        // Single container with rounded corners
        g_statusView = [[NSView alloc] initWithFrame:NSMakeRect(0, 0, statusWidth, statusHeight)];
        g_statusView.wantsLayer = YES;
        g_statusView.layer.cornerRadius = 10.0;  // Rounded rectangle
        g_statusView.layer.backgroundColor = [NSColor colorWithRed:0.0 green:0.0 blue:0.0 alpha:0.75].CGColor;
        g_statusWindow.contentView = g_statusView;

        // Top row: dots + viewer count (initially without clear button)
        CGFloat dotsRowY = kFullscreenButtonHeight + dividerHeight;
        for (int i = 0; i < 4; i++) {
            CGFloat dotX = kStatusPadding + i * (kStatusDotSize + kStatusDotSpacing);
            CGFloat dotY = dotsRowY + kStatusPadding;
            g_statusDots[i] = [[NSView alloc] initWithFrame:NSMakeRect(dotX, dotY, kStatusDotSize, kStatusDotSize)];
            g_statusDots[i].wantsLayer = YES;
            g_statusDots[i].layer.cornerRadius = kStatusDotSize / 2.0;
            // Initial state: empty outline
            g_statusDots[i].layer.backgroundColor = [NSColor clearColor].CGColor;
            g_statusDots[i].layer.borderColor = [NSColor colorWithRed:0.4 green:0.4 blue:0.4 alpha:1.0].CGColor;
            g_statusDots[i].layer.borderWidth = 1.5;
            [g_statusView addSubview:g_statusDots[i]];
        }

        // Viewer count label (after dots)
        CGFloat viewerX = kStatusPadding + dotsWidth + 8.0;
        CGFloat viewerY = dotsRowY + (statusRowHeight - 14.0) / 2.0;
        g_viewerLabel = [[NSTextField alloc] initWithFrame:NSMakeRect(viewerX, viewerY, viewerLabelWidth, 14.0)];
        g_viewerLabel.stringValue = @"0";
        g_viewerLabel.font = [NSFont systemFontOfSize:11 weight:NSFontWeightMedium];
        g_viewerLabel.textColor = [NSColor whiteColor];
        g_viewerLabel.backgroundColor = [NSColor clearColor];
        g_viewerLabel.bordered = NO;
        g_viewerLabel.editable = NO;
        g_viewerLabel.selectable = NO;
        g_viewerLabel.alignment = NSTextAlignmentLeft;
        [g_statusView addSubview:g_viewerLabel];

        // Divider line between rows (initially without clear button)
        NSView *divider = [[NSView alloc] initWithFrame:NSMakeRect(kStatusPadding, kFullscreenButtonHeight, statusWidth - 2 * kStatusPadding, dividerHeight)];
        divider.wantsLayer = YES;
        divider.layer.backgroundColor = [NSColor colorWithRed:0.3 green:0.3 blue:0.3 alpha:1.0].CGColor;
        [g_statusView addSubview:divider];

        // Middle row: Fullscreen toggle button (clickable area, initially at bottom)
        g_fullscreenButtonView = [[NSView alloc] initWithFrame:NSMakeRect(0, 0, statusWidth, kFullscreenButtonHeight)];
        g_fullscreenButtonView.wantsLayer = YES;
        g_fullscreenButtonView.layer.cornerRadius = 0;  // Part of container, no separate rounding
        g_fullscreenButtonView.layer.backgroundColor = [NSColor clearColor].CGColor;  // Transparent by default
        [g_statusView addSubview:g_fullscreenButtonView];

        // Fullscreen button label - properly centered
        CGFloat fsLabelHeight = 14.0;
        CGFloat fsLabelY = (kFullscreenButtonHeight - fsLabelHeight) / 2.0;
        g_fullscreenLabel = [[NSTextField alloc] initWithFrame:NSMakeRect(0, fsLabelY, statusWidth, fsLabelHeight)];
        g_fullscreenLabel.stringValue = @"Fullscreen";
        g_fullscreenLabel.font = [NSFont systemFontOfSize:11 weight:NSFontWeightMedium];
        g_fullscreenLabel.textColor = [NSColor colorWithRed:0.7 green:0.7 blue:0.7 alpha:1.0];
        g_fullscreenLabel.backgroundColor = [NSColor clearColor];
        g_fullscreenLabel.bordered = NO;
        g_fullscreenLabel.editable = NO;
        g_fullscreenLabel.selectable = NO;
        g_fullscreenLabel.alignment = NSTextAlignmentCenter;
        [g_fullscreenButtonView addSubview:g_fullscreenLabel];

        // Bottom row: Clear All button (only visible when sharing)
        g_clearButtonView = [[NSView alloc] initWithFrame:NSMakeRect(0, 0, statusWidth, kClearButtonHeight)];
        g_clearButtonView.wantsLayer = YES;
        g_clearButtonView.layer.cornerRadius = 0;
        g_clearButtonView.layer.backgroundColor = [NSColor clearColor].CGColor;
        // Round only the bottom corners
        g_clearButtonView.layer.maskedCorners = kCALayerMinXMinYCorner | kCALayerMaxXMinYCorner;
        g_clearButtonView.hidden = YES;  // Initially hidden (only shown when sharing)
        [g_statusView addSubview:g_clearButtonView];

        // Clear button label
        CGFloat clearLabelHeight = 14.0;
        CGFloat clearLabelY = (kClearButtonHeight - clearLabelHeight) / 2.0;
        g_clearButtonLabel = [[NSTextField alloc] initWithFrame:NSMakeRect(0, clearLabelY, statusWidth, clearLabelHeight)];
        g_clearButtonLabel.stringValue = @"Clear All";
        g_clearButtonLabel.font = [NSFont systemFontOfSize:11 weight:NSFontWeightMedium];
        g_clearButtonLabel.textColor = [NSColor colorWithRed:0.7 green:0.7 blue:0.7 alpha:1.0];
        g_clearButtonLabel.backgroundColor = [NSColor clearColor];
        g_clearButtonLabel.bordered = NO;
        g_clearButtonLabel.editable = NO;
        g_clearButtonLabel.selectable = NO;
        g_clearButtonLabel.alignment = NSTextAlignmentCenter;
        [g_clearButtonView addSubview:g_clearButtonLabel];

        // Event tap for clicks AND mouse movement (for hover)
        CGEventMask eventMask = CGEventMaskBit(kCGEventLeftMouseDown) | CGEventMaskBit(kCGEventMouseMoved);
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

                atomic_store(&g_shouldStop, NO);
                pthread_t tapThread;
                pthread_create(&tapThread, NULL, eventTapThread, NULL);
                pthread_detach(tapThread);
            } else {
                CFRelease(g_eventTap);
                g_eventTap = NULL;
            }
        }

        atomic_store(&g_initialized, YES);
        [g_overlayWindow display];
    }
}

static void destroyOverlayOnMainThread(void);

static void destroyOverlay(void) {
    atomic_store(&g_shouldStop, YES);
    atomic_store(&g_isAnimating, NO);

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

    // Dispatch NSWindow cleanup to main thread
    if ([NSThread isMainThread]) {
        destroyOverlayOnMainThread();
    } else {
        // Use async with timeout to avoid hanging if main queue isn't serviced
        dispatch_semaphore_t sem = dispatch_semaphore_create(0);
        dispatch_async(dispatch_get_main_queue(), ^{
            destroyOverlayOnMainThread();
            dispatch_semaphore_signal(sem);
        });
        // Wait up to 500ms for cleanup, then give up to avoid hang
        dispatch_semaphore_wait(sem, dispatch_time(DISPATCH_TIME_NOW, 500 * NSEC_PER_MSEC));
    }

    atomic_store(&g_initialized, NO);
}

static void destroyOverlayOnMainThread(void) {
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
    g_viewerLabel = nil;
    g_lastSelectedCount = -1;
}

static void setOverlayEnabled(BOOL enabled) {
    atomic_store(&g_overlayEnabled, enabled);
}

// Main run loop support - needed because Go doesn't automatically service the main queue
static volatile BOOL g_mainLoopRunning = NO;

static void runMainRunLoop(void) {
    @autoreleasepool {
        [NSApplication sharedApplication];
        [NSApp setActivationPolicy:NSApplicationActivationPolicyAccessory];

        g_mainLoopRunning = YES;

        while (g_mainLoopRunning) {
            @autoreleasepool {
                NSDate *future = [NSDate dateWithTimeIntervalSinceNow:0.01];
                [[NSRunLoop currentRunLoop] runMode:NSDefaultRunLoopMode beforeDate:future];
            }
        }
    }
}

static void stopMainRunLoop(void) {
    g_mainLoopRunning = NO;
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

//export goGetViewerCount
func goGetViewerCount() C.int {
	globalMu.RLock()
	o := globalOverlay
	globalMu.RUnlock()

	if o == nil || o.controller == nil {
		return 0
	}

	return C.int(o.controller.GetViewerCount())
}

//export goIsFullscreenSelected
func goIsFullscreenSelected() C.int {
	globalMu.RLock()
	o := globalOverlay
	globalMu.RUnlock()

	if o == nil || o.controller == nil {
		return 0
	}

	if o.controller.IsFullscreenSelected() {
		return 1
	}
	return 0
}

//export goFullscreenButtonClicked
func goFullscreenButtonClicked() {
	if time.Since(lastClickTime) < 300*time.Millisecond {
		return
	}
	lastClickTime = time.Now()

	globalMu.RLock()
	o := globalOverlay
	globalMu.RUnlock()

	if o != nil {
		o.sendEvent(Event{
			Type: EventToggleFullscreen,
		})
	}
}

//export goClearButtonClicked
func goClearButtonClicked() {
	if time.Since(lastClickTime) < 300*time.Millisecond {
		return
	}
	lastClickTime = time.Now()

	globalMu.RLock()
	o := globalOverlay
	globalMu.RUnlock()

	if o != nil {
		o.sendEvent(Event{
			Type: EventClearAll,
		})
	}
}

// runLoop is the 60fps game loop for the overlay.
// It runs in its own goroutine, locked to an OS thread for stability.
// All NSWindow operations are dispatched to the main thread via dispatch_sync/async.
func (o *Overlay) runLoop() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

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
// In the current architecture this is typically called from a background goroutine
// (e.g. the one running RunTUI), while the main goroutine runs the main run loop.
func (o *Overlay) platformStart() error {
	globalMu.Lock()
	globalOverlay = o
	globalMu.Unlock()

	// Create the overlay; the C implementation will dispatch NSWindow work to the
	// main queue if this is not already running on the main thread.
	C.createOverlay()

	// Start the game loop in a separate goroutine
	o.running.Store(true)
	o.stopped = make(chan struct{})
	go o.runLoop()

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

// RunMainRunLoop runs the macOS main run loop on the current thread.
// This MUST be called from the main goroutine (which should be locked to the main OS thread).
// It blocks until StopMainRunLoop is called.
func RunMainRunLoop() {
	C.runMainRunLoop()
}

// StopMainRunLoop signals the main run loop to stop.
func StopMainRunLoop() {
	C.stopMainRunLoop()
}
