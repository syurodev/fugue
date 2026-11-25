package snapshot

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/syurodev/fugue/internal/platform/supervise"
)

const (
	gitModeRegular = "100644"
	gitModeExec    = "100755"
	gitModeSymlink = "120000"
)

type partitionChunk struct {
	keep    []string
	remove  []string
	skipped []Skip
	err     error
}

func (r *Repo) partitionIndex(ctx context.Context, tracked []string) (keep, remove []string, skipped []Skip, err error) {
	if len(tracked) == 0 {
		return nil, nil, nil, nil
	}
	workers := statWorkerCount(len(tracked))
	if workers == 1 {
		chunk := r.partitionIndexSlice(ctx, tracked)
		return chunk.keep, chunk.remove, chunk.skipped, chunk.err
	}
	chunks := make([]partitionChunk, workers)
	tasks := make([]*supervise.Task, 0, workers)
	for i := range workers {
		start := i * len(tracked) / workers
		end := (i + 1) * len(tracked) / workers
		tasks = append(tasks, supervise.Go(ctx, "snapshot-partition-index", func(tctx context.Context) error {
			chunks[i] = r.partitionIndexSlice(tctx, tracked[start:end])
			return nil
		}))
	}
	for _, task := range tasks {
		if waitErr := task.Wait(); waitErr != nil && err == nil {
			err = waitErr
		}
	}
	if err != nil {
		return nil, nil, nil, err
	}
	return mergePartitionChunks(chunks)
}

func mergePartitionChunks(chunks []partitionChunk) (keep, remove []string, skipped []Skip, err error) {
	var totalKeep, totalRemove, totalSkipped int
	for i := range chunks {
		if chunks[i].err != nil {
			return nil, nil, nil, chunks[i].err
		}
		totalKeep += len(chunks[i].keep)
		totalRemove += len(chunks[i].remove)
		totalSkipped += len(chunks[i].skipped)
	}
	keep = make([]string, 0, totalKeep)
	remove = make([]string, 0, totalRemove)
	skipped = make([]Skip, 0, totalSkipped)
	for i := range chunks {
		keep = append(keep, chunks[i].keep...)
		remove = append(remove, chunks[i].remove...)
		skipped = append(skipped, chunks[i].skipped...)
	}
	return keep, remove, skipped, nil
}

func (r *Repo) partitionIndexSlice(ctx context.Context, tracked []string) partitionChunk {
	out := partitionChunk{keep: make([]string, 0, len(tracked))}
	for _, rel := range tracked {
		if err := ctx.Err(); err != nil {
			out.err = err
			return out
		}
		if r.isDenied(rel) {
			out.remove = append(out.remove, rel)
			out.skipped = append(out.skipped, Skip{Path: rel, Reason: SkipExcluded})
			continue
		}
		info, statErr := os.Lstat(filepath.Join(r.workTree, filepath.FromSlash(rel)))
		if statErr != nil || info.IsDir() {
			out.remove = append(out.remove, rel)
			continue
		}
		if info.Mode().IsRegular() && info.Size() > r.limits.MaxFileBytes {
			out.remove = append(out.remove, rel)
			out.skipped = append(out.skipped, Skip{Path: rel, Reason: SkipTooLarge, Bytes: info.Size()})
			continue
		}
		out.keep = append(out.keep, rel)
	}
	return out
}

func (r *Repo) untrackedFiles(ctx context.Context, patterns []string, alreadyKept int) ([]candidateEntry, []Skip, error) {
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
	return r.filterUntrackedCandidates(ctx, candidates)
}

type candidateChunk struct {
	keep    []candidateEntry
	skipped []Skip
	err     error
}

func (r *Repo) filterUntrackedCandidates(ctx context.Context, candidates []string) ([]candidateEntry, []Skip, error) {
	if len(candidates) == 0 {
		return nil, nil, nil
	}
	workers := statWorkerCount(len(candidates))
	if workers == 1 {
		chunk := r.filterCandidatesSlice(ctx, candidates)
		return chunk.keep, chunk.skipped, chunk.err
	}
	chunks := make([]candidateChunk, workers)
	tasks := make([]*supervise.Task, 0, workers)
	for i := range workers {
		start := i * len(candidates) / workers
		end := (i + 1) * len(candidates) / workers
		tasks = append(tasks, supervise.Go(ctx, "snapshot-classify-candidates", func(tctx context.Context) error {
			chunks[i] = r.filterCandidatesSlice(tctx, candidates[start:end])
			return nil
		}))
	}
	var waitErr error
	for _, task := range tasks {
		if err := task.Wait(); err != nil && waitErr == nil {
			waitErr = err
		}
	}
	if waitErr != nil {
		return nil, nil, waitErr
	}
	return mergeCandidateChunks(chunks)
}

