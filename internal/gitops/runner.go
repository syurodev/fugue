package gitops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
)

// Ref-lock retry policy (orchestration §9): a ref-lock failure is retried at
// most refLockMaxRetries times; the backoff doubles between attempts and is
// waited on the injected clock so tests never sleep.
const (
	refLockMaxRetries  = 3
	refLockBackoffBase = 50 * time.Millisecond
)

// envFixedCount is the number of variables scrubbedEnv appends.
const envFixedCount = 6

// runnerFixedArgs is the number of argument slots (three -c pairs) the runner
// puts in front of the caller's arguments.
const runnerFixedArgs = 3

// gitResult carries the outcome of one git invocation. A non-zero exit is not
// an error here; callers decide what an exit code means for their operation.
type gitResult struct {
	Stdout   string
	ExitCode int
	Stderr   string
}

// exec runs one git command in dir with the runner's fixed arguments: the
// injected binary, -c gc.auto=0, core.hooksPath pointing at Fugue's empty
// directory, -c core.fsmonitor=false (a repo-local fsmonitor command, writable
// by an agent inside a worktree, must never run), and a scrubbed environment
// with LC_ALL=C (orchestration §9). Ref-lock failures are retried with backoff
// on the injected clock; every other failure returns at once. The context
// bounds the command.
func (m *Manager) exec(ctx context.Context, dir string, args ...string) (gitResult, error) {
	hooks, err := m.HooksDir()
	if err != nil {
		return gitResult{}, err
	}
	full := make([]string, 0, len(args)+2*runnerFixedArgs)
	full = append(full,
		"-c", "gc.auto=0",
		"-c", "core.hooksPath="+hooks,
		"-c", "core.fsmonitor=false",
	)
	full = append(full, args...)

	var result gitResult
	for attempt := range refLockMaxRetries + 1 {
		if attempt > 0 {
			if sleepErr := clock.Sleep(ctx, m.clk, refLockBackoffBase<<uint(attempt-1)); sleepErr != nil {
				return gitResult{}, fmt.Errorf("gitops: backoff wait: %w", sleepErr)
			}
		}
		result, err = m.runOnce(ctx, dir, full)
		if err == nil {
			return result, nil
		}
		var lockErr *lockBusyError
		if !errors.As(err, &lockErr) {
			return gitResult{}, err
		}
		result.Stderr = lockErr.stderr
		result.ExitCode = lockErr.exitCode
	}
	// Every attempt failed on lock contention, so the failure is the ref-lock
	// kind, not whatever stderr classification would guess.
	return gitResult{}, newGitError(opName(full), failureRefLock, trimDetail(result.Stderr))
}

// runOnce performs a single exec of the git binary.
func (m *Manager) runOnce(ctx context.Context, dir string, args []string) (gitResult, error) {
	if err := ctx.Err(); err != nil {
		return gitResult{}, fmt.Errorf("gitops: context already done: %w", err)
	}
	cmd := exec.CommandContext(ctx, "git")
	cmd.Path = m.gitPath
	cmd.Args = append([]string{m.gitPath}, args...)
	cmd.Dir = dir
	cmd.Env = m.scrubbedEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	result := gitResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}
	// Cancellation beats every classification: when the context ended, the
	// process was killed by it and its exit status says nothing about git.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return result, fmt.Errorf("gitops: %s: %w", opName(args), ctxErr)
	}
	if runErr == nil {
		return result, nil
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
		if isRefLockBusy(stderr.String()) {
			return result, &lockBusyError{stderr: stderr.String(), exitCode: result.ExitCode}
		}
		return result, nil
	}
	return result, fmt.Errorf("gitops: start %s: %w", opName(args), runErr)
}

// lockBusyError marks a git failure as ref (or index) lock contention so the
// exec retry loop can classify it without matching the message twice.
type lockBusyError struct {
	stderr   string
	exitCode int
}

func (e *lockBusyError) Error() string { return "gitops: git reported a busy ref or index lock" }

// isRefLockBusy recognises the ref-lock messages named in orchestration §9:
// "cannot lock ref" and "Unable to create '...lock': File exists" (the latter
// also covers index.lock contention between concurrent commits).
func isRefLockBusy(stderr string) bool {
	return strings.Contains(stderr, "cannot lock ref") ||
		(strings.Contains(stderr, "Unable to create") &&
			strings.Contains(stderr, ".lock") &&
			strings.Contains(stderr, "File exists"))
}

// scrubbedEnv builds the environment for every git invocation: inherited
// GIT_* redirections (GIT_DIR, GIT_WORK_TREE, GIT_INDEX_FILE, GIT_CONFIG*,
// GIT_EXEC_PATH and friends) are dropped, LC_ALL=C keeps error messages
// stable, and the fixed guards stop prompts and system config reads. The
// user's identity and signing configuration come from their normal config
// files and are never overridden.
func (m *Manager) scrubbedEnv() []string {
	raw := os.Environ()
	out := make([]string, 0, len(raw)+envFixedCount)
	for _, kv := range raw {
		key, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "GIT_") {
			continue
		}
		out = append(out, kv)
	}
	return append(out,
		"LC_ALL=C",
		"LANG=C",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_ATTR_NOSYSTEM=1",
	)
}

// opName returns the git verb of the invocation for typed errors. The
// runner's leading -c <key>=<value> pairs are skipped.
func opName(args []string) string {
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" {
			i++
			continue
		}
		return args[i]
	}
	return "git"
}

// run is the operations' wrapper around exec: a non-zero exit becomes a typed
// GitError unless the caller inspects exit codes itself.
func (m *Manager) run(ctx context.Context, dir string, args ...string) (string, error) {
	res, err := m.exec(ctx, dir, args...)
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", m.gitFailure(opName(args), res)
	}
	return res.Stdout, nil
}
