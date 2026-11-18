package vendor

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/process"
)

// LoginEnd is how a login process ended, learned from the OS, never from
// what the CLI printed. Ran is false when the process did not start or was
// cut off by an unreadable output stream: the exit code is then meaningless
// and the caller treats the login as failed.
type LoginEnd struct {
	Ran      bool
	ExitCode int
}

// LoginRun is one running vendor CLI login command (F9.10b, VR-03). The
// lines it prints are untrusted data: the host relays them verbatim to the
// client, never into logs, errors or audit metadata, and never parses them
// for a decision — a login counts as successful only after the login probe
// (the F9.10a status command) says logged in.
type LoginRun interface {
	// Lines streams every complete stdout line the CLI printed, verbatim.
	// It closes when the process has exited or its output became unreadable.
	Lines() <-chan string
	// Done closes when the process has ended; End is valid after it.
	Done() <-chan struct{}
	// End reports how the process ended.
	End() LoginEnd
	// Stop runs the vendor-runtime §8 stop order on the process group.
	Stop(ctx context.Context) (process.StopProof, error)
}

// LoginCommand declares one vendor CLI's own login command (VR-03). An
// adapter whose CLI has no login command does not implement LoginStarter at
// all; the host answers a typed unsupported error.
type LoginCommand struct {
	// NeedsTerminal marks a CLI that finishes its login by reading a code
	// pasted into its own stdin (claude, Q-112): if the browser callback has
	// not completed when the timeout fires, the host ends the run with the
	// outcome needs_terminal instead of timed_out.
	NeedsTerminal bool
}

// LoginStarter is the port of a vendor CLI that has its own login command
// (VR-03). The host runs at most one login per vendor at a time and never
// touches vendor credentials: the process environment is the §8 allowlist
// the probe uses (VR-E09), with no Fugue secret in it.
type LoginStarter interface {
	// LoginCommand declares how the CLI's login ends.
	LoginCommand() LoginCommand
	// StartLogin runs the CLI's own login command as a supervised process
	// (vendor-runtime §8) and returns at once. The caller's context ends the
	// spawn work only; the process outlives it, like a session process.
	StartLogin(ctx context.Context, home AccountHome) (LoginRun, error)
}

// LoginOptions carries what the shared login runner needs beyond the command
// line: the §8-filtered environment and the containment settings the adapter
// already resolved for its sessions.
type LoginOptions struct {
	// Name is the vendor name used in supervise task names.
	Name string
	// Path is the absolute binary path the probe resolved.
	Path string
	Args []string
	// Env is the filtered environment; it is passed on unchanged.
	Env []string
	// SessionID names the containment scope on Linux; the login id.
	SessionID     string
	XDGRuntimeDir string
	Probe         *process.SystemdProbe
	Clock         clock.Clock
}

// loginLineBuffer bounds the lines buffered for the relay while the consumer
// is between reads. An extra line is dropped rather than blocking the pump:
// the relay is best-effort live progress, and the prompt lines arrive in the
// first moments of a login.
const loginLineBuffer = 64

// loginReadStopLimit bounds the stop of a login whose stdout turned
// unreadable, so the child cannot block forever on a full pipe.
const loginReadStopLimit = 10 * time.Second

// StartLoginProcess runs one vendor CLI login command through the process
// supervisor (vendor-runtime §8): its own process group, the filtered
// environment, stdout streamed line by line and the §8 stop order on Stop.
// Spawn failures are typed.
func StartLoginProcess(ctx context.Context, opts *LoginOptions) (*LoginProcess, error) {
	if opts.Path == "" || opts.Name == "" {
		return nil, errcode.New(errcode.InvalidArgument, "the login runner needs a vendor name and a binary path")
	}
	clk := opts.Clock
	if clk == nil {
		clk = clock.Real{}
	}
	proc, err := process.Spawn(ctx, &process.Options{
		Argv:          append([]string{opts.Path}, opts.Args...),
		Env:           opts.Env,
		Clock:         clk,
		SessionID:     opts.SessionID,
		XDGRuntimeDir: opts.XDGRuntimeDir,
		Probe:         opts.Probe,
	})
	if err != nil {
		switch {
		case errors.Is(err, process.ErrBinaryNotFound):
			return nil, errcode.Wrap(errcode.NotFound, "spawn "+opts.Name+" login process", err)
		case errors.Is(err, process.ErrNotExecutable):
			return nil, errcode.Wrap(errcode.PermissionDenied, "spawn "+opts.Name+" login process", err)
		}
		return nil, errcode.Wrap(errcode.Internal, "spawn "+opts.Name+" login process", err)
	}
	p := &LoginProcess{
		lines: make(chan string, loginLineBuffer),
		done:  make(chan struct{}),
		proc:  proc,
	}
	supervise.Go(context.WithoutCancel(ctx), opts.Name+".login-stdout", func(taskCtx context.Context) error {
		p.pump(taskCtx)
		return nil
	})
	supervise.Go(context.WithoutCancel(ctx), opts.Name+".login-wait", func(taskCtx context.Context) error {
		p.finish()
		return nil
	})
	return p, nil
}

