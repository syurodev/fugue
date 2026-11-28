//go:build darwin

package folderpicker

import (
	"context"
	"errors"
	"os/exec"
)

// platformProbe answers what macOS offers the picker: osascript, when it is
// on the PATH. It runs once, on the picker's worker goroutine.
func platformProbe(context.Context) Availability {
	av := Availability{}
	_, err := exec.LookPath("osascript")
	av.OSAScript = err == nil
	return av
}

// pickPortal never runs: no portal is selected off Linux, and the probe never
// sets Availability.Portal here. It exists so the picker's constructor
// compiles on every platform.
func pickPortal(context.Context) (string, bool, error) {
	return "", false, errors.New("the desktop portal chooser runs on Linux only")
}
