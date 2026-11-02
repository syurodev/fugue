//go:build windows

package storage

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

func lockFileExclusive(f *os.File) error {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		1,
		0,
		&overlapped,
	)
	if err != nil {
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return ErrDataDirLocked
		}
		return errcode.Wrap(errcode.Internal, "LockFileEx exclusive non-blocking", err)
	}
	return nil
}

func unlockFile(f *os.File) error {
	var overlapped windows.Overlapped
	if err := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlapped); err != nil {
		return errcode.Wrap(errcode.Internal, "UnlockFileEx", err)
	}
	return nil
}
