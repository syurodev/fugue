//go:build darwin

package app

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func hostTotalMemoryBytes() (uint64, error) {
	total, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0, fmt.Errorf("read hw.memsize: %w", err)
	}
	return total, nil
}
