package gitops

import (
	"context"
	"fmt"
	"strings"
)

// WorktreeStatusResult describes the HEAD commit and uncommitted-change status
// of a task worktree inspected during host-restart recovery (§12, OR-E09).
type WorktreeStatusResult struct {
	HeadSHA      string
	Dirty        bool
	ChangedFiles int
	Summary      string
}

// WorktreeStatus reads a task worktree's HEAD commit and `git status --porcelain`
// output. It is read-only and returns a typed error if worktreeDir is missing
// or not a git worktree.
func (m *Manager) WorktreeStatus(ctx context.Context, worktreeDir string) (WorktreeStatusResult, error) {
	head, err := m.WorktreeHead(ctx, worktreeDir)
	if err != nil {
		return WorktreeStatusResult{}, err
	}
	res, err := m.exec(ctx, worktreeDir, "status", "--porcelain")
	if err != nil {
		return WorktreeStatusResult{}, err
	}
	if res.ExitCode != 0 {
		return WorktreeStatusResult{}, m.gitFailure("status", res)
	}
	return summarizePorcelainStatus(head, res.Stdout), nil
}

func summarizePorcelainStatus(head, stdout string) WorktreeStatusResult {
	var changed int
	for _, line := range strings.Split(stdout, "\n") {
		if strings.TrimSpace(line) != "" {
			changed++
		}
	}
	headLabel := head
	if headLabel == "" {
		headLabel = "unborn"
	}
	status := "clean"
	if changed > 0 {
		status = fmt.Sprintf("dirty (%d changed)", changed)
	}
	return WorktreeStatusResult{
		HeadSHA:      head,
		Dirty:        changed > 0,
		ChangedFiles: changed,
		Summary:      fmt.Sprintf("head=%s status=%s", headLabel, status),
	}
}
