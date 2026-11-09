//go:build unix

package client

import (
	"fmt"
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func trustPrivate(info fs.FileInfo, directory bool) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int64(st.Uid) != int64(os.Getuid()) || info.Mode().Perm()&0o077 != 0 || (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return fmt.Errorf("discovery ownership or permissions: %w", ErrUntrustedHost)
	}
	return nil
}

func openTrustedDiscoveryFile(dataDir string) (*os.File, error) {
	fd, err := unix.Open(dataDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open trusted discovery: %w", err)
	}
	dir := os.NewFile(uintptr(fd), "host directory")
	defer closeBestEffort(dir)
	info, err := dir.Stat()
	if err == nil {
		err = trustPrivate(info, true)
	}
	if err != nil {
		return nil, fmt.Errorf("open trusted discovery: %w", err)
	}
	fd, err = unix.Openat(fd, hostFileName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open trusted discovery: %w", err)
	}
	return os.NewFile(uintptr(fd), hostFileName), nil
}
