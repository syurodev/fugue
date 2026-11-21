//go:build !darwin && !linux

package app

import "errors"

var errDiskFreeUnsupported = errors.New("free disk space inspection unsupported on this OS")

func hostFreeDiskBytes(path string) (uint64, error) {
	return 0, errDiskFreeUnsupported
}
