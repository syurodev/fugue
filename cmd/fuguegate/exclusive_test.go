package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// reexec returns a command that runs this test binary as a fuguegate
// subcommand. Production reaches these entries through main, which a test
// binary never calls; the waiters block the calling goroutine and bare `go`
// is reserved for supervise, so the tests drive them as subprocesses.
func reexec(t *testing.T, subcommand string, args ...string) *exec.Cmd {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // G204: the binary is this test's own and the arguments are literals from this file.
	return exec.CommandContext(t.Context(), exe, append([]string{subcommand}, args...)...)
}

// exclusiveCmd starts an exclusive subprocess with its stdout piped back and
// returns it with a line reader over the pipe; the test end closes the pipe.
func exclusiveCmd(t *testing.T, path string, command ...string) (*exec.Cmd, *bufio.Reader) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	proc := reexec(t, exclusiveCommand, append([]string{"-lock", path}, command...)...)
	proc.Stdout = w
	if err := proc.Start(); err != nil {
		closeQuietly(r)
		closeQuietly(w)
		t.Fatal(err)
	}
	closeQuietly(w)
	t.Cleanup(func() {
		closeQuietly(r)
	})
	return proc, bufio.NewReader(r)
}

// holdLock takes the heavy gate lock in a flockwait helper subprocess and
// returns the release function. The holder exits when its stdin closes, and
// the test fails if it ever exits for another reason.
func holdLock(t *testing.T, path string) func() {
	t.Helper()
	// exclusive creates the lock file before it spawns its own waiter; the
	// tests take the same step for their holders.
	//nolint:gosec // G304: the path is the test's own temporary lock file.
	lock, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	closeQuietly(lock)
	holdR, holdW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	readyR, readyW, err := os.Pipe()
	if err != nil {
		closeQuietly(holdR)
		closeQuietly(holdW)
		t.Fatal(err)
	}
	holder := reexec(t, "flockwait", "-lock", path)
	holder.Stdin, holder.Stdout = holdR, readyW
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	closeQuietly(readyW)
	closeQuietly(holdR)
	if _, err := io.ReadFull(readyR, make([]byte, 1)); err != nil {
		t.Fatalf("holder never held the lock: %v", err)
	}
	closeQuietly(readyR)

	var released bool
	release := func() {
		if released {
			return
		}
		released = true
		closeQuietly(holdW)
		if err := holder.Wait(); err != nil {
			t.Errorf("holder did not exit cleanly: %v", err)
		}
	}
	t.Cleanup(release)
	return release
}

// The lock is free, so exclusive runs the command in place and carries its
// exit status: ordinary code and 128+signal alike.
func TestExclusiveCarriesTheExitStatus(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "heavy.lock")
	marker := filepath.Join(dir, "ran")

	out, err := execute(t, "exclusive", "-lock", path, "--", "sh", "-c", "touch "+marker+"; exit 3")
	if !isExitCode(err, 3) {
		t.Fatalf("err = %v (out %q), want exit code 3", err, out)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the command did not run: %v", err)
	}
	if _, err := execute(t, "exclusive", "-lock", path, "--", "sh", "-c", "kill -TERM $$"); !isExitCode(err, exitSignaled+int(syscall.SIGTERM)) {
		t.Fatalf("err = %v, want exit code %d", err, exitSignaled+int(syscall.SIGTERM))
	}
}

// With another process holding the lock, exclusive announces once, waits, and
// runs its command only after the holder dies.
func TestExclusiveWaitsForTheHolder(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "heavy.lock")
	marker := filepath.Join(dir, "ran")
	release := holdLock(t, path)

	waiter, lines := exclusiveCmd(t, path, "--", "sh", "-c", "touch "+marker)
	if line, err := lines.ReadString('\n'); err != nil || !strings.Contains(line, "waiting") {
		t.Fatalf("announcement = %q, %v; exclusive did not announce the wait", line, err)
	}
	if _, err := os.Stat(marker); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the command ran while the lock was held: %v", err)
	}

	release()
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

// Killing the exclusive process releases the lock even though its lock-wait
// helper, not the killed process, holds it: the helper exits when the parent's
// watch pipe closes, and a crash closes it too.
func TestExclusiveReleasesTheLockAfterACrash(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "heavy.lock")
	release := holdLock(t, path)

	victim, lines := exclusiveCmd(t, path, "--", "sh", "-c", "echo started; sleep 5")
	if line, err := lines.ReadString('\n'); err != nil || !strings.Contains(line, "waiting") {
		t.Fatalf("announcement = %q, %v; exclusive did not announce the wait", line, err)
	}
	// The release hands the lock to the victim's helper, which then starts the
	// command; the helper holds it on the victim's behalf from here.
	release()
	if line, err := lines.ReadString('\n'); err != nil || !strings.Contains(line, "started") {
		t.Fatalf("command output = %q, %v; the command did not start", line, err)
	}
	if err := victim.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := victim.Wait(); err == nil {
		t.Fatal("the killed exclusive process exited 0")
	}

	fresh := reexec(t, exclusiveCommand, "-lock", path, "--", "true")
	var out bytes.Buffer
	fresh.Stdout = &out
	if err := fresh.Run(); err != nil {
		t.Fatalf("a fresh exclusive did not complete: %v (out %q)", err, out.String())
	}
	if strings.Contains(out.String(), "waiting") {
		t.Fatalf("the lock leaked past the crash: %q", out.String())
	}
}

// Killing exclusive while it waits tears down its lock waiter; the lock then
// belongs to nobody and a fresh exclusive acquires it without waiting.
func TestExclusiveStopsWaitingWhenKilled(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "heavy.lock")
	release := holdLock(t, path)

	waiter, lines := exclusiveCmd(t, path, "--", "true")
	if line, err := lines.ReadString('\n'); err != nil || !strings.Contains(line, "waiting") {
		t.Fatalf("announcement = %q, %v; exclusive did not announce the wait", line, err)
	}
	if err := waiter.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := waiter.Wait(); err == nil {
		t.Fatal("the killed waiter exited 0")
	}
	release()

	after := reexec(t, exclusiveCommand, "-lock", path, "--", "true")
	var out bytes.Buffer
	after.Stdout = &out
	if err := after.Run(); err != nil {
		t.Fatalf("a fresh exclusive did not complete: %v (out %q)", err, out.String())
	}
	if strings.Contains(out.String(), "waiting") {
		t.Fatalf("the lock leaked past the killed waiter: %q", out.String())
	}
}

func TestExclusiveUsage(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{exclusiveCommand},
		{exclusiveCommand, "--"},
		{exclusiveCommand, "-lock"},
		{exclusiveCommand, "-nope", "--", "true"},
	} {
		if _, err := execute(t, args...); !errors.Is(err, ErrUsage) {
			t.Fatalf("args %v: err = %v, want ErrUsage", args, err)
		}
	}
}

func isExitCode(err error, want int) bool {
	var exit *ExitError
	return errors.As(err, &exit) && exit.Code == want
}
