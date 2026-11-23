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

func validateExecutable(path string) error {
	return validateExecutableFor(path, int64(os.Getuid()), int64(os.Getgid()))
}

// validateExecutableFor checks the executable and its directory for the app
// user uid with primary group gid. Both must be owned by the user, the
// directory a real directory and the executable a regular executable file.
// Group-writable is accepted only when the group is the user's own, the same
// trust the ancestors get (see validateAncestors): the usual 002 umask with
// per-user groups makes every build output 0775 in a 0775 directory.
// Other-writable is never accepted. Like the ancestors, this trusts that the
// user's primary group has no other members; Fugue does not read the group
// database to confirm it.
func validateExecutableFor(path string, uid, gid int64) error {
	if err := validateAncestors(filepath.Dir(path), uid, gid); err != nil {
		return err
	}
	return validateEntries(path, uid, gid)
}

// validateEntries checks the executable and its directory, the part of
// validateExecutableFor that does not depend on the ancestors.
func validateEntries(path string, uid, gid int64) error {
	for _, name := range []string{filepath.Dir(path), path} {
		info, err := os.Lstat(name)
		if err != nil {
			return fmt.Errorf("host executable metadata: %w", err)
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int64(st.Uid) != uid {
			return ErrUnsafeExecutable
		}
		if name == path && (!info.Mode().IsRegular() || info.Mode().Perm()&0o100 == 0) {
			return ErrUnsafeExecutable
		}
		if name != path && !info.IsDir() {
			return ErrUnsafeExecutable
		}
		if writableByOthers(info.Mode().Perm(), int64(st.Gid), gid) {
			return &WritableExecutableError{Path: path}
		}
	}
	return nil
}

// writableByOthers reports a mode other users can write: other-writable, or
// group-writable for a group other than the user's own.
func writableByOthers(perm os.FileMode, fileGID, gid int64) bool {
	return perm&0o002 != 0 || (perm&0o020 != 0 && fileGID != gid)
}

// validateAncestors walks every directory above the executable's directory,
// symlinks resolved, up to the root. Each must belong to the app user or to
// root, and one that other users can write (other-writable, or
// group-writable for a group other than the user's own, as the usual 002
// umask with per-user groups gives) must carry the sticky bit like /tmp, so
// no other user can rename the tree under the executable between the check
// and the launch.
func validateAncestors(dir string, uid, gid int64) error {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return fmt.Errorf("host executable directory: %w", err)
	}
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
	if writableByOthers(perm, int64(st.Gid), gid) && info.Mode()&os.ModeSticky == 0 {
		return ErrUnsafeExecutable
	}
	return nil
}
