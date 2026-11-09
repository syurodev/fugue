package claude

// The file_change diff builder: transcript §4 maps the Edit, Write and
// MultiEdit tools to a file_change item whose diff is computed from the tool
// input. The vendor event does not carry the file content, so the diff lists
// the changed lines under the git-style path headers without hunk positions —
// they would be invented; the line counts and content are real.

import (
	"encoding/json"
	"fmt"
	"strings"
)

// editPair is one old→new replacement of a tool input.
type editPair struct{ Old, New string }

// fileChange builds the change kind, path, diff and line counts from one tool
// input. Write is an add (the whole content arrives); Edit and MultiEdit are
// modifies. An input without a path yields no item.
func fileChange(toolName string, input []byte) (change, path, diff string, added, removed int) {
	var w wireFileInput
	if json.Unmarshal(input, &w) != nil || w.Path == "" {
		return "", "", "", 0, 0
	}
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n", w.Path, w.Path, w.Path, w.Path)
	switch toolName {
	case toolWrite:
		added = writeLines(&b, splitLines(w.Content))
		change = "add"
	default:
		for _, pair := range editPairs(&w) {
			a, r := editLines(&b, pair)
			added += a
			removed += r
		}
		change = "modify"
	}
	return change, w.Path, b.String(), added, removed
}

// editPairs prefers MultiEdit's edits array over the single Edit pair.
func editPairs(w *wireFileInput) []editPair {
	if len(w.Edits) == 0 {
		return []editPair{{Old: w.OldString, New: w.NewString}}
	}
	pairs := make([]editPair, 0, len(w.Edits))
	for _, e := range w.Edits {
		pairs = append(pairs, editPair{Old: e.OldString, New: e.NewString})
	}
	return pairs
}

// writeLines emits the whole new content as added lines.
func writeLines(b *strings.Builder, lines []string) int {
	for _, line := range lines {
		b.WriteString("+")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return len(lines)
}

// editLines emits one replacement as removed then added lines.
func editLines(b *strings.Builder, pair editPair) (added, removed int) {
	for _, line := range splitLines(pair.Old) {
		b.WriteString("-")
		b.WriteString(line)
		b.WriteString("\n")
	}
	for _, line := range splitLines(pair.New) {
		b.WriteString("+")
		b.WriteString(line)
		b.WriteString("\n")
	}
	return len(splitLines(pair.New)), len(splitLines(pair.Old))
}

// splitLines splits without a trailing empty element for a trailing newline,
// so a one-line replacement counts as one line.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
