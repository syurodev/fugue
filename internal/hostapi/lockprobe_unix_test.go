//go:build unix

package hostapi

import (
	"os"
	"syscall"
	"testing"
)

// assertDataDirLockReleased proves the data-directory lock is no longer held:
// an exclusive non-blocking flock on the lock file succeeds only after the
// host released it. It replaces a storage import, which the layer table does
// not grant to this package's tests.
func assertDataDirLockReleased(t *testing.T, dataDir string) {
	t.Helper()
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		t.Fatalf("open the data directory: %v", err)
	}
	defer func() {
		if rerr := root.Close(); rerr != nil {
			t.Errorf("close the data directory: %v", rerr)
		}
	}()
	f, err := root.Open("fugue.lock")
	if err != nil {
		t.Fatalf("open the lock file: %v", err)
	}
	defer func() {
		if cerr := f.Close(); cerr != nil {
			t.Errorf("close the lock file: %v", cerr)
		}
	}()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("the data-directory lock is still held: %v", err)
	}
	if uerr := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); uerr != nil {
		t.Errorf("unlock the lock file: %v", uerr)
	}
}
