//go:build darwin

package appwin

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework AppKit
#import <AppKit/AppKit.h>

extern void fugueAppearanceChanged(uintptr_t handle);

@interface FugueAppearanceObserver : NSObject
@property (nonatomic, assign) uintptr_t handle;
@property (nonatomic, assign) BOOL observingApp;
@end

@implementation FugueAppearanceObserver
- (void)distributedThemeChanged:(NSNotification *)note {
	(void)note;
	fugueAppearanceChanged(self.handle);
}

- (void)observeValueForKeyPath:(NSString *)keyPath
                      ofObject:(id)object
                        change:(NSDictionary<NSKeyValueChangeKey, id> *)change
                       context:(void *)context {
	(void)keyPath;
	(void)object;
	(void)change;
	(void)context;
	fugueAppearanceChanged(self.handle);
}
@end

static int fugueReadAppearance(void) {
	@autoreleasepool {
		NSApplication *app = NSApp;
		if (app == nil) {
			return 0;
		}
		NSAppearance *eff = app.effectiveAppearance;
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
}

static void *fugueStartAppearanceObserver(uintptr_t handle) {
	@autoreleasepool {
		FugueAppearanceObserver *obs = [[FugueAppearanceObserver alloc] init];
		obs.handle = handle;
		[[NSDistributedNotificationCenter defaultCenter]
			addObserver:obs
			   selector:@selector(distributedThemeChanged:)
			       name:@"AppleInterfaceThemeChangedNotification"
			     object:nil];
		NSApplication *app = NSApp;
		if (app != nil) {
			obs.observingApp = YES;
			[app addObserver:obs
			      forKeyPath:@"effectiveAppearance"
			         options:NSKeyValueObservingOptionNew
			         context:NULL];
		}
		return (void *)obs;
	}
}

static void fugueStopAppearanceObserver(void *ptr) {
	if (ptr == NULL) {
		return;
	}
	@autoreleasepool {
		FugueAppearanceObserver *obs = (FugueAppearanceObserver *)ptr;
		[[NSDistributedNotificationCenter defaultCenter] removeObserver:obs];
		if (obs.observingApp && NSApp != nil) {
			[NSApp removeObserver:obs forKeyPath:@"effectiveAppearance"];
		}
		obs.handle = 0;
		[obs release];
	}
}
// appearance is the package's AppKit appearance entry point.
*/
import "C"

import (
	"runtime/cgo"
	"sync"
)

type darwinAppearanceSource struct{}

func newPlatformAppearanceSource() appearanceSource {
	return darwinAppearanceSource{}
}

func (darwinAppearanceSource) Current() osAppearance {
	switch C.fugueReadAppearance() {
	case 1:
		return appearanceLight
	case 2:
		return appearanceDark
	default:
		return appearanceUnknown
	}
}

func (darwinAppearanceSource) Subscribe(onChange func()) func() {
	if onChange == nil {
		return func() {}
	}
	h := cgo.NewHandle(onChange)
	ptr := C.fugueStartAppearanceObserver(C.uintptr_t(h))
	var once sync.Once
	return func() {
		once.Do(func() {
			C.fugueStopAppearanceObserver(ptr)
			h.Delete()
		})
	}
}
