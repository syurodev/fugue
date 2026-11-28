//go:build darwin

package folderpicker

import (
	"context"
	"errors"
	"os/exec"
	"sync/atomic"
)

// platformProbe answers what macOS offers the picker: osascript, when it is
// on the PATH. It runs once, at the picker's Start.
func platformProbe(context.Context) Availability {
	av := Availability{}
	_, err := exec.LookPath("osascript")
	av.OSAScript = err == nil
	return av
}

// errPortalOffPlatform marks the portal hook on the platforms whose probe
// never offers one; the pick never reaches it here.
var errPortalOffPlatform = errors.New("the desktop portal chooser runs on Linux only")

// pickPortal never runs: no portal is selected off Linux, and the probe never
// sets Availability.Portal here. It exists so the picker's constructor
// compiles on every platform.
func pickPortal(context.Context, *atomic.Uint64) (string, bool, error) {
	return "", false, errPortalOffPlatform
}
