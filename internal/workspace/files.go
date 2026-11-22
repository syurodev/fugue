package workspace

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

// The read-only file and directory reads of host-api v0.18 §5.4: the app's
// viewer asks the host, the host confines every path to the workspace's
// registered roots and never caches content. Transcript paths are untrusted
// data; every rule here assumes the caller got the path from a model.

const (
	// FileReadMaxBytes is the per-read size limit; larger files are refused
	// with ErrFileTooLarge (host-api §5.4).
	FileReadMaxBytes = 1 << 20
	// DirListMaxEntries caps one directory listing.
	DirListMaxEntries = 1000
	// binarySniffBytes is how much of the head a binary check inspects.
	binarySniffBytes = 8 * 1024
)

// FileRead is one confined ReadWorkspaceFile result. Content is UTF-8 text;
// callers must keep it out of logs, errors and audit metadata.
type FileRead struct {
	Content    string
	TotalLines int64
	Language   string
	Truncated  bool
}

// DirEntry is one listed directory entry (host-api §5.4).
type DirEntry struct {
	Name string
	Kind string // "file" or "dir"
	Size int64
}

// ReadFile reads a UTF-8 text file inside one of the workspace's registered
// roots. The path is root-relative; absolute paths, ".." escapes and
// symlinks resolving outside the root are refused with ErrPathOutsideRoot.
// startLine and endLine are a 1-based inclusive range; zero on either bound
// means "from the first line" / "to the last line".
func (s *Service) ReadFile(ctx context.Context, workspaceID, rootName, path string, startLine, endLine int64) (*FileRead, error) {
	target, err := s.confinedPath(ctx, workspaceID, rootName, path)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, NewError(ErrFileNotFound, "the file does not exist in the workspace root")
		}
		return nil, WrapError(ErrFileNotFound, "stat the file", err)
	}
	if info.IsDir() {
		return nil, NewError(ErrFileNotFound, "the path is a directory, not a file")
	}
	if info.Size() > FileReadMaxBytes {
		return nil, NewError(ErrFileTooLarge, "the file exceeds the 1 MiB read limit")
	}
	raw, err := os.ReadFile(target) // #nosec G304 -- target just passed resolveInsideRoot's confinement to a registered root.
	if err != nil {
		if os.IsNotExist(err) {
			return nil, NewError(ErrFileNotFound, "the file does not exist in the workspace root")
		}
		return nil, WrapError(ErrFileNotFound, "read the file", err)
	}
	if int64(len(raw)) > FileReadMaxBytes {
		// The file grew between the stat and the read; the limit still holds.
		return nil, NewError(ErrFileTooLarge, "the file exceeds the 1 MiB read limit")
	}
	if hasNULInHead(raw, binarySniffBytes) || !utf8.Valid(raw) {
		return nil, NewError(ErrNotText, "the file is not UTF-8 text")
	}
	content := strings.TrimSuffix(string(raw), "\n")
	total := totalLines(content)
	out := &FileRead{Content: content, TotalLines: total, Language: LanguageHint(path)}
	out.Truncated = applyLineRange(&out.Content, total, startLine, endLine)
	return out, nil
}

func totalLines(content string) int64 {
	if content == "" {
		return 0
	}
	return int64(strings.Count(content, "\n")) + 1
}

// applyLineRange clips content to the 1-based inclusive [startLine, endLine]
// range in place and reports whether any content was cut. A bound of zero
// means unset; out-of-range or inverted ranges are left unclipped and report
// no cut, so a caller asking for lines of an empty file still reads it whole.
func applyLineRange(content *string, total, startLine, endLine int64) bool {
	if startLine <= 0 && endLine <= 0 {
		return false
	}
	if endLine > 0 && startLine > 0 && endLine < startLine {
		return false
	}
	first := max(startLine, 1)
	last := total
	if endLine > 0 {
		last = min(endLine, total)
	}
	if first > total || first == 1 && last == total {
		return false
	}
	parts := strings.Split(*content, "\n")
	*content = strings.Join(parts[first-1:min(last, int64(len(parts)))], "\n")
	return true
}

