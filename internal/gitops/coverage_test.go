package gitops

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
)

// TestNewManagerRejectsBadDependencies pins the constructor's typed
// rejections; the git binary path and the data directory come from the caller.
func TestNewManagerRejectsBadDependencies(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cases := []struct {
		name    string
		gitPath string
		dir     string
		clk     clock.Clock
	}{
		{"empty git path", "", root, clock.NewFake(epoch())},
		{"relative git path", gitName, root, clock.NewFake(epoch())},
		{"empty data dir", locateGit(t), "", clock.NewFake(epoch())},
		{"relative data dir", locateGit(t), "data", clock.NewFake(epoch())},
		{"nil clock", locateGit(t), root, nil},
	}
	for _, tc := range cases {
		if _, err := NewManager(tc.gitPath, tc.dir, tc.clk); errcode.CodeOf(err) != errcode.InvalidArgument {
			t.Fatalf("%s: err = %v, want invalid_argument", tc.name, err)
		}
	}
	if _, err := NewManager(locateGit(t), root, clock.NewFake(epoch())); err != nil {
		t.Fatalf("valid dependencies rejected: %v", err)
	}
}

// TestCommonDirFallsBackForOlderGit covers the rev-parse fallback for git
// releases without --path-format: the relative common dir is made absolute
// against the repository path, so the gate key still resolves to the
// canonical common git dir.
func TestCommonDirFallsBackForOlderGit(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	base := t.TempDir()
	repoDir := filepath.Join(base, "real")
	if err := os.MkdirAll(repoDir, 0o700); err != nil {
		t.Fatal(err)
	}
	initRepoAt(t, repoDir)
	script := `for a in "$@"; do
  if [ "$a" = "--path-format=absolute" ]; then
    echo "error: unknown option" >&2
    exit 129
  fi
done
echo ".git"
`
	m, _, _ := fakeGitManager(t, script)

	linkDir := filepath.Join(base, "link")
	viaReal, realErr := m.gateFor(ctx, repoDir)
	if realErr != nil {
		t.Fatalf("gate for real path through the fallback: %v", realErr)
	}
	// The symlink path must collapse onto the same gate key.
	if linkErr := os.Symlink(repoDir, linkDir); linkErr != nil {
		t.Fatal(linkErr)
	}
	viaLink, linkErr := m.gateFor(ctx, linkDir)
	if linkErr != nil {
		t.Fatalf("gate for symlink path: %v", linkErr)
	}
	if viaReal != viaLink {
		t.Fatal("the fallback resolved a different repo lock for the same repository")
	}
}

// TestHooksDirFailureIsTyped pins that an unusable data directory fails the
// runner before git starts, with a wrapped error.
func TestHooksDirFailureIsTyped(t *testing.T) {
	t.Parallel()
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(locateGit(t), blocker, clock.NewFake(epoch()))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	if _, err := m.HooksDir(); err == nil || !strings.Contains(err.Error(), "create empty hooks directory") {
		t.Fatalf("HooksDir err = %v, want the wrapped creation failure", err)
	}
	if _, err := m.exec(t.Context(), t.TempDir(), "status"); err == nil {
		t.Fatal("exec succeeded although the hooks directory cannot exist")
	}
}

// TestGitErrorKindMapping pins the public text, code and sentinel of every
// failure kind; the texts never repeat git stderr, and the stable code is
// reachable through errcode.CodeOf.
func TestGitErrorKindMapping(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind   failureKind
		code   errcode.Code
		text   string
		unwrap error
	}{
		{failureGit, errcode.Unavailable, "gitops: git failed", nil},
		{failureIdentity, errcode.FailedPrecondition, "no usable git identity", nil},
		{failureSigning, errcode.FailedPrecondition, "signature creation failed", nil},
		{failureRefLock, errcode.Unavailable, "ref lock stayed busy", ErrRefLockExhausted},
		{failureMissingRef, errcode.NotFound, "unknown branch or commit", nil},
	}
	for _, tc := range cases {
		e := newGitError(gitName, tc.kind, "detail kept for logs")
		if !strings.Contains(e.Error(), tc.text) || strings.Contains(e.Error(), "detail kept") {
			t.Fatalf("kind %d text = %q, want it to name %q without the detail", tc.kind, e.Error(), tc.text)
		}
		if e.Code() != tc.code {
			t.Fatalf("kind %d code = %s, want %s", tc.kind, e.Code(), tc.code)
		}
		if got := errcode.CodeOf(e); got != tc.code {
			t.Fatalf("kind %d CodeOf = %s, want %s", tc.kind, got, tc.code)
		}
		_, exposed := errcode.Expose(e)
		if strings.Contains(exposed, "detail kept") {
			t.Fatalf("kind %d exposed message %q carries the log detail", tc.kind, exposed)
		}
		if tc.unwrap != nil && !errors.Is(e, tc.unwrap) {
			t.Fatalf("kind %d does not match its sentinel %v", tc.kind, tc.unwrap)
		}
	}
}

// TestRunnerHelperDetails covers the small pure helpers of the runner.
func TestRunnerHelperDetails(t *testing.T) {
	t.Parallel()
	if got := opName([]string{"-c", "gc.auto=0", gitName}); got != gitName {
		t.Fatalf("opName = %q, want branch", got)
	}
	if got := opName([]string{"-c"}); got != "git" {
		t.Fatalf("opName of a lone -c = %q, want git", got)
	}
	if got := opName(nil); got != "git" {
		t.Fatalf("opName(nil) = %q, want git", got)
	}

	detail := trimDetail(strings.Repeat("word ", 200))
	if len(detail) != maxErrorDetail || !strings.HasPrefix(detail, "word word") {
		t.Fatalf("trimDetail = %q (len %d), want a bounded single line", detail, len(detail))
	}

	busy := &lockBusyError{stderr: "unable to create lock", exitCode: 1}
	if !strings.Contains(busy.Error(), "busy ref or index lock") {
		t.Fatalf("lockBusyError text = %q", busy.Error())
	}

	if got, err := pruneRetention(time.Hour); err != nil || got != time.Hour {
		t.Fatalf("pruneRetention(1h) = %s, %v", got, err)
	}
}

// TestWorktreeHeadOutsideARepository pins that a path that is not a
// repository is a typed error: only the documented unborn-HEAD case reports
// no head.
func TestWorktreeHeadOutsideARepository(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	m, _, _ := testManager(t)
	_, err := m.WorktreeHead(ctx, t.TempDir())
	var gitErr *GitError
	if !errors.As(err, &gitErr) || gitErr.Code() != errcode.Unavailable {
		t.Fatalf("err = %v, want a typed failure for a path that is not a repository", err)
	}
	if gitErr.Detail == "" {
		t.Fatalf("detail = %q, want git's message for the logs", gitErr.Detail)
	}
}

// TestHasStagedChangesClassifiesTheExitCode covers the diff exit-code table
// the result commit depends on, including the failure exit.
func TestHasStagedChangesClassifiesTheExitCode(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	m, _, _ := fakeGitManager(t, `exit 2`)
	_, err := m.hasStagedChanges(ctx, t.TempDir())
	var gitErr *GitError
	if !errors.As(err, &gitErr) || gitErr.Kind != failureGit {
		t.Fatalf("err = %v, want a typed git failure", err)
	}
}
