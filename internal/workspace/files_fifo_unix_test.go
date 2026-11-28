//go:build unix

package workspace

import (
	"path/filepath"
	"syscall"
	"testing"
)

// TestHashFileRefusesAFIFO pins that an anchor on a named pipe inside a root
// is refused before it is opened: opening a FIFO with no writer blocks, and
// anchors come from agent writes.
func TestHashFileRefusesAFIFO(t *testing.T) {
	t.Parallel()
	f := newFileReadFixture(t)
	if err := syscall.Mkfifo(filepath.Join(f.root, "pipe"), 0o600); err != nil {
		t.Fatalf("make the fifo: %v", err)
	}
	if _, err := f.svc.HashFile(t.Context(), f.wsID, testMainRoot, "pipe"); CodeOf(err) != ErrFileNotFound {
		t.Fatalf("HashFile(fifo) code = %v, want %v", CodeOf(err), ErrFileNotFound)
	}
}
