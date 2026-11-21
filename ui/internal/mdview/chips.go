package mdview

import (
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/syurodev/fugue/ui/internal/kit"
)

// ChipKind identifies an inline chip detected while drawing (INV-09: never stored).
type ChipKind uint8

const (
	// ChipNone is plain text.
	ChipNone ChipKind = iota
	// ChipFile is a file reference (with optional :line or :start-end).
	ChipFile
	// ChipHash is a commit hash or Fugue ID rendered in mono with a dotted underline.
	ChipHash
)

// FileRef is a parsed file reference chip target.
type FileRef struct {
	Path    string
	Line    int
	EndLine int
	Raw     string
}

// ChipRun is one contiguous slice of text with its detected chip kind.
type ChipRun struct {
	Text string
	Kind ChipKind
	File FileRef
}

// ParseChips scans s on the fly during rendering and splits out file reference
// and commit-hash/ID chips while preserving every byte of s in order.
func ParseChips(s string) []ChipRun {
	if s == "" {
		return nil
	}
	var out []ChipRun
	for i := 0; i < len(s); {
		if isChipBoundary(s[i]) {
			start := i
			for i < len(s) && isChipBoundary(s[i]) {
				i++
			}
			appendPlainChip(&out, s[start:i])
			continue
		}
		i = scanChipToken(s, i, &out)
	}
	return out
}

func scanChipToken(s string, start int, out *[]ChipRun) int {
	i := start
	for i < len(s) && !isChipBoundary(s[i]) {
		i++
	}
	trimmed, lead, trail := trimChipPunct(s[start:i])
	appendPlainChip(out, lead)
	switch {
	case trimmed == "":
	case hasFileRefPrefix(trimmed):
		appendPlainChip(out, trimmed)
	default:
		classifyChipToken(trimmed, out)
	}
	appendPlainChip(out, trail)
	return i
}

func hasFileRefPrefix(tok string) bool {
	return strings.Contains(tok, "://") || strings.HasPrefix(tok, "mailto:")
}

func classifyChipToken(tok string, out *[]ChipRun) {
	if ref, ok := parseFileRef(tok); ok {
		*out = append(*out, ChipRun{Text: tok, Kind: ChipFile, File: ref})
		return
	}
	if isHashOrID(tok) {
		*out = append(*out, ChipRun{Text: tok, Kind: ChipHash})
		return
	}
	appendPlainChip(out, tok)
}

// FileIcon returns the Lucide icon for a file path based on its extension.
func FileIcon(path string) kit.IconName {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".mdx", ".txt", ".log", ".csv":
		return kit.IconFileText
	default:
		return kit.IconFileCode
	}
}

func appendPlainChip(out *[]ChipRun, txt string) {
	if txt == "" {
		return
	}
	if n := len(*out); n > 0 && (*out)[n-1].Kind == ChipNone {
		(*out)[n-1].Text += txt
		return
	}
	*out = append(*out, ChipRun{Text: txt, Kind: ChipNone})
}

func isChipBoundary(b byte) bool {
	return unicode.IsSpace(rune(b))
}

func trimChipPunct(tok string) (core, lead, trail string) {
	start := 0
	for start < len(tok) && strings.ContainsRune("(\"'<[", rune(tok[start])) {
		start++
	}
	end := len(tok)
	for end > start && strings.ContainsRune(")\"'>.,;!?]", rune(tok[end-1])) {
		end--
	}
	return tok[start:end], tok[:start], tok[end:]
}

func parseFileRef(tok string) (FileRef, bool) {
	if tok == "" || hasFileRefPrefix(tok) {
		return FileRef{}, false
	}
	pathPart := tok
	line, endLine := 0, 0
	if colon := strings.LastIndexByte(tok, ':'); colon > 0 {
		l, e, ok := parseLineRange(tok[colon+1:])
		if !ok {
			return FileRef{}, false
		}
		pathPart, line, endLine = tok[:colon], l, e
	}
	if !looksLikeFilePath(pathPart, line > 0) {
		return FileRef{}, false
	}
	return FileRef{Path: pathPart, Line: line, EndLine: endLine, Raw: tok}, true
}

