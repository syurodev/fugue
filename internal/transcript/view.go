package transcript

import (
	"strings"
	"unicode/utf8"
)

// Redactor masks protected values before any view is shortened.
type Redactor interface{ Redact(string) string }

// ViewKind selects the display limit for a content value.
type ViewKind uint8

// View limits.
const (
	ViewText ViewKind = iota
	ViewToolOutput
	ViewDiff
)

// FileSummary describes a folded file diff.
type FileSummary struct {
	Header         string
	Added, Removed int
}

// View is a redacted, possibly shortened display value.
type View struct {
	Text      string
	Truncated bool
	Files     []FileSummary
}

// BuildView creates a display value without mutating the stored content.
func BuildView(kind ViewKind, content string, redactor Redactor) View {
	text := strings.ReplaceAll(redactor.Redact(content), "\t", "    ")
	if kind == ViewDiff {
		return diffView(text)
	}

	const limit = 64 * 1024
	if len(text) <= limit {
		return View{Text: text}
	}
	if kind == ViewToolOutput {
		head := utf8Prefix(text, limit/2)
		tail := len(text) - limit/2
		for tail < len(text) && !utf8.RuneStart(text[tail]) {
			tail++
		}
		return View{Text: text[:head] + text[tail:], Truncated: true}
	}
	return View{Text: text[:utf8Prefix(text, limit)], Truncated: true}
}

func utf8Prefix(s string, n int) int {
	for n > 0 && n < len(s) && !utf8.RuneStart(s[n]) {
		n--
	}
	return n
}

func diffView(text string) View {
	lines := strings.Split(text, "\n")
	if len(lines) <= 2000 {
		return View{Text: text}
	}
	var files []FileSummary
	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			files = append(files, FileSummary{Header: line})
			continue
		}
		if len(files) == 0 {
			files = append(files, FileSummary{})
		}
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			files[len(files)-1].Added++
		}
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			files[len(files)-1].Removed++
		}
	}
	return View{Truncated: true, Files: files}
}
