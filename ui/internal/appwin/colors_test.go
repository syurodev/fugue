package appwin

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// nonTestGoSources returns the .go files of dir that are not test files.
func nonTestGoSources(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read the package directory: %v", err)
	}
	var names []string
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() && strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			names = append(names, name)
		}
	}
	return names
}

// reportColorNRGBALiterals fails the test for every color.NRGBA composite
// literal in one parsed file: colors may only come from ui/internal/theme.
func reportColorNRGBALiterals(t *testing.T, fset *token.FileSet, name string, file *ast.File) {
	t.Helper()
	ast.Inspect(file, func(n ast.Node) bool {
		lit, isLit := n.(*ast.CompositeLit)
		if !isLit {
			return true
		}
		sel, isSel := lit.Type.(*ast.SelectorExpr)
		if !isSel {
			return true
		}
		if id, isID := sel.X.(*ast.Ident); isID && id.Name == "color" && sel.Sel.Name == "NRGBA" {
			t.Errorf("%s: color.NRGBA literal at %s; colors come from ui/internal/theme (F10.2c)",
				name, fset.Position(lit.Pos()))
		}
		return true
	})
}

// TestAppwinHasNoColorLiterals keeps every color definition in
// ui/internal/theme (F10.2c): no non-test source file of this package may
// construct an image/color NRGBA, so window and status-bar colors can only
// come from the token layer. The scan parses this package's own sources at
// test time; it touches no gate file.
func TestAppwinHasNoColorLiterals(t *testing.T) {
	t.Parallel()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this package's directory")
	}
	dir := filepath.Dir(thisFile)
	names := nonTestGoSources(t, dir)
	if len(names) == 0 {
		t.Fatal("no non-test sources scanned; the test is not looking at this package")
	}
	fset := token.NewFileSet()
	for _, name := range names {
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		reportColorNRGBALiterals(t, fset, name, file)
	}
}