func parseLineRange(s string) (line, endLine int, ok bool) {
	if s == "" {
		return 0, 0, false
	}
	if dash := strings.IndexByte(s, '-'); dash > 0 {
		a, errA := strconv.Atoi(s[:dash])
		b, errB := strconv.Atoi(s[dash+1:])
		if errA != nil || errB != nil || a <= 0 || b < a {
			return 0, 0, false
		}
		return a, b, true
	}
	a, err := strconv.Atoi(s)
	if err != nil || a <= 0 {
		return 0, 0, false
	}
	return a, 0, true
}

func looksLikeFilePath(p string, hasLine bool) bool {
	if !hasValidPathShape(p) {
		return false
	}
	if !isKnownFileExt(strings.ToLower(filepath.Ext(p))) {
		return false
	}
	return hasLine || strings.ContainsRune(p, '/')
}

func hasValidPathShape(p string) bool {
	if p == "" || strings.HasPrefix(p, "-") {
		return false
	}
	if strings.Contains(p, "..") && !strings.HasPrefix(p, "../") {
		return false
	}
	for i := range len(p) {
		if !isPathByte(p[i]) {
			return false
		}
	}
	return true
}

func isPathByte(c byte) bool {
	return isASCIIAlnum(c) || c == '.' || c == '_' || c == '/' || c == '-'
}

func isASCIIAlnum(c byte) bool {
	if c >= 'a' && c <= 'z' {
		return true
	}
	if c >= 'A' && c <= 'Z' {
		return true
	}
	return c >= '0' && c <= '9'
}

func isKnownFileExt(ext string) bool {
	switch ext {
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".py", ".rs", ".md", ".mdx",
		".json", ".yaml", ".yml", ".toml", ".sql", ".proto", ".sh", ".css",
		".html", ".txt", ".c", ".h", ".cpp", ".java", ".rb", ".mod", ".sum", ".log":
		return true
	default:
		return false
	}
}

func isHashOrID(tok string) bool {
	if len(tok) < 7 || len(tok) > 64 {
		return false
	}
	if hasPrefixedID(tok) || isUUID(tok) {
		return true
	}
	return len(tok) <= 40 && isMixedHex(tok)
}

func hasPrefixedID(tok string) bool {
	for _, prefix := range []string{"ses_", "tsk_", "team_", "prop_", "turn_", "item_", "req_", "ws_"} {
		if after, ok := strings.CutPrefix(tok, prefix); ok && len(after) > 2 {
			return isIdentTail(after)
		}
	}
	return false
}

func isUUID(tok string) bool {
	if len(tok) != 36 || tok[8] != '-' || tok[13] != '-' || tok[18] != '-' || tok[23] != '-' {
		return false
	}
	return isHexRange(tok[:8]) && isHexRange(tok[9:13]) && isHexRange(tok[14:18]) &&
		isHexRange(tok[19:23]) && isHexRange(tok[24:])
}

func isMixedHex(tok string) bool {
	if !isHexRange(tok) {
		return false
	}
	hasDigit, hasAlpha := false, false
	for i := range len(tok) {
		if tok[i] >= '0' && tok[i] <= '9' {
			hasDigit = true
		} else {
			hasAlpha = true
		}
	}
	return hasDigit && hasAlpha
}

func isIdentTail(s string) bool {
	for i := range len(s) {
		c := s[i]
		if !isASCIIAlnum(c) && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

func isHexRange(s string) bool {
	for i := range len(s) {
		c := s[i]
		isDigit := c >= '0' && c <= '9'
		isHexAlpha := c >= 'a' && c <= 'f'
		if !isDigit && !isHexAlpha {
			return false
		}
	}
	return len(s) > 0
}
