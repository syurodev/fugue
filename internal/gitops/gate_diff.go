package gitops

import (
	"bytes"
	"context"
	"strconv"
	"strings"
)

// DiffStat summarizes the file and line count of a task diff (orchestration §10.3).
type DiffStat struct {
	Files int
	Lines int
}

// FileAddedDiff holds the added or modified lines of one changed file for
// secret scanning (orchestration §10.2).
type FileAddedDiff struct {
	Path      string
	AddedText string
}

// ChangedFiles returns the normalized repo-relative paths changed between
// parentRef and HEAD in the task's worktree (orchestration §10.1, OR-E04).
func (m *Manager) ChangedFiles(ctx context.Context, dataDir, repoID, taskID, parentRef string) ([]string, error) {
	worktreeDir, err := m.prepareDiffWorktree(dataDir, repoID, taskID, parentRef)
	if err != nil {
		return nil, err
	}
	raw, err := m.run(ctx, worktreeDir, "diff", "--name-only", "-z", "--no-ext-diff", "--no-textconv", parentRef+"...HEAD")
	if err != nil {
		return nil, err
	}
	var files []string
	for part := range bytes.SplitSeq([]byte(raw), []byte{0}) {
		if len(part) == 0 {
			continue
		}
		files = append(files, string(part))
	}
	return files, nil
}

// DiffNumstat returns the changed file count and added+deleted line count
// between parentRef and HEAD in the task's worktree (orchestration §10.3).
func (m *Manager) DiffNumstat(ctx context.Context, dataDir, repoID, taskID, parentRef string) (DiffStat, error) {
	worktreeDir, err := m.prepareDiffWorktree(dataDir, repoID, taskID, parentRef)
	if err != nil {
		return DiffStat{}, err
	}
	raw, err := m.run(ctx, worktreeDir, "diff", "--numstat", "-z", "--no-ext-diff", "--no-textconv", parentRef+"...HEAD")
	if err != nil {
		return DiffStat{}, err
	}
	return parseNumstatZ([]byte(raw)), nil
}

// DiffAddedByFile returns only the added and modified lines per changed file
// between parentRef and HEAD in the task's worktree (orchestration §10.2).
func (m *Manager) DiffAddedByFile(ctx context.Context, dataDir, repoID, taskID, parentRef string) ([]FileAddedDiff, error) {
	worktreeDir, err := m.prepareDiffWorktree(dataDir, repoID, taskID, parentRef)
	if err != nil {
		return nil, err
	}
	raw, err := m.run(ctx, worktreeDir, "diff", "-U0", "--no-ext-diff", "--no-textconv", "--no-color", parentRef+"...HEAD")
	if err != nil {
		return nil, err
	}
	return parseAddedByFile(raw), nil
}

func (m *Manager) prepareDiffWorktree(dataDir, repoID, taskID, parentRef string) (string, error) {
	worktreeDir, err := TaskWorktreePath(dataDir, repoID, taskID)
	if err != nil {
		return "", err
	}
	if refErr := ValidateRefName(parentRef); refErr != nil {
		return "", refErr
	}
	return worktreeDir, nil
}

func parseNumstatZ(raw []byte) DiffStat {
	var stat DiffStat
	tokens := bytes.Split(raw, []byte{0})
	for i := 0; i < len(tokens); i++ {
		tok := string(tokens[i])
		if tok == "" {
			continue
		}
		addedStr, rest, ok := strings.Cut(tok, "\t")
		if !ok {
			continue
		}
		deletedStr, pathPart, ok := strings.Cut(rest, "\t")
		if !ok {
			continue
		}
		if pathPart == "" {
			// -z rename/copy format emits "\t\x00old\x00new\x00".
			i += 2
		}
		stat.Files++
		if added, err := strconv.Atoi(addedStr); err == nil {
			stat.Lines += added
		}
		if deleted, err := strconv.Atoi(deletedStr); err == nil {
			stat.Lines += deleted
		}
	}
	return stat
}

func parseAddedByFile(raw string) []FileAddedDiff {
	var out []FileAddedDiff
	var curPath string
	var buf strings.Builder
	flush := func() {
		if curPath != "" && buf.Len() > 0 {
			out = append(out, FileAddedDiff{Path: curPath, AddedText: buf.String()})
		}
		curPath = ""
		buf.Reset()
	}
	for line := range strings.SplitSeq(raw, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
		case strings.HasPrefix(line, "+++ "):
			p := strings.TrimPrefix(line, "+++ ")
			if p == "/dev/null" {
				curPath = ""
				continue
			}
			curPath = strings.TrimPrefix(p, "b/")
		case curPath != "" && strings.HasPrefix(line, "+"):
			if buf.Len() > 0 {
				buf.WriteByte('\n')
			}
			buf.WriteString(line[1:])
		}
	}
	flush()
	return out
}
