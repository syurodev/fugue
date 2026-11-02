//go:build linux

package storage

import (
	"fmt"

	"golang.org/x/sys/unix"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

func checkLocalFilesystem(canonicalDir string) error {
	var st unix.Statfs_t
	if err := unix.Statfs(canonicalDir, &st); err != nil {
		return errcode.Wrap(errcode.Internal, "statfs data directory "+canonicalDir, err)
	}
	if fsName, isNet := isLinuxNetworkFSMagic(st.Type); isNet {
		return errcode.New(
			errcode.FailedPrecondition,
			fmt.Sprintf("data directory %s is on unsupported network filesystem %s", canonicalDir, fsName),
		)
	}
	return nil
}
