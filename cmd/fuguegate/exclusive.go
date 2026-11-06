package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
)

// ErrHeavyLock reports a problem acquiring the machine-wide heavy gate lock.
var ErrHeavyLock = errors.New("heavy gate lock")

// exclusiveCommand is the subcommand name of the heavy gate lock.
const exclusiveCommand = "exclusive"

const (
	// exitSignaled is the base of the conventional exit code for a process a
	// signal killed.
	exitSignaled = 128
	// exitInterrupted is the shell's exit code for dying on SIGINT, which is
	// what an interrupted wait is.
	exitInterrupted = 130
	// signalWaitDelay bounds how long a child may ignore the forwarded
	// termination signal before Wait returns anyway.
	signalWaitDelay = 10 * time.Second
	// lockRetryInterval is how often the waiting exclusive retries the lock.
	lockRetryInterval = 250 * time.Millisecond
	// heavyLockName is the lock file's name inside its directory; a constant,
	// because it is opened through an os.Root.
	heavyLockName = "heavy.lock"
)

// ExitError carries the exit status of a command that exclusive ran, so the
// process exit code is the child's instead of fuguegate's.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err == nil {
		return "exit status " + strconv.Itoa(e.Code)
	}
	return e.Err.Error()
}

func (e *ExitError) Unwrap() error {
	return e.Err
}

// runExclusive runs a command while holding the machine-wide heavy gate lock,
// waiting for the current holder to die first. Heavy work started in several
// worktrees stays inside the machine's memory this way instead of swapping.
func runExclusive(ctx context.Context, args []string, stdout io.Writer, clk clock.Clock) (err error) {
	fs := newFlags(exclusiveCommand)
	override := fs.String("lockdir", "", "override the directory holding the heavy gate lock")
	if perr := fs.Parse(args); perr != nil {
		return fmt.Errorf("%w: %w", ErrUsage, perr)
	}
	command := fs.Args()
	if len(command) == 0 {
		return fmt.Errorf("%w: exclusive needs a command after --", ErrUsage)
	}
	dir := *override
	if dir == "" {
		var derr error
		if dir, derr = heavyLockDir(); derr != nil {
			return derr
		}
	}
	lock, path, err := openHeavyLock(dir)
	if err != nil {
		return err
	}
	defer func() {
		if rerr := lock.release(); rerr != nil {
			err = errors.Join(err, fmt.Errorf("%w: release %s: %w", ErrHeavyLock, path, rerr))
		}
	}()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)

	busy, terr := tryLock(lock.file)
	if terr != nil {
		return fmt.Errorf("%w: %s: %w", ErrHeavyLock, path, terr)
	}
	if busy {
		if serr := say(stdout, "waiting for the heavy gate lock at %s", path); serr != nil {
			return serr
		}
		if werr := waitForFree(ctx, clk, func() (bool, error) { return tryLock(lock.file) }); werr != nil {
			return werr
		}
	}
	return runExclusiveCommand(ctx, sigs, command)
}

// heavyLockDir is the directory holding the machine-wide heavy gate lock file.
func heavyLockDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrHeavyLock, err)
	}
	return filepath.Join(base, "fugue"), nil
}

// heavyLock bundles the open root and the lock file inside it, so both close
// together when the lock is released.
type heavyLock struct {
	root *os.Root
	file *os.File
}

// release closes the lock file and its root, dropping the flock.
func (l *heavyLock) release() error {
	return errors.Join(l.file.Close(), l.root.Close())
}

// openHeavyLock opens, creating them if needed, the directory and the heavy
// gate lock file inside it. The file is never unlinked, so concurrent
// creators race onto the same inode instead of onto two.
func openHeavyLock(dir string) (*heavyLock, string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "", fmt.Errorf("%w: create %s: %w", ErrHeavyLock, dir, err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, "", fmt.Errorf("%w: open %s: %w", ErrHeavyLock, dir, err)
	}
	file, err := root.OpenFile(heavyLockName, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		closeErrs := []error{fmt.Errorf("%w: open %s: %w", ErrHeavyLock, dir, err)}
		if cerr := root.Close(); cerr != nil {
			closeErrs = append(closeErrs, fmt.Errorf("%w: close %s: %w", ErrHeavyLock, dir, cerr))
		}
		return nil, "", errors.Join(closeErrs...)
	}
	return &heavyLock{root: root, file: file}, filepath.Join(dir, heavyLockName), nil
}

// waitForFree retries try after every lockRetryInterval on clk until the lock
// frees or ctx ends. The caller announces the wait and made the first try,
// so waiting starts with one interval of sleep.
func waitForFree(ctx context.Context, clk clock.Clock, try func() (bool, error)) error {
	for {
		busy, err := try()
		if err != nil {
			return fmt.Errorf("%w: %w", ErrHeavyLock, err)
		}
		if !busy {
			return nil
		}
		if serr := clock.Sleep(ctx, clk, lockRetryInterval); serr != nil {
			return &ExitError{Code: exitInterrupted, Err: serr}
		}
	}
}

// runExclusiveCommand runs the command in the foreground of this process,
// which the caller already holds the lock for.
func runExclusiveCommand(ctx context.Context, sigs <-chan os.Signal, command []string) error {
	child := newCommand(ctx, "", childStreams{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr}, command[0], command[1:]...)
	child.WaitDelay = signalWaitDelay
	child.Cancel = func() error {
		return child.Process.Signal(pickSignal(sigs))
	}
	return exitStatus(child.Run())
}

// pickSignal returns the termination signal observed so far, or Interrupt
// when none arrived before a child had to be torn down.
func pickSignal(sigs <-chan os.Signal) os.Signal {
	select {
	case got := <-sigs:
		return got
	default:
		return os.Interrupt
	}
}

// exitStatus turns a child's exit into this process's exit: the child's own
// code, 128+signal for a child a signal killed, or a wrapped error for a
// command that never ran.
func exitStatus(err error) error {
	if err == nil {
		return nil
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return fmt.Errorf("%w: %w", ErrCommand, err)
	}
	if code := exit.ExitCode(); code >= 0 {
		return &ExitError{Code: code, Err: err}
	}
	if code, ok := signalExitCode(exit); ok {
		return &ExitError{Code: code, Err: err}
	}
	return &ExitError{Code: exitFailed, Err: err}
}
