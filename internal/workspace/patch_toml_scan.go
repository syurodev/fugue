package workspace

import (
	"slices"
	"strings"
)

type parsedTOMLLayout struct {
	matchStart, matchEnd int
	matchedLHS           string
	trailingComment      string
	sectionInsertIdx     int
	firstSectionIdx      int
}

func scanTOMLLayout(lines []string, targetKey, targetSection string) parsedTOMLLayout {
	layout := parsedTOMLLayout{matchStart: -1, matchEnd: -1, sectionInsertIdx: -1, firstSectionIdx: -1}
	curSection := ""
	inTargetSection := targetSection == ""
	lastContentInTarget := -1
	if targetSection == "" {
		lastContentInTarget = 0
	}
	for i := 0; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if hdr, isArray, ok := parseSectionHeader(trimmed); ok {
			if layout.firstSectionIdx < 0 {
				layout.firstSectionIdx = i
			}
			if inTargetSection {
				layout.sectionInsertIdx = lastContentInTarget
			}
			if isArray {
				curSection = "array:" + hdr
				inTargetSection = false
			} else {
				curSection = hdr
				inTargetSection = curSection == targetSection
				if inTargetSection {
					lastContentInTarget = i + 1
				}
			}
			continue
		}
		lhs, rhs, ok := splitKeyAssign(lines[i])
		if !ok {
			if inTargetSection {
				lastContentInTarget = i + 1
			}
			continue
		}
		endIdx, trailing := findAssignEnd(lines, i, rhs)
		if inTargetSection {
			lastContentInTarget = endIdx + 1
		}
		if !strings.HasPrefix(curSection, "array:") && qualifyKey(curSection, lhs) == targetKey {
			layout.matchStart, layout.matchEnd = i, endIdx
			layout.matchedLHS = lhs
			layout.trailingComment = trailing
		}
		i = endIdx
	}
	if inTargetSection && layout.sectionInsertIdx < 0 {
		layout.sectionInsertIdx = lastContentInTarget
	}
	return layout
}

func applySinglePatch(lines []string, dottedKey, section, key string, op ConfigPatchOp, encoded string) []string {
	layout := scanTOMLLayout(lines, dottedKey, section)
	if layout.matchStart >= 0 {
		if op == PatchOpDelete {
			return append(lines[:layout.matchStart], lines[layout.matchEnd+1:]...)
		}
		indent := leadingIndent(lines[layout.matchStart])
		replacement := indent + layout.matchedLHS + " = " + encoded + layout.trailingComment
		out := make([]string, 0, len(lines)-(layout.matchEnd-layout.matchStart))
		out = append(out, lines[:layout.matchStart]...)
		out = append(out, replacement)
		out = append(out, lines[layout.matchEnd+1:]...)
		return out
	}
	if op == PatchOpDelete {
		return lines
	}
	newLine := key + " = " + encoded
	if layout.sectionInsertIdx >= 0 {
		return slices.Insert(lines, layout.sectionInsertIdx, newLine)
	}
	if section == "" {
		if layout.firstSectionIdx >= 0 {
			return slices.Insert(lines, layout.firstSectionIdx, newLine)
		}
		return append(lines, newLine)
	}
	if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
		lines = append(lines, "")
	}
	return append(lines, "["+section+"]", newLine)
}

func parseSectionHeader(trimmed string) (name string, isArray, ok bool) {
	content := stripInlineComment(trimmed)
	content = strings.TrimSpace(content)
	if strings.HasPrefix(content, "[[") && strings.HasSuffix(content, "]]") {
		return normalizeSectionName(content[2 : len(content)-2]), true, true
	}
	if strings.HasPrefix(content, "[") && strings.HasSuffix(content, "]") {
		return normalizeSectionName(content[1 : len(content)-1]), false, true
	}
	return "", false, false
}

func normalizeSectionName(raw string) string {
	return normalizeDottedPath(raw)
}

func normalizeDottedPath(raw string) string {
	parts := strings.Split(raw, ".")
	for i := range parts {
		parts[i] = unquoteTOMLSegment(parts[i])
	}
	return strings.Join(parts, ".")
}

func unquoteTOMLSegment(seg string) string {
	s := strings.TrimSpace(seg)
	if len(s) >= 2 && ((s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'')) {
		return s[1 : len(s)-1]
	}
	return s
}

func splitKeyAssign(line string) (lhs, rhs string, ok bool) {
	eq := indexUnquoted(line, '=')
	if eq < 0 {
		return "", "", false
	}
	lhs = strings.TrimSpace(line[:eq])
	if lhs == "" || strings.HasPrefix(lhs, "#") {
		return "", "", false
	}
	return lhs, line[eq+1:], true
}

func qualifyKey(section, lhs string) string {
	normLHS := normalizeDottedPath(lhs)
	if section == "" {
		return normLHS
	}
	return section + "." + normLHS
}

func findAssignEnd(lines []string, start int, firstRHS string) (int, string) {
	depth := bracketDelta(firstRHS)
	end := start
	for depth > 0 && end+1 < len(lines) {
		end++
		depth += bracketDelta(lines[end])
	}
	return end, extractTrailingComment(lines[end])
}

func bracketDelta(s string) int {
	delta := 0
	inQuote := byte(0)
	escaped := false
	for i := range len(s) {
		ch := s[i]
		if escaped {
			escaped = false
			continue
		}
		if inQuote != 0 {
			if ch == '\\' && inQuote == '"' {
				escaped = true
			} else if ch == inQuote {
				inQuote = 0
			}
			continue
		}
		switch ch {
		case '#':
			return delta
		case '"', '\'':
			inQuote = ch
		case '[', '{':
			delta++
		case ']', '}':
			delta--
		}
	}
	return delta
}

func stripInlineComment(s string) string {
	idx := indexUnquoted(s, '#')
	if idx < 0 {
		return s
	}
	return s[:idx]
}

func extractTrailingComment(s string) string {
	idx := indexUnquoted(s, '#')
	if idx < 0 {
		return ""
	}
	trimmed := strings.TrimRight(s[:idx], " \t")
	ws := s[len(trimmed):idx]
	if ws == "" {
		ws = " "
	}
	return ws + s[idx:]
}

func indexUnquoted(s string, target byte) int {
	inQuote := byte(0)
	escaped := false
	for i := range len(s) {
		ch := s[i]
		if escaped {
			escaped = false
			continue
		}
		if inQuote != 0 {
			if ch == '\\' && inQuote == '"' {
				escaped = true
			} else if ch == inQuote {
				inQuote = 0
			}
			continue
		}
		if ch == '"' || ch == '\'' {
			inQuote = ch
			continue
		}
		if ch == target {
			return i
		}
	}
	return -1
}

func leadingIndent(s string) string {
	i := 0
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return s[:i]
}
