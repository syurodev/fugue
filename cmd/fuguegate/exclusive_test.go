package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/syurodev/fugue/internal/platform/clock"
)

// errTryFailed is the error the waitForFree unit test's try function reports.
var errTryFailed = errors.New("try failed")

const (
	// acquiredByte is the single readiness byte the lock holder writes once
	// it holds the lock.
	acquiredByte = 1
	// lockdirFlag is exclusive's lock directory override, spelled out in the
	// tests so every one of them is isolated from the machine's real lock.
	lockdirFlag = "-lockdir"
	// waitAnnouncement is the line a waiting exclusive prints exactly once.
	waitAnnouncement = "waiting"
)

// reexec returns a command that runs this test binary as the given mode.
// Production dispatches exclusive in main, which a test binary never calls;
// the waiters block the calling goroutine and bare `go` is reserved for
// supervise, so the tests drive the modes as subprocesses.
func reexec(t *testing.T, mode string, args ...string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return newCommand(t.Context(), "", childStreams{}, exe, append([]string{mode}, args...)...)
}

// exclusiveCmd starts an exclusive subprocess with its stdout piped back and
// returns it with a line reader over the pipe; the test end closes the pipe.
func exclusiveCmd(t *testing.T, dir string, command ...string) (*exec.Cmd, *bufio.Reader) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	proc := reexec(t, exclusiveCommand, append([]string{lockdirFlag, dir}, command...)...)
	proc.Stdout = w
	if err := proc.Start(); err != nil {
		closeWithError(t, r, w)
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Error(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Log(err)
		}
	})
	return proc, bufio.NewReader(r)
}

// holdLockStdio is the entry behind the re-exec lock holder: it flocks the
// lock file inside dir, reports readiness with one byte on stdout, then stays
// until stdin ends. It keeps the default signal disposition, so a forwarded
// signal kills the process and the kernel drops the lock.
func holdLockStdio(args []string, stdout io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("%w: hold needs a lock directory", ErrUsage)
	}
	root, err := os.OpenRoot(args[0])
	if err != nil {
		return fmt.Errorf("open %s: %w", args[0], err)
	}
	file, err := flockLockFile(root)
	if err != nil {
		return err
	}
	if err := root.Close(); err != nil {
		closeErrs := []error{err}
		if cerr := file.Close(); cerr != nil {
			closeErrs = append(closeErrs, cerr)
		}
		return errors.Join(closeErrs...)
	}
	if _, err := stdout.Write([]byte{acquiredByte}); err != nil {
		return errors.Join(fmt.Errorf("report readiness: %w", err), file.Close())
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		return errors.Join(fmt.Errorf("watch the parent: %w", err), file.Close())
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", heavyLockName, err)
	}
	return nil
}

// flockLockFile opens the lock file inside root and takes an exclusive flock
// on it.
func flockLockFile(root *os.Root) (*os.File, error) {
	file, err := root.OpenFile(heavyLockName, os.O_RDWR, 0o600)
	if err != nil {
		closeErrs := []error{err}
		if cerr := root.Close(); cerr != nil {
			closeErrs = append(closeErrs, cerr)
		}
		return nil, errors.Join(closeErrs...)
	}
	var ferr error
	conn, cerr := file.SyscallConn()
	if cerr != nil {
		return nil, errors.Join(cerr, file.Close())
	}
	if cerr := conn.Control(func(fd uintptr) {
		ferr = syscall.Flock(int(fd), syscall.LOCK_EX)
	}); cerr != nil {
		return nil, errors.Join(cerr, file.Close())
	}
	if ferr != nil {
		return nil, errors.Join(fmt.Errorf("flock: %w", ferr), file.Close())
	}
	return file, nil
}

