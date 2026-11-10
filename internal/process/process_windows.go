//go:build !unix

package process

import (
	"context"
)

// SystemdProbe is a fail-closed stub on non-Unix platforms.
type SystemdProbe struct{}

// NewSystemdProbe returns a no-op probe stub on non-Unix platforms.
func NewSystemdProbe() *SystemdProbe {
	return &SystemdProbe{}
}

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

// Identity returns an empty Identity on unsupported platforms.
func (p *Process) Identity() Identity {
	return Identity{}
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

// WriteLine returns ErrUnsupportedPlatform on unsupported platforms.
func (p *Process) WriteLine(line []byte) error {
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

// Reap returns ErrUnsupportedPlatform on unsupported platforms.
func Reap(ctx context.Context, id Identity, opts *ReapOptions) (StopProof, error) {
	return StopProof{
		Status: StopProofUnknown,
		Reason: "windows_unsupported",
	}, ErrUnsupportedPlatform
}
