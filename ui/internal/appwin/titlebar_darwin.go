//go:build darwin

package appwin

/*
#include <stdint.h>
*/
import "C"

import (
	"image"
	"math"
	"runtime/cgo"

	"gioui.org/app"
	"gioui.org/io/event"
)

type darwinTitleBarPlatform struct{}

func defaultMacTitleBarPlatform() macTitleBarPlatform {
	return darwinTitleBarPlatform{}
}

// extractPlatformView returns the retained-candidate AppKit NSView pointer from
// a Gio ViewEvent when available.
func extractPlatformView(e event.Event) (uintptr, bool) {
	av, ok := e.(app.AppKitViewEvent)
	if !ok || !av.Valid() {
		return 0, false
	}
	return av.View, true
}

func (darwinTitleBarPlatform) applyTitleBar(viewPtr uintptr, fullscreen bool, onMetrics func(macWindowMetrics)) {
	if viewPtr == 0 {
		return
	}
	var handle uintptr
	if onMetrics != nil {
		handle = uintptr(cgo.NewHandle(onMetrics))
	}
	cgoConfigureTitleBar(viewPtr, fullscreen, handle)
}

func (darwinTitleBarPlatform) doubleClickAction() macDoubleClickAction {
	switch cgoDoubleClickAction() {
	case 0:
		return macDoubleClickNone
	case 2:
		return macDoubleClickMinimize
	default:
		return macDoubleClickZoom
	}
}

func (darwinTitleBarPlatform) setDragRect(viewPtr uintptr, rect image.Rectangle) {
	if viewPtr == 0 {
		return
	}
	cgoSetDragRect(viewPtr, float64(rect.Min.X), float64(rect.Min.Y), float64(rect.Max.X), float64(rect.Max.Y))
}

//export fugueOnTitleBarMetrics
func fugueOnTitleBarMetrics(handle C.uintptr_t, barH, zx0, zy0, zx1, zy1 C.double) {
	onMetrics, ok := cgo.Handle(handle).Value().(func(macWindowMetrics))
	if !ok || onMetrics == nil {
		return
	}
	onMetrics(macWindowMetrics{
		BarHeightDp: float32(barH),
		ZoomButtonFrame: image.Rect(
			int(math.Floor(float64(zx0))),
			int(math.Floor(float64(zy0))),
			int(math.Ceil(float64(zx1))),
			int(math.Ceil(float64(zy1))),
		),
	})
}

//export fugueReleaseTitleBarHandle
func fugueReleaseTitleBarHandle(handle C.uintptr_t) {
	cgo.Handle(handle).Delete()
}
