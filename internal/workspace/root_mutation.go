package workspace

import (
	"bytes"
	"context"
	"slices"

	"github.com/pelletier/go-toml/v2"
)

// AddRoot adds a validated named root through the config CAS write path.
// It preserves comments and formatting outside the [[roots]] tables; comments
// inside a [[roots]] table are lost when the roots block is rewritten.
func (s *Service) AddRoot(ctx context.Context, id, expectedHash string, spec RootSpec) (*Workspace, error) {
	return s.mutateRoots(ctx, id, expectedHash, func(roots []Root, home string) ([]Root, error) {
		root, err := s.validateRoot(spec.Name, spec.Access, spec.Path, home, roots)
		if err != nil {
			return nil, err
		}
		return append(roots, root), nil
	})
}

// RemoveRoot removes one named root through the config CAS write path.
// It preserves comments and formatting outside the [[roots]] tables; comments
// inside a [[roots]] table are lost when the roots block is rewritten.
func (s *Service) RemoveRoot(ctx context.Context, id, expectedHash, name string) (*Workspace, error) {
	return s.mutateRoots(ctx, id, expectedHash, func(roots []Root, _ string) ([]Root, error) {
		for i, root := range roots {
			if root.Name == name {
				return append(roots[:i:i], roots[i+1:]...), nil
			}
		}
		return nil, NewError(ErrRootInvalid, "root was not found")
	})
}

// UpdateRoot renames or changes the path or access of a root through config CAS.
// It preserves comments and formatting outside the [[roots]] tables; comments
// inside a [[roots]] table are lost when the roots block is rewritten.
func (s *Service) UpdateRoot(ctx context.Context, id, expectedHash, name string, update RootSpec) (*Workspace, error) {
	return s.mutateRoots(ctx, id, expectedHash, func(roots []Root, home string) ([]Root, error) {
		idx := -1
		for i := range roots {
			if roots[i].Name == name {
				idx = i
				break
			}
		}
		if idx < 0 {
			return nil, NewError(ErrRootInvalid, "root was not found")
		}
		current := roots[idx]
		if update.Name == "" {
			update.Name = current.Name
		}
		if update.Path == "" {
			update.Path = current.Path
		}
		if update.Access == "" {
			update.Access = current.Access
		}
		others := append(slices.Clone(roots[:idx]), roots[idx+1:]...)
		changed, err := s.validateRoot(update.Name, update.Access, update.Path, home, others)
		if err != nil {
			return nil, err
		}
		roots[idx] = changed
		return roots, nil
	})
}

func (s *Service) mutateRoots(ctx context.Context, id, expectedHash string, mutate func([]Root, string) ([]Root, error)) (*Workspace, error) {
	if expectedHash == "" {
		return nil, NewError(ErrConfigChanged, "expected_hash is required")
	}
	rec, home, raw, _, _, err := s.loadWorkspaceInputs(ctx, id)
	if err != nil {
		return nil, err
	}
	if hashErr := checkExpectedHash(raw, rec.ConfigHash, expectedHash); hashErr != nil {
		return nil, hashErr
	}
	cfg, roots, err := s.parseAndValidateTier3WithRoots(id, raw, home)
	if err != nil {
		return nil, err
	}
	newRoots, err := mutate(roots, home)
	if err != nil {
		return nil, err
	}
	if updated, ok := rewriteRootsTOML(raw, newRoots); ok {
		if _, parsedRoots, parseErr := s.parseAndValidateTier3WithRoots(id, updated, home); parseErr == nil && slices.Equal(parsedRoots, newRoots) {
			return s.UpdateWorkspaceConfig(ctx, id, updated, expectedHash)
		}
	}
	if s.logger != nil {
		s.logger.WarnContext(ctx, "could not rewrite [[roots]] tables in place; falling back to full-file TOML encode", "workspace_id", id)
	}
	cfg.Roots = &newRoots
	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).Encode(cfg); err != nil {
		return nil, WrapError(ErrConfigInvalid, "encode workspace configuration", err)
	}
	return s.UpdateWorkspaceConfig(ctx, id, encoded.Bytes(), expectedHash)
}

type rootTableSpan struct {
	start int
	end   int
}

