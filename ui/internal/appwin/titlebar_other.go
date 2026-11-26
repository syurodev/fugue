//go:build !darwin

package appwin

import "gioui.org/io/event"

// configurePlatformTitleBar is a no-op outside macOS; native or Linux CSD
// chrome handles window decorations on those platforms.
func configurePlatformTitleBar(e event.Event) {
	_ = e
}
