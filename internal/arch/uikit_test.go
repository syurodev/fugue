package arch

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strings"
	"testing"
)

// The kit's own rules (ui-kit UK-01, UK-03, F10.13): the app's screens
// assemble from the kit, the theme tokens and mdview only. Every package
// under ui/ that is not one of the three may import no widget/material, and
// every non-test source under ui/internal outside theme may write no color,
// dp or sp literal: sizes and colors come from the tokens.

// The subtest names the repo-targeted rules share.
const (
	caseRepository = "repository"
	caseClean      = "clean fixture"
	caseViolations = "violations"
)

// isKitDrawPackage reports whether rel is one of the packages allowed to
// draw with raw Gio primitives and hold geometry: the kit, the theme and
// mdview.
func isKitDrawPackage(rel string) bool {
	for _, p := range []string{"ui/internal/kit", "ui/internal/theme", "ui/internal/mdview"} {
		if rel == p || strings.HasPrefix(rel, p+"/") {
			return true
		}
	}
	return false
}

// checkOnlyKitThemeAndMdviewImportMaterial enforces TestOnlyKitThemeAndMdviewImportMaterial:
// no package under ui/ outside kit, theme and mdview imports
// gioui.org/widget/material (UK-01).
func checkOnlyKitThemeAndMdviewImportMaterial(ctx context.Context, r repo) ([]string, error) {
	pkgs, err := r.packages(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range pkgs {
		rel := p.rel()
		if rel != "ui" && !strings.HasPrefix(rel, "ui/") {
			continue
		}
		if isKitDrawPackage(rel) {
			continue
		}
		for _, imp := range slices.Compact(slices.Sorted(slices.Values(p.allImports()))) {
			if imp == "gioui.org/widget/material" {
				out = append(out, fmt.Sprintf("%s imports %s: only kit, theme and mdview draw with material", rel, imp))
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

func TestOnlyKitThemeAndMdviewImportMaterial(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		target     repo
		violations []string
	}{
		{name: caseRepository, target: repo{root: repoRoot}},
		{name: caseClean, target: fixture("uikit-material-good")},
		{
			name:   caseViolations,
			target: fixture("uikit-material-bad"),
			violations: []string{
				"ui/internal/appwin imports gioui.org/widget/material: only kit, theme and mdview draw with material",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := checkOnlyKitThemeAndMdviewImportMaterial(t.Context(), tc.target)
			if err != nil {
				t.Fatal(err)
			}
			if len(tc.violations) == 0 {
				expectNone(t, got)
				return
			}
			expectViolations(t, got, tc.violations...)
		})
	}
}

// colorImportName returns the local name a file binds "image/color" to, or
// "" when the file does not import it; a dot import hides which identifiers
// are color types and is a violation in itself.
func colorImportName(file *ast.File) string {
	for _, spec := range file.Imports {
		if spec.Path.Value != `"image/color"` {
			continue
		}
		if spec.Name == nil {
			return "color"
		}
		return spec.Name.Name
	}
	return ""
}

// colorLiteral reports a composite literal whose type is a color of the
// file's image/color import, whatever the local name is.
func colorLiteral(fset *token.FileSet, name string, file *ast.File) []string {
	local := colorImportName(file)
	if local == "" {
		return nil
	}
	if local == "." {
		return []string{name + ": dot-imports image/color; colors come from the theme tokens"}
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
			found = append(found, fmt.Sprintf("%s: image/color.%s literal at %s; colors come from the theme tokens",
				name, sel.Sel.Name, fset.Position(lit.Pos())))
		}
		return true
	})
	return found
}

// unitLiteral reports a call that writes a dp or sp value directly:
// unit.Dp(n) or unit.Sp(n) with a numeric literal argument. Calls with a
// computed argument — a window size clamped from the screen, a value
// measured from a token — are plumbing, not styling.
func unitLiteral(fset *token.FileSet, name string, file *ast.File) []string {
	var found []string
	ast.Inspect(file, func(n ast.Node) bool {
		call, isCall := n.(*ast.CallExpr)
		if !isCall {
			return true
		}
		sel, isSel := call.Fun.(*ast.SelectorExpr)
		if !isSel {
			return true
		}
		id, isID := sel.X.(*ast.Ident)
		if !isID || id.Name != "unit" || (sel.Sel.Name != "Dp" && sel.Sel.Name != "Sp") {
			return true
		}
		if len(call.Args) == 1 {
			if _, isLit := call.Args[0].(*ast.BasicLit); isLit {
				found = append(found, fmt.Sprintf("%s: unit.%s literal at %s; sizes come from the theme tokens",
					name, sel.Sel.Name, fset.Position(call.Pos())))
			}
		}
		return true
	})
	return found
}

// checkNoDpSpOrColorLiteralsOutsideTheme enforces TestNoDpSpOrColorLiteralsOutsideTheme:
// every non-test source under ui/internal outside the theme writes no color,
// dp or sp literal (UK-03, extending TestUiInternalDefinesNoColorsOutsideTheme).
// The kit and mdview scan too: they draw with raw Gio primitives, but every
// size and color they write still comes from the tokens.
func checkNoDpSpOrColorLiteralsOutsideTheme(r repo) ([]string, error) {
	files, err := r.files(".go")
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	var out []string
	for _, rel := range files {
		if !strings.HasPrefix(rel, "ui/internal/") || strings.HasSuffix(rel, "_test.go") {
			continue
		}
		if strings.HasPrefix(rel, "ui/internal/theme/") {
			continue
		}
		src, err := r.read(rel)
		if err != nil {
			return nil, err
		}
		file, err := parser.ParseFile(fset, rel, src, 0)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", rel, err)
		}
		out = append(out, colorLiteral(fset, rel, file)...)
		out = append(out, unitLiteral(fset, rel, file)...)
	}
	slices.Sort(out)
	return out, nil
}

func TestNoDpSpOrColorLiteralsOutsideTheme(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		target     repo
		violations []string
	}{
		{name: caseRepository, target: repo{root: repoRoot}},
		{name: caseClean, target: fixture("uikit-literals-good")},
		{
			name:   caseViolations,
			target: fixture("uikit-literals-bad"),
			violations: []string{
				"ui/internal/appwin/screens.go: unit.Dp literal",
				"ui/internal/appwin/screens.go: unit.Sp literal",
				"ui/internal/appwin/screens.go: image/color.NRGBA literal",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := checkNoDpSpOrColorLiteralsOutsideTheme(tc.target)
			if err != nil {
				t.Fatal(err)
			}
			if len(tc.violations) == 0 {
				expectNone(t, got)
				return
			}
			expectViolations(t, got, tc.violations...)
		})
	}
}
