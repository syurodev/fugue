package gitops

import (
	"context"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

const (
	defaultDiffPageBytes = 16 << 10
	maxDiffPageBytes     = 64 << 10
	maxDiffTotalBytes    = 1 << 20
)

// DiffResult is a bounded, paged slice of a read-only git diff.
type DiffResult struct {
	Text       string
	Offset     int
	TotalBytes int
	HasMore    bool
	Truncated  bool
}

// DiffAgainstParent returns a bounded, paged read-only diff of the task's
// worktree HEAD against parentRef (orchestration §6.3, Q-127). The worktree
// path is derived through TaskWorktreePath/ValidateID and no write git command
// is ever run.
func (m *Manager) DiffAgainstParent(ctx context.Context, dataDir, repoID, taskID, parentRef string, offset, limit int) (DiffResult, error) {
	worktreeDir, err := TaskWorktreePath(dataDir, repoID, taskID)
	if err != nil {
		return DiffResult{}, err
	}
	if refErr := ValidateRefName(parentRef); refErr != nil {
		return DiffResult{}, refErr
	}
	if offset < 0 {
		return DiffResult{}, errcode.New(errcode.InvalidArgument, "gitops: diff offset must be non-negative")
	}
	if limit <= 0 {
		limit = defaultDiffPageBytes
	}
	if limit > maxDiffPageBytes {
		limit = maxDiffPageBytes
	}
	raw, err := m.run(ctx, worktreeDir, "diff", "--no-ext-diff", "--no-color", parentRef+"...HEAD")
	if err != nil {
		return DiffResult{}, err
	}
	return sliceDiff(raw, offset, limit), nil
}

func sliceDiff(raw string, offset, limit int) DiffResult {
	truncated := false
	if len(raw) > maxDiffTotalBytes {
		raw = raw[:maxDiffTotalBytes]
		truncated = true
	}
	total := len(raw)
	if offset >= total {
		return DiffResult{Offset: offset, TotalBytes: total, Truncated: truncated}
	}
	end := min(offset+limit, total)
	return DiffResult{
		Text:       raw[offset:end],
		Offset:     offset,
		TotalBytes: total,
		HasMore:    end < total,
		Truncated:  truncated,
	}
}