type rootsTOMLBlock struct {
	Roots []Root `toml:"roots"`
}

// rewriteRootsTOML rewrites only the [[roots]] array-of-tables block in raw,
// preserving all bytes outside those tables (comments, key order, and whitespace).
// Any comments inside a [[roots]] table are lost when the block is rewritten.
// Scattered [[roots]] tables are gathered into the position of the first one.
func rewriteRootsTOML(raw []byte, newRoots []Root) ([]byte, bool) {
	var rootsBytes []byte
	if len(newRoots) > 0 {
		var buf bytes.Buffer
		if err := toml.NewEncoder(&buf).Encode(rootsTOMLBlock{Roots: newRoots}); err != nil {
			return nil, false
		}
		rootsBytes = buf.Bytes()
	}
	spans := locateRootTables(raw)
	if len(spans) == 0 {
		return appendRootsTOML(raw, rootsBytes), true
	}
	return spliceRootTables(raw, spans, rootsBytes), true
}

func appendRootsTOML(raw, rootsBytes []byte) []byte {
	if len(rootsBytes) == 0 {
		return slices.Clone(raw)
	}
	if len(raw) == 0 {
		return rootsBytes
	}
	out := make([]byte, 0, len(raw)+2+len(rootsBytes))
	out = append(out, raw...)
	if !bytes.HasSuffix(out, []byte("\n\n")) && !bytes.HasSuffix(out, []byte("\r\n\r\n")) {
		if bytes.HasSuffix(out, []byte("\n")) {
			out = append(out, '\n')
		} else {
			out = append(out, '\n', '\n')
		}
	}
	return append(out, rootsBytes...)
}

func spliceRootTables(raw []byte, spans []rootTableSpan, rootsBytes []byte) []byte {
	var out []byte
	cursor := 0
	for i, span := range spans {
		out = append(out, raw[cursor:span.start]...)
		if i == 0 && len(rootsBytes) > 0 {
			out = append(out, rootsBytes...)
			cursor = span.end
			continue
		}
		cursor = skipAdjacentBlankLine(raw, span.end, &out)
	}
	return append(out, raw[cursor:]...)
}

func skipAdjacentBlankLine(raw []byte, end int, out *[]byte) int {
	if bytes.HasPrefix(raw[end:], []byte("\r\n")) {
		return end + 2
	}
	if bytes.HasPrefix(raw[end:], []byte("\n")) {
		return end + 1
	}
	if end == len(raw) {
		if bytes.HasSuffix(*out, []byte("\r\n\r\n")) {
			*out = (*out)[:len(*out)-2]
		} else if bytes.HasSuffix(*out, []byte("\n\n")) {
			*out = (*out)[:len(*out)-1]
		}
	}
	return end
}

type tomlScanState struct {
	inMultiBasic   bool
	inMultiLiteral bool
	bracketDepth   int
}

func (s *tomlScanState) atTopLevel() bool {
	return !s.inMultiBasic && !s.inMultiLiteral && s.bracketDepth == 0
}

type rootTableLocator struct {
	state    tomlScanState
	spans    []rootTableSpan
	inRoots  bool
	curStart int
	curEnd   int
}

func (l *rootTableLocator) flush() {
	if l.inRoots {
		l.spans = append(l.spans, rootTableSpan{start: l.curStart, end: l.curEnd})
		l.inRoots = false
	}
}

func (l *rootTableLocator) handleLine(lineStart, lineEnd int, line []byte) {
	if l.state.atTopLevel() {
		if isRoots, isHeader := parseTableHeaderLine(line); isHeader {
			l.flush()
			if isRoots {
				l.inRoots = true
				l.curStart = lineStart
				l.curEnd = lineEnd
			}
			return
		}
	}
	if l.state.scanLine(line) && l.inRoots {
		l.curEnd = lineEnd
	}
}

func locateRootTables(raw []byte) []rootTableSpan {
	var loc rootTableLocator
	for pos := 0; pos < len(raw); {
		lineStart := pos
		lineEnd := nextLineEnd(raw, pos)
		pos = lineEnd
		loc.handleLine(lineStart, lineEnd, trimLineEnding(raw[lineStart:lineEnd]))
	}
	loc.flush()
	return loc.spans
}

