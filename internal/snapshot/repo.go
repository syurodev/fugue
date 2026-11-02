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
	key      string
	workTree string
	gitDir   string
	gitPath  string
	limits   Limits
	clock    clock.Clock
	ids      ids.Generator

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
	toRefresh, toRemove, skipped := r.partitionIndex(tracked)

	untracked, untrackedSkips, err := r.untrackedFiles(ctx, patterns, len(toRefresh))
	if err != nil {
		return Snapshot{}, err
	}
	skipped = append(skipped, untrackedSkips...)

	toRemove = dropReplacedByDirectory(toRemove, toRefresh, untracked)
	if applyErr := r.applyIndexUpdates(ctx, toRemove, toRefresh, untracked); applyErr != nil {
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

func (r *Repo) partitionIndex(tracked []string) (keep, remove []string, skipped []Skip) {
	for _, rel := range tracked {
		if r.isDenied(rel) {
			remove = append(remove, rel)
			skipped = append(skipped, Skip{Path: rel, Reason: SkipExcluded})
			continue
		}
		info, statErr := os.Lstat(filepath.Join(r.workTree, filepath.FromSlash(rel)))
		if statErr != nil || info.IsDir() {
			remove = append(remove, rel)
			continue
		}
		if info.Mode().IsRegular() && info.Size() > r.limits.MaxFileBytes {
			remove = append(remove, rel)
			skipped = append(skipped, Skip{Path: rel, Reason: SkipTooLarge, Bytes: info.Size()})
			continue
		}
		keep = append(keep, rel)
	}
	return keep, remove, skipped
}

func (r *Repo) untrackedFiles(ctx context.Context, patterns []string, alreadyKept int) ([]string, []Skip, error) {
	args := make([]string, 0, 5+len(patterns))
	args = append(args, "ls-files", "-z", "--others", "--exclude-standard")
	for _, p := range patterns {
		args = append(args, "-x", p)
	}
	raw, err := r.git(ctx, nil, args...)
	if err != nil {
		return nil, nil, err
	}
	candidates := splitNUL(raw)
	if alreadyKept+len(candidates) > int(r.limits.MaxFiles) {
		return nil, nil, fmt.Errorf("%w: more than %d files in %s", ErrProjectTooLarge, r.limits.MaxFiles, r.workTree)
	}
	keep, skipped := r.filterUntrackedCandidates(candidates)
	return keep, skipped, nil
}

func (r *Repo) filterUntrackedCandidates(candidates []string) ([]string, []Skip) {
	var keep []string
	var skipped []Skip
	for _, rel := range candidates {
		clean := strings.TrimSuffix(rel, "/")
		if clean == "" {
			continue
		}
		if r.isDenied(clean) {
			skipped = append(skipped, Skip{Path: clean, Reason: SkipExcluded})
			continue
		}
		kept, skip, ok := r.classifyCandidate(rel, clean)
		if skip != nil {
			skipped = append(skipped, *skip)
		}
		if ok {
			keep = append(keep, kept)
		}
	}
	return keep, skipped
}

func (r *Repo) classifyCandidate(rawRel, clean string) (string, *Skip, bool) {
	full := filepath.Join(r.workTree, filepath.FromSlash(clean))
	info, err := os.Lstat(full)
	if err != nil {
		return "", &Skip{Path: clean, Reason: SkipUnreadable}, false
	}
	if info.IsDir() || strings.HasSuffix(rawRel, "/") {
		if looksLikeNestedRepo(full) {
			return "", &Skip{Path: clean, Reason: SkipNestedRepo}, false
		}
		return "", nil, false
	}
	mode := info.Mode()
	if !mode.IsRegular() && mode&os.ModeSymlink == 0 {
		return "", nil, false
	}
	if mode.IsRegular() && info.Size() > r.limits.MaxFileBytes {
		return "", &Skip{Path: clean, Reason: SkipTooLarge, Bytes: info.Size()}, false
	}
	return clean, nil, true
}

func dropReplacedByDirectory(toRemove, toRefresh, untracked []string) []string {
	existing := make(map[string]bool, len(toRemove))
	for _, p := range toRemove {
		existing[p] = true
	}
	for _, list := range [][]string{toRefresh, untracked} {
		for _, p := range list {
			for dir := filepath.ToSlash(filepath.Dir(p)); dir != "." && dir != "" && dir != "/"; dir = filepath.ToSlash(filepath.Dir(dir)) {
				if !existing[dir] {
					existing[dir] = true
					toRemove = append(toRemove, dir)
				}
			}
		}
	}
	return toRemove
}

func (r *Repo) isDenied(rel string) bool {
	dirs := defaultExcludedDirs()
	for _, part := range strings.Split(rel, "/") {
		for _, denied := range dirs {
			if part == denied {
				return true
			}
		}
	}
	base := filepath.Base(rel)
	for _, denied := range defaultExcludedFiles() {
		if base == denied {
			return true
		}
	}
	return false
}

func looksLikeNestedRepo(fullDir string) bool {
	_, err := os.Lstat(filepath.Join(fullDir, ".git"))
	return err == nil
}

func (r *Repo) git(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	runCtx := ctx
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		runCtx, cancel = context.WithTimeout(ctx, r.limits.GitTimeout)
		defer cancel()
	}

	full := make([]string, 0, 5+len(args))
	full = append(full, "--git-dir="+r.gitDir)
	if len(args) == 0 || args[0] != "init" {
		full = append(full, "--work-tree="+r.workTree)
	}
	full = append(full, "-c", "core.quotePath=false")
	full = append(full, args...)

	cmd := exec.CommandContext(runCtx, "git")
	cmd.Path = r.gitPath
	cmd.Args = append([]string{r.gitPath}, full...)
	cmd.Dir = r.workTree
	cmd.Env = cleanGitEnv(r.gitDir)
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
