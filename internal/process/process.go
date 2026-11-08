//go:build unix

package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

// Process wraps an OS vendor CLI process running in its own process group.
type Process struct {
	cmd          *exec.Cmd
	clock        clock.Clock
	interrupt    func(context.Context) error
	onEscalation func(string)

	stdin      io.WriteCloser
	stdoutPipe *outputPipe
	stdoutR    *LineReader
	stderrR    *os.File
	stderrRing *RingBuffer

	stderrTask *supervise.Task
	waitTask   *supervise.Task

	scope             *cgroupScope
	tracker           *descendantTracker
	identity          Identity
	uncontainedReason string

	exitCh  chan struct{}
	waitErr error

	mu sync.Mutex
}

// Spawn starts a new process session in its own process group with explicit argv, cwd, and env.
func Spawn(ctx context.Context, opts *Options) (*Process, error) {
	if opts == nil || len(opts.Argv) == 0 {
		return nil, errcode.New(errcode.InvalidArgument, "process spawn requires at least one argv element")
	}

	clk := opts.Clock
	if clk == nil {
		clk = clock.Real{}
	}

	userArgv0 := opts.Argv[0]
	if _, err := resolveExecutable(userArgv0); err != nil {
		return nil, err
	}

	effectiveOpts := *opts
	effectiveOpts.Clock = clk
	launchArgv, unitName, usedSystemd, err := prepareSpawnArgv(ctx, &effectiveOpts)
	if err != nil {
		return nil, err
	}

	path, err := exec.LookPath(launchArgv[0])
	if err != nil {
		path = launchArgv[0]
	}

	cmd := &exec.Cmd{
		Path:        path,
		Args:        launchArgv,
		Dir:         effectiveOpts.Cwd,
		Env:         effectiveOpts.Env, // explicit: no inheritance from host process
		SysProcAttr: sysProcAttr(),
	}

	pipes, err := setupPipes(cmd, userArgv0)
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		pipes.closeAll()
		return nil, classifySpawnError(userArgv0, err)
	}

	// Close write ends in the parent so that readers receive io.EOF once
	// the child closes them on exit.
	closeQuietly(pipes.stdoutW)
	closeQuietly(pipes.stderrW)

	p := newProcess(cmd, clk, opts, pipes.stdin, pipes.stdoutR)
	p.stderrR = pipes.stderrR
	startStderrDrain(ctx, p, pipes.stderrR)
	containmentErr := attachPlatformContainment(ctx, p, &effectiveOpts, unitName, usedSystemd)
	startWaitTask(ctx, p)
	if containmentErr != nil {
		if _, stopErr := p.Stop(ctx); stopErr != nil {
			return nil, errors.Join(containmentErr, stopErr)
		}
		return nil, containmentErr
	}
	return p, nil
}

func resolveExecutable(argv0 string) (string, error) {
	path, err := exec.LookPath(argv0)
	if err != nil {
		return "", classifySpawnError(argv0, err)
	}
	return path, nil
}

type processPipes struct {
	stdin   io.WriteCloser
	stdoutR *os.File
	stdoutW *os.File
	stderrR *os.File
	stderrW *os.File
}

func (pp *processPipes) closeAll() {
	closeQuietly(pp.stdin)
	closeQuietly(pp.stdoutR)
	closeQuietly(pp.stdoutW)
	closeQuietly(pp.stderrR)
	closeQuietly(pp.stderrW)
}

func closeQuietly(c io.Closer) {
	if c != nil {
		if err := c.Close(); err != nil {
			return
		}
	}
}

func setupPipes(cmd *exec.Cmd, argv0 string) (*processPipes, error) {
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, classifySpawnError(argv0, err)
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		closeQuietly(stdinPipe)
		return nil, classifySpawnError(argv0, err)
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		closeQuietly(stdinPipe)
		closeQuietly(stdoutR)
		closeQuietly(stdoutW)
		return nil, classifySpawnError(argv0, err)
	}
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW
	return &processPipes{
		stdin:   stdinPipe,
		stdoutR: stdoutR,
		stdoutW: stdoutW,
		stderrR: stderrR,
		stderrW: stderrW,
	}, nil
}

