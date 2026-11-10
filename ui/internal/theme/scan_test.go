package theme_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// colorImportName returns the local name a file binds "image/color" to, or
// "" when the file does not import it.
func colorImportName(file *ast.File) string {
	for _, spec := range file.Imports {
		if spec.Path.Value != `"image/color"` {
			continue
		}
		if spec.Name == nil {
			return "color"
		}
		if spec.Name.Name == "." {
			// A dot import hides which identifiers are color types; treat it
			// as a violation in itself.
			return "."
		}
		return spec.Name.Name
	}
	return ""
}

// colorLiterals reports every composite literal in a parsed file whose type
// comes from the file's "image/color" import, whatever the local name is —
// NRGBA, RGBA, Gray and the rest. Colors may only come from this package.
func colorLiterals(fset *token.FileSet, name string, file *ast.File) []string {
	local := colorImportName(file)
	if local == "" {
		return nil
	}
	if local == "." {
		return []string{fmt.Sprintf("%s: %s: dot-imports image/color; colors come from ui/internal/theme",
			name, fset.Position(file.Pos()))}
	}
	var found []string
	ast.Inspect(file, func(n ast.Node) bool {
		lit, isLit := n.(*ast.CompositeLit)
		if !isLit {
			return true
		}
		sel, isSel := lit.Type.(*ast.SelectorExpr)
		if !isSel {
			return true
		}
		if id, isID := sel.X.(*ast.Ident); isID && id.Name == local {
			found = append(found, fmt.Sprintf("%s: image/color.%s literal at %s; colors come from ui/internal/theme",
				name, sel.Sel.Name, fset.Position(lit.Pos())))
		}
		return true
	})
	return found
}

// reportColorLiteralsInDir parses dir's non-test sources and fails the test
// on every image/color composite literal found by colorLiterals.
func reportColorLiteralsInDir(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	scanned := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		scanned++
		for _, msg := range colorLiterals(fset, path, file) {
			t.Error(msg)
		}
	}
	if scanned == 0 {
		t.Fatalf("no non-test sources scanned in %s; the scan is not looking at a real package", dir)
	}
}

// TestUiInternalDefinesNoColorsOutsideTheme keeps every color definition in
// this package: no non-test source file anywhere under ui/internal except
// ui/internal/theme itself may construct an image/color value, so the
// screens of F10.3 onward can only use the shared tokens. The scan parses
// sources at test time and touches no gate file.
func TestUiInternalDefinesNoColorsOutsideTheme(t *testing.T) {
	t.Parallel()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this package's directory")
	}
	internal := filepath.Dir(filepath.Dir(thisFile))
	entries, err := os.ReadDir(internal)
	if err != nil {
		t.Fatalf("read ui/internal: %v", err)
	}
	scanned := 0
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "theme" {
			continue
		}
		reportColorLiteralsInDir(t, filepath.Join(internal, entry.Name()))
		scanned++
	}
	if scanned == 0 {
		t.Fatal("no sibling packages scanned; the scan is not looking under ui/internal")
	}
}

// TestScanCatchesAliasedColorLiterals pins the scanner itself on a sample
// source: an aliased import (`c "image/color"`) and a `c.RGBA{}` literal
// must be caught even though the package name is not "color", while a same
// named selector from another package must not be.
func TestScanCatchesAliasedColorLiterals(t *testing.T) {
	t.Parallel()
	src := `package sample

import (
	c "image/color"
	"other/color"
)

var a = c.RGBA{}
var b = color.NRGBA{}
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "sample.go", src, 0)
	if err != nil {
		t.Fatalf("parse the sample source: %v", err)
	}
	found := colorLiterals(fset, "sample.go", file)
	if len(found) != 1 || !strings.Contains(found[0], "image/color.RGBA") {
		t.Fatalf("scanner reported %v, want exactly the aliased c.RGBA literal", found)
	}
}
