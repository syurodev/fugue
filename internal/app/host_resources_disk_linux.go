//go:build linux

package app

import (
	"errors"
	"fmt"
	"syscall"
)

// errUnusableBlockSize guards the signed block size before the free-space
// math turns it into a wrapped-around byte count.
var errUnusableBlockSize = errors.New("statfs: unusable block size")

func hostFreeDiskBytes(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", path, err)
	}
	// Bsize is the signed filesystem block size; a non-positive value makes
	// the free-space math meaningless, so the port refuses it instead of
	// wrapping around (gosec G115).
	if st.Bsize <= 0 {
		return 0, fmt.Errorf("%w: %s reports bsize %d", errUnusableBlockSize, path, st.Bsize)
	}
	return st.Bavail * uint64(st.Bsize), nil
}
