package mdview

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/syurodev/fugue/ui/internal/markdown"
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
	Spans  []markdown.Span
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

// HighlightDiff applies theme syntax roles (via markdown.Highlight) to diff
// content lines using the language inferred from title, marking metadata rows
// with RoleMeta.
func HighlightDiff(title string, lines []DiffLine, clock markdown.Clock) {
	if len(lines) == 0 {
		return
	}
	if lang := langFromPath(title); lang != "" && clock != nil {
		applyDiffSyntax(lang, lines, clock)
	}
	for i := range lines {
		if lines[i].Kind == DiffMeta && len(lines[i].Spans) == 0 && lines[i].Text != "" {
			lines[i].Spans = []markdown.Span{{Text: lines[i].Text, Role: markdown.RoleMeta}}
		}
	}
}

func applyDiffSyntax(lang string, lines []DiffLine, clock markdown.Clock) {
	body := make([]string, len(lines))
	for i := range lines {
		if lines[i].Kind != DiffMeta {
			body[i] = lines[i].Text
		}
	}
	hl, ok := markdown.Highlight(lang, strings.Join(body, "\n"), clock)
	if !ok || len(hl) != len(lines) {
		return
	}
	for i := range lines {
		if lines[i].Kind != DiffMeta {
			lines[i].Spans = hl[i]
		}
	}
}

const langMarkdown = "markdown"

func extLangMap() map[string]string {
	return map[string]string{
		".go": "go", ".ts": "typescript", ".tsx": "typescript",
		".js": "javascript", ".jsx": "javascript", ".py": "python",
		".rs": "rust", ".json": "json", ".yaml": "yaml", ".yml": "yaml",
		".toml": "toml", ".sql": "sql", ".proto": "protobuf",
		".sh": "bash", ".zsh": "bash", ".md": langMarkdown, ".mdx": langMarkdown,
		".css": "css", ".html": "html",
	}
}

func langFromPath(path string) string {
	if lang := extLangMap()[strings.ToLower(filepath.Ext(strings.TrimSpace(path)))]; lang != "" {
		return lang
	}
	return "diff"
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
