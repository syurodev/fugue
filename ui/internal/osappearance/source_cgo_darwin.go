//go:build darwin

package osappearance

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit
#import <AppKit/AppKit.h>
#import <stdlib.h>

extern void fugueOnAppearanceChange(uintptr_t handle, int code);
extern void fugueReleaseAppearanceHandle(uintptr_t handle);

typedef struct {
	uintptr_t handle;
	CFTypeRef observer;
	BOOL stopped;
} FugueAppearanceWatch;

static int fugueReadAppearance(void) {
	if (NSApp == nil) {
		return 0;
	}
	NSAppearance *eff = NSApp.effectiveAppearance;
	if (eff == nil) {
		return 0;
	}
	NSAppearanceName match = [eff bestMatchFromAppearancesWithNames:@[
		NSAppearanceNameAqua,
		NSAppearanceNameDarkAqua
	]];
	if ([match isEqualToString:NSAppearanceNameDarkAqua]) {
		return 2;
	}
	if ([match isEqualToString:NSAppearanceNameAqua]) {
		return 1;
	}
	return 0;
}

static uintptr_t fugueStartAppearanceWatch(uintptr_t handle) {
	FugueAppearanceWatch *watch = (FugueAppearanceWatch *)calloc(1, sizeof(FugueAppearanceWatch));
	if (watch == NULL) {
		return 0;
	}
	watch->handle = handle;
	dispatch_async(dispatch_get_main_queue(), ^{
		@autoreleasepool {
			if (watch->stopped) {
				return;
			}
			fugueOnAppearanceChange(watch->handle, fugueReadAppearance());
			id obs = [[NSDistributedNotificationCenter defaultCenter]
				addObserverForName:@"AppleInterfaceThemeChangedNotification"
				object:nil
				queue:NSOperationQueue.mainQueue
				usingBlock:^(NSNotification *note) {
					(void)note;
					if (!watch->stopped) {
						fugueOnAppearanceChange(watch->handle, fugueReadAppearance());
					}
				}];
			if (obs != nil) {
				watch->observer = CFRetain((CFTypeRef)obs);
			}
		}
	});
	return (uintptr_t)watch;
}

static void fugueStopAppearanceWatch(uintptr_t ptr) {
	FugueAppearanceWatch *watch = (FugueAppearanceWatch *)(void *)ptr;
	if (watch == NULL) {
		return;
	}
	dispatch_async(dispatch_get_main_queue(), ^{
		@autoreleasepool {
			watch->stopped = YES;
			if (watch->observer != NULL) {
				[[NSDistributedNotificationCenter defaultCenter] removeObserver:(__bridge id)watch->observer];
				CFRelease(watch->observer);
				watch->observer = NULL;
			}
			fugueReleaseAppearanceHandle(watch->handle);
			free(watch);
		}
	});
}
*/
import "C"

func cgoStartAppearanceWatch(handle uintptr) uintptr {
	return uintptr(C.fugueStartAppearanceWatch(C.uintptr_t(handle)))
}

func cgoStopAppearanceWatch(ptr uintptr) {
	C.fugueStopAppearanceWatch(C.uintptr_t(ptr))
}
