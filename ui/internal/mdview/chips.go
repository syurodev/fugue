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
	i := 0
	for i < len(s) {
		if isChipBoundary(s[i]) {
			start := i
			for i < len(s) && isChipBoundary(s[i]) {
				i++
			}
			appendPlainChip(&out, s[start:i])
			continue
		}
		start := i
		for i < len(s) && !isChipBoundary(s[i]) {
			i++
		}
		tok := s[start:i]
		trimmed, lead, trail := trimChipPunct(tok)
		if lead != "" {
			appendPlainChip(&out, lead)
		}
		if ref, ok := parseFileRef(trimmed); ok {
			out = append(out, ChipRun{Text: trimmed, Kind: ChipFile, File: ref})
		} else if isHashOrID(trimmed) {
			out = append(out, ChipRun{Text: trimmed, Kind: ChipHash})
		} else {
			appendPlainChip(&out, trimmed)
		}
		if trail != "" {
			appendPlainChip(&out, trail)
		}
	}
	return out
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
	s := tok
	for len(s) > 0 && strings.ContainsRune("(\"'<[", rune(s[0])) {
		lead += s[:1]
		s = s[1:]
	}
	for len(s) > 0 && strings.ContainsRune(")\"'>.,;!?]", rune(s[len(s)-1])) {
		trail = s[len(s)-1:] + trail
		s = s[:len(s)-1]
	}
	return s, lead, trail
}

func parseFileRef(tok string) (FileRef, bool) {
	if tok == "" || strings.Contains(tok, "://") || strings.HasPrefix(tok, "mailto:") {
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
	if p == "" || strings.HasPrefix(p, "-") || (strings.Contains(p, "..") && !strings.HasPrefix(p, "../")) {
		return false
	}
	for i := 0; i < len(p); i++ {
		c := p[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '.' || c == '_' || c == '/' || c == '-') {
			return false
		}
	}
	ext := strings.ToLower(filepath.Ext(p))
	hasSlash := strings.ContainsRune(p, '/')
	if !isKnownFileExt(ext) {
		return false
	}
	return hasSlash || hasLine
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
	for _, prefix := range []string{"ses_", "tsk_", "team_", "prop_", "turn_", "item_", "req_", "ws_"} {
		if strings.HasPrefix(tok, prefix) && len(tok) > len(prefix)+2 {
			return isIdentTail(tok[len(prefix):])
		}
	}
	if len(tok) == 36 && tok[8] == '-' && tok[13] == '-' && tok[18] == '-' && tok[23] == '-' {
		return isHexRange(tok[:8]) && isHexRange(tok[9:13]) && isHexRange(tok[14:18]) && isHexRange(tok[19:23]) && isHexRange(tok[24:])
	}
	if len(tok) <= 40 && isHexRange(tok) {
		hasDigit, hasAlpha := false, false
		for i := 0; i < len(tok); i++ {
			if tok[i] >= '0' && tok[i] <= '9' {
				hasDigit = true
			} else if tok[i] >= 'a' && tok[i] <= 'f' {
				hasAlpha = true
			}
		}
		return hasDigit && hasAlpha
	}
	return false
}

func isIdentTail(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func isHexRange(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return len(s) > 0
}
