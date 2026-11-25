package snapshot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
)

// shadowAttributes disables filters and line-ending conversion inside the
// shadow repository while keeping diff enabled so numstat counts lines.
const shadowAttributes = "* -text -ident -filter -merge -crlf -working-tree-encoding\n"

// Repo is one project's shadow Git repository.
type Repo struct {
	key           string
	workTree      string
	gitDir        string
	gitPath       string
	limits        Limits
	clock         clock.Clock
	ids           ids.Generator
	excludedDirs  map[string]struct{}
	excludedFiles map[string]struct{}

	initOnce sync.Once
	initErr  error

	// gate is a buffered channel of size 1 used as a context-aware mutex.
	gate chan struct{}
}

// Key returns the project's stable directory key.
func (r *Repo) Key() string { return r.key }

// WorkTree returns the project's canonical working-tree directory.
func (r *Repo) WorkTree() string { return r.workTree }

// GitDir returns the path of the shadow Git repository on disk.
func (r *Repo) GitDir() string { return r.gitDir }

func (r *Repo) runLocked(ctx context.Context, fn func() error) error {
	select {
	case <-r.gate:
		defer func() { r.gate <- struct{}{} }()
		if err := r.clearStaleLocks(); err != nil {
			return err
		}
		return fn()
	case <-ctx.Done():
		return fmt.Errorf("snapshot: wait for project lock: %w", ctx.Err())
	}
}

func (r *Repo) ensureInitialised(ctx context.Context) error {
	r.initOnce.Do(func() {
		r.initErr = r.runLocked(ctx, func() error {
			return r.initialise(ctx)
		})
	})
	return r.initErr
}

func (r *Repo) initialise(ctx context.Context) error {
	if err := os.MkdirAll(r.gitDir, 0o700); err != nil {
		return fmt.Errorf("snapshot: create shadow directory: %w", err)
	}
	headPath := filepath.Join(r.gitDir, "HEAD")
	if _, err := os.Stat(headPath); errors.Is(err, os.ErrNotExist) {
		if _, initErr := r.git(ctx, nil, "init", "--bare", "--quiet", r.gitDir); initErr != nil {
			return initErr
		}
	}
	return r.configureShadowRepo(ctx)
}

func (r *Repo) configureShadowRepo(ctx context.Context) error {
	settings := [][2]string{
		{"core.bare", gitFalse},
		{"core.autocrlf", gitFalse},
		{"core.safecrlf", gitFalse},
		{"core.quotepath", gitFalse},
		{"core.filemode", gitTrue},
		{"core.symlinks", gitTrue},
		{"core.fsmonitor", gitFalse},
		{"core.hooksPath", os.DevNull},
		{"core.looseCompression", "0"},
		{"core.fsync", "none"},
		{"index.threads", gitTrue},
		{"index.version", "4"},
		{"gc.auto", "0"},
		{"advice.ignoredHook", gitFalse},
	}
	if runtime.GOOS == goosDarwin {
		settings = append(settings, [2]string{"core.precomposeunicode", gitTrue})
	}
	for _, kv := range settings {
		if _, err := r.git(ctx, nil, "config", "--local", kv[0], kv[1]); err != nil {
			return err
		}
	}
	return r.writeShadowFiles()
}

func (r *Repo) writeShadowFiles() (err error) {
	gitRoot, err := os.OpenRoot(r.gitDir)
	if err != nil {
		return fmt.Errorf("snapshot: open shadow root: %w", err)
	}
	defer func() {
		if closeErr := gitRoot.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("snapshot: close shadow root: %w", closeErr)
		}
	}()
	if err := gitRoot.Chmod(".", 0o700); err != nil {
		return fmt.Errorf("snapshot: chmod shadow directory: %w", err)
	}
	if err := gitRoot.MkdirAll("info", 0o700); err != nil {
		return fmt.Errorf("snapshot: create info directory: %w", err)
	}
	if err := gitRoot.WriteFile("info/attributes", []byte(shadowAttributes), 0o600); err != nil {
		return fmt.Errorf("snapshot: write info/attributes: %w", err)
	}
	if err := gitRoot.WriteFile(projectFileName, []byte(r.workTree+"\n"), 0o600); err != nil {
		return fmt.Errorf("snapshot: write project marker: %w", err)
	}
	return nil
}

// Snapshot records the current working tree and pins the resulting tree object
// under refs/fugue/cp/<checkpointID>/<slot>.
func (r *Repo) Snapshot(ctx context.Context, checkpointID, slot string) (Snapshot, error) {
	if strings.TrimSpace(checkpointID) == "" {
		return Snapshot{}, errcode.New(errcode.InvalidArgument, "snapshot: checkpoint id is empty")
	}
	if slot != SlotBefore && slot != SlotAfter {
		return Snapshot{}, errcode.New(errcode.InvalidArgument, "snapshot: invalid slot "+slot)
	}
	var out Snapshot
	err := r.runLocked(ctx, func() error {
		var snapErr error
		out, snapErr = r.snapshotLocked(ctx, checkpointID, slot)
		return snapErr
	})
	return out, err
}

