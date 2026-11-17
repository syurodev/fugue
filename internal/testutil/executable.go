package testutil

import (
	"os"
	"syscall"
)

// WriteExecutable writes an executable fixture (a fake vendor CLI, a fake git)
// and returns only once the file is closed. It holds syscall.ForkLock for
// reading across the write: a fork elsewhere in the process would otherwise
// inherit the open write descriptor and the later exec would fail with ETXTBSY
// ("text file busy") on Linux.
func WriteExecutable(t tb, path string, content []byte) {
	t.Helper()
	// The executable bit is what lets the OS run the fixture.
	const mode os.FileMode = 0o700
	syscall.ForkLock.RLock()
	err := os.WriteFile(path, content, mode)
	syscall.ForkLock.RUnlock()
	if err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}
