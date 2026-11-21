//go:build darwin

package app

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// errUnusableBlockSize guards the block size before the free-space math
// multiplies it into a meaningless byte count.
var errUnusableBlockSize = errors.New("statfs: unusable block size")

func hostFreeDiskBytes(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", path, err)
	}
	if st.Bsize == 0 {
		return 0, fmt.Errorf("%w: %s reports bsize %d", errUnusableBlockSize, path, st.Bsize)
	}
	return st.Bavail * uint64(st.Bsize), nil
}