func (r *Repo) snapshotLocked(ctx context.Context, checkpointID, slot string) (Snapshot, error) {
	started := r.clock.Now()
	patterns := denyPatterns(r.limits.ExtraExcludes)

	tracked, err := r.indexPaths(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	toRefresh, toRemove, skipped, err := r.partitionIndex(ctx, tracked)
	if err != nil {
		return Snapshot{}, err
	}

	untracked, untrackedSkips, err := r.untrackedFiles(ctx, patterns, len(toRefresh))
	if err != nil {
		return Snapshot{}, err
	}
	skipped = append(skipped, untrackedSkips...)
	toRemove = dropReplacedByDirectory(tracked, toRemove, toRefresh, untracked)

	if shouldUseFastImport(len(tracked), len(untracked)) {
		tree, allSkips, fiErr := r.snapshotFastImportLocked(
			ctx, checkpointID, slot, tracked, toRemove, toRefresh, untracked, skipped,
		)
		if fiErr != nil {
			return Snapshot{}, fiErr
		}
		return Snapshot{
			Tree:    tree,
			Skipped: allSkips,
			Elapsed: r.clock.Now().Sub(started),
		}, nil
	}

	untrackedPaths := candidatePathsOnly(untracked)
	if applyErr := r.applyIndexUpdates(ctx, toRemove, toRefresh, untrackedPaths); applyErr != nil {
		return Snapshot{}, applyErr
	}

	rawTree, err := r.git(ctx, nil, "write-tree")
	if err != nil {
		return Snapshot{}, err
	}
	tree := strings.TrimSpace(string(rawTree))
	if tree == "" {
		return Snapshot{}, errcode.New(errcode.Internal, "snapshot: git write-tree returned an empty hash")
	}
	if _, refErr := r.git(ctx, nil, "update-ref", RefName(checkpointID, slot), tree); refErr != nil {
		return Snapshot{}, refErr
	}
	return Snapshot{
		Tree:    tree,
		Skipped: skipped,
		Elapsed: r.clock.Now().Sub(started),
	}, nil
}

func (r *Repo) applyIndexUpdates(ctx context.Context, toRemove, toRefresh, untracked []string) error {
	if len(toRemove) > 0 {
		if _, err := r.git(ctx, joinNUL(toRemove), "update-index", "-z", "--force-remove", "--stdin"); err != nil {
			return err
		}
	}
	toAdd := make([]string, 0, len(toRefresh)+len(untracked))
	toAdd = append(toAdd, toRefresh...)
	toAdd = append(toAdd, untracked...)
	if len(toAdd) > 0 {
		if _, err := r.git(ctx, joinNUL(toAdd), "update-index", "-z", "--add", "--remove", "--stdin"); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repo) indexPaths(ctx context.Context) ([]string, error) {
	raw, err := r.git(ctx, nil, "ls-files", "-z", "--cached")
	if err != nil {
		return nil, err
	}
	return splitNUL(raw), nil
}

func candidatePathsOnly(entries []candidateEntry) []string {
	if len(entries) == 0 {
		return nil
	}
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.rel
	}
	return out
}

func (r *Repo) git(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	runCtx, cancel := r.withGitTimeout(ctx)
	defer cancel()
	cmd := r.gitCommand(runCtx, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		verb := "git"
		if len(args) > 0 {
			verb = "git " + args[0]
		}
		return nil, fmt.Errorf("snapshot: %s: %s: %w", verb, msg, err)
	}
	return stdout.Bytes(), nil
}

// withGitTimeout bounds a git run by the configured timeout unless the caller
// already set a deadline.
func (r *Repo) withGitTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, r.limits.GitTimeout)
}

// gitCommand builds a git invocation against the shadow repository with a
// scrubbed environment, so no user or repository configuration can run code.
func (r *Repo) gitCommand(ctx context.Context, args ...string) *exec.Cmd {
	full := make([]string, 0, 5+len(args))
	full = append(full, "--git-dir="+r.gitDir)
	if len(args) == 0 || args[0] != "init" {
		full = append(full, "--work-tree="+r.workTree)
	}
	full = append(full, "-c", "core.quotePath=false")
	full = append(full, args...)

	cmd := exec.CommandContext(ctx, "git")
	cmd.Path = r.gitPath
	cmd.Args = append([]string{r.gitPath}, full...)
	cmd.Dir = r.workTree
	cmd.Env = cleanGitEnv(r.gitDir)
	return cmd
}

func cleanGitEnv(gitDir string) []string {
	raw := os.Environ()
	out := make([]string, 0, len(raw)+4)
	for _, kv := range raw {
		key, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "GIT_") {
			continue
		}
		out = append(out, kv)
	}
	return append(out,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_INDEX_FILE="+filepath.Join(gitDir, "index"),
	)
}

func splitNUL(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	parts := bytes.Split(raw, []byte{0})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if len(p) > 0 {
			out = append(out, string(p))
		}
	}
	return out
}

func joinNUL(paths []string) []byte {
	var buf bytes.Buffer
	for _, p := range paths {
		buf.WriteString(p)
		buf.WriteByte(0)
	}
	return buf.Bytes()
}
