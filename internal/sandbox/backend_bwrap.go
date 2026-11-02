//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
)

func defaultBackend() Backend {
	return NewBwrapBackend()
}

// bwrapContainsDescendants holds because buildBwrapArgs always passes
// --unshare-pid, so the kernel terminates every process in the PID namespace
// when its init exits.
const bwrapContainsDescendants = true

// BwrapBackend starts Linux commands inside a bubblewrap sandbox.
type BwrapBackend struct {
	lookPath func(string) (string, error)
}

// NewBwrapBackend returns a bubblewrap backend for Linux.
func NewBwrapBackend() *BwrapBackend {
	return &BwrapBackend{lookPath: exec.LookPath}
}

// Name returns the backend identifier.
func (*BwrapBackend) Name() string { return "bwrap" }

func (b *BwrapBackend) resolveBinary() (string, error) {
	lookup := exec.LookPath
	if b != nil && b.lookPath != nil {
		lookup = b.lookPath
	}
	path, err := lookup("bwrap")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", ErrUnavailable
		}
		return "", fmt.Errorf("locate bwrap: %w", err)
	}
	return path, nil
}

// Available reports whether bwrap is present on the host.
func (b *BwrapBackend) Available(context.Context) error {
	_, err := b.resolveBinary()
	return err
}

func (*BwrapBackend) buildCmd(ctx context.Context, bwrapPath string, p *Policy, c Cmd) *exec.Cmd {
	cp := p.withDefaults()
	bwrapArgs := buildBwrapArgs(&cp, c)
	cmd := exec.CommandContext(ctx, "/usr/bin/bwrap")
	cmd.Path = bwrapPath
	cmd.Args = append([]string{bwrapPath}, bwrapArgs...)
	cmd.Err = nil
	return cmd
}

// Start launches a command under bubblewrap with buffered stdout and stderr.
func (b *BwrapBackend) Start(ctx context.Context, p *Policy, c Cmd) (*Proc, error) {
	if err := p.ValidateCmd(c); err != nil {
		return nil, err
	}
	bwrapPath, err := b.resolveBinary()
	if err != nil {
		return nil, err
	}
	cmd := b.buildCmd(ctx, bwrapPath, p, c)
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	proc := NewProcWithBuffers(cmd, stdout, stderr)
	proc.contained = bwrapContainsDescendants
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start bwrap: %w", err)
	}
	return proc, nil
}

// StartPiped launches a command under bubblewrap with live stdin and stdout pipes.
func (b *BwrapBackend) StartPiped(ctx context.Context, p *Policy, c Cmd) (*Proc, error) {
	if err := p.ValidateCmd(c); err != nil {
		return nil, err
	}
	bwrapPath, err := b.resolveBinary()
	if err != nil {
		return nil, err
	}
	cmd := b.buildCmd(ctx, bwrapPath, p, c)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("bwrap stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		closePipeQuietly(stdin)
		return nil, fmt.Errorf("bwrap stdout pipe: %w", err)
	}
	stderr := &bytes.Buffer{}
	proc := NewProcPiped(cmd, stdin, stdout, stderr)
	proc.contained = bwrapContainsDescendants
	if err := cmd.Start(); err != nil {
		closePipeQuietly(stdin)
		closePipeQuietly(stdout)
		return nil, fmt.Errorf("start piped bwrap: %w", err)
	}
	return proc, nil
}

func buildBwrapArgs(p *Policy, c Cmd) []string {
	return buildBwrapArgsWith(p, c, defaultBwrapEnv())
}
