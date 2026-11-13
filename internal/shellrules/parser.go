package shellrules

import (
	"strings"
)

type rawSegment struct {
	text    string
	pipedTo bool
}

// Split parses a shell command line into individual simple commands across pipelines,
// control operators (&&, ||, ;, &), subshells, and command substitutions.
//
// Split returns (nil, false) if the command cannot be parsed safely (e.g. unclosed
// quotes, heredocs, eval, source/., dynamic command names, sh/bash -c, or resource limits).
func Split(command string) ([]Command, bool) {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return []Command{}, true
	}
	if len(command) > MaxCommandLength {
		return nil, false
	}
	var cmds []Command
	if !parseRecursive(trimmed, 0, &cmds) || len(cmds) > MaxSimpleCommands {
		return nil, false
	}
	return cmds, true
}

func parseRecursive(input string, depth int, out *[]Command) bool {
	if depth > MaxNestingDepth {
		return false
	}
	segments, nested, ok := splitSegments(input)
	if !ok {
		return false
	}
	for _, seg := range segments {
		cmd, hasCmd, unparsable := parseSimpleCommand(seg.text, seg.pipedTo)
		if unparsable {
			return false
		}
		if hasCmd {
			*out = append(*out, cmd)
			if len(*out) > MaxSimpleCommands {
				return false
			}
		}
	}
	for _, nest := range nested {
		trimmed := strings.TrimSpace(nest)
		if trimmed != "" && !parseRecursive(trimmed, depth+1, out) {
			return false
		}
	}
	return true
}

func splitSegments(s string) ([]rawSegment, []string, bool) {
	var segments []rawSegment
	var nested []string
	start, i, n := 0, 0, len(s)

	for i < n {
		if s[i] == '<' && i+1 < n && s[i+1] == '<' {
			return nil, nil, false // heredoc
		}
		if newI, sub, ok, handled := scanQuoteOrGrouping(s, i); handled {
			if !ok {
				return nil, nil, false
			}
			if len(sub) > 0 {
				nested = append(nested, sub...)
			}
			i = newI
			continue
		}
		if width, piped, handled := scanDelimiter(s, i); handled {
			segments = appendNonEmptySegment(segments, s[start:i], piped)
			i += width
			start = i
			continue
		}
		i++
	}

	segments = appendNonEmptySegment(segments, s[start:n], false)
	return segments, nested, true
}

func scanQuoteOrGrouping(s string, i int) (int, []string, bool, bool) {
	switch s[i] {
	case '\\':
		return i + 2, nil, true, true
	case '\'':
		end := strings.IndexByte(s[i+1:], '\'')
		if end < 0 {
			return 0, nil, false, true
		}
		return i + 2 + end, nil, true, true
	case '"':
		end, subs, ok := scanDoubleQuote(s, i)
		return end, subs, ok, true
	case '`':
		end, ok := scanBacktick(s, i)
		if !ok {
			return 0, nil, false, true
		}
		return end, []string{s[i+1 : end-1]}, true, true
	case '$':
		return scanDollarParen(s, i)
	case '(', '{':
		return scanSubshellOrBrace(s, i)
	default:
		return i, nil, false, false
	}
}

func scanDollarParen(s string, i int) (int, []string, bool, bool) {
	if i+1 < len(s) && s[i+1] == '(' {
		end, ok := scanMatchingDelimiter(s, i+1, '(', ')')
		if !ok {
			return 0, nil, false, true
		}
		return end, []string{s[i+2 : end-1]}, true, true
	}
	return i, nil, false, false
}

func scanSubshellOrBrace(s string, i int) (int, []string, bool, bool) {
	closeByte := byte(')')
	if s[i] == '{' {
		closeByte = '}'
	}
	end, ok := scanMatchingDelimiter(s, i, s[i], closeByte)
	if !ok {
		return 0, nil, false, true
	}
	return end, []string{s[i+1 : end-1]}, true, true
}

func scanDelimiter(s string, i int) (int, bool, bool) {
	switch s[i] {
	case '|':
		return scanPipe(s, i)
	case '&':
		return scanAmp(s, i)
	case ';', '\n':
		return 1, false, true
	default:
		return 0, false, false
	}
}

func scanPipe(s string, i int) (int, bool, bool) {
	if i+1 < len(s) {
		switch s[i+1] {
		case '&':
			return 2, true, true
		case '|':
			return 2, false, true
		}
	}
	return 1, true, true
}

func scanAmp(s string, i int) (int, bool, bool) {
	if i+1 < len(s) && s[i+1] == '&' {
		return 2, false, true
	}
	if i > 0 && (s[i-1] == '>' || s[i-1] == '<') {
		return 0, false, false
	}
	return 1, false, true
}

func appendNonEmptySegment(segs []rawSegment, text string, piped bool) []rawSegment {
	trimmed := strings.TrimSpace(text)
	if trimmed != "" {
		return append(segs, rawSegment{text: trimmed, pipedTo: piped})
	}
	return segs
}

func scanDoubleQuote(s string, start int) (int, []string, bool) {
	var subs []string
	i, n := start+1, len(s)
	for i < n {
		switch s[i] {
		case '\\':
			i += 2
		case '"':
			return i + 1, subs, true
		case '`':
			end, ok := scanBacktick(s, i)
			if !ok {
				return 0, nil, false
			}
			subs = append(subs, s[i+1:end-1])
			i = end
		case '$':
			if i+1 < n && s[i+1] == '(' {
				end, ok := scanMatchingDelimiter(s, i+1, '(', ')')
				if !ok {
					return 0, nil, false
				}
				subs = append(subs, s[i+2:end-1])
				i = end
				continue
			}
			i++
		default:
			i++
		}
	}
	return 0, nil, false
}

func scanBacktick(s string, start int) (int, bool) {
	i, n := start+1, len(s)
	for i < n {
		if s[i] == '\\' {
			i += 2
			continue
		}
		if s[i] == '`' {
			return i + 1, true
		}
		i++
	}
	return 0, false
}

func skipDelimiterInner(s string, i int) (int, bool, bool) {
	switch s[i] {
	case '\\':
		return i + 2, true, true
	case '\'':
		end := strings.IndexByte(s[i+1:], '\'')
		if end < 0 {
			return 0, false, true
		}
		return i + 2 + end, true, true
	case '"':
		end, _, ok := scanDoubleQuote(s, i)
		return end, ok, true
	case '`':
		end, ok := scanBacktick(s, i)
		return end, ok, true
	default:
		return i, false, false
	}
}

func scanMatchingDelimiter(s string, start int, open, close byte) (int, bool) {
	depth, i, n := 0, start, len(s)
	for i < n {
		if newI, ok, handled := skipDelimiterInner(s, i); handled {
			if !ok {
				return 0, false
			}
			i = newI
			continue
		}
		switch s[i] {
		case open:
			depth++
			if depth > MaxNestingDepth {
				return 0, false
			}
		case close:
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
		i++
	}
	return 0, false
}
