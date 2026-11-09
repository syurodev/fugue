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
	if err := validateAncestors(filepath.Dir(path), uid); err != nil {
		return err
	}
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

// validateAncestors walks every directory above the executable's directory,
// symlinks resolved, up to the root. Each must belong to the app user or to
// root, and one that other users can write (other-writable, or
// group-writable for a group other than the user's own, as the usual 002
// umask with per-user groups gives) must carry the sticky bit like /tmp, so
// no other user can rename the tree under the executable between the check
// and the launch.
func validateAncestors(dir string, uid int64) error {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("host executable directory: %w", err)
	}
	gid := int64(os.Getgid())
	for parent := filepath.Dir(real); ; parent = filepath.Dir(parent) {
		if err := trustedAncestor(parent, uid, gid); err != nil {
			return err
		}
		if parent == filepath.Dir(parent) {
			return nil
		}
	}
}

// trustedAncestor checks one ancestor directory for validateAncestors.
func trustedAncestor(path string, uid, gid int64) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("host executable ancestor metadata: %w", err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || (int64(st.Uid) != uid && st.Uid != 0) {
		return ErrUnsafeExecutable
	}
	perm := info.Mode().Perm()
	sharedGroup := perm&0o020 != 0 && int64(st.Gid) != gid
	if (perm&0o002 != 0 || sharedGroup) && info.Mode()&os.ModeSticky == 0 {
		return ErrUnsafeExecutable
	}
	return nil
}
