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
	stdoutR    *LineReader
	stderrRing *RingBuffer

	stderrTask *supervise.Task
	waitTask   *supervise.Task

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

	path, err := exec.LookPath(opts.Argv[0])
	if err != nil {
		path = opts.Argv[0]
	}

	cmd := &exec.Cmd{
		Path:        path,
		Args:        opts.Argv,
		Dir:         opts.Cwd,
		Env:         opts.Env, // explicit: no inheritance from host process
		SysProcAttr: sysProcAttr(),
	}

	stdinPipe, stdoutPipe, stderrPipe, err := setupPipes(cmd, opts.Argv[0])
	if err != nil {
		return nil, err
	}

	if err := cmd.Start(); err != nil {
		closePipes(stdinPipe, stdoutPipe, stderrPipe)
		return nil, classifySpawnError(opts.Argv[0], err)
	}

	p := newProcess(cmd, clk, opts, stdinPipe, stdoutPipe)
	startStderrDrain(ctx, p, stderrPipe)
	startWaitTask(ctx, p)
	return p, nil
}

func setupPipes(cmd *exec.Cmd, argv0 string) (io.WriteCloser, io.ReadCloser, io.ReadCloser, error) {
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, nil, classifySpawnError(argv0, err)
	}
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		closePipes(stdinPipe)
		return nil, nil, nil, classifySpawnError(argv0, err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		closePipes(stdinPipe, stdoutPipe)
		return nil, nil, nil, classifySpawnError(argv0, err)
	}
	return stdinPipe, stdoutPipe, stderrPipe, nil
}

func newProcess(
	cmd *exec.Cmd,
	clk clock.Clock,
	opts *Options,
	stdin io.WriteCloser,
	stdout io.ReadCloser,
) *Process {
	ring := NewRingBuffer(DefaultStderrRingCapacity)
	lineReader := NewLineReader(stdout, MaxFrameSize)

	return &Process{
		cmd:          cmd,
		clock:        clk,
		interrupt:    opts.Interrupt,
		onEscalation: opts.OnEscalation,
		stdin:        stdin,
		stdoutR:      lineReader,
		stderrRing:   ring,
		exitCh:       make(chan struct{}),
	}
}

func startStderrDrain(ctx context.Context, p *Process, stderrPipe io.ReadCloser) {
	p.stderrTask = supervise.Go(ctx, "process.stderr_drain", func(ctx context.Context) error {
		buf := make([]byte, defaultReadChunkSize)
		for {
			done, err := drainStderrChunk(stderrPipe, p.stderrRing, buf)
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
		close(p.exitCh)
		return nil
	})
}

func closePipes(pipes ...io.Closer) {
	for _, p := range pipes {
		if p != nil {
			if err := p.Close(); err != nil {
				continue
			}
		}
	}
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
	return p.waitErr
}
