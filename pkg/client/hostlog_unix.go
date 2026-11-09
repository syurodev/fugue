//go:build linux || darwin

package client

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

func privateInfo(info fs.FileInfo, dir bool) error {
	if err := trustPrivate(info, dir); err != nil {
		return err
	}
	expected := fs.FileMode(0o600)
	if dir {
		expected = 0o700
	}
	if info.Mode().Perm() != expected {
		return ErrUntrustedHost
	}
	return nil
}

// OpenHostLog opens the private, bounded-output host log.
func OpenHostLog(dataDir string) (*os.File, error) {
	root, err := backgroundRoot(dataDir)
	if err != nil {
		return nil, err
	}
	defer closeBestEffort(root)
	if err = root.Mkdir("logs", 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	logs, err := root.OpenRoot("logs")
	if err != nil {
		return nil, fmt.Errorf("open log directory: %w", err)
	}
	defer closeBestEffort(logs)
	info, err := logs.Stat(".")
	if err == nil {
		err = privateInfo(info, true)
	}
	if err != nil {
		return nil, fmt.Errorf("verify log directory: %w", err)
	}
	return openCheckedLog(logs)
}

func openCheckedLog(logs *os.Root) (*os.File, error) {
	f, err := logs.OpenFile("host.log", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open host log: %w", err)
	}
	info, err := f.Stat()
	if err == nil {
		err = privateInfo(info, false)
	}
	if err == nil {
		err = f.Truncate(0)
	}
	if err != nil {
		closeBestEffort(f)
		return nil, fmt.Errorf("verify host log: %w", err)
	}
	return f, nil
}
