//go:build darwin

package appwin

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit
#import <AppKit/AppKit.h>

// workArea reads the main screen's visible frame (points) and backing scale;
// zeros mean no screen was available.
static void fugueWorkArea(double *w, double *h, double *scale) {
	@autoreleasepool {
		NSScreen *screen = NSScreen.mainScreen;
		if (screen == nil) {
			return;
		}
		NSRect frame = screen.visibleFrame;
		*w = frame.size.width;
		*h = frame.size.height;
		*scale = screen.backingScaleFactor;
	}
}

// workArea is the package's only AppKit entry point.
*/
import "C"

// cgoWorkArea implements WorkArea: on macOS points are the dp unit Gio
// uses, so the pixel work area is the frame times the scale.
func cgoWorkArea() (screenW, screenH, scale float64) {
	var w, h, s C.double
	C.fugueWorkArea(&w, &h, &s)
	return float64(w) * float64(s), float64(h) * float64(s), float64(s)
}