func mergeCandidateChunks(chunks []candidateChunk) ([]candidateEntry, []Skip, error) {
	var totalKeep, totalSkipped int
	for i := range chunks {
		if chunks[i].err != nil {
			return nil, nil, chunks[i].err
		}
		totalKeep += len(chunks[i].keep)
		totalSkipped += len(chunks[i].skipped)
	}
	keep := make([]candidateEntry, 0, totalKeep)
	skipped := make([]Skip, 0, totalSkipped)
	for i := range chunks {
		keep = append(keep, chunks[i].keep...)
		skipped = append(skipped, chunks[i].skipped...)
	}
	return keep, skipped, nil
}

func (r *Repo) filterCandidatesSlice(ctx context.Context, candidates []string) candidateChunk {
	out := candidateChunk{keep: make([]candidateEntry, 0, len(candidates))}
	for _, rel := range candidates {
		if err := ctx.Err(); err != nil {
			out.err = err
			return out
		}
		clean := strings.TrimSuffix(rel, "/")
		if clean == "" {
			continue
		}
		if r.isDenied(clean) {
			out.skipped = append(out.skipped, Skip{Path: clean, Reason: SkipExcluded})
			continue
		}
		kept, skip, ok := r.classifyCandidate(rel, clean)
		if skip != nil {
			out.skipped = append(out.skipped, *skip)
		}
		if ok {
			out.keep = append(out.keep, kept)
		}
	}
	return out
}

func (r *Repo) classifyCandidate(rawRel, clean string) (candidateEntry, *Skip, bool) {
	full := filepath.Join(r.workTree, filepath.FromSlash(clean))
	info, err := os.Lstat(full)
	if err != nil {
		return candidateEntry{}, &Skip{Path: clean, Reason: SkipUnreadable}, false
	}
	if info.IsDir() || strings.HasSuffix(rawRel, "/") {
		if looksLikeNestedRepo(full) {
			return candidateEntry{}, &Skip{Path: clean, Reason: SkipNestedRepo}, false
		}
		return candidateEntry{}, nil, false
	}
	mode := info.Mode()
	if !mode.IsRegular() && mode&os.ModeSymlink == 0 {
		return candidateEntry{}, nil, false
	}
	if mode.IsRegular() && info.Size() > r.limits.MaxFileBytes {
		return candidateEntry{}, &Skip{Path: clean, Reason: SkipTooLarge, Bytes: info.Size()}, false
	}
	return candidateEntry{rel: clean, mode: candidateGitMode(mode)}, nil, true
}

func candidateGitMode(mode os.FileMode) string {
	if mode&os.ModeSymlink != 0 {
		return gitModeSymlink
	}
	if mode&0o100 != 0 {
		return gitModeExec
	}
	return gitModeRegular
}

func dropReplacedByDirectory(tracked, toRemove, toRefresh []string, untracked []candidateEntry) []string {
	if len(tracked) == 0 {
		return toRemove
	}
	trackedSet := make(map[string]struct{}, len(tracked))
	for _, p := range tracked {
		trackedSet[p] = struct{}{}
	}
	existing := make(map[string]struct{}, len(toRemove))
	for _, p := range toRemove {
		existing[p] = struct{}{}
	}
	toRemove = markTrackedAncestors(toRemove, toRefresh, trackedSet, existing)
	for _, entry := range untracked {
		toRemove = markPathAncestors(toRemove, entry.rel, trackedSet, existing)
	}
	return toRemove
}

func markTrackedAncestors(toRemove, paths []string, trackedSet, existing map[string]struct{}) []string {
	for _, p := range paths {
		toRemove = markPathAncestors(toRemove, p, trackedSet, existing)
	}
	return toRemove
}

func markPathAncestors(toRemove []string, rel string, trackedSet, existing map[string]struct{}) []string {
	for dir := parentSlashDir(rel); dir != ""; dir = parentSlashDir(dir) {
		if _, inTracked := trackedSet[dir]; !inTracked {
			continue
		}
		if _, already := existing[dir]; !already {
			existing[dir] = struct{}{}
			toRemove = append(toRemove, dir)
		}
	}
	return toRemove
}

func parentSlashDir(rel string) string {
	i := strings.LastIndexByte(rel, '/')
	if i <= 0 {
		return ""
	}
	return rel[:i]
}

func (r *Repo) isDenied(rel string) bool {
	trimmed := strings.TrimSuffix(rel, "/")
	if trimmed == "" {
		return false
	}
	var last string
	rest := trimmed
	for {
		seg, after, found := strings.Cut(rest, "/")
		if _, ok := r.excludedDirs[seg]; ok {
			return true
		}
		if !found {
			last = seg
			break
		}
		rest = after
	}
	_, ok := r.excludedFiles[last]
	return ok
}

func looksLikeNestedRepo(fullDir string) bool {
	_, err := os.Lstat(filepath.Join(fullDir, ".git"))
	return err == nil
}
