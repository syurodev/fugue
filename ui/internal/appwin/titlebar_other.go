//go:build !darwin

package appwin

import "gioui.org/io/event"

// extractPlatformView returns false outside macOS.
func extractPlatformView(e event.Event) (uintptr, bool) {
	_ = e
	return 0, false
}

// applyPlatformTitleBar is a no-op outside macOS; native or Linux CSD
// chrome handles window decorations on those platforms.
func applyPlatformTitleBar(viewPtr uintptr, fullscreen bool) {
	_ = viewPtr
	_ = fullscreen
}
