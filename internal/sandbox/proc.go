package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// Backend starts sandboxed processes according to a validated policy.
type Backend interface {
	Name() string
	Available(ctx context.Context) error
	Start(ctx context.Context, p *Policy, c Cmd) (*Proc, error)
	StartPiped(ctx context.Context, p *Policy, c Cmd) (*Proc, error)
}

// Runner validates policies and delegates process launches to a Backend.
type Runner struct {
	b Backend
}

// NewRunner returns a Runner that uses b to start processes.
func NewRunner(b Backend) *Runner {
	return &Runner{b: b}
}

// DefaultRunner returns a Runner backed by the host platform's default Backend.
func DefaultRunner() *Runner {
	return NewRunner(defaultBackend())
}

// Available reports whether the runner's backend can start processes.
func (r *Runner) Available(ctx context.Context) error {
	if r == nil || r.b == nil {
		return ErrUnavailable
	}
	if err := r.b.Available(ctx); err != nil {
		return fmt.Errorf("sandbox available: %w", err)
	}
	return nil
}

// Start launches a sandboxed process with buffered stdout and stderr.
func (r *Runner) Start(ctx context.Context, p *Policy, c Cmd) (*Proc, error) {
	if err := p.ValidateCmd(c); err != nil {
		return nil, err
	}
	if r == nil || r.b == nil {
		return nil, ErrUnavailable
	}
	cp := p.withDefaults()
	proc, err := r.b.Start(ctx, &cp, c)
	if err != nil {
		return nil, fmt.Errorf("sandbox start: %w", err)
	}
	return proc, nil
}

// StartPiped launches a sandboxed process with live stdin and stdout pipes.
func (r *Runner) StartPiped(ctx context.Context, p *Policy, c Cmd) (*Proc, error) {
	if err := p.ValidateCmd(c); err != nil {
		return nil, err
	}
	if r == nil || r.b == nil {
		return nil, ErrUnavailable
	}
	cp := p.withDefaults()
	proc, err := r.b.StartPiped(ctx, &cp, c)
	if err != nil {
		return nil, fmt.Errorf("sandbox start piped: %w", err)
	}
	return proc, nil
}

// pipedWaitDelay bounds how long Wait keeps copying stderr once the process
// itself has exited.
const pipedWaitDelay = 2 * time.Second

// Proc wraps a started sandboxed exec.Cmd.
type Proc struct {
	waitOnce  sync.Once
	waitErr   error
	exited    chan struct{}
	contained bool
	cmd       *exec.Cmd
	stdoutBuf *bytes.Buffer
	stderrBuf *bytes.Buffer
	stdinW    io.WriteCloser
	stdoutR   io.ReadCloser
}

// NewProc returns a Proc for an already-started command.
func NewProc(cmd *exec.Cmd) *Proc {
	return &Proc{cmd: cmd, exited: make(chan struct{})}
}

// NewProcWithBuffers captures stdout and stderr on cmd for later inspection.
func NewProcWithBuffers(cmd *exec.Cmd, stdout, stderr *bytes.Buffer) *Proc {
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	proc := NewProc(cmd)
	proc.stdoutBuf = stdout
	proc.stderrBuf = stderr
	return proc
}

// NewProcPiped wraps a command started with stdin and stdout pipes.
func NewProcPiped(cmd *exec.Cmd, stdin io.WriteCloser, stdout io.ReadCloser, stderr *bytes.Buffer) *Proc {
	if stderr != nil {
		cmd.Stderr = stderr
	}
	cmd.WaitDelay = pipedWaitDelay
	proc := NewProc(cmd)
	proc.stdinW = stdin
	proc.stdoutR = stdout
	proc.stderrBuf = stderr
	return proc
}

// Output returns copies of captured stdout and stderr when buffers were set.
func (p *Proc) Output() (stdout, stderr []byte) {
	if p.stdoutBuf != nil {
		stdout = append([]byte(nil), p.stdoutBuf.Bytes()...)
	}
	if p.stderrBuf != nil {
		stderr = append([]byte(nil), p.stderrBuf.Bytes()...)
	}
	return stdout, stderr
}

// Wait waits for the process to exit and closes Exited once reaped.
func (p *Proc) Wait() error {
	p.waitOnce.Do(func() {
		if err := p.cmd.Wait(); err != nil {
			p.waitErr = fmt.Errorf("sandbox wait: %w", err)
		}
		if p.exited != nil {
			close(p.exited)
		}
	})
	return p.waitErr
}

// Exited returns a channel closed once Wait has reaped the process.
func (p *Proc) Exited() <-chan struct{} {
	return p.exited
}

// ContainsDescendants reports whether reaping this process guarantees that
// every descendant process inside the jail has also terminated.
func (p *Proc) ContainsDescendants() bool {
	return p.contained
}

// Kill sends SIGKILL to the underlying supervisor process.
func (p *Proc) Kill() error {
	if p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	if err := p.cmd.Process.Kill(); err != nil {
		return fmt.Errorf("sandbox kill: %w", err)
	}
	return nil
}

// PID returns the operating-system process ID, or 0 if not running.
func (p *Proc) PID() int {
	if p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// Stdin returns the configured stdin reader on the underlying command.
func (p *Proc) Stdin() io.Reader {
	return p.cmd.Stdin
}

// Stdout returns the configured stdout writer on the underlying command.
func (p *Proc) Stdout() io.Writer {
	return p.cmd.Stdout
}

// Stderr returns the configured stderr writer on the underlying command.
func (p *Proc) Stderr() io.Writer {
	return p.cmd.Stderr
}

// StdinWriter returns the write end of the stdin pipe from StartPiped.
func (p *Proc) StdinWriter() io.WriteCloser {
	return p.stdinW
}

// StdoutReader returns the read end of the stdout pipe from StartPiped.
func (p *Proc) StdoutReader() io.ReadCloser {
	return p.stdoutR
}

func closePipeQuietly(c io.Closer) {
	if c == nil {
		return
	}
	if err := c.Close(); err != nil {
		return
	}
}
