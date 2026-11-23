package hostconn

import (
	"errors"
	"path/filepath"
)

// WritableExecutableError means the host executable or its directory is owned
// by the app user but writable by others, which a chmod of those two paths
// fixes. It matches ErrUnsafeExecutable, so every caller that refuses an
// unsafe executable refuses this one too; the app's banner reads Path to name
// the fix.
type WritableExecutableError struct {
	// Path is the host executable; its directory is filepath.Dir(Path).
	Path string
}

func (e *WritableExecutableError) Error() string {
	return "unsafe host executable: " + e.Path + " or its directory is writable by others; run: " + e.Fix()
}

// Is lets errors.Is(err, ErrUnsafeExecutable) match.
func (e *WritableExecutableError) Is(target error) bool { return target == ErrUnsafeExecutable }

// Fix is the shell command that makes the executable and its directory
// trusted again.
func (e *WritableExecutableError) Fix() string {
	return "chmod 755 " + filepath.Dir(e.Path) + " " + e.Path
}

// recordRefusal keeps a refused writable executable for the banner.
func (m *Manager) recordRefusal(err error) {
	var refused *WritableExecutableError
	if !errors.As(err, &refused) {
		refused = nil
	}
	m.mu.Lock()
	m.refusal = refused
	m.mu.Unlock()
}

// Refusal returns the writable-executable refusal that stopped the host
// launch, so the app can name the path and the fix; nil when there is none.
func (m *Manager) Refusal() *WritableExecutableError {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.refusal
}
