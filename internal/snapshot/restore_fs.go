package snapshot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func (r *Repo) deleteRestoredFile(wsRoot *os.Root, rel string) error {
	fsRel := filepath.FromSlash(rel)
	if err := wsRoot.Remove(fsRel); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("snapshot: remove %s: %w", rel, err)
	}
	removeEmptyParents(wsRoot, filepath.Dir(fsRel))
	return nil
}

func (r *Repo) writeFileFromBlob(ctx context.Context, wsRoot *os.Root, rel string, target entry) error {
	fsRel := filepath.FromSlash(rel)
	parent := filepath.Dir(fsRel)
	if parent != "." && parent != "" {
		if err := wsRoot.MkdirAll(parent, 0o750); err != nil {
			return fmt.Errorf("snapshot: mkdir %s: %w", rel, err)
		}
	}
	if target.mode == "120000" {
		return r.writeSymlinkFromBlob(ctx, wsRoot, rel, fsRel, target.blob)
	}
	return r.writeRegularFromBlob(ctx, wsRoot, rel, fsRel, target)
}

func (r *Repo) writeSymlinkFromBlob(ctx context.Context, wsRoot *os.Root, rel, fsRel, blob string) error {
	linkBytes, err := r.git(ctx, nil, "cat-file", "blob", blob)
	if err != nil {
		return err
	}
	tmp, err := r.uniqueTempRel(filepath.Dir(fsRel))
	if err != nil {
		return err
	}
	removeBestEffort(wsRoot, tmp)
	if err := wsRoot.Symlink(string(linkBytes), tmp); err != nil {
		return fmt.Errorf("snapshot: symlink %s: %w", rel, err)
	}
	if err := wsRoot.Rename(tmp, fsRel); err != nil {
		removeBestEffort(wsRoot, tmp)
		return fmt.Errorf("snapshot: rename symlink %s: %w", rel, err)
	}
	return nil
}

func (r *Repo) writeRegularFromBlob(ctx context.Context, wsRoot *os.Root, rel, fsRel string, target entry) error {
	perm := os.FileMode(0o600)
	if target.mode == "100755" {
		perm = 0o750
	}
	tmp, err := r.uniqueTempRel(filepath.Dir(fsRel))
	if err != nil {
		return err
	}
	file, err := wsRoot.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return fmt.Errorf("snapshot: create temp for %s: %w", rel, err)
	}
	streamErr := r.streamBlobToFile(ctx, target.blob, file)
	closeErr := file.Close()
	if streamErr != nil {
		removeBestEffort(wsRoot, tmp)
		return streamErr
	}
	if closeErr != nil {
		removeBestEffort(wsRoot, tmp)
		return fmt.Errorf("snapshot: close temp for %s: %w", rel, closeErr)
	}
	if err := wsRoot.Chmod(tmp, perm); err != nil {
		removeBestEffort(wsRoot, tmp)
		return fmt.Errorf("snapshot: chmod %s: %w", rel, err)
	}
	if err := wsRoot.Rename(tmp, fsRel); err != nil {
		removeBestEffort(wsRoot, tmp)
		return fmt.Errorf("snapshot: replace %s: %w", rel, err)
	}
	return nil
}

func removeBestEffort(wsRoot *os.Root, rel string) {
	if err := wsRoot.Remove(rel); err != nil && !errors.Is(err, os.ErrNotExist) {
		return
	}
}

func (r *Repo) streamBlobToFile(ctx context.Context, blob string, dst *os.File) error {
	runCtx := ctx
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		runCtx, cancel = context.WithTimeout(ctx, r.limits.GitTimeout)
		defer cancel()
	}
	cmd := exec.CommandContext(runCtx, "git")
	cmd.Path = r.gitPath
	cmd.Args = []string{r.gitPath, "--git-dir=" + r.gitDir, "cat-file", "blob", blob}
	cmd.Env = cleanGitEnv(r.gitDir)
	cmd.Stdout = dst
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("snapshot: git cat-file %s: %s: %w", blob, strings.TrimSpace(stderr.String()), err)
	}
	return nil
}

func removeEmptyParents(wsRoot *os.Root, relDir string) {
	for dir := filepath.Clean(relDir); dir != "." && dir != "" && dir != string(filepath.Separator); dir = filepath.Dir(dir) {
		if err := wsRoot.Remove(dir); err != nil {
			return
		}
	}
}

