package workspace

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The literals the file-read tests share.
const (
	fileTestNested = "nested"
	fileTestBody   = "a\nb\nc"
)

// fileReadFixture is one workspace whose single root holds the files the
// host-api §5.4 read assertions use.
type fileReadFixture struct {
	svc     *Service
	wsID    string
	root    string
	outside string
}

func newFileReadFixture(t *testing.T) *fileReadFixture {
	t.Helper()
	f := newTestFixture(t)
	root := filepath.Join(f.tempDir, "repo")
	if err := os.MkdirAll(filepath.Join(root, fileTestNested), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(f.tempDir, "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "dir-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(root, "self-link")); err != nil {
		t.Fatal(err)
	}
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("notes.md", strings.Repeat("row\n", 50))
	write(filepath.Join(fileTestNested, "main.go"), "package nested\n")
	write("bin.dat", "a\x00b")
	write("big.txt", strings.Repeat("x", FileReadMaxBytes+1))
	ws, err := f.svc.CreateWorkspace(t.Context(), CreateWorkspaceRequest{
		Name:    "file-reads",
		Roots:   []RootSpec{{Name: testMainRoot, Path: root, Access: AccessReadWrite}},
		HomeDir: filepath.Join(f.tempDir, "userhome"),
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	return &fileReadFixture{svc: f.svc, wsID: ws.ID, root: root, outside: outside}
}

// TestReadFileConfinesToTheRegisteredRoot exercises the service-level file
// reads of host-api v0.18 §5.4: whole and range reads with totals, the
// escape refusals, the binary and size refusals, and directory reads.
func TestReadFileConfinesToTheRegisteredRoot(t *testing.T) {
	t.Parallel()
	f := newFileReadFixture(t)

	assertFileReadAnswersTotalsAndRanges(t, f)
	assertFileReadRefusesEscapesAndNonText(t, f)

	entries, listErr := f.svc.ListDir(t.Context(), f.wsID, testMainRoot, ".")
	if listErr != nil {
		t.Fatalf("list root: %v", listErr)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	if !slices.IsSorted(names) || slices.Index(names, fileTestNested) == -1 {
		t.Fatalf("root listing = %v", names)
	}
	idx := slices.IndexFunc(entries, func(e DirEntry) bool { return e.Name == fileTestNested })
	if entries[idx].Kind != "dir" {
		t.Fatalf("nested entry = %+v, want kind dir", entries[idx])
	}
	if _, dotErr := f.svc.ListDir(t.Context(), f.wsID, testMainRoot, "../outside"); CodeOf(dotErr) != ErrPathOutsideRoot {
		t.Fatalf("listing escape error = %v", dotErr)
	}
	if _, symErr := f.svc.ListDir(t.Context(), f.wsID, testMainRoot, "dir-link"); CodeOf(symErr) != ErrPathOutsideRoot {
		t.Fatalf("listing symlink escape error = %v", symErr)
	}
	if _, nulErr := f.svc.ListDir(t.Context(), f.wsID, testMainRoot, "notes\x00.md"); CodeOf(nulErr) != ErrPathOutsideRoot {
		t.Fatalf("listing NUL error = %v", nulErr)
	}
}

// assertFileReadAnswersTotalsAndRanges covers the whole read, the exact
// range slice with its truncated flag, and the nested language hint.
func assertFileReadAnswersTotalsAndRanges(t *testing.T, f *fileReadFixture) {
	t.Helper()
	ctx := t.Context()
	whole, wholeErr := f.svc.ReadFile(ctx, f.wsID, testMainRoot, "notes.md", 0, 0)
	if wholeErr != nil {
		t.Fatalf("whole read: %v", wholeErr)
	}
	if whole.TotalLines != 50 || whole.Language != langMarkdown || whole.Truncated {
		t.Fatalf("whole read = (%d lines, %q language, truncated %v)", whole.TotalLines, whole.Language, whole.Truncated)
	}

	ranged, rangeErr := f.svc.ReadFile(ctx, f.wsID, testMainRoot, "notes.md", 3, 5)
	if rangeErr != nil {
		t.Fatalf("range read: %v", rangeErr)
	}
	if ranged.Content != "row\nrow\nrow" || ranged.TotalLines != 50 || !ranged.Truncated {
		t.Fatalf("range read = (%q, %d lines, truncated %v)", ranged.Content, ranged.TotalLines, ranged.Truncated)
	}

	nested, nestedErr := f.svc.ReadFile(ctx, f.wsID, testMainRoot, fileTestNested+"/main.go", 0, 0)
	if nestedErr != nil || nested.Language != langGo {
		t.Fatalf("nested read = (%+v, %v)", nested, nestedErr)
	}
}

// assertFileReadRefusesEscapesAndNonText covers every typed refusal plus the
// unknown root and workspace.
func assertFileReadRefusesEscapesAndNonText(t *testing.T, f *fileReadFixture) {
	t.Helper()
	ctx := t.Context()
	for name, tc := range map[string]struct {
		path string
		code Code
	}{
		"dotdot":    {"../outside/secret.txt", ErrPathOutsideRoot},
		"deep":      {fileTestNested + "/../../outside/secret.txt", ErrPathOutsideRoot},
		"absolute":  {filepath.Join(f.outside, "secret.txt"), ErrPathOutsideRoot},
		"symlink":   {"link.txt", ErrPathOutsideRoot},
		"selflink":  {"self-link", ErrPathOutsideRoot},
		"nul":       {"notes\x00.md", ErrPathOutsideRoot},
		"missing":   {"gone.md", ErrFileNotFound},
		"directory": {fileTestNested, ErrFileNotFound},
		"binary":    {"bin.dat", ErrNotText},
		"oversize":  {"big.txt", ErrFileTooLarge},
		"empty":     {"", ErrPathOutsideRoot},
	} {
		_, readErr := f.svc.ReadFile(ctx, f.wsID, testMainRoot, tc.path, 0, 0)
		if CodeOf(readErr) != tc.code {
			t.Fatalf("%s read error = %v, want %v", name, readErr, tc.code)
		}
	}
	if _, rootErr := f.svc.ReadFile(ctx, f.wsID, "nope", "notes.md", 0, 0); CodeOf(rootErr) != ErrRootInvalid {
		t.Fatalf("unknown root error = %v", rootErr)
	}
	if _, wsErr := f.svc.ReadFile(ctx, "ws-none", testMainRoot, "notes.md", 0, 0); CodeOf(wsErr) != ErrNotFound {
		t.Fatalf("unknown workspace error = %v", wsErr)
	}
}

// TestLanguageHintCoversExtensionsAndSpecialNames pins the extension map the
// viewer's syntax coloring relies on.
func TestLanguageHintCoversExtensionsAndSpecialNames(t *testing.T) {
	t.Parallel()
	for path, want := range map[string]string{
		"a.go": langGo, "a.ts": langTypeScript, "a.tsx": langTypeScript,
		"a.js": langJavaScript, "a.jsx": langJavaScript, "a.py": "python",
		"a.rs": "rust", "a.json": "json", "a.yaml": "yaml", "a.toml": "toml",
		"a.sql": "sql", "a.proto": "protobuf", "a.sh": langBash,
		"a.md": langMarkdown, "a.mdx": langMarkdown, "a.css": "css",
		"a.html": "html", "a.cpp": langCPP, "a.cc": langCPP, "a.hpp": langCPP,
		"Dockerfile": "dockerfile", "dockerfile.prod": "dockerfile",
		"Makefile": "makefile", "go.mod": langGo, "go.sum": langGo,
		"a.unknown": langText, "README": langText,
	} {
		if got := LanguageHint(path); got != want {
			t.Fatalf("LanguageHint(%q) = %q, want %q", path, got, want)
		}
	}
}

// TestApplyLineRange pins the range clipping edges: unset bounds read whole,
// clipped ranges mark truncation, inverted or out-of-range ranges never cut.
func TestApplyLineRange(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		content       string
		start, end    int64
		want          string
		wantTruncated bool
	}{
		"unset":        {fileTestBody, 0, 0, fileTestBody, false},
		"middle":       {fileTestBody, 2, 2, "b", true},
		"tail":         {fileTestBody, 3, 0, "c", true},
		"head":         {fileTestBody, 1, 1, "a", true},
		"beyond":       {fileTestBody, 2, 99, "b\nc", true},
		"pastEnd":      {fileTestBody, 9, 0, fileTestBody, false},
		"inverted":     {fileTestBody, 3, 2, fileTestBody, false},
		"emptyFile":    {"", 1, 1, "", false},
		"wholeByRange": {fileTestBody, 1, 3, fileTestBody, false},
	} {
		content := tc.content
		got := applyLineRange(&content, totalLines(tc.content), tc.start, tc.end)
		if content != tc.want || got != tc.wantTruncated {
			t.Fatalf("%s: applyLineRange = (%q, %v), want (%q, %v)", name, content, got, tc.want, tc.wantTruncated)
		}
	}
}

// TestHashFile verifies streamed SHA-256 hashing over raw bytes (binary,
// > 1 MiB, exact trailing newline preservation) and root confinement.
func TestHashFile(t *testing.T) {
	t.Parallel()
	f := newFileReadFixture(t)
	ctx := t.Context()

	binBytes := []byte{0x00, 0xff, 0xfe, 0x01, 'b', 'i', 'n', 0x00}
	largeBytes := bytes.Repeat([]byte("A"), FileReadMaxBytes+4096)
	withNewline := []byte("hello\n")
	withoutNewline := []byte("hello")

	for name, data := range map[string][]byte{
		"raw-bin.dat":    binBytes,
		"over-1mib.dat":  largeBytes,
		"with-nl.txt":    withNewline,
		"without-nl.txt": withoutNewline,
	} {
		if err := os.WriteFile(filepath.Join(f.root, name), data, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		got, err := f.svc.HashFile(ctx, f.wsID, testMainRoot, name)
		if err != nil {
			t.Fatalf("HashFile(%s): %v", name, err)
		}
		sum := sha256.Sum256(data)
		want := hex.EncodeToString(sum[:])
		if got != want {
			t.Fatalf("HashFile(%s) = %q, want %q", name, got, want)
		}
	}

	nlHash, err := f.svc.HashFile(ctx, f.wsID, testMainRoot, "with-nl.txt")
	if err != nil {
		t.Fatalf("HashFile(with-nl.txt): %v", err)
	}
	noNLHash, err := f.svc.HashFile(ctx, f.wsID, testMainRoot, "without-nl.txt")
	if err != nil {
		t.Fatalf("HashFile(without-nl.txt): %v", err)
	}
	if nlHash == noNLHash {
		t.Fatalf("trailing newline should change hash, both gave %q", nlHash)
	}

	for _, esc := range []string{"../outside/secret.txt", filepath.Join(f.outside, "secret.txt"), "link.txt", ""} {
		if _, err := f.svc.HashFile(ctx, f.wsID, testMainRoot, esc); CodeOf(err) != ErrPathOutsideRoot {
			t.Fatalf("HashFile(%q) code = %v, want %v", esc, CodeOf(err), ErrPathOutsideRoot)
		}
	}
	if _, err := f.svc.HashFile(ctx, f.wsID, testMainRoot, "missing.bin"); CodeOf(err) != ErrFileNotFound {
		t.Fatalf("HashFile missing code = %v, want %v", CodeOf(err), ErrFileNotFound)
	}
	if _, err := f.svc.HashFile(ctx, f.wsID, testMainRoot, fileTestNested); CodeOf(err) != ErrFileNotFound {
		t.Fatalf("HashFile dir code = %v, want %v", CodeOf(err), ErrFileNotFound)
	}
}
