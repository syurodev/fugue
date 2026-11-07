package arch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

const (
	adrFile          = "docs/adr/0001-package-layout.md"
	dependenciesFile = "docs/dependencies.md"
	roadmapFile      = "docs/roadmap.md"
	repoRoot         = "../.."
)

func adrFiles() []string {
	return []string{
		"docs/adr/0001-package-layout.md",
		"docs/adr/0002-vendor-domain.md",
	}
}

var (
	errNoTable = errors.New("markdown table not found")
	errBadRow  = errors.New("malformed table row")
)

// repo is a repository root: the real one or a mini-repository in testdata.
type repo struct{ root string }

func fixture(name string) repo { return repo{root: filepath.Join("testdata", name)} }

// goPackage is the part of `go list -json` the rules use.
type goPackage struct {
	ImportPath   string
	Dir          string
	GoFiles      []string
	TestGoFiles  []string
	XTestGoFiles []string
	Imports      []string
	TestImports  []string
	XTestImports []string
	Module       struct{ Path string }
}

func (p *goPackage) rel() string {
	return strings.TrimPrefix(strings.TrimPrefix(p.ImportPath, p.Module.Path), "/")
}

func (p *goPackage) allImports() []string {
	return slices.Concat(p.Imports, p.TestImports, p.XTestImports)
}

// packages lists the repository's packages. -e keeps going past imports that
// are not downloaded: the rules read syntax and import paths, not types.
func (r repo) packages(ctx context.Context) ([]*goPackage, error) {
	cmd := exec.CommandContext(ctx, "go", "list", "-e", "-json", "./...")
	cmd.Dir = r.root
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("go list in %s: %w", r.root, err)
	}
	var pkgs []*goPackage
	dec := json.NewDecoder(&out)
	for {
		var p goPackage
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			return pkgs, nil
		} else if err != nil {
			return nil, fmt.Errorf("decode go list: %w", err)
		}
		pkgs = append(pkgs, &p)
	}
}

// files returns the repository-relative files with one of the suffixes,
// skipping testdata, vendored code and hidden directories.
func (r repo) files(suffixes ...string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(r.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if p != r.root && (name == "testdata" || name == "vendor" || strings.HasPrefix(name, ".") || name == "node_modules") {
				return fs.SkipDir
			}
			return nil
		}
		if slices.ContainsFunc(suffixes, func(sfx string) bool { return strings.HasSuffix(name, sfx) }) {
			rel, relErr := filepath.Rel(r.root, p)
			if relErr != nil {
				return fmt.Errorf("relative path of %s: %w", p, relErr)
			}
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", r.root, err)
	}
	slices.Sort(files)
	return files, nil
}

func (r repo) read(rel string) (string, error) {
	data, err := fs.ReadFile(os.DirFS(r.root), rel)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", rel, err)
	}
	return string(data), nil
}

func (r repo) parse(rel string) (*ast.File, error) {
	src, err := r.read(rel)
	if err != nil {
		return nil, err
	}
	f, err := parser.ParseFile(token.NewFileSet(), rel, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", rel, err)
	}
	return f, nil
}

// table returns the data rows of every markdown table whose header starts with
// header, as trimmed cells with backticks removed.
func table(md, header string) ([][]string, error) {
	lines := strings.Split(md, "\n")
	var rows [][]string
	found := false
	for i, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), header) {
			continue
		}
		found = true
		for _, l := range lines[i+2:] {
			l = strings.TrimSpace(l)
			if !strings.HasPrefix(l, "|") {
				break
			}
			cells := strings.Split(strings.Trim(l, "|"), "|")
			for j := range cells {
				cells[j] = strings.TrimSpace(strings.ReplaceAll(cells[j], "`", ""))
			}
			rows = append(rows, cells)
		}
	}
	if !found {
		return nil, fmt.Errorf("%w: %q", errNoTable, header)
	}
	return rows, nil
}

func (r repo) table(rel, header string) ([][]string, error) {
	md, err := r.read(rel)
	if err != nil {
		return nil, err
	}
	return table(md, header)
}

// matchPackage matches a repository-relative package path against a table
// entry; a trailing "/..." matches the whole subtree.
func matchPackage(entry, rel string) bool {
	if tree, ok := strings.CutSuffix(entry, "/..."); ok {
		return rel == tree || strings.HasPrefix(rel, tree+"/")
	}
	return entry == rel
}

// expectViolations asserts that every wanted fragment appears in some violation.
func expectViolations(t *testing.T, got []string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !slices.ContainsFunc(got, func(v string) bool { return strings.Contains(v, w) }) {
			t.Errorf("no violation mentions %q; got:\n%s", w, strings.Join(got, "\n"))
		}
	}
}

// expectNone asserts a rule found nothing.
func expectNone(t *testing.T, got []string) {
	t.Helper()
	if len(got) > 0 {
		t.Errorf("unexpected violations:\n%s", strings.Join(got, "\n"))
	}
}

// testFixtureRule runs a fixture-backed repository rule over its clean and
// violating fixtures: the second and third subtests such a rule's test has.
func testFixtureRule(t *testing.T, check func(ctx context.Context, r repo) ([]string, error), good, bad, violation string) {
	t.Helper()
	t.Run("clean fixture", func(t *testing.T) {
		t.Parallel()
		got, err := check(t.Context(), fixture(good))
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	t.Run("violations", func(t *testing.T) {
		t.Parallel()
		got, err := check(t.Context(), fixture(bad))
		if err != nil {
			t.Fatal(err)
		}
		expectViolations(t, got, violation)
	})
}
