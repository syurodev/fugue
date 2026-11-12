//go:build !windows

package workspace

import (
	"os"
	"syscall"
)

func fileSysUID(info os.FileInfo) (int, error) {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(stat.Uid), nil
	}
	return os.Getuid(), nil
}
