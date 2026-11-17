package testutil_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/syurodev/fugue/internal/testutil"
)

// The writer takes the fork lock for the write only; a fork (writer lock)
// must be able to proceed once it returns.
func TestFakeExecutableWriterHoldsTheForkLock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake")
	testutil.WriteExecutable(t, path, []byte("#!/bin/sh\n"))

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("mode = %v, want the owner execute bit", info.Mode())
	}

	// The lock is free again afterwards: a fork would take the writer lock,
	// and a leaked reader would block it for the rest of the process.
	if !syscall.ForkLock.TryLock() {
		t.Fatal("the writer left syscall.ForkLock held")
	}
	syscall.ForkLock.Unlock()
}
