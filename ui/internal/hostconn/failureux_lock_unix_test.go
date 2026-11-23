//go:build unix

package hostconn

import (
	"os"
	"syscall"
	"testing"
)

// holdDataDirLock takes the exclusive lock the host takes on its data
// directory (the same flock on fugue.lock), as another process holding it
// would; the lock lives until the test ends.
func holdDataDirLock(t *testing.T, dataDir string) {
	t.Helper()
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeHandle(root)
	f, err := root.OpenFile("fugue.lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeHandle(f) })
	fd := f.Fd()
	if fd > uintptr(^uint(0)>>1) {
		t.Fatal("lock file descriptor overflows int")
	}
	if err := syscall.Flock(int(fd), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("hold the data directory lock: %v", err)
	}
}
