package gitops

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
)

// Package-level names shared by the operations.
const (
	// TeamBranchPrefix prefixes every team branch: fugue/team/<team-id>.
	TeamBranchPrefix = "fugue/team/"
	// TaskBranchPrefix prefixes every task branch: fugue/task/<task-id>.
	TaskBranchPrefix = "fugue/task/"
	// worktreesDirName is the directory under the data directory that holds
	// the task worktrees of every repository (orchestration §9).
	worktreesDirName = "worktrees"
	// hooksDirName is the empty per-host directory the runner passes as
	// core.hooksPath, so repository hooks never run as untrusted code.
	hooksDirName = "hooks"
	// gitopsDirName groups Fugue's own files under the data directory.
	gitopsDirName = "gitops"
	// maxErrorDetail bounds the git stderr kept in a GitError for logging.
	maxErrorDetail = 400
)

// Manager owns every git operation of Fugue for one data directory. All state
// is fixed at construction; the per-repo gates are created on demand.
type Manager struct {
	gitPath string
	root    string
	clk     clock.Clock

	mu    sync.Mutex
	gates map[string]*repoGate

	hooksOnce sync.Once
	hooksDir  string
	hooksErr  error
}

// NewManager returns a Manager that runs the git binary at gitPath (the caller
// resolves it, following internal/snapshot's Locate) for the repository at
// repoDir. The clock drives ref-lock backoff; tests use the fake clock.
func NewManager(gitPath, repoDir string, clk clock.Clock) (*Manager, error) {
	switch {
	case gitPath == "":
		return nil, errcode.New(errcode.InvalidArgument, "gitops: git binary path is empty")
	case !filepath.IsAbs(gitPath):
		return nil, errcode.New(errcode.InvalidArgument, "gitops: git binary path is not absolute")
	case repoDir == "":
		return nil, errcode.New(errcode.InvalidArgument, "gitops: data directory is empty")
	case !filepath.IsAbs(repoDir):
		return nil, errcode.New(errcode.InvalidArgument, "gitops: data directory is not absolute")
	case clk == nil:
		return nil, errcode.New(errcode.InvalidArgument, "gitops: clock is nil")
	}
	return &Manager{
		gitPath: gitPath,
		root:    filepath.Clean(repoDir),
		clk:     clk,
		gates:   make(map[string]*repoGate),
	}, nil
}

// HooksDir returns the empty directory Fugue owns and passes as
// core.hooksPath. It is created on first use under the data directory.
func (m *Manager) HooksDir() (string, error) {
	m.hooksOnce.Do(func() {
		dir := filepath.Join(m.root, gitopsDirName, hooksDirName)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			m.hooksErr = fmt.Errorf("gitops: create empty hooks directory: %w", err)
			return
		}
		m.hooksDir = dir
	})
	return m.hooksDir, m.hooksErr
}

// repoGate is a buffered channel used as a context-aware mutex, one per
// canonical common git dir (internal/snapshot uses the same shape).
type repoGate chan struct{}

func newRepoGate() *repoGate {
	g := make(repoGate, 1)
	return &g
}

func (g *repoGate) acquire(ctx context.Context) error {
	select {
	case *g <- struct{}{}:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("gitops: wait for repo lock: %w", ctx.Err())
	}
}

func (g *repoGate) release() { <-*g }

// withRepoLock runs fn while holding the gate of the repository behind dir,
// which may be the main worktree or any linked worktree. The gate key is the
// canonical (symlink-resolved) common git dir, so two paths to the same repo
// share one lock (OR-E06).
func (m *Manager) withRepoLock(ctx context.Context, dir string, fn func() error) error {
	gate, err := m.gateFor(ctx, dir)
	if err != nil {
		return err
	}
	if err := gate.acquire(ctx); err != nil {
		return err
	}
	defer gate.release()
	return fn()
}

// gateFor resolves the canonical common git dir of the repository behind dir
// and returns that repository's gate, creating it on first use.
func (m *Manager) gateFor(ctx context.Context, dir string) (*repoGate, error) {
	common, err := m.commonDir(ctx, dir)
	if err != nil {
		return nil, err
	}
	key, err := filepath.EvalSymlinks(common)
	if err != nil {
		return nil, fmt.Errorf("gitops: resolve common git dir: %w", err)
	}
	if key == "" {
		return nil, errcode.New(errcode.FailedPrecondition, "gitops: common git dir resolved to an empty path")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	gate, ok := m.gates[key]
	if !ok {
		gate = newRepoGate()
		m.gates[key] = gate
	}
	return gate, nil
}

// commonDir returns the absolute common git dir of the repository behind dir.
// git older than 2.31 does not know --path-format, so the plain form is the
// fallback and is made absolute against dir.
func (m *Manager) commonDir(ctx context.Context, dir string) (string, error) {
	res, err := m.exec(ctx, dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err == nil && res.ExitCode == 0 {
		return strings.TrimSpace(res.Stdout), nil
	}
	if err != nil {
		return "", err
	}
	res, err = m.exec(ctx, dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", m.gitFailure("rev-parse", res)
	}
	common := strings.TrimSpace(res.Stdout)
	if common == "" {
		return "", errcode.New(errcode.FailedPrecondition, "gitops: git reported an empty common dir")
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	return filepath.Clean(common), nil
}
