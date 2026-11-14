package gitops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

// TestGitRunsWithGcAutoDisabledHooksOffAndScrubbedEnv pins the runner's fixed
// behaviour (orchestration §9): -c gc.auto=0, core.hooksPath at Fugue's empty
// directory, LC_ALL=C, the expected working directory, and no inherited GIT_*
// redirection reaches git.
//
// Not parallel: it changes the process environment.
func TestGitRunsWithGcAutoDisabledHooksOffAndScrubbedEnv(t *testing.T) {
	t.Setenv("GIT_DIR", "/evil/git-dir")
	t.Setenv("GIT_WORK_TREE", "/evil/worktree")
	t.Setenv("GIT_INDEX_FILE", "/evil/index")
	t.Setenv("GIT_EXEC_PATH", "/evil/exec")
	t.Setenv("GIT_CONFIG_COUNT", "2")
	scriptManager := func(body string) *Manager {
		m, _, _ := fakeGitManager(t, body)
		return m
	}

	m, root, _ := fakeGitManager(t, `printf '%s\n' "$@"`)
	res, err := m.exec(t.Context(), t.TempDir(), "status", "--porcelain")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	assertRunnerArgs(t, root, res)

	res, err = scriptManager(`env | sort`).exec(t.Context(), t.TempDir(), "status")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	assertRunnerEnv(t, res)

	workDir := t.TempDir()
	res, err = scriptManager(`pwd`).exec(t.Context(), workDir, "status")
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	assertRunnerWorkDir(t, res, workDir)

	assertHooksDirEmpty(t, root)
}

// assertRunnerArgs checks the runner's fixed -c pairs in front of the caller's
// arguments.
func assertRunnerArgs(t *testing.T, root string, res gitResult) {
	t.Helper()
	want := []string{
		"-c", "gc.auto=0",
		"-c", "core.hooksPath=" + filepath.Join(root, gitopsDirName, hooksDirName),
		"-c", "core.fsmonitor=false",
		"status", "--porcelain",
	}
	got := stdoutLines(res)
	if len(got) != len(want) {
		t.Fatalf("args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("args[%d] = %q, want %q (all: %v)", i, got[i], want[i], want)
		}
	}
}

// assertRunnerEnv checks the scrubbed environment the runner hands to git.
func assertRunnerEnv(t *testing.T, res gitResult) {
	t.Helper()
	env := stdoutLines(res)
	for _, want := range []string{"LC_ALL=C", "LANG=C", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1"} {
		if !containsExact(env, want) {
			t.Fatalf("env misses %s: %v", want, env)
		}
	}
	for _, banned := range []string{"GIT_DIR=", "GIT_WORK_TREE=", "GIT_INDEX_FILE=", "GIT_EXEC_PATH=", "GIT_CONFIG_COUNT=", "GIT_CONFIG_GLOBAL="} {
		for _, line := range env {
			if strings.HasPrefix(line, banned) {
				t.Fatalf("inherited %s reached git", banned)
			}
		}
	}
}

// assertRunnerWorkDir checks that the command runs in the caller's directory.
func assertRunnerWorkDir(t *testing.T, res gitResult, workDir string) {
	t.Helper()
	// macOS temp directories sit behind a /var → /private/var symlink; the
	// script reports the resolved path, which is what the runner hands it.
	want, err := filepath.EvalSymlinks(workDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(res.Stdout); got != want {
		t.Fatalf("pwd = %q, want %q", got, want)
	}
}

// assertHooksDirEmpty checks that Fugue's hooks directory exists and holds
// nothing, so no repository hook can run.
func assertHooksDirEmpty(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, gitopsDirName, hooksDirName))
	if err != nil {
		t.Fatalf("read hooks directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("hooks directory holds %d entries, want an empty directory", len(entries))
	}
}

// TestRefLockErrorRetriesThreeTimesWithBackoff drives the retry policy of
// orchestration §9: a ref-lock failure is retried three times with a backoff
// waited on the injected clock, and the fourth attempt succeeds.
//
// Not parallel: it drives the fake clock while the operation runs.
func TestRefLockErrorRetriesThreeTimesWithBackoff(t *testing.T) {
	script := `n=$(cat "$COUNT" 2>/dev/null || echo 0)
n=$((n+1))
echo "$n" > "$COUNT"
if [ "$n" -le 3 ]; then
  echo "fatal: cannot lock ref 'refs/heads/fugue/task/t1': is at 1111 but expected 2222" >&2
  exit 1
fi
echo "done after $n invocations"
`
	t.Setenv("COUNT", filepath.Join(t.TempDir(), "count"))
	m, _, clk := fakeGitManager(t, script)

	type outcome struct {
		res gitResult
		err error
	}
	done := make(chan outcome, 1)
	task := supervise.Go(t.Context(), "test.retry-exec", func(ctx context.Context) error {
		res, execErr := m.exec(ctx, t.TempDir(), "branch", "target")
		done <- outcome{res, execErr}
		return nil
	})
	defer task.Stop()

	for i := range refLockMaxRetries {
		if err := clk.BlockUntilPending(t.Context(), 1); err != nil {
			t.Fatalf("wait for backoff timer %d: %v", i, err)
		}
		clk.Advance(refLockBackoffBase << uint(i))
	}
	out := <-done
	if out.err != nil {
		t.Fatalf("exec after retries: %v", out.err)
	}
	if out.res.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", out.res.ExitCode)
	}
	if want := "done after 4 invocations"; strings.TrimSpace(out.res.Stdout) != want {
		t.Fatalf("stdout = %q, want %q", out.res.Stdout, want)
	}
	if elapsed := clk.Now().Sub(epoch()); elapsed != 350*time.Millisecond {
		t.Fatalf("backoff total = %s, want 350ms", elapsed)
	}
}