func newProcess(
	cmd *exec.Cmd,
	clk clock.Clock,
	opts *Options,
	stdin io.WriteCloser,
	stdout *os.File,
) *Process {
	ring := NewRingBuffer(DefaultStderrRingCapacity)
	out := newOutputPipe(stdout)
	lineReader := NewLineReader(out, MaxFrameSize)

	return &Process{
		cmd:          cmd,
		clock:        clk,
		interrupt:    opts.Interrupt,
		onEscalation: opts.OnEscalation,
		stdin:        stdin,
		stdoutPipe:   out,
		stdoutR:      lineReader,
		stderrRing:   ring,
		exitCh:       make(chan struct{}),
	}
}

func startStderrDrain(ctx context.Context, p *Process, stderrR io.ReadCloser) {
	p.stderrTask = supervise.Go(ctx, "process.stderr_drain", func(ctx context.Context) error {
		defer closeQuietly(stderrR)
		buf := make([]byte, defaultReadChunkSize)
		for {
			done, err := drainStderrChunk(stderrR, p.stderrRing, buf)
			if err != nil {
				return err
			}
			if done {
				return nil
			}
		}
	})
}

func drainStderrChunk(r io.Reader, ring *RingBuffer, buf []byte) (bool, error) {
	n, rErr := r.Read(buf)
	if n > 0 {
		if _, wErr := ring.Write(buf[:n]); wErr != nil {
			return false, fmt.Errorf("write stderr ring: %w", wErr)
		}
	}
	if rErr != nil {
		if errors.Is(rErr, io.EOF) || errors.Is(rErr, os.ErrClosed) {
			return true, nil
		}
		return false, fmt.Errorf("read stderr: %w", rErr)
	}
	return false, nil
}

func startWaitTask(ctx context.Context, p *Process) {
	p.waitTask = supervise.Go(ctx, "process.wait", func(ctx context.Context) error {
		p.waitErr = p.cmd.Wait()
		if p.tracker != nil {
			p.tracker.close()
		}
		close(p.exitCh)
		return nil
	})
}

// classifySpawnError distinguishes binary not found, not executable, and start failed.
func classifySpawnError(path string, err error) error {
	if statErr, handled := classifyStat(path, err); handled {
		return statErr
	}
	return classifyExec(err)
}

func classifyStat(path string, err error) (error, bool) {
	fi, statErr := os.Stat(path)
	if statErr == nil {
		if fi.IsDir() {
			return fmt.Errorf("%w: path is a directory", ErrNotExecutable), true
		}
		if fi.Mode()&0o111 == 0 {
			return fmt.Errorf("%w: file lacks executable bit", ErrNotExecutable), true
		}
		return fmt.Errorf("%w: %w", ErrStartFailed, err), true
	}
	if errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("%w: %w", ErrBinaryNotFound, statErr), true
	}
	if errors.Is(statErr, os.ErrPermission) {
		return fmt.Errorf("%w: %w", ErrNotExecutable, statErr), true
	}
	return nil, false
}

func classifyExec(err error) error {
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT) {
		return fmt.Errorf("%w: %w", ErrBinaryNotFound, err)
	}
	if errors.Is(err, exec.ErrDot) || errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.ENOEXEC) {
		return fmt.Errorf("%w: %w", ErrNotExecutable, err)
	}
	return fmt.Errorf("%w: %w", ErrStartFailed, err)
}

// PID returns the underlying operating system process ID.
func (p *Process) PID() int {
	if p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// Identity returns the serializable process identity (PID, start token, and scope unit).
func (p *Process) Identity() Identity {
	return p.identity
}

// Stdout returns the continuous frame LineReader.
func (p *Process) Stdout() *LineReader {
	return p.stdoutR
}

// StderrRing returns the 64 KiB ring buffer capturing stderr output.
func (p *Process) StderrRing() *RingBuffer {
	return p.stderrRing
}

// CloseStdin closes the stdin pipe to notify the child of EOF.
func (p *Process) CloseStdin() error {
	if p.stdin != nil {
		if err := p.stdin.Close(); err != nil {
			return fmt.Errorf("close stdin: %w", err)
		}
	}
	return nil
}

// Wait waits for the process to exit and returns its exit error, if any.
func (p *Process) Wait() error {
	<-p.exitCh
	if p.tracker != nil {
		p.tracker.close()
	}
	return p.waitErr
}
