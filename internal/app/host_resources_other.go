//go:build !darwin && !linux

package app

import "fmt"

func hostTotalMemoryBytes() (uint64, error) {
	return 0, fmt.Errorf("host total memory inspection unsupported on this OS")
}
