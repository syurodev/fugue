//go:build darwin

package osappearance

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit
#import <AppKit/AppKit.h>
#import <stdlib.h>

extern void fugueOnAppearanceChange(uintptr_t handle, int code);
extern void fugueReleaseAppearanceHandle(uintptr_t handle);

static int fugueAppearanceKVOContext;

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

@interface FugueAppearanceObserver : NSObject
@property (nonatomic, assign) uintptr_t handle;
@property (nonatomic, assign) BOOL stopped;
@property (nonatomic, assign) BOOL observing;
@end

@implementation FugueAppearanceObserver
- (void)observeValueForKeyPath:(NSString *)keyPath
                      ofObject:(id)object
                        change:(NSDictionary<NSKeyValueChangeKey, id> *)change
                       context:(void *)context {
	(void)keyPath;
	(void)object;
	(void)change;
	if (context == &fugueAppearanceKVOContext) {
		if (!self.stopped) {
			if ([NSThread isMainThread]) {
				fugueOnAppearanceChange(self.handle, fugueReadAppearance());
			} else {
				dispatch_async(dispatch_get_main_queue(), ^{
					@autoreleasepool {
						if (!self.stopped) {
							fugueOnAppearanceChange(self.handle, fugueReadAppearance());
						}
					}
				});
			}
		}
		return;
	}
	[super observeValueForKeyPath:keyPath ofObject:object change:change context:context];
}
@end

typedef struct {
	uintptr_t handle;
	CFTypeRef observer;
	BOOL stopped;
} FugueAppearanceWatch;

static uintptr_t fugueStartAppearanceWatch(uintptr_t handle) {
	FugueAppearanceWatch *watch = (FugueAppearanceWatch *)calloc(1, sizeof(FugueAppearanceWatch));
	if (watch == NULL) {
		return 0;
	}
	watch->handle = handle;
	dispatch_async(dispatch_get_main_queue(), ^{
		@autoreleasepool {
			if (watch->stopped || NSApp == nil) {
				return;
			}
			FugueAppearanceObserver *obs = [[FugueAppearanceObserver alloc] init];
			obs.handle = watch->handle;
			obs.stopped = NO;
			obs.observing = YES;
			watch->observer = (CFTypeRef)obs;
			[NSApp addObserver:obs
			        forKeyPath:@"effectiveAppearance"
			           options:(NSKeyValueObservingOptionNew | NSKeyValueObservingOptionInitial)
			           context:&fugueAppearanceKVOContext];
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
				FugueAppearanceObserver *obs = (__bridge FugueAppearanceObserver *)watch->observer;
				obs.stopped = YES;
				if (obs.observing && NSApp != nil) {
					[NSApp removeObserver:obs
					           forKeyPath:@"effectiveAppearance"
					              context:&fugueAppearanceKVOContext];
					obs.observing = NO;
				}
				CFRelease(watch->observer);
				watch->observer = NULL;
			}
			fugueReleaseAppearanceHandle(watch->handle);
			free(watch);
		}
	});
}

// cgo appearance observer entry points.
*/
import "C"

func cgoStartAppearanceWatch(handle uintptr) uintptr {
	return uintptr(C.fugueStartAppearanceWatch(C.uintptr_t(handle)))
}

func cgoStopAppearanceWatch(ptr uintptr) {
	C.fugueStopAppearanceWatch(C.uintptr_t(ptr))
}
