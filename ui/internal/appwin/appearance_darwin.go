//go:build darwin

package appwin

import "C"
import "runtime/cgo"

//export fugueAppearanceChanged
func fugueAppearanceChanged(handle uintptr) {
	h := cgo.Handle(handle)
	if cb, ok := h.Value().(func()); ok && cb != nil {
		cb()
	}
}
