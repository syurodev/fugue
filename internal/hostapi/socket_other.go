//go:build !unix

package hostapi

import "errors"

// errDirCheckUnsupported is returned when the platform has no descriptor-based
// directory check. Stage A supports Linux and macOS only; this fails closed so
// the Windows nightly build compiles and never binds a socket unchecked.
var errDirCheckUnsupported = errors.New("this platform has no supported socket directory check")

// osDirs fails closed on unsupported platforms.
type osDirs struct{}

func (osDirs) Open(string) (Dir, error) { return nil, errDirCheckUnsupported }
