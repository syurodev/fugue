package ocr

import (
	"strconv"
	"strings"
)

// Severity levels in the reviewer output schema (§10.1).
const (
	SeverityLow    = "low"
	SeverityMedium = "medium"
	SeverityHigh   = "high"
)

// Finding is one untrusted code-review finding returned by a reviewer session (§10.1).
type Finding struct {
	File        string `json:"file"`
	LineStart   int    `json:"line_start"`
	LineEnd     int    `json:"line_end"`
	Severity    string `json:"severity"`
	Rule        string `json:"rule"`
	Description string `json:"description"`
}

// LineRange is an inclusive 1-based line interval in a diff's new file.
type LineRange struct {
	Start int
	End   int
}

// VerifyResult holds accepted findings and drop counts after OR-13 / §10.1 filtering.
type VerifyResult struct {
	Accepted             []Finding `json:"accepted,omitempty"`
	DroppedLowCount      int       `json:"dropped_low_count,omitempty"`
	DroppedPositionCount int       `json:"dropped_position_count,omitempty"`
}

// ParseDiffLineRanges parses a unified diff into per-file 1-based line ranges
// covering added/modified lines in the new file.
func ParseDiffLineRanges(unifiedDiff string) map[string][]LineRange {
	out := make(map[string][]LineRange)
	var curFile string
	newLine := 0
	inHunk := false

	for _, rawLine := range strings.Split(unifiedDiff, "\n") {
		line := strings.TrimSuffix(rawLine, "\r")
		if nextFile, handled := parseDiffHeader(line, &inHunk, &newLine); handled {
			if nextFile != "" {
				curFile = nextFile
			}
			continue
		}
		if !inHunk || curFile == "" {
			continue
		}
		if strings.HasPrefix(line, "+") {
			out[curFile] = appendLine(out[curFile], newLine)
			newLine++
		} else if strings.HasPrefix(line, " ") || line == "" {
			newLine++
		}
	}
	return out
}

func parseDiffHeader(line string, inHunk *bool, newLine *int) (string, bool) {
	switch {
	case strings.HasPrefix(line, "+++ "):
		*inHunk = false
		return parseDiffPath(strings.TrimPrefix(line, "+++ ")), true
	case strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "diff --git "):
		*inHunk = false
		return "", true
	case strings.HasPrefix(line, "@@ "):
		if start, ok := parseHunkNewStart(line); ok {
			*newLine = start
			*inHunk = true
		} else {
			*inHunk = false
		}
		return "", true
	default:
		return "", false
	}
}

func parseDiffPath(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "/dev/null" {
		return ""
	}
	trimmed = strings.TrimPrefix(trimmed, "b/")
	return strings.TrimPrefix(trimmed, "./")
}

func parseHunkNewStart(header string) (int, bool) {
	plusIdx := strings.Index(header, "+")
	if plusIdx < 0 {
		return 0, false
	}
	rest := header[plusIdx+1:]
	endIdx := strings.IndexAny(rest, " ,@")
	if endIdx < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(rest[:endIdx])
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func appendLine(ranges []LineRange, line int) []LineRange {
	if n := len(ranges); n > 0 && ranges[n-1].End+1 == line {
		ranges[n-1].End = line
		return ranges
	}
	return append(ranges, LineRange{Start: line, End: line})
}

// VerifyFindings drops low-severity findings and findings whose file or line
// range does not touch any changed line in diffRanges (OR-13, §10.1).
func VerifyFindings(findings []Finding, diffRanges map[string][]LineRange) VerifyResult {
	var res VerifyResult
	for _, f := range findings {
		sev := strings.ToLower(strings.TrimSpace(f.Severity))
		if sev == SeverityLow {
			res.DroppedLowCount++
			continue
		}
		if sev != SeverityMedium && sev != SeverityHigh {
			res.DroppedPositionCount++
			continue
		}
		normFile := strings.TrimPrefix(strings.TrimSpace(f.File), "./")
		ranges, ok := diffRanges[normFile]
		if !ok || !touchesAnyRange(f.LineStart, f.LineEnd, ranges) {
			res.DroppedPositionCount++
			continue
		}
		f.File = normFile
		f.Severity = sev
		res.Accepted = append(res.Accepted, f)
	}
	return res
}

func touchesAnyRange(start, end int, ranges []LineRange) bool {
	if start <= 0 || end < start {
		return false
	}
	for _, r := range ranges {
		if start <= r.End && end >= r.Start {
			return true
		}
	}
	return false
}
