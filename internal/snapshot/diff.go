package snapshot

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// ChangeKind classifies how a path changed between two trees.
type ChangeKind string

const (
	// ChangeAdded means the path was absent from the first tree and present
	// in the second.
	ChangeAdded ChangeKind = "added"
	// ChangeModified means the path was present in both trees with
	// different content or mode.
	ChangeModified ChangeKind = "modified"
	// ChangeDeleted means the path was present in the first tree and absent
	// from the second.
	ChangeDeleted ChangeKind = "deleted"
)

// Change describes one path's difference between two snapshot trees. Added and
// Removed are -1 for binary files.
type Change struct {
	Path    string     `json:"path"`
	Kind    ChangeKind `json:"kind"`
	Mode    string     `json:"mode"`
	Added   int        `json:"added"`
	Removed int        `json:"removed"`
}

// Binary reports whether git treated the file as binary when counting lines.
func (c Change) Binary() bool {
	return c.Added < 0 || c.Removed < 0
}

// Diff returns the file-by-file difference from fromTree to toTree, with added
// and removed line counts populated for text files.
func (r *Repo) Diff(ctx context.Context, fromTree, toTree string) ([]Change, error) {
	if fromTree == "" || toTree == "" {
		return nil, ErrTreeMissing
	}
	if fromTree == toTree {
		return nil, nil
	}
	rawOut, err := r.git(ctx, nil, "diff-tree", "-r", "-z", "--no-renames", "--raw", fromTree, toTree)
	if err != nil {
		return nil, err
	}
	byPath, order, err := ParseRawDiff(rawOut)
	if err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}
	if len(order) == 0 {
		return nil, nil
	}
	numOut, err := r.git(ctx, nil, "diff-tree", "-r", "-z", "--no-renames", "--numstat", fromTree, toTree)
	if err != nil {
		return nil, err
	}
	if err := ParseNumstat(numOut, byPath); err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}
	out := make([]Change, 0, len(order))
	for _, path := range order {
		out = append(out, byPath[path])
	}
	return out, nil
}

// FileContent reads the bytes of relPath as recorded in tree. When maxBytes is
// positive and the blob exceeds it, FileContent returns (nil, true, nil)
// without reading the blob.
func (r *Repo) FileContent(ctx context.Context, tree, relPath string, maxBytes int64) (content []byte, tooLarge bool, err error) {
	clean := filepath.ToSlash(filepath.Clean(relPath))
	if tree == "" || clean == "." || clean == "" || strings.HasPrefix(clean, "../") {
		return nil, false, ErrTreeMissing
	}
	spec := tree + ":" + clean
	if maxBytes > 0 {
		sizeOut, sizeErr := r.git(ctx, nil, "cat-file", "-s", spec)
		if sizeErr != nil {
			return nil, false, sizeErr
		}
		size, parseErr := strconv.ParseInt(strings.TrimSpace(string(sizeOut)), 10, 64)
		if parseErr != nil {
			return nil, false, fmt.Errorf("snapshot: parse blob size for %s: %w", clean, parseErr)
		}
		if size > maxBytes {
			return nil, true, nil
		}
	}
	raw, err := r.git(ctx, nil, "cat-file", "blob", spec)
	if err != nil {
		return nil, false, err
	}
	return raw, false, nil
}

// ParseRawDiff parses the NUL-delimited output of `git diff-tree -r -z --no-renames --raw`.
func ParseRawDiff(raw []byte) (changes map[string]Change, order []string, err error) {
	changes = make(map[string]Change)
	parts := splitNUL(raw)
	for i := 0; i+1 < len(parts); i += 2 {
		header := strings.TrimPrefix(parts[i], ":")
		path := parts[i+1]
		fields := strings.Fields(header)
		if len(fields) < 5 {
			return nil, nil, fmt.Errorf("%w: raw header %q", ErrDiffParse, parts[i])
		}
		oldMode, newMode, status := fields[0], fields[1], fields[4]
		kind, mode := classifyRawStatus(status, oldMode, newMode)
		changes[path] = Change{
			Path:    path,
			Kind:    kind,
			Mode:    mode,
			Added:   -1,
			Removed: -1,
		}
		order = append(order, path)
	}
	return changes, order, nil
}

func classifyRawStatus(status, oldMode, newMode string) (ChangeKind, string) {
	if status != "" {
		switch status[0] {
		case 'A':
			return ChangeAdded, newMode
		case 'D':
			return ChangeDeleted, oldMode
		}
	}
	return ChangeModified, newMode
}

// ParseNumstat parses the NUL-delimited output of `git diff-tree -r -z --no-renames --numstat`
// and writes the line counts into changes.
func ParseNumstat(raw []byte, changes map[string]Change) error {
	for _, record := range splitNUL(raw) {
		parts := strings.SplitN(record, "\t", 3)
		if len(parts) != 3 {
			return fmt.Errorf("%w: numstat record %q", ErrDiffParse, record)
		}
		addedStr, removedStr, path := parts[0], parts[1], parts[2]
		change, ok := changes[path]
		if !ok || addedStr == "-" || removedStr == "-" {
			continue
		}
		added, err := strconv.Atoi(addedStr)
		if err != nil {
			return fmt.Errorf("parse added count %q for %s: %w", addedStr, path, err)
		}
		removed, err := strconv.Atoi(removedStr)
		if err != nil {
			return fmt.Errorf("parse removed count %q for %s: %w", removedStr, path, err)
		}
		change.Added = added
		change.Removed = removed
		changes[path] = change
	}
	return nil
}

// Porcelain parses `git status --porcelain=v1 -z -uall` output into a map of
// path -> single-letter status ("M", "A", "D", "R", "U"). When prefix is
// non-empty, only entries under prefix are kept and the prefix is stripped.
func Porcelain(out []byte, prefix string) map[string]string {
	result := make(map[string]string)
	entries := strings.Split(string(out), "\x00")
	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		if len(entry) < 4 {
			continue
		}
		xy := entry[:2]
		rel := entry[3:]
		if xy[0] == 'R' || xy[0] == 'C' {
			i++
		}
		if prefix != "" {
			after, ok := strings.CutPrefix(rel, prefix)
			if !ok {
				continue
			}
			rel = after
		}
		if code := porcelainCode(xy); code != "" {
			result[rel] = code
		}
	}
	return result
}

func porcelainCode(xy string) string {
	switch {
	case xy == "??":
		return "A"
	case strings.Contains(xy, "U") || xy == "AA" || xy == "DD":
		return "U"
	case strings.ContainsRune(xy, 'R'):
		return "R"
	case strings.ContainsRune(xy, 'A'):
		return "A"
	case strings.ContainsRune(xy, 'D'):
		return "D"
	case strings.ContainsAny(xy, "MT"):
		return "M"
	default:
		return ""
	}
}
