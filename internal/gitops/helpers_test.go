package gitops

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
)

// epoch returns the fixed instant the fake clock starts at in every test.
func epoch() time.Time {
	return time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
}

const (
	// testRepoID is the repository id every test uses under the data directory.
	testRepoID = "repo1"
	// gitName is the binary name the test fixtures resolve through PATH.
	gitName = "git"
)

// locateGit resolves the host git binary the way a caller would.
func locateGit(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("git unavailable: %v", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("absolute git path: %v", err)
	}
	return abs
}

// testManager returns a Manager over a fresh data directory and the fake
// clock that drives its backoff.
func testManager(t *testing.T) (*Manager, string, *clock.Fake) {
	t.Helper()
	return testManagerWithGit(t, locateGit(t))
}

// testManagerWithGit returns a Manager that runs gitPath, so tests can point
// it at a recording shell script instead of the real binary.
func testManagerWithGit(t *testing.T, gitPath string) (*Manager, string, *clock.Fake) {
	t.Helper()
	clk := clock.NewFake(epoch())
	root := filepath.Join(t.TempDir(), "data")
	m, err := NewManager(gitPath, root, clk)
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	return m, root, clk
}

// fixtureGit runs one real git command for test fixtures and fails the test
// when it errors.
func fixtureGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), gitName)
	cmd.Dir = dir
	cmd.Args = append([]string{gitName}, args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("fixture git %s: %v: %s", strings.Join(args, " "), err, stderr.String())
	}
	return string(out)
}

// initRepo creates a deterministic repository in a fresh temp directory.
func initRepo(t *testing.T) string {
	t.Helper()
	repoDir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repoDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return initRepoAt(t, repoDir)
}

// initRepoAt turns dir into the fixture repository: branch main with one
// commit holding a.txt, a .gitignore that ignores ignored.txt, and a local
// identity so result commits never depend on the host's global git config.
func initRepoAt(t *testing.T, repoDir string) string {
	t.Helper()
	fixtureGit(t, repoDir, "init", "--quiet")
	fixtureGit(t, repoDir, "symbolic-ref", "HEAD", "refs/heads/main")
	fixtureGit(t, repoDir, "config", "user.name", "Fugue Test")
	fixtureGit(t, repoDir, "config", "user.email", "fugue@example.invalid")
	fixtureGit(t, repoDir, "config", "commit.gpgsign", "false")
	writeFile(t, repoDir, ".gitignore", "ignored.txt\n")
	writeFile(t, repoDir, "a.txt", "one\n")
	fixtureGit(t, repoDir, "add", "-A")
	fixtureGit(t, repoDir, "commit", "--quiet", "-m", "initial")
	return repoDir
}

// writeFile writes rel under dir, creating parent directories.
func writeFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeScript installs an executable shell script the OS can run as a fake
// git binary.
func writeScript(t *testing.T, path, body string) {
	t.Helper()
	// The executable bit is required for the OS to run the script.
	mode := os.FileMode(0o700)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// fakeGitManager returns a Manager whose git binary is a fresh shell script
// printing body's output.
func fakeGitManager(t *testing.T, body string) (*Manager, string, *clock.Fake) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fakegit")
	writeScript(t, script, body)
	return testManagerWithGit(t, script)
}

// stdoutLines splits a fake git's stdout into non-empty lines.
func stdoutLines(res gitResult) []string {
	var lines []string
	for line := range strings.SplitSeq(res.Stdout, "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// containsExact reports whether list holds exactly the entry.
func containsExact(list []string, want string) bool {
	for _, got := range list {
		if got == want {
			return true
		}
	}
	return false
}

// runOneTask is the per-task evidence body of OR-E06: create the task
// worktree, change files, capture the result commit and remove the worktree.
func runOneTask(ctx context.Context, m *Manager, root, repoDir, taskID string) error {
	wt, err := m.CreateTaskWorktree(ctx, root, repoDir, testRepoID, taskID, "main")
	if err != nil {
		return fmt.Errorf("create worktree: %w", err)
	}
	if writeErr := os.WriteFile(filepath.Join(wt, "patch.txt"), []byte(taskID+"\n"), 0o600); writeErr != nil {
		return fmt.Errorf("write patch: %w", writeErr)
	}
	res, err := m.CommitWorktree(ctx, wt, "result "+taskID)
	if err != nil {
		return fmt.Errorf("commit result: %w", err)
	}
	if !res.Committed {
		return fmt.Errorf("task %s committed nothing: %w", taskID, ErrNoCommit)
	}
	if err := m.RemoveTaskWorktree(ctx, repoDir, root, testRepoID, taskID); err != nil {
		return fmt.Errorf("remove worktree: %w", err)
	}
	return nil
}