// createLockFile creates the directory and an empty lock file inside it, the
// same step exclusive takes before a holder can open the file.
func createLockFile(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	file, err := root.OpenFile(heavyLockName, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		closeErrs := []error{err}
		if cerr := root.Close(); cerr != nil {
			closeErrs = append(closeErrs, cerr)
		}
		t.Fatal(errors.Join(closeErrs...))
	}
	if err := root.Close(); err != nil {
		closeErrs := []error{err}
		if cerr := file.Close(); cerr != nil {
			closeErrs = append(closeErrs, cerr)
		}
		t.Fatal(errors.Join(closeErrs...))
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

// holdLock takes the heavy gate lock in a holder subprocess and returns the
// holder with a release function whose error is the holder's exit status. The
// holder exits when its stdin closes.
func holdLock(t *testing.T, dir string) (*exec.Cmd, func() error) {
	t.Helper()
	createLockFile(t, dir)
	holdR, holdW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	readyR, readyW, err := os.Pipe()
	if err != nil {
		closeWithError(t, holdR, holdW)
		t.Fatal(err)
	}
	holder := reexec(t, "hold", dir)
	holder.Stdin, holder.Stdout = holdR, readyW
	if err := holder.Start(); err != nil {
		closeWithError(t, holdR, holdW, readyR, readyW)
		t.Fatal(err)
	}
	if err := readyW.Close(); err != nil {
		t.Error(err)
	}
	if err := holdR.Close(); err != nil {
		t.Error(err)
	}
	if _, err := io.ReadFull(readyR, make([]byte, 1)); err != nil {
		closeWithError(t, readyR, holdW)
		t.Fatalf("holder never held the lock: %v", err)
	}
	if err := readyR.Close(); err != nil {
		t.Error(err)
	}

	var waited bool
	release := func() error {
		if waited {
			return nil
		}
		waited = true
		if err := holdW.Close(); err != nil {
			t.Error(err)
		}
		return holder.Wait()
	}
	t.Cleanup(func() {
		if err := release(); err != nil {
			t.Log(err)
		}
	})
	return holder, release
}

func closeWithError(t *testing.T, files ...*os.File) {
	t.Helper()
	for _, f := range files {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	}
}

// The lock is free, so exclusive runs the command in place and carries its
// exit status: ordinary code and 128+signal alike.
func TestExclusiveCarriesTheExitStatus(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")

	out, err := execute(t, exclusiveCommand, lockdirFlag, dir, "--", "sh", "-c", "touch "+marker+"; exit 3")
	if !isExitCode(err, 3) {
		t.Fatalf("err = %v (out %q), want exit code 3", err, out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the command did not run: %v", err)
	}
	if _, err := execute(t, exclusiveCommand, lockdirFlag, dir, "--", "sh", "-c", "kill -TERM $$"); !isExitCode(err, exitSignaled+int(syscall.SIGTERM)) {
		t.Fatalf("err = %v, want exit code %d", err, exitSignaled+int(syscall.SIGTERM))
	}
}

// With another process holding the lock, exclusive announces once, waits, and
// runs its command only after the holder dies.
func TestExclusiveWaitsForTheHolder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	_, release := holdLock(t, dir)

	waiter, lines := exclusiveCmd(t, dir, "--", "sh", "-c", "echo started; touch "+marker)
	if line, err := lines.ReadString('\n'); err != nil || !strings.Contains(line, waitAnnouncement) {
		t.Fatalf("announcement = %q, %v; exclusive did not announce the wait", line, err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the command ran while the lock was held: %v", err)
	}

	if err := release(); err != nil {
		t.Fatalf("holder did not exit cleanly: %v", err)
	}
	if line, err := lines.ReadString('\n'); err != nil || !strings.Contains(line, "started") {
		t.Fatalf("command output = %q, %v; the command did not start", line, err)
	}
	if err := waiter.Wait(); err != nil {
		t.Fatalf("exclusive did not complete after the release: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the command did not run: %v", err)
	}
	rest, err := io.ReadAll(lines)
	if err != nil || len(strings.TrimSpace(string(rest))) != 0 {
		t.Fatalf("exclusive announced the wait more than once: %q (%v)", rest, err)
	}
}

// Killing the holder frees the lock and the waiting exclusive proceeds on its
// next retry; a crash never keeps the machine queued.
func TestExclusiveProceedsWhenTheHolderCrashes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	holder, release := holdLock(t, dir)

	waiter, lines := exclusiveCmd(t, dir, "--", "sh", "-c", "echo started; touch "+marker)
	if line, err := lines.ReadString('\n'); err != nil || !strings.Contains(line, waitAnnouncement) {
		t.Fatalf("announcement = %q, %v; exclusive did not announce the wait", line, err)
	}
	if err := holder.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := release(); err == nil {
		t.Fatal("the killed holder exited 0")
	}
	if line, err := lines.ReadString('\n'); err != nil || !strings.Contains(line, "started") {
		t.Fatalf("command output = %q, %v; the waiter did not proceed", line, err)
	}
	if err := waiter.Wait(); err != nil {
		t.Fatalf("exclusive did not complete after the crash: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the command did not run: %v", err)
	}
}

// Killing exclusive while it waits tears nothing down that outlives it: the
// lock was never its, and a fresh exclusive acquires it without waiting.
func TestExclusiveStopsWaitingWhenKilled(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, release := holdLock(t, dir)

	waiter, lines := exclusiveCmd(t, dir, "--", "true")
	if line, err := lines.ReadString('\n'); err != nil || !strings.Contains(line, waitAnnouncement) {
		t.Fatalf("announcement = %q, %v; exclusive did not announce the wait", line, err)
	}
	if err := waiter.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := waiter.Wait(); err == nil {
		t.Fatal("the killed waiter exited 0")
	}
	if err := release(); err != nil {
		t.Fatalf("holder did not exit cleanly: %v", err)
	}

	out, err := execute(t, exclusiveCommand, lockdirFlag, dir, "--", "true")
	if err != nil {
		t.Fatalf("a fresh exclusive did not complete: %v (out %q)", err, out)
	}
	if strings.Contains(out, waitAnnouncement) {
		t.Fatalf("the lock leaked past the killed waiter: %q", out)
	}
}

// An interrupted wait exits with the shell's SIGINT code.
func TestExclusiveInterruptedWhileWaiting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	_, release := holdLock(t, dir)

	waiter, lines := exclusiveCmd(t, dir, "--", "sh", "-c", "exit 0")
	if line, err := lines.ReadString('\n'); err != nil || !strings.Contains(line, waitAnnouncement) {
		t.Fatalf("announcement = %q, %v; exclusive did not announce the wait", line, err)
	}
	if err := waiter.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	if err := waiter.Wait(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != exitInterrupted {
			t.Fatalf("err = %v, want exit code %d", err, exitInterrupted)
		}
	}
	if err := release(); err != nil {
		t.Fatalf("holder did not exit cleanly: %v", err)
	}
}

func TestExclusiveUsage(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{exclusiveCommand},
		{exclusiveCommand, "--"},
		{exclusiveCommand, lockdirFlag},
		{exclusiveCommand, "-nope", "--", "true"},
	} {
		if _, err := execute(t, args...); !errors.Is(err, ErrUsage) {
			t.Fatalf("args %v: err = %v, want ErrUsage", args, err)
		}
	}
}

