package shellrules

import (
	"path/filepath"
	"strings"
	"unicode"
)

func flushWord(words []string, cur *strings.Builder, inWord *bool) []string {
	if *inWord {
		words = append(words, cur.String())
		cur.Reset()
		*inWord = false
	}
	return words
}

func tokenizeWords(raw string) ([]string, bool) {
	var words []string
	var cur strings.Builder
	inWord, i, n := false, 0, len(raw)

	for i < n {
		c := raw[i]
		if unicode.IsSpace(rune(c)) {
			words = flushWord(words, &cur, &inWord)
			i++
			continue
		}
		if c == '>' || c == '<' {
			words = flushWord(words, &cur, &inWord)
			nextI, ok := skipRedirection(raw, i)
			if !ok {
				return nil, false
			}
			i = nextI
			continue
		}

		inWord = true
		nextI, ok := readTokenChar(raw, i, c, &cur)
		if !ok {
			return nil, false
		}
		i = nextI
	}
	words = flushWord(words, &cur, &inWord)
	return words, true
}

func readTokenChar(raw string, i int, c byte, cur *strings.Builder) (int, bool) {
	switch c {
	case '\'':
		return readSingleQuote(raw, i, cur)
	case '"':
		return readDoubleQuote(raw, i, cur)
	case '\\':
		return readEscapedByte(raw, i, cur)
	default:
		cur.WriteByte(c)
		return i + 1, true
	}
}

func skipRedirection(raw string, i int) (int, bool) {
	i = skipRedirectionPrefix(raw, i)
	return skipRedirectTarget(raw, i)
}

func skipRedirectionPrefix(raw string, i int) int {
	n := len(raw)
	for i < n {
		c := raw[i]
		if c != '>' && c != '<' && c != '&' {
			break
		}
		i++
	}
	for i < n && unicode.IsSpace(rune(raw[i])) {
		i++
	}
	return i
}

func isRedirectTerminator(c byte) bool {
	return unicode.IsSpace(rune(c)) || c == ';' || c == '|' || c == '&'
}

func skipRedirectTarget(raw string, i int) (int, bool) {
	n := len(raw)
	for i < n && !isRedirectTerminator(raw[i]) {
		nextI, ok := advanceRedirectTargetChar(raw, i)
		if !ok {
			return 0, false
		}
		i = nextI
	}
	return i, true
}

func advanceRedirectTargetChar(raw string, i int) (int, bool) {
	switch raw[i] {
	case '\'':
		end := strings.IndexByte(raw[i+1:], '\'')
		if end < 0 {
			return 0, false
		}
		return i + end + 2, true
	case '"':
		end, _, ok := scanDoubleQuote(raw, i)
		return end, ok
	case '\\':
		return i + 2, true
	default:
		return i + 1, true
	}
}

func readSingleQuote(raw string, i int, cur *strings.Builder) (int, bool) {
	end := strings.IndexByte(raw[i+1:], '\'')
	if end < 0 {
		return 0, false
	}
	cur.WriteString(raw[i+1 : i+1+end])
	return i + 1 + end + 1, true
}

func readDoubleQuote(raw string, i int, cur *strings.Builder) (int, bool) {
	j, n := i+1, len(raw)
	for j < n {
		if raw[j] == '\\' {
			if j+1 >= n {
				return 0, false
			}
			cur.WriteByte(raw[j+1])
			j += 2
			continue
		}
		if raw[j] == '"' {
			return j + 1, true
		}
		cur.WriteByte(raw[j])
		j++
	}
	return 0, false
}

func readEscapedByte(raw string, i int, cur *strings.Builder) (int, bool) {
	if i+1 >= len(raw) {
		return 0, false
	}
	cur.WriteByte(raw[i+1])
	return i + 2, true
}

