//go:build !darwin

package appwin

import "gioui.org/io/event"

func defaultMacTitleBarPlatform() macTitleBarPlatform {
	return nil
}

// extractPlatformView returns false outside macOS.
func extractPlatformView(e event.Event) (uintptr, bool) {
	_ = e
	return 0, false
}
