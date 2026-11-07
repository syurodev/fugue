package secrets

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

func normalize(name string) string {
	runes := []rune(name)
	var out strings.Builder
	for i, r := range runes {
		if unicode.IsUpper(r) && i > 0 && (unicode.IsLower(runes[i-1]) || unicode.IsDigit(runes[i-1]) || (i+1 < len(runes) && unicode.IsLower(runes[i+1]) && unicode.IsUpper(runes[i-1]))) {
			out.WriteByte('_')
		}
		out.WriteRune(unicode.ToUpper(r))
	}
	return out.String()
}

func sensitiveName(name string) bool {
	name = normalize(name)
	for _, part := range []string{"TOKEN", "SECRET", "PASSWORD", "PASSWD", "CREDENTIAL", "PRIVATE"} {
		if strings.Contains(name, part) {
			return true
		}
	}
	return strings.HasSuffix(name, "_KEY") || strings.HasPrefix(name, "AWS_")
}

func (d *Detector) findAssignments(text string, candidates []Finding, exceptions bool) {
	offset := 0
	for line := range strings.SplitSeq(text, "\n") {
		key, start, end := assignment(line)
		if start < end && d.includeAssignment(key, line[start:end], exceptions) {
			put(candidates, Finding{Start: offset + start, End: offset + end, Kind: KindAssigned, Key: key})
		}
		offset += len(line) + 1
	}
}

func assignment(line string) (key string, start, end int) {
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
			if tail != "" && tail != "," && tail != "}" && tail != ",}" {
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

func skipValue(value string) bool {
	if utf8.RuneCountInString(value) < 4 {
		return true
	}
	if strings.ContainsAny(value, `()[]{}<>$+-*/%=!&|^?:;\`) || strings.ContainsFunc(value, unicode.IsSpace) {
		return true
	}
	lower := strings.ToLower(value)
	switch lower {
	case "changeme", "example", "placeholder", "dummy", "true", "false", "null":
		return true
	}
	return repeated(value) || shortNumber(value)
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

func (d *Detector) includeAssignment(key, value string, exceptions bool) bool {
	allowed := d.allowKeys[normalize(key)]
	if exceptions {
		return allowed
	}
	return sensitiveName(key) && !allowed && !skipValue(value)
}
