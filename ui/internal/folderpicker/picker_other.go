//go:build !darwin && !linux

package folderpicker

import (
	"context"
	"errors"
	"sync/atomic"
)

// platformProbe answers nothing: no packaged helper runs on other platforms,
// so the dialogs fall back to the typed path field.
func platformProbe(context.Context) Availability { return Availability{} }

// errPortalOffPlatform marks the portal hook on the platforms whose probe
// never offers one; the pick never reaches it here.
var errPortalOffPlatform = errors.New("the desktop portal chooser runs on Linux only")

// pickPortal never runs: no portal is selected off Linux, and the probe never
// sets Availability.Portal here. It exists so the picker's constructor
// compiles on every platform.
func pickPortal(context.Context, *atomic.Uint64, Request) (string, bool, error) {
	return "", false, errPortalOffPlatform
}
