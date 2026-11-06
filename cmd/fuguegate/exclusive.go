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
)

// ErrHeavyLock reports a problem acquiring or handing over the machine-wide
// heavy gate lock.
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
	// acquiredByte is the single readiness byte the lock-wait helper writes
	// once it holds the lock.
	acquiredByte = 1
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
func runExclusive(ctx context.Context, args []string, stdout io.Writer) error {
	fs := newFlags(exclusiveCommand)
	override := fs.String("lock", "", "override the heavy gate lock file")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", ErrUsage, err)
	}
	command := fs.Args()
	if len(command) == 0 {
		return fmt.Errorf("%w: exclusive needs a command after --", ErrUsage)
	}
	path := *override
	if path == "" {
		var err error
		if path, err = heavyLockPath(); err != nil {
			return err
		}
	}
	lock, err := openHeavyLock(path)
	if err != nil {
		return err
	}
	defer closeQuietly(lock)

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)

	busy, err := tryLock(lock)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrHeavyLock, path, err)
	}
	if !busy {
		return runExclusiveCommand(ctx, sigs, command)
	}
	if err := say(stdout, "waiting for the heavy gate lock at %s", path); err != nil {
		return err
	}
	return runExclusiveWaiting(ctx, sigs, path, command)
}

// heavyLockPath is the machine-wide heavy gate lock file.
func heavyLockPath() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrHeavyLock, err)
	}
	return filepath.Join(base, "fugue", "heavy.lock"), nil
}

// openHeavyLock opens, creating it if needed, the given heavy gate lock file.
// The file is never unlinked, so concurrent creators race onto the same inode
// instead of onto two.
func openHeavyLock(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("%w: create %s: %w", ErrHeavyLock, filepath.Dir(path), err)
	}
	//nolint:gosec // G304: the path is fuguegate's cache file or the test's own override.
	lock, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("%w: open %s: %w", ErrHeavyLock, path, err)
	}
	return lock, nil
}

// runExclusiveCommand runs the command in the foreground of this process,
// which the caller already holds the lock for.
func runExclusiveCommand(ctx context.Context, sigs <-chan os.Signal, command []string) error {
	child := passthroughCommand(ctx, sigs, command)
	return exitStatus(child.Run())
}

// runExclusiveWaiting spawns a lock-wait helper that holds the heavy gate
// lock on this process's behalf. The helper reports the lock with one byte on
// the ready pipe and stays until the watch pipe closes; the pipe closes on a
// normal exit and on a crash alike, so the lock never outlives this process.
func runExclusiveWaiting(ctx context.Context, sigs <-chan os.Signal, path string, command []string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("%w: locate this executable: %w", ErrHeavyLock, err)
	}
	readyR, readyW, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("%w: ready pipe: %w", ErrHeavyLock, err)
	}
	watchR, watchW, err := os.Pipe()
	if err != nil {
		closeQuietly(readyR)
		closeQuietly(readyW)
		return fmt.Errorf("%w: watch pipe: %w", ErrHeavyLock, err)
	}
	defer closeQuietly(readyR)
	defer closeQuietly(watchW)

	//nolint:gosec // G204: the binary is this process's own and the path is fuguegate's cache file.
	helper := exec.CommandContext(ctx, exe, "flockwait", "-lock", path)
	helper.Stdout = readyW
	helper.Stdin = watchR
	helper.WaitDelay = signalWaitDelay
	helper.Cancel = func() error {
		return helper.Process.Signal(pickSignal(sigs))
	}
	if err := helper.Start(); err != nil {
		closeQuietly(readyW)
		closeQuietly(watchR)
		return fmt.Errorf("%w: start the lock waiter: %w", ErrHeavyLock, err)
	}
	// The helper holds the only other end of each pipe from here on, which is
	// why its copy of the watch pipe closes when this process dies.
	closeQuietly(readyW)
	closeQuietly(watchR)

	if _, err := io.ReadFull(readyR, make([]byte, 1)); err != nil {
		waitErr := helper.Wait()
		if ctx.Err() != nil {
			return &ExitError{Code: exitInterrupted, Err: ctx.Err()}
		}
		return fmt.Errorf("%w: waiter exited before acquiring %s: %w", ErrHeavyLock, path, errors.Join(err, waitErr))
	}

	runErr := runExclusiveCommand(ctx, sigs, command)
	// Release the lock: the helper exits when its stdin hits end of file.
	closeQuietly(watchW)
	if waitErr := helper.Wait(); waitErr != nil && runErr == nil {
		return fmt.Errorf("%w: waiter did not exit cleanly: %w", ErrHeavyLock, waitErr)
	}
	return runErr
}

// passthroughCommand builds the child with the terminal's stdio and a Cancel
// that forwards the signal the user sent instead of killing it outright.
func passthroughCommand(ctx context.Context, sigs <-chan os.Signal, command []string) *exec.Cmd {
	//nolint:gosec // G204: the command is this process's own arguments after --.
	child := exec.CommandContext(ctx, command[0], command[1:]...)
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	child.WaitDelay = signalWaitDelay
	child.Cancel = func() error {
		return child.Process.Signal(pickSignal(sigs))
	}
	return child
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

// runFlockWait is the helper that exclusive spawns to hold the lock while it
// waits. It blocks until it holds the lock, reports readiness with one byte
// on stdout, then stays until stdin closes. It installs no signal handlers:
// dying on the forwarded signal is what tears down an interrupted wait.
func runFlockWait(args []string, stdout io.Writer) error {
	fs := newFlags("flockwait")
	lockPath := fs.String("lock", "", "heavy gate lock file to hold")
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%w: %w", ErrUsage, err)
	}
	if *lockPath == "" || fs.NArg() != 0 {
		return fmt.Errorf("%w: flockwait needs -lock <path>", ErrUsage)
	}
	lock, err := os.OpenFile(*lockPath, os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("%w: open %s: %w", ErrHeavyLock, *lockPath, err)
	}
	defer closeQuietly(lock)
	if err := lockBlocking(lock); err != nil {
		return fmt.Errorf("%w: %s: %w", ErrHeavyLock, *lockPath, err)
	}
	if _, err := stdout.Write([]byte{acquiredByte}); err != nil {
		return fmt.Errorf("%w: report readiness: %w", ErrHeavyLock, err)
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		return fmt.Errorf("%w: watch the parent: %w", ErrHeavyLock, err)
	}
	return nil
}

// closeQuietly closes a resource whose only remaining failure mode is a
// descriptor that is already closed.
func closeQuietly(c io.Closer) {
	if c == nil {
		return
	}
	if err := c.Close(); err != nil {
		return
	}
}