// A task command would deadlock: the inner task's exclusive waits on the lock
// its own parent already holds, so exclusive refuses it — directly, by path,
// and through `go tool task` — while any other command stays allowed.
func TestExclusiveRefusesTaskCommands(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"task", "test"},
		{"/usr/local/bin/task", "check"},
		{"./task", "vet"},
		{"go", "tool", "task", "vet"},
	} {
		argv := append([]string{exclusiveCommand, lockdirFlag, dir, "--"}, args...)
		if _, err := execute(t, argv...); !errors.Is(err, ErrUsage) {
			t.Fatalf("command %v: err = %v, want ErrUsage", args, err)
		}
	}
	if refused, bad := taskPrefix([]string{"go", "test", "./..."}); bad {
		t.Fatalf("go test ./... classified as %q; plain go commands must stay allowed", refused)
	}
	// Executed end to end with a command that finishes at once: a plain go
	// invocation runs instead of being refused.
	if _, err := execute(t, exclusiveCommand, lockdirFlag, dir, "--", "go", "version"); err != nil {
		t.Fatalf("a plain go command did not run: %v", err)
	}
}

// waitForFree tries immediately, retries after every interval, and reports an
// interrupted wait as the shell's SIGINT code.
func TestWaitForFree(t *testing.T) {
	t.Parallel()
	clk := clock.Real{}

	tries := 0
	if err := waitForFree(t.Context(), clk, func() (bool, error) {
		tries++
		return false, nil
	}); err != nil || tries != 1 {
		t.Fatalf("err = %v, tries = %d; a free lock must return on the first try", err, tries)
	}
	if err := waitForFree(t.Context(), clk, func() (bool, error) {
		return false, errTryFailed
	}); !errors.Is(err, ErrHeavyLock) {
		t.Fatalf("err = %v, want ErrHeavyLock", err)
	}
	occupied := []bool{true, false}
	if err := waitForFree(t.Context(), clk, func() (bool, error) {
		busy := occupied[0]
		occupied = occupied[1:]
		return busy, nil
	}); err != nil {
		t.Fatalf("err = %v; the lock must free on a retry", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := waitForFree(ctx, clk, func() (bool, error) { return true, nil }); !isExitCode(err, exitInterrupted) {
		t.Fatalf("err = %v, want exit code %d", err, exitInterrupted)
	}
}

func isExitCode(err error, want int) bool {
	var exit *ExitError
	return errors.As(err, &exit) && exit.Code == want
}
