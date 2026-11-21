package mdview

import (
	"strconv"
	"strings"
)

// DiffKind classifies one line in a unified diff.
type DiffKind uint8

const (
	// DiffContext is an unchanged context line.
	DiffContext DiffKind = iota
	// DiffAdd is an added line (+).
	DiffAdd
	// DiffDel is a removed line (-).
	DiffDel
	// DiffMeta is a diff header or hunk line (diff --git, ---, +++, @@).
	DiffMeta
)

// DiffLine is one parsed line of a diff with its separate marker column and
// line numbers (never included in clipboard copies).
type DiffLine struct {
	Kind   DiffKind
	Marker string
	Text   string
	LineNo int
}

// ParseDiff parses unified diff text into structured lines with a dedicated
// + / − / space marker column and line numbers.
func ParseDiff(src string) []DiffLine {
	src = strings.ReplaceAll(src, "\t", tabSpaces)
	raw := strings.Split(strings.TrimSuffix(src, "\n"), "\n")
	out := make([]DiffLine, 0, len(raw))
	oldNo, newNo := 1, 1
	for _, line := range raw {
		switch {
		case strings.HasPrefix(line, "diff --git "),
			strings.HasPrefix(line, "index "),
			strings.HasPrefix(line, "--- "),
			strings.HasPrefix(line, "+++ "):
			out = append(out, DiffLine{Kind: DiffMeta, Text: line})
		case strings.HasPrefix(line, "@@ "):
			if o, n, ok := parseHunkHeader(line); ok {
				oldNo, newNo = o, n
			}
			out = append(out, DiffLine{Kind: DiffMeta, Text: line})
		case strings.HasPrefix(line, "+"):
			out = append(out, DiffLine{Kind: DiffAdd, Marker: "+", Text: line[1:], LineNo: newNo})
			newNo++
		case strings.HasPrefix(line, "-"):
			out = append(out, DiffLine{Kind: DiffDel, Marker: "−", Text: line[1:], LineNo: oldNo})
			oldNo++
		case strings.HasPrefix(line, " "):
			out = append(out, DiffLine{Kind: DiffContext, Marker: " ", Text: line[1:], LineNo: newNo})
			oldNo++
			newNo++
		default:
			out = append(out, DiffLine{Kind: DiffContext, Marker: " ", Text: line, LineNo: newNo})
			oldNo++
			newNo++
		}
	}
	return out
}

// PlainCopyText returns the clipboard text for a code block or diff, excluding
// line numbers and, for diffs, excluding +/−/space diff markers (desktop-app v0.9 §5.4).
func PlainCopyText(raw string, isDiff bool) string {
	if !isDiff {
		return raw
	}
	lines := ParseDiff(raw)
	body := make([]string, 0, len(lines))
	metaOnly := true
	for _, l := range lines {
		if l.Kind != DiffMeta {
			metaOnly = false
			body = append(body, l.Text)
		}
	}
	if metaOnly {
		return raw
	}
	return strings.Join(body, "\n")
}

func parseHunkHeader(line string) (oldStart, newStart int, ok bool) {
	rest, found := strings.CutPrefix(line, "@@ -")
	if !found {
		return 0, 0, false
	}
	end := strings.Index(rest, " @@")
	if end <= 0 {
		return 0, 0, false
	}
	parts := strings.Split(rest[:end], " +")
	if len(parts) != 2 {
		return 0, 0, false
	}
	o, errO := strconv.Atoi(strings.Split(parts[0], ",")[0])
	n, errN := strconv.Atoi(strings.Split(parts[1], ",")[0])
	if errO != nil || errN != nil {
		return 0, 0, false
	}
	return o, n, true
}
