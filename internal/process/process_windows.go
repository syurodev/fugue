//go:build !unix

package process

import (
	"context"
)

// Process is a fail-closed stub on platforms without Unix process group support.
type Process struct{}

// Spawn returns ErrUnsupportedPlatform on non-Unix systems.
func Spawn(ctx context.Context, opts *Options) (*Process, error) {
	return nil, ErrUnsupportedPlatform
}

// PID returns 0 on unsupported platforms.
func (p *Process) PID() int {
	return 0
}

// Stdout returns nil on unsupported platforms.
func (p *Process) Stdout() *LineReader {
	return nil
}

// StderrRing returns nil on unsupported platforms.
func (p *Process) StderrRing() *RingBuffer {
	return nil
}

// CloseStdin returns ErrUnsupportedPlatform on unsupported platforms.
func (p *Process) CloseStdin() error {
	return ErrUnsupportedPlatform
}

// Wait returns ErrUnsupportedPlatform on unsupported platforms.
func (p *Process) Wait() error {
	return ErrUnsupportedPlatform
}

// Stop returns ErrUnsupportedPlatform on unsupported platforms.
func (p *Process) Stop(ctx context.Context) (StopProof, error) {
	return StopProof{
		Status: StopProofUnknown,
		Reason: "windows_unsupported",
	}, ErrUnsupportedPlatform
}