// TestRefLockExhaustionReturnsTypedUnavailable pins the end of the retry
// policy: when lock contention survives every attempt, the failure is the
// ref-lock kind (unavailable) carrying the log detail, not the generic
// classification of the last stderr.
//
// Not parallel: it drives the fake clock and the environment.
func TestRefLockExhaustionReturnsTypedUnavailable(t *testing.T) {
	script := `n=$(cat "$COUNT" 2>/dev/null || echo 0)
n=$((n+1))
echo "$n" > "$COUNT"
echo "fatal: cannot lock ref 'refs/heads/fugue/task/t1': is at 1111 but expected 2222" >&2
exit 1
`
	t.Setenv("COUNT", filepath.Join(t.TempDir(), "count"))
	m, _, clk := fakeGitManager(t, script)

	done := make(chan error, 1)
	task := supervise.Go(t.Context(), "test.lock-exhaustion", func(ctx context.Context) error {
		_, execErr := m.exec(ctx, t.TempDir(), "branch", "target")
		done <- execErr
		return nil
	})
	defer task.Stop()

	for i := range refLockMaxRetries {
		if err := clk.BlockUntilPending(t.Context(), 1); err != nil {
			t.Fatalf("wait for backoff timer %d: %v", i, err)
		}
		clk.Advance(refLockBackoffBase << uint(i))
	}
	err := <-done
	var gitErr *GitError
	if !errors.As(err, &gitErr) || gitErr.Kind != failureRefLock {
		t.Fatalf("err = %v, want a typed ref-lock failure", err)
	}
	if !errors.Is(err, ErrRefLockExhausted) {
		t.Fatalf("err = %v, want the ErrRefLockExhausted sentinel", err)
	}
	if got := errcode.CodeOf(err); got != errcode.Unavailable {
		t.Fatalf("CodeOf = %s, want unavailable", got)
	}
	if !strings.Contains(gitErr.Detail, "cannot lock ref") {
		t.Fatalf("detail = %q, want git's ref-lock message for the logs", gitErr.Detail)
	}
	if elapsed := clk.Now().Sub(epoch()); elapsed != 350*time.Millisecond {
		t.Fatalf("backoff total = %s, want 350ms", elapsed)
	}
}

// TestContextCancellationReturnsTheContextError pins that a git process cut
// down by its context reports the context error, not an exit-status
// classification of the killed process.
//
// Not parallel: it changes the process environment and cancels under a
// running git.
func TestContextCancellationReturnsTheContextError(t *testing.T) {
	started := filepath.Join(t.TempDir(), "started")
	// The fake git signals that it is running and then blocks until killed.
	t.Setenv("STARTED", started)
	// exec replaces the shell with the sleeper, so the context kill reaches
	// the only process and no child keeps the output pipes open.
	m, _, _ := fakeGitManager(t, `echo ready > "$STARTED"
exec sleep 30
`)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	errCh := make(chan error, 1)
	task := supervise.Go(ctx, "test.cancel-exec", func(ctx context.Context) error {
		_, execErr := m.exec(ctx, t.TempDir(), "branch", "target")
		errCh <- execErr
		return nil
	})
	defer task.Stop()

	waitForFile(t, ctx, started)
	cancel()
	err := <-errCh
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the context error", err)
	}
	if got := errcode.CodeOf(err); got != errcode.Canceled {
		t.Fatalf("CodeOf = %s, want canceled", got)
	}
}

// waitForFile polls for the handshake file a fake git writes when it starts,
// on the real clock with a bounded deadline (no busy waiting, no bare time
// calls).
func waitForFile(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	clk := clock.Real{}
	deadline := clk.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if clk.Now().After(deadline) {
			t.Fatalf("the fake git never signalled readiness at %s", path)
		}
		if err := clock.Sleep(ctx, clk, 10*time.Millisecond); err != nil {
			t.Fatalf("wait for the handshake file: %v", err)
		}
	}
}

// TestNonRefLockErrorIsNotRetried pins that only ref-lock failures retry:
// any other git failure returns immediately as a typed error.
func TestNonRefLockErrorIsNotRetried(t *testing.T) {
	t.Parallel()
	m, _, clk := fakeGitManager(t, `echo "fatal: something else went wrong" >&2
exit 1
`)

	_, err := m.run(t.Context(), t.TempDir(), "branch", "target")
	var gitErr *GitError
	if !errors.As(err, &gitErr) {
		t.Fatalf("err = %v, want a GitError", err)
	}
	if gitErr.Kind != failureGit {
		t.Fatalf("kind = %d, want failureGit", gitErr.Kind)
	}
	if gitErr.Code() != errcode.Unavailable {
		t.Fatalf("code = %s, want unavailable", gitErr.Code())
	}
	if got := errcode.CodeOf(err); got != errcode.Unavailable {
		t.Fatalf("CodeOf = %s, want unavailable", got)
	}
	if errors.Is(err, ErrRefLockExhausted) {
		t.Fatal("a non-ref-lock failure must not be classified as ref lock")
	}
	if clk.Now() != epoch() {
		t.Fatalf("clock moved to %s without a retry", clk.Now())
	}
	if !strings.Contains(gitErr.Detail, "something else went wrong") {
		t.Fatalf("detail = %q, want the trimmed git stderr", gitErr.Detail)
	}
}