// ListDir lists one directory inside a registered root, sorted by name and
// capped at DirListMaxEntries. An empty path lists the root itself.
func (s *Service) ListDir(ctx context.Context, workspaceID, rootName, path string) ([]DirEntry, error) {
	if path == "" {
		path = "."
	}
	target, err := s.confinedPath(ctx, workspaceID, rootName, path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, NewError(ErrFileNotFound, "the directory does not exist in the workspace root")
		}
		return nil, WrapError(ErrFileNotFound, "list the directory", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	out := make([]DirEntry, 0, min(len(entries), DirListMaxEntries))
	for _, e := range entries {
		if len(out) == DirListMaxEntries {
			break
		}
		entry := DirEntry{Name: e.Name(), Kind: "file"}
		if e.Type().IsDir() {
			entry.Kind = "dir"
		} else if info, statErr := e.Info(); statErr == nil {
			entry.Size = info.Size()
		}
		out = append(out, entry)
	}
	return out, nil
}

// confinedPath resolves a root-relative transcript path to a real path inside
// the root's canonical directory (host-api §5.4): reject absolute paths,
// normalize, resolve symlinks on the result, then require the resolved path
// to stay inside the resolved registered root.
func (s *Service) confinedPath(ctx context.Context, workspaceID, rootName, path string) (string, error) {
	if path == "" {
		return "", NewError(ErrPathOutsideRoot, "the path is empty")
	}
	if filepath.IsAbs(path) || strings.ContainsRune(path, 0) {
		return "", NewError(ErrPathOutsideRoot, "the path must be relative to the workspace root")
	}
	rootPath, err := s.confinedRoot(ctx, workspaceID, rootName)
	if err != nil {
		return "", err
	}
	return resolveInsideRoot(rootPath, path)
}

// confinedRoot resolves the named root of the workspace to its canonical
// on-disk directory, refusing unknown names and missing roots.
func (s *Service) confinedRoot(ctx context.Context, workspaceID, rootName string) (string, error) {
	ws, err := s.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return "", err
	}
	targetName, resolveErr := resolveTargetRootName(ws.Roots, rootName)
	if resolveErr != nil {
		return "", resolveErr
	}
	idx := slices.IndexFunc(ws.Roots, func(r Root) bool { return r.Name == targetName })
	if idx == -1 {
		return "", NewError(ErrRootInvalid, "root not found in the workspace")
	}
	root := ws.Roots[idx]
	if IsRootMissing(root) {
		return "", NewError(ErrRootMissing, "the root is missing on disk")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root.Path)
	if err != nil {
		return "", WrapError(ErrRootMissing, "resolve the root", err)
	}
	return resolvedRoot, nil
}

// resolveInsideRoot normalizes a root-relative path, resolves symlinks on
// the join, and refuses anything resolving outside the root: a ".." escape
// before the join and a resolved path outside after it both fail with one
// code (host-api §5.4).
func resolveInsideRoot(resolvedRoot, path string) (string, error) {
	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", NewError(ErrPathOutsideRoot, "the path escapes the workspace root")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(resolvedRoot, clean))
	if err != nil {
		if os.IsNotExist(err) {
			return "", NewError(ErrFileNotFound, "the path does not exist in the workspace root")
		}
		return "", WrapError(ErrFileNotFound, "resolve the path", err)
	}
	rel, relErr := filepath.Rel(resolvedRoot, resolved)
	if relErr != nil || rel == "." && clean != "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", NewError(ErrPathOutsideRoot, "the path resolves outside the workspace root")
	}
	return resolved, nil
}

func hasNULInHead(raw []byte, head int) bool {
	n := min(len(raw), head)
	for _, b := range raw[:n] {
		if b == 0 {
			return true
		}
	}
	return false
}

// Language names LanguageHint returns for more than one extension, named so
// the extension table carries no duplicate literals.
const (
	langBash       = "bash"
	langCPP        = "cpp"
	langGo         = "go"
	langJavaScript = "javascript"
	langMarkdown   = "markdown"
	langText       = "text"
	langTypeScript = "typescript"
)

// LanguageHint suggests a language name from the file's extension (host-api
// §5.4), matching the viewer's chroma roles; known special filenames return
// their own language, everything else "text".
func LanguageHint(path string) string {
	trimmed := strings.TrimSpace(path)
	name := strings.ToLower(filepath.Base(trimmed))
	switch {
	case name == "dockerfile" || strings.HasPrefix(name, "dockerfile."):
		return "dockerfile"
	case name == "makefile" || strings.HasPrefix(name, "makefile."):
		return "makefile"
	case name == "go.mod" || name == "go.sum" || name == "go.work":
		return langGo
	}
	lang, ok := languageHints()[strings.ToLower(filepath.Ext(trimmed))]
	if !ok {
		return langText
	}
	return lang
}

// languageHints maps file extensions to language names (host-api §5.4).
func languageHints() map[string]string {
	return map[string]string{
		".go": langGo, ".ts": langTypeScript, ".tsx": langTypeScript,
		".js": langJavaScript, ".jsx": langJavaScript, ".py": "python",
		".rs": "rust", ".rb": "ruby", ".java": "java", ".kt": "kotlin",
		".swift": "swift", ".c": "c", ".h": "c", ".cpp": langCPP, ".cc": langCPP,
		".hpp": langCPP, ".cs": "csharp", ".php": "php", ".json": "json",
		".yaml": "yaml", ".yml": "yaml", ".toml": "toml", ".sql": "sql",
		".proto": "protobuf", ".sh": langBash, ".bash": langBash, ".zsh": langBash,
		".md": langMarkdown, ".mdx": langMarkdown, ".css": "css", ".scss": "scss",
		".html": "html", ".xml": "xml", ".lua": "lua",
	}
}
