//go:build linux || darwin

package hostconn

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrUnsafeExecutable means the host executable or its directory is not trusted.
var ErrUnsafeExecutable = errors.New("unsafe host executable")

func validateExecutable(path string) error { return validateExecutableForUID(path, int64(os.Getuid())) }

func validateExecutableForUID(path string, uid int64) error {
	for _, name := range []string{filepath.Dir(path), path} {
		info, err := os.Lstat(name)
		if err != nil {
			return fmt.Errorf("host executable metadata: %w", err)
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int64(st.Uid) != uid || info.Mode().Perm()&0o022 != 0 {
			return ErrUnsafeExecutable
		}
		if name == path && (!info.Mode().IsRegular() || info.Mode().Perm()&0o100 == 0) {
			return ErrUnsafeExecutable
		}
		if name != path && !info.IsDir() {
			return ErrUnsafeExecutable
		}
	}
	return nil
}
