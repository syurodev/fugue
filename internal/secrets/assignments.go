package secrets

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

func isASCII(s string) bool {
	for i := range len(s) {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func isASCIIBoundary(name string, i int) bool {
	b := name[i]
	if b < 'A' || b > 'Z' || i == 0 {
		return false
	}
	prev := name[i-1]
	if (prev >= 'a' && prev <= 'z') || (prev >= '0' && prev <= '9') {
		return true
	}
	return i+1 < len(name) && name[i+1] >= 'a' && name[i+1] <= 'z' && prev >= 'A' && prev <= 'Z'
}

func normalizeASCII(name string) string {
	var out strings.Builder
	for i := range len(name) {
		b := name[i]
		boundary := isASCIIBoundary(name, i)
		lower := b >= 'a' && b <= 'z'
		if out.Len() == 0 && !boundary && !lower {
			continue
		}
		if out.Len() == 0 {
			out.Grow(len(name) + 4)
			out.WriteString(name[:i])
		}
		if boundary {
			out.WriteByte('_')
		}
		if lower {
			out.WriteByte(b - ('a' - 'A'))
		} else {
			out.WriteByte(b)
		}
	}
	if out.Len() == 0 {
		return name
	}
	return out.String()
}

func normalize(name string) string {
	if isASCII(name) {
		return normalizeASCII(name)
	}
	runes := []rune(name)
	var out strings.Builder
	out.Grow(len(name) + 4)
	for i, r := range runes {
		if unicode.IsUpper(r) && i > 0 && (unicode.IsLower(runes[i-1]) || unicode.IsDigit(runes[i-1]) || (i+1 < len(runes) && unicode.IsLower(runes[i+1]) && unicode.IsUpper(runes[i-1]))) {
			out.WriteByte('_')
		}
		out.WriteRune(unicode.ToUpper(r))
	}
	return out.String()
}

func isSensitiveNormalized(name string) bool {
	for _, part := range []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL", "PRIVATE"} {
		if strings.Contains(name, part) {
			return true
		}
	}
	return strings.HasSuffix(name, "_KEY") || strings.HasPrefix(name, "AWS_")
}

func sensitiveName(name string) bool {
	return isSensitiveNormalized(normalize(name))
}

func (d *Detector) findAssignments(text string) (findings, allowed []Finding) {
	offset := 0
	for line := range strings.SplitSeq(text, "\n") {
		key, start, end := assignment(line)
		if start < end {
			f := Finding{Start: offset + start, End: offset + end, Kind: KindAssigned, Key: key}
			normKey := normalize(key)
			if d.allowKeys[normKey] {
				allowed = append(allowed, f)
			} else if isSensitiveNormalized(normKey) && !skipValue(line[start:end]) {
				findings = append(findings, f)
			}
		}
		offset += len(line) + 1
	}
	return findings, allowed
}

func assignment(line string) (key string, start, end int) {
	if !strings.ContainsAny(line, "=:") {
		return "", 0, 0
	}
	trimmed := strings.TrimLeft(line, " \t\r{")
	offset := len(line) - len(trimmed)
	if strings.HasPrefix(trimmed, "export ") {
		trimmed = strings.TrimLeft(trimmed[7:], " \t")
		offset = len(line) - len(trimmed)
	}
	keyEnd := strings.IndexAny(trimmed, "=:")
	if keyEnd < 0 {
		return "", 0, 0
	}
	key = strings.Trim(strings.TrimSpace(trimmed[:keyEnd]), `"'`)
	if !validKey(key) {
		return "", 0, 0
	}
	value := strings.TrimSpace(trimmed[keyEnd+1:])
	start = offset + keyEnd + 1 + len(trimmed[keyEnd+1:]) - len(strings.TrimLeftFunc(trimmed[keyEnd+1:], unicode.IsSpace))
	if len(value) == 0 {
		return key, start, start
	}
	if value[0] == '"' || value[0] == '\'' {
		return quotedAssignment(key, value, start)
	}
	return key, start, start + len(value)
}

func quotedAssignment(key, value string, start int) (string, int, int) {
	quote := value[0]
	for i := 1; i < len(value); i++ {
		if value[i] == '\\' {
			i++
			continue
		}
		if value[i] == quote {
			tail := strings.TrimSpace(value[i+1:])
			if tail != "" && !strings.HasPrefix(tail, ",") && tail != "}" {
				return "", 0, 0
			}
			return key, start + 1, start + i
		}
	}
	return "", 0, 0
}

func validKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		if r != '_' && !unicode.IsLetter(r) && (i == 0 || !unicode.IsDigit(r)) {
			return false
		}
	}
	return true
}

func isSkipWord(value string) bool {
	switch len(value) {
	case 4:
		return strings.EqualFold(value, "true") || strings.EqualFold(value, "null")
	case 5:
		return strings.EqualFold(value, "dummy") || strings.EqualFold(value, "false")
	case 7:
		return strings.EqualFold(value, "example")
	case 8:
		return strings.EqualFold(value, "changeme")
	case 11:
		return strings.EqualFold(value, "placeholder")
	default:
		return false
	}
}

func skipValue(value string) bool {
	if utf8.RuneCountInString(value) < 4 {
		return true
	}
	if strings.ContainsAny(value, `()[]{}<>$`) || strings.ContainsFunc(value, unicode.IsSpace) {
		return true
	}
	return isSkipWord(value) || repeated(value) || shortNumber(value)
}

func repeated(value string) bool {
	first, _ := utf8.DecodeRuneInString(value)
	for _, r := range value {
		if r != first {
			return false
		}
	}
	return true
}

func shortNumber(value string) bool {
	if len(value) >= 8 {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
