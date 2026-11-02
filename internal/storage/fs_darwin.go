//go:build darwin

package storage

import (
	"bytes"
	"fmt"

	"golang.org/x/sys/unix"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

func checkLocalFilesystem(canonicalDir string) error {
	var st unix.Statfs_t
	if err := unix.Statfs(canonicalDir, &st); err != nil {
		return errcode.Wrap(errcode.Internal, fmt.Sprintf("statfs data directory %s", canonicalDir), err)
	}
	raw := make([]byte, 0, len(st.Fstypename))
	for _, b := range st.Fstypename {
		if b == 0 {
			break
		}
		raw = append(raw, byte(b))
	}
	fsType := string(bytes.TrimSpace(raw))
	if st.Flags&unix.MNT_LOCAL == 0 || isNetworkFSTypeName(fsType) {
		return errcode.New(
			errcode.FailedPrecondition,
			fmt.Sprintf("data directory %s is on unsupported network filesystem %q", canonicalDir, fsType),
		)
	}
	return nil
}
