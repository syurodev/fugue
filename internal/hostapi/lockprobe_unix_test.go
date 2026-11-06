//go:build unix

package hostapi

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// assertDataDirLockReleased proves the data-directory lock is no longer held:
// an exclusive non-blocking flock on the lock file succeeds only after the
// host released it. It replaces a storage import, which the layer table does
// not grant to this package's tests.
func assertDataDirLockReleased(t *testing.T, dataDir string) {
	t.Helper()
	f, err := os.Open(filepath.Join(dataDir, "fugue.lock"))
	if err != nil {
		t.Fatalf("open the lock file: %v", err)
	}
	defer func() { _ = f.Close() }()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("the data-directory lock is still held: %v", err)
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
