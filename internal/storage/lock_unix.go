//go:build unix

package storage

import (
	"errors"
	"math"
	"os"

	"golang.org/x/sys/unix"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

func fileDescriptor(f *os.File) (int, error) {
	fd := f.Fd()
	if fd > uintptr(math.MaxInt) {
		return 0, errcode.New(errcode.Internal, "file descriptor overflows int")
	}
	return int(fd), nil
}

func lockFileExclusive(f *os.File) error {
	fd, err := fileDescriptor(f)
	if err != nil {
		return err
	}
	if flockErr := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); flockErr != nil {
		if errors.Is(flockErr, unix.EWOULDBLOCK) || errors.Is(flockErr, unix.EAGAIN) {
			return ErrDataDirLocked
		}
		return errcode.Wrap(errcode.Internal, "flock exclusive non-blocking", flockErr)
	}
	return nil
}

func unlockFile(f *os.File) error {
	fd, err := fileDescriptor(f)
	if err != nil {
		return err
	}
	if flockErr := unix.Flock(fd, unix.LOCK_UN); flockErr != nil {
		return errcode.Wrap(errcode.Internal, "flock unlock", flockErr)
	}
	return nil
}