func (r *Repo) readTree(ctx context.Context, tree string) (map[string]entry, error) {
	raw, err := r.git(ctx, nil, "ls-tree", "-r", "-z", tree)
	if err != nil {
		return nil, err
	}
	out := make(map[string]entry)
	for _, record := range splitNUL(raw) {
		meta, path, ok := strings.Cut(record, "\t")
		if !ok {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) < 3 {
			return nil, fmt.Errorf("%w: %q", ErrTreeParse, record)
		}
		if fields[1] == "commit" {
			continue
		}
		out[path] = entry{
			mode:    canonicalMode(fields[0]),
			blob:    fields[2],
			present: true,
		}
	}
	return out, nil
}

func canonicalMode(mode string) string {
	switch mode {
	case "120000":
		return "120000"
	case "100755":
		return "100755"
	default:
		return "100644"
	}
}

func (r *Repo) currentEntry(ctx context.Context, rel string) (cur entry, skipped bool, err error) {
	full, err := checkSafeRelative(r.workTree, rel)
	if err != nil {
		return entry{}, false, err
	}
	info, err := os.Lstat(full)
	if errors.Is(err, os.ErrNotExist) {
		return entry{}, false, nil
	}
	if err != nil {
		return entry{}, false, fmt.Errorf("snapshot: lstat %s: %w", rel, err)
	}
	if info.IsDir() {
		return entry{}, true, nil
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return r.symlinkEntry(ctx, full, rel)
	}
	if !info.Mode().IsRegular() || info.Size() > r.limits.MaxFileBytes {
		return entry{}, true, nil
	}
	raw, err := r.git(ctx, []byte(rel+"\n"), "hash-object", "--no-filters", "--stdin-paths")
	if err != nil {
		return entry{}, false, err
	}
	mode := "100644"
	if info.Mode()&0o111 != 0 {
		mode = "100755"
	}
	return entry{mode: mode, blob: strings.TrimSpace(string(raw)), present: true}, false, nil
}

func (r *Repo) symlinkEntry(ctx context.Context, full, rel string) (entry, bool, error) {
	target, err := os.Readlink(full)
	if err != nil {
		return entry{}, false, fmt.Errorf("snapshot: readlink %s: %w", rel, err)
	}
	blob, err := r.hashBytes(ctx, []byte(target))
	if err != nil {
		return entry{}, false, err
	}
	return entry{mode: "120000", blob: blob, present: true}, false, nil
}

func (r *Repo) hashBytes(ctx context.Context, data []byte) (string, error) {
	raw, err := r.git(ctx, data, "hash-object", "-t", "blob", "--no-filters", "--stdin")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

// ErrUnsafePath reports a path that escapes the workspace root or traverses a
// symlinked parent directory.
var ErrUnsafePath = errors.New("snapshot: unsafe relative path")

func checkSafeRelative(workTree, rel string) (string, error) {
	if rel == "" || strings.ContainsRune(rel, 0) {
		return "", fmt.Errorf("%w: empty or NUL path", ErrUnsafePath)
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q escapes the project root", ErrUnsafePath, rel)
	}
	full := filepath.Join(workTree, clean)
	relCheck, err := filepath.Rel(workTree, full)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q resolves outside %s", ErrUnsafePath, rel, workTree)
	}
	if err := checkParentChain(workTree, filepath.Dir(full)); err != nil {
		return "", err
	}
	return full, nil
}

func checkParentChain(workTree, targetDir string) error {
	rel, err := filepath.Rel(workTree, targetDir)
	if err != nil {
		return fmt.Errorf("snapshot: relative parent path: %w", err)
	}
	if rel == "." {
		return nil
	}
	cur := workTree
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		info, statErr := os.Lstat(cur)
		if errors.Is(statErr, os.ErrNotExist) {
			return nil
		}
		if statErr != nil {
			return fmt.Errorf("lstat %s: %w", cur, statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: parent directory %s is a symlink", ErrUnsafePath, cur)
		}
	}
	return nil
}

func (r *Repo) uniqueTempRel(relDir string) (string, error) {
	id, err := r.ids.NewID()
	if err != nil {
		return "", fmt.Errorf("snapshot: generate temp id: %w", err)
	}
	name := ".fugue-restore-" + id
	if relDir == "." || relDir == "" {
		return name, nil
	}
	return filepath.Join(relDir, name), nil
}
