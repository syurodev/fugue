//go:build linux || darwin

package client

import (
	"errors"
	"fmt"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

func backgroundRoot(dir string) (*os.Root, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create client data directory: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open client data directory: %w", err)
	}
	info, err := root.Stat(".")
	if err == nil {
		err = privateInfo(info, true)
	}
	if err != nil {
		closeBestEffort(root)
		return nil, fmt.Errorf("verify client data directory: %w", err)
	}
	return root, nil
}

// BackgroundEnabled reads the private marker, which defaults to absent.
func BackgroundEnabled(dir string) (bool, error) {
	root, err := backgroundRoot(dir)
	if err != nil {
		return false, err
	}
	defer closeBestEffort(root)
	info, err := root.Lstat("background.enabled")
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err == nil {
		err = privateInfo(info, false)
	}
	if err != nil {
		return false, fmt.Errorf("verify background setting: %w", err)
	}
	return true, nil
}

// SetBackgroundEnabled atomically creates or removes the private preference marker.
func SetBackgroundEnabled(dir string, enabled bool) error {
	root, err := backgroundRoot(dir)
	if err != nil {
		return err
	}
	defer closeBestEffort(root)
	if !enabled {
		err = root.Remove("background.enabled")
		if err == nil || errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("remove background setting: %w", err)
	}
	f, err := root.OpenFile("background.enabled", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, fs.ErrExist) {
		_, err = BackgroundEnabled(dir)
		return err
	}
	if err != nil {
		return fmt.Errorf("create background setting: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("close background setting: %w", err)
	}
	return nil
}

// BackgroundLock holds the per-data-directory background ownership lock.
type BackgroundLock struct{ file *os.File }

// TryBackgroundLock returns nil if another background process owns the lease.
func TryBackgroundLock(dir string) (*BackgroundLock, error) {
	root, err := backgroundRoot(dir)
	if err != nil {
		return nil, err
	}
	defer closeBestEffort(root)
	f, err := root.OpenFile("background.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open background lock: %w", err)
	}
	info, err := f.Stat()
	if err == nil {
		err = privateInfo(info, false)
	}
	if err != nil {
		closeBestEffort(f)
		return nil, fmt.Errorf("verify background lock: %w", err)
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		closeBestEffort(f)
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrBackgroundOwned
		}
		return nil, fmt.Errorf("acquire background lock: %w", err)
	}
	return &BackgroundLock{file: f}, nil
}

// Close releases the background ownership lock.
func (l *BackgroundLock) Close() error {
	if err := l.file.Close(); err != nil {
		return fmt.Errorf("close background lock: %w", err)
	}
	return nil
}
