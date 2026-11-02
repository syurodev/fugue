//go:build darwin

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
)

func defaultBackend() Backend {
	return NewSeatbeltBackend()
}

type seatbeltValidationRunner struct{}

func (seatbeltValidationRunner) Run(ctx context.Context, p *Policy, profile string) error {
	cmd := exec.CommandContext(ctx, "/usr/bin/sandbox-exec")
	cmd.Path = "/usr/bin/sandbox-exec"
	cmd.Args = []string{"/usr/bin/sandbox-exec", "-p", profile, "--", "/usr/bin/true"}
	cmd.Dir = p.WorkDir
	cmd.Env = seatbeltProcessEnv(p, hostHomeDir())
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("validate seatbelt profile: %w", err)
	}
	return nil
}

// SeatbeltBackend starts macOS commands inside a sandbox-exec Seatbelt profile.
type SeatbeltBackend struct {
	lookPath func(string) (string, error)
	gate     *unixProfileGate
}

// NewSeatbeltBackend returns a Seatbelt backend for macOS.
func NewSeatbeltBackend() *SeatbeltBackend {
	return &SeatbeltBackend{
		lookPath: exec.LookPath,
		gate:     &unixProfileGate{runner: seatbeltValidationRunner{}},
	}
}

// Name returns the backend identifier.
func (*SeatbeltBackend) Name() string { return "seatbelt" }

func (b *SeatbeltBackend) resolveBinary() (string, error) {
	lookup := exec.LookPath
	if b != nil && b.lookPath != nil {
		lookup = b.lookPath
	}
	path, err := lookup("sandbox-exec")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", ErrUnavailable
		}
		return "", fmt.Errorf("locate sandbox-exec: %w", err)
	}
	return path, nil
}

func (b *SeatbeltBackend) profileFor(ctx context.Context, p *Policy) string {
	if b != nil && b.gate != nil {
		return b.gate.profile(ctx, p)
	}
	gate := unixProfileGate{runner: seatbeltValidationRunner{}}
	return gate.profile(ctx, p)
}

// Available reports whether sandbox-exec is present on the host.
func (b *SeatbeltBackend) Available(context.Context) error {
	_, err := b.resolveBinary()
	return err
}

func (b *SeatbeltBackend) buildCmd(ctx context.Context, sandboxExec string, p *Policy, c Cmd) *exec.Cmd {
	cp := p.withDefaults()
	args := make([]string, 0, 4+len(c.Args))
	args = append(args, sandboxExec, "-p", b.profileFor(ctx, &cp), "--", c.Path)
	args = append(args, c.Args...)

	cmd := exec.CommandContext(ctx, "/usr/bin/sandbox-exec")
	cmd.Path = sandboxExec
	cmd.Args = args
	cmd.Err = nil
	cmd.Dir = cp.EffectiveChdir(c)
	if env := seatbeltProcessEnv(&cp, hostHomeDir()); env != nil {
		cmd.Env = env
	}
	return cmd
}

// Start launches a command under Seatbelt with buffered stdout and stderr.
func (b *SeatbeltBackend) Start(ctx context.Context, p *Policy, c Cmd) (*Proc, error) {
	if err := p.ValidateCmd(c); err != nil {
		return nil, err
	}
	sandboxExec, err := b.resolveBinary()
	if err != nil {
		return nil, err
	}
	cmd := b.buildCmd(ctx, sandboxExec, p, c)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	proc := NewProcWithBuffers(cmd, stdout, stderr)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start sandbox-exec: %w", err)
	}
	return proc, nil
}

// StartPiped launches a command under Seatbelt with live stdin and stdout pipes.
func (b *SeatbeltBackend) StartPiped(ctx context.Context, p *Policy, c Cmd) (*Proc, error) {
	if err := p.ValidateCmd(c); err != nil {
		return nil, err
	}
	sandboxExec, err := b.resolveBinary()
	if err != nil {
		return nil, err
	}
	cmd := b.buildCmd(ctx, sandboxExec, p, c)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("seatbelt stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		closePipeQuietly(stdin)
		return nil, fmt.Errorf("seatbelt stdout pipe: %w", err)
	}
	stderr := &bytes.Buffer{}
	proc := NewProcPiped(cmd, stdin, stdout, stderr)
	if err := cmd.Start(); err != nil {
		closePipeQuietly(stdin)
		closePipeQuietly(stdout)
		return nil, fmt.Errorf("start piped sandbox-exec: %w", err)
	}
	return proc, nil
}
