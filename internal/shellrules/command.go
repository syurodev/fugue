package shellrules

import (
	"path/filepath"
	"slices"
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

func tokenizeWords(raw string) ([]string, []string, bool) {
	var words, redirs []string
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
			trimFDPrefix(&cur, &inWord)
			words = flushWord(words, &cur, &inWord)
			target, nextI, ok := readRedirection(raw, i)
			if !ok {
				return nil, nil, false
			}
			if target != "" {
				redirs = append(redirs, target)
			}
			i = nextI
			continue
		}

		inWord = true
		nextI, ok := readTokenChar(raw, i, c, &cur)
		if !ok {
			return nil, nil, false
		}
		i = nextI
	}
	words = flushWord(words, &cur, &inWord)
	return words, redirs, true
}

// trimFDPrefix drops the "2" of "2>" and the "&" of "&>" so they are not read as arguments.
func trimFDPrefix(cur *strings.Builder, inWord *bool) {
	rest := strings.TrimSuffix(cur.String(), "&")
	if strings.Trim(rest, "0123456789") != "" {
		return
	}
	cur.Reset()
	*inWord = false
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

// readRedirection parses the operator at raw[i] and returns the output target (empty for
// input redirections and fd duplication). A dynamic output target is unparsable.
func readRedirection(raw string, i int) (string, int, bool) {
	out := raw[i] == '>'
	i, dup := skipRedirectOperator(raw, i, out)
	if dup {
		return "", i, true
	}
	start := i
	var cur strings.Builder
	for i < len(raw) && !isRedirectTerminator(raw[i]) {
		next, ok := readTokenChar(raw, i, raw[i], &cur)
		if !ok {
			return "", 0, false
		}
		i = next
	}
	if i == start || (out && strings.ContainsAny(raw[start:i], "$`")) {
		return "", 0, false
	}
	if !out {
		return "", i, true
	}
	return cur.String(), i, true
}

// skipRedirectOperator consumes the operator and following blanks; dup reports N>&M.
func skipRedirectOperator(raw string, i int, out bool) (next int, dup bool) {
	n := len(raw)
	i++
	if i < n && raw[i] == raw[i-1] {
		i++
	}
	if out && i < n && raw[i] == '|' {
		i++
	}
	if i < n && raw[i] == '&' {
		if j := skipFDDup(raw, i+1); j > i+1 {
			return j, true
		}
		i++
	}
	for i < n && unicode.IsSpace(rune(raw[i])) {
		i++
	}
	return i, false
}

func skipFDDup(raw string, j int) int {
	k := j
	for k < len(raw) && raw[k] >= '0' && raw[k] <= '9' {
		k++
	}
	if k == j && k < len(raw) && raw[k] == '-' {
		k++
	}
	return k
}

func isRedirectTerminator(c byte) bool {
	return unicode.IsSpace(rune(c)) || c == ';' || c == '|' || c == '&'
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

	words, redirs, ok := tokenizeWords(raw)
	if !ok {
		return Command{}, false, true
	}
	if len(words) == 0 && len(redirs) == 0 {
		return Command{}, false, false
	}

	words, env := extractEnvAssignments(words)
	if len(words) == 0 {
		return Command{Raw: raw, Env: env, Redirects: redirs}, true, false
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
		Raw:       raw,
		Name:      cmdName,
		Base:      cmdBase,
		Args:      words[1:],
		Env:       env,
		Redirects: redirs,
		PipedTo:   pipedTo,
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
	return (isShellBinary(base) && containsFlag(args, "-c")) || runsCodeFromData(base, args)
}

// runsCodeFromData reports runners and interpreters whose command is data the splitter
// cannot see: xargs, find -exec, inline interpreter code.
func runsCodeFromData(base string, args []string) bool {
	switch base {
	case "xargs", "watch", "parallel", "busybox":
		return true
	case "find":
		return slices.ContainsFunc(args, func(a string) bool {
			return a == "-exec" || a == "-execdir" || a == "-ok" || a == "-okdir"
		})
	}
	return slices.ContainsFunc(inlineCodeFlags(base), func(f string) bool {
		return containsFlag(args, f) || slices.ContainsFunc(args, func(a string) bool { return strings.HasPrefix(a, f+"=") })
	})
}

func inlineCodeFlags(base string) []string {
	switch {
	case strings.HasPrefix(base, "python"):
		return []string{"-c"}
	case base == "perl":
		return []string{"-e", "-E"}
	case base == "ruby":
		return []string{"-e"}
	case base == "node":
		return []string{"-e", "-p", "--eval", "--print"}
	case base == "php":
		return []string{"-r"}
	}
	return nil
}

// wrapperSpec describes a prefix wrapper: option flags that take a value and the number of
// positional operands (a duration, a priority) before the wrapped command.
type wrapperSpec struct {
	argFlags []string
	operands int
}

func prefixWrapper(base string) (wrapperSpec, bool) {
	switch base {
	case "command", "nohup", "setsid":
		return wrapperSpec{}, true
	case "time":
		return wrapperSpec{argFlags: []string{"-f", "-o", "--format", "--output"}}, true
	case "nice":
		return wrapperSpec{argFlags: []string{"-n", "--adjustment"}}, true
	case "sudo":
		return wrapperSpec{argFlags: []string{"-u", "-g", "-h", "-p", "-C", "-T", "-r", "-t", "-D", "-R", "-U"}}, true
	case "doas":
		return wrapperSpec{argFlags: []string{"-u", "-C"}}, true
	case "timeout":
		return wrapperSpec{argFlags: []string{"-s", "-k", "--signal", "--kill-after"}, operands: 1}, true
	case "exec":
		return wrapperSpec{argFlags: []string{"-a"}}, true
	case "stdbuf":
		return wrapperSpec{argFlags: []string{"-i", "-o", "-e", "--input", "--output", "--error"}}, true
	case "ionice":
		return wrapperSpec{argFlags: []string{"-c", "-n", "-p", "-P", "-u"}}, true
	case "chrt":
		return wrapperSpec{operands: 1}, true
	}
	return wrapperSpec{}, false
}

func unwrapWrappers(words, env []string) ([]string, []string, bool) {
	for len(words) > 0 {
		base := filepath.Base(words[0])
		spec, isPrefix := prefixWrapper(base)
		var ok bool
		switch {
		case base == "env":
			words, env, ok = unwrapEnv(words[1:], env)
		case isPrefix:
			words, env, ok = unwrapPrefix(words[1:], env, spec)
		default:
			return words, env, !strings.HasPrefix(words[0], "$")
		}
		if !ok {
			return nil, nil, false
		}
	}
	return nil, nil, false
}

func unwrapPrefix(words, env []string, spec wrapperSpec) ([]string, []string, bool) {
	for len(words) > 0 {
		w := words[0]
		if w == "--" {
			words = words[1:]
			break
		}
		if isAssignment(w) {
			env = append(env, w)
			words = words[1:]
			continue
		}
		if !strings.HasPrefix(w, "-") {
			break
		}
		step := 1
		if slices.Contains(spec.argFlags, w) {
			step = 2
		}
		if len(words) < step {
			return nil, nil, false
		}
		words = words[step:]
	}
	if len(words) < spec.operands {
		return nil, nil, false
	}
	return words[spec.operands:], env, true
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