// LoginProcess is the shared LoginRun implementation over one spawned
// process. Nothing here reads or writes a vendor credential; the child owns
// its own login flow and the host only watches it run.
type LoginProcess struct {
	lines chan string
	done  chan struct{}

	mu       sync.Mutex
	proc     *process.Process
	end      LoginEnd
	readErr  error
	finished bool
}

// Lines implements LoginRun.
func (p *LoginProcess) Lines() <-chan string { return p.lines }

// Done implements LoginRun.
func (p *LoginProcess) Done() <-chan struct{} { return p.done }

// End implements LoginRun.
func (p *LoginProcess) End() LoginEnd {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.end
}

// Stop implements LoginRun.
func (p *LoginProcess) Stop(ctx context.Context) (process.StopProof, error) {
	p.mu.Lock()
	proc := p.proc
	p.mu.Unlock()
	proof, err := proc.Stop(ctx)
	if err != nil {
		return proof, errcode.Wrap(errcode.CodeOf(err), "stop the login process", err)
	}
	return proof, nil
}

// pump streams complete stdout lines verbatim until the stream ends or a
// line grows past the frame limit; a line no consumer has room for is
// dropped, because the relay is live progress, not a transcript.
func (p *LoginProcess) pump(taskCtx context.Context) {
	defer close(p.lines)
	reader := p.stdout()
	for {
		line, err := reader.ReadLine()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				p.stopUnreadable(taskCtx, err)
			}
			return
		}
		select {
		case p.lines <- string(line):
		default:
		}
	}
}

// stopUnreadable ends the relay of a stream that cannot be read (a line past
// the frame cap, an I/O error): the child must not block forever on a pipe
// nobody reads.
func (p *LoginProcess) stopUnreadable(taskCtx context.Context, readErr error) {
	p.mu.Lock()
	p.readErr = readErr
	proc := p.proc
	p.mu.Unlock()
	stopCtx, cancel := context.WithTimeout(taskCtx, loginReadStopLimit)
	defer cancel()
	if _, stopErr := proc.Stop(stopCtx); stopErr != nil {
		p.mu.Lock()
		p.readErr = errors.Join(p.readErr, stopErr)
		p.mu.Unlock()
	}
}

func (p *LoginProcess) stdout() *process.LineReader {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.proc.Stdout()
}

// finish collects the process end. A stdout that failed before the process
// exited marks the end unreadable, whatever the exit code says.
func (p *LoginProcess) finish() {
	err := p.wait()
	end := LoginEnd{}
	var exitErr *exec.ExitError
	if err == nil {
		end.Ran, end.ExitCode = true, 0
	} else if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
		end.Ran, end.ExitCode = true, exitErr.ExitCode()
	}
	p.mu.Lock()
	if p.readErr != nil {
		end = LoginEnd{}
	}
	p.end = end
	p.finished = true
	p.mu.Unlock()
	close(p.done)
}

func (p *LoginProcess) wait() error {
	p.mu.Lock()
	proc := p.proc
	p.mu.Unlock()
	err := proc.Wait()
	if err == nil {
		return nil
	}
	// A process ended by the stop order carries a signal exit; the wrap keeps
	// a stable code for the caller while errors.As still finds the ExitError.
	return errcode.Wrap(errcode.CodeOf(err), "wait for the login process", err)
}
