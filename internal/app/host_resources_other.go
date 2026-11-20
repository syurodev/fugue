//go:build !darwin && !linux

package app

import "errors"

var errHostMemoryUnsupported = errors.New("host total memory inspection unsupported on this OS")

func hostTotalMemoryBytes() (uint64, error) {
	return 0, errHostMemoryUnsupported
}
