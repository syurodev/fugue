//go:build darwin

package appwin

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit
#import <AppKit/AppKit.h>
#import <objc/runtime.h>

extern void fugueOnTitleBarMetrics(uintptr_t handle, double barH, double zx0, double zy0, double zx1, double zy1);
extern void fugueReleaseTitleBarHandle(uintptr_t handle);

static const char kFugueDragRectKey = 0;

static void fugueInstallDoubleClickMonitor(void) {
	static dispatch_once_t onceToken;
	dispatch_once(&onceToken, ^{
		[NSEvent addLocalMonitorForEventsMatchingMask:NSEventMaskLeftMouseDown
		                                      handler:^NSEvent *(NSEvent *ev) {
			if (ev.clickCount != 2) {
				return ev;
			}
			NSWindow *win = ev.window;
			if (win == nil || (win.styleMask & NSWindowStyleMaskFullScreen) != 0) {
				return ev;
			}
			NSView *view = win.contentView;
			if (view == nil) {
				return ev;
			}
			NSValue *val = objc_getAssociatedObject(view, &kFugueDragRectKey);
			if (val == nil) {
				return ev;
			}
			NSRect dragRect = [val rectValue];
			if (NSIsEmptyRect(dragRect)) {
				return ev;
			}
			NSPoint p = [view convertPoint:ev.locationInWindow fromView:nil];
			CGFloat topY = view.bounds.size.height - p.y;
			if (p.x < dragRect.origin.x || p.x >= NSMaxX(dragRect) ||
			    topY < dragRect.origin.y || topY >= NSMaxY(dragRect)) {
				return ev;
			}
			NSString *action = [[NSUserDefaults standardUserDefaults] stringForKey:@"AppleActionOnDoubleClick"];
			if ([action isEqualToString:@"Minimize"]) {
				[win miniaturize:nil];
				return nil;
			}
			if ([action isEqualToString:@"None"]) {
				return ev;
			}
			[win zoom:nil];
			return nil;
		}];
	});
}

static void fugueConfigureTitleBar(uintptr_t viewPtr, int fullscreen, uintptr_t handle) {
	fugueInstallDoubleClickMonitor();
	if (viewPtr == 0) {
		if (handle != 0) {
			fugueReleaseTitleBarHandle(handle);
		}
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
				NSRect winFrame = win.frame;
				NSRect contentLayout = win.contentLayoutRect;
				double barH = winFrame.size.height - contentLayout.size.height;
				if (barH < 0) {
					barH = 0;
				}
				double zx0 = 0, zy0 = 0, zx1 = 0, zy1 = 0;
				NSButton *zoomBtn = [win standardWindowButton:NSWindowZoomButton];
				if (zoomBtn != nil && !zoomBtn.hidden) {
					NSRect z = [zoomBtn convertRect:zoomBtn.bounds toView:nil];
					CGFloat winH = winFrame.size.height;
					zx0 = z.origin.x;
					zx1 = NSMaxX(z);
					zy0 = winH - NSMaxY(z);
					zy1 = winH - z.origin.y;
				}
				if (handle != 0) {
					fugueOnTitleBarMetrics(handle, barH, zx0, zy0, zx1, zy1);
				}
			}
			if (handle != 0) {
				fugueReleaseTitleBarHandle(handle);
			}
			CFRelease(viewRef);
		}
	});
}

static void fugueSetDragRect(uintptr_t viewPtr, double x0, double y0, double x1, double y1) {
	if (viewPtr == 0) {
		return;
	}
	CFTypeRef viewRef = CFRetain((CFTypeRef)(void *)viewPtr);
	dispatch_async(dispatch_get_main_queue(), ^{
		@autoreleasepool {
			NSView *view = (__bridge NSView *)viewRef;
			NSValue *val = (x1 > x0 && y1 > y0) ? [NSValue valueWithRect:NSMakeRect(x0, y0, x1-x0, y1-y0)] : nil;
			objc_setAssociatedObject(view, &kFugueDragRectKey, val, OBJC_ASSOCIATION_RETAIN_NONATOMIC);
			CFRelease(viewRef);
		}
	});
}

static int fugueDoubleClickAction(void) {
	@autoreleasepool {
		NSString *action = [[NSUserDefaults standardUserDefaults] stringForKey:@"AppleActionOnDoubleClick"];
		if ([action isEqualToString:@"Minimize"]) {
			return 2;
		}
		if ([action isEqualToString:@"None"]) {
			return 0;
		}
		return 1;
	}
}

// cgo titlebar bridge entry points.
*/
import "C"

func cgoConfigureTitleBar(viewPtr uintptr, fullscreen bool, handle uintptr) {
	fs := C.int(0)
	if fullscreen {
		fs = C.int(1)
	}
	C.fugueConfigureTitleBar(C.uintptr_t(viewPtr), fs, C.uintptr_t(handle))
}

func cgoSetDragRect(viewPtr uintptr, x0, y0, x1, y1 float64) {
	C.fugueSetDragRect(C.uintptr_t(viewPtr), C.double(x0), C.double(y0), C.double(x1), C.double(y1))
}

func cgoDoubleClickAction() int {
	return int(C.fugueDoubleClickAction())
}