func parseSimpleCommand(raw string, pipedTo bool) (Command, bool, bool) {
	if isEnclosedGroup(raw) {
		return Command{}, false, false
	}

	words, ok := tokenizeWords(raw)
	if !ok {
		return Command{}, false, true
	}
	if len(words) == 0 {
		return Command{}, false, false
	}

	words, env := extractEnvAssignments(words)
	if len(words) == 0 {
		return Command{Raw: raw, Env: env}, true, false
	}
	if strings.HasPrefix(words[0], "$") {
		return Command{}, false, true
	}

	words, env, ok = unwrapWrappers(words, env)
	if !ok {
		return Command{}, false, true
	}

	cmdName := words[0]
	cmdBase := filepath.Base(cmdName)
	if isBannedCommand(cmdName, cmdBase, words[1:]) {
		return Command{}, false, true
	}

	return Command{
		Raw:     raw,
		Name:    cmdName,
		Base:    cmdBase,
		Args:    words[1:],
		Env:     env,
		PipedTo: pipedTo,
	}, true, false
}

func isEnclosedGroup(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	return (strings.HasPrefix(trimmed, "(") && strings.HasSuffix(trimmed, ")")) ||
		(strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}"))
}

func extractEnvAssignments(words []string) ([]string, []string) {
	var env []string
	for len(words) > 0 && isAssignment(words[0]) {
		env = append(env, words[0])
		words = words[1:]
	}
	return words, env
}

func isBannedCommand(name, base string, args []string) bool {
	if base == "eval" || base == "source" || name == "." {
		return true
	}
	return isShellBinary(base) && containsFlag(args, "-c")
}

func unwrapWrappers(words, env []string) ([]string, []string, bool) {
	for len(words) > 0 {
		base := filepath.Base(words[0])
		var ok bool
		switch base {
		case "env":
			words, env, ok = unwrapEnv(words[1:], env)
			if !ok {
				return nil, nil, false
			}
		case "command":
			words = unwrapCommand(words[1:])
		case "nice":
			words, ok = unwrapNice(words[1:])
			if !ok {
				return nil, nil, false
			}
		case "nohup":
			words = words[1:]
		default:
			if len(words) == 0 || strings.HasPrefix(words[0], "$") {
				return nil, nil, false
			}
			return words, env, true
		}
	}
	return nil, nil, false
}

func unwrapEnv(words, env []string) ([]string, []string, bool) {
	for len(words) > 0 {
		w := words[0]
		if w == "--" {
			return words[1:], env, true
		}
		if isEnvFlagWithArg(w) {
			if len(words) <= 1 {
				return nil, nil, false
			}
			words = words[2:]
			continue
		}
		if strings.HasPrefix(w, "-") {
			words = words[1:]
			continue
		}
		if isAssignment(w) {
			env = append(env, w)
			words = words[1:]
			continue
		}
		break
	}
	return words, env, true
}

func unwrapCommand(words []string) []string {
	for len(words) > 0 && strings.HasPrefix(words[0], "-") {
		words = words[1:]
	}
	return words
}

func unwrapNice(words []string) ([]string, bool) {
	for len(words) > 0 && strings.HasPrefix(words[0], "-") {
		if words[0] == "-n" {
			if len(words) <= 1 {
				return nil, false
			}
			words = words[2:]
			continue
		}
		words = words[1:]
	}
	return words, true
}

func isAssignment(w string) bool {
	idx := strings.IndexByte(w, '=')
	if idx <= 0 {
		return false
	}
	key := w[:idx]
	for i, r := range key {
		if (i == 0 && (r != '_' && !unicode.IsLetter(r))) ||
			(i > 0 && (r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r))) {
			return false
		}
	}
	return true
}

func isEnvFlagWithArg(f string) bool {
	return f == "-u" || f == "--unset" || f == "-C" || f == "--chdir" || f == "-S" || f == "--split-string"
}

func isShellBinary(b string) bool {
	return b == "sh" || b == "bash" || b == "zsh" || b == "dash"
}

func containsFlag(args []string, target string) bool {
	for _, a := range args {
		if a == target || (strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, strings.TrimPrefix(target, "-"))) {
			return true
		}
	}
	return false
}
