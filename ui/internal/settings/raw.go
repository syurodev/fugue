package settings

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/syurodev/fugue/pkg/client"
)

// RawDirty reports whether RawDraft differs from the loaded workspace TOML.
func (m *Model) RawDirty() bool {
	return m.Loaded && m.RawDraft != string(m.RawTOML)
}

// ApplyRawEdit records an edit in the raw TOML editor, advancing RawRevision
// so stale validation responses can be discarded.
func (m *Model) ApplyRawEdit(next string) (changed bool, rev uint64) {
	if next == m.RawDraft {
		return false, m.RawRevision
	}
	m.RawDraft = next
	m.RawRevision++
	m.Validating = true
	m.ValidationErr = ""
	m.PolicyConfirmArmed = false
	m.PendingRawPolicyDiff = nil
	return true, m.RawRevision
}

// ApplyValidationResult updates ValidationIssues when workspaceID and rev still
// match the active raw editor state.
func (m *Model) ApplyValidationResult(workspaceID string, rev uint64, issues []client.WorkspaceConfigIssue, err error) {
	if workspaceID != m.WorkspaceID || rev != m.RawRevision {
		return
	}
	m.Validating = false
	if err != nil {
		m.ValidationErr = err.Error()
		return
	}
	m.ValidationErr = ""
	m.ValidationIssues = slices.Clone(issues)
}

// DiscardRawDraft resets RawDraft to the host's RawTOML and clears conflict,
// validation, and policy-confirm state.
func (m *Model) DiscardRawDraft() {
	m.RawDraft = string(m.RawTOML)
	m.RawRevision++
	m.RawConflict = false
	m.ConflictHash = ""
	m.ShowConflictDiff = false
	m.Validating = false
	m.ValidationIssues = nil
	m.ValidationErr = ""
	m.PolicyConfirmArmed = false
	m.PendingRawPolicyDiff = nil
	m.SaveErr = ""
	m.ChangedUnderneath = nil
}

// IssueLines returns the deduplicated sorted 1-based line numbers from
// ValidationIssues for gutter/line markers.
func (m *Model) IssueLines() []int {
	if len(m.ValidationIssues) == 0 {
		return nil
	}
	seen := make(map[int]bool, len(m.ValidationIssues))
	out := make([]int, 0, len(m.ValidationIssues))
	for _, iss := range m.ValidationIssues {
		if iss.Line < 1 {
			continue
		}
		ln := int(iss.Line)
		if !seen[ln] {
			seen[ln] = true
			out = append(out, ln)
		}
	}
	slices.Sort(out)
	return out
}

// RawPolicyDiff returns the policy diff entries introduced by RawDraft relative
// to RawTOML. Parse errors return nil, err so callers can fall through to host
// validation without failing open.
func (m *Model) RawPolicyDiff() ([]client.PolicyDiffEntry, error) {
	diffs, err := client.DiffRawTOMLPolicy(m.RawTOML, []byte(m.RawDraft))
	if err != nil {
		return nil, fmt.Errorf("diff raw TOML policy: %w", err)
	}
	return diffs, nil
}

// LineColToRuneOffset converts 1-based line and column coordinates in text to
// a 0-based rune offset suitable for kit.TextAreaState.SetCaret.
func LineColToRuneOffset(text string, line, col int64) int {
	if line <= 1 && col <= 1 {
		return 0
	}
	lineStart, ok := lineStartRuneOffset(text, max(int(line), 1))
	if !ok {
		return utf8.RuneCountInString(text)
	}
	return lineStart + colOffsetInLine(text, lineStart, max(int(col), 1))
}

func lineStartRuneOffset(text string, targetLine int) (int, bool) {
	if targetLine <= 1 {
		return 0, true
	}
	curLine := 1
	runeIdx := 0
	for _, r := range text {
		if r == '\n' {
			curLine++
			if curLine == targetLine {
				return runeIdx + 1, true
			}
		}
		runeIdx++
	}
	return 0, false
}

func colOffsetInLine(text string, lineStart, targetCol int) int {
	colOffset := 0
	pos := 0
	for _, r := range text {
		if pos < lineStart {
			pos++
			continue
		}
		if r == '\n' || colOffset >= targetCol-1 {
			break
		}
		colOffset++
	}
	return colOffset
}

// BuildConflictUnifiedDiff builds a unified diff between the host's current
// RawTOML and the user's unsaved RawDraft for rendering in mdview.
func BuildConflictUnifiedDiff(hostTOML, userTOML string) string {
	oldLines := splitDiffLines(hostTOML)
	newLines := splitDiffLines(userTOML)
	var b strings.Builder
	b.WriteString("--- a/fugue.toml (host)\n")
	b.WriteString("+++ b/fugue.toml (yours)\n")
	fmt.Fprintf(&b, "@@ -1,%d +1,%d @@\n", len(oldLines), len(newLines))
	for _, line := range oldLines {
		b.WriteByte('-')
		b.WriteString(line)
		b.WriteByte('\n')
	}
	for _, line := range newLines {
		b.WriteByte('+')
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func splitDiffLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
