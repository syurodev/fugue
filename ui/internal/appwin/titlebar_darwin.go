//go:build darwin

package appwin

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit
#import <AppKit/AppKit.h>

static void fugueConfigureTitleBar(uintptr_t viewPtr) {
	if (viewPtr == 0) {
		return;
	}
	NSView *view = (__bridge NSView *)(void *)viewPtr;
	NSWindow *win = view.window;
	if (win == nil) {
		return;
	}
	dispatch_async(dispatch_get_main_queue(), ^{
		win.styleMask |= NSWindowStyleMaskFullSizeContentView;
		win.titlebarAppearsTransparent = YES;
		win.titleVisibility = NSWindowTitleHidden;
	});
}

// configurePlatformTitleBar bridge entry point.
*/
import "C"

import (
	"gioui.org/app"
	"gioui.org/io/event"
)

// configurePlatformTitleBar opts the macOS window into fullSizeContentView with a
// transparent titlebar so native traffic lights sit inline inside the 32 dp Top Bar.
func configurePlatformTitleBar(e event.Event) {
	av, ok := e.(app.AppKitViewEvent)
	if !ok || !av.Valid() {
		return
	}
	C.fugueConfigureTitleBar(C.uintptr_t(av.View))
}