func nextLineEnd(raw []byte, start int) int {
	if idx := bytes.IndexByte(raw[start:], '\n'); idx >= 0 {
		return start + idx + 1
	}
	return len(raw)
}

func trimLineEnding(line []byte) []byte {
	line = bytes.TrimSuffix(line, []byte("\n"))
	return bytes.TrimSuffix(line, []byte("\r"))
}

func parseTableHeaderLine(line []byte) (isRoots, isHeader bool) {
	trimmed := bytes.TrimLeft(line, " \t")
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return false, false
	}
	if !bytes.HasPrefix(trimmed, []byte("[[")) {
		return false, true
	}
	rest := trimmed[2:]
	end := bytes.Index(rest, []byte("]]"))
	if end < 0 {
		return false, true
	}
	after := bytes.TrimLeft(rest[end+2:], " \t")
	if len(after) > 0 && after[0] != '#' {
		return false, false
	}
	name := bytes.Trim(rest[:end], " \t")
	if len(name) >= 2 && ((name[0] == '"' && name[len(name)-1] == '"') || (name[0] == '\'' && name[len(name)-1] == '\'')) {
		name = name[1 : len(name)-1]
	}
	return string(name) == "roots", true
}

func (s *tomlScanState) scanLine(line []byte) bool {
	startedMultiline := !s.atTopLevel()
	hasToken := false
	for i := 0; i < len(line); {
		switch {
		case s.inMultiBasic:
			i = s.scanMultiBasic(line, i)
		case s.inMultiLiteral:
			i = s.scanMultiLiteral(line, i)
		default:
			next, token, done := s.stepNormal(line, i)
			if token {
				hasToken = true
			}
			if done {
				return startedMultiline || hasToken
			}
			i = next
		}
	}
	return startedMultiline || hasToken
}

func (s *tomlScanState) stepNormal(line []byte, i int) (next int, token, done bool) {
	switch b := line[i]; b {
	case ' ', '\t':
		return i + 1, false, false
	case '#':
		return len(line), false, true
	case '"', '\'':
		return s.stepQuote(line, i, b), true, false
	case '[', '{':
		s.bracketDepth++
		return i + 1, true, false
	case ']', '}':
		if s.bracketDepth > 0 {
			s.bracketDepth--
		}
		return i + 1, true, false
	default:
		return i + 1, true, false
	}
}

func (s *tomlScanState) stepQuote(line []byte, i int, q byte) int {
	if q == '"' {
		if bytes.HasPrefix(line[i:], []byte(`"""`)) {
			s.inMultiBasic = true
			return i + 3
		}
		return scanBasicString(line, i+1)
	}
	if bytes.HasPrefix(line[i:], []byte(`'''`)) {
		s.inMultiLiteral = true
		return i + 3
	}
	return scanLiteralString(line, i+1)
}

func (s *tomlScanState) scanMultiBasic(line []byte, i int) int {
	for i < len(line) {
		if line[i] == '\\' {
			i += 2
			continue
		}
		if bytes.HasPrefix(line[i:], []byte(`"""`)) {
			i += 3
			for extra := 0; extra < 2 && i < len(line) && line[i] == '"'; extra++ {
				i++
			}
			s.inMultiBasic = false
			return i
		}
		i++
	}
	return i
}

func (s *tomlScanState) scanMultiLiteral(line []byte, i int) int {
	for i < len(line) {
		if bytes.HasPrefix(line[i:], []byte(`'''`)) {
			i += 3
			for extra := 0; extra < 2 && i < len(line) && line[i] == '\''; extra++ {
				i++
			}
			s.inMultiLiteral = false
			return i
		}
		i++
	}
	return i
}

func scanBasicString(line []byte, i int) int {
	for i < len(line) {
		if line[i] == '\\' {
			i += 2
			continue
		}
		if line[i] == '"' {
			return i + 1
		}
		i++
	}
	return i
}

func scanLiteralString(line []byte, i int) int {
	for i < len(line) {
		if line[i] == '\'' {
			return i + 1
		}
		i++
	}
	return i
}
