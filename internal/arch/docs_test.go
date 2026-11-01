package arch

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const goleakPath = "go.uber.org/goleak"

func parseFile(file string) (*ast.File, error) {
	src, err := os.ReadFile(filepath.Clean(file))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", file, err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), file, src, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", file, err)
	}
	return f, nil
}

func allGenerated(p *goPackage) (bool, error) {
	if len(p.GoFiles) == 0 {
		return false, nil
	}
	for _, name := range p.GoFiles {
		f, err := parseFile(filepath.Join(p.Dir, name))
		if err != nil {
			return false, err
		}
		if !ast.IsGenerated(f) {
			return false, nil
		}
	}
	return true, nil
}

// checkPackageDocs enforces TestPackageDocs: every package has a doc.go whose
// package comment states its owner and its allowed callers.
func checkPackageDocs(ctx context.Context, r repo) ([]string, error) {
	pkgs, err := r.packages(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range pkgs {
		generated, err := allGenerated(p)
		if err != nil {
			return nil, err
		}
		if generated {
			continue
		}
		problem, err := docProblem(p)
		if err != nil {
			return nil, err
		}
		if problem != "" {
			out = append(out, fmt.Sprintf("%s: %s", p.rel(), problem))
		}
	}
	slices.Sort(out)
	return out, nil
}

func docProblem(p *goPackage) (string, error) {
	file := filepath.Join(p.Dir, "doc.go")
	if _, err := os.Stat(file); errors.Is(err, fs.ErrNotExist) {
		return "has no doc.go", nil
	} else if err != nil {
		return "", fmt.Errorf("stat %s: %w", file, err)
	}
	f, err := parseFile(file)
	if err != nil {
		return "", err
	}
	if f.Doc == nil {
		return "doc.go has no package comment", nil
	}
	text := f.Doc.Text()
	switch {
	case !strings.Contains(text, "Owner:"):
		return "package comment does not state `Owner:`", nil
	case !strings.Contains(strings.ToLower(text), "callers"):
		return "package comment does not state its allowed callers", nil
	default:
		return "", nil
	}
}

// checkGoleak enforces TestGoleakEverywhere: every package with tests has a
// TestMain that calls goleak.VerifyTestMain.
func checkGoleak(ctx context.Context, r repo) ([]string, error) {
	pkgs, err := r.packages(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range pkgs {
		files := slices.Concat(p.TestGoFiles, p.XTestGoFiles)
		if len(files) == 0 {
			continue
		}
		ok, err := anyVerifiesMain(p.Dir, files)
		if err != nil {
			return nil, err
		}
		if !ok {
			out = append(out, p.rel()+": tests without a TestMain that calls goleak.VerifyTestMain")
		}
	}
	slices.Sort(out)
	return out, nil
}

func anyVerifiesMain(dir string, files []string) (bool, error) {
	for _, name := range files {
		f, err := parseFile(filepath.Join(dir, name))
		if err != nil {
			return false, err
		}
		if importsGoleak(f) && callsVerifyTestMain(f) {
			return true, nil
		}
	}
	return false, nil
}

func importsGoleak(f *ast.File) bool {
	return slices.ContainsFunc(f.Imports, func(s *ast.ImportSpec) bool {
		p, err := strconv.Unquote(s.Path.Value)
		return err == nil && p == goleakPath
	})
}

func callsVerifyTestMain(f *ast.File) bool {
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "TestMain" || fn.Recv != nil || fn.Body == nil {
			continue
		}
		found := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "VerifyTestMain" {
					found = true
				}
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}

func TestPackageDocs(t *testing.T) {
	t.Parallel()
	t.Run("repository", func(t *testing.T) {
		t.Parallel()
		got, err := checkPackageDocs(t.Context(), repo{root: repoRoot})
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	t.Run("clean fixture", func(t *testing.T) {
		t.Parallel()
		got, err := checkPackageDocs(t.Context(), fixture("docs-good"))
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	t.Run("violations", func(t *testing.T) {
		t.Parallel()
		got, err := checkPackageDocs(t.Context(), fixture("docs-bad"))
		if err != nil {
			t.Fatal(err)
		}
		expectViolations(t, got,
			"internal/nodoc: has no doc.go",
			"internal/emptydoc: doc.go has no package comment",
			"internal/noowner: package comment does not state `Owner:`",
			"internal/noreader: package comment does not state its allowed callers",
		)
		for _, v := range got {
			if strings.Contains(v, "internal/gen") {
				t.Errorf("generated code is exempt, got %s", v)
			}
		}
	})
}

func TestGoleakEverywhere(t *testing.T) {
	t.Parallel()
	t.Run("repository", func(t *testing.T) {
		t.Parallel()
		got, err := checkGoleak(t.Context(), repo{root: repoRoot})
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	t.Run("clean fixture", func(t *testing.T) {
		t.Parallel()
		got, err := checkGoleak(t.Context(), fixture("goleak-good"))
		if err != nil {
			t.Fatal(err)
		}
		expectNone(t, got)
	})
	t.Run("violations", func(t *testing.T) {
		t.Parallel()
		got, err := checkGoleak(t.Context(), fixture("goleak-bad"))
		if err != nil {
			t.Fatal(err)
		}
		expectViolations(t, got, "internal/nomain: tests without a TestMain", "internal/wrongmain: tests without a TestMain")
		for _, v := range got {
			if strings.Contains(v, "internal/notests") {
				t.Errorf("a package without tests needs no TestMain, got %s", v)
			}
		}
	})
}
