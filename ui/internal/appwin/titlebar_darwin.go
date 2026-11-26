//go:build darwin

package appwin

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit
#import <AppKit/AppKit.h>

static void fugueConfigureTitleBar(uintptr_t viewPtr, int fullscreen) {
	if (viewPtr == 0) {
		return;
	}
	CFTypeRef viewRef = CFRetain((CFTypeRef)(void *)viewPtr);
	dispatch_async(dispatch_get_main_queue(), ^{
		@autoreleasepool {
			NSView *view = (__bridge NSView *)viewRef;
			NSWindow *win = view.window;
			if (win != nil) {
				if ((win.styleMask & NSWindowStyleMaskFullSizeContentView) == 0) {
					win.styleMask |= NSWindowStyleMaskFullSizeContentView;
				}
				if (!win.titlebarAppearsTransparent) {
					win.titlebarAppearsTransparent = YES;
				}
				if (win.titleVisibility != NSWindowTitleHidden) {
					win.titleVisibility = NSWindowTitleHidden;
				}
				if (!fullscreen) {
					[[win standardWindowButton:NSWindowCloseButton] setHidden:NO];
					[[win standardWindowButton:NSWindowMiniaturizeButton] setHidden:NO];
					[[win standardWindowButton:NSWindowZoomButton] setHidden:NO];
				}
				if (view.layer != nil) {
					view.layer.frame = view.frame;
				}
			}
			CFRelease(viewRef);
		}
	});
}

// applyPlatformTitleBar bridge entry point.
*/
import "C"

import (
	"gioui.org/app"
	"gioui.org/io/event"
)

// extractPlatformView returns the retained-candidate AppKit NSView pointer from
// a Gio ViewEvent when available.
func extractPlatformView(e event.Event) (uintptr, bool) {
	av, ok := e.(app.AppKitViewEvent)
	if !ok || !av.Valid() {
		return 0, false
	}
	return av.View, true
}

// applyPlatformTitleBar opts the macOS window into fullSizeContentView with a
// transparent titlebar on the AppKit main thread so native traffic lights sit
// inline inside the 32 dp Top Bar and survive Gio Configure resets.
func applyPlatformTitleBar(viewPtr uintptr, fullscreen bool) {
	if viewPtr == 0 {
		return
	}
	fs := C.int(0)
	if fullscreen {
		fs = C.int(1)
	}
	C.fugueConfigureTitleBar(C.uintptr_t(viewPtr), fs)
}
